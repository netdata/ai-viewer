package presenter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// disconnectReason / disconnectRetryMS are the fixed shutdown payload the
// server emits to every SSE client at graceful shutdown so browsers
// reconnect cleanly after the configured backoff (sse-protocol.md
// §disconnect).
const (
	disconnectReason  = "server_shutdown"
	disconnectRetryMS = 2000
)

// handleEvents answers GET /api/events?sub=<id>: the SSE stream for a
// subscription. Lifecycle:
//
//  1. method gate (GET/HEAD only; others 405);
//  2. validate sub (missing/empty/control-char → 400);
//  3. HEAD: a NON-mutating existence check (hub.Has). 200 + SSE headers +
//     empty body if the sub exists, 404 if not. HEAD never Attaches,
//     consumes the channel, or touches the connect/disconnect lifecycle
//     (sse-protocol.md §Subscription Lifecycle, RFC 9110 §9.3.2).
//  4. GET: Attach as the subscription's SINGLE consumer. AttachUnknown →
//     404; AttachBusy (a stream is already live) → 409 CONFLICT; AttachOK →
//     stream. Only AttachOK arms the deferred Detach (which starts the 60s
//     reconnect-retention timer) — a 404/409 must NOT touch the lifecycle.
//  5. write the SSE headers and flush them; clear the per-connection write
//     deadline so the long-lived stream is not killed by a server-wide
//     WriteTimeout (it is unset, but this is defensive — presenter.md
//     §Middlewares);
//  6. if the replay buffer could not cover Last-Event-ID, send a resync;
//     then replay any buffered events the client missed;
//  7. loop: forward live events, emit a keepalive comment on the idle
//     ticker, and return when the client disconnects (r.Context().Done),
//     the hub closes the channel (Remove/Shutdown), or a write/flush fails.
func (p *Presenter) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	sub, ok := p.validateSubParam(w, r)
	if !ok {
		return
	}

	if r.Method == http.MethodHead {
		p.handleEventsHead(w, r, sub)
		return
	}

	rc := http.NewResponseController(w)
	if _, okFlush := w.(http.Flusher); !okFlush {
		// Without a Flusher we cannot stream; this is a server-side
		// misconfiguration, not a client error.
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "streaming unsupported", nil)
		return
	}

	ch, replay, covered, status := p.hub.Attach(sub, r.Header.Get("Last-Event-ID"))
	switch status {
	case notify.AttachUnknown:
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "subscription not found", map[string]any{"sub": sub})
		return
	case notify.AttachBusy:
		writeJSONError(w, r, p.logger, http.StatusConflict,
			CodeConflict, "subscription already has an active stream", map[string]any{"sub": sub})
		return
	}
	// AttachOK: we own the stream. Detach (arms the retention timer) only on
	// this success path.
	defer p.hub.Detach(sub)

	writeSSEHeaders(w)
	if err := rc.Flush(); err != nil {
		return
	}
	clearWriteDeadline(rc)

	if !covered {
		if err := writeResync(w, rc); err != nil {
			return
		}
	}
	for _, ev := range replay {
		if err := p.writeEvent(w, rc, sub, ev); err != nil {
			return
		}
	}
	p.streamLoop(r.Context(), w, rc, sub, ch)
}

// handleEventsHead answers HEAD /api/events?sub=<id> with the SSE headers
// and an empty body: 200 if the subscription exists, 404 if not. It uses a
// NON-mutating existence check so a HEAD never opens a stream, consumes the
// channel, or arms the reconnect-retention timer (sse-protocol.md
// §Subscription Lifecycle).
func (p *Presenter) handleEventsHead(w http.ResponseWriter, r *http.Request, sub string) {
	if !p.hub.Has(sub) {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "subscription not found", map[string]any{"sub": sub})
		return
	}
	writeSSEHeaders(w)
	// No flush, no Attach, no Detach, no body — the headers and status are
	// the entire response.
}

// validateSubParam extracts and validates the ?sub= query param, writing a
// 400 envelope and returning ok=false on a missing/empty/control-char value.
func (p *Presenter) validateSubParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	sub := r.URL.Query().Get("sub")
	if err := rejectControlChars("sub", sub); err != nil {
		p.writeBadFilter(w, r, err)
		return "", false
	}
	sub = strings.TrimSpace(sub)
	if sub == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "query parameter 'sub' is required", nil)
		return "", false
	}
	return sub, true
}

// streamLoop forwards live events to the client until the client
// disconnects (ctx.Done), the hub closes the channel, or a write/flush
// fails. A keepalive comment is written on the idle ticker so proxies do not
// drop the connection. A write/flush error returns promptly (the client is
// gone) rather than silently spinning (sse-protocol.md §Backpressure;
// no-silent-failure rule).
func (p *Presenter) streamLoop(ctx context.Context, w http.ResponseWriter, rc *http.ResponseController, sub string, ch <-chan notify.Event) {
	keepalive := time.NewTicker(p.sseKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if err := p.writeEvent(w, rc, sub, ev); err != nil {
				p.logger.DebugContext(ctx, "sse write failed", "error", err, "sub", sub)
				return
			}
		case <-keepalive.C:
			if err := writeKeepalive(w, rc); err != nil {
				return
			}
		}
	}
}

