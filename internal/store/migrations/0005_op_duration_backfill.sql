-- ai-viewer schema migration 0005: op-duration backfill (data-only).
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
-- SCHEMA-VERSION-NEUTRAL: this migration changes only row DATA — no table,
-- column, or index is added or altered. The serve binary's
-- presenter.SchemaVersion stays 4 and CheckSchema is an exact-equality gate,
-- so bumping schema_meta.version here would make a v4-built serve binary
-- refuse to start against a freshly-migrated store. It is therefore the FIRST
-- migration that intentionally does NOT touch schema_meta.version. The marker
-- moves only when the schema SHAPE changes.

-- 1) Backfill the per-op duration from the authoritative start/end timestamps.
-- Guards: only rows with both timestamps present and end_ts >= start_ts are
-- touched, so still-running ops (end_ts NULL) keep NULL and clock-skewed rows
-- (end_ts < start_ts) are left as-is rather than written negative.
UPDATE ops
SET duration_us = end_ts - start_ts
WHERE start_ts IS NOT NULL
  AND end_ts IS NOT NULL
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
