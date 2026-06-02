-- ai-viewer schema migration 0006: time-bucketed rollups + FTS5 search.
-- Source of truth: .agents/sow/specs/data-model.md §Rollup tables (SOW-0007)
-- and §Full-text search (FTS5), and the query shapes in
-- .agents/sow/specs/rest-api.md (/api/stats, /api/stats/aggregate,
-- /api/stats/top, /api/search).
--
-- This migration creates EMPTY tables only (DDL + indexes). The rollup
-- backfill and the FTS5 population are SOW-0007 Chunk 4 — no row is written
-- here. It bumps schema_meta.version to '6' in lockstep with
-- presenter.SchemaVersion: serve reads all four tables — rollup_hourly/
-- rollup_daily back /api/stats/aggregate and /api/stats/top, while fts_ops/
-- fts_logs back /api/search (/api/stats itself is live over ops, not these
-- tables) — so a v6 serve binary must refuse to start against a pre-0006 store
-- (CheckSchema, exact-equality) rather than serve missing/empty analytics
-- surfaces.

-- ---------------------------------------------------------------------
-- rollup_hourly / rollup_daily — long-form additive rollups.
--
-- Long-form (one row per dimension value per bucket), deliberately NOT the
-- model×provider×tool×agent×cwd cross-product, which would explode row count.
-- Every metric column is SUM-additive: rollup_daily for a day equals Σ of that
-- day's rollup_hourly rows, and any [from,to) aggregate equals Σ of the buckets
-- it covers (data-model.md §Rollup tables — Additivity invariant). Distinct
-- counts are intentionally absent (non-additive); the additive session_starts
-- is stored instead.
--
-- Rowid tables (NOT WITHOUT ROWID), matching every composite-PK table already
-- in this schema (catalog_providers/models/tools/agents/cwds in 0001 are all
-- plain rowid tables with a composite PRIMARY KEY). Rationale beyond
-- convention: the hot read path is the dimension-led top-N / time-series scan,
-- served by the secondary index below — NOT a point lookup on the 4-column
-- composite PK. WITHOUT ROWID optimizes PK-clustered lookups on a narrow key;
-- here the PK is wide (4 cols) and is used mainly to enforce one-row-per
-- (bucket,source_format,dimension,value) for the ingester's idempotent upsert,
-- not as the query access path. A plain rowid table keeps the secondary index
-- compact (rowid pointer vs the full wide PK as the row locator) and stays
-- consistent with the catalog tables the query layer already joins against.
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
    cost_usd           REAL    NOT NULL DEFAULT 0.0,
    failures           INTEGER NOT NULL DEFAULT 0,
    duration_us        INTEGER NOT NULL DEFAULT 0,  -- SUM of CLOSED ops' duration_us; running ops contribute 0
    session_starts     INTEGER NOT NULL DEFAULT 0,  -- count of sessions whose start_ts is in the bucket; only meaningful for total|agent|cwd (0 for model|provider|tool)
    PRIMARY KEY (bucket_ts, source_format, dimension, dimension_value)
);

-- Top-N by dimension over [from,to) (/api/stats/top) and time-series by
-- group_by (/api/stats/aggregate) both scan one dimension across a bucket
-- range. The PK leads with bucket_ts, so it cannot seek by dimension; this
-- secondary index leads with dimension and then bucket_ts so the planner seeks
-- dimension=? AND bucket_ts >= ? AND bucket_ts < ? and scans only the matching
-- slice. (A `sources` filter binds source_IDs via parseSessionFilter — finer
-- than the rollup source_format key — so it cannot be served from these
-- rollups and instead forces a live fold over ops; the rollup fast path runs
-- only for all-sources queries. See stats_rollup.go isRollupFastPath.)
CREATE INDEX idx_rollup_hourly_dim ON rollup_hourly(dimension, bucket_ts);

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
    cost_usd           REAL    NOT NULL DEFAULT 0.0,
    failures           INTEGER NOT NULL DEFAULT 0,
    duration_us        INTEGER NOT NULL DEFAULT 0,
    session_starts     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_ts, source_format, dimension, dimension_value)
);

-- Same dimension-led access path as rollup_hourly (the daily-bucket variant of
-- /api/stats/top and /api/stats/aggregate).
CREATE INDEX idx_rollup_daily_dim ON rollup_daily(dimension, bucket_ts);

-- ---------------------------------------------------------------------
-- fts_ops / fts_logs — FTS5 full-text search (BM25 default ranking).
--
-- modernc.org/sqlite compiles FTS5 in, so no build flag or external extension
-- is needed. Both rank with BM25 (FTS5's default since SQLite 3.21) and expose
-- snippet() for the matched excerpt; GET /api/search reads MATCH + rank +
-- snippet() (rest-api.md §GET /api/search).
--
-- CONTENT MODE: CONTENT-OWNING (the default FTS5 mode — no `content=` option),
-- NOT external-content. Reason: external-content FTS5 requires
-- content='<table>', content_rowid='<INTEGER rowid column>' mapping each FTS
-- rowid back to a stable INTEGER rowid in the source table. The `ops` table
-- (0001_initial.sql) is keyed `id TEXT PRIMARY KEY` — it has no stable INTEGER
-- rowid column to map to (its implicit rowid is unrelated to `ops.id` and is
-- not durable across vacuum), so external-content is structurally unavailable
-- for ops. A content-owning FTS5 table instead stores the indexed text itself
-- plus explicit UNINDEXED linkage/display columns the ingester populates and
-- the search endpoint reads back without a join. log_entries DOES have an
-- INTEGER rowid (`id INTEGER PRIMARY KEY AUTOINCREMENT`), so fts_logs could in
-- principle be external-content; it is content-owning too, for two reasons:
-- (1) symmetry with fts_ops keeps one population/maintenance path in the
-- ingester (Chunk 4); (2) the per-source `fts5_index_logs=false` flag
-- (data-model.md §Full-text search) requires the ingester to selectively skip
-- or clear log indexing per source — a content-owning table the ingester fully
-- owns makes that a plain DELETE/skip, whereas external-content would couple
-- index contents to log_entries row lifetime and complicate the opt-out.
--
-- The UNINDEXED columns are linkage + display fields the ingester writes from
-- the SAME op/log row it indexes (no extra read, no staleness: an FTS row is
-- rebuilt whenever its op/log is re-emitted), so /api/search returns its
-- documented JSON (op_id/session_id/name/model for ops; log_id/session_id/
-- op_id/severity/ts for logs) directly from the FTS row.

-- fts_ops indexes the op's searchable text — name, model, provider,
-- tool_namespace, and the op error text (error_message + error_class, joined by
-- the ingester into error_text). op_id/session_id are UNINDEXED linkage so a
-- match resolves back to its op without a join.
CREATE VIRTUAL TABLE fts_ops USING fts5(
    name,
    model,
    provider,
    tool_namespace,
    error_text,
    op_id UNINDEXED,
    session_id UNINDEXED
);

-- fts_logs indexes log_entries.message. log_id/session_id/op_id/severity/ts are
-- UNINDEXED so a match returns the documented /api/search log shape directly.
CREATE VIRTUAL TABLE fts_logs USING fts5(
    message,
    log_id UNINDEXED,
    session_id UNINDEXED,
    op_id UNINDEXED,
    severity UNINDEXED,
    ts UNINDEXED
);

-- Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '6');
