package aiagent_v3

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// waitForEvent reads events from ch until pred returns true, or until
// timeout. Returns the matching event and true on success.
func waitForEvent(t *testing.T, ch chan canonical.Event, timeout time.Duration, pred func(canonical.Event) bool) (canonical.Event, bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil, false
			}
			if pred(ev) {
				return ev, true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func waitForHeartbeat(t *testing.T, count *atomic.Int64, want int64, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if count.Load() >= want {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-tick.C:
		}
	}
}

func TestTail_ReturnsOnCanceledCtx(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := mkdirAll(filepath.Join(root, sessionDir)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Tail(ctx, out) }()
	// Give the watcher a moment to register itself.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Tail returned non-nil on cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Tail did not return within 2s of cancel")
	}
}

func TestTail_HoldsBackPartialLineThenCompletes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Tail(ctx, out) }()
	// Wait for the watcher to register.
	time.Sleep(50 * time.Millisecond)

	path := filepath.Join(dir, "new.jsonl")
	// Step 1: write a partial line (no trailing newline). Wait debounce
	// + a bit, then assert no SessionStarted has appeared.
	partial := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"x","sessionId":"x","capturePayloads":true`)
	if err := writeFileBytes(path, partial); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	// Sleep > debounceWindow to let flushDirty run.
	time.Sleep(debounceWindow + 100*time.Millisecond)
	// Drain whatever's pending so we can ASSERT no SessionStarted yet.
	for done := false; !done; {
		select {
		case ev := <-out:
			if _, ok := ev.(canonical.SessionStartedEvent); ok {
				t.Fatalf("partial line should not have produced SessionStarted")
			}
		default:
			done = true
		}
	}

	// Step 2: append the closing "}\n" to complete the line. The
	// tailer should pick it up within one debounce window.
	if err := appendFileBytes(path, []byte("}\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	ev, ok := waitForEvent(t, out, 15*time.Second, func(ev canonical.Event) bool {
		_, is := ev.(canonical.SessionStartedEvent)
		return is
	})
	if !ok {
		t.Fatalf("did not see SessionStarted after line completion")
	}
	if ss := ev.(canonical.SessionStartedEvent); ss.NativeID != "x" {
		t.Fatalf("wrong NativeID: %+v", ss)
	}
}

func TestTail_AppendsToExistingFileTriggerEvents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "existing.jsonl")
	initial := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"y","sessionId":"y","capturePayloads":true}` + "\n")
	if err := writeFileBytes(path, initial); err != nil {
		t.Fatalf("write: %v", err)
	}

	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(50 * time.Millisecond)

	// Append a new line and wait for a TurnStarted event.
	turn := []byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:01.000Z","originId":"y","sessionId":"y","turn":1}` + "\n")
	if err := appendFileBytes(path, turn); err != nil {
		t.Fatalf("append: %v", err)
	}
	ev, ok := waitForEvent(t, out, 3*time.Second, func(ev canonical.Event) bool {
		_, is := ev.(canonical.TurnStartedEvent)
		return is
	})
	if !ok {
		t.Fatalf("did not see TurnStarted")
	}
	if ts := ev.(canonical.TurnStartedEvent); ts.SessionNativeID != "y" || ts.Seq != 1 {
		t.Fatalf("wrong TurnStarted: %+v", ts)
	}
}

