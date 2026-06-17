# SOW-0068 — Session list: primary/secondary distinction + improved filtering

## Status

Status: open

## Requirements

### Purpose

Make the Sessions list the effective entry point for exploring work. The operator needs to find sessions quickly, distinguish primary sessions (the ones they initiated) from secondary ones (sub-agents, tool-internal sessions, forks), and filter by any dimension.

### Background

Currently every session appears in a flat list. The operator sees ai-agent's `web-fetch`, `web-search`, `reddit` sub-agent sessions alongside their primary work sessions. The `group=root` default hides sub-agents, but the list still mixes different kinds of root sessions (real work vs test fixtures vs maintenance). The Sources column helps but doesn't fully solve the "what is this?" problem.

### Design goals

1. **Primary vs secondary distinction** — the list should clearly mark primary sessions (the user's direct interactions: claude-code CLI sessions, codex CLI sessions, ai-agent root sessions the user started) vs secondary (sub-agents spawned by other sessions, tool-internal sessions, maintenance/compaction sessions). Visual: badge or icon.
2. **Secondary session drill-in** — selecting a secondary session should offer to "view in context" (show the primary + all its secondary sessions as a tree). The current session-detail page already has the Topology tab, but the LIST should make the relationship discoverable.
3. **Smart default sort** — by default, show the most recent PRIMARY sessions first. Provide a toggle to show secondary sessions too.
4. **Better filters** — the current free-text sources filter should become a dropdown (from `/api/sources`). The status filter should include `failed` (with error_class as a sub-filter). Add a "has errors" quick filter.
5. **Session identity** — each row should show enough to identify it: agent name, model, source, cwd (for codex/opencode), a time-based label, and status badge with error_class on failures.

### Acceptance Criteria

1. Primary sessions are visually distinguished from secondary ones. **Verification**: UI check.
2. A "show secondary" toggle reveals/hides sub-agent sessions. **Verification**: toggle changes the list.
3. The Sources filter is a dropdown populated from `/api/sources`. **Verification**: dropdown shows the 5 sources.
4. Failed sessions show their error_class as a badge or tag. **Verification**: ai-agent failed sessions show `invalid_response`, `rate_limit`, etc.
5. Selecting a secondary session offers to navigate to its primary session's tree. **Verification**: link/navigate works.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
