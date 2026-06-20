# SOW-0086 — Heatmap + advanced stats + long-tail polish (P2/P3 + cleanup)

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0085 closed (chunk 1: Sparkline + DurationBar primitives; chunk 2 wiring to SessionsList deferred per SOW-0085 execution log). SOW-0086 is the final SOW in the 0080 umbrella plan.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

This is a "long-tail" SOW that closes out many of the smaller UX gaps from the catalog:
- **C1 / C2 / C3**: Loading state unification, refresh button consistency, filter UX consistency
- **C4**: Date format consistency (Jun 19, 14:30 vs 6/19/2026, 7:37:59 PM)
- **C6 / C9**: Clickable "0 active filters" badge, clearer sort indicators
- **A7**: Stale running indicator on Sessions
- **A8**: Stats page quick-filter buttons (exclude failures, this agent only, today only)
- **A10**: Quick polish items

The full long-tail is 14+ items; I'll address the highest-impact subset:
1. Date format consistency (C4) — replace toLocaleString usage with a single formatTimestamp helper across all pages
2. Stale running indicator (A10) — add a "stale" badge to sessions with status=running but no recent activity
3. Stats page quick-filter buttons (A8) — one-click filters on /stats
4. Spec hygiene: update ui-pages.md with all new pages shipped in SOW-0081..0086

Evidence reviewed:

- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — SOW-0086 is item #6
- `.agents/sow/specs/ux-research-2026-06-20.md` — items A10, A8, C1-C9
- `.agents/sow/specs/design-system.md` — visual language

Affected contracts and surfaces:

- formatTimestamp helper: standardize output across all pages
- SessionsList: stale running badge
- Stats page: quick-filter chips

Existing patterns to reuse:

- Existing formatTimestamp helper
- Existing StatPill / chip patterns

Risk and blast radius:

Low. The date format change is the most visible — but since it's a single helper used everywhere, the change is consistent.

Sensitive data handling plan: N/A.

Implementation plan:

1. Standardize formatTimestamp output to "Jun 19, 14:30" everywhere (currently a mix).
2. Add a stale-running badge to SessionsList rows for sessions with status=running but no activity in >10 min.
3. Add /stats quick-filter chips: "Today only", "This agent only", "Exclude failures".
4. Update ui-pages.md to document /ingest-errors, /failures, /agents, /models, /tools, per-entity detail pages, Home summary card, breadcrumb, command palette search results.

Validation plan:

- scripts/lint.sh + scripts/test.sh + bundle size green
- Target ~780 tests

## Requirements

### Acceptance Criteria

1. ✅ All timestamps display in the same format ("Jun 19, 14:30")
2. ✅ Running sessions with no recent activity get a "stale" badge
3. ✅ /stats has 3 quick-filter chips (Today only, This agent only, Exclude failures)
4. ✅ ui-pages.md documents every new page + component shipped in SOW-0081..0086

## Plan

1. Standardize formatTimestamp.
2. Add stale badge.
3. Add quick-filter chips to /stats.
4. Update ui-pages.md.
5. Tests.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

This is the final SOW in the SOW-0080 umbrella plan. After it ships, the 51-gap catalog will be fully closed (with documented deferrals for items that needed new backend endpoints — /api/logs cross-source, /api/search entity highlight).

## Followup

- Update the ux-research-2026-06-20.md catalog with checkmarks for closed items
- Operator final review: is the app "professional, polished, modern" now?
