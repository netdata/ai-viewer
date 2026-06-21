package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapResponseItem dispatches a response_item record to its per-variant emitter
// (spec rules #6-13, #20). The nested payload.type discriminator was validated
// by the parser; an empty type is unreachable here (parseLine skips it). The
// full record is threaded through so emitters can size PayloadRef.OriginalBytes
// from the verbatim line and read sibling fields (e.g. message.phase) off Raw.
//
//nolint:unparam // error return is required by the record-type dispatch in mapRecord, which calls mapEventMsg/mapResponseItem through a uniform (evs, error) shape and propagates a non-nil error from either
func (m *fileMapper) mapResponseItem(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	p := rec.ResponseItem
	if p == nil {
		return nil, nil
	}
	tsUs := m.recordTs(rec)
	bodyBytes := int64(len(rec.Raw))
	switch p.Type {
	case "message":
		return m.mapMessage(rec, advance, tsUs, bodyBytes), nil
	case "reasoning":
		return m.mapReasoning(p, advance, tsUs, bodyBytes), nil
	case "function_call", "custom_tool_call", "local_shell_call",
		"tool_search_call":
		return m.mapToolCall(p, advance, tsUs, bodyBytes), nil
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output",
		"tool_search_output":
		return m.mapToolOutput(p, advance, tsUs, bodyBytes), nil
	case "web_search_call":
		return m.mapWebSearchCall(p, advance, tsUs, bodyBytes), nil
	case "image_generation_call":
		return m.mapImageGenCall(p, advance, tsUs, bodyBytes), nil
	case "compaction", "context_compaction":
		// response_item compaction variants converge on OpCompaction (spec rule
		// #20, gap #4). The body (encrypted_content) is opaque; preview omitted.
		return m.emitCompactionOp(advance, tsUs, map[string]any{"trigger": "auto"}, "json"), nil
	default:
		// Unreachable for persisted variants (parser allowlist); a defensive
		// LogEntry keeps a future persisted-but-unmapped variant visible.
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "response_item:"+p.Type, nil)}, nil
	}
}

// mapMessage handles response_item.message (spec rule #6 user, #7 assistant). A
// user message opens an internal user_input op (deduped against
// event_msg.user_message); an assistant message opens an llm op stamped with the
// turn model. Both attach the body as a PayloadRef. A final_answer assistant
// message also emits an INF LogEntry so the UI can flag the final response.
func (m *fileMapper) mapMessage(rec record, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	p := rec.ResponseItem
	if p.Role == "user" {
		return m.emitUserInput(advance, tsUs, messageText(p.Content), "json", bodyBytes)
	}
	// assistant / system / developer → llm op (the assistant is the LLM output;
	// system/developer messages are rare inline instructions, still llm-kind so
	// they show on the timeline with the model).
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 4)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	ts.lastLLMOpSeq = opSeq
	ts.lastLLMEndTs = tsUs
	out = append(out,
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            "message",
			Model:           m.model,
			Provider:        provider,
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			Status:          "completed",
			EndTs:           tsUs,
		},
		m.payloadRef(advance(tsUs), turnSeq, opSeq, "llm_response", "json", bodyBytes),
	)
	if phaseFromRaw(rec.Raw) == "final_answer" {
		out = append(out, m.logEntry(advance(tsUs), "INF", "final_answer", nil))
	}
	return out
}

// emitUserInput emits the internal user_input op pair + body PayloadRef, deduped
// against the companion event_msg.user_message form (spec rule #6, #18). When
// the fingerprint was already seen, it returns nil so the UI sees exactly one
// user op per logical input. A user message also opens a new turn under the
// active turn_id when none is open (old-CLI user-message boundary, spec edge #3).
//
// SOW-0089 chunk 5c — sub-turn splitting. When the active codex turn has
// ALREADY seen a user_input op (count >= 1) AND has no in-flight tool call
// awaiting its *_output, we finalize the current turn (synthetic
// TurnFinalizedEvent, status=completed, end_ts=now) and open a new sub-turn
// with synthetic codex_turn_id "sub:<counter>". The new sub-turn inherits
// the parent's model/sandbox/effort/approval_policy (snapped from
// turn_context — the codex format doesn't re-emit these per user_input).
//
// Skipping the split when hasOpenToolCall is true avoids orphaning a
// function_call whose *_output is still pending — we'd produce two turns
// with the tool call in the prior turn and its output in the new turn,
// which the in-flight tracker (m.openOps) can't represent. The split
// happens on the NEXT user_input after the tool resolves.
func (m *fileMapper) emitUserInput(advance func(int64) canonical.EventBase, tsUs int64, text, format string, bodyBytes int64) []canonical.Event {
	if !m.firstSeenUser(userFingerprint(text)) {
		return nil
	}
	out := make([]canonical.Event, 0, 4)

	// Sub-turn split: close the current turn (if it has user inputs already)
	// and open a new sub-turn. NO split if a tool call is in flight.
	if m.haveActiveTurn {
		if active, ok := m.turns[m.activeTurnID]; ok {
			if active.userInputCount >= 1 && !active.hasOpenToolCall && !active.finalized {
				out = append(out, m.finalizeTurnForSubSplit(advance, active, tsUs)...)
				// Open the new sub-turn with a synthetic codex_turn_id. openTurn
				// resets ts.userInputCount to 0 and ts.hasOpenToolCall to false via
				// the new turnState constructor. Per-turn policy snapshots (sandbox,
				// effort, approvalPolicy) carry over via the prior turn's
				// turnExtras, NOT via ts.model — the model/provider are owned by
				// fileMapper (file-level state, not per-turn).
				m.subTurnCounter++
				subID := fmt.Sprintf("sub:%d", m.subTurnCounter)
				subTs := m.openTurn(subID, tsUs)
				subTs.sandbox = active.sandbox
				subTs.effort = active.effort
				subTs.approvalPolicy = active.approvalPolicy
			}
		}
	}

	ts := m.ensureTurn(tsUs)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	ts.userInputCount++
	out = append(out,
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpInternal,
			Name:            "user_input",
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			Status:          "completed",
			EndTs:           tsUs,
		},
		m.payloadRef(advance(tsUs), turnSeq, opSeq, "tool_request", format, bodyBytes),
	)
	return out
}

