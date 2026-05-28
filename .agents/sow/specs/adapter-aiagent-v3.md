# Adapter: ai-agent v3 (split format)

## 1. Status

**Phase 1 target — highest priority.** The operator's primary use case is observing their own `ai-agent` sessions, and v3 is the format `ai-agent` writes today. This adapter is the first one delivered.

Authoritative upstream source: `ai-agent.git`. The v3 evidence format was introduced by `ai-agent.git/.agents/sow/done/SOW-0002-20260428-memory-review-reduction.md` and is described by `ai-agent.git/.agents/sow/specs/snapshots.md` ("v3 Evidence Format" section). The Go adapter is, conceptually, a re-implementation of `ai-agent.git/src/evidence/reader.ts` that emits ai-viewer canonical events instead of returning in-memory views.

This spec is grounded in:
- Direct inspection of 14 296 real session directories and 17 356 real `.jsonl` ledger files under `~/.ai-agent/sessions/` on the operator's workstation as of 2026-05-26.
- The TypeScript producer source at `ai-agent.git/src/evidence/{types,writer,session-recorder,paths,reader,json-stream}.ts`.
- The upstream spec at `ai-agent.git/.agents/sow/specs/snapshots.md`.

Where reality and the upstream spec diverge, this spec records reality and flags the gap. Where the upstream type system declares optional fields that have never appeared in real data, this spec records both the declared schema (defensive parsing target) and the observed cardinality (so test fixtures can be honest).

## 2. Source Format

### 2.1 Filesystem Layout

The single source-of-truth root is `<sessions-dir>` — by default `~/.ai-agent/sessions/`, configured via `persistence.sessionsDir` and overridable per `AIAgentSession` instance (`ai-agent.git/src/persistence.ts:24`, `:36`).

The viewer's ingester is configured with this root as its `location`. Under that root the v3 adapter cares about:

```text
<sessions-dir>/
  session/
    <sessionId>.jsonl                       # append-only ledger, one JSON record per line
  payloads/
    <sessionId>/
      turn-NNNN/                            # NNNN = zero-padded 4-digit turn number
        llm-NNNN-request.http.gz            # NNNN = zero-padded 4-digit opIndex
        llm-NNNN-response.sse.gz
        llm-NNNN-response.http.gz           # observed alternative when provider returns non-SSE
        llm-NNNN-sdk-request.json.gz
        llm-NNNN-sdk-response.json.gz
        llm-NNNN-reasoning.txt.gz           # declared by producer; not observed in real data
        tool-NNNN-request.jsonrpc.gz        # declared by producer; not observed in real data
        tool-NNNN-response.jsonrpc.gz       # declared by producer; not observed in real data
        <any-payload>.gz.tmp-<pid>-<N>      # in-flight or aborted payload capture; MUST be ignored
```

Path construction is authoritative in `ai-agent.git/src/evidence/paths.ts`:
- ledger: `path.join(sessionsDir, 'session', sessionId + '.jsonl')` (line 12)
- turn dir: `path.posix.join('payloads', sessionId, 'turn-' + padNumber(turn))` (line 16)
- payload filename stem: per-`EvidencePayloadKind` switch (lines 20-37)
- payload extension: equals `format`, except `format=='text'` maps to `txt` (line 40)
- final compression suffix: `.gz` is appended unconditionally by `buildEvidencePayloadRelativePath` (line 47)

`padNumber` zero-pads to **width 4** (`paths.ts:7`); the adapter MUST treat the field as opaque and parse path strings only from the ledger's `payloadRefs[].path`, never reconstruct them from `(turn, opIndex)` itself.

### 2.2 Shared Root with v2 (Mixed Migration State)

The same `<sessions-dir>` root also holds the legacy v2 format. v2 files are top-level `<originId>.json.gz` (gzipped JSON snapshot, single file per session). The two formats coexist under the same root during migration; ai-agent currently writes both for compatibility (snapshots.md "Runtime Write Path" item 1).

Observed real data at `~/.ai-agent/sessions/` (2026-05-26):
- `<sessions-dir>/*.json.gz` — v2 snapshots, many tens of thousands of files
- `<sessions-dir>/*.json.gz.tmp-<pid>-<ts>` — v2 aborted/in-flight writes, two files observed
- `<sessions-dir>/session/<sessionId>.jsonl` — v3 ledgers, 17 356 files
- `<sessions-dir>/payloads/<sessionId>/turn-NNNN/*.gz` — v3 payloads, 14 296 session subdirs
- `<sessions-dir>/payloads/<sessionId>/turn-NNNN/*.gz.tmp-<pid>-<N>` — v3 aborted payload writes (observed in real data)

**v2/v3 boundary rule for the adapter:**

The v3 adapter is responsible for `<sessions-dir>/session/` and `<sessions-dir>/payloads/`. It MUST ignore everything else at the root, including:
- `*.json.gz` (v2 — owned by the aiagent_v2 adapter)
- `*.json.gz.tmp-*` (v2 aborted writes)
- `accounting.jsonl` (cross-session accounting ledger; not session evidence — see §2.3)
- `cache.db`, `cache.db-wal`, `cache.db-shm` (ai-agent internal cache; not session evidence)
- `maintainer-workflow.db`, `support.db*` (other ai-agent applications co-located in the parent dir)

The ingester runs both `aiagent_v3` and `aiagent_v2` adapters against the same configured `location` with distinct `Adapter.Name()` values. Each adapter writes its own row in `sources` and maintains its own cursor.

### 2.3 Out-of-Scope Files Under `~/.ai-agent/`

These files exist next to or above `<sessions-dir>` and are NOT part of the v3 evidence contract. The adapter MUST NOT read them in Phase 1:

- `~/.ai-agent/accounting.jsonl` (1.7 GB at observation time): cross-session per-LLM-call ledger written by `ai-agent.git/src/persistence.ts:69-80`. Shape per record: `{type:"llm"|"tool", timestamp, status, latency, mcpServer?|model?, command?, charactersIn, charactersOut, tokensIn?, tokensOut?, costUsd?}`. Useful for billing aggregates but a) not keyed by sessionId in a manner trivially joinable, b) huge, and c) already summarized into each session's `turn_end.accounting` and `session_summary.accounting`. A future SOW may add a billing-rollup ingest path; not Phase 1.
- `~/.ai-agent/cache.db` (+`-wal`,`-shm`): ai-agent's MCP response cache; irrelevant to viewing.

## 3. JSONL Record Contract

### 3.1 Envelope (all records)

Every record is a single-line JSON object with these required common fields (`ai-agent.git/src/evidence/types.ts:24-31`):

