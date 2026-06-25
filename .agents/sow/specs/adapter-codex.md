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

The adapter's inputs are:

- current-format `sessions/YYYY/MM/DD/rollout-*.jsonl`;
- legacy flat `sessions/rollout-YYYY-MM-DD-<uuid>.json` files that contain
  source-visible conversation artifacts.

The other Codex artifacts are explicitly out of scope for the codex adapter.

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

Upstream Codex no longer produces or reads this format: `list.rs:898` rejects
any file whose name does not end in `.jsonl`. ai-viewer still ingests valid
legacy files because they are source-visible historical data. A malformed
legacy JSON file is a source corruption error: the adapter reports `SourceError`
and the SOW-0097 parity gate reports `INCOMPLETE`; it must not be silently
ignored. If a legacy file begins with one complete valid flat rollout object and
then contains extra non-whitespace bytes, the adapter and source extractor MUST
ingest the complete valid prefix and also report the trailing bytes as source
corruption. Recoverable prefix artifacts are not dropped just because the file
tail is corrupt.

Legacy file mapping:

- The top-level `session` object maps to the same canonical `SessionStarted`
  contract as a modern `session_meta` record.
- Each `items[]` object maps as a direct response item. The canonical payload
  ref points at the original legacy JSON file with an exact JSON pointer, for
  example `file://.../rollout.json?json_pointer=/items/3/content/0/text`.
- Native payload artifact IDs are `file:<basename>:<json-pointer>` because
  legacy payloads are selected from a whole JSON document rather than a JSONL
  line.
- `local_shell_call.action` is the tool request payload for legacy shell calls;
  `local_shell_call_output.output` is the matching tool response payload.

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

Newer rollout files also persist the serde-flattened body directly at the top
level for known `RolloutItem` and `ResponseItem` variants:

```json
{"timestamp": "...", "type": "session_meta", "id": "...", "cwd": "..."}
{"timestamp": "...", "type": "turn_context", "turn_id": "...", "model": "..."}
{"timestamp": "...", "type": "message", "role": "assistant", "content": [...]}
{"timestamp": "...", "type": "reasoning", "summary": [...]}
{"timestamp": "...", "type": "function_call", "call_id": "...", "arguments": "..."}
{"timestamp": "...", "type": "function_call_output", "call_id": "...", "output": "..."}
```

Both shapes are the same logical records. Wrapped records keep JSON-pointer
selectors under `/payload/...`; direct records use root-field selectors
(`/content/<i>/text`, `/summary/<i>/text`, `/arguments`, `/output`). Direct
`ghost_snapshot` records are no-ops, matching wrapped
`response_item.payload.type="ghost_snapshot"`.

Older sharded JSONL rollout files from the 2025-08 to 2025-09 transition period
can use a no-`type` first-line session header:

```json
{"timestamp": "...", "id": "...", "instructions": null, "git": {...}}
```

The observed key sets are `id,instructions,timestamp` and
`git,id,instructions,timestamp`; `instructions` is either a string or null.
This is a logical legacy `session_meta` header, not a source error. The adapter
and source extractor MUST treat it as the first session record:
`NativeID=id`, `StartedAt=timestamp`, optional `git` preserved in session
extras, and missing modern metadata (`cwd`, `originator`, `cli_version`,
`source`) left empty/defaulted. The `instructions` field is sensitive session
metadata and is not emitted as a parity payload artifact.

Some modern files also contain root-level state sentinels:

```json
{"record_type":"state"}
```

These lines have no `timestamp`, no `type`, and no source-visible payload. They
are Codex bookkeeping and are ignored as parser-level no-ops. They must not
surface as source errors and must not advance old-format EOF turn finalization
time.

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

Modern Codex `SessionMeta` also carries top-level
`payload.parent_thread_id`. For sub-agent sessions, the nested
`source.subagent.thread_spawn.parent_thread_id` is preferred when present; if
the nested source shape is a bare subagent marker with no parent, the adapter
falls back to top-level `parent_thread_id`. This mirrors upstream
`codex-rs/protocol/src/protocol.rs` `SessionMeta.parent_thread_id`.
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
| `tool_search_call`, `tool_search_output` | tool-search subsystem | `tool_search_call.arguments` is a JSON value, not a Responses-API string; the adapter must accept object/array/scalar values and preserve the exact `/arguments` selector. |
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
| `error` | `ErrorEvent` | Persist as an `ERR` `LogEntryEvent`. When `payload.message` exists, `LogEntryEvent.Message` is the exact source message and `Extras.aiViewer.parity` carries `nativeArtifactId=line:<line>:/payload/message`, `selectorURI=file://...#L<line>`, and `jsonPointer=/payload/message` so the ingestion parity gate can match the source log artifact exactly. |
| `guardian_assessment` | | |
| `exec_command_end` | `ExecCommandEndEvent` | `call_id`, `turn_id`, `command[]`, `cwd`, `parsed_cmd`, `source`, `stdout`/`stderr`/`aggregated_output` (truncated to 10000 bytes), `exit_code`, `duration`, `formatted_output`, `status`. **The stdout/stderr/formatted_output fields are cleared on persistence** (`policy.rs:51-59`); only `aggregated_output` (truncated middle) survives. |
| `view_image_tool_call` | | |
| `collab_*_end` | | sub-agent collab lifecycle ends |
| `dynamic_tool_call_request`, `dynamic_tool_call_response` | | |

Default-visible metadata events that do not map to turns, ops, payload refs, or
errors, including observed `thread_goal_updated` and `view_image_tool_call`,
are retained as `DBG` `LogEntryEvent` rows with message `event_msg:<type>`.
The source manifest emits matching `log_entry` artifacts using the generic log
identity (`scope`, `timestamp`, `severity=DBG`, `source=codex`, and message),
not raw-line hashes.

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
- On full scan, ingest each root-level legacy `rollout-*.json` once and record it
  under `legacy_json`. Legacy files are static historical snapshots; tail
  events on them may be ignored after scan coverage.
- A recoverable legacy `.json` file with one valid flat rollout object followed
  by trailing non-whitespace corruption emits artifacts from the valid prefix
  and records a `SourceError` for the tail. The SOW-0097 source manifest also
  emits one parity-only `source_corruption` artifact for the trailing byte range
  with `availability=source_corrupt`, `hash_domain=raw_bytes`, and
  `native_artifact_id=source_corruption:file:<basename>:trailing`; its
  `integrity_failures[]` includes `field=trailing_bytes`, `expected=0`, and
  `actual=<trailing-byte-count>`. The diff reports this as a `source_corrupt`
  finding and leaves the run `INCOMPLETE`.
  A file whose first JSON value is not a valid flat rollout remains a source
  error with no recovered artifacts.
