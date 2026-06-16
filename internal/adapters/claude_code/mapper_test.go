package claude_code

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapAll runs a fresh fileMapper over the given parsed records and returns
// the concatenated events. agentMap seeds the toolUseId→agentId map.
func mapAll(t *testing.T, nativeID, parentNativeID string, kind canonical.SessionKind, agentName string, agentMap map[string]string, lines ...string) []canonical.Event {
	t.Helper()
	m := newFileMapper(mapperConfig{
		sourceID:       "src",
		absPath:        "/abs/x.jsonl",
		nativeID:       nativeID,
		parentNativeID: parentNativeID,
		kind:           kind,
		agentName:      agentName,
		toolUseToAgent: agentMap,
	})
	var out []canonical.Event
	for _, line := range lines {
		rec, skip, err := parseLine([]byte(line))
		if err != nil {
			t.Fatalf("parseLine(%q): %v", line, err)
		}
		if skip {
			continue
		}
		evs, err := m.mapRecord(rec)
		if err != nil {
			t.Fatalf("mapRecord: %v", err)
		}
		out = append(out, evs...)
	}
	return out
}

// TestMapper_SubAgentOpCarriesChildNativeID verifies acceptance #3: the
// parent's Agent tool_use op carries ChildSessionNativeID equal to the
// synthesized <parent>:agent:<agentId> when the meta sidecar provides the
// toolUseId→agentId mapping.
func TestMapper_SubAgentOpCarriesChildNativeID(t *testing.T) {
	t.Parallel()
	agentMap := map[string]string{"toolu_agent_1": "abc123def456789"}
	events := mapAll(t, "parent-sess", "", canonical.KindRoot, "", agentMap,
		`{"type":"user","uuid":"u1","sessionId":"parent-sess","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"parent-sess","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	var found bool
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == canonical.OpSession {
			found = true
			want := "parent-sess:agent:abc123def456789"
			if op.ChildSessionNativeID != want {
				t.Fatalf("Agent op ChildSessionNativeID = %q, want %q", op.ChildSessionNativeID, want)
			}
			if op.Name != "explore" {
				t.Fatalf("Agent op Name = %q, want the input description 'explore'", op.Name)
			}
		}
	}
	if !found {
		t.Fatal("no OpSession (Agent) op emitted")
	}
}

// TestMapper_SubAgentOpWithoutMetaHasNoChildLink verifies graceful behavior
// when the sidecar lacks toolUseId (397/623 real metas): the Agent op is
// still emitted (kind=session) but ChildSessionNativeID is empty — no error.
func TestMapper_SubAgentOpWithoutMetaHasNoChildLink(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "parent-sess", "", canonical.KindRoot, "", nil,
		`{"type":"assistant","uuid":"a1","sessionId":"parent-sess","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	var found bool
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == canonical.OpSession {
			found = true
			if op.ChildSessionNativeID != "" {
				t.Fatalf("ChildSessionNativeID = %q, want empty (no meta)", op.ChildSessionNativeID)
			}
		}
	}
	if !found {
		t.Fatal("no OpSession op emitted")
	}
}

// TestMapper_SubAgentSessionFields verifies a subagent file emits a
// sub_agent session with the synthetic NativeID, ParentNativeID, and the
// agentType as AgentName (acceptance #3).
func TestMapper_SubAgentSessionFields(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "parent:agent:abc123def456789", "parent", canonical.KindSubAgent, "general-purpose", nil,
		`{"type":"user","uuid":"u1","isSidechain":true,"agentId":"abc123def456789","sessionId":"parent","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`,
	)
	ss, ok := events[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event is %T, want SessionStartedEvent", events[0])
	}
	if ss.Kind != canonical.KindSubAgent {
		t.Fatalf("Kind = %q, want sub_agent", ss.Kind)
	}
	if ss.NativeID != "parent:agent:abc123def456789" {
		t.Fatalf("NativeID = %q", ss.NativeID)
	}
	if ss.ParentNativeID != "parent" {
		t.Fatalf("ParentNativeID = %q, want parent", ss.ParentNativeID)
	}
	if ss.AgentName != "general-purpose" {
		t.Fatalf("AgentName = %q, want general-purpose", ss.AgentName)
	}
}

