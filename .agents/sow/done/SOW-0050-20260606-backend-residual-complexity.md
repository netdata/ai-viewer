# SOW-0050 - Backend And CLI Residual Complexity Reduction

## Status

Status: completed

Sub-state: completed. The first implementation slice is hardened, review
converged, and the final full aggregate gate passed on the completed state.

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

Status: completed. First implementation slice selected, implemented, locally
validated, review-hardened, and accepted after the final full aggregate gate.

Problem / root-cause model:

- Backend residual complexity is no longer concentrated in one file. It is a
  set of smaller orchestration, command setup, and validation functions that
  need package-local treatment.
- The refreshed current-state scan reports 53 strict Lizard warnings across 64
  backend/CLI production Go files (`lizard -l go -C 8 -L 50 -a 8 -w`). The
  highest-risk warning family is ingest worker/write-loop orchestration because
  it owns batching, transaction order, rollup/FTS refresh, source progress,
  notify emission, and shutdown drain behavior.

Evidence reviewed:

- SOW-0047 closeout warning buckets and functions.
- Current strict Lizard warning scan after SOW-0049 merged:
  - `internal/ingest/worker.go:52` `run`: 71 NLOC, CCN 17, length 163.
  - `internal/ingest/worker.go:221` `flush`: 53 NLOC, CCN 15, length 113.
  - `internal/ingest/worker.go:348` `refreshRollupsOnly`: length 54.
  - `internal/ingest/notify_producer.go:48` `emitNotify`: CCN 10.
  - Lower-risk non-ingest clusters remain in store DSN helpers, pricing
    validation, presenter gzip/static-asset handlers, and CLI startup/source
    discovery.
- Existing coverage for the selected first slice:
  - `internal/ingest/worker_test.go` covers size flush, interval flush,
    low-sequence events not being dropped, cursor/source progress persistence,
    source-row creation, error callback routing, idle hour/day rollup
    materialization, and post-commit pricing-miss dedup promotion.
  - `internal/ingest/rollup_refresh_test.go` covers the direct flush and
    refresh-only paths, including carry-forward dirty buckets and rollback
    behavior.
  - `internal/ingest/notify_producer_test.go` covers session/op/source/stats
    notify rows and worker-driven tx-atomic notify insertion.
  - `internal/ingest/bench_test.go` benchmarks the batch insert path that runs
    through `worker.flush`.

Affected contracts and surfaces:

- Ingest event application, worker batching, rollups, catalog migration,
  presenter REST/static/middleware paths, pricing validation, store connection
  setup, notify replay, command entrypoint startup, source discovery, and
  backend helper commands.
- First slice: `internal/ingest/worker.go` and
  `internal/ingest/notify_producer.go`. The slice is behavior-preserving:
  worker timer/shutdown drain semantics, `flush` transaction ordering,
  source-progress persistence, rollup/FTS/aggregate refresh, notify/prune
  atomicity, and post-commit writer state promotion must remain identical.

Spec deltas to land before tests/code:

- Update `.agents/sow/specs/ingester.md` §Batching / Flush to match the current
  implementation order before refactoring:
  1. `BeginTx`.
  2. `ensureSourceRow`.
  3. apply every event in arrival order.
  4. refresh incremental rollups for dirty buckets.
  5. refresh `fts_ops` for dirty ops while `fts_logs` remains inline in
     `applyLogEntry`.
  6. refresh turn/session aggregates.
  7. persist `source_progress`.
  8. append notify rows and prune stale notify rows inside the same transaction.
  9. `Commit`.
  10. promote post-commit writer state (`pendingMissDedup`,
      materialized-rollup removals, and HWM observability counter).
- Attest that `.agents/sow/specs/data-model.md`,
  `.agents/sow/specs/observability.md`, and
  `.agents/sow/specs/sse-protocol.md` are unchanged for the first slice: no
  schema, health payload, or SSE/notify protocol contract changes are intended.
