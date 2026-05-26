# Adapter: opencode

## Status

**Phase 2 target.** Specification is preliminary; final schema evidence-driven at implementation time.

## Source Format

Opencode stores everything in a single SQLite database:

```
~/.local/share/opencode/opencode.db
~/.local/share/opencode/opencode.db-shm
~/.local/share/opencode/opencode.db-wal
```

No JSON files, no per-session files. The full session, turn, and message history lives in SQLite tables.

## Watch Strategy

The challenge: SQLite changes do not trigger fsnotify events on the `.db` file in a meaningful way (the database engine writes to the WAL, not the main file).

**Strategy**: poll the WAL file's mtime + the database's `PRAGMA wal_checkpoint(PASSIVE)` state; combine with periodic `SELECT max(rowid) FROM messages` (or equivalent) to detect new rows. Initial poll interval: 1 second when idle, 250 ms when active.

Alternative if polling proves expensive: open opencode.db in WAL mode read-only and use `sqlite3_unlock_notify` via cgo. Reject this for v1 (we use modernc.org/sqlite which is CGO-free).

## Connection Details

- Open with `?mode=ro` for read-only safety.
- `journal_mode` is set by the writer; we do not change it.
- Set a short busy_timeout to avoid blocking writes from opencode itself.

## Schema

Opencode's schema will be reverse-engineered from a real database at implementation time. Authoritative source: opencode mirror at `/opt/baddisk/monitoring/repos/ai/opencode/`. The adapter spec will be updated with exact table names, column types, and the mapping queries.

Expected tables (based on opencode's published architecture):
- sessions (one row per session)
- messages (one row per message)
- tool_calls (or equivalent)
- providers / models metadata

## Cursor

```json
{
  "messages_rowid": <last_seen_rowid>,
  "sessions_rowid": <last_seen_rowid>,
  "version": 1
}
```

Cursor advances as new rows are read. SQLite `rowid` is monotonic per table, ideal for this.

## Mapping to Canonical Events

To be detailed at implementation time. Expected shape:

```sql
SELECT * FROM sessions WHERE rowid > :since ORDER BY rowid;
-- emit SessionStartedEvent for each, SessionFinalizedEvent when status=complete

SELECT * FROM messages WHERE rowid > :since ORDER BY rowid;
-- group by session_id, emit Turn and Op events based on message role/type
```

## Known Considerations

- **Concurrent writer**: opencode writes to its DB while we read. WAL mode handles this; never use `BEGIN EXCLUSIVE`.
- **Schema migrations from opencode**: opencode may bump its own schema between versions. The adapter must tolerate unknown columns (read named columns explicitly) and log `SourceError` if a table is missing.
- **Size**: the operator's opencode.db is currently ~3.5 GB. Initial backfill must be paginated to avoid loading everything into memory.

## References

- /opt/baddisk/monitoring/repos/ai/opencode/ — upstream source mirror
- Real database at `~/.local/share/opencode/opencode.db` on the operator's workstation
