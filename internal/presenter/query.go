package presenter

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// isNoRows reports whether err is the database/sql "no rows" sentinel.
// Used by existence probes that map an absent row to a 404 rather than a
// 503.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// queryTimeout bounds every read query the Chunk 12 endpoints run. Per
// presenter.md §"SQLite Access" a runaway query is killed at 30 s and
// the user gets a 504; the context cancellation propagates to
// modernc.org/sqlite which aborts the statement.
const queryTimeout = 30 * time.Second

// withQueryTimeout derives a child context bounded by queryTimeout from
// the request context. The caller MUST defer the returned cancel. Kept
// in one place so every handler uses the same deadline.
func withQueryTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, queryTimeout)
}

// writeDBError maps a query error to the right HTTP envelope and logs it
// with the request_id (observability.md §"Trace IDs"). A
// deadline-exceeded error becomes 504 TIMEOUT; anything else becomes 503
// DB_UNAVAILABLE, matching the existing /api/sources failure path. The
// op string names the failing query for the log line so a failure is
// greppable back to its source.
func (p *Presenter) writeDBError(w http.ResponseWriter, r *http.Request, ctx context.Context, op string, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		p.logger.LogAttrs(ctx, slog.LevelWarn, "presenter: query timed out",
			slog.String("op", op),
			slog.Any("err", err),
			slog.String("request_id", requestIDFromContext(ctx)))
		writeJSONError(w, r, p.logger, http.StatusGatewayTimeout,
			CodeTimeout, "query timed out", map[string]any{"op": op})
		return
	}
	p.logger.LogAttrs(ctx, slog.LevelError, "presenter: query failed",
		slog.String("op", op),
		slog.Any("err", err),
		slog.String("request_id", requestIDFromContext(ctx)))
	writeJSONError(w, r, p.logger, http.StatusServiceUnavailable,
		CodeDBUnavailable, "database query failed", map[string]any{"op": op})
}

// writeBadFilter maps a filter/cursor validation error to a 400
// BAD_REQUEST envelope. The reason carried by the joined error becomes
// the human-facing message.
func (p *Presenter) writeBadFilter(w http.ResponseWriter, r *http.Request, err error) {
	writeJSONError(w, r, p.logger, http.StatusBadRequest,
		CodeBadRequest, badFilterReason(err), nil)
}

// badFilterReason extracts the human-readable reason from a wrapped
// errBadFilter. errors.Join renders the errBadFilter sentinel and the
// specific reason on separate lines; the last non-empty line is the
// reason. Falls back to a generic message when no reason is attached.
func badFilterReason(err error) string {
	if err == nil {
		return "invalid request"
	}
	lines := splitNonEmptyLines(err.Error())
	if len(lines) == 0 {
		return "invalid request"
	}
	return lines[len(lines)-1]
}

// splitNonEmptyLines splits s on newlines and drops empties. Kept tiny
// to avoid pulling strings into this file for one call site elsewhere.
func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if line := s[start:i]; line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if line := s[start:]; line != "" {
		out = append(out, line)
	}
	return out
}
