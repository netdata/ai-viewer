# Adapter: opencode

## Status

**Phase 2 target.** Opencode is the **only** adapter that does not read filesystem snapshots. Its source of truth is a single live, multi-GB SQLite database that opencode itself writes to concurrently. The watch/cursor/parse model differs fundamentally from every other adapter, and the read-safety story is the dominant design constraint.

This spec is evidence-driven against the operator's workstation database (3.9 GB, opencode `1.15.10` and ~30 earlier versions seen across rows) and the upstream source at `anomalyco/opencode @ 2b3ddf9f34546b9bcea25ec8e0ff57e2811c4537` (chore: cleanup 2026-05-25).

## Source Format (the SQLite Schema)

Opencode stores everything in one SQLite database file with WAL companions:

```
~/.local/share/opencode/opencode.db          main database
~/.local/share/opencode/opencode.db-wal      write-ahead log
~/.local/share/opencode/opencode.db-shm      shared-memory index
```

On the operator's workstation: main file 3.9 GB, WAL 5.5 MB, SHM 32 KB.

The schema is managed by Drizzle ORM (drizzle-sqlite-bun). The migration journal lives in the source repo at `anomalyco/opencode @ 2b3ddf9 :: packages/opencode/migration/` (~30 timestamped directories from `20260127222353_familiar_lady_ursula` through `20260511000411_data_migration_state`). Migrations are applied automatically by opencode at startup; the adapter MUST treat the schema as evolving and must never apply or migrate anything itself.

The connection PRAGMAs opencode uses are visible at `anomalyco/opencode @ 2b3ddf9 :: packages/opencode/src/storage/db.ts:104-109`:

```
PRAGMA journal_mode = WAL
PRAGMA synchronous  = NORMAL
PRAGMA busy_timeout = 5000
PRAGMA cache_size   = -64000
PRAGMA foreign_keys = ON
```

The database is in **WAL mode** and `synchronous=NORMAL`. Both matter for the read story below.

### Tables we read

The adapter reads from the following tables. Other tables (`account`, `account_state`, `control_account`, `permission`, `session_share`, `workspace`, `data_migration`, `__drizzle_migrations`) are out of scope.

#### `session` — root entity (one row per opencode session)

Per `anomalyco/opencode @ 2b3ddf9 :: packages/opencode/src/session/session.sql.ts:16-59`:

| column | SQLite type | notes |
|---|---|---|
| `id` TEXT PK | `ses_<sonyflake>` |
| `project_id` TEXT NOT NULL | FK → `project.id` ON DELETE CASCADE |
| `workspace_id` TEXT NULL | FK to `workspace.id`; NULL for all 6785 operator sessions (workspace is a Console/cloud feature) |
| `parent_id` TEXT NULL | child session pointer; NULL = root session |
| `slug` TEXT NOT NULL | human-readable handle (e.g. `glowing-panda`) |
| `directory` TEXT NOT NULL | working directory at session start (e.g. `/home/costa/src/PRs/topology-containers`) — sensitive |
| `path` TEXT NULL | added by `20260428004200_add_session_path` |
| `title` TEXT NOT NULL | session title (operator-facing, may contain PR titles, SOW labels) |
| `version` TEXT NOT NULL | opencode CLI version that wrote this row (e.g. `1.15.10`) |
| `share_url` TEXT NULL | public share URL if uploaded |
| `summary_additions` INTEGER NULL | diff stats (compaction artifact) |
| `summary_deletions` INTEGER NULL | |
| `summary_files` INTEGER NULL | |
| `summary_diffs` TEXT(JSON) NULL | array of `FileDiff` (compaction artifact) |
| `revert` TEXT(JSON) NULL | `{messageID, partID?, snapshot?, diff?}` |
| `permission` TEXT(JSON) NULL | `Permission.Ruleset` |
| `agent` TEXT NULL | agent name (e.g. `code-reviewer`, `general`); blank for many older rows |
| `model` TEXT(JSON) NULL | `{id, providerID, variant?}`; e.g. `{"id":"glm-5.1","providerID":"llm-netdata-cloud","variant":"default"}` |
| `cost` REAL NOT NULL DEFAULT 0 | rolled-up USD cost; added by `20260510033149_session_usage` (NULL/0 on older rows) |
| `tokens_input` INTEGER NOT NULL DEFAULT 0 | added by `20260510033149_session_usage` |
| `tokens_output` INTEGER NOT NULL DEFAULT 0 | same |
| `tokens_reasoning` INTEGER NOT NULL DEFAULT 0 | same |
| `tokens_cache_read` INTEGER NOT NULL DEFAULT 0 | same |
| `tokens_cache_write` INTEGER NOT NULL DEFAULT 0 | same |
| `time_created` INTEGER NOT NULL | epoch milliseconds (NOT microseconds) |
| `time_updated` INTEGER NOT NULL | epoch milliseconds, auto-updated on row write |
| `time_compacting` INTEGER NULL | non-null while a compaction is running |
| `time_archived` INTEGER NULL | session archived/soft-deleted by user |

Indexes: `session_project_idx(project_id)`, `session_workspace_idx(workspace_id)`, `session_parent_idx(parent_id)`. Note: no index on `time_created`/`time_updated`, so we will pay a scan or maintain our own ordering structures.

Observed row counts on the operator's DB: 6778 sessions; 5493 root (parent_id NULL); 1285 child (parent_id set); 2 archived.

#### `message` — one row per chat-turn message

