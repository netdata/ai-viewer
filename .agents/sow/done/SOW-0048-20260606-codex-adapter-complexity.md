# SOW-0048 - Codex Adapter Complexity Reduction

## Status

Status: completed

Sub-state: completed. Ordered slices complete: Codex benchmark baseline first,
scanner/tailer hot-path decomposition second, review fixes third, final
validation and review convergence last.

## Requirements

### Purpose

Keep the Codex adapter maintainable and safe before it grows more source-format
surface area.

### User Request

Continue reducing code-scanning complexity findings autonomously, SOW by SOW,
without weakening tests, performance gates, or security posture.

### Assistant Understanding

Facts:

- SOW-0047 closeout measured 141 strict source-only Lizard warnings after
  excluding tests and generated embedded frontend assets.
- The Codex adapter is the largest remaining single adapter cluster.
- Current strict warning evidence includes:
  `internal/adapters/codex/tailer.go` with 5 warnings,
  `scanner.go` with 3 warnings, `mapper_turn.go` with 3 warnings,
  `ops_tools.go` with 3 warnings, `stream.go` and `discovery.go` with 2 each,
  and single warnings in `cursor.go`, `mapper.go`, `ops_event.go`,
  `ops_enrich.go`, `ops_response.go`, `parser.go`, and `types.go`.

Inferences:

- The scanner/tailer warnings are high blast-radius because they cover file
  discovery, line streaming, cursor offsets, and real-time updates.
- A Codex adapter benchmark baseline should be added before scanner/tailer
  decomposition, mirroring the Claude-code pattern from SOW-0047.

Unknowns:

- Which Codex warnings are true maintainability defects versus parser/tailer
  state-machine density must be determined by reading the adapter tests and
  current source.

### Acceptance Criteria

- Codex adapter complexity findings are ranked by risk with file/function
  evidence.
- Deterministic Codex `Scan` and `Tail` benchmarks exist before hot-path
  scanner/tailer refactors.
- Behavior-preserving refactors are covered by focused adapter tests, package
  race tests, fuzz seed corpus, benchmark gate, local Codacy/Lizard analysis,
  full gates, and external review convergence.
- Any remaining Codex complexity is justified in this SOW or split into a
  narrower follow-up.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.
- Existing Codex adapter warning files listed above.

Current state:

- Codex adapter complexity is now the highest adapter-specific residual
  complexity cluster.
- Strict production-only Lizard evidence (`lizard internal/adapters/codex/*.go`
  excluding tests, `-C 8 -L 50 -a 8 --warnings_only`) reports:
  - `internal/adapters/codex/scanner.go`: `scanAll` (44 NLOC, CCN 13),
    `readRollout` (81 NLOC, CCN 25, 7 params, 154 lines),
    `firstRecordIsSessionMeta` (32 NLOC, CCN 10).
  - `internal/adapters/codex/tailer.go`: `tailLoop` (68 NLOC, CCN 20),
    `handleEvent` (27 NLOC, CCN 9), `markExistingDirty` callback
    (26 NLOC, CCN 10), `addWatchTree` callback (29 NLOC, CCN 11),
    `flushDirty` (30 NLOC, CCN 9, 7 params).
  - Additional warnings exist in `cursor.go`, `discovery.go`, `mapper.go`,
    `mapper_turn.go`, `ops_enrich.go`, `ops_event.go`, `ops_response.go`,
    `ops_tools.go`, `parser.go`, `stream.go`, and `types.go`.
- `bench/baseline.txt`, `scripts/check-bench.sh`, CI benchmark presence
  checks, `quality-gates.md`, `testing-strategy.md`, and the
  `project-quality-gates` skill currently describe 7 benchmarks across
  5 packages and do not cover Codex scan/tail.

Risks:

- Tail/scan regressions can drop or replay source records.
- Parser regressions can hide malformed input or change canonical mapping.
- Benchmark baseline changes can add workstation-gate noise if the benchmark is
  not deterministic.

## Pre-Implementation Gate

Status: ready for first slice. Specs and runtime skill deltas below must land
before benchmark tests/tooling/code changes.

Problem / root-cause model:

- The Codex adapter combines source discovery, rollout streaming, cursor
  handling, parser dispatch, and mapper state transitions in a few dense files.
- The correct fix is not a semantic rewrite. It is benchmark-guarded
  decomposition around existing adapter contracts.
- The safe execution order is benchmark coverage first, then scanner/tailer
  helper extraction only after deterministic Codex scan/tail benchmarks exist.

