package claude_code

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapUser handles a `user` record. Three shapes (spec §5.4):
//   - string content, non-meta, non-compact-summary → opens turn N+1.
//   - array content (tool_result blocks) → finalizes the matching tool ops.
//   - isMeta / isCompactSummary → LogEntry only, no turn.
func (m *fileMapper) mapUser(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	tsUs := m.recordTs(rec)
	out := make([]canonical.Event, 0, 2)

	if boolValue(rec.Env.IsMeta) {
		out = append(out, m.logEntry(advance(tsUs), "DBG", "meta-user", rec))
		return out, nil
	}
	if boolValue(rec.Env.IsCompactSummary) {
		// Post-compaction summary: does NOT open a new turn (spec §9.2).
		// Surface as INF so the UI can show it in a compaction lane.
		out = append(out, m.logEntry(advance(tsUs), "INF", "compaction-summary", rec))
		return out, nil
	}

	str, blocks, isString := classifyUserContent(rec.User)
	if isString {
		// Operator-typed prompt: open the next turn.
		m.turnSeq++
		m.opSeqInTurn = 0
		_ = str
		out = append(out, canonical.TurnStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			Seq:             m.turnSeq,
		})
		return out, nil
	}

	// Array content: finalize tool ops by tool_use_id.
	for i := range blocks {
		blk := blocks[i]
		if blk.Type != "tool_result" || blk.ToolUseID == "" {
			continue
		}
		open, ok := m.toolOps[blk.ToolUseID]
		if !ok {
			// tool_result with no matching open op (e.g. result for an op
			// emitted before the cursor offset on a tail resume). Skip
			// finalization; the op was already finalized or never seen.
			continue
		}
		status := "completed"
		errClass := ""
		if blk.IsError {
			status = "failed"
			errClass = "tool_error"
		}
		out = append(out, canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         open.turnSeq,
			Seq:             open.opSeq,
			Status:          status,
			ErrorClass:      errClass,
			EndTs:           tsUs,
		})
		delete(m.toolOps, blk.ToolUseID)
	}
	return out, nil
}

// mapAssistant handles an `assistant` record. A non-synthetic model emits an
// LLM op (started+finalized with usage), a nested reasoning op per thinking
// block, and an op-start per tool_use block (finalized later on its
// tool_result). A synthetic model emits a LogEntry only (spec §3.2, §5.4).
func (m *fileMapper) mapAssistant(rec record, advance func(int64) canonical.EventBase) ([]canonical.Event, error) {
	tsUs := m.recordTs(rec)
	msg := rec.Assistant
	if msg == nil {
		return nil, nil
	}

	if msg.Model == syntheticModel || msg.Model == "" {
		return []canonical.Event{m.logEntry(advance(tsUs), "INF", "synthetic-assistant", rec)}, nil
	}

	// Ensure a turn is open. claude-code's first assistant record can
	// precede any operator string prompt in odd flows (e.g. resumed mid
	// tool-cycle); open turn 1 defensively so ops attach to a real turn.
	if m.turnSeq == 0 {
		m.turnSeq = 1
		m.opSeqInTurn = 0
	}

	// Emit a SessionUpdatedEvent once with the model so sessions.model is set.
	out := make([]canonical.Event, 0, 4)
	if !m.modelSeen {
		out = append(out, canonical.SessionUpdatedEvent{
			EventBase: advance(tsUs),
			NativeID:  m.nativeID,
			Model:     msg.Model,
		})
		m.modelSeen = true
	}

	// LLM op (started+finalized). Seq is the next op in the turn.
	m.opSeqInTurn++
	llmSeq := m.opSeqInTurn
	out = append(out, canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         m.turnSeq,
		Seq:             llmSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpLLM,
		Name:            msg.Model,
		Model:           msg.Model,
		Provider:        provider,
		Extras:          assistantUsageExtras(msg.Usage),
	})
	out = append(out, m.buildLLMFinalized(msg, advance(tsUs), m.turnSeq, llmSeq, tsUs))

	// thinking blocks → nested reasoning ops under the LLM op.
	for i := range msg.Content {
		blk := msg.Content[i]
		if blk.Type != "thinking" {
			continue
		}
		m.opSeqInTurn++
		out = append(out,
			canonical.OpStartedEvent{
				EventBase:       advance(tsUs),
				SessionNativeID: m.nativeID,
				TurnSeq:         m.turnSeq,
				Seq:             m.opSeqInTurn,
				ParentOpSeq:     llmSeq,
				Kind:            canonical.OpReasoning,
				ReasoningKind:   "raw",
			},
			canonical.OpFinalizedEvent{
				EventBase:       advance(tsUs),
				SessionNativeID: m.nativeID,
				TurnSeq:         m.turnSeq,
				Seq:             m.opSeqInTurn,
				Status:          "completed",
				EndTs:           tsUs,
				BytesOut:        int64(len(blk.Thinking)),
			},
		)
	}

	// tool_use blocks → op-start (finalized later on tool_result). The Agent
	// tool is a child-session op (spec §4.4, §5.4).
	for i := range msg.Content {
		blk := msg.Content[i]
		if blk.Type != "tool_use" {
			continue
		}
		m.opSeqInTurn++
		opSeq := m.opSeqInTurn
		opName, namespace := splitToolName(blk.Name)
		started := canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         m.turnSeq,
			Seq:             opSeq,
			ParentOpSeq:     -1,
			Name:            opName,
			ToolNamespace:   namespace,
		}
		if blk.Name == "Agent" {
			started.Kind = canonical.OpSession
			if agentID, ok := m.toolUseToAgent[blk.ID]; ok && agentID != "" {
				started.ChildSessionNativeID = childNativeID(m.nativeID, agentID)
			}
			if desc := agentDescription(blk.Input); desc != "" {
				started.Name = desc
			}
		} else {
			started.Kind = canonical.OpTool
		}
		out = append(out, started)
		// Track for finalization on the matching tool_result. The Agent
		// op has no tool_result in the parent (its completion is implicit
		// in the subagent EOF); we still record it so a stray tool_result
		// (rare) can finalize it, but its absence is expected.
		if blk.ID != "" {
			m.toolOps[blk.ID] = openToolOp{turnSeq: m.turnSeq, opSeq: opSeq, name: opName}
		}
	}
	return out, nil
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
		// Effective input includes cache tokens (spec §5.6).
		ev.TokensIn = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		ev.TokensOut = u.OutputTokens
		ev.TokensCacheRead = u.CacheReadInputTokens
		ev.TokensCacheWrite = u.CacheCreationInputTokens
		ev.CtxUsed = ev.TokensIn + ev.TokensOut
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
		extras["serverToolUse"] = json.RawMessage(u.ServerToolUse)
	}
	return extras
}

