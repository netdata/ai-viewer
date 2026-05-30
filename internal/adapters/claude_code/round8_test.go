package claude_code

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestScan_LateMetaRepairsChildWhenTranscriptsAlreadyConsumed pins P1.8 (SOW-0003
// Round 8): the late-meta repair must fire from SCAN, not only Tail. A `.meta.json`
// that first appears AFTER its parent + child transcripts were already consumed (the
// cursor sits past them, so a re-scan re-reads zero transcript bytes) — e.g. it was
// created while the daemon was stopped — must still repair the child session by
// emitting a catalog-safe SessionUpdatedEvent{AgentName, aiViewer.toolUseId}. Before
// the fix scanAll recorded the new meta hash into the cursor via withMetaSeen WITHOUT
// emitting the repair, and the subsequent Tail's flushChangedMetas skips metas already
// in metaSeen — so the child would NEVER get its AgentName / toolUseId, and the
// resolver could never link the op→child edge.
//
// Without the scan-side repairChangedMetas call this test FAILS (no SessionUpdated is
// emitted on scan #2).
func TestScan_LateMetaRepairsChildWhenTranscriptsAlreadyConsumed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := childNativeID(durParentSession, durAgentID)

	// Scan #1: parent transcript (with its Agent op) AND the child sidechain exist;
	// the child's `.meta.json` does NOT (so the child SessionStarted is emitted with
	// an empty AgentName / no toolUseId stash). Written inline — NOT via
	// writeParentWithAgentOp, which would also create the meta and break the premise.
	proj := filepath.Join(root, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, durParentSession+".jsonl"), []byte(strings.Join([]string{
		`{"type":"user","uuid":"pu1","sessionId":"` + durParentSession + `","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"pa1","sessionId":"` + durParentSession + `","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"` + parentAgentToolUseID + `","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	}, "\n")+"\n"))
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a1, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	out1 := make(chan canonical.Event, 256)
	if err := a1.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	events1 := drainBuffered(out1)
	cs, ok := sessionStartedByNative(events1, child)
	if !ok {
		t.Fatalf("child session %q not started during Scan #1", child)
	}
	if cs.AgentName != "" || extrasToolUseID(cs.Extras) != "" {
		t.Fatalf("child carried AgentName=%q toolUseId=%q before its meta exists; test premise broken",
			cs.AgentName, extrasToolUseID(cs.Extras))
	}
	parsed1, err := ParseCursor(lastCursor(t, events1))
	if err != nil {
		t.Fatalf("ParseCursor #1: %v", err)
	}
	// Premise: the meta was never seen by scan #1 (it did not exist).
	if len(parsed1.MetaSeen) != 0 {
		t.Fatalf("scan #1 recorded a metaSeen entry though no meta existed: %v", parsed1.MetaSeen)
	}

	// Between runs (daemon stopped) the subagent `.meta.json` appears.
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))

	// Scan #2 resumes from the persisted cursor: the parent + child transcripts are
	// at EOF (zero re-read, no re-emit), but the meta is NEW vs the starting metaSeen
	// → scan MUST emit the catalog-safe repair SessionUpdated.
	a2, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	out2 := make(chan canonical.Event, 256)
	if err := a2.Scan(context.Background(), parsed1, out2); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	events2 := drainBuffered(out2)

	// A re-emitted SessionStarted/OpStarted/OpFinalized would double-count the catalog
	// (P1.6). The repair is a SessionUpdated only.
	for _, ev := range events2 {
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == child {
			t.Fatalf("scan #2 re-emitted a SessionStarted for child %q (catalog double-count) — repair must be a SessionUpdated", child)
		}
	}
	var repaired bool
	for _, ev := range events2 {
		su, ok := ev.(canonical.SessionUpdatedEvent)
		if !ok || su.NativeID != child {
			continue
		}
		if su.AgentName != "Explore" {
			t.Fatalf("scan-side repair SessionUpdated AgentName = %q, want Explore", su.AgentName)
		}
		if got := extrasToolUseID(su.Extras); got != parentAgentToolUseID {
			t.Fatalf("scan-side repair SessionUpdated toolUseId = %q, want %q (resolver join key)", got, parentAgentToolUseID)
		}
		repaired = true
	}
	if !repaired {
		t.Fatalf("scan #2 did not emit the late-meta repair SessionUpdated for child %q (P1.8 — scan-side repair missing)", child)
	}

	// And the cursor now records the meta hash (so a later Tail treats it as a bare
	// touch and does not re-emit).
	parsed2, err := ParseCursor(lastCursor(t, events2))
	if err != nil {
		t.Fatalf("ParseCursor #2: %v", err)
	}
	if len(parsed2.MetaSeen) != 1 {
		t.Fatalf("scan #2 did not record the meta hash into the cursor: %v", parsed2.MetaSeen)
	}
}
