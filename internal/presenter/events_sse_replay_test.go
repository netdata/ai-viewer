package presenter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// TestEvents_LastEventIDResync asserts that an uncoverable Last-Event-ID
// (one the hub never minted) makes the handler emit a resync event before
// streaming live.
func TestEvents_LastEventIDResync(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	// Last-Event-ID "999999" cannot be covered (empty ring) → resync.
	h := http.Header{"Last-Event-Id": []string{"999999"}}
	sr, cancel, done := startStream(t, p, id, h)
	defer func() { cancel(); <-done }()

	body := waitForBody(t, sr, "event: resync")
	if !strings.Contains(body, "event: resync") {
		t.Fatalf("stream missing resync frame:\n%s", body)
	}
	if !strings.Contains(body, "buffer_overflow") {
		t.Fatalf("resync payload missing reason:\n%s", body)
	}
}

// TestEvents_LastEventIDReplay asserts a covered Last-Event-ID replays the
// buffered events the client missed (no resync).
func TestEvents_LastEventIDReplay(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Deliver two events directly so the hub mints ids 1 (rootA) and 2
	// (childA1) into the replay ring, then reconnect with Last-Event-ID=1.
	// The hub can prove coverage (oldest retained id 1 <= 1), so it replays
	// the missed event (id 2) WITHOUT a resync. (Events delivered before a
	// client attaches also remain queued on the channel — that is expected
	// hub behavior and orthogonal to the replay-vs-resync decision this test
	// pins.)
	p.hub.Deliver(id, notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA", TS: base})
	p.hub.Deliver(id, notify.Event{Kind: "session_changed", SessionID: "childA1", RootSessionID: "rootA", TS: base + 1})

	h := http.Header{"Last-Event-Id": []string{"1"}}
	sr, cancel, done := startStream(t, p, id, h)
	defer func() { cancel(); <-done }()

	body := waitForBody(t, sr, "childA1")
	if strings.Contains(body, "event: resync") {
		t.Fatalf("covered replay must not resync:\n%s", body)
	}
	if !strings.Contains(body, `"session_id":"childA1"`) {
		t.Fatalf("replay missing the missed event (childA1):\n%s", body)
	}
	// The replayed frame for the missed event must carry its id (id: 2).
	if !strings.Contains(body, "id: 2") {
		t.Fatalf("replay missing the missed event id (id: 2):\n%s", body)
	}
}

// TestEvents_GzipSkipped asserts the gzip middleware never compresses
// /api/events even when the client advertises gzip. We assert the absence
// of a Content-Encoding header on the streamed response by driving the
// full middleware chain via httptest.NewServer and a cancellable client.
func TestEvents_GzipSkipped(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events?sub="+id, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ce := resp.Header.Get("Content-Encoding"); ce == "gzip" {
		t.Fatalf("Content-Encoding = gzip; SSE must not be compressed")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Push an event and read at least one frame so the connection is real.
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	buf := make([]byte, 256)
	readDone := make(chan struct{})
	var got string
	go func() {
		n, _ := resp.Body.Read(buf)
		got = string(buf[:n])
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
	}
	cancel()
	if !strings.Contains(got, "event: session_changed") {
		t.Fatalf("stream over real server missing session_changed frame: %q", got)
	}
}
