# REST API

## TL;DR

JSON over HTTP. All endpoints return `application/json` except `/api/payloads/:ref` (variable content type) and `/api/events` (text/event-stream). Pagination, time-range filtering, and structured errors are consistent across endpoints.

## Conventions

- **Time params**: `?from=<us>&to=<us>` UNIX microseconds UTC. `to` omitted = now.
- **Pagination**: `?limit=<n>&cursor=<opaque>`. Responses include `"next_cursor"` when more rows exist. Default limit 100; max 1000. The `cursor` is opaque and is bound to a fingerprint of the **entire result-defining query** it was issued under (all filters, group, time window, sort/order, search; for logs the session id + severity set), not just `sort`/`order`. Replaying a cursor against a changed query (e.g. minting on `?group=root` then replaying with `?group=all&cursor=...`, or changing any of `from`/`to`/`agents`/`models`/`tools`/`status`/`sources`/`q`, or `severity` on logs), or a cursor that is malformed, truncated, partial, or carries unknown fields, returns `BAD_REQUEST`. Re-ordering the same filter set (`?models=a,b` vs `?models=b,a`) is accepted — the fingerprint is order-insensitive. `limit` may change between pages. An absent or empty `cursor` means "first page".
- **Filter array params**: `?agents=a&agents=b` (repeated) or `?agents=a,b` (comma-separated). Server accepts both. A present array key whose every element is empty (`?agents=` or `?agents=,`) returns `BAD_REQUEST`; an absent key is no constraint. The same present-but-empty rule applies to the logs `?severity=` param: `?severity=` or `?severity=,` is a `BAD_REQUEST`, while an absent `severity` key means "all severities". A user-supplied value (any array element, `q`, or the path `:id`) containing an ASCII control character (byte `< 0x20`) returns `BAD_REQUEST` — legitimate names, search text, and ids never carry control bytes. The check runs on the raw value before any whitespace trim, so a leading/trailing control byte cannot be silently trimmed away and accepted.
- **Errors**:
  ```json
  { "error": { "code": "BAD_REQUEST", "message": "...", "details": { ... } } }
  ```
  HTTP status reflects the class (400/404/409/500/504).

## Endpoints

### GET /api/health

```json
{
  "status": "ok" | "degraded" | "down",
  "version": "<git sha>",
  "schema_version": 3,
  "uptime_s": 12345,
  "db_path": "...",
  "db_size_bytes": 12345678,
  "sources": [
    { "id":"...", "format":"aiagent_v3", "location":"...", "enabled":true,
      "last_seen_at":<us>, "lag_us":<int>, "parse_errors":0, "last_seq":12345 }
  ]
}
```

The status union and the per-source fields here MUST match
observability.md §`/api/health`; that spec is the canonical reference
for the contract (degraded/down rules, `last_seq` semantics, etc.).
This REST spec only documents the wire shape so client authors do not
have to cross-read the observability spec to know which fields exist.

`last_seq` is a per-source observability counter (the max `SourceSeq`
seen for that source) — see observability.md §`/api/health` for the
per-adapter semantics. It is NOT a dedup gate and NOT a portable event
count; the field was renamed from the original `events_ingested_total`
in iteration 2 of SOW-0001 Chunk 11. The
`db_size_bytes` and per-source `location` fields landed in iteration 5
of the same chunk as a spec ↔ code parity fix once codex flagged they
were emitted by the binary but absent from this spec.

### GET /api/sources

Full source list with cursor metadata. Used by the Sources admin panel.
Each item carries the per-source `last_seq` (opaque adapter
observability counter = max SourceSeq seen; NOT a dedup gate and NOT a
portable event count — identical semantics to
`/api/health.sources[].last_seq`),
the persisted `cursor`, and the `updated_at` timestamp of the last
writer commit. HEAD is supported on both `/api/health` and
`/api/sources` and returns the same status + headers with an empty
body, per RFC 9110 §9.3.2.

