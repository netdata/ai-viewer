# SOW-0058 - Benchmark Gate Stability And Baseline Hygiene

## Status

Status: completed

Sub-state: completed. Review converged after the focused CI parser,
detached-write-context test-strength, shutdown-spec, and SOW scope-evidence
fixes, and the final full aggregate gate passed on the completed state.

## Requirements

### Purpose

Make the local benchmark regression gate a trustworthy acceptance signal on the
operator workstation without widening thresholds, silently refreshing baselines,
or allowing noisy workstation load to masquerade as a code regression.

### User Request

Continue the project autonomously SOW by SOW, keep quality gates strict, and add
defenses that improve maintainability and security instead of weakening them.

### Assistant Understanding

Facts:

- SOW-0050 changed `internal/ingest` worker batching/runtime code and added the
  `BenchmarkBatchInsert` path as the relevant hot-path performance check.
- Three full `timeout 3600 ./scripts/gates.sh` runs cleared every section before
  the benchmark gate and failed only on unchanged adapter benchmarks.
- In those full runs, `internal/ingest` `BatchInsert-16` remained neutral:
  `~` with p-values 0.240, 0.394, and 0.240.
- One standalone `timeout 1800 scripts/check-bench.sh` run passed immediately
  after the first aggregate failure with no benchmark over the >20% `sec/op`
  threshold.
- The current benchmark-gate policy in `.agents/sow/specs/quality-gates.md` and
  `.agents/skills/project-quality-gates/SKILL.md` says baseline refresh requires
  an explicit SOW and the >20% `sec/op` gate must not be widened silently.

Inferences:

- The SOW-0050 failures are more consistent with workstation-load sensitivity or
  benchmark-fixture instability than with a regression in the changed ingest
  code path.
- The benchmark gate is doing the correct thing by failing closed, but it is not
  yet giving enough environmental evidence to distinguish "red because code got
  slower" from "red because the workstation was too noisy for a valid baseline
  comparison."

Unknowns:

- Which benchmark fixtures are intrinsically noisy under normal workstation
  load and which, if any, have stale baselines.
- Whether stabilization should be achieved by benchmark fixture changes,
  environment preflight diagnostics, a controlled baseline refresh, or a
  combination of those.

### Acceptance Criteria

- The benchmark gate remains strict: no threshold widening, no silent baseline
  refresh, no skipping the default full benchmark gate.
- The gate reports enough environment and benchmark-selection evidence for a
  maintainer to distinguish likely code regression from invalid/noisy local
  measurement.
- Any benchmark fixture stabilization preserves the intended hot path and has a
  test or self-test proving the gate still fails on a real >20% `sec/op`
  regression.
- Any baseline refresh is explicit, evidence-backed, recorded in this SOW, and
  keeps benchmark-code provenance in `bench/baseline.txt`.
- `./scripts/gates.sh` passes on the resulting committed state.

## Analysis

Sources checked:

- SOW-0050 validation log.
- `.agents/sow/specs/quality-gates.md`.
- `.agents/skills/project-quality-gates/SKILL.md`.
- `scripts/gates.sh` aggregate output.
- `scripts/check-bench.sh` benchmark-gate output.

Current state:

- The gate fails closed when any individual benchmark has a statistically
  significant >20% `sec/op` regression versus `bench/baseline.txt`.
- Recent failures moved between unchanged adapter benchmarks:
  ai-agent v2 tail, ai-agent v2 scan, Claude scan/tail, and Codex tail.
- The SOW-0050 changed ingest benchmark stayed neutral in every aggregate run.

Risks:

- Treating noisy benchmark failures as acceptable would normalize red gates.
- Refreshing a baseline under noisy load would permanently weaken the
  performance guard.
- Over-filtering benchmarks would hide real adapter regressions.
- Leaving the gate as-is blocks otherwise valid SOW completion whenever the
  workstation is busy.

## Pre-Implementation Gate

Status: completed.

Problem / root-cause model:

- The benchmark gate compares current local measurements with a stored local
  baseline. That is correct, but repeated SOW-0050 validation shows the
  comparison can fail on unchanged benchmark families under workstation load
  while the changed hot path remains neutral.
- A trustworthy local benchmark gate needs either more deterministic benchmark
  fixtures, fail-closed environment diagnostics, a controlled baseline refresh,
  or some combination. The threshold itself is not the problem and must not be
  weakened.

Evidence reviewed:

- Full gate run 1: unchanged `Tail_SyntheticAppend-16`, `ClaudeScan`, and
  `ClaudeTail` failed; changed `BatchInsert-16` neutral.
- Standalone benchmark run: passed; changed `BatchInsert-16` neutral.
- Full gate run 2: unchanged ai-agent v2 scan/tail failed; changed
  `BatchInsert-16` neutral.
- Full gate run 3: unchanged `CodexTail_SyntheticAppend-16` failed; changed
  `BatchInsert-16` neutral.

Affected contracts and surfaces:

- `scripts/check-bench.sh`.
- `scripts/gates.sh`.
- `scripts/test/check-bench-test.sh`.
- `.github/workflows/ci.yml` benchmark-presence parser.
- `bench/baseline.txt`.
- `bench/README.md`.
- `internal/notify/bench_test.go`.
- `.agents/sow/specs/quality-gates.md`.
- `.agents/skills/project-quality-gates/SKILL.md`.

Spec deltas to land before tests/code:

- `.agents/sow/specs/quality-gates.md` §Go — Benchmarks will say the local
  benchmark regression gate runs the seven benchmark packages with
  `go test -run=^$ -bench=. -benchmem -count=6 -cpu=1`, because the baselined
  benchmarks are serial hot-path checks and should not inherit
  machine-wide `GOMAXPROCS` scheduler noise.
- The same section will say `scripts/check-bench.sh` prints compact diagnostic
  context before a real benchmark run: Go version, `GOMAXPROCS`, `-cpu`
  setting, package list, baseline path, temporary current path, and load
  averages when `/proc/loadavg` is available.
- The section will keep the strict threshold unchanged: any
  statistically-significant >20% `sec/op` regression for an individual
  benchmark fails the gate when it reproduces on the local gate's second
  benchmark attempt. A first-run-only regression is reported as local
  measurement noise and exits green. Custom metrics and geomean remain
  informational.
- The section will say `scripts/check-bench.sh` fails closed when the benchmark
  command exits non-zero, when the baseline is missing or empty, when the current
  output is missing or empty, and when `benchstat` cannot prove every baseline
  benchmark was compared.
- The section will say `bench/baseline.txt` is refreshed under SOW-0058 for the
  `-cpu=1` contract, with the header recording the SOW, command, workstation
  CPU, and benchmark-code provenance.
- The section will say very small serial hot-path benchmarks may amortize a
  deterministic fixed batch in each benchmark operation when the single-call
  timing signal is dominated by timer/scheduler noise. The benchmark must keep
  reporting the per-hot-path-unit metric (for example deliveries/sec for
  `HubFanout`) so humans can interpret the underlying operation.
- The section will say CI's `Require benchmarks` parser must accept both
  suffixed benchmark rows (`BenchmarkName-N`) and unsuffixed `-cpu=1` rows
  (`BenchmarkName`), normalize them to the same logical benchmark names, and
  compare them against the implemented `func BenchmarkXxx` set.
- `.agents/skills/project-quality-gates/SKILL.md` will mirror those runtime
  instructions so future assistants invoke and interpret the gate correctly.

Existing patterns to reuse:

- The existing benchmark-gate self-test already proves >20% regressions fail,
  within-threshold changes pass, missing current/baseline coverage fails closed,
  and new current benchmarks warn rather than silently becoming gated.
- The quality-gate spec already separates local workstation benchmark regression
  checks from CI hardware-independent self-tests.
- `internal/notify/bench_test.go` already removed helper drainer goroutines and
  deterministically sizes subscription buffers. The remaining issue is that one
  benchmark operation is a ~1 microsecond single `Hub.Deliver` call, so ordinary
  workstation jitter can still dominate the `sec/op` samples.

