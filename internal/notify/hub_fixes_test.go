package notify

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// subFor returns the live *subscription the hub holds for id (nil if none).
// In-package test helper used to drive expire() with the exact object a
// retention timer would have captured.
func subFor(h *Hub, id string) *subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs[id]
}

// TestHub_AttachBusyOnSecondConcurrentStream asserts a subscription is
// single-consumer: while one stream is attached, a second Attach returns
// AttachBusy (mapped to 409 by the handler) and does NOT hand out the
// channel. After the first stream Detaches, a new Attach succeeds
// (AttachOK) — the normal serial reconnect.
func TestHub_AttachBusyOnSecondConcurrentStream(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")

	ch1, _, _, st := h.Attach("s1", "")
	if st != AttachOK {
		t.Fatalf("first Attach status = %v, want AttachOK", st)
	}
	if ch1 == nil {
		t.Fatal("first Attach returned nil channel")
	}

	// Second concurrent Attach must be rejected as BUSY (no channel).
	ch2, _, _, st2 := h.Attach("s1", "")
	if st2 != AttachBusy {
		t.Fatalf("second concurrent Attach status = %v, want AttachBusy", st2)
	}
	if ch2 != nil {
		t.Fatal("BUSY Attach must not return a channel (would split events)")
	}

	// Serial reconnect: first stream ends.
	h.Detach("s1")
	ch3, _, _, st3 := h.Attach("s1", "")
	if st3 != AttachOK {
		t.Fatalf("reconnect Attach status = %v, want AttachOK", st3)
	}
	if ch3 == nil {
		t.Fatal("reconnect Attach returned nil channel")
	}
}

// TestHub_AttachUnknownStatus asserts an unknown subscription yields
// AttachUnknown (mapped to 404), distinct from AttachBusy.
func TestHub_AttachUnknownStatus(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	if _, _, _, st := h.Attach("ghost", ""); st != AttachUnknown {
		t.Fatalf("Attach unknown sub status = %v, want AttachUnknown", st)
	}
	// After Shutdown, even a known id is UNKNOWN (hub gone).
	h.Add("s1")
	h.Shutdown()
	if _, _, _, st := h.Attach("s1", ""); st != AttachUnknown {
		t.Fatalf("Attach after Shutdown status = %v, want AttachUnknown", st)
	}
}

// TestHub_ExpireGenerationGuard pins the retention generation guard
// (P1-3): an OLD retention-timer callback that fires during a NEWER
// retention window must NOT remove the subscription. The guard is tested
// deterministically by invoking expire with a stale generation directly,
// then with the live one.
func TestHub_ExpireGenerationGuard(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Hour})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: %v", st)
	}

	s := subFor(h, "s1") // same object throughout (no Remove here)
	// First detach arms generation 1.
	h.Detach("s1")
	// Re-attach (cancels the gen-1 timer, attached=true).
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("re-Attach: %v", st)
	}
	// Second detach arms generation 2 (the live window).
	h.Detach("s1")

	// The STALE gen-1 callback fires late: it must be a no-op because the
	// live generation is now 2 (same object, older generation).
	h.expire("s1", s, 1)
	if !h.Has("s1") {
		t.Fatal("stale gen-1 expire removed the sub during the gen-2 window")
	}

	// The live gen-2 callback fires: the sub is removed.
	h.expire("s1", s, 2)
	if h.Has("s1") {
		t.Fatal("live gen-2 expire did not remove the detached sub")
	}
}

