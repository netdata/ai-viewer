package codex

import "github.com/netdata/ai-viewer/internal/canonical"

// enrichOp merges telemetry from an event_msg end-event onto the op matched by
// call_id, so the enrichment Extras reach the op's ops.extras_json (F4, spec rule
// #14 exec_command_end). The merge is ORDER-INDEPENDENT — real exec ordering is
// exec_command_end BEFORE function_call_output in ~68-85% of files and after it
// in the rest:
//   - op still OPEN (exec-first, the common case): merge the extras onto the
//     tracked op and stash any exec-derived terminal status; the op STAYS OPEN so
//     its *_output finalizes it (mapToolOutput re-emits an OpStarted carrying the
//     merged Extras at that point — OpFinalizedEvent has no Extras field, and the
//     writer upserts (turn,seq), so the re-emit is an idempotent UPDATE, not a
//     second op, satisfying rule #14 "do not emit a second op"). If no *_output
//     ever arrives, the turn-close dangling finalize re-emits the Extras.
//   - op already FINALIZED (output-first): look it up in finalizedOps and emit an
//     OpStarted carrying the merged Extras to UPDATE the existing row.
//   - op NOT locatable (start below a resume offset, or orphaned): a DBG log is
//     the only honest surface (no op row to attach to).
//
// extractor builds the Extras map from the raw payload (nil → no extras, e.g.
// image_generation_end which only marks completion). A blanked-output
// exec_command_end (Limited mode clears stdout/stderr) is NOT an error — the
// status stays the op's derived terminal status (spec rule #14).
func (m *fileMapper) enrichOp(rec record, advance func(int64) canonical.EventBase, tsUs int64, extractor func([]byte) map[string]any) []canonical.Event {
	p := rec.EventMsg
	var extras map[string]any
	if extractor != nil {
		extras = extractor(rec.Raw)
	}
	status, errClass := enrichStatus(rec.Raw)

	op, ok := m.openOps[p.CallID]
	if !ok {
		// The op may have already been finalized by its *_output before this
		// end-event (the ~15-32% output-first ordering): re-emit an OpStarted onto
		// the finalized op so the Extras still land (F4).
		return m.enrichFinalizedOp(p.CallID, advance, tsUs, p.Type, extras)
	}
	// Op still open (exec-first): stash extras + the exec-derived status on the op
	// and leave it open. Its *_output (or the turn-close dangling finalize) emits
	// the canonical OpFinalized AND re-emits an OpStarted carrying these Extras.
	mergeExtras(op, extras)
	if status != "" {
		op.enrichStatus = status
		op.enrichErrClass = errClass
	}
	return nil
}

// enrichFinalizedOp handles an end-event whose op was already finalized by its
// *_output (output-first ordering, ~15-32% of exec files) (F4). It re-emits an
// OpStarted carrying the enrichment Extras to UPDATE the existing op row
// (idempotent on (turn,seq) — NOT a second op). When the op cannot be located in
// finalizedOps (start below a resume offset, or orphaned), a DBG log is the only
// honest surface. The end-event's status is NOT re-applied here: the *_output
// already produced the canonical finalize, and the enrichment is supplementary.
func (m *fileMapper) enrichFinalizedOp(callID string, advance func(int64) canonical.EventBase, tsUs int64, evType string, extras map[string]any) []canonical.Event {
	fop, ok := m.finalizedOps[callID]
	if !ok {
		log := map[string]any{"call_id": callID}
		for k, v := range extras {
			log[k] = v
		}
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "enrich_"+evType, log)}
	}
	if len(extras) == 0 {
		return nil
	}
	return []canonical.Event{m.reemitOpStarted(fop, advance, tsUs, extras)}
}

// finalizeWithExtras emits the op's OpFinalized AND, when the op accumulated
// enrichment Extras, a re-emitted OpStarted that carries them onto (turn,seq)
// (F4, spec rule #14). The OpStarted is emitted FIRST so the op row exists with
// its Extras before the finalize updates its terminal status; both upsert the
// same (turn,seq) row. The op's kind/name/namespace are restated so the re-emit
// is faithful (the writer keeps the original start_ts via MIN).
func (m *fileMapper) finalizeWithExtras(op *openOp, advance func(int64) canonical.EventBase, tsUs int64, status, errClass string) []canonical.Event {
	out := make([]canonical.Event, 0, 2)
	if len(op.extras) > 0 {
		out = append(out, canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         op.turnSeq,
			Seq:             op.opSeq,
			ParentOpSeq:     -1,
			Kind:            op.kind,
			Name:            op.name,
			ToolNamespace:   op.namespace,
			Extras:          op.extras,
		})
	}
	out = append(out, canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         op.turnSeq,
		Seq:             op.opSeq,
		Status:          status,
		ErrorClass:      errClass,
		EndTs:           tsUs,
	})
	return out
}

// reemitOpStarted builds an idempotent OpStarted that carries enrichment Extras
// onto an already-finalized op's (turn,seq) row (F4). The writer's ON CONFLICT
// UPDATE merges the Extras and keeps the original start_ts (MIN) and status
// (the finalize already set it), so this only adds the late telemetry.
func (m *fileMapper) reemitOpStarted(fop finalizedOp, advance func(int64) canonical.EventBase, tsUs int64, extras map[string]any) canonical.Event {
	return canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         fop.turnSeq,
		Seq:             fop.opSeq,
		ParentOpSeq:     -1,
		Kind:            fop.kind,
		Name:            fop.name,
		ToolNamespace:   fop.namespace,
		Extras:          extras,
	}
}

