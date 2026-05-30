package codex

import (
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	tsMeta  = "2025-11-20T16:59:09.857Z"
	tsCtx   = "2025-11-20T16:59:10.000Z"
	tsItem  = "2025-11-20T16:59:11.000Z"
	tsEvent = "2025-11-20T16:59:12.000Z"
	tsDone  = "2025-11-20T17:00:00.000Z"
)

func metaLine(id, sourceJSON string) string {
	return `{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"` + id +
		`","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","model_provider":"openai","source":` + sourceJSON + `}}`
}

// TestMapper_SessionStartedRoot asserts the first record (session_meta) emits a
// single root SessionStartedEvent with extras and AgentName from originator
// (spec rule #1).
func TestMapper_SessionStartedRoot(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid-root")
	events := runLines(t, m, []string{metaLine("sid-root", `"exec"`)})

	if got := countKind(events, canonical.EvSessionStarted); got != 1 {
		t.Fatalf("SessionStarted count = %d, want 1", got)
	}
	s := firstStarted(t, events)
	if s.NativeID != "sid-root" || s.Kind != canonical.KindRoot {
		t.Errorf("session = {NativeID:%q Kind:%q}, want {sid-root root}", s.NativeID, s.Kind)
	}
	if s.RootNativeID != "sid-root" || s.ParentNativeID != "" {
		t.Errorf("root/parent = {%q %q}, want {sid-root \"\"}", s.RootNativeID, s.ParentNativeID)
	}
	if s.AgentName != "codex:codex_exec" {
		t.Errorf("AgentName = %q, want codex:codex_exec", s.AgentName)
	}
	if s.Cwd != "<ROOT>" {
		t.Errorf("Cwd = %q, want <ROOT>", s.Cwd)
	}
	if s.Extras["cli_version"] != "0.125.0" || s.Extras["originator"] != "codex_exec" {
		t.Errorf("extras missing cli_version/originator: %+v", s.Extras)
	}
}

// TestMapper_SubAgentLinkage asserts a sub-agent thread_spawn session links to
// its parent and stamps Kind=sub_agent + relationship=sub_agent (spec rule #1,
// "Sub-Agent Linkage", gap #5/#10).
func TestMapper_SubAgentLinkage(t *testing.T) {
	t.Parallel()
	src := `{"subagent":{"thread_spawn":{"parent_thread_id":"parent-uuid","depth":1,"agent_nickname":"Tesla","agent_role":"explorer"}}}`
	line := `{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"child-uuid","originator":"codex_exec","agent_nickname":"Tesla","agent_role":"explorer","thread_source":"subagent","source":` + src + `}}`
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", absPath: "/t/child.jsonl", nativeID: "child-uuid"})
	events := runLines(t, m, []string{line})

	s := firstStarted(t, events)
	if s.Kind != canonical.KindSubAgent {
		t.Errorf("Kind = %q, want sub_agent", s.Kind)
	}
	if s.ParentNativeID != "parent-uuid" || s.RootNativeID != "parent-uuid" {
		t.Errorf("parent/root = {%q %q}, want {parent-uuid parent-uuid}", s.ParentNativeID, s.RootNativeID)
	}
	if s.AgentName != "Tesla" {
		t.Errorf("AgentName = %q, want Tesla", s.AgentName)
	}
	if s.Extras["relationship"] != "sub_agent" {
		t.Errorf("relationship = %v, want sub_agent", s.Extras["relationship"])
	}
	if s.Extras["subagent_depth"] != 1 {
		t.Errorf("subagent_depth = %v, want 1", s.Extras["subagent_depth"])
	}
}

