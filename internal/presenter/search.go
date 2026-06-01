package presenter

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Full-text search endpoint (GET /api/search, rest-api.md §GET /api/search).
// Backed by the content-owning FTS5 tables fts_ops / fts_logs (migration 0006).
// The operator's `q` is the FTS5 MATCH argument and is ALWAYS bound as a `?`
// placeholder: FTS5 query syntax (AND/OR/NEAR/prefix*/"phrase") is intentionally
// exposed, but because the value never reaches the SQL text there is no
// injection surface. Every parseSessionFilter value is likewise `?`-bound.

const (
	// defaultSearchLimit / maxSearchLimit bound ?limit (rest-api.md §GET
	// /api/search: default 50, max 200). Unlike the list endpoints (which 400 a
	// limit over their max), search CLAMPS to the ceiling — mirroring the
	// stats-top ?n clamp — so a UI that asks for "more" gets the max rather than
	// an error.
	defaultSearchLimit = 50
	maxSearchLimit     = 200

	// searchSort / searchOrder pin the ordering a search cursor is valid for.
	// FTS5 ranked results have no stable keyset key (bm25 is a computed float
	// with arbitrary tie-breaking and the docid order is not a queryable
	// column), so the search cursor is OFFSET-style: it carries an offset into
	// the fingerprint-pinned ranked result set rather than a (ts,id) watermark
	// (rest-api.md §GET /api/search: cursor is "offset-style; mirrors
	// /api/sessions/:id/logs" — it reuses the logs cursor MACHINERY, the opaque
	// base64url pageCursor + the fingerprint-stability guard + the strict
	// decode, while the watermark is an offset). These constants are baked into
	// every minted cursor and checked on replay so a foreign cursor (e.g. a
	// sessions cursor, sort=start_ts) cannot be replayed here.
	searchSort  = "search"
	searchOrder = "rank"
	// searchCursorID is the sentinel ID a search cursor carries. decodeCursor
	// requires a non-empty ID (a structural completeness check shared with the
	// keyset endpoints); the offset itself lives in pageCursor.TS, so ID is a
	// fixed non-empty marker rather than a row id.
	searchCursorID = "off"
)

// searchOpRow is one matched op in the GET /api/search response. rank is the
// BM25 score (negative; nearer zero = less relevant), snippet the matched
// excerpt for display.
type searchOpRow struct {
	OpID      string  `json:"op_id"`
	SessionID string  `json:"session_id"`
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Model     string  `json:"model"`
	Snippet   string  `json:"snippet"`
	Rank      float64 `json:"rank"`
}

// searchLogRow is one matched log in the GET /api/search response.
type searchLogRow struct {
	LogID     int64   `json:"log_id"`
	SessionID string  `json:"session_id"`
	OpID      *string `json:"op_id"`
	Severity  string  `json:"severity"`
	TS        int64   `json:"ts"`
	Snippet   string  `json:"snippet"`
	Rank      float64 `json:"rank"`
}

// searchResponse is the JSON envelope of GET /api/search. Ops/Logs are
// initialised non-nil so an empty result serialises as [] not null.
type searchResponse struct {
	Ops         []searchOpRow  `json:"ops"`
	Logs        []searchLogRow `json:"logs"`
	LogsIndexed bool           `json:"logs_indexed"`
	NextCursor  string         `json:"next_cursor,omitempty"`
}

// handleSearch answers GET /api/search: BM25-ranked full-text matches over ops
// and logs, filtered by the same parseSessionFilter dimensions as the list
// endpoints. Mirrors handleStats's guard → parse → withQueryTimeout → query →
// writeJSON/writeDBError/writeBadFilter shape.
func (p *Presenter) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	q := r.URL.Query()
	match, err := parseSearchQuery(q.Get("q"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	// Search OWNS q, limit, and cursor; the shared parseSessionFilter must not
	// see them. q here is the FTS5 MATCH text, NOT the session list's
	// agent_name LIKE filter (which parseSessionFilter would otherwise apply,
	// silently excluding every row). limit/cursor have search-specific
	// semantics (clamp, not 400; offset cursor, not keyset), so they are parsed
	// separately below. The remaining params (from/to/agents/models/tools/
	// sources/status) flow through parseSessionFilter unchanged.
	f, err := parseSessionFilter(sessionFilterValues(q), p.now())
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	limit := parseSearchLimit(q.Get("limit"))
	offset, err := parseSearchCursor(q.Get("cursor"), match, f)
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	resp := searchResponse{Ops: []searchOpRow{}, Logs: []searchLogRow{}}

	if resp.Ops, err = p.searchOps(ctx, f, match, limit, offset); err != nil {
		p.writeDBError(w, r, ctx, "search.ops", err)
		return
	}

	// logs_indexed reflects the per-source fts5_index_logs flag over the
	// in-scope source set; when false the fts_logs query is skipped entirely.
	indexed, err := p.logsIndexedInScope(ctx, f)
	if err != nil {
		p.writeDBError(w, r, ctx, "search.logs_indexed", err)
		return
	}
	resp.LogsIndexed = indexed
	if indexed {
		if resp.Logs, err = p.searchLogs(ctx, f, match, limit, offset); err != nil {
			p.writeDBError(w, r, ctx, "search.logs", err)
			return
		}
	}

	resp.NextCursor = searchNextCursor(len(resp.Ops), len(resp.Logs), limit, offset, match, f)
	writeJSON(w, r, p.logger, http.StatusOK, resp)
}

// sessionFilterValues returns a shallow copy of v with the search-owned keys
// (q, limit, cursor) removed, so the shared parseSessionFilter parses only the
// dimensions it shares with the list endpoints. Stripping q stops it being
// applied as the agent_name LIKE filter (it is the FTS5 MATCH text here);
// stripping limit/cursor stops their session-list semantics (a >1000 limit is
// a 400 there but clamps here, and the keyset cursor format differs from the
// search offset cursor). The copy is shallow (the per-key slices are shared,
// never mutated) so it is cheap.
func sessionFilterValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		switch k {
		case "q", "limit", "cursor":
			// search-owned; do not hand to parseSessionFilter.
		default:
			out[k] = vals
		}
	}
	return out
}

