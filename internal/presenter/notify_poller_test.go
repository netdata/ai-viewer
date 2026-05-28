package presenter

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// insertNotify writes one notify row directly (the poller reads it back).
// In production the ingester is the sole writer; tests use a writer handle
// to seed the change-log table the read-only poller polls.
func insertNotify(t *testing.T, db *sql.DB, kind, sessionID, rootID, sourceID string, tsUS int64) {
	t.Helper()
	var sid, rid, src any
	if sessionID != "" {
		sid = sessionID
	}
	if rootID != "" {
		rid = rootID
	}
	if sourceID != "" {
		src = sourceID
	}
	if _, err := db.Exec(
		`INSERT INTO notify (ts_us, kind, session_id, root_session_id, source_id) VALUES (?, ?, ?, ?, ?)`,
		tsUS, kind, sid, rid, src,
	); err != nil {
		t.Fatalf("insert notify: %v", err)
	}
}

// drainOne reads one event from a channel within a short deadline, failing
// the test on timeout so a missing delivery is a hard failure not a hang.
func drainOne(t *testing.T, ch <-chan notify.Event) notify.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before an event arrived")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
		return notify.Event{}
	}
}

// assertNoEvent asserts nothing is delivered within a short window.
func assertNoEvent(t *testing.T, ch <-chan notify.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event delivered: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestNotifyPoller_CursorStartsAtMax asserts initNotifyCursor sets the
// cursor to MAX(seq) so a row that existed BEFORE serve started is never
// delivered — clients reconcile history via REST.
func TestNotifyPoller_CursorStartsAtMax(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()

	// A pre-existing notify row for rootA.
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base)

	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}

	// Subscribe to everything and attach.
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	ch, _, _, st := p.hub.Attach(id, "")
	if st != notify.AttachOK {
		t.Fatalf("Attach: status=%v, want AttachOK", st)
	}

	// Poll once: the pre-existing row is below the cursor, nothing delivered.
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	assertNoEvent(t, ch)

	// A NEW row after boot is delivered.
	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	ev := drainOne(t, ch)
	if ev.Kind != "session_changed" || ev.SessionID != "rootA" {
		t.Fatalf("event = %+v, want session_changed rootA", ev)
	}
}

// TestNotifyPoller_DeliversToMatchingOnly asserts a new row is delivered
// only to subscriptions whose filter matches the changed session.
func TestNotifyPoller_DeliversToMatchingOnly(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}

	// matchSub admits all; missSub only admits status=failed (rootA is
	// completed, so it must NOT receive rootA's change).
	matchSub := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	missFilter, _ := parseSubscriptionFilter([]byte(`{"filter":{"status":["failed"]}}`))
	missSub := mustCreateSub(t, p, missFilter)

	matchCh, _, _, _ := p.hub.Attach(matchSub, "")
	missCh, _, _, _ := p.hub.Attach(missSub, "")

	insertNotify(t, db, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	ev := drainOne(t, matchCh)
	if ev.SessionID != "rootA" {
		t.Fatalf("matchSub got %+v, want rootA", ev)
	}
	assertNoEvent(t, missCh)
}

// TestNotifyPoller_StatsCoalesced asserts repeated stats_invalidated rows
// inside one coalesce window deliver at most one stats event per
// subscription.
func TestNotifyPoller_StatsCoalesced(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	// Pin the poller clock so the coalesce window is deterministic.
	now := fixedTime
	p.notifyNow = func() time.Time { return now }
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	ch, _, _, _ := p.hub.Attach(id, "")

	// Three stats rows, all within the same (pinned) second.
	insertNotify(t, db, "stats_invalidated", "", "", "", base+1)
	insertNotify(t, db, "stats_invalidated", "", "", "", base+2)
	insertNotify(t, db, "stats_invalidated", "", "", "", base+3)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	ev := drainOne(t, ch)
	if ev.Kind != "stats_invalidated" {
		t.Fatalf("event = %+v, want stats_invalidated", ev)
	}
	// No second stats event in the same window.
	assertNoEvent(t, ch)

	// Advance the clock past the coalesce window: a new stats row is
	// delivered again.
	now = now.Add(2 * time.Second)
	insertNotify(t, db, "stats_invalidated", "", "", "", base+4)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	ev2 := drainOne(t, ch)
	if ev2.Kind != "stats_invalidated" {
		t.Fatalf("event2 = %+v, want stats_invalidated", ev2)
	}
}

// TestNotifyPoller_StopsOnCancel asserts runNotifyPoller returns promptly
// when its context is cancelled (no goroutine leak; asserted under -race
// across the suite).
func TestNotifyPoller_StopsOnCancel(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.runNotifyPoller(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runNotifyPoller did not return after context cancel")
	}
}

// TestNotifyPoller_HealthState asserts the poller records the last-applied
// seq and ts so /api/health can surface them.
func TestNotifyPoller_HealthState(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	seq0, ts0 := p.notifyHealth()
	if seq0 != 0 || ts0 != 0 {
		t.Fatalf("initial notify health = (%d,%d), want (0,0)", seq0, ts0)
	}
	insertNotify(t, db, "stats_invalidated", "", "", "", base+7)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}
	seq1, ts1 := p.notifyHealth()
	if seq1 == 0 {
		t.Fatalf("last_seq = %d, want > 0 after a row", seq1)
	}
	if ts1 != base+7 {
		t.Fatalf("last ts = %d, want %d", ts1, base+7)
	}
}