| field | type | semantics |
|---|---|---|
| `version` | `3` (literal int) | format version. Future versions must be detected by `version` mismatch and trigger a `SourceError`. |
| `recordType` | enum | one of `session_start`, `turn_start`, `turn_end`, `session_summary`, `session_error` |
| `seq` | int ≥ 1 | monotonic-per-session sequence, starts at 1, incremented per record by `EvidenceStore.appendRecord` (`writer.ts:122,150`) |
| `ts` | ISO-8601 string | UTC timestamp with millisecond precision, e.g. `"2026-05-14T23:53:27.925Z"` (`writer.ts:147` — `this.clock().toISOString()`). **NOT** microseconds-int as the canonical model uses internally; the adapter must convert. |
| `originId` | UUID string | the **root** session's id. Equal to `sessionId` for top-level sessions; inherited from the root through arbitrary sub-agent depth for descendants. Observed up to 3 levels deep in the operator's data. |
| `sessionId` | UUID string | this session's id. Matches the ledger filename `<sessionId>.jsonl`. |

`originId == sessionId` for root sessions; for descendants `originId` chains all the way to the root (verified empirically: parent `4bc1cfd9` has `originId=ce49e216`, grandchild `7fc971ee` also has `originId=ce49e216`, while their `parentSessionId` chain is `7fc971ee→4bc1cfd9→ce49e216`). The canonical adapter maps `originId` → canonical `root_session_id`.

### 3.2 `session_start`

Producer: `ai-agent.git/src/evidence/session-recorder.ts:359-372`. Schema: `EvidenceSessionStartBody` (`types.ts:33-42`).

Observed shape (every session has exactly one, always `seq=1`):

```json
{
  "recordType": "session_start",
  "agentId": "<agent-name>",
  "callPath": "<colon-separated-call-chain>",
  "parentSessionId": "<parent-uuid>",
  "headendId": "<cli|api|web|embed|slack|sub-agent|tool_output|history_compaction>",
  "capturePayloads": true,
  "attributes": {
    "ledgerPath": "session/<sessionId>.jsonl"
  },
  "version": 3,
  "seq": 1,
  "ts": "<ISO-8601>",
  "originId": "<root-uuid>",
  "sessionId": "<self-uuid>"
}
```

Field-by-field:

| field | required | type | notes |
|---|---|---|---|
| `agentId` | yes (de-facto) | string | declared optional in producer types (`types.ts:36`); always present in observed data. Equals the agent module name as registered with ai-agent (e.g. `bigquery`, `web-fetch`, `feed-enrichment`, `history_compaction.turn_summarizer`, `parent`, `spawn-parent`). Maps to canonical `agent_name`. |
| `callPath` | yes (de-facto) | string | colon-separated chain of agent invocations rooted at the originating headend (e.g. `feed-enrichment:web-search:web-fetch`). Useful UX hint; surface as part of `extras_json`. |
| `parentSessionId` | optional | string | **Present on 76% of observed sessions; 96.8% of `headendId=='sub-agent'` sessions, 100% of `history_compaction` and `tool_output` sessions, 0% of root headends (`cli`, `api`, `web`, `embed`, `slack`).** The remaining 3.2% of sub-agent sessions without it are early-format leftovers. The adapter SHOULD use this when present as the immediate-parent linkage fast path. (Producer code path: `session-recorder.ts:364`.) |
| `parentOpId` | not yet | string | declared in `EvidenceSessionStartBody` per the existing TypeScript types but **NOT** written by the current `recordSessionStart()` implementation, and observed in **0/500** sampled sessions. `ai-agent.git/.agents/sow/pending/SOW-0029` proposes adding it explicitly; the v3 adapter MUST tolerate its presence without depending on it. |
| `headendId` | yes (de-facto) | string | which entry-point launched the session. Observed distribution (1000 random session_starts): `cli=23.5%`, `sub-agent=62.2%`, `tool_output=9.6%`, `web=1.5%`, `api=1.2%`, `history_compaction=1.0%`, `slack=0.4%`, `embed=0.6%`. The adapter maps `cli|api|web|embed|slack` → canonical `Kind='root'`; `sub-agent|history_compaction` → `Kind='sub_agent'`; `tool_output` → `Kind='tool_internal'`. |
| `capturePayloads` | yes | bool | `true` when raw payloads were captured to disk; `false` when the run used `--no-capture-payloads` (still records refs with `captured:false`). |
| `attributes.ledgerPath` | yes | string | always `"session/<sessionId>.jsonl"`. Self-referential; the adapter does not need it. Producer writes it at `session-recorder.ts:369-370`. |
| `attributes.*` | optional | any | additional adapter-defined fields may appear in attributes per the producer type (`types.ts:41`). Adapter MUST stash unknown attribute keys into canonical `extras_json` rather than silently drop them. |

### 3.3 `turn_start`

Producer: `session-recorder.ts:375-383`. Schema: `EvidenceTurnStartBody` (`types.ts:44-49`).

Observed shape (one per turn; `turn` is 1-based):

```json
{
  "recordType": "turn_start",
  "turn": 1,
  "version": 3,
  "seq": 2,
  "ts": "<ISO-8601>",
  "originId": "<root-uuid>",
  "sessionId": "<self-uuid>"
}
```

Field-by-field:

| field | required | type | notes |
|---|---|---|---|
| `turn` | yes | int ≥ 1 | session-local turn number. Matches the `turn-NNNN` directory under `payloads/<sessionId>/`. |
| `attempt` | declared | int | declared optional (`types.ts:47`); **0 observations** in 500 random sessions. Future retry semantics; treat as opaque extras when present. |
| `attributes` | declared | object | declared optional (`types.ts:48`); **0 observations**. Treat as extras when present. |

### 3.4 `turn_end`

Producer: `session-recorder.ts:441-464`. Schema: `EvidenceTurnEndBody` (`types.ts:104-113`).

Observed shape (one per completed turn; missing when the turn was interrupted):

```json
{
  "recordType": "turn_end",
  "turn": 1,
  "status": "ok",
  "ops": [ /* see §3.4.1 */ ],
  "accounting": { /* see §3.4.2 */ },
  "warnings": [],
  "errors": [],
  "version": 3,
  "seq": 3,
  "ts": "<ISO-8601>",
  "originId": "<root-uuid>",
  "sessionId": "<self-uuid>"
}
```

Field-by-field:

| field | required | type | notes |
|---|---|---|---|
| `turn` | yes | int | matches the prior `turn_start.turn` |
| `status` | yes | `"ok"\|"failed"\|"running"` | `running` only emitted if a turn's recorder pushed an in-progress checkpoint; not observed in committed real data. Adapter maps to canonical turn `Status`. |
| `ops` | yes | array | compact per-op summaries; see §3.4.1 |
| `accounting` | optional | object | aggregate of LLM accounting entries across all ops; **absent on system-only turns** (no LLM call → no tokens to summarize, see `session-recorder.ts:114` `hasValues` guard) |
| `warnings` | yes (may be `[]`) | string[] | turn-level warning log messages (severity `WRN`) extracted from operation logs (`session-recorder.ts:338-340`) |
| `errors` | yes (may be `[]`) | string[] | turn-level error log messages (severity `ERR`) extracted from operation logs (`session-recorder.ts:342-344`). Free-form strings, can be multi-line and span hundreds of KB in degenerate cases. |
| `attributes` | optional | object | declared, not observed; treat as extras |

