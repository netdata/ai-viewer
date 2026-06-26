# SOW-0104 - Ingester Graceful Restart Timeout

## Status

Status: completed

Sub-state: Core restart/shutdown implementation, benchmark-gate triage
(`SOW-0106`), and local non-benchmark gates are complete. The 2026-06-26
CI-reopen fix for opencode cancellation and sessions-filter E2E has six
`PRODUCTION GRADE` reviewer votes with only P3 residuals. Completed on
2026-06-26 after commit `1884f0842cd26b95b8c7ea7021757bd7bb7be794` passed
GitHub `ci` run `28235802975` and `codeql` run `28235802884`.

### CI Gate Reopen - 2026-06-26

The SOW remains open after the SOW-0105 review rerun because the pushed
lineage-classification commit exposed two CI blockers:

- Go race job: `TestProcessChanges_CheckpointAfterEmit_NoLoss` returned
  `opencode: begin ro tx: interrupted (9)` after the test canceled the caller
  context. This is SOW-0097/SOW-0104 lineage debt because adapter cancellation
  during restart must surface as a bounded caller cancellation, not leak a
  low-level SQLite interruption.
- Frontend E2E job: `frontend/tests/sessions-filter.spec.ts` saw zero rows
  after applying an agent filter derived from `/api/sessions`. This blocks
  SOW-0105 UI/API contract closure until the seeded API contract and browser
  synchronization are proven deterministic.

Follow-up plan before any code changes:

- Update `.agents/sow/specs/adapter-opencode.md` to state that once the caller
  context is canceled or expired, opencode read-only SQL boundaries normalize
  SQLite driver interruption errors to `context.Canceled` or
  `context.DeadlineExceeded`.
- Add focused tests for the cancellation-normalization helper and rerun the
  checkpoint-after-emit resume test under repetition/race.
- Reproduce the seeded browser/API filter path used by the E2E test. If the API
  is correct, strengthen the E2E test to choose a filterable seeded agent and
  wait for the filtered row/link state, not a stale pre-filter row.

Resolution evidence:

- `.agents/sow/specs/adapter-opencode.md` now records the cancellation
  normalization contract for `BeginTx`, `QueryContext`, `QueryRowContext`,
  row `Scan`, `Rows.Err`, and read-only `Commit`.
- `internal/adapters/opencode/tailer_branch_test.go` adds
  `TestNormalizeContextSQLError`,
  `TestCommitSessionSnapshot_NormalizesCanceledCommit`, and
  `TestScanOnePage_NormalizesCanceledRowScan`. The opencode SQL read path now
  normalizes canceled/expired contexts before wrapping driver errors, including
  full-tree read-only commits and per-row scan errors.
- Local focused validation:
  - `go test ./internal/adapters/opencode -run '^(TestNormalizeContextSQLError|TestCommitSessionSnapshot_NormalizesCanceledCommit|TestScanOnePage_NormalizesCanceledRowScan)$'`
  - `go test -race -count=20 ./internal/adapters/opencode -run '^(TestNormalizeContextSQLError|TestCommitSessionSnapshot_NormalizesCanceledCommit|TestScanOnePage_NormalizesCanceledRowScan|TestProcessChanges_CheckpointAfterEmit_NoLoss)$'`
  - `go test -race ./internal/adapters/opencode`
- Seeded API diagnosis proved `/api/sessions?group=root&agents=<agent>` returns
  one row for the fixture agents `research`, `bigquery`, and
  `netdata-research`; the frontend failure was stale/loading DOM sampling.
- `frontend/tests/sessions-filter.spec.ts` now derives a filterable root agent
  and expected row count from the API, then waits for the browser table to reach
  that exact filtered count.
- Local frontend validation:
  - `AI_VIEWER_E2E_PORT=17710 npm run e2e -- --project=chromium tests/sessions-filter.spec.ts`
  - `npm run typecheck`
  - `npm run lint`
  - `git diff --check`
- Broader local validation:
  - `bash scripts/lint.sh`
  - `bash scripts/test.sh`
  - `bash scripts/check-coverage.sh`
  - `bash scripts/build.sh`
  - `AI_VIEWER_E2E_PORT=17710 npm run e2e -- --project=chromium tests/sessions-filter.spec.ts` after rebuild
  - `bash scripts/spec-drift.sh`
  - `bash scripts/scan-secrets.sh`
  - `bash scripts/scan-ai-attribution.sh`

First implementation-review round:

- `glm`: `NEEDS WORK`; accepted P1 that
  `commitSessionSnapshot` did not normalize its read-only commit error, and
  accepted P2 that delta row-scan errors could still leak `interrupted (9)`.
- `minimax`: `NEEDS WORK`; same accepted commit-boundary finding, plus missing
  coverage for that boundary.
- `qwen`: `PRODUCTION GRADE`, but identified the same
  `commitSessionSnapshot` gap as non-blocking. The finding was accepted and
  fixed because it is a spec completeness issue.
- `mimo`: `PRODUCTION GRADE`.
- `deepseek`: `PRODUCTION GRADE`.
- `kimi`: technical failure before a final vote was retrievable; per reviewer
  protocol, no retry was attempted before fixing the accepted blocking findings.

Disposition: the accepted findings were fixed by threading `ctx` into
`commitSessionSnapshot`, normalizing its read-only `Commit` error, normalizing
non-nil row-scan errors before wrapping them in `scanOnePage` and
`scanBoundaryBucket`, and adding targeted tests for the commit and row-scan
normalization paths.

Second implementation-review round:

- `deepseek`: `PRODUCTION GRADE`; P3-only notes that the
  `commitSessionSnapshot` test uses a rolled-back transaction to force commit
  failure, and that `resolveRootID` intentionally preserves `sql.ErrNoRows`
  before cancellation normalization.
- `mimo`: `PRODUCTION GRADE`; P3-only note that the rolled-back-transaction
  commit test is artificial but sufficient when combined with the helper and
  row-scan tests.
- `kimi`: `PRODUCTION GRADE`; P3-only style note that `rows.Err()` normalization
  sites rely on Go `if err := ...` scoping, which is correct but requires a
  reader to notice the scoped variable.
- `minimax`: `PRODUCTION GRADE`; P3-only notes requesting possible future direct
  tests for `scanBoundaryBucket`, direct `check-parity` write-lock behavior, and
  serve signal-ordering. These are not required for this CI-reopen fix.
- `glm`: `PRODUCTION GRADE`; verified the UI default `group=root` contract,
  opencode-only SQLite scope, focused race tests, `go vet`, `gofmt`, and spec
  drift. P3-only notes request possible direct `beginRO` coverage and a small
  sessions-filter comment cleanup.
- `qwen`: `PRODUCTION GRADE`; verified all opencode read-only SQL boundaries,
  `sql.ErrNoRows` ordering, WAL snapshot release before warning flush, focused
  race tests, and SOW lineage. P3-only notes request possible comments for the
  artificial commit test and an explicit Playwright poll timeout.

Disposition: no P0/P1/P2 findings remain. P3 notes are documented and do not
block this SOW. No code was changed after the second-round review.

## Requirements

### Purpose

Ensure ai-viewer upgrades and restarts are reliable on the operator workstation:
a normal `scripts/install-system.sh` upgrade must stop and restart the ingester
without systemd timing out and sending `SIGKILL`, while preserving accepted
source events or making any unavoidable replay/expiry condition explicit.

This is SOW-0097 lineage debt. SOW-0097 cannot be considered fully closed while
the ingest/parity close-out leaves a restart path that can be force-killed by
systemd.

### User Request

The user asked whether SOWs between 0097 and 0105 are technical debt of SOW-0097
and stated that, if yes, SOW-0097 has not finished. This SOW covers the restart
timeout defect discovered during SOW-0097 install validation.

### Assistant Understanding

Facts:

- `scripts/install-system.sh` restarts `ai-viewer-ingest.service` and
  `ai-viewer-serve.service` during upgrades.
- During SOW-0097 install validation on 2026-06-24, the previous ingester
  process did not exit before systemd's stop timeout.
- The journal showed repeated shutdown flush retry warnings with
  `context deadline exceeded`, then systemd reported the ingester stop timed out
  and killed the old process with `SIGKILL`.
- The new ingester instance started cleanly afterward and resumed all configured
  operator sources.

Inferences:

- The bounded shutdown-drain path can still outlive the systemd stop window or
  can keep retrying/logging after the useful shutdown deadline has expired.
- A force-kill during shutdown is operationally unacceptable even if SQLite WAL
  safety prevents corruption. It makes event durability and cursor replay a
  matter of hope rather than a tested contract.

Unknowns:

- Whether the dominant root cause is worker retry behavior after drain expiry,
  oversized buffered-channel drain work, post-scan read-model backfill,
  unbounded final resolver work, systemd timeout sizing, or a combination.

### Acceptance Criteria

- A controlled automated test or integration harness reproduces the current
  shutdown timeout/failure mode or proves the relevant root cause without
  relying on private workstation data.
- `ai-viewer-ingest` stops cleanly under a heavy in-flight batch/restart
  scenario without systemd `SIGKILL`.
- Shutdown logs distinguish:
  - recovered transient flush retry;
  - bounded shutdown-drain expiry;
  - terminal data-loss or replay-required condition.
- Shutdown logs do not spam duplicate retry warnings after the drain context is
  already expired.
- Accepted source events are either committed before exit or left replayable by
  not advancing persisted source progress. Any replay dependency is logged
  explicitly.
- Specs and deployment/operator docs state the intended restart/shutdown
  behavior and timeout assumptions.
- Local gates pass, including focused ingest shutdown tests, command-level
  shutdown tests, and the system install/unit self-tests.

## Gap Analysis

### Evidence Reviewed

Repository evidence:

- `cmd/ai-viewer-ingest/main.go:280-288`: on SIGTERM/SIGINT the binary cancels
  adapters, waits 5 seconds for adapter goroutines, then calls `ing.Stop()`.
- `cmd/ai-viewer-ingest/main.go:437-452`: `waitWithTimeout` bounds adapter
  goroutine drain only; it does not bound `ing.Stop()`.
- `internal/ingest/ingester.go:350-357`: `Stop()` cancels worker context, then
  waits on `i.wg.Wait()` with no timeout and runs final resolver work with
  `context.Background()`.
- `internal/ingest/worker_runtime.go:11-14`: worker shutdown write/drain timeout
  is 10 seconds.
- `internal/ingest/worker_runtime.go:79-83`: cancellation drains buffered
  events, pulls one pending event, flushes final batch, and refreshes pending
  rollups.
- `internal/ingest/worker_runtime.go:112-123`: shutdown drains the current
  channel buffer and can trigger size-based flushes while already in shutdown.
- `internal/ingest/worker_runtime.go:159-197`: a failed flush logs
  `worker: batch flush retry` before checking whether the write context is
  already done during retry backoff.
- `internal/ingest/worker_runtime.go:219-252`: after lifecycle cancellation,
  flushes share one shutdown-drain context. Once it expires, later flush/refresh
  attempts inherit an already-expired context.
- `cmd/ai-viewer-ingest/main.go:266` and
  `cmd/ai-viewer-ingest/main.go:459-488`: the post-scan read-model backfill
  goroutine is launched outside the ingester wait group and runs under a
  5-minute context derived from `context.Background()`, not the shutdown
  context.
- `cmd/ai-viewer-ingest/sources.go:475`: adapter goroutines wait on
  `<-backfillDone` without a `ctx.Done()` escape before entering Tail mode.
- `cmd/ai-viewer-ingest/main.go:182`: the store close error is currently
  discarded, so shutdown-time close/checkpoint failures are not logged.
- `deploy/systemd-system/ai-viewer-ingest.service:32-39`: the system service has
  no explicit `TimeoutStopSec`.
- `deploy/systemd/ai-viewer-ingest.service:9-12`: the user service has the same
  implicit stop-timeout gap.
- `scripts/test/systemd-units-test.sh:43-47`: the existing systemd-unit static
  test only targets `deploy/systemd` user units; it does not lint
  `deploy/systemd-system`.
- `internal/store/store.go:307`: the SQLite DSN pins `busy_timeout(5000)`.
  Shutdown budgeting must account for how blocked writes behave when both
  `busy_timeout` and context cancellation are in play.
- `.agents/sow/specs/ingester.md:526-551`: the current spec explicitly says
  there is no process-level hard timeout beyond adapter wait plus per-worker
  bounded write/drain contexts.

External reference evidence:

- Official `systemd.service(5)` documents that `TimeoutStopSec` controls how
  long systemd waits for the service itself to stop, then forcibly terminates it
  with SIGKILL unless configured otherwise:
  `https://man7.org/linux/man-pages/man5/systemd.service.5.html`.
- Official `systemd.kill(5)` documents the default process kill flow:
  SIGTERM first, then SIGKILL/`FinalKillSignal` after `TimeoutStopSec` if
  processes remain:
  `https://manpages.ubuntu.com/manpages/focal/man5/systemd.kill.5.html`.
- Official Go `os/signal.NotifyContext` documentation states that the returned
  context is canceled when a listed signal arrives, and that calling the
  returned stop function unregisters signal behavior and may restore the
  default disposition for the signal:
  `https://pkg.go.dev/os/signal#NotifyContext`.
- Local mirrored service templates from Grafana Agent, Grafana Alloy,
  Prometheus Ansible, and Netdata set explicit `TimeoutStopSec` values. This is
  not proof ai-viewer should copy their values, but it is evidence that packaged
  daemons commonly make stop-timeout contracts explicit instead of relying on a
  manager default.
- Local workstation evidence on 2026-06-26:
  `systemctl show --property=DefaultTimeoutStopUSec` returned
  `DefaultTimeoutStopUSec=1min 30s`. This records the observed default class
  for the failure investigation, but the repository must still set explicit unit
  values instead of relying on the manager default.

### Current Contract Gaps

1. **The binary has no process-level stop bound after adapter shutdown.**
   Adapter goroutines are bounded to 5 seconds, but `Ingester.Stop()` can wait
   indefinitely on worker goroutines and final resolver work.

2. **The worker's 10-second context bounds writes, not the whole worker drain.**
   A worker can spend time in an active detached write, then enter cancellation
   drain and receive a fresh 10-second drain context. That can exceed the simple
   "5 s adapter + 10 s worker" mental model.

3. **The shutdown-drain context is one cached budget per worker.**
   `shutdownDrainContext()` is allocated once per worker runtime. Size flushes,
   the final `ctx done` flush, and idle rollup refresh all share whatever time
   remains in that one context. The target contract must decide whether the
   budget is intentionally per worker or should become per operation.

4. **Expired shutdown contexts are retried like transient failures.**
   Once the shutdown-drain context is already done, another `flushBatch` or
   `idleRefresh` can still report a recoverable retry. This matches the observed
   repeated `context deadline exceeded` warning shape.

5. **Terminal drain expiry is not a first-class outcome.**
   The current retry path preserves `rt.batch` and returns when the context is
   canceled during backoff, but the worker then exits. If the batch was not
   committed, the durable recovery guarantee relies on source replay and the
   cursor not having advanced. That condition needs an explicit log/error path
   and a test.

6. **The post-scan read-model backfill is not part of shutdown.**
   The backfill goroutine is not canceled by `cancelAdapters()`, not waited by
   `Ingester.Stop()`, and can keep using the same SQLite handle while shutdown
   proceeds to store close. During first scan, schema repair, or replay-heavy
   restart, this 5-minute `context.Background()`-derived path can dominate the
   stop budget.

7. **Adapter goroutines can block on `backfillDone` without ctx escape.**
   `runAdapter` closes the event channel with `defer close(events)`, which is
   the correct production path for workers to observe channel close. But while
   an adapter goroutine is parked on `<-backfillDone`, shutdown cancellation does
   not unblock it. The full-path test must prove cancellation still closes event
   channels and workers drain within budget.

8. **Final resolver pass is unbounded.**
   `resolver.linkOrphans(context.Background())` runs after all workers drain. A
   stuck SQLite call or unexpectedly large orphan set can still delay process
   exit past systemd's stop window.

9. **Systemd timeout is implicit in both install modes.**
   Even if the code becomes bounded, neither the system unit nor the user unit
   documents the intended shutdown budget. A workstation-specific manager
   default can change the effective behavior without changing this repository.

10. **System-unit static testing is missing.**
   The current systemd-unit self-test covers user units only. The recommended
   system install path renders `deploy/systemd-system` templates, so the static
   gate must cover that directory too.

