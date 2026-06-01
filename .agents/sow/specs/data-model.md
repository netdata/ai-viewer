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
    duration_us     INTEGER,                                -- end_ts - start_ts, computed at OpFinalized from the PERSISTED start_ts (OpStarted.Ts), NOT the finalize event's Ts; NULL when start/end unknown. See ingester.md §Catalog Tables.
    status          TEXT NOT NULL,                          -- 'running'|'completed'|'failed'|'cancelled'|'truncated'
    error_class     TEXT,
    error_message   TEXT,
    tokens_in       INTEGER NOT NULL DEFAULT 0,              -- FRESH/uncached input only (canonical-events.md token contract); cache is the two columns below, NEVER folded in. Total input = tokens_in + tokens_cache_read + tokens_cache_write.
    tokens_out      INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read INTEGER NOT NULL DEFAULT 0,            -- cached input read (cache-read rate); separate from tokens_in
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,           -- cache creation (cache-write rate); separate from tokens_in
    cost_usd        REAL NOT NULL DEFAULT 0.0,
    bytes_in        INTEGER NOT NULL DEFAULT 0,             -- request payload size (uncompressed)
    -- ctx_used (below) is the TOTAL context occupancy = tokens_in + tokens_cache_read + tokens_cache_write + tokens_out, NOT tokens_in + tokens_out.
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
-- key='version' value='6', key='created_at' value=...
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
- `0005_op_duration_backfill.sql` — backfills `ops.duration_us = end_ts - start_ts`
  for historical rows and recomputes `catalog_models` / `catalog_tools.total_duration_us`
  from the corrected per-op values (SOW-0026 — fixing the bug where duration was
  computed from the finalize event's `Ts` and persisted as `0`), and bumps
  `schema_meta.version` to `'5'` in lockstep with `presenter.SchemaVersion`. It
  changes only row DATA (no table/column/index change), but it bumps the version
  anyway: `ai-viewer-serve` runs no migrations and gates startup solely on
  `schema_meta.version` (`CheckSchema`, exact-equality), so a v5 serve binary
  refuses to start against a pre-0005 store still reading `'4'` — it therefore
  never serves the stale `duration_us = 0` rows this migration repairs. (The runner
  still tracks applied files in `_schema_migrations` by filename, so 0005 runs once;
  the version bump is the serve-side compatibility gate.) A migration bumps the
  version (with `presenter.SchemaVersion`) when serve reads or validates its
  outcome — a schema-shape change, or served data like these durations; an
  ingester-only migration that serve never reads (e.g. `0002_source_progress.sql`)
  stays version-neutral.
- `0006_rollups_and_fts.sql` — adds the time-bucketed rollup tables
  (`rollup_hourly`, `rollup_daily`) and the FTS5 search tables (`fts_ops`,
  `fts_logs`) defined in §Rollup tables (SOW-0007), and bumps
  `schema_meta.version` to `'6'` in lockstep with `presenter.SchemaVersion`. Serve
  reads all four tables (`/api/stats/aggregate`, `/api/stats/top`, `/api/search`,
  and the rollup-backed `/api/stats`), so a v6 serve binary refuses to start
  against a pre-0006 store.

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
| `turns.tokens_cache_read/write` | ~ | ~ | ✓ | ~ | ✓ |
| `ops.tokens_cache_read/write` | ~ | ~ | ✓ | ~ | ✓ |
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

- **Time-bucketed materialized rollups** (per-hour and per-day), additive across the dimensions model, provider, tool, agent, cwd, source-format, and a `total` row. Refreshed incrementally by the ingester. Avoids `SUM` over millions of rows on every page load.
- The concrete schema is the `rollup_hourly` / `rollup_daily` tables defined in §Rollup tables (SOW-0007) below; the deep-search index is the `fts_ops` / `fts_logs` FTS5 tables in the same section.
- Phase 1 uses live aggregates over `ops`; the materialized rollups land in SOW-0007 (Statistics & Analytics). The current (open) hour is never materialized — the query layer `UNION ALL`s a live aggregate over `ops` for the open hour with the closed-hour rollups (see §Rollup tables).

## Rollup tables (SOW-0007)

Time-bucketed, **additive**, long-form rollups that back the statistics dashboard (`/stats`) and the `/api/stats`, `/api/stats/aggregate`, `/api/stats/top` endpoints (`rest-api.md`). Refreshed incrementally by the ingester after each batch commit and rebuildable from scratch by a one-shot backfill (`ingester.md` §Rollup Refresh and FTS5 Maintenance). The schema is **long-form** — one row per dimension value per bucket — deliberately NOT the `model × provider × tool × agent × cwd` cross-product, which would explode row count combinatorially.

```sql
CREATE TABLE rollup_hourly (
    bucket_ts          INTEGER NOT NULL,   -- UTC hour start, µs: (start_ts / 3600000000) * 3600000000
    source_format      TEXT NOT NULL,      -- 'aiagent_v3'|'aiagent_v2'|'claude_code'|'codex'|'opencode'
    dimension          TEXT NOT NULL,      -- 'total'|'model'|'provider'|'tool'|'agent'|'cwd'
    dimension_value    TEXT NOT NULL,      -- concrete value; '' for dimension='total'; "<ns>.<name>" (or "<name>" when namespace NULL) for 'tool'
    op_count           INTEGER NOT NULL DEFAULT 0,
    tokens_in          INTEGER NOT NULL DEFAULT 0,
    tokens_out         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0.0,
    failures           INTEGER NOT NULL DEFAULT 0,
    duration_us        INTEGER NOT NULL DEFAULT 0,  -- SUM of CLOSED ops' duration_us; running ops contribute 0
    session_starts     INTEGER NOT NULL DEFAULT 0,  -- count of sessions whose start_ts is in the bucket; only meaningful for total|agent|cwd (0 for model|provider|tool)
    PRIMARY KEY (bucket_ts, source_format, dimension, dimension_value)
);

CREATE TABLE rollup_daily (
    bucket_ts          INTEGER NOT NULL,   -- UTC day start, µs
    source_format      TEXT NOT NULL,
    dimension          TEXT NOT NULL,      -- 'total'|'model'|'provider'|'tool'|'agent'|'cwd'
    dimension_value    TEXT NOT NULL,
    op_count           INTEGER NOT NULL DEFAULT 0,
    tokens_in          INTEGER NOT NULL DEFAULT 0,
    tokens_out         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0.0,
    failures           INTEGER NOT NULL DEFAULT 0,
    duration_us        INTEGER NOT NULL DEFAULT 0,
    session_starts     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_ts, source_format, dimension, dimension_value)
);
```

**Per-op fan-out.** Each op contributes its metrics, within the op's `source_format` and hour bucket, to:

- its `model` row (only when `kind='llm'`);
- its `provider` row;
- its `tool` row (only when `kind='tool'`; `dimension_value` is `"<tool_namespace>.<name>"`, or `"<name>"` when `tool_namespace` is NULL — the same tool-id convention as the topology nodes in `rest-api.md`);
- its session's `agent` row (the session's `agent_name`);
- its session's `cwd` row (the session's `cwd`);
- the `total` row (`dimension_value=''`).

