-- ai-viewer schema migration 0008: general per-source meta_json surface (SOW-0024).
-- Source of truth: .agents/sow/specs/data-model.md §sources (the meta_json contract)
-- and §Schema versioning.
--
-- This migration adds ONE column to sources: meta_json TEXT, NULLABLE, no default.
-- It is the general per-source metadata surface: any adapter can populate it
-- with source-native metadata that has no canonical-column analog (SOW-0024);
-- the presenter renders it verbatim under each source in /api/health and
-- /api/sources (omitted when NULL).
--
-- The change is ADDITIVE: a nullable column with no default, so existing rows
-- backfill to NULL (which the presenter omits as absence = "adapter did not
-- populate"), with no table rebuild and no data move. Like 0007, the additive
-- nullable column does NOT reset source cursors and does NOT trigger a re-ingest.
--
-- Sole writer is the ingester: cmd/ai-viewer-ingest auto-discovery marshals the
-- opencode ProbeStatus result via encoding/json and registers it via the new
-- ingest.WithSourceMeta option; file-based adapters leave the column NULL
-- (NULL = not populated, the omit-when-NULL contract in data-model.md).
--
-- It bumps schema_meta.version to '8' in lockstep with presenter.SchemaVersion
-- (CheckSchema, exact-equality). The sources.meta_json column shape is part of
-- the surface serve validates at startup, so a v8 serve binary must refuse a
-- pre-0008 store rather than run against a sources table missing the column.
ALTER TABLE sources ADD COLUMN meta_json TEXT;

-- Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '8');
