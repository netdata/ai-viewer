package presenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// validSeverities is the closed set of severity tokens log_entries.severity
// can hold (data-model.md §log_entries). An unknown token in the
// ?severity filter is a 400 rather than a silently-empty result.
var validSeverities = map[string]struct{}{
	"DBG": {}, "INF": {}, "WRN": {}, "ERR": {},
}

// logsSort / logsOrder are the FIXED ordering of the logs endpoint
// (ts ASC, id ASC). They are baked into every minted logs cursor and
// validated on replay so a sessions-list cursor (sort=start_ts) cannot be
// replayed against logs, and vice-versa, even though both share the
// pageCursor shape.
const (
	logsSort  = "ts"
	logsOrder = "asc"
)

// logItem is one row of GET /api/sessions/:id/logs. Extras is the
// decoded extras_json object (nil when the column is NULL or empty).
type logItem struct {
	TS       int64          `json:"ts"`
	Severity string         `json:"severity"`
	Source   string         `json:"source"`
	OpID     *string        `json:"op_id"`
	Message  string         `json:"message"`
	Extras   map[string]any `json:"extras"`
}

// logsResponse is the JSON envelope of GET /api/sessions/:id/logs.
type logsResponse struct {
	Items      []logItem `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// handleSessionLogs answers GET /api/sessions/:id/logs: severity-filtered,
// keyset-paginated log entries ordered by (ts, id). 404 NOT_FOUND when
// the session id is unknown so the UI distinguishes "no logs" from
// "no such session".
func (p *Presenter) handleSessionLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	// Check control chars on the RAW path id before TrimSpace so a control
	// byte that is also whitespace (\t, \n, \r) is a loud 400 rather than
	// silently trimmed away into a doomed lookup (404). Mirrors the
	// query-value rule (parseSeverities → rejectControlChars).
	idRaw := r.PathValue("id")
	if err := rejectControlChars("id", idRaw); err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	id := strings.TrimSpace(idRaw)
	if id == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "session id is required", nil)
		return
	}

	// Severities first: the cursor fingerprint binds to the severity set, so
	// parseLogPaging must see the parsed filter to validate the cursor.
	severities, err := parseSeverities(r.URL.Query())
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	lf := logFilter{id: id, severities: severities}
	limit, cursor, err := parseLogPaging(r, lf)
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	exists, err := p.sessionExists(ctx, id)
	if err != nil {
		p.writeDBError(ctx, w, r, "session.logs.exists", err)
		return
	}
	if !exists {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "session not found", map[string]any{"id": id})
		return
	}

	items, next, err := p.queryLogs(ctx, lf, limit, cursor)
	if err != nil {
		p.writeDBError(ctx, w, r, "session.logs.query", err)
		return
	}
	writeJSON(w, r, p.logger, logsResponse{Items: items, NextCursor: next})
}

// logFilter is the result-defining filter of GET /api/sessions/:id/logs:
// the session id plus the (validated) severity set. The ordering is the
// fixed (ts, asc) baked into logsSort/logsOrder. Its fingerprint binds a
// minted cursor to this exact query so replaying it with a changed severity
// set (a different result set) is rejected rather than silently skipping or
// duplicating rows.
type logFilter struct {
	id         string
	severities []string
}

// fingerprint returns the canonical length-prefixed string of the logs query
// (path id + sorted severity set + the fixed ordering). Used both when minting
// next_cursor (logsPage) and when validating an incoming cursor
// (parseLogPaging), and compared byte-for-byte, so the two can never drift. The
// severity slice is sorted before encoding so ?severity=WRN,ERR and
// ?severity=ERR,WRN fingerprint identically. Tokens are length-prefixed
// (writeLP / writeSortedDim) so no value content can forge a field boundary;
// because the cursor carries this string rather than a fixed-width digest, two
// distinct queries can never collide — the same exact-by-construction encoding
// the sessions fingerprint uses.
func (lf logFilter) fingerprint() string {
	var b strings.Builder
	writeLP(&b, "id")
	writeLP(&b, lf.id)
	writeLP(&b, "sort")
	writeLP(&b, logsSort)
	writeLP(&b, "order")
	writeLP(&b, logsOrder)
	writeSortedDim(&b, "severity", lf.severities)
	return b.String()
}

// logsCursor is the validated keyset watermark of a logs page: the last
// row's (ts, id). Unlike the wire-shape pageCursor (whose id is a string),
// id here is the int64 log_entries.id row id, parsed and validated once in
// parseLogPaging so the keyset comparison binds an integer rather than a
// string SQLite would coerce by affinity. present is false for the first
// page (no keyset narrowing).
type logsCursor struct {
	ts      int64
	id      int64
	present bool
}

// parseLogPaging reads limit + cursor for the logs endpoint, reusing the
// shared validation rules (default 100, max 1000, opaque cursor). The
// cursor is bound to lf.fingerprint() so replaying it against a changed
// severity set is rejected. The explicit (ts, asc) ordering check runs
// before the fingerprint comparison so a foreign cursor minted by the
// sessions endpoint gets a precise cross-endpoint message rather than the
// generic filter-mismatch one (no double-reporting: the two checks return
// on distinct conditions). Because the logs keyset id is the
// log_entries.id INTEGER column, the cursor id is validated as a decimal
// int64 AFTER the fingerprint check; a non-numeric id is a BAD_REQUEST
// rather than a string silently coerced into the (ts, id) comparison. The
// validated int64 is returned in logsCursor so buildLogsQuery binds an
// integer.
func parseLogPaging(r *http.Request, lf logFilter) (limit int, cursor logsCursor, err error) {
	limit = defaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		n, convErr := strconv.Atoi(l)
		if convErr != nil || n < 1 {
			return 0, cursor, wrapBadFilter("limit must be a positive integer")
		}
		if n > maxLimit {
			return 0, cursor, wrapBadFilter("limit exceeds maximum of 1000")
		}
		limit = n
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		cur, decErr := decodeCursor(c)
		if decErr != nil {
			return 0, cursor, wrapBadFilter("cursor is malformed")
		}
		// Logs have a single fixed ordering; reject any cursor minted under
		// a different sort/order (e.g. a /api/sessions cursor) so it cannot
		// be replayed here with a mismatched comparison direction.
		if cur.Sort != logsSort || cur.Order != logsOrder {
			return 0, cursor, wrapBadFilter("cursor does not match this endpoint's ordering; restart pagination")
		}
		// Bind the cursor to the full logs query (id + severity set).
		if cur.FP != lf.fingerprint() {
			return 0, cursor, wrapBadFilter("cursor does not match the current query filters; restart pagination")
		}
		// The logs keyset id is the log_entries.id INTEGER row id; reject a
		// non-decimal-int64 id loudly instead of letting SQLite string→int
		// affinity yield a wrong/empty page.
		id, idErr := strconv.ParseInt(cur.ID, 10, 64)
		if idErr != nil {
			return 0, cursor, wrapBadFilter("cursor is malformed")
		}
		cursor = logsCursor{ts: cur.TS, id: id, present: true}
	}
	return limit, cursor, nil
}

// parseSeverities parses the ?severity array param under the SAME rule as the
// session array filters (rest-api.md §Conventions): an absent key is no
// constraint (all severities); a key that is present but whose every element
// is empty (`?severity=` or `?severity=,`) is a BAD_REQUEST, NOT a silent
// "no filter". Each surviving token is validated against the closed severity
// set. Reusing parseRequiredNonEmptyArray keeps logs severity consistent with
// the other array dimensions instead of being a quiet exception.
func parseSeverities(v url.Values) ([]string, error) {
	sevs, err := parseRequiredNonEmptyArray(v, "severity")
	if err != nil {
		return nil, err
	}
	for _, s := range sevs {
		if _, ok := validSeverities[s]; !ok {
			return nil, wrapBadFilter("severity must be one of DBG, INF, WRN, ERR")
		}
	}
	return sevs, nil
}

// sessionExists reports whether a sessions row with the given id exists.
func (p *Presenter) sessionExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&one)
	if err != nil {
		if isNoRows(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// logScanRow pairs a decoded logItem with its integer row id; the id is the
// keyset tiebreaker used to mint the next cursor but is not part of the
// wire shape.
type logScanRow struct {
	item logItem
	id   int64
}

// buildLogsQuery renders the parameterized keyset query and its bound args
// for the logs page. Every operator value (id, severities, cursor tuple)
// is a `?` placeholder appended to args; only static SQL is concatenated.
// The cursor id is bound as an int64 (logsCursor.id, validated decimal in
// parseLogPaging) so the (ts, id) comparison uses integer semantics rather
// than SQLite string→int affinity.
func buildLogsQuery(lf logFilter, limit int, cursor logsCursor) (string, []any) {
	q := `
SELECT id, ts, severity, source, op_id, message, extras_json
FROM log_entries
WHERE session_id = ?`
	args := []any{lf.id}
	if len(lf.severities) > 0 {
		// placeholders() emits only "?,?,..."; each severity is bound via
		// args (validated against validSeverities upstream), never
		// interpolated. #nosec G202.
		q += ` AND severity IN (` + placeholders(len(lf.severities)) + `)` // #nosec G202 -- ?-placeholders only; values bound via args
		for _, s := range lf.severities {
			args = append(args, s)
		}
	}
	if cursor.present {
		// Logs are ordered ascending by (ts, id); the next page seeks
		// strictly past the cursor tuple. id is an int64 so the comparison
		// is integer, not affinity-coerced string.
		q += ` AND (ts, id) > (?, ?)`
		args = append(args, cursor.ts, cursor.id)
	}
	q += ` ORDER BY ts ASC, id ASC LIMIT ?`
	args = append(args, limit+1)
	return q, args
}

// queryLogs runs the keyset-paginated log query and returns the page plus
// the next cursor (empty when no further page exists). Selecting limit+1
// rows lets the handler detect a further page without a COUNT.
func (p *Presenter) queryLogs(ctx context.Context, lf logFilter, limit int, cursor logsCursor) ([]logItem, string, error) {
	q, args := buildLogsQuery(lf, limit, cursor)
	rows, err := p.db.QueryContext(ctx, q, args...) // #nosec G201 G701 -- q is static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()

	collected := make([]logScanRow, 0, limit)
	for rows.Next() {
		var (
			lr     logScanRow
			opID   *string
			extras *string
		)
		if err := rows.Scan(&lr.id, &lr.item.TS, &lr.item.Severity, &lr.item.Source, &opID, &lr.item.Message, &extras); err != nil {
			return nil, "", err
		}
		lr.item.OpID = opID
		lr.item.Extras = decodeExtras(extras)
		collected = append(collected, lr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	items, next := logsPage(collected, lf, limit)
	return items, next, nil
}

// logsPage trims the limit+1 fetch to the page and mints the next cursor
// (bound to the fixed logs ordering AND lf.fingerprint() — the SAME helper
// parseLogPaging validates against) only when an extra row proved a further
// page exists.
func logsPage(collected []logScanRow, lf logFilter, limit int) ([]logItem, string) {
	var next string
	if len(collected) > limit {
		collected = collected[:limit]
		last := collected[len(collected)-1]
		next = pageCursor{
			TS: last.item.TS, ID: strconv.FormatInt(last.id, 10),
			Sort: logsSort, Order: logsOrder, FP: lf.fingerprint(),
		}.encode()
	}
	items := make([]logItem, len(collected))
	for i, c := range collected {
		items[i] = c.item
	}
	return items, next
}

// decodeExtras parses the extras_json column into a map. Returns nil for
// a NULL or empty column, or when the JSON does not decode to an object
// (a malformed extras column must not break the whole log page).
func decodeExtras(extras *string) map[string]any {
	if extras == nil || *extras == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(*extras), &m); err != nil {
		return nil
	}
	return m
}
