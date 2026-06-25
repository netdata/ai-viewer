package presenter

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPayloadPreview_GetAndHeadExposeStreamingHeaders(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.txt")
	payloadText := "hello payload body"
	if err := os.WriteFile(payloadPath, []byte(payloadText), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}

	id := seedPayloadPreviewRef(t, db, root, payloadRow{
		kind: "tool_response", format: "text",
		locationURI:   "file://" + payloadPath,
		originalBytes: int64(len(payloadText)),
		storedBytes:   int64(len(payloadText)),
	})

	url := "/api/payloads/" + strconv.FormatInt(id, 10)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Format"); got != "text" {
		t.Fatalf("X-Payload-Format = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Truncated"); got != "false" {
		t.Fatalf("X-Payload-Truncated = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Total-Bytes"); got != strconv.Itoa(len(payloadText)) {
		t.Fatalf("X-Payload-Total-Bytes = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Preview-Bytes"); got != strconv.Itoa(len(payloadText)) {
		t.Fatalf("X-Payload-Preview-Bytes = %q", got)
	}
	if rr.Body.String() != payloadText {
		t.Fatalf("GET body = %q, want %q", rr.Body.String(), payloadText)
	}

	headReq := httptest.NewRequest(http.MethodHead, url, nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200; body=%s", headRR.Code, headRR.Body.String())
	}
	if body, _ := io.ReadAll(headRR.Body); len(body) != 0 {
		t.Fatalf("HEAD body len = %d, want 0", len(body))
	}
	if got := headRR.Header().Get("X-Payload-Total-Bytes"); got != strconv.Itoa(len(payloadText)) {
		t.Fatalf("HEAD X-Payload-Total-Bytes = %q", got)
	}
}

func TestPayloadPreview_TruncatesPlainPayloadAtPreviewCap(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "large.txt")
	payloadBody := bytes.Repeat([]byte("x"), payloadPreviewBytes+1)
	if err := os.WriteFile(payloadPath, payloadBody, 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	id := seedPayloadPreviewRef(t, db, root, payloadRow{
		kind: "tool_response", format: "text",
		locationURI:   "file://" + payloadPath,
		originalBytes: int64(len(payloadBody)),
		storedBytes:   int64(len(payloadBody)),
	})

	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, payloadURL(id), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Payload-Truncated"); got != "true" {
		t.Fatalf("X-Payload-Truncated = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Preview-Bytes"); got != strconv.Itoa(payloadPreviewBytes) {
		t.Fatalf("X-Payload-Preview-Bytes = %q", got)
	}
	if got := rr.Body.Len(); got != payloadPreviewBytes {
		t.Fatalf("GET body len = %d, want %d", got, payloadPreviewBytes)
	}
}

func TestPayloadPreview_DecompressesGzipPayload(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.txt.gz")
	payloadText := "hello gzip payload"
	writeGzipFile(t, payloadPath, []byte(payloadText))
	info, err := os.Stat(payloadPath)
	if err != nil {
		t.Fatalf("stat gzip payload: %v", err)
	}
	id := seedPayloadPreviewRef(t, db, root, payloadRow{
		kind: "tool_response", format: "text", compression: "gzip",
		locationURI:   "file://" + payloadPath,
		originalBytes: int64(len(payloadText)),
		storedBytes:   info.Size(),
	})

	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, payloadURL(id), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != payloadText {
		t.Fatalf("GET body = %q, want %q", got, payloadText)
	}
	if got := rr.Header().Get("X-Payload-Truncated"); got != "false" {
		t.Fatalf("X-Payload-Truncated = %q", got)
	}
	if got := rr.Header().Get("X-Payload-Total-Bytes"); got != strconv.FormatInt(info.Size(), 10) {
		t.Fatalf("X-Payload-Total-Bytes = %q", got)
	}
}

func TestPayloadPreview_TruncatesJSONAtDocumentBoundary(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.json")
	jsonDoc := `{"message":"ok"}`
	payloadText := jsonDoc + strings.Repeat(" ", payloadJSONCapBytes+1)
	if err := os.WriteFile(payloadPath, []byte(payloadText), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}
	id := seedPayloadPreviewRef(t, db, root, payloadRow{
		kind: "llm_response", format: "json",
		locationURI:   "file://" + payloadPath,
		originalBytes: int64(len(payloadText)),
		storedBytes:   int64(len(payloadText)),
	})

	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, payloadURL(id), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Payload-Truncated"); got != "true" {
		t.Fatalf("X-Payload-Truncated = %q", got)
	}
	if got := rr.Body.String(); got != jsonDoc {
		t.Fatalf("GET body = %q, want %q", got, jsonDoc)
	}
	if got := rr.Header().Get("X-Payload-Preview-Bytes"); got != strconv.Itoa(len(jsonDoc)) {
		t.Fatalf("X-Payload-Preview-Bytes = %q", got)
	}
}

func TestPayloadPreview_RejectsFileOutsideSourceRoot(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	root := t.TempDir()
	outside := t.TempDir()
	payloadPath := filepath.Join(outside, "payload.txt")
	if err := os.WriteFile(payloadPath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside payload fixture: %v", err)
	}
	id := seedPayloadPreviewRef(t, db, root, payloadRow{
		kind: "tool_response", format: "text",
		locationURI:   "file://" + payloadPath,
		originalBytes: int64(len("outside")),
		storedBytes:   int64(len("outside")),
	})

	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, payloadURL(id), nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("GET status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "outside configured source roots") {
		t.Fatalf("GET body = %q, want outside-source-root error", rr.Body.String())
	}
}

func seedPayloadPreviewRef(t *testing.T, db *sql.DB, sourceRoot string, row payloadRow) int64 {
	t.Helper()
	base := seedBase()
	seedSource(t, db, "srcPayload", "aiagent_v3", sourceRoot, base)
	seedSession(t, db, sessionRow{
		id: "rootPayload", sourceID: "srcPayload", nativeID: "nativePayload", rootID: "rootPayload",
		kind: "root", agent: "nedi", status: "completed", startTS: base,
		turnCount: 1, opCount: 1,
	})
	seedTurn(t, db, turnRow{id: "turnPayload", sessionID: "rootPayload", seq: 1, startTS: base, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "opPayload", turnID: "turnPayload", sessionID: "rootPayload", seq: 1, kind: "tool", name: "read_file", startTS: base, status: "completed"})
	row.opID = "opPayload"
	return seedPayload(t, db, row)
}

func payloadURL(id int64) string {
	return "/api/payloads/" + strconv.FormatInt(id, 10)
}

func writeGzipFile(t *testing.T, path string, body []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create gzip payload: %v", err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(body); err != nil {
		_ = f.Close()
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close gzip payload: %v", err)
	}
}
