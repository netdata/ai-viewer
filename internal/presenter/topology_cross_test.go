package presenter

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Cross-session topology (GET /api/topology) tests. The endpoint reuses the
// per-session node/edge model but scopes to the active /api/sessions filter:
// one agent node per matching session, NO tool nodes, lineage edges
// (parent_session_id, which subsumes forks in the canonical schema) among the
// matched set, a size_metric from the session's own stored aggregates, and a
// top-N node cap with a top-level "truncated" flag. The error envelope helper
// (errorEnvelope) is shared with the other presenter tests.

// crossTopologyBody mirrors GET /api/topology: the per-session topology shape
// plus the optional top-level "truncated" flag the node cap sets.
type crossTopologyBody struct {
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
	Truncated     bool    `json:"truncated"`
}

// getCrossTopology issues GET /api/topology?<query> and decodes the body or
// the error envelope, mirroring getTopology for the per-session route.
func getCrossTopology(t *testing.T, p *Presenter, query string) (int, crossTopologyBody, errorEnvelope) {
	t.Helper()
	url := "/api/topology"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body crossTopologyBody
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

// crossNode returns the node with the given id, or fails the test.
func crossNode(t *testing.T, body crossTopologyBody, id string) struct {
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

// crossHasEdge reports whether an edge source→target exists with the expected
// calls + total_us.
func crossHasEdge(body crossTopologyBody, source, target string, calls, totalUS int64) bool {
	for _, e := range body.Edges {
		if e.Source == source && e.Target == target {
			return e.Calls == calls && e.TotalUS == totalUS
		}
	}
	return false
}

// seedCrossTree seeds a multi-root cross-session fixture:
//
//   - rootA (nedi, completed): parent of childA1 (worker, completed); rootA has
//     5 ops (1 failed), childA1 has 1 op.
//   - rootB (codex, completed): parent of forkB1 (kind='fork', completed) — the
//     fork's parent_session_id points at rootB, which is how the canonical
//     schema models a fork (no separate forked_from_id column). rootB has 2 ops,
//     forkB1 has 0 ops.
//
// All four sessions start before the injected now() so the default time filter
// includes them. base is the wall-clock anchor in µs.
func seedCrossTree(t *testing.T, db *sql.DB, base int64) {
	t.Helper()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	seedSource(t, db, "src2", "codex", "/tmp/b", base)

	// Tree A: rootA → childA1.
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

	// Tree B: rootB → forkB1 (a fork; parent_session_id = rootB).
	seedSession(t, db, sessionRow{
		id: "rootB", sourceID: "src2", nativeID: "nB", rootID: "rootB",
		kind: "root", agent: "codex", model: "gpt-5", provider: "openai",
		status: "completed", startTS: base + 3_000, endTS: base + 7_000,
		tokensIn: 400, tokensOut: 600, costUSD: 0.10,
		turnCount: 1, opCount: 2, failureCount: 0,
	})
	seedSession(t, db, sessionRow{
		id: "forkB1", sourceID: "src2", nativeID: "nFB1", parentID: "rootB", rootID: "rootB",
		kind: "fork", agent: "codex", model: "gpt-5", provider: "openai",
		status: "completed", startTS: base + 3_500, endTS: base + 6_000,
		tokensIn: 50, tokensOut: 70, costUSD: 0.03, turnCount: 0, opCount: 0, failureCount: 0,
	})
}

// TestCrossTopology_NodesAndLineageEdges asserts the default (duration) graph
// over the whole filtered set (group=all): one agent node per session, NO tool
// nodes, and lineage edges (parent_session_id, which subsumes forks) with
// calls=1, total_us=0.
func TestCrossTopology_NodesAndLineageEdges(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	code, body, env := getCrossTopology(t, p, "group=all")
	if code != http.StatusOK {
		t.Fatalf("status = %d, env=%+v", code, env)
	}
	// 4 agent nodes, zero tool nodes (cross-session view has no tools).
	if len(body.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (%+v)", len(body.Nodes), body.Nodes)
	}
	for _, n := range body.Nodes {
		if n.Kind != "agent" {
			t.Fatalf("node %q kind = %q, want agent (no tool nodes cross-session)", n.ID, n.Kind)
		}
	}

	rootA := crossNode(t, body, "agent:rootA")
	if rootA.Label != "nedi (root)" {
		t.Fatalf("rootA label = %q, want 'nedi (root)'", rootA.Label)
	}
	// duration default = end_ts - start_ts = 9000 - 1000 = 8000.
	if rootA.SizeMetric != 8000 {
		t.Fatalf("rootA size_metric = %v, want 8000 (end-start)", rootA.SizeMetric)
	}
	// failure_ratio = failure_count/op_count = 1/5.
	if rootA.FailureRatio != 1.0/5.0 {
		t.Fatalf("rootA failure_ratio = %v, want 0.2", rootA.FailureRatio)
	}

	child := crossNode(t, body, "agent:childA1")
	if child.Label != "worker" || child.SizeMetric != 2000 {
		t.Fatalf("childA1 node = %+v, want label worker size 2000", child)
	}

	fork := crossNode(t, body, "agent:forkB1")
	// A fork's parent is rootB, NOT itself, so no " (root)" suffix.
	if fork.Label != "codex" {
		t.Fatalf("forkB1 label = %q, want 'codex' (no root suffix)", fork.Label)
	}

	// max_size_metric is the max duration: rootA 8000 is the largest.
	if body.MaxSizeMetric != 8000 {
		t.Fatalf("max_size_metric = %v, want 8000", body.MaxSizeMetric)
	}

	// Lineage edges: rootA→childA1 and rootB→forkB1, both structural
	// (calls=1, total_us=0).
	if len(body.Edges) != 2 {
		t.Fatalf("edges = %d, want 2 (%+v)", len(body.Edges), body.Edges)
	}
	if !crossHasEdge(body, "agent:rootA", "agent:childA1", 1, 0) {
		t.Fatalf("missing rootA→childA1 lineage edge: %+v", body.Edges)
	}
	if !crossHasEdge(body, "agent:rootB", "agent:forkB1", 1, 0) {
		t.Fatalf("missing rootB→forkB1 fork lineage edge: %+v", body.Edges)
	}
	if body.Truncated {
		t.Fatal("truncated = true, want false (4 nodes < cap)")
	}
}

// TestCrossTopology_DefaultSpansAllSessions asserts the cross-session topology
// spans ALL sessions (roots + sub_agents + forks) by DEFAULT — no `group`
// param needed — because the whole point of this view is lineage. With the
// /api/sessions roots-only default leaking in, the child/fork endpoints would
// be absent and every lineage edge would be dropped (disconnected root dots);
// this test pins that the default now includes the children and renders the
// lineage edges (SOW-0006).
func TestCrossTopology_DefaultSpansAllSessions(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	// No group param: must behave like group=all for this endpoint.
	code, body, env := getCrossTopology(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, env=%+v", code, env)
	}
	// All four sessions are nodes (roots + child + fork), zero tool nodes.
	if len(body.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (all sessions by default) (%+v)", len(body.Nodes), body.Nodes)
	}
	_ = crossNode(t, body, "agent:rootA")
	_ = crossNode(t, body, "agent:childA1")
	_ = crossNode(t, body, "agent:rootB")
	_ = crossNode(t, body, "agent:forkB1")
	// Both lineage edges render by default: rootA→childA1 (sub_agent) and
	// rootB→forkB1 (fork), each structural (calls=1, total_us=0).
	if len(body.Edges) != 2 {
		t.Fatalf("edges = %d, want 2 (lineage by default) (%+v)", len(body.Edges), body.Edges)
	}
	if !crossHasEdge(body, "agent:rootA", "agent:childA1", 1, 0) {
		t.Fatalf("missing rootA→childA1 lineage edge by default: %+v", body.Edges)
	}
	if !crossHasEdge(body, "agent:rootB", "agent:forkB1", 1, 0) {
		t.Fatalf("missing rootB→forkB1 fork lineage edge by default: %+v", body.Edges)
	}
}

// TestCrossTopology_GroupRootIsIgnored asserts the cross-session topology
// ALWAYS spans all sessions: even an explicit `group=root` does not collapse it
// to roots-only, because lineage requires the child/fork endpoints to be in the
// node set. (A future roots-only toggle would be a separate param; `group` on
// /api/sessions controls the LIST, not this lineage graph.)
func TestCrossTopology_GroupRootIsIgnored(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	code, body, env := getCrossTopology(t, p, "group=root")
	if code != http.StatusOK {
		t.Fatalf("status = %d, env=%+v", code, env)
	}
	// group=root must NOT restrict this endpoint: all four sessions remain.
	if len(body.Nodes) != 4 {
		t.Fatalf("nodes = %d, want 4 (group=root must not restrict cross-session topology) (%+v)", len(body.Nodes), body.Nodes)
	}
	_ = crossNode(t, body, "agent:childA1")
	_ = crossNode(t, body, "agent:forkB1")
	if len(body.Edges) != 2 {
		t.Fatalf("edges = %d, want 2 (lineage survives group=root) (%+v)", len(body.Edges), body.Edges)
	}
}

// TestCrossTopology_FilterExcludesSessionsAndEdges asserts a source filter
// narrows the node set AND drops lineage edges whose other endpoint was
// filtered out.
func TestCrossTopology_FilterExcludesSessionsAndEdges(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	// group=all so child/fork sessions are eligible; sources=src1 keeps only
	// tree A (rootA + childA1). Tree B is excluded entirely.
	code, body, env := getCrossTopology(t, p, "group=all&sources=src1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, env=%+v", code, env)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (rootA+childA1 only) (%+v)", len(body.Nodes), body.Nodes)
	}
	_ = crossNode(t, body, "agent:rootA")
	_ = crossNode(t, body, "agent:childA1")
	for _, n := range body.Nodes {
		if n.ID == "agent:rootB" || n.ID == "agent:forkB1" {
			t.Fatalf("tree B session %q leaked past the src1 filter", n.ID)
		}
	}
	// Only the rootA→childA1 edge survives; rootB→forkB1 is gone with tree B.
	if len(body.Edges) != 1 || !crossHasEdge(body, "agent:rootA", "agent:childA1", 1, 0) {
		t.Fatalf("edges = %+v, want only rootA→childA1", body.Edges)
	}
}

// TestCrossTopology_MetricVariants asserts each ?metric= changes the agent
// node's size_metric, computed from the session's own stored aggregates.
func TestCrossTopology_MetricVariants(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	cases := []struct {
		metric    string
		wantRootA float64
	}{
		{"cost", 0.30},     // cost_usd
		{"tokens", 3000},   // tokens_in + tokens_out = 1000 + 2000
		{"duration", 8000}, // end_ts - start_ts (default)
		{"calls", 5},       // op_count
		{"ctx_pct", 0},     // best-effort 0 cross-session (documented)
	}
	for _, tc := range cases {
		t.Run(tc.metric, func(t *testing.T) {
			code, body, env := getCrossTopology(t, p, "group=all&metric="+tc.metric)
			if code != http.StatusOK {
				t.Fatalf("status = %d, env=%+v", code, env)
			}
			rootA := crossNode(t, body, "agent:rootA")
			if !floatEq(rootA.SizeMetric, tc.wantRootA) {
				t.Fatalf("metric=%s rootA size = %v, want %v", tc.metric, rootA.SizeMetric, tc.wantRootA)
			}
		})
	}
}

// TestCrossTopology_DefaultMetricIsDuration asserts an absent ?metric= matches
// ?metric=duration.
func TestCrossTopology_DefaultMetricIsDuration(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	_, def, _ := getCrossTopology(t, p, "group=all")
	_, dur, _ := getCrossTopology(t, p, "group=all&metric=duration")
	if crossNode(t, def, "agent:rootA").SizeMetric != crossNode(t, dur, "agent:rootA").SizeMetric {
		t.Fatal("default metric != duration")
	}
}

// TestCrossTopology_UnknownMetricRejected asserts an unknown ?metric= is a 400.
func TestCrossTopology_UnknownMetricRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	code, _, env := getCrossTopology(t, p, "metric=bananas")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestCrossTopology_BadFilterRejected asserts an invalid filter param (a
// present-but-empty array) is a 400, proving the endpoint reuses the
// /api/sessions filter validation.
func TestCrossTopology_BadFilterRejected(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	code, _, env := getCrossTopology(t, p, "agents=")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (present-but-empty agents)", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestCrossTopology_NodeCapAndTruncated seeds more root sessions than a small
// injected cap and asserts the response keeps the top-N by size_metric and sets
// truncated=true.
func TestCrossTopology_NodeCapAndTruncated(t *testing.T) {
	// Inject a small cap via the override (SOW-0034: was a mutable package var).
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	maxTopologyNodesOverride = 3
	defer func() { maxTopologyNodesOverride = 0 }()
	base := seedBase()
	seedSource(t, db, "src1", "aiagent_v3", "/tmp/a", base)
	// Five root sessions with ascending duration (end-start) so the top-3 by
	// duration are the three largest.
	ids := []string{"ra", "rb", "rc", "rd", "re"}
	for i, id := range ids {
		dur := int64((i + 1) * 1000)
		seedSession(t, db, sessionRow{
			id: id, sourceID: "src1", nativeID: "n" + id, rootID: id,
			kind: "root", agent: "a", status: "completed",
			startTS: base + 1_000, endTS: base + 1_000 + dur, opCount: 1,
		})
	}

	code, body, env := getCrossTopology(t, p, "metric=duration")
	if code != http.StatusOK {
		t.Fatalf("status = %d, env=%+v", code, env)
	}
	if !body.Truncated {
		t.Fatal("truncated = false, want true (5 nodes > cap of 3)")
	}
	if len(body.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (capped) (%+v)", len(body.Nodes), body.Nodes)
	}
	// The kept nodes must be the three largest durations: re (5000), rd (4000),
	// rc (3000). ra/rb must be dropped.
	for _, n := range body.Nodes {
		if n.ID == "agent:ra" || n.ID == "agent:rb" {
			t.Fatalf("small session %q kept past the top-3 cap", n.ID)
		}
	}
	_ = crossNode(t, body, "agent:re")
	_ = crossNode(t, body, "agent:rd")
	_ = crossNode(t, body, "agent:rc")
}

// TestCrossTopology_MethodNotAllowed asserts a non-GET/HEAD method is 405.
func TestCrossTopology_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodPost, "/api/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestCrossTopology_HeadParity asserts HEAD returns 200 + JSON content-type +
// empty body.
func TestCrossTopology_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedCrossTree(t, db, seedBase())

	req := httptest.NewRequest(http.MethodHead, "/api/topology", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct == "" {
		t.Fatal("Content-Type missing on HEAD")
	}
}

// TestCrossTopology_EmptySerializesArrays asserts an empty match set returns
// non-null nodes/edges arrays, max 0, truncated false.
func TestCrossTopology_EmptySerializesArrays(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	code, body, _ := getCrossTopology(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Nodes == nil || body.Edges == nil {
		t.Fatalf("nodes/edges null, want empty arrays (%+v)", body)
	}
	if len(body.Nodes) != 0 || len(body.Edges) != 0 {
		t.Fatalf("want empty graph, got %+v", body)
	}
	if body.MaxSizeMetric != 0 || body.Truncated {
		t.Fatalf("max=%v truncated=%v, want 0/false", body.MaxSizeMetric, body.Truncated)
	}
}

// TestCrossTopology_DBFailureReturns503 asserts a query error surfaces as
// DB_UNAVAILABLE rather than a partial body.
func TestCrossTopology_DBFailureReturns503(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
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
