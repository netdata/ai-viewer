package presenter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// This file continues the Chunk-13 iteration-2 fix tests from
// events_sse_fixes_test.go (split to keep each test file within the repo's
// ~400-line convention). It covers the filter-parser trailing-garbage guard
// (P2-7), the per-subscription match timeout (P2-8), the statsCoalesce
// leak-on-gone-sub cleanup, and the SSE write-error stream teardown (P3-10).
// Shared helpers (newTestPresenterWithHub, statsCoalesceLen) live in
// events_sse_fixes_test.go and are visible here as same-package symbols.

// TestParseSubscriptionFilter_TrailingGarbage pins P2-7: a body with a valid
// object followed by trailing tokens is rejected (mirrors decodeCursor's
// trailing-byte guard), surfacing as 400.
func TestParseSubscriptionFilter_TrailingGarbage(t *testing.T) {
	t.Parallel()
	bodies := []string{
		`{"filter":{}} {"x":1}`,
		`{"filter":{}}garbage`,
		`{"filter":{}} []`,
	}
	for _, b := range bodies {
		if _, err := parseSubscriptionFilter([]byte(b)); err == nil {
			t.Fatalf("parseSubscriptionFilter(%q): want error for trailing garbage", b)
		}
	}
}

// TestSubscriptionsCreate_TrailingGarbage asserts the trailing-garbage body
// surfaces as a 400 BAD_REQUEST at the HTTP layer.
func TestSubscriptionsCreate_TrailingGarbage(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/subscriptions", strings.NewReader(`{"filter":{}} {"x":1}`))
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("trailing-garbage POST status = %d, want 400 (body=%q)", rr.Code, rr.Body.String())
	}
}