// TestMapper_ForkLinkage asserts a forked_from_id session is Kind=fork with the
// parent set from forked_from_id and relationship=fork (spec rule #1, gap #5).
func TestMapper_ForkLinkage(t *testing.T) {
	t.Parallel()
	line := `{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"fork-uuid","forked_from_id":"origin-uuid","originator":"codex_cli_rs","source":"cli"}}`
	m := newFileMapper(mapperConfig{sourceID: "codex:/t", absPath: "/t/fork.jsonl", nativeID: "fork-uuid"})
	events := runLines(t, m, []string{line})

	s := firstStarted(t, events)
	if s.Kind != canonical.KindFork {
		t.Errorf("Kind = %q, want fork", s.Kind)
	}
	if s.ParentNativeID != "origin-uuid" || s.RootNativeID != "origin-uuid" {
		t.Errorf("parent/root = {%q %q}, want {origin-uuid origin-uuid}", s.ParentNativeID, s.RootNativeID)
	}
	if s.Extras["relationship"] != "fork" {
		t.Errorf("relationship = %v, want fork", s.Extras["relationship"])
	}
}

// TestMapper_ToolInternalLinkage asserts an internal source maps to
// tool_internal (spec rule #1, gap #6).
func TestMapper_ToolInternalLinkage(t *testing.T) {
	t.Parallel()
	line := metaLine("internal-uuid", `{"internal":"memory_consolidation"}`)
	m := newTestMapper("internal-uuid")
	events := runLines(t, m, []string{line})
	if s := firstStarted(t, events); s.Kind != canonical.KindToolInternal {
		t.Errorf("Kind = %q, want tool_internal", s.Kind)
	}
}

// TestMapper_TurnContextOpensTurnAndModel asserts a turn_context opens turn 1,
// emits TurnStarted once, and announces the model once via SessionUpdated (spec
// rule #2).
func TestMapper_TurnContextOpensTurnAndModel(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.1-codex-max","effort":"high","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write"}}}`,
	}
	events := runLines(t, m, lines)

	if got := countKind(events, canonical.EvTurnStarted); got != 1 {
		t.Fatalf("TurnStarted count = %d, want 1", got)
	}
	if got := countKind(events, canonical.EvSessionUpdated); got != 1 {
		t.Fatalf("SessionUpdated count = %d, want 1", got)
	}
	for _, ev := range events {
		if u, ok := ev.(canonical.SessionUpdatedEvent); ok && u.Model != "gpt-5.1-codex-max" {
			t.Errorf("SessionUpdated.Model = %q, want gpt-5.1-codex-max", u.Model)
		}
		if ts, ok := ev.(canonical.TurnStartedEvent); ok && ts.Seq != 1 {
			t.Errorf("TurnStarted.Seq = %d, want 1", ts.Seq)
		}
	}
}

// TestMapper_TurnBoundaryNewFormat asserts the new format (turn_context +
// task_started + task_complete) yields exactly one TurnStarted and one
// TurnFinalized(completed) per turn (spec rule #2,#3,#4; acceptance #3).
func TestMapper_TurnBoundaryNewFormat(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsCtx + `","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":1763664000,"model_context_window":200000}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":"` + tsDone + `","duration_ms":1000}}`,
	}
	events := runLines(t, m, lines)

	if got := countKind(events, canonical.EvTurnStarted); got != 1 {
		t.Fatalf("TurnStarted count = %d, want 1 (idempotent across turn_context+task_started)", got)
	}
	tf := turnFinals(events)
	if len(tf) != 1 {
		t.Fatalf("TurnFinalized count = %d, want 1", len(tf))
	}
	if tf[0].Status != "completed" || tf[0].Seq != 1 {
		t.Errorf("TurnFinalized = {Status:%q Seq:%d}, want {completed 1}", tf[0].Status, tf[0].Seq)
	}
}

// TestMapper_TurnBoundaryOldFormat asserts the old format (turn_context only, no
// task_started/complete) opens a turn per turn_id; the running turn stays open
// at clean EOF (no clean finalize — SOW C#3) (spec rule #2,#22; edge #3;
// acceptance #3).
func TestMapper_TurnBoundaryOldFormat(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid-old")
	lines := []string{
		`{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"sid-old","originator":"codex-tui","cli_version":"0.61.0","source":"cli"}}`,
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.0"}}`,
		`{"timestamp":"` + tsItem + `","type":"turn_context","payload":{"turn_id":"t2","model":"gpt-5.0"}}`,
	}
	events := runLines(t, m, lines)

	if got := countKind(events, canonical.EvTurnStarted); got != 2 {
		t.Fatalf("TurnStarted count = %d, want 2 (one per turn_id)", got)
	}
	// Clean EOF: no TurnFinalized, no SessionFinalized (SOW C#3).
	if got := countKind(events, canonical.EvTurnFinalized); got != 0 {
		t.Errorf("TurnFinalized count = %d, want 0 at clean EOF", got)
	}
	if got := countKind(events, canonical.EvSessionFinalized); got != 0 {
		t.Errorf("SessionFinalized count = %d, want 0 at clean EOF", got)
	}
}