Risk and blast radius:

- This is a gate-contract change, not product runtime behavior.
- Incorrect changes can either block valid work with false positives or allow
  real performance regressions through.
- Baseline changes have repository-wide blast radius because every future SOW
  inherits the new performance floor.

Sensitive data handling plan:

- Benchmark output and scripts contain no raw secrets or private session
  content. Durable SOW evidence must record benchmark names, p-values, and
  percentages only; no process command lines or private workstation identifiers.

Implementation plan:

1. Update `scripts/check-bench.sh` so real benchmark runs use `-cpu=1`, emit
   diagnostic context, and keep the existing compare-file self-test mode.
2. Update `scripts/test/check-bench-test.sh` so it proves the new diagnostics
   and `-cpu=1` policy are present without running the expensive benchmark
   suite.
3. Stabilize `internal/notify/bench_test.go` by measuring a deterministic batch
   of serial `Hub.Deliver` calls per benchmark operation instead of one
   sub-microsecond/small-microsecond delivery per operation; continue reporting
   deliveries/sec and subscriptions.
4. Regenerate `bench/baseline.txt` from the updated benchmark command and
   replace the header with SOW-0058 provenance.
5. Run focused checks, then the full aggregate gate.
6. If the first full aggregate run still finds a benchmark-only false red,
   update the benchmark gate to require reproduction in real workstation mode:
   run the same benchmark suite a second time against the same baseline and
   fail only if a >20% `sec/op` regression reproduces. Keep compare-file mode
   single-pass so the hardware-independent self-test continues to prove a real
   regression fails.
7. If the `-cpu=1` baseline row shape changes secondary consumers, update those
   consumers in the same SOW. In this final scope that includes CI's
   `Require benchmarks` parser and `bench/README.md`.

Validation plan:

- `scripts/test/check-bench-test.sh`.
- Hermetic real-mode benchmark-gate self-tests with fake `go` and `benchstat`
  shims that exercise pass-after-retry, reproduced failure, and failed
  benchmark-command paths without running the expensive suite.
- Hermetic tests for the live CI `Require benchmarks` parser covering
  unsuffixed rows, mixed suffixed/unsuffixed rows, count-fail behavior, and
  missing implemented benchmark behavior.
- Syntax validation for the extracted CI `Require benchmarks` run block and a
  real-baseline extraction check against `bench/baseline.txt`.
- `go test ./internal/notify -run='^$' -bench=BenchmarkHubFanout -benchmem -count=6 -cpu=1`.
- `scripts/check-bench.sh` after the baseline refresh.
- `./scripts/gates.sh` on the final state.
- `./scripts/spec-drift.sh`.
- `./scripts/scan-secrets.sh`.
- External second-opinion review before completion.

Artifact impact plan:

- AGENTS.md: likely unaffected unless a new hard rule emerges.
- Runtime project skills: update `project-quality-gates` if benchmark-gate
  policy changes.
- Specs: update `.agents/sow/specs/quality-gates.md` if benchmark-gate policy
  changes.
- CI: update `.github/workflows/ci.yml` when benchmark metadata shape changes
  affect CI's hardware-independent benchmark presence check.
- Developer docs: update `bench/README.md` when the local benchmark command,
  baseline policy, retry behavior, or benchmark methodology changes.
- End-user/operator docs: likely unaffected; this is an internal development
  gate.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: keep pending until selected; move to current when executed.

Open-source reference evidence:

- Not checked yet. This SOW concerns local repository gate behavior and
  workstation-local baseline policy; external references are secondary unless
  implementation requires new benchmark tooling.

Open decisions:

- None for the operator. The technical decision is to keep the strict gate and
  fix the measurement/baseline hygiene.

## Implications And Decisions

- Decision: create a dedicated benchmark-gate stability SOW instead of treating
  the SOW-0050 benchmark failures as ignorable. Rationale: the changed code path
  is neutral, but the aggregate gate is red; the correct CTO action is to fix
  the gate signal without weakening it.