Per `session.sql.ts:61-73`:

| column | type | notes |
|---|---|---|
| `id` TEXT PK | `msg_<sonyflake>` |
| `session_id` TEXT NOT NULL | FK → `session.id` CASCADE |
| `time_created` INTEGER NOT NULL | ms epoch |
| `time_updated` INTEGER NOT NULL | ms epoch |
| `data` TEXT(JSON) NOT NULL | discriminated union: `User` \| `Assistant` (see below) |

Index: `message_session_time_created_id_idx(session_id, time_created, id)` — covers our primary read pattern.

Authoritative TypeScript shapes at `anomalyco/opencode @ 2b3ddf9 :: packages/opencode/src/session/message-v2.ts:327-490`:

`User` message `data`:
```jsonc
{
  "id": "msg_...",        // duplicated in JSON
  "sessionID": "ses_...",
  "role": "user",
  "time": { "created": <ms> },
  "format": "...",         // optional
  "summary": { "title?": "...", "body?": "...", "diffs": [FileDiff] },
  "agent": "code-reviewer",
  "model": { "providerID": "...", "modelID": "...", "variant?": "..." },
  "system?": "...",        // optional
  "tools?": { "<toolName>": <bool> }
}
```

`Assistant` message `data`:
```jsonc
{
  "id": "msg_...",
  "sessionID": "ses_...",
  "role": "assistant",
  "parentID": "msg_...",   // user message that triggered this turn
  "agent": "code-reviewer",
  "modelID": "glm-5.1",
  "providerID": "llm-netdata-cloud",
  "mode": "code-reviewer", // deprecated, equal to agent
  "path": { "cwd": "...", "root": "..." },
  "summary?": <bool>,
  "cost": 0.0,
  "tokens": {
    "total?": 102094,
    "input": 625,
    "output": 77,
    "reasoning": 16,
    "cache": { "read": 101376, "write": 0 }
  },
  "time": { "created": <ms>, "completed?": <ms> },
  "finish?": "stop" | "tool-calls" | ...,
  "variant?": "...",
  "structured?": <any>,
  "error?": <AssistantError tagged union>
}
```

Observed: 127345 messages on the operator's DB (117440 assistant, 9907 user, `-2` reconciliation between table and union counts is normal). Token totals on the message are the **session-running total at completion of this turn**, not the per-step token usage (see step-finish parts).

#### `part` — fine-grained pieces of a message (text, tool calls, reasoning, etc.)

Per `session.sql.ts:75-91`:

| column | type | notes |
|---|---|---|
| `id` TEXT PK | `prt_<sonyflake>` — lexicographically sortable as time |
| `message_id` TEXT NOT NULL | FK → `message.id` CASCADE |
| `session_id` TEXT NOT NULL | denormalized; equals the host session even for sub-agent dispatch parts |
| `time_created` INTEGER NOT NULL | ms epoch |
| `time_updated` INTEGER NOT NULL | ms epoch |
| `data` TEXT(JSON) NOT NULL | discriminated union on `$.type` (12 variants) |

Indexes: `part_message_id_id_idx(message_id, id)`, `part_session_idx(session_id)`.

Authoritative Part union at `message-v2.ts:352-378`. Observed `$.type` distribution on the operator's DB:

| type | count | shape |
|---|---|---|
| `tool` | 199,635 | `{type:"tool", callID, tool, state:ToolState, metadata?}` |
| `step-start` | 117,119 | `{type:"step-start", snapshot?}` |
| `step-finish` | 116,589 | `{type:"step-finish", reason, snapshot?, cost, tokens:{input,output,reasoning,total?,cache:{read,write}}}` |
| `text` | 73,439 | `{type:"text", text, synthetic?, ignored?, time?:{start,end?}, metadata?}` |
| `reasoning` | 67,576 | `{type:"reasoning", text, time:{start,end?}, metadata?}` |
| `patch` | 11,082 | `{type:"patch", hash, files:[<absolute-path>]}` |
| `compaction` | 432 | `{type:"compaction", auto, overflow?, tail_start_id?}` |
| `file` | 22 | `{type:"file", mime, filename?, url, source?}` |
| `retry` | 17 | `{type:"retry", attempt, error:APIError, time:{created}}` |
| `snapshot` | 0 in this DB | `{type:"snapshot", snapshot:<hash>}` |
| `subtask` | 0 in this DB | `{type:"subtask", prompt, description, agent, model?, command?}` — sub-agent dispatch |
| `agent` | 0 in this DB | `{type:"agent", name, source?}` |

`ToolState` is a tagged union (`message-v2.ts:248-308`):

```jsonc
// pending
{ "status":"pending", "input": {...}, "raw": "<incomplete json>" }
// running
{ "status":"running", "input": {...}, "title?": "...", "metadata?": {...},
  "time": { "start": <ms> } }
// completed
{ "status":"completed", "input": {...}, "output": "<string>", "title": "...",
  "metadata": {...}, "time": { "start": <ms>, "end": <ms>, "compacted?": <ms> },
  "attachments?": [FilePart] }
// error
{ "status":"error", "input": {...}, "error": "<string>", "metadata?": {...},
  "time": { "start": <ms>, "end": <ms> } }
```

