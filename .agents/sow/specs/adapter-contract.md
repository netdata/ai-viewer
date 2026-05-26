# Adapter Contract

## TL;DR

An adapter is one Go package under `internal/adapters/<name>/` that knows everything about one source format and nothing about anything else. It implements one interface, exports one constructor, and is testable in isolation with fixture files under `testdata/`.

## Interface

`internal/canonical/adapter.go`:

```go
package canonical

import "context"

type Adapter interface {
    // Name returns the stable identifier for this adapter (e.g. "aiagent_v3").
    Name() string

    // Format returns the user-facing format string written into sources.format.
    Format() string

    // Scan emits historical events from the source starting at `since`.
    // Returns when caught up to the current state of the source.
    Scan(ctx context.Context, since Cursor, out chan<- Event) error

    // Tail blocks emitting realtime events as the source changes,
    // until ctx is cancelled.
    Tail(ctx context.Context, out chan<- Event) error

    // ParseCursor decodes a stored cursor JSON blob from sources.cursor.
    // Returns a zero Cursor for empty input (first run).
    ParseCursor(stored string) (Cursor, error)
}

type Cursor interface {
    // String returns an opaque JSON encoding for persistence in sources.cursor.
    String() string
    // After reports whether c is strictly after other (used by ingester for HWM checks).
    After(other Cursor) bool
}

type AdapterFactory func(location string, opts AdapterOptions) (Adapter, error)

type AdapterOptions struct {
    Logger    *slog.Logger
    OnError   func(err error)   // adapter MUST call this for non-fatal parse errors
}
```

## Lifecycle

```
                     ┌──────────────────┐
                     │  Adapter created │ (factory called by ingest at startup)
                     └────────┬─────────┘
                              │
                              ▼
              ┌──────────────────────────────┐
              │  Scan(ctx, cursor, out)      │  initial backfill
              │  reads what's on disk now    │
              │  emits events, returns when  │
              │  caught up                   │
              └────────┬─────────────────────┘
                       │
                       ▼
              ┌──────────────────────────────┐
              │  Tail(ctx, out)              │  blocks forever
              │  watches for new data        │
              │  emits events as they arrive │
              │  returns when ctx cancelled  │
              └──────────────────────────────┘
```

The ingester calls `Scan` first, then `Tail`. The adapter MUST handle the case where new data arrives *during* `Scan`: those events should be picked up by `Tail` and deduped by the ingester via `SourceSeq`.

## Concurrency Rules

- An adapter MAY spawn internal goroutines for parallelism.
- An adapter MUST stop all internal goroutines before `Scan` returns or when `ctx` is cancelled during `Tail`.
- An adapter MUST NOT close the `out` channel — the ingester owns it.
- All file I/O must respect `ctx.Done()` for cancellation.

## Error Handling

Two error categories:

| Category | How to surface |
|---|---|
| **Per-record parse error** (one malformed line in a JSONL file) | Call `opts.OnError(err)`. Continue processing. Emit no event for that record. |
| **Fatal source error** (source root not readable, schema completely wrong) | Return the error from `Scan` or `Tail`. The ingester will mark the source disabled and log loudly. |

Adapters MUST NOT panic. Use `recover` in any internal goroutine entry point and convert to a per-record error.

## Cursor Design Guidance

Each adapter chooses its own cursor format. Examples:

- **aiagent_v3**: `{"session/<sessionId>.jsonl": <byte_offset>}` per file.
- **aiagent_v2**: `{"<originId>.json.gz": <mtime_us>}` per file.
- **claude_code**: same shape as aiagent_v3.
- **codex**: `{"<rollout-file>": <byte_offset>}` keyed by date-sharded filename.
- **opencode**: `{"messages_rowid": <last_seen>, "sessions_rowid": <last_seen>}`.

Cursors are stored as JSON in `sources.cursor`. The ingester treats them as opaque. The adapter is responsible for `Cursor.After()` correctness.

## Testing Requirements (mandatory per adapter)

1. `testdata/<name>/` contains real, sanitized fixture files covering: a happy-path session, a session with sub-agents, a session with tool calls, a session with failures, an in-progress (unfinalized) session.
2. Unit tests assert: `Scan` over fixtures produces the expected stream of events (compared against golden JSON in `testdata/<name>/expected/`).
3. Unit tests for `Tail`: write a new fixture file mid-test, assert the adapter emits its events within 1 second.
4. Unit tests for cursor resume: scan with `since=cursorAtMidpoint`, assert only the latter half of events are emitted.
5. Property test: re-scanning with the final cursor produces zero events.

## Adding a New Adapter

Step-by-step (also lives in `docs/adding-an-adapter.md` for end-user contributors):

1. Create `internal/adapters/<name>/adapter.go` implementing the interface.
2. Add sanitized fixtures under `testdata/<name>/`.
3. Add tests under `internal/adapters/<name>/adapter_test.go`.
4. Register the factory in `internal/adapters/registry.go`.
5. Write `.agents/sow/specs/adapter-<name>.md` documenting the format, file locations, mapping to canonical events, edge cases.
6. Update `README.md` Source Formats table.
7. Open a SOW (or reuse the active one) with the change, run external review.
