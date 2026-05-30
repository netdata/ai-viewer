package opencode

import "strings"

// This file holds the pure tool-part helpers the tool-op emitter (emitToolOp in
// mapper_ops.go) delegates to: the op start/terminal derivation, the byte
// accounting, the task→child-session extraction (AC#4), and the MCP namespace
// heuristic. They are pure functions of a decoded partData; split out of
// mapper_ops.go to keep each file ≤ ~400 lines (mirrors codex's ops_tools.go).

// toolStartUs returns the tool op's start timestamp (µs) from state.time.start,
// falling back to the part's time_created when the state has no start.
func toolStartUs(data partData, p partRow) int64 {
	if data.State != nil && data.State.Time.Start > 0 {
		return msToMicros(data.State.Time.Start)
	}
	return msToMicros(p.TimeCreatedMs)
}

// toolTerminal derives a tool op's CANONICAL terminal status, end timestamp,
// error message, and whether an output body exists, from
// state.status/time/error/output (adapter-opencode.md §"Tool calls and Models").
// The returned status is ALWAYS in the canonical op-status set
// (running|completed|failed|cancelled|truncated — canonical-events.md:196):
// opencode's "error" maps to canonical "failed" (SOW-0005 round-2 P1-C; the raw
// "error" string is NOT a canonical status and breaks status consumers), and an
// unknown future opencode status maps to "completed" once it carries an end
// (it finished) else "" (running, no finalize). A running/pending state has no
// end (endPtr nil → no finalize). failed carries state.error as the message.
func toolTerminal(data partData) (status string, endPtr *int64, errMsg string, hasOutput bool) {
	if data.State == nil {
		return "", nil, "", false
	}
	st := data.State
	switch st.Status {
	case "completed":
		return "completed", st.Time.End, "", st.Output != ""
	case "error":
		// opencode "error" → canonical "failed" (the only valid terminal-error op
		// status); the detail rides in errMsg → OpFinalizedEvent.ErrorMessage.
		return "failed", st.Time.End, st.Error, st.Output != "" || st.Error != ""
	case "running", "pending":
		// In-flight: OpStarted only, no finalize (adapter-opencode.md §"Edge
		// Cases" #4). A later poll observing the part now completed re-emits the
		// whole tree and finalizes it (chunk C). No canonical status is emitted
		// (endPtr nil suppresses the finalize), so the raw "running"/"pending"
		// string is never surfaced AS a canonical op status.
		return "", nil, "", false
	default:
		// Unknown future opencode status: keep the emitted status canonical. If it
		// carries an end it finished → "completed"; otherwise leave it running (no
		// finalize). Never surface the raw opencode string as a canonical status.
		if st.Time.End != nil {
			return "completed", st.Time.End, st.Error, st.Output != ""
		}
		return "", nil, "", false
	}
}

// toolBytesIn approximates op.bytes_in as the byte length of the tool's
// serialized input (adapter-opencode.md §"Tool calls and Models":
// len(JSON.stringify(state.input))). The input is already raw JSON on the
// decoded state, so its length is the serialized size. 0 when absent.
func toolBytesIn(data partData) int64 {
	if data.State == nil {
		return 0
	}
	body := jsonTrimBytes(data.State.Input)
	return int64(len(body))
}

// toolBytesOut approximates op.bytes_out as the byte length of the tool's output
// string (adapter-opencode.md §"Tool calls and Models": len(state.output)).
func toolBytesOut(data partData) int64 {
	if data.State == nil {
		return 0
	}
	return int64(len(data.State.Output))
}

// taskChildSessionID returns the spawned child session id for a tool='task'
// part that carries state.metadata.sessionId, else "" (AC#4; adapter-opencode.md
// §"Sub-Agent Linkage"). Only tool='task' qualifies — the session-op edge is the
// task tool's dispatch, not any tool with a metadata.sessionId. The second return
// reports whether the task metadata was PRESENT but malformed, so the caller can
// surface a structured WARN rather than silently dropping a sub-agent linkage
// (SOW-0005 P2.6).
func taskChildSessionID(data partData) (childID string, metaMalformed bool) {
	if data.Tool != "task" || data.State == nil {
		return "", false
	}
	return data.State.subAgentSessionIDChecked()
}

// toolNameNamespace derives the canonical op Name and ToolNamespace from an
// opencode tool name (adapter-opencode.md §"Tool calls and Models"). MCP tools
// are namespaced with an underscore convention (e.g. github_get_file_contents →
// namespace github, name get_file_contents); builtins (read/bash/grep/…) have no
// underscore and yield an empty namespace + the verbatim name. The split is on
// the FIRST underscore so a name like github_get_file_contents keeps the rest of
// the name intact.
func toolNameNamespace(tool string) (name, namespace string) {
	if i := strings.IndexByte(tool, '_'); i > 0 && i < len(tool)-1 {
		return tool[i+1:], tool[:i]
	}
	return tool, ""
}
