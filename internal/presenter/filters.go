package presenter

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Shared query-parameter parsing for the list-shaped endpoints
// (/api/sessions, /api/stats) and the per-session log endpoint. The
// parser turns rest-api.md §Conventions params into a typed
// sessionFilter plus a parameterized SQL WHERE fragment. Every
// operator-supplied value is bound as a `?` placeholder — NEVER
// string-interpolated — so the surface has no SQL-injection vector.

const (
	// defaultLimit is the page size when ?limit is omitted (rest-api.md
	// §Conventions).
	defaultLimit = 100
	// maxLimit is the hard ceiling; ?limit beyond this is a 400 rather
	// than a silent clamp so a client bug surfaces instead of quietly
	// truncating an expected page size.
	maxLimit = 1000

	// groupRoot returns only root sessions (default); groupAll returns
	// every session including children.
	groupRoot = "root"
	groupAll  = "all"

	// sortStartTS is the only sort supported in v1.
	sortStartTS = "start_ts"
)

// errBadFilter is returned for any invalid filter combination so the
// handler can map it to a BAD_REQUEST envelope. The wrapped message
// carries the specific reason for the operator-facing details.
var errBadFilter = errors.New("presenter: invalid filter")

// sessionFilter is the parsed, validated representation of the shared
// query params. Slice filters are OR-within / AND-across: a session
// matches when its value is in agents AND in models AND ... ; an empty
// slice means "no constraint on this dimension".
//
// toRaw is the operator-supplied `to` BEFORE the now-default applied to
// `to`. It is used only by fingerprint() so a cursor replayed with an
// omitted `to` (the common case) stays valid even though `to` defaults to a
// fresh `now` on every request; fingerprinting the defaulted `to` would
// make every paginated request mint a cursor against a different value and
// reject the next page.
type sessionFilter struct {
	from   *int64 // start_ts >= from
	to     *int64 // start_ts <= to (defaults to now when omitted)
	toRaw  *int64 // supplied `to` before the now-default; fingerprint only
	agents []string
	models []string
	tools  []string
	status []string
	source []string
	q      string // substring match on agent_name

	group string
	sort  string
	order string // "asc" | "desc"
	limit int

	cursor    pageCursor
	hasCursor bool
}

// parseSessionFilter parses and validates the shared query params. now
// is the injected clock used to default the upper time bound. Returns
// errBadFilter (wrapped) for any invalid value so the caller emits a
// single BAD_REQUEST shape. The body delegates to focused helpers so each
// stays well within the function-length budget.
func parseSessionFilter(v url.Values, now time.Time) (sessionFilter, error) {
	f := sessionFilter{
		group: groupRoot,
		sort:  sortStartTS,
		order: "desc",
		limit: defaultLimit,
	}
	if err := parseTimeRange(v, now, &f); err != nil {
		return f, err
	}
	if err := parseArrayFilters(v, &f); err != nil {
		return f, err
	}
	qRaw := v.Get("q")
	if err := rejectControlChars("q", qRaw); err != nil {
		return f, err
	}
	f.q = strings.TrimSpace(qRaw)
	if err := parseScalarFilters(v, &f); err != nil {
		return f, err
	}
	if err := parseCursorParam(v, &f); err != nil {
		return f, err
	}
	return f, nil
}

// parseTimeRange validates from/to and defaults `to` to now when omitted
// (rest-api.md §Conventions), writing the bounds onto f.
func parseTimeRange(v url.Values, now time.Time, f *sessionFilter) error {
	from, err := parseOptionalMicros(v.Get("from"))
	if err != nil {
		return wrapBadFilter("from must be a UNIX microsecond integer")
	}
	to, err := parseOptionalMicros(v.Get("to"))
	if err != nil {
		return wrapBadFilter("to must be a UNIX microsecond integer")
	}
	f.toRaw = to // capture the supplied value before the now-default (fingerprint).
	if to == nil {
		nowUS := now.UnixMicro()
		to = &nowUS
	}
	if from != nil && *from > *to {
		return wrapBadFilter("'from' is after 'to'")
	}
	f.from, f.to = from, to
	return nil
}

// parseArrayFilters parses the five array dimensions onto f. A key that is
// present in the query but whose every element is empty (e.g. `?models=`
// or `?models=,`) is a BAD_REQUEST per presenter.md §Filters; an absent
// key is simply no constraint on that dimension.
func parseArrayFilters(v url.Values, f *sessionFilter) error {
	dims := []struct {
		key string
		dst *[]string
	}{
		{"agents", &f.agents},
		{"models", &f.models},
		{"tools", &f.tools},
		{"status", &f.status},
		{"sources", &f.source},
	}
	for _, d := range dims {
		vals, err := parseRequiredNonEmptyArray(v, d.key)
		if err != nil {
			return err
		}
		*d.dst = vals
	}
	return nil
}

// parseScalarFilters validates the group/sort/order/limit scalars onto f.
func parseScalarFilters(v url.Values, f *sessionFilter) error {
	if err := applyGroupScalar(v.Get("group"), f); err != nil {
		return err
	}
	if err := validateSortScalar(v.Get("sort")); err != nil {
		return err
	}
	if err := applyOrderScalar(v.Get("order"), f); err != nil {
		return err
	}
	return applyLimitScalar(v.Get("limit"), f)
}

func applyGroupScalar(value string, f *sessionFilter) error {
	if value == "" {
		return nil
	}
	if value != groupRoot && value != groupAll {
		return wrapBadFilter("group must be 'root' or 'all'")
	}
	f.group = value
	return nil
}

func validateSortScalar(value string) error {
	if value != "" && value != sortStartTS {
		return wrapBadFilter("sort must be 'start_ts' (only sort supported in v1)")
	}
	return nil
}

