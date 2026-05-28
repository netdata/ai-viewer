package notify

import (
	"sync/atomic"
	"time"
)

// subscription is one SSE client's delivery state inside the Hub. All
// fields are accessed under the Hub's mutex except dropped, which is an
// atomic so Dropped() can read it without contending on the hub lock.
type subscription struct {
	// ch carries decided events to the client's stream goroutine. It is
	// buffered to ChannelCap; a full channel triggers drop-oldest in
	// enqueue so a slow client never blocks Deliver or the hub.
	ch chan Event
	// replay is a fixed-capacity ring of the most recently delivered
	// events, newest last, used to satisfy Last-Event-ID reconnects. Its
	// capacity is Options.ReplayBuffer.
	replay []Event
	// replayCap is the ring capacity, cached so enqueue does not re-read
	// the option.
	replayCap int
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

// newSubscription builds a subscription with the given channel and replay
// capacities.
func newSubscription(channelCap, replayCap int) *subscription {
	return &subscription{
		ch:        make(chan Event, channelCap),
		replay:    make([]Event, 0, replayCap),
		replayCap: replayCap,
	}
}

// enqueue appends ev to the replay ring and pushes it onto the delivery
// channel. When the channel is full it drops the OLDEST queued event and
// increments dropped, then enqueues ev — so the newest events always
// survive and Deliver is non-blocking (sse-protocol.md §Backpressure).
// Must be called under the hub mutex.
func (s *subscription) enqueue(ev Event) {
	s.appendReplay(ev)
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

// appendReplay records ev in the ring, evicting the oldest entry when the
// ring is at capacity. Must be called under the hub mutex.
func (s *subscription) appendReplay(ev Event) {
	if s.replayCap <= 0 {
		return
	}
	if len(s.replay) < s.replayCap {
		s.replay = append(s.replay, ev)
		return
	}
	// Full: shift left by one and append (ring of fixed size, newest last).
	copy(s.replay, s.replay[1:])
	s.replay[len(s.replay)-1] = ev
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
	last, ok := parseID(lastEventID)
	if !ok {
		// A client-supplied Last-Event-ID we did not mint (malformed or
		// from a previous hub instance). We cannot reason about it → ask
		// the client to resync.
		return nil, false
	}
	if len(s.replay) == 0 {
		return nil, false
	}
	oldest, ok := parseID(s.replay[0].ID)
	if !ok {
		return nil, false
	}
	if oldest > last {
		// The ring may have dropped events between last and oldest.
		return nil, false
	}
	newest, ok := parseID(s.replay[len(s.replay)-1].ID)
	if !ok {
		return nil, false
	}
	if last > newest {
		// last is ahead of anything the hub ever delivered: a stale id from
		// a previous hub instance or a forged value. Cannot be covered.
		return nil, false
	}
	out := make([]Event, 0, len(s.replay))
	for _, ev := range s.replay {
		if id, ok := parseID(ev.ID); ok && id > last {
			out = append(out, ev)
		}
	}
	return out, true
}
