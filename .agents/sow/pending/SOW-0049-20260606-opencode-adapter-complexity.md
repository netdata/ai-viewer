# SOW-0049 - Opencode Adapter Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0047 closeout. Not active yet.

## Requirements

### Purpose

Keep the Opencode SQLite adapter maintainable, benchmarked, and safe under
polling/tailing changes.

### User Request

Continue reducing code-scanning complexity findings autonomously, SOW by SOW,
while preserving quality, maintainability, performance, and security.

### Assistant Understanding

Facts:

- SOW-0047 closeout measured residual Opencode adapter warnings in
  `conn.go`, `cursor.go`, `mapper_ops.go`, `mapper_parts.go`, `migrations.go`,
  `tailer.go`, `tailer_batch.go`, `tailer_changes.go`, and `tailer_wal.go`.
- Largest Opencode buckets from the warning-only scan include
  `tailer.go` with 3 warnings and two-warning clusters in
  `tailer_changes.go`, `mapper_parts.go`, `mapper_ops.go`, and `conn.go`.

Inferences:

- The Opencode tailer is likely high risk because it reads an external SQLite
  database, detects WAL changes, and maps batches into canonical events.
- A deterministic Opencode scan/tail benchmark should be considered before
  refactoring polling or batch mapping hot paths.

Unknowns:

- Existing Opencode adapter coverage and benchmark gaps must be confirmed by
  reading the package before implementation.

### Acceptance Criteria

- Opencode complexity findings are ranked with exact file/function evidence.
- Any SQLite/tailer hot-path refactor has benchmark or focused performance
  evidence before implementation.
- Refactors preserve read-only SQLite access, cursor ordering, migration
  handling, mapper output, and error surfacing.
- Any remaining Opencode complexity warning is reduced, explicitly justified,
  or split into a narrower follow-up SOW before completion.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.

Current state:

- Opencode adapter complexity remains a material residual adapter cluster after
  SOW-0047 focused on ai-agent v2 and Claude-code first.

Risks:

- SQLite read-mode regressions could violate the read-only source contract.
- Tailer regressions can miss changes, re-emit rows, or corrupt cursor state.
- Mapper regressions can change canonical event order or totals.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Opencode adapter responsibilities are spread across connection setup,
  migration reads, cursor comparison, polling, WAL detection, batch collection,
  and canonical mapping. Several functions exceed strict complexity limits.

Evidence reviewed:

- SOW-0047 closeout warning buckets.

Affected contracts and surfaces:

- Opencode adapter `Scan`, `Tail`, read-only SQLite DSN construction, cursor
  comparison, migration discovery, and canonical mapping.

Existing patterns to reuse:

- Adapter decomposition and benchmark-gate patterns established in SOW-0047.

Risk and blast radius:

- Medium to high within the Opencode adapter; no schema/API/frontend change is
  expected.

Sensitive data handling plan:

- Use synthetic SQLite fixtures or already-sanitized test fixtures only. Do not
  record real database contents in durable artifacts.

Implementation plan:

1. Audit Opencode spec, fixtures, tests, and warning functions.
2. Add or strengthen characterization tests before production refactors.
3. Add deterministic benchmarks if scan/tail hot paths are selected.
4. Decompose selected functions into focused helpers without changing adapter
   semantics.
5. Validate with focused tests, package race tests, strict Lizard, local Codacy,
   benchmark gate where applicable, full gates, and external review.

Validation plan:

- Focused Opencode tests selected after coverage audit.
- `go test ./internal/adapters/opencode -count=1`
- `go test -race -count=1 ./internal/adapters/opencode`
- Direct strict Lizard on changed Opencode files.
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` if benchmarks or hot paths change.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: likely `adapter-opencode.md`; quality/testing specs only if benchmark
  inventory changes.
- Runtime project skills: likely unaffected.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes Opencode
  interpretation, inspect upstream source or mirrored repositories first and
  cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Outcome

Pending.