Observed tool-state distribution on the operator's DB: 197044 completed, 2423 error, 144 running, 27 pending. Top tools by frequency: `read` (97k), `bash` (54k), `grep` (29k), `glob` (8k), `edit` (6.4k), `todowrite` (1.5k), `task` (1.3k), `write` (852), `webfetch` (587). MCP tools appear under namespaced names like `github_get_file_contents`, `playwright_headless_browser_take_screenshot`, `jina_jina-read_url`, `netdata_cloud_get_metric_data`, `sourcegraph_sourcegraph-Search`, `skill`.

#### `session_message` — sidecar event log (model/agent switches)

Per `session.sql.ts:112-129`:

| column | type | notes |
|---|---|---|
| `id` TEXT PK | `evt_<sonyflake>` |
| `session_id` TEXT NOT NULL | FK → `session.id` CASCADE |
| `type` TEXT NOT NULL | currently always `agent-switched` or `model-switched` |
| `time_created` INTEGER NOT NULL | ms epoch |
| `time_updated` INTEGER NOT NULL | ms epoch |
| `data` TEXT(JSON) NOT NULL | per `anomalyco/opencode @ 2b3ddf9 :: packages/core/src/session-message.ts:20-30` |

Indexes: `session_message_session_idx`, `session_message_session_type_idx`, `session_message_time_created_idx`.

Observed: 3985 rows (1992 agent-switched + 1993 model-switched). The TypeScript schema (`session-message.ts:165`) defines a wider union (`User`, `Synthetic`, `Shell`, `Assistant`, `Compaction`) under a planned next-gen pipeline, but only two variants ship today. Treat unknown types as forward-compatibility data and skip with a structured WARN log.

#### `project` — workspace/project a session belongs to

Per `anomalyco/opencode @ 2b3ddf9 :: packages/opencode/src/project/project.sql` (referenced from session.sql.ts):

| column | type | notes |
|---|---|---|
| `id` TEXT PK | hex SHA-ish hash of worktree path |
| `worktree` TEXT NOT NULL | absolute path on disk |
| `vcs` TEXT NULL | `git`/`hg`/null |
| `name` TEXT NULL | display name |
| `icon_url`, `icon_color`, `icon_url_override` | UI cosmetics |
| `sandboxes` TEXT NOT NULL(JSON) | sandbox config |
| `commands` TEXT NULL(JSON) | project-defined slash commands |
| `time_created`/`time_updated`/`time_initialized` | ms epoch |

Observed: 32 projects. Used by the adapter only to enrich session.directory display.

#### `event` and `event_sequence` — sync queue (added 20260323234822)

| column | type |
|---|---|
| `event_sequence.aggregate_id` TEXT PK | usually a session_id |
| `event_sequence.seq` INTEGER NOT NULL | monotonic per aggregate |
| `event_sequence.owner_id` TEXT NULL | added by `20260504145000_add_sync_owner` |
| `event.id` TEXT PK | |
| `event.aggregate_id` TEXT NOT NULL | FK → `event_sequence.aggregate_id` |
| `event.seq` INTEGER NOT NULL | dedup key |
| `event.type` TEXT NOT NULL | |
| `event.data` TEXT(JSON) NOT NULL | |

Observed on the operator's DB: **0 rows** in both tables. This is an opencode-internal outbox for opencode's own cloud sync, not a persistent event log. Out of scope for v1; revisit if it starts being populated.

#### `__drizzle_migrations` — schema-version probe

| column | type |
|---|---|
| `id` SERIAL PK | |
| `hash` TEXT NOT NULL | |
| `created_at` numeric NULL | |
| `name` TEXT NULL | migration directory name |
| `applied_at` TEXT NULL | |

Observed: 20 migrations applied (range `20260127222353_familiar_lady_ursula` … `20260511000411_data_migration_state`). The adapter queries this table at startup to determine which optional columns are present (see Edge Cases).

## Read Strategy

The defining constraint: opencode's writer holds the database open and may commit transactions at any time. ai-viewer is a strict read-only consumer. The adapter MUST:

1. Open with `mode=ro&_journal_mode=WAL&_busy_timeout=5000&_txlock=deferred`.
2. Use `modernc.org/sqlite` (CGO-free, per AGENTS.md tech stack).
3. Open a **fresh connection per poll cycle** or keep a pool with `SetMaxOpenConns(1)` for the read path. Opening read-only against a WAL-mode database is non-blocking for the writer — multiple readers and a single writer can proceed concurrently (SQLite WAL guarantee). Concrete DSN:

```
file:%2Fhome%2Fcosta%2F.local%2Fshare%2Fopencode%2Fopencode.db?mode=ro&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=query_only(true)&_pragma=foreign_keys(off)&_txlock=deferred
```

Key choices:

- `mode=ro` — refuse any write at the OS level. The OS opens the file `O_RDONLY`; SQLite cannot upgrade the connection.
- `_pragma=query_only(true)` — defense-in-depth; rejects any UPDATE/INSERT/DELETE at the SQL layer.
- `_pragma=journal_mode(WAL)` — does NOT change the file (the writer already set it); confirms our connection enters WAL reader mode so we read a consistent snapshot from the WAL checkpoint and never block the writer.
- `_pragma=busy_timeout(5000)` — wait up to 5 s when an exclusive lock is held (which should be rare in WAL mode but happens during `PRAGMA wal_checkpoint(TRUNCATE)`).
- `_pragma=foreign_keys(off)` — readers don't need FK enforcement; cheaper.
- `_txlock=deferred` — defer BEGIN until first statement; for reads this just means "snapshot taken on first SELECT".

