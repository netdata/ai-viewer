// Package notify provides an in-memory fan-out Hub for Server-Sent
// Events. It is the transport-agnostic, filter-agnostic core that the
// presenter layer drives: the presenter decides which events a
// subscription should receive (by polling the SQLite notify table and
// matching filters) and calls Deliver; this package owns subscription
// delivery channels, drop-oldest backpressure, Last-Event-ID replay, and
// post-disconnect retention.
//
// The Hub knows nothing about SQL, HTTP, or filters. It is safe for
// concurrent use by many goroutines (a poller delivering, per-client
// stream goroutines attaching/detaching, the REST layer
// adding/removing). Behavior is specified in
// .agents/sow/specs/sse-protocol.md (§Backpressure, §Reconnect Behavior).
package notify

import (
	"sync"
	"sync/atomic"
	"time"
)

// Event is one deliverable SSE event, already matched and decided by the
// caller. The Hub assigns ID when the caller leaves it empty so
// Last-Event-ID replay works; callers may also set ID themselves (e.g.
// to mirror a notify-table seq) as long as the values are monotonic
// decimal strings.
type Event struct {
	// ID is the SSE "id:" value, a hub-monotonic decimal string. Assigned
	// by Deliver when empty.
	ID string
	// Kind is "session_changed" | "stats_invalidated" |
	// "source_status_changed".
	Kind string
	// SessionID / RootSessionID are set for session_changed events.
	SessionID     string
	RootSessionID string
	// SourceID is set for source_status_changed events.
	SourceID string
	// TS is the event timestamp in UNIX-microseconds.
	TS int64
}

// Default Options values applied by New when a field is left zero. They
// mirror the constants in sse-protocol.md: a 256-deep per-client buffer,
// a 100-event Last-Event-ID replay ring, and a 60s post-disconnect
// retention window for fast reconnects.
const (
	defaultChannelCap   = 256
	defaultReplayBuffer = 100
	defaultRetention    = 60 * time.Second
)

// AttachStatus is the outcome of Attach. A subscription is
// single-consumer: at most one stream may be attached at a time
// (sse-protocol.md §Subscription Lifecycle).
type AttachStatus int

const (
	// AttachUnknown means the subscription does not exist (or the hub is
	// shut down). The handler maps this to 404 NOT_FOUND.
	AttachUnknown AttachStatus = iota
	// AttachOK means the caller is now the subscription's single consumer
	// and owns the returned channel until it Detaches.
	AttachOK
	// AttachBusy means another stream is already attached to this
	// subscription. The handler maps this to 409 CONFLICT and MUST NOT use
	// the (nil) channel — a second consumer would split the event stream.
	AttachBusy
)

// Options configure a Hub. Zero-valued fields fall back to the
// sse-protocol.md defaults. Now is injectable so tests can pin time; the
// Hub uses it for any wall-clock needs (the retention timer itself is
// driven by the Retention duration via time.AfterFunc, so tests use a
// short Retention rather than a fake clock).
type Options struct {
	ChannelCap   int
	ReplayBuffer int
	Retention    time.Duration
	Now          func() time.Time
	// OnRemove, when set, is called with a subscription id AFTER the hub
	// drops it — on retention expiry AND on explicit Remove, exactly once
	// per removal. It is NOT called during Shutdown (the whole process is
	// going away). The presenter uses it to drop the subscription's
	// server-side filter and coalesce state so they do not leak past the
	// hub's own lifetime.
	//
	// CRITICAL: OnRemove is invoked WITHOUT the hub mutex held, so the
	// callback may safely call back into the hub. It MUST NOT, however,
	// call hub.Remove for the same id (the removal already happened) — that
	// would be redundant work, not a deadlock.
	OnRemove func(id string)
}

