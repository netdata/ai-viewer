package notify

import (
	"fmt"
	"strconv"
	"testing"
)

// Characterization tests for the replay ring's behavior. They pin the
// protocol-sensitive invariants the SOW requires to be preserved across
// the SOW-0057 refactor: wrap-around retention order, oldest/newest
// coverage boundaries, replay disabled short-circuit, and behavior
// after eviction. The tests are written against the stable
// subscription contract (appendReplay + replaySince) and must pass on
// both the pre-refactor slice-with-shift implementation and the
// post-refactor O(1) ring; the SOW acceptance signal is the benchmark
// gate, not these tests.

// sessionIDs renders the SessionID of each event in order, for compact
// error messages. Empty when events is empty.
func sessionIDs(events []Event) string {
	if len(events) == 0 {
		return ""
	}
	out := ""
	for i, ev := range events {
		if i > 0 {
			out += ","
		}
		out += ev.SessionID
	}
	return out
}

// assertReplaySince probes s.replaySince with the given lastEventID
// and asserts the expected sessionID sequence and coverage flag. An
// empty wantIDs asserts the returned slice is empty; a non-empty
// wantIDs asserts the exact comma-separated SessionID sequence.
func assertReplaySince(t *testing.T, s *subscription, lastEventID string, wantCovered bool, wantIDs string) {
	t.Helper()
	out, covered := s.replaySince(lastEventID)
	if covered != wantCovered {
		t.Fatalf("replaySince(%q): covered=%v, want %v", lastEventID, covered, wantCovered)
	}
	if wantIDs != "" {
		if got := sessionIDs(out); got != wantIDs {
			t.Fatalf("replaySince(%q): got [%s], want [%s]", lastEventID, got, wantIDs)
		}
	} else if len(out) != 0 {
		t.Fatalf("replaySince(%q): got %d events, want 0", lastEventID, len(out))
	}
}

// TestSubscription_AppendReplayRetainsNewestAfterWrap pins the
// wrap-around ordering invariant: when more than cap events are
// appended, the ring retains the LAST cap events in delivery order
// (oldest at the head of the iteration, newest at the tail). This is
// what replaySince iterates, so wrap-around correctness pins replay
// output ordering under steady-state — the protocol-sensitive part.
//
// The test drives the contract via the public replaySince API so it
// stays valid across the internal-storage refactor.
func TestSubscription_AppendReplayRetainsNewestAfterWrap(t *testing.T) {
	t.Parallel()
	const ringCap = 4
	s := newSubscription(8, ringCap)

	// Deliver ringCap+5 events with deterministic sequential ids and
	// SessionIDs e0..e8. The last `ringCap` (e5..e8) must be retained
	// in delivery order; the rest must be evicted.
	total := ringCap + 5
	for i := 0; i < total; i++ {
		s.appendReplay(Event{
			ID:        strconv.Itoa(i + 1),
			Kind:      "session_changed",
			SessionID: fmt.Sprintf("e%d", i),
		})
	}

	// Evicted boundary: lastEventID below oldest retained id (6) → uncovered.
	assertReplaySince(t, s, "5", false, "")
	// Oldest-retained probe: replaySince("6") returns events with
	// id > 6 in delivery order — three events (ids 7,8,9 / e6,e7,e8).
	assertReplaySince(t, s, "6", true, "e6,e7,e8")
	// Newest probe: replaySince("9") → ([], true).
	assertReplaySince(t, s, "9", true, "")
	// One-below-newest probe: replaySince("8") → ([e8], true).
	assertReplaySince(t, s, "8", true, "e8")
	// The three probes pin the order: e6 < e7 (probe at 7 vs 8), e7 <
	// e8 (probe at 8), and e5 is older than the ring (probe at 5 is
	// uncovered). Combined with the TestSubscription_ReplaySinceAfterWrap
	// coverage-boundary pins, the full ring is [e5, e6, e7, e8] in
	// delivery order, with e0..e4 evicted.
}