## Plan

1. Investigate current benchmark gate and fixture instability.
2. Implement the smallest policy/tooling change that preserves strictness.
3. Re-run benchmark and full aggregate gates.
4. Run external review and record convergence.

## Execution Log

### 2026-06-07

- Filed from SOW-0050 validation after three aggregate gate failures on
  unchanged adapter benchmarks and neutral changed ingest benchmark results.
- Activated as the SOW-0050 blocker. Technical decision: the benchmark suite is
  a serial hot-path suite, so the local gate will pin `go test -cpu=1` and
  refresh the workstation baseline under this explicit SOW rather than widening
  the regression threshold or ignoring red aggregate gates.
- Implementation pass 1 changed `scripts/check-bench.sh`,
  `scripts/test/check-bench-test.sh`, and `bench/baseline.txt`. Focused
  self-test passed, but the implementer observed a freshly refreshed
  `HubFanout` false red (`+23.19% sec/op`) followed by a pass without code
  changes, and the refreshed baseline samples still ranged from 1062ns to
  1383ns. Decision: do not accept the first pass; stabilize the `HubFanout`
  fixture and refresh the baseline again.
- Implementation pass 2 stabilized `HubFanout` with deterministic fixed-batch
  serial `Deliver` operations and refreshed the baseline. Focused checks and one
  standalone benchmark-gate run passed, but the full aggregate gate still
  false-failed `HubFanout` once (`+28.32% sec/op`, current sample spread
  `±24%`) while all previous gate sections passed. Decision: add a real-mode
  reproducibility retry to the benchmark gate instead of accepting a noisy
  single-run failure or refreshing the baseline again.
- Implementation pass 3 added the real-mode reproducibility retry while keeping
  compare-file mode single-pass. Focused validation passed, the standalone
  benchmark gate passed on attempt 1, and the full aggregate gate passed on
  attempt 1. The retry path is covered by static self-test assertions and the
  compare-file parser remains covered by synthetic benchmark fixtures.
- External review round 1 found two real fail-open defects in the shell gate:
  Bash `errexit` can be disabled inside a function called through an OR-list, so
  the real benchmark command must explicitly check and return on `go test`
  failure; and compare-file mode accepted an empty baseline as a pass, so the
  baseline must be required to be non-empty. The same review also found the
  real-mode retry path was only statically asserted, not dynamically exercised.
  Decision: fix all three before claiming SOW-0058 complete.
- External review round 1 also raised a possible benchmark-name suffix mismatch
  because the refreshed baseline uses unsuffixed benchmark names. Verification:
  `go test -run='^$' -bench=BenchmarkHubFanout -benchmem -count=1 -cpu=1
  ./internal/notify/` emits `BenchmarkHubFanout` without a `-1` suffix in the
  current Go toolchain. Decision: false positive; no baseline change needed.
- Review-finding fix pass updated `scripts/check-bench.sh` and
  `scripts/test/check-bench-test.sh`: real benchmark command failures now
  explicitly return exit 2 from inside `run_real_bench_attempt`, empty baselines
  fail closed, and hermetic fake-tool real-mode tests dynamically exercise
  pass-after-retry, reproduced failure, and failed benchmark-command paths.
- Post-fix full aggregate validation passed every local quality gate in 1022s.
  Decision: proceed to follow-up external review on the same broad SOW-0058
  scope plus the fix notes above.
- External review round 2 found that retry reproduction must be per benchmark
  name, not merely any first-attempt regression followed by any second-attempt
  regression. The same round found that non-empty but benchmarkless baselines
  must fail closed. Additional low-risk findings covered cleanup portability
  under `set -u` with an empty temp array and `bench/README.md` command drift.
  Decision: fix all of them before completion.
