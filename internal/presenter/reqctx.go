package presenter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Request-correlation primitives extracted from middleware.go so the
// file stays under the 400-line ceiling. The helpers below own the
// per-request UUID-v4 minting and the typed context.Value key so every
// log line emitted during a request can be correlated by `request_id`.
// Per observability.md §"Trace IDs" every HTTP request log line MUST
// carry the same UUID-v4; error and panic logs were missing the field
// and this file is part of the fix.

// ctxKey is a private type so context keys cannot collide with other
// packages.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
)

// requestIDFromContext returns the request ID attached to ctx, or "" if
// none. The empty-string fallback keeps log lines safe to emit before
// loggingMiddleware has run (e.g. from a misconfigured test harness)
// without panicking on a nil context.
func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// withRequestID returns a child context carrying rid under the typed
// key. Centralised so future middlewares attach the value the same way.
func withRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, rid)
}

// newRequestID returns a freshly-minted UUID-v4 string per
// observability.md §"Trace IDs". Pure stdlib — RFC 4122 §4.4 only
// requires setting six bits over 16 random bytes, so pulling in
// github.com/google/uuid (currently `indirect` via sqlite) is
// unnecessary and would also force a `go.mod` direct-dep change that
// is out of scope for the iter-5/iter-6 spec/code-parity fixes.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a time-based id so the log line still carries
		// something. Read() failing on Linux is essentially impossible
		// but the contract is "always emit a request_id" — empty
		// strings break log correlation grep recipes.
		return "rid-" + time.Now().UTC().Format("150405.000000")
	}
	// RFC 4122 §4.4: version (4) in the top nibble of byte 6, variant
	// (10) in the top two bits of byte 8. Everything else stays random.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	// Canonical 8-4-4-4-12 dashed form. Manual layout keeps the hot
	// path allocation-light (one 36-byte buffer, no fmt.Sprintf).
	const dashed = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
	var buf [len(dashed)]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
