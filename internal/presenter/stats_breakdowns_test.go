package presenter

import (
	"context"
	"testing"
)

// TestStatsBreakdowns_QueryErrorBranches drives the per-breakdown
// QueryContext error branch directly against a closed DB. The integrated
// /api/stats handler short-circuits at the first failing query, so these
// defensive `return err` paths (one per breakdown) are exercised here by
// calling each helper in isolation. This pins the no-silent-failure
// contract: every breakdown propagates a query error rather than
// swallowing it.
func TestStatsBreakdowns_QueryErrorBranches(t *testing.T) {
	t.Parallel()
	p, _ := newClosedDBPresenter(t)
	ctx := context.Background()
	where := "1=1"
	var args []any
	resp := &statsResponse{}

	if err := p.statsByStatus(ctx, where, args, resp); err == nil {
		t.Error("statsByStatus: want error on closed DB")
	}
	if err := p.statsBySource(ctx, where, args, resp); err == nil {
		t.Error("statsBySource: want error on closed DB")
	}
	if err := p.statsByAgent(ctx, where, args, 0, resp); err == nil {
		t.Error("statsByAgent: want error on closed DB")
	}
	set := "SELECT s.id FROM sessions s WHERE 1=1"
	if err := p.statsByModel(ctx, set, args, resp); err == nil {
		t.Error("statsByModel: want error on closed DB")
	}
	if err := p.statsByTool(ctx, set, args, resp); err == nil {
		t.Error("statsByTool: want error on closed DB")
	}
}

// TestRatioHelpers asserts the share helpers guard against a zero
// denominator (an empty filtered set must yield 0, not a divide panic).
func TestRatioHelpers(t *testing.T) {
	t.Parallel()
	if r := ratio(5, 10); r != 0.5 {
		t.Fatalf("ratio(5,10) = %v, want 0.5", r)
	}
	if r := ratio(1, 0); r != 0 {
		t.Fatalf("ratio(1,0) = %v, want 0", r)
	}
	if r := ratio(0, 0); r != 0 {
		t.Fatalf("ratio(0,0) = %v, want 0", r)
	}
	if r := ratioF(2.0, 8.0); r != 0.25 {
		t.Fatalf("ratioF(2,8) = %v, want 0.25", r)
	}
	if r := ratioF(1.0, 0); r != 0 {
		t.Fatalf("ratioF(1,0) = %v, want 0", r)
	}
}

// TestLoadHelpers_QueryErrorBranches drives the session-detail loader
// error branches against a closed DB so loadSession / loadChildSessions /
// loadTurns / loadOps / attachPayloadRefs each propagate their query
// error rather than panicking on a nil result set.
func TestLoadHelpers_QueryErrorBranches(t *testing.T) {
	t.Parallel()
	p, _ := newClosedDBPresenter(t)
	ctx := context.Background()

	if _, err := p.loadSession(ctx, "x"); err == nil {
		t.Error("loadSession: want error on closed DB")
	}
	if _, err := p.loadChildSessions(ctx, "x"); err == nil {
		t.Error("loadChildSessions: want error on closed DB")
	}
	if _, _, err := p.loadTurns(ctx, "x"); err == nil {
		t.Error("loadTurns: want error on closed DB")
	}
	if _, err := p.loadTurnsWithOps(ctx, "x"); err == nil {
		t.Error("loadTurnsWithOps: want error on closed DB")
	}
}
