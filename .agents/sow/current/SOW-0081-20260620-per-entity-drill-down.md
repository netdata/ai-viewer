# SOW-0081 — Per-entity drill-down: /agents, /models, /tools become real pages

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0080 umbrella approved (operator "go"). SOW-0081 is the first child SOW. Pages to ship: `/agents`, `/models`, `/tools` (replacing ComingSoon placeholders) plus the deep drill routes `/agents/:name`, `/models/:name`, `/tools/:name` (sessions for one entity).

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

The current /agents, /models, /tools routes each render `<ComingSoon title=... note=... />` — a placeholder from the Phase-3 stub set added in SOW-0077. The data layer is rich enough to drive real pages:
- `agent_name` is a first-class indexed column on `sessions` (data-model.md §Sessions)
- The `/api/sessions?agents=...&models=...&tools=...` filter supports entity-scoped queries (rest-api.md §Sessions)
- The /api/stats endpoint supports `by_agent`, `by_model`, `by_tool` breakdowns (rest-api.md §Stats)

This SOW makes those placeholder pages real so P1/P2/P3 use cases ("which agent is failing most?", "which model runs the most?", "what tools are being called?") are answerable in under 30 seconds — without manually composing filters in the URL.

Evidence reviewed:

- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — the umbrella plan; SOW-0081 is item #1 of 6
- `.agents/sow/specs/data-model.md` — sessions schema (agent_name, model, tools), unique-indexed
- `.agents/sow/specs/rest-api.md` — /api/sessions filter contract, /api/stats by_* breakdown contract
- `.agents/sow/specs/ux-research-2026-06-20.md` — items A3, A4, A5, D7
- `.agents/sow/done/SOW-0079-20260620-ux-gaps-p0.md` — Home summary card + /failures pattern (the established shape)
- `.agents/sow/done/SOW-0077-20260618-polish-coming-soon.md` — the placeholder pages this SOW replaces
- `frontend/src/components/ComingSoon.tsx` — the placeholder being replaced (will remain for any future stubs)

Affected contracts and surfaces:

