# Adapter: ai-agent v2 (legacy single-file format)

## Status

**Phase 1 target.** Required because the operator has ~294,000 v2 snapshot files (~30 GB) accumulated on disk.

## Source Format

Each session is a single gzipped JSON file:

```
<sessions-dir>/<originId>.json.gz
```

The decompressed JSON contains the full opTree for that session: metadata, accounting, finalReport, pluginMetas, and a deeply nested `opTree.turns[].ops[]` tree.

Authoritative source: `ai-agent.git/.agents/sow/specs/snapshots.md` and `ai-agent.git/.agents/sow/specs/optree.md`.

## Top-Level Structure (sketched; refined when adapter is implemented)

```json
{
  "originId": "<uuid>",
  "sessionId": "<uuid>",
  "createdAt": <ms>,
  "updatedAt": <ms>,
  "agentName": "<string>",
  "model": "<string>",
  "status": "running"|"completed"|"failed",
  "accounting": { ... totals ... },
  "finalReport": <obj|null>,
  "pluginMetas": [ ... ],
  "opTree": {
    "turns": [
      {
        "seq": 1,
        "startTs": <ms>,
        "endTs": <ms>,
        "status": "...",
        "ops": [
          {
            "id": "...",
            "kind": "llm"|"tool"|"session"|"reasoning",
            "name": "...",
            "startTs": <ms>,
            "endTs": <ms>,
            "status": "...",
            "tokensIn": ..., "tokensOut": ..., "costUsd": ...,
            "model": "...", "provider": "...",
            "toolNamespace": "...",
            "childSession": { ... nested session ... },
            "childSessionRef": "...",
            "childSessionSummary": { ... },
            "errorClass": "...", "errorMessage": "..."
          }
        ]
      }
    ]
  }
}
```

The exact field names and nesting will be confirmed against real samples (the operator's `~/.ai-agent/sessions/` contains 294K examples) during the SOW-0001 implementation. This spec will be updated with field-by-field evidence (file:offset citations) at that point.

## Mapping to Canonical Events

| Source structure | Canonical events |
|---|---|
| Top-level session metadata | `SessionStartedEvent` |
| Each `turns[]` element | `TurnStartedEvent` + `TurnFinalizedEvent` |
| Each `ops[]` item | `OpStartedEvent` + `OpFinalizedEvent` |
| Inline `childSession` (sub-agent or `tool_output` child) | nested session emission: walk the child tree recursively, emit all its events; emit parent `OpStartedEvent` with `ChildSessionNativeID` |
| `childSessionRef` (already compacted) | parent `OpStartedEvent`+`OpFinalizedEvent` with `ChildSessionNativeID`; child session events come from that session's own `<childSessionId>.json.gz` file (if present) |
| `status: failed` | `SessionFinalizedEvent` status=failed |
| `status: completed` | `SessionFinalizedEvent` status=completed |

## Watch Strategy

- `fsnotify.Add()` on `<sessions-dir>/` (directory).
- React to: `CREATE`, `WRITE` (full file rewrite triggers WRITE), and `MOVED_TO` (atomic rename pattern from the producer).
- Each touched file is re-parsed completely; the adapter emits a deduplicated event stream using a content hash for stable `SourceSeq` (since v2 rewrites the entire file on each change, there is no append cursor).

## Cursor

```json
{
  "files": {
    "<originId>.json.gz": {
      "mtime_us": <int>,
      "size": <int>,
      "content_hash": "<sha256:first-N-bytes>"
    }
  },
  "version": 1
}
```

The adapter skips files whose `(mtime, size)` is unchanged since the last scan. For changed files: re-parse and dedup against existing canonical rows by re-emitting events with stable `SourceSeq = sha256(<originId>:<op.id>)`. The ingester upserts.

## Performance Considerations

- 294,000 files × ~62 KB average decompressed = ~18 GB to process for a full initial backfill.
- Backfill strategy: bounded worker pool (e.g. 8 goroutines) reading and decompressing in parallel. Target: full backfill in under 60 minutes on the operator's workstation.
- After backfill, the file watcher is the only load — incremental updates are tiny.
- Initial backfill runs **on first launch only**, then resumes from cursor on subsequent launches.

## Known Edge Cases

- **Active session being re-written constantly**: an active v2 session can rewrite its `.json.gz` dozens of times per minute. The adapter must debounce: if a file's mtime advances while we're reading it, finish the current read, then re-read once (not in a loop).
- **Files with empty/corrupt gzip**: count as `SourceError`, do not block other files.
- **`childSession` inline vs detached**: handle both shapes — inline means we have the child's events in the parent file; detached means we wait for the child's own file.

## References

- ai-agent.git/.agents/sow/specs/snapshots.md
- ai-agent.git/.agents/sow/specs/optree.md
- ai-agent.git/src/persistence.ts
- ai-agent.git/src/session-tree.ts
