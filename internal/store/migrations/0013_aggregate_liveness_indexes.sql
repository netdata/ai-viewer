-- ai-viewer schema migration 0013: aggregate liveness support indexes.
--
-- SOW-0114 live recovery found Tail heartbeat/stale-watchdog writes could still
-- time out while startup scan/repair refreshed dirty-session aggregates for
-- giant sessions. The ingester has one SQLite writer connection by design, so
-- dirty aggregate subqueries must stay session-scoped and index-driven.

CREATE INDEX IF NOT EXISTS idx_ops_session_status ON ops(session_id, status);
CREATE INDEX IF NOT EXISTS idx_ops_session_end ON ops(session_id, end_ts);

-- The serve binary reads aggregate-maintained session fields and refuses schema
-- mismatches, so the aggregate-liveness contract bumps the served schema.
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '13');
