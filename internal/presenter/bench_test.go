package presenter

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// BenchmarkSessionsListQuery measures GET /api/sessions end-to-end
// through the handler (handleSessionsList, sessions_list.go:44): filter
// parse → keyset query against SQLite → row scan → JSON marshal →
// response write. This is the read-path the UI hits on every session
// list load and refresh, so its latency is the user-perceived cost of
// the busiest screen.
//
// The DB is seeded ONCE with `seedCount` root sessions before the timer
// starts; each measured iteration issues one default GET /api/sessions
// (group=root, DESC, default limit) and reads the full response body,
// so the measurement includes the SQL query, the limit+1 row scan, the
// child-count correlated subquery per row, and the JSON encode — the
// complete server-side request cost. The handler is exercised via its
// real ServeHTTP entrypoint (not querySessions directly) so middleware
// and JSON marshalling are charged, matching production.
//
// Reported metrics:
//
//   - requests/sec  — completed list requests per second.
//   - rows          — sessions seeded (the query's working set).
func BenchmarkSessionsListQuery(b *testing.B) {
	const seedCount = 100
	p, db, cleanup := newBenchPresenter(b)
	defer cleanup()
	seedSessionsForBench(b, db, seedCount)

	handler := p.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatalf("status = %d, want 200 (body=%q)", rr.Code, rr.Body.String())
		}
		// Read the body so the response write + buffering is charged to
		// the measured request, mirroring a real client draining it.
		_, _ = io.Copy(io.Discard, rr.Body)
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds()
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(b.N)/wallSec, "requests/sec")
	b.ReportMetric(float64(seedCount), "rows")
}

// newBenchPresenter builds a Presenter against a fresh in-memory store,
// mirroring newTestPresenter but taking *testing.B. The returned cleanup
// closes the store.
func newBenchPresenter(b *testing.B) (*Presenter, *sql.DB, func()) {
	b.Helper()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("open writer store: %v", err)
	}
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	frontend := fstest.MapFS{
		"frontend_dist/index.html": &fstest.MapFile{
			Data:    []byte("<!doctype html><title>bench</title>"),
			ModTime: now,
		},
	}
	p, err := New(Options{
		DB:            s.DB(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "bench-sha",
		DBPath:        "/tmp/bench.db",
		StartedAt:     now.Add(-30 * time.Second),
		SchemaVersion: SchemaVersion,
		Now:           func() time.Time { return now },
		FrontendFS:    frontend,
	})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	return p, s.DB(), func() { _ = s.Close() }
}

// seedSessionsForBench inserts n root sessions whose start_ts all precede
// the presenter's pinned "now" (so the default time filter includes
// them). Each row carries realistic non-zero token/cost/count columns so
// the scan and JSON marshal handle the same shape production does.
func seedSessionsForBench(b *testing.B, db *sql.DB, n int) {
	b.Helper()
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	base := now.Add(-time.Hour).UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, parse_errors, created_at)
		 VALUES (?, ?, ?, 1, ?, 0, ?)`,
		"benchsrc", "aiagent_v3", "/tmp/bench", base, base,
	); err != nil {
		b.Fatalf("seed source: %v", err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-root-%04d", i)
		startTS := base + int64(i)*1_000
		endTS := startTS + 5_000
		if _, err := db.Exec(`
INSERT INTO sessions (
    id, source_id, native_id, root_session_id, kind,
    agent_name, model, provider, status, start_ts, end_ts, last_activity_ts,
    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write,
    cost_usd, turn_count, op_count, failure_count
) VALUES (?, ?, ?, ?, 'root', 'claude', 'claude-opus-4-7', 'anthropic', 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "benchsrc", id, id, startTS, endTS, startTS,
			int64(1000+i), int64(2000+i), int64(3000+i), int64(500+i),
			0.30, int64(2), int64(8), int64(1),
		); err != nil {
			b.Fatalf("seed session %s: %v", id, err)
		}
	}
}
