# Adapter: codex (OpenAI Codex CLI)

## Status

**Phase 2 target.** Specification is evidence-driven from the OpenAI Codex repository (`openai/codex @ 8a94430b`, May 2026) and from 2,585 real rollout files on the operator's workstation (~/.codex/sessions/). The earlier preliminary sketch overstated the layout (claimed `.json` files in sharded subdirs); both claims are corrected here.

## Source Format

### Filesystem Layout (current)

The Codex CLI stores one rollout file per conversation under the user's `codex_home` directory (default `~/.codex/`):

```
~/.codex/
├── sessions/
│   └── YYYY/MM/DD/
│       └── rollout-YYYY-MM-DDTHH-MM-SS-<ThreadId>.jsonl    # one per session (current format)
│   └── rollout-YYYY-MM-DD-<uuid>.json                       # legacy flat layout (see below)
├── archived_sessions/                                       # session archive (out of scope for ingest)
├── session_index.jsonl                                      # rename history (out of scope for ingest)
├── state_5.sqlite{,-shm,-wal}                               # codex internal state (out of scope)
├── logs_2.sqlite{,-shm,-wal}                                # codex internal logs (out of scope)
└── history.json, history.jsonl                              # raw shell history (out of scope, sensitive)
```

The adapter's only input is `sessions/YYYY/MM/DD/rollout-*.jsonl`. The other artifacts are explicitly out of scope for the codex adapter.

References (`openai/codex @ 8a94430b`):

- `codex-rs/rollout/src/lib.rs:23` — `pub const SESSIONS_SUBDIR: &str = "sessions";`
- `codex-rs/rollout/src/recorder.rs:1325-1354` — `precompute_log_file_info` — builds the `YYYY/MM/DD/rollout-YYYY-MM-DDTHH-MM-SS-<id>.jsonl` path. Uses *local* time for both the directory shards and the filename timestamp.
- `codex-rs/rollout/src/list.rs:399` — comment confirms layout: "Directory layout: `~/.codex/sessions/YYYY/MM/DD/rollout-YYYY-MM-DDThh-mm-ss-<uuid>.jsonl`".
- `codex-rs/rollout/src/list.rs:898,918,932` — file discovery filters strictly on `starts_with("rollout-") && ends_with(".jsonl")`.

Filename time component uses `-` as the separator between hour/minute/second (filesystem safety): see recorder comment at line 1338-1339. ThreadId is a UUIDv7 (time-ordered).

### Legacy `.json` layout (pre-mid-2025)

Real workstation observation: 19 files exist directly under `~/.codex/sessions/` with names `rollout-YYYY-MM-DD-<uuid>.json` (no time component, no per-day sharding, `.json` extension, dating June-July 2025). Their content is a single JSON object:

```json
{
  "session": {"timestamp": "...Z", "id": "<uuid>", "instructions": ""},
  "items": [{"type": "message"|"reasoning"|"local_shell_call"|"local_shell_call_output"|"function_call_output", ...}, ...]
}
```

Upstream Codex no longer produces or reads this format: `list.rs:898` rejects any file whose name does not end in `.jsonl`. The adapter MAY support legacy `.json` files behind a `legacy_json_format=true` flag (off by default for v1). Phase 2 v1 scope: ignore them and log one informational `SourceError` per file the first time they are seen, then suppress.

Rationale for deferring legacy: (a) upstream considers them obsolete; (b) their schema is a strict subset of the modern `ResponseItem` enum minus the wrapping `RolloutLine` envelope; (c) operators with old codex installs are rare and a follow-up SOW can add support if asked.

### Authoritative Wire Format (current)

Every line is one JSON object conforming to `codex_protocol::protocol::RolloutLine` (`codex-rs/protocol/src/protocol.rs:2849-2854`):

```rust
struct RolloutLine {
    timestamp: String,       // RFC3339 UTC, millisecond precision, e.g. "2025-11-20T16:59:09.857Z"
    #[serde(flatten)]
    item: RolloutItem,
}

enum RolloutItem {           // serde tag="type", content="payload", rename_all="snake_case"
    SessionMeta(SessionMetaLine),
    ResponseItem(ResponseItem),
    Compacted(CompactedItem),
    TurnContext(TurnContextItem),
    EventMsg(EventMsg),
}
```

On disk:

```json
{"timestamp": "...", "type": "session_meta",  "payload": {...}}
{"timestamp": "...", "type": "response_item", "payload": {...}}
{"timestamp": "...", "type": "event_msg",     "payload": {...}}
{"timestamp": "...", "type": "turn_context",  "payload": {...}}
{"timestamp": "...", "type": "compacted",     "payload": {...}}
```

Observed line-type distribution across sampled real files (10 random rollouts):

| Line `type` | Always present? | Typical frequency |
|---|---|---|
| `session_meta`   | yes, exactly once, as **line 1** | 1 |
| `turn_context`   | per turn (1-N) | tens to hundreds |
| `response_item`  | per model output | hundreds to thousands |
| `event_msg`      | per UI/telemetry event | hundreds to thousands |
| `compacted`      | 0-N when history was compacted | rare |

## Record / Object Contract

### `session_meta` payload (`SessionMetaLine` flattened on `SessionMeta` + `git`)

References: `codex-rs/protocol/src/protocol.rs:2642-2703`.

| Field | Type | Notes |
|---|---|---|
| `id` | `ThreadId` (UUIDv7 string) | canonical session id |
| `forked_from_id` | string\|absent | parent ThreadId when this rollout is a fork |
| `timestamp` | RFC3339Z string | session start, UTC (note: filename uses local time) |
| `cwd` | string path | working directory when codex was launched |
| `originator` | string | one of `codex_cli_rs`, `codex_exec`, `codex-tui`, free-form |
| `cli_version` | string | semver of the codex binary; observed range 0.61.0 → 0.125.0 |
| `source` | enum / object | see "Source" below |
| `thread_source` | `user`\|`subagent`\|`memory_consolidation`\|absent | classification |
| `agent_nickname`, `agent_role`, `agent_path` | string\|absent | sub-agent metadata |
| `model_provider` | string\|null | e.g. `"openai"` |
| `base_instructions` | object\|null | `{text: "..."}` system-prompt text — **sensitive**, may include operator's name |
| `dynamic_tools` | array\|null | tool registration snapshot |
| `memory_mode` | string\|null | |
| `git` | object\|null | `{commit_hash, branch, repository_url}` at session start |

`SessionMeta.source` is an enum with shapes (`codex-rs/protocol/src/protocol.rs:2441-2517`):

- `"cli"` | `"vscode"` | `"exec"` | `"mcp"` | `"unknown"` — strings
- `{"custom": "<name>"}`
- `{"internal": "memory_consolidation"}`
- `{"subagent": "review"|"compact"|"memory_consolidation"|{"thread_spawn": {parent_thread_id, depth, agent_path, agent_nickname, agent_role}}}`
- `{"other": "<name>"}` — unknown variants fall through (forward-compat)