// TestHub_ExpireDoesNotRemoveReAddedSameID pins that a stale retention-timer
// callback for an OLD subscription object cannot remove a NEW subscription
// registered under the SAME id after a Remove+Add. The generation counter
// alone is insufficient (a fresh object starts at gen 0 and can reach the
// stale gen via its own detach cycle), so expire must also verify the
// subscription OBJECT is still the one the timer was armed for.
func TestHub_ExpireDoesNotRemoveReAddedSameID(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Hour})
	t.Cleanup(h.Shutdown)

	// Old object: attach, detach (arms gen 1), then remove it entirely.
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach old: %v", st)
	}
	old := subFor(h, "s1") // the object the old timer captured
	h.Detach("s1")         // old object's live generation is now 1
	h.Remove("s1")         // old object gone; its armed timer is now stale

	// New object under the SAME id, driven to the SAME generation (1).
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach new: %v", st)
	}
	h.Detach("s1") // new object's live generation is also 1

	// The OLD object's stale gen-1 callback fires (captured the old object):
	// it must NOT remove the new object, even though id and gen both match.
	h.expire("s1", old, 1)
	if !h.Has("s1") {
		t.Fatal("stale timer for a removed object dropped the re-added same-id subscription")
	}
}

// TestHub_OnRemoveCalledOnExpiryAndRemove asserts the OnRemove hook fires
// for BOTH retention expiry AND an explicit Remove, exactly once per
// removal, with the removed id — so the presenter can drop its
// filter/statsCoalesce entries (P1-1/P3-9). The hook must be invoked
// WITHOUT the hub mutex held (a re-entrant hub call from the hook would
// deadlock); we prove non-reentrancy by having the hook call back into a
// non-mutating hub method (Has) and a mutating one (Add of a different
// id) without deadlocking.
func TestHub_OnRemoveCalledOnExpiryAndRemove(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var removed []string
	var h *Hub // forward-declared so the OnRemove closure can call back in
	h = New(Options{
		ChannelCap:   4,
		ReplayBuffer: 4,
		Retention:    time.Hour, // long; we drive expiry by hand
		OnRemove: func(id string) {
			// Re-entrant hub calls must not deadlock (hook runs without the
			// hub lock held).
			_ = h.Has(id)
			mu.Lock()
			removed = append(removed, id)
			mu.Unlock()
		},
	})
	t.Cleanup(h.Shutdown)

	// Explicit Remove path.
	h.Add("a")
	h.Remove("a")

	// Retention-expiry path.
	h.Add("b")
	if _, _, _, st := h.Attach("b", ""); st != AttachOK {
		t.Fatalf("Attach b: %v", st)
	}
	sb := subFor(h, "b")
	h.Detach("b")
	h.expire("b", sb, 1) // generation 1 is the live window after one Detach

	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 2 {
		t.Fatalf("OnRemove fired %d times (%v), want 2 (Remove + expiry)", len(removed), removed)
	}
	seen := map[string]int{}
	for _, id := range removed {
		seen[id]++
	}
	if seen["a"] != 1 || seen["b"] != 1 {
		t.Fatalf("OnRemove ids = %v, want exactly one each of a,b", removed)
	}
}

// TestHub_OnRemoveNotCalledForUnknownRemove asserts removing an unknown id
// (or double-remove) does not fire OnRemove — the hook fires only when a
// real subscription is dropped.
func TestHub_OnRemoveNotCalledForUnknownRemove(t *testing.T) {
	t.Parallel()
	var n int
	h := New(Options{
		ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second,
		OnRemove: func(string) { n++ },
	})
	t.Cleanup(h.Shutdown)
	h.Remove("ghost") // unknown → no hook
	h.Add("s1")
	h.Remove("s1") // real → 1 hook
	h.Remove("s1") // already gone → no hook
	if n != 1 {
		t.Fatalf("OnRemove fired %d times, want 1", n)
	}
}

// TestHub_OnRemoveNotCalledOnShutdown documents that Shutdown does NOT fan
// OnRemove out per subscription: shutdown tears the whole process down, so
// per-sub presenter cleanup is moot and firing N callbacks during teardown
// would be wasteful. (If this is ever desired it should be an explicit
// decision; pinning current behavior prevents accidental change.)
func TestHub_OnRemoveNotCalledOnShutdown(t *testing.T) {
	t.Parallel()
	var n int
	h := New(Options{
		ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second,
		OnRemove: func(string) { n++ },
	})
	h.Add("s1")
	h.Add("s2")
	h.Shutdown()
	if n != 0 {
		t.Fatalf("OnRemove fired %d times on Shutdown, want 0", n)
	}
}

