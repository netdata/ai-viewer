# SOW-0069 — Session detail: cross-session tree navigation + unified timeline

## Status

Status: open

## Requirements

### Purpose

When the operator selects a primary session, the session detail should show the COMPLETE execution tree — the primary session plus all its sub-agents, forks, and parallel branches — in a way that makes the sequence, parallelism, flow, errors, duration, and cost/tokens understandable at both the high level and down to each individual LLM prompt, tool execution, and tool response.

### Background

Currently the session detail has 6 tabs (Overview, Trace, Topology, Timeline, Logs, Raw Data) that work independently. The Topology tab shows the actor graph but not the temporal flow. The Timeline tab shows lanes per session but only within the session tree. The Trace tab shows ops within one session but not across sub-agent boundaries.

The operator's mental model: "I started a session → it spawned 3 sub-agents → one of them called a tool that spawned another sub-agent → the tool timed out → the sub-agent retried → eventually succeeded but cost $X." They need to see this as a connected story, not 6 disconnected tabs.

### Design goals (MILESTONE-SCOPED — pick ONE per SOW)

**SOW-0069 (this one): Cross-session tree navigation + unified timeline**
- When viewing a primary session, the Overview should show the complete sub-session tree (parent → children → grandchildren) with cost/tokens/duration/status at each level. Clicking a child navigates to its detail.
- The Timeline should merge ALL sessions in the tree into a unified Gantt view — parallel lanes for parallel sub-agents, with the temporal relationship visible (which sub-agent was running while another waited).
- The Topology tab should overlay cost/duration onto the edges so the operator sees "where the money went" in the call graph.

**SOW-0070 (follow-up): Trace drill-down + payload integration**
- The Trace tab should show ops from ALL sessions in the tree (not just the root), color-coded by which sub-agent executed them.
- Clicking an LLM op should show the payload preview inline (the SOW-0033 endpoint, now implemented).
- Clicking a tool op should show the tool request/response.
- Error ops should show the error_class + error_message prominently.

**SOW-0071 (follow-up): Cross-harness session linking**
- claude-code/codex/opencode sessions can spawn OTHER harness sessions via bash (e.g. `claude` CLI spawning `codex` CLI). These are NOT deterministically linked today (no parent-child relationship in the source data).
- Detect heuristic links: same cwd, overlapping timestamps, one session's tool_use mentioning another harness's name.
- Surface as "possibly related" links, not deterministic edges.

### Acceptance Criteria (SOW-0069)

1. The Overview tab shows the full sub-session tree with per-node cost/tokens/duration/status. **Verification**: a multi-level ai-agent session shows the tree.
2. The Timeline shows ALL sessions in the tree as parallel lanes, time-aligned. **Verification**: parallel sub-agents appear as overlapping lanes.
3. The Topology shows cost/duration on edges (or at least on nodes) so the operator can see "where the money went." **Verification**: edge labels or tooltips show cost.
4. Navigation between a parent and child session is seamless (click → navigate → back). **Verification**: navigate parent→child→back works.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
