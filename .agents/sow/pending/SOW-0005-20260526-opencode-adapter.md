# SOW-0005 - opencode adapter (read-only SQLite + cumulative-token deltas + schema-drift tolerance)

## Status

Status: open

Sub-state: awaits operator approval before moving to current/. Prerequisite: SOW-0001 Phase 1 Foundation completed (canonical event types + ingest pipeline + store) — this SOW reuses that infrastructure.

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
2. **Read-only enforcement asserted in tests.** The adapter's connection helper opens with `mode=ro&_pragma=query_only(true)`; an explicit unit test invokes the helper and then attempts `INSERT`, `UPDATE`, `DELETE`, `PRAGMA wal_checkpoint`, `VACUUM`, and `ATTACH ... 'rwc'` against the returned `*sql.DB` and asserts every one returns an error. **Verification**: `internal/adapters/opencode/readonly_test.go` runs all six attempted-write probes and asserts errors; CI's gates include this test.
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

(To be filled by the assistant picking this SOW up. Required before moving to `current/`.)

## Implementation

(Empty placeholder. Filled as chunks complete.)

## Validation

(Empty placeholder. Filled at SOW close.)

## Reviews

(Empty placeholder. Filled as external reviewers run.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
