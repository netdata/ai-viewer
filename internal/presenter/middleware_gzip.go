package presenter

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const gzipMinBytes = 1024

func gzipMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return gzipHandler{logger: logger, next: next}
	}
}

type gzipHandler struct {
	logger *slog.Logger
	next   http.Handler
}

func (h gzipHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if shouldBypassGzip(r) {
		h.next.ServeHTTP(w, r)
		return
	}

	bw := &bufferingResponseWriter{ResponseWriter: w, header: http.Header{}}
	h.next.ServeHTTP(bw, r)
	if !shouldGzipBufferedResponse(bw) {
		bw.flushPassthrough()
		return
	}
	h.writeGzipResponse(w, r, bw)
}

func shouldBypassGzip(r *http.Request) bool {
	return r.URL.Path == "/api/events" || !clientAcceptsGzip(r)
}

func shouldGzipBufferedResponse(bw *bufferingResponseWriter) bool {
	return !bw.contentEncodingSet() && bw.buf.Len() >= gzipMinBytes
}

func (h gzipHandler) writeGzipResponse(w http.ResponseWriter, r *http.Request, bw *bufferingResponseWriter) {
	copyBufferedHeadersForGzip(w.Header(), bw.header)
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	w.WriteHeader(statusOrOK(bw.status))

	gz := gzip.NewWriter(w)
	if _, err := io.Copy(gz, &bw.buf); err != nil {
		h.logGzipFailure(r, "gzip copy failed", err)
		return
	}
	if err := gz.Close(); err != nil {
		h.logGzipFailure(r, "gzip close failed", err)
	}
}

func copyBufferedHeadersForGzip(dst, src http.Header) {
	dst.Del("Content-Length")
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func statusOrOK(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}

func (h gzipHandler) logGzipFailure(r *http.Request, msg string, err error) {
	if h.logger == nil {
		return
	}
	h.logger.DebugContext(r.Context(), msg,
		"error", err, "path", r.URL.Path,
		"request_id", requestIDFromContext(r.Context()))
}

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

func parseAcceptWeight(params string) float64 {
	for _, p := range strings.Split(params, ";") {
		raw, ok := acceptWeightParam(p)
		if !ok {
			continue
		}
		weight, ok := parseHTTPQValue(raw)
		if !ok {
			return 1.0
		}
		return weight
	}
	return 1.0
}

func acceptWeightParam(param string) (string, bool) {
	return strings.CutPrefix(strings.TrimSpace(strings.ToLower(param)), "q=")
}

type acceptWeightParts struct {
	seenDigit bool
	seenDot   bool
	intPart   int
	fracPart  int
	fracDigit int
}

func parseHTTPQValue(raw string) (float64, bool) {
	parts, ok := scanHTTPQValue(raw)
	if !ok || !parts.seenDigit {
		return 0, false
	}
	return parts.value(), true
}

func scanHTTPQValue(raw string) (acceptWeightParts, bool) {
	var parts acceptWeightParts
	for _, c := range raw {
		switch {
		case isDecimalDigit(c):
			parts.addDigit(c)
		case c == '.' && !parts.seenDot:
			parts.seenDot = true
		default:
			return acceptWeightParts{}, false
		}
	}
	return parts, true
}

func isDecimalDigit(c rune) bool {
	return c >= '0' && c <= '9'
}

func (p *acceptWeightParts) addDigit(c rune) {
	digit := int(c - '0')
	if !p.seenDot {
		p.intPart = p.intPart*10 + digit
	} else {
		p.fracPart = p.fracPart*10 + digit
		p.fracDigit++
	}
	p.seenDigit = true
}

func (p acceptWeightParts) value() float64 {
	if p.intPart > 0 {
		return float64(p.intPart)
	}
	if p.fracDigit == 0 {
		return 0
	}
	return float64(p.fracPart) / float64(decimalDivisor(p.fracDigit))
}

func decimalDivisor(digits int) int {
	divisor := 1
	for i := 0; i < digits; i++ {
		divisor *= 10
	}
	return divisor
}

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
		_ = err
	}
}
