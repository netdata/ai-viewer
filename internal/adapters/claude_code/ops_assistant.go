package claude_code

import (
	"encoding/json"
	"strconv"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type toolUseStarted struct {
	event   canonical.OpStartedEvent
	toolID  string
	opName  string
	opSeq   int
	childID string
}

func (m *fileMapper) ensureAssistantTurn() {
	if m.turnSeq != 0 {
		return
	}
	m.turnSeq = 1
	m.opSeqInTurn = 0
	m.turnFinalized = false
}

func (m *fileMapper) assistantModelUpdate(advance func(int64) canonical.EventBase, tsUs int64, model string) (canonical.SessionUpdatedEvent, bool) {
	if m.modelSeen {
		return canonical.SessionUpdatedEvent{}, false
	}
	ev := canonical.SessionUpdatedEvent{
		EventBase: advance(tsUs),
		NativeID:  m.nativeID,
		Model:     model,
	}
	m.modelSeen = true
	return ev, true
}

func (m *fileMapper) startAssistantLLM(advance func(int64) canonical.EventBase, tsUs int64, msg *assistantMessage) (canonical.OpStartedEvent, int) {
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	return canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpLLM,
		Name:            msg.Model,
		Model:           msg.Model,
		Provider:        provider,
		Extras:          assistantUsageExtras(msg.Usage),
	}, opSeq
}

// buildLLMFinalized builds the OpFinalizedEvent for an LLM op, carrying the
// token accounting derived from message.usage (spec §5.6).
func (m *fileMapper) buildLLMFinalized(msg *assistantMessage, base canonical.EventBase, turnSeq, opSeq int, endUs int64) canonical.OpFinalizedEvent {
	ev := canonical.OpFinalizedEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		Status:          "completed",
		EndTs:           endUs,
	}
	if u := msg.Usage; u != nil {
		// Canonical token contract (canonical-events.md, SOW-0029): TokensIn is
		// the FRESH/uncached input ONLY; the cache portions are separate counters.
		ev.TokensIn = u.InputTokens
		ev.TokensOut = u.OutputTokens
		ev.TokensCacheRead = u.CacheReadInputTokens
		ev.TokensCacheWrite = u.CacheCreationInputTokens
		ev.CtxUsed = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens
	}
	return ev
}

// assistantUsageExtras surfaces the cache-token decomposition and server
// tool-use counters on the LLM op's extras (spec §5.6, §11.8).
func assistantUsageExtras(u *assistantUsage) map[string]any {
	if u == nil {
		return nil
	}
	extras := map[string]any{
		"cacheRead":     u.CacheReadInputTokens,
		"cacheCreation": u.CacheCreationInputTokens,
		"uncachedInput": u.InputTokens,
	}
	if u.ServiceTier != "" {
		extras["serviceTier"] = u.ServiceTier
	}
	if len(u.ServerToolUse) > 0 {
		extras["serverToolUse"] = u.ServerToolUse
	}
	return extras
}

func (m *fileMapper) emitAssistantTextPayloads(rec record, content []contentBlock, advance func(int64) canonical.EventBase, tsUs int64, llmSeq int) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, len(content))
	for i := range content {
		if content[i].Type != "text" {
			continue
		}
		payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, llmSeq, "llm_response", "text", rec, assistantTextPointer(i))
		if err != nil {
			return nil, err
		}
		out = append(out, payload)
	}
	return out, nil
}

func (m *fileMapper) emitAssistantReasoningOps(rec record, content []contentBlock, advance func(int64) canonical.EventBase, tsUs int64, llmSeq int) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, len(content)*2)
	for i := range content {
		if content[i].Type != "thinking" {
			continue
		}
		events, err := m.reasoningOpEvents(rec, i, content[i], advance, tsUs, llmSeq)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
}

func (m *fileMapper) reasoningOpEvents(rec record, index int, blk contentBlock, advance func(int64) canonical.EventBase, tsUs int64, llmSeq int) ([]canonical.Event, error) {
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, opSeq, "llm_reasoning", "text", rec, reasoningPointer(index))
	if err != nil {
		return nil, err
	}
	return []canonical.Event{
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         m.turnSeq,
			Seq:             opSeq,
			ParentOpSeq:     llmSeq,
			Kind:            canonical.OpReasoning,
			ReasoningKind:   "raw",
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         m.turnSeq,
			Seq:             opSeq,
			Status:          "completed",
			EndTs:           tsUs,
			BytesOut:        int64(len(blk.Thinking)),
		},
		payload,
	}, nil
}

func (m *fileMapper) emitAssistantToolUseOps(rec record, content []contentBlock, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, len(content))
	for i := range content {
		if content[i].Type != "tool_use" {
			continue
		}
		started := m.toolUseStarted(content[i], advance, tsUs)
		out = append(out, started.event)
		payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, started.opSeq, "tool_request", "json", rec, toolUseInputPointer(i))
		if err != nil {
			return nil, err
		}
		out = append(out, payload)
		m.rememberOpenToolUse(started)
	}
	return out, nil
}

func (m *fileMapper) toolUseStarted(blk contentBlock, advance func(int64) canonical.EventBase, tsUs int64) toolUseStarted {
	m.opSeqInTurn++
	opName, namespace := splitToolName(blk.Name)
	childID := ""
	event := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             m.opSeqInTurn,
		ParentOpSeq:     -1,
		Name:            opName,
		ToolNamespace:   namespace,
	}
	if blk.Name == "Agent" {
		event, childID = m.agentToolUseStarted(blk, event)
	} else {
		event.Kind = canonical.OpTool
	}
	return toolUseStarted{event: event, toolID: blk.ID, opName: opName, opSeq: m.opSeqInTurn, childID: childID}
}

func (m *fileMapper) agentToolUseStarted(blk contentBlock, event canonical.OpStartedEvent) (canonical.OpStartedEvent, string) {
	event.Kind = canonical.OpSession
	if blk.ID != "" {
		event.Extras = map[string]any{"aiViewer": map[string]any{"toolUseId": blk.ID}}
	}
	childID := ""
	if agentID, ok := m.toolUseToAgent[blk.ID]; ok && agentID != "" {
		childID = childNativeID(m.nativeID, agentID)
		event.ChildSessionNativeID = childID
		m.rememberAgentOp(childID, event.Seq)
	}
	if desc := agentDescription(blk.Input); desc != "" {
		event.Name = desc
	}
	return event, childID
}

func (m *fileMapper) rememberAgentOp(childID string, opSeq int) {
	if m.agentOps == nil {
		m.agentOps = map[string]agentOpRef{}
	}
	m.agentOps[childID] = agentOpRef{turnSeq: m.turnSeq, opSeq: opSeq}
}

func (m *fileMapper) resolveAgentOp(childID string) {
	delete(m.agentOps, childID)
	if m.agentOpsResolved == nil {
		m.agentOpsResolved = map[string]struct{}{}
	}
	m.agentOpsResolved[childID] = struct{}{}
}

func (m *fileMapper) rememberOpenToolUse(started toolUseStarted) {
	if started.toolID == "" {
		return
	}
	m.toolOps[started.toolID] = openToolOp{
		turnSeq: m.turnSeq,
		opSeq:   started.opSeq,
		name:    started.opName,
		childID: started.childID,
	}
}

// agentDescription extracts the Agent tool_use input's "description" field
// for use as the child-session op name. Returns "" when absent.
func agentDescription(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	return m.Description
}

func assistantTextPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index) + "/text"
}

func reasoningPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index) + "/thinking"
}

func toolUseInputPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index) + "/input"
}
