# REST API

## TL;DR

JSON over HTTP. All implemented endpoints return `application/json` except `/api/events` (text/event-stream); the Phase-2 `/api/payloads/:ref` would carry a variable content type once implemented, but in Phase 1 it is unregistered and returns the JSON `NOT_FOUND` envelope. Pagination, time-range filtering, and structured errors are consistent across endpoints.

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
  "schema_version": 5,
  "uptime_s": 12345,
  "db_path": "...",
  "db_size_bytes": 12345678,
  "sources": [
    { "id":"...", "format":"aiagent_v3", "location":"...", "enabled":true,
      "last_seen_at":<us>, "lag_us":<int>, "parse_errors":0, "last_seq":12345 }
  ],
  "notify": { "last_seq":67890, "lag_us":<int> },
  "sse": { "subscriptions":3 }
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
          "parent_op_id":null,
          "start_ts":<us>,"end_ts":<us>,"duration_us":...,
          "status":"...","error_class":null,
          "tokens_in":...,"tokens_out":...,"cost_usd":...,
          "ctx_used":...,"ctx_max":...,
          "child_session_id":null,
          "payload_refs":[
            { "id":1,"kind":"llm_request","format":"http","compression":"gzip",
              "original_bytes":1234,"stored_bytes":456 }
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

Each op row carries `parent_op_id` — the canonical id of the op it nests under
(`ops.parent_op_id`, set by the ingest writer), or `null` for a top-level op. The
Trace view rebuilds the authoritative span tree from this parentage; the key is
always present (nullable), never omitted.

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

Implemented by SOW-0006. Returns the layout-agnostic actor graph (nodes + edges)
for the D3 topology view; the client picks the layout (force / hierarchical), so
the server returns no coordinates. 404 `NOT_FOUND` for an unknown `:id`; a control
byte in `:id` is a `BAD_REQUEST` (same rule as `/api/sessions/:id`). HEAD returns
the status + JSON Content-Type with an empty body; a non-GET/HEAD method is
405 `METHOD_NOT_ALLOWED`.

**Scope = the whole session tree** (the session at `:id` resolved to its
`root_session_id`, plus every session sharing that root — root + all descendants).
This makes the per-session topology answer "which sub-agent burned the most cost",
not just "what did this one session do". The cross-session `/topology` page (no
`:id`) reuses the same node/edge model over the filtered set (Phase 3+).

```json
{
  "nodes": [
    { "id":"agent:<session_id>","kind":"agent","label":"nedi (root)","size_metric":3400000,"failure_ratio":0.0 },
    { "id":"tool:shell.Bash","kind":"tool","label":"shell.Bash","size_metric":300000,"failure_ratio":0.0 }
  ],
  "edges": [
    { "source":"agent:<session_id>","target":"tool:shell.Bash","calls":12,"total_us":3400000 }
  ],
  "max_size_metric": 3400000
}
```

- **Nodes.** One `agent` node per distinct session in the tree (`id` =
  `agent:<session.id>`; `label` = the session's `agent_name`, falling back to its
  `kind`, with `" (root)"` appended for the root session). One `tool` node per
  distinct `(tool_namespace, name)` among the tree's `kind='tool'` ops (`id` =
  `tool:<namespace>.<name>`, or `tool:<name>` when `tool_namespace` is NULL;
  `label` = the same dotted string). Agent-node identity is the **session id**, not
  the agent name, because two sub-agents can share an `agent_name` ("worker") yet
  be distinct actors with distinct cost — collapsing them on name would hide the
  per-sub-agent breakdown the view exists to show.
- **Edges.** Caller→callee with `calls` (op count) and `total_us`
  (`SUM(duration_us)`, NULL durations counted as 0). Two edge sources: (a) every
  `kind='tool'` op contributes an `agent:<its session_id>` → `tool:<ns>.<name>`
  edge; (b) every `kind='session'` op with `child_session_id` set contributes an
  `agent:<parent session_id>` → `agent:<child_session_id>` edge (the sub-agent
  spawn). Edges are aggregated, so repeated calls collapse to one edge with summed
  counts. An edge whose target session is outside the tree (should not happen given
  the shared root) is dropped defensively.