- Review addendum: update `.agents/sow/specs/ingester.md` §Batching / Flush to
  state that shutdown cancellation itself is not an offending-batch error. If a
  size or timer branch starts a flush after the lifecycle context is already
  canceled, the worker must switch to the same bounded shutdown-drain context
  used by the explicit `ctx.Done` path so buffered events are not dropped solely
  because `BeginTx` saw a canceled parent context.
- E2E addendum: the producer channel-close path is also a final shutdown-drain
  path. It must use the bounded shutdown-drain context even when the lifecycle
  context is still live at branch selection, because cancellation can arrive
  concurrently before `BeginTx` or while event rows are being applied.
- Review round 2 addendum: active size/interval flushes must not pass the
  cancellable lifecycle context into SQL once an event has entered the in-memory
  batch. A shutdown can arrive after context selection but before `BeginTx` or
  event application; the worker must use a lifecycle-detached write context for
  active-run flushes and reserve the bounded shutdown-drain context for writes
  chosen after shutdown is already observed or for final channel-close draining.
- Review round 3 addendum: the lifecycle-detached write context must not be a
  plain unbounded `context.WithoutCancel` wrapper. It must preserve parent
  values, ignore immediate lifecycle cancellation, and arm the bounded shutdown
  grace when the parent is canceled or reaches a parent lifecycle deadline.
- Review round 4 addendum: `ingester.md` must not overpromise
  `SourceErrorEvent` persistence for worker transaction failures. Adapter parse
  errors and writer-detected data-quality defects use `SourceErrorEvent`;
  worker batch transaction failures are logged/reported because the same write
  path or database failure may prevent reliable diagnostic persistence.
- Review round 4 addendum: the canceled-parent shutdown idle-refresh path must
  have its own worker/runtime test, not only normal idle-refresh and shutdown
  batch-flush tests.

Existing patterns to reuse:

- SOW-0047 ingest writer/catalog decomposition style.
- Existing package-level race tests, coverage gates, and benchmark gate.
- Keep the production hot path in small helpers with explicit ownership:
  lifecycle/timer helpers, shutdown drain helper, transaction phase helpers,
  and notify-kind emission helpers. Do not introduce a generic framework or
  reorder the write transaction.

Risk and blast radius:

- Medium to high for ingest; medium for presenter/security-sensitive request
  handling; low to medium for pricing/store and command setup helpers depending
  on selected functions.
- First slice risk is high within ingest but bounded to one package. No REST,
  frontend, adapter parsing, SQLite schema, pricing schema, or public API change
  is expected.
- Deferred higher-protocol-risk targets: `internal/presenter/events_sse.go` and
  `internal/notify/subscription.go` until a narrower SOW adds stronger replay,
  `Last-Event-ID`, busy-stream, and shutdown characterization.

Sensitive data handling plan:

- Use synthetic events and existing sanitized fixtures only. Do not write raw
  session content, secrets, private endpoints, or personal data to durable
  artifacts.

Implementation plan:

1. Update `ingester.md` with the flush-order spec hygiene above before any test
   or production edit.
2. Add characterization tests before refactoring:
   - direct shutdown-cancel drain test: buffered events present, context
     cancelled, final flush persists them.
   - notify test proving `stats_invalidated` fires once when only
     `rollupMaterializedThisRefresh=true`.
   - direct `refreshRollupsOnly` rollback/error characterization only if the
     helper extraction changes transaction boundaries.
3. Delegate implementation for the first slice to a production-code subagent:
   split worker lifecycle/timer/shutdown/flush phase helpers and notify
   emission helpers while preserving behavior and transaction ordering.
4. Verify subagent output by reading the diff and running focused tests, package
   race tests, strict Lizard, local Codacy on changed files, `scripts/check-bench.sh`,
   full `./scripts/gates.sh`, and external review until convergence.
5. Record remaining backend warning clusters in this SOW and either continue
   with the next package-local slice or split narrower follow-ups before
   completion.
