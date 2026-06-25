package presenter

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPayloadPreview_GetAndHeadExposeStreamingHeaders(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	base := seedBase()
	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.txt")
	payloadText := "hello payload body"
	if err := os.WriteFile(payloadPath, []byte(payloadText), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}

	seedSource(t, db, "srcPayload", "aiagent_v3", root, base)
	seedSession(t, db, sessionRow{
		id: "rootPayload", sourceID: "srcPayload", nativeID: "nativePayload", rootID: "rootPayload",
		kind: "root", agent: "nedi", status: "completed", startTS: base,
		turnCount: 1, opCount: 1,
	})
	seedTurn(t, db, turnRow{id: "turnPayload", sessionID: "rootPayload", seq: 1, startTS: base, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "opPayload", turnID: "turnPayload", sessionID: "rootPayload", seq: 1, kind: "tool", name: "read_file", startTS: base, status: "completed"})
	id := seedPayload(t, db, payloadRow{
		opID: "opPayload", kind: "tool_response", format: "text",
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
