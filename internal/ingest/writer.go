package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/rollups"
)

// writer applies one canonical event to one *sql.Tx. The writer is
// stateless — every method is a function of (tx, event, sourceID,
// sourceFormat). State that spans events within a batch (dirty sets,
// the max-SourceSeq observability counter) is owned by the worker that
// drives this writer.
type writer struct {
	sourceID     string
	sourceFormat string
	location     string
	pricer       Pricer
	catalog      *catalogWriter
	// stageTiming, when non-nil, accumulates per-sub-stage wall time for the
	// read-model refresh (rollups/fts/aggregates) so the flush breakdown can
	// attribute read-model cost (SOW-0118). Borrowed from the owning worker
	// per flush; nil in tests.
	stageTiming *flushStageTiming
	// dirty tracks session/turn IDs touched by the current batch so the
	// aggregate refresh runs over the bounded set. Cleared after each
	// flush.
	dirtySessionIDs map[string]struct{}
	dirtyTurnIDs    map[string]struct{}
	// batchMaxSeq tracks the maximum SourceSeq applied in the current
	// batch so the worker can advance the observability counter
	// (source_progress.last_seq) atomically. NOT a dedup gate.
	batchMaxSeq uint64
	// lastCursor records the most recent SourceProgressEvent.Cursor
	// applied in the current batch so source_progress.cursor advances
	// at commit.
	lastCursor string
	// hasCursor flips to true when at least one SourceProgressEvent was
	// observed in the current batch; the worker uses this to decide
	// whether to write cursor at flush.
	hasCursor bool
	// affectedSessionIDs records canonical session IDs touched by this
	// batch. emitNotify (notify_producer.go) writes one session_changed
	// notify row per id so the serve poller can fan a change out to
	// matching SSE subscriptions. Cleared after each flush.
	affectedSessionIDs map[string]struct{}
	// sourceStatusChanged flips to true when this batch changed the
	// source's parse_errors count or enabled flag (the only mutation
	// today is the parse_errors bump in bumpSourceErrorCounter, shared
	// by applySourceError and emitPricingMiss). emitNotify writes one
	// source_status_changed notify row when set. Cleared after each
	// flush. See ingester.md §Notify Channel.
	sourceStatusChanged bool
	// readModelRepairRequested flips when the committed batch records durable
	// read_model_state='repair_pending'. The worker promotes it only after
	// commit so the supervisor never repairs uncommitted debt.
	readModelRepairRequested bool
	// pricingMissDedup tracks (provider, model, missKind) tuples for
	// which a SourceError WRN has already been emitted AND DURABLY
	// COMMITTED for THIS SOURCE, across the lifetime of the worker
	// (cross-batch). Per pricing.md §"Temporal resolution algorithm"
	// misses are deduped per (sourceID, provider, model, missKind) —
	// one warning, not one per batch. The map is
	// owned by the writer and survives resetBatch(). Memory bound:
	// per source × per unique (provider, model, missKind) — typically
	// a handful of entries.
	//
	// Keys land here only after the surrounding batch commits — see
	// pendingMissDedup below and promotePendingMissDedup. Marking on
	// emit (pre-commit) would suppress all future warnings for the
	// same tuple even if the warning row was rolled back.
	pricingMissDedup map[pricingMissKey]struct{}
	// pendingMissDedup buffers (provider, model, missKind) keys whose
	// WRN row + parse_errors bump have been INSERTed within the
	// current open transaction but not yet committed. On successful
	// commit the worker calls promotePendingMissDedup which merges
	// these into pricingMissDedup. On rollback or error the worker
	// calls resetBatch which discards them, so the next batch with
	// the same tuple re-emits the (now-missing) warning.
	pendingMissDedup map[pricingMissKey]struct{}
	// batchObservabilityErrs collects errors from best-effort
	// observability writes (e.g. emitPricingMiss) that we do NOT want
	// to abort the surrounding op write for. The worker drains this
	// slice at flush time and surfaces each entry through its logger,
	// satisfying the project "no silent failures" rule without losing
	// the priced op. Cleared at resetBatch().
	batchObservabilityErrs []error
	// dirtyRollupBuckets is the set of HOUR buckets (rollups.BucketTS(ts,
	// Hourly)) whose rollup inputs are pending materialization. refreshRollups
	// (rollup_refresh.go) recomputes each CLOSED hour from this set in the same tx,
	// before commit, and REMOVES each bucket it materializes; a bucket still OPEN at
	// refresh time is RETAINED (the open hour is never materialized — the query
	// layer live-folds it). The matching DAY is tracked separately in
	// dirtyRollupDays (NOT derived from this set), so the two granularities carry
	// independently. Marked by markDirtyRollupBucket from the three rollup-affecting
	// apply paths (SessionStarted, OpStarted, OpFinalized's persisted start).
	//
	// CARRY-FORWARD (NOT per-batch): this set is writer-lifetime state, NOT
	// cleared in resetBatch. A bucket dirtied while it was the open hour stays
	// in the set across batches and is materialized on the first refresh after
	// it closes — without this, a bucket whose ops all arrive during its own
	// open hour would be skipped while open and then never re-marked, leaving
	// the closed bucket permanently un-materialized (round-7 P1). Memory stays
	// bounded: refreshRollups removes every closed bucket it materializes, so
	// only open/recent-pending hours remain.
	dirtyRollupBuckets map[int64]struct{}
	// rollupTouchedThisBatch is true when markDirtyRollupBucket fired during
	// THIS batch (a new op/session marked a bucket). Cleared in resetBatch.
	// emitNotify uses it (∪ rollupMaterializedThisRefresh) instead of
	// len(dirtyRollupBuckets) so a carried-open pending bucket does NOT make
	// stats_invalidated fire on every batch (the original semantics: fire when
	// this batch actually touched a rollup input, not when one is merely
	// pending).
	rollupTouchedThisBatch bool
	// rollupMaterializedThisRefresh is true when refreshRollups materialized
	// (recomputed) at least one bucket during the current batch OR a dedicated
	// idle refresh-only pass. Cleared in resetBatch. The second half of the
	// stats_invalidated trigger, so an idle pass that closes a carried bucket
	// still emits exactly one notify even though no event marked a bucket.
	rollupMaterializedThisRefresh bool
	// pendingMaterializedBuckets buffers HOUR buckets that refreshRollups
	// materialized WITHIN the current open tx but has NOT yet removed from the
	// carried dirtyRollupBuckets set. The removal is deferred to AFTER commit
	// (promoteMaterializedRollupBuckets, called by worker.flush /
	// refreshRollupsOnly post-commit) — mirroring pendingMissDedup. If the tx
	// rolls back, resetBatch discards this map so the bucket stays CARRIED and
	// is retried next pass: a materialized-then-rolled-back idle refresh has no
	// events to re-ingest (the bucket's ops were committed by a PRIOR batch), so
	// removing it from the set on the in-memory delete alone would lose a closed
	// bucket whose DB row never committed — the very round-7 P1 undercount.
	pendingMaterializedBuckets map[int64]struct{}
	// dirtyRollupDays is the DAILY twin of dirtyRollupBuckets: the set of DAY
	// buckets (rollups.BucketTS(ts, Daily)) whose rollup_daily row is pending
	// materialization. refreshRollups recomputes each CLOSED day from this set in
	// the same tx, before commit, and stages its removal; a day still OPEN at
	// refresh time is RETAINED (the open day is never materialized — the query
	// layer live-folds it). Marked by markDirtyRollupBucket alongside the hour, so
	// every rollup-affecting event marks BOTH its hour and its day.
	//
	// CARRY-FORWARD, INDEPENDENTLY OF HOURS (round-8 P1): days are NOT derived
	// per-refresh from the dirty hours. A day touched while it was still open stays
	// carried across batches even AFTER all of its hours have materialized and left
	// dirtyRollupBuckets, and is materialized on the first refresh after the DAY
	// closes. Without a separate carried day set, a day whose ops all arrive before
	// it closes (e.g. within one still-open day, after which its hours materialize
	// and drain the carried hour set) would be stranded once the day closes with no
	// further events — the daily sibling of the round-7 hourly open→closed gap,
	// silently undercounting the all-sources bucket=daily fast path. Memory stays
	// bounded: refreshRollups removes every closed day it materializes, so only
	// open/recent-pending days remain.
	dirtyRollupDays map[int64]struct{}
	// pendingMaterializedDays buffers DAY buckets that refreshRollups materialized
	// WITHIN the current open tx but has NOT yet removed from the carried
	// dirtyRollupDays set. The DAILY twin of pendingMaterializedBuckets: the removal
	// is deferred to AFTER commit (promoteMaterializedRollupBuckets) so a
	// materialized-then-rolled-back day stays CARRIED (resetBatch discards this map
	// on rollback) and is retried — an idle refresh whose commit fails would
	// otherwise lose a closed day whose rollup_daily row never landed.
	pendingMaterializedDays map[int64]struct{}
	// dirtyOpIDs is the set of op ids whose fts_ops searchable text or
	// error_text this batch changed (marked by BOTH applyOpStarted and
	// applyOpFinalized — either write can alter the indexed name/model/
	// provider/tool_namespace or the error text). refreshFTS (fts_refresh.go)
	// rebuilds each one from its FINAL persisted ops row in the same tx, before
	// commit, so the FTS index is byte-identical to BackfillFTS over the same
	// data. Cleared in resetBatch. fts_logs needs no dirty set: logs are
	// append-only and indexed inline in applyLogEntry.
	dirtyOpIDs map[string]struct{}
	// fts5IndexLogs gates fts_logs population for THIS source: when false the
	// applyLogEntry FTS insert is skipped (data-model.md §Full-text search).
	// fts_ops is ALWAYS indexed regardless of this flag. Set per batch by
	// worker.flush from the resolved worker.fts5IndexLogs (mirrors wr.now), so
	// newWriter's signature stays unchanged and the field has a reader.
	fts5IndexLogs bool
	// now supplies the wall-clock cutoff refreshRollups reads to pick its
	// open hour/day. Injectable for deterministic tests (so the incremental
	// path and BackfillRollups share one cutoff — the byte-diff gate's
	// premise); defaults to defaultNow.
	now func() int64
	// maxRollupRowsPerBucket overrides the R1 per-(bucket, source_format,
	// dimension) collapse cap for the incremental refresh path
	// (refreshRollups → rollups.Rollup). Zero means "use the rollups-package
	// default" (2000, data-model.md §R1 safety bound) — so production
	// behaviour is unchanged. Tests that exercise the __other__ tail-collapse
	// set this to a small value so they can use a small high-cardinality batch
	// instead of 2 000+ real events (SOW-0062). The SAME value MUST be set on
	// the matching BackfillRollups option when a test compares the two paths
	// (the refresh≡backfill byte-parity invariant); production leaves it 0.
	maxRollupRowsPerBucket int
	// deferReadModels points at the source-scoped startup Scan deferral flag.
	// When true, refreshBatchReadModels skips refreshRollups + refreshFTS.
	deferReadModels *atomic.Bool
	// readModelRebuildActive points at the ingester-wide full rebuild flag.
	// When true, Tail batches keep primary rows current but defer derived
	// FTS/rollup refresh so they cannot race the truncate/rebuild window.
	readModelRebuildActive *atomic.Bool
}

