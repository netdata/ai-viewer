package presenter

import (
	"context"
	"database/sql"
	"net/http"
)

// sessionListItem is one row of GET /api/sessions. JSON tags match
// rest-api.md §GET /api/sessions. ChildSessionCount is only meaningful
// for group=root (the UI uses it to render the expander); for group=all
// it counts the direct children of each row regardless.
type sessionListItem struct {
	ID                string  `json:"id"`
	NativeID          string  `json:"native_id"`
	RootSessionID     string  `json:"root_session_id"`
	ParentSessionID   *string `json:"parent_session_id"`
	SourceID          string  `json:"source_id"`
	Kind              string  `json:"kind"`
	AgentName         string  `json:"agent_name"`
	Model             string  `json:"model"`
	Provider          string  `json:"provider"`
	Status            string  `json:"status"`
	EffectiveStatus   string  `json:"effective_status"`
	ErrorClass        string  `json:"error_class"`
	StartTS           int64   `json:"start_ts"`
	EndTS             *int64  `json:"end_ts"`
	LastActivityTS    *int64  `json:"last_activity_ts"`
	TokensIn          int64   `json:"tokens_in"`
	TokensOut         int64   `json:"tokens_out"`
	CostUSD           float64 `json:"cost_usd"`
	TurnCount         int64   `json:"turn_count"`
	OpCount           int64   `json:"op_count"`
	FailureCount      int64   `json:"failure_count"`
	ChildSessionCount int64   `json:"child_session_count"`
}

// sessionListResponse is the JSON envelope of GET /api/sessions.
type sessionListResponse struct {
	Items      []sessionListItem `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// handleSessionsList answers GET /api/sessions: filtered, keyset-
// paginated session list. The query selects limit+1 rows so the handler
// can detect whether a further page exists and, if so, emit the
// next_cursor from the limit-th row.
func (p *Presenter) handleSessionsList(w http.ResponseWriter, r *http.Request) {
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

	items, err := p.querySessions(ctx, f)
	if err != nil {
		p.writeDBError(ctx, w, r, "sessions.list", err)
		return
	}
	writeJSON(w, r, p.logger, buildSessionsResponse(items, f))
}

// buildSessionListQuery renders the parameterized list query and its bound
// args for the given filter. where is built by sessionFilter.whereClause:
// static SQL fragments joined by AND, with every operator-supplied value
// bound through args as a `?` placeholder (no user input is interpolated
// into the SQL text — see filters.go). gosec's taint analysis (G201/G202)
// cannot prove that, so the caller's QueryContext carries the suppression.
func buildSessionListQuery(f sessionFilter) (string, []any) {
	where, args := f.whereClause("s")
	query := `
SELECT
    s.id, s.native_id, s.root_session_id, s.parent_session_id, s.source_id,
    s.kind, IFNULL(s.agent_name, ''), IFNULL(s.model, ''), IFNULL(s.provider, ''), s.status,
    s.start_ts, s.end_ts, s.last_activity_ts,
    s.tokens_in, s.tokens_out, s.cost_usd,
    s.turn_count, s.op_count, s.failure_count,
    IFNULL(s.error_class, '') AS error_class,
    (SELECT COUNT(*) FROM sessions c WHERE c.parent_session_id = s.id) AS child_session_count
FROM sessions s
WHERE ` + where

	// Default: exclude sessions with no work (0 ops + 0 turns) — stubs,
	// abandoned test fixtures, not-yet-scanned parents dominate the list
	// otherwise. include_empty=1 opts in (SOW-0063).
	if !f.includeEmpty {
		query += ` AND (s.op_count > 0 OR s.turn_count > 0)`
	}

	// Keyset narrowing: rows strictly after the cursor tuple in the sort
	// direction. Row-value comparison gives a total order on (start_ts, id).
	if f.hasCursor && !f.cursor.isZero() {
		if f.order == "asc" {
			query += ` AND (s.start_ts, s.id) > (?, ?)`
		} else {
			query += ` AND (s.start_ts, s.id) < (?, ?)`
		}
		args = append(args, f.cursor.TS, f.cursor.ID)
	}
	if f.order == "asc" {
		query += ` ORDER BY s.start_ts ASC, s.id ASC`
	} else {
		query += ` ORDER BY s.start_ts DESC, s.id DESC`
	}
	query += ` LIMIT ?`
	args = append(args, f.limit+1)
	return query, args
}

// querySessions runs the list query and scans the page (up to limit+1
// rows so the caller can detect a further page without a COUNT).
func (p *Presenter) querySessions(ctx context.Context, f sessionFilter) ([]sessionListItem, error) {
	query, args := buildSessionListQuery(f)
	rows, err := p.db.QueryContext(ctx, query, args...) // #nosec G201 G202 G701 -- query is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// SOW-0089 chunk 5a: every row's effective_status is derived from the
	// snapshot + freshness signals. Compute the wall clock once for the page
	// (microsecond resolution is plenty for a 10-minute staleness threshold)
	// and pass it to the scanner so all rows agree.
	pageNowUs := p.now().UnixMicro()

	items := make([]sessionListItem, 0, f.limit)
	for rows.Next() {
		it, scanErr := scanSessionListItem(rows, pageNowUs)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// scanSessionListItem scans one list row, mapping the nullable parent and
// end-timestamp columns onto the pointer fields of sessionListItem.
// `nowUs` is the wall clock to use when deriving `effective_status`
// (SOW-0089 chunk 5a); it MUST be the same value for every row on a page
// so a session crossing the stale threshold mid-pagination reports the same
// status on page 1 and page 2.
func scanSessionListItem(rows *sql.Rows, nowUs int64) (sessionListItem, error) {
	var (
		it        sessionListItem
		parent    sql.NullString
		endTS     sql.NullInt64
		lastActTS sql.NullInt64
	)
	if err := rows.Scan(
		&it.ID, &it.NativeID, &it.RootSessionID, &parent, &it.SourceID,
		&it.Kind, &it.AgentName, &it.Model, &it.Provider, &it.Status,
		&it.StartTS, &endTS, &lastActTS, &it.TokensIn, &it.TokensOut, &it.CostUSD,
		&it.TurnCount, &it.OpCount, &it.FailureCount, &it.ErrorClass, &it.ChildSessionCount,
	); err != nil {
		return sessionListItem{}, err
	}
	if parent.Valid {
		v := parent.String
		it.ParentSessionID = &v
	}
	if endTS.Valid {
		v := endTS.Int64
		it.EndTS = &v
	}
	if lastActTS.Valid {
		v := lastActTS.Int64
		it.LastActivityTS = &v
	}
	it.EffectiveStatus = string(deriveEffectiveStatus(
		it.Status,
		endTS.Int64,
		lastActTS.Int64,
		nowUs,
	))
	return it, nil
}

// buildSessionsResponse trims the limit+1 fetch to the page and emits the
// next_cursor (bound to the request's sort/order AND the full-query
// fingerprint) only when an extra row proved a further page exists. The
// fingerprint is minted by the SAME f.fingerprint() helper parseCursorParam
// validates against, so a cursor is accepted only when replayed against the
// identical result-defining query.
func buildSessionsResponse(items []sessionListItem, f sessionFilter) sessionListResponse {
	resp := sessionListResponse{}
	if len(items) > f.limit {
		items = items[:f.limit]
		last := items[len(items)-1]
		resp.NextCursor = pageCursor{
			TS: last.StartTS, ID: last.ID, Sort: f.sort, Order: f.order, FP: f.fingerprint(),
		}.encode()
	}
	resp.Items = items
	return resp
}
