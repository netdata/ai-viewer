# SOW-0117 - Ingest resolver burns ~1 core idle (json_extract full scans)

## Status

Status: open

Sub-state: gap analysis complete; root cause confirmed with pprof + direct query timing; ready to implement.

## Requirements

### Purpose

ai-viewer-ingest must be a lightweight background daemon that indexes
low-volume agent-session data. When idle (nothing to do) it must cost
no more than 1% of a single CPU core; when it has work it must be fully
optimal. Today it burns ~1 whole core unconditionally, 24/7.

### User Request

"ai-viewer-ingest consumes constantly 1 whole cpu core on my system,
unconditionally. Your goal is to make it consume no more than 1% of a
single core when it has nothing to do and make it 100% optimal when it
needs to do something. This should be lightweight daemon that just
indexes low volume data."

### Assistant Understanding

Facts:

- The daemon (PID-level, 7 days uptime) sat at a steady 100% CPU with no
  source activity.
- A 30s CPU pprof (`/tmp/cpu.prof`, captured via `--pprof=127.0.0.1:6060`)
  attributes **64.43% of total CPU** (28.15s of 43.69s; ~0.94 cores) to
  `internal/ingest.(*resolver).linkOrphans`, called every 5s from
  `resolver.loop` (`defaultResolverInterval = 5s`).
- Inside that, the JSON machinery dominates: `modernc …
  _jsonTranslateTextToBlob` (10.35%), `encoding/json.checkValid`
  (10.94%), `stateInString` (4.81%), `skip` (3.91%) — SQLite-side JSON
  parsing + the modernc→Go `encoding/json` bridge that parses every
  `extras_json` blob during these scans.
- The resolver runs 4 `UPDATE … RETURNING` passes per tick whose WHERE
  clauses predicate on `json_extract(extras_json, '$.aiViewer.X')`.
  `json_extract` cannot use any b-tree index, so every pass scans the
  full candidate set and parses JSON on each row.
- Direct timing on the live (read-only) production DB (16 GB, 275 961
  sessions, 5 116 370 ops):

  | Resolver pass | cheap-COUNT time | CPU | matched / scanned |
  |---|---|---|---|
  | `linkParents` (sessions.parentNativeId) | 0.27 s | 0.27 s | 571 / 177 763 NULL-parent |
  | `linkRoots` explicit (sessions.rootNativeId) | 0.32 s | 0.32 s | 571 / 177 763 |
  | `linkOpChildren` (ops.childNativeId) | **1.04 s** | 1.02 s | 3 183 / 5 021 174 NULL-child |
  | `linkOpChildrenByToolUse` (ops.toolUseId) | **1.11 s** | 1.09 s | 322 / 5 021 174 |

  ≈ 2.7 s of CPU every 5 s from the cheap forms alone; the real
  UPDATE…RETURNING + correlated-subquery + EXISTS forms are heavier.
  This is the unconditional ~1-core burn. (The transient scan/catch-up
  CPU seen right after a restart — codex `Tail` at ~30% — is separate
  and self-limiting; it is not the steady-state bug.)

- The matched sets are tiny (322–3 183). The cost is entirely the
  full-scan + per-row JSON parse, not the linkage work itself.

Inferences:

- Backing the four `json_extract` predicates with partial expression
  indexes turns each pass from O(rows-with-NULL-link) full scan into an
  index seek over O(matched). Validated on a temp DB mirroring the real
  schema: the `toolUseId` COUNT went 10.8 ms → 0.3 ms (36×), plan
  changed `SCAN ops` → `SCAN ops USING INDEX idx_ops_link_tooluse`, and
  the UPDATE…RETURNING form uses the index too.

Unknowns:

- Whether the SQLite planner will prefer the new ops expression indexes
  on the *real* 5 M-row distribution (synthetic test confirmed usage; to
  be re-measured on the production DB after the migration). The sessions
  indexes may be shadowed by the existing `idx_sessions_parent` (already
  narrows to 177 K); measured cost there is already only ~0.6 s/5 s and
  will be re-checked, with an `INDEXED BY` fallback if the planner does
  not pick the cheaper expression index.

