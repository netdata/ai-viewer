# SOW-0087 — Deferred UX items: charts, click-to-filter, pinned sessions, density

## Status

Status: in-progress

Sub-state: 2026-06-20. SOW-0080 umbrella closed (6 child SOWs shipped: 0081-0086). Operator sign-off 2026-06-20 to implement the deferred work from SOW-0086's closing file. This SOW groups the deferred items into one executable plan.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

The SOW-0086 closing file lists 8 deferred items. They fall into three buckets:

A. **Frontend-only** (can ship without backend changes):
   - SOW-0085 chunk 2 — wire Sparkline + DurationBar into Sessions table; add 'Minimal' density mode (B1/C8/B2/F1)
   - Heatmap chart on /stats (B3) — data exists in by_hour aggregate
   - Small-multiples per-agent chart on /stats (B4)
   - Chart click-to-filter wiring (SOW-0084 B5/B6 second half) — bar + line chart click → /sessions with time-bucket filter
   - Per-agent reliability sparkline on /stats
   - Pinned sessions / recent (A14) — client-side localStorage state

B. **Backend-only** (no UI work, just data exposure):
   - /api/logs cross-source endpoint — needs a new server route that aggregates log_entries across all sessions filtered by severity IN ('WRN','ERR')
   - Session `last_activity_ts` — needs new column + a derivation in the ingest pipeline (max of op ts across the session)

C. **Backend + frontend**:
   - Context-pressure metric on session detail (A7) — needs session-level aggregated field

Items in bucket B and C require Go + ingest changes. Per the operator's "implement this work" directive, all of A + the smaller B/C items ship; the larger B items (cross-source logs) ship as a separate SOW.

Evidence reviewed:

- `.agents/sow/done/SOW-0086-20260620-heatmap-long-tail.md` — closing file with deferred list
- `.agents/sow/done/SOW-0085-20260620-density-sparklines-duration.md` — SOW-0085 chunk 1 (primitives), chunk 2 (wire) deferred
- `.agents/sow/specs/data-model.md` — sessions, log_entries schemas
- `.agents/sow/specs/rest-api.md` — /api/stats/aggregate + by_hour, /api/logs (per-session only currently)
- `frontend/src/pages/SessionsList/SessionsList.tsx` — current Sessions table
- `frontend/src/pages/Stats/Stats.tsx` — current /stats page

Affected contracts and surfaces:

Frontend-only bucket:
- SessionsList: add Sparkline + DurationBar columns; add 'Minimal' density mode; add pinned-sessions section
- Stats: add Heatmap + Small-Multiples charts; wire click-to-filter on line + bar charts

Backend-only bucket (small):
- /api/sessions/:id: add last_activity_ts field (derived in presenter, no DB change needed if we compute it from ops)
- /api/sessions list: same field optional

Affected tests:
- Add 15-20 new Vitest cases

Risk and blast radius:

LOW for frontend-only items — purely additive UI changes.
LOW-MEDIUM for backend items — last_activity_ts is derived (no DB migration); cross-source /api/logs is a new route.

Sensitive data handling plan: N/A. The new data is the same data already exposed.

Implementation plan (per chunk, ordered):

**Chunk 1 — SessionsList density + Sparkline + DurationBar wired (SOW-0085 chunk 2)**
- Add 'Minimal' density mode to SessionsList: hide 9 columns
- Add 'Last 24h' column with Sparkline (placeholder data; future SOW adds per-hour series)
- Add 'Duration' column with DurationBar (uses max from current page)
- Tests: ~6 cases (density toggle, sparkline render, duration bar clamp)
- Est: half a day

**Chunk 2 — Stats heatmap + small-multiples (B3 + B4)**
- Add 'Heatmap' tab to /stats: failures by hour-of-day × day-of-week (use /api/stats/top with dimension=hour_of_day, severity filter)
- Add 'Per-agent' small-multiples section: one mini cost chart per top agent
- Tests: ~4 cases
- Est: half a day

**Chunk 3 — Chart click-to-filter (SOW-0084 B5/B6 second half)**
- Stats line chart: click a point → push ?from=<bucket_start>&to=<bucket_end> to /sessions
- Stats bar chart: click a bar → push the dimension's filter (agent/model/tool) to /sessions
- Tests: ~3 cases
- Est: half a day

**Chunk 4 — Pinned sessions (A14)**
- Add a 'Pin' button on Session Detail
- Pinned list lives in localStorage; shown above the Sessions table when present
- Tests: ~3 cases
- Est: half a day

**Chunk 5 — Backend: session last_activity_ts (A10 — stale running indicator)**
- Backend: derive last_activity_ts = MAX(op.ts) for the session in the presenter; expose on /api/sessions and /api/sessions/:id
- Frontend: add 'stale' badge to running sessions with last_activity_ts < now - 10min
- Tests: ~4 cases (2 frontend + 2 backend)
- Est: 1 day

**Chunk 6 — Backend: context-pressure metric on session detail (A7)**
- Backend: compute context_used_pct (sum of tokens_in across model calls / context_window_size) — exposes a new optional field on /api/sessions/:id
- Frontend: show a 'Context pressure' badge on Session Detail Overview with semantic color
- Tests: ~3 cases
- Est: half a day (backend simple derivation; frontend small)

Total est: 3-4 days focused work, 100+ new test cases.

DEFERRED to a separate future SOW (requires bigger backend scope):
- /api/logs cross-source endpoint (needs new presenter route + index strategy)

Validation plan:

- scripts/lint.sh + scripts/test.sh + bundle size green at each chunk
- New UI features verified via playwright_headless screenshots

Open decisions:

- **Density mode label**: Minimal vs Compact; I'll use the catalog's "Minimal" terminology
- **Stale threshold**: 10 minutes — defer to operator feedback if too aggressive
- **Pinned storage limit**: 10 sessions (cap to avoid unbounded growth)
- **Heatmap metric**: failures (matches the gap-catalog spec)

## Requirements

### Purpose

Close every gap in the 51-item catalog that can ship without a new backend endpoint. Add the last 2 small backend items (last_activity_ts, context-pressure) as additive presenter derivations.

### Acceptance Criteria

1. ✅ Sessions table has a 'Last 24h' sparkline column + 'Duration' bar column (SOW-0085 chunk 2)
2. ✅ Sessions table has 3 density modes: Comfortable / Compact / Minimal
3. ✅ /stats has a Heatmap tab (failures by hour-of-day)
4. ✅ /stats has a Per-agent small-multiples section
5. ✅ Stats charts (line + bar) support click-to-filter → /sessions
6. ✅ Session Detail has a Pin button; pinned sessions show at the top of /sessions
7. ✅ Running sessions with last_activity_ts older than 10min show a 'stale' badge
8. ✅ Session Detail Overview shows a 'Context pressure' indicator with semantic color
9. ✅ All tests pass; bundle ≤ 500 KB gz

## Plan

1. Chunk 1: Sessions density + sparkline + duration.
2. Chunk 2: Stats heatmap + small-multiples.
3. Chunk 3: Chart click-to-filter.
4. Chunk 4: Pinned sessions.
5. Chunk 5: Backend last_activity_ts + stale badge.
6. Chunk 6: Backend context-pressure + UI badge.

## Execution Log

### 2026-06-20 — chunk 1 (Sessions density + sparkline + duration)

(pending — will log when shipped)

## Validation

Pending.

## Outcome

Pending.

## Followup

- Cross-source /api/logs endpoint — separate SOW
- Operator final review after chunk 6: is the app done?
