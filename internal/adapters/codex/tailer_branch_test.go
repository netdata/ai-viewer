package codex

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestFlushDirty_DirectCoversBranches drives flushDirty directly: a recognized
// modern rollout is read (cursor advances), an unrecognized rel is skipped, and
// a final SourceProgress is emitted.
func TestFlushDirty_DirectCoversBranches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-fd"))
	setMtime(t, path, time.Minute)
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"

	out := make(chan canonical.Event, 64)
	cur := newCursor()
	dirty := map[string]struct{}{
		rel:                         {},
		"session_index.jsonl":       {}, // unrecognized → skipped
		"rollout-2025-06-26-x.json": {}, // legacy → skipped
	}
	if err := flushDirty(context.Background(), resolved, "codex:"+root, dirty, &cur, out, func(error) {}); err != nil {
		t.Fatalf("flushDirty: %v", err)
	}
	if cur.Files[rel].Offset == 0 {
		t.Error("flushDirty did not advance the modern rollout cursor")
	}
	got := drainBuffered(out)
	if !hasKind(got, canonical.EvSessionStarted) {
		t.Error("flushDirty did not emit the session")
	}
	if !hasKind(got, canonical.EvSourceProgress) {
		t.Error("flushDirty did not emit a final SourceProgress")
	}
}

// TestFlushDirty_Empty asserts the empty-dirty-set early return (no progress).
func TestFlushDirty_Empty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	out := make(chan canonical.Event, 4)
	cur := newCursor()
	if err := flushDirty(context.Background(), resolved, "codex:"+root, map[string]struct{}{}, &cur, out, func(error) {}); err != nil {
		t.Fatalf("flushDirty(empty): %v", err)
	}
	if len(drainBuffered(out)) != 0 {
		t.Error("empty flushDirty should emit nothing")
	}
}

// TestFlushDirty_CancelledCtx asserts flushDirty honors a cancelled context.
func TestFlushDirty_CancelledCtx(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, completeSession("sid-x"))
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan canonical.Event, 4)
	cur := newCursor()
	err := flushDirty(ctx, resolved, "codex:"+root, map[string]struct{}{rel: {}}, &cur, out, func(error) {})
	if err == nil {
		t.Error("flushDirty on cancelled ctx should return ctx err")
	}
}

// TestFlushDirty_ReadErrorContinues asserts flushDirty surfaces a per-file open
// error and continues (does not abort the whole flush). Skipped where 0o000 is
// ignored.
func TestFlushDirty_ReadErrorContinues(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 does not block reads")
	}
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	bad := shardPath(root, uuid7(1))
	writeFileBytes(t, bad, completeSession("sid-bad"))
	relBad := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Skipf("chmod unsupported: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })
	if f, oerr := os.Open(bad); oerr == nil { // #nosec G304 -- test probe
		_ = f.Close()
		t.Skip("filesystem allowed opening a 0o000 file; seam not exercised")
	}

	out := make(chan canonical.Event, 8)
	cur := newCursor()
	var errs []string
	err := flushDirty(context.Background(), resolved, "codex:"+root, map[string]struct{}{relBad: {}}, &cur, out, func(e error) { errs = append(errs, e.Error()) })
	if err != nil {
		t.Fatalf("flushDirty should not fatal on a per-file read error: %v", err)
	}
	if len(errs) == 0 {
		t.Error("per-file read error not surfaced")
	}
}

// TestCatchUpFromCursor_NoFiles asserts the early return when discovery finds no
// modern rollouts.
func TestCatchUpFromCursor_NoFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(root)
	out := make(chan canonical.Event, 4)
	cur := newCursor()
	if err := catchUpFromCursor(context.Background(), resolved, root, "codex:"+root, &cur, out, func(error) {}); err != nil {
		t.Fatalf("catchUpFromCursor(empty): %v", err)
	}
	if len(drainBuffered(out)) != 0 {
		t.Error("catch-up with no files should emit nothing")
	}
}

