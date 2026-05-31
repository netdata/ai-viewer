package presenter

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// topologyBody mirrors GET /api/sessions/:id/topology.
type topologyBody struct {
	Nodes []struct {
		ID           string  `json:"id"`
		Kind         string  `json:"kind"`
		Label        string  `json:"label"`
		SizeMetric   float64 `json:"size_metric"`
		FailureRatio float64 `json:"failure_ratio"`
	} `json:"nodes"`
	Edges []struct {
		Source  string `json:"source"`
		Target  string `json:"target"`
		Calls   int64  `json:"calls"`
		TotalUS int64  `json:"total_us"`
	} `json:"edges"`
	MaxSizeMetric float64 `json:"max_size_metric"`
}

// seedTopoTree seeds a two-session tree used by the topology and timeline
// tests: rootA (agent "nedi") with five ops across one turn — an llm op
// (o1, ctx 12000/200000), a completed shell tool (o2 Bash), a session op
// (o3) spawning childA1, a compaction op (o5), and a failed fs tool (o4
// Read) — plus childA1 (agent "worker") with one completed shell tool op
// (oc1 Bash). The shared "shell.Bash" tool exercises cross-session tool
// aggregation; o4's failure exercises failure_ratio; o5 exercises the
// compaction span. base is the wall-clock anchor in µs.
func seedTopoTree(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)

	seedSession(t, db, sessionRow{
		id: "rootA", sourceID: "src1", nativeID: "nA", rootID: "rootA",
		kind: "root", agent: "nedi", model: "claude-opus-4-7", provider: "anthropic",
		status: "completed", startTS: base + 1_000, endTS: base + 9_000,
		tokensIn: 1000, tokensOut: 2000, costUSD: 0.30,
		turnCount: 1, opCount: 5, failureCount: 1,
	})
	seedSession(t, db, sessionRow{
		id: "childA1", sourceID: "src1", nativeID: "nC1", parentID: "rootA", rootID: "rootA",
		kind: "sub_agent", agent: "worker", model: "claude-haiku", provider: "anthropic",
		status: "completed", startTS: base + 2_000, endTS: base + 4_000,
		tokensIn: 100, tokensOut: 200, costUSD: 0.02, turnCount: 1, opCount: 1, failureCount: 0,
	})

	seedTurn(t, db, turnRow{
		id: "t1", sessionID: "rootA", seq: 1, startTS: base + 1_000, endTS: base + 9_000,
		status: "completed", tokensIn: 1000, tokensOut: 2000, costUSD: 0.30, opCount: 5,
	})
	seedTurn(t, db, turnRow{
		id: "tc1", sessionID: "childA1", seq: 1, startTS: base + 2_100, endTS: base + 3_900,
		status: "completed", tokensIn: 100, tokensOut: 200, costUSD: 0.02, opCount: 1,
	})

	seedOp(t, db, opRow{
		id: "o1", turnID: "t1", sessionID: "rootA", seq: 1, kind: "llm", name: "claude-opus-4-7",
		model: "claude-opus-4-7", provider: "anthropic", startTS: base + 1_100, endTS: base + 2_100,
		durationUS: 1_000, status: "completed", tokensIn: 500, tokensOut: 1000, costUSD: 0.15,
		ctxUsed: 12000, ctxMax: 200000,
	})
	seedOp(t, db, opRow{
		id: "o2", turnID: "t1", sessionID: "rootA", seq: 2, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 2_200, endTS: base + 2_500, durationUS: 300,
		status: "completed", costUSD: 0.01,
	})
	seedOp(t, db, opRow{
		id: "o3", turnID: "t1", sessionID: "rootA", seq: 3, kind: "session", name: "worker",
		startTS: base + 2_000, endTS: base + 4_000, durationUS: 2_000, status: "completed",
		childSessionID: "childA1",
	})
	seedOp(t, db, opRow{
		id: "o5", turnID: "t1", sessionID: "rootA", seq: 4, kind: "compaction", name: "auto",
		startTS: base + 4_500, endTS: base + 5_000, durationUS: 500, status: "completed",
	})
	seedOp(t, db, opRow{
		id: "o4", turnID: "t1", sessionID: "rootA", seq: 5, kind: "tool", name: "Read",
		toolNamespace: "fs", startTS: base + 5_100, endTS: base + 5_200, durationUS: 100,
		status: "failed", errorClass: "io_error", costUSD: 0.01,
	})
	seedOp(t, db, opRow{
		id: "oc1", turnID: "tc1", sessionID: "childA1", seq: 1, kind: "tool", name: "Bash",
		toolNamespace: "shell", startTS: base + 2_600, endTS: base + 2_800, durationUS: 200,
		status: "completed", tokensIn: 10, tokensOut: 20, costUSD: 0.005,
	})
}

