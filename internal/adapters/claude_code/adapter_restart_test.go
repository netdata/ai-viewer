package claude_code

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestRestart_NoDupNoGap verifies acceptance #6: ingesting the first half of
// a transcript, persisting the cursor, then resuming from that cursor over
// the full file produces the same end state (no duplicate, no gap) as a
// single one-shot ingest.
//
// "Same end state" is compared on the canonical content that the SQL layer
// keys on (event kind + session/turn/op identity + the load-bearing
// payload fields), NOT on SourceSeq — which is an observability counter the
// adapter derives from byte offset and intentionally differs across a split
// vs one-shot pass (Gate decision #2). The ingester dedups via natural
// identity, so identical content under different SourceSeqs is correct.
func TestRestart_NoDupNoGap(t *testing.T) {
	t.Parallel()

	lines := []string{
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"first"},"timestamp":"2026-05-26T10:00:00.000Z","cwd":"<HOME>/x"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100},"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
		`{"type":"user","uuid":"u2","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false}]},"toolUseResult":"ok","timestamp":"2026-05-26T10:00:03.000Z"}`,
		`{"type":"assistant","uuid":"a2","sessionId":"s","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":8},"content":[{"type":"text","text":"done"}]},"timestamp":"2026-05-26T10:00:05.000Z"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sy1","sessionId":"s","durationMs":5000,"timestamp":"2026-05-26T10:00:05.500Z"}`,
	}

	// One-shot reference run over the full file.
	oneShot := buildAndScan(t, lines)

	// Split run: write the first 2 lines, scan, persist cursor, append the
	// rest, resume from the cursor.
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	path := filepath.Join(proj, "s.jsonl")
	writeFileBytes(t, path, []byte(strings.Join(lines[:2], "\n")+"\n"))

	a, err := New(tmp, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out1 := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	firstHalf := drainBuffered(out1)
	cursor := lastCursor(t, firstHalf)

	// Append the remaining lines and resume.
	appendFileBytes(t, path, []byte(strings.Join(lines[2:], "\n")+"\n"))
	parsed, err := a.ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	out2 := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), parsed, out2); err != nil {
		t.Fatalf("Scan #2 (resume): %v", err)
	}
	secondHalf := drainBuffered(out2)

	combined := append(append([]canonical.Event{}, firstHalf...), secondHalf...)

	gotKeys := contentKeys(combined)
	wantKeys := contentKeys(oneShot)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("split produced %d content events, one-shot produced %d\nsplit:   %v\noneShot: %v",
			len(gotKeys), len(wantKeys), gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("content mismatch at %d:\n split:   %s\n oneShot: %s", i, gotKeys[i], wantKeys[i])
		}
	}
}

