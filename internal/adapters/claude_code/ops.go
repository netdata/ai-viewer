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
		// Surface as INF so the UI can show it in a compaction lane, plus a
		// PayloadRef pointing at the inline summary text (spec §5.4). The ref is
		// scoped to the preceding compaction op so it references an op that
		// EXISTS (P1.1a) — payload_refs.op_id is NOT NULL REFERENCES ops(id).
		out = append(out, m.logEntry(advance(tsUs), "INF", "compaction-summary", rec))
		ref, ok, perr := m.emitSummaryPayload(advance(tsUs), m.lastCompactionTurnSeq, m.lastCompactionOpSeq)
		if perr != nil {
			return nil, perr
		}
		if ok {
			out = append(out, ref)
		}
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

	// Array content: finalize tool ops by tool_use_id. A top-level
	// toolUseResult body becomes ONE PayloadRef for the record, attached to
	// the first matched tool op (the common case is a single tool_result per
	// record); payloadEmitted guards against emitting it more than once.
	payloadEmitted := false
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
		// A top-level toolUseResult body (the structured tool-result echo,
		// inline in the transcript) becomes a PayloadRef on the finalized tool
		// op (spec §5.4, P2b). One ref for the record, attached to the first
		// matched tool op (the common case is a single tool_result per record).
		if rec.HasToolUseResult && !payloadEmitted {
			ref, perr := m.emitToolResultPayload(advance(tsUs), open.turnSeq, open.opSeq)
			if perr != nil {
				return nil, perr
			}
			out = append(out, ref)
			payloadEmitted = true
		}
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
				childID := childNativeID(m.nativeID, agentID)
				started.ChildSessionNativeID = childID
				// Record the Agent op so it can be finalized when the child
				// sidechain reaches EOF (spec §8.1, P1b): the parent has no
				// tool_result for Agent, so without this the op stays running.
				if m.agentOps == nil {
					m.agentOps = map[string]agentOpRef{}
				}
				m.agentOps[childID] = agentOpRef{turnSeq: m.turnSeq, opSeq: opSeq}
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
		extras["preservedSegment"] = json.RawMessage(cm.PreservedSegment)
	}
	if len(cm.PreservedMessage) > 0 {
		extras["preservedMessages"] = json.RawMessage(cm.PreservedMessage)
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
	switch rec.Env.Type {
	case recLastPrompt:
		if v, ok := stringField(fields, "lastPrompt"); ok {
			ev.Extras["lastPrompt"] = v
		}
	case recAITitle:
		if v, ok := stringField(fields, "aiTitle"); ok {
			ev.Extras["aiTitle"] = v
			// AgentName falls back to aiTitle ONLY when no custom title has
			// been seen on this file. A custom-title (operator-chosen) wins
			// regardless of arrival order (spec §3.7, P3): a trailing ai-title
			// must not clobber it.
			if !m.customTitleSeen {
				ev.AgentName = v
			}
		}
	case recCustomTitle:
		if v, ok := stringField(fields, "customTitle"); ok {
			ev.Extras["customTitle"] = v
			ev.AgentName = v // custom title wins (spec §3.7).
			m.customTitleSeen = true
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
		// Store the actual tracked-file backup map (last non-empty wins),
		// not merely a boolean, so the UI can show which files the session
		// backed up (spec §3.11, P3). The map lives under snapshot.trackedFileBackups.
		if backups := fileHistoryBackups(fields); backups != nil {
			ev.Extras["fileHistory"] = backups
		}
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

// fileHistoryBackups extracts the snapshot.trackedFileBackups object from a
// file-history-snapshot record's decoded fields (spec §3.11, P3). Returns nil
// when absent or empty, so the caller stores only non-empty backup maps
// (last-non-empty wins) rather than a meaningless boolean.
func fileHistoryBackups(fields map[string]any) map[string]any {
	snap, ok := fields["snapshot"].(map[string]any)
	if !ok {
		return nil
	}
	backups, ok := snap["trackedFileBackups"].(map[string]any)
	if !ok || len(backups) == 0 {
		return nil
	}
	return backups
}
