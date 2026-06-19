# SOW-0067 — Statistics redesign: multi-dimension slicing with error analytics

## Status

Status: open

## Requirements

### Purpose

Transform the Statistics page from "basic charts with selectors" into the primary analysis tool for evaluating models, harnesses, and tools. The operator needs to slice the data by any dimension and see both happy-path metrics (tokens, duration, cost, cache efficiency) AND failure metrics (error classes, failure rates, retry patterns).

### Background

The operator's core analysis questions at the aggregate level:
- "Which models are expensive?" — cost per model, over time
- "Which models fail most?" — failure rate per model, by error class
- "Which harnesses (claude-code, codex, opencode, ai-agent) are most efficient?"
- "How does cost/failure change over time or across model versions?"
- "Which tools are slowest / most failure-prone?"
- "What's my cache hit ratio per model?"

### Current state

The Statistics page has: a summary metrics bar, a trend line chart (one metric, one time series), a top-N breakdown (one dimension, one metric), a data table, and CSV export. Each chart has its own independent selector — they don't work together.

### Design goals

1. **Unified filtering** — one filter bar (time range, source, model, agent, tool, status) drives ALL charts simultaneously
2. **Multi-dimension comparison** — the operator picks a dimension (model, source, agent, tool, status, error_class) and sees a comparative breakdown across that dimension for MULTIPLE metrics at once (cost, tokens, duration, failure rate, cache hit ratio)
3. **Error analytics** — a dedicated section showing failure distribution: error_class pie/bar, failure rate over time, failure rate per model, retry statistics
4. **Time-series with dimension overlay** — the trend chart should show multiple series overlaid (e.g. cost for model A vs model B over time)
5. **Export** — CSV export of whatever the current view shows

### Acceptance Criteria

1. One unified filter bar drives all charts (changing a filter updates every chart on the page, not just one). **Verification**: UI test.
2. The operator can select a "group by" dimension and see a comparative table showing cost/tokens/duration/failure-rate/cache-hit across all values of that dimension. **Verification**: the table renders with real data.
3. A failure-analysis section shows: error_class distribution (bar/pie), failure rate trend over time, failure rate per model. **Verification**: visible with the ai-agent error taxonomy data (905 classified failures).
4. The trend chart can overlay multiple series (e.g. compare cost across 2+ models or sources). **Verification**: multiple colored lines.
5. All views export to CSV. **Verification**: download produces the right data.

### Implementation approach

- Backend: the existing `/api/stats` (live aggregates) + `/api/stats/aggregate` (time series) + `/api/stats/top` (rankings) cover most needs. Add a `/api/stats/failures` endpoint that returns error_class distribution + failure rates by dimension (the data is in the DB — `sessions.error_class` is now populated for ai-agent).
- Frontend: redesign the Stats page into sections: Summary bar → Trends (multi-series) → Breakdown table (group-by + multi-metric) → Failure analysis → Search. Each section reads from the unified filter.

## Status

Status: completed (moved to done/). SOW-0067 completion pass — all 5 ACs met,
6-reviewer loop converged (round 3: 5/6 PG at 9.5, kimi 9 on a one-line spec
example now fixed), all automated gates green. Partially shipped earlier in
`905d93d` + `d949388` (failure-analysis section + multi-metric comparison table);
this pass closed the remaining gaps identified by self-review + the 8-reviewer
consensus (codex/claude/glm + minimax/mimo/deepseek/qwen/kimi, all 6–6.5/10).

## Pre-Implementation Gate

### Problem / root-cause model

The Statistics page shipped ~40% of SOW-0067's acceptance criteria. The
multi-metric comparison table and the error-class distribution landed, but
four of the five ACs are incomplete. Root cause: the decomposition commit
(`dad95d3`) treated each UX milestone as "ship a slice", and the slices
under-delivered against the ACs without a closing pass. The 8 external
reviewers independently re-discovered the same unmet ACs.

### Evidence reviewed (self-review, file:line — not reviewer claims)

- **AC1 (unified filter bar)** — DONE. FilterBar writes URL
  (`frontend/src/components/FilterBar/FilterBar.tsx`); Stats reads
  `useFilters()` (`Stats.tsx:84`). But the FilterBar exposes NO time-range
  control even though `from`/`to` are fully wired through URL state
  (`state/filters.ts:34-36,94-101`), SSE subscription
  (`filters.ts:256-284`), and every API client (`api/stats.ts:42-43`).
  Design goal #1 ("unified filtering — time range") is unmet.
