package codex

import "encoding/json"

// This file holds the narrow JSON decoders that pull telemetry fields off an
// event_msg end-event's verbatim payload (exec_command_end, web_search_end,
// mcp_tool_call_end, patch_apply_end). They are pure functions with no mapper
// state, split from ops_enrich.go so the enrichment dispatch stays focused.

// execDuration is the Rust std::time::Duration wire shape codex serializes for
// exec_command_end.duration: ALWAYS the {secs,nanos} object (real corpus: 20000/
// 20000 exec_command_end lines, keys always nanos,secs — never a bare number or
// string). A typed decoder so the emitted extras never carry an untyped `any`
// (G3).
type execDuration struct {
	Secs  int64 `json:"secs"`
	Nanos int64 `json:"nanos"`
}

// millis normalizes the {secs,nanos} duration to integer milliseconds (spec rule
// #14 exec_duration_ms). Returns -1 when the field was absent (both zero is a
// legitimately sub-millisecond command, so 0 is a real value, not "missing").
func (d *execDuration) millis(present bool) int64 {
	if !present {
		return -1
	}
	return d.Secs*1000 + d.Nanos/1_000_000
}

// execCommandExtras extracts the exec_command_end telemetry merged into the op
// (spec rule #14): exit_code, duration (normalized to exec_duration_ms), cwd,
// source, and the truncated aggregated_output length (the body itself is blanked
// at the source in Limited mode — only aggregated_output survives, truncated to
// 10 KB).
func execCommandExtras(raw []byte) map[string]any {
	var env struct {
		Payload struct {
			ExitCode         *int64        `json:"exit_code"`
			Duration         *execDuration `json:"duration"`
			Cwd              string        `json:"cwd"`
			Source           string        `json:"source"`
			AggregatedOutput string        `json:"aggregated_output"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	extras := map[string]any{}
	if env.Payload.ExitCode != nil {
		extras["exec_exit_code"] = *env.Payload.ExitCode
	}
	if ms := env.Payload.Duration.millis(env.Payload.Duration != nil); ms >= 0 {
		extras["exec_duration_ms"] = ms
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

// webSearchExtras extracts event_msg.web_search_end query + action (spec rule
// #11). The end event's `action` is ALWAYS an object discriminated by `type`
// (real corpus: search | open_page | find_in_page | other); webSearchAction
// reduces it to a compact map carrying the type plus the variant's url / query /
// pattern (G4). Returns nil only when neither a query nor an action is present.
func webSearchExtras(raw []byte) map[string]any {
	var env struct {
		Payload struct {
			Query  string          `json:"query"`
			Action json.RawMessage `json:"action"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	extras := map[string]any{}
	if env.Payload.Query != "" {
		extras["query"] = trimPreview(env.Payload.Query, previewMax)
	}
	if action := webSearchAction(env.Payload.Action); action != nil {
		extras["action"] = action
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

// webSearchAction reduces web_search_end.action to a compact Extras map (G4, spec
// rule #11). The action is an object discriminated by `type`; only the
// type-relevant scalar fields are surfaced (url for open_page; query for search;
// pattern + optional url for find_in_page). The queries[] array is dropped (the
// scalar `query` already carries the primary term) so the Extras stays compact.
// Returns nil when the action is absent or carries no type.
func webSearchAction(raw json.RawMessage) map[string]any {
	body := jsonTrim(raw)
	if len(body) == 0 {
		return nil
	}
	var a struct {
		Type    string `json:"type"`
		URL     string `json:"url"`
		Query   string `json:"query"`
		Pattern string `json:"pattern"`
	}
	if json.Unmarshal(body, &a) != nil || a.Type == "" {
		return nil
	}
	out := map[string]any{"type": a.Type}
	if a.URL != "" {
		out["url"] = trimPreview(a.URL, previewMax)
	}
	if a.Query != "" {
		out["query"] = trimPreview(a.Query, previewMax)
	}
	if a.Pattern != "" {
		out["pattern"] = trimPreview(a.Pattern, previewMax)
	}
	return out
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

// patchApplyExtras extracts the patch_apply_end success/status merged onto the
// apply_patch op (spec rule #16, adapter-codex.md:361 "Merge success, status into
// the op's Extras") (G2). Returns nil when neither field is present.
func patchApplyExtras(raw []byte) map[string]any {
	var env struct {
		Payload struct {
			Success *bool  `json:"success"`
			Status  string `json:"status"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	extras := map[string]any{}
	if env.Payload.Success != nil {
		extras["patch_success"] = *env.Payload.Success
	}
	if env.Payload.Status != "" {
		extras["patch_status"] = env.Payload.Status
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}
