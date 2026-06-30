-- ai-viewer schema migration 0014: source read-model repair liveness indexes.
--
-- Source-scoped FTS repair must page source sessions first and then page each
-- session's ops/logs by rowid. These indexes prevent repair reads from scanning
-- unrelated sources while holding the ingester's single SQLite connection.

CREATE INDEX IF NOT EXISTS idx_sessions_source_id ON sessions(source_id, id);
CREATE INDEX IF NOT EXISTS idx_ops_session ON ops(session_id);
CREATE INDEX IF NOT EXISTS idx_log_session ON log_entries(session_id);

-- SOW-0114 also changed FTS maintenance to address fts_ops/fts_logs by explicit
-- primary-row docids (ops.rowid / log_entries.id). ops.rowid is an internal
-- no-VACUUM maintenance key, not a durable external identifier; if external
-- maintenance rewrites implicit rowids, a full FTS rebuild is required before
-- search is trusted. Existing derived rows from pre-0014 stores can have
-- auto-assigned FTS docids, so clear the derived search indexes and let source
-- repair / rollups-backfill repopulate them from primary rows.
DELETE FROM fts_ops;
DELETE FROM fts_logs;

-- Serve reads data maintained by source repair, and the schema is part of the
-- liveness contract, so bump the served schema in lockstep.
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '14');
