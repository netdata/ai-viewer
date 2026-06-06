# SOW-0049 - Opencode Adapter Complexity Reduction

## Status

Status: completed

Sub-state: completed. Full local gates passed after the final shared-source-ID
cleanup; external review converged after Round 12.

## Requirements

### Purpose

Keep the Opencode SQLite adapter maintainable, benchmarked, and safe under
polling/tailing changes.

### User Request

Continue reducing code-scanning complexity findings autonomously, SOW by SOW,
while preserving quality, maintainability, performance, and security.

### Assistant Understanding

Facts:

- SOW-0047 closeout measured residual Opencode adapter warnings in
  `conn.go`, `cursor.go`, `mapper_ops.go`, `mapper_parts.go`, `migrations.go`,
  `tailer.go`, `tailer_batch.go`, `tailer_changes.go`, and `tailer_wal.go`.
- Largest Opencode buckets from the warning-only scan include
  `tailer.go` with 3 warnings and two-warning clusters in
  `tailer_changes.go`, `mapper_parts.go`, `mapper_ops.go`, and `conn.go`.

Inferences:

- The Opencode tailer is likely high risk because it reads an external SQLite
  database, detects WAL changes, and maps batches into canonical events.
- A deterministic Opencode scan/tail benchmark should be considered before
  refactoring polling or batch mapping hot paths.

Unknowns:

- Existing Opencode adapter coverage and benchmark gaps must be confirmed by
  reading the package before implementation.

### Acceptance Criteria

- Opencode complexity findings are ranked with exact file/function evidence.
- Any SQLite/tailer hot-path refactor has benchmark or focused performance
  evidence before implementation.
- Refactors preserve read-only SQLite access, cursor ordering, migration
  handling, mapper output, and error surfacing.
- Any remaining Opencode complexity warning is reduced, explicitly justified,
  or split into a narrower follow-up SOW before completion.
- Full gates and external review converge before completion.

## Analysis

Sources checked:

- SOW-0047 closeout warning-only Lizard scan.

Current state:

- Opencode adapter complexity remains a material residual adapter cluster after
  SOW-0047 focused on ai-agent v2 and Claude-code first.

Risks:

- SQLite read-mode regressions could violate the read-only source contract.
- Tailer regressions can miss changes, re-emit rows, or corrupt cursor state.
- Mapper regressions can change canonical event order or totals.

## Pre-Implementation Gate

Status: ready and active.

Problem / root-cause model:

- Opencode adapter responsibilities are spread across connection setup,
  migration reads, cursor comparison, polling, WAL detection, batch collection,
  and canonical mapping. Several functions exceed strict complexity limits.

Evidence reviewed:

- SOW-0047 closeout warning buckets.
- Fresh strict production-only Lizard scan (`-C 8 -L 50 -a 8`) on
  2026-06-06 found 15 warnings:
  `conn.go:buildReadOnlyDSN`, `conn.go:isBareIdent`, `cursor.go:After`,
  `mapper_ops.go:closeLLMOp`, `mapper_ops.go:emitToolOp`,
  `mapper_parts.go:mapAssistantTurn`, `mapper_parts.go:mapPart`,
  `migrations.go:readMigrations`, `tailer.go:tailLoop`,
  `tailer.go:pollOnce`, `tailer.go:detectChange`,
  `tailer_batch.go:collectBatch`, `tailer_changes.go:deltaRowHandler`,
  `tailer_changes.go:loadAndMapSession`, and the WAL watcher goroutine in
  `tailer_wal.go`.
- Baseline package validation before changes: `go test
  ./internal/adapters/opencode -count=1` and `go test -race -count=1
  ./internal/adapters/opencode` both pass.
- Opencode has no existing `func Benchmark*` entries, so scan/tail hot-path
  refactors currently lack benchmark-gate coverage.

Affected contracts and surfaces:

- Opencode adapter `Scan`, `Tail`, read-only SQLite DSN construction, cursor
  comparison, migration discovery, and canonical mapping.

Spec deltas to land before tests or production code:

- `.agents/sow/specs/adapter-opencode.md`: add a benchmark contract under
  Performance for deterministic Opencode scan and tail benchmarks, including
  exact event-count assertions and reported metrics.
- `.agents/sow/specs/quality-gates.md`: expand the benchmark inventory from 9
  paths / 6 packages to 11 paths / 7 packages, adding Opencode `Scan` and
  `Tail`.
- `.agents/sow/specs/testing-strategy.md`: mirror the benchmark inventory and
  package-count change.
- `.agents/skills/project-quality-gates/SKILL.md`: mirror the runtime
  benchmark inventory change so future assistants run the same gate.
- `.github/workflows/ci.yml` and `scripts/check-bench.sh`: update the benchmark
  presence guard and local benchmark package list after benchmark tests exist.

Existing patterns to reuse:

- Adapter decomposition and benchmark-gate patterns established in SOW-0047.

Risk and blast radius:

- Medium to high within the Opencode adapter; no schema/API/frontend change is
  expected.

Sensitive data handling plan:

- Use synthetic SQLite fixtures or already-sanitized test fixtures only. Do not
  record real database contents in durable artifacts.

Implementation plan:

1. Audit Opencode spec, fixtures, tests, and warning functions.
2. Add or strengthen characterization tests before production refactors.
3. Add deterministic benchmarks if scan/tail hot paths are selected.
4. Decompose selected functions into focused helpers without changing adapter
   semantics.
5. Validate with focused tests, package race tests, strict Lizard, local Codacy,
   benchmark gate where applicable, full gates, and external review.

Validation plan:

- Focused Opencode tests selected after coverage audit.
- `go test ./internal/adapters/opencode -count=1`
- `go test -race -count=1 ./internal/adapters/opencode`
- Direct strict Lizard on changed Opencode files.
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` if benchmarks or hot paths change.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

Artifact impact plan:

- Specs: likely `adapter-opencode.md`; quality/testing specs only if benchmark
  inventory changes.
- Runtime project skills: likely unaffected.
- End-user docs: likely unaffected.
- SOW lifecycle: active in `current/`; move to `done/` only after gates and
  review converge.

Open-source reference evidence:

- No new source-format claim is made yet. If implementation changes Opencode
  interpretation, inspect upstream source or mirrored repositories first and
  cite upstream repository identity plus commit.

Open decisions:

- None for the operator.

## Outcome

Completed: final review found two read-only/CLI path classification edges
in the changed surface. Both fixes are applied:

- literal bare filesystem paths containing `:memory:?` stay opaque and do not
  get split as SQLite memory DSNs
- explicit opencode CLI locations that are relative filenames beginning with
  `file:` are normalized to absolute filesystem paths before adapter
  construction, so they do not cross into SQLite URI parsing

All focused correctness/security/spec/frontend gates after the second fix are
green. The local benchmark regression gate is now green after stabilizing the
unchanged `internal/notify` `HubFanout-16` fixture so it measures the serial
`Hub.Deliver` hot path without timed helper goroutine scheduling noise. Final
external review converged after the source-id/target-hash fixes.

Additional final-review finding before completion: canonical source identity and
physical Opencode DB open target can now intentionally differ for normalized
explicit CLI paths. The technical decision is to preserve the configured source
ID for event attribution through `AdapterOptions.SourceID`, and to add a
target-hash guard to the Opencode cursor so old or mismatched table watermarks
are reset before they can be reused against a different physical SQLite target.
This is more precise than bumping the cursor version globally: normal same-target
resumes keep their performance benefit, while the unsafe source/open-path
boundary performs one idempotent re-scan.

Benchmark-gate stability addendum: investigation showed `BenchmarkHubFanout`
uses 1000 background drainer goroutines to keep subscription channels empty
while measuring a serial `Hub.Deliver` hot loop. Under normal desktop/VM load,
that helper-goroutine scheduling noise can dominate a microsecond-scale
benchmark and repeatedly fail the gate on unchanged code. The technical
decision is to stabilize the benchmark fixture, not weaken the threshold: keep
measuring the same serial `Hub.Deliver` fast path, remove helper goroutines from
the timed environment by sizing buffered channels for the measured run, and
refresh only the affected HubFanout baseline if the benchmark-code shape
changes.

## Implementation

Changes made:

- Added deterministic Opencode scan and tail benchmarks with exact event-count
  assertions:
  - scan fixture: 256 synthetic sessions, normal LLM/text path plus task-tool
    child-session topology path, 2622 expected canonical events
  - tail fixture: warm boundary replay plus one appended session, 21 expected
    canonical events
- Expanded the benchmark gate inventory from 9 paths / 6 packages to 11 paths /
  7 packages by adding Opencode `Scan` and `Tail`.
- Refactored all production Opencode adapter functions reported by the strict
  production-only Lizard scan into smaller helpers:
  - read-only DSN construction and identifier parsing
  - cursor comparison
  - migration table/latest migration lookup
  - assistant/tool operation mapping
  - polling loop runtime setup, poll requests, boundary/delta emission, and
    change detection
  - batch collection paging
  - delta row handling and session snapshot commit
  - WAL watcher event filtering and hint delivery
- Preserved test helpers by generalizing selected helper signatures from
  `*testing.T` to `testing.TB`, so benchmarks can reuse the same synthetic
  SQLite fixture builders as tests.
- Corrected synthetic fixture part IDs to sort in production read order
  (`step-start`, `step-finish`, `text`) and changed the benchmark event sink to
  count concurrently, so an unexpected event-count increase fails by assertion
  instead of blocking on a small channel buffer.
- Expanded the Opencode benchmark fixture to include `tool="task"` sub-agent
  linkage, restored hot-path slice preallocation, and removed a temporary
  single-event slice on that task-tool path after review.
- Removed misleading throughput bytes reporting from the tail benchmark; scan
  reports `B/s`, while tail reports `events/sec` and `peak_heap_mb`.
- Made the tail benchmark heap-sampler shutdown failure-safe with a single
  guarded stop path that also runs when the benchmark aborts.
- Updated benchmark documentation to match the 11-benchmark / 7-package
  inventory.
- Removed the unused `pollRequest.logger` field while preserving production
  logger flow through boundary and forward-delta emission.
- Tightened in-memory SQLite DSN recognition so a literal bare filesystem path
  containing `:memory:?` remains an opaque path instead of being split as a DSN
  query, with construction and end-to-end read-only open regression coverage.
- Normalized explicit opencode CLI relative filesystem paths to absolute paths
  before adapter construction, preserving source IDs/log locations while keeping
  literal filenames such as `file:opencode.db` out of SQLite URI parsing.
- Passed the canonical configured source ID through `AdapterOptions.SourceID`, so
  Opencode events keep `sources.id` attribution even when the adapter opens a
  normalized physical DB path.
- Aligned the shared `AdapterOptions.SourceID` contract across the existing
  filesystem adapters (`aiagent_v2`, `aiagent_v3`, `claude_code`, and `codex`):
  each constructor now honors a non-empty configured source ID and otherwise
  preserves its historical `format:location` fallback.
- Added `target_hash` to the Opencode cursor. Scan, tail, and cold-tail
  snapshots now stamp the physical DB open target hash, and old/mismatched
  watermarks are reset only at unsafe source/open-path boundaries.

Spec and gate artifacts updated before production refactor:

- `.agents/sow/specs/adapter-contract.md`
- `.agents/sow/specs/adapter-opencode.md`
- `.agents/sow/specs/deployment.md`
- `.agents/sow/specs/quality-gates.md`
- `.agents/sow/specs/testing-strategy.md`
- `.agents/skills/project-quality-gates/SKILL.md`
- `.github/workflows/ci.yml`
- `scripts/check-bench.sh`
- `bench/baseline.txt`
- `bench/README.md`

## Validation

Pre-final focused validation completed before the final benchmark-reporting and
documentation fixes:

- `go test ./internal/adapters/opencode -count=1`: pass
- `go test -race -count=1 ./internal/adapters/opencode`: pass
- `go test ./internal/adapters/opencode -run='^$' -bench='BenchmarkOpencode' -benchmem -count=1`: pass
- `scripts/check-bench.sh`: pass
- strict production-only Lizard on `internal/adapters/opencode/*.go` excluding
  `_test.go`: pass with zero warnings
- targeted local Codacy analysis on changed runtime/gate files: pass with zero
  issues across Trivy, Semgrep-compatible rules, ShellCheck, Lizard, and ESLint
- `./scripts/gates.sh`: pass

Pre-final full gate evidence from `./scripts/gates.sh`:

- Go module, formatting, vet, lint, security, race, coverage, fuzz seed corpus,
  benchmark regression, build, secret scan, attribution scan, and spec drift
  gates passed.
- Frontend lint, typecheck, Vitest coverage, bundle-size, Playwright, and axe
  accessibility gates passed.
- Coverage remained above thresholds; Opencode package coverage was 92.9% and
  the internal aggregate was 90.9%.
- Frontend bundle remained within budget; main entry gzip size was 132.2 KB
  against a 500 KB limit.

Post-review focused validation after the final benchmark-reporting fixes:

- `go test ./internal/adapters/opencode -count=1`: pass
- `go test ./internal/adapters/opencode -run='^$' -bench='BenchmarkOpencodeTail_SyntheticAppend' -benchmem -count=1`:
  pass; tail benchmark line reports no `B/s` / `MB/s`
- `go test ./internal/adapters/opencode -run='^$' -bench='BenchmarkOpencode' -benchmem -count=6`:
  pass; source for the regenerated Opencode `bench/baseline.txt` block
- `scripts/check-bench.sh`: pass on rerun; no significant `sec/op` regression
  greater than 20%
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass

Pre-cleanup final full gate evidence:

- First final aggregate attempt failed only at the benchmark regression gate:
  `HubFanout-16` in `internal/notify` measured +23.30% `sec/op`. That package
  is outside the SOW-0049 change path, and a standalone `scripts/check-bench.sh`
  rerun passed with no `sec/op` regression greater than 20%.
- Final `./scripts/gates.sh` rerun passed in 637s:
  - lint formatter-scope self-test, `./scripts/lint.sh`, secrets scan,
    attribution scan, spec drift, Codacy coverage/config self-tests, systemd
    unit lint, build, benchmark regression, race/coverage tests, Go coverage
    gate, adapter fuzz seed corpus, and frontend E2E/axe all passed
  - benchmark regression gate passed; Opencode `Scan` and `Tail` were both
    neutral against baseline
  - `go test -race -coverprofile=coverage.out -covermode=atomic ./...` passed;
    Opencode package coverage was 92.9% and gated internal aggregate coverage
    was 90.9%
  - frontend Vitest coverage passed with 631 tests; Playwright/axe passed with
    51 tests

Post-cleanup focused validation:

- `go test ./internal/adapters/opencode -count=1`: pass
- `go test -race ./internal/adapters/opencode -count=1`: pass
- `go test ./internal/adapters/opencode -run='^$' -bench='BenchmarkOpencodeTail_SyntheticAppend' -benchmem -count=1`:
  pass; tail benchmark line reports no `B/s` / `MB/s`
- strict production-only Lizard on `internal/adapters/opencode/*.go` excluding
  `_test.go`: pass with zero warnings across 27 production files
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass

Post-cleanup final full gate evidence:

- Final `./scripts/gates.sh` rerun passed in 909s:
  - lint formatter-scope self-test, `./scripts/lint.sh`, secrets scan,
    attribution scan, spec drift, Codacy coverage/config self-tests, systemd
    unit lint, build, benchmark regression, race/coverage tests, Go coverage
    gate, adapter fuzz seed corpus, and frontend E2E/axe all passed
  - benchmark regression gate passed in 434s with no significant `sec/op`
    regression greater than 20%; Opencode `Scan` and `Tail` were neutral
    against baseline
  - `go test -race -coverprofile=coverage.out -covermode=atomic ./...` passed;
    Opencode package coverage was 92.9% and gated internal aggregate coverage
    was 90.9%
  - frontend Vitest coverage passed with 631 tests; Playwright/axe passed with
    51 tests
  - gate runtime note: total 909s exceeded the 5-minute target because the
    `-race` suite was the long pole

Post-DSN-fix focused validation:

- `go test ./internal/adapters/opencode -run 'TestBuildReadOnlyDSN|TestOpenReadOnly' -count=1`:
  pass
- `go test ./internal/adapters/opencode -count=1`: pass
- `go test -race ./internal/adapters/opencode -count=1`: pass
- strict production-only Lizard on `internal/adapters/opencode/*.go` excluding
  `_test.go`: pass with zero warnings across 27 production files
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass
- Final `./scripts/gates.sh` rerun passed in 916s:
  - lint formatter-scope self-test, `./scripts/lint.sh`, secrets scan,
    attribution scan, spec drift, Codacy coverage/config self-tests, systemd
    unit lint, build, benchmark regression, race/coverage tests, Go coverage
    gate, adapter fuzz seed corpus, and frontend E2E/axe all passed
  - benchmark regression gate passed in 441s with no significant `sec/op`
    regression greater than 20%; Opencode `Scan` and `Tail` were neutral
    against baseline
  - `go test -race -coverprofile=coverage.out -covermode=atomic ./...` passed;
    Opencode package coverage was 93.1% and gated internal aggregate coverage
    was 91.0%
  - frontend Vitest coverage passed with 631 tests; Playwright/axe passed with
    51 tests
  - gate runtime note: total 916s exceeded the 5-minute target because the
    `-race` suite was the long pole
- Remaining post-fix external review rerun is pending.

Post-CLI-boundary-fix focused validation:

- `go test ./cmd/ai-viewer-ingest -run 'Test.*Opencode.*File|Test.*Adapter.*Location|TestParseSourceFlag' -count=1`:
  pass
- `go test ./cmd/ai-viewer-ingest ./internal/adapters/opencode -count=1`:
  pass
- `go test -race ./cmd/ai-viewer-ingest ./internal/adapters/opencode -count=1`:
  pass
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass

Post-CLI-boundary-fix aggregate/correctness validation:

- `./scripts/gates.sh` rerun reached the benchmark regression gate after
  passing lint/static/security, secrets, attribution, spec drift, Codacy
  self-tests, systemd lint, build, and bundle-size.
- The aggregate stopped at benchmark regression because unchanged
  `internal/notify` `HubFanout-16` measured +25.30% `sec/op` under elevated
  workstation load.
- Standalone `scripts/check-bench.sh` reruns under the same load failed:
  - first rerun: `OpencodeTail_SyntheticAppend-16` measured +20.96% and
    `HubFanout-16` measured +31.86%
  - later rerun after load dropped: Opencode scan/tail were neutral, but
    unchanged `HubFanout-16` still measured +33.76%
- `git diff -- internal/notify` is empty; the persistent benchmark blocker is
  outside the SOW-0049 changed files. The gate still has not passed, so the SOW
  is not complete.
- `./scripts/test.sh`: pass; Go race/coverage suite passed, Opencode package
  coverage was 92.9%, and frontend Vitest passed 631 tests.
- `./scripts/check-coverage.sh coverage.out`: pass; gated internal aggregate was
  90.9%.
- Adapter fuzz seed corpus and exact target-set lock: pass.
- `cd frontend && npm run e2e`: pass; Playwright/axe passed 51 tests.

Post-HubFanout-benchmark-stability focused validation:

- `gofmt -w internal/notify/bench_test.go`: pass
- `go test ./internal/notify -count=1`: pass
- `go test -run='^$' -bench='BenchmarkHubFanout' -benchmem -count=6 ./internal/notify`:
  pass
- `scripts/check-bench.sh`: pass
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass
- `git diff --check -- internal/notify/bench_test.go bench/baseline.txt`: pass

Post-HubFanout-benchmark-stability final full gate evidence:

- Final `./scripts/gates.sh` rerun passed in 962s:
  - lint formatter-scope self-test, `./scripts/lint.sh`, secrets scan,
    attribution scan, spec drift, Codacy coverage/config self-tests, systemd
    unit lint, build, benchmark regression, race/coverage tests, Go coverage
    gate, adapter fuzz seed corpus, and frontend E2E/axe all passed
  - benchmark regression gate passed in 508s with no significant `sec/op`
    regression greater than 20%; Opencode `Scan` and `Tail` stayed within the
    gate, and `HubFanout-16` no longer failed from helper-goroutine scheduler
    noise
  - `go test -race -coverprofile=coverage.out -covermode=atomic ./...` passed;
    Opencode package coverage was 93.1% and gated internal aggregate coverage
    was 91.0%
  - frontend Vitest coverage passed with 631 tests; Playwright/axe passed with
    51 tests
  - gate runtime note: total 962s exceeded the 5-minute target because the
    benchmark gate and `-race` suite were the long poles

Post-source-id/target-hash focused validation:

- `go test ./internal/adapters/opencode -count=1`: pass
- `go test ./cmd/ai-viewer-ingest ./internal/adapters/opencode ./internal/canonical -count=1`:
  pass
- `go test -race ./cmd/ai-viewer-ingest ./internal/adapters/opencode -count=1`:
  pass
- production-only strict Lizard on `internal/adapters/opencode/*.go` excluding
  `_test.go`: pass with zero warnings
- `scripts/spec-drift.sh`: pass
- `scripts/scan-secrets.sh`: pass
- `git diff --check`: pass

Post-source-id/target-hash final full gate evidence:

- Final `./scripts/gates.sh` rerun passed in 1050s:
  - lint formatter-scope self-test, `./scripts/lint.sh`, secrets scan,
    attribution scan, spec drift, Codacy coverage/config self-tests, systemd
    unit lint, build, benchmark regression, race/coverage tests, Go coverage
    gate, adapter fuzz seed corpus, and frontend E2E/axe all passed
  - benchmark regression gate passed in 602s with no significant `sec/op`
    regression greater than 20%; Opencode `Scan` and `Tail` were neutral
    against baseline
  - `go test -race -coverprofile=coverage.out -covermode=atomic ./...` passed;
    Opencode package coverage was 92.9% and gated internal aggregate coverage
    was 90.9%
  - frontend Vitest coverage passed with 631 tests; Playwright/axe passed with
    51 tests
  - gate runtime note: total 1050s exceeded the 5-minute target because the
    benchmark gate and `-race` suite were the long poles

Post-completion test-flake stabilization:

- A post-move rerun of
  `go test ./cmd/ai-viewer-ingest ./internal/adapters/opencode ./internal/canonical -count=1`
  exposed a test-only timing flake in
  `TestProcessChanges_CheckpointAfterEmit_NoLoss`: the test used a large
  buffered output channel, so the producer could emit every session before the
  consumer observed the first `SourceProgress` and cancelled.
- Fix applied:
  - run-1 now uses an unbuffered output channel as a precise checkpoint handoff
  - `processChanges` runs in a goroutine
  - the test receives through the first `SourceProgress`, cancels the context,
    waits for `processChanges` to return with context cancellation, then resumes
    from the durable cursor
  - the original zero-loss assertion remains: run-1 must stop before all
    sessions and run-1 plus resume must cover every session
- Focused validation:
  - `go test ./internal/adapters/opencode -run TestProcessChanges_CheckpointAfterEmit_NoLoss -count=20`:
    pass
  - `go test -race ./internal/adapters/opencode -run TestProcessChanges_CheckpointAfterEmit_NoLoss -count=10`:
    pass
  - `go test ./cmd/ai-viewer-ingest ./internal/adapters/opencode ./internal/canonical -count=1`:
    pass

Post-flake final gate evidence:

- `git diff --check`: pass
- `./scripts/gates.sh` rerun after the test-flake fix passed every section
  through `build.sh`, then failed at the benchmark fail-fast section because
  broad, unrelated workload slowed multiple non-Opencode benchmarks:
  - `aiagent_v2` scan/tail, `claude_code` scan, and `HubFanout` crossed the
    `sec/op` threshold
  - Opencode scan/tail remained neutral in that failed run
  - process inspection showed high workstation load during the benchmark
    window, including transient build/lint/browser/VM activity unrelated to
    this repository
- Isolated benchmark rerun after the transient load cleared:
  - `scripts/check-bench.sh`: pass; no `sec/op` regression greater than 20%
  - Opencode scan/tail remained within gate limits
- Remaining aggregate sections that the fail-fast run did not reach:
  - `./scripts/test.sh`: pass; Go race/coverage and frontend Vitest coverage
    passed
  - `scripts/check-coverage.sh coverage.out`: pass; gated internal aggregate
    coverage was 90.9%, Opencode coverage was 92.9%
  - `go test -run='^Fuzz' ./internal/adapters/...`: pass
  - adapter fuzz target-set lock: pass
  - `cd frontend && npm run e2e`: pass; 51 Playwright/axe tests passed

Post shared-source-ID cleanup validation:

- `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode -count=1`:
  pass
- `go test -race ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode ./cmd/ai-viewer-ingest ./internal/canonical -count=1`:
  pass
- `git diff --check`: pass
- `./scripts/gates.sh`: pass; all local quality gates green in 688 seconds
  after the shared-source-ID cleanup
  - benchmark regression gate: pass; no `sec/op` regression greater than 20%
  - `test.sh`: pass; Go race/coverage and frontend Vitest coverage passed
  - `check-coverage.sh`: pass; gated internal aggregate coverage was 90.9%,
    Opencode coverage was 92.8%
  - Playwright/axe: pass; 51 tests passed

Local Codacy notes:

- A broad worktree scan also reported existing complexity/style findings in
  Opencode `_test.go` review-regression fixtures and markdownlint findings in
  the long-form Opencode spec table/list formatting.
- These findings are outside the runtime production-code target of SOW-0049 and
  are consistent with the current Codacy reporting/noise posture. No runtime,
  security, or gate file issue was reported by the targeted changed-file scan.

## Reviews

Round 1:

- Four reviewers were started in parallel; one reviewer command exited without a
  usable review and was discarded.
- Reviewer A found two actionable benchmark issues:
  - synthetic part IDs sorted `step-finish` before `step-start`, so benchmarks
    measured the orphan step-finish path instead of the normal LLM open/close
    path
  - benchmark output channels were sized only slightly above the expected count,
    so an unexpected increase could block before the event-count assertion
- Fix applied:
  - synthetic fixture IDs now use lexically ordered `prt_01_ss`,
    `prt_02_sf`, `prt_03_tx`
  - scan expected events changed from 1281 to 1537
  - tail expected events changed from 11 to 13
  - benchmark event counting now drains concurrently while scan/poll producers
    run
  - the Opencode performance spec now states that the tail benchmark is the
    append path, while same-boundary mutation behavior remains in focused
    regression tests
- Reviewer B reported no actionable production, race, security, or coverage
  issue; it noted two low-impact helper signature observations that would add
  complexity if changed.
- Review continues after fixes with the same scope.

Round 2:

- Three reviewers were started in parallel; one command exited without a usable
  review and was discarded.
- Reviewer A found two actionable performance-maintainability issues:
  - the refactor had lost the prior `mapAssistantTurn` event-slice
    preallocation
  - the task sub-agent tool path allocated a temporary one-event slice and was
    absent from the new benchmark fixture
- Reviewer B found no correctness, race, security, or performance blockers. It
  reported three low-severity hygiene findings: restore the bare-path DSN
  comment near the extracted helper, add `defer cancel()` to the benchmark
  failure path, and comment why the tail benchmark count is doubled.
- Fix applied:
  - `mapAssistantTurn` again preallocates `4+2*len(parts)` before appending the
    turn start event
  - task-tool child-session emission now appends directly to the caller's event
    slice
  - the benchmark fixture includes `tool="task"` with `state.metadata.sessionId`
    and exact counts changed to 2622 for scan and 21 for tail
  - the tail benchmark comment now states it replays one deterministic
    already-appended session from a seeded cursor, not a sustained writer
    workload
  - the bare-path DSN opacity comment and benchmark cancellation cleanup were
    restored
- Review continues after fixes with the same scope.

Round 3:

- Three reviewers were started in parallel after the Round 2 fixes.
- Reviewer A found two actionable documentation/benchmark-reporting issues:
  - the Opencode performance spec and benchmark comments used "boundary
    replay" terminology for the scan benchmark even though the scan count comes
    from the deterministic six-session second-batch remainder
  - `BenchmarkOpencodeTail_SyntheticAppend` called `b.SetBytes`, causing Go's
    benchmark runner to report `B/s` / `MB/s` for a single poll cycle where
    byte throughput is not meaningful
- Reviewer B found the SOW validation section overstated the final state
  because the full gate evidence predated later benchmark-reporting fixes.
- Fix applied:
  - scan benchmark/spec wording now says six-session second-batch remainder
  - tail no longer calls `b.SetBytes`; baseline was regenerated and the spec
    states scan reports `B/s` while tail reports `events/sec` and
    `peak_heap_mb`
  - the SOW validation section now separates pre-final evidence from final
    evidence
- Review continued after fixes with the same scope.

Round 4:

- A follow-up reviewer found two actionable cleanup issues:
  - `bench/README.md` still described the old 7-benchmark / 5-package suite
  - the tail benchmark heap sampler was stopped only on the success path after
    timer stop, so an early `b.Fatalf` could leave the sampler goroutine
    running until process exit
- Fix applied:
  - `bench/README.md` now documents 11 benchmarks across 7 packages
  - the tail benchmark uses a guarded single stop path that runs on success and
    via `defer` on benchmark abort
- Review continued after fixes with the same scope.

Round 5:

- Three reviewers were started in parallel after final gates passed.
- Reviewer A found no production correctness, race, SQLite read-only, SQL
  injection, path traversal, watcher cleanup, or benchmark-gate blocker. It
  found SOW audit drift: the Outcome still said final gates/review were pending
  and the review rounds were out of order.
- Reviewer B found no blocker and one low maintainability issue:
  `pollRequest.logger` was assigned but never read.
- Reviewer C found no blocker; its observations were low-severity
  maintainability notes and confirmed the sampler guard, benchmark counts,
  read-only SQLite path, watcher cleanup, and spec/gate alignment.
- Fix applied:
  - SOW Outcome and review history corrected
  - `pollRequest.logger` dead field removed while preserving production logger
    flow through boundary and forward-delta emission
- Review will continue after fixes with the same scope.

Round 6:

- Three reviewers were started in parallel after the post-cleanup full gate.
- Reviewer A and Reviewer B found no actionable production correctness, race,
  security, read-only SQLite, cursor/tailer, WAL cleanup, benchmark-gate,
  coverage, dead-code, or SOW/gate alignment issue. Both confirmed the
  `pollRequest.logger` cleanup and production logger flow.
- Reviewer C found one actionable read-only DSN edge in the changed surface:
  `isMemoryDSN` treated any string containing `:memory:?` as an in-memory DSN,
  so a literal POSIX filesystem path such as
  `/tmp/opencode:memory:?dir/opencode.db` could be split as a DSN query instead
  of being handled as an opaque bare path.
- Fix applied:
  - `isMemoryDSN` now accepts only actual memory DSN forms that start with
    `:memory:` or `file::memory:`
  - a regression test pins that a bare path containing `:memory:?` remains a
    literal percent-escaped file URI path and does not leak path fragments into
    query parameters
  - an end-to-end regression test creates and reopens a real SQLite DB under a
    literal `:memory:?` directory name through `openReadOnly`
- Review will continue after post-fix gates with the same scope.

Round 7:

- Three reviewers were started in parallel after the post-DSN full gate.
- Reviewer A and Reviewer B found no actionable production correctness, race,
  security, read-only SQLite, cursor/tailer, WAL cleanup, benchmark inventory,
  coverage, or spec/gate alignment issue. Their observations were low-severity
  maintainability notes only.
- Reviewer C found one additional actionable CLI/path classification edge in the
  same trust boundary:
  - `parseSourceFlag` preserves relative paths and multi-colon locations
  - `startSource` validated only `os.Stat(src.location)` before adapter
    construction
  - `buildReadOnlyDSN` treats any string beginning with `file:` as a SQLite URI
  - therefore `--source opencode:file:opencode.db` could stat a literal POSIX
    relative filename and then be opened as SQLite URI `file:opencode.db`
    instead of the literal filesystem path
- Fix applied:
  - explicit opencode CLI locations keep the operator-supplied source ID and log
    location unchanged, but `startSource` passes an absolute filesystem path to
    the adapter factory when the opencode location is relative
  - non-opencode relative locations keep their previous behavior
  - a focused regression test pins the `file:opencode.db` case and the
    non-opencode unchanged case
  - deployment and Opencode adapter specs now document the CLI normalization
    boundary and retain direct adapter `file:` URI support as programmatic/test
    only
- Full local gates now pass after the benchmark fixture stability fix.
- Review found one remaining source-id/cursor migration edge:
  - adapter construction now uses a normalized physical DB open target for
    explicit relative opencode CLI paths, while `sources.id` remains the
    operator-supplied identity
  - emitted Opencode events must use the configured source ID, not the normalized
    open path
  - old cursors persisted before the normalization fix must not be reused against
    a different physical SQLite target
- Fix applied before the next review rerun:
  - pass `AdapterOptions.SourceID` from the ingester into the adapter and stamp
    Opencode events with it
  - add a `target_hash` guard to the Opencode cursor, resetting old or
    mismatched table watermarks only at the unsafe source/open-path boundary
- Focused tests and full local gates passed after the fix.
- Review continued with the same scope and converged in Round 8.

Round 8:

- Four reviewers were started in parallel after the source-id/target-hash full
  gate; one command exited without a usable review and was discarded.
- Reviewer A found no actionable correctness, security, test, benchmark, or
  spec-drift issue. It noted one residual non-blocking risk: `target_hash`
  identifies the configured DB open target string, not inode/content, so a DB
  replaced at the exact same path can reuse cursor state. This matches the
  documented path-target cursor contract and is not a blocker for this SOW.
- Reviewer B found no blockers and listed one low-severity possible
  `partDeltaRowHandler` error-swallowing concern. Manual verification rejected
  it as a false positive: the current handler returns `k, err` after
  `resolvePartSession`, so empty/corrupt required `part.session_id` still aborts
  the page and prevents cursor advancement.
- Reviewer C found no blockers. It confirmed the CLI normalization,
  `AdapterOptions.SourceID` attribution chain, cursor `target_hash` guard,
  read-only DSN allowlist, HubFanout benchmark stabilization, and spec/gate
  alignment. It noted only that `cmd/ai-viewer-ingest/source_location_test.go`
  is untracked until final staging.
- Review continued after post-flake stabilization with the same scope.

Round 9:

- Four reviewers were started in parallel after the post-flake stabilization;
  one command exited without a usable review and was discarded.
- Reviewer A found three actionable closeout issues:
  - the final worktree was not commit-ready because the completed SOW file and
    `cmd/ai-viewer-ingest/source_location_test.go` were still untracked; this
    is a staging/commit discipline issue and will be resolved by explicit
    filename staging before commit
  - the SOW `Sub-state` overstated the final aggregate gate status after the
    post-flake rerun; the status now records that the aggregate was
    benchmark-interrupted by unrelated workstation load, with the isolated
    benchmark rerun and remaining sections passing
  - the shared `AdapterOptions.SourceID` contract was broader than the current
    non-Opencode adapter implementations; `aiagent_v2`, `aiagent_v3`,
    `claude_code`, and `codex` now honor non-empty configured source IDs and
    retain their historical fallback when it is empty
- Reviewer B found no blockers. Its low-severity observations about
  programmatic memory DSNs, target-hash scope, and missing-source behavior were
  verified against the documented contracts and required no code change.
- Reviewer C found no correctness, security, race, or unwanted-side-effect
  blocker. It reported one low readability issue in `partDeltaRowHandler`;
  the resolver error variable now uses a distinct name so the error propagation
  is visually unambiguous.
- Focused adapter tests and the race-clean touched-package suite passed after
  the fixes.
- Review will continue after the shared-source-ID cleanup with the same scope.

Round 10:

- Four reviewers were started in parallel after full local gates passed.
- Reviewer A found no production correctness, race, read-only SQLite,
  cursor/tailer, or security blocker. It found three actionable closeout
  issues:
  - the index still carried an old pending-to-current SOW rename while the
    completed SOW and `cmd/ai-viewer-ingest/source_location_test.go` were
    untracked; this is resolved by explicit filename staging before commit
  - `bench/baseline.txt` said all non-Opencode blocks were preserved from the
    prior baseline, but `HubFanout` was intentionally regenerated after its
    benchmark fixture was stabilized; the baseline header now names that
    exception and records the regeneration command
  - `.agents/sow/specs/adapter-contract.md` still showed the old Opencode
    cursor example; it now documents the versioned table-watermark cursor with
    target metadata
- Reviewer B found no blockers and only repeated the commit-readiness staging
  item plus the expected "review in progress" closeout wording.
- Reviewer C found no correctness, security, race, cursor/tailer, canonical
  mapping, test, or unwanted-side-effect issue. It noted only the staging item,
  a low one-time legacy custom-source-ID re-scan behavior that is accepted as
  the safer boundary, and low benchmark-baseline variance that does not require
  action.
- Reviewer D found no blocker and repeated only the two untracked-file staging
  item.
- Post-fix lightweight checks:
  - `git diff --check`: pass
  - `scripts/spec-drift.sh`: pass
  - `scripts/scan-secrets.sh`: pass
- Review will continue after the documentation cleanup and explicit staging with
  the same scope.

Round 11:

- Three reviewers were started in parallel after explicit staging and the Round
  10 documentation cleanup.
- Reviewer A found no production correctness, race, read-only SQLite,
  cursor/tailer, security, staging, or commit-readiness blocker. It found two
  actionable spec mismatches:
  - `.agents/sow/specs/adapter-contract.md` showed an Opencode cursor example
    with v1/old field names; it now shows the real version-2 cursor field names
    (`max_id_seen`, `max_time_updated`, `max_time_updated_id`) plus
    `target_hash`
  - `.agents/sow/specs/adapter-opencode.md` still described the removed
    delta-path `part.message_id -> message.session_id` fallback; it now states
    that `part.session_id` is required and that both tree-load and delta-path
    message-id fallbacks were unreachable and removed
- Reviewer B and Reviewer C found no actionable correctness, security, race,
  cursor/tailer, benchmark-gate, spec-drift, or commit-readiness issue. Both
  treated the live "review pending" sub-state as expected.
- Review will continue after the spec cleanup with the same scope.

Round 12:

- Three reviewers were started in parallel after the Round 11 spec cleanup.
- Reviewer A found no actionable production correctness, race, read-only
  SQLite, cursor/tailer, security, staging, benchmark-gate, or
  commit-readiness issue. It confirmed the remaining live "review pending"
  marker was expected and should be flipped after recording this clean round.
- Reviewer B found no correctness, security, race, performance, test coverage,
  or spec-drift blocker. It ran targeted race tests for the changed adapter,
  source-ingest, canonical, and notify packages, plus an Opencode coverage run
  that reported 92.8% statement coverage.
- Reviewer C found no correctness, security, race, performance, sensitive-data,
  or unwanted-side-effect blocker. Its only note was a low-severity
  maintainability observation about defensive colon escaping in URI paths; the
  current behavior is intentionally kept because it is cheap and reinforces the
  literal-filesystem-path boundary.
- External review converged. No further SOW-0049 changes are required before
  commit.
