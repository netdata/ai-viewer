package presenter

import (
	"context"
	"database/sql"
)

func (p *Presenter) searchLogs(ctx context.Context, f sessionFilter, match string, limit int, offset int64) ([]searchLogRow, bool, error) {
	q, args := buildSearchLogsQuery(f, match, limit, offset)
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	return scanSearchLogRows(rows, limit)
}

func buildSearchLogsQuery(f sessionFilter, match string, limit int, offset int64) (string, []any) {
	where, whereArgs := f.whereClause("s")
	// Two-stage CTE query (same rationale as searchOps / searchContent):
	// the inner CTE does MATCH + ORDER BY bm25 + LIMIT (cheap, no row
	// materialization beyond rowid); the outer query joins back to
	// fts_logs to compute snippet() only for the limited row set. The
	// outer query re-states MATCH so snippet() picks the matching
	// column (without outer MATCH, snippet() falls back to the first
	// indexed column value). Without the CTE split, snippet() runs on
	// every MATCH-matching row, dominating total query time on
	// common-term queries (tens of seconds).
	q := `
WITH top AS (
  SELECT fts_logs.rowid, bm25(fts_logs) AS rank
  FROM fts_logs
  JOIN log_entries le ON le.id = fts_logs.log_id
  JOIN sessions s ON le.session_id = s.id
  JOIN sources src ON s.source_id = src.id
  WHERE fts_logs MATCH ? AND src.fts5_index_logs = 1 AND ` + where + `
  ORDER BY rank, fts_logs.log_id
  LIMIT ? OFFSET ?
)
SELECT fts_logs.log_id, fts_logs.session_id, fts_logs.op_id, fts_logs.severity, fts_logs.ts,
       snippet(fts_logs, -1, '[', ']', '…', 10) AS snip, top.rank
FROM fts_logs
JOIN top ON top.rowid = fts_logs.rowid
WHERE fts_logs MATCH ?
ORDER BY top.rank, fts_logs.log_id` // #nosec G201 G202 -- static SQL + ?-placeholders; where is the parameterized whereClause (filters.go)
	args := make([]any, 0, len(whereArgs)+4)
	args = append(args, match)
	args = append(args, whereArgs...)
	args = append(args, limit+1, offset)
	// outer MATCH re-stated so snippet() has matched terms in scope
	args = append(args, match)
	return q, args
}

func scanSearchLogRows(rows *sql.Rows, limit int) ([]searchLogRow, bool, error) {
	out := make([]searchLogRow, 0, limit+1)
	for rows.Next() {
		row, err := scanSearchLogRow(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	page, hasMore := trimSearchLogRows(out, limit)
	return page, hasMore, nil
}

func scanSearchLogRow(rows *sql.Rows) (searchLogRow, error) {
	var (
		row  searchLogRow
		opID sql.NullString
	)
	err := rows.Scan(&row.LogID, &row.SessionID, &opID, &row.Severity, &row.TS, &row.Snippet, &row.Rank)
	if err != nil {
		return searchLogRow{}, err
	}
	row.OpID = searchLogOpID(opID)
	return row, nil
}

func searchLogOpID(opID sql.NullString) *string {
	if !opID.Valid {
		return nil
	}
	v := opID.String
	return &v
}

func trimSearchLogRows(rows []searchLogRow, limit int) ([]searchLogRow, bool) {
	if len(rows) <= limit {
		return rows, false
	}
	return rows[:limit], true
}

func (p *Presenter) logsIndexedInScope(ctx context.Context, f sessionFilter) (bool, error) {
	q := `SELECT EXISTS (SELECT 1 FROM sources WHERE fts5_index_logs = 1`
	var args []any
	if c, a := inClause("id", f.source); c != "" {
		q += " AND " + c
		args = append(args, a...)
	}
	q += ")"
	var indexed bool
	if err := p.db.QueryRowContext(ctx, q, args...).Scan(&indexed); err != nil { // #nosec G201 G701 -- static SQL + ?-bound args only
		return false, err
	}
	return indexed, nil
}
