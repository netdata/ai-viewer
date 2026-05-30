package codex

import "encoding/json"

// This file holds the narrow JSON decoders that pull telemetry fields off an
// event_msg end-event's verbatim payload (exec_command_end, web_search_end,
// mcp_tool_call_end, patch_apply_end). They are pure functions with no mapper
// state, split from ops_enrich.go so the enrichment dispatch stays focused.

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