// logEntryOnConflict is the ON CONFLICT clause appended to every
// log_entries INSERT. The conflict target repeats the expression list of
// idx_log_entries_identity (migration 0003) so a re-emitted row collides
// instead of duplicating. The COALESCE sentinels make NULL-owner rows
// (source-level parse errors / pricing misses, or session-level logs)
// match on replay — raw SQL NULLs are distinct in a UNIQUE index. turn_id
// is part of the target: turn-scoped logs with op_id NULL but distinct
// turns must NOT collapse into one row. The expression list MUST match
// idx_log_entries_identity character-for-character (SQLite requires the
// conflict target to match the index expression). See ingester.md §Dedup
// and Idempotency.
//
// INVARIANT: this conflict target MUST list every persisted log_entries
// content column (everything except the autoincrement id). A log row is a
// duplicate iff it is byte-identical; omitting any persisted column
// reintroduces false-dedup data loss (see SOW-0015 iter-2 turn_id,
// iter-4 extras_json). When adding a column to the log_entries INSERT,
// add it here AND to idx_log_entries_identity in the same commit.
const logEntryOnConflict = `
ON CONFLICT (
    COALESCE(session_id, ''),
    COALESCE(source_id, ''),
    COALESCE(op_id, ''),
    COALESCE(turn_id, ''),
    ts, severity, source, message,
    COALESCE(extras_json, '')
) DO NOTHING`

// pricingMissKey identifies a pricing-miss SourceError dedup slot.
// Lower-cased so case variants (e.g. "Anthropic" vs "anthropic") fold
// to one warning per source-batch.
type pricingMissKey struct {
	provider string
	model    string
	kind     string
}

// aiViewerStashKeys are the resolver-owned join keys under `$.aiViewer` that must
// survive a stash-free re-emit (SOW-0003 P2.6d/P1.7b/P2.7c). `toolUseId` is the
// claude-code meta-independent op→child join key; `childNativeId` is the
// ChildSessionNativeID stash used by every adapter that knows the child native id
// at op-write time. Both are grafted back PER-KEY (not as a whole `$.aiViewer`
// object) because the sessions writer ALWAYS rewrites `$.aiViewer` with
// `parentNativeId`/`rootNativeId`, so the re-emit's `$.aiViewer` is present-but-
// incomplete — a whole-object graft would never fire for sessions.
var aiViewerStashKeys = []string{"toolUseId", "childNativeId"}

var sessionSyntheticLinkageKeys = []string{"parentNativeId", "parentOpKey", "rootNativeId", "toolUseId", "childNativeId"}

// graftAiViewerExtras returns the ON CONFLICT extras_json expression shared by the
// sessions and ops UPSERT paths (applySessionStarted, applyOpStarted). Both are
// full re-emits of the same natural-identity row, so the new (`excluded`) extras
// REPLACE the existing extras WHOLESALE — EXCEPT each resolver join key under
// `$.aiViewer` (aiViewerStashKeys), which is grafted back from the existing row
// whenever the re-emit omits it.
//
// Why a per-key graft and NOT json_patch (SOW-0003 P2.7c, RFC 7386): SQLite's
// json_patch treats a JSON `null` VALUE as a DELETE directive, and adapters copy
// arbitrary source attributes into op extras (aiagent_v3 ops.go: `extras["attr."+k]
// = v`). A replay whose extras legitimately carry `{"attr.x":null}` would, under
// json_patch, DELETE key `attr.x` — a shared-ingester data-loss regression. Taking
// `excluded` wholesale and json_set-ing only the named stash keys never deletes a
// key for a null value (json_set with a NULL value would CREATE a null-valued key,
// which is why the graft is GUARDED to fire only when the existing row actually has
// the key and the re-emit lacks it — it never introduces a spurious null).
//
// existingCol is the existing-row extras_json column reference (e.g.
// "ops.extras_json" or "sessions.extras_json"); the new value is always
// `excluded.extras_json`.
//
// Shape (one graftOne layer per stash key, nested over `excluded.extras_json`):
//
//	CASE
//	  WHEN excluded.extras_json IS NULL THEN <stashOnly>   -- re-emit carried none; keep ONLY the stash
//	  WHEN <existing> IS NULL           THEN excluded.extras_json  -- nothing to preserve
//	  ELSE graftKey(graftKey(excluded.extras_json, toolUseId), childNativeId)
//	END
//
// where graftKey(base, K) = CASE WHEN <existing>.$.aiViewer.K NOT NULL AND
// base.$.aiViewer.K IS NULL THEN json_set(base, '$.aiViewer.K', <existing>.$.aiViewer.K)
// ELSE base END (json_set auto-creates `$.aiViewer` when base lacks it, e.g. ops).
//
// The NULL-excluded branch (SOW-0003 P2.8) does NOT return the whole existing blob —
// that would stale-preserve every non-`aiViewer` key the re-emit deliberately dropped
// (e.g. an aiagent_v3 op's copied `attr.*`), contradicting "excluded wins wholesale".
// Instead <stashOnly> = graftKeyOnto(graftKeyOnto(json_object(), toolUseId),
// childNativeId) builds a fresh object holding ONLY the present `$.aiViewer.<key>`
// values from the existing row, collapsed to SQL NULL when none were grafted (so a
// no-stash op re-emit with NULL extras yields NULL, not an empty `{}`).
func graftAiViewerExtras(existingCol string) string {
	base := "excluded.extras_json"
	for _, key := range aiViewerStashKeys {
		base = graftAiViewerKey(base, existingCol, key)
	}
	stashOnly := "json_object()"
	for _, key := range aiViewerStashKeys {
		stashOnly = graftAiViewerKeyOnto(stashOnly, existingCol, key)
	}
	return fmt.Sprintf(`CASE
    WHEN excluded.extras_json IS NULL THEN (CASE WHEN json_extract((%[3]s), '$.aiViewer') IS NULL THEN NULL ELSE (%[3]s) END)
    WHEN %[1]s IS NULL THEN excluded.extras_json
    ELSE (%[2]s)
END`, existingCol, base, stashOnly)
}

// graftAiViewerKeyOnto returns an expression that grafts the existing row's
// `$.aiViewer.<key>` onto base whenever the existing row has it (base is the fresh
// stash-only accumulator, so there is no "base already has it" guard — unlike
// graftAiViewerKey). json_set auto-creates `$.aiViewer` on the empty json_object().
// Used only by the NULL-excluded branch of graftAiViewerExtras (SOW-0003 P2.8).
func graftAiViewerKeyOnto(base, existingCol, key string) string {
	path := "'$.aiViewer." + key + "'"
	return fmt.Sprintf(`CASE WHEN json_extract(%[1]s, %[3]s) IS NOT NULL
        THEN json_set(%[2]s, %[3]s, json_extract(%[1]s, %[3]s))
        ELSE %[2]s END`, existingCol, base, path)
}

// graftAiViewerKey returns an expression that grafts the existing row's
// `$.aiViewer.<key>` onto base ONLY when the existing row has it and base lacks it,
// otherwise base unchanged. See graftAiViewerExtras.
func graftAiViewerKey(base, existingCol, key string) string {
	path := "'$.aiViewer." + key + "'"
	return fmt.Sprintf(`CASE WHEN json_extract(%[1]s, %[3]s) IS NOT NULL AND json_extract(%[2]s, %[3]s) IS NULL
        THEN json_set(%[2]s, %[3]s, json_extract(%[1]s, %[3]s))
        ELSE %[2]s END`, existingCol, base, path)
}

func preserveRealSessionMetadataPredicate() string {
	return `json_extract(excluded.extras_json, '$.synthesizedFromParent') = 1
        AND json_type(sessions.extras_json, '$.capturePayloads') IS NOT NULL`
}

func sessionStartedExtrasConflictExpression() string {
	return fmt.Sprintf(`CASE
    WHEN %[1]s THEN (%[2]s)
    ELSE (%[3]s)
END`,
		preserveRealSessionMetadataPredicate(),
		graftIncomingSessionLinkageKeys("sessions.extras_json", "excluded.extras_json"),
		graftAiViewerExtras("sessions.extras_json"))
}

func sessionStartedParentIDConflictExpression() string {
	return fmt.Sprintf(`CASE
    WHEN %[1]s THEN sessions.parent_session_id
    ELSE COALESCE(sessions.parent_session_id, excluded.parent_session_id)
END`, syntheticConflictsWithExistingSessionLineage("parentNativeId"))
}

func sessionStartedRootIDConflictExpression() string {
	return fmt.Sprintf(`CASE
    WHEN %[1]s THEN sessions.root_session_id
    ELSE excluded.root_session_id
END`, syntheticConflictsWithExistingSessionLineage("rootNativeId"))
}

func syntheticConflictsWithExistingSessionLineage(key string) string {
	path := "'$.aiViewer." + key + "'"
	return fmt.Sprintf(`%[1]s
        AND json_extract(sessions.extras_json, %[2]s) IS NOT NULL
        AND json_extract(sessions.extras_json, %[2]s) <> ''
        AND (
            json_extract(excluded.extras_json, %[2]s) IS NULL
            OR json_extract(excluded.extras_json, %[2]s) = ''
            OR json_extract(excluded.extras_json, %[2]s) <> json_extract(sessions.extras_json, %[2]s)
        )`, preserveRealSessionMetadataPredicate(), path)
}

func graftIncomingSessionLinkageKeys(base, incoming string) string {
	out := base
	for _, key := range sessionSyntheticLinkageKeys {
		out = graftIncomingSessionLinkageKey(out, incoming, key)
	}
	return out
}

func graftIncomingSessionLinkageKey(base, incoming, key string) string {
	path := "'$.aiViewer." + key + "'"
	return fmt.Sprintf(`CASE WHEN json_extract(%[2]s, %[3]s) IS NOT NULL
        AND json_extract(%[2]s, %[3]s) <> ''
        AND (json_extract(%[1]s, %[3]s) IS NULL OR json_extract(%[1]s, %[3]s) = '')
        THEN json_set(%[1]s, %[3]s, json_extract(%[2]s, %[3]s))
        ELSE %[1]s END`, base, incoming, path)
}

