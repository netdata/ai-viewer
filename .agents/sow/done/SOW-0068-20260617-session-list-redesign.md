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

## Status

Status: completed (moving to done/). ACs 3 + 4 shipped earlier; this pass closed
ACs 1, 2, 5. 6-reviewer loop converged round 2: 6/6 PRODUCTION GRADE (avg ~9.7).

## Pre-Implementation Gate

### Problem / root-cause model

The Sessions list (`frontend/src/pages/SessionsList/SessionsList.tsx:48`)
hardcodes `useSessionsInfinite(filters, 'root')`, so it shows ONLY root
sessions. There is no way to surface secondary sessions (sub-agents,
tool-internal, forks), no visual distinction between primary and secondary, and
no "view this secondary in its primary's tree" affordance. The data model
already supports all of it: `SessionListItem.kind` (`root|sub_agent|
tool_internal|fork`), `parent_session_id`, and the hook already accepts
`group: 'root' | 'all'`. Pure frontend gap.

### Evidence reviewed (self-review, file:line)

- **AC1 (primary/secondary distinction)** — NOT DONE. `SessionRowBody`
  (`components/SessionRow/SessionRow.tsx`) renders no `kind` badge. Since the
  list is hardcoded to `group='root'`, every row is a root today, so the
  distinction only matters once AC2 enables secondaries.
- **AC2 ("show secondary" toggle)** — NOT DONE. `SessionsList.tsx:48` hardcodes
  `'root'`. No toggle control exists.
- **AC3 (Sources dropdown from /api/sources)** — DONE (`FilterBar.tsx` Sources
  chips, commit `70e2e9a`; verified during SOW-0067).
- **AC4 (error_class badge on failed)** — DONE (`SessionRow.tsx` StatusBadge +
  `error_class` span, commit `3dbefb3`).
- **AC5 (secondary → primary tree)** — NOT DONE. `ChildExpander` links a ROOT
  row to its own detail; there is no "go to my parent's tree" affordance for a
  secondary row. `parent_session_id` is on `SessionListItem` (types.ts:81).
  Detail page tabs live in `?tab=` (default `overview`; `topology` shows the
  tree) — `SessionDetail.tsx:42`.

### Affected contracts and surfaces

- `frontend/src/pages/SessionsList/SessionsList.tsx` — add the toggle; thread
  the `group` arg; render a kind badge + parent link (delegated to SessionRow).
- `frontend/src/components/SessionRow/SessionRow.tsx` — render the `kind` badge
  + the parent-tree link.
- `frontend/src/pages/SessionsList/SessionsList.module.css` + `SessionRow.module.css`
  — styles for the badge + toggle + link.
- `.agents/sow/specs/ui-pages.md` §"/" — describe the distinction + toggle.
- No backend / schema / adapter / canonical changes. `/api/sessions?group=all`
  already returns secondaries with `kind` + `parent_session_id`.

### Spec deltas to land before any test or code

1. `ui-pages.md` §"/" — replace "Hierarchical list: root sessions at the top
   level…" with: the list defaults to PRIMARY (root) sessions; a "Show
   secondary" toggle reveals sub-agent / tool-internal / fork sessions (group
   =all), each marked with a kind badge; a secondary row links to its parent
   session's Topology tab.

### Patterns to reuse

- The `group: 'root' | 'all'` arg is already on `useSessionsInfinite`
  (`api/sessions.ts:97`) — just thread a toggle to it.
- `StatusBadge` + the `error_class` span pattern in SessionRow for the new kind
  badge styling.
- `Link to={\`/sessions/${id}\`}` pattern (ChildExpander) for the parent link,
  with `?tab=topology`.

### Risk and blast radius