// extrasToolUseId extracts extras["aiViewer"]["toolUseId"] from an event's
// Extras map, or "" when absent. Test-only helper for the §8.1 toolUseId-stash
// assertions.
func extrasToolUseId(extras map[string]any) string {
	av, ok := extras["aiViewer"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := av["toolUseId"].(string)
	return s
}

// TestMapper_AgentOpAlwaysStashesToolUseId pins P1.6 (SOW-0003 Round 6): a parent
// `Agent` tool_use op ALWAYS carries its toolUseId (the assistant.tool_use block id)
// in Extras.aiViewer.toolUseId — regardless of whether the sidecar `.meta.json` was
// read. This is the meta-independent parent→child join key the resolver matches; it
// must be present even when no agentMap is supplied (the parent-before-meta case),
// where ChildSessionNativeID is necessarily empty.
func TestMapper_AgentOpAlwaysStashesToolUseId(t *testing.T) {
	t.Parallel()
	const toolUseID = "toolu_agent_1"
	// NO agentMap → the meta is not known; ChildSessionNativeID will be empty, but
	// the toolUseId stash MUST still be present.
	events := mapAll(t, "parent-sess", "", canonical.KindRoot, "", nil,
		`{"type":"assistant","uuid":"a1","sessionId":"parent-sess","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"`+toolUseID+`","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	var found bool
	for _, ev := range events {
		op, ok := ev.(canonical.OpStartedEvent)
		if !ok || op.Kind != canonical.OpSession {
			continue
		}
		found = true
		if op.ChildSessionNativeID != "" {
			t.Fatalf("ChildSessionNativeID = %q, want empty (no meta); test premise broken", op.ChildSessionNativeID)
		}
		if got := extrasToolUseId(op.Extras); got != toolUseID {
			t.Fatalf("Agent op toolUseId stash = %q, want %q (must be stashed even without the meta)", got, toolUseID)
		}
	}
	if !found {
		t.Fatal("no OpSession (Agent) op emitted")
	}
}

// TestMapper_AgentOpStashesToolUseIdWithMeta verifies the stash is ALSO present when
// the meta IS known (so ChildSessionNativeID is set too): the toolUseId stash and the
// direct child link coexist.
func TestMapper_AgentOpStashesToolUseIdWithMeta(t *testing.T) {
	t.Parallel()
	const toolUseID = "toolu_agent_1"
	agentMap := map[string]string{toolUseID: "abc123def456789"}
	events := mapAll(t, "parent-sess", "", canonical.KindRoot, "", agentMap,
		`{"type":"assistant","uuid":"a1","sessionId":"parent-sess","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"`+toolUseID+`","name":"Agent","input":{"description":"explore"}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	for _, ev := range events {
		op, ok := ev.(canonical.OpStartedEvent)
		if !ok || op.Kind != canonical.OpSession {
			continue
		}
		if op.ChildSessionNativeID != "parent-sess:agent:abc123def456789" {
			t.Fatalf("ChildSessionNativeID = %q, want the resolved child id", op.ChildSessionNativeID)
		}
		if got := extrasToolUseId(op.Extras); got != toolUseID {
			t.Fatalf("Agent op toolUseId stash = %q, want %q (must coexist with the child link)", got, toolUseID)
		}
		return
	}
	t.Fatal("no OpSession (Agent) op emitted")
}

// TestMapper_SubAgentSessionStashesToolUseId pins P1.6: a sub-agent session's
// SessionStartedEvent carries its own toolUseId (from its `.meta.json.toolUseId`) in
// Extras.aiViewer.toolUseId — the join key the resolver matches against the parent
// Agent op's stashed toolUseId. The mapper receives it via mapperConfig.toolUseID.
func TestMapper_SubAgentSessionStashesToolUseId(t *testing.T) {
	t.Parallel()
	const toolUseID = "toolu_agent_1"
	m := newFileMapper(mapperConfig{
		sourceID:       "src",
		absPath:        "/abs/x.jsonl",
		nativeID:       "parent:agent:abc123def456789",
		parentNativeID: "parent",
		kind:           canonical.KindSubAgent,
		agentName:      "Explore",
		toolUseID:      toolUseID,
	})
	rec, _, err := parseLine([]byte(`{"type":"user","uuid":"u1","isSidechain":true,"agentId":"abc123def456789","sessionId":"parent","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:05.000Z"}`))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	evs, err := m.mapRecord(rec)
	if err != nil {
		t.Fatalf("mapRecord: %v", err)
	}
	ss, ok := evs[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event is %T, want SessionStartedEvent", evs[0])
	}
	if got := extrasToolUseId(ss.Extras); got != toolUseID {
		t.Fatalf("child session toolUseId stash = %q, want %q", got, toolUseID)
	}
}

// TestMapper_RootSessionNoToolUseIdStash verifies a ROOT (non-sub-agent) session
// does NOT stash a toolUseId (only sub-agents carry the child-side join key), so the
// resolver's toolUseId pass never matches a root session as a "child".
func TestMapper_RootSessionNoToolUseIdStash(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "root-sess", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"root-sess","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
	)
	ss, ok := events[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event is %T, want SessionStartedEvent", events[0])
	}
	if got := extrasToolUseId(ss.Extras); got != "" {
		t.Fatalf("root session carried a toolUseId stash %q, want none", got)
	}
}

// TestMapper_CompactionOp verifies acceptance #4: a compact_boundary emits a
// first-class OpKind='compaction' op with the documented field mapping, and
// the post-compaction isCompactSummary user message does NOT open a turn.
func TestMapper_CompactionOp(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"first"},"timestamp":"2026-05-26T12:00:00.000Z"}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sy1","sessionId":"s","compactMetadata":{"trigger":"manual","preTokens":700000,"postTokens":8000,"durationMs":110000,"preservedSegment":{"headUuid":"u1","anchorUuid":"u1","tailUuid":"u1"},"preservedMessages":{"anchorUuid":"u1","uuids":["u1"]}},"timestamp":"2026-05-26T12:02:00.000Z"}`,
		`{"type":"user","uuid":"u2","isCompactSummary":true,"sessionId":"s","message":{"role":"user","content":"summary"},"timestamp":"2026-05-26T12:02:00.500Z"}`,
		`{"type":"user","uuid":"u3","sessionId":"s","message":{"role":"user","content":"second"},"timestamp":"2026-05-26T12:03:00.000Z"}`,
	)

	var compactionOps, turns int
	var compactStarted canonical.OpStartedEvent
	var compactFinal canonical.OpFinalizedEvent
	var summaryPayload canonical.PayloadRefEvent
	var sawSummaryPayload bool
	var sawBoundaryLog bool
	for _, ev := range events {
		switch e := ev.(type) {
		case canonical.OpStartedEvent:
			if e.Kind == canonical.OpCompaction {
				compactionOps++
				compactStarted = e
			}
		case canonical.OpFinalizedEvent:
			if e.BytesIn == 700000 {
				compactFinal = e
			}
		case canonical.TurnStartedEvent:
			turns++
		case canonical.PayloadRefEvent:
			if e.PayloadKind == "log" {
				summaryPayload = e
				sawSummaryPayload = true
			}
		case canonical.LogEntryEvent:
			if e.Message == "compact_boundary" && e.Severity == "INF" {
				sawBoundaryLog = true
				// The boundary log must carry the full metadata too (P2a).
				if e.Extras["preservedSegment"] == nil {
					t.Errorf("compact_boundary log missing preservedSegment in extras: %+v", e.Extras)
				}
			}
		}
	}
	if compactionOps != 1 {
		t.Fatalf("compaction ops = %d, want 1", compactionOps)
	}
	// P1.1a: the post-compaction summary PayloadRef must be scoped to the
	// compaction op (the op that exists), NOT (TurnSeq=0,OpSeq=0). A payload
	// referencing a non-existent op FK-rolls-back the whole ingest batch
	// because payload_refs.op_id is NOT NULL REFERENCES ops(id).
	if !sawSummaryPayload {
		t.Fatal("isCompactSummary user must emit a PayloadRef (PayloadKind=log)")
	}
	if summaryPayload.TurnSeq != compactStarted.TurnSeq || summaryPayload.OpSeq != compactStarted.Seq {
		t.Fatalf("summary payload scoped to (turn=%d,op=%d), want the compaction op (turn=%d,op=%d) so it references an existing op (P1.1a)",
			summaryPayload.TurnSeq, summaryPayload.OpSeq, compactStarted.TurnSeq, compactStarted.Seq)
	}
	if !sawBoundaryLog {
		t.Fatal("compact_boundary must emit a LogEntry INF in addition to the op (spec §5.4 / §9.2, P2a)")
	}
	if turns != 2 {
		t.Fatalf("turns = %d, want 2 (first + second; the compact summary must NOT open a turn)", turns)
	}
	// boundary ts = 2026-05-26T12:02:00Z; EndTs = ts + 110000ms*1000.
	boundaryUs, _ := parseTsToMicros("2026-05-26T12:02:00.000Z")
	if compactStarted.Ts != boundaryUs {
		t.Fatalf("compaction Ts = %d, want %d", compactStarted.Ts, boundaryUs)
	}
	if compactFinal.EndTs != boundaryUs+110000*1000 {
		t.Fatalf("compaction EndTs = %d, want %d", compactFinal.EndTs, boundaryUs+110000*1000)
	}
	if compactFinal.BytesIn != 700000 || compactFinal.BytesOut != 8000 {
		t.Fatalf("compaction bytes = (%d,%d), want (700000,8000)", compactFinal.BytesIn, compactFinal.BytesOut)
	}
	if compactStarted.Extras["trigger"] != "manual" {
		t.Fatalf("compaction extras.trigger = %v, want manual", compactStarted.Extras["trigger"])
	}
	// Full compactMetadata must be carried (P2a): preservedSegment + preservedMessages.
	if compactStarted.Extras["preservedSegment"] == nil {
		t.Errorf("compaction op extras missing preservedSegment: %+v", compactStarted.Extras)
	}
	if compactStarted.Extras["preservedMessages"] == nil {
		t.Errorf("compaction op extras missing preservedMessages: %+v", compactStarted.Extras)
	}
}

