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
		// the finalized op so the Extras land AND, when the exec carries an explicit
		// exit_code, a CORRECTING OpFinalized so a non-zero exit overrides the
		// output-derived status (G1, spec rule #5/#14 — exit_code is authoritative
		// in BOTH orders).
		return m.enrichFinalizedOp(p.CallID, advance, tsUs, p.Type, extras, status, errClass)
	}
	// Op still open (exec-first): stash extras + the exec-derived status on the op
	// and leave it open. Its *_output (or the turn-close dangling finalize) emits
	// the canonical OpFinalized AND re-emits an OpStarted carrying these Extras. The
	// exec status WINS over the output-string heuristic when exec carries an
	// explicit exit_code (spec rule #5/#14).
	mergeExtras(op, extras)
	if status != "" {
		op.enrichStatus = status
		op.enrichErrClass = errClass
	}
	return nil
}

// enrichFinalizedOp handles an end-event whose op was already finalized by its
// *_output (output-first ordering, ~15-32% of exec files) (F4/G1). It re-emits an
// OpStarted carrying the enrichment Extras to UPDATE the existing op row
// (idempotent on (turn,seq) — NOT a second op) AND, when the end-event carries an
// authoritative terminal status (an exec exit_code — spec rule #5/#14), a
// CORRECTING OpFinalized so a failed exec that was provisionally finalized
// completed by its output string is upserted to failed/command_failed (G1). When
// the op cannot be located in finalizedOps (start below a resume offset, or
// orphaned), a DBG log is the only honest surface.
func (m *fileMapper) enrichFinalizedOp(callID string, advance func(int64) canonical.EventBase, tsUs int64, evType string, extras map[string]any, status, errClass string) []canonical.Event {
	fop, ok := m.finalizedOps[callID]
	if !ok {
		log := map[string]any{"call_id": callID}
		for k, v := range extras {
			log[k] = v
		}
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "enrich_"+evType, log)}
	}
	out := make([]canonical.Event, 0, 2)
	if len(extras) > 0 {
		out = append(out, m.reemitOpStarted(fop, advance, tsUs, extras))
	}
	// An explicit exit_code-derived status corrects the op's terminal status in the
	// output-first ordering: the *_output already finalized it (often completed off a
	// benign-looking output), but a non-zero exit_code is authoritative (G1, spec
	// rule #5/#14). The writer upserts on (turn,seq), so this overwrites the status.
	//
	// Emit the correcting OpFinalized ONLY when the enrichment-derived status differs
	// from the one the op was already finalized with (H1b). An exec exit 0 on an
	// already-`completed` op carries no correction, so re-emitting OpFinalized(completed)
	// would be spurious — and the catalog rollups (which contribute each finalize's
	// failure/token/duration totals) would double-count it. A genuine change
	// (completed → failed on a non-zero exit) still corrects exactly once.
	if status != "" && (status != fop.status || errClass != fop.errClass) {
		out = append(out, m.correctFinalizedOp(fop, advance, tsUs, status, errClass))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// correctFinalizedOp emits an OpFinalized that re-applies an authoritative
// terminal status onto an already-finalized op's (turn,seq) row (G1). Used when an
// exec_command_end exit_code (output-first ordering) must override the status the
// op's *_output provisionally set. The writer's ON CONFLICT upsert overwrites the
// status/errClass on the existing row; EndTs is the enrichment event's timestamp.
func (m *fileMapper) correctFinalizedOp(fop finalizedOp, advance func(int64) canonical.EventBase, tsUs int64, status, errClass string) canonical.Event {
	return canonical.OpFinalizedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         fop.turnSeq,
		Seq:             fop.opSeq,
		Status:          status,
		ErrorClass:      errClass,
		EndTs:           tsUs,
	}
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
// preserved so the re-emit restates the op faithfully. status/errClass are the
// TERMINAL status the op was finalized with, so an output-first enrichment only
// emits a correcting OpFinalized when its authoritative status actually differs
// from this one (H1b — no spurious re-finalize on an unchanged status).
func (m *fileMapper) recordFinalizedOp(callID string, op *openOp, status, errClass string) {
	if callID == "" {
		return
	}
	m.finalizedOps[callID] = finalizedOp{
		turnSeq:   op.turnSeq,
		opSeq:     op.opSeq,
		kind:      op.kind,
		name:      op.name,
		namespace: op.namespace,
		status:    status,
		errClass:  errClass,
	}
}

// enrichWebSearch handles event_msg.web_search_end (F7/G4, spec rule #11). It
// pairs POSITIONALLY with the OLDEST open web_search op (the FRONT of the
// openWebSearch FIFO queue), because web_search_call carries no correlation key;
// FIFO order means interleaved searches pair in the order they opened. It
// finalizes that op completed and re-emits an OpStarted carrying the query +
// action Extras (OpFinalized has no Extras field). When no web_search is open
// (the end is orphaned, or its call was below a resume offset), a DBG log keeps it
// visible.
func (m *fileMapper) enrichWebSearch(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	ws := m.dequeueWebSearch()
	if ws == nil {
		return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "web_search_end_no_call", nil)}
	}
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
	m.recordFinalizedOp(ws.syntheticCallID, op, "completed", "")
	return m.finalizeWithExtras(op, advance, tsUs, "completed", "")
}

