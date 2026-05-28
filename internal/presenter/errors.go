package presenter

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Error codes returned in the JSON envelope per observability.md
// §"Self-Documenting Errors". Stable strings — the UI maps each code to
// a human-friendly message. Extend by adding constants here; do not
// rename existing ones.
const (
	// CodeBadRequest is returned when the caller sent a malformed
	// request (bad query param, illegal filter combination, etc.).
	CodeBadRequest = "BAD_REQUEST"
	// CodeNotFound is returned when the requested resource does not
	// exist.
	CodeNotFound = "NOT_FOUND"
	// CodeInternalError is the catch-all for unexpected server-side
	// failures. Always accompanied by a log line with full context.
	CodeInternalError = "INTERNAL_ERROR"
	// CodeDBUnavailable is returned when SQLite refuses a query (file
	// missing, locked beyond busy_timeout, etc.).
	CodeDBUnavailable = "DB_UNAVAILABLE"
	// CodeSchemaMismatch is returned when the database schema_meta
	// version disagrees with the binary's expected version. Surfaces at
	// startup; never thrown from a handler at request time.
	CodeSchemaMismatch = "SCHEMA_MISMATCH"
	// CodeMethodNotAllowed is returned when the route exists but the
	// HTTP method is not supported (e.g. POST to /api/health).
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	// CodeTimeout is returned (HTTP 504) when a read query exceeds the
	// 30 s query deadline. Distinct from DB_UNAVAILABLE so the UI can
	// tell "the database is gone" from "this query was too slow".
	CodeTimeout = "TIMEOUT"
)

// errorEnvelope is the JSON shape every error response carries. The
// outer object exists so future fields (request_id, retry_after) can be
// added without breaking clients.
type errorEnvelope struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// writeJSONError serialises a structured error response and logs the
// failure. The caller passes the HTTP status (4xx/5xx) and the
// machine-readable code; the human message is plain English suitable
// for a UI toast. HEAD requests receive the status code and the
// Content-Type header but NO body, mirroring writeJSON's HEAD branch
// and presenter.md §"Routing" — RFC 9110 §9.3.2 makes the empty-body
// contract mandatory for every method that supports GET.
func writeJSONError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, code, msg string, details map[string]any) {
	if logger != nil {
		// request_id mirrors the X-Request-ID header so a 4xx/5xx warning
		// can be grepped together with the deferred access log line for
		// the same request. Per observability.md §"Trace IDs" every
		// request-scoped log line MUST carry the field; codex iter-5 P2
		// flagged this surface as the last gap.
		attrs := []any{
			"status", status,
			"code", code,
			"path", r.URL.Path,
			"method", r.Method,
			"request_id", requestIDFromContext(r.Context()),
		}
		for k, v := range details {
			attrs = append(attrs, "detail."+k, v)
		}
		logger.LogAttrs(r.Context(), slog.LevelWarn, msg, toAttrs(attrs)...)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	body := errorEnvelope{Error: errorPayload{Code: code, Message: msg, Details: details}}
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.ErrorContext(r.Context(), "presenter: failed to encode error envelope",
			"err", err, "code", code, "path", r.URL.Path,
			"request_id", requestIDFromContext(r.Context()))
	}
}

// toAttrs converts an alternating key/value slice into slog.Attr values.
// Keeps writeJSONError concise without forcing each caller to assemble
// the attrs themselves.
func toAttrs(kv []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		out = append(out, slog.Any(key, kv[i+1]))
	}
	return out
}

// writeJSON encodes v as JSON, sets Content-Type, and writes the status
// code. The helper exists so every handler emits identical headers and
// the gzip middleware sees a single Content-Type to gate on. For HEAD
// requests the handler still sets headers and the status code but
// skips writing the body, per RFC 9110 §9.3.2.
func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil && logger != nil {
		logger.ErrorContext(r.Context(), "presenter: failed to encode JSON response",
			"err", err, "path", r.URL.Path,
			"request_id", requestIDFromContext(r.Context()))
	}
}
