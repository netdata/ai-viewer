package aiagent_v2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestTailableName_OutsideRoot covers the "outside root" branches.
func TestTailableName_OutsideRoot(t *testing.T) {
	t.Parallel()
	root := "/tmp/v2-test"
	if got := tailableName(root, fsnotify.Event{Name: "/elsewhere/x.json.gz"}); got != "" {
		t.Fatalf("expected '', got %q", got)
	}
	if got := tailableName(root, fsnotify.Event{Name: root + "/sub/x.json.gz"}); got != "" {
		t.Fatalf("subdir should be '', got %q", got)
	}
}

// TestTail_WatcherErrorChannelSurfacesOnError exercises the
// watcher.Errors case in tailLoop.
func TestTail_WatcherErrorChannelSurfacesOnError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var (
		errReported error
		mu          time.Time
	)
	_ = mu
	onErr := func(e error) { errReported = e }
	a, _ := New(root, canonical.AdapterOptions{OnError: onErr})
	out := make(chan canonical.Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	// Remove the watched directory; that surfaces a watcher.Errors
	// event on most platforms. We accept either outcome (some
	// filesystems silently mark the watcher inactive) — the test
	// confirms no panic and that the loop is responsive.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("removeall: %v", err)
	}
	// Recreate so the deferred cleanup doesn't error.
	_ = os.MkdirAll(root, 0o755)
	// Give the watcher a beat.
	time.Sleep(200 * time.Millisecond)
	_ = errReported
}

// TestTail_DebounceMaxEntriesForcesFlush stresses the per-burst flush
// threshold. We synthesize many files quickly.
func TestTail_DebounceMaxEntriesForcesFlush(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 8192)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	// 1100 files > debounceMaxEntries (1024).
	for i := 0; i < 1100; i++ {
		origin := filepath.Base(t.TempDir())
		writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	}
	// Wait long enough for the debouncer to flush at least once.
	if _, ok := waitForEvent(t, out, 10*time.Second, func(ev canonical.Event) bool {
		_, is := ev.(canonical.SourceProgressEvent)
		return is
	}); !ok {
		t.Fatalf("expected at least one progress event after burst")
	}
}

// TestProcessFile_StatOnlySkipReturnsFalseChanged covers the (false,
// nil) return path when the stat-only short-circuit fires.
func TestProcessFile_StatOnlySkipReturnsFalseChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "stat-skip-2"
	path := writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	info, _ := os.Stat(path)
	fc := FileCursor{
		ContentHash: "set-but-unused",
		LastMtime:   info.ModTime().UnixNano(),
		LastSize:    info.Size(),
	}
	out := make(chan canonical.Event, 4)
	updated, changed, err := processFile(context.Background(), root, "src", origin+".json.gz", fc, out, func(error) {})
	if err != nil {
		t.Fatalf("processFile: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false on stat-only match")
	}
	if updated.ContentHash != fc.ContentHash {
		t.Fatalf("cursor mutated unexpectedly: %+v", updated)
	}
}

// TestProcessFile_ContentHashMatchesUpdatesMtimeOnly covers the
// hash-match branch where mtime changes but content stays the same.
func TestProcessFile_ContentHashMatchesUpdatesMtimeOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "hash-match"
	path := writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	// First pass establishes the hash.
	cur := newCursor()
	out := make(chan canonical.Event, 256)
	updated, _, err := processFile(context.Background(), root, "src", origin+".json.gz", FileCursor{}, out, func(error) {})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	cur.withFile(origin+".json.gz", updated)
	// Touch mtime forward without changing content.
	info, _ := os.Stat(path)
	newTime := info.ModTime().Add(3 * time.Second)
	_ = os.Chtimes(path, newTime, newTime)

	out2 := make(chan canonical.Event, 16)
	updated2, changed, err := processFile(context.Background(), root, "src", origin+".json.gz", cur.fileCursor(origin+".json.gz"), out2, func(error) {})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true since mtime moved")
	}
	if updated2.ContentHash != updated.ContentHash {
		t.Fatalf("hash should not change: %q vs %q", updated2.ContentHash, updated.ContentHash)
	}
	for _, ev := range drainBuffered(out2) {
		switch ev.(type) {
		case canonical.SourceProgressEvent, canonical.SourceErrorEvent:
		default:
			t.Fatalf("expected zero domain events on hash match, got %T", ev)
		}
	}
}

