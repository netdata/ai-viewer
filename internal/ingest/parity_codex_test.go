package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/codex"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestCodexIngestArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 18 {
		t.Fatalf("source artifact count = %d, want 18 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 18 {
		t.Fatalf("canonical artifact count = %d, want 18 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSessionBoundary); got != 1 {
		t.Fatalf("source session_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 1 {
		t.Fatalf("source turn_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 6 {
		t.Fatalf("source op_boundary count = %d, want 6", got)
	}
	if got := countArtifactsByAvailability(sourceArtifacts, parity.AvailabilitySourceEmpty); got != 4 {
		t.Fatalf("source_empty source artifact count = %d, want 4", got)
	}
	if got := countArtifactsByAvailability(canonicalArtifacts, parity.AvailabilitySourceEmpty); got != 4 {
		t.Fatalf("source_empty canonical artifact count = %d, want 4", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex payload parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestDirectResponseItemsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexDirectResponseItemParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 11 {
		t.Fatalf("source artifact count = %d, want 11 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 11 {
		t.Fatalf("canonical artifact count = %d, want 11 from %s; by class=%s", len(canonicalArtifacts), sessionFile, artifactClassCounts(canonicalArtifacts))
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 4 {
		t.Fatalf("source op_boundary count = %d, want 4", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex direct response-item parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestUserImageArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexUserImageParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 64)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexUserImageParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexUserImageParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s; by class=%s", len(sourceArtifacts), sessionFile, artifactClassCounts(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s; by class=%s", len(canonicalArtifacts), sessionFile, artifactClassCounts(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex user-image parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestLegacyJSONLSessionHeaderMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexLegacyJSONLSessionHeaderParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 16)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex legacy jsonl session-header parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestSessionMetadataArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexSessionMetadataParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 32)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexSessionMetadataParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexSessionMetadataParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s; by class=%s", len(sourceArtifacts), sessionFile, artifactClassCounts(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s; by class=%s", len(canonicalArtifacts), sessionFile, artifactClassCounts(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex session metadata parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestLegacyFlatJSONMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexLegacyFlatJSONParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 11 {
		t.Fatalf("source artifact count = %d, want 11 from %s; by class=%s", len(sourceArtifacts), sessionFile, artifactClassCounts(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 11 {
		t.Fatalf("canonical artifact count = %d, want 11 from %s; by class=%s", len(canonicalArtifacts), sessionFile, artifactClassCounts(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex legacy flat JSON parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestNewFormatTurnArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexNewFormatParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSessionBoundary); got != 1 {
		t.Fatalf("source session_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 1 {
		t.Fatalf("source turn_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 2 {
		t.Fatalf("source op_boundary count = %d, want 2", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex new-format parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestAbortedTurnArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexAbortedTurnParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 1 {
		t.Fatalf("source turn_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 2 {
		t.Fatalf("source op_boundary count = %d, want 2", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex aborted-turn parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestTaskCompleteDanglingToolMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexTaskCompleteDanglingToolParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source artifact count = %d, want 4 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical artifact count = %d, want 4 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 1 {
		t.Fatalf("source op_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolRequest); got != 1 {
		t.Fatalf("source tool_request count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex task-complete dangling-tool parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestTurnAbortedDanglingToolMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexTurnAbortedDanglingToolParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source artifact count = %d, want 4 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical artifact count = %d, want 4 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 1 {
		t.Fatalf("source op_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolRequest); got != 1 {
		t.Fatalf("source tool_request count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex turn-aborted dangling-tool parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestOldFormatMultipleTurnsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexOldFormatMultiTurnParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 11 {
		t.Fatalf("source artifact count = %d, want 11 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 11 {
		t.Fatalf("canonical artifact count = %d, want 11 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 2 {
		t.Fatalf("source turn_boundary count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 4 {
		t.Fatalf("source op_boundary count = %d, want 4", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex old-format multi-turn parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestTaskStartedReplacementMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexTaskStartedReplacementParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 11 {
		t.Fatalf("source artifact count = %d, want 11 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 11 {
		t.Fatalf("canonical artifact count = %d, want 11 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 2 {
		t.Fatalf("source turn_boundary count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 4 {
		t.Fatalf("source op_boundary count = %d, want 4", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex task-started replacement parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestStaleNewFormatEOFMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexUnfinishedNewFormatParityFixture(t, "2025", "11", "25", "stale")
	sourceID := "codex:" + root
	staleMtime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(sessionFile, staleMtime, staleMtime); err != nil {
		t.Fatalf("set stale mtime: %v", err)
	}

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex stale EOF parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestFreshNewFormatEOFMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexUnfinishedNewFormatParityFixture(t, "2025", "11", "26", "fresh")
	sourceID := "codex:" + root
	freshMtime := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	if err := os.Chtimes(sessionFile, freshMtime, freshMtime); err != nil {
		t.Fatalf("set fresh mtime: %v", err)
	}

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex fresh EOF parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestSubTurnSplitMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexSubTurnSplitParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 11 {
		t.Fatalf("source artifact count = %d, want 11 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 11 {
		t.Fatalf("canonical artifact count = %d, want 11 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassTurnBoundary); got != 2 {
		t.Fatalf("source turn_boundary count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 4 {
		t.Fatalf("source op_boundary count = %d, want 4", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex sub-turn parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestWebSearchFIFOMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexWebSearchFIFOParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 6 {
		t.Fatalf("source artifact count = %d, want 6 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 6 {
		t.Fatalf("canonical artifact count = %d, want 6 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 2 {
		t.Fatalf("source op_boundary count = %d, want 2", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolRequest); got != 2 {
		t.Fatalf("source tool_request count = %d, want 2", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex web-search FIFO parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestCollabSpawnLinkMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, parentFile := writeCodexCollabSpawnParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexSubagentLinkParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexSubagentLinkParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), parentFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), parentFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 1 {
		t.Fatalf("source op_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("source subagent_link count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex collab-spawn link parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestNestedSubagentSessionBoundariesMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeCodexNestedSubagentParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	rootID := canonicalSessionID(sourceID, "root-session")
	childID := canonicalSessionID(sourceID, "child-session")
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != rootID {
		t.Fatalf("nested child root_session_id = %q, want top-level root %q", got, rootID)
	}

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexSessionBoundaryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexSessionBoundaryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 3 {
		t.Fatalf("source session_boundary count = %d, want 3", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 3 {
		t.Fatalf("canonical session_boundary count = %d, want 3", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex nested session boundary parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestAbsentParentSessionBoundaryUsesStashedNativeLineage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := writeCodexAbsentParentSessionBoundaryFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	childID := canonicalSessionID(sourceID, "child-session")
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE id=?`, childID); got != "" {
		t.Fatalf("child parent_session_id = %q, want unresolved absent parent", got)
	}
	if got := scanString(t, db, `SELECT root_session_id FROM sessions WHERE id=?`, childID); got != childID {
		t.Fatalf("child root_session_id = %q, want self while parent row is absent", got)
	}

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexSessionBoundaryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexSessionBoundaryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source session_boundary count = %d, want 1", len(sourceArtifacts))
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical session_boundary count = %d, want 1", len(canonicalArtifacts))
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex absent-parent session boundary parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestCollabLifecycleLogsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexCollabLifecycleLogParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexLogEntryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexLogEntryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex collab lifecycle log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestDefaultEventLogsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexDefaultEventLogParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexLogEntryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexLogEntryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex default event log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestSystemOpArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexDefaultEventLogParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexSystemOpParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexSystemOpParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex system-op parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestCompactionLogArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexCompactionLogParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexLogEntryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexLogEntryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex compaction log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}

	sourceArtifacts, err = parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource for compaction event: %v", err)
	}
	canonicalArtifacts, err = parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical for compaction event: %v", err)
	}
	sourceArtifacts = filterCodexCompactionEventParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexCompactionEventParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source compaction_event count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical compaction_event count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result = parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex compaction event parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestLoneContextCompactedMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexLoneContextCompactedParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source artifact count = %d, want 4 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical artifact count = %d, want 4 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex lone context_compacted parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestAgentMessageDoesNotDuplicateAssistantArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexAgentMessageParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexAssistantMessageParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexAssistantMessageParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex agent_message dedup parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestEventReasoningDoesNotDuplicateReasoningArtifact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexEventReasoningParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexReasoningTextParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexReasoningTextParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex event reasoning dedup parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestPatchApplyEndErrorMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexPatchApplyEndParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexToolErrorParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexToolErrorParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 1 {
		t.Fatalf("source op_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex patch_apply_end error parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestMcpToolCallEndMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexMcpToolCallEndParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexOpBoundaryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexOpBoundaryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex mcp_tool_call_end parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestImageGenerationEndMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexImageGenerationEndParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexOpBoundaryParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexOpBoundaryParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source artifact count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical artifact count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex image_generation_end parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestCodexIngestExecCommandEndErrorMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeCodexExecCommandEndParityFixture(t)
	sourceID := "codex:" + root

	adapter, err := codex.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("codex adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("codex.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("codex Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "codex", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyEventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractCodexSource: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterCodexToolErrorParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterCodexToolErrorParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassOpBoundary); got != 1 {
		t.Fatalf("source op_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("codex exec_command_end error parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeCodexParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T10-00-00-019aa234.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"please list files"}]}}`,
		`{"timestamp":"2025-11-20T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will inspect the directory."}]}}`,
		`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Need inspect directory"}]}}`,
		`{"timestamp":"2025-11-20T16:59:14.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"call-1"}}`,
		`{"timestamp":"2025-11-20T16:59:15.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"file.txt"}}`,
		`{"timestamp":"2025-11-20T16:59:16.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}}`,
		`{"timestamp":"2025-11-20T16:59:17.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"","call_id":"call-2"}}`,
		`{"timestamp":"2025-11-20T16:59:18.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-2","output":""}}`,
		`{"timestamp":"2025-11-20T16:59:19.000Z","type":"event_msg","payload":{"type":"error","message":"sandbox denied"}}`,
		`{"timestamp":"2025-11-20T16:59:20.000Z","type":"event_msg","payload":{"type":"error","message":""}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexDirectResponseItemParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "20", "rollout-2025-11-20T11-00-00-direct.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-20T17:59:09.857Z","type":"session_meta","id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}`,
		`{"timestamp":"2025-11-20T17:59:10.000Z","type":"turn_context","turn_id":"turn-1","model":"gpt-5.1-codex-max"}`,
		`{"timestamp":"2025-11-20T17:59:11.000Z","type":"message","role":"user","content":[{"type":"input_text","text":"direct prompt"}]}`,
		`{"timestamp":"2025-11-20T17:59:12.000Z","type":"message","role":"assistant","content":[{"type":"output_text","text":"direct answer"}]}`,
		`{"timestamp":"2025-11-20T17:59:13.000Z","type":"reasoning","summary":[{"type":"summary_text","text":"direct think"}]}`,
		`{"timestamp":"2025-11-20T17:59:14.000Z","type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"call-1"}`,
		`{"timestamp":"2025-11-20T17:59:15.000Z","type":"function_call_output","call_id":"call-1","output":"direct file.txt"}`,
		`{"timestamp":"2025-11-20T17:59:16.000Z","type":"ghost_snapshot","data":{}}`,
		`{"record_type":"state"}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexUserImageParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2026", "07", "13", "rollout-user-image.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-13T00:00:00.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2026-07-13T00:00:01.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2026-07-13T00:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect this"},{"type":"input_image","image_url":"file:///tmp/screenshot.png"}]}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexLegacyJSONLSessionHeaderParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "09", "10", "rollout-2025-09-10T19-21-08-legacy.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-09-10T19:21:08Z","id":"legacy-session","instructions":null,"git":{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/example.git"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexLegacyFlatJSONParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "rollout-2025-06-26-11111111-2222-3333-4444-555555555555.json")
	body := `{"session":{"timestamp":"2025-06-26T00:00:00Z","id":"session-1"},"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"prompt"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]},{"type":"local_shell_call","call_id":"call-1","action":{"cmd":"ls"}},{"type":"local_shell_call_output","call_id":"call-1","output":"done"}]}`
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex legacy flat fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexSessionMetadataParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2026", "07", "12", "rollout-metadata.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2026-07-12T00:00:00.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"/workspace/project","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec","git":{"commit_hash":"abc123","branch":"main","repository_url":"git@github.com:example/project.git"}}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexSubTurnSplitParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "27", "rollout-2025-11-27T10-00-00-subturn.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-27T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-27T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-27T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-27T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		`{"timestamp":"2025-11-27T16:59:13.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2025-11-27T16:59:20.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request"}]}}`,
		`{"timestamp":"2025-11-27T16:59:21.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,
		`{"timestamp":"2025-11-27T16:59:30.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-11-27T16:59:30.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexWebSearchFIFOParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "30", "rollout-2025-11-30T10-00-00-web-search.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-30T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-30T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-30T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-30T16:59:12.000Z","type":"response_item","payload":{"type":"web_search_call","action":{"type":"search","query":"alpha"}}}`,
		`{"timestamp":"2025-11-30T16:59:13.000Z","type":"response_item","payload":{"type":"web_search_call","action":{"type":"open_page","url":"https://example.invalid/p"}}}`,
		`{"timestamp":"2025-11-30T16:59:14.000Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"end-1","query":"alpha","action":{"type":"search","query":"alpha"}}}`,
		`{"timestamp":"2025-11-30T16:59:15.000Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"end-2","action":{"type":"open_page","url":"https://example.invalid/p"}}}`,
		`{"timestamp":"2025-11-30T16:59:16.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-11-30T16:59:16.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexCollabSpawnParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	parentFile := filepath.Join(root, "2025", "12", "01", "rollout-parent.jsonl")
	childFile := filepath.Join(root, "2025", "12", "01", "rollout-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(parentFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	parentLines := []string{
		`{"timestamp":"2025-12-01T16:59:09.000Z","type":"session_meta","payload":{"id":"parent-session","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-01T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-01T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-01T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"parent-session","new_thread_id":"child-session","new_agent_nickname":"Tesla","new_agent_role":"explorer","model":"gpt-5.1-codex-max","reasoning_effort":"high","status":"completed"}}`,
		`{"timestamp":"2025-12-01T16:59:13.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-01T16:59:13.000Z"}}`,
	}
	childSource := `{"subagent":{"thread_spawn":{"parent_thread_id":"parent-session","depth":1,"agent_nickname":"Tesla","agent_role":"explorer"}}}`
	childLines := []string{
		`{"timestamp":"2025-12-01T17:00:00.000Z","type":"session_meta","payload":{"id":"child-session","originator":"codex_exec","agent_nickname":"Tesla","agent_role":"explorer","thread_source":"subagent","source":` + childSource + `}}`,
	}
	if err := os.WriteFile(parentFile, []byte(strings.Join(parentLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write parent codex fixture: %v", err)
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child codex fixture: %v", err)
	}
	return root, parentFile
}

func writeCodexNestedSubagentParityFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	shard := filepath.Join(root, "2025", "12", "01")
	rootFile := filepath.Join(shard, "rollout-1-root.jsonl")
	parentFile := filepath.Join(shard, "rollout-2-parent.jsonl")
	childFile := filepath.Join(shard, "rollout-3-child.jsonl")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	rootLines := []string{
		`{"timestamp":"2025-12-01T16:59:09.000Z","type":"session_meta","payload":{"id":"root-session","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-01T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-root","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-01T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-root"}}`,
		`{"timestamp":"2025-12-01T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"root-session","new_thread_id":"parent-session","new_agent_nickname":"Planner","new_agent_role":"planner","model":"gpt-5.1-codex-max","reasoning_effort":"high","status":"completed"}}`,
		`{"timestamp":"2025-12-01T16:59:13.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-root","completed_at":"2025-12-01T16:59:13.000Z"}}`,
	}
	parentSource := `{"subagent":{"thread_spawn":{"parent_thread_id":"root-session","depth":1,"agent_nickname":"Planner","agent_role":"planner"}}}`
	parentLines := []string{
		`{"timestamp":"2025-12-01T17:00:00.000Z","type":"session_meta","payload":{"id":"parent-session","originator":"codex_exec","agent_nickname":"Planner","agent_role":"planner","thread_source":"subagent","source":` + parentSource + `}}`,
		`{"timestamp":"2025-12-01T17:00:01.000Z","type":"turn_context","payload":{"turn_id":"turn-parent","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-01T17:00:02.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-parent"}}`,
		`{"timestamp":"2025-12-01T17:00:03.000Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"parent-session","new_thread_id":"child-session","new_agent_nickname":"Verifier","new_agent_role":"reviewer","model":"gpt-5.1-codex-max","reasoning_effort":"high","status":"completed"}}`,
		`{"timestamp":"2025-12-01T17:00:04.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-parent","completed_at":"2025-12-01T17:00:04.000Z"}}`,
	}
	childSource := `{"subagent":{"thread_spawn":{"parent_thread_id":"parent-session","depth":2,"agent_nickname":"Verifier","agent_role":"reviewer"}}}`
	childLines := []string{
		`{"timestamp":"2025-12-01T17:01:00.000Z","type":"session_meta","payload":{"id":"child-session","originator":"codex_exec","agent_nickname":"Verifier","agent_role":"reviewer","thread_source":"subagent","source":` + childSource + `}}`,
	}
	for path, lines := range map[string][]string{
		rootFile:   rootLines,
		parentFile: parentLines,
		childFile:  childLines,
	} {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write codex fixture %s: %v", path, err)
		}
	}
	return root
}

func writeCodexAbsentParentSessionBoundaryFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "01", "rollout-absent-parent-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-01T17:00:00.000Z","type":"session_meta","payload":{"id":"child-session","parent_thread_id":"parent-session","originator":"codex_exec","agent_nickname":"Verifier","agent_role":"reviewer","thread_source":"subagent","source":{"subagent":"review"}}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root
}

func writeCodexPatchApplyEndParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "02", "rollout-patch.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-02T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-02T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-02T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-02T16:59:12.000Z","type":"response_item","payload":{"type":"function_call","name":"apply_patch","call_id":"patch-1","arguments":"{}"}}`,
		`{"timestamp":"2025-12-02T16:59:13.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"patch-1","success":false,"status":"failed"}}`,
		`{"timestamp":"2025-12-02T16:59:14.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-02T16:59:14.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexCollabLifecycleLogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "05", "rollout-collab-logs.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-05T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-05T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-05T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-05T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_close_end","message":"child session closed"}}`,
		`{"timestamp":"2025-12-05T16:59:13.000Z","type":"event_msg","payload":{"type":"collab_waiting_end","message":"child session resumed"}}`,
		`{"timestamp":"2025-12-05T16:59:15.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-05T16:59:15.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexDefaultEventLogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "10", "rollout-default-event-logs.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-10T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-10T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-10T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-10T16:59:12.000Z","type":"event_msg","payload":{"type":"thread_goal_updated"}}`,
		`{"timestamp":"2025-12-10T16:59:13.000Z","type":"event_msg","payload":{"type":"view_image_tool_call"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexCompactionLogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "07", "rollout-compaction-log.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-07T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-07T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-07T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-07T16:59:12.000Z","type":"compacted","payload":{"message":"summary of the conversation","replacement_history":[{"type":"message","role":"user","content":[]}]}}`,
		`{"timestamp":"2025-12-07T16:59:12.000Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
		`{"timestamp":"2025-12-07T16:59:15.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-07T16:59:15.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexLoneContextCompactedParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "11", "rollout-context-compacted.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-11T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-11T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-11T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-11T16:59:12.000Z","type":"event_msg","payload":{"type":"context_compacted"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexAgentMessageParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "08", "rollout-agent-message.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-08T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-08T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-08T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
		`{"timestamp":"2025-12-08T16:59:12.000Z","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}`,
		`{"timestamp":"2025-12-08T16:59:13.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-08T16:59:13.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexEventReasoningParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "09", "rollout-event-reasoning.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-09T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-09T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-09T16:59:11.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"durable"}]}}`,
		`{"timestamp":"2025-12-09T16:59:12.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"visible summary"}}`,
		`{"timestamp":"2025-12-09T16:59:13.000Z","type":"event_msg","payload":{"type":"agent_reasoning_raw_content","text":"raw cot"}}`,
		`{"timestamp":"2025-12-09T16:59:14.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-09T16:59:14.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexExecCommandEndParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "03", "rollout-exec.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-03T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-03T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-03T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-03T16:59:12.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"exec-1","arguments":"{}"}}`,
		`{"timestamp":"2025-12-03T16:59:13.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"exec-1","exit_code":2,"aggregated_output":"done","duration":{"secs":0,"nanos":500000000}}}`,
		`{"timestamp":"2025-12-03T16:59:14.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"exec-1","output":"done"}}`,
		`{"timestamp":"2025-12-03T16:59:15.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-03T16:59:15.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexMcpToolCallEndParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "04", "rollout-mcp.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-04T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-04T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-04T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-04T16:59:12.000Z","type":"response_item","payload":{"type":"function_call","name":"github.list","call_id":"mcp-1","arguments":"{}"}}`,
		`{"timestamp":"2025-12-04T16:59:13.000Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"mcp-1","invocation":{"server":"github","tool":"list"},"result":{"Ok":{"is_error":false}}}}`,
		`{"timestamp":"2025-12-04T16:59:15.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-04T16:59:15.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexImageGenerationEndParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "12", "06", "rollout-image-generation.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-12-06T16:59:09.000Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-12-06T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-12-06T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-12-06T16:59:12.000Z","type":"response_item","payload":{"type":"image_generation_call","call_id":"img-1","status":"in_progress","revised_prompt":"draw a chart"}}`,
		`{"timestamp":"2025-12-06T16:59:13.000Z","type":"event_msg","payload":{"type":"image_generation_end","call_id":"img-1"}}`,
		`{"timestamp":"2025-12-06T16:59:15.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-12-06T16:59:15.000Z"}}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexUnfinishedNewFormatParityFixture(t *testing.T, year string, month string, day string, slug string) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, year, month, day, "rollout-"+year+"-"+month+"-"+day+"T10-00-00-"+slug+".jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"` + year + `-` + month + `-` + day + `T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"` + year + `-` + month + `-` + day + `T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"` + year + `-` + month + `-` + day + `T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"` + year + `-` + month + `-` + day + `T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"unfinished prompt"}]}}`,
		`{"timestamp":"` + year + `-` + month + `-` + day + `T16:59:13.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"unfinished answer"}]}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexTaskStartedReplacementParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "24", "rollout-2025-11-24T10-00-00-019aa238.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-24T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-24T16:59:10.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-24T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first prompt"}]}}`,
		`{"timestamp":"2025-11-24T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2025-11-24T16:59:20.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2"}}`,
		`{"timestamp":"2025-11-24T16:59:21.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second prompt"}]}}`,
		`{"timestamp":"2025-11-24T16:59:22.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,
		`{"timestamp":"2025-11-24T16:59:23.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-2","completed_at":"2025-11-24T16:59:30.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexOldFormatMultiTurnParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "23", "rollout-2025-11-23T10-00-00-019aa237.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-23T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.80.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-23T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-23T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first prompt"}]}}`,
		`{"timestamp":"2025-11-23T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
		`{"timestamp":"2025-11-23T16:59:20.000Z","type":"turn_context","payload":{"turn_id":"turn-2","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-23T16:59:21.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second prompt"}]}}`,
		`{"timestamp":"2025-11-23T16:59:22.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexAbortedTurnParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "22", "rollout-2025-11-22T10-00-00-019aa236.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-22T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-22T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-22T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-22T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"stop soon"}]}}`,
		`{"timestamp":"2025-11-22T16:59:13.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}`,
		`{"timestamp":"2025-11-22T16:59:14.000Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted","completed_at":"2025-11-22T16:59:20.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexTaskCompleteDanglingToolParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "28", "rollout-2025-11-28T10-00-00-dangling-complete.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-28T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-28T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-28T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-28T16:59:12.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"sleep 1\"}","call_id":"call-1"}}`,
		`{"timestamp":"2025-11-28T16:59:13.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-11-28T16:59:20.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexTurnAbortedDanglingToolParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "29", "rollout-2025-11-29T10-00-00-dangling-abort.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-29T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-29T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-29T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-29T16:59:12.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"sleep 1\"}","call_id":"call-1"}}`,
		`{"timestamp":"2025-11-29T16:59:13.000Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted","completed_at":"2025-11-29T16:59:20.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func writeCodexNewFormatParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "2025", "11", "21", "rollout-2025-11-21T10-00-00-019aa235.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir codex fixture: %v", err)
	}
	lines := []string{
		`{"timestamp":"2025-11-21T16:59:09.857Z","type":"session_meta","payload":{"id":"session-1","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":"exec"}}`,
		`{"timestamp":"2025-11-21T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max"}}`,
		`{"timestamp":"2025-11-21T16:59:11.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2025-11-21T16:59:12.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"say hi"}]}}`,
		`{"timestamp":"2025-11-21T16:59:13.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		`{"timestamp":"2025-11-21T16:59:14.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"2025-11-21T16:59:20.000Z"}}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(sessionFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}
	return root, sessionFile
}

func applyEventsForParity(t *testing.T, ctx context.Context, db *sql.DB, sourceID string, location string, events <-chan canonical.Event) {
	t.Helper()

	writer := newWriter(sourceID, "codex", location, NopPricer{})
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

func filterCodexParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassSessionBoundary:  {},
		parity.ClassTurnBoundary:     {},
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
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexSessionMetadataParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassSessionMetadata {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexSystemOpParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassSystemOp {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexUserImageParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassUserImage {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexSubagentLinkParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:   {},
		parity.ClassSubagentLink: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexSessionBoundaryParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Class == parity.ClassSessionBoundary {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexOpBoundaryParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexLogEntryParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassLogEntry: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexCompactionEventParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassCompactionEvent: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexAssistantMessageParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassAssistantMessage: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexReasoningTextParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassReasoningText: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterCodexToolErrorParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary: {},
		parity.ClassToolError:  {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func countArtifactsByClass(artifacts []parity.Artifact, class parity.ArtifactClass) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Class == class {
			count++
		}
	}
	return count
}

func countArtifactsByAvailability(artifacts []parity.Artifact, availability parity.Availability) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.Availability == availability {
			count++
		}
	}
	return count
}

func artifactClassCounts(artifacts []parity.Artifact) string {
	counts := map[parity.ArtifactClass]int{}
	for _, artifact := range artifacts {
		counts[artifact.Class]++
	}
	return strings.TrimPrefix(strings.TrimSuffix(strings.ReplaceAll(strings.Trim(fmt.Sprint(counts), "map[]"), " ", ","), ","), ",")
}