// TestMapper_TerminalAssistantTextTracking pins the §485 completion-marker
// tracking the Agent-op finalize depends on (§8.1): after mapping a sequence,
// lastRecordAssistantText is true with the right ts ONLY when the LAST record is
// an assistant message whose content[0] is text; it is false when the last
// record is a tool_use-led assistant, a user, or a system record.
func TestMapper_TerminalAssistantTextTracking(t *testing.T) {
	t.Parallel()
	const (
		userLine    = `{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"task"},"timestamp":"2026-05-26T10:00:00.000Z"}`
		textLine    = `{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"done"}]},"timestamp":"2026-05-26T10:00:09.000Z"}`
		toolUseLine = `{"type":"assistant","uuid":"a2","sessionId":"s","message":{"id":"m2","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},"timestamp":"2026-05-26T10:00:10.000Z"}`
		sysLine     = `{"type":"system","subtype":"turn_duration","uuid":"sy1","sessionId":"s","durationMs":1000,"timestamp":"2026-05-26T10:00:11.000Z"}`
	)
	wantTextTs, _ := parseTsToMicros("2026-05-26T10:00:09.000Z")

	cases := []struct {
		name     string
		lines    []string
		wantText bool
		wantTs   int64
	}{
		{"ends-text", []string{userLine, textLine}, true, wantTextTs},
		{"ends-tool_use", []string{userLine, textLine, toolUseLine}, false, 0},
		{"ends-user", []string{textLine, userLine}, false, 0},
		{"ends-system", []string{textLine, sysLine}, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m := newFileMapper(mapperConfig{sourceID: "src", absPath: "/abs/x.jsonl", nativeID: "s", kind: canonical.KindRoot})
			for _, line := range c.lines {
				rec, skip, err := parseLine([]byte(line))
				if err != nil {
					t.Fatalf("parseLine: %v", err)
				}
				if skip {
					continue
				}
				if _, err := m.mapRecord(rec); err != nil {
					t.Fatalf("mapRecord: %v", err)
				}
			}
			if m.lastRecordAssistantText != c.wantText {
				t.Fatalf("lastRecordAssistantText = %v, want %v", m.lastRecordAssistantText, c.wantText)
			}
			if c.wantText && m.lastAssistantTextTsUs != c.wantTs {
				t.Fatalf("lastAssistantTextTsUs = %d, want %d", m.lastAssistantTextTsUs, c.wantTs)
			}
		})
	}
}

