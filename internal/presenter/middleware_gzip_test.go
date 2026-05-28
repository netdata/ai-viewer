package presenter

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGzipMiddlewareCompressesLargeBodies asserts a response above
// gzipMinBytes is compressed when the client advertises gzip support.
func TestGzipMiddlewareCompressesLargeBodies(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("abcd"), gzipMinBytes) // ~ 4 KB
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/big", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", rr.Header().Get("Content-Encoding"))
	}
	gz, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("decoded payload != original")
	}
}

// TestGzipMiddlewareSkipsSmallBodies asserts a response under the
// threshold is sent uncompressed regardless of Accept-Encoding so the
// CPU cost is not wasted on a few hundred bytes.
func TestGzipMiddlewareSkipsSmallBodies(t *testing.T) {
	t.Parallel()
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest(http.MethodGet, "/small", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding = %q, want empty (body too small)", rr.Header().Get("Content-Encoding"))
	}
	if rr.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

// TestGzipMiddlewareSkipsEventStream asserts the SSE path is exempt
// from gzip even when the client advertises support; framing matters
// more than payload size for that endpoint.
func TestGzipMiddlewareSkipsEventStream(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("x"), gzipMinBytes*2)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("SSE path must not be gzipped")
	}
}

// TestGzipMiddlewareSkipsWithoutAcceptEncoding asserts a client that
// does not advertise gzip support receives the raw bytes even when the
// response exceeds the size threshold.
func TestGzipMiddlewareSkipsWithoutAcceptEncoding(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("y"), gzipMinBytes*2)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("Content-Encoding=gzip without Accept-Encoding hint")
	}
}

// TestClientAcceptsGzip exercises the parser against the realistic
// shapes browsers send.
func TestClientAcceptsGzip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"gzip, deflate", true},
		{"deflate, gzip", true},
		{"gzip;q=0", false},
		{"gzip;q=0.5", true},
		{"deflate", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Accept-Encoding", tc.header)
		}
		got := clientAcceptsGzip(req)
		if got != tc.want {
			t.Errorf("clientAcceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
