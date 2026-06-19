package presenter

import (
	"context"
	"net/http"
)

// statsResponse is the JSON envelope of GET /api/stats. Every breakdown
// slice is initialised to an empty (non-nil) slice so the JSON serialises
// as [] rather than null on an empty result set.
type statsResponse struct {
	Totals       statsTotals     `json:"totals"`
	ByModel      []statModelRow  `json:"by_model"`
	ByTool       []statToolRow   `json:"by_tool"`
	ByAgent      []statAgentRow  `json:"by_agent"`
	ByStatus     []statStatusRow `json:"by_status"`
	BySource     []statSourceRow `json:"by_source"`
	ByErrorClass []statErrorRow  `json:"by_error_class"`
}

type statsTotals struct {
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
}

type statModelRow struct {
	Name             string  `json:"name"`
	Provider         string  `json:"provider"`
	Calls            int64   `json:"calls"`
	TokensIn         int64   `json:"tokens_in"`
	TokensOut        int64   `json:"tokens_out"`
	TokensCacheRead  int64   `json:"tokens_cache_read"`
	TokensCacheWrite int64   `json:"tokens_cache_write"`
	CostUSD          float64 `json:"cost_usd"`
	Failures         int64   `json:"failures"`
	DurationUS       int64   `json:"duration_us"`
	PctOfCost        float64 `json:"pct_of_cost"`
}

type statToolRow struct {
	Namespace  string  `json:"namespace"`
	Name       string  `json:"name"`
	Calls      int64   `json:"calls"`
	Failures   int64   `json:"failures"`
	TotalUS    int64   `json:"total_us"`
	PctOfCalls float64 `json:"pct_of_calls"`
}

type statAgentRow struct {
	Name             string  `json:"name"`
	Sessions         int64   `json:"sessions"`
	Failures         int64   `json:"failures"`
	TokensIn         int64   `json:"tokens_in"`
	TokensOut        int64   `json:"tokens_out"`
	TokensCacheRead  int64   `json:"tokens_cache_read"`
	TokensCacheWrite int64   `json:"tokens_cache_write"`
	CostUSD          float64 `json:"cost_usd"`
	PctOfSessions    float64 `json:"pct_of_sessions"`
}

