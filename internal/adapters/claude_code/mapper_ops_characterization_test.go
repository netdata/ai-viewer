package claude_code

import (
	"net/url"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestMapper_UserMultipleToolResultsEmitsExactPayloadsAndOneEcho(t *testing.T) {
	t.Parallel()

	events := mapAll(t, "s", "", canonical.KindRoot, "", nil,
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"run tools"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","content":[{"type":"tool_use","id":"toolu_first","name":"Read","input":{}},{"type":"tool_use","id":"toolu_second","name":"Bash","input":{}}]},"timestamp":"2026-05-26T10:00:01.000Z"}`,
		`{"type":"user","uuid":"u2","sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_second","content":"[REDACTED_TOOL_OUTPUT]","is_error":false},{"type":"tool_result","tool_use_id":"toolu_first","content":"[REDACTED_TOOL_OUTPUT]","is_error":false}]},"toolUseResult":{"stdout":"[REDACTED_TOOL_OUTPUT]"},"timestamp":"2026-05-26T10:00:02.000Z"}`,
	)

	finalized := map[int]canonical.OpFinalizedEvent{}
	var payloads []canonical.PayloadRefEvent
	for _, ev := range events {
		switch e := ev.(type) {
		case canonical.OpFinalizedEvent:
			if e.TurnSeq == 1 && (e.Seq == 3 || e.Seq == 4) {
				finalized[e.Seq] = e
			}
		case canonical.PayloadRefEvent:
			if e.PayloadKind == "tool_response" {
				payloads = append(payloads, e)
			}
		}
	}

	for _, seq := range []int{3, 4} {
		ev, ok := finalized[seq]
		if !ok {
			t.Fatalf("tool op seq %d was not finalized", seq)
		}
		if ev.Status != "completed" {
			t.Fatalf("tool op seq %d status = %q, want completed", seq, ev.Status)
		}
	}
	if len(payloads) != 3 {
		t.Fatalf("tool_response payload refs = %d, want 3: two block payloads plus one record-level toolUseResult", len(payloads))
	}
	wantPointers := map[string]int{
		"/message/content/0/content": 4,
		"/message/content/1/content": 3,
		"/toolUseResult":             4,
	}
	for _, payload := range payloads {
		pointer := payloadRefJSONPointer(t, payload.LocationURI)
		opSeq, ok := wantPointers[pointer]
		if !ok {
			t.Fatalf("unexpected tool_response payload pointer %q in URI %q", pointer, payload.LocationURI)
		}
		if payload.TurnSeq != 1 || payload.OpSeq != opSeq {
			t.Fatalf("payload %s scoped to turn=%d op=%d, want turn=1 op=%d",
				pointer, payload.TurnSeq, payload.OpSeq, opSeq)
		}
		delete(wantPointers, pointer)
	}
	if len(wantPointers) != 0 {
		t.Fatalf("missing payload pointers: %v", wantPointers)
	}
}

func payloadRefJSONPointer(t *testing.T, locationURI string) string {
	t.Helper()

	parsed, err := url.Parse(locationURI)
	if err != nil {
		t.Fatalf("parse LocationURI %q: %v", locationURI, err)
	}
	return parsed.Query().Get("json_pointer")
}

func TestMapper_AssistantMixedThinkingAndToolsPreservesOpOrder(t *testing.T) {
	t.Parallel()

	const agentToolUseID = "toolu_agent_mixed"
	events := mapAll(t, "s", "", canonical.KindRoot, "", map[string]string{agentToolUseID: "abc123def456789"},
		`{"type":"user","uuid":"u1","sessionId":"s","message":{"role":"user","content":"plan work"},"timestamp":"2026-05-26T10:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","sessionId":"s","message":{"id":"m1","role":"assistant","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":4},"content":[{"type":"thinking","thinking":"first private note","signature":"sig-a"},{"type":"tool_use","id":"toolu_read","name":"Read","input":{}},{"type":"thinking","thinking":"second private note","signature":"sig-b"},{"type":"tool_use","id":"`+agentToolUseID+`","name":"Agent","input":{"description":"Investigate helper boundary"}}]},"timestamp":"2026-05-26T10:00:01.000Z"}`,
	)

	got, agentOp, sawAgent := collectOpTrace(events)
	assertOpTrace(t, got, mixedAssistantOpTrace())
	if !sawAgent {
		t.Fatal("Agent tool_use did not emit a session op")
	}
	if got := extrasToolUseId(agentOp.Extras); got != agentToolUseID {
		t.Fatalf("Agent op toolUseId stash = %q, want %q", got, agentToolUseID)
	}
	if agentOp.ChildSessionNativeID != "s:agent:abc123def456789" {
		t.Fatalf("Agent op ChildSessionNativeID = %q, want s:agent:abc123def456789", agentOp.ChildSessionNativeID)
	}
}

func TestMapper_SnapshotLastPromptUpdatesExtras(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	lastPrompt := mustSnapshotUpdate(t, m, `{"type":"last-prompt","lastPrompt":"[REDACTED_USER_MESSAGE]","leafUuid":"u1","sessionId":"s"}`)
	if lastPrompt.Extras["lastPrompt"] != "[REDACTED_USER_MESSAGE]" || lastPrompt.AgentName != "" {
		t.Fatalf("last-prompt update = extras %+v AgentName %q", lastPrompt.Extras, lastPrompt.AgentName)
	}
}

func TestMapper_SnapshotTitlesPopulateExtrasNotAgentName(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	aiTitle := mustSnapshotUpdate(t, m, `{"type":"ai-title","aiTitle":"AI generated title","sessionId":"s"}`)
	if aiTitle.Extras["aiTitle"] != "AI generated title" || aiTitle.AgentName != "" {
		t.Fatalf("ai-title: extras %+v AgentName %q (AgentName must be empty — titles go to Extras only, feedback #4)", aiTitle.Extras, aiTitle.AgentName)
	}

	customTitle := mustSnapshotUpdate(t, m, `{"type":"custom-title","customTitle":"Pinned title","sessionId":"s"}`)
	if customTitle.Extras["customTitle"] != "Pinned title" || customTitle.AgentName != "" {
		t.Fatalf("custom-title: extras %+v AgentName %q (AgentName must be empty — titles go to Extras only, feedback #4)", customTitle.Extras, customTitle.AgentName)
	}
}

func TestMapper_SnapshotPermissionModeUpdatesExtras(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	permission := mustSnapshotUpdate(t, m, `{"type":"permission-mode","permissionMode":"acceptEdits","sessionId":"s"}`)
	if permission.Extras["permissionMode"] != "acceptEdits" || permission.AgentName != "" {
		t.Fatalf("permission-mode update = extras %+v AgentName %q", permission.Extras, permission.AgentName)
	}
}

func TestMapper_SnapshotBridgeSessionUpdatesExtras(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	bridge := mustSnapshotUpdate(t, m, `{"type":"bridge-session","sessionId":"s","bridgeSessionId":"cse_example","lastSequenceNum":12}`)
	if bridge.Extras["bridge.bridgeSessionId"] != "cse_example" || bridge.Extras["bridge.lastSequenceNum"] != float64(12) {
		t.Fatalf("bridge-session update extras = %+v", bridge.Extras)
	}
}

func TestMapper_SnapshotFileHistoryStoresBackups(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	history := mustSnapshotUpdate(t, m, `{"type":"file-history-snapshot","messageId":"u1","snapshot":{"messageId":"u1","trackedFileBackups":{"project/file.go":{"backupFileName":"project/.backup/file.go","version":3,"backupTime":"2026-05-26T10:00:01.000Z"}},"timestamp":"2026-05-26T10:00:01.000Z"},"isSnapshotUpdate":false}`)
	fileHistory, ok := history.Extras["fileHistory"].(map[string]any)
	if !ok {
		t.Fatalf("file-history update missing fileHistory map: %+v", history.Extras)
	}
	backup, ok := fileHistory["project/file.go"].(map[string]any)
	if !ok {
		t.Fatalf("fileHistory missing project/file.go backup: %+v", fileHistory)
	}
	if backup["backupFileName"] != "project/.backup/file.go" || backup["version"] != float64(3) {
		t.Fatalf("fileHistory backup = %+v", backup)
	}
}

func TestMapper_SnapshotEmptyFileHistoryEmitsNoUpdate(t *testing.T) {
	t.Parallel()

	m := newSnapshotMapper()
	if ev := snapshotEvent(t, m, `{"type":"file-history-snapshot","messageId":"u2","snapshot":{"messageId":"u2","trackedFileBackups":{},"timestamp":"2026-05-26T10:00:02.000Z"},"isSnapshotUpdate":false}`); ev != nil {
		t.Fatalf("empty file-history-snapshot emitted update %+v, want nil", ev)
	}
}

type opTrace struct {
	event  string
	kind   canonical.OpKind
	seq    int
	parent int
	name   string
}

func mixedAssistantOpTrace() []opTrace {
	return []opTrace{
		{event: "start", kind: canonical.OpInternal, seq: 1, parent: -1, name: "user_input"},
		{event: "final", seq: 1},
		{event: "start", kind: canonical.OpLLM, seq: 2, parent: -1, name: "claude-opus-4-7"},
		{event: "final", seq: 2},
		{event: "start", kind: canonical.OpReasoning, seq: 3, parent: 2},
		{event: "final", seq: 3},
		{event: "start", kind: canonical.OpReasoning, seq: 4, parent: 2},
		{event: "final", seq: 4},
		{event: "start", kind: canonical.OpTool, seq: 5, parent: -1, name: "Read"},
		{event: "start", kind: canonical.OpSession, seq: 6, parent: -1, name: "Investigate helper boundary"},
	}
}

func collectOpTrace(events []canonical.Event) ([]opTrace, canonical.OpStartedEvent, bool) {
	var agentOp canonical.OpStartedEvent
	var sawAgent bool
	got := make([]opTrace, 0, len(events))
	for _, ev := range events {
		switch e := ev.(type) {
		case canonical.OpStartedEvent:
			got = append(got, opTrace{event: "start", kind: e.Kind, seq: e.Seq, parent: e.ParentOpSeq, name: e.Name})
			if e.Kind == canonical.OpSession {
				agentOp = e
				sawAgent = true
			}
		case canonical.OpFinalizedEvent:
			got = append(got, opTrace{event: "final", seq: e.Seq})
		}
	}
	return got, agentOp, sawAgent
}

func assertOpTrace(t *testing.T, got, want []opTrace) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("op event count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("op event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func newSnapshotMapper() *fileMapper {
	return newFileMapper(mapperConfig{
		sourceID: "src",
		absPath:  "/abs/x.jsonl",
		nativeID: "s",
		kind:     canonical.KindRoot,
	})
}

func mustSnapshotUpdate(t *testing.T, m *fileMapper, line string) canonical.SessionUpdatedEvent {
	t.Helper()

	ev := snapshotEvent(t, m, line)
	if ev == nil {
		t.Fatalf("snapshot line emitted no update: %s", line)
	}
	update, ok := ev.(canonical.SessionUpdatedEvent)
	if !ok {
		t.Fatalf("snapshot line emitted %T, want SessionUpdatedEvent", ev)
	}
	return update
}

func snapshotEvent(t *testing.T, m *fileMapper, line string) canonical.Event {
	t.Helper()

	rec, skip, err := parseLine([]byte(line))
	if err != nil {
		t.Fatalf("parseLine: %v", err)
	}
	if skip {
		t.Fatalf("snapshot line skipped unexpectedly: %s", line)
	}
	return m.mapSnapshot(rec, canonical.EventBase{SourceID: "src"})
}
