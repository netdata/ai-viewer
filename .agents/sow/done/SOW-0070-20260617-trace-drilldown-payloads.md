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

## Status

Status: completed (moving to done/). Full SOW (AC1+AC2+AC3+AC4). 6-reviewer loop
converged round 4: 6/6 PRODUCTION GRADE (avg ~9.25).

## Pre-Implementation Gate

### Problem / root-cause model

The Trace tab builds its op tree from `detail.turns` — the SINGLE queried
session's turns+ops (`TraceTab.tsx:42`). Sub-session ops are invisible: a
`child_session_id` op is a LEAF boundary by design (`viz/trace.ts` buildOpTree:
"the child session's ops are fetched and rendered separately"). AC1 needs the
MERGED whole-tree trace: every session sharing `root_session_id`, each op tagged
with its owning session, spliced so a sub-session's op-tree nests under its
spawning `child_session_id` op. The Timeline + Topology endpoints already resolve
the whole tree via `resolveRootSessionID` + `WHERE root_session_id = ?`
(`session_timeline.go:92,143,177`) — the reusable pattern.

### Evidence reviewed (self-review, file:line)

- AC1 — GAP. `TraceTab.tsx:42` `buildOpTree(detail.turns)`; single session.
  `buildOpTree` (`viz/trace.ts`) nests by `parent_op_id`; a `child_session_id`
  op is `isLeafBoundary`. The whole-tree merge needs every session's ops tagged
  + spliced.
- AC2 — DONE (stale comments). `SpanDetailDrawer.test.tsx:135,150-153` asserts
  an ACTIVE preview button fetching `/api/payloads/:id`; but
  `SpanDetailDrawer.tsx:33-36` + test:14-15 say "deferred to SOW-0033 /
  DISABLED preview coming soon" (SOW-0033 is long done).
- AC3 — PARTIAL. `error_class` shown (`SpanDetailDrawer.tsx:291`); `error_message`
  EXISTS in the DB (`ops.error_message`, 0001_initial.sql:114) + canonical model
  (`events.go:226,315`) but is NOT in `opDetail`/`OpDetail`/the SELECT/the drawer.
- AC4 — PARTIAL. op-kind + status filters shipped (`71cd7ca`, `TraceTab.tsx:36-53`).
  Sub-agent filter is impossible until AC1 brings `session_agent_name` per op.

### Design (CTO decision: new endpoint, long-term-best)

New endpoint **`GET /api/sessions/:id/trace`** (mirrors Timeline/Topology: each
view has its own tree-resolving endpoint). Resolves :id → root via the existing
`resolveRootSessionID`; returns every op in the tree, each tagged with
`session_id` + `session_agent_name` + `session_kind` + `turn_seq` + `error_message`,
in one query (`session_id IN (SELECT id FROM sessions WHERE root_session_id = ?)`).

Frontend merge (`viz/trace.ts` new `buildMergedTree`): group ops by session;
build per-session trees via `parent_op_id`; splice each child session's roots
under its spawning `child_session_id` op. Reuses the existing per-session tree
semantics (leaf-boundary + cycle-hoist).

Sub-agent color (AC1): each op carries `session_agent_name`; a new categorical
agent-color palette in `viz/color` maps agent → stable color (left-border swatch
on each op row). Sub-agent filter (AC4): a dropdown of distinct agent names.

`error_message` (AC3): thread through `opDetail` → `OpDetail` → the drawer
(prominent, under error_class, on failed ops).

### Affected contracts and surfaces

- `internal/presenter/session_trace.go` (NEW) — the endpoint + query.
- `internal/presenter/presenter.go` — register `/api/sessions/{id}/trace`.
- `internal/presenter/session_detail.go` — `opDetail` gains `ErrorMessage`
  (additive JSON key); the SELECT gains the column.
- `frontend/src/api/sessions.ts` — `useSessionTrace(id)` hook.
- `frontend/src/api/types.ts` — `TraceOp` (OpDetail + session tags + error_message);
  `OpDetail.error_message`.
- `frontend/src/viz/trace.ts` — `buildMergedTree(ops)`; extend `TraceNode` with
  `sessionId`/`sessionAgent`.
- `frontend/src/viz/color.ts` — categorical agent-color palette.
- `frontend/src/pages/SessionDetail/TraceTab/TraceTab.tsx` — use the merged tree;
  add the sub-agent filter; color ops by agent.
- `frontend/src/components/SpanDetailDrawer/` — error_message field; stale-comment
  cleanup.
- `.agents/sow/specs/rest-api.md` — `GET /api/sessions/:id/trace`; error_message
  on opDetail.
- `.agents/sow/specs/ui-pages.md` — the whole-tree Trace.

### Spec deltas before any code

1. `rest-api.md` — new `### GET /api/sessions/:id/trace` (whole-tree ops, tagged
   by session); `opDetail` gains `error_message` (nullable).
2. `ui-pages.md` §/sessions/:id Trace — the merged whole-tree trace, sub-agent
   color + filter, error ops showing error_class + error_message, payload preview.

### Patterns to reuse

