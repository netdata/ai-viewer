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

func TestClaudeCodeIngestUserTextBlockMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeUserTextParityFixture(t)
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

	events := make(chan canonical.Event, 128)
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

	sourceArtifacts = filterClaudeCodeUserTextParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeUserTextParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code user-text parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeClaudeCodeUserTextParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code user-text fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"operator clarification"},{"type":"tool_result","tool_use_id":"toolu_1","content":"file body","is_error":false}]},"toolUseResult":{"stdout":"file body","exit_code":0}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code user-text transcript: %v", err)
	}
	return root, transcript
}

func filterClaudeCodeUserTextParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" {
			continue
		}
		switch {
		case artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID == "op:1:4":
			out = append(out, artifact)
		case artifact.Class == parity.ClassUserPrompt && artifact.NativeArtifactID == "line:3:/message/content/0/text":
			out = append(out, artifact)
		}
	}
	return out
}
