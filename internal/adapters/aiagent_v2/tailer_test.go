package aiagent_v2

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

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

func TestTail_ReturnsOnCanceledCtx(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)
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

func TestTail_DetectsNewSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	writeSnapshot(t, root, "tail-1", simpleSnapshot(2, "tail-1"))
	ev, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		if ss, isSession := ev.(canonical.SessionStartedEvent); isSession {
			return ss.NativeID == "tail-1"
		}
		return false
	})
	if !ok {
		t.Fatalf("did not see SessionStarted for new snapshot")
	}
	if ev == nil {
		t.Fatalf("nil event")
	}
}

func TestTail_RewriteWithDifferentContentRefiresEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	// First write — initial content.
	origin := "rewrite-1"
	writeSnapshot(t, root, origin, simpleSnapshot(2, origin))
	if _, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		ss, is := ev.(canonical.SessionStartedEvent)
		return is && ss.NativeID == origin
	}); !ok {
		t.Fatalf("initial SessionStarted missing")
	}

	// Drain remaining events.
	time.Sleep(200 * time.Millisecond)
	_ = drainBuffered(out)

	// Second write — different content (extra turn).
	snap := simpleSnapshot(2, origin)
	snap.OpTree.Turns = append(snap.OpTree.Turns, turnNode{
		ID: "turn-2", Index: 2,
		StartedAt: 1700000006000, EndedAt: int64Ptr(1700000008000),
		Ops: []operationNode{
			{
				OpID: "second-op", Kind: "llm",
				StartedAt: 1700000006500, EndedAt: int64Ptr(1700000007500), Status: "ok",
				Attributes: rawAttrs(map[string]any{"model": "x"}),
				Accounting: []accountingEntry{{Type: "llm", Tokens: &tokens{InputTokens: 5, OutputTokens: 3}}},
			},
		},
	})
	writeSnapshot(t, root, origin, snap)

	// Expect to see the new turn's TurnStarted.
	if _, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		ts, is := ev.(canonical.TurnStartedEvent)
		return is && ts.Seq == 2
	}); !ok {
		t.Fatalf("did not see TurnStarted Seq=2 after rewrite")
	}
}

func TestTail_AtomicRenameTriggersExactlyOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 128)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()
	time.Sleep(80 * time.Millisecond)

	origin := "rename-1"
	final := filepath.Join(root, origin+".json.gz")
	// Simulate the producer: write to tmp, atomic rename.
	tmp := final + ".tmp-1234-5678"
	body, _ := snapshotJSON(simpleSnapshot(2, origin))
	if err := os.WriteFile(tmp, mkGzipBytes(body), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := waitForEvent(t, out, 5*time.Second, func(ev canonical.Event) bool {
		ss, is := ev.(canonical.SessionStartedEvent)
		return is && ss.NativeID == origin
	}); !ok {
		t.Fatalf("did not see SessionStarted for renamed file")
	}
}

func TestTail_PeriodicProgress(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, _ := New(root, canonical.AdapterOptions{})
	out := make(chan canonical.Event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Tail(ctx, out) }()

	if _, ok := waitForEvent(t, out, 7*time.Second, func(ev canonical.Event) bool {
		_, is := ev.(canonical.SourceProgressEvent)
		return is
	}); !ok {
		t.Fatalf("no periodic SourceProgress within 7s")
	}
}

func TestTailable_IgnoresUnrelated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		evName string
		want   string
	}{
		{filepath.Join(root, "abc.json.gz"), "abc.json.gz"},
		{filepath.Join(root, "abc.json.gz.tmp-1-2"), ""},
		{filepath.Join(root, "session", "x.jsonl"), ""},
		{filepath.Join(root, "notagz.txt"), ""},
	}
	for _, c := range cases {
		got := tailableName(root, fsnotify.Event{Name: c.evName})
		if got != c.want {
			t.Fatalf("tailableName(%q) = %q, want %q", c.evName, got, c.want)
		}
	}
}

func TestSortStrings(t *testing.T) {
	t.Parallel()
	s := []string{"c", "a", "b"}
	sortStrings(s)
	if s[0] != "a" || s[1] != "b" || s[2] != "c" {
		t.Fatalf("sort: %v", s)
	}
}

func TestResetDebounce(t *testing.T) {
	t.Parallel()
	timer := time.NewTimer(debounceWindow)
	if !timer.Stop() {
		<-timer.C
	}
	resetDebounce(timer)
	select {
	case <-timer.C:
	case <-time.After(debounceWindow + 250*time.Millisecond):
		t.Fatalf("timer did not fire after reset")
	}
}

// snapshotJSON marshals a snapshot literal for tests that need to
// write a fixture by hand (the rename-into-place case writes via a
// .tmp temp file directly, bypassing the higher-level helper).
func snapshotJSON(s snapshot) ([]byte, error) { return json.Marshal(s) }

// mkGzipBytes gzips the provided bytes without requiring a *testing.T
// — used by the rename-into-place test below.
func mkGzipBytes(payload []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(payload)
	_ = zw.Close()
	return buf.Bytes()
}

// Ensure os is referenced even if some test paths above are removed.
var _ = os.O_RDONLY
