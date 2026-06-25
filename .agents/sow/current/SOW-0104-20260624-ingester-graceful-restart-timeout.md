# SOW-0104 - Ingester Graceful Restart Timeout

## Status

Status: in-progress

Sub-state: Gap analysis reviewer round 3 did not converge. Accepted findings
from `minimax` and `kimi` are folded into this SOW. The next step is a
same-scope gap-analysis rerun after this revision; `glm` ended without a usable
final vote and is not retried in round 3 because accepted P1/P2 findings exist.

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
- Local mirrored service templates from Grafana Agent, Grafana Alloy,
  Prometheus Ansible, and Netdata set explicit `TimeoutStopSec` values. This is
  not proof ai-viewer should copy their values, but it is evidence that packaged
  daemons commonly make stop-timeout contracts explicit instead of relying on a
  manager default.

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
   The target contract must define a formula before choosing `TimeoutStopSec`,
   for example:
   `adapter_grace + active_write_bound + worker_drain_bound + backfill_bound +
   final_resolver_bound + safety_margin`.

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

14. **Serve restart is related but lower risk.**
   `scripts/install-system.sh` restarts the server unit too. The server has a
   separate bounded HTTP shutdown path and no ingest drain, so implementation may
   keep serve out of scope, but the specs/tests must state that explicitly if
   only the ingester units receive a stop-timeout change.

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

20. **The observed systemd timeout value is not recorded.**
    The failure was "systemd stop timed out", but the SOW has not recorded
    whether the manager used the default `DefaultTimeoutStopSec` value or a
    workstation-specific override. The shutdown budget and static unit value
    must be anchored to the observed timeout class and an explicit safety
    margin.

21. **Serve unit timeout scope must be decided before implementation.**
    The installer restarts both `ai-viewer-ingest.service` and
    `ai-viewer-serve.service`. The serve binary has its own 30 s bounded HTTP
    shutdown path, so it is lower risk than the ingester, but both system and
    user serve unit templates also lack explicit `TimeoutStopSec`. The SOW must
    decide whether serve units receive an explicit timeout for consistency or
    remain on the systemd default with a written rationale and matching tests.

22. **Store close errors and close latency need a shutdown contract.**
    `Store.Close()` returns an error but the ingester currently discards it.
    `sql.DB.Close()` can also wait for in-flight queries to release the
    connection. Shutdown observability and the budget formula must include close
    error logging and either a close-latency bound or a documented tested
    assumption.

23. **Second-signal and abandoned-goroutine behavior is implicit.**
    A second SIGTERM/SIGINT after Go's signal context is cancelled can
    force-terminate the process, and goroutines not drained before process exit
    are reaped by the OS. This may be acceptable, but it needs to be stated in
    the shutdown contract so later reviews do not treat it as an accidental
    leak.

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
  `busy_timeout` contribution.
- A post-scan backfill shutdown test proving SIGTERM during read-model backfill
  cancels or bounds the backfill, does not race store close, and leaves derived
  read models safely rebuildable.
- A production-path adapter shutdown test proving `ctx.Done()` causes
  `runAdapter` to close the event channel and the corresponding worker to reach
  channel-close final flush within budget.
- A source-start failure accounting test proving `scanWG` drains when any source
  fails before `runAdapter` starts, `scanDone` and `backfillDone` still close,
  and successful sources are not stranded before Tail.
- Per-adapter cancellation tests, or explicit adapter-by-adapter residual-risk
  entries, proving concrete adapters return promptly from `Scan`/`Tail` when
  `ctx` is canceled.
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
- `scripts/test/install-system-test.sh` must verify rendered system units keep
  the selected stop-timeout value and must not drop existing hardening
  directives while rendering. Assertions must check exact directive values, not
  mere presence.
- Spec updates before tests/code:
  - `.agents/sow/specs/ingester.md`: define terminal shutdown-drain expiry,
    replay semantics, duplicate-warning suppression, backfill shutdown, SQLite
    contention assumptions, final resolver bound, worker serialization under
    `SetMaxOpenConns(1)`, terminal exit-code behavior, second-signal behavior,
    source-start scan accounting, adapter cancellation/resource-release
    behavior, adapter-cursor replay assumptions, one-shot subcommand non-scope,
    and store-close/WAL behavior.
  - `.agents/sow/specs/deployment.md`: define the systemd stop-timeout budget
    formula and its relationship to ingester shutdown budgets in system and user
    install modes, including the serve-unit in/out decision, sum-vs-max
    budget arithmetic, rendered-unit assertions, and operator-visible restart
    latency.
  - `.agents/sow/specs/observability.md`: define the shutdown log messages and
    structured fields for transient retry, drain expiry, replay-required,
    terminal drop, backfill cancellation, final resolver timeout, store close
    failures, and selected non-zero/zero exit behavior.
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

Status: rerun pending after round-2 findings were folded into the gap analysis.

Positive vote required: `NOTHING MORE CAN BE DONE`.

The reviewers must check whether this gap analysis misses any shutdown,
durability, source-replay, SQLite, systemd, logging, testing, installation, or
side-effect concern before implementation planning begins.

## Pre-Implementation Gate

Status: blocked until gap-analysis gate converges.

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
- Document second-signal force termination and abandoned goroutine behavior.
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

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
