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
- All queries are parameterized (every operator-supplied value is bound via
  a `?` placeholder; no string interpolation of user input). Filter and list
  queries are assembled dynamically per request (variable `IN (...)` arity
  plus optional predicates create many query shapes) and executed directly
  via `QueryContext`/`QueryRowContext`, NOT held in a prepared-statement
  cache; fixed-shape queries may be prepared in a future optimization SOW.
- Long-running queries are killed at 30 s; the user gets a 504 with a useful error.

### Filters, pagination, and cursors (Chunk 12)

The list-shaped endpoints (`/api/sessions`, `/api/sessions/:id/logs`)
and `/api/stats` share one filter parser (`internal/presenter/filters.go`)
that turns the `rest-api.md` §Conventions query params into a typed
filter plus a parameterized SQL `WHERE` fragment. The parser binds every
operator-supplied value as a `?` placeholder — never string-interpolated —
so the surface has no SQL-injection vector. Invalid combinations are
rejected with `BAD_REQUEST`: `from > to`, `limit > 1000`, unknown
`sort`/`order`/`group`, and an array filter whose key is present in the
query but whose every element is empty (e.g. `?models=` or `?models=,`).
A present array filter that yields at least one non-empty value is valid
even when it also contains empty segments (`?models=a,` keeps `a`); an
absent key is simply "no constraint on that dimension". The logs
`?severity=` param follows the SAME present-but-empty rule
(`parseSeverities` reuses the shared array parser): present-but-empty is a
`BAD_REQUEST`, an absent key means "all severities". A filter value (any
array element, or `q`) carrying an ASCII control character (byte `< 0x20`)
is also a `BAD_REQUEST` (`rejectControlChars`) — defense in depth, since
legitimate agent/model/tool/status/source names and search text never
contain control bytes. The control-char check runs on the **raw**
query value **before** any `TrimSpace`, so a leading/trailing control byte
(e.g. `?q=\tabc`, `?models=\ta`, `?severity=\tERROR`) is rejected rather
than silently trimmed away and accepted (`\t`, `\n`, `\r` are whitespace,
so a trim-first order would erase them). `q` is checked at its parse site;
array dimensions (including logs `severity`) are checked inside the shared
`parseRequiredNonEmptyArray` before splitting/trimming; and the path `:id`
of `/api/sessions/:id` and `/api/sessions/:id/logs` is checked on the raw
`PathValue` before trim — so every user-supplied value on the surface
(query and path) is covered by one rule. A control byte in the path `:id`
is `BAD_REQUEST`, not a `NOT_FOUND` from a doomed lookup.

Pagination is **keyset (seek), not offset**. The opaque `cursor`
(`internal/presenter/cursor.go`) is a base64url-encoded JSON object that
carries the last row's sort key plus its id — `(start_ts, id)` for
sessions, `(ts, id)` for logs — the `sort` + `order` the cursor was minted
under, AND a `fp` fingerprint of the full result-defining query (see
below). The next page selects rows strictly after that tuple in the active
sort direction, so deep pages stay O(log n). The keyset guarantee under
concurrent writes is precise: rows at or behind the cursor in the traversed
order are never skipped or repeated when other rows are inserted or deleted
between page fetches — unlike OFFSET pagination, which shifts and so
double-shows or skips already-traversed rows. It does NOT mean an in-progress
traversal back-fills rows inserted AHEAD of the current position: a row that
sorts above the cursor (e.g. a newer session under the default DESC order)
simply is not injected mid-traversal; the client sees it on a fresh query
from the first page. `next_cursor` is present only when another page exists (the
query fetches `limit+1` rows and emits the cursor of the `limit`-th row
when the extra row is seen).

