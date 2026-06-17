# SOW-0070 — Session detail: trace drill-down + payload integration

## Status

Status: open (follow-up to SOW-0069)

## Requirements

### Purpose

Make the Trace tab the definitive drill-down for understanding what happened inside a session (and its sub-sessions) at the op level: every LLM prompt, every tool execution, every error, every payload — navigable from high level to fine detail.

### Scope

- Trace tab shows ops from ALL sessions in the tree (color-coded by sub-agent)
- LLM ops show payload preview inline (SOW-0033 endpoint, already implemented)
- Tool ops show tool request/response
- Error ops show error_class + error_message prominently
- The operator can drill from the high-level timeline → a specific span → its ops → its payload content

### Acceptance Criteria

1. The Trace tab shows ops from all sessions in the tree, with a visual indicator (color/icon) for which sub-agent each op belongs to. **Verification**: multi-session tree shows mixed-color ops.
2. Clicking an LLM op's "Preview" shows the first 4KB of the request/response payload inline. **Verification**: preview renders content.
3. Error ops are visually highlighted (red border/icon) and show error_class + error_message. **Verification**: failed ops are distinguishable.
4. The operator can filter the trace by op kind (LLM / tool / session / reasoning), by status (failed / completed), or by sub-agent. **Verification**: filter narrows the trace.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
