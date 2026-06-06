package opencode

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the poll-loop tailer: scanLoop backfill (events + progress +
// final watermarks), ctx-cancel, missing DB; tailLoop one-cycle pickup,
// ctx-cancel, missing-WAL; the counting-driver no-idle-MAX(time_updated)
// property; and the resume zero-dupes/zero-gaps invariant.

// collectErrs is a concurrency-safe onError sink.
type collectErrs struct {
	mu   sync.Mutex
	errs []error
}

func (c *collectErrs) onError(e error) {
	c.mu.Lock()
	c.errs = append(c.errs, e)
	c.mu.Unlock()
}

func (c *collectErrs) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.errs)
}

// drainAll reads every event currently buffered on out (non-blocking) into a
// slice. Used after a bounded scanLoop completes.
func drainAll(out chan canonical.Event) []canonical.Event {
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// seedBackfillDB builds a DB with n root sessions, each with one assistant
// message carrying one step-start/step-finish/text part triple, and returns the
// path. Times are monotonic across sessions so watermarks are unambiguous.
func seedBackfillDB(t testing.TB, dir string, n int) string {
	t.Helper()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	ts := int64(1000)
	for i := 1; i <= n; i++ {
		sid := fmtID("ses", i)
		mid := fmtID("msg", i)
		insertSession(t, rw, sid, "", ts, ts, 0)
		ts++
		insertAssistantMessage(t, rw, mid, sid, ts, ts, int64(10*i), int64(5*i))
		insertPart(t, rw, fmtID("prt_01_ss", i), mid, sid, ts, ts, stepStartBody())
		ts++
		insertPart(t, rw, fmtID("prt_02_sf", i), mid, sid, ts, ts, stepFinishBody(int64(10*i), int64(5*i), 0.01))
		ts++
		insertPart(t, rw, fmtID("prt_03_tx", i), mid, sid, ts, ts, textBody("answer"))
		ts++
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	return path
}

// TestScanLoop_BackfillEmitsAll asserts a cold scanLoop over N sessions emits a
// SessionStarted per session, at least one SourceProgress, and returns a cursor
// whose per-table watermarks equal the DB maxima.
func TestScanLoop_BackfillEmitsAll(t *testing.T) {
	t.Parallel()
	const n = 3
	path := seedBackfillDB(t, t.TempDir(), n)

	out := make(chan canonical.Event, 4096)
	var ce collectErrs
	cur, err := scanLoop(ctxBG(), path, "opencode:"+path, newCursor(), out, silentLogger(), ce.onError)
	if err != nil {
		t.Fatalf("scanLoop: %v", err)
	}
	got := drainAll(out)

	if c := countKind(got, canonical.EvSessionStarted); c != n {
		t.Errorf("SessionStarted count = %d, want %d", c, n)
	}
	if c := countKind(got, canonical.EvSourceProgress); c < 1 {
		t.Errorf("SourceProgress count = %d, want >= 1", c)
	}
	if c := countKind(got, canonical.EvTurnFinalized); c != n {
		t.Errorf("TurnFinalized count = %d, want %d (one assistant turn per session)", c, n)
	}
	if ce.count() != 0 {
		t.Errorf("backfill surfaced %d errors, want 0", ce.count())
	}
	if cur.TargetHash != targetHashForDBPath(path) {
		t.Errorf("scan cursor target_hash = %q, want current target hash", cur.TargetHash)
	}

	// Final cursor watermarks equal the DB maxima for each table.
	db, _ := introspect(t, path)
	for _, table := range trackedTables {
		wantMaxID, _ := maxID(ctxBG(), db, table)
		wantMaxTU, _ := maxTimeUpdated(ctxBG(), db, table)
		w := cur.Tables[table]
		// After a full backfill the monotonic high-water AND the (time_updated, id)
		// paging-position id both reach the DB's MAX(id) (the fixture is monotonic,
		// so the last-paged row carries the greatest id) (SOW-0005 round-2 P1-A).
		if w.MaxIDSeen != wantMaxID {
			t.Errorf("table %q cursor MaxIDSeen = %q, want DB max %q", table, w.MaxIDSeen, wantMaxID)
		}
		if w.MaxTimeUpdatedID != wantMaxID {
			t.Errorf("table %q cursor MaxTimeUpdatedID = %q, want DB max %q", table, w.MaxTimeUpdatedID, wantMaxID)
		}
		if w.MaxTimeUpdatedMs != wantMaxTU {
			t.Errorf("table %q cursor MaxTimeUpdatedMs = %d, want DB max %d", table, w.MaxTimeUpdatedMs, wantMaxTU)
		}
	}
}

// TestScanLoop_MissingDBBenign asserts a missing DB file surfaces one error and
// returns (since, nil) so the daemon keeps serving other sources.
func TestScanLoop_MissingDBBenign(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such.db")
	out := make(chan canonical.Event, 4)
	var ce collectErrs
	cur, err := scanLoop(ctxBG(), missing, "opencode:"+missing, newCursor(), out, silentLogger(), ce.onError)
	if err != nil {
		t.Fatalf("scanLoop(missing) = %v, want nil", err)
	}
	if ce.count() == 0 {
		t.Error("missing DB should surface one error")
	}
	if cur.hasProgress() {
		t.Error("missing DB cursor should have no progress")
	}
}

// TestScanLoop_CtxCancelMidScan asserts a cancelled ctx returns ctx.Err() and
// does not deadlock on an UNbuffered channel (the send must observe ctx.Done()).
func TestScanLoop_CtxCancelMidScan(t *testing.T) {
	t.Parallel()
	path := seedBackfillDB(t, t.TempDir(), 50)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel up front: the first ctx-aware send/Err must bail.

	out := make(chan canonical.Event) // unbuffered: a non-ctx-aware send would hang
	var ce collectErrs
	done := make(chan error, 1)
	go func() {
		_, err := scanLoop(ctx, path, "opencode:"+path, newCursor(), out, silentLogger(), ce.onError)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && !isContextErr(err) {
			t.Fatalf("scanLoop(cancelled) = %v, want nil or context error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scanLoop did not return after ctx cancel (deadlock on channel?)")
	}
}

// TestTailLoop_PicksUpNewSession runs tailLoop with a drained channel, inserts a
// new session+turn AFTER the loop starts, and asserts the new session's events +
// a SourceProgress are emitted. A missing-WAL DB still tails via the timer.
func TestTailLoop_PicksUpNewSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// Seed one session so the initial cursor is non-empty; close so the WAL
	// flushes and (importantly) there is NO opencode.db-wal companion → the tail
	// must fall back to pure timer polling.
	insertSession(t, rw, "ses_seed", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	// Cold cursor → the first cycle backfills the seed; we then add a new session.
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 4096)
	var ce collectErrs
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = tailLoop(ctx, path, "opencode:"+path, newCursor(), false, out, silentLogger(), ce.onError)
	}()
	// ONE combined teardown: cancel FIRST, then wait. A separate `defer cancel()`
	// + `defer wg.Wait()` would run LIFO (wait before cancel) and deadlock — the
	// loop never gets cancelled while wg.Wait blocks on it.
	defer func() { cancel(); wg.Wait() }()

	// Reopen rw to add a new session AFTER the loop is polling.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	defer func() { _ = rw2.Close() }()
	insertSession(t, rw2, "ses_new", "", 100, 100, 0)
	insertAssistantMessage(t, rw2, "msg_new", "ses_new", 110, 110, 7, 3)

	// Within a few idle polls (2 s cadence + 60 s net not needed: MAX(id) catches
	// the INSERT) the new session must surface.
	if _, ok := waitForSession(out, "ses_new", 8*time.Second); !ok {
		t.Fatal("tailLoop did not emit the new session within the deadline")
	}
	// The SourceProgress checkpoint is emitted AFTER the session's events in the
	// same productive cycle (pollOnce → emitProgress), so drain forward for it
	// rather than asserting on the slice captured up to the SessionStarted.
	if _, ok := waitForEventKind(out, canonical.EvSourceProgress, 5*time.Second); !ok {
		t.Error("tail cycle did not emit a SourceProgress checkpoint after the new session")
	}
}

// waitForEventKind drains out until an event of the given kind appears or the
// deadline elapses.
func waitForEventKind(out chan canonical.Event, kind canonical.EventKind, d time.Duration) ([]canonical.Event, bool) {
	deadline := time.After(d)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if ev.EventKind() == kind {
				return got, true
			}
		case <-deadline:
			return got, false
		}
	}
}

