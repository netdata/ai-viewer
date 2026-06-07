# SOW-0055 - Ingest Write-Model Residual Complexity Reduction

## Status

Status: open

Sub-state: split from SOW-0050 residual backend scan. Not active yet.

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

Status: ready for future activation.

Problem / root-cause model:

- The ingest write model contains several semantically dense functions that
  encode transaction-local state, carry-forward rollup behavior, and migration
  math. Splitting must preserve exact data contracts.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/ingest/catalog_migrate.go`, `rollup_backfill.go`,
  `rollup_refresh.go`, `writer.go`, and `resolver.go`.

Affected contracts and surfaces:

- SQLite canonical tables, rollup tables, catalog totals, source progress, FTS,
  resolver updates, health visibility for ingest errors, and benchmarked ingest
  write paths.

Existing patterns to reuse:

- Existing writer, catalog, rollup, resolver, and benchmark tests.
- SOW-0047/SOW-0050 helper extraction style with transaction order preserved.

Risk and blast radius:

- High within ingest data integrity; no REST/frontend/source-adapter behavior
  change is expected.

Sensitive data handling plan:

- Use synthetic events and committed sanitized fixtures only. Do not add raw
  prompts, tool output, source IDs, private paths, secrets, personal data, or
  private endpoints.

Implementation plan:

1. Audit each residual ingest warning and current coverage.
2. Add characterization tests before touching any transaction/carry-forward
   boundary.
3. Refactor one package-local slice at a time.
4. Keep SQL ordering, transaction boundaries, post-commit promotions, and error
   surfacing unchanged.
5. Validate focused tests, race tests, strict Lizard, local Codacy, benchmark
   gate when hot paths change, full gates, and external review.

Validation plan:

- Focused ingest tests selected after coverage audit.
- `go test ./internal/ingest -count=1`
- `go test -race ./internal/ingest -count=1`
- `scripts/check-bench.sh` for writer/rollup/backfill hot-path changes.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

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

Pending.
