package presenter

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncRecorder is a minimal http.ResponseWriter + http.Flusher whose body,
// status, and header SNAPSHOT are mutex-guarded so a test goroutine can
// observe the SSE handler's output while the handler writes, without a data
// race. The handler mutates a private header map (returned by Header())
// single-threaded before WriteHeader; WriteHeader copies that map into the
// guarded snapshot the test reads via headerValue(). The live header map is
// therefore never read by the test goroutine.
type syncRecorder struct {
	mu       sync.Mutex
	hdr      http.Header
	snapshot http.Header
	buf      strings.Builder
	status   int
	wrote    bool
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: http.Header{}, status: http.StatusOK}
}

// Header returns the live header map the handler mutates before WriteHeader.
// Only the handler goroutine touches it; the test goroutine reads the guarded
// snapshot instead.
func (s *syncRecorder) Header() http.Header { return s.hdr }

func (s *syncRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wrote {
		s.snapshotLocked()
	}
	return s.buf.Write(b)
}

func (s *syncRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wrote {
		return
	}
	s.status = code
	s.snapshotLocked()
}

// snapshotLocked copies the live header map into the guarded snapshot. Must
// be called under s.mu.
func (s *syncRecorder) snapshotLocked() {
	s.wrote = true
	cp := make(http.Header, len(s.hdr))
	for k, vs := range s.hdr {
		vv := make([]string, len(vs))
		copy(vv, vs)
		cp[k] = vv
	}
	s.snapshot = cp
}

func (s *syncRecorder) Flush() {}

func (s *syncRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// headerValue returns the snapshotted header value (set when the handler
// wrote the response header), or "" before headers are written.
func (s *syncRecorder) headerValue(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return ""
	}
	return s.snapshot.Get(key)
}

// headerWritten reports whether the handler has written the response header.
func (s *syncRecorder) headerWritten() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

// waitForHeader polls until the handler has written the response header
// (status code set), so header assertions do not race the handler goroutine.
func waitForHeader(t *testing.T, sr *syncRecorder) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if sr.headerWritten() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for SSE response headers")
		case <-tick.C:
		}
	}
}

// waitForBody polls the recorder until want appears or the deadline
// elapses. Returns the final body for assertion messages.
func waitForBody(t *testing.T, sr *syncRecorder, want string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if b := sr.body(); strings.Contains(b, want) {
			return b
		}
		select {
		case <-deadline:
			return sr.body()
		case <-tick.C:
		}
	}
}

// startStream launches the GET /api/events handler in a goroutine against
// a cancellable context, returning the recorder, a cancel func, and a
// done channel closed when the handler returns.
func startStream(t *testing.T, p *Presenter, sub string, header http.Header) (*syncRecorder, context.CancelFunc, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events?sub="+sub, nil).WithContext(ctx)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	sr := newSyncRecorder()
	done := make(chan struct{})
	go func() {
		p.handleEvents(sr, req)
		close(done)
	}()
	return sr, cancel, done
}

// TestEvents_StreamsSessionChanged drives the full path: create a sub,
// open the stream, write a matching notify row, poll once, and assert the
// client receives a session_changed frame with event:/data:/id:.
func TestEvents_StreamsSessionChanged(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}

	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	sr, cancel, done := startStream(t, p, id, nil)

	// Header flush should land the SSE content-type before any event.
	waitForHeader(t, sr)
	if ct := sr.headerValue("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := sr.headerValue("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if xab := sr.headerValue("X-Accel-Buffering"); xab != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", xab)
	}

	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}

	body := waitForBody(t, sr, "event: session_changed")
	if !strings.Contains(body, "event: session_changed") {
		t.Fatalf("stream missing session_changed frame:\n%s", body)
	}
	if !strings.Contains(body, `"session_id":"rootA"`) {
		t.Fatalf("stream missing session_id payload:\n%s", body)
	}
	if !strings.Contains(body, "id: ") {
		t.Fatalf("stream missing id: field:\n%s", body)
	}
	// Each frame ends with a blank line (SSE terminator).
	if !sseFrameWellFormed(body, "session_changed") {
		t.Fatalf("session_changed frame is not well-formed:\n%s", body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client cancel")
	}
	// After the handler returns it must Detach; the subscription enters the
	// retention window but is still Has()-known until it expires.
	if !p.hub.Has(id) {
		t.Fatalf("subscription should survive in retention window after detach")
	}
}

// sseFrameWellFormed checks that the named event frame is followed by a
// data: line and a blank-line terminator.
func sseFrameWellFormed(body, eventType string) bool {
	sc := bufio.NewScanner(strings.NewReader(body))
	state := 0 // 0=looking for event, 1=saw event, 2=saw data
	for sc.Scan() {
		line := sc.Text()
		switch state {
		case 0:
			if line == "event: "+eventType {
				state = 1
			}
		case 1:
			if strings.HasPrefix(line, "data: ") {
				state = 2
			}
		case 2:
			// id: line is optional position; the terminator is a blank line.
			if line == "" {
				return true
			}
		}
	}
	return false
}

// TestEvents_Keepalive asserts a `: keepalive` comment is emitted on the
// idle ticker. The handler's keepalive interval is injected short for the
// test.
func TestEvents_Keepalive(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	p.sseKeepalive = 20 * time.Millisecond
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()

	body := waitForBody(t, sr, ": keepalive")
	if !strings.Contains(body, ": keepalive") {
		t.Fatalf("stream missing keepalive comment:\n%s", body)
	}
}

// TestEvents_UnknownSub asserts an unknown/expired sub returns 404.
func TestEvents_UnknownSub(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/events?sub=sub-deadbeefdeadbeefdeadbeefdeadbeef", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestEvents_BadSub asserts a missing or malformed sub returns 400.
func TestEvents_BadSub(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	cases := []string{
		"/api/events",           // missing
		"/api/events?sub=",      // empty
		"/api/events?sub=a%01b", // control char
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("GET %s: status = %d, want 400 (body=%q)", path, rr.Code, rr.Body.String())
		}
	}
}

// TestEvents_MethodGate asserts non-GET (except HEAD) is 405.
func TestEvents_MethodGate(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/events?sub=sub-deadbeefdeadbeefdeadbeefdeadbeef", nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/events: status = %d, want 405", m, rr.Code)
		}
	}
}
