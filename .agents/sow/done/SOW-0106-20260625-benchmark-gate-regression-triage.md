# SOW-0106 - Benchmark gate regression triage

## Status

Status: completed

Sub-state: SOW-0097 cleanup mandate closed for this SOW. Gap-analysis review round 6 converged: all six reviewers (`glm`, `minimax`, `kimi`, `mimo`, `deepseek`, `qwen`) voted `NOTHING MORE CAN BE DONE`; only P3 notes remain. Implementation-plan review round 5 converged: all six reviewers voted `READY FOR IMPLEMENTATION`; only P3 notes remain. Implementation review round 2 converged: all six reviewers voted `PRODUCTION GRADE`; only P3 notes remain. Benchmark self-tests pass, the corrected standalone benchmark gate passed under valid preflight conditions, and full `scripts/gates.sh` passed after moving the benchmark section before aggregate-created load.

## Requirements

### Purpose

Keep the SOW-0097 ingestion/parity program honest: no SOW-0097 derivative work may be called finished while the local benchmark gate has a reproduced adapter scan regression or an unclassified benchmark-baseline problem.

### User Request

The user asked whether SOWs between 0097 and 0105 are technical debt of SOW-0097 and stated that if they are, SOW-0097 has not finished.

### Assistant Understanding

Facts:

- `scripts/check-bench.sh` failed during SOW-0105 close-out after its retry.
- The retry reproduced `ClaudeScan_SyntheticCorpus` at `+27.73% sec/op` and `CodexScan_SyntheticCorpus` at `+26.72% sec/op`.
- SOW-0105 does not modify `internal/adapters/`, `internal/ingest/`, `internal/parity/`, or `bench/`.
- The benchmark baseline says it was refreshed during SOW-0097.
- SOW-0097 close-out already recorded a similar high-load benchmark gate
  failure followed by a successful standalone rerun.
- The post-baseline HEAD commit for the SOW-0099 through SOW-0102 adapter
  follow-up batch modified Codex adapter code and recorded `scripts/gates.sh`
  as passing at commit time.

Inferences:

- The benchmark failure is not evidence that the SOW-0105 UI/DB contract implementation is wrong.
- It is still SOW-0097-lineage technical debt because the failing benchmark surface covers ingestion/parity adapter performance.
- The likely root-cause class is benchmark-gate instability under workstation
  contention. The script has two credibility defects: it does not set `-p=1`,
  so it can create cross-package benchmark contention itself, and it does not
  fail closed when the host is already too busy for wall-time benchmarks.

Unknowns:

- Whether the benchmark gate passes on the same code after the host becomes
  quiet enough for wall-time measurements. "Quiet enough" means `/proc/loadavg`
  1-minute load below `0.50 * effective_GOMAXPROCS`; at or above that limit, the
  run is not accepted as performance evidence even if a focused benchmark
  happens to pass.
- Whether aiagent_v2 `BenchmarkTail_SyntheticAppend` needs a fixture-level
  stabilization after gate-level package serialization and busy-host preflight
  are in place.

### Acceptance Criteria

- The root cause class is proven with local evidence: code regression, stale baseline, benchmark instability, or environment contention.
- If it is a real regression, the code is fixed and the benchmark gate passes without weakening the threshold.
- If it is a stale baseline, `bench/baseline.txt` is refreshed only after proving the current performance is intentional and no allocation/event-count regression is hidden.
- If it is benchmark instability, the benchmark or gate is made more deterministic without silently widening the >20% `sec/op` policy.
- The script, specs, skills, and self-tests make the real benchmark environment
  explicit: package benchmark binaries run serially with `-p=1`, and real-mode
  benchmark execution fails closed before every real benchmark attempt when
  `/proc/loadavg` 1-minute load is at or above
  `0.50 * effective_GOMAXPROCS`.
- `bench/baseline.txt` provenance is corrected if needed without changing any
  benchmark samples, and the baseline header's `# Benchmark command:` block is
  updated to include `-p=1` so it remains aligned with the gate command. Sample
  refresh remains forbidden unless quiet-host evidence proves the checked-in
  samples stale.
- The SOW-0097 lineage is not reported finished until this SOW and SOW-0104 are resolved.
  SOW-0097's parity-gate deliverable remains completed; the unfinished item is
  the broader SOW-0097 derivative close-out.

## Analysis

Sources checked:

- `scripts/check-bench.sh`
- `bench/baseline.txt`
- SOW-0105 validation output
- `git diff --name-only` for SOW-0105 touched files
- SOW-0097 close-out benchmark evidence
- `git diff` / `git show` evidence for post-baseline adapter changes

Current state:

- Earlier benchmark gate retries reproduced adapter scan regressions:
  - `ClaudeScan_SyntheticCorpus`: `+27.73% sec/op`
  - `CodexScan_SyntheticCorpus`: `+26.72% sec/op`
- SOW-0105's uncommitted implementation does not touch adapter, ingest, parity,
  or benchmark files, so the reproduced failure is not caused by the UI/DB
  contract diff.
- The SOW-0099 through SOW-0102 adapter follow-up commit landed after the
  SOW-0097 baseline refresh. It modified Codex and aiagent_v2 adapter code and
  recorded `scripts/gates.sh` as passing at commit time. That does not prove the
  current failure is noise, but it is required evidence for the root-cause
  model.
- The SOW-0097 baseline diff refreshed the benchmark samples in the same commit
  that added the deterministic parity work. Gap-review round 3 resolved the
  provenance question: `git diff 33439d5^ 33439d5 -- bench/baseline.txt` proves
  the samples were refreshed post-parity, while the
  `Pre-refresh repository HEAD: e783894bfe3e` line is stale SOW-0058 provenance
  that SOW-0097 did not update. This is provenance debt, not evidence that
  samples are stale.
- The same benchmark output showed unchanged allocation counts for the
  reproduced scan benchmarks, which rules out a proportional allocation blow-up
  but not a CPU-side code regression or environment contention.
- Fresh SOW-0106 evidence on 2026-06-25:
  - `uptime` before rerun showed load averages around `13.98 15.73 18.81` on a
    24-logical-CPU workstation.
  - `bash scripts/check-bench.sh` attempt 1 ran with load around
    `13.91 15.65 18.75` and found >20% `sec/op` regressions in
    `Tail_SyntheticAppend`, `CodexScan_SyntheticCorpus`,
    `CodexTail_SyntheticAppend`, and `OpencodeScan_SyntheticDB`.
  - The same gate's attempt 2 ran with load around `21.19 19.47 19.09` and
    failed because `Tail_SyntheticAppend` reproduced at `+36.08% sec/op`.
    Its bytes/op and allocs/op were unchanged.
  - A focused `BenchmarkTail_SyntheticAppend` run for
    `./internal/adapters/aiagent_v2/` compared against the same baseline at
    `+14.94% sec/op`, below the 20% threshold, with unchanged bytes/op and
    allocs/op.
  - A focused full-package aiagent_v2 benchmark run compared at
    `Scan_SyntheticCorpus +8.65% sec/op` and
    `Tail_SyntheticAppend +9.42% sec/op`, both below threshold, with unchanged
    bytes/op and allocs/op.
  - `go help build` documents `-p n` as the number of programs, including test
    binaries, that can run in parallel; the default is `GOMAXPROCS`. The current
    benchmark script does not set `-p=1`; the quality-gate spec and skill only
    document `-cpu=1`, so `-p=1` is a deliberate strengthening, not an
    already-documented package-level clause.
  - Gap-analysis round 2 reviewers required a decisive full-suite `-p=1`
    diagnostic. The diagnostic ran for 838 seconds with pre-run load around
    `14.44 13.66 14.13` and post-run load around `22.45 15.40 14.48`. It still
    compared aiagent_v2 `Tail_SyntheticAppend` at `+73.85% sec/op` with
    unchanged bytes/op and allocs/op.
  - A focused aiagent_v2 tail-only rerun immediately after the long diagnostic
    compared at `+13.44% sec/op`, below threshold, with unchanged bytes/op and
    allocs/op under load around `24.53 16.64 14.93`.
  - A focused aiagent_v2 package rerun under load around `25.29 17.47 15.25`
    compared `Scan_SyntheticCorpus +8.54% sec/op` and
    `Tail_SyntheticAppend +24.24% sec/op`, with unchanged bytes/op and allocs/op.
  - A read-only process snapshot during the red measurements showed several
    unrelated high-CPU workstation processes, including the installed ai-viewer
    ingester, browser renderers/GPU process, local virtual machines, and
    monitoring daemons. The raw process command lines are not recorded in this
    SOW to avoid private-path leakage.
  - Gap-analysis round 3 required a complete post-baseline surface survey. The
    exact `git diff --name-status 33439d5..HEAD -- internal/adapters/
    internal/ingest/ internal/parity/ bench/` surface contains 17 files:
    adapter source (`internal/adapters/aiagent_v2/mapper_ops.go`,
    `internal/adapters/codex/mapper_turn.go`,
    `internal/adapters/codex/types.go`), adapter tests
    (`internal/adapters/aiagent_v2/mapper_test.go`,
    `internal/adapters/codex/mapper_test.go`,
    `internal/adapters/codex/parser_test.go`), ingest resolver source/tests
    (`internal/ingest/resolver.go`, `internal/ingest/parity_codex_test.go`,
    `internal/ingest/resolver_notify_test.go`), and parity source/tests
    (`internal/parity/aiagent_v2_source_helpers.go`,
    `internal/parity/aiagent_v2_source_structural.go`,
    `internal/parity/canonical.go`,
    `internal/parity/codex_session_metadata.go`,
    `internal/parity/codex_source.go`,
    `internal/parity/codex_source_legacy.go`,
    `internal/parity/aiagent_v2_source_test.go`,
    `internal/parity/codex_source_test.go`).
  - Hot-path disposition from that survey:
    - `aiagent_v2/mapper_ops.go` is in the adapter map path used by
      `processOnce` scan and tail paths, but the change is an O(1) display-name
      fallback for system ops and every red/focused aiagent_v2 tail comparison
      kept the exact SOW-0097 baseline `94305 B/op` and `446 allocs/op`. It
      remains a CPU-side candidate only if the corrected gate fails on a quiet
      host.
    - Codex adapter changes are in Codex session lineage mapping. They cannot
      explain simultaneous claude-code regressions because `claude_code` had no
      post-baseline adapter changes.
    - `internal/ingest/resolver.go` can affect ingestion resolver behavior, but
      it is not called by adapter `Scan`/`Tail` benchmarks, and `BatchInsert`
      did not exceed the gate threshold in the `-p=1` diagnostic.
    - `internal/parity/*` files are not on adapter benchmark paths; they are
      used by parity gates and source/canonical diffing.
  - Baseline allocation/sample provenance now rules out the stale-baseline
    explanation for the red aiagent_v2 tail runs: the SOW-0097 sample refresh
    moved `Tail_SyntheticAppend` from `93729 B/op, 418 allocs/op` to
    `94305 B/op, 446 allocs/op`, and current red/focused runs match
    `94305 B/op, 446 allocs/op`.

