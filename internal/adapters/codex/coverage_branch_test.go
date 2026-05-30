package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestStreamLines_SkipAndReplaySuppress covers two streamLines branches: a
// skip=true line (ghost_snapshot) is passed over, and a resume with emitFrom>0
// replays early lines to rebuild state but emits nothing for them (the !emit
// branch).
func TestStreamLines_SkipAndReplaySuppress(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		metaLine("sid-s", `"exec"`),
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"ghost_snapshot"}}`, // skip
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
	}, "\n") + "\n"

	// First, a full pass (emitFrom=0) to learn the byte offset after line 1.
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid-s"})
	out := make(chan canonical.Event, 64)
	full, err := streamLines(context.Background(), strings.NewReader(src), 0, "r.jsonl", m, newUnknownDedup(), out, func(error) {})
	if err != nil {
		t.Fatalf("full streamLines: %v", err)
	}
	_ = drainBuffered(out)
	if full.advanced != int64(len(src)) {
		t.Fatalf("full advanced = %d, want %d", full.advanced, len(src))
	}

	// Now replay with emitFrom past the session_meta line so it is rebuilt but
	// NOT re-emitted (the !emit branch), and the ghost_snapshot skip still runs.
	metaLen := int64(len(metaLine("sid-s", `"exec"`)) + 1)
	m2 := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid-s"})
	out2 := make(chan canonical.Event, 64)
	res, err := streamLines(context.Background(), strings.NewReader(src), metaLen, "r.jsonl", m2, newUnknownDedup(), out2, func(error) {})
	if err != nil {
		t.Fatalf("replay streamLines: %v", err)
	}
	got := drainBuffered(out2)
	// The session_meta line was below emitFrom → no SessionStarted re-emitted.
	if countKind(got, canonical.EvSessionStarted) != 0 {
		t.Errorf("replay re-emitted SessionStarted (%d); want 0 (below emitFrom)", countKind(got, canonical.EvSessionStarted))
	}
	// But the turn_context above emitFrom did emit a TurnStarted.
	if countKind(got, canonical.EvTurnStarted) == 0 {
		t.Error("replay did not emit the above-emitFrom TurnStarted")
	}
	if res.advanced != int64(len(src)) {
		t.Errorf("replay advanced = %d, want %d", res.advanced, len(src))
	}
}

// TestRelPath_Error covers relPath's error branch via a relative base (Rel of
// an absolute target against a relative base fails).
func TestRelPath_Error(t *testing.T) {
	t.Parallel()
	if _, err := relPath("relative-base", "/absolute/target"); err == nil {
		t.Error("relPath(relative base, absolute target) should error")
	}
}

// TestFileCursor_NilMap covers fileCursor's nil-map branch.
func TestFileCursor_NilMap(t *testing.T) {
	t.Parallel()
	var c Cursor // Files is nil
	if fc := c.fileCursor("any"); fc.Offset != 0 {
		t.Errorf("nil-map fileCursor = %+v, want zero", fc)
	}
}

// TestFirstRecordIsSessionMeta_BlankOnly covers the blank-line-then-EOF path:
// a file of only blank lines has no parseable record → false.
func TestFirstRecordIsSessionMeta_BlankOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "r.jsonl")
	if err := os.WriteFile(path, []byte("\n\n   \n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	info, _ := f.Stat()
	got, perr := firstRecordIsSessionMeta(f, info.Size())
	if perr != nil || got {
		t.Errorf("blank-only probe = (%v,%v), want (false,nil)", got, perr)
	}
}

// TestMarkExistingDirty_WalkErrorSurfaced covers markExistingDirty's
// non-IsNotExist walk-error branch via a chmod-000 subtree. Skipped where 0o000
// is ignored.
func TestMarkExistingDirty_WalkErrorSurfaced(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	newDir := filepath.Join(resolved, "2025", "12", "01")
	deep := filepath.Join(newDir, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(deep, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(deep, 0o755) })
	if _, derr := os.ReadDir(deep); derr == nil {
		t.Skip("filesystem allowed reading a 0o000 dir; walk-error seam not exercised")
	}

	dirty := map[string]struct{}{}
	var errs []string
	markExistingDirty(resolved, newDir, dirty, func(e error) { errs = append(errs, e.Error()) })
	found := false
	for _, e := range errs {
		if strings.Contains(e, "walk new dir") {
			found = true
		}
	}
	if !found {
		t.Errorf("markExistingDirty walk-error not surfaced; errs=%v", errs)
	}
}

// TestMarkExistingDirty_NilOnError asserts the nil-onError default is installed
// (no panic when called with a nil callback).
func TestMarkExistingDirty_NilOnError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	shard := filepath.Join(resolved, "2025", "11", "20")
	writeFileBytes(t, filepath.Join(shard, "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl"), completeSession("sid"))
	dirty := map[string]struct{}{}
	markExistingDirty(resolved, shard, dirty, nil) // nil onError must not panic
	if len(dirty) != 1 {
		t.Errorf("dirty count = %d, want 1", len(dirty))
	}
}

// TestDiscoverRollouts_NilOnError asserts the nil-onError default (no panic) and
// returns the modern file.
func TestDiscoverRollouts_NilOnError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFileBytes(t, shardPath(root, uuid7(1)), completeSession("sid"))
	disc, err := discoverRollouts(root, nil)
	if err != nil {
		t.Fatalf("discoverRollouts: %v", err)
	}
	if len(disc.modern) != 1 {
		t.Errorf("modern = %d, want 1", len(disc.modern))
	}
}
