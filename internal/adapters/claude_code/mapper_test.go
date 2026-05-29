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
func TestMapper_CustomTitleWinsOverLaterAITitle(t *testing.T) {
	t.Parallel()
	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"go"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"custom-title","customTitle":"My Title","sessionId":"s"}`,
		`{"type":"ai-title","aiTitle":"Robot Title","sessionId":"s"}`,
	)
	// The custom-title update must set AgentName; the ai-title update must NOT.
	var customSet, aiClobbered bool
	for _, ev := range events {
		su, ok := ev.(canonical.SessionUpdatedEvent)
		if !ok {
			continue
		}
		if su.Extras["customTitle"] == "My Title" && su.AgentName == "My Title" {
			customSet = true
		}
		if su.Extras["aiTitle"] == "Robot Title" && su.AgentName != "" {
			aiClobbered = true
		}
	}
	if !customSet {
		t.Fatal("custom-title must set AgentName")
	}
	if aiClobbered {
		t.Fatal("ai-title after a custom-title must NOT set AgentName (custom wins, P3)")
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
