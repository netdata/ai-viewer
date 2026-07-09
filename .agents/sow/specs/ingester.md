# Ingester (`ai-viewer-ingest`)

## TL;DR

A long-running daemon. Loads source configs, runs one adapter per source, fans events into a single ingest pipeline that dedups, resolves links, and writes batched transactions into SQLite. Appends rows to the SQLite `notify` table (same transaction as each batch) for the server to poll.

## Lifecycle

```
main()
  ├─ load config (sources, paths)
  ├─ open SQLite (OpenWriter), run migrations
  ├─ construct Ingester(db, pricer, logger)
  ├─ Ingester.Start(ctx)
  │     ├─ load per-source observability seq counter from source_progress.last_seq
  │     ├─ start stale-tail watchdog goroutine
  │     └─ start background resolver goroutine (5 s ticker)
  ├─ resolve sources and register per-source runtime metadata/options
  ├─ reconcile persisted source lifecycle/read-model state for the resolved set
  ├─ for each source:
  │    ├─ record source_progress.lifecycle_state='starting'
  │    ├─ instantiate adapter via registry (ParseCursor from source_progress.cursor)
  │    ├─ Ingester.Submit(sourceID, events) — spawns one worker
  │    └─ source supervisor goroutine:
  │         ├─ startup Scan(...) → events chan
  │         ├─ clear startup read-model deferral
  │         ├─ schedule per-source read-model repair
  │         ├─ Tail(...) → events chan immediately for this source
  │         └─ restart Tail on failure/stale heartbeat with bounded backoff
  └─ wait for SIGTERM/SIGINT → bounded graceful shutdown
```

The ingester owns the write-side `*sql.DB`; downstream packages do not
touch it. The store remains the only SQLite handle; the ingester is the
only writer (per `data-model.md` §single-writer invariant).

Each source has an independent supervisor. A source that finishes startup
`Scan` starts `Tail` without waiting for unrelated sources to finish scanning,
repairing derived tables, failing, or timing out. The only all-sources startup
signal that may remain is a background reconciliation trigger; it must never be
passed to Tail or block Tail startup.

The source supervisor owns adapter construction, the event channel lifetime,
per-attempt contexts, source lifecycle transitions, restart requests, and Tail
backoff. The event channel stays open across restart attempts and is closed only
when no accepted source attempt can still send. If an adapter violates the Tail
cancellation contract and later sends after a cancellation-timeout path, the
supervisor's Tail goroutine wrapper converts that panic into adapter-failure
evidence instead of crashing the process. Before any location stat, factory lookup, adapter construction,
`Submit`, or `Scan`, the supervisor records a durable
`source_progress.lifecycle_state='starting'` row. Pre-submit failures become
durable lifecycle states (`start_failed` or `construct_failed`) and emit
`source_status_changed`. The initial `starting` row is mandatory: if SQLite
temporarily rejects it, the source startup path retries with
context-cancellable 1s/2s/.../60s backoff and does not proceed invisibly.
Supervisor-owned restart and read-model repair request channels are registered
only for a supervisor that can actually run. If startup returns before the
supervisor goroutine starts, the ingester must not retain stale request-channel
registrations for that source.

Initial Scan may defer derived FTS/rollup maintenance for that source, but
canonical rows, aggregates, `source_progress`, lifecycle/read-model state, and
notify rows still commit. After the source startup Scan outcome, the supervisor
clears that source's startup deferral, records/schedules
`read_model_state='repair_pending'`, and starts Tail. Source-scoped repair runs
behind Tail under the read-model repair coordinator. A repair attempt is allowed
to run to completion while the source supervisor context is alive; it must not
be killed by a short wall-clock timeout and restarted from the beginning,
because large local corpora can make that pattern loop forever while never
reaching `ready`. Real repair errors retry with context-cancellable
1s/2s/.../60s backoff while the source context is alive, interrupted shutdown
work remains `repair_pending`, and `read_model_repair_attempts` resets after a
successful `ready` transition. A retained all-sources reconciliation is
background repair only; it is not the only path that makes a completed source
searchable or stat-able, and it does not own source-scoped deferral or repair
state. In particular, it must not clear every source's deferral flag at start
and it must not blanket-mark sources `ready` at completion: Tail batches can
commit while the all-sources rebuild is streaming and correctly record fresh
per-source repair debt. Only the source-scoped repair loop clears that debt to
`ready`.

The startup-deferral clear records `repair_pending` as a transition-only
read-model state change. If the source is already durably `repair_pending`, the
supervisor still queues in-process repair, but it does not refresh
`read_model_state_at`, clear `read_model_error`, or emit a duplicate
`source_status_changed` row. Existing repair evidence is more important than a
cosmetic timestamp refresh.

Source-scoped FTS repair streams affected primary rows in storage order
(`ops.rowid` and `log_entries.id`) through source-session keyset loops:
`sessions(source_id, id)` pages the source's sessions, then
`ops(session_id)` and `log_entries(session_id)` page each session's rows by the
rowid ordering. It must not sort all rows for a source before each page, and it
must not walk global `ops.rowid` / `log_entries.id` while probing each row's
session/source ownership. Either plan can hold the single writer connection for
tens of seconds on large or sparse corpora and starve lifecycle/heartbeat
writes. The content-owning FTS5 rows are addressed by explicit FTS `rowid`
(`ops.rowid` for `fts_ops`, `log_entries.id` for `fts_logs`); source repair
must not delete rows by unindexed linkage columns such as `op_id`,
`session_id`, or `log_id` inside a per-row loop. `ops.rowid` is not durable
across SQLite `VACUUM`; ai-viewer runtime code must never run `VACUUM`, and an
external rowid-rewriting maintenance action requires a full FTS rebuild before
search is trusted. Migration 0014 clears existing derived `fts_ops` and
`fts_logs` rows when it introduces this explicit-docid repair contract, because
rows written by older builds can have auto-assigned FTS docids that source
repair cannot remove by the new rowid key. The clear is not source-data loss:
these tables are derived search indexes and are repopulated by source repair or
`rollups-backfill`; the recovery is test-pinned by clearing the derived tables
and proving source repair restores searchable rows from primary data.

Tail batches that commit while the retained all-sources reconciliation owns the
global rebuild-active flag also record `read_model_state='repair_pending'`.
That durable debt is paired with a post-commit, coalesced repair request to the
owning source supervisor. The request is sent only after the batch transaction
commits, and the supervisor repairs it through the same source-scoped
run-to-completion/backoff/`ready` lifecycle path used for startup-scan repair.
A worker must never leave rebuild-window repair debt waiting for daemon restart.
If the retained all-sources reconciliation itself fails after entering its
destructive rebuild path, the ingester records `repair_pending` for every known
source and sends coalesced repair requests to active supervisors before
returning the error. A partial global rebuild must be health-visible and
self-healing, not a silent search/statistics corruption. This all-source mark
uses the same source lifecycle write contract as normal source state changes:
each changed source emits `source_status_changed` atomically with its
`read_model_state='repair_pending'` update, clears stale read-model errors, and
does not rewrite `source_progress.updated_at` for an existing progress row.
Sources that are already `repair_pending` are not rewritten, do not emit another
`source_status_changed`, and keep their original `read_model_state_at` so health
grace windows cannot be extended by repeated global failures. `updated_at`
remains the source cursor/progress freshness timestamp; `read_model_state_at`
records the repair-pending transition time.

