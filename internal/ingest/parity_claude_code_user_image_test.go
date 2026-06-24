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

func TestClaudeCodeIngestUserImageBlockMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeUserImageParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeUserImageParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeUserImageParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code user-image parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeClaudeCodeUserImageParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code user-image fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code user-image transcript: %v", err)
	}
	return root, transcript
}

func filterClaudeCodeUserImageParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" {
			continue
		}
		switch {
		case artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID == "op:1:1":
			out = append(out, artifact)
		case artifact.Class == parity.ClassUserImage && artifact.NativeArtifactID == "line:1:/message/content/0":
			out = append(out, artifact)
		}
	}
	return out
}
