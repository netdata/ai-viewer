# Adapter: codex

## Status

**Phase 2 target.** Specification is preliminary; final mapping evidence-driven at implementation time.

## Source Format

Codex CLI stores sessions in multiple parallel layouts under `~/.codex/`:

```
~/.codex/
├── sessions/
│   └── YYYY/MM/DD/
│       └── rollout-YYYY-MM-DD-<uuid>.json     # date-sharded per-session rollout
├── session_index.jsonl                         # index of all sessions
├── logs_2.sqlite                               # logs sqlite db (and -shm, -wal)
├── state_5.sqlite                              # state sqlite db (and -shm, -wal)
├── history.json                                # command history
└── history.jsonl                               # detailed history (likely append-only)
```

The adapter's primary source is `sessions/YYYY/MM/DD/rollout-*.json`. `session_index.jsonl` may be consulted for fast discovery. The SQLite databases may carry log/state context — TBD at implementation.

## Watch Strategy

- `fsnotify.Add()` recursively on `~/.codex/sessions/` (must add new date directories as they're created).
- For `session_index.jsonl`: append cursor.
- For SQLite: probably not needed in v1 of this adapter; revisit if log_entries miss critical context.

## Record Shape

Authoritative source: Codex CLI mirror at `/opt/baddisk/monitoring/repos/ai/codex/`. Rollout JSON shape will be sampled from real files and documented here at implementation time.

## Cursor

```json
{
  "rollout_files": {
    "YYYY/MM/DD/rollout-...json": {
      "offset": <byte_offset>,
      "mtime_us": <int>
    }
  },
  "session_index_offset": <int>,
  "version": 1
}
```

## Mapping to Canonical Events

To be detailed at implementation time. Expected shape: each rollout file represents one session; records inside map to turn/op events using the standard canonical model.

## Known Considerations

- **Date-sharded directories.** The adapter must recognize and watch newly-created date directories. fsnotify in Go does not recurse by default — explicit `Add` on each new subdir.
- **SQLite WAL files.** The presence of `-shm`/`-wal` files means codex is actively writing. If we ever need to read its SQLite, we must open with `journal_mode=WAL` and `?mode=ro&immutable=0` (read-only, but not immutable).
- **Command history sensitive.** `history.json` contains full shell commands the user ran through codex. Fixtures redacted; never commit the operator's own history.

## References

- /opt/baddisk/monitoring/repos/ai/codex/ — upstream source mirror
- Real samples at `~/.codex/sessions/` on the operator's workstation
