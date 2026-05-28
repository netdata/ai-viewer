package presenter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// newClosedDBPresenter builds a presenter whose underlying store is
// closed, so every QueryContext returns "sql: database is closed". This
// drives the DB-error branch of each Chunk-12 handler without stubbing
// the driver — mirrors TestSources_DBErrorLogCarriesRequestID.
func newClosedDBPresenter(t *testing.T) (*Presenter, *slog.Logger) {
	t.Helper()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	frontend := fstest.MapFS{
		"frontend_dist/index.html": &fstest.MapFile{
			Data:    []byte("<!doctype html><title>test</title>"),
			ModTime: fixedTime,
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := New(Options{
		DB:            s.DB(),
		Logger:        logger,
		Version:       "test-sha",
		DBPath:        "/tmp/test.db",
		StartedAt:     fixedTime.Add(-30 * time.Second),
		SchemaVersion: SchemaVersion,
		Now:           func() time.Time { return fixedTime },
		FrontendFS:    frontend,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return p, logger
}

// TestChunk12_DBErrorPaths asserts every Chunk-12 endpoint maps a failed
// query to a 503 DB_UNAVAILABLE envelope (the closed-DB path) and that a
// detail/logs request on the closed DB never panics.
func TestChunk12_DBErrorPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, path string
	}{
		{"sessions list", "/api/sessions"},
		{"session detail", "/api/sessions/rootA"},
		{"session logs", "/api/sessions/rootA/logs"},
		{"stats", "/api/stats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newClosedDBPresenter(t)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			p.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: status = %d, want 503 (body=%q)", tc.path, rr.Code, rr.Body.String())
			}
			var env errorEnvelope
			if rr.Body.Len() > 0 {
				_ = json.NewDecoder(rr.Body).Decode(&env)
			}
			if env.Error.Code != CodeDBUnavailable {
				t.Fatalf("%s: code = %q, want %q", tc.path, env.Error.Code, CodeDBUnavailable)
			}
		})
	}
}

// TestChunk12_TimeoutMapsTo504 asserts the query-timeout branch maps to a
// 504 TIMEOUT envelope. We drive it by cancelling the request context
// before the handler runs its query, which surfaces as
// context.Canceled — but withQueryTimeout wraps it so a deadline that has
// already elapsed yields DeadlineExceeded. To get a deterministic
// DeadlineExceeded we use a request context whose deadline is already in
// the past.
func TestChunk12_TimeoutMapsTo504(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// A context already past its deadline forces QueryContext to return
	// context.DeadlineExceeded immediately.
	ctx, cancel := context.WithDeadline(context.Background(), fixedTime.Add(-time.Hour))
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body=%q)", rr.Code, rr.Body.String())
	}
	var env errorEnvelope
	if rr.Body.Len() > 0 {
		_ = json.NewDecoder(rr.Body).Decode(&env)
	}
	if env.Error.Code != CodeTimeout {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeTimeout)
	}
}

// TestChunk12_EmptyPathID asserts that a trailing-slash session id is
// treated as not-found / bad request rather than panicking. With the
// ServeMux {id} wildcard an empty id segment does not match the pattern,
// so the request falls through to the catch-all (404). This test pins
// that the handler's own empty-id guard is reachable when invoked
// directly via a crafted request.
func TestChunk12_EmptyPathIDGuards(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// Whitespace-only id: the wildcard captures it, the handler trims it
	// to "" and returns 400.
	for _, path := range []string{"/api/sessions/%20", "/api/sessions/%20/logs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body=%q)", path, rr.Code, rr.Body.String())
		}
	}
}
