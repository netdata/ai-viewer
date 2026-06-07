package notify

import (
	"sync/atomic"
	"time"
)

// ringReplay is a fixed-capacity circular buffer of the most recently
// delivered events, used to satisfy Last-Event-ID reconnects. It is
// equivalent in observable behavior to a slice that shifts left on
// every overflow, but append is O(1) instead of O(cap): the hot path
// in Hub.Deliver records the event at the slot computed from the
// current head and advances head on overflow, so the post-cap cost is
// a single store and a modulo, not a memmove of every retained event.
//
// The ring stores newest events in append order; iteration is from
// oldest to newest (the order replaySince must return them in).
//
// All access happens under the Hub mutex; the type is not safe for
// concurrent use on its own.
type ringReplay struct {
	buf  []Event // pre-allocated to cap, or nil when cap <= 0 (disabled)
	head int     // index of the OLDEST valid entry; 0 when empty
	size int     // number of valid entries in [0, cap(buf)]
}

// newRingReplay returns a ring with the given capacity. A non-positive
// capacity yields a disabled ring (buf is nil and appends are no-ops).
// The buf slice is allocated to its full capacity so index writes in
// append do not grow the slice — the head/size pair is the source of
// truth for "which entries are currently valid".
func newRingReplay(capacity int) ringReplay {
	if capacity <= 0 {
		return ringReplay{}
	}
	return ringReplay{buf: make([]Event, capacity)}
}

// append records ev in the ring, evicting the oldest entry when the
// ring is at capacity. O(1) at all sizes. No-op when the ring is
// disabled.
func (r *ringReplay) append(ev Event) {
	n := cap(r.buf)
	if n == 0 {
		return
	}
	if r.size < n {
		// Growing: write at the slot just past the current tail.
		r.buf[(r.head+r.size)%n] = ev
		r.size++
		return
	}
	// Full: overwrite the oldest slot and advance head.
	r.buf[r.head] = ev
	r.head = (r.head + 1) % n
}

// oldest returns the oldest entry in the ring and true, or
// (zero, false) when the ring is empty.
func (r *ringReplay) oldest() (Event, bool) {
	if r.size == 0 {
		return Event{}, false
	}
	return r.buf[r.head], true
}

// newest returns the newest entry in the ring and true, or
// (zero, false) when the ring is empty.
func (r *ringReplay) newest() (Event, bool) {
	if r.size == 0 {
		return Event{}, false
	}
	n := cap(r.buf)
	return r.buf[(r.head+r.size-1)%n], true
}

// ordered returns a fresh slice of the ring's entries in oldest-to-newest
// order — the order replaySince must iterate. Allocates a new slice of
// the current size; callers that need zero allocation can use a
// manual loop with the (head, size) invariants.
func (r *ringReplay) ordered() []Event {
	if r.size == 0 {
		return nil
	}
	n := cap(r.buf)
	out := make([]Event, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(r.head+i)%n]
	}
	return out
}

// subscription is one SSE client's delivery state inside the Hub. All
// fields are accessed under the Hub's mutex except dropped, which is an
// atomic so Dropped() can read it without contending on the hub lock.
type subscription struct {
	// ch carries decided events to the client's stream goroutine. It is
	// buffered to ChannelCap; a full channel triggers drop-oldest in
	// enqueue so a slow client never blocks Deliver or the hub.
	ch chan Event
	// ring is a fixed-capacity circular buffer of the most recently
	// delivered events, used to satisfy Last-Event-ID reconnects. Its
	// capacity is Options.ReplayBuffer. The ring's append is O(1) at
	// all sizes (see ringReplay above for the overflow algorithm).
	ring ringReplay
	// dropped counts events discarded by drop-oldest backpressure. Read
	// lock-free via Dropped().
	dropped atomic.Uint64
	// attached is true while a client stream is connected. Detach sets it
	// false and arms retentionTimer; Attach sets it true and cancels the
	// timer. It also serves as the single-consumer guard: a second Attach
	// while attached is rejected as BUSY (one stream per subscription).
	attached bool
	// retentionTimer, when non-nil, will remove this subscription after
	// the retention window unless a re-Attach cancels it first.
	retentionTimer *time.Timer
	// gen is the retention generation. Each Detach increments it and the
	// armed timer captures the new value; expire removes the subscription
	// only when the firing generation still matches gen. This defeats a
	// stale callback from an earlier detach→attach→detach cycle that would
	// otherwise remove the subscription during a newer retention window.
	gen uint64
}