// mapReasoning handles response_item.reasoning (spec rule #8, acceptance #4). It
// emits an OpReasoning pair with reasoning_kind = "summary" when only summary[]
// is non-empty, "raw" when content[] carries text OR encrypted_content is set.
// The body goes to a PayloadRef (Format=text for a summary, json for the full
// item). event_msg.agent_reasoning* is a LogEntry only (ops_event.go) so the UI
// never sees a duplicate reasoning op.
func (m *fileMapper) mapReasoning(p *responseItemPayload, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	kind, format := reasoningKind(p)
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 4)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	out = append(out,
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpReasoning,
			Name:            "reasoning",
			ReasoningKind:   kind,
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         turnSeq,
			Seq:             opSeq,
			Status:          "completed",
			EndTs:           tsUs,
		},
		m.payloadRef(advance(tsUs), turnSeq, opSeq, "llm_reasoning", format, bodyBytes),
	)
	return out
}

// reasoningKind classifies a reasoning item (spec rule #8, acceptance #4):
// "summary" when only summary[] is non-empty (PayloadRef Format=text), "raw"
// when content[] has text or encrypted_content is set (Format=json). When both
// summary and raw signals are present, raw wins (the durable model state is the
// fuller record). Defaults to "raw"/json when the item is opaque (encrypted).
func reasoningKind(p *responseItemPayload) (kind, format string) {
	hasSummary := jsonArrayNonEmpty(p.Summary)
	hasContent := jsonArrayNonEmpty(p.Content)
	hasEnc := len(jsonTrim(p.EncryptedContent)) > 0
	switch {
	case hasContent || hasEnc:
		return "raw", "json"
	case hasSummary:
		return "summary", "text"
	default:
		// No discernible body (rare); treat as raw so the op is not mislabeled
		// a summary it does not carry.
		return "raw", "json"
	}
}

// jsonArrayNonEmpty reports whether raw is a JSON array with at least one
// element. Tolerates null/absent (returns false).
func jsonArrayNonEmpty(raw json.RawMessage) bool {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return false
	}
	var arr []json.RawMessage
	if json.Unmarshal(body, &arr) != nil {
		return false
	}
	return len(arr) > 0
}

// messageText extracts the concatenated text of a message content[] array for
// the user-dedup fingerprint (spec rule #6). Each element is
// {type:"input_text"|"output_text"|..., text}. Returns "" when absent or when no
// element carries text.
func messageText(raw json.RawMessage) string {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return ""
	}
	var items []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(body, &items) != nil {
		return ""
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.Text)
	}
	return b.String()
}

// phaseFromRaw reads the optional message.phase ("commentary" | "final_answer")
// off the verbatim payload bytes (spec rule #7). phase is a sibling field not
// kept in the typed responseItemPayload, so it is decoded narrowly from the
// payload object inside the envelope. Returns "" when absent or unparseable.
func phaseFromRaw(raw []byte) string {
	var env struct {
		Payload struct {
			Phase string `json:"phase"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	return env.Payload.Phase
}

// finalizeTurnForSubSplit (SOW-0089 chunk 5c) emits a synthetic
// TurnFinalizedEvent for `ts` (the active codex turn) to close it BEFORE we
// open a new sub-turn at a user_input boundary. The event carries:
//
//   - Status: "completed" (a sub-split is NOT a failure — the codex task
//     itself is still alive; we're just visualizing one user/assistant
//     exchange as its own turn).
//   - EndTs: tsUs (the timestamp of the new user_input that triggered the
//     split — operator sees an accurate duration for the prior sub-turn).
//   - The full per-turn extras (rollup, sandbox, ttft_ms, etc) so the
//     finalized sub-turn's row in turns.extras_json is complete.
//
// We mark ts.finalized=true so a subsequent task_complete (for the parent
// codex task) does NOT re-finalize this turn (finalizeTurn is idempotent
// on the flag — see mapper_turn.go).
func (m *fileMapper) finalizeTurnForSubSplit(advance func(int64) canonical.EventBase, ts *turnState, tsUs int64) []canonical.Event {
	if ts.finalized {
		return nil
	}
	base := advance(tsUs)
	return []canonical.Event{m.finalizeTurn(ts, base, tsUs, "completed", "")}
}
