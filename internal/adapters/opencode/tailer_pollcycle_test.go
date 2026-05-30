package opencode

import (
	"context"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file covers the integrated poll-cycle path (pollOnce → detectChange →
// processChanges → emitProgress) and the scanLoop introspect-fatal branch,
// driving real DBs rather than the loop goroutine so the assertions are
// deterministic (no timers).

// TestPollOnce_ProductiveCycle drives pollOnce directly against a DB with a new
// session past the cursor and asserts it emits the session's events + a
// SourceProgress and reports advanced=true.
func TestPollOnce_ProductiveCycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 110, 110, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)

	out := make(chan canonical.Event, 256)
	cur := newCursor()
	st := newPollState()
	advanced, err := pollOnce(ctxBG(), db, schema, &cur, "opencode:test", &st, out, func(error) {})
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if !advanced {
		t.Fatal("pollOnce over a DB with new rows reported advanced=false")
	}
	got := drainAll(out)
	if n := countKind(got, canonical.EvSessionStarted); n != 1 {
		t.Errorf("SessionStarted count = %d, want 1", n)
	}
	if n := countKind(got, canonical.EvSourceProgress); n < 1 {
		t.Errorf("productive pollOnce emitted %d SourceProgress, want >= 1", n)
	}
	// The cursor must have advanced past the inserted rows.
	if cur.Tables["session"].MaxID != "ses_a" {
		t.Errorf("cursor session MaxID = %q, want ses_a", cur.Tables["session"].MaxID)
	}

	// A SECOND pollOnce over the now-current cursor is a no-op (advanced=false,
	// nothing emitted) — proves the cheap MAX(id) gate closes after catch-up.
	out2 := make(chan canonical.Event, 16)
	adv2, err := pollOnce(ctxBG(), db, schema, &cur, "opencode:test", &st, out2, func(error) {})
	if err != nil {
		t.Fatalf("pollOnce (second): %v", err)
	}
	if adv2 {
		t.Error("second pollOnce over an up-to-date cursor reported advanced=true")
	}
	if got := drainAll(out2); len(got) != 0 {
		t.Errorf("second pollOnce emitted %d events, want 0", len(got))
	}
}

// TestScanLoop_IntrospectFatal asserts scanLoop returns a fatal error (not a
// benign skip) when a tracked table is missing a required column — an
// incompatible schema must surface, not silently emit nothing.
func TestScanLoop_IntrospectFatal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// Drop the message.data column dependency by replacing the message table with
	// one lacking the required `data` column.
	if _, err := rw.Exec(`DROP TABLE message`); err != nil {
		t.Fatalf("drop message: %v", err)
	}
	if _, err := rw.Exec(`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
		time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`); err != nil {
		t.Fatalf("recreate message: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	out := make(chan canonical.Event, 16)
	var ce collectErrs
	_, err := scanLoop(ctxBG(), path, "opencode:"+path, newCursor(), out, ce.onError)
	if err == nil {
		t.Fatal("scanLoop over a schema missing a required column: want fatal error")
	}
}

// TestReloadAndEmit_GenericErrorViaOnError asserts a non-context load error for
// one session is surfaced via onError and the loop continues (not returned).
// Triggered by closing the DB AFTER a successful session load is impossible to
// time deterministically, so instead we drive loadAndMapSession's tree path with
// a session whose tree load fails: we delete the session row's table mid-way is
// also racy. Simplest deterministic trigger: an affected id that loads its
// session row but whose part table query errors — emulated by a closed DB, which
// makes loadSession itself error (generic, non-context) → onError, continue.
func TestReloadAndEmit_GenericErrorViaOnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)
	if err := db.Close(); err != nil { // closed → loadSession errors (generic)
		t.Fatalf("close ro db: %v", err)
	}

	out := make(chan canonical.Event, 16)
	var ce collectErrs
	err := reloadAndEmit(ctxBG(), db, schema, "opencode:test", []string{"ses_a"}, out, ce.onError)
	if err != nil {
		t.Fatalf("reloadAndEmit should surface the load error via onError, not return it: %v", err)
	}
	if ce.count() == 0 {
		t.Error("expected a generic load error via onError")
	}
}

// TestTailLoop_WALHintWakesCycle exercises the tailLoop WAL-hint branch end to
// end: with a live WAL companion, a write to it (plus a new row) wakes a cycle
// faster than the idle cadence and the new session surfaces.
func TestTailLoop_WALHintWakesCycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_seed", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	// Reopen WAL-mode so a -wal companion exists for the watch.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw (wal): %v", err)
	}
	defer func() { _ = rw2.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 4096)
	var ce collectErrs
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tailLoop(ctx, path, "opencode:"+path, newCursor(), out, ce.onError)
	}()
	defer func() { cancel(); <-done }()

	// Insert a new session; the WAL write fires the fsnotify hint.
	insertSession(t, rw2, "ses_wal", "", 100, 100, 0)
	if _, ok := waitForSession(out, "ses_wal", 8*time.Second); !ok {
		t.Fatal("tailLoop did not surface a new session with a live WAL companion")
	}
}