### GET /api/sessions

```
?from=<us>&to=<us>
&agents=a,b           filter
&models=m,n           filter
&tools=t1,t2          filter (sessions where any op uses these tools)
&status=running,failed
&sources=src1,src2
&q=<text>             search in agent name (and future: notes)
&group=root           "root" returns only root sessions (default); "all" returns all
&sort=start_ts        default; only sort supported in v1
&order=desc           default; "asc" also supported
&limit=100&cursor=...
```

Response:

```json
{
  "items": [
    {
      "id":"...","native_id":"...","root_session_id":"...","parent_session_id":null,
      "source_id":"...","kind":"root","agent_name":"nedi","model":"claude-opus-4-7",
      "status":"completed","start_ts":<us>,"end_ts":<us>,
      "tokens_in":1234,"tokens_out":5678,"cost_usd":0.42,
      "turn_count":7,"op_count":42,"failure_count":0,
      "child_session_count":3
    }
  ],
  "next_cursor": "..."
}
```

When `group=root`, each item includes `child_session_count`; the UI uses this to render the expander.

### GET /api/sessions/:id

```json
{
  "session": { ...full row plus computed children list... },
  "turns": [
    {
      "id":"...","seq":1,"start_ts":<us>,"end_ts":<us>,"status":"completed",
      "tokens_in":...,"tokens_out":...,"cost_usd":...,"op_count":...,
      "ops": [
        { "id":"...","kind":"llm","name":"...","model":"...","provider":"...",
          "start_ts":<us>,"end_ts":<us>,"duration_us":...,
          "status":"...","error_class":null,
          "tokens_in":...,"tokens_out":...,"cost_usd":...,
          "ctx_used":...,"ctx_max":...,
          "child_session_id":null,
          "payload_refs":[
            { "id":1,"kind":"llm_request","format":"http","compression":"gzip",
              "url":"/api/payloads/1","original_bytes":1234,"stored_bytes":456 }
          ]
        }
      ]
    }
  ],
  "child_sessions": [
    { ...summary fields per child session... }
  ]
}
```

### GET /api/sessions/:id/logs

```
?limit=...&cursor=...&severity=WRN,ERR
```

```json
{
  "items": [
    { "ts":<us>,"severity":"WRN","source":"aiagent_v3","op_id":"...","message":"...","extras":{...} }
  ],
  "next_cursor":"..."
}
```

### GET /api/sessions/:id/topology

Returns nodes and edges for the D3 force-directed view.

```json
{
  "nodes": [
    { "id":"agent:nedi","kind":"agent","label":"nedi","size_metric":1.0,"failure_ratio":0.0 },
    { "id":"tool:mcp__slack__send_message","kind":"tool","label":"slack.send_message","size_metric":0.4 }
  ],
  "edges": [
    { "source":"agent:nedi","target":"tool:mcp__slack__send_message","calls":12,"total_us":3400000 }
  ]
}
```

Size metric selection: `?metric=cost|tokens|duration|calls|ctx_pct` (default `duration`).

### GET /api/sessions/:id/timeline

Returns ordered spans for the timeline view.

```json
{
  "lanes": [
    {
      "key":"session:<id>",
      "label":"nedi (root)",
      "spans":[
        { "id":"<op_id>","kind":"llm","name":"claude-opus-4-7","start_ts":<us>,"end_ts":<us>,"status":"completed" }
      ]
    }
  ],
  "t_start": <us>, "t_end": <us>
}
```

One lane per session (root and children).

### GET /api/stats

Cross-session aggregates over the filtered set.