A cursor is **bound to a fingerprint of the entire result-defining query,
not just `(sort, order)`.** The keyset `(ts, id)` watermark is only
meaningful against the exact result set the cursor was minted on; replaying
it against a different set (a changed filter, group, time window, search,
or — for logs — a changed severity) would silently skip or duplicate rows.
The cursor therefore carries `fp`, the **canonical length-prefixed string**
of the result-defining query itself — NOT a hash of it. A single helper
builds this string both when **minting** `next_cursor` and when
**validating** an incoming cursor, so the two can never drift, and the
validator compares the two strings **byte-for-byte**. The string is built
with **length-prefixed** encoding (each token written as `<byte-len>:<value>`,
each array dimension also prefixed by its element count), NOT
separator-joining: every token is self-delimiting, so no value content can
forge a field/element boundary. Because the cursor stores and compares the
full canonical string rather than a fixed-width digest, two distinct filter
sets can **never** collide — the property is exact by construction, not a
probabilistic bound. (Earlier iterations hashed the string with FNV-64a →
hex; codex iter-4 correctly noted a 64-bit digest is finite and can collide,
so the cursor now carries the string directly. The cursor is an opaque
localhost token that only echoes back filter values the client already sent,
so its size is immaterial.) What is encoded:

- Sessions (`sessionFilter.fingerprint()`): encodes `group`, `from`, the
  operator-**supplied** `to` (the pre-default value — NOT the `now`-default
  applied when `?to` is omitted, otherwise every page would mint against a
  fresh `now` and reject the next page), `sort`, `order`, `q`, and each
  array filter (`agents`, `models`, `tools`, `status`, `sources`)
  **sorted** so `?models=a,b` and `?models=b&models=a` produce the SAME
  fingerprint. `limit` (page size may legitimately change between pages)
  and `cursor` itself are excluded.
- Logs (`logFilter.fingerprint()`): encodes the path `:id`, the **sorted**
  `severity` set, and the fixed `(ts, asc)` ordering.