- Low. Frontend-only; the backend already supports `group=all`. The toggle is
  local component state (a view switch, not a shareable filter — matches the
  SOW's "provide a toggle" language). Default stays `group='root'` so the
  default view is unchanged for the operator.
- A11y: the toggle is a real checkbox (keyboard reachable); the badge + parent
  link are text/links (SR reachable).

### Sensitive data handling

None. No new data sources; `kind`/`parent_session_id` are structural metadata.

### Implementation plan

- **A. Spec** — ui-pages.md §"/" delta.
- **B. SessionRow** — kind badge (secondary only) + parent-tree link (secondary
  only, when `parent_session_id` set).
- **C. SessionsList** — "Show secondary" checkbox → switches group 'root'↔'all'.
- **D. Tests** — SessionsList + SessionRow assertions.
- **E. Gates** — full local gate aggregate.

### Validation plan (named tests + behaviors)

- `SessionsList.test.tsx` — the toggle switches the `useSessionsInfinite` group
  arg from 'root' to 'all'; off by default.
- `SessionRow.test.tsx` — a `kind='sub_agent'` row renders a "sub-agent" badge;
  a `kind='root'` row renders no badge; a secondary row with a `parent_session_id`
  renders a link to `/sessions/<parent>?tab=topology`.
- Existing tests stay green.

### Open decisions

None blocking (CTO calls per Hard Rule #1): toggle = local state (not URL);
default stays root-only; secondary badge text = "sub-agent"/"internal"/"fork";
parent link target = Topology tab.

## Implementation

SOW-0068 completion pass — closed ACs 1, 2, 5 (ACs 3 + 4 were already shipped).
Frontend-only; no backend/schema/adapter changes (`/api/sessions?group=all` +
`kind` + `parent_session_id` already existed).

- `SessionsList.tsx` — "Show secondary" checkbox (local state) switches the
  `useSessionsInfinite` group arg `'root' ↔ 'all'`; default stays root-only so
  the operator's default view is unchanged.
- `SessionRow.tsx` — secondary rows render a kind badge next to the agent name
  (`sub-agent` / `internal` / `fork`; root renders no badge — primary is the
  default); a secondary with a `parent_session_id` renders a "↩ parent" link to
  `/sessions/<parent>?tab=topology` (the tree that spawned it).
- CSS (`SessionsList.module.css`, `SessionRow.module.css`) — header/toggle +
  badge/parent-link styles, all theme tokens, focus-visible rings.
- `ui-pages.md` §"/" updated to describe primary-default + the toggle + the
  secondary drill-in.

## Validation

- `SessionsList.test.tsx` — defaults to group=root; the toggle widens to
  group=all.
- `SessionRow.test.tsx` — root renders no badge; each secondary kind renders its
  badge; a secondary with a parent renders the "↩ parent" link to the parent's
  Topology tab; a root renders no parent link.
- Gates: frontend 666 tests PASS; tsc + eslint clean; spec-drift PASS; go build
  OK (backend unchanged).

## Reviews

### Round 1 (6 reviewers: glm/minimax/mimo/kimi/qwen/deepseek)

Scores: glm 9 PG, minimax 9.5 PG, mimo 9.5 PG, deepseek 10 PG, kimi 8 NEEDS
WORK, qwen 8.5 NEEDS WORK. 4/6 PG. Both NEEDS-WORK findings verified real + fixed:

- **FIXED (P2) `--font-size-xs` was undefined** (qwen) — the token was
  referenced in 6 places (`.dimSublabel`, the SOW-0067 `.note`, OverviewTab,
  and my new `.kindBadge`/`.parentLink`) but never declared in `tokens.css`;
  every one of those elements inherited the parent font-size. Added
  `--font-size-xs: 0.6875rem` to tokens.css — fixes all 6 usages at the source.
  (Pre-existing latent bug I had propagated.)
- **FIXED (P2) parent-link defensive guard** (kimi) — the link was gated only
  on `parent_session_id`, so a (structurally-impossible) root-with-parent or
  self-parent would render a misleading link. Now gated on `kind !== 'root'`
  too (defense-in-depth + spec precision); pinned by a new test.
- **REJECTED (P3, taste)** group='all' recency ordering (the SOW scopes primary-
  first to the default root-only view, which is satisfied), unknown-future-kind
  renders no badge (OpenEnum graceful-degradation is correct), the pre-existing
  stale ChildExpander comment (out of SOW scope).

Round 2 runs the same scope + the fix notes.

### Round 2 — CONVERGED (6/6 PRODUCTION GRADE)

Scores: glm 9.5, minimax 9.5, mimo 9.5, kimi 10, qwen 9.5, deepseek 10 (avg
~9.7). No P0/P1/P2. Both round-1 fixes verified correct. Remaining P3s:
- The `&& session.parent_session_id` inside the `showParentLink` branch (2
  reviewers called "redundant") is a REQUIRED TypeScript narrowing guard —
  `showParentLink` is boolean (doesn't narrow the field type), and
  `encodeURIComponent` needs `string` not `string | null`; tsc clean confirms.
  Kept as-is.
- Comment precision ("never a self-link") + parent-link aria-label verbosity —
  taste; left as-is.

## Outcome

**Status: completed — all 5 ACs met, 6/6 PG, all gates green.** AC1 (primary/
secondary kind badge), AC2 ("Show secondary" toggle), AC5 (secondary → parent
Topology drill-in) delivered; AC3 (Sources dropdown) + AC4 (error_class badge)
were already shipped. Drive-by: fixed a pre-existing latent token bug
(`--font-size-xs` was referenced in 6 places, never declared).

## Outcome

(filled on completion)