func newWriter(sourceID, sourceFormat, location string, pricer Pricer) *writer {
	return &writer{
		sourceID:                   sourceID,
		sourceFormat:               sourceFormat,
		location:                   location,
		pricer:                     pricer,
		catalog:                    newCatalogWriter(pricer),
		dirtySessionIDs:            make(map[string]struct{}),
		dirtyTurnIDs:               make(map[string]struct{}),
		affectedSessionIDs:         make(map[string]struct{}),
		pricingMissDedup:           make(map[pricingMissKey]struct{}),
		pendingMissDedup:           make(map[pricingMissKey]struct{}),
		dirtyRollupBuckets:         make(map[int64]struct{}),
		pendingMaterializedBuckets: make(map[int64]struct{}),
		dirtyRollupDays:            make(map[int64]struct{}),
		pendingMaterializedDays:    make(map[int64]struct{}),
		dirtyOpIDs:                 make(map[string]struct{}),
		now:                        defaultNow,
	}
}

// resetBatch clears per-batch state. Called by the worker on EVERY
// flush() exit (commit OR rollback) so the writer can be re-used for
// the next batch. pricingMissDedup is deliberately NOT cleared — it
// lives for the lifetime of the worker so an unknown (provider, model)
// emits one warning per source, not one per batch (pricing.md
// §"Temporal resolution algorithm" — "deduped per
// (sourceID, provider, model, missKind)").
//
// pendingMissDedup IS cleared here. On rollback this drops the
// uncommitted dedup intentions so the next batch with the same
// (provider, model, missKind) re-emits the (now-missing) warning. On
// commit, promotePendingMissDedup runs FIRST (in worker.flush after
// tx.Commit()) so the entries are moved into pricingMissDedup before
// resetBatch wipes the pending map — see promotePendingMissDedup.
func (w *writer) resetBatch() {
	clear(w.dirtySessionIDs)
	clear(w.dirtyTurnIDs)
	clear(w.affectedSessionIDs)
	clear(w.pendingMissDedup)
	// dirtyRollupBuckets is deliberately NOT cleared here — it is writer-lifetime
	// carry-forward state. Only refreshRollups removes a bucket, and only after it
	// materializes it (once the bucket closes) AND the tx commits (via
	// promoteMaterializedRollupBuckets). A bucket still open at refresh time stays
	// carried to the next batch; clearing it here would re-introduce the round-7
	// P1 bug (an open-hour bucket forgotten before it could materialize).
	//
	// pendingMaterializedBuckets IS cleared here. On rollback this discards the
	// not-yet-committed removals so the buckets stay carried and are retried; on
	// commit, promoteMaterializedRollupBuckets runs FIRST (post-commit, before the
	// caller's resetBatch) and applies the removals to dirtyRollupBuckets.
	clear(w.pendingMaterializedBuckets)
	// dirtyRollupDays is the DAILY twin of dirtyRollupBuckets and is likewise NOT
	// cleared here — it is writer-lifetime carry-forward state (round-8 P1). Only
	// refreshRollups removes a day, and only after it materializes it (once the day
	// closes) AND the tx commits (via promoteMaterializedRollupBuckets).
	// pendingMaterializedDays IS cleared here, mirroring pendingMaterializedBuckets:
	// rollback discards the staged day removals so they stay carried and are
	// retried; commit promotes them first.
	clear(w.pendingMaterializedDays)
	clear(w.dirtyOpIDs)
	w.batchMaxSeq = 0
	w.lastCursor = ""
	w.hasCursor = false
	w.sourceStatusChanged = false
	w.readModelRepairRequested = false
	// Per-batch rollup notify signals reset every batch; the carried
	// dirtyRollupBuckets set (above) does not.
	w.rollupTouchedThisBatch = false
	w.rollupMaterializedThisRefresh = false
	w.batchObservabilityErrs = w.batchObservabilityErrs[:0]
}

// promotePendingMissDedup merges per-batch pending dedup keys into the
// lifetime dedup map. Called by worker.flush AFTER tx.Commit() succeeds
// so a rolled-back warning never silences future warnings. The pending
// map is left intact (resetBatch clears it next) so
// this method is idempotent against repeated calls.
func (w *writer) promotePendingMissDedup() {
	for k := range w.pendingMissDedup {
		w.pricingMissDedup[k] = struct{}{}
	}
}

// promoteMaterializedRollupBuckets removes the buckets refreshRollups
// materialized in the just-committed tx from the carried sets: HOUR buckets from
// dirtyRollupBuckets and DAY buckets from dirtyRollupDays. Called by worker.flush /
// worker.refreshRollupsOnly AFTER tx.Commit() succeeds — a rolled-back
// materialization therefore leaves its bucket CARRIED (retried next pass),
// mirroring promotePendingMissDedup. The pending maps are left intact (resetBatch
// clears them next), so this is idempotent against repeated calls.
func (w *writer) promoteMaterializedRollupBuckets() {
	for h := range w.pendingMaterializedBuckets {
		delete(w.dirtyRollupBuckets, h)
	}
	for d := range w.pendingMaterializedDays {
		delete(w.dirtyRollupDays, d)
	}
}

// drainObservabilityErrs returns and clears the per-batch slice of
// best-effort observability errors so the worker can surface them
// through its logger after a successful commit. Errors collected here
// are NOT propagated as op-write failures — they describe a missed
// observability hook (e.g. pricing-miss WRN insert), not a data
// integrity defect.
func (w *writer) drainObservabilityErrs() []error {
	if len(w.batchObservabilityErrs) == 0 {
		return nil
	}
	out := make([]error, len(w.batchObservabilityErrs))
	copy(out, w.batchObservabilityErrs)
	w.batchObservabilityErrs = w.batchObservabilityErrs[:0]
	return out
}

// apply dispatches one event to its kind-specific writer. The per-kind
// switch is the body of dispatchEvent; apply itself is only responsible for
// updating the batch's max-seq observability counter and delegating to the
// dispatcher. Unknown kinds (events with no registered handler) return an
// explicit error so a future canonical event added without a writer wiring
// fails closed instead of silently dropping.
func (w *writer) apply(ctx context.Context, tx *sql.Tx, ev canonical.Event) error {
	if seq := ev.EventSourceSeq(); seq > w.batchMaxSeq {
		w.batchMaxSeq = seq
	}
	return w.dispatchEvent(ctx, tx, ev)
}

// dispatchEvent forwards ev to its concrete apply* writer method. The 11
// canonical event kinds are split across three sub-dispatchers (session,
// turn-or-op, and ancillary) so no single function carries the full CCN of
// the per-kind switch while the behaviour-preserving contract — same
// handlers, same call shape, same unknown-kind error — is maintained.
func (w *writer) dispatchEvent(ctx context.Context, tx *sql.Tx, ev canonical.Event) error {
	if handled, err := w.dispatchSessionEvent(ctx, tx, ev); handled {
		return err
	}
	if handled, err := w.dispatchTurnOrOpEvent(ctx, tx, ev); handled {
		return err
	}
	if handled, err := w.dispatchAncillaryEvent(ctx, tx, ev); handled {
		return err
	}
	return fmt.Errorf("writer: unknown event kind %s", ev.EventKind())
}

// dispatchSessionEvent matches the three SessionXxx event kinds and forwards
// to the corresponding apply method. handled=false means ev is not a session
// event — the caller falls through to the next sub-dispatcher.
func (w *writer) dispatchSessionEvent(ctx context.Context, tx *sql.Tx, ev canonical.Event) (handled bool, err error) {
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return true, w.applySessionStarted(ctx, tx, e)
	case canonical.SessionUpdatedEvent:
		return true, w.applySessionUpdated(ctx, tx, e)
	case canonical.SessionFinalizedEvent:
		return true, w.applySessionFinalized(ctx, tx, e)
	}
	return false, nil
}

// dispatchTurnOrOpEvent matches the four Turn/Op start/finalize event kinds
// — the turns and ops table writers — and forwards to the corresponding
// apply method. handled=false means ev is not one of these.
func (w *writer) dispatchTurnOrOpEvent(ctx context.Context, tx *sql.Tx, ev canonical.Event) (handled bool, err error) {
	switch e := ev.(type) {
	case canonical.TurnStartedEvent:
		return true, w.applyTurnStarted(ctx, tx, e)
	case canonical.TurnFinalizedEvent:
		return true, w.applyTurnFinalized(ctx, tx, e)
	case canonical.OpStartedEvent:
		return true, w.applyOpStarted(ctx, tx, e)
	case canonical.OpFinalizedEvent:
		return true, w.applyOpFinalized(ctx, tx, e)
	}
	return false, nil
}

// dispatchAncillaryEvent matches the four non-session/non-turn/non-op event
// kinds: payload refs, log entries, source progress, and source errors.
// handled=false means ev matches none of these — dispatchEvent then surfaces
// the unknown-kind error.
func (w *writer) dispatchAncillaryEvent(ctx context.Context, tx *sql.Tx, ev canonical.Event) (handled bool, err error) {
	switch e := ev.(type) {
	case canonical.PayloadRefEvent:
		return true, w.applyPayloadRef(ctx, tx, e)
	case canonical.LogEntryEvent:
		return true, w.applyLogEntry(ctx, tx, e)
	case canonical.SourceProgressEvent:
		w.applySourceProgress(e)
		return true, nil
	case canonical.SourceErrorEvent:
		return true, w.applySourceError(ctx, tx, e)
	}
	return false, nil
}

