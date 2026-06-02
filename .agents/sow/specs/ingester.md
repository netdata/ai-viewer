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
  │     └─ start background resolver goroutine (5 s ticker)
  ├─ for each source:
  │    ├─ instantiate adapter via registry (ParseCursor from source_progress.cursor)
  │    ├─ goroutine: adapter.Scan(...) → events chan
  │    ├─ on Scan return: goroutine: adapter.Tail(...) → events chan
  │    └─ Ingester.Submit(sourceID, events) — spawns one worker
  └─ wait for SIGTERM/SIGINT → Ingester.Stop() → graceful shutdown
```

The ingester owns the write-side `*sql.DB`; downstream packages do not
touch it. The store remains the only SQLite handle; the ingester is the
only writer (per `data-model.md` §single-writer invariant).

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

// Stop drains every worker's pending batch, commits, persists
// source_progress, stops the resolver, and returns. Idempotent.
func (i *Ingester) Stop() error
```

Options (functional-option pattern):

- `WithLogger(*slog.Logger)` — structured logger; default `slog.Default()`.
- `WithPricer(Pricer)` — pricing seam; default `NopPricer{}` (returns 0).
- `WithBatchSize(int)` — batch flush at N events; default 1000.
- `WithBatchInterval(time.Duration)` — batch flush at duration; default 500ms.
- `WithResolverInterval(time.Duration)` — parent-link resolver tick; default 5s.

## Concurrency Model

- One **worker goroutine per source**. Each worker drains its source's `<-chan canonical.Event`, batches events into transactions of **up to 1000 events OR 500 ms**, whichever trips first, and commits.
- The ingest pipeline is **single-writer to SQLite** — every worker uses the same `*sql.DB` handle but is gated by SQLite's connection pool (set to MaxOpenConns=1 for in-memory tests; bounded for on-disk by the driver). This gives deterministic batch ordering per worker while serializing transaction commits.
- One **background resolver goroutine** runs at 5 s ticks, retrying parent linkage for sessions inserted with `parent_session_id = NULL` whose `parent_native_id` is now resolvable.

## Batching

Per-worker accumulator:

- Append every event to an in-memory slice.
- Track the set of dirty session/turn IDs touched by the batch (for aggregate updates).
- Flush when `len(batch) >= batchSize` OR `time.Since(start) >= batchInterval`.

Flush:

1. `db.BeginTx(ctx, nil)`.
2. Apply every event in arrival order via per-kind UPSERTs (writer.go).
3. Recompute session/turn aggregates over the dirty set (aggregates.go).
4. `UPDATE source_progress SET last_seq=MAX(last_seq, batch_max_seq), cursor=last_cursor, updated_at=now()`. `last_seq` is an observability counter (max `SourceSeq` seen), not a dedup gate — see §Dedup and Idempotency.
5. `tx.Commit()`.

