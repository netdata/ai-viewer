# SOW-0059 - Ingest Test Residual Complexity Reduction

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: split from PR #61 remediation review. Not active yet.

## Requirements

### Purpose

Reduce or justify remaining strict Lizard warnings in ingest test files while
preserving the characterization coverage that protects worker shutdown,
rollup-refresh, notify, pricing-miss, and idempotency behavior.

### User Request

Continue backend maintainability cleanup autonomously without weakening tests,
benchmarks, data-integrity guarantees, or security posture.

### Assistant Understanding

Facts:

- PR #61 remediation removed the PR-added worker shutdown and notify rollback
  test complexity findings.
- Strict local Lizard still reports older pre-existing warnings in:
  - `internal/ingest/worker_test.go`
  - `internal/ingest/rollup_refresh_test.go`
  - `internal/ingest/notify_producer_test.go`
- Current strict Lizard residual inventory after PR #61 remediation:
  - `internal/ingest/worker_test.go:562`
    `TestWorker_LowSeqEventsNotDropped`
  - `internal/ingest/worker_test.go:766`
    `TestWorker_IdleTickMaterializesClosedBucket`
  - `internal/ingest/worker_test.go:839`
    `TestWorker_IdleTickMaterializesClosedDayAfterMidnight`
  - `internal/ingest/worker_test.go:927`
    `TestWorker_FlushPromotesPendingMissDedupAfterCommit`
  - `internal/ingest/rollup_refresh_test.go:122`
    `TestRefreshRollups_OpenHourMaterializedAfterClose`
  - `internal/ingest/rollup_refresh_test.go:213`
    `TestRefreshRollups_RefreshOnlyMaterializesClosedCarried`
  - `internal/ingest/rollup_refresh_test.go:490`
    `llmOpEvents`
  - `internal/ingest/rollup_refresh_test.go:527`
    `TestRefreshRollups_ParityWithBackfill`
  - `internal/ingest/rollup_refresh_test.go:748`
    `TestRefreshRollups_OtherStaleRowRemoval`
  - `internal/ingest/rollup_refresh_test.go:856`
    `TestRefreshRollups_AggregatesMultipleSourcesSameFormat`
  - `internal/ingest/rollup_refresh_test.go:1010`
    `TestRefreshRollups_OpFinalizedRefreshesStartBucket`
  - `internal/ingest/rollup_refresh_test.go:1121`
    `TestRefreshRollups_SessionUpdatedReattributesAgent`
  - `internal/ingest/rollup_refresh_test.go:1217`
    `TestRefreshRollups_SessionUpdatedReattributesCwd`
  - `internal/ingest/rollup_refresh_test.go:1336`
    `TestRefreshRollups_SessionStartedRepairsStubMetadataAndStart`
  - `internal/ingest/rollup_refresh_test.go:1453`
    `TestRefreshRollups_OpenDayMaterializedAfterMidnight`
  - `internal/ingest/notify_producer_test.go:50`
    `TestEmitNotify_SessionChangedPerAffectedSession`
  - `internal/ingest/notify_producer_test.go:123`
    `TestEmitNotify_RootSessionIDFromRow`
  - `internal/ingest/notify_producer_test.go:185`
    `TestEmitNotify_SourceStatusChangedOnParseError`
- These warnings are test maintainability debt, not production runtime defects.
- SOW-0055 tracks production ingest write-model residuals; it does not explicitly
  track test-file warnings.
- Final local Codacy analysis over the same touched ingest tests and benchmark
  shell scripts reported zero Trivy, Semgrep, and ShellCheck issues. Its Lizard
  wrapper reported 23 findings because it counts threshold dimensions separately:
  12 function NLOC findings, 8 function CCN findings, 1 helper parameter-count
  finding, and 2 file-NLOC findings for the large ingest test files. These are
  the same residual ingest-test maintainability class as the strict Lizard
  function inventory above, plus file-size cleanup context for this SOW.

Inferences:

- Test refactoring must be more conservative than production refactoring here:
  the current tests encode data-loss, rollback, and notification contracts, so
  helper extraction is acceptable only when assertions remain identical or
  stronger.

Unknowns:

- Some old test warnings may be better justified than decomposed if splitting
  them would hide scenario flow or weaken failure diagnostics.

### Acceptance Criteria

- Every selected test warning is either removed by assertion-preserving helper
  extraction or explicitly justified in the SOW outcome.
- No assertion is removed, loosened, or converted from fatal to non-fatal without
  a test-quality reason recorded in the SOW.
- The focused ingest tests and race-focused slices pass after each refactor.
- Strict Lizard and local Codacy output for changed test files are recorded.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- PR #61 follow-up review and local strict Lizard output after SOW-0050/SOW-0058
  remediation.

Current state:

- The remaining warnings are concentrated in older scenario-heavy ingest tests.
- Production worker/notify warnings addressed by SOW-0050 are not part of this
  SOW unless a test helper requires a production change, which should be avoided.

Risks:

- Over-aggressive helper extraction can make failure output less useful.
- Shared test helpers can create hidden coupling across large test files.
- Reordering setup or assertions can accidentally stop proving rollback or
  post-commit promotion behavior.

## Pre-Implementation Gate

Status: ready for future activation.

Problem / root-cause model:

- Several old ingest tests combine multi-step fixture setup, event emission,
  transaction behavior, and assertions in one physical function. Lizard flags
  the size/branching, but the tests are valuable because they expose complete
  scenarios. The cleanup must separate fixture/assertion repetition from the
  scenario narrative without reducing coverage.

Evidence reviewed:

- Strict Lizard output on `internal/ingest/worker_test.go`,
  `internal/ingest/rollup_refresh_test.go`, and
  `internal/ingest/notify_producer_test.go`.
- Local Codacy output on the touched ingest test files and benchmark shell
  scripts, including the per-rule Lizard breakdown recorded above.
- Direct reviewer verification that the same residual warning function names
  existed on `master`, so this SOW tracks pre-existing test debt rather than
  PR-introduced remediation debt.

Affected contracts and surfaces:

- Test-only code under `internal/ingest/*_test.go`.
- Indirectly protected runtime contracts: worker shutdown drain, idle rollup
  materialization, notify emission/rollback, pricing-miss deduplication,
  rollup parity, and source-progress idempotency.

Existing patterns to reuse:

- SOW-0050 helper extraction style:
  - fixture helpers create explicit state structs only when it clarifies setup
  - assertion helpers use `t.Helper()`
  - fatal diagnostics keep scenario labels
  - production code remains untouched for test-only complexity work

Risk and blast radius:

- Medium within test reliability; low runtime blast radius if production files
  remain untouched.

Sensitive data handling plan:

- Use synthetic events and committed sanitized fixtures only. Do not add raw
  prompts, tool output, source IDs, private paths, secrets, personal data, or
  private endpoints.

Implementation plan:

1. Inventory the remaining test warnings and rank by value/risk.
2. For each selected warning, map every assertion before editing.
3. Extract only repeated setup/assertion logic, not the scenario's causal flow.
4. Run focused tests immediately after each extraction.
5. Record any warnings intentionally left in place with justification.

Validation plan:

- Focused tests selected from changed functions.
- `go test ./internal/ingest -count=1`
- `go test -race ./internal/ingest -run '<focused pattern>' -count=1`
- Direct strict Lizard on changed test files.
- Local Codacy analysis on changed test files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: no runtime spec update expected; this is test-only maintainability work.
- Runtime project skills: update only if a new test-helper pattern emerges.
- End-user docs: no change expected.
- SOW lifecycle: move to `current/` when activated.

Open-source reference evidence:

- This is internal test maintainability work. External references are not
  required unless a selected slice changes source-format behavior, which is out
  of scope.

Open decisions:

- None for the operator.

## Outcome

Pending.
