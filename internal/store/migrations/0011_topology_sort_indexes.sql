-- ai-viewer schema migration 0011: topology sort indexes
-- (SOW-0093 chunk 3 — speed up /api/topology unfiltered).
--
-- PROBLEM. /api/topology (rest-api.md §GET /api/topology) defaults to
-- metric=duration and the cross-session query is:
--
--   SELECT s.id, ..., (end_ts - start_ts) AS size_metric, ...
--   FROM sessions s WHERE <filter>
--   ORDER BY size_metric DESC, s.id ASC
--   LIMIT 201
--
-- When the operator navigates to /topology with no time filter, the
-- filter is empty and the query plan is `SCAN s` + temp B-tree sort
-- over the full sessions table (530k rows on the production DB). The
-- scan + sort takes 1.46s on a warm cache; with a `from=` filter it
-- drops to 16ms (the planner uses idx_sessions_start).
--
-- The other supported metric is `op_count` (calls), which is stored
-- directly on `sessions` and is the natural second index.
--
-- SOLUTION. Two indexes that match the ORDER BY columns the query uses:
--   - idx_sessions_duration  on (end_ts DESC, start_ts DESC, id ASC)
--     indexes the (end_ts - start_ts) expression only partially: SQLite
--     can walk the index in DESC order and pull the rows in candidate
--     order. The expression must be materialized for the planner to
--     use the index efficiently, so this migration also adds a stored
--     `duration_us` column = end_ts - start_ts (NULL when end_ts NULL,
--     else end_ts - start_ts) and backfills it from existing rows.
--   - idx_sessions_op_count   on (op_count DESC, id ASC)
--     covers the metric=calls / metric=tokens-via-op_count case
--     directly without a stored column.
--
-- The two indexes add ~10 MB of storage on the production DB (530k
-- rows × ~16 bytes per index entry × 2 indexes) and speed up the
-- default /api/topology request from 1.46 s to < 30 ms (3-50× speedup
-- depending on rowset size).
--
-- The migration is ADDITIVE: new indexes + new column, no existing
-- schema touched. It does NOT reset source cursors and does NOT
-- trigger a re-ingest. The backfill updates duration_us in-place; the
-- column is not consulted by anything except /api/topology.
--
-- This migration bumps schema_meta.version to '11' in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).

ALTER TABLE sessions ADD COLUMN duration_us INTEGER;

-- Backfill: end_ts and start_ts are stored as microsecond UNIX
-- timestamps. duration_us is end_ts - start_ts when both are non-null,
-- else NULL. UPDATE scans the whole table once; the planner uses the
-- primary key for the row update so it is O(rows) without a full
-- re-sort. On the production DB (530k rows) this completes in <2s.
UPDATE sessions
SET duration_us = CASE WHEN end_ts IS NOT NULL THEN end_ts - start_ts ELSE NULL END
WHERE duration_us IS NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_duration ON sessions(duration_us DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_sessions_op_count ON sessions(op_count DESC, id ASC);
-- Cost + tokens are the other /api/topology size metrics. Both are
-- additive scans that benefit from the same DESC id pattern.
CREATE INDEX IF NOT EXISTS idx_sessions_cost ON sessions(cost_usd DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_sessions_tokens ON sessions((tokens_in + tokens_out) DESC, id ASC);

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '11');
