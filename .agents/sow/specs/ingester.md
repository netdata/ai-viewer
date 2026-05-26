# Ingester (`ai-viewer-ingest`)

## TL;DR

A long-running daemon. Loads source configs, runs one adapter per source, fans events into a single ingest pipeline that dedups, resolves links, and writes batched transactions into SQLite. Emits notify-pings to the server.

## Lifecycle

```
main()
  ├─ load config (sources, paths)
  ├─ open SQLite (OpenWriter), run migrations
  ├─ construct Ingester(db, pricer, logger)
  ├─ Ingester.Start(ctx)
  │     ├─ load per-source HWM cache from source_progress
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
// (SOW-0001 Chunk 7); Chunk 10 wires in the real Pricer.
func New(db *sql.DB, opts ...Option) (*Ingester, error)

// Start begins the resolver goroutine. Must be called once before any
// Submit. Returns an error if the HWM cache cannot be loaded.
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
4. `UPDATE source_progress SET last_seq=MAX(last_seq, batch_max_seq), cursor=last_cursor, updated_at=now()`.
5. `tx.Commit()`.

On error: `tx.Rollback()`, log a structured error via `opts.OnError` (or default `logger.Error`), advance past the offending batch and continue. The offending events are NOT retried — they are logged for operator triage. A SourceErrorEvent is also written for visibility in `/api/health` (Chunk 11).

## Dedup

The ingester maintains an in-memory **per-source high-water-mark (HWM)**:

```go
type Ingester struct {
    hwm map[string]uint64 // sourceID -> last_seq
    ...
}
```

Loaded at `Start()` from `source_progress.last_seq`. Updated atomically with each batch commit.

Per-event check:

```go
if ev.EventSourceSeq() <= hwm[ev.EventSourceID()] {
    drop // already seen
}
```

`source_progress.last_seq` is the durable HWM; `source_progress.cursor` is the durable resume point. The two advance together inside the batch transaction so a restart resumes correctly even if the binary is killed mid-flush.

`SourceProgressEvent` carries no `SourceSeq` (it sits at zero by convention) — it only updates `source_progress.cursor` and `updated_at` without advancing the HWM.

Idempotency at the SQL layer: every `*Started`/`*Updated`/`*Finalized` event uses `INSERT ... ON CONFLICT DO UPDATE` with `COALESCE` on update so a replayed event after a crash produces the same row state. Re-emitted events from a re-scan are caught by the HWM first; the UPSERT is the second line of defence.

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
- `OpFinalizedEvent` → increments call_count / failure_count / totals on the catalog row matching the parent OpStartedEvent.

The catalog rows use SQLite's `ON CONFLICT (...) DO UPDATE SET first_seen=MIN(first_seen, excluded.first_seen), last_seen=MAX(last_seen, excluded.last_seen), call_count=call_count+1, ...` so the rollups are eventually consistent with the ops table.

Time-bucketed rollups for hour-/day-grained analytics (per `data-model.md` §Aggregation) are NOT in Chunk 7 — they land in SOW-0007.

## Cost Computation

Cost computation is staged behind the `Pricer` interface:

```go
type Pricer interface {
    Cost(provider, model string, tokensIn, tokensOut, tokensCacheRead, tokensCacheWrite int64) float64
}

type NopPricer struct{}
func (NopPricer) Cost(...) float64 { return 0 }
```

Per-op rule:

```go
cost := ev.CostUSD
if cost == 0 {
    cost = pricer.Cost(provider, model, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
}
```

In Chunk 7 the constructor defaults to `NopPricer{}`; Chunk 10 wires in the real implementation backed by `internal/pricing/pricing.json`. The ingester code does not change between the two — only the constructor argument.

## Notify Channel

After each successful SQLite commit, the ingester sends a small message to the server over a Unix domain socket at `<state-dir>/notify.sock`:

```json
{"type":"batch_commit","affected_sessions":["<id>","<id>"],"ts":<us>}
```

The server's SSE hub subscribes to this socket and fans the message out to interested SSE clients. If the socket is not connected (server not running), the ingester drops the message — clients will get the data on next subscribe.

Fallback: if Unix socket setup fails, fall back to "poll WAL mtime every 250 ms" on the server side. Spec'd here so the server has a defined fallback path.

The notify wiring is **not in Chunk 7**; it lands with the server scaffolding in Chunk 11. Chunk 7 leaves a no-op `notify.Publisher` interface so the seam exists.

## Configuration

CLI flags (also accepts env vars and a YAML config file):

```
--db <path>                SQLite path (default ~/.local/share/ai-viewer/index.db)
--state-dir <path>         where notify.sock lives (default ~/.local/share/ai-viewer/)
--source <format>:<path>   add a source. May be repeated.
  e.g. --source aiagent_v3:/home/costa/.ai-agent/sessions
       --source aiagent_v2:/home/costa/.ai-agent/sessions
       --source claude_code:/home/costa/.claude/projects
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
