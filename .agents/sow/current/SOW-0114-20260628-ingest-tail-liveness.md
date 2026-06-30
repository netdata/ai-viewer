# SOW-0114 - Ingest Tail Liveness

## Status

Status: in-progress

Sub-state: Milestones 2-7 implemented; implementation review round 12
converged with all six reviewer outputs positive or P3/rejected
non-blockers; focused/static/full test/build gates are green; the installed DB
was deleted and a fresh low-priority systemd ingest is running from scratch.
Benchmark comparison remains blocked because the operator-requested
idle-scheduled execution is not comparable to the committed normal-priority
workstation baseline.

## Requirements

### Purpose

Ensure realtime ingestion cannot silently stall for a completed source because
another source is still scanning, slow, blocked, or endlessly producing new
history. A source that has completed its startup scan must enter realtime tail
mode independently, and the operator must be able to see freshness failures in
health and logs.

### User Request

The user reported that a Codex session created after June 26 was missing from
the UI and said the project must not only fix the symptom: this class of failure
should never happen again.

### Assistant Understanding

Facts:

- A Codex rollout file for session `019f08f4-...-ccdeec9ea9b2`
  exists under `$HOME/.codex/sessions`.
- The installed database has no row for that session id.
- The installed Codex source progress stopped after the June 26 scan.
- Installed logs show Codex scan completed, but do not show Codex `tail
  starting`.
- Code currently waits for all configured sources to finish startup `Scan()`
  before the shared read-model backfill gate closes.
- Code currently starts `Tail()` only after that shared backfill gate closes.
- In the installed run, `aiagent_v2` and `opencode` did not log scan
  completion, so the shared gate did not close for completed sources.

Inferences:

- The missing Codex session is not a UI search issue. It is absent because the
  Codex adapter never entered realtime tail mode after the startup scan.
- A single global scan barrier is the structural defect. It lets one source's
  long or stuck scan block other sources that have already caught up.
- Restarting the service may ingest the missing file, but that is only symptom
  recovery. It does not remove the structural liveness risk.

Unknowns:

- Whether `aiagent_v2` and `opencode` are genuinely still scanning, blocked on
  worker backpressure, or progressing indefinitely because their live sources
  keep changing. The fix must be correct for all three cases.

### Acceptance Criteria

- A source that finishes `Scan()` starts `Tail()` without waiting for unrelated
  sources to finish scanning, finish backfilling, fail, or time out.
- The source lifecycle is durable and API-visible: health can distinguish
  scanning, scan complete but not tailing, tailing, tail failure, and stale
  progress without relying on log absence.
- A source whose own `Scan()` runs abnormally long becomes visible as a degraded
  lifecycle state with structured logs and health evidence. The fix must not
  only make other sources healthy while leaving the stuck source silent.
- A source whose `Tail()` returns a non-shutdown error does not die silently:
  the source lifecycle records the failure, the health surface degrades, and the
  source is restarted with bounded exponential backoff unless the implementation
  deliberately changes the existing spec and explains why.
- A source whose `Tail()` hangs without returning is not indistinguishable from
  a healthy idle tail. Tail loops must produce a real tail heartbeat from inside
  the adapter/watch loop, and absence of that heartbeat beyond the stale-tail
  threshold must degrade health and trigger a controlled per-source restart
  attempt. A stale Tail must not remain only an observed-but-dead state.
- Tail startup remains safe with the deferred read-model optimization:
  canonical tables remain current even if FTS/rollup backfill is slow, blocked,
  timed out, or still waiting on other sources.
- The global `deferReadModels` behavior is explicitly redesigned or retained
  with documented semantics so early tailing cannot leave realtime data
  permanently missing from FTS/rollups.
- A stuck source scan cannot permanently suppress FTS/rollup refresh for
  unrelated sources that have already entered Tail.
- A source that completed its scan receives read-model repair for its scan-time
  events without waiting for every other source to complete. The all-sources
  backfill may remain as a final reconciliation pass, but it is not the only
  path that makes completed-source scan rows searchable/stat-able.
- Failed, interrupted, or externally cancelled read-model repair is
  health-visible and retryable; it must not leave FTS/rollup tables silently
  empty or stale. Source-scoped repair must not use a short synthetic execution
  timeout that restarts large repairs from the beginning.
- Source lifecycle state exists from scan start, even before the source emits
  enough events to trigger a normal worker batch flush.
- A configured source that fails before Scan starts is health-visible as
  `start_failed` / `construct_failed`; startup failures must not be only a log.
- Lifecycle transitions are visible through the read API and realtime notify
  path without waiting for manual refresh or log inspection.
- Automated tests cover the regression: a completed source tails and ingests new
  events while another source's scan is still blocked.
- Automated tests cover the scan-error / scan-deadline path and stale-source
  observability.
- Automated tests cover read-model behavior when tail events arrive before or
  during startup backfill.
- Runtime recovery verifies all configured sources for data completeness across
  the stall window, not only the reported Codex session.
- Specs are updated before tests and implementation.
- The installed service is upgraded and the previously missing Codex session is
  verified in the database/UI after implementation using local-only exact ids.

## Analysis

Sources checked:

- `cmd/ai-viewer-ingest/main.go`
- `cmd/ai-viewer-ingest/sources.go`
- `internal/adapters/codex/adapter.go`
- `internal/adapters/opencode/adapter.go`
- `internal/adapters/aiagent_v2/adapter.go`
- `.agents/sow/specs/ingester.md`
- `.agents/sow/specs/adapter-contract.md`
  - `.agents/sow/specs/observability.md`
- systemd status, journald logs, `/api/health`, and installed SQLite source
  progress rows

Current state:

- The current lifecycle is `Scan()` for each source, then one shared
  all-sources read-model backfill, then `Tail()` for each source.
- This design preserves derived read-model backfill isolation, but it couples
  realtime ingestion liveness across unrelated sources.
- The observed incident proves the coupling is unsafe: Codex finished scanning
  but never tailed because other sources did not finish scanning.
- `/api/health` does not expose durable source lifecycle state today. It reports
  `sources.last_seen_at`, but the writer updates that field only when bumping
  source error counters; successful source progress is committed to
  `source_progress.updated_at` and is not surfaced as scan/tail state.
- `deferReadModels` is a single global ingester flag. It was designed for the
  all-sources startup scan path and must be made explicit in any design where
  some sources tail while others still scan.

Risks:

- Decoupling Tail from the shared backfill may reintroduce write contention if
  the derived read-model backfill runs concurrently with tail-mode flushes.
- Starting Tail before read-model backfill means derived FTS/rollup tables may
  be stale until repair runs; this is acceptable only if canonical tables remain
  current and the stale derived state is visible/repairable.
- Per-source lifecycle status must avoid false confidence: stale source progress
  should degrade health, but scans that are intentionally long must not be
  mistaken for successful tailing.
- Tail failure is the same class of liveness defect as scan-to-tail blocking:
  the source can stop producing events while the process remains healthy.
- Read-model backfill currently deletes and rebuilds derived FTS/rollup rows.
  Letting tail-mode flushes run before or during backfill requires a clear
  correctness contract and a test for the chosen behavior.
- The implementation must not write to source files or source databases.

## Gap Analysis

The goal is not only "make the missing Codex session appear". The complete
system outcome requires all of the following:

- Remove the global liveness coupling:
  - `Scan()` completion for source A must not wait for source B before source A
    can enter `Tail()`.
  - Source A must not wait for global read-model backfill before entering
    canonical tail ingestion.
  - The implementation plan must explicitly remove or replace the current
    `runAdapter` wait on `backfillDone` before `Tail()`. The global read-model
    backfill may still wait for all scans if that remains the chosen repair
    trigger, but Tail startup cannot be gated by it.
  - Gap-level decision: source Tail startup uses approach A, not a per-source
    `backfillDone` gate. After a source's Scan outcome, Tail starts immediately
    from that source's safe cursor. The global backfill path becomes background
    repair/reconciliation, not the Tail-start gate.
  - A failing, timed-out, or still-pending read-model backfill must not block
    primary canonical ingestion.
  - Existing `startPostScanBackfill` tests must be updated for the new
    background-reconciliation role. They should still prove bounded all-scan
    reconciliation behavior where retained, but new tests must prove Tail starts
    before global backfill completes.

- Address same-source scan liveness:
  - Removing cross-source blocking is insufficient if a source's own `Scan()`
    never returns. Such a source would still never enter Tail, and its own
    realtime data could still be missed.
  - The implementation plan must audit all five registered adapters
    (`aiagent_v3`, `aiagent_v2`, `claude-code`, `codex`, `opencode`) and record
    what makes each `Scan()` terminate.
  - Long-running scans must be observable with scan start time, elapsed time,
    and structured warnings. If a scan timeout / promote-to-tail design is used,
    `adapter-contract.md` must be updated because the lifecycle contract
    changes.
  - Scan error and scan local-deadline paths must be handled explicitly. A scan
    failure may still fall through to Tail; parent-context shutdown must still
    stop cleanly.
  - Fatal-vs-recoverable Scan error decision: adapters signal a fatal Scan
    failure by returning an error wrapping a canonical `FatalScanError` marker
    that the source supervisor detects with `errors.As`. Non-context Scan
    errors that do not wrap this marker are recoverable by default: the
    supervisor records `scan_failed` evidence and may fall through to Tail.
    Fatal Scan errors include source-root/storage/schema failures that make Tail
    unsafe or impossible; parse/content errors that can be skipped or reported
    through `OnError` remain recoverable unless an adapter spec explicitly says
    otherwise.
  - Scan context cancellation paths must write lifecycle state before returning:
    parent shutdown moves `scanning -> stopped`, while a local scan deadline or
    recoverable scan error records `scan_failed` evidence before the chosen
    recover-or-stop transition.
  - The current binary creates one shared adapter context for all sources. If
    the implementation plan uses per-source scan deadlines, cancellation, or
    promote-to-tail behavior, it must add per-source context ownership so one
    stuck source is not cancelled by cancelling every source.
  - Scan-termination audit, based on current adapter implementations:
    aiagent_v2, aiagent_v3, claude-code, and Codex walk finite file sets and
    should terminate when the walked source snapshot is exhausted; Opencode scans
    a paginated SQLite watermark and should terminate when caught up to the
    current source DB state. Pathological source growth can make any scan
    effectively long-running, so observability remains required even though the
    algorithms are bounded over a stable snapshot.
  - The adapter audit must include the Scan-to-Tail cursor handoff. Codex,
    Claude Code, and Opencode record the final scan cursor on the adapter before
    Tail; aiagent_v2 and aiagent_v3 currently discard the scan cursor and
    snapshot Tail from current disk state.
  - aiagent_v2 and aiagent_v3 are not just "needs audit" cases: their current
    snapshot-on-tail behavior is a confirmed Scan-to-Tail data-loss window.
    This SOW must close it by adopting the scan-cursor handoff already used by
    Codex, Claude Code, and Opencode. Adapter-specific exceptions are out of
    scope for this SOW and would require a future SOW with proof and tests.
  - Gap-level adapter contract decision: keep the existing `canonical.Adapter`
    interface for this SOW. The ingester continues to drive `Scan()` followed by
    `Tail()` on one adapter instance. Every adapter must persist the final Scan
    cursor on that instance, and the immediately following `Tail()` must resume
    from that cursor instead of snapshotting current source state. Cold Tail
    from a fresh instance is allowed only after the ingester has first reloaded
    the durable cursor and run a catch-up `Scan()` to re-establish the handoff.
  - For fsnotify adapters, Scan-to-Tail handoff includes a Tail-start catch-up
    phase: establish the watcher, read from the stored scan cursor through the
    current source state, then follow live fsnotify events. aiagent_v2 and
    aiagent_v3 must add this catch-up reread behavior; a `scanCursor` field
    alone is not sufficient.

- Close all permanent source-death paths:
  - The ingester spec already says a non-shutdown `Tail()` error should log
    loudly, mark `parse_errors++`, and restart the adapter after exponential
    backoff. Current `runAdapter` logs the error and returns, which closes that
    source worker and leaves the source dead until process restart.
  - The implementation plan must implement the documented restart loop. Silent
    source death is not acceptable.
  - Tail restart must be bounded, parent-context aware, health-visible, and
    tested with a fake adapter whose first Tail returns a non-context error.
  - Tail restart must reuse the recorded scan/tail cursor and must not
    re-snapshot current source state in a way that skips records appended during
    a Tail failure/backoff window.
  - Gap-level Tail restart cursor decision: do not extend the adapter interface
    in this SOW. On Tail restart, the ingester reloads
    `source_progress.cursor` through the same cursor lookup path used by normal
    source startup, always constructs a fresh adapter instance through the same
    factory path used by normal startup after the prior attempt has been canceled
    or returned, runs a catch-up
    `Scan(ctx, persistedCursor, out)` to emit anything missed during the
    failure/backoff window and refresh the adapter's in-instance scan cursor,
    then starts `Tail()` from that handoff cursor. Restart must never call a
    cold Tail that snapshots current source state.
  - Tail restart catch-up Scan is not the startup Scan. It does not call
    `scanDone`, does not participate in the all-sources scan barrier, and does
    not trigger post-scan global backfill. Its rows are treated like tail-time
    rows: normal incremental FTS/rollup refresh applies unless global
    rebuild-active deferral is set.
  - Tail restart catch-up Scan must be observable. While the source is in
    `tail_restarting`, catch-up Scan emits structured progress logs at bounded
    intervals with elapsed time, emitted row/event count where available, and
    cursor/progress evidence. A long catch-up Scan is reported as progressing
    retry work, not mistaken for healthy `tailing`.
  - Tail restart factory-construction failure decision: a factory or adapter
    construction error during `tail_restarting` is a restart-attempt failure,
    not a permanent source death. It records sanitized lifecycle error evidence,
    increments the same consecutive `tail_restart_count`, applies the same
    1s/2s/4s/.../60s context-cancellable backoff, and retries while the parent
    source context is alive. Constructor panics remain process-fatal under the
    existing panic policy; adapter constructors must not perform unbounded
    blocking I/O in this SOW.
  - Adapter-specific Tail restart semantics must be pinned in the plan. In
    particular, Opencode's `warmStart` flag currently distinguishes Tail after a
    Scan cursor from a cold HEAD-snapshot Tail; restart catch-up Scan must use
    the same proven no-skip/no-duplicate behavior as startup Scan->Tail, with a
    regression test instead of an unverified boolean assumption. Gap-level
    expectation: after a successful restart catch-up Scan on the same logical
    Tail attempt, Opencode's subsequent Tail follows a Scan cursor and therefore
    uses the warm-start path. If catch-up Scan fails before recording the
    handoff cursor, Tail must not start.
  - The restart loop must keep the source events channel open across retry
    attempts and close it only when the source lifecycle is terminal. A restart
    loop inside `runAdapter` or its owning goroutine is acceptable; closing the
    channel after the first Tail failure is not.
  - Gap-level Tail restart ownership decision: the source lifecycle supervisor
    goroutine created by `startSourceWithFactoryLookup` owns the restart loop
    and determines when no accepted source attempt may continue. It has closure
    access to factory lookup, adapter construction location, source id, format,
    logger, `Ingester`, and cursor lookup. `runAdapter` becomes an attempt
    helper or is replaced by one; it does not own channel closure. The owning
    source goroutine closes the event channel only after the supervisor exits
    because of parent-context shutdown, unrecoverable startup failure, or a
    documented terminal lifecycle state. If an adapter violates the
    cancellation contract and sends after that closure, the Tail wrapper must
    convert the panic into adapter-failure evidence instead of crashing the
    process.
  - The supervisor creates a per-source context derived from the parent
    adapter context, and a per-attempt child context for each Scan/Tail attempt.
    Cancelling a stale or failed attempt must not cancel unrelated sources.
    Shared parent cancellation remains the shutdown path for all sources.
  - The implementation plan must choose a concrete Tail-cancellation grace bound
    for stale restart before a non-returning Tail becomes terminal/degraded
    `tail_failed`.
  - Tail restart backoff sleeps must be context-cancellable so shutdown is not
    delayed by a sleeping retry loop.
  - Backoff parameters are part of the contract: start at 1s, double on repeated
    Tail failures, cap at 60s, and continue indefinitely while the parent
    context is alive.
  - Tail restart escalation is health-visible. After sustained restart failure
    (100 consecutive Tail/catch-up failures or 24 hours in `tail_restarting`,
    whichever happens first), the source remains degraded, continues the bounded
    retry loop while the parent context is alive, and records a sanitized
    `lifecycle_error` summary that tells the operator this is no longer a
    short transient. The runbook must document that the automatic loop keeps
    trying, but operator intervention is needed when the same error persists.
    The consecutive failure counter resets to zero only after a successful
    `tail_starting -> tailing` transition.
  - Rapid repeated Tail failure is covered by the same backoff contract. A
    source that cycles through `tail_failed -> tail_restarting ->
    tail_starting -> tail_failed` must not write lifecycle rows in a tight loop:
    the 1s/2s/4s/.../60s backoff bounds restart cadence, keeps writer
    contention acceptable, and still updates the consecutive failure counter and
    escalation evidence at the documented thresholds.

- Make source lifecycle durable and API-visible:
  - Add or reuse durable fields so `/api/health` can expose source lifecycle
    state without log scraping. The minimum contract is a source `state` enum
    and state timestamps sufficient to identify `scanning`, `tailing`,
    `tail_failed`, and stale pre-tail states.
  - Lifecycle state must be persisted in SQLite because `ai-viewer-serve` is a
    separate process. The location is the existing one-row-per-source runtime
    table, `source_progress`.
  - The legacy `sources.cursor` column remains in the schema for this SOW as a
    nullable historical column. Migration 0012 does not drop it and does not add
    a CHECK constraint to it, because this SOW keeps migrations additive and
    must not risk table-rebuild compatibility for old local databases that may
    contain legacy values. The implementation must not read from or write to
    `sources.cursor`; tests and comment/spec updates make
    `source_progress.cursor` authoritative. Migration 0012 must not copy
    historical `sources.cursor` values into `source_progress.cursor`: the
    latter has been the authoritative cursor since migration 0002, and copying
    a stale legacy column could move restart cursors to an unverified point.
  - Gap-level schema decision: lifecycle fields live on `source_progress`. The
    target columns are
    `lifecycle_state TEXT NOT NULL DEFAULT 'unknown'`,
    `lifecycle_state_at INTEGER NOT NULL DEFAULT 0`,
    `scan_started_at INTEGER`, `scan_completed_at INTEGER`,
    `tail_started_at INTEGER`, `tail_heartbeat_at INTEGER`,
    `tail_failed_at INTEGER`, and
    `tail_restart_count INTEGER NOT NULL DEFAULT 0`, and
    `lifecycle_error TEXT`. The same migration adds read-model repair state on
    `source_progress`:
    `read_model_state TEXT NOT NULL DEFAULT 'unknown'`,
    `read_model_state_at INTEGER NOT NULL DEFAULT 0`,
    `read_model_repair_started_at INTEGER`,
    `read_model_repair_completed_at INTEGER`,
    `read_model_repair_failed_at INTEGER`,
    `read_model_repair_attempts INTEGER NOT NULL DEFAULT 0`, and
    `read_model_error TEXT`. The implementation plan may rename fields for
    local style, but must preserve the information contract.
  - Existing rows default to `unknown` so migration does not claim old lifecycle
    state it never observed. On first fixed-ingester startup, each configured
    source moves from `unknown` to the real state. Migration 0012 should add
    CHECK constraints for the new text enum columns (`lifecycle_state` and
    `read_model_state`) so invalid state strings cannot be written silently.
    Startup ordering is part of this contract: after discovery/config expansion
    and unconfigured-row reconciliation, every configured/discovered source whose
    persisted lifecycle is `unknown` must be moved to `starting` by the
    pre-submit lifecycle path before adapter factory lookup, location stat,
    adapter construction, `Submit`, or Scan. The first health response after a
    fixed ingester has started must not show a configured source as degraded only
    because migration left `unknown` with timestamp `0`; it should show
    `starting` within the 60 s pre-tail grace window.
  - Unconfigured existing source rows are handled explicitly. After source
    discovery/config expansion on ingester startup, every `sources` row whose
    source id is not in the current configured/discovered set is transitioned to
    `lifecycle_state='stopped'` with a sanitized lifecycle detail that indicates
    the source is no longer configured, and a `source_status_changed` notify row
    commits in the same transaction. The implementation must not delete the row
    or silently leave it in `unknown`, because migrated `unknown` rows with
    timestamp `0` would otherwise create permanent false `degraded` health.
    `stopped` is a non-degrading historical/source-inactive state.
  - Migration numbering decision: this current SOW claims the next migration
    slot, `0012_source_progress_lifecycle.sql`. Pending SOW-0108 currently
    discusses `0012_display_title.sql` and downstream pending SOWs discuss later
    numbers; those pending SOWs must renumber when they move to `current/` if
    this SOW lands first. If another migration lands before this SOW, this SOW
    renumbers to the next available slot and updates all schema-version text in
    the same change.
  - The lifecycle row must be created eagerly at source scan start, not lazily on
    the first event batch flush. Empty sources, slow scans, and quiet tails must
    still be visible.
  - Because `source_progress.source_id` references `sources(id)`, eager
    lifecycle row creation must also create or update the `sources` row first.
  - Pre-submit lifecycle persistence is mandatory. If the initial
    `RecordSourceLifecycle(... starting ...)` write fails before adapter
    construction or `Submit`, the supervisor does not start that source
    invisibly. It logs structured context, retries the lifecycle write with
    context-cancellable 1s/2s/.../60s backoff while the parent context is alive,
    and proceeds to factory lookup only after the `starting` row commits. Hard
    DB errors therefore surface as startup/health failures instead of silent
    source omission.
  - Lifecycle transitions must be persisted at transition time
    (`scan_start`, `scan_complete`, `tail_start`, `tail_failed`,
    `tail_restarting`, terminal shutdown states) through a write path independent
    of normal event-batch flushes. The implementation plan must name the owner
    of that write path while preserving the single-writer invariant.
  - Gap-level ownership decision: lifecycle transition writes are owned by the
    `internal/ingest.Ingester` via a dedicated method called from the source
    lifecycle goroutine. The method uses the same writer DB handle, one
    transaction, and the existing single-writer SQLite serialization. The
    method must preserve the `RecordSourceLifecycle` information contract
    defined below. Concurrent supervisor lifecycle writes and worker batch
    flushes serialize through the same single writer connection and SQLite
    `busy_timeout`; lifecycle write failures are returned to the supervisor and
    handled by the retry/degraded rules instead of being swallowed.
  - The lifecycle state machine must be explicit: state enum values, legal
    transitions, transition timestamps, and default/unknown state for existing
    rows after migration.
  - Target lifecycle states: `unknown`, `starting`, `start_failed`,
    `construct_failed`, `scanning`, `scan_failed`, `scan_complete`,
    `tail_starting`, `tailing`, `tail_stale`, `tail_failed`,
    `tail_restarting`, and `stopped`. The implementation plan may collapse
    adjacent states only if tests still distinguish the acceptance-criteria
    cases.
  - Legal lifecycle transitions for this SOW:
    `unknown -> starting -> scanning`;
    `unknown -> stopped` only for existing rows that are not in the current
    configured/discovered source set;
    `stopped -> starting` when a previously stopped source is again present in
    the current configured/discovered source set at daemon startup;
    `starting -> start_failed | construct_failed | stopped`;
    any prior persisted non-`unknown`, non-`stopped` state on daemon restart ->
    `starting` when the source is still configured/discovered, or `stopped`
    when the source is no longer configured/discovered;
    any prior persisted state, including terminal failure states, transitions to
    `stopped` during startup reconciliation when the source id is absent from
    the current configured/discovered set;
    `scanning -> scan_complete | scan_failed | stopped`;
    recoverable `scan_failed -> tail_starting` when the parent context is still
    alive; fatal `scan_failed` remains degraded/terminal and does not attempt
    Tail until config changes or the service restarts;
    `scan_complete -> tail_starting`;
    `tail_starting -> tailing | tail_failed | stopped`;
    `tailing -> tail_stale | tail_failed | stopped`;
    `tail_stale -> tailing | tail_restarting | tail_failed | stopped`;
    `tail_failed -> tail_restarting | stopped`;
    `tail_restarting -> tail_starting | stopped`.
    A failed catch-up Scan during `tail_restarting` leaves the source in
    `tail_restarting`, records the error, increments retry state, and backs off
    before retrying the reload-cursor -> catch-up Scan -> Tail sequence.
    `start_failed`, `construct_failed`, and fatal `scan_failed` are terminal
    within the current process run until config changes or the service restarts;
    a later daemon startup retries them through the startup recovery rule when
    the source is still configured/discovered.
  - Crash/restart recovery rule: on ingester startup, any configured source with
    a prior non-`unknown`, non-`stopped` persisted lifecycle state is first
    transitioned to `starting` and then follows normal Scan/Tail startup. A
    graceful prior `stopped` state may remain `stopped` until the service starts
    the source again, but hard-crash residues such as `tailing`,
    `tail_restarting`, or `scanning` must not be trusted as current state.
    Read-model crash residues follow the same principle: any persisted
    `read_model_state='repairing'` from a prior process is transitioned to
    `repair_pending` on startup before the source repair loop evaluates work.
  - Lifecycle transitions must be guarded by expected current state when more
    than one goroutine can write the same source. `RecordSourceLifecycle` must
    support conditional transition updates, implemented as an equivalent of
    `UPDATE ... WHERE source_id=? AND lifecycle_state=?`; zero affected rows mean
    another actor won the race and the late transition is a logged no-op, not an
    error. The stale-tail watchdog's `tailing -> tail_stale` and
    `tail_stale -> tailing` writes and supervisor restart/failure/shutdown writes
    all use this guarded path so a late watchdog cannot overwrite
    `tail_failed`, `tail_restarting`, or `stopped`.
  - Stale Tail recovery decision: the stale-tail watchdog first transitions a
    missing-heartbeat source from `tailing` to `tail_stale`, then asks the
    source supervisor to restart that source. The supervisor cancels the active
    per-attempt context, waits a bounded grace period for `Tail()` to return,
    and then transitions to `tail_restarting` and runs the durable-cursor
    catch-up Scan path. It must never start a second Tail for the same source
    while the prior Tail goroutine is still running. If the prior Tail ignores
    cancellation and does not exit before the grace period, the source
    transitions to `tail_failed` with a sanitized `lifecycle_error`; operator
    process restart is the recovery path. Real adapters must have tests proving
    Tail observes context cancellation so the automatic restart path works.
  - Watchdog-to-supervisor restart signaling is a first-class contract, not an
    implementation detail. Each source lifecycle supervisor creates a buffered,
    size-one restart-request channel and registers it with `Ingester` before the
    source can enter `tailing`. The ingester-owned stale-tail watchdog calls the
    registry to enqueue a coalesced restart request for that source after it
    persists `tail_stale`. The source supervisor selects on that channel, cancels
    only the active per-attempt context, and unregisters the channel when the
    source stops. A missing registration for a source in `tailing` is a degraded
    lifecycle error because the watchdog has no safe restart path for it.
  - `lifecycle_state_at` is updated on every lifecycle transition. Phase-specific
    timestamps such as `scan_started_at`, `scan_completed_at`,
    `tail_started_at`, `tail_heartbeat_at`, and `tail_failed_at` record the
    phase events they name and are not rewritten on unrelated transitions.
    `tail_started_at` is set to the current attempt start time every time the
    source enters `tailing`, including after `tail_restarting -> tail_starting`
    catch-up success, so each restarted Tail attempt receives its own
    first-heartbeat grace window.
  - `scan_complete` is a transient state while the supervisor clears per-source
    read-model deferral, records repair debt, and prepares Tail start. If it
    persists beyond the existing pre-tail stale threshold, health degrades with
    `scan_complete`/`lifecycle_state_at` evidence.
  - Tail heartbeat contract: adapters call a lifecycle heartbeat callback from
    inside their Tail watch/poll loop at least once per idle poll interval and
    after every emitted tail event. A wrapper timer that heartbeats while the
    adapter is blocked is not sufficient, because it would hide a hung Tail.
    Gap-level callback decision: add `OnTailHeartbeat func()` to
    `canonical.AdapterOptions`, with a nil-safe canonical helper method such as
    `AdapterOptions.TailHeartbeat()` that no-ops when the field is nil. Adapters
    must call the helper, not the raw function field, so the zero-value
    `AdapterOptions` used by existing tests cannot panic. The
    `canonical.Adapter` interface itself remains unchanged. For fsnotify
    adapters, Tail must select on a 30 s heartbeat ticker in addition to
    fsnotify and context channels. Every adapter must call the nil-safe heartbeat
    helper at least once every 30 seconds while its Tail loop is healthy; poll
    adapters may use their natural poll cycle only if it fires within that bound,
    otherwise they must add an explicit heartbeat ticker.
    Missing heartbeats for 5 minutes in `tailing` state moves the source to
    `tail_stale` and degrades health; a later real heartbeat returns it to
    `tailing`.
    Downstream backpressure that prevents the adapter Tail loop from cycling for
    5 minutes is intentionally surfaced as `tail_stale`, because the source is
    no longer making observable progress.
  - Each adapter stores `AdapterOptions.OnTailHeartbeat` as an instance field,
    like `OnError`, and threads it into every internal Tail loop. The five
    existing adapter Tail loop signatures must be updated or wrapped so the
    heartbeat callback is called from the real watch/poll loop, not from an
    outer timer.
  - Heartbeat persistence is throttled by the ingester, not by adapters.
    Adapters may call `OnTailHeartbeat` on every poll/event, but the ingester
    keeps the latest heartbeat in memory and writes `tail_heartbeat_at` to
    SQLite at most once per 30 s per source. The persisted heartbeat may be up
    to 30 s behind the adapter callback, which is acceptable for the 5-minute
    stale threshold.
  - `OnTailHeartbeat` must be non-blocking. The callback records the latest
    heartbeat in memory only and must not perform SQLite writes, wait on a
    worker flush, or hold locks shared with the persistence path. After the
    per-attempt context is canceled, late heartbeat callbacks are defensive
    no-ops so shutdown/restart drain cannot panic or revive a stopped attempt.
    The in-memory heartbeat timestamp, throttle bookkeeping, and cancelled-attempt
    flag must use an atomic primitive or an equivalent small per-source mutex so
    the adapter Tail goroutine and watchdog goroutine are race-detector clean.
  - Heartbeat throttle state is in-memory and per ingester process. After
    ingester restart, the first heartbeat for a source may persist immediately;
    subsequent heartbeats are throttled again to the 30 s/source bound.
  - Tail stale watchdog decision: the ingester owns one context-cancellable
    stale-tail watchdog that ticks every 30 s, scans currently tailing sources'
    latest in-memory heartbeat first, falls back to durable `tail_heartbeat_at`
    only when no in-process heartbeat exists, and uses `tail_started_at` as the
    first-heartbeat grace timestamp when both live and durable heartbeat evidence
    are absent or stale immediately after `tail_starting -> tailing`. It writes
    guarded `tail_stale` or guarded recovery-to-`tailing` transitions when
    thresholds cross, and emits `source_status_changed` in the same transaction
    as each state transition. Heartbeat updates refresh `tail_heartbeat_at` but
    do not emit notify rows unless the lifecycle state changes.
  - Heartbeat persistence failure must not create false stale restarts while the
    ingester process is alive. If a throttled `tail_heartbeat_at` write fails
    behind writer contention, the in-memory heartbeat remains authoritative for
    the ingester watchdog and the next throttle window retries persistence. Serve
    can only use persisted heartbeat state, so an ingester crash after repeated
    persistence failures may still surface stale status on read; that is correct
    because the live in-memory proof is gone.
  - Serve-side stale detection is also required. `/api/health` and
    `/api/sources` compute an effective lifecycle state from persisted
    `lifecycle_state`, `tail_started_at`, and `tail_heartbeat_at` on every
    request. If the ingester process is down and the watchdog is not running, a
    source persisted as `tailing` still appears degraded/effectively
    `tail_stale` once `tail_heartbeat_at` is older than 5 minutes. The
    ingester-owned watchdog remains the write/notify path when the ingester is
    alive.
  - Serve-side effective `tail_stale` is state-filtered. It applies only to a
    persisted `lifecycle_state='tailing'`; persisted `tail_stale` remains
    `tail_stale`, and pre-tail, terminal, failed, or restarting states keep
    their persisted lifecycle state. A NULL `tail_heartbeat_at` in `tailing`
    uses `tail_started_at` as the grace timestamp until the first heartbeat is
    persisted. If both are NULL or zero for a `tailing` source, the effective
    state is `tail_stale`. NULL `tail_heartbeat_at` is normal for pre-tail,
    startup-failed, construct-failed, scan-failed, stopped, and unknown states
    and must not by itself produce `tail_stale`.
  - `source_progress.updated_at` is already durable batch-progress evidence, but
    it is not a standalone liveness signal: it advances when the worker commits
    progress and can be old for a healthy idle tail. Health decisions use
    lifecycle phase plus phase-specific timestamps: `scan_started_at` for long
    scans, `lifecycle_state_at` for pre-tail transition age,
    `tail_heartbeat_at` for tail liveness, `tail_failed_at` /
    `lifecycle_error` for failure evidence, and `read_model_state_at` for
    derived-read-model repair age. `/api/health` and `/api/sources` may expose
    `source_progress.last_seq` and `source_progress.updated_at` as secondary
    progress diagnostics, but must not treat either as a freshness or liveness
    signal.
  - `sources.last_seen_at` currently means "last source error/pricing miss time"
    in the writer path. This SOW leaves that semantic intact and adds explicit
    lifecycle/progress fields instead. Silent reinterpretation is not allowed.
  - The degraded-lag rule must be re-anchored to lifecycle phase plus the
    phase-specific timestamps above, not to `sources.last_seen_at` and not to
    `source_progress.updated_at` alone. The plan must define stale semantics per
    lifecycle phase (`scanning`, `tailing`, `tail_failed`, idle tail, unknown).
  - Gap-level API decision: keep `sources.last_seen_at` with its current
    error/pricing-miss meaning for backward compatibility, but do not use it as
    the source freshness/degraded-lag input. Add explicit lifecycle/progress
    fields to health and source status instead.
  - Target degraded rules: terminal startup/source failures (`start_failed`,
    `construct_failed`, fatal `scan_failed`, and `tail_failed`), `tail_stale`,
    and repeated `tail_restarting` are degraded immediately; `unknown`,
    `starting`, `scan_complete`, or `tail_starting` beyond the existing 60 s
    source lag threshold is degraded; `scanning` beyond 10 minutes is degraded
    with elapsed scan evidence; a single `tail_restarting` catch-up Scan beyond
    10 minutes is degraded with elapsed restart/catch-up evidence even if it has
    not yet failed repeatedly; `tailing` with no new events is not degraded
    solely because it is idle as long as `tail_heartbeat_at` is fresh or still
    within the first-heartbeat grace window from `tail_started_at`; `stopped`
    rows are not degraded because they represent a graceful stop or an existing
    source row that is no longer configured/discovered.
  - `0` timestamp values on lifecycle/read-model fields are unset sentinels, not
    epoch timestamps. Age-based degradation must not compute `now - 0` except
    for the explicit invalid `tailing` case covered above, where both
    `tail_started_at` and `tail_heartbeat_at` are NULL or zero and the effective
    state is `tail_stale`. Freshly migrated unconfigured rows move to
    `stopped` before presenter health can treat `unknown`/`0` as stale evidence.
  - All time-based lifecycle decisions must read from injectable clocks and/or
    injectable threshold/backoff settings so tests can exercise long scan,
    pre-tail stale, heartbeat stale, repair timeout, and restart escalation
    paths without waiting real production durations. Production defaults remain
    the documented thresholds.
  - Overall health status remains `degraded`, not `down`, when every configured
    source is in a degraded lifecycle state but the database and serve process
    are available. `down` remains reserved for DB/process unavailability.
  - `/api/health` must select and return the lifecycle/progress fields
    additively.
  - Gap-level API decision: `/api/sources` exposes the same lifecycle state,
    transition timestamp, progress timestamp, and restart counter fields as
    `/api/health`, because it is the operator-facing source status list.
  - Lifecycle and read-model transitions emit the existing
    `source_status_changed` notify kind, and the SSE protocol documents the
    minimal invalidation payload so the UI can refresh source status without
    polling.
  - Gap-level no-sources health decision: if no sources are configured or
    discovered, `/api/health` returns `status="degraded"` with
    `status_detail="no_sources_configured"` and an empty source list. It does
    not synthesize a fake source row. `status_detail` is an additive top-level
    JSON field with `omitempty` behavior for normal health responses. For this
    SOW, `status_detail` is single-purpose: only the no-sources condition sets
    it, through a named presenter constant so future values are explicit
    contract changes. Other degraded causes are represented in the per-source
    lifecycle/read-model fields.
  - `/api/health.sources[]` and `/api/sources.sources[]` expose the same
    lifecycle/read-model fields: `lifecycle_state`, `lifecycle_state_at`,
    `scan_started_at`, `scan_completed_at`, `tail_started_at`,
    `tail_heartbeat_at`, `tail_failed_at`, `tail_restart_count`,
    `lifecycle_error`,
    `read_model_state`, `read_model_state_at`,
    `read_model_repair_started_at`, `read_model_repair_completed_at`,
    `read_model_repair_failed_at`, `read_model_repair_attempts`, and
    `read_model_error`, plus `progress_updated_at` from the existing
    `source_progress.updated_at` column for backward-compatible progress
    evidence. Optional lifecycle/read-model timestamp fields serialize as
    nullable pointer values (`null`/omitted when unset); the presenter must not
    serialize an internal zero sentinel as an epoch timestamp.
  - Gap-level notify decision: reuse `source_status_changed` for lifecycle and
    read-model transitions. The SSE frame remains a minimal invalidation payload
    `{ "source_id": "<id>", "ts": <us> }`; clients refetch `/api/sources` or
    `/api/health` for lifecycle details. Notify rows must commit in the same
    transaction as the lifecycle or read-model state change, preserving the
    atomic data-and-notify visibility invariant.
  - Existing batch-writer parse-error notification may keep its current
    `sourceStatusChanged` boolean gate, but lifecycle/read-model transitions
    written by `RecordSourceLifecycle` insert `source_status_changed` directly
    in that lifecycle transaction. The implementation must not depend on the
    batch writer's parse-error gate to publish lifecycle/read-model invalidation
    rows.
  - Notify rows remain realtime hints with bounded retention. No retention
    increase is required for lifecycle events; `ai-viewer-serve` must refetch
    `/api/health` and `/api/sources` on startup/reconnect because old
    `source_status_changed` rows may have been pruned while serve was down.
  - Logs must distinguish scan start, scan complete, scan error, scan timeout or
    long-running warning, tail start, tail failure, tail-before-backfill, and
    backfill completion/failure/timeout.
  - Lifecycle write path decision: add an `internal/ingest.Ingester` method with
    this information contract:
    `RecordSourceLifecycle(ctx, sourceID, format, location string, update)`.
    The update carries lifecycle state, read-model state, heartbeat timestamp,
    retry counters, bounded sanitized lifecycle error text, bounded sanitized
    read-model error text, and whether a notify row is required. The method
    uses one writer transaction to upsert `sources` first, then upsert/preserve
    `source_progress`, then insert `source_status_changed` when the state
    changed. It is called from the source lifecycle goroutine, from pre-submit
    startup failure paths, from read-model repair code, and from the stale-tail
    watchdog.
  - `RecordSourceLifecycle` must preserve non-lifecycle `sources` columns such
    as `meta_json`, `fts5_index_logs`, existing source configuration values,
    and any future source metadata. It must not use `INSERT OR REPLACE`.
    Pre-submit source-row creation must resolve and write `fts5_index_logs` and
    `meta_json` through the same configuration/override path used by the worker's
    normal source-row creation, because failed sources may never produce a batch.
    `source_progress.cursor` is the authoritative durable cursor for scan/tail
    restart; the older `sources.cursor` column is historical/deprecated and must
    not be revived as a competing cursor source. Lifecycle writes update only
    lifecycle/read-model columns and must not touch batch-progress fields:
    `last_seq`, `last_ts_us`, `cursor`, or `updated_at`. Adapter contract
    comments that still name `sources.cursor` must be corrected.
    Initial `RecordSourceLifecycle` insertion is the only `updated_at`
    exception: because `source_progress.updated_at` is already `NOT NULL`
    without a default, the insert must provide a deterministic value
    (`lifecycle_state_at`) so pre-submit lifecycle rows are insertable;
    subsequent lifecycle updates preserve existing batch-progress `updated_at`.
  - Pre-submit failure decision: every configured/discovered source gets a
    durable `sources` row and a `source_progress` row with
    `lifecycle_state='starting'` before adapter factory lookup, location stat,
    cursor parse, adapter construction, or `Submit`. Unknown format or missing
    source location transitions to `start_failed`; adapter construction failure
    and `Submit` failure transition to `construct_failed` unless the
    implementation plan justifies a retryable state with tests. These rows remain
    enabled and degraded so `/api/health` and `/api/sources` show the failed
    configured source.
  - Unconfigured-row reconciliation decision: startup lifecycle reconciliation
    compares durable `sources.id` values to the current configured/discovered
    source id set before source supervisors are considered healthy. Rows absent
    from the configured set are transitioned to `stopped` through
    `RecordSourceLifecycle`, preserving metadata/progress and emitting notify.
    This prevents historical rows for removed source locations from becoming
    false permanent health degradations.
  - Reconfigured-row reconciliation decision: if a row was previously moved to
    `stopped` because it was absent from the configured/discovered set, and the
    source id later appears again at daemon startup, the supervisor must write
    `stopped -> starting` and then run normal Scan/Tail startup. A previously
    stopped source must not remain quietly stopped when it is again configured.