// TestMapper_AbsentTurnIDFallback asserts an old-CLI rollout with no turn_id on
// turn_context still opens a single fallback turn under the empty key (spec edge
// #3 "user message → next user message" fallback bucket).
func TestMapper_AbsentTurnIDFallback(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid-noturn")
	lines := []string{
		`{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"sid-noturn","source":"cli"}}`,
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"model":"gpt-5.0"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
	}
	events := runLines(t, m, lines)
	if got := countKind(events, canonical.EvTurnStarted); got != 1 {
		t.Fatalf("TurnStarted count = %d, want 1 (single fallback turn)", got)
	}
}

// TestMapper_ReasoningKindSummary asserts a reasoning item with only summary[]
// → reasoning_kind=summary (spec rule #8, acceptance #4).
func TestMapper_ReasoningKindSummary(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`,
	}
	events := runLines(t, m, lines)
	r := firstReasoning(t, events)
	if r.ReasoningKind != "summary" {
		t.Errorf("ReasoningKind = %q, want summary", r.ReasoningKind)
	}
}

// TestMapper_ReasoningKindRaw asserts a reasoning item with encrypted_content
// (or content[]) → reasoning_kind=raw (spec rule #8, acceptance #4).
func TestMapper_ReasoningKindRaw(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"AAAABBBB"}}`,
	}
	events := runLines(t, m, lines)
	r := firstReasoning(t, events)
	if r.ReasoningKind != "raw" {
		t.Errorf("ReasoningKind = %q, want raw", r.ReasoningKind)
	}
}

// TestMapper_EventReasoningIsLogOnlyNoDupOp asserts event_msg.agent_reasoning*
// produces a LogEntry and NEVER a reasoning op, so the canonical reasoning op
// comes only from response_item.reasoning (spec rule #8, acceptance #4).
func TestMapper_EventReasoningIsLogOnlyNoDupOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"s"}]}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"agent_reasoning","text":"the visible summary"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"agent_reasoning_raw_content","text":"raw cot"}}`,
	}
	events := runLines(t, m, lines)

	reasoningOps := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpReasoning {
			reasoningOps++
		}
	}
	if reasoningOps != 1 {
		t.Fatalf("reasoning ops = %d, want 1 (only response_item.reasoning)", reasoningOps)
	}
	// The two event_msg reasoning entries must be LogEntry rows.
	if got := countKind(events, canonical.EvLogEntry); got < 2 {
		t.Errorf("LogEntry count = %d, want >= 2 (agent_reasoning + raw)", got)
	}
}

// TestMapper_ToolCallPairing asserts a function_call + function_call_output pair
// produces one tool op (started+finalized) with the shell namespace and two
// PayloadRefs (request + response) (spec rule #9).
func TestMapper_ToolCallPairing(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"file.txt"}}`,
	}
	events := runLines(t, m, lines)

	starts := opStarts(events)
	tools := 0
	for _, s := range starts {
		if s.Kind == canonical.OpTool {
			tools++
			if s.Name != "shell" || s.ToolNamespace != "shell" {
				t.Errorf("tool op = {Name:%q NS:%q}, want {shell shell}", s.Name, s.ToolNamespace)
			}
		}
	}
	if tools != 1 {
		t.Fatalf("tool op starts = %d, want 1", tools)
	}
	fins := opFinals(events)
	toolFin := 0
	for _, f := range fins {
		if f.Status == "completed" && f.Seq >= 1 {
			toolFin++
		}
	}
	if toolFin < 1 {
		t.Errorf("no completed op finalize for the tool")
	}
	if got := countKind(events, canonical.EvPayloadRef); got != 2 {
		t.Errorf("PayloadRef count = %d, want 2 (request+response)", got)
	}
}