func applyOrderScalar(value string, f *sessionFilter) error {
	if value == "" {
		return nil
	}
	if value != "asc" && value != "desc" {
		return wrapBadFilter("order must be 'asc' or 'desc'")
	}
	f.order = value
	return nil
}

func applyLimitScalar(value string, f *sessionFilter) error {
	if value == "" {
		return nil
	}
	n, convErr := strconv.Atoi(value)
	if convErr != nil || n < 1 {
		return wrapBadFilter("limit must be a positive integer")
	}
	if n > maxLimit {
		return wrapBadFilter("limit exceeds maximum of 1000")
	}
	f.limit = n
	return nil
}

// parseCursorParam decodes the opaque cursor (when supplied) and binds it
// to the request's full result-defining query. An absent or empty cursor
// means "first page". Two guards run, in order: first an EXPLICIT
// sort/order check (cur.Sort/cur.Order must equal the live request's
// normalized f.sort/f.order), then the fingerprint comparison. The explicit
// check fires first so an ordering mismatch yields a precise
// ordering-mismatch message rather than the generic filter-mismatch one, and
// so a forged/tampered cursor carrying the CORRECT fingerprint but junk or
// flipped sort/order is still rejected (the fingerprint covers sort+order, so
// a normally minted cursor passes both — this is a defense-in-depth guard
// that mirrors the logs endpoint's fixed-ordering check, keeping the two
// uniform; see presenter.md §"Filters, pagination, and cursors"). The
// fingerprint then catches every other filter change (group/time/q/array
// filters) so a mid-pagination query change is a loud client error, not
// silent duplicate/skip corruption.
// parseTimeRange/parseArrayFilters/parseScalarFilters must run first so
// f.sort/f.order and the fingerprint reflect the live request.
func parseCursorParam(v url.Values, f *sessionFilter) error {
	c := v.Get("cursor")
	if c == "" {
		return nil
	}
	cur, decErr := decodeCursor(c)
	if decErr != nil {
		return wrapBadFilter("cursor is malformed")
	}
	if cur.Sort != f.sort || cur.Order != f.order {
		return wrapBadFilter("cursor does not match this query's ordering; restart pagination")
	}
	if cur.FP != f.fingerprint() {
		return wrapBadFilter("cursor does not match the current query filters; restart pagination")
	}
	f.cursor = cur
	f.hasCursor = true
	return nil
}

// forceAllSessions widens an already-parsed filter to span every session kind
// by setting group=all, so whereClause omits the s.kind='root' constraint. The
// rollup-backed stats endpoints (/api/stats/aggregate, /api/stats/top) and
// /api/search call this after parseSessionFilter: those surfaces aggregate /
// search over ALL sessions (root + sub-agent), and the materialized rollups
// fold every op regardless of kind, so forcing group=all keeps the live-fold /
// search path consistent with the all-session rollup fast path. The root-vs-all
// distinction is a session-LIST concern (/api/sessions) and does not apply here;
// parseSessionFilter's group=root default is intentionally left untouched.
func (f *sessionFilter) forceAllSessions() {
	f.group = groupAll
}

// wrapBadFilter joins errBadFilter with a human-readable reason so the
// handler can surface the message in the error envelope while callers
// still match on errBadFilter via errors.Is.
func wrapBadFilter(reason string) error {
	return errors.Join(errBadFilter, errors.New(reason))
}

// parseOptionalMicros parses an optional UNIX-microsecond integer query
// value. Empty string => nil (param absent). A non-integer is an error.
func parseOptionalMicros(s string) (*int64, error) {
	if s == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// parseRequiredNonEmptyArray parses the array param at key and rejects the
// "present but empty" case: when the key appears in the query yet every
// element is empty (`?models=` / `?models=,`), that is a BAD_REQUEST per
// presenter.md §Filters. An absent key yields nil with no error (no
// constraint); a key with at least one non-empty value yields that value
// set even when it also contains empty segments (`?models=a,` keeps `a`).
// Control characters are rejected on the RAW entries (before splitting on
// comma or trimming) so a leading/trailing control byte that is also
// whitespace cannot be erased and slip through; this is the single rule that
// covers every array dimension AND logs severity (both route through here).
func parseRequiredNonEmptyArray(v url.Values, key string) ([]string, error) {
	if !v.Has(key) {
		return nil, nil
	}
	for _, raw := range v[key] {
		if err := rejectControlChars(key, raw); err != nil {
			return nil, err
		}
	}
	vals := parseArrayParam(v[key])
	if len(vals) == 0 {
		return nil, wrapBadFilter("filter " + strconv.Quote(key) + " is present but empty")
	}
	return vals, nil
}

// rejectControlChars returns a BAD_REQUEST error when v contains any ASCII
// control character (byte < 0x20). It runs on the RAW query value before any
// TrimSpace, so a leading/trailing control byte that is also whitespace
// (\t, \n, \r) is caught rather than silently trimmed away. Legitimate
// agent/model/tool/status/source names and search text never carry control
// bytes, so rejecting them keeps junk out of the SQL and the cursor with a
// loud 400 rather than a silent match (defense in depth). key names the filter
// for the operator-facing message.
func rejectControlChars(key, v string) error {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 {
			return wrapBadFilter("filter " + strconv.Quote(key) + " value contains control characters")
		}
	}
	return nil
}

// parseArrayParam flattens the repeated-and/or-comma-separated array
// syntax (rest-api.md §Conventions: `?a=x&a=y` or `?a=x,y`) into a
// deduplicated slice with empty elements dropped. Order is preserved by
// first appearance so the SQL IN(...) list is deterministic for tests.
func parseArrayParam(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, dup := seen[part]; dup {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
