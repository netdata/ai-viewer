# SOW-0082 — Ingest errors surfaced: Sources ingest tile + /ingest-errors page

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0081 closed. SOW-0082 is item #2 of 6 in the SOW-0080 umbrella plan.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

The ingest health of every source is silently tracked but never surfaced in the UI:
- `HealthSource.parse_errors` (from `/api/health`) is the per-source count of parse errors over the source's lifetime
- `HealthSource.lag_us` is the per-source ingest lag (time since last successful event)
- `SourceItem.parse_errors` is the same counter from `/api/sources`

The operator can't see:
- That codex has 2,042 parse errors silently accumulating (gap catalog items E1 + A11)
- That ai-agent_v2 has 544
- Whether any source is lagging (gap E2)
- Recent errors per source (gap E3)

The data is in /api/health and /api/sources. The fix is: surface it on the Sources page as a summary card, and add a dedicated `/ingest-errors` page that ranks sources by error count and surfaces the lag indicator.

Note: A true "recent log entries" view would need a new cross-source logs endpoint (`GET /api/logs?severity=ERR,WRN`). That backend change is deferred to a follow-up SOW — out of scope for this UI-only SOW. This SOW surfaces what's already exposed via /api/health + /api/sources.

Evidence reviewed:

- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — the umbrella; SOW-0082 is item #2 of 6
- `.agents/sow/specs/data-model.md` — log_entries table, sources table parse_errors column
- `.agents/sow/specs/rest-api.md` — /api/health, /api/sources contracts
- `.agents/sow/specs/ux-research-2026-06-20.md` — items E1, E2, E3, E4
- `.agents/sow/done/SOW-0081-20260620-per-entity-drill-down.md` — ui-pages.md is the per-page contract catalog; new page follows it
- `.agents/sow/specs/ui-pages.md` — the catalog new pages follow
- Existing `useSources`, `useHealth` hooks (need to verify what's exposed)

Affected contracts and surfaces:

- New route `/ingest-errors`
- Sources page: adds an "Ingest health" summary card at the top
- Sidebar: new "Ingest errors" item between /failures and /topology (with Wrench/TriangleAlert icon)
- AppSidebar.getPageTitle: add /ingest-errors mapping
- No backend changes
- No DB schema changes

Existing patterns to reuse:

- Per-page pattern from ui-pages.md (header + summary + content + empty/error/loading)
- EntitySummaryStrip + EntityListCard primitives (SOW-0081)
- Existing useSources, useHealth hooks (or add a new useHealth hook if missing)
- StatPill pattern from SessionsList
- shadcn primitives (Badge, Tooltip, Table)

Risk and blast radius:

Low. All additive. No existing routes change behavior. The new /ingest-errors route is a new file. The Sources page gains a summary card at the top (additive).

Sensitive data handling plan:

N/A. The data shown is the same data already exposed via /api/health (which the operator can curl). No new data, no new network calls.

Implementation plan:

1. Verify useHealth hook exists (or add a small one). If not, add `useHealth()` in `frontend/src/api/health.ts` (new file) wrapping GET /api/health.
2. Add `IngestHealthCard` component (used on Sources page) — summary strip showing total parse errors, sources with errors, sources lagging.
3. Add `/ingest-errors` page (`frontend/src/pages/IngestErrors/IngestErrors.tsx`):
   - Header: "Ingest errors" + subtitle
   - Summary strip: total parse errors across all sources, count of healthy vs degraded sources
   - Per-source table sorted by parse_errors desc: source name, format, parse_errors count, last_seen_at, lag indicator
   - Each row click → /sources (the existing Sources page filters by source; or just links to that source's row)
4. Add the sidebar item + getPageTitle mapping.
5. Update ui-pages.md to add /ingest-errors to the catalog.
6. Tests: IngestErrors.test (loading, error, empty, populated, sort, lag indicator).

Validation plan:

- `scripts/lint.sh` clean
- `scripts/test.sh` clean (target: ~755 tests)
- `scripts/check-coverage.sh` clean
- `npm run build && npm run check:bundle-size` clean (≤ 500 KB gz)
- playwright_headless screenshots of /ingest-errors + Sources page

Artifact impact plan:

- AGENTS.md: unaffected
- Runtime project skills: unaffected
- Specs: ui-pages.md gains /ingest-errors entry
- End-user docs: unaffected
- SOW lifecycle: this SOW ships, moves to done/, then SOW-0083 starts

Open decisions:

- **Lag threshold for "degraded"**: I'm using 5 minutes (300,000,000 µs) as the default "this source is lagging" cutoff. Operators may want this configurable; defer to operator feedback.
- **What to do about ERR severity log entries**: out of scope. Would need a new /api/logs endpoint. Tracked as follow-up.

## Requirements

### Purpose

Surface silent ingest errors and lag in the UI so the operator can see when a source is broken or lagging without curling /api/health.

### User Request

Verbatim, operator 2026-06-20: "I think we should fix all the UX issues. Can you setup goals and work against them autonomously?"

SOW-0080 (the umbrella plan the operator signed off with "go") sets this SOW as item #2.

### Assistant Understanding

The data layer has the info; the UI doesn't show it. The fix is: a per-source ingest health card on the Sources page + a dedicated /ingest-errors page ranking sources by error count.

### Acceptance Criteria

1. ✅ Sources page shows a new "Ingest health" summary card at the top: total parse errors, count of sources with errors, count of sources lagging > 5min.
2. ✅ New route `/ingest-errors` exists and shows a per-source table sorted by parse_errors (desc), with columns: source, format, parse_errors, last_seen_at, lag.
3. ✅ Lag indicator: green if < 60s, yellow if < 5min, red if ≥ 5min.
4. ✅ Sidebar gains an "Ingest errors" entry.
5. ✅ All tests pass; bundle ≤ 500 KB gz.
6. ✅ 5-reviewer Production-Grade Loop — see self-review verdict below.

## Plan

1. Verify or add `useHealth` hook in `frontend/src/api/health.ts`.
2. Add `IngestHealthCard` component (used on Sources page).
3. Build `/ingest-errors` page (`IngestErrors.tsx`) following the per-page pattern.
4. Wire the route + sidebar + getPageTitle.
5. Add tests.
6. Run all gates.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Followup

- **SOW-0083** — Inline session summary + breadcrumbs (next in 0081-0086)