// buildAndScan writes lines to a fresh transcript and returns the one-shot
// Scan event stream.
func buildAndScan(t *testing.T, lines []string) []canonical.Event {
	t.Helper()
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, "s.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))
	a, err := New(tmp, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return drainBuffered(out)
}

// lastCursor returns the cursor JSON from the last SourceProgressEvent.
func lastCursor(t *testing.T, events []canonical.Event) string {
	t.Helper()
	cur := ""
	for _, ev := range events {
		if sp, ok := ev.(canonical.SourceProgressEvent); ok {
			cur = sp.Cursor
		}
	}
	if cur == "" {
		t.Fatal("no SourceProgressEvent in first-half events")
	}
	return cur
}

// contentKeys reduces a stream to a sorted slice of content identity keys,
// ignoring SourceProgress and SourceSeq. Two streams with identical content
// keys represent the same end state after SQL-layer dedup.
func contentKeys(events []canonical.Event) []string {
	keys := make([]string, 0, len(events))
	for _, ev := range events {
		if _, ok := ev.(canonical.SourceProgressEvent); ok {
			continue
		}
		keys = append(keys, contentKey(ev))
	}
	sort.Strings(keys)
	return keys
}

// contentKey builds a stable identity string for an event from the fields
// the SQL layer keys on (kind + session/turn/op identity), excluding
// SourceSeq.
func contentKey(ev canonical.Event) string {
	switch e := ev.(type) {
	case canonical.SessionStartedEvent:
		return "ss|" + e.NativeID + "|" + string(e.Kind)
	case canonical.SessionUpdatedEvent:
		return "su|" + e.NativeID + "|" + e.Model + "|" + e.AgentName
	case canonical.TurnStartedEvent:
		return "ts|" + e.SessionNativeID + "|" + itoa(e.Seq)
	case canonical.TurnFinalizedEvent:
		return "tf|" + e.SessionNativeID + "|" + itoa(e.Seq)
	case canonical.OpStartedEvent:
		return "os|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + itoa(e.Seq) + "|" + string(e.Kind)
	case canonical.OpFinalizedEvent:
		return "of|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + itoa(e.Seq)
	case canonical.LogEntryEvent:
		return "log|" + e.SessionNativeID + "|" + itoa(e.TurnSeq) + "|" + e.Message
	default:
		return "other"
	}
}

func itoa(i int) string {
	return strconvItoa(i)
}

// strconvItoa avoids importing strconv just for one call site in tests.
func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestRestart_GResumedFixtureSplit drives the resume path over the committed
// g_resumed golden fixture: scan turn 1, persist, then scan the full file
// from the cursor and confirm turn 2's events appear with no turn-1 dups.
func TestRestart_GResumedFixtureSplit(t *testing.T) {
	t.Parallel()
	src := filepath.Join("..", "..", "..", "testdata", "claude_code", "g_resumed", "INPUT",
		"-home-user-src-demo", "77777777-7777-4777-8777-777777777777.jsonl")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	allLines := splitNonEmptyLines(string(raw))
	if len(allLines) < 6 {
		t.Fatalf("fixture has %d lines, want >= 6", len(allLines))
	}

	// First half: through the first turn_duration (line index 2).
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-src-demo")
	path := filepath.Join(proj, "77777777-7777-4777-8777-777777777777.jsonl")
	writeFileBytes(t, path, []byte(strings.Join(allLines[:3], "\n")+"\n"))

	a, _ := New(tmp, canonical.AdapterOptions{})
	out1 := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	first := drainBuffered(out1)
	cur := lastCursor(t, first)

	appendFileBytes(t, path, []byte(strings.Join(allLines[3:], "\n")+"\n"))
	parsed, _ := a.ParseCursor(cur)
	out2 := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), parsed, out2); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	second := drainBuffered(out2)

	// Turn 1 must appear exactly once across both passes; turn 2 exactly once.
	combined := append(append([]canonical.Event{}, first...), second...)
	turn1, turn2 := 0, 0
	for _, ev := range combined {
		if ts, ok := ev.(canonical.TurnStartedEvent); ok {
			switch ts.Seq {
			case 1:
				turn1++
			case 2:
				turn2++
			}
		}
	}
	if turn1 != 1 {
		t.Fatalf("turn 1 started %d times across resume, want 1 (no dup)", turn1)
	}
	if turn2 != 1 {
		t.Fatalf("turn 2 started %d times, want 1 (no gap)", turn2)
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestScanThenTail_NoLossInWindow pins P2c (spec §6.3): records appended to a
// transcript BETWEEN Scan finishing and Tail starting must NOT be lost. Tail
// resumes from the per-file offsets Scan recorded on the instance (and does an
// initial catch-up read to current EOF), so the during-window append surfaces.
// With the pre-fix behavior (Tail snapshots current EOF), turn 2 below would be
// silently skipped.
func TestScanThenTail_NoLossInWindow(t *testing.T) {
	t.Parallel()
	turn1 := []string{
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"first"},"timestamp":"2026-05-26T10:00:00.000Z","cwd":"<HOME>/x"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sy1","sessionId":"s","durationMs":1000,"timestamp":"2026-05-26T10:00:01.000Z"}`,
	}
	// Appended AFTER Scan, BEFORE Tail reads — the data-loss window.
	turn2 := []string{
		`{"type":"user","uuid":"u2","sessionId":"s","message":{"role":"user","content":"second"},"timestamp":"2026-05-26T10:01:00.000Z","cwd":"<HOME>/x"}`,
	}

	tmp := t.TempDir()
	proj := filepath.Join(tmp, "-home-user-x")
	path := filepath.Join(proj, "s.jsonl")
	writeFileBytes(t, path, []byte(strings.Join(turn1, "\n")+"\n"))

	a, err := New(tmp, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Scan turn 1; the adapter records its final offsets for the Tail handoff.
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	_ = drainBuffered(scanOut)
	if a.scanCursor == nil {
		t.Fatal("Scan must record scanCursor for the Tail handoff (P2c)")
	}

	// The window: append turn 2 before Tail starts watching.
	appendFileBytes(t, path, []byte(strings.Join(turn2, "\n")+"\n"))

	// Tail on the SAME instance. Its startup catch-up reads from the recorded
	// offset to EOF, so turn 2 must appear. Run in a goroutine and wait for
	// turn 2's TurnStarted, then cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	if !waitForTurn2(t, tailOut) {
		cancel()
		<-done
		t.Fatal("turn 2 (appended in the Scan→Tail window) was not emitted by Tail (P2c data loss)")
	}
	cancel()
	<-done
}