11. **The stop budget is not decomposed.**
   The target contract must define a formula before choosing `TimeoutStopSec`.
   The formula must be serialization-aware: with the writer pool pinned to one
   connection, worker drain cost may be `sum(per-worker drain)` rather than a
   single max term unless tests prove the effective bound is parallel. The plan
   must write the exact arithmetic before specs/tests/code choose a concrete
   `TimeoutStopSec` value, for example:
   `max(adapter_grace, active_write_bound) + serialized_worker_drain_bound +
   backfill_bound + final_resolver_bound + store_close_bound + safety_margin`.

12. **SQLite busy-timeout/context-cancellation behavior is an assumption.**
   The 10-second drain bound is only meaningful if blocked SQLite writes observe
   the context before `busy_timeout(5000)` multiplied by retry attempts consumes
   the stop budget. This needs a focused contention/cancellation test or an
   explicit residual-risk note in the budget math.

13. **Current tests cover happy drain and cancellation races, not expiry.**
   Existing tests prove pending batches survive normal lifecycle cancellation,
   channel-close final flush uses the drain context, and idle rollups refresh
   under cancellation. They do not prove behavior when the drain context is
   already expired or when `Stop()` is under heavy in-flight flush pressure.

14. **Serve restart is related but not fully bounded by its HTTP timeout.**
   `scripts/install-system.sh` restarts the server unit too. The server's
   `http.Server.Shutdown` drain has a 30 s bound, but graceful shutdown waits
   for the notify poller to stop before the HTTP shutdown window starts, and
   that poller wait has no independent timeout. Implementation may keep serve
   out of scope, but the specs/tests must state that explicitly from the
   accurate property: HTTP drain is bounded; poller drain depends on cancelable
   SQLite notify queries.

15. **SIGKILL recovery can leave temporary UI staleness.**
   If the ingester is killed before a final batch commits, no notify rows exist
   for that uncommitted batch. Replay on the next ingester run should eventually
   emit notify rows, but the operator-visible staleness window and WAL replay
   cost should be acknowledged in the recovery contract.

16. **Terminal drain expiry has no explicit exit-code contract.**
    The process currently returns success after `ing.Stop()` logs a warning. If
    shutdown becomes bounded and exits before every accepted event is committed,
    the target contract must decide whether that terminal replay-required
    condition exits non-zero, or exits zero with a mandatory structured log. The
    plan must account for systemd's `Restart=on-failure` semantics correctly:
    auto-restart is generally suppressed for explicit `systemctl stop` /
    `systemctl restart`, so exit code is primarily an observability and
    non-shutdown failure contract for upgrade stops, not a guaranteed replay
    accelerator.

17. **Backfill, adapter drain, and store close are one shutdown dependency.**
    The post-scan backfill uses a `context.Background()`-derived timeout, while
    adapters can wait on `<-backfillDone>` without a shutdown-context escape.
    If shutdown starts during scan/backfill, adapter goroutines can consume the
    5 s adapter grace, event channels may remain open until process exit, the
    backfill can keep using the SQLite handle, and `Store.Close()` can block
    waiting for that work. The plan must treat these as one dependency chain.

18. **Resolver work can block in two places.**
    The final `resolver.linkOrphans(context.Background())` call is unbounded,
    and the resolver goroutine can already be inside `linkOrphans(ctx)` when
    shutdown cancellation begins. `i.wg.Wait()` must account for both cases.
    The budget and tests must prove resolver-loop in-flight work and the final
    resolver pass are bounded or explicitly skipped/deferred safely.

19. **The single-writer SQLite pool serializes shutdown work.**
    The writer uses `SetMaxOpenConns(1)`. Worker final flushes, read-model
    backfill, resolver work, and `Store.Close()` contend for the same connection
    budget. The stop budget cannot assume all worker drains finish in parallel
    unless tests prove the current contexts expire in parallel and still leave
    replay-safe state. The formula must account for serialization or document
    the tested bound.

20. **The observed systemd timeout value is evidence, not a contract.**
    The failure was "systemd stop timed out"; the local manager default observed
    for this SOW is `DefaultTimeoutStopUSec=1min 30s`. The shutdown budget and
    static unit value must still be anchored to an explicit repository-owned
    `TimeoutStopSec` value and safety margin, not to the host default.

21. **Serve unit timeout scope must be decided before implementation.**
    The installer restarts both `ai-viewer-ingest.service` and
    `ai-viewer-serve.service`. The serve binary has its own 30 s bounded HTTP
    shutdown path, but the notify-poller wait that precedes it is not separately
    bounded. It is lower risk than the ingester because it has no ingest drain,
    but both system and user serve unit templates also lack explicit
    `TimeoutStopSec`. The SOW must decide whether serve units receive an
    explicit timeout and poller-bound proof for consistency or remain on the
    systemd default with a written rationale and matching tests.

22. **Store close errors and close latency need a shutdown contract.**
    `Store.Close()` returns an error but the ingester currently discards it.
    `sql.DB.Close()` can also wait for in-flight queries to release the
    connection. Shutdown observability and the budget formula must include close
    error logging and either a close-latency bound or a documented tested
    assumption.

23. **Second-signal and abandoned-goroutine behavior is implicit.**
    The current `signal.NotifyContext` path cancels the signal context on the
    first SIGTERM/SIGINT, but it does not install a deliberate second-signal
    fast-exit handler. A second SIGTERM/SIGINT therefore does not, by itself,
    create an explicit force-terminate contract while notification remains
    active. If the target design wants second-signal fast exit, it must add and
    test that behavior; otherwise the spec must state that forced termination
    comes from systemd's final SIGKILL or process exit, and goroutines not
    drained before process exit are reaped by the OS.

24. **Source scan accounting can strand the centralized backfill.**
    `scanWG.Add(len(sources))` pre-adds one scan counter per configured source.
    If a source fails before `runAdapter` starts (unknown format, inaccessible
    path, adapter construction failure, cursor lookup path that prevents
    submit, or `ing.Submit` failure), no goroutine calls `scanWG.Done()`. Then
    `scanDone` never closes, the centralized read-model backfill never closes
    `backfillDone`, and already-started adapters can wait forever before Tail.
    This can directly produce the adapter grace-timeout symptom during
    shutdown.

25. **The shutdown budget depends on real adapters honoring cancellation.**
    The adapter contract requires `Scan` and `Tail` to respect `ctx`, but the
    shutdown path depends on the concrete adapters returning promptly so
    `runAdapter` can close its event channel. A cooperative fake adapter test is
    not enough by itself. The plan must either add per-adapter cancellation
    assertions for registered adapters or record explicit residual risk for any
    adapter path that cannot be tested, especially read-only SQLite paths.

26. **Adapter-owned resources have no graceful `Close()` hook.**
    The canonical adapter interface has `Name`, `Format`, `Scan`, `Tail`, and
    `ParseCursor`, but no `Close`. Adapter-owned resources such as fsnotify
    watchers, file handles, or read-only source DB handles are released when the
    adapter returns or the process exits. The shutdown contract must state this
    deliberately, or the implementation plan must add a close hook with tests.

27. **Replay safety needs at least one end-to-end adapter proof.**
    The writer can prove that an uncommitted batch does not advance
    `source_progress.cursor`, but source replay also depends on adapter cursor
    granularity. The plan must prove at least one representative source resumes
    from the unadvanced cursor and re-emits the dropped events after a bounded
    drain expiry, or explicitly classify each adapter's cursor-granularity
    residual risk.

28. **Shutdown-under-load tests need a deterministic failure-mode injector.**
    Existing E2E seeding sends SIGTERM after fixtures report scan completion,
    but it does not reproduce expired drain, failed flush retry, in-flight
    backfill, or resolver pressure. The command-level shutdown test must define
    how it injects slow/failing flushes, in-flight backfill, and resolver work;
    otherwise a clean empty/fixture shutdown proves only the happy path.

29. **Rendered system-unit tests must preserve existing hardening values.**
    `scripts/install-system.sh` renders service templates through token
    substitution. `systemd-analyze verify` does not prove optional hardening
    directives are preserved. Static tests must assert selected values such as
    `Restart`, `RestartSec`, `TimeoutStopSec`, resource limits, security
    hardening, and run-as operator fields survive rendering with the expected
    values.

30. **Startup signal registration has a non-graceful window.**
    `signal.NotifyContext` is registered only after `ing.Start()`, the
    post-scan backfill goroutine, and all configured source goroutines have
    been started. A SIGTERM/SIGINT delivered in that startup window follows the
    Go/runtime default signal disposition instead of the intended graceful path:
    adapters are not canceled, `Ingester.Stop()` is not called, and accepted
    in-memory events rely entirely on cursor replay. The shutdown contract must
    either close this window by registering signal handling before source work
    starts, or document it as an intentional startup-only non-graceful path with
    replay-safe recovery evidence.

31. **Systemd memory and IO controls are independent shutdown risk inputs.**
    The system ingester unit sets `MemoryHigh=4G`, `MemoryMax=8G`, and
    `IOSchedulingClass=idle`. A shutdown drain under high in-flight batch,
    backfill, resolver, or SQLite pressure can be killed by the memory cgroup or
    delayed by idle-priority writes before `TimeoutStopSec` is reached. The
    stop-budget formula must account for resource controls, or document tested
    residual risk and the safety margin.

32. **Shutdown forensics are insufficient if systemd kills the process.**
    Terminal drain-expiry logs cover the path where the process remains alive
    long enough to report expiry. If systemd sends SIGKILL before a terminal log
    is emitted, the journal can end with only retry warnings. The target
    observability contract needs an earlier structured shutdown-state marker
    and a terminal expiry/replay-required marker so a later operator can
    distinguish clean drain, bounded replay-required exit, and forced-kill
    interruption without reading private source paths or payloads.

33. **One-shot ingest subcommands have no signal/shutdown contract.**
    `dispatchSubcommand` routes `check-parity`, `rollups-backfill`,
    `fts-content-backfill`, and `reprice` before the daemon signal path is
    installed. These helpers use `context.Background()`-derived contexts and
    share the same DB/resource surfaces as the daemon. They also bypass the
    daemon's single-instance flock while some helpers open the canonical DB for
    write, so they can create a second OS-level writer while the daemon is
    running. The SOW must explicitly decide whether they share the daemon's
    bounded-shutdown and single-writer lock contract or are out of scope, and
    deployment docs must not imply they are safe to interrupt or run concurrently
    with service restarts unless tests prove it.

34. **Restart-ordering and flock release affect operator-visible latency.**
    `scripts/install-system.sh` restarts the ingester before the server, while
    the single-instance flock is released only after the process has finished
    deferred cleanup, including store close. This is not necessarily wrong, but
    the deployment contract should state the resulting staleness and restart
    latency window so the chosen `TimeoutStopSec` budget maps to the operator's
    observed upgrade delay.

35. **The worker drain contract is best-effort plus replay-safe, not lossless.**
    The current drain drains the buffered channel length and one pending event;
    events emitted later by an adapter that has not returned are not guaranteed
    to be committed before exit. This can be correct only because cursor
    progress advances inside committed transactions. The spec and tests must
    state this property directly so future work does not assume every accepted
    event is synchronously lossless on shutdown.

36. **Store-close latency includes database/sql and WAL-reader interactions.**
    Gap 22 captures close errors and close latency, but the plan must name the
    concrete contributors: `sql.DB.Close()` waits for in-flight connections,
    the writer pool has one connection, and SQLite WAL/checkpoint behavior can
    be affected by the server's reader handle during ingester-only restarts.
    Tests or residual-risk notes must cover these contributors explicitly.

37. **System install validates the server but not ingester liveness.**
    `scripts/install-system.sh` restarts the ingester and server, then waits for
    the HTTP server URL only. A successful server response can be served from an
    existing SQLite file even if the newly restarted ingester crashed or failed
    to start. Reliable upgrade validation must include an ingester-active check
    or another explicit post-restart ingester health signal, and the installer
    self-test must assert that behavior.

38. **Crash-safety, power-loss safety, and WAL-growth boundaries are implicit.**
    The replay contract relies on SQLite WAL crash safety for process kill, but
    the store uses `synchronous(normal)`, which is not the same as a power-loss
    durability guarantee. Repeated ingester-only restarts while the server keeps
    a reader open can also defer WAL truncation. The shutdown contract and
    runbook must distinguish process-kill replay safety from power-loss safety,
    and decide whether a final checkpoint/truncate is in scope or deliberately
    avoided to keep shutdown bounded.

39. **Systemd exit classification and OOM policy are part of the deployment
    contract.**
    If terminal drain expiry exits non-zero, `Restart=on-failure` and optional
    `SuccessExitStatus=` choices decide how systemd classifies that condition.
    Likewise, memory-limit kills interact with systemd's `OOMPolicy`. The SOW
    must require the implementation plan to make these choices explicit or
    document why the defaults are correct.

40. **Serve notify-poller shutdown has the same cancellation-assumption class.**
    The serve shutdown path stops the notify poller and waits for it before
    closing SSE clients and entering the 30 s HTTP `Shutdown` window. That wait
    is `<-done` with no timeout, so the plan must either scope serve in and
    bound/test notify-poller cancellation, or scope serve out with an explicit
    residual-risk note. Specs must not describe serve shutdown as fully bounded
    unless the poller wait is proven bounded.

41. **Systemd readiness/watchdog mechanisms are unconsidered.**
    Both system and user units currently use `Type=simple` and have no
    `NotifyAccess=` or `WatchdogSec=` contract. Because this SOW already needs
    post-restart ingester liveness validation and an explicit stop-budget
    contract, the implementation plan must consider or reject `Type=notify`,
    `sd_notify` readiness/stopping/watchdog messages, and `WatchdogSec` with a
    deployment-spec rationale. Rejection is acceptable if the dependency,
    portability, or complexity cost outweighs the benefit.

42. **Flush retry/backoff arithmetic is part of the shutdown budget.**
    `flushBatchWithWriteContext` can perform multiple flush attempts with
    exponential backoff under a single shutdown-drain context. With SQLite
    `busy_timeout(5000)`, the wall-clock cost is not just one write timeout; it
    includes retry attempts, backoffs, and whether the driver observes context
    cancellation promptly. The serialized budget and tests must account for
    retry/backoff/busy-timeout interaction and prove the selected drain bound.

43. **One-shot subcommand lock choices need a concrete matrix.**
    Gap 33 captures that subcommands bypass daemon locking, but the plan must
    enumerate each subcommand's DB access mode and lock decision:
    `check-parity`, `rollups-backfill`, `fts-content-backfill`, and `reprice`.
    Writer subcommands must either acquire/refuse on the daemon lock or be
    explicitly documented as unsafe to run concurrently with the daemon; read
    paths must not use a writer handle unless they truly need one.

44. **Installer ownership repair can race a running old unit.**
    `scripts/install-system.sh` runs recursive ownership repair on
    `/opt/ai-viewer` before restarting the units, while the previous ingester
    may still hold DB, WAL, SHM, log, and lockfile handles. The common case
    reasserts the same operator/group and is likely idempotent, but user/group
    changes or permission repair during a live writer can create confusing
    failures. The implementation plan must decide the safe ordering or scope
    for ownership repair and add a static installer assertion for that choice.

45. **Migration failure restart-loop behavior is implicit.**
    A startup migration error exits non-zero under `Restart=on-failure`, which
    can create a restart loop until operator intervention. This SOW does not
    need to solve migration safety broadly, but the deployment contract must
    either state that migration failure loops are out of scope or choose a
    systemd classification/circuit-breaker behavior if the implementation
    touches startup readiness.

### Required Additions Before This SOW Can Close

- A focused worker-runtime test that starts from an already-expired shutdown
  context and proves retry spam is suppressed and terminal drain expiry is
  reported exactly once.
- A focused worker-runtime test using the existing `workerRuntime.flush` seam to
  reproduce slow or failing flush behavior without real SQLite contention.
- A worker/ingester shutdown test that exercises a large buffered channel or
  repeated size-flush path under cancellation and proves `Stop()` returns within
  the documented budget.
- A durability/replay test that proves uncommitted events after drain expiry do
  not advance persisted source progress.
- A SQLite contention/cancellation test proving the driver stops blocked writes
  within the shutdown budget, or proving and documenting the exact
  `busy_timeout` contribution. The test or budget proof must include
  `flushMaxRetries`, retry backoff, and driver context-cancellation behavior so
  the selected drain bound is wall-clock true, not just nominal.
- A post-scan backfill shutdown test proving SIGTERM during read-model backfill
  cancels or bounds the backfill, does not race store close, and leaves derived
  read models safely rebuildable.
- A production-path adapter shutdown test proving `ctx.Done()` causes
  `runAdapter` to close the event channel and the corresponding worker to reach
  channel-close final flush within budget.
