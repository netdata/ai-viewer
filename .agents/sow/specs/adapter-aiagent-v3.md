# Adapter: ai-agent v3 (split format)

## Status

**Phase 1 target.** This adapter is the highest-priority deliverable because the operator's primary use case is observing their own ai-agent sessions.

## Source Format

ai-agent's v3 evidence format introduced in SOW-0002 of the ai-agent repository. Layout under the `--sessions-dir` root (default `~/.ai-agent/sessions/`):

```
<sessions-dir>/
  session/
    <sessionId>.jsonl              # append-only ledger, one record per line
  payloads/
    <sessionId>/
      turn-NNNN/
        llm-NNNN-request.http.gz
        llm-NNNN-response.sse.gz
        llm-NNNN-sdk-request.json.gz
        llm-NNNN-sdk-response.json.gz
        llm-NNNN-reasoning.txt.gz
        tool-NNNN-request.jsonrpc.gz
        tool-NNNN-response.jsonrpc.gz
```

Authoritative source: `ai-agent.git/.agents/sow/specs/snapshots.md` (see "v3 Evidence Format" section).

## JSONL Record Contract

Each line is a JSON object with:

```json
{
  "version": 3,
  "recordType": "session_start" | "turn_start" | "turn_end" | "session_summary" | "session_error",
  "seq": <int>,
  "ts": <int microseconds>,
  "originId": "<uuid>",
  "sessionId": "<uuid>",
  ...record-specific fields
}
```

### session_start

Carries: originId, sessionId, parent originId (if sub-agent), agent name, model (when known at start), kind.

### turn_start

Carries: turn seq.

### turn_end

Carries: turn seq, status, error class, end ts, accounting (tokens in/out, cost), `ops[]` array of compact operation summaries, `payloadRefs[]` of payload artifact references.

`ops[]` items have fields:
- `id`, `kind` (`llm|tool|session|reasoning|internal`), `name`, `seq`
- `start_ts`, `end_ts`
- `tokensIn`, `tokensOut`, `costUsd`, `bytesIn`, `bytesOut`
- `status`, `errorClass`, `errorMessage`
- `childSessionId` (when kind=session)
- `model`, `provider` (when kind=llm)
- `toolNamespace` (when kind=tool)
- `parentOpId` (when nested)

### session_summary

Final aggregates (totals, status, end ts).

### session_error

Top-level session failure.

## Mapping to Canonical Events

| v3 record | Canonical events emitted |
|---|---|
| `session_start` | `SessionStartedEvent` |
| `turn_start` | `TurnStartedEvent` |
| `turn_end` | `TurnFinalizedEvent` + one `OpStartedEvent`+`OpFinalizedEvent` pair per `ops[]` item + one `PayloadRefEvent` per `payloadRefs[]` item |
| `session_summary` | `SessionFinalizedEvent` with status=completed |
| `session_error` | `SessionFinalizedEvent` with status=failed + `LogEntry` with severity=ERR |

Note: ai-agent v3 does NOT emit `op_start` records mid-turn. Ops are only revealed at `turn_end`. The adapter therefore emits `OpStartedEvent` and `OpFinalizedEvent` back-to-back at turn_end time, using the op's recorded `start_ts`/`end_ts` for the canonical fields. The "live ops in progress" view will show only the current turn's running state until that turn ends — accepted limitation.

## Watch Strategy

- `fsnotify.Add()` on `<sessions-dir>/session/` (directory watch).
- React to `CREATE`, `WRITE`, `CHMOD` (signals append).
- For each touched JSONL file: read from last-known byte offset to EOF, parse line by line.
- Skip lines that don't end with `\n` (in-flight write); next event will replay.
- Atomic-rename writes from the producer: also watch for `RENAME`/`MOVED_TO` events; treat the renamed file as new.

## Cursor

```json
{
  "files": {
    "session/<sessionId>.jsonl": {
      "offset": <byte_offset>,
      "size": <file_size_at_offset>,
      "lastTs": <last_ts_us>
    }
  },
  "version": 1
}
```

On startup, the adapter reads the cursor and skips bytes already consumed in each file. For files not in the cursor: full scan.

## Sub-Agent Linkage

When the v3 producer attaches a child session inline (kind=`session` op with `childSessionId`), the child has its own `session/<childSessionId>.jsonl` file. The adapter emits the parent's `OpStartedEvent` (kind=session, ChildSessionNativeID=childSessionId) and the child's full event stream from its own file. The ingester links them via `parent_session_id`/`root_session_id`.

## Payload Resolution

`payloadRefs[]` items have a `path` relative to the `--sessions-dir` root. The adapter emits `PayloadRefEvent` with `LocationURI = "file://<sessions-dir>/<path>"`. The presenter (not the adapter) reads payload bytes when the UI requests them.

## Known Edge Cases

- **In-progress turn**: a `turn_start` with no matching `turn_end`. The adapter emits `TurnStartedEvent` and waits; the ingester records turn status=`running`.
- **Out-of-order records**: SDK guarantees monotonic `seq` per session. If the adapter sees a non-monotonic seq, it logs `SourceError` and skips.
- **Mixed v2/v3**: during ai-agent's own migration, a `<sessions-dir>` may contain both `<originId>.json.gz` (v2) and `session/<sessionId>.jsonl` (v3). The v3 adapter ignores `.json.gz` files; the v2 adapter ignores `session/` and `payloads/`. The ingester runs both adapters against the same location with different `Adapter.Name()` values.

## References

- ai-agent.git/.agents/sow/specs/snapshots.md — authoritative format spec
- ai-agent.git/src/evidence/writer.ts — producer code
- ai-agent.git/src/evidence/reader.ts — reference reader
