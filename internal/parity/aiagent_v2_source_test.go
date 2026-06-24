package parity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExtractAIAgentV2SourceStructuralAndPayloadArtifacts(t *testing.T) {
	t.Parallel()

	root, snapshotPath, payloads := writeAIAgentV2ParityFixture(t)
	sourceID := "aiagent_v2:" + root

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:root-session"), sessionBoundaryIdentity{
		NativeSessionID:     "root-session",
		RootNativeSessionID: "root-session",
		Kind:                "root",
		Status:              "completed",
		StartedAt:           1_700_000_000_000_000,
		EndedAt:             ptrInt64(1_700_000_010_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:root-session:metadata"), aiAgentV2SessionMetadataIdentity{
		NativeSessionID:   "root-session",
		OriginID:          "root-session",
		Version:           2,
		NodeID:            "root-node",
		AgentID:           "root-agent",
		CallPath:          "root-agent",
		SessionTitle:      "synthetic parity fixture",
		LatestStatus:      "metadata status",
		AttributesSHA256:  aiAgentV2CanonicalJSONHashForTest(t, map[string]any{"priority": 2, "purpose": "root-metadata"}),
		TotalsSHA256:      aiAgentV2CanonicalJSONHashForTest(t, map[string]any{"agentsRun": 2, "tokensIn": 11, "tokensOut": 7, "toolsRun": 1}),
		PluginMetasSHA256: aiAgentV2CanonicalJSONHashForTest(t, map[string]any{"support": map[string]any{"ok": true, "ticketId": 42}}),
	})
	assertAIAgentV2FinalReportArtifact(t, findArtifact(t, artifacts, ClassAssistantMessage, "session:root-session:final_report"), snapshotPath)
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:child-session"), sessionBoundaryIdentity{
		NativeSessionID:       "child-session",
		ParentNativeSessionID: "root-session",
		RootNativeSessionID:   "root-session",
		Kind:                  "sub_agent",
		Status:                "completed",
		StartedAt:             1_700_000_004_000_000,
		EndedAt:               ptrInt64(1_700_000_006_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:child-session:metadata"), aiAgentV2SessionMetadataIdentity{
		NativeSessionID:  "child-session",
		OriginID:         "root-session",
		Version:          0,
		NodeID:           "child-node",
		AgentID:          "child-agent",
		CallPath:         "root-agent->child-agent",
		SessionTitle:     "child title",
		LatestStatus:     "child status",
		AttributesSHA256: aiAgentV2CanonicalJSONHashForTest(t, map[string]any{"child": true}),
		TotalsSHA256:     aiAgentV2CanonicalJSONHashForTest(t, map[string]any{"agentsRun": 1, "tokensIn": 1, "tokensOut": 2, "toolsRun": 0}),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		Status:          "failed",
		StartedAt:       1_700_000_001_000_000,
		EndedAt:         ptrInt64(1_700_000_008_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:0"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           0,
		Kind:            "llm",
		Name:            "message",
		Status:          "completed",
		StartedAt:       1_700_000_002_000_000,
		EndedAt:         ptrInt64(1_700_000_003_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "tool",
		Name:            "read_file",
		ToolNamespace:   "filesystem",
		Status:          "failed",
		StartedAt:       1_700_000_003_000_000,
		EndedAt:         ptrInt64(1_700_000_004_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "session",
		Name:            "delegate",
		Status:          "completed",
		StartedAt:       1_700_000_004_000_000,
		EndedAt:         ptrInt64(1_700_000_006_000_000),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "root-session",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "reasoning",
		Status:          "completed",
		StartedAt:       1_700_000_002_000_000,
		EndedAt:         ptrInt64(1_700_000_003_000_000),
	})
	assertAIAgentV2ReasoningFinalArtifact(t, findArtifact(t, artifacts, ClassReasoningText, "op:1:3:reasoning.final"), snapshotPath)
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSubagentLink, "op:1:2:child_session:child-session"), subagentLinkIdentity{
		ParentNativeSessionID: "root-session",
		ParentTurnSeq:         1,
		ParentOpSeq:           2,
		ChildNativeSessionID:  "child-session",
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:1:error"), opErrorIdentity{
		NativeSessionID:    "root-session",
		TurnSeq:            1,
		OpSeq:              1,
		OpKind:             "tool",
		ErrorClass:         "permission_denied",
		ErrorMessageSHA256: stringSHA256(""),
	})

	for _, payload := range payloads.captured {
		assertAIAgentV2CapturedPayloadArtifact(t, artifacts, payload)
	}
	uncaptured := findArtifact(t, artifacts, ClassToolResponse, "op:1:1:payload:tool_response:1")
	if uncaptured.SourceFile != snapshotPath {
		t.Fatalf("uncaptured source_file = %q, want %q", uncaptured.SourceFile, snapshotPath)
	}
	if uncaptured.Availability != AvailabilitySourceUnavailable {
		t.Fatalf("uncaptured availability = %q, want %q", uncaptured.Availability, AvailabilitySourceUnavailable)
	}
	if uncaptured.ProducerSHA256 != payloads.uncapturedToolResponseSHA {
		t.Fatalf("uncaptured producer sha = %q, want %q", uncaptured.ProducerSHA256, payloads.uncapturedToolResponseSHA)
	}
}

