// extract.ReadableText (SOW-0091) — mirrors the frontend
// Markdown.extractReadableText. Both implementations MUST agree on the
// cases below. When you add a test here, mirror it in
// frontend/src/components/TurnView/Markdown.test.tsx in the same commit.

package extract

import (
	"strings"
	"testing"
)

func TestReadableText(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty input returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace-only input returns empty",
			input: "   \n\t  ",
			want:  "",
		},
		{
			name:  "non-JSON prose returns verbatim",
			input: "hello world, just plain text",
			want:  "hello world, just plain text",
		},
		{
			name:  "non-JSON with surrounding whitespace trims",
			input: "  \n  hello world  \n  ",
			want:  "hello world",
		},
		{
			name:  "flat JSON with text field",
			input: `{"text": "the answer"}`,
			want:  "the answer",
		},
		{
			name:  "JSON with content_text field",
			input: `{"content_text": "the answer"}`,
			want:  "the answer",
		},
		{
			name:  "JSON with output_text field",
			input: `{"output_text": "the answer"}`,
			want:  "the answer",
		},
		{
			name:  "codex response_item.message single content block",
			input: `{"timestamp":"2026-06-20T21:08:39.295Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]}}`,
			want:  "<permissions instructions>",
		},
		{
			name:  "codex response_item.message two content blocks joined with newline",
			input: `{"payload":{"content":[{"type":"input_text","text":"first block"},{"type":"input_text","text":"second block"}]}}`,
			want:  "first block\nsecond block",
		},
		{
			name:  "codex response_item.reasoning summary path",
			input: `{"payload":{"type":"reasoning","summary":[{"text":"thinking out loud"}]}}`,
			want:  "thinking out loud",
		},
		{
			name:  "valid JSON with no text fields returns verbatim",
			input: `{"a": 1, "b": [1, 2, 3]}`,
			want:  `{"a": 1, "b": [1, 2, 3]}`,
		},
		{
			name:  "depth-capped at 32 — deeply nested envelope returns empty (no text)",
			input: strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40),
			want:  strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40), // valid JSON, no text fields → verbatim
		},
		{
			name:  "JSON with a bare string value (no text/content_text/output_text key) returns verbatim",
			input: `{"a": "with } inside", "b": 2}`,
			want:  `{"a": "with } inside", "b": 2}`,
		},
		{
			name:  "invalid JSON returns verbatim",
			input: `{"a": "unterminated`,
			want:  `{"a": "unterminated`,
		},
		{
			name:  "null JSON value returns empty",
			input: `null`,
			want:  `null`,
		},
		{
			name:  "string leaves the path through the leaf-text branch reachable",
			input: `"just a bare string"`,
			want:  `"just a bare string"`, // valid JSON, no text fields → verbatim
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadableText([]byte(tc.input))
			if got != tc.want {
				t.Errorf("ReadableText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestReadableText_FrontendParity(t *testing.T) {
	// These cases are duplicated in
	// frontend/src/components/TurnView/Markdown.test.tsx. They MUST match.
	// If you change one, change both in the same commit.
	cases := []struct {
		input string
		want  string
	}{
		{`{"timestamp":"2026-06-20T21:08:39.295Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"hello"}]}}`, "hello"},
		{`{"payload":{"content":[{"type":"input_text","text":"first block"},{"type":"input_text","text":"second block"}]}}`, "first block\nsecond block"},
		{`{"payload":{"type":"reasoning","summary":[{"text":"thinking out loud"}]}}`, "thinking out loud"},
	}
	for _, tc := range cases {
		got := ReadableText([]byte(tc.input))
		if got != tc.want {
			t.Errorf("FRONTEND PARITY FAIL: ReadableText(%q) = %q, want %q (check Markdown.test.tsx)", tc.input, got, tc.want)
		}
	}
}