- Ignore: any file NOT matching `^rollout-.*\.jsonl$` under `sessions/YYYY/MM/DD/`
  or root-level `^rollout-.*\.json$`.
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

- If `offset == size == current_size`, `eof_finalized_size == current_size`,
  and `mtime_us` matches the current file mtime, the scanner skips opening the
  rollout. The cursor already proves that every complete line was consumed and
  the EOF finalization decision for that exact file state already fired. This
  fast path is required for large live backfills and diagnostic sampled parity
  scans; it must not apply when `mtime_us` is missing/mismatched, when the file
  grew or shrank, or when `eof_finalized_size` is absent.
- For each tracked file: if `current_size >= cursor.offset`, resume from `cursor.offset`.
- If `current_size < cursor.offset`: file was truncated (codex never truncates, so this means manual operator deletion + recreation) — emit `SourceError`, reset to 0, full re-scan (also clears `eof_finalized_size`).
- For new files (not in cursor): start at 0, full scan.
- For files no longer present on disk: leave cursor entry; never re-emit. Optional GC after N days.

## Mapping to Canonical Events

Codex rollout files emit fine-grained `RolloutItem` records but do NOT carry pre-aggregated turn/op summaries (unlike ai-agent v3 which emits a `turn_end` record with rollup totals). The adapter therefore acts as a state machine over the line stream:

### Per-file state machine

1. **`session_meta` (always first line, either typed modern
   `type=session_meta` or the no-`type` legacy JSONL header):**
   - Emit `SessionStartedEvent` with:
     - `NativeID = payload.id`
     - `ParentNativeID = payload.forked_from_id` (when present), OR `source.subagent.thread_spawn.parent_thread_id` (when present), falling back to top-level `payload.parent_thread_id` for sub-agent sessions when the nested source marker carries no parent, else empty
     - `RootNativeID =` the top-level root of the Codex session tree. When all
       ancestor rollout metadata is visible, the adapter/source parity extractor
       walk `ParentNativeID` ancestry to the root. If an ancestor rollout has not
       landed yet, the immediate parent is a provisional root and the ingester
       resolver repairs the stored `root_session_id` after the missing ancestor
       is linked. Canonical parity extraction still reads the stashed native
       parent/root ids for `session_boundary` comparison while those rows are
       absent; this records the source lineage without inventing missing
       canonical sessions.
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
   - Source-manifest parity must emit matching `op_boundary` artifacts for those dangling ops at the same `completed_at`/line timestamp used for the turn close. They are finalized at `task_complete`, not later at EOF, so a parity gate can detect timestamp drift and missing held-open operations.

5. **`event_msg` payload `turn_aborted`:**
   - Emit `TurnFinalizedEvent(turn_seq, Status="failed", ErrorClass=reason, EndTs)`.
   - For `reason="interrupted"` set ErrorClass="user_interrupt"; `"replaced"` → `"replaced"`; `"review_ended"` → `"review_ended"`; `"budget_limited"` → `"rate_limit"`.
   - Emit `OpFinalizedEvent` for each held-open op tied to this turn with Status="cancelled" at the same close timestamp. Source-manifest parity must emit matching cancelled `op_boundary` artifacts before the failed `turn_boundary`; a later EOF cleanup with Status="completed" is wrong because the source explicitly says the turn was aborted.

