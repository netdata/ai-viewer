# Adapter: ai-agent v2 (Legacy Single Gzipped JSON)

## Status

**Phase 1 co-priority with v3.** The operator's `~/.ai-agent/sessions/` directory contains 294,316 v2 `.json.gz` files (~25.4 GB compressed, ~8 months of history) alongside 17,356 v3 `.jsonl` ledgers. Both formats live in the same root directory and must be parsed concurrently by separate adapters; the v2 adapter is the only path to historical data captured before the v3 evidence migration began.

This spec is an evidence-based replacement of the bootstrap sketch. It corrects multiple claims in the sketch that did not match real on-disk data. Every shape statement below is grounded in citation to the producer source or to direct inspection of the operator's snapshot files (sanitized).

## Source Format

### Producer behavior

The producer is `ai-agent` (`~/src/ai-agent.git`). Snapshots are written exclusively by `createPersistenceHandlers().handleSnapshot` in `ai-agent.git/src/persistence.ts:50-67`. The write sequence is:

1. `fs.mkdirSync(dir, { recursive: true })` — directory created on demand.
2. Serialize `{ version, reason, opTree }` via `JSON.stringify`, then `gzipSync`.
3. Write to a temp path `<dir>/<originId>.json.gz.tmp-<pid>-<Date.now()>`.
4. `fs.renameSync(tmp, finalPath)` — atomic replace.

If the rename succeeds, the final path now contains the new snapshot. If the writer crashes between step 3 and step 4, the `.tmp-<pid>-<ts>` artifact is left behind. The producer never cleans these up. The operator's directory currently has two such orphans (cited under Edge Cases).

### Filename convention

`<sessions-dir>/<originId>.json.gz` where `originId` is `AIAgentSession.originTxnId` (`ai-agent.git/src/ai-agent.ts:531`). For ROOT sessions, `originTxnId = txnId` (a fresh UUID v4 produced by `crypto.randomUUID()`). For SUB-AGENT sessions, `originTxnId` is inherited from the parent via `sessionConfig.trace.originId` (`ai-agent.git/src/ai-agent.ts:531,669`). **All descendants of a single root session share the same `originId` and therefore the same file path.** Each child session's own `persistSessionSnapshot('final')` and the parent's `persistSessionSnapshot('subagent_finish' | 'final')` all target the same `.json.gz` file (`ai-agent.git/src/ai-agent.ts:678,1221`). The last writer wins; in practice the parent's terminal `final` snapshot is the last write and contains the full embedded tree (verified empirically — see Snapshot Shape below).

### Atomic-rename behavior

The producer relies on Linux's POSIX rename atomicity. From the adapter's standpoint:

- `fsnotify` will surface a `WRITE` event on the temp file (no listener needed), then a `RENAME`/`MOVED_TO` event on the final path. The `.tmp-<pid>-<ts>` files are tracked under `RENAME`/`CREATE` paths only if the watcher is set to surface all events; the adapter ignores them by suffix.
- `RENAME` (target side, i.e. `Rename` in Go fsnotify) on the final path means "a new file was atomically moved into place".
- mtime on the final path equals the temp file's mtime at rename time, which is approximately `Date.now()` of step 3. It is not the session's `endedAt`; it is the snapshot write time.

### Coexistence with v3

The same `<sessions-dir>` root contains:

- v2 root-level files: `<originId>.json.gz`, `<originId>.json.gz.tmp-<pid>-<ts>` (orphans).
- v3 subdirectories: `session/<sessionId>.jsonl`, `payloads/<sessionId>/turn-NNNN/...`.

This is documented in `ai-agent.git/.agents/sow/specs/snapshots.md:30-45`.

The v2 adapter MUST:

- Watch only the root directory non-recursively, ignoring all subdirectories (`session/`, `payloads/`, and anything else).
- Process only `*.json.gz` files at the root; skip `*.json.gz.tmp-*` and any other suffix.
- Coordinate with the v3 adapter only through the shared cursor table (the SQLite `sources` table per `data-model.md`). The two adapters never see each other's files.

A future v2 file MAY have an `originId` matching a v3 `sessionId` (the same root session migrated mid-lifetime). The ingester writes both into the same canonical `sessions` row keyed on `(source_id, native_id)`; running each adapter against the same `<sessions-dir>` location requires distinct `source_id` values (one for `aiagent_v2`, one for `aiagent_v3`). De-duplication across formats is an ingester concern, not an adapter concern.

## Snapshot Shape (Canonical Contract Confirmed Against Real Files)

The bootstrap sketch claimed a flat top-level with `originId`, `sessionId`, `createdAt`, `updatedAt`, `agentName`, `model`, `status`, `accounting`, `finalReport`, `pluginMetas`, plus a nested `opTree.turns[].ops[]`. **This is wrong.** The actual shape is:

### Top level (`ai-agent.git/src/persistence.ts:53-66`)

```json
{
  "version": <int>,
  "reason": <string>,
  "opTree": { ... }
}
```

These are the three fields the **v2 adapter decodes** (`parser.go:18-44`); the JSON block above is the adapter's view, not the full on-disk envelope. Since `ai-agent@8a0078bc` the on-disk JSON ALSO carries `sessionId, originId, originTxnId, parentId, parentTxnId, timestamp` (see the note after the field list below); the adapter ignores them by design. The three decoded fields were verified across 200 random samples and the producer code path:

- `version`: integer, either `1` or `2`. Distribution on disk (50-sample run): v1 ≈ 60%, v2 ≈ 40%. The producer code at `ai-agent.git/src/ai-agent.ts:394` builds new snapshots with `version: 2`. Older snapshots on disk still carry `version: 1` from before the bump; they were never rewritten because the sessions ended before the change. **The version field gates SHAPE features inside `opTree`:**
  - `version: 1` opTree lacks `steps[]`, `finalReport`, and `pluginMetas`.
  - `version: 2` opTree may carry `steps[]`, `finalReport`, `pluginMetas`. All three are still optional within v2.
  - Op-level shape is identical across v1 and v2.
- `reason`: snapshot trigger reason, one of `'subagent_finish'` (intermediate, emitted by the parent after a child finishes), `'final'` (terminal, emitted once at session end), or `undefined` (test paths). On disk, **all 50 sampled files carry `reason: "final"`** — because intermediate `subagent_finish` snapshots are overwritten by the terminal `final` snapshot for the same originId.
- `opTree`: the `SessionNode` from `SessionTreeBuilder.getSession()` at the moment of write.

The wire-level `SessionSnapshotPayload` (`ai-agent.git/src/types.ts:786-793`) ALSO carries `sessionId`, `originId`, `timestamp`, and (since the lineage fix `ai-agent@8a0078bc`) `originTxnId`, `parentId`, `parentTxnId`. As of that fix the producer **persists** all of them to disk alongside `{version, reason, opTree}` (`persistence.ts:56-61` writes `sessionId, originId, originTxnId, parentId, parentTxnId, timestamp`); they are no longer stripped before gzip. **The v2 adapter ignores these top-level fields by design** — it decodes only `{version, reason, opTree}` (`parser.go:18-44`) and recovers `originId` from the filename and `sessionId`/`timestamp` from inside `opTree` (see below). Reading them is unnecessary for v2's lineage model, which recurses over embedded `operationNode.childSession` values (`parser.go:82`, `mapper_ops.go:237-242`), so the adapter deliberately does not consume the now-persisted envelope lineage fields.

### opTree shape

`opTree` is a `SessionNode` as defined in `ai-agent.git/src/session-tree.ts:8-22` and `ai-agent.git/.agents/sow/specs/optree.md:18-35`. Confirmed key union from 200 random samples:

```json
{
  "id":            "<string>",      // session-tree internal id, NOT a UUID (e.g. "mitrcrkj-g6mp5m")
  "traceId":       "<uuid>",        // = AIAgentSession.txnId. For root sessions == filename UUID
  "agentId":       "<string>",      // agent name (e.g. "comparison-page", "web-research")
  "callPath":      "<string>",      // hierarchical agent path (e.g. "web-research->agent__web-search->web-search")
  "sessionTitle":  "<string>",      // often empty
  "latestStatus":  "<string>",      // optional, set by agent__task_status
  "startedAt":     <int millis>,    // ms-since-epoch from Date.now()
  "endedAt":       <int millis>,    // optional, absent only for sessions that never completed
  "success":       <bool>,          // optional, absent for never-completed sessions
  "error":         "<string>",      // optional, free text error message
  "attributes":    { ... },         // optional, observed empty across samples
  "totals":        { ... },         // see SessionTotals below
  "turns":         [ TurnNode... ],
  "steps":         [ StepNode... ], // v2 only, optional
  "finalReport":   { ... },         // v2 only, optional, see below
  "pluginMetas":   { ... }          // v2 only, optional
}
```

The relationship between `opTree.id`, `opTree.traceId`, and the filename:

| Field | Value | Source |
|---|---|---|
| filename UUID | parent `originTxnId` | `ai-agent.git/src/persistence.ts:59` |
| `opTree.traceId` | this session's `txnId` (== `selfId` if child, == `originTxnId` if root) | `ai-agent.git/src/ai-agent.ts:543` |
| `opTree.id` | `uid()` random string from `SessionTreeBuilder` constructor | `ai-agent.git/src/session-tree.ts` builder |

Therefore, for a ROOT session, `filename UUID == opTree.traceId`. For a CHILD session viewed inside a parent file (`op.childSession.traceId`), the child's traceId is a DIFFERENT UUID. The adapter must use `opTree.traceId` (and, for children, `childSession.traceId`) as the canonical `native_id` for the `sessions` table, NOT the filename.

### TurnNode (`ai-agent.git/src/session-tree.ts:25-31`)

```json
{
  "id":         "<string>",     // turn-tree internal id
  "index":      <int>,          // 0-based: index 0 == "init" turn (system bookkeeping); index >= 1 == real user turns
  "startedAt":  <int millis>,
  "endedAt":    <int millis>,   // optional
  "attributes": { ... },        // optional; system turns carry {system: true, label: "init"}
  "ops":        [ OperationNode... ]
}
```

The spec's claim that `index` is 1-based (optree.md:24) is **inconsistent with on-disk reality**: turn 0 always exists and is the system init turn; user turns begin at 1. The adapter MUST treat `index` as 0-based and either map turn 0 to canonical turn_seq=0 (preferred — exposes init events) or skip turn 0 entirely (acceptable for a leaner timeline). See "Mapping to Canonical Events" for the chosen convention.

### StepNode (v2 only, `ai-agent.git/src/session-tree.ts:42-50`)

```json
{
  "id":         "<string>",
  "index":      <int>,
  "kind":       "system" | "user" | "advisors" | "router_handoff" | "handoff" | "internal",
  "startedAt":  <int millis>,
  "endedAt":    <int millis>,
  "attributes": { ... },
  "ops":        [ OperationNode... ]
}
```

Steps are sibling to turns; both contain ops. Observed `kind` in operator data: `"internal"` (history-compaction summarizer child sessions). Other kinds exist in the producer but are rarer.

### OperationNode (`ai-agent.git/src/session-tree.ts:8-22`)

Verified key union across 200 random samples:

```json
{
  "opId":                "<string>",                      // session-tree internal id
  "kind":                "llm" | "tool" | "session" | "system",  // four observed kinds
  "startedAt":           <int millis>,
  "endedAt":             <int millis>,                    // optional
  "status":              "ok" | "failed",                 // optional, present for completed ops
  "attributes":          { ... },                         // see below
  "logs":                [ LogEntry... ],                 // always present, may be empty
  "accounting":          [ AccountingEntry... ],          // always present, may be empty
  "reasoning":           { ... },                         // optional, llm ops only
  "request":             { kind, payload, size },         // optional
  "response":            { payload, size, truncated },    // optional
  "childSession":        SessionNode,                     // optional, when kind=session (and history-compaction internal step ops)
  "childSessionRef":     { sessionId, originId, ... },    // optional, post-v3 compaction
  "childSessionSummary": { sessionId, turns, steps, ... } // optional, post-v3 compaction
}
```

Observed `kind` values in operator data: `system`, `llm`, `tool`, `session`. The spec's claimed `reasoning` op kind does NOT exist as a standalone kind — reasoning lives as `op.reasoning` on `llm` ops.

Observed `attributes` keys (across 50 random files): `provider`, `model`, `latency`, `isFinalTurn` (llm ops); `name`, `provider`, `kind`, `latency`, `size`, `error` (tool / session ops); `label` (system ops).

Observed `kind: session` op attributes (from a sub-agent sample): `{name: "agent__web-search", provider: "subagent", kind: "agent", latency: 216344, size: 11210}`.

Observed `kind: tool` op attributes: `{name: "<tool_name>", provider: "<provider>", kind: "<tool_kind>", latency, size}`.

Observed `status: failed` op attributes: `{error: "<slug>"}` — e.g. `error: "token_budget_exceeded"`.

#### LogEntry shape

Union of observed keys (50-sample): `agentId, agentPath, callPath, direction, fatal, headendId, llmRequestPayload, llmResponsePayload, max_subturns, max_turns, message, originTxnId, parentTxnId, path, remoteIdentifier, severity, subturn, timestamp, toolKind, turn, turnPath, txnId, type, details`. Mandatory-in-practice: `timestamp` (ms), `severity` (`"VRB"|"DBG"|"INF"|"WRN"|"ERR"`), `message`, `path` (stable bijective op path like `"1-2"` or `"S0-1"`).

#### AccountingEntry shape

Union of observed keys: `agentId, callPath, charactersIn, charactersOut, command, costUsd, error, latency, mcpServer, model, originTxnId, parentTxnId, provider, status, stopReason, timestamp, tokens, txnId, type`.

Two values for `type`:

- `type: "llm"`: carries `provider`, `model`, `tokens: { inputTokens, outputTokens, cacheReadInputTokens?, cacheWriteInputTokens?, cachedTokens?, totalTokens? }`, `costUsd`, `stopReason`, `latency`, `status`.
- `type: "tool"`: carries `mcpServer`, `command`, `charactersIn`, `charactersOut`, `latency`, `status`, optional `error`.

`tokens` has both Anthropic (`cacheReadInputTokens`, `cacheWriteInputTokens`) and OpenAI (`cachedTokens`) cache-token naming in the same data. The adapter normalizes to canonical `tokens_in` (FRESH/uncached input only) plus the separate canonical fields `tokens_cache_read` (= `cacheReadInputTokens + cachedTokens`) and `tokens_cache_write` (= `cacheWriteInputTokens`) — see `mapper_ops.go:115-122`. Per the SOW-0029 token contract, cache is NEVER folded into `tokens_in`.

#### reasoning shape

```json
{
  "chunks":     [],                  // legacy field, new snapshots keep empty
  "final":      "<string>",          // optional, compact reasoning summary
  "chunkCount": <int>,               // optional counter
  "charCount":  <int>                // optional counter
}
```

Legacy snapshots may have non-empty `chunks: [{ text, ts }, ...]`. The adapter passes whichever it finds; `final` is preferred when present (per `ai-agent.git/.agents/sow/specs/snapshots.md:105`).

#### request / response shape

```json
"request":  { "kind": "llm" | "tool", "payload": <any>, "size": <int> }
"response": { "payload": <any>, "size": <int>, "truncated": <bool?> }
```

