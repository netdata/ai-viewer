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

// TestRoot_ClientRouteServesSPA asserts the SPA-fallback contract
// (presenter.md §"SPA fallback"): a hard navigation / reload / bookmark of
// a client-side route that the mux does not route to a more-specific
// handler (not /api/*, not /assets/*, not an exact root public file) serves
// the SPA shell — the same built index.html with 200 + text/html +
// no-cache as GET /. Several distinct paths are exercised to prove the
// fallback is a catch-all, NOT special-cased to one route. (Inverts the
// former TestRoot_NonRootPathReturns404, which pinned the old behaviour of
// 404-ing any path other than "/".)
func TestRoot_ClientRouteServesSPA(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	for _, p2 := range []string{"/sessions/abc123", "/sources"} {
		t.Run(p2, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p2, nil)
			rr := httptest.NewRecorder()
			p.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200 (body=%q)", p2, rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("GET %s Content-Type = %q, want text/html", p2, ct)
			}
			if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Fatalf("GET %s Cache-Control = %q, want no-cache", p2, cc)
			}
			// Same built index.html bytes the newTestPresenter synthetic FS
			// installs (marker also asserted by TestServeIndex_PlaceholderReturned).
			if !strings.Contains(rr.Body.String(), "test") {
				t.Fatalf("GET %s body = %q, want the built index.html shell", p2, rr.Body.String())
			}
		})
	}
}

// TestRoot_ClientRouteHeadEmptyBody asserts a HEAD on a client-side route
// honours the shell's HEAD parity: 200 + text/html + no-cache + a
// Content-Length header, empty body (RFC 9110 §9.3.2). Mirrors
// TestServeIndex_NoIndexHeadEmptyBody for the built-shell path.
func TestRoot_ClientRouteHeadEmptyBody(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodHead, "/sessions/abc123", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if cl := rr.Header().Get("Content-Length"); cl == "" {
		t.Fatalf("HEAD /sessions/abc123: Content-Length not set")
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
}

// TestRoot_ClientRouteMethodNotAllowed asserts a non-GET/HEAD method on a
// client-side route is rejected with the METHOD_NOT_ALLOWED envelope — the
// SPA fallback serves the shell only for GET/HEAD (presenter.md §"SPA
// fallback").
func TestRoot_ClientRouteMethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/sessions/abc123", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "METHOD_NOT_ALLOWED") {
		t.Fatalf("body = %q, want METHOD_NOT_ALLOWED envelope", rr.Body.String())
	}
}

// TestRoot_ApiUnknownStillJSON404 asserts the SPA fallback did NOT swallow
// unknown /api/* paths: they must still return the structured JSON
// NOT_FOUND envelope (Content-Type application/json), never the HTML shell
// (presenter.md §"SPA fallback": /api/* is exempt and surfaces real
// errors). TestHandlerRegistersRoutes already pins the 404 status for
// /api/does-not-exist; this adds the body + content-type assertions that
// prove the response is JSON, not the fallback shell.
func TestRoot_ApiUnknownStillJSON404(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json (JSON envelope, not the HTML shell)", ct)
	}
	if !strings.Contains(rr.Body.String(), "NOT_FOUND") {
		t.Fatalf("body = %q, want NOT_FOUND envelope", rr.Body.String())
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