6. Address review round 1 shutdown-drain finding with a failing worker test
   before implementation, then rerun focused validation and external review on
   the full SOW-0050 diff.
7. Address review round 2 in-flight cancellation finding with a failing worker
   runtime test before implementation, then rerun focused validation and external
   review on the full SOW-0050 diff.
8. Address review round 3 unbounded-detached-context finding with focused tests
   for shutdown grace and parent lifecycle deadlines, then rerun validation and
   external review on the full SOW-0050 diff.
9. Address review round 4 findings: correct worker write-error visibility in
   `ingester.md`, add a canceled-parent shutdown idle-refresh test, harden the
   context timing tests against scheduler delay, and apply the small
   maintainability cleanups reviewers identified.

Validation plan:

- Focused tests for first slice:
  - `go test ./internal/ingest -run 'TestWorker|TestRefreshRollups|TestNotify|TestEmitNotify' -count=1`
  - `go test -race ./internal/ingest -run 'TestWorker|TestRefreshRollups|TestNotify|TestEmitNotify' -count=1`
  - `go test ./internal/ingest -run '^$' -bench BenchmarkBatchInsert -benchmem -count=1`
- Full package tests:
  - `go test ./internal/ingest -count=1`
  - `go test -race ./internal/ingest -count=1`
- Direct strict Lizard on changed files:
  - `lizard -l go -C 8 -L 50 -a 8 internal/ingest/worker.go internal/ingest/worker_runtime.go internal/ingest/notify_producer.go`
- Local Codacy analysis on changed files.
- `scripts/check-bench.sh` because `worker.flush` is the benchmarked batch
  insert hot path.
- Full `./scripts/gates.sh`.
- External second-opinion review until convergence.

## Reviews

### Round 1 - 2026-06-07

Findings:

- Reviewer A found a shutdown-drain edge case: after lifecycle context
  cancellation, Go `select` may still take an event or timer branch before the
  `ctx.Done` branch. That branch passed the already-canceled lifecycle context
  into `flushBatch`, allowing `BeginTx` to fail with context cancellation and
  the in-memory batch to be cleared. Decision: real in-scope correctness issue;
  fix in SOW-0050 with spec update, failing test, implementation patch, and
  review rerun.
- Reviewer A and Reviewer B found stale SOW status text saying no production
  code had been written. Decision: fixed in this SOW update.
- Reviewer A found stale exact `worker.go` line references in test comments.
  Decision: remove exact line references in the delegated test cleanup.
- The Playwright E2E seed path then reproduced the same class of bug through
  the channel-close branch: workers logged `flush (channel closed): begin tx:
  context canceled` / `apply event ... context canceled`, leaving only 1 of the
  expected 4 sessions seeded. Decision: extend the shutdown-drain fix so
  producer-close final flushes always use the bounded drain context.

### Round 2 - 2026-06-07

Findings:

- Reviewer A found a remaining in-flight cancellation race: an active size or
  interval flush can choose the lifecycle context while it is still live, then
  receive shutdown cancellation before `BeginTx` or event application. The SQL
  write can fail with context cancellation while `flushBatch` still clears the
  in-memory batch. Decision: real in-scope data-loss risk; tighten the spec,
  add a deterministic failing worker-runtime test, and fix context selection
  before any completion claim.
- Reviewer A also found stale SOW text and an incomplete execution-log test list.
  Decision: update the SOW as part of the same fix.
- Reviewers B and C did not find additional blocking issues. Decision: do not
  treat clean secondary reviews as overriding Reviewer A's concrete race
  evidence.

### Round 3 - 2026-06-07

Findings:

- Reviewer A found that the `context.WithoutCancel` fix prevents data loss but
  leaves an active flush unbounded if shutdown arrives after context selection:
  `WithoutCancel` drops `Done`, `Err`, and deadline, and `Ingester.Stop()` waits
  for workers without its own timeout. Decision: real in-scope correctness issue;
  keep the data-loss fix but replace plain `WithoutCancel` with a
  lifecycle-detached write context that preserves values and starts the bounded
  shutdown-drain timeout after parent cancellation.
