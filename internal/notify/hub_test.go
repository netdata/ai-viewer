package notify

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// testHub builds a Hub with small, fast defaults for deterministic tests
// (short retention so Detach→removal does not stall the suite).
func testHub(t *testing.T) *Hub {
	t.Helper()
	h := New(Options{
		ChannelCap:   4,
		ReplayBuffer: 3,
		Retention:    30 * time.Millisecond,
	})
	t.Cleanup(h.Shutdown)
	return h
}

func TestHub_AddHasRemove(t *testing.T) {
	t.Parallel()
	h := testHub(t)

	if h.Has("s1") {
		t.Fatal("Has before Add: want false")
	}
	h.Add("s1")
	if !h.Has("s1") {
		t.Fatal("Has after Add: want true")
	}
	// Add is idempotent.
	h.Add("s1")
	if got := len(h.IDs()); got != 1 {
		t.Fatalf("IDs after double Add = %d, want 1", got)
	}
	h.Remove("s1")
	if h.Has("s1") {
		t.Fatal("Has after Remove: want false")
	}
	// Remove of an unknown id is a no-op.
	h.Remove("s1")
}

func TestHub_IDsSnapshot(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	h.Add("a")
	h.Add("b")
	h.Add("c")
	ids := h.IDs()
	if len(ids) != 3 {
		t.Fatalf("IDs len = %d, want 3", len(ids))
	}
	// Mutating the returned slice must not affect the hub.
	ids[0] = "mutated"
	for _, id := range h.IDs() {
		if id == "mutated" {
			t.Fatal("IDs() returned a non-snapshot slice")
		}
	}
}

func TestHub_DeliverFanOutToTargetOnly(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	h.Add("s1")
	h.Add("s2")

	ch1, _, _, st := h.Attach("s1", "")
	if st != AttachOK {
		t.Fatalf("Attach s1: status=%v, want AttachOK", st)
	}
	ch2, _, _, st := h.Attach("s2", "")
	if st != AttachOK {
		t.Fatalf("Attach s2: status=%v, want AttachOK", st)
	}

	if !h.Deliver("s1", Event{Kind: "session_changed", SessionID: "x"}) {
		t.Fatal("Deliver to s1: want delivered=true")
	}

	select {
	case ev := <-ch1:
		if ev.SessionID != "x" {
			t.Errorf("s1 got SessionID %q, want x", ev.SessionID)
		}
		if ev.ID == "" {
			t.Error("delivered event has empty ID (Last-Event-ID would break)")
		}
	default:
		t.Fatal("s1 channel empty, want one event")
	}

	select {
	case ev := <-ch2:
		t.Fatalf("s2 received an event meant for s1: %+v", ev)
	default:
		// correct: s2 got nothing
	}
}

func TestHub_DeliverUnknownSubReturnsFalse(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	if h.Deliver("ghost", Event{Kind: "stats_invalidated"}) {
		t.Fatal("Deliver to unknown sub: want delivered=false")
	}
}

// TestHub_BackpressureDropsOldest fills a subscription's channel beyond
// capacity and asserts drop-OLDEST semantics: the newest events survive,
// the dropped counter advances, and Deliver never blocks.
func TestHub_BackpressureDropsOldest(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 2, ReplayBuffer: 100, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}

	// Capacity 2; deliver 4 without draining. Drop-oldest keeps the last 2.
	for i := 1; i <= 4; i++ {
		if !h.Deliver("s1", Event{Kind: "session_changed", SessionID: fmt.Sprintf("e%d", i)}) {
			t.Fatalf("Deliver e%d: want delivered=true (drop-oldest still enqueues)", i)
		}
	}

	if got := h.Dropped("s1"); got != 2 {
		t.Fatalf("Dropped = %d, want 2", got)
	}

	// Single-consumer: detach the first stream before re-attaching to
	// re-read the buffered events (the serial reconnect path).
	h.Detach("s1")
	ch, _, _, _ := h.Attach("s1", "")
	var got []string
	for len(ch) > 0 {
		got = append(got, (<-ch).SessionID)
	}
	// Oldest two (e1,e2) dropped; e3,e4 retained in order.
	if len(got) != 2 || got[0] != "e3" || got[1] != "e4" {
		t.Fatalf("retained events = %v, want [e3 e4]", got)
	}
}

// TestHub_AttachReplayAfterLastEventID asserts the replay buffer returns
// only events whose ID is strictly greater than the supplied
// Last-Event-ID, and reports covered=true.
func TestHub_AttachReplayAfterLastEventID(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 16, ReplayBuffer: 10, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("initial Attach: status=%v, want AttachOK", st)
	}

	var ids []string
	for i := 0; i < 5; i++ {
		ev := Event{Kind: "session_changed", SessionID: fmt.Sprintf("e%d", i)}
		h.Deliver("s1", ev)
	}
	// Capture assigned IDs by draining and re-reading is messy; instead
	// detach knowledge: IDs are monotonic decimal starting at 1.
	for i := 1; i <= 5; i++ {
		ids = append(ids, fmt.Sprintf("%d", i))
	}

	// Reconnect with Last-Event-ID = id of the 2nd event → expect events
	// 3,4,5. Detach first (single-consumer serial reconnect).
	h.Detach("s1")
	_, replay, covered, st := h.Attach("s1", ids[1])
	if st != AttachOK {
		t.Fatalf("reconnect Attach: status=%v, want AttachOK", st)
	}
	if !covered {
		t.Fatal("covered=false, want true (buffer holds all 5 events)")
	}
	if len(replay) != 3 {
		t.Fatalf("replay len = %d, want 3 (events after id=%s)", len(replay), ids[1])
	}
	if replay[0].SessionID != "e2" || replay[2].SessionID != "e4" {
		t.Errorf("replay = [%s..%s], want [e2..e4]", replay[0].SessionID, replay[2].SessionID)
	}
}