// applySessionStarted inserts or updates a sessions row. Parent linkage
// is best-effort: if the parent is already present we set
// parent_session_id directly; otherwise we leave it NULL and stash
// parent_native_id in extras_json.aiViewer.parentNativeId so the
// resolver can backfill later.
func (w *writer) applySessionStarted(ctx context.Context, tx *sql.Tx, ev canonical.SessionStartedEvent) error {
	if ev.NativeID == "" {
		return errors.New("writer: SessionStartedEvent missing NativeID")
	}
	id := canonicalSessionID(w.sourceID, ev.NativeID)
	rootID := id
	if ev.RootNativeID != "" && ev.RootNativeID != ev.NativeID {
		// Only point root_session_id at another session when that
		// session is already present; otherwise the FK constraint
		// fires. The resolver pass fixes both parent_session_id and
		// root_session_id once the missing rows land.
		if rid, err := w.lookupSessionID(ctx, tx, ev.RootNativeID); err == nil {
			rootID = rid
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var parentID sql.NullString
	if ev.ParentNativeID != "" {
		if pid, err := w.lookupSessionID(ctx, tx, ev.ParentNativeID); err == nil {
			parentID = sql.NullString{String: pid, Valid: true}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	extras := mergeExtras(ev.Extras, map[string]any{
		"aiViewer": map[string]any{
			"parentNativeId": ev.ParentNativeID,
			"parentOpKey":    ev.ParentOpKey,
			"rootNativeId":   ev.RootNativeID,
		},
	})
	extrasJSON, err := marshalExtras(extras)
	if err != nil {
		return fmt.Errorf("writer: marshal session extras: %w", err)
	}
	kind := string(ev.Kind)
	if kind == "" {
		kind = string(canonical.KindRoot)
	}
	// Read the existing row's start_ts BEFORE the UPSERT so we know (a) whether the
	// session already existed (a requireSessionID stub from an out-of-order op/log,
	// or a prior re-emit) and (b) its OLD start_ts, which the UPSERT's
	// MIN(sessions.start_ts, excluded.start_ts) may pull earlier. Both feed the
	// rollup re-marking below.
	var oldStart sql.NullInt64
	existed := false
	switch err := tx.QueryRowContext(ctx,
		`SELECT start_ts FROM sessions WHERE id = ?`, id).Scan(&oldStart); {
	case err == nil:
		existed = true
	case errors.Is(err, sql.ErrNoRows):
		// genuine new insert
	default:
		return fmt.Errorf("writer: read session start_ts %s: %w", id, err)
	}
	// extras_json is REPLACED wholesale on conflict EXCEPT the resolver's
	// `aiViewer` stash sub-object, which is grafted back when a stash-free
	// re-emit omits it (SOW-0003 P1.7b). The claude-code sub-agent SessionStarted
	// stashes the child's `toolUseId` in `aiViewer`; a later stash-free session
	// re-emit (e.g. a parent-map re-read) that wholesale-replaced extras would
	// erase that join key and permanently orphan the op→child edge. The graft
	// (json_set, NOT json_patch) preserves the stash without the json_patch
	// null-as-delete hazard (see graftAiViewerExtras).
	// A synthetic parent-side SessionStartedEvent is a linkage hint, not a
	// replacement for a real child session_start. When the existing row already
	// carries real source metadata, keep source-owned fields and graft only
	// missing aiViewer linkage hints from the synthetic replay.
	if !existed {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
    id, source_id, native_id, parent_session_id, root_session_id,
    kind, agent_name, model, provider, provider_alias, cwd, call_path, status,
    start_ts, last_activity_ts, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			id, w.sourceID, ev.NativeID, parentID, rootID,
			kind, nullIfEmpty(ev.AgentName), nullIfEmpty(ev.Model),
			nullIfEmpty(ev.Provider), nullIfEmpty(ev.ProviderAlias),
			nullIfEmpty(ev.Cwd), nullIfEmpty(ev.CallPath), string(canonical.StatusRunning),
			ev.Ts, ev.Ts, extrasJSON,
		); err != nil {
			return fmt.Errorf("writer: insert session: %w", err)
		}
	} else {
		preserveRealMetadata := preserveRealSessionMetadataPredicate()
		sessionExtrasConflict := sessionStartedExtrasConflictExpression()
		parentIDConflict := sessionStartedParentIDConflictExpression()
		rootIDConflict := sessionStartedRootIDConflictExpression()
		// #nosec G202 -- the interpolated values are SQL fragments built solely from
		// compile-time-constant column literals and package-const JSON key lists; no
		// caller/source input reaches the SQL.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
    id, source_id, native_id, parent_session_id, root_session_id,
    kind, agent_name, model, provider, provider_alias, cwd, call_path, status,
    start_ts, last_activity_ts, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id, native_id) DO UPDATE SET
    parent_session_id = `+parentIDConflict+`,
    root_session_id   = `+rootIDConflict+`,
    kind              = CASE WHEN `+preserveRealMetadata+` THEN sessions.kind ELSE excluded.kind END,
    agent_name        = CASE WHEN `+preserveRealMetadata+` THEN sessions.agent_name ELSE COALESCE(NULLIF(excluded.agent_name, ''), sessions.agent_name) END,
    model             = CASE WHEN `+preserveRealMetadata+` THEN sessions.model ELSE COALESCE(NULLIF(excluded.model, ''), sessions.model) END,
    provider          = CASE WHEN `+preserveRealMetadata+` THEN sessions.provider ELSE COALESCE(NULLIF(excluded.provider, ''), sessions.provider) END,
    provider_alias    = CASE WHEN `+preserveRealMetadata+` THEN sessions.provider_alias ELSE COALESCE(NULLIF(excluded.provider_alias, ''), sessions.provider_alias) END,
    cwd               = CASE WHEN `+preserveRealMetadata+` THEN sessions.cwd ELSE COALESCE(NULLIF(excluded.cwd, ''), sessions.cwd) END,
    call_path         = CASE WHEN `+preserveRealMetadata+` THEN sessions.call_path ELSE COALESCE(NULLIF(excluded.call_path, ''), sessions.call_path) END,
    start_ts          = MIN(sessions.start_ts, excluded.start_ts),
    last_activity_ts  = MAX(sessions.last_activity_ts, excluded.last_activity_ts),
    extras_json       = `+sessionExtrasConflict+`
`,
			id, w.sourceID, ev.NativeID, parentID, rootID,
			kind, nullIfEmpty(ev.AgentName), nullIfEmpty(ev.Model),
			nullIfEmpty(ev.Provider), nullIfEmpty(ev.ProviderAlias),
			nullIfEmpty(ev.Cwd), nullIfEmpty(ev.CallPath), string(canonical.StatusRunning),
			ev.Ts, ev.Ts, extrasJSON,
		); err != nil {
			return fmt.Errorf("writer: insert session: %w", err)
		}
	}
	w.markDirtySession(id)
	// The session start contributes session_starts to its start-hour bucket
	// (total/agent/cwd), so that hour's rollup must be recomputed.
	w.markDirtyRollupBucket(ev.Ts)
	// Twin of the applySessionUpdated/F1 re-marking: this UPSERT can REPAIR an
	// already-existing session's denormalized rollup inputs. A requireSessionID
	// stub (out-of-order op/log) carries empty agent_name/cwd and start_ts = the
	// referencing event's ts; a later SessionStarted then (a) fills agent/cwd via
	// COALESCE(NULLIF(...)) and (b) can pull start_ts EARLIER via MIN. Both stale
	// the materialized rollups the incremental refresh already wrote, and neither
	// is covered by markDirtyRollupBucket(ev.Ts) alone:
	//   - filled agent/cwd → every hour bucket holding one of this session's ops
	//     kept the old (empty/previous) agent/cwd dimension fold, exactly the
	//     multi-bucket hazard F1 fixed for applySessionUpdated, so re-mark them all.
	//   - start moved earlier → the OLD start bucket keeps a phantom session_start
	//     that must be recomputed away (the NEW start bucket ev.Ts is already
	//     marked above). On a genuine first insert neither applies.
	// Marking is idempotent (recomputing an unchanged bucket yields the same row),
	// so over-marking is harmless.
	if existed {
		if ev.AgentName != "" || ev.Cwd != "" {
			if err := w.markSessionRollupBucketsDirty(ctx, tx, id); err != nil {
				return err
			}
		}
		if oldStart.Valid && ev.Ts < oldStart.Int64 {
			w.markDirtyRollupBucket(oldStart.Int64)
		}
	}
	if err := w.catalog.onSessionStarted(ctx, tx, w.sourceFormat, ev); err != nil {
		return err
	}
	return nil
}

func (w *writer) applySessionUpdated(ctx context.Context, tx *sql.Tx, ev canonical.SessionUpdatedEvent) error {
	id, err := w.requireSessionID(ctx, tx, ev.NativeID, ev.Ts)
	if err != nil {
		return err
	}
	// Merge extras into existing JSON. SQLite's json_patch is exactly
	// what we need: it walks both trees and merges objects.
	extrasJSON, err := marshalExtras(ev.Extras)
	if err != nil {
		return fmt.Errorf("writer: marshal session updated extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET
    agent_name        = COALESCE(NULLIF(?, ''), agent_name),
    model             = COALESCE(NULLIF(?, ''), model),
    provider          = COALESCE(NULLIF(?, ''), provider),
    provider_alias    = COALESCE(NULLIF(?, ''), provider_alias),
    cwd               = COALESCE(NULLIF(?, ''), cwd),
    status            = COALESCE(NULLIF(?, ''), status),
    last_activity_ts  = MAX(last_activity_ts, ?),
    extras_json       = CASE
        WHEN ? IS NULL THEN extras_json
        WHEN extras_json IS NULL THEN ?
        ELSE json_patch(extras_json, ?)
    END
WHERE id = ?
`,
		ev.AgentName, ev.Model, ev.Provider, ev.ProviderAlias, ev.Cwd, ev.Status, ev.Ts,
		extrasJSON, extrasJSON, extrasJSON,
		id,
	); err != nil {
		return fmt.Errorf("writer: update session: %w", err)
	}
	w.markDirtySession(id)
	// agent_name and cwd are session-denormalized rollup inputs: the fold reads
	// sessions.agent_name/cwd via the ops⋈sessions join (rollup_backfill.go
	// scanOpRow) AND attributes session_starts to the agent/cwd dimensions. So a
	// SessionUpdated that CHANGES either one invalidates the materialized agent/
	// cwd dimension rows of EVERY hour bucket holding one of this session's ops
	// (a session's ops span many buckets), plus the session-start bucket. The
	// incremental refresh only recomputes buckets in dirtyRollupBuckets, so
	// without marking them here a late metadata repair (e.g. claude_code
	// re-emitting agent/cwd) would leave those dimension rows STALE until a full
	// backfill. Marking is idempotent (recomputing an unchanged bucket yields the
	// same row), so over-marking when only one of agent/cwd changed is harmless.
	if ev.AgentName != "" || ev.Cwd != "" {
		if err := w.markSessionRollupBucketsDirty(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// markSessionRollupBucketsDirty marks every rollup hour bucket whose recompute
// reads this session's denormalized agent_name/cwd: the session's START-hour
// bucket (for session_starts re-attribution) and the START-hour bucket of each
// of the session's ops (for the agent/cwd dimension folds). Called by
// applySessionUpdated when agent_name or cwd may have changed. Single-connection
// discipline: each cursor is fully drained into a slice BEFORE any marking, and
// marking is in-memory only (no SQL write follows the cursor on the batch tx).
//
// Implementation is two phases that mirror the discipline above exactly:
// (1) drainSessionRollupBuckets reads the session-start ts + every op-start
// ts into slices, fully closing each cursor before returning; (2) the
// receiver then walks those slices and applies the idempotent in-memory
// markDirtyRollupBucket marks. The split keeps the cursor-close error paths
// in one place and the marking loop trivially small.
func (w *writer) markSessionRollupBucketsDirty(ctx context.Context, tx *sql.Tx, sessionID string) error {
	startTs, opStarts, err := drainSessionRollupBuckets(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if startTs.Valid {
		w.markDirtyRollupBucket(startTs.Int64)
	}
	for _, opStart := range opStarts {
		w.markDirtyRollupBucket(opStart)
	}
	return nil
}

// drainSessionRollupBuckets reads the session's start_ts and every op_start_ts
// for sessionID into slices, fully draining and closing each cursor before
// returning. Single-connection discipline (store.OpenWriter pins SetMaxOpenConns=1):
// no SQL write may straddle an open read on the pinned connection, so the
// drain happens here BEFORE the caller marks. A NULL start_ts (no sessions
// row yet) is returned as an invalid NullInt64 — the caller skips that mark.
func drainSessionRollupBuckets(ctx context.Context, tx *sql.Tx, sessionID string) (sql.NullInt64, []int64, error) {
	startTs, err := loadSessionStartTs(ctx, tx, sessionID)
	if err != nil {
		return sql.NullInt64{}, nil, err
	}
	opStarts, err := loadSessionOpStartTs(ctx, tx, sessionID)
	if err != nil {
		return sql.NullInt64{}, nil, err
	}
	return startTs, opStarts, nil
}

// loadSessionStartTs reads the session's start_ts into a NullInt64. A missing
// session row is NOT an error here — it can happen during out-of-order ingest
// when a session-update arrives before the matching SessionStarted. The
// caller treats a NULL start_ts as "nothing to mark on the start bucket".
func loadSessionStartTs(ctx context.Context, tx *sql.Tx, sessionID string) (sql.NullInt64, error) {
	var startTs sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT start_ts FROM sessions WHERE id = ?`, sessionID).Scan(&startTs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt64{}, nil
		}
		return sql.NullInt64{}, fmt.Errorf("writer: read session start_ts %s: %w", sessionID, err)
	}
	return startTs, nil
}

// loadSessionOpStartTs reads every op's start_ts for sessionID, fully draining
// the cursor before returning. Errors from Scan / Err / Close are all wrapped
// with the session id so they surface contextually in the logs. The cursor
// must NOT outlive this call — single-connection discipline requires every
// read to finish before any write follows on the batch tx.
func loadSessionOpStartTs(ctx context.Context, tx *sql.Tx, sessionID string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT start_ts FROM ops WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("writer: query session op buckets %s: %w", sessionID, err)
	}
	var opStarts []int64
	for rows.Next() {
		var opStart int64
		if scanErr := rows.Scan(&opStart); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("writer: scan session op start %s: %w", sessionID, scanErr)
		}
		opStarts = append(opStarts, opStart)
	}
	if iterErr := rows.Err(); iterErr != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("writer: iterate session op buckets %s: %w", sessionID, iterErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("writer: close session op buckets %s: %w", sessionID, closeErr)
	}
	return opStarts, nil
}

func (w *writer) applySessionFinalized(ctx context.Context, tx *sql.Tx, ev canonical.SessionFinalizedEvent) error {
	id, err := w.requireSessionID(ctx, tx, ev.NativeID, ev.Ts)
	if err != nil {
		return err
	}
	status := string(ev.Status)
	if status == "" {
		status = string(canonical.StatusCompleted)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET
    status           = ?,
    error_class      = NULLIF(?, ''),
    error_message    = NULLIF(?, ''),
    end_ts           = ?,
    last_activity_ts = MAX(last_activity_ts, ?)
WHERE id = ?
`,
		status, ev.ErrorClass, ev.ErrorMessage, nullIfZero(ev.EndTs), ev.Ts, id,
	); err != nil {
		return fmt.Errorf("writer: finalize session: %w", err)
	}
	w.markDirtySession(id)
	return nil
}

func (w *writer) applyTurnStarted(ctx context.Context, tx *sql.Tx, ev canonical.TurnStartedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.Seq)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, seq) DO UPDATE SET
    start_ts = MIN(turns.start_ts, excluded.start_ts)
`,
		turnID, sessionID, ev.Seq, ev.Ts, string(canonical.StatusRunning),
	); err != nil {
		return fmt.Errorf("writer: insert turn: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyTurnFinalized(ctx context.Context, tx *sql.Tx, ev canonical.TurnFinalizedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.Seq)
	// Turn extras (SOW-0021): marshal the adapter's per-turn metadata into
	// turns.extras_json. marshalExtras returns (nil,nil) for an empty map → the
	// ? binds NULL, so a turn with no extras leaves the column NULL (matching
	// the data-model default). Turn-finalize is terminal + single-shot per
	// (session, seq), so the ON CONFLICT wholesale write is idempotent under
	// re-emit (a re-emit carries the same extras).
	extrasJSON, err := marshalExtras(ev.Extras)
	if err != nil {
		return fmt.Errorf("writer: marshal turn extras: %w", err)
	}
	// Insert if missing (e.g. TurnFinalized arrives without a TurnStarted).
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status, error_class, extras_json)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)
ON CONFLICT (session_id, seq) DO UPDATE SET
    end_ts      = excluded.end_ts,
    status      = excluded.status,
    error_class = excluded.error_class,
    extras_json = excluded.extras_json
`,
		turnID, sessionID, ev.Seq, ev.Ts, nullIfZero(ev.EndTs),
		nonEmpty(ev.Status, string(canonical.StatusCompleted)), ev.ErrorClass, extrasJSON,
	); err != nil {
		return fmt.Errorf("writer: finalize turn: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) error {
	prep, err := w.prepareOpStarted(ctx, tx, ev)
	if err != nil {
		return err
	}
	if err := w.upsertOpStarted(ctx, tx, ev, prep); err != nil {
		return err
	}
	return w.afterOpStarted(ctx, tx, ev, prep)
}

// opStartedPrep carries the resolved-row state applyOpStarted needs to feed the
// SQL upsert and the post-upsert hooks. It captures the canonical session/turn/
// op ids, the optional parent op id, the (possibly-stashed) child session id,
// the resolver-grafted extras blob, and the op's prior persisted catalog
// identity — read BEFORE the upsert so the catalog migrate logic sees the OLD
// row (SOW-0004 H1a / I1). opInserted mirrors the original `!prior.found`
// signal so onOpStarted knows whether this is a genuine new insert or a
// same/changed-identity re-emit.
type opStartedPrep struct {
	sessionID      string
	turnID         string
	opID           string
	parentOpID     sql.NullString
	childSessionID sql.NullString
	extras         any
	prior          priorOpIdentity
	opInserted     bool
}

// prepareOpStarted does every read the upsert needs: resolves the session/turn
// ids, synthesizes the parent turn if no TurnStarted arrived first, captures
// the op's prior persisted catalog identity (so the catalog upsert can migrate
// contributions on an identity-changed re-emit — SOW-0004 I1), and computes
// the parent-op id + child-session link + marshaled extras blob. No mutating
// SQL beyond the turn ON CONFLICT DO NOTHING synth — the ops UPSERT runs in
// upsertOpStarted.
func (w *writer) prepareOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) (opStartedPrep, error) {
	sessionID, turnID, opID, prior, err := w.opStartedIdentity(ctx, tx, ev)
	if err != nil {
		return opStartedPrep{}, err
	}
	parentOpID := resolveOpStartedParent(turnID, ev.ParentOpSeq)
	childSessionID, opExtras, err := w.resolveOpStartedChild(ctx, tx, ev)
	if err != nil {
		return opStartedPrep{}, err
	}
	extras, err := marshalExtras(opExtras)
	if err != nil {
		return opStartedPrep{}, fmt.Errorf("writer: marshal op extras: %w", err)
	}
	return opStartedPrep{
		sessionID:      sessionID,
		turnID:         turnID,
		opID:           opID,
		parentOpID:     parentOpID,
		childSessionID: childSessionID,
		extras:         extras,
		prior:          prior,
		opInserted:     !prior.found,
	}, nil
}

// opStartedIdentity resolves the canonical session/turn/op ids for this
// OpStarted (creating stub session/turn rows if no SessionStarted/TurnStarted
// arrived first) and reads the op's PRIOR persisted catalog identity. The
// prior identity is captured BEFORE the ops UPSERT so the catalog upsert can
// (a) count the call ONCE per distinct op (SOW-0004 H1a) and (b) MIGRATE the
// op's contribution off its old key when this re-emit CHANGES the catalog
// identity (codex MCP enrichment re-stamping tool_namespace/name on the same
// (turn,seq) — SOW-0004 I1). ON CONFLICT DO UPDATE returns RowsAffected=1 for
// both insert and update under modernc/sqlite, so reading the row first is
// the authoritative insert-vs-update signal: prior.found=false (sql.ErrNoRows)
// ⇒ genuine new insert. The OpStarted upsert touches only identity columns +
// start_ts/extras — never the status/tokens/cost/duration columns — so the
// totals read here are exactly the contribution onOpFinalized already booked
// under the old identity.
func (w *writer) opStartedIdentity(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) (string, string, string, priorOpIdentity, error) {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return "", "", "", priorOpIdentity{}, err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	// Ensure parent turn row exists — synthesize a running turn if no
	// TurnStarted arrived first. This matches the spec: turns may be implicit
	// when the source format doesn't emit explicit start records.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, seq) DO NOTHING
`, turnID, sessionID, ev.TurnSeq, ev.Ts, string(canonical.StatusRunning)); err != nil {
		return "", "", "", priorOpIdentity{}, fmt.Errorf("writer: synthesize turn for op: %w", err)
	}
	opID := canonicalOpID(turnID, ev.Seq)
	prior, err := w.opPriorIdentity(ctx, tx, opID)
	if err != nil {
		return "", "", "", priorOpIdentity{}, err
	}
	return sessionID, turnID, opID, prior, nil
}

// resolveOpStartedParent maps an OpStartedEvent.ParentOpSeq into a NullString
// op id, mirroring the original inline ParentOpSeq>=0 guard. A negative
// ParentOpSeq stays as SQL NULL (the op has no parent op).
func resolveOpStartedParent(turnID string, parentOpSeq int) sql.NullString {
	if parentOpSeq < 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: canonicalOpID(turnID, parentOpSeq), Valid: true}
}

// resolveOpStartedChild looks up the child session row (when the event names
// one) and either returns its id directly OR stashes the child native id
// into the op's extras_json.aiViewer.childNativeId so the resolver can re-link
// it once the child session lands (P1a). A SQL error other than ErrNoRows is
// propagated unchanged. When the event carries no ChildSessionNativeID, the
// child link stays NULL and the extras blob is the event's untouched extras.
func (w *writer) resolveOpStartedChild(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) (sql.NullString, map[string]any, error) {
	if ev.ChildSessionNativeID == "" {
		return sql.NullString{}, ev.Extras, nil
	}
	cid, err := w.lookupSessionID(ctx, tx, ev.ChildSessionNativeID)
	if err == nil {
		return sql.NullString{String: cid, Valid: true}, ev.Extras, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, nil, err
	}
	// Child has not landed yet (parent transcript is read before, or in a
	// different batch than, the child sidechain) — leave the link NULL and
	// stash the child native id in ops.extras_json.aiViewer.childNativeId.
	// The resolver re-links child_session_id from that stash once the child
	// session lands (P1a) — mirroring the session aiViewer.parentNativeId
	// stash + resolver pass.
	stashed := mergeExtras(ev.Extras, map[string]any{
		"aiViewer": map[string]any{"childNativeId": ev.ChildSessionNativeID},
	})
	return sql.NullString{}, stashed, nil
}

// upsertOpStarted runs the ops UPSERT exactly as before — same column list,
// same ON CONFLICT clause, same graftAiViewerExtras wiring. Pulled out so
// applyOpStarted is orchestration-only and the dense SQL lives in one place.
func (w *writer) upsertOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent, prep opStartedPrep) error {
	// extras_json is REPLACED wholesale on conflict EXCEPT the resolver's
	// `aiViewer` stash sub-object, which is grafted back when a stash-free re-emit
	// omits it (SOW-0003 P2.6d/P2.7c). A re-emit of the same op whose extras lack
	// the resolver stash (childNativeId / toolUseId) must not erase a join key the
	// resolver still needs — a wholesale replace would permanently orphan the
	// op→child edge. The graft uses json_set (NOT json_patch): json_patch would
	// interpret a JSON `null` VALUE as a DELETE (RFC 7386), and adapters copy
	// arbitrary source attributes into op extras (aiagent_v3 `extras["attr."+k]`),
	// so a replay carrying `{"attr.x":null}` would silently drop key `attr.x`.
	// See graftAiViewerExtras.
	// #nosec G202 -- the only interpolated value is graftAiViewerExtras's output,
	// built solely from the compile-time-constant column literal "ops.extras_json"
	// and the package-const aiViewerStashKeys; no caller/source input reaches the SQL.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO ops (
    id, turn_id, session_id, parent_op_id, seq,
    kind, name, tool_namespace, model, provider, provider_alias,
    reasoning_kind, start_ts, status,
    child_session_id, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (turn_id, seq) DO UPDATE SET
    kind           = excluded.kind,
    name           = excluded.name,
    tool_namespace = COALESCE(NULLIF(excluded.tool_namespace, ''), ops.tool_namespace),
    model          = COALESCE(NULLIF(excluded.model, ''), ops.model),
    provider       = COALESCE(NULLIF(excluded.provider, ''), ops.provider),
    provider_alias = COALESCE(NULLIF(excluded.provider_alias, ''), ops.provider_alias),
    reasoning_kind = COALESCE(NULLIF(excluded.reasoning_kind, ''), ops.reasoning_kind),
    start_ts       = MIN(ops.start_ts, excluded.start_ts),
    parent_op_id   = COALESCE(ops.parent_op_id, excluded.parent_op_id),
    child_session_id = COALESCE(ops.child_session_id, excluded.child_session_id),
    extras_json    = `+graftAiViewerExtras("ops.extras_json")+`
`,
		prep.opID, prep.turnID, prep.sessionID, prep.parentOpID, ev.Seq,
		string(ev.Kind), ev.Name, nullIfEmpty(ev.ToolNamespace), nullIfEmpty(ev.Model), nullIfEmpty(ev.Provider), nullIfEmpty(ev.ProviderAlias),
		nullIfEmpty(ev.ReasoningKind), ev.Ts, string(canonical.StatusRunning),
		prep.childSessionID, prep.extras,
	); err != nil {
		return fmt.Errorf("writer: insert op: %w", err)
	}
	return nil
}

// afterOpStarted applies the post-upsert side effects: dirty turn/session
// marking for the aggregate refresh, dirty rollup-bucket marking for the
// hourly/daily rollup recompute (ev.Ts IS the op start, the bucket the op
// rolls into), dirty fts_ops marking so refreshFTS rebuilds the search row,
// and the catalog upsert (which uses prep.prior to decide between count-once
// new-insert vs migrate-contribution identity change). Behaviour is byte-for-
// byte identical to the original inline tail of applyOpStarted.
func (w *writer) afterOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent, prep opStartedPrep) error {
	w.markDirtyTurn(prep.turnID)
	w.markDirtySession(prep.sessionID)
	// The op's start hour gains an op, so that hour's rollup must be
	// recomputed. ev.Ts IS the op start (the OpStarted timestamp), which is
	// the bucket the op rolls up into. ASSUMPTION (catalog + SOW-0027): an op
	// never moves buckets after its start — adapters emit the authoritative
	// start at/before finalize, so cross-bucket op migration is out of scope.
	w.markDirtyRollupBucket(ev.Ts)
	// The op's indexed text (name/model/provider/tool_namespace) may have
	// changed; refreshFTS rebuilds its fts_ops row from the final persisted
	// columns at flush time (fts_ops is always indexed — no flag gate).
	w.markDirtyOp(prep.opID)
	return w.catalog.onOpStarted(ctx, tx, ev, prep.opInserted, prep.prior)
}

// opPriorIdentity reads an op's persisted catalog identity (kind/name/namespace/
// model/provider/alias) AND its terminal rollup totals as they stand BEFORE the
// current OpStarted UPSERT, so the catalog can migrate the op's contribution when
// a re-emit changes its catalog identity (SOW-0004 I1). found=false (sql.ErrNoRows)
// means the op has no row yet — a genuine new insert with nothing to migrate. The
// totals share opPriorTotals' semantics (the OpStarted upsert leaves the
// status/tokens/cost/duration columns untouched, so this is the contribution
// onOpFinalized already booked under the old identity).
func (w *writer) opPriorIdentity(ctx context.Context, tx *sql.Tx, opID string) (priorOpIdentity, error) {
	var (
		p             priorOpIdentity
		toolNamespace sql.NullString
		model         sql.NullString
		provider      sql.NullString
		providerAlias sql.NullString
		dur           sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
SELECT kind, name, tool_namespace, model, provider, provider_alias,
       status, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, duration_us
  FROM ops WHERE id = ?`, opID).
		Scan(&p.kind, &p.name, &toolNamespace, &model, &provider, &providerAlias,
			&p.totals.status, &p.totals.tokensIn, &p.totals.tokensOut,
			&p.totals.tokensCacheRead, &p.totals.tokensCacheWrite, &p.totals.costUSD, &dur)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return priorOpIdentity{}, nil
		}
		return priorOpIdentity{}, fmt.Errorf("writer: read op prior identity %s: %w", opID, err)
	}
	p.found = true
	p.toolNamespace = toolNamespace.String
	p.model = model.String
	p.provider = provider.String
	p.providerAlias = providerAlias.String
	p.totals.found = true
	p.totals.durationUS = dur.Int64
	return p, nil
}

type finalizedOpLookup struct {
	provider sql.NullString
	model    sql.NullString
	kind     sql.NullString
	startTs  sql.NullInt64
}

type finalizedOpTiming struct {
	endTsArg sql.NullInt64
	durUS    sql.NullInt64
}

func (w *writer) applyOpFinalized(ctx context.Context, tx *sql.Tx, ev canonical.OpFinalizedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	opID := canonicalOpID(turnID, ev.Seq)

	prior, err := w.opPriorTotals(ctx, tx, opID)
	if err != nil {
		return err
	}
	persisted, err := w.lookupFinalizedOp(ctx, tx, opID)
	if err != nil {
		return err
	}
	cost := w.resolveFinalizedOpCost(ctx, tx, ev, persisted)
	timing := resolveFinalizedOpTiming(ev, persisted.startTs)
	if err := w.updateFinalizedOp(ctx, tx, opID, ev, timing, cost); err != nil {
		return err
	}
	w.markOpFinalizedDirty(turnID, sessionID, opID, persisted.startTs)
	if err := w.catalogOpFinalized(ctx, tx, opID, ev, cost, prior); err != nil {
		return err
	}
	return nil
}

func (w *writer) lookupFinalizedOp(ctx context.Context, tx *sql.Tx, opID string) (finalizedOpLookup, error) {
	var op finalizedOpLookup
	if err := tx.QueryRowContext(ctx, `SELECT provider, model, kind, start_ts FROM ops WHERE id = ?`, opID).
		Scan(&op.provider, &op.model, &op.kind, &op.startTs); err != nil {
		// Missing op rows are valid orphan finalizes; schema/tx errors must bubble.
		if errors.Is(err, sql.ErrNoRows) {
			return finalizedOpLookup{}, nil
		}
		return finalizedOpLookup{}, fmt.Errorf("ingest writer: lookup op %s: %w", opID, err)
	}
	return op, nil
}

func (w *writer) resolveFinalizedOpCost(ctx context.Context, tx *sql.Tx, ev canonical.OpFinalizedEvent, op finalizedOpLookup) float64 {
	cost := ev.CostUSD
	if cost != 0 || w.pricer == nil || !isPriceableOp(op.kind, op.provider, op.model) {
		return cost
	}
	// Temporal pricing follows the persisted op start, not the finalize event.
	pricingTs := ev.Ts
	if op.startTs.Valid && op.startTs.Int64 > 0 {
		pricingTs = op.startTs.Int64
	}
	return w.priceOp(ctx, tx, op.provider.String, op.model.String, pricingTs, ev)
}

func resolveFinalizedOpTiming(ev canonical.OpFinalizedEvent, startTs sql.NullInt64) finalizedOpTiming {
	// Returning NULL args lets the UPDATE COALESCE preserve stored timing.
	if ev.EndTs <= 0 || !startTs.Valid || startTs.Int64 <= 0 || ev.EndTs < startTs.Int64 {
		return finalizedOpTiming{}
	}
	return finalizedOpTiming{
		endTsArg: sql.NullInt64{Int64: ev.EndTs, Valid: true},
		durUS:    sql.NullInt64{Int64: ev.EndTs - startTs.Int64, Valid: true},
	}
}

func (w *writer) updateFinalizedOp(ctx context.Context, tx *sql.Tx, opID string, ev canonical.OpFinalizedEvent, timing finalizedOpTiming, cost float64) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE ops SET
    end_ts             = COALESCE(?, end_ts),
    duration_us        = COALESCE(?, duration_us),
    status             = ?,
    error_class        = NULLIF(?, ''),
    error_message      = NULLIF(?, ''),
    tokens_in          = ?,
    tokens_out         = ?,
    tokens_cache_read  = ?,
    tokens_cache_write = ?,
    cost_usd           = ?,
    bytes_in           = ?,
    bytes_out          = ?,
    chars_in           = NULLIF(?, 0),
    chars_out          = NULLIF(?, 0),
    ctx_used           = NULLIF(?, 0),
    ctx_max            = NULLIF(?, 0)
WHERE id = ?
`,
		timing.endTsArg, timing.durUS, nonEmpty(ev.Status, string(canonical.StatusCompleted)),
		ev.ErrorClass, ev.ErrorMessage,
		ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite, cost,
		ev.BytesIn, ev.BytesOut, ev.CharsIn, ev.CharsOut, ev.CtxUsed, ev.CtxMax,
		opID,
	); err != nil {
		return fmt.Errorf("writer: finalize op: %w", err)
	}
	return nil
}

func (w *writer) markOpFinalizedDirty(turnID, sessionID, opID string, startTs sql.NullInt64) {
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	// Rollups repair the start bucket; FTS repairs by op id.
	if startTs.Valid {
		w.markDirtyRollupBucket(startTs.Int64)
	}
	w.markDirtyOp(opID)
}

func (w *writer) catalogOpFinalized(ctx context.Context, tx *sql.Tx, opID string, ev canonical.OpFinalizedEvent, cost float64, prior opPriorTotals) error {
	// Catalog totals must see the computed cost that was persisted on ops.
	evForCatalog := ev
	evForCatalog.CostUSD = cost
	return w.catalog.onOpFinalized(ctx, tx, opID, evForCatalog, prior)
}

// opPriorTotals reads an op's persisted terminal contribution (status + the
// token/cost/duration columns the catalog rollups sum) as it stands BEFORE the
// current OpFinalized UPDATE. It is the durable prior state the catalog subtracts
// to stay idempotent under a re-emitted / corrected finalize (SOW-0004 H1a):
// reading the persisted row (not the event) means a re-finalize across a daemon
// restart — where any in-memory per-op tracking would be gone — still computes a
// correct delta. sql.ErrNoRows ⇒ no row yet (first finalize, or OpStarted not yet
// landed): found=false and every prior contribution is zero, so the delta equals
// the full new contribution and the single-emission path is unchanged.
func (w *writer) opPriorTotals(ctx context.Context, tx *sql.Tx, opID string) (opPriorTotals, error) {
	var p opPriorTotals
	var dur sql.NullInt64
	err := tx.QueryRowContext(ctx, `
SELECT status, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, duration_us
  FROM ops WHERE id = ?`, opID).
		Scan(&p.status, &p.tokensIn, &p.tokensOut, &p.tokensCacheRead, &p.tokensCacheWrite, &p.costUSD, &dur)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return opPriorTotals{}, nil
		}
		return opPriorTotals{}, fmt.Errorf("writer: read op prior totals %s: %w", opID, err)
	}
	p.found = true
	p.durationUS = dur.Int64
	return p, nil
}

