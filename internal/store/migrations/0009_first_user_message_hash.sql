-- ai-viewer schema migration 0009: first_user_message_hash on sessions
-- (SOW-0089 chunk 5b — deterministic related sessions).
--
-- The operator's verbatim: "related sessions are 'possibly related', although
-- they could probably be matched accurately, because we have the prompt the
-- agent was run, so we can do this match deterministically".
--
-- The current related-sessions query joins on (cwd, different harness,
-- overlapping time window). That's a heuristic. Two sessions in the same
-- working directory at the same time can be unrelated (e.g. two different
-- agents in the same repo). The deterministic match: hash the first user
-- prompt of each session and join on hash equality — same hash = same initial
-- prompt = same task, regardless of cwd / harness / timing.
--
-- This migration adds ONE column to sessions: first_user_message_hash TEXT,
-- NULLABLE, indexed (idx_sessions_first_user_message_hash). Existing rows
-- backfill to NULL (which the related-sessions query treats as "not yet
-- computed, fall back to the cwd heuristic"). A separate backfill pass
-- populates the column for historical sessions in <1 min for ~3k rows.
--
-- The column carries a sha256 hex digest of the first user-input op's
-- payload text (lowercased, whitespace-normalized). It is a one-way hash —
-- the prompt text itself lives in the (separate, redactable) payloads
-- table; we never store the raw prompt here.
--
-- Like 0008, this is ADDITIVE: nullable column, no default, no table rebuild,
-- no data move. It does NOT reset source cursors and does NOT trigger a
-- re-ingest (the hash is populated by ingest on the NEXT write to each
-- session, and by the one-time backfill pass).
--
-- It bumps schema_meta.version to '9' in lockstep with
-- presenter.SchemaVersion. A v9 serve binary refuses a pre-0009 store rather
-- than run against a sessions table missing the column.

ALTER TABLE sessions ADD COLUMN first_user_message_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_sessions_first_user_message_hash
    ON sessions(first_user_message_hash)
    WHERE first_user_message_hash IS NOT NULL;

INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '9');