`session_starts` is added to the `total`, `agent`, and `cwd` rows of the **session's** start-hour bucket (it counts session starts, not ops), and stays `0` on the op-level `model`/`provider`/`tool` rows.

**Additivity is the correctness invariant.** Every metric column is SUM-additive: `rollup_daily` for a day equals the Σ of that day's `rollup_hourly` rows, and any arbitrary `[from, to)` aggregate equals the Σ of the buckets it covers. This is what makes the backfill-vs-incremental diff gate well-defined (`quality-gates.md` §Rollup correctness diff) — a full backfill and an incremental refresh of the same input must yield byte-identical tables. It is also why **non-additive distinct counts (e.g. distinct `session_count`) are NOT stored**: distinct counts cannot be summed across buckets. The additive `session_starts` is stored instead (a session whose lifetime spans buckets counts once, in its start bucket).

**Open-bucket rule (independent cutoffs per granularity).** Materialization uses an independent open-bucket cutoff per granularity, both derived from the same wall-clock `now`:

- `rollup_hourly` materializes every bucket with `bucket_ts < floor(now, hour)`. The single open hour `[floor(now, hour), …)` is **never** materialized.
- `rollup_daily` materializes every bucket with `bucket_ts < floor(now, day)`. The entire open day `[floor(now, day), …)` is **never** materialized.

