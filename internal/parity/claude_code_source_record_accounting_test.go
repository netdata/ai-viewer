package parity

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractClaudeCodeSourceUnknownRecordTypeReturnsError(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeRecordAccountingFixture(t, `{"type":"future-source-artifact","payload":{"text":"must not be ignored"}}`)

	_, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err == nil {
		t.Fatal("ExtractClaudeCodeSource succeeded, want unknown record type error")
	}
	if !strings.Contains(err.Error(), `unknown claude-code source record type "future-source-artifact"`) {
		t.Fatalf("error = %q, want unknown record type", err)
	}
}

func TestExtractClaudeCodeSourceMalformedJSONLineEmitsSourceCorruptionAndContinues(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	transcript := filepath.Join(root, "-repo", "session-1.jsonl")
	writeClaudeCodeSourceLines(t, transcript, []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":`,
		`{"type":"user","uuid":"u2","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"second"}}`,
	})

	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}
	corrupt := findArtifact(t, artifacts, ClassSourceCorruption, "source_corruption:line:2")
	if corrupt.Availability != AvailabilitySourceCorrupt {
		t.Fatalf("availability = %q, want %q", corrupt.Availability, AvailabilitySourceCorrupt)
	}
	if corrupt.Selector.URI != (&url.URL{Scheme: "file", Path: transcript, Fragment: "L2"}).String() {
		t.Fatalf("selector.uri = %q", corrupt.Selector.URI)
	}
	assertIntegrityFailures(t, corrupt, []IntegrityFailure{{
		Field:    "json",
		Expected: "valid claude-code JSONL record",
		Actual:   "decode_error",
	}})
	findArtifact(t, artifacts, ClassUserPrompt, "line:3:/message/content")
}

func TestExtractClaudeCodeSourceKnownNoOpRecordTypeIsIgnored(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeRecordAccountingFixture(t, `{"type":"summary","summary":"old compact summary metadata"}`)

	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts len = %d, want 0 for timestamp-less no-op record", len(artifacts))
	}
}

func TestExtractClaudeCodeSourceForkContextRefIsIgnored(t *testing.T) {
	t.Parallel()

	root := writeClaudeCodeSubagentRecordAccountingFixture(t, `{"type":"fork-context-ref","agentId":"a1b2c3d4e5f6071","parentSessionId":"session-1","parentLastUuid":"u1","contextLength":503}`)

	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts len = %d, want 0 for fork-context-ref no-op record", len(artifacts))
	}
}

func TestExtractClaudeCodeSourceWorkflowJournalIsIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	journal := filepath.Join(root, "-repo", "session-1", "subagents", "workflows", "wf-1", "journal.jsonl")
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		t.Fatalf("mkdir workflow journal: %v", err)
	}
	body := strings.Join([]string{
		`{"type":"started","key":"v2:redacted","agentId":"a1b2c3d4e5f6071"}`,
		`{"type":"result","key":"v2:redacted","agentId":"a1b2c3d4e5f6071","result":{"status":"ok"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(journal, []byte(body), 0o600); err != nil {
		t.Fatalf("write workflow journal: %v", err)
	}

	artifacts, err := ExtractClaudeCodeSource(context.Background(), ClaudeCodeSourceOptions{
		Root:     root,
		SourceID: "claude-code:" + root,
	})
	if err != nil {
		t.Fatalf("ExtractClaudeCodeSource: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts len = %d, want 0 for workflow journal", len(artifacts))
	}
}

func writeClaudeCodeRecordAccountingFixture(t *testing.T, line string) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatalf("mkdir claude-code accounting fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write claude-code accounting transcript: %v", err)
	}
	return root
}

func writeClaudeCodeSubagentRecordAccountingFixture(t *testing.T, line string) string {
	t.Helper()

	root := t.TempDir()
	transcript := filepath.Join(root, "-repo", "session-1", "subagents", "agent-a1b2c3d4e5f6071.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatalf("mkdir claude-code subagent accounting fixture: %v", err)
	}
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write claude-code subagent accounting transcript: %v", err)
	}
	return root
}
