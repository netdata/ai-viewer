package parity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadClaudeCodeSourceMetaLimitedRejectsBytesAboveCap(t *testing.T) {
	t.Parallel()

	_, err := readClaudeCodeSourceMetaLimited(strings.NewReader(strings.Repeat("x", claudeCodeSourceMetaReadMax+1)))
	if err == nil {
		t.Fatal("readClaudeCodeSourceMetaLimited succeeded, want size-cap error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want size-cap error", err)
	}
}

func TestExtractClaudeCodeSourceMalformedSidecarReturnsError(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeSourceSidecarFixture(t, []byte(`{"toolUseId":`))

	_, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err == nil {
		t.Fatal("ExtractClaudeCodeSource succeeded, want malformed sidecar error")
	}
	if !strings.Contains(err.Error(), "decode claude-code source meta") {
		t.Fatalf("error = %q, want decode claude-code source meta", err)
	}
}

func TestExtractClaudeCodeSourceOversizedSidecarReturnsError(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeSourceSidecarFixture(t, []byte(strings.Repeat("x", claudeCodeSourceMetaReadMax+1)))

	_, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err == nil {
		t.Fatal("ExtractClaudeCodeSource succeeded, want oversized sidecar error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want size-cap error", err)
	}
}

func writeClaudeCodeSourceSidecarFixture(t *testing.T, meta []byte) string {
	t.Helper()

	root := t.TempDir()
	subagentDir := filepath.Join(root, "-repo", "parent-session", "subagents")
	if err := os.MkdirAll(subagentDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code sidecar fixture: %v", err)
	}
	childTranscript := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.jsonl")
	childLines := []string{
		`{"type":"user","uuid":"cu1","parentUuid":null,"isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"inspect"}}`,
	}
	if err := os.WriteFile(childTranscript, []byte(joinJSONLLines(childLines)), 0o600); err != nil {
		t.Fatalf("write claude-code child transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.meta.json"), meta, 0o600); err != nil {
		t.Fatalf("write claude-code child meta: %v", err)
	}
	return root
}