// TestMapper_DanglingOpCancelledOnAbort asserts a function_call with no matching
// output is finalized cancelled at turn_aborted (spec edge #9, rule #5).
func TestMapper_DanglingOpCancelledOnAbort(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"interrupted"}}`,
	}
	events := runLines(t, m, lines)

	var cancelled int
	for _, f := range opFinals(events) {
		if f.Status == "cancelled" {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("cancelled op finalize count = %d, want 1", cancelled)
	}
	tf := turnFinals(events)
	if len(tf) != 1 || tf[0].Status != "failed" || tf[0].ErrorClass != "user_interrupt" {
		t.Fatalf("turn finalize = %+v, want failed/user_interrupt", tf)
	}
}

// TestMapper_AbortErrorClasses covers every turn_aborted reason→ErrorClass
// mapping (spec rule #5).
func TestMapper_AbortErrorClasses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"interrupted":    "user_interrupt",
		"replaced":       "replaced",
		"review_ended":   "review_ended",
		"budget_limited": "rate_limit",
	}
	for reason, want := range cases {
		reason, want := reason, want
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			m := newTestMapper("sid")
			lines := []string{
				metaLine("sid", `"exec"`),
				`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
				`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"` + reason + `"}}`,
			}
			tf := turnFinals(runLines(t, m, lines))
			if len(tf) != 1 || tf[0].ErrorClass != want {
				t.Fatalf("reason %q → %+v, want ErrorClass %q", reason, tf, want)
			}
		})
	}
}

// TestMapper_TokenRollup asserts the C#1 rollup: TurnFinalized.TokensIn/Out are
// the SUM of per-call last_token_usage over the turn's token_count events, and
// the cumulative total_token_usage feeds the LLM op CtxUsed only — never the
// per-turn tokens (spec rule #4, #17; SOW C#1).
func TestMapper_TokenRollup(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763664000}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]}}`,
		// two token_count events for this turn: last usage 10/5 then 20/8 → sum 30/13.
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"token_count","turn_id":"t1","info":{"total_token_usage":{"total_tokens":100},"last_token_usage":{"input_tokens":10,"output_tokens":5}},"model_context_window":200000}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"token_count","turn_id":"t1","info":{"total_token_usage":{"total_tokens":250},"last_token_usage":{"input_tokens":20,"output_tokens":8}},"model_context_window":200000}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)

	tf := turnFinals(events)
	if len(tf) != 1 {
		t.Fatalf("turn finalize count = %d, want 1", len(tf))
	}
	if tf[0].TokensIn != 30 || tf[0].TokensOut != 13 {
		t.Errorf("rollup = {In:%d Out:%d}, want {30 13} (sum of last_token_usage)", tf[0].TokensIn, tf[0].TokensOut)
	}
	// CtxUsed = cumulative total (250), CtxMax = model_context_window (200000),
	// applied to the LLM op via a re-finalize at task_complete.
	var ctxUsed, ctxMax int64
	for _, f := range opFinals(events) {
		if f.CtxUsed > 0 || f.CtxMax > 0 {
			ctxUsed, ctxMax = f.CtxUsed, f.CtxMax
		}
	}
	if ctxUsed != 250 || ctxMax != 200000 {
		t.Errorf("LLM op ctx = {Used:%d Max:%d}, want {250 200000}", ctxUsed, ctxMax)
	}
}

// TestMapper_SessionLevelTokenCountAttributedToActiveTurn asserts a token_count
// with NO turn_id attributes to the most-recently-active turn (spec rule #17,
// "Token accounting nuance").
func TestMapper_SessionLevelTokenCountAttributedToActiveTurn(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":50},"last_token_usage":{"input_tokens":7,"output_tokens":3}}}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	tf := turnFinals(runLines(t, m, lines))
	if len(tf) != 1 || tf[0].TokensIn != 7 || tf[0].TokensOut != 3 {
		t.Fatalf("session-level token_count rollup = %+v, want In7/Out3 on the active turn", tf)
	}
}

// TestMapper_UserDedup asserts user input arriving as BOTH
// response_item.message(role=user) and event_msg.user_message produces exactly
// one user_input op (spec rule #6, #18).
func TestMapper_UserDedup(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"user_message","message":"do the thing"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do the thing"}]}}`,
	}
	events := runLines(t, m, lines)

	userOps := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpInternal && s.Name == "user_input" {
			userOps++
		}
	}
	if userOps != 1 {
		t.Fatalf("user_input ops = %d, want 1 (deduped)", userOps)
	}
}