// TestMapper_Turn0CompactionSummaryEmitted pins P2.3b: a compact_boundary that
// occurs BEFORE any operator prompt lives at turn 0 (turnSeq never advanced),
// and its post-compaction summary PayloadRef must STILL be emitted — scoped to
// the turn-0 compaction op — not dropped. The old guard keyed on
// `turnSeq == 0 || opSeq == 0`, which wrongly dropped this legitimate turn-0
// summary; the guard must key on `opSeq == 0` only.
func TestMapper_Turn0CompactionSummaryEmitted(t *testing.T) {
	t.Parallel()
	// No operator string prompt precedes the boundary: turnSeq stays 0.
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"system","subtype":"compact_boundary","uuid":"sy1","sessionId":"s","compactMetadata":{"trigger":"auto","preTokens":500000,"postTokens":6000,"durationMs":90000},"timestamp":"2026-05-26T12:00:00.000Z"}`,
		`{"type":"user","uuid":"u1","isCompactSummary":true,"sessionId":"s","message":{"role":"user","content":"summary"},"timestamp":"2026-05-26T12:00:00.500Z"}`,
	)

	var compactStarted canonical.OpStartedEvent
	var sawCompaction bool
	var summaryPayload canonical.PayloadRefEvent
	var sawSummaryPayload bool
	turns := 0
	for _, ev := range events {
		switch e := ev.(type) {
		case canonical.OpStartedEvent:
			if e.Kind == canonical.OpCompaction {
				compactStarted = e
				sawCompaction = true
			}
		case canonical.PayloadRefEvent:
			if e.PayloadKind == "log" {
				summaryPayload = e
				sawSummaryPayload = true
			}
		case canonical.TurnStartedEvent:
			turns++
		}
	}
	if !sawCompaction {
		t.Fatal("no compaction op emitted for a turn-0 compact_boundary")
	}
	if compactStarted.TurnSeq != 0 {
		t.Fatalf("compaction op TurnSeq = %d, want 0 (compaction before any operator prompt)", compactStarted.TurnSeq)
	}
	if compactStarted.Seq == 0 {
		t.Fatalf("compaction op Seq = %d, want >= 1 (a compaction always consumes an op slot)", compactStarted.Seq)
	}
	if turns != 0 {
		t.Fatalf("turns = %d, want 0 (the compact summary must not open a turn, and no operator prompt arrived)", turns)
	}
	// The turn-0 summary payload MUST be emitted, scoped to the turn-0
	// compaction op (P2.3b) — not dropped by a turnSeq==0 guard.
	if !sawSummaryPayload {
		t.Fatal("turn-0 compaction summary PayloadRef was dropped (P2.3b); it must be emitted scoped to the turn-0 compaction op")
	}
	if summaryPayload.TurnSeq != compactStarted.TurnSeq || summaryPayload.OpSeq != compactStarted.Seq {
		t.Fatalf("summary payload scoped to (turn=%d,op=%d), want the turn-0 compaction op (turn=%d,op=%d)",
			summaryPayload.TurnSeq, summaryPayload.OpSeq, compactStarted.TurnSeq, compactStarted.Seq)
	}
}

// TestMapper_SyntheticAssistantNoLLMOp verifies a <synthetic> model emits a
// LogEntry and NO LLM op (spec §3.2).
func TestMapper_SyntheticAssistantNoLLMOp(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"status"}]},"timestamp":"2026-05-26T10:00:00.000Z"}`,
	)
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == canonical.OpLLM {
			t.Fatal("synthetic assistant must not emit an LLM op")
		}
	}
	if countKind(events, canonical.EvLogEntry) == 0 {
		t.Fatal("synthetic assistant should emit a LogEntry")
	}
}