func getTopology(t *testing.T, p *Presenter, id, query string) (int, topologyBody, errorEnvelope) {
	t.Helper()
	url := "/api/sessions/" + id + "/topology"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body topologyBody
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

// nodeByID returns the node with the given id, or fails the test.
func nodeByID(t *testing.T, body topologyBody, id string) struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	SizeMetric   float64 `json:"size_metric"`
	FailureRatio float64 `json:"failure_ratio"`
} {
	t.Helper()
	for _, n := range body.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not found in %+v", id, body.Nodes)
	return body.Nodes[0] // unreachable
}

// hasEdge reports whether an aggregated edge source→target exists with the
// expected calls + total_us.
func hasEdge(body topologyBody, source, target string, calls, totalUS int64) bool {
	for _, e := range body.Edges {
		if e.Source == source && e.Target == target {
			return e.Calls == calls && e.TotalUS == totalUS
		}
	}
	return false
}

// TestTopology_NodesEdges asserts the default-metric (duration) graph over
// the whole session tree: agent nodes for rootA + childA1, tool nodes for
// the two distinct tools, and the four aggregated edges. Tool aggregation
// across sessions (shell.Bash from both rootA and childA1) and
// failure_ratio (fs.Read 1/1, rootA 1/5) are pinned here.
func TestTopology_NodesEdges(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, body, _ := getTopology(t, p, "rootA", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// 2 agents + 2 tools.
	if len(body.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (%+v)", len(body.Nodes), body.Nodes)
	}

	root := nodeByID(t, body, "agent:rootA")
	if root.Kind != "agent" || root.Label != "nedi (root)" {
		t.Fatalf("root node = %+v", root)
	}
	// duration sum over rootA's five ops: 1000+300+2000+500+100 = 3900.
	if root.SizeMetric != 3900 {
		t.Fatalf("root size_metric = %v, want 3900", root.SizeMetric)
	}
	if root.FailureRatio != 1.0/5.0 {
		t.Fatalf("root failure_ratio = %v, want 0.2", root.FailureRatio)
	}

	child := nodeByID(t, body, "agent:childA1")
	if child.Label != "worker" || child.SizeMetric != 200 || child.FailureRatio != 0 {
		t.Fatalf("child node = %+v", child)
	}

	bash := nodeByID(t, body, "tool:shell.Bash")
	if bash.Kind != "tool" || bash.Label != "shell.Bash" {
		t.Fatalf("bash node = %+v", bash)
	}
	// shell.Bash duration across both sessions: 300 (o2) + 200 (oc1) = 500.
	if bash.SizeMetric != 500 || bash.FailureRatio != 0 {
		t.Fatalf("bash size/failure = %v/%v, want 500/0", bash.SizeMetric, bash.FailureRatio)
	}

	read := nodeByID(t, body, "tool:fs.Read")
	if read.SizeMetric != 100 || read.FailureRatio != 1 {
		t.Fatalf("read size/failure = %v/%v, want 100/1", read.SizeMetric, read.FailureRatio)
	}

	if body.MaxSizeMetric != 3900 {
		t.Fatalf("max_size_metric = %v, want 3900", body.MaxSizeMetric)
	}

	// Edges: rootA→Bash(o2), rootA→Read(o4), rootA→childA1(o3 session),
	// childA1→Bash(oc1).
	if len(body.Edges) != 4 {
		t.Fatalf("edges = %d, want 4 (%+v)", len(body.Edges), body.Edges)
	}
	if !hasEdge(body, "agent:rootA", "tool:shell.Bash", 1, 300) {
		t.Fatalf("missing rootA→Bash edge: %+v", body.Edges)
	}
	if !hasEdge(body, "agent:rootA", "tool:fs.Read", 1, 100) {
		t.Fatalf("missing rootA→Read edge: %+v", body.Edges)
	}
	if !hasEdge(body, "agent:rootA", "agent:childA1", 1, 2000) {
		t.Fatalf("missing rootA→childA1 spawn edge: %+v", body.Edges)
	}
	if !hasEdge(body, "agent:childA1", "tool:shell.Bash", 1, 200) {
		t.Fatalf("missing childA1→Bash edge: %+v", body.Edges)
	}
}

// TestTopology_MetricVariants asserts each ?metric= changes the agent
// node's size_metric (and max_size_metric) per the contract.
func TestTopology_MetricVariants(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	cases := []struct {
		metric   string
		wantRoot float64
	}{
		// cost: 0.15 + 0.01 + 0 (session) + 0 (compaction) + 0.01 = 0.17.
		{"cost", 0.17},
		// tokens: o1 1500 + others 0 = 1500.
		{"tokens", 1500},
		// duration: 3900 (default).
		{"duration", 3900},
		// calls: 5 ops on rootA.
		{"calls", 5},
		// ctx_pct: max(12000/200000) = 0.06.
		{"ctx_pct", 0.06},
	}
	for _, tc := range cases {
		t.Run(tc.metric, func(t *testing.T) {
			code, body, env := getTopology(t, p, "rootA", "metric="+tc.metric)
			if code != http.StatusOK {
				t.Fatalf("status = %d, env=%+v", code, env)
			}
			root := nodeByID(t, body, "agent:rootA")
			if !floatEq(root.SizeMetric, tc.wantRoot) {
				t.Fatalf("metric=%s root size_metric = %v, want %v", tc.metric, root.SizeMetric, tc.wantRoot)
			}
		})
	}
}

// TestTopology_DefaultMetricIsDuration asserts an absent ?metric= behaves
// identically to ?metric=duration.
func TestTopology_DefaultMetricIsDuration(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	_, defBody, _ := getTopology(t, p, "rootA", "")
	_, durBody, _ := getTopology(t, p, "rootA", "metric=duration")
	if nodeByID(t, defBody, "agent:rootA").SizeMetric != nodeByID(t, durBody, "agent:rootA").SizeMetric {
		t.Fatal("default metric != duration")
	}
}

// TestTopology_UnknownMetricRejected asserts an unknown ?metric= is a 400
// BAD_REQUEST before any query runs.
func TestTopology_UnknownMetricRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, _, env := getTopology(t, p, "rootA", "metric=bananas")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestTopology_ScopedToTreeViaChildID asserts requesting the topology by a
// CHILD session id returns the same whole-tree graph (the handler resolves
// to root_session_id), not just the child's actors.
func TestTopology_ScopedToTreeViaChildID(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, body, _ := getTopology(t, p, "childA1", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Both agents must be present even though we asked for the child id.
	_ = nodeByID(t, body, "agent:rootA")
	_ = nodeByID(t, body, "agent:childA1")
}

// TestTopology_NotFound asserts an unknown id is 404 NOT_FOUND.
func TestTopology_NotFound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	code, _, env := getTopology(t, p, "does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if env.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeNotFound)
	}
}

// TestTopology_HeadParity asserts HEAD returns 200 + empty body for a
// known session.
func TestTopology_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodHead, "/api/sessions/rootA/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
}

