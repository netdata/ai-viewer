package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// newFilePresenter builds a presenter backed by a real on-disk SQLite
// file: a writer store seeds rows (standing in for the ingester) and a
// reader store (mode=ro, like production serve) backs the presenter +
// poller. Returns the presenter, the writer DB (to seed), and a cleanup.
func newFilePresenter(t *testing.T) (*Presenter, *sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.db")
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	ws, err := store.OpenWriter(ctx, dbPath, discard)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	rs, err := store.OpenReader(ctx, dbPath, discard)
	if err != nil {
		_ = ws.Close()
		t.Fatalf("open reader: %v", err)
	}
	frontend := fstest.MapFS{
		"frontend_dist/index.html": &fstest.MapFile{Data: []byte("<!doctype html>"), ModTime: fixedTime},
	}
	p, err := New(Options{
		DB:            rs.DB(),
		Logger:        discard,
		Version:       "test-sha",
		DBPath:        dbPath,
		StartedAt:     fixedTime.Add(-30 * time.Second),
		SchemaVersion: SchemaVersion,
		Now:           func() time.Time { return fixedTime },
		FrontendFS:    frontend,
	})
	if err != nil {
		_ = ws.Close()
		_ = rs.Close()
		t.Fatalf("New: %v", err)
	}
	cleanup := func() {
		_ = rs.Close()
		_ = ws.Close()
	}
	return p, ws.DB(), cleanup
}

// TestSSE_EndToEnd ingests (seeds) a session + a notify row through a
// writer store, then drives the read-only poller → hub → SSE handler and
// asserts the client receives the session_changed event with the right
// session_id. On shutdown the client receives a disconnect event.
func TestSSE_EndToEnd(t *testing.T) {
	t.Parallel()
	p, wdb, cleanup := newFilePresenter(t)
	defer cleanup()
	base := seedBase()

	// Seed the canonical graph through the writer store (the ingester's
	// role) so the poller's whereClause lookup can resolve the session.
	seedGraph(t, wdb, base)

	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}
	id := mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})
	sr, cancel, done := startStream(t, p, id, nil)
	defer func() { cancel(); <-done }()

	// A new notify row (as the ingester would write atomically with a batch).
	insertNotify(t, wdb, "session_changed", "rootA", "rootA", "", base+10)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}

	body := waitForBody(t, sr, `"session_id":"rootA"`)
	if !strings.Contains(body, "event: session_changed") {
		t.Fatalf("end-to-end stream missing session_changed:\n%s", body)
	}

	// Graceful shutdown path: broadcast disconnect, then the client sees it.
	p.broadcastDisconnect()
	discBody := waitForBody(t, sr, "event: disconnect")
	if !strings.Contains(discBody, "server_shutdown") {
		t.Fatalf("shutdown stream missing disconnect/server_shutdown:\n%s", discBody)
	}
}

// TestHealth_NotifyAndSSEFields asserts /api/health surfaces the notify
// poller's last_seq + lag_us and the active subscription count.
func TestHealth_NotifyAndSSEFields(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	if err := p.initNotifyCursor(context.Background()); err != nil {
		t.Fatalf("initNotifyCursor: %v", err)
	}

	// One active subscription.
	_ = mustCreateSub(t, p, subscriptionFilter{base: sessionFilter{group: groupAll}})

	// A notify row applied by the poller sets last_seq + ts.
	insertNotify(t, db, "stats_invalidated", "", "", "", base+5)
	if err := p.pollNotifyOnce(context.Background()); err != nil {
		t.Fatalf("pollNotifyOnce: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var body struct {
		Notify struct {
			LastSeq int64 `json:"last_seq"`
			LagUS   int64 `json:"lag_us"`
		} `json:"notify"`
		SSE struct {
			Subscriptions int `json:"subscriptions"`
		} `json:"sse"`
	}
	decodeJSON(t, rr.Body, &body)
	if body.Notify.LastSeq == 0 {
		t.Fatalf("notify.last_seq = 0, want > 0 after a row")
	}
	// lag_us = fixedTime - (base+5); base is one hour before fixedTime.
	wantLag := fixedTime.UnixMicro() - (base + 5)
	if body.Notify.LagUS != wantLag {
		t.Fatalf("notify.lag_us = %d, want %d", body.Notify.LagUS, wantLag)
	}
	if body.SSE.Subscriptions != 1 {
		t.Fatalf("sse.subscriptions = %d, want 1", body.SSE.Subscriptions)
	}
}

// decodeJSON is a tiny helper so the health test reads cleanly.
func decodeJSON(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode json: %v", err)
	}
}
