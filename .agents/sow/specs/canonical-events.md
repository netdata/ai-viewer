# Canonical Events

## TL;DR

Adapters do not write to SQLite directly. They emit a stream of typed `canonical.Event` values into a channel. The ingester is the only writer. This gives one place to enforce dedup, ordering, and schema invariants — and lets adapters be tested in pure isolation.

The canonical event model is **deliberately wider than any single source format** so the same downstream schema covers ai-agent (v2/v3), claude-code, codex, and opencode. The cross-walk lives in this document; each adapter spec documents how its source format projects onto these events.

## Event Types

Defined in `internal/canonical/events.go`. All events carry: `SourceID string`, `SourceSeq uint64` (monotonic per file; observability counter, NOT a dedup key — see §Ordering Guarantees, §Idempotency, and ingester.md §Dedup and Idempotency), `Ts int64` (microseconds UTC).

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
    EvSessionUpdated   EventKind = "session_updated"     // metadata became known (model, agent, cwd, etc.)
    EvSessionFinalized EventKind = "session_finalized"
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

### SessionStartedEvent

```go
type SessionStartedEvent struct {
    EventBase
    NativeID        string  // originId/sessionId/uuid from the source
    RootNativeID    string  // root of the session tree (== NativeID for top-level sessions)
    ParentNativeID  string  // empty for root sessions
    ParentOpKey     string  // op identifier inside parent (when known; empty otherwise)
    Kind            SessionKind  // 'root' | 'sub_agent' | 'tool_internal' | 'fork'
    AgentName       string  // may be empty if not yet known
    Model           string  // may be empty if not yet known at start
    Cwd             string  // working directory at session start (claude-code, codex, opencode)
    CallPath        string  // durable agent-chain string (ai-agent v3 'callPath'); may be empty
    Provider        string  // canonical provider ('anthropic'|'openai'|...); empty if unknown at start (SOW-0023)
    ProviderAlias   string  // user-defined provider alias (opencode); empty otherwise (SOW-0023)
    Extras          map[string]any   // adapter-specific extras → sessions.extras_json
}
```

Notes:

