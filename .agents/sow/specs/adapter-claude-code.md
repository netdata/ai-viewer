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
| `/home/costa` | `-home-costa` |
| `/home/costa/.agents/skills/manjaro-updates` | `-home-costa--agents-skills-manjaro-updates` |
| `/home/costa/src/ai-agent.git` | `-home-costa-src-ai-agent-git` |
| `/home/costa/src/llama.cpp.git` | `-home-costa-src-llama-cpp-git` |
| `/home/costa/src/dashboard/netdata-cloud-frontend.git` | `-home-costa-src-dashboard-netdata-cloud-frontend-git` |
| `/tmp/nefeli` | `-tmp-nefeli` |
| `/opt/baddisk/monitoring` | `-opt-baddisk-monitoring` |

Note the double-hyphen `--agents` arising from the leading `.` of `.agents`: every original `.` and `/` becomes a hyphen, with no collapsing.

#### Lossiness implications

The encoded name **does not uniquely round-trip to a cwd**. For example, `/home/costa/ai-agent.git` and `/home/costa/ai-agent/git` and `/home/costa/ai_agent_git` all sanitize to `-home-costa-ai-agent-git`. The viewer **must read the authoritative cwd from inside the jsonl records** (see §3) and treat the directory name as a presentation/index aid only.

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

- `file` — operator attached a file: `{type, filename, content:{type:"text", file:{filePath, content}}}`.
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
- `compact_file_reference` — reference to a spilled large tool-result stored under `<sessionDir>/tool-results/<id>.txt`. The viewer can resolve this to a file:// URI for payload-on-demand.
- `nested_memory` — memory directive injected from a nested `CLAUDE.md`.
- `command_permissions` — permission-prompt history.
- `ultrathink_effort` — extended-thinking effort indicator.

Most attachments are content the harness injects into the model's context, NOT operator-visible UI events. The adapter emits them as `LogEntry` rows with `severity=DBG` (so they don't dominate the timeline) and optionally as `PayloadRefEvent` when there's a backing file (`compact_file_reference`, `file` with content).

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

No timestamp. Last-wins. `custom-title` is user-set and wins over `ai-title` for display (`logs.ts:67-74`). The adapter promotes the surviving title to `sessions.extras_json.title` (preferring custom over AI).

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

Has `timestamp` but no `uuid` or `parentUuid`. Records the session-to-PR linkage (when `gh pr create` ran). The adapter stores into `sessions.extras_json.prLinks[]` (a session may produce multiple PRs).

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

No top-level `timestamp` (the inner `snapshot.timestamp` is the write time). Records the producer's tracked-file backup state for Edit/Write undo. The adapter emits a single `LogEntry` `DBG` with the snapshot summary and stores per-file backups under `sessions.extras_json.fileHistory[]` only if non-empty.

### 3.12 Records observed only in source, not in real data

- `summary` (`SummaryMessage`) — possibly produced by `/compact` in older versions.
- `task-summary` — produced by `claude ps` instrumentation; off by default in observed installs.
- `tag`, `agent-name`, `agent-color`, `agent-setting`, `mode`, `worktree-state`, `content-replacement`, `attribution-snapshot`, `speculation-accept`, `marble-origami-commit`, `marble-origami-snapshot`.

