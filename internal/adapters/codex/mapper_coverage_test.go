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

// TestMapper_EnrichOnAlreadyFinalizedOpReemits covers the output-first exec
// ordering (~15-32% of real files, F4): an exec_command_end whose op was ALREADY
// finalized by its function_call_output re-emits an OpStarted carrying the exec
// Extras onto the SAME (turn,seq) — an idempotent UPDATE, NOT a DBG log (spec
// rule #14). The enrichment must land in ops.extras_json regardless of order.
func TestMapper_EnrichOnAlreadyFinalizedOpReemits(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		// output finalizes c1 first (deletes it from openOps) — output-first order.
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`,
		// exec_command_end now arrives for the already-finalized op.
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":0,"aggregated_output":"ok","source":"model"}}`,
	}
	events := runLines(t, m, lines)
	// The enrichment must arrive as a re-emitted OpStarted (same turn/seq as the
	// shell op) carrying exec_* Extras — NOT a DBG log.
	reemit := false
	for _, s := range opStarts(events) {
		if s.Name == "shell" && s.Seq == 1 {
			if code, ok := s.Extras["exec_exit_code"]; ok && code == int64(0) {
				reemit = true
			}
		}
	}
	if !reemit {
		t.Errorf("late exec_command_end on finalized op did not re-emit an OpStarted carrying exec Extras (F4)")
	}
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "enrich_exec_command_end" {
			t.Errorf("late exec_command_end logged instead of re-emitting onto the op (F4 regression)")
		}
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

// TestMapper_TokenCountBeforeAnyTurnLogs covers mapTokenCount's nil-turn path
// (token_count before any turn opened): it must surface a DBG
// "token_count_no_turn" log, NOT drop silently (F6, spec rule #6 "no silent
// failures").
func TestMapper_TokenCountBeforeAnyTurnLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"token_count","turn_id":"ghost","info":{"last_token_usage":{"input_tokens":5}}}}`,
	}
	events := runLines(t, m, lines)
	// No turn → no rollup, no crash, no token_count-derived turn finalize.
	if got := countKind(events, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("TurnFinalized = %d, want 0", got)
	}
	// But a DBG log MUST surface the dropped count (F6).
	logged := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "token_count_no_turn" {
			logged = true
			if le.Extras["turn_id"] != "ghost" {
				t.Errorf("token_count_no_turn log turn_id = %v, want ghost", le.Extras["turn_id"])
			}
		}
	}
	if !logged {
		t.Errorf("token_count with no open turn was dropped silently (F6 regression: must DBG-log)")
	}
}

// TestMapper_CollabSpawnSessionOp covers collab_agent_spawn_end (F3): a
// session/spawn op whose ChildSessionNativeID is new_thread_id (NOT
// agent_ref.thread_id), carrying the spawned agent metadata in Extras.
func TestMapper_CollabSpawnSessionOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("parent-sid")
	lines := []string{
		metaLine("parent-sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"parent-sid","new_thread_id":"child-uuid","new_agent_nickname":"Dewey","new_agent_role":"explorer","status":"completed"}}`,
	}
	events := runLines(t, m, lines)
	var spawn *canonical.OpStartedEvent
	for i := range events {
		if s, ok := events[i].(canonical.OpStartedEvent); ok && s.Kind == canonical.OpSession && s.Name == "spawn" {
			sc := s
			spawn = &sc
		}
	}
	if spawn == nil {
		t.Fatalf("no session/spawn op emitted for collab_agent_spawn_end (F3)")
	}
	if spawn.ChildSessionNativeID != "child-uuid" {
		t.Errorf("ChildSessionNativeID = %q, want child-uuid (new_thread_id, NOT agent_ref)", spawn.ChildSessionNativeID)
	}
	if spawn.Extras["relationship"] != "sub_agent" || spawn.Extras["new_agent_nickname"] != "Dewey" {
		t.Errorf("spawn extras = %v, want relationship=sub_agent + nickname Dewey", spawn.Extras)
	}
}