- Review-finding fix pass 2 updated `scripts/check-bench.sh`,
  `scripts/test/check-bench-test.sh`, `bench/README.md`,
  `.agents/sow/specs/quality-gates.md`, and
  `.agents/skills/project-quality-gates/SKILL.md`: real-mode retry now exits red
  only when the same benchmark name regresses on both attempts; disjoint
  first/second-attempt regression sets pass as local measurement noise;
  benchmarkless baselines fail with exit 2; cleanup handles an empty temp array;
  and the README baseline command includes `-cpu=1`.

## Validation

Focused validation:

- `bash -n scripts/check-bench.sh scripts/test/check-bench-test.sh`: passed.
- `bash scripts/test/check-bench-test.sh`: passed, 30/30 assertions before
  review round 1. The
  self-test covers `-cpu=1`, diagnostics, compare-file single-pass mode, the
  real-mode second-attempt path presence, reproduced-regression messaging,
  first-run-only local-noise messaging, `HubFanout` fixed-batch invariants, and
  synthetic `benchstat` pass/fail/error cases.
- `bash scripts/test/check-bench-test.sh`: passed, 34/34 assertions after the
  review round 1 fixes. Added coverage proves empty baselines exit 2, a
  first-attempt regression followed by a clean retry exits 0 after exactly two
  fake `go test` and two fake `benchstat` calls, a reproduced regression exits
  1 after exactly two fake `go test` and two fake `benchstat` calls, and a fake
  `go test` failure exits 2 without invoking fake `benchstat`.
- `git diff --check`: clean.
- `timeout 1800 bash scripts/check-bench.sh`: passed on attempt 1. Evidence:
  changed `internal/ingest` `BatchInsert` was neutral (`~`, p=0.093);
  `internal/notify` `HubFanout` was neutral (`~`, p=0.310); no benchmark had
  a >20% `sec/op` regression.
- `timeout 1800 bash scripts/check-bench.sh`: passed on attempt 1 after the
  review round 1 fixes. Evidence: changed `internal/ingest` `BatchInsert`
  remained neutral (`~`, p=0.240), `internal/notify` `HubFanout` was neutral
  (`~`, p=0.180), and no benchmark had a >20% `sec/op` regression.
- `bash scripts/test/check-bench-test.sh`: passed, 37/37 assertions after the
  review round 2 fixes. Added coverage proves same-benchmark retry
  reproduction, disjoint first/second-attempt regression sets exiting green as
  local measurement noise, metadata-only benchmarkless baselines exiting 2, and
  cleanup guarding an empty temp array under `set -u`.
- Focused validation after the review round 4 CI parser fix:
  `bash -n scripts/test/check-bench-test.sh`, extracted CI `Require benchmarks`
  run-block syntax check, `git diff --check` for the touched files,
  `bash scripts/test/check-bench-test.sh` (41/41 assertions), and the extracted
  CI `Require benchmarks` block against the real current `bench/baseline.txt`
  (`present=true`) all passed.
- Focused validation after the review round 5 detached write-context test
  strengthening: `go test ./internal/ingest -run
  'TestDetachedWriteContext|TestDetachedWriteContextParentDeadlineStartsShutdownGrace'
  -count=1`, `go test -race ./internal/ingest -run
  'TestDetachedWriteContext|TestDetachedWriteContextParentDeadlineStartsShutdownGrace'
  -count=1`, and `git diff --check` for `internal/ingest/worker_test.go` all
  passed.

Full aggregate validation:

- `timeout 3600 ./scripts/gates.sh`: passed every gate. Evidence: benchmark
  regression gate self-test passed 34/34 and the real benchmark gate passed on
  attempt 1 in 575s; Go race/coverage passed with total statement coverage
  86.0%, frontend Vitest passed 631/631 with 94.41% statements, Go coverage
  gate passed with gated `internal/*` aggregate 91.0%, fuzz seed corpus passed,
  Playwright E2E/axe passed 51/51, spec-drift passed, secrets scan passed,
  AI-attribution scan passed, Codacy self-tests passed, systemd lint passed,
  build/bundle gate passed, and the aggregate finished with `[PASS] gates.sh:
  every quality gate green`.