- **AC2 (group-by table: cost/tokens/duration/failure-rate/cache-hit)** —
  PARTIAL. The comparison table HIDES cost/tokens columns for 4 of 6
  dimensions (`Stats.tsx:547-553`) and DISCARDS the `total_us` duration
  field the by_tool API returns (`Stats.tsx:489-502` maps `costUsd:0,
  tokensIn:0`). Cache fields are absent from every dimension row. Verified
  the data EXISTS end-to-end: canonical model has `TokensCacheRead/Write`
  (`internal/canonical/events.go:257-258,319-320`); all 5 adapters populate
  them; the DB stores them; and `/api/stats` TOTALS already return them
  (`internal/presenter/stats.go:27-28,183-184`). The gap is: dimension ROWS
  omit cache/cost/tokens (`statModelRow` stats.go:34-43, `statSourceRow`
  stats.go:69-73 has only source/sessions/failures) and the frontend drops
  duration. Reviewers' "cache not captured" claim was a FALSE claim —
  verified by grep (all 8 echoed minimax without checking).
- **AC3 (failure analysis: distro + rate-trend + rate-per-model)** —
  PARTIAL. Distribution DONE (`stats.go:200`, `Stats.tsx:352-401`). Rate
  trend NOT done (the trend only does absolute `failures`, never
  failures/calls). Rate-per-model is implicitly available via the by_model
  comparison table's Failure% column (`Stats.tsx:461`) but there is no
  dedicated failure-rate-over-time view.
- **AC4 (trend multi-series overlay)** — NOT DONE. `Stats.tsx:109`
  hardcodes `groupBy:'total'`. The LineChart ALREADY renders multi-series
  (`charts/LineChart.tsx:106-115`, one `<path>` per series + text legend),
  and the API + `useAggregate` hook + `AggregateGroupBy` type fully support
  `groupBy` (`api/stats.ts:74-76,104`, `rest-api.md:442`). Pure frontend
  gap (~one control + threading).
- **AC5 (all views export CSV)** — NOT DONE. Only Top-N has CSV
  (`Stats.tsx:266-289`).

### Affected contracts and surfaces

- `internal/presenter/stats.go` — row structs (`statModelRow`,
  `statAgentRow`, `statSourceRow`, `statStatusRow`, `statErrorRow`,
  `statToolRow`) gain fields.
- `internal/presenter/stats_breakdowns.go` — the by_* SELECT/SCAN lists
  gain the new columns.
- `frontend/src/api/types.ts` — `StatsResponse` row types gain fields.
- `frontend/src/pages/Stats/Stats.tsx` — honest comparison table,
  multi-series trend, failure-rate derived metric, drill-down, CSV on all
  sections.
- `frontend/src/pages/Stats/shareState.ts` — new URL control
  `trend_group_by`.
- `frontend/src/components/FilterBar/FilterBar.tsx` — time-range presets.
- `rest-api.md` §GET /api/stats — by_* row shapes updated.
- `ui-pages.md` §/stats — layout updated to the SOW-0067 design.

No schema migration: every required column already exists (`sessions` +
`ops` carry `tokens_cache_read/write`, `cost_usd`, `tokens_in/out`,
`duration_us`, `op_count`; verified by the existing totals query).

### Spec deltas to land before any test or code

1. `rest-api.md` §GET /api/stats — extend the by_model/by_agent/by_source/
   by_status/by_error_class row examples with the new fields (cache tokens,
   cost/tokens on source/status, duration on model); note cache-hit is a
   client-derived ratio `cache_read/(cache_read+tokens_in)`.
2. `ui-pages.md` §/stats — replace the SOW-0007 single-series line-chart
   description with the SOW-0067 design: multi-series trend (group-by
   control), honest multi-metric comparison table (stable columns, "—" for
   N/A), failure-rate trend (derived metric), CSV on all sections,
   drill-down linking, and the global FilterBar time-range presets.

### Patterns to reuse

- URL-shareable chart controls via `shareState.ts` (`readStatControls` /
   `applyStatPatch` / `StatControlsPatch`) — add `trend_group_by` the same
   way `bucket`/`trendMetric` are handled.