Observed in real files: 164/200 `"exec"`, 28/200 `"cli"`, plus a handful of `{"subagent": {"thread_spawn": {...}}}` (sub-agent spawns).

### `turn_context` payload (`TurnContextItem`)

References: `codex-rs/protocol/src/protocol.rs:2745-2776`. Emitted once per turn and again after mid-turn compaction.

| Field | Type | Notes |
|---|---|---|
| `turn_id` | string\|absent | correlation key with `task_started`/`task_complete`/`exec_command_end` |
| `cwd` | string | may differ from session cwd if the model `cd`d |
| `current_date`, `timezone` | string\|absent | injected into model context |
| `approval_policy` | enum | `untrusted`\|`on-failure`\|`on-request`\|`never` |
| `sandbox_policy` | object | Object whose `type` is one of `danger-full-access`, `read-only`, or `workspace-write`; observed all three in real files (proportions: 68% workspace-write, 30% danger-full-access, 2% read-only across 50 sampled sessions) |
| `permission_profile` | object\|absent | finer-grained replacement of `sandbox_policy` in newer versions |
| `network` | `{allowed_domains, denied_domains}`\|absent | |
| `file_system_sandbox_policy` | object\|absent | newer field |
| `model` | string | active model for this turn (e.g. `"gpt-5.1-codex-max"`, `"gpt-5.5"`) |
| `personality` | object\|absent | |
| `collaboration_mode` | enum\|absent | |
| `realtime_active` | bool\|absent | |
| `effort` | enum\|absent | `low`\|`medium`\|`high`\|`xhigh` |
| `summary` | enum | legacy compatibility field (deprecated but still written) |

### `response_item` payload (`ResponseItem`)

References: `codex-rs/protocol/src/models.rs:750-903`. Tagged union; the variants that reach disk (filtered by `codex-rs/rollout/src/policy.rs:67-85`):

