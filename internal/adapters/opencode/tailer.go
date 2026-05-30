package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

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
	cur, _, err = processChanges(ctx, db, schema, cur, sourceID, out, onError)
	if err != nil {
		return cur, err
	}
	if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
		return cur, perr
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
			advanced, perr := pollOnce(ctx, db, schema, &cur, sourceID, &st, out, onError)
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
// delta+reload+emit path with a SourceProgress checkpoint. Returns whether the
// cycle produced a change (so the loop switches to the active cadence).
func pollOnce(ctx context.Context, db *sql.DB, schema schemaSet, cur *Cursor, sourceID string, st *pollState, out chan<- canonical.Event, onError func(error)) (bool, error) {
	now := time.Now()
	changed, probed, err := detectChange(ctx, db, schema, *cur, st, now)
	if err != nil {
		return false, err
	}
	if probed {
		st.markProbe(now)
	}
	if !changed {
		return false, nil
	}
	next, advanced, perr := processChanges(ctx, db, schema, *cur, sourceID, out, onError)
	*cur = next
	if perr != nil {
		return advanced, perr
	}
	if advanced {
		if cperr := emitProgress(ctx, sourceID, *cur, out); cperr != nil {
			return advanced, cperr
		}
	}
	return advanced, nil
}

// detectChange reports whether any tracked table shows new/changed rows since
// the cursor's watermark, using the cheap PK-indexed MAX(id) on every poll and
// the expensive unindexed MAX(time_updated) ONLY when the gate is open
// (shouldProbeTimeUpdated). The second return reports whether the probe ran (so
// the caller records lastProbe). A table on an old schema without time_updated
// is checked by MAX(id) alone.
func detectChange(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, st *pollState, now time.Time) (changed, probed bool, err error) {
	// Cheap path: MAX(id) per table.
	for _, table := range trackedTables {
		mid, mErr := maxID(ctx, db, table)
		if mErr != nil {
			return false, false, mErr
		}
		if mid > cur.Tables[table].MaxID {
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

// watchWAL sets up a best-effort fsnotify watch on the opencode.db-wal companion
// path and returns a channel that fires (a bare struct{}) on each Write/Chmod
// event plus a close func. It is a WAKEUP HINT ONLY: a missing WAL file, an Add
// failure, or a watcher error is reported once via onError and yields a closed
// channel so the caller falls back to pure timer polling. The watch is on the
// PARENT directory (the WAL file may not exist yet, and watching a not-yet-
// existing file fails); events are filtered to the WAL basename.
func watchWAL(dbPath string, onError func(error)) (<-chan struct{}, func()) {
	walPath := dbPath + "-wal"
	dir := filepath.Dir(walPath)
	walBase := filepath.Base(walPath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		onError(fmt.Errorf("opencode: wal watcher (falling back to timer polling): %w", err))
		return closedHintChan(), func() {}
	}
	if aerr := watcher.Add(dir); aerr != nil {
		onError(fmt.Errorf("opencode: watch wal dir %s (falling back to timer polling): %w", dir, aerr))
		_ = watcher.Close()
		return closedHintChan(), func() {}
	}

	hint := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(hint)
		for {
			select {
			case <-done:
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != walBase {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Chmod|fsnotify.Create) == 0 {
					continue
				}
				// Non-blocking notify: a pending hint already wakes the next poll.
				select {
				case hint <- struct{}{}:
				default:
				}
			case werr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				// A watcher error never terminates the tail loop; report and
				// keep the watch (or let the timer net carry it).
				onError(fmt.Errorf("opencode: wal watcher error: %w", werr))
			}
		}
	}()

	closeWatch := func() {
		close(done)
		_ = watcher.Close()
	}
	return hint, closeWatch
}

// closedHintChan returns an already-closed hint channel, used when the WAL watch
// cannot be established so the tail loop falls back to pure timer polling.
func closedHintChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// resetTimer safely resets a timer to fire after d, draining any pending fire so
// the next select sees only the new deadline (the standard time.Timer reset
// idiom).
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
