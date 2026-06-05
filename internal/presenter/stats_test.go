package presenter

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// statsBody mirrors GET /api/stats.
type statsBody struct {
	Totals struct {
		SessionCount     int64   `json:"session_count"`
		TurnCount        int64   `json:"turn_count"`
		OpCount          int64   `json:"op_count"`
		TokensIn         int64   `json:"tokens_in"`
		TokensOut        int64   `json:"tokens_out"`
		TokensCacheRead  int64   `json:"tokens_cache_read"`
		TokensCacheWrite int64   `json:"tokens_cache_write"`
		CostUSD          float64 `json:"cost_usd"`
		Failures         int64   `json:"failures"`
		DurationUS       int64   `json:"duration_us"`
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
	// Cache-token totals roll up alongside tokens_in/out (SOW-0029). tokens_in
	// is FRESH; cache is reported separately so the Stats totals can show a
	// cache breakdown + overall hit-rate.
	if body.Totals.TokensIn != 1000 || body.Totals.TokensOut != 2000 {
		t.Fatalf("tokens in/out = %d/%d, want 1000/2000 (fresh)", body.Totals.TokensIn, body.Totals.TokensOut)
	}
	if body.Totals.TokensCacheRead != 3000 {
		t.Errorf("tokens_cache_read = %d, want 3000", body.Totals.TokensCacheRead)
	}
	if body.Totals.TokensCacheWrite != 500 {
		t.Errorf("tokens_cache_write = %d, want 500", body.Totals.TokensCacheWrite)
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

// TestStats_MaliciousFilterValuesStayBound pins the SQL-construction
// false-positive disposition for SOW-0046: filter values that look like SQL
// remain literal bound parameters and cannot widen the stats session set.
func TestStats_MaliciousFilterValuesStayBound(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	base := seedBase()
	seedGraph(t, db, base)

	code, broad, _ := getStats(t, p, "group=all")
	if code != http.StatusOK {
		t.Fatalf("baseline status = %d", code)
	}
	if broad.Totals.SessionCount != 3 || broad.Totals.OpCount != 6 {
		t.Fatalf("baseline totals = sessions:%d ops:%d, want sessions:3 ops:6",
			broad.Totals.SessionCount, broad.Totals.OpCount)
	}

	sqlOR := "failed') OR 1=1 --"
	sqlLike := "%' OR 1=1 --"
	cases := []struct {
		name  string
		query url.Values
	}{
		{"status", url.Values{"group": {"all"}, "status": {sqlOR}}},
		{"agents", url.Values{"group": {"all"}, "agents": {"worker') OR 1=1 --"}}},
		{"models", url.Values{"group": {"all"}, "models": {"gpt-5') OR 1=1 --"}}},
		{"sources", url.Values{"group": {"all"}, "sources": {"src1') OR 1=1 --"}}},
		{"tools", url.Values{"group": {"all"}, "tools": {"Read') OR 1=1 --"}}},
		{"q", url.Values{"group": {"all"}, "q": {sqlLike}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, env := getStats(t, p, tc.query.Encode())
			if code != http.StatusOK {
				t.Fatalf("status = %d, env=%+v", code, env)
			}
			assertStatsEmpty(t, body)
		})
	}
}

func assertStatsEmpty(t *testing.T, body statsBody) {
	t.Helper()
	assertStatsTotalInt(t, "session_count", body.Totals.SessionCount)
	assertStatsTotalInt(t, "turn_count", body.Totals.TurnCount)
	assertStatsTotalInt(t, "op_count", body.Totals.OpCount)
	assertStatsTotalInt(t, "tokens_in", body.Totals.TokensIn)
	assertStatsTotalInt(t, "tokens_out", body.Totals.TokensOut)
	assertStatsTotalInt(t, "tokens_cache_read", body.Totals.TokensCacheRead)
	assertStatsTotalInt(t, "tokens_cache_write", body.Totals.TokensCacheWrite)
	assertStatsTotalFloat(t, "cost_usd", body.Totals.CostUSD)
	assertStatsTotalInt(t, "failures", body.Totals.Failures)
	assertStatsTotalInt(t, "duration_us", body.Totals.DurationUS)
	assertStatsBreakdownEmpty(t, "by_model", len(body.ByModel))
	assertStatsBreakdownEmpty(t, "by_tool", len(body.ByTool))
	assertStatsBreakdownEmpty(t, "by_agent", len(body.ByAgent))
	assertStatsBreakdownEmpty(t, "by_status", len(body.ByStatus))
	assertStatsBreakdownEmpty(t, "by_source", len(body.BySource))
}

func assertStatsTotalInt(t *testing.T, name string, got int64) {
	t.Helper()
	if got != 0 {
		t.Errorf("malicious filter broadened stats total %s: %d", name, got)
	}
}

func assertStatsTotalFloat(t *testing.T, name string, got float64) {
	t.Helper()
	if got != 0 {
		t.Errorf("malicious filter broadened stats total %s: %f", name, got)
	}
}

func assertStatsBreakdownEmpty(t *testing.T, name string, got int) {
	t.Helper()
	if got != 0 {
		t.Errorf("malicious filter broadened stats breakdown %s: %d rows", name, got)
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