- A source-start failure accounting test proving `scanWG` drains when any source
  fails before `runAdapter` starts, `scanDone` and `backfillDone` still close,
  and successful sources are not stranded before Tail. The test and plan must
  treat the `scanWG` leak and adapters blocked on `<-backfillDone>` as one
  causal chain, not unrelated bugs.
- Per-adapter cancellation tests, or explicit adapter-by-adapter residual-risk
  entries, proving concrete adapters return promptly from `Scan`/`Tail` when
  `ctx` is canceled. The adapter list must explicitly cover read-only DB handles
  and fsnotify watchers where a registered adapter owns them, including the
  opencode read-only SQLite handle and WAL watcher.
- A serve shutdown in/out decision based on the accurate serve property:
  notify-poller wait is unbounded unless tested, while HTTP `Shutdown` has a
  30 s bound. If serve is scoped in, tests must prove notify-poller cancellation
  returns within the selected budget; if scoped out, specs must record the
  residual risk.
- A final-resolver shutdown test or command-level test that proves resolver work
  is bounded during process shutdown.
- A resolver-loop in-flight test proving shutdown cancellation does not let an
  already-running resolver pass block `i.wg.Wait()` beyond the documented
  budget.
- A command-level or integration shutdown test that drives the combined path
  under load: adapter cancellation, event-channel close, worker drain, optional
  backfill, final resolver, store close, and process exit within the documented
  budget.
- A combined backfill/adapters/store-close shutdown test proving shutdown
  cancels or bounds read-model backfill, unblocks adapters waiting for
  `backfillDone`, waits for any scoped backfill cleanup before closing the
  store, and does not race `Store.Close()`.
- A terminal-drain-expiry exit-code test proving the selected process exit
  behavior and the correct systemd semantics for manual stop/restart versus
  spontaneous failure.
- An end-to-end replay-after-drain-expiry test for at least one representative
  adapter, proving uncommitted events are re-emitted from an unadvanced cursor
  after restart.
- A store-close test or integration assertion proving close errors are logged
  and close latency is accounted in the shutdown budget.
- A shutdown-under-load harness or command-level test with deterministic
  slow/failing flush, in-flight backfill, and resolver-pressure injection. A
  fixture-only happy shutdown is insufficient evidence for this SOW.
- Static unit tests that fail if either ingester unit lacks the explicit
  stop-timeout contract after this SOW defines one:
  - `deploy/systemd-system/ai-viewer-ingest.service` via a new or extended
    system-unit static test.
  - `deploy/systemd/ai-viewer-ingest.service` via
    `scripts/test/systemd-units-test.sh`.
- The static test must assert the expected `TimeoutStopSec` value, not only the
  presence of the directive. If serve units are scoped in, the same assertion
  applies to `deploy/systemd-system/ai-viewer-serve.service` and
  `deploy/systemd/ai-viewer-serve.service`; if they are scoped out, the test
  must preserve the documented out-of-scope decision.
- A deployment decision that considers or rejects `Type=notify`, `sd_notify`
  readiness/stopping/watchdog messages, `NotifyAccess=`, and `WatchdogSec`.
  Rejection must include a rationale such as dependency cost, portability,
  complexity, or sufficient evidence from explicit timeout and liveness checks.
- `scripts/test/install-system-test.sh` must verify rendered system units keep
  the selected stop-timeout value and must not drop existing hardening
  directives while rendering. Assertions must check exact directive values, not
  mere presence.
- `scripts/install-system.sh` must validate post-restart ingester liveness in
  addition to server HTTP readiness, and `scripts/test/install-system-test.sh`
  must assert that check exists.
- `scripts/install-system.sh` must either avoid recursive ownership repair while
  the old ingester may still be running, or document why the live `chown -R`
  ordering is safe for same-user upgrades. The installer self-test must assert
  the selected ordering.
- The installer/runbook must either warn when a restart's previous ingester was
  force-killed, or explicitly document that previous-stop forensics are journal
  evidence only and outside installer success/failure detection.
- A startup-signal test or contract proof that either closes the signal-handler
  window before source work starts, or records the startup window as a
  replay-safe non-graceful path with bounded blast radius.
- A shutdown resource-budget test, measurement, or documented residual-risk
  proof that covers `MemoryHigh`, `MemoryMax`, and idle-priority IO under the
  shutdown-under-load harness. If the selected `TimeoutStopSec` or drain design
  depends on these values, static/rendered-unit tests must assert the exact
  values that make the budget true.
- A serialized shutdown-budget proof that uses the observed local
  `DefaultTimeoutStopUSec=1min 30s` only as evidence, chooses a concrete
  repository-owned `TimeoutStopSec`, and states whether worker drain cost is
  summed across sources or proven parallel. Static tests must assert the exact
  selected value.
- A shutdown-forensics test proving structured logs include an early
  shutdown-state marker and a terminal drain-expiry/replay-required marker
  without raw source paths, payloads, hostnames, or operator identity. The test
  must make clear what evidence remains in journald if systemd kills the process
  before terminal expiry logging.
- A one-shot subcommand signal-scope decision for `check-parity`,
  `rollups-backfill`, `fts-content-backfill`, and `reprice`: either implement
  and test bounded SIGTERM plus single-instance locking behavior for them, or
  document them as out of scope for daemon restart semantics and unsafe to
  interrupt or run concurrently with the daemon unless their own command
  contract says otherwise. The implementation plan must include a matrix for
  each subcommand: DB handle mode, write/read requirement, lock/refuse behavior,
  and signal/shutdown behavior.
- A deployment/restart latency assertion or spec note covering ingester-before-
  server restart order, UI/SSE staleness while the ingester is stopped, and the
  fact that the single-instance flock is released only after deferred cleanup.
- A best-effort-drain replay test or explicit proof that events accepted but not
  committed before exit are re-emitted from the unadvanced cursor and deduped
  without corrupting aggregates.
- A deployment/systemd decision covering terminal drain-expiry exit
  classification (`Restart=on-failure`, optional `SuccessExitStatus=`) and
  memory-kill classification (`OOMPolicy` or documented default). If startup
  migration failures remain subject to a restart loop, the deployment spec or
  runbook must say so explicitly or define a circuit-breaker/classification.
- A deployment/systemd decision stating whether `KillMode`, `KillSignal`, and
  `FinalKillSignal` defaults are intentional. The default final SIGKILL must
  remain the last-resort terminator unless a later SOW changes the kill
  contract with tests.
- A store-close/WAL decision covering whether shutdown runs an explicit final
  checkpoint/truncate, and, if not, how the runbook explains possible WAL growth
  across ingester-only restarts with a live server reader.
- Spec updates before tests/code:
  - `.agents/sow/specs/ingester.md`: define terminal shutdown-drain expiry,
    replay semantics, duplicate-warning suppression, backfill shutdown, SQLite
    contention assumptions, final resolver bound, worker serialization under
    `SetMaxOpenConns(1)`, terminal exit-code behavior, second-signal behavior,
    source-start scan accounting, adapter cancellation/resource-release
    behavior, adapter-cursor replay assumptions, startup signal behavior,
    best-effort-drain semantics, one-shot subcommand signal/concurrency/lock
    scope, precise second-signal behavior under `signal.NotifyContext`, process
    kill versus power-loss durability boundaries, store-close/WAL behavior, the
    pprof server's off-by-default process-exit cleanup scope, and the existing
    duplicate step numbering in the graceful-shutdown sequence.
  - `.agents/sow/specs/deployment.md`: define the systemd stop-timeout budget
    formula and its relationship to ingester shutdown budgets in system and user
    install modes, including the serve-unit in/out decision, sum-vs-max
    budget arithmetic, rendered-unit assertions, memory/IO resource-control
    assumptions, ingester-before-server restart ordering, flock-release timing,
    post-restart ingester health validation, exit-status/OOM classification,
    `Type=notify`/watchdog decision, kill-mode defaults, ownership-repair
    ordering, migration-failure restart-loop behavior, and operator-visible
    restart latency/staleness.
  - `.agents/sow/specs/observability.md`: define the shutdown log messages and
    structured fields for transient retry, drain expiry, replay-required,
    terminal drop, backfill cancellation, final resolver timeout, store close
    failures, early forced-kill forensics, selected non-zero/zero exit behavior,
    and exact log field names for clean-drain, replay-required, and forced-kill
    diagnostic paths.
- Skill/doc updates if the restart validation process changes:
  - `.agents/skills/project-deployment/SKILL.md` for operator restart checks.
  - `scripts/test/install-system-test.sh` and/or unit tests for static gate
    enforcement.
  - An operator runbook note for diagnosing a prior forced-kill restart without
    exposing private source paths or payloads.

### Non-Goals

- Do not delete or rebuild the user's derived database as part of this SOW.
  Database reset is a separate operational action.
- Do not hide the issue by only increasing `TimeoutStopSec`.
- Do not weaken flush retry behavior for normal transient SQLite contention.
- Do not disable systemd's final SIGKILL behavior; the process must terminate
  cleanly within a bounded budget.
- Do not log raw source paths, payload contents, hostnames, or operator identity.
- Do not change server shutdown unless the plan proves the server unit must share
  the same stop-timeout contract for consistency.

## Gap Review Gate

Status: converged in round 7.

Positive vote required: `NOTHING MORE CAN BE DONE`.

The reviewers must check whether this gap analysis misses any shutdown,
durability, source-replay, SQLite, systemd, logging, testing, installation, or
side-effect concern before implementation planning begins.

## Pre-Implementation Gate

Status: implementation plan converged; ready for spec updates.

Problem / root-cause model:

- A real upgrade restart exposed that ingester shutdown can outlive systemd's
  stop timeout. The observed warnings were repeated shutdown flush retries with
  `context deadline exceeded`, followed by systemd `SIGKILL`.
- The likely fault domain is the interaction between worker shutdown-drain retry
  behavior, buffered channel draining, post-scan read-model backfill, final
  resolver work, SQLite busy-timeout behavior, and the service stop timeout.

Affected contracts and surfaces:

- `ai-viewer-ingest` shutdown behavior.
- `scripts/install-system.sh` upgrade experience.
- `scripts/install-systemd-user.sh` and user-unit restart experience.
- `deploy/systemd-system/ai-viewer-ingest.service` stop timeout assumptions.
- `deploy/systemd/ai-viewer-ingest.service` stop timeout assumptions.
- `deploy/systemd-system/ai-viewer-serve.service` and
  `deploy/systemd/ai-viewer-serve.service` stop-timeout scope decision.
- `ai-viewer-serve` startup/shutdown behavior, including signal registration,
  notify-poller wait, HTTP shutdown, and read-only store close.
- Ingest data durability, source progress, and operator trust during upgrades.
- Derived read-model backfill durability/rebuild semantics.
- Notify/SSE freshness after restart if a final batch was not committed before
  process exit.
- Process exit-code semantics for bounded shutdown failures and replay-required
  outcomes.

Existing patterns to reuse:

- Worker shutdown-drain tests in `internal/ingest/worker_test.go`.
- Post-scan backfill gating tests in `cmd/ai-viewer-ingest/main_test.go`.
- Static installer/unit validation in `scripts/test/install-system-test.sh` and
  `scripts/test/systemd-units-test.sh`.
- Structured warning/error logging patterns in `internal/ingest`.

Risk and blast radius:

- Medium. The runtime path is shutdown/upgrade only, but it touches ingest
  durability and source progress.
- The fix must not trade forced-kill avoidance for silent event loss.

Sensitive data handling plan:

- Do not commit live source paths, hostnames, operator names, raw payloads, or
  private journal snippets. Use `$HOME`, `$STATE_DIR`, and redacted summaries
  only.

### Decisions

- **Timeout budget:** set `TimeoutStopSec=45s` for ingester and serve units in
  both system and user install modes. The ingester code-path budget is:
  phase 1 `max(adapter_grace=5s, ingesterStopTimeout=30s) = 30s`, phase 2
  `backfill_cancel_wait=5s`, phase 3 `store_close=5s`, for a 40s bounded path
  plus 5s unit-level margin. `ingesterStopTimeout=30s` is
  `worker_exit_bound=25s + final_resolver<=5s + internal_margin=0s`.
  The idle-worker drain bound is 15s: `shutdownDrainTimeout=10s` plus a
  conservative post-deadline SQLite `busy_timeout(5000)` tail for any flush
  attempt that starts before the drain context expires. The current worker has
  `flushMaxRetries=3` plus retry backoff, so the test proof, not the prose math,
  is authoritative: persistent contention must return replay-required within the
  drain context plus the single post-deadline busy tail, or the budget must be
  revised. The worst mid-flush worker bound is 25s:
  `detached_active_write=10s` plus the same conservative post-deadline
  `busy_timeout(5000)` tail plus a fresh `shutdownDrainTimeout=10s` drain. The
  final resolver uses only the
  caller's remaining deadline, up to 5s; if workers consume the full 30s,
  resolver work returns a timeout outcome instead of extending `StopContext`.
  `SetMaxOpenConns(1)` serialization affects which workers can commit before
  the deadline; it does not extend process exit because workers that cannot
  commit before their context expires report replay-required or timeout and
  return. Contended workers time out while parked on the single writer or inside
  a bounded write context, so wall-clock worker exit is expected to be max-like,
  not `sum(source_count * per-worker bound)`. The N=5 validation tests must
  prove both idle-worker and mid-flush bounds; if they exceed 45s, the spec and
  budget must be revised before implementation review or SOW close. Under the
  current invariant, the fresh shutdown-drain phase does not add a second
  `busy_timeout` tail: there is one daemon writer connection, serve is a
  read-only WAL/query-only client, and writer one-shots refuse the daemon lock
  while the service is running. If any of those three constraints change, the
  worker budget must be re-derived before the implementation can be accepted.
  Under worst-case multi-source mid-flush serialization, a bounded-guard timeout
  (exit 1, replay-required evidence) is acceptable for the no-SIGKILL safety
  goal; a clean-drain exit 0 is still required for the idle-worker bounded-drain
  case. The serve budget is:
  `notify_poller_wait=5s + http_shutdown=30s + store_close=5s + margin=5s`.
  The deployment spec must state the expected source-count assumption
  (default/operator install ≤5 sources; custom larger source sets rely more on
  replay-required and must re-derive the budget if they require clean commits).
- **No `Type=notify` / watchdog in this SOW:** keep `Type=simple`. Official
  `sd_notify` supports `READY=1`, `STOPPING=1`, `WATCHDOG=1`, and
  `EXTEND_TIMEOUT_USEC`, but it requires a notify socket contract and
  `NotifyAccess=`. That dependency and readiness-state model are not necessary
  to solve this restart timeout. Use explicit timeouts, bounded code paths,
  structured shutdown logs, and installer `systemctl is-active` validation
  instead. Revisit `Type=notify` only in a later service-readiness SOW.
- **No `WatchdogSec` in this SOW:** watchdogs are useful for steady-state hangs,
  but they add another failure mode during long scans and shutdown. The existing
  memory controls stay; watchdog semantics need a separate design.
- **Systemd kill defaults stay intentional:** do not add `KillMode`,
  `KillSignal`, or `FinalKillSignal`. Defaults (`control-group`, `SIGTERM`,
  final `SIGKILL`) are correct for a single-process Go daemon; the code handles
  `SIGTERM`/`SIGINT`, and systemd's final kill remains the last resort.
- **Systemd OOM policy is explicit for the memory-capped system ingester:**
  add `OOMPolicy=stop` to `deploy/systemd-system/ai-viewer-ingest.service` so a
  `MemoryMax` kill stops/fails the unit predictably under `Restart=on-failure`.
  User units and serve units have no `MemoryMax` in this SOW and do not get an
  `OOMPolicy` directive.
- **Shutdown is best-effort plus replay-safe, not lossless:** a final batch that
  cannot commit before the bounded drain deadline is reported once as
  replay-required and exits without advancing source progress. Permanent
  non-shutdown write failures remain errors.
- **Exit behavior:** clean drain and replay-required drain expiry exit 0 because
  stop/restart was operator-requested and replay is expected on next start.
  Store-close failures, permanent worker drop errors, startup/migration errors,
  and unbounded-shutdown guard failures exit non-zero. `main.go` maps typed
  `StopContext` outcomes explicitly: clean-drain and replay-required outcomes
  return exit 0; timeout, store-close-failure, permanent-worker-failure, and
  bounded-guard-failure outcomes return exit 1.