**Never** call `PRAGMA wal_checkpoint`, `PRAGMA optimize`, `VACUUM`, `BEGIN EXCLUSIVE`, or `ATTACH … AS … 'rwc'`. The connection MUST remain a pure reader.

Connection pool settings:

- `SetMaxOpenConns(2)` — one for the watch poll, one for the (rare) presenter-triggered re-read.
- `SetConnMaxLifetime(30 * time.Minute)` — recycle to release any stale WAL pages from cache.
- `SetMaxIdleConns(1)`.

Read transactions: every query batch wraps in `BEGIN DEFERRED ... COMMIT` so the adapter sees a consistent snapshot across multi-statement cursors. Long transactions on a WAL DB pin the WAL and stop checkpointing, so each cycle keeps its transaction shorter than 1 s of wall time. If a backfill must read more than a few thousand rows, it does so in **paged transactions** (close the transaction every N rows, then start a new one), accepting that the snapshot advances between pages.

## Watch Strategy

SQLite does not notify external consumers of writes. The adapter has two complementary signals:

1. **Coarse fsnotify on the WAL file.** When opencode commits, the `.opencode.db-wal` file is appended to (mtime + size change). When a checkpoint runs, the WAL is truncated. `fsnotify.Add()` on the parent directory and reacting to WRITE/CHMOD events for `opencode.db-wal` gives us a cheap "something happened" hint. We do not block on this — it is purely a wakeup trigger. The same directory watch picks up `opencode.db-shm` activity as a secondary signal.

2. **Per-table `MAX(time_updated)` polling.** Authoritative change detection. After every wakeup, the adapter runs:

```sql
SELECT MAX(time_updated) FROM session;
SELECT MAX(time_updated) FROM message;
SELECT MAX(time_updated) FROM part;
SELECT MAX(time_updated) FROM session_message;
```

If any value exceeds the cursor's recorded watermark, the adapter performs a delta read for that table. `time_updated` is auto-bumped on every UPDATE by the Drizzle ORM (`schema.sql.ts:7-9`), so even row-mutations (status changes, token-total updates) are visible.

**Poll cadence**:

- **Idle**: 2 s. Cheap (the four `MAX(time_updated)` queries together are <5 ms on the 3.9 GB DB because they are full-table scans without indexes on these columns; if this proves expensive we maintain `MAX(id)` per table instead, since `id` is lexicographically time-prefixed — see Performance).
- **Active** (any of the watermarks moved on the previous cycle): 500 ms.
- **fsnotify event on `opencode.db-wal`**: drop to 250 ms for the next 5 s, then back off.
- **Manual `/api/sources/<id>/reload`**: immediate.

Rationale for the floor at 250 ms (not lower): opencode writes can be very chatty (one transaction per part), and we are not a real-time replication layer — 250 ms is well below human perception for the UI's SSE updates.

Rationale for using `time_updated` over `id` (rowid) for the watermark: opencode rewrites in-place to update `tokens`, `cost`, `status` etc. on existing rows (via Drizzle `.$onUpdate`). An `id`-based cursor would miss these mutations. `time_updated` catches both inserts and updates.

## Cursor

Cursor shape, stored as opaque JSON in `sources.cursor`:

```json
{
  "version": 1,
  "schema_hash": "<sha256 of __drizzle_migrations.name list>",
  "tables": {
    "session":         { "max_time_updated": 1779793294883, "max_id": "ses_..." },
    "message":         { "max_time_updated": 1779793313106, "max_id": "msg_..." },
    "part":            { "max_time_updated": 1779793313250, "max_id": "prt_..." },
    "session_message": { "max_time_updated": 1779793313191, "max_id": "evt_..." }
  }
}
```

Two-watermark scheme:

- `max_time_updated` is the source of truth for delta queries.
- `max_id` is a secondary tiebreaker used only when multiple rows share the same `time_updated` (common — Drizzle stamps the same `Date.now()` across a single transaction). Without this tiebreaker the delta query can miss rows.

Delta query template (per table):

```sql
SELECT * FROM <t>
WHERE time_updated > :max_time_updated
   OR (time_updated = :max_time_updated AND id > :max_id)
ORDER BY time_updated, id
LIMIT 1000;
```

The 1000-row page LIMIT keeps each read transaction short. The adapter pages until the next page is empty.

`schema_hash` invalidates the cursor when opencode applies a new migration that affects shape we read; on mismatch the adapter logs a structured WARN, re-reads `__drizzle_migrations`, and continues without resetting the cursor (column drift is handled per-column; see Edge Cases). A full re-ingest is only triggered when a column we depend on disappears or its type changes incompatibly.

`SourceProgress` events are emitted every 1000 rows or every 5 s during steady state, whichever comes first.

## Mapping to Canonical Events

Opencode does not have an explicit "turn" or "op" concept on the wire. The adapter synthesizes them by walking message+part trees.

**Vocabulary mapping**:

| canonical | opencode |
|---|---|
| Session | `session` row (1:1) |
| Sub-agent session | `session` row with `parent_id != NULL` |
| Turn | one assistant `message` row + its preceding user message (1 turn = 1 user → 1 assistant cycle) |
| LLM Op | one `step-start` → `step-finish` pair inside an assistant message |
| Tool Op | one `tool` part (nested under the current step) |
| Reasoning Op | one `reasoning` part (nested under the current step) |
| Session Op (sub-agent attach) | `tool` part with `tool='task'` and `state.metadata.sessionId` set |

