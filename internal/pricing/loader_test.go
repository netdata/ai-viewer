package pricing

import (
	"fmt"
	"strings"
	"testing"
)

// TestLoaderRejectsMalformedJSON exercises the json.Unmarshal error
// branch. The error wraps so callers can errors.Is over it.
func TestLoaderRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	_, _, err := parseDoc([]byte("{not json"))
	if err == nil {
		t.Fatal("expected error from malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse json") {
		t.Errorf("error %q does not mention 'parse json'", err.Error())
	}
}

// TestLoaderRejectsBadSchemaVersion verifies the loader rejects v1
// (legacy) and any future version it doesn't understand.
func TestLoaderRejectsBadSchemaVersion(t *testing.T) {
	t.Parallel()
	const docJSON = `{
	  "version": 1,
	  "schema_url": "x",
	  "generated_at": "2026-05-27T00:00:00Z",
	  "generated_by": "test",
	  "currency": "USD",
	  "providers": []
	}`
	_, _, err := parseDoc([]byte(docJSON))
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("expected schema-version error, got %v", err)
	}
}

// TestLoaderValidationCases is a table of malformed-but-parseable
// inputs exercising every validateDoc branch.
func TestLoaderValidationCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{
			"missing currency",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"","providers":[]}`,
			"currency is required",
		},
		{
			"non-USD currency",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"EUR","providers":[]}`,
			"only USD is supported",
		},
		{
			"empty providers",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[]}`,
			"providers[] is empty",
		},
		{
			"missing generated_at",
			`{"version":2,"schema_url":"x","generated_at":"","generated_by":"t","currency":"USD","providers":[{"name":"x","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"generated_at is required",
		},
		{
			"malformed generated_at",
			`{"version":2,"schema_url":"x","generated_at":"not-rfc3339","generated_by":"t","currency":"USD","providers":[{"name":"x","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"not RFC3339",
		},
		{
			"missing generated_by",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"","currency":"USD","providers":[{"name":"x","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"generated_by is required",
		},
		{
			"empty provider name",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"","models":[]}]}`,
			"providers[0].name is empty",
		},
		{
			"duplicate provider",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[
				{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]},
				{"name":"P","models":[{"name":"n","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}
			]}`,
			"duplicate provider",
		},
		{
			"provider with no models",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[]}]}`,
			"has no models",
		},
		{
			"empty model name",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"","tiers":[]}]}]}`,
			"models[0].name is empty",
		},
		{
			"duplicate model name (case-folded)",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[
				{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]},
				{"name":"M","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}
			]}]}`,
			"duplicate model",
		},
		{
			"empty tiers",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[]}]}]}`,
			"has no tiers",
		},
		{
			"missing effective_date",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"effective_date is empty",
		},
		{
			"malformed effective_date",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"15/06/2025","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"not YYYY-MM-DD",
		},
		{
			"duplicate effective_date",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[
				{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}},
				{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":3,"output_per_million":4}}
			]}]}]}`,
			"duplicate effective_date",
		},
		{
			"missing citation_url",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"missing citation_url",
		},
		{
			"missing source",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"missing source",
		},
		{
			"negative input_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":-1,"output_per_million":2}}]}]}]}`,
			"negative input_per_million",
		},
		{
			"negative output_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":-2}}]}]}]}`,
			"negative output_per_million",
		},
		{
			"negative cache_read_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_read_per_million":-0.1}}]}]}]}`,
			"negative cache_read_per_million",
		},
		{
			"negative cache_write_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_per_million":-0.5}}]}]}]}`,
			"negative cache_write_per_million",
		},
		{
			"negative cache_write_5m_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_5m_per_million":-1}}]}]}]}`,
			"negative cache_write_5m_per_million",
		},
		{
			"negative cache_write_1h_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_1h_per_million":-3}}]}]}]}`,
			"negative cache_write_1h_per_million",
		},
		{
			"negative reasoning_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"reasoning_per_million":-5}}]}]}]}`,
			"negative reasoning_per_million",
		},
		{
			"missing schema_url",
			`{"version":2,"schema_url":"","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"schema_url is required",
		},
		{
			"negative ctx_max",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","ctx_max":-1,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"ctx_max is negative",
		},
		{
			"unknown top-level field",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[],"unknown_extra":"oops"}`,
			"unknown field",
		},
		{
			// Empty prices object: both required fields absent. Must
			// fail with a clear message rather than silently pricing
			// the tier at zero.
			"prices object empty",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{}}]}]}]}`,
			"missing required input_per_million",
		},
		{
			"prices missing input_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"output_per_million":2}}]}]}]}`,
			"missing required input_per_million",
		},
		{
			"prices missing output_per_million",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1}}]}]}]}`,
			"missing required output_per_million",
		},
		{
			// Mixed-case provider name: must be accepted, since lookup
			// is case-insensitive and the schema pattern was broadened
			// to allow it. This is an asserts-no-error case; an empty
			// wantSubstr is invalid for the table runner, so we use a
			// shape that triggers a downstream failure and check we got
			// past the provider-name pattern check.
			"mixed-case provider name passes provider validation",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"xAI","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":-2}}]}]}]}`,
			"negative output_per_million",
		},
		{
			"provider name with invalid character",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"bad name","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			"invalid provider name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDoc([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestLoaderRejectsNullsInOptionals covers the iter-8 fix iter8-3:
// the Go loader is the runtime safety net (pricing.md §"Validation")
// and must reject `null` for every schema field declared as a typed
// (non-nullable) value, mirroring scripts/lib/pricing-validate.jq.
// Prior to the fix, json.Unmarshal silently decoded `aliases: null`
// into nil slice, `ctx_max: null` into 0, and an optional price set
// to null into 0 — none of which match the schema. validateDoc could
// not distinguish "absent" from "null" because both produced the same
// Go zero value.
func TestLoaderRejectsNullsInOptionals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{
			"provider aliases null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","aliases":null,"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			`"aliases" is null`,
		},
		{
			"model aliases null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","aliases":null,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			`"aliases" is null`,
		},
		{
			"ctx_max null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","ctx_max":null,"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`,
			`"ctx_max" is null`,
		},
		{
			"cache_read_per_million null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_read_per_million":null}}]}]}]}`,
			`"cache_read_per_million" is null`,
		},
		{
			"cache_write_per_million null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_per_million":null}}]}]}]}`,
			`"cache_write_per_million" is null`,
		},
		{
			"cache_write_5m_per_million null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_5m_per_million":null}}]}]}]}`,
			`"cache_write_5m_per_million" is null`,
		},
		{
			"cache_write_1h_per_million null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"cache_write_1h_per_million":null}}]}]}]}`,
			`"cache_write_1h_per_million" is null`,
		},
		{
			"reasoning_per_million null",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2,"reasoning_per_million":null}}]}]}]}`,
			`"reasoning_per_million" is null`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDoc([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestLoaderAcceptsAbsentOptionals proves the null-rejection guard
// does NOT regress against omitted optional fields. Schema declares
// these as optional + typed; absence is legal and decode-zero is fine
// when the field genuinely was not set.
func TestLoaderAcceptsAbsentOptionals(t *testing.T) {
	t.Parallel()
	// Minimal doc with NO aliases, NO ctx_max, and NO optional prices.
	const docJSON = `{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`
	if _, _, err := parseDoc([]byte(docJSON)); err != nil {
		t.Fatalf("absent optionals should be accepted, got: %v", err)
	}
}

// TestLoaderRejectsTrailingJSON covers the iter-9 fix iter9-3:
// json.Decoder.Decode consumes one top-level value and silently
// leaves trailing content unread. The pricing schema declares a
// single document; concatenated JSON values (e.g. `{valid}\n{}`) or
// any trailing non-whitespace content is a corrupted seed and must
// be rejected, mirroring the jq validator's single-value contract.
// Trailing whitespace (newlines, spaces, tabs) is fine — that is
// just file-formatting and io.EOF still fires after the whitespace
// scan.
func TestLoaderRejectsTrailingJSON(t *testing.T) {
	t.Parallel()
	const validDoc = `{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`
	cases := []struct {
		name string
		body string
	}{
		{"empty object after doc", validDoc + "\n{}"},
		{"second valid doc", validDoc + "\n" + validDoc},
		{"trailing array", validDoc + "[]"},
		{"trailing number", validDoc + " 42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDoc([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected trailing-content error, got nil")
			}
			if !strings.Contains(err.Error(), "trailing JSON content") {
				t.Errorf("error %q does not mention 'trailing JSON content'", err.Error())
			}
		})
	}
}

// TestLoaderAcceptsTrailingWhitespace pins the iter-9 fix iter9-3
// "boring trailing whitespace" path: a valid doc followed by any
// amount of whitespace must still load. json.Decoder's second
// Decode call skips whitespace before reading the next token, so
// io.EOF fires cleanly. Without this test a future "reject EVERY
// byte after the doc" tightening would silently break pretty-
// printed files that end with a newline.
func TestLoaderAcceptsTrailingWhitespace(t *testing.T) {
	t.Parallel()
	const validDoc = `{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]}]}`
	cases := []string{
		validDoc + "\n",
		validDoc + "  \n  ",
		validDoc + "\t\n\t",
		validDoc + "\r\n",
	}
	for i, body := range cases {
		body := body
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			t.Parallel()
			if _, _, err := parseDoc([]byte(body)); err != nil {
				t.Fatalf("trailing whitespace should be accepted, got: %v", err)
			}
		})
	}
}

// TestLoaderRejectsAliasCollisions covers every alias-collision shape
// the loader must catch: (a) two models within the same provider
// sharing an alias, (b) a model alias matching a sibling model's
// canonical name, (c) two providers sharing an alias, (d) a provider
// alias matching another provider's canonical name. Each case must
// fail at parseDoc time with a descriptive error so the operator sees
// the collision instead of the lookup silently routing to the wrong
// entry.
func TestLoaderRejectsAliasCollisions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			"two models share an alias within a provider",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[
				{"name":"alpha","aliases":["shared"],"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]},
				{"name":"beta","aliases":["shared"],"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":3,"output_per_million":4}}]}
			]}]}`,
		},
		{
			"model alias matches sibling model canonical name",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[{"name":"p","models":[
				{"name":"alpha","aliases":["beta"],"tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]},
				{"name":"beta","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":3,"output_per_million":4}}]}
			]}]}`,
		},
		{
			"two providers share an alias",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[
				{"name":"p1","aliases":["shared"],"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]},
				{"name":"p2","aliases":["shared"],"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":3,"output_per_million":4}}]}]}
			]}`,
		},
		{
			"provider alias matches sibling provider canonical name",
			`{"version":2,"schema_url":"x","generated_at":"2026-05-27T00:00:00Z","generated_by":"t","currency":"USD","providers":[
				{"name":"p1","aliases":["p2"],"models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":1,"output_per_million":2}}]}]},
				{"name":"p2","models":[{"name":"m","tiers":[{"effective_date":"2025-01-01","citation_url":"u","source":"s","prices":{"input_per_million":3,"output_per_million":4}}]}]}
			]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseDoc([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected alias-collision error, got nil")
			}
			if !strings.Contains(err.Error(), "alias collision") {
				t.Errorf("error %q does not mention 'alias collision'", err.Error())
			}
		})
	}
}

// TestLoaderHappyPath proves a fully-populated valid doc loads with
// alias expansion in both provider and model dimensions.
func TestLoaderHappyPath(t *testing.T) {
	t.Parallel()
	const docJSON = `{
	  "version": 2,
	  "schema_url": "https://example.com",
	  "generated_at": "2026-05-27T00:00:00Z",
	  "generated_by": "test",
	  "currency": "USD",
	  "providers": [{
	    "name": "anthropic",
	    "aliases": ["claude"],
	    "models": [{
	      "name": "claude-opus-4-7",
	      "aliases": ["claude-opus-4.7"],
	      "ctx_max": 1000000,
	      "tiers": [{
	        "effective_date": "2026-01-01",
	        "citation_url": "https://example.com",
	        "source": "test",
	        "prices": {
	          "input_per_million": 15,
	          "output_per_million": 75,
	          "cache_read_per_million": 1.5,
	          "cache_write_per_million": 18.75,
	          "cache_write_5m_per_million": 18.75,
	          "cache_write_1h_per_million": 30,
	          "reasoning_per_million": 75
	        }
	      }]
	    }]
	  }]
	}`
	lookup, doc, err := parseDoc([]byte(docJSON))
	if err != nil {
		t.Fatalf("parseDoc: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("Version = %d, want 2", doc.Version)
	}
	// Canonical key.
	if lookup[modelKey{provider: "anthropic", model: "claude-opus-4-7"}] == nil {
		t.Error("canonical key missing")
	}
	// Provider alias.
	if lookup[modelKey{provider: "claude", model: "claude-opus-4-7"}] == nil {
		t.Error("provider-alias key missing")
	}
	// Model alias.
	if lookup[modelKey{provider: "anthropic", model: "claude-opus-4.7"}] == nil {
		t.Error("model-alias key missing")
	}
	// Cross of provider+model aliases.
	if lookup[modelKey{provider: "claude", model: "claude-opus-4.7"}] == nil {
		t.Error("cross-alias key missing")
	}
	// ctx_max carried through.
	entry := lookup[modelKey{provider: "anthropic", model: "claude-opus-4-7"}]
	if entry.ctxMax != 1000000 {
		t.Errorf("ctxMax = %d, want 1000000", entry.ctxMax)
	}
}
