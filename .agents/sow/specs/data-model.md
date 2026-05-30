# Canonical Data Model (SQLite Schema)

## TL;DR

A normalized, format-agnostic, **span-shaped** model. Sessions contain Turns; Turns contain Ops; Ops are the universal span (LLM call, tool call, child session, reasoning block, compaction event). Payloads are referenced by URI, never inlined. Adapters write rows via the canonical event pipeline; the presenter reads them. The schema is the contract between the two halves.

The schema is **deliberately wider than any single source format** so it cleanly absorbs all five adapters (ai-agent v2/v3, claude-code, codex, opencode) and any future format. Columns unused by one adapter are NULL; format-specific extras live in compact JSON `extras_json` fields.

## Design Principles

- **Span-shaped.** Every operation is an `op` row with start/end timestamps, parent/child links, tokens, cost, status. Maps 1:1 to APM/OTel concepts.
- **Format-agnostic.** No `aiagent_only` column, no `claude_code_only` column. Format-specific extras go into `extras_json`.
- **Payloads stay on disk.** SQLite stores `payload_refs` pointers; original gz/json files are the source of truth.
- **Catalog tables for fast aggregation.** Denormalized rollups (`catalog_tools`, `catalog_models`, `catalog_agents`, `catalog_providers`) are updated by the ingester after each batch commit.
- **Cursors live in SQLite.** Each `source` row records its last-ingested position so the ingester resumes after restart.
- **Wider, not deeper.** Cache tokens, reasoning kind, provider alias, cwd, call_path — these are first-class columns because at least two adapters surface them and the cost-analysis / filtering paths need them. They're not extras_json bloat.

## Schema (v1)

All timestamps are `INTEGER` UNIX-microseconds (UTC). All IDs are `TEXT` UUID-v4 or stable hashes derived from source IDs.

### sources

Each row is one configured source the ingester watches.

```sql
CREATE TABLE sources (
    id              TEXT PRIMARY KEY NOT NULL,  -- e.g. "aiagent-v3:/home/user/.ai-agent/sessions"
    format          TEXT NOT NULL,              -- 'aiagent_v3'|'aiagent_v2'|'claude_code'|'codex'|'opencode'
    location        TEXT NOT NULL,              -- filesystem path or DSN
    cursor          TEXT,                       -- opaque per-adapter cursor (JSON)
    last_seen_at    INTEGER,
    enabled         INTEGER NOT NULL DEFAULT 1,
    parse_errors    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);
```

`NOT NULL` is explicit on every `TEXT PRIMARY KEY` column because SQLite's
default rowid tables allow NULL in TEXT PK columns (only `INTEGER PRIMARY
KEY` — the rowid alias — is implicitly NOT NULL). Without the marker a
malformed ingest insert could land a NULL id and corrupt cross-table
references.

### sessions

