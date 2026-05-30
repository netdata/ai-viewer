package codex

import (
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestMapper_ImageGenerationOp covers mapImageGenCall + image_generation_end
// enrichment (spec rule #12): one media tool op.
func TestMapper_ImageGenerationOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"image_generation_call","id":"i1","status":"completed","call_id":"g1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"image_generation_end","call_id":"g1"}}`,
	}
	events := runLines(t, m, lines)
	media := 0
	for _, s := range opStarts(events) {
		if s.Name == "image_generation" && s.ToolNamespace == "media" {
			media++
		}
	}
	if media != 1 {
		t.Fatalf("image_generation op count = %d, want 1", media)
	}
}

// TestMapper_CustomToolCall covers the custom namespace branch (spec rule #10).
func TestMapper_CustomToolCall(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"my_tool","input":"i"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"o"}}`,
	}
	events := runLines(t, m, lines)
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpTool && s.Name == "my_tool" && s.ToolNamespace != "custom" {
			t.Errorf("custom_tool_call namespace = %q, want custom", s.ToolNamespace)
		}
	}
}

// TestMapper_LocalShellLegacy covers the legacy local_shell_call path (spec rule
// #13): shell namespace, default name "shell".
func TestMapper_LocalShellLegacy(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"local_shell_call","call_id":"l1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"local_shell_call_output","call_id":"l1","output":"done"}}`,
	}
	events := runLines(t, m, lines)
	shell := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpTool && s.ToolNamespace == "shell" {
			shell++
		}
	}
	if shell != 1 {
		t.Fatalf("local_shell op (shell namespace) count = %d, want 1", shell)
	}
}

// TestMapper_ToolSearchOp covers the tool_search_call/output pair (spec
// adapter-codex.md:158).
func TestMapper_ToolSearchOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"tool_search_call","call_id":"ts1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"tool_search_output","call_id":"ts1","output":"results"}}`,
	}
	events := runLines(t, m, lines)
	got := 0
	for _, s := range opStarts(events) {
		if s.Name == "tool_search" {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("tool_search op count = %d, want 1", got)
	}
}

// TestMapper_FsToolNamespaces covers the fs-namespace heuristic for the read/
// write/edit/list_dir/view_image/apply_patch names (spec rule #9).
func TestMapper_FsToolNamespaces(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"read", "write", "edit", "list_dir", "view_image", "apply_patch"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newTestMapper("sid")
			lines := []string{
				metaLine("sid", `"exec"`),
				`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
				`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"` + name + `","arguments":"{}","call_id":"c1"}}`,
			}
			events := runLines(t, m, lines)
			for _, s := range opStarts(events) {
				if s.Kind == canonical.OpTool && s.Name == name && s.ToolNamespace != "fs" {
					t.Errorf("%s namespace = %q, want fs", name, s.ToolNamespace)
				}
			}
		})
	}
}

// TestMapper_ExecPrefixShellNamespace covers the "exec*" → shell branch and the
// default "custom" branch (spec rule #9).
func TestMapper_ExecPrefixAndDefaultNamespace(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"exec_command":  "shell",
		"shell_command": "shell",
		"weird_tool":    "custom",
	}
	for name, wantNS := range cases {
		name, wantNS := name, wantNS
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newTestMapper("sid")
			lines := []string{
				metaLine("sid", `"exec"`),
				`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
				`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"` + name + `","arguments":"{}","call_id":"c1"}}`,
			}
			events := runLines(t, m, lines)
			for _, s := range opStarts(events) {
				if s.Kind == canonical.OpTool && s.Name == name && s.ToolNamespace != wantNS {
					t.Errorf("%s namespace = %q, want %q", name, s.ToolNamespace, wantNS)
				}
			}
		})
	}
}

