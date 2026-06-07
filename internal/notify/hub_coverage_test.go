package notify

import (
	"testing"
	"time"
)

// TestHub_DefaultOptions exercises withDefaults' zero-value branches: an
// empty Options must yield the sse-protocol.md defaults.
func TestHub_DefaultOptions(t *testing.T) {
	t.Parallel()
	h := New(Options{})
	t.Cleanup(h.Shutdown)
	if h.opts.ChannelCap != defaultChannelCap {
		t.Errorf("ChannelCap = %d, want %d", h.opts.ChannelCap, defaultChannelCap)
	}
	if h.opts.ReplayBuffer != defaultReplayBuffer {
		t.Errorf("ReplayBuffer = %d, want %d", h.opts.ReplayBuffer, defaultReplayBuffer)
	}
	if h.opts.Retention != defaultRetention {
		t.Errorf("Retention = %v, want %v", h.opts.Retention, defaultRetention)
	}
	if h.opts.Now == nil {
		t.Error("Now is nil, want a default clock")
	}
}

// TestHub_OperationsAfterShutdownAreNoOps covers the shutdown guard
// branches in Add, Deliver, Attach, and Detach.
func TestHub_OperationsAfterShutdownAreNoOps(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	h.Add("s1")
	h.Shutdown()

	// Add after shutdown does not register.
	h.Add("s2")
	if h.Has("s2") {
		t.Error("Add after Shutdown registered a subscription")
	}
	// Deliver after shutdown returns false.
	if h.Deliver("s1", Event{Kind: "stats_invalidated"}) {
		t.Error("Deliver after Shutdown returned true")
	}
	// Attach after shutdown returns AttachUnknown.
	if _, _, _, st := h.Attach("s1", ""); st != AttachUnknown {
		t.Errorf("Attach after Shutdown status=%v, want AttachUnknown", st)
	}
	// Detach after shutdown is a no-op (must not panic).
	h.Detach("s1")
}

// TestHub_DetachUnknownAndDoubleDetach covers Detach's unknown-id and
// already-detached branches.
func TestHub_DetachUnknownAndDoubleDetach(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	t.Cleanup(h.Shutdown)

	// Unknown id: no-op.
	h.Detach("ghost")

	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Detach("s1")
	// Second Detach while already detached must not restart the timer.
	h.Detach("s1")
	if !h.Has("s1") {
		t.Error("double Detach removed the sub prematurely")
	}
}

// TestHub_ExpireStaleTimerIgnored drives the retention timer to fire
// AFTER a re-Attach has cancelled it, exercising expire's guard that a
// stale callback does not remove a resumed subscription. The retention
// is short and the re-Attach happens before it elapses.
func TestHub_ExpireStaleTimerIgnored(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: 15 * time.Millisecond})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Detach("s1")
	// Re-attach immediately so the armed timer becomes stale.
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("re-Attach: status=%v, want AttachOK", st)
	}
	// Let the original timer fire; expire must see attached=true / nil
	// timer and leave the sub in place.
	time.Sleep(40 * time.Millisecond)
	if !h.Has("s1") {
		t.Fatal("stale retention timer removed a re-attached subscription")
	}
}

// TestHub_ExpireUnknownAfterRemove covers expire's "subscription gone"
// branch: Remove fires before the retention timer, so the callback finds
// no subscription.
func TestHub_ExpireUnknownAfterRemove(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: 15 * time.Millisecond})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Detach("s1") // arms the timer
	h.Remove("s1") // removes before it fires; stops the timer too
	time.Sleep(40 * time.Millisecond)
	if h.Has("s1") {
		t.Fatal("sub still present after Remove")
	}
}

// TestHub_ReplayDisabledWhenBufferZero covers appendReplay's
// replayCap<=0 short-circuit. A zero ReplayBuffer falls back to the
// default, so to exercise a truly disabled ring we construct the
// subscription directly.
func TestHub_ReplayDisabledWhenBufferZero(t *testing.T) {
	t.Parallel()
	s := newSubscription(4, 0)
	s.appendReplay(Event{ID: "1", Kind: "stats_invalidated"})
	if got := s.ring.ordered(); len(got) != 0 {
		t.Fatalf("replay ordered = %d events, want 0 (ring disabled)", len(got))
	}
	if s.ring.size != 0 {
		t.Fatalf("replay size = %d, want 0 (ring disabled)", s.ring.size)
	}
	if cap(s.ring.buf) != 0 {
		t.Fatalf("replay buf cap = %d, want 0 (no allocation when disabled)", cap(s.ring.buf))
	}
	// replaySince with a non-empty lastEventID on an empty ring → gap.
	events, covered := s.replaySince("1")
	if covered || len(events) != 0 {
		t.Fatalf("empty-ring replaySince = (%d events, covered=%v), want (0, false)", len(events), covered)
	}
}

// TestHub_ReplaySinceMalformedLastEventID covers parseID's error path and
// replaySince's "unparseable lastEventID" branch: a client-supplied id
// the hub never minted yields covered=false (resync).
func TestHub_ReplaySinceMalformedLastEventID(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 8, ReplayBuffer: 8, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	if _, _, _, st := h.Attach("s1", ""); st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Deliver("s1", Event{Kind: "session_changed", SessionID: "x"})

	// Detach first (single-consumer serial reconnect) before re-attaching
	// with the malformed Last-Event-ID.
	h.Detach("s1")
	_, replay, covered, st := h.Attach("s1", "not-a-number")
	if st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	if covered {
		t.Error("covered=true for a malformed Last-Event-ID, want false")
	}
	if len(replay) != 0 {
		t.Errorf("replay len = %d, want 0 for a malformed Last-Event-ID", len(replay))
	}
}

// TestHub_DeliverWithCallerSuppliedID covers the Deliver branch where the
// caller pre-sets Event.ID (the hub must not overwrite it).
func TestHub_DeliverWithCallerSuppliedID(t *testing.T) {
	t.Parallel()
	h := New(Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	t.Cleanup(h.Shutdown)
	h.Add("s1")
	ch, _, _, st := h.Attach("s1", "")
	if st != AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}
	h.Deliver("s1", Event{ID: "999", Kind: "session_changed", SessionID: "x"})
	ev := <-ch
	if ev.ID != "999" {
		t.Errorf("ev.ID = %q, want 999 (caller-supplied id preserved)", ev.ID)
	}
}