| `payload.type` | Variant | Key fields |
|---|---|---|
| `message` | `Message` | `role` (user/assistant/system/developer), `content[]` items for `input_text`, `output_text`, or `input_image` with text or image URLs, optional `phase` (`commentary` or `final_answer`) |
| `reasoning` | `Reasoning` | `summary[]` (visible reasoning summaries), `content[]` (raw CoT, model-dependent, often null), `encrypted_content` (opaque base64, for OpenAI Responses API resume) |
| `local_shell_call` | `LocalShellCall` | LEGACY — only in old `.json` files; not used in current rollouts |
| `function_call` | `FunctionCall` | `name` (tool name, e.g. `shell_command`, `apply_patch`, `read`), `namespace` (optional), `arguments` (JSON-encoded **string**, not parsed object), `call_id` |
| `function_call_output` | `FunctionCallOutput` | `call_id`, `output` (string OR `{content: string\|content_items}`) |
| `custom_tool_call` | `CustomToolCall` | `call_id`, `name`, `input` (string), `status` |
| `custom_tool_call_output` | `CustomToolCallOutput` | `call_id`, `output` (same shape as function_call_output) |
| `tool_search_call`, `tool_search_output` | tool-search subsystem | |
| `web_search_call` | `WebSearchCall` | `call_id`, `status`, `action` (e.g. `{type:"search", query}`) |
| `image_generation_call` | `ImageGenerationCall` | `id`, `status`, `revised_prompt`, `result` — **0 real files** (forward-compat only) |
| `compaction` | `Compaction` | `encrypted_content` (opaque) — **0 real files** (forward-compat only) |
| `context_compaction` | `ContextCompaction` | `encrypted_content`\|null — **0 real files** (forward-compat only). NOTE: distinct from the real `event_msg.context_compacted` bare marker (rule #20), which is the actual compaction companion that occurs in the corpus. |
| `ghost_snapshot` | `Other` (catch-all) | observed in real files; not in the persisted-allowlist enum but slips through as `Other`; older lines stripped during reconstruction (`recorder.rs:836,926`) |

The `ResponseItem` enum has `#[serde(other)] Other` (`models.rs:901`), so any unknown variant deserializes successfully — the adapter MUST be forgiving and emit `LogEntry` at most once per unknown variant.

### `event_msg` payload (`EventMsg`)

References: `codex-rs/protocol/src/protocol.rs:1137-1328`, persistence filter `codex-rs/rollout/src/policy.rs:135-220`.

Two persistence modes exist upstream (`Limited` default, `Extended` opt-in). The adapter MUST handle both — operators on different cli versions write different subsets.

**Limited mode (always persisted):**

| `payload.type` | Struct | Key fields |
|---|---|---|
| `user_message` | `UserMessageEvent` | `message`, `images[]`, `image_details[]`, `local_images[]`, `text_elements[]` |
| `agent_message` | `AgentMessageEvent` | `message`, `phase`, `memory_citation` |
| `agent_reasoning` | `AgentReasoningEvent` | `text` (visible reasoning summary) |
| `agent_reasoning_raw_content` | `AgentReasoningRawContentEvent` | `text` (full CoT, model-dependent) |
| `patch_apply_end` | `PatchApplyEndEvent` | `call_id`, `turn_id`, `stdout`, `stderr`, `success`, `changes` (map of path→FileChange), `status` |
| `token_count` | `TokenCountEvent` | `info: {total_token_usage, last_token_usage, model_context_window}`, `rate_limits` |
| `thread_goal_updated` | `ThreadGoalUpdatedEvent` | |
| `context_compacted` | `ContextCompactedEvent` | unit-struct (zero fields) — note: distinct from the `response_item.context_compaction` variant |
| `entered_review_mode`, `exited_review_mode` | review events | |
| `mcp_tool_call_end` | `McpToolCallEndEvent` | `call_id`, `invocation: {server, tool, arguments}`, `duration`, `result` (Result<CallToolResult, String>) |
| `thread_rolled_back` | `ThreadRolledBackEvent` | `num_turns` |
| `turn_aborted` | `TurnAbortedEvent` | `turn_id`, `reason` (`interrupted`\|`replaced`\|`review_ended`\|`budget_limited`), `completed_at`, `duration_ms` |
| `task_started` (alias `turn_started`) | `TurnStartedEvent` | `turn_id`, `trace_id`, `started_at` (unix-seconds), `model_context_window`, `collaboration_mode_kind` |
| `task_complete` (alias `turn_complete`) | `TurnCompleteEvent` | `turn_id`, `last_agent_message`, `completed_at`, `duration_ms`, `time_to_first_token_ms` |
| `web_search_end` | `WebSearchEndEvent` | `call_id`, `query`, `action` |
| `image_generation_end` | `ImageGenerationEndEvent` | |
| `item_completed` (Plan items only) | `ItemCompletedEvent` | sometimes persisted |

**Extended mode (additionally persisted when configured):**

| `payload.type` | Struct | Notes |
|---|---|---|
| `error` | `ErrorEvent` | |
| `guardian_assessment` | | |
| `exec_command_end` | `ExecCommandEndEvent` | `call_id`, `turn_id`, `command[]`, `cwd`, `parsed_cmd`, `source`, `stdout`/`stderr`/`aggregated_output` (truncated to 10000 bytes), `exit_code`, `duration`, `formatted_output`, `status`. **The stdout/stderr/formatted_output fields are cleared on persistence** (`policy.rs:51-59`); only `aggregated_output` (truncated middle) survives. |
| `view_image_tool_call` | | |
| `collab_*_end` | | sub-agent collab lifecycle ends |
| `dynamic_tool_call_request`, `dynamic_tool_call_response` | | |

All other `EventMsg` variants (deltas, begins, approval requests, MCP startup, etc.) are NOT persisted (`policy.rs:175-219`).

### `compacted` payload (`CompactedItem`)

```json
{"message": "<summary>", "replacement_history": [<ResponseItem>...]\|null}
```

Marks a context-window compaction: prior history was summarized into `message` (and optionally replaced by `replacement_history`). Codex strips legacy "ghost_snapshot" wrapping during read (`recorder.rs:836,926`).

### Versioning / Forward Compatibility

Codex does not include a schema-version field on the rollout file. The contract is "schema is the union of what `codex_protocol` crate accepts, evolved per CLI release." Real workstation files span CLI 0.61.0 → 0.125.0 — schema additions are common, removals rare. The adapter MUST:

- Treat unknown `RolloutItem.type` strings as `LogEntry` (warn once per unknown variant per session).
- Treat unknown nested `payload.type` strings the same way.
- Tolerate unknown enum variants in `source`, `sandbox_policy.type`, `approval_policy`, etc. (Rust upstream uses `#[serde(other)]` catch-alls; Go decoder must do the same).
- Decode `cli_version` and `originator` from `session_meta` and surface them in `Extras` so the UI can show "captured by codex 0.93.0 (codex_exec)".

## Atomicity & Write Pattern

References: `codex-rs/rollout/src/recorder.rs:1346-1369, 1610-1620, 1622-1654`.

- Files are opened with `OpenOptions::new().append(true).create(true)`. **Pure append, no temp-file rename, no atomic write.**
- Each `RolloutItem` is serialized with `serde_json::to_string`, suffixed with `'\n'`, then written via `file.write_all(json_bytes).await; file.flush().await`. `flush` here flushes the userspace `BufWriter`/tokio buffer — it does NOT call `fsync`/`sync_data`. The OS page cache holds the bytes until kernel flush.
- Filename is constant for the lifetime of the session (created on first write; reused on every subsequent append).
- The recorder also exposes `append_rollout_item_to_path` (`recorder.rs:1610`) for metadata-only updates after a session ends — also append-only.

Implications for ai-viewer:

- **Append-only JSONL with byte-offset tail is the correct watch strategy.** No RENAME events to chase.
- **A `\n`-terminated line is the unit.** If the watcher reads a partial trailing line (no final `\n`), it MUST seek back to the last `\n` and wait for the next write event.
- **Concurrent crash safety**: because there is no fsync, an OS crash can lose the most recently written bytes. From the adapter's view, this just looks like a shorter file — the cursor resumes after the truncation point on next start.
- **No file is ever rewritten in place.** Bytes once written are durable for ingest purposes; if the cursor offset equals the file size, all known content is consumed.

## Watch Strategy

- One `fsnotify.Watcher` registered on `~/.codex/sessions/` recursively (Go fsnotify doesn't recurse natively — the adapter walks the tree at startup and `Add`s every existing `YYYY`, `YYYY/MM`, `YYYY/MM/DD` directory it finds).
- React to `fsnotify.Create` on those parent directories: add the new child directory (and any deeper child created within it) to the watcher.
- React to `fsnotify.Create`, `fsnotify.Write` on `*.jsonl` files inside `YYYY/MM/DD/`:
  - On `Create`: register the new file in the cursor at offset=0; immediate tail read.
  - On `Write`: tail-read from cursor offset to current file size; parse complete lines; advance cursor.
- Ignore: any file NOT matching `^rollout-.*\.jsonl$` under `sessions/YYYY/MM/DD/` (this excludes the legacy flat `.json` files in `sessions/` root, unless legacy mode is enabled).
- Ignore: `archived_sessions/`, `session_index.jsonl`, `state_*.sqlite*`, `logs_*.sqlite*`, `history*`.
- A periodic full sweep (every 5 seconds, matching the other tailing adapters)
  covers missed inotify events on slow filesystems and adds new date directories
  that came into existence while no other event fired in their parent.

## Cursor

Per-file byte offset plus discovery hints. JSON shape:

```json
{
  "version": 1,
  "files": {
    "2025/11/20/rollout-2025-11-20T18-59-09-019aa234-a2a1-75c3-a9bf-d8425e1785f5.jsonl": {
      "offset": 2917322,
      "size": 2917322,
      "mtime_us": 1763664584000000,
      "last_ts_us": 1763664584000000,
      "eof_finalized_size": 2917322
    }
  },
  "legacy_json": {
    "rollout-2025-06-26-5556f03d-348c-4463-987c-053ccd0b1df5.json": {
      "ingested": true
    }
  }
}
```

Path keys are RELATIVE to the configured `--codex-home` (default `~/.codex`) — the cursor survives a home-directory move.

Per-file fields:

- `offset` — byte offset of the next unread byte; always at a line start (trailing partial lines held back).
- `size` — file size when `offset` was recorded; drives truncation detection.
- `mtime_us` / `last_ts_us` — observability + the staleness heuristic (rule #23).
- `eof_finalized_size` — DURABLE marker of the file size at which an EOF-finalize already fired (the synthetic close of a hanging turn at full-read EOF: an OLD-format turn closed `completed`, or a stale NEW-format turn closed `failed/incomplete` + its `SessionFinalized`). The mapper's own in-memory guard is rebuilt on every replay-from-0, so the marker must persist in the cursor: without it, an unchanged rescan/restart would re-fire the synthetic `TurnFinalized`/`SessionFinalized`. A metadata-only `session_meta` append (recorder.rs:1615) GROWS `size` past this marker but carries no new turn content, so the scanner ALSO suppresses the re-fire when a grown rescan emitted no new content, then advances the marker to the new size. A genuine new-turn append (always emitting at least a `TurnStarted`) re-opens and closes normally. `0`/absent ⇒ no EOF-finalize has fired yet.

Restart logic:

- For each tracked file: if `current_size >= cursor.offset`, resume from `cursor.offset`.
- If `current_size < cursor.offset`: file was truncated (codex never truncates, so this means manual operator deletion + recreation) — emit `SourceError`, reset to 0, full re-scan (also clears `eof_finalized_size`).
- For new files (not in cursor): start at 0, full scan.
- For files no longer present on disk: leave cursor entry; never re-emit. Optional GC after N days.

## Mapping to Canonical Events

Codex rollout files emit fine-grained `RolloutItem` records but do NOT carry pre-aggregated turn/op summaries (unlike ai-agent v3 which emits a `turn_end` record with rollup totals). The adapter therefore acts as a state machine over the line stream:

### Per-file state machine

1. **`session_meta` (always first line):**
   - Emit `SessionStartedEvent` with:
     - `NativeID = payload.id`
     - `ParentNativeID = payload.forked_from_id` (when present), OR `source.subagent.thread_spawn.parent_thread_id` (when present), else empty
     - `Kind`: `'root'` when `source` is `cli`/`vscode`/`exec`/`mcp`/`unknown`/`custom`; `'sub_agent'` when `source` is `subagent` or `thread_source=="subagent"`; `'tool_internal'` when `source` is `internal`
     - `AgentName = payload.agent_nickname` or `payload.agent_role` (when sub-agent); else `"codex:" + payload.originator`
     - `Model`: unknown at this point (left empty; learned from first `turn_context` and emitted as `SessionUpdatedEvent`)
     - `Extras`: `{cli_version, originator, source, cwd, git, sandbox: (deferred), memory_mode}`
   - Emit `SourceProgress` after handshake.

2. **`turn_context`:**
   - First time seen for a `turn_id`: open a new turn.
   - Emit `TurnStartedEvent(SessionNativeID, Seq=monotonic-from-1, Ts=line.timestamp)` if no prior `task_started` for this `turn_id`.
   - If `model` is not yet known on the session, emit `SessionUpdatedEvent(Model = payload.model, Provider = "openai" assumed)`.
   - Store `turn_id → turn_seq` in adapter state.

3. **`event_msg` payload `task_started` (alias `turn_started`):**
   - Open a turn for `payload.turn_id` if not already open (some sessions have `turn_context` before `task_started`; some have `task_started` first — handle both).
   - Use `started_at` (unix seconds) for canonical `Ts` if newer than the wire `timestamp`.
   - Stash `model_context_window` → applied at TurnFinalized.

4. **`event_msg` payload `task_complete`:**
   - Emit `TurnFinalizedEvent(turn_seq, Status="completed", EndTs=completed_at_us)` with `TokensIn`/`TokensOut` set from the turn's token rollup (see rule #17 and "Token accounting nuance" below): the **sum of the per-call `last_token_usage`** over the `token_count` events attributed to this turn — **not** a delta of the cumulative `total_token_usage`. (`last_token_usage` is the per-call field; the cumulative `total_token_usage` feeds `OpFinalized.CtxUsed` only, never per-turn `TokensIn/Out`.)
   - Emit `OpFinalizedEvent` for each held-open op tied to this turn (function_call without matching output, etc.) with Status="completed" inferred or "unknown" if no output ever arrived.

5. **`event_msg` payload `turn_aborted`:**
   - Emit `TurnFinalizedEvent(turn_seq, Status="failed", ErrorClass=reason, EndTs)`.
   - For `reason="interrupted"` set ErrorClass="user_interrupt"; `"replaced"` → `"replaced"`; `"review_ended"` → `"review_ended"`; `"budget_limited"` → `"rate_limit"`.

6. **`response_item` payload `message` role=user:**
   - User input within a turn. Emit `OpStartedEvent` + `OpFinalizedEvent` with Kind=`internal`, Name=`user_input`. Bodies stored as `PayloadRefEvent` (PayloadKind=`user_input`, Format=`json`, LocationURI=`file://...#L<line>`).
   - Some sessions duplicate user input as both `response_item.message(role=user)` and `event_msg.user_message`. Deduplicate by emitting only on the first occurrence (preferring `event_msg.user_message` if both arrive — it's the canonical UI event).

7. **`response_item` payload `message` role=assistant:**
   - Emit OpStarted+OpFinalized Kind=`llm`, Name=`message`, Model=current turn model, Provider=`openai`. Body in PayloadRefEvent.
   - When `phase=final_answer`, also emit a `LogEntry` so the UI can flag "this is the final response".

8. **`response_item` payload `reasoning` OR `event_msg` payload `agent_reasoning`/`agent_reasoning_raw_content`:**
   - Emit OpStarted+OpFinalized Kind=`reasoning`, Name=`reasoning`. Body in PayloadRefEvent (Format=`text` for summary lines, Format=`json` for full ResponseItem).
   - **Both forms exist in the same file** (the `response_item` form is the durable model state; the `event_msg` form is the UI-displayed summary; they may carry different text). Adapter emits only the `response_item` form to canonical; uses `event_msg` only to surface in `LogEntry` for the UI "reasoning panel". Otherwise the UI sees duplicate reasoning ops.

9. **`response_item` payload `function_call` (+ matching `function_call_output`):**
   - Emit OpStarted at `function_call` line: Kind=`tool`, Name=`payload.name`, ToolNamespace=`payload.namespace` (or inferred — see below), Extras={call_id, arguments_raw}.
   - Match `function_call_output` by `call_id` to emit OpFinalized with EndTs=output's line timestamp, Status derived (success if output not an error, else failed).
   - PayloadRefs: `tool_request` (Format=`json`, the arguments string) and `tool_response` (Format=`json`).
   - **Tool namespace heuristic** (codex tools are not pre-namespaced on disk):
     - `name == "shell" || name == "shell_command" || name starts with "exec"` → `tool_namespace = "shell"`
     - `name == "apply_patch"` → `tool_namespace = "fs"`
     - `name == "read" || name == "write" || name == "edit" || name == "list_dir"` → `tool_namespace = "fs"`
     - `name == "view_image"` → `tool_namespace = "fs"`
     - When the call sits inside an `event_msg.mcp_tool_call_end.invocation.server` correlation → `tool_namespace = "mcp:" + invocation.server`
     - All others → `tool_namespace = "custom"`

10. **`response_item` payload `custom_tool_call` / `custom_tool_call_output`:**
    - Same as function_call/output. `tool_namespace = "custom"`.

11. **`response_item` payload `web_search_call` / `event_msg.web_search_end`:**
    - Single op: Kind=`tool`, Name=`web_search`, ToolNamespace=`web`. **`web_search_call` carries NEITHER `id` NOR `call_id`** (real corpus: 483 files, no call-side correlation key), so it CANNOT pair by key. Pair the `response_item` (start) POSITIONALLY with the next `event_msg.web_search_end` in the same turn — track the most-recent open web_search op per turn and finalize it on the next `web_search_end` (which DOES carry a `call_id`, but in a different correlation space). The end carries `query` and `action`, merged onto the op's Extras via an OpStarted re-emit (F7).

12. **`response_item` payload `image_generation_call` / `event_msg.image_generation_end`:**
    - Op: Kind=`tool`, Name=`image_generation`, ToolNamespace=`media`. **UNOBSERVED: 0 real files for both `image_generation_call` and `image_generation_end`** — this mapping is forward-compat only and has no fixture coverage (no real data exists to sanitize). `image_generation_call` would use `id` (not `call_id`); the code keeps the path but does not pair beyond the active-turn fallback (F7).

13. **`response_item` payload `local_shell_call` / `local_shell_call_output`:**
    - LEGACY ONLY (does not occur in modern `.jsonl`). When ingesting legacy `.json` files: Kind=`tool`, Name=`shell`, ToolNamespace=`shell`.

14. **`event_msg` payload `exec_command_end`:**
    - Used for telemetry enrichment — the matching `function_call`/`function_call_output` pair carries the same `call_id` and produces the op. The `exec_command_end` adds: parsed_cmd, exit_code, duration, source. Adapter merges these into the op's Extras: `{exec_exit_code, exec_duration_ms, exec_cwd, exec_source}`. The `duration` is a Rust `Duration` object `{secs, nanos}` (real corpus: always this shape) normalized to integer `exec_duration_ms = secs*1000 + nanos/1e6`. **Do not** emit a second op.
    - The `exit_code` is AUTHORITATIVE for the op's terminal status, ORDER-INDEPENDENTLY (G1, rule #5): non-zero `exit_code` → op `failed` / ErrorClass `command_failed`; `exit_code` 0 → `completed`. When `exec_command_end` arrives BEFORE the `function_call_output` (~68-85%), the exec status is stashed and WINS over the output-string heuristic at finalize. When it arrives AFTER (output-first, ~15-32%), the adapter emits a CORRECTING `OpFinalized` on the op's `(turn,seq)` so a non-zero exit overrides a provisionally-`completed` op. A blanked `aggregated_output` is NOT an error.
    - Note: `aggregated_output` is truncated to 10 KB at the source; `stdout`/`stderr`/`formatted_output` are blanked (`policy.rs:51-59`). Adapter cannot recover full output.

15. **`event_msg` payload `mcp_tool_call_end`:**
    - For MCP-routed function calls (which appear ALSO as `function_call`/`function_call_output` with `name = "<server>.<tool>"` or via the `namespace` field). The `invocation` field gives canonical (server, tool). Use it to set `tool_namespace = "mcp:" + server` and `name = tool` on the matching op.

16. **`event_msg` payload `patch_apply_end`:**
    - Telemetry for an `apply_patch` `function_call`. Merge `success`, `status` into the op's Extras as `{patch_success, patch_status}`. Set Op Status accordingly (success=false → `failed` / ErrorClass `patch_failed`). ORDER-INDEPENDENT, mirroring exec (G2): an `apply_patch` op still open is finalized here with the extras merged; an already-finalized op (output-first) gets the extras re-emitted plus a correcting `OpFinalized` on its `(turn,seq)`.

17. **`event_msg` payload `token_count`:**
    - Stream of token accounting snapshots. Each carries cumulative `total_token_usage` and the per-call `last_token_usage`, plus optional `model_context_window`.
    - Per-turn rollup: at TurnFinalized, set `TokensIn = sum(last_token_usage.input_tokens − last_token_usage.cached_input_tokens)` (clamped ≥0) over `token_count` events between this turn's `task_started` and `task_complete`. codex's `input_tokens` is the TOTAL prompt (cached + uncached) — upstream `non_cached_input() = input_tokens − cached_input_tokens` (`codex-rs/protocol/src/protocol.rs`) — so the cached portion is subtracted to keep `TokensIn` FRESH per the canonical token contract (`canonical-events.md`), and the cached portion is recorded in `TokensCacheRead`. `TokensOut = sum(last_token_usage.output_tokens)`. (SOW-0029 fix: previously this summed `input_tokens` directly, folding cache into `TokensIn` and double-charging it in the pricer.)
    - Also derive `OpFinalized.CtxUsed = total_token_usage.total_tokens` and `OpFinalized.CtxMax = model_context_window` on the LLM op that immediately precedes the `token_count` line.
    - When `model_context_window` is present, update `catalog_models.ctx_max` via Extras propagation.

18. **`event_msg` payload `user_message`:**
    - Preferred user-input source (see #6). Emit `OpStarted`+`OpFinalized` Kind=`internal`, Name=`user_input`. If `local_images` / `images` carry paths/URLs, store them in Extras.

19. **`event_msg` payload `agent_message`:**
    - Companion to the assistant `response_item.message`. Adapter emits only the `response_item` (see #7); uses `event_msg.agent_message` only to populate `TurnFinalized.LastAgentMessage` Extras (for the UI "latest answer" preview).

20. **Top-level `compacted` line AND its companion `event_msg.context_compacted`:**
    - These are TWO representations of ONE compaction, written as ADJACENT lines with IDENTICAL timestamps (real workstation corpus: 293 `compacted` + 258 `event_msg.context_compacted`). The top-level `compacted` is data-bearing (`{message, replacement_history}`); the `event_msg.context_compacted` is a bare `{type}` marker. Emit exactly ONE Op Kind=`compaction`, Name=`compaction`, Extras={`replacement_history_size`, `message_preview`} from the data-bearing `compacted` line; SUPPRESS the adjacent `event_msg.context_compacted` so it does NOT produce a second op. A lone `event_msg.context_compacted` with no preceding `compacted` (defensive) emits the op itself. The body goes to PayloadRef Format=`json`. (Note: `response_item.compaction` / `response_item.context_compaction` have ZERO real files — they are forward-compat only; if a future CLI emits one it converges on the same OpCompaction.)

21. **`event_msg` payload `ghost_snapshot`:**
    - Codex internal book-keeping for resume — strip and ignore (`recorder.rs:836,926` shows upstream strips them during read).

22. **`event_msg` payloads `task_started` / `task_complete` without a `turn_context`:**
    - Some older sessions (e.g. cli 0.61.0) have `turn_context` only, no task_started/complete. Newer (>= ~0.93.0) emit both. Adapter must treat either as the turn boundary signal — whichever arrives first opens the turn, whichever last closes it.

23. **EOF without any `task_complete` or `turn_aborted` for the most recent turn:**
    The EOF-finalize splits on the most-recent open turn's FORMAT (whether a
    `task_started` was ever seen for it) — this is the F1 fix; an earlier draft
    treated all formats identically and mislabeled the large pure-old-format
    corpus as crashes:
    - **OLD-format turn (turn_context-only, no `task_started` — cli < ~0.93):**
      close `TurnFinalizedEvent(Status="completed")` at EOF, REGARDLESS of
      staleness (edge #3 "close at EOF"; ~38% of the real corpus is pure
      old-format ending cleanly with no completion marker). `EndTs` is the turn's
      **last-activity timestamp** (the max record timestamp in the file — for the
      most-recent open turn this IS its last activity), NOT the file mtime /
      wall-clock: a clean old-format turn ended when its last record was written,
      and using the live mtime makes the emitted stream non-deterministic across
      runs (CI-flaky golden) and semantically wrong (G6). NO
      `SessionFinalizedEvent` — codex has no per-session terminal signal (C#3);
      the session stays `running`.
    - **NEW-format turn (a `task_started` opened it, no `task_complete`):** the
      turn is still in-flight on a fresh file — keep it open and ingest more on
      the next event. Only when the file mtime is stale (≥ 1 hour) is it treated
      as a crash: emit `TurnFinalizedEvent(Status="failed", ErrorClass="incomplete")`
      and `SessionFinalizedEvent(Status="failed", ErrorClass="incomplete")`.
      (`turn_aborted` upstream uses similar logic when codex restarts on a crashed
      session.)
    - **No open turn (clean end, or none opened):** nothing — stays `running`.

24. **No `session_meta` line ever seen (corrupt file or pre-write crash):**
    - Emit `SourceError` and skip the file. Cursor.offset stays 0 so it is retried on next CREATE-style event.

### Tabular summary

| Source line | Canonical events emitted |
|---|---|
| `session_meta` | `SessionStartedEvent` (+`SessionUpdatedEvent` when model first known) |
| `turn_context` (new turn_id) | `TurnStartedEvent` (idempotent with task_started) |
| `event_msg.task_started` | `TurnStartedEvent` (idempotent) |
| `event_msg.task_complete` | `TurnFinalizedEvent(completed)` |
| `event_msg.turn_aborted` | `TurnFinalizedEvent(failed)` |
| `response_item.message` role=user | `OpStarted/Finalized` Kind=internal Name=user_input + PayloadRef |
| `response_item.message` role=assistant | `OpStarted/Finalized` Kind=llm + PayloadRef |
| `response_item.reasoning` | `OpStarted/Finalized` Kind=reasoning + PayloadRef |
| `response_item.function_call` / `_output` (paired) | `OpStarted` + `OpFinalized` Kind=tool + 2× PayloadRef |
| `response_item.custom_tool_call` / `_output` | same |
| `response_item.web_search_call` + `event_msg.web_search_end` | one Op Kind=tool Name=web_search |
| `response_item.image_generation_call` + `event_msg.image_generation_end` | one Op Kind=tool Name=image_generation |
| `event_msg.exec_command_end` | merge into existing tool op (Extras) |
| `event_msg.mcp_tool_call_end` | merge into existing tool op + ToolNamespace=mcp:server |
| `event_msg.patch_apply_end` | merge into existing apply_patch op |
| `event_msg.token_count` | turn rollups + LLM op ctx_used/ctx_max |
| `event_msg.user_message` | dedup with response_item.message(user); use as canonical user op |
| `event_msg.agent_message` | dedup with response_item.message(assistant); populate TurnFinalized.LastAgentMessage |
| `compacted` line (+ adjacent `event_msg.context_compacted` companion, suppressed) | one Op Kind=compaction Name=compaction |
| lone `event_msg.context_compacted` (no preceding `compacted`) | one Op Kind=compaction Name=compaction (defensive) |
| `response_item.compaction` / `response_item.context_compaction` | forward-compat only (0 real files); converges on one OpCompaction if ever emitted |
| EOF, OLD-format open turn (turn_context-only, no task_started) | `TurnFinalizedEvent(completed)` at EOF regardless of staleness, `EndTs` = turn's last-activity ts (deterministic, NOT mtime — G6); **no `SessionFinalizedEvent`** (F1) |
| EOF, NEW-format open turn (saw task_started), file mtime-stale ≥ 1 h | synthetic `TurnFinalizedEvent(failed,incomplete)` + `SessionFinalizedEvent(failed,incomplete)` |
| EOF, NEW-format open turn, file FRESH (< 1 h) | turn stays open (still in-flight); no finalize (F1) |
| EOF clean (most recent event is task_complete / no open turn) | **no `SessionFinalizedEvent`** — session stays `running` (codex has no per-session terminal signal; rollouts are resumable and metadata-appendable per `recorder.rs:1610`). UI uses `last_activity_ts` for staleness, identical to claude-code. |
| unknown `type` or unknown `payload.type` | `SourceError` (once per variant) + `LogEntry` |

### Cost calculation

Codex rollouts do NOT record cost (only tokens). The ingester computes cost via `internal/canonical/pricing.go` keyed on `(provider="openai", model=<turn_context.model>)`. The adapter just emits raw token totals.

### Token accounting nuance

`TokenUsageInfo.total_token_usage` is cumulative for the session, not per-turn. `last_token_usage` is per-LLM-call. The adapter accumulates `last_token_usage` deltas per `turn_id` using `token_count` events between `task_started.turn_id` and `task_complete.turn_id`. If `turn_id` is not present on the `token_count` event (it's a session-level event), the adapter attributes each `token_count` to the most recently active turn (the one whose `task_started` is the latest before this `token_count`).

## Sub-Agent Linkage

Codex supports sub-agents (`SubAgentSource::ThreadSpawn`) and forks (`forked_from_id`). Both produce SEPARATE rollout files under the normal `sessions/YYYY/MM/DD/` tree:

- **Sub-agent**: `session_meta.payload.source = {"subagent": {"thread_spawn": {"parent_thread_id": "<uuid>", "depth": N, "agent_nickname": "...", "agent_role": "..."}}}` and `thread_source = "subagent"`. The parent session's rollout file does NOT inline the child; it appears separately and the parent is identified via `parent_thread_id`.
- **Fork**: `session_meta.payload.forked_from_id = "<uuid>"` — branched/resumed from another session.
- **`event_msg.collab_agent_spawn_begin`/`_end`** in the PARENT rollout name the spawn but the `_begin` event is NOT persisted (`policy.rs:215`). Only `_end` is. The `_end` event carries the parent→child link as `sender_thread_id` (parent) → `new_thread_id` (child), alongside `new_agent_nickname`, `new_agent_role`, `model`, `reasoning_effort`, and `status`. (Real workstation corpus: 5 `collab_agent_spawn_end` files; the field is `new_thread_id`, NOT `agent_ref.thread_id` as an earlier draft of this spec wrongly stated.)
- **`event_msg.collab_close_end`** (72 files) and **`event_msg.collab_waiting_end`** (74 files) also appear in collab sessions. They carry no parent→child edge the topology view needs, so the adapter recognizes them (no `SourceError`) and surfaces each as a `LogEntry` only — no canonical op.

Adapter behavior:

- Emit `SessionStartedEvent.ParentNativeID = parent_thread_id` when the child's `session_meta.source` is `subagent`.
- Emit `SessionStartedEvent.ParentNativeID = forked_from_id` otherwise when `forked_from_id` is present.
- In the parent, when an `event_msg.collab_agent_spawn_end` line appears, emit an Op Kind=`session`, Name=`spawn`, ChildSessionNativeID=`new_thread_id`. (If the child rollout file doesn't yet exist at that moment, the ingester's foreign-key constraint must be relaxed temporarily — the canonical-events spec allows out-of-order child arrival.)
- A sub-agent rollout file with `parent_thread_id` referring to an unknown session is recorded with `parent_session_id` set to NULL and a `LogEntry` warning; reconciled when the parent appears.

Real observation: 8 distinct sub-agent sessions in the sampled set, all `depth=1`, with named nicknames (Raman, Tesla, Nash, Boyle, etc.) and role `"explorer"`.

## Edge Cases

1. **Crash mid-stream**: file ends without `task_complete`. Handle per state-machine rule #23 (synthetic finalization after stale mtime).

2. **Multiple `task_started` without intervening `task_complete`**: legitimate (user interrupted and re-prompted). Emit `TurnFinalizedEvent(failed, reason="replaced")` for the previous turn at the timestamp of the new `task_started`.

3. **Old CLI versions (< 0.93.0) lacking `task_started`/`task_complete`**: turn boundaries come only from `turn_context`. Open a turn at each new `turn_context.turn_id`; close it at the next `turn_context` with a different `turn_id` OR at EOF. If `turn_context.turn_id` is absent, fall back to "user message → next user message" heuristic.

4. **Schema additions** (new `payload.type` strings in newer CLI versions): forward-compat via `#[serde(other)]` upstream → unknown variants in Go decoder produce a `LogEntry` warning and pass through as Op Kind=`internal`, Name=`"unknown:" + type` with the raw JSON in Extras.

5. **Sandbox mode `read-only`**: file operations may be `function_call`s that produce `function_call_output` with an error ("operation denied by sandbox"). Adapter emits `OpFinalized.Status="failed"`, `ErrorClass="sandbox_denied"` (heuristic from output string).

6. **Sandbox mode `danger-full-access`**: no parsing difference; surface in Extras only.

7. **Very large reasoning content** (`encrypted_content` can be 50+ KB of base64): keep as PayloadRefEvent pointing to a byte range within the file (`file://<path>#L<line>` URI scheme is reasonable; presenter reads on demand). DO NOT inline raw content into SQLite.

8. **Token streaming truncated mid-response**: not directly visible in rollout (deltas not persisted, `policy.rs:184,210`). The terminal `agent_message` event arrives only on completion, so a truncated assistant response means no terminal event → handled by edge case #1.

9. **`function_call` without matching `function_call_output`** (e.g. user interrupted): emit `OpStarted` with no matching `OpFinalized`. At `task_complete` or `turn_aborted`, finalize all dangling ops with Status=`cancelled`.

10. **`function_call_output` without matching `function_call`** (corrupt or out-of-order): emit `SourceError` log_entry; skip.

11. **Embedded control characters / ANSI escapes in tool output strings**: codex serializes these as `\uXXXX` JSON escapes. Go's `encoding/json` accepts them. jq's strict mode rejects them — do not test parsing with jq alone.

12. **Legacy `.json` files (pre-mid-2025)**: 19 such files exist on this workstation. Out of scope for v1 (emit one informational log entry per file).

13. **File renamed/moved**: codex does not rename files. If an operator manually renames or moves a rollout file, the adapter sees a Delete event on the old path and Create on the new path; cursor entry for the old path is left stale. Optional cleanup after N days.

14. **Two rollouts with the same `id`**: not observed (0 of the 2,566 modern files on the reference workstation) but theoretically possible (codex could resume into a forked thread). The intended behavior is to treat them as separate canonical sessions keyed on `(source_id, native_id+":"+file_basename)` with a LogEntry warning. **v1 limitation (SOW-0004):** the adapter uses the authoritative `session_meta.payload.id` as `NativeID`, and the ingester upserts sessions on `(source_id, native_id)`, so two same-`id` rollout files would COLLAPSE into one canonical session rather than disambiguate. The basename-disambiguation is deferred to **SOW-0022** (requires cross-file id-collision detection the per-file adapter does not have today). Unobserved edge; no data loss within a single session.

15. **`originator` variants**: observed `codex_cli_rs`, `codex_exec`, `codex-tui`. Treat as identifying string; surface in Extras.

16. **Sub-agent with role="explorer" or other**: just metadata; no parsing difference.

17. **Cwd identifies the project**: `session_meta.cwd` is the de facto project identifier. UI can group sessions by cwd.

18. **`git` block with sensitive `repository_url`**: real files can contain hosted-git SSH URLs with account/repository identity. Fixtures MUST sanitize to a neutral example repository URL that does not resemble a real account identity.

## Canonical Model Gaps

Items in codex that don't map cleanly to canonical-events.md:

> **v1 `turns.extras_json` reachability (gaps #2, #3, #8).** The `turns` table has an
> `extras_json` column (data-model.md), but no canonical turn event carries an `Extras`
> field today (`TurnStartedEvent`/`TurnFinalizedEvent` in `internal/canonical/events.go`
> have none) and the ingest writer never populates `turns.extras_json`. So `codex_turn_id`,
> `turns.extras_json.sandbox`, and `ttft_ms` are structurally unreachable from any adapter
> as of SOW-0004. The codex adapter therefore surfaces these per-turn values via a single
> informational `turn_meta` LogEntry at turn finalize (no silent loss), and populating the
> real `turns.extras_json` column is deferred to a follow-up SOW that adds a turn-extras
> carrier to the canonical event + writer (shared infrastructure benefiting every adapter).

1. **Reasoning op as first-class**: covered (`OpKind = 'reasoning'` exists). However, codex distinguishes `agent_reasoning` (visible summary) from `agent_reasoning_raw_content` (full CoT) — canonical model has no field for that distinction. Stash in Extras: `{reasoning_kind: "summary"|"raw"}`.

2. **No "turn" concept in codex pre-0.93**: older sessions infer turns from `turn_context` boundaries. Canonical `turn.seq` becomes a synthesized 1-based counter that may not match any codex-internal id. Store the codex `turn_id` (UUID) in `turns.extras_json.codex_turn_id` for cross-reference.

3. **Sandbox/approval/permission policy**: rich codex metadata that has no canonical-events equivalent. Goes into `sessions.extras_json.sandbox` (deferred from session_meta.source) and `turns.extras_json.sandbox` (snapshotted from each turn_context).

4. **Compaction**: canonical has no first-class compaction event. Modeled as Op Kind=`internal`, Name=`compaction`. Acceptable.

5. **Forks (`forked_from_id`)**: canonical has `parent_session_id` which fits forks naturally — modeled the same as sub-agents. UI must distinguish via `extras_json.relationship = "fork"|"sub_agent"|"thread_spawn"`.

6. **`thread_source = memory_consolidation`**: a Codex-internal "fake" session that consolidates memories. Map to `SessionKind = 'tool_internal'`.

7. **MCP tool call namespacing**: covered via `tool_namespace = "mcp:<server>"`. Pricing/catalog rollups MUST treat this as one namespace per MCP server.

8. **`task_complete.time_to_first_token_ms`**: time-to-first-token metric. Canonical has no field; goes to `turns.extras_json.ttft_ms`.

9. **`rate_limits` snapshot inside `token_count`**: usage/quota info. Goes to `sessions.extras_json.rate_limits` (overwritten with the latest snapshot per session).

10. **Sub-agent depth**: canonical `parent_session_id` allows arbitrary nesting via recursive walk. Codex flat-stamps `depth` on the child; surface in `sessions.extras_json.subagent_depth`.

11. **Plan items (`item_completed` with `TurnItem::Plan`)**: codex's planning subsystem. Map to LogEntry severity=INF for now; future SOW may add a `plans` table if the UI needs structured plan display.

12. **Real-time conversation events** (`realtime_conversation_*`): voice/streaming subsystem; none of the real workstation files contain these. Adapter ignores them with a `LogEntry` if seen.

## Performance Benchmarks

The adapter has two deterministic workstation benchmarks in
`internal/adapters/codex/bench_test.go`, and both are included in
`scripts/check-bench.sh` and `bench/baseline.txt`:

- `BenchmarkCodexScan_SyntheticCorpus` exercises first backfill over a
  synthetic `codex_home/sessions/YYYY/MM/DD/rollout-*.jsonl` tree. The fixture
  uses fake UUIDs, example paths, generic message/tool content, and exact event
  count assertions so the benchmark cannot silently stop exercising mapper
  behavior.
- `BenchmarkCodexTail_SyntheticAppend` exercises the deterministic tail flush
  path after an initial cursor seed. Timers, fsnotify delivery, and producer
  sleeps are outside the timed region; the timed path is the adapter's
  append-read/parse/map/emit work.

Both benchmarks report `B/s` through `b.SetBytes`, adapter-specific throughput
metrics via `b.ReportMetric` (`events/sec`; scan also reports `files/sec`), and
`peak_heap_mb` so the Codex path has the same memory-regression visibility as
the other adapter benchmarks. The benchmark contract is semantic as well as
performance-related: if the exact emitted event counts change, the benchmark
fails instead of recording a misleading faster/slower result.

## References

All citations use the convention from AGENTS.md (`openai/codex @ 8a94430b`). Where `protocol.rs` is referenced, the path is `codex-rs/protocol/src/protocol.rs`; where `models.rs` is referenced, the path is `codex-rs/protocol/src/models.rs`.

- `openai/codex @ 8a94430bb273623be42b68f144f1ab1df343bb53`
- `codex-rs/rollout/src/lib.rs:23-24` — `SESSIONS_SUBDIR`, `ARCHIVED_SESSIONS_SUBDIR` constants
- `codex-rs/rollout/src/recorder.rs:1325-1354` — path construction (`YYYY/MM/DD/rollout-...jsonl`), local-time formatting
- `codex-rs/rollout/src/recorder.rs:1357-1369` — `open_log_file` — `OpenOptions::append(true).create(true)`
- `codex-rs/rollout/src/recorder.rs:1605-1620` — `append_rollout_item_to_path` — confirms append-only semantics
- `codex-rs/rollout/src/recorder.rs:1622-1654` — `JsonlWriter` — `write_all + flush` (no fsync), `\n` line terminator
- `codex-rs/rollout/src/recorder.rs:836,926` — legacy ghost_snapshot stripping during read
- `codex-rs/rollout/src/list.rs:399` — directory layout comment
- `codex-rs/rollout/src/list.rs:898,918,932` — `.jsonl` extension filter, filename regex
- `codex-rs/rollout/src/policy.rs:17-26` — `is_persisted_rollout_item` — which records reach disk
- `codex-rs/rollout/src/policy.rs:51-63` — `sanitize_rollout_item_for_persistence` — stdout/stderr blanking for `exec_command_end`
- `codex-rs/rollout/src/policy.rs:67-85` — `should_persist_response_item` — persisted `ResponseItem` variants
- `codex-rs/rollout/src/policy.rs:135-220` — `event_msg_persistence_mode` — Limited vs Extended event filtering
- `codex-rs/rollout/src/session_index.rs:17-65` — `session_index.jsonl` append-only rename log (out of scope for ingest)
- `codex-rs/protocol/src/protocol.rs:1133-1328` — `EventMsg` enum (all variants)
- `codex-rs/protocol/src/protocol.rs:1832-1865` — `TurnCompleteEvent`, `TurnStartedEvent`
- `codex-rs/protocol/src/protocol.rs:1895-1979` — `TokenUsage`, `TokenUsageInfo`, `TokenCountEvent`
- `codex-rs/protocol/src/protocol.rs:1981-2003` — `RateLimitSnapshot`
- `codex-rs/protocol/src/protocol.rs:2137-2188` — `AgentMessageEvent`, `UserMessageEvent`, `AgentReasoningEvent`, `AgentReasoningRawContentEvent`
- `codex-rs/protocol/src/protocol.rs:2191-2228` — `McpInvocation`, `McpToolCallEndEvent`
- `codex-rs/protocol/src/protocol.rs:2271-2275` — `WebSearchEndEvent`
- `codex-rs/protocol/src/protocol.rs:2438-2517` — `SessionSource`, `InternalSessionSource`, `SubAgentSource`
- `codex-rs/protocol/src/protocol.rs:2638-2703` — `SessionMeta`, `SessionMetaLine`
- `codex-rs/protocol/src/protocol.rs:2705-2734` — `RolloutItem`, `CompactedItem`
- `codex-rs/protocol/src/protocol.rs:2745-2776` — `TurnContextItem` (model, sandbox_policy, approval_policy, effort)
- `codex-rs/protocol/src/protocol.rs:2849-2854` — `RolloutLine` envelope
- `codex-rs/protocol/src/protocol.rs:2856-2867` — `GitInfo`
- `codex-rs/protocol/src/protocol.rs:3003-3043` — `ExecCommandEndEvent`
- `codex-rs/protocol/src/protocol.rs:3140-3166` — `PatchApplyEndEvent`, `PatchApplyStatus`
- `codex-rs/protocol/src/protocol.rs:3609-3631` — `TurnAbortedEvent`, `TurnAbortReason`
- `codex-rs/protocol/src/models.rs:708-723` — `ContentItem`
- `codex-rs/protocol/src/models.rs:750-903` — `ResponseItem` enum (Message, Reasoning, LocalShellCall, FunctionCall, FunctionCallOutput, CustomToolCall/Output, WebSearchCall, ImageGenerationCall, Compaction, ContextCompaction, Other)

Real-file observations (sanitized; no operator-specific values quoted):

- 2,585 rollout files on workstation: 2,566 modern `.jsonl` in `sessions/YYYY/MM/DD/`, 19 legacy `.json` in `sessions/` root.
- CLI version range observed: 0.61.0 → 0.125.0.
- Sandbox modes observed: `workspace-write` (68%), `danger-full-access` (30%), `read-only` (2%) across 50 sampled sessions.
- Sub-agent sources observed (`thread_spawn` with `depth=1`, role `"explorer"`): real fixture pool present.
