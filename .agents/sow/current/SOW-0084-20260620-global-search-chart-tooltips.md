# SOW-0084 — Global search + interactive chart tooltips

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0083 closed. SOW-0084 is item #4 of 6.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

Two related UX gaps:
- **D4**: ⌘K opens the command palette (SOW-0074.1) but only navigates to known routes — it doesn't search. The operator can't type "agent-alpha" or "rate_limit" and jump to the matching session or to the /failures filtered by that error class.
- **B5/B6**: The /stats charts show tooltips with just the bucket title on hover. The operator can't see "this bucket: $X cost, Y tokens, Z failures" or click the bucket to filter the sessions list.

Both are high-impact for the AI-agent builder persona (gap catalog P2). The data exists: `/api/search?q=...` already does FTS5 over ops + logs (used by the existing /stats deep-search box). `/api/stats/aggregate` already returns the per-bucket values the tooltips need.

Evidence reviewed:

- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — SOW-0084 is item #4
- `.agents/sow/specs/ux-research-2026-06-20.md` — items A2, A3-fu, D4, B5, B6
- `frontend/src/components/Layout/CommandPalette.tsx` — current palette (routes-only)
- `frontend/src/api/stats.ts` — useSearch / fetchSearch hook (already exists)
- `.agents/sow/specs/rest-api.md` — `/api/search` and `/api/stats/aggregate` contracts

Affected contracts and surfaces:

- CommandPalette: add inline search results section above route results
- /stats charts (line + bar): replace the title-only tooltip with a multi-row breakdown; clicking a bar/point filters the sessions list to that time bucket
- No backend changes
- The new search behavior uses the existing `/api/search` endpoint

Existing patterns to reuse:

- useSearch hook (api/stats.ts)
- The /stats deep-search box pattern (State deep-search input + results dropdown)
- Recharts / d3 tooltip pattern in `frontend/src/pages/Stats/charts/`

Risk and blast radius:

Low. All additive UI changes. The CommandPalette extension is opt-in (only shows search results when q has non-whitespace content). The chart tooltip change is purely additive (more info in the same hover).

Sensitive data handling plan:

N/A. The search endpoint returns ranked ops/logs that the operator already has access to via the existing /stats deep search.

Implementation plan:

1. Extend CommandPalette to fetch `/api/search?q=...` when q has content; show the top N results in a "Results" section above the route results.
2. Each result is a row with: kind (op/log), session id, snippet, timestamp. Click → /sessions/:id with the op highlighted (deferred — for now just navigate).
3. Stats chart tooltips: update the line chart + bar chart to render a richer tooltip on hover showing all per-bucket values.
4. Click-to-filter: clicking a bar/point on the stats charts pushes a `?from=<bucket_start>&to=<bucket_end>` to the Sessions page via a `Link` or window.location.

Validation plan:

- scripts/lint.sh + scripts/test.sh + bundle size all green
- Target ~770 tests

Open decisions:

- **Search result snippet**: show context (15 chars before/after the match) — FTS5 snippet() already returns this. Truncate in the UI.
- **Click-to-filter scope**: only the bar chart (which is grouped by entity) for now. The line chart's click would filter by time bucket, which is already possible via the time-window selector.

## Requirements

### Purpose

Make ⌘K a real search (not just a route navigator) and make /stats charts interactive.

### Acceptance Criteria

1. ✅ CommandPalette shows top N /api/search results when the query has non-whitespace content.
2. ✅ Each result links to /sessions/:id.
3. ✅ /stats charts show a richer tooltip with the bucket's per-metric values on hover.
4. ✅ Clicking a bar on the /stats bar chart navigates to /sessions with the time-bucket filter applied.

## Plan

1. Extend CommandPalette with search.
2. Update chart tooltips.
3. Wire bar-chart click → filtered sessions.
4. Tests.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Followup

- **SOW-0085** — Density + sparklines + duration bars (next)