- `resolveRootSessionID` + `WHERE root_session_id = ?` (Timeline/Topology).
- `buildOpTree` per-session tree semantics (parent_op_id, leaf-boundary, cycle-hoist).
- The presenter's method/HEAD/405/control-byte/param-binding discipline.
- The op-kind/status filter UI in TraceTab.

### Risk and blast radius

- Moderate. New endpoint (additive; the existing `/api/sessions/:id` detail is
  unchanged). The merge algorithm is the subtle part (cross-session splice +
  cycle defense). Performance: one query over the tree's ops (bounded by tree
  size; matches Timeline). Sub-agent color is theme-token-driven (no hardcoded
  hex).
- The detail endpoint's `opDetail.ErrorMessage` is additive; existing consumers
  ignore it.

### Sensitive data handling

Payload previews already go through the `/api/payloads/:id` containment check;
no change. The trace op list carries structural + aggregate metadata; no raw
payload content beyond what the single-session trace already exposes.

### Implementation plan

- **A. Specs** — rest-api.md + ui-pages.md deltas.
- **B. Backend** — `session_trace.go` (endpoint + query); register; thread
  `error_message` into `opDetail` + SELECT (both endpoints).
- **C. Frontend data** — `TraceOp` type; `useSessionTrace`; `OpDetail.error_message`.
- **D. Merge** — `buildMergedTree` in viz/trace (+ tests).
- **E. Color** — agent palette in viz/color.
- **F. TraceTab** — merged tree; sub-agent filter; agent-colored rows;
  error_message in the drawer; stale-comment cleanup.
- **G. Gates** — full local gate aggregate; 6-reviewer loop.

### Validation plan (named tests + behaviors)

- `session_trace_test.go` — a 2-level tree (root + child) returns BOTH sessions'
  ops tagged with their session_id + agent; the child's ops are present; an
  unknown id → 404; method/HEAD/control-byte discipline.
- `viz/trace` `buildMergedTree` tests — the child session's roots splice under
  the spawning child_session_id op; a child_session_id with no matching session
  stays a leaf; a cross-session cycle is defended.
- `TraceTab` tests — the merged tree renders ops from 2 sessions; the sub-agent
  filter narrows; error ops show error_message.
- `SpanDetailDrawer` — error_message renders on a failed op.

### Open decisions

None blocking (CTO): new dedicated endpoint (not bloating detail); merged tree
built client-side from a flat tagged op list; sub-agent color via a stable
categorical palette; sub-agent filter as a new dropdown alongside kind/status.

## Implementation

Full SOW (AC1+AC2+AC3+AC4). AC1 is the SOW's purpose (whole-tree trace); the
rest are its details.

- **Backend** `internal/presenter/session_trace.go` (NEW): `GET /api/sessions/:id
  /trace` — resolves :id → root via the existing `resolveRootSessionID`, returns
  every op of every session sharing `root_session_id`, each tagged with
  `session_id`/`session_agent_name`/`session_kind` + `turn_seq` + `error_message`,
  in one query + one payload_refs query. Mirrors the Timeline/Topology
  root-resolution + tree scope.
- **Backend** `session_detail.go`/`session_detail_ops.go`: `opDetail` +
  `/sessions/:id` gain `error_message` (AC3); threaded through SELECT + scan +
  `fillOpNullables`.
- **Frontend types** `api/types.ts`: `TraceOp` (OpDetail + session tags), `Trace
  Response`; `OpDetail.error_message`.
- **Frontend hook** `api/sessions.ts`: `useSessionTrace(id)`.
- **Merge** `viz/trace.ts`: `buildMergedTree(ops)` — per-session trees by
  `parent_op_id` (scoped to the session, so a reused op id cannot cross-link),
  then splice each child session's roots under its `child_session_id` op. Cycle-
  safe (a session is never spliced under its own descendant). `TraceNode` gains
  optional `sessionId`/`sessionAgent`.
- **Color** `viz/color.ts`: `colorForAgent(name)` — stable djb2-hash → theme-
  token palette (theme-aware, no hardcoded hex); registered in refreshThemeColors.
- **TraceTab**: uses the merged tree (falls back to single-session while the
  trace loads); new sub-agent filter (when >1 agent); EventList gets a Sub-agent
  column (swatch + name).
- **SpanDetailDrawer**: error_message field on failed ops (AC3); stale "deferred
  to SOW-0033 / preview coming soon" comments removed (AC2 cleanup — the preview
  is live).
- **Specs**: `rest-api.md` new §GET /api/sessions/:id/trace + error_message on
  opDetail; `ui-pages.md` the whole-tree Trace.

## Validation

- `session_trace_test.go` — whole-tree returns all sessions' ops tagged;
  child-id resolves to the whole tree; error_message surfaced; 404; empty tree;
  405.
- `viz/trace.test.ts` — `buildMergedTree` splice + absent-child-leaf + parent
  scoping (reused op id in two sessions) + empty.
- `EventList.test.tsx` — per-row sub-agent indicator for whole-tree nodes.
- `SpanDetailDrawer.test.tsx` — error_message renders on a failed op.
- Gates: go test -race PASS (presenter), coverage PASS (presenter 92.x%),
  675 frontend tests PASS, tsc + eslint clean, spec-drift PASS, lint clean.