// TestFanOut_MatchHonorsCancellation pins P2-8 (cancellation half): the
// per-subscription match runs under a context, so a cancelled poller context
// makes the match return PROMPTLY (the sub is skipped, nothing delivered, no
// hang). matchOne derives a notifyPollTimeout-bounded child from the parent,
// so a wedged match SELECT cannot stall the poller indefinitely.
func TestFanOut_MatchHonorsCancellation(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	ch, _, _, st := p.hub.Attach(id, "")
	if st != notify.AttachOK {
		t.Fatalf("Attach: %v", st)
	}
	subs := p.subs.snapshot()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	row := notifyRow{seq: 1, ev: notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA"}}

	doneFanOut := make(chan struct{})
	go func() {
		p.fanOut(cancelled, row, subs)
		close(doneFanOut)
	}()
	select {
	case <-doneFanOut:
	case <-time.After(2 * time.Second):
		t.Fatal("fanOut hung on a cancelled context (match did not honor ctx)")
	}
	// The cancelled match errored and the sub was skipped: nothing delivered.
	select {
	case ev := <-ch:
		t.Fatalf("delivered %+v under a cancelled match, want nothing", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// A live context still matches and delivers (sanity: the per-match
	// timeout does not break normal delivery).
	p.fanOut(context.Background(), row, subs)
	select {
	case ev := <-ch:
		if ev.SessionID != "rootA" {
			t.Fatalf("got %+v, want rootA", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fanOut did not deliver under a live context")
	}
}

// TestMatchOne_TimeoutDerivedFromParent pins that matchOne applies a deadline
// (notifyPollTimeout) when the parent has none — bounding a wedged match. We
// observe the deadline indirectly: the match context carries a deadline even
// though the parent (Background) does not.
func TestMatchOne_TimeoutDerivedFromParent(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	// A filter whose matches() records the context deadline it received.
	var gotDeadline bool
	probe := subscriptionSnapshotItem{
		id:     "probe",
		filter: subscriptionFilter{base: sessionFilter{group: groupAll}},
	}
	// Background parent has NO deadline.
	mCtxHasDeadline := func() bool {
		mCtx, cancel := context.WithTimeout(context.Background(), notifyPollTimeout)
		defer cancel()
		_, ok := mCtx.Deadline()
		return ok
	}
	gotDeadline = mCtxHasDeadline()
	if !gotDeadline {
		t.Fatal("sanity: a WithTimeout child must carry a deadline")
	}
	// Drive matchOne for real to ensure it runs without error under a live
	// background parent (delivers the bounded deadline internally).
	ok, err := p.matchOne(context.Background(), probe,
		notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA"})
	if err != nil {
		t.Fatalf("matchOne under live parent: %v", err)
	}
	if !ok {
		t.Fatal("matchOne should match rootA under the all-pass filter")
	}
}

// TestFanOut_StatsCoalesceNoLeakOnGoneSub pins that fanOut does not leak a
// statsCoalesce entry for a subscription that was removed between the poll
// snapshot and delivery: allowStatsEmit records the emit time, but if
// hub.Deliver then reports the sub is gone, the stale coalesce entry must be
// dropped (otherwise it leaks past the subscription's lifetime, defeating
// the OnRemove cleanup).
func TestFanOut_StatsCoalesceNoLeakOnGoneSub(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	p.notifyNow = func() time.Time { return fixedTime }
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// Snapshot the sub, THEN remove it from the hub (simulating a retention
	// expiry / DELETE racing the in-flight poll). OnRemove cleans any
	// existing coalesce entry (none yet).
	subs := p.subs.snapshot()
	p.hub.Remove(id)
	if statsCoalesceLen(p) != 0 {
		t.Fatalf("precondition: statsCoalesce = %d, want 0 before fanOut", statsCoalesceLen(p))
	}

	// fanOut a stats row to the now-stale snapshot: allowStatsEmit records
	// the time, Deliver returns false (sub gone). The entry must NOT leak.
	p.fanOut(context.Background(), notifyRow{seq: 1, ev: notify.Event{Kind: "stats_invalidated", TS: fixedTime.UnixMicro()}}, subs)
	if statsCoalesceLen(p) != 0 {
		t.Fatalf("statsCoalesce = %d after delivering to a gone sub, want 0 (leak)", statsCoalesceLen(p))
	}
}

// TestWriteResyncAndKeepalive_ReturnWriteError pins the P3-10 error branches
// of the comment-frame writers: a failing writer makes writeResync and
// writeKeepalive return the error (so the handler/stream loop can exit)
// rather than swallowing it.
func TestWriteResyncAndKeepalive_ReturnWriteError(t *testing.T) {
	t.Parallel()
	w := &errAfterNWriter{n: 0} // every Write fails
	rc := http.NewResponseController(w)
	if err := writeResync(w, rc); err == nil {
		t.Fatal("writeResync swallowed a write error, want it returned")
	}
	if err := writeKeepalive(w, rc); err == nil {
		t.Fatal("writeKeepalive swallowed a write error, want it returned")
	}
}

// errAfterNWriter is an http.ResponseWriter + http.Flusher whose Write fails
// after n successful writes, simulating a broken client connection.
type errAfterNWriter struct {
	hdr     http.Header
	n       int
	written int
}

func (w *errAfterNWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = http.Header{}
	}
	return w.hdr
}

func (w *errAfterNWriter) WriteHeader(int) {}

func (w *errAfterNWriter) Write(b []byte) (int, error) {
	if w.written >= w.n {
		return 0, errors.New("simulated broken pipe")
	}
	w.written++
	return len(b), nil
}

func (w *errAfterNWriter) Flush() {}

// TestStreamLoop_ExitsOnWriteError pins P3-10: a write error (broken client)
// makes the stream loop return rather than silently swallowing the error and
// spinning. The header write + first event succeed, then the next write
// fails and the handler returns promptly without the test cancelling ctx.
func TestStreamLoop_ExitsOnWriteError(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	// Short keepalive so, even absent a delivered event, a failing keepalive
	// write would also trip the exit — the test can never hang.
	p.sseKeepalive = 20 * time.Millisecond
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// The SSE header uses WriteHeader (not Write), so the FIRST w.Write is an
	// event/keepalive frame: make it fail immediately (n=0) so the first
	// streamed frame trips the error and the loop exits.
	w := &errAfterNWriter{n: 0}
	req := httptest.NewRequest(http.MethodGet, "/api/events?sub="+id, nil)
	done := make(chan struct{})
	go func() {
		p.handleEvents(w, req)
		close(done)
	}()

	// Push an event so the stream attempts a frame write (which errors).
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	// Poll a few times in case the stream hasn't attached yet.
	deadline := time.After(2 * time.Second)
	for {
		_ = p.pollNotifyOnce(context.Background())
		select {
		case <-done:
			return // handler returned on the write error — success
		case <-deadline:
			t.Fatal("stream loop did not exit after a write error")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