```sql
CREATE TABLE sessions (
    id                TEXT PRIMARY KEY NOT NULL, -- canonical session id (hash of source_id + native_id)
    source_id         TEXT NOT NULL REFERENCES sources(id),
    native_id         TEXT NOT NULL,            -- originId/sessionId/uuid from the source format
    parent_session_id TEXT REFERENCES sessions(id),
    root_session_id   TEXT NOT NULL REFERENCES sessions(id),
    kind              TEXT NOT NULL,            -- 'root' | 'sub_agent' | 'tool_internal' | 'fork'
    agent_name        TEXT,                     -- if known
    model             TEXT,                     -- primary model used in this session (last-known)
    provider          TEXT,                     -- 'anthropic'|'openai'|'google'|'openrouter'|...
    provider_alias    TEXT,                     -- user-defined provider alias (opencode); NULL otherwise
    cwd               TEXT,                     -- working directory at session start (claude-code, codex, opencode)
    call_path         TEXT,                     -- durable agent-chain string (ai-agent v3 callPath); NULL otherwise
    status            TEXT NOT NULL,            -- 'running' | 'completed' | 'failed' | 'abandoned' | 'interrupted'
    error_class       TEXT,                     -- present when status='failed'
    error_message     TEXT,
    start_ts          INTEGER NOT NULL,
    end_ts            INTEGER,
    last_activity_ts  INTEGER NOT NULL,         -- updated on every event for this session; powers "stale running" filter
    tokens_in         INTEGER NOT NULL DEFAULT 0,
    tokens_out        INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL NOT NULL DEFAULT 0.0,
    turn_count        INTEGER NOT NULL DEFAULT 0,
    op_count          INTEGER NOT NULL DEFAULT 0,
    failure_count     INTEGER NOT NULL DEFAULT 0,
    extras_json       TEXT,                     -- format-specific extras (finalReport, pluginMetas, latestStatus, etc.)
    UNIQUE (source_id, native_id)
);

CREATE INDEX idx_sessions_root_start ON sessions(root_session_id, start_ts);
CREATE INDEX idx_sessions_start ON sessions(start_ts DESC);
CREATE INDEX idx_sessions_agent ON sessions(agent_name);
CREATE INDEX idx_sessions_model ON sessions(model);
CREATE INDEX idx_sessions_provider ON sessions(provider);
CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_parent ON sessions(parent_session_id);
CREATE INDEX idx_sessions_cwd ON sessions(cwd);
CREATE INDEX idx_sessions_activity ON sessions(last_activity_ts DESC);
```

Notes:

- `status` is an explicit 5-value enum. `running` covers in-flight AND sessions from sources without a per-session terminal signal (claude-code, codex). The UI uses `last_activity_ts` to render "stale running" sessions distinctly from active ones.
- `cwd`, `provider_alias`, and `call_path` are promoted to first-class columns because at least one adapter populates them today AND they drive a filter or grouping path in the UI. Extras_json holds the long tail (finalReport, pluginMetas, claude-code title, codex sandbox policy, etc.).

### turns

```sql
CREATE TABLE turns (
    id                TEXT PRIMARY KEY NOT NULL,
    session_id        TEXT NOT NULL REFERENCES sessions(id),
    seq               INTEGER NOT NULL,         -- 0-based: 0 reserved for init turns (ai-agent v2); 1+ for normal turns
    start_ts          INTEGER NOT NULL,
    end_ts            INTEGER,
    status            TEXT NOT NULL,            -- 'running' | 'completed' | 'failed' | 'aborted'
    error_class       TEXT,
    tokens_in         INTEGER NOT NULL DEFAULT 0,
    tokens_out        INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL NOT NULL DEFAULT 0.0,
    op_count          INTEGER NOT NULL DEFAULT 0,
    extras_json       TEXT,                     -- e.g. codex_turn_id, claude-code system.subtype='turn_duration'
    UNIQUE (session_id, seq)
);

CREATE INDEX idx_turns_session_seq ON turns(session_id, seq);
CREATE INDEX idx_turns_start ON turns(start_ts);
```

### ops

The universal span. Every LLM call, tool call, child-session attachment, reasoning block, system housekeeping op, and compaction event is an `op`.