- Reviewer B found no blocking issues.
- Reviewer C found only clarity findings around the intentionally ignored
  `handleClose` context and the non-obvious cancellation-race test helper.
  Decision: address clarity while fixing Reviewer A's blocking issue.
- Reviewer A found nearby ingester spec drift: the concurrency model still said
  only in-memory tests pin `MaxOpenConns=1`, while `OpenWriter` pins writer
  stores unconditionally. Decision: fix the spec text in this SOW because
  `ingester.md` is already part of the touched contract surface.

### Round 4 - 2026-06-07

Findings:

- Reviewer A found spec drift: `ingester.md` claimed active worker write errors
  also write `SourceErrorEvent` rows for `/api/health`, while normal ingester
  workers only log/report batch failures. Decision: correct the spec to match
  the existing contract. Do not add `SourceErrorEvent` persistence for worker
  transaction failures in this slice because the same write path or database
  failure may prevent reliable diagnostic persistence.
- Reviewer A found missing test coverage for the canceled-parent
  shutdown-idle-refresh path. Decision: add a worker/runtime regression test
  before any related implementation cleanup.
- Reviewer A found the short context-grace timing tests could be flaky under
  heavy scheduler delay. Decision: lengthen the test grace values while keeping
  watchdog timeouts bounded.
- Reviewers B and C found only low maintainability issues: make the
  channel-close shutdown-drain decision visible in code, simplify timer
  rearming, and make the custom test context safer for future concurrent use.
  Decision: apply these cleanups before the next review rerun.

### Round 5 - 2026-06-07

Findings:

- Reviewer A found the corrected worker write-error spec still used stale
  `opts.OnError` wording even though production workers report through
  `worker.report`, using the private `onErr` seam only in tests and otherwise
  `logger.Error`. Decision: real spec drift; corrected `ingester.md`.
- Reviewer A found the detached-context timing tests could still false-fail
  under extreme scheduler delay because they made an immediate non-blocking
  `Done` assertion after parent cancellation/deadline. Decision: make the tests
  elapsed-time tolerant while preserving the invariant that the write context
  must not cancel before the bounded grace has elapsed.
- Reviewers A and C found the cancellation-race test helper had non-standard
  context semantics and made the race harder to reason about. Decision: replace
  it with normal `context.WithCancel` semantics by selecting the write context
  first, canceling the parent, then flushing with the already-selected write
  context.
- Reviewer A found `internal/ingest/worker.go` still exceeded the project
  file-size smell threshold after the function-complexity cleanup. Decision:
  split worker runtime/lifecycle/timer/context orchestration into
  `internal/ingest/worker_runtime.go`; `worker.go` now stays focused on worker
  write/transaction helpers and is under 400 lines.
- Reviewer C noted `rearmTimer()` still runs after terminal shutdown flushes.
  Decision: no code change in this slice; the rearm is immediately neutralized
  by `workerRuntime.close()` and has no observable runtime effect. Avoiding it
  would add shutdown-only branching for no behavior or gate benefit.

### Round 6 - 2026-06-07

Findings:

- Reviewer A found the SOW validation command still recorded Codacy only for
  `worker.go` and `notify_producer.go`, omitting new production file
  `worker_runtime.go`. Decision: real validation-record defect; reran Codacy on
  all three touched production files and updated the validation evidence.
- Reviewer A found `BenchmarkBatchInsert` measures `worker.flush` directly and
  therefore bypasses `workerRuntime.flushBatch` context selection. Decision:
  document this intentionally. `BenchmarkBatchInsert` is the accepted benchmark
  gate for this slice because it measures the throughput-critical SQL batch
  transaction path already baselined in `bench/baseline.txt`; adding a new
  benchmark without a historical baseline would not protect the existing
  regression gate. The detached-context path is covered by focused unit/race
  tests rather than benchmarked as a separate baseline in this slice.