// TestHub_AttachReplayGapNotCovered asserts that when the buffer can no
// longer cover the gap since Last-Event-ID (client offline too long /
// events evicted from the ring), Attach returns covered=false with an
// empty replay so the presenter can send a resync.
func TestHub_AttachReplayGapNotCovered(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 16, ReplayBuffer: 3, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("initial Attach: status=%v, want AttachOK", st)
	}

	// Deliver 6 events into a ring of 3 → events 1,2,3 evicted; 4,5,6 remain.
	for i := 0; i < 6; i++ {
		h.Deliver("s1", Event{Kind: "session_changed", SessionID: fmt.Sprintf("e%d", i)})
	}

	// Reconnect claiming Last-Event-ID="2" — that event is gone from the
	// ring, so the hub cannot prove it replayed everything since. Detach
	// first (single-consumer serial reconnect).
	h.Detach("s1")
	_, replay, covered, st := h.Attach("s1", "2")
	if st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	if covered {
		t.Fatal("covered=true, want false (id=2 evicted from a 3-deep ring)")
	}
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want 0 on a gap", len(replay))
	}
}

func TestHub_AttachUnknownSub(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	if _, _, _, st := h.Attach("ghost", ""); st != AttachUnknown {
		t.Fatalf("Attach unknown sub: status=%v, want AttachUnknown", st)
	}
}

func TestHub_AttachEmptyLastEventIDIsCovered(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	h.Add("s1")
	_, replay, covered, st := h.Attach("s1", "")
	if st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	if !covered {
		t.Fatal("fresh connect (empty Last-Event-ID): want covered=true")
	}
	if len(replay) != 0 {
		t.Fatalf("fresh connect replay = %d, want 0", len(replay))
	}
}

// TestHub_DetachRetentionRemoves asserts a detached subscription is
// removed after the retention window elapses with no re-Attach.
func TestHub_DetachRetentionRemoves(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 3, Retention: 20 * time.Millisecond})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Detach("s1")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !h.Has("s1") {
			return // removed as expected
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("subscription survived past retention window")
}

// TestHub_AttachWithinRetentionResumes asserts that re-Attaching before
// the retention timer fires cancels the removal — the subscription
// survives.
func TestHub_AttachWithinRetentionResumes(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 3, Retention: 50 * time.Millisecond})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Detach("s1")

	// Re-attach quickly (well within the 50ms window).
	time.Sleep(10 * time.Millisecond)
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("re-Attach within retention: status=%v (sub was dropped too early)", st)
	}

	// Wait past the original window; the sub must still exist.
	time.Sleep(80 * time.Millisecond)
	if !h.Has("s1") {
		t.Fatal("re-Attach did not cancel the retention timer; sub was removed")
	}
}

// TestHub_ShutdownClosesChannels asserts Shutdown closes every delivery
// channel so per-client stream goroutines unblock, and is idempotent.
func TestHub_ShutdownClosesChannels(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 3, Retention: time.Second})
	h.Add("s1")
	ch, _, _, st := h.Attach("s1", "")
	if st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}

	h.Shutdown()
	// Idempotent second call must not panic.
	h.Shutdown()

	// Reading a closed channel returns the zero value with ok=false
	// promptly (does not block).
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("channel still open after Shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("read from channel blocked after Shutdown (channel not closed)")
	}
}

// TestHub_DroppedUnknownSub asserts Dropped on an unknown sub is 0, not a
// panic.
func TestHub_DroppedUnknownSub(t *testing.T) {
	t.Parallel()
	h := testHub(t)
	if got := h.Dropped("ghost"); got != 0 {
		t.Fatalf("Dropped(unknown) = %d, want 0", got)
	}
}

// TestHub_ConcurrentStress hammers the hub with many concurrent
// Add/Deliver/Attach/Detach/Remove operations. The race detector
// (go test -race) is the assertion: any data race fails the test.
func TestHub_ConcurrentStress(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 8, ReplayBuffer: 16, Retention: 5 * time.Millisecond})
	t.Cleanup(h.Shutdown)

	const subs = 16
	const iters = 200
	var wg sync.WaitGroup

	for s := 0; s < subs; s++ {
		id := fmt.Sprintf("s%d", s)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				h.Add(id)
				ch, _, _, st := h.Attach(id, "")
				if st == AttachOK {
					// Drain opportunistically so channels don't wedge.
					for len(ch) > 0 {
						<-ch
					}
				}
				h.Deliver(id, Event{Kind: "session_changed", SessionID: id})
				_ = h.Dropped(id)
				_ = h.Has(id)
				_ = h.IDs()
				if i%3 == 0 {
					h.Detach(id)
				}
				if i%7 == 0 {
					h.Remove(id)
				}
			}
		}()
	}

	// A concurrent producer fanning to all ids.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			for _, id := range h.IDs() {
				h.Deliver(id, Event{Kind: "stats_invalidated"})
			}
		}
	}()

	wg.Wait()
}