- `RootNativeID` is the **root** of a sub-agent tree. For ai-agent v3, `originId` is already the root id; the adapter passes it directly. For other formats, the adapter resolves the root (or sets it equal to `NativeID` if no parent).
- `ParentOpKey` is an opaque string identifying the parent op that spawned this child. Format is adapter-specific (e.g. `<turnSeq>:<opSeq>` or the parent's `opId` string). The ingester uses it only for cross-referencing; canonical `ParentOpSeq` (an integer) on `OpStartedEvent` is derived independently.
- `Kind` values:
  - `root` — top-level interactive session
  - `sub_agent` — spawned as a sub-agent / Task / Agent tool call
  - `tool_internal` — internal helper session created by a tool implementation
  - `fork` — session forked from another (codex `forked_from_id`)

### SessionUpdatedEvent

Emitted when the adapter learns metadata about a session — e.g. the model name
appears in the first LLM call, or the agent name becomes known. The normal path
updates an already-started row. If metadata legitimately arrives before the
matching `SessionStartedEvent`, the ingester creates the same stub session row
used by turn/op/log events, then applies the partial metadata update. The event
must never mark a session as changed without a durable `sessions` row, because
notify rows are emitted atomically with the batch.

```go
type SessionUpdatedEvent struct {
    EventBase
    NativeID       string
    AgentName      string  // empty if no update
    Model          string  // empty if no update
    Cwd            string  // empty if no update
    Status         string  // empty if no update
    Provider       string  // last-known canonical provider; empty if no update (SOW-0023)
    ProviderAlias  string  // last-known user-defined alias; empty if no update (SOW-0023)
    Extras         map[string]any  // merged into existing extras_json
}
```

### SessionFinalizedEvent

```go
type SessionFinalizedEvent struct {
    EventBase
    NativeID      string
    Status        SessionStatus // see below
    ErrorClass    string        // present when failed
    ErrorMessage  string        // present when failed
    EndTs         int64
}

type SessionStatus string
const (
    StatusRunning     SessionStatus = "running"      // started, no terminal signal yet
    StatusCompleted   SessionStatus = "completed"    // terminal: success
    StatusFailed      SessionStatus = "failed"       // terminal: explicit error
    StatusAbandoned   SessionStatus = "abandoned"    // started but never produced any turn (orphan)
    StatusInterrupted SessionStatus = "interrupted"  // started turns but no terminal record (process killed)
)
```

If the terminal signal arrives before a `SessionStartedEvent`, the ingester
creates a stub session row and applies the terminal status to that row. A later
`SessionStartedEvent` fills source metadata through the usual idempotent upsert
rules.

Notes on terminal signal availability per source:

- **ai-agent v3** — has explicit `session_summary` (→ completed) and `session_error` (→ failed). Orphans (session_start only) → `abandoned`. Mid-turn deaths (no `turn_end`) → `interrupted`.
- **ai-agent v2** — `opTree.success`/`opTree.error` carry terminal state; the `'final'` snapshot marks completed/failed. Pre-final snapshots → `running`.
- **claude-code** — has **no native terminal signal** (sessions are resumable indefinitely). Adapter never emits `SessionFinalizedEvent` for claude-code; sessions stay `running`. UI filters via `last_activity_ts` for staleness display.
- **codex** — emits `task_complete` per turn but has **no per-session terminal signal** (a clean rollout simply stops being appended; `recorder.rs:1610` may even append metadata after a turn ends). Like claude-code, the adapter does **not** emit `SessionFinalizedEvent(completed)` for a cleanly-ended session — it stays `running` and the UI uses `last_activity_ts` for staleness. The *only* `SessionFinalizedEvent` codex emits is the synthetic `failed/incomplete` for a session whose most-recent turn was left hanging (no `task_complete`/`turn_aborted`) and whose file is mtime-stale ≥ 1 h (a crash); see `adapter-codex.md` state-machine rule #23.
- **opencode** — no explicit session-end column. Adapter infers terminal status from the last assistant message's `data.error` and `data.completedAt`. On a failed terminal it sets `ErrorClass` from `data.error.name` (or a default when empty) and `ErrorMessage` from `data.error.data.message` — opencode's `AssistantError` union serializes as `{name, data:{message, …}}` and every shipping variant except `MessageOutputLengthError` carries `data.message` (so `ErrorMessage` is empty only for that one message-less variant or a malformed body; decode is best-effort and never aborts the session).

### TurnStartedEvent / TurnFinalizedEvent

```go
type TurnStartedEvent struct {
    EventBase
    SessionNativeID string
    Seq             int       // 1-based within session; turn 0 reserved for init turns (ai-agent v2 may emit)
}

type TurnFinalizedEvent struct {
    EventBase
    SessionNativeID string
    Seq             int
    Status          string    // 'running' | 'completed' | 'failed' | 'aborted'
    ErrorClass      string
    EndTs           int64
    TokensIn        int64     // FRESH / uncached input tokens ONLY — billed at the full input rate. Every adapter MUST exclude cached tokens so tokens_in means the same thing across sources and the pricer never double-counts cache. Total input the model processed = TokensIn + TokensCacheRead + TokensCacheWrite.
    TokensOut       int64
    TokensCacheRead int64     // cached input tokens READ (Anthropic/claude-code cache_read_input_tokens; OpenAI/opencode cached), billed at the cache-read rate — NOT part of TokensIn.
    TokensCacheWrite int64    // cache-CREATION tokens (Anthropic cache_creation_input_tokens), billed at the cache-write rate — NOT part of TokensIn.
    CostUSD         float64   // 0.0 when adapter cannot compute (no native cost + no pricing table hit)
    Extras          map[string]any // per-turn metadata the adapter computes by finalize time (codex codex_turn_id/sandbox/effort/approval_policy/ttft_ms/last_agent_message; claude-code may surface turn_duration); marshalled into turns.extras_json. nil/empty writes NULL.
}
```

**Token accounting contract (canonical, SOW-0029).** `TokensIn` is the FRESH/uncached input only; `TokensCacheRead` and `TokensCacheWrite` are the cache portions, counted SEPARATELY; total input the model processed = `TokensIn + TokensCacheRead + TokensCacheWrite`. This SEMANTIC definition is uniform across adapters — ai-agent v2/v3, codex, opencode, claude-code all map the source's fresh-input field (not the total) to `TokensIn`, and the cache fields to `TokensCacheRead/Write` — so the pricer (`internal/pricing`, which prices each component at its own rate) never double-charges cache and `tokens_in` is comparable across sources.

Two known, tracked gaps (NOT yet uniform):

- **`CtxUsed` completeness.** `CtxUsed` is INTENDED as the total context occupancy (`TokensIn + TokensCacheRead + TokensCacheWrite + TokensOut`). claude-code and codex compute it fully; aiagent_v3, aiagent_v2, and opencode currently omit components (cache_write / output) — a pre-existing inconsistency tracked for alignment (**SOW-0031**).
- **Token persistence is OP-rollup.** The ingester rolls turn/session token totals from OP rows (`internal/ingest/aggregates.go` SUMs `ops.tokens_in`), so an adapter's token data only reaches stored totals + pricing if it sets tokens on its `OpFinalizedEvent`s. claude-code does. codex currently sets them only on `TurnFinalizedEvent`, so codex token/cost totals do not yet persist — tracked (**SOW-0030**).

**Turn `running` status**: emitted by adapters that observe a mid-turn checkpoint (e.g. ai-agent v3 can write a `turn_end` record with `status='running'` before the turn truly ends — rare; not observed in committed real data but supported by the producer). The ingester transitions `running` → terminal (`'completed' | 'failed' | 'aborted'`) when the final `turn_end` (or equivalent) arrives. UIs should treat `running` as "in progress" rather than terminal.

Notes:

- **Turn synthesis**: ai-agent v3 has explicit `turn_start`/`turn_end` records. ai-agent v2 derives turns from opTree structure. claude-code synthesizes turns from message-chain pivots. codex uses `task_started`/`task_complete` (new) or `turn_context` (old) as the delimiter. opencode treats each assistant message as a turn. Each adapter spec documents its turn-synthesis algorithm; the canonical event model treats them uniformly.
- **Cumulative-to-delta conversion**: some sources (opencode `step-finish`, others) report cumulative token counts. The adapter MUST convert to per-event deltas before emitting. Canonical events always carry deltas.

### OpStartedEvent / OpFinalizedEvent

```go
type OpStartedEvent struct {
    EventBase
    SessionNativeID  string
    TurnSeq          int
    Seq              int             // order within turn
    ParentOpSeq      int             // -1 if top-level within turn
    Kind             OpKind
    Name             string
    ToolNamespace    string          // tool ops: 'mcp:<server>' | 'shell' | 'fs' | 'builtin' | format-specific
    Model            string          // llm ops
    Provider         string          // llm ops: 'anthropic' | 'openai' | 'google' | 'openrouter' | ...
    ProviderAlias    string          // user-defined provider alias (opencode); empty otherwise
    ReasoningKind    string          // reasoning ops only: 'summary' | 'raw'
    ChildSessionNativeID string      // session ops only
    Extras           map[string]any
}

type OpKind string
const (
    OpLLM        OpKind = "llm"          // LLM API call
    OpTool       OpKind = "tool"         // tool invocation (shell, fs, MCP, builtin)
    OpSession    OpKind = "session"      // child session attachment (sub-agent / Task / Agent tool)
    OpReasoning  OpKind = "reasoning"    // model reasoning / chain-of-thought
    OpInternal   OpKind = "internal"     // adapter-internal housekeeping (no UI surface by default)
    OpSystem     OpKind = "system"       // session system ops (init/fin/handoff); UI may show muted
    OpCompaction OpKind = "compaction"   // history compaction event (claude-code, codex)
)

type OpFinalizedEvent struct {
    EventBase
    SessionNativeID  string
    TurnSeq          int
    Seq              int
    Status           string    // 'running' | 'completed' | 'failed' | 'cancelled' | 'truncated'
    ErrorClass       string
    ErrorMessage     string
    EndTs            int64
    TokensIn         int64
    TokensOut        int64
    TokensCacheRead  int64
    TokensCacheWrite int64
    CostUSD          float64
    BytesIn          int64     // request payload size (uncompressed)
    BytesOut         int64     // response payload size
    CharsIn          int64     // when source records chars instead of bytes (ai-agent v2 tool accounting)
    CharsOut         int64
    CtxUsed          int64     // context window tokens consumed (LLM ops)
    CtxMax           int64     // model max context (LLM ops)
}
```

**Op `running` status**: emitted when a `turn_end` (or equivalent) checkpoint observes an op that has not finished yet. The ingester transitions `running` → terminal (`'completed' | 'failed' | 'cancelled' | 'truncated'`) when the final `turn_end` is observed and the op carries a terminal status. UIs treat `running` as in-progress rather than terminal, matching the turn-level semantics above.

Notes on op kinds:

- `OpSystem` covers ai-agent's `system` op kind (init/fin housekeeping) and any adapter-internal lifecycle op the operator may want to filter out of the topology view but keep in the timeline.
- `OpCompaction` is **first-class**: claude-code's `system.subtype="compact_boundary"` and codex's `compacted` / `context_compacted` map here. Extras carry `preTokens`, `postTokens`, `durationMs`, `trigger`. The UI renders compactions as visible breakpoints on the timeline.
- `OpReasoning` with `ReasoningKind='summary'` (model-provided summary) vs `ReasoningKind='raw'` (raw chain-of-thought). codex distinguishes these; other adapters emit one or the other or omit.

### PayloadRefEvent

```go
type PayloadRefEvent struct {
    EventBase
    SessionNativeID string
    TurnSeq         int
    OpSeq           int
    PayloadKind     string   // 'llm_request'|'llm_response'|'llm_sdk_request'|'llm_sdk_response'|'sdk_request'|'sdk_response'|'llm_reasoning'|'reasoning_stream'|'tool_request'|'tool_response'|'log'
    Format          string   // 'http'|'sse'|'json'|'jsonrpc'|'text'|'binary'
    Compression     string   // 'gzip' | ''
    LocationURI     string   // source selector URI, e.g. file://...?json_pointer=...#L<n>
    OriginalBytes   int64    // logical payload bytes when recoverable
    StoredBytes     int64
    SHA256          string   // hex; empty when source does not provide
}
```

## Ingestion Parity Artifact Mapping

`PayloadRefEvent` and the surrounding session/turn/op events are the canonical
representation that the ingestion parity gate verifies. The gate is defined in
`ingestion-parity.md` and compares source manifests against canonical manifests.

Rules:

- A canonical payload artifact MUST identify the exact logical source artifact.
- `PayloadKind` is persisted as the adapter-facing/source-facing value. Adapters
  do not rewrite source aliases merely to satisfy UI rendering.
- Payload kind aliases are normalized by the parity extractor:
  `sdk_request -> llm_sdk_request`, `sdk_response -> llm_sdk_response`, and
  `reasoning_stream -> reasoning_text`.
  A URI pointing only to a containing file is insufficient for parity unless a
  line/field/json-pointer selector or equivalent metadata identifies the exact
  fragment.
- Presenter payload-ref DTOs expose a derived `artifact_class` for UI dispatch:

  | Persisted `PayloadKind` | Derived `artifact_class` |
  |---|---|
  | `llm_request` | `llm_request` |
  | `llm_response` | `llm_response` |
  | `llm_sdk_request`, `sdk_request` | `llm_sdk_request` |
  | `llm_sdk_response`, `sdk_response` | `llm_sdk_response` |
  | `llm_reasoning`, `reasoning_stream` | `reasoning_text` |
  | `tool_request` | `tool_request` |
  | `tool_response` | `tool_response` |
  | `log` | `log` |

  The raw `PayloadKind` remains present for provenance. UI code dispatches
  payload rendering by `artifact_class` and `PayloadKind`, not by payload array
  position.
- Adapter-specific inline transcript selectors may refine the artifact class.
  For claude-code, `llm_response` refs on an LLM op whose JSON pointer matches
  `/message/content/<index>/text` are assistant-message artifacts, and
  `tool_request` refs on the internal `user_input` op whose pointer is
  `/message/content` are user-prompt artifacts.
- `OriginalBytes` MUST be populated for source-available payload artifacts when
  the logical payload bytes are recoverable. `-1` is allowed only when the
  adapter spec documents that the source cannot provide or reconstruct the byte
  length.
- `SHA256` SHOULD be populated from the source when available. If the source
  does not provide a hash but the bytes are recoverable, the parity extractor
  computes the hash during verification.
- User prompts and assistant messages are parity artifact classes independent of
  op taxonomy. They may be represented by existing `kind + name` combinations
  or by future first-class op kinds; the parity spec, not a UI convenience,
  decides whether taxonomy changes are required.
- Synthetic canonical artifacts MUST be marked and documented. They cannot hide
  a missing source artifact.

### LogEntryEvent

Surfaces log lines that the source recorded against a session/turn/op. Used for the per-session "Logs" tab in the UI.

```go
type LogEntryEvent struct {
    EventBase
    SessionNativeID string
    TurnSeq         int       // 0 when not turn-scoped
    OpSeq           int       // 0 when not op-scoped
    Severity        string    // 'DBG' | 'INF' | 'WRN' | 'ERR'
    Source          string    // adapter name or subsystem
    Message         string
    Extras          map[string]any
}
```

For source-visible log artifacts that need exact source parity, adapters may
store selector proof under `Extras.aiViewer.parity`:

```json
{
  "aiViewer": {
    "parity": {
      "nativeArtifactId": "line:42:/payload/message",
      "selectorURI": "file:///sessions/rollout.jsonl#L42",
      "jsonPointer": "/payload/message"
    }
  }
}
```

The canonical parity extractor uses this metadata when present; otherwise it
falls back to the deterministic `log://...` selector derived from the log row.

### SourceProgressEvent

Emitted periodically by an adapter to checkpoint its cursor. The ingester
persists this into `source_progress.cursor` so a restart resumes from there.
Cursor format is opaque JSON — each adapter chooses its own shape (file
offsets, file mtimes, SQLite rowids, etc.). The legacy `sources.cursor` column
is historical and is not read or written by the daemon.

### SourceErrorEvent

Emitted when an adapter hits a parse error. The ingester increments `sources.parse_errors` and writes a `log_entries` row with severity `ERR`. When a `SourceErrorEvent` is converted to a `log_entries` row, the row has `session_id = NULL` and `source_id = <the source id>`; the `CHECK (session_id IS NOT NULL OR source_id IS NOT NULL)` constraint on `log_entries` enforces that one of the two is present. Surfaced in `/api/health` and the Sources UI panel.

## Cross-Format Mapping (Summary)

The full per-format projection lives in each adapter spec. High-level cross-walk:

| Concept | ai-agent v3 | ai-agent v2 | claude-code | codex | opencode |
|---|---|---|---|---|---|
| Native session id | `sessionId` | `opTree.traceId` | `sessionId` (per-jsonl) | `sessionId` (UUIDv7) | `session.id` (Sonyflake) |
| Root session id | `originId` | `originTxnId` | (`sessionId` of root jsonl) | walked via `parent_thread_id` | walked via `parent_id` |
| Parent linkage | `parentSessionId` on child (96.8%) + parent's `ops[]` listing | embedded in parent's opTree | sidecar `.meta.json::toolUseId` ↔ parent's `tool_use.id` | `parent_thread_id` / `forked_from_id` | `session.parent_id` |
| Turn delimiter | explicit `turn_start`/`turn_end` | opTree `steps[]` | synthesized from user→assistant→user pivots | `task_started`/`task_complete` (new) or `turn_context` (old) | each assistant message |
| LLM op | `ops[].kind='llm'` | `op.kind='llm'` | `assistant` record with `usage` | `function_call(name='llm_response')` + `event_msg.token_count` | `part.type='step-start'`/`step-finish` pair |
| Tool op | `ops[].kind='tool'` | `op.kind='tool'` | `assistant.content[].type='tool_use'` + `tool_result` | `function_call(name='shell' / 'apply_patch')` + `event_msg.exec_command_*` | `part.type='tool'` |
| Sub-agent op | `ops[].kind='session'` + `childSessionId` | embedded `op.childSession` | `Agent` tool call + sidecar file | separate rollout file linked via `parent_thread_id` | `task` tool part + child `session.parent_id` |
| Reasoning | `ops[].kind='session'` payload `kind='reasoning_stream'` (declared) | `op.reasoning` field on llm op | not exposed in transcripts | `agent_reasoning` (summary) / `agent_reasoning_raw_content` (raw) | `part.type='reasoning'` |
| Compaction | n/a | n/a (single-snapshot rewrite handles it) | `system.subtype='compact_boundary'` | `compacted` / `context_compacted` | n/a |
| Cost source | `ops[].cost` (when present) | `op.accounting[].cost` | computed via pricing.go (not recorded) | computed via pricing.go (not recorded) | `message.data.cost` |
| Cache tokens | `ops[].tokensCacheRead/Write` (when present) | not recorded | `usage.cache_creation_input_tokens` / `cache_read_input_tokens` (ephemeral 5m/1h decomposition) | recorded in newer rollouts | `message.data.tokens.cache.read/write` |

## Ordering Guarantees

- Within a single session, the adapter MUST emit events in chronological order (turn 1 start before turn 2 start, etc.).
- Across sessions, no ordering guarantee is required. The ingester orders by `Ts`.
- `SourceSeq` is monotonic **per file**, not per source. The ingester records the max seen per source in `source_progress.last_seq` as an observability counter only — it is NOT used for ordering or dedup.

## Idempotency

- `SourceSeq` is NOT a dedup gate: one source aggregates many independently-sequenced files, so a per-source scalar watermark would drop valid events (SOW-0015). Idempotency is enforced at the SQL layer — every writer table uses idempotent upserts keyed on a natural identity, so re-emitted events never duplicate rows. See `ingester.md` §Dedup and Idempotency.
- `*Finalized` events are upserts: if the corresponding `*Started` arrives later, the ingester reconciles (`Ts` from Started, fields from Finalized).
- Re-scanning the same source files MUST NOT produce duplicate rows.
- For snapshot-based sources (ai-agent v2 rewrites the whole file on every snapshot): the adapter computes per-session content deltas using `(seq, contentHash)` cursors so re-reading the same file produces zero new events when nothing changed.

## Adapter Responsibilities (Cross-Cut)

Per ai-viewer's "Adapters do all the heavy lifting" rule, each adapter handles the format-specific quirks below before emitting canonical events. The ingester sees only the clean canonical stream.

- **Cumulative-to-delta conversion** (opencode `step-finish`, others): adapter computes deltas; canonical carries deltas only.
- **Sub-agent linkage**: each adapter implements its source-specific lookup (see Cross-Format Mapping table); canonical carries the resolved `ParentNativeID` / `RootNativeID`.
- **Turn synthesis** (claude-code, codex new format): adapter computes turn boundaries; canonical carries explicit `Seq`.
- **Atomic-rename detection** (ai-agent v2, codex): adapter listens for `RENAME` / `MOVED_TO` events.
- **Append vs rewrite**: adapter chooses byte-offset cursor (v3, codex, claude-code) vs content-hash cursor (v2).
- **Schema version tolerance** (opencode migrations, v2 v1-vs-v2 snapshot shape): adapter queries the source's schema metadata and dynamically projects.

## Why this shape

- **Decouples parsers from SQL.** Adapters tested with a fake `chan Event`.
- **Single ingest path.** One place to dedup, order, enforce invariants.
- **Replay-friendly.** A future "replay" tool can re-emit canonical events from a SQLite dump into a different store.
- **Format-agnostic UI.** The frontend reads canonical SQL rows; it never branches on source format. Adding a 6th adapter never touches the UI.
- **Survives source-side evolution.** When ai-agent ships v4, claude-code adds a new record type, or opencode runs a schema migration, only the adapter changes — canonical events stay stable.

## References

- `internal/canonical/events.go` — Go definitions of every event type (created in SOW-0001 Chunk 1 spec deltas; populated in Chunk 2-onwards).
- `.agents/sow/specs/data-model.md` — SQLite schema; one row per event family.
- `.agents/sow/specs/adapter-*.md` — per-format mapping detail.
- `.agents/sow/done/SOW-0002-20260526-cross-format-data-model-analysis.md` — the analysis that produced this model.
