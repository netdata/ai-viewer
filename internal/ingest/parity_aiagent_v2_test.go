package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestAIAgentV2IngestArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeAIAgentV2ParityFixture(t)
	sourceID := "aiagent_v2:" + root

	adapter, err := aiagent_v2.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v2 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v2.New: %v", err)
	}

	events := make(chan canonical.Event, 256)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v2 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v2", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV2EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV2ParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV2ParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 24 {
		t.Fatalf("source artifact count = %d, want 24", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 24 {
		t.Fatalf("canonical artifact count = %d, want 24", len(canonicalArtifacts))
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSessionMetadata); got != 2 {
		t.Fatalf("source session_metadata count = %d, want 2", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSessionMetadata); got != 2 {
		t.Fatalf("canonical session_metadata count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassAssistantMessage); got != 1 {
		t.Fatalf("source assistant_message count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassAssistantMessage); got != 1 {
		t.Fatalf("canonical assistant_message count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassReasoningText); got != 1 {
		t.Fatalf("source reasoning_text count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassReasoningText); got != 1 {
		t.Fatalf("canonical reasoning_text count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("source subagent_link count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMError); got != 1 {
		t.Fatalf("source llm_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassLLMError); got != 1 {
		t.Fatalf("canonical llm_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolResponse); got != 2 {
		t.Fatalf("source tool_response count = %d, want 2", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassToolResponse); got != 2 {
		t.Fatalf("canonical tool_response count = %d, want 2", got)
	}
	if got := countArtifactsByAvailability(sourceArtifacts, parity.AvailabilitySourceUnavailable); got != 1 {
		t.Fatalf("source_unavailable source artifact count = %d, want 1", got)
	}
	if got := countArtifactsByAvailability(canonicalArtifacts, parity.AvailabilitySourceUnavailable); got != 1 {
		t.Fatalf("source_unavailable canonical artifact count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v2 parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV2IngestInlinePayloadArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeAIAgentV2InlineParityFixture(t)
	sourceID := "aiagent_v2:" + root

	adapter, err := aiagent_v2.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v2 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v2.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v2 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v2", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV2EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV2ParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV2ParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6", len(canonicalArtifacts))
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSessionMetadata); got != 1 {
		t.Fatalf("source session_metadata count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSessionMetadata); got != 1 {
		t.Fatalf("canonical session_metadata count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMRequest); got != 1 {
		t.Fatalf("source llm_request count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMResponse); got != 1 {
		t.Fatalf("source llm_response count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassLLMRequest); got != 1 {
		t.Fatalf("canonical llm_request count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassLLMResponse); got != 1 {
		t.Fatalf("canonical llm_response count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v2 inline payload parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV2IngestLogArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeAIAgentV2LogParityFixture(t)
	sourceID := "aiagent_v2:" + root

	adapter, err := aiagent_v2.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v2 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v2.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v2 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v2", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV2EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV2LogArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV2LogArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source log_entry count = %d, want 4", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical log_entry count = %d, want 4", len(canonicalArtifacts))
	}
	if got := countArtifactsByAvailability(sourceArtifacts, parity.AvailabilitySourceEmpty); got != 1 {
		t.Fatalf("source_empty source log count = %d, want 1", got)
	}
	if got := countArtifactsByAvailability(canonicalArtifacts, parity.AvailabilitySourceEmpty); got != 1 {
		t.Fatalf("source_empty canonical log count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v2 log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV2IngestSystemOpArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeAIAgentV2SystemOpParityFixture(t)
	sourceID := "aiagent_v2:" + root

	adapter, err := aiagent_v2.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v2 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v2.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v2 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v2", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV2EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV2SystemOpArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV2SystemOpArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source system_op count = %d, want 1", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical system_op count = %d, want 1", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v2 system_op parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV2IngestCompactionArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeAIAgentV2CompactionParityFixture(t)
	sourceID := "aiagent_v2:" + root

	adapter, err := aiagent_v2.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v2 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v2.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v2 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v2", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV2EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV2CompactionArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV2CompactionArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source compaction_event count = %d, want 1", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical compaction_event count = %d, want 1", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v2 compaction_event parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

type aiAgentV2CapturedPayload struct {
	rel             string
	raw             []byte
	hash            string
	compressedBytes int64
}

func writeAIAgentV2ParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	payloads := []aiAgentV2CapturedPayload{
		{rel: "payloads/root-session/turn-0001/llm-request.json.gz", raw: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)},
		{rel: "payloads/root-session/turn-0001/sdk-request.json.gz", raw: []byte(`{"sdk":{"input":"hi"}}`)},
		{rel: "payloads/root-session/turn-0001/llm-response.json.gz", raw: []byte(`{"content":[{"type":"text","text":"hello"}]}`)},
		{rel: "payloads/root-session/turn-0001/sdk-response.json.gz", raw: []byte(`{"sdk":{"output":"hello"}}`)},
		{rel: "payloads/root-session/turn-0001/tool-request.json.gz", raw: []byte(`{"path":"README.md"}`)},
		{rel: "payloads/root-session/turn-0001/tool-request-2.json.gz", raw: []byte(`{"path":"main.go"}`)},
		{rel: "payloads/root-session/turn-0001/tool-response.json.gz", raw: []byte(`{"ok":true}`)},
	}
	for i := range payloads {
		payloads[i].hash, payloads[i].compressedBytes = writeAIAgentV2Payload(t, root, payloads[i])
	}
	toolResponseSHA := sha256HexForAIAgentV2Test([]byte("tool output unavailable"))

	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":           "root-node",
			"traceId":      "root-session",
			"agentId":      "root-agent",
			"callPath":     "root-agent",
			"sessionTitle": "synthetic parity fixture",
			"latestStatus": "metadata status",
			"startedAt":    int64(1_700_000_000_000),
			"endedAt":      int64(1_700_000_010_000),
			"success":      true,
			"attributes": map[string]any{
				"priority": 2,
				"purpose":  "root-metadata",
			},
			"totals": map[string]any{
				"tokensIn":  11,
				"tokensOut": 7,
				"toolsRun":  1,
				"agentsRun": 2,
			},
			"finalReport": map[string]any{
				"format":   "json",
				"captured": true,
				"summary":  "All checks passed.",
			},
			"pluginMetas": map[string]any{
				"support": map[string]any{
					"ticketId": 42,
					"ok":       true,
				},
			},
			"turns": []any{
				map[string]any{
					"id":        "turn-node",
					"index":     1,
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_008_000),
					"ops": []any{
						aiAgentV2ParityLLMOp(payloads[0], payloads[1], payloads[2], payloads[3]),
						aiAgentV2ParityToolOp(payloads[4], toolResponseSHA),
						aiAgentV2ParityCapturedToolOp(payloads[5], payloads[6]),
						aiAgentV2ParityFailedLLMOp(),
						aiAgentV2ParitySessionOp(),
					},
				},
			},
		},
	}
	writeAIAgentV2Snapshot(t, root, snapshot)
	return root
}

func writeAIAgentV2InlineParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	requestRaw := json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`)
	responseText := "assistant text"
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "inline-node",
			"traceId":   "inline-session",
			"agentId":   "root-agent",
			"callPath":  "root-agent",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_003_000),
			"success":   true,
			"turns": []any{
				map[string]any{
					"id":        "inline-turn",
					"index":     1,
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_002_000),
					"ops": []any{
						map[string]any{
							"opId":      "inline-llm",
							"kind":      "llm",
							"startedAt": int64(1_700_000_001_100),
							"endedAt":   int64(1_700_000_001_900),
							"status":    "ok",
							"attributes": map[string]any{
								"name":     "message",
								"provider": "anthropic",
								"model":    "claude",
							},
							"request": map[string]any{
								"kind":    "llm",
								"payload": requestRaw,
								"size":    len(requestRaw),
							},
							"response": map[string]any{
								"payload": responseText,
								"size":    len(responseText),
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2Snapshot(t, root, snapshot)
	return root
}

func writeAIAgentV2LogParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "root-node",
			"traceId":   "root-session",
			"agentId":   "root-agent",
			"callPath":  "root-agent",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_004_000),
			"success":   false,
			"error":     "session failed",
			"turns": []any{
				map[string]any{
					"id":        "turn-node",
					"index":     1,
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_003_000),
					"ops": []any{
						map[string]any{
							"opId":      "op-log",
							"kind":      "tool",
							"startedAt": int64(1_700_000_001_100),
							"endedAt":   int64(1_700_000_001_900),
							"status":    "ok",
							"logs": []any{
								map[string]any{
									"timestamp": int64(1_700_000_001_200),
									"severity":  "warn",
									"message":   "opened README",
									"path":      "README.md",
								},
								map[string]any{
									"timestamp": int64(1_700_000_001_300),
									"severity":  "info",
									"message":   "",
								},
							},
						},
						map[string]any{
							"opId":      "op-failed",
							"kind":      "tool",
							"startedAt": int64(1_700_000_002_000),
							"endedAt":   int64(1_700_000_003_000),
							"status":    "failed",
							"attributes": map[string]any{
								"error": "permission_denied",
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2Snapshot(t, root, snapshot)
	return root
}

func writeAIAgentV2SystemOpParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "system-node",
			"traceId":   "system-session",
			"agentId":   "root-agent",
			"callPath":  "root-agent",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_002_000),
			"success":   true,
			"turns": []any{
				map[string]any{
					"id":        "turn-0",
					"index":     0,
					"startedAt": int64(1_700_000_000_100),
					"endedAt":   int64(1_700_000_001_000),
					"ops": []any{
						map[string]any{
							"opId":      "init-op",
							"kind":      "system",
							"startedAt": int64(1_700_000_000_200),
							"endedAt":   int64(1_700_000_000_500),
							"status":    "ok",
							"attributes": map[string]any{
								"name": "init",
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2Snapshot(t, root, snapshot)
	return root
}

func writeAIAgentV2CompactionParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "compact-node",
			"traceId":   "compact-session",
			"agentId":   "root-agent",
			"callPath":  "root-agent",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_004_000),
			"success":   true,
			"steps": []any{
				map[string]any{
					"id":        "compact-step",
					"index":     0,
					"kind":      "internal",
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_003_000),
					"attributes": map[string]any{
						"archivedTurn": 1,
						"currentTurn":  2,
						"label":        "history-compaction",
					},
					"ops": []any{
						map[string]any{
							"opId":      "compact-op",
							"kind":      "session",
							"startedAt": int64(1_700_000_001_500),
							"endedAt":   int64(1_700_000_002_500),
							"status":    "ok",
							"attributes": map[string]any{
								"name":     "history_compaction.turn_summarizer",
								"provider": "history-compaction",
							},
							"childSessionRef": map[string]any{
								"sessionId": "compact-child",
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2Snapshot(t, root, snapshot)
	return root
}

func aiAgentV2ParityLLMOp(req, sdkReq, resp, sdkResp aiAgentV2CapturedPayload) map[string]any {
	return map[string]any{
		"opId":      "op-llm",
		"kind":      "llm",
		"startedAt": int64(1_700_000_002_000),
		"endedAt":   int64(1_700_000_003_000),
		"status":    "ok",
		"attributes": map[string]any{
			"name":     "message",
			"provider": "anthropic",
			"model":    "claude",
		},
		"request": map[string]any{
			"kind":    "llm",
			"size":    len(req.raw),
			"payload": aiAgentV2ParityDualRef(req, sdkReq),
		},
		"response": map[string]any{
			"size":    len(resp.raw),
			"payload": aiAgentV2ParityDualRef(resp, sdkResp),
		},
		"reasoning": map[string]any{
			"final": "root reasoning summary",
		},
	}
}

func aiAgentV2ParityToolOp(req aiAgentV2CapturedPayload, responseSHA string) map[string]any {
	return map[string]any{
		"opId":      "op-tool",
		"kind":      "tool",
		"startedAt": int64(1_700_000_003_000),
		"endedAt":   int64(1_700_000_004_000),
		"status":    "failed",
		"attributes": map[string]any{
			"name":     "read_file",
			"provider": "filesystem",
			"error":    "permission_denied",
		},
		"request": map[string]any{
			"kind":    "tool",
			"size":    len(req.raw),
			"payload": map[string]any{"ref": aiAgentV2ParityRef(req, true)},
		},
		"response": map[string]any{
			"size": 23,
			"payload": map[string]any{
				"ref": map[string]any{
					"format":        "text",
					"originalBytes": 23,
					"sha256":        responseSHA,
					"captured":      false,
				},
			},
		},
	}
}

func aiAgentV2ParityCapturedToolOp(req, resp aiAgentV2CapturedPayload) map[string]any {
	return map[string]any{
		"opId":      "op-tool-captured",
		"kind":      "tool",
		"startedAt": int64(1_700_000_004_000),
		"endedAt":   int64(1_700_000_005_000),
		"status":    "ok",
		"attributes": map[string]any{
			"name":     "read_file",
			"provider": "filesystem",
		},
		"request": map[string]any{
			"kind":    "tool",
			"size":    len(req.raw),
			"payload": map[string]any{"ref": aiAgentV2ParityRef(req, true)},
		},
		"response": map[string]any{
			"size":    len(resp.raw),
			"payload": map[string]any{"ref": aiAgentV2ParityRef(resp, true)},
		},
	}
}

func aiAgentV2ParityFailedLLMOp() map[string]any {
	return map[string]any{
		"opId":      "op-llm-failed",
		"kind":      "llm",
		"startedAt": int64(1_700_000_005_000),
		"endedAt":   int64(1_700_000_006_000),
		"status":    "failed",
		"attributes": map[string]any{
			"name":     "message",
			"provider": "anthropic",
			"model":    "claude",
			"error":    "provider_error",
		},
	}
}

func aiAgentV2ParitySessionOp() map[string]any {
	return map[string]any{
		"opId":      "op-session",
		"kind":      "session",
		"startedAt": int64(1_700_000_004_000),
		"endedAt":   int64(1_700_000_006_000),
		"status":    "ok",
		"attributes": map[string]any{
			"name":     "delegate",
			"provider": "subagent",
		},
		"childSession": map[string]any{
			"id":           "child-node",
			"traceId":      "child-session",
			"agentId":      "child-agent",
			"callPath":     "root-agent->child-agent",
			"sessionTitle": "child title",
			"latestStatus": "child status",
			"startedAt":    int64(1_700_000_004_000),
			"endedAt":      int64(1_700_000_006_000),
			"success":      true,
			"attributes": map[string]any{
				"child": true,
			},
			"totals": map[string]any{
				"tokensIn":  1,
				"tokensOut": 2,
				"toolsRun":  0,
				"agentsRun": 1,
			},
			"turns": []any{},
		},
	}
}

func aiAgentV2ParityDualRef(regular, sdk aiAgentV2CapturedPayload) map[string]any {
	return map[string]any{
		"ref": aiAgentV2ParityRef(regular, true),
		"sdk": map[string]any{
			"ref": aiAgentV2ParityRef(sdk, true),
		},
	}
}

func aiAgentV2ParityRef(payload aiAgentV2CapturedPayload, captured bool) map[string]any {
	return map[string]any{
		"path":            payload.rel,
		"format":          "json",
		"compression":     "gzip",
		"originalBytes":   len(payload.raw),
		"compressedBytes": payload.compressedBytes,
		"sha256":          payload.hash,
		"captured":        captured,
	}
}

func writeAIAgentV2Payload(t *testing.T, root string, payload aiAgentV2CapturedPayload) (string, int64) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(payload.rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir payload: %v", err)
	}
	if err := os.WriteFile(path, gzipBytesForAIAgentV2Test(t, payload.raw), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	return sha256HexForAIAgentV2Test(payload.raw), info.Size()
}

func writeAIAgentV2Snapshot(t *testing.T, root string, snapshot interface{}) {
	t.Helper()

	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(root, "root-session.json.gz")
	if err := os.WriteFile(path, gzipBytesForAIAgentV2Test(t, body), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func gzipBytesForAIAgentV2Test(t *testing.T, raw []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func sha256HexForAIAgentV2Test(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}

func applyAIAgentV2EventsForParity(t *testing.T, ctx context.Context, db *sql.DB, sourceID string, location string, events <-chan canonical.Event) {
	t.Helper()

	writer := newWriter(sourceID, "aiagent_v2", location, NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for event := range events {
		if err := writer.apply(ctx, tx, event); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %T: %v", event, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	resolver := newResolver(db, silentLogger(), time.Minute)
	if err := resolver.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}
}

func filterAIAgentV2ParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassSessionBoundary:  {},
		parity.ClassTurnBoundary:     {},
		parity.ClassOpBoundary:       {},
		parity.ClassSessionMetadata:  {},
		parity.ClassAssistantMessage: {},
		parity.ClassReasoningText:    {},
		parity.ClassSubagentLink:     {},
		parity.ClassToolError:        {},
		parity.ClassLLMError:         {},
		parity.ClassLLMRequest:       {},
		parity.ClassLLMResponse:      {},
		parity.ClassLLMSDKRequest:    {},
		parity.ClassLLMSDKResponse:   {},
		parity.ClassToolRequest:      {},
		parity.ClassToolResponse:     {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV2LogArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassLogEntry {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV2SystemOpArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassSystemOp {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV2CompactionArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassCompactionEvent {
			out = append(out, artifact)
		}
	}
	return out
}
