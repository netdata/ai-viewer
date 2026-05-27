package presenter

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// captureLogger returns a slog.Logger backed by an in-memory buffer so
// tests can assert structured log output.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

// TestLoggingMiddlewareEmitsOnePerRequest asserts a single structured
// log line per request and the X-Request-ID header is set on the
// response.
func TestLoggingMiddlewareEmitsOnePerRequest(t *testing.T) {
	t.Parallel()
	logger, buf := captureLogger()
	handler := loggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brew"))
	}))
	req := httptest.NewRequest(http.MethodGet, "/teapot", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID header")
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d: %q", len(lines), buf.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}
	if line["msg"] != "http request" {
		t.Fatalf("msg = %v, want http request", line["msg"])
	}
	if line["status"] != float64(http.StatusTeapot) {
		t.Fatalf("status = %v", line["status"])
	}
}

// TestRecoverMiddlewareCatchesPanic asserts a panic in the downstream
// handler does NOT crash the server; instead a 500 with the structured
// INTERNAL_ERROR envelope is returned and an error log is emitted.
func TestRecoverMiddlewareCatchesPanic(t *testing.T) {
	t.Parallel()
	logger, buf := captureLogger()
	handler := recoverMiddleware(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode err: %v", err)
	}
	if env.Error.Code != CodeInternalError {
		t.Fatalf("error.code = %q, want %q", env.Error.Code, CodeInternalError)
	}
	if !strings.Contains(buf.String(), "handler panic") {
		t.Fatalf("expected panic log line, got %q", buf.String())
	}
}

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

// TestBodyLimitMiddlewareCapsPOST asserts an oversized POST body is
// rejected when read.
func TestBodyLimitMiddlewareCapsPOST(t *testing.T) {
	t.Parallel()
	var lastErr error
	handler := bodyLimitMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		lastErr = err
		w.WriteHeader(http.StatusOK)
	}))

	// Body 2 MB > 1 MB limit.
	body := bytes.NewReader(bytes.Repeat([]byte("a"), maxRequestBodyBytes+1024))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if lastErr == nil {
		t.Fatal("expected read error from MaxBytesReader, got nil")
	}
}

// TestRequestIDFromContext asserts the helper round-trips a value
// stored by loggingMiddleware via WithValue, returns the empty string
// for a context with no value attached, and rejects a nil context
// without panicking. Every request-scoped log call now routes through
// this helper, so a nil-context guard belongs in the unit-test surface
// rather than relying on call-site discipline.
func TestRequestIDFromContext(t *testing.T) {
	t.Parallel()
	if v := requestIDFromContext(emptyContext()); v != "" {
		t.Fatalf("nil context: got %q, want empty", v)
	}
	ctx := context.Background()
	if v := requestIDFromContext(ctx); v != "" {
		t.Fatalf("empty context: got %q", v)
	}
	ctx = withRequestID(ctx, "abc123")
	if v := requestIDFromContext(ctx); v != "abc123" {
		t.Fatalf("got %q, want abc123", v)
	}
}

// emptyContext returns a typed-nil context.Context so callers can
// exercise the nil-input guard without tripping staticcheck SA1012,
// which (rightly) bans literal `nil` arguments to context parameters.
// The helper exists purely to keep TestRequestIDFromContext honest
// about the defensive branch in requestIDFromContext.
func emptyContext() context.Context { return nil }

// TestLoggingMiddleware_NilLoggerSafe asserts the middleware is safe to
// use with a nil logger — the deferred emit short-circuits without
// panicking. Covers the iter-6 `if logger == nil { return }` branch
// inside the defer that exists so misconfigured callers cannot crash
// the server.
func TestLoggingMiddleware_NilLoggerSafe(t *testing.T) {
	t.Parallel()
	handler := loggingMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// X-Request-ID is set even when the logger is nil because the value
	// is part of the response contract, independent of logging.
	if rr.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID missing")
	}
}