Payload may be raw object, raw JSON string/blob, or — in newer v2-era snapshots — a nested `EvidencePayloadRef` pointing into `payloads/<sessionId>/...`. Producer-shaped references appear as `payload.ref` for LLM/tool request and response payloads and as `payload.sdk.ref` for SDK request and response captures. A ref descriptor carries the v3 evidence fields (`path`, `format`, `compression`, `originalBytes`, `compressedBytes`, `sha256`, `captured`, `truncated`, `redacted`); `PayloadRefEvent.StoredBytes` maps from `compressedBytes`. The adapter inspects these known ref wrappers and emits a `PayloadRefEvent`. Plain `payload.ref` uses the op kind + side (`llm_request`, `llm_response`, `tool_request`, `tool_response`); `payload.sdk.ref` uses the canonical SDK kinds (`llm_sdk_request`, `llm_sdk_response`). When `captured=true` and `path` is present, `LocationURI` resolves the relative `path` against `<sessions-dir>` and `Compression` is copied from the ref. When `captured=false` or `path` is absent, the event still carries available metadata (`PayloadKind`, `Format`, `OriginalBytes`, `SHA256`) but leaves `LocationURI` and `Compression` empty, mirroring the v3 adapter.

If a request/response side has no producer ref descriptor and carries an inline `payload`, the adapter emits a `PayloadRefEvent` that points back to the exact source fragment inside the original snapshot: `LocationURI=file://<absolute-snapshot>.json.gz?json_pointer=<RFC6901 pointer>`, `Compression=gzip`, and `PayloadKind` from op kind + side (`llm_request`, `llm_response`, `tool_request`, `tool_response`). The selector points at `/opTree/turns/<array-index>/ops/<array-index>/<side>/payload` or the equivalent `steps[]` / nested `childSession` path. JSON strings are `format=text` and hash as `semantic_text`; all other inline JSON values are `format=json` and hash as `canonical_json`. Inline sides with producer refs do not emit an extra inline row for the ref wrapper itself. `truncated=true` remains source-visible partial data and the parity gate must report it as `partial_source`; a canonical row that presents the partial body as complete is wrong.

#### childSession shape (the embedded sub-agent pattern)

`op.childSession` is a full nested `SessionNode` with its own `traceId`, `agentId`, `callPath`, `startedAt`, `endedAt`, `success`, `totals`, `turns[]`, `steps[]`. Verified empirically: across 31 child sessions inside one parent file (`<parent-traceId>.json.gz`), **none** of the child `traceId`s exist as their own `<traceId>.json.gz` file at the root. **In v2, sub-agent sessions are NOT independently persisted.** They live exclusively inside the parent's snapshot. The adapter MUST recursively walk `childSession` to extract all sub-agent events; it cannot rely on a separate file for the child.

`childSessionRef` / `childSessionSummary`: these post-v3-migration compaction artifacts appear in newer snapshots when the producer was already on the v3 evidence path but still wrote a legacy snapshot for compatibility. Observed in **0** of 50 random samples on the operator's disk, indicating they are rare in this particular dataset (most data predates the v3 migration). When present, the ref's `sessionId` should be looked up either in the v3 ledger (out of scope for this adapter) or in another v2 file (also rare). The adapter emits `OpStartedEvent(kind=session, ChildSessionNativeID=ref.sessionId)` and records the opaque `childSessionSummary` in the started-event extras for diagnostic visibility; the matching `OpFinalizedEvent` is derived from the session op's own timing/status, with no recursive descent.

### SessionTotals shape

```json
{
  "tokensIn":         <int>,
  "tokensOut":        <int>,
  "tokensCacheRead":  <int>,
  "tokensCacheWrite": <int>,
  "costUsd":          <float>,
  "toolsRun":         <int>,
  "agentsRun":        <int>,
  "turnsUsed":        <int?>,
  "turnsFailed":      <int?>,
  "toolsInvalid":     <int?>,
  "finalFailed":      <int?>,
  "toolsFixed":       <int?>,
  "finalFixed":       <int?>,
  "finalForced":      <int?>
}
```

Defined at `ai-agent.git/src/session-tree.ts:66-82`. `agentsRun` counts the root session itself plus all descendants. The adapter SHOULD NOT emit totals as canonical events — totals are reconstructed by the ingester from individual ops. It MAY persist them on the canonical `sessions` row's `extras_json` for cross-check at QA time.

### finalReport / pluginMetas

`finalReport` (when present) is the final user-facing report payload synced by `setSessionFinalArtifacts` (`ai-agent.git/src/session-tree.ts:122`). `pluginMetas` is keyed by plugin name, each entry a shallow record. Both are optional in v2 snapshots and absent from v1. The adapter stores both in `sessions.extras_json` as opaque JSON; no canonical event maps to them today.

## Mapping to Canonical Events

The translation is harder than v3 because the v2 file is a **snapshot of full state at one moment**, not an append-only ledger. The adapter therefore behaves as a deterministic projection: for each file scan, walk the full opTree and emit deterministic canonical events for sessions, turns, steps, ops, payload refs, logs, and terminal states. Replaying the same file produces the same events; the ingester is idempotent at the SQL layer (every table upserts on a natural identity, so re-emitted rows never duplicate — see `ingester.md` §Dedup and Idempotency and SOW-0015). The deterministic `SourceSeq` is a stable per-emitted-event identifier and an observability counter, **not** a dedup gate.

### Conventions

- Timestamps: opTree `startedAt`/`endedAt` are milliseconds. Canonical `Ts`/`EndTs` are microseconds. Multiply by 1000.
- `SourceID`: a stable identifier for this v2 source, e.g. `aiagent_v2:/home/<user>/.ai-agent/sessions`.
- `SourceSeq`: a stable per-emitted-event identifier (deterministic across rescans), NOT monotonic per source and NOT a dedup gate — see `ingester.md` §Dedup and Idempotency. Strategy: compute `fnv64a(<originId> + NUL + <eventPath>) & 0x7fffffffffffffff`, where `eventPath` is the adapter's emitted path string rooted at the current session `traceId` and including the event suffix, e.g. `<traceId>::start`, `<traceId>::T:1::O:0:<opId>::start`, `<traceId>::S:0::kind`, `<childTraceId>::T:1::end`. FNV-64a is used to keep the adapter stdlib-only; the sign bit is masked so downstream int64 conversions stay positive.
- Stable native IDs:
  - Session native_id = `opTree.traceId` (root) or `childSession.traceId` (sub-agent).
  - Turn native_id = `<session.traceId>:T:<turn.index>`.
  - Step native_id = `<session.traceId>:S:<step.index>`.
  - Op native_id = `op.opId` (already unique within the tree, per `SessionTreeBuilder.uid()`).

### Per-node emission

