package claude_code

import (
	"testing"
)

// FuzzParseLine exercises the JSONL line parser against arbitrary bytes. The
// contract under fuzz is: parseLine never panics regardless of input. It
// returns either a parsed record, a skip, or a wrapped error. Seeds cover
// every observed `type`, the polymorphic user content shapes, the metadata
// snapshots, malformed JSON, and unknown / known-no-op types. Satisfies
// SOW-0003 acceptance #7 (fuzz target on the JSONL parser).
func FuzzParseLine(f *testing.F) {
	for _, s := range parseLineFuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// MUST NOT panic on any input. Returned values are not asserted.
		rec, skip, err := parseLine(data)
		// If a record parsed without skip/err, classifying its user content
		// must also not panic.
		if err == nil && !skip && rec.User != nil {
			_, _, _ = classifyUserContent(rec.User)
		}
	})
}

func parseLineFuzzSeeds() [][]byte {
	var seeds [][]byte
	seeds = append(seeds, parseLineUserSeeds()...)
	seeds = append(seeds, parseLineAssistantSeeds()...)
	seeds = append(seeds, parseLineSystemSeeds()...)
	seeds = append(seeds, parseLineMetadataSeeds()...)
	seeds = append(seeds, parseLineEdgeSeeds()...)
	return seeds
}

func parseLineUserSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"user","uuid":"u1","parentUuid":null,"isSidechain":false,"message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z","sessionId":"s","version":"2.1.150"}`),
		[]byte(`{"type":"user","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false}]},"toolUseResult":{"stdout":"x"},"timestamp":"2026-05-26T10:00:01.000Z","sessionId":"s"}`),
		[]byte(`{"type":"user","uuid":"u3","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>"},"timestamp":"2026-05-26T10:00:02.000Z","sessionId":"s"}`),
		[]byte(`{"type":"user","uuid":"u4","isCompactSummary":true,"message":{"role":"user","content":"summary..."},"timestamp":"2026-05-26T10:00:03.000Z","sessionId":"s"}`),
	}
}

func parseLineAssistantSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"s","message":{"id":"msg_1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":1,"cache_read_input_tokens":100,"server_tool_use":{"web_search_requests":1}},"content":[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"text","text":"hi"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},"timestamp":"2026-05-26T10:00:04.000Z"}`),
		[]byte(`{"type":"assistant","uuid":"a2","sessionId":"s","message":{"id":"msg_2","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"status"}]},"timestamp":"2026-05-26T10:00:05.000Z"}`),
		[]byte(`{"type":"assistant","uuid":"a3","sessionId":"s","message":{"id":"msg_3","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"toolu_agent","name":"Agent","input":{"description":"d","subagent_type":"general-purpose"}}]},"timestamp":"2026-05-26T10:00:06.000Z"}`),
	}
}

func parseLineSystemSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"system","subtype":"compact_boundary","uuid":"sy1","parentUuid":null,"logicalParentUuid":"a1","sessionId":"s","compactMetadata":{"trigger":"manual","preTokens":700000,"postTokens":8000,"durationMs":110000,"preservedSegment":{"headUuid":"u1","anchorUuid":"a1","tailUuid":"a1"},"preservedMessages":{"anchorUuid":"a1","uuids":["a1"]}},"timestamp":"2026-05-26T10:00:07.000Z"}`),
		[]byte(`{"type":"system","subtype":"turn_duration","uuid":"sy2","sessionId":"s","durationMs":5000,"messageCount":4,"timestamp":"2026-05-26T10:00:08.000Z"}`),
		[]byte(`{"type":"system","subtype":"api_error","uuid":"sy3","sessionId":"s","content":"err","error":{"status":529,"type":"overloaded_error"},"retryInMs":1000,"retryAttempt":1,"timestamp":"2026-05-26T10:00:09.000Z"}`),
	}
}

func parseLineMetadataSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"attachment","uuid":"at1","parentUuid":"u1","sessionId":"s","attachment":{"type":"file","filename":"x"},"timestamp":"2026-05-26T10:00:10.000Z"}`),
		[]byte(`{"type":"queue-operation","operation":"enqueue","content":"queued","timestamp":"2026-05-26T10:00:11.000Z","sessionId":"s"}`),
		[]byte(`{"type":"last-prompt","lastPrompt":"p","leafUuid":"a1","sessionId":"s"}`),
		[]byte(`{"type":"ai-title","aiTitle":"t","sessionId":"s"}`),
		[]byte(`{"type":"custom-title","customTitle":"t","sessionId":"s"}`),
		[]byte(`{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"s"}`),
		[]byte(`{"type":"pr-link","sessionId":"s","prNumber":12,"prUrl":"https://x","prRepository":"o/r","timestamp":"2026-05-26T10:00:12.000Z"}`),
		[]byte(`{"type":"bridge-session","sessionId":"s","bridgeSessionId":"cse_x","lastSequenceNum":5}`),
		[]byte(`{"type":"file-history-snapshot","messageId":"a1","snapshot":{"messageId":"a1","trackedFileBackups":{},"timestamp":"2026-05-26T10:00:13.000Z"},"isSnapshotUpdate":false}`),
	}
}

func parseLineEdgeSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"summary","leafUuid":"a1","summary":"s"}`),
		[]byte(`{"type":"marble-origami-commit","sessionId":"s"}`),
		[]byte(`{"type":"totally-unknown","sessionId":"s"}`),
		[]byte(`{not json`),
		[]byte(""),
		[]byte("  \t\n "),
		[]byte(`{"type":"user","uuid":"u9","sessionId":"s","timestamp":"2026-05-26T10:00:00.000Z"}`),
		[]byte(`{"type":"user","uuid":"u10","sessionId":"s","message":{"role":"user","content":42},"timestamp":"2026-05-26T10:00:00.000Z"}`),
	}
}

// FuzzParseCursor exercises ParseCursor against arbitrary cursor blobs.
// Contract: never panics; either returns a Cursor or a wrapped error.
func FuzzParseCursor(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		`{"version":1}`,
		`{"version":1,"files":{}}`,
		`{"version":1,"files":{"a/b.jsonl":{"offset":7,"size":7}}}`,
		`{"version":1,"files":{"a.jsonl":{"offset":1}},"metaSeen":{"a.meta.json":"deadbeef"}}`,
		`{"version":99}`,
		`{"files":{"x":{}}}`,
		"not json",
		`{"version":1,"files":null,"metaSeen":null}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseCursor(s)
	})
}
