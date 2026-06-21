package presenter

import (
	"context"
	"database/sql"
	"net/http"
)

// Cross-session topology: GET /api/topology. Reuses the per-session
// node/edge model (topoNode/topoEdge/topologyResponse from
// session_topology.go) but scopes to the active /api/sessions filter rather
// than one session tree:
//
//   - Scope: ALL sessions matching the filter (roots + sub_agents + forks).
//     handleCrossTopology forces group=all because lineage edges need the
//     child/fork endpoints in the node set; the /api/sessions roots-only group
//     default does NOT apply here (rest-api.md §GET /api/topology).
//   - Nodes: one `agent` node per matching session. NO `tool` nodes — a
//     filtered set can span thousands of sessions and their tools would
//     explode the graph; tools live only in the per-session view
//     (rest-api.md §GET /api/topology, SOW-0006 AC#2).
//   - Edges: session lineage among the matched nodes. In the canonical schema
//     BOTH sub-agent spawns AND forks set parent_session_id (the codex adapter
//     points a fork's parent_session_id at its forked_from_id with kind='fork';
//     there is no separate forked_from_id column — internal/adapters/codex).
//     So one parent_session_id pass captures the spec's "(a) parent_session_id"
//     and "(b) forked_from_id" lineage types together. Edges are structural, so
//     calls=1 and total_us=0 (they carry no call duration; the shape is reused
//     for renderer parity). An edge whose other endpoint is not in the matched
//     set is dropped (lineage to a filtered-out session is not drawn).
//   - size_metric: from the session's own stored aggregates (NOT a per-op
//     rescan, to stay bounded over a large filtered set) — see crossSizeExpr.
//   - Bound: the node set is capped at effectiveMaxTopologyNodes() (top-N by size_metric);
//     truncated=true when the cap was hit.

// defaultMaxTopologyNodes caps the cross-session node set. A filter can match
// very many sessions; the force graph and the perf budget cannot, so the
// endpoint keeps the top-N sessions by the selected size_metric and flags the
// response truncated when the cap is hit. Tests inject a smaller cap via
// maxTopologyNodesOverride (SOW-0034: was a mutable package var that prevented
// test parallelism).
const defaultMaxTopologyNodes = 500

// maxTopologyNodesOverride is set by tests that need a smaller cap. Production
// code leaves it 0 (→ defaultMaxTopologyNodes). Accessed under no lock because
// only tests set it, and topology tests are the sole writer.
var maxTopologyNodesOverride int

func effectiveMaxTopologyNodes() int {
	if maxTopologyNodesOverride > 0 {
		return maxTopologyNodesOverride
	}
	return defaultMaxTopologyNodes
}