// isPriceableOp reports whether the op identified by (kind, provider,
// model) should be passed to the pricer. Non-LLM ops (tool, system,
// session) carry no token counts and have empty provider/model; pricing
// them produces noisy and unactionable "unknown pricing for provider \"\"
// model \"\"" warnings. Ops without a recorded kind but with both
// provider and model present are treated as priceable for defence in
// depth (legacy adapters that pre-date the kind column).
func isPriceableOp(kind, provider, model sql.NullString) bool {
	if !provider.Valid || provider.String == "" {
		return false
	}
	if !model.Valid || model.String == "" {
		return false
	}
	if kind.Valid && kind.String != "" && kind.String != string(canonical.OpLLM) {
		return false
	}
	return true
}

// priceOp invokes the pricer and, when the pricer supports it (i.e.
// implements DetailedPricer), emits a deduped SourceError WRN log
// entry on miss per pricing.md §"Temporal resolution algorithm". The
// dedup key is (provider, model, missKind) for the lifetime of the
// worker (i.e. per source) so the same unknown-pricing warning is
// not repeated for every op of an unknown model, even across batches.
// tsUS is the op's start_ts — the value the pricer uses to pick a
// tier, and the value the log entry records so the warning sits at
// the same point in the timeline as the priced op.
func (w *writer) priceOp(ctx context.Context, tx *sql.Tx, provider, model string, tsUS int64, ev canonical.OpFinalizedEvent) float64 {
	if dp, ok := w.pricer.(DetailedPricer); ok {
		cost, hit, missKind := dp.CostWithDetail(provider, model, tsUS, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
		if !hit {
			// Failing to write a WRN row or bump the per-source error
			// counter is a non-fatal observability gap, not a
			// data-integrity defect: the op cost still lands as zero
			// and returning the error here would abort the entire op
			// write. We record the error on a per-batch slice the
			// worker drains and logs after commit so the failure
			// surfaces in structured logs — satisfying the project
			// "no silent failures" rule without sacrificing the op.
			if logErr := w.emitPricingMiss(ctx, tx, provider, model, missKind, tsUS); logErr != nil {
				w.batchObservabilityErrs = append(w.batchObservabilityErrs,
					fmt.Errorf("emit pricing miss (provider=%q model=%q miss=%q): %w",
						provider, model, missKind, logErr))
			}
		}
		return cost
	}
	return w.pricer.Cost(provider, model, tsUS, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
}

// emitPricingMiss writes a WRN log entry naming the unknown (provider,
// model) pair AND bumps sources.parse_errors so the miss surfaces in
// the Sources panel's "errors recently" counter via the same path the
// adapter SourceError events use. Both writes are deduped per
// (provider, model, missKind) for the LIFETIME of the worker (i.e.
// per source) so a single unknown model that fires on every op of
// every batch produces exactly one log row and one parse_errors
// increment — matching pricing.md §"Temporal resolution algorithm".
//
// This function is best-effort observability: a returned error must
// never abort the surrounding op write (see priceOp's caller comment).
// The error paths (failed UPDATE, failed marshal, failed INSERT) are
// intentionally lightly covered — they only fire when the tx itself
// is already broken, in which case the surrounding flush() will
// roll back and the missed observability hook is the least of the
// caller's problems. See the SOW-0001 Gate Suppression entry that
// records this waiver.
func (w *writer) emitPricingMiss(ctx context.Context, tx *sql.Tx, provider, model, missKind string, tsUS int64) error {
	key := pricingMissKey{
		provider: strings.ToLower(provider),
		model:    strings.ToLower(model),
		kind:     missKind,
	}
	// Lifetime map records committed warnings; pending map records
	// warnings INSERTed in the current open tx but not yet committed.
	// Either is sufficient to skip re-emission within the same batch.
	if _, seen := w.pricingMissDedup[key]; seen {
		return nil
	}
	if _, seen := w.pendingMissDedup[key]; seen {
		return nil
	}

	if err := w.bumpSourceErrorCounter(ctx, tx, tsUS); err != nil {
		return err
	}

	msg := fmt.Sprintf("unknown pricing for provider %q model %q (%s)", provider, model, missKind)
	extras, err := json.Marshal(map[string]any{
		"provider":  provider,
		"model":     model,
		"miss_kind": missKind,
	})
	if err != nil {
		return fmt.Errorf("writer: marshal pricing-miss extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (NULL, ?, NULL, NULL, ?, 'WRN', ?, ?, ?)
`+logEntryOnConflict, w.sourceID, tsUS, w.sourceFormat, msg, string(extras)); err != nil {
		return fmt.Errorf("writer: insert pricing-miss log: %w", err)
	}
	// Mark the key only AFTER the INSERT succeeds. The mark lives in
	// the per-batch pending map so a subsequent commit failure leaves
	// pricingMissDedup untouched and the next batch with the same
	// (provider, model) re-emits the warning.
	w.pendingMissDedup[key] = struct{}{}
	return nil
}

// bumpSourceErrorCounter is the shared low-level UPDATE that
// applySourceError and emitPricingMiss both call so the Sources panel
// surfaces parse errors and pricing misses through the same metric.
// Both call sites must agree on what counts as a "recent error".
func (w *writer) bumpSourceErrorCounter(ctx context.Context, tx *sql.Tx, tsUS int64) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE sources SET parse_errors = parse_errors + 1, last_seen_at = MAX(COALESCE(last_seen_at, 0), ?)
WHERE id = ?
`, tsUS, w.sourceID); err != nil {
		return fmt.Errorf("writer: bump parse_errors: %w", err)
	}
	// parse_errors moved → the Sources panel's error counter changed, so
	// emitNotify must publish a source_status_changed row for this batch.
	w.sourceStatusChanged = true
	return nil
}

func (w *writer) applyPayloadRef(ctx context.Context, tx *sql.Tx, ev canonical.PayloadRefEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	opID := canonicalOpID(turnID, ev.OpSeq)
	// Defense-in-depth: payload_refs.op_id is NOT NULL REFERENCES ops(id)
	// (migration 0001), so an INSERT naming an op that has no row raises a
	// foreign-key error that rolls back the ENTIRE batch — one stray
	// PayloadRefEvent would kill ingestion for the whole source. Adapters
	// are expected to emit op-scoped refs only (and to order OpStarted
	// before its children), but a future adapter bug must never be able to
	// abort a batch here. So verify the parent op exists first; if it does
	// not, surface the condition through the same Sources-panel error
	// mechanism applySourceError uses and skip the insert — never a silent
	// drop (project "no silent failures" contract). See ingester.md
	// §Layer 3 — payload_ref orphan guard.
	if err := w.requireOpExists(ctx, tx, opID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return w.reportOrphanPayloadRef(ctx, tx, ev, opID)
		}
		return err
	}
	// ON CONFLICT DO NOTHING on the natural identity
	// (op_id, kind, location_uri) makes the write idempotent: re-emitting
	// the same payload (Tail re-read, file re-scan) never duplicates the
	// row. See migration 0003 and ingester.md §Dedup and Idempotency.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO payload_refs (op_id, kind, format, compression, location_uri, original_bytes, stored_bytes, sha256)
VALUES (?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, ''))
ON CONFLICT (op_id, kind, location_uri) DO NOTHING
`,
		opID, ev.PayloadKind, ev.Format, ev.Compression, ev.LocationURI,
		ev.OriginalBytes, ev.StoredBytes, ev.SHA256,
	); err != nil {
		return fmt.Errorf("writer: insert payload_ref: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

// requireOpExists returns nil when an ops row with id exists, sql.ErrNoRows
// when it does not, and a wrapped error on any other query failure. Used by
// applyPayloadRef to guard the NOT NULL FK on payload_refs.op_id before the
// insert, so a missing parent op is handled gracefully instead of aborting
// the batch.
func (w *writer) requireOpExists(ctx context.Context, tx *sql.Tx, opID string) error {
	var one int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM ops WHERE id = ?`, opID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return fmt.Errorf("writer: lookup payload_ref op: %w", err)
	}
	return nil
}

