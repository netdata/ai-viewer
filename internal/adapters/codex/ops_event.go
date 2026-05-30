package codex

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapEventMsg dispatches an event_msg record to its per-variant handler (spec
// rules #3, #4, #5, #8, #14-20, #22). Variants the adapter consumes only for
// enrichment (exec_command_end, mcp_tool_call_end, patch_apply_end) merge onto
// an already-emitted op and do NOT emit a second op. Variants the adapter uses
// only for the UI (agent_reasoning*, agent_message) produce a LogEntry, never a
// duplicate op.
func (m *fileMapper) mapEventMsg(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	p := rec.EventMsg
	if p == nil {
		return nil, nil
	}
	tsUs := m.recordTs(rec)
	switch p.Type {
	case "task_started", "turn_started":
		return m.mapTaskStarted(rec, advance, tsUs), nil
	case "task_complete", "turn_complete":
		return m.mapTaskComplete(rec, advance, tsUs), nil
	case "turn_aborted":
		return m.mapTurnAborted(rec, advance, tsUs), nil
	case "user_message":
		return m.emitUserInput(advance, tsUs, p.Message, "json", int64(len(rec.Raw))), nil
	case "agent_message":
		// Dedup companion to response_item.message(assistant) (spec rule #19):
		// no op; stash the message as the turn's last_agent_message preview and
		// surface a DBG log so the UI reasoning/answer panel can show it.
		m.stashAgentMessage(p.Message)
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "agent_message", nil)}, nil
	case "agent_reasoning", "agent_reasoning_raw_content":
		// Reasoning UI summary (spec rule #8): LogEntry ONLY — the canonical
		// reasoning op is emitted from response_item.reasoning so the UI never
		// sees a duplicate.
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "agent_reasoning", nil)}, nil
	case "token_count":
		return m.mapTokenCount(rec, advance, tsUs), nil
	case "exec_command_end":
		return m.enrichOp(rec, advance, tsUs, execCommandExtras), nil
	case "mcp_tool_call_end":
		return m.enrichMcp(rec, advance, tsUs), nil
	case "patch_apply_end":
		return m.enrichPatchApply(rec, advance, tsUs), nil
	case "web_search_end":
		return m.enrichOp(rec, advance, tsUs, webSearchExtras), nil
	case "image_generation_end":
		return m.enrichOp(rec, advance, tsUs, nil), nil
	case "context_compacted":
		// event_msg.context_compacted → OpCompaction (spec rule #20, gap #4).
		return m.emitCompactionOp(advance, tsUs, map[string]any{"trigger": "auto"}, "json"), nil
	case "error":
		return []canonical.Event{m.logEntry(advance(tsUs), "ERR", "error", errorExtras(p))}, nil
	case "thread_rolled_back":
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", "thread_rolled_back", nil)}, nil
	case "entered_review_mode", "exited_review_mode":
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", p.Type, nil)}, nil
	case "item_completed":
		// Plan items (spec gap #11): INF log for now.
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", "item_completed", nil)}, nil
	default:
		// thread_goal_updated, guardian_assessment, view_image_tool_call,
		// dynamic_tool_call_*, and any future persisted variant: keep visible.
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "event_msg:"+p.Type, nil)}, nil
	}
}

// mapTaskStarted handles event_msg.task_started (alias turn_started) (spec rule
// #3, #22). It opens the turn for turn_id (idempotent with turn_context) and
// emits TurnStartedEvent the first time. started_at (unix seconds) is used as
// the canonical Ts when it is newer than the wire timestamp. model_context_window
// is stashed for the turn's LLM ctx_max (spec rule #3, #17).
func (m *fileMapper) mapTaskStarted(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	startUs := tsUs
	if sa := startedAtMicros(rec.Raw); sa > startUs {
		startUs = sa
	}
	ts := m.openTurn(p.TurnID, startUs)
	out := make([]canonical.Event, 0, 1)
	if ev := m.emitTurnStarted(ts, advance(startUs)); ev != nil {
		out = append(out, ev)
	}
	if mcw := modelContextWindow(rec.Raw); mcw > 0 {
		ts.ctxMax = mcw
	}
	return out
}