```sql
CREATE TABLE ops (
    id              TEXT PRIMARY KEY NOT NULL,
    turn_id         TEXT NOT NULL REFERENCES turns(id),
    session_id      TEXT NOT NULL REFERENCES sessions(id),  -- denormalized for fast filter
    parent_op_id    TEXT REFERENCES ops(id),                -- for nested ops
    seq             INTEGER NOT NULL,                       -- order within turn
    kind            TEXT NOT NULL,                          -- 'llm'|'tool'|'session'|'reasoning'|'internal'|'system'|'compaction'
    name            TEXT NOT NULL,                          -- tool name, model name, child agent name, compaction trigger, etc.
    tool_namespace  TEXT,                                   -- kind='tool': 'mcp:<server>' | 'shell' | 'fs' | 'builtin' | format-specific
    model           TEXT,                                   -- kind='llm'
    provider        TEXT,                                   -- 'anthropic'|'openai'|'google'|'openrouter'|...
    provider_alias  TEXT,                                   -- user-defined provider alias (opencode)
    reasoning_kind  TEXT,                                   -- kind='reasoning': 'summary' | 'raw'
    start_ts        INTEGER NOT NULL,
    end_ts          INTEGER,
    duration_us     INTEGER,                                -- end_ts - start_ts when both known
    status          TEXT NOT NULL,                          -- 'running'|'completed'|'failed'|'cancelled'|'truncated'
    error_class     TEXT,
    error_message   TEXT,
    tokens_in       INTEGER NOT NULL DEFAULT 0,
    tokens_out      INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL NOT NULL DEFAULT 0.0,
    bytes_in        INTEGER NOT NULL DEFAULT 0,             -- request payload size (uncompressed)
    bytes_out       INTEGER NOT NULL DEFAULT 0,
    chars_in        INTEGER,                                -- when source records UTF-8 chars instead of bytes (ai-agent v2 tools)
    chars_out       INTEGER,
    ctx_used        INTEGER,                                -- context window tokens consumed (LLM ops)
    ctx_max         INTEGER,                                -- model max context (LLM ops)
    child_session_id TEXT REFERENCES sessions(id),          -- present when kind='session'
    extras_json     TEXT,
    UNIQUE (turn_id, seq)
);

CREATE INDEX idx_ops_session_start ON ops(session_id, start_ts);
CREATE INDEX idx_ops_turn_seq ON ops(turn_id, seq);
CREATE INDEX idx_ops_kind_name ON ops(kind, name);
CREATE INDEX idx_ops_tool ON ops(tool_namespace, name) WHERE kind='tool';
CREATE INDEX idx_ops_model ON ops(model) WHERE kind='llm';
CREATE INDEX idx_ops_provider ON ops(provider) WHERE kind='llm';
CREATE INDEX idx_ops_status ON ops(status);
CREATE INDEX idx_ops_start ON ops(start_ts);
CREATE INDEX idx_ops_parent ON ops(parent_op_id);
CREATE INDEX idx_ops_compaction ON ops(session_id, start_ts) WHERE kind='compaction';
```

Notes on op kinds:

- `llm` — model API call. Populates `model`, `provider`, optionally `provider_alias`, `ctx_used`, `ctx_max`.
- `tool` — tool invocation. Populates `tool_namespace`, `name`. `chars_in`/`chars_out` populated when source records characters (ai-agent v2 tools).
- `session` — child-session attachment (sub-agent, Task, Agent tool). Populates `child_session_id`.
- `reasoning` — model reasoning. Populates `reasoning_kind` ('summary' or 'raw').
- `internal` — adapter-internal housekeeping. UI hides by default; available via "Show internal" toggle.
- `system` — session system ops (init/fin/handoff). ai-agent's `system` kind maps here. UI renders muted.
- `compaction` — history compaction. claude-code `compact_boundary` and codex `compacted`/`context_compacted` map here. Extras_json carries `preTokens`, `postTokens`, `durationMs`, `trigger`. UI renders as a visible breakpoint on the session timeline.

### payload_refs

Pointers to payload artifacts living on disk in the source system. ai-viewer NEVER copies these; only reads them when the UI requests.

```sql
CREATE TABLE payload_refs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id           TEXT NOT NULL REFERENCES ops(id),
    kind            TEXT NOT NULL,           -- 'llm_request'|'llm_response'|'llm_sdk_request'|'llm_sdk_response'|'llm_reasoning'|'tool_request'|'tool_response'|'log'
    format          TEXT NOT NULL,           -- 'http'|'sse'|'json'|'jsonrpc'|'text'|'binary'
    compression     TEXT,                    -- 'gzip' | NULL
    location_uri    TEXT NOT NULL,           -- 'file:///<absolute-path>'
    original_bytes  INTEGER,
    stored_bytes    INTEGER,
    sha256          TEXT                     -- hex; NULL when source does not provide
);

CREATE INDEX idx_payload_refs_op ON payload_refs(op_id);

-- Migration 0003: natural-identity uniqueness for idempotent re-scans.
-- An op has at most one payload per (kind, location). The ingester
-- inserts with ON CONFLICT DO NOTHING so re-emitting the same payload
-- (Tail re-read, file re-scan) never duplicates the row.
CREATE UNIQUE INDEX idx_payload_refs_identity
    ON payload_refs(op_id, kind, location_uri);
```