// mapSystem handles a `system` record. compact_boundary emits the synthetic
// compaction op; turn_duration finalizes the current turn; api_error and the
// rest become LogEntry rows (spec §5.4).
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
			ev.Extras["error"] = json.RawMessage(body.APIError)
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
	var extras map[string]any
	if cm != nil {
		endUs = tsUs + cm.DurationMs*1000
		extras = map[string]any{
			"trigger":    cm.Trigger,
			"preTokens":  cm.PreTokens,
			"postTokens": cm.PostTokens,
			"durationMs": cm.DurationMs,
		}
	}
	// The compaction op is top-level within whatever turn is currently open
	// (or turn 0 if compaction precedes any turn). It does not consume an
	// op-seq slot in the turn's normal numbering — give it the next slot so
	// Seq stays unique, but do not reset opSeqInTurn.
	m.opSeqInTurn++
	opSeq := m.opSeqInTurn
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
	return []canonical.Event{started, finalized}
}

// mapPRLink surfaces a pr-link record as a session-extras update plus an INF
// log (it carries a timestamp). The prLink is appended to extras_json.prLinks.
func (m *fileMapper) mapPRLink(rec record, advance func(int64) canonical.EventBase) []canonical.Event {
	tsUs := m.recordTs(rec)
	fields := decodeRawFields(rec.Raw)
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
		out = append(out, canonical.SessionUpdatedEvent{
			EventBase: advance(tsUs),
			NativeID:  m.nativeID,
			Extras:    map[string]any{"prLink": prLink},
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
	switch rec.Env.Type {
	case recLastPrompt:
		if v, ok := stringField(fields, "lastPrompt"); ok {
			ev.Extras["lastPrompt"] = v
		}
	case recAITitle:
		if v, ok := stringField(fields, "aiTitle"); ok {
			ev.Extras["aiTitle"] = v
			// AgentName falls back to aiTitle when no custom title set yet.
			ev.AgentName = v
		}
	case recCustomTitle:
		if v, ok := stringField(fields, "customTitle"); ok {
			ev.Extras["customTitle"] = v
			ev.AgentName = v // custom title wins (spec §3.7).
		}
	case recPermissionMode:
		if v, ok := stringField(fields, "permissionMode"); ok {
			ev.Extras["permissionMode"] = v
		}
	case recBridgeSession:
		for _, k := range []string{"bridgeSessionId", "lastSequenceNum"} {
			if v, ok := fields[k]; ok {
				ev.Extras["bridge."+k] = v
			}
		}
	case recFileHistorySnapshot:
		// Record only that a snapshot exists; the per-file backup map can
		// be large and is not needed on the timeline (spec §3.11).
		ev.Extras["fileHistorySnapshot"] = true
	}
	if len(ev.Extras) == 0 && ev.AgentName == "" {
		return nil
	}
	return ev
}

// childNativeID builds the synthetic subagent NativeID per spec §5.1:
// "<parentSessionId>:agent:<agentId>".
func childNativeID(parentSessionID, agentID string) string {
	return parentSessionID + ":agent:" + agentID
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

// decodeRawFields decodes a record's raw bytes into a flat field map for
// snapshot extraction. Returns an empty map on failure (defensive).
func decodeRawFields(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// stringField returns the string value at key, or ("", false) when absent or
// not a string.
func stringField(fields map[string]any, key string) (string, bool) {
	v, ok := fields[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