// TestMapper_Compaction asserts a top-level compacted line emits one compaction
// op with extras + a PayloadRef (spec rule #20, gap #4).
func TestMapper_Compaction(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"compacted","payload":{"message":"summary text","replacement_history":[{"type":"message"},{"type":"message"}]}}`,
	}
	events := runLines(t, m, lines)

	var compactionOps int
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpCompaction {
			compactionOps++
			if s.Extras["replacement_history_size"] != 2 {
				t.Errorf("replacement_history_size = %v, want 2", s.Extras["replacement_history_size"])
			}
			if s.Extras["message_preview"] != "summary text" {
				t.Errorf("message_preview = %v, want 'summary text'", s.Extras["message_preview"])
			}
		}
	}
	if compactionOps != 1 {
		t.Fatalf("compaction ops = %d, want 1", compactionOps)
	}
}

// TestMapper_ContextCompactionVariants asserts response_item.compaction,
// response_item.context_compaction, and event_msg.context_compacted all converge
// on a compaction op (spec rule #20, gap #4).
func TestMapper_ContextCompactionVariants(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"compaction","encrypted_content":"X"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"context_compaction","encrypted_content":null}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"context_compacted"}}`,
	} {
		m := newTestMapper("sid")
		events := runLines(t, m, []string{metaLine("sid", `"exec"`), line})
		if got := 0; func() int {
			for _, s := range opStarts(events) {
				if s.Kind == canonical.OpCompaction {
					got++
				}
			}
			return got
		}() != 1 {
			t.Errorf("compaction op count for %q != 1", line)
		}
	}
}

// TestMapper_ExecCommandEndEnrichesNoSecondOp asserts exec_command_end enriches
// the matching tool op (it finalizes via exit_code) without emitting a second
// op-start (spec rule #14).
func TestMapper_ExecCommandEndEnrichesNoSecondOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":0,"aggregated_output":"out","cwd":"<ROOT>","source":"model"}}`,
	}
	events := runLines(t, m, lines)

	toolStarts := 0
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpTool {
			toolStarts++
		}
	}
	if toolStarts != 1 {
		t.Fatalf("tool op starts = %d, want 1 (exec_command_end must not add a second op)", toolStarts)
	}
	// The op is finalized completed (exit_code 0).
	completed := false
	for _, f := range opFinals(events) {
		if f.Status == "completed" {
			completed = true
		}
	}
	if !completed {
		t.Errorf("tool op not finalized completed by exec_command_end exit_code 0")
	}
}

// TestMapper_ExecCommandEndNonZeroExitFails asserts a non-zero exit_code
// finalizes the op failed/command_failed (spec rule #14).
func TestMapper_ExecCommandEndNonZeroExitFails(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"exec_command_end","call_id":"c1","exit_code":2,"aggregated_output":""}}`,
	}
	events := runLines(t, m, lines)
	failed := false
	for _, f := range opFinals(events) {
		if f.Status == "failed" && f.ErrorClass == "command_failed" {
			failed = true
		}
	}
	if !failed {
		t.Errorf("non-zero exit did not finalize failed/command_failed")
	}
}

// TestMapper_McpToolCallEndSetsNamespace asserts mcp_tool_call_end re-stamps the
// matching op's namespace to mcp:<server> and finalizes it (spec rule #15).
func TestMapper_McpToolCallEndSetsNamespace(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"github.list","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"c1","invocation":{"server":"github","tool":"list"},"result":{"Ok":{"is_error":false}}}}`,
	}
	events := runLines(t, m, lines)

	var sawMcp bool
	for _, s := range opStarts(events) {
		if s.ToolNamespace == "mcp:github" && s.Name == "list" {
			sawMcp = true
		}
	}
	if !sawMcp {
		t.Errorf("mcp_tool_call_end did not set namespace mcp:github / name list")
	}
}