6. **`response_item` payload `message` role=user:**
   - User input within a turn. Emit `OpStartedEvent` + `OpFinalizedEvent` with Kind=`internal`, Name=`user_input`. Bodies are stored as `PayloadRefEvent` with PayloadKind=`tool_request` (the canonical artifact class is `user_prompt` by `kind=internal,name=user_input` plus selector metadata). For text content arrays, LocationURI uses an exact JSON-pointer selector such as `file://.../rollout.jsonl?json_pointer=%2Fpayload%2Fcontent%2F0%2Ftext#L<line>`. For `event_msg.user_message`, the selector is `/payload/message`. `OriginalBytes` is the decoded logical text byte length, not the containing line length.
   - Some sessions duplicate user input as both `response_item.message(role=user)` and `event_msg.user_message`. Deduplicate by emitting only on the first occurrence (preferring `event_msg.user_message` if both arrive — it's the canonical UI event).

7. **`response_item` payload `message` role=assistant:**
   - Emit OpStarted+OpFinalized Kind=`llm`, Name=`message`, Model=current turn model, Provider=`openai`. Text content bodies are stored as PayloadKind=`llm_response` refs with exact JSON-pointer selectors such as `/payload/content/<index>/text`; the canonical artifact class is `assistant_message` by `kind=llm,name=message` plus selector metadata.
   - When `phase=final_answer`, also emit a `LogEntry` so the UI can flag "this is the final response".

8. **`response_item` payload `reasoning` and `event_msg` payload `agent_reasoning`/`agent_reasoning_raw_content`:**
   - `response_item.reasoning` emits OpStarted+OpFinalized Kind=`reasoning`,
     Name=`reasoning`. Text-bearing summary/content bodies are stored as
     PayloadKind=`llm_reasoning` refs with exact JSON-pointer selectors such as
     `/payload/summary/<index>/text` or `/payload/content/<index>/text`.
     Format=`text` for summary-only reasoning and Format=`json` for raw/full
     response items. Opaque encrypted content can fall back to the containing
     record until the parity matrix classifies that source artifact explicitly.
     A reasoning record with no text-bearing summary/content and no encrypted
     content emits the reasoning op boundary only; it MUST NOT emit a whole-file
     fallback payload ref, because there is no source-visible reasoning text to
     prove and such refs collapse multiple empty reasoning records onto the same
     artifact key.
   - `event_msg.agent_reasoning` and `event_msg.agent_reasoning_raw_content`
     are UI-visible companion summaries. They produce only derived DBG logs and
     MUST NOT emit second `reasoning_text` source artifacts or source-backed
     `log_entry` artifacts for `payload.text`. The exact reasoning proof comes
     from `response_item.reasoning`; claiming both forms as reasoning artifacts
     duplicates source content and makes source-vs-canonical parity noisy.

9. **`response_item` payload `function_call` (+ matching `function_call_output`):**
   - Emit OpStarted at `function_call` line: Kind=`tool`, Name=`payload.name`, ToolNamespace=`payload.namespace` (or inferred — see below), Extras={call_id, arguments_raw}.
   - Match `function_call_output` by `call_id` to emit OpFinalized with EndTs=output's line timestamp, Status derived (success if output not an error, else failed).
   - PayloadRefs: `tool_request` (Format=`json`, selector `/payload/arguments`) and `tool_response` (Format=`json`, selector `/payload/output`). `OriginalBytes` is the selected logical value length: decoded string bytes for string values, zero for JSON null, and canonical JSON bytes for object/array/scalar values.
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
    - Source-manifest parity must mirror that FIFO rule: each `web_search_call` opens a `web_search` `op_boundary`, the next still-open `web_search_end` closes the oldest one as `completed`, and a turn close finalizes an unpaired web search using the normal dangling-op rule. The `tool_request` proof for the current canonical mapping is the whole `web_search_call` JSONL record (`line:<n>`, `hash_domain=raw_bytes`) because the mapper emits a whole-record payload ref for this forward-compatible call shape rather than a nested `json_pointer`.

12. **`response_item` payload `image_generation_call` / `event_msg.image_generation_end`:**
    - Op: Kind=`tool`, Name=`image_generation`, ToolNamespace=`media`. **UNOBSERVED: 0 real files for both `image_generation_call` and `image_generation_end`** — this mapping is forward-compat only and synthetic fixture coverage is acceptable until real data exists to sanitize. `image_generation_end.call_id` closes the matching open image-generation op at the end-event timestamp; if the end event cannot be matched, the active-turn fallback still closes the dangling op at turn close.
    - Source-manifest parity mirrors the same forward-compatible lifecycle rule: `image_generation_call` opens the media tool op, and `image_generation_end.call_id` finalizes the matching open op as `completed` at the end-event timestamp so a parity diff catches accidental fallback to `task_complete` / EOF.

13. **`response_item` payload `local_shell_call` / `local_shell_call_output`:**
    - LEGACY ONLY (does not occur in modern `.jsonl`). When ingesting legacy `.json` files: Kind=`tool`, Name=`shell`, ToolNamespace=`shell`.

14. **`event_msg` payload `exec_command_end`:**
    - Used for telemetry enrichment — the matching `function_call`/`function_call_output` pair carries the same `call_id` and produces the op. The `exec_command_end` adds: parsed_cmd, exit_code, duration, source. Adapter merges these into the op's Extras: `{exec_exit_code, exec_duration_ms, exec_cwd, exec_source}`. The `duration` is a Rust `Duration` object `{secs, nanos}` (real corpus: always this shape) normalized to integer `exec_duration_ms = secs*1000 + nanos/1e6`. **Do not** emit a second op.
    - The `exit_code` is AUTHORITATIVE for the op's terminal status, ORDER-INDEPENDENTLY (G1, rule #5): non-zero `exit_code` → op `failed` / ErrorClass `command_failed`; `exit_code` 0 → `completed`. When `exec_command_end` arrives BEFORE the `function_call_output` (~68-85%), the exec status is stashed and WINS over the output-string heuristic at finalize. When it arrives AFTER (output-first, ~15-32%), the adapter emits a CORRECTING `OpFinalized` on the op's `(turn,seq)` so a non-zero exit overrides a provisionally-`completed` op. A blanked `aggregated_output` is NOT an error.
    - Source-manifest parity mirrors the exec-first rule: an
      `exec_command_end` with matching open `call_id` does not emit a second
      source op, but stashes the exit-code-derived terminal status on that open
      op. The later `function_call_output` or dangling turn close emits the
      `op_boundary`; non-zero `exit_code` emits a matching `tool_error` identity
      with `ErrorClass="command_failed"` and the canonical empty-message hash
      when the source event carries no separate error message.
    - Note: `aggregated_output` is truncated to 10 KB at the source; `stdout`/`stderr`/`formatted_output` are blanked (`policy.rs:51-59`). Adapter cannot recover full output.

15. **`event_msg` payload `mcp_tool_call_end`:**
    - For MCP-routed function calls (which appear ALSO as `function_call`/`function_call_output` with `name = "<server>.<tool>"` or via the `namespace` field). The `invocation` field gives canonical (server, tool). Use it to set `tool_namespace = "mcp:" + server` and `name = tool` on the matching op.
    - Source-manifest parity treats `mcp_tool_call_end.call_id` as the
      source-visible finalizer for the matching open tool op. The manifest must
      restamp the op identity to `Name=invocation.tool` and
      `ToolNamespace="mcp:"+invocation.server`, close the op at the
      `mcp_tool_call_end` timestamp, and emit a `tool_error` identity with
      `ErrorClass="tool_error"` when `result.Err` is present or
      `result.Ok.is_error=true`. The `op_boundary` identity records the MCP
      namespace for MCP-restamped tools so the parity diff proves namespace
      migration, not just op existence.

16. **`event_msg` payload `patch_apply_end`:**
    - Telemetry for an `apply_patch` `function_call`. Merge `success`, `status` into the op's Extras as `{patch_success, patch_status}`. Set Op Status accordingly (success=false → `failed` / ErrorClass `patch_failed`). ORDER-INDEPENDENT, mirroring exec (G2): an `apply_patch` op still open is finalized here with the extras merged; an already-finalized op (output-first) gets the extras re-emitted plus a correcting `OpFinalized` on its `(turn,seq)`.
    - Source-manifest parity treats `patch_apply_end.call_id` as a source-visible
      op finalizer for the matching open `apply_patch` tool op. `success=false`,
      `status="failed"`, or `status="error"` produces an `op_boundary`
      status=`failed` and a `tool_error` identity artifact with
      `ErrorClass="patch_failed"` and the canonical empty-message hash when the
      source event carries no separate error message.

17. **`event_msg` payload `token_count`:**
    - Stream of token accounting snapshots. Each carries cumulative `total_token_usage` and the per-call `last_token_usage`, plus optional `model_context_window`.
    - Per-turn rollup: at TurnFinalized, set `TokensIn = sum(last_token_usage.input_tokens − last_token_usage.cached_input_tokens)` (clamped ≥0) over `token_count` events between this turn's `task_started` and `task_complete`. codex's `input_tokens` is the TOTAL prompt (cached + uncached) — upstream `non_cached_input() = input_tokens − cached_input_tokens` (`codex-rs/protocol/src/protocol.rs`) — so the cached portion is subtracted to keep `TokensIn` FRESH per the canonical token contract (`canonical-events.md`), and the cached portion is recorded in `TokensCacheRead`. `TokensOut = sum(last_token_usage.output_tokens)`. (SOW-0029 fix: previously this summed `input_tokens` directly, folding cache into `TokensIn` and double-charging it in the pricer.)
    - Also derive `OpFinalized.CtxUsed = total_token_usage.total_tokens` and `OpFinalized.CtxMax = model_context_window` on the LLM op that immediately precedes the `token_count` line.
    - When `model_context_window` is present, update `catalog_models.ctx_max` via Extras propagation.

18. **`event_msg` payload `user_message`:**
    - Preferred user-input source (see #6). Emit `OpStarted`+`OpFinalized` Kind=`internal`, Name=`user_input`. If `local_images` / `images` carry paths/URLs, store them in Extras.

19. **`event_msg` payload `agent_message`:**
    - Companion to the assistant `response_item.message`. Adapter emits only the `response_item` (see #7); uses `event_msg.agent_message` only to populate `TurnFinalized.LastAgentMessage` Extras (for the UI "latest answer" preview).
    - Source-manifest parity mirrors that deduplication rule: `agent_message`
      MUST NOT emit a second `assistant_message` artifact and MUST NOT claim a
      source-backed `log_entry` for `payload.message`. The exact assistant body
      proof comes from the paired `response_item.message(role=assistant)`
      payload; the adapter's DBG `agent_message` log is a derived UI marker, not
      the parity artifact for the answer text.

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

24. **No `session_meta` or legacy no-`type` session header ever seen (corrupt
    file or pre-write crash):**
    - Emit `SourceError` and skip the file. Cursor.offset stays 0 so it is retried on next CREATE-style event.

### Tabular summary

| Source line | Canonical events emitted |
|---|---|
| `session_meta` | `SessionStartedEvent` (+`SessionUpdatedEvent` when model first known) |
| `turn_context` (new turn_id) | `TurnStartedEvent` (idempotent with task_started) |
| `event_msg.task_started` | `TurnStartedEvent` (idempotent) |
| `event_msg.task_complete` | `TurnFinalizedEvent(completed)` |
| `event_msg.turn_aborted` | `TurnFinalizedEvent(failed)` |
| `response_item.message` role=user, or direct `type=message` role=user | `OpStarted/Finalized` Kind=internal Name=user_input + PayloadRef |
| `response_item.message` role=assistant, or direct `type=message` role=assistant | `OpStarted/Finalized` Kind=llm + PayloadRef |
| `response_item.reasoning`, or direct `type=reasoning` | `OpStarted/Finalized` Kind=reasoning + PayloadRef |
| `response_item.function_call` / `_output` (paired), or direct `type=function_call` / `type=function_call_output` | `OpStarted` + `OpFinalized` Kind=tool + 2× PayloadRef |
| `response_item.custom_tool_call` / `_output`, or direct equivalents | same |
| `response_item.web_search_call`, or direct `type=web_search_call`, + `event_msg.web_search_end` | one Op Kind=tool Name=web_search |
| `response_item.image_generation_call`, or direct equivalent, + `event_msg.image_generation_end` | one Op Kind=tool Name=image_generation |
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

### Ingestion parity matrix

SOW-0097 adds a source-manifest extractor for Codex current JSONL rollouts and
legacy flat JSON rollouts. The extractor reads the rollout files directly and
must not call the canonical mapper. Its artifact IDs and identity JSON must
match the canonical extractor's boundary formulas so the diff proves
source-to-canonical parity instead of just checking that rows exist. Source
file scope mirrors production discovery: only
`sessions/YYYY/MM/DD/rollout-*.jsonl` and root-level
`sessions/rollout-*.json` legacy files are source-visible for this adapter.
The parity extractor MUST prune `archived_sessions/` and MUST ignore
root-level, wrong-depth, non-numeric-shard, or non-`rollout-` JSONL files, just
as the production scanner does. An ignored JSONL file is outside the Codex
adapter contract and must not create source artifacts or source parse errors.
The extractor walks the symlink-resolved sessions root and MUST refuse any
candidate rollout or legacy flat JSON file whose resolved target escapes that
root; such a source is incomplete until the unsafe path is removed, and the
extractor must not read the escaped target.
Source
rollout line reads are bounded to the adapter streamer's 16 MiB line cap. The
cap was raised from 8 MiB after live SOW-0097 evidence found valid Codex rollout
lines up to about 14 MiB. If a source rollout line exceeds that cap, source
extraction returns an error and `check-parity` reports the run as incomplete
instead of allocating an unbounded buffer or trying to decode a pathological
line. A malformed legacy flat JSON document likewise returns a source-extractor
error and makes `check-parity` `INCOMPLETE`. If the legacy document has a
complete valid first rollout object followed by trailing non-whitespace bytes,
the extractor emits source artifacts from the valid prefix and returns an error
for the trailing corruption. It also emits one parity-only `source_corruption`
artifact for the corrupt trailing byte range, so the diff can prove everything
recoverable while the run still fails closed on identified source-corrupt bytes.
The artifact records `integrity_failures[]` with `field=trailing_bytes`,
`expected=0`, and `actual=<trailing-byte-count>`.

For JSONL payload artifacts, the source extractor decodes the payload document
once per record and reuses that decoded document for selector discovery and
proof resolution. It must not re-decode the containing JSONL line once per
emitted nested artifact. Wrapped selectors keep their canonical `/payload/...`
form and direct response-item selectors keep their direct form, but the selected
bytes/hash/chars are resolved from the decoded payload document. Whole-record
classes such as `web_search_call` and source-backed top-level `compacted` bodies
still hash the trimmed JSONL record directly.

The high-volume `response_item` and `event_msg` source paths also route on
fields read from that decoded payload document (`type`, `role`, `name`, and
`call_id` where present). They must not perform a separate typed payload
unmarshal before proving nested payload artifacts.

Initial covered classes:

| Class | Source availability | Hash domain | Canonical representation | Selector / identity rule |
|---|---|---|---|---|
| `session_boundary` | `available` | `identity_json` | `sessions` row from `SessionStartedEvent` and optional stale-crash `SessionFinalizedEvent` | `native_artifact_id=session:<session_meta.payload.id>` or `session:<legacy-header.id>`. Clean Codex sessions stay `status=running` because Codex has no clean session-finalized record. Stale new-format crashes may become `failed/incomplete` per EOF rule #23. |
| `turn_boundary` | `available` | `identity_json` | `turns` rows from `turn_context`, `task_started`, `task_complete`, `turn_aborted`, supersede, or EOF-finalize rules | `native_artifact_id=turn:<seq>`. Old-format turns opened by `turn_context` close `completed` at EOF with `EndTs` equal to the last content timestamp, not file mtime. New-format fresh hanging turns stay running; stale hanging turns fail incomplete. |
| `op_boundary` | `available` | `identity_json` | `ops` rows from source-visible response/tool/compaction/collab records | `native_artifact_id=op:<turn_seq>:<op_seq>`. Sequences are the adapter's source-derived monotone op order inside each synthesized turn. MCP-restamped tool ops include `tool_namespace="mcp:<server>"` in the identity so source-vs-canonical diffs catch missed MCP namespace migration. |
| `user_prompt` | `available` / `source_empty` | `semantic_text` | `kind=internal,name=user_input` op plus `payload_refs.kind=tool_request` exact selector | `line:<line>:/payload/content/<i>/text` for wrapped response items, `line:<line>:/content/<i>/text` for direct JSONL response items, `file:<basename>:/items/<i>/content/<j>/text` for legacy flat JSON items, or `line:<line>:/payload/message` for event messages. |
| `user_image` | `available` | `canonical_json` | `kind=internal,name=user_input` op plus `payload_refs.kind=tool_request,format=json` exact selector | `line:<line>:/payload/content/<i>` or `line:<line>:/content/<i>` for `response_item.message(role=user)` image blocks, and `line:<line>:/payload/images/<i>`, `/payload/local_images/<i>`, or `/payload/image_details/<i>` for `event_msg.user_message` image references/details. Hashes cover canonical JSON for the selected source-visible block or scalar, not decoded image bytes. |
| `assistant_message` | `available` / `source_empty` | `semantic_text` | `kind=llm,name=message` op plus `payload_refs.kind=llm_response` exact selector | `line:<line>:/payload/content/<i>/text` for wrapped response items, `line:<line>:/content/<i>/text` for direct JSONL response items, or `file:<basename>:/items/<i>/content/<j>/text` for legacy flat JSON items. |
| `reasoning_text` | `available` / `source_empty` | `semantic_text` | `kind=reasoning,name=reasoning` op plus `payload_refs.kind=llm_reasoning` exact selector | `line:<line>:/payload/summary/<i>/text` / `/payload/content/<i>/text` for wrapped response items, `line:<line>:/summary/<i>/text` / `/content/<i>/text` for direct JSONL response items, or `file:<basename>:/items/<i>/summary/<j>/text` for legacy flat JSON items. |
| `llm_error` | `not_source_visible` | n/a | none as an LLM op error | Codex rollout files persist generic `event_msg.error` diagnostics, not provider/model error envelopes tied to a specific LLM op. The adapter maps those diagnostics to `log_entry`, where exact source-message parity is available. The canonical parity extractor must not synthesize `llm_error` artifacts for codex failed LLM ops, because there is no source-visible Codex artifact that could prove them. |
| `tool_request` | `available` / `source_empty` | `semantic_text`, `canonical_json`, or `raw_bytes` | `kind=tool` op plus `payload_refs.kind=tool_request` exact selector | `line:<line>:/payload/arguments` for wrapped tool calls, `line:<line>:/arguments` for direct JSONL tool calls, or `file:<basename>:/items/<i>/action` for legacy `local_shell_call` items. `web_search_call` currently uses whole-record `line:<line>` raw-bytes proof because canonical stores a whole-line payload ref. |
| `tool_response` | `available` / `source_empty` | `semantic_text` or `canonical_json` | same tool op finalized by output record plus `payload_refs.kind=tool_response` exact selector | `line:<line>:/payload/output` for wrapped tool outputs, `line:<line>:/output` for direct JSONL tool outputs, or `file:<basename>:/items/<i>/output` for legacy flat JSON outputs. |
| `tool_error` | `available` when a tool end event carries failed status/error semantics | `identity_json` | failed `ops` row with `error_class` / `error_message`; currently covered for non-zero `exec_command_end` and failed `patch_apply_end` finalization | `op:<turn_seq>:<op_seq>:error`. Identity records owning op kind, turn/op sequence, error class, and error-message hash. |
| `log_entry` | `available` / `source_empty` for source diagnostics intentionally surfaced to the operator and source-backed compaction bodies | `semantic_text` | `log_entries` row when the source diagnostic is represented as a log row; `payload_refs.kind=log` when the source diagnostic/body is op-scoped payload content | `line:<line>:/payload/message` for `event_msg.error` and other exact-message diagnostics that claim source parity. For data-bearing top-level `compacted` records, the compaction op owns one `payload_refs.kind=log` artifact keyed as `line:<line>` over the whole trimmed JSONL record because the adapter intentionally stores a whole-line payload ref for the compaction body. Derived logs use deterministic `log://` IDs and do not satisfy source-field parity. |
| `subagent_link` | `available` when `event_msg.collab_agent_spawn_end.new_thread_id` is present | `identity_json` | `kind=session,name=spawn` op with `child_session_id` resolved to the spawned session | `op:<turn_seq>:<op_seq>:child_session:<new_thread_id>`. Identity records parent native session id, parent turn/op sequence, child native session id, link kind `child_session`, and direction `parent_to_child`. |
| `system_op` | `available` for persisted Codex lifecycle/review/default metadata events that the adapter surfaces as log rows | `identity_json` | `log_entries` rows with `source=codex` and messages from the Codex event type | `log:<scope>:<timestamp>:<severity>:<source-hash>:<message-hash>`. Identity records native session id, optional turn seq, severity, canonical log message, timestamp, and original `event_msg` type. This is an additional system-operation view over the existing log row; it does not remove the `log_entry` artifact. |
| `session_metadata` | `available` when `session_meta` carries persisted descriptive fields beyond the native id | `identity_json` | `sessions` row plus `sessions.extras_json` from `SessionStartedEvent` | `session:<session_meta.payload.id>:metadata` or `session:<legacy-header.id>:metadata`. Identity verifies the derived `agent_name` plus selected persisted `session_meta` fields: `cli_version`, `originator`, compact `source`, `model_provider`, `relationship`, `subagent_depth`, SHA-256 of `cwd`, and SHA-256 of the non-empty canonical `git` object. Sensitive raw fields such as `base_instructions`, `dynamic_tools`, `memory_mode`, and legacy `instructions` are intentionally excluded. `session_boundary` remains the proof for kind, parent, root, status, and timestamps. |

Machine-readable matrix rows:

| Class | Source availability | Hash domain | Canonical representation | Selector / identity rule | Evidence |
|---|---|---|---|---|---|
| `session_boundary` | `available` | `identity_json` | `sessions` row | `session:<session_meta.payload.id>` or `session:<legacy-header.id>` | Initial covered classes table above. |
| `turn_boundary` | `available` | `identity_json` | `turns` rows from turn pivots and EOF rules | `turn:<seq>` | Initial covered classes table above. |
| `op_boundary` | `available` | `identity_json` | `ops` rows from response/tool/compaction/collab records | `op:<turn_seq>:<op_seq>` | Initial covered classes table above. |
| `user_prompt` | `available` / `source_empty` | `semantic_text` / `canonical_json` | internal user-input op plus `payload_refs.kind=tool_request` | source line/file selector plus JSON pointer to user content | Initial covered classes table above. |
| `user_image` | `available` | `canonical_json` | internal user-input op plus `payload_refs.kind=tool_request` | source line selector plus JSON pointer to image block/reference/detail | Initial covered classes table above. |
| `assistant_message` | `available` / `source_empty` | `semantic_text` | LLM message op plus `payload_refs.kind=llm_response` | source line/file selector plus JSON pointer to assistant text | Initial covered classes table above. |
| `reasoning_text` | `available` / `source_empty` | `semantic_text` | reasoning op plus `payload_refs.kind=llm_reasoning` | source line/file selector plus JSON pointer to reasoning text | Initial covered classes table above. |
| `llm_request` | `not_source_visible` | n/a | none | Codex rollout does not persist raw provider request envelopes | Rollout item schema above. |
| `llm_response` | `not_source_visible` | n/a | none for provider envelope; assistant text is `assistant_message` | Codex rollout persists response items, not raw provider HTTP/SSE responses | Rollout item schema above. |
| `llm_sdk_request` | `not_source_visible` | n/a | none | Codex rollout does not persist a separate SDK request envelope | Rollout item schema above. |
| `llm_sdk_response` | `not_source_visible` | n/a | none | Codex rollout does not persist a separate SDK response envelope | Rollout item schema above. |
| `tool_request` | `available` / `source_unavailable` / `source_empty` | `semantic_text` / `canonical_json` / `raw_bytes` | tool op plus `payload_refs.kind=tool_request` | source line/file selector plus JSON pointer or whole-record selector | Initial covered classes table above. |
| `tool_response` | `available` / `source_unavailable` / `source_empty` | `semantic_text` / `canonical_json` | finalized tool op plus `payload_refs.kind=tool_response` | source line/file selector plus JSON pointer to output | Initial covered classes table above. |
| `llm_error` | `not_source_visible` | n/a | none as an LLM op error; generic errors are `log_entry` | no provider/model error envelope tied to an LLM op exists in Codex rollout files | Event message schema above. |
| `tool_error` | `available` | `identity_json` | failed `ops` row | `op:<turn_seq>:<op_seq>:error` | Initial covered classes table above. |
| `subagent_link` | `available` | `identity_json` | session spawn op with `child_session_id` | `op:<turn_seq>:<op_seq>:child_session:<new_thread_id>` | Initial covered classes table above. |
| `system_op` | `available` | `identity_json` | `log_entries` rows for lifecycle/review/default metadata events | `log:<scope>:<timestamp>:<severity>:<source-hash>:<message-hash>` over the Codex event type and log identity | Initial covered classes table above. |
| `compaction_event` | `available` | `identity_json` | compaction op metadata | `op:<turn_seq>:<op_seq>:compaction`, including `trigger`, optional `replacement_history_size`, optional `message_preview` hash, and op timestamp/sequence | Compaction rules above. |
| `session_metadata` | `available` | `identity_json` | `sessions` row plus `sessions.extras_json` | `session:<session_meta.payload.id>:metadata` or `session:<legacy-header.id>:metadata` over persisted descriptive fields only | Initial covered classes table above. |
| `log_entry` | `available` / `source_empty` | `semantic_text` / `raw_bytes` | log row or `payload_refs.kind=log` | `line:<line>:/payload/message` or whole-line compaction body | Initial covered classes table above. |
| `attachment_metadata` | `not_source_visible` | n/a | none as a separate attachment record | Codex rollout files do not persist a Claude-style `attachment` record. Image/file-like user inputs live inside `response_item.message(role=user)` content blocks or `event_msg.user_message` image fields and are covered by `user_image` artifacts. | Response item and event schema above. |
| `patch_metadata` | `not_source_visible` | n/a | none as a separate patch/file-change metadata record | Codex rollout files do not persist Opencode-style patch part records. Patch application telemetry is represented by `apply_patch` tool ops and `tool_error` when the patch fails. | Event message schema above. |

`event_msg.context_compacted` follows the adapter suppression rule in source
manifests: when it is the immediate next line after a top-level `compacted`
record with the same timestamp, it is the bare companion marker and emits no
second artifact. When it is not that adjacent companion, it emits the compaction
`op_boundary` plus a `payload_refs.kind=log`-equivalent `log_entry` keyed as
`line:<line>` over the whole trimmed JSONL record. Every emitted compaction op
also emits a `compaction_event` parity artifact keyed as
`op:<turn_seq>:<op_seq>:compaction`. For data-bearing top-level `compacted`
records, the identity includes `replacement_history_size` and a SHA-256 hash of
the stored `message_preview`; for lone `event_msg.context_compacted` and forward-
compatible response-item compaction records, the identity records `trigger=auto`
and the op timestamp/sequence fields.

`tool_output_unmatched` is not a persisted Codex `event_msg` payload type in the
adapter allowlist. It is a mapper-derived warning emitted when a
`function_call_output` has no matching open or finalized op. Source-manifest
extraction must not claim source-backed `log_entry` parity for
`event_msg.tool_output_unmatched`; such a source record is unsupported and must
fail as an unknown event variant, matching the canonical parser contract.

For the first structural parity fixture, an old-format single-turn rollout with
one `turn_context`, user prompt, assistant message, reasoning record, two tool
calls, and no `task_started`/`task_complete` must produce:

- one `session_boundary` artifact with `kind=root`, `status=running`, and no
  `ended_at`;
- one `turn_boundary` artifact with `seq=1`, `status=completed`,
  `started_at=<turn_context timestamp>`, and `ended_at=<last content timestamp>`;
- one `op_boundary` artifact for every source-visible op emitted by the mapper:
  user input, assistant message, reasoning, and each tool call.

For old-format multi-turn rollouts with no `task_started`/`task_complete`, each
new `turn_context.turn_id` that differs from the active turn closes the prior
turn as `completed` at the new `turn_context` timestamp, then opens the next
turn with the next monotone source-derived turn sequence. The final old-format
turn still closes at EOF using the last source-visible content timestamp. The
source manifest must emit one `turn_boundary` per source-derived turn and must
reset op sequencing per turn so canonical `op:<turn_seq>:<op_seq>` artifacts
prove no old-format turn was merged into its neighbor.

For task-started-only new-format rollouts, `event_msg.task_started.turn_id` is
also a source turn boundary. A new `task_started.turn_id` that differs from the
active new-format turn closes the prior turn as `failed` at the new
`task_started` timestamp, marks any dangling ops as `cancelled`, and opens the
next source-derived turn. The matching `task_complete` / `turn_aborted` record
then finalizes the active replacement turn. The source manifest must not merge
the replaced turn's user/assistant/tool artifacts into the replacement turn.

For Codex sub-turn splitting, the source manifest mirrors the adapter's
user-input visualization boundary. Once an active source-derived turn has
already emitted one `user_input` op, a later deduped user prompt in the same
Codex task closes that active turn as `completed` at the later user prompt
timestamp and opens a synthetic source-derived sub-turn with the next monotone
turn sequence. The later user prompt and following assistant/tool artifacts land
in the synthetic sub-turn. If a tool call is still open, the split is deferred so
the tool request and response stay in the same turn; the next user prompt after
the tool resolves performs the split. The source manifest must therefore prove
both that repeated user prompts are not merged into one turn and that
mid-tool-call splits do not orphan a tool response.

For unfinished new-format rollouts at EOF, the source manifest mirrors rule #23
instead of pretending the missing completion marker is harmless:

- If the rollout file mtime is fresh (`now - mtime < 1h`), the active
  `task_started` turn remains a `running` `turn_boundary` with no `ended_at`,
  and the session boundary remains `running`.
- If the rollout file mtime is stale (`now - mtime >= 1h`), the active
  `task_started` turn becomes a `failed` `turn_boundary` with `ended_at` equal
  to the file mtime, dangling tool ops become `cancelled`, and the
  `session_boundary` becomes `failed` with the same `ended_at`. If filesystem
  mtime predates the source turn start because of clock skew or a synthetic test
  fixture, the parity timestamp is floored at the active turn start, matching
  the adapter's EOF finalization rule.

The source extractor must use the same file mtime that the adapter scanner uses
for the stale EOF decision. The parity gate must therefore catch both failure
modes: a fresh in-flight turn incorrectly closed as failed, and a stale crashed
turn incorrectly left running.

### Cost calculation

Codex rollouts do NOT record cost (only tokens). The ingester computes cost via `internal/canonical/pricing.go` keyed on `(provider="openai", model=<turn_context.model>)`. The adapter just emits raw token totals.

### Token accounting nuance

`TokenUsageInfo.total_token_usage` is cumulative for the session, not per-turn. `last_token_usage` is per-LLM-call. The adapter accumulates `last_token_usage` deltas per `turn_id` using `token_count` events between `task_started.turn_id` and `task_complete.turn_id`. If `turn_id` is not present on the `token_count` event (it's a session-level event), the adapter attributes each `token_count` to the most recently active turn (the one whose `task_started` is the latest before this `token_count`).

## Sub-Agent Linkage

Codex supports sub-agents (`SubAgentSource::ThreadSpawn`) and forks (`forked_from_id`). Both produce SEPARATE rollout files under the normal `sessions/YYYY/MM/DD/` tree:

- **Sub-agent**: `session_meta.payload.source = {"subagent": {"thread_spawn": {"parent_thread_id": "<uuid>", "depth": N, "agent_nickname": "...", "agent_role": "..."}}}` and `thread_source = "subagent"`. The parent session's rollout file does NOT inline the child; it appears separately and the parent is identified via the nested `source.subagent.thread_spawn.parent_thread_id`, falling back to top-level `session_meta.payload.parent_thread_id` when the nested source marker does not carry a parent.
- **Fork**: `session_meta.payload.forked_from_id = "<uuid>"` — branched/resumed from another session.
- **`event_msg.collab_agent_spawn_begin`/`_end`** in the PARENT rollout name the spawn but the `_begin` event is NOT persisted (`policy.rs:215`). Only `_end` is. The `_end` event carries the parent→child link as `sender_thread_id` (parent) → `new_thread_id` (child), alongside `new_agent_nickname`, `new_agent_role`, `model`, `reasoning_effort`, and `status`. (Real workstation corpus: 5 `collab_agent_spawn_end` files; the field is `new_thread_id`, NOT `agent_ref.thread_id` as an earlier draft of this spec wrongly stated.)
- **`event_msg.collab_close_end`** (72 files) and **`event_msg.collab_waiting_end`** (74 files) also appear in collab sessions. They carry no parent→child edge the topology view needs, so the adapter recognizes them (no `SourceError`) and surfaces each as a `LogEntry` only — no canonical op. When either event carries `payload.message`, the `LogEntry` message is the exact source message and `Extras.aiViewer.parity` carries `nativeArtifactId=line:<line>:/payload/message`, `selectorURI=file://...#L<line>`, and `jsonPointer=/payload/message`, matching the `event_msg.error` source-backed log parity contract.

Adapter behavior:

- Emit `SessionStartedEvent.ParentNativeID = parent_thread_id` when the child's `session_meta.source` is `subagent`, using nested source parent first and top-level `payload.parent_thread_id` second.
- Emit `SessionStartedEvent.RootNativeID` as the top-level root when the
  session tree is visible. Nested sub-agents must not remain rooted at their
  immediate parent after the parent/root rows exist; the ingester resolver
  repairs any provisional direct-parent root to the parent's resolved root.
- Emit `SessionStartedEvent.ParentNativeID = forked_from_id` otherwise when `forked_from_id` is present.
- In the parent, when an `event_msg.collab_agent_spawn_end` line appears, emit an Op Kind=`session`, Name=`spawn`, ChildSessionNativeID=`new_thread_id`. (If the child rollout file doesn't yet exist at that moment, the ingester's foreign-key constraint must be relaxed temporarily — the canonical-events spec allows out-of-order child arrival.)
- Source-manifest parity mirrors the parent-side spawn event. The source extractor emits both an `op_boundary` for the `session/spawn` op and a `subagent_link` artifact keyed as `op:<turn_seq>:<op_seq>:child_session:<new_thread_id>`. If the child rollout has not landed yet, the canonical side is incomplete until the resolver can link `ops.child_session_id`; silently dropping the link is a P0 parity failure.
- A sub-agent rollout file with `parent_thread_id` referring to an unknown session is recorded with `parent_session_id` set to NULL and a `LogEntry` warning; reconciled when the parent appears.

Real observation: 8 distinct sub-agent sessions in the sampled set, all `depth=1`, with named nicknames (Raman, Tesla, Nash, Boyle, etc.) and role `"explorer"`.

## Edge Cases

1. **Crash mid-stream**: file ends without `task_complete`. Handle per state-machine rule #23 (synthetic finalization after stale mtime).

2. **Multiple `task_started` without intervening `task_complete`**: legitimate (user interrupted and re-prompted). Emit `TurnFinalizedEvent(failed, reason="replaced")` for the previous turn at the timestamp of the new `task_started`.

3. **Old CLI versions (< 0.93.0) lacking `task_started`/`task_complete`**: turn boundaries come only from `turn_context`. Open a turn at each new `turn_context.turn_id`; close it at the next `turn_context` with a different `turn_id` OR at EOF. If `turn_context.turn_id` is absent, fall back to "user message → next user message" heuristic.

4. **Schema additions** (new `payload.type` strings in newer CLI versions): forward-compat via `#[serde(other)]` upstream → unknown variants in Go decoder produce a `LogEntry` warning and pass through as Op Kind=`internal`, Name=`"unknown:" + type` with the raw JSON in Extras.

5. **Sandbox mode `read-only`**: file operations may be `function_call`s that produce `function_call_output` with an error ("operation denied by sandbox"). Adapter emits `OpFinalized.Status="failed"`, `ErrorClass="sandbox_denied"` (heuristic from output string).

6. **Sandbox mode `danger-full-access`**: no parsing difference; surface in Extras only.

7. **Very large reasoning content** (`encrypted_content` can be 50+ KB of base64): keep as PayloadRefEvent pointing at source bytes; never inline raw content into SQLite. Text-bearing nested fields use exact `json_pointer` selectors. Opaque encrypted content may use a whole-record fallback only when the parity availability matrix documents how that opaque artifact is classified.

8. **Token streaming truncated mid-response**: not directly visible in rollout (deltas not persisted, `policy.rs:184,210`). The terminal `agent_message` event arrives only on completion, so a truncated assistant response means no terminal event → handled by edge case #1.

9. **`function_call` without matching `function_call_output`** (e.g. user interrupted): emit `OpStarted` with no matching `OpFinalized`. At `task_complete` or `turn_aborted`, finalize all dangling ops with Status=`cancelled`.

10. **`function_call_output` without matching `function_call`** (corrupt or out-of-order): emit `SourceError` log_entry; skip.

11. **Embedded control characters / ANSI escapes in tool output strings**: codex serializes these as `\uXXXX` JSON escapes. Go's `encoding/json` accepts them. jq's strict mode rejects them — do not test parsing with jq alone.

12. **Legacy `.json` files (pre-mid-2025)**: 19 such files exist on this
workstation. Valid legacy flat files are ingested once during full scan. A file
whose first JSON value is malformed is source corruption with no recovered
artifacts. A file whose first JSON value is a valid flat rollout but has
trailing non-whitespace bytes ingests the valid prefix, emits one `SourceError`
for the trailing corruption, emits one parity `source_corruption` artifact for
the trailing byte range, and makes parity incomplete.

13. **File renamed/moved**: codex does not rename files. If an operator manually renames or moves a rollout file, the adapter sees a Delete event on the old path and Create on the new path; cursor entry for the old path is left stale. Optional cleanup after N days.

14. **Two rollouts with the same `id`**: not observed (0 of the 2,566 modern files on the reference workstation) but theoretically possible (codex could resume into a forked thread). The intended behavior is to treat them as separate canonical sessions keyed on `(source_id, native_id+":"+file_basename)` with a LogEntry warning. **v1 limitation (SOW-0004):** the adapter uses the authoritative `session_meta.payload.id` as `NativeID`, and the ingester upserts sessions on `(source_id, native_id)`, so two same-`id` rollout files would COLLAPSE into one canonical session rather than disambiguate. The basename-disambiguation is deferred to **SOW-0022** (requires cross-file id-collision detection the per-file adapter does not have today). Unobserved edge; no data loss within a single session.

15. **`originator` variants**: observed `codex_cli_rs`, `codex_exec`, `codex-tui`. Treat as identifying string; surface in Extras.

16. **Sub-agent with role="explorer" or other**: just metadata; no parsing difference.

17. **Cwd identifies the project**: `session_meta.cwd` is the de facto project identifier. UI can group sessions by cwd.

18. **`git` block with sensitive `repository_url`**: real files can contain hosted-git SSH URLs with account/repository identity. Fixtures MUST sanitize to a neutral example repository URL that does not resemble a real account identity.

## Canonical Model Gaps

Items in codex that don't map cleanly to canonical-events.md:

> **`turns.extras_json` is now reachable (SOW-0021).** `TurnFinalizedEvent`
> carries an `Extras` field and the ingest writer marshals it into
> `turns.extras_json` (wholesale write, idempotent under re-emit since turns are
> terminal). The codex adapter attaches the per-turn metadata below to the
> `TurnFinalizedEvent.Extras` at finalize — the interim `turn_meta` LogEntry
> SOW-0004 used is removed. The gap items #2/#3/#8 are therefore RESOLVED (the
> values live in `turns.extras_json.{codex_turn_id,sandbox,effort,approval_policy,ttft_ms,last_agent_message}`
> as documented).

1. **Reasoning op as first-class**: covered (`OpKind = 'reasoning'` exists) for `response_item.reasoning`. Codex also persists `event_msg.agent_reasoning` and `event_msg.agent_reasoning_raw_content` as UI companion summaries; those are derived DBG logs only and are not parity `reasoning_text` artifacts.

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
