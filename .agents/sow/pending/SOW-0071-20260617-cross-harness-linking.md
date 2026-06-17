# SOW-0071 — Cross-harness session linking (heuristic)

## Status

Status: open (follow-up to SOW-0070)

## Requirements

### Purpose

Link sessions across different harnesses when one harness spawns another via a shell tool (e.g. claude-code running `codex` via Bash, or opencode running `claude` via Bash). These links are NOT deterministic in the source data — the harnesses don't know about each other. This SOW detects heuristic links and surfaces them as "possibly related."

### Scope

- Detect heuristic links: same cwd, overlapping timestamps, one session's tool_use content mentioning another harness's name/CLI.
- Surface as soft links (not parent-child edges) in the session detail and topology.
- The operator can see "this claude-code session spawned a codex session via Bash" even though neither source records the link.

### Acceptance Criteria

1. Heuristic cross-harness links detected and surfaced. **Verification**: a claude-code session that ran `codex` via Bash shows a "possibly related" link to the codex session.
2. Soft links are visually distinct from deterministic parent-child edges. **Verification**: different color/style.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
