package presenter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/netdata/ai-viewer/internal/notify"
)

// subscriptionFilter is the parsed, validated SSE subscription filter
// (sse-protocol.md §Filter Shape). It is a SUBSET of the REST list filter
// — the shared dimensions (time_range, sources, agents, models, tools,
// status) plus two extra equality predicates (session_id,
// root_session_id) and NONE of the list-only knobs (sort/order/limit/
// cursor/q/group).
//
// The shared dimensions are stored in an embedded sessionFilter so the
// Chunk-12 whereClause is reused VERBATIM for matching — SSE matching is
// then identical to REST list matching by construction. The embedded
// filter is built with group=groupAll so whereClause never adds the
// kind='root' predicate: a subscription matches a changed session
// regardless of whether it is a root or a child. time_range bounds are
// applied only when supplied (no now-default: an open-ended subscription
// into the future is the typical real-time case).
type subscriptionFilter struct {
	base          sessionFilter
	sessionID     *string
	rootSessionID *string
}

// subscriptionRequest is the POST /api/subscriptions request body. The
// outer object carries a single "filter" object; DisallowUnknownFields on
// the decoder rejects any sibling key.
type subscriptionRequest struct {
	Filter *subscriptionFilterJSON `json:"filter"`
}

// subscriptionFilterJSON is the wire shape of the filter object. Pointers
// distinguish "absent" from "present but empty" so the present-but-empty
// array rule (BAD_REQUEST) can fire. time_range is a nested object.
type subscriptionFilterJSON struct {
	TimeRange     *timeRangeJSON `json:"time_range"`
	Sources       *[]string      `json:"sources"`
	Agents        *[]string      `json:"agents"`
	Models        *[]string      `json:"models"`
	Tools         *[]string      `json:"tools"`
	Status        *[]string      `json:"status"`
	SessionID     *string        `json:"session_id"`
	RootSessionID *string        `json:"root_session_id"`
}

// timeRangeJSON is the {from,to} sub-object. Both bounds are optional
// UNIX-microsecond integers; a null bound is "open" on that side.
type timeRangeJSON struct {
	From *int64 `json:"from"`
	To   *int64 `json:"to"`
}

// normalizedFilter is the filter_normalized echo on the POST response and
// the reusable normalized shape. Arrays are sorted+deduped; omitted
// dimensions are absent (nil) so the JSON omits them.
type normalizedFilter struct {
	TimeRange     *timeRangeJSON `json:"time_range,omitempty"`
	Sources       []string       `json:"sources,omitempty"`
	Agents        []string       `json:"agents,omitempty"`
	Models        []string       `json:"models,omitempty"`
	Tools         []string       `json:"tools,omitempty"`
	Status        []string       `json:"status,omitempty"`
	SessionID     *string        `json:"session_id,omitempty"`
	RootSessionID *string        `json:"root_session_id,omitempty"`
}

// parseSubscriptionFilter decodes and validates the POST body into a
// subscriptionFilter. Unknown fields (outer or filter) are rejected;
// validation reuses the Chunk-12 rules (present-but-empty array →
// BAD_REQUEST, ASCII control char → BAD_REQUEST, from>to → BAD_REQUEST) so
// the SSE filter and the REST list filter validate identically. A body that
// carries trailing tokens after the JSON object (e.g. `{"filter":{}} {}`)
// is rejected too, mirroring decodeCursor's trailing-byte guard, so a
// second object cannot be silently ignored. Returns a wrapped errBadFilter
// on any invalid input so the handler maps it to one BAD_REQUEST envelope.
func parseSubscriptionFilter(body []byte) (subscriptionFilter, error) {
	var f subscriptionFilter
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req subscriptionRequest
	if err := dec.Decode(&req); err != nil {
		return f, wrapBadFilter("request body is not valid JSON")
	}
	// Reject trailing tokens after the object so a body with a second value
	// (garbage, a second object) is a hard error rather than silently
	// accepting only the first.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return f, wrapBadFilter("request body has trailing data after the JSON object")
	}
	if req.Filter == nil {
		// Absent or null filter: no constraints.
		f.base = sessionFilter{group: groupAll}
		return f, nil
	}
	return buildSubscriptionFilter(*req.Filter)
}

// buildSubscriptionFilter validates each dimension of the decoded filter
// object and assembles the typed subscriptionFilter. group is forced to
// groupAll so whereClause omits the kind='root' predicate.
func buildSubscriptionFilter(in subscriptionFilterJSON) (subscriptionFilter, error) {
	f := subscriptionFilter{base: sessionFilter{group: groupAll}}
	if err := applySubscriptionTimeRange(in.TimeRange, &f.base); err != nil {
		return f, err
	}
	arrays := []struct {
		key string
		in  *[]string
		dst *[]string
	}{
		{"sources", in.Sources, &f.base.source},
		{"agents", in.Agents, &f.base.agents},
		{"models", in.Models, &f.base.models},
		{"tools", in.Tools, &f.base.tools},
		{"status", in.Status, &f.base.status},
	}
	for _, a := range arrays {
		vals, err := normalizeFilterArray(a.key, a.in)
		if err != nil {
			return f, err
		}
		*a.dst = vals
	}
	sid, err := normalizeScalar("session_id", in.SessionID)
	if err != nil {
		return f, err
	}
	f.sessionID = sid
	rid, err := normalizeScalar("root_session_id", in.RootSessionID)
	if err != nil {
		return f, err
	}
	f.rootSessionID = rid
	return f, nil
}