// newSubscription builds a subscription with the given channel and
// replay capacities. A non-positive replayCap disables the ring
// (appendReplay becomes a no-op, replaySince reports uncovered for any
// non-empty Last-Event-ID).
func newSubscription(channelCap, replayCap int) *subscription {
	return &subscription{
		ch:   make(chan Event, channelCap),
		ring: newRingReplay(replayCap),
	}
}

// enqueue appends ev to the replay ring and pushes it onto the delivery
// channel. When the channel is full it drops the OLDEST queued event and
// increments dropped, then enqueues ev — so the newest events always
// survive and Deliver is non-blocking (sse-protocol.md §Backpressure).
// Must be called under the hub mutex.
func (s *subscription) enqueue(ev Event) {
	s.ring.append(ev)
	for {
		select {
		case s.ch <- ev:
			return
		default:
			// Channel full: discard the oldest queued event to make room.
			// A concurrent reader may have drained the channel between the
			// failed send and this receive, so the receive is also
			// guarded by select/default to stay non-blocking.
			select {
			case <-s.ch:
				s.dropped.Add(1)
			default:
			}
		}
	}
}

// appendReplay records ev in the replay ring, evicting the oldest
// entry when the ring is at capacity. It is a thin package-internal
// wrapper around ringReplay.append so callers (including tests) can
// drive the ring without reaching into its internals. Must be called
// under the hub mutex; no-op when the ring is disabled (cap <= 0).
func (s *subscription) appendReplay(ev Event) {
	s.ring.append(ev)
}

// parseAndValidateBounds checks whether the ring can prove full
// coverage for the given lastEventID. It parses the client-supplied
// cursor, fetches the retained oldest/newest entries, and verifies
// that lastEventID falls within [oldest, newest]. Returns the parsed
// numeric lastEventID and true when coverage is provable; (0, false)
// when it is not. The caller must handle the empty-lastEventID case
// before calling this helper. Must be called under the hub mutex.
func (s *subscription) parseAndValidateBounds(lastEventID string) (uint64, bool) {
	last, ok := parseID(lastEventID)
	if !ok {
		return 0, false
	}
	oldestEv, ok := s.ring.oldest()
	if !ok {
		return 0, false
	}
	oldest, ok := parseID(oldestEv.ID)
	if !ok {
		return 0, false
	}
	if oldest > last {
		return 0, false
	}
	newestEv, ok := s.ring.newest()
	if !ok {
		return 0, false
	}
	newest, ok := parseID(newestEv.ID)
	if !ok {
		return 0, false
	}
	if last > newest {
		return 0, false
	}
	return last, true
}

// replaySince returns the buffered events whose ID is ordered strictly
// after lastEventID, plus whether the buffer can prove it covers the gap.
//
//   - lastEventID == "": fresh stream, nothing to replay, covered=true.
//   - oldest retained ID <= lastEventID: every event this sub ever
//     received with ID > lastEventID is still in the ring (eviction is
//     oldest-first), so the missed events are exactly those returned and
//     covered=true. This also covers the "client already current" case
//     (replay empty, covered=true).
//   - oldest retained ID > lastEventID: events in (lastEventID, oldest]
//     may have been evicted, so the hub cannot prove full coverage —
//     returns an empty slice with covered=false and the caller sends a
//     resync.
//   - lastEventID > newest retained ID: the hub never delivered an event
//     this new, so lastEventID is stale (a previous hub instance) or
//     forged. It cannot be covered → covered=false and the caller sends a
//     resync (sse-protocol.md §Reconnect Behavior).
//   - empty ring with a non-empty lastEventID: a potential gap we cannot
//     disprove → covered=false.
//
// Event IDs are a hub-wide strictly increasing counter. Because the
// counter is shared across subscriptions, one sub's consecutive events
// may carry non-consecutive IDs, so coverage is decided by the
// oldest-retained-ID rule above (NOT by lastEventID+1). IDs are decimal
// strings compared numerically via parseID — lexical comparison would be
// wrong ("10" < "9"). Must be called under the hub mutex.
func (s *subscription) replaySince(lastEventID string) (events []Event, covered bool) {
	if lastEventID == "" {
		return nil, true
	}
	last, ok := s.parseAndValidateBounds(lastEventID)
	if !ok {
		return nil, false
	}
	ordered := s.ring.ordered()
	out := make([]Event, 0, len(ordered))
	for _, ev := range ordered {
		if id, ok := parseID(ev.ID); ok && id > last {
			out = append(out, ev)
		}
	}
	return out, true
}
