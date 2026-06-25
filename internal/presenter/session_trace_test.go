package presenter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// session_trace_test.go — tests for GET /api/sessions/:id/trace (SOW-0070).
// The whole-tree trace must return ops from EVERY session in the tree, each
// tagged with its owning session, so the client builds ONE merged op tree.

// traceBody mirrors GET /api/sessions/:id/trace.
type traceBody struct {
	RootID string     `json:"root_id"`
	Ops    []traceOpT `json:"ops"`
}

// traceOpT is the test mirror of presenter.traceOp (the fields the tests assert).
type traceOpT struct {
	ID             string  `json:"id"`
	TurnSeq        int64   `json:"turn_seq"`
	Kind           string  `json:"kind"`
	Name           string  `json:"name"`
	ParentOpID     *string `json:"parent_op_id"`
	ChildSessionID *string `json:"child_session_id"`
	Status         string  `json:"status"`
	ErrorMessage   *string `json:"error_message"`
	SessionID      string  `json:"session_id"`
	SessionAgent   string  `json:"session_agent_name"`
	SessionKind    string  `json:"session_kind"`
	PayloadRefs    []struct {
		ID            int64   `json:"id"`
		OpID          string  `json:"op_id"`
		Kind          string  `json:"kind"`
		ArtifactClass string  `json:"artifact_class"`
		LocationURI   *string `json:"location_uri"`
		SHA256        *string `json:"sha256"`
	} `json:"payload_refs"`
}

func getTrace(t *testing.T, p *Presenter, id string) (int, traceBody, errorEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/trace", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body traceBody
	var env errorEnvelope
	raw := rr.Body.Bytes()
	if len(raw) > 0 {
		if rr.Code >= 400 {
			_ = json.Unmarshal(raw, &env)
		} else if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v (raw=%q)", err, raw)
		}
	}
	return rr.Code, body, env
}

// TestTrace_WholeTreeReturnsAllSessionOps pins AC1: the trace returns ops from
// EVERY session in the tree, each tagged with its owning session. A root with a
// kind=session op that spawns a child, and the child carries its own llm op —
// BOTH ops appear, tagged with their respective session_id + agent.
func TestTrace_WholeTreeReturnsAllSessionOps(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTr", "aiagent_v3", "/tmp/tr", base)
	// Root session with one session-op that spawns the child.
	seedSession(t, db, sessionRow{
		id: "rootTr", sourceID: "srcTr", nativeID: "nR", rootID: "rootTr",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 2, turnCount: 1,
	})
	// Child session with its own work.
	seedSession(t, db, sessionRow{
		id: "childTr", sourceID: "srcTr", nativeID: "nC", parentID: "rootTr", rootID: "rootTr",
		kind: "sub_agent", agent: "worker", status: "completed",
		startTS: base + 2_000, endTS: base + 8_000, opCount: 1, turnCount: 1,
	})
	seedTurn(t, db, turnRow{id: "tR", sessionID: "rootTr", seq: 1, startTS: base + 1_000, endTS: base + 9_000, status: "completed", opCount: 2})
	seedTurn(t, db, turnRow{id: "tC", sessionID: "childTr", seq: 1, startTS: base + 2_000, endTS: base + 8_000, status: "completed", opCount: 1})
	// Root: a session-op linking the child (child_session_id set), + an llm op.
	seedOp(t, db, opRow{id: "rSession", turnID: "tR", sessionID: "rootTr", seq: 1, kind: "session", name: "worker", startTS: base + 1_500, endTS: base + 7_000, status: "completed", childSessionID: "childTr"})
	seedOp(t, db, opRow{id: "rLLM", turnID: "tR", sessionID: "rootTr", seq: 2, kind: "llm", name: "claude", model: "claude", provider: "anthropic", startTS: base + 1_100, endTS: base + 1_400, status: "completed", tokensIn: 10, tokensOut: 2})
	// Child: its own llm op.
	seedOp(t, db, opRow{id: "cLLM", turnID: "tC", sessionID: "childTr", seq: 1, kind: "llm", name: "haiku", model: "haiku", provider: "anthropic", startTS: base + 3_000, endTS: base + 3_500, status: "completed", tokensIn: 5, tokensOut: 1})

	code, body, _ := getTrace(t, p, "rootTr")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.RootID != "rootTr" {
		t.Fatalf("root_id = %q, want rootTr", body.RootID)
	}
	if len(body.Ops) != 3 {
		t.Fatalf("ops = %d, want 3 (root session-op + root llm + child llm)", len(body.Ops))
	}
	// Each op is tagged with its owning session + agent.
	byID := map[string]traceOpT{}
	for _, o := range body.Ops {
		byID[o.ID] = o
	}
	if byID["rLLM"].SessionID != "rootTr" || byID["rLLM"].SessionAgent != "nedi" {
		t.Errorf("rLLM tag = %+v, want rootTr/nedi", byID["rLLM"])
	}
	if byID["cLLM"].SessionID != "childTr" || byID["cLLM"].SessionAgent != "worker" {
		t.Errorf("cLLM tag = %+v, want childTr/worker (sub-session op surfaced)", byID["cLLM"])
	}
	// The session-op carries its child_session_id so the client can splice.
	if byID["rSession"].SessionID != "rootTr" {
		t.Errorf("rSession tag = %+v, want rootTr", byID["rSession"])
	}
	if byID["rSession"].ChildSessionID == nil || *byID["rSession"].ChildSessionID != "childTr" {
		t.Fatalf("rSession.child_session_id = %v, want childTr (splice point)", byID["rSession"].ChildSessionID)
	}
}