// reportOrphanPayloadRef surfaces a PayloadRefEvent whose parent op row does
// not exist. It mirrors applySourceError exactly — bump the shared
// Sources-panel error counter (so /api/health and the source-status panel
// reflect the problem) and write a source-scoped ERR log_entries row — then
// returns nil so the rest of the batch still commits. The orphaned ref is
// NOT inserted (its FK target is missing) and the turn/session are NOT marked
// dirty, since nothing was persisted for them here.
func (w *writer) reportOrphanPayloadRef(ctx context.Context, tx *sql.Tx, ev canonical.PayloadRefEvent, opID string) error {
	if err := w.bumpSourceErrorCounter(ctx, tx, ev.Ts); err != nil {
		return err
	}
	extras, err := json.Marshal(map[string]any{
		"op_id":        opID,
		"turn_seq":     ev.TurnSeq,
		"op_seq":       ev.OpSeq,
		"payload_kind": ev.PayloadKind,
		"location_uri": ev.LocationURI,
	})
	if err != nil {
		return fmt.Errorf("writer: marshal orphan payload_ref extras: %w", err)
	}
	msg := fmt.Sprintf("payload_ref references unknown op %s; dropped to protect the batch", opID)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (NULL, ?, NULL, NULL, ?, 'ERR', ?, ?, ?)
`+logEntryOnConflict, w.sourceID, ev.Ts, w.sourceFormat, msg, string(extras)); err != nil {
		return fmt.Errorf("writer: insert orphan payload_ref log: %w", err)
	}
	return nil
}

func (w *writer) applyLogEntry(ctx context.Context, tx *sql.Tx, ev canonical.LogEntryEvent) error {
	refs, err := w.resolveLogEntryRefs(ctx, tx, ev)
	if err != nil {
		return err
	}
	logID, inserted, err := insertLogEntry(ctx, tx, refs, ev)
	if err != nil {
		return err
	}
	w.markDirtySession(refs.sessionID)
	return w.indexLogEntryFTS(ctx, tx, refs, ev, logID, inserted)
}

// logEntryRefs carries the canonical session/turn/op linkage + the marshaled
// extras + the normalized severity that applyLogEntry's insert and FTS hooks
// both consume. Computed once in resolveLogEntryRefs so the downstream steps
// stay parameter-light.
type logEntryRefs struct {
	sessionID string
	turnID    sql.NullString
	opID      sql.NullString
	extras    any
	severity  string
}

// resolveLogEntryRefs resolves the canonical session/turn/op linkage for the
// LogEntryEvent and marshals its extras + normalizes its severity (default
// "INF" when missing). turnID/opID are kept as sql.NullString so the insert
// binds SQL NULL when the event has no turn or op context — preserving the
// exact COALESCE behaviour idx_log_entries_identity relies on.
func (w *writer) resolveLogEntryRefs(ctx context.Context, tx *sql.Tx, ev canonical.LogEntryEvent) (logEntryRefs, error) {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return logEntryRefs{}, err
	}
	var turnID sql.NullString
	if ev.TurnSeq > 0 {
		turnID = sql.NullString{String: canonicalTurnID(sessionID, ev.TurnSeq), Valid: true}
	}
	var opID sql.NullString
	if ev.OpSeq > 0 && turnID.Valid {
		opID = sql.NullString{String: canonicalOpID(turnID.String, ev.OpSeq), Valid: true}
	}
	extras, err := marshalExtras(ev.Extras)
	if err != nil {
		return logEntryRefs{}, fmt.Errorf("writer: marshal log extras: %w", err)
	}
	severity := strings.ToUpper(strings.TrimSpace(ev.Severity))
	if severity == "" {
		severity = "INF"
	}
	return logEntryRefs{
		sessionID: sessionID,
		turnID:    turnID,
		opID:      opID,
		extras:    extras,
		severity:  severity,
	}, nil
}

// insertLogEntry runs the INSERT … ON CONFLICT DO NOTHING … RETURNING id. On
// a genuine first-seen insert it returns the new log_entries.id and inserted=true;
// on a replayed byte-identical row (idx_log_entries_identity collides) the ON
// CONFLICT DO NOTHING fires and RETURNING yields no row → sql.ErrNoRows, which
// this helper translates to (0, false, nil) so the caller can skip the FTS
// insert without surfacing an error. Any other SQL failure propagates.
func insertLogEntry(ctx context.Context, tx *sql.Tx, refs logEntryRefs, ev canonical.LogEntryEvent) (int64, bool, error) {
	var logID int64
	insertErr := tx.QueryRowContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)
`+logEntryOnConflict+`
RETURNING id`, refs.sessionID, refs.turnID, refs.opID, ev.Ts, refs.severity, ev.Source, ev.Message, refs.extras).Scan(&logID)
	if insertErr != nil {
		if errors.Is(insertErr, sql.ErrNoRows) {
			return 0, false, nil // duplicate: the fts_logs row already exists.
		}
		return 0, false, fmt.Errorf("writer: insert log_entry: %w", insertErr)
	}
	return logID, true, nil
}

