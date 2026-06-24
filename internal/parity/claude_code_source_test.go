package parity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractClaudeCodeSourceInlinePayloadArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	lines := claudeCodeSourceFixtureLines()
	assertClaudeCodePointerArtifact(t, artifacts, ClassUserPrompt, "line:1:/message/content", transcript, 1, "/message/content", lines[0])
	assertClaudeCodePointerArtifact(t, artifacts, ClassAssistantMessage, "line:2:/message/content/0/text", transcript, 2, "/message/content/0/text", lines[1])
	assertClaudeCodePointerArtifact(t, artifacts, ClassReasoningText, "line:2:/message/content/1/thinking", transcript, 2, "/message/content/1/thinking", lines[1])
	assertClaudeCodePointerArtifact(t, artifacts, ClassToolRequest, "line:2:/message/content/2/input", transcript, 2, "/message/content/2/input", lines[1])
	assertClaudeCodePointerArtifact(t, artifacts, ClassToolResponse, "line:3:/message/content/0/content", transcript, 3, "/message/content/0/content", lines[2])
	assertClaudeCodePointerArtifact(t, artifacts, ClassToolResponse, "line:3:/toolUseResult", transcript, 3, "/toolUseResult", lines[2])
	assertClaudeCodePointerArtifact(t, artifacts, ClassLogEntry, "line:5:/message/content", transcript, 5, "/message/content", lines[4])

	userAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:01.000Z")
	assistantAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	toolEnd := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:03.000Z")
	compactionAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:04.000Z")
	compactionEnd := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:05.000Z")
	turnEnd := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:06.000Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:session-1"), sessionBoundaryIdentity{
		NativeSessionID:     "session-1",
		RootNativeSessionID: "session-1",
		Kind:                "root",
		Status:              "running",
		StartedAt:           userAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &turnEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &userAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "claude-opus-4-7",
		Status:          "completed",
		StartedAt:       assistantAt,
		EndedAt:         &assistantAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "reasoning",
		Status:          "completed",
		StartedAt:       assistantAt,
		EndedAt:         &assistantAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:4"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           4,
		Kind:            "tool",
		Name:            "Read",
		ToolNamespace:   "builtin",
		Status:          "completed",
		StartedAt:       assistantAt,
		EndedAt:         &toolEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:5"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           5,
		Kind:            "compaction",
		Name:            "compaction",
		Status:          "completed",
		StartedAt:       compactionAt,
		EndedAt:         &compactionEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassCompactionEvent, "op:1:5:compaction"), struct {
		NativeSessionID         string `json:"native_session_id"`
		TurnSeq                 int64  `json:"turn_seq"`
		OpSeq                   int64  `json:"op_seq"`
		Trigger                 string `json:"trigger,omitempty"`
		PreTokens               int64  `json:"pre_tokens"`
		PostTokens              int64  `json:"post_tokens"`
		MetadataPreTokens       int64  `json:"metadata_pre_tokens"`
		MetadataPostTokens      int64  `json:"metadata_post_tokens"`
		DurationMs              int64  `json:"duration_ms"`
		StartedAt               int64  `json:"started_at"`
		EndedAt                 *int64 `json:"ended_at,omitempty"`
		PreservedSegmentSHA256  string `json:"preserved_segment_sha256,omitempty"`
		PreservedMessagesSHA256 string `json:"preserved_messages_sha256,omitempty"`
	}{
		NativeSessionID:         "session-1",
		TurnSeq:                 1,
		OpSeq:                   5,
		Trigger:                 "manual",
		PreTokens:               100,
		PostTokens:              20,
		MetadataPreTokens:       100,
		MetadataPostTokens:      20,
		DurationMs:              1000,
		StartedAt:               compactionAt,
		EndedAt:                 &compactionEnd,
		PreservedSegmentSHA256:  mustClaudeCodeCanonicalJSONHash(t, map[string]any{"headUuid": "u1", "anchorUuid": "a1", "tailUuid": "u3"}),
		PreservedMessagesSHA256: mustClaudeCodeCanonicalJSONHash(t, map[string]any{"anchorUuid": "a1", "uuids": []any{"u1", "a1", "u3"}}),
	})
}

