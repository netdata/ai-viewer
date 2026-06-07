# SOW-0057 - Notify Replay Complexity Reduction

## Status

Status: open

Sub-state: split from SOW-0050 residual backend scan. Not active yet.

## Requirements

### Purpose

Reduce or justify notify replay complexity while preserving subscriber replay,
cursor, shutdown, and live-event delivery semantics.

### User Request

Continue backend maintainability cleanup autonomously while keeping live update
behavior reliable and tested.

### Assistant Understanding

Facts:

- SOW-0050's declared backend/CLI scan left one notify warning:
  `internal/notify/subscription.go:118` `replaySince`.
- The notify replay path feeds presenter/SSE live updates and is
  protocol-sensitive despite the small warning count.
- SOW-0050 deliberately deferred replay/SSE-sensitive work until stronger
  characterization exists.

Inferences:

- This should stay isolated from generic presenter cleanup because replay
  regressions can create duplicate, missing, or out-of-order live updates.

Unknowns:

- Existing replay tests must be audited before deciding whether refactoring is
  safe or whether the warning is better justified.

### Acceptance Criteria

- Notify replay behavior is characterized with focused tests before any
  refactor.
- Replay cursor, ordering, subscriber shutdown, busy-stream, and no-missed-event
  behavior remain unchanged.
- Complexity is reduced or explicitly justified with evidence.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0050 declared backend/CLI strict Lizard scan after the worker/notify
  producer slice.

Current state:

- Notify producer complexity was reduced in SOW-0050. Consumer replay remains
  as a separate protocol-sensitive warning.

Risks:

- Replay regressions can cause browsers to miss updates, receive duplicates, or
  hang on shutdown/reconnect.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Replay code combines cursor filtering, buffering, subscriber liveness, and
  delivery control flow in one function. The behavior is small but subtle.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/notify/subscription.go:118` `replaySince`.

Affected contracts and surfaces:

- Notify hub replay, subscriber delivery, SSE polling/reconnect behavior, and
  presenter live updates.

Existing patterns to reuse:

- Existing notify package tests and presenter SSE tests.

Risk and blast radius:

- Medium protocol risk; low database/schema/frontend design risk.

Sensitive data handling plan:

- Use synthetic notify events only. Do not add real session identifiers, private
  data, secrets, or endpoints.

Implementation plan:

1. Audit notify and SSE tests for replay coverage.
2. Add missing characterization for cursor/order/shutdown behavior before code.
3. Refactor `replaySince` only if tests prove the behavior can be preserved.
4. Otherwise justify the warning with evidence and leave the implementation
   unchanged.
5. Validate focused tests, race tests, strict Lizard, local Codacy, full gates,
   and external review.

Validation plan:

- `go test ./internal/notify -count=1`
- `go test -race ./internal/notify -count=1`
- Presenter SSE tests if replay behavior crosses package boundaries.
- Direct strict Lizard on changed files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: SSE/notify specs only if behavior contracts change; otherwise record
  unchanged attestations.
- Runtime project skills: likely unaffected.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal replay maintainability work. External references are required
  only if behavior changes to match an external protocol/library.

Open decisions:

- None for the operator.

## Outcome

Pending.
