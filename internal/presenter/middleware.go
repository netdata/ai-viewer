package presenter

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// maxRequestBodyBytes caps any inbound POST/PUT body so a malicious or
// runaway client cannot exhaust server memory. /api/health and
// /api/sources are read-only today; the cap is defence in depth for
// future POST endpoints (subscriptions land in Chunk 13). Per
// security.md §"Bounded parsing" the project caps inbound parsing at
// 16 MB for adapters; 1 MB suffices for JSON request bodies.
const maxRequestBodyBytes = 1 << 20

// gzipMinBytes is the threshold above which the gzip middleware will
// compress a response. Below the threshold the CPU cost outweighs the
// network saving on localhost. Picked to match the value documented in
// presenter.md §Middlewares.
const gzipMinBytes = 1024

// loggingMiddleware wraps next with a per-request structured log line
// emitted via defer so the access log fires even when a downstream
// handler panics. The log carries method, path, status, duration_us,
// bytes_out, client_ip, and a freshly-minted UUID-v4 request_id that
// also surfaces on the X-Request-ID response header. The field list is
// the contract documented in observability.md §"Structured Logging" +
// §"Trace IDs"; codex iter-5 P2 flagged that the pre-defer
// implementation skipped the access log on the panic path AND that
// error/panic logs were missing request_id — both gaps are closed
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

// Flush implements http.Flusher when the inner writer supports it. SSE
// handlers (landing in Chunk 13) rely on this passthrough.
func (lw *loggingResponseWriter) Flush() {
	if f, ok := lw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

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

// gzipMiddleware compresses eligible JSON responses with gzip. Eligible
// = client advertises gzip in Accept-Encoding AND the response body is
// >= gzipMinBytes AND the path is NOT the SSE event stream (compressing
// SSE breaks the framing observers expect). The handler reads the body
// into a buffer so the length can be inspected before deciding to
// compress — this is acceptable because Phase 1's largest JSON
// responses are bounded by the limit + cursor model in rest-api.md.
//
// The middleware does NOT compress an explicit
// Content-Encoding-already-set response, so SSE (which sets its own
// Content-Type and never goes through this gate) is doubly safe.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/events" || !clientAcceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		bw := &bufferingResponseWriter{ResponseWriter: w, header: http.Header{}}
		next.ServeHTTP(bw, r)
		// If the downstream handler hijacked the connection or already
		// wrote a compressed/non-gzippable encoding, fall back to raw
		// passthrough.
		if bw.contentEncodingSet() {
			bw.flushPassthrough()
			return
		}
		if bw.buf.Len() < gzipMinBytes {
			bw.flushPassthrough()
			return
		}
		// Compress.
		w.Header().Del("Content-Length")
		for k, vs := range bw.header {
			if strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		status := bw.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		gz := gzip.NewWriter(w)
		if _, err := io.Copy(gz, &bw.buf); err != nil {
			return
		}
		_ = gz.Close()
	})
}

// clientAcceptsGzip reports whether the request advertises gzip
// support via Accept-Encoding. Handles common shapes: bare token,
// quality-weighted token, multiple tokens. A weight of q=0 (or q=0.0,
// q=0.00, ...) means the client refuses gzip per RFC 9110 §12.5.3.
func clientAcceptsGzip(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(tok)
			weight := 1.0
			if i := strings.IndexByte(tok, ';'); i >= 0 {
				params := tok[i+1:]
				tok = tok[:i]
				weight = parseAcceptWeight(params)
			}
			if strings.EqualFold(tok, "gzip") && weight > 0 {
				return true
			}
		}
	}
	return false
}

// parseAcceptWeight extracts the q-value from a parameter string like
// "q=0.5" or "level=fast;q=0". Returns 1.0 when no q parameter is
// present (the HTTP default). Malformed numbers fall back to 1.0 so a
// best-effort header still wins; the spec is permissive here.
func parseAcceptWeight(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		p = strings.TrimSpace(strings.ToLower(p))
		if !strings.HasPrefix(p, "q=") {
			continue
		}
		raw := strings.TrimPrefix(p, "q=")
		// Manual parse: HTTP q values are between 0 and 1 with up to 3
		// decimals. Reject anything else and fall back to 1.0.
		var (
			seenDigit, seenDot bool
			intPart, fracPart  int
			fracDigits         int
		)
		for _, c := range raw {
			switch {
			case c >= '0' && c <= '9':
				if !seenDot {
					intPart = intPart*10 + int(c-'0')
				} else {
					fracPart = fracPart*10 + int(c-'0')
					fracDigits++
				}
				seenDigit = true
			case c == '.' && !seenDot:
				seenDot = true
			default:
				return 1.0
			}
		}
		if !seenDigit {
			return 1.0
		}
		if intPart > 0 {
			return float64(intPart)
		}
		if fracDigits == 0 {
			return 0
		}
		divisor := 1
		for i := 0; i < fracDigits; i++ {
			divisor *= 10
		}
		return float64(fracPart) / float64(divisor)
	}
	return 1.0
}

// bufferingResponseWriter holds the downstream handler's response in
// memory so the gzip middleware can inspect the length before
// committing to a compression path. Reasonable for Phase 1's bounded
// JSON; later chunks (payload streaming) will route around this writer
// via dedicated handlers.
type bufferingResponseWriter struct {
	http.ResponseWriter
	header  http.Header
	buf     bytes.Buffer
	status  int
	flushed bool
}

func (b *bufferingResponseWriter) Header() http.Header { return b.header }

func (b *bufferingResponseWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferingResponseWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.buf.Write(p)
}

func (b *bufferingResponseWriter) contentEncodingSet() bool {
	return b.header.Get("Content-Encoding") != ""
}

func (b *bufferingResponseWriter) flushPassthrough() {
	if b.flushed {
		return
	}
	b.flushed = true
	for k, vs := range b.header {
		for _, v := range vs {
			b.ResponseWriter.Header().Add(k, v)
		}
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	b.ResponseWriter.WriteHeader(status)
	if _, err := io.Copy(b.ResponseWriter, &b.buf); err != nil && !errors.Is(err, http.ErrBodyNotAllowed) {
		// Best-effort; the connection is likely already gone.
		_ = err
	}
}