// waitForTurn2 drains tailOut until a TurnStartedEvent with Seq==2 arrives or
// the timeout elapses. Returns true on success.
func waitForTurn2(t *testing.T, tailOut <-chan canonical.Event) bool {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-tailOut:
			if ts, ok := ev.(canonical.TurnStartedEvent); ok && ts.Seq == 2 {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// parentAgentRecord is the parent transcript line whose `Agent` tool_use spawns
// the subagent. agentMetaToolUse links it to the meta sidecar's toolUseId.
const (
	parentAgentToolUseID = "toolu_agent_dur"
	durAgentID           = "abc111def222ccc"
	durParentSession     = "dur-parent"
	parentAgentOpSeq     = 3
)

// writeParentWithAgentOp writes a parent transcript whose assistant record
// spawns an Agent subagent (no tool_result follows — claude-code never writes
// one, §4.4), plus the meta sidecar that links the Agent tool_use to the agent
// id. Returns the parent file path.
func writeParentWithAgentOp(t *testing.T, root string) {
	t.Helper()
	proj := filepath.Join(root, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, durParentSession+".jsonl"), []byte(strings.Join([]string{
		`{"type":"user","uuid":"pu1","sessionId":"` + durParentSession + `","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"pa1","sessionId":"` + durParentSession + `","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"` + parentAgentToolUseID + `","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	}, "\n")+"\n"))
	metaDir := filepath.Join(proj, durParentSession, "subagents")
	writeFileBytes(t, filepath.Join(metaDir, "agent-"+durAgentID+".meta.json"),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))
}

// childPath returns the subagent sidechain path for the durability fixtures.
func childPath(root string) string {
	return filepath.Join(root, "-home-user-x", durParentSession, "subagents", "agent-"+durAgentID+".jsonl")
}

// childLine builds a subagent record line for the durability fixtures.
func childLine(uuid, content, ts string) string {
	return `{"type":"user","uuid":"` + uuid + `","isSidechain":true,"agentId":"` + durAgentID + `","sessionId":"` + durParentSession + `","message":{"role":"user","content":"` + content + `"},"timestamp":"` + ts + `"}`
}

// childAssistantLine builds the subagent's final assistant text record — the
// §485 completion marker (content[0].type=="text").
func childAssistantLine(uuid, text, ts string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","isSidechain":true,"agentId":"` + durAgentID + `","sessionId":"` + durParentSession + `","message":{"id":"cm1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"text","text":"` + text + `"}]},"timestamp":"` + ts + `"}`
}

// childToolUseLine builds a subagent assistant record whose content[0] is a
// tool_use block (NOT a text completion marker). A child whose LAST record is
// this represents an interrupted/paused subagent and is NOT complete (§8.1).
func childToolUseLine(uuid, toolUseID, toolName, ts string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","isSidechain":true,"agentId":"` + durAgentID + `","sessionId":"` + durParentSession + `","message":{"id":"cm2","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"tool_use","id":"` + toolUseID + `","name":"` + toolName + `","input":{}}]},"timestamp":"` + ts + `"}`
}

// childOpFinalized reports the OpFinalizedEvent for the parent's Agent op, or false.
func childOpFinalized(events []canonical.Event) (canonical.OpFinalizedEvent, bool) {
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
			of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
			return of, true
		}
	}
	return canonical.OpFinalizedEvent{}, false
}

// agentOpStartedWithChild reports the most recent parent Agent OpStarted and
// whether its ChildSessionNativeID is set.
func agentOpStartedChild(events []canonical.Event) (string, bool) {
	child := ""
	found := false
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok &&
			os.SessionNativeID == durParentSession && os.Kind == canonical.OpSession &&
			os.TurnSeq == 1 && os.Seq == parentAgentOpSeq {
			child = os.ChildSessionNativeID
			found = true
		}
	}
	return child, found
}

// agentOpStartedToolUseId reports the toolUseId stash on the parent Agent OpStarted
// or "" when absent. Mirrors the mapper-side extras shape.
func agentOpStartedToolUseId(events []canonical.Event) string {
	out := ""
	for _, ev := range events {
		if os, ok := ev.(canonical.OpStartedEvent); ok &&
			os.SessionNativeID == durParentSession && os.Kind == canonical.OpSession &&
			os.TurnSeq == 1 && os.Seq == parentAgentOpSeq {
			if av, ok := os.Extras["aiViewer"].(map[string]any); ok {
				if s, ok := av["toolUseId"].(string); ok {
					out = s
				}
			}
		}
	}
	return out
}

// writeParentAgentOpNoMeta writes the parent transcript with the Agent tool_use
// but does NOT write the linking .meta.json (the late-meta scenario, P1.3b).
func writeParentAgentOpNoMeta(t *testing.T, root string) {
	t.Helper()
	proj := filepath.Join(root, "-home-user-x")
	writeFileBytes(t, filepath.Join(proj, durParentSession+".jsonl"), []byte(strings.Join([]string{
		`{"type":"user","uuid":"pu1","sessionId":"` + durParentSession + `","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"pa1","sessionId":"` + durParentSession + `","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"tool_use","id":"` + parentAgentToolUseID + `","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	}, "\n")+"\n"))
}

// metaPath returns the subagent meta sidecar path for the durability fixtures.
func metaPath(root string) string {
	return filepath.Join(root, "-home-user-x", durParentSession, "subagents", "agent-"+durAgentID+".meta.json")
}

// TestScanThenTail_LateMetaParentOpStashesToolUseIdNoReEmit pins P1.6 (SOW-0003
// Round 6): when the parent's Agent tool_use is read BEFORE its `.meta.json` exists,
// the Agent op (a) carries its toolUseId stash — the meta-independent parent→child
// join key the resolver matches — and (b) has an empty ChildSessionNativeID. When the
// `.meta.json` later arrives during Tail, the adapter must NOT re-read the parent and
// re-emit its Agent OpStarted (the old from-0 re-emit double-counted the catalog,
// P1.6). The op→child link is repaired by the resolver's toolUseId match (covered by
// the ingester test TestResolver_LinksOpChildByToolUseId), not by a transcript
// re-read here. This test asserts the stash is present and that NO re-emitted parent
// Agent OpStarted appears after the late meta.
func TestScanThenTail_LateMetaParentOpStashesToolUseIdNoReEmit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentAgentOpNoMeta(t, root)

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Scan reads the parent. No meta yet → the Agent op has an empty child link but
	// MUST carry the toolUseId stash (the meta-independent join key).
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	if child, ok := agentOpStartedChild(scanEvents); !ok {
		t.Fatal("Agent op not emitted during Scan")
	} else if child != "" {
		t.Fatalf("Agent op already linked to %q before the meta exists; test premise broken", child)
	}
	if got := agentOpStartedToolUseId(scanEvents); got != parentAgentToolUseID {
		t.Fatalf("parent Agent op toolUseId stash = %q, want %q (the meta-independent join key)", got, parentAgentToolUseID)
	}

	// Tail on the SAME instance; after the watch is live, create the .meta.json. The
	// adapter must NOT re-emit the parent Agent OpStarted (no from-0 re-read).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	time.Sleep(150 * time.Millisecond)
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))

	// Watch for a grace window: any re-emitted parent Agent OpStarted is a P1.6
	// regression (it would re-count the catalog). The child AgentName repair
	// (SessionUpdated) is allowed and ignored here.
	deadline := time.After(tailTickInterval + 1500*time.Millisecond)
	reEmits := 0
	for stop := false; !stop; {
		select {
		case ev := <-tailOut:
			if os, ok := ev.(canonical.OpStartedEvent); ok &&
				os.SessionNativeID == durParentSession && os.Kind == canonical.OpSession &&
				os.TurnSeq == 1 && os.Seq == parentAgentOpSeq {
				reEmits++
			}
		case <-deadline:
			stop = true
		}
	}
	cancel()
	<-done
	if reEmits != 0 {
		t.Fatalf("late .meta.json re-emitted the parent Agent OpStarted %d times during Tail (P1.6 regression — must be re-emit-free)", reEmits)
	}
}

