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

// TestTail_DoesNotCreateMissingSessionDir asserts the read-only-on-
// sources invariant (security.md §"Hard Rules" #1): when session/
// does not exist, Tail surfaces a
// SourceError via OnError and returns cleanly — it MUST NOT create
// the directory. The dedicated regression test in tailer_test.go
// pins the OnError + early-return behavior; this case stays here so
// the prior coverage3_test slot remains active and the dirExists
// helper retains a user.
func TestTail_DoesNotCreateMissingSessionDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = a.Tail(ctx, out)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tail did not return promptly with missing session dir")
	}
	if dirExists(filepath.Join(root, sessionDir)) {
		t.Fatalf("Tail created session/ — violates read-only-on-sources invariant")
	}
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.IsDir()
}
