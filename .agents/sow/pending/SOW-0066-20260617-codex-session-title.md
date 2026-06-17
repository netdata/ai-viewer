# SOW-0066 — codex session title/identification improvement

## Status

Status: open

## Requirements

### Purpose

Codex sessions show `codex:codex_exec` / `codex:codex_cli_rs` / `codex:Codex Desktop` / `codex:codex-tui` as the agent_name — internal process names that are meaningless to the operator. The operator cannot identify which session is which work session. Derive a human-readable label from the session's content (first user prompt, project name, or cwd).

### Acceptance Criteria

1. Codex sessions show a recognizable label (first user prompt truncated, or cwd basename, or session_meta field) instead of the raw process name. **Verification**: a codex session in the Sessions list shows something the operator recognizes.
2. The raw process name is preserved in extras_json for diagnostics. **Verification**: extras shows the original agent_name.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