// TestMapper_CollabCloseAndWaitingRecognized covers collab_close_end and
// collab_waiting_end (F3): recognized (runLines would Fatalf on an unknown
// payload type via parseLine), surfaced as a DBG log, and producing NO canonical
// op.
func TestMapper_CollabCloseAndWaitingRecognized(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"collab_close_end","call_id":"x"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"collab_waiting_end","call_id":"y"}}`,
	}
	// runLines Fatalf's on a parse error, so reaching here proves the types are
	// recognized (no errUnknownPayloadType).
	events := runLines(t, m, lines)
	// No session/spawn or other op from these markers.
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpSession {
			t.Errorf("collab_close_end/waiting_end wrongly produced a session op")
		}
	}
	closeLogged, waitLogged := false, false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok {
			if le.Message == "event_msg:collab_close_end" {
				closeLogged = true
			}
			if le.Message == "event_msg:collab_waiting_end" {
				waitLogged = true
			}
		}
	}
	if !closeLogged || !waitLogged {
		t.Errorf("collab_close_end/waiting_end not surfaced as DBG logs (close=%v wait=%v)", closeLogged, waitLogged)
	}
}

// TestMapper_CollabSpawnNoChildLogs covers mapCollabSpawn's no-child branch (F3):
// a collab_agent_spawn_end with no new_thread_id surfaces a DBG log and emits no
// session op. Also covers spawnStatus's failed branch via a "failed" status.
func TestMapper_CollabSpawnNoChildLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"p","status":"failed"}}`,
	}
	events := runLines(t, m, lines)
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpSession {
			t.Errorf("collab_agent_spawn_end with no new_thread_id wrongly produced a session op")
		}
	}
	logged := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "collab_agent_spawn_end_no_child" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("collab_agent_spawn_end with no child did not surface a DBG log (F3)")
	}
	// spawnStatus failed branch (a spawned child + status=failed → op finalized failed).
	if got := spawnStatus("failed"); got != "failed" {
		t.Errorf("spawnStatus(failed) = %q, want failed", got)
	}
	if got := spawnStatus("completed"); got != "completed" {
		t.Errorf("spawnStatus(completed) = %q, want completed", got)
	}
}

// TestMapper_WebSearchEndOrphanLogs covers enrichWebSearch's no-call branch (F7):
// a web_search_end with no preceding web_search_call surfaces a DBG log.
func TestMapper_WebSearchEndOrphanLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"web_search_end","call_id":"orphan","query":"q"}}`,
	}
	events := runLines(t, m, lines)
	logged := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "web_search_end_no_call" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("orphan web_search_end did not surface a DBG log (F7)")
	}
}

// TestMapper_LateEnrichOrphanLogs covers enrichFinalizedOp's not-locatable branch
// (F4): an exec_command_end whose call_id matches NO op (neither open nor
// finalized) surfaces a DBG log rather than inventing an op reference.
func TestMapper_LateEnrichOrphanLogs(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"ghost","exit_code":0,"aggregated_output":"x"}}`,
	}
	events := runLines(t, m, lines)
	logged := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "enrich_exec_command_end" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("orphan exec_command_end did not surface a DBG log (F4 not-locatable path)")
	}
}

// TestMapper_OutputFirstExecEnrich covers the output-first ordering where the
// function_call_output finalizes the op BEFORE the exec_command_end, so the late
// exec_command_end re-emits onto the finalized op via finalizedOps (F4). Also
// covers mapToolOutput's finalizedOps PayloadRef-attach branch via a duplicate
// output line.
func TestMapper_OutputFirstExecEnrich(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":0,"aggregated_output":"ok","cwd":"<ROOT>"}}`,
		// A second (duplicate) output for the now-finalized op: its tool_response
		// PayloadRef should still attach via finalizedOps, not warn.
		`{"timestamp":"` + tsDone + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok-again"}}`,
	}
	events := runLines(t, m, lines)
	// The late exec_command_end re-emitted onto the shell op carrying exec_exit_code.
	reemit := false
	for _, s := range opStarts(events) {
		if s.Name == "shell" {
			if _, ok := s.Extras["exec_exit_code"]; ok {
				reemit = true
			}
		}
	}
	if !reemit {
		t.Errorf("output-first late exec_command_end did not re-emit exec Extras onto the op (F4)")
	}
	// No tool_output_unmatched warn for the duplicate output (it attaches via finalizedOps).
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "tool_output_unmatched" {
			t.Errorf("duplicate output on a finalized op wrongly warned tool_output_unmatched (F4)")
		}
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

