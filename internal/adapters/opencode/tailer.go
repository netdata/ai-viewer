package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file is the POLL-LOOP TAILER (SOW-0005 chunk C): the historical-backfill
// scan loop and the realtime poll loop, plus the pure MAX(time_updated) gating
// predicate and the WAL-fsnotify wakeup hint. It mirrors codex's free-function
// tailer shape (functions that take explicit params, not methods on an Adapter
// struct — chunk D's adapter.go calls these). The DB is ALWAYS opened via the
// chunk-A openReadOnly helper (defer close); the delta-query layer (store_query
// .go) + tree load (store_load.go) do the SQL, the pure mapper turns rows into
// events. The adapter MUST NOT close `out` (the ingester owns it), and every I/O
// respects ctx cancellation (adapter-opencode.md §"Watch Strategy" → "Poll-loop
// state machine…"; canonical/adapter.go).

const (
	// idlePollInterval is the steady-state poll cadence when the previous cycle
	// produced no change (adapter-opencode.md §"Watch Strategy"; SOW Open
	// Decision #2).
	idlePollInterval = 2 * time.Second
	// activePollInterval is the cadence after a cycle that produced a change.
	activePollInterval = 500 * time.Millisecond
	// walFloorInterval is the floor cadence for the walFloorWindow after a WAL
	// fsnotify event.
	walFloorInterval = 250 * time.Millisecond
	// walFloorWindow is how long the 250 ms floor stays active after a WAL event.
	walFloorWindow = 5 * time.Second
	// timeUpdatedSafetyNet is the maximum interval between MAX(time_updated)
	// probes — the safety net that catches in-place mutations even with no WAL
	// fsnotify signal (adapter-opencode.md §"Performance"; AC#6).
	timeUpdatedSafetyNet = 60 * time.Second
	// progressEveryRows checkpoints SourceProgress every N rows paged during the
	// backfill so a restart resumes mid-scan (adapter-opencode.md §"Performance").
	progressEveryRows = 1000
)

// emitProgress publishes a SourceProgressEvent carrying the current cursor,
// sent ctx-aware. Shape copied verbatim from codex/scanner.go emitProgress
// (SourceSeq:0, Ts: time.Now().UnixMicro()).
func emitProgress(ctx context.Context, sourceID string, cur Cursor, out chan<- canonical.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ev := canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        time.Now().UnixMicro(),
		},
		Cursor: cur.String(),
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- ev:
		return nil
	}
}

// emitEvents sends a mapped session's events ctx-aware. Returns ctx.Err() on
// cancellation so the caller stops promptly without deadlocking on the channel.
func emitEvents(ctx context.Context, evs []canonical.Event, out chan<- canonical.Event) error {
	for _, ev := range evs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- ev:
		}
	}
	return nil
}

// scanLoop is the historical backfill. It opens the DB read-only, introspects
// the schema once (recording the hash into the cursor), pages every tracked
// table forward from `since`, derives the affected session ids, loads each
// session's full tree, maps it, and emits the events — checkpointing
// SourceProgress every ~progressEveryRows rows paged AND once at the end so a
// restart resumes mid-backfill. A cold start (`since` zero) walks the entire DB.
// Returns the final advanced cursor.
//
// A missing DB file surfaces one structured error via onError and returns
// (since, nil) so the daemon keeps serving other sources (mirrors codex's
// missing-root handling). ctx cancellation returns ctx.Err() promptly.
func scanLoop(ctx context.Context, dbPath, sourceID string, since Cursor, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) (Cursor, error) {
	logger = orDefaultLogger(logger)
	onError = orNoop(onError)
	db, err := openReadOnly(ctx, dbPath, withMaxOpenConns(2))
	if err != nil {
		// A missing/unreadable file is non-fatal: report once, keep the daemon
		// running for other sources.
		onError(fmt.Errorf("opencode: scan open %s (ro): %w", dbPath, err))
		return since, nil
	}
	defer func() { _ = db.Close() }()

	schema, err := introspectAll(ctx, db)
	if err != nil {
		// An incompatible schema (a required column missing) is fatal for this
		// source — surface it so /api/health shows the failure rather than
		// silently emitting nothing.
		return since, fmt.Errorf("opencode: scan introspect %s: %w", dbPath, err)
	}
	// Surface optional column drift once per (table, column). Scan and Tail each
	// log this set once on every (re)start; that per-phase duplication is
	// acceptable for this rare old-schema path (introspection runs twice — once
	// per phase — by design, see adapter.go Scan→Tail hand-off).
	logMissingColumns(logger, schema)

	cur := recordSchemaHash(ctx, db, coerceScanCursor(since), onError)
	// SourceProgress is checkpointed by the batch processor (commitBatch),
	// per-batch, AFTER each batch's affected sessions are emitted — the
	// checkpoint-after-emit invariant. The trailing emitProgress that used to fire
	// here was a SECOND emit of the same final cursor and is removed (SOW-0005
	// round-2 P3-C: one checkpoint layer only). A backfill that pages any rows ends
	// on a batch that advanced, so at least one checkpoint always fires.
	cur, _, err = processChanges(ctx, db, schema, cur, sourceID, out, logger, onError)
	if err != nil {
		return cur, err
	}
	return cur, nil
}

