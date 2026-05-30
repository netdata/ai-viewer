package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// mapToolCall handles function_call / custom_tool_call / local_shell_call /
// tool_search_call (spec rule #9, #10, #13). It emits an OpStarted (Kind=tool)
// with the namespace heuristic and tracks the op by call_id so the matching
// *_output finalizes it. The arguments string becomes a tool_request PayloadRef.
// The op is finalized later by mapToolOutput (or at turn close as dangling —
// spec edge #9).
func (m *fileMapper) mapToolCall(p *responseItemPayload, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 2)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	name, namespace := toolNameNamespace(p)
	extras := map[string]any{}
	if p.CallID != "" {
		extras["call_id"] = p.CallID
	}
	out = append(out, canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpTool,
		Name:            name,
		ToolNamespace:   namespace,
		Extras:          extras,
	})
	// Arguments string → tool_request PayloadRef (spec rule #9). Only emit when
	// the op has a body to point at.
	if bodyBytes > 0 {
		out = append(out, m.payloadRef(advance(tsUs), turnSeq, opSeq, "tool_request", "json", bodyBytes))
	}
	m.trackOp(p.CallID, m.activeTurnID, turnSeq, opSeq, canonical.OpTool, name, namespace)
	return out
}

// mapToolOutput handles function_call_output / custom_tool_call_output /
// local_shell_call_output / tool_search_output (spec rule #9). It finalizes the
// op matched by call_id with a status derived from the output (failed when the
// output looks like a sandbox/error string — spec edge #5), and attaches the
// output body as a tool_response PayloadRef. An output with no matching call is a
// SourceError-class event surfaced as a WRN LogEntry (spec edge #10 — the
// scanner does not see it; the mapper has no SourceError channel, so a WRN log
// keeps it visible without dropping silently).
func (m *fileMapper) mapToolOutput(p *responseItemPayload, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	op, ok := m.openOps[p.CallID]
	if !ok || op.finalized {
		// The op may already exist and be finalized because its enrichment
		// end-event (exec_command_end) was the close signal in a rare path, OR the
		// output is genuinely orphaned. If we can locate the op (finalizedOps),
		// attach the tool_response PayloadRef to it rather than warning (spec rule
		// #9 — the body still belongs to the op). Only a truly unmatched output
		// surfaces a WRN (spec edge #10).
		if fop, found := m.finalizedOps[p.CallID]; found && bodyBytes > 0 {
			return []canonical.Event{m.payloadRef(advance(tsUs), fop.turnSeq, fop.opSeq, "tool_response", "json", bodyBytes)}
		}
		return []canonical.Event{m.logEntry(advance(tsUs), "WRN", "tool_output_unmatched", map[string]any{"call_id": p.CallID})}
	}
	op.finalized = true
	status, errClass := outputStatus(p.Output)
	// A prior exec_command_end (exec-first ~68-85% ordering) may have stashed an
	// authoritative exit_code-derived status; it WINS over a benign-looking output
	// string (a non-zero exit with terse stdout must finalize failed) (F4).
	if op.enrichStatus != "" {
		status, errClass = op.enrichStatus, op.enrichErrClass
	}
	out := make([]canonical.Event, 0, 3)
	// If a prior enrichment (exec_command_end BEFORE function_call_output — the
	// ~68-85% exec ordering) stashed Extras on the op, re-emit an OpStarted
	// carrying them so they reach ops.extras_json (F4, spec rule #14). The writer
	// upserts (turn,seq), so this is an idempotent UPDATE, not a second op.
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
	if bodyBytes > 0 {
		out = append(out, m.payloadRef(advance(tsUs), op.turnSeq, op.opSeq, "tool_response", "json", bodyBytes))
	}
	delete(m.openOps, p.CallID)
	// Record the finalized op (with the status it was finalized with) so a LATER
	// exec_command_end (output-first ~15-32% ordering) can merge its Extras via an
	// OpStarted re-emit (F4) AND only emit a correcting OpFinalized when its
	// authoritative status differs from this one (H1b — no spurious re-finalize).
	m.recordFinalizedOp(p.CallID, op, status, errClass)
	return out
}

// mapWebSearchCall handles response_item.web_search_call (spec rule #11, F7/G4).
// It emits a tool op (Name=web_search, namespace=web). web_search_call carries
// NEITHER id NOR call_id, so the op is NOT tracked by call_id; instead it is
// appended to the FIFO queue of open web_search ops (openWebSearch) for POSITIONAL
// pairing with a later event_msg.web_search_end (which DOES carry a call_id, but
// for a DIFFERENT correlation space). Each web_search_end finalizes the OLDEST
// queued op, so interleaved searches pair in order. If no end arrives the op
// finalizes at turn close as a dangling op (it is tracked under a synthetic
// per-op call_id so finalizeDanglingOps closes it).
func (m *fileMapper) mapWebSearchCall(p *responseItemPayload, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 2)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	out = append(out, canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpTool,
		Name:            "web_search",
		ToolNamespace:   "web",
	})
	if bodyBytes > 0 {
		out = append(out, m.payloadRef(advance(tsUs), turnSeq, opSeq, "tool_request", "json", bodyBytes))
	}
	// Track under a synthetic call_id so a turn-close dangling-finalize closes it
	// if no web_search_end arrives; the synthetic id never collides with a real
	// call_id (the "ws#" prefix is not a codex call_id form).
	synthetic := fmt.Sprintf("ws#%d:%d", turnSeq, opSeq)
	m.trackOp(synthetic, m.activeTurnID, turnSeq, opSeq, canonical.OpTool, "web_search", "web")
	m.openWebSearch = append(m.openWebSearch, &openWebSearchRef{turnID: m.activeTurnID, turnSeq: turnSeq, opSeq: opSeq, syntheticCallID: synthetic})
	return out
}

