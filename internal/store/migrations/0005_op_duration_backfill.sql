-- ai-viewer schema migration 0005: op-duration backfill.
-- Source of truth: .agents/sow/specs/data-model.md §ops.duration_us and
-- §catalog_models / §catalog_tools (total_duration_us).
--
-- WHY: before SOW-0026 the ingester computed ops.duration_us as
-- (OpFinalizedEvent.EndTs - OpFinalizedEvent.Ts). A finalize event sorts
-- AFTER its OpStarted within a session, so OpFinalizedEvent.Ts ≈ the op END
-- (== EndTs); the subtraction collapsed to ≈0 and EVERY historical op was
-- persisted with duration_us = 0. The matching catalog rollups
-- (catalog_models.total_duration_us / catalog_tools.total_duration_us), which
-- accumulate the persisted ops.duration_us, were therefore 0 as well. The
-- writer fix derives duration from the persisted start_ts (the OpStarted's Ts)
-- for all NEW finalizes; this migration repairs the historical rows + rollups.
--
-- This migration bumps schema_meta.version to '5' in lockstep with
-- presenter.SchemaVersion. ai-viewer-serve runs NO migrations and gates startup
-- solely on schema_meta.version (CheckSchema, an exact-equality match), so a
-- pre-0005 store still reads '4' and a v5 serve binary refuses to start against
-- it — preventing serve from handing out the stale duration_us = 0 rows this
-- migration repairs. A migration bumps the version when serve depends on its
-- outcome (0005 does — serve reads these durations); 0002_source_progress.sql,
-- which serve never reads, is version-neutral.

-- 1) Backfill the per-op duration from the authoritative start/end timestamps.
-- Guards mirror the writer's finalize gate (writer.go:
-- ev.EndTs > 0 && startTs.Int64 > 0 && ev.EndTs >= startTs.Int64) exactly: only
-- rows with both timestamps present, both strictly positive, and end_ts >=
-- start_ts are touched. Still-running ops (end_ts NULL) keep NULL, sentinel/zero
-- timestamps are skipped, and clock-skewed rows (end_ts < start_ts) are left
-- as-is rather than written negative.
UPDATE ops
SET duration_us = end_ts - start_ts
WHERE start_ts IS NOT NULL
  AND end_ts IS NOT NULL
  AND start_ts > 0
  AND end_ts > 0
  AND end_ts >= start_ts;

-- 2) Recompute catalog_models.total_duration_us as the SUM of the corrected
-- durations of its member LLM ops. Grouping mirrors catalog.go (onOpFinalized,
-- the OpLLM case gated at provider.Valid && model.Valid && provider != '' &&
-- model != '') exactly: members are ops with kind='llm' whose (provider, model)
-- equals the catalog row's (provider, name), and the catalog row's own
-- (provider, name) must be non-empty — empty-keyed rows never receive a live
-- total, so they are kept at 0 here too. NULL durations (un-backfilled rows
-- above) are excluded; COALESCE keeps the NOT NULL column at 0 when a row has
-- no members.
UPDATE catalog_models
SET total_duration_us = COALESCE((
    SELECT SUM(o.duration_us)
    FROM ops o
    WHERE o.kind = 'llm'
      AND o.provider = catalog_models.provider
      AND o.model = catalog_models.name
      AND o.duration_us IS NOT NULL
), 0)
WHERE catalog_models.provider <> ''
  AND catalog_models.name <> '';

-- 3) Recompute catalog_tools.total_duration_us as the SUM of the corrected
-- durations of its member tool ops. Grouping mirrors catalog.go (onOpFinalized,
-- the OpTool case) exactly: members are ops with kind='tool' whose normalized
-- namespace (COALESCE(NULLIF(tool_namespace,''),'builtin') — the same 'builtin'
-- default catalog.go applies via its `ns` variable) equals the catalog row's
-- namespace and whose name equals the catalog row's name. catalog.go's tool
-- path applies NO non-empty guard on namespace or name (the 'builtin' default
-- means namespace is never '', and name is bound directly), so NONE is added
-- here either — adding one would DIVERGE from catalog.go, not mirror it.
UPDATE catalog_tools
SET total_duration_us = COALESCE((
    SELECT SUM(o.duration_us)
    FROM ops o
    WHERE o.kind = 'tool'
      AND COALESCE(NULLIF(o.tool_namespace, ''), 'builtin') = catalog_tools.namespace
      AND o.name = catalog_tools.name
      AND o.duration_us IS NOT NULL
), 0);

-- 4) Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '5');