// TestMapper_PatchApplyEndFinalizes asserts patch_apply_end finalizes the
// matching apply_patch op with success→completed / failure→failed (spec rule
// #16).
func TestMapper_PatchApplyEndFinalizes(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"apply_patch","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"c1","success":false,"status":"failed"}}`,
	}
	events := runLines(t, m, lines)
	// The apply_patch op must carry the fs namespace and be finalized failed.
	var nsOK, failed bool
	for _, s := range opStarts(events) {
		if s.Name == "apply_patch" && s.ToolNamespace == "fs" {
			nsOK = true
		}
	}
	for _, f := range opFinals(events) {
		if f.Status == "failed" && f.ErrorClass == "patch_failed" {
			failed = true
		}
	}
	if !nsOK {
		t.Errorf("apply_patch op missing fs namespace")
	}
	if !failed {
		t.Errorf("patch_apply_end success=false did not finalize failed/patch_failed")
	}
}

// TestMapper_SandboxDeniedOutput asserts a tool output string signalling a
// sandbox denial finalizes the op failed/sandbox_denied (spec edge #5).
func TestMapper_SandboxDeniedOutput(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m","sandbox_policy":{"type":"read-only"}}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"operation denied by sandbox"}}`,
	}
	events := runLines(t, m, lines)
	denied := false
	for _, f := range opFinals(events) {
		if f.Status == "failed" && f.ErrorClass == "sandbox_denied" {
			denied = true
		}
	}
	if !denied {
		t.Errorf("sandbox-denial output did not finalize failed/sandbox_denied")
	}
}

