package presenter

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// statsBody mirrors GET /api/stats.
type statsBody struct {
	Totals struct {
		SessionCount int64   `json:"session_count"`
		TurnCount    int64   `json:"turn_count"`
		OpCount      int64   `json:"op_count"`
		TokensIn     int64   `json:"tokens_in"`
		TokensOut    int64   `json:"tokens_out"`
		CostUSD      float64 `json:"cost_usd"`
		Failures     int64   `json:"failures"`
		DurationUS   int64   `json:"duration_us"`
	} `json:"totals"`
	ByModel []struct {
		Name      string  `json:"name"`
		Provider  string  `json:"provider"`
		Calls     int64   `json:"calls"`
		TokensIn  int64   `json:"tokens_in"`
		TokensOut int64   `json:"tokens_out"`
		CostUSD   float64 `json:"cost_usd"`
		Failures  int64   `json:"failures"`
		PctOfCost float64 `json:"pct_of_cost"`
	} `json:"by_model"`
	ByTool []struct {
		Namespace  string  `json:"namespace"`
		Name       string  `json:"name"`
		Calls      int64   `json:"calls"`
		Failures   int64   `json:"failures"`
		TotalUS    int64   `json:"total_us"`
		PctOfCalls float64 `json:"pct_of_calls"`
	} `json:"by_tool"`
	ByAgent []struct {
		Name          string  `json:"name"`
		Sessions      int64   `json:"sessions"`
		Failures      int64   `json:"failures"`
		TokensIn      int64   `json:"tokens_in"`
		TokensOut     int64   `json:"tokens_out"`
		CostUSD       float64 `json:"cost_usd"`
		PctOfSessions float64 `json:"pct_of_sessions"`
	} `json:"by_agent"`
	ByStatus []struct {
		Status string `json:"status"`
		Count  int64  `json:"count"`
	} `json:"by_status"`
	BySource []struct {
		Source   string `json:"source"`
		Sessions int64  `json:"sessions"`
		Failures int64  `json:"failures"`
	} `json:"by_source"`
}

func getStats(t *testing.T, p *Presenter, query string) (int, statsBody, errorEnvelope) {
	t.Helper()
	path := "/api/stats"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body statsBody
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

// TestStats_TotalsAndBreakdowns asserts the aggregates sum over the
// filtered set. The stats endpoint aggregates over the same session set
// the list endpoint would return for the given filters; ops/tools/models
// roll up from the ops belonging to those sessions.
func TestStats_TotalsAndBreakdowns(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	// Default group=root: only rootA in the session set.
	code, body, _ := getStats(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Totals.SessionCount != 1 {
		t.Fatalf("session_count = %d, want 1 (root only)", body.Totals.SessionCount)
	}
	if body.Totals.TurnCount != 2 {
		t.Fatalf("turn_count = %d, want 2", body.Totals.TurnCount)
	}
	if body.Totals.OpCount != 4 {
		t.Fatalf("op_count = %d, want 4", body.Totals.OpCount)
	}
	if body.Totals.Failures != 1 {
		t.Fatalf("failures = %d, want 1", body.Totals.Failures)
	}

	// by_status over the session set.
	if len(body.ByStatus) != 1 || body.ByStatus[0].Status != "completed" || body.ByStatus[0].Count != 1 {
		t.Fatalf("by_status = %+v", body.ByStatus)
	}

	// by_source: one source, one session.
	if len(body.BySource) != 1 || body.BySource[0].Sessions != 1 {
		t.Fatalf("by_source = %+v", body.BySource)
	}
}

// TestStats_GroupAll asserts group=all widens the session set and that
// pct_of_cost across by_model sums to ~1.0.
func TestStats_GroupAll(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getStats(t, p, "group=all")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Totals.SessionCount != 3 {
		t.Fatalf("session_count = %d, want 3", body.Totals.SessionCount)
	}

	// pct_of_cost across models must sum to ~1.0 (when total cost > 0).
	var sumPct float64
	var sumCost float64
	for _, m := range body.ByModel {
		sumPct += m.PctOfCost
		sumCost += m.CostUSD
	}
	if sumCost > 0 && math.Abs(sumPct-1.0) > 1e-6 {
		t.Fatalf("sum(pct_of_cost) = %v, want ~1.0", sumPct)
	}

	// by_tool pct_of_calls must sum to ~1.0 across tools.
	var sumToolPct float64
	var sumCalls int64
	for _, tl := range body.ByTool {
		sumToolPct += tl.PctOfCalls
		sumCalls += tl.Calls
	}
	if sumCalls > 0 && math.Abs(sumToolPct-1.0) > 1e-6 {
		t.Fatalf("sum(pct_of_calls) = %v, want ~1.0", sumToolPct)
	}

	// by_agent pct_of_sessions sums to ~1.0.
	var sumAgentPct float64
	for _, a := range body.ByAgent {
		sumAgentPct += a.PctOfSessions
	}
	if math.Abs(sumAgentPct-1.0) > 1e-6 {
		t.Fatalf("sum(pct_of_sessions) = %v, want ~1.0", sumAgentPct)
	}
}

// TestStats_FilterStatus asserts the same filters as /api/sessions apply.
func TestStats_FilterStatus(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, body, _ := getStats(t, p, "group=all&status=failed")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Totals.SessionCount != 1 {
		t.Fatalf("session_count = %d, want 1 (failed only)", body.Totals.SessionCount)
	}
	if body.Totals.Failures != 1 {
		t.Fatalf("failures = %d, want 1", body.Totals.Failures)
	}
}

// TestStats_Empty asserts an empty DB returns zeroed totals and empty
// breakdown arrays (not null).
func TestStats_Empty(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body, _ := getStats(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Totals.SessionCount != 0 {
		t.Fatalf("session_count = %d, want 0", body.Totals.SessionCount)
	}
	if body.ByModel == nil || body.ByTool == nil || body.ByAgent == nil ||
		body.ByStatus == nil || body.BySource == nil {
		t.Fatal("breakdown arrays must serialize as [] not null")
	}
}

// TestStats_BadRequest asserts filter validation reuses the shared parser.
func TestStats_BadRequest(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, _, env := getStats(t, p, "from=9&to=1")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestStats_HeadParity asserts HEAD on /api/stats.
func TestStats_HeadParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	headReq := httptest.NewRequest(http.MethodHead, "/api/stats", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headRR.Code)
	}
	if headRR.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headRR.Body.String())
	}
}

// TestStats_MethodNotAllowed asserts non-GET/HEAD is rejected.
func TestStats_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/stats", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