type statStatusRow struct {
	Status    string  `json:"status"`
	Count     int64   `json:"count"`
	CostUSD   float64 `json:"cost_usd"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
}

// statSourceRow is one row in the by_source breakdown. SOW-0067 enriches it
// with full economics (cost/tokens/cache/op_count) + the harness format label
// so the Statistics comparison table can answer "which harness is most
// efficient". `Source` is the source_id (the filterable value driving the
// Sources chip drill-down); `Format` is the source_format label (e.g.
// claude-code, codex) shown in the table.
type statSourceRow struct {
	Source          string  `json:"source"`
	Format          string  `json:"format"`
	Sessions        int64   `json:"sessions"`
	Failures        int64   `json:"failures"`
	CostUSD         float64 `json:"cost_usd"`
	TokensIn        int64   `json:"tokens_in"`
	TokensOut       int64   `json:"tokens_out"`
	TokensCacheRead int64   `json:"tokens_cache_read"`
	OpCount         int64   `json:"op_count"`
}

// statErrorRow is one row in the by_error_class breakdown: how many failed
// sessions share each error class, with their total cost/tokens (SOW-0067).
type statErrorRow struct {
	ErrorClass string  `json:"error_class"`
	Sessions   int64   `json:"sessions"`
	Ops        int64   `json:"ops"`
	CostUSD    float64 `json:"cost_usd"`
}

type statsQueryError struct {
	op  string
	err error
}

// handleStats answers GET /api/stats: cross-session aggregates over the
// same filtered set /api/sessions would return. The matching-session set
// is expressed once as a parameterized subquery (`sessions WHERE
// <filter>`) and reused by each breakdown so the filter semantics are
// identical to the list endpoint.
func (p *Presenter) handleStats(w http.ResponseWriter, r *http.Request) {
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

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	resp, qerr := p.loadStatsResponse(ctx, f)
	if qerr != nil {
		p.writeDBError(ctx, w, r, qerr.op, qerr.err)
		return
	}
	writeJSON(w, r, p.logger, resp)
}

func newStatsResponse() statsResponse {
	return statsResponse{
		ByModel:      []statModelRow{},
		ByTool:       []statToolRow{},
		ByAgent:      []statAgentRow{},
		ByStatus:     []statStatusRow{},
		BySource:     []statSourceRow{},
		ByErrorClass: []statErrorRow{},
	}
}

func statsFilterScope(f sessionFilter) (string, []any, string) {
	where, args := f.whereClause("s")
	// sessionSet selects the ids of the matching sessions; the ops-based
	// breakdowns join against it so only ops belonging to the filtered
	// sessions are counted.
	// SOW-0046: whereClause returns static SQL plus bound placeholders; TestStats_MaliciousFilterValuesStayBound proves attacker-looking values remain parameters.
	sessionSet := "SELECT s.id FROM sessions s WHERE " + where // nosemgrep: go.lang.security.injection.tainted-sql-string.tainted-sql-string
	return where, args, sessionSet
}

func (p *Presenter) loadStatsResponse(ctx context.Context, f sessionFilter) (statsResponse, *statsQueryError) {
	where, args, sessionSet := statsFilterScope(f)
	resp := newStatsResponse()

	if err := p.statsTotals(ctx, where, args, &resp.Totals); err != nil {
		return resp, &statsQueryError{op: "stats.totals", err: err}
	}
	if err := p.statsByStatus(ctx, where, args, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_status", err: err}
	}
	if err := p.statsBySource(ctx, where, args, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_source", err: err}
	}
	if err := p.statsByAgent(ctx, where, args, resp.Totals.SessionCount, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_agent", err: err}
	}
	if err := p.statsByModel(ctx, sessionSet, args, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_model", err: err}
	}
	if err := p.statsByTool(ctx, sessionSet, args, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_tool", err: err}
	}
	if err := p.statsByErrorClass(ctx, where, args, &resp); err != nil {
		return resp, &statsQueryError{op: "stats.by_error_class", err: err}
	}
	return resp, nil
}

// statsTotals aggregates the session-level rollup columns over the
// filtered set. The DurationUS total is the summed SESSION wall-clock span
// (each session's end_ts − start_ts, counted only when end_ts is known) — a
// session-level measure, distinct from and NOT a sum of ops.duration_us (the
// per-op end_ts − start_ts the catalog rollups use). where is the
// parameterized filter fragment from sessionFilter.whereClause: static SQL +
// ?-placeholders, values bound via args (no interpolation). gosec G201 cannot
// trace that; same rationale and suppression style as
// internal/ingest/aggregates.go.
func (p *Presenter) statsTotals(ctx context.Context, where string, args []any, out *statsTotals) error {
	q := `
SELECT
    COUNT(*),
    IFNULL(SUM(s.turn_count), 0),
    IFNULL(SUM(s.op_count), 0),
    IFNULL(SUM(s.tokens_in), 0),
    IFNULL(SUM(s.tokens_out), 0),
    IFNULL(SUM(s.tokens_cache_read), 0),
    IFNULL(SUM(s.tokens_cache_write), 0),
    IFNULL(SUM(s.cost_usd), 0),
    IFNULL(SUM(s.failure_count), 0),
    IFNULL(SUM(CASE WHEN s.end_ts IS NOT NULL THEN s.end_ts - s.start_ts ELSE 0 END), 0)
FROM sessions s WHERE ` + where // #nosec G201 G202 -- where is static SQL + ?-placeholders; values bound via args
	return p.db.QueryRowContext(ctx, q, args...).Scan( // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
		&out.SessionCount, &out.TurnCount, &out.OpCount,
		&out.TokensIn, &out.TokensOut, &out.TokensCacheRead, &out.TokensCacheWrite,
		&out.CostUSD, &out.Failures, &out.DurationUS,
	)
}

// statsByErrorClass breaks down failed sessions by error_class, showing how
// many sessions share each failure type and their aggregate cost/ops. This is
// the data behind the failure-analysis section of the Statistics page
// (SOW-0067). Only sessions with a non-empty error_class are counted.
func (p *Presenter) statsByErrorClass(ctx context.Context, where string, args []any, resp *statsResponse) error {
	q := `
SELECT IFNULL(NULLIF(s.error_class, ''), 'unknown') AS class,
       COUNT(*) AS sessions,
       IFNULL(SUM(s.op_count), 0) AS ops,
       IFNULL(SUM(s.cost_usd), 0) AS cost
FROM sessions s
WHERE ` + where + ` AND s.status = 'failed'
GROUP BY class
ORDER BY sessions DESC
LIMIT 50` // #nosec G201 G202 -- where is static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row statErrorRow
		if err := rows.Scan(&row.ErrorClass, &row.Sessions, &row.Ops, &row.CostUSD); err != nil {
			return err
		}
		resp.ByErrorClass = append(resp.ByErrorClass, row)
	}
	return rows.Err()
}
