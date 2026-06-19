package presenter

import (
	"encoding/json"
	"fmt"
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
			ParentOpID     *string `json:"parent_op_id"`
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
	ChildSessions []childDetailJSON `json:"child_sessions"`
}

// childDetailJSON is the recursive test mirror of presenter.childSummary: a
// child node carries its own nested child_sessions (SOW-0069), so the test can
// assert the full execution tree (parent → children → grandchildren).
type childDetailJSON struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	AgentName     string            `json:"agent_name"`
	Status        string            `json:"status"`
	ChildSessions []childDetailJSON `json:"child_sessions"`
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

// TestSessionDetail_ExposesParentOpID asserts each op row carries
// `parent_op_id` so the Trace tab can build the authoritative span tree from
// the stored ops.parent_op_id column: a child op returns its parent's id, a
// top-level op returns null (SOW-0006). Without this the frontend cannot
// reconstruct the parent/child nesting that the ingest writer already records.
func TestSessionDetail_ExposesParentOpID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()

	seedSource(t, db, "srcP", "aiagent_v3", "/tmp/p", base)
	seedSession(t, db, sessionRow{
		id: "rootP", sourceID: "srcP", nativeID: "nP", rootID: "rootP",
		kind: "root", agent: "nedi", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 9_000,
		turnCount: 1, opCount: 2, failureCount: 0,
	})
	seedTurn(t, db, turnRow{
		id: "tp1", sessionID: "rootP", seq: 1, startTS: base + 1_000, endTS: base + 9_000,
		status: "completed", opCount: 2,
	})
	// parentOp is top-level (parentOpID ""); childOp nests under it via
	// parent_op_id = parentOp.id.
	seedOp(t, db, opRow{
		id: "parentOp", turnID: "tp1", sessionID: "rootP", seq: 1, kind: "llm",
		name: "claude-opus-4-7", model: "claude-opus-4-7", provider: "anthropic",
		startTS: base + 1_100, endTS: base + 2_100, durationUS: 1_000, status: "completed",
	})
	seedOp(t, db, opRow{
		id: "childOp", turnID: "tp1", sessionID: "rootP", parentOpID: "parentOp", seq: 2,
		kind: "tool", name: "Bash", toolNamespace: "shell",
		startTS: base + 2_200, endTS: base + 2_500, durationUS: 300, status: "completed",
	})

	code, body, _ := getSessionDetail(t, p, "rootP")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Turns) != 1 || len(body.Turns[0].Ops) != 2 {
		t.Fatalf("want 1 turn with 2 ops, got %+v", body.Turns)
	}
	ops := body.Turns[0].Ops
	var parent, child *struct {
		ID             string  `json:"id"`
		Kind           string  `json:"kind"`
		Name           string  `json:"name"`
		Model          string  `json:"model"`
		Provider       string  `json:"provider"`
		ParentOpID     *string `json:"parent_op_id"`
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
	}
	for i := range ops {
		switch ops[i].ID {
		case "parentOp":
			parent = &ops[i]
		case "childOp":
			child = &ops[i]
		}
	}
	if parent == nil || child == nil {
		t.Fatalf("parent/child op not found in %+v", ops)
	}
	// Top-level op: parent_op_id is null.
	if parent.ParentOpID != nil {
		t.Fatalf("parentOp parent_op_id = %v, want null (top-level op)", *parent.ParentOpID)
	}
	// Child op: parent_op_id equals the parent op's id.
	if child.ParentOpID == nil {
		t.Fatal("childOp parent_op_id is null, want parentOp's id")
	}
	if *child.ParentOpID != "parentOp" {
		t.Fatalf("childOp parent_op_id = %q, want %q", *child.ParentOpID, "parentOp")
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

// TestSessionDetail_ChildTreeIsNested pins SOW-0069: child_sessions is the FULL
// descendant tree, nested. A root with a child that itself has a grandchild
// returns child_sessions[0].child_sessions populated with the grandchild. The
// fixture is self-contained (a 3-level tree) so the shared seedGraph is
// untouched.
func TestSessionDetail_ChildTreeIsNested(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcT", "aiagent_v3", "/tmp/t", base)
	seedSession(t, db, sessionRow{
		id: "rootT", sourceID: "srcT", nativeID: "nT", rootID: "rootT",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "childT", sourceID: "srcT", nativeID: "nC", parentID: "rootT", rootID: "rootT",
		kind: "sub_agent", agent: "worker", status: "completed",
		startTS: base + 2_000, endTS: base + 8_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "grandT", sourceID: "srcT", nativeID: "nG", parentID: "childT", rootID: "rootT",
		kind: "sub_agent", agent: "sub-worker", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	})

	code, body, _ := getSessionDetail(t, p, "rootT")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.ChildSessions) != 1 || body.ChildSessions[0].ID != "childT" {
		t.Fatalf("child_sessions = %+v, want [childT]", body.ChildSessions)
	}
	child := body.ChildSessions[0]
	if len(child.ChildSessions) != 1 || child.ChildSessions[0].ID != "grandT" {
		t.Fatalf("childT.child_sessions = %+v, want [grandT] (nested tree, SOW-0069)", child.ChildSessions)
	}
}