Because the two cutoffs are independent, **the current day's already-closed hours DO appear in `rollup_hourly`, while the current day has NO `rollup_daily` row at all.** The "`rollup_daily` for a day = Σ that day's `rollup_hourly` rows" additivity identity therefore holds only for **fully-closed** days; the open day is never derived from its (partial) set of closed hourly rows. The query layer serves the open hour and the open day live by `UNION ALL`ing a live aggregate over `ops` with the materialized closed-bucket rollups. This keeps rollups immutable once written and removes any "partial bucket" race between the ingester and serve. Both the one-shot backfill and the incremental refresh (`ingester.md` §"One-shot backfill", §"Incremental rollup refresh") MUST apply these identical cutoffs, because the backfill-vs-incremental byte-diff gate (`quality-gates.md` §"Rollup correctness diff") compares their output exactly — a boundary disagreement is a gate failure.

**R1 safety bound (high-cardinality collapse).** A per-`(bucket_ts, source_format, dimension)` row cap, `maxRollupRowsPerBucket` (default `2000`), bounds the table against a high-cardinality dimension (most plausibly `cwd`). When a single dimension within one bucket would exceed the cap, its lowest-metric tail collapses into a single `dimension_value='__other__'` row, so an unbounded set of distinct cwds cannot explode the rollup tables. The collapse preserves additivity (the `__other__` row carries the summed tail metrics).

**Retention.** `rollup_hourly` defaults to 90 days; `rollup_daily` is kept forever (both are small relative to `ops`). Hourly pruning is a maintenance step (a bounded delete in the ingester's write cycle), not an automatic cascade, so an operator can widen the window without losing already-materialized daily history.

### Full-text search (FTS5)

Two FTS5 virtual tables back `GET /api/search` (`rest-api.md`). `modernc.org/sqlite` compiles FTS5 in, so no build flag or external extension is needed. Both rank with **BM25** (FTS5's default ranking since SQLite 3.21) and expose `snippet()` for the matched excerpt.

- **`fts_ops`** — FTS5 over the op's searchable text: `name`, `model`, `provider`, `tool_namespace`, and the op's `error_message` / `error_class` (joined by the ingester into a single `error_text` indexed column). Carries UNINDEXED linkage columns `op_id`, `session_id` so a match resolves back to its op without a join.
- **`fts_logs`** — FTS5 over `log_entries.message`, with UNINDEXED linkage/display columns `log_id`, `session_id`, `op_id`, `severity`, `ts`.

**Content mode (decided in the `0006` migration): CONTENT-OWNING** (the default FTS5 mode — no `content=` option), for both tables, NOT external-content. External-content FTS5 requires `content='<table>', content_rowid='<INTEGER rowid column>'` mapping each FTS rowid to a stable INTEGER rowid in the source. `ops` is keyed `id TEXT PRIMARY KEY` (`0001_initial.sql`) — it has no stable INTEGER rowid to map to (its implicit rowid is unrelated to `ops.id` and is not durable across vacuum), so external-content is structurally unavailable for ops. A content-owning table stores the indexed text plus the explicit UNINDEXED linkage/display columns above, which the ingester populates from the same op/log row it indexes (an FTS row is rebuilt whenever its op/log is re-emitted, so the duplicated columns never go stale) and `/api/search` reads back directly. `log_entries` *does* have an INTEGER rowid (`id INTEGER PRIMARY KEY AUTOINCREMENT`), so `fts_logs` could in principle be external-content; it is content-owning too for symmetry (one ingester population/maintenance path) and because the per-source `fts5_index_logs=false` flag below requires the ingester to selectively skip or clear log indexing — a content-owning table the ingester fully owns makes that a plain `DELETE`/skip, whereas external-content would couple index contents to `log_entries` row lifetime. Both rank with BM25 and expose `snippet()`; `modernc.org/sqlite` compiles FTS5 in, so no build flag or extension is needed.

All indexed timestamps remain UTC microseconds; the UI converts for display.

**Per-source config flag `fts5_index_logs` (default `true`).** When `false`, only `fts_ops` is maintained and `fts_logs` is left empty — this keeps the search index well under ~100 MB on log-heavy installs (log bodies dominate the index size). `GET /api/search` reports `"logs_indexed": false` and returns an empty `logs` array when the flag is off (`rest-api.md`).

Migrations are file-based; the rollup + FTS5 schema lands as `internal/store/migrations/0006_*.sql` (the next migration number after `0005`) and bumps `schema_meta.version` / `presenter.SchemaVersion` in lockstep, since serve reads these tables (§Schema versioning).

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