// TestHub_ConcurrentDeliverReplayMonotonic pins that the replay ring stays
// strictly increasing by event id even under CONCURRENT Deliver to the same
// subscription. The id must be minted under the same lock that appends to
// the ring; otherwise two goroutines can mint ids in one order and enqueue
// in the other, leaving the ring out of order — which would break
// replaySince's oldest/newest coverage reasoning. The replay buffer is sized
// to hold every delivered event so eviction does not mask ordering.
func TestHub_ConcurrentDeliverReplayMonotonic(t *testing.T) {
	t.Parallel()
	const n = 400
	h := New(Options{ChannelCap: n + 1, ReplayBuffer: n + 1, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Deliver("s1", Event{Kind: "session_changed", SessionID: "x"})
		}()
	}
	wg.Wait()

	s := subFor(h, "s1")
	h.mu.Lock()
	ordered := s.ring.ordered()
	ids := make([]uint64, 0, len(ordered))
	for _, ev := range ordered {
		v, ok := parseID(ev.ID)
		if !ok {
			h.mu.Unlock()
			t.Fatalf("replay event has unparseable id %q", ev.ID)
		}
		ids = append(ids, v)
	}
	h.mu.Unlock()

	if len(ids) != n {
		t.Fatalf("replay ring holds %d events, want %d", len(ids), n)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("replay ring not strictly increasing at %d: %d then %d (id minted outside the enqueue lock)", i, ids[i-1], ids[i])
		}
	}
}

// TestHub_SetOnRemoveReplaces covers SetOnRemove: a hook installed after
// construction (the path the presenter uses for a pre-built hub) fires on
// removal, and a later SetOnRemove replaces it.
func TestHub_SetOnRemoveReplaces(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	t.Cleanup(h.Shutdown)

	var first, second int
	h.SetOnRemove(func(string) { first++ })
	h.Add("a")
	h.Remove("a")
	if first != 1 {
		t.Fatalf("first hook fired %d times, want 1", first)
	}

	// Replace the hook: only the new one fires for the next removal.
	h.SetOnRemove(func(string) { second++ })
	h.Add("b")
	h.Remove("b")
	if first != 1 {
		t.Fatalf("old hook fired again (%d), want it replaced", first)
	}
	if second != 1 {
		t.Fatalf("replacement hook fired %d times, want 1", second)
	}
}

// TestHub_ExpireUnknownIDNoOp covers expire's "subscription gone" branch
// directly (the timer fires after the sub was already removed).
func TestHub_ExpireUnknownIDNoOp(t *testing.T) {
	t.Parallel()
	var fired int
	h := New(Options{
		ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second,
		OnRemove: func(string) { fired++ },
	})
	t.Cleanup(h.Shutdown)
	// No such id: expire is a no-op and must not fire OnRemove.
	h.expire("ghost", nil, 1)
	if fired != 0 {
		t.Fatalf("expire on unknown id fired OnRemove %d times, want 0", fired)
	}
}

// TestHub_ReplaySinceRejectsForgedFutureID pins P2-5: a Last-Event-ID that
// is AHEAD of the newest retained id (a stale or forged value) must yield
// covered=false (driving a resync), not a false "covered" with an empty
// replay.
func TestHub_ReplaySinceRejectsForgedFutureID(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 16, ReplayBuffer: 8, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: %v", st)
	}
	// Deliver 3 events → newest retained id is 3.
	for i := 0; i < 3; i++ {
		h.Deliver("s1", Event{Kind: "session_changed", SessionID: fmt.Sprintf("e%d", i)})
	}
	h.Detach("s1")

	// Reconnect claiming Last-Event-ID = 99 (ahead of newest=3): the hub
	// cannot have delivered it, so coverage is impossible → resync.
	_, replay, covered, st := h.Attach("s1", "99")
	if st != AttachOK {
		t.Fatalf("reconnect Attach status = %v, want AttachOK", st)
	}
	if covered {
		t.Fatal("covered=true for a Last-Event-ID ahead of the newest retained id, want false")
	}
	if len(replay) != 0 {
		t.Fatalf("replay len = %d, want 0 for a forged future id", len(replay))
	}
}