- **`?metric=`** selects what `size_metric` carries on **agent** nodes:
  `cost` (`SUM(cost_usd)`), `tokens` (`SUM(tokens_in+tokens_out)`), `duration`
  (`SUM(duration_us)` — **default**), `calls` (op count), `ctx_pct`
  (`MAX(ctx_used/ctx_max)` across the session's LLM ops where both are > 0, in
  `0..1`; `0` when never known). The metric is computed over the ops owned by that
  session (`ops.session_id`). **Tool** nodes always carry the tool's own
  `SUM(duration_us)` as `size_metric` regardless of `?metric=` (a tool has no
  tokens/ctx of its own; duration is the one metric every tool node can express
  consistently) — except under `?metric=calls`, where a tool node carries its op
  count, and `?metric=cost`/`tokens`, where it carries the tool ops' summed
  cost / tokens. An unknown `?metric=` value is a `BAD_REQUEST`.
- **`size_metric` is raw, not normalized.** The server returns the raw aggregate
  per node plus a top-level **`max_size_metric`** (the maximum `size_metric` across
  all nodes, `0` when there are no nodes). The client normalizes to its preferred
  radius range using `max_size_metric`. Returning raw + max (rather than a
  server-side 0..1 scale) keeps the contract honest for tooltips ("$0.42",
  "3.4 s") and lets the client choose linear/sqrt/log sizing without a second
  round-trip. (The earlier draft's `1.0`/`0.4` example implied normalization; this
  is the resolved contract — SOW-0006.)
- **`failure_ratio`** is `failed_ops / total_ops` for that node in `0..1`
  (`0` when the node has no ops). For an agent node it is over the session's ops;
  for a tool node it is over that tool's ops across the tree.

A session in the tree with no ops still appears as an agent node (`size_metric` 0,
`failure_ratio` 0) so the lineage stays visible. The two driving queries (one over
the tree's sessions, one over the tree's ops) are bounded by the tree size and use
`idx_sessions_root_start` / `idx_ops_session_start`.

### GET /api/topology

Implemented by SOW-0006 (chunk 6b). The **cross-session** topology: the same
node/edge model as `/api/sessions/:id/topology`, but the scope is the active
filter rather than one session tree. Drives the `/topology` page. GET (+ HEAD with
empty body) only; any other method is 405 `METHOD_NOT_ALLOWED`. An unknown
`?metric=` value is `BAD_REQUEST`. Response shape is identical to the per-session
topology (`nodes` / `edges` / `max_size_metric`).

**Scope = ALL sessions matching the filter (roots + sub_agents + forks).** The
cross-session topology spans every session kind by default — NOT just roots —
because its purpose is lineage: the parent→child and origin→fork edges need the
child/fork sessions present as nodes, or the graph collapses to disconnected
root dots. The endpoint accepts the same filter query params as `GET /api/sessions`
(time range `from`/`to`, `agents`, `models`, `tools`, `sources`, `status`, `q`,
etc. — whatever that endpoint already validates), with one deliberate exception:
the `group` param does NOT apply here. `/api/sessions` defaults `group=root` (the
list shows roots and expands children inline); the topology FORCES the
all-sessions scope (`handleCrossTopology` sets it after parsing) so an explicit
`?group=root` does not strip the child/fork endpoints. The node set is the
sessions matching all the other filters. (A roots-only topology, if ever wanted,
would be a separate explicit toggle, not the `/api/sessions` list default.)

