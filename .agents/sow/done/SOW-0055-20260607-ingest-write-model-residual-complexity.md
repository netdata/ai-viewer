# SOW-0055 - Ingest Write-Model Residual Complexity Reduction

## Status

Status: completed

Sub-state: local gates green; external review converged; ready to move to
`done/`.

## Requirements

### Purpose

Reduce or justify remaining ingest writer, catalog migration, rollup, backfill,
and resolver complexity while preserving persisted canonical rows, rollups,
catalog totals, FTS state, and resolver behavior.

### User Request

Continue backend maintainability cleanup autonomously without weakening data
integrity, tests, benchmarks, or security posture.

### Assistant Understanding

Facts:

- SOW-0050 removed the worker/notify warnings but left 9 declared backend-scope
  ingest warnings in writer, catalog migration, rollup refresh/backfill, and
  resolver paths.
- These paths own canonical row application, rollup materialization,
  catalog-total migration, and orphan parent-link repair.
- SOW-0050 added characterization coverage for worker shutdown and idle-refresh
  transaction boundaries that this SOW must preserve.

Inferences:

- The writer/catalog/rollup warnings are higher data-integrity risk than
  store/pricing/CLI helpers and should stay in a dedicated SOW.

Unknowns:

- Some anonymous warning locations in `writer.go` may be dense inline SQL/helper
  closures; each needs file/line review before deciding to refactor or justify.

### Acceptance Criteria

- Remaining ingest warnings are ranked by data-integrity and performance risk.
- Every selected refactor has characterization tests before implementation.
- Rollup, catalog, source-progress, FTS, resolver, and idempotency behavior
  remain unchanged.
- Hot-path changes include benchmark evidence.
- Remaining warnings are justified or split further.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0050 declared backend/CLI strict Lizard scan after the worker/notify slice.

Current state:

- Worker orchestration is decomposed. Remaining ingest complexity is inside the
  write model and repair/rollup helpers.

Risks:

- Ingest regressions can corrupt persisted rows, undercount rollups, drift
  catalog totals, break FTS search, or lose parent/child lineage.

## Pre-Implementation Gate

Status: completed for implementation.

Problem / root-cause model:

- The ingest write model contains several semantically dense functions that
  encode transaction-local state, carry-forward rollup behavior, and migration
  math. Splitting must preserve exact data contracts.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/ingest/catalog_migrate.go`, `rollup_backfill.go`,
  `rollup_refresh.go`, `writer.go`, and `resolver.go`.
- Current strict warning inventory:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/ingest/catalog_migrate.go internal/ingest/rollup_backfill.go internal/ingest/rollup_refresh.go internal/ingest/writer.go internal/ingest/resolver.go`
  reports exactly 9 warnings:
  - `internal/ingest/catalog_migrate.go:140` `removeOpContribution`.
  - `internal/ingest/catalog_migrate.go:208` `addMigratedTotals`.
  - `internal/ingest/rollup_backfill.go:38` `BackfillRollups`.
  - `internal/ingest/rollup_refresh.go:51` `refreshRollups`.
  - `internal/ingest/writer.go:420` `apply`.
  - `internal/ingest/writer.go:639` `markSessionRollupBucketsDirty`.
  - `internal/ingest/writer.go:754` `applyOpStarted`.
  - `internal/ingest/writer.go:1290` `applyLogEntry`.
  - `internal/ingest/resolver.go:90` `linkOrphans`.
- The `writer.go` warnings previously described as anonymous are Lizard display
  labels for named receiver methods, not true anonymous functions.
- Focused coverage command:
  `go test -coverprofile=/tmp/cov.out ./internal/ingest` plus
  `go tool cover -func=/tmp/cov.out`.
  Current relevant coverage includes `writer.apply` 100%,
  `writer.applyLogEntry` 92.6%, `writer.applyOpStarted` 85.3%,
  `catalog_migrate.removeOpContribution` 92.3%,
  `rollup_refresh.refreshRollups` 92.3%, and
  `rollup_backfill.BackfillRollups` 80.0%.

Affected contracts and surfaces:

- SQLite canonical tables, rollup tables, catalog totals, source progress, FTS,
  resolver updates, health visibility for ingest errors, and benchmarked ingest
  write paths.

Existing patterns to reuse:

- Existing writer, catalog, rollup, resolver, and benchmark tests.
- SOW-0047/SOW-0050 helper extraction style with transaction order preserved.
- SOW-0054 helper extraction style: package-local helpers, public contracts
  unchanged, SQL ordering unchanged, and tests covering extracted behavior.

Risk and blast radius:

- High within ingest data integrity; no REST/frontend/source-adapter behavior
  change is expected.

Sensitive data handling plan:

- Use synthetic events and committed sanitized fixtures only. Do not add raw
  prompts, tool output, source IDs, private paths, secrets, personal data, or
  private endpoints.

Spec deltas to land before tests/code:

- No runtime contract change is targeted. The implementation must preserve the
  current `ingester.md`, `data-model.md`, and `sse-protocol.md` contracts.
- `.agents/sow/specs/ingester.md`: unchanged. It already records the batch
  write order, rollup refresh order, FTS refresh behavior, source-progress
  persistence, notify atomicity, post-commit promotion, resolver pass, and
  shutdown-drain contracts this SOW must preserve.
- `.agents/sow/specs/data-model.md`: unchanged. It already records catalog,
  rollup, log, payload, source-progress, and FTS table semantics.
- `.agents/sow/specs/sse-protocol.md`: unchanged. Resolver linkage and notify
  rows must still commit atomically so open subscribers refetch changed sessions.
- Characterization tests added by this SOW are expected to pass before and after
  implementation because the target behavior already exists; they are regression
  pins for behavior-preserving decomposition, not new-feature tests.

Implementation plan:

1. Add characterization tests before production refactors:
   - catalog migration: cascading identity correction and explicit cost movement;
   - writer op-start: nil-extras re-emit preserving only resolver stash;
   - log-entry FTS: duplicate replay does not create duplicate `fts_logs`;
   - rollup refresh: carried hour/day buckets remain independent across
     multiple refreshes;
   - resolver: linkage failure rolls back without notify rows.
2. Refactor catalog migration helpers so remove/add migration paths share small
   table-specific helper functions while preserving exact SQL column sets and
   key predicates.
3. Refactor `BackfillRollups` and `refreshRollups` with orchestration/logging
   and shared dirty-bucket materialization helpers. Keep the open-bucket and
   carried-set rules unchanged.
4. Refactor writer helpers:
   - replace the event-kind dispatch switch with a package-local dispatch table
     or equivalent small helpers that preserve unknown-kind errors and
     `batchMaxSeq` updates;
   - split `markSessionRollupBucketsDirty` cursor draining from in-memory
     marking while preserving single-connection cursor-drain discipline;
   - split `applyOpStarted` into preparation, SQL upsert, and post-upsert
     marking/catalog calls without changing the SQL semantics;
   - split `applyLogEntry` reference resolution and FTS indexing while
     preserving `INSERT ... RETURNING id` conflict behavior.
5. Refactor `linkOrphans` with a transaction runner and link-step sequence only
   if strict complexity remains. Preserve the single transaction containing all
   linkage updates and notify rows.
6. Re-run strict Lizard on all target files. Remaining warnings are acceptable
   only if explicitly justified in this SOW and not reported by local Codacy on
   changed files.
7. Validate focused tests, race tests, local Codacy, benchmark gate, full gates,
   and external review.

Validation plan:

- Pre-code characterization:
  `go test ./internal/ingest -run 'TestCatalog_|TestWriter_|TestRefreshRollups_|TestResolver_' -count=1`.
- Focused package:
  `go test ./internal/ingest -count=1`.
- Focused race:
  `go test -race ./internal/ingest -count=1`.
