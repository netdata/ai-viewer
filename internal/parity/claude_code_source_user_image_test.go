package parity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractClaudeCodeSourceUserImageBlockArtifacts(t *testing.T) {
	t.Parallel()

	root, transcript := writeClaudeCodeUserImageSourceFixture(t)
	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}

	lines := claudeCodeUserImageFixtureLines()
	userAt := mustClaudeCodeTestMicros(t, "2026-06-22T00:00:01.000Z")
	assertClaudeCodePointerArtifact(t, artifacts, ClassUserImage, "line:1:/message/content/0", transcript, 1, "/message/content/0", lines[0])
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

func writeClaudeCodeUserImageSourceFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code user-image fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(joinJSONLLines(claudeCodeUserImageFixtureLines())), 0o600); err != nil {
		t.Fatalf("write claude-code user-image transcript: %v", err)
	}
	return root, transcript
}

func claudeCodeUserImageFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}}`,
	}
}