**Turn numbering**: opencode does not record a turn sequence number. The adapter assigns `seq=1..N` by ordering assistant messages within a session by `(time_created, id)`. The matching user message is paired by `data.parentID` if present (assistants always carry `parentID`), otherwise by the immediately preceding user message in time order.

### Per-table emit rules

When a new `session` row appears (delta on `session` table):

- Emit `SessionStartedEvent` with:
  - `NativeID = session.id`
  - `ParentNativeID = session.parent_id` (empty if NULL)
  - `Kind = "sub_agent"` if `parent_id` set, else `"root"`
  - `AgentName = session.agent`
  - `Model = json_extract(session.model, '$.id')` if non-NULL
  - `Extras = { providerID, variant, project_id, directory, version, slug, title }`
- `Ts = session.time_created * 1000` (convert ms→µs)
- `SourceSeq = deterministic per-event identifier` (stable across rescans; observability counter, not a dedup gate — see Idempotency)

When `session.time_updated` changes for a row already known:

- Emit `SessionUpdatedEvent` with the changed fields (agent/model/cost/tokens).

When `session.time_archived` becomes non-NULL:

- Emit `SessionFinalizedEvent` with `Status = "completed"`, `EndTs = time_archived * 1000`.

Opencode does not have a `status='failed'` row column. Failed sessions are inferred from the last assistant message carrying `data.error` (any `AssistantError` variant). When the adapter sees an assistant message with non-NULL `data.error`, the session is finalized with `Status = "failed"`, `ErrorClass = data.error.name`.

When a new `message` row appears (role=`assistant`):

- Emit `TurnStartedEvent` with:
  - `SessionNativeID = message.session_id`
  - `Seq = (count of prior assistant messages in same session) + 1`
- When `data.time.completed` is set (or when the message has at least one `step-finish` part), emit `TurnFinalizedEvent` with the message-level `cost`/`tokens` and `Status` derived from `data.finish` (`stop`→completed, anything else→completed unless `data.error` is set).

For each `part` row of the assistant message, walking in `id` order:

| part.type | canonical Op |
|---|---|
| `step-start` | open a new LLM Op (record state in adapter memory; emit `OpStartedEvent` with kind=`llm`, name=`<modelID>`, provider=`<providerID>` from the parent message) |
| `step-finish` | close the current LLM Op (emit `OpFinalizedEvent` with the step's `tokens`/`cost`; `Status="completed"`) |
| `reasoning` | emit `OpStartedEvent`+`OpFinalizedEvent` (kind=`reasoning`, ParentOpSeq=current LLM Op) using `data.time.start`/`data.time.end`; on missing `end`, `Status="running"` and end ts is null |
| `text` | NOT an op; surface as the assistant's final text. Skip canonical-event emission; the presenter retrieves text via a payload-style read. |
| `tool` | emit `OpStartedEvent`+`OpFinalizedEvent` (kind=`tool`, ParentOpSeq=current LLM Op, name=`tool`, ToolNamespace=derived from `tool` (e.g. `github_get_file_contents` → namespace `github`, name `get_file_contents`)) using `state.time.start`/`state.time.end`; `Status` derived from `state.status` |
| `tool` where `tool='task'` AND `state.metadata.sessionId` set | additionally emit `OpStartedEvent` of kind=`session` with `ChildSessionNativeID = state.metadata.sessionId`, alongside the tool Op (or instead of — TBD; current decision: emit both, with `session` op as the parent so the sub-agent attaches in the topology view) |
| `patch` | NOT an op; record in extras of the surrounding LLM op for the "Files changed" UI tab |
| `compaction` | emit `LogEntry` severity=`INF`, source=`opencode`, message=`session compacted (auto=<bool>)` |
| `retry` | emit `LogEntry` severity=`WRN`, message=`API retry attempt <attempt>: <error.name>` |
| `file` | recorded as a `PayloadRef` with `kind="user_attachment"`, `format="json"`, `LocationURI = data.url` |
| `subtask` | emit `OpStartedEvent` of kind=`session` with `Extras.prompt`/`description`/`agent`/`model`; finalize is implicit when the child session finalizes |
| `agent`, `snapshot` | recorded in extras; no op emission |

**Op `seq` numbering within a turn**: increment a counter per part processed, regardless of part type that gets emitted. `ParentOpSeq` for parts that fall inside a step is the LLM Op's seq (the `step-start`'s seq).

### Payload references

Opencode keeps payloads in the SQLite database itself (`part.data.state.output`, `part.data.text`, etc.). It does not write payload files to disk. The adapter emits `PayloadRefEvent` with a custom URI scheme:

```
LocationURI = "opencode-sqlite://opencode.db?part_id=<prt_...>&field=state.output"
```

The presenter resolves this scheme by re-querying SQLite for the named field. This keeps payloads out of ai-viewer's own database (they may be hundreds of MB total) and respects the read-only contract.

### Sub-Agent Linkage