// TestSubscription_AppendReplayGrowsThenWraps pins the two-phase
// growth then full-state behavior separately: under cap, append
// preserves order and grows; at cap, append evicts the oldest and
// preserves the relative order of the survivors.
//
// The test drives the contract via the public replaySince API only,
// so it stays valid across the internal-storage refactor.
func TestSubscription_AppendReplayGrowsThenWraps(t *testing.T) {
	t.Parallel()
	const ringCap = 3
	s := newSubscription(8, ringCap)

	// Phase 1: under cap. All three appended events must be present in
	// delivery order. Probe each boundary to assert relative ordering
	// without needing a "below the oldest" probe (which would yield
	// covered=false and be indistinguishable from a wrap).
	t.Run("under cap", func(t *testing.T) {
		for i := 0; i < ringCap; i++ {
			s.appendReplay(Event{ID: strconv.Itoa(i + 1), SessionID: fmt.Sprintf("g%d", i)})
		}
		// Newest is g2 (id=3): replaySince("3") → ([], true).
		assertReplaySince(t, s, "3", true, "")
		// Probe between oldest and newest: replaySince("1") → ([g1,g2], true).
		assertReplaySince(t, s, "1", true, "g1,g2")
		// Probe one below the newest: replaySince("2") → ([g2], true).
		assertReplaySince(t, s, "2", true, "g2")
		// Confirm g0 is older than g1 by checking that replaySince("0")
		// yields covered=false (oldest is 1, so 0 < oldest → gap).
		assertReplaySince(t, s, "0", false, "")
	})

	// Phase 2: overflow. Two more events (g3, g4) are appended; the
	// oldest two (g0, g1) must be evicted and the survivors (g2, g3,
	// g4) must be in delivery order. The newest is now g4 (id=5).
	t.Run("over cap", func(t *testing.T) {
		for i := ringCap; i < ringCap+2; i++ {
			s.appendReplay(Event{ID: strconv.Itoa(i + 1), SessionID: fmt.Sprintf("g%d", i)})
		}
		// Newest probe: replaySince("5") → ([], true).
		assertReplaySince(t, s, "5", true, "")
		// Oldest-retained probe: replaySince("3") → ([g3,g4], true).
		assertReplaySince(t, s, "3", true, "g3,g4")
		// Evicted probe: replaySince("2") → ([], false) — g0,g1 evicted.
		assertReplaySince(t, s, "2", false, "")
		// Middle probe: replaySince("4") → ([g4], true).
		assertReplaySince(t, s, "4", true, "g4")
	})
}

// TestSubscription_AppendReplayDisabled pins appendReplay's
// replayCap<=0 short-circuit: no storage, no growth, and replaySince
// behaves indistinguishably from an empty ring with a non-empty id.
func TestSubscription_AppendReplayDisabled(t *testing.T) {
	t.Parallel()
	s := newSubscription(4, 0)
	s.appendReplay(Event{ID: "1", Kind: "session_changed", SessionID: "e0"})
	s.appendReplay(Event{ID: "2", Kind: "session_changed", SessionID: "e1"})

	// Empty-ring branch of replaySince: covered=false for a non-empty
	// Last-Event-ID (gap cannot be covered; the ring holds nothing).
	events, covered := s.replaySince("1")
	if covered || len(events) != 0 {
		t.Fatalf("disabled-ring replaySince = (%d events, covered=%v), want (0, false)", len(events), covered)
	}
}

// TestSubscription_ReplaySinceBoundaryAtOldest pins the exact-match
// coverage boundary: when Last-Event-ID equals the oldest retained id,
// coverage is provable and the returned slice is every event strictly
// after Last-Event-ID (no false resync at the edge).
func TestSubscription_ReplaySinceBoundaryAtOldest(t *testing.T) {
	t.Parallel()
	s := newSubscription(8, 5)
	for i := 1; i <= 5; i++ {
		s.appendReplay(Event{ID: strconv.Itoa(i), SessionID: fmt.Sprintf("e%d", i)})
	}

	out, covered := s.replaySince("1") // == oldest
	if !covered {
		t.Fatal("covered=false for lastEventID==oldest, want true (boundary inclusive)")
	}
	if len(out) != 4 {
		t.Fatalf("lastEventID==oldest: replay len = %d, want 4", len(out))
	}
	if got := sessionIDs(out); got != "e2,e3,e4,e5" {
		t.Errorf("lastEventID==oldest: replay = [%s], want [e2,e3,e4,e5]", got)
	}
}

// TestSubscription_ReplaySinceBoundaryAtNewest pins the upper
// coverage boundary: when Last-Event-ID equals the newest retained id,
// coverage is provable and the returned slice is empty (the client is
// already current — no false resync, no spurious events).
func TestSubscription_ReplaySinceBoundaryAtNewest(t *testing.T) {
	t.Parallel()
	s := newSubscription(8, 5)
	for i := 1; i <= 5; i++ {
		s.appendReplay(Event{ID: strconv.Itoa(i), SessionID: fmt.Sprintf("e%d", i)})
	}

	out, covered := s.replaySince("5") // == newest
	if !covered {
		t.Fatal("covered=false for lastEventID==newest, want true (boundary inclusive)")
	}
	if len(out) != 0 {
		t.Fatalf("lastEventID==newest: replay len = %d, want 0 (client already current)", len(out))
	}
}

