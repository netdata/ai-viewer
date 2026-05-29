package presenter

import (
	"context"
	"reflect"
	"testing"

	"github.com/netdata/ai-viewer/internal/notify"
)

// TestParseSubscriptionFilter_Valid asserts a fully-populated filter
// parses, normalizes its arrays (sorted, deduped), and round-trips the
// scalar dimensions. The normalized shape is what POST echoes back as
// filter_normalized.
func TestParseSubscriptionFilter_Valid(t *testing.T) {
	t.Parallel()
	body := []byte(`{"filter":{
		"time_range":{"from":100,"to":200},
		"sources":["src2","src1","src1"],
		"agents":["neda","nedi"],
		"models":["claude-opus-4-7"],
		"tools":["Bash","Read"],
		"status":["failed"],
		"session_id":"s1",
		"root_session_id":"rootA"
	}}`)
	f, err := parseSubscriptionFilter(body)
	if err != nil {
		t.Fatalf("parseSubscriptionFilter: %v", err)
	}
	norm := f.normalized()
	if norm.TimeRange == nil || norm.TimeRange.From == nil || *norm.TimeRange.From != 100 {
		t.Fatalf("time_range.from not normalized: %+v", norm.TimeRange)
	}
	if norm.TimeRange.To == nil || *norm.TimeRange.To != 200 {
		t.Fatalf("time_range.to not normalized: %+v", norm.TimeRange)
	}
	if !reflect.DeepEqual(norm.Sources, []string{"src1", "src2"}) {
		t.Fatalf("sources = %v, want sorted+deduped [src1 src2]", norm.Sources)
	}
	if !reflect.DeepEqual(norm.Agents, []string{"neda", "nedi"}) {
		t.Fatalf("agents = %v", norm.Agents)
	}
	if !reflect.DeepEqual(norm.Tools, []string{"Bash", "Read"}) {
		t.Fatalf("tools = %v", norm.Tools)
	}
	if norm.SessionID == nil || *norm.SessionID != "s1" {
		t.Fatalf("session_id = %v, want s1", norm.SessionID)
	}
	if norm.RootSessionID == nil || *norm.RootSessionID != "rootA" {
		t.Fatalf("root_session_id = %v, want rootA", norm.RootSessionID)
	}
}

// TestParseSubscriptionFilter_Empty asserts an absent or empty filter
// object is valid (no constraints) and normalizes to an all-absent shape.
func TestParseSubscriptionFilter_Empty(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"filter":{}}`, `{"filter":null}`} {
		f, err := parseSubscriptionFilter([]byte(body))
		if err != nil {
			t.Fatalf("parseSubscriptionFilter(%s): %v", body, err)
		}
		norm := f.normalized()
		if norm.TimeRange != nil || norm.Sources != nil || norm.Agents != nil ||
			norm.Models != nil || norm.Tools != nil || norm.Status != nil ||
			norm.SessionID != nil || norm.RootSessionID != nil {
			t.Fatalf("normalized(%s) = %+v, want all-absent", body, norm)
		}
	}
}

// TestParseSubscriptionFilter_Rejects asserts the validation rules shared
// with the REST list filters: unknown fields, present-but-empty arrays,
// control characters, and from>to all fail.
func TestParseSubscriptionFilter_Rejects(t *testing.T) {
	t.Parallel()
	// The two control-char cases are assembled at runtime: a literal
	// control byte in a .go source string breaks the Go lexer, so the byte
	// is spliced in here instead of written inline.
	ctrlAgent := `{"filter":{"agents":["a` + string(rune(0x01)) + `b"]}}`
	ctrlSession := `{"filter":{"session_id":"a` + string(rune(0x01)) + `b"}}`
	cases := []struct {
		name string
		body string
	}{
		{"unknown top field", `{"filter":{},"bogus":1}`},
		{"unknown filter field", `{"filter":{"nope":1}}`},
		{"empty agents array", `{"filter":{"agents":[]}}`},
		{"empty-string element", `{"filter":{"models":[""]}}`},
		{"control char in agent", ctrlAgent},
		{"control char in session_id", ctrlSession},
		{"from after to", `{"filter":{"time_range":{"from":200,"to":100}}}`},
		{"malformed json", `{"filter":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseSubscriptionFilter([]byte(tc.body)); err == nil {
				t.Fatalf("parseSubscriptionFilter(%s): want error, got nil", tc.body)
			}
		})
	}
}

// TestParseSubscriptionFilter_ScalarWhitespace asserts the scalar
// session_id/root_session_id predicates are trimmed before the empty check
// (matching the array dimensions and session_detail.go / subscriptions.go):
// a whitespace-only value is a present-but-empty BAD_REQUEST, and a value
// with surrounding spaces is accepted and normalized (trimmed) in the
// filter_normalized echo. Without the trim, a whitespace-only scalar would
// slip through and silently match everything.
func TestParseSubscriptionFilter_ScalarWhitespace(t *testing.T) {
	t.Parallel()

	// Space-only scalars pass the control-char check (space is 0x20, not a
	// control byte) and then trim to empty → present-but-empty BAD_REQUEST.
	// This is the path the D1 fix adds: without TrimSpace the value would
	// slip through and silently match everything.
	for _, body := range []string{
		`{"filter":{"session_id":"   "}}`,
		`{"filter":{"root_session_id":"  "}}`,
	} {
		if _, err := parseSubscriptionFilter([]byte(body)); err == nil {
			t.Fatalf("parseSubscriptionFilter(%s): want BAD_REQUEST error, got nil", body)
		}
	}

	// Surrounding spaces are accepted and the stored value is trimmed.
	f, err := parseSubscriptionFilter([]byte(`{"filter":{"session_id":"  s1  ","root_session_id":" rootA "}}`))
	if err != nil {
		t.Fatalf("parseSubscriptionFilter (padded scalars): %v", err)
	}
	norm := f.normalized()
	if norm.SessionID == nil || *norm.SessionID != "s1" {
		t.Fatalf("session_id = %v, want trimmed \"s1\"", norm.SessionID)
	}
	if norm.RootSessionID == nil || *norm.RootSessionID != "rootA" {
		t.Fatalf("root_session_id = %v, want trimmed \"rootA\"", norm.RootSessionID)
	}
}

