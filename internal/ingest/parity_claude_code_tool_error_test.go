package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestClaudeCodeIngestToolErrorArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeClaudeCodeToolErrorParityFixture(t)
	sourceID := "claude-code:" + root

	adapter, err := claude_code.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("claude-code adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("claude_code.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("claude-code Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "claude-code", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyClaudeCodeEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractClaudeCodeSource(ctx, parity.ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterClaudeCodeToolErrorParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeToolErrorParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source artifact count = %d, want 4", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical artifact count = %d, want 4", len(canonicalArtifacts))
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code tool-error parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeClaudeCodeToolErrorParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code tool-error fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"missing.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file not found","is_error":true}]},"toolUseResult":{"stderr":"file not found","exit_code":1}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code tool-error transcript: %v", err)
	}
	return root
}

func filterClaudeCodeToolErrorParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" {
			continue
		}
		if artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID == "op:1:3" {
			out = append(out, artifact)
		}
		if artifact.Class == parity.ClassToolError || artifact.Class == parity.ClassToolResponse {
			out = append(out, artifact)
		}
	}
	return out
}
