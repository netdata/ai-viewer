package presenter

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// randReader is the entropy source for newSubscriptionID. It is a
// package-level variable (defaulting to crypto/rand.Reader) only so tests
// can substitute a failing reader to exercise the RNG-failure path; it is
// never reassigned at runtime.
var randReader io.Reader = rand.Reader //nolint:revive // explicit io.Reader: tests reassign this var with a different concrete reader (failingReader), so inferring rand.reader would break those assignments

// subscriptionEntry is one registered subscription's server-side state: the
// validated filter the poller matches against and the creation time
// (diagnostics). The hub owns the delivery channel + replay ring; this
// registry owns the filter and mirrors lifecycle into the hub.
type subscriptionEntry struct {
	filter    subscriptionFilter
	createdAt time.Time
}

// subscriptionManager is the REST-facing registry of SSE subscriptions. It
// maps subscription id → entry under a mutex and mirrors create/delete into
// the hub so the two stay consistent (Add on create, Remove on delete). The
// poller snapshots the entries (without holding the lock during SQL) to
// decide deliveries.
//
// Lock-ordering contract (deadlock safety): the manager mutex (m.mu), the hub
// mutex, and the presenter's notifyMu are NEVER held simultaneously. Each path
// takes one lock, releases it, then takes the next:
//   - create: [presenter.sseLifecycleMu held by createSubscriptionLifecycle] →
//     hub.mu (via hub.Add) → release → m.mu → release
//   - delete: m.mu → release → hub.mu (via hub.Remove) → release →
//     OnRemove hook → m.mu (forget) → release → notifyMu
//   - expiry: hub.mu (in expire) → release → OnRemove hook → m.mu → notifyMu
//
// The presenter's sseLifecycleMu is the OUTERMOST lock and is held across the
// whole of create (hub.Add + the m.mu insert) when create runs via
// createSubscriptionLifecycle, so the documented one-directional order is
// sseLifecycleMu → hub.mu → m.mu. No path acquires sseLifecycleMu while
// already holding hub.mu / m.mu / notifyMu, and ShutdownSSE never holds
// sseLifecycleMu while calling hub.Shutdown (presenter.md §SSE Lifecycle
// Mutex). Because the hub invokes OnRemove only AFTER dropping its own lock,
// and the hook (onSubRemoved) never calls back into the hub or takes
// sseLifecycleMu, no cycle can form. Do NOT refactor any of these to hold two
// of these locks at once.
type subscriptionManager struct {
	hub *notifyHubAdder

	mu      sync.Mutex
	entries map[string]subscriptionEntry

	// createHook, when non-nil, is invoked inside create AFTER the id is
	// generated but BEFORE hub.Add and the registry insert. It is nil in
	// production and exists only so a test can drive a concurrent
	// ShutdownSSE into the exact window between the shutting-down check and
	// the hub/registry mutation, proving the SSE lifecycle mutex closes that
	// TOCTOU (presenter.md §SSE Lifecycle Mutex).
	createHook func()
}

// notifyHubAdder is the narrow slice of the notify.Hub the manager needs:
// register and unregister a subscription id. Declaring it here keeps the
// manager decoupled from the rest of the hub surface and documents exactly
// which hub operations are lifecycle-coupled to the registry.
type notifyHubAdder struct {
	add    func(id string)
	remove func(id string)
}

// newSubscriptionManager builds a manager bound to hub's Add/Remove.
func newSubscriptionManager(hub interface {
	Add(string)
	Remove(string)
},
) *subscriptionManager {
	return &subscriptionManager{
		hub:     &notifyHubAdder{add: hub.Add, remove: hub.Remove},
		entries: make(map[string]subscriptionEntry),
	}
}

// create mints a fresh subscription id, registers the id in the hub, stores
// the filter, and returns the id. The id is "sub-" + 32 lowercase hex chars
// (128-bit crypto-random) per sse-protocol.md. It returns an error (and
// registers nothing) when the cryptographic RNG fails: per rest-api.md
// §POST /api/subscriptions the server returns 500 INTERNAL_ERROR rather
// than handing out a weak/predictable id, so the id is generated BEFORE any
// hub or registry mutation to keep the failure side-effect-free.
//
// The hub.add MUST precede publishing the registry entry: the poller
// snapshots the registry to decide deliveries, so if the entry were visible
// before the hub knew the id, a poll racing in between would match the
// filter and call hub.Deliver — which returns false (sub not yet in the
// hub) and silently DROPS the event before the client ever connects.
// Adding to the hub first guarantees any matched event lands in the hub's
// replay buffer instead (sse-protocol.md §Reconnect Behavior).
// maxSubscriptions caps the number of concurrent SSE subscriptions (SOW-0019,
// defense-in-depth). A single-user localhost app will never approach this; the
// cap prevents a runaway client (or a bug) from leaking goroutines/memory via
// orphan subscriptions. Returns 503 when the cap is hit so the client can retry.
const maxSubscriptions = 100

