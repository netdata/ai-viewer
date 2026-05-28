// Residual coverage tests for the presenter package after iter-4
// split this file by area: see coverage_health_test.go,
// coverage_middleware_test.go, and coverage_embed_test.go for the
// neighbouring test files. This file keeps the cross-cutting cases
// (DB-unavailable envelopes, schema_meta error paths, method-gating,
// not-implemented payload, custom-logger acceptance) so each file
// stays well under the project's 400-line budget.
package presenter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// TestSources_DBQueryFailureReturns503 asserts that a query error
// against the sources table surfaces as DB_UNAVAILABLE rather than
// silently degrading. We force the failure by closing the DB before
// the request.
func TestSources_DBQueryFailureReturns503(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeDBUnavailable)
	}
}

// TestCheckSchema_NonNumeric asserts the helper rejects a
// schema_meta.version that is not a positive integer string.
func TestCheckSchema_NonNumeric(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.DB().Exec(`UPDATE schema_meta SET value='abc' WHERE key='version'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err = CheckSchema(s.DB(), SchemaVersion)
	if err == nil || !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("got %v, want ErrSchemaMismatch", err)
	}
}

// TestCheckSchema_MissingRow asserts the helper reports
// ErrSchemaMismatch when the row is absent.
func TestCheckSchema_MissingRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.DB().Exec(`DELETE FROM schema_meta WHERE key='version'`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err = CheckSchema(s.DB(), SchemaVersion)
	if err == nil || !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("got %v, want ErrSchemaMismatch", err)
	}
}

// TestSchemaVersionErrorString asserts the formatted message includes
// both numbers.
func TestSchemaVersionErrorString(t *testing.T) {
	t.Parallel()
	e := &schemaVersionError{got: 2, want: 1}
	if !strings.Contains(e.Error(), "2") || !strings.Contains(e.Error(), "1") {
		t.Fatalf("err: %q", e.Error())
	}
}

// TestRoot_PostMethodNotAllowed asserts a POST to / is refused with a
// 405 carrying the METHOD_NOT_ALLOWED code.
func TestRoot_PostMethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestNotImplementedReportsChunk asserts the deferred-route handler
// includes the chunk hint in the JSON details so future readers see
// where the implementation will land.
func TestNotImplementedReportsChunk(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	// /api/sessions/{id}/topology is still deferred (Chunk 14); it falls
	// through the live session routes to the notImplemented catch-all.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/abc/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if v, _ := env.Error.Details["chunk"].(string); v != "13+" {
		t.Fatalf("chunk = %v", env.Error.Details["chunk"])
	}
}

// TestHEAD_DeferredRouteReturns404WithEmptyBody pins the HEAD contract
// on the error path through the full middleware chain: a HEAD request
// to a still-deferred route returns 404 with the JSON Content-Type
// header set but an empty response body. Codex iter-4 P3 flagged that
// without this guard, HEAD to error paths leaked the JSON envelope,
// violating presenter.md §"Routing". As of Chunk 12 the deferred route
// used here is the topology sub-route (Chunk 14).
func TestHEAD_DeferredRouteReturns404WithEmptyBody(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodHead, "/api/sessions/abc/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q (%d bytes), want empty",
			rr.Body.String(), rr.Body.Len())
	}
	if ct := rr.Header().Get("Content-Type"); ct == "" {
		t.Fatal("Content-Type missing on HEAD error response")
	}
}

// TestNewAcceptsCustomLogger documents that New itself accepts any
// logger — invalid log levels are the binary's main()'s problem, not
// the package's. This test exists to ensure the constructor does not
// regress into rejecting custom loggers.
func TestNewAcceptsCustomLogger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var buf bytes.Buffer
	custom := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p, err := New(Options{DB: s.DB(), Logger: custom, StartedAt: time.Now()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.logger == nil {
		t.Fatal("logger nil")
	}
	// Drive a request so the custom logger emits something.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if buf.Len() == 0 {
		t.Fatal("custom logger received no output")
	}
}