| Source structure | Canonical events emitted | Notes |
|---|---|---|
| `opTree` (the file's session) | `SessionStartedEvent(NativeID=traceId, ParentNativeID="", Kind="root", AgentName=agentId, Model=<from first llm op when present>, Extras={callPath, sessionTitle, filename})` | One per file; emitted on every scan; upsert by ingester. Model discovery uses the same child-session depth cap as event emission; over-cap subtrees are not searched and cannot influence parent model metadata. |
| `opTree` with `endedAt` set | `SessionFinalizedEvent(Status=completed when success=true, failed when success=false, interrupted when success is absent, ErrorMessage=opTree.error, EndTs=endedAt)` | Only when `endedAt` exists |
| `opTree.turns[i]` (any i including 0) | `TurnStartedEvent(Seq=index)` + (if `endedAt`) `TurnFinalizedEvent(Seq=index, Status=derived, EndTs=endedAt, TokensIn/Out=sum-of-llm-accounting-in-ops, CostUSD=sum)` | Turn 0 (init) is preserved as canonical turn 0 — operators can hide it in UI but the spec keeps it for fidelity |
| `opTree.steps[i]` | `TurnStartedEvent(Seq=encode(step.index, kind))` + `TurnFinalizedEvent` | Encoded so step seqs do not collide with turn seqs, e.g. `seq = 10000 + step.index`; step kind is recorded in session extras as `step.<index>.kind` |
| `op` with `kind="llm"` | `OpStartedEvent(Kind="llm", Name=attributes.name, Model=attributes.model, Provider=attributes.provider, ParentOpSeq=-1)` + `OpFinalizedEvent(TokensIn/Out from accounting[0].tokens, CostUSD from accounting[0].costUsd, Status, ErrorClass=attributes.error, BytesIn=request.size, BytesOut=response.size)` | When op has `reasoning.final`, emit a nested `OpStartedEvent(Kind="reasoning", ParentOpSeq=<llm op's seq>)` and matching `OpFinalizedEvent` so the reasoning span nests under the llm op. The reasoning op MUST have its own deterministic `Seq` that does not collide with any source op in the same turn/step; allocate synthetic reasoning seqs after the source-op index range in traversal order. The summary text is carried in the reasoning op extras. |
| `op` with `kind="tool"` | `OpStartedEvent(Kind="tool", Name=attributes.name, ToolNamespace=attributes.provider)` + `OpFinalizedEvent` | `CharsIn=charactersIn`, `CharsOut=charactersOut`, no tokens unless tool emits them; request/response `size` still maps to `BytesIn`/`BytesOut` when present. |
| `op` with `kind="session"` (embedded sub-agent) | `OpStartedEvent(Kind="session", Name=attributes.name, ChildSessionNativeID=childSession.traceId)` + `OpFinalizedEvent`, then RECURSE into `childSession` and emit its full event stream (SessionStarted, turns, ops, etc., with `ParentNativeID = parent traceId`) | The recursion is breadth-unbounded but depth-capped at 32 nested child-session levels. Over-cap descendants emit a `SourceErrorEvent` and are skipped consistently by both event emission and model discovery. Embedded child `SessionStartedEvent.Extras.version` is `0`; the snapshot `version` belongs to the root file envelope, not each child subtree. |
| `op` with `kind="system"` | `OpStartedEvent(Kind="system", Name=attributes.name, falling back to attributes.label when name is empty, Extras.original_kind="system")` + `OpFinalizedEvent` | System ops contain logs only; ingested for log surfacing and filterable through the first-class canonical system kind |
| `op.logs[k]` | `LogEntryEvent(Severity, Source="aiagent_v2", Message, Ts=log.timestamp*1000, op_id_linkage)` | One event per log line |
| `op.request.payload.ref`, `op.response.payload.ref`, `op.request.payload.sdk.ref`, or `op.response.payload.sdk.ref` (when present) | One `PayloadRefEvent(PayloadKind, Format, Compression, LocationURI=file://<sessions-dir>/<ref.path> when captured/pathful, OriginalBytes, StoredBytes=ref.compressedBytes)` per present ref descriptor | A single side may carry both the regular ref and the SDK ref because the producer deep-merges payload updates. Emit both independently. A malformed/path-escaping ref emits `SourceErrorEvent` and skips only that ref; any sibling ref on the same side is still emitted. Regular request/response payload refs keep the historical event path `::payload:<side>`; SDK refs use `::payload:<side>:sdk` so `SourceSeq` stays unique when both are present. |
| `op.request.payload`, `op.response.payload` inline value with no ref descriptor | One `PayloadRefEvent(PayloadKind, Format, Compression=gzip, LocationURI=file://<snapshot>.json.gz?json_pointer=<pointer>)` per inline side | The event never copies raw payload bytes into SQLite. The snapshot selector is exact enough for canonical parity to resolve and hash only the logical payload fragment. JSON strings hash as semantic text; objects/arrays/numbers/bools/null hash as canonical JSON. |
| `op.childSessionRef` (no `childSession`) | `OpStartedEvent(Kind="session", ChildSessionNativeID=ref.sessionId, Extras.childSessionSummary=<opaque summary>)` + `OpFinalizedEvent(from the session op's timing/status)` | No recursion; child is found via another file/ledger |
| `op.status == "failed"` | `OpFinalizedEvent(Status="failed", ErrorClass=attributes.error)` | Plus `LogEntryEvent(severity="ERR")` with attributes.error message when present; no synthetic error fallback is injected |
| Periodic checkpoint after batch | `SourceProgressEvent(Cursor=...)` | See Cursor below |
| Parse error on a file | `SourceErrorEvent(Path, Message)` | Increments `sources.parse_errors`, does not block other files |

## Source Manifest Parity

The SOW-0097 parity gate has an independent aiagent_v2 source extractor under
`internal/parity`. It reads root-level `<sessions-dir>/*.json.gz` snapshots
directly and emits source artifacts without calling the aiagent_v2 canonical
mapper. It ignores producer temp files matching `*.json.gz.tmp-*` and all
subdirectories; v3 owns `session/` and `payloads/` traversal.

Snapshot reads are bounded with the parity resolver's default 1 GiB
per-artifact safety cap. The source extractor rejects a snapshot whose
compressed file size is over the cap before opening gzip, and rejects a snapshot
whose decompressed JSON stream exceeds the cap before JSON decode. Either case
is a source-extractor error, so `check-parity` reports the source as
`INCOMPLETE`; it is never a passing manifest built from partial or unbounded
snapshot bytes.

Recoverable per-file snapshot corruption does not stop the source extractor
from checking later snapshots. A zero-byte `.json.gz`, invalid gzip stream, or
malformed/decode-invalid snapshot JSON emits a `source_corruption` artifact with
`availability=source_corrupt`, a file selector, raw byte length/hash over the
rejected compressed snapshot bytes, and a typed integrity failure. The source is
still `INCOMPLETE`, never `PASS`, but the diagnostic is machine-countable and
one corrupt snapshot cannot hide parity evidence from subsequent snapshots.

The extractor emits:

- `session_boundary` from every `opTree` node, including embedded
  `childSession` subtrees. Session native id is `traceId`; root id is the root
  file's `opTree.traceId`; embedded children use `kind=sub_agent` and
  `parent_native_session_id` equal to the containing session.
- `turn_boundary` from every `turns[]` item using the source `index` as
  canonical turn seq, and from every `steps[]` item using
  `10000 + step.index`, matching the adapter's projection.
- `op_boundary` from every operation in traversal order. Op seq is the
  operation's index within its containing turn/step, with synthetic reasoning
  op seqs allocated after the source-op range exactly like the adapter.
  Status maps `ok -> completed`, `failed -> failed`, and missing status to
  `running`.
- `reasoning_text` from every LLM op whose `reasoning.final` string is
  non-empty. The source artifact uses the synthetic reasoning op's native id
  plus `:reasoning.final`, `hash_domain=semantic_text`, and
  `field_path=reasoning.final`. Canonical proves the same text from the
  reasoning op's `ops.extras_json["reasoning.final"]`; dropping the text while
  keeping only the reasoning op boundary is a parity failure.
- `assistant_message` from every `opTree.finalReport` object when present. The
  artifact is session-scoped, uses `hash_domain=canonical_json`, and uses
  `field_path=finalReport`. Canonical proves the same JSON from
  `sessions.extras_json["final_report"]`; a session that keeps the boundary but
  drops the final user-facing report is a parity failure.
- `session_metadata` from every `opTree` node whose session-level metadata is
  source-visible beyond the boundary itself. The identity covers `traceId`,
  filename-derived `originId`, `version`, `id`, `agentId`, `callPath`,
  `sessionTitle`, `latestStatus`, and canonical JSON hashes for optional
  `attributes`, `totals`, and `pluginMetas`. Canonical proves the same identity
  from first-class `sessions` columns plus `sessions.extras_json` keys
  `originId`, `version`, `nodeId`, `sessionTitle`, `latestStatus`,
  `attributes`, `totals`, and `plugin_metas`; dropping any source-visible
  session metadata is a parity failure.
- `subagent_link` for every `kind=session` op with embedded `childSession` or
  pathless `childSessionRef`, keyed by parent turn/op seq and child native id.
- `llm_error` / `tool_error` for failed ops whose `attributes.error` string is
  present. The parity identity stores the error-message hash, not the raw text.
- `system_op` for every source op with `kind="system"`. The artifact is an
  `identity_json` proof of the canonical `ops.kind=system` row, with source
  kind, canonical name, status, and timestamps derived from the same fields as
  `op_boundary`. The native id is `op:<turn_seq>:<op_seq>:system`; the ordinary
  `op_boundary` artifact for the same op remains present.
- `compaction_event` for every `steps[]` item with `kind="internal"` that
  contains a `kind="session"` operation whose provider attribute is
  `history-compaction`. The identity covers native session id, step-projected
  turn seq, op seq, trigger, step kind, op name/provider, child native session
  id when present, archived/current turn, status, and timestamps.
  `archivedTurn` and `currentTurn` are read from op attributes first and fall
  back to step attributes. Canonical proves the same identity from the session
  op row plus `ops.extras_json`: compaction proof attributes are stored as
  `attr.<key>`, and step metadata for step-projected ops is stored as
  `step.kind` plus `step.attr.<key>`.
- `log_entry` for source-backed log text:
  - every `op.logs[k].message`, keyed by
    `file:<snapshot-basename>:/opTree/.../ops/<i>/logs/<k>/message`;
  - every failed op `attributes.error`, keyed by
    `file:<snapshot-basename>:/opTree/.../ops/<i>/attributes/error`;
  - every failed session `opTree.error`, including embedded child sessions,
    keyed by `file:<snapshot-basename>:/opTree/.../error`.
  Each artifact uses the source snapshot file URI plus the exact JSON pointer,
  `hash_domain=semantic_text`, and `availability=source_empty` when the source
  field is present but empty. Canonical proves the same artifact from
  `log_entries` rows carrying `extras_json.aiViewer.parity` with the same
  native artifact id, selector URI, and JSON pointer.
- payload artifacts from producer ref descriptors under
  `request.payload.ref`, `response.payload.ref`, `request.payload.sdk.ref`, and
  `response.payload.sdk.ref`. Captured refs resolve under the configured
  sessions root, enforce the parity payload-file safety cap before materializing
  compressed bytes and again after decompression, decompress gzip before hashing,
  compare producer `originalBytes`, `compressedBytes`, and `sha256` when
  present, mark mismatches as `availability=source_corrupt` with typed
  `integrity_failures[]` entries for every failed proof field, and map to parity
  classes `llm_request`, `llm_response`, `llm_sdk_request`,
  `llm_sdk_response`, `tool_request`, and `tool_response`.
- uncaptured or pathless producer refs as `availability=source_unavailable`
  with the same stable ordinal native artifact id used by the canonical
  extractor, so metadata-only canonical `PayloadRefEvent` rows are still
  matched explicitly.
- legacy inline request/response payload bodies when no producer ref descriptor
  exists on that side. The source artifact uses the source snapshot file selector
  plus RFC 6901 JSON pointer into `opTree`, and maps the side to `llm_request`,
  `llm_response`, `tool_request`, or `tool_response`. JSON strings use
  `hash_domain=semantic_text`; all other JSON values use
  `hash_domain=canonical_json`. Truncated inline payloads are
  `availability=partial_source`, not `source_unavailable`.
- No `user_prompt` or `user_image` artifacts. The v2 snapshot format does not
  persist a separate user-message stream; user-authored text or image-bearing
  JSON can only be present inside request payload bodies. The parity gate proves
  those bytes through `llm_request` / `tool_request` artifacts. Emitting an
  additional `user_prompt` or `user_image` artifact from the same request body
  would double-count one source artifact as two logical classes.

There is no separate aiagent_v2 attachment artifact. Upstream `SessionNode`,
`TurnNode`, `StepNode`, and `OperationNode` have no attachment field; file
paths, images, or other attachment-like values can only appear inside
request/response payload JSON. Those bytes are covered by payload artifact
classes, so `attachment_metadata` is `not_source_visible`.

Machine-readable matrix rows:

| Class | Source availability | Hash domain | Canonical representation | Selector / identity rule | Evidence |
|---|---|---|---|---|---|
| `session_boundary` | `available` | `identity_json` | `sessions` row | `session:<traceId>` | Source Manifest Parity bullets above. |
| `turn_boundary` | `available` | `identity_json` | `turns` row | `turn:<turn.index>` or `turn:<10000+step.index>` | Source Manifest Parity bullets above. |
| `op_boundary` | `available` | `identity_json` | `ops` row | `op:<turn_seq>:<op_seq>` | Source Manifest Parity bullets above. |
| `user_prompt` | `not_source_visible` | n/a | none | source format has no separate user-message artifact; prompt text is inside request payload artifacts | Source Manifest Parity bullets above. |
| `user_image` | `not_source_visible` | n/a | none | source format has no separate user-image artifact; image-bearing JSON is inside request payload artifacts | Source Manifest Parity bullets above. |
| `assistant_message` | `available` | `canonical_json` | `sessions.extras_json.final_report` | `session:<traceId>:final_report` with `field_path=finalReport` | Source Manifest Parity bullets above. |
| `reasoning_text` | `available` | `semantic_text` | reasoning op `ops.extras_json.reasoning.final` | `op:<turn_seq>:<op_seq>:reasoning.final` | Source Manifest Parity bullets above. |
| `llm_request` | `available` / `source_unavailable` / `partial_source` | `raw_bytes` / `canonical_json` / `semantic_text` | `payload_refs.kind=llm_request` | producer ref path, metadata-only payload ordinal, or inline snapshot JSON pointer | Source Manifest Parity bullets above. |
| `llm_response` | `available` / `source_unavailable` / `partial_source` | `raw_bytes` / `canonical_json` / `semantic_text` | `payload_refs.kind=llm_response` | producer ref path, metadata-only payload ordinal, or inline snapshot JSON pointer | Source Manifest Parity bullets above. |
| `llm_sdk_request` | `available` / `source_unavailable` | `raw_bytes` | `payload_refs.kind=llm_sdk_request` | producer SDK ref path or metadata-only payload ordinal | Source Manifest Parity bullets above. |
| `llm_sdk_response` | `available` / `source_unavailable` | `raw_bytes` | `payload_refs.kind=llm_sdk_response` | producer SDK ref path or metadata-only payload ordinal | Source Manifest Parity bullets above. |
| `tool_request` | `available` / `source_unavailable` / `partial_source` | `raw_bytes` / `canonical_json` / `semantic_text` | `payload_refs.kind=tool_request` | producer ref path, metadata-only payload ordinal, or inline snapshot JSON pointer | Source Manifest Parity bullets above. |
| `tool_response` | `available` / `source_unavailable` / `partial_source` | `raw_bytes` / `canonical_json` / `semantic_text` | `payload_refs.kind=tool_response` | producer ref path, metadata-only payload ordinal, or inline snapshot JSON pointer | Source Manifest Parity bullets above. |
| `llm_error` | `available` | `identity_json` | failed LLM `ops` row | `op:<turn_seq>:<op_seq>:error` | Source Manifest Parity bullets above. |
| `tool_error` | `available` | `identity_json` | failed tool/session `ops` row | `op:<turn_seq>:<op_seq>:error` | Source Manifest Parity bullets above. |
| `subagent_link` | `available` | `identity_json` | parent session op with child link | `op:<turn_seq>:<op_seq>:child_session:<child_traceId>` | Source Manifest Parity bullets above. |
| `system_op` | `available` | `identity_json` | `ops.kind=system` row | `op:<turn_seq>:<op_seq>:system` | Source Manifest Parity bullets above. |
| `compaction_event` | `available` | `identity_json` | history-compaction step session op row plus `ops.extras_json` | `op:<turn_seq>:<op_seq>:compaction`; identity includes trigger, step kind, name/provider, child session id, archived/current turn, status, and timestamps | Source Manifest Parity bullets above; upstream `session-orchestration-steps.ts` creates `internal` history-compaction steps. |
| `session_metadata` | `available` | `identity_json` | `sessions` row plus `sessions.extras_json` | `session:<traceId>:metadata`; includes canonical JSON hashes for `attributes`, `totals`, and `pluginMetas` / `plugin_metas` | Source Manifest Parity bullets above. |
| `log_entry` | `available` / `source_empty` | `semantic_text` | `log_entries` rows with `extras_json.aiViewer.parity` | source snapshot file URI plus JSON pointer; native id `file:<snapshot-basename>:<json_pointer>` | Source Manifest Parity bullets above. |
| `attachment_metadata` | `not_source_visible` | n/a | none | v2 `opTree` has no separate attachment artifact; attachment-like values are payload JSON covered by request/response payload artifacts | Source Manifest Parity bullets above; upstream `SessionNode`, `TurnNode`, `StepNode`, and `OperationNode` have no attachment field. |
| `patch_metadata` | `not_source_visible` | n/a | none | v2 `opTree` has no separate patch/file-change metadata artifact | Source Manifest Parity bullets above; upstream operation records have payload refs, logs, and attributes, not Opencode-style patch part records. |

### Ordering

Within a single file scan, the adapter emits events depth-first in opTree order: SessionStarted → Turn0Started → Turn0Op0Started/Finalized → … → TurnNFinalized → SessionFinalized. Child sessions are emitted in-place at the point of their parent `kind="session"` op. This satisfies the canonical-events.md ordering guarantee that turns within a session are chronological.

## Watch Strategy

- `fsnotify.NewWatcher()`, `watcher.Add(<sessions-dir>)`. **Non-recursive**: do NOT add `session/` or `payloads/`.
- React to:
  - `fsnotify.Create` on `*.json.gz` (new session file just renamed in).
  - `fsnotify.Write` on `*.json.gz` (unlikely with atomic-rename producer, but defensive — older writers may not have used the temp+rename pattern).
  - `fsnotify.Rename` on `*.json.gz` (target side; primary signal because the producer ALWAYS renames into place — `persistence.ts:62`).
- Ignore:
  - `*.json.gz.tmp-*` (the producer's temp files).
  - Any path that descends into `session/` or `payloads/` (the v3 adapter owns those).
  - Hidden files and dotfiles.
- Debouncing: an active session can rewrite its `.json.gz` many times during one human turn (every sub-agent completion triggers `persistSessionSnapshot('subagent_finish')`). The adapter MUST coalesce: maintain a per-file `pendingScan` flag; on fsnotify event, set the flag; a worker pool drains the flag at most once per `debounceWindow` (proposed: 500 ms). Multiple events during the window collapse into one scan. The scan reads mtime first, then opens; if the file's mtime changes during read, re-read ONCE (not in a loop) and accept the second read.
- Initial backfill on startup: enumerate the directory once, queue every `*.json.gz` (skipping tmp suffixes), process in worker pool with cursor-aware skip.

## Cursor

The cursor's purpose is to make a restart cheap: known-unchanged files MUST be skipped without opening them. Because v2 rewrites the entire file on each snapshot, the adapter cannot use byte offsets; it tracks file identity via `(mtime, size, contentHash)`.

```json
{
  "version": 1,
  "files": {
    "<originId>.json.gz": {
      "mtime_us":    <int>,
      "size":        <int>,
      "content_sha": "<hex64>"
    }
  }
}
```

Skip logic on backfill or watch-driven scan:

1. Stat the file. If size == 0, emit `SourceError` (cite path, "empty file") and skip.
2. If cursor has an entry AND cursor.mtime_us == stat.mtime_us AND cursor.size == stat.size → skip.
3. Otherwise read first 64 KiB and compute `content_sha`. If cursor entry exists AND `content_sha` matches → update cursor mtime/size, skip re-emission.
4. Otherwise: re-parse, emit all events (the ingester's SQL-layer idempotent upserts absorb duplicates), then write the new cursor row.

Because event `SourceSeq` is computed deterministically from `(originId, eventPath)`, replaying produces the SAME `SourceSeq` values as the original emission. Re-emitted events upsert onto the same canonical rows (every writer table has a natural-identity key with `ON CONFLICT`), so duplicates collapse at the SQL layer — there is no per-source high-water-mark gate (a scalar HWM is incompatible with one `sourceID` aggregating many per-file-sequenced files; see `ingester.md` §Dedup and Idempotency and SOW-0015). Therefore the adapter is safe to re-emit every event on every scan; this is the deliberate trade-off for v2's snapshot-not-ledger nature.

Cursor checkpointing: the adapter emits a `SourceProgressEvent` every N files during backfill (proposed N=1000) and after each watch-driven scan. The ingester persists `cursor` into the `sources` table per `data-model.md`.

## Sub-Agent Linkage

In v2, children are **embedded** in the parent's snapshot via `op.childSession` (a full nested `SessionNode`). They are NOT independently persisted at the root — verified by checking 31 child traceIds from one parent file: 0 of 31 had their own `<traceId>.json.gz` file (cited under Edge Cases).

The adapter therefore:

1. Walks `opTree.turns[].ops[]` and `opTree.steps[].ops[]` looking for `op.kind === "session"` and `op.childSession` populated.
2. For each such op: emits `OpStartedEvent(Kind="session", ChildSessionNativeID=childSession.traceId)` + `OpFinalizedEvent`, then recursively emits the child's `SessionStartedEvent`, its turns, its ops, etc., with `ParentNativeID = parent traceId` and stable `SourceSeq` derived from child event paths rooted at `childSession.traceId`.
3. The ingester sets the canonical `sessions.parent_session_id` and `sessions.root_session_id` for the child via the canonical hash of `(source_id, native_id)` of the parent and the file's root traceId.

If a future v2 file contains `childSessionRef` (without `childSession`) — produced when the v3 path was active but a v2 snapshot was still being written for compatibility — the adapter emits the session-kind op but does NOT recurse. The ingester records the link by native_id and reconciles when the referenced session is ingested from elsewhere. In the operator's current dataset this is rare (0/50 random samples). The adapter MUST be defensive: a `childSessionRef` whose `sessionId` is never observed elsewhere is fine — the child session row remains a stub (`status: 'unknown'`).

If the same child traceId appears BOTH embedded in a parent AND as its own file (theoretical — never observed in operator data), the adapter MUST emit child events from both passes; the ingester's SQL-layer idempotent upserts absorb duplicates because `SourceSeq` is computed from `(originId-of-the-file-being-scanned, eventPath-in-that-file)`. Note: the SAME canonical session row is updated from both passes — `sessions.native_id` is the child's traceId, identical in both emissions, and the ingester upserts by `(source_id, native_id)`.

## Edge Cases

1. **Empty (zero-byte) `.json.gz` files.** Verified count: **29 of 294,316** files are 0 bytes. Cause: producer crashed between `writeFileSync` (which created the temp file) and the gzip data being flushed, then the temp was renamed by another producer's later attempt. The adapter MUST detect zero-byte files via stat (no gzip decompression attempted) and emit `SourceError` with explanatory message; record file in cursor as "skipped, zero bytes" so it is not retried until the file is modified. The parity source extractor MUST emit a typed `source_corruption` artifact for the file and continue scanning later snapshots, so one corrupt snapshot cannot suppress live parity evidence for the rest of the source tree.

2. **Orphaned `.tmp-<pid>-<ts>` files.** Verified count on operator's disk: 2 files (`143f3e6c-...json.gz.tmp-702094-1768162665628`, 13 KB; `e48f9399-...json.gz.tmp-702094-1768162665636`, 0 bytes). Producer never cleans these. The adapter MUST ignore them by suffix match (`*.json.gz.tmp-*`); never include them in scans.

3. **Active session being rewritten while we read.** An active root session whose sub-agents are running rewrites its `.json.gz` many times per turn (every `subagent_finish` plus every child's own `final`). Pattern: open file → stream-decompress → during decompression, mtime advances; the next fsnotify event will fire. The adapter MUST tolerate this:
   - Read with `os.Open` and a streaming gzip + json decoder (no `ioutil.ReadFile`).
   - After successful parse, re-stat. If mtime advanced, schedule one additional debounced scan; do NOT loop.
   - The atomic-rename means an in-flight rename never produces a partial file at the final path: either the OLD complete file is visible (if rename hasn't happened) or the NEW complete file is visible. Partial gzip should be extremely rare; if observed, emit `SourceError` and retry once.

4. **Very old v1 snapshots.** v1 (no `steps[]`, no `finalReport`, no `pluginMetas`) is ~60% of disk data. Handle by treating those fields as optional; no schema differences inside ops or turns.

5. **Sessions interrupted mid-turn (no `final` snapshot).** Producer writes `subagent_finish` snapshots during a session; if the process is killed before the `final` snapshot, the disk file is whatever the last intermediate write produced. Such a session has `opTree.endedAt` and `opTree.success` UNSET. The adapter:
   - Emits `SessionStartedEvent` only (no `SessionFinalizedEvent`).
   - The canonical row carries `status: 'running'` (or `'unknown'` if there is reason to suspect the producer is no longer alive).
   - Turns whose `endedAt` is unset emit `TurnStartedEvent` only.
   - Ops whose `endedAt` is unset emit `OpStartedEvent` plus `OpFinalizedEvent(Status="running", EndTs=<startedAt fallback>)` so the current op state is visible.
   - The ingester's `running` rows are eligible for promotion to `completed`/`failed` on a later re-scan of the same file (if the producer eventually writes `final`).

6. **Snapshots that already reference v3 payloads.** When `op.request.payload.ref`, `op.response.payload.ref`, `op.request.payload.sdk.ref`, or `op.response.payload.sdk.ref` is populated and captured with a path, the payload bytes live under `<sessions-dir>/payloads/<sessionId>/...`. The v2 adapter emits `PayloadRefEvent` with `LocationURI = "file://<absolute-path>"`; the file content is read on demand by the presenter, not by the adapter. If the payload file is missing (the migration left a stale ref), the adapter still emits the event; the presenter handles the missing-file UI. Uncaptured/pathless refs still emit metadata-only `PayloadRefEvent` rows with empty `LocationURI`.

7. **Snapshots > 100 MB compressed.** The largest file on disk is 151,881,088 bytes (151 MB compressed); decompressed it may exceed 1 GiB. The adapter MUST stream:
   - `gzip.NewReader(file)` not `gzip.Decompress(ioutil.ReadAll)`.
   - JSON decode via `encoding/json.Decoder.Decode` over the streaming reader.
   - DO NOT load the entire JSON object into memory. Walk `opTree.turns[]` and `opTree.steps[]` and their nested `op.childSession.turns[]` via a streaming SAX-like decoder when the file size exceeds a configurable threshold (proposed: 50 MB compressed). For smaller files, full-object decode is acceptable.
   - The largest files dominate decompression cost but are a small fraction of files (p99 = 1.2 MB compressed; max is the only extreme outlier). The streaming path is required for correctness under memory pressure, not for throughput.

8. **Mixed agent versions in flight.** A single `<sessions-dir>` may contain files written by producers running different ai-agent versions concurrently (e.g. one process pre-v3 evidence, another post). The adapter MUST tolerate any `version` value (currently `1` or `2`); reject only when `version` is missing or not an integer (emit `SourceError`).

9. **Filename whose UUID does not match `opTree.traceId`.** Possible if a future producer writes child sessions to their own files. The adapter trusts `opTree.traceId` as the canonical `native_id`; the filename is decoration. Mismatch is recorded in `extras_json.filename_originid_mismatch` for diagnostic visibility, not an error.

10. **`payload.ref` / `payload.sdk.ref` paths escaping `<sessions-dir>`.** A malformed ref like `path: "../../../../etc/passwd"` would resolve outside the sessions root. The adapter MUST validate that the resolved absolute path is a descendant of `<sessions-dir>`; otherwise emit `SourceError` and skip the ref. This mirrors `readEvidencePayload` (`ai-agent.git/src/evidence/reader.ts`) which rejects path-escaping refs.

11. **Concurrent shared-filename writes (root + descendants).** Because all descendants of a root session share the same filename, multiple producer processes (or one process with concurrent sub-agents) may racingly rename to the same final path. POSIX rename is atomic, so partial reads are impossible; the adapter just sees a different snapshot on each event. The adapter must accept that the file's CONTENT may flip between "child-only opTree" and "parent-with-children opTree" depending on which write was most recent — the parent's final write typically wins, but during execution the file may transiently show a child subtree. Re-scanning emits all events; the ingester's SQL-layer idempotent upserts (keyed on natural identity) ensure no duplication. Per-session canonical rows converge to the final state once the parent's `final` snapshot lands.

## Performance Considerations

### Sizing reality

- 294,316 files at root.
- ~25.4 GB compressed total; mean 92 KB; median 10 KB; p99 1.2 MB; max 151 MB.
- Workstation: 12th Gen i9-12900K, 16 logical CPUs.

### Single-thread benchmark

Direct Python `gzip + json.loads` over 200 random files: 887 files/s/thread, 40 MB/s compressed-throughput, 160 MB/s decompressed-throughput. Extrapolated single-thread total for 294,316 files: **5.5 minutes**.

### Parallelization

Go's `compress/gzip` is ~1.4× faster than Python; structured `encoding/json` is similar. The adapter uses a bounded worker pool (proposed: `runtime.NumCPU()` or 8, whichever is smaller) processing files in parallel:

- 8 workers, ideal parallelism: ~0.7 min wall.
- Realistic with I/O serialization and SQLite write contention: 5-10 minutes wall.

**The 60-minute backfill gate is not at risk for v2.** The risk surface is elsewhere: SQLite write throughput, not adapter parsing.

### Memory

- Small files (p50): fully decoded in memory, ~64 KB heap per file. With 8 workers, ≤ 1 MB resident.
- Large files (p99 = 1.2 MB compressed → ~5 MB decoded): ≤ 40 MB across 8 workers.
- The 151 MB compressed outlier (~1+ GB decoded): MUST stream — `json.Decoder.Token()` over `gzip.NewReader(file)`. The adapter's worker that hits this file allocates more heap; other workers are unaffected. Proposed limit: workers detect compressed size > 50 MB and serialize through a single "large file" lane to bound peak memory.

### Backfill checkpointing

Emit `SourceProgressEvent` after every 1000 files processed. On restart, the cursor's `files` map skips already-processed files in O(1) (Bloom/hash-keyed lookup). The cursor is small: ~80 bytes per file × 294K files ≈ 23 MB JSON. Storing in `sources.cursor` as a single JSON blob is acceptable for v1; if the cursor grows beyond ~100 MB, the adapter should switch to a side table.

### Incremental updates

Post-backfill, fsnotify drives only changed files. Steady-state load is ≤ tens of events per second across all active sessions, trivially within budget.

## Canonical Model Gaps

These are v2 concepts that do not fit cleanly into `canonical-events.md` and `data-model.md`. The adapter records each one through the settled projection below.

1. **0-based init turn (turn 0).** v2 has a turn 0 with `attributes.system: true`. The adapter preserves the source turn index, so init turn events use canonical `turns.seq = 0` for fidelity.
2. **Steps as a sibling of turns.** v2 has `opTree.steps[]` for orchestration (advisors/router/handoff) and history-compaction (internal). Canonical has only `turns`. The adapter projects steps onto turn seqs with a reserved offset (`step_seq = 10000 + step.index`), records step kind in session extras as `step.<index>.kind`, and copies the compaction proof step metadata onto every history-compaction step session op as `ops.extras_json.step.kind` plus `ops.extras_json.step.attr.<key>`; adding a dedicated `steps` table would be a larger future surface change.
3. **`system` op kind.** Canonical `OpKind` has a first-class `system` value. v2 uses `system` for the init/fin housekeeping ops. The adapter maps `system → system` and stores the original kind in `extras_json.original_kind = "system"` for source fidelity.
4. **Accounting type `tool` carrying `charactersIn`/`charactersOut`.** Canonical `OpFinalized` has dedicated `CharsIn`/`CharsOut` fields for sources that report UTF-8 character counts instead of bytes. The adapter maps tool accounting `charactersIn → chars_in` and `charactersOut → chars_out`; request/response `size` still maps to `BytesIn`/`BytesOut` when present.
5. **`opTree.totals` denormalization.** v2 carries pre-computed totals on the session node. Canonical computes totals server-side from ops. The adapter does NOT emit a canonical event from totals (avoids double-counting) and keeps the original totals in `sessions.extras_json.totals` for QA cross-check.
6. **`finalReport` payload.** A potentially-large structured object containing the agent's final user-facing report. The adapter stores it in `sessions.extras_json.final_report`; there is no separate `final_reports` table in v1. SOW-0097 parity treats this as a session-scoped `assistant_message` artifact and proves its canonical JSON hash/length with selector `field_path=finalReport`.
7. **`pluginMetas`.** Per-plugin final metadata. Same situation as `finalReport`; stored in `sessions.extras_json.plugin_metas`.
8. **`latestStatus`.** Optional free-text progress string set by `agent__task_status`. Useful for showing "what was the agent's last self-reported status" in the UI. The adapter stores it as `sessions.extras_json.latestStatus`. Canonical does not have a dedicated field.
9. **Per-op `reasoning` block.** Canonical has `reasoning` as a separate `OpKind`. The adapter spawns a nested `OpStartedEvent(Kind="reasoning", ParentOpSeq=<llm>)` plus `OpFinalizedEvent` per llm op that has `reasoning.final`. The reasoning text content is stored in the reasoning op's extras as `reasoning.final`; no raw reasoning payload is inlined. SOW-0097 parity treats that extras field as the exact `reasoning_text` artifact for this adapter and proves its semantic-text hash/length with selector `field_path=reasoning.final`.
10. **Embedded request/response payloads (legacy, no ref descriptor).** Older v2 snapshots may inline payloads directly. Canonical still never inlines raw bytes, and the v2 adapter stays read-only on source files, but inline payloads are represented by exact snapshot selectors: `PayloadRefEvent.LocationURI=file://<snapshot>.json.gz?json_pointer=<pointer>` with `Compression=gzip`. Producer-shaped `payload.ref` and `payload.sdk.ref` descriptors produce `PayloadRefEvent` rows and suppress inline emission for that side. The adapter also keeps constrained legacy flat descriptor compatibility for older helper-shaped snapshots and tests: a flat descriptor must have a non-empty string `ref`, or a top-level `path` accompanied by evidence metadata such as `format`, byte counts, hash, compression, or `captured`. A bare inline object like `{"path":"src/file"}` is not a payload ref; it is an inline JSON payload and receives a snapshot JSON-pointer selector. If both producer descriptors are present on the same request/response side, both rows are emitted with independent path validation. No side-cache or generated artifact is introduced.

## References

- `ai-agent.git/.agents/sow/specs/snapshots.md` — authoritative format spec (v2 and v3 sections).
- `ai-agent.git/.agents/sow/specs/optree.md` — SessionNode/TurnNode/OperationNode field reference.
- `ai-agent.git/.agents/sow/specs/accounting.md` — accounting entry semantics, token normalization.
- `ai-agent.git/src/persistence.ts:19-67` — `getDefaultPersistenceConfig`, `createPersistenceHandlers.handleSnapshot` — the producer write path.
- `ai-agent.git/src/session-persistence-events.ts:30-65` — `emitSessionSnapshotEvent` — what gets emitted. Since `ai-agent@8a0078bc` the on-disk payload also carries `sessionId, originId, originTxnId, parentId, parentTxnId, timestamp`; the v2 adapter reads only `{version, reason, opTree}` by design.
- `ai-agent.git/src/ai-agent.ts:392-403` — `persistSessionSnapshot`; uses `originTxnId` for filename.
- `ai-agent.git/src/ai-agent.ts:530-544` — `txnId`/`originTxnId` identity model; `SessionTreeBuilder({traceId: txnId, ...})`.
- `ai-agent.git/src/ai-agent.ts:669-678` — sub-agent dispatch; child inherits `originTxnId`, parent calls `persistSessionSnapshot('subagent_finish')` after child returns.
- `ai-agent.git/src/ai-agent.ts:1220-1222` — terminal `persistSessionSnapshot('final')` callback site.
- `ai-agent.git/src/session-tree.ts` — `SessionTreeBuilder`, node shape definitions.
- `ai-agent.git/src/types.ts:786-793` — `SessionSnapshotPayload` in-memory shape (sessionId, originId, originTxnId, parentId, parentTxnId, timestamp, snapshot). Since `ai-agent@8a0078bc` these top-level fields ARE persisted to disk (`persistence.ts:56-61`); the v2 adapter ignores them by design and reads only `{version, reason, opTree}`.
- `ai-agent.git/src/evidence/reader.ts` — payload-ref path validation pattern (`readEvidencePayload`).
- `ai-viewer.git/.agents/sow/specs/canonical-events.md` — Event types this adapter emits.
- `ai-viewer.git/.agents/sow/specs/data-model.md` — SQLite schema this adapter's events populate.
- `ai-viewer.git/.agents/sow/specs/adapter-aiagent-v3.md` — sibling adapter that shares the same `<sessions-dir>`.

## Sampling Evidence (Sanitized)

All evidence below was gathered by direct inspection of `<sessions-dir>` on the operator's workstation. UUIDs, agent names, paths, and session titles have been replaced with `<placeholder>` tokens.

- File counts: 294,316 `.json.gz` at root; 17,356 `session/<sessionId>.jsonl`; 14,296 `payloads/<sessionId>/` dirs; 2 `.json.gz.tmp-*` orphans; 29 zero-byte `.json.gz`.
- Compressed size distribution: min 0; p10 2,458; p50 10,878; p90 28,683; p99 1,192,114; max 151,881,088 bytes; total 27,272,835,000 bytes.
- 200-sample shape audit: all top-level keys ∈ `{version, reason, opTree}`; version values `{1, 2}` only; all `reason` values `"final"`; opTree keys union exactly matches the producer source spec.
- 50-sample child-session audit: 7 ops with embedded `childSession`, 0 with `childSessionRef`, 0 with `childSessionSummary` → embedded is dominant on this dataset.
- 31-child traceId audit (one parent file): 0 of 31 child traceIds existed as their own `<traceId>.json.gz`, confirming v2 does not independently persist sub-agents.
- 50-sample op-kind histogram: `{system: 132, llm: 107, tool: 46, session: 2, STEP_session: 5}`; step kinds: `{internal: 5}`.
- 200-sample failed-op audit: 249 op-level failures observed; common attribute `error: "token_budget_exceeded"`; session-level errors include free-text slugs like `"Turn 1 failed after 1 attempt of 1 (maxTurns=3); ..."` and `"canceled"`.
- 200-sample accounting tokens keys union: `{cacheReadInputTokens, cacheWriteInputTokens, cachedTokens, inputTokens, outputTokens, totalTokens}` — both Anthropic and OpenAI naming conventions appear.
- Bench: 887 files/s/thread compressed decode + JSON parse on workstation CPU; extrapolated 5.5 min single-thread for full 294K backfill; ample headroom against the 60-min SOW gate.
- File-mtime range on disk: oldest 2025-09-15, newest 2026-05-22 — ~8 months of history.