func TestExtractAIAgentV2SourceLLMErrorArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"traceId":   "root-session",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_003_000),
			"success":   false,
			"turns": []any{
				map[string]any{
					"index":     1,
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_002_000),
					"ops": []any{
						map[string]any{
							"opId":      "op-llm-failed",
							"kind":      "llm",
							"startedAt": int64(1_700_000_001_100),
							"endedAt":   int64(1_700_000_001_900),
							"status":    "failed",
							"attributes": map[string]any{
								"name":  "message",
								"error": "provider_error",
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2GzipJSON(t, root, "root-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassLLMError, "op:1:0:error"), opErrorIdentity{
		NativeSessionID:    "root-session",
		TurnSeq:            1,
		OpSeq:              0,
		OpKind:             "llm",
		ErrorClass:         "provider_error",
		ErrorMessageSHA256: stringSHA256(""),
	})
}

func TestExtractAIAgentV2SourceCorruptSnapshotEmitsSourceCorruptionAndContinues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		badFile        string
		writeBad       func(t *testing.T, path string)
		expectedActual string
	}{
		{
			name:    "zero_byte",
			badFile: "bad-zero.json.gz",
			writeBad: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatalf("write zero-byte snapshot: %v", err)
				}
			},
			expectedActual: "zero_bytes",
		},
		{
			name:    "invalid_gzip",
			badFile: "bad-gzip.json.gz",
			writeBad: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not gzip"), 0o600); err != nil {
					t.Fatalf("write invalid gzip snapshot: %v", err)
				}
			},
			expectedActual: "gzip_error",
		},
		{
			name:    "malformed_json",
			badFile: "bad-json.json.gz",
			writeBad: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, gzipBytes(t, []byte(`{"version":`)), 0o600); err != nil {
					t.Fatalf("write malformed JSON snapshot: %v", err)
				}
			},
			expectedActual: "decode_error",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			badPath := filepath.Join(root, tt.badFile)
			tt.writeBad(t, badPath)
			writeAIAgentV2GzipJSON(t, root, "good-session.json.gz", map[string]any{
				"version": 2,
				"reason":  "final",
				"opTree": map[string]any{
					"traceId":   "good-session",
					"startedAt": int64(1_700_000_000_000),
				},
			})

			artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
				Root:     root,
				SourceID: "aiagent_v2:" + root,
			})
			if err != nil {
				t.Fatalf("ExtractAIAgentV2Source: %v", err)
			}

			corrupt := findArtifact(t, artifacts, ClassSourceCorruption, "source_corruption:file:"+tt.badFile+":snapshot")
			if corrupt.NativeSessionID != strings.TrimSuffix(tt.badFile, aiAgentV2SnapshotExt) {
				t.Fatalf("native_session_id = %q, want %q", corrupt.NativeSessionID, strings.TrimSuffix(tt.badFile, aiAgentV2SnapshotExt))
			}
			if corrupt.SourceFile != badPath {
				t.Fatalf("source_file = %q, want %q", corrupt.SourceFile, badPath)
			}
			if corrupt.Availability != AvailabilitySourceCorrupt {
				t.Fatalf("availability = %q, want %q", corrupt.Availability, AvailabilitySourceCorrupt)
			}
			if corrupt.HashDomain != HashRawBytes {
				t.Fatalf("hash_domain = %q, want %q", corrupt.HashDomain, HashRawBytes)
			}
			assertIntegrityFailures(t, corrupt, []IntegrityFailure{{
				Field:    "snapshot",
				Expected: "valid aiagent_v2 gzip JSON snapshot",
				Actual:   tt.expectedActual,
			}})
			assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:good-session"), sessionBoundaryIdentity{
				NativeSessionID:     "good-session",
				RootNativeSessionID: "good-session",
				Kind:                "root",
				Status:              "abandoned",
				StartedAt:           1_700_000_000_000_000,
			})
		})
	}
}