// TestMapper_WebSearchOp asserts a web_search_call + web_search_end produces one
// web tool op enriched by the end event (spec rule #11).
func TestMapper_WebSearchOp(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"web_search_call","call_id":"w1","status":"completed","action":{"type":"search","query":"q"}}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"web_search_end","call_id":"w1","query":"q"}}`,
	}
	events := runLines(t, m, lines)
	web := 0
	for _, s := range opStarts(events) {
		if s.Name == "web_search" && s.ToolNamespace == "web" {
			web++
		}
	}
	if web != 1 {
		t.Fatalf("web_search op count = %d, want 1", web)
	}
}

// TestMapper_NoCleanFinalize asserts a cleanly-ended session (last event a
// task_complete) emits NO SessionFinalizedEvent — it stays running (SOW C#3,
// spec rule #23, tabular "clean EOF" row).
func TestMapper_NoCleanFinalize(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763664000}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)
	if got := countKind(events, canonical.EvSessionFinalized); got != 0 {
		t.Fatalf("SessionFinalized count = %d, want 0 (no clean-EOF finalize)", got)
	}
	// And finalizeStale on a clean session (no open turn) emits nothing.
	if extra := m.finalizeStale(1_700_000_000_000_000); len(extra) != 0 {
		t.Fatalf("finalizeStale on clean session emitted %d events, want 0", len(extra))
	}
}

// TestMapper_SyntheticStaleFinalize asserts a hanging turn (no task_complete)
// yields a synthetic TurnFinalized(failed,incomplete) + SessionFinalized(failed,
// incomplete) ONLY when the scanner calls finalizeStale (spec rule #23, SOW
// C#3, acceptance #5h).
func TestMapper_SyntheticStaleFinalize(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid-crash")
	lines := []string{
		`{"timestamp":"` + tsMeta + `","type":"session_meta","payload":{"id":"sid-crash","source":"cli"}}`,
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1763664000}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
	}
	events := runLines(t, m, lines)
	// Before EOF: no terminal events (turn is in-flight).
	if got := countKind(events, canonical.EvTurnFinalized); got != 0 {
		t.Fatalf("pre-EOF TurnFinalized = %d, want 0", got)
	}

	// Scanner determines mtime is stale ≥ 1h and calls finalizeStale.
	const staleUs = 1_763_700_000_000_000
	stale := m.finalizeStale(staleUs)

	if countKind(stale, canonical.EvTurnFinalized) != 1 {
		t.Fatalf("stale finalize: TurnFinalized = %d, want 1", countKind(stale, canonical.EvTurnFinalized))
	}
	tf := turnFinals(stale)
	if tf[0].Status != "failed" || tf[0].ErrorClass != "incomplete" {
		t.Errorf("stale turn finalize = {%q %q}, want {failed incomplete}", tf[0].Status, tf[0].ErrorClass)
	}
	sf := 0
	for _, ev := range stale {
		if s, ok := ev.(canonical.SessionFinalizedEvent); ok {
			sf++
			if s.Status != canonical.StatusFailed || s.ErrorClass != "incomplete" {
				t.Errorf("stale session finalize = {%q %q}, want {failed incomplete}", s.Status, s.ErrorClass)
			}
		}
	}
	if sf != 1 {
		t.Fatalf("stale SessionFinalized = %d, want 1", sf)
	}
	// The dangling shell op is finalized cancelled.
	cancelled := 0
	for _, f := range opFinals(stale) {
		if f.Status == "cancelled" {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Errorf("stale dangling op finalize cancelled = %d, want 1", cancelled)
	}
	// Idempotent: a second call emits nothing.
	if again := m.finalizeStale(staleUs); len(again) != 0 {
		t.Fatalf("second finalizeStale emitted %d events, want 0 (idempotent)", len(again))
	}
}

// TestMapper_ReplacedTurnThenNewTurn asserts that two task_started without an
// intervening task_complete are both representable: the first turn stays open
// until explicitly closed; the second opens a new turn (spec edge #2). (Codex
// records a turn_aborted(replaced) for the superseded turn in practice; this
// pins that the mapper opens distinct turns per turn_id and does not conflate
// them.)
func TestMapper_ReplacedTurnThenNewTurn(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t1","started_at":1}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t1","reason":"replaced"}}`,
		`{"timestamp":"` + tsEvent + `","type":"turn_context","payload":{"turn_id":"t2","model":"m"}}`,
		`{"timestamp":"` + tsEvent + `","type":"event_msg","payload":{"type":"task_started","turn_id":"t2","started_at":2}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)
	if got := countKind(events, canonical.EvTurnStarted); got != 2 {
		t.Fatalf("TurnStarted = %d, want 2 (t1 + t2)", got)
	}
	tf := turnFinals(events)
	if len(tf) != 2 {
		t.Fatalf("TurnFinalized = %d, want 2", len(tf))
	}
	// Order: t1 failed/replaced (seq 1), t2 completed (seq 2).
	if tf[0].Seq != 1 || tf[0].Status != "failed" || tf[0].ErrorClass != "replaced" {
		t.Errorf("turn1 finalize = %+v, want seq1 failed replaced", tf[0])
	}
	if tf[1].Seq != 2 || tf[1].Status != "completed" {
		t.Errorf("turn2 finalize = %+v, want seq2 completed", tf[1])
	}
}

// TestMapper_PayloadRefLineAnchor asserts a PayloadRef LocationURI carries the
// "file://<abs>#L<line>" line anchor at the owning record's line number (the
// Chunk-B↔D contract; spec rule #6/#7/#8). Chunk D adds containment but keeps
// the anchor.
func TestMapper_PayloadRefLineAnchor(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		// line 3 is the assistant message whose PayloadRef must anchor #L3.
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
	}
	events := runLines(t, m, lines)
	var ref canonical.PayloadRefEvent
	found := false
	for _, ev := range events {
		if r, ok := ev.(canonical.PayloadRefEvent); ok {
			ref = r
			found = true
		}
	}
	if !found {
		t.Fatal("no PayloadRef emitted for the assistant message")
	}
	if !strings.HasSuffix(ref.LocationURI, "#L3") {
		t.Errorf("PayloadRef LocationURI = %q, want suffix #L3", ref.LocationURI)
	}
	if !strings.HasPrefix(ref.LocationURI, "file:///test/sessions/") {
		t.Errorf("PayloadRef LocationURI = %q, want file:// prefix", ref.LocationURI)
	}
}

// TestMapper_SeqMonotone asserts SourceSeq is monotone non-decreasing across the
// emitted stream (packSeq per-record packing; observability only).
func TestMapper_SeqMonotone(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c1"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)
	var last uint64
	for i, ev := range events {
		if ev.EventSourceSeq() < last {
			t.Fatalf("event %d SourceSeq %d < previous %d (not monotone)", i, ev.EventSourceSeq(), last)
		}
		last = ev.EventSourceSeq()
	}
}

// TestMapper_FinalAnswerLog asserts an assistant message with phase=final_answer
// emits an INF LogEntry flagging the final response (spec rule #7).
func TestMapper_FinalAnswerLog(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}}`,
	}
	events := runLines(t, m, lines)
	final := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "final_answer" {
			final = true
		}
	}
	if !final {
		t.Errorf("no final_answer LogEntry emitted")
	}
}

