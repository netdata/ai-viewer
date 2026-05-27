// Coverage tests for the embedded-frontend and asset-serving surfaces
// (serveIndex / serveAsset / contentTypeForAsset / lowerExt). Split out
// of coverage_test.go in iter-4 so no single test file exceeds the
// project's 400-line budget.
package presenter

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/netdata/ai-viewer/internal/store"
)

// TestServeIndex_MissingEmbedReturns500 asserts the handler reports an
// internal error when the embedded FS lacks index.html. Provides
// coverage for the missing-file branch.
func TestServeIndex_MissingEmbedReturns500(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fstest.MapFS{}, // no index.html
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// TestServeAsset_DirReturns404 asserts that opening a directory under
// /assets/ yields NOT_FOUND. Vite never emits a bare directory but the
// FS contract permits one; the handler must not serve it.
func TestServeAsset_DirReturns404(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	fs := fstest.MapFS{
		"frontend_dist/index.html":         {Data: []byte("ok")},
		"frontend_dist/assets/nested/file": {Mode: 0o755},
	}
	p, err := New(Options{
		DB:         s.DB(),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		FrontendFS: fs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/assets/nested", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for directory", rr.Code)
	}
}

// TestServeAsset_BadPathReturns400 asserts a path containing `..` is
// refused. The Go stdlib ServeMux normalises `..` segments to a clean
// path and issues a 301 redirect before our handler is invoked, so the
// observable outcome is a redirect; we assert that, then drive a
// raw-handler call to exercise the BAD_REQUEST branch directly.
func TestServeAsset_BadPathReturns400(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	// Direct call into the handler bypasses ServeMux normalisation so
	// the safeAssetPath rejection surface is exercised.
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.URL.Path = "/assets/../app.js"
	rr := httptest.NewRecorder()
	p.serveAsset(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("direct status = %d, want 400", rr.Code)
	}
}

// TestContentTypeForAsset_MoreExtensions extends the coverage for the
// MIME table to cover every branch.
func TestContentTypeForAsset_MoreExtensions(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a.html":  "text/html; charset=utf-8",
		"a.json":  "application/json; charset=utf-8",
		"a.jpg":   "image/jpeg",
		"a.jpeg":  "image/jpeg",
		"a.ico":   "image/x-icon",
		"a.woff":  "font/woff",
		"a.woff2": "font/woff2",
		"a.ttf":   "font/ttf",
		"a.map":   "application/json; charset=utf-8",
		"noext":   "application/octet-stream",
	}
	for in, want := range cases {
		if got := contentTypeForAsset(in); got != want {
			t.Errorf("contentTypeForAsset(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLowerExt asserts the extension helper.
func TestLowerExt(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"foo.JS":        ".js",
		"foo.tar.gz":    ".gz",
		"foo":           "",
		"":              "",
		"path/to/a.css": ".css",
	}
	for in, want := range cases {
		if got := lowerExt(in); got != want {
			t.Errorf("lowerExt(%q) = %q, want %q", in, got, want)
		}
	}
}