func (o Options) withDefaults() Options {
	if o.ChannelCap <= 0 {
		o.ChannelCap = defaultChannelCap
	}
	if o.ReplayBuffer <= 0 {
		o.ReplayBuffer = defaultReplayBuffer
	}
	if o.Retention <= 0 {
		o.Retention = defaultRetention
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// Hub fans events out to SSE subscriptions. A single mutex guards the
// subscription map and every per-subscription field except the atomic
// dropped counter; the lock is held only for short, non-blocking work
// (enqueue is non-blocking by construction), so a slow client never
// stalls Deliver or other subscriptions.
type Hub struct {
	opts Options

	mu       sync.Mutex
	subs     map[string]*subscription
	shutdown bool

	// seq mints monotonic event IDs. Atomic so ID assignment in Deliver
	// does not depend on the mutex ordering.
	seq atomic.Uint64
}

// New constructs a Hub with the given options (zero fields take the
// sse-protocol.md defaults).
func New(opts Options) *Hub {
	return &Hub{
		opts: opts.withDefaults(),
		subs: make(map[string]*subscription),
	}
}

// SetOnRemove installs (or replaces) the OnRemove hook after construction.
// The serve binary passes the presenter a pre-built hub, so the presenter
// wires its per-subscription cleanup via this setter rather than at New.
// Call it before the hub starts serving; it is mutex-guarded for safety.
// See Options.OnRemove for the invocation contract (called without the hub
// lock held; must not re-Remove the same id).
func (h *Hub) SetOnRemove(fn func(id string)) {
	h.mu.Lock()
	h.opts.OnRemove = fn
	h.mu.Unlock()
}

// Add registers a new subscription. Idempotent: a second Add for the same
// id is a no-op (the existing subscription, channel, and replay buffer
// are preserved). No-op after Shutdown.
func (h *Hub) Add(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shutdown {
		return
	}
	if _, ok := h.subs[id]; ok {
		return
	}
	h.subs[id] = newSubscription(h.opts.ChannelCap, h.opts.ReplayBuffer)
}

// Remove hard-removes a subscription (DELETE /api/subscriptions). Cancels
// any pending retention timer, closes the delivery channel so a connected
// stream goroutine unblocks, and forgets the replay buffer. Removing an
// unknown id is a no-op. The OnRemove hook (if set) fires after the lock
// is released so the presenter can drop its per-subscription state.
func (h *Hub) Remove(id string) {
	h.mu.Lock()
	removed := h.removeLocked(id)
	hook := h.opts.OnRemove
	h.mu.Unlock()
	if removed && hook != nil {
		hook(id)
	}
}

// removeLocked removes a subscription and reports whether one was actually
// present. The caller must hold h.mu and, when this returns true, MUST
// capture h.opts.OnRemove under the lock and invoke it AFTER releasing the
// lock so the hook runs without the hub mutex held (avoids a lock-ordering
// deadlock with the presenter's registry lock).
func (h *Hub) removeLocked(id string) bool {
	s, ok := h.subs[id]
	if !ok {
		return false
	}
	if s.retentionTimer != nil {
		s.retentionTimer.Stop()
		s.retentionTimer = nil
	}
	close(s.ch)
	delete(h.subs, id)
	return true
}

// Has reports whether a subscription with id is registered.
func (h *Hub) Has(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.subs[id]
	return ok
}

// IDs returns a snapshot of the registered subscription ids. The poller
// iterates this to decide deliveries. The returned slice is a copy;
// mutating it does not affect the hub.
func (h *Hub) IDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.subs))
	for id := range h.subs {
		out = append(out, id)
	}
	return out
}

// Deliver enqueues ev to the subscription's channel. It is non-blocking:
// when the channel is full the oldest queued event is dropped and the
// per-subscription dropped counter is incremented (drop-oldest
// backpressure), so a slow consumer never blocks Deliver or other
// subscriptions. The event is also appended to the replay ring for
// Last-Event-ID reconnects. When ev.ID is empty the Hub assigns the next
// monotonic id. Returns false if the subscription is unknown (or the hub
// is shut down).
//
// ID assignment happens UNDER the lock, atomically with the enqueue, so the
// id order and the replay-ring append order always agree. Minting the id
// before the lock would let two concurrent Delivers mint ids in one order
// and enqueue in the other, leaving the replay ring out of order and
// breaking replaySince's oldest/newest coverage reasoning.
func (h *Hub) Deliver(id string, ev Event) (delivered bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shutdown {
		return false
	}
	s, ok := h.subs[id]
	if !ok {
		return false
	}
	if ev.ID == "" {
		ev.ID = h.nextID()
	}
	s.enqueue(ev)
	return true
}

// nextID returns the next monotonic event id as a decimal string.
func (h *Hub) nextID() string {
	return formatID(h.seq.Add(1))
}