// TestTail_DebounceFlushPath drives a real append after the watch is live and
// asserts the debounce-flush path emits the appended turn (covers the
// watcher.Events → resetDebounce → debounce.C → flushDirty cycle), then a tick
// fires (covers the tick branch's progress emit).
func TestTail_DebounceFlushAndTick(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	writeFileBytes(t, path, []byte(metaLine("sid-deb", `"exec"`)+"\n"))

	var heartbeats atomic.Int64
	cancel, wait, out := runTailWithHeartbeat(t, root, "codex:"+root, newCursor(), func() {
		heartbeats.Add(1)
	})
	defer func() { cancel(); wait() }()

	// Drain the catch-up SessionStarted.
	if _, ok := waitForKind(out, canonical.EvSessionStarted, 5*time.Second); !ok {
		t.Fatal("catch-up SessionStarted missing")
	}
	// Append a complete turn → Write event → debounce → flush.
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsCtx+`","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`+"\n"))
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsDone+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"`+tsDone+`"}}`+"\n"))
	if _, ok := waitForKind(out, canonical.EvTurnFinalized, 5*time.Second); !ok {
		t.Fatal("debounce flush did not emit the appended TurnFinalized")
	}
	// Drain everything currently pending, then wait QUIETLY (no writes) for >1
	// tick interval so the ONLY remaining SourceProgress can come from the tick
	// arm (tailLoop:126), not a debounce flush — deterministically covering it.
	drainFor(out, 200*time.Millisecond)
	nextHeartbeat := heartbeats.Load() + 1
	if _, ok := waitForKind(out, canonical.EvSourceProgress, tailTickInterval+3*time.Second); !ok {
		t.Fatal("tick did not emit a SourceProgress during a quiet window")
	}
	if !waitForHeartbeat(t, &heartbeats, nextHeartbeat, time.Second) {
		t.Fatal("quiet tick did not call tail heartbeat")
	}
}

// drainFor discards events for d, then returns. Used to flush pending
// debounce-driven events so a subsequent wait isolates the tick arm.
func drainFor(out chan canonical.Event, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-out:
		case <-deadline:
			return
		}
	}
}

// TestTail_ForcedFlushAtMaxEntries covers tailLoop's forced-flush arm: a single
// new-date-shard Create event whose dir already contains more than
// debounceMaxEntries rollout files dirties them all via markExistingDirty, so
// the loop's `len(dirty) >= debounceMaxEntries` branch trips an immediate flush
// (before the debounce timer), ingesting them. This is the inotify-queue-burst
// protection path.
func TestTail_ForcedFlushAtMaxEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Watch exists at the year level so the day-shard Create is observed.
	if err := os.MkdirAll(filepath.Join(root, "2025", "12"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cancel, wait, out := runTail(t, root, "codex:"+root, newCursor())
	defer func() { cancel(); wait() }()
	time.Sleep(150 * time.Millisecond) // let the watch establish

	// Build a day-shard dir with > debounceMaxEntries rollouts, then move it into
	// place so a SINGLE Create event fires for the dir (markExistingDirty then
	// dirties all of them at once → forced flush).
	staging := filepath.Join(t.TempDir(), "28")
	n := debounceMaxEntries + 5
	for i := 0; i < n; i++ {
		p := filepath.Join(staging, "rollout-2025-12-28T10-"+pad2(i/60)+"-"+pad2(i%60)+"-"+manyUUID(i)+".jsonl")
		writeFileBytes(t, p, completeSession("sid-many"))
	}
	dest := filepath.Join(root, "2025", "12", "28")
	if err := os.Rename(staging, dest); err != nil {
		t.Fatalf("rename staging into place: %v", err)
	}

	// At least one of the many sessions must ingest, proving the forced-flush ran.
	if _, ok := waitForKind(out, canonical.EvSessionStarted, 15*time.Second); !ok {
		t.Fatal("forced-flush did not ingest the burst of rollouts")
	}
}

// manyUUID returns a unique UUID-shaped id for the i-th burst file so each rel
// is distinct (the 12-hex tail encodes i).
func manyUUID(i int) string {
	return "019aa234-a2a1-75c3-a9bf-" + leftPadHex(i, 12)
}

// leftPadHex renders i as a width-w lowercase-hex string.
func leftPadHex(i, w int) string {
	const hexdigits = "0123456789abcdef"
	buf := make([]byte, w)
	for j := w - 1; j >= 0; j-- {
		buf[j] = hexdigits[i&0xf]
		i >>= 4
	}
	return string(buf)
}
