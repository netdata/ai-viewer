package codex

import (
	"encoding/json"
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
func (m *fileMapper) emitUserInput(advance func(int64) canonical.EventBase, tsUs int64, text, format string, bodyBytes int64) []canonical.Event {
	if !m.firstSeenUser(userFingerprint(text)) {
		return nil
	}
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
