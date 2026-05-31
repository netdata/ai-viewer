# SOW-0007 - Statistics & Analytics (Milestone 4)

## Status

Status: in-progress

Sub-state: activated 2026-06-01 on branch `sow-0007-statistics-and-analytics`. Prerequisites met — SOW-0001 + the five adapters are in `done/` (canonical schema + REST + catalog tables; v3/v2/claude-code/codex/opencode all ship cost/token data into the store), and SOW-0006 (the viz surface that consumes analytics) is merged. Operator sign-off granted (blanket Phase-2 backlog mandate + explicit "proceed", 2026-06-01). Implementing per the 13-chunk plan in the Pre-Implementation Gate; Chunk 1 (spec deltas) lands first.

## Requirements

### Purpose

Turn the canonical store into a cross-session analytics surface. The operator needs to answer questions like "how much did I spend yesterday across all agents?", "which tool is the slowest in p99?", "which model produced the most failures this week?", "show me every session that mentioned timeout error" — fast, over millions of ops, without re-scanning raw rows on every page load. This SOW delivers the materialized rollup tables, the FTS5 search index, the new REST endpoints, the Stats dashboard, and the saved-views URL-state mechanism.

### User Request

From the Phase Mapping in `.agents/sow/specs/ui-pages.md`:

> Phase 3-5 (Milestone 4): `/topology`, `/tools`, `/models`, `/agents` cross-session analytics + advanced filters + deep search.

And from `.agents/sow/specs/data-model.md`:

> "Time-bucketed materialized rollups (per-hour and per-day) per source, per model, per tool, per agent, per provider, per cwd. Refreshed incrementally by the ingester. Avoids `SUM` over millions of rows on every page load. Schema for rollup tables is defined in SOW-0007 (Statistics & Analytics)."

The data-model spec already pre-declares this SOW by name.

### Assistant Understanding

Facts:

- The canonical schema records cost, tokens, duration, status, and dimensional metadata (provider, model, tool namespace, agent name, cwd, source format) on every op. See `.agents/sow/specs/data-model.md`.
- Catalog tables (`catalog_providers`, `catalog_models`, `catalog_tools`, `catalog_agents`, `catalog_cwds`) already exist and are refreshed by the ingester after each batch commit. They cover total-since-forever totals, not time-bucketed analytics.
- Phase 1 SOW-0001 deliberately deferred rollup tables and FTS5 to "Phase 4" — i.e. this SOW. SOW-0001 uses live aggregates over `ops` for the small Phase 1 surface.
- SQLite FTS5 is the standard built-in full-text index for SQLite; `modernc.org/sqlite` (the chosen driver) compiles FTS5 in by default.
- `GET /api/stats` already exists in `.agents/sow/specs/rest-api.md` as a Phase 1 stub returning live aggregates; this SOW promotes it to use rollups behind the scenes and adds three new endpoints: `/api/stats/aggregate`, `/api/stats/top`, `/api/search`.

Inferences:

- Hourly buckets at UTC are non-negotiable for correctness — DST and local-time rollups would silently produce wrong sums across DST transitions. UI converts UTC buckets to operator-local time for display.
- Backfill correctness requires that the rollup computation is a pure function of the op rows in the window (`(ts >= bucket_start, ts < bucket_end)`). Live-mode incremental updates and a full-recompute backfill must produce identical bytes when run on the same data — this is the proof obligation in the acceptance criteria.
- The current open hour cannot be materialized while it's still being written to; the query layer computes it on-demand by `UNION ALL`-ing a live aggregate over `ops` with the materialized rollups for closed hours.
- FTS5 index size scales linearly with text content. On the operator's workstation (294K v2 sessions × dozens of logs each = potentially 10M log entries), the index could be 1-3 GB. Acceptable, but the index is opt-in per source via a config flag so it can be disabled on memory-constrained installs.
- "Saved views" without server-side persistence is the URL-state mechanism that already exists; this SOW adds a "copy share link" button and a parser for incoming shared links — no schema additions needed.

Unknowns:

- Exact log-entry volume per session per adapter — needs measurement after SOW-0001 backfill. Drives the FTS5 index size estimate; if larger than expected, the SOW may add a "FTS over op names and error messages only, not full log bodies" mode.
- Whether SQLite FTS5 with WAL mode handles concurrent writes from the ingester without contention with the rollup refresh transaction. The store's WAL setup from SOW-0001 should cover it; worst case, FTS5 updates land in a dedicated short transaction at the tail of each ingest batch.
- Real-world cardinality of dimensional columns: how many distinct (model, provider, tool, agent, cwd) tuples exist on the operator's workstation. Drives the rollup table row count and the GROUP BY query plan. Hourly × 6 dimensions could blow up if cardinality is high; a `max_rows_per_hour` safety bound is in scope.

