# SOW-0056 - Store, Pricing, and Ingest CLI Complexity Reduction

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: split from SOW-0050 residual backend scan. Not active yet.

## Requirements

### Purpose

Reduce store connection/migration helpers, pricing validation/lookup helpers,
and `ai-viewer-ingest` command startup/source-discovery complexity while
preserving database opening, migration, pricing, source discovery, and CLI
behavior.

### User Request

Continue backend maintainability cleanup autonomously, with tests, security
checks, and external review before completion.

### Assistant Understanding

Facts:

- SOW-0050's declared backend/CLI scan left 3 store warnings, 5 pricing
  warnings, and 4 `cmd/ai-viewer-ingest` warnings.
- Store DSN and schema-prefix helpers are security-sensitive because path/DSN
  handling must remain explicit and predictable.
- Pricing validation and lookup helpers are correctness-sensitive because they
  protect model-cost calculations.
- Ingest CLI startup/source discovery wires user configuration into adapters and
  the ingest daemon.

Inferences:

- These warning families are lower data-integrity risk than ingest writer
  internals but still worth grouping because they are configuration/validation
  boundaries.

Unknowns:

- Some CLI complexity may be deliberate orchestration density; each function
  needs coverage review before refactoring.

### Acceptance Criteria

- Store, pricing, and ingest CLI warnings are ranked with file/function
  evidence.
- Selected refactors have tests before implementation.
- DB DSN/path handling, migrations, pricing validation, source discovery, and
  startup behavior remain unchanged.
- Remaining warnings are justified or split further.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0050 declared backend/CLI strict Lizard scan after the worker/notify slice.

Current state:

- Store, pricing, and ingest CLI warnings are distributed across validation and
  orchestration helpers rather than hot ingest write loops.

Risks:

- Store regressions can open the wrong database or apply migrations incorrectly.
- Pricing regressions can silently change cost calculations.
- CLI/source discovery regressions can miss sources or wire incorrect adapter
  configuration.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Configuration, validation, and startup helpers have accumulated branching for
  supported modes and edge cases. They need smaller helper boundaries without
  weakening validation.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/store/store.go`, `internal/store/migrations.go`,
  `internal/pricing/loader.go`, `internal/pricing/loader_null_check.go`,
  `cmd/ai-viewer-ingest/main.go`, `sources.go`, `discovery.go`, and
  `backfill.go`.

Affected contracts and surfaces:

- SQLite DSN/opening behavior, schema migration application, pricing data
  validation/lookup, ingest CLI flags/config, source auto-discovery, and
  backfill command behavior.

Existing patterns to reuse:

- Existing store, pricing, CLI, and discovery tests plus project CLI parsing
  conventions.

Risk and blast radius:

- Medium for configuration/security correctness; low frontend risk. No schema,
  REST, SSE, or source-format behavior change is expected.

Sensitive data handling plan:

- Use synthetic paths/configs and committed sanitized fixtures only. Do not add
  secrets, real API keys, private endpoints, personal data, or private paths.

Implementation plan:

1. Audit store/pricing/CLI warnings and current coverage.
2. Add focused tests for selected validation and startup boundaries.
3. Refactor helpers into explicit small functions or data-driven tables where
   it reduces complexity without hiding validation rules.
4. Validate focused tests, strict Lizard, local Codacy, full gates, and external
   review.

Validation plan:

- Focused tests selected after coverage audit.
- `go test ./internal/store ./internal/pricing ./cmd/ai-viewer-ingest -count=1`
- Race tests for touched command/store paths when concurrency is involved.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: store/pricing/deployment specs only if behavior contracts change;
  otherwise record unchanged attestations.
- Runtime project skills: likely unaffected unless a new CLI helper convention
  emerges.
- End-user docs: likely unaffected unless CLI behavior/help changes.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal configuration/validation maintainability work. External
  references are required only if a selected slice changes library/API behavior.

Open decisions:

- None for the operator.

## Outcome

Pending.
