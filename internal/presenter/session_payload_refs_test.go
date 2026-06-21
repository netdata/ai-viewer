// session_payload_refs_test.go (SOW-0092 chunk 3) — tests for the lazy
// /api/sessions/:id/payload_refs endpoint. The endpoint supports three
// scopes (?op=, ?turn=, neither) and is the lazy-load path that lets the
// session-detail page ship the slim response by default.

package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// payloadRefsBody is the lazy-load envelope.
type payloadRefsBody struct {
	Refs []payloadRefT `json:"refs"`
}

type payloadRefT struct {
	ID   int64  `json:"id"`
	OpID string `json:"op_id"`
}

func decodePayloadRefs(t *testing.T, data []byte) payloadRefsBody {
	t.Helper()
	var body payloadRefsBody
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, data)
	}
	return body
}

func TestPayloadRefs_Endpoint(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcPref", "aiagent_v3", "/tmp/pref", base)
	seedSession(t, db, sessionRow{
		id: "rootPref", sourceID: "srcPref", nativeID: "nR", rootID: "rootPref",
		kind: "root", agent: "nedi", status: "completed",
	})
	seedTurn(t, db, turnRow{id: "tPref1", sessionID: "rootPref", seq: 1, startTS: base, endTS: base + 1_000, status: "completed", opCount: 2})
	seedTurn(t, db, turnRow{id: "tPref2", sessionID: "rootPref", seq: 2, startTS: base + 2_000, endTS: base + 3_000, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "opPref1", turnID: "tPref1", sessionID: "rootPref", seq: 1, kind: "llm", name: "claude", startTS: base, endTS: base + 100, status: "completed"})
	seedOp(t, db, opRow{id: "opPref2", turnID: "tPref1", sessionID: "rootPref", seq: 2, kind: "tool", name: "read_file", startTS: base + 200, endTS: base + 800, status: "completed"})
	seedOp(t, db, opRow{id: "opPref3", turnID: "tPref2", sessionID: "rootPref", seq: 1, kind: "llm", name: "claude", startTS: base + 2_000, endTS: base + 2_500, status: "completed"})

	for _, ref := range []struct {
		opID, kind, uri string
		bytes           int64
	}{
		{"opPref1", "llm_response", "file:///tmp/r1.json", 100},
		{"opPref2", "tool_request", "file:///tmp/r2.json", 200},
		{"opPref2", "tool_response", "file:///tmp/r3.json", 300},
		{"opPref3", "llm_response", "file:///tmp/r4.json", 400},
	} {
		if _, err := db.Exec(`INSERT INTO payload_refs (op_id, kind, format, location_uri, original_bytes, stored_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
			ref.opID, ref.kind, "json", ref.uri, ref.bytes, ref.bytes); err != nil {
			t.Fatalf("seed ref %s: %v", ref.opID, err)
		}
	}

	tests := []struct {
		name      string
		url       string
		wantOpIDs []string
	}{
		{"all refs for session", "/api/sessions/rootPref/payload_refs", []string{"opPref1", "opPref2", "opPref2", "opPref3"}},
		{"refs for one op", "/api/sessions/rootPref/payload_refs?op=opPref2", []string{"opPref2", "opPref2"}},
		{"refs for one turn", "/api/sessions/rootPref/payload_refs?turn=tPref1", []string{"opPref1", "opPref2", "opPref2"}},
		{"refs for op with no refs", "/api/sessions/rootPref/payload_refs?op=opPref1", []string{"opPref1"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rr := httptest.NewRecorder()
			p.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			body := decodePayloadRefs(t, rr.Body.Bytes())
			if len(body.Refs) != len(tc.wantOpIDs) {
				t.Fatalf("refs count = %d, want %d", len(body.Refs), len(tc.wantOpIDs))
			}
			for i, ref := range body.Refs {
				if ref.OpID != tc.wantOpIDs[i] {
					t.Errorf("refs[%d].op_id = %q, want %q", i, ref.OpID, tc.wantOpIDs[i])
				}
			}
		})
	}
}

// TestPayloadRefs_MutuallyExclusiveRejectsBoth verifies the endpoint
// rejects requests that supply BOTH ?op and ?turn — the scopes are
// disjoint and the caller has to pick one.
func TestPayloadRefs_MutuallyExclusiveRejectsBoth(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootA/payload_refs?op=x&turn=y", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPayloadRefs_EmptySessionReturnsEmptyArray verifies an empty
// payload_refs list marshals as `[]` not `null` — clients iterate without
// a null guard.
func TestPayloadRefs_EmptySessionReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcEmpty", "aiagent_v3", "/tmp/empty", base)
	seedSession(t, db, sessionRow{
		id: "emptySession", sourceID: "srcEmpty", nativeID: "nE", rootID: "emptySession",
		kind: "root", agent: "nedi", status: "completed",
	})
	seedTurn(t, db, turnRow{id: "tEmpty", sessionID: "emptySession", seq: 1, startTS: base, endTS: base + 100, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "opEmpty", turnID: "tEmpty", sessionID: "emptySession", seq: 1, kind: "internal", name: "no_payload", startTS: base, status: "completed"})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/emptySession/payload_refs", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"refs":[]`) {
		t.Errorf("body should contain empty refs array, got %s", rr.Body.String())
	}
}
