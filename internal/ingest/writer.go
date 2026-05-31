package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
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

func newWriter(sourceID, sourceFormat, location string, pricer Pricer) *writer {
	return &writer{
		sourceID:           sourceID,
		sourceFormat:       sourceFormat,
		location:           location,
		pricer:             pricer,
		catalog:            newCatalogWriter(pricer),
		dirtySessionIDs:    make(map[string]struct{}),
		dirtyTurnIDs:       make(map[string]struct{}),
		affectedSessionIDs: make(map[string]struct{}),
		pricingMissDedup:   make(map[pricingMissKey]struct{}),
		pendingMissDedup:   make(map[pricingMissKey]struct{}),
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
	w.batchMaxSeq = 0
	w.lastCursor = ""
	w.hasCursor = false
	w.sourceStatusChanged = false
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

// apply dispatches one event to its kind-specific writer.
func (w *writer) apply(ctx context.Context, tx *sql.Tx, ev canonical.Event) error {
	seq := ev.EventSourceSeq()
	if seq > w.batchMaxSeq {
		w.batchMaxSeq = seq
	}
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return w.applySessionStarted(ctx, tx, e)
	case canonical.SessionUpdatedEvent:
		return w.applySessionUpdated(ctx, tx, e)
	case canonical.SessionFinalizedEvent:
		return w.applySessionFinalized(ctx, tx, e)
	case canonical.TurnStartedEvent:
		return w.applyTurnStarted(ctx, tx, e)
	case canonical.TurnFinalizedEvent:
		return w.applyTurnFinalized(ctx, tx, e)
	case canonical.OpStartedEvent:
		return w.applyOpStarted(ctx, tx, e)
	case canonical.OpFinalizedEvent:
		return w.applyOpFinalized(ctx, tx, e)
	case canonical.PayloadRefEvent:
		return w.applyPayloadRef(ctx, tx, e)
	case canonical.LogEntryEvent:
		return w.applyLogEntry(ctx, tx, e)
	case canonical.SourceProgressEvent:
		return w.applySourceProgress(e)
	case canonical.SourceErrorEvent:
		return w.applySourceError(ctx, tx, e)
	default:
		return fmt.Errorf("writer: unknown event kind %s", ev.EventKind())
	}
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
	// extras_json is REPLACED wholesale on conflict EXCEPT the resolver's
	// `aiViewer` stash sub-object, which is grafted back when a stash-free
	// re-emit omits it (SOW-0003 P1.7b). The claude-code sub-agent SessionStarted
	// stashes the child's `toolUseId` in `aiViewer`; a later stash-free session
	// re-emit (e.g. a parent-map re-read) that wholesale-replaced extras would
	// erase that join key and permanently orphan the op→child edge. The graft
	// (json_set, NOT json_patch) preserves the stash without the json_patch
	// null-as-delete hazard (see graftAiViewerExtras).
	// #nosec G202 -- the only interpolated value is graftAiViewerExtras's output,
	// built solely from the compile-time-constant column literal "sessions.extras_json"
	// and the package-const aiViewerStashKeys; no caller/source input reaches the SQL.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (
    id, source_id, native_id, parent_session_id, root_session_id,
    kind, agent_name, model, cwd, call_path, status,
    start_ts, last_activity_ts, extras_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source_id, native_id) DO UPDATE SET
    parent_session_id = COALESCE(sessions.parent_session_id, excluded.parent_session_id),
    root_session_id   = excluded.root_session_id,
    kind              = excluded.kind,
    agent_name        = COALESCE(NULLIF(excluded.agent_name, ''), sessions.agent_name),
    model             = COALESCE(NULLIF(excluded.model, ''), sessions.model),
    cwd               = COALESCE(NULLIF(excluded.cwd, ''), sessions.cwd),
    call_path         = COALESCE(NULLIF(excluded.call_path, ''), sessions.call_path),
    start_ts          = MIN(sessions.start_ts, excluded.start_ts),
    last_activity_ts  = MAX(sessions.last_activity_ts, excluded.last_activity_ts),
    extras_json       = `+graftAiViewerExtras("sessions.extras_json")+`
`,
		id, w.sourceID, ev.NativeID, parentID, rootID,
		kind, nullIfEmpty(ev.AgentName), nullIfEmpty(ev.Model), nullIfEmpty(ev.Cwd), nullIfEmpty(ev.CallPath), string(canonical.StatusRunning),
		ev.Ts, ev.Ts, extrasJSON,
	); err != nil {
		return fmt.Errorf("writer: insert session: %w", err)
	}
	w.markDirtySession(id)
	if err := w.catalog.onSessionStarted(ctx, tx, w.sourceFormat, ev); err != nil {
		return err
	}
	return nil
}

func (w *writer) applySessionUpdated(ctx context.Context, tx *sql.Tx, ev canonical.SessionUpdatedEvent) error {
	id := canonicalSessionID(w.sourceID, ev.NativeID)
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
		ev.AgentName, ev.Model, ev.Cwd, ev.Status, ev.Ts,
		extrasJSON, extrasJSON, extrasJSON,
		id,
	); err != nil {
		return fmt.Errorf("writer: update session: %w", err)
	}
	w.markDirtySession(id)
	return nil
}

func (w *writer) applySessionFinalized(ctx context.Context, tx *sql.Tx, ev canonical.SessionFinalizedEvent) error {
	id := canonicalSessionID(w.sourceID, ev.NativeID)
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
	// Insert if missing (e.g. TurnFinalized arrives without a TurnStarted).
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, end_ts, status, error_class)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))
ON CONFLICT (session_id, seq) DO UPDATE SET
    end_ts      = excluded.end_ts,
    status      = excluded.status,
    error_class = excluded.error_class
`,
		turnID, sessionID, ev.Seq, ev.Ts, nullIfZero(ev.EndTs),
		nonEmpty(ev.Status, string(canonical.StatusCompleted)), ev.ErrorClass,
	); err != nil {
		return fmt.Errorf("writer: finalize turn: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	return nil
}

func (w *writer) applyOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	// Ensure parent turn row exists — synthesize a running turn if no
	// TurnStarted arrived first. This matches the spec: turns may be
	// implicit when the source format doesn't emit explicit start
	// records.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO turns (id, session_id, seq, start_ts, status)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (session_id, seq) DO NOTHING
`, turnID, sessionID, ev.TurnSeq, ev.Ts, string(canonical.StatusRunning)); err != nil {
		return fmt.Errorf("writer: synthesize turn for op: %w", err)
	}
	opID := canonicalOpID(turnID, ev.Seq)
	// Read the op's PRIOR persisted catalog identity + terminal totals BEFORE the
	// upsert so the catalog can (a) count the call ONCE per distinct op (SOW-0004
	// H1a) and (b) MIGRATE the op's contribution off its old key when this re-emit
	// CHANGES the catalog identity (codex MCP enrichment re-stamping
	// tool_namespace/name on the same (turn,seq) — SOW-0004 I1). A re-emitted
	// OpStarted (late enrichment on the same (turn,seq), as the codex/claude_code
	// replay-from-0 + enrichment design emits) is an UPDATE, not a new call. ON
	// CONFLICT DO UPDATE returns RowsAffected=1 for both insert and update under
	// modernc/sqlite, so reading the row first is the authoritative
	// insert-vs-update signal: found=false (sql.ErrNoRows) ⇒ genuine new insert.
	// The OpStarted upsert below touches only identity columns + start_ts/extras —
	// never the status/tokens/cost/duration columns — so the totals read here are
	// exactly the contribution onOpFinalized already booked under the old identity.
	prior, err := w.opPriorIdentity(ctx, tx, opID)
	if err != nil {
		return err
	}
	opInserted := !prior.found
	var parentOpID sql.NullString
	if ev.ParentOpSeq >= 0 {
		parentOpID = sql.NullString{String: canonicalOpID(turnID, ev.ParentOpSeq), Valid: true}
	}
	var childSessionID sql.NullString
	opExtras := ev.Extras
	if ev.ChildSessionNativeID != "" {
		// Only point child_session_id at another session when that row
		// is already in the store; otherwise the FK fires. When the child
		// has not landed yet (the parent transcript is read before, or in a
		// different batch than, the child sidechain), leave the link NULL and
		// stash the child native id in ops.extras_json.aiViewer.childNativeId.
		// The resolver re-links child_session_id from that stash once the
		// child session lands (P1a) — mirroring the session
		// extras_json.aiViewer.parentNativeId stash + resolver pass.
		if cid, err := w.lookupSessionID(ctx, tx, ev.ChildSessionNativeID); err == nil {
			childSessionID = sql.NullString{String: cid, Valid: true}
		} else if errors.Is(err, sql.ErrNoRows) {
			opExtras = mergeExtras(ev.Extras, map[string]any{
				"aiViewer": map[string]any{"childNativeId": ev.ChildSessionNativeID},
			})
		} else {
			return err
		}
	}
	extras, err := marshalExtras(opExtras)
	if err != nil {
		return fmt.Errorf("writer: marshal op extras: %w", err)
	}
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
		opID, turnID, sessionID, parentOpID, ev.Seq,
		string(ev.Kind), ev.Name, nullIfEmpty(ev.ToolNamespace), nullIfEmpty(ev.Model), nullIfEmpty(ev.Provider), nullIfEmpty(ev.ProviderAlias),
		nullIfEmpty(ev.ReasoningKind), ev.Ts, string(canonical.StatusRunning),
		childSessionID, extras,
	); err != nil {
		return fmt.Errorf("writer: insert op: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	if err := w.catalog.onOpStarted(ctx, tx, ev, opInserted, prior); err != nil {
		return err
	}
	return nil
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

func (w *writer) applyOpFinalized(ctx context.Context, tx *sql.Tx, ev canonical.OpFinalizedEvent) error {
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
	}
	turnID := canonicalTurnID(sessionID, ev.TurnSeq)
	opID := canonicalOpID(turnID, ev.Seq)
	// Capture the op's persisted terminal contribution BEFORE the UPDATE below
	// overwrites it, so the catalog can move its rollups by the (new − prior)
	// delta and stay idempotent under a re-emitted / corrected OpFinalized on the
	// same (turn,seq) (SOW-0004 H1a). Absent row ⇒ first finalize ⇒ zero prior ⇒
	// delta equals the full new contribution (unchanged single-emission path).
	prior, err := w.opPriorTotals(ctx, tx, opID)
	if err != nil {
		return err
	}
	// Resolve provider/model/kind and start_ts from the row recorded by the
	// matching OpStartedEvent (or absent until it arrives). Read ONCE here,
	// unconditionally: both the pricer (temporal tier selection) AND the
	// duration computation below need the persisted start_ts. The kind column
	// gates the pricer call (only kind='llm' ops carry priceable tokens).
	// sql.ErrNoRows is expected — the op start may have been ingested in a
	// prior batch or not yet arrived in this scan (orphan finalize); all
	// columns then scan to their zero value (startTs invalid), which is
	// non-fatal. Any OTHER error means the database is unhealthy and silently
	// continuing would violate the "no silent failures" invariant in AGENTS.md.
	var provider, model, kind sql.NullString
	var startTs sql.NullInt64
	if lookupErr := tx.QueryRowContext(ctx, `SELECT provider, model, kind, start_ts FROM ops WHERE id = ?`, opID).
		Scan(&provider, &model, &kind, &startTs); lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return fmt.Errorf("ingest writer: lookup op %s: %w", opID, lookupErr)
	}
	cost := ev.CostUSD
	if cost == 0 && w.pricer != nil {
		// Skip pricing for non-LLM ops (kind != 'llm') and for ops
		// without a provider/model pair — pricing those produces noisy
		// "unknown pricing for provider \"\" model \"\"" warnings that
		// are not actionable. Non-LLM ops legitimately have zero cost
		// (they did not consume tokens). When the OpStarted row is
		// missing entirely (sql.ErrNoRows), all four columns scan to
		// their zero value and isPriceableOp returns false, so we
		// never reach priceOp without a real (provider, model).
		if isPriceableOp(kind, provider, model) {
			// start_ts drives the temporal tier selection so an op
			// straddling a price-change date is priced against the tier
			// in effect when the op STARTED, not ended (the finalize
			// event timestamp). ops.start_ts is NOT NULL per the schema;
			// the guard against zero is defence-in-depth in case a future
			// migration relaxes the constraint, and to match pricing.md.
			pricingTs := ev.Ts
			if startTs.Valid && startTs.Int64 > 0 {
				pricingTs = startTs.Int64
			}
			cost = w.priceOp(ctx, tx, provider.String, model.String, pricingTs, ev)
		}
	}
	// Compute end_ts and duration_us TOGETHER from one validity gate so the two
	// columns can never disagree (data-model.md §ops: duration_us = end_ts - start_ts).
	// A zero or clock-skewed (EndTs < start_ts) incoming end is NOT trusted: both
	// columns are preserved via COALESCE rather than clobbering a previously-recorded
	// good end_ts (e.g. a corrective re-finalize that carries EndTs=0). Duration
	// derives from the PERSISTED start_ts, never ev.Ts (the finalize Ts ≈ the end:
	// a finalize sorts AFTER its OpStarted, so EndTs-ev.Ts ≈ 0). An orphan finalize
	// (start_ts unknown) likewise leaves both invalid → COALESCE preserves the
	// existing values rather than fabricating a duration.
	endTsArg := sql.NullInt64{}
	durUS := sql.NullInt64{}
	if ev.EndTs > 0 && startTs.Valid && startTs.Int64 > 0 && ev.EndTs >= startTs.Int64 {
		endTsArg = sql.NullInt64{Int64: ev.EndTs, Valid: true}
		durUS = sql.NullInt64{Int64: ev.EndTs - startTs.Int64, Valid: true}
	}
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
		endTsArg, durUS, nonEmpty(ev.Status, string(canonical.StatusCompleted)),
		ev.ErrorClass, ev.ErrorMessage,
		ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite, cost,
		ev.BytesIn, ev.BytesOut, ev.CharsIn, ev.CharsOut, ev.CtxUsed, ev.CtxMax,
		opID,
	); err != nil {
		return fmt.Errorf("writer: finalize op: %w", err)
	}
	w.markDirtyTurn(turnID)
	w.markDirtySession(sessionID)
	// Forward the RESOLVED cost (post-pricer) to the catalog rollups so
	// catalog_providers.total_cost_usd / catalog_models.total_cost_usd
	// stay in sync with ops.cost_usd. The pricer mutates `cost` in this
	// function but does NOT touch ev.CostUSD; passing the unmodified ev
	// to onOpFinalized would silently undercount catalog rollups for
	// every op whose cost was computed.
	evForCatalog := ev
	evForCatalog.CostUSD = cost
	if err := w.catalog.onOpFinalized(ctx, tx, opID, evForCatalog, prior); err != nil {
		return err
	}
	return nil
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
	sessionID, err := w.requireSessionID(ctx, tx, ev.SessionNativeID, ev.Ts)
	if err != nil {
		return err
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
		return fmt.Errorf("writer: marshal log extras: %w", err)
	}
	severity := strings.ToUpper(strings.TrimSpace(ev.Severity))
	if severity == "" {
		severity = "INF"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO log_entries (session_id, source_id, turn_id, op_id, ts, severity, source, message, extras_json)
VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?)
`+logEntryOnConflict, sessionID, turnID, opID, ev.Ts, severity, ev.Source, ev.Message, extras); err != nil {
		return fmt.Errorf("writer: insert log_entry: %w", err)
	}
	w.markDirtySession(sessionID)
	return nil
}

// applySourceProgress records the cursor checkpoint and the wall-clock
// timestamp the adapter emitted at. The actual write to source_progress
// is deferred to the worker's flush so it happens once per batch.
func (w *writer) applySourceProgress(ev canonical.SourceProgressEvent) error {
	w.lastCursor = ev.Cursor
	w.hasCursor = true
	return nil
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