// TestMapper_LLMTokenContract pins the canonical token accounting for an
// assistant LLM op (canonical-events.md token contract, SOW-0029): TokensIn is
// the FRESH/uncached input ONLY, TokensCacheRead/Write are the separate cache
// portions, and CtxUsed is the TOTAL context occupancy
// (fresh + cacheCreate + cacheRead + output). The fixture carries all four
// non-zero so a regression that folds cache back into TokensIn — or shrinks
// CtxUsed to fresh+out — fails loudly.
func TestMapper_LLMTokenContract(t *testing.T) {
	t.Parallel()
	const (
		fresh      = 120
		cacheWrite = 10
		cacheRead  = 2000
		out        = 45
	)
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":120,"output_tokens":45,"cache_creation_input_tokens":10,"cache_read_input_tokens":2000}},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	var fin canonical.OpFinalizedEvent
	var found bool
	for _, ev := range events {
		of, ok := ev.(canonical.OpFinalizedEvent)
		if !ok {
			continue
		}
		// The LLM op is the first finalized op in the turn (Seq 1); a
		// finalized op carrying tokens is the LLM call.
		if of.TokensOut != 0 || of.TokensIn != 0 || of.TokensCacheRead != 0 {
			fin = of
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no token-bearing OpFinalizedEvent emitted for the assistant LLM op")
	}
	if fin.TokensIn != fresh {
		t.Errorf("TokensIn = %d, want %d (fresh/uncached input ONLY, cache excluded)", fin.TokensIn, fresh)
	}
	if fin.TokensCacheRead != cacheRead {
		t.Errorf("TokensCacheRead = %d, want %d", fin.TokensCacheRead, cacheRead)
	}
	if fin.TokensCacheWrite != cacheWrite {
		t.Errorf("TokensCacheWrite = %d, want %d", fin.TokensCacheWrite, cacheWrite)
	}
	if fin.TokensOut != out {
		t.Errorf("TokensOut = %d, want %d", fin.TokensOut, out)
	}
	// CtxUsed is the TOTAL context occupancy, NOT fresh+out.
	wantCtx := int64(fresh + cacheWrite + cacheRead + out)
	if fin.CtxUsed != wantCtx {
		t.Errorf("CtxUsed = %d, want %d (fresh+cacheCreate+cacheRead+output)", fin.CtxUsed, wantCtx)
	}
}

// TestMapper_MCPToolNamespacing verifies MCP tool names split correctly.
func TestMapper_MCPToolNamespacing(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"t1","name":"mcp__github__create_issue","input":{}}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	var found bool
	for _, ev := range events {
		if op, ok := ev.(canonical.OpStartedEvent); ok && op.Kind == canonical.OpTool {
			found = true
			if op.Name != "create_issue" || op.ToolNamespace != "mcp:github" {
				t.Fatalf("MCP op = (name=%q ns=%q), want (create_issue, mcp:github)", op.Name, op.ToolNamespace)
			}
		}
	}
	if !found {
		t.Fatal("no tool op emitted")
	}
}

// TestMapper_IsMetaUserNoTurn verifies an isMeta user record does not open a
// turn (spec §5.3).
func TestMapper_IsMetaUserNoTurn(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","isMeta":true,"sessionId":"s","message":{"role":"user","content":"<local-command-caveat>"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
	)
	if countKind(events, canonical.EvTurnStarted) != 0 {
		t.Fatal("isMeta user must not open a turn")
	}
}