// ShutdownSSE is the exported graceful-shutdown entry point the serve binary
// calls: it delivers a disconnect event to every active SSE client so
// browsers reconnect cleanly, then shuts the hub down (closing every
// delivery channel so the per-client stream goroutines unblock and return).
// Idempotent via hub.Shutdown. Per presenter.md §Graceful Shutdown this runs
// before the HTTP server's own Shutdown so in-flight stream handlers drain.
//
// The sseShuttingDown flag is raised FIRST (before broadcastDisconnect or
// hub.Shutdown) so a POST /api/subscriptions racing this window sees it and
// returns 503 rather than minting a subscription the about-to-close hub
// would silently drop (rest-api.md §POST /api/subscriptions).
//
// The flag is flipped under sseLifecycleMu so it is serialized with
// createSubscriptionLifecycle's check-then-create: a create already past the
// check completes its hub.Add + registry insert fully before the flip lands,
// and any create reaching the check after the flip returns 503. The mutex is
// RELEASED before broadcastDisconnect + hub.Shutdown so the (potentially
// longer) hub close does not hold the create lock — only the instantaneous
// state flip is guarded (presenter.md §SSE Lifecycle Mutex).
func (p *Presenter) ShutdownSSE() {
	p.sseLifecycleMu.Lock()
	p.sseShuttingDown = true
	p.sseLifecycleMu.Unlock()
	p.broadcastDisconnect()
	p.hub.Shutdown()
	// hub.Shutdown removed every subscription WITHOUT firing OnRemove (the
	// process is going away), so the registry would otherwise keep reporting
	// subscriptions the closed hub no longer holds. Clear it so the registry
	// and the hub stay consistent (both empty) — this also drops any entry a
	// create completed in the instant before the flag flipped, leaving no
	// orphan. Done OUTSIDE sseLifecycleMu: any in-flight create finished its
	// hub.Add + registry insert before the flag flip (so its entry is among
	// those just cleared), and any later create sees the flag and returns 503
	// without inserting (presenter.md §SSE Lifecycle Mutex / §Graceful
	// Shutdown).
	p.subs.clear()
}

// broadcastDisconnect delivers a disconnect event to every active
// subscription so connected SSE clients receive a clean shutdown signal
// before the hub closes their channels. Called by the serve binary in the
// graceful-shutdown sequence BEFORE hub.Shutdown().
func (p *Presenter) broadcastDisconnect() {
	ev := notify.Event{Kind: "disconnect", TS: p.notifyNow().UnixMicro()}
	for _, id := range p.hub.IDs() {
		p.hub.Deliver(id, ev)
	}
}

// writeSSEHeaders sets the headers every SSE response carries. text/event-
// stream + no-cache keep intermediaries from buffering; X-Accel-Buffering:no
// disables nginx response buffering for the stream.
func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// clearWriteDeadline clears the per-connection write deadline so the long-
// lived stream is not killed by a write timeout. http.Server.WriteTimeout
// is intentionally unset (presenter.md §Middlewares); this is defensive and
// future-proof. An unsupported ResponseWriter (e.g. a test recorder) returns
// http.ErrNotSupported, which is benign and ignored.
func clearWriteDeadline(rc *http.ResponseController) {
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		// A real error other than "unsupported" is non-fatal: the stream
		// can still proceed under the server's (absent) write timeout.
		_ = err
	}
}

// writeEvent writes one SSE event frame (event:/data:/id: + blank-line
// terminator) and flushes. The data payload is single-line JSON built from
// the event kind so a curl observer sees exactly the documented shape. For
// session_changed it includes the subscription's current backpressure drop
// count when non-zero (sse-protocol.md §session_changed/§Backpressure).
// Returns the first write/flush error so the caller tears the stream down
// (no silent failure on a broken client).
func (p *Presenter) writeEvent(w http.ResponseWriter, rc *http.ResponseController, sub string, ev notify.Event) error {
	var dropped uint64
	if ev.Kind == "session_changed" {
		dropped = p.hub.Dropped(sub)
	}
	payload := eventPayload(ev, dropped)
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(ev.Kind)
	b.WriteByte('\n')
	b.WriteString("data: ")
	b.Write(payload)
	b.WriteByte('\n')
	if ev.ID != "" {
		b.WriteString("id: ")
		b.WriteString(ev.ID)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	return rc.Flush()
}

// eventPayload renders the single-line JSON `data:` body for an event by
// kind (sse-protocol.md §Event Types). For session_changed, dropped is
// included only when > 0 (the client missed that many events and should
// re-fetch). Unknown kinds emit just the timestamp so the frame stays
// well-formed.
func eventPayload(ev notify.Event, dropped uint64) []byte {
	var v any
	switch ev.Kind {
	case "session_changed":
		m := map[string]any{"session_id": ev.SessionID, "root_session_id": ev.RootSessionID, "ts": ev.TS}
		if dropped > 0 {
			m["dropped"] = dropped
		}
		v = m
	case "source_status_changed":
		v = map[string]any{"source_id": ev.SourceID, "ts": ev.TS}
	case "disconnect":
		v = map[string]any{"reason": disconnectReason, "retry_after_ms": disconnectRetryMS}
	default: // stats_invalidated and any future kind
		v = map[string]any{"ts": ev.TS}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// writeResync writes the resync event the handler sends when the replay
// buffer cannot prove it covers the client's Last-Event-ID (sse-protocol.md
// §Reconnect Behavior). The client re-fetches its current view from REST.
// Returns the write/flush error so the caller stops streaming on a broken
// client.
func writeResync(w http.ResponseWriter, rc *http.ResponseController) error {
	if _, err := w.Write([]byte("event: resync\ndata: {\"reason\":\"buffer_overflow\"}\n\n")); err != nil {
		return err
	}
	return rc.Flush()
}

// writeKeepalive writes the `: keepalive` comment line (sse-protocol.md
// §keepalive). It is a comment, not an event, so clients ignore it. Returns
// the write/flush error so the stream loop exits on a broken client.
func writeKeepalive(w http.ResponseWriter, rc *http.ResponseController) error {
	if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
		return err
	}
	return rc.Flush()
}