// TestTrace_ScopedToTreeViaChildID mirrors the Timeline contract: querying a
// CHILD session id resolves to the whole tree (root_session_id), so the trace
// is identical to querying the root.
func TestTrace_ScopedToTreeViaChildID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTr2", "aiagent_v3", "/tmp/tr2", base)
	seedSession(t, db, sessionRow{
		id: "rootTr2", sourceID: "srcTr2", nativeID: "nR", rootID: "rootTr2",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base, endTS: base + 1_000, opCount: 1, turnCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "childTr2", sourceID: "srcTr2", nativeID: "nC", parentID: "rootTr2", rootID: "rootTr2",
		kind: "sub_agent", agent: "worker", status: "completed",
		startTS: base + 100, endTS: base + 900, opCount: 1, turnCount: 1,
	})
	seedTurn(t, db, turnRow{id: "tR2", sessionID: "rootTr2", seq: 1, startTS: base, endTS: base + 1_000, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "rLLM2", turnID: "tR2", sessionID: "rootTr2", seq: 1, kind: "llm", name: "claude", startTS: base + 50, endTS: base + 100, status: "completed"})
	seedTurn(t, db, turnRow{id: "tC2", sessionID: "childTr2", seq: 1, startTS: base + 100, endTS: base + 900, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "cLLM2", turnID: "tC2", sessionID: "childTr2", seq: 1, kind: "llm", name: "haiku", startTS: base + 200, endTS: base + 300, status: "completed"})

	// Querying the CHILD returns the WHOLE tree (root + child op).
	code, body, _ := getTrace(t, p, "childTr2")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.RootID != "rootTr2" {
		t.Fatalf("root_id = %q, want rootTr2 (child resolves to root)", body.RootID)
	}
	if len(body.Ops) != 2 {
		t.Fatalf("ops = %d, want 2 (root + child op, whole tree)", len(body.Ops))
	}
}

// TestTrace_ErrorMessage pins AC3: error_message is surfaced on a failed op.
// (error_class is NOT carried on the trace endpoint — the trace shape is
// the slim one documented at traceOp; full error metadata is delivered
// via /api/sessions/:id when the operator clicks the failed op.)
func TestTrace_ErrorMessage(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTr3", "aiagent_v3", "/tmp/tr3", base)
	seedSession(t, db, sessionRow{
		id: "rootTr3", sourceID: "srcTr3", nativeID: "nR", rootID: "rootTr3",
		kind: "root", agent: "nedi", status: "failed",
		startTS: base, endTS: base + 1_000, opCount: 1, turnCount: 1,
	})
	seedTurn(t, db, turnRow{id: "tR3", sessionID: "rootTr3", seq: 1, startTS: base, endTS: base + 1_000, status: "failed", opCount: 1})
	seedOp(t, db, opRow{id: "failOp", turnID: "tR3", sessionID: "rootTr3", seq: 1, kind: "llm", name: "claude", startTS: base, endTS: base + 100, status: "failed", errorClass: "rate_limit"})

	// Seed an error_message directly (opRow has no error_message field).
	if _, err := db.Exec(`UPDATE ops SET error_message = ? WHERE id = ?`, "429 Too Many Requests", "failOp"); err != nil {
		t.Fatalf("seed error_message: %v", err)
	}

	code, body, _ := getTrace(t, p, "rootTr3")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Ops) != 1 || body.Ops[0].ID != "failOp" {
		t.Fatalf("ops = %+v, want [failOp]", body.Ops)
	}
	if body.Ops[0].ErrorMessage == nil || *body.Ops[0].ErrorMessage != "429 Too Many Requests" {
		t.Errorf("error_message = %v, want '429 Too Many Requests'", body.Ops[0].ErrorMessage)
	}
}