// mapImageGenCall handles response_item.image_generation_call (spec rule #12):
// a tool op Name=image_generation, namespace=media, tracked by call_id for the
// event_msg.image_generation_end enrichment.
func (m *fileMapper) mapImageGenCall(p *responseItemPayload, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	// image_generation_call uses `id`, not `call_id`; the typed payload keeps
	// only call_id, so fall back to call_id and (when empty) leave it untracked —
	// the end event then enriches by the same empty key path and the op finalizes
	// at turn close.
	return m.emitSingleToolOp(p.CallID, "image_generation", "media", advance, tsUs, bodyBytes)
}

// emitSingleToolOp emits a tool OpStarted tracked by callID (for a later
// enrichment end-event) plus an optional tool_request PayloadRef. Shared by
// web_search and image_generation, which both pair a response_item start with an
// event_msg end (spec rule #11, #12).
func (m *fileMapper) emitSingleToolOp(callID, name, namespace string, advance func(int64) canonical.EventBase, tsUs, bodyBytes int64) []canonical.Event {
	ts := m.ensureTurn(tsUs)
	out := make([]canonical.Event, 0, 2)
	if ev := m.emitTurnStarted(ts, advance(tsUs)); ev != nil {
		out = append(out, ev)
	}
	turnSeq, opSeq := m.nextOp(ts)
	out = append(out, canonical.OpStartedEvent{
		EventBase:       advance(tsUs),
		SessionNativeID: m.nativeID,
		TurnSeq:         turnSeq,
		Seq:             opSeq,
		ParentOpSeq:     -1,
		Kind:            canonical.OpTool,
		Name:            name,
		ToolNamespace:   namespace,
	})
	if bodyBytes > 0 {
		out = append(out, m.payloadRef(advance(tsUs), turnSeq, opSeq, "tool_request", "json", bodyBytes))
	}
	m.trackOp(callID, m.activeTurnID, turnSeq, opSeq, canonical.OpTool, name, namespace)
	return out
}

// toolNameNamespace derives the canonical op Name and ToolNamespace from a tool
// call payload using the codex namespace heuristic (spec rule #9). Codex tools
// are not pre-namespaced on disk; the name pattern selects the namespace. A
// custom_tool_call / local_shell_call carries its own implied namespace.
func toolNameNamespace(p *responseItemPayload) (name, namespace string) {
	name = p.Name
	switch p.Type {
	case "custom_tool_call":
		return name, "custom"
	case "local_shell_call":
		// Legacy .json shell op (spec rule #13).
		if name == "" {
			name = "shell"
		}
		return name, "shell"
	case "tool_search_call":
		if name == "" {
			name = "tool_search"
		}
		return name, "custom"
	}
	return name, namespaceForName(name)
}

// namespaceForName maps a function_call tool name to a namespace (spec rule #9
// heuristic). mcp routing is resolved later from event_msg.mcp_tool_call_end
// (ops_event.go sets tool_namespace="mcp:<server>" on the matching op).
func namespaceForName(name string) string {
	switch {
	case name == "shell" || name == "shell_command" || strings.HasPrefix(name, "exec"):
		return "shell"
	case name == "apply_patch":
		return "fs"
	case name == "read" || name == "write" || name == "edit" || name == "list_dir":
		return "fs"
	case name == "view_image":
		return "fs"
	default:
		return "custom"
	}
}

// outputStatus derives an op's terminal status from a tool output body (spec
// rule #9, edge #5). A success output yields "completed"; an output whose string
// content matches a sandbox-denial or error signal yields "failed" with the
// matching ErrorClass. The output is either a bare string or {output} /
// {content} — all reduced to a lower-cased scan string.
func outputStatus(raw json.RawMessage) (status, errClass string) {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return "completed", ""
	}
	text := outputText(body)
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "denied by sandbox") || strings.Contains(low, "operation not permitted") || strings.Contains(low, "sandbox deny"):
		return "failed", "sandbox_denied"
	case strings.Contains(low, "\"error\"") || strings.HasPrefix(low, "error:") || strings.Contains(low, "exit code 1") || strings.Contains(low, "command failed"):
		return "failed", "tool_error"
	default:
		return "completed", ""
	}
}

// outputText reduces a tool output body to a scan string: a bare JSON string
// returns its value; an object returns its `output`/`content` field (string or
// re-serialized); anything else returns the raw bytes verbatim. Used only for
// the heuristic status scan — never surfaced as content.
func outputText(body json.RawMessage) string {
	var s string
	if json.Unmarshal(body, &s) == nil {
		return s
	}
	var obj struct {
		Output  json.RawMessage `json:"output"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(body, &obj) == nil {
		if v := scalarOrJSON(obj.Output); v != "" {
			return v
		}
		if v := scalarOrJSON(obj.Content); v != "" {
			return v
		}
	}
	return string(body)
}

// scalarOrJSON returns a JSON value's string form if it is a string, else its
// raw JSON, else "" when absent.
func scalarOrJSON(raw json.RawMessage) string {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(body, &s) == nil {
		return s
	}
	return string(body)
}