Tail liveness has two signals. A returned non-context Tail error records
`tail_failed`, transitions through `tail_restarting`, reloads
`source_progress.cursor`, constructs a fresh adapter, runs a catch-up `Scan`,
then calls `Tail` again with context-cancellable 1s/2s/.../60s backoff. A hung
Tail is detected by the adapter calling the canonical tail-heartbeat helper from
inside its real watch/poll loop; absence of heartbeat beyond the stale-tail
threshold records `tail_stale` and asks the supervisor to restart the active
attempt. If the old Tail ignores cancellation past the adapter grace, the source
records terminal/degraded `tail_failed`; process restart is recovery.
Catch-up `Scan` during `tail_restarting` is not the startup Scan: if it fails,
the source remains in `tail_restarting`, records sanitized retry evidence,
backs off, and retries from the durable cursor instead of falling through to
Tail with an unproven handoff cursor. Sustained restart failure remains retrying
but records escalated lifecycle evidence after 100 consecutive restart failures
or 24 hours in the restart loop, whichever comes first.
An adapter `Tail` returning `nil` while the source supervisor context is still
alive is an abnormal Tail exit, not a clean stop: the supervisor records
`tail_failed` evidence and enters the same restart path as a Tail error. A
`nil` Tail return is considered clean only when the supervisor context is
already cancelled. Likewise, if startup `Scan` or restart catch-up `Scan`
returns `nil` after the source context was cancelled (some adapters normalize
context cancellation to nil after saving a partial cursor), the supervisor
records `stopped` using a shutdown-safe lifecycle context and must not continue
into Tail or leave the source stuck as `scanning` / `tail_restarting`.
The persisted `tail_restart_count` is the consecutive restart-failure counter:
it increments on each `tail_restarting` attempt and resets to zero after a
successful transition back to `tailing`.
Heartbeat persistence and stale-tail checks are independent loops. A blocked or
slow heartbeat-persistence write is bounded and logged, but it cannot block the
watchdog from evaluating stale sources or enqueueing restart requests on later
ticks. Heartbeat throttling distinguishes in-memory observation, queued
persistence, and committed persistence: a heartbeat write that times out or is
dropped because the persistence queue is full must not advance the committed
throttle marker. The next heartbeat is then allowed to retry instead of letting
the durable `tail_heartbeat_at` age into a false `tail_stale` health signal.
Stale-tail state writes are also bounded per tick; if SQLite is temporarily
unavailable, the watchdog logs the failure and retries on the next tick instead
of wedging the liveness subsystem.
Tail heartbeat persistence and stale-tail watchdog operations mark a liveness
state operation as pending before they wait for the database connection.
Source-scoped read-model repair yields between its read/write batches while that
pending marker is present, so repair cannot repeatedly reacquire the single
SQLite connection ahead of liveness and starve the 30 s heartbeat/watchdog
budget.
Normal worker flushes and idle rollup refreshes participate in the same
priority rule before opening their write transaction. Startup scans can keep
multiple workers busy for long periods, so liveness priority cannot be limited
to read-model repair; every lower-priority worker transaction entry point must
yield when tail-state persistence or stale-tail checking is pending. The
background orphan resolver participates too, because its periodic maintenance
transaction uses the same single writer connection and can otherwise contend
with liveness even when worker flushes and read-model repair are yielding.

Supervisor lifecycle writes that make a source observable or monitorable are
required writes, not best-effort breadcrumbs. Before the supervisor starts or
restarts Tail, it must durably record the scan/tail state that health and the
stale-tail watchdog depend on, including the `tailing` transition with
`tail_started_at` and an initial `tail_heartbeat_at`. If such a write fails,
the supervisor retries with cancellable backoff and does not enter Tail until
the state is committed; otherwise Tail could run without watchdog coverage for
the rest of the daemon process.

Startup reconciliation runs after source resolution because only the resolved
source list can distinguish configured from historical rows. It transitions
configured non-active residues to `starting`, unconfigured rows to `stopped`,
and prior `read_model_state='repairing'` residues to `repair_pending` before
the source repair loop evaluates work.

Lifecycle and read-model state are stored on `source_progress` (see
`data-model.md` §source_progress) and are the health source of truth. Legacy
`sources.last_seen_at` remains a parse-error/pricing-miss diagnostic only and
must not be used as lifecycle freshness.

## Ingester API

```go
// New constructs an Ingester wrapping db. The pricer plugs in cost
// calculation for ops where the source did not record cost (see
// pricing.md). Pass NopPricer{} when pricing data is not available
// (SOW-0001 Chunk 7). Chunk 10 lands the Pricer interface + the
// concrete *pricing.Pricer; Chunk 11 wires `pricing.New()` into the
// production binary via the `ingest.WithPricer(...)` option. The
// ingester code does NOT change between Chunk 10 and Chunk 11 — only
// the constructor argument moves at the call site.
func New(db *sql.DB, opts ...Option) (*Ingester, error)

// Start begins the resolver goroutine. Must be called once before any
// Submit. Returns an error if the per-source seq counter cannot be
// loaded from source_progress.
func (i *Ingester) Start(ctx context.Context) error

// Submit attaches a source's event channel to the ingester. One worker
// goroutine drains the channel per call. Safe to call concurrently for
// distinct source IDs; calling twice for the same sourceID returns
// ErrSourceAlreadySubmitted.
func (i *Ingester) Submit(sourceID string, events <-chan canonical.Event) error

// ShutdownOutcome classifies StopContext results for process-exit mapping,
// tests, and operator logs.
type ShutdownOutcome string

const (
    ShutdownClean             ShutdownOutcome = "clean"
    ShutdownReplayRequired    ShutdownOutcome = "replay_required"
    ShutdownTimeout           ShutdownOutcome = "timeout"
    ShutdownWorkerFailure     ShutdownOutcome = "worker_failure"
    ShutdownResolverTimeout   ShutdownOutcome = "resolver_timeout"
    ShutdownAlreadyStopping   ShutdownOutcome = "already_stopping"
    ShutdownAlreadyStopped    ShutdownOutcome = "already_stopped"
)

// ShutdownResult carries the typed shutdown outcome plus bounded timing and
// replay evidence suitable for structured logs.
type ShutdownResult struct {
    Outcome ShutdownOutcome
}

// StopContext drains workers within ctx, preserves uncommitted batches for
// source replay when the drain deadline expires, runs a final resolver pass
// bounded by the caller's remaining deadline (maximum 5s), stops the resolver,
// and returns a typed result. Exactly one concurrent caller owns shutdown; other
// callers return already_stopping while the owner is active or already_stopped
// after it completes.
func (i *Ingester) StopContext(ctx context.Context) (ShutdownResult, error)

// Stop is the compatibility wrapper. It uses the production bounded default
// timeout, maps clean and replay-required outcomes to nil, preserves the legacy
// ErrNotStarted pre-start error, and maps timeout/failure outcomes to non-nil
// errors. It must not call StopContext(context.Background()).
func (i *Ingester) Stop() error
```

Options (functional-option pattern):

- `WithLogger(*slog.Logger)` — structured logger; default `slog.Default()`.
- `WithPricer(Pricer)` — pricing seam; default `NopPricer{}` (returns 0).
- `WithBatchSize(int)` — batch flush at N events; default 100.
- `WithBatchInterval(time.Duration)` — batch flush at duration; default 500ms.
- `WithResolverInterval(time.Duration)` — parent-link resolver tick; default 5s.

## Concurrency Model

