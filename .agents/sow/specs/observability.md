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
- Source lifecycle transitions for start failure, construct failure, scan
  failure, Tail start, Tail failure, Tail restart/backoff, stale Tail, and
  clean source stop.
- Read-model repair pending/start/ready/timeout/failure transitions.
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
  "status_detail": "no_sources_configured",
  "schema_version": 12,
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
      "lag_us":<int>,           // legacy diagnostic; not lifecycle freshness
      "parse_errors":0,
      "last_seq":12345,         // per-source observability counter (max SourceSeq seen); NOT a dedup gate
      "progress_updated_at":<us>,
      "lifecycle_state":"tailing",
      "lifecycle_state_at":<us>,
      "scan_started_at":<us>,
      "scan_completed_at":<us>,
      "tail_started_at":<us>,
      "tail_heartbeat_at":<us>,
      "tail_failed_at":null,
      "tail_restart_count":0,
      "lifecycle_error":null,
      "read_model_state":"ready",
      "read_model_state_at":<us>,
      "read_model_repair_started_at":null,
      "read_model_repair_completed_at":<us>,
      "read_model_repair_failed_at":null,
      "read_model_repair_attempts":0,
      "read_model_error":null,
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

`status_detail` is omitted for normal responses. The only current value is
`no_sources_configured`, returned with `status="degraded"` and an empty source
list when the ingester has no configured or discovered source.

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

Lifecycle/read-model fields are the primary source-health contract:

- `progress_updated_at` is `source_progress.updated_at`, the timestamp of the
  last committed progress row.
- `last_seen_at` remains a legacy parse-error/pricing-miss diagnostic timestamp.
  Do not derive lifecycle freshness or source health from it.
- `lifecycle_state`, `lifecycle_state_at`, `scan_started_at`,
  `scan_completed_at`, `tail_started_at`, `tail_heartbeat_at`,
  `tail_failed_at`, `tail_restart_count`, and `lifecycle_error` are persisted
  source lifecycle evidence. `tail_restart_count` is consecutive restart
  evidence and resets to zero after a successful transition back to `tailing`.
- `read_model_state`, `read_model_state_at`,
  `read_model_repair_started_at`, `read_model_repair_completed_at`,
  `read_model_repair_failed_at`, `read_model_repair_attempts`, and
  `read_model_error` are persisted FTS/rollup repair evidence.
- Optional lifecycle/read-model timestamps serialize as `null`/omitted when
  unset. A zero internal sentinel is treated as unset, not epoch.
- Diagnostic strings are bounded and redacted before persistence and again
  before presentation: control characters are stripped, whitespace is collapsed,
  configured source locations and home-directory prefixes are replaced with
  neutral placeholders, and truncation preserves valid UTF-8.
- A source must not enter Tail unless the supervisor has durably recorded the
  `tailing` transition and initial heartbeat evidence. The stale-tail watchdog
  discovers monitorable sources from persisted lifecycle state; allowing Tail to
  run after a failed lifecycle write would make a live source invisible to
  health and restart monitoring until daemon restart.

`status` is `degraded` when:

- No sources are configured/discovered (`status_detail="no_sources_configured"`).
- Any enabled source is in `start_failed`, `construct_failed`, fatal
  `scan_failed`, `tail_stale`, `tail_failed`, or repeated/prolonged
  `tail_restarting`.
- Any enabled source is in `unknown`, `starting`, `scan_complete`, or
  `tail_starting` beyond the pre-tail grace window.
- Any enabled source remains `scanning` beyond the long-scan threshold.
- Any enabled source has `read_model_state` of `repair_pending`,
  `repair_timeout`, or `repair_failed` outside the accepted repair grace.
  `repair_pending` created by a committed Tail batch during a global read-model
  rebuild also sends a coalesced supervisor repair request in the same daemon
  run; degraded health is evidence of delayed or failed repair, not the normal
  retry trigger.
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
- Lifecycle state, Tail heartbeat/failure evidence, and read-model repair state.
  Any displayed legacy lag/last-seen value is secondary diagnostic text, not the
  health badge source.
- Last 100 parse error log lines (expandable).
- Toggle to enable/disable the source (Phase 2; needs writes).

## Operator Runbook

Lives at `docs/runbook.md` (created during Phase 1). Covers:

- "Ingester is lagging" → check inotify limits, disk space, adapter logs.
- "Parse errors spiking" → expand source detail in UI, inspect log lines, check source format upgrade.
- "Server returns 500 on /api/sessions" → check SQLite size, check WAL contention, restart server.
- "SSE events not arriving" → check `/api/health`, check the notify-table poller is advancing (`notify.last_seq` increasing as the ingester commits), check browser EventSource console.
