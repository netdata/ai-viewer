# SOW-0050 - Backend And CLI Residual Complexity Reduction

## Status

Status: open

Sub-state: pending from SOW-0047 closeout. Not active yet.

## Requirements

### Purpose

Reduce or justify residual backend and CLI complexity in ingest, presenter,
pricing, store, notify, command entrypoint, and backend tooling paths without
changing product behavior.

### User Request

Continue maintainability cleanup autonomously while keeping quality gates,
security, and performance strict.

### Assistant Understanding

Facts:

- SOW-0047 closeout found backend residual warnings in:
  `internal/ingest/writer.go`, `worker.go`, `catalog_migrate.go`,
  `notify_producer.go`, `resolver.go`, `rollup_backfill.go`,
  `rollup_refresh.go`, presenter handlers/middleware, pricing loader,
  store DSN/migration helpers, notify subscription replay, `cmd/ai-viewer-*`
  command entrypoints, and internal backend helper commands.
- The largest backend buckets were `internal/ingest/writer.go` with 4 warnings,
  `internal/ingest/worker.go` with 3, `internal/presenter/middleware.go` with
  4, `internal/presenter/embed.go` with 3, and `internal/pricing/loader.go`
  with 4.
- Command/tooling warnings include `cmd/ai-viewer-ingest/main.go`,
  `cmd/ai-viewer-ingest/sources.go`, `cmd/ai-viewer-ingest/backfill.go`,
  `cmd/ai-viewer-ingest/discovery.go`, and `cmd/ai-viewer-serve/main.go`.

Inferences:

- Ingest worker/writer complexity carries the highest data-integrity and
  performance risk.
- Presenter/pricing/store complexity is likely lower data-risk but still
  important for maintainability and security review.

Unknowns:

- Some warnings may be intentional orchestration density; each must be judged
  with tests and code evidence before refactoring.

### Acceptance Criteria

- Backend and CLI residual findings are ranked into ingest, presenter,
  pricing/store, notify, command entrypoint, and backend tooling slices.
- Each selected slice has tests before implementation and benchmarks when a hot
  path is touched.
- Remaining warnings are explicitly justified or split further.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.

Current state:

- SOW-0047 already decomposed specific ingest writer and catalog hotspots, but
  residual backend warnings remain across several packages.

Risks:

- Ingest regressions can affect persisted rows, rollups, catalog totals, FTS,
  or notify events.
- Presenter regressions can affect REST/SSE responses and static asset serving.
- Pricing/store regressions can affect cost calculations or database opening.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Backend residual complexity is no longer concentrated in one file. It is a
  set of smaller orchestration, command setup, and validation functions that
  need package-local treatment.

Evidence reviewed:

- SOW-0047 closeout warning buckets and functions.

Affected contracts and surfaces:

- Ingest event application, worker batching, rollups, catalog migration,
  presenter REST/static/middleware paths, pricing validation, store connection
  setup, notify replay, command entrypoint startup, source discovery, and
  backend helper commands.

Existing patterns to reuse:

- SOW-0047 ingest writer/catalog decomposition style.
- Existing package-level race tests, coverage gates, and benchmark gate.

Risk and blast radius:

- Medium to high for ingest; medium for presenter/security-sensitive request
  handling; low to medium for pricing/store and command setup helpers depending
  on selected functions.

Sensitive data handling plan:

- Use synthetic events and existing sanitized fixtures only. Do not write raw
  session content, secrets, private endpoints, or personal data to durable
  artifacts.

Implementation plan:

1. Rank backend warning clusters by risk and available test coverage.
2. Select one package-local slice at a time.
3. Update specs first if runtime behavior or documented contracts change.
4. Add characterization tests before refactoring.
5. Refactor selected functions into smaller helpers while preserving behavior.
6. Validate with focused tests, package race tests, strict Lizard, local Codacy,
   benchmark gate where applicable, full gates, and external review.

Validation plan:

- Focused package tests chosen per selected slice.
- Relevant package `go test -count=1` and `go test -race -count=1`.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` for ingest or notify/presenter hot paths.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: ingester, presenter, pricing, data-model, security, or deployment
  specs only if behavior/contracts change.
- Runtime project skills: update only if a new permanent backend pattern
  emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal backend maintainability work. External open-source evidence
  is required only if a selected slice changes a protocol or dependency
  behavior claim.

Open decisions:

- None for the operator.

## Outcome

Pending.