On replay the server recomputes the fingerprint from the LIVE request's
normalized filters and compares it to the cursor's. A mismatch is
`BAD_REQUEST` ("cursor does not match the current query filters; restart
pagination"). The fingerprint includes `sort`+`order`, but BOTH endpoints
ALSO perform an explicit `sort`/`order` check before the fingerprint
comparison, so a forged/tampered cursor whose explicit `sort`/`order` fields
differ from the active ordering is rejected with a precise ordering-mismatch
message rather than silently relying on the fingerprint: the sessions
endpoint requires `cur.sort == f.sort && cur.order == f.order` (the live
request's normalized ordering); the logs endpoint requires the fixed
`(ts, asc)` (`cur.sort == logsSort && cur.order == logsOrder`), which also
rejects a foreign cursor minted by the sessions endpoint. The keyset SQL
comparison **direction** (`>` for asc, `<` for desc) is driven by the
**live** request's `order` (`f.order`), not by the cursor's field — the
cursor's `sort`/`order` are a validated guard, not the direction source, so a
cursor that passed the explicit check necessarily agrees with `f.order`
anyway.

A cursor is rejected with `BAD_REQUEST` when it is structurally invalid:
not valid base64url, not a valid JSON object, carries trailing bytes after
the object, carries an unknown field (so an older binary cannot silently
reinterpret a future format), or is missing any required field — a
non-empty supplied cursor MUST carry a non-zero `ts`, a non-empty `id`,
and a `sort`/`order` that matches the active ordering (the sessions
endpoint's `parseCursorParam` rejects `cur.sort != f.sort || cur.order !=
f.order` with `BAD_REQUEST` before the fingerprint comparison — the explicit
guard described above, mirroring the logs endpoint's fixed-ordering check —
so a non-empty `sort`/`order` is not enough; it must agree with the live
request). For the **logs** endpoint the keyset `id`
is the `log_entries.id` INTEGER row id, so a logs cursor whose `id` is not a
decimal `int64` is `BAD_REQUEST` (validated in `parseLogPaging` after the
fingerprint check) and the validated id is bound to the keyset comparison as
an integer, not as a string coerced by SQLite affinity; the sessions
endpoint keeps a TEXT session `id`. The `fp` field is a known wire field but
is validated semantically by the filter (the fingerprint comparison above),
not as a structural decode requirement: a tampered or absent `fp` simply
fails the comparison against the live (always non-empty) query fingerprint,
which is also `BAD_REQUEST`. An **absent or empty** `cursor` param is not
malformed: it means "first page" (no keyset narrowing).

The query layer wraps every statement in a `context.WithTimeout` of 30 s
(`queryTimeout`); a deadline-exceeded error maps to HTTP 504 with a
`TIMEOUT` envelope so a runaway scan cannot hang a request. A SQLite
error that is not a deadline maps to HTTP 503 `DB_UNAVAILABLE`, matching
the existing `/api/sources` failure path.

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
GET  /                          → frontend index.html                                       (live)
GET  /assets/*                  → embedded frontend assets                                  (live)
GET  /api/health                → JSON health status                                        (live)
GET  /api/sources               → list sources, ingest cursors, parse error counts          (live)
GET  /api/sessions              → list sessions with filters and pagination                 (live)
GET  /api/sessions/:id          → session detail with turns and ops                         (live)
GET  /api/sessions/:id/logs     → log entries for a session                                 (live)
GET  /api/sessions/:id/topology → topology graph for a session (nodes, edges)               (Chunks 14+ — not yet implemented)
GET  /api/sessions/:id/timeline → ordered spans for the timeline view                       (Chunks 14+ — not yet implemented)
GET  /api/stats                 → cross-session statistics with filters                     (live)
GET  /api/catalog/tools         → catalog_tools, with filters                               (Chunks 12+ — not yet implemented)
GET  /api/catalog/models        → catalog_models, with filters                              (Chunks 12+ — not yet implemented)
GET  /api/catalog/agents        → catalog_agents, with filters                              (Chunks 12+ — not yet implemented)
GET  /api/payloads/:ref         → streams payload bytes (gz-decompressed inline if asked)   (Chunks 12+ — not yet implemented)
POST /api/subscriptions         → create an SSE subscription with a filter                  (Chunk 13 — not yet implemented)
DELETE /api/subscriptions/:id   → cancel                                                    (Chunk 13 — not yet implemented)
GET  /api/events?sub=:id        → SSE event stream                                          (Chunk 13 — not yet implemented)
```

Chunk 11 of SOW-0001 shipped the first four "live" routes (`/`,
`/assets/...`, `/api/health`, `/api/sources`); Chunk 12 added
`/api/sessions`, `/api/sessions/:id`, `/api/sessions/:id/logs`, and
`/api/stats`. The catch-all `/api/*` handler returns a structured
`NOT_FOUND` envelope naming the chunk in which a still-missing route
(topology, timeline, catalog, payloads, subscriptions, events) will
land, so future operators reading the response see the implementation
roadmap at the error site. Anything not in the live set MUST NOT be
added to a client (UI, curl scripts) until the corresponding chunk
merges.

Path-parameter routing uses Go 1.22+ `http.ServeMux` wildcard patterns
(`/api/sessions/{id}`, `/api/sessions/{id}/logs`); the `{id}` segment is
read with `r.PathValue("id")`. The patterns are registered WITHOUT a
method verb so every handler keeps the same in-handler method-gating
style as `/api/health` and `/api/sources` (one routing style across the
surface). More-specific wildcard patterns take precedence over the
`/api/` subtree catch-all, so `/api/sessions/{id}/topology` still falls
through to the `NOT_FOUND` handler until Chunk 14 lands it.

Every route that supports `GET` also supports `HEAD` and returns the
same status code + headers with an empty body, per RFC 9110 §9.3.2.
Tests guard parity for `/`, `/assets/*`, `/api/health`, and
`/api/sources` — this is the contract `curl -I` and HTTP cache
intermediaries rely on. Future routes inherit the same expectation.

Full schemas: `rest-api.md`.

## Middlewares

- **Request logging**: structured slog, one line per request with method, path, status, duration, bytes.
- **Recover panic**: log + 500 + continue serving (do NOT crash on a single bad handler). The structured 500 envelope is written ONLY when the response has not yet started; if a handler already wrote status/body before panicking (e.g. a future streaming handler), the recover path logs the panic and returns without a second write, so it cannot append a JSON error to a partially-sent body or emit a superfluous header. The recover middleware reads the wrapped writer's `wrote()` flag for this decision.
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