// TestScanThenTail_AgentOpFinalizeDurable pins the §8.1 durability property: a
// parent Agent op observed during Scan must be finalizable when its child
// sidechain COMPLETES (terminal assistant-text marker) during a later Tail
// flush. The child does not exist at Scan time; it is created (complete) after
// Tail starts. With a naive Scan→Tail boundary that drops the parent's Agent-op
// deferral, the parent op would never finalize.
func TestScanThenTail_AgentOpFinalizeDurable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Scan reads the parent (and its empty subagents/ dir). The Agent op is
	// emitted but stays running (no child yet).
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	if _, ok := childOpFinalized(scanEvents); ok {
		t.Fatal("parent Agent op finalized during Scan though the child does not exist yet")
	}

	// Tail on the SAME instance; after the watch is live, create the COMPLETE
	// child sidechain (terminal assistant-text). The flush that reads it newly
	// marks the child completed and pairs it with the parent op (event-driven,
	// no tick needed).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	time.Sleep(150 * time.Millisecond)
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	// The parent Agent op must finalize, stamped at the child's terminal
	// assistant-text ts.
	wantEnd, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-tailOut:
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
				if of.EndTs != wantEnd {
					t.Errorf("Agent op finalize EndTs = %d, want the child's terminal assistant-text ts %d", of.EndTs, wantEnd)
				}
				cancel()
				<-done
				return
			}
		case <-deadline:
			cancel()
			<-done
			t.Fatal("parent Agent op (from Scan) never finalized after the child completed in Tail (§8.1 durability)")
		}
	}
}