- **Stop API compatibility:** production shutdown paths call
  `StopContext(stopCtx)` with a bounded deadline. The legacy `Stop()` wrapper
  uses the same bounded default, not `context.Background()`, so no caller keeps
  the old unbounded behavior by accident. `Stop()` keeps its `error` return:
  clean-drain and replay-required outcomes map to `nil`; the legacy
  `ErrNotStarted` pre-start case is preserved as a non-nil error before the
  typed shutdown outcome path runs; timeout, store-close failure, permanent
  worker failure, and bounded-guard failure map to non-nil errors. A concurrent
  non-owning `StopContext` caller returns an `already_stopping` or
  `already_stopped` typed outcome immediately; the compatibility `Stop()` wrapper
  maps those follower outcomes to `nil`, preserving idempotent "safe to call
  again" behavior while making the typed API explicit. The implementation must
  use an explicit in-progress/completed state, done channel, or equivalent; the
  current single `stopped` boolean cannot distinguish `already_stopping` from
  `already_stopped`.
- **Serve is scoped in for timeout consistency:** bound the notify-poller wait
  before HTTP shutdown and give serve units the same explicit
  `TimeoutStopSec=45s`.
- **Migration-failure restart loops:** do not add `StartLimitBurst` or
  `StartLimitIntervalSec` directives in this SOW. Keep distribution/systemd
  defaults and document migration-failure restart-loop diagnosis in the
  deployment spec and runbook. The runbook must explain how to inspect the
  effective values with `systemctl show` rather than hard-code cross-distro
  assumptions. A custom circuit breaker is a later deployment policy SOW, not
  part of the restart timeout fix.
- **One-shot subcommands:** `check-parity` remains read-only and does not take
  the daemon write lock. Writer subcommands (`rollups-backfill`,
  `fts-content-backfill`, `reprice`) become daemon-lock aware with a
  `--state-dir` option resolved through the same `resolveStateDir` path as the
  daemon. The default is the configured state directory, not `filepath.Dir(db)`,
  so a custom `--db` with the default state directory probes the same
  `<stateDir>/ingester.lock` file the daemon holds. If the daemon lock is held,
  writer one-shots fail with a clear message that tells the operator to stop the
  daemon or use a different `--state-dir` when appropriate. All one-shots use
  signal-aware contexts. The lock is keyed on `--state-dir`; one-shots targeting
  the system-install DB must pass the system state directory, normally
  `/opt/ai-viewer/data`, to detect the system daemon lock.
- **Installer forced-kill detection:** the installer will not scrape journald to
  infer whether the previous stop was force-killed. The authoritative evidence
  is the structured shutdown markers plus `systemctl status`/journal runbook.

### Spec Deltas

- `.agents/sow/specs/ingester.md`
  - Define early signal registration, second-signal behavior, shutdown budgets,
    best-effort replay-safe drain semantics, terminal replay-required logging,
    final resolver timeout, read-model backfill cancellation, `scanWG` source
    failure accounting, one-shot command signal/lock behavior, store-close
    handling, adapter-owned resource-release behavior with no new `Close()`
    hook, power-loss vs process-kill boundaries, and fixed graceful-shutdown
    step numbering. Also state that the pprof server is intentionally outside
    the shutdown wait group because it is off by default and operator-gated;
    process exit reaps its listener. If `--pprof` is enabled, in-flight pprof
    handlers are a documented residual and may keep the process alive until the
    unit timeout; `StopContext` does not wait for or control them. Define the
    legacy `Stop()` typed-outcome-to-error mapping, preserve `ErrNotStarted`,
    and state that concurrent `Stop()` / `StopContext()` callers return explicit
    owner/follower outcomes under the existing idempotency guard. Document the
    intentional shutdown sequence change from sequential adapter wait then
    ingester stop to parallel adapter wait plus `StopContext`, and record that
    `detachedWriteContext` helper goroutines are bounded to active writes, not
    part of the wait group, and reaped by process exit if the OS kills the
    process first. State that `runAdapter` panic recovery is out of scope for
    this SOW; panics remain process-fatal, matching current behavior.
- `.agents/sow/specs/deployment.md`
  - Define `TimeoutStopSec=45s` for system/user ingester and serve units, the
    phase-based budget formula, source-count assumption, and sum-vs-max worker
    drain arithmetic, the rejection rationale for `Type=notify`/`WatchdogSec`,
    intentional kill defaults, resource-control assumptions, explicit system
    ingester `OOMPolicy=stop`, installer stop/start/chown ordering,
    post-restart ingester liveness validation, the fact that installer liveness
    polling catches immediate startup failures but not every delayed migration
    failure after the poll window, migration failure restart-loop diagnosis
    using the effective distribution/systemd `StartLimitBurst` /
    `StartLimitIntervalSec` values visible through `systemctl show`,
    process-kill versus power-loss safety, one-shot lock-key behavior for system
    installs, no
    final checkpoint/truncate during shutdown, WAL-growth expectations with a
    live server reader as operator-observable residual risk rather than a
    numeric bound, restart latency/staleness expectations, the
    no-second-`busy_timeout` worker-budget invariant, and the fact that a
    bounded-guard exit 1 during operator stop/restart may leave the previous
    unit result visible in `systemctl status` as forensic evidence.
- `.agents/sow/specs/observability.md`
  - Define structured shutdown log messages and exact field names for shutdown
    start, adapter-grace expiry, worker drain retry suppression, replay-required
    drain expiry, backfill cancellation/timeout, final resolver timeout,
    store-close failure, clean drain, and forced-kill forensic boundaries. The
    shutdown-start marker must be emitted synchronously from the signal-observer
    path before deferred shutdown work begins.
- `.agents/sow/specs/presenter.md`
  - Define serve-side graceful shutdown updates: run-level signal registration
    before read-only store open/schema check, signal-aware context propagation
    into `newServeRuntime` and `serveHTTP`, bounded notify-poller wait, bounded
    read-only store close with double-close prevention, and the unchanged 30s
    HTTP `Shutdown` window.
- `.agents/sow/specs/quality-gates.md`
  - Update the systemd-unit static gate description unconditionally so it records
    the expanded per-variant coverage across both `deploy/systemd` and
    `deploy/systemd-system`, even if the implementation extends the existing
    `scripts/test/systemd-units-test.sh` instead of adding a new script.

### Implementation Plan

1. **Specs first.**
   Update `ingester.md`, `deployment.md`, `observability.md`, `presenter.md`,
   and `quality-gates.md` with the target behavior above before writing tests or
   runtime code.

2. **Introduce shutdown constants and result types.**
   Add constants in their owning packages rather than creating new cross-binary
   coupling: `internal/ingest` owns worker drain, final resolver, and
   `ingesterStopTimeout`; `cmd/ai-viewer-ingest` owns adapter grace, backfill
   cancel, store close, and ingester unit timeout; `cmd/ai-viewer-serve` owns
   poller wait, HTTP shutdown, store close, and serve unit timeout. Add
   `ingesterStopTimeout=30s` for production `StopContext` callers:
   worst-case `worker_exit_bound=25s + final_resolver<=5s`. `Stop()` uses the
   same 30s default when delegating to `StopContext`. Add typed shutdown
   outcomes so code and tests can distinguish clean drain, replay-required
   drain expiry, permanent worker failure, resolver timeout, backfill timeout,
   store close failure, already-stopping/already-stopped follower calls, and
   bounded guard failure. `internal/ingest` exposes a typed `ShutdownOutcome`
   enum plus a `ShutdownResult` struct; `StopContext(ctx)` returns
   `(ShutdownResult, error)`, and failure/replay classes also have sentinel
   errors so callers and tests can use `errors.Is`. Add test-only timeout seams
   in the owning packages: `internal/ingest` uses an unexported
   `shutdownTimeoutConfig` / option visible only to package tests, while command
   packages use unexported package-level test hooks restored with `t.Cleanup`.
   Wall-clock shutdown tests run with proportionally scaled millisecond budgets
   while production constants stay pinned to the documented 5s/10s/30s/45s
   values.

3. **Fix worker shutdown drain behavior.**
   Keep normal transient flush retries for non-shutdown writes. For shutdown
   writes, check `writeCtx.Err()` before logging recoverable retries, suppress
   duplicate expired-context retry warnings, preserve uncommitted batches for
   source replay, and emit exactly one replay-required warning per source/reason
   when the drain deadline prevents commit. Document and test that the effective
   shutdown retry count is wall-clock bounded by the drain context: under
   persistent SQLite contention, a worker may see fewer than `flushMaxRetries`
   attempts and must report replay-required rather than keep retrying.

4. **Add bounded ingester stop.**
   Add `StopContext(ctx)` while keeping `Stop()` as a compatibility wrapper.
   `StopContext` first preserves the legacy pre-start guard: if the ingester was
   never started, it returns `ErrNotStarted` before entering the typed shutdown
   outcome path. It also preserves the existing `i.stopped` / mutex idempotency
   guard so concurrent `Stop()` and `StopContext()` calls have one owner; a
   follower returns `already_stopping` while the owner is active and
   `already_stopped` after shutdown has completed, while `Stop()` maps those
   follower outcomes to `nil`. Use an explicit in-progress/completed state,
   done channel, or equivalent; the current single `stopped` boolean is not
   sufficient to distinguish the two follower outcomes. `StopContext` cancels
   workers and waits for the worker wait group by running `wg.Wait()` in a
   goroutine and selecting on
   `ctx.Done()`. If the caller's deadline expires before workers drain,
   `StopContext` returns a timeout typed outcome without blocking indefinitely.
   The final resolver pass gets `min(remaining caller deadline, 5s)`, not an
   unconditional fresh 5s; if the caller deadline has expired, resolver work is
   skipped or returns a timeout outcome without extending the 30s stop phase.
   `StopContext` then stops the resolver and returns typed shutdown outcomes.
   `StopContext` exposes typed outcomes for production callers; `Stop()` keeps
   its existing `error` return, maps clean-drain and replay-required to nil,
   maps `ErrNotStarted`, timeout, and failure outcomes to non-nil errors,
   creates a bounded default context using `ingesterStopTimeout=30s`, and
   delegates to `StopContext`. It must not call
   `StopContext(context.Background())`. It must not abandon worker goroutines
   and then close SQLite underneath them; if the wait deadline is exceeded, the
   main process exits through the bounded guard without claiming a clean close.

5. **Make command shutdown orchestration bounded.**
   Create a top-level `signal.NotifyContext` at the start of `run`, before
   `dispatchSubcommand`, and pass it into `dispatchSubcommand(ctx, args, ...)`
   so one-shots derive from the same signal-aware parent context. The daemon path
   reuses that context after logger/config setup and before `store.OpenWriter`,
   `ing.Start`, backfill, source scanning, or any other long-running DB/source
   work. `store.OpenWriter` already accepts a `context.Context`; pass the
   signal-aware startup context at every command call site instead of
   `context.Background()` or a context that cannot be canceled by SIGTERM, so
   migrations and schema repair observe cancellation through their existing
   `ExecContext` / `QueryContext` calls. On first signal, call the returned
   `stop()` so a second SIGTERM/SIGINT uses the default disposition, then start
   the bounded shutdown path. Cancel adapters and ingester work together so
   worker drain runs in parallel with adapter grace: start adapter waiting in a
   goroutine at the same time as `StopContext`, record adapter-grace expiry, and
   do not run a sequential adapter wait after `StopContext` returns. Replace
   deferred store close with explicit close logging so terminal guard paths do
   not silently block on `sql.DB.Close()`. The main shutdown path passes
   `ingesterStopTimeout=30s` to `StopContext`, then runs explicit ingester store
   close under its own 5s accounting window by running `Store.Close()` in a
   goroutine and selecting against a timer; on timeout, log a bounded writer
   close timeout and let process exit close the handle. `main.go` converts typed
   shutdown outcomes into process exit codes: clean drain and replay-required
   return 0; timeout, store-close failure, permanent worker failure, and
   bounded-guard failure return 1. The existing
   `defer ws.Close()` must be removed or made conditional so a bounded-guard
   path that deliberately skips explicit store close does not still close the
   store while backfill or workers may be alive. Startup and other pre-signal
   error paths that currently rely on the defer must use a logged close helper
   or a conditional defer that only closes when safe. If SIGTERM/SIGINT arrives
   during partial startup after the store is opened but before `ing.Start`
   completes, the path must close any opened store, release the flock through
   existing defers, log the early shutdown marker, and avoid calling
   `StopContext` on a nil or unstarted ingester.
   Close strategy matrix:
   - before store open succeeds: no store close;
   - after store open but before `ing.Start`: bounded logged close, no
     `StopContext`;
   - after `ing.Start` but before source goroutines/backfill: `StopContext`, then
     bounded logged close;
   - normal shutdown: parallel adapter wait plus `StopContext`, bounded backfill
     wait, then bounded logged close;
   - bounded worker/backfill guard while live SQLite work may still exist: skip
     explicit close, log the skip, and let process exit close handles;
   - ordinary startup/config errors after store open and before live background
     work: bounded logged close.

6. **Fix post-scan backfill and source-start accounting.**
   Make `startPostScanBackfill` accept the shutdown context and return both
   `backfillDone` and `backfillWait <-chan struct{}`; `backfillDone` preserves
   the adapter gating contract, and `backfillWait` closes when the backfill
   goroutine exits. The backfill goroutine is the sole closer of both channels:
   shutdown cancels the context and never closes either channel directly, avoiding
   double-close panics. Shutdown waits with
   `select { case <-backfillWait: ...; case <-timer.C: ... }` using the 5s
   backfill budget. If shutdown occurs before `scanDone`, the goroutine observes
   `shutdownCtx.Done()`, logs/skips the derived-data rebuild as retryable, closes
   `backfillDone`, then exits and closes `backfillWait`; if shutdown occurs
   during backfill, cancel the backfill context and report the derived-data
   rebuild as safely retryable.
   The main shutdown path waits for the backfill goroutine within
   `backfill_cancel=5s` before explicit `Store.Close()`. If the backfill does
   not return in that window, log a bounded-guard failure, skip explicit store
   close, and exit non-zero so the OS closes handles without racing a live
   SQLite operation. When `startSource` fails before launching `runAdapter`,
   decrement `scanWG` for that source with a single-exit pattern. The
   implementation may either preserve the existing pre-add in the caller and
   balance failed source slots with `scanWG.Done()`, or move per-source
   `scanWG.Add(1)` into the source-start path only if the `scanWG.Wait()`
   goroutine starts after all Adds are complete. It must not call `Add` while a
   `Wait` goroutine is already observing a zero counter. Change `runAdapter` to
   wait on either `backfillDone` or `ctx.Done()` before Tail.

7. **Bound serve shutdown.**
   Move serve signal registration to `run` before `newServeRuntime` so
   `store.OpenReader`, schema check, presenter construction, notify polling, and
   HTTP serving share one signal-aware context. `serveHTTP` consumes that context
   instead of registering a second signal handler. Bound notify-poller wait
   before SSE shutdown and HTTP `Shutdown`. Log if the poller does not stop in
   the configured wait; then continue with SSE disconnect and HTTP shutdown. Add
   either a notify-poller cancellation test under SQLite contention or an explicit
   residual-risk note that the 5s poller wait is best-effort and covered by the
   45s unit budget. Log read-only store close errors in `serveRuntime.close`.
   Enforce the 5s serve store-close budget by running `Store.Close()` in a
   goroutine and selecting against a timer; on timeout, log a bounded read-only
   close timeout and let process exit close the handle. Prevent double close by
   making `runtime.close` idempotent with an explicit closed flag or nil-guard,
   or by clearing/disabling the deferred close after the bounded close path
   starts.

8. **Update systemd units and installer.**
   Add `TimeoutStopSec=45s` to system/user ingester and serve templates. Add
   `OOMPolicy=stop` to the memory-capped system ingester unit. Preserve existing
   hardening/resource directives. `OOMPolicy=stop` applies only to the system
   ingester unit; serve units do not get `OOMPolicy` because they have no
   `MemoryMax`. Change `scripts/install-system.sh` from live `chown -R` before
   restart to a safe stop/chown/start ordering for upgrades: install
   binaries/units, daemon-reload, stop units if active, repair ownership, start
   ingester, verify ingester active after a settle/poll window, start/verify
   serve, then re-check ingester active after the server readiness loop. Keep
   first-install behavior simple when units are absent. `scripts/install-systemd-user.sh`
   is out of scope for liveness validation because it installs user units but
   does not auto-start them; it still copies updated templates, and static unit
   tests cover those user templates.