`payload_refs` uses `DO NOTHING` (not `DO UPDATE`) intentionally:
payload artifacts are immutable once written. If a re-emit carries the
same `(op_id, kind, location_uri)` but different `format`,
`compression`, `original_bytes`, `stored_bytes`, or `sha256`, the
original row's metadata is kept. This is safe because a changed payload
never mutates an existing artifact in place — it lands at a new
`location_uri` / a new on-disk ledger record, so it inserts as a
distinct row rather than colliding. If a future adapter ever rewrites a
payload at the same `location_uri` with different content, switch this
INSERT to `ON CONFLICT ... DO UPDATE`.

### log_entries

Structured log lines attached to a session/turn/op, surfaced in the per-session detail page. The table also stores source-level log entries that have no session attached — most notably the rows written from `SourceErrorEvent` (parse errors surfaced in `/api/health`). For those rows `session_id` is `NULL` and `source_id` references `sources(id)`. The `CHECK` constraint enforces that at least one of `session_id` and `source_id` is set so every row has a navigable owner in the UI.

```sql
CREATE TABLE log_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT REFERENCES sessions(id),  -- NULL for source-level entries
    source_id   TEXT REFERENCES sources(id),   -- set when session_id is NULL
    turn_id     TEXT REFERENCES turns(id),
    op_id       TEXT REFERENCES ops(id),
    ts          INTEGER NOT NULL,
    severity    TEXT NOT NULL,               -- 'DBG'|'INF'|'WRN'|'ERR'
    source      TEXT NOT NULL,               -- adapter name or subsystem
    message     TEXT NOT NULL,
    extras_json TEXT,
    CHECK (session_id IS NOT NULL OR source_id IS NOT NULL)
);

CREATE INDEX idx_log_session_ts ON log_entries(session_id, ts);
CREATE INDEX idx_log_source_ts  ON log_entries(source_id, ts) WHERE source_id IS NOT NULL;
CREATE INDEX idx_log_severity   ON log_entries(severity, ts) WHERE severity IN ('WRN','ERR');

-- Migration 0003: natural-identity uniqueness for idempotent re-scans.
-- COALESCE maps NULL owner columns to a '' sentinel so re-emitted
-- rows collide (raw SQL NULLs are distinct in a UNIQUE index, which
-- would otherwise let duplicates through). turn_id is part of the key:
-- v3 emits turn-scoped warnings/errors with turn_id set but op_id NULL,
-- so two genuinely distinct logs in the same session under different
-- turns (same ts/severity/source/message, op_id NULL) must NOT collide.
-- INVARIANT: the key lists every persisted content column (everything
-- except the autoincrement id), so extras_json is the last keyed column
-- (v2 stores the source `path` in extras — two logs identical except for
-- extras must stay distinct). A log row is a duplicate iff it is
-- byte-identical; omitting any persisted column reintroduces false-dedup
-- data loss. The ingester's logEntryOnConflict ON CONFLICT target must
-- match this expression list character-for-character, and any new
-- log_entries column must be added to both in the same commit.
-- The ingester inserts with ON CONFLICT DO NOTHING. message and
-- extras_json are indexed directly (not hashed): log rows here are short
-- structured lines (parse errors, pricing-miss warnings) — payload
-- bodies live in payload_refs, never log_entries — so the b-tree stays
-- small and a hash column would add schema/write complexity for no
-- measurable benefit.
CREATE UNIQUE INDEX idx_log_entries_identity ON log_entries(
    COALESCE(session_id, ''),
    COALESCE(source_id, ''),
    COALESCE(op_id, ''),
    COALESCE(turn_id, ''),
    ts, severity, source, message,
    COALESCE(extras_json, '')
);
```

### Catalog tables (denormalized rollups)

Refreshed by the ingester after each batch commit. Powers the cross-session analytics pages.