Risks:

- Ignoring this makes the benchmark gate meaningless for the ingestion/parity work.
- Refreshing the baseline without proof can hide a real adapter performance regression.
- Changing `scripts/check-bench.sh` in a way that changes the threshold,
  baseline, or retry semantics can weaken the gate. Adding `-p=1` is a
  deliberate strengthening that removes self-inflicted cross-package contention;
  it is not enough by itself. Adding a busy-host preflight must fail closed
  with exit 2 and an actionable diagnostic, never pass or silently skip the
  benchmark gate.
- The busy-host preflight is intentionally conservative. A run above the
  threshold is unsupported performance evidence, even if one focused benchmark
  happens to compare below the >20% threshold. This prevents optimistic
  cherry-picking from noisy workstation conditions.
- The threshold may block routine local gates on this workstation. Reviewer
  round 4 observed live 1-minute load straddling the proposed 12.0 effective-CPU
  threshold, and the documented red full-suite run began around 13.9. That
  makes blocking an intentional correctness tradeoff, not an edge case: if the
  host is busy, the benchmark gate must report `exit 2` and wait for a quieter
  window or explicit operator approval to stop the exact workload causing
  contention. Raising the threshold would admit a known bad measurement band.
- Treating it as SOW-0105 failure would misattribute a cross-cutting adapter
  gate blocker to a UI/API contract change.

## Pre-Implementation Gate

Status: plan-converged-ready-for-implementation

Problem / root-cause model:

- The local aggregate gate is red because benchmark `sec/op` regressions exceed
  the threshold on retry. The latest reproduced failure is
  `Tail_SyntheticAppend`, not the earlier scan-only set. The failing surface
  belongs to ingestion/parity adapter performance and benchmark-gate reliability,
  not the SOW-0105 UI/API contract files.
- The first SOW-0106 plan assumed the next fix should be a load preflight.
  External reviewers rejected that sequencing before package-level parallelism
  was investigated. Gap-analysis round 2 then required a decisive `-p=1`
  full-suite diagnostic. That diagnostic falsified the `-p=1`-only plan: the
  gate still produced a red aiagent_v2 tail comparison under high host load.
- The corrected model is not a code regression yet. It is a benchmark-gate
  reliability issue: real-mode wall-time benchmarks must remove self-contention
  with `-p=1` and must refuse to run on a host that is already too busy to make
  wall-time measurements meaningful.

Evidence reviewed:

- SOW-0105 close-out benchmark run.
- `scripts/check-bench.sh` retry output.
- SOW-0105 touched-file surface excludes adapter, parity, ingest, and benchmark files.
- SOW-0097 close-out benchmark evidence: similar high-load benchmark failure
  followed by a standalone pass.
- Post-baseline code survey: the SOW-0099 through SOW-0102 follow-up commit
  modified aiagent_v2/Codex adapter source, ingest resolver source, parity
  source, and tests after the SOW-0097 baseline refresh. Only the aiagent_v2 and
  Codex adapter source files are on adapter benchmark paths, and no
  post-baseline claude-code adapter change explains the simultaneous
  `ClaudeScan_SyntheticCorpus` regression.
- Baseline diff from the SOW-0097 commit shows benchmark samples were refreshed
  with the larger parity-visible workloads. The `Pre-refresh repository HEAD`
  line is stale SOW-0058 provenance and should be corrected as metadata without
  changing benchmark samples.
- Fresh benchmark reruns under current workstation load show unstable
  wall-time-only failures:
  - The full suite without `-p=1` reproduced a >20% failure.
  - The full suite with `-p=1` still reproduced
    `Tail_SyntheticAppend +73.85% sec/op` under high load.
  - A focused tail-only rerun under high load stayed below threshold at
    `+13.44% sec/op`.
  - A focused aiagent_v2 package rerun under high load reproduced
    `Tail_SyntheticAppend +24.24% sec/op`.
  - All these aiagent_v2 tail comparisons kept bytes/op and allocs/op stable.
- Local Go help confirms package-level `go test` parallelism defaults to
  `GOMAXPROCS` unless `-p` is set.
- Local process inspection confirms the host was busy with unrelated CPU
  consumers during these benchmark runs.
- SOW-0058 benchmark-gate stability SOW: established `-cpu=1`, same-benchmark
  retry, fail-closed parser/tooling errors, and load diagnostics. SOW-0106
  extends that contract with `-p=1` and busy-host fail-closed behavior.
- SOW-0104 remains in `current/` and tracks the SOW-0097 install/restart timeout
  defect. It is a lineage close-out co-condition, not a technical dependency of
  this benchmark gate fix.

Affected contracts and surfaces:

- `scripts/check-bench.sh` to enforce serial package benchmark execution and a
  fail-closed busy-host preflight before real-mode benchmark attempts.
- `bench/baseline.txt` header comments: provenance correction and benchmark-command
  block alignment are in scope; sample values are not changed. A sample refresh
  remains out of scope unless quiet-host, corrected-gate evidence proves the
  checked-in samples stale.
- `scripts/gates.sh` is an affected caller but should not need code changes:
  its existing `run_bench_gate` exit propagation means a busy-host `exit 2` from
  `scripts/check-bench.sh` correctly fails the aggregate local gate.
- Adapter benchmarks under `internal/adapters/...` if a real regression is proven
- SOW-0097 derivative close-out discipline
- `.agents/skills/project-quality-gates/SKILL.md` if benchmark-gate behavior changes
- `.agents/skills/project-testing/SKILL.md` because it is also operator-facing
  benchmark-gate guidance and must not drift from `project-quality-gates`.
- `.agents/sow/specs/quality-gates.md` if benchmark-gate behavior changes
- `.agents/sow/specs/testing-strategy.md` because it also summarizes the
  benchmark baseline and local/workstation benchmark gate.
- `bench/README.md` because it documents the benchmark command used to generate
  and compare baseline samples.
- `AGENTS.md` because a busy-host benchmark `exit 2` changes how future sessions
  must interpret the local gate result.

Existing patterns to reuse:

