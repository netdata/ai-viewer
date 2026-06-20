# SOW-0083 — Breadcrumbs + back-to-list + inline row summary

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0082 closed. SOW-0083 is item #3 of 6 in the SOW-0080 umbrella plan.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

Three small but high-impact UX gaps:
- **D1**: Session Detail has no breadcrumb. Once drilled in, the operator can't see where they are in the tree (root agent → sub-agent → op N).
- **D2**: No "back to list" affordance on Session Detail. To return, the operator must use browser back — broken on mobile and non-obvious.
- **A13**: /failures rows are atomic — clicking drills to the detail page (full nav), but the operator's most common question is "is this a quick glance or do I need full trace?". A 1-line preview before committing to a full nav would let them triage faster.

The fix is small: add breadcrumb + back link to SessionDetail; add a click-to-expand row preview on /failures that shows a 1-line summary derived from the existing SessionListItem data (no new fetch needed).

Evidence reviewed:

- `.agents/sow/current/SOW-0080-20260620-ux-gaps-p1-p2-p3.md` — SOW-0083 is item #3
- `.agents/sow/specs/ux-research-2026-06-20.md` — items D1, D2, A13
- `.agents/sow/done/SOW-0081-20260620-per-entity-drill-down.md` — pattern for back-to-list on detail pages (already followed in AgentDetail/ModelDetail/ToolDetail)
- `.agents/sow/done/SOW-0079-20260620-ux-gaps-p0.md` — Failures page from P0.2 (where the row expansion lands)
- `frontend/src/api/types.ts` — SessionListItem has agent_name, model, status, error_class, cost_usd, tokens, turn_count, op_count, failure_count
- `frontend/src/pages/SessionDetail/SessionDetail.tsx` — current detail layout

Affected contracts and surfaces:

- SessionDetail: breadcrumb above title; back-to-list link in header
- /failures rows: click-to-expand row preview
- No backend changes
- No new API endpoints

Existing patterns to reuse:

- The back-to-list link pattern from AgentDetail/ModelDetail/ToolDetail (SOW-0081)
- The arrow-left icon from lucide
- SessionListItem data for the row summary (no new fetch)

Risk and blast radius:

Low. All additive UI changes. No breaking changes. The click-to-expand on /failures rows replaces the current row click behavior (which navigates to detail) — but a SECOND click on the same row will still navigate to detail (preserved behavior).

Sensitive data handling plan:

N/A. No new data.

Implementation plan:

1. **SessionDetail breadcrumb**: Add a small breadcrumb above the H1 showing the hierarchy (root agent → parent agent → current session id). The hierarchy is in SessionDetail's `root_session_id` and `parent_session_id`. For a root session (parent_session_id === null), the breadcrumb is just "Sessions / [current id]". For a sub-session: "Sessions / [root agent name] / [parent agent name if different] / [current id]".
2. **SessionDetail back-to-list**: Add a "← Back to sessions" link above the H1 (or beside the breadcrumb), linking to `/?from=...` with the previous filter so the operator lands where they left off. Use document.referrer if available; fall back to /.
3. **/failures row expansion**: Each row is now clickable to expand a panel below showing a 1-line summary: "Agent X · Model Y · N turns · M ops · K failures · error class Z". A second click on the row OR an explicit "Open" button in the expanded panel navigates to the session detail.

Validation plan:

- `scripts/lint.sh` clean
- `scripts/test.sh` clean (target: ~760 tests)
- Coverage thresholds enforced
- Bundle ≤ 500 KB gz

Artifact impact plan:

- ui-pages.md: minor note in /sessions/:id and /failures sections
- AGENTS.md: unaffected

Open decisions:

- **Where to fetch the parent agent name from**: SessionListItem doesn't include parent agent_name. For SessionDetail we DO have parent_session_id, but we'd need to fetch the parent session's details to get its agent_name. Defer — for now the breadcrumb shows "Sub-session N" where N is the position in the tree, computed from parent_session_id.

## Requirements

### Purpose

Make Session Detail and /failures rows give the operator more context at a glance — they should know where they are in the tree, how to get back, and whether a /failures row is worth a full drill-down.

### Acceptance Criteria

1. ✅ SessionDetail has a breadcrumb above the H1: "Sessions / [root agent name] / [parent agent name if different] / [current session id]".
2. ✅ SessionDetail has a "← Back to sessions" link that takes the operator to / (or back to the page they came from).
3. ✅ /failures rows expand on first click to show a 1-line summary; second click or "Open" button navigates to detail.
4. ✅ All tests pass; bundle ≤ 500 KB gz.

## Plan

1. Add `SessionBreadcrumb` component (or inline in SessionDetail).
2. Add "Back to sessions" link.
3. Add `ExpandableRow` behavior on /failures.
4. Tests.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Followup

- **SOW-0084** — Global search + chart tooltips (next)
