-- ai-viewer schema migration 0015: resolver link-back indexes
-- (SOW-0117 — kill the unconditional ~1-core idle CPU burn).
--
-- PROBLEM. The background resolver (internal/ingest/resolver.go) runs four
-- UPDATE … RETURNING passes every 5 s (defaultResolverInterval). Each pass's
-- WHERE clause predicates on json_extract(<table>.extras_json, '$.aiViewer.<f>'):
--
--   linkParents            sessions.extras_json.$.aiViewer.parentNativeId
--   linkRoots (explicit)   sessions.extras_json.$.aiViewer.rootNativeId
--   linkOpChildren          ops.extras_json.$.aiViewer.childNativeId
--   linkOpChildrenByToolUse ops.extras_json.$.aiViewer.toolUseId
--
-- json_extract is not indexable, so each pass full-scans every row that
-- satisfies the cheap leading `…_id IS NULL` predicate and parses the JSON
-- blob on each such row. Nearly every op has child_session_id IS NULL (only
-- kind='session' Agent ops ever gain one), so the two ops passes scan ~all
-- ops every 5 s. On the production DB (5 116 370 ops, 275 961 sessions) the
-- four passes cost ≈ 2.7 s of CPU every 5 s — measured 64.4% of total CPU in
-- a 30 s pprof, ≈ 0.94 cores, unconditionally, 24/7. The matched sets are
-- tiny (322 / 3 183 / 571 / 571 rows); the cost is entirely the scan + per-row
-- JSON parse, not the linkage.
--
-- SOLUTION. Partial expression indexes on the four stashed JSON fields. The
-- partial WHERE (json_extract(…) IS NOT NULL) keeps each index tiny — only
-- rows that actually carry a stash are indexed (a few thousand, not millions).
-- Each resolver pass then seeks the index instead of scanning the table.
-- Validated on a temp DB mirroring the real schema: the toolUseId pass went
-- 10.8 ms → 0.3 ms (36×) and EXPLAIN changed SCAN ops → SCAN ops USING INDEX.
--
-- The migration is ADDITIVE: four new indexes, no existing schema touched, no
-- cursor reset, no re-ingest. Building the indexes evaluates json_extract once
-- per existing row (one-time, inside store.OpenWriter before the ingester
-- starts, so there is no resolver contention) but inserts only the stashed
-- rows into each index. Write-path overhead going forward is negligible: an
-- op/session insert updates at most one of these indexes, and only when the
-- stash is present (rare).
--
-- serve reads schema_meta.version via CheckSchema and refuses on mismatch;
-- 0013/0014 (also ingester-liveness indexes) bumped the version, so this
-- index-only migration does too for the same operator-clarity reason.
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '15');

CREATE INDEX IF NOT EXISTS idx_sessions_link_parent
    ON sessions(json_extract(extras_json, '$.aiViewer.parentNativeId'))
    WHERE json_extract(extras_json, '$.aiViewer.parentNativeId') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_link_root
    ON sessions(json_extract(extras_json, '$.aiViewer.rootNativeId'))
    WHERE json_extract(extras_json, '$.aiViewer.rootNativeId') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_link_child
    ON ops(json_extract(extras_json, '$.aiViewer.childNativeId'))
    WHERE json_extract(extras_json, '$.aiViewer.childNativeId') IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_link_tooluse
    ON ops(json_extract(extras_json, '$.aiViewer.toolUseId'))
    WHERE json_extract(extras_json, '$.aiViewer.toolUseId') IS NOT NULL;