func TestExtractClaudeCodeSourceToWriterMatchesSliceExtractor(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeSubagentSourceFixture(t)
	opts := ClaudeCodeSourceOptions{Root: root, SourceID: "claude-code:" + root}
	want, err := ExtractClaudeCodeSource(context.Background(), opts)
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	var got []Artifact
	err = ExtractClaudeCodeSourceToWriter(context.Background(), opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		got = append(got, artifact)
		return nil
	}))
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSourceToWriter: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streamed claude-code artifacts mismatch\ngot=%+v\nwant=%+v", got, want)
	}
}

func TestExtractClaudeCodeSourceCoalescesDuplicateBoundaryArtifacts(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeDuplicateBoundarySourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	assertNoClaudeCodeDuplicateBoundaryArtifacts(t, artifacts)

	userAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:03.000Z")
	turnEnd := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:04.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionBoundary, "session:session-1"), sessionBoundaryIdentity{
		NativeSessionID:     "session-1",
		RootNativeSessionID: "session-1",
		Kind:                "root",
		Status:              "running",
		StartedAt:           userAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &turnEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &userAt,
	})
}

func TestExtractClaudeCodeSourceDelayedToolResultUsesOriginalTurn(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeDelayedToolResultSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	toolStartedAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	toolEndedAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:05.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "tool",
		Name:            "Read",
		ToolNamespace:   "builtin",
		Status:          "failed",
		StartedAt:       toolStartedAt,
		EndedAt:         &toolEndedAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:3:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              3,
		OpKind:             "tool",
		ErrorClass:         "tool_error",
		ErrorMessageSHA256: stringSHA256(""),
	})
	if got := findArtifact(t, artifacts, ClassToolResponse, "line:5:/message/content/0/content"); got.NativeTurnID != "turn:1" {
		t.Fatalf("delayed tool_response turn = %q, want turn:1", got.NativeTurnID)
	}
	assertNoArtifact(t, artifacts, ClassOpBoundary, "op:2:3")
	assertNoArtifact(t, artifacts, ClassToolError, "op:2:3:error")
}

func TestExtractClaudeCodeSourceOpenToolEOFUsesOriginalTurn(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeOpenToolEOFSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	toolStartedAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "tool",
		Name:            "Bash",
		ToolNamespace:   "builtin",
		Status:          "running",
		StartedAt:       toolStartedAt,
	})
	assertNoArtifact(t, artifacts, ClassOpBoundary, "op:2:3")
}

func TestExtractClaudeCodeSourceSubagentLinkArtifacts(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeSubagentSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	parentAssistantAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	childAssistantAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:05.000Z")
	childNativeID := "parent-session:agent:a1b2c3d4e5f6071"

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "parent-session",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "session",
		Name:            "explore repository",
		ToolNamespace:   "builtin",
		Status:          "completed",
		StartedAt:       parentAssistantAt,
		EndedAt:         &childAssistantAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSubagentLink, "op:1:3:child_session:"+childNativeID), subagentLinkIdentity{
		ParentNativeSessionID: "parent-session",
		ParentTurnSeq:         1,
		ParentOpSeq:           3,
		ChildNativeSessionID:  childNativeID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	})
}

func TestExtractClaudeCodeSourceAPIErrorArtifacts(t *testing.T) {
	t.Parallel()

	root, _ := writeClaudeCodeAPIErrorSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	userAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:01.000Z")
	apiErrorAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:1"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           1,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       userAt,
		EndedAt:         &userAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:2"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           2,
		Kind:            "llm",
		Name:            "api_error",
		Status:          "failed",
		StartedAt:       apiErrorAt,
		EndedAt:         &apiErrorAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassLLMError, "op:1:2:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              2,
		OpKind:             "llm",
		ErrorClass:         "api_error_529",
		ErrorMessageSHA256: stringSHA256("overloaded"),
	})
}

func TestExtractClaudeCodeSourceSystemOpArtifacts(t *testing.T) {
	t.Parallel()

	root, _ := writeClaudeCodeSystemOpSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	systemAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSystemOp, "line:2:/system"), claudeCodeSystemOpIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Subtype:         "local_command",
		Severity:        "INF",
		Message:         "system:local_command",
		Timestamp:       systemAt,
		ContentSHA256:   stringSHA256("/clear"),
	})
}

