package aiagent_v2

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// drainBuffered collects all events currently available on ch in a
// single non-blocking round. Test-only helper used by scanner and
// tailer tests.
func drainBuffered(ch chan canonical.Event) []canonical.Event {
	out := make([]canonical.Event, 0, cap(ch))
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// mkGzip gzips the provided bytes and returns the compressed blob.
// Used by tests to materialise a snapshot fixture on disk without a
// real producer.
func mkGzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// writeSnapshot writes a v2 snapshot envelope to <root>/<originId>.json.gz.
// Returns the absolute file path. Encodes the envelope via json.Marshal
// so callers can construct a snapshot literal in Go.
func writeSnapshot(t *testing.T, root, originID string, snap snapshot) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(root, originID+".json.gz")
	if err := os.WriteFile(path, mkGzip(t, body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// writeRaw writes pre-gzipped bytes verbatim to <root>/<name>.
// Useful for negative tests (corrupt gzip header, truncated body).
func writeRaw(t *testing.T, root, name string, gz []byte) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, gz, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// boolPtr returns a pointer to b for nullable JSON fields.
func boolPtr(b bool) *bool { return &b }

// int64Ptr returns a pointer to v for nullable JSON fields.
func int64Ptr(v int64) *int64 { return &v }

// simpleSnapshot builds a minimal-but-valid v2 envelope at the
// requested version. originID also serves as the opTree.traceId for
// root sessions (matching v2 producer semantics).
func simpleSnapshot(version int, originID string) snapshot {
	return snapshot{
		Version: version,
		Reason:  "final",
		OpTree: opTree{
			ID:           "session-builder-id",
			TraceID:      originID,
			AgentID:      "test-agent",
			SessionTitle: "test session",
			StartedAt:    1700000000000,
			EndedAt:      int64Ptr(1700000010000),
			Success:      boolPtr(true),
			Turns: []turnNode{
				{
					ID:        "turn-1",
					Index:     1,
					StartedAt: 1700000001000,
					EndedAt:   int64Ptr(1700000005000),
					Ops: []operationNode{
						{
							OpID:      "op-llm-1",
							Kind:      "llm",
							StartedAt: 1700000001500,
							EndedAt:   int64Ptr(1700000004500),
							Status:    "ok",
							Attributes: rawAttrs(map[string]any{
								"provider": "anthropic",
								"model":    "claude-3-5-sonnet",
								"name":     "claude-3-5-sonnet",
							}),
							Accounting: []accountingEntry{
								{
									Type:     "llm",
									Provider: "anthropic",
									Model:    "claude-3-5-sonnet",
									CostUSD:  0.0123,
									Tokens: &tokens{
										InputTokens:          100,
										OutputTokens:         50,
										CacheReadInputTokens: 10,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// rawAttrs converts a typed attribute map into the json.RawMessage
// shape opTree fields require. Test-only.
func rawAttrs(in map[string]any) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}