- **Nodes.** One `agent` node per matching session (`id` = `agent:<session.id>`;
  `label` = `agent_name` falling back to `kind`, with `" (root)"` appended when the
  session is its own root). **No `tool` nodes** in the cross-session view — a
  filtered set can span thousands of sessions and their tools would explode the
  graph; tools live in the per-session view. (SOW-0006 AC#2: "cross-session view:
  sessions".)
- **Edges.** Session lineage among the matched nodes, aggregated: a session with
  `parent_session_id` set → `agent:<parent>` → `agent:<child>`. This single pass
  covers BOTH sub-agent spawns (`kind='sub_agent'`) AND forks (`kind='fork'`):
  there is no separate `forked_from_id` canonical column — the codex adapter
  resolves a source `forked_from_id` into `parent_session_id` + `kind='fork'` at
  ingest (`internal/adapters/codex/mapper_turn.go`), so one `parent_session_id`
  pass captures both. An edge whose other endpoint is NOT in the matched set is
  dropped defensively (lineage to a filtered-out session is not drawn). Lineage
  edges are structural, so `calls` = 1 and `total_us` = 0 (no call duration; the
  shape is reused for renderer parity).
- **`?metric=`** selects `size_metric` on each agent node, computed from the
  session's own stored aggregates (NOT a per-op rescan, to stay bounded over a
  large filtered set): `cost` = `cost_usd`; `tokens` = `tokens_in + tokens_out`;
  `duration` = `end_ts - start_ts` (**default**, 0 when either is unknown);
  `calls` = `op_count`. `ctx_pct` is best-effort: 0 unless a session-level context
  ratio is available (cross-session ctx_pct over ops is out of scope for 6b —
  documented, not silently wrong). `max_size_metric` is the max across nodes (0
  when empty), same client-normalization contract as the per-session route.
- **`failure_ratio`** = `failure_count / op_count` per session node (0 when the
  session has no ops).
- **Bound.** A filter can match very many sessions; the force graph and the perf
  budget cannot. The endpoint caps the node set at **`maxTopologyNodes` (default
  500)** — the top-N sessions by the selected `size_metric` — and sets a top-level
  **`"truncated": true`** when the cap was hit (lineage edges are then restricted to
  the retained nodes). The cap is NOT silent: the client surfaces a "showing top N
  of M" notice. (Aligns with `frontend-architecture.md`'s 500-node Canvas/Worker
  threshold.)

### GET /api/sessions/:id/timeline

Implemented by SOW-0006. Returns the per-lane span set for the Timeline view.
404 `NOT_FOUND` for an unknown `:id`; control-byte `:id` → `BAD_REQUEST`; HEAD and
405 behave exactly as the topology route above.

**Scope = the whole session tree** (root + all sessions sharing its
`root_session_id`), one lane per session, matching the Timeline's "root + children
stacked" model (`ui-pages.md`).

```json
{
  "lanes": [
    {
      "key":"session:<id>",
      "label":"nedi (root)",
      "spans":[
        { "id":"<op_id>","kind":"llm","name":"claude-opus-4-7","start_ts":<us>,"end_ts":<us>,"status":"completed" },
        { "id":"<op_id>","kind":"compaction","name":"auto","start_ts":<us>,"end_ts":<us>,"status":"completed" }
      ]
    }
  ],
  "t_start": <us>, "t_end": <us>
}
```

- **Lanes.** One per session in the tree. `key` = `session:<session.id>`; `label`
  = the session's `agent_name` (falling back to `kind`), with `" (root)"` appended
  for the root session. Lanes are ordered by the session's `start_ts` (then `id`),
  so the root sorts first when it starts first. A session with no ops still emits a
  lane with an empty `spans` array (the lineage stays visible).
- **Spans.** Every op of that session, ordered by `start_ts` then `seq` then `id`.
  Each span carries `id` (op id), `kind`, `name`, `start_ts`, `end_ts`, `status`.
  **Compaction is not a separate array**: `kind='compaction'` ops are emitted as
  ordinary spans with `kind:"compaction"`, and the frontend keys on `kind` to draw
  the full-height vertical breakpoint (`ui-pages.md`). A still-running op (no
  `end_ts`) emits `"end_ts": null`; a **point event** emits `"end_ts"` equal to
  its `start_ts` (the server returns the stored `end_ts` as-is — it does not
  null a recorded point). The client draws a null OR a `<= start_ts` end as an
  **instant marker** — a point/tick at `start_ts`, NOT a bar stretched to the
  viewport edge — matching the source-aware Trace/Timeline rule (`ui-pages.md`: a
  source that records no duration must not imply one). The
  server does not synthesize an end, so the raw fact "not finished / point event"
  is preserved.