// TestMapper_TurnExtrasLog asserts the turn-meta LogEntry carries codex_turn_id,
// sandbox, effort, and ttft_ms at turn close (the canonical-gap workaround; spec
// gaps #2/#3/#8).
func TestMapper_TurnExtrasLog(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"turn-abc","model":"gpt-5.5","effort":"high","sandbox_policy":{"type":"workspace-write"}}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-abc","started_at":1}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-abc","completed_at":"` + tsDone + `","time_to_first_token_ms":250}}`,
	}
	events := runLines(t, m, lines)
	var meta canonical.LogEntryEvent
	found := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Message == "turn_meta" {
			meta = le
			found = true
		}
	}
	if !found {
		t.Fatal("no turn_meta LogEntry emitted")
	}
	if meta.Extras["codex_turn_id"] != "turn-abc" {
		t.Errorf("codex_turn_id = %v, want turn-abc", meta.Extras["codex_turn_id"])
	}
	if meta.Extras["sandbox"] != "workspace-write" {
		t.Errorf("sandbox = %v, want workspace-write", meta.Extras["sandbox"])
	}
	if meta.Extras["ttft_ms"] != int64(250) {
		t.Errorf("ttft_ms = %v, want 250", meta.Extras["ttft_ms"])
	}
	if meta.TurnSeq != 1 {
		t.Errorf("turn_meta TurnSeq = %d, want 1", meta.TurnSeq)
	}
}

// TestMapper_UnmatchedToolOutputLogged asserts a function_call_output with no
// matching call surfaces a WRN log and emits no op finalize (spec edge #10).
func TestMapper_UnmatchedToolOutputLogged(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call_output","call_id":"orphan","output":"x"}}`,
	}
	events := runLines(t, m, lines)
	warn := false
	for _, ev := range events {
		if le, ok := ev.(canonical.LogEntryEvent); ok && le.Severity == "WRN" && le.Message == "tool_output_unmatched" {
			warn = true
		}
	}
	if !warn {
		t.Errorf("unmatched tool output did not surface a WRN log")
	}
	if got := countKind(events, canonical.EvOpFinalized); got != 0 {
		t.Errorf("unmatched output emitted %d op finalizes, want 0", got)
	}
}

// TestMapper_TwoTurnsOpCounters asserts op Seq restarts per turn (each turn's
// first op is Seq 1) while turn Seq increments globally (spec rule #2,
// data-model UNIQUE(turn_id,seq)).
func TestMapper_TwoTurnsOpCounters(t *testing.T) {
	t.Parallel()
	m := newTestMapper("sid")
	lines := []string{
		metaLine("sid", `"exec"`),
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"t1","model":"m"}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","completed_at":"` + tsDone + `"}}`,
		`{"timestamp":"` + tsDone + `","type":"turn_context","payload":{"turn_id":"t2","model":"m"}}`,
		`{"timestamp":"` + tsDone + `","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"b"}]}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","completed_at":"` + tsDone + `"}}`,
	}
	events := runLines(t, m, lines)

	// Each turn's first (and only) LLM op must be Seq 1.
	llmByTurn := map[int]int{}
	for _, s := range opStarts(events) {
		if s.Kind == canonical.OpLLM {
			llmByTurn[s.TurnSeq] = s.Seq
		}
	}
	if llmByTurn[1] != 1 || llmByTurn[2] != 1 {
		t.Errorf("LLM op Seq per turn = %v, want turn1→1 turn2→1", llmByTurn)
	}
}