func (m *subscriptionManager) create(filter subscriptionFilter) (string, error) {
	// Cap check (SOW-0019): reject if at the limit so the registry can't grow
	// unbounded. The check is under the lock to avoid a TOCTOU race.
	m.mu.Lock()
	if len(m.entries) >= maxSubscriptions {
		m.mu.Unlock()
		return "", errTooManySubscriptions
	}
	m.mu.Unlock()

	id, err := newSubscriptionID()
	if err != nil {
		return "", err
	}
	if m.createHook != nil {
		// Test-only seam (nil in production): lets a test interleave a
		// concurrent ShutdownSSE between id generation and the hub/registry
		// mutation below. See createHook's doc and presenter.md §SSE
		// Lifecycle Mutex.
		m.createHook()
	}
	m.hub.add(id)
	m.mu.Lock()
	// Re-check under the lock: a concurrent create may have pushed us over.
	if len(m.entries) >= maxSubscriptions {
		m.mu.Unlock()
		m.hub.remove(id)
		return "", errTooManySubscriptions
	}
	m.entries[id] = subscriptionEntry{filter: filter, createdAt: time.Now().UTC()}
	m.mu.Unlock()
	return id, nil
}

// errTooManySubscriptions is returned when the subscription cap is reached.
var errTooManySubscriptions = errors.New("too many concurrent subscriptions")

// delete removes a subscription from the registry and the hub. Idempotent:
// deleting an unknown id is a no-op (the DELETE route returns 204 either
// way). The hub's OnRemove hook (onSubRemoved) then runs forget() for the
// same id — a no-op here since the entry is already gone — and drops the
// poller's statsCoalesce entry, so explicit delete and retention expiry
// share one cleanup path.
func (m *subscriptionManager) delete(id string) {
	m.mu.Lock()
	delete(m.entries, id)
	m.mu.Unlock()
	m.hub.remove(id)
}

// forget drops ONLY the registry entry for id, without touching the hub. It
// is the presenter's OnRemove hook target: the hub has already removed the
// subscription (retention expiry or explicit Remove) and calls back here to
// drop the mirrored filter so it cannot leak. Calling the hub from here
// would recurse; forget never does.
func (m *subscriptionManager) forget(id string) {
	m.mu.Lock()
	delete(m.entries, id)
	m.mu.Unlock()
}

// clear drops every registry entry without touching the hub. It is the
// registry half of ShutdownSSE: hub.Shutdown deletes every subscription
// WITHOUT firing OnRemove (the whole process is going away), which would
// otherwise leave the registry reporting subscriptions the closed hub no
// longer holds (a stale /api/health sse.subscriptions count and an orphan
// for any create that completed just before the flag flipped). clear keeps
// the registry and the hub consistent post-shutdown (both empty). It does
// NOT call the hub — calling it after hub.Shutdown would be a no-op anyway
// and must not be done while holding sseLifecycleMu (presenter.md §SSE
// Lifecycle Mutex / §Graceful Shutdown).
func (m *subscriptionManager) clear() {
	m.mu.Lock()
	m.entries = make(map[string]subscriptionEntry)
	m.mu.Unlock()
}

// has reports whether the registry holds id.
func (m *subscriptionManager) has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[id]
	return ok
}

// count returns the number of registered subscriptions (surfaced by
// /api/health as sse.subscriptions). The registry and the hub are kept in
// lockstep: create adds to both, and every hub removal (explicit delete or
// retention expiry) drops the registry entry via the OnRemove hook, so the
// count never counts subscriptions the hub has already dropped.
func (m *subscriptionManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// snapshot returns a copy of (id, filter) pairs so the poller can evaluate
// matches WITHOUT holding the registry lock during SQL. Mutating the result
// does not affect the registry.
func (m *subscriptionManager) snapshot() []subscriptionSnapshotItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]subscriptionSnapshotItem, 0, len(m.entries))
	for id, e := range m.entries {
		out = append(out, subscriptionSnapshotItem{id: id, filter: e.filter})
	}
	return out
}

// subscriptionSnapshotItem is one (id, filter) pair from snapshot().
type subscriptionSnapshotItem struct {
	id     string
	filter subscriptionFilter
}