// mapTaskComplete handles event_msg.task_complete (alias turn_complete) (spec
// rule #4). It finalizes the turn (completed) with the C#1 token rollup,
// finalizes every dangling op tied to the turn (status "completed" inferred —
// codex output for a completed turn that lacked an explicit _output is treated
// as success), records ttft_ms, and applies the turn's accumulated CtxUsed/CtxMax
// to its last LLM op (spec rule #4, #17). completed_at is the EndTs when present.
func (m *fileMapper) mapTaskComplete(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	ts, ok := m.turns[p.TurnID]
	if !ok || ts.finalized {
		// task_complete with no open turn (or already closed): surface and skip
		// so a stray completion does not double-close (spec edge robustness).
		return []canonical.Event{m.logEntry(advance(tsUs), "WRN", "task_complete_no_turn", map[string]any{"turn_id": p.TurnID})}
	}
	endUs := tsUs
	if ca := completedAtMicros(rec.Raw); ca > 0 {
		endUs = ca
	}
	if ttft := ttftMillis(rec.Raw); ttft > 0 {
		ts.ttftMs = ttft
	}
	base := func() canonical.EventBase { return advance(endUs) }
	out := make([]canonical.Event, 0, 4)
	// Apply the accumulated ctx to the turn's last LLM op (spec rule #17).
	if ev, ok := m.applyLLMCtx(ts, base); ok {
		out = append(out, ev)
	}
	// Finalize dangling ops BEFORE the turn close so they share the turn (spec
	// rule #4, edge #9: status completed inferred at task_complete).
	out = append(out, m.finalizeDanglingOps(p.TurnID, base, endUs, "completed")...)
	out = append(out, m.finalizeTurn(ts, base(), endUs, "completed", ""))
	if ev := m.turnExtrasLog(ts, base()); ev != nil {
		out = append(out, ev)
	}
	return out
}

// mapTurnAborted handles event_msg.turn_aborted (spec rule #5, edge #2). It
// finalizes the turn (failed) with the reason→ErrorClass mapping and finalizes
// dangling ops as "cancelled" (edge #9 — the user interrupted, so in-flight ops
// did not complete). completed_at is the EndTs when present.
func (m *fileMapper) mapTurnAborted(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	ts, ok := m.turns[p.TurnID]
	if !ok || ts.finalized {
		return []canonical.Event{m.logEntry(advance(tsUs), "WRN", "turn_aborted_no_turn", map[string]any{"turn_id": p.TurnID})}
	}
	endUs := tsUs
	if ca := completedAtMicros(rec.Raw); ca > 0 {
		endUs = ca
	}
	base := func() canonical.EventBase { return advance(endUs) }
	out := make([]canonical.Event, 0, 3)
	out = append(out, m.finalizeDanglingOps(p.TurnID, base, endUs, "cancelled")...)
	out = append(out, m.finalizeTurn(ts, base(), endUs, "failed", abortErrorClass(p.Reason)))
	if ev := m.turnExtrasLog(ts, base()); ev != nil {
		out = append(out, ev)
	}
	return out
}

// mapTokenCount handles event_msg.token_count (spec rule #17, C#1). It folds the
// per-call last_token_usage into the attributed turn's rollup and stashes the
// cumulative total / model_context_window for the turn's last LLM op. A
// token_count carrying turn_id attributes to that turn; one without attributes
// to the most-recently-active turn ("Token accounting nuance"). model_context_
// window is also surfaced to the catalog via the next LLM op's CtxMax at turn
// finalize. token_count itself emits no event.
func (m *fileMapper) mapTokenCount(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	info := decodeTokenCount(rec.Raw)
	ts := m.tokenTurn(p.TurnID)
	if ts == nil {
		// No turn to attribute to yet (token_count before any turn opened):
		// surface a DBG log so it is visible and drop the counts (they cannot be
		// attributed; rare and not load-bearing).
		_ = tsUs
		return nil
	}
	ts.addTokenUsage(info)
	return nil
}

// tokenTurn resolves the turn a token_count attributes to (spec rule #17,
// "Token accounting nuance"): the turn for turn_id when present and known, else
// the most-recently-active turn. Returns nil when no turn is open.
func (m *fileMapper) tokenTurn(turnID string) *turnState {
	if turnID != "" {
		if ts, ok := m.turns[turnID]; ok {
			return ts
		}
	}
	if m.haveActiveTurn {
		if ts, ok := m.turns[m.activeTurnID]; ok {
			return ts
		}
	}
	return nil
}

// applyLLMCtx emits an OpFinalizedEvent that sets CtxUsed/CtxMax on the turn's
// last LLM op (spec rule #17) when the turn accumulated a cumulative total and
// has an LLM op to attach it to. The ingester reconciles this finalize with the
// op's earlier finalize (idempotent upsert keyed on (turn,seq)). Returns
// (event, true) when emitted, (zero, false) when there is nothing to apply.
func (m *fileMapper) applyLLMCtx(ts *turnState, base func() canonical.EventBase) (canonical.OpFinalizedEvent, bool) {
	if ts.lastLLMOpSeq == 0 || (ts.lastLLMCtxUsed == 0 && ts.ctxMax == 0) {
		return canonical.OpFinalizedEvent{}, false
	}
	endUs := ts.lastLLMEndTs
	if endUs == 0 {
		endUs = ts.startTsUs
	}
	return canonical.OpFinalizedEvent{
		EventBase:       base(),
		SessionNativeID: m.nativeID,
		TurnSeq:         ts.seq,
		Seq:             ts.lastLLMOpSeq,
		Status:          "completed",
		EndTs:           endUs,
		CtxUsed:         ts.lastLLMCtxUsed,
		CtxMax:          ts.ctxMax,
	}, true
}