// TestScanThenTail_AgentOpNotPrematureToolUseTerminated pins the §8.1
// "not premature" property with NO timing gap: a child whose LAST record is a
// tool_use (an interrupted/paused subagent, never an assistant-text completion
// marker) must NEVER finalize its parent Agent op — and the test drives PAST a
// full tail tick to prove it is not merely "not finalized in the first flush"
// (closing codex's test-gap note from Round 3). The old quiescent-EOF design
// would have finalized this child after one quiet tick; the terminal-record-type
// rule never does.
func TestScanThenTail_AgentOpNotPrematureToolUseTerminated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	_ = drainBuffered(scanOut)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	// Watch for ANY finalize of the parent Agent op for the whole test lifetime.
	premature := make(chan canonical.OpFinalizedEvent, 1)
	go func() {
		for {
			select {
			case ev, ok := <-tailOut:
				if !ok {
					return
				}
				if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
					of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
					select {
					case premature <- of:
					default:
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	time.Sleep(150 * time.Millisecond)
	// A COMPLETE-LOOKING-but-interrupted child: it is fully written (no partial
	// line) and quiescent (no further appends), but its terminal record is a
	// tool_use, NOT an assistant-text marker — so it must stay running forever.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childToolUseLine("ct1", "toolu_child_1", "Read", "2026-05-26T10:00:06.000Z"),
	}, "\n")+"\n"))

	// Drive PAST at least one full tail tick (the old design's quiescence sweep
	// fired here). The parent op must NOT finalize at any point.
	time.Sleep(tailTickInterval + 600*time.Millisecond)

	select {
	case of := <-premature:
		cancel()
		<-done
		t.Fatalf("parent Agent op finalized for a child whose terminal record is a tool_use (not an assistant-text completion marker) — premature finalize (§8.1): EndTs=%d", of.EndTs)
	default:
	}
	cancel()
	<-done
}

