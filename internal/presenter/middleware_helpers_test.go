package presenter

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGzipMiddleware_BypassDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		path   string
		accept string
		want   bool
	}{
		{name: "sse", path: "/api/events", accept: "gzip", want: true},
		{name: "no accept encoding", path: "/api/health", want: true},
		{name: "gzip accepted", path: "/api/health", accept: "gzip", want: false},
		{name: "gzip refused", path: "/api/health", accept: "gzip;q=0", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			if got := shouldBypassGzip(req); got != tc.want {
				t.Fatalf("shouldBypassGzip(%s, %q) = %v, want %v", tc.path, tc.accept, got, tc.want)
			}
		})
	}
}

func TestGzipMiddleware_ResponseDecision(t *testing.T) {
	t.Parallel()
	large := &bufferingResponseWriter{header: http.Header{}}
	large.buf.Write(bytes.Repeat([]byte("x"), gzipMinBytes))
	if !shouldGzipBufferedResponse(large) {
		t.Fatal("large unencoded response should be gzipped")
	}

	small := &bufferingResponseWriter{header: http.Header{}}
	small.buf.Write(bytes.Repeat([]byte("x"), gzipMinBytes-1))
	if shouldGzipBufferedResponse(small) {
		t.Fatal("small response should not be gzipped")
	}

	encoded := &bufferingResponseWriter{header: http.Header{}}
	encoded.header.Set("Content-Encoding", "br")
	encoded.buf.Write(bytes.Repeat([]byte("x"), gzipMinBytes))
	if shouldGzipBufferedResponse(encoded) {
		t.Fatal("already-encoded response should not be gzipped")
	}
}

func TestParseAcceptWeight_RawQValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want float64
		ok   bool
	}{
		{raw: "0", want: 0, ok: true},
		{raw: "0.", want: 0, ok: true},
		{raw: ".5", want: 0.5, ok: true},
		{raw: "0.25", want: 0.25, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "1.5", want: 1, ok: true},
		{raw: "", ok: false},
		{raw: "abc", ok: false},
		{raw: "0.x", ok: false},
	}

	for _, tc := range cases {
		got, ok := parseHTTPQValue(tc.raw)
		if ok != tc.ok {
			t.Errorf("parseHTTPQValue(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("parseHTTPQValue(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