// stashAgentMessage records event_msg.agent_message.message as the active
// turn's last_agent_message preview (spec rule #19). Truncated to previewMax
// runes; full text lives in the response_item.message PayloadRef.
func (m *fileMapper) stashAgentMessage(msg string) {
	if !m.haveActiveTurn {
		return
	}
	if ts, ok := m.turns[m.activeTurnID]; ok {
		if prev := trimPreview(msg, previewMax); prev != "" {
			ts.lastAgentMessage = prev
		}
	}
}

// abortErrorClass maps a turn_aborted reason to a canonical ErrorClass (spec
// rule #5). Unknown reasons pass through verbatim (forward-compat).
func abortErrorClass(reason string) string {
	switch reason {
	case "interrupted":
		return "user_interrupt"
	case "replaced":
		return "replaced"
	case "review_ended":
		return "review_ended"
	case "budget_limited":
		return "rate_limit"
	default:
		return reason
	}
}

// turnExtrasLog emits an INF LogEntry carrying the turn's computed metadata that
// the spec routes to turns.extras_json — codex_turn_id, sandbox, effort,
// approval_policy, ttft_ms, last_agent_message (spec "Canonical Model Gaps" #2,
// #3, #8; rule #19). It is scoped to the turn (TurnSeq) so the UI's per-turn
// Logs surface it.
//
// IMPORTANT (canonical-model gap surfaced in Chunk B): the canonical
// TurnFinalizedEvent has NO Extras field and the ingest writer's turns INSERT
// (internal/ingest/writer.go) does not populate turns.extras_json from any
// event, so these values cannot reach turns.extras_json today. Emitting them as
// a turn-scoped LogEntry keeps the data DURABLE and VISIBLE (no silent loss)
// without touching the canonical schema or the writer, both out of Chunk B
// scope. A follow-up SOW should add a turn-extras carrier (a TurnFinalized
// Extras field or a turn-scoped SessionUpdated-style event) so the data lands in
// turns.extras_json as the spec intends. Returns nil when the turn carried no
// surfaced metadata.
func (m *fileMapper) turnExtrasLog(ts *turnState, base canonical.EventBase) canonical.Event {
	extras := map[string]any{}
	if ts.codexTurnID != "" {
		extras["codex_turn_id"] = ts.codexTurnID
	}
	if ts.sandbox != "" {
		extras["sandbox"] = ts.sandbox
	}
	if ts.effort != "" {
		extras["effort"] = ts.effort
	}
	if ts.approvalPolicy != "" {
		extras["approval_policy"] = ts.approvalPolicy
	}
	if ts.ttftMs > 0 {
		extras["ttft_ms"] = ts.ttftMs
	}
	if ts.lastAgentMessage != "" {
		extras["last_agent_message"] = ts.lastAgentMessage
	}
	if len(extras) == 0 {
		return nil
	}
	le := m.logEntry(base, "INF", "turn_meta", extras)
	le.TurnSeq = ts.seq
	return le
}

// errorExtras surfaces an event_msg.error message in the LogEntry extras.
func errorExtras(p *eventMsgPayload) map[string]any {
	if p.Message == "" {
		return nil
	}
	return map[string]any{"message": trimPreview(p.Message, previewMax)}
}

// startedAtMicros reads task_started.started_at (unix seconds) from the raw
// payload and returns it in micros, or 0 when absent (spec rule #3).
func startedAtMicros(raw []byte) int64 {
	v := payloadNumber(raw, "started_at")
	if v == 0 {
		return 0
	}
	return v * 1_000_000
}

// completedAtMicros reads task_complete/turn_aborted.completed_at, accepting
// either an RFC3339 string or a unix-seconds number, and returns micros (0 when
// absent). codex versions vary in the encoding (spec rule #4, #5).
func completedAtMicros(raw []byte) int64 {
	var env struct {
		Payload struct {
			CompletedAt json.RawMessage `json:"completed_at"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return 0
	}
	body := jsonTrim(env.Payload.CompletedAt)
	if len(body) == 0 {
		return 0
	}
	var s string
	if json.Unmarshal(body, &s) == nil {
		if us, err := parseTsToMicros(s); err == nil {
			return us
		}
		return 0
	}
	var secs int64
	if json.Unmarshal(body, &secs) == nil {
		return secs * 1_000_000
	}
	return 0
}

// ttftMillis reads task_complete.time_to_first_token_ms (spec gap #8).
func ttftMillis(raw []byte) int64 { return payloadNumber(raw, "time_to_first_token_ms") }

// modelContextWindow reads task_started/token_count.model_context_window (spec
// rule #3, #17).
func modelContextWindow(raw []byte) int64 { return payloadNumber(raw, "model_context_window") }

// payloadNumber extracts an integer field from the payload object inside the
// envelope. Returns 0 when absent or non-numeric. A shared narrow decoder so
// each scalar lookup avoids a bespoke struct.
func payloadNumber(raw []byte, field string) int64 {
	var env struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return 0
	}
	body := jsonTrim(env.Payload[field])
	if len(body) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(body, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(body, &f) == nil {
		return int64(f)
	}
	return 0
}