// TestMapper_NativeIDFromPayloadID covers G5: the mapper is seeded with a
// FILENAME-derived nativeID, but session_meta.payload.id is AUTHORITATIVE and must
// override it on the session AND every subsequent event. The scanner seeds the
// filename id as a fallback; the body id wins (spec adapter-codex.md:290).
func TestMapper_NativeIDFromPayloadID(t *testing.T) {
	t.Parallel()
	m := newTestMapper("file-id") // filename-derived fallback
	lines := []string{
		metaLine("meta-id", `"exec"`), // payload.id is the authoritative id
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
	}
	events := runLines(t, m, lines)
	s := firstStarted(t, events)
	if s.NativeID != "meta-id" || s.RootNativeID != "meta-id" {
		t.Errorf("session ids = {NativeID:%q RootNativeID:%q}, want both meta-id (payload.id wins, G5)", s.NativeID, s.RootNativeID)
	}
	// Every op/turn event must carry the authoritative id, not the filename fallback.
	for _, st := range opStarts(events) {
		if st.SessionNativeID != "meta-id" {
			t.Errorf("op SessionNativeID = %q, want meta-id (G5)", st.SessionNativeID)
		}
	}
	if m.nativeID != "meta-id" {
		t.Errorf("m.nativeID = %q, want meta-id (G5)", m.nativeID)
	}
}

// TestMapper_MultiWebSearchFIFO covers G4: two web_search_call ops followed by two
// web_search_end events pair in FIFO order (oldest open search first), each
// carrying its own query+action Extras. Also exercises pruneWebSearchQueue's
// survivor path: a leftover unpaired search from turn 1 is dropped when turn 1
// closes, so a turn-2 web_search_end does not pair with it.
func TestMapper_MultiWebSearchFIFO(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"web_search_call","action":{"type":"search","query":"alpha"}}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"web_search_call","action":{"type":"open_page","url":"https://e.invalid/p"}}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"web_search_end","call_id":"x1","query":"alpha","action":{"type":"search","query":"alpha"}}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"web_search_end","call_id":"x2","action":{"type":"open_page","url":"https://e.invalid/p"}}}`,
	}
	events := runLines(t, m, lines)
	// The first-opened web_search op (Seq 1) must carry the search action (paired
	// with the FIRST end); the second (Seq 2) the open_page (the SECOND end). Seqs
	// are 1 and 2 because the turn opens no other op before them.
	var firstAction, secondAction map[string]any
	for _, st := range opStarts(events) {
		if st.Name != "web_search" || st.Extras == nil {
			continue
		}
		if a, ok := st.Extras["action"].(map[string]any); ok {
			switch st.Seq {
			case 1:
				firstAction = a
			case 2:
				secondAction = a
			}
		}
	}
	if firstAction == nil || firstAction["type"] != "search" {
		t.Errorf("Seq1 (oldest search) action = %+v, want type=search (FIFO, G4)", firstAction)
	}
	if secondAction == nil || secondAction["type"] != "open_page" {
		t.Errorf("Seq2 (newer search) action = %+v, want type=open_page (FIFO, G4)", secondAction)
	}
	// Both web_search ops finalized completed; no orphan log.
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "web_search_end_no_call" {
			t.Errorf("FIFO pairing wrongly logged an orphan end (G4)")
		}
	}
}

// TestMapper_WebSearchPruneAcrossTurns covers G4's pruneWebSearchQueue survivor
// path: an unpaired web_search_call in turn 1 is dropped when turn 1 closes, so a
// web_search_end in turn 2 does NOT pair with the stale turn-1 ref (it is an
// orphan).
func TestMapper_WebSearchPruneAcrossTurns(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		// An unpaired web_search_call in turn 1.
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"web_search_call","action":{"type":"search","query":"dangling"}}}`,
		// turn 1 closes — its open web_search op is dangling-finalized and pruned.
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
		// turn 2 opens; a web_search_end with no in-turn call must be an orphan.
		`{"timestamp":"` + tsDone + `","type":"turn_context","payload":{"turn_id":"t2","model":"m"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"web_search_end","call_id":"x9","query":"late"}}`,
	}
	events := runLines(t, m, lines)
	orphan := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "web_search_end_no_call" {
			orphan = true
		}
	}
	if !orphan {
		t.Errorf("turn-2 web_search_end paired with a stale turn-1 ref instead of logging orphan (G4 prune)")
	}
}