// TestSubscription_ReplaySinceAfterWrap pins replaySince's coverage
// reasoning AFTER the ring has wrapped: a Last-Event-ID from the
// EVICTED range yields covered=false (resync), and a Last-Event-ID
// from the RETAINED range yields the correct suffix in delivery
// order. This is the protocol-sensitive interaction that must remain
// identical to the pre-refactor slice-with-shift behavior.
func TestSubscription_ReplaySinceAfterWrap(t *testing.T) {
	t.Parallel()
	const ringCap = 3
	s := newSubscription(8, ringCap)
	// Deliver ringCap+2 = 5 events. The ring retains ids 3,4,5.
	for i := 1; i <= ringCap+2; i++ {
		s.appendReplay(Event{ID: strconv.Itoa(i), SessionID: fmt.Sprintf("e%d", i)})
	}

	// 1. Evicted lastEventID: gap cannot be covered.
	out, covered := s.replaySince("2")
	if covered {
		t.Fatal("covered=true for evicted lastEventID after wrap, want false (resync)")
	}
	if len(out) != 0 {
		t.Errorf("evicted lastEventID: replay = [%s], want []", sessionIDs(out))
	}

	// 2. Oldest-retained lastEventID: covered, suffix = [e4, e5].
	out, covered = s.replaySince("3")
	if !covered {
		t.Fatal("covered=false for oldest-retained lastEventID, want true")
	}
	if got := sessionIDs(out); got != "e4,e5" {
		t.Errorf("oldest-retained lastEventID: replay = [%s], want [e4,e5]", got)
	}

	// 3. Middle lastEventID: covered, suffix = [e5].
	out, covered = s.replaySince("4")
	if !covered {
		t.Fatal("covered=false for middle lastEventID, want true")
	}
	if got := sessionIDs(out); got != "e5" {
		t.Errorf("middle lastEventID: replay = [%s], want [e5]", got)
	}
}

// TestSubscription_ReplaySinceEmptyRing pins the empty-ring,
// non-empty-lastEventID contract: covered=false, no replay. This
// distinguishes "no events ever delivered" (covered=false) from
// "events delivered, but client is already current" (covered=true,
// empty replay).
func TestSubscription_ReplaySinceEmptyRing(t *testing.T) {
	t.Parallel()
	s := newSubscription(8, 5)
	// No appendReplay calls.

	out, covered := s.replaySince("1")
	if covered || len(out) != 0 {
		t.Fatalf("empty-ring non-empty-id: replay = (%d events, covered=%v), want (0, false)", len(out), covered)
	}
}

// TestSubscription_ReplaySinceEmptyRingEmptyID pins the fresh-stream
// contract: empty Last-Event-ID on an empty ring returns (nil, true)
// so the client starts receiving live events with no resync.
func TestSubscription_ReplaySinceEmptyRingEmptyID(t *testing.T) {
	t.Parallel()
	s := newSubscription(8, 5)

	out, covered := s.replaySince("")
	if !covered {
		t.Fatal("covered=false for fresh stream, want true")
	}
	if len(out) != 0 {
		t.Errorf("fresh stream replay = %d events, want 0", len(out))
	}
}

// TestSubscription_ReplaySinceOrderingAfterWrap drives the
// post-refactor ring through a long wrap-around and asserts the
// returned events are strictly increasing in id AND in delivery
// order. This pins the contract: replaySince returns events in the
// order they were delivered, regardless of how many times the ring
// wrapped.
func TestSubscription_ReplaySinceOrderingAfterWrap(t *testing.T) {
	t.Parallel()
	const ringCap = 4
	const total = ringCap * 5 // 20 deliveries → 4 wraps
	s := newSubscription(8, ringCap)
	for i := 1; i <= total; i++ {
		s.appendReplay(Event{ID: strconv.Itoa(i), SessionID: fmt.Sprintf("e%d", i)})
	}

	// Last-Event-ID just below the oldest retained id (total-ringCap+1=17,
	// so use 16) is evicted → covered=false. Use the actual oldest
	// retained id to drive a covered=true probe.
	out, covered := s.replaySince("16")
	if covered {
		t.Fatal("covered=true for evicted lastEventID after long wrap, want false")
	}
	_ = out

	// Probe the oldest retained id (17) — covered, events strictly
	// after cursor in delivery order (ringCap-1 events).
	out, covered = s.replaySince("17")
	if !covered {
		t.Fatal("covered=false for oldest-retained lastEventID after long wrap, want true")
	}
	if len(out) != ringCap-1 {
		t.Fatalf("replay len = %d, want %d (events strictly after oldest)", len(out), ringCap-1)
	}
	// Each event's id must be the next one after its predecessor.
	for i := 1; i < len(out); i++ {
		prev, _ := strconv.Atoi(out[i-1].ID)
		cur, _ := strconv.Atoi(out[i].ID)
		if cur != prev+1 {
			t.Fatalf("replay order broken at %d: %q then %q (must be strictly consecutive delivery order)",
				i, out[i-1].ID, out[i].ID)
		}
	}
}