## Reviews

### Round 1 (6 reviewers: glm/minimax/mimo/kimi/qwen/deepseek)

Scores: glm 7, minimax 8, mimo 9 PG, kimi 9 PG, qwen 8, deepseek 9 PG. 3/6 PG.
Consensus findings verified real + fixed:

- **FIXED (P1) SSE live-refresh** (glm) — the trace key `['session-trace', id]`
  was NOT invalidated by SSE (only timeline/topology), so the whole-tree trace
  never live-refreshed. Added the invalidation in `api/sse.ts` alongside
  timeline/topology.
- **FIXED (P1/P2) per-session cycle-break** (glm/minimax/kimi consensus) —
  `buildMergedTree` lacked `buildOpTree`'s reachability pass, so a closed
  parent_op_id cycle (A→B→A) within a session silently DROPPED its nodes —
  contradicting the spec's "never drops or duplicates a node." Ported the
  reachability + hoist pass (per-session, before the splice); pinned by a test.
- **FIXED (P2) topPad colSpan** (qwen) — the top padding row was still colSpan=5
  (I'd only fixed bottomPad); now 6 to match the new Sub-agent column.
- **REJECTED (P2/P3, taste/by-design)** cross-session splice cycle "infinite
  recursion" (theoretical — requires a session spliced under its own descendant,
  which the `childSid !== node.sessionId` guard + the splice direction forbid;
  no test reproduces it); `t.seq` LEFT JOIN nullability (FK guarantees the turn;
  a fail-fast scan is correct); agentFilter persistence (local view state, like
  the kind/status filters — by design); SpanDetailDrawer test stale comment,
  colorForAgent unit tests, Event-list spec bullet (P3 polish).

Round 2 runs the same scope + the fix notes.

### Round 2 (same 6 reviewers) → Round 3

Round 2: mimo 9 PG, deepseek 9 PG; glm/minimax/kimi/qwen re-flagged two items
that were VERIFIED-correct on self-review (Hard Rule #4):
- **SSE invalidation** (kimi P1) — REJECTED as a false positive. The resolver
  emits `session_changed` for the child, parent, AND root (resolver_notify_test
  line 145: "an open R-detail view only refetches if R itself is signalled"), so
  `['session-trace', e.session_id]` DOES match the root-keyed trace when the
  root's event fires. deepseek independently verified this (9 PG).
- **3+ cycle shape** (minimax/kimi P1, qwen P2) — the cycle-break is a FAITHFUL
  port of buildOpTree's algorithm (same DFS + hoist); the "flatten to roots"
  behavior for cycle members is the EXISTING single-session contract, not a
  drop/duplicate. Verified by a new 3-cycle + descendant test (no drop, no dupe).

Still applied the cheap, valid doc/test hardening: a precision comment on
buildMergedTree's cycle contract + the 3-cycle test. Round 3 follows.

### Round 3 (same 6 reviewers) → Round 4

Round 3: 5/6 PG at 9 (mimo/kimi/qwen/deepseek/minimax). glm 8 raised a P2 the
CTO initially mis-dispositioned as a false positive in round 2 — but on
re-verification (Hard Rule #4) **glm was right**: buildMergedTree's cycle-break
omitted buildOpTree's INNER re-DFS (the post-hoist descendant-marking that keeps
a cycle's members NESTED under the first hoisted root instead of flattening them
to separate roots). The "faithful port" comment was inaccurate. Fixed:
- **FIXED (P2)** ported buildOpTree's inner re-DFS into buildMergedTree's hoist
  loop — a hoisted root's descendants are now marked reachable so they stay
  nested (one root per cycle, not a flat list). The 3-cycle test asserts
  `forest.length === 1` (nesting preserved), not just "no drop."

(Correction logged: round-2 self-review called this a false positive; it was a
real porting gap. The SSE finding from round 2 remains a verified false positive
— the resolver emits session_changed for child+parent+root.)

Round 4 confirms convergence.

### Round 4 — CONVERGED (6/6 PRODUCTION GRADE)

Scores: glm 9, minimax ~9, mimo 9.5, kimi 9, qwen 9, deepseek 10 (avg ~9.25).
The round-3 inner re-DFS fix verified correct — buildMergedTree's cycle behavior
now matches buildOpTree exactly (one hoisted root per cycle, members nested).
One stale-JSDoc nit ("flattens"→"preserves nesting") fixed. No P0/P1/P2.

## Outcome

**Status: completed — all 4 ACs met, 6/6 PG round 4, all gates green.** AC1
(whole-tree trace via new `/api/sessions/:id/trace` + `buildMergedTree` with
sub-agent color/filter); AC2 (payload preview — already live, stale comments
cleaned); AC3 (error_message threaded through both endpoints + the drawer); AC4
(op-kind/status/sub-agent filters). The merge is a faithful port of buildOpTree's
parentage + cycle contract, extended with the cross-session splice.

## Outcome

(filled on completion)
