-- ai-viewer schema migration 0016: resolver op-child-by-toolUse join index
-- (SOW-0117 — the linkOpChildrenByToolUse EXISTS scan).
--
-- PROBLEM. The resolver's linkOpChildrenByToolUse pass correlates each
-- pending parent op against sessions to find the child whose stashed
-- toolUseId matches the op's stashed toolUseId:
--
--   ... AND json_extract(c.extras_json, '$.aiViewer.toolUseId')
--            = json_extract(ops.extras_json, '$.aiViewer.toolUseId')
--   FROM sessions c JOIN sessions parent ON parent.id = ops.session_id
--   WHERE c.source_id = parent.source_id ...
--
-- The ops side is backed by idx_ops_link_tooluse (migration 0015), so the
-- pending-op set is tiny (~300). But the sessions side `c` lookup filters by
-- json_extract(c.extras_json, '$.aiViewer.toolUseId') with no index: scoped by
-- source_id (idx_sessions_source_id), it still parses the JSON of every
-- session in the op's source per candidate. On the production DB that is the
-- dominant per-pass cost (~1.5 s every resolver tick while data is active).
--
-- SOLUTION. A partial expression index on the sessions-side toolUseId stash,
-- mirroring idx_ops_link_tooluse. The partial WHERE keeps it tiny (only
-- sessions that carry a toolUseId — claude-code sidecar children). The planner
-- can then seek the matching `c` directly instead of JSON-scanning the source.
--
-- ADDITIVE: one new index, no schema/column/cursor change, no re-ingest. Serve
-- reads schema_meta.version via CheckSchema; bumped in lockstep with 0015.

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '16');

CREATE INDEX IF NOT EXISTS idx_sessions_link_tooluse
    ON sessions(json_extract(extras_json, '$.aiViewer.toolUseId'))
    WHERE json_extract(extras_json, '$.aiViewer.toolUseId') IS NOT NULL;
