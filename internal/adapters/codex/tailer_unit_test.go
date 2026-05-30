package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// newWatcherT builds an fsnotify watcher closed on test cleanup.
func newWatcherT(t *testing.T) *fsnotify.Watcher {
	t.Helper()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify watcher: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// TestHandleEvent_WriteMarksModernDirty asserts a Write on a modern rollout
// marks its rel dirty, while a Write on a legacy/ignored/non-rollout file does
// not.
func TestHandleEvent_WriteMarksModernDirty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	watched := map[string]struct{}{}
	dirty := map[string]struct{}{}
	w := newWatcherT(t)

	modern := filepath.Join(resolved, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	handleEvent(w, resolved, fsnotify.Event{Name: modern, Op: fsnotify.Write}, watched, dirty, func(error) {})
	rel := "2025/11/20/rollout-2025-11-20T10-00-00-" + uuid7(1) + ".jsonl"
	if _, ok := dirty[rel]; !ok {
		t.Fatalf("modern write did not mark %q dirty; dirty=%v", rel, dirty)
	}

	// A legacy .json at the root is NOT a modern rollout → not dirtied.
	legacy := filepath.Join(resolved, "rollout-2025-06-26-abc.json")
	handleEvent(w, resolved, fsnotify.Event{Name: legacy, Op: fsnotify.Write}, watched, dirty, func(error) {})
	// A sqlite/history file → not dirtied.
	handleEvent(w, resolved, fsnotify.Event{Name: filepath.Join(resolved, "state_5.sqlite"), Op: fsnotify.Write}, watched, dirty, func(error) {})
	if len(dirty) != 1 {
		t.Errorf("non-modern writes dirtied something; dirty=%v", dirty)
	}
}

// TestHandleEvent_RemoveRenameLogged asserts a Remove/Rename on a modern rollout
// surfaces a SourceError (logged, not acted on) and does not dirty anything.
func TestHandleEvent_RemoveRenameLogged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	watched := map[string]struct{}{}
	dirty := map[string]struct{}{}
	w := newWatcherT(t)
	var errs []string

	modern := filepath.Join(resolved, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	handleEvent(w, resolved, fsnotify.Event{Name: modern, Op: fsnotify.Remove}, watched, dirty, func(e error) { errs = append(errs, e.Error()) })
	if len(dirty) != 0 {
		t.Errorf("remove dirtied a file; dirty=%v", dirty)
	}
	if len(errs) == 0 || !strings.Contains(errs[0], "removed/renamed") {
		t.Errorf("remove not logged; errs=%v", errs)
	}
	// A Rename on a non-rollout name logs nothing.
	errs = nil
	handleEvent(w, resolved, fsnotify.Event{Name: filepath.Join(resolved, "notes.txt"), Op: fsnotify.Rename}, watched, dirty, func(e error) { errs = append(errs, e.Error()) })
	if len(errs) != 0 {
		t.Errorf("rename of non-rollout logged; errs=%v", errs)
	}
}

// TestHandleEvent_CreateDirWatchesAndDirties asserts a Create on a directory
// adds it to the watch set and dirties any rollout already inside it (the
// create-race window), and that an archive dir Create is ignored.
func TestHandleEvent_CreateDirWatchesAndDirties(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	watched := map[string]struct{}{}
	dirty := map[string]struct{}{}
	w := newWatcherT(t)

	// Create a new shard dir on disk with a rollout already inside it.
	shard := filepath.Join(resolved, "2025", "12", "31")
	inside := filepath.Join(shard, "rollout-2025-12-31T10-00-00-"+uuid7(2)+".jsonl")
	writeFileBytes(t, inside, completeSession("sid-x"))
	handleEvent(w, resolved, fsnotify.Event{Name: shard, Op: fsnotify.Create}, watched, dirty, func(error) {})
	if _, ok := watched[shard]; !ok {
		t.Errorf("created dir not watched; watched=%v", watched)
	}
	rel := "2025/12/31/rollout-2025-12-31T10-00-00-" + uuid7(2) + ".jsonl"
	if _, ok := dirty[rel]; !ok {
		t.Errorf("rollout inside created dir not dirtied; dirty=%v", dirty)
	}

	// An archived_sessions dir Create is ignored (never watched).
	arch := filepath.Join(resolved, archivedSessionsDir)
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatalf("mkdir arch: %v", err)
	}
	handleEvent(w, resolved, fsnotify.Event{Name: arch, Op: fsnotify.Create}, watched, dirty, func(error) {})
	if _, ok := watched[arch]; ok {
		t.Error("archived_sessions dir must not be watched")
	}
}

