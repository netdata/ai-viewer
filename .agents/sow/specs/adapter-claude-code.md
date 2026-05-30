# Adapter: claude-code

## 1. Status

**Phase 2 target.** Not a Phase 1 blocker — the operator's primary use case is observing their own ai-agent sessions. Claude Code is the second-priority adapter because its transcripts are the second-largest body of real evidence on the operator's workstation (3,614 `*.jsonl` files spread across 70 project directories observed on 2026-05-26) and the format is closed-source but well-mirrored.

Claude Code is a closed-source CLI produced by Anthropic. The source has been reverse-engineered from leaked npm sourcemaps and is preserved as three `frozen` mirrors under `/opt/baddisk/monitoring/repos/ai/` (per the local mirror registry at `platforms.conf`):

- `jarmuine/claude-code` (frozen) — TypeScript sources, the most complete reverse-engineering. Used as the authoritative source for shape questions in this spec.
- `Kuberwastaken/claude-code` (frozen) — partial, mostly docs and a Rust rewrite.
- `yasasbanukaofficial/claude-code` (frozen) — partial.

This spec is grounded in:

- Direct inspection of 70 project directories and 3,614 root-level `*.jsonl` transcripts plus their `subagents/*.jsonl` sidechains under `~/.claude/projects/` on the operator's workstation (2026-05-26).
- Multi-file inspection across diverse session versions (`2.1.76`, `2.1.126`, `2.1.146`, `2.1.148`, `2.1.150`).
- TypeScript source from the `jarmuine/claude-code` mirror, citation form `jarmuine/claude-code @ <commit>`:
  - `src/utils/sessionStoragePortable.ts` — path sanitization, project-dir discovery.
  - `src/utils/sessionStorage.ts` — append writer, subagent and remote-agent transcript layout.
  - `src/types/logs.ts` — exhaustive `Entry` union and per-record-type fields.

Where reality and the reverse-engineered source diverge, this spec records reality and flags the gap. Where the source declares record types that have never been observed in the operator's data, the spec records both (declared schema = defensive parsing target; observed cardinality = honest test fixtures).

## 2. Source Format

### 2.1 Root Location

The single source-of-truth root is `~/.claude/projects/`, computed as `join(getClaudeConfigHomeDir(), 'projects')` (jarmuine/claude-code `src/utils/sessionStoragePortable.ts:325-326`). The Claude config home is `~/.claude` by default, overridable via `CLAUDE_CONFIG_DIR`.

### 2.2 Project Directory Naming (sanitized cwd)

Under the root, every session is keyed by **sanitized cwd**. The sanitization function (`sessionStoragePortable.ts:311-319`) is:

```typescript
export const MAX_SANITIZED_LENGTH = 200;

export function sanitizePath(name: string): string {
  const sanitized = name.replace(/[^a-zA-Z0-9]/g, '-');
  if (sanitized.length <= MAX_SANITIZED_LENGTH) {
    return sanitized;
  }
  const hash =
    typeof Bun !== 'undefined' ? Bun.hash(name).toString(36) : simpleHash(name);
  return `${sanitized.slice(0, MAX_SANITIZED_LENGTH)}-${hash}`;
}
```

The algorithm is **lossy**:

1. **Every non-alphanumeric character is replaced with `-`** (a single hyphen, not collapsed). The class `[^a-zA-Z0-9]` matches `/`, `.`, `_`, `-`, `:`, space, every Unicode codepoint outside ASCII, etc.
2. **For paths whose sanitized form exceeds 200 characters**, a hash suffix is appended: `<first-200-chars>-<hash-base36>`. The hash algorithm differs between Bun (CLI) and Node (SDK) — they produce **different directory names for the same long path**. The CLI's `findProjectDir` (`sessionStoragePortable.ts:354-`) falls back to prefix-scan matching to tolerate this; the adapter must do the same when locating a session by cwd.
3. **Canonicalization**: before sanitization, Claude Code resolves the cwd with `realpath()` + NFC normalization (`canonicalizePath`, `sessionStoragePortable.ts:339-344`). On macOS this collapses `/tmp` → `/private/tmp`. On Linux, it follows symlinks. The adapter does not need to invert sanitization (see §2.4); it just needs to read what's there.

#### Concrete observed mappings

| cwd from inside the jsonl | encoded dir name |
|---|---|
| `/home/operator` | `-home-operator` |
| `/home/operator/.agents/skills/manjaro-updates` | `-home-operator--agents-skills-manjaro-updates` |
| `/home/operator/src/ai-agent.git` | `-home-operator-src-ai-agent-git` |
| `/home/operator/src/llama.cpp.git` | `-home-operator-src-llama-cpp-git` |
| `/home/operator/src/dashboard/netdata-cloud-frontend.git` | `-home-operator-src-dashboard-netdata-cloud-frontend-git` |
| `/tmp/nefeli` | `-tmp-nefeli` |
| `/opt/baddisk/monitoring` | `-opt-baddisk-monitoring` |

Note the double-hyphen `--agents` arising from the leading `.` of `.agents`: every original `.` and `/` becomes a hyphen, with no collapsing.

#### Lossiness implications

The encoded name **does not uniquely round-trip to a cwd**. For example, `/home/operator/ai-agent.git` and `/home/operator/ai-agent/git` and `/home/operator/ai_agent_git` all sanitize to `-home-operator-ai-agent-git`. The viewer **must read the authoritative cwd from inside the jsonl records** (see §3) and treat the directory name as a presentation/index aid only.

### 2.3 Session File Layout

Each session is a UUIDv4 `sessionId`. For the **main session transcript** (`getTranscriptPathForSession`, jarmuine `src/utils/sessionStorage.ts:218-224`):

```text
~/.claude/projects/<sanitized-cwd>/<sessionId>.jsonl
```

For **subagent (sidechain) transcripts** spawned by the `Agent` tool (`getAgentTranscriptPath`, jarmuine `src/utils/sessionStorage.ts:247-258`):

```text
~/.claude/projects/<sanitized-cwd>/<sessionId>/subagents/agent-<agentId>.jsonl
~/.claude/projects/<sanitized-cwd>/<sessionId>/subagents/agent-<agentId>.meta.json
```

Optionally, when workflow runs group related agents:

```text
~/.claude/projects/<sanitized-cwd>/<sessionId>/subagents/<subdir>/agent-<agentId>.jsonl
```

Additionally, large tool results may be spilled to per-session files under the session dir (`tool-results/<id>.txt`):

```text
~/.claude/projects/<sanitized-cwd>/<sessionId>/tool-results/<id>.txt
```

These spill files are referenced by `attachment` records of type `compact_file_reference` and are NOT directly part of the canonical event stream — the adapter treats them as out-of-band payload artifacts (see §5 and §11).

### 2.4 File Creation, Append, and Permission

- Files are created with `mkdir mode 0o700` and `appendFileSync mode 0o600` (jarmuine `sessionStorage.ts:2575-2583`). Append-only; never truncated mid-stream in normal operation.
- One record = one JSON object on one line, terminated by `\n`, serialized via `JSON.stringify` (`sessionStorage.ts:2577`). The adapter must not assume pretty-printed JSON.
- Re-append is used for **trailing metadata snapshots** (`reAppendSessionMetadata`, `sessionStorage.ts:766-839`). These rewrite a handful of last-wins fields (`last-prompt`, `custom-title`, `tag`, etc.) at session end so a tail-only read can recover them without scanning the whole file. Implication: the same record type can appear many times; only the LAST occurrence is authoritative for those types.
- Files can grow to **multiple gigabytes**; the producer's own readers cap at `MAX_TRANSCRIPT_READ_BYTES = 50 MB` and bail above that (`sessionStorage.ts:229`). The adapter must stream-parse line by line and never load whole files into memory.

### 2.5 Cwd vs Session Identity

The encoded-cwd directory is the **launch cwd**. Sessions can change cwd at runtime (Bash `cd`, etc.); the `cwd` field inside each record reflects the current cwd at write time. Observed: a single session in `-opt-baddisk-monitoring/<sessionId>.jsonl` had records with two distinct `cwd` values across its lifetime (`/opt/baddisk/monitoring` and a deeper subdir).

## 3. JSONL Record Contract

Each line in any `.jsonl` (main or subagent) is one JSON object with a `type` discriminator. The producer's union is `Entry` (jarmuine `src/types/logs.ts:297-317`). Observed types across all 3,614 transcripts plus their subagents, ranked by raw cardinality (sampled all files):

| `type` value | rough share (real data) | category |
|---|---|---|
| `assistant` | ~37% | message |
| `user` | ~25% | message |
| `last-prompt` | ~6% | session metadata snapshot |
| `system` | ~5% | event |
| `ai-title` | ~5% | session metadata snapshot |
| `attachment` | ~5% | attached context |
| `queue-operation` | ~4% | event |
| `permission-mode` | ~4% | session metadata snapshot |
| `file-history-snapshot` | ~2% | session metadata snapshot |
| `pr-link` | <1% | session metadata snapshot |
| `bridge-session` | <1% | session metadata snapshot |
| `custom-title` | <1% | session metadata snapshot |

The producer's `Entry` union additionally declares record types not observed in the operator's data; the adapter MUST tolerate them on read:

- `summary` (`SummaryMessage`, `logs.ts:55-59`) — `{type, leafUuid, summary}`. A standalone conversation summary (distinct from compaction's `compact_boundary` system record).
- `task-summary` (`TaskSummaryMessage`, `logs.ts:93-98`) — periodic fork-generated summary written every `min(5 steps, 2min)` so `claude ps` can show what an agent is doing. `{type, sessionId, summary, timestamp}`.
- `tag` (`TagMessage`, `logs.ts:100-104`).
- `agent-name`, `agent-color`, `agent-setting` (`logs.ts:106-122`).
- `mode` (`ModeEntry`, `logs.ts:137-141`) — `'coordinator' | 'normal'`.
- `worktree-state` (`WorktreeStateEntry`, `logs.ts:167-171`) — last-wins persistence of git-worktree state.
- `content-replacement` (`ContentReplacementEntry`, `logs.ts:181-186`) — replays large-content stubs on resume.
- `attribution-snapshot` (`AttributionSnapshotMessage`, `logs.ts:208-219`) — per-file character contribution counts; used for git commit attribution.
- `speculation-accept` (`SpeculationAcceptMessage`, `logs.ts:233-237`) — speculation/prediction accepted; carries `timeSavedMs`.
- `marble-origami-commit`, `marble-origami-snapshot` (`logs.ts:255-295`) — internal context-collapse mechanism (the discriminator is obfuscated in source; same comment notes "the gate name [...] would leak into external builds via the appendEntry dispatch"). The adapter should ignore these record types.

Records carry a shared envelope plus a per-type body. Common envelope fields (from `SerializedMessage` + `TranscriptMessage` in `logs.ts:8-17, 221-231`):

| field | type | semantics | required |
|---|---|---|---|
| `type` | string | discriminator | yes |
| `uuid` | UUID | unique record id | yes for message/event types; absent on metadata-snapshot types |
| `parentUuid` | UUID \| null | uuid of the predecessor in the logical chain; `null` for the first message of the session or sidechain | yes for `user`/`assistant`/`system`/`attachment` |
| `logicalParentUuid` | UUID \| null | preserved logical parent when `parentUuid` is nullified at session breaks (e.g. compaction) | optional |
| `isSidechain` | bool | `true` for subagent records; `false` for main session | yes for message/event types |
| `agentId` | string | subagent id (15-hex-char, prefixed `a` in filename: `agent-<agentId>.jsonl`); present only on sidechain records | sidechain only |
| `promptId` | string | correlates with OTel `prompt.id`; appears on user-prompt records and the first sidechain record | when applicable |
| `sessionId` | UUID | the **main session** UUID — subagent records carry the same `sessionId` as their parent | yes |
| `cwd` | string | current working directory at write time (may differ from launch cwd) | yes for message/event types |
| `userType` | string | usually `"external"` | optional |
| `entrypoint` | string | `CLAUDE_CODE_ENTRYPOINT` — observed values: `"cli"`, `"claude-desktop"` | optional |
| `timestamp` | ISO-8601 string | record write time (UTC) | required for message/event types; absent on metadata-snapshot types |
| `version` | string | Claude Code version (e.g. `"2.1.150"`) | yes for message/event |
| `gitBranch` | string | git branch at write time | optional |
| `slug` | string | session slug used to derive sibling file paths (plans, etc.) | optional |
| `isMeta` | bool/null | `true` for synthetic user messages that should be ignored when computing turn semantics (e.g. `<local-command-caveat>` markers) | optional |
| `isCompactSummary` | bool/null | `true` for the synthetic post-compaction summary user message | optional |
| `requestId` | string | Anthropic API `req_...` id; present on `assistant` | optional |
| `teamName`, `agentName`, `agentColor` | string | swarm/team metadata | optional |

**Records that LACK `timestamp` and `uuid`** (verified across all data): `ai-title`, `custom-title`, `last-prompt`, `permission-mode`, `bridge-session`, `file-history-snapshot`. These are last-wins metadata snapshots, not events on the timeline. The adapter must treat them as session-property updates without contributing events with `Ts`.

### 3.1 `user` records

```json
{
  "type": "user",
  "uuid": "<uuid>",
  "parentUuid": "<uuid|null>",
  "isSidechain": false,
  "promptId": "<uuid|absent>",
  "isMeta": null,
  "isCompactSummary": null,
  "message": {
    "role": "user",
    "content": "<string>" | [ <block>, ... ]
  },
  "timestamp": "<iso8601>",
  "userType": "external",
  "entrypoint": "cli",
  "cwd": "<abs path>",
  "sessionId": "<uuid>",
  "version": "2.1.x",
  "gitBranch": "<branch>",
  "toolUseResult": <see below>
}
```

`message.content` is **polymorphic**:

- **string** — operator-typed prompt. The first such record in a sidechain is the Agent tool's prompt.
- **array of content blocks** — almost always `tool_result` blocks responding to a previous `assistant.tool_use`. Content-block types observed: `tool_result` (~99.6%), `text` (operator-injected text mid-tool-cycle, ~0.4%), `image` (3 of ~40,000 — operator-pasted images).

`tool_result` block shape (Anthropic content-blocks API):
```json
{ "type": "tool_result", "tool_use_id": "toolu_...", "content": "<string>" | [<block>...], "is_error": false }
```

`toolUseResult` (top-level sibling of `message`) is a **structured echo** of the tool result, distinct from the block. It's an object whose keys vary by tool:

- `Bash`: `{ interrupted, isImage, noOutputExpected, stderr, stdout }`
- `Read`: `{ file: { ... }, type: "text" }`
- `Edit`: `{ filePath, newString, oldString, originalFile, replaceAll, structuredPatch, userModified }`
- `Write`: `{ content, filePath, originalFile, structuredPatch, type, userModified }`
- `TaskCreate`/`TaskUpdate`: `{ statusChange, success, taskId, updatedFields }` (operator's internal task list, NOT subagent task)
- `TaskList`: `{ tasks }`
- `ToolSearch`: `{ matches, query, total_deferred_tools }`
- raw string: a bare error message (when the tool stack returns a string instead of structured output)

The adapter should treat `toolUseResult` as opaque structured payload for ops of `kind='tool'`; the `tool_use_id` from the corresponding block is the canonical join key back to the `assistant.tool_use`.

### 3.2 `assistant` records

```json
{
  "type": "assistant",
  "uuid": "<uuid>",
  "parentUuid": "<uuid>",
  "isSidechain": false,
  "requestId": "req_...",
  "message": {
    "id": "msg_...",
    "type": "message",
    "role": "assistant",
    "model": "claude-opus-4-7",
    "stop_reason": "tool_use" | "end_turn" | "stop_sequence" | ...,
    "stop_sequence": null,
    "stop_details": null,
    "usage": { ... },
    "content": [ <block>, ... ],
    "diagnostics": null
  },
  "timestamp": "<iso8601>",
  ...envelope
}
```

`message.content` blocks observed: `text`, `thinking`, `tool_use` (39,566 observed across all data), in roughly 11:16:40 ratio.

- `thinking` block: `{ type: "thinking", thinking: "<text>", signature: "<base64>" }` — the model's extended-thinking output. `signature` is a cryptographic seal; the adapter should preserve it but not treat its contents as meaningful.
- `tool_use` block: `{ type: "tool_use", id: "toolu_...", name: "<tool>", input: { ... }, caller: { type: "direct" } }`. The `name` is the canonical tool identifier; `id` joins to a later `user.toolUseResult` and/or `tool_result` block. `caller.type` is `"direct"` for tools the model invoked directly.
- `text` block: `{ type: "text", text: "<assistant prose>" }` — the user-visible answer.

`message.usage` is the accounting envelope. Observed shape:
```json
{
  "input_tokens": <int>,
  "output_tokens": <int>,
  "cache_creation_input_tokens": <int>,
  "cache_read_input_tokens": <int>,
  "cache_creation": { "ephemeral_1h_input_tokens": <int>, "ephemeral_5m_input_tokens": <int> },
  "server_tool_use": { "web_search_requests": <int>, "web_fetch_requests": <int> },
  "service_tier": "standard" | null,
  "inference_geo": "" | null,
  "iterations": [ { "type": "message", ...per-iteration breakdown... } ],
  "speed": "standard" | null
}
```

**Cost is NOT recorded.** Grep across all observed transcripts found zero occurrences of `cost_usd`, `costUsd`, or `totalCost`. The adapter must compute cost downstream via `internal/canonical/pricing.go`'s static `(provider, model)` table.

#### Synthetic assistant messages

`message.model` is normally a Claude model id (observed: `"claude-opus-4-7"`, `"claude-opus-4-6"`). It may also be the literal string `"<synthetic>"` — Claude Code's marker for assistant turns it injects locally (not from the LLM). These records still have `content` blocks (often a `text` block with a status/error message) and zero usage. The adapter must:
- Treat `<synthetic>` as `Provider=anthropic, Model="<synthetic>"` so it doesn't pollute model statistics; OR
- Tag with `extras_json.synthetic=true` and skip the LLM-op emission entirely. **This spec chooses the second**: synthetic assistant messages are emitted as a `LogEntry` with `severity=INF` and no `OpStarted/OpFinalized` pair, because they do not represent a real model call.

### 3.3 `system` records

```json
{
  "type": "system",
  "subtype": "<see below>",
  "uuid": "<uuid>",
  "parentUuid": "<uuid|null>",
  "logicalParentUuid": "<uuid|null>",
  "isSidechain": false,
  "isMeta": false,
  "content": "<string>",
  "timestamp": "<iso8601>",
  ...envelope,
  ...subtype-specific fields
}
```

Observed `subtype` values:

- `stop_hook_summary` — a session-stop hook fired. Body includes `hookCount`, `hookInfos[]`, `hookErrors[]`, `preventedContinuation`, `stopReason`, `level`, `toolUseID`. Useful for plugin/hook visibility.
- `api_error` — an Anthropic API call failed. Body: `error{status, headers, requestID, type}`, `retryInMs`, `retryAttempt`, `maxRetries`. Maps to a failed LLM op (see §5).
- `compact_boundary` — **compaction marker** (see §9). Body: `compactMetadata{trigger, preTokens, postTokens, durationMs, preservedSegment{headUuid, anchorUuid, tailUuid}, preservedMessages{anchorUuid, uuids[]}}`. `trigger` observed values: `"manual"`.
- `turn_duration` — emitted at the end of a turn. Body: `durationMs`, `messageCount`. The adapter uses this as a definitive **turn boundary signal** for the preceding turn.
- `local_command` — a `/`-prefixed local CLI command ran. Body varies.
- `informational` — generic notice; body is just `content`.
- `away_summary` — agent went idle; body summarizes activity.
- `bridge_status` — see `bridge-session` (§3.10).
- `scheduled_task_fire` — a scheduled task triggered.

### 3.4 `attachment` records

```json
{
  "type": "attachment",
  "uuid": "<uuid>",
  "parentUuid": "<uuid>",
  "isSidechain": false,
  "attachment": { "type": "<see below>", ...payload },
  "timestamp": "<iso8601>",
  ...envelope
}
```

`attachment.type` values observed and their semantics:

- `file` — operator attached a file: `{type, filename, displayPath, content:{type:"text", file:{filePath, content}}}`. A bare file attachment is turn-context the harness injected, NOT a tool op; the canonical model has no op to own it, and `payload_refs.op_id` is `NOT NULL REFERENCES ops(id)` (`migrations/0001_initial.sql:147`). The adapter therefore does NOT emit a `PayloadRefEvent` for a `file` attachment (an orphan ref would reference a non-existent op and roll back the ingest batch). Instead it records `filename`, `displayPath`, and the attachment `type` in the attachment `LogEntry`'s extras (§333, §338), so the attached file is still visible in the UI.
- `directory` — operator attached a directory listing.
- `opened_file_in_ide` — IDE signal that operator opened a file: `{type, filename}`.
- `edited_text_file` — IDE signal that operator edited a file outside the agent's edits.
- `task_reminder` / `todo_reminder` / `task_status` — internal TODO/task list reminders the harness injects into the model's context.
- `queued_command` — a command waiting in the operator's input queue.
- `skill_listing` — list of skills available to the model (Anthropic Skills feature).
- `invoked_skills` — record of which skills were activated and their full content.
- `deferred_tools_delta` — diff to the deferred-tool list (which tools are available via `ToolSearch`).
- `mcp_instructions_delta` — diff to the MCP-server instructions context.
- `diagnostics` — IDE diagnostics (linter errors, etc.).
- `date_change` — the calendar day changed mid-session.
- `compact_file_reference` — **reality (verified `jarmuine/claude-code @ <commit> :: src/utils/attachments.ts:307-312, 3136`)**: the record carries `{type, filename, displayPath}` where `filename` is the **original project file** the model read (a CWD-relative or absolute path under the operator's project, e.g. `/home/user/src/x.go`), NOT a spill file under `<sessionDir>/tool-results/`. `displayPath` is the CWD-relative form for display. The earlier draft of this spec assumed a `tool-results/<id>.txt` spill path; that is the layout for Bash/PowerShell oversized-output spills (`BashTool.tsx:292`, `toolResultStorage.ts:27`), referenced by `persistedOutputPath`, not by `compact_file_reference`. Because the referenced file lives **outside the configured projects root**, the adapter does NOT emit a servable `PayloadRefEvent` for it (a read-only viewer serves only files under its source root; pointing a payload at an arbitrary project path would fail the §6.1 containment guard). The adapter records the `displayPath` in the attachment `LogEntry`'s extras so the reference is still visible in the UI, and leaves payload-on-demand for these to a future SOW if the operator wants project-file serving.
- `nested_memory` — memory directive injected from a nested `CLAUDE.md`.
- `command_permissions` — permission-prompt history.
- `ultrathink_effort` — extended-thinking effort indicator.

Most attachments are content the harness injects into the model's context, NOT operator-visible UI events. The adapter emits them as `LogEntry` rows with `severity=DBG` (so they don't dominate the timeline). For a `file` attachment it additionally records `filename`, `displayPath`, and the attachment `type` in the `LogEntry`'s extras so the reference is visible without a backing payload row. The adapter does NOT emit a `PayloadRefEvent` for any attachment subtype: a `file` attachment has no owning op (a payload row's `op_id` is `NOT NULL REFERENCES ops(id)`, so an orphan ref would roll back the ingest batch), and `compact_file_reference` targets a project file outside the served root (§3.4). Payload-on-demand for attached content is left to a future SOW.

### 3.5 `queue-operation` records

```json
{
  "type": "queue-operation",
  "operation": "enqueue" | "dequeue",
  "timestamp": "<iso8601>",
  "sessionId": "<uuid>",
  "content": "<the queued user prompt, on enqueue>"
}
```

Operator queues prompts (Shift+Enter) while the agent is working. Each pair `(enqueue, dequeue)` for a given prompt forms a small lifecycle. No `uuid`. Out of scope for canonical events in Phase 2 — but stored as `LogEntry` `INF` so the UI can show "queued at HH:MM:SS, dequeued at HH:MM:SS".

### 3.6 `last-prompt` records

```json
{
  "type": "last-prompt",
  "lastPrompt": "<string>",
  "leafUuid": "<uuid>",
  "sessionId": "<uuid>"
}
```

Re-appended near EOF for tail readers. **No timestamp**. The adapter treats the LAST occurrence as the session's "last user prompt" snapshot — written to `sessions.extras_json.lastPrompt`. Does not contribute an event.

### 3.7 `custom-title` / `ai-title` records

```json
{ "type": "custom-title", "customTitle": "<string>", "sessionId": "<uuid>" }
{ "type": "ai-title",    "aiTitle":    "<string>", "sessionId": "<uuid>" }
```

No timestamp. Last-wins. `custom-title` is user-set and wins over `ai-title` for display (`logs.ts:67-74`). The adapter promotes the surviving title to the session's `AgentName`. Precedence is **custom-title over ai-title regardless of arrival order**: once a `custom-title` has set the AgentName, a later `ai-title` does NOT overwrite it (the writer's `COALESCE(NULLIF(excluded.agent_name,''), agent_name)` would otherwise let a trailing `ai-title` clobber the user's chosen title). The adapter enforces this by not emitting an `ai-title` AgentName update once a `custom-title` has been seen on the file.

### 3.8 `permission-mode` records

```json
{ "type": "permission-mode", "permissionMode": "default" | "acceptEdits" | "bypassPermissions" | "plan", "sessionId": "<uuid>" }
```

No timestamp. Last-wins. Stored in `sessions.extras_json.permissionMode`. **Sensitive**: a `"bypassPermissions"` mode means the operator authorized YOLO mode in this session. Fixtures must redact.

### 3.9 `pr-link` records

```json
{
  "type": "pr-link",
  "sessionId": "<uuid>",
  "prNumber": <int>,
  "prUrl": "<url>",
  "prRepository": "owner/repo",
  "timestamp": "<iso8601>"
}
```

Has `timestamp` but no `uuid` or `parentUuid`. Records the session-to-PR linkage (when `gh pr create` ran). A session may produce multiple PRs, so the adapter accumulates every `pr-link` seen on the file into `sessions.extras_json.prLinks[]` (an array of `{prNumber, prUrl, prRepository}`) and emits a `SessionUpdatedEvent` carrying the FULL array each time a new `pr-link` arrives. The ingester overwrites the `prLinks` key wholesale (json_patch), so the full-array emission is required — a singular per-PR object would clobber the previous PRs and only the last would survive. On a resume the chain replays from offset 0, so the re-emitted final array is complete (last-wins on the whole array).

### 3.10 `bridge-session` records

```json
{
  "type": "bridge-session",
  "sessionId": "<uuid>",
  "bridgeSessionId": "cse_...",
  "lastSequenceNum": <int>
}
```

No timestamp, no uuid. Records the link between the local Claude Code session and a backing Anthropic "Claude Server Engine" remote session (`cse_*`). The `lastSequenceNum` checkpoints how much of the server-side session has been mirrored locally. The adapter stores into `sessions.extras_json.bridge` for traceability.

### 3.11 `file-history-snapshot` records

```json
{
  "type": "file-history-snapshot",
  "messageId": "<uuid>",
  "snapshot": {
    "messageId": "<uuid>",
    "trackedFileBackups": { "<path>": { "backupFileName": "<path or null>", "version": <int>, "backupTime": "<iso8601>" }, ... },
    "timestamp": "<iso8601>"
  },
  "isSnapshotUpdate": <bool>
}
```

No top-level `timestamp` (the inner `snapshot.timestamp` is the write time). Records the producer's tracked-file backup state for Edit/Write undo. The adapter stores the actual `snapshot.trackedFileBackups` map under `sessions.extras_json.fileHistory` when non-empty (last-non-empty wins, mirroring the other last-wins snapshots), so the UI can show which files the session backed up — not merely a boolean that a snapshot existed.

### 3.12 Records observed only in source, not in real data

- `summary` (`SummaryMessage`) — possibly produced by `/compact` in older versions.
- `task-summary` — produced by `claude ps` instrumentation; off by default in observed installs.
- `tag`, `agent-name`, `agent-color`, `agent-setting`, `mode`, `worktree-state`, `content-replacement`, `attribution-snapshot`, `speculation-accept`, `marble-origami-commit`, `marble-origami-snapshot`.

Defensive policy: any unknown `type` value triggers a `SourceError` event with severity `INF` (informational; not blocking). The adapter surfaces **exactly one `SourceError` per distinct unknown `type` value per scan** (deduped on the `type` string), not one per occurrence — a transcript with thousands of records of one unrecognized type must not flood `/health` with thousands of identical errors. The dedup set lives on the per-file mapper; a Tail resume that re-reads the same file re-arms the set (the warning re-fires once on the resumed pass, absorbed downstream).

## 4. Subagent (Sidechain) Records

Claude Code's `Agent` tool spawns a subagent. The subagent's full transcript lives in a sibling file under the parent session directory, NOT inline in the parent.

### 4.1 File layout (recap)

```text
<parent>.jsonl                                           # parent transcript, contains an `assistant.tool_use` with name="Agent"
<sessionId>/subagents/agent-<agentId>.jsonl              # subagent transcript
<sessionId>/subagents/agent-<agentId>.meta.json          # subagent metadata sidecar
```

`agentId` is a 15-hex-character identifier the producer generates; the filename is `agent-<agentId>.jsonl`.

### 4.2 Sidecar metadata

`agent-<agentId>.meta.json` shape (jarmuine `sessionStorage.ts:264-272`, observed):

```json
{
  "agentType": "general-purpose",
  "description": "<task description from Agent tool input>",
  "toolUseId": "toolu_..."
}
```

- `agentType`: the subagent definition name (e.g. `general-purpose`, `Explore`, custom names from `~/.claude/agents/`).
- `description`: copied from the `Agent` tool input — short human-readable label.
- `toolUseId`: **the canonical parent→child link**. It equals the `id` of the `assistant.tool_use` block in the parent's transcript that spawned this subagent.

A second sidecar shape exists for remote agents (`RemoteAgentMetadata`, `sessionStorage.ts:305-318`):
```json
{ "taskId": "...", "remoteTaskType": "...", "sessionId": "...", "title": "...", "command": "...",
  "spawnedAt": <ms>, "toolUseId": "...", "isLongRunning": <bool>, ... }
```

### 4.3 Subagent record envelope

Subagent records share the same shape as main records. Distinguishing fields:

- `isSidechain: true` — set on every record in the subagent jsonl.
- `agentId: "<agentId>"` — present on every record; equals the filename's `agentId`.
- `sessionId` — **identical to the parent's `sessionId`**. The viewer must NOT treat the subagent as having its own root session id; it shares the parent's session identity.
- `parentUuid` of the FIRST record is `null` (the prompt root). All subsequent records chain by `parentUuid`.
- `promptId` differs from the parent's `promptId` — it's the subagent's local prompt id.

### 4.4 Result return path

The subagent's "result" is never copied back into the parent's jsonl. The parent's `assistant.tool_use` for `Agent` has NO matching `tool_result` block in the parent's records (verified by tool-id grep across the ai-viewer's own session: 1 occurrence of the Agent's `toolu_id` instead of the typical 2). The result is implicit: it equals the LAST `assistant` record with `content[0].type == "text"` in `agent-<agentId>.jsonl`.

This is the most important structural difference from ai-agent v3 and must be encoded in the adapter:

1. While streaming the parent's jsonl, on each `assistant.tool_use` with `name == "Agent"`, emit `OpStartedEvent` with `kind='session'`, capture the `toolu_id`.
2. The corresponding `OpFinalizedEvent` is deferred until the subagent's jsonl reports completion (its last assistant record or end-of-file with no more activity).
3. Read the sidecar `.meta.json` to recover `agentType` (the subagent's effective "agent name") and the `toolUseId` join key.
4. Emit a separate canonical Session for the subagent: `SessionStartedEvent` with `Kind='sub_agent'`, `ParentNativeID=<parent sessionId>` and the synthetic `NativeID=<parent sessionId>:agent:<agentId>` (see §5.1, §5.2), `AgentName=<agentType>`.

## 5. Mapping to Canonical Events

The canonical model (`canonical-events.md`) is session/turn/op shaped. Claude Code's JSONL is uuid-chained messages without explicit turn or op boundaries. The adapter synthesizes them.

### 5.1 Native id strategy

| canonical concept | claude-code source |
|---|---|
| `SessionStartedEvent.NativeID` (main) | the file's `sessionId` (UUIDv4) |
| `SessionStartedEvent.NativeID` (subagent) | `<parent sessionId>:agent:<agentId>` (synthetic) |
| `SessionStartedEvent.ParentNativeID` (subagent) | `<parent sessionId>` |
| `TurnStartedEvent.SessionNativeID` | as above |
| `OpStartedEvent.Seq`, `TurnStartedEvent.Seq` | adapter-synthesized 1-based monotonic counter |
| `SourceSeq` | adapter's deterministic per-event identifier (stable across rescans); observability counter, not a dedup gate |
| `OpStartedEvent.Ts` | parsed `timestamp` (ISO-8601) → microseconds UTC |

The subagent's NativeID is NOT the parent's `sessionId` (they would collide); the synthetic `:agent:<agentId>` suffix uniquifies. The `agentId` is durable across resumes (same subagent dir is appended on resume).

### 5.2 Session bootstrapping

A `SessionStartedEvent` is emitted on the FIRST observed record in a file, with `Ts = first record's timestamp`. Fields:

- `Kind`: `'root'` for main session jsonls; `'sub_agent'` for `subagents/agent-*.jsonl` files.
- `AgentName`: for main sessions, the `customTitle` if seen, else `aiTitle` if seen, else empty; for subagents, the `agentType` from `.meta.json`.
- `Model`: empty initially. A `SessionUpdatedEvent` is emitted on the first `assistant` record carrying a non-`<synthetic>` model.
- `Extras`: `{ cwd, version, entrypoint, gitBranch, permissionMode (when seen), customTitle, aiTitle, lastPrompt, prLinks, bridge, slug }`.

### 5.3 Turn boundary inference

Claude Code does not write explicit turn records. The adapter infers turns from the message chain:

- A **new turn starts** at any `user` record whose `message.content` is a STRING (operator-typed prompt) AND `isMeta != true` AND `isCompactSummary != true`. Each such record marks the start of turn N+1.
- A turn is implicitly **finalized** when (a) the next turn starts, (b) a `system.subtype="turn_duration"` record arrives carrying the turn's `durationMs`, or (c) EOF of the active stream.
- The first user record of a subagent jsonl always opens turn 1 of the sidechain (its content is a string from the `Agent` tool input).

`TurnStartedEvent.Seq` is the 1-based counter. `TurnFinalizedEvent` uses the `turn_duration` body when present (`tokens` derived from the sum of `assistant.message.usage.input_tokens + output_tokens` across the turn), else computes `EndTs` from the LAST record before the next turn opens.

### 5.4 Per-record mapping

| Source record | Canonical event(s) emitted |
|---|---|
| First record in file | `SessionStartedEvent` (once per file) |
| `user` with string content (non-meta, non-compact) | `TurnStartedEvent(Seq=N+1)` |
| `user` with array content (`tool_result` blocks) | one `OpFinalizedEvent` per `tool_result` block, matched by `tool_use_id`; plus one `PayloadRefEvent` (`PayloadKind='tool_response'`, `Format='text'`, `LocationURI=file://<transcript>`) for the `toolUseResult` body when present, matched to the finalized tool op's `Seq` |
| `user` with `isMeta==true` (`<local-command-caveat>` etc.) | `LogEntry` `DBG`; no turn/op events |
| `user` with `isCompactSummary==true` | `LogEntry` `INF` carrying the post-compaction summary, plus a `PayloadRefEvent` (`PayloadKind='log'`, `Format='text'`, `LocationURI=file://<transcript>`) pointing at the summary text so the UI can render it in a compaction lane. The payload is scoped to the **compaction op** that immediately precedes the summary (the same `(TurnSeq, OpSeq)` the `compact_boundary` synthetic op was emitted under), because `payload_refs.op_id` is `NOT NULL REFERENCES ops(id)` — a payload must reference an op that exists, and the compaction op is the natural owner of its own summary. The drop guard keys on `OpSeq == 0` only (the real "no owning op" sentinel): a compaction op legitimately exists at turn 0 (a `/compact` before any operator prompt), so a turn-0 compaction summary is still emitted, scoped to that turn-0 compaction op (§9.2). |
| `assistant` (model != `<synthetic>`) | `OpStartedEvent(Kind='llm', Model, Provider='anthropic', Name=Model)` covering the LLM call; for each `tool_use` block in `content[]`, an additional `OpStartedEvent(Kind='tool', Name=name, ToolNamespace=mcp_server or '')`; `OpFinalizedEvent` for the LLM op with tokens from `message.usage` |
| `assistant` (model == `<synthetic>`) | `LogEntry` `INF`; no LLM op emitted |
| `assistant.content[].type=="thinking"` | nested `OpStartedEvent`/`OpFinalizedEvent` with `Kind='reasoning'`, `ParentOpSeq=<the LLM op>`, `BytesOut=len(thinking)` |
| `assistant.tool_use` with `name=="Agent"` | `OpStartedEvent(Kind='session', Name=description, Extras.aiViewer.toolUseId=<the Agent tool_use block id>)`, ALWAYS carrying the `toolUseId` stash (the meta-independent parent→child join key, §8.1); plus `ChildSessionNativeID=<parent sessionId>:agent:<agentId>` WHEN the sidecar `.meta.json` is already known at map time. The `OpFinalizedEvent` is deferred until the spawned subagent sidechain is fully read AND its terminal record is an assistant-text completion marker (see §8.1, §485). The parent transcript has NO `tool_result` for the Agent tool, so the op is finalized from the child's end state, not from a `tool_result` block. When the linking `.meta.json` lands after the parent's `Agent` block was already tailed, the op→child link is repaired by the resolver matching the op's stashed `toolUseId` to the child session's stashed `toolUseId` — NO transcript re-read, no catalog double-count (§8.1). |
| Each subagent jsonl file | a separate canonical Session (`Kind='sub_agent'`, parent linkage via `ParentNativeID`, `Extras.aiViewer.toolUseId=<the child's .meta.json.toolUseId>` when the sidecar is known — the join key the resolver matches against the parent op's stashed `toolUseId`, §8.1); turn/op inference identical to main session |
| `system.subtype=="turn_duration"` | `TurnFinalizedEvent` for the just-completed turn |
| `system.subtype=="api_error"` | `LogEntry` severity `ERR`; if the next record is a synthetic assistant or absence-of-assistant, mark the in-flight LLM op `status='failed', error_class=api_error_<status>` |
| `system.subtype=="compact_boundary"` | BOTH a `LogEntry` `INF` (Message `compact_boundary`, Extras carry the full `compactMetadata`) AND a synthetic op `OpKind='compaction'` carrying `Ts=record.timestamp`, `EndTs=Ts+durationMs*1000`, `BytesIn=preTokens`, `BytesOut=postTokens`, and `Extras=` the FULL `compactMetadata` (`trigger`, `preTokens`, `postTokens`, `durationMs`, AND `preservedSegment` + `preservedMessages`). Subsequent records' `parentUuid` may be `null` (post-compaction chain restart); the adapter must accept that without error. See §9. |
| `system.subtype=="stop_hook_summary"` | `LogEntry` `DBG` (one per hook) plus aggregate `LogEntry` `INF`; no canonical op |
| `system.subtype=="local_command"` | `LogEntry` `INF` |
| `system.subtype=="informational"` | `LogEntry` `INF` |
| `attachment` (any subtype) | `LogEntry` `DBG`. For a `file` subtype the LogEntry's extras additionally carry `filename`, `displayPath`, and the attachment `type` (§333, §338). NO `PayloadRefEvent` is emitted for any attachment subtype: a `file` attachment has no owning op (and `payload_refs.op_id` is `NOT NULL REFERENCES ops(id)`, so an orphan ref rolls back the batch), and `compact_file_reference` targets a path outside the served root. See §3.4. |
| `queue-operation` | `LogEntry` `INF` |
| `last-prompt` | UPDATE `sessions.extras_json.lastPrompt`; no event (no `Ts`) — implemented as last-wins in the adapter's in-memory state, flushed on `SourceProgress` |
| `custom-title` / `ai-title` | UPDATE `sessions.extras_json.title` (custom wins) |
| `permission-mode` | UPDATE `sessions.extras_json.permissionMode` |
| `pr-link` | accumulate into `sessions.extras_json.prLinks[]` and emit a `SessionUpdatedEvent` carrying the FULL `prLinks` array seen so far on the file (NOT a singular `prLink` object); has `timestamp` so also a `LogEntry` `INF` at that ts. The ingester applies session extras via `json_patch` (whole-key overwrite), so emitting the complete array each time — combined with replay-from-0 on resume — makes the final array authoritative (last-wins on the whole array). A singular `prLink` key would be overwritten by each subsequent PR, losing all but the last. |
| `bridge-session` | UPDATE `sessions.extras_json.bridge` |
| `file-history-snapshot` | UPDATE `sessions.extras_json.fileHistory` (last non-empty wins) |
| Unknown `type` | `SourceError` (informational); the bad line is logged but not blocking |

### 5.5 Op Seq within turn

`OpStartedEvent.Seq` is 1-based monotonic within the turn. The ordering rule:

1. The LLM op for an `assistant` record gets `Seq = next available`.
2. Each `tool_use` block inside that assistant record gets `Seq = next available + 1, +2, ...` AFTER the LLM op (because the LLM produced them).
3. Each `thinking` block gets a NESTED op under the LLM op (`ParentOpSeq = LLM op's Seq`).
4. Tool ops are `Finalized` not when emitted but when the matching `user.tool_result` arrives; the adapter holds a small in-memory map `tool_use_id -> op state`.

### 5.6 Token and provider fields

- `Provider` is always `'anthropic'` for `kind='llm'` ops (Claude Code only calls Anthropic).
- `Model` is taken verbatim from `assistant.message.model` (e.g. `"claude-opus-4-7"`). The `pricing.go` catalog must include this id.
- `TokensIn` = `usage.input_tokens + cache_creation_input_tokens + cache_read_input_tokens` (effective input including cache). The adapter additionally records `extras_json.cacheCreation`, `extras_json.cacheRead`, `extras_json.uncachedInput` so the UI can decompose cost properly.
- `TokensOut` = `usage.output_tokens`.
- `CtxUsed` = `TokensIn + TokensOut` (closest available proxy for context window utilization on the LAST turn; per-turn input only loosely tracks the running context).
- `CtxMax` = looked up via `catalog_models.ctx_max` (1M for `claude-opus-4-7[1m]`, 200K otherwise per Anthropic docs at time of writing).
- `CostUSD` = computed from `pricing.go` per `(provider, model, cache tier)`; ai-viewer's pricing table tracks separate $/Mtok for input, output, cache-creation (ephemeral_1h, ephemeral_5m), cache-read.

## 6. Watch Strategy

### 6.1 What to watch

Recursive watch is the natural model, but `fsnotify` is **not recursive on Linux**. The adapter walks the tree at startup and `Add()`s every directory it cares about:

- `~/.claude/projects/` — to detect new project dirs (CREATE on subdir).
- Each `~/.claude/projects/<sanitized-cwd>/` — to detect new session files (`<sessionId>.jsonl`) and new session subdirs (`<sessionId>/`).
- Each `~/.claude/projects/<sanitized-cwd>/<sessionId>/subagents/` — to detect new subagent jsonls and meta sidecars.

Every path the adapter opens or watches — project dir, session file, subagent file, spill/meta sidecar — is resolved with `filepath.EvalSymlinks` and verified to stay inside the configured projects root before the read or watch (`security.md` §6 "No symlink traversal escape"). A path that resolves outside the root — a symlink planted to point at `/etc/passwd`, a session dir, or any other location — is refused with a `SourceError` and skipped; the adapter never reads or watches it. The root itself is resolved once at startup so a legitimately symlinked projects root (e.g. `~/.claude` → an external volume) still works: containment is judged against the resolved root.

Containment is **uniform across every read path**, not only Scan-time transcript discovery:

- Scan transcript discovery (the directory walk) — guarded.
- Subagent `.meta.json` collection and read (the `agentType` / `toolUseId` sidecar reads in both Scan and Tail) — guarded; a symlinked `.meta.json` resolving outside the root is refused and surfaces a `SourceError`.
- Tail transcript reads (a file marked dirty by an fsnotify WRITE, resolved from `root + relpath`) — guarded; a `*.jsonl` symlink created in a watched directory after Tail starts is refused before it is opened.
- Tail meta-hash checkpointing — guarded; the same containment gate applies before hashing a sidecar's content, and the hash reads the resolved path the guard returns (no TOCTOU).
- Orphan-root earliest-timestamp probe (the single-line read of an orphan parent's child sidechain that seeds the synthetic root's `Ts`, §10.1) — guarded; it opens the symlink-resolved path, not the discovery-time unresolved path.

A symlink planted after the watch is established is therefore refused on the read path, not merely at startup discovery.

**The read opens the symlink-resolved path as best-effort containment.** The containment guard resolves a path with `filepath.EvalSymlinks` and then the adapter `os.Open`/`os.ReadFile`s the **resolved** path the guard returned — never the original unresolved path. Opening the original after checking the resolved one would leave a wider time-of-check/time-of-use window: a symlink swapped between the check and the open would redirect the read outside the root even though the check passed; opening the already-resolved path closes that obvious window because the resolved path is fully symlink-evaluated. This applies UNIFORMLY to every file open in the adapter: transcript opens (Scan + Tail), subagent `.meta.json` reads (Scan + Tail), the Tail meta-hash checkpoint read, and the orphan-root earliest-timestamp probe. No code path opens an unresolved discovery-time path. This NARROWS but does not ELIMINATE a same-user check-then-open race: ai-viewer is a single-user, read-only tool reading files the same user owns, so a malicious same-user swap is outside its threat model — the resolved-path open is defense-in-depth containment against an accidental or stray symlink, not a guarantee against a determined same-user attacker who can race the resolve. Treat it as best-effort containment, not a hard TOCTOU-free property.

**Meta read/parse failures surface a `SourceError` (no silent failure).** A subagent `.meta.json` that is PRESENT but cannot be read (`os.ReadFile` error) or cannot be parsed (`json.Unmarshal` error) carries the `toolUseId → agentId` linkage and the subagent's `agentType`; silently dropping it would silently fail the parent-`Agent`-op→child link repair and lose the child's `AgentName`. The adapter therefore surfaces a `SourceError` (via the `OnError` callback → `sources.parse_errors` + a `log_entries` ERR row, visible in `/api/health` and the Sources panel) on a present meta file's read or parse failure. This holds on EVERY meta read path, including the Tail meta-hash checkpoint: a meta whose content cannot be read when the checkpoint hashes it surfaces a `SourceError` rather than being silently skipped (the silent skip would mask a broken sidecar whose rewrite should have driven the late-meta linkage repair). A genuinely-absent meta dir or file is NOT an error — the structural path-based linkage still works without the sidecar; only a present-but-broken sidecar is surfaced.

**Meta reads are size-capped.** A `.meta.json` sidecar is a tiny JSON object (`agentType`, `toolUseId`, `description`, a few scalars). The adapter caps every meta read at a fixed `metaReadMax` (mirroring the spirit of the transcript-line `scanBufferMax`): a sidecar whose size exceeds the cap is NOT read into memory — it is skipped with a `SourceError` (so a pathological or hostile oversized sidecar cannot force an unbounded allocation). The cap applies UNIFORMLY to every meta read: the Scan/Tail `agentType` + `toolUseId` read, the meta-hash checkpoint read, and the Tail late-meta `AgentName`-repair read. A present-but-oversized sidecar is surfaced (not silently dropped); a genuinely-absent sidecar is still not an error.

**The Tail watch walk and the meta-hash walk descend the symlink-RESOLVED root.** `filepath.WalkDir` does not descend INTO a symlinked walk-root, so a legitimately symlinked projects root (e.g. `~/.claude` → an external volume) would make the Tail watch tree and the meta-hash refresh silently miss every directory and file under it if they walked the unresolved configured root. Both walks therefore start from the resolved root (resolved once at startup, the same value used for per-path containment), so a symlinked projects root is fully watched and its sidecars are fully hashed. Per-path containment is still judged against the resolved root.

On `CREATE` of a new directory at depth 1 (a new project dir), the watcher `Add()`s the new dir.

On `CREATE` of a new directory at depth 2 (a new session subdir under a project), the watcher `Add()`s the `subagents/` subdir as soon as it appears, AND the `tool-results/` subdir.

### 6.2 Events the adapter cares about

- `CREATE` on `*.jsonl` → backfill from offset 0, then watch.
- `WRITE` (or Linux `MODIFY`) on a watched `*.jsonl` → read from last-known offset to EOF, parse line by line, advance offset.
- `WRITE` on `*.meta.json` (subagent sidecar) → re-read the small (size-capped) JSON file. On a content change the adapter repairs the child session's `AgentName` by emitting a catalog-safe `SessionUpdatedEvent{AgentName=agentType}` (NOT a transcript re-read; §8.1). The op→child link is repaired separately by the resolver's `toolUseId` match (§8.1) and needs no meta re-read.
- `RENAME` / `MOVED_FROM` / `MOVED_TO` — not used by claude-code in normal operation (it appends in place). The adapter logs these but does not act on them in Phase 2.
- `DELETE` — operator deleted a project directory or jsonl. The adapter logs and stops watching the missing file. Existing rows in SQLite remain (read-only viewer; data is never deleted).

### 6.3 Tail semantics

Tail resumes each file from the **persisted cursor offset** where Scan left off —
NOT from the file's current EOF. The ingester drives one adapter instance through
`Scan` then `Tail` (`runAdapter`); Scan records its final per-file offsets on the
adapter, and Tail continues from those. This closes the data-loss window where
records appended to a file *between* Scan finishing and Tail starting would be
skipped if Tail snapshotted current EOF (any re-emission of an already-seen line is
absorbed by the ingester's SQL-layer idempotent upserts). A cold Tail with no
preceding Scan (no recorded offsets) falls back to current file sizes so it does
not replay full history.

Because fsnotify does not fire for bytes written before the watch was
established, Tail performs an **initial catch-up read** once at startup, after
the watches are added: it reads every currently-discovered transcript from its
cursor offset to current EOF (reusing the steady-state flush path, so the
offset advance, partial-line hold-back, and Agent-op deferral rebuild are
identical). A file already fully consumed by Scan re-reads zero new bytes
(offset == size) and emits nothing — including emitting no Agent-op finalize,
because the terminal assistant-text record is below the resume offset and so is
not newly read (§8.1). A file that grew during the window emits exactly the new
records. After the catch-up, the fsnotify loop drives all further reads.

Stream-parse line-by-line from the cursor offset. A line without a trailing `\n` is an in-flight write; the adapter parks the partial bytes and resumes on the next event. A line that parses but has unknown `type` produces a `SourceError`. A line that fails to parse as JSON: same. The adapter does not skip bytes blindly; it always advances `offset` past completed lines only.

**Oversized line (exceeds the scan buffer).** A single line longer than the scan buffer bound (`scanBufferMax`, 8 MB) cannot be buffered whole. The adapter surfaces exactly one `SourceError` for that line, discards bytes up to AND including the next `\n`, and **continues reading subsequent records** — it does NOT jump to EOF. Skipping to EOF would silently discard every later valid record in the file (a 100 MB transcript with one pathological line would lose everything after it). Only the one oversized line is dropped; the offset advances past its terminating newline so the cursor stays consistent and the records after it are ingested normally.

### 6.4 Throughput considerations

A session jsonl can grow at multi-MB/sec during an active agent run. The adapter uses a small read-ahead buffer (e.g. 64 KB) per file and flushes events in batches of a few hundred per SQLite transaction (see `data-model.md` ingester strategy).

## 7. Cursor

The cursor is the adapter's resume contract. Shape (JSON, stored in `sources.cursor`):

```json
{
  "version": 1,
  "files": {
    "<sanitized-cwd>/<sessionId>.jsonl": {
      "offset": <byte_offset>,
      "size":   <file_size_at_offset>,
      "lastTs": <last_record_ts_microseconds>
    },
    "<sanitized-cwd>/<sessionId>/subagents/agent-<agentId>.jsonl": { ... }
  },
  "metaSeen": {
    "<sanitized-cwd>/<sessionId>/subagents/agent-<agentId>.meta.json": "<sha256-of-content>"
  },
  "parked": {
    "<parent sessionId>:agent:<agentId>": <completion_ts_microseconds>
  },
  "finalized": [
    "<parent sessionId>:agent:<agentId>"
  ]
}
```

Keys are paths **relative to the configured root** (`~/.claude/projects/`). The adapter never stores absolute paths in the cursor — moving the projects dir or running under a different `CLAUDE_CONFIG_DIR` shifts the root, not every cursor key.

`parked` and `finalized` are the durable Agent-op-finalization state described in §8.1: `parked` carries the completion timestamps of subagent children that completed before their parent `Agent` op was known (so a restart can still finalize the parent when the op appears), and `finalized` carries the child ids already finalized (so a restart does not re-emit a finalize across the Scan→Tail / process boundary). Both are JSON `omitempty` so cursors that predate them still parse, and both are observability/durability state only — they do NOT participate in `After()` ordering, which is keyed solely on per-file byte offsets.

On startup:

1. Walk the projects dir to discover all `*.jsonl` files (root + subagents).
2. For each file in the cursor: seek to `offset`, validate `size` matches the file's current size (if smaller, the file was truncated — flag `SourceError` and re-scan from 0; if larger, resume tail).
3. For each file NOT in the cursor: it's new; backfill from 0.
4. For each `.meta.json` whose content hash differs from `metaSeen`, re-read and update.

`SourceProgress` is emitted periodically (every N lines or T milliseconds) so the cursor is durable even mid-stream.

## 8. Sub-Agent Linkage

(Summary; the detail is in §4.)

The link is **structural and bidirectional**:

- **Filesystem**: subagent jsonl lives at `<parent-jsonl-dir>/<parent-sessionId>/subagents/agent-<agentId>.jsonl`. The directory layout itself encodes "this subagent belongs to that parent session".
- **Sidecar**: `agent-<agentId>.meta.json.toolUseId` equals the parent's `assistant.tool_use.id` block that spawned it.
- **In-record**: every subagent record carries `isSidechain:true` and `agentId:"<agentId>"`. The `sessionId` matches the parent's (this is a structural feature, not a bug — Claude Code treats the subagent as a "sidechain" of the same logical session).

Canonical-model expression:

- Parent session: `Kind='root'`, `NativeID = <parent sessionId>`.
- Subagent session: `Kind='sub_agent'`, `NativeID = <parent sessionId>:agent:<agentId>`, `ParentNativeID = <parent sessionId>`.
- The parent's `Agent` tool_use becomes an `op` with `Kind='session'`, `ChildSessionNativeID = <parent sessionId>:agent:<agentId>`. The ingester resolves this to a foreign key.

Resume case: an interrupted subagent (Claude Code can `/resume` an agent) appends to the same `agent-<agentId>.jsonl`. The adapter does not start a new canonical session; it continues the existing one (which is achieved by stable `NativeID`).

### 8.1 Op→child linkage and Agent-op finalization

Two ordering hazards arise because the parent `Agent` op and the child session
land independently:

1. **Op→child link survives child-after-parent ordering, meta-independently and
   re-emit-free (ingester).** The parent's `OpStartedEvent(Kind='session')` and the
   child `sessions` row land independently and in either order, and the link must
   never depend on re-reading a transcript (a re-read re-emits catalog-counted
   events and would double-count the `catalog_*` rollups, which accumulate on
   conflict — §catalog idempotency). The adapter therefore stashes the join key on
   BOTH ends from data each side already has, and the resolver links them at the DB
   layer:

   - **Parent op stashes the `toolUseId` unconditionally.** The parent `Agent`
     `tool_use` block carries its own `id` (the `assistant.tool_use.id`); the
     adapter stamps it into `OpStartedEvent.Extras.aiViewer.toolUseId` for EVERY
     `Agent` op — regardless of whether the sidecar `.meta.json` has been read yet.
     This is the canonical parent→child join key (§4.2: the child's
     `.meta.json.toolUseId` equals this `id`). When the meta IS already known at
     map time the op additionally carries `ChildSessionNativeID` (the direct link,
     resolved by the existing `childNativeId` pass below); the `toolUseId` stash is
     the meta-independent fallback that works even when the meta is not yet read.

   - **Child session carries its own `toolUseId`.** When the adapter synthesizes a
     sub-agent `SessionStartedEvent`, it includes the child's `toolUseId` (from its
     `.meta.json.toolUseId`, available whenever the child transcript is read with
     its sidecar) in `SessionStartedEvent.Extras.aiViewer.toolUseId`. The writer
     merges this into `sessions.extras_json.aiViewer` (alongside `parentNativeId` /
     `rootNativeId`), so the durable child row carries the join key.

   - **The resolver links by `toolUseId`, additively.** A dedicated resolver pass
     (`linkOpChildrenByToolUse`) re-links `ops.child_session_id` for any op whose
     `child_session_id` is still NULL and whose
     `extras_json.aiViewer.toolUseId` is set, to the session in the SAME source
     whose `extras_json.aiViewer.toolUseId` matches (joined through the op's parent
     session for `source_id`, since ops carry no `source_id` column). It emits a
     `session_changed` notify for the affected **parent** session so an open UI
     refetches. This pass is purely additive: an adapter that does not stash
     `aiViewer.toolUseId` (aiagent v2/v3) matches zero rows and is unaffected.

   The earlier `ChildSessionNativeID` → `ops.extras_json.aiViewer.childNativeId`
   stash + `linkOpChildren` resolver pass REMAINS UNCHANGED (aiagent v2/v3 rely on
   it; claude-code also benefits from it when the meta was present at map time). The
   `toolUseId` pass is the second, additive bridge that closes the late-meta gap
   (the parent `Agent` op tailed before its `.meta.json`) WITHOUT any transcript
   re-read. The writer stashes (and never drops on a later upsert) the `aiViewer`
   sub-object so a parent re-emit lacking the stash cannot erase a previously
   recorded `toolUseId`/`childNativeId` (§op-stash merge below).

2. **Agent op finalize is inherently child-side, via the terminal
   assistant-text completion marker (adapter).** The parent transcript has NO
   `tool_result` block for the `Agent` tool (§4.4, verified against a real
   transcript: the Agent's `tool_use` id appears once, not the typical twice),
   so there is no parent-side completion record. The subagent's result is
   implicit — the **last record** of `agent-<agentId>.jsonl` is an `assistant`
   message whose `content[0].type == "text"` (§4.4, §485; verified on real
   workstation transcripts: completed sidechains end with `{assistant, text}`,
   while a child whose last record is a `user`/`tool` record was interrupted and
   is NOT complete). The adapter therefore finalizes the parent's
   `OpStartedEvent(Kind='session')` from the **child sidechain's end state**,
   never from a parent record, and the link is the op's `ChildSessionNativeID`
   (equal to the child file's synthetic `NativeID`).

   The completion rule is exact:

   - A child is **complete** iff it was streamed fully to EOF (no parked partial
     trailing line) AND its terminal record is an assistant-text record
     (`childComplete := mapper.fullyRead && mapper.lastRecordAssistantText`). The
     mapper tracks `lastRecordAssistantText`: set true (with the record's
     timestamp captured in `lastAssistantTextTsUs`) when a record is an assistant
     message with `content[0].type == "text"`, and set false on ANY other record
     type. A child terminated by a `user`/`tool_use` record therefore stays
     `running` — no timing heuristic, no quiescence window. This holds identically
     in a static Scan and in live Tail.

   - **The flag reflects the PHYSICAL last record, not the last MAPPED record.**
     The completion marker is the genuine last line of the file, regardless of
     whether the adapter mapped, skipped, failed to parse, or could not even buffer
     it. The line reader therefore sets `lastRecordAssistantText` (and
     `lastRecordEmitted`) for EVERY physical line it consumes — on EVERY path that
     advances the offset past a line: the normal mapped path, the parse-error skip,
     the known-no-op skip, AND the oversized-line skip. A parse-error line, a
     skipped known-no-op line (e.g. a trailing `summary` / `task-summary` record),
     a malformed JSON line, and a line that exceeds `scanBufferMax` all set
     `lastRecordAssistantText = false`. So a child whose physical last record is
     `[assistant{text}, <skipped no-op>]`, `[assistant{text}, <malformed line>]`,
     or `[assistant{text}, <oversized line>]` is NOT complete — its parent `Agent`
     op stays `running` — because the assistant-text record is not the physical
     last line. Without this, a trailing no-op / malformed / oversized line would
     leave the flag stale-true from the preceding assistant-text record and wrongly
     finalize the parent. The ONLY non-line-consuming loop exit is the clean
     end-of-file return, which leaves the flag set by the genuine last line.

   - **Emit-gated by `emitFrom` (replay emits nothing).** A child is marked
     `completed` only when its terminal assistant-text record was **newly read**
     this pass — i.e. that record's `lineStart >= emitFrom`. A catch-up / resume
     read (`emitFrom == size`) re-reads the terminal record below the resume
     offset to rebuild counters, so it is NOT newly read and the child is NOT
     re-marked, so no second finalize is emitted (a replay over an
     already-finalized child emits nothing). A child id already finalized in the
     loop lifetime is additionally skipped (a `finalized` set that is READ before
     emitting, not merely written).

   - **Durable across the Scan→Tail boundary.** A parent fully read during Scan
     leaves no unread bytes, so a naive Tail catch-up that early-returns on
     `offset >= size` would rebuild an empty Agent-op set and never learn the
     parent existed — the child completing later in Tail would then never finalize
     the parent. To prevent this, whenever a transcript is already at EOF
     (`offset >= size`) the adapter still **replays the chain from offset 0 with
     the emit-gate set to the file size** (emit nothing, identical to the
     counter-rebuild used for resume), reconstructing the per-file Agent-op map
     without re-emitting any event. The parent's Agent op is thus visible to
     Tail's loop-lifetime deferral when its child completes in a later flush.

   - **Event-driven pairing (no ticks).** Across the Tail loop lifetime the
     deferral carries two sets: `completed` (child id → completion ts, for
     children observed complete whose parent op is not yet finalized) and
     `finalized` (child ids already finalized). The parent Agent op ref is
     discovered via the existing `agentOps` deferral (`def.pending[childID] =
     parentRef`, rebuilt on every read including the emit-suppressed replay so it
     survives the Scan→Tail boundary). After each flush's reads, for every child
     id in `completed` not in `finalized`: if its parent op ref is known, emit the
     finalize, move the child id into `finalized`, and drop it from `completed`; if
     the parent op is not yet known, leave the child parked in `completed` (it
     finalizes in a later flush once the parent op is observed). There is no
     `cycle` counter, no quiescence window, and no tick-driven sweep.

   - **A parked completion is RETRACTED when a re-read child is no longer
     complete.** Folding one transcript's mapper state into the deferral is
     bidirectional, not add-only: when a subagent transcript is read and it IS
     currently complete (fully read, terminal assistant-text, newly read this
     pass) its child id is added to `completed`; when a subagent transcript is
     read and it is NOT currently complete (`!(fullyRead &&
     lastRecordAssistantText)` — e.g. it grew a trailing `tool_use` / `user`
     record after a prior pass had observed it complete, or a parked-but-not-yet-
     finalized child re-read after appending), its child id is **deleted** from
     `completed`. Without retraction, a child that completed and parked (because
     its parent op was not yet known) but then grew a non-text terminal record
     BEFORE the parent op appeared would keep its stale parked completion and
     wrongly finalize the parent once the op landed. The `lastRecordEmitted` gate
     stays on the ADD branch only: a pure replay of an already-complete child
     (fully read, terminal assistant-text, but read below the resume offset so
     `lastRecordEmitted == false`) is neither re-added (the emit gate) nor
     retracted (it IS still complete) — its existing park / `finalized` state is
     left untouched, so the no-double-finalize property holds.

   - **Parked completions survive a daemon restart (cursor-durable).** The
     `completed` set is also persisted in the cursor (a `parked` map of child
     native id → completion-ts-micros, JSON `omitempty` so older cursors without
     it still parse). It is checkpointed on every `SourceProgress` and restored
     into the loop's `completed` set on Tail startup, and an entry is removed from
     the cursor when its finalize is emitted. Without this, a child that completes
     BEFORE its parent `Agent` op is known would lose its parked completion on a
     restart: if the daemon restarts before the parent op appears, the parent later
     appears but is never finalized (the in-memory park is gone). Persisting the
     park closes that gap. This is isolated to the cursor and the park set; it does
     NOT change the finalize-emit gating (the `finalized` set and the
     `lastRecordEmitted` gate are unchanged), so a replay over an already-finalized
     child still emits nothing.

   - **The `finalized` set is also cursor-durable (no re-finalize after a
     restart).** The `finalized` set is persisted in the cursor too (a `finalized`
     list of child native ids, JSON `omitempty` so older cursors still parse),
     checkpointed alongside `parked` on every `SourceProgress` and restored on Tail
     startup. The loop-lifetime `finalized` set guards against a re-finalize WITHIN
     one process lifetime; persisting it carries that guard across the lifetime
     boundary — a child finalized during Scan and then re-observed by a Tail in the
     same or a restarted process is not finalized a second time. (The late-meta
     repair no longer re-reads any child transcript — §8.1 item 3 — so it can no
     longer re-mark a finalized child; the cursor-durable `finalized` set now serves
     only the Scan→Tail / restart boundary.) Like `parked`, this is
     observability/durability state only — it does not participate in cursor
     `After()` ordering — and it does NOT change the normal finalize behavior (a
     child finalized for the first time still emits exactly once).

   The finalize's `EndTs` is the child's terminal assistant-text record's
   timestamp (its implicit completion time).

3. **Late `.meta.json` is repaired WITHOUT any transcript re-read (catalog-safe).**
   The parent transcript and the sidecar `.meta.json` land independently, so Tail
   may read the parent's `Agent` block in flush N before the `.meta.json` arrives
   in flush N+1, and a child sidechain read before its `.meta.json` emitted its
   `SessionStarted` with an empty `AgentName`. An earlier design re-read the parent
   AND child transcripts from offset 0 WITH EMISSION on a meta change to repair
   both. That re-emitted `SessionStarted` / `OpStarted` / `OpFinalized` — events the
   `catalog_*` rollups COUNT on conflict (`session_count + 1`, `call_count + 1`,
   `total_tokens_* += …`, `total_cost_usd += …`, `total_duration_us += …`) — so a
   single `.meta.json` rewrite DOUBLE-COUNTED the catalog. That from-0 re-emit is
   REMOVED. The two repairs it served are now done without re-emitting any
   catalog-counted event:

   - **Op→child link** no longer needs the parent re-read at all: it is handled by
     the meta-independent `toolUseId` stash + `linkOpChildrenByToolUse` resolver
     pass (item 1 above). The parent op stamped its `toolUseId` at map time
     regardless of meta arrival; once the child session lands carrying the same
     `toolUseId`, the resolver links them at the DB layer. No transcript is re-read.

   - **Child `AgentName`** is repaired by emitting a **`SessionUpdatedEvent`**
     carrying `{NativeID: <child native id>, AgentName: <agentType>}` when a Tail
     flush observes the child's `.meta.json` content change. `applySessionUpdated`
     makes NO catalog call (only `applySessionStarted` touches `catalog_agents` /
     `catalog_cwds`), so this repair is catalog-safe by construction, and the
     ingester upserts the name via `COALESCE(NULLIF(?, ''), agent_name)` so a
     previously-empty `AgentName` is filled while a non-empty one is preserved. The
     adapter reads the changed meta's `agentType` directly (the same bounded read it
     already does for the `toolUseId → agentId` map) and emits one
     `SessionUpdatedEvent` per changed subagent meta whose `agentType` is non-empty;
     no child transcript is re-read.

   Net: a `.meta.json` rewrite emits at most a catalog-safe `SessionUpdatedEvent`
   (AgentName) and never re-emits a `SessionStarted` / `OpStarted` / `OpFinalized`,
   so no `catalog_*` aggregate changes on a meta rewrite. The from-0 re-read
   machinery (`forceFromZero`, `metaParentRels`, `metaChildRels`) is gone.

## 9. Compaction Handling

Compaction is Claude Code's mechanism for summarizing a long conversation to stay within the model's context window. It is signalled by a `system.subtype=="compact_boundary"` record (verified shape, §3.3).

### 9.1 Pre/post chain

- Records BEFORE the boundary belong to the pre-compaction era; their `parentUuid` chain is intact backward to the session start.
- The compact boundary record itself has `logicalParentUuid` pointing into the pre-era and `parentUuid: null` (verified observation: `parentUuid:null, logicalParentUuid:"<pre-uuid>"`).
- The first user record AFTER the boundary is typically `isCompactSummary:true` with `message.content` = a long string starting `"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion..."`. Its `parentUuid` points to the boundary record.
- Subsequent records form a fresh chain rooted at the summary user message.

### 9.2 Canonical-model treatment

The decision: **keep both pre- and post-compaction history in the canonical model**, separated by a synthetic `OpKind='compaction'` op (the canonical model's first-class compaction kind, not an `internal` op).

1. The `compact_boundary` record emits one synthetic `OpStartedEvent`/`OpFinalizedEvent` pair with `OpKind='compaction'` (the canonical model defines `OpCompaction` as a first-class op kind), `Ts=boundary.timestamp`, `EndTs = boundary.timestamp + compactMetadata.durationMs * 1000`. `BytesIn = compactMetadata.preTokens`, `BytesOut = compactMetadata.postTokens`, `Extras = compactMetadata`.
2. The `isCompactSummary:true` user message after the boundary does NOT start a new turn. It is emitted as a `LogEntry` `INF` and as a `PayloadRef` for the summary text (so the UI can show it in a "compaction" lane). The summary `PayloadRef` is scoped to the preceding compaction op so it references an op that exists (`payload_refs.op_id` is `NOT NULL`). The drop guard is `OpSeq == 0` only — NOT `TurnSeq == 0 || OpSeq == 0`. A compaction can occur before any operator prompt (turn 0), in which case the compaction op lives at `(TurnSeq=0, OpSeq>=1)` and its summary payload is correctly emitted and op-scoped; keying the guard on `TurnSeq == 0` would wrongly drop that legitimate turn-0 summary.
3. The next regular user-prompt record opens a new turn as usual.
4. The pre-compaction turn count is preserved; the post-compaction continues numbering monotonically.

This means the UI can display "Turn N: compacted here" without losing history and can render the compaction as a single visible event with its own latency, tokens-in/out, and metadata.

### 9.3 Manual vs auto compaction

Observed `compactMetadata.trigger` value: `"manual"` (operator ran `/compact`). The source declares `auto` (automatic context-window-pressure compaction) and `clear` (cleared, not summarized) but they were not observed in the operator's data. The adapter must tolerate any string value and surface it in `extras_json`.

## 10. Edge Cases

### 10.1 Older session layouts

Sessions older than ~Mar 2026 may live as a `<sessionId>/` directory containing ONLY `subagents/` — no parent `.jsonl` at root. Verified observation: `~/.claude/projects/-home-operator-src-alerts/<sessionId>/subagents/agent-*.jsonl` exists; no `<sessionId>.jsonl` next to it. Even older (Feb 2026) sessions have just `subagents/` with two orphan files and nothing else.

Adapter behavior: emit a `SessionStartedEvent` with `Kind='root'`, `AgentName=''`, `Model=''`, `Ts = earliest record across all subagent files`, and treat each subagent file as a `sub_agent` child. The parent session has zero turns/ops directly — the entire activity lives in the children. Mark `sessions.extras_json.orphanRoot = true` so the UI can hint at this.

### 10.2 Partial writes

A line that does not end in `\n` is in-flight (the producer appends with one `appendFileSync(line)` call, but the OS may have flushed only part). The adapter parks the partial bytes and resumes on the next `WRITE` event. Verified by inspection of `appendEntryToFile` (jarmuine `sessionStorage.ts:2572-2584`): the writer is sync but `appendFileSync` is not atomic on `O_APPEND` across page-cache boundaries.

### 10.3 Tmp files

No `.tmp-*` files have been observed under `~/.claude/projects/`. Claude Code appends in place rather than atomic-rename. The adapter ignores any file not matching `*.jsonl` / `*.meta.json` / `tool-results/*`.

### 10.4 Long path hash collision

Paths whose sanitized form is >200 chars get a hash suffix. The CLI hash is `Bun.hash`; the SDK hash is `djb2`. For the same long cwd, CLI and SDK produce DIFFERENT directory names. If the operator runs both CLI and SDK in the same long-path project, there will be two distinct project dirs for one cwd. The adapter treats them as distinct sources — no merging — and surfaces both in `/health`.

### 10.5 cwd changes mid-session

The launch cwd is the directory name; subsequent records' `cwd` may differ. The adapter stores the directory's encoded form as the source's index key but records the actual cwd per session into `sessions.extras_json.cwd` from the FIRST record's `cwd` (which equals the launch cwd).

### 10.6 Same sessionId across formats

Claude Code's `sessionId` is a UUIDv4; collision with another adapter's session id is improbable but possible by adversarial input. The store uses `(source_id, native_id)` as the unique key, so distinct adapters cannot collide.

### 10.7 Synthetic assistant messages

Already covered (§3.2): treat as `LogEntry` `INF`; emit no LLM op.

### 10.8 Resume / `--continue`

When the operator runs `claude --resume <sessionId>` the producer appends new records to the existing `<sessionId>.jsonl`. The adapter resumes from its cursor offset transparently. No new session is created.

### 10.9 Worktrees and bridge-session

`worktree-state` and `bridge-session` records redirect the session's logical home to a different filesystem path / remote session. The adapter does NOT follow worktree symlinks; it stays within `~/.claude/projects/`. Bridge sessions surface only as metadata; the remote `cse_*` session is not fetched.

### 10.10 MCP tool naming and namespacing

MCP tools appear as `mcp__<server>__<tool>` (double-underscore separator) in `assistant.tool_use.name`. The adapter splits on `__` to derive:
- `Op.Name = <tool>` (last segment)
- `Op.ToolNamespace = mcp:<server>` (middle segment, prefixed `mcp:` so a built-in tool named `read` doesn't collide with an MCP tool `mcp__foo__read`).

Built-in tools (no `mcp__` prefix) get `ToolNamespace = "builtin"`. Sub-agent tools (`Agent`, `Task`*) are also `builtin`.

### 10.11 Very large transcripts

Some session jsonl files in observed data exceed 100 MB; the producer's own reader bails at 50 MB. The ai-viewer adapter has no such limit because it streams — but the SQLite writer should be batched to avoid one giant transaction. See `ingester.md` (Phase 1 spec) for batch policy.

### 10.12 Encoding

All record fields are UTF-8 JSON. `sanitizePath` operates on the canonicalized (NFC-normalized) cwd; the adapter does not need to renormalize when reading.

## 11. Canonical Model Gaps

The canonical events model is span-shaped and assumes adapters can produce explicit turn/op boundaries. Claude Code's reality is messier; the following gaps require either canonical-model extension OR adapter-side synthesis:

1. **No native turn or op boundaries.** Inferred from message-chain heuristics (§5.3, §5.5). Risk: an exotic flow (mid-turn user interrupt with a `text` block instead of a tool_result; multi-prompt operator runs without explicit completion) could break inference. Mitigation: extensive fixtures from real data; if inference is ambiguous, the adapter emits `SourceError` `INF` and continues with best-effort.
2. **No native cost.** Compute from `pricing.go`. Risk: pricing model changes silently. Mitigation: pricing is a versioned spec; tested against known totals where Anthropic's web console can be cross-checked.
3. **Compaction is a first-class op.** Modeled as a synthetic `OpKind='compaction'` op (§9). The canonical model already defines `OpCompaction`, so no schema change is needed — but the UI must learn to render `compact_boundary` distinctively (a different lane / icon).
4. **Subagents share `sessionId` with parent.** Resolved via synthetic NativeID (`<sessionId>:agent:<agentId>`). The canonical model needs no extension; the adapter owns the NativeID synthesis.
5. **Last-wins metadata snapshots have no Ts.** Records like `last-prompt`, `permission-mode`, `custom-title`, `ai-title`, `bridge-session`, `file-history-snapshot` have no `timestamp`. They are emitted as **session updates** (`SessionUpdatedEvent` for AgentName/Model; otherwise as in-memory state flushed to `sessions.extras_json` on the next `SourceProgress`). They do NOT appear on the timeline.
6. **Synthetic `<synthetic>` model.** Handled as `LogEntry`, not as a model op (§3.2). No schema change.
7. **`thinking` content blocks.** Modeled as nested ops under the LLM op with `Kind='reasoning'`. Already supported by the canonical model.
8. **Server-tool requests** (`usage.server_tool_use.web_search_requests`, `web_fetch_requests`). These are Anthropic-managed server-side tools — they have no `tool_use` block in the record, only a counter in usage. The adapter records them in `extras_json.serverToolUse` on the LLM op; no separate tool op is created.
9. **`isMeta` and `isCompactSummary` synthetic users.** These are NOT operator-initiated turn starts. Handled by the turn-boundary rule in §5.3.
10. **`isSidechain` and `agentId` are not part of the canonical envelope.** They live in adapter-internal state used to route a record to the right canonical session. No schema change needed.
11. **No explicit session-end record.** The canonical model expects a `SessionFinalizedEvent`. Claude Code does not emit one — a session is "done" when the file stops being written to. The adapter never emits `SessionFinalizedEvent` for claude-code; sessions remain `status='running'` until an explicit policy (e.g. "no writes for 24h") closes them. This is a deliberate decision: the operator can resume any session at any time; there is no "completed" state. The UI should render `status='running'` as "active or resumable" rather than "still running".
12. **Operator-pasted images.** 3 of ~40,000 user content blocks were images. Currently stored only as `PayloadRefEvent` (base64 data inline in the jsonl); the canonical model has no first-class image affordance. Phase 2 acceptable. The UI shows them as image-thumbnail attachments.

## 12. References

- `jarmuine/claude-code` (frozen mirror at `/opt/baddisk/monitoring/repos/ai/jarmuine__claude-code/`):
  - `src/utils/sessionStoragePortable.ts:293` — `MAX_SANITIZED_LENGTH = 200`.
  - `src/utils/sessionStoragePortable.ts:311-319` — `sanitizePath`.
  - `src/utils/sessionStoragePortable.ts:325-331` — `getProjectsDir`, `getProjectDir`.
  - `src/utils/sessionStoragePortable.ts:339-344` — `canonicalizePath`.
  - `src/utils/sessionStoragePortable.ts:354-` — `findProjectDir` (hash-suffix fallback).
  - `src/utils/sessionStorage.ts:218-224` — `getTranscriptPathForSession`.
  - `src/utils/sessionStorage.ts:229` — `MAX_TRANSCRIPT_READ_BYTES = 50 MB`.
  - `src/utils/sessionStorage.ts:247-258` — `getAgentTranscriptPath`.
  - `src/utils/sessionStorage.ts:260-262` — `getAgentMetadataPath`.
  - `src/utils/sessionStorage.ts:264-272` — `AgentMetadata`.
  - `src/utils/sessionStorage.ts:283-303` — `writeAgentMetadata`, `readAgentMetadata`.
  - `src/utils/sessionStorage.ts:305-318` — `RemoteAgentMetadata`.
  - `src/utils/sessionStorage.ts:766-839` — `reAppendSessionMetadata` (last-wins re-append loop).
  - `src/utils/sessionStorage.ts:2572-2584` — `appendEntryToFile`.
  - `src/types/logs.ts:8-17` — `SerializedMessage`.
  - `src/types/logs.ts:55-59` — `SummaryMessage`.
  - `src/types/logs.ts:61-104` — `CustomTitleMessage`, `AiTitleMessage`, `LastPromptMessage`, `TaskSummaryMessage`, `TagMessage`.
  - `src/types/logs.ts:106-122` — `AgentNameMessage`, `AgentColorMessage`, `AgentSettingMessage`.
  - `src/types/logs.ts:128-141` — `PRLinkMessage`, `ModeEntry`.
  - `src/types/logs.ts:149-171` — `PersistedWorktreeSession`, `WorktreeStateEntry`.
  - `src/types/logs.ts:181-219` — `ContentReplacementEntry`, `FileHistorySnapshotMessage`, `AttributionSnapshotMessage`.
  - `src/types/logs.ts:221-231` — `TranscriptMessage`.
  - `src/types/logs.ts:233-237` — `SpeculationAcceptMessage`.
  - `src/types/logs.ts:255-295` — `ContextCollapseCommitEntry`, `ContextCollapseSnapshotEntry`.
  - `src/types/logs.ts:297-317` — `Entry` union.

- Operator's workstation (observed evidence, 2026-05-26):
  - `~/.claude/projects/` — 70 project directories, 3,614 root-level `.jsonl` files, 411 session subdirs containing subagents.
  - Type-distribution and field-presence statistics in §3 derived from `jq` aggregation across all of the above.

- ai-viewer specs (cross-references):
  - `.agents/sow/specs/canonical-events.md` — canonical Event types this adapter emits.
  - `.agents/sow/specs/data-model.md` — SQLite schema.
  - `.agents/sow/specs/adapter-aiagent-v3.md` — structural template followed here.