// TestMapper_PatchApplyOpenOpFailed covers G2's open-op path: a patch_apply_end
// arriving while the apply_patch op is still OPEN (no function_call_output yet)
// finalizes it with the success-derived status AND merges {patch_success,
// patch_status} into Extras.
func TestMapper_PatchApplyOpenOpFailed(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"apply_patch","arguments":"{}","call_id":"p1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"p1","success":false,"status":"failed"}}`,
	}
	events := runLines(t, m, lines)
	// The apply_patch op is finalized failed/patch_failed with merged extras.
	gotFailed, gotExtras := false, false
	for _, f := range opFinals(events) {
		if f.Status == "failed" && f.ErrorClass == "patch_failed" {
			gotFailed = true
		}
	}
	for _, st := range opStarts(events) {
		if st.Name == "apply_patch" && st.Extras != nil && st.Extras["patch_success"] == false && st.Extras["patch_status"] == "failed" {
			gotExtras = true
		}
	}
	if !gotFailed {
		t.Errorf("open-op patch_apply_end did not finalize failed/patch_failed (G2)")
	}
	if !gotExtras {
		t.Errorf("open-op patch_apply_end did not merge patch_success/patch_status Extras (G2)")
	}
}

// TestMapper_OutputFirstExecFailedCorrects covers G1: an output-first
// exec_command_end(exit≠0) emits a CORRECTING OpFinalized(failed, command_failed)
// onto the op that its function_call_output had provisionally finalized completed.
func TestMapper_OutputFirstExecFailedCorrects(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		// Output-first: provisional completed off a benign-looking output string.
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"some output"}}`,
		// Late exec_command_end with a non-zero exit_code → authoritative failed.
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":2,"aggregated_output":"some output","duration":{"secs":0,"nanos":500000000}}}`,
	}
	events := runLines(t, m, lines)
	fins := opFinals(events)
	// The shell op (Seq 1 — it is the turn's first op) must END with a corrected
	// failed/command_failed finalize (the LAST finalize on its (turn,seq) wins via
	// the writer upsert).
	var lastSeq1 *canonical.OpFinalizedEvent
	for i := range fins {
		if fins[i].Seq == 1 {
			f := fins[i]
			lastSeq1 = &f
		}
	}
	if lastSeq1 == nil || lastSeq1.Status != "failed" || lastSeq1.ErrorClass != "command_failed" {
		t.Errorf("output-first failed exec: last Seq1 finalize = %+v, want failed/command_failed (G1)", lastSeq1)
	}
	// The exec extras (exit_code, duration_ms) reached the op via an OpStarted re-emit.
	reemit := false
	for _, st := range opStarts(events) {
		if st.Name == "shell" && st.Extras != nil && st.Extras["exec_exit_code"] == int64(2) && st.Extras["exec_duration_ms"] == int64(500) {
			reemit = true
		}
	}
	if !reemit {
		t.Errorf("output-first failed exec did not re-emit exec_exit_code/exec_duration_ms Extras (G1/G3)")
	}
}
