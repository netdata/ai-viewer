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
| `directory` TEXT NOT NULL | working directory at session start (e.g. `/home/operator/src/PRs/example-project`) — sensitive |
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

Observed: 20 migrations applied (range `20260127222353_familiar_lady_ursula` … `20260511000411_data_migration_state`). opencode applies migrations from a journal of `{sql, timestamp, name}` entries ordered by the migration directory name, which embeds a `YYYYMMDDHHMMSS` timestamp prefix (anomalyco/opencode `packages/opencode/src/storage/db.ts`); Drizzle's standard `__drizzle_migrations` row carries an auto-increment `id` that increases in application order. The adapter reads the `name` column ordered by `id ASC` (application order) at scan/tail start.

That ordered name list serves two purposes (chunk D):

- **Schema hash.** `schema_hash` in the cursor (see Cursor) is `sha256(strings.Join(names, "\n"))` over the ordered names — a stable digest that changes only when opencode applies a new migration. It supersedes chunk C's interim present-column-shape fingerprint (which hashed the readable column shape as a placeholder before this table was read). The watermark semantics are unchanged: a hash mismatch logs a structured WARN, re-reads, and continues WITHOUT resetting watermarks (column drift is handled per-column by the dynamic SELECT; a depended-on column vanishing is the only re-ingest trigger).
- **Latest migration / counts (AC#8).** `latest_migration` is the name with the highest `id` (last applied). The auto-discovery probe (`ProbeStatus`) reads it alongside `COUNT(*)` of `session`/`message`/`part` so `/api/health` and the discovery log surface what the source will yield. A missing `__drizzle_migrations` table (a very old or foreign SQLite file) is non-fatal: the probe returns empty names + a soft sentinel, the schema hash is left empty, and no migration is reported — the adapter degrades rather than crashing.

`ProbeStatus` opens the database read-only via the same `openReadOnly` helper. The three `COUNT(*)` queries are full counts; on a multi-GB database that costs a few hundred ms ONCE at startup, which is acceptable for a one-time discovery probe (the steady-state tailer never runs them). A table that does not exist makes its count 0 and is noted as a soft error rather than failing the probe, so a foreign SQLite file the probe stumbles on degrades gracefully.

## Read Strategy

The defining constraint: opencode's writer holds the database open and may commit transactions at any time. ai-viewer is a strict read-only consumer. The adapter MUST:

1. Open with `mode=ro&_txlock=deferred` plus the fixed read-only PRAGMA set below.
2. Use `modernc.org/sqlite` (CGO-free, per AGENTS.md tech stack).
3. Open a **fresh connection per poll cycle** or keep a pool with `SetMaxOpenConns(1)` for the read path. Opening read-only against a WAL-mode database is non-blocking for the writer — multiple readers and a single writer can proceed concurrently (SQLite WAL guarantee). Concrete DSN (the one `buildReadOnlyDSN` rebuilds, `conn.go`):

```
file:%2Fhome%2Foperator%2F.local%2Fshare%2Fopencode%2Fopencode.db?mode=ro&_txlock=deferred&_pragma=query_only(true)&_pragma=busy_timeout(5000)
```

Key choices:

- `mode=ro` — refuse any write at the OS level. The OS opens the file `O_RDONLY`; SQLite cannot upgrade the connection.
- `_pragma=query_only(true)` — defense-in-depth; rejects any UPDATE/INSERT/DELETE at the SQL layer.
- `_pragma=busy_timeout(5000)` — wait up to 5 s when an exclusive lock is held (which should be rare in WAL mode but happens during `PRAGMA wal_checkpoint(TRUNCATE)`).
- `_txlock=deferred` — defer BEGIN until first statement; for reads this just means "snapshot taken on first SELECT".

**No `journal_mode` in the DSN (SOW-0005 round-2 P3-B).** `conn.go`'s `readOnlyPragmas` allowlist deliberately OMITS `journal_mode(WAL)`: the journal mode is a WRITER concern recorded in the database header by whoever created/opened it read-write (opencode), and a read-only connection inherits the database's existing mode — it cannot (and must not try to) change it. Setting `journal_mode` on a `mode=ro` connection is a no-op at best and an attempted write at worst; either way it earns nothing, so the reader simply does not send it. The reader still gets WAL-reader snapshot semantics automatically because the database file is already in WAL mode. Likewise `foreign_keys(off)` is not sent — FK enforcement only matters for writes, and the reader issues none.

**DSN is an ALLOWLIST, not a denylist (SOW-0005 P1.2).** `buildReadOnlyDSN` parses the caller-supplied query string only to VALIDATE it, then DISCARDS it and rebuilds the query from scratch with exactly: `mode=ro`, `_txlock=deferred`, and the fixed `readOnlyPragmas` set (`query_only(true)`, `busy_timeout(5000)`). Therefore NO caller-supplied `_pragma` survives — neither one that name-collides with the read-only set NOR a non-colliding write-path pragma (`wal_checkpoint(TRUNCATE)`, `optimize`, `foreign_keys(on)`, …) — and a caller `_txlock=exclusive` is replaced with `deferred`. A maliciously-constructed path string therefore cannot reach a write-path pragma or an exclusive (write-lock) BEGIN. The earlier denylist that stripped only colliding `_pragma` names is replaced by this allowlist.

**Never** call `PRAGMA wal_checkpoint`, `PRAGMA optimize`, `VACUUM`, `BEGIN EXCLUSIVE`, or `ATTACH … AS … 'rwc'`. The connection MUST remain a pure reader.

Connection pool settings:

- `SetMaxOpenConns(2)` — one for the watch poll, one for the (rare) presenter-triggered re-read.
- `SetConnMaxLifetime(30 * time.Minute)` — recycle to release any stale WAL pages from cache.
- `SetMaxIdleConns(1)`.

Read transactions: every query batch wraps in `BEGIN DEFERRED ... COMMIT` so the adapter sees a consistent snapshot across multi-statement cursors. Long transactions on a WAL DB pin the WAL and stop checkpointing, so each cycle keeps its transaction shorter than 1 s of wall time. If a backfill must read more than a few thousand rows, it does so in **paged transactions** (close the transaction every N rows, then start a new one), accepting that the snapshot advances between pages.

### Delta query, affected-session derivation, and tree load (Chunk C)

The delta-query layer is the bridge between the watermark cursor and the pure mapper. It runs three steps per change cycle, each in its own short read transaction:

1. **Paged delta query per tracked table.** Each `session`/`message`/`part`/`session_message` table is paged from its `TableWatermark` with the composite-key SELECT (`buildSelect`, naming only live columns — never `SELECT *`):

   ```sql
   SELECT <present cols> FROM <table>
   WHERE time_updated > :u OR (time_updated = :u AND id > :id)
   ORDER BY time_updated, id LIMIT 1000
   ```

   Each page runs inside a `BEGIN DEFERRED` read transaction opened via `database/sql`'s `BeginTx{ReadOnly:true}` and committed promptly, keeping the WAL unpinned. Paging continues until a short page (`< 1000` rows) returns. The new max `(time_updated, id)` seen across all pages becomes the table's advanced watermark.

   **No id-only fallback (SOW-0005 P3.1).** `time_updated` is a REQUIRED column for every tracked table (`requiredColumns`); `introspectAll` fails fast when it is absent, so a table that reaches a delta query ALWAYS has `time_updated`. The composite-key SELECT above is therefore the ONLY delta query. The earlier pre-`Timestamps`-mixin id-only fallback (`buildSelectByID`) was unreachable dead code — `introspectAll`'s required-column gate makes a `time_updated`-less table fatal upstream, never a delta-query input — and it was removed along with its introspection-bypassing isolation test.

2. **Affected-session derivation.** From the changed rows, the layer computes the SET of session ids whose full tree must be reloaded and re-mapped:
   - a changed `session` row contributes its own `id`;
   - a changed `message` row contributes its `session_id`;
   - a changed `part` row contributes its `session_id` (the `part` table denormalizes `session_id`); on a hypothetical old schema where `part` lacks `session_id`, the owning session is resolved via an indexed `SELECT session_id FROM message WHERE id = :message_id` lookup (with the changed-message delta consulted first to avoid the query);
   - a changed `session_message` row contributes its `session_id`.

   The set is de-duplicated: a session touched by several tables in one cycle is reloaded exactly once.

3. **Full-session-tree load + map.** For each affected session id the layer loads the whole tree — the `session` row, all its `message` rows ordered by `(time_created, id)`, and each message's `part` rows ordered by `(id)`, all under ONE bounded read transaction (`loadSessionTree`) — assembles `[]messageWithParts`, and calls the pure mapper (`mapSession`) on it. Full-tree reload is mandatory, not partial: the mapper computes per-turn cumulative-token deltas across the ordered message list, so a partial reload would miscompute deltas. Re-emitting an unchanged session is harmless — the ingester's idempotent upserts + the (post-SOW-0004) idempotent catalog absorb it.

   **True tree root (SOW-0005 P2.4).** Before mapping, the layer resolves the session's TRUE tree root by walking its `parent_id` chain to the topmost ancestor (`resolveRootID`: an indexed `SELECT parent_id FROM session WHERE id=?` walked up, read-only, depth-capped at 32 with a seen-set cycle guard) and injects it into the mapper (`WithRootNativeID`). A nested sub-agent's `RootNativeID` is therefore the whole tree's root, not its direct parent; `ParentNativeID` still points at the direct parent. If the chain cannot be fully resolved (a missing ancestor row, a cycle, or the depth cap), it falls back to the furthest resolvable ancestor (the direct parent on a one-step failure) and surfaces one WARN via `onError`.

   A session id observed in a delta whose `session` row cannot be loaded (deleted between pages, or a part/message orphaned from its session) is skipped with one structured error via `onError`; the cycle continues with the remaining sessions.

**Checkpoint-after-emit invariant (SOW-0005 P1.1, data-loss fix).** A `SourceProgress` checkpoint carrying cursor `W` is emitted ONLY after every session affected by rows ≤ `W` in this run has been reloaded, mapped, and emitted. The pipeline (`processChanges` → `batchProcessor`) runs in BOUNDED BATCHES: each batch pages ≤ `progressEveryRows` delta rows forward ACROSS the tracked tables (one shared row budget, so a session touched by several tables is reloaded once per batch — cross-table dedupe), `reloadAndEmit`s that batch's affected sessions, and ONLY THEN advances the persisted cursor + checkpoints. On ctx-cancel/error mid-batch the LAST fully-committed cursor (the previous batch's) is returned, never the in-progress batch's scanned watermark — so a restart from the persisted cursor can never resume PAST rows whose canonical events were never emitted. The earlier scheme that emitted a `SourceProgress` every `progressEveryRows` rows DURING paging (before the affected sessions were emitted) advanced the watermark ahead of content and is replaced.

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

