package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the SHARED change-processing helper both poll loops use
// (processChanges) and the poll-loop cadence STATE MACHINE (pollState). It is
// split out of tailer.go to keep each file ≤400 lines. processChanges is the one
// "delta → affected sessions → reload → map → emit → checkpoint" pipeline:
// scanLoop runs it once over the whole backfill; tailLoop runs it per change
// cycle.
//
// CHECKPOINT-AFTER-EMIT INVARIANT (SOW-0005 P1.1, data-loss fix): a
// SourceProgress checkpoint carrying cursor W is emitted ONLY after every session
// affected by rows ≤ W in this run has been reloaded, mapped, and emitted. The
// pipeline therefore runs in BOUNDED BATCHES: each batch pages ≤ progressEveryRows
// delta rows forward (advancing the per-table watermark), reloadAndEmits that
// batch's affected sessions, and ONLY THEN checkpoints the batch cursor. A crash
// or ctx-cancel mid-batch returns the LAST fully-committed cursor (the previous
// batch's), never the in-progress batch's scanned watermark — so a restart from
// the persisted cursor can never resume PAST rows whose canonical events were
// never emitted (adapter-opencode.md §"Read Strategy" → "checkpoint-after-emit").

// processChanges pages every tracked table forward from `cur` in bounded batches,
// reloading+emitting each batch's affected sessions BEFORE checkpointing that
// batch's cursor, and returns the advanced cursor + whether anything advanced. It
// is used by BOTH scanLoop (whole backfill) and tailLoop (one cycle). The
// returned cursor is always a checkpoint-safe one: every session for a row it
// covers has been emitted.
//
// Re-emitting an unchanged/partly-changed session is harmless: the ingester's
// idempotent upserts + the post-SOW-0004 idempotent catalog absorb it
// (adapter-opencode.md §"Read Strategy" → "Full-session-tree load + map").
func processChanges(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, sourceID string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) (Cursor, bool, error) {
	bp := &batchProcessor{
		db:         db,
		schema:     schema,
		sourceID:   sourceID,
		out:        out,
		logger:     orDefaultLogger(logger),
		onError:    onError,
		committed:  cur,                 // last cursor whose sessions are fully emitted
		msgSession: map[string]string{}, // accumulates across batches (part→session fallback)
	}
	if err := bp.run(ctx); err != nil {
		// On error/cancel the committed cursor is the last batch whose content was
		// fully emitted — never the in-progress batch's scanned watermark.
		return bp.committed, bp.advanced, err
	}
	return bp.committed, bp.advanced, nil
}

// deltaRowHandler returns the per-row scan callback for one table: it scans the
// row into its typed struct, records the owning session id into the affected
// set, and (for message rows) populates the message→session map the part
// fallback consults. The returned closure matches scanTableDelta's onRow type
// and reports the row's watermark key. onError is threaded into the per-table
// scanner (round-4 P2-1): a corrupt OPTIONAL numeric cell surfaces a WARN and
// degrades to 0, while a corrupt REQUIRED watermark cell (id/time_updated)
// returns an ERROR that aborts the page so the cursor never advances to a
// poisoned watermark. onError ALSO surfaces non-fatal per-row anomalies (a
// session_message with an unknown type — adapter-opencode.md §"Edge Cases"
// #1/§"session_message") without aborting the cycle.
func deltaRowHandler(ctx context.Context, db *sql.DB, table string, s tableSchema, affected *affectedSet, msgSession map[string]string, onError func(error)) func(*sql.Rows) (rowKey, error) {
	idx := newColumnIndex(s)
	n := len(s.Present)
	switch table {
	case "session":
		scan, row := scanSessionRow(idx, n, onError)
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.ID)
			return k, nil
		}
	case "message":
		scan, row := scanMessageRow(idx, n, onError)
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.SessionID)
			if row.ID != "" {
				msgSession[row.ID] = row.SessionID
			}
			return k, nil
		}
	case "part":
		scan, row := scanPartRow(idx, n, onError)
		hasSessionID := s.has("session_id")
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			sid, rerr := resolvePartSession(ctx, db, hasSessionID, *row, msgSession)
			if rerr != nil {
				return k, rerr
			}
			affected.add(sid)
			return k, nil
		}
	default: // session_message
		scan, row := scanSessionMessageRow(idx, n, onError)
		hasType := s.has("type")
		return func(rows *sql.Rows) (rowKey, error) {
			k, err := scan(rows)
			if err != nil {
				return k, err
			}
			affected.add(row.SessionID)
			// Spec Edge #1 (§"session_message"): warn on an unrecognized
			// session_message.type so a new opencode event variant is visible
			// rather than silently absorbed. Introspection-aware: if the schema
			// lacks the type column, skip silently (row.Type is "").
			if hasType {
				warnUnknownSessionMessageType(row.ID, row.Type, onError)
			}
			return k, nil
		}
	}
}