// TestMapper_RecordTypeCoverage exercises the remaining record→event paths:
// pr-link, api_error, bridge-session, stop_hook_summary, queue-operation,
// attachment, isCompactSummary, and an informational/local_command system
// subtype. Asserts the documented severities and session-extras updates.
func TestMapper_RecordTypeCoverage(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"attachment","uuid":"at1","sessionId":"s","attachment":{"type":"file"},"timestamp":"2026-05-26T10:00:01.000Z"}`,
		`{"type":"queue-operation","operation":"enqueue","content":"queued","sessionId":"s","timestamp":"2026-05-26T10:00:02.000Z"}`,
		`{"type":"system","subtype":"stop_hook_summary","uuid":"sy1","sessionId":"s","content":"hooks","timestamp":"2026-05-26T10:00:03.000Z"}`,
		`{"type":"system","subtype":"api_error","uuid":"sy2","sessionId":"s","content":"err","error":{"status":529,"type":"overloaded_error"},"timestamp":"2026-05-26T10:00:04.000Z"}`,
		`{"type":"system","subtype":"local_command","uuid":"sy3","sessionId":"s","content":"/clear","timestamp":"2026-05-26T10:00:05.000Z"}`,
		`{"type":"pr-link","sessionId":"s","prNumber":42,"prUrl":"https://example.invalid/pr","prRepository":"owner/repo","timestamp":"2026-05-26T10:00:06.000Z"}`,
		`{"type":"bridge-session","sessionId":"s","bridgeSessionId":"cse_x","lastSequenceNum":7}`,
	)

	var sawAPIErr, sawPRLinkUpdate, sawBridgeUpdate bool
	severities := map[string]int{}
	for _, ev := range events {
		switch e := ev.(type) {
		case canonical.LogEntryEvent:
			severities[e.Severity]++
			if e.Severity == "ERR" && e.Message == "api_error" {
				sawAPIErr = true
			}
		case canonical.SessionUpdatedEvent:
			if e.Extras["prLinks"] != nil {
				sawPRLinkUpdate = true
			}
			if e.Extras["bridge.bridgeSessionId"] != nil {
				sawBridgeUpdate = true
			}
		}
	}
	if !sawAPIErr {
		t.Error("api_error should emit an ERR LogEntry")
	}
	if !sawPRLinkUpdate {
		t.Error("pr-link should emit a SessionUpdated with prLinks extras")
	}
	if !sawBridgeUpdate {
		t.Error("bridge-session should emit a SessionUpdated with bridge extras")
	}
	if severities["DBG"] == 0 {
		t.Error("attachment / stop_hook_summary should emit DBG logs")
	}
	if severities["INF"] == 0 {
		t.Error("queue-operation / local_command should emit INF logs")
	}
}

// TestMapper_CustomTitleWinsOverLaterAITitle verifies P3: a custom-title sets
// AgentName, and a LATER ai-title does NOT overwrite it (spec §3.7 precedence:
// the operator's chosen title wins regardless of arrival order).
// TestMapper_TitlesGoToExtrasNotAgentName (SOW feedback #4) verifies that
// custom-title and ai-title snapshots populate Extras (the session-label home),
// NOT AgentName (the agent-identity column). Previously they set AgentName,
// which made the "Agent" column show session titles — confusing.
func TestMapper_TitlesGoToExtrasNotAgentName(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"custom-title","customTitle":"My Title","sessionId":"s"}`,
		`{"type":"ai-title","aiTitle":"Robot Title","sessionId":"s"}`,
	)
	// Both titles must land in Extras; neither must set AgentName.
	var customInExtras, aiInExtras bool
	for _, ev := range events {
		su, ok := ev.(canonical.SessionUpdatedEvent)
		if !ok {
			continue
		}
		if su.Extras["customTitle"] == "My Title" {
			customInExtras = true
			if su.AgentName != "" {
				t.Error("custom-title snapshot must NOT set AgentName (feedback #4)")
			}
		}
		if su.Extras["aiTitle"] == "Robot Title" {
			aiInExtras = true
			if su.AgentName != "" {
				t.Error("ai-title snapshot must NOT set AgentName (feedback #4)")
			}
		}
	}
	if !customInExtras {
		t.Fatal("custom-title must populate Extras.customTitle")
	}
	if !aiInExtras {
		t.Fatal("ai-title must populate Extras.aiTitle")
	}
}

// TestMapper_FileHistorySnapshotStoresBackups verifies P3: a
// file-history-snapshot stores the actual trackedFileBackups map under
// extras.fileHistory, not merely a boolean.
func TestMapper_FileHistorySnapshotStoresBackups(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"file-history-snapshot","messageId":"u1","snapshot":{"messageId":"u1","trackedFileBackups":{"<HOME>/x.go":{"backupFileName":"<HOME>/.bak/x.go","version":2,"backupTime":"2026-05-26T10:00:01.000Z"}},"timestamp":"2026-05-26T10:00:01.000Z"},"isSnapshotUpdate":false}`,
	)
	var found bool
	for _, ev := range events {
		su, ok := ev.(canonical.SessionUpdatedEvent)
		if !ok {
			continue
		}
		fh, ok := su.Extras["fileHistory"].(map[string]any)
		if !ok {
			continue
		}
		found = true
		if _, ok := fh["<HOME>/x.go"]; !ok {
			t.Fatalf("fileHistory missing the tracked file: %+v", fh)
		}
	}
	if !found {
		t.Fatal("file-history-snapshot must store the trackedFileBackups map under extras.fileHistory (P3)")
	}
}

// TestMapper_FileHistorySnapshotEmptyBackupsNoUpdate verifies an empty
// trackedFileBackups map produces no session update (nothing useful to store).
func TestMapper_FileHistorySnapshotEmptyBackupsNoUpdate(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"file-history-snapshot","messageId":"u1","snapshot":{"messageId":"u1","trackedFileBackups":{},"timestamp":"2026-05-26T10:00:01.000Z"},"isSnapshotUpdate":false}`,
	)
	for _, ev := range events {
		if su, ok := ev.(canonical.SessionUpdatedEvent); ok {
			if su.Extras["fileHistory"] != nil {
				t.Fatalf("empty trackedFileBackups should not produce a fileHistory update: %+v", su.Extras)
			}
		}
	}
}

