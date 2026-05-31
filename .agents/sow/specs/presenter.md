# Presenter (`ai-viewer-serve`)

## TL;DR

HTTP server. Serves the embedded frontend at `/`, exposes REST endpoints under `/api/`, and a single SSE endpoint at `/api/events`. Reads canonical SQLite. Knows nothing about adapters.

## Lifecycle

```
main()
  ├─ load config (db path, bind addr, state dir)
  ├─ open SQLite (read-only mode for safety; WAL allows concurrent writer)
  ├─ run migration check (refuse to start if schema_meta.version != supported — any mismatch, older or newer; CheckSchema)
  ├─ start SSE hub goroutine
  ├─ start notify-table poller goroutine (read-only; cursor starts at MAX(seq))
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
- The change feed from the **notify-table poller** (a separate read-only
  goroutine — see below — reading the SQLite `notify` table, not an IPC channel).
- An events channel from each connected SSE client (for keepalives).

The notify-table poller is a separate read-only goroutine that polls
`SELECT … FROM notify WHERE seq > <cursor> ORDER BY seq` roughly once per
second (cursor starts at `MAX(seq)` on boot). For each change row it evaluates
each subscription's filter by **reusing the Chunk-12 `sessionFilter.whereClause`**
— `SELECT 1 FROM sessions s WHERE s.id=? AND (<whereClause>) LIMIT 1` — so SSE
matching is identical to REST list matching by construction. This per-row
match evaluation runs **in the poller, off the hub's fan-out path**; the hub
only enqueues the resulting minimal events onto each matching subscription's
channel. (`stats_invalidated` rows fan out to all subscriptions, coalesced to
≈1/s on emit; `source_status_changed` rows fan out to subscriptions whose
`sources` filter admits that source.)

The SSE message does NOT carry the full session data — only `{"type":"session_changed","session_id":"..."}`. The client decides whether to re-fetch the full session detail via REST. This keeps SSE messages tiny and keeps the SQL load on REST endpoints where caching and pagination already live.

Backpressure, replay, and retention (see `sse-protocol.md` for the client
contract):

- **Backpressure** — each subscription has a buffered channel capped at 256
  events. When it is full the hub drops the oldest event and increments a
  per-subscription `dropped` counter; slow clients never block the hub or
  other subscriptions.
- **Last-Event-ID replay** — each subscription keeps a 100-event ring of its
  most recent events; on reconnect with `Last-Event-ID` the hub replays the
  buffered events after that ID (or signals a `resync` when the ring has rolled
  past it).
- **Reconnect retention** — a subscription survives 60 s after its client
  disconnects so a reconnecting browser keeps the same buffer; after 60 s with
  no client the subscription is dropped.

## Embedded Frontend

The frontend's Vite-built `dist/` directory is embedded via `go:embed`:

```go
//go:embed all:frontend_dist
var frontendFS embed.FS
```

The `all:` prefix embeds dotfiles, so the directory is never empty: a tracked
`cmd/ai-viewer-serve/frontend_dist/.gitkeep` keeps the embed compiling on a
clean checkout before any frontend build runs. Everything else under that
directory is build output, git-ignored, and produced by `scripts/build.sh`
(see `deployment.md` and `architecture.md` §"Embed-dir git policy").

Build pipeline: `scripts/build.sh` runs `npm run build` in `frontend/`, copies the output to `cmd/ai-viewer-serve/frontend_dist/`, then `go build`. Documented in `deployment.md`.

### serveIndex contract

`GET /` and `HEAD /` are answered by `serveIndex`, which has three states:

- **Built UI present** — `index.html` exists in the embedded FS: respond `200`
  with `Content-Type: text/html; charset=utf-8` and `Cache-Control: no-cache`
  so the browser re-fetches the shell after a redeploy and picks up the new
  hashed asset names. `HEAD` carries the same headers with an empty body
  (RFC 9110 §9.3.2).
- **UI not built** — the FS is wired (`p.frontend != nil`) but `index.html` is
  absent (the `.gitkeep`-only state, e.g. `go run ./cmd/ai-viewer-serve`
  without a prior `scripts/build.sh`): respond `200` with the same
  `text/html`/`no-cache` headers and a small built-in notice instructing the
  operator to run `scripts/build.sh`. The server logs this once at `Info` and
  keeps every `/api/*` route fully functional — the absence of a built UI is a
  recoverable dev-time state, not a fatal error. The serve binary's
  `embeddedFrontend()` therefore never fails on a missing `index.html`.
- **Frontend disabled** — `p.frontend == nil` (a test/wiring misconfiguration
  the production binary cannot reach, since it always embeds the FS): respond
  `500` with the structured `INTERNAL_ERROR` envelope.

`serveAsset` is unchanged: `/assets/*` serves the hashed bundle with a long
immutable cache and returns `404` on a miss — assets never fall back to the SPA
shell, so a missing bundle surfaces as a real failure rather than masquerading
as the app.

**SPA fallback.** Every `GET`/`HEAD` request that the mux does not route to a
more-specific handler — i.e. not `/api/*`, not `/assets/*`, and not an exact
root public file (`/favicon.svg`) — serves the SPA shell via `serveIndex`: the
built `index.html`, or the not-built notice, with the same `200` + `no-cache` +
explicit `Content-Length` (and HEAD parity) as `GET /`. This is what makes a
hard navigation / reload / bookmark of a client-side route (`/sessions/:id`,
`/sources`, `/topology`, `/tools`, `/models`, `/agents`) load the app instead of
a JSON `404`: `BrowserRouter` owns client-side routing and the app renders its
own in-router `NotFound` for genuinely unknown client paths. A non-`GET`/`HEAD`
method on such a path is `405 METHOD_NOT_ALLOWED`. Only `/api/*` (structured JSON
`404` for unknown sub-routes) and `/assets/*` (`404` on miss) are exempt — they
must surface real errors, never the shell. The fallback is a catch-all, not an
allowlist of known routes: the server stays ignorant of the client route table
(so Phase-2/3 routes need no server change), at the cost that a typo'd non-API
path loads the app (which then shows its own `NotFound`) rather than a server
`404` — the conventional SPA contract.

### Root public assets

Vite copies `frontend/public/*` to the root of `dist/` (not under `assets/`).
The built `index.html` references these at the site root — Phase 1 ships
`favicon.svg`. `scripts/build.sh` therefore copies the ENTIRE `dist/` tree into
the embed dir (not only `index.html` + `assets/`), and the presenter serves
each referenced root file from an explicit route (`GET /favicon.svg`) via
`servePublicFile`. That handler reads a single, traversal-safe basename from the
embed root, returns the file with a content-typed `200` and a short
revalidating cache (`no-cache`, since these names are not content-hashed), and
returns `404` on a miss. Each root public file gets an EXPLICIT exact mux route
so it is served with its correct content-type and short revalidating cache;
without that route the SPA fallback (above) would serve the HTML shell in place
of `/favicon.svg`. Adding a new root public file (e.g. `robots.txt`) is one new
mux registration plus a build-output copy. (An unexpected non-API, non-asset
path is NOT a `404` — it serves the SPA shell so client-side routes load on
reload; see the SPA-fallback contract above.)

## Routing

```
GET  /                          → frontend index.html (or not-built notice)                 (live)
GET  /<any non-/api non-/assets> → SPA shell (client-route fallback: /sessions/:id, …)       (live)
GET  /favicon.svg               → embedded root public asset                                (live)
GET  /assets/*                  → embedded frontend assets                                  (live)
GET  /api/health                → JSON health status                                        (live)
GET  /api/sources               → list sources, ingest cursors, parse error counts          (live)
GET  /api/sessions              → list sessions with filters and pagination                 (live)
GET  /api/sessions/:id          → session detail with turns and ops                         (live)
GET  /api/sessions/:id/logs     → log entries for a session                                 (live)
GET  /api/sessions/:id/topology → topology graph for a session (nodes, edges)               (implemented — SOW-0006)
GET  /api/sessions/:id/timeline → ordered spans for the timeline view                       (implemented — SOW-0006)
GET  /api/topology              → cross-session topology graph over the active filter        (implemented — SOW-0006)
GET  /api/stats                 → cross-session statistics with filters                     (live)
GET  /api/catalog/tools         → catalog_tools, with filters                               (Chunks 12+ — not yet implemented)
GET  /api/catalog/models        → catalog_models, with filters                              (Chunks 12+ — not yet implemented)
GET  /api/catalog/agents        → catalog_agents, with filters                              (Chunks 12+ — not yet implemented)
GET  /api/payloads/:ref         → streams payload bytes (gz-decompressed inline if asked)   (Chunks 12+ — not yet implemented)
POST /api/subscriptions         → create an SSE subscription with a filter                  (live)
DELETE /api/subscriptions/:id   → cancel                                                    (live)
GET  /api/events?sub=:id        → SSE event stream                                          (live)
```

Chunk 11 of SOW-0001 shipped the first four "live" routes (`/`,
`/assets/...`, `/api/health`, `/api/sources`); Chunk 12 added
`/api/sessions`, `/api/sessions/:id`, `/api/sessions/:id/logs`, and
`/api/stats`; Chunk 13 added the SSE surface — `POST /api/subscriptions`,
`DELETE /api/subscriptions/:id`, and `GET /api/events?sub=:id`. **SOW-0006
added the per-session `/api/sessions/:id/topology` + `/api/sessions/:id/timeline`
and the cross-session `/api/topology`.** The catch-all `/api/*` handler returns
a structured `NOT_FOUND` envelope naming the chunk/SOW in which a still-missing
route (`catalog`, `payloads`) will land, so future operators reading the response
see the implementation roadmap at the error site. Anything not in the
live set MUST NOT be added to a client (UI, curl scripts) until the
corresponding chunk merges.

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
- **WriteTimeout**: `http.Server.WriteTimeout` is intentionally unset (0) because SSE streams are long-lived — a global write deadline would kill an idle-but-healthy `/api/events` connection. Normal handlers stay bounded by the 30 s per-request query context (the `queryTimeout`), and the SSE handler additionally clears its own write deadline per-connection via `http.NewResponseController(w).SetWriteDeadline(time.Time{})`. (This resolves the Chunk-11 glm WriteTimeout deferral.)

## Graceful Shutdown

1. Stop the notify-table poller FIRST (cancel its context and wait for it to
   exit) so no new events are produced while the SSE clients are being torn
   down — this avoids a race where the poller delivers onto channels that are
   about to close.
2. Deliver a `disconnect` event to every active SSE subscription, then close
   all SSE client channels (`hub.Shutdown`) so the long-lived stream handlers
   return and the browser reconnects cleanly.
3. Stop accepting new connections and wait for in-flight handlers to drain
   (`http.Server.Shutdown` with a 30 s timeout).
4. Close SQLite.
5. Exit 0.

### SSE Lifecycle Mutex (create vs. shutdown)

`ShutdownSSE` and `POST /api/subscriptions` share a dedicated presenter mutex
(`sseLifecycleMu`) that serializes subscription creation against shutdown:

- `ShutdownSSE` acquires the mutex, records the shutting-down state, releases
  the mutex, and only THEN runs `broadcastDisconnect` + `hub.Shutdown`. The
  long-running hub close happens **outside** the mutex so it never blocks an
  in-flight create beyond the instant needed to flip the state.
- `POST /api/subscriptions` parses and validates the filter first (no lock —
  a bad filter is a `400` regardless of shutdown). It then acquires the mutex,
  checks the shutting-down state, and either returns `503` (release, mutate
  nothing) or calls the registry `create` (hub registration + registry insert)
  and releases the mutex only after `create` returns.

Because the state flip and the create both happen under the same mutex, the
check-then-create pair is atomic with respect to shutdown: a create that begins
before shutdown completes fully (hub + registry consistent) and a create that
begins after shutdown sees the state and returns `503`. The guaranteed
invariant is consistency, NOT shutdown-survival: there is never a `200` for a
subscription the hub did not register, and never an orphan (a registry entry
without a matching hub subscription, or vice-versa) — `hub.Shutdown` + the
registry `clear()` run after the flag flip, by which point every in-flight
create has finished its insert. A subscription created in the instant just
before shutdown is validly registered (a correct `200`) and is then torn down
together with all others by the shutdown sequence (disconnect broadcast → hub
close → registry clear); the client handles that exactly like any other
mid-stream shutdown (reconnect/`retry_after_ms`, then re-fetch). The mutex does
NOT promise that a successful `POST` remains attachable across an immediately
following graceful shutdown — that stronger semantic is neither needed nor
provided.

After `hub.Shutdown` removes every subscription channel, `ShutdownSSE` also
clears the subscription registry (`subscriptionManager.clear`). `hub.Shutdown`
deletes subscriptions WITHOUT firing the `OnRemove` hook (the process is going
away), so without this step the registry would keep reporting subscriptions the
closed hub no longer holds — a stale `/api/health` `sse.subscriptions` count and
an orphan entry for any create that completed in the instant before the flag
flipped. The clear runs OUTSIDE `sseLifecycleMu`, and because the mutex forces
every in-flight create to finish its hub-registration + registry insert before
the flag flip (hence before `hub.Shutdown` and the clear), the registry ends
empty and consistent with the empty hub. A create arriving after the flag flip
returns `503` and inserts nothing. Net post-shutdown invariant: the registry
holds no id the hub does not also hold (after a full shutdown both are empty).

**Lock-ordering contract (deadlock safety):** the only nesting is
`sseLifecycleMu` → (`hub.mu` via `hub.Add`) → (`subscriptionManager.mu`). The
hub's `OnRemove` hook (`onSubRemoved`) and the notify poller never acquire
`sseLifecycleMu`, and `ShutdownSSE` never holds `sseLifecycleMu` while calling
`hub.Shutdown`. No code path acquires `sseLifecycleMu` while already holding
`hub.mu`, `subscriptionManager.mu`, or `notifyMu`, so no cycle can form.

## Configuration

```
--db <path>                SQLite path (must exist; ingester creates it)
--state-dir <path>         state directory (reserved; serve does not use an IPC channel — the notify channel is the SQLite `notify` table). Flag kept for symmetry with the ingester.
--bind <addr>              default 127.0.0.1:7710
--log-level <level>        default info
```

The port 7710 is a placeholder; will be locked in during Phase 1 implementation when no conflicts are confirmed on the user's workstation.