// recordFinalizedOp records a now-finalized op so a LATE enrichment event can
// merge Extras onto it via reemitOpStarted (F4). The op's kind/name/namespace are
// preserved so the re-emit restates the op faithfully.
func (m *fileMapper) recordFinalizedOp(callID string, op *openOp) {
	if callID == "" {
		return
	}
	m.finalizedOps[callID] = finalizedOp{
		turnSeq:   op.turnSeq,
		opSeq:     op.opSeq,
		kind:      op.kind,
		name:      op.name,
		namespace: op.namespace,
	}
}

// enrichWebSearch handles event_msg.web_search_end (F7, spec rule #11). It pairs
// POSITIONALLY with the active turn's most-recent open web_search op
// (openWebSearch), because web_search_call carries no correlation key. It
// finalizes that op completed and re-emits an OpStarted carrying the query Extras
// (OpFinalized has no Extras field). When no web_search is open (the end is
// orphaned, or its call was below a resume offset), a DBG log keeps it visible.
func (m *fileMapper) enrichWebSearch(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	ws := m.openWebSearch
	if ws == nil {
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "web_search_end_no_call", nil)}
	}
	m.openWebSearch = nil
	extras := webSearchExtras(rec.Raw)
	op, ok := m.openOps[ws.syntheticCallID]
	if !ok {
		// The op was already finalized (e.g. at a prior turn close) — re-emit onto
		// its row via the positional ref.
		fop := finalizedOp{turnSeq: ws.turnSeq, opSeq: ws.opSeq, kind: canonical.OpTool, name: "web_search", namespace: "web"}
		if len(extras) == 0 {
			return nil
		}
		return []canonical.Event{m.reemitOpStarted(fop, advance, tsUs, extras)}
	}
	op.finalized = true
	mergeExtras(op, extras)
	delete(m.openOps, ws.syntheticCallID)
	m.recordFinalizedOp(ws.syntheticCallID, op)
	return m.finalizeWithExtras(op, advance, tsUs, "completed", "")
}

// enrichMcp handles event_msg.mcp_tool_call_end (spec rule #15). It re-stamps
// the matching op's ToolNamespace to "mcp:<server>" and Name to the invocation
// tool by emitting an OpStarted update (the ingester upserts on (turn,seq), so a
// second OpStarted with the corrected namespace/name overwrites the placeholder
// from the function_call), then finalizes the op with the result status. When no
// op matches, it surfaces a DBG log.
func (m *fileMapper) enrichMcp(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	server, tool := mcpInvocation(rec.Raw)
	op, ok := m.openOps[p.CallID]
	if !ok {
		extras := map[string]any{"call_id": p.CallID}
		if server != "" {
			extras["server"] = server
		}
		if tool != "" {
			extras["tool"] = tool
		}
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "enrich_mcp_tool_call_end", extras)}
	}
	name := op.name
	if tool != "" {
		name = tool
	}
	namespace := "custom"
	if server != "" {
		namespace = "mcp:" + server
	}
	op.name = name
	op.namespace = namespace
	status, errClass := mcpResultStatus(rec.Raw)
	op.finalized = true
	out := []canonical.Event{
		canonical.OpStartedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         op.turnSeq,
			Seq:             op.opSeq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpTool,
			Name:            name,
			ToolNamespace:   namespace,
		},
		canonical.OpFinalizedEvent{
			EventBase:       advance(tsUs),
			SessionNativeID: m.nativeID,
			TurnSeq:         op.turnSeq,
			Seq:             op.opSeq,
			Status:          status,
			ErrorClass:      errClass,
			EndTs:           tsUs,
		},
	}
	delete(m.openOps, p.CallID)
	m.recordFinalizedOp(p.CallID, op)
	return out
}

// enrichPatchApply handles event_msg.patch_apply_end (spec rule #16). It
// finalizes the matching apply_patch op with the success/status from the event.
// When no op matches, it surfaces a DBG log.
func (m *fileMapper) enrichPatchApply(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	op, ok := m.openOps[p.CallID]
	status, errClass := patchApplyStatus(rec.Raw)
	if !ok {
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "enrich_patch_apply_end", map[string]any{"call_id": p.CallID, "status": status})}
	}
	op.finalized = true
	fin := canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         op.turnSeq,
		Seq:             op.opSeq,
		Status:          status,
		ErrorClass:      errClass,
		EndTs:           tsUs,
	}
	delete(m.openOps, p.CallID)
	m.recordFinalizedOp(p.CallID, op)
	return []canonical.Event{fin}
}

// mergeExtras folds enrichment extras onto a tracked op so its eventual finalize
// (if not produced here) carries them. A nil op or nil extras is a no-op.
func mergeExtras(op *openOp, extras map[string]any) {
	if op == nil || len(extras) == 0 {
		return
	}
	if op.extras == nil {
		op.extras = map[string]any{}
	}
	for k, v := range extras {
		op.extras[k] = v
	}
}

// The narrow JSON decoders for the enrichment end-events (execCommandExtras,
// webSearchExtras, enrichStatus, mcpInvocation, mcpResultStatus,
// patchApplyStatus) live in ops_enrich_decode.go.