// TestTrace_NotFound: an unknown id → 404 NOT_FOUND.
func TestTrace_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	code, _, env := getTrace(t, p, "does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestTrace_EmptyTree: an op-less tree returns an empty (non-null) ops array.
func TestTrace_EmptyTree(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTr4", "aiagent_v3", "/tmp/tr4", base)
	seedSession(t, db, sessionRow{
		id: "rootTr4", sourceID: "srcTr4", nativeID: "nR", rootID: "rootTr4",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base, endTS: base + 1_000, opCount: 1,
	})

	code, body, _ := getTrace(t, p, "rootTr4")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Ops == nil {
		t.Fatal("ops must serialize as [] not null")
	}
	if len(body.Ops) != 0 {
		t.Fatalf("ops = %d, want 0 (op-less tree)", len(body.Ops))
	}
}

// TestTrace_MethodNotAllowed: non-GET/HEAD is rejected.
func TestTrace_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/x/trace", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestTrace_PayloadRefsOptIn asserts the default trace response omits
// payload_refs (saves ~30-40% response size on a typical session; the
// trace is consumed by Waterfall/FlameGraph/EventList/ByTurnWaterfall
// which never render payload previews), and that ?include=payload_refs
// opts in to the full set.
func TestTrace_PayloadRefsOptIn(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTr", "aiagent_v3", "/tmp/tr", base)
	seedSession(t, db, sessionRow{
		id: "rootTr", sourceID: "srcTr", nativeID: "nR", rootID: "rootTr",
		kind: "root", agent: "nedi", status: "completed",
	})
	seedTurn(t, db, turnRow{id: "tR", sessionID: "rootTr", seq: 1, startTS: base + 1_000, endTS: base + 9_000, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "rLLM", turnID: "tR", sessionID: "rootTr", seq: 1, kind: "llm", name: "claude", model: "claude", provider: "anthropic", startTS: base + 1_100, endTS: base + 1_400, status: "completed", tokensIn: 10, tokensOut: 2})
	if _, err := db.Exec(`INSERT INTO payload_refs (op_id, kind, format, location_uri, original_bytes, stored_bytes) VALUES (?, ?, ?, ?, ?, ?)`, "rLLM", "llm_response", "json", "file:///tmp/r.json", 100, 100); err != nil {
		t.Fatalf("seed payload_ref: %v", err)
	}

	// Default: payload_refs field is OMITTED from each op's JSON.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootTr/trace", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	raw := rr.Body.Bytes()
	if !bytes.Contains(raw, []byte(`"id":"rLLM"`)) {
		t.Fatalf("default response missing rLLM op: %s", raw)
	}
	// payload_refs MUST be absent from the default response.
	if bytes.Contains(raw, []byte(`"payload_refs"`)) {
		t.Errorf("default response should NOT contain payload_refs, got: %s", raw)
	}

	// Opt-in: payload_refs field is present per op, but proof-only fields
	// remain omitted until include=proof is also requested.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/rootTr/trace?include=payload_refs", nil)
	rr2 := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("opt-in status = %d, want 200", rr2.Code)
	}
	raw2 := rr2.Body.Bytes()
	if !bytes.Contains(raw2, []byte(`"payload_refs"`)) {
		t.Errorf("opt-in response should contain payload_refs, got: %s", raw2)
	}
	for _, forbidden := range [][]byte{[]byte(`"location_uri"`), []byte(`"sha256"`)} {
		if bytes.Contains(raw2, forbidden) {
			t.Fatalf("include=payload_refs leaked proof field %s: %s", forbidden, raw2)
		}
	}
}

func TestTrace_IncludeProofAddsProofMetadata(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcTrProof", "aiagent_v3", "/tmp/tr-proof", base)
	seedSession(t, db, sessionRow{
		id: "rootTrProof", sourceID: "srcTrProof", nativeID: "nR", rootID: "rootTrProof",
		kind: "root", agent: "nedi", status: "completed",
	})
	seedTurn(t, db, turnRow{id: "tTrProof", sessionID: "rootTrProof", seq: 1, startTS: base, status: "completed", opCount: 1})
	seedOp(t, db, opRow{id: "opTrProof", turnID: "tTrProof", sessionID: "rootTrProof", seq: 1, kind: "llm", name: "sdk", startTS: base, status: "completed"})
	sha := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	seedPayload(t, db, payloadRow{
		opID: "opTrProof", kind: "sdk_response", format: "json",
		locationURI: "file:///tmp/tr-proof/sdk-response.json",
		sha256:      sha, originalBytes: 120, storedBytes: 120,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootTrProof/trace?include=payload_refs,proof", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body traceBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode trace: %v (body=%s)", err, rr.Body.String())
	}
	if len(body.Ops) != 1 || len(body.Ops[0].PayloadRefs) != 1 {
		t.Fatalf("payload refs = %+v, want one ref", body.Ops)
	}
	ref := body.Ops[0].PayloadRefs[0]
	if ref.OpID != "opTrProof" {
		t.Fatalf("payload op_id = %q, want opTrProof", ref.OpID)
	}
	if ref.Kind != "sdk_response" || ref.ArtifactClass != "llm_sdk_response" {
		t.Fatalf("payload class = %q/%q, want sdk_response/llm_sdk_response", ref.Kind, ref.ArtifactClass)
	}
	if ref.LocationURI == nil || *ref.LocationURI != "file:///tmp/tr-proof/sdk-response.json" {
		t.Fatalf("location_uri = %v, want proof URI", ref.LocationURI)
	}
	if ref.SHA256 == nil || *ref.SHA256 != sha {
		t.Fatalf("sha256 = %v, want %s", ref.SHA256, sha)
	}
}

func TestTrace_IncludeProofRequiresPayloadRefs(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedGraph(t, db, seedBase())

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootA/trace?include=proof", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