- Preserve correctness of derived read models:
  - FTS and rollup backfill remain derived-data repair, not primary ingestion.
  - The global `deferReadModels` flag is an implicit all-source coupling. The
    implementation plan must not keep the current all-sources-scan-gated global
    flag. It must use per-source/per-worker gating or an equivalent mechanism
    that proves a stuck scan cannot suppress FTS/rollup refresh for unrelated
    tailing sources indefinitely.
  - Gap-level read-model decision: the current single global flag is replaced by
    per-source/per-worker read-model deferral state. When source A completes its
    scan, source A's scan-time events must become eligible for read-model repair
    independently of source B's scan state.
  - Per-source deferral mechanism: `Ingester` owns a sourceID-keyed map of
    `*atomic.Bool` deferral flags created before `Submit`. Each worker receives
    the pointer for its own source and reads it on every batch flush. The source
    lifecycle goroutine clears only its source flag after Scan outcome and
    before Tail starts. The captured pointer remains valid for the worker's
    lifetime; the map is not deleted while workers can still hold pointers, and
    any cleanup happens only after the source worker has stopped.
    The pointer is source-supervisor owned, not attempt-local: Tail restarts,
    catch-up Scans, or any worker respawn for the same source must reuse the
    same pointer until the supervisor closes the source channel and the worker
    is fully stopped.
  - Global rebuild-active mechanism: `Ingester` owns a separate
    `readModelRebuildActive atomic.Bool` shared by all workers. Workers skip
    only derived FTS/rollup refresh when either their per-source deferral flag
    or the global rebuild-active flag is true; they still commit canonical rows,
    aggregates, source progress, lifecycle/read-model state, and notify rows.
    The read-model repair coordinator owns setting and clearing
    `readModelRebuildActive` and owns the durable `repair_pending` transition for
    sources whose incremental derived refresh is suppressed by a global rebuild.
  - Existing `deferReadModels` fate: the old single global
    `Ingester.deferReadModels` field and `SetDeferReadModels` /
    `DeferReadModels` startup-scan semantics are retired for normal ingestion.
    They are replaced by the sourceID-keyed deferral map plus
    `readModelRebuildActive`. Existing call sites and tests that currently use
    `SetDeferReadModels(true)` for startup scan must be migrated to explicit
    per-source deferral setup; full rebuild paths must use
    `readModelRebuildActive`, not the old all-sources startup flag. If the
    implementation plan keeps the public methods temporarily for subcommand
    compatibility, it must document the narrowed semantics and ensure daemon
    startup no longer depends on them.
  - The `readModelBackfiller` interface and `startPostScanBackfill` helper must
    be redesigned or retired with the old global `DeferReadModels()` gate. A
    retained post-scan reconciliation trigger may wait for the startup
    scan-outcome barrier, but it must not decide whether to run from the old
    global flag, and its tests must be rewritten around per-source deferral and
    the repair coordinator.
  - Two-level deferral contract: a per-source deferral suppresses FTS/rollup
    refresh only for that source's startup Scan rows; a separate global
    read-model-rebuild-active state suppresses incremental FTS/rollup refresh
    for all workers only while a full truncate/rebuild reconciliation is
    actually running. Both deferrals still commit canonical rows, aggregates,
    source progress, lifecycle state, and notify rows. Deferred derived work is
    recorded as `read_model_state='repair_pending'`, not hidden in memory.
  - Per-source repair and global reconciliation must not interleave derived-table
    deletes/reinserts in a way that loses either repair. The ingester owns a
    read-model repair coordinator that serializes all derived-table repair jobs:
    per-source repair waits while full global reconciliation is active, and full
    global reconciliation waits for any in-flight per-source repair before it
    truncates/rebuilds derived tables. Waiting repairs remain durably
    `repair_pending`, and a completed global reconciliation may satisfy queued
    per-source repair only if it recomputes the same canonical scope and records
    the source read-model state transition.
    The existing `backfillMu` serialization point is replaced by the
    coordinator's single lock; the old field must not remain as a second
    independent serialization mechanism. `BackfillReadModels`, per-source
    repair and global reconciliation both enter through the coordinator so the
    implementation cannot accidentally serialize different repair paths with
    different locks.
  - Same-source repair and same-source Tail incremental refresh must not strand
    Tail-time derived rows. Source-scoped FTS repair deletes/reinserts rows by
    individual op/log id inside SQLite transactions, and rollup repair
    recomputes closed buckets through the same single-writer connection. Tail
    workers for the same source therefore continue incremental FTS/rollup
    refresh while source repair runs. A repair-active per-source deferral is
    forbidden unless it also captures a high-water mark and reruns repair for
    every canonical row committed while the deferral was active; this SOW uses
    the simpler no-repair-active-deferral model.
  - The source-level read-model repair trigger is mandatory: after source A's
    scan outcome, source A's per-source deferral is cleared before Tail starts,
    so Tail-time rows use the normal incremental FTS/rollup path. A background
    repair then rebuilds every derived row that could have been skipped while
    source A was deferred. The all-sources backfill may still run later as
    reconciliation, but it cannot be the only repair path.
  - Source-level repair scope:
    - FTS repair is source-scoped: delete/reinsert index rows for ops/logs that
      belong to the completed source.
    - Rollup repair is source-format/bucket scoped, not source-id scoped,
      because rollup primary keys aggregate all sources with the same format.
      For every closed hour/day touched by the completed source, recompute the
      bucket by reading all sessions for that `source_format`.
      If another source of the same format is still scanning or deferred, this
      recomputation is intentionally idempotent over the canonical rows visible
      at repair time; the later source repair or global reconciliation must
      converge to the same final rollup rows. The plan must test this
      cross-source same-format interaction.
    - The repair derives its scope from canonical rows already committed to the
      database; correctness must not depend on an in-memory dirty set surviving a
      crash.
  - `BackfillReadModels` currently clears `deferReadModels` before the FTS and
    rollup backfills succeed. That ordering is not acceptable under early Tail
    unless it is replaced by the two-level deferral contract above; global
    truncate/rebuild work must not silently interleave with incremental derived
    refresh without recording repair debt.
  - Tail workers do not run incremental FTS/rollup refresh while a full
    FTS/rollup backfill is truncating or rebuilding the derived tables. During a
    full rebuild, workers still commit canonical rows and mark derived repair
    debt with `read_model_state='repair_pending'`. The current
    `deferReadModels.Store(false)` at the start of `BackfillReadModels` is a
    specific risk that this SOW replaces.
  - Backfill deletes and rebuilds derived rows. Tail-mode worker flushes keep
    canonical ingestion moving, but derived FTS/rollup refresh is deferred while
    full rebuild state is active. FTS/rollups are eventually consistent after
    source repair or global reconciliation succeeds, and tests must pin this
    contract.
  - Gap-level consistency decision: canonical tables and source progress are
    immediately consistent. FTS/rollups are eventually consistent during startup
    scan deferral or full global rebuild. Any skipped derived refresh sets
    durable `read_model_state='repair_pending'`; successful source repair sets
    `ready`; caller/deadline timeout, when present, sets `repair_timeout`; other
    real repair failure sets `repair_failed`. The daemon source repair loop must
    not synthesize a short wall-clock deadline that restarts large repairs from
    the beginning. `repair_pending`, `repair_timeout`, and `repair_failed`
    degrade health until a retry succeeds.
  - The read-model state machine is explicit. Target states are `unknown`,
    `repair_pending`, `repairing`, `ready`, `repair_timeout`, and
    `repair_failed`. Legal transitions are:
    `unknown -> repair_pending | ready`;
    `ready -> repair_pending` when derived refresh is skipped by per-source
    startup deferral or global rebuild-active deferral;
    `repair_pending -> repairing`;
    `repairing -> ready | repair_timeout | repair_failed`;
    `repair_timeout -> repair_pending`;
    `repair_failed -> repair_pending`;
    and persisted `repairing` on process startup -> `repair_pending`. Parent
    shutdown leaves unfinished repair work as `repair_pending`, not `repairing`,
    so the next startup can resume it.
  - One-shot derived-table repair subcommands (`rollups-backfill` and
    `fts-content-backfill`) do not write lifecycle or `read_model_state`
    directly in this SOW. They hold the daemon lock and rebuild their target
    derived tables; the daemon re-evaluates and records read-model state on next
    startup or repair pass. The runbook must make this visible so an operator
    understands that `/api/health` reflects daemon-evaluated read-model state,
    not the direct result of a one-shot helper.
  - Error sanitization is part of the data contract for both `lifecycle_error`
    and `read_model_error`: store at most 1024 UTF-8 bytes without splitting a
    rune, strip control characters, and replace configured source locations,
    absolute home-directory prefixes, and home-directory shorthand path prefixes
    with placeholders before persisting or serving the string. Sanitization is
    applied by the ingester before writing either field to SQLite; presenter
    APIs serve the stored sanitized values and tests verify both stored and
    served output. Configured source locations are matched as case-sensitive
    path/string prefixes wherever they appear in the error text; otherwise the
    substring is preserved. Sanitization covers paths and configured source
    locations; adapter error producers must not include transcript content, user
    message text, credentials, or payload bytes in errors passed to
    lifecycle/read-model reporting.
  - The writer store pins `SetMaxOpenConns(1)` and applies
    `busy_timeout(5000)`. Tail flushes and read-model backfill therefore
    serialize through one writer connection and may wait behind truncate/rebuild
    transactions. The plan must cover long backfill windows, shutdown-drain
    timeouts, `replayRequired` paths, and the exact repair/retry behavior if a
    tail batch cannot flush while backfill is active.
  - Full read-model repair/reconciliation must not hold a single writer
    transaction across an unbounded FTS/rollup rebuild. The plan must define a
    WAL/checkpoint strategy, such as chunked repair transactions with
    checkpoints between chunks, and must prove tail canonical writes either
    flush within the configured busy timeout or record durable replay/repair
    debt instead of failing silently.
  - Repair transaction chunks need an explicit wall-clock bound, not only a row
    count. The implementation plan must choose the exact chunk target, but each
    derived-table write transaction should normally commit within 1-2 seconds so
    unrelated source tail flushes can interleave under the configured 5 s
    `busy_timeout`. If a chunk cannot meet that bound, the repair path must
    record retryable repair debt instead of starving canonical ingestion.
  - Superseded timeout decision: source repair attempts are not bounded by the
    global startup backfill timeout. The later live-regression fix rejected that
    design because a short synthetic deadline can restart large local repairs
    from the beginning forever. A failed repair schedules
    automatic retry with the same 1s, 2s, 4s, ... max 60s backoff family used
    for Tail restart, while the parent context is alive. Retry attempts
    increment `read_model_repair_attempts` and update the read-model timestamps
    and bounded/sanitized error field. The retry attempt counter resets to zero
    when the source transitions to `read_model_state='ready'`.
  - Read-model repair retry ownership decision: the source lifecycle goroutine
    owns that source's repair loop. It attempts repair after scan outcome, sets
    `repairing`, then `ready` on success or `repair_failed` on real repair
    error. `repair_timeout` remains a legal state for caller/deadline-driven
    failures, but the daemon does not create a short synthetic repair timeout.
    The loop schedules retries with a context-cancellable timer and leaves
    `repair_pending` durable across shutdown so next startup can resume repair.
    Read-model repair retry is owned by that source repair loop, not by a second
    global watchdog; the stale-tail watchdog remains dedicated to Tail heartbeat
    state.
    Active repair work must observe parent-context cancellation and commit or
    roll back the current bounded transaction promptly before recording durable
    `repair_pending` shutdown debt.
  - Full global reconciliation must not reintroduce the original defect class.
    Its trigger may be a completed-all-scans signal, a timer, or an explicit
    repair command, but it must not be the only way source-level read models
    become current. If it still waits for all scans, start/construct failures
    must release that barrier, and an indefinitely scanning source must be
    visible as degraded while per-source repairs continue for completed sources.
  - SOW-0063's backfill-vs-incremental parity tests are the existing evidence to
    reuse, but this SOW must add tests for the new tail-before-backfill window.
  - Backfill timeout/failure must set a durable derived-read-model state that
    degrades health and records the operator repair path. A truncate-then-failed
    FTS/rollup rebuild must never be invisible. Automatic retry with backoff is
    required; the operator repair path is either the existing read-model rebuild
    command when available or fresh installed DB rebuild when the installed
    state is suspect.
  - Existing batch UPSERTs into `sources` and `source_progress` must preserve
    lifecycle columns. Normal progress updates must not reset lifecycle state or
    transition timestamps.
  - No lifecycle-state indexes are required in migration 0012. The presenter
    enumerates configured source rows and left-joins `source_progress`; it does
    not filter by lifecycle/read-model state. If a future UI adds lifecycle
    filtering over many sources, that index is a separate SOW.

- Prove the regression cannot recur:
  - Add a `cmd/ai-viewer-ingest` lifecycle test with fake adapters: source A
    returns from `Scan()` and then emits Tail events; source B blocks in
    `Scan()`. The test must assert source A's `Tail()` starts and its events
    commit while source B is still scanning.
  - Add a scan-error / scan-deadline lifecycle test. If `Scan()` returns an
    error that should not stop the source, Tail must still be attempted and the
    lifecycle state/logs must reflect the scan outcome.
  - Add fatal scan-error lifecycle tests: source-root unreadable or fatal schema
    errors transition to terminal/degraded `scan_failed` and do not attempt
    Tail, while recoverable partial scan errors still fall through to Tail.
  - Add a tail-failure lifecycle test. If `Tail()` returns a non-context error,
    the lifecycle records the failure, health degrades, and the adapter is
    restarted with backoff instead of permanently exiting.
  - Add a tail-stale lifecycle test. If a Tail loop stops emitting real
    heartbeats without returning, the source transitions to `tail_stale` after
    5 minutes and `/api/health` degrades; a later heartbeat returns it to
    `tailing`.
  - Add heartbeat throttling tests: frequent adapter `OnTailHeartbeat` calls
    update in-memory liveness immediately but persist `tail_heartbeat_at` at
    most once per 30 s per source.
  - Add serve-side stale detection tests: if the ingester is down and persisted
    `lifecycle_state='tailing'` has stale `tail_heartbeat_at`, `/api/health`
    and `/api/sources` expose/degrade the source as effectively `tail_stale`
    without relying on the ingester watchdog.
  - Add real-adapter Tail heartbeat tests for aiagent_v2, aiagent_v3,
    Claude Code, Codex, and Opencode. Each adapter must call
    `AdapterOptions.OnTailHeartbeat` during an idle Tail interval and after an
    emitted Tail event.
  - Add presenter health tests for a source that scanned but never tailed, a
    long-running scan, and a healthy tail with no recent events.
  - Add explicit long-running scan health tests: a source whose Scan exceeds the
    chosen threshold appears degraded/stale in `/api/health` and `/api/sources`
    with elapsed scan evidence.
  - Add presenter health tests for no configured/discovered sources returning
    `status="degraded"` and `status_detail="no_sources_configured"`.
  - Add presenter health/source tests for the additive lifecycle/read-model JSON
    fields and `status_detail` omission when not needed.
  - Add read-model tests for tail events written before or during startup
    backfill, including the backfill-timeout or failed-backfill contract if that
    state remains possible.
  - Add a test where source A completes Scan and source B remains blocked, then
    source A's scan-time events become searchable/stat-able without waiting for
    source B.
  - Add read-model state tests for `repair_pending`, `repairing`, `ready`,
    `repair_timeout`, and `repair_failed`, including retry backoff and health
    degradation.
  - Add a global-reconciliation test proving a retained all-scans trigger cannot
    park forever on start/construct failure and is not the only repair path for
    completed sources.
  - Add adapter cursor-handoff tests for aiagent_v2 and aiagent_v3 so the
    immediate Tail startup window cannot skip appended records. The implemented
    package-local tests are both named `TestScanThenTail_NoLossInWindow`
    (`internal/adapters/aiagent_v2/tailer_test.go` and
    `internal/adapters/aiagent_v3/tailer_test.go`). The expected behavior is
    mandatory scan-cursor handoff, not a snapshot-on-tail proof.
  - Add a test for Tail restart after a non-context failure that proves the
    restarted Tail reloads the durable cursor, runs catch-up Scan, then resumes
    Tail without skipping records appended during the failure/backoff window.
  - Add a Tail restart catch-up Scan failure test: a catch-up Scan failure during
    `tail_restarting` records degraded error evidence, backs off, and retries
    without touching the startup scan barrier.
  - Add a Tail restart shutdown test: cancellation during restart backoff moves
    the source to `stopped` and shutdown completes within the bounded timeout.
  - Add tests for lifecycle transition persistence before any event batch flush
    and for notify/SSE emission on lifecycle transitions.
  - Add tests that lifecycle transition state and `source_status_changed` notify
    rows commit atomically, and that normal batch UPSERTs preserve lifecycle
    fields.
  - Add a test for a source that fails before Scan (`start_failed`) and is still
    visible in `/api/health` and `/api/sources`.
  - Add a pre-submit startup failure test: unknown adapter format, missing
    location, and adapter construction failure each create `sources` and
    `source_progress` rows before returning, with degraded lifecycle state and
    `source_status_changed` notify where applicable.
  - Add a starting-shutdown test: cancellation after pre-submit row creation but
    before Scan starts transitions `starting -> stopped`.
  - Add a `read_model_error` sanitization test covering 1024-byte truncation,
    control-character stripping, configured-source-location replacement,
    `$HOME` prefix replacement, and served API output.
  - Add migration tests for `0012_source_progress_lifecycle.sql`: pre-existing
    `source_progress` rows default to `lifecycle_state='unknown'` and
    `read_model_state='unknown'`, then transition to real states on first fixed
    ingester startup without claiming unobserved history.
  - Add configured-unknown startup test: seed migrated rows with
    `lifecycle_state='unknown'` and `lifecycle_state_at=0` for a source that is
    still configured/discovered, run startup through the pre-submit lifecycle
    path, and verify the first health-visible state is `starting` inside the
    60 s grace window rather than a false degraded `unknown`/epoch-age state.
  - Add startup reconciliation test: seed a durable `sources` row that is not in
    the current configured/discovered source set, run fixed-ingester startup
    reconciliation, verify the row transitions to `lifecycle_state='stopped'`,
    preserves source metadata/progress, emits `source_status_changed`, and does
    not make `/api/health` or `/api/sources` report degraded health.
  - Add startup reconfiguration test: seed a durable source row with
    `lifecycle_state='stopped'`, include that source id in the current
    configured/discovered source set, run startup, and verify the source moves
    through `stopped -> starting -> scanning` and continues to Tail/ingest
    according to normal startup behavior instead of remaining silently stopped.
  - Add presenter timestamp-sentinel tests: `lifecycle_state_at=0` and other
    zero lifecycle/read-model timestamps are treated as unset, not epoch, for
    age-based degradation; the explicit invalid `tailing` case with no
    heartbeat or tail-start timestamp still resolves to effective `tail_stale`.
  - Add same-failure searches so the fix is not Codex-only.

- Keep install/runtime recovery explicit:
  - After code is fixed, rebuild and install the application.
  - Restart the installed ingester safely through systemd; verify the daemon
    lock is not held by another process if startup fails.
  - Verify the previously missing Codex session appears in the installed DB
    using a local-only exact-id query. Durable SOW/spec text keeps the id
    redacted.
  - For the June 26-to-fix stall window, compare source-native inventories to
    canonical rows for every configured source: file/session identifiers for
    file-backed adapters and the adapter-native probe/counts for Opencode.
    Record any remaining omissions as defects before closing this SOW.
  - Verify lifecycle state and progress for every configured source after
    restart, not only Codex.
  - Gap-level recovery decision: after the code is fixed, rebuild the installed
    DB from scratch unless a same-failure inventory proves every source window is
    already complete. The observed Codex cursor marks the June 26 scan endpoint;
    catch-up Scan from that cursor should recover post-June-26 sessions, but
    repair by restart alone is not assumed until exact inventory verifies every
    source window. SOW closure is blocked until the post-install
    same-failure inventory/canonical reconciliation proves every configured
    source's stall-window is closed, including the originally missing Codex
    session id.
  - If no sources are configured/discovered, `/api/health` must surface that as
    degraded/misconfigured rather than only logging a startup warning.
  - Source discovery remains startup-scoped in this SOW. If the operator creates
    a new source location while `ai-viewer-ingest` is already running, the
    source is picked up on the next ingester restart; mid-run source
    re-discovery is out of scope and needs a separate SOW.

- Maintain project contracts:
  - Update `ingester.md`, `observability.md`, and `data-model.md` before
    tests/code. Update `adapter-contract.md` if scan timeout / promote-to-tail
    semantics change the adapter lifecycle contract.
  - Update `adapter-contract.md` for mandatory Scan-to-Tail cursor handoff even
    if no scan timeout is added; future adapters must carry the final Scan
    cursor into the immediately following Tail on the same instance. A future
    adapter-specific snapshot-on-tail exception requires its own SOW, proof, and
    regression tests.
  - Update `adapter-aiagent-v2.md` and `adapter-aiagent-v3.md` for the new
    scan-cursor handoff behavior. Update all five adapter specs
    (`adapter-aiagent-v2.md`, `adapter-aiagent-v3.md`,
    `adapter-claude-code.md`, `adapter-codex.md`, `adapter-opencode.md`) for
    the `AdapterOptions.OnTailHeartbeat` Tail-loop callback requirement.
  - Update `sse-protocol.md` to document that `source_status_changed` remains a
    minimal invalidation frame for lifecycle/read-model transitions and clients
    refetch details from REST.
  - Update `rest-api.md` for the expanded `/api/health` and `/api/sources`
    response shapes, including `status_detail` and lifecycle/read-model fields.
  - Update `presenter.md` for the expanded `/api/health` and `/api/sources`
    contracts.
  - Add migration `0012_source_progress_lifecycle.sql` and update
    `presenter.SchemaVersion` to `12` in lockstep.
  - Do not change the existing panic policy in this SOW. Adapter panics remain
    process-fatal per `ingester.md`; they are visible process failures, not the
    silent source-stall class this SOW fixes.
  - Keep source readers read-only.
  - Do not change adapter event semantics unless the tests prove the lifecycle
    fix requires it.
  - Keep changes isolated from pending UI SOW work, except for the minimal
    Sources/Ingest Errors update required by this health contract: the Sources
    view must show lifecycle/read-model status and must not present
    `last_seen_at` as a freshness indicator after the backend contract
    redefines freshness around lifecycle and heartbeat fields. `last_seen_at`
    may remain visible only as a clearly secondary legacy diagnostic field, not
    as the primary freshness signal. The Ingest Errors page and global layout
    health hints must stop deriving health from legacy lag/freshness fields
    where lifecycle/read-model fields supersede them.

## Pre-Implementation Gate

Status: Milestone 3 supervisor/watchdog, Milestone 5 adapter heartbeat/cursor
handoff, Milestone 4 read-model deferral/repair, and Milestone 6 presenter/UI
surfaces implemented with focused gates green; install/reingest and
implementation review remain pending before SOW closure.

Problem / root-cause model:

- Root cause: the ingester uses an all-sources scan barrier before tail startup.
  `main.go` waits for every source scan to finish before closing `scanDone`.
  `startPostScanBackfill` closes `backfillDone` only after `scanDone`, and
  `runAdapter` waits on `backfillDone` before calling `Tail()`. In the observed
  run, Codex completed `Scan()` but did not reach `Tail()` because other sources
  did not complete startup scan.

Evidence reviewed:

- Installed logs: Codex scan completed on June 26, but no Codex `tail starting`
  log followed.
- Installed database: requested Codex session id is absent; latest Codex
  sessions stop on June 26.
- Installed source progress: Codex progress stopped after the startup scan,
  while other sources continued changing.
- Code: `cmd/ai-viewer-ingest/main.go` owns the all-sources `scanWG` barrier;
  `cmd/ai-viewer-ingest/sources.go` waits on `backfillDone` before tailing.
- Code: `internal/ingest/ingester.go` stores `deferReadModels` as a single
  global atomic flag and clears it inside `BackfillReadModels`.
- Code: `internal/ingest/writer.go` updates `sources.last_seen_at` only while
  bumping source error counters.
- Code: `internal/ingest/worker.go` already commits
  `source_progress.updated_at` on source progress updates.
- Code: `internal/presenter/health.go` does not expose
  `source_progress.updated_at` or any scan/tail lifecycle state.
- Same-failure scan: installed source progress and canonical session maxima show
  staleness across aiagent_v3, Claude Code, Codex, aiagent_v2, and Opencode
  windows, while source-native inventories changed after June 26. This is a
  systemic installed-ingestion stale-data incident, not a Codex-only display
  issue.

Affected contracts and surfaces:

- `ai-viewer-ingest` source lifecycle.
- Adapter `Scan()` / `Tail()` sequencing.
- Source progress and `/api/health` freshness semantics.
- `/api/sources` source status semantics.
- Notify/SSE source status events.
- Structured logs for source lifecycle.
- Derived read models: FTS and rollups.
- System install / restart behavior after upgrade.
- Presenter health JSON contract.
- SQLite data model if durable lifecycle fields are added.

Existing patterns to reuse:

- Per-source adapter goroutine ownership in `startSource`.
- Context-aware adapter event channels.
- `SourceProgressEvent` as the durable source cursor/freshness update.
- Existing structured lifecycle logs.
- Existing `startPostScanBackfill` timeout pattern for bounded derived-data
  repair.
- Existing `source_progress` row as durable per-source progress evidence.
- Existing migration pattern for additive source metadata fields.
- Existing fake-adapter tests in `cmd/ai-viewer-ingest` for lifecycle seams.
- SOW-0063 parity tests for backfill-vs-incremental read-model correctness.
- Existing worker retry and `replayRequired` reporting paths for write failures.
- Existing exponential-backoff language in `ingester.md` failure recovery.
- Existing notify-table `source_status_changed` path for source status refreshes.

Risk and blast radius:

- Medium backend blast radius: all source adapters share this lifecycle.
- Low source-data risk: source readers remain read-only.
- Medium derived-data risk: FTS/rollup freshness may change during initial
  startup.
- Medium operational risk: systemd upgrade/restart affects the installed local
  service.
- High correctness risk if the plan only decouples completed sources and leaves
  an own-source stuck scan silent.
- High correctness risk if the plan decouples scan and tail startup but leaves
  tail failures as terminal source exits.
- High observability risk if health continues to rely on `last_seen_at` without
  durable lifecycle phase/progress fields.
- High observability risk if lifecycle state is written only during normal batch
  flushes, because slow/empty scans remain invisible until the first event batch.
- High read-model correctness risk if a stuck scan keeps a global
  `deferReadModels` flag true and suppresses FTS/rollups for every other source.
- Medium performance risk while tail flushes and read-model backfill contend for
  the single SQLite writer connection.
- Medium architecture risk if per-source deadlines are added without replacing
  the shared adapter context with per-source contexts.
- Medium schema risk if lifecycle fields are added to `source_progress` without
  updating all existing UPSERT paths to preserve them.
- Medium schema-planning risk: pending UI/data SOWs mention future migrations
  starting at `0012`; this current SOW must either take that slot and force
  pending SOW renumbering, or renumber itself if another migration lands first.
- Medium derived-data risk if FTS/rollup backfill failures remain log-only after
  truncate-then-rebuild repair starts.

Sensitive data handling plan:

- Durable SOW/spec/test artifacts must not include raw transcript content,
  prompts, tool output, credentials, or personal filesystem paths. Evidence is
  summarized with `$HOME` placeholders and source ids redacted where needed.

Implementation plan:

- Written in the `## Plan` section below. The plan executes this SOW as one
  reliability change with ordered milestones: specs, schema/lifecycle
  primitives, source supervision, read-model repair, adapter handoff/heartbeat,
  presenter/API/frontend exposure, and installed recovery. No runtime code is
  written until the implementation-plan reviewer gate returns
  `READY FOR IMPLEMENTATION`.

Spec Deltas:

- `.agents/sow/specs/ingester.md`
  - Update the startup lifecycle diagram and surrounding text that currently
    show all scans -> shared backfill -> Tail.
  - Change the lifecycle from all-sources `Scan()` -> shared backfill ->
    `Tail()` to per-source tail startup after that source's scan outcome.
  - Document the retained global backfill as background reconciliation, not a
    Tail-start gate, and prove its trigger cannot park forever on
    start/construct failure.
  - Update the notify-channel section so `source_status_changed` fires on
    lifecycle state transitions and read-model state transitions, not only
    parse-error/pricing-miss status changes.
  - Document that the source lifecycle goroutine uses deferred scan-barrier
    release for the initial Scan phase. Adapter panics remain process-fatal, but
    non-panic scan failures and early exits must not strand the startup barrier.
  - Document the canonical fatal Scan error marker and the default recoverable
    behavior for non-context Scan errors that do not wrap the marker.
  - Document `deferReadModels` semantics under early tail, including the rule
    that a stuck scan cannot permanently suppress read-model refresh for
    unrelated tailing sources.
  - Document that the old global `deferReadModels` startup-scan flag is retired
    and replaced by per-source deferral plus `readModelRebuildActive`.
  - Document per-source read-model repair after source scan completion plus any
    later all-sources reconciliation pass.
  - Document the two-level read-model deferral model: per-source scan deferral
    plus global rebuild-active deferral, both committing canonical rows while
    recording derived repair debt.
  - Document replacement or redesign of `readModelBackfiller` and
    `startPostScanBackfill` so global startup reconciliation no longer depends
    on `DeferReadModels()`.
  - Reconcile the existing Tail failure recovery spec with implementation:
    implement restart with bounded exponential backoff and durable cursor
    catch-up Scan before Tail.
  - Document Tail stale heartbeat semantics: adapters heartbeat from inside the
    real Tail loop via `AdapterOptions.OnTailHeartbeat`; missing heartbeat for
    5 minutes degrades health.
  - Document that adapter heartbeat callbacks are throttled by the ingester
    before SQLite persistence: at most one `tail_heartbeat_at` write per source
    per 30 s.
  - Document source supervisor ownership of restart loops, event-channel
    closure, per-source/per-attempt contexts, stale Tail cancellation, and
    catch-up Scan retry.
  - Document restart-attempt factory-construction failures as retryable
    `tail_restarting` failures that share the Tail/catch-up Scan counter,
    backoff, lifecycle evidence, and sanitization path.
  - Document guarded lifecycle transitions so watchdog writes cannot overwrite
    newer supervisor failure/restart/shutdown states.
  - Document that Tail-restart catch-up Scan does not participate in the startup
    scan barrier or post-scan global backfill gate, and that catch-up Scan
    failure remains in `tail_restarting` with degraded retry evidence.
  - Document that Tail restart always constructs a fresh adapter instance before
    catch-up Scan and emits bounded catch-up Scan progress logs while in
    `tail_restarting`.
  - Document that sustained Tail restart failure records `lifecycle_error`
    escalation evidence while automatic retry continues.
  - Document shutdown ordering when tail and backfill overlap. If a source
    supervisor ignores cancellation past the existing adapter-context grace
    period and the store has already closed, the final lifecycle write is
    best-effort; startup residue recovery must convert stale `tailing`,
    `tail_restarting`, or `scanning` states back to `starting` on the next run
    instead of blocking store close indefinitely.
  - Document startup reconciliation for durable `sources` rows that are no
    longer configured/discovered: they transition to non-degrading `stopped`
    with notify, rather than staying as migrated `unknown` rows forever.
  - Document the symmetric reconfiguration path: a durable row in `stopped`
    transitions to `starting` when the source is again configured/discovered on
    daemon startup.
  - Document configured migrated `unknown` startup ordering: configured sources
    are moved to `starting` by pre-submit lifecycle writes before adapter
    construction, so health does not age timestamp `0` as epoch during startup.
- `.agents/sow/specs/observability.md`
  - Add the per-source lifecycle fields exposed by `/api/health`.
  - Add read-model repair fields exposed by `/api/health`.
  - Re-anchor source lag/degraded rules to healthy progress/lifecycle state
    instead of error-only `last_seen_at`.
  - Document `last_seen_at` and `lag_us` as legacy error/pricing-miss fields,
    not freshness fields, and point freshness/degraded decisions at lifecycle
    phase timestamps and Tail heartbeat.
  - Update degraded-status rules for stale pre-tail and long-running scan
    states.
  - Add required structured log moments for scan/tail/backfill state changes.
  - Add degraded rules for start failure, long scan, scan-complete-not-tailing,
    tail failure, stale tail heartbeat, read-model repair pending/timeout/failure,
    and no configured/discovered sources.
  - Pin thresholds: long scan 10 minutes, pre-tail stale 60 seconds, stale tail
    heartbeat 5 minutes, restart/repair backoff 1s doubling to a 60s cap, and
    single long `tail_restarting` catch-up Scan degradation at 10 minutes.
    Source read-model repair attempts intentionally have no short synthetic
    execution timeout.
  - Document that those thresholds and clocks are injectable in tests while
    production defaults remain unchanged.
  - Document `starting` degradation and ingester-side first-heartbeat grace based
    on `tail_started_at`, matching serve-side stale-tail computation.
  - Document top-level `status_detail` on `/api/health`, including
    `status_detail="no_sources_configured"` for the no-sources degraded state.
  - Document serve-side effective stale-tail computation so health degrades when
    the ingester is down and persisted `tail_heartbeat_at` is stale, including
    the state filter and NULL timestamp semantics.
  - Document that `stopped` rows are non-degrading and that `0` lifecycle
    timestamps are unset sentinels, not epoch values, so migrated historical
    rows cannot create permanent false degraded health.
  - Document that one-shot derived-table repair helpers leave read-model health
    to daemon evaluation; successful helper execution alone does not rewrite
    `read_model_state`.
  - Document that overall health remains `degraded`, not `down`, when all
    sources are degraded but DB/serve are available.