```sql
CREATE TABLE catalog_providers (
    name             TEXT NOT NULL,                  -- canonical: 'anthropic'|'openai'|'google'|'openrouter'|...
    alias            TEXT NOT NULL DEFAULT '',       -- user-defined alias (opencode); '' for canonical-only
    first_seen       INTEGER NOT NULL,
    last_seen        INTEGER NOT NULL,
    session_count    INTEGER NOT NULL DEFAULT 0,
    call_count       INTEGER NOT NULL DEFAULT 0,
    failure_count    INTEGER NOT NULL DEFAULT 0,
    total_tokens_in  INTEGER NOT NULL DEFAULT 0,
    total_tokens_out INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_read INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    total_cost_usd   REAL NOT NULL DEFAULT 0.0,
    PRIMARY KEY (name, alias)
);

CREATE TABLE catalog_models (
    provider          TEXT NOT NULL,
    name              TEXT NOT NULL,
    first_seen        INTEGER NOT NULL,
    last_seen         INTEGER NOT NULL,
    call_count        INTEGER NOT NULL DEFAULT 0,
    failure_count     INTEGER NOT NULL DEFAULT 0,
    total_tokens_in   INTEGER NOT NULL DEFAULT 0,
    total_tokens_out  INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_read INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    total_cost_usd    REAL NOT NULL DEFAULT 0.0,
    total_duration_us INTEGER NOT NULL DEFAULT 0,
    ctx_max           INTEGER,                       -- layered: pricing-metadata floor seeded on first OpStarted; raised by adapter observations (see Context-Window-Percent below)
    PRIMARY KEY (provider, name)
);

CREATE TABLE catalog_tools (
    namespace        TEXT NOT NULL,                  -- 'mcp:<server>' | 'shell' | 'fs' | 'builtin' | format-specific
    name             TEXT NOT NULL,
    first_seen       INTEGER NOT NULL,
    last_seen        INTEGER NOT NULL,
    call_count       INTEGER NOT NULL DEFAULT 0,
    failure_count    INTEGER NOT NULL DEFAULT 0,
    total_tokens_in  INTEGER NOT NULL DEFAULT 0,
    total_tokens_out INTEGER NOT NULL DEFAULT 0,
    total_cost_usd   REAL NOT NULL DEFAULT 0.0,
    total_duration_us INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (namespace, name)
);

CREATE TABLE catalog_agents (
    source_format    TEXT NOT NULL,                  -- 'aiagent_v3'|'aiagent_v2'|'claude_code'|'codex'|'opencode'
    name             TEXT NOT NULL,
    first_seen       INTEGER NOT NULL,
    last_seen        INTEGER NOT NULL,
    session_count    INTEGER NOT NULL DEFAULT 0,
    turn_count       INTEGER NOT NULL DEFAULT 0,
    failure_count    INTEGER NOT NULL DEFAULT 0,
    total_tokens_in  INTEGER NOT NULL DEFAULT 0,
    total_tokens_out INTEGER NOT NULL DEFAULT 0,
    total_cost_usd   REAL NOT NULL DEFAULT 0.0,
    PRIMARY KEY (source_format, name)
);

CREATE TABLE catalog_cwds (
    source_format    TEXT NOT NULL,
    cwd              TEXT NOT NULL,
    first_seen       INTEGER NOT NULL,
    last_seen        INTEGER NOT NULL,
    session_count    INTEGER NOT NULL DEFAULT 0,
    total_cost_usd   REAL NOT NULL DEFAULT 0.0,
    PRIMARY KEY (source_format, cwd)
);
```

### source_progress

Per-source ingest bookkeeping introduced in migration `0002`. Holds an observability sequence counter and the most recent adapter cursor JSON so the ingester can resume scanning from the right offset. Re-emitted events are deduped at the SQL layer (idempotent upserts), not by a sequence watermark — see `ingester.md` §Dedup and Idempotency.