- Reviewer A found stale validation text saying "after the Round 4 fixes".
  Decision: corrected the pending-validation wording.
- Reviewer A found a stale `worker.go:221` reference in the batch-insert
  benchmark comment. Decision: replaced the line-specific reference with
  `worker.flush`.
- Reviewer B raised a possible detached-write goroutine linger. Decision: false
  positive for the current implementation. If parent cancellation and write
  cancellation race, the second `select` in `detachedWriteContext` still
  observes `writeCtx.Done()` and exits; otherwise the goroutine is correctly
  bounded by the shutdown grace while the write is still active.
- Reviewer C found the scheduler-tolerant test helper name imprecise.
  Decision: renamed it to `probeContextNotCanceledBeforeGrace`.

### Round 7 - 2026-06-07

Findings:

- Reviewer A found the SOW validation plan still omitted `TestEmitNotify` in the
  focused test regex and `internal/ingest/worker_runtime.go` in the strict
  Lizard command, while the executed validation evidence already included both.
  Decision: real SOW-only drift; corrected the validation plan and reran review
  on the full staged diff.
- Final convergence review after that correction found no blocking correctness,
  race, goroutine-leak, security, spec-drift, unwanted-side-effect,
  performance, or maintainability issues.
- Reviewers noted only non-blocking observations already accepted in this SOW:
  `handleClose` intentionally ignores the lifecycle context, terminal shutdown
  flushes may rearm a timer that `workerRuntime.close()` immediately stops, and
  `Ingester.Stop()` has no overall timeout outside the per-worker bounded drain.

### Round 8 - 2026-06-07

Findings:

- Reviewer A found stale ingester shutdown spec text promising a process-level
  15 s hard timeout and a 5 s worker drain timeout. The implementation has a
  5 s adapter-goroutine wait in the CLI and a 10 s per-worker write/drain
  context, but no separate process-level hard timeout.
- Reviewer A found stale outcome text saying the final full aggregate gate was
  green, while later SOW-0058 review fixes still required a fresh full gate.

Resolution:

- Corrected `.agents/sow/specs/ingester.md` to describe the actual shutdown
  contract: adapter wait, per-worker bounded write/drain context, and no
  process-level hard timeout in the current implementation.
- Corrected the outcome wording so SOW-0050 does not overstate the final gate
  state before SOW-0058 review convergence and a fresh aggregate gate.

### Round 9 - 2026-06-07

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

Artifact impact plan:

- Specs: `ingester.md` gets flush-order hygiene for the first slice. Presenter,
  pricing, store, data-model, security, and deployment specs are unaffected
  unless later SOW-0050 slices touch their contracts.
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

Completed. The first implementation slice is hardened for bounded-context
lifecycle-cancellation findings, focused tests pass, package tests pass,
external review converged, and the final full aggregate gate passed on the
completed state.

## Execution Log

### 2026-06-07

- Activated the SOW and selected the first slice:
  `internal/ingest/worker.go` plus `internal/ingest/notify_producer.go`.
- Updated `.agents/sow/specs/ingester.md` to document the current worker flush
  order before tests or production code changed.
- Added characterization tests before refactoring:
  - `TestWorker_CancelDrainsPendingBatch`
  - `TestWorker_CancelDrainsBufferedChannel`
  - `TestEmitNotify_MaterializedRollupInvalidatesStatsOnce`
  - `TestRefreshRollups_RefreshOnlyNotifyErrorRollsBack`
- Refactored worker lifecycle/timer/shutdown orchestration, flush phases,
  idle-refresh notify handling, and notify row emission helpers without
  changing transaction order or notify semantics.
- Added review-driven shutdown tests:
  - `TestWorkerRuntime_FlushBatchUsesShutdownDrainAfterLifecycleCancel`
  - `TestWorkerRuntime_HandleCloseUsesShutdownDrainForFinalFlush`
  - `TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace`
