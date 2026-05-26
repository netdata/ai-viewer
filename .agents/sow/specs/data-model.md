# Canonical Data Model (SQLite Schema)

## TL;DR

A normalized, format-agnostic, **span-shaped** model. Sessions contain Turns; Turns contain Ops; Ops are the universal span (LLM call, tool call, child session, reasoning block). Payloads are referenced by URI, never inlined. Adapters write rows; the presenter reads them. The schema is the contract between the two halves.

## Design Principles

- **Span-shaped.** Every operation is an `op` row with start/end timestamps, parent/child links, tokens, cost, status. This maps 1:1 to APM/OTel concepts and makes the frontend trivial.
- **Format-agnostic.** No `aiagent_only` column, no `claude_code_only` column. Format-specific extras go into a small `extras_json` field on the relevant row.
- **Payloads stay on disk.** SQLite stores `payload_ref` pointers; the original gz/json files are the source of truth.
- **Catalog tables for fast aggregation.** `catalog_tool`, `catalog_model`, `catalog_agent` are denormalized rollups updated by triggers (or batched by the ingester) so the Tools/Models/Agents analytics pages are fast.
- **Cursors live in SQLite.** Each `source` row records its last-ingested position, so the ingester can resume after a restart.

## Schema (v1)

All timestamps are `INTEGER` UNIX-microseconds (UTC). All IDs are `TEXT` UUID-v4 or stable hashes derived from source IDs.

### sources

Each row is one configured source the ingester watches.

```sql
CREATE TABLE sources (
    id              TEXT PRIMARY KEY,           -- e.g. "aiagent-v3:/home/costa/.ai-agent/sessions"
    format          TEXT NOT NULL,              -- 'aiagent_v3'|'aiagent_v2'|'claude_code'|'codex'|'opencode'
    location        TEXT NOT NULL,              -- filesystem path or DSN
    cursor          TEXT,                       -- opaque per-adapter cursor (JSON)
    last_seen_at    INTEGER,
    enabled         INTEGER NOT NULL DEFAULT 1,
    parse_errors    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);
```

### sessions

```sql
CREATE TABLE sessions (
    id                TEXT PRIMARY KEY,         -- canonical session id (hash of source_id + native id)
    source_id         TEXT NOT NULL REFERENCES sources(id),
    native_id         TEXT NOT NULL,            -- the originId/sessionId/uuid from the source format
    parent_session_id TEXT REFERENCES sessions(id),
    root_session_id   TEXT NOT NULL REFERENCES sessions(id),
    kind              TEXT NOT NULL,            -- 'root' | 'sub_agent' | 'tool_internal'
    agent_name        TEXT,                     -- if known
    model             TEXT,                     -- primary model used in this session
    status            TEXT,                     -- 'running' | 'completed' | 'failed' | 'unknown'
    error_class       TEXT,                     -- present when status='failed'
    start_ts          INTEGER NOT NULL,
    end_ts            INTEGER,
    tokens_in         INTEGER NOT NULL DEFAULT 0,
    tokens_out        INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL NOT NULL DEFAULT 0.0,
    turn_count        INTEGER NOT NULL DEFAULT 0,
    op_count          INTEGER NOT NULL DEFAULT 0,
    failure_count     INTEGER NOT NULL DEFAULT 0,
    extras_json       TEXT,                     -- format-specific extras (cwd for claude-code, etc.)
    UNIQUE (source_id, native_id)
);

CREATE INDEX idx_sessions_root_start ON sessions(root_session_id, start_ts);
CREATE INDEX idx_sessions_start ON sessions(start_ts DESC);
CREATE INDEX idx_sessions_agent ON sessions(agent_name);
CREATE INDEX idx_sessions_model ON sessions(model);
CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_parent ON sessions(parent_session_id);
```

### turns

```sql
CREATE TABLE turns (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id),
    seq          INTEGER NOT NULL,              -- 1-based turn number within the session
    start_ts     INTEGER NOT NULL,
    end_ts       INTEGER,
    status       TEXT NOT NULL,                 -- 'running' | 'completed' | 'failed'
    error_class  TEXT,
    tokens_in    INTEGER NOT NULL DEFAULT 0,
    tokens_out   INTEGER NOT NULL DEFAULT 0,
    cost_usd     REAL NOT NULL DEFAULT 0.0,
    op_count     INTEGER NOT NULL DEFAULT 0,
    UNIQUE (session_id, seq)
);

CREATE INDEX idx_turns_session_seq ON turns(session_id, seq);
CREATE INDEX idx_turns_start ON turns(start_ts);
```

### ops

The universal span. Every LLM call, tool call, child-session attachment, and reasoning block is an `op`.

