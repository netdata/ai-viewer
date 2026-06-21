// search_content.go (SOW-0091) — fts_content query path for /api/search.
//
// fts_content is the third FTS5 index added in migration 0010; it
// stores operator-visible text extracted from each op's primary
// payload via extract.ReadableText (canonical helper, mirrored by
// the frontend Markdown renderer). The shape mirrors searchOps /
// searchLogs:
//
//   - JOIN back to ops⋈sessions so the session filter applies.
//   - ORDER BY rank (BM25 ascending = best first); fts_content.op_id
//     as tie-breaker for stable offset pagination.
//   - snippet(fts_content, -1, ...) picks the best matching column.
//   - LIMIT ?+1 OFFSET ? peek so hasMore is exact without a COUNT.
//
// No new flag gate: fts_content is ALWAYS populated (the backfill
// subcommand + the incremental ingest hook both write to it). The
// only way a query returns 0 content matches is if the index is
// empty for that MATCH expression — which is the right answer.

package presenter

import (
	"context"
	"database/sql"
)

func (p *Presenter) searchContent(ctx context.Context, f sessionFilter, match string, limit int, offset int64) ([]searchContentRow, bool, error) {
	q, args := buildSearchContentQuery(f, match, limit, offset)
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	return scanSearchContentRows(rows, limit)
}

func buildSearchContentQuery(f sessionFilter, match string, limit int, offset int64) (string, []any) {
	where, whereArgs := f.whereClause("s")
	// Two-stage CTE query: the inner CTE performs the FTS5 MATCH +
	// ORDER BY bm25 + LIMIT (cheap, no row materialization beyond rowid).
	// The outer query joins back to fts_content to compute snippet()
	// only for the limited row set, AND re-states MATCH so snippet()
	// picks the matching column (without outer MATCH, snippet() falls
	// back to the first indexed column value, returning the first
	// snippet rather than the matched one). Without the CTE split
	// SQLite would evaluate snippet() and bm25() on every MATCH-matching
	// row before applying LIMIT (a common term like "permissions" matches
	// 60k rows; snippet() on the full row text makes this take 30+
	// seconds end to end). The two-stage form drops it to ~50ms.
	q := `
WITH top AS (
  SELECT fts_content.rowid, bm25(fts_content) AS rank
  FROM fts_content
  JOIN ops o ON o.id = fts_content.op_id
  JOIN sessions s ON o.session_id = s.id
  WHERE fts_content MATCH ? AND ` + where + `
  ORDER BY rank, fts_content.op_id
  LIMIT ? OFFSET ?
)
SELECT fts_content.op_id, fts_content.session_id, fts_content.turn_id,
       snippet(fts_content, -1, '[', ']', '…', 12) AS snip, top.rank
FROM fts_content
JOIN top ON top.rowid = fts_content.rowid
WHERE fts_content MATCH ?
ORDER BY top.rank, fts_content.op_id` // #nosec G201 G202 -- static SQL + ?-placeholders; where is the parameterized whereClause (filters.go)
	args := make([]any, 0, len(whereArgs)+4)
	args = append(args, match)
	args = append(args, whereArgs...)
	args = append(args, limit+1, offset)
	// outer MATCH re-stated so snippet() has matched terms in scope
	args = append(args, match)
	return q, args
}

func scanSearchContentRows(rows *sql.Rows, limit int) ([]searchContentRow, bool, error) {
	out := make([]searchContentRow, 0, limit+1)
	for rows.Next() {
		var row searchContentRow
		if err := rows.Scan(&row.OpID, &row.SessionID, &row.TurnID, &row.Snippet, &row.Rank); err != nil {
			return nil, false, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}