// TestTopology_MethodNotAllowed asserts a non-GET/HEAD method is 405.
func TestTopology_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/rootA/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestTopology_ControlCharRejected asserts a control byte in :id is a loud
// 400 (checked on the raw path value before TrimSpace), mirroring the
// detail route.
func TestTopology_ControlCharRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedTopoTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/a%09b/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestTopology_EmptyNodesSerializeAsArrays asserts a session with no ops
// still returns its agent node and empty (non-null) edges. Uses a
// standalone single-session tree.
func TestTopology_EmptyNodesSerializeAsArrays(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "solo", sourceID: "src1", nativeID: "nS", rootID: "solo",
		kind: "root", agent: "lonely", status: "completed",
		startTS: base + 1_000, endTS: base + 2_000,
	})

	code, body, _ := getTopology(t, p, "solo", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (the agent)", len(body.Nodes))
	}
	if body.Nodes[0].ID != "agent:solo" || body.Nodes[0].SizeMetric != 0 {
		t.Fatalf("solo node = %+v", body.Nodes[0])
	}
	if body.Edges == nil {
		t.Fatal("edges is null, want empty array")
	}
	if body.MaxSizeMetric != 0 {
		t.Fatalf("max_size_metric = %v, want 0", body.MaxSizeMetric)
	}
}

// TestTopology_ToolWithoutNamespace asserts a tool op with a NULL
// tool_namespace yields a tool node keyed by the bare name (no leading
// dot).
func TestTopology_ToolWithoutNamespace(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSession(t, db, sessionRow{
		id: "solo", sourceID: "src1", nativeID: "nS", rootID: "solo",
		kind: "root", agent: "nedi", status: "completed",
		startTS: base + 1_000, endTS: base + 2_000, opCount: 1,
	})
	seedTurn(t, db, turnRow{id: "st", sessionID: "solo", seq: 1, startTS: base + 1_000, status: "completed", opCount: 1})
	// Tool op with no namespace (toolNamespace "" => NULL in seedOp).
	seedOp(t, db, opRow{
		id: "so1", turnID: "st", sessionID: "solo", seq: 1, kind: "tool", name: "raw_tool",
		startTS: base + 1_100, endTS: base + 1_300, durationUS: 200, status: "completed",
	})

	code, body, _ := getTopology(t, p, "solo", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	tool := nodeByID(t, body, "tool:raw_tool")
	if tool.Label != "raw_tool" {
		t.Fatalf("bare-name tool label = %q, want 'raw_tool'", tool.Label)
	}
}

// TestTopology_DBFailureReturns503 asserts a query error (DB closed before
// the request) surfaces as DB_UNAVAILABLE rather than a partial body —
// exercises the writeDBError branch in the topology build path.
func TestTopology_DBFailureReturns503(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/rootA/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeDBUnavailable)
	}
}

// floatEq compares two float64 within a small epsilon (cost/ctx_pct are
// fractional).
func floatEq(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