// indexLogEntryFTS inserts a matching fts_logs row when this batch wrote a
// NEW log_entries row AND the source has fts5_index_logs=true. fts_logs is
// append-only (no DELETE) — re-emitting the same log_entries row is a no-op
// here too, so an open scan + a Tail re-read of the same line never
// duplicates the FTS row. fts_ops has no flag gate; this hook is fts_logs-only.
func (w *writer) indexLogEntryFTS(ctx context.Context, tx *sql.Tx, refs logEntryRefs, ev canonical.LogEntryEvent, logID int64, insertedNewLog bool) error {
	if !insertedNewLog || !w.fts5IndexLogs {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO fts_logs (rowid, message, log_id, session_id, op_id, severity, ts)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		logID, ev.Message, logID, refs.sessionID, refs.opID, refs.severity, ev.Ts); err != nil {
		return fmt.Errorf("writer: index log_entry in fts_logs: %w", err)
	}
	return nil
}

// applySourceProgress records the cursor checkpoint and the wall-clock
// timestamp the adapter emitted at. The actual write to source_progress
// is deferred to the worker's flush so it happens once per batch.
func (w *writer) applySourceProgress(ev canonical.SourceProgressEvent) {
	w.lastCursor = ev.Cursor
	w.hasCursor = true
}

