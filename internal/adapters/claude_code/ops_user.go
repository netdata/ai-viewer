package claude_code

import "github.com/netdata/ai-viewer/internal/canonical"

func (m *fileMapper) mapCompactSummaryUser(rec record, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := []canonical.Event{m.logEntry(advance(tsUs), "INF", "compaction-summary", rec)}
	ref, ok, err := m.emitSummaryPayload(advance(tsUs), m.lastCompactionTurnSeq, m.lastCompactionOpSeq)
	if err != nil {
		return nil, err
	}
	if ok {
		out = append(out, ref)
	}
	return out, nil
}

func (m *fileMapper) startUserTurn(advance func(int64) canonical.EventBase, tsUs int64) canonical.TurnStartedEvent {
	m.turnSeq++
	m.opSeqInTurn = 0
	return canonical.TurnStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		Seq:             m.turnSeq,
	}
}

func (m *fileMapper) mapToolResultUser(rec record, blocks []contentBlock, advance func(int64) canonical.EventBase, tsUs int64) ([]canonical.Event, error) {
	out := make([]canonical.Event, 0, len(blocks)+1)
	payloadEmitted := false
	for i := range blocks {
		evs, emitted, err := m.mapToolResultBlock(rec, blocks[i], advance, tsUs, payloadEmitted)
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

func (m *fileMapper) mapToolResultBlock(rec record, blk contentBlock, advance func(int64) canonical.EventBase, tsUs int64, payloadEmitted bool) ([]canonical.Event, bool, error) {
	if blk.Type != "tool_result" || blk.ToolUseID == "" {
		return nil, false, nil
	}
	open, ok := m.toolOps[blk.ToolUseID]
	if !ok {
		return nil, false, nil
	}

	out := []canonical.Event{m.toolFinalizedEvent(open, blk, advance(tsUs), tsUs)}
	if rec.HasToolUseResult && !payloadEmitted {
		ref, err := m.emitToolResultPayload(advance(tsUs), open.turnSeq, open.opSeq)
		if err != nil {
			return nil, false, err
		}
		out = append(out, ref)
		payloadEmitted = true
	}
	delete(m.toolOps, blk.ToolUseID)
	return out, payloadEmitted, nil
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