- `.agents/sow/specs/data-model.md`
  - Add lifecycle/progress fields on `source_progress`.
  - Add read-model repair state fields on `source_progress`.
  - Add `lifecycle_error`, CHECK constraints for lifecycle/read-model enum
    values, and the `source_progress.cursor` authoritative-cursor rule.
  - Update the stale `## Schema (v1)` header while editing this file so the
    schema overview does not conflict with the new migration-12 contract.
  - Document conditional lifecycle transition writes and startup crash recovery
    for stale `read_model_state='repairing'` rows.
  - Document that `sources.cursor` remains as a nullable historical/deprecated
    column in this SOW and is neither dropped nor used as a cursor source.
  - Document the `RecordSourceLifecycle` initial-insert exception for
    `source_progress.updated_at`: initial lifecycle rows must provide a
    deterministic value for the existing NOT NULL column, while later lifecycle
    updates preserve batch-progress `updated_at`.
  - Add the migration/version-bump contract, including defaults for existing
    rows and eager lifecycle row creation at scan start.
  - Fix existing schema-version drift in the spec while editing it: update the
    stale `schema_meta` example from version `8` to `12` and backfill migration
    history entries for 0009, 0010, and 0011 before adding 0012.
  - Document that lifecycle transition notify rows are committed in the same
    transaction as the lifecycle state they describe.
  - Document `lifecycle_error` and `read_model_error` sanitization: 1024-byte
    UTF-8 boundary cap, control-character stripping, configured-path and
    home-directory placeholder replacement, and the boundary that adapter error
    producers must not include transcript/user/payload/credential content.
  - Document that no lifecycle/read-model-state indexes are added in migration
    0012 because presenter enumerates configured sources instead of filtering by
    state.
  - Document startup reconciliation for existing `sources` rows not present in
    the current configured/discovered source set: transition them to `stopped`,
    preserve metadata/progress, and emit `source_status_changed` in the same
    transaction.
  - Document `stopped -> starting` startup reconciliation for durable rows that
    are reconfigured/re-discovered after a prior stopped state.
  - Document configured migrated `unknown` rows moving to `starting` with a fresh
    lifecycle timestamp through the pre-submit lifecycle path before adapter
    construction.
  - Document that `lifecycle_state_at=0` and other zero lifecycle/read-model
    timestamps mean unset. The presenter and watchdog must not interpret them
    as epoch timestamps for age-based degradation.
  - Document nullable JSON timestamp semantics for API responses so internal
    zero sentinels do not render as epoch timestamps.
  - Add migration `0012_source_progress_lifecycle.sql` and bump
    `presenter.SchemaVersion` to `12`.
- `.agents/sow/specs/adapter-contract.md`
  - Require Scan-to-Tail cursor handoff on the same adapter instance. This SOW
    keeps the adapter interface unchanged and requires aiagent_v2 and
    aiagent_v3 to adopt the existing scan-cursor pattern.
  - Document Tail restart cursor semantics: restart resumes from durable cursor
    state by running catch-up Scan before Tail, not a fresh snapshot.
  - Document `AdapterOptions.OnTailHeartbeat`, the nil-safe canonical helper
    adapters must call, and the Tail heartbeat callback requirement for
    adapters. Zero-value `AdapterOptions` must not panic when Tail code emits a
    heartbeat.
  - Document that heartbeat state shared between adapter Tail goroutines and the
    watchdog must be race-detector clean, using atomic or equivalent synchronized
    per-source state.
  - Correct stale adapter-contract comments that still refer to `sources.cursor`
    as the persisted cursor; `source_progress.cursor` is authoritative.
- `.agents/sow/specs/canonical-events.md`
  - Correct `SourceProgressEvent` cursor persistence text from `sources.cursor`
    to `source_progress.cursor`.
- `.agents/sow/specs/adapter-aiagent-v2.md`
  - Replace snapshot-on-tail startup semantics with Scan-to-Tail cursor handoff
    plus Tail-start catch-up reread from the scan cursor.
  - Document nil-safe `AdapterOptions` heartbeat helper calls from the Tail loop.
  - Document that Scan and Tail must observe context cancellation promptly.
  - Correct cursor persistence text from `sources.cursor` to
    `source_progress.cursor`.
- `.agents/sow/specs/adapter-aiagent-v3.md`
  - Replace snapshot-on-tail startup semantics with Scan-to-Tail cursor handoff
    plus Tail-start catch-up reread from the scan cursor.
  - Document nil-safe `AdapterOptions` heartbeat helper calls from the Tail loop.
  - Document that Scan and Tail must observe context cancellation promptly.
  - Correct cursor persistence text from `sources.cursor` to
    `source_progress.cursor`.
- `.agents/sow/specs/adapter-claude-code.md`,
  `.agents/sow/specs/adapter-codex.md`, and
  `.agents/sow/specs/adapter-opencode.md`
  - Document nil-safe `AdapterOptions` heartbeat helper calls from each Tail loop.
  - Document that Scan and Tail must observe context cancellation promptly.
  - Correct cursor persistence text from `sources.cursor` to
    `source_progress.cursor`.
- `.agents/sow/specs/adapter-opencode.md`
  - Pin the restart catch-up invariant: after a successful catch-up `Scan()` on
    a fresh adapter instance, Opencode must have a non-nil scan cursor so the
    following `Tail()` takes the warm-start path and does not skip or duplicate
    the boundary bucket.
- `.agents/sow/specs/sse-protocol.md`
  - Document that `source_status_changed` also invalidates lifecycle and
    read-model state while keeping the existing minimal `{source_id, ts}`
    payload.
- `.agents/sow/specs/rest-api.md`
  - Update `/api/health` and `/api/sources` response examples for
    `status_detail` and lifecycle/progress/read-model fields.
  - Fix existing `/api/health` example schema-version drift from `5` to `12`
    while adding the new fields.
- `.agents/sow/specs/presenter.md`
  - Update `/api/health` and `/api/sources` source status contracts for
    lifecycle/progress/read-model fields and `status_detail` for no sources.
  - Record `presenter.SchemaVersion = 12`.
- `.agents/sow/specs/ui-pages.md`
  - Update the `/sources` page contract so it displays lifecycle/read-model
    status and no longer presents `last_seen_at` as source freshness.
- `docs/runbook.md`
  - Add a short recovery note for stale source lifecycle, installed restart,
    daemon lock diagnostics, and DB verification.
  - Document that one-shot derived-table repair helpers do not themselves write
    `read_model_state`; daemon startup/repair re-evaluates read-model health.
  - Document migration-aware upgrade ordering for schema-changing releases:
    stop/upgrade services, start `ai-viewer-ingest` first so it runs migrations
    and bumps `schema_meta`, then start `ai-viewer-serve`, which gates startup
    on `presenter.SchemaVersion`. Include the exact serve schema-mismatch
    symptom, the likely cause when migration 0012 fails or ingester did not run,
    and the resolution path using `journalctl -u ai-viewer-ingest` plus
    ingester-first restart.
  - If `scripts/install-system.sh` or another install helper controls service
    restart ordering, update it or document why the existing ordering is already
    ingester-before-serve for schema-changing releases.
- Go source comments
  - Correct stale cursor persistence comments in `internal/canonical` and the
    adapter cursor files so code comments match the authoritative
    `source_progress.cursor` contract.
  - Correct the stale schema-version comment in
    `internal/presenter/presenter.go` so it no longer says migration 0008 /
    version 8 is the latest after this SOW bumps the schema to 12.

Validation plan:

- `cmd/ai-viewer-ingest` lifecycle test: one fake adapter completes scan and
  tails while a second fake adapter remains blocked in scan. Planned test name:
  `TestLifecycle_PerSourceTailBeforeOtherScanComplete` unless a better local
  naming pattern appears during implementation.
- `cmd/ai-viewer-ingest` lifecycle test: scan error or scan local deadline path
  records lifecycle state and attempts Tail when the parent context is still
  alive.
- `cmd/ai-viewer-ingest` lifecycle test: fatal scan errors keep the source in
  terminal/degraded `scan_failed` and do not attempt Tail; recoverable scan
  errors still fall through to Tail. The fake adapter must exercise the
  canonical `FatalScanError` marker path and the default recoverable non-context
  error path.
- `cmd/ai-viewer-ingest` lifecycle test: non-context Tail failure records
  lifecycle failure, increments visible error state, and restarts Tail with
  bounded backoff.
- `cmd/ai-viewer-ingest` lifecycle test: Tail heartbeat absence transitions a
  tailing source to `tail_stale`, degrades health, and a later real heartbeat
  returns it to `tailing`.
- `cmd/ai-viewer-ingest` lifecycle test: a source that just entered `tailing`
  with no in-process heartbeat yet and stale/NULL durable `tail_heartbeat_at`
  stays healthy inside the first-heartbeat grace window based on
  `tail_started_at`, then degrades only after the stale threshold elapses without
  a heartbeat.
- `cmd/ai-viewer-ingest` lifecycle test: a restarted Tail attempt updates
  `tail_started_at` to the new attempt start time, so stale-tail detection does
  not reuse the prior attempt's expired first-heartbeat grace window.
- `cmd/ai-viewer-ingest` lifecycle test: stale Tail triggers source-supervisor
  restart by cancelling only that source's per-attempt context; the supervisor
  does not start a duplicate Tail while the old Tail goroutine is running.
- `cmd/ai-viewer-ingest` lifecycle race tests: guarded lifecycle transitions
  prevent a late watchdog `tail_stale` write from overwriting
  `tail_restarting`/`tail_failed`/`stopped`, and prevent a late watchdog
  recovery-to-`tailing` write from overwriting `tail_failed`.
- `cmd/ai-viewer-ingest` lifecycle test: stale-tail watchdog restart request is
  delivered through the registered per-source restart channel, coalesces repeated
  watchdog ticks, and unregisters cleanly on source shutdown.
- `cmd/ai-viewer-ingest` lifecycle test: a Tail that ignores cancellation after
  stale detection transitions to terminal/degraded `tail_failed` with sanitized
  `lifecycle_error` evidence instead of launching a second Tail.
- `cmd/ai-viewer-ingest` heartbeat throttling test: frequent
  `OnTailHeartbeat` calls persist `tail_heartbeat_at` at most once per 30 s per
  source.
- `cmd/ai-viewer-ingest` heartbeat throttling test: ingester restart resets the
  in-memory throttle, so the first heartbeat after restart may persist
  immediately and subsequent heartbeats are throttled again.
- `cmd/ai-viewer-ingest` heartbeat failure test: a throttled heartbeat SQLite
  write failure caused by writer contention does not transition a source to
  `tail_stale` while the in-memory heartbeat is fresh, and persistence retries on
  the next throttle window.
- `cmd/ai-viewer-ingest` race test: adapter `OnTailHeartbeat` callbacks,
  heartbeat throttling, attempt cancellation, and watchdog reads run concurrently
  under `go test -race` without a data race.
- `internal/presenter` stale-ingester tests: stale persisted
  `tail_heartbeat_at` degrades/effectively marks sources `tail_stale` even when
  the ingester watchdog is down.
- `internal/presenter` stale-ingester tests: effective `tail_stale` applies only
  to persisted `tailing`; stale/NULL heartbeat timestamps on `tail_failed`,
  `scan_failed`, `tail_starting`, `scanning`, `stopped`, and `unknown` do not
  get rewritten to `tail_stale`.
- `internal/presenter` stale-ingester tests: `tailing` with NULL
  `tail_heartbeat_at` uses `tail_started_at` as grace; `tailing` with both
  timestamps NULL/zero is effectively `tail_stale`.
- `internal/presenter` health tests: all configured sources degraded by
  lifecycle state still produce overall `status="degraded"`, not `down`, while
  DB/serve are available.
- Adapter Tail heartbeat tests: aiagent_v2, aiagent_v3, Claude Code, Codex, and
  Opencode call `AdapterOptions.OnTailHeartbeat` during idle Tail and after
  emitted Tail events.
- Adapter Tail heartbeat nil-safety tests: zero-value `canonical.AdapterOptions`
  and test-created adapters that omit `OnTailHeartbeat` do not panic in Tail
  loops because adapters call the canonical nil-safe heartbeat helper instead of
  the raw function field.
- Adapter Tail cancellation tests: aiagent_v2, aiagent_v3, Claude Code, Codex,
  and Opencode Tail loops return promptly on context cancellation so stale Tail
  restart can recover without process restart.
- Adapter Scan cancellation tests: aiagent_v2, aiagent_v3, Claude Code, Codex,
  and Opencode Scan loops return promptly on context cancellation so startup,
  restart catch-up Scan, and shutdown remain bounded.
- `cmd/ai-viewer-ingest` or adapter tests: Tail restart after failure reuses the
  durable recorded cursor by running catch-up Scan before Tail, and does not
  skip or duplicate records appended during backoff.
- `cmd/ai-viewer-ingest` or adapter tests: Tail restart constructs a fresh
  adapter instance through the factory before catch-up Scan and Tail, instead of
  reusing a failed instance with possibly stale watcher/connection state.
- `cmd/ai-viewer-ingest` lifecycle test: factory or adapter construction failure
  during `tail_restarting` records sanitized lifecycle error evidence,
  increments the same `tail_restart_count`, applies the same backoff, and
  retries without closing the source event channel.
- `cmd/ai-viewer-ingest` or adapter tests: Tail restart catch-up Scan emits
  structured progress logs while in `tail_restarting`, including elapsed time and
  event/cursor progress evidence where available.
- `cmd/ai-viewer-ingest` or adapter tests: successful Tail-restart catch-up
  Scan rows use tail-time read-model behavior and become searchable/stat-able
  unless `readModelRebuildActive` is true.
- `cmd/ai-viewer-ingest` lifecycle test: Tail-restart cursor reload uses the
  same `source_progress.cursor` lookup path as normal startup.
- `cmd/ai-viewer-ingest` or adapter test: Opencode Tail restart catch-up Scan
  populates the fresh adapter instance's scan cursor so the subsequent Tail uses
  the warm-start path, and the boundary bucket is neither skipped nor duplicated.
- `cmd/ai-viewer-ingest` lifecycle test: Tail-restart catch-up Scan failure
  remains in `tail_restarting`, records degraded evidence, retries with backoff,
  and does not touch the startup scan barrier.
- `cmd/ai-viewer-ingest` lifecycle/clock test: a single
  `tail_restarting` catch-up Scan that exceeds the 10-minute long-restart
  threshold degrades health with elapsed catch-up evidence before repeated
  restart-failure escalation is required.
- `cmd/ai-viewer-ingest` lifecycle test: shutdown during Tail-restart backoff
  transitions to `stopped` and completes within the bounded timeout.
- `cmd/ai-viewer-ingest` lifecycle test: sustained Tail restart failure beyond
  the documented threshold records degraded `lifecycle_error` escalation
  evidence while the retry loop remains context-cancellable, and the consecutive
  failure counter resets after a successful `tail_starting -> tailing`.
- `cmd/ai-viewer-ingest` lifecycle/clock test: repeated restart failure over
  the escalation threshold (at least 100 simulated cycles, without waiting real
  time) reaches the documented escalation evidence and does not starve unrelated
  source flushes or writer lifecycle updates.
- `cmd/ai-viewer-ingest` lifecycle test: rapid Tail failure cycling over at
  least three consecutive failures verifies the 1s/2s/4s restart backoff delays
  each retry, avoids tight-loop lifecycle writes, keeps writer contention within
  the documented bounds, and still advances escalation counters.
- `cmd/ai-viewer-ingest` lifecycle/clock tests: long scan, pre-tail stale,
  heartbeat stale, repair timeout, restart backoff, and restart escalation use
  injectable clocks/thresholds so tests do not wait real production durations.
- `cmd/ai-viewer-ingest` lifecycle test: full-process restart or simulated
  startup reattachment transitions sources persisted as `tailing`,
  `tail_restarting`, or `scanning` through `starting` and back into Scan/Tail
  instead of trusting stale persisted state.
- `cmd/ai-viewer-ingest` lifecycle shutdown tests: parent-context shutdown writes
  `stopped` from representative active states, including `tailing` and
  `tail_restarting`, not only `starting`.
- `internal/presenter` health tests: scanned-not-tailed degrades with lifecycle
  evidence; long-running scan is visible; healthy tail with no recent events is
  distinguishable from pre-tail stall.
- `internal/presenter` health tests: `unknown`, `starting`,
  `scan_complete`, and `tail_starting` beyond the configured threshold degrade
  with retry/lifecycle evidence.
- `internal/presenter` health tests: `construct_failed` and fatal
  `scan_failed` degrade immediately, like `start_failed` and `tail_failed`.
- `internal/presenter` health tests: no configured/discovered sources returns
  `status="degraded"` and `status_detail="no_sources_configured"`.
- `internal/presenter` health/source tests: expanded lifecycle/read-model JSON
  fields are present in `/api/health` and `/api/sources`, and `status_detail` is
  omitted when no detail applies.
- `internal/presenter` source/SSE tests: lifecycle transitions are visible via
  `/api/health`, `/api/sources` where applicable, and source-status notify/SSE
  events.
- `internal/presenter` reconnect/source-status test: serve refetches
  `/api/health` and `/api/sources` on startup/reconnect and does not depend on
  retained `source_status_changed` rows that may have been pruned.
- `internal/ingest` read-model tests: tail events written before or during
  startup backfill obey the chosen FTS/rollup consistency contract.
- `internal/ingest` read-model tests: source A's scan-time events get FTS/rollup
  repair after source A's scan completes even while source B is still scanning.
- `internal/ingest` read-model tests: source A Tail rows committed while source
  A's own FTS repair is deleting/reinserting derived rows are not lost; Tail
  incremental refresh remains enabled during source-scoped repair so rows are
  not stranded behind an unobserved repair-active deferral.
- `internal/ingest` read-model tests: source A repair for format X while source
  B of the same format is still scanning/deferred produces rollup rows that
  converge byte-for-byte with a later full reconciliation.
- `internal/ingest` read-model tests: existing
  `SetDeferReadModels`/`DeferReadModels` startup-scan call sites are retired or
  migrated, per-source deferral pointers remain valid for worker lifetime, and
  `readModelRebuildActive` suppresses only derived refresh while canonical rows
  continue committing.
- `internal/ingest` read-model tests: legal `read_model_state` transitions are
  enforced for `unknown`, `repair_pending`, `repairing`, `ready`,
  `repair_timeout`, and `repair_failed`; persisted `repairing` converts to
  `repair_pending` on startup; invalid transitions are rejected or logged as
  guarded no-ops.
- `internal/ingest` read-model tests: backfill timeout/failure is visible as a
  derived-read-model health state and is retryable/repairable.
- `internal/ingest` read-model restart test: a persisted
  `read_model_state='repairing'` at process start is converted to
  `repair_pending` and retried, instead of remaining stuck in an in-progress
  state with no running repair.
- `internal/ingest` read-model cancellation test: active repair work observes
  parent-context cancellation, exits bounded transactions promptly, and leaves
  durable `repair_pending` debt for startup retry.
- `internal/ingest` read-model tests: `read_model_repair_attempts` resets to zero
  when repair transitions to `ready`.
- `internal/ingest` read-model tests: full global reconciliation does not block
  canonical tail ingestion and does not remain the only repair path for a source
  that already completed Scan.
- `internal/ingest` read-model tests: per-source repair and global
  reconciliation serialize through the repair coordinator, so a global truncate
  cannot erase a concurrently completed per-source repair and queued
  `repair_pending` work is either satisfied by the global pass or runs after it.
- `internal/ingest` read-model tests: `backfillMu` or its successor remains the
  single repair/rebuild serialization point, so `BackfillReadModels`,
  per-source repair, and global reconciliation cannot use independent locks.
- `internal/ingest` read-model/WAL tests: full repair/reconciliation is chunked
  or otherwise bounded so tail canonical writes either flush under the busy
  timeout or record replay/repair debt without silent data loss.
- `internal/ingest` read-model/WAL tests: derived-table repair transactions
  commit within the chosen wall-clock chunk target under test clock/control, or
  record retryable repair debt instead of starving unrelated tail flushes.
- `internal/ingest` read-model/WAL tests: repair chunks use an internal
  deadline/timeout mechanism so a chunk that exceeds the 1-2 s target aborts or
  reschedules retryable debt instead of holding the writer past the configured
  busy timeout.
- `internal/ingest` writer tests: `sources`/`source_progress` UPSERTs preserve
  lifecycle columns during normal batch progress updates.
- `internal/ingest` writer tests: lifecycle UPSERTs preserve `sources.meta_json`,
  `fts5_index_logs`, `created_at`, existing source configuration fields, and
  `source_progress.last_seq`, `source_progress.last_ts_us`,
  `source_progress.cursor`, and `source_progress.updated_at`.
- `internal/ingest` writer test:
  `TestRecordSourceLifecycle_InitialInsertSetsUpdatedAt` proves a pre-submit
  lifecycle insert creates a `source_progress` row despite the existing
  `updated_at INTEGER NOT NULL` column, and later lifecycle updates preserve the
  batch-progress value.
- `internal/ingest` writer tests: worker batch UPSERTs preserve seeded
  lifecycle columns on both cursor and no-cursor progress update paths.
- `internal/ingest` writer tests: lifecycle/read-model transitions insert
  `source_status_changed` rows directly in the lifecycle transaction,
  independent of the batch writer's parse-error `sourceStatusChanged` gate.
- Adapter cursor handoff tests: aiagent_v2 and aiagent_v3 adopt the same
  scan-cursor handoff pattern as Codex, Claude Code, and Opencode and cannot
  skip or duplicate records appended between Scan and Tail. The tests must pin
  that `Scan(since, out)` respects the persisted cursor for catch-up behavior
  before Tail startup.
- Adapter cursor handoff implemented test names:
  `internal/adapters/aiagent_v2/tailer_test.go::TestScanThenTail_NoLossInWindow`
  and
  `internal/adapters/aiagent_v3/tailer_test.go::TestScanThenTail_NoLossInWindow`.
- Startup failure tests: unknown adapter format, missing location, and adapter
  construction or `Submit` failure create `sources` and `source_progress` rows
  before returning, with degraded lifecycle state.
- Startup failure test: `TestLifecycle_StartFailedReleasesScanBarrier` proves
  `start_failed` and `construct_failed` sources release the startup scan-outcome
  barrier so background reconciliation cannot park forever.
- Startup failure tests: pre-submit `RecordSourceLifecycle` resolves
  `fts5_index_logs` and `meta_json` through the same source configuration path as
  normal worker source-row creation, even when no batch ever flushes.
- Startup failure test: transient failure in the pre-submit
  `RecordSourceLifecycle(... starting ...)` write retries with bounded backoff
  and does not construct/start the adapter until the `starting` row commits.
- Startup shutdown test: cancellation after pre-submit row creation but before
  Scan starts transitions `starting -> stopped`.
- `read_model_error` sanitization test: truncation, control-character stripping,
  configured-source-location replacement, `$HOME` prefix replacement, and served
  API output are verified.
- `lifecycle_error` sanitization test: the same sanitization boundary as
  `read_model_error`, including UTF-8 boundary truncation and home-directory
  shorthand replacement, is verified in stored SQLite values and served API
  output.
- Sanitization test: configured source locations are replaced as
  case-sensitive prefixes when they appear inside error text.
- Sanitization audit/test: adapter error-producing paths in all five adapters are
  checked so lifecycle/read-model errors do not embed transcript content, user
  messages, credentials, tool payloads, or response bodies before sanitizer input.
- `internal/store` migration test: migration 0012 adds lifecycle/read-model
  columns with `unknown` defaults for existing rows, enum CHECK constraints,
  nullable heartbeat/error fields with documented NULL semantics, and bumps the
  schema version to 12.
- `internal/store` schema contract test: update
  `internal/store/schema_contract_test.go` so the pinned `source_progress`
  column set includes every lifecycle/read-model column, NOT NULL/default
  contract, enum CHECK constraint, nullable timestamp/error field, and primary
  key shape added by migration 0012.
- `internal/store` version tests: update `internal/store/store_test.go`
  schema-version assertions and failure messages to version 12 so stale
  hardcoded values cannot hide the migration bump.
  This includes the stale "migration 0008/version 8 is latest" comment and the
  `expectedMigrations` constant. Refresh touched
  `internal/store/schema_contract_test.go` "v1 schema" comments while updating
  the `source_progress` contract.
- `internal/store` or ingest migration test: `sources.cursor` remains a
  nullable historical column, is not read for restart, and is not written by new
  lifecycle/source-progress paths.
- Spec-drift check: `rest-api.md` and `data-model.md` schema-version examples
  are updated to version 12, and data-model migration history includes 0009,
  0010, 0011, and 0012.
- Minimal Sources UI check: the Sources view displays lifecycle/read-model state
  and does not present `last_seen_at` as freshness after the new API fields
  exist.
- Frontend tests: add Vitest coverage for Sources rendering of
  lifecycle/read-model state and a Playwright source-status check covering
  `scanning`, `tailing`, and `tail_stale` when practical.
- Frontend contract tests: update `frontend/src/api/types.ts` and
  `frontend/src/api/types.contract.test.ts` for the additive lifecycle/read-model
  fields; existing `last_seen_at`/`lag_us` fixture coverage remains for backward
  compatibility, but Sources UI freshness assertions move to lifecycle fields.
- Startup-scan deferral migration tests include `internal/paritycheck/check.go`
  and `internal/paritycheck/opencode_sample.go`, because both currently call the
  old `SetDeferReadModels(true)` startup-scan API.
- Startup-scan deferral migration tests cover the replacement or retirement of
  `readModelBackfiller.DeferReadModels()` and the existing
  `startPostScanBackfill` tests, proving no Tail-start or repair trigger still
  depends on the old global startup-scan flag.
- Pre-plan adapter error-producer audit: before the implementation plan gate,
  enumerate every adapter error path that reaches `OnError`, Scan/Tail return
  errors, lifecycle errors, or read-model errors across all five adapters and
  classify it as safe or needing redaction. The sanitizer tests are not enough
  unless this audit proves transcript/user/payload/credential content is not
  embedded before sanitizer input.
- Installed recovery check: per-source inventory/count comparison over the
  June 26-to-fix window plus exact verification for the originally missing
  Codex session.
- Installed recovery check: same-failure scan runs before implementation plan
  closure and after install; results are recorded in this SOW.
- Focused package tests after implementation, then full local gates before
  implementation review.

Artifact impact plan:

- AGENTS.md: likely unaffected; this is a runtime lifecycle defect, not a
  process-rule change.
- Runtime project skills: likely unaffected unless a new lifecycle pattern must
  be captured.
- Specs: update `ingester.md`, `observability.md`, `data-model.md`,
  `adapter-contract.md`, `canonical-events.md`, `sse-protocol.md`,
  `rest-api.md`, adapter specs, `presenter.md`, and `ui-pages.md` as listed in
  Spec Deltas.
- Frontend: minimal Sources view/status rendering update so lifecycle and
  read-model states are visible and `last_seen_at` is no longer presented as
  freshness; update `frontend/src/api/types.ts`,
  `frontend/src/api/types.contract.test.ts`, and existing Sources fixtures for
  the additive API fields.
- End-user/operator docs: update runbook/deployment docs for stale lifecycle
  diagnosis and installed recovery. Update install scripts if they own service
  restart ordering for schema-changing upgrades.
- End-user/operator skills: likely unaffected.
- SOW lifecycle: keep this SOW in `current/` until code, install, verification,
  and review are complete.

Open-source reference evidence:

- Not used for the gap analysis. The issue is in project-local lifecycle
  orchestration, not a foreign source-format interpretation.

Open decisions:

- None for the user. This is a long-term-best reliability fix: remove the
  all-sources liveness coupling and add automated regression coverage.

## Implications And Decisions

1. Long-term-best decision: fix the ingestion lifecycle, not only restart or
   re-ingest.
   - Implication: more code/test/spec work now.
   - Risk avoided: the same class of stall recurring whenever any source scan is
     slow, blocked, or effectively endless.

## Plan

Gap review converged in round 19. This plan converts the accepted gap analysis
into executable work. The implementation must keep the codebase compiling at
each committed milestone, but the SOW is not complete until all milestones,
tests, install recovery, local gates, and the implementation reviewer gate are
done.

### Plan Controls

- Scope unit: SOW-0114 is one reliability SOW. Reviews are gates, not discovery
  loops.
- Reviewer cadence:
  - Run one implementation-plan gate after this plan passes local self-review.
  - During implementation, do not run reviewers after small edits. Use external
    implementation review only after a complete meaningful milestone if a
    milestone is independently shippable, or once at the end before install.
  - Before any rerun, perform the blocker-class sweep required by
    `project-second-opinions`; do not patch one finding and immediately rerun.
- Ordering:
  - Specs first.
  - Failing tests second, grouped by milestone.
  - Runtime implementation third.
  - Focused tests after each milestone; each milestone exits compile-green; full
    gates run before implementation review.
- Sensitive-data rule: no raw transcript text, prompts, payload bodies,
  credentials, personal paths, or exact local source paths in committed
  artifacts. Stored/served lifecycle errors are sanitized before persistence.

### Milestone 1 - Specs And Contract Text

Before any test or runtime code, update the specs listed in `Spec Deltas`.
The spec update must describe target behavior, not current behavior:

- Ingestion lifecycle:
  - `ingester.md` replaces the all-sources scan/backfill/tail sequence with
    per-source scan outcome -> per-source repair scheduling -> immediate Tail.
  - The retained all-sources reconciliation is background repair only, never a
    Tail-start gate.
  - Tail restart, stale-tail watchdog, restart-request channel ownership,
    per-source/per-attempt contexts, lifecycle state machine, scan fatality,
    and shutdown residue recovery are documented.
- Data contracts:
  - `data-model.md` documents migration 0012, `source_progress` lifecycle and
    read-model columns, enum checks, nullable timestamp semantics, zero
    timestamp sentinel rules, `source_progress.cursor` authority, and
    `sources.cursor` deprecation.
  - `canonical-events.md` and code comments stop referring to `sources.cursor`
    as the durable cursor.
- Adapter contracts:
  - `adapter-contract.md` documents unchanged `canonical.Adapter`, new
    `AdapterOptions.OnTailHeartbeat`, nil-safe helper, mandatory heartbeat from
    the real Tail loop, and Scan-to-Tail cursor handoff.
  - All five adapter specs document Tail heartbeat and context cancellation;
    aiagent_v2/v3 additionally document scan-cursor handoff plus Tail-start
    catch-up reread.
- API/UI/observability:
  - `observability.md`, `rest-api.md`, `presenter.md`, `sse-protocol.md`, and
    `ui-pages.md` document lifecycle/read-model fields, `status_detail`,
    source degraded rules, `source_status_changed` invalidation semantics, and
    Sources/Ingest Errors UI use of lifecycle instead of `last_seen_at` as
    freshness. Review and update `architecture.md` if its health/source-status
    summary still describes the old lag-only panel model.
- Deployment/recovery:
  - `deployment.md`, `docs/runbook.md`, and install docs/scripts describe
    migration-aware upgrade ordering, ingester-before-serve startup, stale
    lifecycle diagnosis, daemon-lock checks, and installed DB rebuild/re-ingest
    verification.

Milestone exit:

- Specs have no `TBD` placeholders.
- Any intentional temporary spec/code structural drift caused by spec-first work
  is recorded while the milestone is in progress; full gates require
  `scripts/spec-drift.sh` to be clean before completion.

### Milestone 2 - Schema, Types, And Lifecycle Write Primitive

Implement the durable foundation before changing source orchestration:

- Add `internal/store/migrations/0012_source_progress_lifecycle.sql`.
  - Add the accepted lifecycle and read-model columns to `source_progress`.
  - Use `unknown` defaults for existing rows.
  - Add CHECK constraints for lifecycle/read-model enums.
  - Do not drop or backfill from `sources.cursor`.
- Bump schema/version references:
  - `internal/presenter/presenter.go` `SchemaVersion` to 12.
  - `internal/store/store_test.go` and `internal/store/migrations_test.go`
    migration count/version assertions to 12.
  - Every full-chain head schema-version test, including
    `internal/store/migration_0004_notify_test.go`,
    `internal/store/migration_0006_rollups_fts_test.go`,
    `internal/store/migration_0007_fts5_index_logs_test.go`,
    `internal/store/migration_0008_source_meta_test.go`,
    `internal/store/migration_0010_fulltext_content_test.go`, and
    `internal/store/migration_0011_topology_sort_indexes_test.go`.
  - `internal/store/schema_contract_test.go` for the new column contract.
  - Stale schema-version comments in touched store/presenter tests and docs.
- Add lifecycle/read-model Go types and constants in `internal/ingest`.
- Add the sanitizer used by lifecycle/read-model errors:
  - 1024 UTF-8 bytes maximum without splitting runes.
  - Strip control characters.
  - Replace configured source locations, `$HOME`, and home shorthand prefixes.
- Add `Ingester.RecordSourceLifecycle(ctx, sourceID, format, location string, update ...)`.
  - One transaction.
  - Upsert/preserve the `sources` row without `INSERT OR REPLACE`.
  - Upsert/preserve `source_progress` progress fields.
  - Support guarded expected-current-state transitions.
  - Insert `source_status_changed` in the same transaction when state changes.
  - Preserve `meta_json`, `fts5_index_logs`, progress cursor, sequence, and
    progress timestamps.
  - Initial pre-submit insert uses `lifecycle_state_at` as the deterministic
    `source_progress.updated_at` value; later lifecycle updates preserve the
    batch-progress `updated_at`.
- Add startup reconciliation helpers:
  - Existing configured rows with stale active states move to `starting`.
  - Existing rows no longer configured/discovered move to non-degrading
    `stopped`.
  - `stopped` does not flip `sources.enabled` to `0`; `enabled` remains the
    configured/user-intent flag and lifecycle state carries inactive status.
  - Persisted `read_model_state='repairing'` moves to `repair_pending`.

Tests added before implementation in this milestone:

- Migration 0012 defaults, checks, nullable fields, and version bump.
- Schema contract tests for `source_progress`.
- `RecordSourceLifecycle` insert/update/preservation/notify atomicity.
- Sanitization tests for stored and served lifecycle/read-model errors.
- Startup reconciliation tests for `unknown`, stale active states,
  unconfigured `stopped`, and reconfigured `stopped -> starting`.

### Milestone 3 - Source Supervisor, Tail Liveness, And Restart

Replace the current `runAdapter` ownership model with a source supervisor:

- Replace the existing `scanWG` / `scanDone` / `backfillDone` plumbing at the
  source lifecycle boundary:
  - `startSourceWithFactoryLookup` remains the source-supervisor creation point.
  - The `backfillDone` argument is removed from `startSource`,
    `startSourceWithFactoryLookup`, and any Tail-start path.
  - `runAdapter` is replaced by supervisor/attempt helpers. No helper may both
    call startup `Scan()`, wait on a global reconciliation/backfill channel, and
    then call `Tail()`.
  - The existing `scanWG`/`scanDone` pattern may be retained only as an
    all-startup-scan-outcome signal for retained global reconciliation. If
    retained, the supervisor calls the scan-outcome reporter exactly once for
    every source path: scan success, recoverable scan error, fatal scan error,
    start failure, construct/Submit failure, and cancellation before Scan.
  - The scan-outcome signal is never passed to Tail and never gates Tail.
  - `startPostScanBackfill` is retired or refactored/renamed into a background
    reconciliation launcher. Its completion channel is not passed to source
    supervisors. Tests must prove Tail starts while global reconciliation is
    still pending.
  - Retirement/refactor includes the `readModelBackfiller` interface,
    `fakeReadModelBackfiller` test helper, `backfillCancelWait`, and tests in
    `cmd/ai-viewer-ingest/main_test.go`,
    `cmd/ai-viewer-ingest/sources_test.go`, and
    `cmd/ai-viewer-ingest/source_location_test.go` that currently encode the
    old `scanWG`/`backfillDone` signatures.
- Keep one supervisor goroutine per source; the supervisor owns:
  - Event channel lifetime.
  - Per-source context derived from the adapter parent context.
  - Per-attempt child context for startup Scan, Tail, stale cancellation, and
    restart catch-up Scan.
  - Adapter factory lookup and fresh adapter construction for each Tail
    restart.
  - Restart-request channel registration/unregistration with the ingester.
  - Scan-outcome barrier release exactly once for startup reconciliation.
- Pre-submit lifecycle:
  - Every configured/discovered source records `starting` before factory lookup,
    location stat, cursor parsing, adapter construction, or `Submit`.
  - Pre-submit lifecycle write failure retries with 1s/2s/.../60s backoff and
    does not construct the adapter until the row commits.
  - Unknown adapter, missing location, construction failure, and Submit failure
    become durable degraded states and release startup scan outcome.
- Scan behavior:
  - Parent cancellation writes/stabilizes `stopped`.
  - Canonical `FatalScanError` produces terminal/degraded `scan_failed` for the
    process run and no Tail attempt.
  - Recoverable non-context scan errors record `scan_failed` evidence and fall
    through to Tail.
  - Long scans are health-visible through state/timestamps and logs.
- Tail behavior:
  - `scan_complete -> tail_starting -> tailing` happens per source without
    waiting for unrelated sources.
  - Non-context Tail error records `tail_failed`, then `tail_restarting`, then
    retries with context-cancellable 1s/2s/.../60s backoff.
  - Restart always reloads `source_progress.cursor`, constructs a fresh adapter,
    runs catch-up `Scan()` from that durable cursor, and only then calls
    `Tail()`. Restart catch-up Scan never participates in the startup barrier or
    global reconciliation trigger.
  - Factory/constructor failures during restart are retryable
    `tail_restarting` failures and increment the same restart counter.
  - Sustained restart failure escalates lifecycle error evidence after 100
    consecutive failures or 24 hours, while bounded retry continues.
