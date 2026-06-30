# SSE Protocol

## TL;DR

One SSE endpoint at `GET /api/events?sub=<subscription_id>`. Subscriptions are created via REST. Events are minimal JSON envelopes; the client re-fetches details via REST when needed. Designed for `curl -N` debuggability.

## Subscription Lifecycle

```
1. POST /api/subscriptions
   body: { "filter": { ... } }
   resp: { "id": "sub-abc123", "filter_normalized": { ... } }

2. GET /api/events?sub=sub-abc123
   → opens SSE stream; server pushes events matching filter
   → on disconnect, the subscription remains valid for 60 s for fast reconnect
   → after 60 s of no client, subscription is dropped

3. DELETE /api/subscriptions/sub-abc123     (optional explicit cleanup)
```

The subscription `id` is `sub-` followed by 32 lowercase hex characters
(128-bit crypto-random), e.g. `sub-1f3c…` (32 hex digits). It is opaque to
clients; do not parse or construct it.

**At most ONE active `/api/events` stream per subscription.** A subscription is
single-consumer: while one stream is connected, a second concurrent
`GET /api/events?sub=<id>` for the same subscription is rejected with
`409 CONFLICT` (the normal reconnect flow is serial — the old stream's
connection closes, the subscription stays alive for 60 s, then the new stream
attaches). This prevents two connections from splitting one subscription's
events. `HEAD /api/events?sub=<id>` returns the stream headers with an empty
body — `200` if the subscription exists, `404` if not — and does NOT open a
stream, consume the channel, or touch the connect/disconnect lifecycle
(reconnect-retention timer).

## Filter Shape

```json
{
  "time_range": { "from": <ts_us>, "to": <ts_us> },
  "sources": ["aiagent_v3:~/.ai-agent/sessions"],
  "agents": ["nedi", "neda"],
  "models": ["claude-opus-4-7"],
  "tools": ["mcp__slack__send_message"],
  "status": ["failed"],
  "session_id": null,
  "root_session_id": null
}
```

All fields are optional; omitted = no constraint. `time_range.to == null` means "open-ended into the future" (the typical real-time case).

The filter is validated and normalized with the **same rules as the REST list
filters** (`rest-api.md` §Conventions): unknown fields are rejected; an array
field that is present but empty (e.g. `"models": []`) is a `BAD_REQUEST`; and
any value containing an ASCII control character (byte `< 0x20`) is a
`BAD_REQUEST`. The normalized filter is echoed back as `filter_normalized` on
the `POST /api/subscriptions` response.

Subscription filters remain index-backed invalidation filters. `cwd`,
`provider_alias`, `call_path`, and `error_class` are not subscription filter
dimensions in SOW-0105 and are rejected as unknown fields. SSE event payloads
stay minimal invalidation frames; clients refetch REST details for any newly
exposed metadata or proof fields.

## Event Envelope

Standard SSE format:

```
event: <type>
data: <single-line JSON>

```

(Note the trailing blank line per SSE spec.)

The `id:` field is also set on each message to enable `Last-Event-ID` reconnect.

## Event Types

### `session_changed`

Emitted when any row of a matching session is inserted or updated — including when
the background parent-link resolver repairs a child-first ingestion: it emits a
`session_changed` for the affected child, its newly-linked parent, and its root in
the same transaction as the linkage UPDATE (`ingester.md` §resolver pass).

```json
{ "session_id": "<canonical_id>", "root_session_id": "<canonical_id>", "ts": <us>, "dropped": <n> }
```

The client decides whether to re-fetch full session detail. `dropped` is the
per-subscription backpressure drop counter (see §Backpressure); it is included
ONLY when non-zero, signalling the client missed `dropped` events and should
re-fetch its full view. Clients that don't track it can ignore it.

A session's logs are part of the session: a log write marks the session row
dirty, so this frame also re-fetches the open session's logs. The reference
client invalidates `['session', session_id]`, `['sessions']`, the
`['logs', session_id]` key family (partial-match across severity sub-keys), and
the per-session viz keys `['session-timeline', session_id]` +
`['session-topology', session_id]` plus `['topology']` (cross-session graph) so the
open Trace/Timeline/Topology tabs live-refresh (SOW-0006 AC#6).

### `stats_invalidated`

Emitted (rate-limited to ~1 per second) when catalog rollups change so the analytics pages know to re-fetch.

```json
{ "ts": <us> }
```

### `source_status_changed`

Emitted when a source's parse_errors count, enabled flag, lifecycle state,
read-model state, or lifecycle/read-model transition/error evidence changes.
Heartbeat-only `tail_heartbeat_at` persistence while the source remains
`tailing` does not emit this event; clients reconcile that liveness evidence
through REST health/source reads.
The event is only an invalidation hint; clients re-fetch `/api/sources` and
`/api/health` for the full state.

```json
{ "source_id": "<id>", "ts": <us> }
```

### `keepalive`

Comment line (`: keepalive`) every 15 s so proxies don't time out idle connections. Not a real event; clients ignore.

### `disconnect`

Emitted at graceful server shutdown.

```json
{ "reason": "server_shutdown", "retry_after_ms": 2000 }
```

## Reconnect Behavior

- Browser `EventSource` reconnects automatically on disconnect.
- Server respects `Last-Event-ID` header: replays any events buffered for the subscription since that ID (buffer size: 100 most recent events per subscription).
- If the buffer cannot prove coverage of the gap — the client was offline too long (oldest retained event ID is already greater than `Last-Event-ID`) OR the `Last-Event-ID` is unparseable or ahead of the newest retained ID (a stale/forged value) — the server sends a `resync` event telling the client to re-fetch its current view from REST.

```
event: resync
data: { "reason": "buffer_overflow" }
```

## Backpressure

- Each SSE client has a buffered channel (capacity 256 events).
- If the channel is full when an event arrives: the server drops the oldest event and increments a per-subscription `dropped` counter. The client sees a counter in subsequent `session_changed` events and may re-fetch its full view.
- Slow clients do not block other clients or the SSE hub goroutine.

## Transport (server-internal)

The server learns of changes by **polling the SQLite `notify` table** that the
ingester writes (see `data-model.md` §notify and `architecture.md` §"Notify
channel"): a read-only poller goroutine reads new `notify` rows (`WHERE seq >
<cursor>`, ~1 s interval) and fans matching changes onto subscription channels.
This transport is entirely server-internal and invisible to clients — the wire
contract above (subscriptions, event frames, `Last-Event-ID`) is unchanged
regardless of how the server is notified.

## Debuggability

A working `curl` should be enough to reproduce any client behavior:

```bash
SUB=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"filter":{"status":["failed"]}}' \
  http://127.0.0.1:7710/api/subscriptions | jq -r .id)
curl -N "http://127.0.0.1:7710/api/events?sub=$SUB"
```