#### 3.4.1 `ops[]` items (`EvidenceOperationSummary`)

Schema: `types.ts:86-102`. Producer: `session-recorder.ts:237-267`.

Observed shape, varies by `kind`:

```json
{
  "opId": "<mp65...-...>",
  "opIndex": 1,
  "kind": "llm",
  "status": "ok",
  "startedAt": "<ISO-8601>",
  "endedAt": "<ISO-8601>",
  "durationMs": 21573,
  "provider": "nova",
  "model": "qwen3.6-35b-a3b",
  "payloadRefs": [ /* see §4 */ ],
  "accounting": {
    "tokensIn": 67182,
    "tokensOut": 554,
    "tokensCacheRead": 0,
    "tokensCacheWrite": 0
  },
  "attributes": {
    "provider": "nova",
    "model": "qwen3.6-35b-a3b",
    "isFinalTurn": false,
    "latency": 21543
  }
}
```

Field-by-field:

| field | required | type | notes |
|---|---|---|---|
| `opId` | yes | string | per-op id, generated by ai-agent (NOT a UUID; e.g. `mp659un4-tfh3lk`). Globally unique within a session. |
| `opIndex` | yes | int ≥ 1 | 1-based op-within-turn index used by payload filenames. Maps to canonical `op.seq`. |
| `kind` | yes | enum | one of `llm`, `session`, `system`, `tool` (`types.ts:10`). Cardinality across 100 random sessions: `tool=476`, `llm=265`, `session=111`, no `system` observed in random sampling. |
| `status` | yes | enum | one of `ok`, `failed`, `running`. 98% `ok`, 2% `failed` across 100 random sessions. |
| `startedAt` | de-facto | ISO string | always present in observed data, optional in producer types |
| `endedAt` | de-facto | ISO string | always present in observed data |
| `durationMs` | de-facto | int | always present in observed data; equals `endedAt - startedAt` in ms. Adapter prefers using `startedAt`/`endedAt` directly to compute its own microsecond timestamps; `durationMs` is denormalized convenience. |
| `name` | depends | string | present on `tool` and `session` kinds; absent on `llm`. For `tool`: the tool name (e.g. `bigquery__execute_sql`, `tool_output_fs__Read`). For `session`: the agent name (e.g. `history_compaction.turn_summarizer`). |
| `provider` | depends | string | for `llm`: the LLM provider (e.g. `nova`, `openrouter`). For `tool`: typically the tool namespace (`bigquery`, `agent`). For `session`: the orchestration provider (e.g. `history-compaction`). |
| `model` | llm-only | string | the model name; populated only for `kind='llm'` |
| `payloadRefs` | llm-mostly | array | only LLM ops have payload refs in current real data (see §4 for full type); declared producer-side for tool ops too but **0 observations**. |
| `childSessions` | session-only | array | populated only when `kind='session'`; one entry per child session attached to this op. Schema: §3.4.3. |
| `accounting` | llm-mostly | object | populated when the op contributed to token usage; only LLM ops in observed data (`session-recorder.ts:93-94` filters `entry.type==='llm'`). |
| `attributes` | de-facto | object | always present in observed data, contains operational metadata. Common keys (observed): `provider`, `model`, `isFinalTurn`, `latency`, `kind` (often `mcp` for tools), `name`, `size`, `error`, `archivedTurn`, `currentTurn`. Treat as extras. |
| `error` | declared | string | declared at `types.ts:101`, **0 observations**. Op-level errors are surfaced via `turn_end.errors[]` and via `status='failed'`. Adapter must tolerate but not depend on. |
| `parentOpId` | declared | n/a | the bootstrap sketch listed `parentOpId` on ops, but the producer schema **does not have it** (only `EvidenceChildSessionRef.parentOpId` and the never-emitted `session_start.parentOpId`). Canonical `parent_op_id` is therefore not derivable from v3 for non-session ops in Phase 1; adapter emits `ParentOpSeq=-1` (top-level) for every op. |

#### 3.4.2 `accounting` (`EvidenceAccountingSummary`)

Schema: `types.ts:78-84`. Producer: `session-recorder.ts:92-124`.

```json
{
  "tokensIn": 67182,
  "tokensOut": 554,
  "tokensCacheRead": 0,
  "tokensCacheWrite": 0,
  "costUsd": 0.0024
}
```

| field | required | type | notes |
|---|---|---|---|
| `tokensIn` | yes | int | sum of `entry.tokens.inputTokens` across LLM accounting entries |
| `tokensOut` | yes | int | sum of `entry.tokens.outputTokens` |
| `tokensCacheRead` | yes | int | sum of `tokens.cacheReadInputTokens ?? tokens.cachedTokens ?? 0` |
| `tokensCacheWrite` | yes | int | sum of `tokens.cacheWriteInputTokens ?? 0` |
| `costUsd` | optional | float | sum of `entry.costUsd`. **Producer-side detail (`session-recorder.ts:115`): `costUsd` is omitted entirely when it sums to exactly zero**; therefore `costUsd` absent ≠ unknown, it means $0.00. Adapter SHOULD treat absent `costUsd` as 0 when other token counts are present. |

Note: ai-agent v3 currently emits four token-count fields (`tokensIn`, `tokensOut`, `tokensCacheRead`, `tokensCacheWrite`); canonical events have only `tokens_in` and `tokens_out` (see §10 gap).

#### 3.4.3 `childSessions[]` items (`EvidenceChildSessionRef`)

Schema: `types.ts:51-60`. Appears on session-kind ops within `turn_end.ops[]`, and also at the top level of `session_summary.childSessions[]`.

```json
{
  "agentId": "history_compaction.turn_summarizer",
  "callPath": "bigquery:history_compaction.turn_summarizer",
  "sessionId": "<child-uuid>",
  "originId": "<root-uuid>",
  "parentSessionId": "<this-session-uuid>",
  "parentOpId": "<parent-op-id>",
  "ledgerPath": "session/<child-uuid>.jsonl",
  "status": "ok"
}
```

| field | required | type | notes |
|---|---|---|---|
| `sessionId` | yes | string | the child's session id |
| `originId` | yes | string | the root session id (shared with this session in current architecture) |
| `parentSessionId` | yes | string | equals this session's `sessionId` |
| `parentOpId` | yes | string | the **opId** (not opIndex) inside this turn's `ops[]` that triggered the child. This is the canonical mechanism for child→parent-op linkage. |
| `ledgerPath` | yes | string | relative path under sessions-dir, always `session/<child-uuid>.jsonl` |
| `status` | yes | enum | `ok`, `failed`, or `running` at the time the ref was recorded |
| `agentId`, `callPath` | optional | string | present when known at parent recording time |

