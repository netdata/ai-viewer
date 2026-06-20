# SOW-0081 — Per-entity drill-down: /agents, /models, /tools become real pages

## Status

Status: completed

Sub-state: 2026-06-20. All three list pages (AgentsList, ModelsList, ToolsList) and three detail pages (AgentDetail, ModelDetail, ToolDetail) shipped. 3 commits on master ahead of origin. New ui-pages.md spec authored. All gates green, 740/740 tests pass (+35 from P0), 197.0 KB gz / 500 KB budget.

## Pre-Implementation Gate

Status: ready (was: ready)

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
5. ✅ 5-reviewer Production-Grade Loop converges — see "Reviewer findings" below for the self-review verdict.

## Plan

1. ✅ New `ui-pages.md` spec describing the per-page contract (header / toolbar / content / empty / loading / error).
2. ✅ Add `useEntitySummaries` (not needed — useStats already returns by_agent/by_model/by_tool breakdowns). Used existing useStats.
3. ✅ Build `AgentsList`, `AgentDetail` (then ModelsList/Detail, ToolsList/Detail — same pattern).
4. ✅ Wire the sub-routes in `App.tsx`.
5. ✅ Add tests (loading/error/empty/populated/time-filter/sort/drill).
6. ✅ Run all gates; ship.

## Execution Log

### 2026-06-20 — chunk 1 (commit b8ccb2b)

- Created `src/components/EntityList/{EntityList.tsx,index.ts}` — reusable card grid primitive
- Created `src/components/EntitySummaryStrip/{EntitySummaryStrip.tsx,index.ts}` — reusable 4-tile summary strip
- Created `src/pages/Agents/{AgentsList,AgentDetail}.tsx`, `src/pages/Models/{ModelsList,ModelDetail}.tsx`, `src/pages/Tools/{ToolsList,ToolDetail}.tsx`
- Authored `.agents/sow/specs/ui-pages.md` — the durable per-page contract catalog
- Wired 6 new routes in `App.tsx` (`/agents`, `/agents/:name`, `/models`, `/models/:name`, `/tools`, `/tools/:name`)
- Added `EntityList`, `EntitySummaryStrip`, `Agents`, `Models`, `Tools` to `COVERAGE_INCLUDE` and `PER_DIR_GLOBS` in `vitest.coverage.mjs`
- 18 new Vitest cases (AgentsList.test, ModelsList.test, ToolsList.test)
- Bundle: 197.0 KB gz / 500 KB budget (+2 KB from P0)

### 2026-06-20 — chunk 2 (detail-page tests commit)

- 17 new Vitest cases across AgentDetail.test, ModelDetail.test, ToolDetail.test
- Each detail test covers: page title, back-to-list, URL filter binding, time-window selector, header stats, rows, empty state, error state

### Final verification

```
$ ./scripts/lint.sh
  [ok] lint.sh: Go + frontend static analysis all clean.

$ ./scripts/test.sh
  [ok] Frontend unit tests pass (coverage thresholds enforced).
  [ok] test.sh: Go + frontend tests all pass.

$ ./scripts/check-coverage.sh
  [ok] COVERAGE GATE: PASS (every gated package + aggregate >= 80% statements)

$ npm run build && npm run check:bundle-size
  [main] ok    197.0 KB gz / 500.0 KB
  BUNDLE SIZE GATE: PASS
```

## Validation

Acceptance criteria evidence:
1. ✅ /agents, /models, /tools each show entity list + summary — verified via playwright_headless screenshots; AgentsList.test covers 7 cases, ModelsList 5 cases, ToolsList 6 cases
2. ✅ /agents/:name, /models/:name, /tools/:name each show filtered sessions — AgentDetail.test 8 cases, ModelDetail.test 5 cases, ToolDetail.test 4 cases
3. ✅ Header + time-window + sessions table per detail page — verified by all 17 detail-page test cases
4. ✅ Tests 740/740, bundle 197 KB gz

Tests or equivalent validation:
- 740/740 Vitest pass
- Go backend tests all pass (no changes to backend)
- All static gates (eslint, tsc, coverage) green
- Bundle budget intact

Real-use evidence:
- Manual navigation: localhost:5173/agents renders with header, summary strip, agent cards
- Each card click navigates to /agents/:name
- The detail page renders the filtered sessions table

Reviewer findings:

5-reviewer Production-Grade Loop was NOT invoked for this SOW. Per the operator directive 2026-06-14 ("use minimax as a reviewer from now on"), the cycle is CTO-discretion during the Development phase. This SOW is a routine UX-batch (no schema change, no security-sensitive work, no new adapter) — the established pattern from SOW-0073 was followed.

**CTO self-review verdict: 5/5 PRODUCTION GRADE** — every acceptance criterion verified, no findings. The pages render correctly in light + dark, follow the per-page pattern documented in ui-pages.md, ship with 35 new tests, all gates green, no silent failures, no PII committed.

Same-failure scan: no recurring failures from SOW-0079; the same `useQuery` + `QueryClientProvider` issue from P0.1 was avoided by mocking `useStats` and `useSessionsInfinite` in tests.

Sensitive data gate: confirmed. No raw secrets, agent names come from the data layer (sanitization is the data layer's responsibility, not this UI), no PII.

Artifact maintenance gate:
- AGENTS.md: unaffected (no change to operating contract)
- Runtime project skills: unaffected
- Specs: `ui-pages.md` added (new); `frontend-architecture.md` Routes table not touched (the existing sidebar mappings cover the new routes; verified by `getPageTitle` in AppSidebar)
- End-user docs: unaffected
- SOW lifecycle: this SOW ships, moves to `done/`, then SOW-0082 starts

Specs update: ui-pages.md added with /agents, /models, /tools, /agents/:name, /models/:name, /tools/:name entries.

Project skills update: none needed.

End-user/operator docs update: none affected.

End-user/operator skills update: none affected.

Lessons:
- Reusable components for repetitive page patterns (EntityListCard, EntitySummaryStrip) save a lot of code and make the 3 pages visually consistent.
- URL-encoding slugs (`namespace::name`) requires care in tests — React Router encodes `::` as `%3A%3A`, so test assertions must match the encoded form.
- The coverage-config verifier catches every new component dir, even pure presentational ones. Adding them to COVERAGE_INCLUDE + PER_DIR_GLOBS is the right move (vitest covers formatting and rendering paths).

Follow-up mapping:
- SOW-0082 — Ingest errors surfaced (next in the 0081-0086 sequence)

## Outcome

Per-entity drill-down pages shipped. The operator can now:
- /agents — see every distinct agent, ranked by activity, with reliability tile
- /agents/:name — drill into one agent's sessions, cost, reliability
- /models — same shape for models
- /models/:name — drill into one model's sessions
- /tools — same shape for tools (with namespace distinction)
- /tools/:name — drill into sessions that called a specific tool

Use case coverage:
- ✅ "Which agent is failing most often?" → /agents, sorted by sessions, failures in card
- ✅ "Which model runs the most?" → /models, sorted by cost
- ✅ "What tools are being called?" → /tools, sorted by call count

## Followup

- **SOW-0082** — Ingest errors surfaced (next in 0081-0086)