// parseSearchQuery validates the required `q` param: it must be non-empty after
// trimming, and must carry no ASCII control byte (checked on the RAW value
// before the trim, so a leading/trailing control byte cannot be silently
// trimmed away — same rule as rejectControlChars elsewhere). The trimmed value
// is the FTS5 MATCH argument (bound `?` by the caller).
func parseSearchQuery(raw string) (string, error) {
	if err := rejectControlChars("q", raw); err != nil {
		return "", err
	}
	q := strings.TrimSpace(raw)
	if q == "" {
		return "", wrapBadFilter("q is required and must be non-empty")
	}
	return q, nil
}

// parseSearchLimit clamps ?limit to [1, maxSearchLimit] with a default of
// defaultSearchLimit. A non-integer or out-of-range value clamps rather than
// erroring (rest-api.md §GET /api/search: "default 50, max 200"; mirrors the
// stats-top ?n clamp).
func parseSearchLimit(s string) int {
	if s == "" {
		return defaultSearchLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return defaultSearchLimit
	}
	if n > maxSearchLimit {
		return maxSearchLimit
	}
	return n
}

// searchFingerprint binds a search cursor to its full result-defining query:
// the MATCH text + the session filter fingerprint + the fixed search ordering.
// A cursor replayed against a changed q or any changed filter mismatches and is
// rejected (parseSearchCursor), so an offset minted on one ranked set can never
// be applied to a different one (which would silently skip or duplicate rows).
// It reuses the length-prefixed encoding (writeLP) the keyset fingerprints use,
// so two distinct queries can never collide.
func searchFingerprint(match string, f sessionFilter) string {
	var b strings.Builder
	writeLP(&b, "q")
	writeLP(&b, match)
	writeLP(&b, "sort")
	writeLP(&b, searchSort)
	writeLP(&b, "order")
	writeLP(&b, searchOrder)
	writeLP(&b, "filter")
	writeLP(&b, f.fingerprint())
	return b.String()
}

// parseSearchCursor decodes the opaque ?cursor into the page offset. An absent
// cursor means "first page" (offset 0). Two guards run in order, mirroring the
// logs endpoint: first the explicit sort/order check (so a foreign cursor gets
// a precise cross-endpoint message), then the fingerprint comparison (so a
// changed q/filter is a loud 400, not silent corruption). The offset lives in
// pageCursor.TS; it is validated as a positive int64 (a real next_cursor always
// carries offset >= limit >= 1, and decodeCursor already rejects TS==0).
func parseSearchCursor(c, match string, f sessionFilter) (int64, error) {
	if c == "" {
		return 0, nil
	}
	cur, decErr := decodeCursor(c)
	if decErr != nil {
		return 0, wrapBadFilter("cursor is malformed")
	}
	if cur.Sort != searchSort || cur.Order != searchOrder {
		return 0, wrapBadFilter("cursor does not match this endpoint's ordering; restart pagination")
	}
	if cur.FP != searchFingerprint(match, f) {
		return 0, wrapBadFilter("cursor does not match the current query filters; restart pagination")
	}
	if cur.TS < 1 {
		return 0, wrapBadFilter("cursor is malformed")
	}
	return cur.TS, nil
}

// searchNextCursor mints the next page's opaque cursor, or "" when neither the
// ops nor the logs result filled the page (no further rows on EITHER side).
// Pagination advances BOTH arrays by the same offset (rest-api.md §GET
// /api/search), so a further page exists when either array returned a full
// `limit` rows; the next offset is the current offset + limit.
func searchNextCursor(nOps, nLogs, limit int, offset int64, match string, f sessionFilter) string {
	if nOps < limit && nLogs < limit {
		return ""
	}
	return pageCursor{
		TS:    offset + int64(limit),
		ID:    searchCursorID,
		Sort:  searchSort,
		Order: searchOrder,
		FP:    searchFingerprint(match, f),
	}.encode()
}