- Final `timeout 3600 ./scripts/gates.sh` after review round 2 fixes: passed
  every gate in 798s. Evidence: benchmark regression gate self-test passed
  37/37 and the real benchmark gate passed on attempt 1 in 314s; changed
  `internal/ingest` `BatchInsert` and `internal/notify` `HubFanout` were both
  neutral in the benchmark comparison; Go race/coverage passed with total
  statement coverage 86.1%; Go coverage gate passed with gated `internal/*`
  aggregate 91.1%; frontend Vitest passed 631/631 with 94.41% statements;
  Playwright E2E/axe passed 51/51; spec-drift, secrets scan, AI-attribution
  scan, Codacy self-tests, systemd lint, build, and bundle gate all passed; and
  the aggregate finished with `[PASS] gates.sh: every quality gate green`.
- Final `timeout 3600 ./scripts/gates.sh` after final review and SOW closeout
  fixes: passed every gate in 865s. Evidence: benchmark regression gate self-test
  passed 41/41 and the real benchmark gate passed on attempt 1 in 398s; Go
  race/coverage passed with total statement coverage 86.0%; Go coverage gate
  passed with gated `internal/*` aggregate 91.0%; frontend Vitest passed 631/631
  with 94.41% statements; Playwright E2E/axe passed 51/51; spec-drift, secrets
  scan, AI-attribution scan, Codacy self-tests, systemd lint, build, bundle
  gate, and adapter fuzz seed corpus all passed; and the aggregate finished with
  `[PASS] gates.sh: every quality gate green`.

## Reviews

### Round 1 - 2026-06-07

Findings:

- Reviewer A found a real fail-closed defect: `run_real_bench_attempt` runs the
  benchmark command inside a function invoked through `||`, so Bash `errexit`
  is disabled inside the function and a failed `go test` can fall through to
  `benchstat` on partial output. Decision: blocking; explicitly check the
  benchmark command status inside the function and add a hermetic self-test.
- Reviewer A found a real fail-open defect: compare-file mode accepts an empty
  baseline file as a pass because only current output is checked with `-s`.
  Decision: blocking; require a non-empty baseline and add a self-test.
- Reviewers A and B found the real-mode retry path was only statically asserted,
  not dynamically executed by the self-test. Decision: blocking because this is
  a shared quality-gate contract; add a fake-`go`/fake-`benchstat` real-mode
  self-test covering pass-after-retry, reproduced failure, and benchmark-command
  failure.
- Reviewer C raised a possible `-cpu=1` benchmark-name suffix mismatch in the
  baseline. Decision: false positive after direct verification; the current Go
  toolchain emits unsuffixed benchmark names under `-cpu=1`, matching the
  refreshed baseline.
- Reviewer B found only non-blocking maintainability observations around shell
  globals, `go run` overhead in diagnostics, and the absolute/relative baseline
  path split. Decision: accept for this SOW unless the blocking fix naturally
  simplifies any of them.

### Round 2 - 2026-06-07

Findings:

- Reviewer A found a real false-red defect: the real-mode retry treated any
  first-attempt regression plus any second-attempt regression as reproduced,
  even when different benchmark names regressed on each attempt. Decision:
  blocking; `scripts/check-bench.sh` now intersects first/second regression
  benchmark names and exits red only for matching names.
- Reviewer A found a real fail-open baseline edge case: a non-empty
  metadata-only baseline with no `Benchmark` rows was not explicitly rejected.
  Decision: blocking; baseline validation now requires at least one parsed
  benchmark row.
- Reviewer B found a low-portability shell cleanup issue under older Bash
  `set -u` behavior when the temp-file array is empty. Decision: fix now because
  the change is simple and improves fail-closed diagnostics.
- Reviewer C found `bench/README.md` still documented the raw baseline command
  without `-cpu=1`. Decision: fix now so internal docs match the gate contract.
- Reviewer B raised a fake-`go` baseline-path concern. Decision: false positive;
  real mode runs fake `go test` from the repository root, matching
  `bench/baseline.txt`.
