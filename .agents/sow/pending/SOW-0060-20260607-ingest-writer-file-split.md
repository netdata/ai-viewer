# SOW-0060 - Ingest Writer File Split

## Status

Status: open

Sub-state: split from SOW-0055 local Codacy closeout. Not active yet.

## Requirements

### Purpose

Split the oversized ingest writer implementation into small package-local files
while preserving every persisted canonical row, catalog update, rollup dirty
mark, FTS write, source-progress update, pricing-miss side effect, and notify
contract.

### User Request

Continue backend maintainability cleanup autonomously without weakening data
integrity, tests, benchmarks, security posture, or the read-only source
contract.

### Assistant Understanding

Facts:

- SOW-0055 cleared the targeted strict function-complexity warnings in
  `internal/ingest/writer.go` and related ingest files.
- Local Codacy still reports one Lizard file-length finding for
  `internal/ingest/writer.go`: 1048 lines of code with a threshold of 500.
- The file-length issue is pre-existing residual debt. Before SOW-0055,
  `HEAD:internal/ingest/writer.go` had 1529 physical lines.
- The writer owns transaction-local application of canonical events and is a
  high data-integrity surface.

Inferences:

- This should be a file-boundary refactor, not a behavior refactor. The safest
  target shape is multiple package-local files grouped by writer responsibility:
  dispatch/session/turn/op/log/source-progress/pricing helpers.

Unknowns:

- The final file split boundaries must be chosen after reading the current
  writer helper graph to avoid circular helper movement or less readable
  locality.

### Acceptance Criteria

- `internal/ingest/writer.go` and every new writer-related file stay below the
  Codacy/Lizard file-length threshold, unless a remaining exception is
  explicitly justified here.
- No runtime behavior changes.
- Existing SOW-0055 characterization tests remain unchanged or are only renamed
  mechanically.
- Strict Lizard and local Codacy on changed writer files report no new function
  or file-size findings.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0055 local Codacy output on changed files.
- SOW-0055 strict Lizard output on target production and characterization test
  files.

Current state:

- Function-level complexity in the target writer paths is cleared.
- File-level size remains above the Codacy/Lizard threshold.

Risks:

- Moving helpers can hide transaction ordering mistakes if edits become more
  than mechanical.
- Splitting by event kind can separate shared helper state too aggressively and
  reduce readability.
- Package-local names can collide if the split is careless.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- `writer.go` accumulated multiple event families and helper groups in one file.
  SOW-0055 reduced individual function complexity, but the file still violates
  file-size maintainability gates. The next cleanup should move cohesive helper
  groups into package-local files without changing method receivers, SQL, or
  transaction boundaries.

Evidence reviewed:

- Local Codacy reported:
  `internal/ingest/writer.go:1 File has 1048 lines of code (threshold: 500)`.
- Baseline evidence captured during SOW-0055 before its commit:
  `git show HEAD:internal/ingest/writer.go | wc -l` returned 1529 physical
  lines for the pre-SOW-0055 version.

Affected contracts and surfaces:

- SQLite canonical tables, writer transaction order, catalog totals, rollup
  dirty sets, source progress, FTS, pricing-miss emission, notify rows, and
  ingest error surfacing.

Existing patterns to reuse:

- SOW-0055 package-local helper extraction style.
- Existing writer tests plus SOW-0055 characterization tests.
- No public API changes; all split files remain in package `ingest`.

Risk and blast radius:

- Medium within ingest maintainability. Runtime blast radius should remain low
  if the split is mechanical and tests/gates prove behavior preservation.

Sensitive data handling plan:

- No fixtures or real source snapshots are required. Do not add raw prompts,
  tool output, source IDs, private paths, secrets, personal data, or private
  endpoints.

Spec deltas to land before tests/code:

- No runtime spec change is expected. If the file split reveals a documented
  writer-order mismatch, update the relevant spec before tests/code.

Implementation plan:

1. Read the current writer helper graph and choose cohesive file boundaries.
2. Move helpers mechanically, preserving package, receiver methods, comments,
   SQL text, and call order.
3. Run `gofmt`, focused ingest tests, strict Lizard, and local Codacy after each
   movement slice.
4. Run full gates and external review on the final split.

Validation plan:

- `go test ./internal/ingest -count=1`
- `go test -race ./internal/ingest -count=1 -timeout=10m`
- Strict Lizard on all writer-related files.
- Local Codacy on changed writer-related files.
- `./scripts/gates.sh`
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: unchanged unless runtime contracts are corrected.
- Runtime skills: update only if a durable writer file-split pattern emerges.
- End-user docs: no change expected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal file-boundary maintainability work. External references are
  not required unless a runtime contract changes.

Open decisions:

- None for the operator.

## Outcome

Pending.