```sql
CREATE TABLE ops (
    id              TEXT PRIMARY KEY,
    turn_id         TEXT NOT NULL REFERENCES turns(id),
    session_id      TEXT NOT NULL REFERENCES sessions(id),  -- denormalized for fast filter
    parent_op_id    TEXT REFERENCES ops(id),                -- for nested ops (e.g. reasoning inside an LLM op)
    seq             INTEGER NOT NULL,                       -- order within turn
    kind            TEXT NOT NULL,                          -- 'llm' | 'tool' | 'session' | 'reasoning' | 'internal'
    name            TEXT NOT NULL,                          -- tool name, model name, child agent name, etc.
    tool_namespace  TEXT,                                   -- present when kind='tool'
    model           TEXT,                                   -- present when kind='llm'
    provider        TEXT,                                   -- 'anthropic'|'openai'|'google'|...
    start_ts        INTEGER NOT NULL,
    end_ts          INTEGER,
    duration_us     INTEGER,                                -- end_ts - start_ts when both known
    status          TEXT NOT NULL,                          -- 'running'|'completed'|'failed'|'cancelled'
    error_class     TEXT,
    error_message   TEXT,
    tokens_in       INTEGER NOT NULL DEFAULT 0,
    tokens_out      INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL NOT NULL DEFAULT 0.0,
    bytes_in        INTEGER NOT NULL DEFAULT 0,             -- request payload size (uncompressed)
    bytes_out       INTEGER NOT NULL DEFAULT 0,             -- response payload size
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
CREATE INDEX idx_ops_status ON ops(status);
CREATE INDEX idx_ops_start ON ops(start_ts);
CREATE INDEX idx_ops_parent ON ops(parent_op_id);
```

### payload_refs

Pointers to payload artifacts living on disk in the source system.

```sql
CREATE TABLE payload_refs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id           TEXT NOT NULL REFERENCES ops(id),
    kind            TEXT NOT NULL,           -- 'llm_request'|'llm_response'|'llm_sdk_request'|'llm_sdk_response'|'llm_reasoning'|'tool_request'|'tool_response'|'log'
    format          TEXT NOT NULL,           -- 'http'|'sse'|'json'|'jsonrpc'|'text'
    compression     TEXT,                    -- 'gzip' | NULL
    location_uri    TEXT NOT NULL,           -- e.g. 'file:///home/costa/.ai-agent/sessions/payloads/...'
    original_bytes  INTEGER,
    stored_bytes    INTEGER
);

CREATE INDEX idx_payload_refs_op ON payload_refs(op_id);
```

### log_entries

Structured log lines attached to a session/turn/op, surfaced in the per-session detail page.

```sql
CREATE TABLE log_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    turn_id     TEXT REFERENCES turns(id),
    op_id       TEXT REFERENCES ops(id),
    ts          INTEGER NOT NULL,
    severity    TEXT NOT NULL,               -- 'DBG'|'INF'|'WRN'|'ERR'
    source      TEXT NOT NULL,               -- adapter name or subsystem
    message     TEXT NOT NULL,
    extras_json TEXT
);

CREATE INDEX idx_log_session_ts ON log_entries(session_id, ts);
CREATE INDEX idx_log_severity ON log_entries(severity, ts) WHERE severity IN ('WRN','ERR');
```

### Catalog tables (denormalized rollups)

Refreshed by the ingester after each batch commit. Powers the cross-session analytics pages.

```sql
CREATE TABLE catalog_tools (
    namespace        TEXT NOT NULL,
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

CREATE TABLE catalog_models (
    provider          TEXT NOT NULL,
    name              TEXT NOT NULL,
    first_seen        INTEGER NOT NULL,
    last_seen         INTEGER NOT NULL,
    call_count        INTEGER NOT NULL DEFAULT 0,
    failure_count     INTEGER NOT NULL DEFAULT 0,
    total_tokens_in   INTEGER NOT NULL DEFAULT 0,
    total_tokens_out  INTEGER NOT NULL DEFAULT 0,
    total_cost_usd    REAL NOT NULL DEFAULT 0.0,
    total_duration_us INTEGER NOT NULL DEFAULT 0,
    ctx_max           INTEGER,
    PRIMARY KEY (provider, name)
);

CREATE TABLE catalog_agents (
    source_format    TEXT NOT NULL,
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
```

### Schema versioning

```sql
CREATE TABLE schema_meta (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL
);
-- key='version' value='1', key='created_at' value=...
```

Migrations are file-based under `internal/store/migrations/NNNN_*.sql`. The store runs them in order at startup, idempotent. Major schema bumps trigger a full re-ingest (source cursors reset).

## Aggregation strategy

For statistics views over an arbitrary timeframe:

- **Time-bucketed materialized rollups** (per-hour and per-day) per source, per model, per tool, per agent. Refreshed incrementally by the ingester. Avoids `SUM` over millions of rows on every page load.
- Schema for rollups TBD in Phase 2 SOW (Phase 1 uses live aggregates over `ops`).

## Retention

- ai-viewer **never deletes source files**. Retention is the source system's concern.
- ai-viewer's SQLite grows with the source. Initial policy: no automatic deletion. A future SOW may add: "drop `payload_refs` older than N days where the underlying file no longer exists".
- Catalog and rollup tables persist forever (small).

## Cost calculation

- `cost_usd` on every `op` and rolled-up onto turns/sessions.
- Cost comes from two sources, in order:
  1. **From the source snapshot itself** when the adapter can extract it (ai-agent records accounting per op).
  2. **From a static pricing table** keyed on `(provider, model)` when the source doesn't record cost. Pricing table lives in code at `internal/canonical/pricing.go`; updated manually when models change. Documented in `pricing.md` spec (created when first needed).

## Context-window-percent

For LLM ops where the model's max context is known (catalog_models.ctx_max), the UI computes `ctx_used / ctx_max` as a percentage. When ctx_max is unknown the UI shows the raw token count instead.