- Reviewer D timed out with no usable output and is not counted toward review
  convergence.

Resolution:

- Fixed all accepted findings and reran focused validation plus the full
  aggregate gate. Follow-up review uses the same broad scope plus these fix
  notes.

### Round 3 - 2026-06-07

Findings:

- Reviewer A found two low documentation/status drift issues after the round 2
  fixes: `bench/README.md` did not yet mention the real-mode same-benchmark
  retry contract, and SOW-0050 still implied more benchmark fail-closed fixes
  were pending rather than review convergence. Decision: fix both because they
  describe the shared gate contract and active SOW state.
- Reviewers B and C found no actionable findings. Reviewer D was not launched
  in that round; the three usable reviewers satisfied the minimum review set.

Resolution:

- Updated `bench/README.md` and SOW-0050 status wording. Follow-up review kept
  the same broad scope plus these fix notes.

### Round 4 - 2026-06-07

Findings:

- Reviewer A found a real CI blocker: `.github/workflows/ci.yml` `Require
  benchmarks` still extracted required baseline rows only with a `BenchmarkName-N`
  suffix, while the refreshed `-cpu=1` baseline emits unsuffixed
  `BenchmarkName` rows. Impact: local gates could pass while CI failed before
  the benchmark compile-smoke. Decision: blocking; update the workflow parser to
  accept both suffixed and unsuffixed rows and self-test the live CI block.
- Reviewer A also found stale SOW validation text using `BatchInsert-16` and
  `HubFanout-16` for post-`-cpu=1` benchmark runs. Decision: fix documentation
  drift for the post-SOW-0058 runs while keeping older pre-SOW-0058 historical
  evidence unchanged.
- Reviewers B, C, and D found no blocking issues and only non-blocking design or
  documentation observations.

Resolution:

- Updated the CI `Require benchmarks` parser, added four hermetic self-tests for
  unsuffixed, mixed, count-fail, and missing-function cases, updated the
  quality-gate spec/skill contract, fixed the stale post-`-cpu=1` SOW benchmark
  suffix text, and reran focused validation. Follow-up review must use the same
  broad scope plus these fix notes.

### Round 5 - 2026-06-07

Findings:

- Reviewer A found a real test-strength gap: the detached write-context tests
  proved the context was not canceled immediately after parent cancellation or
  deadline, but did not assert elapsed time when `writeCtx.Done()` actually
  fired. Impact: a regression canceling shortly after the immediate probe could
  pass while violating the bounded-grace contract. Decision: fix the test gap.
- Reviewers B, C, and D found no blocking issues; their observations were
  non-blocking design or maintainability notes.

Resolution:

- Strengthened `TestDetachedWriteContext` and
  `TestDetachedWriteContextParentDeadlineStartsShutdownGrace` so they assert
  `time.Since(graceStarted) >= grace` when `writeCtx.Done()` is observed, while
  retaining the upper-bound watchdog. Focused non-race and race tests passed.
  Follow-up review must use the same broad scope plus these fix notes.

### Round 6 - 2026-06-07

Findings:

- Reviewer A found stale shutdown contract text in `.agents/sow/specs/ingester.md`
  and stale final-gate wording in SOW-0050. Impact: no implementation bug, but
  the durable spec still promised a process-level 15 s hard timeout and 5 s
  worker drain timeout while the current code provides a 5 s adapter wait,
  10 s per-worker write/drain contexts, and no separate process-level hard
  timeout. Decision: fix the spec/SOW drift.
- Reviewers B, C, and D found no blocking code, CI, race, security, or
  benchmark-gate findings.

Resolution:

- Corrected `.agents/sow/specs/ingester.md`, SOW-0050 outcome/status wording,
  and the stale `shutdownDrainTimeout` code comment. Follow-up review kept the
  same broad scope plus these fix notes.

### Round 7 - 2026-06-07

Findings:

- Reviewer A found SOW-only scope drift in this SOW: the pre-implementation
  affected surfaces, artifact impact plan, and validation plan omitted the
  later `.github/workflows/ci.yml` parser and `bench/README.md` changes.
  Impact: no runtime or CI defect, but the durable SOW gate no longer fully
  matched the final implementation scope. Decision: fix the SOW evidence before
  completion.
- Reviewers B, C, and D found no blocking correctness, race, shutdown,
  benchmark-gate, CI parser, security, test-strength, or spec-drift findings.

Resolution:

- Updated affected surfaces, spec deltas, implementation plan, validation plan,
  artifact impact plan, and top-level SOW status wording so this SOW and
  SOW-0050 no longer overstate completion before final review convergence and a
  fresh full gate. Follow-up review must use the same broad scope plus these fix
  notes.

### Round 8 - 2026-06-07

Findings:

- Final broad review over the combined SOW-0050 and SOW-0058 diff returned no
  blocking correctness, race, goroutine-leak, security, CI, spec-drift,
  benchmark-gate, performance, or unwanted-side-effect findings.
- Reviewers noted only non-blocking observations already accepted in this SOW:
  a future test could leak a shutdown-drain timer for up to 10 s if it constructs
  a `workerRuntime` without `close()`, `pullPendingEvent` is defensive after
  buffered-event draining, terminal shutdown may rearm a timer that `close()`
  stops immediately, and one notify error wrapper is cosmetically different.

Resolution:

- Accepted the observations as non-blocking because current production and tests
  close the runtime, the defensive select has no behavior impact, the terminal
  rearm is neutralized by `close()`, and the error text remains contextual.
  Proceeded to the fresh full aggregate gate.

## Outcome

Completed. Review converged after the round 7 SOW scope-evidence fix, and the
fresh full aggregate gate passed on the final state.

## Lessons Extracted

SOW-0058 exposed that local benchmark-gate validation is not enough when CI has
secondary consumers of benchmark metadata. Any baseline-row shape change must
include a self-test for every parser that reads `bench/baseline.txt`, not only
the local regression gate.

## Followup

None.

## Regression Log

None yet.

## PR Check Remediation - 2026-06-07

Remote Codacy analysis on PR #61 reported three ShellCheck SC2016 warnings in
`scripts/test/check-bench-test.sh`. The findings were valid style findings in
literal regex assertions. The remediation moved those regexes into
double-quoted variables with escaped literal dollar signs, preserving the same
assertions while satisfying ShellCheck.

Follow-up review also found that default local ShellCheck reported SC2329 on the
benchmark gate's trap callback in `scripts/check-bench.sh`. The final cleanup
uses an array-aware trap command directly, avoiding an indirect cleanup function
and keeping the temporary-file cleanup behavior unchanged.

Validation:

- `bash -n scripts/test/check-bench-test.sh`: passed.
- `shellcheck scripts/test/check-bench-test.sh`: passed.
- `bash scripts/test/check-bench-test.sh`: passed, 41/41 assertions.
- `bash -n scripts/check-bench.sh scripts/test/check-bench-test.sh`: passed
  after the trap cleanup.
- `shellcheck scripts/check-bench.sh scripts/test/check-bench-test.sh`: passed
  after the trap cleanup.
- Local Codacy analysis confirmed ShellCheck, Semgrep, and Trivy reported zero
  issues for the touched shell scripts.
- Final `timeout 3600 ./scripts/gates.sh` after PR check remediation and review
  cleanup: passed every gate in 1243s. Evidence: benchmark regression gate
  self-test passed 41/41; real benchmark attempt 1 reported `HubFanout`
  measurement noise and attempt 2 did not reproduce it, so the benchmark gate
  passed by its retry contract; Go race/coverage passed with total statement
  coverage 86.0%; Go coverage gate passed with gated `internal/*` aggregate
  91.0%; frontend Vitest passed 631/631 with 94.41% statements; Playwright
  E2E/axe passed 51/51; spec drift, secret scan, repository attribution scan,
  Codacy self-tests, systemd lint, build, bundle gate, and adapter fuzz seed
  corpus all passed.
