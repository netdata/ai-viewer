# REST API

## TL;DR

JSON over HTTP. All endpoints return `application/json` except `/api/payloads/:ref` (variable content type) and `/api/events` (text/event-stream). Pagination, time-range filtering, and structured errors are consistent across endpoints.

## Conventions

- **Time params**: `?from=<us>&to=<us>` UNIX microseconds UTC. `to` omitted = now.
- **Pagination**: `?limit=<n>&cursor=<opaque>`. Responses include `"next_cursor"` when more rows exist. Default limit 100; max 1000.
- **Filter array params**: `?agents=a&agents=b` (repeated) or `?agents=a,b` (comma-separated). Server accepts both.
- **Errors**:
  ```json
  { "error": { "code": "BAD_REQUEST", "message": "...", "details": { ... } } }
  ```
  HTTP status reflects the class (400/404/409/500/504).

## Endpoints

### GET /api/health

```json
{
  "status": "ok" | "degraded",
  "version": "<git sha>",
  "db_path": "...",
  "schema_version": 1,
  "uptime_s": 12345,
  "sources": [
    { "id":"...", "format":"aiagent_v3", "enabled":true, "parse_errors":0, "last_seen_at":<us>, "lag_us":<int> }
  ]
}
```

### GET /api/sources

Full source list with cursor metadata. Used by the Sources admin panel.

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

### POST /api/subscriptions / DELETE /api/subscriptions/:id / GET /api/events

See `sse-protocol.md`.