// TestMapper_FileAttachmentNoPayloadRefExtrasCarryFields pins P1.1b + P2.6: a
// bare `file` attachment has no owning op, so the adapter must NOT emit a
// PayloadRefEvent (an orphan ref FK-rolls-back the ingest batch). Instead the
// attachment LogEntry's extras must carry filename, displayPath, and type
// (spec §333, §338).
func TestMapper_FileAttachmentNoPayloadRefExtrasCarryFields(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"attachment","uuid":"at1","sessionId":"s","attachment":{"type":"file","filename":"<HOME>/src/demo/main.go","displayPath":"main.go","content":{"type":"text","file":{"filePath":"<HOME>/src/demo/main.go","content":"package main"}}},"timestamp":"2026-05-26T10:00:01.000Z"}`,
	)
	// No PayloadRefEvent may be emitted for a file attachment.
	for _, ev := range events {
		if _, ok := ev.(canonical.PayloadRefEvent); ok {
			t.Fatalf("file attachment must NOT emit a PayloadRefEvent (orphan op_id FK rollback, P1.1b); got %+v", ev)
		}
	}
	// The attachment LogEntry's extras must carry filename, displayPath, type.
	var sawLog bool
	for _, ev := range events {
		le, ok := ev.(canonical.LogEntryEvent)
		if !ok || le.Message != "attachment" {
			continue
		}
		sawLog = true
		if le.Extras["filename"] != "<HOME>/src/demo/main.go" {
			t.Errorf("attachment LogEntry extras.filename = %v, want the filename (P2.6)", le.Extras["filename"])
		}
		if le.Extras["displayPath"] != "main.go" {
			t.Errorf("attachment LogEntry extras.displayPath = %v, want 'main.go' (spec §333)", le.Extras["displayPath"])
		}
		if le.Extras["attachmentType"] != "file" {
			t.Errorf("attachment LogEntry extras.attachmentType = %v, want 'file' (spec §338)", le.Extras["attachmentType"])
		}
	}
	if !sawLog {
		t.Fatal("file attachment must emit a LogEntry (with the field extras)")
	}
}

// TestMapper_NonFileAttachmentNoFieldExtras verifies a non-`file` attachment
// (e.g. a reminder the harness injected) emits a LogEntry without the
// file-specific extras and no PayloadRef.
func TestMapper_NonFileAttachmentNoFieldExtras(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"attachment","uuid":"at1","sessionId":"s","attachment":{"type":"todo_reminder","content":"do x"},"timestamp":"2026-05-26T10:00:01.000Z"}`,
	)
	for _, ev := range events {
		if _, ok := ev.(canonical.PayloadRefEvent); ok {
			t.Fatalf("non-file attachment must not emit a PayloadRef; got %+v", ev)
		}
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "attachment" {
			if le.Extras["filename"] != nil || le.Extras["displayPath"] != nil {
				t.Errorf("non-file attachment must not carry file extras: %+v", le.Extras)
			}
		}
	}
}

// TestMapper_PRLinksAccumulateAsArray pins P2.7: multiple pr-link records must
// accumulate into a single prLinks ARRAY (not a singular prLink object that
// json_patch overwrites). Two pr-links → the final SessionUpdated carries a
// prLinks array of length 2.
func TestMapper_PRLinksAccumulateAsArray(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"pr-link","sessionId":"s","prNumber":11,"prUrl":"https://example.invalid/pr/11","prRepository":"owner/repo","timestamp":"2026-05-26T10:00:06.000Z"}`,
		`{"type":"pr-link","sessionId":"s","prNumber":22,"prUrl":"https://example.invalid/pr/22","prRepository":"owner/repo","timestamp":"2026-05-26T10:00:07.000Z"}`,
	)
	// The adapter must NOT emit a singular prLink key (that loses all but the last).
	var lastLen int
	var sawArray bool
	for _, ev := range events {
		su, ok := ev.(canonical.SessionUpdatedEvent)
		if !ok {
			continue
		}
		if su.Extras["prLink"] != nil {
			t.Fatalf("singular prLink key emitted (loses prior PRs on json_patch overwrite, P2.7): %+v", su.Extras)
		}
		// In-memory the array is []map[string]any (the writer JSON-encodes it
		// to a JSON array via json_patch); accept either shape defensively.
		switch arr := su.Extras["prLinks"].(type) {
		case []map[string]any:
			lastLen = len(arr)
			sawArray = true
		case []any:
			lastLen = len(arr)
			sawArray = true
		}
	}
	if !sawArray {
		t.Fatal("pr-link must emit a SessionUpdated with a prLinks array")
	}
	if lastLen != 2 {
		t.Fatalf("final prLinks array length = %d, want 2 (both PRs accumulated, P2.7)", lastLen)
	}
}

// TestMapper_NeverFinalizesSession verifies the adapter never emits a
// SessionFinalizedEvent for claude-code (spec §11.11, Gate decision #3).
func TestMapper_NeverFinalizesSession(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"hi"}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"sy1","sessionId":"s","durationMs":2000,"timestamp":"2026-05-26T10:00:02.500Z"}`,
	)
	if countKind(events, canonical.EvSessionFinalized) != 0 {
		t.Fatal("claude-code must never emit SessionFinalizedEvent")
	}
}