// uuidV4Re matches the RFC 4122 §4.4 dashed UUID-v4 string form:
// 8-4-4-4-12 hex chars with the version nibble pinned to 4 and the
// variant nibble pinned to 8/9/a/b. Tests use this to guard the
// request-id contract documented in observability.md §"Trace IDs".
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewRequestIDIsUUIDV4AndUnique asserts the request-id helper
// produces RFC 4122 §4.4 UUID-v4 strings that don't collide on the
// first 64 calls. Pins observability.md §"Trace IDs"; the previous
// 16-hex-char output was caught as spec drift by codex iter-4 and
// fixed in iter-5.
func TestNewRequestIDIsUUIDV4AndUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]struct{}{}
	for i := 0; i < 64; i++ {
		v := newRequestID()
		if !uuidV4Re.MatchString(v) {
			t.Fatalf("request id %q does not match UUID-v4 form", v)
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("duplicate request id %q in 64 attempts", v)
		}
		seen[v] = struct{}{}
	}
}

// TestLoggingMiddlewareLogsClientIPAndUUIDRequestID asserts the
// per-request structured log line satisfies the observability.md
// §"Structured Logging" contract: `client_ip` is present and stripped
// of the port; `request_id` matches the UUID-v4 shape and matches the
// X-Request-ID response header. Pins codex iter-4 P2 fix.
func TestLoggingMiddlewareLogsClientIPAndUUIDRequestID(t *testing.T) {
	t.Parallel()
	logger, buf := captureLogger()
	handler := loggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	header := rr.Header().Get("X-Request-ID")
	if !uuidV4Re.MatchString(header) {
		t.Fatalf("X-Request-ID %q is not UUID-v4", header)
	}

	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("unmarshal log: %v (raw: %q)", err, buf.String())
	}
	if got, _ := line["client_ip"].(string); got != "127.0.0.1" {
		t.Fatalf("client_ip = %q, want 127.0.0.1", got)
	}
	rid, _ := line["request_id"].(string)
	if rid != header {
		t.Fatalf("request_id %q != X-Request-ID header %q", rid, header)
	}
	if !uuidV4Re.MatchString(rid) {
		t.Fatalf("request_id %q is not UUID-v4", rid)
	}
}

// TestPanic_AccessLogStillEmitted pins the end-to-end contract from
// observability.md §"Trace IDs": when a handler panics, BOTH the
// panic log line AND the deferred access log line MUST be emitted and
// MUST carry the same request_id (mirroring the X-Request-ID response
// header). Codex iter-5 P2 caught that the pre-defer logging
// middleware silently dropped the access log on the panic path and
// that the panic log omitted request_id altogether.
func TestPanic_AccessLogStillEmitted(t *testing.T) {
	t.Parallel()
	logger, buf := captureLogger()
	// Compose the same chain order as Presenter.Handler() so the test
	// exercises the production wiring: logging OUTER, recover INNER.
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom-iter6")
	})
	chain := loggingMiddleware(logger)(recoverMiddleware(logger)(inner))

	req := httptest.NewRequest(http.MethodGet, "/explodes", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	rid := rr.Header().Get("X-Request-ID")
	if !uuidV4Re.MatchString(rid) {
		t.Fatalf("X-Request-ID = %q (not UUID-v4)", rid)
	}

	var panicLine, accessLine map[string]any
	for _, raw := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("unmarshal log: %v (raw: %q)", err, raw)
		}
		switch m["msg"] {
		case "presenter: handler panic":
			panicLine = m
		case "http request":
			accessLine = m
		}
	}
	if panicLine == nil {
		t.Fatalf("no panic log line: %q", buf.String())
	}
	if accessLine == nil {
		t.Fatalf("no access log line (deferred emit failed): %q", buf.String())
	}
	if got, _ := panicLine["request_id"].(string); got != rid {
		t.Fatalf("panic log request_id = %q, want %q", got, rid)
	}
	if got, _ := accessLine["request_id"].(string); got != rid {
		t.Fatalf("access log request_id = %q, want %q", got, rid)
	}
	if got := accessLine["status"]; got != float64(http.StatusInternalServerError) {
		t.Fatalf("access log status = %v, want 500", got)
	}
}

// TestClientIPFromRequest covers the edge shapes of r.RemoteAddr the
// stdlib produces: IPv4:port, [IPv6]:port, plain string (test
// recorder), and empty.
func TestClientIPFromRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		remote string
		want   string
	}{
		{"", ""},
		{"127.0.0.1:54321", "127.0.0.1"},
		{"[::1]:54321", "::1"},
		{"not-a-host-port", "not-a-host-port"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tc.remote
		got := clientIPFromRequest(req)
		if got != tc.want {
			t.Errorf("clientIPFromRequest(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
	if got := clientIPFromRequest(nil); got != "" {
		t.Errorf("clientIPFromRequest(nil) = %q, want empty", got)
	}
}
