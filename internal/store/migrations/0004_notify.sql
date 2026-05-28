-- ai-viewer schema migration 0004: the notify change-log table.
-- Source of truth: .agents/sow/specs/data-model.md §notify,
-- .agents/sow/specs/ingester.md §Notify Channel, and
-- .agents/sow/specs/sse-protocol.md §Transport.
--
-- notify is the change channel between the two binaries. The ingester
-- (the sole writer) appends one or more rows INSIDE the same
-- transaction as each batch commit, so the serve process can never
-- observe a notify row before the canonical rows it refers to are
-- visible (no notify-before-data race). The serve process is read-only
-- against this table: a poller goroutine reads `WHERE seq > <cursor>`
-- (~1s interval) and fans matching changes out to SSE clients. Living
-- inside the shared SQLite file keeps the two-binary coupling to exactly
-- "the SQLite file" — no second IPC channel — and works over a
-- shared-file network mount where a socket would not.
--
-- seq is INTEGER PRIMARY KEY AUTOINCREMENT (not a bare ROWID alias): the
-- AUTOINCREMENT keyword forces SQLite to track the highest-ever rowid in
-- sqlite_sequence so a value is NEVER reused after a low row is pruned.
-- Serve's poll cursor is a seq high-water mark; without AUTOINCREMENT a
-- reused seq would let the cursor skip the replacing row. `WHERE seq > ?`
-- rides the primary-key index, so no secondary index is needed.
--
-- No foreign keys: session_id / root_session_id / source_id are loose
-- references into disposable transport rows that the ingester prunes on
-- a bounded retention window. A FK would let a deleted/abandoned session
-- block a notify insert, which is wrong for a change log whose rows are
-- meant to outlive nothing.
CREATE TABLE notify (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_us           INTEGER NOT NULL,
    kind            TEXT NOT NULL,          -- 'session_changed' | 'stats_invalidated' | 'source_status_changed'
    session_id      TEXT,                   -- set when kind='session_changed'
    root_session_id TEXT,                   -- set when kind='session_changed'
    source_id       TEXT                    -- set when kind='source_status_changed'
);

-- Bump the operator-facing schema marker in lockstep with
-- presenter.SchemaVersion (the server refuses to start on mismatch).
INSERT OR REPLACE INTO schema_meta (key, value) VALUES ('version', '4');
