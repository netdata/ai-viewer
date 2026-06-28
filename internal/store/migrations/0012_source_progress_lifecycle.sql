-- ai-viewer schema migration 0012: durable source lifecycle/read-model state
-- (SOW-0114).
--
-- Adds API-visible source lifecycle and read-model repair state to the existing
-- one-row-per-source runtime table. This is additive only: legacy sources.cursor
-- remains untouched, and source_progress.cursor stays the authoritative resume
-- cursor.

ALTER TABLE source_progress ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'unknown'
    CHECK (lifecycle_state IN (
        'unknown',
        'starting',
        'start_failed',
        'construct_failed',
        'scanning',
        'scan_failed',
        'scan_complete',
        'tail_starting',
        'tailing',
        'tail_stale',
        'tail_failed',
        'tail_restarting',
        'stopped'
    ));

ALTER TABLE source_progress ADD COLUMN lifecycle_state_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_progress ADD COLUMN scan_started_at INTEGER;
ALTER TABLE source_progress ADD COLUMN scan_completed_at INTEGER;
ALTER TABLE source_progress ADD COLUMN tail_started_at INTEGER;
ALTER TABLE source_progress ADD COLUMN tail_heartbeat_at INTEGER;
ALTER TABLE source_progress ADD COLUMN tail_failed_at INTEGER;
ALTER TABLE source_progress ADD COLUMN tail_restart_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_progress ADD COLUMN lifecycle_error TEXT;

ALTER TABLE source_progress ADD COLUMN read_model_state TEXT NOT NULL DEFAULT 'unknown'
    CHECK (read_model_state IN (
        'unknown',
        'repair_pending',
        'repairing',
        'ready',
        'repair_timeout',
        'repair_failed'
    ));

ALTER TABLE source_progress ADD COLUMN read_model_state_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_progress ADD COLUMN read_model_repair_started_at INTEGER;
ALTER TABLE source_progress ADD COLUMN read_model_repair_completed_at INTEGER;
ALTER TABLE source_progress ADD COLUMN read_model_repair_failed_at INTEGER;
ALTER TABLE source_progress ADD COLUMN read_model_repair_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_progress ADD COLUMN read_model_error TEXT;

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '12');
