package claude_code

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestScanThenTail_LateMetaRepairStashesToolUseId pins P1.7a (SOW-0003 Round 7):
// the late-meta repair must emit a SessionUpdatedEvent carrying the child's
// `aiViewer.toolUseId` (not only AgentName). A child sidechain read BEFORE its own
// `.meta.json` emits a SessionStarted with NO toolUseId stash, so the resolver's
// linkOpChildrenByToolUse pass (which matches the parent op's toolUseId against the
// CHILD session's toolUseId) could never link it. Emitting the toolUseId on the
// repair closes the child-before-meta gap.
//
// Without the fix the repair SessionUpdated carries AgentName only, so this test
// FAILS (no toolUseId in the repair extras).
func TestScanThenTail_LateMetaRepairStashesToolUseId(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	child := childNativeID(durParentSession, durAgentID)
	// The child sidechain exists at Scan time; its meta does NOT.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Scan reads the child. No meta → its SessionStarted carries no toolUseId stash.
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	cs, ok := sessionStartedByNative(drainBuffered(scanOut), child)
	if !ok {
		t.Fatalf("child session %q not started during Scan", child)
	}
	if tu := extrasToolUseID(cs.Extras); tu != "" {
		t.Fatalf("child SessionStarted carried toolUseId %q before meta exists; test premise broken", tu)
	}

	// Tail on the SAME instance; after the watch is live, create the .meta.json.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	time.Sleep(150 * time.Millisecond)
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))

	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-tailOut:
			su, ok := ev.(canonical.SessionUpdatedEvent)
			if !ok || su.NativeID != child {
				continue
			}
			// The repair MUST carry the toolUseId stash (P1.7a) AND the AgentName.
			if su.AgentName != "Explore" {
				continue
			}
			if got := extrasToolUseID(su.Extras); got != parentAgentToolUseID {
				cancel()
				<-done
				t.Fatalf("late-meta repair SessionUpdated carried toolUseId %q, want %q (P1.7a — the child join key must be re-stashed)", got, parentAgentToolUseID)
			}
			cancel()
			<-done
			return
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("late-meta repair SessionUpdated for child %q never observed", child)
		}
	}
}

// extrasToolUseID digs the aiViewer.toolUseId out of an event's extras map.
func extrasToolUseID(extras map[string]any) string {
	av, ok := extras["aiViewer"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := av["toolUseId"].(string)
	return s
}

// TestScanThenTail_SymlinkedRootNoHistoryReplay pins P1.7d (SOW-0003 Round 7):
// under a symlinked projects root (link → real), a Tail that follows a Scan must
// NOT re-emit history. The bug: the Tail watch is established over the RESOLVED
// root, so fsnotify event paths are resolved-root-prefixed; deriving cursor keys
// relative to the UNRESOLVED (configured) root yields "../real/..." keys that miss
// the scan cursor entry, so a steady-state read re-reads the file from offset 0 and
// re-emits every historical SessionStarted/OpStarted → catalog double-count.
//
// The test scans a populated symlinked root (recording the cursor), then tails on
// the same instance and appends ONE new assistant record. It asserts that the only
// LLM OpStarted seen during Tail is the genuinely-new one — no historical op is
// replayed. Without the fix the whole history re-emits and the assertion fails.
func TestScanThenTail_SymlinkedRootNoHistoryReplay(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	const sess = "seed"
	transcript := filepath.Join(realRoot, "-home-user-x", sess+".jsonl")
	// Seed history: a user turn + an assistant LLM op (the historical record).
	writeFileBytes(t, transcript, []byte(strings.Join([]string{
		`{"type":"user","uuid":"u1","sessionId":"` + sess + `","message":{"role":"user","content":"hi"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"` + sess + `","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"text","text":"hello"}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	}, "\n")+"\n"))

	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	// Configure the adapter with the SYMLINKED root.
	a, err := New(linkRoot, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Scan the populated tree; this records the per-file cursor offset at EOF.
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	// Premise: Scan emitted exactly one historical LLM op for the session.
	if n := llmOpStartCount(scanEvents, sess); n != 1 {
		t.Fatalf("Scan emitted %d LLM ops for %q, want 1 (test premise)", n, sess)
	}

	// Tail on the SAME instance (resumes from the scan cursor). After the watch is
	// live, append ONE new assistant record under the REAL tree.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = a.Tail(ctx, tailOut) }()

	time.Sleep(250 * time.Millisecond)
	appendFileBytes(t, transcript, []byte(
		`{"type":"assistant","uuid":"a2","sessionId":"`+sess+`","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":4},"content":[{"type":"text","text":"again"}]},"timestamp":"2026-05-26T10:00:09.000Z"}`+"\n"))

	// Collect Tail events for a window covering the debounce + a tick.
	deadline := time.After(tailTickInterval + 2*time.Second)
	var tailEvents []canonical.Event
	sawNew := false
	for !sawNew {
		select {
		case ev := <-tailOut:
			tailEvents = append(tailEvents, ev)
			// The genuinely-new assistant record finalizes the second LLM op
			// (turn 1 op 2 — after the historical op 1). Use it as the signal that
			// the append was processed.
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == sess && of.TurnSeq == 1 && of.Seq == 2 {
				sawNew = true
			}
		case <-deadline:
			cancel()
			wg.Wait()
			t.Fatalf("Tail never processed the appended record under the symlinked root (got %d events)", len(tailEvents))
		}
	}
	// Give a brief grace window for any spurious history replay to surface too.
	time.Sleep(200 * time.Millisecond)
	tailEvents = append(tailEvents, drainBuffered(tailOut)...)
	cancel()
	wg.Wait()

	// The crux: Tail must NOT have re-emitted the HISTORICAL op (turn 1, op 1). A
	// missed cursor key would re-read from offset 0 and re-emit it.
	for _, ev := range tailEvents {
		if os, ok := ev.(canonical.OpStartedEvent); ok &&
			os.SessionNativeID == sess && os.TurnSeq == 1 && os.Seq == 1 {
			t.Fatalf("Tail re-emitted the historical LLM OpStarted (turn 1, op 1) under a symlinked root (P1.7d — cursor key mismatch caused a from-0 re-read → catalog double-count)")
		}
		if ss, ok := ev.(canonical.SessionStartedEvent); ok && ss.NativeID == sess {
			t.Fatalf("Tail re-emitted the historical SessionStarted for %q under a symlinked root (P1.7d — from-0 re-read)", sess)
		}
	}
}

// llmOpStartCount counts LLM OpStarted events for a session.
func llmOpStartCount(events []canonical.Event, sess string) int {
	n := 0
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok &&
			os.SessionNativeID == sess && os.Kind == canonical.OpLLM {
			n++
		}
	}
	return n
}
