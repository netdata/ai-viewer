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
		db:        db,
		schema:    schema,
		sourceID:  sourceID,
		out:       out,
		logger:    orDefaultLogger(logger),
		onError:   onError,
		committed: cur, // last cursor whose sessions are fully emitted
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
// set, and reports the row's watermark key. The returned closure matches
// scanTableDelta's onRow type. onError is threaded into the per-table scanner
// (round-4 P2-1): a corrupt OPTIONAL numeric cell surfaces a WARN and degrades to
// 0, while a corrupt REQUIRED watermark/owning-id cell (id/time_updated/session_id)
// returns an ERROR that aborts the page so the cursor never advances to a poisoned
// watermark or an empty affected session. onError ALSO surfaces non-fatal per-row
// anomalies (a session_message with an unknown type — adapter-opencode.md §"Edge
// Cases" #1/§"session_message") without aborting the cycle.
//
// A part's owning session is its REQUIRED denormalized session_id (resolvePartSession;
// session_id is in requiredColumns["part"], so the old-schema message-lookup fallback
// was unreachable and removed — SOW-0005 round-6 P3-2). The message→session map the
// fallback once consulted is therefore gone too.
func deltaRowHandler(table string, s tableSchema, affected *affectedSet, onError func(error)) func(*sql.Rows) (rowKey, error) {
	switch table {
	case "session":
		return sessionDeltaRowHandler(s, affected, onError)
	case "message":
		return messageDeltaRowHandler(s, affected, onError)
	case "part":
		return partDeltaRowHandler(s, affected, onError)
	default: // session_message
		return sessionMessageDeltaRowHandler(s, affected, onError)
	}
}

func sessionDeltaRowHandler(s tableSchema, affected *affectedSet, onError func(error)) func(*sql.Rows) (rowKey, error) {
	scan, row := scanSessionRow(newColumnIndex(s), len(s.Present), onError)
	return func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err == nil {
			affected.add(row.ID)
		}
		return k, err
	}
}

func messageDeltaRowHandler(s tableSchema, affected *affectedSet, onError func(error)) func(*sql.Rows) (rowKey, error) {
	scan, row := scanMessageRow(newColumnIndex(s), len(s.Present), onError)
	return func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err == nil {
			affected.add(row.SessionID)
		}
		return k, err
	}
}

func partDeltaRowHandler(s tableSchema, affected *affectedSet, onError func(error)) func(*sql.Rows) (rowKey, error) {
	scan, row := scanPartRow(newColumnIndex(s), len(s.Present), onError)
	return func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err != nil {
			return k, err
		}
		sid, resolveErr := resolvePartSession(*row)
		if resolveErr != nil {
			return k, resolveErr
		}
		affected.add(sid)
		return k, nil
	}
}

