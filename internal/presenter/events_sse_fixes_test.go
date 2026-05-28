package presenter

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
	"github.com/netdata/ai-viewer/internal/store"
)

// newTestPresenterWithHub is like newTestPresenter but injects a caller-built
// hub so a test can pin a short retention window (to exercise expiry) or a
// tiny channel cap (to exercise backpressure). The presenter wires its
// OnRemove cleanup onto whatever hub it is handed.
func newTestPresenterWithHub(t *testing.T, hub *notify.Hub) (*Presenter, *sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	frontend := fstest.MapFS{
		"frontend_dist/index.html": &fstest.MapFile{Data: []byte("<!doctype html>"), ModTime: fixedTime},
	}
	p, err := New(Options{
		DB:            s.DB(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test-sha",
		DBPath:        "/tmp/test.db",
		StartedAt:     fixedTime.Add(-30 * time.Second),
		SchemaVersion: SchemaVersion,
		Now:           func() time.Time { return fixedTime },
		FrontendFS:    frontend,
		Hub:           hub,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, s.DB(), func() { _ = s.Close() }
}

// statsCoalesceLen reads the size of the poller's statsCoalesce map under
// the lock so a test can assert per-subscription coalesce state is dropped
// when the subscription is removed.
func statsCoalesceLen(p *Presenter) int {
	p.notifyMu.Lock()
	defer p.notifyMu.Unlock()
	return len(p.statsCoalesce)
}

// mustCreateSub creates a subscription and fails the test if id generation
// errors. create returns (id, error) since the Chunk-13 iter-2 RNG-failure
// fix; tests that exercise the happy path use this helper so the real RNG
// (which does not fail) keeps the call sites concise.
func mustCreateSub(t *testing.T, p *Presenter, filter subscriptionFilter) string {
	t.Helper()
	id, err := p.subs.create(filter)
	if err != nil {
		t.Fatalf("subs.create: %v", err)
	}
	return id
}

// TestEvents_ExpiryDropsManagerAndCoalesce pins P1-1/P3-9: when a
// subscription's retention window elapses, the hub's OnRemove hook drops
// the subscription from the hub AND the presenter's subscription manager
// AND the poller's statsCoalesce map — no leak of filter/coalesce state and
// /api/health's count goes back to zero.
func TestEvents_ExpiryDropsManagerAndCoalesce(t *testing.T) {
	t.Parallel()
	hub := notify.New(notify.Options{ChannelCap: 8, ReplayBuffer: 8, Retention: 20 * time.Millisecond})
	p, db, cleanup := newTestPresenterWithHub(t, hub)
	defer cleanup()
	base := seedBase()
	p.notifyNow = func() time.Time { return fixedTime }
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}

	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	// Prime statsCoalesce for this sub by delivering a stats row.
	insertNotify(t, db, "stats_invalidated", "", "", "", base+1)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	if statsCoalesceLen(p) == 0 {
		t.Fatal("statsCoalesce not primed; test cannot prove cleanup")
	}

	// Attach then detach so the retention timer arms.
	ch, _, _, st := hub.Attach(id, "")
	if st != notify.AttachOK {
		t.Fatalf("Attach: %v", st)
	}
	_ = ch
	hub.Detach(id)

	// Within ~2s the retention window (20ms) fires and OnRemove cleans up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !hub.Has(id) && !p.subs.has(id) && statsCoalesceLen(p) == 0 {
			return // fully cleaned up
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("after expiry: hub.Has=%v manager.has=%v statsCoalesce=%d, want all gone/0",
		hub.Has(id), p.subs.has(id), statsCoalesceLen(p))
}

// TestEvents_ExplicitDeleteDropsCoalesce pins that an explicit DELETE also
// runs the OnRemove cleanup so statsCoalesce does not leak (P1-1/P3-9 on the
// explicit-removal path).
func TestEvents_ExplicitDeleteDropsCoalesce(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	p.notifyNow = func() time.Time { return fixedTime }
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	insertNotify(t, db, "stats_invalidated", "", "", "", base+1)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	if statsCoalesceLen(p) != 1 {
		t.Fatalf("statsCoalesce = %d, want 1 after priming", statsCoalesceLen(p))
	}
	p.subs.delete(id)
	if p.hub.Has(id) {
		t.Fatal("hub still knows the deleted sub")
	}
	if p.subs.has(id) {
		t.Fatal("manager still knows the deleted sub")
	}
	if statsCoalesceLen(p) != 0 {
		t.Fatalf("statsCoalesce = %d after delete, want 0 (leak)", statsCoalesceLen(p))
	}
}

// TestEvents_SecondConcurrentGetConflict pins P1-2: while one GET stream is
// live, a second concurrent GET for the same subscription returns 409.
func TestEvents_SecondConcurrentGetConflict(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// First stream: live.
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()
	waitForHeader(t, sr)

	// Second concurrent GET: 409 CONFLICT (single-consumer).
	req := httptest.NewRequest(http.MethodGet, "/api/events?sub="+id, nil)
	rr := httptest.NewRecorder()
	p.handleEvents(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("second concurrent GET status = %d, want 409 (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestEvents_ReconnectAfterDisconnect pins P1-2: after the first GET stream
// disconnects (its retention window keeps the sub alive), a new GET attaches
// successfully (200, streams).
func TestEvents_ReconnectAfterDisconnect(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	sr1, cancel1, done1 := startStream(t, p, id, nil)
	waitForHeader(t, sr1)
	cancel1()
	<-done1 // first stream fully detached

	// New stream attaches fine (sub still in retention window).
	sr2, cancel2, done2 := startStream(t, p, id, nil)
	defer func() { cancel2(); <-done2 }()
	waitForHeader(t, sr2)
	if ct := sr2.headerValue("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("reconnect Content-Type = %q, want text/event-stream", ct)
	}
}

// TestEvents_HeadDoesNotStealEventsOrArmRetention pins P1-2: a HEAD while a
// GET stream is live must NOT consume the GET's channel, must NOT arm the
// retention timer, and must NOT cause a BUSY rejection (it uses a
// non-mutating existence check). The live GET still receives an event
// delivered after the HEAD.
func TestEvents_HeadDoesNotStealEventsOrArmRetention(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Live GET stream.
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()
	waitForHeader(t, sr)

	// A HEAD on the same sub while the GET is live: 200, no stream, and it
	// must not disturb the GET (no BUSY, no retention, no stolen events).
	headReq := httptest.NewRequest(http.MethodHead, "/api/events?sub="+id, nil)
	headRR := httptest.NewRecorder()
	p.handleEvents(headRR, headReq)
	if headRR.Code != http.StatusOK {
		t.Fatalf("HEAD during live GET status = %d, want 200", headRR.Code)
	}
	if headRR.Body.Len() != 0 {
		t.Fatalf("HEAD body should be empty, got %q", headRR.Body.String())
	}

	// The live GET still receives an event delivered AFTER the HEAD (the
	// HEAD did not steal the channel).
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	body := waitForBody(t, sr, "event: session_changed")
	if !strings.Contains(body, `"session_id":"rootA"`) {
		t.Fatalf("live GET did not receive the event after a HEAD:\n%s", body)
	}
}

// TestEvents_HeadUnknownIs404 pins P1-2: HEAD on a non-existent sub returns
// 404 (not 200) via the non-mutating existence check.
func TestEvents_HeadUnknownIs404(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodHead, "/api/events?sub=sub-deadbeefdeadbeefdeadbeefdeadbeef", nil)
	rr := httptest.NewRecorder()
	p.handleEvents(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("HEAD unknown sub status = %d, want 404", rr.Code)
	}
}

// TestEvents_HeadDoesNotArmRetention pins that a bare HEAD (no live GET)
// does NOT arm the retention timer: the subscription must persist
// indefinitely (here, well past a short retention window) because HEAD never
// touches the connect/disconnect lifecycle.
func TestEvents_HeadDoesNotArmRetention(t *testing.T) {
	t.Parallel()
	hub := notify.New(notify.Options{ChannelCap: 4, ReplayBuffer: 4, Retention: 20 * time.Millisecond})
	p, _, cleanup := newTestPresenterWithHub(t, hub)
	defer cleanup()
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	req := httptest.NewRequest(http.MethodHead, "/api/events?sub="+id, nil)
	rr := httptest.NewRecorder()
	p.handleEvents(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	// If HEAD had armed retention, the sub would vanish after 20ms. Wait
	// well past that and assert it is still present.
	time.Sleep(80 * time.Millisecond)
	if !hub.Has(id) {
		t.Fatal("HEAD armed the retention timer; subscription was dropped")
	}
}

// TestEvents_DroppedSurfacedInSessionChanged pins P2-6: when the
// per-subscription drop counter is non-zero, the session_changed frame
// carries "dropped": <n>.
func TestEvents_DroppedSurfacedInSessionChanged(t *testing.T) {
	t.Parallel()
	// Tiny channel cap so we can force a drop before the stream drains.
	hub := notify.New(notify.Options{ChannelCap: 1, ReplayBuffer: 16, Retention: time.Second})
	p, db, cleanup := newTestPresenterWithHub(t, hub)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Deliver several session_changed events directly (more than cap) WITHOUT
	// a consumer so drop-oldest increments the dropped counter.
	for i := 0; i < 4; i++ {
		hub.Deliver(id, notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA", TS: base + int64(i)})
	}
	if hub.Dropped(id) == 0 {
		t.Fatal("precondition: expected a non-zero dropped counter")
	}

	// Now connect: the surviving buffered session_changed frame must carry
	// the dropped count.
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()
	body := waitForBody(t, sr, "event: session_changed")
	if !strings.Contains(body, `"dropped":`) {
		t.Fatalf("session_changed frame missing dropped counter:\n%s", body)
	}
}

// TestEvents_FutureLastEventIDResync pins P2-5 end-to-end at the handler:
// a Last-Event-ID ahead of the newest delivered id (a stale/forged value)
// drives a resync frame rather than a false "covered" with no replay.
func TestEvents_FutureLastEventIDResync(t *testing.T) {
	t.Parallel()
	hub := notify.New(notify.Options{ChannelCap: 16, ReplayBuffer: 16, Retention: time.Second})
	p, db, cleanup := newTestPresenterWithHub(t, hub)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Deliver one event so the newest retained id is 1, then "disconnect"
	// the (never-attached) accounting by reconnecting with a far-future id.
	hub.Deliver(id, notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA", TS: base})

	h := http.Header{"Last-Event-Id": []string{"100000"}}
	sr, cancel, done := startStream(t, p, id, h)
	defer func() { cancel(); <-done }()

	body := waitForBody(t, sr, "event: resync")
	if !strings.Contains(body, "event: resync") {
		t.Fatalf("future Last-Event-ID did not trigger a resync:\n%s", body)
	}
}