// applySubscriptionTimeRange validates the optional from/to bounds and
// writes them onto the embedded filter. Unlike the REST parser there is NO
// now-default for `to`: an omitted upper bound means "open-ended into the
// future", the typical real-time subscription case (sse-protocol.md
// §Filter Shape). from>to is a BAD_REQUEST.
func applySubscriptionTimeRange(tr *timeRangeJSON, base *sessionFilter) error {
	if tr == nil {
		return nil
	}
	if tr.From != nil && tr.To != nil && *tr.From > *tr.To {
		return wrapBadFilter("'from' is after 'to'")
	}
	base.from = tr.From
	base.to = tr.To
	return nil
}

// normalizeFilterArray applies the present-but-empty + control-char rules
// to one array dimension and returns the sorted, deduplicated values (nil
// when the dimension is absent). A present-but-empty array (every element
// blank, or a literal []) is a BAD_REQUEST, matching
// parseRequiredNonEmptyArray for the REST surface.
func normalizeFilterArray(key string, in *[]string) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	for _, raw := range *in {
		if err := rejectControlChars(key, raw); err != nil {
			return nil, err
		}
	}
	vals := parseArrayParam(*in)
	if len(vals) == 0 {
		return nil, wrapBadFilter("filter " + quoteKey(key) + " is present but empty")
	}
	sort.Strings(vals)
	return vals, nil
}

// normalizeScalar validates an optional scalar string (session_id,
// root_session_id): rejects control chars on the RAW value first, then trims
// surrounding whitespace; an empty string after trimming is a
// present-but-empty BAD_REQUEST so a client bug surfaces rather than silently
// matching everything. The trimmed value is returned so the stored predicate
// and the filter_normalized echo carry the canonical form — matching the
// array dimensions (parseArrayParam) and the REST surface
// (session_detail.go, subscriptions.go), which all trim.
func normalizeScalar(key string, in *string) (*string, error) {
	if in == nil {
		return nil, nil
	}
	if err := rejectControlChars(key, *in); err != nil {
		return nil, err
	}
	v := strings.TrimSpace(*in)
	if v == "" {
		return nil, wrapBadFilter("filter " + quoteKey(key) + " is present but empty")
	}
	return &v, nil
}

// normalized renders the filter_normalized echo. Arrays are already
// sorted+deduped by buildSubscriptionFilter; the embedded filter's slices
// are copied directly (nil stays nil so the JSON omits the dimension).
func (f subscriptionFilter) normalized() normalizedFilter {
	out := normalizedFilter{
		Sources:       f.base.source,
		Agents:        f.base.agents,
		Models:        f.base.models,
		Tools:         f.base.tools,
		Status:        f.base.status,
		SessionID:     f.sessionID,
		RootSessionID: f.rootSessionID,
	}
	if f.base.from != nil || f.base.to != nil {
		out.TimeRange = &timeRangeJSON{From: f.base.from, To: f.base.to}
	}
	return out
}

// matches reports whether ev should be delivered to a subscription
// carrying this filter. Matching is per event kind (sse-protocol.md
// §Event Types):
//
//   - session_changed: the session must satisfy the SQL dimensions
//     (whereClause, reused from Chunk-12) AND the session_id /
//     root_session_id equality predicates (checked against the event's
//     own ids, cheaply, in Go).
//   - stats_invalidated: matches every subscription (the rows are
//     coalesced by the poller).
//   - source_status_changed: matches when the filter has no sources
//     constraint or the event's source is in the sources set.
//
// The SQL is a single parameterized point lookup so it stays cheap in the
// poller goroutine. Any other kind never matches.
func (f subscriptionFilter) matches(ctx context.Context, db *sql.DB, ev notify.Event) (bool, error) {
	switch ev.Kind {
	case "session_changed":
		return f.matchesSession(ctx, db, ev)
	case "stats_invalidated":
		return true, nil
	case "source_status_changed":
		return f.admitsSource(ev.SourceID), nil
	default:
		return false, nil
	}
}

// matchesSession evaluates the session_id/root_session_id equality
// predicates first (cheap, no SQL) and then the SQL dimension match.
func (f subscriptionFilter) matchesSession(ctx context.Context, db *sql.DB, ev notify.Event) (bool, error) {
	if f.sessionID != nil && ev.SessionID != *f.sessionID {
		return false, nil
	}
	if f.rootSessionID != nil && ev.RootSessionID != *f.rootSessionID {
		return false, nil
	}
	where, args := f.base.whereClause("s")
	query := "SELECT 1 FROM sessions s WHERE s.id = ? AND (" + where + ") LIMIT 1"
	queryArgs := make([]any, 0, len(args)+1)
	queryArgs = append(queryArgs, ev.SessionID)
	queryArgs = append(queryArgs, args...)
	var one int
	err := db.QueryRowContext(ctx, query, queryArgs...).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// admitsSource reports whether a source_status_changed event for sourceID
// matches this filter: true when the filter places no constraint on
// sources, otherwise true only when sourceID is one of the named sources.
func (f subscriptionFilter) admitsSource(sourceID string) bool {
	if len(f.base.source) == 0 {
		return true
	}
	for _, s := range f.base.source {
		if s == sourceID {
			return true
		}
	}
	return false
}

// quoteKey wraps a filter key in double quotes for operator-facing error
// messages, matching the Chunk-12 strconv.Quote usage without importing
// strconv into this file for a single call shape.
func quoteKey(key string) string {
	return "\"" + key + "\""
}
