package presenter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sessionDetailBody mirrors GET /api/sessions/:id.
type sessionDetailBody struct {
	Session struct {
		ID               string  `json:"id"`
		RootSessionID    string  `json:"root_session_id"`
		Kind             string  `json:"kind"`
		AgentName        string  `json:"agent_name"`
		Model            string  `json:"model"`
		Status           string  `json:"status"`
		StartTS          int64   `json:"start_ts"`
		TokensIn         int64   `json:"tokens_in"`
		TokensOut        int64   `json:"tokens_out"`
		TokensCacheRead  int64   `json:"tokens_cache_read"`
		TokensCacheWrite int64   `json:"tokens_cache_write"`
		CostUSD          float64 `json:"cost_usd"`
		TurnCount        int64   `json:"turn_count"`
		OpCount          int64   `json:"op_count"`
	} `json:"session"`
	Turns []struct {
		ID       string  `json:"id"`
		Seq      int64   `json:"seq"`
		Status   string  `json:"status"`
		TokensIn int64   `json:"tokens_in"`
		CostUSD  float64 `json:"cost_usd"`
		OpCount  int64   `json:"op_count"`
		Ops      []struct {
			ID             string  `json:"id"`
			Kind           string  `json:"kind"`
			Name           string  `json:"name"`
			Model          string  `json:"model"`
			Provider       string  `json:"provider"`
			DurationUS     *int64  `json:"duration_us"`
			Status         string  `json:"status"`
			ErrorClass     *string `json:"error_class"`
			CtxUsed        *int64  `json:"ctx_used"`
			CtxMax         *int64  `json:"ctx_max"`
			ChildSessionID *string `json:"child_session_id"`
			PayloadRefs    []struct {
				ID            int64   `json:"id"`
				Kind          string  `json:"kind"`
				Format        string  `json:"format"`
				Compression   *string `json:"compression"`
				OriginalBytes *int64  `json:"original_bytes"`
				StoredBytes   *int64  `json:"stored_bytes"`
			} `json:"payload_refs"`
		} `json:"ops"`
	} `json:"turns"`
	ChildSessions []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		AgentName string `json:"agent_name"`
		Status    string `json:"status"`
	} `json:"child_sessions"`
}

func getSessionDetail(t *testing.T, p *Presenter, id string) (int, sessionDetailBody, errorEnvelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body sessionDetailBody
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

// TestSessionDetail_HappyPath asserts the full nested shape: session +
// ordered turns + ordered ops + payload_refs + child_sessions.
func TestSessionDetail_HappyPath(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessionDetail(t, p, "rootA")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Session.ID != "rootA" || body.Session.Kind != "root" {
		t.Fatalf("session = %+v", body.Session)
	}
	if len(body.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(body.Turns))
	}
	if body.Turns[0].Seq != 1 || body.Turns[1].Seq != 2 {
		t.Fatalf("turns out of order: %d, %d", body.Turns[0].Seq, body.Turns[1].Seq)
	}
	t1 := body.Turns[0]
	if len(t1.Ops) != 3 {
		t.Fatalf("turn1 ops = %d, want 3", len(t1.Ops))
	}
	// Ops ordered by seq within the turn: o1 (llm), o2 (tool Bash), o3 (session).
	if t1.Ops[0].ID != "o1" || t1.Ops[0].Kind != "llm" {
		t.Fatalf("turn1 op0 = %+v", t1.Ops[0])
	}
	if t1.Ops[0].Model != "claude-opus-4-7" || t1.Ops[0].Provider != "anthropic" {
		t.Fatalf("op0 model/provider = %q/%q", t1.Ops[0].Model, t1.Ops[0].Provider)
	}
	if t1.Ops[0].CtxUsed == nil || *t1.Ops[0].CtxUsed != 12000 {
		t.Fatalf("op0 ctx_used = %v", t1.Ops[0].CtxUsed)
	}
	// o1 carries two payload_refs. The streaming URL is Phase 2 (no `url`
	// field is emitted yet — the /api/payloads route is unregistered), so the
	// detail view only surfaces the ref metadata.
	if len(t1.Ops[0].PayloadRefs) != 2 {
		t.Fatalf("op0 payload_refs = %d, want 2", len(t1.Ops[0].PayloadRefs))
	}
	pr := t1.Ops[0].PayloadRefs[0]
	if pr.ID == 0 {
		t.Fatalf("payload ref id = 0, want a real row id (%+v)", pr)
	}
	// o3 is a session op linking childA1.
	if t1.Ops[2].ChildSessionID == nil || *t1.Ops[2].ChildSessionID != "childA1" {
		t.Fatalf("op2 child_session_id = %v", t1.Ops[2].ChildSessionID)
	}
	// child_sessions lists both children.
	if len(body.ChildSessions) != 2 {
		t.Fatalf("child_sessions = %d, want 2", len(body.ChildSessions))
	}
}

