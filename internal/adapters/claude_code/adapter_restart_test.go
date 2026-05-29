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

// childAssistantLine builds the subagent's final assistant text record.
func childAssistantLine(uuid, text, ts string) string {
	return `{"type":"assistant","uuid":"` + uuid + `","isSidechain":true,"agentId":"` + durAgentID + `","sessionId":"` + durParentSession + `","message":{"id":"cm1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3},"content":[{"type":"text","text":"` + text + `"}]},"timestamp":"` + ts + `"}`
}

// childOpFinalized reports the OpFinalizedEvent for the parent's Agent op (the
// session op at parent turn 1, op 2 — after the LLM op at op 1), or false.
func childOpFinalized(events []canonical.Event) (canonical.OpFinalizedEvent, bool) {
	for _, ev := range events {
		if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
			of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == 2 {
			return of, true
		}
	}
	return canonical.OpFinalizedEvent{}, false
}

// TestScanThenTail_AgentOpFinalizeDurable pins P1.2: a parent Agent op observed
// during Scan must be finalizable when its child sidechain COMPLETES during a
// later Tail flush. The child does not exist at Scan time; it is created
// (complete) after Tail starts. With the pre-fix behavior (Scan→Tail boundary
// drops the parent's Agent-op deferral), the parent op would never finalize.
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
	// child sidechain. Its quiescent EOF (next tick) finalizes the parent op.
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

	// The parent Agent op must finalize, stamped at the child's last-record ts.
	wantEnd, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-tailOut:
			if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
				of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == 2 {
				if of.EndTs != wantEnd {
					t.Errorf("Agent op finalize EndTs = %d, want the child's last-record ts %d", of.EndTs, wantEnd)
				}
				cancel()
				<-done
				return
			}
		case <-deadline:
			cancel()
			<-done
			t.Fatal("parent Agent op (from Scan) never finalized after the child completed in Tail (P1.2 durability)")
		}
	}
}

// TestScanThenTail_AgentOpNotPrematureOnLiveChild pins P2.4: a child actively
// appended in the current flush must NOT finalize its parent Agent op — byte-EOF
// of a live subagent is not semantic completion. Here the child is created
// (and thus dirty/just-appended) but the parent op must stay running through the
// flush that first reads it; only a later quiescent tick may finalize it.
func TestScanThenTail_AgentOpNotPrematureOnLiveChild(t *testing.T) {
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

	time.Sleep(150 * time.Millisecond)
	// Append the child in two bursts so it is "just appended" in consecutive
	// flushes. We assert that within the first ~debounce window after the FIRST
	// burst, the parent op is not finalized (the child is dirty that flush).
	writeFileBytes(t, childPath(root), []byte(childLine("cu1", "task", "2026-05-26T10:00:05.000Z")+"\n"))

	// Watch for a premature finalize during a short observation window. The
	// flush that first reads this just-appended child must NOT finalize it.
	premature := make(chan canonical.OpFinalizedEvent, 1)
	go func() {
		for {
			select {
			case ev, ok := <-tailOut:
				if !ok {
					return
				}
				if of, ok := ev.(canonical.OpFinalizedEvent); ok &&
					of.SessionNativeID == durParentSession && of.TurnSeq == 1 && of.Seq == 2 {
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

	// Keep appending to the child across two debounce windows so it stays dirty
	// (non-quiescent) — the parent op must remain running the whole time.
	appendFileBytes(t, childPath(root), []byte(childLine("cu2", "more", "2026-05-26T10:00:06.000Z")+"\n"))
	time.Sleep(debounceWindow + 80*time.Millisecond)
	appendFileBytes(t, childPath(root), []byte(childLine("cu3", "more2", "2026-05-26T10:00:07.000Z")+"\n"))
	time.Sleep(debounceWindow + 80*time.Millisecond)

	select {
	case of := <-premature:
		cancel()
		<-done
		t.Fatalf("parent Agent op finalized while the child was still being appended this flush (premature, P2.4): EndTs=%d", of.EndTs)
	default:
	}
	cancel()
	<-done
}

// TestScan_AgentOpFinalizeQuiescentChildEOF verifies the happy path: in a static
// Scan (no per-flush dirty set), a fully-read child finalizes its parent Agent
// op at the child's last-record timestamp. This is the "quiescent EOF" case the
// P2.4 fix must preserve for historical data.
func TestScan_AgentOpFinalizeQuiescentChildEOF(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeParentWithAgentOp(t, root)
	// The child already exists, complete, at Scan time → quiescent.
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
		t.Fatal("fully-read child in Scan did not finalize its parent Agent op (quiescent EOF)")
	}
	wantEnd, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")
	if of.EndTs != wantEnd {
		t.Fatalf("Agent op finalize EndTs = %d, want the child's last-record ts %d", of.EndTs, wantEnd)
	}
	if of.Status != "completed" {
		t.Fatalf("Agent op finalize Status = %q, want completed", of.Status)
	}
}