- Routes: existing /agents, /models, /tools (replacing ComingSoon); new sub-routes /agents/:name, /models/:name, /tools/:name
- API: no new endpoints; uses existing /api/sessions + /api/stats
- DB: no schema changes
- Specs: `frontend-architecture.md` (Routes table) gets the new sub-routes; new `ui-pages.md` describes the per-entity page contract
- Components: new `src/pages/Agents/{AgentsList,AgentDetail}.tsx`; same for Models, Tools
- Tests: 6 new test files (~8 cases each)
- Sidebar: existing Drill-down section (Agents/Models/Tools) gains detail-link behavior on the entries (already does — NavLink goes to the index page; the per-entity pages are reached via the index's entity cards)

Existing patterns to reuse:

- Per-page pattern from SOW-0073 (header + summary stats + toolbar + designed table + empty/loading/error states)
- useFilters + URL-synced filters (the per-entity pages bind the entity name into the agents/models/tools filter)
- useSessionsInfinite + useStats hooks (existing)
- StatPill from SessionsList.tsx
- shadcn/ui primitives (Card, Table, Badge, Tooltip)
- The /failures page (SOW-0079 P0.2) — same shape: header → time-window selector → summary stats → designed table
- The HomeSummaryCard pattern for the entity index page (a card per entity, each with summary stats + drill-down)

Risk and blast radius:

Low. All new routes are additive. The 3 ComingSoon pages are replaced — anyone who somehow bookmarked them will see the real content. The new sub-routes /agents/:name etc. have no prior behavior. No DB migrations, no API changes, no breaking changes to existing components.

Sensitive data handling plan:

N/A. The per-entity pages render the same SessionListItem rows the Sessions page already shows. No new data exposed.

Implementation plan:

1. **New spec: ui-pages.md** — describe the per-entity page contract (header shape, summary stats shape, table shape, empty/loading/error states). Lands in the same commit as the first page implementation.
2. **Frontend types** — add `AgentSummary`, `ModelSummary`, `ToolSummary` to `frontend/src/api/types.ts`. Each is `{name: string, session_count, total_cost_usd, total_tokens, failure_count, last_seen_ts}`. Derives from /api/stats by_* endpoints.
3. **`useEntitySummaries` hook** — wraps /api/stats with a `group_by` parameter that selects the right by_* breakdown. Lives in `frontend/src/api/stats.ts` next to the existing `useStats`.
4. **`/agents` page (AgentsList)** — header + summary stat strip across all agents + grid of agent cards (each card: agent name, session count, cost, reliability %, last seen, click → /agents/:name).
5. **`/agents/:name` page (AgentDetail)** — header (agent name + back-to-list + summary stats for this agent) + toolbar (time window) + designed sessions table filtered to this agent.
6. Same pattern for `/models` + `/models/:name` and `/tools` + `/tools/:name`.
7. **Tests** — 6 new test files: AgentsList, AgentDetail, ModelsList, ModelDetail, ToolsList, ToolDetail. Each covers loading, error, empty, populated, time filter, sort, drill-down.
8. **Routes** — add the sub-routes in App.tsx.
9. **5-reviewer Production-Grade Loop** — runs on the final diff at the end of the SOW.

Validation plan:

- `scripts/lint.sh` clean
- `scripts/test.sh` clean (target: ~750 tests after this SOW, from 705)
- `scripts/check-coverage.sh` clean
- `npm run build && npm run check:bundle-size` clean (≤ 500 KB gz)
- playwright_headless screenshots of /agents, /models, /tools in light + dark mode
- 5 reviewer verdicts (PRODUCTION GRADE / NEEDS WORK) on the final diff

Artifact impact plan:

- AGENTS.md: unaffected (no change to operating contract)
- Runtime project skills: unaffected (no new patterns; per-page pattern is in SOW-0073)
- Specs: new `ui-pages.md` (per-page contract catalog); `frontend-architecture.md` Routes table updated
- End-user docs: unaffected (the routes were already documented as "coming soon")
- SOW lifecycle: this SOW ships, moves to `done/`, then SOW-0082 starts

Open decisions:

- **Agent name collision with source name**: `agent_name` values like `codex`, `claude-code`, `opencode` happen to match source names. The /agents/:name route accepts the agent name verbatim; no collision in practice because the agent name is a free-form string set by the agent (could be `codex-prod-v2` or `claude-code-internal`). If the operator wants strict separation later, we can namespace (`/agents/by-name/:name`). Deferred to operator feedback.
- **Drill from any chart point to entity**: the chart-to-entity drill-down is in SOW-0084 (chart tooltips). This SOW makes the per-entity pages exist; SOW-0084 wires the chart click → entity page navigation.

## Requirements

### Purpose

Turn the 3 ComingSoon placeholders into real pages that answer "which agent is failing most often?", "which model runs the most?", and "what tools are being called?" in under 30 seconds, with one-click drill-down to sessions filtered to that entity.

### User Request

Verbatim, operator 2026-06-20: "I think we should fix all the UX issues. Can you setup goals and work against them autonomously?"

SOW-0080 (the umbrella plan the operator signed off with "go") sets this SOW as item #1.

### Assistant Understanding

The data layer is rich; the gap is information architecture. The 51-gap catalog marked A3, A4, A5, D7 as P1 with High severity — these are the most-asked questions by both P1 (Power User) and P2 (AI-Agent Builder) personas. The fix is straightforward: the per-entity pages exist as placeholders, the data exists, the filters exist. The work is wiring them into real pages following the established per-page pattern.

### Acceptance Criteria

1. ✅ `/agents`, `/models`, `/tools` each show a list/summary view of every distinct entity (agent name, model, tool) with summary stats per entity.
2. ✅ `/agents/:name`, `/models/:name`, `/tools/:name` each show the sessions table filtered to that entity.
3. ✅ Each per-entity detail page has a header (entity name + summary stats), a toolbar (time window), and the designed sessions table.
4. ✅ All tests pass; bundle stays under 500 KB gz.
5. ✅ 5-reviewer Production-Grade Loop converges with 5/5 PRODUCTION GRADE (or only P3 noise with documented disposition).

## Plan

1. New `ui-pages.md` spec describing the per-page contract (header / toolbar / content / empty / loading / error).
2. Add `AgentSummary`, `ModelSummary`, `ToolSummary` types + `useEntitySummaries` hook in `frontend/src/api/stats.ts`.
3. Build `AgentsList`, `AgentDetail` (then ModelsList/Detail, ToolsList/Detail — same pattern).
4. Wire the sub-routes in `App.tsx`.
5. Add tests (loading/error/empty/populated/time-filter/sort/drill).
6. Run all gates; run the 5-reviewer cycle; merge.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Followup

- **SOW-0082** — Ingest errors surfaced (next in the 0081-0086 sequence)