- Hardened worker write-context selection so active size/interval flush SQL is
  detached from lifecycle cancellation, while already-observed shutdown and
  channel-close final drains still use the bounded shutdown-drain context.
- Review round 3 hardened that design further: active writes must not be
  unbounded after lifecycle cancellation. Added `detachedWriteContext` and
  `TestDetachedWriteContext` so active writes preserve parent values, ignore
  immediate lifecycle cancellation, and cancel after the shutdown grace window.
- Review round 4 pinned the parent-deadline variant of the same lifecycle
  contract. Added `TestDetachedWriteContextParentDeadlineStartsShutdownGrace`
  so parent deadlines start the bounded shutdown grace instead of canceling
  active writes immediately.
- Review round 4 follow-up corrected the worker write-error visibility spec,
  added `TestWorkerRuntime_CanceledParentShutdownIdleRefreshMaterializesClosedBuckets`,
  lengthened the focused detached-context grace windows to avoid scheduler-load
  flake, simplified timer rearming, and documented the intentional
  channel-close shutdown-drain context.
- Review round 5 corrected the remaining `opts.OnError` spec wording, replaced
  the custom cancellation-race test context with normal context cancellation,
  made detached-context timing assertions scheduler-tolerant, and split worker
  runtime/lifecycle/timer/context orchestration into
  `internal/ingest/worker_runtime.go`. `internal/ingest/worker.go` is now 286
  lines.
- Review round 6 corrected stale validation/benchmark text, recorded Codacy
  coverage for `worker_runtime.go`, documented why `BenchmarkBatchInsert`
  remains the accepted performance gate for the SQL hot path, and renamed the
  scheduler-tolerant detached-context test helper for clarity.
- Review round 7 corrected the remaining SOW validation-plan drift and converged
  with no blocking reviewer findings.
- Re-ran the declared backend/CLI strict Lizard scan after the slice. The
  worker/notify warnings dropped from 4 to 0; SOW-0050 declared backend/CLI
  residual warnings dropped from 53 to 49.
- Split the remaining declared backend/CLI warnings into narrower follow-up
  SOWs:
  - `SOW-0054-20260607-presenter-and-serve-complexity.md`: 25 presenter
    warnings plus 2 serve command warnings.
  - `SOW-0055-20260607-ingest-write-model-residual-complexity.md`: 9 ingest
    writer/catalog/rollup/resolver warnings.
  - `SOW-0056-20260607-store-pricing-cli-complexity.md`: 3 store warnings,
    5 pricing warnings, and 4 ingest CLI warnings.
  - `SOW-0057-20260607-notify-replay-complexity.md`: 1 notify replay warning.

## Validation

Acceptance criteria evidence:

- First selected slice had tests before production implementation.
- Direct strict Lizard on `internal/ingest/worker.go`,
  `internal/ingest/worker_runtime.go`, and
  `internal/ingest/notify_producer.go` reports no warnings.
- Remaining backend/CLI warnings are explicitly split into follow-up SOWs.

Tests or equivalent validation:

- `go test ./internal/ingest -run 'TestWorker|TestRefreshRollups|TestNotify|TestEmitNotify' -count=1`:
  passed.
- `go test -race ./internal/ingest -run 'TestWorker|TestRefreshRollups|TestNotify|TestEmitNotify' -count=1`:
  passed.
- `go test ./internal/ingest -run '^TestDetachedWriteContext' -count=1 -v`:
  passed.
- `go test ./internal/ingest -run 'TestDetachedWriteContext|TestWorkerRuntime_.*IdleRefresh|TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace' -count=1 -v`:
  passed after the Round 4 fixes.
- `go test -race ./internal/ingest -run 'TestDetachedWriteContext|TestWorkerRuntime_.*IdleRefresh|TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace' -count=1 -v`:
  passed after the Round 4 fixes.