// tailLoop is the realtime follow. It opens the DB read-only, introspects once,
// sets up a best-effort fsnotify watch on the WAL companion path as a wakeup
// hint, then polls on a timer whose cadence follows the idle/active/WAL-floor
// state machine. Each poll runs the cheap PK-indexed MAX(id) check per table;
// when the gate is open it ALSO runs the expensive MAX(time_updated) probe; on
// any indicated change it runs the delta+reload+emit path and advances the
// cursor. Returns nil on ctx cancellation.
//
// The WAL watch is best-effort: a missing WAL file or watcher error is logged
// once via onError and the loop falls back to pure timer polling (the 60 s
// safety net still guarantees in-place mutations are eventually seen). A watcher
// error never terminates the loop.
func tailLoop(ctx context.Context, dbPath, sourceID string, cur Cursor, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) error {
	logger = orDefaultLogger(logger)
	onError = orNoop(onError)
	db, err := openReadOnly(ctx, dbPath, withMaxOpenConns(2))
	if err != nil {
		// Non-fatal: report once and return cleanly so the daemon keeps serving
		// other sources (mirrors codex's missing-root handling).
		onError(fmt.Errorf("opencode: tail open %s (ro): %w", dbPath, err))
		return nil
	}
	defer func() { _ = db.Close() }()

	schema, err := introspectAll(ctx, db)
	if err != nil {
		return fmt.Errorf("opencode: tail introspect %s: %w", dbPath, err)
	}
	// Surface optional column drift once per (table, column). Scan logs the same
	// set once too; the per-phase duplication on this rare old-schema path is
	// acceptable (introspection runs once per phase by design).
	logMissingColumns(logger, schema)
	cur = recordSchemaHash(ctx, db, coerceScanCursor(cur), onError)

	walEvents, closeWatch := watchWAL(dbPath, onError)
	defer closeWatch()

	st := newPollState()
	timer := time.NewTimer(st.nextInterval(time.Now()))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-walEvents:
			if !ok {
				walEvents = nil // watcher closed; fall back to pure timer polling
				continue
			}
			st.markWALEvent(time.Now())
			resetTimer(timer, st.nextInterval(time.Now()))
		case <-timer.C:
			advanced, perr := pollOnce(ctx, db, schema, &cur, sourceID, &st, out, logger, onError)
			if perr != nil {
				if errors.Is(perr, context.Canceled) || errors.Is(perr, context.DeadlineExceeded) {
					return nil
				}
				// A transient query error is non-fatal: report and keep polling.
				onError(perr)
			}
			st.markCycle(advanced, time.Now())
			resetTimer(timer, st.nextInterval(time.Now()))
		}
	}
}

// pollOnce runs one poll cycle: the cheap MAX(id) change check per table, the
// gated MAX(time_updated) probe, and — when either indicates change — the
// delta+reload+emit path. SourceProgress is checkpointed by the batch processor
// (commitBatch), per batch, AFTER that batch's sessions are emitted; the trailing
// emitProgress that used to fire here was a SECOND emit of the same cursor and is
// removed (SOW-0005 round-2 P3-C: one checkpoint layer only). Returns whether the
// cycle produced a change (so the loop switches to the active cadence).
func pollOnce(ctx context.Context, db *sql.DB, schema schemaSet, cur *Cursor, sourceID string, st *pollState, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) (bool, error) {
	now := time.Now()
	// Capture, BEFORE markProbe mutates the state, whether THIS probe is opened by
	// a WAL write (vs the periodic safety net) AND whether any probe has already
	// completed (priorProbe) — the boundary re-scan keys off both (round-4 P1).
	walDriven := st.lastWALEvent.After(st.lastProbe)
	priorProbe := st.priorProbe
	changed, probed, err := detectChange(ctx, db, schema, *cur, st, now)
	if err != nil {
		return false, err
	}
	if probed {
		st.markProbe(now)
	}
	active := false
	if changed {
		next, advanced, perr := processChanges(ctx, db, schema, *cur, sourceID, out, logger, onError)
		*cur = next
		active = active || advanced
		if perr != nil {
			return active, perr
		}
		return active, nil
	}
	// Boundary-millisecond re-scan (SOW-0005 round-3 P1-1, completed in round-4 P1):
	// re-emit any session touched by an in-place UPDATE at exactly the cursor's
	// boundary ms — the case the cheap MAX(id) path, the gated MAX(time_updated) >
	// gate, and the forward delta's strict `> :tuid` tie-break all miss (changed ==
	// false). It runs on a gate-open probe (probed) under EITHER trigger:
	//   - walDriven: a real opencode write fired the WAL fsnotify hint since the last
	//     probe — the immediate, round-3 path.
	//   - priorProbe: the SAFETY-NET path (round-4 P1). When the WAL hint is missed
	//     (dropped fsnotify event, watcher setup failed, timer-only polling), the 60 s
	//     net still opens the gate; once at least one probe has already run (priorProbe,
	//     captured BEFORE markProbe), a same-ms in-place update is no longer stranded.
	// The cold-Tail guard: the VERY FIRST probe of a fresh Tail has priorProbe==false
	// and (for a HEAD snapshot) is not walDriven, so it does NOT replay the snapshot's
	// boundary session; an INSERT after a cold snapshot advances MAX(id) → changed ==
	// true → this is skipped and the forward delta handles it. AC#6 idle property is
	// preserved: within the 60 s window an idle DB with no WAL event has probed ==
	// false, so this never runs on an idle poll (it fires only when the gate opens —
	// a WAL event or the 60 s net). The cursor is NOT advanced (the boundary rows are
	// already at the watermark); re-emission is idempotent.
	if probed && (walDriven || priorProbe) {
		emitted, berr := emitBoundarySessions(ctx, db, schema, *cur, sourceID, out, logger, onError)
		if berr != nil {
			return active, berr
		}
		active = active || emitted
	}
	return active, nil
}

