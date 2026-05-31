package presenter

import "strings"

// In-memory aggregation for GET /api/sessions/:id/topology. Split out of
// session_topology.go (HTTP handler + SQL queries) so each file stays
// under the project's 400-line ceiling. This file owns the streaming
// accumulator: it folds the tree's ops into agent/tool nodes and edges in
// one pass and materialises the response. It does no I/O.

// topoOpRow is one ops row reduced to the columns the topology builder
// needs. childSessionID/toolNamespace are empty when NULL.
type topoOpRow struct {
	sessionID      string
	kind           string
	toolNamespace  string
	toolName       string
	childSessionID string
	durationUS     int64
	cost           float64
	tokensIn       int64
	tokensOut      int64
	ctxRatio       float64
	failed         bool
}

// toolKey returns the dotted "<namespace>.<name>" tool identity, or just
// "<name>" when tool_namespace is NULL/empty.
func (o topoOpRow) toolKey() string {
	if o.toolNamespace == "" {
		return o.toolName
	}
	return o.toolNamespace + "." + o.toolName
}

// topoAgent is the per-session accumulator behind one agent node.
type topoAgent struct {
	id       string // node id: "agent:<session_id>"
	label    string
	ops      int64
	failures int64
	metric   float64 // running aggregate for the selected ?metric=
	ctxPct   float64 // running MAX(ctx_used/ctx_max) for metric=ctx_pct
}

// topoTool is the per-tool accumulator behind one tool node.
type topoTool struct {
	id       string // node id: "tool:<ns>.<name>"
	label    string
	ops      int64
	failures int64
	duration int64
	cost     float64
	tokens   int64
}

// topoBuilder accumulates agent/tool nodes and edges while a single ops
// scan walks the tree. Agents are pre-seeded from the sessions query so a
// session with zero ops still yields a node; tools and edges are created
// lazily as ops are seen.
type topoBuilder struct {
	metric     topologyMetric
	agents     map[string]*topoAgent // keyed by session_id
	agentOrder []string
	tools      map[string]*topoTool // keyed by tool node id
	toolOrder  []string
	edges      map[string]*topoEdge // keyed by source+"\x00"+target
	edgeOrder  []string
}

// newTopoBuilder pre-seeds the agent accumulators from the session rows so
// the node set is the full tree even when some sessions have no ops.
func newTopoBuilder(agents []topoAgent) *topoBuilder {
	b := &topoBuilder{
		agents: make(map[string]*topoAgent, len(agents)),
		tools:  map[string]*topoTool{},
		edges:  map[string]*topoEdge{},
	}
	for i := range agents {
		a := agents[i]
		// sessionID is the bare id; the node id carries the "agent:" prefix.
		sessionID := strings.TrimPrefix(a.id, "agent:")
		b.agents[sessionID] = &a
		b.agentOrder = append(b.agentOrder, sessionID)
	}
	return b
}

// observeOp folds one ops row into the builder. sessionID owns the op
// (ops.session_id); kind/status/metric inputs drive node sizing and edge
// creation.
func (b *topoBuilder) observeOp(o topoOpRow) {
	ag := b.agents[o.sessionID]
	if ag != nil {
		ag.ops++
		if o.failed {
			ag.failures++
		}
		switch b.metric {
		case metricCost:
			ag.metric += o.cost
		case metricTokens:
			ag.metric += float64(o.tokensIn + o.tokensOut)
		case metricCalls:
			ag.metric++
		case metricCtxPct:
			if o.ctxRatio > ag.ctxPct {
				ag.ctxPct = o.ctxRatio
			}
		default: // duration
			ag.metric += float64(o.durationUS)
		}
	}
	if o.kind == "tool" {
		b.observeToolOp(o)
	}
	if o.kind == "session" && o.childSessionID != "" {
		b.addEdge("agent:"+o.sessionID, "agent:"+o.childSessionID, o.durationUS)
	}
}

