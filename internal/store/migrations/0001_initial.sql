-- ai-viewer canonical schema v1.
-- Source of truth: .agents/sow/specs/data-model.md.
-- This file is embedded via go:embed and applied at startup by
-- internal/store.migrations.Up. Re-runs are guarded by the
-- _schema_migrations bookkeeping table maintained by the runner.
--
-- All timestamps are INTEGER UNIX-microseconds (UTC).
-- All IDs are TEXT UUID-v4 or stable hashes derived from source IDs.
-- Format-specific extras live in compact JSON extras_json fields.

-- ---------------------------------------------------------------------
-- sources
-- ---------------------------------------------------------------------
CREATE TABLE sources (
    id              TEXT PRIMARY KEY NOT NULL,
    format          TEXT NOT NULL,
    location        TEXT NOT NULL,
    cursor          TEXT,
    last_seen_at    INTEGER,
    enabled         INTEGER NOT NULL DEFAULT 1,
    parse_errors    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);

-- ---------------------------------------------------------------------
-- sessions
-- ---------------------------------------------------------------------
CREATE TABLE sessions (
    id                 TEXT PRIMARY KEY NOT NULL,
    source_id          TEXT NOT NULL REFERENCES sources(id),
    native_id          TEXT NOT NULL,
    parent_session_id  TEXT REFERENCES sessions(id),
    root_session_id    TEXT NOT NULL REFERENCES sessions(id),
    kind               TEXT NOT NULL,
    agent_name         TEXT,
    model              TEXT,
    provider           TEXT,
    provider_alias     TEXT,
    cwd                TEXT,
    call_path          TEXT,
    status             TEXT NOT NULL,
    error_class        TEXT,
    error_message      TEXT,
    start_ts           INTEGER NOT NULL,
    end_ts             INTEGER,
    last_activity_ts   INTEGER NOT NULL,
    tokens_in          INTEGER NOT NULL DEFAULT 0,
    tokens_out         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL    NOT NULL DEFAULT 0.0,
    turn_count         INTEGER NOT NULL DEFAULT 0,
    op_count           INTEGER NOT NULL DEFAULT 0,
    failure_count      INTEGER NOT NULL DEFAULT 0,
    extras_json        TEXT,
    UNIQUE (source_id, native_id)
);

CREATE INDEX idx_sessions_root_start ON sessions(root_session_id, start_ts);
CREATE INDEX idx_sessions_start      ON sessions(start_ts DESC);
CREATE INDEX idx_sessions_agent      ON sessions(agent_name);
CREATE INDEX idx_sessions_model      ON sessions(model);
CREATE INDEX idx_sessions_provider   ON sessions(provider);
CREATE INDEX idx_sessions_status     ON sessions(status);
CREATE INDEX idx_sessions_parent     ON sessions(parent_session_id);
CREATE INDEX idx_sessions_cwd        ON sessions(cwd);
CREATE INDEX idx_sessions_activity   ON sessions(last_activity_ts DESC);

-- ---------------------------------------------------------------------
-- turns
-- ---------------------------------------------------------------------
CREATE TABLE turns (
    id                 TEXT PRIMARY KEY NOT NULL,
    session_id         TEXT NOT NULL REFERENCES sessions(id),
    seq                INTEGER NOT NULL,
    start_ts           INTEGER NOT NULL,
    end_ts             INTEGER,
    status             TEXT NOT NULL,
    error_class        TEXT,
    tokens_in          INTEGER NOT NULL DEFAULT 0,
    tokens_out         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL    NOT NULL DEFAULT 0.0,
    op_count           INTEGER NOT NULL DEFAULT 0,
    extras_json        TEXT,
    UNIQUE (session_id, seq)
);

CREATE INDEX idx_turns_session_seq ON turns(session_id, seq);
CREATE INDEX idx_turns_start       ON turns(start_ts);

-- ---------------------------------------------------------------------
-- ops
-- ---------------------------------------------------------------------
CREATE TABLE ops (
    id                 TEXT PRIMARY KEY NOT NULL,
    turn_id            TEXT NOT NULL REFERENCES turns(id),
    session_id         TEXT NOT NULL REFERENCES sessions(id),
    parent_op_id       TEXT REFERENCES ops(id),
    seq                INTEGER NOT NULL,
    kind               TEXT NOT NULL,
    name               TEXT NOT NULL,
    tool_namespace     TEXT,
    model              TEXT,
    provider           TEXT,
    provider_alias     TEXT,
    reasoning_kind     TEXT,
    start_ts           INTEGER NOT NULL,
    end_ts             INTEGER,
    duration_us        INTEGER,
    status             TEXT NOT NULL,
    error_class        TEXT,
    error_message      TEXT,
    tokens_in          INTEGER NOT NULL DEFAULT 0,
    tokens_out         INTEGER NOT NULL DEFAULT 0,
    tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL    NOT NULL DEFAULT 0.0,
    bytes_in           INTEGER NOT NULL DEFAULT 0,
    bytes_out          INTEGER NOT NULL DEFAULT 0,
    chars_in           INTEGER,
    chars_out          INTEGER,
    ctx_used           INTEGER,
    ctx_max            INTEGER,
    child_session_id   TEXT REFERENCES sessions(id),
    extras_json        TEXT,
    UNIQUE (turn_id, seq)
);

