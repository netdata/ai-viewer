package presenter

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// topoNode is one node in the topology graph: an agent (a session in the
// tree) or a tool (a distinct (tool_namespace, name) among the tree's
// tool ops). size_metric carries the value of the selected ?metric= for
// agent nodes; failure_ratio is failed_ops/total_ops for the node in
// 0..1. See rest-api.md §GET /api/sessions/:id/topology.
type topoNode struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Label        string  `json:"label"`
	SizeMetric   float64 `json:"size_metric"`
	FailureRatio float64 `json:"failure_ratio"`
}

// topoEdge is one aggregated caller→callee edge with a call count and a
// summed duration (NULL durations counted as 0).
type topoEdge struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Calls   int64  `json:"calls"`
	TotalUS int64  `json:"total_us"`
}

// topologyResponse is the JSON envelope of GET /api/sessions/:id/topology.
// Nodes and edges are always non-nil slices so the body serialises as []
// rather than null on an empty graph. max_size_metric is the maximum
// size_metric across all nodes (0 when there are no nodes); the client
// normalizes node radii against it (raw values are returned, not a
// server-side 0..1 scale — rest-api.md).
type topologyResponse struct {
	Nodes         []topoNode `json:"nodes"`
	Edges         []topoEdge `json:"edges"`
	MaxSizeMetric float64    `json:"max_size_metric"`
}

// topologyMetric is the validated ?metric= selector. duration is the
// default; an unknown value is a 400 (handleSessionTopology rejects it
// before any query runs).
type topologyMetric string

const (
	metricDuration topologyMetric = "duration"
	metricCost     topologyMetric = "cost"
	metricTokens   topologyMetric = "tokens"
	metricCalls    topologyMetric = "calls"
	metricCtxPct   topologyMetric = "ctx_pct"
)

// parseTopologyMetric validates the ?metric= query value. An absent or
// empty value defaults to duration; any other unknown value is a
// BAD_REQUEST (mirrors the strict-validation stance of the filter
// params). The raw value is control-char-checked by the caller via the
// query-string path is not needed here — metric is a fixed enum, so an
// out-of-set value (including one carrying control bytes) is simply
// rejected as unknown.
func parseTopologyMetric(raw string) (topologyMetric, error) {
	switch m := topologyMetric(strings.TrimSpace(raw)); m {
	case "", metricDuration:
		return metricDuration, nil
	case metricCost, metricTokens, metricCalls, metricCtxPct:
		return m, nil
	default:
		return "", wrapBadFilter("unknown metric " + raw + " (want one of cost,tokens,duration,calls,ctx_pct)")
	}
}

// handleSessionTopology answers GET /api/sessions/:id/topology. It
// resolves :id to its session tree (root + all sessions sharing the
// root), derives agent nodes (one per session) and tool nodes (one per
// distinct tool), and aggregates edges (agent→tool for tool ops,
// agent→agent for child-session ops). 404 NOT_FOUND for an unknown id;
// method/HEAD/control-char handling mirrors handleSessionDetail.
func (p *Presenter) handleSessionTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	id, ok := p.sessionIDFromPath(w, r)
	if !ok {
		return
	}
	metric, err := parseTopologyMetric(r.URL.Query().Get("metric"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	rootID, err := p.resolveRootSessionID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "session not found", map[string]any{"id": id})
		return
	}
	if err != nil {
		p.writeDBError(w, r, ctx, "session.topology.root", err)
		return
	}

	resp, err := p.buildTopology(ctx, rootID, metric)
	if err != nil {
		p.writeDBError(w, r, ctx, "session.topology.build", err)
		return
	}
	writeJSON(w, r, p.logger, http.StatusOK, resp)
}

// buildTopology assembles the node/edge graph for a resolved session
// tree. It runs two bounded queries: one over the tree's sessions (agent
// nodes), one over the tree's ops (tool nodes, per-agent metrics, and
// edges). The metric drives the agent node's size_metric.
func (p *Presenter) buildTopology(ctx context.Context, rootID string, metric topologyMetric) (topologyResponse, error) {
	agents, err := p.loadTopologyAgents(ctx, rootID)
	if err != nil {
		return topologyResponse{}, err
	}
	tb := newTopoBuilder(agents)
	if err := p.scanTopologyOps(ctx, rootID, metric, tb); err != nil {
		return topologyResponse{}, err
	}
	return tb.finish(), nil
}