### Poll-loop state machine and the `MAX(time_updated)` gate (Chunk C)

The realtime tailer is a timer-driven poll loop with an fsnotify wakeup hint. It is implemented as two free functions Chunk D's `adapter.go` calls (mirroring codex's free-function tailer rather than methods on the `Adapter` struct):

- `scanLoop(ctx, dbPath, sourceID, since, out, logger, onError) (Cursor, error)` — the historical backfill: introspect once, emit one INFO per missing optional column (see Edge Cases #1), record the schema hash into the cursor, page every tracked table from `since`, derive affected sessions, reload + map each, emit, checkpoint `SourceProgress` every ~1000 rows processed and once at the end, return the advanced cursor.
- `tailLoop(ctx, dbPath, sourceID, cur, out, logger, onError) error` — the realtime follow until `ctx` is cancelled (returns `nil` on cancel); also emits the missing-optional-column INFO set once at its introspection.

The `logger` parameter is `*slog.Logger`, threaded from `Adapter.logger` (non-nil after `New`, which defaults to `slog.Default()`); both loops guard a nil logger defensively (`slog.Default()`) so a direct test caller passing nil does not panic.

**Adapter Scan→Tail cursor hand-off (Chunk D).** `adapter.go` mirrors codex: `Scan` records the final advanced cursor on the `Adapter` instance (`scanCursor`) even on `ctx` cancellation, and a following `Tail` on the SAME instance resumes from it instead of snapshotting current HEAD — closing the data-loss window where rows committed between `Scan` finishing and `Tail` starting would otherwise be skipped. Any re-emission of an already-seen session tree is absorbed by the ingester's idempotent upserts. A **cold `Tail`** (no preceding `Scan`, e.g. a resumed daemon whose `Scan` ran in a previous process) builds a HEAD-snapshot cursor: open read-only, introspect, and set each tracked table's watermark to its current `MAX(id)` + `MAX(time_updated)` (via `maxID`/`maxTimeUpdated`). This is the SQLite analogue of codex stat'ing current file sizes — `Tail` then follows from NOW rather than replaying full history. The HEAD snapshot also records the real `__drizzle_migrations` schema hash into the cursor. A missing/unreadable database during the snapshot surfaces one structured error and `Tail` returns cleanly (the daemon keeps serving other sources).

**Cadence intervals** (decided, SOW-0005 Open Decision #2):

- **Idle** poll interval: 2 s (the previous cycle produced no change).
- **Active** poll interval: 500 ms (the previous cycle produced a change).
- **WAL-event floor**: 250 ms for a 5 s window after an `opencode.db-wal` fsnotify Write/Chmod event.

The next interval is the minimum of the active/idle interval and the WAL-event floor when the floor window is open.

**Cheap primary change check.** Every poll first runs the PK-indexed `MAX(id)` per table (a b-tree lookup on the time-prefixed Sonyflake PK, ~µs). When any table's `MAX(id)` exceeds the cursor's `MaxID`, the cycle runs the delta+reload+emit path. On a current schema this catches every INSERT.

**The gated `MAX(time_updated)` probe.** In-place row mutations (token totals, status, archive) do NOT change `MAX(id)`, so they are caught by the unindexed `MAX(time_updated)` probe — which on the 585k-row `part` table is a 400–800 ms full scan and therefore MUST NOT run on every idle poll. It runs only when the gate is open. The gate predicate is a pure function:

```
shouldProbeTimeUpdated(now, lastWALEvent, lastProbe, safetyNet) =
    lastWALEvent.After(lastProbe)  ||  now.Sub(lastProbe) >= safetyNet
```

i.e. the probe is issued only when (a) a WAL-mtime fsnotify event has fired since the last probe, OR (b) the 60 s safety-net interval has elapsed since the last probe. `safetyNet` is **60 s** (decided, matching Performance §"suspect activity gate" and AC#6). During steady-state idle with no WAL events the predicate is false on every poll, so the expensive scan never runs — the property AC#6 pins.

**WAL fsnotify hint.** The loop sets up an fsnotify watch on the `opencode.db-wal` companion path (`<dbPath>-wal`) as a wakeup hint only. A Write/Chmod event records `lastWALEvent = now` (opening the 250 ms floor window and the probe gate). The hint is best-effort: if the WAL file does not exist, the watch `Add` fails, or the watcher errors, that is **non-fatal** — the loop logs once via `onError` and falls back to pure timer polling (the 60 s safety net still guarantees in-place mutations are eventually seen). A watcher error never terminates the loop.

**Manual reload** (`/api/sources/<id>/reload`) is out of scope for Chunk C (the route does not exist yet); when added it will force one immediate cycle with the probe gate open.

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

`SourceProgress` events are emitted per BATCH, AFTER that batch's affected sessions are emitted (the checkpoint-after-emit invariant — see Read Strategy §"Full-session-tree load + map"). A batch's row budget is `progressEveryRows` (1000) across the tracked tables, so the persisted cursor advances at most one batch ahead of fully-emitted content, never past un-emitted content. The earlier "every 1000 rows or every 5 s" mid-paging cadence is superseded.

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
- Emit `TurnFinalizedEvent` ONLY when the turn is TERMINAL (`turnIsTerminal`): `data.time.completed` is set, OR `data.error` is present, OR the message has at least one `step-finish` part. Otherwise the turn is **RUNNING** — `TurnStartedEvent` with NO `TurnFinalizedEvent` (opencode writes the assistant `message` row LIVE while the turn is still in progress; finalizing every live row would wrongly mark an in-flight turn completed). A later poll re-emits the whole tree and finalizes the turn once it actually completes; the re-emit is idempotent. When terminal, `TurnFinalizedEvent` carries the message-level `cost`/per-turn token delta and `Status` derived from `data.finish` (`stop`→completed, anything else→completed unless `data.error` is set → failed).

For each `part` row of the assistant message, walking in `id` order:

| part.type | canonical Op |
|---|---|
| `step-start` | open a new LLM Op (record state in adapter memory; emit `OpStartedEvent` with kind=`llm`, name=`<modelID>`, provider=`<providerID>` from the parent message) |
| `step-finish` | close the current LLM Op (emit `OpFinalizedEvent` with the step's `tokens`/`cost`; `Status="completed"`) |
| `reasoning` | emit `OpStartedEvent`+`OpFinalizedEvent` (kind=`reasoning`, ParentOpSeq=current LLM Op) using `data.time.start`/`data.time.end`; on missing `end`, `Status="running"` and end ts is null. **ReasoningKind** (canonical-events.md:202 — `summary` \| `raw`): opencode reasoning parts carry no native summary-vs-raw discriminator, so the adapter emits `raw` (the part is the model's raw chain-of-thought text), unless `data.metadata.summary` is truthy, in which case it emits `summary`. The reasoning body (`data.text`) is referenced as a PayloadRef (kind `llm_reasoning`, field `text`), never inlined. |
| `text` | NOT an op; surface as the assistant's final text. The adapter does NOT emit an op for a `text` part, but DOES emit a `PayloadRef` (kind `llm_response`, field `text`) scoped to the turn's most-recent LLM op so the presenter can retrieve the assistant's text on demand without ai-viewer copying it. When no LLM op is open yet (a `text` part before any `step-start`), the ref is dropped (it has no op to attach to; `payload_refs.op_id` is NOT NULL). |
| `tool` | emit `OpStartedEvent`+`OpFinalizedEvent` (kind=`tool`, ParentOpSeq=current LLM Op, name=`tool`, ToolNamespace=derived from `tool` (e.g. `github_get_file_contents` → namespace `github`, name `get_file_contents`)) using `state.time.start`/`state.time.end`; `Status` derived from `state.status` |
| `tool` where `tool='task'` AND `state.metadata.sessionId` set | emit BOTH the tool Op AND an `OpStartedEvent` of kind=`session` with `ChildSessionNativeID = state.metadata.sessionId` (SOW-0005 decision: emit both; the `session` op is the topology parent so the sub-agent attaches in the topology view) |
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

**Mapper/URI seam (SOW-0005 chunk split).** The row→event mapper (chunk B) is pure and DB-agnostic: it knows the owning `part.id` and the `field` path (`state.output`, `state.input`, `text`, …) but NOT how to build the final `opencode-sqlite://` URI. The canonical URI grammar lives in ONE place — `payloads.go`'s `buildPayloadURI(partID, field)` (chunk D) — mirroring how codex/claude_code keep URI construction in their `payloads.go`. The grammar is:

- scheme `opencode-sqlite` (no host, no path);
- query params `part_id=<id>&field=<field>`, with both values URL-encoded via `net/url` so a part id or field path containing a reserved character is safe;
- producing exactly `opencode-sqlite://?part_id=<id>&field=<field>`.

The mapper's built-in default (`defaultPayloadURI`, used in mapper-only unit tests) delegates to `buildPayloadURI`, so there is a single source of truth and the relative form is byte-identical to chunk B's contract. The future `/api/payloads` resolver (a separate Phase-2 SOW, NOT this chunk) will look up the owning source's database path from the `payload_ref`'s `source_id` and `SELECT part.<field>` for that `part_id` read-only; chunk D builds NO resolver/parser (there is no consumer yet — that would be dead code). This mirrors codex, whose mapper defers `file://` construction to a `payloadURI` helper. The PayloadRef field map per part type:

| part type | PayloadKind | field |
|---|---|---|
| `text` | `llm_response` | `text` |
| `reasoning` | `llm_reasoning` | `text` |
| `tool` (completed/error) | `tool_response` | `state.output` |
| `file` | `user_attachment` | (verbatim `data.url`, not a SQLite field) |

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
| `turn.tokens_in/out/cost` | from the assistant `message.data.tokens.input/output/cost`. SOW-0005 decision: per-turn tokens are the **delta from the previous assistant message's cumulative totals** within the session (matching the opencode UI). The implementer MUST confirm the cumulative pattern on the live DB before pinning the golden — the step-finish cumulative pattern (row above / AC#3) is verified; this message-level pattern is the analogous one level up and is not yet independently confirmed. |
| `session.tokens_in/out/cost` | the rolled-up `session` columns (`tokens_input`, `tokens_output`, `cost`) when present; fall back to summing turns for sessions written before migration `20260510033149` |
| `ctx_max` | static pricing table per `(providerID, modelID)`; opencode does not store it |
| `ctx_used` | `tokens.input + tokens.cache.read` at the most recent step-finish for the turn |

## Edge Cases

1. **Schema drift across opencode versions.** Sessions span ~30 migrations. Older rows may lack `cost`, `tokens_input`, `tokens_output`, `tokens_reasoning`, `tokens_cache_read`, `tokens_cache_write` (added by `20260510033149`), `workspace_id` (added by `20260227213759`), `path` (added by `20260428004200`), `agent`, `model`, `time_compacting`, `time_archived`. Drizzle adds them with NOT-NULL DEFAULT 0 or NULL where appropriate; all rows in the operator's DB have the columns now, but the column **values** are zero on old rows. The adapter:
   - At startup, queries `PRAGMA table_info(session)`, `PRAGMA table_info(message)`, `PRAGMA table_info(part)`, `PRAGMA table_info(session_message)`.
   - Builds the SELECT list dynamically — naming only known columns; never `SELECT *`.
   - Tolerates missing columns by emitting empty/zero values in the canonical event and emitting one structured INFO log per wanted-but-absent OPTIONAL column, at introspection time. The log carries a stable message (`opencode: optional column absent on this database schema; omitted from projection (old opencode version)`) plus structured keys `table` and `column`, emitted in a deterministic order (tables in `trackedTables` order, columns sorted). Required-column loss is fatal upstream (`introspectAll`), so every column that reaches the INFO path is an optional one the dynamic SELECT silently omitted. `Scan` and `Tail` EACH emit this set once on (re)start — they each introspect once, so on the rare old-schema path the missing-column set appears twice per source lifetime; that per-phase duplication is accepted (it is not deduplicated across phases). Production wiring: `tailer.go` `logMissingColumns` is called right after `introspectAll` succeeds in both `scanLoop` and `tailLoop`; the logger is threaded from `Adapter.logger` (`adapter.go` `Scan`/`Tail`).
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

## Testing / Golden Fixtures (Chunk E)

The adapter's row→event behaviour is pinned by a committed golden suite plus
direct per-scenario invariant assertions and a `data`-JSON fuzz target. This is
the SQLite analogue of the codex golden harness (`codex/golden_test.go`).

**Fixture format — `fixture.sql`, never a binary `.db`.** The repo commits ZERO
binary database fixtures (opaque to diffs, can't be secret-scanned). Each scenario
under `testdata/opencode/<scenario>/` ships a human-reviewable `fixture.sql`
(`CREATE TABLE` + `INSERT`s, the faithful real schema for normal scenarios; the
reduced pre-`20260510033149` schema for the drift scenario). At run time the
golden harness (`buildFixtureDB`) builds a throwaway SQLite database in
`t.TempDir()` from `fixture.sql` via a SEPARATE read-write `database/sql`
connection (production NEVER opens opencode.db read-write — this is the harness
constructing the fixture), closes the writer, and the adapter under test reopens
the path strictly read-only through `New`/`openReadOnly`. All fixture content is
synthetic and invented (ids like `ses_happy01`/`prt_a01`, providers
`anthropic`/`openai`, models `claude-x`/`gpt-y`); the operator's real database is
never read or copied (R5).

**Golden encoding.** `golden_test.go` auto-discovers every `testdata/opencode/*/`
directory, scans the built DB, filters out `SourceProgressEvent` (a checkpoint,
not content), and serialises the remaining events one `{kind,payload}` JSONL line
per event into `expected.jsonl`. The only absolute path embedded in a
non-SourceProgress event is the `SourceID` (`opencode:<dbPath>`), rewritten to
`opencode:<ROOT>` for portability and PII hygiene. The `opencode-sqlite://?part_id=&field=`
PayloadRef URIs are DB-relative (no path, no basename) and need no substitution.
Run `go test ./internal/adapters/opencode/ -update-golden` to regenerate; the
generator is deterministic (every non-SourceProgress `Ts` derives from a fixture
row timestamp ×1000 — no wall-clock leaks), so regeneration is byte-idempotent.

**Goldens are not self-justifying.** A `-update-golden` run pins whatever the code
emitted, regressions included. `golden_invariants_test.go` therefore re-scans each
fixture and asserts the load-bearing invariant keyed on canonical-event FIELDS
(not golden text), so a regression fails there even after a golden refresh.

The five scenarios and what each pins:

| scenario | pins |
|---|---|
| `a_happy` | baseline `session→turn→op` tree: root SessionStarted → TurnStarted → LLM op (step-start) → reasoning op (+ `llm_reasoning` PayloadRef) → `llm_response` PayloadRef (text) → tool op (+ `tool_response` PayloadRef) → LLM op_finalized (step-finish) → TurnFinalized; NO SessionFinalized (running). PayloadRef URIs + ms→µs. |
| `b_subagent_task` | sub-agent linkage BOTH ways (AC#4): the child `session.parent_id` row maps to `Kind=sub_agent`+`ParentNativeID`/`RootNativeID`=parent; the parent's `tool='task'` part (with `state.metadata.sessionId`) emits BOTH a session Op (`Kind=session`, `ChildSessionNativeID`, the topology parent, emitted first) AND a tool Op (`Kind=tool`, `name=task`) in the same turn. |
| `c_multi_provider` | multi-provider (AC#7): two turns with `providerID` anthropic then openai → each LLM op carries its `ProviderAlias` verbatim + canonical `Provider` (two catalog providers downstream). Also the two-level token model: per-op tokens reset per message (turn2 op = 300/80) while per-turn tokens are the session-level delta (turn2 turn = 200/50). |
| `d_schema_drift` | graceful degrade on the pre-`20260510033149` schema (AC#5): `introspectAll` ACCEPTS it (required cols present), the dynamic SELECT omits the 9 missing optional `session` columns, SessionStarted carries empty `Model`/`AgentName` and Extras WITHOUT `providerID`/`variant`, while op/turn token+provider values survive (they come from `message.data`, untouched by the column drift). |
| `e_cumulative_tokens` | cumulative→delta token math (AC#3): four step-finish parts with CUMULATIVE inputs 100/250/410/400 (outputs 20/50/90/80) → per-LLM-op deltas 100/150/160/0 and 20/30/40/0 (the 4th clamps to 0 because the cumulative decreased). The per-turn rollup is the message-level cumulative (400/80). |
| `g_nested_subagent` | nested-root resolution (SOW-0005 P2.4): a 3-level tree root→child→grandchild. The grandchild's `RootNativeID` is the TRUE tree root (`ses_groot`), NOT its direct parent (`ses_gchild`), while its `ParentNativeID` is the direct parent — proving `resolveRootID` walks the chain to the top. Each session's turn finalizes (completed ts present, P1.3). |

**Resume property (AC#6, scenario-level).** Complementary to chunk C's
two-stage-insert `TestScanLoop_ResumeZeroDupesZeroGaps`, `golden_resume_test.go`
pins the durability properties expressible over a STATIC fixture: (a) a re-scan
from the final cursor (persisted+reparsed) emits ZERO content events (no duplicate
on restart), (b) two cold scans from the zero cursor emit the identical content
multiset (no nondeterministic drop/duplicate), and (c) on the two-session
`b_subagent_task` fixture a re-scan from the final cursor re-emits neither session
(the watermark advances past every session touched in one cycle). Together:
resume/re-scan never drops or duplicates a content event.

**`data`-JSON fuzz.** `data_fuzz_test.go` fuzzes `decodeMessageData` (the
message.data user|assistant union) and `decodePartData` (the part.data 12-variant
`$.type` union) — the untrusted-bytes boundary where a malformed/truncated blob
from the live database meets the adapter (opencode's analogue of codex's
`FuzzParseLine`). Contract: the decoder NEVER panics on any input — it returns a
struct or a wrapped error; the typed helpers reachable from a decoded value
(`role`/`kind`/`subAgentSessionID`/`modelID`/`reasoningKind`) must also not panic.
Seeds cover both message roles, all 12 part variants (incl. the `tool='task'`
metadata.sessionId edge), unknown `$.type`/role, and malformed/truncated/empty/
deeply-nested bodies.

**AC#5 INF logging (wired).** The "one INFO log per missing optional column"
promise (Edge Cases #1) is implemented: `tailer.go` `logMissingColumns` iterates
each table's `tableSchema.Missing` right after `introspectAll` succeeds in BOTH
`scanLoop` and `tailLoop`, emitting one `logger.Info(...)` per (table, column)
with the stable message + `table`/`column` keys. `TestGoldenInvariant_DSchemaDrift_MissingColumnsLoggedINF`
(`golden_invariants_test.go`) proves it: it `Scan`s the `d_schema_drift` fixture
through the public adapter with a record-capturing `slog.Handler`
(`golden_loghandler_test.go` `captureHandler`) and asserts the set of logged
(table, column) pairs equals the set introspection reports Missing — exactly one
INFO record per missing column, nothing extra. The `d_schema_drift` golden still
pins the graceful DEGRADE (accept + omit columns + zero values); the INF set is
not serialised into `expected.jsonl` (it is a log, not a canonical event).

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