// dequeueWebSearch pops the OLDEST open web_search op (front of the FIFO queue)
// for positional pairing with a web_search_end (G4). Returns nil when the queue is
// empty (an orphaned end). Skips refs whose op already finalized at a turn close
// (its synthetic call_id is gone from openOps) so a stale front entry never
// shadows a still-open later search.
func (m *fileMapper) dequeueWebSearch() *openWebSearchRef {
	for len(m.openWebSearch) > 0 {
		ws := m.openWebSearch[0]
		m.openWebSearch = m.openWebSearch[1:]
		if _, stillOpen := m.openOps[ws.syntheticCallID]; stillOpen {
			return ws
		}
		// The op was finalized at a prior turn close; its query/action can no longer
		// be paired meaningfully — keep scanning for a still-open search. (Pairing a
		// closed op would just re-stamp Extras; the FIFO contract is to pair the
		// oldest STILL-OPEN search, so we drop the closed ref and continue.)
	}
	return nil
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
	m.recordFinalizedOp(p.CallID, op, status, errClass)
	return out
}

// enrichPatchApply handles event_msg.patch_apply_end (spec rule #16,
// adapter-codex.md:361). It is ORDER-INDEPENDENT (G2), mirroring the exec fix:
//   - op still OPEN (the apply_patch function_call_output has not arrived):
//     finalize the op with the success/status-derived terminal status and merge
//     {patch_success, patch_status} into its Extras via an OpStarted re-emit.
//   - op already FINALIZED (output-first): re-emit an OpStarted carrying the
//     extras AND a correcting OpFinalized so success=false upserts the op to
//     failed/patch_failed (spec rule #16 "Set Op Status accordingly").
//   - op NOT locatable: a DBG log is the only honest surface.
func (m *fileMapper) enrichPatchApply(rec record, advance func(int64) canonical.EventBase, tsUs int64) []canonical.Event {
	p := rec.EventMsg
	status, errClass := patchApplyStatus(rec.Raw)
	extras := patchApplyExtras(rec.Raw)
	op, ok := m.openOps[p.CallID]
	if !ok {
		// Output-first ordering: the op was already finalized by its
		// function_call_output — merge the extras and correct the status on its row.
		return m.enrichFinalizedOp(p.CallID, advance, tsUs, p.Type, extras, status, errClass)
	}
	op.finalized = true
	mergeExtras(op, extras)
	delete(m.openOps, p.CallID)
	m.recordFinalizedOp(p.CallID, op, status, errClass)
	// finalizeWithExtras emits the OpStarted (carrying patch_success/patch_status)
	// followed by the OpFinalized with the success-derived status.
	return m.finalizeWithExtras(op, advance, tsUs, status, errClass)
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