// Attach connects a client to a subscription as its SINGLE consumer. It
// returns the delivery channel, any buffered events to replay after
// lastEventID, whether the replay buffer could cover the gap since
// lastEventID, and an AttachStatus.
//
//   - AttachUnknown: the subscription is unknown or expired (or the hub is
//     shut down); the caller returns 404. Channel is nil.
//   - AttachBusy: another stream is already attached; the caller returns
//     409 CONFLICT. Channel is nil — a second consumer would split events.
//   - AttachOK, covered=true: replay holds exactly the events the client
//     missed (possibly empty for a fresh connect with empty lastEventID
//     or an already-current client); stream them, then live events.
//   - AttachOK, covered=false: the buffer cannot prove it covers the gap
//     (client offline too long / events evicted / a stale-or-forged
//     lastEventID); replay is empty and the caller sends a resync event.
//
// On AttachOK, Attach cancels any pending retention timer and marks the
// subscription attached, so a fast (serial) reconnect resumes the same
// subscription (and its replay buffer) instead of being dropped.
func (h *Hub) Attach(id, lastEventID string) (ch <-chan Event, replay []Event, covered bool, status AttachStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, exists := h.subs[id]
	if !exists || h.shutdown {
		return nil, nil, false, AttachUnknown
	}
	if s.attached {
		// A stream is already connected: reject the second consumer so the
		// two connections do not split this subscription's events.
		return nil, nil, false, AttachBusy
	}
	if s.retentionTimer != nil {
		s.retentionTimer.Stop()
		s.retentionTimer = nil
	}
	s.attached = true
	events, cov := s.replaySince(lastEventID)
	return s.ch, events, cov, AttachOK
}

// Detach marks the subscription's client as disconnected and arms the
// retention timer. If no Attach arrives within Options.Retention the
// subscription is removed. Detaching an unknown or already-detached
// subscription is a no-op (the latter keeps the existing timer so the
// original disconnect deadline stands).
func (h *Hub) Detach(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.subs[id]
	if !ok || h.shutdown {
		return
	}
	if !s.attached {
		// Already detached; do not restart the timer.
		return
	}
	s.attached = false
	if s.retentionTimer != nil {
		s.retentionTimer.Stop()
	}
	// Each Detach starts a NEW retention window: bump the generation and
	// capture BOTH the subscription object and the generation in the timer
	// closure. AfterFunc fires on its own goroutine; expire removes the
	// subscription only when the live entry is still THIS object AND its
	// generation still matches. The object check defeats a stale callback
	// for an object that was Removed and re-Added under the same id (a new
	// object can reach the same generation via its own detach cycle); the
	// generation check defeats a stale callback from an earlier
	// detach→attach→detach cycle on the SAME object (whose timer Stop lost
	// the race).
	s.gen++
	gen := s.gen
	s.retentionTimer = time.AfterFunc(h.opts.Retention, func() {
		h.expire(id, s, gen)
	})
}

// expire is the retention-timer callback. It removes the subscription only
// if the live entry for id is still the SAME object the timer was armed
// for, that object is still detached, and the firing generation is still
// the live one — so neither a re-Attach, a Remove, nor a Remove+re-Add
// under the same id can be undone by a late callback. The OnRemove hook
// fires after the lock is released.
func (h *Hub) expire(id string, s *subscription, gen uint64) {
	h.mu.Lock()
	cur, ok := h.subs[id]
	if !ok || cur != s {
		// Subscription gone, or replaced by a different object under the
		// same id (Remove+re-Add): the timer is stale.
		h.mu.Unlock()
		return
	}
	if s.attached || s.gen != gen {
		h.mu.Unlock()
		return
	}
	removed := h.removeLocked(id)
	hook := h.opts.OnRemove
	h.mu.Unlock()
	if removed && hook != nil {
		hook(id)
	}
}

// Dropped returns the number of events discarded by drop-oldest
// backpressure for the subscription. Unknown id returns 0.
func (h *Hub) Dropped(id string) uint64 {
	h.mu.Lock()
	s, ok := h.subs[id]
	h.mu.Unlock()
	if !ok {
		return 0
	}
	return s.dropped.Load()
}

// Shutdown closes every subscription's delivery channel so per-client
// stream goroutines unblock, stops all retention timers, and marks the
// hub shut down (further Add/Deliver/Attach become no-ops). Idempotent.
// Callers that want clients to receive a final "disconnect" event must
// Deliver it before calling Shutdown.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shutdown {
		return
	}
	h.shutdown = true
	for id, s := range h.subs {
		if s.retentionTimer != nil {
			s.retentionTimer.Stop()
			s.retentionTimer = nil
		}
		close(s.ch)
		delete(h.subs, id)
	}
}
