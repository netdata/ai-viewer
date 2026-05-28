package presenter

import (
	"context"
	"database/sql"
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
type sourceItem struct {
	ID          string `json:"id"`
	Format      string `json:"format"`
	Location    string `json:"location"`
	Enabled     bool   `json:"enabled"`
	ParseErrors int64  `json:"parse_errors"`
	LastSeenAt  *int64 `json:"last_seen_at"`
	CreatedAt   int64  `json:"created_at"`
	Cursor      string `json:"cursor"`
	LastSeq     int64  `json:"last_seq"`
	LastTsUS    *int64 `json:"last_ts_us"`
	UpdatedAt   *int64 `json:"updated_at"`
}

// handleSources answers GET /api/sources. Returns every configured
// source with the matching source_progress cursor + last_seq
// observability counter (max SourceSeq seen; NOT a dedup gate). The
// Sources admin panel (lands in Chunk 15) uses this endpoint to render
// its per-source diagnostics.
func (p *Presenter) handleSources(w http.ResponseWriter, r *http.Request) {
	// HEAD parity: same headers as GET, empty body. writeJSON skips
	// the body when r.Method == HEAD.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := p.db.QueryContext(ctx, `
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
    sp.updated_at
FROM sources s
LEFT JOIN source_progress sp ON sp.source_id = s.id
ORDER BY s.created_at, s.id
`)
	if err != nil {
		p.logger.LogAttrs(ctx, slog.LevelError, "presenter: sources query failed",
			slog.Any("err", err),
			slog.String("request_id", requestIDFromContext(ctx)))
		writeJSONError(w, r, p.logger, http.StatusServiceUnavailable,
			CodeDBUnavailable, "failed to list sources", nil)
		return
	}
	defer func() { _ = rows.Close() }()

	items := make([]sourceItem, 0, 8)
	for rows.Next() {
		var (
			id, format, location, cursor string
			enabled                      int64
			parseErrors                  int64
			lastSeenAt                   sql.NullInt64
			createdAt                    int64
			lastSeq                      int64
			lastTsUS, updatedAt          sql.NullInt64
		)
		if err := rows.Scan(&id, &format, &location, &enabled, &parseErrors,
			&lastSeenAt, &createdAt, &cursor, &lastSeq, &lastTsUS, &updatedAt); err != nil {
			p.logger.LogAttrs(ctx, slog.LevelError, "presenter: sources row scan failed",
				slog.Any("err", err),
				slog.String("request_id", requestIDFromContext(ctx)))
			writeJSONError(w, r, p.logger, http.StatusInternalServerError,
				CodeInternalError, "failed to read sources", nil)
			return
		}
		item := sourceItem{
			ID:          id,
			Format:      format,
			Location:    location,
			Enabled:     enabled != 0,
			ParseErrors: parseErrors,
			CreatedAt:   createdAt,
			Cursor:      cursor,
			LastSeq:     lastSeq,
		}
		if lastSeenAt.Valid {
			v := lastSeenAt.Int64
			item.LastSeenAt = &v
		}
		if lastTsUS.Valid {
			v := lastTsUS.Int64
			item.LastTsUS = &v
		}
		if updatedAt.Valid {
			v := updatedAt.Int64
			item.UpdatedAt = &v
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		p.logger.LogAttrs(ctx, slog.LevelError, "presenter: sources row iteration failed",
			slog.Any("err", err),
			slog.String("request_id", requestIDFromContext(ctx)))
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "failed to iterate sources", nil)
		return
	}

	writeJSON(w, r, p.logger, http.StatusOK, sourcesResponse{Items: items})
}