- `go test ./internal/ingest -run 'TestDetachedWriteContext|TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace|TestWorkerRuntime_.*IdleRefresh' -count=1 -v`:
  passed after the Round 6 fixes.
- `go test -race ./internal/ingest -run 'TestDetachedWriteContext|TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace|TestWorkerRuntime_.*IdleRefresh' -count=1 -v`:
  passed after the Round 6 fixes.
- `gofmt -d internal/ingest/worker.go internal/ingest/worker_runtime.go internal/ingest/worker_test.go internal/ingest/notify_producer.go internal/ingest/notify_producer_test.go internal/ingest/rollup_refresh_test.go`:
  clean after the Round 6 fixes.
- `git diff --check`: clean after the Round 6 fixes.
- `go test ./internal/ingest -count=1`: passed.
- `go test ./internal/ingest -run '^$' -bench BenchmarkBatchInsert -benchmem -count=1`:
  passed.
- `lizard -l go -C 8 -L 50 -a 8 internal/ingest/worker.go internal/ingest/worker_runtime.go internal/ingest/notify_producer.go`:
  passed with no threshold warnings.
- `codacy-analysis analyze --files internal/ingest/worker.go internal/ingest/worker_runtime.go internal/ingest/notify_producer.go --parallel-tools 4 --tool-timeout 600000 --output-format json`:
  passed with zero issues from Lizard, Semgrep, and Trivy.
- `./scripts/lint.sh`: passed.
- `./scripts/test.sh`: passed.
- `./scripts/check-coverage.sh coverage.out`: passed; gated aggregate 91.0% and
  `internal/ingest` 86.7%.
- `go test -run='^Fuzz' ./internal/adapters/...`: passed.
- `./scripts/build.sh`: passed, including frontend bundle-size gate.
- `cd frontend && npm run e2e`: passed, 51/51 Chromium tests.
- `./scripts/spec-drift.sh`: passed.
- `./scripts/scan-secrets.sh`: passed.
- `codacy-analysis analyze --files internal/ingest/worker.go internal/ingest/worker_runtime.go internal/ingest/notify_producer.go --parallel-tools 4 --tool-timeout 600000 --output-format json --output /tmp/ai-viewer-sow0050-codacy-production-r7.json`:
  passed after the Round 6 review findings with zero issues from Lizard,
  Semgrep, and Trivy.
- `go test ./internal/ingest -count=1`: passed after the Round 6 review
  findings.
- `./scripts/spec-drift.sh`: passed after the Round 6 review findings.
- `./scripts/scan-secrets.sh`: passed after the Round 6 review findings.
- Latest `scripts/check-bench.sh`: failed on unchanged
  `internal/adapters/aiagent_v2` `Tail_SyntheticAppend-16` (+40.45% `sec/op`)
  while `internal/ingest` `BatchInsert-16` was neutral (`~`, p=0.310). The run
  started under high workstation load (`uptime` load average 14.38, 12.01,
  10.95) and ended while load was 16.18, 28.62, 20.29. Decision: do not mark the
  SOW complete until the benchmark gate is green or a separate evidence-backed
  SOW updates the benchmark baseline.
- Final external review convergence after the SOW validation-plan correction:
  passed with no blocking findings from three reviewers.
- `timeout 3600 ./scripts/gates.sh`: failed at the benchmark section after all
  earlier sections passed. Failing unchanged benchmarks:
  `Tail_SyntheticAppend-16` +36.51% `sec/op`,
  `ClaudeScan_SyntheticCorpus-16` +26.88% `sec/op`, and
  `ClaudeTail_SyntheticAppend-16` +34.30% `sec/op`. Changed
  `internal/ingest` `BatchInsert-16` was neutral (`~`, p=0.240).
- Standalone `timeout 1800 scripts/check-bench.sh`: passed immediately after the
  aggregate failure; no benchmark exceeded the >20% `sec/op` threshold. Changed
  `internal/ingest` `BatchInsert-16` was neutral (`~`, p=0.132).
