package presenter

import (
	"context"
	"net/http"
)

// statsResponse is the JSON envelope of GET /api/stats. Every breakdown
// slice is initialised to an empty (non-nil) slice so the JSON serialises
// as [] rather than null on an empty result set.
type statsResponse struct {
	Totals   statsTotals     `json:"totals"`
	ByModel  []statModelRow  `json:"by_model"`
	ByTool   []statToolRow   `json:"by_tool"`
	ByAgent  []statAgentRow  `json:"by_agent"`
	ByStatus []statStatusRow `json:"by_status"`
	BySource []statSourceRow `json:"by_source"`
}

type statsTotals struct {
	SessionCount int64   `json:"session_count"`
	TurnCount    int64   `json:"turn_count"`
	OpCount      int64   `json:"op_count"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
	CostUSD      float64 `json:"cost_usd"`
	Failures     int64   `json:"failures"`
	DurationUS   int64   `json:"duration_us"`
}

type statModelRow struct {
	Name      string  `json:"name"`
	Provider  string  `json:"provider"`
	Calls     int64   `json:"calls"`
	TokensIn  int64   `json:"tokens_in"`
	TokensOut int64   `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
	Failures  int64   `json:"failures"`
	PctOfCost float64 `json:"pct_of_cost"`
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
	Name          string  `json:"name"`
	Sessions      int64   `json:"sessions"`
	Failures      int64   `json:"failures"`
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	CostUSD       float64 `json:"cost_usd"`
	PctOfSessions float64 `json:"pct_of_sessions"`
}

type statStatusRow struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type statSourceRow struct {
	Source   string `json:"source"`
	Sessions int64  `json:"sessions"`
	Failures int64  `json:"failures"`
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

	where, args := f.whereClause("s")
	// sessionSet selects the ids of the matching sessions; the ops-based
	// breakdowns join against it so only ops belonging to the filtered
	// sessions are counted.
	sessionSet := "SELECT s.id FROM sessions s WHERE " + where

	resp := statsResponse{
		ByModel:  []statModelRow{},
		ByTool:   []statToolRow{},
		ByAgent:  []statAgentRow{},
		ByStatus: []statStatusRow{},
		BySource: []statSourceRow{},
	}

	if err := p.statsTotals(ctx, where, args, &resp.Totals); err != nil {
		p.writeDBError(w, r, ctx, "stats.totals", err)
		return
	}
	if err := p.statsByStatus(ctx, where, args, &resp); err != nil {
		p.writeDBError(w, r, ctx, "stats.by_status", err)
		return
	}
	if err := p.statsBySource(ctx, where, args, &resp); err != nil {
		p.writeDBError(w, r, ctx, "stats.by_source", err)
		return
	}
	if err := p.statsByAgent(ctx, where, args, resp.Totals.SessionCount, &resp); err != nil {
		p.writeDBError(w, r, ctx, "stats.by_agent", err)
		return
	}
	if err := p.statsByModel(ctx, sessionSet, args, &resp); err != nil {
		p.writeDBError(w, r, ctx, "stats.by_model", err)
		return
	}
	if err := p.statsByTool(ctx, sessionSet, args, &resp); err != nil {
		p.writeDBError(w, r, ctx, "stats.by_tool", err)
		return
	}

	writeJSON(w, r, p.logger, http.StatusOK, resp)
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
    IFNULL(SUM(s.cost_usd), 0),
    IFNULL(SUM(s.failure_count), 0),
    IFNULL(SUM(CASE WHEN s.end_ts IS NOT NULL THEN s.end_ts - s.start_ts ELSE 0 END), 0)
FROM sessions s WHERE ` + where // #nosec G201 G202 -- where is static SQL + ?-placeholders; values bound via args
	return p.db.QueryRowContext(ctx, q, args...).Scan( // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
		&out.SessionCount, &out.TurnCount, &out.OpCount,
		&out.TokensIn, &out.TokensOut, &out.CostUSD, &out.Failures, &out.DurationUS,
	)
}
