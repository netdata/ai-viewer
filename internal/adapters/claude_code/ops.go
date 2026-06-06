package claude_code

import "github.com/netdata/ai-viewer/internal/canonical"

// mapUser handles the `user` record shapes from spec §5.4:
//   - string content, non-meta, non-compact-summary → opens turn N+1.
//   - array content (tool_result blocks) → finalizes the matching tool ops.
//   - isMeta → LogEntry only, no turn.
//   - isCompactSummary → LogEntry plus summary PayloadRef when present, no turn.
func (m *fileMapper) mapUser(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	tsUs := m.recordTs(rec)

	if boolValue(rec.Env.IsMeta) {
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "meta-user", rec)}, nil
	}
	if boolValue(rec.Env.IsCompactSummary) {
		return m.mapCompactSummaryUser(rec, advance, tsUs)
	}

	_, blocks, isString := classifyUserContent(rec.User)
	if isString {
		return []canonical.Event{m.startUserTurn(advance, tsUs)}, nil
	}

	return m.mapToolResultUser(rec, blocks, advance, tsUs)
}

// mapAssistant handles an `assistant` record. A non-synthetic model emits an
// LLM op (started+finalized with usage), a nested reasoning op per thinking
// block, and an op-start per tool_use block (finalized later on its
// tool_result). A synthetic model emits a LogEntry only (spec §3.2, §5.4).
//
//nolint:unparam // error return is required by the record-type dispatch in mapRecord, where the sibling mapUser arm returns real errors; the uniform (evs, error) shape across all arms is intentional
func (m *fileMapper) mapAssistant(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	tsUs := m.recordTs(rec)
	msg := rec.Assistant
	if msg == nil {
		return nil, nil
	}

	if msg.Model == syntheticModel || msg.Model == "" {
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", "synthetic-assistant", rec)}, nil
	}

	m.ensureAssistantTurn()

	out := make([]canonical.Event, 0, 4)
	if ev, ok := m.assistantModelUpdate(advance, tsUs, msg.Model); ok {
		out = append(out, ev)
	}

	started, llmSeq := m.startAssistantLLM(advance, tsUs, msg)
	out = append(out, started)
	out = append(out, m.buildLLMFinalized(msg, advance(tsUs), m.turnSeq, llmSeq, tsUs))
	out = append(out, m.emitAssistantReasoningOps(msg.Content, advance, tsUs, llmSeq)...)
	out = append(out, m.emitAssistantToolUseOps(msg.Content, advance, tsUs)...)
	return out, nil
}

// mapSystem handles a `system` record. compact_boundary emits the synthetic
// compaction op; turn_duration finalizes the current turn; api_error and the
// rest become LogEntry rows (spec §5.4).
//
//nolint:unparam // error return is required by the record-type dispatch in mapRecord, where the sibling mapUser arm returns real errors; the uniform (evs, error) shape across all arms is intentional
func (m *fileMapper) mapSystem(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	tsUs := m.recordTs(rec)
	body := rec.System
	switch rec.Env.Subtype {
	case "compact_boundary":
		return m.mapCompaction(rec, advance, tsUs), nil
	case "turn_duration":
		// Finalize the just-completed turn.
		if m.turnSeq == 0 {
			return nil, nil
		}
		endUs := tsUs
		fin := canonical.TurnFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			Seq:             m.turnSeq,
			Status:          "completed",
			EndTs:           endUs,
		}
		return []canonical.Event{fin}, nil
	case "api_error":
		ev := m.logEntry(advance(tsUs), "ERR", "api_error", rec)
		if body != nil && len(body.APIError) > 0 {
			ev.Extras["error"] = body.APIError
		}
		return []canonical.Event{ev}, nil
	case "stop_hook_summary":
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "stop_hook_summary", rec)}, nil
	default:
		// informational, local_command, away_summary, bridge_status,
		// scheduled_task_fire, and any future subtype.
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", "system:"+rec.Env.Subtype, rec)}, nil
	}
}

// mapCompaction emits the first-class compaction op for a compact_boundary
// record (spec §9.2, acceptance #4): Ts=boundary ts, EndTs=Ts+durationMs*1000,
// BytesIn=preTokens, BytesOut=postTokens, Extras=compactMetadata. It does NOT
// touch the turn counter — the post-compaction summary user message must not
// open a new turn (handled in mapUser).
func (m *fileMapper) mapCompaction(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	cm := rec.System.Compact
	endUs := tsUs
	extras := compactionExtras(cm)
	if cm != nil {
		endUs = tsUs + cm.DurationMs*1000
	}
	// The compaction op is top-level within whatever turn is currently open
	// (or turn 0 if compaction precedes any turn). It does not consume an
	// op-seq slot in the turn's normal numbering — give it the next slot so
	// Seq stays unique, but do not reset opSeqInTurn.
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
	// Remember the compaction op's (turn,op) so the post-compaction summary
	// user message can scope its PayloadRef to an op that EXISTS (P1.1a):
	// payload_refs.op_id is NOT NULL REFERENCES ops(id), so a summary payload
	// at (0,0) would FK-roll-back the whole ingest batch.
	m.lastCompactionTurnSeq = m.turnSeq
	m.lastCompactionOpSeq = opSeq
	started := canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpCompaction,
		Name:            "compaction",
		Extras:          extras,
	}
	finalized := canonical.OpFinalizedEvent{
		EventBase:       advance(endUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             opSeq,
		Status:          "completed",
		EndTs:           endUs,
	}
	if cm != nil {
		finalized.BytesIn = cm.PreTokens
		finalized.BytesOut = cm.PostTokens
	}
	// Spec §5.4 / §9.2: emit BOTH the compaction op AND a LogEntry INF so the
	// UI can render the boundary on the timeline and in a compaction lane. The
	// log carries the full compactMetadata (the op already does too).
	logEv := m.logEntry(advance(tsUs), "INF", "compact_boundary", rec)
	for k, v := range extras {
		logEv.Extras[k] = v
	}
	return []canonical.Event{started, finalized, logEv}
}