- Second `timeout 3600 ./scripts/gates.sh`: failed again at the benchmark
  section after all earlier sections passed. Failing unchanged benchmarks:
  `Scan_SyntheticCorpus-16` +30.57% `sec/op` and `Tail_SyntheticAppend-16`
  +36.10% `sec/op`. Changed `internal/ingest` `BatchInsert-16` was neutral
  (`~`, p=0.394). Workstation load remained high (`uptime` load average 10.85,
  10.96, 12.24).
- Third `timeout 3600 ./scripts/gates.sh`: failed again at the benchmark
  section after all earlier sections passed. Failing unchanged benchmark:
  `CodexTail_SyntheticAppend-16` +26.73% `sec/op`. Changed `internal/ingest`
  `BatchInsert-16` was neutral (`~`, p=0.240). Decision: keep SOW-0050 open and
  track the benchmark-gate stability/baseline remediation in SOW-0058; do not
  lower the >20% `sec/op` threshold and do not mark the SOW complete with a red
  aggregate gate.
- Final `timeout 3600 ./scripts/gates.sh` after SOW-0058 remediation: passed
  every gate in 798s. Evidence: benchmark regression gate self-test passed
  37/37 and the real benchmark gate passed on attempt 1 in 314s; changed
  `internal/ingest` `BatchInsert` and `internal/notify` `HubFanout` were
  both neutral in the benchmark comparison; Go race/coverage passed with total
  statement coverage 86.1%, gated `internal/*` aggregate coverage 91.1%,
  frontend Vitest passed 631/631 with 94.41% statements, Playwright E2E/axe
  passed 51/51, and the aggregate finished with `[PASS] gates.sh: every quality
  gate green`.
- SOW-0058 round 4 found and fixed a CI `Require benchmarks` parser blocker
  caused by the new unsuffixed `-cpu=1` benchmark rows. Focused validation
  passed (`check-bench-test.sh` 41/41 plus extracted CI block `present=true`);
  follow-up review converged.
- Final `timeout 3600 ./scripts/gates.sh` after final review and SOW closeout
  fixes: passed every gate in 865s. Evidence: benchmark regression gate self-test
  passed 41/41 and the real benchmark gate passed on attempt 1 in 398s; Go
  race/coverage passed with total statement coverage 86.0%; Go coverage gate
  passed with gated `internal/*` aggregate 91.0%; frontend Vitest passed 631/631
  with 94.41% statements; Playwright E2E/axe passed 51/51; spec-drift, secrets
  scan, AI-attribution scan, Codacy self-tests, systemd lint, build, bundle
  gate, and adapter fuzz seed corpus all passed; and the aggregate finished with
  `[PASS] gates.sh: every quality gate green`.

Completion validation:

- External second-opinion review converged on the final combined SOW-0050 and
  SOW-0058 diff.
- A fresh `timeout 3600 ./scripts/gates.sh` passed after the final review-fix
  state.

Sensitive data gate:

- Synthetic events and committed sanitized fixtures only. No raw secrets,
  credentials, bearer tokens, private endpoints, personal data, or private
  session content were added to durable artifacts.

Artifact maintenance gate:

- Specs: `.agents/sow/specs/ingester.md` updated for flush-order accuracy.
- Runtime project skills: no change needed; the existing delegation,
  quality-gate, and backend skills already cover this workflow.
- End-user docs: no change needed; runtime behavior is unchanged.
- SOW lifecycle: active in `.agents/sow/current/`; completion move is pending
  full gates and review convergence.

Follow-up mapping:

- Presenter/serve residuals: SOW-0054.
- Ingest write-model residuals: SOW-0055.
- Store/pricing/ingest CLI residuals: SOW-0056.
- Notify replay residual: SOW-0057.
- Benchmark gate stability/baseline hygiene: SOW-0058.
