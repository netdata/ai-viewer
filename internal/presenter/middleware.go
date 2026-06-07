package presenter

import (
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// maxRequestBodyBytes caps any inbound POST/PUT body so a malicious or
// runaway client cannot exhaust server memory. /api/health and
// /api/sources are read-only today; the cap is defence in depth for
// future POST endpoints (subscriptions land in Chunk 13). Per
// security.md §"Bounded parsing" the project caps inbound parsing at
// 16 MB for adapters; 1 MB suffices for JSON request bodies.
const maxRequestBodyBytes = 1 << 20

// loggingMiddleware wraps next with a per-request structured log line
// emitted via defer so the access log fires even when a downstream
// handler panics. The log carries method, path, status, duration_us,
// bytes_out, client_ip, and a freshly-minted UUID-v4 request_id that
// also surfaces on the X-Request-ID response header. The field list is
// the contract documented in observability.md §"Structured Logging" +
// §"Trace IDs"; the pre-defer implementation skipped the access log
// on the panic path AND error/panic logs were missing request_id —
// both gaps are closed
// here.
//
// loggingMiddleware is registered as the OUTERMOST middleware so the
// `defer` unwinds AFTER recoverMiddleware has absorbed any panic and
// written its 500. By the time the deferred emit runs, lw.status
// reflects the final response code (500 on panic, the handler's status
// otherwise) and the access log carries the same request_id the panic
// log already emitted.
func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rid := newRequestID()
			w.Header().Set("X-Request-ID", rid)
			ctx := withRequestID(r.Context(), rid)
			lw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if logger == nil {
					return
				}
				logger.LogAttrs(ctx, slog.LevelInfo, "http request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", lw.status),
					slog.Int64("duration_us", time.Since(start).Microseconds()),
					slog.Int64("bytes_out", lw.bytes),
					slog.String("client_ip", clientIPFromRequest(r)),
					slog.String("request_id", rid),
				)
			}()
			next.ServeHTTP(lw, r.WithContext(ctx))
		})
	}
}

// clientIPFromRequest extracts the remote IP from r.RemoteAddr stripped
// of the port. Returns the raw RemoteAddr when parsing fails (IPv6
// without brackets, malformed header, etc.) so the log line still
// carries best-effort context. The presenter binds 127.0.0.1 in v1 so
// the value is almost always "127.0.0.1"; the field exists so future
// non-localhost deployments and reverse-proxy hops have a stable place
// to land. We deliberately do NOT honour `X-Forwarded-For` /
// `X-Real-IP` headers in v1 because the server does not yet sit behind
// a trusted proxy; respecting client-supplied headers without an
// allow-list is a log-spoofing primitive.
func clientIPFromRequest(r *http.Request) string {
	if r == nil || r.RemoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loggingResponseWriter shadows the underlying ResponseWriter so the
// logging middleware can record the status code and the number of bytes
// written for the access log line.
type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (lw *loggingResponseWriter) WriteHeader(code int) {
	if !lw.wroteHeader {
		lw.status = code
		lw.wroteHeader = true
	}
	lw.ResponseWriter.WriteHeader(code)
}

func (lw *loggingResponseWriter) Write(b []byte) (int, error) {
	if !lw.wroteHeader {
		lw.wroteHeader = true
	}
	n, err := lw.ResponseWriter.Write(b)
	lw.bytes += int64(n)
	return n, err
}

// Unwrap exposes the wrapped ResponseWriter so http.ResponseController can
// walk to the real underlying writer for SetWriteDeadline/SetReadDeadline
// and FlushError. Without it the controller stops at this wrapper and those
// operations return http.ErrNotSupported even when the underlying writer
// supports them — making the SSE handler's write-deadline clear ineffective
// and hiding flush errors (presenter.md §Middlewares).
func (lw *loggingResponseWriter) Unwrap() http.ResponseWriter { return lw.ResponseWriter }

// FlushError flushes buffered data to the client and returns any error.
// It prefers the underlying writer's FlushError (the interface
// http.ResponseController itself uses) so a real flush failure propagates
// and the SSE stream loop can tear down; it falls back to http.Flusher
// (Flush + nil) and finally http.ErrNotSupported when neither is available.
func (lw *loggingResponseWriter) FlushError() error {
	if fe, ok := lw.ResponseWriter.(interface{ FlushError() error }); ok {
		return fe.FlushError()
	}
	if f, ok := lw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
		return nil
	}
	return http.ErrNotSupported
}

// Flush implements http.Flusher so existing Flusher callers are unaffected.
// It delegates to FlushError and discards the error (Flush has no return);
// callers that need the error use http.ResponseController, which now reaches
// FlushError via Unwrap. SSE handlers rely on this passthrough.
func (lw *loggingResponseWriter) Flush() { _ = lw.FlushError() }

// wrote reports whether the response has already started (a status code
// or body byte was written). recoverMiddleware reads this via an
// interface assertion so it does not append a 500 envelope over a
// partially-sent body. See presenter.md §Middlewares §"Recover panic".
func (lw *loggingResponseWriter) wrote() bool { return lw.wroteHeader }

// recoverMiddleware catches panics in downstream handlers, logs the
// stack trace at error level, and returns a structured 500 to the
// client. Per AGENTS.md §"No silent failures", a recovered panic is
// always logged with full context — including the per-request
// `request_id` extracted from r.Context() so the panic log and the
// (deferred) access log can be grepped together.
func recoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				stack := debug.Stack()
				if logger != nil {
					logger.LogAttrs(r.Context(), slog.LevelError, "presenter: handler panic",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path),
						slog.String("method", r.Method),
						slog.String("request_id", requestIDFromContext(r.Context())),
						slog.String("stack", string(stack)),
					)
				}
				// Only send the structured 500 when the response has NOT
				// started. If the handler already wrote status/body before
				// panicking (e.g. a future streaming handler), appending a
				// JSON error would corrupt the partially-sent body and emit
				// a superfluous WriteHeader — the panic is already logged
				// above, so the safe action is to return. The wrapped
				// *loggingResponseWriter (logging is the outermost
				// middleware) exposes wrote() for this decision.
				if rw, ok := w.(interface{ wrote() bool }); ok && rw.wrote() {
					return
				}
				writeJSONError(w, r, logger, http.StatusInternalServerError,
					CodeInternalError, "internal server error", nil)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// bodyLimitMiddleware caps r.Body to maxRequestBodyBytes for unsafe
// methods (POST/PUT/PATCH). Other methods leave the body alone — there
// is no body to cap.
func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}