Evidence reviewed:

- SOW-0047 closeout strict warning buckets and function list.
- Fresh source-only Lizard warning scan over `internal/adapters/codex/*.go`
  excluding tests, with exact scanner/tailer warning metrics listed in this
  SOW's Analysis section.
- Existing Codex tests cover golden fixture discovery, scanner resume,
  truncation, symlink containment, realtime tailing, catch-up, and parser fuzz
  seeds, but no Codex benchmark exists yet.
- Existing benchmark gate comments/specs/CI count guard identify exactly
  7 benchmark names across 5 packages.

Affected contracts and surfaces:

- Codex adapter `Scan`, `Tail`, cursor persistence, parser classification, and
  canonical event mapping.
- Benchmark inventory if Codex benchmarks are added to `scripts/check-bench.sh`.
- CI benchmark presence guard in `.github/workflows/ci.yml`.
- Local benchmark baseline in `bench/baseline.txt`.

Spec deltas to land before tests/code:

- `.agents/sow/specs/adapter-codex.md`: add a `Performance Benchmarks`
  section defining deterministic Codex scan/tail benchmark behavior, synthetic
  fixture requirements, exact event-count assertions, and the rule that
  fsnotify/timers stay outside the timed tail path.
- `.agents/sow/specs/quality-gates.md`: update the Go benchmark inventory from
  7 benchmarks across 5 packages to 9 benchmarks across 6 packages, adding
  Codex `Scan` and `Tail`.
- `.agents/sow/specs/testing-strategy.md`: update the performance regression
  test inventory from 7/5 to 9/6 and name the Codex benchmark file.
- `.agents/skills/project-quality-gates/SKILL.md`: keep the runtime checklist
  aligned with the 9-benchmark inventory.

Existing patterns to reuse:

- Claude-code benchmark prerequisite and decomposition sequence from SOW-0047.
- Adapter spec/test workflow in `.agents/skills/project-adapters/SKILL.md`.
- Existing adapter benchmark shape from `internal/adapters/claude_code` and
  `internal/adapters/aiagent_v2`: deterministic temp-tree corpus, exact emitted
  event counts, `b.ReportMetric`, `b.SetBytes`, and no operator data.

Risk and blast radius:

- High within the Codex adapter. Schema, REST, SSE, and frontend changes are not
  expected.
- The first slice blast radius is intentionally limited to benchmark tests,
  local benchmark gate inventory, CI benchmark presence count, baseline, specs,
  skill, and this SOW.
- Later slices inside this SOW are limited to behavior-preserving scanner/tailer
  helper extraction inside `internal/adapters/codex/`; no schema, REST, SSE,
  frontend, or source-format interpretation change is allowed.

Sensitive data handling plan:

- Use synthetic or already-sanitized Codex fixtures only. Do not write raw
  source transcripts or prompts to durable artifacts.
- Benchmark fixtures must use fake UUIDs, example paths, and generic message
  content only.

Implementation plan:

1. Land the spec/skill updates listed above.
2. Add `internal/adapters/codex/bench_test.go` with:
   - `BenchmarkCodexScan_SyntheticCorpus` for first backfill over a synthetic
     `sessions/YYYY/MM/DD/rollout-*.jsonl` corpus.
   - `BenchmarkCodexTail_SyntheticAppend` for deterministic append/flush over
     already-discovered rollout files.
3. Wire `./internal/adapters/codex/` into `scripts/check-bench.sh`.
4. Update the CI benchmark presence guard from 7 to 9 benchmark names.
5. Refresh `bench/baseline.txt` in this SOW after benchmark code lands.
6. Only after benchmark coverage is green, decompose scanner/tailer in narrow
   follow-up slices inside this SOW.

Validation plan:

- `go test ./internal/adapters/codex -run='^$' -bench='BenchmarkCodex' -benchmem -count=1`
- `go test ./internal/adapters/codex -count=1`
- `go test -race -count=1 ./internal/adapters/codex`
- `go test -run='^Fuzz' ./internal/adapters/...`
- `scripts/check-bench.sh` after `bench/baseline.txt` refresh; any unrelated
  workstation-noise failure must be recorded with exact benchmark evidence and
  rerun before completion.
- Direct strict Lizard on changed Codex production and test files.
- Local Codacy analysis on changed files.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: `adapter-codex.md`, `quality-gates.md`, and `testing-strategy.md`.
- Runtime project skills: `project-quality-gates`.
- End-user docs: likely unaffected.
- SOW lifecycle: move to `current/` now; move to `done/` only after all gates
  and external reviews converge.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes Codex
  format interpretation, inspect upstream source or mirrored repositories first
  and cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Reviews

