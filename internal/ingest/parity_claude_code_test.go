package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestClaudeCodeIngestInlinePayloadArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeInlineParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeInlineParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 12 {
		t.Fatalf("source artifact count = %d, want 12 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 12 {
		t.Fatalf("canonical artifact count = %d, want 12 from %s", len(canonicalArtifacts), transcript)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 5 {
		t.Fatalf("source op_boundary count = %d, want 5", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolResponse); got != 2 {
		t.Fatalf("source tool_response count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLogEntry); got != 1 {
		t.Fatalf("source exact log_entry count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code inline payload parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestCompactionEventArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeCompactionEventArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeCompactionEventArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code compaction-event parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestSubagentLinkArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeClaudeCodeSubagentParityFixture(t)
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
	resolver := newResolver(db, silentLogger(), time.Minute)
	if err := resolver.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

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

	sourceArtifacts = filterClaudeCodeSubagentLinkParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeSubagentLinkParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6", len(canonicalArtifacts))
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("source subagent_link count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code subagent-link parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestAPIErrorArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeAPIErrorParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeAPIErrorParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeAPIErrorParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 3 {
		t.Fatalf("source artifact count = %d, want 3 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 3 {
		t.Fatalf("canonical artifact count = %d, want 3 from %s", len(canonicalArtifacts), transcript)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMError); got != 1 {
		t.Fatalf("source llm_error count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code api-error parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestSystemOpArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeSystemOpParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeSystemOpParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeSystemOpParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source system_op count = %d, want 1 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical system_op count = %d, want 1 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code system-op parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestSessionMetadataArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeSessionMetadataParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeSessionMetadataParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeSessionMetadataParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source session_metadata count = %d, want 1 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical session_metadata count = %d, want 1 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code session-metadata parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestGenericLogArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeGenericLogParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeLogEntryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeLogEntryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 8 {
		t.Fatalf("source log_entry count = %d, want 8 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 8 {
		t.Fatalf("canonical log_entry count = %d, want 8 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code generic-log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestAgentToolResultArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeAgentToolResultParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeAgentToolResultParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterClaudeCodeAgentToolResultParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source Agent result artifact count = %d, want 6 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical Agent result artifact count = %d, want 6 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code Agent result parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestTurnBoundaryAtNextPromptMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeNextPromptTurnBoundaryFixture(t)
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

	sourceArtifacts = filterClaudeCodeTurnBoundaryParityArtifacts(sourceArtifacts, "session-1")
	canonicalArtifacts = filterClaudeCodeTurnBoundaryParityArtifacts(canonicalArtifacts, "session-1")
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source turn_boundary count = %d, want 2 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical turn_boundary count = %d, want 2 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code next-prompt turn parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestAgentParentResultWithChildMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeClaudeCodeAgentToolResultWithChildParityFixture(t)
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
	resolver := newResolver(db, silentLogger(), time.Minute)
	if err := resolver.linkOrphans(ctx); err != nil {
		t.Fatalf("linkOrphans: %v", err)
	}

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

	sourceArtifacts = filterClaudeCodeAgentParentResultParityArtifacts(sourceArtifacts, "parent-session")
	canonicalArtifacts = filterClaudeCodeAgentParentResultParityArtifacts(canonicalArtifacts, "parent-session")
	if len(sourceArtifacts) != 7 {
		t.Fatalf("source parent Agent artifact count = %d, want 7", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 7 {
		t.Fatalf("canonical parent Agent artifact count = %d, want 7", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code parent Agent result with child parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestClaudeCodeIngestDuplicateLogRowsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, transcript := writeClaudeCodeDuplicateLogParityFixture(t)
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

	sourceArtifacts = filterClaudeCodeDuplicateLogParityArtifacts(sourceArtifacts, "session-1")
	canonicalArtifacts = filterClaudeCodeDuplicateLogParityArtifacts(canonicalArtifacts, "session-1")
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source duplicate log artifact count = %d, want 4 from %s", len(sourceArtifacts), transcript)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical duplicate log artifact count = %d, want 4 from %s", len(canonicalArtifacts), transcript)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("claude-code duplicate log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeClaudeCodeParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(strings.Join(claudeCodeParityFixtureLines(), "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeNextPromptTurnBoundaryFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code next-prompt fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"answer"}]}}`,
		`{"type":"user","uuid":"u2","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:05.000Z","message":{"role":"user","content":"second"}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code next-prompt transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeDuplicateLogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code duplicate-log fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a_syn1","sessionId":"session-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"synthetic"}]}}`,
		`{"type":"assistant","uuid":"a_syn2","sessionId":"session-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"synthetic"}]}}`,
		`{"type":"system","subtype":"local_command","uuid":"sy1","sessionId":"session-1","content":"/clear","timestamp":"2026-06-22T00:00:03.000Z"}`,
		`{"type":"system","subtype":"local_command","uuid":"sy2","sessionId":"session-1","content":"/clear","timestamp":"2026-06-22T00:00:03.000Z"}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code duplicate-log transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeGenericLogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code generic-log fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"user","uuid":"u_meta","sessionId":"session-1","isMeta":true,"timestamp":"2026-06-22T00:00:02.000Z","message":{"role":"user","content":"<local-command-caveat>"}}`,
		`{"type":"assistant","uuid":"a_syn","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"synthetic"}]}}`,
		`{"type":"queue-operation","sessionId":"session-1","timestamp":"2026-06-22T00:00:04.000Z","operation":"enqueue","prompt":"next"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":10,"prUrl":"https://example.invalid/pr/10","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:05.000Z"}`,
		`{"type":"system","subtype":"api_error","uuid":"sy1","sessionId":"session-1","content":"provider overloaded","timestamp":"2026-06-22T00:00:06.000Z","error":{"status":529,"type":"overloaded_error","message":"overloaded"}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sys1","sessionId":"session-1","timestamp":"2026-06-22T00:00:07.000Z","compactMetadata":{"trigger":"manual","preTokens":100,"postTokens":20,"durationMs":1000}}`,
		`{"type":"user","uuid":"u_summary","sessionId":"session-1","isCompactSummary":true,"timestamp":"2026-06-22T00:00:08.000Z","message":{"role":"user","content":"summary"}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code generic-log transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeAgentToolResultParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code agent-result fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"review work","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_agent_1","content":"agent failed","is_error":true}]},"toolUseResult":{"stderr":"agent failed","exit_code":1}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code agent-result transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeAgentToolResultWithChildParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	parentSessionID := "parent-session"
	parentTranscript := filepath.Join(projectDir, parentSessionID+".jsonl")
	subagentDir := filepath.Join(projectDir, parentSessionID, "subagents")
	childTranscript := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.jsonl")
	childMeta := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.meta.json")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code agent-result child fixture: %v", err)
	}
	parentLines := []string{
		`{"type":"user","uuid":"u1","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"parent-session","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"review work","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"parent-session","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_agent_1","content":"agent failed","is_error":true}]},"toolUseResult":{"stderr":"agent failed","exit_code":1}}`,
	}
	childLines := []string{
		`{"type":"user","uuid":"cu1","parentUuid":null,"isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:04.000Z","message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","uuid":"ca1","parentUuid":"cu1","isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","requestId":"req-2","timestamp":"2026-06-22T00:00:05.000Z","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":2},"content":[{"type":"text","text":"child done"}]}}`,
	}
	if err := os.WriteFile(parentTranscript, []byte(strings.Join(parentLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code parent transcript: %v", err)
	}
	if err := os.WriteFile(childTranscript, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code child transcript: %v", err)
	}
	if err := os.WriteFile(childMeta, []byte(`{"agentType":"general-purpose","toolUseId":"toolu_agent_1"}`), 0o644); err != nil {
		t.Fatalf("write claude-code child meta: %v", err)
	}
	return root
}

func writeClaudeCodeSubagentParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	parentSessionID := "parent-session"
	parentTranscript := filepath.Join(projectDir, parentSessionID+".jsonl")
	subagentDir := filepath.Join(projectDir, parentSessionID, "subagents")
	childTranscript := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.jsonl")
	childMeta := filepath.Join(subagentDir, "agent-a1b2c3d4e5f6071.meta.json")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code subagent fixture: %v", err)
	}
	parentLines := []string{
		`{"type":"user","uuid":"u1","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"delegate"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"parent-session","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"explore repository","subagent_type":"general-purpose","prompt":"inspect"}}]}}`,
	}
	childLines := []string{
		`{"type":"user","uuid":"cu1","parentUuid":null,"isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","cwd":"/repo","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":"inspect"}}`,
		`{"type":"assistant","uuid":"ca1","parentUuid":"cu1","isSidechain":true,"agentId":"a1b2c3d4e5f6071","sessionId":"parent-session","requestId":"req-2","timestamp":"2026-06-22T00:00:05.000Z","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":4,"output_tokens":2},"content":[{"type":"text","text":"done"}]}}`,
	}
	if err := os.WriteFile(parentTranscript, []byte(strings.Join(parentLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code parent transcript: %v", err)
	}
	if err := os.WriteFile(childTranscript, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code child transcript: %v", err)
	}
	if err := os.WriteFile(childMeta, []byte(`{"agentType":"general-purpose","toolUseId":"toolu_agent_1"}`), 0o644); err != nil {
		t.Fatalf("write claude-code child meta: %v", err)
	}
	return root
}

func writeClaudeCodeAPIErrorParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code api-error fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	if err := os.WriteFile(transcript, []byte(strings.Join(claudeCodeAPIErrorFixtureLines(), "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code api-error transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeSystemOpParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code system-op fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"system","subtype":"local_command","uuid":"sy1","sessionId":"session-1","content":"/clear","timestamp":"2026-06-22T00:00:02.000Z"}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code system-op transcript: %v", err)
	}
	return root, transcript
}

func writeClaudeCodeSessionMetadataParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	projectDir := filepath.Join(root, "-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir claude-code metadata fixture: %v", err)
	}
	transcript := filepath.Join(projectDir, "session-1.jsonl")
	lines := []string{
		`{"type":"last-prompt","lastPrompt":"draft prompt","leafUuid":"u0","sessionId":"session-1"}`,
		`{"type":"permission-mode","permissionMode":"acceptEdits","sessionId":"session-1"}`,
		`{"type":"custom-title","customTitle":"Pinned title","sessionId":"session-1"}`,
		`{"type":"ai-title","aiTitle":"AI title","sessionId":"session-1"}`,
		`{"type":"bridge-session","sessionId":"session-1","bridgeSessionId":"cse_fixture","lastSequenceNum":42}`,
		`{"type":"file-history-snapshot","messageId":"u1","sessionId":"session-1","snapshot":{"messageId":"u1","trackedFileBackups":{"README.md":{"backupFileName":"README.md.bak","version":2}},"timestamp":"2026-06-22T00:00:00.000Z"},"isSnapshotUpdate":true}`,
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"last-prompt","lastPrompt":"final prompt","leafUuid":"u1","sessionId":"session-1"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":10,"prUrl":"https://example.invalid/pr/10","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:02.000Z"}`,
		`{"type":"pr-link","sessionId":"session-1","prNumber":11,"prUrl":"https://example.invalid/pr/11","prRepository":"owner/repo","timestamp":"2026-06-22T00:00:03.000Z"}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude-code metadata transcript: %v", err)
	}
	return root, transcript
}

func claudeCodeParityFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"session-1","requestId":"req-1","timestamp":"2026-06-22T00:00:02.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"answer"},{"type":"thinking","thinking":"think","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"session-1","timestamp":"2026-06-22T00:00:03.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file body","is_error":false}]},"toolUseResult":{"stdout":"file body","exit_code":0}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sys1","sessionId":"session-1","timestamp":"2026-06-22T00:00:04.000Z","compactMetadata":{"trigger":"manual","preTokens":100,"postTokens":20,"durationMs":1000,"preservedSegment":{"headUuid":"u1","anchorUuid":"a1","tailUuid":"u3"},"preservedMessages":{"anchorUuid":"a1","uuids":["u1","a1","u3"]}}}`,
		`{"type":"user","uuid":"u3","sessionId":"session-1","isCompactSummary":true,"timestamp":"2026-06-22T00:00:05.000Z","message":{"role":"user","content":"summary"}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sys2","sessionId":"session-1","durationMs":5000,"timestamp":"2026-06-22T00:00:06.000Z"}`,
	}
}

func claudeCodeAPIErrorFixtureLines() []string {
	return []string{
		`{"type":"user","uuid":"u1","sessionId":"session-1","cwd":"/repo","timestamp":"2026-06-22T00:00:01.000Z","message":{"role":"user","content":"question"}}`,
		`{"type":"system","subtype":"api_error","uuid":"sy1","sessionId":"session-1","content":"provider overloaded","timestamp":"2026-06-22T00:00:02.000Z","error":{"status":529,"type":"overloaded_error","message":"overloaded","requestID":"req_1"},"retryInMs":38317.38269012852,"retryAttempt":1}`,
	}
}

func applyClaudeCodeEventsForParity(t *testing.T, ctx context.Context, db *sql.DB, sourceID string, location string, events <-chan canonical.Event) {
	t.Helper()

	writer := newWriter(sourceID, "claude-code", location, NopPricer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	for event := range events {
		if err := writer.apply(ctx, tx, event); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %T: %v", event, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func filterClaudeCodeInlineParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:       {},
		parity.ClassUserPrompt:       {},
		parity.ClassAssistantMessage: {},
		parity.ClassReasoningText:    {},
		parity.ClassToolRequest:      {},
		parity.ClassToolResponse:     {},
		parity.ClassLogEntry:         {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; !ok {
			continue
		}
		if artifact.Class == parity.ClassLogEntry && !strings.HasSuffix(artifact.NativeArtifactID, ":/message/content") {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

func filterClaudeCodeAPIErrorParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary: {},
		parity.ClassLLMError:   {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; !ok || artifact.Adapter != "claude-code" {
			continue
		}
		if artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID != "op:1:1" && artifact.NativeArtifactID != "op:1:2" {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

func filterClaudeCodeCompactionEventArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.Class == parity.ClassCompactionEvent {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeSystemOpParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.Class == parity.ClassSystemOp {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeSessionMetadataParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.Class == parity.ClassSessionMetadata {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeLogEntryParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.Class == parity.ClassLogEntry {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeAgentToolResultParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:   {},
		parity.ClassToolResponse: {},
		parity.ClassToolError:    {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" {
			continue
		}
		if _, ok := allowed[artifact.Class]; !ok {
			continue
		}
		if artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID != "op:1:1" && artifact.NativeArtifactID != "op:1:2" && artifact.NativeArtifactID != "op:1:3" {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

func filterClaudeCodeTurnBoundaryParityArtifacts(artifacts []parity.Artifact, nativeSessionID string) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter == "claude-code" && artifact.NativeSessionID == nativeSessionID && artifact.Class == parity.ClassTurnBoundary {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeAgentParentResultParityArtifacts(artifacts []parity.Artifact, nativeSessionID string) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:   {},
		parity.ClassSubagentLink: {},
		parity.ClassToolResponse: {},
		parity.ClassToolError:    {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" || artifact.NativeSessionID != nativeSessionID {
			continue
		}
		if _, ok := allowed[artifact.Class]; !ok {
			continue
		}
		if artifact.Class == parity.ClassOpBoundary && artifact.NativeArtifactID != "op:1:1" && artifact.NativeArtifactID != "op:1:2" && artifact.NativeArtifactID != "op:1:3" {
			continue
		}
		out = append(out, artifact)
	}
	return out
}

func filterClaudeCodeDuplicateLogParityArtifacts(artifacts []parity.Artifact, nativeSessionID string) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassLogEntry: {},
		parity.ClassSystemOp: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "claude-code" || artifact.NativeSessionID != nativeSessionID {
			continue
		}
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterClaudeCodeSubagentLinkParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:   {},
		parity.ClassSubagentLink: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok && artifact.Adapter == "claude-code" {
			out = append(out, artifact)
		}
	}
	return out
}
