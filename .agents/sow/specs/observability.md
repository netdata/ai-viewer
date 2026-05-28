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
- Frontend not built: a single `Info` line when `GET /` is served with the
  built-in not-built notice (the embedded FS has no `index.html`), instructing
  the operator to run `scripts/build.sh`. Logged once so a dev-time unbuilt
  state is visible without flooding the log on every request.
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
  "schema_version": 3,
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
      "last_seq":12345          // per-source observability counter (max SourceSeq seen); NOT a dedup gate
    }
  ],
  "notify": {
    "last_seq":67890,           // last notify.seq the poller applied (0 before the first row)
    "lag_us":<int>              // now - ts_us of the last applied notify row; 0 when idle (no rows applied yet)
  },
  "sse": {
    "subscriptions":3           // active SSE subscriptions held by the hub (incl. those in the 60 s reconnect window)
  }
}
```

`notify.last_seq` is the high-water `notify.seq` the read-only poller has
applied (it starts the cursor at `MAX(seq)` on boot, so `last_seq` advances
only with changes that occur while serve is running); `notify.lag_us` is `now`
minus the `ts_us` of that last applied row and is `0` when the poller has not
yet applied any row. `sse.subscriptions` is the count of active subscriptions
the hub holds. These three fields are the operator's window into the
ingester→serve notify path (`presenter.md` §SSE Hub; `data-model.md` §notify).

`last_seq` is a per-source observability counter: the max `SourceSeq`
seen for that source (`source_progress.last_seq`). It is **NOT a dedup
gate** (dedup is a SQL-layer guarantee — see ingester.md §Dedup and
Idempotency) and **NOT a portable event count. Semantics depend on the
adapter:**

- `aiagent_v3` packs `ledgerSeq << 12 | subIdx`, so it grows roughly
  linearly with events emitted by that source — operators can
  approximate "events ingested" by comparing two snapshots of the
  same source.
- `aiagent_v2` packs FNV-64(`originId`, opTree path), which is an
  opaque 64-bit hash — it bears no relation to event count and the
  value can be very large (e.g. ~9.2e18).

Do NOT compare `last_seq` across formats; the only portable meaning is
"the most recent cursor position the writer committed". A separate
true `events_ingested_total` counter is future work and not yet
plumbed; the field was renamed in iteration 2 of SOW-0001 Chunk 11
once a real-corpus run surfaced the misleading v2 values.

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
- "SSE events not arriving" → check `/api/health`, check the notify-table poller is advancing (`notify.last_seq` increasing as the ingester commits), check browser EventSource console.
