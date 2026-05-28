package presenter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// pageCursor is the keyset (seek) pagination cursor shared by the
// list-shaped endpoints. It carries the sort key of the LAST row in the
// page plus that row's id as a tiebreaker, AND the sort/order the cursor
// was minted under. The next page selects rows strictly after this tuple
// in the active sort direction, so deep pages stay O(log n) and remain
// stable when new rows land between fetches (offset pagination would skip
// or repeat rows under concurrent writes).
//
// TS is the primary sort key: sessions.start_ts for /api/sessions,
// log_entries.ts for /api/sessions/:id/logs. ID is the row id used to
// break ties when two rows share the same timestamp — every keyset query
// orders by (TS, id) so the tuple is total.
//
// Sort/Order pin the ordering the cursor is valid for. Replaying a cursor
// against a different order would flip the row-value comparison direction
// (`> (?, ?)` vs `< (?, ?)`) and silently duplicate or skip rows; the
// handler rejects the mismatch with BAD_REQUEST rather than serving a
// corrupt page. The logs endpoint has a single fixed ordering, so its
// cursors are minted and validated against that constant.
//
// FP is the canonical length-prefixed STRING of the ENTIRE result-defining
// query the cursor was minted on (all filters, group, time window, search;
// sorted array dimensions; for logs the session id + severity set) — the
// string itself, not a digest of it. The keyset (TS, ID) watermark is only
// meaningful against the exact result set it was minted against, so a cursor
// minted on one query and replayed against a different one (a changed
// filter/group/severity) would silently skip or duplicate rows. The handler
// recomputes the canonical string from the live request and compares it to FP
// byte-for-byte, rejecting a mismatch with BAD_REQUEST. Because there is no
// fixed-width digest, two distinct filter sets can never collide — the match
// is exact by construction. FP is validated semantically by the caller (the
// fingerprint comparison) rather than as a structural decode requirement: a
// tampered or absent FP simply fails the comparison against the live (always
// non-empty) query fingerprint. Sort/Order are explicit fields so the handler
// can reject a cursor whose ordering does not match the active query — a
// precise BAD_REQUEST guard (mirrored by the logs endpoint's fixed-ordering
// check). The keyset comparison direction itself comes from the live request's
// order, not from the cursor.
type pageCursor struct {
	TS    int64  `json:"ts"`
	ID    string `json:"id"`
	Sort  string `json:"sort"`
	Order string `json:"order"`
	FP    string `json:"fp"`
}

// isZero reports whether the cursor is the zero value, which the handler
// treats as "from the beginning" (no keyset narrowing). A real cursor
// always carries a non-empty ID because every row has a non-NULL id.
func (c pageCursor) isZero() bool {
	return c == pageCursor{}
}

// encode renders the cursor as a base64url-encoded JSON object. The
// value is opaque to clients — they only echo it back as ?cursor=.
// base64url (no padding) keeps the token URL-safe without escaping.
func (c pageCursor) encode() string {
	raw, err := json.Marshal(c)
	if err != nil {
		// pageCursor is four scalar fields; Marshal cannot fail. The
		// error branch exists only so a future field change surfaces
		// rather than silently emitting an empty cursor.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// errBadCursor is returned by decodeCursor for any malformed token so the
// handler can map it to a BAD_REQUEST envelope.
var errBadCursor = errors.New("presenter: malformed pagination cursor")

// decodeCursor parses a base64url-encoded JSON cursor produced by encode
// and validates it is structurally complete. A token is errBadCursor when
// it is not valid base64url, not a single valid JSON object, carries
// trailing bytes after the object, carries an unknown field (so an older
// binary cannot silently reinterpret a future cursor format), or is
// missing any required field — a real cursor carries a non-zero TS, a
// non-empty ID, a non-empty Sort, and a non-empty Order. The caller never
// decodes the empty string (an absent cursor means "from the beginning")
// and is responsible for checking Sort/Order against the live request.
func decodeCursor(s string) (pageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageCursor{}, errBadCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c pageCursor
	if err := dec.Decode(&c); err != nil {
		return pageCursor{}, errBadCursor
	}
	// Reject trailing bytes (a second token, garbage) after the object so
	// a truncated/padded cursor cannot be reinterpreted as valid.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return pageCursor{}, errBadCursor
	}
	if c.TS == 0 || c.ID == "" || c.Sort == "" || c.Order == "" {
		return pageCursor{}, errBadCursor
	}
	return c, nil
}
