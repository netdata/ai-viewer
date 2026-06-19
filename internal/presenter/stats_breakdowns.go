package presenter

import "context"

// The breakdown queries below concatenate the parameterized `where` /
// `sessionSet` fragment built by sessionFilter.whereClause into otherwise
// static SQL. Every operator-supplied value is bound via args as a `?`
// placeholder — no user input is interpolated (see filters.go). gosec's
// taint analysis (G201/G202) cannot prove that, so each query carries an
// inline suppression, matching the precedent in
// internal/ingest/aggregates.go.

// statsByStatus groups the filtered sessions by status. SOW-0067: each row
// also carries the aggregate cost + tokens for that status, so the comparison
// table can answer "how much did failed sessions cost". Ordered by count desc
// then status for a stable response.
func (p *Presenter) statsByStatus(ctx context.Context, where string, args []any, resp *statsResponse) error {
	q := `
SELECT s.status, COUNT(*),
       IFNULL(SUM(s.cost_usd), 0),
       IFNULL(SUM(s.tokens_in), 0), IFNULL(SUM(s.tokens_out), 0)
FROM sessions s WHERE ` + where + `
GROUP BY s.status ORDER BY COUNT(*) DESC, s.status ASC` // #nosec G201 G202 -- static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row statStatusRow
		if err := rows.Scan(&row.Status, &row.Count, &row.CostUSD, &row.TokensIn, &row.TokensOut); err != nil {
			return err
		}
		resp.ByStatus = append(resp.ByStatus, row)
	}
	return rows.Err()
}

// statsBySource groups the filtered sessions by source_id with full economics
// (SOW-0067 harness-efficiency): sessions, failures, cost, tokens, cache,
// op_count, plus the source_format label (joined from sources) so the table
// shows the harness name. A LEFT JOIN + IFNULL is used defensively so a session
// whose source row is (theoretically) missing still appears with an empty
// format label, rather than being silently dropped. GROUP BY includes both
// s.source_id and src.format so a (theoretical) format spanning multiple source
// ids stays correctly split.
func (p *Presenter) statsBySource(ctx context.Context, where string, args []any, resp *statsResponse) error {
	q := `
SELECT s.source_id, IFNULL(src.format, ''), COUNT(*),
       SUM(CASE WHEN s.status = 'failed' THEN 1 ELSE 0 END),
       IFNULL(SUM(s.cost_usd), 0),
       IFNULL(SUM(s.tokens_in), 0), IFNULL(SUM(s.tokens_out), 0),
       IFNULL(SUM(s.tokens_cache_read), 0),
       IFNULL(SUM(s.op_count), 0)
FROM sessions s LEFT JOIN sources src ON s.source_id = src.id WHERE ` + where + `
GROUP BY s.source_id, src.format ORDER BY COUNT(*) DESC, s.source_id ASC` // #nosec G201 G202 -- static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row statSourceRow
		if err := rows.Scan(&row.Source, &row.Format, &row.Sessions, &row.Failures,
			&row.CostUSD, &row.TokensIn, &row.TokensOut, &row.TokensCacheRead, &row.OpCount); err != nil {
			return err
		}
		resp.BySource = append(resp.BySource, row)
	}
	return rows.Err()
}

// statsByAgent groups the filtered sessions by agent_name. pct_of_sessions
// is the share of the filtered session set; computed in Go from
// totalSessions so the percentages sum to 1.0 across the breakdown.
// SOW-0067: each row also carries cache tokens (session rollups store them)
// so the comparison table can show a per-agent cache-hit ratio. Sessions
// with a NULL agent_name collapse to the empty string so they still appear.
func (p *Presenter) statsByAgent(ctx context.Context, where string, args []any, totalSessions int64, resp *statsResponse) error {
	q := `
SELECT IFNULL(s.agent_name, ''), COUNT(*),
       SUM(CASE WHEN s.status = 'failed' THEN 1 ELSE 0 END),
       IFNULL(SUM(s.tokens_in), 0), IFNULL(SUM(s.tokens_out), 0),
       IFNULL(SUM(s.tokens_cache_read), 0), IFNULL(SUM(s.tokens_cache_write), 0),
       IFNULL(SUM(s.cost_usd), 0)
FROM sessions s WHERE ` + where + `
GROUP BY IFNULL(s.agent_name, '') ORDER BY COUNT(*) DESC, s.agent_name ASC` // #nosec G201 G202 -- static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row statAgentRow
		if err := rows.Scan(&row.Name, &row.Sessions, &row.Failures,
			&row.TokensIn, &row.TokensOut, &row.TokensCacheRead, &row.TokensCacheWrite,
			&row.CostUSD); err != nil {
			return err
		}
		row.PctOfSessions = ratio(row.Sessions, totalSessions)
		resp.ByAgent = append(resp.ByAgent, row)
	}
	return rows.Err()
}