func TestExtractClaudeCodeSourceSessionMetadataArtifacts(t *testing.T) {
	t.Parallel()

	root, _ := writeClaudeCodeSessionMetadataSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassSessionMetadata, "session:session-1:metadata"), claudeCodeSessionMetadataIdentity{
		NativeSessionID:       "session-1",
		LastPromptSHA256:      stringSHA256("final prompt"),
		CustomTitle:           "Pinned title",
		AITitle:               "AI title",
		PermissionMode:        "acceptEdits",
		BridgeSessionID:       "cse_fixture",
		BridgeLastSequenceNum: 42,
		FileHistorySHA256: mustClaudeCodeCanonicalJSONHash(t, map[string]any{
			"README.md":  map[string]any{"backupFileName": "README.md.bak", "version": float64(3)},
			"cmd/app.go": map[string]any{"backupTime": "2026-06-22T00:00:00.500Z", "version": float64(1)},
		}),
		PRLinks: []claudeCodePRLinkIdentity{
			{Number: 10, URL: "https://example.invalid/pr/10", Repository: "owner/repo"},
			{Number: 11, URL: "https://example.invalid/pr/11", Repository: "owner/repo"},
		},
	})
}

func TestExtractClaudeCodeSourceAttachmentMetadataArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeAttachmentSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:test-source",
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	got := findArtifact(t, artifacts, ClassAttachmentMetadata, "line:1:/attachment")
	wantURI := (&url.URL{Scheme: "file", Path: transcript, Fragment: "L1"}).String()
	if got.HashDomain != HashIdentityJSON {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, HashIdentityJSON)
	}
	if got.Selector.URI != wantURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, wantURI)
	}
	if got.NativeTurnID != "turn:0" {
		t.Fatalf("native_turn_id = %q, want turn:0 before the first prompt", got.NativeTurnID)
	}
	if got.Bytes <= 0 || got.ComputedSHA256 == "" {
		t.Fatalf("attachment metadata proof missing: %+v", got)
	}
}

func TestExtractClaudeCodeSourceGenericLogArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeGenericLogSourceFixture(t)
	sourceID := "claude-code:" + root
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 2, "turn:1", "DBG", "meta-user")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 3, "turn:1", "INF", "synthetic-assistant")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 4, "turn:1", "INF", "queue-operation")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 5, "turn:1", "INF", "pr-link")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 6, "turn:1", "ERR", "api_error")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 7, "turn:1", "INF", "compact_boundary")
	assertClaudeCodeLogArtifact(t, artifacts, sourceID, transcript, 8, "turn:1", "INF", "compaction-summary")
}

func TestExtractClaudeCodeSourceAgentToolResultArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeAgentToolResultSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	assistantAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	resultAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:03.000Z")
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "session",
		Name:            "review work",
		ToolNamespace:   "builtin",
		Status:          "failed",
		StartedAt:       assistantAt,
		EndedAt:         &resultAt,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:3:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              3,
		OpKind:             "session",
		ErrorClass:         "tool_error",
		ErrorMessageSHA256: stringSHA256(""),
	})
	lines := claudeCodeAgentToolResultFixtureLines()
	assertClaudeCodePointerArtifact(t, artifacts, ClassToolResponse, "line:3:/message/content/0/content", transcript, 3, "/message/content/0/content", lines[2])
	assertClaudeCodePointerArtifact(t, artifacts, ClassToolResponse, "line:3:/toolUseResult", transcript, 3, "/toolUseResult", lines[2])
}

func writeClaudeCodeSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(claudeCodeSourceFixtureLines())), 0o600); err != nil {
		t.Fatalf("write claude-code transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeGenericLogSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code generic-log fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"user","uuid":"u_meta","sessionId":"session-1","isMeta":true,"timestamp":"2026-06-22T00:00:02.000Z","message":{"role":"user","content":"<local-command-caveat>"}}`,
		`{"type":"assistant","uuid":"a_syn","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"synthetic"}]}}`,
		`{"type":"queue-operation","sessionId":"session-1","timestamp":"2026-06-22T00:00:04.000Z","operation":"enqueue","prompt":"next"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":10,"prUrl":"https://example.invalid/pr/10","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:05.000Z"}`,
		`{"type":"system","subtype":"api_error","uuid":"sy1","sessionId":"session-1","content":"provider overloaded","timestamp":"2026-06-22T00:00:06.000Z","error":{"status":529,"type":"overloaded_error","message":"overloaded"}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sys1","sessionId":"session-1","timestamp":"2026-06-22T00:00:07.000Z","compactMetadata":{"trigger":"manual","preTokens":100,"postTokens":20,"durationMs":1000}}`,
		`{"type":"user","uuid":"u_summary","sessionId":"session-1","isCompactSummary":true,"timestamp":"2026-06-22T00:00:08.000Z","message":{"role":"user","content":"summary"}}`,
	}
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write claude-code generic-log transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeAgentToolResultSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code agent-result fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(claudeCodeAgentToolResultFixtureLines())), 0o600); err != nil {
		t.Fatalf("write claude-code agent-result transcript: %v", err)
	}
	return root, transcript
}

func claudeCodeAgentToolResultFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"review work","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_agent_1","content":"agent failed","is_error":true}]},"toolUseResult":{"stderr":"agent failed","exit_code":1}}`,
	}
}

func writeClaudeCodeAttachmentSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code attachment fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	line := `{"type":"attachment","uuid":"att1","sessionId":"session-1","timestamp":"2026-06-22T00:00:01.000Z","attachment":{"type":"file","filename":"/repo/main.go","displayPath":"main.go"}}`
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write claude-code attachment transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeSubagentSourceFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	parentSessionID := "parent-session"
	parentTranscript := filepath.Join(projectDir, parentSessionID+".jsonl")
	subagentDir := filepath.Join(projectDir, parentSessionID, "subagents")
	childTranscript := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.jsonl")
	childMeta := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.meta.json")
	if err := os.MkdirAll(subagentDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code subagent fixture: %v", err)
	}
	parentLines := []string{
		`{"type":"user","uuid":"u1","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"parent-session","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"explore repository","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
	}
	childLines := []string{
		`{"type":"user","uuid":"cu1","parentUuid":null,"isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","uuid":"ca1","parentUuid":"cu1","isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","requestId":"req-2","timestamp":"2026-06-22T00:00:05.000Z","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":2},"content":[{"type":"text","text":"done"}]}}`,
	}
	if err := os.WriteFile(parentTranscript, []byte(joinJSONLLines(parentLines)), 0o600); err != nil {
		t.Fatalf("write claude-code parent transcript: %v", err)
	}
	if err := os.WriteFile(childTranscript, []byte(joinJSONLLines(childLines)), 0o600); err != nil {
		t.Fatalf("write claude-code child transcript: %v", err)
	}
	if err := os.WriteFile(childMeta, []byte(`{"agentType":"general-purpose","toolUseId":"toolu_agent_1"}`), 0o600); err != nil {
		t.Fatalf("write claude-code child meta: %v", err)
	}
	return root
}

func writeClaudeCodeAPIErrorSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code api-error fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(claudeCodeAPIErrorFixtureLines())), 0o600); err != nil {
		t.Fatalf("write claude-code api-error transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeSystemOpSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code system-op fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"system","subtype":"local_command","uuid":"sy1","sessionId":"session-1","content":"/clear","timestamp":"2026-06-22T00:00:02.000Z"}`,
	}
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write claude-code system-op transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeSessionMetadataSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code metadata fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"last-prompt","lastPrompt":"draft prompt","leafUuid":"u0","sessionId":"session-1"}`,
		`{"type":"permission-mode","permissionMode":"acceptEdits","sessionId":"session-1"}`,
		`{"type":"custom-title","customTitle":"Pinned title","sessionId":"session-1"}`,
		`{"type":"ai-title","aiTitle":"AI title","sessionId":"session-1"}`,
		`{"type":"bridge-session","sessionId":"session-1","bridgeSessionId":"cse_fixture","lastSequenceNum":42}`,
		`{"type":"file-history-snapshot","messageId":"u1","sessionId":"session-1","snapshot":{"messageId":"u1","trackedFileBackups":{"README.md":{"backupFileName":"README.md.bak","version":2},"old.go":{"backupFileName":"old.go.bak","version":1}},"timestamp":"2026-06-22T00:00:00.000Z"},"isSnapshotUpdate":true}`,
		`{"type":"file-history-snapshot","messageId":"u1","sessionId":"session-1","snapshot":{"messageId":"u1","trackedFileBackups":{"README.md":{"version":3},"cmd/app.go":{"backupFileName":null,"backupTime":"2026-06-22T00:00:00.500Z","version":1},"old.go":null},"timestamp":"2026-06-22T00:00:00.500Z"},"isSnapshotUpdate":true}`,
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"last-prompt","lastPrompt":"final prompt","leafUuid":"u1","sessionId":"session-1"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":10,"prUrl":"https://example.invalid/pr/10","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:02.000Z"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":11,"prUrl":"https://example.invalid/pr/11","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:03.000Z"}`,
	}
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write claude-code metadata transcript: %v", err)
	}
	return root, transcript
}

func claudeCodeSourceFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"answer"},{"type":"thinking","thinking":"think","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file body","is_error":false}]},"toolUseResult":{"stdout":"file body","exit_code":0}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sys1","sessionId":"session-1","timestamp":"2026-06-22T00:00:04.000Z","compactMetadata":{"trigger":"manual","preTokens":100,"postTokens":20,"durationMs":1000,"preservedSegment":{"headUuid":"u1","anchorUuid":"a1","tailUuid":"u3"},"preservedMessages":{"anchorUuid":"a1","uuids":["u1","a1","u3"]}}}`,
		`{"type":"user","uuid":"u3","sessionId":"session-1","isCompactSummary":true,"timestamp":"2026-06-22T00:00:05.000Z","message":{"role":"user","content":"summary"}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys2","sessionId":"session-1","durationMs":5000,"timestamp":"2026-06-22T00:00:06.000Z"}`,
	}
}

func mustClaudeCodeCanonicalJSONHash(t *testing.T, value interface{}) string {
	t.Helper()

	body, err := canonicalIdentityBytes(value)
	if err != nil {
		t.Fatalf("canonicalIdentityBytes: %v", err)
	}
	return stringSHA256(string(body))
}

func claudeCodeAPIErrorFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"system","subtype":"api_error","uuid":"sy1","sessionId":"session-1","content":"provider overloaded","timestamp":"2026-06-22T00:00:02.000Z","error":{"status":529,"type":"overloaded_error","message":"overloaded","requestID":"req_1"},"retryInMs":38317.38269012852,"retryAttempt":1}`,
	}
}

func writeClaudeCodeDuplicateBoundarySourceFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeClaudeCodeSourceLines(t, filepath.Join(root, "-a", "session-1.jsonl"), []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys1","sessionId":"session-1","durationMs":1000,"timestamp":"2026-06-22T00:00:02.000Z"}`,
	})
	writeClaudeCodeSourceLines(t, filepath.Join(root, "-b", "session-1.jsonl"), []string{
		`{"type":"user","uuid":"u2","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"second"}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys2","sessionId":"session-1","durationMs":1000,"timestamp":"2026-06-22T00:00:04.000Z"}`,
	})
	return root
}

func writeClaudeCodeDelayedToolResultSourceFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeClaudeCodeSourceLines(t, filepath.Join(root, "-repo", "session-1.jsonl"), []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys1","sessionId":"session-1","durationMs":1000,"timestamp":"2026-06-22T00:00:03.000Z"}`,
		`{"type":"user","uuid":"u2","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:04.000Z","message":{"role":"user","content":"second"}}`,
		`{"type":"user","uuid":"u3","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:05.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"denied","is_error":true}]}}`,
	})
	return root
}

func writeClaudeCodeOpenToolEOFSourceFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeClaudeCodeSourceLines(t, filepath.Join(root, "-repo", "session-1.jsonl"), []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"sleep 1"}}]}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys1","sessionId":"session-1","durationMs":1000,"timestamp":"2026-06-22T00:00:03.000Z"}`,
		`{"type":"user","uuid":"u2","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:04.000Z","message":{"role":"user","content":"second"}}`,
	})
	return root
}

func writeClaudeCodeSourceLines(t *testing.T, path string, lines []string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir claude-code source fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write claude-code source fixture: %v", err)
	}
}

func assertNoArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeArtifactID string) {
	t.Helper()

	for _, artifact := range artifacts {
		if artifact.Class == class && artifact.NativeArtifactID == nativeArtifactID {
			t.Fatalf("unexpected %s artifact %q: %+v", class, nativeArtifactID, artifact)
		}
	}
}