func sessionMessageDeltaRowHandler(s tableSchema, affected *affectedSet, onError func(error)) func(*sql.Rows) (rowKey, error) {
	scan, row := scanSessionMessageRow(newColumnIndex(s), len(s.Present), onError)
	hasType := s.has("type")
	return func(rows *sql.Rows) (rowKey, error) {
		k, err := scan(rows)
		if err != nil {
			return k, err
		}
		affected.add(row.SessionID)
		if hasType {
			warnUnknownSessionMessageType(row.ID, row.Type, onError)
		}
		return k, nil
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
// mapper, and emits the events. ctx cancellation stops promptly.
//
// Error policy (SOW-0005 round-7 P1-2): only TWO outcomes are non-fatal
// skip-and-continue — (1) the `time_compacting` pause (`skipped == true`), which
// re-surfaces in a later delta when the column clears (Edge Cases #8); and (2) a
// session whose row is GONE (`loadAndMapSession` returns nil events with no error
// — deleted between the delta page and the load, or an orphaned part/message),
// surfaced once as `errSessionGone`. ANY OTHER error (a transient tree-load/read
// error, a commit failure, a corrupt-tree decode error) PROPAGATES so the caller
// (commitBatch) does NOT promote the cursor: the same rows are retried next cycle.
// The earlier code swallowed every non-context error (logged + continued) and let
// commitBatch advance the cursor anyway, persisting a watermark beyond rows whose
// content was never emitted — a permanent, health-invisible content loss. Letting
// the error propagate keeps the checkpoint-after-emit invariant: a cursor is
// promoted only after every affected session's content was successfully emitted.
func reloadAndEmit(ctx context.Context, db *sql.DB, schema schemaSet, sourceID string, affected []string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) error {
	for _, sid := range affected {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, skipped, err := loadAndMapSession(ctx, db, schema, sourceID, sid, logger, onError)
		if err != nil {
			// Every load/map/commit error (context or not) propagates: a context error
			// stops the run, and any other transient error must NOT let the cursor
			// advance past rows whose content was not emitted (round-7 P1-2). It is
			// surfaced via onError at the propagation boundary (commitBatch's caller).
			return err
		}
		if skipped {
			// Session paused mid-compaction (time_compacting non-NULL). It is NOT
			// emitted this cycle and re-surfaces in a later delta when the column
			// clears (its time_updated bumps) — adapter-opencode.md §"Edge Cases" #8.
			continue
		}
		if evs == nil {
			// Session row legitimately gone — the only load failure treated as
			// skip-and-continue. Surfaced once as a structured error; the cursor may
			// advance past it (the row is not coming back).
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
	tx, err := beginRO(ctx, db)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	sink := &warnSink{}

	snap, found, skipped, err := readSessionSnapshot(ctx, tx, schema, sessionID, logger, sink)
	if err != nil || !found || skipped {
		rollbackFlush(tx, sink, onError)
		return nil, skipped, err
	}
	if err := commitSessionSnapshot(ctx, tx, sink, sessionID, onError); err != nil {
		return nil, false, err
	}
	evs, err = mapSession(sourceID, snap.session, snap.tree,
		WithRootNativeID(snap.rootID),
		WithSessionMessages(snap.sessionMessages),
		WithOnWarn(onError))
	return evs, false, err
}

type sessionSnapshot struct {
	session         sessionRow
	tree            []messageWithParts
	sessionMessages []sessionMessageRow
	rootID          string
}

func readSessionSnapshot(ctx context.Context, tx *sql.Tx, schema schemaSet, sessionID string, logger *slog.Logger, sink *warnSink) (sessionSnapshot, bool, bool, error) {
	s, ok, err := loadSession(ctx, tx, schema, sessionID, sink.collect)
	if err != nil {
		return sessionSnapshot{}, false, false, err
	}
	if !ok {
		return sessionSnapshot{}, false, false, nil
	}
	if s.TimeCompactingMs > 0 {
		orDefaultLogger(logger).Info("opencode: session compaction in progress; skipping tree emit this cycle (re-emits when time_compacting clears)",
			"session_id", sessionID)
		return sessionSnapshot{}, true, true, nil
	}
	tree, err := loadSessionTree(ctx, tx, schema, sessionID, sink.collect)
	if err != nil {
		return sessionSnapshot{}, false, false, err
	}
	sessionMessages, err := loadSessionMessages(ctx, tx, schema["session_message"], sessionID, sink.collect)
	if err != nil {
		return sessionSnapshot{}, false, false, err
	}
	root := resolveRootID(ctx, tx, s.ID, s.ParentID, sink.collect)
	return sessionSnapshot{session: s, tree: tree, sessionMessages: sessionMessages, rootID: root}, true, false, nil
}

func commitSessionSnapshot(ctx context.Context, tx *sql.Tx, sink *warnSink, sessionID string, onError func(error)) error {
	commitErr := tx.Commit()
	sink.flush(onError)
	if commitErr = normalizeContextSQLError(ctx, commitErr); commitErr != nil {
		return fmt.Errorf("opencode: commit session-read tx for %s: %w", sessionID, commitErr)
	}
	return nil
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