// compactionExtras builds the op/log Extras carrying the FULL compactMetadata
// (spec §9.2, P2a): the scalar trigger/token/duration fields PLUS the
// preservedSegment and preservedMessages sub-objects, so a consumer can see
// exactly what context the compaction kept. Returns nil when the metadata is
// absent (a malformed boundary record).
func compactionExtras(cm *compactMetadata) map[string]any {
	if cm == nil {
		return nil
	}
	extras := map[string]any{
		"trigger":    cm.Trigger,
		"preTokens":  cm.PreTokens,
		"postTokens": cm.PostTokens,
		"durationMs": cm.DurationMs,
	}
	if len(cm.PreservedSegment) > 0 {
		extras["preservedSegment"] = cm.PreservedSegment
	}
	if len(cm.PreservedMessage) > 0 {
		extras["preservedMessages"] = cm.PreservedMessage
	}
	return extras
}

// mapPRLink surfaces a pr-link record as a session-extras update plus an INF
// log (it carries a timestamp). It accumulates every PR seen on the file and
// emits the FULL prLinks ARRAY each time (spec §3.9, §397, P2.7): the ingester
// overwrites the prLinks extras key wholesale (json_patch), so a singular
// per-PR object would lose all but the last. Replay-from-0 on resume re-emits
// the complete array, so it is last-wins on the whole array.
func (m *fileMapper) mapPRLink(rec record, advance func(int64) canonical.EventBase) []canonical.Event {
	tsUs := m.recordTs(rec)
	fields := decodeRawFields(rec.Raw)
	// A FRESH map per pr-link: m.prLinks only ever appends these never-mutated
	// maps, so the shallow copy(snapshot, m.prLinks) below cannot alias a map a
	// previously-emitted SessionUpdatedEvent still references.
	prLink := map[string]any{}
	for _, k := range []string{"prNumber", "prUrl", "prRepository"} {
		if v, ok := fields[k]; ok {
			prLink[k] = v
		}
	}
	out := []canonical.Event{
		m.logEntry(advance(tsUs), "INF", "pr-link", rec),
	}
	if len(prLink) > 0 {
		m.prLinks = append(m.prLinks, prLink)
		// Emit a copy of the accumulated array so a later mutation of m.prLinks
		// cannot alias an already-emitted event's slice.
		snapshot := make([]map[string]any, len(m.prLinks))
		copy(snapshot, m.prLinks)
		out = append(out, canonical.SessionUpdatedEvent{
			EventBase: advance(tsUs),
			NativeID:  m.nativeID,
			Extras:    map[string]any{"prLinks": snapshot},
		})
	}
	return out
}

// mapSnapshot converts a last-wins metadata-snapshot record into a
// SessionUpdatedEvent that the ingester applies as a partial UPDATE (spec
// §5.4, §11.5). Returns nil when the record carries nothing useful.
func (m *fileMapper) mapSnapshot(rec record, base canonical.EventBase) canonical.Event {
	fields := decodeRawFields(rec.Raw)
	ev := canonical.SessionUpdatedEvent{
		EventBase: base,
		NativeID:  m.nativeID,
		Extras:    map[string]any{},
	}
	m.applySnapshot(rec.Env.Type, fields, &ev)
	return snapshotUpdateOrNil(ev)
}

// childNativeID builds the synthetic subagent NativeID per spec §5.1:
// "<parentSessionId>:agent:<agentId>".
func childNativeID(parentSessionID, agentID string) string {
	return parentSessionID + ":agent:" + agentID
}

// agentFinalizeEvent builds the deferred OpFinalizedEvent for a parent Agent
// op whose child sidechain has ended (spec §8.1, P1b). parentNativeID is the
// parent session, ref locates the op, endUs is the child's end timestamp, and
// status is "completed" for a fully-read child. SourceSeq is 0 (observability
// only; this finalize is synthesized post-stream, outside the per-record
// packing).
func agentFinalizeEvent(sourceID, parentNativeID string, ref agentOpRef, endUs int64, status string) canonical.OpFinalizedEvent {
	return canonical.OpFinalizedEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        endUs,
		},
		SessionNativeID: parentNativeID,
		TurnSeq:         ref.turnSeq,
		Seq:             ref.opSeq,
		Status:          status,
		EndTs:           endUs,
	}
}
