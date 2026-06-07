package presenter

import "testing"

func TestTopoBuilderObserveAgentOpMetrics(t *testing.T) {
	t.Parallel()

	base := topoOpRow{
		durationUS: 100,
		cost:       0.25,
		tokensIn:   11,
		tokensOut:  29,
		ctxRatio:   0.30,
		failed:     true,
	}

	cases := []struct {
		name       string
		metric     topologyMetric
		wantMetric float64
		wantCtxPct float64
	}{
		{name: "duration", metric: metricDuration, wantMetric: 100},
		{name: "cost", metric: metricCost, wantMetric: 0.25},
		{name: "tokens", metric: metricTokens, wantMetric: 40},
		{name: "calls", metric: metricCalls, wantMetric: 1},
		{name: "ctx_pct", metric: metricCtxPct, wantMetric: 0, wantCtxPct: 0.30},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			agent := &topoAgent{}
			observeAgentOp(agent, tc.metric, base)

			if agent.ops != 1 {
				t.Fatalf("ops = %d, want 1", agent.ops)
			}
			if agent.failures != 1 {
				t.Fatalf("failures = %d, want 1", agent.failures)
			}
			if !floatEq(agent.metric, tc.wantMetric) {
				t.Fatalf("metric = %v, want %v", agent.metric, tc.wantMetric)
			}
			if !floatEq(agent.ctxPct, tc.wantCtxPct) {
				t.Fatalf("ctxPct = %v, want %v", agent.ctxPct, tc.wantCtxPct)
			}
		})
	}
}

func TestTopoBuilderObserveAgentOpCtxPctKeepsMaximum(t *testing.T) {
	t.Parallel()

	agent := &topoAgent{}
	observeAgentOp(agent, metricCtxPct, topoOpRow{ctxRatio: 0.25})
	observeAgentOp(agent, metricCtxPct, topoOpRow{ctxRatio: 0.10})
	observeAgentOp(agent, metricCtxPct, topoOpRow{ctxRatio: 0.75})

	if !floatEq(agent.ctxPct, 0.75) {
		t.Fatalf("ctxPct = %v, want 0.75", agent.ctxPct)
	}
}

func TestTopoAppendTopologyNodeUpdatesMaxAndNodeSet(t *testing.T) {
	t.Parallel()

	resp := topologyResponse{Nodes: []topoNode{}, Edges: []topoEdge{}}
	nodeIDs := map[string]struct{}{}

	appendTopologyNode(&resp, nodeIDs, topoNode{ID: "agent:a", SizeMetric: 2})
	appendTopologyNode(&resp, nodeIDs, topoNode{ID: "tool:b", SizeMetric: 5})
	appendTopologyNode(&resp, nodeIDs, topoNode{ID: "tool:c", SizeMetric: 3})

	if len(resp.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(resp.Nodes))
	}
	if resp.MaxSizeMetric != 5 {
		t.Fatalf("max_size_metric = %v, want 5", resp.MaxSizeMetric)
	}
	for _, id := range []string{"agent:a", "tool:b", "tool:c"} {
		if _, ok := nodeIDs[id]; !ok {
			t.Fatalf("node id %q missing from set %+v", id, nodeIDs)
		}
	}
}
