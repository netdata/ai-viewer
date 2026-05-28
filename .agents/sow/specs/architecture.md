# Architecture

## TL;DR

Two cooperating Go binaries:

1. **`ai-viewer-ingest`** — a daemon that watches one or more source directories/databases, runs per-format adapters, and writes a normalized event stream into a canonical SQLite database.
2. **`ai-viewer-serve`** — an HTTP server that reads the canonical SQLite database, serves the embedded frontend, exposes REST endpoints for queries, and pushes realtime updates via Server-Sent Events.

The two binaries communicate **only via the SQLite file**: the canonical rows plus an append-only `notify` table the ingester writes (in the same transaction as each batch) and the serve process polls (`WHERE seq > cursor`, ~1 s). There is no second IPC channel — keeping the coupling to exactly "the SQLite file" forces strong separation of concerns and lets the two run on different hosts in the future. See `data-model.md` §notify.

## Goals That Shape This Design

- **Multi-format extensibility.** Adding a 6th source format must be one new adapter package plus its tests — no schema change, no UI change.
- **Real-time without polling.** Browser sees new spans within a few seconds of the source file changing on disk.
- **Read-only on sources.** Never write to source files, never call a live agent.
- **Debuggable.** Every event can be traced from the source file → adapter → canonical row → SSE message → browser. `curl -N` against the SSE endpoint must work.
- **Workstation-friendly.** Idle RSS under 50 MB combined; startup under 2 seconds excluding initial backfill.
- **Production-grade quality at v1.** Zero silent failures, zero half-built features.

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                           Source systems                         │
│  ai-agent v3   ai-agent v2   claude-code   codex   opencode      │
│   (files)       (files)       (files)      (files)   (sqlite)    │
└────────┬──────────┬────────────┬────────────┬───────────┬───────┘
         │ fsnotify  │ fsnotify   │ fsnotify   │ fsnotify  │ wal-tail
         ▼           ▼            ▼            ▼           ▼
┌─────────────────────────────────────────────────────────────────┐
│                      ai-viewer-ingest                            │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │ adapters/  (one goroutine pool per adapter)               │   │
│  │   aiagent_v3 │ aiagent_v2 │ claude_code │ codex │opencode │   │
│  └───────────┬───────────────────────────────────────────────┘   │
│              │ canonical.Event chan                              │
│              ▼                                                   │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │ ingest/                                                   │   │
│  │   - idempotent upserts (natural-identity dedup at SQL layer)│  │
│  │   - resolve parent/child links                            │   │
│  │   - write to SQLite in batched txns                       │   │
│  │   - append `notify` rows in the SAME txn; prune old rows   │   │
│  └───────────────────────────────────────────────────────────┘   │
└───────────────────┬───────────────────────────────────────────────┘
                    │ SQLite file (WAL mode): canonical rows +
                    ▼ append-only `notify` table (serve polls seq>cursor)
┌─────────────────────────────────────────────────────────────────┐
│                      ai-viewer-serve                             │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │ presenter/                                                │   │
│  │   GET  /api/sessions?filter=...      (REST)               │   │
│  │   GET  /api/sessions/:id             (REST)               │   │
│  │   GET  /api/stats?filter=...         (REST)               │   │
│  │   POST /api/subscriptions            (REST)               │   │
│  │   GET  /api/events?sub=...           (SSE)                │   │
│  │   GET  /api/payloads/:ref            (REST, lazy)         │   │
│  │   GET  /                             (embedded frontend)  │   │
│  └─────┬─────────────────────────────────────────────────────┘   │
│        │ go:embed                                                │
│  ┌─────▼─────────────────────────────────────────────────────┐   │
│  │ frontend (built Vite bundle, served from /)               │   │
│  └───────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                    │ HTTP + SSE
                    ▼
                 Browser