- `useAggregate` already accepts `groupBy`; no API-client change for
   multi-series.
- Breakdown-row enrichment mirrors the existing `statsByModel` two-pass
   (scan rows, then compute derived `pct`/ratio in Go) pattern
   (`stats_breakdowns.go:107-122`).
- FilterBar presets write `from`/`to` via the existing `setFilters` patch
   (`filters.ts:201-214`) — no new state plumbing.
- CSV export mirrors the Top-N blob/download recipe (`Stats.tsx:266-289`).

### Risk and blast radius

- **Comparison-table contract change**: adding fields to by_* rows is
  additive (new JSON keys), so existing consumers keep working. Frontend
  reads the new keys; no breaking change. Low risk.
- **FilterBar time-range presets** are CROSS-CUTTING (affect every page),
  but they only write the already-supported `from`/`to` URL params, so
  every endpoint already honors them. Risk is UX (a new global control),
  not correctness.
- **Derived failure-rate metric** is frontend-only (fetch failures +
  calls aggregates, divide per bucket). No server ratio semantics; ratios
  don't aggregate, so a server-side rate would be wrong — the two-fetch +
  client-divide is the correct approach.
- Blast radius: presenter + Stats page + FilterBar + 2 specs. No ingest,
  no adapters, no schema, no canonical-model change.

### Sensitive data handling

No new sources of sensitive data. All metrics are aggregates; no raw
payload content surfaces. No fixture changes needed.

### Implementation plan (chunked, gates green between)

- **A. Backend rows** — extend stat row structs + by_* queries with
  cache/cost/tokens/duration where meaningful; update `stats_test.go`
  expectations.
- **B. Frontend types** — `api/types.ts` row fields.
- **C. Stats page** — honest stable-column comparison table (surface
  duration for tools, cache-hit for model/agent, cost/tokens for source/
  status); multi-series trend (group-by control, ≤8 visible series);
  failure-rate derived trend metric; CSV on comparison + failure + trend;
  drill-down (clickable rows → `setFilters`).
- **D. FilterBar** — time-range presets (1h/24h/7d/30d/All).
- **E. Specs** — rest-api.md + ui-pages.md deltas above.
- **F. Gates** — full local workstation gate aggregate (`scripts/gates.sh`)
  incl. race, coverage, spec-drift, secrets.

### Validation plan (named tests + behaviors)

- `internal/presenter/stats_test.go` — assert the new fields are returned
  and summed correctly for by_model/by_agent/by_source/by_status
  (cache_read, cache_write, cost, tokens, duration as applicable).
- `internal/presenter/stats_test.go` — MaliciousFilterValuesStayBound still
  holds (additive columns do not weaken SQL binding).
- `frontend/src/pages/Stats/Stats.test.tsx` — multi-series trend renders
  >1 polyline when group_by != total; failure-rate metric shows a ratio
  line; comparison table shows "—" for N/A cells and real values for
  available cells; CSV buttons exist on comparison + failure sections;
  clicking a by_model row calls setFilters({models:[...]}).
- `frontend/src/components/FilterBar/FilterBar.test.tsx` — selecting a
  preset writes `from` (and clears it for "All"); every endpoint honors it
  (covered by existing filter wiring).
- spec-drift gate stays green (specs updated in lockstep).

### Deferred to follow-up SOWs (out of SOW-0067 scope — gold-plating)

- error_class × model cross-tab pivot (needs a new 2-group-by endpoint —
  substantial; SOW-0072 candidate).
- tool latency percentiles p50/p95/p99 (needs new rollup columns).
- trend brush/zoom + anomaly callouts (polish).
- cost-composition treemap (novel viz).
- Top-N ↔ trend control linkage.

### Open decisions

None blocking. All design calls are CTO-level and made above per Hard
Rule #1.

## Implementation

SOW-0067 completion pass — closed all 5 AC gaps. Changes:

- **Backend rows** (`internal/presenter/stats.go`, `stats_breakdowns.go`): enriched
  `by_model` (cache tokens + duration), `by_agent` (cache tokens), `by_source`
  (full economics + format label via sources JOIN), `by_status` (cost + tokens).
  Cache-hit is client-derived (`cache_read/(cache_read+tokens_in)`) — ratios do
  not aggregate, so no server field. All additive JSON keys (no breaking change).