### Acceptance Criteria

1. **Hourly + daily rollup tables created and populated.** New tables `rollup_hourly` and `rollup_daily` per the schema in this SOW. Backfilled from existing `ops` rows in a one-shot migration; refreshed incrementally on each ingest batch commit. **Verification**: schema migration applies cleanly; backfill of the operator's existing data completes in < 5 minutes; row counts non-zero; sample bucket totals match a hand-computed `SUM(...) FROM ops WHERE ts BETWEEN ...`.
2. **Rollup correctness invariant.** For any closed hour, the value of every (cost/tokens/calls/failures) cell in `rollup_hourly` equals the result of the equivalent live aggregate over the underlying `ops` rows. **Verification**: an automated test that runs the ingester in two modes — full backfill from scratch vs incremental refresh from an empty rollup — on a shared fixture, and `diff`s the two `rollup_hourly` tables byte-for-byte (after sorting). Test passes only when identical.
3. **FTS5 cross-session search.** `GET /api/search?q=<text>` returns matching ops (by tool name, model name, error message) and matching log entries (by message body), with session/turn/op linkage. The FTS5 index is built on tool names, model names, op error messages, and log entry messages. **Verification**: full-text search across 10M log entries returns in < 500 ms p95 (bench in CI on a synthetic 10M-row fixture); search hits a known fixture string and returns the correct row; relevance ranking (FTS5 BM25) is used.
4. **`GET /api/stats/aggregate` over arbitrary timeframe + dimensions.** Parameters: `from`, `to`, `bucket` (hourly|daily), `group_by` (one or more of `model|provider|tool|agent|cwd|source_format`), `metric` (`cost|tokens_in|tokens_out|calls|failures|duration_us`). Returns time-series buckets per group. **Verification**: aggregate query over 1M ops returns in < 200 ms p95 (bench in CI); response matches a hand-computed control on a fixture.
5. **`GET /api/stats/top` top-N by dimension.** Parameters: `from`, `to`, `dimension` (`model|provider|tool|agent|cwd`), `metric`, `n` (default 20, max 200). Returns the top-N entities by the chosen metric in the timeframe. **Verification**: response order matches hand-computed `ORDER BY ... LIMIT n` against `ops`.
6. **Stats dashboard page** at `/stats` (or merged into `/`'s header — final placement TBD in Chunk 1). Charts: per-day cost / tokens / sessions / failures (line); top-N models, providers, tools, agents, cwds (horizontal bar). Uses the global FilterBar for timeframe + dimensional filtering. **Verification**: page renders against fixture; visual regression test via Playwright screenshot; axe-clean.
7. **Saved views via URL.** Filter state encodes into the URL query string (already true in SOW-0001 for filters); this SOW adds a "Copy share link" button in the FilterBar and verifies that opening the copied URL on a fresh tab restores the same filtered view exactly. **Verification**: Playwright E2E test that copies URL → opens new tab → asserts filter state matches.
8. **Specs updated**: `data-model.md` (rollup schema), `rest-api.md` (three new endpoints), `ui-pages.md` (Stats dashboard + saved-views button), `ingester.md` (rollup refresh hook + FTS5 index maintenance), `quality-gates.md` (FTS5 / rollup-correctness gates if any new ones land). **Verification**: spec drift check (`scripts/spec-drift.sh`) green.

## Analysis

Sources checked (at SOW drafting):

- `.agents/sow/specs/data-model.md` — schema, catalog tables, the "rollups deferred to SOW-0007" note.
- `.agents/sow/specs/rest-api.md` — `/api/stats`, `/api/catalog/{tools,models,agents}`, pagination conventions.
- `.agents/sow/specs/ui-pages.md` — `/tools`, `/models`, `/agents` routes; phase mapping.
- `.agents/sow/specs/canonical-events.md` — dimensions available on every op (provider, model, tool, agent via session, cwd via session, source format via session).
- `.agents/sow/specs/sse-protocol.md` — `stats_invalidated` event already specified (rate-limited 1/s); the rollup refresh fires it.
- `.agents/sow/specs/ingester.md` — batch-commit lifecycle is where the rollup refresh hook lands.

Current state:

- After SOW-0001: catalog tables populated; live aggregates serving `/api/stats`; no time-bucketed rollups; no FTS5; no `/stats` route; no `/api/search`.
- The `stats_invalidated` SSE event is wired but currently fires only when catalog tables change.

Risks:

- **R1 — Rollup table cardinality explosion**. If every hour produces (model × provider × tool × agent × cwd × source_format) tuples and each dimension has hundreds of values, hourly row counts could reach millions per week. Mitigation: the rollup primary key is `(bucket_ts, source_format, dimension_key)` where `dimension_key` is the materialized GROUP BY tuple; high-cardinality dimensions (cwd in particular) are bucketed into "top N + other" only when the operator queries by that dimension. Hourly rollup retention defaults to 90 days; daily rollup is forever.
- **R2 — Backfill correctness regressions**. The proof that live-mode and backfill-mode produce identical rollups is the core invariant. Mitigation: the dedicated diff test in Acceptance #2 is a CI gate; runs on every commit touching `internal/store/rollups.go`.
- **R3 — FTS5 index size on 10M+ log entries**. Could be 1-3 GB on the operator's workstation. Mitigation: per-source config flag (`fts5_index_logs: true|false`); default true; when false, the index covers only op names + error messages (typically < 100 MB).
- **R4 — FTS5 vs WAL contention**. Long-running FTS5 rebuild during ingest could stall reads. Mitigation: rebuilds run in a short transaction at end of each ingest batch; full rebuild (rare) is offered as a maintenance command, not auto-triggered.
- **R5 — Time-zone consistency**. Operator views in local time, store works in UTC. Mitigation: every rollup bucket is `floor(ts_utc / 3600_seconds) * 3600_seconds` (no local adjustment); UI converts for display only. Documented in spec.
- **R6 — Sub-rollup for the current open hour**. The active hour cannot be materialized yet, but aggregates that include "now" need it. Mitigation: query layer computes the current-hour sub-aggregate on-demand via live `ops` query (small row count since it's one hour of data) and `UNION ALL`s with materialized closed-hour rollups.
- **R7 — Stats page bundle weight**. Charts often pull a charting library. Mitigation: D3 is already in the bundle from SOW-0006; reuse it for line + bar charts; no new chart library. Bundle stays within the 500 KB gzipped budget.
- **R8 — Saved views URL length**. Deeply filtered views may produce > 2 KB URLs. Mitigation: query-string compression (e.g. lz-string) only if URLs cross the 2 KB threshold; otherwise plain query string. URL length asserted in test.

## Pre-Implementation Gate

Status: SATISFIED (2026-06-01) — SOW-0001 in `done/`; operator sign-off granted (blanket Phase-2 backlog mandate + explicit "proceed"). No blocking open decisions (Stats page = own `/stats` route, per Open Decisions). Cleared to implement; Chunk 1 (spec deltas) lands first.

Problem / root-cause model:

- Cross-session analytics today require `SUM` over potentially millions of `ops` rows on every page load. The catalog tables cover lifetime totals only — no time-bucketed views, no top-N over a timeframe. The operator's primary use case ("what happened yesterday across everything?") is unanswerable without materialized rollups. Additionally, no cross-session text search exists; finding all sessions that hit a specific error requires manually walking sessions. This SOW addresses both gaps with rollup tables, FTS5, and three new REST endpoints feeding a Stats dashboard.

Evidence reviewed:

- All specs cited above.
- The data-model.md "Aggregation Strategy" section that pre-declares this SOW by name.
- The Phase Mapping table in ui-pages.md placing analytics in Phase 4.
- SQLite documentation on FTS5 (built into modernc.org/sqlite); BM25 ranking is default since SQLite 3.21.

Affected contracts and surfaces:

- Schema: two new tables (`rollup_hourly`, `rollup_daily`), one new FTS5 virtual table (`fts_ops`, `fts_logs`). New migration file under `internal/store/migrations/`.
- Backend: new package `internal/rollups/`; refresh hook in `internal/ingest/`; new query helpers in `internal/store/queries.go`; three new presenter handlers in `internal/presenter/`.
- REST: three new endpoints (`/api/stats/aggregate`, `/api/stats/top`, `/api/search`); `/api/stats` continues to work but uses rollups behind the scenes.
- Frontend: new `pages/Stats/` page; new "Copy share link" button in `FilterBar`; line + bar chart renderers under `viz/` (reusing D3 from SOW-0006).
- Specs: `data-model.md`, `rest-api.md`, `ui-pages.md`, `ingester.md`, possibly `quality-gates.md`.

Existing patterns to reuse:

- Catalog table refresh pattern from SOW-0001 (`internal/ingest/catalog.go`) is the template for the rollup refresh hook.
- TanStack Query cache invalidation pattern on `stats_invalidated` SSE event (already wired).
- The URL-synced filter state from SOW-0001 covers the saved-views mechanism.
- D3 line + bar chart references via `mirrored-repos`: Grafana (line charts), Prometheus UI, Jaeger UI (top-N bars), SigNoz (dashboards).

Risk and blast radius:

- New tables only; no schema changes to existing tables. Worst case: rollup tables incorrect — drop and recompute; ingest unaffected.
- FTS5 indexes can be dropped and rebuilt without affecting source data.
- Frontend changes are additive (new page, one new button).

Sensitive data handling plan:

- FTS5 indexes log message bodies. Per the AGENTS.md sensitive-data rule, fixture log messages MUST be sanitized via `scripts/sanitize-fixture.sh` (already enforced by SOW-0001) before any `testdata/` commit.
- Search results in the UI render log message excerpts; no new sensitive-data surface vs the existing Logs tab.
- The `stats_invalidated` SSE event carries only `{ts}`; no per-session data leaks.

Implementation plan (ordered chunks):

1. **Spec deltas (lands FIRST, no code)**: rollup tables schema → `data-model.md`; `/api/stats/aggregate`, `/api/stats/top`, `/api/search` contracts → `rest-api.md`; Stats dashboard layout + saved-views button → `ui-pages.md`; rollup refresh hook + FTS5 maintenance → `ingester.md`; FTS5-disabled fallback mode documented as config flag.
2. **Schema migration**: `internal/store/migrations/000N_rollups_fts5.sql` creating `rollup_hourly`, `rollup_daily`, `fts_ops`, `fts_logs`. Idempotent.
3. **`internal/rollups/` package**: pure-function rollup computation `RollupBucket(ops []Op, bucketSize Duration) []RollupRow`; unit tests with golden tables.
4. **Backfill runner**: one-shot command `ai-viewer-ingest rollups-backfill` that scans `ops` from `MIN(ts)` to last closed hour and populates rollup tables. Idempotent (re-runnable).
5. **Incremental rollup hook**: wire into `internal/ingest/` batch commit; after each commit, recompute rollups for the affected closed hours only.
6. **Rollup correctness CI test**: runs backfill-from-scratch and incremental-from-empty on the same fixture; `diff`s the resulting tables.
7. **FTS5 index population**: build initial index on backfill; update incrementally on each ingest batch; per-source `fts5_index_logs` config flag.
8. **REST handlers**: `/api/stats/aggregate`, `/api/stats/top`, `/api/search`; new bench tests targeting the 200 ms / 500 ms p95 budgets.
9. **Stats dashboard page**: line + bar charts; integrates with FilterBar; uses TanStack Query + `stats_invalidated` invalidation.
10. **"Copy share link" button**: encodes filter state as URL; clipboard copy; Playwright E2E.
11. **a11y pass**: axe-clean on `/stats`; keyboard nav reaches every chart and control.
12. **External review round**: codex + gemini + glm + qwen per `project-second-opinions` skill.
13. **Address review findings**, re-review, mark SOW completed, move to `done/`.

Validation plan:

- Per-chunk unit + integration tests.
- Bench tests in CI for the 200 ms aggregate and 500 ms search p95 budgets.
- Rollup correctness diff test (Acceptance #2) is a CI gate.
- Playwright E2E for the Stats dashboard and the saved-views button.
- axe-clean on `/stats`.
- External review converges before SOW close.

Artifact impact plan:

- `AGENTS.md`: no expected change.
- Runtime project skills: `project-go-backend` may grow an "FTS5 maintenance" section.
- Specs: `data-model.md`, `rest-api.md`, `ui-pages.md`, `ingester.md`, possibly `quality-gates.md` updated in Chunk 1.
- End-user/operator docs: `docs/runbook.md` gets "Rollups and search" section; `docs/architecture-overview.md` mentions FTS5 disk-footprint expectations.
- End-user/operator skills: none expected.
- SOW lifecycle: standard — completed + moved to `done/` in the final commit.

Open-source reference evidence:

- FTS5 implementation reference: SQLite official docs + `clickhouse/ClickHouse` (different DB, but instructive on time-bucketed rollup patterns) — to be cited via `mirrored-repos` skill during Chunk 7 if relevant.
- Time-bucketed rollup patterns: `prometheus/prometheus` (chunked aggregation), `victoriametrics/VictoriaMetrics` (downsampling) — read for inspiration, not for code reuse.
- Chart references: `grafana/grafana` line+bar implementations.

Open decisions:

- None blocking. Stats page placement (own route `/stats` vs merged into `/`'s header strip) resolved during Chunk 1 spec delta; default is own route to keep `/` focused on the sessions list.

## Implications And Decisions

(Filled if operator surfaces design decisions during review.)

## Plan

(Mirror of Implementation Plan above; expanded with commit refs as chunks land.)

## Execution Log

(Filled per chunk during implementation.)

## Validation

(Filled at end. Bench numbers, rollup-diff test result, review summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