func TestExtractAIAgentV2SourcePayloadByteCountMismatchIsSourceCorrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	request := aiAgentV2CapturedPayloadFixture{
		class: ClassLLMRequest,
		rel:   "payloads/root-session/turn-0001/llm-request.json.gz",
		raw:   []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	response := aiAgentV2CapturedPayloadFixture{
		class: ClassLLMResponse,
		rel:   "payloads/root-session/turn-0001/llm-response.json.gz",
		raw:   []byte(`{"content":[{"type":"text","text":"hello"}]}`),
	}
	request.path, request.hash = writeAIAgentV2GzipPayload(t, root, request.rel, request.raw)
	response.path, response.hash = writeAIAgentV2GzipPayload(t, root, response.rel, response.raw)

	requestRef := aiAgentV2RefObject(request, true)
	requestRef["originalBytes"] = len(request.raw) + 7
	responseRef := aiAgentV2RefObject(response, true)
	responseRef["compressedBytes"] = compressedAIAgentV2PayloadSize(response.path) + 7

	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"traceId":   "root-session",
			"startedAt": int64(1_700_000_000_000),
			"turns": []any{
				map[string]any{
					"index":     1,
					"startedAt": int64(1_700_000_001_000),
					"endedAt":   int64(1_700_000_008_000),
					"ops": []any{
						map[string]any{
							"opId":      "op-llm",
							"kind":      "llm",
							"startedAt": int64(1_700_000_002_000),
							"endedAt":   int64(1_700_000_003_000),
							"status":    "ok",
							"attributes": map[string]any{
								"name":     "message",
								"provider": "anthropic",
							},
							"request": map[string]any{
								"kind":    "llm",
								"payload": map[string]any{"ref": requestRef},
							},
							"response": map[string]any{
								"payload": map[string]any{"ref": responseRef},
							},
						},
					},
				},
			},
		},
	}
	writeAIAgentV2GzipJSON(t, root, "root-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	requestArtifact := findArtifact(t, artifacts, ClassLLMRequest, "file:"+request.rel)
	if requestArtifact.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("request availability = %q, want %q", requestArtifact.Availability, AvailabilitySourceCorrupt)
	}
	if requestArtifact.Bytes != int64(len(request.raw)) || requestArtifact.ComputedSHA256 != request.hash || requestArtifact.ProducerSHA256 != request.hash {
		t.Fatalf("request proof mismatch: %+v", requestArtifact)
	}
	assertIntegrityFailures(t, requestArtifact, []IntegrityFailure{{
		Field:    "original_bytes",
		Expected: fmt.Sprintf("%d", len(request.raw)+7),
		Actual:   fmt.Sprintf("%d", len(request.raw)),
	}})
	responseArtifact := findArtifact(t, artifacts, ClassLLMResponse, "file:"+response.rel)
	if responseArtifact.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("response availability = %q, want %q", responseArtifact.Availability, AvailabilitySourceCorrupt)
	}
	if responseArtifact.Bytes != int64(len(response.raw)) || responseArtifact.ComputedSHA256 != response.hash || responseArtifact.ProducerSHA256 != response.hash {
		t.Fatalf("response proof mismatch: %+v", responseArtifact)
	}
	assertIntegrityFailures(t, responseArtifact, []IntegrityFailure{{
		Field:    "compressed_bytes",
		Expected: fmt.Sprintf("%d", compressedAIAgentV2PayloadSize(response.path)+7),
		Actual:   fmt.Sprintf("%d", compressedAIAgentV2PayloadSize(response.path)),
	}})
}