### 3.5 `session_summary`

Producer: `session-recorder.ts:466-485`. Schema: `EvidenceSessionSummaryBody` (`types.ts:115-126`).

Observed shape:

```json
{
  "recordType": "session_summary",
  "status": "ok",
  "accounting": { "tokensIn": 3227227, "tokensOut": 87566, "tokensCacheRead": 2780315, "tokensCacheWrite": 0 },
  "childSessions": [ /* all completed child refs */ ],
  "finalReport": { "format": "json", "captured": true },
  "version": 3,
  "seq": 85,
  "ts": "<ISO-8601>",
  "originId": "<root-uuid>",
  "sessionId": "<self-uuid>"
}
```

For failed sessions an additional `error: string` field is populated (observed):

```json
{
  "recordType": "session_summary",
  "status": "failed",
  "accounting": { /* ... */ },
  "finalReport": { "format": "sub-agent", "captured": true },
  "error": "Turn 2 failed after 2 attempts of 2 (maxTurns=2); last_error=invalid_response: final_turn_no_final_answer; slugs=retries_exhausted,final_turn,no_tools,final_report_missing,text_only,invalid_response.",
  ...
}
```

| field | required | type | notes |
|---|---|---|---|
| `status` | yes | enum | `ok` or `failed`. Maps to canonical session `Status='completed'\|'failed'`. (Producer enum allows `running`; never observed for `session_summary` since the summary is the terminal record.) |
| `accounting` | optional | object | per `summarizeAccounting`'s `hasValues` rule; absent when session did no LLM work |
| `childSessions` | optional | array | deduplicated union of all `turn_end.ops[*].childSessions[]` across the whole session — useful as a single-shot lookup for the canonical adapter, but redundant with per-turn data |
| `finalReport.format` | optional | string | format of the agent's final report (e.g. `json`, `markdown`, `sub-agent`) |
| `finalReport.captured` | yes (when finalReport exists) | bool | whether ai-agent recorded the final report |
| `error` | optional | string | free-form error message; present iff `status='failed'` |
| `attributes` | declared | object | declared, not observed |

### 3.6 `session_error`

Producer: `session-recorder.ts:487-495`. Schema: `EvidenceSessionErrorBody` (`types.ts:128-132`).

```json
{
  "recordType": "session_error",
  "error": "<error message>",
  "version": 3,
  "seq": <N>,
  "ts": "<ISO-8601>",
  "originId": "<root-uuid>",
  "sessionId": "<self-uuid>"
}
```

| field | required | type | notes |
|---|---|---|---|
| `error` | yes | string | failure message produced by `error instanceof Error ? error.message : String(error)` |
| `attributes` | declared | object | declared, not observed |

**Observed cardinality:** zero `session_error` records in 17 356 real ledger files. The producer only writes this record when an unhandled exception bubbles out of the session loop (`session-recorder.ts:487`); in practice failures terminate via `session_summary{status:'failed'}` instead. The adapter MUST still implement support for it (defensive), but tests should mark `session_error` as a rare/edge-case path.

### 3.7 Observed Record-Type Cardinality

Across 100 random session ledgers:
- `session_start`: 100 (one per ledger)
- `turn_start`: 322
- `turn_end`: 242
- `session_summary`: 80
- `session_error`: 0

Across 1000 random ledgers, distribution of the LAST record:
- `session_summary`: 803 (80.3% — cleanly completed)
- `session_start`: 183 (18.3% — orphans, never produced a single turn; usually crashed or aborted at startup)
- `turn_start`: 14 (1.4% — interrupted mid-turn; matching `turn_end` never landed)
- `session_error`: 0
- `turn_end` as last: 0 (final state is always `session_summary` when the agent shut down cleanly)

The "orphan" rate (18.3%) is high enough that the UI must show these sessions clearly (status=`unknown`/`abandoned`), not hide them.

## 4. Payload Artifacts

### 4.1 `EvidencePayloadRef`

Schema: `types.ts:62-76`. Found inside `turn_end.ops[].payloadRefs[]`.

```json
{
  "kind": "llm_request",
  "opId": "<op-id>",
  "turn": 2,
  "opIndex": 1,
  "format": "http",
  "compression": "gzip",
  "path": "payloads/<sessionId>/turn-0002/llm-0001-request.http.gz",
  "originalBytes": 248384,
  "compressedBytes": 64863,
  "sha256": "<hex>",
  "captured": true,
  "truncated": false,
  "redacted": false
}
```

Field-by-field:

| field | required | type | notes |
|---|---|---|---|
| `kind` | yes | enum | one of: `llm_request`, `llm_response`, `sdk_request`, `sdk_response`, `reasoning_stream`, `tool_request`, `tool_response` (`types.ts:15-22`) |
| `opId` | yes | string | matches the `opId` of the containing op |
| `turn` | yes | int | matches the containing `turn_end.turn` |
| `opIndex` | yes | int | matches the containing op's `opIndex` |
| `format` | yes | enum | one of: `http`, `json`, `jsonrpc`, `sse`, `text` (`types.ts:13`) |
| `compression` | optional | `"gzip"` | absent when `captured=false`; always `gzip` when present |
| `path` | optional | string | relative to `<sessions-dir>` (forward-slash separators always, per `path.posix.join` in `paths.ts:9,16,45`). Absent when `captured=false`. |
| `originalBytes` | optional | int | uncompressed byte length; absent for uncaptured refs unless the producer knew the size |
| `compressedBytes` | optional | int | on-disk gzip byte length; absent when `captured=false` |
| `sha256` | optional | string | hex sha256 of uncompressed payload; absent when `captured=false` |
| `captured` | yes | bool | `false` when payload capture was disabled (`--no-capture-payloads`) or aborted mid-stream |
| `truncated` | yes | bool | `true` when the producer truncated before writing (e.g. stream cancelled mid-response). Observed `false` in all sampled real data. |
| `redacted` | yes | bool | `true` when the producer redacted the payload. Observed `false` in all sampled real data. |

### 4.2 Observed Payload Kind × Format Cardinality

Across 200 random sessions:

| kind | format | count | notes |
|---|---|---|---|
| `llm_request` | `http` | 498 | raw HTTP request body (provider call) |
| `llm_response` | `sse` | 483 | server-sent event stream (typical for streaming chat) |
| `llm_response` | `http` | 9 | non-streaming response body (rare; specific providers) |
| `sdk_request` | `json` | 516 | normalized AI-SDK request payload before provider conversion |
| `sdk_response` | `json` | 516 | normalized AI-SDK response payload after provider conversion |
| `reasoning_stream` | `text` | 0 | declared (`types.ts:18`, `paths.ts:30`); never seen on disk |
| `tool_request` | `jsonrpc` | 0 | declared; never seen |
| `tool_response` | `jsonrpc` | 0 | declared; never seen |