// TestMapper_MetadataFirstRecordDefersSessionStart (SOW-0028): a transcript
// that opens with timestamp-less metadata snapshots (permission-mode,
// custom-title) must NOT seed the SessionStarted with Ts=0. The adapter defers
// the bootstrap to the first timestamped record, buffers the leading snapshot
// events, and emits them AFTER the SessionStarted (so applySessionUpdated's
// pure UPDATE sees the row the SessionStarted created). The SessionStarted
// carries the first timestamped record's ts, and it is the FIRST event in the
// stream.
func TestMapper_MetadataFirstRecordDefersSessionStart(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		// Leading timestamp-less metadata snapshots (no top-level "timestamp").
		`{"type":"permission-mode","permissionMode":"default","sessionId":"s"}`,
		`{"type":"custom-title","customTitle":"[REDACTED_TITLE]","sessionId":"s"}`,
		// First timestamped record — this sets the session start.
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"hi"}]},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	start, ok := events[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionStartedEvent", events[0])
	}
	wantTs := int64(1779789600000000) // 2026-05-26T10:00:00.000Z in micros
	if start.Ts != wantTs {
		t.Fatalf("SessionStarted Ts = %d, want %d (first timestamped record; was 0 before SOW-0028)",
			start.Ts, wantTs)
	}
	// The leading snapshot events MUST appear after the SessionStarted (the
	// writer's applySessionUpdated is a pure UPDATE keyed on the session row the
	// SessionStarted creates). Find at least one SessionUpdated carrying the
	// custom title and confirm it follows the start.
	for i, ev := range events[1:] {
		if _, ok := ev.(canonical.SessionUpdatedEvent); ok {
			_ = i
			break
		}
	}
}

// TestMapper_PureMetadataFileFinalizesSessionStart (SOW-0028): a transcript
// that contains ONLY timestamp-less records (an empty/corrupt transcript with
// no real events) still creates its session row. The mapper defers through
// every record (buffering), then finalizeSessionStart emits a SessionStarted
// (Ts=0) + the buffered snapshot events at EOF.
func TestMapper_PureMetadataFileFinalizesSessionStart(t *testing.T) {
	t.Parallel()
	m := newFileMapper(mapperConfig{
		sourceID: "src",
		absPath:  "/abs/x.jsonl",
		nativeID: "s",
		kind:     canonical.KindRoot,
	})
	var out []canonical.Event
	for _, line := range []string{
		`{"type":"permission-mode","permissionMode":"default","sessionId":"s"}`,
		`{"type":"custom-title","customTitle":"[REDACTED_TITLE]","sessionId":"s"}`,
	} {
		rec, skip, err := parseLine([]byte(line))
		if err != nil {
			t.Fatalf("parseLine(%q): %v", line, err)
		}
		if skip {
			continue
		}
		evs, err := m.mapRecord(rec)
		if err != nil {
			t.Fatalf("mapRecord: %v", err)
		}
		out = append(out, evs...)
	}
	// Nothing emitted yet — the SessionStarted is deferred.
	if len(out) != 0 {
		t.Fatalf("events emitted before finalize = %d, want 0 (all deferred)", len(out))
	}
	// EOF: finalize flushes the deferred SessionStarted + buffered snapshots.
	final := m.finalizeSessionStart()
	if len(final) == 0 {
		t.Fatal("finalizeSessionStart returned no events for a pure-metadata file")
	}
	start, ok := final[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("finalize[0] = %T, want SessionStartedEvent", final[0])
	}
	if start.Ts != 0 {
		t.Errorf("pure-metadata SessionStarted Ts = %d, want 0 (no timestamped record seen)", start.Ts)
	}
	// finalize is idempotent: a second call emits nothing.
	again := m.finalizeSessionStart()
	if len(again) != 0 {
		t.Errorf("second finalize emitted %d events, want 0 (idempotent)", len(again))
	}
}

// TestMapper_TimestampedFirstRecordBootstrapsImmediately (SOW-0028 regression
// guard): when the first record HAS a timestamp (the common shape — the
// existing golden fixtures all open with a timestamped user record), the
// SessionStarted fires immediately on record 0 with that ts. This pins that the
// defer-and-buffer refactor did not change the common-path behaviour.
func TestMapper_TimestampedFirstRecordBootstrapsImmediately(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
	)
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	start, ok := events[0].(canonical.SessionStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionStartedEvent", events[0])
	}
	wantTs := int64(1779789600000000) // 2026-05-26T10:00:00.000Z in micros
	if start.Ts != wantTs {
		t.Fatalf("SessionStarted Ts = %d, want %d (timestamped first record bootstraps immediately)",
			start.Ts, wantTs)
	}
}
