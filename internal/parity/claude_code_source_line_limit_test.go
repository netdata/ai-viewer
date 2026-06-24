package parity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractClaudeCodeSourceOversizedTranscriptLineReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code line-limit fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	oversized := strings.Repeat("x", 8*1024*1024+1) + "\n"
	if err := os.WriteFile(transcript, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write oversized transcript: %v", err)
	}

	_, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err == nil {
		t.Fatal("ExtractClaudeCodeSource succeeded, want oversized line error")
	}
	if !strings.Contains(err.Error(), "line exceeds 8388608 bytes") {
		t.Fatalf("error = %q, want line-size cap", err)
	}
}

func TestExtractClaudeCodeSourceOversizedChildCompletionLineReturnsError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	subagentDir := filepath.Join(root, "-repo", "parent-session", "subagents")
	if err := os.MkdirAll(subagentDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code child line-limit fixture: %v", err)
	}
	childTranscript := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.jsonl")
	oversized := strings.Repeat("x", 8*1024*1024+1) + "\n"
	if err := os.WriteFile(childTranscript, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write oversized child transcript: %v", err)
	}

	_, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err == nil {
		t.Fatal("ExtractClaudeCodeSource succeeded, want oversized child line error")
	}
	if !strings.Contains(err.Error(), "line exceeds 8388608 bytes") {
		t.Fatalf("error = %q, want line-size cap", err)
	}
}