// statsByModel rolls up the llm ops belonging to the filtered session set
// by (model, provider). pct_of_cost is each model's share of the total
// llm cost across the breakdown, computed in Go so it sums to 1.0 (when
// total cost > 0). SOW-0067: each row also carries cache tokens (op-level)
// and summed llm-op duration, so the comparison table can show a per-model
// cache-hit ratio AND "which model is slowest". sessionSet is the
// parameterized id subquery built by the handler.
func (p *Presenter) statsByModel(ctx context.Context, sessionSet string, args []any, resp *statsResponse) error {
	q := `
SELECT IFNULL(o.model, ''), IFNULL(o.provider, ''),
       COUNT(*), IFNULL(SUM(o.tokens_in), 0), IFNULL(SUM(o.tokens_out), 0),
       IFNULL(SUM(o.tokens_cache_read), 0), IFNULL(SUM(o.tokens_cache_write), 0),
       IFNULL(SUM(o.cost_usd), 0),
       SUM(CASE WHEN o.status = 'failed' THEN 1 ELSE 0 END),
       IFNULL(SUM(o.duration_us), 0)
FROM ops o
WHERE o.kind = 'llm' AND o.session_id IN (` + sessionSet + `)
GROUP BY o.model, o.provider
ORDER BY SUM(o.cost_usd) DESC, o.model ASC` // #nosec G201 G202 -- static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var totalCost float64
	for rows.Next() {
		var row statModelRow
		if err := rows.Scan(&row.Name, &row.Provider, &row.Calls,
			&row.TokensIn, &row.TokensOut, &row.TokensCacheRead, &row.TokensCacheWrite,
			&row.CostUSD, &row.Failures, &row.DurationUS); err != nil {
			return err
		}
		totalCost += row.CostUSD
		resp.ByModel = append(resp.ByModel, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range resp.ByModel {
		resp.ByModel[i].PctOfCost = ratioF(resp.ByModel[i].CostUSD, totalCost)
	}
	return nil
}

// statsByTool rolls up the tool ops belonging to the filtered session set
// by (tool_namespace, name). pct_of_calls is each tool's share of the
// total tool-call count across the breakdown.
func (p *Presenter) statsByTool(ctx context.Context, sessionSet string, args []any, resp *statsResponse) error {
	q := `
SELECT IFNULL(o.tool_namespace, ''), o.name,
       COUNT(*), SUM(CASE WHEN o.status = 'failed' THEN 1 ELSE 0 END),
       IFNULL(SUM(o.duration_us), 0)
FROM ops o
WHERE o.kind = 'tool' AND o.session_id IN (` + sessionSet + `)
GROUP BY o.tool_namespace, o.name
ORDER BY COUNT(*) DESC, o.name ASC` // #nosec G201 G202 -- static SQL + ?-placeholders; values bound via args
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var totalCalls int64
	for rows.Next() {
		var row statToolRow
		if err := rows.Scan(&row.Namespace, &row.Name, &row.Calls,
			&row.Failures, &row.TotalUS); err != nil {
			return err
		}
		totalCalls += row.Calls
		resp.ByTool = append(resp.ByTool, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range resp.ByTool {
		resp.ByTool[i].PctOfCalls = ratio(resp.ByTool[i].Calls, totalCalls)
	}
	return nil
}

// ratio returns part/total as a float in [0,1], or 0 when total is 0.
func ratio(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// ratioF is the float variant for cost shares.
func ratioF(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return part / total
}
