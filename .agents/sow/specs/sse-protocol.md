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

## Filter Shape

```json
{
  "time_range": { "from": <ts_us>, "to": <ts_us> },
  "sources": ["aiagent_v3:/home/costa/.ai-agent/sessions"],
  "agents": ["nedi", "neda"],
  "models": ["claude-opus-4-7"],
  "tools": ["mcp__slack__send_message"],
  "status": ["failed"],
  "session_id": null,
  "root_session_id": null
}
```

All fields are optional; omitted = no constraint. `time_range.to == null` means "open-ended into the future" (the typical real-time case).

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

Emitted when any row of a matching session is inserted or updated.

```json
{ "session_id": "<canonical_id>", "root_session_id": "<canonical_id>", "ts": <us> }
```

The client decides whether to re-fetch full session detail.

### `stats_invalidated`

Emitted (rate-limited to ~1 per second) when catalog rollups change so the analytics pages know to re-fetch.

```json
{ "ts": <us> }
```

### `source_status_changed`

Emitted when a source's parse_errors count changes or enabled flag flips. The Sources panel re-fetches.

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
- If the buffer is exhausted (client was offline too long): server sends a `resync` event telling the client to re-fetch its current view from REST.

```
event: resync
data: { "reason": "buffer_overflow" }
```

## Backpressure

- Each SSE client has a buffered channel (capacity 256 events).
- If the channel is full when an event arrives: the server drops the oldest event and increments a per-subscription `dropped` counter. The client sees a counter in subsequent `session_changed` events and may re-fetch its full view.
- Slow clients do not block other clients or the SSE hub goroutine.

## Debuggability

A working `curl` should be enough to reproduce any client behavior:

```bash
SUB=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"filter":{"status":["failed"]}}' \
  http://127.0.0.1:7710/api/subscriptions | jq -r .id)
curl -N "http://127.0.0.1:7710/api/events?sub=$SUB"
```
