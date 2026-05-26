package aiagent_v3

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestScan_ListLedgersErrorPropagates: when session/ is a regular file,
// the underlying ReadDir fails. scanAll returns an error which Scan
// wraps (non-ctx path).
func TestScan_ListLedgersErrorPropagates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, sessionDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	if err := a.Scan(context.Background(), nil, out); err == nil {
		t.Fatalf("expected error")
	}
}

// TestTail_SnapshotCursorErrorPropagates: same trick — session/ is a
// regular file → snapshotCursor fails → Tail returns wrapped error
// before ever spinning up the watcher.
func TestTail_SnapshotCursorErrorPropagates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, sessionDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	if err := a.Tail(context.Background(), out); err == nil {
		t.Fatalf("expected error")
	}
}

// TestStreamLines_RegressesLastSeqViaOlderRecord: exercise the
// "if Seq > cur.LastSeq" false branch.
func TestStreamLines_LastSeqNotUpdatedWhenStale(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true}` + "\n")
	if err := writeFileBytes(filepath.Join(dir, "a.jsonl"), line); err != nil {
		t.Fatalf("write: %v", err)
	}
	cur := newCursor()
	// Pre-seed with a LastSeq higher than the file's seq=1.
	cur.Files["a.jsonl"] = FileCursor{Offset: 0, Size: int64(len(line)), LastSeq: 99, LastTsUs: int64(1) << 50}
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 8)
	if err := a.Scan(context.Background(), cur, out); err != nil {
		t.Fatalf("scan: %v", err)
	}
}

// TestTail_MkdirAllOnAbsentSessionDir: when session/ does not exist
// yet, Tail creates it before adding the watcher (spec §6.1). Watcher
// must end up watching the new directory.
func TestTail_CreatesMissingSessionDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	// Poll briefly for the directory creation.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dirExists(filepath.Join(root, sessionDir)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Tail did not create session/ within 2s")
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.IsDir()
}
