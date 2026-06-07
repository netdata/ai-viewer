package presenter

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNewStatsResponseInitializesBreakdownSlices(t *testing.T) {
	t.Parallel()

	resp := newStatsResponse()

	if resp.ByModel == nil {
		t.Fatal("ByModel is nil, want empty slice")
	}
	if resp.ByTool == nil {
		t.Fatal("ByTool is nil, want empty slice")
	}
	if resp.ByAgent == nil {
		t.Fatal("ByAgent is nil, want empty slice")
	}
	if resp.ByStatus == nil {
		t.Fatal("ByStatus is nil, want empty slice")
	}
	if resp.BySource == nil {
		t.Fatal("BySource is nil, want empty slice")
	}
}

func TestStatsFilterScopeKeepsFilterValuesBound(t *testing.T) {
	t.Parallel()

	f, err := parseSessionFilter(parseValues(t,
		"group=all&agents=worker%27%29%20OR%201%3D1%20--&sources=src1&from=10&to=20"), fixedTime)
	if err != nil {
		t.Fatalf("parseSessionFilter: %v", err)
	}

	where, args, sessionSet := statsFilterScope(f)

	if !strings.HasPrefix(sessionSet, "SELECT s.id FROM sessions s WHERE ") {
		t.Fatalf("sessionSet = %q, want sessions id subquery", sessionSet)
	}
	if !strings.Contains(sessionSet, where) {
		t.Fatalf("sessionSet = %q does not contain where fragment %q", sessionSet, where)
	}
	if strings.Contains(sessionSet, "OR 1=1") {
		t.Fatalf("sessionSet interpolated attacker-looking value: %q", sessionSet)
	}

	wantArgs := []any{"worker') OR 1=1 --", "src1", int64(10), int64(20)}
	if diff := cmp.Diff(wantArgs, args); diff != "" {
		t.Fatalf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadStatsResponseReportsFirstQueryStage(t *testing.T) {
	t.Parallel()

	p, _ := newClosedDBPresenter(t)
	f, err := parseSessionFilter(parseValues(t, "group=all"), fixedTime)
	if err != nil {
		t.Fatalf("parseSessionFilter: %v", err)
	}

	_, qerr := p.loadStatsResponse(context.Background(), f)
	if qerr == nil {
		t.Fatal("loadStatsResponse returned nil error on closed DB")
	}
	if qerr.op != "stats.totals" {
		t.Fatalf("op = %q, want stats.totals", qerr.op)
	}
	if qerr.err == nil {
		t.Fatal("wrapped err is nil")
	}
}
