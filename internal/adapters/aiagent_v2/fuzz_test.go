package aiagent_v2

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzParseSnapshot exercises the v2 envelope parser against
// arbitrary bytes. Contract under fuzz: parseSnapshot never panics on
// any input; it returns either a valid snapshot or a wrapped error.
// Corpus seeds cover both versions, embedded sub-agents, malformed
// JSON, missing required fields, and unknown-version envelopes.
func FuzzParseSnapshot(f *testing.F) {
	good := simpleSnapshot(2, "fuzz-1")
	body, _ := json.Marshal(good)
	f.Add(body)

	v1 := snapshot{Version: 1, Reason: "final", OpTree: opTree{TraceID: "v1-fuzz"}}
	body, _ = json.Marshal(v1)
	f.Add(body)

	// Embedded sub-agent fixture.
	embed := simpleSnapshot(2, "parent")
	child := opTree{TraceID: "child", StartedAt: 1700000000000}
	embed.OpTree.Turns[0].Ops = append(embed.OpTree.Turns[0].Ops, operationNode{
		OpID: "x", Kind: "session", ChildSession: &child,
	})
	body, _ = json.Marshal(embed)
	f.Add(body)

	// Various adversarial inputs.
	f.Add([]byte(""))
	f.Add([]byte("not json"))
	f.Add([]byte(`{"version":2}`))
	f.Add([]byte(`{"version":2,"opTree":{}}`))
	f.Add([]byte(`{"version":2,"opTree":{"traceId":"x","turns":null}}`))
	f.Add([]byte(`{"version":99,"opTree":{"traceId":"y"}}`))
	f.Add([]byte(`{"version":2,"opTree":{"traceId":"deep","turns":[{"index":1,"ops":[{"opId":"o","kind":"session","childSession":{"traceId":"c","turns":[]}}]}]}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseSnapshot(data)
		// Also exercise the streaming path so a panic in either
		// surfaces.
		_, _ = parseSnapshotStream(bytes.NewReader(data))
	})
}

// FuzzParseCursor exercises ParseCursor against adversarial cursor
// JSON blobs.
func FuzzParseCursor(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		`{"version":1}`,
		`{"version":1,"files":{}}`,
		`{"version":1,"files":{"a.json.gz":{"content_hash":"abc","last_mtime_ns":1,"last_size":2}}}`,
		`{"version":99}`,
		`{"files":null}`,
		"not json",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseCursor(s)
	})
}