Confirmed two parallel mechanisms (both observed on the operator's DB):

1. **Direct**: `session.parent_id` points to the host session (1285 rows). Authoritative.
2. **Via task tool**: a `part` with `data.tool='task'` and `data.state.metadata.sessionId` set names the child session. Cross-checked against (1): for all 1274 task parts that have a `state.metadata.sessionId`, the matching `session` row's `parent_id` equals the host session_id (1274 of 1274 match).

The remaining 11 (1285 − 1274) parent-set sessions have parents created by other mechanisms (the planned `subtask` part type that is not yet populated, or sessions created manually via opencode's `/api/session/fork`). The adapter relies on `session.parent_id` as the source of truth and uses task-tool `state.metadata.sessionId` only to wire the LLM Op → child Session Op edge inside a turn.

### Multi-provider awareness

Opencode is the most provider-diverse adapter we will ship. Observed providers on the operator's DB (`providerID`):

- `llm-netdata-cloud`, `zai-coding-plan`, `minimax-coding-plan`, `deepseek`, `kimi-for-coding`, `openrouter`, `alibaba-coding-plan`

These are opencode-configured aliases, not the canonical Anthropic/OpenAI/Google/etc. The adapter records both `Provider = providerID` (raw alias) and `Extras.providerCanonical` (best-effort mapping in `internal/canonical/providers.go`, defaulting to the alias unchanged when unknown). `catalog_models.provider` will therefore see opencode's provider strings rather than the canonical ones; a follow-up SOW may normalize.

### Tool calls and Models — concrete field map

Op | Source field |
|---|---|
| `op.kind=tool, name` | `part.data.tool` (raw; e.g. `read`, `bash`, `github_get_file_contents`) |
| `op.tool_namespace` | derived: prefix-up-to-first-underscore of `part.data.tool` when the tool name contains `_` (MCP convention); empty for builtins like `read`/`bash`/`grep` |
| `op.start_ts` | `part.data.state.time.start * 1000` (ms→µs) |
| `op.end_ts` | `part.data.state.time.end * 1000` |
| `op.status` | `part.data.state.status` → `completed`/`error`/`running`/`pending` |
| `op.error_message` | `part.data.state.error` (string, for error status) |
| `op.bytes_in` | `len(JSON.stringify(part.data.state.input))` (approximate) |
| `op.bytes_out` | `len(part.data.state.output)` for completed |
| `op.kind=llm, model` | parent `message.data.modelID` |
| `op.kind=llm, provider` | parent `message.data.providerID` |
| `op.tokens_in/out/cost` | from the step-finish part: `part.data.tokens.input`, `.output`, `.cost`. NOTE: step-finish tokens **appear cumulative across steps within one assistant message** based on observed data (a sequence of input tokens 17438, 23075, 31713, 35407, … all monotonically increasing). The adapter records the **delta** between successive step-finish values within the same message, not the raw value, so per-LLM-op tokens are correct. |
| `turn.tokens_in/out/cost` | from the assistant `message.data.tokens.input/output/cost` (those are the session totals at completion; we use the **delta from the previous assistant message's totals** for per-turn tokens — to be verified during implementation; this is also the rule the opencode UI uses) |
| `session.tokens_in/out/cost` | the rolled-up `session` columns (`tokens_input`, `tokens_output`, `cost`) when present; fall back to summing turns for sessions written before migration `20260510033149` |
| `ctx_max` | static pricing table per `(providerID, modelID)`; opencode does not store it |
| `ctx_used` | `tokens.input + tokens.cache.read` at the most recent step-finish for the turn |

## Edge Cases

1. **Schema drift across opencode versions.** Sessions span ~30 migrations. Older rows may lack `cost`, `tokens_input`, `tokens_output`, `tokens_reasoning`, `tokens_cache_read`, `tokens_cache_write` (added by `20260510033149`), `workspace_id` (added by `20260227213759`), `path` (added by `20260428004200`), `agent`, `model`, `time_compacting`, `time_archived`. Drizzle adds them with NOT-NULL DEFAULT 0 or NULL where appropriate; all rows in the operator's DB have the columns now, but the column **values** are zero on old rows. The adapter:
   - At startup, queries `PRAGMA table_info(session)`, `PRAGMA table_info(message)`, `PRAGMA table_info(part)`, `PRAGMA table_info(session_message)`.
   - Builds the SELECT list dynamically — naming only known columns; never `SELECT *`.
   - Tolerates missing columns by emitting empty/zero values in the canonical event and logging a structured INF on first occurrence per (table, column).
   - Tolerates unknown tables and unknown `session_message.type`/`part.data.type` by skipping with a structured WARN.

2. **Soft delete.** `session.time_archived` is set when a session is archived in the opencode UI (2 sessions on the operator's DB). The adapter treats archive as `SessionFinalizedEvent` with `Status="completed"`. The data is never physically deleted by opencode under normal operation; the FK ON DELETE CASCADE only fires if a project is deleted, which would cascade to sessions+messages+parts. The adapter should not delete its own canonical rows when an opencode row disappears (deletion is rare and we want history). A follow-up SOW will decide deletion semantics.

3. **Concurrent writer mid-poll.** WAL guarantees the reader sees a consistent snapshot from the moment its transaction began. New writes by opencode after BEGIN are invisible until the next cycle, which is exactly what we want for paged reads. The risk is: if our transaction lasts longer than ~1 s during steady-state writes, WAL grows unbounded because the checkpointer cannot reclaim space the reader still pins. Mitigation: each delta page is its own BEGIN/COMMIT, limit 1000 rows, expected completion in <50 ms.

4. **Pending and running tool/step states.** 27 pending and 144 running tool parts exist in the operator's DB (mostly from sessions that crashed or were interrupted). The adapter emits `OpStartedEvent` but never `OpFinalizedEvent` for these. Ingester records them with `status='running'`. If a later poll observes the same `part.id` now in completed/error state (the part row's `time_updated` will have moved), the adapter emits `OpFinalizedEvent` and the ingester reconciles.

5. **`step-start` without matching `step-finish`.** 530 messages on the operator's DB have unbalanced step pairs (117119 starts vs 116589 finishes = 530 orphans). Treat orphan step-start as a running LLM op (no finalize); when a new step-start appears in the same message, force-close the previous one with `Status="cancelled"` and synthetic end_ts = next step-start's start_ts.

6. **Time units.** All opencode timestamps are **milliseconds since epoch**. Canonical events use **microseconds**. The adapter multiplies by 1000 on every emission. Mixing units is the most likely class of bug; a unit test pins this with fixture rows that span boundary values.

7. **`event` / `event_sequence` tables empty.** They exist in the schema but are unused on the operator's DB. The adapter ignores them. If opencode starts populating `event` in a future version, the adapter logs an INF and continues; a follow-up SOW will integrate it (it may give us monotonic per-session sequence numbers we currently synthesize).

8. **Compaction reshapes data.** When opencode compacts a session, message and part rows can change (text/tool output get summarized, marker `compaction` parts get inserted). The adapter detects this via `time_updated` and `time_compacting`. Strategy: when `time_compacting` becomes non-NULL the adapter pauses delta reads for that session until `time_compacting` returns to NULL, then re-reads the whole session's messages and parts and emits `SessionUpdatedEvent`+re-emits ops with new content (the ingester absorbs the re-emission via SQL-layer idempotent upserts, not a `SourceSeq` gate). Compaction is rare (432 out of 127k messages = 0.3%).

9. **Cross-process WAL inheritance.** If opencode crashes mid-transaction, the WAL file may contain uncommitted pages. SQLite handles this transparently on the next open: any reader sees only committed pages. We rely on this — never call `wal_checkpoint`.

10. **Database may be `:memory:` or absent.** When opencode is not installed (or run with `OPENCODE_DB=:memory:`), the file path won't exist. The adapter logs an ERR `source not configured` once and disables itself; it does not retry forever.

11. **Sensitive content.** `session.title`, `session.directory`, `message.data.summary.title`, every `text`/`reasoning` part, every tool `state.input`/`state.output`, and every `patch.files` entry contain real operator data. ai-viewer never copies these into its own database except as references (via `payload_refs.location_uri`). The presenter fetches them on demand at request time and is the only component that materializes payload bytes.

## Canonical Model Gaps

1. **Multi-provider opencode vs single-provider canonical `Provider`.** Opencode's `providerID` is a user-defined alias (`llm-netdata-cloud`, `zai-coding-plan`, `kimi-for-coding`, etc.), not a canonical vendor. The canonical `Provider` field is documented (per `canonical-events.md:101`) as a vendor name like `anthropic`. The adapter passes the alias through; the canonical model needs either (a) a `provider_alias` field separate from `provider`, or (b) acceptance that `provider` is the source-recorded value and the UI must dereference. **Recommendation**: extend canonical event with `ProviderAlias` (optional) and reserve `Provider` for canonical vendor names; add a vendor-mapping table in `internal/canonical/providers.go`. Filed as a follow-up SOW question.

2. **Token classes opencode tracks that canonical does not.** opencode separates `tokens.reasoning`, `tokens.cache.read`, `tokens.cache.write`; canonical only has `tokens_in`/`tokens_out`. Cache reads are roughly equivalent to "input tokens that were free", which materially affects cost accounting (Anthropic and most others charge ~10% for cache reads vs full input price). **Recommendation**: extend `ops`/`turns`/`sessions` with `tokens_reasoning`, `tokens_cache_read`, `tokens_cache_write` columns to faithfully represent opencode (and Claude Code) cost. Filed as a follow-up SOW.

3. **Cumulative-vs-delta semantics.** opencode's step-finish tokens look cumulative within a message. Canonical Op `tokens_in`/`tokens_out` are per-op. The adapter computes deltas; this is correct but means we depend on observing **every** step-finish in order. A skipped step-finish (e.g. due to a missed poll cycle that was reconciled later) would corrupt deltas. Mitigation: when reconciling out-of-order step-finishes, recompute the entire message's deltas from scratch and emit `OpFinalizedEvent`s with the corrected values (the ingester upserts).

4. **`patch` parts have no canonical home.** They record "this turn changed these files" with a content-addressable snapshot hash that maps to a git-like object in `~/.local/share/opencode/snapshot/`. The canonical model has nowhere to attach a file-change list to an Op. **Recommendation**: keep in `ops.extras_json` for v1; revisit when the UI grows a "files changed" view.

5. **No explicit "session failed" signal.** The adapter infers failure from `message.data.error` on the last assistant message. There is no opencode-level "this session crashed at the OS level" record. Some sessions in the operator's DB simply trail off (last assistant message has `time.completed` NULL); these are recorded as `status='running'` indefinitely — opencode does not finalize sessions, only archives them. We accept this; a session is "running" until either (a) archived (mapped to completed) or (b) the last assistant message carries an error.

6. **`message.data.path.cwd` is per-turn, not per-session.** A user can `cd` mid-session (or opencode itself can). The session's `directory` column records start-of-session cwd. The canonical model has no per-turn cwd. **Recommendation**: store per-turn cwd in `turns.extras_json`; not a v1 blocker.

## Performance

Sample query timings on the operator's 3.9 GB DB, measured cold:

| query | scan | expected latency |
|---|---|---|
| `SELECT MAX(time_updated) FROM session` | full table scan, 6778 rows, 3 MB | ~3 ms |
| `SELECT MAX(time_updated) FROM message` | full scan, 127345 rows, 1.5 GB | ~80-200 ms (large because `time_updated` has no index and message rows are big) |
| `SELECT MAX(time_updated) FROM part` | full scan, 585894 rows, 2.3 GB | ~400-800 ms |
| `SELECT MAX(time_updated) FROM session_message` | full scan, 3985 rows, 760 KB | ~1 ms |
| `SELECT id, session_id, time_created, time_updated, data FROM message WHERE time_updated > ? ORDER BY time_updated, id LIMIT 1000` | index seek on `message_session_time_created_id_idx`? No — that index is on `(session_id, time_created, id)`, wrong order. Falls back to full scan when `time_updated > ?` has low selectivity. Mitigation: poll all four tables in parallel from one transaction, but **switch to `MAX(id)` watermark for `part` and `message`** to exploit the PRIMARY KEY index — `id` is lexicographically time-prefixed (Sonyflake), so `id > 'last_id'` is monotonic and uses the PK b-tree | <5 ms |

**Decision**: use `MAX(id)` as the primary watermark (PK-index-backed, ~µs latency) and `MAX(time_updated)` as a fallback for detecting in-place mutations. Combined query:

```sql
-- detect new inserts (fast, PK-indexed)
SELECT MAX(id) FROM part;
-- detect mutations to known rows (full scan, but only run when we suspect activity)
SELECT MAX(time_updated) FROM part;
```

The "suspect activity" gate: the second query runs only after the WAL file's mtime changes (fsnotify), or every 60 s as a safety net. Without this gate, polling cost on idle is ~1 s/poll; with it, idle polls cost <5 ms.

For `message` and `session_message`, `id` is also Sonyflake and lexicographically time-ordered, so the same trick applies. For `session`, the table is small enough that full scans are free.

**Backfill performance.** Initial ingest on the operator's DB will read 585k parts + 127k messages + 6.8k sessions = ~3.9 GB of data. At ~100 MB/s sequential read (SSD) + JSON decode CPU bound at ~50 MB/s in Go with `encoding/json`, expect 60-90 s wall time for a cold backfill. The adapter must:

- Page reads at 1000 rows per transaction.
- Emit `SourceProgress` every 1000 rows so a restart mid-backfill resumes.
- Use `goccy/go-json` or `bytedance/sonic` for faster JSON decode (decision deferred; baseline `encoding/json` is acceptable for v1).

**No indexes we can add.** The source DB is opencode's, not ours. We tolerate the indexes opencode has and design our cursor + paging around them. If future performance proves inadequate, we maintain a parallel index in ai-viewer's own SQLite (`part_native_index` keyed on `(opencode_session_id, part_id)`), populated as we ingest.

## References

Upstream evidence — `anomalyco/opencode @ 2b3ddf9f34546b9bcea25ec8e0ff57e2811c4537`:

- `packages/opencode/src/storage/db.ts:38-44` — `getPath` resolution (`OPENCODE_DB` env, channel suffix).
- `packages/opencode/src/storage/db.ts:99-127` — `Client` open: PRAGMAs, migration application.
- `packages/opencode/src/storage/schema.ts:1-6` — table-export aggregator.
- `packages/opencode/src/storage/schema.sql.ts:1-11` — `Timestamps` mixin (auto `time_updated`).
- `packages/opencode/src/session/session.sql.ts:16-59` — `SessionTable` definition.
- `packages/opencode/src/session/session.sql.ts:61-73` — `MessageTable` definition.
- `packages/opencode/src/session/session.sql.ts:75-91` — `PartTable` definition.
- `packages/opencode/src/session/session.sql.ts:93-110` — `TodoTable`.
- `packages/opencode/src/session/session.sql.ts:112-129` — `SessionMessageTable`.
- `packages/opencode/src/session/message-v2.ts:82-378` — `Part` discriminated union (12 variants) and shared `partBase`.
- `packages/opencode/src/session/message-v2.ts:248-308` — `ToolState` tagged union.
- `packages/opencode/src/session/message-v2.ts:327-350` — `User` message.
- `packages/opencode/src/session/message-v2.ts:452-490` — `Assistant` message.
- `packages/opencode/src/session/message-v2.ts:517-552` — `SyncEvent` aggregate definitions for message/part updates (used internally by opencode's API; observed feeding the `event` table when populated).
- `packages/core/src/session-message.ts:1-167` — `SessionMessage` union (`AgentSwitched`, `ModelSwitched`, `User`, `Synthetic`, `Shell`, `Assistant`, `Compaction`).
- `packages/opencode/migration/` — 20 migration directories; latest `20260511000411_data_migration_state`.
- `packages/opencode/migration/20260510033149_session_usage/migration.sql:1-6` — adds the `cost`/`tokens_*` columns to `session`.
- `packages/opencode/migration/20260323234822_events/migration.sql:1-12` — creates `event`/`event_sequence`.

Tables read by the adapter: `session`, `message`, `part`, `session_message`, `project`, `__drizzle_migrations`.

Tables present but ignored: `event`, `event_sequence` (empty), `permission`, `session_share`, `todo`, `workspace`, `account`, `account_state`, `control_account`, `data_migration`.

Local evidence database (the operator's workstation, never written to by ai-viewer): `~/.local/share/opencode/opencode.db`. The adapter MUST open it with `mode=ro&_pragma=query_only(true)`.
