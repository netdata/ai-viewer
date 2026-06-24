package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestClaudeCodeIngestAttachmentMetadataMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeAttachmentParityFixture(t)
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

	events := make(chan canonical.Event, 32)
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

	sourceArtifacts = filterClaudeCodeAttachmentMetadataParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeAttachmentMetadataParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source attachment artifact count = %d, want 1 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical attachment artifact count = %d, want 1 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code attachment metadata parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeClaudeCodeAttachmentParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code attachment fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	line := `{"type":"attachment","uuid":"att1","sessionId":"session-1","timestamp":"2026-06-22T00:00:01.000Z","attachment":{"type":"edited_text_file","filename":"/repo/main.go","snippet":"1\tpackage main"}}`
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code attachment transcript: %v", err)
	}
	return root, transcript
}

func filterClaudeCodeAttachmentMetadataParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.Class == parity.ClassAttachmentMetadata {
			out = append(out, artifact)
		}
	}
	return out
}
