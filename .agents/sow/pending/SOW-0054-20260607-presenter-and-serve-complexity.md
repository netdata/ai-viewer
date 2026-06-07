# SOW-0054 - Presenter and Serve Complexity Reduction

## Status

Status: open

Sub-state: split from SOW-0050 residual backend scan. Not active yet.

## Requirements

### Purpose

Reduce presenter HTTP/static/SSE/query complexity and serve command complexity
while preserving current REST, SSE, embedded-asset, and UI-serving behavior.

### User Request

Continue maintainability cleanup autonomously, SOW by SOW, with tests, security
checks, and external review before completion.

### Assistant Understanding

Facts:

- SOW-0050's post-worker strict backend/CLI scan left 25 warnings in
  `internal/presenter` and 2 warnings in `cmd/ai-viewer-serve`.
- The presenter warning set includes request handlers, filters, search, stats,
  source/session/log endpoints, middleware, embedded asset serving, topology,
  timeline, health, and SSE handling.
- `internal/presenter/events_sse.go` and `internal/notify/subscription.go` are
  protocol-sensitive and need stronger replay/shutdown characterization before
  refactoring.

Inferences:

- The safest order is to start with lower-protocol-risk request/asset helpers,
  then handle SSE/replay only after focused tests pin reconnect, `Last-Event-ID`,
  busy-stream, and shutdown behavior.

Unknowns:

- Which presenter warnings are already intentionally dense query builders must
  be decided after reading current tests and specs.

### Acceptance Criteria

- Presenter and serve warnings are ranked by protocol/security/user-facing risk
  with file/function evidence.
- Each selected slice has focused tests before implementation.
- REST/SSE/static asset behavior, HTTP status codes, headers, compression, and
  query parameter semantics remain unchanged.
- Remaining warnings are justified in this SOW or split further.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0050 declared backend/CLI strict Lizard scan after the ingest worker slice.

Current state:

- Presenter has the largest remaining backend warning count and covers public
  HTTP surfaces, so it should not be mixed into lower-level store/pricing work.

Risks:

- Handler regressions can change API payloads, status codes, compression,
  static-asset caching, or live-event delivery.
- SSE/replay regressions can create duplicate or missing events for connected
  browsers.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Presenter code combines HTTP parsing, SQL/filter composition, response
  shaping, and transport details in several large functions. Serve command
  startup also carries orchestration complexity.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/presenter/*` (25 warnings) and `cmd/ai-viewer-serve/main.go`
  (2 warnings).

Affected contracts and surfaces:

- REST handlers, query filters, search, stats, source/session/log endpoints,
  topology/timeline builders, gzip negotiation, embedded asset serving, health,
  SSE transport, and the `ai-viewer-serve` command.

Existing patterns to reuse:

- Existing presenter package tests, HTTP response helpers, filter tests, SSE
  tests, embedded-asset tests, and Playwright/API coverage where relevant.

Risk and blast radius:

- Medium to high because these are user-facing and HTTP-facing surfaces. No
  SQLite schema, adapter, ingest write path, or frontend design change is
  expected.

Sensitive data handling plan:

- Use synthetic stores and sanitized fixtures only. Do not add real prompts,
  tool output, private paths, secrets, personal data, or private endpoints.

Implementation plan:

1. Rank presenter/serve warnings by risk and existing coverage.
2. Start with middleware/static/query helpers that can be characterized with
   focused tests.
3. Treat SSE/replay as a separate high-risk slice with explicit protocol tests.
4. Refactor in package-local helpers while preserving public behavior.
5. Validate with focused tests, strict Lizard, local Codacy, full gates, and
   external review.

Validation plan:

- Focused presenter and serve tests selected after coverage audit.
- `go test ./internal/presenter ./cmd/ai-viewer-serve -count=1`
- Race tests for touched presenter/SSE paths.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: presenter/API/SSE/static-serving specs only if behavior contracts
  change; otherwise record unchanged attestations.
- Runtime project skills: likely unaffected unless a new presenter
  decomposition convention emerges.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal presenter maintainability work. External references are
  required only if a selected slice changes protocol/library behavior.

Open decisions:

- None for the operator.

## Outcome

Pending.