// detectChange reports whether any tracked table shows new/changed rows since
// the cursor's watermark, using the cheap PK-indexed MAX(id) on every poll and
// the expensive unindexed MAX(time_updated) ONLY when the gate is open
// (shouldProbeTimeUpdated). The second return reports whether the probe ran (so
// the caller records lastProbe). A table on an old schema without time_updated
// is checked by MAX(id) alone.
//
// The cheap path compares MAX(id) against MaxIDSeen — the MONOTONIC high-water,
// NOT the (time_updated, id) paging position (SOW-0005 round-2 P1-A). The
// pre-P1-A code compared against the paging-position id, which an in-place
// UPDATE of an OLD row regressed to a small value, leaving MAX(id) permanently
// greater so this "cheap" path falsely reported a change on every idle poll →
// the expensive (time_updated, id) full scan ran forever. Comparing against the
// never-regressing MaxIDSeen makes an INSERT the only thing this path fires on;
// in-place mutations of existing rows are caught only by the gated
// MAX(time_updated) probe below, exactly as AC#6 intends.
func detectChange(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, st *pollState, now time.Time) (changed, probed bool, err error) {
	// Cheap path: MAX(id) per table vs the monotonic high-water.
	for _, table := range trackedTables {
		mid, mErr := maxID(ctx, db, table)
		if mErr != nil {
			return false, false, mErr
		}
		if mid > cur.Tables[table].MaxIDSeen {
			return true, false, nil
		}
	}
	// Gated expensive path: MAX(time_updated) per table, only when the gate opens.
	if !shouldProbeTimeUpdated(now, st.lastWALEvent, st.lastProbe, timeUpdatedSafetyNet) {
		return false, false, nil
	}
	for _, table := range trackedTables {
		if !schema[table].has("time_updated") {
			continue
		}
		mtu, mErr := maxTimeUpdated(ctx, db, table)
		if mErr != nil {
			return false, true, mErr
		}
		if mtu > cur.Tables[table].MaxTimeUpdatedMs {
			return true, true, nil
		}
	}
	return false, true, nil
}

// shouldProbeTimeUpdated is the PURE gating predicate for the expensive
// MAX(time_updated) probe (AC#6, load-bearing). The probe is issued ONLY when
// (a) a WAL-mtime fsnotify event has fired since the last probe, OR (b) the
// safetyNet interval has elapsed since the last probe. During steady-state idle
// (no WAL event, within the net window) it returns false on every poll, so the
// unindexed full scan never runs — the property the AC#6 test pins.
func shouldProbeTimeUpdated(now, lastWALEvent, lastProbe time.Time, safetyNet time.Duration) bool {
	if lastWALEvent.After(lastProbe) {
		return true
	}
	return now.Sub(lastProbe) >= safetyNet
}

// orNoop returns a no-op onError when the supplied one is nil so adapter code
// can call it unconditionally (mirrors codex/claude_code).
func orNoop(onError func(error)) func(error) {
	if onError == nil {
		return func(error) {}
	}
	return onError
}

// orDefaultLogger guards a nil logger so a direct test caller passing nil does
// not panic. Production always passes a.logger (non-nil after New), so this is
// defence-in-depth, not a hot path.
func orDefaultLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// logMissingColumns emits exactly one INF per wanted-but-absent OPTIONAL column
// across the introspected tables, satisfying AC#5 / adapter-opencode.md
// §"Edge Cases" #1. Required-column loss is fatal upstream (introspectAll), so
// every column reaching here is an optional one the dynamic SELECT silently
// omitted; this surfaces the drift so an operator sees WHY a column reads zero
// on an old opencode database. Iteration is deterministic: tables in
// trackedTables order, columns already sorted by introspectTable
// (sort.Strings(s.Missing)).
func logMissingColumns(logger *slog.Logger, schema schemaSet) {
	for _, table := range trackedTables {
		for _, col := range schema[table].Missing {
			logger.Info("opencode: optional column absent on this database schema; omitted from projection (old opencode version)",
				"table", table, "column", col)
		}
	}
}

// The WAL fsnotify wakeup-hint machinery (watchWAL/closedHintChan) and the
// resetTimer idiom live in tailer_wal.go (split to keep each file ≤400 lines).