Defensive policy: any unknown `type` value triggers a `SourceError` event with severity `INF` (informational; not blocking) plus a per-file counter so a future upgrade is visible in `/health`.

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
4. Emit a separate canonical Session for the subagent: `SessionStartedEvent` with `Kind='sub_agent'`, `ParentNativeID=<parent sessionId>+<toolUseId>` synthetic native id (see §5.2), `AgentName=<agentType>`.

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
| `SourceSeq` | adapter's monotonic per-source counter (separate from any uuid) |
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
| `user` with array content (`tool_result` blocks) | one `OpFinalizedEvent` per `tool_result` block, matched by `tool_use_id`; plus `PayloadRefEvent` for the `toolUseResult` body or for `compact_file_reference` spillovers |
| `user` with `isMeta==true` (`<local-command-caveat>` etc.) | `LogEntry` `DBG`; no turn/op events |
| `user` with `isCompactSummary==true` | `LogEntry` `INF` carrying the post-compaction summary, plus updates the previous turn's `extras_json.compactionSummary` |
| `assistant` (model != `<synthetic>`) | `OpStartedEvent(Kind='llm', Model, Provider='anthropic', Name=Model)` covering the LLM call; for each `tool_use` block in `content[]`, an additional `OpStartedEvent(Kind='tool', Name=name, ToolNamespace=mcp_server or '')`; `OpFinalizedEvent` for the LLM op with tokens from `message.usage` |
| `assistant` (model == `<synthetic>`) | `LogEntry` `INF`; no LLM op emitted |
| `assistant.content[].type=="thinking"` | nested `OpStartedEvent`/`OpFinalizedEvent` with `Kind='reasoning'`, `ParentOpSeq=<the LLM op>`, `BytesOut=len(thinking)` |
| `assistant.tool_use` with `name=="Agent"` | `OpStartedEvent(Kind='session', Name=description, ChildSessionNativeID=<parent sessionId>:agent:<agentId>)`; deferred `OpFinalizedEvent` resolved when subagent EOF |
| Each subagent jsonl file | a separate canonical Session (`Kind='sub_agent'`, parent linkage via `ParentNativeID`); turn/op inference identical to main session |
| `system.subtype=="turn_duration"` | `TurnFinalizedEvent` for the just-completed turn |
| `system.subtype=="api_error"` | `LogEntry` severity `ERR`; if the next record is a synthetic assistant or absence-of-assistant, mark the in-flight LLM op `status='failed', error_class=api_error_<status>` |
| `system.subtype=="compact_boundary"` | `LogEntry` `INF` with body=compactMetadata; emit a synthetic op `Kind='internal', Name='compact'` carrying `Ts=record.timestamp`, `Duration=durationMs`, `BytesIn=preTokens`, `BytesOut=postTokens`. Subsequent records' `parentUuid` may be `null` (post-compaction chain restart); the adapter must accept that without error. See §9. |
| `system.subtype=="stop_hook_summary"` | `LogEntry` `DBG` (one per hook) plus aggregate `LogEntry` `INF`; no canonical op |
| `system.subtype=="local_command"` | `LogEntry` `INF` |
| `system.subtype=="informational"` | `LogEntry` `INF` |
| `attachment` (any subtype) | `LogEntry` `DBG`; for `file` and `compact_file_reference` subtypes, also a `PayloadRefEvent` for the backing content |
| `queue-operation` | `LogEntry` `INF` |
| `last-prompt` | UPDATE `sessions.extras_json.lastPrompt`; no event (no `Ts`) — implemented as last-wins in the adapter's in-memory state, flushed on `SourceProgress` |
| `custom-title` / `ai-title` | UPDATE `sessions.extras_json.title` (custom wins) |
| `permission-mode` | UPDATE `sessions.extras_json.permissionMode` |
| `pr-link` | APPEND to `sessions.extras_json.prLinks[]`; has `timestamp` so optionally also a `LogEntry` `INF` at that ts |
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

On `CREATE` of a new directory at depth 1 (a new project dir), the watcher `Add()`s the new dir.

On `CREATE` of a new directory at depth 2 (a new session subdir under a project), the watcher `Add()`s the `subagents/` subdir as soon as it appears, AND the `tool-results/` subdir.

### 6.2 Events the adapter cares about

