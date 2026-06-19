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

## Status

Status: completed (moving to done/). ACs 2, 3, 4 already met; AC1 (recursive
child-sessions tree) closed in this pass. 6-reviewer loop converged round 3:
6/6 PRODUCTION GRADE (avg ~9.75).

## Pre-Implementation Gate

### Problem / root-cause model

`loadChildSessions` (`internal/presenter/session_detail.go:217`) runs
`WHERE parent_session_id = ?` — DIRECT children only. The Overview renders that
flat list. AC1 wants "the complete sub-session tree (parent → children →
grandchildren)". Grandchildren are invisible today. Root cause: no recursive
fetch and no nesting.

### Evidence reviewed (self-review, file:line)

- AC1 — GAP. `loadChildSessions` returns one level (`session_detail.go:221`).
  `childSummary` has no `children` field (struct at :91). Overview maps
  `detail.child_sessions` flat (`OverviewTab.tsx:90`).
- AC2 — DONE. `fetchTimeline` = "the whole session tree resolved"
  (`api/sessions.ts:169`); TimelineTab lays out all lanes.
- AC3 — DONE. Topology METRICS include cost/tokens/duration
  (`TopologyTab.tsx:43-48`); drawer shows the size-metric value.
- AC4 — DONE. Child rows are `<Link to={/sessions/<child>}>`
  (`OverviewTab.tsx:93`).

### Affected contracts and surfaces

- `internal/presenter/session_detail.go` — `childSummary` gains a nested
  `Children` field; `loadChildSessions` → recursive descendant fetch (single
  `WITH RECURSIVE` CTE, depth-capped at 20 as cycle defense-in-depth), nested
  in Go.
- `frontend/src/api/types.ts` — `ChildSummary.children?`.
- `frontend/src/pages/SessionDetail/OverviewTab/OverviewTab.tsx` — render the
  child list as a recursive indented tree.
- `.agents/sow/specs/rest-api.md` §GET /api/sessions/:id — child_sessions now a
  nested tree.
- `.agents/sow/specs/ui-pages.md` §/sessions/:id Overview — the tree.
- No schema change (`parent_session_id` already exists). Additive JSON key.

### Spec deltas before any code

1. `rest-api.md` §GET /api/sessions/:id — `child_sessions` becomes a nested
   tree (each child may carry its own `child_sessions`); depth-capped.
2. `ui-pages.md` §/sessions/:id — Overview shows the full sub-session tree
   (root → children → grandchildren), indented, with per-node economics.

### Patterns to reuse

- The Timeline + Topology endpoints already resolve the full session tree
  server-side — the recursion pattern (descendants of a root) is established.
- The existing child-table row (link + StatusBadge + columns) — the recursive
  renderer reuses it with an indent per depth.

### Risk and blast radius

- Low–moderate. The `child_sessions` contract change is additive (a new
  `children` key; existing consumers that read the flat list still work — they
  just don't descend). The recursive CTE is bounded (depth cap + the tree is
  naturally small: typically 2–4 levels). Defense-in-depth: depth cap guards
  against a hypothetical parent-cycle in malformed data.
- Performance: one query (was one query); the CTE scans descendants of one
  root, bounded by tree size.

### Sensitive data handling

None — session structural + aggregate metadata only.

### Implementation plan

- **A. Spec** — rest-api.md + ui-pages.md deltas.
- **B. Backend** — recursive CTE + Go nesting + `Children` field + depth cap.
- **C. Frontend** — ChildSummary type + recursive tree renderer (indented).
- **D. Tests** — backend: a grandchild in the fixture, assert nesting +
  depth-cap; frontend: recursive render shows grandchildren indented.
- **E. Gates** — full local gate aggregate.

### Validation plan

- `session_detail_test.go` — seed a grandchild (child of childA1); assert
  `child_sessions[0].child_sessions` is populated; assert a deeper-than-cap
  cycle is truncated (depth-cap defense).
- `OverviewTab.test.tsx` — a child with a grandchild renders both, the
  grandchild indented under its parent.
- Existing flat-list tests stay green (additive).

### Open decisions

None blocking (CTO): recursive backend (single fetch, long-term-best) over lazy
expansion; all-expanded render (AC1 wants the complete tree visible); depth cap
20.

## Implementation

AC1 completion pass (ACs 2/3/4 already met). The Overview's flat direct-children
list is now the full recursive descendant tree:

- Backend (`internal/presenter/session_detail.go`): `childSummary` gains a nested
  `Children` field; `loadChildSessions` → `loadChildTree` — a single `WITH
  RECURSIVE` CTE fetches every descendant (depth-capped at 20 as cycle
  defense-in-depth), nested in Go via a pointer tree (`childTreeNode` + recursive
  `copyChildTree` value copy, so Go value semantics don't freeze stale child-less
  copies). One query (was one), bounded by tree size.
- Frontend (`api/types.ts`): `ChildSummary.child_sessions?`.
- Frontend (`OverviewTab.tsx`): a recursive `ChildTreeRows` component walks the
  nested tree depth-first, each level indenting the Agent cell (CSS var `--depth`)
  with a `└` marker, reusing the existing row (link/StatusBadge/columns).
- Specs: `rest-api.md` §GET /api/sessions/:id (child_sessions is a nested tree,
  depth-capped); `ui-pages.md` §/sessions/:id (child-sessions tree).

## Validation

- `session_detail_test.go` — `TestSessionDetail_ChildTreeIsNested`: a 3-level
  fixture (root → child → grandchild) asserts `child_sessions[0].child_sessions`
  holds the grandchild; `TestSessionDetail_ChildTreeDepthCap`: a chain deeper
  than the cap is truncated (no unbounded payload / no panic).
- `OverviewTab.test.tsx` — the nested grandchild renders under its parent,
  indented, linking to its own detail.
- Gates: go test -race PASS (presenter), 668 frontend tests PASS, tsc + eslint
  clean, spec-drift PASS, lint clean.

## Reviews

### Round 1 (6 reviewers: glm/minimax/mimo/kimi/qwen/deepseek)

Scores: glm 9 PG, minimax 9.5 PG, mimo 9.5 PG, kimi 9.5 PG, qwen 8 NEEDS WORK,
deepseek 10 PG. 5/6 PG. qwen's P2 verified real + fixed:

- **FIXED (P2) cycle-inversion guard** (qwen) — a malformed parent_session_id
  cycle could surface the queried `id` among its own descendants, nesting it as
  its own child. Added `if c.ID == id { continue }` in the scan loop (a session
  is never its own child) — defense-in-depth.
- **FIXED (P2) branching-tree coverage** (qwen) — `TestSessionDetail_ChildTree-
  Branching`: a root with two children, one with two grandchildren, asserts both
  roots render AND nested grandchildren attach to the correct parent (not the
  sibling); a leaf sibling has no nested child_sessions. (Round 1 only had a
  linear chain + a deep chain.)
- **FIXED (P3) a11y** (glm/mimo) — the `└` tree marker is now `aria-hidden`
  (decorative; the link text is the SR name).
- **REJECTED (P3) "dead parent field"** (glm) — false positive; `childTreeNode.
  parent` IS read at the nesting loop (session_detail.go:319/322).
- **REJECTED (P3)** treegrid ARIA roles (a reasonable deferral for this phase;
  the indented table is SR-readable as a table; the depth cap is documented in
  rest-api.md already).

Round 2 runs the same scope + the fix notes.

### Round 2 (same 6 reviewers) → Round 3 (CONVERGED)

Round 2: 5/6 PG; qwen 9 flagged that the Round-1 cycle guard was untested (the
DepthCap test is an acyclic chain, not a cycle). Fixed:
- **FIXED (P2→test)** `TestSessionDetail_ChildTreeCycleGuard` — seeds a real
  3-node cycle (rootCyc → A → B → rootCyc, the closing edge via UPDATE for
  FK-order) and asserts rootCyc never appears in its own child tree while the
  acyclic remainder (A → B) surfaces. Pins the `c.ID == id` guard (Hard Rule #5).

Round 3: 6/6 PRODUCTION GRADE (glm 10, minimax 9.5, mimo 9.5, kimi 9.5, qwen
9.5, deepseek 10; avg ~9.75). No P0/P1/P2. Two trivial P3 cleanups applied
(dead `if out == nil` after make; a stale `loadChildSessions` doc comment).

## Outcome

**Status: completed — all 4 ACs met, 6/6 PG round 3, all gates green.** AC1
(recursive child-sessions tree) delivered via a single recursive-CTE fetch +
Go pointer-tree nesting (value-copy-safe) + a recursive indented renderer;
ACs 2/3/4 verified already met. Defense-in-depth: CTE depth cap (20) + the
queried-id cycle guard, both pinned by tests (branching, deep-chain, cycle).

## Outcome

(filled on completion)