// TestMapper_ObjectOutputErrorStatus covers outputStatus/outputText for an
// object-shaped output carrying an error (spec rule #9, edge #5 tool_error).
func TestMapper_ObjectOutputErrorStatus(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":{"content":"command failed: exit code 1"}}}`,
	}
	events := runLines(t, m, lines)
	failed := false
	for _, f := range opFinals(events) {
		if f.Status == "failed" && f.ErrorClass == "tool_error" {
			failed = true
		}
	}
	if !failed {
		t.Errorf("object-output error did not finalize failed/tool_error")
	}
}

// TestMapper_AgentMessageStashedAndDeduped covers stashAgentMessage and the
// agent_message DBG log (spec rule #19): the assistant op comes from
// response_item.message; agent_message only stashes the preview.
func TestMapper_AgentMessageStashedAndDeduped(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the answer"}]}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"agent_message","message":"the answer","phase":"final_answer"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)
	// Exactly one LLM op (no duplicate from agent_message).
	llm := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpLLM {
			llm++
		}
	}
	if llm != 1 {
		t.Fatalf("LLM op count = %d, want 1 (agent_message must not add a second)", llm)
	}
	// The turn_meta log carries the stashed last_agent_message.
	var meta canonical.LogEntryEvent
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "turn_meta" {
			meta = le
		}
	}
	if meta.Extras["last_agent_message"] != "the answer" {
		t.Errorf("last_agent_message = %v, want 'the answer'", meta.Extras["last_agent_message"])
	}
}

// TestMapper_EventError covers the event_msg.error → ERR LogEntry path.
func TestMapper_EventError(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"error","message":"boom happened"}}`,
	}
	events := runLines(t, m, lines)
	errLog := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Severity == "ERR" && le.Message == "error" {
			errLog = true
			if le.Extras["message"] != "boom happened" {
				t.Errorf("error extras message = %v, want 'boom happened'", le.Extras["message"])
			}
		}
	}
	if !errLog {
		t.Errorf("event_msg.error did not surface an ERR log")
	}
}

// TestMapper_ModelLearnedOnceAcrossTurns asserts SessionUpdated(Model) is
// emitted only the FIRST time a model is learned, even across multiple
// turn_context records (spec rule #2).
func TestMapper_ModelLearnedOnceAcrossTurns(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsEvent + `","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.6"}}`,
	}
	events := runLines(t, m, lines)
	if got := countKind(events, canonical.EvSessionUpdated); got != 1 {
		t.Fatalf("SessionUpdated count = %d, want 1 (model announced once)", got)
	}
}

// TestMapper_EnrichOnAlreadyFinalizedOpLogs covers enrichFinalizedOrLog: an
// exec_command_end whose op was ALREADY finalized by its function_call_output
// surfaces a DBG enrichment log (spec rule #14 supplementary telemetry).
func TestMapper_EnrichOnAlreadyFinalizedOpLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		// output finalizes c1 first (deletes it from openOps).
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`,
		// exec_command_end now arrives for the already-finalized op.
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":0,"aggregated_output":"ok","source":"model"}}`,
	}
	events := runLines(t, m, lines)
	dbg := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "enrich_exec_command_end" {
			dbg = true
		}
	}
	if !dbg {
		t.Errorf("late exec_command_end on finalized op did not surface a DBG enrichment log")
	}
}

// TestMapper_McpEndUnmatchedLogs covers the mcp_tool_call_end no-op-match branch.
func TestMapper_McpEndUnmatchedLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"orphan","invocation":{"server":"gh","tool":"x"}}}`,
	}
	events := runLines(t, m, lines)
	dbg := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "enrich_mcp_tool_call_end" {
			dbg = true
		}
	}
	if !dbg {
		t.Errorf("unmatched mcp_tool_call_end did not surface a DBG log")
	}
}

// TestMapper_PatchApplyEndUnmatchedLogs covers the patch_apply_end no-match
// branch.
func TestMapper_PatchApplyEndUnmatchedLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"orphan","success":true}}`,
	}
	events := runLines(t, m, lines)
	dbg := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "enrich_patch_apply_end" {
			dbg = true
		}
	}
	if !dbg {
		t.Errorf("unmatched patch_apply_end did not surface a DBG log")
	}
}

