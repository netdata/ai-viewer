# SOW-0104 - Ingester Graceful Restart Timeout

## Status

Status: in-progress

Sub-state: Gap analysis converged in reviewer round 7 with six
`NOTHING MORE CAN BE DONE` votes. The next step is drafting the implementation
plan for plan-gate review.

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

Status: implementation plan drafting pending after gap-analysis convergence.

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

Implementation plan:

- To be finalized after the gap-analysis review gate converges.

Validation plan:

- To be finalized after the gap-analysis review gate converges.

Artifact impact plan:

- Specs: `.agents/sow/specs/ingester.md`,
  `.agents/sow/specs/deployment.md`, and
  `.agents/sow/specs/observability.md`.
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
  diagnosis.
- Serve's read-only store close error handling and startup signal window are
  low-risk but should be considered under the serve in/out decision.
- A `--shutdown-timeout` CLI flag is not part of this SOW unless the
  implementation plan proves operator configurability is necessary.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
