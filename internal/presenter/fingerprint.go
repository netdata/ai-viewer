package presenter

import (
	"slices"
	"strconv"
	"strings"
)

// Cursor fingerprinting. A keyset (seek) cursor's (ts, id) watermark is only
// meaningful against the exact result set it was minted on, so every cursor
// carries a fingerprint of the FULL result-defining query (filters, group,
// time window, search, ordering — for logs the session id + severity set).
// On replay the handler recomputes the fingerprint from the live request and
// rejects a mismatch with BAD_REQUEST, so a cursor cannot be replayed against
// a different query and silently skip or duplicate rows. The SAME helper is
// used to mint the next_cursor and to validate an incoming cursor, so the two
// can never drift. See presenter.md §"Filters, pagination, and cursors".

// The cursor carries the canonical length-prefixed STRING itself, not a
// digest of it: each value is written as its byte length, a ':', then the
// value (writeLP), and the validator compares the two strings byte-for-byte.
// Length-prefixing makes every token self-delimiting, so no value content —
// including any control or separator byte — can forge a field/element
// boundary. Because the cursor stores and compares the full string rather
// than a fixed-width digest, two distinct filter sets can never collide: the
// property is exact by construction, not a probabilistic bound. (Earlier
// iterations hashed the string with FNV-64a; a 64-bit digest is finite
// and can collide, so the cursor now carries the string directly.
// The cursor is an opaque localhost token echoing back values the client
// already sent, so its size is immaterial.) The parser still rejects control
// characters in filter values as defense in depth (filters.go).

// fingerprint returns the canonical length-prefixed string of the entire
// result-defining query so a cursor can be bound byte-for-byte to the exact
// filter set it was minted on. Array dimensions are sorted before encoding so
// the fingerprint is insensitive to filter order (?models=a,b == ?models=b,a).
// limit (page size may legitimately change between pages) and cursor itself are
// excluded. The supplied `to` (toRaw) is encoded rather than the now-defaulted
// `to`, so a cursor replayed with an omitted `to` (the common case) stays valid
// instead of mismatching a fresh `now` on every request.
func (f sessionFilter) fingerprint() string {
	var b strings.Builder
	writeLP(&b, "g")
	writeLP(&b, f.group)
	writeLP(&b, "from")
	writeLP(&b, microsPtrKey(f.from))
	writeLP(&b, "to")
	writeLP(&b, microsPtrKey(f.toRaw)) // supplied `to`, not the now-default.
	writeLP(&b, "sort")
	writeLP(&b, f.sort)
	writeLP(&b, "order")
	writeLP(&b, f.order)
	writeLP(&b, "q")
	writeLP(&b, f.q)
	writeSortedDim(&b, "agents", f.agents)
	writeSortedDim(&b, "models", f.models)
	writeSortedDim(&b, "tools", f.tools)
	writeSortedDim(&b, "status", f.status)
	writeSortedDim(&b, "sources", f.source)
	return b.String()
}

// writeLP appends one length-prefixed token (`<len>:<value>`) to b. The
// declared byte length makes the token self-delimiting, so the surrounding
// stream stays unambiguous no matter what bytes `s` contains — a value can
// never forge a field/element boundary. strconv.Itoa(len) is the byte length,
// matching len(s) on the raw string.
func writeLP(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}

// writeSortedDim appends a length-prefixed rendering of one array filter
// dimension to b: the dimension name, the element count, then each element
// (sorted) as its own length-prefixed token. The element count is encoded so
// `[]` and `[""]` cannot collapse to the same byte stream. The slice is
// copied before sorting so the caller's order (which drives deterministic SQL
// IN(...) lists) is untouched.
func writeSortedDim(b *strings.Builder, name string, vals []string) {
	writeLP(b, name)
	b.WriteString(strconv.Itoa(len(vals)))
	b.WriteByte(':')
	sorted := slices.Clone(vals)
	slices.Sort(sorted)
	for _, s := range sorted {
		writeLP(b, s)
	}
}

// microsPtrKey renders an optional microsecond bound for the fingerprint: a
// stable empty token when the bound is absent so present-vs-absent is
// distinguishable from a zero value.
func microsPtrKey(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}
