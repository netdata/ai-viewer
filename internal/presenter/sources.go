package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// sourcesResponse is the JSON payload of GET /api/sources. The shape
// matches rest-api.md §"GET /api/sources": a list of sources with
// cursor metadata for the Sources admin panel.
type sourcesResponse struct {
	Items []sourceItem `json:"items"`
}

// sourceItem is one row from the joined sources + source_progress
// view. JSON tags use snake_case to match the rest of the REST API.
//
// LastSeq is the adapter's opaque per-source observability counter (max
// SourceSeq seen); NOT a dedup gate and NOT a portable event count. See
// healthSource in health.go for the per-adapter semantics. Comparing
// last_seq across formats is meaningless; comparing it across two
// snapshots of the same source tells you whether the writer has advanced.
//
// Meta is the adapter-owned JSON metadata blob (SOW-0024), rendered
// verbatim from sources.meta_json. The field is OMITTED (omitempty) when
// the adapter did not populate the column — absence is the "not
// populated" signal, not zero / {}.
type sourceItem struct {
	ID          string          `json:"id"`
	Format      string          `json:"format"`
	Location    string          `json:"location"`
	Enabled     bool            `json:"enabled"`
	ParseErrors int64           `json:"parse_errors"`
	LastSeenAt  *int64          `json:"last_seen_at"`
	CreatedAt   int64           `json:"created_at"`
	Cursor      string          `json:"cursor,omitempty"`
	LastSeq     int64           `json:"last_seq"`
	LastTsUS    *int64          `json:"last_ts_us"`
	UpdatedAt   *int64          `json:"updated_at"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

type sourceItemRow struct {
	id          string
	format      string
	location    string
	cursor      string
	enabled     int64
	parseErrors int64
	lastSeenAt  sql.NullInt64
	createdAt   int64
	lastSeq     int64
	lastTsUS    sql.NullInt64
	updatedAt   sql.NullInt64
	// metaJSON is the raw sources.meta_json column. Valid==false means the
	// adapter did not populate it (the "not populated" signal). See
	// healthSourceRow.metaJSON for the full contract.
	metaJSON sql.NullString
}

type sourceItemsFailureKind uint8

const (
	sourceItemsFailureNone sourceItemsFailureKind = iota
	sourceItemsFailureQuery
	sourceItemsFailureScan
	sourceItemsFailureIterate
)

type sourceItemsFailure struct {
	kind sourceItemsFailureKind
	err  error
}

type sourceItemsFailureResponse struct {
	level         slog.Level
	logMessage    string
	status        int
	code          string
	clientMessage string
}

// handleSources answers GET /api/sources. Returns every configured
// source with the matching source_progress cursor + last_seq
// observability counter (max SourceSeq seen; NOT a dedup gate). The
// Sources admin panel uses this endpoint to render its per-source
// diagnostics.
//
// The per-source Cursor field (a JSON blob of file offsets, typically
// 1–15 MB) is OMItted by default — the operator-facing Sources page
// only needs the metadata (status, parse_errors, last_seen_at). It
// inflates the response by ~10× per source. Opt in via
// ?include=cursors for the diagnostic page that needs the per-file
// resume state.
func (p *Presenter) handleSources(w http.ResponseWriter, r *http.Request) {
	// HEAD parity: same headers as GET, empty body. writeJSON skips
	// the body when r.Method == HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	includeCursors := r.URL.Query().Get("include") == "cursors"

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	items, failure := p.collectSourceItems(ctx, includeCursors)
	if failure.err != nil {
		p.writeSourcesFailure(ctx, w, r, failure)
		return
	}
	writeJSON(w, r, p.logger, sourcesResponse{Items: items})
}

func (p *Presenter) collectSourceItems(ctx context.Context, includeCursors bool) ([]sourceItem, sourceItemsFailure) {
	// When the caller opts out of cursors, the SQL projection skips the
	// cursor column entirely — saves ~10 MB of network + JSON marshaling
	// on the operator-facing Sources page.
	var q string
	if includeCursors {
		q = `
SELECT
    s.id,
    s.format,
    s.location,
    s.enabled,
    s.parse_errors,
    s.last_seen_at,
    s.created_at,
    IFNULL(sp.cursor, ''),
    IFNULL(sp.last_seq, 0),
    sp.last_ts_us,
    sp.updated_at,
    s.meta_json
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`
	} else {
		q = `
SELECT
    s.id,
    s.format,
    s.location,
    s.enabled,
    s.parse_errors,
    s.last_seen_at,
    s.created_at,
    '',
    IFNULL(sp.last_seq, 0),
    sp.last_ts_us,
    sp.updated_at,
    s.meta_json
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`
	}
	rows, err := p.db.QueryContext(ctx, q)
	if err != nil {
		return nil, sourceItemsFailure{kind: sourceItemsFailureQuery, err: err}
	}
	defer func() { _ = rows.Close() }()
	return readSourceItemRows(rows, p.logger)
}

func readSourceItemRows(rows *sql.Rows, logger *slog.Logger) ([]sourceItem, sourceItemsFailure) {
	items := make([]sourceItem, 0, 8)
	for rows.Next() {
		row, err := scanSourceItemRow(rows)
		if err != nil {
			return nil, sourceItemsFailure{kind: sourceItemsFailureScan, err: err}
		}
		item := buildSourceItem(row)
		item.Meta = metaFromColumn(logger, row.id, row.metaJSON)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, sourceItemsFailure{kind: sourceItemsFailureIterate, err: err}
	}
	return items, sourceItemsFailure{kind: sourceItemsFailureNone}
}

func scanSourceItemRow(rows *sql.Rows) (sourceItemRow, error) {
	var row sourceItemRow
	err := rows.Scan(
		&row.id,
		&row.format,
		&row.location,
		&row.enabled,
		&row.parseErrors,
		&row.lastSeenAt,
		&row.createdAt,
		&row.cursor,
		&row.lastSeq,
		&row.lastTsUS,
		&row.updatedAt,
		&row.metaJSON,
	)
	return row, err
}

func buildSourceItem(row sourceItemRow) sourceItem {
	item := sourceItem{
		ID:          row.id,
		Format:      row.format,
		Location:    row.location,
		Enabled:     row.enabled != 0,
		ParseErrors: row.parseErrors,
		CreatedAt:   row.createdAt,
		Cursor:      row.cursor,
		LastSeq:     row.lastSeq,
	}
	setSourceItemNullables(&item, row)
	// Meta is set by the caller (readSourceItemRows) via metaFromColumn so
	// the json.Valid defence + WARN live in one shared helper (SOW-0024).
	return item
}

func setSourceItemNullables(item *sourceItem, row sourceItemRow) {
	if row.lastSeenAt.Valid {
		v := row.lastSeenAt.Int64
		item.LastSeenAt = &v
	}
	if row.lastTsUS.Valid {
		v := row.lastTsUS.Int64
		item.LastTsUS = &v
	}
	if row.updatedAt.Valid {
		v := row.updatedAt.Int64
		item.UpdatedAt = &v
	}
}

func (p *Presenter) writeSourcesFailure(ctx context.Context, w http.ResponseWriter, r *http.Request, failure sourceItemsFailure) {
	if failure.err == nil {
		return
	}
	resp := failure.response()
	p.logger.LogAttrs(ctx, resp.level, resp.logMessage,
		slog.Any("err", failure.err),
		slog.String("request_id", requestIDFromContext(ctx)))
	writeJSONError(w, r, p.logger, resp.status, resp.code, resp.clientMessage, nil)
}

func (f sourceItemsFailure) response() sourceItemsFailureResponse {
	switch f.kind {
	case sourceItemsFailureQuery:
		return sourceItemsFailureResponse{
			level:         slog.LevelError,
			logMessage:    "presenter: sources query failed",
			status:        http.StatusServiceUnavailable,
			code:          CodeDBUnavailable,
			clientMessage: "failed to list sources",
		}
	case sourceItemsFailureScan:
		return sourceItemsFailureResponse{
			level:         slog.LevelError,
			logMessage:    "presenter: sources row scan failed",
			status:        http.StatusInternalServerError,
			code:          CodeInternalError,
			clientMessage: "failed to read sources",
		}
	default:
		return sourceItemsFailureResponse{
			level:         slog.LevelError,
			logMessage:    "presenter: sources row iteration failed",
			status:        http.StatusInternalServerError,
			code:          CodeInternalError,
			clientMessage: "failed to iterate sources",
		}
	}
}