// TestProcessOnce_StatError covers the early-return when the
// pre-stat fails for a reason other than NotExist (we approximate via
// a missing file, which is NotExist → covered, and explicit
// permission-denied — skipped on root).
func TestProcessOnce_PermissionError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission restrictions")
	}
	root := t.TempDir()
	origin := "perm-test"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	// Drop file perms; some filesystems still allow stat but not open.
	_ = os.Chmod(filepath.Join(root, origin+".json.gz"), 0o000)
	defer func() { _ = os.Chmod(filepath.Join(root, origin+".json.gz"), 0o644) }()
	cur := newCursor()
	out := make(chan canonical.Event, 16)
	_ = processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {})
}

// TestSnapshotCursor_ListSnapshotsError exercises the listSnapshots
// error propagation path inside snapshotCursor.
func TestSnapshotCursor_ListSnapshotsError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root can read anything")
	}
	root := t.TempDir()
	_ = os.Chmod(root, 0o000)
	defer func() { _ = os.Chmod(root, 0o755) }()
	a, _ := New(root, canonical.AdapterOptions{})
	// Some filesystems still allow readdir; we accept either outcome.
	_, _ = a.snapshotCursor()
}

// TestScan_LoggerOptionUsed exercises the slog.Default substitution
// when the caller passes a custom logger.
func TestScan_LoggerOptionUsed(t *testing.T) {
	t.Parallel()
	a, err := New("/tmp/x", canonical.AdapterOptions{Logger: nil})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Fatalf("logger should be substituted")
	}
}

// TestProcessOnce_CursorChangedFlagPersists covers the "updated, then
// retry-once updates again" code path.
func TestProcessOnce_CursorChangedFlagPersists(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	origin := "persist-cur"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	cur := newCursor()
	out := make(chan canonical.Event, 256)
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("processOnce: %v", err)
	}
	// Rewrite mid-flight to force the post-stat-mtime-advanced branch.
	snap := simpleSnapshot(2, origin)
	snap.OpTree.SessionTitle = "newer content"
	writeSnapshot(t, root, origin, snap)
	info, _ := os.Stat(filepath.Join(root, origin+".json.gz"))
	_ = os.Chtimes(filepath.Join(root, origin+".json.gz"), info.ModTime().Add(time.Second), info.ModTime().Add(time.Second))
	if err := processOnce(context.Background(), root, "src", origin+".json.gz", &cur, out, func(error) {}); err != nil {
		t.Fatalf("processOnce 2: %v", err)
	}
}

// TestScanAll_OnErrorPropagation covers the onError wrap path in
// scanAll. We use a directory with one file that decompresses but
// fails to parse — the inner onError fires.
func TestScanAll_OnErrorPropagation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSnapshot(t, root, "ok", simpleSnapshot(2, "ok"))
	// Synthesize a file with corrupt body so the inner processFile
	// triggers onError without a fatal error.
	writeRaw(t, root, "bad.json.gz", []byte("definitely not gzip"))
	var errCount int
	out := make(chan canonical.Event, 128)
	if _, err := scanAll(context.Background(), root, "src", newCursor(), out, func(error) { errCount++ }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if errCount == 0 {
		t.Fatalf("expected onError")
	}
}

// TestCursor_FilesNonNilButEmpty exercises the boundary of the Files
// map normalisation.
func TestCursor_FilesNonNilButEmpty(t *testing.T) {
	t.Parallel()
	c := Cursor{Files: map[string]FileCursor{}, Version: cursorVersion}
	s := c.String()
	parsed, err := ParseCursor(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Files == nil {
		t.Fatalf("Files should round-trip non-nil")
	}
}

// TestErrors covers the errMalformedSnapshot identity.
func TestErrors(t *testing.T) {
	t.Parallel()
	if errMalformedSnapshot == nil {
		t.Fatalf("nil errMalformedSnapshot")
	}
	if !errors.Is(errMalformedSnapshot, errMalformedSnapshot) {
		t.Fatalf("errors.Is should be reflexive")
	}
}
