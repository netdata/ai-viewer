// Coverage tests for the HTTP middleware surface: gzip, response-writer
// wrapper, parseAcceptWeight q-value parser, writeJSONError details
// pass-through, and toAttrs non-string-key skip. Split out of
// coverage_test.go in iter-4 so no single test file exceeds the
// project's 400-line budget.
package presenter

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseAcceptWeight asserts the q-value parser handles the shapes
// browsers send.
func TestParseAcceptWeight(t *testing.T) {
	t.Parallel()
	cases := map[string]float64{
		"":              1.0,
		"q=":            1.0,
		"q=0":           0,
		"q=0.0":         0,
		"q=0.5":         0.5,
		"q=1":           1.0,
		"q=1.0":         1.0,
		"q=0.25":        0.25,
		"q=abc":         1.0,
		"level=5;q=0.3": 0.3,
	}
	for in, want := range cases {
		if got := parseAcceptWeight(in); got != want {
			t.Errorf("parseAcceptWeight(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestWriteJSONErrorWithDetails asserts the helper threads structured
// details through to both the JSON envelope and the log output.
func TestWriteJSONErrorWithDetails(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	writeJSONError(rr, req, logger, http.StatusBadRequest, CodeBadRequest, "bad", map[string]any{"k": "v"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if v, ok := env.Error.Details["k"].(string); !ok || v != "v" {
		t.Fatalf("details = %v", env.Error.Details)
	}
	if !strings.Contains(buf.String(), "detail.k") {
		t.Fatalf("logger missing details: %q", buf.String())
	}
}

// TestWriteJSONErrorHEADHasEmptyBody asserts the helper honours the
// HEAD contract: status and Content-Type are written but no JSON body.
// Pins presenter.md §"Routing" (HEAD returns empty body) — without
// this guard, HEAD requests to error paths
// (missing assets, deferred API routes) leak the JSON envelope.
func TestWriteJSONErrorHEADHasEmptyBody(t *testing.T) {
	t.Parallel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/missing", nil)
	writeJSONError(rr, req, nil, http.StatusNotFound, CodeNotFound, "missing", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct == "" {
		t.Fatalf("Content-Type missing on HEAD error response")
	}
}

// TestToAttrsSkipsNonStringKey asserts the slog converter ignores any
// key that is not a string.
func TestToAttrsSkipsNonStringKey(t *testing.T) {
	t.Parallel()
	got := toAttrs([]any{42, "v", "k", "v"})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (non-string key dropped)", len(got))
	}
	if got[0].Key != "k" {
		t.Fatalf("got key %q, want k", got[0].Key)
	}
}

// TestGzipMiddlewareSkipsAlreadyCompressed asserts the middleware does
// not double-compress when the downstream handler sets
// Content-Encoding.
func TestGzipMiddlewareSkipsAlreadyCompressed(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("z"), gzipMinBytes*2)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/already", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if got := rr.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
}

// flushingRecorder lets us observe whether the loggingResponseWriter
// propagates Flush to its delegate.
type flushingRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushingRecorder) Flush() { f.flushed = true }

// TestLoggingResponseWriterFlushPassthrough asserts the response
// wrapper threads Flush through when the underlying writer supports it.
func TestLoggingResponseWriterFlushPassthrough(t *testing.T) {
	t.Parallel()
	rec := &flushingRecorder{ResponseRecorder: httptest.NewRecorder()}
	lw := &loggingResponseWriter{ResponseWriter: rec, status: 200}
	lw.Flush()
	if !rec.flushed {
		t.Fatal("Flush did not propagate to underlying recorder")
	}
}

// TestWriteJSONError_IncludesRequestID pins observability.md §"Trace
// IDs" on the error log surface: when writeJSONError emits its warning
// line, the line MUST carry the same `request_id` value that
// loggingMiddleware seeded into r.Context() (and that surfaces as the
// X-Request-ID response header). Error/panic logs were silently
// dropping the field and breaking per-request grep.
func TestWriteJSONError_IncludesRequestID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	const rid = "11111111-2222-4333-8444-555555555555"
	req = req.WithContext(withRequestID(req.Context(), rid))
	writeJSONError(rr, req, logger, http.StatusNotFound, CodeNotFound, "not found", nil)

	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &line); err != nil {
		t.Fatalf("unmarshal log: %v (raw: %q)", err, buf.String())
	}
	got, _ := line["request_id"].(string)
	if got != rid {
		t.Fatalf("request_id = %q, want %q", got, rid)
	}
}
