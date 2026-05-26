# Presenter (`ai-viewer-serve`)

## TL;DR

HTTP server. Serves the embedded frontend at `/`, exposes REST endpoints under `/api/`, and a single SSE endpoint at `/api/events`. Reads canonical SQLite. Knows nothing about adapters.

## Lifecycle

```
main()
  ├─ load config (db path, bind addr, state dir)
  ├─ open SQLite (read-only mode for safety; WAL allows concurrent writer)
  ├─ run migration check (refuse to start if schema_meta.version > supported)
  ├─ start SSE hub goroutine
  ├─ start notify-socket subscriber goroutine
  ├─ register HTTP routes
  ├─ ListenAndServe on bind addr
  └─ wait for SIGTERM/SIGINT → graceful shutdown
```

## SQLite Access

- Opened with `?mode=ro&_journal_mode=WAL&_busy_timeout=5000`.
- Connection pool size: 8 (configurable). Sufficient for single-user load; revisit if multi-user.
- All queries are parameterized prepared statements held in a package-level cache.
- Long-running queries are killed at 30 s; the user gets a 504 with a useful error.

## SSE Hub

Single goroutine that owns:

- A map of `subscription_id → subscriber_state` (in-memory only; lost on restart).
- An events channel from the notify-socket subscriber.
- An events channel from each connected SSE client (for keepalives).

When a notify message arrives, the hub:

1. Looks up affected sessions in the message.
2. For each subscription, checks if any affected session matches the subscription's filter.
3. Pushes a minimal `event` to matching clients via their SSE channel.

The SSE message does NOT carry the full session data — only `{"type":"session_changed","session_id":"..."}`. The client decides whether to re-fetch the full session detail via REST. This keeps SSE messages tiny and keeps the SQL load on REST endpoints where caching and pagination already live.

## Embedded Frontend

The frontend's Vite-built `dist/` directory is embedded via `go:embed`:

```go
//go:embed all:frontend_dist
var frontendFS embed.FS
```

Build pipeline: `scripts/build.sh` runs `npm --prefix frontend run build`, copies output to `cmd/ai-viewer-serve/frontend_dist/`, then `go build`. Documented in `deployment.md`.

## Routing

```
GET  /                          → frontend index.html
GET  /assets/*                  → embedded frontend assets
GET  /api/health                → JSON health status
GET  /api/sources               → list sources, ingest cursors, parse error counts
GET  /api/sessions              → list sessions with filters and pagination
GET  /api/sessions/:id          → session detail with turns and ops
GET  /api/sessions/:id/logs     → log entries for a session
GET  /api/sessions/:id/topology → topology graph for a session (nodes, edges)
GET  /api/sessions/:id/timeline → ordered spans for the timeline view
GET  /api/stats                 → cross-session statistics with filters
GET  /api/catalog/tools         → catalog_tools, with filters
GET  /api/catalog/models        → catalog_models, with filters
GET  /api/catalog/agents        → catalog_agents, with filters
GET  /api/payloads/:ref         → resolves payload_ref → streams the bytes (gz-decompressed inline if requested)
POST /api/subscriptions         → create an SSE subscription with a filter
DELETE /api/subscriptions/:id   → cancel
GET  /api/events?sub=:id        → SSE event stream
```

Full schemas: `rest-api.md`.

## Middlewares

- **Request logging**: structured slog, one line per request with method, path, status, duration, bytes.
- **Recover panic**: log + 500 + continue serving (do NOT crash on a single bad handler).
- **Gzip**: response bodies > 1 KB gzipped when client supports it (excluding SSE).
- **CORS**: not enabled in v1 (localhost-only). Documented as a Phase 2 concern.
- **Auth**: none in v1 (localhost-only bind). Phase 2 SOW will define auth design.

## Graceful Shutdown

1. Stop accepting new connections (`http.Server.Shutdown`).
2. Close all SSE client channels with a `disconnect` event so browsers reconnect cleanly.
3. Wait for in-flight handlers (with 30 s timeout).
4. Close SQLite, close notify socket.
5. Exit 0.

## Configuration

```
--db <path>                SQLite path (must exist; ingester creates it)
--state-dir <path>         where notify.sock lives
--bind <addr>              default 127.0.0.1:7710
--log-level <level>        default info
```

The port 7710 is a placeholder; will be locked in during Phase 1 implementation when no conflicts are confirmed on the user's workstation.