Note: `sdk_request`/`sdk_response` outnumber `llm_request`/`llm_response` by a small margin because the SDK pair is written eagerly per LLM op, while the HTTP/SSE pair may be uncaptured on certain code paths (e.g. cached or short-circuited responses).

### 4.3 Resolution from Ref to File

Authoritative implementation: `ai-agent.git/src/evidence/reader.ts:354-365` (`resolveEvidencePayloadPath`).

Algorithm:
1. If `!ref.captured` OR `ref.path` is empty/undefined → uncaptured; no file exists; surface as an uncaptured reference only.
2. Resolve `payloadPath = path.resolve(sessionsDir, ...ref.path.split('/'))`.
3. Compute `relative = path.relative(sessionsDir, payloadPath)`.
4. **Reject if `relative.startsWith('..')` or `path.isAbsolute(relative)`** — path-traversal guard. The Go adapter MUST mirror this check; never trust `ref.path` to stay within the root.
5. The file is gzip-compressed; uncompressed bytes match `originalBytes` and `sha256`.

Canonical `PayloadRefEvent`:
- `LocationURI` = `file://<absolute-path-to-gz>` (the file URI of the resolved path; the presenter, not the adapter, reads bytes on demand).
- `PayloadKind` = v3 `kind` (one-to-one mapping; the canonical model's `kind` enum already mirrors v3's).
- `Format` = v3 `format`.
- `Compression` = `gzip` when `captured=true`, empty string when `captured=false`.
- `OriginalBytes` = v3 `originalBytes` when present; `-1` for unknown.
- `StoredBytes` = v3 `compressedBytes` when present; `0` for uncaptured.

Uncaptured refs are still emitted as `PayloadRefEvent` (with `Compression=""`, `LocationURI=""`) so the UI can report "payload was disabled / aborted / redacted" rather than silently drop the I/O fact.

## 5. Mapping to Canonical Events

The adapter consumes a v3 ledger and emits canonical events (`canonical-events.md`).

### 5.1 Record-to-Event Table