// TestRolloutForRel covers the recognized-modern, legacy-rejected, and
// ignored-name branches.
func TestRolloutForRel(t *testing.T) {
	t.Parallel()
	resolved := "/sessions"
	r, ok := rolloutForRel(resolved, "2025/11/20/rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	if !ok || r.abs != filepath.Join(resolved, "2025", "11", "20", "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl") {
		t.Fatalf("rolloutForRel modern = (%+v,%v)", r, ok)
	}
	if _, ok := rolloutForRel(resolved, "rollout-2025-06-26-abc.json"); ok {
		t.Error("legacy .json must not map to a modern rollout")
	}
	if _, ok := rolloutForRel(resolved, "session_index.jsonl"); ok {
		t.Error("ignored name must not map to a rollout")
	}
}

// TestRelOrBase covers both branches: a path under base (rel) and a path
// outside base (basename fallback).
func TestRelOrBase(t *testing.T) {
	t.Parallel()
	if got := relOrBase("/a/b", "/a/b/c.jsonl"); got != "c.jsonl" {
		t.Errorf("relOrBase in-base = %q, want c.jsonl", got)
	}
	// A path on a different absolute subtree still produces a rel via filepath.Rel
	// on POSIX ("../x"); the basename fallback only triggers on a Rel error
	// (cross-volume), which is platform-specific. Assert the in-base case and
	// that the function never panics on an unrelated path.
	_ = relOrBase("/a/b", "/totally/other/x.jsonl")
}

// TestMarkExistingDirty_PrunesArchiveAndIgnores asserts markExistingDirty marks
// modern rollouts dirty, skips legacy/ignored names, and prunes the archive
// subtree.
func TestMarkExistingDirty_PrunesArchiveAndIgnores(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	shard := filepath.Join(resolved, "2025", "11", "20")
	writeFileBytes(t, filepath.Join(shard, "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl"), completeSession("sid-a"))
	writeFileBytes(t, filepath.Join(shard, "rollout-x.json"), []byte("{}")) // ignored (legacy ext inside shard)
	writeFileBytes(t, filepath.Join(shard, "notes.jsonl"), []byte("{}"))    // ignored (wrong prefix)
	writeFileBytes(t, filepath.Join(shard, archivedSessionsDir, "rollout-2025-11-20T10-00-00-"+uuid7(2)+".jsonl"), completeSession("sid-arch"))

	dirty := map[string]struct{}{}
	markExistingDirty(resolved, shard, dirty, func(error) {})
	if len(dirty) != 1 {
		t.Fatalf("markExistingDirty dirtied %d, want 1 (only the modern rollout); dirty=%v", len(dirty), dirty)
	}
	rel := "2025/11/20/rollout-2025-11-20T10-00-00-" + uuid7(1) + ".jsonl"
	if _, ok := dirty[rel]; !ok {
		t.Errorf("modern rollout not dirtied; dirty=%v", dirty)
	}
}

