package opencode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file covers the WAL fsnotify hint (success + missing-dir fallback) and the
// SQL error paths of the delta/load layer (a closed DB surfaces errors rather
// than panicking) — the branches the happy-path and pure-helper tests don't hit.

// TestWatchWAL_FiresOnWrite asserts the WAL watch delivers a hint when the
// companion opencode.db-wal file is written after the watch is established.
func TestWatchWAL_FiresOnWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	walPath := dbPath + "-wal"
	// Create the DB file (so its dir exists) and an initial empty WAL companion.
	if err := os.WriteFile(dbPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	if err := os.WriteFile(walPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	var ce collectErrs
	hint, closeWatch := watchWAL(dbPath, ce.onError)
	defer closeWatch()

	// Append to the WAL to fire a Write event on opencode.db-wal.
	time.Sleep(100 * time.Millisecond) // let the watch establish
	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open wal for append: %v", err)
	}
	_, _ = f.WriteString("frame")
	_ = f.Close()

	select {
	case <-hint:
		// Got the wakeup hint.
	case <-time.After(5 * time.Second):
		t.Fatal("WAL write did not deliver a wakeup hint")
	}
	if ce.count() != 0 {
		t.Errorf("watchWAL on a present WAL surfaced %d errors, want 0", ce.count())
	}
}

// TestWatchWAL_MissingDirFallsBack asserts that when the WAL's parent directory
// does not exist, watchWAL reports one error and returns a closed channel (the
// caller falls back to pure timer polling) without panicking.
func TestWatchWAL_MissingDirFallsBack(t *testing.T) {
	t.Parallel()
	missingDir := filepath.Join(t.TempDir(), "no-such-dir")
	dbPath := filepath.Join(missingDir, "opencode.db")

	var ce collectErrs
	hint, closeWatch := watchWAL(dbPath, ce.onError)
	defer closeWatch()

	// The channel must be closed (drains immediately) so the select in tailLoop
	// nils it and falls back to the timer.
	select {
	case _, ok := <-hint:
		if ok {
			t.Error("expected a closed hint channel for a missing WAL dir")
		}
	case <-time.After(2 * time.Second):
		t.Error("missing-dir hint channel did not drain as closed")
	}
	if ce.count() == 0 {
		t.Error("watchWAL on a missing dir should surface one error")
	}
}

// TestClosedHintChan asserts the helper returns an already-closed channel.
func TestClosedHintChan(t *testing.T) {
	t.Parallel()
	ch := closedHintChan()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("closedHintChan returned an open channel")
		}
	default:
		t.Error("closedHintChan channel did not drain immediately (not closed)")
	}
}

// TestQueryLayer_ClosedDBErrors asserts the cheap probes and the delta scan
// surface an error (not a panic) when the DB handle is closed mid-flight — the
// SQL error branches the happy path skips.
func TestQueryLayer_ClosedDBErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)
	// Close the read-only handle so subsequent queries error.
	if err := db.Close(); err != nil {
		t.Fatalf("close ro db: %v", err)
	}

	if _, err := maxID(ctxBG(), db, "session"); err == nil {
		t.Error("maxID on a closed DB: want error")
	}
	if _, err := maxTimeUpdated(ctxBG(), db, "session"); err == nil {
		t.Error("maxTimeUpdated on a closed DB: want error")
	}
	scan, _ := scanSessionRow(newColumnIndex(schema["session"]), len(schema["session"].Present), nil)
	if _, err := scanTableDelta(ctxBG(), db, schema["session"], TableWatermark{}, scan); err == nil {
		t.Error("scanTableDelta on a closed DB: want error")
	}
	if _, _, err := loadSession(ctxBG(), db, schema, "ses_a", func(error) {}); err == nil {
		t.Error("loadSession on a closed DB: want error")
	}
	if _, err := loadSessionTree(ctxBG(), db, schema, "ses_a", func(error) {}); err == nil {
		t.Error("loadSessionTree on a closed DB: want error")
	}
}

// TestProcessChanges_CollectError asserts processChanges propagates a delta-scan
// error (closed DB) rather than swallowing it.
func TestProcessChanges_CollectError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 1, 1, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db, schema := introspect(t, path)
	if err := db.Close(); err != nil {
		t.Fatalf("close ro db: %v", err)
	}
	out := make(chan canonical.Event, 16)
	if _, _, err := processChanges(ctxBG(), db, schema, newCursor(), "opencode:test", out, silentLogger(), func(error) {}); err == nil {
		t.Error("processChanges over a closed DB: want error")
	}
}