// TestScan_AgentOpFinalizeTerminalAssistantText verifies the happy path: in a
// static Scan, a fully-read child whose terminal record is an assistant-text
// marker finalizes its parent Agent op at that record's timestamp (§8.1, §485).
func TestScan_AgentOpFinalizeTerminalAssistantText(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// The child already exists, complete (ends on assistant-text), at Scan time.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	of, ok := childOpFinalized(events)
	if !ok {
		t.Fatal("fully-read child ending in assistant-text did not finalize its parent Agent op (§8.1)")
	}
	wantEnd, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")
	if of.EndTs != wantEnd {
		t.Fatalf("Agent op finalize EndTs = %d, want the child's terminal assistant-text ts %d", of.EndTs, wantEnd)
	}
	if of.Status != "completed" {
		t.Fatalf("Agent op finalize Status = %q, want completed", of.Status)
	}
}

// TestScan_AgentOpNotFinalizedToolUseTerminated pins the static-Scan "not
// premature" property: a fully-read child whose terminal record is a tool_use
// (interrupted subagent) must NOT finalize its parent Agent op. No timing window
// is involved — the rule is purely the terminal record's type (§8.1).
func TestScan_AgentOpNotFinalizedToolUseTerminated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// Child fully written but ending in a tool_use (no assistant-text marker).
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childToolUseLine("ct1", "toolu_child_1", "Read", "2026-05-26T10:00:06.000Z"),
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	events := drainBuffered(out)
	if of, ok := childOpFinalized(events); ok {
		t.Fatalf("Agent op finalized for a tool_use-terminated child (not complete, §8.1): EndTs=%d", of.EndTs)
	}
}