// applySourceError records a parse error against the source: increments
// sources.parse_errors (via the shared bumpSourceErrorCounter so
// pricing misses and parse errors land in the same counter) and writes
// a log_entries row with session_id NULL.
func (w *writer) applySourceError(ctx context.Context, tx *sql.Tx, ev canonical.SourceErrorEvent) error {
	if err := w.bumpSourceErrorCounter(ctx, tx, ev.Ts); err != nil {
		return err
	}
	extras, err := json.Marshal(map[string]any{
		"file":   ev.File,
		"offset": ev.Offset,
	})
	if err != nil {
		return fmt.Errorf("writer: marshal source error extras: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (NULL, ?, NULL, NULL, ?, 'ERR', ?, ?, ?)
`+logEntryOnConflict, w.sourceID, ev.Ts, w.sourceFormat, ev.Message, string(extras)); err != nil {
		return fmt.Errorf("writer: insert source error log: %w", err)
	}
	return nil
}

// requireSessionID looks up the canonical session id for nativeID. If
// the session has not yet been observed (events ordered out-of-order),
// a stub row is inserted with start_ts = ts so foreign-key constraints
// on the dependent row succeed. The session row's metadata is filled
// later by the SessionStarted event (UPSERT on conflict).
func (w *writer) requireSessionID(ctx context.Context, tx *sql.Tx, nativeID string, ts int64) (string, error) {
	if nativeID == "" {
		return "", errors.New("writer: empty session native id")
	}
	id, err := w.lookupSessionID(ctx, tx, nativeID)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = canonicalSessionID(w.sourceID, nativeID)
	if _, insErr := tx.ExecContext(ctx, `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id, native_id) DO NOTHING
`,
		id, w.sourceID, nativeID, id, string(canonical.KindRoot), string(canonical.StatusRunning), ts, ts,
	); insErr != nil {
		return "", fmt.Errorf("writer: stub session: %w", insErr)
	}
	w.markDirtySession(id)
	return id, nil
}

// lookupSessionID returns the canonical sessions.id for (sourceID,
// nativeID). Returns sql.ErrNoRows if not present.
func (w *writer) lookupSessionID(ctx context.Context, tx *sql.Tx, nativeID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM sessions WHERE source_id = ? AND native_id = ?`,
		w.sourceID, nativeID).Scan(&id)
	return id, err
}

func (w *writer) markDirtySession(id string) {
	w.dirtySessionIDs[id] = struct{}{}
	w.affectedSessionIDs[id] = struct{}{}
}

func (w *writer) markDirtyTurn(id string) { w.dirtyTurnIDs[id] = struct{}{} }

// markDirtyRollupBucket records that this batch changed a rollup input whose
// op/session-start falls in the UTC bucket containing ts. It marks BOTH the HOUR
// (dirtyRollupBuckets) and the DAY (dirtyRollupDays) the input belongs to, so each
// granularity is an independently-carried set — the day is NOT derived from the
// hour at refresh time (round-8 P1: a derived day is dropped once its hours
// materialize while the day is still open, stranding rollup_daily after the day
// closes). Called ONLY from the three rollup-affecting apply paths:
// applySessionStarted (session-start time), applyOpStarted (op-start time),
// applyOpFinalized (the op's PERSISTED start_ts — finalize changes the op's
// duration/status, so its START bucket, not the end, must be recomputed).
func (w *writer) markDirtyRollupBucket(ts int64) {
	w.dirtyRollupBuckets[rollups.BucketTS(ts, rollups.Hourly)] = struct{}{}
	w.dirtyRollupDays[rollups.BucketTS(ts, rollups.Daily)] = struct{}{}
	w.rollupTouchedThisBatch = true
}

// hasPendingRollupBuckets reports whether any rollup bucket — HOUR or DAY — is
// still awaiting materialization (carried forward because it was open at the last
// refresh). The worker's run loop reads it to decide whether to keep its flush
// timer armed for an idle materialization tick: the timer must stay armed while
// EITHER pending hours OR pending days remain, so a day that closes during a lull
// (after its hours have already materialized and drained the hour set) is still
// materialized by the idle pass — see worker.run.
func (w *writer) hasPendingRollupBuckets() bool {
	return len(w.dirtyRollupBuckets) > 0 || len(w.dirtyRollupDays) > 0
}

// markDirtyOp records that this batch wrote op `id`, so refreshFTS rebuilds its
// fts_ops row from the FINAL persisted ops columns at flush time. Marked by BOTH
// applyOpStarted and applyOpFinalized: either write can change the op's indexed
// text (name/model/provider/tool_namespace) or its error_text (error_class/
// error_message, set at finalize). Rebuilding from the persisted row makes a
// started+finalized-in-one-batch op index its final error text once.
func (w *writer) markDirtyOp(id string) {
	w.dirtyOpIDs[id] = struct{}{}
}

// nullIfEmpty returns a sql.NullString backed by s when s != "" and
// NULL otherwise. Used to keep TEXT columns null when the source did
// not provide a value, so distinct downstream queries can rely on IS
// NULL rather than IS NULL OR = ”.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// marshalExtras encodes a map[string]any to JSON, returning NULL (nil)
// when the map is empty so the column stores SQL NULL. Used by every
// extras_json writer.
func marshalExtras(extras map[string]any) (any, error) {
	if len(extras) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(extras)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// mergeExtras returns the union of the two maps with values from b
// taking precedence on collisions. Nested map[string]any objects are
// merged recursively one level deep — sufficient for the
// aiViewer.parentNativeId / aiViewer.parentOpKey pattern used by the
// SessionStarted writer without pulling in a generic deep-merge.
func mergeExtras(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if av, ok := out[k]; ok {
			am, aIsMap := av.(map[string]any)
			bm, bIsMap := v.(map[string]any)
			if aIsMap && bIsMap {
				merged := make(map[string]any, len(am)+len(bm))
				for kk, vv := range am {
					merged[kk] = vv
				}
				for kk, vv := range bm {
					merged[kk] = vv
				}
				out[k] = merged
				continue
			}
		}
		out[k] = v
	}
	return out
}