func TestExtractAIAgentV2SourceToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	root, _, _ := writeAIAgentV2ParityFixture(t)
	opts := AIAgentV2SourceOptions{Root: root, SourceID: "aiagent_v2:" + root}
	want, err := ExtractAIAgentV2Source(context.Background(), opts)
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	var got []Artifact
	err = ExtractAIAgentV2SourceToWriter(context.Background(), opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractAIAgentV2SourceToWriter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed aiagent_v2 artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractAIAgentV2SourceSystemOpArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "system-node",
			"traceId":   "system-session",
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
	snapshotPath := writeAIAgentV2GzipJSON(t, root, "system-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:0:0"), opBoundaryIdentity{
		NativeSessionID: "system-session",
		TurnSeq:         0,
		OpSeq:           0,
		Kind:            "system",
		Name:            "init",
		Status:          "completed",
		StartedAt:       1_700_000_000_200_000,
		EndedAt:         ptrInt64(1_700_000_000_500_000),
	})
	systemOp := findArtifact(t, artifacts, ClassSystemOp, "op:0:0:system")
	if systemOp.SourceFile != snapshotPath {
		t.Fatalf("system_op source_file = %q, want %q", systemOp.SourceFile, snapshotPath)
	}
	if systemOp.NativeTurnID != "turn:0" {
		t.Fatalf("system_op native_turn_id = %q, want turn:0", systemOp.NativeTurnID)
	}
	assertIdentityArtifact(t, systemOp, systemOpIdentity{
		NativeSessionID: "system-session",
		TurnSeq:         0,
		OpSeq:           0,
		OpKind:          "system",
		Name:            "init",
		Status:          "completed",
		StartedAt:       1_700_000_000_200_000,
		EndedAt:         ptrInt64(1_700_000_000_500_000),
		OriginalKind:    "system",
	})
}

func TestExtractAIAgentV2SourceCompactionEventArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "compact-node",
			"traceId":   "compact-session",
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
	snapshotPath := writeAIAgentV2GzipJSON(t, root, "compact-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	compaction := findArtifact(t, artifacts, ClassCompactionEvent, "op:10000:0:compaction")
	if compaction.SourceFile != snapshotPath {
		t.Fatalf("compaction source_file = %q, want %q", compaction.SourceFile, snapshotPath)
	}
	if compaction.NativeTurnID != "turn:10000" {
		t.Fatalf("compaction native_turn_id = %q, want turn:10000", compaction.NativeTurnID)
	}
	assertIdentityArtifact(t, compaction, struct {
		NativeSessionID      string `json:"native_session_id"`
		TurnSeq              int64  `json:"turn_seq"`
		OpSeq                int64  `json:"op_seq"`
		Trigger              string `json:"trigger"`
		StepKind             string `json:"step_kind"`
		Name                 string `json:"name,omitempty"`
		Provider             string `json:"provider,omitempty"`
		ChildNativeSessionID string `json:"child_native_session_id,omitempty"`
		ArchivedTurn         int64  `json:"archived_turn,omitempty"`
		CurrentTurn          int64  `json:"current_turn,omitempty"`
		Status               string `json:"status"`
		StartedAt            int64  `json:"started_at"`
		EndedAt              *int64 `json:"ended_at,omitempty"`
	}{
		NativeSessionID:      "compact-session",
		TurnSeq:              10000,
		OpSeq:                0,
		Trigger:              "history_compaction",
		StepKind:             "internal",
		Name:                 "history_compaction.turn_summarizer",
		Provider:             "history-compaction",
		ChildNativeSessionID: "compact-child",
		ArchivedTurn:         1,
		CurrentTurn:          2,
		Status:               "completed",
		StartedAt:            1_700_000_001_500_000,
		EndedAt:              ptrInt64(1_700_000_002_500_000),
	})
}

