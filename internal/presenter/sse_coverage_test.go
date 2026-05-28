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

// TestSubscriptionFilter_UnknownKind asserts an unrecognized event kind
// never matches (the default branch of matches).
func TestSubscriptionFilter_UnknownKind(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = p
	f, _ := parseSubscriptionFilter([]byte(`{"filter":{}}`))
	got, err := f.matches(context.Background(), db, notify.Event{Kind: "mystery"})
	if err != nil {
		t.Fatalf("matches unknown kind: %v", err)
	}
	if got {
		t.Fatal("unknown event kind must never match")
	}
}

// TestSubscriptionFilter_MatchesSessionDBError asserts a query error from a
// closed DB surfaces as an error (not a silent false) from matchesSession.
func TestSubscriptionFilter_MatchesSessionDBError(t *testing.T) {
	t.Parallel()
	p, _ := newClosedDBPresenter(t)
	f, _ := parseSubscriptionFilter([]byte(`{"filter":{}}`))
	_, err := f.matches(context.Background(), p.db,
		notify.Event{Kind: "session_changed", SessionID: "rootA"})
	if err == nil {
		t.Fatal("matchesSession on a closed DB must return an error, not silent false")
	}
}

// TestSubscriptionsCreate_BodyTooLarge asserts a body exceeding the 1 MB
// cap (enforced by bodyLimitMiddleware) is rejected with 400 rather than a
// 500 — exercising the io.ReadAll error branch.
func TestSubscriptionsCreate_BodyTooLarge(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	// 1 MB + slack of valid-ish JSON padding inside an unknown field; the
	// MaxBytesReader trips before the decoder finishes.
	big := `{"filter":{},"pad":"` + strings.Repeat("a", (1<<20)+16) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", strings.NewReader(big))
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: status = %d, want 400 (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestSubscriptionsDelete_WhitespaceID asserts a whitespace-only id (which
// the {id} wildcard captures) trims to empty and returns 400.
func TestSubscriptionsDelete_WhitespaceID(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodDelete, "/api/subscriptions/%20", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("whitespace id: status = %d, want 400 (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestParseSubscriptionFilter_EmptyScalar asserts a present-but-empty
// session_id / root_session_id is a 400 (a client bug that would otherwise
// match everything), exercising normalizeScalar's empty-string branch.
func TestParseSubscriptionFilter_EmptyScalar(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"filter":{"session_id":""}}`,
		`{"filter":{"root_session_id":""}}`,
	} {
		if _, err := parseSubscriptionFilter([]byte(body)); err == nil {
			t.Fatalf("parseSubscriptionFilter(%s): want error for empty scalar", body)
		}
	}
}

// TestEvents_HeadReturnsHeadersNoStream asserts a HEAD on /api/events
// returns 200 with the SSE headers and no streamed body (RFC 9110 §9.3.2),
// and does not block.
func TestEvents_HeadReturnsHeadersNoStream(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	req := httptest.NewRequest(http.MethodHead, "/api/events?sub="+id, nil)
	rr := httptest.NewRecorder()
	p.handleEvents(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD /api/events: status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("HEAD Content-Type = %q, want text/event-stream", ct)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body should be empty, got %q", rr.Body.String())
	}
}

// TestEvents_SourceStatusFrame asserts a source_status_changed notify row
// is streamed as a source_status_changed frame carrying source_id (covers
// the eventPayload source branch end-to-end).
func TestEvents_SourceStatusFrame(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()
	waitForHeader(t, sr)

	insertNotify(t, db, "source_status_changed", "", "", "src1", base+20)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	body := waitForBody(t, sr, "event: source_status_changed")
	if !strings.Contains(body, "event: source_status_changed") {
		t.Fatalf("stream missing source_status_changed frame:\n%s", body)
	}
	if !strings.Contains(body, `"source_id":"src1"`) {
		t.Fatalf("source frame missing source_id:\n%s", body)
	}
}

// TestNotifyPoller_QueryErrorSurfaces asserts pollNotifyOnce returns the
// query error (not a silent nil) when the DB is closed, so runNotifyPoller
// logs it rather than swallowing it.
func TestNotifyPoller_QueryErrorSurfaces(t *testing.T) {
	t.Parallel()
	p, _ := newClosedDBPresenter(t)
	if err := p.pollNotifyOnce(context.Background()); err == nil {
		t.Fatal("pollNotifyOnce on a closed DB must return an error")
	}
}

// TestNotifyPoller_FanOutSkipsMatchError asserts a per-subscription match
// error does not abort the whole poll: pollNotifyOnce still returns nil
// (the error is logged and that subscription skipped — no silent failure,
// no poll-wide abort) and the cursor still advances. The error is forced by
// dropping the sessions table the session_changed match queries.
func TestNotifyPoller_FanOutSkipsMatchError(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	_, _, _, _ = p.hub.Attach(id, "")

	// Drop the sessions table so matchesSession's SELECT errors.
	if _, err := db.Exec(`DROP TABLE sessions`); err != nil {
		t.Fatalf("drop sessions: %v", err)
	}
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+30)

	// The match errors, the sub is skipped, but the poll succeeds and the
	// cursor advances past the row.
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce should tolerate a per-sub match error: %v", err)
	}
	if seq, _ := p.notifyHealth(); seq == 0 {
		t.Fatal("cursor did not advance past the errored row")
	}
}

// TestNotifyPoller_FanOutManySubs asserts fanOut tolerates a
// stats_invalidated row delivered to many subscriptions without error.
func TestNotifyPoller_FanOutManySubs(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	p.notifyNow = func() time.Time { return fixedTime }
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	var chans []<-chan notify.Event
	for i := 0; i < 5; i++ {
		id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
		ch, _, _, _ := p.hub.Attach(id, "")
		chans = append(chans, ch)
	}
	insertNotify(t, db, "stats_invalidated", "", "", "", base+1)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	for i, ch := range chans {
		ev := drainOne(t, ch)
		if ev.Kind != "stats_invalidated" {
			t.Fatalf("sub %d got %+v, want stats_invalidated", i, ev)
		}
	}
}
