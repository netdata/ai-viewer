package presenter

import (
	"net/url"
	"strings"
	"testing"
)

// parseValues is a tiny helper to build url.Values from a raw query.
func parseValues(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	return v
}

// TestParseSessionFilter_Defaults asserts an empty query yields the
// documented defaults: group=root, sort=start_ts, order=desc, limit=100.
func TestParseSessionFilter_Defaults(t *testing.T) {
	t.Parallel()
	f, err := parseSessionFilter(parseValues(t, ""), fixedTime)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.group != groupRoot {
		t.Fatalf("group = %q, want root", f.group)
	}
	if f.order != "desc" {
		t.Fatalf("order = %q, want desc", f.order)
	}
	if f.limit != defaultLimit {
		t.Fatalf("limit = %d, want %d", f.limit, defaultLimit)
	}
}

// TestParseSessionFilter_ArrayBothSyntaxes asserts comma-separated and
// repeated params both populate the same slice, and empty elements are
// dropped.
func TestParseSessionFilter_ArrayBothSyntaxes(t *testing.T) {
	t.Parallel()
	f, err := parseSessionFilter(parseValues(t, "models=a,b&models=c&models=,"), fixedTime)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(f.models) != 3 {
		t.Fatalf("models = %v, want 3 distinct", f.models)
	}
	for _, m := range f.models {
		if !want[m] {
			t.Fatalf("unexpected model %q", m)
		}
	}
}

// TestParseSessionFilter_Errors asserts every documented 400 condition.
func TestParseSessionFilter_Errors(t *testing.T) {
	t.Parallel()
	cases := []string{
		"from=9&to=1",
		"limit=1001",
		"limit=-1",
		"sort=cost",
		"order=up",
		"group=many",
		"from=abc",
		"to=xyz",
		"limit=abc",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := parseSessionFilter(parseValues(t, c), fixedTime); err == nil {
				t.Fatalf("parseSessionFilter(%q): want error", c)
			}
		})
	}
}

// TestParseSessionFilter_EmptyArrayFilterRejected asserts a present array
// key whose every element is empty is a 400 (presenter.md §Filters), while
// an absent key is fine and a present key with at least one non-empty
// value is fine even alongside empty segments.
func TestParseSessionFilter_EmptyArrayFilterRejected(t *testing.T) {
	t.Parallel()
	reject := []string{
		"models=",
		"agents=,",
		"status=,,",
		"tools=, ,",    // whitespace-only segments are empty after trim
		"sources=&q=x", // empty sources key still rejected
	}
	for _, c := range reject {
		t.Run("reject/"+c, func(t *testing.T) {
			if _, err := parseSessionFilter(parseValues(t, c), fixedTime); err == nil {
				t.Fatalf("parseSessionFilter(%q): want error (empty-only filter)", c)
			}
		})
	}
	accept := []string{
		"",                 // no array keys at all
		"models=a,",        // one non-empty value plus an empty segment
		"agents=a&agents=", // one populated, one empty value for same key
	}
	for _, c := range accept {
		t.Run("accept/"+c, func(t *testing.T) {
			if _, err := parseSessionFilter(parseValues(t, c), fixedTime); err != nil {
				t.Fatalf("parseSessionFilter(%q): want ok, got %v", c, err)
			}
		})
	}
}

// TestParseSessionFilter_ControlCharsRejected asserts that a filter value (or
// q) carrying an ASCII control character is a 400 (defense in
// depth). Control bytes never appear in legitimate agent/model/tool/status/
// source names or search text; rejecting them keeps junk out of the SQL and
// the cursor fingerprint. \x1e/\x1f (the old fingerprint separators) and \x00
// survive TrimSpace, so they must be caught explicitly.
func TestParseSessionFilter_ControlCharsRejected(t *testing.T) {
	t.Parallel()
	// url.ParseQuery does not decode raw control bytes for us, so build the
	// values directly with the control byte embedded in the element.
	reject := []url.Values{
		{"models": {"a\x1eb"}},      // record separator inside an array value
		{"agents": {"x\x1fy"}},      // unit separator inside an array value
		{"status": {"r\x00unning"}}, // NUL inside an array value
		{"q": {"foo\x1ebar"}},       // control char inside the search text
	}
	for i, v := range reject {
		if _, err := parseSessionFilter(v, fixedTime); err == nil {
			t.Fatalf("case %d %v: want 400 for control-char value", i, v)
		}
	}
	// A value with only ordinary printable bytes is accepted.
	if _, err := parseSessionFilter(url.Values{"models": {"normal-name"}}, fixedTime); err != nil {
		t.Fatalf("printable value rejected: %v", err)
	}
}

// TestSessionFilter_WhereClauseParameterized asserts the rendered WHERE
// uses only bound placeholders (no interpolated user input) and that the
// arg count matches the placeholder count.
func TestSessionFilter_WhereClauseParameterized(t *testing.T) {
	t.Parallel()
	f, err := parseSessionFilter(parseValues(t,
		"group=all&status=running,failed&agents=nedi&models=m1&sources=s1&q=foo&from=10&to=20"), fixedTime)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	where, args := f.whereClause("s")
	// No raw user values should appear in the SQL text.
	for _, danger := range []string{"nedi", "running", "failed", "m1", "s1", "foo"} {
		if strings.Contains(where, danger) {
			t.Fatalf("where clause leaks user input %q: %s", danger, where)
		}
	}
	if strings.Count(where, "?") != len(args) {
		t.Fatalf("placeholder count %d != arg count %d: %s",
			strings.Count(where, "?"), len(args), where)
	}
	if len(args) == 0 {
		t.Fatal("expected bound args for the supplied filters")
	}
}

// TestSessionFilter_ToDefaultsToNow asserts that when `to` is omitted the
// filter uses the injected now as the upper bound.
func TestSessionFilter_ToDefaultsToNow(t *testing.T) {
	t.Parallel()
	f, err := parseSessionFilter(parseValues(t, "from=10"), fixedTime)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.to == nil {
		t.Fatal("to is nil, want default to now")
	}
	if *f.to != fixedTime.UnixMicro() {
		t.Fatalf("to = %d, want now=%d", *f.to, fixedTime.UnixMicro())
	}
}