// TestMapper_TaskCompleteNoTurnWarns covers the stray-task_complete branch.
func TestMapper_TaskCompleteNoTurnWarns(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"ghost","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)
	warn := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "task_complete_no_turn" {
			warn = true
		}
	}
	if !warn {
		t.Errorf("stray task_complete did not surface a WRN log")
	}
}

// TestMapper_TurnAbortedNoTurnWarns covers the stray-turn_aborted branch.
func TestMapper_TurnAbortedNoTurnWarns(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"ghost","reason":"interrupted"}}`,
	}
	events := runLines(t, m, lines)
	warn := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "turn_aborted_no_turn" {
			warn = true
		}
	}
	if !warn {
		t.Errorf("stray turn_aborted did not surface a WRN log")
	}
}

// TestMapper_ReasoningContentRaw covers the content[]-non-empty raw branch of
// reasoningKind (Format=json) (spec rule #8).
func TestMapper_ReasoningContentRaw(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"reasoning","content":[{"type":"reasoning_text","text":"chain"}],"summary":[]}}`,
	}
	events := runLines(t, m, lines)
	if r := firstReasoning(t, events); r.ReasoningKind != "raw" {
		t.Errorf("ReasoningKind = %q, want raw (content[] non-empty)", r.ReasoningKind)
	}
}

// TestMapper_TokenCountBeforeAnyTurnDropped covers mapTokenCount's nil-turn path
// (token_count before any turn opened).
func TestMapper_TokenCountBeforeAnyTurnDropped(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5}}}}`,
	}
	events := runLines(t, m, lines)
	// No turn → no rollup, no crash, no token_count-derived event.
	if got := countKind(events, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("TurnFinalized = %d, want 0", got)
	}
}

// TestMapper_DeveloperMessageIsLLM covers the assistant/system/developer message
// branch for a non-user, non-assistant role (still llm-kind).
func TestMapper_DeveloperMessageIsLLM(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"sys"}]}}`,
	}
	events := runLines(t, m, lines)
	llm := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpLLM {
			llm++
		}
	}
	if llm != 1 {
		t.Fatalf("developer message LLM op count = %d, want 1", llm)
	}
}

// TestMapper_CompletedAtUnixSeconds covers completedAtMicros' unix-seconds
// branch (some codex versions encode completed_at as a number).
func TestMapper_CompletedAtUnixSeconds(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763664000}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":1763664600}}`,
	}
	tf := turnFinals(runLines(t, m, lines))
	if len(tf) != 1 {
		t.Fatalf("TurnFinalized count = %d, want 1", len(tf))
	}
	if tf[0].EndTs != 1763664600*1_000_000 {
		t.Errorf("EndTs = %d, want %d (unix-seconds completed_at)", tf[0].EndTs, int64(1763664600)*1_000_000)
	}
}

// TestMapper_MissingSessionMetaStillAnchors covers mapRecord's bootstrap when
// the first record is NOT a session_meta (corrupt file, rule #24): a minimal
// root session is started so events still attach.
func TestMapper_MissingSessionMetaStillAnchors(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid-nometa")
	// First record is a turn_context (no session_meta).
	lines := []string{
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
	}
	events := runLines(t, m, lines)
	s := firstStarted(t, events)
	if s.NativeID != "sid-nometa" || s.Kind != canonical.KindRoot {
		t.Errorf("fallback session = {%q %q}, want {sid-nometa root}", s.NativeID, s.Kind)
	}
}

// TestMapper_LateSessionMetaNoSecondStart covers the recSessionMeta arm of
// mapRecord when a session_meta arrives after bootstrap (metadata-only append):
// exactly one SessionStarted total.
func TestMapper_LateSessionMetaNoSecondStart(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		metaLine("sid", `"exec"`), // a second session_meta (append-after-end)
	}
	events := runLines(t, m, lines)
	if got := countKind(events, canonical.EvSessionStarted); got != 1 {
		t.Fatalf("SessionStarted count = %d, want 1 (no second start on late meta)", got)
	}
}
