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

Graceful shutdown log markers are structured and stable. They must not include
raw payloads, raw payload locations, or operator-specific host/user names.
`source_id` is the existing canonical diagnostic source key; do not add separate
raw `location` or payload fields to shutdown markers.

Mandatory shutdown markers:

- `shutdown_start`: emitted synchronously from the signal-observer path before
  deferred shutdown work begins. Fields: `subsystem`, `signal`, `timeout_ms`.
- `shutdown_adapter_grace_expired`: aggregate adapter wait exceeded the 5 s
  grace. Fields: `elapsed_ms`, `grace_ms`. This marker is aggregate because the
  shutdown wait group does not identify which adapter goroutine is still active.
- `shutdown_worker_retry_suppressed`: shutdown context expired and normal
  transient retry logging was suppressed. Fields: `source_id`, `reason`.
- `shutdown_replay_required`: a final batch could not commit before the bounded
  drain deadline and source progress was left unadvanced. Fields: `source_id`,
  `source_format`, `outcome=replay_required`, `pending_events`, `reason`.
- `shutdown_backfill_cancelled`: read-model backfill observed shutdown and is
  safe to retry. Fields: `phase`, `elapsed_ms`.
- `shutdown_backfill_timeout`: the 5 s backfill wait expired and explicit DB
  close will be skipped. Fields: `elapsed_ms`, `timeout_ms`.
- `shutdown_resolver_timeout`: the final resolver pass had no remaining caller
  deadline or exceeded its capped window. Fields: `remaining_ms`, `timeout_ms`.
- `shutdown_store_close_error`: explicit store close returned an error. Fields:
  `store_role` (`writer` or `reader`), `elapsed_ms`, `err`.
- `shutdown_store_close_timeout`: explicit store close exceeded its 5 s close
  timer. Fields: `store_role`, `elapsed_ms`, `timeout_ms`.
- `shutdown_clean`: graceful shutdown completed with all bounded phases clean.
  Fields: `subsystem`, `elapsed_ms`, `outcome=clean`.
- `shutdown_bounded_guard`: shutdown exited non-zero because a bounded guard
  fired while live work may still own SQLite. Fields: `phase`, `elapsed_ms`,
  `outcome`.

Forced-kill evidence comes from missing terminal markers plus systemd status and
journal timing. The process must log enough phase markers that an operator can
distinguish clean drain, replay-required drain, bounded guard, and external
SIGKILL without journal scraping inside the installer.

Prohibited log patterns:

- `log.Printf("error: %v", err)` without context. Every error log includes the subsystem and the operation in progress.
- Swallowed errors (`if err != nil { return }` without logging at least at debug level).

## /api/health

The single source of truth for "is this thing alive and what state is it in":

```json
{
  "status": "ok" | "degraded" | "down",
  "version": "<git sha>",
  "schema_version": 5,
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
      "last_seq":12345,         // per-source observability counter (max SourceSeq seen); NOT a dedup gate
      "meta":{                  // OPTIONAL — omitted when the adapter did not populate sources.meta_json
        "session_count":42,     // opencode source-native row counts (the source DB's own tables),
        "message_count":1200,   // NOT ai-viewer's ingested canonical counts; a distinct signal.
        "part_count":3400,      // File-based adapters omit the field entirely (NULL ≠ zero).
        "latest_migration":"0009_..."
      }
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

`meta` is the **optional per-source metadata blob** (SOW-0024). It is the
`sources.meta_json` column rendered verbatim; the field is OMITTED from the
response when the adapter did not populate the column (NULL), so absence — not
zero — is the "adapter has no metadata" signal. It is adapter-owned: opencode
populates `{session_count, message_count, part_count, latest_migration}` from
its startup probe (source-native opencode-DB row counts, NOT ai-viewer's
ingested canonical counts); file-based adapters omit it. Freshness is the last
ingester startup (the probe runs once at auto-discovery; a restart refreshes
it). The presenter renders the blob as-is and has no per-adapter knowledge of
its shape (`data-model.md` §sources).

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
