-- ai-viewer schema migration 0007: per-source fts5_index_logs opt-out flag.
-- Source of truth: .agents/sow/specs/data-model.md §Full-text search (FTS5)
-- (the per-source `fts5_index_logs=false` opt-out) and §sources.
--
-- This migration adds ONE column to sources; it does NOT populate fts_ops or
-- fts_logs (that ingester-side FTS index population is a SEPARATE later step).
-- The flag is config plumbing only here: the ingester persists the operator's
-- choice per source so a later step can read it and skip/clear log indexing for
-- sources that opt out. SQLite supports ALTER TABLE ... ADD COLUMN with a
-- NOT NULL DEFAULT (the default backfills existing rows in place), so the
-- additive change needs no table rebuild and no data move.
--
-- Default 1 (index logs) so the behaviour is opt-OUT: an operator who never
-- sets the flag keeps full-text log search, matching the spec default. The
-- ingester is the runtime source of truth and re-asserts the resolved value on
-- every source row upsert (daemon restart re-applies the configured value).
--
-- It bumps schema_meta.version to '7' in lockstep with presenter.SchemaVersion
-- (CheckSchema, exact-equality). A column-shape change to sources is a schema
-- surface serve validates at startup, so a v7 serve binary must refuse a
-- pre-0007 store rather than run against a sources table missing the column.
ALTER TABLE sources ADD COLUMN fts5_index_logs INTEGER NOT NULL DEFAULT 1;

-- Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '7');
