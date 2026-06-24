package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/parity"
)

func TestAIAgentV3IngestArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3ParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3ParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3ParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 9 {
		t.Fatalf("source artifact count = %d, want 9 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 9 {
		t.Fatalf("canonical artifact count = %d, want 9 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMSDKRequest); got != 1 {
		t.Fatalf("source llm_sdk_request count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassLLMSDKResponse); got != 1 {
		t.Fatalf("source llm_sdk_response count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassReasoningText); got != 1 {
		t.Fatalf("source reasoning_text count = %d, want 1", got)
	}
	if got := countArtifactsByAvailability(sourceArtifacts, parity.AvailabilitySourceUnavailable); got != 1 {
		t.Fatalf("source_unavailable source artifact count = %d, want 1", got)
	}
	if got := countArtifactsByAvailability(canonicalArtifacts, parity.AvailabilitySourceUnavailable); got != 1 {
		t.Fatalf("source_unavailable canonical artifact count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestErrorAndSubagentLinkArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3StructuralParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3StructuralParityArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3StructuralParityArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source artifact count = %d, want 4 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical artifact count = %d, want 4 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("source tool_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassToolError); got != 1 {
		t.Fatalf("canonical tool_error count = %d, want 1", got)
	}
	if got := countArtifactsByClass(sourceArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("source subagent_link count = %d, want 1", got)
	}
	if got := countArtifactsByClass(canonicalArtifacts, parity.ClassSubagentLink); got != 1 {
		t.Fatalf("canonical subagent_link count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 structural parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestParentOnlyChildSessionBoundaryMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3StructuralParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3SessionBoundaryArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3SessionBoundaryArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source session_boundary count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical session_boundary count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}
	if got := countArtifactsByAvailability(sourceArtifacts, parity.AvailabilityPartialSource); got != 1 {
		t.Fatalf("source partial_source session_boundary count = %d, want 1", got)
	}
	if got := countArtifactsByAvailability(canonicalArtifacts, parity.AvailabilityPartialSource); got != 1 {
		t.Fatalf("canonical partial_source session_boundary count = %d, want 1", got)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 parent-only child session boundary parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestUnresolvedNativeLineageMatchesSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3UnresolvedNativeLineageFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3LineageArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3LineageArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source lineage artifact count = %d, want 2 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical lineage artifact count = %d, want 2 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 unresolved native lineage parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestParentSideLineageEnrichesRealChildSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3ParentSideLineageParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3LineageArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3LineageArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 4 {
		t.Fatalf("source lineage artifact count = %d, want 4 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 4 {
		t.Fatalf("canonical lineage artifact count = %d, want 4 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 parent-side lineage enrichment parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestSelfReferentialToolOutputDoesNotOverrideRealLineage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, childFile := writeAIAgentV3SelfReferentialToolOutputLineageFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3SessionBoundaryArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3SessionBoundaryArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source session_boundary count = %d, want 2 from %s", len(sourceArtifacts), childFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical session_boundary count = %d, want 2 from %s", len(canonicalArtifacts), childFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 self-referential tool_output lineage parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestUncapturedPayloadUsesEnclosingOpIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3InterleavedUncapturedPayloadFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3LLMResponseArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3LLMResponseArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source llm_response count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical llm_response count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 uncaptured payload op-index parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestToolOutputSessionKindArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, childFile := writeAIAgentV3ToolOutputSessionKindFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3SessionBoundaryArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3SessionBoundaryArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source artifact count = %d, want 2 from %s", len(sourceArtifacts), childFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical artifact count = %d, want 2 from %s", len(canonicalArtifacts), childFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 tool_output session kind parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestLogArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3LogParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3LogArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3LogArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 3 {
		t.Fatalf("source artifact count = %d, want 3 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 3 {
		t.Fatalf("canonical artifact count = %d, want 3 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 log parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestSystemOpArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3SystemOpParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3SystemOpArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3SystemOpArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source system_op count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical system_op count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 system_op parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestSessionMetadataArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, childFile := writeAIAgentV3SessionMetadataParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3SessionMetadataArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3SessionMetadataArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 2 {
		t.Fatalf("source session_metadata count = %d, want 2 from %s", len(sourceArtifacts), childFile)
	}
	if len(canonicalArtifacts) != 2 {
		t.Fatalf("canonical session_metadata count = %d, want 2 from %s", len(canonicalArtifacts), childFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 session_metadata parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func TestAIAgentV3IngestCompactionEventArtifactsMatchSourceManifest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root, sessionFile := writeAIAgentV3CompactionParityFixture(t)
	sourceID := "aiagent_v3:" + root

	adapter, err := aiagent_v3.New(root, canonical.AdapterOptions{
		SourceID: sourceID,
		Logger:   silentLogger(),
		OnError: func(err error) {
			t.Fatalf("aiagent_v3 adapter error: %v", err)
		},
	})
	if err != nil {
		t.Fatalf("aiagent_v3.New: %v", err)
	}

	events := make(chan canonical.Event, 128)
	if err := adapter.Scan(ctx, nil, events); err != nil {
		t.Fatalf("aiagent_v3 Scan: %v", err)
	}
	close(events)

	_, db := openTestStore(t)
	if err := ensureSourceRowDirect(ctx, db, sourceID, "aiagent_v3", root); err != nil {
		t.Fatalf("ensure source: %v", err)
	}
	applyAIAgentV3EventsForParity(t, ctx, db, sourceID, root, events)
	if err := newResolver(db, silentLogger(), time.Minute).linkOrphans(ctx); err != nil {
		t.Fatalf("resolver linkOrphans: %v", err)
	}

	sourceArtifacts, err := parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
		Root:     root,
		SourceID: sourceID,
	})
	if err != nil {
		t.Fatalf("ExtractAIAgentV3Source: %v", err)
	}
	canonicalArtifacts, err := parity.ExtractCanonical(ctx, db)
	if err != nil {
		t.Fatalf("ExtractCanonical: %v", err)
	}

	sourceArtifacts = filterAIAgentV3CompactionEventArtifacts(sourceArtifacts)
	canonicalArtifacts = filterAIAgentV3CompactionEventArtifacts(canonicalArtifacts)
	if len(sourceArtifacts) != 1 {
		t.Fatalf("source compaction_event count = %d, want 1 from %s", len(sourceArtifacts), sessionFile)
	}
	if len(canonicalArtifacts) != 1 {
		t.Fatalf("canonical compaction_event count = %d, want 1 from %s", len(canonicalArtifacts), sessionFile)
	}

	result := parity.Diff(sourceArtifacts, canonicalArtifacts)
	if result.State != parity.StatePass {
		t.Fatalf("aiagent_v3 compaction_event parity failed: state=%s findings=%+v", result.State, result.Findings)
	}
}

func writeAIAgentV3ParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 fixture: %v", err)
	}

	sdkRequestRaw := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	sdkResponseRaw := []byte(`{"id":"msg_1","content":"hello"}`)
	reasoningRaw := []byte("think")
	toolRequestRaw := []byte(`{"path":"README.md"}`)
	sdkRequestRef := writeAIAgentV3ParityPayload(t, root, "payloads/root-session/turn-0001/sdk-request.json.gz", sdkRequestRaw)
	sdkResponseRef := writeAIAgentV3ParityPayload(t, root, "payloads/root-session/turn-0001/sdk-response.json.gz", sdkResponseRaw)
	reasoningRef := writeAIAgentV3ParityPayload(t, root, "payloads/root-session/turn-0001/reasoning.txt.gz", reasoningRaw)
	toolRequestRef := writeAIAgentV3ParityPayload(t, root, "payloads/root-session/turn-0001/tool-request.json.gz", toolRequestRaw)

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		fmt.Sprintf(`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","provider":"anthropic","model":"claude","payloadRefs":[%s,%s,%s]},{"opId":"tool-1","opIndex":2,"kind":"tool","name":"read_file","provider":"filesystem","status":"ok","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:04.000Z","payloadRefs":[%s,{"kind":"tool_response","opId":"tool-1","turn":1,"opIndex":2,"format":"json","captured":false,"truncated":false,"redacted":false}]}]}`,
			sdkRequestRef, sdkResponseRef, reasoningRef, toolRequestRef),
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3UnresolvedNativeLineageFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "child-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 unresolved lineage fixture: %v", err)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"missing-root","sessionId":"child-session","parentSessionId":"missing-parent","parentOpId":"missing-parent-op","agentId":"child-agent","callPath":"root-agent:child-agent","headendId":"sub-agent","capturePayloads":true,"attributes":{"ledgerPath":"session/child-session.jsonl"}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"missing-root","sessionId":"child-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 unresolved lineage fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3ParentSideLineageParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 parent-side lineage fixture: %v", err)
	}

	rootFile := filepath.Join(sessionDir, "root-session.jsonl")
	rootLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","agentId":"root-agent","callPath":"root-agent","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:02.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"parent-op-1","opIndex":1,"kind":"session","name":"child-agent","provider":"agent","status":"ok","startedAt":"2026-06-22T00:00:01.100Z","endedAt":"2026-06-22T00:00:01.900Z","childSessions":[{"sessionId":"child-session","originId":"root-session","parentSessionId":"root-session","parentOpId":"parent-op-1","ledgerPath":"session/child-session.jsonl","status":"ok","agentId":"child-agent","callPath":"root-agent:child-agent"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:03.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(rootFile, []byte(strings.Join(rootLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write root aiagent_v3 parent-side lineage fixture: %v", err)
	}

	childFile := filepath.Join(sessionDir, "child-session.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:01.200Z","originId":"root-session","sessionId":"child-session","agentId":"child-agent","callPath":"root-agent:child-agent","headendId":"sub-agent","capturePayloads":true,"attributes":{"ledgerPath":"session/child-session.jsonl"}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:02.500Z","originId":"root-session","sessionId":"child-session","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child aiagent_v3 parent-side lineage fixture: %v", err)
	}
	return root, childFile
}

func writeAIAgentV3SelfReferentialToolOutputLineageFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 self-referential lineage fixture: %v", err)
	}

	childFile := filepath.Join(sessionDir, "a-child-session.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:01.000Z","originId":"z-parent-session","sessionId":"a-child-session","parentSessionId":"z-parent-session","parentOpId":"parent-op-1","agentId":"child-agent","callPath":"parent-agent:child-agent","headendId":"sub-agent","capturePayloads":true,"attributes":{"ledgerPath":"session/a-child-session.jsonl"}}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:02.000Z","originId":"z-parent-session","sessionId":"a-child-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:03.000Z","originId":"z-parent-session","sessionId":"a-child-session","turn":1,"status":"ok","ops":[{"opId":"tool-output-op-1","opIndex":1,"kind":"session","name":"tool_output","provider":"tool-output","status":"running","startedAt":"2026-06-22T00:00:02.100Z","endedAt":"2026-06-22T00:00:02.900Z","childSessions":[{"sessionId":"a-child-session","originId":"z-parent-session","parentSessionId":"a-child-session","parentOpId":"tool-output-op-1","ledgerPath":"session/a-child-session.jsonl","status":"running","agentId":"tool_output","callPath":"parent-agent:child-agent:tool_output"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:04.000Z","originId":"z-parent-session","sessionId":"a-child-session","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child aiagent_v3 self-referential lineage fixture: %v", err)
	}

	parentFile := filepath.Join(sessionDir, "z-parent-session.jsonl")
	parentLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"z-parent-session","sessionId":"z-parent-session","agentId":"parent-agent","callPath":"parent-agent","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:00.500Z","originId":"z-parent-session","sessionId":"z-parent-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:01.500Z","originId":"z-parent-session","sessionId":"z-parent-session","turn":1,"status":"ok","ops":[{"opId":"parent-op-1","opIndex":1,"kind":"session","name":"child-agent","provider":"agent","status":"ok","startedAt":"2026-06-22T00:00:00.600Z","endedAt":"2026-06-22T00:00:01.400Z","childSessions":[{"sessionId":"a-child-session","originId":"z-parent-session","parentSessionId":"z-parent-session","parentOpId":"parent-op-1","ledgerPath":"session/a-child-session.jsonl","status":"ok","agentId":"child-agent","callPath":"parent-agent:child-agent"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:02.000Z","originId":"z-parent-session","sessionId":"z-parent-session","status":"ok"}`,
	}
	if err := os.WriteFile(parentFile, []byte(strings.Join(parentLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write parent aiagent_v3 self-referential lineage fixture: %v", err)
	}

	return root, childFile
}

func writeAIAgentV3InterleavedUncapturedPayloadFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 interleaved uncaptured fixture: %v", err)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":2}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":2,"status":"ok","ops":[{"opId":"llm-1","opIndex":1,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"},{"opId":"tool-output-1","opIndex":2,"kind":"session","name":"tool_output","provider":"tool-output","status":"ok","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:03.500Z"},{"opId":"tool-output-2","opIndex":3,"kind":"session","name":"tool_output","provider":"tool-output","status":"ok","startedAt":"2026-06-22T00:00:03.500Z","endedAt":"2026-06-22T00:00:04.000Z"},{"opId":"llm-2","opIndex":4,"kind":"llm","name":"message","status":"ok","startedAt":"2026-06-22T00:00:04.000Z","endedAt":"2026-06-22T00:00:05.000Z","payloadRefs":[{"kind":"llm_response","opId":"llm-2","turn":2,"opIndex":2,"format":"sse","captured":false,"truncated":false,"redacted":false}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 interleaved uncaptured fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3SessionMetadataParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 metadata fixture: %v", err)
	}

	rootFile := filepath.Join(sessionDir, "root-session.jsonl")
	rootLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","agentId":"root-agent","callPath":"root-agent","headendId":"cli","capturePayloads":true,"attributes":{"ledgerPath":"session/root-session.jsonl","tier":"prod"}}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:00.750Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"op-parent-1","opIndex":1,"kind":"session","name":"child-agent","provider":"agent","status":"ok","startedAt":"2026-06-22T00:00:00.800Z","endedAt":"2026-06-22T00:00:00.900Z","childSessions":[{"sessionId":"child-session","originId":"root-session","parentSessionId":"root-session","parentOpId":"op-parent-1","ledgerPath":"session/child-session.jsonl","status":"ok","agentId":"child-agent","callPath":"root-agent:child-agent"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:02.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(rootFile, []byte(strings.Join(rootLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write root aiagent_v3 metadata fixture: %v", err)
	}

	childFile := filepath.Join(sessionDir, "child-session.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.500Z","originId":"root-session","sessionId":"child-session","parentSessionId":"root-session","parentOpId":"op-parent-1","agentId":"child-agent","callPath":"root-agent:child-agent","headendId":"sub-agent","capturePayloads":false,"attributes":{"ledgerPath":"session/child-session.jsonl","priority":2,"nested":{"enabled":true}}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.500Z","originId":"root-session","sessionId":"child-session","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child aiagent_v3 metadata fixture: %v", err)
	}
	return root, childFile
}

func writeAIAgentV3SystemOpParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 system fixture: %v", err)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"system-1","opIndex":1,"kind":"system","name":"maintenance","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z"}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 system fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3CompactionParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 compaction fixture: %v", err)
	}

	childFile := filepath.Join(sessionDir, "compact-child.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:02.100Z","originId":"root-session","sessionId":"compact-child","parentSessionId":"root-session","parentOpId":"compact-1","agentId":"history_compaction.turn_summarizer","callPath":"root-agent:history_compaction.turn_summarizer","headendId":"history_compaction","capturePayloads":true,"attributes":{"ledgerPath":"session/compact-child.jsonl"}}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:03.100Z","originId":"root-session","sessionId":"compact-child","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child aiagent_v3 compaction fixture: %v", err)
	}

	rootFile := filepath.Join(sessionDir, "root-session.jsonl")
	rootLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","agentId":"root-agent","callPath":"root-agent","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":2}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":2,"status":"ok","ops":[{"opId":"compact-1","opIndex":3,"kind":"session","name":"history_compaction.turn_summarizer","provider":"history-compaction","status":"ok","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","childSessions":[{"sessionId":"compact-child","originId":"root-session","parentSessionId":"root-session","parentOpId":"compact-1","ledgerPath":"session/compact-child.jsonl","status":"ok","agentId":"history_compaction.turn_summarizer","callPath":"root-agent:history_compaction.turn_summarizer"}],"attributes":{"archivedTurn":1,"currentTurn":2,"name":"history_compaction.turn_summarizer","provider":"history-compaction"}}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(rootFile, []byte(strings.Join(rootLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write root aiagent_v3 compaction fixture: %v", err)
	}
	return root, rootFile
}

func writeAIAgentV3LogParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 fixture: %v", err)
	}
	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"failed","ops":[],"warnings":["slow request"],"errors":["provider failed"]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"failed","error":"session failed"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3ToolOutputSessionKindFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 session dir: %v", err)
	}

	rootFile := filepath.Join(sessionDir, "root-session.jsonl")
	rootLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:02.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(rootFile, []byte(strings.Join(rootLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write root aiagent_v3 fixture: %v", err)
	}

	childFile := filepath.Join(sessionDir, "tool-session.jsonl")
	childLines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.500Z","originId":"root-session","sessionId":"tool-session","parentSessionId":"root-session","headendId":"tool_output","capturePayloads":true}`,
		`{"version":3,"recordType":"session_summary","seq":2,"ts":"2026-06-22T00:00:01.500Z","originId":"root-session","sessionId":"tool-session","status":"ok"}`,
	}
	if err := os.WriteFile(childFile, []byte(strings.Join(childLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write child aiagent_v3 fixture: %v", err)
	}
	return root, childFile
}

func writeAIAgentV3StructuralParityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	sessionFile := filepath.Join(root, "session", "root-session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir aiagent_v3 structural fixture: %v", err)
	}

	lines := []string{
		`{"version":3,"recordType":"session_start","seq":1,"ts":"2026-06-22T00:00:00.000Z","originId":"root-session","sessionId":"root-session","headendId":"cli","capturePayloads":true}`,
		`{"version":3,"recordType":"turn_start","seq":2,"ts":"2026-06-22T00:00:01.000Z","originId":"root-session","sessionId":"root-session","turn":1}`,
		`{"version":3,"recordType":"turn_end","seq":3,"ts":"2026-06-22T00:00:05.000Z","originId":"root-session","sessionId":"root-session","turn":1,"status":"ok","ops":[{"opId":"tool-1","opIndex":1,"kind":"tool","name":"read_file","provider":"filesystem","status":"failed","startedAt":"2026-06-22T00:00:02.000Z","endedAt":"2026-06-22T00:00:03.000Z","error":"permission denied"},{"opId":"session-1","opIndex":2,"kind":"session","name":"delegate","provider":"agent","status":"ok","startedAt":"2026-06-22T00:00:03.000Z","endedAt":"2026-06-22T00:00:04.000Z","childSessions":[{"sessionId":"child-1","originId":"root-session","parentSessionId":"root-session","parentOpId":"session-1","ledgerPath":"session/child-1.jsonl","status":"ok"}]}]}`,
		`{"version":3,"recordType":"session_summary","seq":4,"ts":"2026-06-22T00:00:06.000Z","originId":"root-session","sessionId":"root-session","status":"ok"}`,
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write aiagent_v3 structural fixture: %v", err)
	}
	return root, sessionFile
}

func writeAIAgentV3ParityPayload(t *testing.T, root string, relPath string, raw []byte) string {
	t.Helper()

	compressed := gzipBytesForAIAgentV3Parity(t, raw)
	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir payload dir: %v", err)
	}
	if err := os.WriteFile(path, compressed, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	sum := sha256.Sum256(raw)
	kind := "sdk_request"
	opID := "llm-1"
	opIndex := 1
	format := "json"
	switch {
	case strings.Contains(relPath, "sdk-response"):
		kind = "sdk_response"
	case strings.Contains(relPath, "reasoning"):
		kind = "reasoning_stream"
		format = "text"
	case strings.Contains(relPath, "tool-request"):
		kind = "tool_request"
		opID = "tool-1"
		opIndex = 2
	}
	return fmt.Sprintf(`{"kind":%q,"opId":%q,"turn":1,"opIndex":%d,"format":%q,"compression":"gzip","path":%q,"originalBytes":%d,"compressedBytes":%d,"sha256":%q,"captured":true,"truncated":false,"redacted":false}`,
		kind, opID, opIndex, format, relPath, len(raw), len(compressed), fmt.Sprintf("%x", sum))
}

func gzipBytesForAIAgentV3Parity(t *testing.T, raw []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip payload: %v", err)
	}
	return buf.Bytes()
}

func applyAIAgentV3EventsForParity(t *testing.T, ctx context.Context, db *sql.DB, sourceID string, location string, events <-chan canonical.Event) {
	t.Helper()

	writer := newWriter(sourceID, "aiagent_v3", location, NopPricer{})
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

func filterAIAgentV3ParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassSessionBoundary: {},
		parity.ClassTurnBoundary:    {},
		parity.ClassOpBoundary:      {},
		parity.ClassLLMSDKRequest:   {},
		parity.ClassLLMSDKResponse:  {},
		parity.ClassReasoningText:   {},
		parity.ClassToolRequest:     {},
		parity.ClassToolResponse:    {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3StructuralParityArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassOpBoundary:   {},
		parity.ClassToolError:    {},
		parity.ClassSubagentLink: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3SessionBoundaryArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassSessionBoundary {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3LineageArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	allowed := map[parity.ArtifactClass]struct{}{
		parity.ClassSessionBoundary: {},
		parity.ClassSessionMetadata: {},
	}
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if _, ok := allowed[artifact.Class]; ok {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3LogArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassLogEntry {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3SystemOpArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassSystemOp {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3SessionMetadataArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassSessionMetadata {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3LLMResponseArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassLLMResponse {
			out = append(out, artifact)
		}
	}
	return out
}

func filterAIAgentV3CompactionEventArtifacts(artifacts []parity.Artifact) []parity.Artifact {
	out := make([]parity.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Adapter != "aiagent_v3" {
			continue
		}
		if artifact.Class == parity.ClassCompactionEvent {
			out = append(out, artifact)
		}
	}
	return out
}