- Existing benchmark-gate retry behavior in `scripts/check-bench.sh`.
- Benchmark baseline refresh rule: explicit SOW only, no silent threshold widening.
- SOW-0097 validation ledger pattern for gate failures and dispositions.
- SOW-0058 benchmark-gate stability contract: `-cpu=1`, same-benchmark retry,
  fail-closed parser/tooling errors, and load diagnostics.

Risk and blast radius:

- Medium. The likely fix is still local to the benchmark gate, but it affects
  the credibility and ergonomics of the local workstation gate. The
  implementation must preserve the >20% threshold, retry semantics,
  compare-file behavior, and fail-closed error behavior. The busy-host preflight
  will make local gates more honest but may also require the operator to wait
  for background ingestion or other workstation workloads to settle before a
  full local gate can pass.
- Because the latest high-load process snapshot involved processes not started
  by this assistant, this SOW must not stop them as part of validation without
  explicit operator approval. If the preflight blocks validation, the SOW remains
  open until the host naturally settles or the operator approves stopping the
  relevant workload.

Sensitive data handling plan:

- Use benchmark names, percentages, repo-relative paths, and aggregate performance data only. Do not record workstation-private paths, raw session payloads, prompts, credentials, or private source names.

Implementation plan:

1. Run the gap-analysis reviewer gate on this revised SOW evidence. Positive
   vote required: `NOTHING MORE CAN BE DONE`.
2. If the gap gate converges, run the implementation-plan reviewer gate.
   Positive vote required: `READY FOR IMPLEMENTATION`.
3. Spec first:
   - Update `.agents/sow/specs/quality-gates.md` so the benchmark command is
     explicitly `go test -p=1 -run=^$ -bench=. -benchmem -count=6 -cpu=1 ...`.
     Update each benchmark-gate sentence that names the command or behavior:
     command literal, retry "same flags" list, fail-closed cases, diagnostics
     fields, and local-vs-CI distinction. Leave the CI benchmark-name parser
     contract unchanged.
   - Update `.agents/skills/project-quality-gates/SKILL.md` with the same
     runtime command and the reason: `-cpu=1` pins the benchmark binary's
     `GOMAXPROCS`; `-p=1` additionally serializes package benchmark binaries
     so the gate does not create cross-package contention.
   - Update `.agents/skills/project-testing/SKILL.md` wherever it summarizes
     the benchmark gate, so it also records `-p=1` and the busy-host fail-closed
     behavior. This skill is a durable operator-facing troubleshooting surface,
     not an optional duplicate.
   - Update `.agents/sow/specs/testing-strategy.md` wherever it summarizes the
     benchmark baseline or local benchmark gate, so it points at the strengthened
     local/workstation gate contract and does not leave `-count=6` as the only
     command detail.
   - Update `bench/README.md` benchmark-command prose so its documented command
     includes `-p=1` for the full local gate. Focused one-off benchmark examples
     may remain convenience commands if they are clearly not the regression gate.
   - Document that real-mode benchmark runs are invalid on a busy workstation
     and fail closed before collecting samples when the host load is above the
     configured gate limit. The configured limit is
     `BENCH_MAX_LOAD_PER_EFFECTIVE_CPU=0.50`, evaluated against the 1-minute
     `/proc/loadavg` value divided by the existing `effective_gomaxprocs()`
     helper, not raw `nproc`, so diagnostics and gate math use the same CPU
     availability model. CI remains compile-smoke plus self-test only; it does
     not run the workstation benchmark regression gate.
   - Document that `/proc/loadavg` absence or an unavailable
     `effective_gomaxprocs()` divisor is a real-mode gate error (`exit 2`)
     because the checked-in baseline is a Linux workstation baseline; compare-file
     mode remains platform-independent.
   - Correct `bench/baseline.txt` provenance-only header text so SOW-0058 and
     SOW-0097 refresh provenance are not conflated. Do not change benchmark
     samples under this provenance fix. Resolve any replacement SHA with
     `git rev-parse`, not by hand. Either relabel the current
     `Pre-refresh repository HEAD` line as SOW-0058-specific provenance or
     replace it with a clearer SOW-0097 refresh note; the header must no longer
     imply that `e783894bfe3e` is the SOW-0097 pre-refresh HEAD.
   - Update the `# Benchmark command:` comment block in `bench/baseline.txt` to
     include `-p=1`. This is a header-comment alignment with the strengthened gate
     command, distinct from provenance correction and distinct from sample refresh;
     the busy-host preflight remains a gate precondition and is not part of the
     benchmark command string.
   - Update `AGENTS.md` to record the new operating rule: a benchmark-gate
     `exit 2` from `scripts/check-bench.sh` can mean the workstation is too busy
     for valid wall-time benchmark evidence, not that adapter code regressed.
     The same note must also mention that the benchmark gate serializes package
     benchmark binaries with `-p=1`. It must tell future sessions to wait for a
     quieter host or request explicit operator approval before stopping the exact
     contending workload.
4. Tests second:
   - Extend `scripts/test/check-bench-test.sh` with static and dynamic checks:
     the script must contain/pass `-p="$BENCH_PKG_PARALLELISM"`, and the fake
     real-mode `go test` path must fail unless the command resolves to `-p=1`.
     The fake `go test` implementation should inspect its arguments and fail
     with a distinct non-zero code when `-p=1` is missing.
   - Add static durable-memory assertions proving the quality-gates spec,
     `project-quality-gates`, `project-testing`, `testing-strategy`,
     `bench/README.md`, `bench/baseline.txt`, `AGENTS.md`, and the
     `scripts/check-bench.sh` header comments contain the new `-p=1` and/or
     busy-host `exit 2` contract where they document the benchmark gate.
   - Add a baseline-header alignment assertion: the `bench/baseline.txt`
     `# Benchmark command:` block must include `-p=1` and must remain distinct
     from busy-host preflight diagnostics.
   - Add a README alignment assertion so `bench/README.md` cannot drift from the
     gate command after this SOW.
   - Add `BENCH_LOADAVG_FILE` as a loadavg fixture hook guarded by an explicit
     `BENCH_SELF_TEST=1` sentinel. The fixture points to a file with
     `/proc/loadavg`-compatible contents and is honored only when the sentinel is
     present; normal real mode reads `/proc/loadavg` directly. Setting
     `BENCH_LOADAVG_FILE` without the sentinel exits 2 with an actionable error
     instead of silently weakening the gate. The threshold constant remains
     non-env-overridable.
   - Add a misconfigured self-test assertion: `BENCH_SELF_TEST=1` without
     `BENCH_LOADAVG_FILE` exits 2 before fake `go test`, because hermetic self-test
     mode must not silently fall back to the host's `/proc/loadavg`.
   - Add a strict-sentinel assertion: `BENCH_LOADAVG_FILE` with
     `BENCH_SELF_TEST` unset, empty, or any value other than exactly `1` exits 2
     before fake `go test`. The assertion must include named cases for unset,
     empty string, `0`, `true`, and `2`, so the implementation cannot treat any
     non-empty sentinel value as enabled.
   - Retrofit the existing fake real-mode pass/retry/fail assertions so they run
     with `BENCH_SELF_TEST=1` and an explicit below-threshold
     `BENCH_LOADAVG_FILE`; otherwise they would become host-load-flaky when the
     preflight lands.
   - Add a hermetic busy-host preflight self-test that proves real mode exits 2
     before running fake `go test` when the load fixture is above the limit, and
     that compare-file self-test mode is unaffected.
   - Add an explicit above-threshold-attempt-1 self-test: if the first attempt's
     fixture is above the limit, real mode exits 2 before the first fake `go test`
     and never reaches retry logic.
   - Add an exact-threshold boundary assertion: a fixture with 1-minute load
     exactly equal to `0.50 * effective_GOMAXPROCS` fails closed with `exit 2`.
   - Add fail-closed parsing assertions for the loadavg fixture contract. A
     valid fixture must contain at least three whitespace-separated fields, and
     the first three fields must be non-negative floats; fields after the third
     are ignored, matching `/proc/loadavg`. Empty files, missing files, files
     with fewer than three fields, non-numeric first/second/third fields, and a
     negative first/second/third field must exit 2 before fake `go test` with
     an actionable loadavg diagnostic.
   - Add an unavailable-divisor assertion: fake `go run` for
     `effective_gomaxprocs()` returns `unavailable` or exits non-zero while the
     load fixture is otherwise below threshold; real mode must exit 2 before
     fake `go test` with an actionable effective-CPU diagnostic. Extend
     `write_fake_go` with two explicit controls: `FAKE_GOMAXPROCS=unavailable`
     makes fake `go run` print `unavailable`, and `FAKE_GO_RUN_FAIL=1` makes
     fake `go run` exit non-zero. Exercise both controls.
   - Add a per-attempt effective-CPU cache assertion. Extend `write_fake_go` so
     fake `go run` increments `FAKE_GO_RUN_COUNT`; in fake real-mode runs, prove
     the helper probe is called no more than once per real benchmark attempt
     (one call for a one-attempt pass, two calls across a two-attempt retry).
   - Add a busy-host error-field assertion: the above-threshold path's stderr
     must include the actual 1-minute load, effective `GOMAXPROCS`, computed
     threshold, package parallelism, pass/fail comparison, and operator action,
     and must not include process command lines.
   - Add a guard self-test proving `BENCH_LOADAVG_FILE` without
     `BENCH_SELF_TEST=1` exits 2 before fake `go test`; the fixture is not a
     general local override.
   - Add a retry-interaction self-test: load below limit on attempt 1 allows the
     fake benchmark to run and fake `benchstat` must report a >20% regression so
     the gate enters retry. The fake `go test` should mutate the loadavg fixture
     after attempt 1, before attempt 2's preflight, so attempt 2 exits 2 before
     its benchmark command runs. Expected counts are exactly one fake `go test`
     and one fake `benchstat`, with final `exit 2`; assert the second-attempt
     fake `go test` did not run.
   - Keep compare-file self-tests single-pass and unchanged; compare-file mode
     never reads `BENCH_LOADAVG_FILE` and never runs the fixture guard.
   - Add a concrete `scripts/gates.sh` propagation assertion. Prefer a static
     assertion that `run_bench_gate` preserves `scripts/check-bench.sh`'s status
     via `|| return $?` and that `section()` exits with the captured status; if a
     hermetic dynamic assertion is practical without running the whole aggregate,
     it may be used instead.
   - Add a non-overridable-constant assertion: run a fake real-mode self-test
     with `BENCH_PKG_PARALLELISM=99` and
     `BENCH_MAX_LOAD_PER_EFFECTIVE_CPU=0.99` exported in the environment and
     prove diagnostics and behavior still use `1` and `0.50`.