9. **Add ingester liveness validation.**
   After starting the ingester, `scripts/install-system.sh` must poll
   `systemctl is-active --quiet ai-viewer-ingest.service` every 1s for up to
   15 attempts to catch immediate startup/migration/source-resolution failures.
   It must re-check ingester liveness after the server HTTP wait loop. On
   failure, print `systemctl status` and a bounded recent journal hint. Server
   HTTP readiness remains the serve validation. This check is immediate
   liveness evidence, not a replacement for the migration restart-loop runbook;
   a delayed migration failure after the poll window is diagnosed from unit
   status, journal markers, and the effective `StartLimit*` values.

10. **Make writer one-shot subcommands lock aware.**
    Add shared helper code for optional daemon-lock acquisition. One-shots derive
    from the dispatcher's signal-aware parent context; they do not each install a
    separate signal handler. `rollups-backfill`, `fts-content-backfill`, and
    `reprice` acquire the lock before opening the writer and exit non-zero with a
    resolution-oriented message if the daemon lock is held. `check-parity`
    documents and tests its read-only no-lock behavior. The spec and tests must
    render the concrete subcommand matrix:
    - `check-parity`: read-only canonical DB handle, no daemon lock,
      signal-aware context cancellation.
    - `rollups-backfill`: writer handle, daemon lock required/refuse if held,
      signal-aware context cancellation.
    - `fts-content-backfill`: writer handle, daemon lock required/refuse if
      held, signal-aware context cancellation.
    - `reprice`: writer handle, daemon lock required/refuse if held,
      signal-aware context cancellation.

11. **Update deployment skill/runbook text.**
    Update `.agents/skills/project-deployment/SKILL.md` only if installer
    commands or operator restart checks change. Add operator-facing diagnostic
    notes for replay-required shutdown, prior forced-kill evidence, and
    migration restart loops without exposing private source paths.

### Validation Plan

- Spec checks:
  - `scripts/spec-drift.sh`
  - manual readback of `ingester.md`, `deployment.md`, `observability.md`,
    `presenter.md`, and `quality-gates.md`
- Required-addition mapping:
  - Real failure-mode proof: add a deterministic shutdown-under-load harness in
    `cmd/ai-viewer-ingest/main_test.go` or a dedicated command-level test that
    combines multiple buffered sources, slow/failing flush, in-flight backfill,
    resolver pressure, signal cancellation, and bounded process exit.
  - SQLite contention proof: add a real SQLite contention/cancellation test
    (for example `internal/ingest/worker_contention_test.go`) that forces
    `SQLITE_BUSY`/single-writer contention and proves shutdown exits within the
    documented worker bounds while reporting replay-required exactly once. The
    harness must distinguish idle-worker drain (15s expected clean path) from
    mid-flush drain (25s worst worker path) and must use the
    `workerRuntime.flush` seam or equivalent deterministic blocker so shutdown
    occurs while workers are inside `flushBatchWithWriteContext`. The mid-flush
    assertion must be a hard wall-clock check against
    `worker_exit_bound + scheduling_slack`, not a descriptive label; an observed
    second `busy_timeout` tail fails the test and forces a budget revision before
    implementation review. For scaled CI tests, `scheduling_slack` is
    `max(100ms, 10% of scaled worker_exit_bound)`; any full-duration
    production-constant proof must be isolated behind an explicit long-running
    mode or nightly gate so the per-push race suite stays practical. The
    per-push N=5 contention proof uses scaled seams; any production-constant N=5
    proof is long-running, and its measured wall-clock result is recorded in the
    SOW outcome if it is run before close.
  - Replay proof: add an end-to-end command-level replay-after-drain-expiry test
    for an aiagent_v3 fixture source, proving uncommitted events are re-emitted
    because source progress was not advanced. If an opencode read-only SQLite
    replay fixture is practical in this SOW, add it as a second production-shape
    proof; otherwise the adapter residual-risk matrix must explicitly state why
    aiagent_v3 is the committed replay proof and what remains for opencode.
  - Backfill/store-close proof: add a combined test proving shutdown cancels and
    waits for scoped backfill cleanup before explicit store close, or exits
    through the non-zero bounded guard without racing close under live SQLite
    work.
  - Resolver proof: add a resolver-loop shutdown test using
    `WithResolverInterval(50ms)` and the scaled shutdown timeout seam, proving
    the resolver goroutine observes cancellation and exits within
    `min(interval, remaining_caller_deadline + scheduling_slack)`. Add a
    final-resolver test proving the final resolver pass is capped by the caller's
    remaining deadline when less than 5s remains.
  - Forensics proof: assert captured logs include shutdown-start, clean-drain or
    replay-required, and bounded-guard/failure markers with no raw source paths
    or payloads.
  - Adapter proof: add per-adapter cancellation tests or an explicit
    adapter-by-adapter residual-risk matrix, including read-only DB handles and
    fsnotify watchers. The matrix must name the fixture or harness used for
    opencode's read-only SQLite handle/WAL watcher, or record a concrete residual
    risk if cancellation is not directly observable in a unit test.
- Focused Go tests before implementation:
  - `internal/ingest/worker_test.go`: expired shutdown context suppresses retry
    spam, logs one replay-required outcome, preserves source progress, and keeps
    normal non-shutdown retries.
  - `internal/ingest/ingester_test.go`: `StopContext` bounds worker wait via a
    goroutine/select on `ctx.Done()`, resolver timeout, caller-remaining-deadline
    resolver context use, worker-error aggregation, idempotent `Stop`, and final
    resolver context use. New tests exercise the bounded `StopContext` path;
    existing `Stop` tests continue to exercise the legacy wrapper under its new
    bounded default. Add a stuck-worker/slow-flush test proving `Stop()` itself
    returns within `ingesterStopTimeout` plus small scheduling margin. Add
    `TestIngester_StopContextBeforeStartReturnsErrNotStarted` and a concurrent
    `Stop()` / `StopContext()` idempotency test that proves follower
    `already_stopping` / `already_stopped` outcomes, including the case where the
    owning caller returns a timeout. Add
    `TestIngester_StopBeforeStartReturnsErrNotStarted` so the legacy wrapper's
    pre-start behavior is pinned separately from `StopContext`.
  - `internal/ingest/ingester_test.go`: multi-source `StopContext` test with
    N=5 workers contending on the single writer. Split it into idle-worker and
    mid-flush scenarios: idle workers must prove clean drain within the 15s
    worker bound; mid-flush workers must prove the 25s worst worker path and
    assert that excess uncommitted batches become replay-required or bounded
    timeout outcomes instead of extending shutdown. Both scenarios must assert
    the writer pool is single-connection (`SetMaxOpenConns(1)`) and wall-clock
    exit stays below the selected unit budget.
  - `cmd/ai-viewer-ingest/main_test.go`: early signal registration seam,
    source-start failure decrements `scanWG`, shutdown cancels/skips backfill,
    `runAdapter` unblocks on `ctx.Done()` while waiting for `backfillDone`,
    including the race where cancellation arrives while the goroutine is parked
    in the `backfillDone` select, explicit store-close logging, bounded-guard
    paths that do not run a deferred store close under live work, startup
    error-path logged close, and process exit-code classification. Add a
    partial-startup signal test proving SIGTERM/SIGINT after signal registration
    but before `ing.Start` completion closes an opened store, releases the flock,
    logs the early marker, exits within budget, and does not call `StopContext`
    on an unstarted ingester. Add a separate startup-store test proving a signal
    during `store.OpenWriter` / migration work cancels the signal-aware context
    and exits within the unit budget, or explicitly documents any remaining
    migration window as a bounded residual before implementation review.
  - `cmd/ai-viewer-ingest/main_test.go`: writer `Store.Close()` success, error,
    and timeout paths are logged and bounded by the 5s ingester close timer; a
    close timeout exits through the documented non-zero bounded guard without
    claiming clean drain.
  - `cmd/ai-viewer-ingest/main_test.go`: shutdown while the backfill goroutine is
    parked before `scanDone` proves the goroutine is the sole closer of
    `backfillDone` and `backfillWait`, no double-close panic occurs, adapters are
    released through `backfillDone`, and `backfillWait` closes within the scaled
    backfill budget.
  - `cmd/ai-viewer-ingest/backfill_test.go`,
    `backfill_fts_content_test.go`, and `reprice_test.go`: writer one-shots
    acquire/refuse daemon lock and respect signal context. Add a custom-DB
    default-state-dir test proving a daemon-held default state lock is refused
    even when the one-shot uses `--db` outside the default state directory.
  - `cmd/ai-viewer-ingest/check_parity_test.go`: read-only no-lock behavior is
    preserved and documented.
  - `cmd/ai-viewer-serve/main_test.go` / server tests: notify-poller wait is
    bounded before HTTP shutdown, notify-poller SQLite cancellation is proven or
    explicitly treated as residual risk, run-level signal registration happens
    before read-only store open/schema check, store-close errors and store-close
    timeouts are logged under the 5s serve close timer, double-close prevention
    is proven, and existing graceful shutdown order remains intact.
- Script/static tests:
  - Extend `scripts/test/systemd-units-test.sh` to lint both `deploy/systemd`
    and `deploy/systemd-system`; assert exact `TimeoutStopSec=45s`, no
    `Type=notify`, no watchdog directives, preserved restart/hardening/resource
    directives, and `OOMPolicy=stop` on the system ingester only. The static
    assertions must be per-variant, not a flat shared loop:
    - all four units: `Restart=on-failure`, `RestartSec=3s`,
      `TimeoutStopSec=45s`, and expected template `ExecStart` command, flags,
      and placeholder tokens.
    - system units only: explicit `Type=simple` and
      `WantedBy=multi-user.target`.
    - user units only: no explicit `Type=` directive; systemd's default simple
      behavior is the contract, with `WantedBy=default.target`.
    - system ingester: `After=network.target`,
      `ReadWritePaths=/opt/ai-viewer/data /opt/ai-viewer/logs`, and
      `OOMPolicy=stop`, `MemoryHigh=4G`, `MemoryMax=8G`,
      `IOSchedulingClass=idle`, and `LimitNOFILE=65536`.
    - system serve: `After=network.target ai-viewer-ingest.service`, no
      `ReadWritePaths` directive, and no `MemoryHigh`, `MemoryMax`,
      `IOSchedulingClass`, or `OOMPolicy` directives.
    - user ingester: `After=default.target` and no `MemoryHigh`, `MemoryMax`,
      or `OOMPolicy` directives.
    - user serve: `After=ai-viewer-ingest.service` and no `MemoryHigh`,
      `MemoryMax`, or `OOMPolicy` directives.
  - `scripts/test/install-system-test.sh`: rendered unit values, stop/chown/start
    ordering, ingester liveness poll before and after server readiness, server
    readiness check, rendered hardening preservation, token rendering for
    `__AI_VIEWER_SOURCES__`, `__OPERATOR_USER__`, and `__OPERATOR_GROUP__`,
    and failure diagnostics. Replace the existing `systemctl restart` assertions
    with stop-then-start assertions matching the new ordering, and update the
    `enable --now` negative-check rationale so it describes stop/start rather
    than restart. Assert the first-install stop path is guarded so missing units
    do not fail the script under `set -euo pipefail`.
- Focused command runs during implementation:
  - `go test -count=1 ./internal/ingest ./cmd/ai-viewer-ingest ./cmd/ai-viewer-serve`
  - `go test -race -count=1 ./internal/ingest ./cmd/ai-viewer-ingest ./cmd/ai-viewer-serve`
  - `scripts/test/systemd-units-test.sh`
  - `scripts/test/install-system-test.sh`
- Full gates before implementation review:
  - `./scripts/gates.sh`
  - `scripts/scan-secrets.sh`
  - `scripts/scan-ai-attribution.sh`

Artifact impact plan:

- Specs: `.agents/sow/specs/ingester.md`,
  `.agents/sow/specs/deployment.md`, `.agents/sow/specs/observability.md`,
  `.agents/sow/specs/presenter.md`, and
  `.agents/sow/specs/quality-gates.md`.
- Runtime skill: `.agents/skills/project-deployment/SKILL.md` if operator
  restart validation changes.
- Tests: `internal/ingest/worker_test.go`,
  `internal/ingest/ingester_test.go`, command-level ingest tests, and
  installer/systemd static tests for both system and user unit templates.
- End-user docs: only if restart behavior changes in operator-facing commands.

Open decisions:

- None for the user. This is technical reliability debt and should be handled
  autonomously once reviewer gates converge.

## Outcome

Pending.

## Reviews

### Gap Analysis Gate - Round 1

Outcome: `NEEDS WORK`.

Reviewer votes:

- `mimo`: `NOTHING MORE CAN BE DONE`.
- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.
- `kimi`: `NEEDS WORK`.
- `deepseek`: `NEEDS WORK`.
- `qwen`: `NEEDS WORK`.

Accepted findings added to this SOW:

- Post-scan read-model backfill is a shutdown path: it is launched outside the
  ingester wait group, runs with a `context.Background()`-derived 5-minute
  timeout, and adapters wait on `backfillDone` without a shutdown-context
  escape.
- Both system and user ingester units need an explicit stop-timeout contract.
- Static unit tests currently lint user units only; system-unit directive
  coverage must be added.
- The target `TimeoutStopSec` value must be derived from a written shutdown
  budget formula.
- SQLite `busy_timeout(5000)` and context cancellation behavior must be tested
  or explicitly budgeted.
- A combined command/integration shutdown test is required; isolated worker
  tests are not enough for the live failure mode.
- Shutdown observability must name distinct transient retry, drain-expiry,
  replay-required, terminal-drop, backfill, and resolver-timeout log outcomes.
- Serve shutdown is lower-risk but must be explicitly scoped in or out because
  the installer restarts both units.

Rejected findings:

- Claim: the system ingester unit lacks `RestartSec=3s`.
  Disposition: false positive. `deploy/systemd-system/ai-viewer-ingest.service`
  already contains `RestartSec=3s`.

### Gap Analysis Gate - Round 2

Outcome: `NEEDS WORK`.

Reviewer votes:

- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.
- `kimi`: `NEEDS WORK`.
- `mimo`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `deepseek`: `NOTHING MORE CAN BE DONE` with P2/P3 implementation-plan
  refinements, accepted as non-blocking because overlapping P1/P2 findings from
  other reviewers already require a SOW update.
- `qwen`: technical non-result; the process ended before a usable final vote
  was captured. Not retried in this round because accepted P1/P2 findings
  existed.

Accepted findings added to this SOW:

- Define the terminal drain-expiry exit-code contract and its interaction with
  `Restart=on-failure`.
- Treat backfill cancellation, adapters waiting on `backfillDone`, event-channel
  closure, and `Store.Close()` as one shutdown dependency chain.
- Account for both final resolver work and resolver-loop in-flight work during
  shutdown.
- Include `SetMaxOpenConns(1)` serialization in the stop-budget formula and
  tests.
- Anchor the stop budget to the observed systemd timeout class and an explicit
  safety margin.
- Decide whether serve units receive explicit `TimeoutStopSec` or remain scoped
  out with documented rationale.
- Add store-close error and close-latency behavior to shutdown observability and
  tests.
- Document current second-signal behavior and abandoned goroutine behavior.
- Extend static tests to cover rendered system units and, depending on the
  serve decision, serve unit timeout assertions.

Rejected or downgraded findings:

- Claim: `detachedWriteContext` goroutines are an immediate implementation
  blocker. Disposition: downgraded to budget/test evidence. The goroutine count
  is bounded by the number of active writes, but the shutdown plan must still
  account for it if tests show material latency or leak risk.
- Claim: pprof server shutdown is a blocking gap. Disposition: P3. The pprof
  server is off by default and process exit closes its listener, but the
  behavior is now included in the shutdown-contract documentation requirement.

### Gap Analysis Gate - Round 4

Outcome: `NEEDS WORK`.

Reviewer votes:

- `kimi`: `NOTHING MORE CAN BE DONE` with one P3 note.
- `mimo`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `deepseek`: `NOTHING MORE CAN BE DONE` with one P3 note.
- `qwen`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.

Accepted findings added to this SOW:

- Registering `signal.NotifyContext` after source startup leaves a startup
  signal window outside the intended graceful path. The gap analysis now
  requires either closing that window or documenting and testing it as
  replay-safe.
- Systemd `MemoryHigh`, `MemoryMax`, and idle-priority IO can interact with the
  shutdown budget. The budget, static tests, or residual-risk documentation must
  cover these resource controls.
- Shutdown forensics need an early structured marker plus terminal
  drain-expiry/replay-required evidence so a forced kill does not leave only
  retry warnings.
