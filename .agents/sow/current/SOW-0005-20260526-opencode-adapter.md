# SOW-0005 - opencode adapter (read-only SQLite + cumulative-token deltas + schema-drift tolerance)

## Status

Status: in-progress

Sub-state: active in `current/`. Approved under the operator's blanket Phase-2 backlog sign-off ("deliver them all, any order"). Prerequisites met: SOW-0001 Phase 1 in `done/`; SOW-0004 (codex) merged, which left the catalog idempotent under op re-emission (reused here). Pre-Implementation Gate filled 2026-05-30 (below).

## Requirements

### Purpose

Deliver the opencode adapter end-to-end against the single live SQLite database at `~/.local/share/opencode/opencode.db` (3.9 GB on the operator's workstation). The adapter opens the database **strictly read-only**, polls per-table watermarks with PK-indexed `MAX(id)` queries, synthesizes turns and ops from the opencode `session → message → part` tree, computes per-LLM-op token deltas from opencode's cumulative `step-finish` totals, tolerates schema drift across ~30 historic migrations via `PRAGMA table_info` + dynamic SELECT, registers multi-provider sessions with `provider_alias`, and exposes an auto-discovery probe. Outcome: the operator sees every opencode session — including sub-agents linked via `session.parent_id` — without opencode ever observing a write from ai-viewer.

### User Request

From the operator's 2026-05-26 milestone list (recorded in conversation while planning post-Phase-1 work): "Add claude-code, codex, and opencode adapters next, one SOW each, so each can be reviewed and scoped independently." This SOW is the opencode slice of that instruction and inherits its full scope (parser + Scan + Tail + cursor + tests + fixtures + auto-discovery + spec sync).

### Assistant Understanding

Facts:

- Opencode stores everything in one SQLite database with WAL companions at `~/.local/share/opencode/opencode.db` (3.9 GB main + 5.5 MB WAL + 32 KB SHM on the operator's workstation; `adapter-opencode.md` §"Source Format").
- The defining read-safety constraint, recorded as a hard invariant in AGENTS.md and `adapter-opencode.md` §"Read Strategy": open with `mode=ro&_pragma=query_only(true)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`. NEVER call `PRAGMA wal_checkpoint`, `PRAGMA optimize`, `VACUUM`, `BEGIN EXCLUSIVE`, `ATTACH ... rwc`, or any other write-path. This is the highest-risk adapter on read-safety because opencode's writer is live and concurrent.
- Schema is `session → message → part` with no native `turn` or `op` concept (`adapter-opencode.md` §"Source Format", §"Mapping to Canonical Events"). The adapter synthesizes: Turn = assistant message; LLM-Op = `step-start` → `step-finish` pair; Tool-Op = `tool` part nested under current step; Reasoning-Op = `reasoning` part nested under current step.
- **`step-finish` token counts are CUMULATIVE within a message, not per-step** (`adapter-opencode.md` §"Tool calls and Models — concrete field map", §"Canonical Model Gaps" #3). Observed monotonic sequence (input tokens 17438, 23075, 31713, 35407, …) confirms this. The adapter MUST compute deltas between successive `step-finish` values within the same message before emitting per-op tokens. Mixing cumulative for delta would triple-count tokens silently — this is the top-line defect to prevent.
- Sub-agent linkage is dual and 100% consistent on observed data: `session.parent_id` (authoritative, 1285 child sessions) + `part.data.state.metadata.sessionId` on `tool` parts where `tool='task'` (1274 of 1274 cross-checks match) (`adapter-opencode.md` §"Sub-Agent Linkage"). Adapter prefers `parent_id`.
- IDs are time-prefixed Sonyflake; `id > '<last_id>'` is monotonic and PK-indexed. Cursor uses `MAX(id)` per table as the primary watermark, with `MAX(time_updated)` as a fallback for detecting in-place mutations behind an fsnotify-gated trigger (`adapter-opencode.md` §"Performance").
- Schema evolves between opencode versions (~30 migrations in `packages/opencode/migration/` per `anomalyco/opencode @ 2b3ddf9`). The adapter queries `PRAGMA table_info(session|message|part|session_message)` at startup, builds dynamic SELECT lists naming only known columns (never `SELECT *`), tolerates missing columns with empty/zero values + one INF log per (table, column) on first occurrence (`adapter-opencode.md` §"Edge Cases" #1).
- Opencode is multi-provider: observed `providerID` values include `llm-netdata-cloud`, `zai-coding-plan`, `minimax-coding-plan`, `deepseek`, `kimi-for-coding`, `openrouter`, `alibaba-coding-plan`. These are user-defined aliases, not canonical vendors. Canonical model adds `sessions.provider_alias` + `catalog_providers` table per SOW-0002; the adapter populates the alias verbatim and emits a best-effort canonical mapping where known (`adapter-opencode.md` §"Multi-provider awareness").
- Poll cadence: 2 s idle / 500 ms active / 250 ms after `opencode.db-wal` fsnotify mtime change for the next 5 s (`adapter-opencode.md` §"Watch Strategy"). Each delta page is its own short transaction (limit 1000 rows, target <50 ms) to avoid pinning the WAL.
- Phase 1 Foundation (SOW-0001) delivers `internal/canonical/`, `internal/ingest/`, `internal/store/`, `internal/adapters/registry.go`, the `canonical.Adapter` interface, pricing catalog, fixture sanitization tooling, and CI gates that this SOW reuses unchanged.

Inferences:

- Initial backfill of 6,778 sessions + 127,345 messages + 585,894 parts (~3.9 GB) is expected in 60-90 s wall-clock per `adapter-opencode.md` §"Performance" (SSD read ~100 MB/s + JSON decode CPU bound at ~50 MB/s in Go with `encoding/json`). Page reads at 1000 rows/transaction with `SourceProgress` every 1000 rows so restart resumes.
- Read-only enforcement is layered defense-in-depth: `mode=ro` (OS-level, the file is opened `O_RDONLY` so SQLite cannot upgrade), `_pragma=query_only(true)` (SQL-layer rejection of writes), and a test that asserts an attempted write panics or errors at the adapter's connection-helper boundary.
- The cumulative-token regression test should pin the delta math against a synthetic fixture with deliberately monotonic step-finish values and a committed `.golden.json` so a future change cannot silently revert to raw-value emission.

Unknowns:

- Whether the opencode binary on this workstation is currently running concurrently with the test runs (the adapter must be safe either way). Resolved by the read-only-enforcement test which uses a copy-on-write fixture; production runs against the live DB are validated only in the manual-walkthrough acceptance.
- Whether any `session_message.type` beyond `agent-switched` / `model-switched` appears on this workstation. Spec records "treat unknown types as forward-compatibility data and skip with structured WARN"; resolved by a `SELECT DISTINCT type` query during Pre-Implementation Gate authoring.
- Whether the dynamic `PRAGMA table_info`-driven SELECT can be tested against an older opencode schema. Acceptance #8 requires this; the fixture is a small synthetic SQLite file with a subset of columns mimicking an older migration state.

### Acceptance Criteria

1. `internal/adapters/opencode/` package compiles, lints clean, and is registered in `internal/adapters/registry.go`. **Verification**: `go build ./...` exits 0; `golangci-lint run` exits 0; `internal/adapters/registry_test.go` asserts the adapter is enumerable by name `"opencode"`.
2. **Read-only enforcement asserted in tests.** The adapter's connection helper opens with `mode=ro&_pragma=query_only(true)` (layered OS + SQL guard). An explicit unit test invokes the helper and probes `INSERT`, `UPDATE`, `DELETE`, `PRAGMA wal_checkpoint`, `VACUUM`, and `ATTACH ... 'rwc'`, asserting each probe **cannot mutate `opencode.db`** plus a byte-untouched read-back. **Verified ground truth (modernc.org/sqlite, Chunk A):** INSERT/UPDATE/DELETE/VACUUM return an error (`attempt to write a readonly database`); `PRAGMA wal_checkpoint(TRUNCATE)` is a no-op under `mode=ro` (returns the `busy=1, log=-1, checkpointed=-1` "nothing checkpointed" sentinel — asserted); `ATTACH ... 'rwc'` attaches a SEPARATE side file (never `opencode.db`) and `query_only(true)` blocks the write INTO it (the side `CREATE TABLE` errors — asserted). Asserting "all six error" would pin a false mechanism; the test asserts each probe's precise no-mutation property instead. **Verification**: `internal/adapters/opencode/conn_test.go` runs all six probes + the read-back; CI's gates include it.
3. **Cumulative-token-delta regression test.** A synthetic SQLite fixture contains one assistant message with three `step-finish` parts whose `tokens.input` are `100, 250, 410` (cumulative). The adapter must emit per-LLM-op `tokens_in` of `100, 150, 160` (deltas). **Verification**: `internal/adapters/opencode/tokens_delta_test.go` asserts the exact delta sequence; the golden file pins the values so a regression to raw-value emission fails the gate.
4. Sub-agent linkage is correct: every `session` row with `parent_id` set emits a `SessionStartedEvent` with `Kind='sub_agent'` and `ParentNativeID=parent_id`; `tool` parts where `tool='task'` and `state.metadata.sessionId` is set emit both a tool Op AND a session Op (`Kind='session'`, `ChildSessionNativeID=state.metadata.sessionId`) per `adapter-opencode.md` §"Mapping to Canonical Events" rule for `tool` where `tool='task'`. **Verification**: golden test on a sanitized real-data fixture with one parent + one task-spawned child asserts both edges exist in the emitted event stream.
5. **Schema-drift tolerance proven against an older schema fixture.** A second synthetic SQLite fixture mimics a pre-`20260510033149_session_usage` schema (no `cost`/`tokens_*` columns on `session`). The adapter, reading `PRAGMA table_info` at startup, builds a dynamic SELECT that omits the missing columns, emits empty/zero values in the canonical event, and logs exactly one INF per (table, column) on first occurrence. **Verification**: `internal/adapters/opencode/schema_drift_test.go` opens the older-schema fixture, asserts the SELECT does not reference the missing columns (by inspecting the prepared statement or via a query-log probe), and asserts the INF log fires once per missing column then is suppressed.
6. Watermark cursor (per-table `MAX(id)` primary + `MAX(time_updated)` fallback gated by `opencode.db-wal` fsnotify) is durable across restart with zero duplicates and zero gaps; the `time_updated` query runs only after WAL mtime change or every 60 s safety net. **Verification**: integration test that ingests half a fixture, persists cursor, restarts, ingests rest, asserts identical end state to a one-shot ingest; a second test asserts the `MAX(time_updated)` query is NOT issued during steady-state idle polls (probed via a query-counting test driver).
7. Multi-provider sessions register correctly: every distinct opencode `providerID` observed becomes a row in `catalog_providers` with `alias=providerID` and `canonical=<best-effort mapping or alias unchanged>`; `sessions.provider_alias` is populated from `data.providerID`. **Verification**: golden test on a fixture with two sessions using different aliased providers asserts both `catalog_providers` rows and both `sessions.provider_alias` values.
8. Auto-discovery probe detects `~/.local/share/opencode/opencode.db` (and `$OPENCODE_DB` when set) at startup, opens read-only, queries `__drizzle_migrations` to record the schema hash, and exposes `(session_count, message_count, part_count, latest_migration_name)` in `/api/health`. **Verification**: unit test on the probe with a fixture DB; manual run on the operator's workstation registers the real source and `/api/sources` reports the live counts.

## Analysis

Sources checked:

- `.agents/sow/specs/adapter-opencode.md` (full spec, all sections) — primary contract.
- `.agents/sow/specs/canonical-events.md` — target event types, including `Kind='sub_agent'`, `OpKind='reasoning'`, `provider_alias` field on SessionStartedEvent, indefinite-`running` SessionStatus (opencode never finalizes; only archives).
- `.agents/sow/specs/data-model.md` — SQLite schema, especially `sessions.provider_alias`, `catalog_providers`, cross-format compatibility matrix.
- `.agents/sow/done/SOW-0002-20260526-cross-format-data-model-analysis.md` — analysis context confirming opencode's cumulative-token quirk and read-only invariant.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — infrastructure the adapter plugs into.
- Real evidence on the operator's workstation: `~/.local/share/opencode/opencode.db` (3.9 GB, 6778 sessions, 127345 messages, 585894 parts, 3985 session_messages, 20 migrations applied through `20260511000411_data_migration_state` as of 2026-05-26).
- Upstream source at `anomalyco/opencode @ 2b3ddf9f34546b9bcea25ec8e0ff57e2811c4537` — `packages/opencode/src/storage/db.ts`, `packages/opencode/src/session/session.sql.ts`, `packages/opencode/src/session/message-v2.ts`, `packages/core/src/session-message.ts`, `packages/opencode/migration/` per `adapter-opencode.md` §"References".

Current state:

- SOW-0001 (in-progress) delivers canonical event types, SQLite store, ingest pipeline, adapter registry, pricing catalog, fixture sanitization tooling, CI gates, and the ai-agent v3/v2 adapters end-to-end. This SOW assumes that infrastructure is in place; if SOW-0001 is not yet completed, this SOW remains in `pending/`.
- No `internal/adapters/opencode/` package exists yet (the bootstrap only documented the format).
- The canonical model already absorbed opencode's gaps in SOW-0002 (cache tokens, reasoning tokens, provider alias, catalog_providers table); this SOW does NOT propose new canonical changes — only adapter implementation.

Risks:

- **R1 — Read-only DB safety (CRITICAL).** Opencode's writer is live and concurrent; any accidental write from ai-viewer corrupts the operator's primary AI coding tool. Mitigation: layered defense — OS-level `mode=ro`, SQL-layer `query_only(true)`, explicit test asserting six write paths all error (acceptance #2). The DSN string is encoded as a constant in the adapter and validated by a unit test that pattern-matches the connection string to guarantee the read-only PRAGMAs are present. No code path in the adapter ever takes a `*sql.Tx` that begins anything other than `BEGIN DEFERRED`.
- **R2 — Cumulative-token miscount (CRITICAL).** The most likely silent defect; misses would triple-count tokens and corrupt every cost calculation. Mitigation: acceptance #3 regression test with a frozen golden file; the test fixture is named explicitly so future code review surfaces the contract; per-LLM-op delta computation is implemented in a single named function (`computeStepDeltas`) with its own table-driven unit tests covering reset-on-message-boundary, missing intermediate step-finish (cancelled step), and out-of-order observation.
- **R3 — Schema drift between opencode versions.** ~30 historic migrations; older rows lack newer columns. Mitigation: dynamic `PRAGMA table_info`-driven SELECT (acceptance #5), tolerance for missing columns with structured INF on first occurrence, and a `schema_hash` field in the cursor that detects new migrations and triggers a re-probe without resetting the cursor (only when a depended-on column disappears does the adapter perform a full re-ingest).
- **R4 — Multi-GB DB query latency.** Full-table `MAX(time_updated)` scans on the `part` table (585k rows, 2.3 GB) take 400-800 ms cold. Mitigation: PK-indexed `MAX(id)` is the primary watermark; the expensive `MAX(time_updated)` query runs only when fsnotify on `opencode.db-wal` signals activity OR every 60 s as a safety net (acceptance #6). The 1000-row page limit + sub-1 s transactions keep the WAL from growing unboundedly.
- **R5 — Sensitive content in fixtures.** Every real opencode message/part carries operator data — session titles, directories, prompts, tool outputs, patch file paths. Mitigation: every committed fixture under `testdata/opencode/` is a synthetic SQLite file constructed by a fixture-builder utility, NOT a copy of the operator's DB. The fixture-builder writes only sanitized data shaped like the real schema; the live DB is consulted only for shape verification (via `PRAGMA table_info` printed during Pre-Implementation Gate authoring) and never copied into `testdata/`.

## Pre-Implementation Gate

Filled 2026-05-30. A readiness-briefing subagent re-probed the live `opencode.db` read-only (`immutable=1`); load-bearing claims (the read-only DSN, acceptance #1-8) were re-verified against ground truth before this gate.

### Problem / model

Additive feature: a new `opencode` adapter that projects OpenCode's **SQLite** session store onto the canonical event model. Unlike the four JSONL/file adapters (byte-offset cursor + fsnotify-on-append), opencode keeps everything in one SQLite DB (`~/.local/share/opencode/opencode.db`, WAL, Drizzle-managed, ~4.36 GB live, 20 migrations). So the read model is SQL delta-queries + a watermark cursor + DB polling, not line streaming. The adapter is a read-only projection from `session`/`message`/`part`/`session_message` rows → canonical events, reusing the registry/payloads/golden patterns from `codex`/`claude_code` but replacing parser+scanner+stream+tailer with a query layer + poll loop.

### Evidence reviewed

- `.agents/sow/specs/adapter-opencode.md` (567 lines, evidence-driven) — primary contract.
- Live DB re-probe (read-only, 2026-05-30): `session` 7,775 (6,335 root / 1,440 child / 2 archived), `message` 144,551 (assistant `data` keys role/time/error/parentID/modelID/providerID/mode/path/cost/tokens), `part` 667,335 (tool 230,606 / step-start 133,186 / step-finish 132,595 / text 83,367 / reasoning 75,361 / patch 11,686 / compaction 495 / file 22 / retry 17), `session_message` 5,975 (**only** agent-switched + model-switched). `event`/`event_sequence` = 0 (ignore). Latest migration `20260510033149_session_usage`.
- `internal/store/store.go:303-318` — `buildDSN` forces `foreign_keys(on)`+`busy_timeout(5000)`, readers add `query_only(true)`; this is ai-viewer's OWN-DB reader (not for the external opencode.db). `internal/canonical/events.go` — `TurnFinalizedEvent` carries `TokensCacheRead/Write`+`CostUSD` (per-turn cache accounting works) but NO `Extras` (turn-extras unreachable — SOW-0021); `OpFinalizedEvent` carries cache tokens + ProviderAlias; `KindSubAgent`/`OpReasoning`/`OpSession`/`OpCompaction` exist (no canonical change needed).
- `internal/adapters/{codex,claude_code}/` — structural template; `registry.go` self-registration; codex `discovery.go`/golden harness.
- SOW-0005 Acceptance #1-8 + Risks R1-R5.

### Affected contracts & surfaces

- **NEW** package `internal/adapters/opencode/` (SQLite-backed; see structural map).
- **ADDITIVE** `cmd/ai-viewer-ingest/sources.go`: a 5th auto-discovery probe (`$OPENCODE_DB` else `~/.local/share/opencode/opencode.db`) + a `__drizzle_migrations` schema-hash + count helper (acceptance #8); blank-import for `init()` registration.
- **ADDITIVE** `testdata/opencode/<scenario>/` — synthetic SQLite fixtures built by a fixture-builder.
- **NO** change to `internal/canonical/` (all target fields exist), `internal/ingest/` (catalog already idempotent post-SOW-0004), `internal/store/` schema, or sibling adapters.

### Spec deltas (LANDED before tests/code, committed with this gate)

1. adapter-opencode.md task→session rule (was "TBD; emit both"): ratified to **emit both** (tool Op + session Op; session op is the topology parent).
2. adapter-opencode.md per-turn token rule (was "to be verified"): firmed to **delta from the previous assistant message's cumulative totals**, with an explicit implementer-verify-on-live-DB note (the step-finish cumulative pattern is verified; the message-level pattern is the analogous one level up, not yet independently confirmed).

### Patterns to reuse vs differ (briefing §B)

- **Reuse**: `init()→adapters.Register("opencode", Factory)`; `Adapter` struct + compile-time `var _ canonical.Adapter`; `Name()/Format()/ParseCursor()`; Scan-then-Tail single-thread lifecycle; fail-soft `onError`; codex `discovery.go` → the auto-discovery probe; the golden_test harness shape (seed a `.db` instead of files).
- **Differ**: parser+scanner+stream+tailer → a **`store.go` query layer** (prepared delta SQL + `database/sql` rows) + a **poll loop** (2 s idle / 500 ms active / 250 ms post-WAL-fsnotify; coarse fsnotify on `opencode.db-wal` as a wakeup hint only). `payloads.go` emits `opencode-sqlite://…?part_id=&field=…` URIs (spec 420-426), not `file://`. `mapper.go` keeps turn/op-synthesis but walks message+part trees.

### Cursor model (decision)

Per-table two-watermark JSON: `{version, schema_hash, tables:{session,message,part,session_message:{max_id, max_time_updated}}}`. Primary watermark = `MAX(id)` (the 30-char Sonyflake PK is time-prefixed + monotonic + PK-indexed → `WHERE id > :last` is cheap). `MAX(time_updated)` (13-digit ms, **unindexed** — a part-table full scan ~400-800 ms) catches in-place mutations and is gated to run only after an `opencode.db-wal` mtime change or a 60 s safety net. Delta page: `… WHERE time_updated>:u OR (time_updated=:u AND id>:id) ORDER BY time_updated,id LIMIT 1000`, page until empty. Scan→Tail resumes from persisted watermarks; re-reads are absorbed by the ingester's idempotent upserts + the now-idempotent catalog.

### Canonical mapping (briefing §D)

Session=`session` row; Turn=assistant `message` (seq by `(time_created,id)`); LLM-Op=`step-start`→`step-finish`; Tool-Op=`tool` part (namespace derived, e.g. `github_get_file_contents`→`github`/`get_file_contents`); Reasoning-Op=`reasoning` part; text/patch are not ops (text→presenter read; patch→op extras); compaction→INF LogEntry; retry→WRN LogEntry. Terminal status: assistant `data.time.completed` NULL → `running`; `data.error` → `failed` (ErrorClass=`data.error.name`); `time_archived` → `completed`; else stays `running` (no per-session terminal, like claude-code/codex). **Cumulative-token delta (AC#3, verified):** step-finish `tokens.*` are cumulative within a message → emit per-op deltas via one `computeStepDeltas`. Sub-agent (AC#4): `parent_id` child → `Kind=sub_agent`+ParentNativeID; `tool='task'` with `state.metadata.sessionId` → tool Op + session Op. Multi-provider (AC#7): `ProviderAlias=data.providerID` verbatim; `Provider`=best-effort canonical (default=alias). Turn-extras (cwd etc.) deferred to SOW-0021 (no canonical turn Extras); per-turn cache tokens DO work via `TurnFinalizedEvent`.

### Risk & blast radius

Purely additive (new package + registry blank-import + additive `sources.go` probe); no canonical/ingest/store change (target fields exist; catalog idempotent post-SOW-0004). **R1 (CRITICAL) read-safety:** the opencode writer is live + concurrent on a 4.36 GB DB — layered defense: own helper opens `mode=ro` (OS `O_RDONLY`) + `query_only(true)` + `busy_timeout`, never calls any write-path pragma, each delta page in its own short `BEGIN DEFERRED` (<1 s) to avoid pinning the WAL / blocking the writer's checkpoint; acceptance #2's six write-probes pin it. **R2 (CRITICAL)** cumulative-token miscount → `computeStepDeltas` + AC#3 golden. **R4** part-table `MAX(time_updated)` full scan → gated by `MAX(id)` primary + WAL-mtime. **R5** fixtures are synthetic SQLite (never copy the operator DB).

### Sensitive-data plan

Every committed fixture under `testdata/opencode/` is a synthetic SQLite file built by a fixture-builder writing only sanitized, schema-shaped data (synthetic titles/dirs/prompts; `git@github.com:example/example.git`; no operator PII). The live DB is consulted ONLY for shape verification (`PRAGMA table_info`), never copied. `scripts/scan-secrets.sh` is the net.

### Implementation plan (chunked; each = spec → failing tests → subagent impl → gates → integrate)

- **Chunk A** — read-only connection helper (own DSN constant + the 6 write-probe test, AC#2) + the watermark `cursor.go` + typed row/`data`-JSON structs + `store.go` schema introspection (`PRAGMA table_info` → dynamic SELECT, AC#5).
- **Chunk B** — `mapper.go` row→event synthesis: session/turn/op trees, terminal status, `computeStepDeltas` (AC#3), reasoning/tool/patch/compaction/retry, sub_agent + task→session linkage (AC#4), provider alias (AC#7).
- **Chunk C** — `store.go` delta queries + the poll-loop tailer (WAL-mtime fsnotify hint + idle/active cadence; `MAX(time_updated)` gating, AC#6).
- **Chunk D** — `payloads.go` (`opencode-sqlite://` URIs) + `adapter.go` (Scan/Tail/ParseCursor + `init()`) + the `sources.go` auto-discovery probe + `__drizzle_migrations` schema-hash/counts (AC#8) + registry_test.
- **Chunk E** — fixture-builder + synthetic-DB golden scenarios (happy, sub-agent+task-child, multi-provider, old-schema-drift, cumulative-token) + restart/resume + idle-no-MAX(time_updated) integration tests + fuzz on the `data`-JSON decode.

### Validation plan (acceptance → tests)

#1 registry_test asserts `"opencode"`. #2 `readonly_test.go` (6 write-probes error). #3 `tokens_delta_test.go` (100/250/410 → 100/150/160). #4 golden parent+task-child (both edges). #5 `schema_drift_test.go` (pre-`20260510033149` fixture, dynamic SELECT omits missing cols, one INF/col). #6 restart/resume integration + query-counter (no idle `MAX(time_updated)`). #7 multi-provider golden (two catalog_providers + provider_alias). #8 `cmd/ai-viewer-ingest/sources_test.go` (probe registers a fixture DB, reports counts + latest migration). Plus a `data`-JSON fuzz target.

### Artifact impact plan

Producer: the adapter's Scan (watermark backfill) + Tail (poll loop). Refresh: WAL-mtime fsnotify hint / poll cadence → delta query. Repair: cursor corruption → re-read from zero watermark (idempotent upserts absorb). Served by the existing presenter/REST + the now-idempotent catalog; `/api/sources` + `/api/health` report the opencode source + (session/message/part counts, latest migration) (AC#8). No DB migration (ai-viewer schema unchanged).

### Open decisions — DECIDED by CTO (recorded)

1. **Connection helper:** the adapter uses its OWN read-only helper (DSN `mode=ro&_pragma=query_only(true)&_pragma=busy_timeout(5000)`, `MaxOpenConns(2)`), NOT `store.OpenReader` (that targets ai-viewer's own DB + forces `foreign_keys(on)`/pool 8). The helper's DSN is a tested constant; acceptance #2's six write-probes are its contract. `foreign_keys` is immaterial for a read-only connection. **Decided.**
2. **Poll cadence:** 2 s idle / 500 ms active / 250 ms floor after a WAL-mtime fsnotify event (ratify spec). **Decided.**
3. **Cursor granularity:** per-table `MAX(id)` (primary, PK-indexed) + `MAX(time_updated)` (gated by WAL-mtime / 60 s). **Decided.**
4. **Turn-extras:** opencode per-turn extras (cwd, etc.) are DEFERRED to SOW-0021 (no canonical turn `Extras` carrier); do NOT half-build a write path; per-turn cache tokens use the existing `TurnFinalizedEvent` fields. State the limitation. **Decided.**
5. **task→session op:** emit BOTH the tool Op and the session Op (session = topology parent). **Decided** (spec ratified above).
6. **Provider alias:** `ProviderAlias = data.providerID` verbatim; `Provider` = best-effort canonical (default = alias unchanged). **Decided.**

Open (implementer-verify, not blocking): the message-level per-turn cumulative-token pattern (spec row firmed but flagged for live-DB confirmation before pinning the golden); whether `immutable=1` is ever used in production (NO — production uses `mode=ro` to respect the live WAL; `immutable=1` only for static test fixtures).

## Implementation

### Chunk C — delta-query layer + poll-loop tailer (2026-05-30)

Delivered the SQL delta-query layer and the poll-loop tailer (the backfill scan loop + the realtime poll loop). Purely additive inside `internal/adapters/opencode/`; no sibling adapter, `canonical`, `ingest`, or `store` package touched. Read-only invariant held: the only production DB open is the chunk-A `openReadOnly` helper, and every transaction is `BeginTx{ReadOnly:true}` (BEGIN DEFERRED); no write-path pragma anywhere.

Files:

- `store_query.go` (NEW, 241 lines) — paged delta query per table (`scanTableDelta` → `scanOnePage`, each page its own short read tx; pages until a short page), the cheap PK-indexed `maxID` probe, the expensive gated `maxTimeUpdated` probe, the affected-session set (`affectedSet`, first-seen dedup), and `resolvePartSession` (denormalized `session_id` → message-map → indexed `message_id` lookup fallback).
- `store_load.go` (NEW, 400 lines) — full-session-tree load (`loadSession`, `loadSessionTree` → ordered `[]messageWithParts`), the per-table dynamic-column scanners (present-columns only, never `SELECT *`), and the present-column point/ordered SELECT builders. `errSessionGone` for an affected id whose row vanished.
- `store.go` (MODIFIED) — added `buildSelectByID` companion to `tableSchema` (the old-schema `time_updated`-absent fallback: `WHERE id > ? ORDER BY id LIMIT 1000`).
- `tailer.go` (NEW, 357 lines) — `scanLoop` (backfill), `tailLoop` (realtime follow with the idle/active/WAL-floor cadence state machine), `pollOnce`, `detectChange` (cheap `MAX(id)` every poll; gated `MAX(time_updated)`), the pure `shouldProbeTimeUpdated` gate (AC#6), `watchWAL` (best-effort fsnotify hint with non-fatal missing-WAL fallback), `emitProgress`/`emitEvents` (ctx-aware, codex shape).
- `tailer_changes.go` (NEW, 309 lines) — the shared `processChanges` pipeline (delta → affected → reload → map → emit → advance), `collectDeltas` (with the every-~1000-rows `SourceProgress` checkpoint), `reloadAndEmit`, `loadAndMapSession`, the `pollState` cadence machine, and `coerceScanCursor` + `schemaFingerprint` (records a present-column schema-shape hash into the cursor; the `__drizzle_migrations`-name hash is deferred to chunk D).
- Tests (NEW): `store_query_test.go`, `store_load_test.go`, `store_testhelpers_test.go` (synthetic-DB builders + a registered query-counting `driver.Driver` wrapper), `tailer_test.go`, `tailer_gate_test.go` (AC#6 pure gate + cadence), `tailer_counting_test.go` (literal no-idle-`MAX(time_updated)` via the counter), `tailer_resume_test.go` (zero-dupes/zero-gaps), `tailer_branch_test.go`, `tailer_wal_test.go`, `tailer_pollcycle_test.go`.

Key decisions locked (honoring the recorded SOW/spec):

- **Delta page SQL** = `buildSelect` (present columns) with `WHERE time_updated > :u OR (time_updated = :u AND id > :id) ORDER BY time_updated, id LIMIT 1000`; old-schema fallback (no `time_updated`) = `buildSelectByID` (`WHERE id > :id ORDER BY id`), watermark advancing on `MaxID` only. Chosen from the introspected schema (`tableSchema.has("time_updated")`), never crashes.
- **Affected-session derivation**: session row → own id; message → `session_id`; part → denormalized `session_id` (fallback to `message_id`→`session_id` lookup on a hypothetical old schema lacking it); session_message → `session_id`. De-duplicated; full-tree reload per affected session (the mapper's per-turn cumulative-token delta requires the whole ordered message list).
- **Cadence state machine**: idle 2 s / active 500 ms / 250 ms floor for 5 s after a WAL fsnotify event; next interval = min(active|idle, floor-while-open).
- **`MAX(time_updated)` gate (AC#6)**: pure `shouldProbeTimeUpdated(now, lastWALEvent, lastProbe, safetyNet)` = `lastWALEvent.After(lastProbe) || now.Sub(lastProbe) >= 60s`. Proven false on idle polls by both the pure-truth-table test and the query-counting-driver test (zero `MAX(time_updated)` across 5 idle polls; `MAX(id)` runs every poll).
- **WAL-watch-missing fallback**: a missing `-wal` file / Add failure / watcher error → one `onError` + a closed hint channel → pure timer polling; the 60 s safety net still catches in-place mutations. A watcher error never kills the loop.

Gates (run 2026-05-30): `go build ./...` exit 0; `go vet` exit 0; `golangci-lint` 0 issues; `gosec -severity medium -confidence medium ./...` exit 0 (two justified `// #nosec G202` on `MAX(id)`/`MAX(time_updated)` where the only interpolated token is a fixed `trackedTables` name via `quoteIdent`); `go test -race -cover` pass at **91.6%** (package was 96.1% pre-chunk; the delta is the new code's defensive error branches, all new code ≥ target). All new `.go` files ≤ 400 lines.

### Chunk D — payloads + adapter wiring + auto-discovery + real schema-hash (2026-05-30)

Wired the chunk-A/B/C pure pieces into a registered `canonical.Adapter`, formalized the payload-URI grammar in `payloads.go`, replaced chunk C's present-column placeholder with the REAL `__drizzle_migrations` schema hash, and added the opencode auto-discovery probe (AC#8). Purely additive inside `internal/adapters/opencode/` plus the documented `cmd/ai-viewer-ingest/sources.go` integration point; no sibling adapter, `canonical`, `ingest`, `store`, or `presenter` touched. Read-only invariant held: every new production DB open goes through the chunk-A `openReadOnly` helper (`adapter.go:snapshotCursor`, `migrations.go:ProbeStatus`); no write-path pragma, no `rwc`, no `mkdir`, no `ATTACH`.

Files (NEW):

- `payloads.go` (47 lines) — `buildPayloadURI(partID, field)`, the SINGLE source of truth for the `opencode-sqlite://?part_id=<id>&field=<field>` grammar, URL-encoding both values via `net/url`. No resolver/parser (no consumer yet — that would be dead code; the `/api/payloads` resolver is a separate Phase-2 SOW).
- `migrations.go` (190 lines) — `readMigrations` (ordered `name` list by `id ASC` + latest; missing-table → `errNoMigrationsTable` soft sentinel), `schemaHash` (length-prefixed sha256 of the ordered names — injection-safe framing, replacing the chunk-C present-column fingerprint), `readSchemaHash`/`recordSchemaHash` (the poll-loop hook; mismatch → WARN + re-read + watermarks preserved), and `ProbeStatus` (read-only session/message/part `COUNT(*)` + latest migration for AC#8, degrading gracefully on a foreign DB).
- `adapter.go` (218 lines) — the registered `Adapter` (mirrors codex): `New`/`Name`/`Format`, `Scan` (records `scanCursor` even on cancel), `Tail` (resumes from `scanCursor` or cold-`snapshotCursor` HEAD), `ParseCursor`, `coerceCursor`, `snapshotCursor` (HEAD watermarks via `maxID`/`maxTimeUpdated` + real schema hash), `Factory`, `init()→adapters.Register(Format, Factory)`, `var _ canonical.Adapter`.
- Tests (NEW): `payloads_test.go`, `migrations_test.go`, `adapter_test.go` (construction + cursor), `adapter_lifecycle_test.go` (Scan/Tail/snapshot lifecycle), `cmd/ai-viewer-ingest/discovery_test.go` (codex probe tests, split from `sources_test.go`).

Files (MODIFIED):

- `mapper_turn.go` — `defaultPayloadURI` now delegates to `payloads.go:buildPayloadURI` (byte-identical; the chunk-B mapper goldens are unchanged, confirmed by the full-repo `go test`).
- `tailer.go` / `tailer_changes.go` — `coerceScanCursor` reduced to pure cursor-shaping (Tables/Version); the REAL migration-name hash is recorded by the new `recordSchemaHash`, called by `scanLoop`/`tailLoop` after `introspectAll`. The chunk-C `schemaFingerprint` placeholder is fully removed.
- `cmd/ai-viewer-ingest/sources.go` — named import of the opencode adapter (registers via `init()` AND exposes `ProbeStatus`), an `opencode` probe entry (`opencodeDBPath(home)`, a regular-file `os.Stat`), and a `case "opencode"` rich-attrs branch logging `sessions`/`messages`/`parts`/`latest_migration` (best-effort: a `ProbeStatus` error logs `probe_error` and still registers the source). The discovery counters + path helpers were extracted to a new `discovery.go` to bring `sources.go` back under the 400-line budget (it was already 464 at HEAD; the split also reduces that pre-existing overage).

Key decisions locked (honoring the recorded SOW/spec):

- **Scan→Tail cursor hand-off**: `Scan` records the final watermark on the instance even on ctx-cancel; `Tail` resumes from it. A cold `Tail` (no preceding `Scan`) snapshots current HEAD per table (`maxID`+`maxTimeUpdated`) + records the schema hash, so it follows from NOW (the SQLite analogue of codex stat'ing EOF). Re-emission is absorbed by idempotent upserts.
- **Real schema hash**: `sha256` of the `__drizzle_migrations.name` list ordered by `id ASC` (application order), length-prefixed so the digest is unambiguous regardless of name content. On a tail-time mismatch the loop logs a structured WARN, re-reads, and CONTINUES without resetting watermarks (column drift is per-column via the dynamic SELECT) — spec adapter-opencode.md §"Cursor". A missing `__drizzle_migrations` (foreign/old DB) leaves the hash empty and degrades gracefully.
- **Payload-URI grammar home**: `payloads.go` is the single source of truth; the mapper default delegates to it; behavior is byte-identical so chunk-B goldens are unchanged.
- **Probe reporting + graceful degradation**: `ProbeStatus` opens read-only, `COUNT(*)`s the three tables, reads the latest migration; a missing table → count 0 + soft error (not a hard failure), a hard open failure → returned so discovery logs it but the source still registers.

Carried-forward chunk-C notes resolved:

1. **`buildSelectByID` reachability**: KEEP (not dead code). It is reached by `scanTableDelta` (store_query.go:96-97) when `!s.has("time_updated")`. The migration history shows `time_updated` is part of the base `Timestamps` mixin on all four tracked tables across the entire observed schema (adapter-opencode.md lists `time_updated INTEGER NOT NULL` for session/message/part/session_message), so on every observed schema `time_updated` IS universal — but the fallback remains a genuine, tested backward-compat safeguard (tailer tests + `TestPureHelpers` cover it). It should NOT be flagged for removal.
2. **`schemaFingerprint` placeholder**: fully removed. `coerceScanCursor` no longer computes any hash; the real `__drizzle_migrations`-name digest is recorded by `recordSchemaHash` at scan/tail/snapshot start.

Gates (run 2026-05-30): `go build ./...` exit 0; `go vet ./internal/adapters/opencode/... ./cmd/ai-viewer-ingest/...` exit 0; `golangci-lint` **0 issues**; `gosec -severity medium -confidence medium ./...` exit 0 (added one justified `// #nosec G202` on the `__drizzle_migrations` name read — fixed package constant via `quoteIdent`, never user input); `go test -race -cover` pass — **opencode 91.8%** (up from chunk-C's 91.6%, no regression), cmd unchanged at 47.8%; full-repo `go test -race ./...` all pass (mapper goldens intact); `scan-secrets.sh` PASS. Every new/modified `.go` file ≤ 400 lines.

## Validation

(Empty placeholder. Filled at SOW close.)

## Reviews

(Empty placeholder. Filled as external reviewers run.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