### Acceptance Criteria

- AC1: After install + restart + scan completion, steady-state idle CPU
  (no source writes for ≥5 min) is ≤ 1% of one core, measured by
  `ps`/`top` and confirmed absent of the resolver in a 30s pprof.
- AC2: The resolver's two `ops` passes use `idx_ops_link_child` /
  `idx_ops_link_tooluse` (EXPLAIN shows `USING INDEX`); each pass is
  < 50 ms on the production DB.
- AC3: Migration `0015` creates the four partial expression indexes and
  bumps `schema_meta.version` to `15`; runs cleanly against the existing
  16 GB store.
- AC4: `TestSchema_ColumnContract` index allowlist,
  `TestSchema_PartialIndexPredicates`, and the new migration-0015
  contract test all pass; `data-model.md` documents the indexes; all
  automated gates green (`scripts/gates.sh`).
- AC5: Linkage correctness is unchanged — the resolver still links
  orphans (covered by existing resolver tests + a new index-usage test).

## Analysis

Sources checked:

- `internal/ingest/resolver.go` (the 4 passes, `loop`, `runResolverTx`)
- `internal/ingest/ingester.go` (`defaultResolverInterval`, resolver start)
- `/tmp/cpu.prof` (30s CPU pprof of the live daemon)
- `internal/ingest/_artifacts/goroutine.pb` (prior SOW-0094 goroutine
  capture showing the same resolver-in-json_extract stack)
- `internal/store/migrations/*.sql`, `internal/store/schema_contract_test.go`
  (index contract + partial-index predicate test + expression-index
  precedent `idx_sessions_tokens`)
- `.agents/sow/specs/data-model.md` (schema blocks + index lists)
- `.agents/sow/done/SOW-0093-…` (perf-via-index precedent)
- `scripts/spec-drift.sh` (confirmed it checks columns/tables, NOT indexes)

Current state:

- No index backs any `json_extract(extras_json, …)` predicate. Expression
  indexes ARE already used (`idx_sessions_tokens` on `(tokens_in +
  tokens_out)`), and partial indexes are used widely, so the combined
  partial-expression-index feature is already exercised in production.

Risks:

- **Migration build cost**: `CREATE INDEX` over 5 M ops parses
  `extras_json` once per row. One-time, runs inside `store.OpenWriter`
  before the ingester starts (no resolver contention), but it adds
  startup wall time. Mitigated: indexes are PARTIAL (WHERE … IS NOT
  NULL), so the planner only indexes the few thousand stashed rows, not
  all 5 M — the build touches 5 M rows to evaluate the predicate but
  inserts only ~3 K index entries. Acceptable for a one-time migration.
- **Planner mis-pick on sessions**: the existing `idx_sessions_parent`
  may win for `linkParents`. Cost there is already small (~0.27 s); will
  verify and only add `INDEXED BY` if needed (mirrors SOW-0095's
  documented `INDEXED BY` escape hatch).
- **Write overhead**: each op/session insert now updates 0–1 of the new
  indexes (only when the stash is present). Negligible — the stash is
  rare and the indexes are partial.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The background resolver (`resolver.loop`, 5 s tick) runs four
  `UPDATE…RETURNING` passes whose WHERE clauses predicate on
  `json_extract(<table>.extras_json, '$.aiViewer.<field>')`.
- `json_extract` is not indexable, so each pass full-scans every row
  that satisfies the cheap leading `…_id IS NULL` predicate and parses
  the JSON blob on each such row. With 5 021 174 ops having
  `child_session_id IS NULL`, the two ops passes parse millions of JSON
  blobs every 5 s — ≈ 2.7 s CPU / 5 s, the unconditional ~1-core burn.

Evidence reviewed:

- `internal/ingest/resolver.go:166-355` (the four passes)
- `internal/ingest/resolver.go:51-63` (`loop`, 5 s ticker)
- `internal/ingest/ingester.go:44-46` (`defaultResolverInterval`)
- 30s CPU pprof: resolver = 64.43% of CPU; JSON funcs the bulk of it.
- Read-only query timing on the live DB (table above).
- Temp-DB validation: partial expression index → 36× faster, index used.

Affected contracts and surfaces:

- Schema: new migration `0015`; `schema_meta.version` → `15`.
- `internal/store/schema_contract_test.go`: `expectedSchema()` sessions +
  ops index allowlists; `TestSchema_PartialIndexPredicates` want-map.
- New migration contract test (mirror `migration_0014_*_test.go`).
- `.agents/sow/specs/data-model.md`: document the four indexes.
- No API/UI/SSE contract change. No canonical-event change.

Existing patterns to reuse:

- Migration file convention `NNNN_description.sql` +
  `INSERT OR REPLACE INTO schema_meta … 'version'`.
- Partial-index precedent: `idx_sessions_first_user_message_hash`,
  `idx_ops_compaction`.
- Expression-index precedent: `idx_sessions_tokens` (`(tokens_in +
  tokens_out)`); its schema_contract entry uses `Cols: []string{"", "id"}`.
- Migration contract test precedent: `migration_0014_source_repair_liveness_indexes_test.go`.

Risk and blast radius:

- Low. Purely additive indexes; no query result changes; linkage logic
  unchanged. Only the resolver (and future json_extract link readers)
  benefit. One-time migration cost on startup (bounded; partial indexes).

Sensitive data handling plan:

- None. No fixtures, no session content, no secrets touched. pprof
  artifacts under `/tmp` are not committed.

Implementation plan:

1. **Spec** (`data-model.md`): add the link indexes to the sessions and
   ops schema blocks with a short note tying them to the resolver.
2. **Migration 0015** (`internal/store/migrations/0015_resolver_link_indexes.sql`):
   four `CREATE INDEX IF NOT EXISTS … ON <t>(json_extract(extras_json,
   '$.aiViewer.<f>')) WHERE json_extract(extras_json, '$.aiViewer.<f>')
   IS NOT NULL`; bump version to `15`.
3. **Migration 0016** (`0016_resolver_opchild_tooluse_index.sql`): adds
   `idx_sessions_link_tooluse` (the sessions-side index backing
   linkOpChildrenByToolUse's correlated EXISTS subquery; the ops side is
   idx_ops_link_tooluse). Measured: the EXISTS dropped 1.49 s → 6 ms.
   Bumps version to `16`.