- One-shot ingest subcommands need an explicit signal/shutdown scope decision
  because they bypass the daemon signal path and use `context.Background()`-
  derived contexts.
- Deployment docs/specs need to state ingester-before-server restart ordering,
  UI/SSE staleness during ingester stop/start, and single-instance flock release
  timing as part of operator-visible restart latency.
- The worker drain contract must be stated as best-effort plus replay-safe, not
  synchronously lossless.
- Store-close budget evidence must explicitly include `sql.DB.Close`,
  single-connection writer behavior, and SQLite WAL/reader interactions.

Rejected, downgraded, or already-covered findings:

- Claim: pprof server shutdown blocks this SOW. Disposition: still P3. It is
  off by default, operator-gated, and process exit closes the listener; the
  contract can mention it without making it part of the restart-timeout fix.
- Claim: WAL checkpoint-on-close needs a separate top-level gap. Disposition:
  folded into the store-close latency gap rather than separate, because the
  required evidence is the same budget/latency proof.
- Claim: `runAdapter` panic recovery needs a test. Disposition: P3. The process
  has no goroutine-level recovery contract today; the shutdown SOW only needs
  to state whether panic exits are process-fatal or intentionally recovered.
- Claim: second-signal force termination is already true. Disposition: false as
  written. The SOW now states the current `signal.NotifyContext` behavior and
  requires an explicit design/test if second-signal fast exit is desired.

### Gap Analysis Gate - Round 5

Outcome: `NEEDS WORK`.

Reviewer votes:

- `kimi`: `NOTHING MORE CAN BE DONE`.
- `mimo`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `deepseek`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `qwen`: `NOTHING MORE CAN BE DONE`.
- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.

Accepted findings added to this SOW:

- The system installer validates server HTTP readiness after restart but does
  not validate that `ai-viewer-ingest.service` restarted cleanly. Reliable
  upgrade validation now requires a post-restart ingester-liveness check and
  installer self-test assertion.
- One-shot ingest subcommands bypass the daemon's single-instance flock while
  some open the canonical DB for writes. The subcommand scope decision now
  includes concurrent-writer/lock behavior, not only signal handling.
- The stop-budget formula must be serialization-aware. With `SetMaxOpenConns(1)`
  and per-worker drain contexts, the plan must decide and prove whether worker
  drain cost is summed across sources or effectively parallel.
- The local systemd default observed for this investigation is
  `DefaultTimeoutStopUSec=1min 30s`, but the implementation must choose an
  explicit repository-owned `TimeoutStopSec` and assert the exact rendered unit
  value in tests.
- The Go `signal.NotifyContext` contract is now cited as external evidence for
  second-signal behavior.
- Adapter resource proof must explicitly include adapters that own read-only DB
  handles or fsnotify watchers, including opencode's read-only SQLite handle and
  WAL watcher.
- Shutdown observability specs must name exact structured log fields for
  clean-drain, replay-required, and forced-kill diagnostic paths.
- Deployment/systemd specs must decide drain-expiry exit classification
  (`Restart=on-failure`, optional `SuccessExitStatus=`) and memory-kill
  classification (`OOMPolicy` or documented default).
- Store-close/WAL specs must decide whether shutdown runs a final checkpoint/
  truncate, distinguish process-kill crash safety from power-loss safety, and
  document WAL-growth implications when the server keeps a reader open.

Rejected, downgraded, or already-covered findings:

- Claim: the gap analysis must specify the exact code fix for the `scanWG` leak.
  Disposition: too detailed for gap analysis, but the causal chain is now
  explicit. The exact fix belongs in the implementation plan gate.
- Claim: the SOW must choose a concrete `TimeoutStopSec` value during gap
  analysis. Disposition: premature. The gap now requires the implementation
  plan to choose an exact value from the serialized budget before specs/tests/
  code land.
- Claim: pprof in-flight requests are a P2 restart input. Disposition: P3. The
  endpoint is off by default and operator-gated; it remains documented as
  out-of-critical-path unless the implementation plan deliberately scopes it in.
- Claim: test isolation needs a separate top-level gap. Disposition: already
  covered by the shutdown-under-load harness and project test discipline; the
  implementation plan will name temp DB/state-dir fixtures.

### Gap Analysis Gate - Round 6

Outcome: `NEEDS WORK`.

Reviewer votes:

- `kimi`: `NOTHING MORE CAN BE DONE`.
- `mimo`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `deepseek`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `qwen`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.

Accepted findings added to this SOW:

- Serve shutdown was corrected from "bounded" to the actual contract:
  `http.Server.Shutdown` has a 30 s bound, but the notify-poller wait that
  precedes it has no independent timeout. The serve in/out decision must now be
  made from that accurate property.
- The deployment plan must consider or reject `Type=notify`, `sd_notify`
  readiness/stopping/watchdog messages, `NotifyAccess=`, and `WatchdogSec`.
- The shutdown-budget proof must include flush retry count, retry backoff,
  SQLite `busy_timeout(5000)`, and driver context-cancellation behavior.
- One-shot subcommands need a concrete matrix for DB access mode, write/read
  requirement, lock/refuse behavior, and signal/shutdown behavior.
- Installer ownership repair currently runs before service restart while the
  old ingester may still hold DB/WAL/SHM/log/lock handles. The plan must choose
  safe ordering or document why live same-user repair is safe.
- Deployment specs/runbook must state whether previous forced-kill detection is
  an installer responsibility or journal/runbook evidence only.
- Deployment specs must state whether default `KillMode`, `KillSignal`, and
  `FinalKillSignal` behavior is intentional.
- The ingester spec rewrite must fix the duplicate step number in the graceful
  shutdown sequence.
- Migration failure under `Restart=on-failure` must be documented or classified
  if startup readiness changes touch that path.

Rejected, downgraded, or already-covered findings:

- Claim: test isolation needs a separate top-level gap. Disposition: still P3.
  Existing project test discipline plus the plan-stage validation matrix will
  require temp DB/state-dir fixtures for shutdown tests.
- Claim: pprof cleanup is a blocking restart gap. Disposition: P3. The endpoint
  is off by default and process exit reaps the listener; the ingester spec will
  record that it is outside the shutdown wait set unless a future SOW scopes it
  in.

### Gap Analysis Gate - Round 7

Outcome: `NOTHING MORE CAN BE DONE`.

Reviewer votes:

- `glm`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `minimax`: `NOTHING MORE CAN BE DONE` with P3-only notes.
- `kimi`: `NOTHING MORE CAN BE DONE`.
- `mimo`: `NOTHING MORE CAN BE DONE`.
- `deepseek`: `NOTHING MORE CAN BE DONE`.
- `qwen`: `NOTHING MORE CAN BE DONE` with P3-only notes.

P3-only observations recorded for planning awareness:

- If the final stop budget depends on summing per-source worker drains, the
  deployment spec should state the source-count assumption or the rule for
  re-deriving `TimeoutStopSec` when source count grows.
- The implementation-plan budget proof should be explicit about fixture
  measurement limits versus production-scale shutdown duration.
- The migration-failure runbook may name `StartLimitBurst` /
  `StartLimitIntervalSec` as the systemd mechanism behind restart-loop
  diagnosis, but it must describe inspection of effective host values instead
  of hard-coding cross-distro defaults.
- Serve's read-only store close error handling and startup signal window are
  low-risk but should be considered under the serve in/out decision.
- A `--shutdown-timeout` CLI flag is not part of this SOW unless the
  implementation plan proves operator configurability is necessary.

### Plan Review Gate - Round 1

Outcome: `NEEDS WORK`.

Reviewer votes:

- `kimi`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with one P3 note.
- `glm`: `NEEDS WORK`.
- `minimax`: `NEEDS WORK`.
- `deepseek`: `NEEDS WORK`.
- `qwen`: technical timeout/no usable final vote. Not retried in this round
  because accepted P1/P2 findings existed.

Accepted findings folded into the implementation plan:

- Budget arithmetic now documents why `SetMaxOpenConns(1)` serialization affects
  commit success and replay-required outcomes, not the process exit bound.
  `worker_exit_bound` is now 15s (`shutdownDrainTimeout=10s` plus one
  `busy_timeout(5000)` tail), with an explicit source-count assumption.
- `StopContext` integration is now explicit: production callers use a bounded
  `ingesterStopTimeout=30s`, and the `Stop()` compatibility wrapper must also
  use a bounded default instead of `context.Background()`.
- Backfill shutdown now has an owner and wait contract: cancel the backfill
  context, wait for the goroutine before explicit store close, and exit through
  the non-zero bounded guard if it cannot stop safely.
- Validation now includes a required-addition mapping for the failure-mode
  harness, real SQLite contention proof, replay-after-drain-expiry proof,
  backfill/store-close race proof, resolver in-flight proof, forensics log
  proof, and adapter cancellation/resource proof.
- Static unit validation now explicitly extends `scripts/test/systemd-units-test.sh`
  to both `deploy/systemd` and `deploy/systemd-system`, and installer tests must
  assert rendered system-unit values and hardening preservation.
- Installer ingester liveness validation now uses a settle/poll window and a
  second check after server readiness so immediate startup failures cannot pass
  through a single early `is-active`.
- Serve notify-poller validation now requires either SQLite-cancellation proof
  or an explicit residual-risk note within the 45s serve unit budget.
- The system ingester unit now gets an explicit `OOMPolicy=stop` decision.

P3 notes folded or deferred:

- The spec delta now records the adapter-owned resource decision: no adapter
  `Close()` hook in this SOW; resources are released when adapter goroutines
  return or by process exit.
- Deployment spec work now includes process-kill versus power-loss safety,
  no final shutdown checkpoint/truncate, WAL growth with a live server reader,
  and `StartLimitBurst` / `StartLimitIntervalSec` restart-loop diagnosis.
- `e2e-serve.sh` bounded wait is a non-blocking test-script robustness item; if
  touched during implementation it should be fixed, otherwise file a follow-up
  SOW only if it becomes a real local hang risk.

### Plan Review Gate - Round 2

Outcome: `NEEDS WORK`.

Reviewer votes:

- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `deepseek`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `glm`: `READY FOR IMPLEMENTATION` but listed two P2 clarifications, treated
  as blocking until folded in.
- `minimax`: `NEEDS WORK`.
- `kimi`: `NEEDS WORK`.

Accepted findings folded into the implementation plan:

- The timeout budget is now expressed as a phase-based path:
  `max(adapter_grace=5s, ingesterStopTimeout=30s) + backfill_cancel_wait=5s +
  store_close=5s = 40s`, with `TimeoutStopSec=45s` leaving 5s unit-level
  margin. The internal `ingesterStopTimeout` margin is no longer double-counted
  as external systemd margin.
- The single-writer worker-drain claim now states why the expected wall-clock is
  max-like, not a sum across sources, and requires budget/spec revision if the
  N=5 validation test disproves the 45s unit bound.
- `Stop()` now has an exact compatibility default: it delegates to
  `StopContext` with `ingesterStopTimeout=30s`.
- The main shutdown path now maps typed outcomes to exit codes explicitly:
  clean-drain and replay-required return 0; timeout, store-close failure,
  permanent worker failure, and bounded-guard failure return 1.
- Migration-failure restart loops are explicitly documented as using systemd /
  distribution `StartLimit*` defaults in this SOW; adding a custom circuit
  breaker is out of scope.
- `scripts/install-systemd-user.sh` is explicitly out of scope for liveness
  validation because it does not auto-start units, while the copied user unit
  templates are covered by static tests.
- The validation plan now states which tests exercise `StopContext` versus the
  legacy `Stop` wrapper, WAL growth is a documented operator-observable
  residual risk, pprof is intentionally outside the shutdown wait group, and
  `OOMPolicy=stop` applies only to the system ingester.
- Static unit and installer tests now name key directive and token-rendering
  assertions instead of relying on broad "preserve hardening" wording.

P3 notes folded or deferred:

- Second-signal fast exit bypasses explicit store close by design; the ingester
  spec must pair that with process-kill/WAL replay boundaries.
- Serve notify-poller stuck-close behavior can remain a documented residual
  risk if the cancellation proof is not straightforward, because serve is
  read-only and the 45s unit timeout bounds process lifetime.
- `check-parity` remains read-only against the canonical DB; implementation
  docs/specs may include a one-line justification when the subcommand matrix is
  written.

### Plan Review Gate - Round 3

Outcome: `NEEDS WORK`.

Reviewer votes:

- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `minimax`: `READY FOR IMPLEMENTATION`, but listed P2 tightening items that
  were accepted and folded in.
- `deepseek`: `READY FOR IMPLEMENTATION`, but listed P2 clarifications that
  were accepted and folded in.
- `kimi`: `NEEDS WORK`.
- `glm`: `NEEDS WORK`.

Accepted findings folded into the implementation plan:

- The worker-exit budget now distinguishes idle-worker drain (15s) from the
  worst mid-flush path (25s) caused by the separate detached active-write
  context before shutdown drain. The plan no longer claims a 10s internal
  `StopContext` margin that does not exist in the mid-flush case.
- Worst-case multi-source mid-flush shutdown may return a bounded timeout /
  replay-required outcome instead of clean drain; that is acceptable for the
  no-SIGKILL safety goal, while the idle-worker drain case must still prove a
  clean exit.
- The N=5 validation plan now splits idle-worker and mid-flush scenarios and
  requires the mid-flush scenario to place workers inside
  `flushBatchWithWriteContext` through the existing flush seam or equivalent
  deterministic blocker.
- `StopContext` must wait for `wg.Wait()` through a goroutine plus
  `select` on `ctx.Done()`, returning a timeout typed outcome if the caller
  deadline expires.
- The final resolver pass must use `min(remaining caller deadline, 5s)`, not a
  fresh unconditional 5s window, so `StopContext` cannot exceed its caller's
  deadline.
- The legacy `Stop()` wrapper keeps its `error` return and maps clean-drain /
  replay-required outcomes to nil while mapping timeout and failure outcomes to
  non-nil errors.
- Timeout constants are now assigned to their owning packages instead of
  creating serve-to-ingest package coupling.
- Removing the current deferred store close now includes error-path cleanup
  requirements and a conditional/explicit close rule so bounded-guard paths do
  not close SQLite under live backfill or workers.
- Tests now explicitly cover `Stop()` delegation, caller-deadline resolver
  bounding, backfillDone cancellation while parked in the select, store-close
  duration/guard behavior, and single-writer N=5 contention evidence.

Rejected or downgraded findings:

- Request to make pprof part of graceful shutdown was still treated as a
  documented residual, not a code requirement. The pprof server is off by
  default and operator-gated; the spec must state that `StopContext` does not
  wait for in-flight pprof handlers.
- Request to add custom `StartLimitBurst` / `StartLimitIntervalSec` directives
  remains out of scope. This SOW documents systemd/distribution defaults and
  leaves custom circuit breakers to a later deployment-policy SOW.

### Plan Review Gate - Round 4

Outcome: `NEEDS WORK`.

Reviewer votes:

- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `deepseek`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `minimax`: `READY FOR IMPLEMENTATION`, but listed P2 clarifications that were
  accepted and folded in.
- `kimi`: `READY FOR IMPLEMENTATION`, but listed P2 clarifications that were
  accepted and folded in.
- `glm`: `NEEDS WORK`.

Accepted findings folded into the implementation plan:

- `StopContext` now explicitly preserves the legacy `ErrNotStarted` pre-start
  behavior before entering the typed shutdown outcome path, and validation
  includes a dedicated pre-start test.
- `StopContext` now explicitly preserves the existing idempotency guard for
  concurrent `Stop()` / `StopContext()` callers.
- Signal registration is now placed immediately after logger/config setup and
  before store open, `ing.Start`, backfill, source scanning, or other long-running
  DB/source work. Validation includes a partial-startup signal test that closes
  opened resources, releases the flock, logs the early marker, exits within
  budget, and avoids calling `StopContext` on an unstarted ingester.
- Adapter wait is now explicitly parallel with `StopContext`; no sequential
  post-`StopContext` adapter wait may extend shutdown.
- `startPostScanBackfill` now has a concrete wait contract:
  `backfillWait <-chan struct{}` closes when the goroutine exits, and shutdown
  selects on it with the 5s backfill timer.
- Serve store close is now explicitly enforced with a 5s goroutine/timer pattern,
  including timeout/error logging.
- The worker-budget invariant now explains why the fresh drain does not add a
  second `busy_timeout` tail: one daemon writer connection, read-only WAL serve
  access, and lock-refusing writer one-shots. The mid-flush test must hard-assert
  wall-clock exit within `worker_exit_bound + scheduling_slack`.
