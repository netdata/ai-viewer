# SOW-0057 - Notify Replay Complexity Reduction

## Status

Status: completed

Sub-state: completed; full gates and external review converged.
Activated from the SOW-0054 final gate rerun because the local benchmark gate
reproduced a `HubFanout` regression in unchanged notify code.

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
- SOW-0054's exact final full-gate rerun failed only at the local benchmark
  gate: `internal/notify` `BenchmarkHubFanout` reproduced a >20% `sec/op`
  regression on retry while SOW-0054 did not touch `internal/notify`.
- A focused `go test -run='^$' -bench=BenchmarkHubFanout -benchmem -count=6
  -cpu=1 ./internal/notify/` reproduced the current slowdown at roughly
  352-388 us/op versus the checked-in baseline range around 254-294 us/op.
- `subscription.appendReplay` shifts the full replay buffer with
  `copy(s.replay, s.replay[1:])` on every delivery after the buffer reaches
  capacity. This is correct, but it is O(replay capacity) in the `Hub.Deliver`
  hot path.

Inferences:

- This should stay isolated from generic presenter cleanup because replay
  regressions can create duplicate, missing, or out-of-order live updates.
- A circular replay buffer can preserve the public replay contract while
  removing the per-delivery full-slice shift from the hot path.

Unknowns:

- Whether the circular replay buffer alone is enough to restore `HubFanout`
  below the benchmark gate under current workstation load.

### Acceptance Criteria

- Notify replay behavior is characterized with focused tests before any
  refactor.
- Replay cursor, ordering, subscriber shutdown, busy-stream, and no-missed-event
  behavior remain unchanged.
- Complexity is reduced or explicitly justified with evidence.
- `BenchmarkHubFanout` no longer reproduces the >20% `sec/op` regression under
  the local benchmark gate.
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

Status: completed.

Problem / root-cause model:

- Replay code combines cursor filtering, buffering, subscriber liveness, and
  delivery control flow in one function. The behavior is small but subtle.
- The current replay buffer stores newest events in slice order but evicts by
  shifting the entire full slice left on every append. Under the benchmarked
  `Hub.Deliver` hot path this makes replay retention cost proportional to the
  replay capacity after warm-up.

Evidence reviewed:

- SOW-0050 warning inventory:
  `internal/notify/subscription.go:118` `replaySince`.
- SOW-0054 final exact full-gate rerun:
  `BenchmarkHubFanout` failed the benchmark gate after retry with `+35.01%`
  `sec/op` on the second attempt.
- Focused reproduction:
  `go test -run='^$' -bench=BenchmarkHubFanout -benchmem -count=6 -cpu=1
  ./internal/notify/` reported `BenchmarkHubFanout` samples between
  351492 ns/op and 387771 ns/op.
- Source evidence:
  `internal/notify/subscription.go` `appendReplay` uses
  `copy(s.replay, s.replay[1:])` when the replay buffer is full.

Affected contracts and surfaces:

- Notify hub replay, subscriber delivery, SSE polling/reconnect behavior,
  presenter live updates, and the local benchmark regression gate.

Existing patterns to reuse:

- Existing notify package tests and presenter SSE tests.

Risk and blast radius:

- Medium protocol risk; low database/schema/frontend design risk. Performance
  risk is positive if the hot-path shift is removed, but the replay ordering and
  gap-coverage contract must remain identical.

Spec deltas to land before tests/code:

- `.agents/sow/specs/sse-protocol.md`: no target wire-contract change. The
  existing contract already states that the server buffers the 100 most recent
  events per subscription, replays buffered events after `Last-Event-ID`, and
  sends `resync` when coverage cannot be proven.
- `.agents/sow/specs/quality-gates.md`: no target benchmark-policy change. The
  benchmark gate remains strict; this SOW fixes the notify hot path instead of
  widening thresholds or refreshing the baseline.

Sensitive data handling plan:

- Use synthetic notify events only. Do not add real session identifiers, private
  data, secrets, or endpoints.

Implementation plan:

1. Audit notify and SSE tests for replay coverage.
2. Add missing characterization for replay order after wrap-around, exact
   oldest/newest gap coverage, malformed/future `Last-Event-ID`, disabled
   replay buffers, and backpressure interaction before implementation changes.
3. Replace the full-slice-shift replay buffer with a circular replay buffer if
   the characterization tests prove behavior is pinned.
4. Preserve `replaySince` output order and coverage decisions exactly.
5. Validate focused tests, race tests, strict Lizard, focused `HubFanout`
   benchmark, local Codacy, full gates, and external review.

Validation plan:

- `go test ./internal/notify -count=1`
- `go test -race ./internal/notify -count=1`
- `go test ./internal/notify -run 'TestHub_|TestSubscription' -count=1`
- `go test -run='^$' -bench=BenchmarkHubFanout -benchmem -count=6 -cpu=1 ./internal/notify/`
- `scripts/check-bench.sh`
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

## Implementation

Status: completed by external development delegation; master review, full gates,
and external review converged.

Files changed:

- `internal/notify/subscription.go`
- `internal/notify/subscription_replay_test.go`
- `internal/notify/hub_coverage_test.go`
- `internal/notify/hub_fixes_test.go`

Changes:

- Replaced the full-slice-shift replay storage with `ringReplay`, a
  fixed-capacity circular buffer with O(1) append.
- Kept the exported `Hub` API unchanged and kept the package-local
  `appendReplay` helper as a thin test/contract wrapper around the ring.
- Kept `replaySince` coverage semantics unchanged: empty `Last-Event-ID` is a
  fresh covered stream; malformed/future/gap/empty-ring-with-nonempty-id return
  uncovered; retained oldest/newest boundaries return covered suffixes in
  delivery order.
- Updated existing white-box tests that inspected `s.replay` to inspect
  `s.ring.ordered()` and ring size/capacity instead.
- Added focused replay characterization tests for wrap-around retention,
  growth-to-full behavior, disabled replay, oldest/newest boundaries, evicted
  gaps, empty-ring semantics, and long wrap-around ordering.

Focused validation:

- Characterization tests passed before implementation:
  `go test -run 'TestSubscription_' -v -count=1 ./internal/notify`.
- Notify package tests passed:
  `go test ./internal/notify -count=1`.
- Notify race tests passed:
  `go test -race ./internal/notify -count=1`.
- Notify race stress passed during delegation:
  `go test -race -count=10 ./internal/notify`.
- Presenter race tests passed during delegation:
  `go test -race ./internal/presenter -count=1`.
- Notify coverage passed with 96.2% statements:
  `go test -coverprofile=/tmp/notify-cov.out -covermode=atomic ./internal/notify`.
- Notify linter passed:
  `golangci-lint run --timeout=5m ./internal/notify/...`.
- Focused `HubFanout` benchmark after implementation reported 22943-29327
  ns/op in the master run:
  `go test -run='^$' -bench=BenchmarkHubFanout -benchmem -count=6 -cpu=1 ./internal/notify/`.
- `gofmt` and `goimports` on touched notify files produced no output.
- Exact staged Codacy scan on all staged `.go` and `.md` files passed with zero
  issues:
  `codacy-analysis analyze --files $(git diff --cached --name-only --diff-filter=ACMR -- '*.go' '*.md') --parallel-tools 4 --tool-timeout 600000 --output-format json --output /tmp/sow0054-0057-codacy-final2.json`.
- Full local gates passed:
  `./scripts/gates.sh`.
  Result: `[PASS] gates.sh: every quality gate green.` Total runtime was 1303s.
  The final benchmark gate passed; `BenchmarkHubFanout` improved from the
  checked-in baseline around 274.52 us/op to about 23 us/op in the final gate
  runs, approximately a 91% `sec/op` reduction.
- Final full-gate highlights:
  `scripts/lint.sh` passed, `scripts/test.sh` passed, `scripts/spec-drift.sh`
  passed, `scripts/scan-secrets.sh` passed, `scripts/scan-ai-attribution.sh`
  passed, frontend E2E and axe passed, Go race tests passed, Go coverage passed,
  and the benchmark regression gate passed.

## Reviews

External second-opinion review was run on the complete staged state for both
SOW-0054 and SOW-0057:

- Reviewer 1 verdict: `PRODUCTION GRADE`.
- Reviewer 2 verdict: `PRODUCTION GRADE`.
- One reviewer process timed out after read-only validation commands and did not
  provide a final verdict; it was discarded as an invalid review result.
- Replacement reviewer verdict: `PRODUCTION GRADE`.

Review findings:

- No correctness blockers found.
- No security blockers found.
- No unwanted side effects found.
- Notify replay ordering, cursor coverage, disabled-replay behavior, and
  wrap-around retention were verified against the staged implementation and
  focused tests.
- Presenter and serve command behavior preservation from SOW-0054 was verified
  together with this SOW because both changes are staged together.

## Outcome

Completed.

The notify replay buffer now uses an O(1) circular ring instead of shifting the
full retained replay slice on every hot-path delivery after warm-up. The public
Hub API and SSE replay contract remain unchanged. The benchmark regression that
activated this SOW is fixed, with `BenchmarkHubFanout` improving by roughly 91%
against the checked-in baseline in the final gate run.