- Stale Tail watchdog:
  - Add `AdapterOptions.OnTailHeartbeat` nil-safe helper in `internal/canonical`.
  - The watchdog lifecycle is owned by `internal/ingest.Ingester` start/stop
    wiring, or an equivalent ingester-owned lifecycle hook, not by `main.go`;
    it needs access to registered source heartbeat/restart state.
  - Ingester callback stores live heartbeat in memory without blocking on DB.
  - Persistence throttles `tail_heartbeat_at` to at most once per source per
    30 seconds.
  - Watchdog ticks every 30 seconds, uses live heartbeat first, persists guarded
    `tail_stale`, and enqueues a coalesced restart request.
  - Supervisor cancels only the active attempt; it does not start a duplicate
    Tail while the old Tail goroutine is still running.
  - If Tail ignores cancellation beyond the 5-second adapter grace, the source
    records terminal/degraded `tail_failed`; process restart is recovery.

Tests added before implementation in this milestone:

- Fake-adapter regression proving source A tails and commits while source B is
  still scanning.
- Scan recoverable/fatal/cancellation lifecycle tests.
- Pre-submit start/construct failure visibility and barrier-release tests.
- Tail failure restart, cursor reload, catch-up Scan, fresh factory, shutdown
  during backoff, restart construction failure, repeated-failure backoff, and
  escalation tests.
- Tail stale, heartbeat persistence throttle, heartbeat write-failure,
  watchdog guarded-transition, restart-channel coalescing, ignored-cancellation,
  and race-detector tests.

### Milestone 4 - Read-Model Deferral And Repair

Replace the global startup deferral with explicit per-source and global repair
state:

- Retire normal-daemon dependency on `Ingester.deferReadModels`,
  `SetDeferReadModels(true)`, and `DeferReadModels()` as all-sources startup
  gates.
- Remove or replace the old `internal/ingest.Ingester` `backfillDone` and
  `backfillMu` coordination fields as part of the new repair coordinator; no
  all-sources completion channel may gate Tail startup or per-source repair.
- Add an ingester-owned sourceID -> `*atomic.Bool` deferral map:
  - Created before `Submit`.
  - Passed to that source worker through the worker setup path, including
    `internal/ingest/worker.go`, `internal/ingest/worker_runtime.go`, and
    `internal/ingest/writer.go`.
  - Cleared after the source startup Scan outcome and before Tail starts.
  - Kept alive until the source worker fully stops.
  - Represents startup-scan deferral only. Same-source repair-active deferral is
    not used because it can strand Tail-time FTS/rollup work unless paired with
    a high-water/re-run mechanism.
- Add separate `readModelRebuildActive` for full rebuild/reconciliation.
- Update worker refresh behavior:
  - Per-source deferral or global rebuild-active skips derived FTS/rollup only.
  - Canonical rows, aggregates, source progress, lifecycle/read-model state, and
    notify rows still commit.
  - Any skipped derived refresh records `read_model_state='repair_pending'`.
- Add a read-model repair coordinator:
  - One serialization point replaces `backfillMu`.
  - Serializes per-source repair and full global reconciliation.
  - Prevents global truncate/rebuild from erasing concurrent source repair.
  - Same-source Tail incremental FTS/rollup refresh remains enabled during
    source repair; source repair is row/bucket scoped and SQLite-serialized.
- Add per-source repair loop owned by the source supervisor:
  - After scan outcome, clear startup deferral, record/schedule
    `repair_pending`, then start Tail; the source-scoped FTS repair and
    format/bucket rollup repair run behind Tail under the coordinator.
  - Use bounded repair attempts with the existing five-minute timeout family.
  - Use 1s/2s/.../60s retry backoff and reset attempts on `ready`.
  - Keep unfinished work durable as `repair_pending` on shutdown.
  - Use chunked write transactions with a 1-2 second target; if the target
    cannot be met, record retryable repair debt instead of starving tail writes.
- Retained global reconciliation:
  - May run after all startup scan outcomes, but start/construct/fatal failures
    release the barrier.
  - Must not be the only path that makes a completed source searchable or
    stat-able.
- Update paritycheck helpers that currently call `SetDeferReadModels(true)` to
  the new explicit repair/deferral API or documented compatibility wrapper.

Tests added before implementation in this milestone:

- Tail-before/during-backfill consistency tests.
- Source A repair completes while source B is still scanning.
- Same-source Tail rows are not lost while source A repair deletes/reinserts
  derived rows.
- Same-format rollup convergence against later global reconciliation.
- Read-model state machine, timeout/failure/retry, shutdown persistence,
  attempts reset, and persisted `repairing -> repair_pending` tests.
- Worker preservation tests for lifecycle/read-model columns.
- Repair coordinator serialization and chunk-timeout tests.
- Migration of paritycheck/subcommand call sites away from old global startup
  deferral semantics.

### Milestone 5 - Adapter Cursor Handoff And Heartbeats

Update all registered adapters after the canonical heartbeat/helper contract is
in place:

- `internal/adapters/aiagent_v2`:
  - Preserve final Scan cursor on the adapter instance.
  - Tail establishes watcher, rereads from scan cursor through current source
    state, then follows live events.
  - Tail calls heartbeat during idle ticks and after emitted events.
  - Scan/Tail observe context cancellation.
- `internal/adapters/aiagent_v3`:
  - Same scan-cursor handoff and Tail-start catch-up reread as aiagent_v2.
  - Same heartbeat and cancellation guarantees.
- `internal/adapters/claude_code`, `internal/adapters/codex`,
  `internal/adapters/opencode`:
  - Keep existing scan-cursor handoff behavior.
  - Add heartbeat calls from real Tail watch/poll loops.
  - Pin cancellation behavior.
- Opencode:
  - Add a restart catch-up test proving a fresh adapter with successful catch-up
    Scan enters warm-start Tail and does not skip or duplicate the boundary
    bucket.
- Error-producer audit:
  - This is a Milestone 5 entry gate: perform and record the audit before
    adapter cursor/heartbeat code changes, and before implementation review.
    Milestone 2 sanitizer tests use synthetic inputs; Milestone 5 proves real
    adapter producers do not feed transcript/user/payload/credential content
    into lifecycle/read-model persistence.
  - Enumerate every adapter path that reaches `OnError`, Scan/Tail returned
    errors, lifecycle errors, or read-model errors.
  - Classify as safe or needing redaction. Any unsafe path gets a redaction fix
    and regression test in the same milestone.

Tests added before implementation in this milestone:

- aiagent_v2/v3 scan-cursor handoff tests:
  `internal/adapters/aiagent_v2/tailer_test.go::TestScanThenTail_NoLossInWindow`
  and
  `internal/adapters/aiagent_v3/tailer_test.go::TestScanThenTail_NoLossInWindow`.
- Five adapter Tail heartbeat tests for idle and emitted-event paths.
- Five adapter nil-safety tests for zero-value `AdapterOptions`.
- Five adapter Scan/Tail cancellation tests.
- Opencode warm-start restart catch-up test.
- Adapter error-producer audit evidence recorded in this SOW.

### Milestone 6 - Presenter, API, SSE, Frontend

Expose the new state through read surfaces:

- `internal/presenter/health.go`:
  - Add `status_detail` with named constant `no_sources_configured`.
  - Select lifecycle/read-model fields and `progress_updated_at`.
  - Compute lifecycle/read-model degraded rules from phase-specific timestamps,
    not `sources.last_seen_at`.
  - Do not repurpose `internal/ingest/writer.go` `bumpSourceErrorCounter` /
    `sources.last_seen_at` updates as lifecycle freshness. They remain legacy
    parse-error/pricing-miss diagnostics unless a spec explicitly changes that
    behavior.
  - Compute effective stale Tail on read when ingester is down, limited to
    persisted `tailing` state.
  - Keep overall `down` reserved for DB/process unavailability.
- `internal/presenter/sources.go`:
  - Add the same lifecycle/read-model/progress fields.
  - Preserve cursor opt-in behavior.
  - Serialize nullable timestamps as null/omitted, not epoch.
- SSE:
  - Keep `source_status_changed` payload minimal: `{source_id, ts}`.
  - Ensure clients refetch `/api/sources` and `/api/health`; retained notify
    history is not required.
- Frontend:
  - Update `frontend/src/api/types.ts` and contract tests for additive fields.
  - Update `/sources` to show lifecycle/read-model state and stop presenting
    `last_seen_at` as freshness.
  - Update Ingest Errors and layout health hints only where they depend on
    lag/freshness semantics.
  - Keep this minimal and isolated from pending session-view UI SOWs.

Tests added before implementation in this milestone:

- Presenter health/source JSON shape, `status_detail`, no-sources degraded,
  all-sources-degraded-not-down, timestamp sentinel, long scan, pre-tail stale,
  healthy idle tail, tail failed/stale/restarting, read-model degraded states,
  and effective stale-tail tests.
- SSE/source-status invalidation tests.
- Frontend type contract tests, Sources rendering tests, and a focused
  Playwright route check for source lifecycle states when practical.

### Milestone 7 - Install, Rebuild, Re-Ingest, And Closure

After implementation tests and local gates pass:

- Build and install the application using the project install path.
- Stop services safely, verify the daemon lock if startup fails, and delete the
  installed ingester DB only at the recovery step already approved for this
  incident.
- Start `ai-viewer-ingest` first so migration 0012 and schema metadata are
  applied before `ai-viewer-serve` validates `SchemaVersion`.
- Verify and document service-ordering mechanics in `scripts/install-system.sh`,
  `scripts/test/install-system-test.sh`, and `deploy/systemd-system/*.service`:
  the system installer must stop serve before ingest, start ingest, wait for
  ingester active/migration evidence, then start serve. If the existing
  `After=ai-viewer-ingest.service` plus `Restart=on-failure` serve-unit retry
  pattern remains the direct-systemd fallback, document why it is acceptable for
  workstation upgrades; otherwise update the unit/test in this SOW.
- Let ingestion rebuild from source systems.
- Verify installed recovery:
  - Exact local-only query for the originally missing Codex session id.
  - Per-source lifecycle and read-model state through `/api/health` and
    `/api/sources`.
  - Same-failure inventory/count comparison over the stall window for every
    configured source, not Codex only.
  - UI visibility through the Sources page and session search/list surfaces.
- Record recovery evidence in this SOW without raw transcript data or personal
  local paths.

### Accepted Gap Coverage Matrix

- Cross-source stall: covered by Milestones 3 and 4.
- Same-source long scan: covered by Milestones 2, 3, and 6.
- Non-context Tail failure: covered by Milestone 3.
- Hung Tail heartbeat: covered by Milestones 3, 5, and 6.
- Durable lifecycle/API visibility: covered by Milestones 2 and 6.
- Pre-submit/start failure visibility: covered by Milestones 2 and 3.
- Per-source read-model repair: covered by Milestone 4.
- Global rebuild concurrency and writer contention: covered by Milestone 4.
- aiagent_v2/v3 scan-to-tail data-loss window: covered by Milestone 5.
- Source cursor authority and schema migration: covered by Milestones 1 and 2.
- Notify/SSE freshness invalidation: covered by Milestones 2 and 6.
- Minimal Sources UI update: covered by Milestone 6.
- Installed DB rebuild and same-failure recovery: covered by Milestone 7.

### Local Plan Self-Review Checklist

Completed on 2026-06-28 before the implementation-plan reviewer gate:

- Accepted gaps map to milestones, test families, runtime file families, and
  spec deltas. Evidence: the coverage matrix covers cross-source stall,
  same-source scan liveness, Tail failure/stale restart, lifecycle persistence,
  read-model repair, aiagent_v2/v3 cursor handoff, API/SSE/UI exposure, and
  installed recovery.
- No milestone depends on runtime code before specs. Evidence: Milestone 1 is
  specs-only and Milestones 2-6 each list tests before implementation.
- Reviewer cadence follows SOW-0115 waste controls. Evidence: the plan calls for
  one plan gate after local self-review and implementation review only on a
  completed meaningful milestone or at SOW completion, never for small edits.
- Implementation can proceed in compile-green chunks. Evidence: schema/types
  and lifecycle write primitives land before supervisor changes; source
  supervision lands before adapter heartbeats; read-model repair is isolated
  behind explicit coordinator/deferral contracts; presenter/frontend changes
  happen after the API fields exist.
- No plan item writes source-system data or stores sensitive content. Evidence:
  the plan keeps source readers read-only and includes sanitizer tests before
  persisted/served lifecycle or read-model errors ship.
- Pending UI SOWs remain isolated. Evidence: only the minimal Sources status
  contract is in scope; session-detail/UI workbench SOWs remain out of scope.

## Execution Log

### 2026-06-28

- Verified the missing Codex session exists in source files but is absent from
  the installed database.
- Verified Codex scan completed but did not enter tail mode.
- Identified the all-sources scan/backfill barrier as the structural liveness
  defect.
- Created this SOW for durable tracking.
- Drafted and locally self-reviewed the implementation plan after gap-review
  convergence. Self-review result: ready for the implementation-plan reviewer
  gate.
- Ran gap-analysis reviewer gate with `glm`, `minimax`, `kimi`, `mimo`, and
  `deepseek`; `qwen` did not return retrievable output in the harness session.
- Reviewer result: NEEDS WORK. Accepted blocking findings:
  - The gap analysis must address same-source scans that run too long or never
    return, not only unrelated-source blocking.
  - The gap analysis must specify the `deferReadModels` and read-model backfill
    contract when Tail can start before all scans/backfill complete.
  - The gap analysis must define durable/API-visible lifecycle state, not rely
    on absence of a `tail starting` log.
  - The gap analysis must include scan error/deadline behavior, concrete tests,
    adapter scan-termination audit, and installed recovery checks.
- Reviewer correction recorded: `source_progress.updated_at` exists in the
  schema and is updated by the worker, but `/api/health` does not expose it as
  lifecycle freshness today.
- Ran gap-analysis reviewer gate round 2 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `mimo` and `qwen` voted `NOTHING MORE CAN BE DONE`;
  `glm` and `kimi` found additional blocking gaps; `minimax` and `deepseek`
  became unretrievable harness sessions.
- Accepted round-2 findings:
  - Runtime recovery must verify data completeness for every source over the
    stall window, not only the reported Codex session.
  - Tail/backfill overlap must cover the single-writer SQLite queue,
    `busy_timeout(5000)`, shutdown-drain, and `replayRequired` behavior.
  - Tail non-context failures are currently terminal even though the spec says
    restart-after-backoff; this is a same-class liveness defect.
  - Shared `adapterCtx` is an architecture constraint for any per-source scan
    deadline or cancellation design.
  - aiagent_v2 and aiagent_v3 need explicit Scan-to-Tail cursor-handoff audit.
  - Durable lifecycle state should be persisted in SQLite and preferably live
    with `source_progress` unless the plan justifies a different table.
- Ran gap-analysis reviewer gate round 3 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `mimo` and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `glm`, `minimax`, `kimi`, and `deepseek` found
  additional blocking gaps.
- Accepted round-3 findings:
  - aiagent_v2/aiagent_v3 Scan-to-Tail cursor handoff is a confirmed data-loss
    window unless tests prove an adapter-specific exception; default fix should
    mirror the existing scan-cursor pattern.
  - Lifecycle state must be created and updated at lifecycle transitions, not
    only during normal event batch flushes.
  - A stuck scan must not keep a global `deferReadModels` flag true forever and
    suppress FTS/rollup refresh for unrelated sources.
  - Tail workers must not run incremental FTS/rollup refresh while full backfill
    is truncating/rebuilding those tables unless a tested serialization/catch-up
    contract exists.
  - Source health lag must be re-anchored away from error-only
    `sources.last_seen_at`.
  - Lifecycle state needs migration/version/default semantics, `/api/health`
    query changes, `/api/sources` consideration, and notify/SSE contract updates.
  - Tail-restart backoff and cursor reuse need explicit tests.
- Ran gap-analysis reviewer gate round 4 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `mimo` and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `glm`, `minimax`, `kimi`, and `deepseek` found
  additional blocking gaps.
- Accepted round-4 findings:
  - Completed-source scan-time rows need per-source read-model repair; waiting
    for all sources is still a stale search/stat path when one source is stuck.
  - Source lifecycle must include startup/construct failure, eager `sources` row
    creation, concrete lifecycle schema/state/default semantics, and UPSERT
    preservation tests.
  - Lifecycle notify rows must commit atomically with lifecycle state changes.
  - Tail restart must use durable cursor state or an equivalent proven-safe
    mechanism; channel ownership must keep the worker alive across retries.
  - Backfill timeout/failure must be health-visible and repairable, not only a
    log after truncate-then-rebuild starts.
  - `/api/sources`, `source_status_changed`, degraded rules, and
    `presenter.SchemaVersion` must be explicit parts of the contract.
  - Same-failure scan and installed DB rebuild decision must be explicit before
    closure.
- Rejected round-4 finding:
  - Adapter panic recovery is not folded into this SOW. Existing ingester spec
    says adapter panics are process-fatal; a process crash is visible and
    restartable, not the silent-source-stall failure class being fixed here.
- Same-failure scan evidence gathered read-only from the installed DB and source
  inventories:
  - Installed source progress is stale for aiagent_v3, Claude Code, and Codex on
    June 26; aiagent_v2/opencode source progress rows are newer, but canonical
    session maxima still do not reach June 28.
  - Installed canonical session max start times by format stop at June 26 for
    aiagent_v2, aiagent_v3, Claude Code, and Codex; Opencode reaches June 27 but
    not June 28.
  - Source-native counts changed since June 26 are materially higher than the
    installed canonical post-June-26 rows for several sources: Codex has 136
    JSONL files, Claude Code has 144 JSONL files, `$HOME/.ai-agent/sessions` has
    1,302 candidate session files, and the Opencode source DB has 1,680 sessions.
  - Conclusion: this is systemic installed-ingestion staleness, not a Codex-only
    display issue. Default recovery after the fix is a fresh installed DB
    rebuild unless a later exact inventory proves it unnecessary.
- Ran gap-analysis reviewer gate round 5 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `deepseek` voted `NOTHING MORE CAN BE DONE`; `glm`,
  `minimax`, and `mimo` found additional blocking gaps; `kimi` and `qwen` did
  not return retrievable final output in the harness session.
- Accepted round-5 findings:
  - The SOW must pin the Scan-to-Tail cursor handoff design instead of leaving
    instance-field versus interface-change as a plan-time choice.
  - Tail restart must use a concrete durable cursor path; this SOW chooses
    reload persisted cursor, run catch-up Scan, then Tail from the scan handoff.
  - Read-model repair state needs explicit durable schema/API fields, timeout,
    retry, and degraded-health semantics.
  - Per-source read-model deferral and global rebuild-active deferral need
    separate contracts so canonical tail ingestion cannot be blocked while
    FTS/rollups remain repairable.
  - The lifecycle state machine needs explicit shutdown, stale Tail heartbeat,
    startup failure, and scan-failure-to-tail transitions.
  - `source_status_changed` must have an explicit SSE payload decision; this SOW
    keeps the minimal invalidation payload and requires REST refetch.
  - No configured/discovered sources must have a concrete `/api/health`
    representation.
  - Migration/schema version, same-failure evidence, eager source row creation,
    and validation tests need explicit entries before planning.
- Ran gap-analysis reviewer gate round 6 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `mimo` and `deepseek` voted
  `NOTHING MORE CAN BE DONE`; `glm`, `minimax`, and `kimi` found additional
  blocking gaps; `qwen` did not return retrievable final output in the harness
  session.
- Accepted round-6 findings:
  - Tail heartbeat requires a concrete mechanism. This SOW chooses
    `AdapterOptions.OnTailHeartbeat`, not an adapter interface change or wrapper
    timer.
  - Tail heartbeat must be implemented in all five real adapters, including
    fsnotify adapters with an internal heartbeat ticker.
  - `tail_stale` requires an ingester-owned watchdog that writes state and
    notify rows; it cannot be only a computed serve-side status.
  - Startup failures before `Submit` need a durable pre-submit row path so
    `/api/health` and `/api/sources` can show failed configured sources.
  - Lifecycle writes need a concrete `Ingester` method/transaction information
    contract and must preserve source progress written by normal batches.
  - Per-source read-model deferral, global rebuild-active deferral, and
    read-model retry scheduling need concrete owners and shared state.
  - Tail-restart catch-up Scan must not touch the startup scan barrier, and
    catch-up Scan failure needs a degraded retry behavior.
  - `rest-api.md` and all per-adapter specs affected by heartbeat/cursor
    behavior must be in the spec-delta list.
  - `status_detail`, `read_model_error` sanitization, and lifecycle-state index
    policy need explicit data/API contracts.
- Ran gap-analysis reviewer gate round 7 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `mimo` and `kimi` voted
  `NOTHING MORE CAN BE DONE`; `glm` and `deepseek` found additional blocking
  gaps; `minimax` and `qwen` did not return retrievable final output in the
  harness session.
- Accepted round-7 findings:
  - Serve must compute stale Tail from persisted heartbeat timestamps on read,
    so a stopped/crashed ingester cannot leave sources looking healthy forever.
  - Adapter heartbeat callbacks must not write SQLite on every poll/event; the
    ingester throttles persisted heartbeat writes to at most once per 30 s per
    source.
  - aiagent_v2 and aiagent_v3 need a Tail-start catch-up reread from the scan
    cursor, not only a stored `scanCursor` field.
  - Scan failure behavior must distinguish recoverable partial scan errors from
    fatal source errors; fatal scan errors do not attempt Tail.
  - Tail restart backoff must be covered by a shutdown/cancellation test, and
    `starting -> stopped` must be a legal lifecycle transition.
  - `read_model_error` sanitization, `status_detail` scope, operator runbook
    path, and atomic notify wording needed concrete SOW text.
- Ran gap-analysis reviewer gate round 8 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `kimi`, `mimo`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `glm`, `minimax`, and `deepseek` found
  additional blocking gaps.
- Accepted round-8 findings:
  - Migration `0012` is also discussed by pending SOW-0108, so this SOW must
    record migration-number ownership and pending-SOW renumbering policy.
  - Serve-side effective `tail_stale` needs explicit state filtering and NULL
    `tail_heartbeat_at` semantics to avoid false stale reports on pre-tail or
    terminal sources.
  - `rest-api.md` and `data-model.md` have pre-existing schema-version drift
    that must be fixed while this SOW edits those specs.
  - Schema-changing install recovery needs runbook ordering: ingester migrates
    before serve passes its schema gate.
  - Lifecycle failures need a durable `lifecycle_error` column; generic
    lifecycle updates cannot promise error text without a schema field.
  - Stale Tail detection must trigger a controlled per-source restart path, and
    true hung Tail cancellation failure must become terminal/degraded instead
    of spawning duplicate Tails.
  - Every adapter must heartbeat at least every 30 seconds from inside its real
    Tail loop; natural poll heartbeat is allowed only when the poll interval
    meets that bound.
  - The source supervisor must own restart/channel closure/factory access, use
    per-source/per-attempt contexts, and preserve non-lifecycle source metadata
    during lifecycle upserts.
  - `source_progress.cursor` is the authoritative cursor; older
    `sources.cursor` references must be treated as deprecated spec/comment
    drift.
  - Read-model rebuild/reconciliation must define WAL/checkpoint behavior so
    large FTS/rollup work cannot silently starve tail canonical writes.
  - `source_progress.updated_at` is batch-progress evidence, not a standalone
    liveness signal; health must use lifecycle phase plus phase-specific
    timestamps.
  - Minimal Sources UI status rendering is in scope so the UI does not keep
    presenting `last_seen_at` as source freshness.
  - Full process restart/crash reattachment, heartbeat throttle reset, all-source
    degraded health, UPSERT preservation, and error sanitization boundaries need
    explicit tests.
- Rejected or narrowed round-8 findings:
  - Opencode restart `warmStart=false` was not accepted as stated. Existing code
    uses `warmStart` to distinguish Tail after Scan cursor from cold
    HEAD-snapshot Tail; restart catch-up Scan is another Scan->Tail handoff.
    The SOW now requires the implementation plan and tests to pin the correct
    no-skip/no-duplicate behavior instead of assuming a boolean value.
  - A `source_status_changed` debounce was not made mandatory. The state changes
    are threshold-driven and sparse; if flapping becomes noisy after
    implementation, it can be handled as a separate UI/notification tuning SOW.
- Ran gap-analysis reviewer gate round 9 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `glm`, `mimo`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `minimax` and `kimi` found additional blocking
  gaps; `deepseek` ended as a technical/no-final-output failure.
- Accepted round-9 findings:
  - The SOW must pin the fate of the old global `deferReadModels` startup-scan
    flag. It is retired and replaced by per-source deferral plus
    `readModelRebuildActive`; old startup-scan call sites/tests must migrate.
  - Sanitization must happen at ingest-time before SQLite persistence, with
    tests for stored and served values.
  - The `sources.cursor` schema column needs an explicit policy. This SOW keeps
    it as nullable historical drift and does not drop or CHECK-constrain it;
    new code must not read or write it, and comments/specs must point to
    `source_progress.cursor`.
  - Pre-submit `RecordSourceLifecycle(... starting ...)` write failure must not
    make a source invisible. The supervisor retries the lifecycle write before
    adapter construction or `Submit`.
  - Lifecycle UPSERT preservation must explicitly include `created_at` and
    existing source configuration fields.
  - Lifecycle/read-model transition notifications must not depend on the batch
    writer's parse-error notify boolean; the lifecycle transaction inserts its
    own `source_status_changed` row.
  - Notify rows remain realtime hints with bounded retention; serve must refetch
    health/source status on startup or reconnect.
  - Validation must cover Tail-restart catch-up Scan success, reuse of the
    normal cursor lookup path, frontend Sources rendering, and `sources.cursor`
    non-use.
  - Mid-run source auto-discovery remains out of scope; newly created source
    locations are picked up on ingester restart.
  - Read-model repair retry is owned by the source repair loop, not a second
    global watchdog. The source repair loop intentionally has no short synthetic
    execution timeout.
- Narrowed round-9 findings:
  - Adding `CHECK (cursor IS NULL)` to `sources.cursor` was not accepted. The
    migration remains additive and avoids rebuilding `sources`; tests and
    comments enforce that the historical column is not used.
- Ran gap-analysis reviewer gate round 10 with `glm`, `minimax`, and `kimi`
  returning usable output. `minimax` voted `NOTHING MORE CAN BE DONE` with P3
  refinements; `glm` and `kimi` found blocking gaps. `mimo`, `deepseek`, and
  `qwen` sessions were not retrievable after the harness/context transition and
  are not retried before fixing the accepted blocking findings.
- Accepted round-10 findings:
  - The stale-tail watchdog must have an explicit watchdog-to-supervisor restart
    signal path. This SOW chooses a registered buffered per-source restart
    request channel so the watchdog can coalesce restart requests without knowing
    supervisor internals.
  - Time-based lifecycle thresholds and backoff decisions must be testable
    through injectable clocks and/or thresholds, while production defaults remain
    unchanged.
  - Per-source read-model repair and full global reconciliation must serialize
    through a coordinator so a global truncate/rebuild cannot erase or race a
    per-source repair.
  - Heartbeat persistence failure behind writer contention must not falsely mark
    a source stale while the ingester still has a fresh in-memory heartbeat.
  - Repair transactions need an explicit wall-clock chunk bound, not only
    unbounded row batching.
  - `RecordSourceLifecycle` must not touch `source_progress.last_seq`,
    `last_ts_us`, `cursor`, or `updated_at`.
  - `OnTailHeartbeat` must be non-blocking and late callbacks after attempt
    cancellation must be safe no-ops.
  - Spec/test/artifact lists must name `ui-pages.md`, frontend API type
    contracts, adapter error-producer audit, `paritycheck` deferral call sites,
    install-script restart ordering, and the restart failure-counter reset rule.
- Ran gap-analysis reviewer gate round 11 with `glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, and `qwen`. `minimax`, `kimi`, `mimo`, and `deepseek` voted
  `NOTHING MORE CAN BE DONE`; `glm` and `qwen` found blocking gaps.
- Accepted round-11 findings:
  - The ingester watchdog needs the same first-heartbeat grace as serve-side
    stale detection, using `tail_started_at` before the first live/persisted
    heartbeat.
  - Lifecycle writes that can race between watchdog and supervisor must be
    conditional on the expected current state so late watchdog transitions cannot
    overwrite `tail_failed`, `tail_restarting`, or `stopped`.
  - In-memory heartbeat state shared by adapter Tail goroutines and the watchdog
    must be atomic or equivalently synchronized and race-detector clean.
  - Tail restart catch-up Scan must be observable while in `tail_restarting`.
  - Tail restart must always construct a fresh adapter instance after the failed
    attempt is cancelled/returned, instead of reusing a possibly corrupt watcher
    or connection.
  - Startup must convert persisted `read_model_state='repairing'` crash residue
    back to `repair_pending` so repair resumes.
  - `starting` beyond threshold must degrade; graceful shutdown needs tests for
    `tailing -> stopped` and `tail_restarting -> stopped`; pre-submit lifecycle
    row creation must preserve resolved `fts5_index_logs` and `meta_json`;
    adapter Scan cancellation needs tests; `read_model_repair_attempts` resets on
    `ready`; `Submit` failure is a visible startup failure.
- Ran gap-analysis reviewer gate round 12 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `qwen` voted
  `NOTHING MORE CAN BE DONE`; `kimi` produced a positive review before its
  harness handle became unavailable; `glm` and `mimo` found blocking gaps;
  `minimax` and `deepseek` were technical/unretrievable harness failures and
  are not retried before fixing accepted findings.
- Accepted round-12 findings:
  - A source in `construct_failed` must degrade immediately like
    `start_failed`; fatal `scan_failed` must be included in the same canonical
    degradation contract.
  - Migrated or crash-residue `unknown` lifecycle state must not appear healthy
    indefinitely; `unknown` beyond the pre-tail threshold degrades.
  - Same-source FTS repair must be serialized against same-source Tail
    incremental FTS/rollup refresh so repair deletes cannot erase Tail-time
    derived rows.
  - `read_model_state` needs a formal legal state machine.
  - Restarted Tail attempts must reset `tail_started_at` so first-heartbeat grace
    is per attempt, not inherited from a prior failed attempt.
  - Source-format rollup repair must explicitly handle another same-format
    source still scanning/deferred and converge with later global reconciliation.
  - Active read-model repair must observe context cancellation and leave durable
    retry debt.
  - The new repair coordinator must define the fate of the existing `backfillMu`
    serialization point.
  - The stale schema-version comment in `internal/presenter/presenter.go` must
    be updated with the schema bump.
- Ran gap-analysis reviewer gate round 13 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `minimax`, `kimi`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `qwen` included a P3 test refinement. `glm`
  found one blocking P2 and three P3 hygiene findings. `mimo` and `deepseek`
  ended as technical/unretrievable harness failures and are not retried before
  fixing accepted findings.
- Accepted round-13 findings:
  - Migration 0012 must explicitly update
    `internal/store/schema_contract_test.go`, because it pins the exact
    `source_progress` column set and would otherwise fail CI without asserting
    the new lifecycle/read-model column contract.
  - `internal/store/store_test.go` schema-version assertions and messages must
    move to version 12 with the migration.
  - Shutdown ordering must define what happens when a supervisor ignores
    cancellation past the adapter-context grace period and the store closes:
    final lifecycle writes are best-effort and startup residue recovery is the
    safety net.
  - `data-model.md`'s stale `## Schema (v1)` header must be updated while the
    file is edited for migration 12.
  - Rapid Tail failure cycling should have an explicit backoff/writer-contention
    validation case.
- Ran gap-analysis reviewer gate round 14 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. All six voted
  `NOTHING MORE CAN BE DONE`. Several reviewers included P3 observations and
  implementation-plan reminders; no reviewer found a blocking gap.
- Accepted round-14 clarifications:
  - Migration 0012 must not copy stale historical `sources.cursor` values into
    `source_progress.cursor`, because `source_progress.cursor` has been the
    authoritative cursor since migration 0002.
  - `/api/health` and `/api/sources` may expose `last_seq` and
    `progress_updated_at` only as secondary progress diagnostics, not freshness
    or liveness signals.
  - The runbook must include the exact serve schema-mismatch symptom, likely
    migration-12/ingester-not-run cause, and the `journalctl -u
    ai-viewer-ingest` diagnostic path.
  - aiagent_v2/aiagent_v3 Scan-to-Tail and catch-up behavior must prove
    no-skip/no-duplicate cursor semantics.
  - Opencode Tail restart catch-up Scan must prove the subsequent Tail uses the
    warm-start path without boundary skip/duplication.
- Ran gap-analysis reviewer gate round 15 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `qwen`, `kimi`, `mimo`, and `minimax` voted
  `NOTHING MORE CAN BE DONE`; `deepseek` became an unretrievable harness session
  after compaction; `glm` found one accepted P2 gap.
- Accepted round-15 finding:
  - Existing `sources` rows that are no longer configured/discovered must not
    remain migrated `unknown` rows with timestamp `0`, because presenter health
    enumerates durable source rows and would otherwise create false permanent
    `degraded` health. Startup reconciliation transitions those rows to
    non-degrading `stopped`, preserves metadata/progress, emits notify, and tests
    sentinel-zero timestamp semantics.
- Ran gap-analysis reviewer gate round 16 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `minimax`, `kimi`, `mimo`, `deepseek`, and
  `qwen` voted `NOTHING MORE CAN BE DONE`; `glm` found one accepted P2 gap.
- Accepted round-16 finding:
  - The round-15 `stopped` reconciliation needed the symmetric reconfiguration
    path. A previously stopped source that is again configured/discovered must
    legally transition `stopped -> starting` and resume normal startup. Startup
    reconciliation now also explicitly allows any prior state to move to
    `stopped` when the source id is absent from the configured/discovered set.
  - Folded in non-blocking clarity that one-shot derived-table repair helpers
    leave `read_model_state` to daemon evaluation.
- Ran gap-analysis reviewer gate round 17 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `glm`, `kimi`, `mimo`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `deepseek` found two accepted P2 gaps. `minimax`
  became an unretrievable harness session and is not retried before fixing the
  accepted blocking findings.
- Accepted round-17 findings:
  - `AdapterOptions.OnTailHeartbeat` needs an explicit nil-safe invocation
    contract. The SOW now requires a canonical helper that no-ops when the raw
    function field is nil, and adapters must call the helper instead of the raw
    function field.
  - Configured migrated `unknown` source rows with timestamp `0` need explicit
    startup ordering. The SOW now requires pre-submit `unknown -> starting`
    writes for configured/discovered sources before adapter construction and
    tests the first health-visible state.
  - Folded in non-blocking clarity that the read-model repair coordinator
    replaces `backfillMu` as the single repair serialization lock, and that a
    single long `tail_restarting` catch-up Scan degrades after 10 minutes.
- Ran gap-analysis reviewer gate round 18 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`; the user explicitly requested that `claude`
  not be used due to quota exhaustion, and it was not used. `glm`, `mimo`,
  `deepseek`, and `qwen` voted `NOTHING MORE CAN BE DONE`. The first `minimax`
  harness session became unretrievable, then the allowed single retry returned
  `NEEDS WORK`. `kimi` also returned `NEEDS WORK`.
- Accepted round-18 findings:
  - Fatal-vs-recoverable Scan errors need an explicit adapter/supervisor
    signaling mechanism; the SOW now requires a canonical fatal Scan error
    marker with default recoverable behavior for non-context errors without the
    marker.
  - Tail-restart factory/adapter construction failure must share the same
    retry counter, backoff, lifecycle evidence, and sanitization path as
    Tail/catch-up Scan failure.
  - `RecordSourceLifecycle` initial inserts must provide
    `source_progress.updated_at` because the existing column is `NOT NULL`
    without a default; later lifecycle updates preserve batch-progress
    `updated_at`.
  - The old `readModelBackfiller.DeferReadModels()` /
    `startPostScanBackfill` global gate must be redesigned or retired as part
    of the per-source deferral work.
  - Startup failure paths need an explicit scan-barrier release test, and
    store/schema tests need exact version-12 comment/constant maintenance.
  - The adapter error-producer audit is elevated to a pre-plan deliverable so
    sanitizer tests are backed by evidence that adapter errors do not embed
    transcript/user/payload/credential content.
  - Folded in P2/P3 clarity for opencode warm-start spec text, restart
    escalation threshold tests, repair chunk deadlines, status-detail constant
    semantics, nullable timestamp JSON semantics, and post-install
    same-failure reconciliation as a SOW-closure blocker.
- Ran gap-analysis reviewer gate round 19 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`; all six voted `NOTHING MORE CAN BE DONE`.
  No `claude` reviewer was used. The gap-analysis gate is converged.