Round 1 external review:

- Reviewer A found no scanner/tailer correctness, cursor replay, symlink
  containment, or watcher regression. Findings addressed:
  - SOW scope drift: this SOW now records the ordered benchmark-first,
    scanner/tailer-second execution.
  - Benchmark gate caveat: final validation must capture a clean benchmark-gate
    run before completion.
  - Baseline scope: `bench/baseline.txt` now preserves all non-Codex blocks from
    the prior baseline and adds only the Codex block in this SOW.
- Reviewer B found no behavior/security defect. Findings addressed:
  - Codex benchmarks now report `peak_heap_mb`.
  - `flushDirty` no longer carries the dead `root` parameter.
  - The tail benchmark now drains exactly the expected event count and fails
    clearly on under/over-emission.
- Reviewer C found no correctness, security, race, benchmark-methodology, or
  spec-drift defect. Finding addressed:
  - `probeLineSessionMeta` has a restored comment explaining why malformed first
    concrete lines are treated as `not session_meta` for rule #24 rather than a
    fatal scan error.
- The first replacement reviewer command failed with no usable output and is not
  counted toward review coverage.

Round 2 external review:

- Reviewers found no scanner/tailer correctness, race, source-root containment,
  cursor replay, watcher, or benchmark-inventory blocker. Findings addressed:
  - SOW-0053 was tightened from activation-ready to drafted-only and now requires
    exact section-level spec deltas or unchanged attestations before activation.
  - SOW-0053 validation now names the Codex test files and behaviors that must
    pin residual cursor, discovery, stream, parser, mapper, and benchmark work.
  - `adapter-codex.md` now records the implementation's 5-second periodic tail
    sweep cadence, matching the other tailing adapters.
  - `scanner.go` now accurately documents that the first-record `size` argument
    short-circuits empty files while `readOneLine` bounds allocations and drains
    oversized lines.
- Reviewers correctly kept the benchmark gate and full gate as closeout
  blockers until final validation.

Final external review rerun:

- Three reviewers found no blocking correctness, race, security, cursor replay,
  watcher/tailer, benchmark methodology, spec drift, sensitive-data, or unwanted
  side-effect issue.
- Informational observations were accepted without code changes:
  - Codex tail `B/s` is an informational metric and event-count assertions are
    the semantic guard.
  - First-record oversized-line boundedness is already implemented by
    `readOneLine`; SOW-0053 will record an unchanged attestation if that area is
    touched.

## Outcome

Completed.

Implementation state:

- Added deterministic Codex scan/tail benchmarks and wired them into the local
  benchmark gate, CI benchmark presence guard, benchmark baseline, benchmark
  specs, and runtime quality-gate skill.
- Refactored `internal/adapters/codex/scanner.go`; the strict source-only
  Lizard warnings for `scanAll`, `readRollout`, and `firstRecordIsSessionMeta`
  are removed.
- Refactored `internal/adapters/codex/tailer.go` into package-local helper
  files; the strict source-only Lizard warnings for `tailLoop`, `handleEvent`,
  `markExistingDirty`, `addWatchTree`, and `flushDirty` are removed.
- Codex strict production warnings dropped from 25 to 17. Remaining
  mapper/stream/parser/discovery/cursor/type warnings are split to
  `.agents/sow/pending/SOW-0053-20260606-codex-residual-mapper-stream-complexity.md`.

Validation:

- `git diff --check` passes.
- `gofmt -l` on changed Codex Go files reports no files.
- `go test ./internal/adapters/codex -count=1` passes.
- `go test -race -count=1 ./internal/adapters/codex` passes.
- `go test ./internal/adapters/codex -run='^$' -bench='BenchmarkCodex' -benchmem -count=1` passes.
- Strict Lizard on changed Codex scanner/tailer/benchmark files reports zero
  warnings.
- Strict source-only Lizard on all Codex production files now reports no scanner
  or tailer warnings; 17 residual Codex warnings remain outside this SOW's final
  slice and are tracked in SOW-0053.
- `scripts/check-bench.sh` passes on rerun. The first final run failed on
  unrelated `aiagent_v2` benchmarks while review/gate processes overlapped;
  Codex scan/tail were neutral in both runs.
- Local Codacy analysis on changed files passes with 0 issues.
- Final `./scripts/gates.sh` passes on the completed SOW state before commit.