- **`t_start` / `t_end`.** Minimum `start_ts` and maximum `end_ts` across all spans
  in all lanes. A span with a null `end_ts` contributes its `start_ts` to the
  `t_end` computation (so the window always covers every span's known extent).
  When the tree has no ops at all, both are `0`.

The single op query is bounded by the tree size and uses `idx_ops_session_start`.

### GET /api/stats

Cross-session aggregates over the filtered set.

```json
{
  "totals": {
    "session_count": ..., "turn_count": ..., "op_count": ...,
    "tokens_in": ..., "tokens_out": ..., "cost_usd": ...,
    "tokens_cache_read": ..., "tokens_cache_write": ...,
    "failures": ..., "duration_us": ...
  },
  "by_model":    [ { "name":"...","provider":"...","calls":...,"tokens_in":...,"tokens_out":...,"cost_usd":...,"failures":...,"pct_of_cost":0.42 } ],
  "by_tool":     [ { "namespace":"...","name":"...","calls":...,"failures":...,"total_us":...,"pct_of_calls":0.12 } ],
  "by_agent":    [ { "name":"...","sessions":...,"failures":...,"tokens_in":...,"tokens_out":...,"cost_usd":...,"pct_of_sessions":0.20 } ],
  "by_status":   [ { "status":"completed","count":... }, { "status":"failed","count":... } ],
  "by_source":   [ { "source":"...","sessions":...,"failures":... } ]
}
```

`/api/stats` is **NOT** rollup-backed and is unchanged by SOW-0007: it continues to
compute its breakdowns live over `sessions`/`ops` for the filtered session set.
This is deliberate — `/api/stats` is a *filtered, non-time-bucketed* summary
(`by_status` has no rollup dimension, and `by_agent`/`by_model`/`by_source` are
scoped to the filtered session set), none of which maps onto the time-bucketed,
per-single-dimension, all-sessions rollups. The rollup-backed analytics surfaces
delivered by SOW-0007 are the dedicated endpoints below — `/api/stats/aggregate`
and `/api/stats/top`. (An earlier SOW-0007 draft proposed transparently
rollup-backing `/api/stats`; that was dropped as ill-fitting — the summary's shape
and filter semantics differ fundamentally from the rollups.)

### GET /api/stats/aggregate

A time-series aggregate for the statistics dashboard's line charts. Reads the
materialized rollups for closed buckets and `UNION ALL`s a live aggregate over
`ops` for the open bucket (`data-model.md` §Rollup tables — open-hour rule). Reuses
the **same filter params as `GET /api/sessions`** via `parseSessionFilter` (time
range `from`/`to` on `start_ts`, plus `agents`, `models`, `tools`, `sources`,
`status`, `q`), all bound via `?` placeholders. HEAD, 405 `METHOD_NOT_ALLOWED` on a
non-GET/HEAD method, control-byte rejection on path/query, and `BAD_REQUEST` on an
unknown enum value behave exactly as the other GET routes.

**Rollup fast path vs. live fold (correctness contract).** The long-form rollups
are keyed by a SINGLE dimension per row (`data-model.md` §Rollup tables — they
deliberately avoid the `model×provider×tool×agent×cwd` cross-product), so they can
natively answer only a time-range (`from`/`to`) filter alongside the requested
`group_by`/`dimension`. ANY dimension filter — including `sources`, which binds to
`sessions.source_id` (finer than the rollups' `source_format` key, so a
`source_format`-level filter would over-count sibling sources) — forces the live
fold. Therefore:
- **Rollup fast path** — taken when the ONLY filters present are `from`/`to` (no
  dimension filter and no `sources`): closed buckets are summed from
  `rollup_hourly`/`rollup_daily` (`WHERE dimension=? AND bucket_ts ∈ [from,
  openBucketStart)`), and the still-open bucket(s) are folded live.
- **Live fold** — taken when ANY of `agents`, `models`, `tools`, `status`, `q`,
  **or `sources`** is present: the WHOLE `[from, to)` is aggregated live from `ops`
  (joined to `sessions`), applying every filter. `sources` forces the live fold
  because it binds to `sessions.source_id` (e.g. `codex:/loc`), which is STRICTLY
  FINER than the rollups' `source_format` key (e.g. `codex`) — one `source_format`
  can cover many `source_id`s (`data-model.md` §Rollup tables), so a
  `source_format`-level filter would over-count sibling sources. Correct, just not
  rollup-accelerated.

The live fold's TIME bound is `op.start_ts ∈ [from, to)` (the bucket key), NOT
`session.start_ts`: the `sessions` join supplies only the agent/cwd dimension and
the session-level dimension filters (`agents`/`status`/`sources`/`group`), so an
op whose session STARTED before `from` is still counted iff the op itself falls in
the window — keeping the live fold byte-equal to the op-bucketed rollup (the AC#2
invariant). The sole exception is `metric=sessions` (`session_starts`), which is
attributed by `session.start_ts` because it counts session starts, so its live
fold loads the window's session starts (by `s.start_ts`) and folds them through
`rollups.Rollup`'s `starts` input — matching how the rollup materializes the
`session_starts` column.

**Bucket-window semantics.** A bucket is included iff `from <= bucket_ts < to`
(selection by the bucket's START); within an included bucket the whole bucket's
data is returned (the rollups cannot sub-select inside a bucket). A `from`/`to`
that falls mid-bucket therefore excludes that partial lower bucket — this is what
keeps the fast path and the live fold byte-identical. The default-`to` window
bound and the open-bucket cutoff (`openStart`) are computed from a SINGLE
wall-clock read per request, so a clock that crosses an hour/day boundary
mid-request cannot desync them and drop the just-open bucket's live fold
(`closedHi = min(to, openStart)`).

Both paths and the open-bucket fold compute their numbers through the SAME pure
`internal/rollups` fold (the server reads the relevant `ops` into `rollups.OpRow`s
and calls `Rollup(...)`), so an open bucket and a live-folded range are
byte-consistent with the materialized closed buckets by construction — the same
property the backfill-vs-incremental diff gate guarantees for the ingester.
`group_by=source_format` reads the `dimension='total'` rows keyed by
`source_format`; `group_by=total` returns the single `dimension='total'`,
`dimension_value=''` series.

```
?from=<us>&to=<us>
&bucket=daily            'hourly'|'daily' (default 'daily')
&group_by=total          'model'|'provider'|'tool'|'agent'|'cwd'|'source_format'|'total' (default 'total')
&metric=cost             'cost'|'tokens_in'|'tokens_out'|'calls'|'failures'|'duration_us'|'sessions' (default 'cost'); 'calls' maps to op_count, 'sessions' to the additive session_starts column (meaningful for group_by total|agent|cwd; 0 for model|provider|tool, exactly as the rollup stores it — drives the per-day sessions trend, ui-pages.md)
&agents=...&models=...&tools=...&sources=...&status=...&q=...   same as /api/sessions
```

Response — one entry per time bucket, each carrying the per-`group_by`-value series
for that bucket:

```json
{
  "buckets": [
    {
      "bucket_ts": <us>,
      "series": [
        { "key": "<dimension_value>", "value": <number> }
      ]
    }
  ],
  "bucket": "daily",
  "metric": "cost"
}
```

- `bucket_ts` is the UTC bucket start in microseconds (hour or day, per `bucket`).
- `key` is the `dimension_value` for the row (a model name, provider, `"<ns>.<name>"`
  tool id, agent name, cwd, or source format). `group_by=total` returns a single
  series entry keyed `""`.
- `value` is the selected `metric` summed over that `(bucket, key)`.
- An unknown `bucket`, `group_by`, or `metric` value is a `BAD_REQUEST`.

### GET /api/stats/top

Top-N ranking for the dashboard's horizontal bar charts: the highest-`metric`
dimension values over the window. Computed by summing the dimension's rollup rows
over `[from, to)` (plus the live open hour) and `ORDER BY value DESC LIMIT n`.
Same filter params and HEAD/405/bad-param behavior as `/api/stats/aggregate`.

```
?from=<us>&to=<us>
&dimension=model         'model'|'provider'|'tool'|'agent'|'cwd'
&metric=cost             same enum as /api/stats/aggregate (default 'cost'; 'calls' → op_count)
&n=20                    default 20, max 200
&agents=...&models=...&tools=...&sources=...&status=...&q=...   same as /api/sessions
```

Response — items ordered by `value` descending, top-N:

```json
{
  "dimension": "model",
  "metric": "cost",
  "items": [
    { "key": "<value>", "value": <number> }
  ]
}
```

An unknown `dimension` or `metric` value is a `BAD_REQUEST`; `n` is clamped to the
`[1, 200]` range.

### GET /api/search

Deep full-text search across ops and logs, backed by the FTS5 tables (`fts_ops`,
`fts_logs`; `data-model.md` §Full-text search). Drives the `/stats` deep-search box.
Reuses the standard filter params (`from`/`to`, `agents`, `models`, `tools`,
`sources`, `status`) via `parseSessionFilter`. HEAD, 405, and control-byte
rejection behave as the other GET routes.

```
?q=<text>                required; non-empty after trim; control chars rejected (BAD_REQUEST)
&from=<us>&to=<us>&agents=...&models=...&tools=...&sources=...&status=...   same as /api/sessions
&limit=50                default 50, max 200
&cursor=<opaque>         pagination (offset-style; mirrors /api/sessions/:id/logs)
```

Response — matched ops and logs, ranked by BM25 with `snippet()` excerpts:

```json
{
  "ops": [
    { "op_id":"...","session_id":"...","kind":"tool","name":"...","model":"...","snippet":"…matched…","rank":<number> }
  ],
  "logs": [
    { "log_id":123,"session_id":"...","op_id":"...","severity":"ERR","ts":<us>,"snippet":"…matched…","rank":<number> }
  ],
  "logs_indexed": true
}
```

- `q` is passed as the FTS5 `MATCH` argument, **always parameterized** (bound `?`):
  FTS5 query syntax (`AND`/`OR`/`NEAR`/prefix `*`/phrase `"…"`) is intentionally
  exposed to the operator, but because the `MATCH` value is a bound parameter there
  is no SQL-injection surface — the query string never reaches the SQL text.
- `rank` is the BM25 score; `ops` and `logs` are each ordered by `rank` (best
  first). `snippet()` returns the matched excerpt for display.
- `op_id`/`session_id` (and `log_id`/`session_id`/`op_id` for logs) are the linkage
  the UI uses to navigate to the session/op.
- **`logs_indexed`** reflects the per-source `fts5_index_logs` flag
  (`data-model.md`). When log indexing is disabled the `logs` array is empty and
  `"logs_indexed": false` is set, so the client can distinguish "no log matches"
  from "logs not indexed on this install". Log hits are additionally restricted at
  query time to sources with `fts5_index_logs=1`, so a disabled source's logs never
  appear in search even if previously-indexed `fts_logs` rows remain until a
  `rollups-backfill` rebuild.
- An empty/whitespace-only `q` is a `BAD_REQUEST`.

### GET /api/catalog/{tools,models,agents}

**Phase 2 — not implemented in Phase 1.** The catalog tables are populated by the
ingester but no handler serves them yet; returns a structured `NOT_FOUND` today.

Catalog table contents with filters and sorting.

### GET /api/payloads/:ref

**Phase 2 — not implemented in Phase 1.** The route is not registered (returns a
structured `NOT_FOUND`), and Phase 1 deliberately does **not** emit a `url` on
`payload_refs` (a viewer must not advertise a route it does not serve). The
`payload_refs` entries still carry `id`/`kind`/`format`/`compression`/bytes so a
later phase can add the streaming route + link. The shape below is the planned
contract.

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
`BAD_REQUEST`). The scalar `session_id` / `root_session_id` are **trimmed** and
normalized; a present-but-whitespace-only value → `BAD_REQUEST` (consistent with
the array dimensions and the other ID paths, which all trim). A bad filter
returns `400`. Response `200`:

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