- Ran implementation-plan reviewer gate round 1 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`. `glm`, `minimax`, `kimi`, `mimo`, and `qwen`
  voted `READY FOR IMPLEMENTATION`; `deepseek` voted `NEEDS WORK`.
- Accepted round-1 P1/P2 findings:
  - The plan needed to name the fate of `scanWG`, `scanDone`, `backfillDone`,
    `startPostScanBackfill`, `startSourceWithFactoryLookup`, and `runAdapter`
    instead of describing only abstract supervisor behavior.
  - The plan needed to state how retained all-sources reconciliation receives
    scan-outcome signals without reintroducing a Tail-start gate.
  - The migration-aware install step needed to explicitly confirm or change the
    systemd/install-script ordering pattern.
  - The adapter error-producer audit sequencing conflicted with the earlier
    "pre-plan" wording; it is now a Milestone 5 entry gate before adapter code
    changes and a blocker before implementation review.
- Plan-round-1 blocker class sweep:
  - Swept for plan items that named behavior without naming the current
    function/file seam. Added explicit fates for `runAdapter`,
    `startSourceWithFactoryLookup`, `scanWG`/`scanDone`, `backfillDone`, and
    `startPostScanBackfill`.
  - Swept for schema/version surfaces beyond the first cited test. Added
    `internal/store/migrations_test.go`, stale store/presenter comments, and
    schema-version comment maintenance to Milestone 2.
  - Swept for lifecycle state ambiguity against existing columns. Added the
    `sources.enabled` vs `lifecycle_state='stopped'` separation and the
    `lifecycle_state_at` initial `updated_at` decision.
  - Swept for ingester-owned background loops. Added explicit stale-tail
    watchdog start/stop ownership under `internal/ingest.Ingester`.
  - Swept for read-model deferral ambiguity. Added `worker_runtime.go`, the
    startup deferral path, and the required clear-deferral -> record/schedule
    repair -> start Tail ordering. The later round-1 race gate rejected
    repair-active per-source deferral without a high-water/rerun mechanism.
  - Swept for install-ordering ambiguity. Added `scripts/install-system.sh`,
    `scripts/test/install-system-test.sh`, and `deploy/systemd-system/*.service`
    verification/update requirements.
  - P3 comments from positive reviewers were folded in where they reduced
    implementation ambiguity. No `claude` reviewer was used.
- Ran implementation-plan reviewer gate round 2 with `glm`, `minimax`, `kimi`,
  `mimo`, `deepseek`, and `qwen`; all six voted
  `READY FOR IMPLEMENTATION`. No `claude` reviewer was used.
- Plan-round-2 disposition:
  - No P0/P1/P2 blocking findings were accepted. One reviewer returned
    `READY FOR IMPLEMENTATION` while labeling precision notes as
    "non-blocking P2 observations"; verified disposition is P3 clarity because
    the notes named existing implementation surfaces but did not change the
    design, sequencing, tests, or acceptance criteria.
  - Folded in the precision notes that reduce implementation ambiguity:
    full-chain schema-head tests including `migration_0010` and
    `migration_0011`, `worker.go`/`writer.go` read-model deferral plumbing,
    `bumpSourceErrorCounter`/`last_seen_at` as legacy diagnostics rather than
    lifecycle freshness, `main_test.go`/`sources_test.go`/
    `source_location_test.go` scan/backfill signature tests, and
    `deployment.md`/`architecture.md`/Ingest Errors spec surfaces.
  - No plan-gate rerun is opened for P3-only clarity edits; this follows the
    SOW-0115 waste-control rule that positive reviewer votes plus P3-only
    refinements do not justify another six-reviewer round.
- Applied Milestone 1 spec updates before tests/code:
  - `ingester.md`: per-source supervisor lifecycle, Tail restart/stale
    heartbeat, read-model repair, notify, shutdown, and failure-recovery
    contracts.
  - `data-model.md`: schema v12 target, `source_progress` lifecycle/read-model
    columns/enums, `source_progress.cursor` authority, migration 0009-0012
    history, and legacy `sources.cursor` handling.
  - `adapter-contract.md`, `canonical-events.md`, and all five adapter specs:
    `source_progress.cursor`, Scan-to-Tail cursor handoff, and Tail heartbeat
    contracts.
  - `rest-api.md`, `observability.md`, `presenter.md`, `sse-protocol.md`, and
    `ui-pages.md`: lifecycle/read-model fields, `status_detail`, health rules,
    source-status invalidation, Sources UI, and Ingest Errors UI contracts.
  - `architecture.md`, `deployment.md`, and `docs/runbook.md`: per-source
    source-supervisor architecture, migration-aware install/start ordering, and
    operator recovery/status language.
- Milestone 1 self-checks:
  - `git diff --check` over the touched spec/runbook files: clean.
  - Identity scan over touched spec/runbook/SOW files for personal name/path:
    clean.
  - Stale-contract grep for old all-sources barrier terms found only an
    unrelated aiagent_v2 performance phrase ("60-minute backfill gate").
  - `scripts/spec-drift.sh` intentionally not run yet: spec-first work creates
    expected drift until Milestones 2-6 write tests and implementation.

## Validation

Acceptance criteria evidence:

- Cross-source liveness: covered by
  `cmd/ai-viewer-ingest/lifecycle_test.go::TestRunAdapterStartsTailBeforeGlobalBackfillDone`
  and
  `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceTailCommitsWhileAnotherSourceScanIsBlocked`.
- Tail failure/restart: covered by command-boundary supervisor tests for
  Tail failure, restart backoff, fresh adapter construction, and durable
  lifecycle evidence.
- Tail heartbeat/stale detection: covered by
  `internal/ingest/tail_liveness_test.go` plus adapter Tail-loop heartbeat
  tests across all five adapters.
- aiagent_v2/v3 Scan-to-Tail loss window: covered by
  `TestScanThenTail_NoLossInWindow` in both adapter packages.
- Per-source scan-time read-model repair: covered by
  `internal/ingest/read_model_repair_test.go` and the extended lifecycle
  command-boundary test that proves scan-time FTS/rollup repair while another
  source remains blocked.
- Lifecycle/read-model API and UI status: covered by presenter helper tests,
  `/api/sources` tests, frontend Sources/Ingest Errors tests, and the contract
  matrix.
- Out-of-order session update/finalize notify-root safety: covered by
  `internal/ingest/notify_producer_test.go` tests added after installed
  validation found the batch-drop defect.
- Codex cold-rebuild recency priority: covered by
  `internal/adapters/codex/scanner_test.go::TestDiscover_MultiShardNewestFirst`
  after installed validation proved a cold rebuild could hide recent Codex
  sessions behind old history for too long.
- Installed runtime: fresh DB reinstall completed and all configured sources
  are visible in lifecycle state immediately after startup.
- Exact reported Codex session verification: the rebuilt installed DB contains
  one row for the reported native Codex session id, and the Codex source cursor
  includes the target rollout file.

Tests or equivalent validation:

- Focused tests:
  - `go test ./cmd/ai-viewer-ingest -run TestSourceTailCommitsWhileAnotherSourceScanIsBlocked -count=1`
  - `go test ./internal/ingest -run 'TestEmitNotify_Session(Update|Finalized)BeforeStartCreatesStubAndNotifies' -count=1`
  - `go test -race -count=1 -coverprofile=/tmp/ingest-writer-cover.out -covermode=atomic ./internal/ingest`
  - `cd frontend && npm run test -- --run src/components/TurnView/TurnView.test.tsx`
- Package/broader tests:
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
  - `go test ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 -count=1`
  - `go test ./internal/adapters/codex -run TestDiscover_MultiShardNewestFirst -count=1`
  - `go test -race ./internal/adapters/codex -count=1`
  - `scripts/test.sh`
  - `scripts/check-coverage.sh coverage.out`
- Static/build/frontend gates:
  - `scripts/lint.sh`
  - `scripts/build.sh`
  - `cd frontend && npm run lint`
  - `cd frontend && npm run typecheck`
  - `cd frontend && npm run test -- --run --coverage`
  - `cd frontend && npm run e2e`
  - `cd frontend && npm run e2e:a11y`
- Cross-cutting gates:
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
  - `scripts/test/check-contract-matrix-test.sh && scripts/check-contract-matrix.sh`
  - `scripts/test/check-ingestion-parity-test.sh && scripts/check-ingestion-parity.sh --fixtures`
  - `go test -run='^Fuzz' ./internal/adapters/... ./internal/parity`
  - `scripts/scan-secrets.sh && scripts/scan-ai-attribution.sh`
- Coverage:
  - Go `scripts/test.sh`: total 81.6% statements, race-clean.
  - `scripts/check-coverage.sh coverage.out`: gated internal aggregate 85.2%,
    every gated package at or above 80%; `internal/ingest` 81.0%.
  - Frontend coverage: 76 files / 931 tests passed; aggregate lines 90.08%.
- Full aggregate gate:
  - A complete `scripts/gates.sh` run previously passed before the installed
    writer/notify defect and TurnView timer cleanup were added.
  - After the writer/notify fix, `scripts/gates.sh` passed lint/static,
    security, scans, spec drift/parity, build, Go race/coverage, then failed
    in frontend Vitest due the TurnView unmount timer leak. The leak is now
    fixed and covered; frontend coverage and Playwright gates are green.
  - Final `scripts/gates.sh` run passed every gate. The benchmark preflight
    accepted the host sample (`11.81 < 12.00`), benchmark regression gate passed
    with no `sec/op` regression over 20%, and the aggregate completed in 615 s.
    The script's own note says the total exceeds the 5-minute target because
    the Go race suite is the long pole; this is expected gate behavior, not a
    failure.

Real-use evidence:

- `scripts/install-system.sh` successfully rebuilt, installed, and started the
  system units after deleting the installed derived DB files.
- `/api/health` on the installed service reports `schema_version: 12` and five
  configured sources.
- Installed SQLite row counts are increasing during cold reingest.
- Installed journal after the writer/notify fix has no recurrence of
  `DROPPING`, `lookup root`, or batch-failed notify-root errors.
- Second fresh DB reinstall after the Codex newest-first fix started cleanly,
  reported schema version `12`, and reached the target rollout file within the
  first few minutes of scanning instead of walking old history first.
- Rebuilt installed DB sample after the second reinstall:
  `sessions=12742`, `turns=32244`, `ops=168689`, and
  `payload_refs=222994`; Codex cursor contained 169 scanned rollout files and
  the target rollout entry.
- Exact reported Codex session query returned one installed DB row with native
  id matching the reported session id, agent name beginning `codex:`, and
  `status='running'`.
- Earlier cold-rebuild samples did not yet contain the exact reported Codex
  session because the historical scan had not reached that rollout file; later
  samples after the Codex newest-first fix verified the exact reported session
  in the rebuilt installed DB.

Reviewer findings:

- Gap review round 1: NEEDS WORK. Accepted findings incorporated into the
  revised gap analysis.
- Gap review round 2: NEEDS WORK from successful reviewers due to accepted P1/P2
  liveness, recovery, adapter-cursor, and backfill-contention gaps. Findings are
  incorporated.
- Gap review round 3: NEEDS WORK from `glm`, `minimax`, `kimi`, and `deepseek`;
  `mimo` and `qwen` voted positive. Accepted findings incorporated; full gap
  gate rerun pending.
- Gap review round 4: NEEDS WORK from `glm`, `minimax`, `kimi`, and `deepseek`;
  `mimo` and `qwen` voted positive. Accepted findings incorporated; one panic
  recovery finding rejected with evidence; full gap gate rerun pending.
- Gap review round 5: NEEDS WORK from `glm`, `minimax`, and `mimo`;
  `deepseek` voted positive; `kimi` and `qwen` had technical/no-final-output
  failures. Accepted findings incorporated; full gap gate rerun pending.
- Gap review round 6: NEEDS WORK from `glm`, `minimax`, and `kimi`;
  `mimo` and `deepseek` voted positive; `qwen` had a technical/no-final-output
  failure. Accepted findings incorporated; full gap gate rerun pending.
- Gap review round 7: NEEDS WORK from `glm` and `deepseek`;
  `mimo` and `kimi` voted positive; `minimax` and `qwen` had
  technical/no-final-output failures. Accepted findings incorporated; full gap
  gate rerun pending.
- Gap review round 8: NEEDS WORK from `glm`, `minimax`, and `deepseek`;
  `kimi`, `mimo`, and `qwen` voted positive. Accepted findings incorporated;
  two findings narrowed/rejected with evidence; full gap gate rerun pending.
- Gap review round 9: NEEDS WORK from `minimax` and `kimi`;
  `glm`, `mimo`, and `qwen` voted positive; `deepseek` had a
  technical/no-final-output failure. Accepted findings incorporated; one
  `sources.cursor` CHECK-constraint suggestion narrowed with evidence; full gap
  gate rerun pending.
- Gap review round 10: NEEDS WORK from `glm` and `kimi`; `minimax` voted
  positive with P3 refinements; `mimo`, `deepseek`, and `qwen` were not
  retrievable after the harness/context transition. Accepted findings
  incorporated; full gap gate rerun pending.
- Gap review round 11: NEEDS WORK from `glm` and `qwen`; `minimax`, `kimi`,
  `mimo`, and `deepseek` voted positive. Accepted findings incorporated; full
  gap gate rerun pending.
- Gap review round 12: NEEDS WORK from `glm` and `mimo`; `qwen` voted positive;
  `kimi` produced a positive review before a harness handle failure; `minimax`
  and `deepseek` had technical/no-final-output failures. Accepted findings
  incorporated; full gap gate rerun pending.
- Gap review round 13: NEEDS WORK from `glm`; `minimax`, `kimi`, and `qwen`
  voted positive; `mimo` and `deepseek` had technical/no-final-output failures.
  Accepted findings incorporated; full gap gate rerun pending.
- Gap review round 14: all six reviewers (`glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, `qwen`) voted `NOTHING MORE CAN BE DONE`. P3 implementation-plan
  reminders were incorporated as clarifying SOW text; full gap gate rerun
  pending for clean convergence after the clarifications.
- Gap review round 15: `qwen`, `kimi`, `mimo`, and `minimax` voted
  `NOTHING MORE CAN BE DONE`; `glm` voted `NEEDS WORK` with one accepted P2
  orphan/unconfigured-source health false-positive gap; `deepseek` became an
  unretrievable harness session after compaction. Accepted finding incorporated;
  full gap gate rerun pending.
- Gap review round 16: `minimax`, `kimi`, `mimo`, `deepseek`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `glm` voted `NEEDS WORK` with one accepted P2
  stopped-source reconfiguration transition gap. Accepted finding incorporated;
  full gap gate rerun pending.
- Gap review round 17: `glm`, `kimi`, `mimo`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `deepseek` voted `NEEDS WORK` with two accepted P2
  gaps: heartbeat callback nil-safety and configured migrated `unknown` startup
  ordering. `minimax` became an unretrievable harness session. Accepted findings
  incorporated; full gap gate rerun pending.
- Gap review round 18: `glm`, `mimo`, `deepseek`, and `qwen` voted
  `NOTHING MORE CAN BE DONE`; `kimi` voted `NEEDS WORK`; the first `minimax`
  session became unretrievable and the allowed retry voted `NEEDS WORK`.
  Accepted P1/P2/P3 findings incorporated; full gap gate rerun pending. `claude`
  was not used.
- Gap review round 19: all six reviewers (`glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, `qwen`) voted `NOTHING MORE CAN BE DONE`. Gap-analysis gate
  converged. `claude` was not used.
- Plan review round 1: `glm`, `minimax`, `kimi`, `mimo`, and `qwen` voted
  `READY FOR IMPLEMENTATION`; `deepseek` voted `NEEDS WORK` with accepted P1/P2
  plan precision findings. Accepted findings and class sweep are recorded in
  the execution log; full plan gate rerun pending. `claude` was not used.
- Plan review round 2: all six reviewers (`glm`, `minimax`, `kimi`, `mimo`,
  `deepseek`, `qwen`) voted `READY FOR IMPLEMENTATION`. Non-blocking precision
  notes were folded into the plan as P3 clarity. Implementation-plan gate
  converged. `claude` was not used.

Milestone 2 failing-test evidence:

- Added schema/migration tests for `0012_source_progress_lifecycle.sql`,
  full-chain schema version `12`, `source_progress` lifecycle/read-model
  columns, defaults, and CHECK constraints. Focused command
  `go test ./internal/store` fails as expected because migration 0012 does not
  exist yet: schema head remains `11` and `source_progress` has no
  `lifecycle_state` / `read_model_state` columns.
- Added ingester lifecycle primitive tests for `RecordSourceLifecycle`,
  lifecycle/read-model error sanitization, startup reconciliation, notify
  emission, and preservation of lifecycle/read-model fields during normal
  progress UPSERTs. Focused command `go test ./internal/ingest` fails as
  expected because `SourceLifecycleUpdate`, lifecycle/read-model constants,
  `RecordSourceLifecycle`, `SourceRegistration`, and
  `ReconcileSourceLifecycles` are not implemented yet.
- Added presenter tests for schema version `12`, `status_detail`, health
  re-anchoring away from legacy `last_seen_at`, lifecycle/read-model JSON
  fields, and zero timestamp sentinel handling. Focused command
  `go test ./internal/presenter` fails as expected because the presenter still
  serves schema version `11`, lacks lifecycle columns/fields, and still uses the
  old lag-based degraded rule.

Milestone 2 implementation evidence:

- Added migration `internal/store/migrations/0012_source_progress_lifecycle.sql`
  with additive `source_progress` lifecycle/read-model columns, enum CHECK
  constraints, defaults, and `schema_meta.version='12'`.
- Added `internal/ingest/source_lifecycle.go` with lifecycle/read-model state
  types, `RecordSourceLifecycle`, startup reconciliation, bounded diagnostic
  sanitization, source row preservation, and same-transaction
  `source_status_changed` notify emission.
- Updated `/api/health` and `/api/sources` presenter projections to serve
  lifecycle/read-model fields, `status_detail=no_sources_configured`, zero
  timestamp sentinel handling, and lifecycle-anchored degraded rules.
- Focused gates passed:
  - `go test ./internal/store ./internal/ingest ./internal/presenter`
  - `go test ./cmd/ai-viewer-ingest`
  - `scripts/spec-drift.sh`

Milestone 3 failing-test evidence:

- Added `cmd/ai-viewer-ingest/lifecycle_test.go`
  `TestRunAdapterStartsTailBeforeGlobalBackfillDone`. Focused command
  `go test ./cmd/ai-viewer-ingest -run TestRunAdapterStartsTailBeforeGlobalBackfillDone -count=1`
  fails as expected because `runAdapter` waits on the global `backfillDone`
  channel before calling `Tail()`.
- Added command-boundary lifecycle tests for pre-submit `start_failed`,
  constructor `construct_failed`, scan-to-tail `tailing`, and Tail failure
  restart. Focused command
  `go test ./cmd/ai-viewer-ingest -run 'Test(StartSourceRecordsStartFailedForUnknownAdapter|StartSourceRecordsConstructFailed|StartSourceRecordsScanAndTailLifecycle|StartSourceRestartsAfterTailFailure)' -count=1`
  fails as expected because source startup writes no lifecycle row today and a
  non-context `Tail()` error exits the adapter goroutine instead of recording
  `tail_failed` / `tail_restarting` and constructing a fresh adapter.

Milestone 3 core supervisor implementation evidence:

- Removed the global `backfillDone` Tail-start gate from
  `cmd/ai-viewer-ingest` source startup. Tail now starts after that source's
  own startup Scan outcome, and the all-sources backfill channel remains only a
  background reconciliation wait used during shutdown.
- Added `cmd/ai-viewer-ingest/source_supervisor.go`. The supervisor records
  durable `starting`, `start_failed`, `construct_failed`, `scanning`,
  `scan_failed`, `scan_complete`, `tail_starting`, `tailing`, `tail_failed`,
  `tail_restarting`, and `stopped` transitions for the core startup/Tail paths.
- Tail returned non-context errors now record `tail_failed`, increment restart
  state through `tail_restarting`, wait with bounded exponential backoff, build
  a fresh adapter, reload `source_progress.cursor`, run catch-up `Scan()`, and
  re-enter `Tail()`.
- Added canonical support primitives described by the specs:
  `AdapterOptions.OnTailHeartbeat`, nil-safe `AdapterOptions.TailHeartbeat()`,
  and the `FatalScanError` marker helpers.
- Split auto-discovery into `cmd/ai-viewer-ingest/source_discovery.go` and
  updated the spec-drift detector, its hermetic self-test, the quality-gates
  spec, and runtime skills so adapter-probe drift is checked at the new file.
- Added command-boundary tests for:
  - source Tail starting before global backfill completion;
  - pre-submit `start_failed` and constructor `construct_failed`;
  - scan-to-tail lifecycle reaching `tailing`;
  - recoverable scan errors falling through to Tail;
  - fatal scan errors staying at `scan_failed` and not Tail;
  - source A committing Tail progress while source B remains blocked in Scan;
  - Tail failure restart with fresh adapter construction and durable failure
    evidence.
- Added canonical tests for the nil-safe heartbeat helper and fatal-scan marker.
- Focused gates passed:
  - `go test ./cmd/ai-viewer-ingest`
  - `go test ./internal/canonical ./cmd/ai-viewer-ingest ./internal/ingest ./internal/store ./internal/presenter`
  - `go test -race ./cmd/ai-viewer-ingest`
  - `scripts/test/spec-drift-test.sh`
  - `scripts/spec-drift.sh`

Milestone 3 tail-liveness/watchdog implementation evidence:

- Added `internal/ingest/tail_liveness.go` and supporting `Ingester` state:
  registered source supervisors can receive coalesced restart requests, adapter
  heartbeats update live in-memory liveness, and persisted `tail_heartbeat_at`
  writes are throttled per source.
- The stale-tail watchdog scans `lifecycle_state='tailing'` rows, uses live
  heartbeat evidence before persisted fallback, records guarded `tail_stale`
  transitions, emits same-transaction `source_status_changed` notify rows, and
  asks the owning source supervisor to restart the active Tail attempt.
- Fixed the watchdog read/write order so stale-source ids are collected and the
  read cursor is closed before lifecycle writes on the single SQLite writer
  connection.
- Added `internal/ingest/tail_liveness_test.go` coverage for heartbeat
  persistence throttling and stale-tail restart request behavior.

Milestone 5 Tail heartbeat implementation evidence:

- Wired `canonical.AdapterOptions.TailHeartbeat()` through all five registered
  adapters: `aiagent_v2`, `aiagent_v3`, `claude-code`, `codex`, and `opencode`.
- Filesystem-watch adapters call heartbeat from the real Tail loop on idle
  ticks and after successful flush/catch-up work. `opencode` calls heartbeat
  from its real poll/WAL loop without forcing idle `SourceProgress` events.
- Added adapter tests proving Tail heartbeat is invoked for idle ticks/polls
  and periodic/emitted-event paths:
  - `internal/adapters/aiagent_v2/tailer_test.go`
  - `internal/adapters/aiagent_v3/tailer_test.go`
  - `internal/adapters/claude_code/tailer_test.go`
  - `internal/adapters/codex/tailer_branch_test.go`
  - `internal/adapters/opencode/tailer_test.go`
- Focused gates passed after heartbeat wiring:
  - `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode`
  - `go test -race ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode`
  - `go test ./internal/canonical ./cmd/ai-viewer-ingest ./internal/ingest ./internal/store ./internal/presenter`
  - `go test -race ./cmd/ai-viewer-ingest ./internal/ingest`
  - `go test ./...`
  - `scripts/test/spec-drift-test.sh`
  - `scripts/spec-drift.sh`

Milestone 5 aiagent_v2/v3 cursor-handoff implementation evidence:

- `aiagent_v2` and `aiagent_v3` now record the final Scan cursor on the adapter
  instance and warm-start Tail from that cursor when Scan and Tail run on the
  same instance.
- Warm Tail establishes the fsnotify watch first, then performs one catch-up
  pass over current source files from the Scan cursor before entering the live
  event loop. Cold Tail keeps the existing follow-from-now snapshot behavior.
- Added regression tests proving records written after Scan returns but before
  Tail starts are emitted by Tail:
  - `internal/adapters/aiagent_v2/tailer_test.go::TestScanThenTail_NoLossInWindow`
  - `internal/adapters/aiagent_v3/tailer_test.go::TestScanThenTail_NoLossInWindow`
- The new tests failed before the implementation with:
  - `Tail did not emit snapshot appended in the Scan-to-Tail window`
  - `Tail did not emit record appended in the Scan-to-Tail window`
- Focused gates passed after cursor handoff:
  - `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 -run TestScanThenTail_NoLossInWindow -count=1`
  - `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3`
  - `go test -race ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3`
  - `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode`
  - `go test ./internal/canonical ./cmd/ai-viewer-ingest ./internal/ingest ./internal/store ./internal/presenter`
  - `go test ./...`
  - `scripts/test/spec-drift-test.sh`
  - `scripts/spec-drift.sh`

Milestone 4 Tail read-model deferral implementation evidence:

- Added per-source read-model deferral flags to `internal/ingest.Ingester`.
  `SetDeferReadModels(true)` still enables the startup scan fast path, but each
  worker now reads its own source flag instead of the global flag directly.
- The source supervisor clears the completed source's deferral before Tail
  starts, including recoverable scan-error paths that still fall through to
  Tail. Tail batches from that source therefore refresh FTS/rollups even while
  another source is still scanning.
- Added a command-boundary regression to
  `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceTailCommitsWhileAnotherSourceScanIsBlocked`
  proving a Tail-created LLM op appears in `fts_ops` while another source's
  Scan remains blocked.
- The new assertion failed before implementation with
  `tail FTS rows = 0, want 1 while another source scan is blocked`.
- Focused gates passed after source-scoped Tail deferral:
  - `go test ./cmd/ai-viewer-ingest -run TestSourceTailCommitsWhileAnotherSourceScanIsBlocked -count=1`
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest`
  - `go test -race ./cmd/ai-viewer-ingest ./internal/ingest`
  - `go test ./...`
  - `scripts/test/spec-drift-test.sh`
  - `scripts/spec-drift.sh`

Milestone 4 scan-time read-model repair implementation evidence:

- Added source-scoped read-model repair on `internal/ingest.Ingester`.
  The repair rebuilds FTS rows for the completed source and recomputes closed
  rollup buckets touched by that source using the existing format-scoped rollup
  contract.
- The source supervisor now records `read_model_state='repair_pending'` only
  when it clears a source that actually had deferred read-model writes, starts
  Tail, then runs the repair path and records `repairing -> ready` or
  `repair_failed` with sanitized evidence.
- Shutdown/cancellation attempts to restore interrupted repair work to
  `repair_pending`, so a later startup can retry instead of treating stale
  `repairing` as progress.
- Extended
  `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceTailCommitsWhileAnotherSourceScanIsBlocked`
  to prove:
  - a scan-time LLM op appears in `fts_ops` after the source Scan completes;
  - the source's hourly rollup includes both the Scan op and Tail op;
  - the source reaches `read_model_state='ready'`;
  - all of this happens while another source's Scan remains blocked.
- The new scan-time FTS assertion failed before repair implementation with
  `scan FTS rows = 0, want 1 after source scan completed while another source
  scan is blocked`.
- Focused gates passed after source-scoped scan repair:
  - `go test ./cmd/ai-viewer-ingest -run TestSourceTailCommitsWhileAnotherSourceScanIsBlocked -count=1`
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest`
  - `go test -race ./cmd/ai-viewer-ingest ./internal/ingest`
  - `go test ./...`
  - `scripts/spec-drift.sh`
  - `scripts/test/spec-drift-test.sh`

Milestone 6 presenter/API/SSE/frontend implementation evidence:

- `/api/health` now computes effective Tail staleness from persisted
  `tail_started_at` / `tail_heartbeat_at` when the ingester process is not
  there to run the watchdog. Persisted `tailing` becomes effective
  `tail_stale` only when the Tail heartbeat age exceeds the 5-minute threshold.
- `/api/health` degraded decisions now use lifecycle/read-model phase clocks:
  pre-tail states after 60 seconds, long scans after 10 minutes, stale Tail
  heartbeat after 5 minutes, and read-model pending/repairing after the
  5-minute repair grace. Legacy `last_seen_at` lag remains reported but no
  longer drives freshness.
- `/api/sources` uses the same effective Tail-stale state, so the Sources page
  cannot show stale persisted `tailing` as healthy after the ingester is down.
- `frontend/src/api/types.ts` now mirrors the lifecycle/read-model fields on
  `/api/health.sources[]` and `/api/sources.items[]`, including open unions for
  lifecycle and read-model state values.
- The Sources page now renders lifecycle and read-model columns from
  `/api/sources`, keeps the overall health badge from `/api/health`, uses the
  progress timestamp as the primary progress evidence, and stops presenting
  legacy lag/`last_seen_at` as freshness.
- The Ingest Errors page now ranks parse errors and surfaces lifecycle/read-model
  degradation from `/api/sources`; legacy lag is no longer used as a freshness
  signal.
- Focused tests/gates passed after Milestone 6:
  - `go test ./internal/presenter`
  - `go test ./internal/presenter ./cmd/ai-viewer-ingest ./internal/ingest`
  - `cd frontend && npm run lint`
  - `cd frontend && npm run typecheck`
  - `cd frontend && npm run test -- --run src/pages/Sources/Sources.test.tsx src/pages/IngestErrors/IngestErrors.test.tsx src/api/types.contract.test.ts`

Remaining implementation before SOW-0114 can close:

- Implementation review gate must run after the installed verification evidence
  and aggregate-gate status are recorded.

Operational defect found during installed reingest validation:

- After the first fresh install attempt, the installed ingester logged a batch
  drop from the notification producer:
  `DROPPING ... ingest notify: lookup root for session ... sessions row ...
  absent in batch tx`.
- Root cause: `applySessionUpdated` and `applySessionFinalized` could mark a
  session dirty after an out-of-order update/finalize event even when no
  durable `sessions` row existed yet. `emitSessionChangedNotify` then correctly
  failed closed because it could not resolve the session root.
- Spec updates:
  - `canonical-events.md`: session update/finalize events are out-of-order
    tolerant and require stub-row creation before dirty/notify marking.
  - `ingester.md`: dirty session ids must never be recorded without a durable
    row in the same transaction.
  - `adapter-claude-code.md`: transcript metadata normally lands after the
    start row, but ingester update/finalize handling defensively stubs
    out-of-order sidecar/meta repairs.
- Regression tests:
  - `internal/ingest/notify_producer_test.go::TestEmitNotify_SessionUpdatedBeforeStartCreatesStubAndNotifies`
  - `internal/ingest/notify_producer_test.go::TestEmitNotify_SessionFinalizedBeforeStartCreatesStubAndNotifies`
- The targeted test failed before the fix with the same `lookup root for
  session ... sessions row ... absent in batch tx` error.
- Implementation:
  - `internal/ingest/writer.go` now routes both update/finalize paths through
    `requireSessionID` before the update and dirty/notify marking.

Milestone 7 install/reingest evidence:

- Stopped exact systemd units:
  `ai-viewer-ingest.service` and `ai-viewer-serve.service`.
- Deleted only the derived installed DB files:
  `/opt/ai-viewer/data/index.db`,
  `/opt/ai-viewer/data/index.db-wal`, and
  `/opt/ai-viewer/data/index.db-shm`.
- Ran `scripts/install-system.sh`; build, bundle-size gate, unit rendering,
  binary install, and service start all succeeded.
- Installed services after reinstall:
  - `systemctl is-active ai-viewer-ingest.service ai-viewer-serve.service`
    returned `active` / `active`.
  - `/api/health` returned `schema_version: 12` and five configured sources.
  - All five sources were visible as `lifecycle_state='scanning'` immediately
    after startup, proving eager lifecycle persistence on a fresh DB.
- Fresh DB progress sample after reinstall:
  - Initial sample: `sessions=1886`, `turns=6204`, `ops=25351`,
    `payload_refs=33531`.
  - Later sample: `sessions=5856`, `turns=17209`, `ops=79419`,
    `payload_refs=104134`.
  - Codex cursor advanced from 59 to 96 files during the sample window.
- Fresh Codex scan status:
  - Codex is still scanning historical files and had not reached the reported
    June 27 rollout at the latest sample.
  - Latest sampled Codex cursor file was under `2025/09/07`.
  - The exact reported session id still had `target_count=0` at that sample.
  - This is expected during a full cold DB rebuild because startup Scan walks
    historical source files before reaching the newest Codex rollout.
- Journal validation since reinstall:
  - No `DROPPING`, `lookup root`, `batch failed`, panic, or fatal installed
    ingester log recurred after the writer fix.
  - One old malformed Codex legacy JSON file produced a normal adapter parse
    warning. That is acceptable: it is logged and surfaced, not silent.

TurnView gate hygiene fix:

- The full aggregate gate reached frontend Vitest and failed because
  `CopyTurnButton` left a 1.5s copied-state reset timer alive after unmount.
  Vitest reported `ReferenceError: window is not defined` from the timer after
  jsdom teardown.
- Fixed `frontend/src/components/TurnView/TurnView.tsx` to store the timeout in
  a ref, clear any previous timer before scheduling a new one, clear it on
  unmount, and clear it on clipboard failure.
- Added
  `frontend/src/components/TurnView/TurnView.test.tsx::clears the copied reset timer when unmounted`.
  This is a gate-hygiene fix for an existing UI component, not part of the
  ingest behavior surface.

Cold-rebuild recency-priority defect found during installed reingest validation:

- The fresh installed rebuild used the fixed per-source lifecycle path, but the
  Codex scanner still walked modern rollout files in ascending historical order.
  That meant the reported recent Codex session could remain absent from the UI
  for a long cold rebuild even though realtime tail liveness was fixed.
- This is part of the SOW-0114 recovery acceptance path, not a separate
  product feature: the approved recovery step deletes the derived installed DB,
  so recent Codex sessions must surface before old history during that rebuild.
- Spec update:
  - `adapter-codex.md` now requires modern rollout full scans to process files
    in descending relative-path order, newest date and filename first, while
    preserving per-file line order, per-file cursor state, and idempotent resume.
- Regression test:
  - `internal/adapters/codex/scanner_test.go::TestDiscover_MultiShardNewestFirst`
    failed before the code change because discovered modern rollout files were
    sorted oldest-first.
- Implementation:
  - `internal/adapters/codex/discovery.go` now sorts modern rollout files
    newest-first by relative path.
- Focused gates after the fix:
  - `go test ./internal/adapters/codex -run TestDiscover_MultiShardNewestFirst -count=1`
  - `go test ./internal/adapters/codex -count=1`
  - `go test -race ./internal/adapters/codex -count=1`
  - `go test ./internal/adapters/aiagent_v2 ./internal/adapters/aiagent_v3 ./internal/adapters/claude_code ./internal/adapters/codex ./internal/adapters/opencode -count=1`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`

Same-failure scan:

- Initial read-only evidence recorded in the execution log.
- Fresh install/reingest confirms the previous notify-root batch-drop class is
  fixed for the installed binary so far.
- Exact reported Codex-session verification is complete on the rebuilt
  installed DB.

Final local gate evidence before implementation review:

- `git diff --check`: passed.
- `scripts/gates.sh`: passed every configured local quality gate.
- Aggregate gate sections included lint formatter-scope self-test, benchmark
  regression gate, `scripts/lint.sh`, sensitive-data scans, AI-attribution scan,
  contract-matrix self-test, spec-drift self-test and live drift check,
  ingestion parity self-test and fixture gate, Codacy self-tests, system
  installer and systemd unit checks, `scripts/build.sh`, `scripts/test.sh`,
  `scripts/check-coverage.sh`, adapter fuzz seed corpus, and headless
  Playwright E2E/axe.
- `scripts/test.sh` inside the aggregate was race-clean, frontend coverage
  passed, and Go total coverage was 81.6% statements.
- `scripts/check-coverage.sh coverage.out` inside the aggregate reported gated
  internal aggregate coverage 85.2%, with every gated package at or above 80%.
- Frontend coverage inside the aggregate passed 76 files / 931 tests, aggregate
  90.08% lines.
- Headless Playwright inside the aggregate automatically used
  `AI_VIEWER_E2E_PORT=17710` because the installed service owns
  `127.0.0.1:7710`; 43 tests passed.
- Post-round-1 `scripts/gates.sh` rerun after the repair-active deferral fix
  passed every configured local quality gate. Summary:
  - benchmark regression gate passed after temporarily stopping only the
    installed ingester for a valid workload sample;
  - `scripts/lint.sh`, standalone gosec, and govulncheck passed;
  - secrets and AI-attribution scans passed over tracked files;
  - spec drift, contract matrix, ingestion parity, installer, systemd, build,
    Go race/coverage, adapter fuzz seeds, and headless Playwright/axe passed;
  - Go total coverage was 81.6%, gated internal aggregate coverage was 85.2%,
    `internal/ingest` coverage was 81.1%, frontend coverage was 90.08% lines,
    and 43 Playwright tests passed on the alternate E2E port.

Implementation review readiness:

- Original goal is written in `## Requirements`.
- Accepted gap analysis and implementation plan are recorded above, including
  reviewer convergence for `NOTHING MORE CAN BE DONE` and
  `READY FOR IMPLEMENTATION`.
- Implementation evidence, focused tests, installed recovery evidence, exact
  Codex-session verification, and full aggregate gate evidence are recorded in
  this `## Validation` section.
- CTO self-review sweep before implementation review:
  - checked specs and code for the new Codex cold-rebuild recency behavior;
  - verified the out-of-order session update/finalize notify-root class with
    failing tests before code and focused passing tests after code;
  - reran full local gates after the final lint cleanup;
  - verified installed services remain active and the rebuilt DB continues to
    ingest source rows.
- External implementation reviewers must review the whole SOW and diff, not only
  the final Codex scan-order and TurnView timer cleanup patches. No `claude`
  reviewer is used.

Sensitive data gate:

- `scripts/scan-secrets.sh`: passed over tracked files.
- `scripts/scan-ai-attribution.sh`: passed.

Artifact maintenance gate:

- AGENTS.md: no SOW-0114-specific change required.
- Runtime project skills: updated where the source-discovery file move changed
  quality/spec-drift guidance.
- Specs: updated before tests/code for lifecycle, read-model repair,
  adapter heartbeats/cursor handoff, health/API fields, installed deployment,
  and the notify-root out-of-order session update/finalize defect.
- End-user/operator docs: `docs/runbook.md` updated for lifecycle/reingest
  operator evidence.
- End-user/operator skills: no separate portable operator-skill update required.
- SOW lifecycle: remains `current/` until exact installed-session verification,
  aggregate gate, and implementation review finish.

Specs update:

- Complete for the implemented behavior; latest structural drift gate is clean.

Project skills update:

- Complete where needed for this SOW's spec-drift/quality-gate file paths.

End-user/operator docs update:

- `docs/runbook.md` updated.

End-user/operator skills update:

- Not applicable for this SOW.

Lessons:

- A notification producer failure can expose missing writer-row invariants even
  when focused lifecycle tests are green. Any future path that marks sessions,
  turns, or ops dirty must prove the referenced root row exists in the same
  transaction before notify rows are emitted.
- Installed recovery validation can reveal adjacent acceptance-path defects even
  after the primary liveness implementation is correct. The Codex cold-rebuild
  scan order was one such defect, and it is now pinned by spec and test.

Follow-up mapping:

- If other adapters show the same "recent history appears late during a fresh
  rebuild" behavior, open adapter-specific SOWs with source-native evidence.
  This SOW fixed the observed Codex recovery path only.

## Implementation Review Round 1 - 2026-06-28

Reviewer gate:

- `glm`: NEEDS WORK.
- `minimax`: PRODUCTION GRADE, with P2/P3 follow-ups.
- `kimi`: NEEDS WORK.
- `mimo`: NEEDS WORK.
- `deepseek`: NEEDS WORK.
- `qwen`: technical failure before final output. Per the reviewer protocol, this
  reviewer is not retried while accepted P1/P2 findings exist; it will be
  included in the next full same-scope implementation-review round.

Accepted blocker classes:

- Startup/recovery wiring:
  - `ReconcileSourceLifecycles` exists and is tested, but no production startup
    path calls it.
  - The same class includes startup residue recovery for
    `read_model_state='repairing'`, configured-source `starting` rows, and
    unconfigured historical sources.
- Race-aware lifecycle transitions:
  - General lifecycle writes do not yet support expected-current-state guards.
  - Watchdog-specific direct SQL uses guards, but the durable lifecycle API
    does not offer the same protection to supervisor paths.
- Pre-submit and shutdown transition resilience:
  - The mandatory `starting` lifecycle row is written once, without
    context-cancellable 1s/2s/.../60s retry.
  - The shutdown-before-Scan transition lacks an explicit regression test.
- Diagnostic hygiene:
  - `lifecycle_error` and `read_model_error` are only whitespace-collapsed and
    byte-truncated. They do not yet strip controls, redact configured source
    locations/home prefixes, or truncate at a guaranteed UTF-8 boundary.
- Derived read-model correctness:
  - Workers do not know when a global FTS/rollup rebuild is active. Because
    Tail now runs during the retained global backfill window, incremental
    worker refresh can interleave with full truncate/reinsert rebuilds.
  - Source-scoped read-model repair does not set the source deferral flag while
    repair owns the source's FTS/rollup rows.
  - Read-model repair failure is visible but not yet bounded by timeout,
    retried with backoff, or reset on `ready`.
- Restart/catch-up completeness:
  - Catch-up Scan errors on restart can follow the startup recoverable-scan path
    and Tail anyway; restart catch-up failures must stay in
    `tail_restarting` and retry from the durable cursor.
  - Sustained Tail restart failure escalation is not implemented.
  - Opencode's fresh-adapter catch-up Scan -> warm Tail restart boundary needs
    a dedicated regression test.
- API/query consistency:
  - `/api/sources?include=cursors` exposes `source_progress.updated_at` without
    the zero-to-null normalization used by the non-cursor and health paths.
- Scope discipline:
  - Session-detail UI files, SOW-0088 regression notes, and pending UI SOWs are
    unrelated to SOW-0114 and must not be staged with this implementation.
  - The TurnView timer cleanup is in scope as gate hygiene discovered by the
    SOW-0114 full aggregate gate and already has a focused regression test.

Round-1 class sweep before fixes:

- Startup/recovery: searched production call sites for
  `ReconcileSourceLifecycles`; none exist. Read `main.go`, `sources.go`, and
  `source_lifecycle.go` to identify the correct insertion point after source
  resolution/metadata registration and before the source startup loop.
- Lifecycle state writes: inspected `RecordSourceLifecycle`,
  `updateLifecycleColumns`, `tail_liveness.go`, and supervisor state writes.
  Result: watchdog SQL has narrow guards; the shared lifecycle API needs an
  optional expected-state guard and no-op semantics on lost races.
- Diagnostic paths: inspected ingester and presenter sanitizers and tests.
  Result: both surfaces need the same safe diagnostic normalization, and tests
  must cover control bytes, exact configured source locations, home prefixes,
  and multibyte truncation.
- Derived read models: inspected `BackfillReadModels`, `RepairSourceReadModels`,
  worker read-model refresh, FTS repair/backfill insert paths, and rollup repair
  paths. Result: add one rebuild-active flag shared by workers and source
  repair, and keep canonical/progress/notify commits live while derived refresh
  is deferred.
- Restart/retry: inspected the source supervisor run/restart loops and existing
  lifecycle restart tests. Result: add restart catch-up failure behavior,
  sustained-failure escalation, read-model repair retry/timeout, and focused
  tests for the missing transitions rather than rerunning reviewers after only
  line-local patches.

Round-1 fixes and local evidence:

- Startup/recovery wiring:
  - `cmd/ai-viewer-ingest/main.go` now calls
    `ReconcileSourceLifecycles` after source discovery and metadata
    registration, before starting sources.
  - Reconciliation now transitions configured crash residues to `starting`,
    unconfigured rows to `stopped`, and persisted
    `read_model_state='repairing'` to `repair_pending`.
- Race-aware lifecycle transitions:
  - `RecordSourceLifecycle` now supports an expected-current-state guard.
    Lost races are no-op transitions with no notify row.
- Pre-submit and shutdown resilience:
  - Pre-submit `starting` lifecycle writes now retry with
    context-cancellable 1s/2s/.../60s backoff.
  - Shutdown-before-Scan now records `stopped` with a short
    `context.WithoutCancel` timeout so parent cancellation does not suppress
    the durable terminal state.
- Diagnostic hygiene:
  - Ingester and presenter diagnostic sanitizers now normalize invalid UTF-8,
    strip controls, collapse whitespace, redact the configured source location
    and home prefix, and truncate at a valid UTF-8 boundary.
- Derived read-model correctness:
  - Workers receive an ingester-wide `readModelRebuildActive` flag and defer
    incremental FTS/rollup refresh during global rebuilds while still
    committing canonical/progress/notify data.
  - Source-scoped repair no longer sets the source deferral flag while it owns
    source-derived rows. The round-1 race gate proved that repair-active
    deferral can strand Tail-time FTS rows; source repair is row/bucket scoped
    and SQLite-serialized while Tail incremental refresh remains enabled.
  - Read-model repair retries real errors with backoff, restores interrupted
    repairs to `repair_pending`, and resets repair attempts on `ready`. The
    later live-regression fix removed the short synthetic execution timeout
    because it could restart large repairs from the beginning forever.
- Restart/catch-up completeness:
  - Restart catch-up `Scan()` failures keep the source in `tail_restarting`,
    record sanitized error evidence, and retry instead of starting Tail from an
    unproven cursor.
  - Sustained Tail restart failure records escalation evidence while continuing
    bounded retry.
  - Opencode has a fresh-adapter restart regression test proving catch-up Scan
    hands off to warm Tail instead of a cold snapshot.
- API/query consistency:
  - `/api/sources?include=cursors` now applies the same zero-to-null
    `source_progress.updated_at` normalization as the non-cursor path.
- Scope discipline:
  - Session-detail UI files, pending UI SOWs, and SOW-0088 changes remain
    excluded from the SOW-0114 commit scope. The TurnView timer cleanup remains
    tracked only as gate hygiene discovered by this SOW's aggregate gate.

Focused gates after round-1 fixes:

- `go test ./internal/ingest -run 'TestRecordSourceLifecycle|TestRecordReadModelState|TestReconcileSourceLifecycles|TestTailHeartbeatPersistsWithThrottle|TestTailWatchdogMarksStaleAndRequestsRestart|TestRepairSourceReadModels|TestWorkerSkipsDerivedRefreshDuringGlobalRebuild' -count=1`
- `go test ./cmd/ai-viewer-ingest -run 'TestStartSource|TestRunAdapterStartsTailBeforeGlobalBackfillDone' -count=1`
- `go test ./internal/presenter -run 'TestSources_ZeroLifecycleTimestampsAreUnset|TestSources_ListsLifecycleAndReadModelState|TestHealth_DegradedOnLifecycleFailure' -count=1`
- `go test ./internal/adapters/opencode -run 'TestAdapter_FreshRestartScanThenTailWarmHandoff|TestAdapter_ScanThenTailHandoff' -count=1`
- `go test ./cmd/ai-viewer-ingest -count=1`
- `go test ./... -count=1`
- First post-fix `scripts/gates.sh` failed in
  `go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...`
  on
  `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceTailCommitsWhileAnotherSourceScanIsBlocked`:
  Tail primary rows committed, but `fts_ops` for the Tail op remained absent
  under `-race`.
- Root cause: source-scoped read-model repair set the same source's deferral
  flag while repair ran. Tail could emit and commit primary rows while that
  repair-active deferral was set; the worker correctly skipped FTS/rollup and
  recorded durable repair debt, but the supervisor's already-running repair pass
  could miss those later Tail rows and then mark the source `ready`.
- Fix: reject repair-active per-source deferral for this SOW. Source-scoped FTS
  repair is row-id scoped and SQLite-serialized, and rollup repair recomputes
  closed buckets through the same writer connection. Same-source Tail
  incremental FTS/rollup refresh remains enabled during source-scoped repair.
  The global `readModelRebuildActive` flag remains the deferral mechanism for
  full truncate/rebuild reconciliation.
- Focused race proof after the fix:
  `go test -race ./cmd/ai-viewer-ingest -run TestSourceTailCommitsWhileAnotherSourceScanIsBlocked -count=10 -v`
  passed 10/10.

Pending before round-2 implementation review:

- Rerun the same broad implementation-review prompt against the whole SOW and
  diff, adding only short notes about the round-1 fixes above.

## Implementation Review Round 2 - 2026-06-28

Reviewer gate:

- `glm`: NEEDS WORK.
- `minimax`: PRODUCTION GRADE, with P3 scope/legacy-helper notes.
- `kimi`: NEEDS WORK.
- `mimo`: NEEDS WORK.
- `deepseek`: NEEDS WORK.
- `qwen`: NEEDS WORK.

Accepted blocker classes:

- Persisted Tail restart counters:
  - The supervisor reset its in-memory consecutive restart counter after a
    successful `tail_restarting -> tailing` transition, but
    `source_progress.tail_restart_count` only ever incremented. The SOW and
    specs define this as consecutive restart-failure evidence, so the durable
    counter must reset after successful Tail recovery.
- Stopped-source health semantics:
  - `lifecycle_state='stopped'` was not degraded by the lifecycle branch, but
    `/api/health` still evaluated stale/failed read-model state afterward.
    An unconfigured historical source with `stopped + repair_failed` could
    permanently degrade health even though no repair loop owns it anymore.
- Lifecycle/backoff test coverage:
  - The initial pre-submit lifecycle insert path needed direct test coverage
    proving `source_progress.updated_at` is set on first insert and preserved
    by later lifecycle-only transitions.
  - The bounded exponential backoff arithmetic needed direct test coverage for
    doubling and the 60-second cap.
- Durable cursor comment/spec hygiene:
  - Go comments still referred to the legacy `sources.cursor` column as the
    persisted cursor in some canonical/adapter files. The durable cursor is
    `source_progress.cursor`; `sources.cursor` is historical schema only.
  - SOW evidence still named planned aiagent_v2/v3 cursor tests instead of the
    implemented package-local names.
- Commit-scope discipline:
  - Session Detail UI files, SOW-0088 regression notes, pending UI SOWs
    SOW-0107..SOW-0113, and `frontend/tests/session-layout.spec.ts` are
    unrelated dirty-worktree changes. They must not be staged with SOW-0114.

Rejected or already-covered findings:

- Codex side-effect import in `cmd/ai-viewer-ingest/source_discovery.go`:
  rejected as already covered. The import has an explicit comment explaining
  that the adapter registers its factory via `init()` for auto-discovery.
- `startPostScanBackfill` using `DeferReadModels()`: accepted only as a
  maintainability/comment issue. The helper is the retained all-sources
  background reconciliation pass, not a Tail-start gate.
- `internal/ingest/writer.go` out-of-order session update/finalize fix:
  rejected as out-of-scope only. It was an operational defect found during
  installed reingest validation, documented in this SOW, reflected in
  `ingester.md`, and pinned by notify-root regression tests.
- Session Detail dirty files: accepted as commit-scope contamination risk, not
  as an implementation defect in SOW-0114. They remain unstaged for this SOW.

Round-2 class sweep before fixes:

- Lifecycle counters and degraded states:
  - Inspected every `TailRestartDelta` write, `tail_restart_count` presenter/API
    projection, lifecycle-state degradation switch, stopped-source
    reconciliation path, and read-model degradation branch.
  - Result: add a lifecycle-update reset flag for Tail restart count; add
    `stopped` as a terminal non-degraded lifecycle state before read-model
    degradation is evaluated.
- Test coverage named by the SOW:
  - Searched for the planned/actual test names covering lifecycle initial
    insert, backoff, aiagent_v2/v3 scan-to-tail handoff, and restart
    escalation.
  - Result: add missing focused tests and update SOW evidence to the actual
    implemented package-local aiagent_v2/v3 test names.
- Cursor/comment hygiene:
  - Searched Go code comments for `sources.cursor`.
  - Result: update canonical/adapter comments to `source_progress.cursor`.
    Remaining spec mentions of `sources.cursor` are intentional legacy-column
    policy statements.
- Source-discovery and background reconciliation comments:
  - Inspected `source_discovery.go`, `main.go`, and `sources.go` comments.
  - Result: the Codex side-effect import was already documented; the retained
    background reconciliation helper now explicitly states that its done channel
    does not gate per-source Tail startup; the legacy `runAdapter` helper is
    documented as test-only.

Round-2 fixes and local evidence:

- Durable Tail restart reset:
  - `SourceLifecycleUpdate` now has `ResetTailRestartCount`.
  - `updateLifecycleColumns` uses
    `tail_restart_count = CASE WHEN ? THEN 0 ELSE tail_restart_count + ? END`.
  - `sourceSupervisor.runTail` captures whether restarts occurred, resets the
    in-memory escalation state, and persists the durable counter reset when the
    source re-enters `tailing`.
  - Specs updated in `ingester.md`, `data-model.md`, and `observability.md`.
  - Tests:
    `internal/ingest/source_lifecycle_test.go::TestRecordSourceLifecycle_ResetsTailRestartCount`
    and
    `cmd/ai-viewer-ingest/lifecycle_restart_test.go::TestStartSourceRestartsAfterTailFailure`.
- Stopped-source health:
  - `sourceLifecycleDegraded` now returns false for `stopped` before evaluating
    read-model degradation.
  - Tests:
    `internal/presenter/health_helpers_test.go::TestHealthBuildSource_StoppedIgnoresReadModelDegradation`
    and
    `internal/presenter/health_test.go::TestHealth_StoppedSourceWithFailedReadModelDoesNotDegrade`.
- Lifecycle/backoff coverage:
  - Added
    `internal/ingest/source_lifecycle_test.go::TestRecordSourceLifecycle_InitialInsertSetsUpdatedAt`.
  - Added
    `cmd/ai-viewer-ingest/lifecycle_restart_test.go::TestNextSourceBackoffDoublesAndCaps`.
- Cursor/comment hygiene:
  - Updated canonical and adapter Go comments from `sources.cursor` to
    `source_progress.cursor`.
  - Updated the SOW planned aiagent_v2/v3 cursor test names to the actual
    implemented tests:
    `internal/adapters/aiagent_v2/tailer_test.go::TestScanThenTail_NoLossInWindow`
    and
    `internal/adapters/aiagent_v3/tailer_test.go::TestScanThenTail_NoLossInWindow`.
- Background reconciliation/legacy helper comments:
  - `startPostScanBackfill` now documents that `DeferReadModels()` only checks
    whether startup scan deferral was enabled and that its done channel is not
    passed to Tail startup.
  - `runAdapter` is documented as a legacy scan-then-tail helper retained for a
    focused background-backfill regression test; production source lifecycle is
    owned by `sourceSupervisor`.

Focused gates after round-2 fixes:

- `go test ./internal/ingest -run 'TestRecordSourceLifecycle_InitialInsertSetsUpdatedAt|TestRecordSourceLifecycle_ResetsTailRestartCount|TestRecordSourceLifecycle_UpsertsStateAndPreservesCursor' -count=1`
- `go test ./cmd/ai-viewer-ingest -run 'TestNextSourceBackoffDoublesAndCaps|TestStartSourceRestartsAfterTailFailure|TestStartSourceEscalatesSustainedTailRestartFailures|TestStartSourceRestartCatchupScanFailureRetriesWithoutTail' -count=1`
- `go test ./internal/presenter -run 'TestHealthBuildSource_StoppedIgnoresReadModelDegradation|TestHealth_StoppedSourceWithFailedReadModelDoesNotDegrade|TestHealth_DegradedOnLifecycleFailure|TestHealthBuildSource_ReadModelRepairGrace' -count=1`
- `go test ./internal/presenter -count=1`
- `! rg -n 'sources\.cursor' --glob '*.go' internal cmd`
- `! rg -n 'TestAiagentV2_ScanCursorHandoffCatchesRecordsAppendedBeforeTail|TestAiagentV3_ScanCursorHandoffCatchesRecordsAppendedBeforeTail|TestLifecycle_TailRestartBackoffAndEscalation' .agents/sow/current/SOW-0114-20260628-ingest-tail-liveness.md`

Full aggregate gate after round-2 fixes:

- `scripts/gates.sh` passed in 541 s.
- Gate summary:
  - benchmark regression gate: passed.
  - `scripts/lint.sh`: Go module tidy, gofmt, goimports, `go vet`,
    `golangci-lint`, standalone `gosec`, `govulncheck`, frontend ESLint,
    TypeScript, bundle/coverage-config self-tests all clean.
  - secrets and AI-attribution scans: passed.
  - contract-matrix and spec-drift self-tests: passed.
  - `scripts/spec-drift.sh`: passed across REST, SSE, data-model, canonical,
    adapter-probes, and contract-matrix indicators.
  - ingestion parity self-test and fixture gate: passed.
  - Codacy coverage/config self-tests: passed.
  - installer and systemd unit gates: passed.
  - build: frontend build, bundle-size gate, embedded frontend, and both Go
    binaries passed.
  - Go race/coverage: `go test -race -count=1 -coverprofile=coverage.out
    -covermode=atomic ./...` passed; total coverage 81.6%.
  - Go coverage gate: gated internal aggregate 85.2%; every gated internal
    package >= 80%; `internal/ingest` 81.1%; `internal/presenter` 89.3%.
  - adapter fuzz seed corpus: passed.
  - frontend Vitest coverage: 76 files / 931 tests passed; 90.08% lines.
  - Playwright/axe: 43 Chromium tests passed on `127.0.0.1:17710` because the
    installed app occupied `127.0.0.1:7710`.

P3 findings documented, not blocking:

- `~/` shorthand path sanitization is not added in this SOW because current
  adapter errors emit absolute paths or configured source locations; exact
  source paths and home prefixes are sanitized.
- Watchdog stale-source processing returns on the first DB write error and
  retries on the next tick; this can delay other stale sources by one tick but
  does not lose recovery.
- The 24-hour Tail restart escalation branch uses `time.Now()` directly. The
  consecutive-failure branch is tested with lowered thresholds.

Pending before round-3 implementation review:

- Run the full aggregate gate again after the round-2 fixes.
- Rerun the same broad implementation-review prompt against the whole SOW and
  diff, adding only short notes about the round-1 and round-2 fixes above.

## Implementation Review Round 3 - 2026-06-28

Reviewer gate:

- `qwen`: PRODUCTION GRADE, with P3 naming/duplication/testability notes.
- `deepseek`: PRODUCTION GRADE, with P3 shutdown/watchdog/testability notes.
- `minimax`: NEEDS WORK.
- `glm`: technical failure; command timed out after 30 minutes before a final
  vote.
- `mimo`: technical failure; process ended before a final vote was captured.
- `kimi`: technical failure; process failed while summarizing before a final
  vote.

Accepted blocker classes:

- Shutdown terminal-state persistence:
  - `recordTailCancelTimeout()` recorded `tail_failed` through `s.record()`,
    which uses the supervisor's parent context. During process shutdown that
    context is already cancelled, so a Tail that ignores cancellation past the
    grace period could silently lose the terminal lifecycle write even though
    the store had not closed yet. This violated the SOW shutdown-ordering
    contract and the no-silent-failures rule.
- Durable Tail restart counter after process restart:
  - Round 2 reset the durable restart counter only when the same supervisor
    instance had in-memory restart history. If the daemon restarted after
    persisted `tail_restart_count > 0`, the next healthy `tailing` transition
    could leave the durable "consecutive" counter stale.
- Runbook source/migration accuracy:
  - `docs/runbook.md` still described claude-code, codex, and opencode as
    future adapters even though SOW-0114 installs and verifies all five source
    families.
  - The schema-mismatch troubleshooting entry named a generic schema error but
    not the actual serve log line, `schema_meta.version` symptom, or
    ingester-first repair path.

Rejected or documented non-blockers:

- `NewFatalScanError` unused by real adapters: documented as P3. The marker and
  supervisor handling are implemented and tested with fake adapters. No current
  real adapter has an identified fatal Scan condition that must use it in this
  SOW.
- Restart catch-up progress logs: documented as P3. The source remains
  observable as `tail_restarting`; bounded per-row progress logging can be a
  future observability polish item.
- Duplicate presenter stale-tail computation and legacy `runAdapter` naming:
  documented as P3/P2-style maintainability notes. The production path is
  covered by supervisor tests and the legacy helper is explicitly marked
  test-only.
- `handleTailReturn(nil)` stops the source: documented as P3 defensive
  hardening. Real adapters return context errors on cancellation; non-cancelled
  nil Tail return is an adapter contract violation outside this SOW.

Round-3 class sweep before fixes:

- Shutdown lifecycle writes:
  - Inspected every `s.record(...)` call in `source_supervisor.go` to identify
    which writes can run after parent cancellation.
  - Result: most shutdown paths either end with `recordStopped()` or
    `recordInterruptedReadModelRepair()`, both already detach from the cancelled
    parent context. `recordTailCancelTimeout()` was the only terminal write that
    exited without a follow-up detached write.
- Durable restart counters:
  - Inspected `tailRestartingUpdate()`, `runTail()`,
    `ResetTailRestartCount`, and process-restart reconciliation semantics.
  - Result: a healthy `tailing` transition is the correct reset point for the
    durable consecutive counter, independent of whether the current supervisor
    instance observed the earlier failures.
- Operator documentation:
  - Checked the runbook source list and schema-mismatch troubleshooting against
    `source_discovery.go`, `presenter.CheckSchema`, and `ai-viewer-serve`
    startup logging.
  - Result: docs needed small factual corrections, not design changes.

Round-3 fixes and local evidence:

- Shutdown terminal-state persistence:
  - Added `sourceSupervisor.recordWithSupervisorContext()` and routed
    `recordStopped()`, `recordInterruptedReadModelRepair()`, and
    `recordTailCancelTimeout()` through the same detached shutdown-context
    helper when `s.ctx` is already cancelled.
  - Added
    `cmd/ai-viewer-ingest/lifecycle_restart_test.go::TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation`.
    The fake Tail ignores cancellation past a shortened grace period; the test
    asserts `lifecycle_state='tail_failed'`, timeout evidence, and a
    `source_status_changed` notify row.
- Durable Tail restart counter:
  - `sourceSupervisor.runTail()` now sets `ResetTailRestartCount: true` for
    every successful transition to `tailing`; lifecycle error clearing remains
    limited to in-memory restart recovery so recoverable scan evidence is not
    erased.
  - Added
    `cmd/ai-viewer-ingest/lifecycle_restart_test.go::TestStartSourceSuccessfulTailResetsPersistedRestartCount`.
- Runbook accuracy:
  - Updated the default source discovery text to ai-agent v2/v3, claude-code,
    codex, and opencode.
  - Updated schema-mismatch troubleshooting with the actual
    `ai-viewer-serve: schema version mismatch` log line, the
    `schema_meta.version row missing` / `schema_meta.version is <got>, want 12`
    symptoms, and the `journalctl --user -u ai-viewer-ingest.service`
    ingester-first recovery path.

Focused gates after round-3 fixes:

- `go test ./cmd/ai-viewer-ingest -run 'TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation|TestStartSourceSuccessfulTailResetsPersistedRestartCount|TestStartSourceRestartsAfterTailFailure|TestStartSourceEscalatesSustainedTailRestartFailures' -count=1`
- `go test ./cmd/ai-viewer-ingest -run 'TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation|TestStartSourceSuccessfulTailResetsPersistedRestartCount' -race -count=1`
- `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
- `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`

Full aggregate gate after round-3 fixes:

- First `scripts/gates.sh` attempt stopped at the benchmark preflight because
  the workstation was busy: 1-minute load 16.24 with benchmark threshold 12.00.
  The top workload was the installed `ai-viewer-ingest.service` performing the
  requested full installed reingest.
- Paused the targeted system unit `ai-viewer-ingest.service` with systemd,
  verified it was inactive, reran the full gate after load dropped below the
  benchmark threshold, then restarted the same service. No generic process kill
  was used.
- Second `scripts/gates.sh` passed in 536 s.
- Gate summary:
  - benchmark regression gate: passed; no sec/op regression > 20%.
  - `scripts/lint.sh`: Go module tidy, gofmt, goimports, `go vet`,
    `golangci-lint`, standalone `gosec`, `govulncheck`, frontend ESLint,
    TypeScript, bundle/coverage-config self-tests all clean.
  - secrets and AI-attribution scans: passed.
  - contract-matrix and spec-drift self-tests: passed.
  - `scripts/spec-drift.sh`: passed across REST, SSE, data-model, canonical,
    adapter-probes, and contract-matrix indicators.
  - ingestion parity self-test and fixture gate: passed.
  - Codacy coverage/config self-tests: passed.
  - installer and systemd unit gates: passed.
  - build: frontend build, bundle-size gate, embedded frontend, and both Go
    binaries passed.
  - Go race/coverage: `go test -race -count=1 -coverprofile=coverage.out
    -covermode=atomic ./...` passed; total coverage 81.7%.
  - Go coverage gate: gated internal aggregate 85.2%; every gated internal
    package >= 80%; `internal/ingest` 81.1%; `internal/presenter` 89.3%.
  - adapter fuzz seed corpus: passed.
  - frontend Vitest coverage: 76 files / 931 tests passed; 90.08% lines.
  - Playwright/axe: 43 Chromium tests passed on `127.0.0.1:17710` because the
    installed app occupied `127.0.0.1:7710`.

Pending before round-4 implementation review:

- Rerun the same broad implementation-review prompt against the whole SOW and
  diff, adding only short notes about the round-3 fixes above. This is the
  final allowed implementation-review rerun for this gate under the reviewer
  waste controls unless the approach changes and the operator receives a new
  status report.

## Implementation Review Round 4 - 2026-06-28

Reviewer gate:

- `qwen`: PRODUCTION GRADE, with P3 cleanup notes only.
- `deepseek`: PRODUCTION GRADE, with P3 cleanup notes only.
- `mimo`: PRODUCTION GRADE.
- `minimax`: PRODUCTION GRADE, with P3 cleanup notes only.
- `glm`: NEEDS WORK.
- `kimi`: technical failure; it ran focused tests and a full local
  `scripts/gates.sh` successfully and produced a status summary saying
  PRODUCTION GRADE, but the command timed out before a final formatted vote.

Accepted blocker class:

- Worker-created read-model repair debt after global rebuild:
  - During the retained all-sources read-model rebuild,
    `readModelRebuildActive` makes Tail workers skip FTS/rollup refresh and
    write durable `read_model_state='repair_pending'`.
  - That write was health-visible but did not wake the source supervisor's
    in-memory repair loop. Once the worker batch committed, the debt could sit
    in the database until daemon restart.
  - This violated the SOW requirement that `repair_pending`,
    `repair_timeout`, and `repair_failed` be automatically retried with backoff,
    not only surfaced to health.

Round-4 class sweep before fixing:

- Searched all `repair_pending` writers and repair triggers across
  `cmd/ai-viewer-ingest`, `internal/ingest`, and presenter health.
- Result:
  - Startup-scan deferral debt is already owned by the source supervisor:
    `clearReadModelDeferral()` sets the in-memory pending flag and starts the
    repair loop while Tail continues.
  - Interrupted source repair correctly records durable `repair_pending` from
    the supervisor and keeps the in-memory loop state.
  - Startup reconciliation repairs only prior `repairing` crash residue to
    `repair_pending`; it does not solve same-run worker-created pending debt.
  - The only unbridged class was worker-created repair debt during
    `readModelRebuildActive`.

Round-4 fix:

- Added an ingester-owned read-model repair request registry:
  - `RegisterSourceReadModelRepair(sourceID, ch)` registers the owning source
    supervisor's coalesced repair channel.
  - `RequestSourceReadModelRepair(sourceID)` wakes that supervisor and logs if
    no supervisor is registered.
- Worker batches now track when `markReadModelRepairPending()` changed durable
  read-model state and send the repair request only after the SQL transaction
  commits. Rollbacks therefore do not wake a repair for uncommitted debt.
- The source supervisor now has a `repairRequests` channel and handles it while
  Tail remains active. A repair request sets the in-memory pending flag and
  starts the existing repair loop, preserving parent-context cancellation,
  exponential backoff, `repair_failed` / `repair_timeout` health visibility,
  and `ready` reset semantics without adding a short synthetic execution
  timeout.
- Added regression coverage:
  - `internal/ingest/read_model_repair_test.go::TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending`
    now asserts a committed rebuild-window batch emits exactly one repair
    request.
  - `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceReadModelRepairRequestRepairsDurablePendingDebt`
    starts a real source supervisor, records durable `repair_pending`, deletes
    an FTS row, sends a repair request, and proves the same daemon run returns
    `read_model_state='ready'` and restores the FTS row.

Validation after round-4 fix:

- Focused tests passed:
  - `go test ./internal/ingest -run 'TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending|TestRepairSourceReadModels_RebuildsDeferredSourceFTSAndFormatRollups' -count=1`
  - `go test ./cmd/ai-viewer-ingest -run 'TestSourceReadModelRepairRequestRepairsDurablePendingDebt|TestSourceTailCommitsWhileAnotherSourceScanIsBlocked' -count=1`
  - `go test ./cmd/ai-viewer-ingest -run 'TestSourceReadModelRepairRequestRepairsDurablePendingDebt|TestSourceTailCommitsWhileAnotherSourceScanIsBlocked' -race -count=1`
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
  - `go test ./internal/ingest -run 'TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending|TestRecordSourceLifecycle|TestReconcileSourceLifecycles|TestTail' -race -count=1`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
- Full aggregate gate passed:
  - `scripts/gates.sh` passed every configured local gate in 1009 s.
  - Benchmark regression gate passed; no significant `sec/op` regression above
    the 20% threshold.
  - Lint/static/security passed: module tidiness, `gofmt`, `goimports`,
    `go vet`, `golangci-lint`, standalone `gosec`, `govulncheck`, frontend
    ESLint, TypeScript, and frontend gate self-tests.
  - Secrets scan and AI-attribution scan passed.
  - Contract-matrix, spec-drift, ingestion parity, Codacy, installer, and
    systemd unit gates passed.
  - Build passed: frontend build, bundle-size gate, embedded frontend, and both
    Go binaries.
  - Go race/coverage passed; total coverage 81.6%, gated internal aggregate
    85.1%, `internal/ingest` 80.4%, `internal/presenter` 89.3%.
  - Adapter fuzz seed corpus passed.
  - Frontend Vitest coverage passed: 76 files / 931 tests, 90.08% lines.
  - Playwright/axe passed: 43 Chromium tests on `127.0.0.1:17710` because the
    installed app occupied `127.0.0.1:7710`.
- Reviewer waste control:
  - The accepted P2 produced an approach change, a class sweep, and local proof
    before any further reviewer spend.
  - The next implementation-review run must use the same broad SOW/diff scope,
    with only short notes about the round-4 fix and the validation above.

## Live Regression - 2026-06-29

After installing commit `a35dc5c` and rebuilding `/opt/ai-viewer/data/index.db`
from scratch, the missing Codex session was recovered, but the live install
remained degraded.

Evidence:

- `/api/health` returned `status="degraded"`.
- The previously missing Codex native session
  `019f08f4-9dc6-7251-aa4a-ccdeec9ea9b2` existed in SQLite as canonical session
  `a5cae7c3ed823fdbaf88a313e27f79fb`, with 117 turns and more than 15k ops.
- `source_progress` persisted `lifecycle_state='tailing'` for aiagent_v3,
  claude-code, and Codex, but `/api/health` derived effective `tail_stale`
  because `tail_heartbeat_at` was old and `tail_restart_count` remained `0`.
- Journald contained Tail start lines for aiagent_v3, claude-code, and Codex,
  but no stale-tail restart lines.
- Journald repeatedly logged `repairing source read models` for aiagent_v3,
  claude-code, and Codex, but no `source read models repaired` completion lines.
- Live FTS coverage was partial or absent while canonical ops were present:
  Codex had more than 1.3M ops with only about 22k `fts_ops` rows; opencode and
  aiagent_v2 had canonical ops and zero `fts_ops` rows.

Root-cause model:

- Tail-liveness heartbeat persistence and stale-tail checking shared one
  goroutine. A DB write that blocked or ran behind long repair work could starve
  the stale watchdog, leaving health to derive `tail_stale` while the ingester
  never persisted `tail_stale` or requested a supervisor restart.
- Source read-model repair used a short per-attempt timeout. On large local
  corpora, each timeout restarted the FTS repair from the beginning, so the
  system spent hours reprocessing early rows and never reached `ready`.

Spec deltas for this live-regression fix:

- `.agents/sow/specs/ingester.md` now states that source-scoped read-model
  repair runs to completion while the source supervisor context is alive and is
  not killed by a short wall-clock timeout that restarts work from zero.
- `.agents/sow/specs/ingester.md` now states that heartbeat persistence and
  stale-tail checking are independent loops, and that liveness DB writes are
  bounded/logged so a blocked write cannot wedge the liveness subsystem.

Implementation plan:

- Split tail heartbeat persistence and stale-tail checking into independent
  ingester goroutines.
- Add bounded contexts for liveness DB writes; failed writes log and retry on
  the next heartbeat/tick instead of blocking the loop forever.
- Remove the short source read-model repair execution timeout; repair remains
  cancellable by the source supervisor context and still retries real errors
  with backoff.
- Add focused tests before code:
  - liveness writes respect the configured timeout when SQLite is unavailable;
  - the watchdog still records stale state and sends a restart request after the
    DB becomes writable again;
  - source read-model repair is invoked with the supervisor context, not a
    short synthetic timeout.

Validation plan:

- Run focused `internal/ingest` tail-liveness tests.
- Run focused `cmd/ai-viewer-ingest` source-supervisor repair tests.
- Run `go test ./internal/ingest ./cmd/ai-viewer-ingest -count=1`.
- Reinstall the system service and verify the live DB no longer gets stuck in
  the same no-completion repair loop; the exact health state may remain
  degraded while aiagent_v2/opencode startup scans continue, but tail restart
  evidence and repair progress must be observable.

Follow-up live finding:

- After the first live-regression fix was installed, source read-model repair
  still starved liveness writes while repairing Codex FTS rows.
- Journald showed `repairing source read models` followed by repeated
  `tail heartbeat persist failed ... context deadline exceeded` and
  `tail staleness check failed ... context deadline exceeded`.
- SQLite query-plan evidence showed `DELETE FROM fts_ops WHERE op_id = ?` and
  `DELETE FROM fts_logs WHERE log_id = ?` scan the content-owning FTS5 virtual
  tables. Both linkage columns are `UNINDEXED`, so source repair performed
  repeated FTS scans inside writer transactions.
- Source repair query plans were also tightened to stream source rows by
  `ops.rowid` / `log_entries.id` and avoid `USE TEMP B-TREE FOR ORDER BY`.

Follow-up spec/code fix:

- `.agents/sow/specs/ingester.md` now requires source-scoped FTS repair to
  stream rows in primary storage order and to address FTS rows by stable FTS
  `rowid`: `ops.rowid` for `fts_ops`, `log_entries.id` for `fts_logs`.
- Incremental FTS maintenance, full FTS backfill, and source FTS repair now use
  `INSERT OR REPLACE` with explicit FTS `rowid`.
- Source repair deletes stale FTS rows by `rowid`, not by unindexed linkage
  columns such as `op_id` or `log_id`.
- The ingest default batch size was restored to the documented `1000` events so
  one scan flush cannot monopolize the single writer for an oversized batch.

Follow-up tests and validation:

- Added tests that failed before the rowid fix:
  - `internal/ingest::TestFTS_IncrementalOpsUseOpsRowID`
  - `internal/ingest::TestFTS_IncrementalLogsUseLogEntryID`
  - `internal/ingest::TestNew_DefaultBatchSizeMatchesSpec`
- Added source-repair query-plan coverage:
  - `internal/ingest::TestRepairSourceReadModels_SourceFTSQueriesAvoidTempSort`
- Focused validations passed:
  - `go test ./internal/ingest -run 'TestNew_DefaultBatchSizeMatchesSpec|TestTail|TestRepairSourceReadModels' -count=1`
  - `go test ./internal/ingest -run 'TestFTSParity_AllFixtures|TestFTS_IncrementalOpsUseOpsRowID|TestFTS_IncrementalLogsUseLogEntryID|TestFTS_Backfill' -count=1`
  - `go test ./internal/ingest ./cmd/ai-viewer-ingest -count=1`
  - `go test -race ./internal/ingest ./cmd/ai-viewer-ingest -run 'TestTail|TestSourceReadModelRepair|TestRepairSourceReadModels|TestStartSource|TestRunAdapter|TestNew_DefaultBatchSizeMatchesSpec|TestFTS_Incremental' -count=1`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
  - `go test ./...`
  - `scripts/lint.sh`
- The installed DB was deleted again after the rowid fix and `scripts/install-system.sh`
  was rerun. Clean rebuild evidence at `2026-06-29 09:42 EEST`:
  - `ai-viewer-ingest.service` active since `2026-06-29 09:30:22 EEST` with
    zero restarts;
  - the rebuilt DB contained 340,815 ops, 147,848 `fts_logs`, and deferred
    `fts_ops=0` while all sources were still in startup scan;
  - no `context deadline`, stale-tail, or heartbeat-persist failure logs matched
    the new run through that sample;
  - the reported Codex session
    `019f08f4-9dc6-7251-aa4a-ccdeec9ea9b2` was present and served by
    `/api/sessions/a5cae7c3ed823fdbaf88a313e27f79fb` with 117 turns and 205
    child sessions.
- `scripts/gates.sh` did not complete:
  - attempt 1 passed benchmark preflight while the clean ingest was running, but
    the benchmark gate reproduced an unrelated `aiagent_v2 Tail_SyntheticAppend`
    wall-time regression under host load;
  - the ingester was paused at 40,825 sessions / 660,540 ops and the gate was
    retried;
  - attempt 2 failed benchmark preflight because unrelated workstation load
    remained above the gate threshold (`15.07 >= 12.00`);
  - process evidence showed unrelated reviewer/opencode, Chromium, Netdata, and
    desktop workloads consuming CPU. Those processes were not owned by this SOW
    and were not stopped.
- After the benchmark-blocked gate attempt, `ai-viewer-ingest.service` was
  restarted. It resumed every source with `resume=true`, had zero restarts, and
  the rebuild continued to 41,205 sessions / 669,214 ops at
  `2026-06-29 10:14 EEST`. No `context deadline`, stale-tail, or heartbeat
  failure logs matched the post-restart sample.

Follow-up live finding 2:

- A later installed cold rebuild recovered the missing Codex session and moved
  aiagent_v3 plus claude-code to `tailing` / `ready`, but liveness writes again
  timed out while other large sources were still scanning and repairing:
  `tail heartbeat persist failed ... context deadline exceeded` and
  `tail staleness check failed ... context deadline exceeded`.
- At the same sample, `source_progress.progress_updated_at` was fresh for every
  source, proving scanners were active rather than dead, but the single writer
  still made liveness writes miss their 30 s budget.
- Rowid-based FTS repair fixed the unindexed virtual-table scan class, but the
  restored 1000-row normal and FTS repair batches were still too large for this
  workstation's real cold-rebuild corpus.

Follow-up spec/code fix 2:

- `.agents/sow/specs/ingester.md` now treats the ingest batch size as a
  liveness budget and sets the default to 100 events / 500 ms.
- FTS backfill and source-scoped FTS repair now mirror the normal ingest default
  batch size instead of carrying an independent 1000-row writer chunk.
- `.agents/skills/project-go-backend/SKILL.md` now records the same 100-row
  transaction budget so future backend work does not reintroduce the stale
  1000-row default.

Follow-up tests 2:

- `internal/ingest::TestNew_DefaultBatchSizeMatchesSpec` now pins the spec
  default at 100.
- `internal/ingest::TestFTS_BackfillDefaultBatchSizeMatchesIngestBudget` pins
  FTS repair/backfill to the same default writer budget.

## Implementation Review Round 5 - 2026-06-28

Reviewer gate:

- `qwen`: PRODUCTION GRADE, with P3 cleanup notes only.
- `mimo`: PRODUCTION GRADE, with P3 cleanup notes only.
- `kimi`: PRODUCTION GRADE, with one P3 cleanup note.
- `deepseek`: PRODUCTION GRADE vote, but returned three P2-labeled findings and
  six P3 notes. P2 labels are treated as blocking claims until verified.
- `glm`: technical failure; command timed out before a final vote.
- `minimax`: technical failure; command timed out before a final vote.

Verified dispositions:

- Accepted as real blocker class:
  - Supervisor lifecycle writes that make a source observable/monitorable were
    best-effort after the initial `starting` write. In particular, `runTail()`
    could start the adapter Tail even if `scan_complete`, `tail_starting`, or
    the durable `tailing` state with initial heartbeat failed to persist. The
    stale-tail watchdog discovers active sources from
    `source_progress.lifecycle_state='tailing'`, so a failed `tailing` write
    could leave a live Tail unmonitored for the current daemon run.
- Reclassified to P3:
  - Duplicate presenter stale-tail effective-state helpers in `/api/health` and
    `/api/sources`. They are short and currently identical; extraction is
    maintainability cleanup, not a correctness or contract blocker.
  - `ExpectedLifecycleState` is not used by supervisor writes. The active risk
    is not the specific guard field; it is that critical supervisor writes were
    allowed to fail best-effort. The stale-tail watchdog already uses guarded
    SQL transitions and cannot overwrite terminal supervisor states. The fix
    therefore targets required durable writes before Tail/restart progress
    rather than adding an unsafe one-state guard to shutdown writes.
- P3 notes documented and not blocking:
  - Defensive handling for an adapter `Tail()` returning nil without context
    cancellation.
  - Dedicated Codex idle-heartbeat unit test parity with the other adapters.
  - Minor sanitizer/home-dir caching and UI symmetry cleanup.

Round-5 blocker class sweep:

- Checked every `sourceSupervisor` lifecycle write that can affect source
  monitorability or retry behavior:
  - pre-scan `scanning`;
  - startup `scan_failed` / `scan_complete`;
  - restart catch-up `tail_restarting` evidence;
  - pre-Tail `tail_starting`;
  - monitorable `tailing` with initial heartbeat;
  - Tail failure evidence before restart.
- Target behavior:
  - These writes must retry with cancellable backoff and the supervisor must not
    start or restart Tail until the required state is durable.
  - Shutdown-after-cancel terminal writes keep the bounded
    `context.WithoutCancel` write window, but retry inside that bounded window
    instead of issuing one best-effort write.
  - `scanDone` must still be released if a required lifecycle write cannot
    complete before context cancellation, so global startup repair does not wait
    forever on a source that will not Tail.

Round-5 fix:

- Specs updated:
  - `.agents/sow/specs/ingester.md` now states that supervisor lifecycle
    writes needed for source observability/watchdog coverage are required
    writes, and Tail must not start until `tailing` plus initial heartbeat is
    durable.
  - `.agents/sow/specs/observability.md` now records the same health contract:
    Tail without durable `tailing` state is invisible to the stale-tail
    watchdog and is therefore forbidden.
- Added regression coverage:
  - `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState`
    blocks Scan, closes the writer before Scan completes, and proves Tail does
    not start while lifecycle state cannot be persisted. The test failed before
    implementation with `Tail started even though lifecycle state could not be
    persisted`.
- Implemented required supervisor lifecycle writes:
  - `sourceSupervisor.recordRequired()` uses the existing retrying lifecycle
    write path.
  - Pre-scan `scanning`, startup `scan_failed`/`scan_complete`, restart
    `tail_restarting`, pre-Tail `tail_starting`, monitorable `tailing`, and
    Tail failure evidence now block supervisor progress until persisted or until
    the source context is canceled.
  - Shutdown-after-cancel terminal writes use the bounded supervisor context and
    retry inside that bounded window.
  - If context cancellation prevents a required write before Tail, the
    startup-scan completion signal is still released exactly once so retained
    global repair does not wait forever.

Validation after round-5 fix:

- Focused tests passed:
  - `go test ./cmd/ai-viewer-ingest -run 'TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState|TestStartSourceRecordsScanAndTailLifecycle|TestStartSourceRecordsStoppedWhenScanIsCancelled|TestStartSourceRecordsScanFailedAndContinuesToTail|TestStartSourceRecordsFatalScanFailureWithoutTail|TestSourceTailCommitsWhileAnotherSourceScanIsBlocked|TestSourceReadModelRepairRequestRepairsDurablePendingDebt|TestStartSourceRestartsAfterTailFailure|TestStartSourceRestartCatchupScanFailureRetriesWithoutTail|TestStartSourceEscalatesSustainedTailRestartFailures|TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation|TestStartSourceSuccessfulTailResetsPersistedRestartCount' -count=1`
  - `go test ./cmd/ai-viewer-ingest -run 'TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState|TestStartSourceRestartsAfterTailFailure|TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation|TestStartSourceSuccessfulTailResetsPersistedRestartCount|TestSourceReadModelRepairRequestRepairsDurablePendingDebt' -race -count=1`
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
- First full aggregate `scripts/gates.sh` after the round-5 fix failed only at
  static analysis. The failures were local maintainability issues in the new
  supervisor helper code (`staticcheck` S1008 and `unparam` on the retry helper
  return value), not test/runtime failures. The implementation was simplified
  and `gofmt` was rerun.
- Full aggregate gate passed after the lint fix:
  - `scripts/gates.sh` passed every configured local gate in 953 s.
  - Benchmark regression gate passed; no significant `sec/op` regression above
    the 20% threshold.
  - Lint/static/security passed: module tidiness, `gofmt`, `goimports`,
    `go vet`, `golangci-lint`, standalone `gosec`, `govulncheck`, frontend
    ESLint, TypeScript, and frontend gate self-tests.
  - Secrets scan and AI-attribution scan passed.
  - Contract-matrix, spec-drift, ingestion parity, Codacy, installer, and
    systemd unit gates passed.
  - Build passed: frontend build, bundle-size gate, embedded frontend, and both
    Go binaries.
  - Go race/coverage passed; total coverage 81.6%, gated internal aggregate
    85.1%, `internal/ingest` 80.4%, `internal/presenter` 89.3%.
  - Adapter fuzz seed corpus passed.
  - Frontend Vitest coverage passed: 76 files / 931 tests, 90.08% lines.
  - Playwright/axe passed: 43 Chromium tests on `127.0.0.1:17710` because the
    installed app occupied `127.0.0.1:7710`.
## Implementation Review Round 6 - 2026-06-29

Reviewer gate:

- `kimi`: PRODUCTION GRADE, with P3 cleanup notes only.
- `mimo`: PRODUCTION GRADE.
- `deepseek`: NEEDS WORK.
- `glm`: interrupted before vote after the user correctly identified that this
  round was again being used as discovery instead of validation.
- `minimax`: interrupted before vote for the same reason.
- `qwen`: interrupted before vote for the same reason.

Accepted blocker class:

- `startSourceWithFactoryLookup()` registered per-source stale-tail restart and
  read-model repair request channels before adapter construction and
  `Ingester.Submit()` completed.
- If adapter construction failed or `Submit()` returned an error, the source
  supervisor goroutine never started and therefore never ran the deferred
  unregister functions in `sourceSupervisor.run()`.
- This left stale in-memory registry entries in
  `Ingester.tailRestartChans` / `Ingester.readModelRepairChans` until process
  restart. Functional impact was low for the current five-source workstation
  deployment, but the class violates the supervisor ownership contract and
  could hide a missing-supervisor restart/repair signal.

Round-6 process failure:

- The reviewer round should not have been launched yet. This was a local
  self-review miss, not a subtle model-only finding.
- The false framing was "final validation after the round-5 fix"; the evidence
  says the artifact still had an obvious ownership/lifetime gap in the same
  supervisor area that was just changed.
- The remaining reviewer processes were stopped immediately after the user
  flagged the waste. No additional reviewer spend is justified until local
  self-review and gates are complete again.

Round-6 class sweep before fixing:

- Checked every path before `sourceSupervisor.run()` can own cleanup:
  - unknown adapter format returns before request-channel registration;
  - inaccessible source path returns before request-channel registration;
  - adapter location resolution returns before request-channel registration;
  - adapter construction returned after registration in the buggy version;
  - `Ingester.Submit()` returned after registration in the buggy version.
- Checked every path after `sourceSupervisor.run()` starts:
  - normal shutdown, scan failure, Tail failure, stale-tail restart, and source
    stop remain owned by `sourceSupervisor.run()` deferred cleanup.
- Fix target:
  - request-channel registration must occur only after adapter construction and
    `Ingester.Submit()` succeed, immediately before the supervisor goroutine
    starts.

Round-6 fix:

- Spec updated:
  - `.agents/sow/specs/ingester.md` now states that supervisor-owned restart and
    read-model repair request channels are registered only for a supervisor
    that can actually run, and failed startup must not retain stale
    registrations.
- Added regression coverage:
  - `cmd/ai-viewer-ingest/lifecycle_test.go::TestStartSourceRecordsConstructFailed`
    now proves constructor failure leaves no stale restart/repair registrations.
  - `cmd/ai-viewer-ingest/lifecycle_test.go::TestStartSourceCleansSupervisorRegistrationsOnSubmitFailure`
    proves duplicate-submit failure leaves no stale restart/repair
    registrations.
  - Both tests failed before the implementation with
    `tail restart registrations = 1, want 0`.
- Implemented cleanup by moving restart/repair request-channel registration
  until after adapter construction and `Ingester.Submit()` succeed. The
  supervisor still owns unregister after its goroutine starts.

Validation after round-6 fix:

- Focused tests passed:
  - `go test ./cmd/ai-viewer-ingest -run 'TestStartSourceRecordsConstructFailed|TestStartSourceCleansSupervisorRegistrationsOnSubmitFailure' -count=1`
  - `go test ./cmd/ai-viewer-ingest -run 'TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState|TestStartSourceRecordsScanAndTailLifecycle|TestStartSourceRecordsStoppedWhenScanIsCancelled|TestStartSourceRecordsScanFailedAndContinuesToTail|TestStartSourceRecordsFatalScanFailureWithoutTail|TestSourceTailCommitsWhileAnotherSourceScanIsBlocked|TestSourceReadModelRepairRequestRepairsDurablePendingDebt|TestStartSourceRestartsAfterTailFailure|TestStartSourceRestartCatchupScanFailureRetriesWithoutTail|TestStartSourceEscalatesSustainedTailRestartFailures|TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation|TestStartSourceSuccessfulTailResetsPersistedRestartCount|TestStartSourceRecordsConstructFailed|TestStartSourceCleansSupervisorRegistrationsOnSubmitFailure' -count=1`
  - `go test ./cmd/ai-viewer-ingest -run 'TestStartSourceRecordsConstructFailed|TestStartSourceCleansSupervisorRegistrationsOnSubmitFailure|TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState|TestStartSourceRestartsAfterTailFailure|TestSourceReadModelRepairRequestRepairsDurablePendingDebt' -race -count=1`
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
  - `golangci-lint run --timeout=5m ./cmd/ai-viewer-ingest`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
- Local class sweep completed before rerunning full gates:
  - no restart/repair request channels are registered before unknown-format,
    source-path, source-location, adapter-construction, and `Ingester.Submit()`
    startup checks complete;
  - after `sourceSupervisor.run()` starts, normal shutdown, scan failure,
    Tail failure, stale-tail restart, and explicit source stop still run
    supervisor-owned deferred cleanup.
- Full aggregate attempt 1 after the round-6 fix stopped at benchmark preflight
  due host load (`13.81 >= 12.00`); this was not a code failure.
- Full aggregate attempt 2 progressed through all non-Playwright gates, then
  failed only the `/sources` light-theme axe color-contrast check on
  `bg-status-running/10 text-status-running` badges. The failure was accepted as
  a local quality miss, fixed before any reviewer rerun, and covered by:
  - `.agents/sow/specs/ui-pages.md` requiring lifecycle/read-model status
    badges to pass axe contrast in both themes;
  - `frontend/src/theme/app.css` darkening the light-theme `--status-running`
    token;
  - focused Playwright proof:
    `AI_VIEWER_E2E_PORT=17710 npx playwright test tests/a11y.spec.ts --project=chromium -g "sources.*light"`;
  - full Playwright proof:
    `AI_VIEWER_E2E_PORT=17710 npm run e2e` passed 43/43 Chromium tests.
- Final full aggregate gate passed:
  - `scripts/gates.sh` passed every configured local quality gate in 878 s.
  - Benchmark regression gate passed; no `sec/op` regression exceeded the 20%
    threshold.
  - Lint/static/security passed: module tidiness, `gofmt`, `goimports`,
    `go vet`, `golangci-lint`, standalone `gosec`, `govulncheck`, frontend
    ESLint, TypeScript, and frontend gate self-tests.
  - Secrets scan and AI-attribution scan passed.
  - Contract-matrix, spec-drift, ingestion parity, Codacy, installer, and systemd
    unit gates passed.
  - Build passed: frontend build, bundle-size gate, embedded frontend, and both
    Go binaries.
  - Go race/coverage passed; total coverage 81.6%, gated internal aggregate
    85.1%, `internal/ingest` 80.5%, `internal/presenter` 89.3%.
  - Frontend Vitest coverage passed: 76 files / 931 tests, 90.08% lines.
  - Adapter fuzz seed corpus passed.
  - Playwright/axe passed: 43 Chromium tests on `127.0.0.1:17710` because the
    installed app occupied `127.0.0.1:7710`.

## Runtime Recovery - 2026-06-29

Actions:

- Stopped the installed system services:
  `sudo systemctl stop ai-viewer-serve.service ai-viewer-ingest.service`.
- Deleted only the derived installed SQLite index files:
  `/opt/ai-viewer/data/index.db`,
  `/opt/ai-viewer/data/index.db-shm`, and
  `/opt/ai-viewer/data/index.db-wal`.
- Reinstalled with `scripts/install-system.sh`.

Install evidence:

- `scripts/install-system.sh` rebuilt the frontend and Go binaries, installed the
  system services, and started both units successfully.
- The rendered install discovered the five expected explicit sources:
  `aiagent_v3`, `aiagent_v2`, `claude-code`, `codex`, and `opencode`.
- `systemctl is-active ai-viewer-ingest.service ai-viewer-serve.service`
  reported both services active after install.
- `systemctl status` after the 30-minute monitor showed:
  - `ai-viewer-ingest.service` active since `2026-06-29 01:44:02 EEST`;
  - `ai-viewer-serve.service` active since `2026-06-29 01:44:05 EEST`;
  - ingester memory around 1 GiB, below the configured 4 GiB/8 GiB limits.

Recovered-session evidence:

- Native Codex session `019f08f4-9dc6-7251-aa4a-ccdeec9ea9b2` is present in the
  rebuilt installed DB.
- It maps to canonical session id `a5cae7c3ed823fdbaf88a313e27f79fb`, source
  `codex:$HOME/.codex/sessions`, status `running`, model `gpt-5.5`, with
  117 turns and 11,788 ops at the time of verification.

Rebuild state after bounded monitor:

- A 30-minute poll loop watched `/opt/ai-viewer/data/index.db` after reinstall.
- At poll 60 (`2026-06-29T02:17:35+03:00`), the DB contained 30,462 sessions,
  260 Codex sessions, and 502,659 summed session ops.
- The installed DB had grown to about 999 MiB plus a 59 MiB WAL shortly after the
  monitor, so the rebuild was active and substantial.
- `/api/health` reported five sources and `status=degraded` because all sources
  were still `lifecycle_state=scanning` with `read_model_state=repair_pending`.
- No source had reached `scan_complete`/`tailing` within the bounded monitor.
- The only fresh warning found in the installed ingest log after reinstall was
  the known Codex legacy flat JSON parse warning for
  `rollout-2025-07-01-0187df2e-dbd3-4fb3-a837-8e51233dd60a.json`.

Disposition:

- The symptom that triggered this SOW is fixed in the rebuilt DB: the requested
  Codex session is now visible through installed storage/API data.
- The stronger all-source recovery proof is not complete yet. The services are
  left running so the rebuild can continue. This SOW remains `in-progress` until
  a later check proves sources have entered tailing/read-model-ready states or a
  concrete follow-up SOW records why the remaining long scan is a separate
  operational issue.

## Live Regression - 2026-06-30

Evidence:

- A ten-minute monitor of the installed fresh-ingest DB counted five new
  liveness timeout log entries after `2026-06-30T11:04:14+03:00` while
  `aiagent_v3` and `claude-code` were already in `tailing/ready`.
- The failures were `tail heartbeat persist failed` and `tail staleness check
  failed` with `context deadline exceeded`. This proves cross-source tailing now
  starts, but liveness writes can still be starved behind the single SQLite
  writer during heavy startup repair/scan work.
- Query-plan inspection of the dirty-session aggregate refresh found the
  failed-op subquery can use the global `idx_ops_status` index for
  `WHERE ops.session_id = sessions.id AND ops.status = 'failed'`, instead of a
  session-scoped index. The installed DB also has giant Codex sessions, including
  one session with more than 100k ops, so a dirty aggregate pass can hold the
  writer long enough to exceed the liveness write budget.

Follow-up fix target:

- Add aggregate-liveness support indexes:
  `idx_ops_session_status ON ops(session_id, status)` and
  `idx_ops_session_end ON ops(session_id, end_ts)`.
- Bump the schema to v13 so serve refuses a pre-v13 DB whose aggregate
  maintenance schema is known to violate the liveness contract.
- Add migration/query-plan tests proving the aggregate refresh subqueries use the
  new session-scoped indexes, then reinstall and monitor the live service again.

Follow-up live finding after v13:

- Migration `0013_aggregate_liveness_indexes.sql` applied successfully on the
  installed DB at `2026-06-30T11:36:32+03:00`; `/api/health` reported
  `schema_version=13`, and the DB contained both `idx_ops_session_status` and
  `idx_ops_session_end`.
- A 12-minute post-v13 monitor still counted four real
  `tail heartbeat persist failed` warnings:
  - `2026-06-30T11:38:29+03:00` for `claude-code`;
  - `2026-06-30T11:40:04+03:00` for `claude-code`;
  - `2026-06-30T11:40:34+03:00` for `aiagent_v3`;
  - `2026-06-30T11:44:29+03:00` for `claude-code`.
- The heartbeat failures began after source read-model repair started for
  `claude-code`. Query-plan inspection showed source FTS repair avoided temp
  sort, but it still walked global `ops.rowid` / `log_entries.id` and checked
  each row's source through `sessions`. That can scan unrelated sources while
  holding the ingester's single database connection.

Second follow-up fix target:

- Add source-repair liveness indexes:
  `idx_sessions_source_id ON sessions(source_id, id)`,
  `idx_ops_session ON ops(session_id)`, and
  `idx_log_session ON log_entries(session_id)`.
- Change source FTS repair to page source sessions first, then per-session
  `ops` / `log_entries` by rowid using the new indexes.
- Bump the schema to v14 and add query-plan tests that fail on global rowid
  repair scans or temp B-tree repair sorts.

Follow-up live finding after v14:

- Migration `0014_source_repair_liveness_indexes.sql` applied successfully on
  the installed DB at `2026-06-30T12:01:38+03:00`; `/api/health` reported
  `schema_version=14`, and the DB contained all three v14 indexes.
- A 12-minute post-v14 monitor still counted three liveness timeout warnings:
  two `tail staleness check failed` warnings and one
  `tail heartbeat persist failed` warning.
- The watchdog query is a trivial scan of `source_progress`, so the remaining
  failure is not an inefficient watchdog query. The active source read-model
  repair had not completed, which indicates repair can still repeatedly acquire
  the single DB connection ahead of liveness operations even after the repair
  queries became source/session-scoped.

Third follow-up fix target:

- Add a liveness-priority marker: heartbeat persistence and stale-tail watchdog
  operations increment a pending counter before waiting for the DB connection.
- Make source read-model repair yield between read/write batches while that
  marker is present, so liveness gets a chance to acquire the single connection
  before repair starts the next batch.

Follow-up live finding after the repair-only liveness-priority fix:

- The patched service was installed at `2026-06-30T12:25:01+03:00` with
  schema v14.
- A post-install monitor immediately found fresh liveness failures:
  - `2026-06-30T12:26:31+03:00`: `tail staleness check failed` with
    `tail staleness query: context deadline exceeded`.
  - `2026-06-30T12:27:54+03:00`: `tail heartbeat persist failed` with
    `tail heartbeat begin: context deadline exceeded`.
  - `2026-06-30T12:28:01+03:00`: another `tail staleness query` timeout.
  - By sample 5 (`2026-06-30T12:31:19+03:00`), the monitor counted four
    post-install liveness timeout warnings.
- The first failure occurred before the source read-model repair log line for
  `aiagent_v3`, so the repair-only priority rule was incomplete. Normal scan
  workers can also repeatedly acquire the single writer connection while
  liveness is pending.

Fourth follow-up fix target:

- Extend the same liveness-priority protocol to normal worker flushes and idle
  rollup refreshes before they call `BeginTx`.
- Add tests proving both worker DB entry points yield to tail-state priority
  before opening a transaction.
- Reinstall and rerun the live liveness monitor from a fresh service start.

Follow-up live finding after worker-flush priority:

- The patched service was installed at `2026-06-30T12:42:27+03:00`.
- The post-install monitor stayed clean through sample 4 while
  `aiagent_v3` and `claude-code` were tailing and read-model repairs were
  active.
- Sample 5 counted one fresh liveness timeout:
  `2026-06-30T12:46:21+03:00`, `tail heartbeat persist failed`,
  `tail heartbeat begin: context deadline exceeded`, source
  `claude-code:$HOME/.claude/projects`.
- Source repair began for `claude-code` at `2026-06-30T12:43:26+03:00`, but
  repair and worker paths were no longer the only single-connection owners.
  The background orphan resolver still opened periodic maintenance write
  transactions without checking the liveness-priority marker.

Fifth follow-up fix target:

- Extend the liveness-priority protocol to the orphan resolver before it opens
  its maintenance transaction.
- Add a resolver regression test proving it yields before `BeginTx`.
- Reinstall and rerun the live monitor from a fresh service start. If the
  timeout persists, treat the result as evidence that a single source-repair
  transaction exceeds the liveness budget and reduce that transaction
  granularity next.

Final live evidence after resolver priority:

- The resolver-priority build was installed successfully; `systemctl` reported
  `ai-viewer-ingest.service` active/running from
  `2026-06-30T12:57:09+03:00`.
- `/api/health` reported `schema_version=14`; degradation remained expected
  because large sources were still scanning or repairing, not because liveness
  had failed.
- A 12-sample installed monitor from `2026-06-30T12:57:09+03:00` counted
  `failures_since_resolver_priority=0` at every sample for:
  `context deadline`, `tail heartbeat persist failed`, and
  `tail staleness check failed`.
- Final monitor sample at `2026-06-30T13:11:35+03:00`:
  - sessions: `95,503`
  - ops: `2,157,031`
  - `fts_ops`: `380,938`
  - `fts_logs`: `1,331,841`
  - `aiagent_v3`: `tailing/repairing`, heartbeat
    `2026-06-30T10:11:28Z`, restarts `0`
  - `claude-code`: `tailing/repairing`, heartbeat
    `2026-06-30T10:11:25Z`, restarts `0`
  - `aiagent_v2`, `codex`, and `opencode` remained
    `scanning/repair_pending`, which keeps this a real contention test rather
    than an idle-system test.
- A follow-up explicit journald query after the monitor returned no matching
  timeout lines since `2026-06-30T12:57:09+03:00`.

Final implementation evidence:

- Source read-model repair, normal worker flushes, idle rollup refresh, and the
  orphan resolver now all yield to pending tail-state persistence/watchdog work
  before opening lower-priority database transactions.
- Added regression coverage:
  - `internal/ingest/worker_test.go::TestWorkerFlushYieldsToTailStateBeforeBeginTx`
  - `internal/ingest/worker_test.go::TestWorkerIdleRefreshYieldsToTailStateBeforeBeginTx`
  - `internal/ingest/resolver_test.go::TestResolverYieldsToTailStateBeforeBeginTx`
- Focused tests passed:
  - `go test ./internal/ingest -run 'TestResolverYieldsToTailStateBeforeBeginTx|TestWorkerFlushYieldsToTailStateBeforeBeginTx|TestWorkerIdleRefreshYieldsToTailStateBeforeBeginTx|TestTailStateWriteContextMarksPendingUntilCancel|TestRepairSourceFTSYieldsBeforeDBWork|TestTailHeartbeat|TestTailWatchdog|TestResolver_NoOpWhenNoOrphans' -count=1`
  - `go test ./internal/ingest ./internal/store ./internal/presenter ./cmd/ai-viewer-ingest -count=1`
  - `go test -race ./internal/ingest ./cmd/ai-viewer-ingest -run 'TestResolverYieldsToTailStateBeforeBeginTx|TestWorkerFlushYieldsToTailStateBeforeBeginTx|TestWorkerIdleRefreshYieldsToTailStateBeforeBeginTx|TestTailStateWriteContextMarksPendingUntilCancel|TestRepairSourceFTSYieldsBeforeDBWork|TestRepairSourceReadModels|TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending|TestStartSourceRecordsScanAndTailLifecycle|TestSourceReadModelRepairRequestRepairsDurablePendingDebt|TestWorkerRuntime_SQLiteContentionReportsReplayRequiredAndReplays' -count=1`
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
  - `git diff --check`
- Post-monitor local gates:
  - `scripts/gates.sh` stopped at the benchmark regression gate before running
    the rest of the aggregate gate because the workstation was too busy for
    valid wall-time benchmark evidence: loadavg `24.27`, threshold `12.00`,
    exit `2`.
  - `scripts/lint.sh` passed: module tidy, `gofmt`, `goimports`, `go vet`,
    `golangci-lint`, standalone `gosec`, `govulncheck`, frontend ESLint,
    frontend typecheck, bundle-size self-test, and coverage-config self-tests.
  - `scripts/test.sh` passed: full Go `go test -race -count=1
    -coverprofile=coverage.out -covermode=atomic ./...`, coverage total
    `81.6%`, frontend Vitest `931` tests / `76` files, frontend line coverage
    `90.08%`.
  - `git diff --exit-code go.mod go.sum` passed after `go mod tidy`.

## Implementation Review Round 7 - 2026-06-30

Reviewer gate:

- Ran all six external implementation reviewers on the whole SOW and whole diff:
  `glm`, `minimax`, `kimi`, `mimo`, `deepseek`, and `qwen`. No `claude`
  reviewer was used.
- Successful responses were either `PRODUCTION GRADE`, P3-only cleanup, or
  positive votes with P2-labeled claims. Per protocol, every P2-labeled claim is
  treated as blocking until verified, even when the same response says
  `PRODUCTION GRADE`.

Accepted blocker class:

- Migration 0014 changed the source-scoped FTS repair contract so `fts_ops` and
  `fts_logs` are maintained by explicit FTS docids:
  `fts_ops.rowid = ops.rowid` and `fts_logs.rowid = log_entries.id`. The
  `ops.rowid` key is an internal no-`VACUUM` maintenance key, not a durable
  external identifier.
- The migration added the required source/session indexes but did not clear
  already-derived `fts_ops` / `fts_logs` rows from stores upgraded in place.
  Pre-0014 rows can have auto-assigned FTS docids, so later source repair
  deleting by the new stable rowid key can leave the old row behind and produce
  duplicate logical search results.

Verified dispositions:

- Accepted and fixed: in-place schema upgrade must clear the affected derived
  FTS tables when introducing the explicit-docid repair contract.
- Reclassified to process/packaging checklist: untracked migration/test files
  were a real commit-risk warning, not a runtime defect. SOW closure requires
  adding each SOW-0114 file explicitly by path; `git add -A` remains forbidden.
- Rejected as P2 runtime blocker: the 100-event batch size is deliberate
  liveness budget, documented in spec, covered by tests, and backed by live
  evidence that larger scan/repair work starved 30 s tail-state writes.
  Throughput tuning remains possible only with proof that tail heartbeat and
  stale-tail writes stay within budget.
- Rejected as code defect: the benchmark gate did not report a performance
  regression in the latest run; it refused to sample because the workstation
  load was above the gate's fail-closed validity threshold. That is a validation
  gap until a quiet-host aggregate run succeeds, not evidence of a code
  regression.
- Not accepted as a blocker to the liveness fix: installed evidence still showed
  some large sources scanning/repairing, but the live liveness test ran in that
  exact non-idle condition and recorded zero new heartbeat/stale-tail timeouts
  after resolver priority. Final SOW closure still needs a fresh installed-state
  check and either all-source recovery evidence or a concrete follow-up if a
  remaining source is independently slow.

Targeted class verification before fixing:

- Counted the affected FTS tables: only `fts_ops` and `fts_logs` changed to
  explicit primary-row docids in SOW-0114. `fts_content` is rebuilt by its own
  explicit backfill command and was not re-keyed by this liveness work.
- Checked all maintained paths for the class:
  - incremental `fts_ops` refresh inserts with explicit `rowid = ops.rowid`;
  - incremental `fts_logs` insert uses explicit `rowid = log_entries.id`;
  - source repair deletes and reinserts `fts_ops` by `ops.rowid`;
  - source repair deletes and reinserts `fts_logs` by `log_entries.id`;
  - one-shot `BackfillFTS` truncates both tables before rebuilding them.
- The only missing occurrence was the schema-upgrade boundary: applying 0014 to
  a store that already had derived FTS rows could preserve old auto-docid rows.

Fresh open-ended milestone review before rerun:

- Re-read the liveness-priority DB owners across the milestone:
  source repair, normal worker flush, idle rollup refresh, and orphan resolver
  all yield to pending tail-state operations before opening lower-priority
  transactions; tail heartbeat/stale-tail writes are the high-priority work and
  intentionally do not yield to themselves.
- Re-read supervisor ownership paths from round 6:
  request-channel registration still happens only after adapter construction
  and `Ingester.Submit()` succeed, and supervisor-owned cleanup still covers
  normal shutdown, scan failure, Tail failure, stale-tail restart, and explicit
  source stop.
- Re-read schema/version/comment surfaces for stale `0013` chain-head wording.
  Remaining stale comments in migration tests were fixed to `0014`.
- Re-read operator docs for schema repair. `docs/runbook.md` now records that
  migration 0014 clears derived search indexes and that source repair or
  `rollups-backfill` repopulates them from primary rows.

Round-7 fix:

- Specs updated before code:
  - `.agents/sow/specs/data-model.md` now records the explicit-docid FTS contract
    and 0014's derived-table clear.
  - `.agents/sow/specs/ingester.md` now records that 0014 clears old derived
    `fts_ops` / `fts_logs` rows before source repair repopulates them.
- Added regression coverage:
  - `internal/store/migration_0014_source_repair_liveness_indexes_internal_test.go::TestMigration0014_ClearsDerivedFTSRowsForStableDocIDRekey`
    applies the chain through 0013, seeds old-docid `fts_ops`/`fts_logs` rows,
    applies the real embedded 0014 SQL, and asserts both derived tables are
    empty and `schema_meta.version='14'`.
  - The test failed before the migration fix with
    `fts_ops rows after migration 0014 = 1, want 0`.
- Implemented migration repair:
  - `internal/store/migrations/0014_source_repair_liveness_indexes.sql`
    deletes from `fts_ops` and `fts_logs` after creating the liveness indexes
    and before bumping `schema_meta.version` to `14`.

Validation after round-7 fix:

- Focused test passed:
  - `go test ./internal/store -run 'TestMigration0014|TestMigration0006_ChainHeadSchemaVersion|TestMigration0007_ChainHeadSchemaVersion|TestMigration0010_ChainHeadSchemaVersion' -count=1`
- Backend focused tests passed:
  - `go test ./internal/store ./internal/ingest ./internal/presenter ./cmd/ai-viewer-ingest -count=1`
- Focused race tests passed:
  - `go test -race ./internal/store ./internal/ingest ./cmd/ai-viewer-ingest -run 'TestMigration0014|TestRepairSourceReadModels|TestResolverYieldsToTailStateBeforeBeginTx|TestWorkerFlushYieldsToTailStateBeforeBeginTx|TestWorkerIdleRefreshYieldsToTailStateBeforeBeginTx|TestTailStateWriteContextMarksPendingUntilCancel|TestSourceReadModelRepairRequestRepairsDurablePendingDebt' -count=1`
- Static and full test gates passed:
  - `scripts/lint.sh`
  - `scripts/test.sh`
- Cross-contract gates passed:
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
  - `scripts/test/check-contract-matrix-test.sh && scripts/check-contract-matrix.sh`
  - `scripts/test/check-ingestion-parity-test.sh && scripts/check-ingestion-parity.sh --fixtures`
- Build and hygiene gates passed:
  - `scripts/build.sh`
  - `scripts/scan-secrets.sh && scripts/scan-ai-attribution.sh`
  - `git diff --exit-code go.mod go.sum`
- Whitespace gate passed:
  - `git diff --check`
- Full aggregate gate status:
  - First post-fix `scripts/gates.sh` attempt failed closed before sampling
    benchmarks because the workstation load was invalid for wall-time benchmark
    evidence: loadavg `13.04`, threshold `12.00`, exit `2`.
  - After stopping only the installed `ai-viewer-ingest.service`, a second
    `scripts/gates.sh` attempt passed preflight and sampled benchmark attempt 1,
    but broad unrelated hot-path slowdowns indicated host noise
    (`aiagent_v2 Tail_SyntheticAppend +26.27%`, `claude Scan_LargeFixture
    +23.88%`, `opencode Tail_NewParts +33.73%`, `BatchInsert +25.47%`).
    The retry then failed closed before attempt 2 because loadavg rose to
    `23.94` over the same `12.00` threshold, exit `2`.
  - The installed ingester service was restarted after the benchmark attempt and
    both installed services were active.
  - A later aggregate retry again failed closed before benchmark sampling:
    loadavg `13.14`, threshold `12.00`, exit `2`. The restart trap ran after
    the gate exited, and both installed services were active afterward.
  - A controlled retry stopped only `ai-viewer-ingest.service`, waited
    120 seconds for loadavg decay, then ran `scripts/gates.sh` with a restart
    trap. The benchmark gate still failed closed before sampling: loadavg
    `19.02`, threshold `12.00`, exit `2`. The restart trap ran after the gate
    exited, and both installed services were active afterward.
- Full aggregate gate passed after stopping only `ai-viewer-ingest.service`,
  waiting 90 seconds for loadavg decay, and using a restart trap:
  - `scripts/gates.sh` passed every gate.
  - Benchmark preflight was valid: loadavg `8.71`, threshold `12.00`.
  - Benchmark gate passed: no `sec/op` regression above the 20% threshold.
  - Full gate summary: benchmark, lint/static, secrets scan, attribution scan,
    contract matrix, spec drift, ingestion parity, Codacy self-tests,
    installer/systemd checks, build, Go race/coverage, coverage threshold,
    adapter fuzz seeds, and frontend Playwright E2E/axe all passed.
  - Total gate runtime was `1006s`.
  - The restart trap ran after the gate exited, and both installed services were
    active afterward.
- Pending before rerun:
  - Rerun the same broad implementation-review prompt with a short fix note.

## Implementation Review Round 8 - 2026-06-30

Reviewer gate:

- Ran the next broad implementation-review gate on the whole SOW and whole diff
  after the round-7 explicit-docid migration fix. No `claude` reviewer was used.
- Usable outcomes before fixes:
  - `mimo`: `PRODUCTION GRADE`.
  - `qwen`: positive with P3 stale-comment cleanup.
  - `kimi`: `NEEDS WORK` with accepted P1/P2 lifecycle/read-model ownership
    findings.
  - `minimax`: later positive with the same P3 stale-comment cleanup, but on
    a stale pre-fix snapshot.
  - `deepseek`: technically incomplete; one partial read-model timeout claim
    was verified and rejected.
  - `glm`: timed out without a usable final vote.

Accepted blocker classes:

- Normal source startup still used global read-model deferral. The intended
  SOW-0114 contract is source-scoped deferral before each source scan, not a
  global mutable flag that can conflate unrelated sources.
- All-source `BackfillReadModels()` still cleared source-owned deferral state.
  The all-source reconciliation pass does not own source repair state and must
  not erase fresh per-source debt that Tail can create while reconciliation is
  streaming.
- `/api/health` degraded every `tail_restarting` state immediately, even though
  the SOW accepted a 10-minute single-restart grace. This made a transient first
  restart look as bad as persistent churn.
- Lifecycle writes for pre-submit source failures and source repair-state
  transitions were still best-effort in some paths. If those writes fail, the
  system must return/log that failure instead of silently losing lifecycle
  truth.
- Local open-ended review found additional same-milestone lifecycle misses:
  unexpected `Tail()` nil return while the supervisor context is alive could
  exit the supervisor and unregister restart channels while the source remained
  `tailing`; adapters that normalize canceled `Scan()` to nil could leave stale
  `scanning` state or fall through to Tail during shutdown; destructive global
  read-model backfill failure could leave FTS/rollups partial without
  re-marking sources `repair_pending`.

Rejected findings:

- Rejected as false positive: read-model repair has no synthetic timeout. The
  spec intentionally says source repair runs to completion while the supervisor
  context is alive, and
  `internal/ingest/read_model_repair_test.go::TestSourceReadModelRepairContextHasNoSyntheticDeadline`
  covers it.
- Rejected as unsafe: global backfill should blanket-mark all sources ready.
  Tail can commit source-owned work while global reconciliation is streaming;
  only source-scoped repair may clear the source `repair_pending` state.
- Rejected as non-blocking: `ExpectedLifecycleState` is not the only guard for
  lifecycle persistence. The accepted durable-write and abnormal-return issues
  were fixed directly in the supervisor and startup paths.

Targeted class verification before fixing:

- Re-read global versus source-owned read-model state across:
  `cmd/ai-viewer-ingest/main.go`, `cmd/ai-viewer-ingest/sources.go`,
  `internal/ingest/ingester.go`, `internal/ingest/worker.go`,
  `internal/ingest/writer.go`, and `internal/ingest/read_model_repair.go`.
  The class count was two real ownership defects: normal startup set global
  deferral, and all-source backfill cleared source-owned deferral.
- Re-read lifecycle persistence paths across source construction failure,
  submit failure, scan return, Tail return, repair success/failure, explicit
  stop, and stale-tail restart. The class count was four real lifecycle
  defects: pre-submit failure writes could be dropped, repair-state writes
  could be dropped, canceled Scan nil could leave stale state, and unexpected
  Tail nil could look like clean exit.
- Re-read health state classification for every lifecycle state in
  `internal/presenter/health.go`. The only real mismatch was immediate
  degradation for the first `tail_restarting` occurrence inside the accepted
  grace window.

Fresh open-ended milestone review before rerun:

- Re-read SOW goal, specs, migrations, supervisor lifecycle, read-model repair,
  global reconciliation, presenter health, paritycheck callers, and stale
  comments from scratch without limiting the pass to the accepted reviewer
  classes.
- Additional issue found and fixed from that pass: global destructive
  FTS/rollup backfill failures must mark every known source `repair_pending`
  before returning, otherwise a source can look ready while derived indexes are
  partial.
- Additional gate issue found after the aggregate run: `internal/paritycheck`
  coverage was just below the raw 80% threshold even though the rounded report
  displayed `80.0%`. Added meaningful snapshot-freeze coverage instead of
  changing the gate.

Round-8 fixes:

- Specs updated before code:
  - `.agents/sow/specs/ingester.md` now records that all-source reconciliation
    does not own source-scoped deferral/repair state; global reconciliation
    failure marks known sources `repair_pending`; unexpected Tail nil while the
    supervisor is alive records `tail_failed` and restarts; canceled Scan nil
    records `stopped` with shutdown-safe context and does not Tail.
  - `.agents/sow/specs/observability.md` now records the
    `tail_restarting` grace rule: degraded only after more than one restart or
    a single restart older than the long-scan grace threshold.
- `cmd/ai-viewer-ingest/main.go` no longer enables normal global
  `deferReadModels`; startup backfill only uses the global flag when there are
  no source-scoped workers to repair.
- `cmd/ai-viewer-ingest/sources.go` sets source-scoped read-model deferral
  before source `Submit()` and clears it on submit failure; pre-submit lifecycle
  failures use required retrying writes and return joined errors when lifecycle
  persistence also fails.
- `cmd/ai-viewer-ingest/source_supervisor.go` records repair and lifecycle
  transitions with required writes; canceled Scan nil records `stopped` and
  returns; unexpected Tail nil records `tail_failed` and restarts.
- `internal/ingest/ingester.go` stopped mutating existing source deferral flags
  from `SetDeferReadModels(false)` and makes `BackfillReadModels()` failure
  mark all known sources `repair_pending` before returning.
- `internal/presenter/health.go` applies the accepted restart grace for
  `tail_restarting`.
- Removed legacy global-deferral calls from paritycheck helpers.
- Fixed stale P3 comments in store migration tests.
- Added paritycheck snapshot-freeze tests for missing source roots and
  cancellation so the coverage gate failure is fixed by exercising a real
  fail-closed path.

Validation after round-8 fix:

- Focused tests passed:
  - `go test ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter -count=1`
  - `go test ./internal/paritycheck -coverprofile=/tmp/paritycheck.coverage.out -covermode=atomic -count=1`
- Focused race tests passed:
  - `go test -race ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter ./internal/store -run 'TestStartSourceRestartsAfterUnexpectedTailNilReturn|TestStartSourceRecordsStoppedWhenScanReturnsNilAfterCancellation|TestStartSourceRestartsAfterTailFailure|TestBackfillReadModelsDoesNotClearSourceDeferralFlags|TestBackfillReadModelsFailureMarksAllSourcesRepairPending|TestHealthBuildSource_TailRestartingGrace|TestMigration0014_ClearsDerivedFTSRowsForStableDocIDRekey' -count=1`
- Cross-package focused tests passed:
  - `go test ./internal/paritycheck ./cmd/ai-viewer-ingest ./internal/ingest ./internal/presenter ./internal/store -count=1`
- Static and full test gates passed:
  - `scripts/lint.sh`
  - `scripts/test.sh`
  - `scripts/check-coverage.sh coverage.out`
- Coverage evidence:
  - `internal/paritycheck` improved from raw-below-threshold rounded `80.0%`
    to `80.3%`.
  - gated `internal/*` aggregate remained `85.3%`.
- Cross-contract gates passed:
  - `scripts/spec-drift.sh && scripts/test/spec-drift-test.sh`
  - `scripts/test/check-contract-matrix-test.sh && scripts/check-contract-matrix.sh`
  - `scripts/test/check-ingestion-parity-test.sh && scripts/check-ingestion-parity.sh --fixtures`
- Build and hygiene gates passed:
  - `scripts/build.sh`
  - `scripts/scan-secrets.sh && scripts/scan-ai-attribution.sh`
  - `git diff --check`
- Full aggregate gate status:
  - First round-8 aggregate run reached `scripts/check-coverage.sh` and failed
    only because `internal/paritycheck` was raw-below-threshold while displayed
    as `80.0%`.
  - After the paritycheck coverage test fix, `scripts/test.sh` and
    `scripts/check-coverage.sh coverage.out` passed.
  - A full aggregate retry passed benchmark preflight with
    `loadavg 8.73 < 12.00`, but benchmark attempt 1 saw a noisy single
    `CodexTail_SyntheticAppend +29.69% sec/op` result and correctly requested
    retry. The retry was blocked by host load `12.58 >= 12.00`, so the gate
    failed closed with exit `2`.
  - After the operator requested lowest-priority execution, the installed
    ingester was given a runtime systemd drop-in with `Nice=19`,
    `CPUSchedulingPolicy=idle`, `IOSchedulingClass=idle`, `CPUWeight=1`, and
    `IOWeight=1`; live process checks showed the ingester running in `IDL`
    scheduling class.
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/gates.sh` correctly ran the
    aggregate at idle CPU/IO priority. Benchmark preflight passed
    `8.30 < 12.00`, attempt 1 reported broad unrelated sec/op slowdowns, and
    retry preflight passed `11.28 < 12.00`. The retry reproduced broad
    slowdowns (`Scan_SyntheticCorpus`, `CodexScan_SyntheticCorpus`,
    `OpencodeScan_SyntheticDB`, `OpencodeTail_SyntheticAppend`,
    `SessionsListQuery`) and failed the benchmark gate.
  - This is a benchmark-validity blocker, not a code pass: the committed
    `bench/baseline.txt` command is the plain `go test -p=1 ...` workstation
    baseline, while the requested validation ran under `SCHED_IDLE`. The gate
    must not be weakened and the baseline must not be refreshed without its own
    explicit SOW.
  - To preserve current evidence for the rest of the gate surface, the
    post-benchmark sections were run manually under the same idle wrapper and
    passed:
    `scripts/lint.sh`, `scripts/test/scan-secrets-test.sh`,
    `scripts/scan-secrets.sh`, `scripts/scan-ai-attribution.sh`,
    `scripts/test/check-contract-matrix-test.sh`,
    `scripts/test/spec-drift-test.sh`, `scripts/spec-drift.sh`,
    `scripts/test/check-ingestion-parity-test.sh`,
    `scripts/check-ingestion-parity.sh --fixtures`,
    `scripts/test/codacy-coverage-upload-test.sh`,
    `scripts/test/codacy-config-test.sh`,
    `scripts/test/install-system-test.sh`,
    `scripts/test/systemd-units-test.sh`, `scripts/build.sh`,
    `scripts/test.sh`, `scripts/check-coverage.sh coverage.out`, adapter fuzz
    seed corpus + target-set lock, and frontend Playwright E2E + axe.
  - The installed ingester was restarted after validation and verified active
    with the idle systemd properties still effective.

Pending before rerun:

- Obtain benchmark evidence in a comparator-compatible execution mode that also
  respects the workstation-impact constraint. Do not refresh the baseline or
  weaken the gate for this SOW.
- Rerun the same broad implementation-review prompt with a short fix note.
- Install and perform fresh reingest recovery after reviewer convergence.

## Implementation Review Round 9 - 2026-06-30

Reviewer gate:

- Ran the next broad implementation-review gate on the whole SOW and whole diff
  after the round-8 fixes. No `claude` reviewer was used. All reviewer commands
  ran under `chrt --idle 0 nice -n 19 ionice -c3` after the operator requested
  lowest-priority execution.
- Usable outcomes before fixes:
  - `mimo`: `PRODUCTION GRADE`, but with two hallucinated validation claims
    about normal-priority gates and installed recovery; those claims were not
    accepted as evidence.
  - `qwen`: `PRODUCTION GRADE`; correctly treated the SCHED_IDLE benchmark
    mismatch as a validation blocker, not a code pass.
  - `kimi`: voted `PRODUCTION GRADE` but listed two P2 notes: stale repair
    timeout wording and FTS DELETE/INSERT redundancy.
  - `glm`: `NEEDS WORK` with two accepted P2 findings in the all-source
    read-model repair-pending mark.
  - `minimax`: timed out with no usable output.
  - `deepseek`: technical failure / no usable final vote captured.

Accepted blocker class:

- `internal/ingest/ingester.go::markAllSourceReadModelsRepairPending` used a
  bespoke `source_progress` upsert instead of the source lifecycle writer
  contract. The conflict path rewrote existing `source_progress.updated_at`,
  which is progress/cursor freshness exposed as `progress_updated_at`, and it
  did not emit `source_status_changed` rows atomically with the
  `read_model_state='repair_pending'` transition.

Accepted cleanup / false-positive disposition:

- Kimi's timeout note identified stale SOW wording, not a code defect. Current
  code and specs intentionally have no short synthetic source repair timeout;
  source repair runs under the source supervisor context.
- Kimi's FTS note was accepted as cleanup. Since explicit-docid maintenance now
  truncates, deletes by rowid, or inserts only newly-created log rows before FTS
  writes, the shared FTS insert SQL was tightened from `INSERT OR REPLACE` to
  plain `INSERT` so duplicate rowid assumptions fail loudly.

Targeted class verification before fixing:

- Re-read every `source_progress`, `read_model_state`, lifecycle, and
  `source_status_changed` writer across `internal/ingest`, `cmd/ai-viewer-ingest`,
  presenter specs, and store migrations.
- Real same-class code defect count was one: the all-source repair-pending mark.
  Normal worker progress writes legitimately update `updated_at`; lifecycle
  writers preserve it; Tail stale/heartbeat state changes already wrap state
  mutation and notify in the same transaction when they change lifecycle state.

Fresh open-ended milestone review before rerun:

- Re-read the source supervisor repair flow, all remaining read-model writers,
  Tail liveness writers, notify producer rules, and affected specs without
  limiting the pass to GLM/Kimi's findings.
- Additional issue found and fixed: specs were too broad about
  `source_status_changed` on lifecycle/read-model timestamp evidence. Existing
  tests intentionally pin that heartbeat-only `tail_heartbeat_at` persistence
  while a source remains `tailing` does not emit source-status SSE. The specs now
  distinguish transition/error evidence from high-frequency heartbeat evidence
  that REST health reads on demand.

Round-9 fixes:

- `.agents/sow/specs/ingester.md` now states that all-source backfill failure
  marks sources through the source lifecycle write contract, emits
  `source_status_changed` atomically, clears stale read-model errors, and
  preserves existing `source_progress.updated_at`.
- `internal/ingest/read_model_repair_test.go::TestBackfillReadModelsFailureMarksAllSourcesRepairPending`
  now pins preservation of existing `updated_at`, `read_model_state_at`,
  `read_model_error=NULL`, one `source_status_changed` row per source, and
  repair request delivery.
- `internal/ingest/ingester.go::markAllSourceReadModelsRepairPending` now
  selects source id/format/location, opens one transaction, ensures the progress
  row, updates read-model state through the lifecycle column helper, and inserts
  `source_status_changed` rows in that same transaction.
- `internal/ingest/fts_refresh.go`, `internal/ingest/fts_backfill.go`, and
  `internal/ingest/writer.go` now use plain `INSERT` for explicit-docid FTS writes
  where callers already truncate/delete or know the log row is new.
- `.agents/sow/specs/ingester.md`, `.agents/sow/specs/data-model.md`, and
  `.agents/sow/specs/sse-protocol.md` now clarify that heartbeat-only
  `tail_heartbeat_at` persistence does not emit `source_status_changed`.
- This SOW's requirements and historical round-1 note were clarified so they do
  not imply the rejected short synthetic repair timeout model.

Validation after round-9 fix:

- Proved the new regression test failed before the code fix:
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest -run '^TestBackfillReadModelsFailureMarksAllSourcesRepairPending$' -count=1`
  - Failure: `codex:/a updated_at = 9000, want preserved 1000`.
- Focused tests passed after code fix:
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest -run '^TestBackfillReadModelsFailureMarksAllSourcesRepairPending$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest -run '^(TestBackfillReadModelsFailureMarksAllSourcesRepairPending|TestRepairSourceReadModels|TestWorkerSkipsDerivedRefreshDuringGlobalRebuildAndMarksRepairPending|TestFTS|TestBackfillFTS|TestFTSBackfill|TestFTS_.*|TestFTSParity)' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest ./cmd/ai-viewer-ingest -count=1`
- Spec drift gates passed after spec clarifications:
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/spec-drift.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/test/spec-drift-test.sh`

Pending before rerun:

- Run whitespace/lint checks after the round-9 edits.
- Rerun the same broad implementation-review prompt with short round-9 fix
  notes. Do not narrow the scope.
- Obtain comparator-compatible benchmark evidence without weakening the gate or
  refreshing `bench/baseline.txt`.
- Install and perform fresh reingest recovery after reviewer convergence.

## Implementation Review Round 10 - 2026-06-30

Reviewer gate:

- Reran the same broad implementation-review gate on the whole SOW and whole
  diff after the round-9 fixes. No `claude` reviewer was used. Reviewer commands
  ran under `chrt --idle 0 nice -n 19 ionice -c3` to preserve workstation
  responsiveness.
- Usable outcomes before fixes:
  - `kimi`: `PRODUCTION GRADE`; P3 notes only.
  - `mimo`: `PRODUCTION GRADE`; P3 notes only.
  - `deepseek`: `PRODUCTION GRADE`.
  - `qwen`: `PRODUCTION GRADE`.
  - `minimax`: `PRODUCTION GRADE`.
  - `glm`: `NEEDS WORK` with six accepted P2 findings.

Accepted P2 classes and verification:

- Already-`repair_pending` sources were refreshed by the all-source repair mark.
  Verified real: a new regression test failed before code with
  `read_model_state_at = 9000, want preserved 2000`.
- Opencode incompatible scan-schema errors were consumed as recoverable supervisor
  errors because no adapter produced the canonical fatal-scan marker. Verified
  real: the opencode incompatible-schema test failed before code because the
  error was not `FatalScanError`.
- A Tail goroutine that ignored cancellation could send after the owning source
  goroutine closed the event channel and panic the process. Verified real: the
  new source-supervisor late-send test failed before code with `panic: send on
  closed channel`.
- `.agents/sow/specs/data-model.md` still said `Schema (v12 target)`, while
  migrations and presenter schema are v14. Verified real by cross-checking the
  migration history and `internal/presenter/presenter.go::SchemaVersion`.
- First-heartbeat presenter grace was implemented but lacked direct tests for
  `tailing` + NULL `tail_heartbeat_at`. Verified as a test gap and pinned in
  health and source-list helper tests.
- Watchdog-to-supervisor restart had unit coverage in the pieces but lacked an
  end-to-end source-supervisor test. Verified as a test gap and pinned with a
  real supervisor restart request test.

Round-10 fixes:

- `.agents/sow/specs/data-model.md` now states `Schema (v14 target)`.
- `.agents/sow/specs/adapter-opencode.md` now requires incompatible
  `scanLoop` schema introspection errors to use the canonical fatal-scan marker.
- `.agents/sow/specs/ingester.md` now states:
  - source event channels remain open across restart attempts and close only
    after no accepted source attempt may send;
  - a late adapter send after cancellation-timeout closure is converted to
    adapter-failure evidence instead of process panic;
  - all-source repair-pending marking does not rewrite already-pending sources,
    does not emit duplicate `source_status_changed`, and preserves the original
    `read_model_state_at`.
- The SOW pre-implementation text now matches the implemented event-channel
  ownership split: supervisor controls restart lifetime, the owning source
  goroutine closes the event channel after supervisor exit, and the Tail wrapper
  shields late adapter sends.
- `internal/ingest/source_lifecycle.go` added a transition-only guard for
  read-model updates.
- `internal/ingest/ingester.go::markAllSourceReadModelsRepairPending` uses that
  guard so already-`repair_pending` rows keep their original timestamp/error and
  do not emit a duplicate notify row.
- `internal/adapters/opencode/tailer.go` wraps incompatible scan introspection
  with `canonical.NewFatalScanError`.
- `cmd/ai-viewer-ingest/source_supervisor.go` treats fatal restart catch-up Scan
  as terminal/degraded `scan_failed` and wraps adapter `Tail` calls so panics
  become Tail errors.
- Added focused regression coverage for:
  - already-pending all-source repair marks;
  - opencode fatal scan marker production;
  - restart catch-up fatal Scan stopping without Tail;
  - late Tail send after cancellation timeout not crashing the process;
  - watchdog stale-tail restart reaching a fresh Tail;
  - presenter first-heartbeat grace in both `/api/health` and `/api/sources`.

Class verification before rerun:

- Re-read all current producers and consumers of `FatalScanError`. Only opencode
  has a source-schema incompatibility that should be fatal in this SOW; startup
  construction/location failures are handled before Scan, and other adapter scan
  read errors remain recoverable by contract.
- Re-read all `read_model_state='repair_pending'` writers. The all-source path
  now guards same-state rewrites; worker batch repair-pending writes already
  guarded on the conflict path and legitimately update cursor freshness when
  they commit new source data.
- Re-read event-channel ownership and Tail cancellation paths. Recoverable Tail
  restarts keep the channel open; terminal source exit closes it once; late
  sends from a non-cooperative adapter are now contained by the Tail wrapper.
- Re-read presenter effective lifecycle helpers and source-list/health tests.
  NULL `tail_heartbeat_at` now has direct first-heartbeat grace coverage.
- Re-ran broad `rg` sweeps over schema version, fatal scan, heartbeat,
  repair-pending, source-status notify, and supervisor restart terms before
  reviewer rerun.

Fresh open-ended milestone review before rerun:

- Re-read the SOW goal, updated specs, supervisor lifecycle, source submission
  wrapper, opencode scan handoff, all-source repair failure path, read-model
  transition helper, health/source presenter helpers, and the new tests without
  limiting the pass to GLM's six findings.
- Additional issue found and fixed during that open-ended pass: stale SOW wording
  still implied the supervisor itself closed the event channel. The final SOW
  text now matches the actual code contract.
- No additional P0/P1/P2 implementation issue was found in this local pass.

Validation after round-10 fixes:

- Proved representative new tests failed before code fixes:
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest -run '^TestBackfillReadModelsFailureDoesNotRefreshAlreadyPendingSources$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/adapters/opencode -run '^TestAdapter_ScanIncompatibleSchemaHardError$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./cmd/ai-viewer-ingest -run '^(TestStartSourceRestartCatchupFatalScanStopsWithoutTail|TestStartSourceRestartsAfterWatchdogStaleRequest|TestStartSourceLateTailSendAfterCancelTimeoutDoesNotPanic)$' -count=1`
- Focused tests passed after code fixes:
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest -run '^TestBackfillReadModelsFailureDoesNotRefreshAlreadyPendingSources$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/adapters/opencode -run '^TestAdapter_ScanIncompatibleSchemaHardError$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./cmd/ai-viewer-ingest -run '^(TestStartSourceRestartCatchupFatalScanStopsWithoutTail|TestStartSourceRestartsAfterWatchdogStaleRequest|TestStartSourceLateTailSendAfterCancelTimeoutDoesNotPanic)$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/presenter -run '^(TestHealthBuildSource_TailingFirstHeartbeatGrace|TestSourcesBuildItem_TailingFirstHeartbeatGrace)$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest ./cmd/ai-viewer-ingest ./internal/adapters/opencode ./internal/presenter -count=1`
- Spec and lint gates passed after fixes:
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/spec-drift.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/test/spec-drift-test.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/lint.sh`
- Full test/build gates passed after fixes:
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/test.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/check-coverage.sh coverage.out`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/build.sh`

Pending before rerun:

- Rerun the same broad implementation-review prompt with short round-10 fix
  notes. Do not narrow the scope.
- Obtain comparator-compatible benchmark evidence without weakening the gate or
  refreshing `bench/baseline.txt`. Under the current workstation-protection
  instruction, the benchmark gate must not be treated as comparator evidence
  when run under idle scheduling.
- Install and perform fresh reingest recovery after reviewer convergence.

## Implementation Review Round 11 - 2026-06-30

Reviewer gate:

- Reran the same broad implementation-review gate on the whole SOW and whole
  diff after the round-10 fixes. No `claude` reviewer was used.
- Usable outcomes before fixes:
  - `kimi`: `PRODUCTION GRADE`; P3 notes only.
  - `deepseek`: `NEEDS WORK`.
  - `qwen`: `NEEDS WORK`.
  - `glm`: `NEEDS WORK`.
  - `minimax`: internally inconsistent `PRODUCTION GRADE` response that still
    carried one P2-labeled finding; treated as blocking until verified.
  - `mimo`: technical failure / no usable final vote captured. It was not
    retried while accepted P2 findings existed.

Accepted P2 classes and verification:

- Global retained read-model backfill still had one-shot destructive/read/write
  paths that did not yield to pending Tail lifecycle/heartbeat writes:
  `BackfillFTS` and `BackfillRollups` ran with the normal offline CLI path from
  `Ingester.BackfillReadModels`. Verified real by reading both functions and
  their daemon caller. The same-class count was two global rebuild functions:
  FTS truncate/read/write batches and rollup truncate/read/day-flush phases.
- Migration 0014 clears derived `fts_ops` / `fts_logs`, but recovery proof only
  covered the migration clear, not source-scoped search restoration after the
  clear. Verified as a missing test, not missing production code: source repair
  already reads primary rows and inserts explicit FTS docids, but the recovery
  contract needed executable evidence.
- The SOW/spec wording still framed `ops.rowid` as durable across all SQLite
  maintenance. Verified against SQLite's documented rowid-table behavior: an
  implicit rowid not aliased by `INTEGER PRIMARY KEY` can change during
  `VACUUM`. Runtime code does not run `VACUUM`, so this was a spec/operations
  foot-gun rather than an immediate runtime bug.
- `clearReadModelDeferral` could rewrite an already-`repair_pending` source and
  clear its stored read-model error evidence because it used a normal
  `repair_pending` write. Verified real by reading the supervisor path. The
  same-class count was one additional repair-pending writer beyond the
  all-source path fixed in round 10.

Round-11 fixes:

- `.agents/sow/specs/ingester.md` now states that daemon retained
  reconciliation yields before destructive/read/write DB work when live source
  supervisors have pending Tail lifecycle/heartbeat writes; the offline
  `rollups-backfill` CLI keeps the no-yield path.
- `.agents/sow/specs/ingester.md` now states that startup-deferral clearing is a
  transition-only `repair_pending` write: already-pending sources queue repair
  in memory but keep their original `read_model_state_at`, `read_model_error`,
  and notify state.
- `.agents/sow/specs/data-model.md`, `.agents/sow/specs/ingester.md`,
  `docs/runbook.md`, and migration 0014 comments now describe `fts_ops.rowid =
  ops.rowid` as an explicit no-`VACUUM` maintenance key, not a durable stable
  identifier. External `VACUUM` / rowid-rewriting maintenance requires a full
  FTS rebuild before search is trusted; source-scoped repair alone is not the
  recovery path after such maintenance.
- `internal/ingest/fts_backfill.go` keeps public `BackfillFTS` as the offline
  no-yield wrapper and adds `backfillFTSWithYield` for daemon reconciliation.
  The yield-aware path yields before truncating FTS and before each ops/logs
  read and write batch.
- `internal/ingest/rollup_backfill.go` keeps public `BackfillRollups` compatible
  and adds a package-local yield option. The daemon path yields before rollup
  truncation, before read phases, and before each day flush.
- `internal/ingest/ingester.go::BackfillReadModels` now uses the yield-aware FTS
  and rollup paths with `waitForTailStatePriority`. The offline CLI in
  `cmd/ai-viewer-ingest/backfill.go` still calls the public no-yield functions.
- `cmd/ai-viewer-ingest/source_supervisor.go::clearReadModelDeferral` now uses
  the read-model transition-only guard when writing `repair_pending`.
- Added regression coverage for:
  - FTS global backfill yielding before its destructive truncate;
  - rollup global backfill yielding before its destructive truncate;
  - source repair restoring searchable `fts_ops` / `fts_logs` rows after the
    derived tables are cleared;
  - startup-deferral clearing preserving already-pending read-model timestamp
    and error evidence.

Targeted class verification before fixing:

- Re-read every FTS and rollup backfill caller. Only daemon
  `Ingester.BackfillReadModels` has live source supervisors and therefore needs
  `waitForTailStatePriority`; the CLI backfill command is offline and correctly
  has no live-priority yield.
- Re-read FTS maintenance paths. Incremental refresh, source repair, and
  one-shot backfill now all insert explicit docids; migration 0014 clears old
  auto-docid rows; docs/specs now forbid treating `ops.rowid` as durable across
  `VACUUM`.
- Re-read all `repair_pending` writers. The all-source mark and source
  deferral-clear path now both avoid rewriting already-pending evidence; worker
  flush repair-pending writes still legitimately occur with new committed source
  data and cursor freshness.

Fresh open-ended milestone review before rerun:

- Re-read the SOW goal, read-model rebuild/repair specs, migration 0013/0014,
  FTS backfill/repair, rollup backfill, global backfill failure path, source
  supervisor deferral clearing, Tail-priority wait path, offline CLI backfill,
  runbook, and the new tests without limiting the pass to the round-11 findings.
- Additional issue found and fixed during that pass: SOW history still used a
  misleading durable-docid phrase. The work ledger now consistently says
  explicit/no-`VACUUM` maintenance docid.
- No additional P0/P1/P2 implementation issue was found in this local pass.

Validation after round-11 fixes:

- Proved the new focused tests failed before implementation where applicable:
  - FTS/rollup yield tests failed to compile before the yield hooks existed.
  - The source-repair FTS restoration test initially exposed an FTS5 test-query
    syntax issue (`sonnet-repair` parsed as an operator expression), then passed
    after the test used an unambiguous token.
- Focused tests passed:
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest ./cmd/ai-viewer-ingest -run '^(TestFTS_BackfillYieldsBeforeTruncate|TestBackfillRollupsYieldsBeforeTruncate|TestRepairSourceReadModelsRestoresFTSAfterDerivedTableClear|TestClearReadModelDeferralPreservesAlreadyPendingEvidence)$' -count=1`
  - `chrt --idle 0 nice -n 19 ionice -c3 go test ./internal/ingest ./cmd/ai-viewer-ingest ./internal/store -count=1`
- Spec and static gates passed:
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/spec-drift.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/test/spec-drift-test.sh`
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/lint.sh`
- Full test/build gates passed:
  - First `scripts/test.sh` run passed Go race/coverage and then hit one
    frontend Topology timeout. The isolated test immediately passed, and a
    second full `scripts/test.sh` run passed end-to-end.
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/check-coverage.sh coverage.out`
    passed with gated aggregate `85.3%`.
  - `chrt --idle 0 nice -n 19 ionice -c3 scripts/build.sh` passed.
- Systemd priority evidence:
  - `ai-viewer-ingest.service` is active with `Nice=19`,
    `CPUSchedulingPolicy=idle`, `IOSchedulingClass=idle`, `CPUWeight=1`, and
    `IOWeight=1`.

Pending before rerun:

- Rerun the same broad implementation-review prompt with short round-11 fix
  notes. Do not narrow the scope.
- Obtain comparator-compatible benchmark evidence later on a quiet host without
  idle scheduling; idle-priority benchmark runs are not valid comparator
  evidence.
- Install and perform fresh reingest recovery after reviewer convergence.

## Implementation Review Round 12 - 2026-06-30

Reviewer gate:

- Reran the same broad implementation-review gate on the whole SOW and whole
  diff after the round-11 fixes. No `claude` reviewer was used.
- The low-priority reviewer wrapper wrote complete review outputs for all six
  reviewers, then exited non-zero because the zsh wrapper used the read-only
  variable name `status`. The wrapper bug happened after review output capture;
  the review files are usable evidence for this gate.
- Outcomes:
  - `glm`: `PRODUCTION GRADE`; P3 notes only.
  - `kimi`: `PRODUCTION GRADE`; P3 notes only.
  - `mimo`: `PRODUCTION GRADE`; P3 notes only.
  - `minimax`: `PRODUCTION GRADE`; P3/open benchmark-comparator note only.
  - `qwen`: `PRODUCTION GRADE`; P3 notes only.
  - `deepseek`: internally inconsistent `PRODUCTION GRADE` vote with one
    P2-labeled source-repair-timeout claim and two P3 notes; treated as
    blocking until verified.

Deepseek P2 disposition:

- Finding: `cmd/ai-viewer-ingest/source_supervisor.go::sourceReadModelRepairContext`
  creates a cancellable context without a deadline, while an old SOW line still
  said source repair attempts used the same five-minute startup-backfill timeout.
- Disposition: rejected as a code defect; accepted as SOW ledger cleanup.
- Evidence:
  - Current requirements at the top of this SOW state that source-scoped repair
    must not use a short synthetic execution timeout that restarts large repairs
    from the beginning.
  - `.agents/sow/specs/ingester.md` states that source-scoped repair may run to
    completion while the source supervisor context is alive and must not be
    killed by a short wall-clock timeout.
  - `cmd/ai-viewer-ingest/source_supervisor.go::sourceReadModelRepairContext`
    intentionally uses parent cancellation without adding `WithTimeout`.
  - `cmd/ai-viewer-ingest/lifecycle_test.go::TestSourceReadModelRepairContextHasNoSyntheticDeadline`
    pins the no-synthetic-deadline contract and parent-cancellation behavior.
- Same-class verification: searched the SOW, spec, source supervisor, and ingest
  package for source-repair timeout/deadline wording. Remaining runtime timeout
  code is for unrelated shutdown, global startup backfill, tail-state writes,
  opencode probe, check-parity, and resolver shutdown deadlines.
- Fix: removed contradictory SOW ledger wording that still implied source repair
  used the global startup backfill timeout or an "existing repair timeout". The
  SOW now consistently says source repair has no short synthetic execution
  timeout; `repair_timeout` remains a legal state for caller/deadline-driven
  failures, not a daemon-created short deadline.

Round-12 P3 dispositions:

- `meta_json` lifecycle preservation: rejected as a behavior defect. The
  contract is runtime reassertion from `WithSourceMeta`, not preserving a stale
  historical metadata blob when runtime metadata is absent. Existing tests cover
  `meta_json` persistence and restart reassertion; a lifecycle-specific
  preservation test would encode the wrong contract.
- Worker/global FTS rebuild race: rejected as theoretical, not actionable.
  `BackfillReadModels` sets `readModelRebuildActive` before truncation, and the
  worker checks the flag inside its writer transaction in
  `refreshBatchReadModels`; SQLite single-writer serialization prevents the
  claimed duplicate-docid interleaving from becoming a committed race.
- P3 maintainability observations such as duplicate presenter helpers,
  `waitForTailStatePriority` polling, and redundant channel return shapes do not
  block this SOW. They are cosmetic cleanup, not liveness or data-integrity
  defects.
- Benchmark comparator remains open by operator constraint: all local
  compilations/tests ran at idle priority to protect desktop work, and an idle
  benchmark run is not comparable to the normal-priority workstation baseline.

Fresh open-ended milestone review after the rejected P2:

- Re-read the source repair contract, source supervisor repair loop, current
  SOW requirements, live ingester spec, `meta_json` contract/tests, global FTS
  rebuild/worker serialization, and Round-12 reviewer outputs without limiting
  the pass to Deepseek's cited timeout claim.
- Additional issue found and fixed during that pass: old SOW ledger text still
  contained several timeout phrases that conflicted with the accepted no-short-
  synthetic-deadline repair contract.
- No additional P0/P1/P2 implementation issue was found in this local pass.

Round-12 gate conclusion:

- External implementation-review gate is converged for SOW-0114: all six
  reviewer outputs are positive or have only rejected/documented P3/non-blocking
  findings.
- Remaining validation before close: rerun cheap spec/diff checks after the SOW
  ledger cleanup, then install/reingest under the low-priority systemd
  configuration. Comparator-compatible benchmark evidence must wait for a quiet
  host and normal scheduling; it cannot be produced during the operator-requested
  desktop-protection window.

Post-review install and fresh reingest:

- Stopped only `ai-viewer-serve.service` and `ai-viewer-ingest.service`.
- Deleted only the installed derived SQLite files:
  `/opt/ai-viewer/data/index.db`, `/opt/ai-viewer/data/index.db-wal`, and
  `/opt/ai-viewer/data/index.db-shm`.
- Reinstalled with the idle-priority wrapper:
  `chrt --idle 0 nice -n 19 ionice -c3 scripts/install-system.sh`.
- Installer evidence:
  - frontend/Go build passed;
  - bundle-size gate passed (`318.6 KB gz / 500.0 KB` main chunk);
  - units rendered and verified;
  - five explicit sources were installed:
    `aiagent_v3`, `aiagent_v2`, `claude-code`, `codex`, and `opencode`;
  - UI responded at `http://127.0.0.1:7710/`.
- Fresh DB evidence:
  - new DB started at schema version 14 and applied migrations 0001 through 0014;
  - all five sources started with `resume=false`;
  - after about one minute the fresh DB was `195342336` bytes and all five
    sources were still `lifecycle_state='scanning'`, which is expected while the
    low-priority fresh ingest catches up.
- Runtime priority evidence after reinstall:
  - `ai-viewer-ingest.service` active/running with `Nice=19`,
    `CPUSchedulingPolicy=idle`, `IOSchedulingClass=idle`, `CPUWeight=1`, and
    `IOWeight=1`.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