// TestSessionDetail_ChildTreeBranching pins SOW-0069 for a branching tree (not
// just a linear chain): a root with two children, one of which has two of its
// own children. Asserts both roots render AND the nested grandchildren attach
// to the correct parent (not the sibling).
func TestSessionDetail_ChildTreeBranching(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcB", "aiagent_v3", "/tmp/b", base)
	seedSession(t, db, sessionRow{
		id: "rootB", sourceID: "srcB", nativeID: "nRB", rootID: "rootB",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	})
	// Two direct children of rootB.
	seedSession(t, db, sessionRow{
		id: "A", sourceID: "srcB", nativeID: "nA", parentID: "rootB", rootID: "rootB",
		kind: "sub_agent", agent: "alpha", status: "completed",
		startTS: base + 2_000, endTS: base + 8_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "B", sourceID: "srcB", nativeID: "nB", parentID: "rootB", rootID: "rootB",
		kind: "sub_agent", agent: "bravo", status: "completed",
		startTS: base + 2_500, endTS: base + 7_000, opCount: 1,
	})
	// A has two children; B is a leaf.
	seedSession(t, db, sessionRow{
		id: "C", sourceID: "srcB", nativeID: "nC", parentID: "A", rootID: "rootB",
		kind: "sub_agent", agent: "charlie", status: "completed",
		startTS: base + 3_000, endTS: base + 5_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "D", sourceID: "srcB", nativeID: "nD", parentID: "A", rootID: "rootB",
		kind: "sub_agent", agent: "delta", status: "completed",
		startTS: base + 3_500, endTS: base + 4_500, opCount: 1,
	})

	code, body, _ := getSessionDetail(t, p, "rootB")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Two roots: A and B (ordered by start_ts).
	if len(body.ChildSessions) != 2 {
		t.Fatalf("roots = %d, want 2 (A, B)", len(body.ChildSessions))
	}
	if body.ChildSessions[0].ID != "A" || body.ChildSessions[1].ID != "B" {
		t.Fatalf("root order = %s,%s want A,B", body.ChildSessions[0].ID, body.ChildSessions[1].ID)
	}
	// A's children are C and D; B is a leaf (no nested child_sessions).
	if len(body.ChildSessions[0].ChildSessions) != 2 {
		t.Fatalf("A.child_sessions = %d, want 2 (C, D)", len(body.ChildSessions[0].ChildSessions))
	}
	if len(body.ChildSessions[1].ChildSessions) != 0 {
		t.Fatalf("B.child_sessions = %d, want 0 (leaf)", len(body.ChildSessions[1].ChildSessions))
	}
}