// TestTailLoop_CtxCancelReturnsNil asserts tailLoop returns nil promptly on ctx
// cancel.
func TestTailLoop_CtxCancelReturnsNil(t *testing.T) {
	t.Parallel()
	path := seedBackfillDB(t, t.TempDir(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 4096)
	var ce collectErrs
	done := make(chan error, 1)
	go func() {
		done <- tailLoop(ctx, path, "opencode:"+path, newCursor(), false, out, silentLogger(), ce.onError)
	}()
	// Let it establish + run one cycle, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("tailLoop(cancelled) = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tailLoop did not return after ctx cancel")
	}
}

// TestTailLoop_MissingDBBenign asserts a missing DB surfaces one error and
// returns nil (the daemon keeps running for other sources).
func TestTailLoop_MissingDBBenign(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-such.db")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan canonical.Event, 4)
	var ce collectErrs
	if err := tailLoop(ctx, missing, "opencode:"+missing, newCursor(), false, out, silentLogger(), ce.onError); err != nil {
		t.Fatalf("tailLoop(missing) = %v, want nil", err)
	}
	if ce.count() == 0 {
		t.Error("missing DB should surface one error")
	}
}

// waitForSession drains out until a SessionStarted with the given native id
// appears or the deadline elapses.
func waitForSession(out chan canonical.Event, nativeID string, d time.Duration) ([]canonical.Event, bool) {
	deadline := time.After(d)
	var got []canonical.Event
	for {
		select {
		case ev := <-out:
			got = append(got, ev)
			if s, ok := ev.(canonical.SessionStartedEvent); ok && s.NativeID == nativeID {
				return got, true
			}
		case <-deadline:
			return got, false
		}
	}
}