```sql
CREATE TABLE source_progress (
    source_id  TEXT PRIMARY KEY NOT NULL REFERENCES sources(id),
    last_seq   INTEGER NOT NULL DEFAULT 0,
    last_ts_us INTEGER NOT NULL DEFAULT 0,
    cursor     TEXT,
    updated_at INTEGER NOT NULL
);
```

Notes:

- `last_seq` records the maximum `SourceSeq` observed per source, advanced atomically with the batch that wrote the matching events. It is an **observability counter** surfaced via `/api/health`; the ingester does NOT read it as a dedup gate. A per-source scalar watermark is structurally wrong here because one `sourceID` aggregates many independently-sequenced files (`SourceSeq` is per-file, not per-source) — see `ingester.md` §Dedup and Idempotency.
- `last_ts_us` records the Ts of the most recent observed event for diagnostics; the ingester does not use it for dedup.
- `cursor` mirrors the adapter's opaque JSON cursor; updated from `SourceProgressEvent` so a restart re-enters the source at the right offset. This is the durable resume point.
- The table is separate from `sources` (which holds operator-facing configuration) so per-batch updates do not contend with operator metadata. Spec'd in `ingester.md` §Dedup.

### notify

Append-only change log introduced in migration `0004`. It is the **notify channel** between the two binaries: the ingester (the sole writer) appends one or more rows inside the SAME transaction as each batch commit, and the serve process (read-only) polls `WHERE seq > <cursor>` to learn what changed and fan out SSE events. Living inside the shared SQLite file keeps the two-binary coupling to exactly "the SQLite file" (no second IPC channel). See `sse-protocol.md` and `architecture.md` §"Notify channel".

```sql
CREATE TABLE notify (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_us           INTEGER NOT NULL,
    kind            TEXT NOT NULL,          -- 'session_changed' | 'stats_invalidated' | 'source_status_changed'
    session_id      TEXT,                   -- set when kind='session_changed'
    root_session_id TEXT,                   -- set when kind='session_changed'
    source_id       TEXT                    -- set when kind='source_status_changed'
);
```

Notes:

- `seq` is `AUTOINCREMENT` (not just `ROWID`) so values are **strictly monotonic and never reused**, even after pruning deletes low rows. Serve's poll cursor is a `seq` high-water mark; reuse would make it skip rows. `WHERE seq > ?` uses the primary-key index — no extra index needed.
- **Atomicity**: rows are inserted in the same `*sql.Tx` as the data they describe, so serve can never observe a `notify` row before the row it refers to is visible (no notify-before-data race).
- **Producer rules** (`ingester.md` §Notify channel): one `session_changed` row per canonical session ID in the batch's `affectedSessionIDs` (carrying its `root_session_id` and the batch commit `ts_us`); at most one `stats_invalidated` row per batch when catalog rollups changed; one `source_status_changed` row when a source's `parse_errors` count or `enabled` flag changed.
- **Pruning**: the ingester deletes `notify` rows older than a bounded retention window as part of its write cycle so the table stays small; the data is disposable transport, not history. Serve keeps its cursor in memory and jumps to `MAX(seq)` on startup (it only delivers changes that occur while a client is connected; clients reconcile historical state through the REST API), so pruning consumed rows is always safe.
- **Read-only serve**: serve never writes or prunes `notify` — it only `SELECT`s. All writes/prunes are the ingester's, honoring the read-only-serve contract (`architecture.md`).

### Schema versioning

```sql
CREATE TABLE schema_meta (
    key     TEXT PRIMARY KEY NOT NULL,
    value   TEXT NOT NULL
);
-- key='version' value='4', key='created_at' value=...
```

Migrations are file-based under `internal/store/migrations/NNNN_*.sql`. The store runs them in order at startup, idempotent. Major schema bumps trigger a full re-ingest (source cursors reset).

Migration history:

- `0001_initial.sql` — full v1 schema; sets `schema_meta.version = '1'`.
- `0002_source_progress.sql` — adds the `source_progress` table.
- `0003_idempotent_children.sql` — adds the natural-identity UNIQUE
  indexes `idx_payload_refs_identity` and `idx_log_entries_identity`,
  and bumps `schema_meta.version` to `'3'`. The matching binary version
  is `presenter.SchemaVersion` (servers refuse to start on mismatch), so
  the two bump in lockstep.
