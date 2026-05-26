package aiagent_v3

import (
	"testing"
)

// FuzzParseLine exercises the v3 ledger line parser against arbitrary
// bytes. The contract under fuzz is: parseLine never panics regardless
// of input. It returns either a parsed record, a skip, or a wrapped
// error. The corpus seeds cover all five record types plus malformed
// envelopes and unknown record types.
func FuzzParseLine(f *testing.F) {
	seeds := [][]byte{
		// session_start, happy.
		[]byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"a","sessionId":"a","capturePayloads":true}`),
		// session_start with parentSessionId.
		[]byte(`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"r","sessionId":"c","parentSessionId":"r","headendId":"sub-agent","capturePayloads":true}`),
		// turn_start happy.
		[]byte(`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-05-26T10:00:00.000Z","originId":"a","sessionId":"a","turn":1}`),
		// turn_end with one llm op.
		[]byte(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:30.000Z","originId":"a","sessionId":"a","turn":1,"status":"ok","ops":[{"opId":"x","opIndex":1,"kind":"llm","status":"ok"}]}`),
		// turn_end with payload ref.
		[]byte(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-05-26T10:00:30.000Z","originId":"a","sessionId":"a","turn":1,"status":"ok","ops":[{"opId":"x","opIndex":1,"kind":"llm","status":"ok","payloadRefs":[{"kind":"llm_request","opId":"x","turn":1,"opIndex":1,"format":"http","compression":"gzip","path":"payloads/a/turn-0001/llm-0001-request.http.gz","captured":true,"truncated":false,"redacted":false}]}]}`),
		// session_summary completed.
		[]byte(`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:31.000Z","originId":"a","sessionId":"a","status":"ok","finalReport":{"format":"json","captured":true}}`),
		// session_summary failed.
		[]byte(`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-05-26T10:00:31.000Z","originId":"a","sessionId":"a","status":"failed","error":"oops"}`),
		// session_error.
		[]byte(`{"version":3,"recordType":"session_error","seq":5,"ts":"2026-05-26T10:00:35.000Z","originId":"a","sessionId":"a","error":"oops"}`),
		// malformed JSON.
		[]byte(`{not json`),
		// unknown record type.
		[]byte(`{"version":3,"recordType":"snazzy","seq":1,"ts":"2026-05-26T10:00:00.000Z","originId":"a","sessionId":"a"}`),
		// blank.
		[]byte(""),
		// whitespace only.
		[]byte("   \t\n"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// The parser MUST NOT panic on any input. We do not assert on
		// the returned values — only on the absence of panic. Any err
		// is fine; skip is fine; a parsed record is fine.
		_, _, _ = parseLine(data)
	})
}

// FuzzParseCursor exercises ParseCursor's defenses against arbitrary
// JSON-shaped cursor blobs. Contract: never panics; either returns a
// Cursor or a wrapped error.
func FuzzParseCursor(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		`{"version":1}`,
		`{"version":1,"files":{}}`,
		`{"version":1,"files":{"a.jsonl":{"offset":7}}}`,
		`{"version":99}`,
		`{"files":{"x":{}}}`,
		"not json",
		`{"version":1,"files":null}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseCursor(s)
	})
}
