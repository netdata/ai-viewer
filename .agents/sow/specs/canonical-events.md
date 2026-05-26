# Canonical Events

## TL;DR

Adapters do not write to SQLite directly. They emit a stream of typed `canonical.Event` values into a channel. The ingester is the only writer. This gives one place to enforce dedup, ordering, and schema invariants — and lets adapters be tested in pure isolation.

## Event Types

Defined in `internal/canonical/events.go`. All events carry: `SourceID string`, `SourceSeq uint64` (monotonic-per-source dedup key), `Ts int64` (microseconds UTC).

```go
type Event interface {
    EventKind() EventKind
    EventSourceID() string
    EventSourceSeq() uint64
    EventTs() int64
}

type EventKind string
const (
    EvSessionStarted   EventKind = "session_started"
    EvSessionFinalized EventKind = "session_finalized"
    EvSessionUpdated   EventKind = "session_updated"     // status/agent/model became known
    EvTurnStarted      EventKind = "turn_started"
    EvTurnFinalized    EventKind = "turn_finalized"
    EvOpStarted        EventKind = "op_started"
    EvOpFinalized      EventKind = "op_finalized"
    EvPayloadRef       EventKind = "payload_ref"
    EvLogEntry         EventKind = "log_entry"
    EvSourceProgress   EventKind = "source_progress"     // cursor checkpoint
    EvSourceError      EventKind = "source_error"        // parse error, surfaced in /health
)
```

### SessionStarted

```go
type SessionStartedEvent struct {
    EventBase
    NativeID        string  // originId/sessionId/uuid from the source
    ParentNativeID  string  // empty for root sessions
    Kind            SessionKind  // 'root'|'sub_agent'|'tool_internal'
    AgentName       string  // may be empty if not yet known
    Model           string  // may be empty if not yet known
    Extras          map[string]any
}
```

### SessionFinalized

```go
type SessionFinalizedEvent struct {
    EventBase
    NativeID      string
    Status        string   // 'completed'|'failed'
    ErrorClass    string   // present when failed
    EndTs         int64
}
```

### SessionUpdated

Emitted when the adapter learns metadata about an already-started session (e.g. the model name appears in the first LLM call). The ingester `UPDATE`s the row; idempotent.

### TurnStarted / TurnFinalized

```go
type TurnStartedEvent struct {
    EventBase
    SessionNativeID string
    Seq             int
}

type TurnFinalizedEvent struct {
    EventBase
    SessionNativeID string
    Seq             int
    Status          string
    ErrorClass      string
    EndTs           int64
    TokensIn        int64
    TokensOut       int64
    CostUSD         float64
}
```

### OpStarted / OpFinalized

```go
type OpStartedEvent struct {
    EventBase
    SessionNativeID string
    TurnSeq         int
    Seq             int             // order within turn
    ParentOpSeq     int             // -1 if top-level
    Kind            OpKind          // 'llm'|'tool'|'session'|'reasoning'|'internal'
    Name            string
    ToolNamespace   string          // tool ops only
    Model           string          // llm ops only
    Provider        string          // llm ops only
    ChildSessionNativeID string     // session ops only
    Extras          map[string]any
}

type OpFinalizedEvent struct {
    EventBase
    SessionNativeID string
    TurnSeq         int
    Seq             int
    Status          string
    ErrorClass      string
    ErrorMessage    string
    EndTs           int64
    TokensIn        int64
    TokensOut       int64
    CostUSD         float64
    BytesIn         int64
    BytesOut        int64
    CtxUsed         int64
    CtxMax          int64
}
```

### PayloadRef

```go
type PayloadRefEvent struct {
    EventBase
    SessionNativeID string
    TurnSeq         int
    OpSeq           int
    PayloadKind     string   // 'llm_request'|'llm_response'|...
    Format          string   // 'http'|'sse'|'json'|'jsonrpc'|'text'
    Compression     string   // 'gzip' | ''
    LocationURI     string
    OriginalBytes   int64
    StoredBytes     int64
}
```

### LogEntry

Surfaces log lines that the source recorded against a session/turn/op. Used for the per-session "Logs" tab in the UI.

### SourceProgress

Emitted periodically by an adapter to checkpoint its cursor. The ingester persists this into `sources.cursor` so a restart resumes from there. Cursor format is opaque JSON — each adapter chooses its own shape (file offsets, file mtime, SQLite rowid, etc.).

### SourceError

Emitted when an adapter hits a parse error. The ingester increments `sources.parse_errors` and writes a `log_entries` row with severity `ERR` (no session attached). Surfaced in `/health` and the adapters UI panel.

## Ordering Guarantees

- Within a single session, the adapter MUST emit events in chronological order (turn 1 start before turn 2 start, etc.).
- Across sessions, no ordering guarantee is required. The ingester orders by `Ts`.
- `SourceSeq` is monotonic **per source** and used only for dedup, not ordering.

## Idempotency

- Every event has a stable `SourceSeq`. The ingester maintains a high-water-mark per source and discards events with `SourceSeq <= hwm`.
- `*Finalized` events are upserts: if the corresponding `*Started` arrives later, the ingester reconciles (`Ts` from Started, fields from Finalized).
- Re-scanning the same source files MUST NOT produce duplicate rows.

## Why this shape

- Decouples parsers from the SQL layer; adapters can be tested with a fake `chan Event`.
- Lets a future "replay" tool re-emit canonical events from a SQLite dump into a different store.
- Keeps the SQL writer simple and centralized — one place to enforce invariants.
