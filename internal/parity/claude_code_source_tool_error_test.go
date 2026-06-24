package parity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractClaudeCodeSourceToolErrorArtifacts(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeToolErrorSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	userAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:01.000Z")
	assistantAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:02.000Z")
	toolEnd := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:03.000Z")

	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:3"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           3,
		Kind:            "tool",
		Name:            "Read",
		ToolNamespace:   "builtin",
		Status:          "failed",
		StartedAt:       assistantAt,
		EndedAt:         &toolEnd,
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassToolError, "op:1:3:error"), opErrorIdentity{
		NativeSessionID:    "session-1",
		TurnSeq:            1,
		OpSeq:              3,
		OpKind:             "tool",
		ErrorClass:         "tool_error",
		ErrorMessageSHA256: stringSHA256(""),
	})
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassTurnBoundary, "turn:1"), turnBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		Status:          "running",
		StartedAt:       userAt,
	})
}

func writeClaudeCodeToolErrorSourceFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code tool-error fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := claudeCodeToolErrorFixtureLines()
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(lines)), 0o600); err != nil {
		t.Fatalf("write claude-code tool-error transcript: %v", err)
	}
	return root
}

func claudeCodeToolErrorFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"missing.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file not found","is_error":true}]},"toolUseResult":{"stderr":"file not found","exit_code":1}}`,
	}
}