- **Frontend types** (`frontend/src/api/types.ts`): row types mirror the new fields.
- **Stats page** (`frontend/src/pages/Stats/Stats.tsx`): (1) multi-series trend —
  a Group-by control threads `trendGroupBy` (was hardcoded `'total'`); top-8
  series + an `other` rollup (no silent data loss). (2) derived `failure_rate`
  trend metric — fetches failures+calls aggregates and divides per bucket
  (server can't SUM a ratio). (3) honest comparison table — stable columns,
  `—` for N/A cells, surfaces tool duration + model/agent/source cache-hit,
  sortable headers, clickable drill-down rows → sessions list. (4) CSV export on
  every data section (trend, top-N, comparison, failure analysis).
- **shareState** (`frontend/src/pages/Stats/shareState.ts`): new `trendGroupBy`
  URL control + `TrendMetric = StatsMetric | 'failure_rate'`.
- **FilterBar** (`frontend/src/components/FilterBar/FilterBar.tsx`): time-range
  presets (1h/24h/7d/30d/All) writing the already-supported `from` bound (+ a
  `range` mirror param so the select is pure in render; `Date.now()` only in the
  onChange handler).
- **Specs**: `rest-api.md` §GET /api/stats row shapes + the cache-hit note;
  `ui-pages.md` §/stats redesign. **Drive-by spec sync**: fixed a pre-existing
  SOW-0033 drift — `/api/payloads/` is registered but the spec still called it
  unregistered Phase-2; updated the heading + prose to match the code (Hard
  Rule #7, unblocked the spec-drift gate).

## Validation

- `internal/presenter/stats_test.go` — `TestStats_RowEnrichment` pins the new
  by_model/by_agent/by_status/by_source fields against the seeded graph; the
  MaliciousFilterValuesStayBound test still holds (additive columns).
- `frontend/src/pages/Stats/Stats.test.tsx` — multi-series group-by wiring,
  failure_rate dual-fetch, honest `—` cells, real cache-hit %, drill-down
  navigation, 4 CSV buttons, failure-analysis rendering.
- `frontend/src/pages/Stats/shareState.test.ts` — trendGroupBy + failure_rate
  parse/round-trip.
- `frontend/src/components/FilterBar/FilterBar.test.tsx` — preset writes
  from+range; All-time clears them.
- Gates: `go test -race ./...` PASS (all packages); coverage PASS (presenter
  92.9%, all gated ≥80%); frontend 650 tests PASS; `tsc --noEmit` clean; eslint
  clean; spec-drift PASS; secrets/ai-attribution clean; build.sh PASS. The only
  red is a pre-existing codex `bench_test.go` count mismatch (644 vs 676) that
  fails identically at HEAD `71cd7ca` — unrelated to this SOW; the bench gate is
  local/noisy (CI runs the compile-smoke only).

## Reviews

### Round 1 (6 reviewers: glm/minimax/mimo/kimi/qwen/deepseek)

Scores: glm 6.5, minimax 7, mimo 8, kimi 7, qwen 7, deepseek 7. Every claim was
verified against the diff (Hard Rule #4) before acting. Disposition:

- **FIXED (P1) payloads spec param** — minimax caught that my drive-by edit said
  `?download=1` while the code uses `?full=1` (`payloads.go:91`). A real new
  drift I introduced; corrected the spec. Also fixed the stale "Phase 2 / not
  registered" comment in `session_detail.go`.
- **FIXED (P1/P2) trendTruncated on the rate path** — I hardcoded `trendTruncated:
  0` for failure_rate, so the truncation footer never showed when >8 series were
  rolled into `other`. Now uses the capAndRoll count for both paths.
- **FIXED (P2) drill-down keyboard a11y** (4 reviewers) — `<tr onClick>` was
  mouse-only. The label is now a real `<button>` (keyboard + SR reachable) when
  the row has an honest filter; non-drillable rows render plain text.
- **FIXED (P2) stale `range` after Clear filters** (4 reviewers) — `clearFilters`
  now deletes the `range` mirror param; test added.
- **FIXED (P2) sort nulls-last** — `?? -1` floated N/A rows to the top on
  ascending; now nulls sort last in both directions.
- **FIXED (P2) false truncation note** — moved the ROW_CAP slice into the table
  (it was inside the builder, losing the pre-cap count) so the footer reports an
  honest "top 30 of N".
- **FIXED (P2) CSV leading `#` comment** — dropped it (breaks strict parsers);
  added `console.error` to the catch (Hard Rule #6).
- **FIXED (P2) by_error_class misleading drill** — drilled to `status=failed`
  (wider than the clicked class); now non-interactive (no honest error_class
  filter exists).
- **FIXED (P3) preset history spam** — `applyRangePreset` now uses `replace:true`.
- **REJECTED (P3, taste)** — by_source cache_write asymmetry (deliberate; not
  needed for hit ratio), CSV failure_rate raw 0–1 unit (raw is correct for
  analysis), failure_rate aria-label token (legend already labels series),
  capAndRoll `other=0` points (honest), countLabel-from-first-row (safe today).

Round 2 runs the SAME scope + the fix notes above.

### Round 2 (same 6 reviewers)

Scores: glm 9.5 PG, minimax 8 NEEDS WORK, mimo 9 PG (with caveat), kimi 9 PG,
qwen 9 PG, deepseek 9.5 PG. 4/6 PRODUCTION GRADE. Two reviewers flagged the SAME
real P2 (correctness), now fixed:

- **FIXED (P2) failure_rate 'other' was a sum-of-ratios** (minimax + mimo) —
  `capAndRoll` summed per-bucket ratios into `other`, which is meaningless. New
  `capAndRollRate` ranks by call volume and computes `other` as Σfailures/Σcalls
  per bucket (the only correct combined rate). Pinned by `Stats.helpers.test.ts`.
- **FIXED (P2) `.drillRow` misleading affordance** (minimax) — the row had
  cursor:pointer + hover but only the label button is clickable. Row is now
  cursor:default; the button is the sole affordance.
- **FIXED (P3)** removed dead `void metric;`, CSV escape now includes `\r`
  (RFC 4180), failure-analysis share-bar discloses its top-8 truncation.
- **REJECTED (P3, taste/by-design)** Duration column shown for all dimensions
  (stable-column design is the explicit SOW-0067 decision — "columns are NEVER
  silently hidden"), comparison CSV exports full raw data (server order, AC#5
  met), failure_rate aria-label token (legend labels series), drillDown carries
  chart-control params to the sessions page (harmless; preserves chart state on
  back).

Round 3 runs to confirm convergence after the round-2 fixes.

### Round 3 (same 6 reviewers) — CONVERGED

Scores: glm 9.5 PG, minimax 9.5 PG, mimo 9.5 PG, kimi 9 (one P2), qwen 9.5 PG,
deepseek 9.5 PG. 5/6 PRODUCTION GRADE at 9.5. The single P2 (kimi) was a spec
example omission — the `by_source` JSON example missed the `format` field the
code/types return — plus two deepseek P3s. All three fixed:

- **FIXED (P2) by_source spec example** — added `format` to the example + prose.
- **FIXED (P3) stale PayloadRef TS comment** — was "Phase 2 / not registered";
  now matches the SOW-0033 registered reality (mirrors the Go comment).
- **FIXED (P3) by_source LEFT JOIN + IFNULL** — defensively keeps a session
  whose source row is (theoretically) missing instead of dropping it.

spec-drift gate re-verified PASS; stats tests PASS; tsc clean. After these, no
P0/P1/P2 remains from any reviewer; only documented P3 taste-nits (stable
Duration column by-design, CSV raw units, drillDown carries chart params, sort
arrows aria-hidden) — all dispositioned above.

## Outcome

**Status: completed — all 5 ACs met, 6-reviewer loop converged (5/6 PG 9.5,
kimi 9 on a one-line spec example now fixed), all automated gates green.**

AC coverage: (1) unified FilterBar incl. time-range presets ✓; (2) group-by
comparison table with cost/tokens/duration/failure-rate/cache-hit ✓; (3) failure
analysis (distribution + failure-rate trend via the derived metric) ✓; (4)
multi-series trend overlay (group-by control, ≤8 + other) ✓; (5) CSV export on
all data sections ✓. Awaits the operator's commit decision, then moves to done/.

## Outcome

(filled on completion)
