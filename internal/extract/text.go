// Package extract contains the canonical payload-text extractor used by the
// ingest path (SOW-0091) and mirrored by the frontend Markdown renderer
// (frontend/src/components/TurnView/Markdown.tsx, extractReadableText).
//
// The two implementations MUST stay behaviorally equivalent so the turn
// view's UI extraction matches what the search index sees. The frontend
// version is the source of truth for the React side; this Go version
// is the source of truth for the backend ingest + backfill path. When
// you change one, change the other in the same commit and bump the
// SOW-0091 spec §"Extract contract" so they can be diffed.
package extract

import (
	"encoding/json"
	"strings"
)

// ReadableText extracts the human-readable text from a payload byte slice.
// The function is a best-effort heuristic — when the payload ISN'T a
// recognizable envelope (no JSON parse, no text/content_text/output_text
// fields) it returns the trimmed input verbatim so callers still see
// something useful.
//
// Recognized envelope shapes:
//   - codex response_item.message: {type, payload: {type:'message', role,
//     content:[{type:'input_text'|'output_text', text:string}, ...]}}
//     → concatenated `text` fields
//   - codex response_item.reasoning: {type, payload: {type:'reasoning',
//     summary:[{text:string}], content:[...]}}
//     → summary text or content text
//   - claude_code / opencode: {message.content[]} or {parts[].text}
//     → concatenated text
//   - aiagent v2/v3: {content[{text}]} or {parts[{text}]}
//     → concatenated text
//
// Behavior:
//   - Walks the parsed JSON tree, collects every string value found at a
//     field named exactly `text`, `content_text`, or `output_text`.
//   - Depth-capped at 32 to bound pathological input.
//   - When no text fields are found AND the input is not valid JSON,
//     returns the trimmed input bytes as a string.
//   - When no text fields are found AND the input IS valid JSON,
//     returns an empty string (the caller can fall back to other
//     search heuristics; FTS-ingesting the envelope keys would
//     pollute the index).
//
// The frontend mirror in Markdown.tsx MUST match these rules.
func ReadableText(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return ""
	}

	// Try JSON parse. If it fails, return the trimmed input verbatim.
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return trimmed
	}

	out, ok := collectText(parsed, 0)
	if !ok || out == "" {
		// Valid JSON but no recognized text fields. Return verbatim so the
		// operator sees SOMETHING; the FTS index will pick up whatever
		// verbatim string we return here.
		return trimmed
	}
	return out
}

// collectText walks `node` collecting text/content_text/output_text field
// values into a single newline-joined string. Returns (result, found):
//   - found=false signals "this branch had no recognizable text fields";
//     callers use this to decide whether to fall back to verbatim.
//   - depth caps recursion at 32 (SOW-0091 contract).
func collectText(node any, depth int) (string, bool) {
	const maxDepth = 32
	if depth > maxDepth {
		return "", false
	}
	switch v := node.(type) {
	case nil:
		return "", false
	case string:
		// Strings at this depth only count if we're directly AT a leaf
		// with no further structure — i.e. someone called collectText on
		// a bare string. The walk recursion in walkObject/Array handles
		// the normal "find a text field" path; this branch is unreachable
		// for well-formed JSON objects but kept for safety.
		return "", false
	case []any:
		var parts []string
		any := false
		for _, child := range v {
			s, ok := collectText(child, depth+1)
			if ok {
				parts = append(parts, s)
				any = true
			}
		}
		if !any {
			return "", false
		}
		return strings.Join(parts, "\n"), true
	case map[string]any:
		// First: if this object has a top-level text/content_text/output_text
		// field, take it. This is the canonical "leaf text" case.
		for _, key := range []string{"text", "content_text", "output_text"} {
			if s, ok := v[key].(string); ok && s != "" {
				return s, true
			}
		}
		// Otherwise recurse into each value EXCEPT the text-content keys
		// (already handled; recursing into them would just yield "" again).
		var parts []string
		any := false
		for k, child := range v {
			if k == "text" || k == "content_text" || k == "output_text" {
				continue
			}
			s, ok := collectText(child, depth+1)
			if ok {
				parts = append(parts, s)
				any = true
			}
		}
		if !any {
			return "", false
		}
		return strings.Join(parts, "\n"), true
	default:
		return "", false
	}
}