```json
{
  "totals": {
    "session_count": ..., "turn_count": ..., "op_count": ...,
    "tokens_in": ..., "tokens_out": ..., "cost_usd": ...,
    "failures": ..., "duration_us": ...
  },
  "by_model":    [ { "name":"...","provider":"...","calls":...,"tokens_in":...,"tokens_out":...,"cost_usd":...,"failures":...,"pct_of_cost":0.42 } ],
  "by_tool":     [ { "namespace":"...","name":"...","calls":...,"failures":...,"total_us":...,"pct_of_calls":0.12 } ],
  "by_agent":    [ { "name":"...","sessions":...,"failures":...,"tokens_in":...,"tokens_out":...,"cost_usd":...,"pct_of_sessions":0.20 } ],
  "by_status":   [ { "status":"completed","count":... }, { "status":"failed","count":... } ],
  "by_source":   [ { "source":"...","sessions":...,"failures":... } ]
}
```

### GET /api/catalog/{tools,models,agents}

Catalog table contents with filters and sorting.

### GET /api/payloads/:ref

Streams the payload bytes. Headers:

- `Content-Type` reflects the payload format (`application/http`, `text/event-stream`, `application/json`, `application/json-rpc`, `text/plain`).
- `Content-Encoding: gzip` set when the underlying file is gzip and the client accepts gzip.
- `Cache-Control: public, max-age=86400` (payload files are append-only and immutable once written).

Query: `?decompress=1` forces inline decompression for clients that can't handle gzip.

### POST /api/subscriptions

Creates an SSE subscription. Request body:

```json
{ "filter": { ...REST-style filter... } }
```

The `filter` is validated and normalized with the **same rules as the list
endpoints** (`time_range`, `sources`, `agents`, `models`, `tools`, `status`,
`session_id`, `root_session_id`; unknown fields rejected; present-but-empty
array → `BAD_REQUEST`; ASCII control char `< 0x20` in any value →
`BAD_REQUEST`). A bad filter returns `400`. Response `200`:

```json
{ "id": "sub-<32 hex>", "filter_normalized": { ... } }
```

`id` is `sub-` followed by 32 lowercase hex characters (128-bit crypto-random).
If the cryptographic RNG fails (effectively never on Linux) the server returns
`500 INTERNAL_ERROR` rather than a weak/non-spec id — it never hands out a
predictable id. While the server is shutting down (SSE hub closed) new
subscription creation returns `503 SERVICE_UNAVAILABLE` rather than a
subscription that would not receive events. The code is `SERVICE_UNAVAILABLE`
(not `DB_UNAVAILABLE`): the database is fine; the server is unable to serve the
request and the client should retry later.

The shutting-down check and the subscription creation (hub registration plus
registry insert) execute as **one critical section** under the presenter's SSE
lifecycle mutex, so they cannot interleave with `ShutdownSSE`. This closes a
time-of-check/time-of-use gap: an atomic flag checked once and then read again
across the create call would still permit shutdown to run in between, leaving
either a `200` whose subscription the closed hub already dropped (never
attaches, never receives events) or an orphan registry entry with no hub
channel. With the mutex the outcome is binary: a create either completes fully
(subscription live in both the hub and the registry) before shutdown is
observed, or it sees the shutting-down state and returns `503` having mutated
nothing. See `presenter.md` §Graceful Shutdown for the lock-ordering contract.

### DELETE /api/subscriptions/:id

Cancels a subscription. Returns `204 No Content`. **Idempotent** — deleting an
unknown or already-expired `id` is still `204`.

### GET /api/events?sub=:id

Opens the SSE stream for a subscription. On success returns `200` with
`Content-Type: text/event-stream`. A missing or malformed `sub` returns `400`;
an unknown or expired `sub` returns `404`; a second concurrent stream for a
subscription that already has an active stream returns `409` (one stream per
subscription — see `sse-protocol.md`). `HEAD` returns the same headers with an
empty body (`200` if the subscription exists, `404` if not) without opening a
stream or touching the subscription lifecycle. Gzip is **not** applied to
`/api/events` (the stream is sent uncompressed so events flush immediately).

See `sse-protocol.md` for the subscription lifecycle, filter shape, event-frame
format, the five event types, `Last-Event-ID` replay, and backpressure.
