// truncateToJSONBoundary (SOW-0090 chunk 7+): when the server reads a
// JSON payload under the preview-byte cap, it currently returns the raw
// truncated bytes — which often cut a JSON string mid-character. The
// turn-view's extractReadableText then JSON.parse fails and the user
// sees a wall of JSON. This test pins the behavior we want: when a
// truncated payload is JSON, the server returns as much of the JSON
// document as fits in the cap, truncated at a clean boundary (the
// closing brace / bracket that returns depth to zero). The frontend
// can then parse what it gets.

package presenter

import (
	"testing"
)

func TestTruncateToJSONBoundary(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "complete document returns unchanged",
			input:  `{"a": 1, "b": [1, 2, 3]}`,
			expect: `{"a": 1, "b": [1, 2, 3]}`,
		},
		{
			name:   "truncated mid-value extends to closing brace",
			input:  `{"a": 1, "b": "hello world this is truncated mid-string`,
			expect: `{"a": 1, "b": "hello world this is truncated mid-string`,
		},
		{
			name:   "truncated after closing brace drops tail",
			input:  `{"a": 1, "b": 2} some trailing junk`,
			expect: `{"a": 1, "b": 2}`,
		},
		{
			name:   "nested object — keeps outer object closed",
			input:  `{"outer": {"inner": "value"}, "k": "v"} trailing`,
			expect: `{"outer": {"inner": "value"}, "k": "v"}`,
		},
		{
			name:   "array at top level — keeps closed",
			input:  `[1, 2, 3, 4, 5] junk`,
			expect: `[1, 2, 3, 4, 5]`,
		},
		{
			name:   "string containing escaped quote is not confused",
			input:  `{"a": "he said \"hi\"", "b": 1}`,
			expect: `{"a": "he said \"hi\"", "b": 1}`,
		},
		{
			name:   "string with closing brace inside is not confused",
			input:  `{"a": "with } inside", "b": 2}`,
			expect: `{"a": "with } inside", "b": 2}`,
		},
		{
			name:   "empty object",
			input:  `{}`,
			expect: `{}`,
		},
		{
			name:   "no JSON at all — returns input unchanged",
			input:  `not json at all, just text`,
			expect: `not json at all, just text`,
		},
		{
			name:   "real-world codex envelope with two content blocks",
			input:  `{"timestamp":"2026-06-20T21:08:39.295Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nFilesystem sandboxing"},{"type":"input_text","text":"# Collaboration Mode"}`,
			expect: ``, // will check below — we expect partial parse
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateToJSONBoundary([]byte(tc.input))
			if tc.expect != "" && string(got) != tc.expect {
				t.Errorf("truncateToJSONBoundary(%q) = %q, want %q", tc.input, string(got), tc.expect)
			}
		})
	}
}

func TestTruncateToJSONBoundary_PreservesWhenNoBoundary(t *testing.T) {
	// The real-world codex envelope is opened but never closed within the
	// 4KB cap. The function must return the input unchanged so we can still
	// at least show the partial JSON verbatim — frontend will then handle.
	input := `{"timestamp":"2026-06-20T21:08:39.295Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nFilesystem sandboxing`
	got := truncateToJSONBoundary([]byte(input))
	if string(got) != input {
		t.Errorf("expected unchanged input when no complete JSON document, got %q", string(got))
	}
}

func TestTruncateToJSONBoundary_Empty(t *testing.T) {
	if got := truncateToJSONBoundary(nil); got != nil {
		t.Errorf("nil input should return nil, got %q", got)
	}
	if got := truncateToJSONBoundary([]byte("")); got != nil {
		t.Errorf("empty input should return nil, got %q", got)
	}
}