On error: `tx.Rollback()`, log a structured error via `opts.OnError` (or default `logger.Error`), advance past the offending batch and continue. The offending events are NOT retried — they are logged for operator triage. A SourceErrorEvent is also written for visibility in `/api/health` (Chunk 11).

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
  - The partial `applySessionUpdated` UPDATE (a `SessionUpdatedEvent`, which is NOT
    a full re-emit) still MERGES via `json_patch` by design — a partial metadata
    update must combine with the existing `aiViewer` stash (e.g. the late-meta
    `toolUseId` repair merges into the child's `parentNativeId`/`rootNativeId`), and
    its inputs never carry explicit nulls.
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

The **resolver pass** (every 5 s):

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

The dirty-set bounding makes each batch's aggregate work O(|dirty sessions| + |dirty turns|), not O(table size). Idempotent — re-running on the same dirty set produces the same numbers.

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

**One-shot backfill.** A `ai-viewer-ingest rollups-backfill` subcommand recomputes **all** closed-bucket rollups from `MIN(start_ts)` to the last closed bucket and builds the FTS index over all ops/logs from scratch. It applies the independent open-hour/open-day cutoffs (`data-model.md` §"Open-bucket rule"): `rollup_hourly` covers every hour bucket `< floor(now, hour)` (**including the current day's already-closed hours**), while `rollup_daily` covers every day bucket `< floor(now, day)` (**excluding the entire current open day**). It is idempotent and re-runnable — recomputing over the same `ops`/`log_entries` data reproduces byte-identical rollup tables (the property the diff gate asserts, `quality-gates.md` §Rollup correctness diff). To stay byte-identical to the incremental path it folds via the same pure `internal/rollups` package. It first **`DELETE`s `rollup_hourly` + `rollup_daily`** so the run is a TRUE full rebuild — a re-run therefore *repairs* stale rows (e.g. a dimension value that has since collapsed into `__other__`, or any row from data that has since changed), matching the incremental path's delete-then-insert and `BackfillFTS`'s delete-before-rebuild; an upsert-only backfill could only refresh rows the recompute still produces, never remove a vanished one. It then bounds memory by streaming `ops` in time order (windowed per UTC day) rather than loading the whole table, folding and inserting each closed bucket (`ON CONFLICT … DO UPDATE` is then just a harmless idempotency guard, since the tables were emptied). `now` is injectable so the closed-bucket boundary is deterministic under test. The backfill builds the FTS index the same way (`DELETE` then re-populate) and respects `fts5_index_logs` exactly as the incremental path does. Like the rollup pass, it **bounds memory** by streaming `ops` and indexable `log_entries` in stable-id keyset batches (`WHERE id > ? ORDER BY id ASC LIMIT N`, each batch fully drained before its own write transaction) rather than loading every row at once — required because on the largest installs the FTS index spans **10M+ log entries** (§R3), and a full materialize would OOM the very memory-constrained hosts the per-source `fts5_index_logs` opt-out exists to protect. A crash mid-rebuild leaves a partial index repaired by the idempotent re-run (the same bounded-memory-over-single-transaction-atomicity tradeoff the rollup pass makes). It is the recovery path when `rollup_*`/`fts_*` are missing or stale (the ingest DB is derived/disposable — `data-model.md` §Schema versioning). The query layer always serves the OPEN hour/day live, so the current period is never wrong; but CLOSED buckets are read ONLY from the materialized rollups — there is deliberately NO live-fold fallback for a missing closed bucket (that absence IS the all-sources fast-path performance contract; gap-filling would re-scan `ops` and defeat the rollup). So a missing or stale CLOSED-bucket rollup is silently UNDERCOUNTED by the all-sources fast path until `rollups-backfill` repairs it. The incremental carry-forward (§"Incremental rollup refresh" — both hours and days are carried until materialized) keeps every closed bucket materialized in normal operation, so a missing rollup is an ABNORMAL state — a corrupted/manually-cleared rollup table, or the window before the first backfill on a fresh store — not a normal occurrence; `rollups-backfill` is the deterministic repair. (A future enhancement could surface rollup staleness via `/api/health`; tracked separately, not required for correctness in normal operation.)

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
- one `source_status_changed` row when a source's `parse_errors` count or
  `enabled` flag changed.

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

1. Cancel all adapter contexts.
2. Wait for in-flight Scan/Tail to return (with timeout 5 s).
3. Each worker flushes its pending batch (one final transaction).
4. Persist `source_progress` rows (last_seq, cursor).
5. Stop the resolver goroutine.
6. Close SQLite.
7. Exit 0.

Hard timeout: 15 s. After that, exit non-zero with a loud log.

## Failure Recovery

- Crashed adapter (returned error from Tail) → log loudly, mark source `parse_errors++`, restart adapter after exponential backoff (1 s, 2 s, 4 s, … max 60 s).
- SQLite transaction failed → log loudly, drop the failed batch's events, advance past them. Future batches continue. The decision NOT to retry is deliberate: SQL failures here are deterministic (bad data, schema mismatch); retrying never converges and burns CPU. If the failure rate spikes the operator must intervene (this surfaces in `/api/health`).
- Disk full → log loudly, refuse new writes until space returns. Adapters continue scanning into a memory-buffered queue with cap 10000 events; oldest dropped if cap exceeded (counted in metrics).