// TestSessionDetail_ChildTreeCycleGuard pins SOW-0069's `c.ID == id` defense:
// a malformed parent_session_id cycle that routes the queried id back into its
// OWN descendant set must NOT surface the id as its own child. Seed a 3-node
// cycle rootCyc -> A -> B -> rootCyc (rootCyc.parent_session_id = B). Without
// the guard, rootCyc would reappear nested under B; with it, the response is
// the acyclic remainder (A -> B) and rootCyc is absent.
func TestSessionDetail_ChildTreeCycleGuard(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcCy", "aiagent_v3", "/tmp/cy", base)
	// rootCyc's OWN parent_session_id points at B (malformed cycle). The cycle
	// is circular on the FK (rootCyc -> B -> A -> rootCyc), so insert rootCyc
	// parent-less first, then A and B, then complete the cycle via UPDATE (each
	// step keeps the FK satisfied).
	seedSession(t, db, sessionRow{
		id: "rootCyc", sourceID: "srcCy", nativeID: "nRC", rootID: "rootCyc",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base + 1_000, endTS: base + 9_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "A", sourceID: "srcCy", nativeID: "nA", parentID: "rootCyc", rootID: "rootCyc",
		kind: "sub_agent", agent: "alpha", status: "completed",
		startTS: base + 2_000, endTS: base + 8_000, opCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "B", sourceID: "srcCy", nativeID: "nB", parentID: "A", rootID: "rootCyc",
		kind: "sub_agent", agent: "bravo", status: "completed",
		startTS: base + 3_000, endTS: base + 7_000, opCount: 1,
	})
	if _, err := db.Exec(`UPDATE sessions SET parent_session_id = 'B' WHERE id = 'rootCyc'`); err != nil {
		t.Fatalf("complete cycle: %v", err)
	}

	code, body, _ := getSessionDetail(t, p, "rootCyc")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// The acyclic remainder: one root (A) with one child (B). rootCyc must NOT
	// appear anywhere in its own child tree.
	if len(body.ChildSessions) != 1 || body.ChildSessions[0].ID != "A" {
		t.Fatalf("roots = %+v, want [A] (rootCyc must be skipped by the cycle guard)", body.ChildSessions)
	}
	if len(body.ChildSessions[0].ChildSessions) != 1 || body.ChildSessions[0].ChildSessions[0].ID != "B" {
		t.Fatalf("A.child_sessions = %+v, want [B]", body.ChildSessions[0].ChildSessions)
	}
	// Walk the whole tree and assert rootCyc never appears.
	var assertAbsent func(nodes []childDetailJSON)
	assertAbsent = func(nodes []childDetailJSON) {
		for _, n := range nodes {
			if n.ID == "rootCyc" {
				t.Fatalf("rootCyc surfaced in its own child tree (cycle guard failed): %+v", n)
			}
			assertAbsent(n.ChildSessions)
		}
	}
	assertAbsent(body.ChildSessions)
}

// TestSessionDetail_ChildTreeDepthCap pins the cycle defense-in-depth: a parent
// cycle deeper than childTreeMaxDepth is truncated (the CTE's depth cap), so a
// malformed cycle cannot yield an unbounded nested payload. A real acyclic tree
// at the cap depth still resolves.
func TestSessionDetail_ChildTreeDepthCap(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "srcD", "aiagent_v3", "/tmp/d", base)
	seedSession(t, db, sessionRow{
		id: "rootD", sourceID: "srcD", nativeID: "nRD", rootID: "rootD",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base, endTS: base + 1_000, opCount: 1,
	})
	// Build a chain rootD -> c1 -> c2 -> ... -> c(childTreeMaxDepth+2), deeper
	// than the cap. The cap must truncate (no panic, bounded payload).
	prev := "rootD"
	for i := 1; i <= childTreeMaxDepth+2; i++ {
		id := fmt.Sprintf("c%d", i)
		seedSession(t, db, sessionRow{
			id: id, sourceID: "srcD", nativeID: id, parentID: prev, rootID: "rootD",
			kind: "sub_agent", agent: "chain", status: "completed",
			startTS: base + int64(i)*1_000, endTS: base + int64(i)*1_000 + 500, opCount: 1,
		})
		prev = id
	}

	code, body, _ := getSessionDetail(t, p, "rootD")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// The depth cap truncates; assert the tree is bounded by walking to the
	// deepest leaf and confirming it does not exceed childTreeMaxDepth levels.
	depth := 0
	cur := body.ChildSessions
	for len(cur) > 0 {
		depth++
		if depth > childTreeMaxDepth+5 {
			t.Fatalf("tree depth unbounded (exceeded cap by > 5): cycle defense failed")
		}
		cur = cur[0].ChildSessions
	}
	if depth > childTreeMaxDepth {
		t.Fatalf("tree depth = %d, want <= cap %d", depth, childTreeMaxDepth)
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