- `CREATE` on `*.jsonl` → backfill from offset 0, then watch.
- `WRITE` (or Linux `MODIFY`) on a watched `*.jsonl` → read from last-known offset to EOF, parse line by line, advance offset.
- `WRITE` on `*.meta.json` (subagent sidecar) → re-read the small JSON file; update the session's `extras_json` from the metadata (it can change if `description` is rewritten on resume).
- `RENAME` / `MOVED_FROM` / `MOVED_TO` — not used by claude-code in normal operation (it appends in place). The adapter logs these but does not act on them in Phase 2.
- `DELETE` — operator deleted a project directory or jsonl. The adapter logs and stops watching the missing file. Existing rows in SQLite remain (read-only viewer; data is never deleted).

### 6.3 Tail semantics

Stream-parse line-by-line from the cursor offset. A line without a trailing `\n` is an in-flight write; the adapter parks the partial bytes and resumes on the next event. A line that parses but has unknown `type` produces a `SourceError`. A line that fails to parse as JSON: same. The adapter does not skip bytes blindly; it always advances `offset` past completed lines only.

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
  }
}
```

Keys are paths **relative to the configured root** (`~/.claude/projects/`). The adapter never stores absolute paths in the cursor — moving the projects dir or running under a different `CLAUDE_CONFIG_DIR` shifts the root, not every cursor key.

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

## 9. Compaction Handling

Compaction is Claude Code's mechanism for summarizing a long conversation to stay within the model's context window. It is signalled by a `system.subtype=="compact_boundary"` record (verified shape, §3.3).

### 9.1 Pre/post chain

- Records BEFORE the boundary belong to the pre-compaction era; their `parentUuid` chain is intact backward to the session start.
- The compact boundary record itself has `logicalParentUuid` pointing into the pre-era and `parentUuid: null` (verified observation: `parentUuid:null, logicalParentUuid:"<pre-uuid>"`).
- The first user record AFTER the boundary is typically `isCompactSummary:true` with `message.content` = a long string starting `"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion..."`. Its `parentUuid` points to the boundary record.
- Subsequent records form a fresh chain rooted at the summary user message.

### 9.2 Canonical-model treatment

The decision: **keep both pre- and post-compaction history in the canonical model**, separated by a synthetic internal op.

1. The `compact_boundary` record emits one synthetic `OpStartedEvent`/`OpFinalizedEvent` pair with `Kind='internal'`, `Name='compact'`, `Ts=boundary.timestamp`, `EndTs = boundary.timestamp + compactMetadata.durationMs * 1000`. `BytesIn = compactMetadata.preTokens`, `BytesOut = compactMetadata.postTokens`, `Extras = compactMetadata`.
2. The `isCompactSummary:true` user message after the boundary does NOT start a new turn. It is emitted as a `LogEntry` `INF` and as a `PayloadRef` for the summary text (so the UI can show it in a "compaction" lane).
3. The next regular user-prompt record opens a new turn as usual.
4. The pre-compaction turn count is preserved; the post-compaction continues numbering monotonically.

This means the UI can display "Turn N: compacted here" without losing history and can render the compaction as a single visible event with its own latency, tokens-in/out, and metadata.

### 9.3 Manual vs auto compaction

Observed `compactMetadata.trigger` value: `"manual"` (operator ran `/compact`). The source declares `auto` (automatic context-window-pressure compaction) and `clear` (cleared, not summarized) but they were not observed in the operator's data. The adapter must tolerate any string value and surface it in `extras_json`.

## 10. Edge Cases

### 10.1 Older session layouts

Sessions older than ~Mar 2026 may live as a `<sessionId>/` directory containing ONLY `subagents/` — no parent `.jsonl` at root. Verified observation: `~/.claude/projects/-home-costa-src-alerts/<sessionId>/subagents/agent-*.jsonl` exists; no `<sessionId>.jsonl` next to it. Even older (Feb 2026) sessions have just `subagents/` with two orphan files and nothing else.

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
3. **Compaction is structural, not an op.** Modeled as a synthetic `Kind='internal'` op (§9). The canonical model already supports `Kind='internal'`, so no schema change is needed — but the UI must learn to render `compact_boundary` distinctively (a different lane / icon).
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