- `0004_notify.sql` — adds the `notify` change-log table (the ingester→serve
  notify channel; see §notify) and bumps `schema_meta.version` to `'4'`.
  `presenter.SchemaVersion` moves to `4` in the same change so serve refuses
  to start against an older DB.

The `0003` indexes are `CREATE UNIQUE INDEX` (no `IF NOT EXISTS` needed —
the migration runs once, tracked in `_schema_migrations`). The ingest DB
is **derived/disposable** (deleting `index.db` triggers a full re-ingest,
see `deployment.md`). If an operator's existing `index.db` already
contains duplicate `payload_refs`/`log_entries` rows from a pre-`0003`
binary, the `CREATE UNIQUE INDEX` will fail on apply; the one-time fix is
to delete `index.db` and re-ingest. A dedup-existing-rows migration is
deliberately not written because the data is disposable.

## Cross-Format Compatibility Matrix

How each canonical column is populated per adapter. `✓` = always when known; `~` = sometimes; `n/a` = source has no equivalent.

| Column | v3 | v2 | claude-code | codex | opencode |
|---|---|---|---|---|---|
| `sessions.cwd` | ~ | ~ | ✓ | ✓ | ✓ |
| `sessions.call_path` | ✓ | ~ | n/a | n/a | n/a |
| `sessions.provider_alias` | n/a | n/a | n/a | n/a | ✓ |
| `sessions.model` (at start) | ~ | ~ | ✓ | ✓ | ✓ |
| `sessions.status='abandoned'` | ✓ | ~ | n/a | n/a | n/a |
| `sessions.status='interrupted'` | ✓ | ~ | n/a | ~ | ~ |
| `sessions.status='running'` indefinite | ~ | n/a | ✓ | ~ | ~ |
| `turns.tokens_cache_read/write` | ~ | n/a | ✓ | ~ | ✓ |
| `ops.tokens_cache_read/write` | ~ | n/a | ✓ | ~ | ✓ |
| `ops.reasoning_kind='summary'` | n/a | n/a | n/a | ✓ | n/a |
| `ops.reasoning_kind='raw'` | n/a | n/a | n/a | ✓ | n/a |
| `ops.cost_usd` (from source) | ~ | ~ | n/a (computed) | n/a (computed) | ✓ |
| `ops.chars_in/out` instead of bytes | n/a | ✓ (tool accounting) | n/a | n/a | n/a |
| `ops.kind='system'` | ✓ | ✓ | n/a | n/a | n/a |
| `ops.kind='compaction'` | n/a | n/a | ✓ | ✓ | n/a |
| `payload_refs` populated | ✓ | ~ (legacy inline base64 not addressable as ref) | n/a | n/a | n/a |

## Sub-Agent Linkage Strategy (Per Adapter)

| Adapter | Strategy |
|---|---|
| ai-agent v3 | Child's `session_start` carries `parentSessionId` (96.8% observed). Fallback: parent's `ops[].kind='session'` lists `childSessionId`. Adapter emits both; ingester reconciles. |
| ai-agent v2 | Children are embedded inside parent's opTree (no separate file). Adapter walks `op.childSession` recursively and synthesizes child `SessionStartedEvent` with `ParentNativeID` set to the wrapping op's session traceId. |
| claude-code | Parent's assistant record has `tool_use` with id=X; sub-agent stored at `<sessionId>/subagents/agent-<agentId>.jsonl` with sidecar `.meta.json::toolUseId == X`. Adapter joins on toolUseId. Because sub-agent sessionId == parent sessionId, adapter synthesizes `NativeID = <parentSessionId>:agent:<agentId>` to avoid canonical-row collision. |
| codex | Separate rollout file; linkage via `payload.source.subagent.thread_spawn.parent_thread_id` or `forked_from_id`. Adapter emits Kind=`sub_agent` (parent_thread_id) or Kind=`fork` (forked_from_id). |
| opencode | `session.parent_id` column is authoritative (cross-checked against `part.data.state.metadata.sessionId` on `task` tool parts; 100% consistent observed). |