// resolveRootSessionID reads the root_session_id for a session id. Returns
// sql.ErrNoRows when the id is unknown so the handler maps it to 404. Root
// sessions reference themselves (root_session_id = id), so the returned
// value is always the tree's root.
func (p *Presenter) resolveRootSessionID(ctx context.Context, id string) (string, error) {
	var root string
	err := p.db.QueryRowContext(ctx,
		`SELECT root_session_id FROM sessions WHERE id = ?`, id).Scan(&root)
	return root, err
}

// loadTopologyAgents reads every session in the tree (root + all sessions
// sharing root_session_id) and returns the pre-seeded agent accumulators.
// The root session's node label carries a " (root)" suffix. Ordered by
// start_ts so the agent node order is stable and root-first when it starts
// first.
func (p *Presenter) loadTopologyAgents(ctx context.Context, rootID string) ([]topoAgent, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, IFNULL(agent_name, ''), kind
FROM sessions WHERE root_session_id = ?
ORDER BY start_ts ASC, id ASC`, rootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]topoAgent, 0, 4)
	for rows.Next() {
		var id, agent, kind string
		if err := rows.Scan(&id, &agent, &kind); err != nil {
			return nil, err
		}
		out = append(out, topoAgent{id: "agent:" + id, label: agentLabel(agent, kind, id == rootID)})
	}
	return out, rows.Err()
}

// scanTopologyOps streams every op of the tree's sessions through the
// builder in one query. ops.session_id is matched against the tree via a
// subquery on root_session_id so the scan is a single indexed pass.
func (p *Presenter) scanTopologyOps(ctx context.Context, rootID string, metric topologyMetric, b *topoBuilder) error {
	b.metric = metric
	rows, err := p.db.QueryContext(ctx, `
SELECT o.session_id, o.kind, o.tool_namespace, o.name, o.child_session_id,
       o.duration_us, o.cost_usd, o.tokens_in, o.tokens_out, o.ctx_used, o.ctx_max, o.status
FROM ops o
WHERE o.session_id IN (SELECT id FROM sessions WHERE root_session_id = ?)
ORDER BY o.session_id ASC, o.seq ASC`, rootID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			o        topoOpRow
			ns       sql.NullString
			childID  sql.NullString
			duration sql.NullInt64
			ctxUsed  sql.NullInt64
			ctxMax   sql.NullInt64
			status   string
		)
		if err := rows.Scan(&o.sessionID, &o.kind, &ns, &o.toolName, &childID,
			&duration, &o.cost, &o.tokensIn, &o.tokensOut, &ctxUsed, &ctxMax, &status); err != nil {
			return err
		}
		o.toolNamespace = ns.String
		o.childSessionID = childID.String
		o.durationUS = duration.Int64
		o.failed = status == "failed"
		if ctxUsed.Valid && ctxMax.Valid && ctxMax.Int64 > 0 && ctxUsed.Int64 > 0 {
			o.ctxRatio = float64(ctxUsed.Int64) / float64(ctxMax.Int64)
		}
		b.observeOp(o)
	}
	return rows.Err()
}

// agentLabel builds an agent node's label: the agent name when known,
// else the session kind, with " (root)" appended for the root session.
func agentLabel(agent, kind string, isRoot bool) string {
	label := agent
	if label == "" {
		label = kind
	}
	if isRoot {
		label += " (root)"
	}
	return label
}

// sessionIDFromPath reads, control-char-checks, and trims the :id path value
// value shared by the session sub-route handlers. It writes the
// appropriate 400 envelope and returns ok=false when the id is missing or
// carries a control byte; otherwise it returns the trimmed id. Mirrors the
// guard inlined in handleSessionDetail so every session sub-route applies
// the identical rule.
func (p *Presenter) sessionIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	idRaw := r.PathValue("id")
	if err := rejectControlChars("id", idRaw); err != nil {
		p.writeBadFilter(w, r, err)
		return "", false
	}
	id := strings.TrimSpace(idRaw)
	if id == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "session id is required", nil)
		return "", false
	}
	return id, true
}