// handleCrossTopology answers GET /api/topology. It parses the same filter
// params as /api/sessions, validates ?metric=, builds one agent node per
// matching session (capped at effectiveMaxTopologyNodes() by size_metric) plus the
// lineage edges among the retained nodes, and writes the shared topology
// envelope. Method/HEAD handling and the unknown-metric BAD_REQUEST mirror the
// per-session handler.
func (p *Presenter) handleCrossTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	f, err := parseSessionFilter(r.URL.Query(), p.now())
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	// The cross-session topology is inherently an ALL-sessions view: its purpose
	// is lineage, and lineage edges (parent_session_id) need the child/fork
	// sessions in the node set. /api/sessions defaults group to root-only (the
	// list shows roots, children are expanded inline); inheriting that here would
	// strip every child/fork endpoint and drop every lineage edge — a graph of
	// disconnected root dots. So force groupAll regardless of any ?group= value;
	// the time/agent/model/cwd/source/status filters still apply. (A roots-only
	// topology, if ever wanted, would be a separate explicit toggle, not the
	// /api/sessions list default — rest-api.md §GET /api/topology.)
	f.group = groupAll
	metric, err := parseTopologyMetric(r.URL.Query().Get("metric"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	resp, err := p.buildCrossTopology(ctx, f, metric)
	if err != nil {
		p.writeDBError(ctx, w, r, "topology.cross.build", err)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

// crossAgent is one matched session reduced to the node fields plus the
// parent id (for lineage) and the keep/drop bookkeeping the cap needs.
type crossAgent struct {
	id           string // bare session id
	parentID     string // parent_session_id, "" when NULL
	label        string
	sizeMetric   float64
	failureRatio float64
}

type crossAgentRow struct {
	id           string
	parentID     string
	agentName    string
	kind         string
	rootID       string
	sizeMetric   float64
	failureRatio float64
}

func (r crossAgentRow) agent() crossAgent {
	return crossAgent{
		id:           r.id,
		parentID:     r.parentID,
		label:        agentLabel(r.agentName, r.kind, r.id == r.rootID),
		sizeMetric:   r.sizeMetric,
		failureRatio: r.failureRatio,
	}
}

// buildCrossTopology assembles the cross-session node/edge graph for the
// filtered set. Two bounded queries: one selects the matching sessions ordered
// by size_metric DESC limited to effectiveMaxTopologyNodes()+1 (so the cap and the
// truncated flag are detected without a separate COUNT), one selects the
// lineage parent links among the retained sessions.
func (p *Presenter) buildCrossTopology(ctx context.Context, f sessionFilter, metric topologyMetric) (topologyResponse, error) {
	agents, truncated, err := p.loadCrossAgents(ctx, f, metric)
	if err != nil {
		return topologyResponse{}, err
	}
	resp := topologyResponse{
		Nodes:     make([]topoNode, 0, len(agents)),
		Edges:     []topoEdge{},
		Truncated: truncated,
	}
	retained := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		retained[a.id] = struct{}{}
		resp.Nodes = append(resp.Nodes, topoNode{
			ID: "agent:" + a.id, Kind: "agent", Label: a.label,
			SizeMetric: a.sizeMetric, FailureRatio: a.failureRatio,
		})
		if a.sizeMetric > resp.MaxSizeMetric {
			resp.MaxSizeMetric = a.sizeMetric
		}
	}
	// Lineage edges from parent_session_id, kept only when BOTH endpoints are
	// in the retained set. Deterministic order: input (size-desc) order of the
	// child, which loadCrossAgents already fixed.
	for _, a := range agents {
		if a.parentID == "" || a.parentID == a.id {
			continue
		}
		if _, ok := retained[a.parentID]; !ok {
			continue
		}
		resp.Edges = append(resp.Edges, topoEdge{
			Source: "agent:" + a.parentID, Target: "agent:" + a.id, Calls: 1, TotalUS: 0,
		})
	}
	return resp, nil
}

// crossSizeExpr returns the SQL scalar expression for a session's size_metric
// under the selected metric, computed from the session's own stored aggregate
// columns (alias = the sessions table alias). duration is end_ts - start_ts
// (0 when end_ts is NULL); ctx_pct is best-effort 0 cross-session (a
// session-level context ratio is not stored — documented in rest-api.md, not
// silently wrong). The expression is a fixed switch on the validated metric
// enum, so no user input is interpolated.
func crossSizeExpr(metric topologyMetric, alias string) string {
	switch metric {
	case metricCost:
		return alias + ".cost_usd"
	case metricTokens:
		return "(" + alias + ".tokens_in + " + alias + ".tokens_out)"
	case metricCalls:
		return alias + ".op_count"
	case metricCtxPct:
		return "0"
	default: // duration
		// Use the stored `duration_us` column (migration 0011) so the
		// optimizer can use `idx_sessions_duration` for the ORDER BY.
		// The raw column (no COALESCE) is required: wrapping the column
		// reference in an expression would force a temp B-tree sort on
		// top of the index scan (verified by EXPLAIN QUERY PLAN before
		// and after the change). NULL rows (in-progress sessions) sort
		// before non-NULL in DESC order, which preserves the visual
		// ranking: in-progress sessions sink to the bottom of the
		// long-running-session list, matching the pre-migration
		// behavior where end_ts-IS-NULL sessions got duration=0.
		return alias + ".duration_us"
	}
}

// loadCrossAgents selects the matching sessions as agent accumulators, ordered
// by the selected size_metric DESC (then id ASC for a stable tie-break),
// limited to effectiveMaxTopologyNodes()+1 so the caller can both keep the top-N and learn
// whether more matched (truncated). The size expression and failure_ratio are
// computed in SQL from the session's stored aggregates; the WHERE clause is the
// shared sessionFilter fragment (static SQL + ?-placeholders).
func (p *Presenter) loadCrossAgents(ctx context.Context, f sessionFilter, metric topologyMetric) ([]crossAgent, bool, error) {
	query, args := crossAgentsSelect(f, metric, effectiveMaxTopologyNodes())
	rows, err := p.db.QueryContext(ctx, query, args...) // #nosec G201 G202 G701 -- crossAgentsSelect uses a fixed crossSizeExpr enum switch (not user input); whereClause is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	agents, err := scanCrossAgents(rows, effectiveMaxTopologyNodes())
	if err != nil {
		return nil, false, err
	}
	agents, truncated := trimCrossAgentsToLimit(agents, effectiveMaxTopologyNodes())
	return agents, truncated, nil
}

func crossAgentsSelect(f sessionFilter, metric topologyMetric, limit int) (string, []any) {
	where, args := f.whereClause("s")
	sizeExpr := crossSizeExpr(metric, "s")
	// failure_ratio = failure_count / op_count (0 when op_count is 0). Computed
	// as a REAL so the division is not integer-truncated.
	query := `
SELECT s.id, IFNULL(s.parent_session_id, ''), IFNULL(s.agent_name, ''), s.kind, s.root_session_id,
       ` + sizeExpr + ` AS size_metric,
       CASE WHEN s.op_count > 0 THEN CAST(s.failure_count AS REAL) / s.op_count ELSE 0 END AS failure_ratio
FROM sessions s WHERE ` + where + `
ORDER BY size_metric DESC, s.id ASC
LIMIT ?` // #nosec G202 -- sizeExpr is a fixed crossSizeExpr enum switch (not user input); whereClause is static SQL + ?-placeholders; values bound via args
	args = append(args, limit+1)
	return query, args
}

func scanCrossAgents(rows *sql.Rows, limit int) ([]crossAgent, error) {
	out := make([]crossAgent, 0, limit)
	for rows.Next() {
		agent, err := scanCrossAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanCrossAgent(rows *sql.Rows) (crossAgent, error) {
	var row crossAgentRow
	err := rows.Scan(
		&row.id,
		&row.parentID,
		&row.agentName,
		&row.kind,
		&row.rootID,
		&row.sizeMetric,
		&row.failureRatio,
	)
	if err != nil {
		return crossAgent{}, err
	}
	return row.agent(), nil
}

func trimCrossAgentsToLimit(out []crossAgent, limit int) ([]crossAgent, bool) {
	// More rows than the cap means the result was truncated; drop the overflow
	// row so exactly effectiveMaxTopologyNodes() nodes are returned.
	if len(out) > limit {
		return out[:limit], true
	}
	return out, false
}