```

## Why two binaries

- **Process isolation.** A crash in the ingester does not bring down the UI; a crash in the server does not lose ingest progress.
- **Independent deployability.** The ingester can run on the host where snapshots live (e.g. `agent-events`), while the server runs on the workstation, sharing only the SQLite file over a network mount or rsync — a future option, not v1. Because the notify channel is a table inside that same file (rather than an IPC channel), it works over a shared-file mount where a local IPC channel would not.
- **Forced separation of concerns.** The ingester cannot accidentally serve HTTP; the server cannot accidentally write canonical rows. The boundary is the schema.
- **Easier to reason about.** Two small mental models instead of one large one.

## Why SQLite as the canonical store

- Indexed time-range and filter queries over millions of rows; we cannot grep 30 GB of gzipped JSON per query.
- WAL mode supports concurrent writer + many readers — exactly our pattern.
- Single file, no server, no operational burden.
- Excellent Go support via `modernc.org/sqlite` (pure Go, no CGO, cross-compiles cleanly).
- Frontend never touches SQLite directly — only via REST/SSE from the server.

## Why SSE not WebSocket

User decision after debuggability tradeoff analysis. SSE is trivially debuggable (`curl -N`), browser-native (`EventSource`), auto-reconnects, works through any HTTP proxy. Control-plane (subscription create/update, filter changes) is plain REST.

## Adapter pattern

Each source format is one Go package under `internal/adapters/`. Every adapter implements:

```go
type Adapter interface {
    Name() string
    Scan(ctx context.Context, since canonical.Cursor, out chan<- canonical.Event) error
    Tail(ctx context.Context, out chan<- canonical.Event) error
}
```

- `Scan` is the backfill path: walk what's on disk now, emit events from `since` forward.
- `Tail` is the realtime path: subscribe to file/DB changes, emit events as they arrive.
- Both write into the same channel. The ingester does not care which path produced an event.

Adding a new format = one new package, one Adapter implementation, fixture files under `testdata/<format>/`, unit tests, and a row in the adapters table in the UI. No schema migration. No UI change.

## Data flow timing (target)

| Stage | Target latency |
|---|---|
| Source file written → fsnotify event → adapter parses | < 100 ms |
| Adapter event → ingest dedup → SQLite write | < 50 ms |
| SQLite commit → notify channel → SSE message → browser | < 100 ms |
| **End-to-end (file write → visible in UI)** | **< 500 ms typical, < 2 s p99** |

Backfill of the 294K existing local snapshots is a separate concern with its own progress UI; targeted to complete in under an hour on the user's workstation. Phase 1 SOW will validate this with real timing.

## Failure handling principles

- Every parse error is logged with file path, byte offset, format version, and reason. Counted per-adapter. Surfaced in `/health` and in the UI's adapter status panel.
- A persistently-failing source does NOT block other sources: each adapter runs in its own goroutine pool with its own cursor.
- The ingester is **at-least-once**; dedup is by natural-identity idempotent upserts in SQLite (every table has an ON CONFLICT key); `(source_id, source_seq)` is NOT the dedup key. Idempotent writes are mandatory.
- If the canonical schema migrates, the ingester resets its cursor for affected sources and re-reads. Cursors are stored in SQLite alongside the rows.

## Code organization rules

- `internal/canonical/` defines types. Knows nothing about HTTP, SQLite, or any adapter.
- `internal/adapters/<name>/` depends on `canonical/`. Knows nothing about ingest, store, or HTTP.
- `internal/ingest/` depends on `canonical/` and `store/`. Knows nothing about HTTP or specific adapters (uses the Adapter interface only).
- `internal/store/` defines the SQLite schema, migrations, query helpers. Knows nothing about HTTP, canonical events, or adapters.
- `internal/presenter/` depends on `store/`. Knows nothing about adapters or ingest internals (only the notify channel).
- `cmd/ai-viewer-ingest/` wires adapters + ingest + store.
- `cmd/ai-viewer-serve/` wires store + presenter + embedded frontend.

A change in one package should rarely cascade into another. If it does, the spec is wrong; revisit the boundary before committing.