4. **Resolver idle gating** (`resolver.go`, `ingester.go`, `worker.go`):
   (a) generation-skip — the worker bumps an atomic `ingestionGen` on every
   committed batch; the resolver skips the whole pass when it is unchanged
   (idle → ~0 CPU). (b) session-watermark gate — the resolver probes
   `MAX(sessions.last_activity_ts)` (covering-index, 0.4 ms) and skips the
   two SESSION passes (linkParents, linkRoots' recursive CTE) when only ops
   committed, because ops cannot change session parentage or roots. This
   killed the O(sessions) recursive CTE that dominated active-scan CPU.
5. **Schema contract test**: add the link indexes to `expectedSchema()`
   (sessions: parent + root + tooluse; ops: child + tooluse), each `Cols:
   []string{""}`, `Partial: true`; add them to
   `TestSchema_PartialIndexPredicates` want-map.
6. **Migration test**: new `migration_0015_resolver_link_indexes_test.go`
   asserting the indexes exist, are partial, and that EXPLAIN for the ops
   resolver queries uses them.
7. **Resolver idle tests**: new `resolver_idle_test.go` pinning the
   generation-skip and session-watermark gate (skip when unchanged, run when
   advanced, full-resolve default for direct callers).

Validation plan:

- `go test -race ./internal/store/... ./internal/ingest/...`
- `scripts/gates.sh` (lint/vet/staticcheck/security/coverage/spec-drift)
- Temp-DB EXPLAIN already shows index usage; re-confirm on production DB
  after install.
- Deploy: `scripts/install-system.sh`; wait for scan completion; capture
  30s pprof + `ps` CPU; assert ≤ 1% idle, resolver absent from pprof.

Artifact impact plan:

- AGENTS.md: add a Hard-Won Lesson (partial expression indexes back
  json_extract predicates that run on a cadence).
- Runtime project skills: unaffected (no new skill needed).
- Specs: `data-model.md` updated (this SOW).
- End-user/operator docs: unaffected (no user-visible change).
- End-user/operator skills: unaffected.
- SOW lifecycle: single SOW; move pending→current→done on completion.

Open-source reference evidence:

- Not applicable. SQLite expression-index + partial-index semantics are
  official SQLite features (expression indexes since 3.9.0, 2015;
  partial indexes since 3.8.0, 2014); modernc.org/sqlite v1.52 tracks
  current SQLite. Validated empirically against modernc via the temp-DB
  test above and the existing `idx_sessions_tokens` expression index in
  this repo.

Open decisions:

- None blocking. (CTO-owned technical decision: partial expression
  indexes chosen over a denormalized-columns migration or a pending-link
  worklist because it is the smallest blast-radius fix that makes the
  existing resolver design index-backed; the resolver's "re-scan every
  5 s" design is sound once each pass is O(matched).)

## Implications And Decisions

1. Fix approach. Options considered:
   - A) Partial expression indexes on the four stashed JSON fields
     (chosen). Surgical; mirrors existing expression/partial-index
     patterns; makes each resolver pass O(matched).
   - B) Denormalize stashed IDs into real indexed columns + backfill.
     Long-term-cleanest but invasive (writer changes, migration,
     drift risk vs extras_json). Disproportionate for a workstation
     daemon.
   - C) Adaptive resolver interval / skip-when-idle. Reduces frequency
     only; each pass still scans millions; does not hit <1% on its own.
   - D) Pending-link worklist. Most elegant O(pending) but the most new
     machinery; reserved as a follow-up if a cadence-based resolver ever
     proves insufficient.
   Selection: A — best fit-for-purpose (quality + simplicity + low
   blast radius), proven empirically.

## Plan

1. Spec delta (data-model.md index blocks).
2. Migration 0015.
3. schema_contract_test.go + partial-index predicate test updates.
4. New migration-0015 contract + resolver index-usage test.
5. Gates local; deploy + measure on production; external reviewer gate.

## Execution Log

### 2026-07-08

- Diagnosed: pprof + query timing confirmed resolver json_extract scans
  are the unconditional ~1-core burn.
- Enabled `--pprof` via drop-in `91-pprof.conf` for diagnosis (to be
  removed before completion).
- Validated the fix on a temp DB mirroring the real schema: partial
  expression index → 36× faster, plan uses the index.
- Implemented: migrations 0015 + 0016 (5 link indexes); resolver
  generation-skip + session-watermark gate.
- Measured on production DB (16 GB, 5.4 M ops, 306 K sessions):
  - linkOpChildrenByToolUse EXISTS: 1.49 s → 6 ms (idx_sessions_link_tooluse).
  - linkOpChildren: 1.04 s → 0.3 s (idx_ops_link_child).
  - Resolver CPU share during active scan: **73 % → 2.17 %** (linkRoots
    recursive CTE eliminated from the profile via the session-watermark
    gate; it now only runs when a session row actually changed).
  - Idle (no committed batches): resolver skipped entirely via
    generation-skip (unit-tested; resolver absent from idle profile).
- INDEXED BY NOT added to sessions queries (measured no benefit: the
  planner's idx_sessions_parent choice is already near-optimal and the
  remaining cost is cold-cache I/O that the hot daemon cache avoids).
- Open follow-up (out of scope): the ai-agent v2 adapter scans
  `/home/costa/.ai-agent/sessions` (31 GB / 615 K files) in ~30+ min on
  every restart — a separate scan-throughput issue, not the idle
  resolver burn this SOW fixes.