// TestScanThenTail_ParkedCompletionDurableAcrossRestart pins P2.4d: a child that
// completes (terminal assistant-text) BEFORE its parent Agent op is known parks
// its completion; that park must survive a daemon restart via the cursor. After
// restart (new adapter instance, same cursor) the parent Agent op finally appears
// and the parent must finalize EXACTLY ONCE. Without cursor-durable parking the
// restart loses the in-memory park and the parent never finalizes.
func TestScanThenTail_ParkedCompletionDurableAcrossRestart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// The child sidechain + its meta exist; the PARENT transcript does NOT yet.
	// So the child completes with no known parent Agent op → it parks.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore","toolUseId":"`+parentAgentToolUseID+`"}`))

	// Run #1: Scan reads the child (completes, parks — parent unknown). No
	// finalize is possible yet. Capture the persisted cursor.
	a1, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	out1 := make(chan canonical.Event, 256)
	if err := a1.Scan(context.Background(), nil, out1); err != nil {
		t.Fatalf("Scan #1: %v", err)
	}
	events1 := drainBuffered(out1)
	if of, ok := childOpFinalized(events1); ok {
		t.Fatalf("parent Agent op finalized in run #1 though its transcript does not exist yet: EndTs=%d", of.EndTs)
	}
	cursor := lastCursor(t, events1)

	// The persisted cursor must carry the parked completion so a restart can
	// restore it (the durability the fix adds).
	parsed1, err := ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	wantChild := childNativeID(durParentSession, durAgentID)
	if _, ok := parsed1.Parked[wantChild]; !ok {
		t.Fatalf("persisted cursor did not record the parked completion for %q (P2.4d); parked=%v", wantChild, parsed1.Parked)
	}

	// Run #2 (simulated restart): a FRESH adapter instance resuming from the
	// persisted cursor. Now the parent transcript appears (its Agent tool_use
	// spawns the child). Scan reads it → the parent op becomes known → the
	// restored park must finalize the parent exactly once.
	writeParentWithAgentOp(t, root)

	a2, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	parsed2, err := a2.ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor #2: %v", err)
	}
	// Drive Scan then Tail on the restarted instance (mirrors runAdapter). The
	// finalize may land in Scan (parent read there) — assert it appears exactly
	// once across Scan + a brief Tail.
	out2 := make(chan canonical.Event, 256)
	if err := a2.Scan(context.Background(), parsed2, out2); err != nil {
		t.Fatalf("Scan #2: %v", err)
	}
	scan2 := drainBuffered(out2)

	finalizes := 0
	for _, ev := range scan2 {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
			of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
			finalizes++
		}
	}

	// Tail briefly to confirm no SECOND finalize is emitted (the finalized set +
	// the dropped park entry guard against it).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a2.Tail(ctx, tailOut); close(done) }()
	deadline := time.After(tailTickInterval + 600*time.Millisecond)
	for done2 := false; !done2; {
		select {
		case ev := <-tailOut:
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
				finalizes++
			}
		case <-deadline:
			done2 = true
		}
	}
	cancel()
	<-done

	if finalizes != 1 {
		t.Fatalf("parent Agent op finalized %d times across the restart, want exactly 1 (P2.4d parked-completion durability)", finalizes)
	}
}

// TestScanThenTail_AgentOpNoDoubleFinalizeOnReplay pins P2.3a: once a child
// finalizes its parent Agent op, a subsequent catch-up / replay (re-reading the
// same already-consumed child from EOF) must emit NO second OpFinalized for it.
// The terminal assistant-text record then sits BELOW the resume offset, so it is
// not newly read and the child is not re-marked completed; the `finalized` set
// is a second guard. Exactly one finalize must be observed across the whole run.
func TestScanThenTail_AgentOpNoDoubleFinalizeOnReplay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// Child already complete at Scan time → Scan finalizes the parent op once.
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	finalizes := 0
	for _, ev := range scanEvents {
		if _, ok := ev.(canonical.OpFinalizedEvent); ok {
			if of := ev.(canonical.OpFinalizedEvent); of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
				finalizes++
			}
		}
	}
	if finalizes != 1 {
		t.Fatalf("Scan finalized the parent Agent op %d times, want exactly 1", finalizes)
	}

	// Tail on the SAME instance: its startup catch-up RE-READS the already-
	// consumed, UNCHANGED, COMPLETE child from EOF (offset==size). The terminal
	// assistant-text record is the genuine last record (so lastRecordAssistantText
	// is true) BUT it sits below the resume offset, so lastRecordEmitted is false
	// → the child is NOT re-marked completed (the emit-gate). The `finalized` set
	// is the second guard. Neither the catch-up nor any later flush may
	// re-finalize.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	// Touch the child's mtime without changing content so a WRITE event fires and
	// the flush re-reads it from offset==size (a pure replay of the complete
	// child). Combined with the startup catch-up this exercises the replay path
	// twice; neither pass may re-finalize.
	time.Sleep(150 * time.Millisecond)
	now := time.Now()
	if err := os.Chtimes(childPath(root), now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Also append a benign non-completing record so a real byte-growth WRITE
	// drives a flush too: the terminal record becomes a user record (not
	// complete), so this growth cannot newly complete the child either.
	appendFileBytes(t, childPath(root), []byte(childLine("cu2", "more", "2026-05-26T10:00:11.000Z")+"\n"))

	// Observe for well over a tick; count any further finalizes of the parent op.
	deadline := time.After(tailTickInterval + 800*time.Millisecond)
	extra := 0
	for {
		select {
		case ev := <-tailOut:
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
				extra++
			}
		case <-deadline:
			cancel()
			<-done
			if extra != 0 {
				t.Fatalf("replay/catch-up emitted %d additional OpFinalized for the parent Agent op, want 0 (P2.3a no double-finalize)", extra)
			}
			return
		}
	}
}