// TestSessionDetail_ExposesCacheTokens asserts the session row carries the
// separate cache-token counters (tokens_cache_read / tokens_cache_write) so the
// Overview can render the cache breakdown + hit-rate (SOW-0029). tokens_in is
// the FRESH input only; cache is reported separately.
func TestSessionDetail_ExposesCacheTokens(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getSessionDetail(t, p, "rootA")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	s := body.Session
	if s.TokensIn != 1000 || s.TokensOut != 2000 {
		t.Fatalf("tokens in/out = %d/%d, want 1000/2000 (fresh)", s.TokensIn, s.TokensOut)
	}
	if s.TokensCacheRead != 3000 {
		t.Errorf("tokens_cache_read = %d, want 3000", s.TokensCacheRead)
	}
	if s.TokensCacheWrite != 500 {
		t.Errorf("tokens_cache_write = %d, want 500", s.TokensCacheWrite)
	}
}

// TestSessionDetail_OpsWithoutPayloadEmitEmptyArray asserts an op with no
// payload_refs serializes payload_refs as [] not null.
func TestSessionDetail_OpsWithoutPayloadEmitEmptyArray(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	_, body, _ := getSessionDetail(t, p, "rootA")
	// o2 (Bash) has no payload refs.
	var found bool
	for _, tn := range body.Turns {
		for _, op := range tn.Ops {
			if op.ID == "o2" {
				found = true
				if op.PayloadRefs == nil {
					t.Fatal("o2 payload_refs is null, want empty array")
				}
				if len(op.PayloadRefs) != 0 {
					t.Fatalf("o2 payload_refs = %d, want 0", len(op.PayloadRefs))
				}
			}
		}
	}
	if !found {
		t.Fatal("o2 not found in detail")
	}
}

// TestSessionDetail_NotFound asserts an unknown id returns 404 NOT_FOUND.
func TestSessionDetail_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, _, env := getSessionDetail(t, p, "does-not-exist")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestSessionDetail_HeadParity asserts HEAD on the detail route.
func TestSessionDetail_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	headReq := httptest.NewRequest(http.MethodHead, "/api/sessions/rootA", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headRR.Code)
	}
	if headRR.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headRR.Body.String())
	}
}

// TestSessionDetail_PathControlCharRejected pins that a control
// byte in the path :id (e.g. a%09b => "a\tb") must be a loud 400 (BAD_REQUEST)
// checked on the RAW PathValue before TrimSpace, not a silent doomed lookup
// that returns 404. Mirrors the query-value control-char rule so the path id
// is covered by the same defense-in-depth check.
func TestSessionDetail_PathControlCharRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/a%09b", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (control byte in path id)", rr.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode err: %v (raw=%q)", err, rr.Body.Bytes())
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSessionDetail_MethodNotAllowed asserts non-GET/HEAD is rejected.
func TestSessionDetail_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/rootA", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
