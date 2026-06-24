package claude_code

import (
	"strconv"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func (m *fileMapper) mapCompactSummaryUser(rec record, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := []canonical.Event{m.logEntry(advance(tsUs), "INF", "compaction-summary", rec)}
	ref, ok, err := m.emitSummaryPayload(advance(tsUs), m.lastCompactionTurnSeq, m.lastCompactionOpSeq, rec)
	if err != nil {
		return nil, err
	}
	if ok {
		out = append(out, ref)
	}
	return out, nil
}

func (m *fileMapper) mapUserPrompt(rec record, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, 5)
	if m.turnSeq > 0 && !m.turnFinalized {
		out = append(out, m.finalizeCurrentTurn(advance(tsUs), tsUs))
		m.turnFinalized = true
	}
	turn := m.startUserTurn(advance, tsUs)
	out = append(out, turn)
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	started := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpInternal,
		Name:            "user_input",
	}
	finalized := canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		Status:          "completed",
		EndTs:           tsUs,
	}
	payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, opSeq, "tool_request", "text", rec, "/message/content")
	if err != nil {
		return nil, err
	}
	out = append(out, started, finalized, payload)
	return out, nil
}

func (m *fileMapper) startUserTurn(advance func(int64) canonical.EventBase, tsUs int64) canonical.TurnStartedEvent {
	m.turnSeq++
	m.opSeqInTurn = 0
	m.turnFinalized = false
	return canonical.TurnStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		Seq:             m.turnSeq,
	}
}

func (m *fileMapper) finalizeCurrentTurn(base canonical.EventBase, tsUs int64) canonical.TurnFinalizedEvent {
	return canonical.TurnFinalizedEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		Seq:             m.turnSeq,
		Status:          "completed",
		EndTs:           tsUs,
	}
}

func (m *fileMapper) mapToolResultUser(rec record, blocks []contentBlock, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, len(blocks)+1)
	payloadEmitted := false
	for i := range blocks {
		evs, emitted, err := m.mapToolResultBlock(rec, i, blocks[i], advance, tsUs, payloadEmitted)
		if err != nil {
			return nil, err
		}
		if emitted {
			payloadEmitted = true
		}
		out = append(out, evs...)
	}
	return out, nil
}

func (m *fileMapper) mapToolResultBlock(rec record, index int, blk contentBlock, advance func(int64) canonical.EventBase, tsUs int64, payloadEmitted bool) ([]canonical.Event, bool, error) {
	if blk.Type == "text" {
		evs, err := m.mapUserArrayTextBlock(rec, index, advance, tsUs)
		return evs, false, err
	}
	if blk.Type == "image" {
		evs, err := m.mapUserArrayImageBlock(rec, index, advance, tsUs)
		return evs, false, err
	}
	if blk.Type != "tool_result" || blk.ToolUseID == "" {
		return nil, false, nil
	}
	open, ok := m.toolOps[blk.ToolUseID]
	if !ok {
		return nil, false, nil
	}

	ref, err := m.emitInlinePayload(advance(tsUs), open.turnSeq, open.opSeq, "tool_response", "json", rec, toolResultContentPointer(index))
	if err != nil {
		return nil, false, err
	}
	out := []canonical.Event{ref}
	if rec.HasToolUseResult && !payloadEmitted {
		ref, err := m.emitInlinePayload(advance(tsUs), open.turnSeq, open.opSeq, "tool_response", "json", rec, "/toolUseResult")
		if err != nil {
			return nil, false, err
		}
		out = append(out, ref)
		payloadEmitted = true
	}
	out = append(out, m.toolFinalizedEvent(open, blk, advance(tsUs), tsUs))
	delete(m.toolOps, blk.ToolUseID)
	if open.childID != "" {
		m.resolveAgentOp(open.childID)
	}
	return out, payloadEmitted, nil
}

func (m *fileMapper) mapUserArrayTextBlock(rec record, index int, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, 4)
	if m.turnSeq == 0 {
		out = append(out, m.startUserTurn(advance, tsUs))
	}
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	started := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpInternal,
		Name:            "user_input",
	}
	finalized := canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		Status:          "completed",
		EndTs:           tsUs,
	}
	payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, opSeq, "tool_request", "text", rec, userArrayTextPointer(index))
	if err != nil {
		return nil, err
	}
	out = append(out, started, finalized, payload)
	return out, nil
}

func (m *fileMapper) mapUserArrayImageBlock(rec record, index int, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, 4)
	if m.turnSeq == 0 {
		out = append(out, m.startUserTurn(advance, tsUs))
	}
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	started := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpInternal,
		Name:            "user_input",
	}
	finalized := canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		Status:          "completed",
		EndTs:           tsUs,
	}
	payload, err := m.emitInlinePayload(advance(tsUs), m.turnSeq, opSeq, "tool_request", "json", rec, userArrayBlockPointer(index))
	if err != nil {
		return nil, err
	}
	out = append(out, started, finalized, payload)
	return out, nil
}

func toolResultContentPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index) + "/content"
}

func userArrayTextPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index) + "/text"
}

func userArrayBlockPointer(index int) string {
	return "/message/content/" + strconv.Itoa(index)
}

func (m *fileMapper) toolFinalizedEvent(open openToolOp, blk contentBlock, base canonical.EventBase, tsUs int64) canonical.OpFinalizedEvent {
	status, errClass := toolResultStatus(blk)
	return canonical.OpFinalizedEvent{
		EventBase:       base,
		SessionNativeID: m.nativeID,
		TurnSeq:         open.turnSeq,
		Seq:             open.opSeq,
		Status:          status,
		ErrorClass:      errClass,
		EndTs:           tsUs,
	}
}

func toolResultStatus(blk contentBlock) (string, string) {
	if blk.IsError {
		return "failed", "tool_error"
	}
	return "completed", ""
}