func assertNoClaudeCodeDuplicateBoundaryArtifacts(t *testing.T, artifacts []Artifact) {
	t.Helper()

	counts := map[MatchKey]int{}
	for _, artifact := range artifacts {
		switch artifact.Class {
		case ClassSessionBoundary, ClassTurnBoundary, ClassOpBoundary:
			counts[artifact.Key()]++
		}
	}
	for key, count := range counts {
		if count > 1 {
			t.Fatalf("duplicate %s boundary key native_session=%q native_artifact=%q count=%d", key.Class, key.NativeSessionID, key.NativeArtifactID, count)
		}
	}
}

func assertClaudeCodePointerArtifact(t *testing.T, artifacts []Artifact, class ArtifactClass, nativeID string, filePath string, line int, pointer string, sourceLine string) {
	t.Helper()

	got := findArtifact(t, artifacts, class, nativeID)
	if got.Adapter != "claude-code" {
		t.Fatalf("adapter = %q, want claude-code", got.Adapter)
	}
	if got.NativeSessionID != "session-1" {
		t.Fatalf("native_session_id = %q, want session-1", got.NativeSessionID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", line)}).String()
	if got.Selector.URI != wantURI || got.Selector.JSONPointer != pointer {
		t.Fatalf("selector = %+v, want uri=%q pointer=%q", got.Selector, wantURI, pointer)
	}
	resolved, err := resolveJSONPointerPayload([]byte(sourceLine), pointer)
	if err != nil {
		t.Fatalf("resolve fixture pointer %s: %v", pointer, err)
	}
	wantHash := sha256.Sum256(resolved.bytes)
	wantAvailability := AvailabilityAvailable
	wantChars := int64(-1)
	if len(resolved.bytes) == 0 {
		wantAvailability = AvailabilitySourceEmpty
		wantChars = 0
	} else if resolved.hashDomain == HashSemanticText {
		wantChars = int64(len(string(resolved.bytes)))
	}
	if got.Availability != wantAvailability {
		t.Fatalf("availability = %q, want %q", got.Availability, wantAvailability)
	}
	if got.HashDomain != resolved.hashDomain {
		t.Fatalf("hash_domain = %q, want %q", got.HashDomain, resolved.hashDomain)
	}
	if got.Bytes != int64(len(resolved.bytes)) || got.Chars != wantChars || got.ComputedSHA256 != fmt.Sprintf("%x", wantHash) {
		t.Fatalf("pointer proof mismatch: %+v", got)
	}
}

func assertClaudeCodeLogArtifact(t *testing.T, artifacts []Artifact, sourceID string, filePath string, lineNo int, turnID string, severity string, message string) {
	t.Helper()

	nativeID := fmt.Sprintf("line:%d:/log", lineNo)
	got := findArtifact(t, artifacts, ClassLogEntry, nativeID)
	if got.Adapter != "claude-code" || got.SourceID != sourceID || got.NativeSessionID != "session-1" {
		t.Fatalf("log artifact identity mismatch: %+v", got)
	}
	if got.NativeTurnID != turnID {
		t.Fatalf("native_turn_id = %q, want %s", got.NativeTurnID, turnID)
	}
	wantURI := (&url.URL{Scheme: "file", Path: filePath, Fragment: fmt.Sprintf("L%d", lineNo)}).String()
	if got.Selector.URI != wantURI {
		t.Fatalf("selector.uri = %q, want %q", got.Selector.URI, wantURI)
	}
	if got.Availability != AvailabilityAvailable || got.HashDomain != HashSemanticText {
		t.Fatalf("log proof flags mismatch: %+v", got)
	}
	if got.Bytes != int64(len(message)) || got.Chars != int64(len(message)) || got.ComputedSHA256 != stringSHA256(message) {
		t.Fatalf("log proof mismatch: %+v", got)
	}
}

func mustClaudeCodeTestMicros(t *testing.T, timestamp string) int64 {
	t.Helper()

	tsUs, err := parseClaudeCodeSourceTimestamp(timestamp)
	if err != nil {
		t.Fatalf("parseClaudeCodeSourceTimestamp: %v", err)
	}
	return tsUs
}
