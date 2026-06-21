-- ai-viewer schema migration 0010: fts_content full-text search index
-- (SOW-0091 — find sessions by prompt/response text content).
--
-- PROBLEM. /api/search (SOW-0035) uses two FTS5 indexes:
--   - fts_ops  : name, model, provider, tool_namespace, error_text
--   - fts_logs : message
-- Neither indexes the actual prompt / response text. An operator asking
-- "where did I have the agent discuss rate limiting?" cannot use the
-- search today — they get 0 results even when the term appears in
-- hundreds of first user prompts.
--
-- SOLUTION. A third FTS5 table fts_content, content-owning (mirroring
-- fts_ops), that indexes the operator-visible text extracted from each
-- op's primary payload via the canonical extractReadableText heuristic
-- (also used by the frontend's Markdown renderer). Indexed columns: text.
-- UNINDEXED linkage: op_id, session_id, turn_id (lets /api/search
-- resolve matches to ops without a join, same shape as fts_ops).
--
-- CONTENT OWNING (not external-content) for the same reason as fts_ops:
-- the `ops` table is keyed `id TEXT PRIMARY KEY` with no stable INTEGER
-- rowid column, so external-content is structurally unavailable. fts_content
-- stores the indexed text itself.
--
-- Per-op indexed text is bounded:
--   - 4 KB per payload file (same preview cap the API serves)
--   - typical message text after extractReadableText: ~2-3 KB
--   - worst case (a long assistant response): ~4 KB
-- fts_content row size is therefore bounded at ~4 KB. With ~1.7M ops in
-- the production DB that's < 7 GB worst case; in practice the median
-- indexed text is ~500 bytes so the table is ~1 GB.
--
-- Like 0006 and 0009, this is ADDITIVE: new table, no existing schema
-- touched. It does NOT reset source cursors and does NOT trigger a
-- re-ingest. The column is populated by ingest on the NEXT write to
-- each op (via the existing fts_refresh hook) and by a one-time
-- `ai-viewer-ingest backfill-fts-content` pass for historical ops.
--
-- This migration bumps schema_meta.version to '10' in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).

CREATE VIRTUAL TABLE fts_content USING fts5(
    text,
    op_id UNINDEXED,
    session_id UNINDEXED,
    turn_id UNINDEXED
);

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '10');
