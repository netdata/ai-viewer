package parity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractClaudeCodeSourceUserTextBlockArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeUserTextSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	lines := claudeCodeUserTextFixtureLines()
	userTextAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:03.000Z")
	assertClaudeCodePointerArtifact(t, artifacts, ClassUserPrompt, "line:3:/message/content/0/text", transcript, 3, "/message/content/0/text", lines[2])
	assertIdentityArtifact(t, findArtifact(t, artifacts, ClassOpBoundary, "op:1:4"), opBoundaryIdentity{
		NativeSessionID: "session-1",
		TurnSeq:         1,
		OpSeq:           4,
		Kind:            "internal",
		Name:            "user_input",
		Status:          "completed",
		StartedAt:       userTextAt,
		EndedAt:         &userTextAt,
	})
}

func writeClaudeCodeUserTextSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code user-text fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(claudeCodeUserTextFixtureLines())), 0o600); err != nil {
		t.Fatalf("write claude-code user-text transcript: %v", err)
	}
	return root, transcript
}

func claudeCodeUserTextFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"operator clarification"},{"type":"tool_result","tool_use_id":"toolu_1","content":"file body","is_error":false}]},"toolUseResult":{"stdout":"file body","exit_code":0}}`,
	}
}
