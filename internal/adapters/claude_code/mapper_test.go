package claude_code

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapAll runs a fresh fileMapper over the given parsed records and returns
// the concatenated events. agentMap seeds the toolUseId→agentId map.
func mapAll(t *testing.T, nativeID, parentNativeID string, kind canonical.SessionKind, agentName string, agentMap map[string]string, lines ...string) []canonical.Event {
	t.Helper()
	m := newFileMapper("src", "/abs/x.jsonl", nativeID, parentNativeID, kind, agentName, agentMap)
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

// TestMapper_CompactionOp verifies acceptance #4: a compact_boundary emits a
// first-class OpKind='compaction' op with the documented field mapping, and
// the post-compaction isCompactSummary user message does NOT open a turn.
func TestMapper_CompactionOp(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"first"},"timestamp":"2026-05-26T12:00:00.000Z"}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"sy1","sessionId":"s","compactMetadata":{"trigger":"manual","preTokens":700000,"postTokens":8000,"durationMs":110000},"timestamp":"2026-05-26T12:02:00.000Z"}`,
		`{"type":"user","uuid":"u2","isCompactSummary":true,"sessionId":"s","message":{"role":"user","content":"summary"},"timestamp":"2026-05-26T12:02:00.500Z"}`,
		`{"type":"user","uuid":"u3","sessionId":"s","message":{"role":"user","content":"second"},"timestamp":"2026-05-26T12:03:00.000Z"}`,
	)

	var compactionOps, turns int
	var compactStarted canonical.OpStartedEvent
	var compactFinal canonical.OpFinalizedEvent
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
		}
	}
	if compactionOps != 1 {
		t.Fatalf("compaction ops = %d, want 1", compactionOps)
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
			if e.Extras["prLink"] != nil {
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
		t.Error("pr-link should emit a SessionUpdated with prLink extras")
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