The ingester's 5-second resolver pass handles out-of-order parent/child arrival universally.

## Aggregation Strategy

For statistics views over an arbitrary timeframe:

- **Time-bucketed materialized rollups** (per-hour and per-day) per source, per model, per tool, per agent, per provider, per cwd. Refreshed incrementally by the ingester. Avoids `SUM` over millions of rows on every page load.
- Schema for rollup tables is defined in SOW-0007 (Statistics & Analytics).
- Phase 1 uses live aggregates over `ops`; rollups land in Phase 4.

## Retention

- ai-viewer **never deletes source files**. Retention is the source system's concern.
- ai-viewer's SQLite grows with the source. Initial policy: no automatic deletion. A future SOW may add: "drop `payload_refs` older than N days where the underlying file no longer exists".
- Catalog and rollup tables persist forever (small).

## Cost Calculation

- `cost_usd` on every `op` and rolled-up onto turns/sessions.
- Cost comes from two sources, in order:
  1. **From the source itself** when the adapter can extract it (ai-agent v3 records accounting per op; opencode records cost on `message.data.cost`; ai-agent v2 records on `op.accounting[]`).
  2. **From the static pricing table** keyed on `(provider, model)` when the source doesn't record cost. Pricing data lives at `internal/pricing/pricing.json`, embedded via `go:embed`. Updated by `scripts/refresh-pricing.sh` per SOW-0001 decision.
- Documented in `pricing.md` (created in SOW-0001 Chunk 1).

## Context-Window-Percent

For LLM ops where the model's max context is known (`catalog_models.ctx_max`), the UI computes `ctx_used / ctx_max` as a percentage. When `ctx_max` is unknown the UI shows the raw token count instead.

`catalog_models.ctx_max` is populated by a **layered** strategy — pricing metadata acts as a floor, adapter observations raise it from there, and the value never decreases:

1. **Pricing-metadata seed (floor).** On the first `OpStarted` for a (provider, model) pair the ingester's `catalogWriter` calls the `MetadataPricer` interface — declared in the ingest package as `internal/ingest.MetadataPricer` and satisfied by `*internal/pricing.Pricer` via its `CtxMax(provider, model string) (int64, bool)` method — to obtain `MaxInputTokens` for that model from the embedded `internal/pricing/pricing.json` catalog. The seed lands via `COALESCE(catalog_models.ctx_max, excluded.ctx_max)` so an already-known value is never overwritten by a smaller pricing-table number — the catalog row's existing value wins.
2. **Adapter-observation raise.** When an `OpFinalizedEvent` carries `CtxMax > 0` (e.g. ai-agent v3 records the actual context window used for the call), the ingester runs `ctx_max = CASE WHEN ? > 0 THEN MAX(COALESCE(ctx_max, 0), ?) ELSE ctx_max END`. Observations strictly climb; a smaller observation never lowers the value.
3. **Net effect.** The stored `ctx_max` is the maximum of (pricing-seed-floor, all observed `ev.CtxMax`). If pricing has no entry for the model, the value starts NULL and is set on the first observation. If the source never reports `CtxMax`, the value stays at the pricing-seed.

Code references: `internal/ingest/catalog.go:64-95` (seed-on-OpStarted in `catalogWriter.onOpStarted`) and `internal/ingest/catalog.go:173-199` (observation-raise in `catalogWriter.onOpFinalized`). The interface contract lives in `internal/ingest/pricing.go` (`MetadataPricer`); the loader at `internal/pricing/loader.go` provides the default implementation.

## References

- `internal/store/migrations/0001_initial.sql` — DDL (created in SOW-0001 Chunk 2).
- `.agents/sow/specs/canonical-events.md` — Event types one-to-one with the schema.
- `.agents/sow/specs/adapter-*.md` — per-format projection details.
- `.agents/sow/done/SOW-0002-20260526-cross-format-data-model-analysis.md` — the analysis that produced this schema.