func TestTail_EmitsPeriodicProgress(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := mkdirAll(filepath.Join(root, sessionDir)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var heartbeats atomic.Int64
	a, _ := New(root, canonical.AdapterOptions{
		OnTailHeartbeat: func() { heartbeats.Add(1) },
	})
	out := make(chan canonical.Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()

	// Tail's tick is 5s — at the cost of test wall-time, that's the
	// only realistic way to assert the periodic path without
	// re-architecting. We bound this with a 7s budget so the test
	// remains deterministic in CI.
	ev, ok := waitForEvent(t, out, 7*time.Second, func(ev canonical.Event) bool {
		_, is := ev.(canonical.SourceProgressEvent)
		return is
	})
	if !ok {
		t.Fatalf("did not see periodic SourceProgress within 7s")
	}
	if sp := ev.(canonical.SourceProgressEvent); sp.Cursor == "" {
		t.Fatalf("progress cursor empty")
	}
	if !waitForHeartbeat(t, &heartbeats, 1, time.Second) {
		t.Fatalf("periodic SourceProgress did not call tail heartbeat")
	}
}

func TestScanThenTail_NoLossInWindow(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, sessionDir)
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	path := filepath.Join(dir, "window.jsonl")
	initial := []byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"window","sessionId":"window","capturePayloads":true}` + "\n")
	if err := writeFileBytes(path, initial); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	scanOut := make(chan canonical.Event, 128)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !hasSessionStarted(drainBuffered(scanOut), "window") {
		t.Fatalf("Scan did not emit the seeded session")
	}

	// The window: this record lands after Scan returns but before Tail registers.
	turn := []byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:01.000Z","originId":"window","sessionId":"window","turn":1}` + "\n")
	if err := appendFileBytes(path, turn); err != nil {
		t.Fatalf("append window record: %v", err)
	}

	tailOut := make(chan canonical.Event, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, tailOut) }()

	if _, ok := waitForEvent(t, tailOut, 5*time.Second, func(ev canonical.Event) bool {
		ts, is := ev.(canonical.TurnStartedEvent)
		return is && ts.SessionNativeID == "window" && ts.Seq == 1
	}); !ok {
		t.Fatalf("Tail did not emit record appended in the Scan-to-Tail window")
	}
}

// TestTailer_RefusesToCreateMissingSourceDir asserts the tailer never
// mkdirs into the source tree (security.md §"Hard Rules" #1 — read-only
// on sources). When the watch dir is absent we want a clean, fast
// shutdown with the cause surfaced via OnError so /api/health flags it,
// NOT a silently-created directory.
func TestTailer_RefusesToCreateMissingSourceDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	watchDir := filepath.Join(root, sessionDir)
	// Important: do NOT mkdir the watch dir — that is the test invariant.

	var captured []error
	a, err := New(root, canonical.AdapterOptions{
		OnError: func(e error) { captured = append(captured, e) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Tail(ctx, out) }()

	// Tail must return promptly when the source dir is absent — no
	// fsnotify watch was registered, so there is nothing to do.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Tail returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Tail did not return within 2s of missing source dir")
	}

	// The dir must NOT have been created — that would violate the
	// read-only-sources invariant.
	if _, statErr := os.Stat(watchDir); statErr == nil {
		t.Fatalf("tailer created %s — violates security.md §Hard Rules #1", watchDir)
	}

	// OnError must have fired so /api/health surfaces the issue.
	if len(captured) == 0 {
		t.Fatal("expected at least one OnError invocation describing the missing source dir")
	}
}

func TestSortStrings(t *testing.T) {
	t.Parallel()

	in := []string{"c", "a", "b"}
	sortStrings(in)
	if in[0] != "a" || in[1] != "b" || in[2] != "c" {
		t.Fatalf("not sorted: %v", in)
	}
}

func hasSessionStarted(events []canonical.Event, nativeID string) bool {
	for _, ev := range events {
		ss, ok := ev.(canonical.SessionStartedEvent)
		if ok && ss.NativeID == nativeID {
			return true
		}
	}
	return false
}

func TestResetDebounce(t *testing.T) {
	t.Parallel()

	timer := time.NewTimer(debounceWindow)
	// Drain initial firing.
	if !timer.Stop() {
		<-timer.C
	}
	resetDebounce(timer)
	select {
	case <-timer.C:
	case <-time.After(debounceWindow + 200*time.Millisecond):
		t.Fatalf("timer did not fire after reset")
	}
}