// TestSubscriptionFilter_MatchesSessionChanged exercises matches() for
// session_changed events against a seeded DB: a row that satisfies the
// SQL dimensions matches; one that does not match on status, source, or
// the session_id/root_session_id equality predicates does not.
func TestSubscriptionFilter_MatchesSessionChanged(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = p
	seedGraph(t, db, seedBase())

	ctx := context.Background()
	evRoot := notify.Event{Kind: "session_changed", SessionID: "rootA", RootSessionID: "rootA"}
	evChild := notify.Event{Kind: "session_changed", SessionID: "childA2", RootSessionID: "rootA"}

	mustMatch := func(t *testing.T, body string, ev notify.Event, want bool) {
		t.Helper()
		f, err := parseSubscriptionFilter([]byte(body))
		if err != nil {
			t.Fatalf("parse %s: %v", body, err)
		}
		got, err := f.matches(ctx, db, ev)
		if err != nil {
			t.Fatalf("matches %s: %v", body, err)
		}
		if got != want {
			t.Fatalf("matches(%s, %s) = %v, want %v", body, ev.SessionID, got, want)
		}
	}

	// No constraints: every session matches.
	mustMatch(t, `{"filter":{}}`, evRoot, true)
	// rootA is status=completed; a failed-only filter excludes it.
	mustMatch(t, `{"filter":{"status":["failed"]}}`, evRoot, false)
	// childA2 is status=failed.
	mustMatch(t, `{"filter":{"status":["failed"]}}`, evChild, true)
	// Source constraint admits src1 (the seeded source).
	mustMatch(t, `{"filter":{"sources":["src1"]}}`, evRoot, true)
	mustMatch(t, `{"filter":{"sources":["nope"]}}`, evRoot, false)
	// agents: rootA is nedi.
	mustMatch(t, `{"filter":{"agents":["nedi"]}}`, evRoot, true)
	mustMatch(t, `{"filter":{"agents":["neda"]}}`, evRoot, false)
	// session_id equality predicate.
	mustMatch(t, `{"filter":{"session_id":"rootA"}}`, evRoot, true)
	mustMatch(t, `{"filter":{"session_id":"childA2"}}`, evRoot, false)
	// root_session_id equality predicate matches both rootA and its child.
	mustMatch(t, `{"filter":{"root_session_id":"rootA"}}`, evChild, true)
	mustMatch(t, `{"filter":{"root_session_id":"other"}}`, evChild, false)
	// A child session must match even though it is not a root session
	// (the filter has no group dimension; whereClause must not add
	// kind='root').
	mustMatch(t, `{"filter":{}}`, evChild, true)
	// Unknown session id never matches.
	mustMatch(t, `{"filter":{}}`, notify.Event{Kind: "session_changed", SessionID: "ghost"}, false)
}

// TestSubscriptionFilter_MatchesNonSession asserts the non-session event
// kinds match per spec: stats_invalidated matches any subscription;
// source_status_changed matches when the filter has no sources constraint
// or the event's source is admitted.
func TestSubscriptionFilter_MatchesNonSession(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	_ = p
	ctx := context.Background()

	stats := notify.Event{Kind: "stats_invalidated"}
	for _, body := range []string{`{"filter":{}}`, `{"filter":{"sources":["src1"]}}`, `{"filter":{"status":["failed"]}}`} {
		f, _ := parseSubscriptionFilter([]byte(body))
		got, err := f.matches(ctx, db, stats)
		if err != nil {
			t.Fatalf("matches stats: %v", err)
		}
		if !got {
			t.Fatalf("stats_invalidated should match every subscription (%s)", body)
		}
	}

	srcEv := notify.Event{Kind: "source_status_changed", SourceID: "src1"}
	cases := []struct {
		body string
		want bool
	}{
		{`{"filter":{}}`, true},                       // no sources constraint
		{`{"filter":{"sources":["src1"]}}`, true},     // admitted
		{`{"filter":{"sources":["src1","x"]}}`, true}, // admitted among many
		{`{"filter":{"sources":["other"]}}`, false},   // not admitted
		{`{"filter":{"agents":["nedi"]}}`, true},      // non-source constraint => admit
	}
	for _, tc := range cases {
		f, _ := parseSubscriptionFilter([]byte(tc.body))
		got, err := f.matches(ctx, db, srcEv)
		if err != nil {
			t.Fatalf("matches source %s: %v", tc.body, err)
		}
		if got != tc.want {
			t.Fatalf("source_status_changed matches(%s) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
