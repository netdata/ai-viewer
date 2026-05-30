package codex

import (
	"encoding/json"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// enrichOp merges telemetry from an event_msg end-event onto the op matched by
// call_id, emitting an OpFinalizedEvent that re-states the op's terminal status
// and carries the enrichment Extras (spec rule #14 exec_command_end, #11
// web_search_end). It does NOT emit a second op — the ingester reconciles this
// finalize with the op's existing (turn,seq) row (idempotent upsert). When no
// op matches the call_id (the start was below a resume offset, or the end is
// orphaned), it surfaces a DBG log so the enrichment is not silently lost.
//
// extractor builds the Extras map from the raw payload (nil → no extras, e.g.
// image_generation_end which only marks completion). A blanked-output
// exec_command_end (Limited mode clears stdout/stderr) is NOT an error — the
// status stays the op's derived terminal status (spec rule #14).
func (m *fileMapper) enrichOp(rec record, advance func(int64) canonical.EventBase, tsUs int64, extractor func([]byte) map[string]any) []canonical.Event {
	p := rec.EventMsg
	op, ok := m.openOps[p.CallID]
	if !ok {
		// The op may have already been finalized by its *_output before this
		// end-event; re-state with the enrichment so the Extras still land.
		return m.enrichFinalizedOrLog(rec, advance, tsUs, extractor)
	}
	var extras map[string]any
	if extractor != nil {
		extras = extractor(rec.Raw)
	}
	status, errClass := enrichStatus(rec.Raw)
	if status == "" {
		// No explicit status/exit_code on the end-event: leave the op's terminal
		// status to its *_output (or turn-close inference). Emit nothing here but
		// record the extras on the tracked op so its eventual finalize carries
		// them (the finalize path reads op.extras when present).
		mergeExtras(op, extras)
		return nil
	}
	op.finalized = true
	mergeExtras(op, extras)
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
	return withExtrasLog(m, advance, tsUs, fin, op.extras)
}

// enrichFinalizedOrLog handles an end-event whose op is no longer tracked (its
// *_output already finalized it, or its start was below a resume offset). It
// re-emits an OpFinalizedEvent ONLY when the end-event carries an explicit
// status AND the op can be located in a turn — otherwise it surfaces a DBG log
// so the enrichment is visible without inventing an op reference. Because a
// finalized op was deleted from openOps, this path cannot recover the (turn,seq)
// and therefore always logs (the *_output already produced the canonical
// finalize; the enrichment is supplementary telemetry).
func (m *fileMapper) enrichFinalizedOrLog(rec record, advance func(int64) canonical.EventBase, tsUs int64, extractor func([]byte) map[string]any) []canonical.Event {
	p := rec.EventMsg
	extras := map[string]any{"call_id": p.CallID}
	if extractor != nil {
		for k, v := range extractor(rec.Raw) {
			extras[k] = v
		}
	}
	return []canonical.Event{m.logEntry(advance(tsUs), "DBG", "enrich_"+p.Type, extras)}
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

// withExtrasLog appends a DBG LogEntry carrying the op's enrichment extras after
// its finalize, so exec/web telemetry is visible in the Logs tab even though the
// canonical OpFinalized carries no Extras field. Returns the finalize alone when
// there are no extras.
func withExtrasLog(m *fileMapper, advance func(int64) canonical.EventBase, tsUs int64, fin canonical.OpFinalizedEvent, extras map[string]any) []canonical.Event {
	out := []canonical.Event{fin}
	if len(extras) > 0 {
		out = append(out, m.logEntry(advance(tsUs), "DBG", "op_enrichment", extras))
	}
	return out
}

// execCommandExtras extracts the exec_command_end telemetry merged into the op
// (spec rule #14): exit_code, duration, cwd, source, and the truncated
// aggregated_output length (the body itself is blanked at the source in Limited
// mode — only aggregated_output survives, truncated to 10 KB).
func execCommandExtras(raw []byte) map[string]any {
	var env struct {
		Payload struct {
			ExitCode         *int64 `json:"exit_code"`
			Duration         any    `json:"duration"`
			Cwd              string `json:"cwd"`
			Source           string `json:"source"`
			AggregatedOutput string `json:"aggregated_output"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	extras := map[string]any{}
	if env.Payload.ExitCode != nil {
		extras["exec_exit_code"] = *env.Payload.ExitCode
	}
	if env.Payload.Cwd != "" {
		extras["exec_cwd"] = env.Payload.Cwd
	}
	if env.Payload.Source != "" {
		extras["exec_source"] = env.Payload.Source
	}
	if env.Payload.AggregatedOutput != "" {
		extras["exec_output_bytes"] = len(env.Payload.AggregatedOutput)
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

// webSearchExtras extracts event_msg.web_search_end query/action (spec rule #11).
func webSearchExtras(raw []byte) map[string]any {
	var env struct {
		Payload struct {
			Query string `json:"query"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	if env.Payload.Query == "" {
		return nil
	}
	return map[string]any{"query": trimPreview(env.Payload.Query, previewMax)}
}

// enrichStatus derives a terminal status/ErrorClass from an end-event carrying
// an exit_code (spec rule #14). exit_code 0 → completed; non-zero → failed
// (command_failed). A blanked output is NOT an error (spec rule #14). Returns
// ("", "") when the event carries no exit_code (status left to the *_output).
func enrichStatus(raw []byte) (status, errClass string) {
	var env struct {
		Payload struct {
			ExitCode *int64 `json:"exit_code"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return "", ""
	}
	if env.Payload.ExitCode == nil {
		return "", ""
	}
	if *env.Payload.ExitCode == 0 {
		return "completed", ""
	}
	return "failed", "command_failed"
}

// mcpInvocation extracts mcp_tool_call_end.invocation.{server,tool} (spec rule
// #15). Returns ("","") when absent.
func mcpInvocation(raw []byte) (server, tool string) {
	var env struct {
		Payload struct {
			Invocation struct {
				Server string `json:"server"`
				Tool   string `json:"tool"`
			} `json:"invocation"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return "", ""
	}
	return env.Payload.Invocation.Server, env.Payload.Invocation.Tool
}

// mcpResultStatus derives status from mcp_tool_call_end.result, a
// Result<CallToolResult, String> serialized as {"Ok":...} or {"Err":"..."} (spec
// rule #15, protocol.rs:2191-2228). An Err, or a CallToolResult with
// is_error=true, is failed; anything else completed.
func mcpResultStatus(raw []byte) (status, errClass string) {
	var env struct {
		Payload struct {
			Result json.RawMessage `json:"result"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return "completed", ""
	}
	body := jsonTrim(env.Payload.Result)
	if len(body) == 0 {
		return "completed", ""
	}
	var res struct {
		Err json.RawMessage `json:"Err"`
		Ok  struct {
			IsError bool `json:"is_error"`
		} `json:"Ok"`
	}
	if json.Unmarshal(body, &res) != nil {
		return "completed", ""
	}
	if len(jsonTrim(res.Err)) > 0 || res.Ok.IsError {
		return "failed", "tool_error"
	}
	return "completed", ""
}

// patchApplyStatus derives status from patch_apply_end.success/status (spec rule
// #16). success=false → failed; an explicit status string maps directly. Default
// completed.
func patchApplyStatus(raw []byte) (status, errClass string) {
	var env struct {
		Payload struct {
			Success *bool  `json:"success"`
			Status  string `json:"status"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return "completed", ""
	}
	if env.Payload.Success != nil && !*env.Payload.Success {
		return "failed", "patch_failed"
	}
	switch env.Payload.Status {
	case "failed", "error":
		return "failed", "patch_failed"
	}
	return "completed", ""
}