CREATE INDEX idx_ops_session_start ON ops(session_id, start_ts);
CREATE INDEX idx_ops_turn_seq      ON ops(turn_id, seq);
CREATE INDEX idx_ops_kind_name     ON ops(kind, name);
CREATE INDEX idx_ops_tool          ON ops(tool_namespace, name) WHERE kind='tool';
CREATE INDEX idx_ops_model         ON ops(model) WHERE kind='llm';
CREATE INDEX idx_ops_provider      ON ops(provider) WHERE kind='llm';
CREATE INDEX idx_ops_status        ON ops(status);
CREATE INDEX idx_ops_start         ON ops(start_ts);
CREATE INDEX idx_ops_parent        ON ops(parent_op_id);
CREATE INDEX idx_ops_compaction    ON ops(session_id, start_ts) WHERE kind='compaction';

-- ---------------------------------------------------------------------
-- payload_refs
-- ---------------------------------------------------------------------
CREATE TABLE payload_refs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    op_id          TEXT NOT NULL REFERENCES ops(id),
    kind           TEXT NOT NULL,
    format         TEXT NOT NULL,
    compression    TEXT,
    location_uri   TEXT NOT NULL,
    original_bytes INTEGER,
    stored_bytes   INTEGER,
    sha256         TEXT
);

CREATE INDEX idx_payload_refs_op ON payload_refs(op_id);

-- ---------------------------------------------------------------------
-- log_entries
-- session_id is nullable so source-level entries (e.g. SourceErrorEvent
-- parse errors) can be stored with source_id set instead. The CHECK
-- enforces that every row is owned by either a session or a source.
-- ---------------------------------------------------------------------
CREATE TABLE log_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT REFERENCES sessions(id),
    source_id   TEXT REFERENCES sources(id),
    turn_id     TEXT REFERENCES turns(id),
    op_id       TEXT REFERENCES ops(id),
    ts          INTEGER NOT NULL,
    severity    TEXT NOT NULL,
    source      TEXT NOT NULL,
    message     TEXT NOT NULL,
    extras_json TEXT,
    CHECK (session_id IS NOT NULL OR source_id IS NOT NULL)
);

CREATE INDEX idx_log_session_ts ON log_entries(session_id, ts);
CREATE INDEX idx_log_source_ts  ON log_entries(source_id, ts) WHERE source_id IS NOT NULL;
CREATE INDEX idx_log_severity   ON log_entries(severity, ts) WHERE severity IN ('WRN','ERR');

-- ---------------------------------------------------------------------
-- catalog_providers
-- ---------------------------------------------------------------------
CREATE TABLE catalog_providers (
    name                     TEXT NOT NULL,
    alias                    TEXT NOT NULL DEFAULT '',
    first_seen               INTEGER NOT NULL,
    last_seen                INTEGER NOT NULL,
    session_count            INTEGER NOT NULL DEFAULT 0,
    call_count               INTEGER NOT NULL DEFAULT 0,
    failure_count            INTEGER NOT NULL DEFAULT 0,
    total_tokens_in          INTEGER NOT NULL DEFAULT 0,
    total_tokens_out         INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    total_cost_usd           REAL    NOT NULL DEFAULT 0.0,
    PRIMARY KEY (name, alias)
);

-- ---------------------------------------------------------------------
-- catalog_models
-- ---------------------------------------------------------------------
CREATE TABLE catalog_models (
    provider                 TEXT NOT NULL,
    name                     TEXT NOT NULL,
    first_seen               INTEGER NOT NULL,
    last_seen                INTEGER NOT NULL,
    call_count               INTEGER NOT NULL DEFAULT 0,
    failure_count            INTEGER NOT NULL DEFAULT 0,
    total_tokens_in          INTEGER NOT NULL DEFAULT 0,
    total_tokens_out         INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_read  INTEGER NOT NULL DEFAULT 0,
    total_tokens_cache_write INTEGER NOT NULL DEFAULT 0,
    total_cost_usd           REAL    NOT NULL DEFAULT 0.0,
    total_duration_us        INTEGER NOT NULL DEFAULT 0,
    ctx_max                  INTEGER,
    PRIMARY KEY (provider, name)
);

-- ---------------------------------------------------------------------
-- catalog_tools
-- ---------------------------------------------------------------------
CREATE TABLE catalog_tools (
    namespace         TEXT NOT NULL,
    name              TEXT NOT NULL,
    first_seen        INTEGER NOT NULL,
    last_seen         INTEGER NOT NULL,
    call_count        INTEGER NOT NULL DEFAULT 0,
    failure_count     INTEGER NOT NULL DEFAULT 0,
    total_tokens_in   INTEGER NOT NULL DEFAULT 0,
    total_tokens_out  INTEGER NOT NULL DEFAULT 0,
    total_cost_usd    REAL    NOT NULL DEFAULT 0.0,
    total_duration_us INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (namespace, name)
);

-- ---------------------------------------------------------------------
-- catalog_agents
-- ---------------------------------------------------------------------
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
    total_cost_usd   REAL    NOT NULL DEFAULT 0.0,
    PRIMARY KEY (source_format, name)
);

-- ---------------------------------------------------------------------
-- catalog_cwds
-- ---------------------------------------------------------------------
CREATE TABLE catalog_cwds (
    source_format  TEXT NOT NULL,
    cwd            TEXT NOT NULL,
    first_seen     INTEGER NOT NULL,
    last_seen      INTEGER NOT NULL,
    session_count  INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL    NOT NULL DEFAULT 0.0,
    PRIMARY KEY (source_format, cwd)
);

-- ---------------------------------------------------------------------
-- schema_meta — schema bookkeeping (version, created_at)
-- ---------------------------------------------------------------------
CREATE TABLE schema_meta (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

INSERT OR REPLACE INTO schema_meta (key, value) VALUES
    ('version', '1'),
    ('created_at', strftime('%s','now') || '000000');