// TestAddWatchTree_AddsDirsSkipsSymlink asserts addWatchTree Add()s the real
// shard dirs under the root and does NOT descend a symlinked entry (WalkDir does
// not follow symlinks: an escaping symlink is therefore never watched, and the
// per-path containment guard — exercised directly in
// TestWithinSourceRoot_SurfacesEscape — defends a real dir whose resolved path
// escapes).
func TestAddWatchTree_AddsDirsSkipsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	shard := filepath.Join(resolved, "2025", "11", "20")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	// A symlinked dir at the root pointing OUTSIDE the sessions root.
	outside := t.TempDir()
	escape := filepath.Join(resolved, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	w := newWatcherT(t)
	watched := map[string]struct{}{}
	addWatchTree(w, resolved, resolved, watched, func(error) {})
	if _, ok := watched[shard]; !ok {
		t.Errorf("shard dir not watched; watched=%v", watched)
	}
	// The symlinked entry is not a directory to WalkDir, so it is never watched.
	if _, ok := watched[escape]; ok {
		t.Error("symlinked entry must not be watched (WalkDir does not follow symlinks)")
	}
}

// TestAddWatchTree_PruneArchiveAndDedup covers addWatchTree's archive-prune
// branch (an archived_sessions subtree is never watched) and the
// already-watched dedup branch (a second call adds nothing new).
func TestAddWatchTree_PruneArchiveAndDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	shard := filepath.Join(resolved, "2025", "11", "20")
	arch := filepath.Join(resolved, archivedSessionsDir, "2025", "11", "20")
	for _, d := range []string{shard, arch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	w := newWatcherT(t)
	watched := map[string]struct{}{}
	addWatchTree(w, resolved, resolved, watched, func(error) {})
	if _, ok := watched[shard]; !ok {
		t.Error("shard dir not watched")
	}
	if _, ok := watched[arch]; ok {
		t.Error("archived_sessions subtree must not be watched")
	}
	// A second call must not re-Add (dedup branch); watched set size unchanged.
	before := len(watched)
	addWatchTree(w, resolved, resolved, watched, func(error) {})
	if len(watched) != before {
		t.Errorf("second addWatchTree changed watched set: %d → %d", before, len(watched))
	}
}

// TestCatchUpFromCursor_DiscoveryError covers catchUpFromCursor's non-fatal
// discovery-error branch: an unreadable root surfaces a SourceError and returns
// nil (the watch loop still runs). Skipped where 0o000 is ignored.
func TestCatchUpFromCursor_DiscoveryError(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, derr := os.ReadDir(root); derr == nil {
		t.Skip("filesystem allowed reading a 0o000 dir; discovery-error seam not exercised")
	}

	resolved := root // resolve will fail inside; pass root as the resolved arg too
	var errs []string
	cur := newCursor()
	err := catchUpFromCursor(context.Background(), resolved, root, "codex:"+root, &cur, make(chan canonical.Event, 4), func(e error) { errs = append(errs, e.Error()) })
	if err != nil {
		t.Fatalf("catchUpFromCursor discovery error should be non-fatal, got %v", err)
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "tail catch-up discovery") {
			found = true
		}
	}
	if !found {
		t.Errorf("catch-up discovery error not surfaced; errs=%v", errs)
	}
}

// TestAddWatchTree_WalkErrorSurfaced asserts a non-IsNotExist walk error over an
// unreadable subtree is surfaced and pruned (fail-soft). Skipped on filesystems
// that allow descending a 0o000 dir.
func TestAddWatchTree_WalkErrorSurfaced(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	blocked := filepath.Join(resolved, "2025", "11", "20", "deep")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(filepath.Dir(blocked), 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(blocked), 0o755) })
	if _, derr := os.ReadDir(filepath.Dir(blocked)); derr == nil {
		t.Skip("filesystem allowed descending an unreadable dir; walk-error seam not exercised")
	}

	w := newWatcherT(t)
	watched := map[string]struct{}{}
	var errs []string
	addWatchTree(w, resolved, resolved, watched, func(e error) { errs = append(errs, e.Error()) })
	found := false
	for _, e := range errs {
		if strings.Contains(e, "walk watch tree") {
			found = true
		}
	}
	if !found {
		t.Errorf("unreadable subtree walk error not surfaced; errs=%v", errs)
	}
}