- Static unit tests now use an explicit per-variant assertion matrix for system
  and user units, including `Type=`, `After=`, `WantedBy=`, `ReadWritePaths`,
  `OOMPolicy`, `TimeoutStopSec`, restart policy, and `ExecStart` expectations.
- One-shot commands now derive from the dispatcher's signal-aware parent context
  and do not each register their own signal handler.
- The deployment spec/runbook must note that a bounded-guard exit 1 during
  operator stop/restart can leave the previous unit result visible in
  `systemctl status` as forensic evidence.

Rejected or downgraded findings:

- Request to make `detachedWriteContext` helper goroutines part of the wait group
  remains P3/documentation. The helper count is bounded by active writes and the
  helper exits within the shutdown-drain timeout or is reaped by process exit;
  the ingester spec will record that residual behavior.
- pprof shutdown remains a documented residual, not a runtime requirement. The
  pprof server is off by default and operator-gated; `StopContext` does not wait
  for in-flight pprof handlers.

### Plan Review Gate - Round 5

Outcome: `NEEDS WORK`.

Reviewer votes:

- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `deepseek`: `READY FOR IMPLEMENTATION`, but listed one P2 finding that was
  accepted and folded in.
- `glm`: `NEEDS WORK`.
- `kimi`: technical non-result; command exited without a usable vote or review
  body. Not retried in this round because accepted P2 findings existed.
- `minimax`: technical malformed/truncated result; command output did not
  provide a usable final vote. Not retried in this round because accepted P2
  findings existed.

Accepted findings folded into the implementation plan:

- Ingester writer `Store.Close()` now has the same explicit bounded enforcement
  as serve: run close in a goroutine, select against the 5s timer, log error or
  timeout, and exit through the documented non-zero bounded guard on timeout.
- Startup signal validation now includes the `store.OpenWriter` / migration
  window. The plan requires passing the signal-aware context into `OpenWriter`
  so migrations and schema repair observe cancellation through their existing
  context-aware SQL calls, or documenting any remaining migration window as a
  bounded residual before implementation review.
- Concurrent `Stop()` / `StopContext()` semantics now specify follower outcomes:
  typed callers receive `already_stopping` or `already_stopped`; the legacy
  `Stop()` wrapper maps those follower outcomes to `nil`.
- Backfill channel ownership is now single-owner: the backfill goroutine closes
  both `backfillDone` and `backfillWait`; shutdown cancels the context and never
  closes those channels directly.
- Shutdown wall-clock tests now require a test-only timeout seam so the per-push
  suite can prove the budget ratios with scaled millisecond values while keeping
  production constants pinned. Any full-duration production-constant proof must
  live in an explicit long-running or nightly mode.
- Static unit tests now also assert the absence of `ReadWritePaths` on the system
  serve unit.
- The quality-gates spec delta is unconditional because this SOW expands the
  coverage of an existing static gate even if no new gate script is added.

Rejected or downgraded findings:

- The request to make `e2e-serve.sh` bounded remains P3/out of scope unless the
  implementation touches that helper. The SOW already treats it as a follow-up
  only if it becomes a real local hang risk.
- Source-count runtime warning remains P3. The deployment spec must state the
  default/operator source-count assumption and re-derivation rule; adding a new
  runtime warning is optional unless implementation evidence shows operators
  cannot observe the assumption otherwise.

### Plan Review Gate - Round 6

Outcome: `NEEDS WORK`.

Reviewer votes:

- `qwen`: `READY FOR IMPLEMENTATION`.
- `mimo`: `READY FOR IMPLEMENTATION`.
- `kimi`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `glm`: `NEEDS WORK` with one P2 finding.
- `deepseek`: `NEEDS WORK` with five P2 findings.
- `minimax`: `READY FOR IMPLEMENTATION`, but listed P2 caveats; real caveats
  were accepted and folded in under the long-term-best rule.

Accepted findings folded into the implementation plan:

- Serve is now scoped consistently with the ingester: run-level signal
  registration happens before read-only store open/schema check, and
  `presenter.md` becomes a spec delta for serve shutdown behavior.
- Ingest subcommands now receive the signal-aware context explicitly:
  `run` creates the signal context before `dispatchSubcommand`, and the
  dispatcher receives the context.
- The replay proof names aiagent_v3 as the required end-to-end command-level
  adapter/source proof, with opencode either added if practical or documented in
  the residual-risk matrix.
- Ingester close handling now has a close strategy matrix for before-open,
  post-open/pre-start, post-start/pre-source, normal shutdown, bounded guard, and
  ordinary startup error paths.
- Serve bounded close now requires double-close prevention by making
  `runtime.close` idempotent or disabling the deferred close after the bounded
  close path starts.
- The existing `scripts/test/install-system-test.sh` `systemctl restart`
  assertions must be replaced with stop-then-start assertions, and the stale
  `enable --now` rationale must be updated.
- `scheduling_slack` is now defined for scaled tests as
  `max(100ms, 10% of scaled worker_exit_bound)`.
- Resolver-loop validation now uses `WithResolverInterval(50ms)` plus the scaled
  timeout seam to prove cancellation and exit bounds.
- Adapter cancellation validation now requires a named opencode fixture/harness
  or an explicit residual-risk entry for opencode's read-only SQLite handle and
  WAL watcher.
- `StopContext` API shape is now concrete: exported `ShutdownOutcome` enum,
  `ShutdownResult` struct, and sentinel errors for failure/replay classes.
- Test-only timeout seams are now constrained to unexported package/test hooks
  restored with `t.Cleanup`, so scaled test knobs do not become production API.
- The ingester spec delta now records that `runAdapter` panic recovery is out of
  scope and panics remain process-fatal.
- Installer ingester liveness polling now has exact parameters: every 1s for up
  to 15 attempts.

Rejected or downgraded findings:

- The request to make a runtime warning mandatory when configured sources exceed
  the default/operator source-count assumption remains P3. The deployment spec
  will record the assumption and re-derivation rule; implementation can add a
  warning if it is cheap and useful, but the plan does not depend on it.
- `e2e-serve.sh` bounded waiting remains outside this SOW unless the
  implementation touches that helper.

### Plan Review Gate - Round 7

Outcome: `NEEDS WORK`.

Reviewer votes:

- `deepseek`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `glm`: `NEEDS WORK` with one P2 finding.
- `minimax`: `NEEDS WORK` with one P2 finding and P3 notes.
- `kimi`: technical non-result; command exited without a usable vote or review
  body. Not retried in this round because accepted P2 findings existed.

Accepted findings folded into the implementation plan:

- Writer one-shot `--state-dir` default is corrected to use the same
  `resolveStateDir` logic as the daemon, not `filepath.Dir(db)`. A custom
  `--db` path with default state dir must still probe the daemon's lockfile and
  refuse if the daemon is running.
- Static systemd tests now pin the resource directives that the shutdown budget
  depends on: system ingester `MemoryHigh=4G`, `MemoryMax=8G`,
  `IOSchedulingClass=idle`, and `LimitNOFILE=65536`, plus explicit absence of
  irrelevant memory/OOM/IO directives on serve and user units.
- Static unit-template checks now distinguish template command/token assertions
  from rendered installer-value assertions.
- The timeout-budget prose now describes the SQLite busy tail as a conservative
  post-deadline tail under `flushMaxRetries=3`; the real contention test remains
  the authoritative proof and forces a budget revision if observed wall-clock
  behavior exceeds the bound.
- Migration restart-loop guidance now requires inspecting effective
  `StartLimit*` values with `systemctl show` instead of asserting a portable
  distribution default.
- The observability spec delta now pins shutdown-start marker timing to the
  synchronous signal-observer path.
- The implementation plan now states that `store.OpenWriter` already accepts a
  context; this SOW changes call-site context propagation, not the store API
  shape.
- The `scanWG` fix now names the `wgAdded` plus `defer` single-exit pattern.
- The validation plan now adds a legacy `Stop()` pre-start test and a
  first-install guarded-stop installer assertion.

Rejected or downgraded findings:

- Adding a mandatory production-constant nightly shutdown-budget CI workflow is
  P3. The SOW keeps long-running proof explicit and allows either a named
  long-running local run recorded in the outcome or a future nightly gate.
- Adding a runtime warning for source-count assumptions remains P3 for the same
  reason recorded in round 6.

### Plan Review Gate - Round 8

Outcome: converged. The implementation plan is ready for spec updates, then
tests, then runtime implementation.

Reviewer votes:

- `deepseek`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `glm`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `kimi`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `mimo`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `minimax`: `READY FOR IMPLEMENTATION` with P3-only notes.
- `qwen`: `READY FOR IMPLEMENTATION` with P3-only notes.

P3 notes folded or recorded:

- The one-shot lock-key residual for system installs is now explicit: the lock
  is keyed on `--state-dir`, so one-shots targeting `/opt/ai-viewer/data/index.db`
  must pass `--state-dir /opt/ai-viewer/data` to detect the system daemon.
- The `StopContext` follower-state distinction now requires an explicit
  in-progress/completed mechanism instead of relying on the current single
  `stopped` boolean.
- The `scanWG` fix now forbids calling `WaitGroup.Add` while a wait goroutine is
  already observing a zero counter, and it allows either preserving the
  caller-side pre-add or moving Wait after all per-source Adds.
- Serve close idempotency now names an explicit closed flag or nil-guard as
  acceptable mechanisms.
- The validation plan now states that per-push N=5 contention proof uses scaled
  seams, while production-constant N=5 proof is long-running and recorded in the
  SOW outcome only if run before close.
- Source-count runtime logging/warning, `e2e-serve.sh` bounded waiting, and a
  mandatory nightly workflow remain P3 and non-blocking.

## Implementation Evidence - 2026-06-26

Implemented scope:

- Specs updated first:
  - `.agents/sow/specs/ingester.md`: bounded `StopContext`, replay-required
    drain semantics, shutdown outcome classes, pprof residual, one-shot lock
    behavior, and graceful-shutdown budget.
  - `.agents/sow/specs/deployment.md`: stop/chown/start install order,
    ingester liveness polling, `TimeoutStopSec=45s`, system resource
    directives, `OOMPolicy=stop`, one-shot `--state-dir` behavior, and
    `StartLimit*` diagnostic guidance.
  - `.agents/sow/specs/observability.md`: structured shutdown markers and
    fields.
  - `.agents/sow/specs/presenter.md`: serve signal/shutdown/store-close
    behavior.
  - `.agents/sow/specs/quality-gates.md`: expanded systemd and installer static
    gate contracts.
  - `.agents/skills/project-deployment/SKILL.md`: operator runbook aligned with
    the new install and shutdown behavior.
- Tests added or hardened before runtime changes:
  - Ingester `StopContext` pre-start, timeout, and concurrent follower-state
    tests.
  - Worker replay-required tests proving expired shutdown context preserves
    batch/source progress and reports one replay-required error.
  - Ingest command tests for pre-added `scanWG` failure cleanup, post-scan
    backfill shutdown channels, one-shot default `--state-dir` lock refusal, and
    subcommand state-dir parsing.
  - Serve tests for signal-aware startup, bounded notify-poller wait, bounded
    store close, and idempotent runtime close.
  - Installer/systemd tests pin stop-before-chown-before-start, ingester
    liveness polling, first-install guarded stop, resource directives,
    `TimeoutStopSec=45s`, no notify/watchdog, and absence of wrong-unit
    directives.
  - Finite-scan E2E harnesses were hardened to use unbuffered channels and
    per-event flushing for deterministic race/coverage behavior; unrelated
    fixed-timeout tests that failed only under whole-suite race load were
    widened without changing product behavior.
- Runtime changes:
  - `ai-viewer-ingest` creates the top-level signal context before subcommand
    dispatch and writer-store open, passes it through one-shot commands, and
    uses bounded `StopContext`, bounded backfill wait, and bounded writer-store
    close.
  - `ai-viewer-ingest` writer one-shots acquire the same state-dir daemon lock
    before opening the writer DB; default state dir mirrors daemon
    `resolveStateDir`, including custom `--db` cases.
  - `internal/ingest` now exposes typed shutdown outcomes and replay-required
    semantics; worker shutdown preserves uncommitted batches when the drain
    deadline expires.
  - Post-scan backfill channel ownership is single-owner and shutdown-safe.
  - Source startup failure decrements the pre-added scan wait group before
    returning.
  - `ai-viewer-serve` creates its signal context before reader-store open,
    drains notify poller/SSE/HTTP in order, closes the store with a bounded
    guard, and makes runtime close idempotent.
  - Installer uses explicit active-unit stop, ownership repair, ingester start
    and liveness verification, server start, and post-server ingester
    verification instead of `systemctl restart`.
  - User and system unit templates set `TimeoutStopSec=45s`; system ingester
    carries the pinned resource/OOM directives.

Local validation completed:

- `go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...`
  via `scripts/test.sh`: passed.
- Frontend Vitest coverage via `scripts/test.sh`: passed, 929 tests.
- `scripts/check-coverage.sh`: passed; gated aggregate 85.4%, every gated
  package >= 80%.
- `scripts/lint.sh`: passed after implementation fixes; includes gofmt,
  goimports, vet, golangci-lint, gosec, govulncheck, eslint, TypeScript,
  bundle-size self-test, and frontend coverage-gate self-tests.
- `scripts/build.sh`: passed; frontend build, bundle-size gate, embedded assets,
  and both Go binaries.
- `scripts/check-ingestion-parity.sh --fixtures`: passed.
- `go test -run='^Fuzz' ./internal/adapters/...`: passed.
- `scripts/spec-drift.sh`: passed.
- `scripts/test/systemd-units-test.sh`: passed.
- `scripts/test/install-system-test.sh`: passed.
- `git diff --check`: passed.
- `bash -n scripts/install-system.sh scripts/test/install-system-test.sh
  scripts/test/systemd-units-test.sh`: passed.
- `scripts/scan-ai-attribution.sh`: passed.
- `scripts/scan-secrets.sh`: passed; existing null-byte warnings came from
  tracked binary/fixture content and the scanner reported no secrets or
  operator PII.
- `scripts/test/check-bench-test.sh`: passed.

Pending validation:

- `scripts/check-bench.sh` actual benchmark gate is not yet valid evidence:
  it refused to run twice on 2026-06-26 because the workstation was busy:
  first `loadavg 1m=29.74`, then `loadavg 1m=24.70`, threshold `12.00`.
  Retry in a quieter window before closing the SOW.

### Implementation Review Gate - Round 1

Outcome: `NEEDS WORK`.

Accepted findings:

- Adapter wait was sequential before `StopContext`, violating the budget model
  `max(adapter_grace=5s, ingesterStopTimeout=30s)`. Fixed by starting adapter
  wait in a goroutine before `StopContext` and joining it after `StopContext`.
- Serve registered its signal context before opening the store but did not call
  the returned `stop()` function on the first signal. Fixed by passing the
  release function into `serveHTTP` and calling it immediately on `ctx.Done()`.
- Worker shutdown classified any joined error containing `ErrReplayRequired` as
  replay-required, which could mask permanent worker drops. Fixed by
  classifying replay-required only when every recorded worker error wraps
  `ErrReplayRequired`.
- Structured shutdown marker names in `observability.md` were not reflected in
  the implementation. Fixed for shutdown start, adapter grace expiry, retry
  suppression, replay-required, backfill cancellation/timeout, resolver timeout,
  store-close error/timeout, clean shutdown, and bounded-guard exit.
- The finite-scan E2E helper still used a 10 s timeout after the harness changed
  to per-event flushing and unbuffered channels. Fixed by widening
  `waitForScan` to 20 s; whole-package `internal/ingest` race tests now pass.
- Missing validation tests were real. Added tests for parallel adapter/ingester
  shutdown timing, writer-store close error/timeout, replay-only vs mixed
  worker-error classification, five-source clean drain, resolver deadline/loop
  shutdown, and real SQLite write contention followed by replay from unadvanced
  source progress.

Rejected or downgraded findings:

- A production-constant 25 s/N=5 wall-clock contention proof remains too slow
  for the per-push gate and is still treated as long-running validation. The
  new per-push tests use scaled deadlines and a real SQLite contention path with
  a short test-only busy timeout.
- Per-adapter cancellation matrix remains documented as a residual-risk class
  rather than fully tested in this patch. The runtime `runAdapter` shutdown
  escape and the existing adapter tests cover the changed shared path; deeper
  adapter-specific cancellation proofs can be split into a follow-up SOW if
  reviewers continue to require them for closure.

### Reviewer-Fix Evidence - 2026-06-26

Additional validation after round-1 fixes:

- `go test -count=1 ./cmd/ai-viewer-ingest ./cmd/ai-viewer-serve
  ./internal/ingest`: passed.
- Focused race run for the previously failing E2E cases plus the new shutdown
  tests: passed.
- `go test -race -count=1 ./internal/ingest/...`: passed in 45.734 s.
- `go test -race -count=1 ./cmd/ai-viewer-ingest ./cmd/ai-viewer-serve`:
  initially found a race in the test close hook, then passed after the hook was
  mutex-protected and rerun.
- `go test -count=1 ./...`: passed.
- `scripts/test.sh`: passed; Go race/coverage and frontend Vitest coverage.
- `scripts/check-coverage.sh`: passed; gated aggregate 85.4%, every gated
  package >= 80%.
- `scripts/test/systemd-units-test.sh`: passed.
- `scripts/test/install-system-test.sh`: passed.
- `bash -n scripts/install-system.sh scripts/test/install-system-test.sh
  scripts/test/systemd-units-test.sh`: passed.
- `scripts/lint.sh`: passed.
- `scripts/build.sh`: passed.
- `scripts/spec-drift.sh`: passed.
- `scripts/check-ingestion-parity.sh --fixtures`: passed.
- `go test -run='^Fuzz' ./internal/adapters/...`: passed.
- `scripts/scan-ai-attribution.sh`: passed.
- `scripts/scan-secrets.sh`: passed; existing null-byte fixture warnings only,
  final scanner result clean.
- `git diff --check`: passed.

Pending validation:

- External implementation-review rerun is pending after these fixes.
- `scripts/check-bench.sh` still cannot produce valid benchmark evidence while
  host load remains above threshold.

### Implementation Review Gate - Round 2

Outcome: `NEEDS WORK`.

Reviewer votes:

- `glm`: `NEEDS WORK`.
- `kimi`: `PRODUCTION GRADE`.
- `mimo`: `PRODUCTION GRADE`.
- `qwen`: `PRODUCTION GRADE`.
- `deepseek`: `PRODUCTION GRADE`.
- `minimax`: timed out before final vote, but independently confirmed the
  startup-close validation gap while reviewing.

Accepted findings:

- Missing shutdown-forensics validation was real. The SOW validation plan
  required a test proving early shutdown-state markers and terminal
  replay-required/bounded-guard markers. Runtime markers existed, but no test
  pinned marker names or field shape. Fixed with
  `TestIngestShutdownForensicsMarkers` and
  `TestRecordWorkerErrorLogsReplayRequiredFields`.
- Missing partial-startup signal validation was real. The code registered the
  signal context before writer open, but the post-open/pre-start error path
  still used the old unbounded deferred `Store.Close()`. Fixed by using the
  bounded writer close in the deferred startup/error path, adding
  `TestRun_StoreOpenReceivesCanceledSignalContext`, and adding
  `TestRun_PartialStartupSignalUsesBoundedCloseAndReleasesLock`.
- `shutdown_replay_required` field drift was real. `pending_events` and
  `reason` were embedded in the error string instead of structured fields.
  Fixed by introducing an internal replay-required error carrying structured
  reason and pending-event count.
- Serve runtime close idempotency existed in code but was not pinned by a test.
  Fixed with `TestServeRuntimeCloseIsIdempotent`.

Rejected or downgraded findings:

- Aggregate adapter-wait timeout cannot honestly report per-source
  `source_id`/`source_format` without new active-adapter ownership tracking.
  The observability spec was corrected to define
  `shutdown_adapter_grace_expired` as an aggregate marker with `elapsed_ms` and
  `grace_ms`.
- Shutdown markers must not add separate raw `location` or payload fields. The
  spec now states that `source_id` is the existing canonical diagnostic key and
  forbids additional raw location/payload fields on shutdown markers.

### Reviewer-Fix Evidence - Round 2 - 2026-06-26

Code/spec fixes:

- Startup/error deferred writer-store close now uses the same 5 s
  `closeStoreWithTimeout` path as normal shutdown.
- A signal-canceled startup path logs `shutdown_start` before returning from
  writer-open or pre-start errors.
- Writer-store open and ingester start are test-seamed without changing
  production behavior, so cancellation and partial-startup close behavior can
  be verified deterministically.
- Shutdown marker fields now include elapsed timing for adapter grace expiry,
  backfill timeout/cancel, writer/reader store close error/timeout, and serve
  clean shutdown.
- `shutdown_replay_required` now exposes structured `pending_events` and
  `reason`.
- Serve logs terminal `shutdown_clean` from the HTTP shutdown path that knows
  the elapsed duration.
- `.agents/sow/specs/observability.md` was aligned to the marker field
  contract and aggregate adapter wait behavior.

Additional validation after round-2 fixes:

- Focused race tests for new validation points:
  `go test -race -count=1 -timeout=120s -run
  'TestIngestShutdownForensicsMarkers|TestRun_StoreOpenReceivesCanceledSignalContext|TestRun_PartialStartupSignalUsesBoundedCloseAndReleasesLock|TestShutdownIngestRuntimeWaitsAdaptersInParallelWithStopContext|TestCloseStoreWithTimeoutReportsErrorAndTimeout'
  ./cmd/ai-viewer-ingest`: passed.
- Focused replay/marker tests:
  `go test -race -count=1 -timeout=120s -run
  'TestRecordWorkerErrorLogsReplayRequiredFields|TestStopContext_ReplayOnlyWorkerErrorsClassifyReplayRequired|TestWorkerRuntime_ExpiredShutdownContextReportsReplayRequiredOnce|TestWorkerRuntime_SQLiteContentionReportsReplayRequiredAndReplays'
  ./internal/ingest`: passed.
- Focused serve tests:
  `go test -race -count=1 -timeout=120s -run
  'TestServeRuntimeCloseIsIdempotent|TestServeGracefulShutdownOrder|TestServeGracefulShutdownBoundsNotifyPollerWait|TestServeGracefulShutdownBoundsStoreClose'
  ./cmd/ai-viewer-serve`: passed.
- Affected-package race suite:
  `go test -race -count=1 -timeout=240s ./cmd/ai-viewer-ingest
  ./cmd/ai-viewer-serve ./internal/ingest`: passed.
- `scripts/test.sh`: passed; Go race/coverage total 81.7%, frontend Vitest
  929 tests passed.
- `scripts/check-coverage.sh`: passed; gated aggregate 85.4%, every gated
  package >= 80%.
- `scripts/lint.sh`: passed.
- `scripts/build.sh`: passed.
- `scripts/check-ingestion-parity.sh --fixtures`: passed.
- `go test -run='^Fuzz' ./internal/adapters/...`: passed.
- `scripts/spec-drift.sh`: passed.
- `scripts/test/systemd-units-test.sh`: passed.
- `scripts/test/install-system-test.sh`: passed.
- `bash -n scripts/install-system.sh scripts/test/install-system-test.sh
  scripts/test/systemd-units-test.sh`: passed.
- `scripts/scan-ai-attribution.sh`: passed.
- `scripts/scan-secrets.sh`: passed; existing null-byte fixture warnings only,
  final scanner result clean.
- `git diff --check`: passed.

Pending validation:

- External implementation-review rerun is pending after round-2 fixes.
- `scripts/check-bench.sh` still cannot produce valid benchmark evidence:
  latest refusal on 2026-06-26 was `loadavg 1m=33.66`, threshold `12.00`.

### Implementation Review Gate - Round 3

Outcome: `PRODUCTION GRADE` from five reviewers; `glm` unavailable for a final
vote after two 30-minute timeouts. The partial `glm` output still identified one
real spec-memory gap, which was accepted and fixed.

Reviewer votes:

- `mimo`: `PRODUCTION GRADE`.
- `deepseek`: `PRODUCTION GRADE`.
- `qwen`: `PRODUCTION GRADE`.
- `minimax`: `PRODUCTION GRADE` with P3-only residual test-depth observations.
- `kimi`: `PRODUCTION GRADE` with P3-only residual observations.
- `glm`: no final vote; two 30-minute runs timed out after read-only validation.
  The first run passed focused tests, systemd/install tests, spec drift,
  secrets/attribution scans, lint, and build before timing out. The second run
  passed focused race tests, affected-package race tests, spec drift, and
  systemd/install tests before timing out.

Accepted findings and fixes:

- `glm` partial review found that the `scanWG` pre-add/source-start-failure
  contract was only in code comments/tests, not durable specs. Fixed in
  `.agents/sow/specs/ingester.md`: source-start failures before the adapter
  `Scan` goroutine starts must decrement the pre-added scan counter exactly once;
  started sources delegate the decrement to `runAdapter` after `Scan` completes
  or is canceled.
- `kimi` flagged a P3 observability inconsistency: serve could log
  `shutdown_clean` even when the read-only store close timed out. Fixed by
  making `runGracefulShutdown` return an all-phases-clean boolean and logging
  `shutdown_clean` only when listener wait succeeds and every bounded phase was
  clean.
- The stale serve shutdown comment still said the store was closed only by the
  `run()` defer. Fixed to describe the bounded close path plus idempotent defer
  fallback.
- The ingester pprof residual text overstated the risk by implying pprof can keep
  the process alive until systemd timeout. Fixed to state that process exit reaps
  the pprof listener/handlers and `StopContext` does not wait for pprof.

Rejected or downgraded residuals:

- Missing dedicated `ShutdownResolverTimeout` tests remain P3. Existing tests
  cover caller-deadline worker timeout, resolver deadline capping, resolver-loop
  stop behavior, and clean final resolver pass. A direct resolver-timeout
  outcome seam can be added later if a future change touches resolver internals.
- Missing direct tests for `check-parity` not taking the daemon write lock and
  serve signal-registration ordering remain P3. Both are statically evident from
  the code paths and were not required to close SOW-0104.

Post-fix validation:

- Focused serve race tests:
  `go test -race -count=1 -timeout=120s -run
  'TestServeGracefulShutdownOrder|TestServeGracefulShutdownReportsCleanWhenAllPhasesClean|TestServeGracefulShutdownBoundsNotifyPollerWait|TestServeGracefulShutdownBoundsStoreClose|TestServeRuntimeCloseIsIdempotent'
  ./cmd/ai-viewer-serve`: passed.
- Focused ingester race tests:
  `go test -race -count=1 -timeout=120s -run
  'TestIngestShutdownForensicsMarkers|TestRun_StoreOpenReceivesCanceledSignalContext|TestRun_PartialStartupSignalUsesBoundedCloseAndReleasesLock|TestShutdownIngestRuntimeWaitsAdaptersInParallelWithStopContext|TestCloseStoreWithTimeoutReportsErrorAndTimeout'
  ./cmd/ai-viewer-ingest`: passed.
- `scripts/spec-drift.sh`: passed.
- Affected-package race suite:
  `go test -race -count=1 -timeout=240s ./cmd/ai-viewer-ingest
  ./cmd/ai-viewer-serve ./internal/ingest`: passed.
- `scripts/test/systemd-units-test.sh && scripts/test/install-system-test.sh`:
  passed.
- `git diff --check`: passed.
- `scripts/lint.sh`: passed.
- `scripts/test.sh`: first concurrent run failed once in
  `TestTail_AppendsToExistingFileTriggerEvents` while `scripts/lint.sh` was also
  running heavy static analysis; immediate isolated reruns of that test and the
  full `./internal/adapters/aiagent_v3` package passed. A sequential rerun of
  `scripts/test.sh` passed: Go race/coverage total 81.8%, frontend Vitest 929
  tests passed.
- `scripts/check-coverage.sh`: passed; gated aggregate 85.4%, every gated
  package >= 80%.
- `scripts/build.sh`: passed; frontend bundle-size gate passed, binaries built.
- `scripts/check-ingestion-parity.sh --fixtures`: passed.
- `go test -run='^Fuzz' ./internal/adapters/...`: passed.
- `scripts/scan-ai-attribution.sh && scripts/scan-secrets.sh`: passed; existing
  null-byte fixture warnings only, final scanner result clean.
- `scripts/check-bench.sh`: still blocked by host load; latest refusal on
  2026-06-26 was `loadavg 1m=16.47`, threshold `12.00`.

### CI Follow-Up - 2026-06-26

The `test` CI job failed on commits `e5c320d` and `44df0dd` in
`TestRun_PartialStartupSignalUsesBoundedCloseAndReleasesLock`:

- CI evidence: `partial startup signal elapsed = 632.75357ms, want bounded close
  timeout`.
- Root cause: the test measured the whole command path, including SQLite
  writer-store open and migration under `-race`, while the behavior under test is
  the bounded close path after a post-open/pre-start signal. On a loaded CI
  runner, store open can consume the 500 ms test budget even when the bounded
  close path is correct.
- Fix: pre-open the writer store before the measured run and return it through
  the `openWriterStore` seam. This keeps the test focused on the
  partial-startup shutdown path, while existing store tests continue to own
  SQLite open/migration behavior.

Post-fix validation:

- `go test -race -count=10 -timeout=120s -run '^TestRun_PartialStartupSignalUsesBoundedCloseAndReleasesLock$' ./cmd/ai-viewer-ingest`
- `go test -race -count=1 -timeout=240s ./cmd/ai-viewer-ingest`
- `scripts/lint.sh`
- `scripts/test.sh`
- `scripts/check-coverage.sh coverage.out`
- `git diff --check -- cmd/ai-viewer-ingest/main_test.go .agents/sow/current/SOW-0104-20260624-ingester-graceful-restart-timeout.md`
- `scripts/scan-secrets.sh`

### Benchmark Gate Attempt - 2026-06-26

`scripts/check-bench.sh` started during a valid preflight window:

- attempt 1 preflight: `loadavg 1m=11.02`, threshold `12.00`,
  effective `GOMAXPROCS=24`, `-cpu=1`, `-p=1`.
- attempt 1 result: the gate found first-pass `sec/op` regressions above the
  20% threshold in `ClaudeScan_SyntheticCorpus` (`+36.53%`),
  `CodexScan_SyntheticCorpus` (`+28.29%`), and `SessionsListQuery`
  (`+24.49%`).
- gate contract: a real workstation benchmark failure requires the same
  benchmark to regress on the second attempt.
- attempt 2 result: refused before sampling because the host became busy:
  `loadavg 1m=13.51`, threshold `12.00`.

Disposition: no valid benchmark pass and no reproduced benchmark failure yet.
SOW-0104 remains current. Rerun the full benchmark gate in a quieter window,
preferably only after `loadavg 1m` is comfortably below the `12.00` preflight
threshold, so both attempts can complete if the first attempt reports a
regression.

### Benchmark Headroom Watch - 2026-06-26

A bounded 30-minute watcher waited for `loadavg 1m < 8.00` before launching
`scripts/check-bench.sh`. This was intentionally stricter than the benchmark
gate's hard `12.00` cutoff because the previous attempt started at `11.02` and
lost the required retry to rising host load.

Observed range during the watch:

- first sample: `loadavg 1m=16.93`.
- lowest observed sample: `loadavg 1m=9.79`.
- last sample before timeout: `loadavg 1m=12.43`.
- result: watcher timed out before launching `scripts/check-bench.sh`.

Disposition: still no valid benchmark pass and no reproduced benchmark failure.
This is repeated busy-host evidence only. SOW-0104 remains current until the
local benchmark gate completes in a valid window or the operator explicitly
accepts a documented exception.

### CI Closeout - 2026-06-26

The SOW-0104 benchmark blocker was resolved by `SOW-0106`, which completed the
benchmark-gate regression triage and records the corrected standalone benchmark
gate plus full `scripts/gates.sh` passing under valid preflight conditions.

The later CI-reopen blockers from SOW-0105 review were closed by commit
`1884f0842cd26b95b8c7ea7021757bd7bb7be794`:

- GitHub `ci` run `28235802975`: success. Jobs passed: `lint`, `gates`,
  `frontend` including Playwright E2E, `test`, `embed-smoke`, and
  `codacy-coverage`.
- GitHub `codeql` run `28235802884`: success for actions, Go, and
  JavaScript/TypeScript.

SOW-0104 is completed and moved to `.agents/sow/done/`. The remaining
SOW-0097-lineage work is the parent SOW-0096 final closure review.

## Lessons Extracted

- Shutdown terminal markers must reflect all bounded phases, not only the
  listener/server result. A prior warning marker is not enough if the terminal
  marker says `shutdown_clean`.
- Wait-group pre-add patterns are durable contracts when a later gate depends on
  the counter reaching zero. Tests prove the race, but specs must also record who
  owns each decrement on success and failure paths.

## Followup

None yet.

## Regression Log

None yet.