- Strict target complexity:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/ingest/catalog_migrate.go internal/ingest/rollup_backfill.go internal/ingest/rollup_refresh.go internal/ingest/writer.go internal/ingest/resolver.go`.
- Local Codacy on changed files.
- Benchmark gate because writer/rollup hot paths are touched:
  `scripts/check-bench.sh`.
- Full local aggregate:
  `./scripts/gates.sh`.
- External second-opinion review on the final staged state until convergence.

Artifact impact plan:

- Specs: `ingester.md`, `data-model.md`, `pricing.md`, and related specs only
  if behavior contracts change; otherwise record unchanged attestations.
- Runtime project skills: likely unaffected unless a new ingest decomposition
  pattern emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal data-model maintainability work. External references are
  required only if a selected slice changes a source-format or protocol claim.

Open decisions:

- None for the operator.

## Outcome

Completed.

Implemented changes:

- Added 9 characterization tests covering catalog migration cascades, LLM cost
  migration, op-start re-emits, log-entry FTS idempotency and gating, rollup
  carried-set behavior, and resolver transaction atomicity.
- Split the characterization tests across small SOW-local files so the tests do
  not add new strict complexity debt.
- Refactored the 9 targeted ingest write-model warnings:
  - catalog migration totals now use table-specific helpers with unchanged SQL
    columns, predicates, and bind order.
  - rollup backfill and refresh orchestration now use smaller pass/helper
    functions while preserving open-bucket carry-forward behavior.
  - writer event dispatch, session rollup marking, op-start writes, and
    log-entry/FTS writes now use smaller helpers with the same state updates and
    error paths.
  - resolver orphan linkage now runs through a transaction helper and ordered
    step list while preserving the single transaction containing linkage and
    notify rows.

Spec outcome:

- No runtime spec changed. The implementation preserved the existing
  `ingester.md`, `data-model.md`, and `sse-protocol.md` contracts listed in the
  Pre-Implementation Gate.

Validation evidence:

- `gofmt -l` on touched Go files: clean.
- `git diff --check`: clean.
- Strict target complexity:
  `lizard -l go -C 8 -L 50 -a 8 -w internal/ingest/catalog_migrate.go internal/ingest/rollup_backfill.go internal/ingest/rollup_refresh.go internal/ingest/writer.go internal/ingest/resolver.go`
  produced no warnings.
- Strict complexity on the SOW-0055 characterization tests produced no warnings.
- `go test ./internal/ingest -run 'TestCatalog_|TestWriter_|TestRefreshRollups_|TestResolver_' -count=1`: pass.
- `go test ./internal/ingest -count=1`: pass.
- `go test -race ./internal/ingest -count=1 -timeout=10m`: pass.
- `golangci-lint run --timeout=5m ./internal/ingest/...`: pass.
- Local Codacy on changed files: Semgrep clean, Trivy clean, and all function
  complexity findings cleared. One Lizard file-length finding remains for
  `internal/ingest/writer.go` at 1048 LOC; this is pre-existing residual debt
  because `HEAD:internal/ingest/writer.go` had 1529 physical lines before this
  SOW. It is tracked separately in SOW-0060.
- `./scripts/gates.sh`: pass in 809s. Covered lint, standalone security tools,
  secrets scan, attribution scan, spec drift, build, bundle-size gate,
  benchmark regression gate, Go race/coverage tests, adapter fuzz seed corpus,
  frontend unit tests, Playwright E2E, and accessibility checks.

Reviews:

- External reviewer 1: no actionable correctness, security, race, or behavioral
  findings. Noted only non-blocking residual risks: future resolver-step
  allocation and the pre-existing writer file-length debt.
- External reviewer 2: no actionable findings. Noted that the SOW closeout
  still needed final evidence; this section addresses that.
- One approved reviewer attempt failed before producing a review because the
  provider quota was exhausted. It was not counted.
- Replacement external reviewer 3: no actionable correctness, security, race,
  performance, test-coverage, or separation-of-concerns findings. Confirmed the
  only remaining gap was this SOW outcome update.

Residual debt:

- `internal/ingest/writer.go` is still too large for Codacy/Lizard's file-NLOC
  threshold even after this SOW reduced it from its previous size and cleared the
  targeted function warnings. SOW-0060 tracks a behavior-preserving file split.