// searchOps runs the fts_ops MATCH joined back to ops⋈sessions, applying the
// parseSessionFilter constraints and rendering kind/name/model. The MATCH value
// and every filter value are `?`-bound; only static SQL + the parameterized
// whereClause fragment are concatenated. ORDER BY rank = bm25 ascending = best
// first (bm25 is negative). The cursor is fully drained before return.
func (p *Presenter) searchOps(ctx context.Context, f sessionFilter, match string, limit int, offset int64) ([]searchOpRow, error) {
	where, whereArgs := f.whereClause("s")
	// snippet(fts_ops, -1, ...) lets SQLite pick the best-matching indexed
	// column for the excerpt (the same form the migration test exercises). The
	// only concatenated fragment is the parameterized whereClause (filters.go);
	// the MATCH value, filter values, limit, and offset are all ?-bound, so the
	// query carries no user input in its text — same convention as
	// stats_rollup.go's loadFoldOps.
	q := `
SELECT fts_ops.op_id, fts_ops.session_id, o.kind, o.name, IFNULL(o.model, ''),
       snippet(fts_ops, -1, '[', ']', '…', 10) AS snip, bm25(fts_ops) AS rank
FROM fts_ops
JOIN ops o ON o.id = fts_ops.op_id
JOIN sessions s ON o.session_id = s.id
WHERE fts_ops MATCH ? AND ` + where + `
ORDER BY rank
LIMIT ? OFFSET ?` // #nosec G201 G202 -- static SQL + ?-placeholders; where is the parameterized whereClause (filters.go)
	args := make([]any, 0, len(whereArgs)+3)
	args = append(args, match)
	args = append(args, whereArgs...)
	args = append(args, limit, offset)

	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]searchOpRow, 0, limit)
	for rows.Next() {
		var row searchOpRow
		if err := rows.Scan(&row.OpID, &row.SessionID, &row.Kind, &row.Name, &row.Model, &row.Snippet, &row.Rank); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// searchLogs runs the fts_logs MATCH joined back to log_entries⋈sessions,
// applying the parseSessionFilter constraints. Logs are session-scoped (the
// ingester's only fts_logs writer sets session_id), so the join chain is
// fts_logs.log_id → log_entries.id → sessions.id → sources.id; the filter binds
// to the session/source exactly as searchOps does. MATCH + all filter values
// are `?`-bound. ORDER BY rank = best first. The cursor is fully drained.
func (p *Presenter) searchLogs(ctx context.Context, f sessionFilter, match string, limit int, offset int64) ([]searchLogRow, error) {
	where, whereArgs := f.whereClause("s")
	// The only concatenated fragment is the parameterized whereClause
	// (filters.go); the MATCH value, filter values, limit, and offset are all
	// ?-bound — same convention as searchOps / stats_rollup.go.
	q := `
SELECT fts_logs.log_id, fts_logs.session_id, fts_logs.op_id, fts_logs.severity, fts_logs.ts,
       snippet(fts_logs, -1, '[', ']', '…', 10) AS snip, bm25(fts_logs) AS rank
FROM fts_logs
JOIN log_entries le ON le.id = fts_logs.log_id
JOIN sessions s ON le.session_id = s.id
WHERE fts_logs MATCH ? AND ` + where + `
ORDER BY rank
LIMIT ? OFFSET ?` // #nosec G201 G202 -- static SQL + ?-placeholders; where is the parameterized whereClause (filters.go)
	args := make([]any, 0, len(whereArgs)+3)
	args = append(args, match)
	args = append(args, whereArgs...)
	args = append(args, limit, offset)

	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]searchLogRow, 0, limit)
	for rows.Next() {
		var (
			row  searchLogRow
			opID sql.NullString
		)
		if err := rows.Scan(&row.LogID, &row.SessionID, &opID, &row.Severity, &row.TS, &row.Snippet, &row.Rank); err != nil {
			return nil, err
		}
		if opID.Valid {
			v := opID.String
			row.OpID = &v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// logsIndexedInScope reports whether AT LEAST ONE in-scope source has
// fts5_index_logs=1 (rest-api.md §GET /api/search §logs_indexed). In-scope = the
// ?sources set when present, else ALL sources. This is the exact set whose logs
// the search could return, so the flag lets the client distinguish "no log
// matches" from "logs not indexed". When no source is in scope (an empty store,
// or a ?sources= naming only unknown ids) it is false. The `sources` values are
// `?`-bound.
func (p *Presenter) logsIndexedInScope(ctx context.Context, f sessionFilter) (bool, error) {
	q := `SELECT EXISTS (SELECT 1 FROM sources WHERE fts5_index_logs = 1`
	var args []any
	if c, a := inClause("id", f.source); c != "" {
		q += " AND " + c
		args = append(args, a...)
	}
	q += ")"
	var indexed bool
	// q is a static literal plus the parameterized inClause fragment
	// (filters.go: "id IN (?,?)"); the sources values are ?-bound via args.
	if err := p.db.QueryRowContext(ctx, q, args...).Scan(&indexed); err != nil { // #nosec G201 G701 -- static SQL + ?-bound args only
		return false, err
	}
	return indexed, nil
}
