# Ingester (`ai-viewer-ingest`)

## TL;DR

A long-running daemon. Loads source configs, runs one adapter per source, fans events into a single ingest pipeline that dedups, resolves links, and writes batched transactions into SQLite. Emits notify-pings to the server.

## Lifecycle

```
main()
  ├─ load config (sources, paths)
  ├─ open SQLite, run migrations
  ├─ load source cursors from sources table
  ├─ start metrics endpoint (optional, separate from serve binary)
  ├─ for each source:
  │    ├─ instantiate adapter via registry
  │    ├─ goroutine: adapter.Scan(...) → events chan
  │    ├─ on Scan return: goroutine: adapter.Tail(...) → events chan
  │    └─ goroutine: periodic cursor checkpoint
  ├─ goroutine: ingest pipeline reads events chan
  └─ wait for SIGTERM/SIGINT → graceful shutdown
```

## Concurrency Model

- One goroutine pool per adapter (workers configurable, default 4 per adapter).
- A single buffered channel `chan canonical.Event` (capacity e.g. 1024) carries all events into the ingest pipeline.
- The ingest pipeline itself is **single-writer to SQLite** — this avoids lock contention and gives deterministic ordering for batched commits.
- Periodic flush: every 500 ms OR every 1000 events, whichever comes first.

## Dedup

```go
type hwm struct {
    sourceID string
    seq      uint64
}
```

Per-source high-water-mark stored in memory and persisted to `sources.cursor`. Events with `SourceSeq <= hwm` are dropped without write. The HWM is advanced atomically with the SQLite commit.

For sources whose adapters use non-monotonic SourceSeq (e.g. content-hash-based), dedup is done via SQLite `INSERT ... ON CONFLICT DO UPDATE` on the appropriate UNIQUE constraint.

## Link Resolution

The ingester resolves parent/child session links:

1. When `SessionStartedEvent` arrives with `ParentNativeID`, look up parent's canonical session id from `(source_id, native_id) → id` cache (LRU-bounded).
2. If parent not yet seen: write child session with `parent_session_id = NULL`, queue for later resolution.
3. Periodic resolver pass (every 5 s): retry NULL parents in case the parent arrives later.

The cache eviction policy: LRU with capacity 10,000 sessions. Cold lookups fall back to SQLite.

## Sequencing and Aggregation Updates

When events arrive:

- `OpFinalizedEvent` updates `ops` row, then increments parent `turns` and `sessions` aggregates (tokens, cost, op_count, failure_count if applicable).
- `TurnFinalizedEvent` updates `turns` row and `sessions` aggregates.
- Catalog tables (`catalog_tools`, `catalog_models`, `catalog_agents`) updated by triggers in SQLite (preferred) or by ingester after each batch (fallback if trigger complexity grows).

Decision for v1: explicit ingester-side aggregate updates in the same transaction. SQLite triggers may come in Phase 2 if perf demands it.

## Notify Channel

After each successful SQLite commit, the ingester sends a small message to the server over a Unix domain socket at `<state-dir>/notify.sock`:

```json
{"type":"batch_commit","affected_sessions":["<id>","<id>"],"ts":<us>}
```

The server's SSE hub subscribes to this socket and fans the message out to interested SSE clients. If the socket is not connected (server not running), the ingester drops the message — clients will get the data on next subscribe.

Fallback: if Unix socket setup fails, fall back to "poll WAL mtime every 250 ms" on the server side. Spec'd here so the server has a defined fallback path.

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
3. Drain the events channel, write final batch.
4. Persist all source cursors.
5. Close SQLite.
6. Exit 0.

Hard timeout: 15 s. After that, exit non-zero with a loud log.

## Failure Recovery

- Crashed adapter (returned error from Tail) → log loudly, mark source `parse_errors++`, restart adapter after exponential backoff (1 s, 2 s, 4 s, … max 60 s).
- SQLite transaction failed → retry once with exponential backoff; if still failing, log loudly and exit 1 (this is a configuration or disk-full problem; user must intervene).
- Disk full → log loudly, refuse new writes until space returns. Adapters continue scanning into a memory-buffered queue with cap 10000 events; oldest dropped if cap exceeded (counted in metrics).