- One **worker goroutine per source**. Each worker drains its source's `<-chan canonical.Event`, batches events into transactions of **up to 1000 events OR 500 ms**, whichever trips first, and commits.
- The ingest pipeline is **single-writer to SQLite** — every worker uses the same
  `*sql.DB` handle, and the writer store pins `MaxOpenConns=1` for both
  in-memory and on-disk stores. This gives deterministic batch ordering per
  worker while serializing transaction commits before SQLite can return
  avoidable writer-lock `SQLITE_BUSY` errors.
- The 1000-event default is a liveness budget, not a throughput target. Source
  lifecycle writes, Tail heartbeat persistence, stale-tail watchdog writes, and
  read-model repair share the same writer connection; larger cold-rebuild
  batches have live-proven they can starve the 30 s liveness write budget.
- One **background resolver goroutine** runs at 5 s ticks, retrying parent linkage for sessions inserted with `parent_session_id = NULL` whose `parent_native_id` is now resolvable. **Idle gating (SOW-0117):** the pass is skipped entirely when no canonical rows have been committed since the last pass (the worker bumps an atomic generation counter on every committed batch), and the two SESSION passes (parent-link + the transitive root recursive CTE) are additionally skipped when `MAX(sessions.last_activity_ts)` is unchanged since the last pass. That watermark is a CONSERVATIVE (over-)approximation of "a session changed": it advances on session insert/update and also when a session gains an op with a newer `end_ts` (aggregate refresh), so the session passes may run slightly more often than strictly needed (harmless — they find nothing) but never miss a real session change. The gate is effective where it matters — idle (no commits) and resume scans of unchanged sessions (the common restart case), where the recursive CTE that previously dominated CPU is skipped. The watermarks advance only on a successful pass, so a transient failure retries on the next tick. A daemon with nothing to do costs ~0 resolver CPU.

## Batching

Per-worker accumulator:

- Append every event to an in-memory slice.
- Track the set of dirty session/turn IDs touched by the batch (for aggregate updates).
- Flush when `len(batch) >= batchSize` OR `time.Since(start) >= batchInterval`.

Flush:

1. `db.BeginTx(ctx, nil)`.
2. Ensure the source row exists for the worker's `source_id`.
3. Apply every event in arrival order via per-kind UPSERTs (writer.go).
4. Refresh incremental hourly/daily rollups for dirty buckets unless this
   source's startup deferral or the global rebuild-active flag is set.
   When skipped, canonical rows still commit and
   `read_model_state='repair_pending'` is recorded.
5. Refresh `fts_ops` rows for dirty ops unless derived read-model refresh is
   deferred. `fts_logs` rows are maintained inline by `applyLogEntry` when log
   entries are written. The same global rebuild-active flag also suppresses
   idle rollup refresh-only passes while a full rebuild owns derived tables.
6. Recompute session/turn aggregates over the dirty set (aggregates.go).
7. `UPDATE source_progress SET last_seq=MAX(last_seq, batch_max_seq), cursor=last_cursor, updated_at=now()`. `last_seq` is an observability counter (max `SourceSeq` seen), not a dedup gate — see §Dedup and Idempotency.
8. Append notify rows and prune stale notify rows inside the same transaction.
9. `tx.Commit()`.
10. Promote post-commit writer state that must only become visible after a
    successful commit: pending pricing-miss dedup keys, materialized-rollup
    bucket removals, durable read-model repair requests, and the source
    high-water observability counter.

Shutdown cancellation is not an offending-batch error. Once an event is accepted
into the worker's in-memory batch, lifecycle cancellation must not cause that
event to be dropped without a committed write. Active size/interval flushes use a
write context detached from immediate lifecycle cancellation for their SQL
transaction, so a concurrent shutdown cannot abort `BeginTx` or event
application after the batch has been selected for flush. That active write
context must preserve parent context values and must not be unbounded: if the
lifecycle context is canceled while the write is in flight, the write context
starts the same bounded shutdown-drain timeout and then cancels. Lifecycle
cancellation includes explicit parent cancellation and parent deadlines; it is
not a per-query SQL timeout. A parent deadline therefore starts the bounded
shutdown grace instead of canceling the active write context immediately. Once
the worker observes lifecycle cancellation before choosing the write context, it
switches directly to the bounded shutdown-drain context for any remaining flush
and idle rollup refresh. The final flush after the producer channel closes also
uses the bounded shutdown-drain context, because parent cancellation may arrive
concurrently with the channel-close branch. Buffered events must not be dropped
merely because the parent run context was canceled before or during shutdown
`BeginTx` / event application; the bounded drain context is the explicit
shutdown deadline.

On active-run write error: `tx.Rollback()`, retry the batch a small bounded
number of times for transient SQLite contention/checkpoint races. Retry attempts
are logged as recoverable warnings and must not invoke the terminal worker
batch-failure path. After retries are exhausted, report the failure through
`worker.report` (tests may install the private `onErr` seam, production falls
back to `logger.Error`), advance past the offending batch and continue. Terminal
worker write failures are not converted into `SourceErrorEvent` rows, because
the same write path or database failure may prevent reliable persistence of that
diagnostic event; adapter parse errors and writer-detected data-quality defects
use `SourceErrorEvent` for `/api/health` visibility, while terminal worker
transaction failures are logged/reported as batch failures.

## Dedup and Idempotency

Dedup has two independent layers with a strict division of labour. **No
per-source scalar high-water-mark gates events.**

### Why no scalar high-water-mark

A single `sourceID` aggregates every session file under a source root
(e.g. `aiagent_v3:/<root>/.ai-agent/sessions` covers hundreds of files).
A per-source scalar high-water-mark assumes `SourceSeq` is monotonic
across everything under that `sourceID` — but it is not, for either
adapter:

- **aiagent_v2**: `SourceSeq = FNV-64(originId::path)` — a content hash,
  non-monotonic by design (it is a stable identifier for idempotent
  re-scans, not an ordering).
- **aiagent_v3**: `SourceSeq = packSeq(ledgerSeq, subIdx) = ledgerSeq<<12 |
  subIdx` — monotonic only **per file**. Each file's ledger restarts at
  1, so values are non-monotonic across files under one `sourceID`.

A scalar HWM therefore silently drops valid events: once events from one
file advance the HWM, lower-valued events from another file are dropped.
When the dropped event is the `OpStartedEvent` that creates the `ops`
row, a surviving child `PayloadRefEvent`/`LogEntryEvent` with a higher
`SourceSeq` then trips `FOREIGN KEY constraint failed (787)` because its
parent `ops` row was never written. This is a structural mismatch, not a
tuning problem; it cannot be fixed by a format-aware "monotonic" flag
because v3 is also non-monotonic at the source-root level. The scalar
HWM event-drop is therefore removed.

### Layer 1 — resume skipping is the adapter cursor's job

Resume after a restart is handled entirely by the adapter **cursor**, not
by any ingest-side watermark:

- aiagent_v3 tracks per-file byte offsets in its `Cursor.Files`.
- aiagent_v2 tracks per-file content state.

The cursor is loaded at startup from `source_progress.cursor` and passed
to `Scan`. Each adapter emits each file's events once in adapter order
(`OpStarted` before its `PayloadRef`/`LogEntry`), so within a single
`Scan` the parent `ops` row always exists before any child insert in the
same batch.

`SourceProgressEvent` carries no `SourceSeq` (it sits at zero by
convention) — it only updates `source_progress.cursor` and `updated_at`.

### Layer 2 — event-level idempotency is a SQL-layer guarantee

Every writer table absorbs re-emission (a Tail re-read on mtime advance,
or a re-scan of a changed file) at the SQL layer:

- `sessions`, `turns`, `ops` use `INSERT ... ON CONFLICT DO UPDATE` with
  `COALESCE` on update, keyed on their natural identity (`(source_id,
  native_id)`, `(session_id, seq)`, `(turn_id, seq)`).
  - **The `sessions` and `ops` UPSERT `extras_json` is REPLACED wholesale on
    conflict EXCEPT the resolver's `aiViewer` stash sub-object, which is grafted
    back from the existing row whenever the re-emit omits it** (the shared
    `graftAiViewerExtras` expression). Both tables are full re-emits of the same
    natural-identity row, so the new (`excluded`) extras win wholesale for every
    key the re-emit carries; the ONLY key preserved across a stash-free re-emit is
    `$.aiViewer` (`childNativeId` / `toolUseId`), the join key the resolver needs.
    A wholesale replace WITHOUT the graft would let a stash-free re-emit erase that
    join key and permanently orphan the op→child edge. **When the re-emit carries NO
    extras at all (`excluded.extras_json IS NULL`)** the result is NOT the whole old
    blob — that would stale-preserve every non-`aiViewer` key (e.g. an `aiagent_v3`
    op's copied `attr.*` attributes) the re-emit deliberately dropped, contradicting
    "excluded wins wholesale". Instead the graft yields ONLY the present
    `$.aiViewer.<stashKey>` values from the existing row (built with `json_set` onto an
    empty object), or SQL `NULL` when the existing row has no such stash — so the
    stash survives a no-extras re-emit while every other stale key is correctly
    dropped.
  - **Synthetic parent-side `SessionStartedEvent` rows are repair hints, not
    source-owned replacements.** Some adapters, including aiagent_v3, emit
    `SessionStartedEvent{Extras.synthesizedFromParent=true}` from a parent-side
    child-session reference before or after the child's own ledger is ingested.
    The writer may use those synthetic events to fill missing
    `parent_session_id`, `root_session_id`, and resolver-owned `aiViewer` linkage
    hints, but when the existing row already carries real source metadata from a
    child `session_start` (for aiagent_v3 this is proven by
    `extras_json.capturePayloads`), the synthetic replay MUST NOT overwrite
    source-owned columns or extras: `kind`, `agent_name`, `model`, `provider`,
    `provider_alias`, `cwd`, `call_path`, and non-`aiViewer` `extras_json` remain
    the real child row's values. This prevents a parent summary from changing a
    real `tool_output` child into a generic `sub_agent` or erasing
    `session_metadata` parity proof. The same synthetic replay may resolve
    `parent_session_id` / `root_session_id` only when it agrees with, or fills a
    blank in, the real child row's stashed `$.aiViewer.parentNativeId` /
    `$.aiViewer.rootNativeId`; it must not install a foreign key that contradicts
    those source-owned native ids. The resolver keeps retrying the stashed real
    native ids until the matching real parent/root rows are present.
  - **The graft uses `json_set`, NOT `json_patch`.** `json_patch` (RFC 7386
    merge-patch) treats a JSON `null` VALUE as a DELETE directive, and adapters copy
    arbitrary source attributes into op extras (e.g. `aiagent_v3` emits
    `extras["attr."+k] = v`), so a replay carrying `{"attr.x": null}` under
    `json_patch` would silently DELETE key `attr.x` from the stored row — a
    cross-adapter data-loss regression. Taking `excluded` wholesale never deletes a
    key for a null value; `json_set` only ADDS the grafted `$.aiViewer` and never
    interprets a null. The behaviour is idempotent (re-applying the same extras
    yields the same row) and is a no-op graft for adapters whose extras carry no
    `aiViewer` sub-object.
  - `SessionUpdatedEvent` and `SessionFinalizedEvent` are out-of-order tolerant.
    Before their partial update/finalize SQL runs, the writer resolves the native
    session id through the same stub-creation path used by turn/op/log events.
    Therefore an update/finalize that arrives before `SessionStartedEvent` still
    leaves a durable `sessions` row in the batch transaction. The writer must not
    add a session id to the dirty/notify sets unless the corresponding
    `sessions` row exists in that transaction; otherwise `emitNotify` would fail
    and the worker would drop a valid batch after retry exhaustion. The partial
    `SessionUpdatedEvent` update (which is NOT a full re-emit) still MERGES via
    `json_patch` by design — a partial metadata update must combine with the
    existing `aiViewer` stash (e.g. the late-meta `toolUseId` repair merges into
    the child's `parentNativeId`/`rootNativeId`), and its inputs never carry
    explicit nulls.
- `payload_refs` and `log_entries` (migration `0003`) carry
  natural-identity UNIQUE indexes and insert with `ON CONFLICT DO
  NOTHING`:
  - `payload_refs`: UNIQUE `(op_id, kind, location_uri)` — an op has at
    most one payload per `(kind, location)`.
  - `log_entries`: UNIQUE expression index over
    `(COALESCE(session_id,''), COALESCE(source_id,''),
    COALESCE(op_id,''), COALESCE(turn_id,''), ts, severity, source,
    message, COALESCE(extras_json,''))`. The `COALESCE` sentinels make
    re-emitted NULL-owner rows collide (raw SQL NULLs are distinct in a
    UNIQUE index). `turn_id` is part of the key so two genuinely-distinct
    turn-scoped log lines (turn set, op NULL) in different turns of the
    same session are not false-deduped. `extras_json` is the last keyed
    column so the key covers every persisted content column — a log row
    is a duplicate iff it is byte-identical, and two logs identical except
    for their extras (e.g. v2 stores the source `path` in extras) stay
    distinct. Omitting any persisted column reintroduces false-dedup data
    loss; the writer's `ON CONFLICT` target matches this expression list
    character-for-character.

This promotes the "idempotent ingest" invariant from a fragile
dedup-layer optimisation to a SQL-layer guarantee that holds regardless
of event ordering or batch boundaries. The natural-identity canonical
IDs (`canonicalOpID`, etc.) are deterministic across runs, so a replayed
event resolves to the same row.

### Layer 3 — payload_ref orphan guard (defense-in-depth)

`payload_refs.op_id` is `NOT NULL REFERENCES ops(id)` (migration `0001`),
so a `PayloadRefEvent` whose `(TurnSeq, OpSeq)` resolves to an op that was
never written would raise a foreign-key error and roll back the **entire
batch** — one malformed ref from any adapter could stall a source. Unlike
`log_entries.op_id` (nullable, so `applyLogEntry` simply omits the link),
a payload ref must point at a real op or not exist at all.

`applyPayloadRef` therefore verifies the parent op exists (`SELECT 1 FROM
ops WHERE id = ?`, same tx) before inserting. On a miss it does **not**
insert; instead it surfaces the orphan the same way a parse error is
surfaced — `sources.parse_errors` is bumped (visible via `/api/health`
and the Sources panel) and a source-scoped `ERR` `log_entries` row is
written — then returns nil so the rest of the batch still commits. This
is a no-silent-failure safety net: adapters are expected to emit only
op-scoped payload refs (e.g. the claude-code compaction summary attaches
to its compaction op; bare file attachments emit a `LogEntry` instead of
an orphan ref), but a future adapter bug can never roll back ingestion.

### `source_progress.last_seq` is an observability counter only

`source_progress.last_seq` still records the maximum `SourceSeq` seen per
source (advanced atomically with the batch that wrote it) and is surfaced
via `/api/health`. It is **never** read as a dedup gate. `cursor` remains
the durable resume point and advances together with the batch inside the
same transaction.

## Link Resolution

When `SessionStartedEvent` carries a `ParentNativeID`:

1. Look up `(source_id, parent_native_id) → sessions.id` in the running batch's local cache.
2. If hit, set the child's `parent_session_id` directly.
3. If miss, `SELECT id FROM sessions WHERE source_id=? AND native_id=?`.
4. If still miss (parent not yet ingested), insert child with `parent_session_id = NULL` and queue for the background resolver.

The **resolver pass** (every 5 s, gated on new ingestion — SOW-0117; see the resolver bullet in §Concurrency above):

```sql
UPDATE sessions
   SET parent_session_id = (
       SELECT id FROM sessions p
        WHERE p.source_id = sessions.source_id
          AND p.native_id = json_extract(sessions.extras_json, '$.aiViewer.parentNativeId'))
 WHERE parent_session_id IS NULL
   AND json_extract(extras_json, '$.aiViewer.parentNativeId') IS NOT NULL
   AND json_extract(extras_json, '$.aiViewer.parentNativeId') <> '';
```

The `parent_native_id` is persisted into `extras_json.aiViewer.parentNativeId` at insert time so the resolver can re-run against the durable state. This is implementation detail of the ingester (the field is not part of the public schema or API contract).

The resolver also repairs `root_session_id` in two cases:

1. **Explicit native root**: when a child was inserted before its native root
   row existed, `extras_json.aiViewer.rootNativeId` is resolved to the matching
   `(source_id, native_id)` row once it lands.
2. **Transitive parent root**: after `parent_session_id` is known, a child whose
   root still points at itself or its immediate parent is re-rooted to the
   top-level ancestor of the resolved parent chain. This keeps stored session
   trees aligned with `canonical-events.md`: `root_session_id` is the top-level
   root, not the direct parent of a nested child.

Canonical parity extraction may still use the stashed
`extras_json.aiViewer.parentNativeId` / `rootNativeId` as native lineage
evidence while a parent or root row is absent. This does not create foreign-key
links; it only lets source-vs-canonical parity compare the same source-native
ids for partial corpora.

The resolver also re-links **op→child** edges, by two complementary passes (both run inside the same transaction as the parent/root link, and both add the affected **parent** session id to the notify set):

1. `linkOpChildren` — re-links `ops.child_session_id` from `ops.extras_json.aiViewer.childNativeId` (stashed when a parent child-session op was written before its child `sessions` row existed) once a session with the matching `(source_id, native_id)` lands. Used by every adapter that emits `OpStartedEvent.ChildSessionNativeID` (ai-agent v2/v3, claude-code when the child native id is known at op-write time).

2. `linkOpChildrenByToolUse` — an ADDITIVE pass for adapters that cannot know the child native id at op-write time. It re-links `ops.child_session_id` for any op whose `child_session_id` is still NULL and whose `extras_json.aiViewer.toolUseId` is set, to the session in the SAME source whose `extras_json.aiViewer.toolUseId` matches (joined through the op's parent session for `source_id`, since `ops` carry no `source_id`). The match is ADDITIONALLY constrained to the op's STRUCTURAL child: the candidate child must descend from the op's owning (parent) session — its resolved foreign key already points at the parent (`child.parent_session_id = parent.id`) OR its stashed parent native id names the parent (`child.extras_json.aiViewer.parentNativeId = parent.native_id`). Without this, two sessions in one source sharing or forging the same `toolUseId` would let the scalar subquery pick an arbitrary same-source child; the structural constraint forces each parent op to link to its own child. This is how the claude-code adapter links a parent `Agent` op tailed BEFORE its sidecar `.meta.json` to its child session, without re-reading any transcript (which would double-count the accumulating `catalog_*` rollups). An adapter that does not stash `aiViewer.toolUseId` matches zero rows, so this pass is a no-op for ai-agent and any other format.

Both op→child passes (like the session parent/root passes) emit a `session_changed` notify for the affected parent session so an open parent-detail view refetches once the child op edge resolves.

Because the resolver mutates `parent_session_id` / `root_session_id` **outside**
the normal batch-writer path, it must emit its own `notify` rows in the **same
transaction** as the linkage UPDATEs: a `session_changed` for each affected child,
its newly-linked parent, and its root, plus one `stats_invalidated` (child-count /
topology aggregates changed). Without this, an already-open UI would never refetch
parent/child detail after a child-first ingestion is repaired — the SSE contract
(`sse-protocol.md` §`session_changed`) promises an event whenever a matching
session row changes, and resolver linkage is such a change.

## Sequencing and Aggregation Updates

After each batch's per-event UPSERTs, the ingester runs a **two-stage aggregate refresh** for the dirty session and turn IDs touched in this batch:

1. `UPDATE turns SET tokens_in = (SELECT COALESCE(SUM(tokens_in),0) FROM ops WHERE turn_id = turns.id), tokens_out=..., op_count=..., failure_count=..., cost_usd=... WHERE id IN (...)`.
2. `UPDATE sessions SET tokens_in = (SELECT COALESCE(SUM(tokens_in),0) FROM turns WHERE session_id = sessions.id), turn_count=..., op_count=(SELECT COALESCE(SUM(op_count),0) FROM turns ...), failure_count=..., cost_usd=..., last_activity_ts=MAX(last_activity_ts, batch_max_ts) WHERE id IN (...)`.

The dirty-set bounding makes each batch's aggregate work O(|dirty sessions| +
|dirty turns|), not O(table size). Idempotent — re-running on the same dirty
set produces the same numbers. Dirty sessions and turns can still be very large,
so the planner contract is explicit: turn aggregates use `idx_ops_turn_seq`;
session aggregates over `turns` use `idx_turns_session_seq`; failed-op counts
MUST use `idx_ops_session_status`; and `last_activity_ts` repair from
`MAX(ops.end_ts)` MUST use `idx_ops_session_end`. These support indexes are part
of the source-liveness budget, because aggregate refresh runs on the same single
writer connection as source lifecycle writes, Tail heartbeat persistence, and
the stale-tail watchdog. A dirty aggregate query that scans all failed ops or all
ops for a giant session can block the 30 s liveness write window and make healthy
tailers look stale.

## Catalog Tables

Inline upserts run per event in Chunk 7:

- `OpStartedEvent{Kind=llm}` → `catalog_providers` (provider, alias), `catalog_models` (provider, model).
- `OpStartedEvent{Kind=tool}` → `catalog_tools` (namespace, name).
- `SessionStartedEvent` → `catalog_agents` (source_format, agent_name) when agent_name is set; `catalog_cwds` (source_format, cwd) when cwd is set.
- `OpFinalizedEvent` → applies a `(now − prior)` DELTA to call_count's siblings — failure_count / total_tokens_* / total_cost_usd / total_duration_us — on the catalog row matching the parent OpStartedEvent (see below).

`first_seen` / `last_seen` floors/ceilings and the `ctx_max` seed are always idempotent (`MIN`/`MAX`/`COALESCE`) and run on every event. The accumulating counters are made idempotent under **op re-emission** — adapters that replay from offset 0 on resume, or carry late enrichment that corrects an op's status/identity, re-emit `OpStarted`/`OpFinalized` for the same `(turn, seq)` (SOW-0004 H1a/I1, superseded SOW-0020):

- **`call_count` increments from `OpStarted` ONLY on a genuine new op** — the writer probes whether the op's `ops` row already exists BEFORE the upsert (`requireOpExists` / `opPriorIdentity`); a same-identity re-emit is an UPDATE and adds 0, so a replay/enrichment re-emit never double-counts. `call_count = call_count + ?` where the bind is 1 on insert, 0 on a same-identity re-emit.
- **`call_count` is MIGRATED, not duplicated, when an op's catalog identity CHANGES.** A re-emitted `OpStarted` may correct the op's identity on the same `(turn, seq)` — the codex case is MCP enrichment re-stamping a heuristic `tool_namespace="custom"`→`"mcp:<server>"` (and the tool name). The writer captures the op's prior persisted identity + already-booked totals before the upsert; when the identity differs, it DECREMENTS the old catalog row's `call_count` by 1 and subtracts the op's booked failure/tokens/cost/duration totals, then re-books them under the new key (+1 call, + the migrated totals). One physical op therefore contributes to exactly ONE catalog row (its FINAL identity), `call_count = 1`.
- **`OpFinalizedEvent` applies a `(now − prior)` delta.** The writer reads the op's persisted terminal contribution (status → failure, tokens, cost, duration) BEFORE the finalize UPDATE overwrites it, then the catalog moves each total by `(new − prior)`. A first finalize sees a zero prior (delta = full contribution, identical to single-emission). A corrected re-finalize (e.g. codex output-first `exec` exit≠0 flipping a provisional `completed`→`failed`) moves `failure_count` by exactly ±1 and leaves unchanged totals at delta 0. `ctx_max` stays `MAX`-based (idempotent by construction), not delta-based.

**`duration_us` is `end_ts − start_ts`, computed at OpFinalized from the PERSISTED `start_ts`** (written by the matching OpStarted, whose `Ts` IS the op start), NOT from the OpFinalized event's own `Ts`. The canonical contract (`canonical/events.go` EventBase.Ts: events are ordered by `Ts`; a finalize sorts AFTER its start) makes `OpFinalizedEvent.Ts ≈ end`, so `EndTs − Ts ≈ 0` — using it would zero every duration. The writer therefore reads the op's `start_ts` and stores `duration_us = EndTs − start_ts` when both are known and `EndTs ≥ start_ts`, else leaves it NULL (an orphan finalize with no recorded start must not fabricate a duration). The `total_duration_us` catalog rollups are a plain additive `SUM` of member ops' `duration_us` (grouped by the op's final catalog identity), so they are recomputable directly from the corrected `ops` rows. Duration is computed ONLY at OpFinalized, from the `start_ts` persisted at that moment. The op-insert UPSERT applies `start_ts = MIN(ops.start_ts, excluded.start_ts)`, so a later OpStarted re-emit could in principle lower `start_ts` AFTER finalize without re-triggering the duration computation; adapters therefore must emit the authoritative start at or before finalize (real adapters re-emit the same or a later start, never an earlier one post-finalize). A defensive recompute-on-start-change is tracked as a follow-up (SOW-0027). **Migration `0005` backfills pre-existing `ops.duration_us` (`end_ts − start_ts` where both known) and recomputes `catalog_models.total_duration_us` / `catalog_tools.total_duration_us` from the corrected per-op values, and bumps `schema_meta.version` to `'5'`** so a serve binary (which runs no migrations) refuses a pre-0005 store and never serves the stale `0` durations — earlier builds computed duration as `EndTs − finalize.Ts`, persisting `0` for every spec-conformant adapter (SOW-0026).

So the rollups are eventually consistent with the ops table AND idempotent under any number of re-emissions of the same op.

Time-bucketed rollups for hour-/day-grained analytics (per `data-model.md` §Aggregation) were NOT in Chunk 7 — they land in SOW-0007, described in §Rollup Refresh and FTS5 Maintenance below.

## Rollup Refresh and FTS5 Maintenance

SOW-0007 adds the time-bucketed rollup tables (`rollup_hourly`, `rollup_daily`) and the FTS5 search tables (`fts_ops`, `fts_logs`) — schema in `data-model.md` §Rollup tables. The ingester maintains both incrementally, mirroring the catalog-refresh pattern (`catalog.go`'s `onSessionStarted`/`onOpStarted`/`onOpFinalized`).

**Incremental rollup refresh (affected closed hours only).** After each batch's per-event UPSERTs and the catalog refresh, the ingester recomputes the rollups for the **affected closed hours** — the set of `floor(start_ts / hour)` buckets touched by the batch, **excluding the current open hour** (the open hour is never materialized; the query layer `UNION ALL`s a live aggregate for it — `data-model.md` §Rollup tables). **The dirty-bucket set is carried forward, not per-batch:** a bucket dirtied while it is still the open hour is RETAINED in the writer's dirty set across batches (only buckets that were actually materialized — i.e. closed at refresh time — are removed) and is materialized on the first refresh after it closes. Without this, a bucket whose ops all arrive during its own open hour would be skipped while open and then never re-marked, leaving the closed bucket permanently un-materialized and silently undercounted by the all-sources fast path (filtered queries live-fold and are unaffected). **Idle materialization:** when ingestion goes quiet with buckets still pending, the worker keeps its flush timer armed and runs a refresh-only pass (no events) once per `batchEvery` interval until the carried set drains, so a bucket that closes during a lull is materialized within ~one interval rather than waiting for the next event or a manual `rollups-backfill`; the timer self-terminates when no buckets remain pending (no idle busy-spin). For each affected `(bucket_ts, source_format, dimension, dimension_value)` row, the metrics are recomputed from `ops` joined to `sessions` over that hour window, so the refresh is idempotent (recomputing an unchanged window yields the same row) and never double-counts a replayed op. The recompute is scoped to a **`source_format`**, not a `source_id`: rollup rows aggregate **every** `source_id` of that format (the PK is `source_format`), so a writer recomputing a bucket must DELETE and re-read by `src.format = <its source_format>` — reading by `source_id` would, when two sources share a format (e.g. two `claude_code` locations), delete the shared cell and re-insert only its own ops, silently dropping the sibling's contribution and diverging from `BackfillRollups` (the one-shot backfill aggregates the same way). The DELETE scope and the re-read scope MUST both be `source_format`. Affected `rollup_daily` rows are then recomputed for the affected days, **excluding the current open day** (never materialized, exactly as the open hour is excluded from `rollup_hourly`; so the current day's already-closed hours appear in `rollup_hourly` but the current day has no `rollup_daily` row — `data-model.md` §"Open-bucket rule"). **The dirty-DAY set is carried forward exactly like the hourly set** (a separate writer-lifetime carried set, NOT derived per-refresh from the dirty hours): a day touched while still open is RETAINED across batches and materialized on the first refresh after it closes, and the idle tick stays armed while EITHER pending hours OR pending days remain. Without a separate carried day set, a day whose ops all arrive before it closes — e.g. all within one still-open day, after which its hours materialize and leave the carried hour set — would be stranded once the day closes with no further events (the daily sibling of the hourly open→closed gap; the all-sources `bucket=daily` fast path, the Stats UI default, would silently undercount it). The R1 high-cardinality collapse (`maxRollupRowsPerBucket`, default 2000) is applied per `(bucket, source_format, dimension)` during the recompute so the tail folds into `dimension_value='__other__'`.

The refresh runs **within / immediately after the same batch transaction** as the per-event writes (like the catalog refresh and the resolver's notify rows), so a crash cannot leave the rollups partially applied relative to the ops they summarize.

**FTS5 maintenance.** In the same batch, the ingester inserts/updates `fts_ops` for new or changed ops and `fts_logs` for new logs. `fts_logs` maintenance is gated on the per-source `fts5_index_logs` flag (default true; `data-model.md`): when false, only `fts_ops` is maintained.

**Notify.** The batch emits exactly one `stats_invalidated` notify row when any rollup or FTS table changed — extending the existing per-batch `stats_invalidated` trigger (today fired on catalog-rollup change; §Notify Channel) to also fire on a rollup/FTS change. Still at most one per batch.

**One-shot backfill.** A `ai-viewer-ingest rollups-backfill` subcommand recomputes **all** closed-bucket rollups from `MIN(start_ts)` to the last closed bucket and builds the FTS index over all ops/logs from scratch. It applies the independent open-hour/open-day cutoffs (`data-model.md` §"Open-bucket rule"): `rollup_hourly` covers every hour bucket `< floor(now, hour)` (**including the current day's already-closed hours**), while `rollup_daily` covers every day bucket `< floor(now, day)` (**excluding the entire current open day**). It is idempotent and re-runnable — recomputing over the same `ops`/`log_entries` data reproduces byte-identical rollup tables (the property the diff gate asserts, `quality-gates.md` §Rollup correctness diff). To stay byte-identical to the incremental path it folds via the same pure `internal/rollups` package. It first **`DELETE`s `rollup_hourly` + `rollup_daily`** so the run is a TRUE full rebuild — a re-run therefore *repairs* stale rows (e.g. a dimension value that has since collapsed into `__other__`, or any row from data that has since changed), matching the incremental path's delete-then-insert and `BackfillFTS`'s delete-before-rebuild; an upsert-only backfill could only refresh rows the recompute still produces, never remove a vanished one. It then bounds memory by streaming `ops` in time order (windowed per UTC day) rather than loading the whole table, folding and inserting each closed bucket (`ON CONFLICT … DO UPDATE` is then just a harmless idempotency guard, since the tables were emptied). `now` is injectable so the closed-bucket boundary is deterministic under test. The backfill builds the FTS index the same way (`DELETE` then re-populate with explicit FTS docids) and respects `fts5_index_logs` exactly as the incremental path does. Like the rollup pass, it **bounds memory** by streaming `ops` and indexable `log_entries` in keyset batches (`WHERE id > ? ORDER BY id ASC LIMIT N`, each batch fully drained before its own write transaction) rather than loading every row at once — required because on the largest installs the FTS index spans **10M+ log entries** (§R3), and a full materialize would OOM the very memory-constrained hosts the per-source `fts5_index_logs` opt-out exists to protect. FTS rebuild and source-scoped FTS repair use the same 100-row batch budget as normal ingest writer transactions so repair cannot monopolize the single writer long enough to make Tail heartbeat or stale-tail writes time out. When the daemon runs the retained all-sources reconciliation while source supervisors are alive, every destructive/read/write step in `BackfillFTS` and `BackfillRollups` must yield to pending Tail lifecycle/heartbeat writes before opening its transaction; the offline `rollups-backfill` command has no live source supervisors and uses the same functions without a live-priority yield. A crash mid-rebuild leaves a partial index repaired by the idempotent re-run (the same bounded-memory-over-single-transaction-atomicity tradeoff the rollup pass makes). It is the recovery path when `rollup_*`/`fts_*` are missing or stale (the ingest DB is derived/disposable — `data-model.md` §Schema versioning). The query layer always serves the OPEN hour/day live, so the current period is never wrong; but CLOSED buckets are read ONLY from the materialized rollups — there is deliberately NO live-fold fallback for a missing closed bucket (that absence IS the all-sources fast-path performance contract; gap-filling would re-scan `ops` and defeat the rollup). So a missing or stale CLOSED-bucket rollup is silently UNDERCOUNTED by the all-sources fast path until `rollups-backfill` repairs it. The incremental carry-forward (§"Incremental rollup refresh" — both hours and days are carried until materialized) keeps every closed bucket materialized in normal operation, so a missing rollup is an ABNORMAL state — a corrupted/manually-cleared rollup table, or the window before the first backfill on a fresh store — not a normal occurrence; `rollups-backfill` is the deterministic repair. (A future enhancement could surface rollup staleness via `/api/health`; tracked separately, not required for correctness in normal operation.)

## Cost Computation

Cost computation is staged behind two interfaces in `internal/ingest`:

```go
// Pricer is the minimum contract: a cost lookup keyed by
// (provider, model, tsUS, token counts). tsUS is the op's start
// timestamp in UNIX-microseconds UTC; pricers that carry temporal
// tiers use it to pick the price tier that was in effect when the
// session ran. tsUS<=0 means "timestamp unknown"; the production
// pricer defaults to the most-recent tier in that case.
type Pricer interface {
    Cost(provider, model string, tsUS int64, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64
}

// DetailedPricer is an optional extension. When the wired pricer
// implements it, the writer routes lookups through CostWithDetail so
// miss events can be deduped + surfaced via the standard SourceError
// channel.
type DetailedPricer interface {
    Pricer
    CostWithDetail(provider, model string, tsUS, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) (cost float64, hit bool, missKind string)
}

// NopPricer is the default — returns 0 unconditionally so
// adapter-supplied costs flow through unchanged and ops without
// recorded cost remain at zero (visible as "cost unknown" in the UI).
// NopPricer deliberately does NOT satisfy DetailedPricer.
type NopPricer struct{}
func (NopPricer) Cost(...) float64 { return 0 }
```

Per-op rule (writer.go `applyOpFinalized` / `priceOp`):

```go
cost := ev.CostUSD
if cost == 0 && isPriceableOp(opRow) {
    if dp, ok := pricer.(DetailedPricer); ok {
        cost, hit, missKind := dp.CostWithDetail(provider, model, opStartUS,
            ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
        if !hit { emitPricingMiss(sourceID, provider, model, missKind) }
    } else {
        cost = pricer.Cost(provider, model, opStartUS,
            ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
    }
}
```

In Chunk 7 the constructor defaulted to `NopPricer{}`; Chunk 10 lands
the `internal/ingest.Pricer` + `DetailedPricer` interfaces and the
concrete `*internal/pricing.Pricer` that satisfies them. The
production binary continues to default to `NopPricer{}` at Chunk 10
(see `internal/ingest/ingester.go:131`); **Chunk 11** wires
`pricing.New()` into the production binary by passing the returned
`*Pricer` through `ingest.WithPricer(...)`. The ingester code itself
does not change between Chunk 7 and Chunk 11 — only the constructor
argument moves at the call site. See `pricing.md` §"Pricer Go types"
for the matching concrete-type contract.

## Notify Channel

The notify channel between the two binaries is the SQLite `notify` table
(`data-model.md` §notify), not a separate IPC channel. As **part of** each batch
transaction (not after it), the ingester INSERTs `notify` rows so they commit
**atomically** with the data they describe — the server can never observe a
`notify` row before the canonical rows it refers to are visible:

- one `session_changed` row per canonical session ID in the batch's
  `affectedSessionIDs`, carrying its `root_session_id` and the batch commit
  `ts_us`;
- at most one `stats_invalidated` row per batch (or per idle refresh-only
  pass), emitted when the batch/pass changed any analytics input — the catalog
  rollups, the time-bucketed `rollup_hourly`/`rollup_daily`, or the FTS tables —
  i.e. on `len(affectedSessionIDs) > 0 || rollupTouchedThisBatch ||
  rollupMaterializedThisRefresh`. It is deliberately NOT keyed off
  `len(dirtyRollupBuckets) > 0`: under carry-forward that set is non-empty
  whenever an open bucket is merely pending, which would fire on every batch;
  the per-batch `rollupTouchedThisBatch` (a new op/session marked a bucket this
  batch) and `rollupMaterializedThisRefresh` (a refresh — incl. an idle pass —
  materialized a now-closed carried bucket) preserve the "fire when rollups
  actually changed" semantics (`data-model.md` §Rollup tables, §Full-text search);
- one `source_status_changed` row when a source's `parse_errors` count,
  `enabled` flag, lifecycle state, read-model state, or lifecycle/read-model
  transition/error evidence changed. Heartbeat-only `tail_heartbeat_at`
  persistence while the source remains `tailing` deliberately does not emit a
  source-status row; it is high-frequency liveness evidence and REST health
  reads it on demand.

The ingester is the sole writer of `notify`. Once per flush cycle it also
**prunes** `notify` rows older than a bounded retention window (≈5 min) so the
table stays small — the rows are disposable transport, not history. The server
(`presenter.md` §SSE Hub) is read-only against this table: a poller goroutine
reads `WHERE seq > <cursor>` and fans the changes out to matching SSE clients
(`sse-protocol.md`). On serve restart the poller jumps its cursor to `MAX(seq)`,
so pruning already-consumed rows is always safe (clients reconcile historical
state through the REST API).

The `notify.Publisher` seam introduced in earlier chunks is now realized by
this in-transaction table writer; there is no separate IPC publisher.

## Configuration

CLI flags (also accepts env vars and a YAML config file):

```
--db <path>                SQLite path (default ~/.local/share/ai-viewer/index.db)
--state-dir <path>         state directory (default ~/.local/share/ai-viewer/). The notify channel is the SQLite `notify` table, so no IPC channel file lives here. Flag retained for other state.
--source <format>:<path>   add a source. May be repeated.
  e.g. --source aiagent_v3:~/.ai-agent/sessions
       --source aiagent_v2:~/.ai-agent/sessions
       --source claude_code:~/.claude/projects
--workers <n>              workers per adapter (default 4)
--log-level <level>        debug|info|warn|error (default info)
--metrics-addr <addr>      optional metrics endpoint bind (default disabled)
```

Auto-discovery (Phase 1.5): if no `--source` flags are given, the ingester probes default locations for each known format and adds the ones that exist. Documented in `deployment.md`.

## Graceful Shutdown

On SIGTERM/SIGINT:

1. The process installs a top-level `signal.NotifyContext` before opening the
   writer DB or dispatching one-shot subcommands. On the first SIGTERM/SIGINT it
   emits the shutdown-start log marker synchronously, calls the context's
   `stop()` function so a second signal uses the default disposition, and enters
   the bounded shutdown path.
2. Adapter contexts and `StopContext` are canceled/started together. Adapter
   waiting runs in parallel with worker drain, not as a sequential wait after
   `StopContext`. Adapters get a 5 s grace window; expiry is logged, but process
   shutdown continues.
   Each source supervisor releases any retained startup-scan-outcome signal
   exactly once on every path: scan success, recoverable scan error, fatal scan
   error, start/construct/Submit failure, and cancellation before Scan. That
   signal is for background reconciliation only and never gates Tail startup.
3. Workers drain already-buffered events and pending rollup buckets under the
   bounded shutdown-drain context. The current worker drain bound is 10 s, plus
   a conservative post-deadline SQLite `busy_timeout(5000)` tail for any flush
   attempt that starts before the drain context expires. Active writes selected
   before cancellation detach from immediate lifecycle cancellation, arm the
   same bounded write context, and then workers use a fresh drain context for
   remaining buffered work.
4. The effective idle-worker bound is 15 s. The worst mid-flush worker bound is
   25 s: 10 s detached active write, one conservative post-deadline busy tail,
   and 10 s fresh shutdown drain. Under persistent contention, workers may see
   fewer than `flushMaxRetries` attempts and must report replay-required instead
   of retrying past the drain context.
5. A final flush persists `source_progress` (`last_seq`, cursor) and notify rows
   in the same transaction as the batch. If a final batch cannot commit before
   the bounded drain deadline, the worker logs exactly one replay-required
   outcome for that source/reason, leaves source progress unadvanced, and
   returns a replay-required outcome so the next start re-emits the uncommitted
   source records. This is best-effort replay safety, not a lossless shutdown
   promise.
6. `StopContext` waits for worker completion with `wg.Wait()` in a goroutine and
   `select` on the caller context. It never waits indefinitely. The final
   resolver pass gets `min(remaining caller deadline, 5s)`; if no time remains,
   resolver work returns a timeout outcome instead of extending shutdown.
7. Source repair and any retained global reconciliation are canceled by the
   shutdown context. Unfinished read-model work remains durable as
   `read_model_state='repair_pending'`, not `repairing`; a later startup
   retries it. Tail startup is not waiting on these repair loops.
8. Explicit writer DB close runs under a 5 s goroutine/timer. On timeout, log a
   bounded writer-close timeout and let process exit close the handle. Startup
   and partial-startup paths use the close strategy matrix from SOW-0104:
   before store open no close; post-open/pre-start bounded close and no
   `StopContext`; post-start/pre-source `StopContext` then bounded close; normal
   shutdown parallel adapter wait plus `StopContext`, bounded backfill wait, and
   bounded close; bounded guard skips explicit close; ordinary startup/config
   errors after store open use bounded logged close.
9. Exit 0 for clean drain and replay-required drain expiry. Exit non-zero for
   store-close failure, permanent worker drop errors, startup/migration errors,
   timeout/failure outcomes, and bounded-guard failure.

The production ingester stop budget is 30 s:
`worker_exit_bound=25s + final_resolver<=5s`. The process-level systemd stop
budget is 45 s: `max(adapter_grace=5s, ingesterStopTimeout=30s) +
store_close=5s + 10s systemd margin`.

The pprof server is intentionally outside the shutdown wait group. It is off by
default and operator-gated; process exit reaps its listener and in-flight
handlers. `StopContext` does not wait for pprof.

Source supervisor panic recovery is out of scope for SOW-0104. Panics remain
process-fatal, matching the pre-existing behavior.

## One-Shot Commands And Locks

`check-parity` opens the canonical DB read-only and does not take the daemon
write lock.

Writer one-shot commands (`rollups-backfill`, `fts-content-backfill`,
`reprice`) use the dispatcher's signal-aware parent context, acquire/refuse the
same daemon lock key as the daemon before opening a writer handle, and exit
non-zero with a resolution-oriented message if the lock is held. The lock is
keyed on `--state-dir`. The default `--state-dir` uses the same
`resolveStateDir` path as the daemon, not `filepath.Dir(--db)`. A one-shot that
targets the system-install DB must pass `--state-dir /opt/ai-viewer/data` to
detect the system daemon lock.

`store.OpenWriter` already accepts `context.Context`. SOW-0104 requires every
daemon and one-shot call site to pass the signal-aware context so migrations and
schema repair observe SIGTERM through their existing `ExecContext` and
`QueryContext` calls.

## Failure Recovery

- Tail returned non-context error → log loudly, record `tail_failed`, then
  `tail_restarting`, and restart the source after exponential backoff
  (1 s, 2 s, 4 s, … max 60 s). Restart reloads `source_progress.cursor`, runs a
  catch-up Scan on a fresh adapter, then enters Tail.
- Tail heartbeat stale → record `tail_stale`, cancel the active attempt, and
  use the same bounded restart path.
- SQLite transaction failed → retry briefly for transient contention, then log loudly, drop the failed batch's events, and advance past them only after retries are exhausted. Future batches continue. Recovered retries are warnings, not terminal worker errors. If terminal failures spike the operator must intervene (this surfaces in `/api/health`).
- Disk full → log loudly, refuse new writes until space returns. Adapters continue scanning into a memory-buffered queue with cap 10000 events; oldest dropped if cap exceeded (counted in metrics).
