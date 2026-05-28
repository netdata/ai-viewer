// Tests for the SOW-0001 Chunk 17 single-binary serve surfaces: the
// not-built degrade path of serveIndex (when the embedded FS has no
// index.html), the one-time Info log, the Content-Length/HEAD parity, and
// the root public-file handler (/favicon.svg). Split out of embed_test.go to
// keep each test file within the project's 400-line budget.
package presenter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/netdata/ai-viewer/internal/store"
)

// TestServeIndex_NoIndexServesNotice asserts the not-built degrade path
// (SOW-0001 Chunk 17, D2): when the frontend FS is wired but carries no
// index.html (the .gitkeep-only embed produced by a clean checkout), GET
// / serves the built-in "UI not built" notice at 200 with no-cache —
// NOT a 500 — while the body points the operator at scripts/build.sh.
// This is the dev-time state of `go run ./cmd/ai-viewer-serve`.
func TestServeIndex_NoIndexServesNotice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// FS has assets but deliberately no frontend_dist/index.html, mirroring
	// the .gitkeep-only embed before scripts/build.sh has run.
	frontend := fstest.MapFS{
		"frontend_dist/.gitkeep":      {Data: []byte("")},
		"frontend_dist/assets/app.js": {Data: []byte("console.log('x');\n")},
	}
	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: frontend,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (not-built notice, not 500)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "scripts/build.sh") {
		t.Fatalf("notice body must tell operator to run scripts/build.sh; body = %q", body)
	}
	if !strings.Contains(body, "not built") {
		t.Fatalf("notice body must say the UI is not built; body = %q", body)
	}
}

// TestServeIndex_NoIndexHeadEmptyBody asserts the not-built notice path
// honours HEAD: same 200 + headers, empty body (RFC 9110 §9.3.2).
func TestServeIndex_NoIndexHeadEmptyBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{"frontend_dist/.gitkeep": {Data: []byte("")}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer so a slog handler can be
// driven from the test without data races flagged by -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestServeIndex_NotBuiltLogsOnce asserts the not-built notice logs the
// single Info line at most once across many GET / requests (the sync.Once
// guard), per observability.md §Structured Logging: "Logged once so a
// dev-time unbuilt state is visible without flooding the log".
func TestServeIndex_NotBuiltLogsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	logBuf := &syncBuffer{}
	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})),
		FrontendFS: fstest.MapFS{"frontend_dist/.gitkeep": {Data: []byte("")}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rr.Code)
		}
	}

	got := strings.Count(logBuf.String(), "frontend not built; serving placeholder notice")
	if got != 1 {
		t.Fatalf("not-built Info log count = %d after 3 requests, want exactly 1\nlog:\n%s", got, logBuf.String())
	}
}

// TestServeIndex_NotBuiltSetsContentLength asserts the not-built notice sets a
// Content-Length so HEAD / advertises the same body length GET / returns
// (RFC 9110 parity). Pins the SOW-0001 Chunk 17 review fix.
func TestServeIndex_NotBuiltSetsContentLength(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{"frontend_dist/.gitkeep": {Data: []byte("")}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "/", nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		if cl := rr.Header().Get("Content-Length"); cl == "" {
			t.Fatalf("%s /: Content-Length not set on not-built notice", method)
		}
	}
}

// TestServePublicFile_FaviconServed asserts a root public asset referenced by
// index.html (favicon.svg) is served with 200 + the right content type and a
// no-cache header (it is not content-hashed). SOW-0001 Chunk 17 review fix:
// Vite copies frontend/public/* to dist/ root, so the binary must serve them.
func TestServePublicFile_FaviconServed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := New(Options{
		DB:     s.DB(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{
			"frontend_dist/index.html":  {Data: []byte("<!doctype html>")},
			"frontend_dist/favicon.svg": {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /favicon.svg status = %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Fatalf("Content-Type = %q, want image/svg+xml", ct)
	}
	if !strings.Contains(rr.Body.String(), "<svg") {
		t.Fatalf("body = %q, want the svg bytes", rr.Body.String())
	}
}

// TestServePublicFile_FaviconHeadEmptyBody asserts a root public asset
// honours HEAD: same 200 + Content-Type + Content-Length (equal to the file
// byte length) as GET, but an empty body (RFC 9110 §9.3.2). HEAD parity is a
// project contract (presenter.md §Routing); servePublicFile must advertise the
// length GET would return so a HEAD client can size the response.
func TestServePublicFile_FaviconHeadEmptyBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	favicon := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	p, err := New(Options{
		DB:     s.DB(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{
			"frontend_dist/index.html":  {Data: []byte("<!doctype html>")},
			"frontend_dist/favicon.svg": {Data: favicon},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodHead, "/favicon.svg", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD /favicon.svg status = %d, want 200 (body=%q)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Fatalf("Content-Type = %q, want image/svg+xml", ct)
	}
	if cl := rr.Header().Get("Content-Length"); cl != strconv.Itoa(len(favicon)) {
		t.Fatalf("Content-Length = %q, want %d (the favicon byte length)", cl, len(favicon))
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
}

// TestServePublicFile_MissingReturns404 asserts a missing root public asset
// returns the structured NOT_FOUND envelope (no SPA fallback for these paths).
func TestServePublicFile_MissingReturns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{"frontend_dist/index.html": {Data: []byte("<!doctype html>")}}, // no favicon
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /favicon.svg (missing) status = %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "NOT_FOUND") {
		t.Fatalf("body = %q, want NOT_FOUND envelope", rr.Body.String())
	}
}

// TestServePublicFile_MethodNotAllowed asserts a non-GET/HEAD method on a root
// public asset returns 405 (parity with serveAsset's method gating).
func TestServePublicFile_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := New(Options{
		DB:     s.DB(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{
			"frontend_dist/index.html":  {Data: []byte("<!doctype html>")},
			"frontend_dist/favicon.svg": {Data: []byte(`<svg/>`)},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/favicon.svg", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /favicon.svg status = %d, want 405", rr.Code)
	}
}