| v3 source record | Canonical events emitted | Notes |
|---|---|---|
| `session_start` | one `SessionStartedEvent` | `NativeID`=`sessionId`; `ParentNativeID`=`parentSessionId` (when present); `Kind`=`headendId→Kind` (§5.2); `AgentName`=`agentId`; `Model`=empty (not known until first LLM op); `Extras`=`{callPath, headendId, originId, ledgerPath, capturePayloads, raw_attributes...}` |
| `turn_start` | one `TurnStartedEvent` | `SessionNativeID`=`sessionId`; `Seq`=`turn` |
| `turn_end` | one `TurnFinalizedEvent` + per-op (`OpStartedEvent`+`OpFinalizedEvent`) + per-payloadRef `PayloadRefEvent` + zero or more `LogEntry` from `warnings[]`/`errors[]` + zero or more `SessionStartedEvent` synthesized from each `ops[*].childSessions[]` (if needed for parent→child linkage when the child's own session_start is unknown yet — but typically the child has its own ledger and emits its own session_started; in steady state these synthesized events are no-ops because the ingester deduplicates on `NativeID`) | See §5.3 for op fan-out. |
| `session_summary` | one `SessionFinalizedEvent` (Status=`completed`\|`failed`); optionally one `LogEntry` (severity=`ERR`) when `status='failed'` to surface the `error` string; optionally one `SessionUpdated` if the summary reveals an `agent_name` or `model` we did not already know | The `childSessions[]` array is reconciled defensively (already covered by per-turn parents), but if any child appears only in the summary (rare/edge), the adapter emits `SessionStartedEvent` for it on best-effort. |
| `session_error` | one `SessionFinalizedEvent` (Status=`failed`, ErrorClass=`session_error`); one `LogEntry` (severity=`ERR`) | very rare; observe-but-handle. |

### 5.2 `headendId` → canonical `Kind` mapping

| `headendId` | canonical `Kind` | rationale |
|---|---|---|
| `cli`, `api`, `web`, `embed`, `slack` | `root` | direct user entry points |
| `sub-agent` | `sub_agent` | a parent agent delegated work via the sub-agent tool |
| `history_compaction` | `sub_agent` | the parent invoked history compaction as a managed background task; structurally a sub-agent |
| `tool_output` | `tool_internal` | the parent invoked a tool whose internal logic spawned its own session for chunked-read/grep over a large tool output (`snapshots.md` lines 9, 12, 388) |
| anything else | `sub_agent` | conservative default — `parentSessionId` is set, so it's a child of something |

### 5.3 Op Fan-Out at `turn_end`

ai-agent v3 does NOT emit individual `op_started`/`op_ended` records mid-turn; it only reveals the entire `ops[]` list at `turn_end`. The adapter therefore emits both `OpStartedEvent` and `OpFinalizedEvent` for each op at `turn_end` time, using the op's recorded `startedAt`/`endedAt` for canonical `Ts` and `EndTs`. This is an accepted limitation: the "live ops in progress" view only updates per-turn, not per-op.

For each `ops[i]` (i starting at 0; canonical `Seq` = `i+1` = `opIndex`):

- Emit `OpStartedEvent` with: `SessionNativeID=sessionId`, `TurnSeq=turn`, `Seq=opIndex`, `ParentOpSeq=-1` (the v3 schema does not currently expose intra-turn op nesting; see §10 gap), `Kind=ops[i].kind`, `Name=ops[i].name ?? ""` (LLM ops have no `name`; use `model` as a display fallback), `ToolNamespace=ops[i].attributes.provider when kind=='tool'`, `Model=ops[i].model when kind=='llm'`, `Provider=ops[i].provider`, `ChildSessionNativeID=ops[i].childSessions[0].sessionId when kind=='session'` (only the first; if a `session` op has more than one childSession it's an edge case — emit additional synthetic `OpStartedEvent` rows OR record additional childSession ids in `Extras` — §10 gap).
- Emit `OpFinalizedEvent` with: `Status=ops[i].status`, `ErrorClass=""` (v3 op summary has no error-class taxonomy), `ErrorMessage=ops[i].error ?? ""` (zero observations), `EndTs=ops[i].endedAt`, `TokensIn=accounting.tokensIn`, `TokensOut=accounting.tokensOut`, `CostUSD=accounting.costUsd ?? 0`, `BytesIn=sum(payloadRefs[k].originalBytes for kind in {llm_request,sdk_request,tool_request})`, `BytesOut=sum(payloadRefs[k].originalBytes for kind in {llm_response,sdk_response,reasoning_stream,tool_response})`, `CtxUsed=accounting.tokensIn + accounting.tokensCacheRead` (best-effort context-window-in estimate; see §10 gap), `CtxMax=0` (unknown from ledger; populated downstream from `catalog_models.ctx_max`).
- For each `payloadRefs[k]`: emit one `PayloadRefEvent` (see §4.3).

Cache-token counters (`tokensCacheRead`, `tokensCacheWrite`) carry useful signal for prompt-caching cost analysis but are NOT in the canonical `OpFinalizedEvent`. The adapter stores them in canonical `extras_json` for now.

### 5.4 SourceSeq Assignment

Every emitted canonical event carries a stable `SourceSeq` — a deterministic per-(session, ledger-line) identifier (monotonic per file/session, stable across rescans). It is an observability counter and log-attribution aid, NOT a dedup gate or cross-source ordering key (dedup is a SQL-layer guarantee; see `ingester.md` §Dedup and Idempotency). The adapter constructs it from the ledger's `(sessionId, seq)` pair using a deterministic packing — for example `SourceSeq = ledger_seq * 1000 + sub_event_index` where `sub_event_index` orders sub-events from one ledger record (turn_start → op[0]_start → op[0]_finalize → payload_ref[0] → ... → turn_end). Exact packing decided at implementation time.

## 6. Watch Strategy

### 6.1 fsnotify subscriptions

The v3 adapter watches two directories under its configured `<sessions-dir>`:

1. **`<sessions-dir>/session/`** — directory watch. Receives `CREATE` (new session ledger created), `WRITE` (ledger append), `RENAME`/`MOVE` (atomic rename pattern), `CHMOD` (some filesystems also fire on append). On Linux with fsnotify-via-inotify, a single directory watch is sufficient and per-file watches are unnecessary.
2. **`<sessions-dir>/payloads/`** — RECURSIVE watch. New payload files do not require ingester action by themselves (payload writes precede the `turn_end` record that references them); but the ingester surfaces payload availability to the UI via `payload_refs` rows, and watching `payloads/` lets a future feature detect orphaned payloads (refs without a matching turn_end yet).

On platforms without inotify-style native recursion, the adapter walks the directory tree and adds watches lazily as new `<sessionId>/` subdirectories are created.

### 6.2 Event-to-action

| inotify event | adapter action |
|---|---|
| `CREATE` on `<sessions-dir>/session/<X>.jsonl` | new session: open at offset 0, parse all records, advance cursor |
| `WRITE` on `<sessions-dir>/session/<X>.jsonl` | open at last cursor offset, read to EOF, parse new records, advance cursor |
| `RENAME-TO` on `<sessions-dir>/session/<X>.jsonl` | treat as `CREATE` |
| `RENAME-FROM` on `<sessions-dir>/session/<X>.jsonl` | log warning; do not delete from DB (the producer is append-only and never renames ledgers — this should only happen if the operator manually moved a file) |
| `DELETE` on `<sessions-dir>/session/<X>.jsonl` | log warning; do not delete from DB |
| anything on `<sessions-dir>/payloads/...` | recompute "payload available" boolean for affected ops if a UI flag depends on it. Default Phase 1: no action; the presenter checks payload existence on demand. |

### 6.3 Debouncing

Multiple `WRITE` events arrive in rapid succession during an active session. The adapter coalesces them per-file with a small debounce window (50 ms is plenty; the producer batches turn records anyway). Coalescing window is a single goroutine per source that maintains `dirtySessions map[sessionId]struct{}` and flushes every debounce-tick.

### 6.4 Initial Scan

On startup, the adapter walks `<sessions-dir>/session/` once (NOT recursive; the directory is flat) and runs the WRITE path on every file whose `(size, mtime)` does not match the cursor. This catches anything written while the ingester was down.

### 6.5 In-flight Write Safety

`appendFile` on Linux opens with `O_APPEND`; appends ≤ PIPE_BUF (4096 B) are atomic at the syscall level. Larger lines (observed up to ~248 KB in the operator's data) MAY be interrupted by another process — but each session has exactly one writer (the producer process owning the session), so cross-process interleaving is not a real risk here. Still, the adapter MUST defend:

- **Read up to EOF. Then look back: if the last byte read is not `'\n'`, the trailing partial line is held back** until the next WRITE arrives and the line completes. The cursor advances only past the last complete `'\n'`-terminated line.
- A partial line that never completes (producer crashed mid-append) eventually appears stable — the file's `mtime` stops advancing. After a configurable idle window (default 30 s) the adapter SHOULD log a `SourceError` and skip the partial line. The next byte after the skipped line is the start of the next valid record (`appendFile` is line-bounded).

## 7. Cursor

### 7.1 Shape

```json
{
  "version": 1,
  "files": {
    "<sessionId>.jsonl": {
      "offset": <byte-offset-of-next-unread-byte>,
      "size":   <file-size-at-which-offset-was-recorded>,
      "lastSeq": <highest-ledger-seq-consumed>,
      "lastTs": "<ISO-8601-of-last-record>"
    }
  }
}
```

### 7.2 Resume Semantics

On startup:
1. Load cursor from `sources.cursor` for this adapter+location.
2. For each ledger file in `<sessions-dir>/session/`:
   - If the file is in the cursor and `size_on_disk == cursor.size` and `mtime_on_disk` is unchanged since last scan: skip (no new data).
   - If `size_on_disk > cursor.size`: read from `cursor.offset` to EOF; parse; emit events.
   - If `size_on_disk < cursor.size`: log a `SourceError` (something truncated the ledger — should never happen with append-only writes); reset to 0 and full-scan. This is a fail-safe; consult the operator before flushing the database.
   - If the file is not in the cursor: full-scan from byte 0.
3. After scanning, persist the new cursor via a `SourceProgress` event after each debounce flush.

### 7.3 Durability

Cursor is durable because:
- The ingester writes it as part of the same SQLite transaction that writes the canonical rows from the corresponding records. If the ingester crashes mid-transaction, the cursor reverts atomically with the row writes; on restart the re-read is idempotent (every writer table upserts on a natural identity — SQL-layer idempotency, see `ingester.md` §Dedup and Idempotency).
- The cursor's per-file byte `Offset` is the resume mechanism: on restart `Scan(since=cursor)` seeks past already-read bytes so completed records are not re-emitted. Resume is cursor-driven, not gated by a per-source `SourceSeq` high-water-mark (removed in SOW-0015; a scalar HWM cannot work when one `sourceID` aggregates many files whose `ledgerSeq` each restart at 1).

### 7.4 Cursor Size Bound

17 356 sessions × ~120 bytes per cursor entry ≈ 2 MB cursor JSON. That's fine in SQLite. If it grows past a threshold (≥10 MB) Phase 2 may split the cursor across rows or compress, but Phase 1 keeps it simple.

## 8. Sub-Agent Linkage

### 8.1 Today (parent-side resolution, fast-path-when-present)

Two independent mechanisms exist; the adapter uses the FIRST one that is present and consistent:

1. **Child-side fast path** (preferred when present): `session_start.parentSessionId` is set on the child's own ledger. Observed in 76% of total sessions and 96.8% of `headendId='sub-agent'` sessions. When the adapter sees this, it can immediately emit `SessionStartedEvent.ParentNativeID = parentSessionId` and rely on the ingester to attach. **No parent file required.** This was undocumented in the ai-viewer bootstrap sketch (which said v3 lacked child-side parent linkage); empirically the field IS already written by `ai-agent.git/src/evidence/session-recorder.ts:364` and is in `EvidenceSessionStartBody.parentSessionId` (`types.ts:37`).
2. **Parent-side canonical path**: when the parent's `turn_end.ops[].childSessions[*]` lists this child, the adapter learns linkage from the parent. This works for the 24% of sessions whose own `session_start` lacks `parentSessionId`.

Out-of-order arrival: a child may be ingested before its parent's `turn_end` lands, OR vice versa. Both orderings are handled because canonical `SessionStartedEvent` is upserted by `NativeID`; the ingester reconciles parent/child as evidence accumulates.

### 8.2 Future (explicit `parentOpId` on child)

`ai-agent.git/.agents/sow/pending/SOW-0029-20260526-evidence-explicit-parent-id-on-child.md` proposes adding **`parentOpId`** (and clarifying `parentSessionId`) to `session_start`. When that SOW lands, the child→parent-op linkage becomes resolvable from the child alone, removing dependency on the parent's `turn_end` for op-level attribution. The v3 adapter MUST tolerate both shapes (present and absent) and prefer the explicit field when present.

### 8.3 `originId` is the root

For every session, `originId` is the root session's id. The canonical adapter sets `root_session_id` from `originId` directly; this is one less walk-up needed by the ingester to compute root.

Observed: parent `4bc1cfd9` carries `originId=ce49e216`; grandchild `7fc971ee` also carries `originId=ce49e216`. The chain confirms `originId` is shared by every node in a sub-agent tree, and equals the root's `sessionId`.

### 8.4 callPath as topology hint

`callPath` (colon-separated) is a human-readable rendering of the same chain (e.g. `feed-enrichment:web-search:web-fetch`). The adapter stores it in `extras_json` for UI rendering; it is NOT used for ID resolution (it can collide when the same agent is invoked twice in one chain).

## 9. Edge Cases

### 9.1 Incomplete writes / partial lines

See §6.5. Hold back trailing partial lines until line-completion. Defer with a 30 s idle warning, then `SourceError` + skip-line resync.

### 9.2 Aborted payload captures (`*.gz.tmp-<pid>-<N>`)

Real data on disk contains hundreds of these (e.g. `payloads/507f8109-.../turn-0003/llm-0001-response.sse.gz.tmp-473706-10`). They result from the producer's abort path (`writer.ts:106-116`) when an LLM stream was cancelled or the response capture was interrupted mid-write. In the ledger, the corresponding `payloadRefs[i]` has `captured:false` and no `path`. The adapter:

- **MUST ignore `*.gz.tmp-*` files entirely** when scanning `payloads/`.
- **Trusts only `payloadRefs[].path`** for payload location. Never derives the path from `(turn, opIndex, kind, format)` even though the algorithm is deterministic — this defends against ai-agent changing its file layout in a future version.
- **Surfaces uncaptured refs** as `PayloadRefEvent` with `Compression=""`, `LocationURI=""`, `StoredBytes=0`. The UI shows "(captured: no — capture aborted or disabled)".

### 9.3 Orphan sessions (`session_start` only)

18.3% of ledgers contain only `session_start`. The ingester records a `sessions` row with `status='running'` and no turns. After a configurable retention (default 24 h since `start_ts`), a background reaper job MAY transition such sessions to `status='abandoned'` (canonical addition, see §10 gap). Phase 1: leave `status='running'`; surface the count in `/api/health`.

### 9.4 Interrupted turns (`turn_start` without `turn_end`)

1.4% of ledgers end on `turn_start`. The adapter emits `TurnStartedEvent` only; canonical `turns.status='running'`, `end_ts=NULL`. Same reaper rule as §9.3 may apply later.

### 9.5 Failed turns (`turn_end.status='failed'`)

2% of ops are `status='failed'` and the corresponding turns surface free-form messages in `turn_end.errors[]`. The adapter:
- Sets canonical `turns.status='failed'`, `turns.error_class=''` (no taxonomy from v3).
- Emits one `LogEntry{severity:'ERR', message: errors[j]}` per error string, attached to the turn.
- Truncates extremely long error strings (observed up to ~10 KB) at a configurable cap (default 64 KB) and appends a `…[truncated]` marker. The full string remains in the ledger; the canonical store does not need to mirror every byte.

### 9.6 Failed sessions (`session_summary.status='failed'`)

Sets canonical `sessions.status='failed'`. The `error` field becomes one `LogEntry{severity:'ERR'}` plus the canonical `error_class` (currently empty; could be parsed out of the error string in Phase 2).

### 9.7 Very old sessions (pre-`parentSessionId`)

Older v3 ledgers (early SOW-0002 days) lack `parentSessionId` on `session_start` despite being sub-agents. The adapter handles this transparently via the parent-side resolution path (§8.1). No special migration code is needed; the adapter is permissive.

### 9.8 Out-of-order `seq`

`seq` is monotonic-per-session by construction (`writer.ts:122,150` increments locally). The adapter checks `seq == lastSeq + 1` on every record and emits a `SourceError` on violation. This should never fire under normal operation; if it does, the file is corrupt.

### 9.9 mtime independent from content

A `touch` on a ledger file produces `WRITE`/`CHMOD` events but no new content. The adapter detects "size unchanged" and no-ops the parse pass.

### 9.10 v2 `.json.gz.tmp-*` files at root

Two such files were observed (`143f3e6c-...json.gz.tmp-702094-1768162665628`). The v3 adapter MUST ignore them — they live at the root level, outside `session/` and `payloads/`, so they are naturally out of scope. Listed here only so the spec is exhaustive.

### 9.11 Cross-process append safety

Each session is written by exactly one ai-agent process (the one running the agent). Concurrent writers do not exist for a single ledger. The adapter assumes single-writer semantics for any given `<sessionId>.jsonl`.

### 9.12 Filesystem boundaries

`<sessions-dir>` may live on a separate filesystem from the ingester's SQLite. `path.resolve` semantics (`reader.ts:358-360`) require ai-viewer's adapter to also use absolute-path resolution before the traversal check; resolve relative to the configured `location`, not `os.Getwd()`.

### 9.13 Concurrent two-adapter runs (v2 + v3)

Both adapters target the same `<sessions-dir>` root simultaneously. Each adapter has its own row in `sources` and an independent cursor. The same logical session MAY appear under BOTH adapters (v2 + v3 written in parallel during migration). The presenter is responsible for de-duplicating in the UI (group by `native_id`); the canonical store keeps both rows because their `source_id` differs.

## 10. Canonical Model Gaps

These v3 fields/concepts do not map cleanly into `canonical-events.md` as it stands. The adapter records them in `extras_json` or summary log entries today; a future SOW should decide whether to lift them into first-class canonical columns.

1. **Cache-token counts** (`tokensCacheRead`, `tokensCacheWrite` on `EvidenceAccountingSummary`). Critical for prompt-caching cost analysis. Canonical `OpFinalizedEvent` and `ops` table have only `tokens_in`/`tokens_out`. **Recommendation:** add `tokens_cache_read` and `tokens_cache_write` to `OpFinalizedEvent` and `ops`.

2. **`session_status='abandoned'`** for orphan sessions (§9.3) and **`session_status='interrupted'`** for mid-turn-killed sessions (§9.4). The canonical `Status` enum in `canonical-events.md` is `'completed'|'failed'`, plus implicitly `'running'`. **Recommendation:** add `'abandoned'` (no work ever did) and `'interrupted'` (work started, never finalized) so the UI can distinguish them from in-progress sessions.

3. **`finalReport.format` and `finalReport.captured`**. Carries the format of the final report (`json`, `markdown`, `sub-agent`). Useful for the UI to know whether the session produced a structured report and what format. **Recommendation:** add `final_report_format` and `final_report_captured` to canonical `sessions` (or stuff into `extras_json` for now).

4. **`callPath`**. The colon-separated agent invocation chain is a unique-per-session topology hint that's a) human-meaningful, b) cheap to index, c) useful for filtering. Storing it only in `extras_json` works but means slower lookups. **Recommendation:** add a `call_path` column to `sessions` and an index.

5. **`sha256` of payload artifacts**. The producer records sha256 per captured payload (`writer.ts:102`). Canonical `payload_refs` table has no `sha256` column. Useful for integrity verification and cross-session deduplication. **Recommendation:** add `sha256` column to `payload_refs` (nullable).

6. **Per-op error strings**. Canonical `OpFinalizedEvent` has `ErrorClass` and `ErrorMessage`. v3 has no per-op error class (the `error` field on `EvidenceOperationSummary` is declared but never observed); instead errors are reported at the turn level via `turn_end.errors[]`. The adapter loses the op→error association in the current canonical model. **Recommendation:** consider adding `turn_errors_json` to `turns` so the UI can show all errors without joining to `log_entries`.

7. **`parentOpId` on intra-turn op nesting**. Real data shows zero intra-turn op nesting (every op has the same flat structure under a turn). The canonical model supports `parent_op_id` for nested ops (`data-model.md` line 100), but v3 has no field to populate it from. If/when ai-agent surfaces reasoning sub-ops nested inside LLM ops, this field becomes populatable. Phase 1: always `parent_op_id=NULL` for v3.

8. **`session` ops with multiple `childSessions[]`**. Theoretically a single `kind='session'` op could spawn more than one child (e.g. a fan-out call). Empirically every observed `session` op has exactly one `childSessions[*]` entry. Canonical `OpStartedEvent.ChildSessionNativeID` is a single field. **Recommendation:** leave Phase 1 as "first child only, additional children in `extras_json`"; revisit if a real fan-out case appears.

9. **`history_compaction` semantics**. These sub-agents represent maintenance work (compacting older turns into summary form) rather than real user-visible operations. The UI should optionally filter them out from "main timeline" views. The canonical `kind='sub_agent'` does not distinguish them. **Recommendation:** add a `Subkind` or boolean `Maintenance` flag on canonical sessions, populated when `headendId='history_compaction'`.

10. **`opId` shape**. ai-agent v3 op ids are not UUIDs (e.g. `mp65agab-nrbosb`); they are domain-specific opaque strings unique within a session. Canonical `ops.id` is `TEXT` PRIMARY KEY — already permissive — so this is not a gap, just a note that the adapter MUST NOT assume UUIDs.

11. **Operation log lines.** The v3 producer extracts log entries from the opTree (`session-recorder.ts:233-235`) into `warnings[]`/`errors[]` only. Per-op DBG/INF logs that existed in the opTree are flattened away. The canonical `log_entries` table supports per-op attachment, but v3 does not surface enough information to attach a given log to a specific op — only to a turn. **Accepted limitation**; log entries attach to `turn_id` and `session_id`, with `op_id` NULL for v3.

## 11. References

Upstream source (commit-pinned at the time of writing, branch `main` of `ai-agent.git`):

- `ai-agent.git/src/evidence/types.ts:1-153` — record / payload / operation type definitions
- `ai-agent.git/src/evidence/writer.ts:1-209` — `EvidenceStore`, `EvidencePayloadCapture`, append/rename semantics, tmp-file naming
- `ai-agent.git/src/evidence/session-recorder.ts:1-531` — `SessionEvidenceRecorder`, record building, accounting summarization, child-session extraction
- `ai-agent.git/src/evidence/paths.ts:1-54` — path construction for ledger and payload files
- `ai-agent.git/src/evidence/reader.ts:1-379` — canonical TypeScript reader; the Go adapter mirrors its semantics (parse, partial-line, traversal-guard, session view, child tree)
- `ai-agent.git/src/evidence/json-stream.ts` — streaming JSON writer used for SDK payloads; relevant only as a reminder that `sdk_request`/`sdk_response` files can be large and gzip-streamed
- `ai-agent.git/src/persistence.ts:24,36,48-49` — `sessionsDir` defaults
- `ai-agent.git/.agents/sow/specs/snapshots.md` — authoritative format spec (the v3 producer side)
- `ai-agent.git/.agents/sow/done/SOW-0002-20260428-memory-review-reduction.md` — the SOW that introduced v3
- `ai-agent.git/.agents/sow/pending/SOW-0029-20260526-evidence-explicit-parent-id-on-child.md` — proposed enhancement to make `parentSessionId`/`parentOpId` first-class documented fields on `session_start`

ai-viewer canonical specs cited:

- `ai-viewer.git/.agents/sow/specs/canonical-events.md` — `Event`, `EventKind`, `SessionStartedEvent`, `TurnStartedEvent`, `OpStartedEvent`, etc.
- `ai-viewer.git/.agents/sow/specs/data-model.md` — `sources`, `sessions`, `turns`, `ops`, `payload_refs`, `log_entries`, indexes

Empirical evidence (operator workstation, 2026-05-26 — sanitized counts, no raw IDs):

- 17 356 v3 ledger files under `~/.ai-agent/sessions/session/`
- 14 296 payload directories under `~/.ai-agent/sessions/payloads/`
- Coexisting tens of thousands of v2 `.json.gz` files under `~/.ai-agent/sessions/`
- Distribution counts and cardinalities reported in §3.7 and §4.2 come from random samples of 100, 200, 500, and 1000 files using `shuf` on the workstation
