package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestStreamLines_CtxCancelAtLoopTop covers streamLines' ctx.Err() check at the
// top of the loop (a context cancelled before any line is read returns
// immediately with the cancel error and a zero advanced offset).
func TestStreamLines_CtxCancelAtLoopTop(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", nativeID: "sid"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 4)
	res, err := streamLines(ctx, nil, 0, "r.jsonl", m, newUnknownDedup(), out, func(error) {})
	if err == nil {
		t.Fatal("streamLines on a cancelled ctx should return the cancel error at the loop top")
	}
	if res.advanced != 0 {
		t.Errorf("advanced = %d, want 0 (nothing read before cancel)", res.advanced)
	}
}

// TestFirstRecordIsSessionMeta_SeekError covers the Seek-error branch of the
// rule-#24 probe: a closed file fails to Seek, surfacing the error.
func TestFirstRecordIsSessionMeta_SeekError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "r.jsonl")
	if err := os.WriteFile(path, completeSession("sid"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	size := int64(100)
	_ = f.Close() // close BEFORE probing so Seek fails on the closed fd
	if _, perr := firstRecordIsSessionMeta(f, size); perr == nil {
		t.Fatal("firstRecordIsSessionMeta on a closed file should return a Seek error")
	}
}

// TestScan_CancelMidWalkReturnsCursor covers scanAll's ctx.Err() check between
// files: a context cancelled before the walk reaches a file returns the cursor
// and the cancel error path (scanAll returns nil from Scan via the adapter, but
// scanAll itself returns the ctx error so the caller can decide).
func TestScan_CancelMidWalkReturnsCursor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-"+pad2(i)+"-"+uuid7(i)+".jsonl")
		writeFileBytes(t, p, completeSession("sid-"+pad2(i)))
		setMtime(t, p, time.Minute)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 256)
	cur, err := scanAll(ctx, root, "codex:"+root, newCursor(), out, func(error) {})
	if err == nil {
		t.Fatal("scanAll with cancelled ctx should return the cancel error")
	}
	// The returned cursor is the best-effort resume point (empty here since the
	// walk was cancelled before reading).
	_ = cur
}

// TestScan_NilFilesCursor covers scanAll's `cur.Files == nil` initialisation
// branch (a zero Cursor handed in is upgraded to a fresh cursor).
func TestScan_NilFilesCursor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-z"))
	setMtime(t, path, time.Minute)
	events, _, final := scanCollect(t, root, "codex:"+root, Cursor{}) // nil Files
	if !hasKind(events, canonical.EvSessionStarted) {
		t.Error("scan with a nil-Files cursor must still ingest")
	}
	if final.Files == nil {
		t.Error("scanAll must initialise the cursor's Files map")
	}
}

// TestReadRollout_ContainmentRefusesEscape covers readRollout's own containment
// guard (scanner.go:270-282): a rollout descriptor whose abs is a symlink
// escaping the root (as can reach readRollout via the Tail flush path, which
// has no prior discovery check) is refused with a SourceError and never opened.
func TestReadRollout_ContainmentRefusesEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.jsonl")
	writeFileBytes(t, secret, completeSession("sid-secret"))
	shardDir := filepath.Join(resolved, "2025", "11", "20")
	if err := os.MkdirAll(shardDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rel := "2025/11/20/rollout-2025-11-20T10-00-00-" + uuid7(1) + ".jsonl"
	link := filepath.Join(shardDir, "rollout-2025-11-20T10-00-00-"+uuid7(1)+".jsonl")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	out := make(chan canonical.Event, 8)
	r := rollout{rel: rel, abs: link}
	_, n, err := readRollout(context.Background(), resolved, r, "codex:"+root, FileCursor{}, out, func(error) {}, nil)
	if err == nil {
		t.Fatal("readRollout must refuse a symlink escaping the root")
	}
	if n != 0 {
		t.Errorf("escaped rollout emitted %d events, want 0", n)
	}
}

// TestReadRollout_OpenError covers readRollout's open-error branch
// (scanner.go:304): a descriptor pointing at a non-existent (but in-root) path
// returns an open error. (Containment tolerates a not-yet-created tail; the
// open then fails.)
func TestReadRollout_OpenError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	rel := "2025/11/20/rollout-2025-11-20T10-00-00-" + uuid7(1) + ".jsonl"
	r := rollout{rel: rel, abs: filepath.Join(resolved, filepath.FromSlash(rel))} // not created
	out := make(chan canonical.Event, 4)
	_, _, err := readRollout(context.Background(), resolved, r, "codex:"+root, FileCursor{}, out, func(error) {}, nil)
	if err == nil {
		t.Fatal("readRollout on a non-existent in-root path should return an open error")
	}
}