// TestScanThenTail_LateMetaRewriteNoDoubleFinalize pins the Round-6 invariant that a
// `.meta.json` rewrite during Tail emits NO additional parent Agent-op finalize. In
// Round 6 the late-meta path no longer re-reads any child transcript (the from-0
// re-emit was removed to stop catalog double-counting, P1.6), so a meta rewrite can
// no longer re-mark a finalized child — the property is now even stronger than the
// old durable-`finalized`-set guard (which is still exercised across the Scan→Tail
// boundary). Exactly one finalize must be observed across Scan + Tail; a meta rewrite
// adds none.
func TestScanThenTail_LateMetaRewriteNoDoubleFinalize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Parent + child + meta ALL present at Scan time → the child completes and the
	// parent Agent op finalizes ONCE during Scan.
	writeParentWithAgentOp(t, root)
	writeFileBytes(t, childPath(root), []byte(strings.Join([]string{
		childLine("cu1", "task", "2026-05-26T10:00:05.000Z"),
		childAssistantLine("ca1", "result", "2026-05-26T10:00:09.000Z"),
	}, "\n")+"\n"))

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scanOut := make(chan canonical.Event, 256)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainBuffered(scanOut)
	finalizes := 0
	for _, ev := range scanEvents {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
			of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
			finalizes++
		}
	}
	if finalizes != 1 {
		t.Fatalf("Scan finalized the parent Agent op %d times, want exactly 1 (test premise)", finalizes)
	}
	// The Scan cursor must carry the finalized child id so Tail can suppress a
	// re-finalize (the durability the P2.5c fix adds).
	wantChild := childNativeID(durParentSession, durAgentID)
	parsed, err := ParseCursor(lastCursor(t, scanEvents))
	if err != nil {
		t.Fatalf("ParseCursor: %v", err)
	}
	foundFinalized := false
	for _, id := range parsed.Finalized {
		if id == wantChild {
			foundFinalized = true
		}
	}
	if !foundFinalized {
		t.Fatalf("Scan cursor did not record the finalized child %q (P2.5c); finalized=%v", wantChild, parsed.Finalized)
	}

	// Tail on the SAME instance (resumes from the Scan cursor, restoring the
	// finalized set). After the watch is live, REWRITE the .meta.json with changed
	// content so the late-meta path fires and re-reads the child from offset 0.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailOut := make(chan canonical.Event, 256)
	done := make(chan struct{})
	go func() { _ = a.Tail(ctx, tailOut); close(done) }()

	time.Sleep(150 * time.Millisecond)
	// Changed content (different description) → hash differs from the cursor's
	// metaSeen → the meta is "changed". In Round 6 this triggers ONLY a catalog-safe
	// SessionUpdated AgentName repair — no child transcript re-read — so it must emit
	// NO parent Agent-op finalize.
	writeFileBytes(t, metaPath(root),
		[]byte(`{"agentType":"Explore","description":"explore-rewritten","toolUseId":"`+parentAgentToolUseID+`"}`))

	deadline := time.After(tailTickInterval + 1500*time.Millisecond)
	extra := 0
	for done2 := false; !done2; {
		select {
		case ev := <-tailOut:
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == parentAgentOpSeq {
				extra++
			}
		case <-deadline:
			done2 = true
		}
	}
	cancel()
	<-done
	if extra != 0 {
		t.Fatalf("late-meta rewrite in Tail emitted %d additional OpFinalized for the parent Agent op, want 0 (Round 6: no re-read, durable finalized set)", extra)
	}
}