// knownSessionMessageTypes is the set of session_message.type discriminators the
// adapter recognizes today (adapter-opencode.md §"session_message": only
// agent-switched / model-switched ship currently; the upstream union is wider but
// unpopulated). Any other value is forward-compatibility data surfaced via a WARN.
var knownSessionMessageTypes = map[string]struct{}{
	"agent-switched": {},
	"model-switched": {},
}

// warnUnknownSessionMessageType emits one structured WARN via onError for a
// session_message row whose type is not recognized (spec Edge #1). An empty type
// (older schema with the column NULL, or absent) is not flagged — only a present
// but unrecognized value. The row's session_id still drives the affected set
// (the caller already added it), so the tree is reloaded regardless.
func warnUnknownSessionMessageType(id, typ string, onError func(error)) {
	if typ == "" {
		return
	}
	if _, ok := knownSessionMessageTypes[typ]; ok {
		return
	}
	onError(fmt.Errorf("opencode: unknown session_message type %q (table=session_message id=%s); skipping unrecognized event variant", typ, id))
}

// reloadAndEmit loads each affected session's full tree, maps it via the pure
// mapper, and emits the events. A session whose row cannot be loaded (deleted
// between the delta and the load, or an orphaned part/message) is skipped with
// one structured error and the cycle continues with the rest
// (adapter-opencode.md §"Read Strategy"). ctx cancellation stops promptly.
func reloadAndEmit(ctx context.Context, db *sql.DB, schema schemaSet, sourceID string, affected []string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) error {
	for _, sid := range affected {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, skipped, err := loadAndMapSession(ctx, db, schema, sourceID, sid, logger, onError)
		if err != nil {
			if isContextErr(err) {
				return err
			}
			onError(err)
			continue
		}
		if skipped {
			// Session paused mid-compaction (time_compacting non-NULL). It is NOT
			// emitted this cycle and re-surfaces in a later delta when the column
			// clears (its time_updated bumps) — adapter-opencode.md §"Edge Cases" #8.
			continue
		}
		if evs == nil {
			// Session row gone — skipped with one structured error.
			onError(fmt.Errorf("opencode: affected session %s: %w", sid, errSessionGone))
			continue
		}
		if eerr := emitEvents(ctx, evs, out); eerr != nil {
			return eerr
		}
	}
	return nil
}

// rollbackFlush closes the per-session read tx (rolling back the read-only
// snapshot) and THEN flushes the buffered warnings through onError (SOW-0005
// round-5 P2-1) — the early-return chokepoint for loadAndMapSession so no warning
// is emitted with the snapshot held. Rollback-after-an-eventual-commit is a no-op
// (database/sql), and the deferred rollback in the caller is harmless after this.
func rollbackFlush(tx *sql.Tx, sink *warnSink, onError func(error)) {
	_ = tx.Rollback()
	sink.flush(onError)
}

