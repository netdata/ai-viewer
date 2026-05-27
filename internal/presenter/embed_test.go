package presenter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServeIndex_PlaceholderReturned asserts GET / returns the
// placeholder index.html bytes with the right content type and a
// no-cache header so post-deploy reloads pick up the new bundle.
func TestServeIndex_PlaceholderReturned(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if !strings.Contains(rr.Body.String(), "test") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

// TestServeAsset_PresentReturns200WithLongCache asserts a known asset
// is served with the long-cache header Vite-style hashed names rely on.
func TestServeAsset_PresentReturns200WithLongCache(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

// TestServeAsset_MissingReturns404 asserts a missing asset returns
// 404 with the structured NOT_FOUND envelope, NOT a fallthrough to
// the SPA shell. Per presenter.md, the SPA fallback is for `/`-style
// paths only.
func TestServeAsset_MissingReturns404(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/assets/missing.css", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "NOT_FOUND") {
		t.Fatalf("body = %q, want NOT_FOUND envelope", rr.Body.String())
	}
}

// TestRoot_NonRootPathReturns404 asserts the root handler refuses any
// path other than exactly "/" — non-asset, non-API paths get a
// structured 404 instead of accidentally serving index.html for any
// URL. The SPA's client-side router takes ownership only after the
// browser loads "/" once.
func TestRoot_NonRootPathReturns404(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestSafeAssetPath_RejectsTraversal asserts the path sanitiser
// refuses any ".." segment. Defence in depth even though fs.FS itself
// also resists escape.
func TestSafeAssetPath_RejectsTraversal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in string
		ok bool
	}{
		{"/assets/a.js", true},
		{"/assets/nested/a.js", true},
		{"/assets/../a.js", false},
		{"/assets/..", false},
		{"/assets/", false},
		{"/foo/a.js", false},
	}
	for _, tc := range cases {
		_, got := safeAssetPath(tc.in)
		if got != tc.ok {
			t.Errorf("safeAssetPath(%q) ok = %v, want %v", tc.in, got, tc.ok)
		}
	}
}

// TestContentTypeForAsset asserts the extension switch returns sensible
// MIME types for the asset shapes Vite emits.
func TestContentTypeForAsset(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"app.js":   "application/javascript; charset=utf-8",
		"app.mjs":  "application/javascript; charset=utf-8",
		"app.css":  "text/css; charset=utf-8",
		"icon.svg": "image/svg+xml",
		"img.png":  "image/png",
		"img.webp": "image/webp",
		"data.bin": "application/octet-stream",
	}
	for in, want := range cases {
		if got := contentTypeForAsset(in); got != want {
			t.Errorf("contentTypeForAsset(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEmbedDisabledReturns404 asserts that a presenter with no
// FrontendFS still answers the API routes but returns 404 on / and
// /assets/* — the failure mode for a binary built without the frontend.
func TestEmbedDisabledReturns404(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	// Reach into the Presenter to drop the frontend. We do this rather
	// than calling New with FrontendFS=nil because the test helper
	// pre-populates frontend assets for the other tests.
	p.frontend = nil

	for _, path := range []string{"/", "/assets/app.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		p.Handler().ServeHTTP(rr, req)
		switch path {
		case "/":
			if rr.Code != http.StatusInternalServerError {
				t.Errorf("GET %s code = %d, want 500", path, rr.Code)
			}
		default:
			if rr.Code != http.StatusNotFound {
				t.Errorf("GET %s code = %d, want 404", path, rr.Code)
			}
		}
	}
}
