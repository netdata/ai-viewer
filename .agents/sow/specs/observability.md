# Observability

## TL;DR

Structured logging (slog), a `/api/health` endpoint, per-adapter parse-error counters, and an optional Prometheus-style metrics endpoint. No silent failures, ever.

## Structured Logging

- Library: Go stdlib `log/slog` with JSON handler when running as a daemon, text handler in dev.
- Every log line carries: `ts`, `level`, `subsystem` (`ingest`/`presenter`/`adapter:<name>`/`store`), `msg`, and contextual key=value pairs.
- Levels: `debug`, `info`, `warn`, `error`. Default level `info`. CLI flag `--log-level`.
- Adapter errors include `source_id`, `file`, `offset`, `record_seq` when applicable.
- Per-request HTTP logs (one line per request) include: `method`, `path`, `status`, `duration_us`, `bytes_out`, `client_ip`.

Mandatory log moments:

- Ingester startup: list every configured source with format and location.
- Adapter scan start/finish with event count and duration.
- Every parse error (with file:offset).
- Every SQLite error.
- Source disabled / re-enabled.
- Server startup: bind address, embedded frontend SHA.
- Graceful shutdown progress.

Prohibited log patterns:

- `log.Printf("error: %v", err)` without context. Every error log includes the subsystem and the operation in progress.
- Swallowed errors (`if err != nil { return }` without logging at least at debug level).

## /api/health

The single source of truth for "is this thing alive and what state is it in":

```json
{
  "status": "ok" | "degraded" | "down",
  "version": "<git sha>",
  "schema_version": 1,
  "uptime_s": 12345,
  "db_path": "...",
  "db_size_bytes": 12345678,
  "sources": [
    {
      "id":"...",
      "format":"aiagent_v3",
      "location":"...",
      "enabled":true,
      "last_seen_at":<us>,
      "lag_us":<int>,           // now - last_seen_at
      "parse_errors":0,
      "events_ingested_total":12345
    }
  ]
}
```

`status` is `degraded` when:

- Any enabled source has `lag_us > 60_000_000` (60 s of staleness).
- Any source has parse_errors > 0 in the last hour.

`status` is `down` when:

- SQLite is unreachable.
- Schema migration failed.

The Sources page in the UI surfaces every field with appropriate colors.

## Metrics (optional, Phase 2)

A Prometheus-style endpoint at `/metrics` exposing:

- `aiviewer_ingest_events_total{source,kind}`
- `aiviewer_ingest_parse_errors_total{source,reason}`
- `aiviewer_ingest_lag_us{source}`
- `aiviewer_sqlite_write_duration_seconds_bucket{...}`
- `aiviewer_http_requests_total{method,path,status}`
- `aiviewer_http_request_duration_seconds_bucket{method,path}`
- `aiviewer_sse_subscriptions{state}`

Disabled by default (the user is single-host and Netdata already scrapes plenty). Enabled with `--metrics-addr 127.0.0.1:7711`.

## Trace IDs

Every HTTP request generates a `request_id` (UUID-v4) attached to all log lines for that request and returned as `X-Request-ID` header. Useful for grepping a specific user action across logs.

## Self-Documenting Errors

User-facing errors (HTTP 4xx/5xx) carry a structured shape:

```json
{
  "error": {
    "code": "FILTER_PARSE_ERROR",
    "message": "Invalid time range: 'from' is after 'to'",
    "details": { "from": 1234, "to": 999 }
  }
}
```

Error codes are listed in `internal/presenter/errors.go` with comments. The UI maps codes to human-friendly messages where appropriate.

## Adapter Status Surface

The Sources page renders, per source:

- Last 24h parse error count (red badge if > 0).
- Lag indicator (green < 1s, amber < 10s, red > 10s).
- Last 100 parse error log lines (expandable).
- Toggle to enable/disable the source (Phase 2; needs writes).

## Operator Runbook

Lives at `docs/runbook.md` (created during Phase 1). Covers:

- "Ingester is lagging" → check inotify limits, disk space, adapter logs.
- "Parse errors spiking" → expand source detail in UI, inspect log lines, check source format upgrade.
- "Server returns 500 on /api/sessions" → check SQLite size, check WAL contention, restart server.
- "SSE events not arriving" → check `/api/health`, check notify socket, check browser EventSource console.