// loadAndMapSession loads one session's row + full ordered tree and maps it,
// returning (events, skipped, error). skipped=true means the session is paused
// mid-compaction (time_compacting non-NULL) and must NOT be emitted this cycle
// (SOW-0005 round-2 P2-E / adapter-opencode.md §"Edge Cases" #8); the caller
// continues without emitting and the session re-surfaces when the column clears.
// A nil event slice with skipped=false (and nil error) means the session row was
// not found (the caller surfaces errSessionGone). The mapper uses the
// deterministic default PayloadRef URI builder (the production tailer path injects
// no builder; defaultPayloadURI is the single source of truth). It also resolves
// the session's TRUE tree root by walking the parent_id chain (resolveRootID,
// SOW-0005 P2.4) and injects it so a nested sub-agent's RootNativeID is the whole
// tree's root rather than its direct parent.
func loadAndMapSession(ctx context.Context, db *sql.DB, schema schemaSet, sourceID, sessionID string, logger *slog.Logger, onError func(error)) (evs []canonical.Event, skipped bool, err error) {
	// ONE read-only transaction for the WHOLE per-session read (SOW-0005 round-3
	// P1-2): the session row, the time_compacting check, the parent-chain root
	// resolution, and the message+part tree all share a single consistent snapshot.
	// Opening a second tx for the tree after checking time_compacting in a first one
	// was a compaction-race TOCTOU — opencode could begin compaction between the two
	// reads and the adapter would emit a partial/mutating tree despite the Edge #8
	// skip rule, and the metadata would come from a different snapshot than the tree.
	tx, err := beginRO(ctx, db)
	if err != nil {
		return nil, false, err
	}
	// No warning EMISSION while this snapshot is open (SOW-0005 round-5 P2-1): the
	// loaders (loadSession/loadSessionTree corrupt-cell + oversized-session WARNs)
	// and resolveRootID (parent-chain WARNs) write into sink — a non-blocking slice
	// append — instead of the live onError, which would send on the (possibly
	// backpressured) out channel and pin the WAL snapshot. The tx is closed FIRST
	// (explicit rollback/commit), THEN sink is flushed through onError, THEN the
	// pure mapper runs and the caller emits the content events — so NEITHER a
	// warning NOR a content event is emitted with the snapshot held. The deferred
	// rollback is a panic-safety net only (a no-op after the explicit close).
	defer func() { _ = tx.Rollback() }()
	sink := &warnSink{}

	s, ok, err := loadSession(ctx, tx, schema, sessionID, sink.collect)
	if err != nil {
		rollbackFlush(tx, sink, onError)
		return nil, false, err
	}
	if !ok {
		rollbackFlush(tx, sink, onError)
		return nil, false, nil
	}
	if s.TimeCompactingMs > 0 {
		// Pause: compaction is reshaping this session's message/part rows, so reading
		// now would emit partial/stale content. Skip emitting this cycle; the session
		// re-appears in a later delta when time_compacting clears (P2-E). The check and
		// the (skipped) tree read are now atomic on this one snapshot (P1-2).
		rollbackFlush(tx, sink, onError)
		orDefaultLogger(logger).Info("opencode: session compaction in progress; skipping tree emit this cycle (re-emits when time_compacting clears)",
			"session_id", sessionID)
		return nil, true, nil
	}
	tree, err := loadSessionTree(ctx, tx, schema, sessionID, sink.collect)
	if err != nil {
		rollbackFlush(tx, sink, onError)
		return nil, false, err
	}
	root := resolveRootID(ctx, tx, s.ID, s.ParentID, sink.collect)
	// Commit the read-only snapshot before mapping (mapping is pure CPU work; holding
	// the snapshot across it would pin the WAL needlessly). A commit failure on a
	// read-only tx is surfaced rather than silently dropped.
	commitErr := tx.Commit()
	// Flush the buffered loader/root warnings now that the snapshot is released
	// (P2-1) — before mapping/emitting, so the ordering is: tx closed → warnings →
	// content events (the caller emits evs).
	sink.flush(onError)
	if commitErr != nil {
		return nil, false, fmt.Errorf("opencode: commit session-read tx for %s: %w", sessionID, commitErr)
	}
	// The mapper runs AFTER the tx is closed, so its own WARNs (mwarn) may go
	// straight to the live onError — any channel send now blocks without the
	// snapshot held.
	evs, err = mapSession(sourceID, s, tree, WithRootNativeID(root), WithOnWarn(onError))
	return evs, false, err
}

// watermarkAdvanced reports whether b is strictly after a on the composite
// (time_updated, id) key — the same order the delta query and cursor.After use.
// Guards against re-recording an unchanged watermark (which would set advanced
// when nothing moved).
func watermarkAdvanced(a, b TableWatermark) bool {
	return cmpWatermark(b, a) > 0
}

// isContextErr reports whether err is a context cancellation/deadline (so the
// reload loop returns it rather than swallowing it via onError).
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