func TestExtractAIAgentV2SourceInlinePayloadArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	requestRaw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	responseText := "assistant text"
	snapshot := map[string]any{
		"version": 2,
		"opTree": map[string]any{
			"traceId":   "inline-session",
			"startedAt": int64(1_700_000_000_000),
			"turns": []any{
				map[string]any{
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
								"name": "message",
							},
							"request": map[string]any{
								"kind":    "llm",
								"payload": json.RawMessage(requestRaw),
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
	snapshotPath := writeAIAgentV2GzipJSON(t, root, "inline-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	requestPointer := "/opTree/turns/0/ops/0/request/payload"
	responsePointer := "/opTree/turns/0/ops/0/response/payload"
	assertAIAgentV2InlineJSONPayloadArtifact(t, findArtifact(t, artifacts, ClassLLMRequest, "file:inline-session.json.gz:"+requestPointer), snapshotPath, requestPointer, requestRaw)
	assertAIAgentV2InlineTextPayloadArtifact(t, findArtifact(t, artifacts, ClassLLMResponse, "file:inline-session.json.gz:"+responsePointer), snapshotPath, responsePointer, responseText)
}

func TestExtractAIAgentV2PayloadRefsAcceptsLegacyAndWrappedForms(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"ref":"payloads/legacy-request.json.gz",
		"format":"json",
		"compression":"gzip",
		"originalBytes":11,
		"compressedBytes":7,
		"sha256":"legacy-sha",
		"captured":true,
		"sdk":{"ref":{"path":"payloads/sdk-request.json","format":"json","storedBytes":5,"captured":false,"sha256":"sdk-sha"}}
	}`)

	refs := extractAIAgentV2PayloadRefs(raw)
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want legacy + sdk refs", refs)
	}
	if refs[0].Ref != "payloads/legacy-request.json.gz" ||
		refs[0].Path != "payloads/legacy-request.json.gz" ||
		refs[0].Format != "json" ||
		refs[0].Compression != "gzip" ||
		refs[0].OriginalBytes != 11 ||
		!refs[0].OriginalSet ||
		refs[0].StoredBytes != 7 ||
		!refs[0].StoredSet ||
		refs[0].SHA256 != "legacy-sha" ||
		refs[0].Captured == nil ||
		!*refs[0].Captured ||
		refs[0].SDK {
		t.Fatalf("legacy ref mismatch: %+v", refs[0])
	}
	if refs[1].Path != "payloads/sdk-request.json" ||
		refs[1].Format != "json" ||
		refs[1].StoredBytes != 5 ||
		!refs[1].StoredSet ||
		refs[1].SHA256 != "sdk-sha" ||
		refs[1].Captured == nil ||
		*refs[1].Captured ||
		!refs[1].SDK {
		t.Fatalf("sdk ref mismatch: %+v", refs[1])
	}

	pathOnly := extractAIAgentV2PayloadRefs(json.RawMessage(`{"path":"payloads/path-only.json","sha256":"path-sha"}`))
	if len(pathOnly) != 1 || pathOnly[0].Path != "payloads/path-only.json" || pathOnly[0].SHA256 != "path-sha" {
		t.Fatalf("path-only legacy ref = %+v, want path + sha", pathOnly)
	}

	objectRef := extractAIAgentV2PayloadRefs(json.RawMessage(`{"ref":{"path":"payloads/object.json","captured":true},"path":"ignored.json","sha256":"ignored-sha"}`))
	if len(objectRef) != 1 || objectRef[0].Path != "payloads/object.json" || objectRef[0].SHA256 != "" {
		t.Fatalf("object ref = %+v, want wrapped object to win over legacy envelope", objectRef)
	}
}

func TestExtractAIAgentV2SourceLogEntryArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	snapshot := map[string]any{
		"version": 2,
		"opTree": map[string]any{
			"traceId":   "root-session",
			"startedAt": int64(1_700_000_000_000),
			"endedAt":   int64(1_700_000_004_000),
			"success":   false,
			"error":     "session failed",
			"turns": []any{
				map[string]any{
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
	snapshotPath := writeAIAgentV2GzipJSON(t, root, "root-session.json.gz", snapshot)

	artifacts, err := ExtractAIAgentV2Source(context.Background(), AIAgentV2SourceOptions{
		Root:     root,
		SourceID: "aiagent_v2:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV2Source: %v", err)
	}

	assertAIAgentV2LogArtifact(t, findArtifact(t, artifacts, ClassLogEntry, "file:root-session.json.gz:/opTree/turns/0/ops/0/logs/0/message"), snapshotPath, "root-session", "turn:1", "/opTree/turns/0/ops/0/logs/0/message", "opened README", AvailabilityAvailable)
	assertAIAgentV2LogArtifact(t, findArtifact(t, artifacts, ClassLogEntry, "file:root-session.json.gz:/opTree/turns/0/ops/0/logs/1/message"), snapshotPath, "root-session", "turn:1", "/opTree/turns/0/ops/0/logs/1/message", "", AvailabilitySourceEmpty)
	assertAIAgentV2LogArtifact(t, findArtifact(t, artifacts, ClassLogEntry, "file:root-session.json.gz:/opTree/turns/0/ops/1/attributes/error"), snapshotPath, "root-session", "turn:1", "/opTree/turns/0/ops/1/attributes/error", "permission_denied", AvailabilityAvailable)
	assertAIAgentV2LogArtifact(t, findArtifact(t, artifacts, ClassLogEntry, "file:root-session.json.gz:/opTree/error"), snapshotPath, "root-session", "", "/opTree/error", "session failed", AvailabilityAvailable)
}

func TestAIAgentV2PayloadArtifactExceedingCapReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	relPath := "payloads/root-session/turn-0001/request.txt"
	absPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("123456789"), 0o600); err != nil {
		t.Fatalf("write payload fixture: %v", err)
	}

	captured := true
	state := aiAgentV2SourceState{
		root:       root,
		sourceID:   "aiagent_v2:" + root,
		sourceFile: filepath.Join(root, "root-session.json.gz"),
	}
	ref := aiAgentV2PayloadRef{
		Path:     relPath,
		Format:   "text",
		Captured: &captured,
	}

	_, err := state.aiAgentV2PayloadArtifactWithLimit(ref, "root-session", 1, 1, "llm_request", 1, 8)
	if err == nil {
		t.Fatal("aiAgentV2PayloadArtifactWithLimit returned nil error for payload above cap")
	}
	if !strings.Contains(err.Error(), "payload_ref exceeds 8 bytes") {
		t.Fatalf("error = %q, want payload cap error", err)
	}
}

func TestReadAIAgentV2SourceSnapshotCompressedOverCapReturnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "snapshot.json.gz")
	if err := os.WriteFile(path, []byte("123456789"), 0o600); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}

	_, err := readAIAgentV2SourceSnapshotWithLimit(path, 8)
	if err == nil {
		t.Fatal("readAIAgentV2SourceSnapshotWithLimit returned nil error for compressed snapshot above cap")
	}
	if !strings.Contains(err.Error(), "aiagent_v2 snapshot exceeds 8 bytes") {
		t.Fatalf("error = %q, want compressed snapshot cap error", err)
	}
}

func TestReadAIAgentV2SourceSnapshotGzipExpansionOverCapReturnsError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "snapshot.json.gz")
	if err := os.WriteFile(path, gzipBytes(t, bytes.Repeat([]byte("x"), 256)), 0o600); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}

	_, err := readAIAgentV2SourceSnapshotWithLimit(path, 128)
	if err == nil {
		t.Fatal("readAIAgentV2SourceSnapshotWithLimit returned nil error for decompressed snapshot above cap")
	}
	if !strings.Contains(err.Error(), "decompressed aiagent_v2 snapshot exceeds 128 bytes") {
		t.Fatalf("error = %q, want decompressed snapshot cap error", err)
	}
}

type aiAgentV2PayloadFixtureSet struct {
	captured                  []aiAgentV2CapturedPayloadFixture
	uncapturedToolResponseSHA string
}

type aiAgentV2CapturedPayloadFixture struct {
	class ArtifactClass
	rel   string
	path  string
	raw   []byte
	hash  string
}

func writeAIAgentV2ParityFixture(t *testing.T) (string, string, aiAgentV2PayloadFixtureSet) {
	t.Helper()

	root := t.TempDir()
	payloads := []aiAgentV2CapturedPayloadFixture{
		{class: ClassLLMRequest, rel: "payloads/root-session/turn-0001/llm-request.json.gz", raw: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)},
		{class: ClassLLMSDKRequest, rel: "payloads/root-session/turn-0001/sdk-request.json.gz", raw: []byte(`{"sdk":{"input":"hi"}}`)},
		{class: ClassLLMResponse, rel: "payloads/root-session/turn-0001/llm-response.json.gz", raw: []byte(`{"content":[{"type":"text","text":"hello"}]}`)},
		{class: ClassLLMSDKResponse, rel: "payloads/root-session/turn-0001/sdk-response.json.gz", raw: []byte(`{"sdk":{"output":"hello"}}`)},
		{class: ClassToolRequest, rel: "payloads/root-session/turn-0001/tool-request.json.gz", raw: []byte(`{"path":"README.md"}`)},
	}
	for i := range payloads {
		payloads[i].path, payloads[i].hash = writeAIAgentV2GzipPayload(t, root, payloads[i].rel, payloads[i].raw)
	}
	toolResponseSHA := sha256HexForTest([]byte("tool output unavailable"))

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
						aiAgentV2LLMOp(payloads[0], payloads[1], payloads[2], payloads[3]),
						aiAgentV2ToolOp(payloads[4], toolResponseSHA),
						aiAgentV2SessionOp(),
					},
				},
			},
		},
	}
	snapshotPath := writeAIAgentV2GzipJSON(t, root, "root-session.json.gz", snapshot)
	return root, snapshotPath, aiAgentV2PayloadFixtureSet{
		captured:                  payloads,
		uncapturedToolResponseSHA: toolResponseSHA,
	}
}

func aiAgentV2LLMOp(req, sdkReq, resp, sdkResp aiAgentV2CapturedPayloadFixture) map[string]any {
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
			"payload": aiAgentV2DualRefPayload(req, sdkReq),
		},
		"response": map[string]any{
			"size":    len(resp.raw),
			"payload": aiAgentV2DualRefPayload(resp, sdkResp),
		},
		"reasoning": map[string]any{
			"final": aiAgentV2ReasoningFinalText,
		},
	}
}

func aiAgentV2ToolOp(req aiAgentV2CapturedPayloadFixture, responseSHA string) map[string]any {
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
			"payload": map[string]any{"ref": aiAgentV2RefObject(req, true)},
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

func aiAgentV2SessionOp() map[string]any {
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

func aiAgentV2DualRefPayload(regular, sdk aiAgentV2CapturedPayloadFixture) map[string]any {
	return map[string]any{
		"ref": aiAgentV2RefObject(regular, true),
		"sdk": map[string]any{
			"ref": aiAgentV2RefObject(sdk, true),
		},
	}
}

func aiAgentV2RefObject(payload aiAgentV2CapturedPayloadFixture, captured bool) map[string]any {
	return map[string]any{
		"path":            payload.rel,
		"format":          "json",
		"compression":     "gzip",
		"originalBytes":   len(payload.raw),
		"compressedBytes": compressedAIAgentV2PayloadSize(payload.path),
		"sha256":          payload.hash,
		"captured":        captured,
	}
}

func writeAIAgentV2GzipPayload(t *testing.T, root string, relPath string, raw []byte) (string, string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir payload: %v", err)
	}
	if err := os.WriteFile(path, gzipBytes(t, raw), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return path, sha256HexForTest(raw)
}

func writeAIAgentV2GzipJSON(t *testing.T, root string, name string, value interface{}) string {
	t.Helper()

	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, gzipBytes(t, body), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return path
}

func compressedAIAgentV2PayloadSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func sha256HexForTest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}

func aiAgentV2CanonicalJSONHashForTest(t *testing.T, value any) string {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal hash fixture: %v", err)
	}
	hash, err := canonicalJSONHash(raw)
	if err != nil {
		t.Fatalf("hash fixture JSON: %v", err)
	}
	return hash
}

func assertAIAgentV2CapturedPayloadArtifact(t *testing.T, artifacts []Artifact, payload aiAgentV2CapturedPayloadFixture) {
	t.Helper()

	got := findArtifact(t, artifacts, payload.class, "file:"+payload.rel)
	if got.Adapter != "aiagent_v2" {
		t.Fatalf("adapter = %q, want aiagent_v2", got.Adapter)
	}
	if got.NativeSessionID != "root-session" {
		t.Fatalf("native_session_id = %q, want root-session", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: payload.path}).String()
	if got.Selector.URI != wantURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, wantURI)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashRawBytes {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashRawBytes)
	}
	if got.Bytes != int64(len(payload.raw)) || got.Chars != -1 || got.ComputedSHA256 != payload.hash || got.ProducerSHA256 != payload.hash {
		t.Fatalf("payload proof mismatch: %+v", got)
	}
}

func assertAIAgentV2InlineJSONPayloadArtifact(t *testing.T, got Artifact, snapshotPath string, pointer string, raw []byte) {
	t.Helper()

	if got.SourceFile != snapshotPath {
		t.Fatalf("source_file = %q, want %q", got.SourceFile, snapshotPath)
	}
	wantURI := (&url.URL{Scheme: "file", Path: snapshotPath}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	if got.Availability != AvailabilityAvailable || got.HashDomain != HashCanonicalJSON {
		t.Fatalf("availability/hash_domain mismatch: %+v", got)
	}
	canonical, err := canonicalJSONBytes(raw, "inline request")
	if err != nil {
		t.Fatalf("canonical inline request: %v", err)
	}
	if got.Bytes != int64(len(canonical)) || got.Chars != -1 || got.ComputedSHA256 != sha256HexForTest(canonical) {
		t.Fatalf("inline JSON proof mismatch: %+v", got)
	}
}

func assertAIAgentV2InlineTextPayloadArtifact(t *testing.T, got Artifact, snapshotPath string, pointer string, text string) {
	t.Helper()

	wantURI := (&url.URL{Scheme: "file", Path: snapshotPath}).String()
	if got.SourceFile != snapshotPath || got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("inline text selector mismatch: %+v", got)
	}
	if got.Availability != AvailabilityAvailable || got.HashDomain != HashSemanticText {
		t.Fatalf("availability/hash_domain mismatch: %+v", got)
	}
	if got.Bytes != int64(len(text)) || got.Chars != int64(len(text)) || got.ComputedSHA256 != stringSHA256(text) {
		t.Fatalf("inline text proof mismatch: %+v", got)
	}
}

func assertAIAgentV2LogArtifact(t *testing.T, got Artifact, snapshotPath string, nativeSessionID string, nativeTurnID string, pointer string, message string, availability Availability) {
	t.Helper()

	wantURI := (&url.URL{Scheme: "file", Path: snapshotPath}).String()
	if got.SourceFile != snapshotPath || got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("log selector mismatch: %+v, want source=%q uri=%q pointer=%q", got, snapshotPath, wantURI, pointer)
	}
	if got.NativeSessionID != nativeSessionID || got.NativeTurnID != nativeTurnID {
		t.Fatalf("log native scope mismatch: %+v, want session=%q turn=%q", got, nativeSessionID, nativeTurnID)
	}
	if got.Availability != availability || got.HashDomain != HashSemanticText {
		t.Fatalf("log availability/hash mismatch: %+v, want availability=%q hash=%q", got, availability, HashSemanticText)
	}
	if got.Bytes != int64(len(message)) || got.Chars != int64(len([]rune(message))) || got.ComputedSHA256 != stringSHA256(message) {
		t.Fatalf("log proof mismatch: %+v", got)
	}
}

func assertAIAgentV2ReasoningFinalArtifact(t *testing.T, got Artifact, snapshotPath string) {
	t.Helper()

	if got.Adapter != aiAgentV2Format {
		t.Fatalf("adapter = %q, want %q", got.Adapter, aiAgentV2Format)
	}
	if got.SourceFile != snapshotPath {
		t.Fatalf("source_file = %q, want %q", got.SourceFile, snapshotPath)
	}
	if got.NativeSessionID != "root-session" || got.NativeTurnID != "turn:1" {
		t.Fatalf("native scope mismatch: %+v", got)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashSemanticText {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashSemanticText)
	}
	if got.Selector.URI != aiAgentV2SelectorURI("ops", "root-session", "op:1:3") || got.Selector.FieldPath != "reasoning.final" {
		t.Fatalf("selector = %+v, want aiagent_v2 reasoning.final selector", got.Selector)
	}
	if got.Bytes != int64(len(aiAgentV2ReasoningFinalText)) || got.Chars != int64(len(aiAgentV2ReasoningFinalText)) {
		t.Fatalf("reasoning text length mismatch: %+v", got)
	}
	if got.ComputedSHA256 != stringSHA256(aiAgentV2ReasoningFinalText) {
		t.Fatalf("computed_sha256 = %q, want %q", got.ComputedSHA256, stringSHA256(aiAgentV2ReasoningFinalText))
	}
}

const aiAgentV2ReasoningFinalText = "root reasoning summary"

func assertAIAgentV2FinalReportArtifact(t *testing.T, got Artifact, snapshotPath string) {
	t.Helper()

	if got.Adapter != aiAgentV2Format {
		t.Fatalf("adapter = %q, want %q", got.Adapter, aiAgentV2Format)
	}
	if got.SourceFile != snapshotPath {
		t.Fatalf("source_file = %q, want %q", got.SourceFile, snapshotPath)
	}
	if got.NativeSessionID != "root-session" || got.NativeTurnID != "" {
		t.Fatalf("native scope mismatch: %+v", got)
	}
	if got.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %q, want %q", got.Availability, AvailabilityAvailable)
	}
	if got.HashDomain != HashCanonicalJSON {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashCanonicalJSON)
	}
	if got.Selector.URI != aiAgentV2SelectorURI("sessions", "root-session", "session:root-session") || got.Selector.FieldPath != "finalReport" {
		t.Fatalf("selector = %+v, want aiagent_v2 finalReport selector", got.Selector)
	}
	want := aiAgentV2FinalReportCanonicalJSON(t)
	if got.Bytes != int64(len(want)) || got.Chars != -1 {
		t.Fatalf("final_report proof length mismatch: %+v", got)
	}
	if got.ComputedSHA256 != sha256HexForTest(want) {
		t.Fatalf("computed_sha256 = %q, want %q", got.ComputedSHA256, sha256HexForTest(want))
	}
}

func aiAgentV2FinalReportCanonicalJSON(t *testing.T) []byte {
	t.Helper()

	var doc interface{}
	if err := json.Unmarshal([]byte(aiAgentV2FinalReportJSON), &doc); err != nil {
		t.Fatalf("decode final report fixture: %v", err)
	}
	out, err := canonicalIdentityBytes(doc)
	if err != nil {
		t.Fatalf("canonicalize final report fixture: %v", err)
	}
	return out
}

const aiAgentV2FinalReportJSON = `{"format":"json","captured":true,"summary":"All checks passed."}`