// observeToolOp creates/updates the tool node for a tool op and the
// agent→tool edge.
func (b *topoBuilder) observeToolOp(o topoOpRow) {
	toolID := "tool:" + o.toolKey()
	tl := b.tools[toolID]
	if tl == nil {
		tl = &topoTool{id: toolID, label: o.toolKey()}
		b.tools[toolID] = tl
		b.toolOrder = append(b.toolOrder, toolID)
	}
	tl.ops++
	tl.duration += o.durationUS
	tl.cost += o.cost
	tl.tokens += o.tokensIn + o.tokensOut
	if o.failed {
		tl.failures++
	}
	b.addEdge("agent:"+o.sessionID, toolID, o.durationUS)
}

// addEdge aggregates one caller→callee observation into the edge map.
func (b *topoBuilder) addEdge(source, target string, durationUS int64) {
	key := source + "\x00" + target
	e := b.edges[key]
	if e == nil {
		e = &topoEdge{Source: source, Target: target}
		b.edges[key] = e
		b.edgeOrder = append(b.edgeOrder, key)
	}
	e.Calls++
	e.TotalUS += durationUS
}

// finish materialises the accumulated maps into the response, computing
// failure ratios, tool size_metrics for the selected metric, and the
// max_size_metric. Order is deterministic: agents by session start order
// (insertion), tools by first appearance, edges by first appearance.
func (b *topoBuilder) finish() topologyResponse {
	resp := topologyResponse{Nodes: []topoNode{}, Edges: []topoEdge{}}
	// nodeIDs collects every materialised node id (agents + tools) so dangling
	// edges can be dropped defensively below.
	nodeIDs := make(map[string]struct{}, len(b.agentOrder)+len(b.toolOrder))
	for _, sid := range b.agentOrder {
		a := b.agents[sid]
		size := a.metric
		if b.metric == metricCtxPct {
			size = a.ctxPct
		}
		resp.Nodes = append(resp.Nodes, topoNode{
			ID: a.id, Kind: "agent", Label: a.label,
			SizeMetric: size, FailureRatio: ratio(a.failures, a.ops),
		})
		nodeIDs[a.id] = struct{}{}
		if size > resp.MaxSizeMetric {
			resp.MaxSizeMetric = size
		}
	}
	for _, tid := range b.toolOrder {
		t := b.tools[tid]
		size := toolSize(b.metric, t)
		resp.Nodes = append(resp.Nodes, topoNode{
			ID: t.id, Kind: "tool", Label: t.label,
			SizeMetric: size, FailureRatio: ratio(t.failures, t.ops),
		})
		nodeIDs[t.id] = struct{}{}
		if size > resp.MaxSizeMetric {
			resp.MaxSizeMetric = size
		}
	}
	// Append edges in first-appearance order, but drop any whose endpoint is
	// not a materialised node. A kind='session' op can carry a child_session_id
	// outside this tree (no agent node for it); the spec promises such an edge
	// is dropped defensively (rest-api.md §GET /api/sessions/:id/topology).
	// Agent→tool and agent→in-tree-child edges are unaffected — their endpoints
	// are always materialised.
	for _, key := range b.edgeOrder {
		e := b.edges[key]
		if _, ok := nodeIDs[e.Source]; !ok {
			continue
		}
		if _, ok := nodeIDs[e.Target]; !ok {
			continue
		}
		resp.Edges = append(resp.Edges, *e)
	}
	return resp
}

// toolSize returns the size_metric a tool node carries under the selected
// metric. Tools have no tokens/ctx of their own, so ctx_pct/duration both
// map to the tool's summed duration; cost/tokens/calls map to the tool's
// own aggregates (rest-api.md).
func toolSize(metric topologyMetric, t *topoTool) float64 {
	switch metric {
	case metricCost:
		return t.cost
	case metricTokens:
		return float64(t.tokens)
	case metricCalls:
		return float64(t.ops)
	default: // duration, ctx_pct
		return float64(t.duration)
	}
}
