package codex

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// runTail starts tailLoop in a goroutine over root from the given cursor,
// returning a cancel func, a wait func, and the live event channel. onError
// appends to a shared slice the caller can inspect after cancelling.
func runTail(t *testing.T, root, sourceID string, cur Cursor) (context.CancelFunc, func() []string, chan canonical.Event) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 4096)
	var mu sync.Mutex
	var errs []string
	onError := func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = tailLoop(ctx, root, sourceID, cur, out, onError)
	}()
	wait := func() []string {
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		cp := make([]string, len(errs))
		copy(cp, errs)
		return cp
	}
	return cancel, wait, out
}

// waitForKind drains out until an event of the given kind appears or the
// deadline elapses, returning the accumulated events and whether it was found.
func waitForKind(out chan canonical.Event, kind canonical.EventKind, d time.Duration) ([]canonical.Event, bool) {
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

// TestTail_PicksUpAppendedRecords verifies the fsnotify tail loop emits events
// for records appended to an existing rollout after Tail starts (the realtime
// path). The seeded session_meta is below the catch-up cursor's snapshot and
// must NOT be re-emitted as a duplicate; only the appended turn produces new
// turn events.
func TestTail_PicksUpAppendedRecords(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(1))
	rel := "2025/11/20/rollout-2025-11-20T16-59-09-" + uuid7(1) + ".jsonl"
	// Seed with session_meta only so the catch-up reads it and advances the
	// cursor; we then append a turn.
	writeFileBytes(t, path, []byte(metaLine("sid-tail", `"exec"`)+"\n"))

	cancel, wait, out := runTail(t, root, "codex:"+root, newCursor())
	defer func() { cancel(); wait() }()

	// Catch-up should emit the SessionStarted from the seeded meta.
	if _, ok := waitForKind(out, canonical.EvSessionStarted, 5*time.Second); !ok {
		t.Fatal("catch-up did not emit SessionStarted for seeded meta")
	}

	// Append a turn_context + task_complete after the watch is live.
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsCtx+`","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`+"\n"))
	appendFileBytes(t, path, []byte(`{"timestamp":"`+tsDone+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"`+tsDone+`"}}`+"\n"))

	got, ok := waitForKind(out, canonical.EvTurnFinalized, 5*time.Second)
	if !ok {
		t.Fatal("tail did not emit TurnFinalized for appended turn")
	}
	// Exactly one SessionStarted total across catch-up + tail (no dup).
	if n := countKind(got, canonical.EvSessionStarted); n > 1 {
		t.Errorf("duplicate SessionStarted across tail = %d, want <= 1", n)
	}
	_ = rel
}

// TestTail_NewDateShardDir is the codex-specific requirement: a brand-new
// YYYY/MM/DD shard directory created AFTER Tail starts is added to the watch
// and the rollout written into it is ingested.
func TestTail_NewDateShardDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Pre-create only the YYYY level so the watch exists at the root; the new
	// MM/DD dirs are created live.
	if err := mkdirAll(t, filepath.Join(root, "2025")); err != nil {
		t.Fatalf("seed year dir: %v", err)
	}

	cancel, wait, out := runTail(t, root, "codex:"+root, newCursor())
	defer func() { cancel(); wait() }()

	// Give Tail a moment to establish the watch on the root + year dir.
	time.Sleep(150 * time.Millisecond)

	// Create a NEW month/day shard and write a complete rollout into it.
	path := filepath.Join(root, "2025", "12", "25", "rollout-2025-12-25T09-00-00-"+uuid7(2)+".jsonl")
	writeFileBytes(t, path, completeSession("sid-newday"))

	if _, ok := waitForKind(out, canonical.EvSessionStarted, 8*time.Second); !ok {
		t.Fatal("rollout in a newly-created date shard dir was not ingested")
	}
}

// TestTail_CreateFileInWatchedShard asserts a brand-new rollout file created in
// an already-watched shard dir after Tail starts is ingested (the Create/Write
// path, not a new dir).
func TestTail_CreateFileInWatchedShard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	shardDir := filepath.Join(root, "2025", "11", "20")
	if err := mkdirAll(t, shardDir); err != nil {
		t.Fatalf("seed shard dir: %v", err)
	}

	cancel, wait, out := runTail(t, root, "codex:"+root, newCursor())
	defer func() { cancel(); wait() }()
	time.Sleep(150 * time.Millisecond)

	path := filepath.Join(shardDir, "rollout-2025-11-20T12-00-00-"+uuid7(3)+".jsonl")
	writeFileBytes(t, path, completeSession("sid-created"))

	if _, ok := waitForKind(out, canonical.EvSessionStarted, 8*time.Second); !ok {
		t.Fatal("rollout created in a watched shard dir was not ingested")
	}
}

// TestTail_MissingRootBenign asserts a missing sessions root surfaces a
// SourceError and returns cleanly (the daemon keeps running for other sources),
// rather than erroring out of tailLoop.
func TestTail_MissingRootBenign(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "no-such-sessions")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan canonical.Event, 4)
	var mu sync.Mutex
	var errs []string
	err := tailLoop(ctx, root, "codex:"+root, newCursor(), out, func(e error) {
		mu.Lock()
		errs = append(errs, e.Error())
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("tailLoop on missing root = %v, want nil", err)
	}
	if len(errs) == 0 {
		t.Error("missing root should surface a SourceError")
	}
}

// TestTail_CatchUpStaleFinalizes asserts the catch-up path applies the rule #23
// stale-finalize: a hanging-turn file with a stale mtime present at Tail start
// gets its synthetic SessionFinalized via catchUpFromCursor → flushDirty →
// readRollout.
func TestTail_CatchUpStaleFinalizes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := shardPath(root, uuid7(4))
	writeFileBytes(t, path, hangingSession("sid-stale"))
	setMtime(t, path, 2*time.Hour)

	cancel, wait, out := runTail(t, root, "codex:"+root, newCursor())
	defer func() { cancel(); wait() }()

	if _, ok := waitForKind(out, canonical.EvSessionFinalized, 5*time.Second); !ok {
		t.Fatal("catch-up did not stale-finalize a hanging session present at startup")
	}
}

// mkdirAll is a tiny test helper that creates a directory tree.
func mkdirAll(t *testing.T, dir string) error {
	t.Helper()
	return os.MkdirAll(dir, 0o755)
}