5. Code last:
   - Add a hard benchmark package-parallelism constant to
     `scripts/check-bench.sh`, fixed at `1` and not env-overridable. Add an
     inline comment if the constant uses a `BENCH_` prefix, so operators do not
     confuse it with overridable knobs such as `BENCH_THRESHOLD`.
   - Add a hard busy-host load constant fixed at `0.50` and not
     env-overridable. Implement both new constants as unconditional script
     assignments, not `${VAR:-...}` defaults; environment variables with the
     same names must not change the gate. The inline comment must explain the
     asymmetry with `BENCH_THRESHOLD`: `BENCH_THRESHOLD` remains a loud local
     experimentation override, while package parallelism and busy-host load are
     physical validity constraints and are not knobs.
   - Pass `-p="$BENCH_PKG_PARALLELISM"` to real benchmark `go test` attempts.
   - Update `scripts/check-bench.sh` header comments so the documented command
     and exit-code meanings mention `-p=1` and busy-host/unavailable-preflight
     `exit 2`, not only generic usage/tooling errors.
   - Add a per-attempt busy-host preflight inside the real benchmark attempt
     path. It must read `/proc/loadavg` in normal real mode, read
     `BENCH_LOADAVG_FILE` only when `BENCH_SELF_TEST=1`, reject
     `BENCH_LOADAVG_FILE` without that sentinel, evaluate 1-minute load against
     `BENCH_MAX_LOAD_PER_EFFECTIVE_CPU=0.50`, divide by `effective_gomaxprocs()`,
     exit 2 with an actionable error before sample collection when the host is
     too busy, exit 2 when `/proc/loadavg` or the effective CPU divisor is
     unavailable in real mode, and avoid recording process command lines or
     private paths. Unavailable loadavg diagnostics must name the selected source
     (`/proc/loadavg` or the fixture), why it is invalid/unavailable, and that
     this is a Linux workstation benchmark gate. Unavailable CPU-divisor
     diagnostics must name the failed `effective_gomaxprocs()` probe result and
     the operator action.
   - Preflight order is part of the contract:
     1. If `BENCH_LOADAVG_FILE` is set and `${BENCH_SELF_TEST:-}` is not exactly
        `1`, exit 2 before any host interaction.
     2. If `${BENCH_SELF_TEST:-}` is exactly `1` and `BENCH_LOADAVG_FILE` is not
        set, exit 2 before any host interaction.
     3. Resolve the load source (`/proc/loadavg` in normal mode, fixture in
        self-test mode), validate it, then resolve/cache effective `GOMAXPROCS`.
     4. Compare the 1-minute load against the computed threshold and either
        exit 2 or proceed to diagnostics and benchmark collection.
     Tests must prove the misconfiguration paths do not emit host-derived
     `effective GOMAXPROCS` or loadavg diagnostics before the sentinel error.
   - Use a float-safe comparison for the load threshold and treat equality as
     failing closed. Use `awk` or an equivalent float-safe comparator with
     `load >= threshold` semantics; do not use integer arithmetic or string
     comparison. Extend `loadavg_values()` to accept the guarded self-test
     fixture so diagnostics and gating read the same load source. Do not reuse
     the current `/proc/loadavg`-only helper unchanged for the gating decision.
     Reuse the existing `effective_gomaxprocs()` helper, but cache the effective
     CPU divisor per real benchmark attempt so the script does not run the helper
     probe twice for the same attempt.
   - The busy-host error and diagnostics must include: actual 1-minute load,
     effective `GOMAXPROCS`, computed threshold, package parallelism, the
     pass/fail comparison, and the operator action ("wait for a quieter window or
     request approval to stop the exact workload"). They must not include process
     command lines.
   - Normal benchmark diagnostics should include the package parallelism and
     busy-host threshold values even when the preflight passes, so passing and
     failing runs carry comparable audit context.
6. Validation:
   - `bash scripts/test/check-bench-test.sh`
   - `bash scripts/check-bench.sh` on a quiet enough host. If the preflight
     exits 2, record that as correct gate behavior and rerun only after the host
     settles; do not treat a busy-host exit as a code regression. A quiet enough
     host means the preflight sees 1-minute load below
     `0.50 * effective_GOMAXPROCS`.
   - `bash scripts/spec-drift.sh`
   - `bash scripts/scan-secrets.sh`
   - `git diff --check`
   - Confirm `scripts/gates.sh` still propagates benchmark-gate `exit 2`
     through `section()` / `run_bench_gate` without modification.
   - Record the benchmark section wall-clock in the validation log after the
     corrected gate runs on a quiet host, so the local-gate cost of `-p=1` is
     visible.
   - Run full `scripts/gates.sh` on a quiet enough host before closing the SOW.
     If the benchmark preflight exits 2, record that as correct gate behavior and
     rerun only after the host settles or after explicit operator approval to
     stop the exact contending workload.
   - Focused aiagent_v2 benchmark comparison remains below threshold as
     supporting evidence, not as the gate replacement.

Validation plan:

- `bash scripts/check-bench.sh`
- Focused `go test` benchmarks for the affected adapter packages when needed.
- `bash scripts/test/check-bench-test.sh`
- `bash scripts/spec-drift.sh`
- `git diff --check`
- Any modified gate self-tests if scripts change.

Artifact impact plan:

- AGENTS.md: add benchmark busy-host `exit 2` guidance so future sessions do not
  misread environment-invalid benchmark evidence as an adapter regression, and
  mention that the gate uses `-p=1` package-binary serialization. Place the note
  adjacent to the Go bench quality-gate row or immediately below it, so future
  sessions see it with the benchmark gate summary.
- Runtime project skills: update `project-quality-gates` and `project-testing`
  to document `-p=1` and busy-host fail-closed behavior.
- Specs: update `.agents/sow/specs/quality-gates.md` and
  `.agents/sow/specs/testing-strategy.md` to document `-p=1` and busy-host
  fail-closed behavior where they describe the local benchmark gate.
- Benchmark baseline: provenance-only header correction in `bench/baseline.txt`;
  update the baseline header command block to include `-p=1`; no benchmark sample
  values are changed.
- Benchmark docs: update `bench/README.md` benchmark command to include `-p=1`.
- `scripts/gates.sh`: no code change expected; verify the existing aggregate
  propagation keeps busy-host `exit 2` fail-closed.
- End-user/operator docs: not expected.
- End-user/operator skills: not expected.
- SOW lifecycle: keep open until the benchmark gate blocker is proven resolved or correctly reclassified with evidence.

Open-source reference evidence:

- Not checked yet. This SOW remains an evidence-classification task over local
  benchmark output and local git history. External references become relevant
  only if the plan proposes a new benchmark harness beyond Go's documented
  `-cpu` and `-p` controls plus a local busy-host fail-closed preflight.

Open decisions:

- No technical/product decision is required. The long-term-best technical choice
  is to keep the benchmark evidence conservative and fail closed on a busy host.
  If validation remains blocked because unrelated workstation workload keeps the
  host above the threshold, stopping that workload is a separate operational
  approval and must be requested explicitly.

## Implications And Decisions

No technical/product decision is required. This is a long-term-best technical
correction: the project must not call the SOW-0097 lineage finished while
benchmark-gate debt is unclassified. The corrected decision is
investigation-first: do not change `scripts/check-bench.sh`,
`bench/baseline.txt`, specs, or skills until the root-cause class is proven.
Operationally, if the final validation gate cannot run because the workstation
stays above the conservative load threshold, the SOW remains open until a quiet
window exists or the operator explicitly approves stopping the exact workload.

## Plan

1. Record the post-baseline code-change survey and benchmark evidence.
2. Repair the benchmark gate to enforce package benchmark serialization with
   `go test -p=1` and to fail closed before real-mode runs on a busy host, after
   the gap and implementation-plan reviewer gates converge.
3. Prove the repair with benchmark self-tests and a real benchmark gate on a
   quiet enough host.
4. If the corrected gate still fails on a quiet host, continue
   adapter/baseline investigation in this SOW before considering any sample
   refresh.

## Execution Log

### 2026-06-25

- Created from SOW-0105 close-out because `scripts/check-bench.sh` reproduced adapter scan regressions after retry.
- Gap/plan review round 1:
  - `glm`: `NEEDS GAP WORK` / `PLAN NEEDS CHANGES`.
  - `minimax`: technical non-vote; process ended before a final vote was
    captured.
  - `kimi`: `NEEDS GAP WORK` / `PLAN NEEDS CHANGES`.
  - `mimo`: `NEEDS GAP WORK` / `PLAN NEEDS CHANGES`.
  - `deepseek`: `NEEDS GAP WORK` / `PLAN NEEDS CHANGES`.
  - `qwen`: technical non-vote; process ended before a final vote was captured.
- Accepted reviewer findings:
  - Investigate post-baseline adapter changes before assuming load is the cause.
  - Cite the SOW-0097 high-load benchmark precedent and existing retry behavior.
  - Do not add a load preflight before proving the existing retry is
    insufficient.
  - Define threshold, exit code, closure semantics, and hermetic tests only if a
    future plan proposes benchmark-gate behavior changes.
  - Initially treat the `Pre-refresh repository HEAD` baseline header as
    ambiguous provenance, not as proof by itself that the SOW-0097 baseline
    samples were generated from stale code. Round 3 later resolved this as stale
    SOW-0058 provenance while proving SOW-0097 sample refresh did happen.
- Investigation update:
  - Full-suite benchmark rerun still failed, but the reproduced benchmark set
    changed and the final reproduced failure was `Tail_SyntheticAppend
    +36.08% sec/op` under high load. Allocation and byte counts were unchanged.
  - Focused aiagent_v2 tail and aiagent_v2 full-package reruns did not reproduce
    a >20% regression.
  - The benchmark script's real `go test` command lacks `-p=1`; Go package-list
    mode can run test binaries in parallel by default. This contradicts the
    intended serial-hot-path benchmark behavior, though specs/skills currently
    document only `-cpu=1`.
  - Revised plan was to repair the gate to enforce package-level serial
    execution, not to refresh the baseline or widen the threshold.
- Gap-analysis round 2:
  - `glm`: `NEEDS WORK`.
  - `kimi`: `NOTHING MORE CAN BE DONE` with P3 follow-ups.
  - `mimo`: `NOTHING MORE CAN BE DONE` with plan refinements.
  - `qwen`: `NEEDS WORK`.
  - `minimax`: technical non-vote; process ended before a final vote was
    captured.
  - `deepseek`: technical non-vote; process ended before a final vote was
    captured.
  - Accepted reviewer findings:
    - The `-p=1` root cause had to be tested with a full-suite diagnostic before
      implementation planning.
    - The SOW had to stop describing `-p=1` as already documented; the existing
      contract documented `-cpu=1`, and `-p=1` is a deliberate strengthening.
    - The plan needed a hard, non-overridable `BENCH_PKG_PARALLELISM=1`, static
      and dynamic self-test enforcement, explicit wall-clock impact, CI
      compile-smoke distinction, and a branch plan if `-p=1` did not close the
      gate.
- Follow-up investigation after round 2:
  - Full-suite `go test -p=1 ...` ran for 838 seconds under high host load and
    still compared aiagent_v2 `Tail_SyntheticAppend +73.85% sec/op`, with
    unchanged bytes/op and allocs/op.
  - Focused aiagent_v2 tail-only rerun immediately after that long diagnostic
    compared `+13.44% sec/op`, below threshold, with unchanged bytes/op and
    allocs/op.
  - Focused aiagent_v2 package rerun under high load compared
    `Scan_SyntheticCorpus +8.54% sec/op` and
    `Tail_SyntheticAppend +24.24% sec/op`, with unchanged bytes/op and
    allocs/op.
  - Read-only process inspection showed unrelated high-CPU workstation
    consumers, so the latest red benchmark evidence is not credible proof of an
    adapter code regression.
  - The revised plan adds a fail-closed busy-host preflight in addition to
    `-p=1`; it still does not refresh the baseline or widen the threshold.
- Gap-analysis round 3:
  - `glm`: `NEEDS WORK`.
  - `mimo`: `NOTHING MORE CAN BE DONE` with P3/P2-for-plan notes.
  - `deepseek`: `NEEDS WORK`.
  - `qwen`: `NEEDS WORK`.
  - `minimax`: `NEEDS WORK`.
  - `kimi`: technical non-vote; process ended before a final vote was captured.
  - Accepted reviewer findings:
    - The busy-host preflight needs a concrete metric, threshold, per-attempt
      cadence, non-`/proc/loadavg` behavior, and retry interaction before plan
      review.
    - The SOW must distinguish "unsupported benchmark evidence under load" from
      "predict every focused pass/fail from loadavg"; above-threshold runs are
      invalid regardless of their outcome.
    - The post-baseline code survey must list the full `internal/adapters`,
      `internal/ingest`, `internal/parity`, and `bench` surface, not only the
      adapter hot-path subset.
    - Baseline provenance is resolvable: SOW-0097 refreshed samples, while the
      `Pre-refresh repository HEAD: e783894bfe3e` line is stale SOW-0058
      provenance. A provenance-only header correction is distinct from a sample
      refresh.
    - SOW-0058 and SOW-0104 context must be explicit.
- Gap-analysis round 4:
  - `kimi`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `mimo`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `deepseek`: `NOTHING MORE CAN BE DONE`.
  - `qwen`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `minimax`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `glm`: `NEEDS WORK`.
  - Accepted reviewer findings:
    - The busy-host threshold is close to the workstation's observed resting load.
      The SOW must quantify that operator-workflow impact and explicitly choose
      whether to keep the conservative threshold or raise it. The chosen
      disposition is to keep the conservative threshold because the documented red
      full-suite run began in the above-threshold band; raising the limit would
      admit known-bad evidence.
    - Existing fake real-mode benchmark self-tests would become host-load-flaky
      unless they inject a below-threshold load fixture. The plan must define the
      fixture mechanism and retrofit those assertions.
    - The preflight divisor must deliberately use either raw `nproc` or the
      script's existing `effective_gomaxprocs()` helper. The chosen disposition is
      `effective_gomaxprocs()` so diagnostics, cgroup-aware CPU availability, and
      gate math use the same divisor.
- Gap-analysis round 5:
  - `mimo`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `deepseek`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `qwen`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `minimax`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `kimi`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `glm`: `NEEDS WORK`.
  - Accepted reviewer findings:
    - The baseline header's `# Benchmark command:` block is a contract artifact.
      Adding `-p=1` to the real gate command while leaving that comment block at
      the old command would create silent drift. The SOW now requires a
      header-comment update that includes `-p=1`, without changing samples.
    - `BENCH_LOADAVG_FILE` cannot be described as test-only unless the script has
      a way to distinguish hermetic self-tests from normal real mode. The SOW now
      requires a `BENCH_SELF_TEST=1` sentinel; `BENCH_LOADAVG_FILE` without the
      sentinel exits 2 before fake or real `go test`.
- Gap-analysis round 6:
  - `glm`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `minimax`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `kimi`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `mimo`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - `deepseek`: `NOTHING MORE CAN BE DONE`; no P3 called out.
  - `qwen`: `NOTHING MORE CAN BE DONE` with P3 notes.
  - No accepted P0/P1/P2 findings remain. P3 notes are reserved for the
    implementation-plan gate: optional baseline-header alignment self-test,
    explicit compare-file exclusion for the `BENCH_LOADAVG_FILE` guard,
    explicit above-threshold-attempt-1 assertion, `BENCH_SELF_TEST=1` without
    fixture behavior, busy-host error-message fields, diagnostic threshold fields,
    `scripts/gates.sh` exit-2 propagation note, and SOW-0105 batch-commit wording.
- Implementation-plan review round 1:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `kimi`: `NEEDS WORK` by disposition because it identified an accepted P2
    durable-memory gap, despite a positive headline vote.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `mimo`: technical non-vote; process ended before a final vote was captured.
  - `qwen`: technical non-vote; process ended before a final vote was captured.
  - Accepted P2 findings:
    - Add `bench/README.md` to the artifact-impact plan and update its benchmark
      command prose to include `-p=1`.
    - Add `.agents/skills/project-testing/SKILL.md` to the artifact-impact plan
      because it is also durable benchmark-gate guidance.
    - Make the `AGENTS.md` update mandatory for the new busy-host `exit 2`
      operating rule, so future sessions do not misread environment-invalid
      benchmark evidence as adapter regression evidence.
  - Accepted P3 refinements:
    - Pin the fake `go test` argument-inspection mechanism for the dynamic
      `-p=1` test.
    - Add an exact-threshold fail-closed self-test.
    - Spell out retry-interaction fixture mutation and expected fake command
      counts.
    - Require a float-safe load comparison, helper reuse, per-attempt CPU divisor
      caching, and a comment for hardcoded non-overridable benchmark constants.
    - Verify `scripts/gates.sh` exit-2 propagation during validation.
- Implementation-plan review round 2:
  - `glm`: `NEEDS WORK`.
  - `minimax`: `READY FOR IMPLEMENTATION`; response contained severity-label
    inconsistency by naming "P2 non-blocking" refinements, so the substantive
    items were reviewed and useful parts were folded into the plan.
  - `kimi`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `mimo`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `qwen`: `READY FOR IMPLEMENTATION` with P3 notes.
  - Accepted P2 finding:
    - The code plan required fail-closed `exit 2` behavior for an unavailable
      effective CPU divisor and unavailable/unparseable loadavg source, but the
      test plan did not dynamically exercise those paths. The SOW now requires
      hermetic self-tests for fake `effective_gomaxprocs()` unavailable/non-zero
      behavior and empty/malformed/non-numeric loadavg fixtures.
  - Accepted defensive refinements:
    - Add a busy-host error-field self-test for the required diagnostic fields
      and absence of process command lines.
    - Make both new constants explicitly hardcoded and non-env-overridable.
    - Enumerate the quality-gates spec sentences that must be updated and add
      static durable-memory assertions.
    - Add `.agents/sow/specs/testing-strategy.md` to affected specs because it
      summarizes the local benchmark gate.
    - Resolve the `loadavg_values()` ambiguity: the gating reader must be
      sentinel-aware and must not reuse the current `/proc/loadavg`-only helper
      unchanged.
    - Update the `scripts/check-bench.sh` header comments and normal diagnostics
      to include `-p=1`, busy-host `exit 2`, package parallelism, and threshold
      context.
- Implementation-plan review round 3:
  - `glm`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `kimi`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `mimo`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `qwen`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `minimax`: `NEEDS WORK`.
  - Accepted findings:
    - Pin strict sentinel mechanics with explicit unset, empty, `0`, `true`, and
      `2` test cases. The implementation must use `${BENCH_SELF_TEST:-}` or
      equivalent nounset-safe parameter expansion and must not treat any
      non-empty sentinel value as enabled.
    - Pin retry-interaction mechanics: attempt 1 must produce a regression to
      enter retry, fake `go test` mutates the fixture after attempt 1, and
      expected counts are exactly one fake `go test`, one fake `benchstat`, and
      final `exit 2`.
    - Pin fake `go run` controls for unavailable effective CPU divisor:
      `FAKE_GOMAXPROCS=unavailable` and `FAKE_GO_RUN_FAIL=1`.
    - Pin exact-threshold comparison with a float-safe `load >= threshold`
      comparator.
    - Pin preflight ordering so sentinel errors occur before host interaction or
      host-derived diagnostics.
    - Add concrete `scripts/gates.sh` exit-2 propagation assertion.
    - Add env-override resistance test for the two new hard constants and an
      inline rationale explaining why they differ from `BENCH_THRESHOLD`.
    - Define the loadavg fixture validity contract and invalid cases.
    - Pin the `AGENTS.md` insertion point near the Go bench quality-gate row and
      clarify the `bench/baseline.txt` provenance correction.
- Implementation-plan review round 4:
  - `kimi`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `mimo`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `qwen`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `minimax`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `glm`: initial run exited without a captured final vote; technical retry
    voted `NEEDS WORK`.
  - Accepted P2 finding:
    - The code plan required per-attempt caching of `effective_gomaxprocs()`,
      but the test plan did not prove fake `go run` is called no more than once
      per real benchmark attempt. The SOW now requires `FAKE_GO_RUN_COUNT` and a
      cache-count assertion.
  - Accepted P3 refinements:
    - Spell out unavailable loadavg and unavailable effective-CPU diagnostic
      fields.
    - Explicitly enumerate negative second/third loadavg fields as invalid.
    - Prefer extending `loadavg_values()` so diagnostics and gating use the same
      source in self-test mode.
    - Add `scripts/scan-secrets.sh` and a quiet-host full `scripts/gates.sh` run
      to validation.
    - Require the AGENTS.md benchmark note to mention both busy-host `exit 2`
      and `-p=1` package-binary serialization.
- Implementation-plan review round 5:
  - `glm`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `minimax`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `kimi`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `mimo`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `deepseek`: `READY FOR IMPLEMENTATION` with P3 notes.
  - `qwen`: `READY FOR IMPLEMENTATION` with P3 notes.
  - No accepted P0/P1/P2 findings remain. P3 notes reserved for implementation
    hardening:
    - Add optional passing-path diagnostic assertions for package parallelism and
      busy-host threshold fields.
    - Assert busy-host diagnostics report the fixture's actual 1-minute load
      value in self-test mode.
    - Add an unreadable loadavg fixture case if portable in the hermetic shell
      test.
    - Keep the `scripts/gates.sh` propagation assertion concrete: check both
      `run_bench_gate` status preservation and `section()` captured-status exit.
    - Keep `FAKE_GO_RUN_COUNT` implemented with the same file-based counter
      style as the existing fake command counters.
- Implementation pass:
  - Updated specs and runtime skills before script code:
    `.agents/sow/specs/quality-gates.md`,
    `.agents/sow/specs/testing-strategy.md`,
    `.agents/skills/project-quality-gates/SKILL.md`, and
    `.agents/skills/project-testing/SKILL.md`.
  - Updated durable operator-facing surfaces:
    `AGENTS.md`, `bench/README.md`, and the comment header in
    `bench/baseline.txt`.
  - Implemented `scripts/check-bench.sh` changes:
    - hard `BENCH_PKG_PARALLELISM=1` and hard
      `BENCH_MAX_LOAD_PER_EFFECTIVE_CPU=0.50`;
    - real benchmark `go test` command now passes `-p="$BENCH_PKG_PARALLELISM"`;
    - strict `BENCH_SELF_TEST=1` guard for `BENCH_LOADAVG_FILE`;
    - loadavg fixture validation for missing, non-file, empty, short,
      non-numeric, and negative first/second/third fields;
    - per-attempt effective-CPU resolution and caching for diagnostics plus
      gating;
    - fail-closed busy-host preflight using `load >= threshold`, so exact
      threshold fails closed;
    - passing and failing diagnostics include package parallelism, busy-host
      threshold, comparison, load source, and no process command lines.
  - Extended `scripts/test/check-bench-test.sh` with dynamic fake real-mode
    coverage for every planned preflight branch, cache-count branch, retry
    interaction, compare-file isolation, env-override resistance, durable-memory
    assertions, and `scripts/gates.sh` exit propagation assertions.

## Validation

Acceptance criteria evidence:

- Root-cause classification for planning is benchmark-gate instability under
  workstation contention plus a gate self-contention defect. Quiet-host
  validation remains open; no code, benchmark sample, spec, or skill change has
  been made under this SOW.
- Static code-change survey:
  - `git diff --name-status 33439d5..HEAD -- internal/adapters/ internal/ingest/ internal/parity/ bench/` shows one post-baseline commit surface from the SOW-0099 through SOW-0102 follow-up batch. The full surface is the 17-file list recorded in Analysis and repeated in the Round-3 evidence below.
  - The adapter benchmark hot-path subset is Codex and aiagent_v2 only:
    `internal/adapters/codex/mapper_turn.go`,
    `internal/adapters/codex/types.go`, and
    `internal/adapters/aiagent_v2/mapper_ops.go`. The remaining files are adapter
    tests, ingest resolver source/tests, and parity source/tests, which are not on
    adapter `Scan`/`Tail` benchmark paths.
  - No post-baseline claude-code adapter file explains the simultaneous
    `ClaudeScan_SyntheticCorpus` regression.
  - The post-baseline follow-up commit recorded `scripts/gates.sh` as passing at
    commit time, so the current red benchmark result appeared after that green
    gate evidence.
- Baseline variance survey from `bench/baseline.txt`:
  - `ClaudeScan_SyntheticCorpus`: `n=6`, mean `26152932 ns/op`, `cv=6.24%`.
  - `CodexScan_SyntheticCorpus`: `n=6`, mean `6310821 ns/op`, `cv=12.34%`.
  - `Scan_SyntheticCorpus` (aiagent_v2): `n=6`, mean `142898053 ns/op`,
    `cv=3.02%`.
  - `OpencodeScan_SyntheticDB`: `n=6`, mean `49054060 ns/op`, `cv=1.72%`.
  - The regressing scan benchmarks are more variance-sensitive than aiagent_v2
    and opencode scans, but this does not by itself prove environment noise.
- Current machine state check before rerun: `uptime` reported load averages
  around `12.05 12.38 13.36` with unrelated CPU-heavy processes active. A new
  full wall-time benchmark run was intentionally not started under that load
  because it would not provide the lower-load evidence reviewers asked for.
- Fresh benchmark evidence:
  - `bash scripts/check-bench.sh` failed after retry. Attempt 1 load was around
    `13.91 15.65 18.75`; attempt 2 load was around `21.19 19.47 19.09`.
  - Attempt 1 regressions: `Tail_SyntheticAppend +35.37%`,
    `CodexScan_SyntheticCorpus +39.76%`,
    `CodexTail_SyntheticAppend +24.78%`, and
    `OpencodeScan_SyntheticDB +21.11%`.
  - Attempt 2 regressions included `Tail_SyntheticAppend +36.08%`,
    `ClaudeScan_SyntheticCorpus +32.84%`,
    `ClaudeTail_SyntheticAppend +20.88%`, and
    `OpencodeTail_SyntheticAppend +35.31%`; the gate failed on the reproduced
    `Tail_SyntheticAppend` name.
  - Focused `BenchmarkTail_SyntheticAppend` for aiagent_v2 compared at
    `+14.94% sec/op`, below threshold.
  - Focused aiagent_v2 package benchmark compared at
    `Scan_SyntheticCorpus +8.65% sec/op` and
    `Tail_SyntheticAppend +9.42% sec/op`, both below threshold.
  - Bytes/op and allocs/op remained unchanged for the aiagent_v2 focused runs.
  - `go help build` documents `-p n` as controlling how many test binaries can
    run in parallel, defaulting to `GOMAXPROCS`; the script currently omits it.
- Round-2 falsification evidence:
  - Full-suite `go test -p=1 -run='^$' -bench=. -benchmem -count=6 -cpu=1`
    across the seven benchmark packages ran for 838 seconds and still produced
    `Tail_SyntheticAppend +73.85% sec/op` with unchanged bytes/op and allocs/op.
  - Focused aiagent_v2 tail-only rerun under similar high load produced
    `Tail_SyntheticAppend +13.44% sec/op`, below threshold, with unchanged
    bytes/op and allocs/op.
  - Focused aiagent_v2 package rerun produced
    `Tail_SyntheticAppend +24.24% sec/op` and
    `Scan_SyntheticCorpus +8.54% sec/op`, again with unchanged bytes/op and
    allocs/op.
  - A read-only process snapshot showed unrelated high-CPU workstation load,
    including the installed ai-viewer ingester. The process command lines are
    not recorded here to avoid private-path leakage.
- Round-3 evidence updates:
  - Complete post-baseline diff surface contains 17 files; only
    `internal/adapters/aiagent_v2/mapper_ops.go`,
    `internal/adapters/codex/mapper_turn.go`, and
    `internal/adapters/codex/types.go` are adapter hot-path source files.
  - `aiagent_v2/mapper_ops.go` is in the tail map path, but the post-baseline
    change is an O(1) display-name fallback and current red/focused runs match
    the SOW-0097 baseline allocation counts exactly.
  - SOW-0097 baseline samples are proven post-parity by allocation/sample
    changes in `git diff 33439d5^ 33439d5 -- bench/baseline.txt`; the stale
    `Pre-refresh repository HEAD` header line is provenance debt, not a sample
    refresh trigger.
  - Busy-host preflight threshold is now specified as 1-minute load below
    `0.50 * effective_GOMAXPROCS`. This threshold is intentionally conservative
    and invalidates every documented red high-load run as performance evidence;
    it may also invalidate focused runs that happened to pass under load.
  - Rounds 4 through 6 sharpened the preflight contract: the divisor is
    `effective_gomaxprocs()` rather than raw `nproc`, hermetic self-tests use
    `BENCH_SELF_TEST=1` plus a `BENCH_LOADAVG_FILE` fixture for
    below/above-threshold cases, the baseline header command block is updated to
    include `-p=1`, and the SOW now records that the threshold may block routine
    workstation runs until the host settles or the operator approves stopping the
    exact workload.

Tests or equivalent validation:

- `git diff --check -- .agents/sow/current/SOW-0106-20260625-benchmark-gate-regression-triage.md` passed after revising the SOW.
- Sensitive string scan over SOW-0106 for the user's personal name, private home
  path, and email-style attribution strings returned no hits.
- Gap-analysis review round 6 converged with all six reviewers voting
  `NOTHING MORE CAN BE DONE`; only P3 notes remain.
- Implementation-plan review round 5 converged with all six reviewers voting
  `READY FOR IMPLEMENTATION`; only P3 notes remain.
- `bash -n scripts/check-bench.sh && bash -n scripts/test/check-bench-test.sh`
  passed.
- `bash scripts/test/check-bench-test.sh` passed: 89/89 assertions.
- `git diff --check` passed.
- `bash scripts/spec-drift.sh` passed:
  no spec/code drift across all six indicators.
- `bash scripts/scan-secrets.sh` passed:
  no secrets or operator-PII in tracked files.
- `bash scripts/check-bench.sh` executed the corrected real gate:
  - Attempt 1 preflight passed with load `11.46 < 12.00`
    (`effective_GOMAXPROCS=24`, `-p=1`, `-cpu=1`).
  - Attempt 1 produced first-run regressions:
    `CodexTail_SyntheticAppend +25.09% sec/op` and
    `OpencodeTail_SyntheticAppend +22.06% sec/op`.
  - Attempt 2 preflight correctly exited 2 before sampling because load rose to
    `15.16 >= 12.00`.
  - This is correct invalid-measurement behavior, not a proven code regression.
    The SOW remains open until the corrected gate can run on a quiet enough host
    and either pass or reproduce a real regression under valid preflight
    conditions.
- A second `bash scripts/check-bench.sh` rerun also executed the corrected real
  gate:
  - Attempt 1 preflight passed with load `11.32 < 12.00`.
  - Attempt 1 produced one first-run regression:
    `ClaudeScan_SyntheticCorpus +22.08% sec/op`.
  - Attempt 2 preflight correctly exited 2 before sampling at the exact boundary:
    `12.00 >= 12.00`.
  - A later no-load wait still showed `12.09 >= 12.00`, so a third immediate
    rerun would be expected to fail preflight and was not started.
- A third `bash scripts/check-bench.sh` rerun executed after a longer quiet
  window:
  - Attempt 1 preflight passed with load `10.96 < 12.00`.
  - No benchmark had a statistically-significant `sec/op` regression above 20%.
  - The gate exited 0: `BENCH GATE: PASS`.
  - Full `scripts/gates.sh` was started after load dropped below threshold:
  - It passed through lint formatter-scope self-test, `lint.sh` Go/frontend
    static analysis, secrets self-test and live scan, AI-attribution scan,
    contract-matrix self-test, spec-drift self-test and live scan, ingestion
    parity self-test and fixtures, Codacy coverage-upload self-test, Codacy
    config self-test, system installer, systemd unit lint, and build/bundle-size.
  - It then failed correctly at the benchmark regression gate preflight before
    sampling because the earlier aggregate sections had raised load to
    `12.61 >= 12.00`.
  - This is correct invalid-measurement behavior. Full aggregate closure remains
    pending until a quieter window lets the benchmark section collect valid
    evidence inside `scripts/gates.sh`.
- Implementation adjustment on 2026-06-26:
  - The original implementation plan expected no `scripts/gates.sh` code change
    because exit-code propagation was already correct.
  - Validation proved a separate aggregate-ordering defect: lint, scans,
    parity, installer, systemd, and build sections can raise workstation load
    enough to make the benchmark preflight correctly fail before sampling.
  - The long-term-best correction is to run the benchmark gate immediately after
    the cheap formatter-scope self-test, before lint/static analysis, build,
    `-race`, fuzz, and Playwright sections. This does not weaken or remove any
    gate; it preserves benchmark measurement validity inside the full aggregate.
  - The quality-gates spec and runtime skill now document the new ordering, and
    `scripts/test/check-bench-test.sh` now asserts the order against
    `scripts/gates.sh`.
- A bounded 30-minute quiet-window wait was run after the full aggregate
  preflight block. It required 1-minute load below `8.0` before rerunning the
  full gate, to leave headroom for aggregate setup work before the benchmark
  section. The wait timed out without a qualifying sample. Observed 1-minute
  load values stayed in the noisy range and included `11.87`, `10.16`, `12.57`,
  `9.49`, `12.20`, `26.37`, `14.30`, `9.65`, `11.72`, and `11.51`. A
  command-name-only process snapshot during the wait showed unrelated CPU
  consumers, including virtual machine, browser, installed ai-viewer ingester,
  system monitoring/network-update, unrelated reviewer, and unrelated Go test
  processes. No process was stopped. Full aggregate closure remains blocked on a
  quieter workstation window or explicit operator approval to stop the exact
  contending workload.
- Refreshed cheap, non-benchmark validation while the workstation remained too
  busy for valid benchmark evidence:
  - `git diff --check` passed.
  - `bash -n scripts/check-bench.sh && bash -n scripts/test/check-bench-test.sh
    && bash scripts/test/check-bench-test.sh` passed: 89/89 assertions.
  - `bash scripts/spec-drift.sh` passed: no drift across all six indicators.
  - `bash scripts/scan-secrets.sh` passed: no secrets or operator-PII in tracked
    files.
- After the aggregate-ordering correction:
  - `bash -n scripts/gates.sh scripts/check-bench.sh
    scripts/test/check-bench-test.sh` passed.
  - `bash scripts/test/check-bench-test.sh` passed: 98/98 assertions. New
    assertions cover `scripts/gates.sh` ordering, `LC_ALL=C`, and absence of the
    orphaned `loadavg_values()` helper, so the benchmark section stays before
    lint/static analysis, build, `-race`, and Playwright and the round-1 cleanup
    remains guarded.
  - `git diff --check` passed.
  - `bash scripts/spec-drift.sh` passed: no drift across all six indicators.
  - `bash scripts/scan-secrets.sh` passed: no secrets or operator-PII in tracked
    files.
  - Full `scripts/gates.sh` was run after passively waiting for a valid
    benchmark window (`/proc/loadavg` 1-minute load below 9.50). It started at
    load `7.14 10.35 10.88`.
  - The benchmark section ran second, immediately after the lint formatter-scope
    self-test. Its real preflight passed with load `7.14 < 12.00`,
    `effective_GOMAXPROCS=24`, `-p=1`, and `-cpu=1`.
  - `scripts/check-bench.sh` passed inside the aggregate in 188 seconds: no
    benchmark had a statistically-significant `sec/op` regression above 20%.
  - The rest of the aggregate passed: Go lint/security/vulnerability checks,
    frontend lint/typecheck, bundle-size self-test, coverage-config checks,
    secret and AI-attribution scans, spec drift, parity fixtures, Codacy
    self-tests, installer/systemd checks, build/bundle-size, Go `-race`, Go
    coverage, adapter fuzz seed corpus, and Playwright E2E/axe.
  - Go race/coverage evidence from the aggregate: Go tests passed race-clean,
    `coverage.out` total was 81.4%, and `scripts/check-coverage.sh` passed with
    gated `internal/*` aggregate coverage 85.4%.
  - Frontend test evidence from the aggregate: Vitest passed 76 files / 929
    tests with 89.18% statement coverage, and Playwright passed 42 chromium E2E
    and axe tests.
  - Aggregate timing: total 522 seconds. The benchmark section was 188 seconds;
    the long-pole correctness section remained `test.sh` at 118 seconds. The
    documented >5-minute total remains expected and is not a gate failure.

Reviewer findings:

- Gap-analysis review rounds 1 through 5 did not converge. Accepted findings are
  recorded in the execution log above.
- Gap-analysis review round 6 converged: six of six reviewers voted
  `NOTHING MORE CAN BE DONE`.
- Implementation-plan review round 5 converged: six of six reviewers voted
  `READY FOR IMPLEMENTATION`.
- Implementation review round 1:
  - `mimo`, `kimi`, and `minimax` voted `PRODUCTION GRADE`.
  - `glm` voted `NEEDS WORK` because benchmark-gate durable-memory text had
    landed earlier under the SOW-0105 commit before the script implementation was
    committed. This is a real process finding, not a runtime defect in the final
    working tree. Rewriting pushed history is out of scope and riskier than the
    defect. Disposition: accepted. This SOW records the split explicitly, and
    `scripts/test/check-bench-test.sh` now acts as the benchmark-gate
    drift-prevention guard by asserting that the script, specs, skills,
    `AGENTS.md`, `bench/README.md`, `bench/baseline.txt`, CI benchmark presence
    check, and aggregate gate order all describe the same benchmark contract.
  - `mimo`, `kimi`, `minimax`, and `glm` flagged the orphaned
    `loadavg_values()` helper as P3 dead code. Disposition: accepted and fixed by
    deleting the unused helper.
  - `mimo` and `glm` flagged locale-dependent awk numeric parsing as P3
    defensive hardening. Disposition: accepted and fixed by pinning
    `LC_ALL=C` in `scripts/check-bench.sh`.
  - `kimi` and `minimax` suggested an additional unreadable regular-file fixture
    for the loadavg source. Disposition: dismissed as duplicate coverage for the
    same branch. The existing non-file fixture exercises the
    `! -f || ! -r` failure path portably; a chmod-based unreadable-file test is
    less portable because privileged runs can still read it.
- Implementation review round 2 converged: `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen` all voted `PRODUCTION GRADE`.
  - `glm` and `mimo` noted that the validation ledger still recorded the
    pre-cleanup assertion count. Disposition: accepted and fixed above; the
    current self-test is 98/98.
  - `glm` noted that a synthetic loadavg file without a trailing newline would be
    rejected as malformed. Disposition: dismissed as P3. Real `/proc/loadavg`
    includes a trailing newline, all hermetic fixtures write one, and the gate
    still fails closed with no privacy or correctness risk.
  - `kimi` noted that `mktemp` failure in `run_real_bench_attempt` would produce
    a less-specific failure message. Disposition: dismissed as P3 and
    pre-existing. The path still fails closed; adding clearer exceptional-path
    wording is not needed for this SOW.
  - `deepseek`, `qwen`, and `minimax` raised only P3 maintainability/cosmetic
    notes: naming of numeric helper functions, redundant loadavg diagnostic
    detail, long positional Bash test helper arguments, global variables between
    Bash helpers, generic forbidden-pattern wording, and header line length.
    Disposition: dismissed. They do not affect correctness, security, privacy,
    performance, or contract coverage.

## Outcome

Completed. The benchmark gate now serializes package benchmark binaries with
`-p=1`, fails closed with `exit 2` before sampling when workstation load or
effective-CPU evidence cannot support valid wall-time measurements, preserves
that exit code through `scripts/gates.sh`, and runs early in the aggregate before
CPU-heavy correctness gates can raise load. The gate still fails on real
statistically-significant `>20% sec/op` regressions, compare-file mode remains
single-pass, and drift guards bind the script to specs, skills, `AGENTS.md`,
`bench/README.md`, and `bench/baseline.txt`.

## Lessons Extracted

- Spec, skill, and operator-contract updates for a gate must land in the same
  commit as the executable gate behavior they describe. If a preparatory commit
  accidentally creates durable-memory drift, do not rewrite pushed history by
  default. Close the SOW by documenting the split, landing the implementation and
  drift guard together, and making the commit message reference the SOW that owns
  the final behavior.

## Followup

None yet.

## Regression Log

None yet.