// newSubscriptionID returns "sub-" + 32 lowercase hex chars from 16
// crypto-random bytes (128-bit). The cryptographic RNG does not fail in
// practice on Linux, but if io.ReadFull does fail this returns ("", err)
// with NO fallback: rest-api.md §POST /api/subscriptions mandates a 500
// INTERNAL_ERROR over a weak/predictable id. The entropy source is the
// package-level randReader (crypto/rand.Reader at runtime; overridable in
// tests).
func newSubscriptionID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		return "", err
	}
	return "sub-" + hex.EncodeToString(b[:]), nil
}

// createSubscriptionResponse is the POST /api/subscriptions success body.
type createSubscriptionResponse struct {
	ID     string           `json:"id"`
	Filter normalizedFilter `json:"filter_normalized"`
}

// handleSubscriptionsCreate answers POST /api/subscriptions. It parses and
// validates the filter (reusing the Chunk-12 rules via
// parseSubscriptionFilter), creates the subscription, and returns
// {id, filter_normalized}. The body is already capped at 1 MB by
// bodyLimitMiddleware. Non-POST methods are 405.
func (p *Presenter) handleSubscriptionsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader trips here when the body exceeds the cap; surface a
		// 400 rather than a 500 so a runaway client sees a clear error.
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "request body could not be read", nil)
		return
	}
	// Parse/validate BEFORE taking the SSE lifecycle mutex: a bad filter is a
	// 400 regardless of shutdown, and validation must not hold a lock the
	// shutdown path also needs.
	filter, err := parseSubscriptionFilter(body)
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	id, status := p.createSubscriptionLifecycle(filter)
	switch status {
	case http.StatusServiceUnavailable:
		// SSE shutdown has begun: hub.Add is a no-op after Shutdown, so
		// creating would hand back an id the closed hub already dropped — a
		// subscription that never receives events (rest-api.md §POST
		// /api/subscriptions). The lifecycle mutex makes the shutting-down
		// check and the create one atomic step (presenter.md §SSE Lifecycle
		// Mutex).
		writeJSONError(w, r, p.logger, http.StatusServiceUnavailable,
			CodeUnavailable, "server is shutting down", nil)
		return
	case http.StatusInternalServerError:
		// crypto/rand failed (effectively never on Linux). Surface a 500
		// rather than a weak/predictable id — no subscription was registered
		// (rest-api.md §POST /api/subscriptions). The log carries request_id.
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "failed to generate subscription id", nil)
		return
	}
	writeJSON(w, r, p.logger, createSubscriptionResponse{
		ID:     id,
		Filter: filter.normalized(),
	})
}

// createSubscriptionLifecycle is the create half of the SSE lifecycle critical
// section. It serializes the shutting-down check together with the registry
// create (hub.Add + registry insert) under p.sseLifecycleMu so a concurrent
// ShutdownSSE cannot interleave between them (presenter.md §SSE Lifecycle
// Mutex; rest-api.md §POST /api/subscriptions). It returns:
//
//   - (id, 200) when the subscription was created and is live in both the hub
//     and the registry;
//   - ("", 503) when SSE shutdown has begun — nothing was mutated;
//   - ("", 500) when the cryptographic RNG failed — nothing was mutated.
//
// The mutex spans [check shutting-down, create]; the long-running hub close in
// ShutdownSSE runs OUTSIDE the mutex (only the state flip is guarded), so an
// in-flight create is never blocked longer than that flip.
//
// Lock-ordering: this acquires p.sseLifecycleMu, and create then takes hub.mu
// (via hub.Add) and subscriptionManager.mu in turn. No path acquires
// sseLifecycleMu while already holding hub.mu / subscriptionManager.mu /
// notifyMu, and ShutdownSSE never holds sseLifecycleMu while calling
// hub.Shutdown, so no cycle can form.
func (p *Presenter) createSubscriptionLifecycle(filter subscriptionFilter) (string, int) {
	p.sseLifecycleMu.Lock()
	defer p.sseLifecycleMu.Unlock()
	if p.sseShuttingDown {
		return "", http.StatusServiceUnavailable
	}
	id, err := p.subs.create(filter)
	if err != nil {
		return "", http.StatusInternalServerError
	}
	return id, http.StatusOK
}

// handleSubscriptionDelete answers DELETE /api/subscriptions/{id}. It is
// idempotent: an unknown or already-expired id still returns 204 No Content.
// A control character in the path id is a 400 (mirrors the Chunk-12 path-id
// rule). Non-DELETE methods are 405.
func (p *Presenter) handleSubscriptionDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	idRaw := r.PathValue("id")
	if err := rejectControlChars("id", idRaw); err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	id := strings.TrimSpace(idRaw)
	if id == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "subscription id is required", nil)
		return
	}
	p.subs.delete(id)
	w.WriteHeader(http.StatusNoContent)
}
