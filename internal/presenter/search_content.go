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
	q := `
SELECT fts_content.op_id, fts_content.session_id, fts_content.turn_id,
       snippet(fts_content, -1, '[', ']', '…', 12) AS snip, bm25(fts_content) AS rank
FROM fts_content
JOIN ops o ON o.id = fts_content.op_id
JOIN sessions s ON o.session_id = s.id
WHERE fts_content MATCH ? AND ` + where + `
ORDER BY rank, fts_content.op_id
LIMIT ? OFFSET ?` // #nosec G201 G202 -- static SQL + ?-placeholders; where is the parameterized whereClause (filters.go)
	args := make([]any, 0, len(whereArgs)+3)
	args = append(args, match)
	args = append(args, whereArgs...)
	args = append(args, limit+1, offset)
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
