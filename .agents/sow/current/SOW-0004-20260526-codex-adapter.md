# SOW-0004 - codex adapter (Scan + Tail + cursor + forks + reasoning + turn-boundary evolution)

## Status

Status: in-progress

Sub-state: active in `current/`. Approved under the operator's blanket Phase-2 backlog sign-off ("deliver them all, any order"). Prerequisite met: SOW-0001 Phase 1 Foundation is in `done/` (canonical event types + ingest pipeline + store + adapter registry + pricing + CI gates) — this SOW reuses that infrastructure unchanged. Pre-Implementation Gate filled 2026-05-30 (see below).

## Requirements

### Purpose

Deliver the codex adapter end-to-end: Scan + Tail of OpenAI Codex CLI rollouts under `~/.codex/sessions/YYYY/MM/DD/rollout-...UUIDv7.jsonl`, faithful mapping to the canonical event model per `adapter-codex.md`, durable byte-offset cursor, fixture coverage of every persisted `RolloutItem` variant (modern format), tolerance for the wide CLI-version range observed (0.61.0 → 0.125.0), auto-discovery probe so operators with codex installed get the source wired automatically, and a fuzz target on the JSONL parser. Outcome: the operator can see every codex session they have run on this workstation, with forks and sub-agent spawns linked into the topology view, reasoning rendered as first-class ops, and compaction surfaced on the timeline.

### User Request

From the operator's 2026-05-26 milestone list (recorded in conversation while planning post-Phase-1 work): "Add claude-code, codex, and opencode adapters next, one SOW each, so each can be reviewed and scoped independently." This SOW is the codex slice of that instruction and inherits its full scope (parser + Scan + Tail + cursor + tests + fixtures + auto-discovery + spec sync).

### Assistant Understanding

Facts:

- The operator's workstation has 2,585 codex rollout files: 2,566 modern sharded `~/.codex/sessions/YYYY/MM/DD/rollout-...UUIDv7.jsonl` plus 19 legacy flat `~/.codex/sessions/rollout-YYYY-MM-DD-uuid.json` files dated June-July 2025 (`adapter-codex.md` §"Status", §"Legacy", §"References").
- Modern format is the priority; legacy `.json` is deferred behind a `legacy_json_format=true` flag with one informational `SourceError` per file at first sight (`adapter-codex.md` §"Legacy `.json` layout"). Upstream Codex no longer reads the legacy format (`openai/codex @ 8a94430b :: codex-rs/rollout/src/list.rs:898`).
- Writes are pure append, no atomic rename, no `fsync` (`adapter-codex.md` §"Atomicity & Write Pattern"; `codex-rs/rollout/src/recorder.rs:1346-1369, 1610-1620, 1622-1654`). Byte-offset tail is correct; the watcher seeks back to the last `\n` on partial reads.
- Filename uses LOCAL time (`recorder.rs:1325-1354, 1338-1339`); the line `timestamp` field uses UTC (`protocol.rs:2849-2854`). The adapter trusts the body timestamp; filename time is for sharding only.
- Turn boundary evolved across CLI versions: older sessions (cli 0.61.0) carry only `turn_context`; newer (>= ~0.93.0) emit both `turn_context` and `task_started`/`task_complete` with `turn_id`. The adapter accepts either as the boundary signal (`adapter-codex.md` §"Mapping to Canonical Events" rules 2-4, 22; "Edge Cases" #3).
- Persistence has Limited (default) vs Extended modes (`codex-rs/rollout/src/policy.rs:135-220`); payloads such as `exec_command_end.stdout/stderr/formatted_output` are blanked at write time (`policy.rs:51-59`). Adapter cannot recover full output and must surface this as expected behavior, not an error.
- Reasoning is split: `response_item.reasoning` (durable model state) vs `event_msg.agent_reasoning` (visible summary) vs `event_msg.agent_reasoning_raw_content` (raw CoT). Canonical adds `ops.reasoning_kind = 'summary' | 'raw'` (per `canonical-events.md` updates in SOW-0002); the adapter emits the `response_item` form only and uses `event_msg` reasoning entries for `LogEntry` enrichment to avoid duplication (`adapter-codex.md` §"Per-file state machine" rule 8).
- Sub-agents and forks produce SEPARATE rollout files under the same `sessions/YYYY/MM/DD/` tree, linked via `session_meta.payload.source.subagent.thread_spawn.parent_thread_id` (sub-agent) or `session_meta.payload.forked_from_id` (fork). Both map to canonical `parent_session_id`; `extras_json.relationship` distinguishes (`adapter-codex.md` §"Sub-Agent Linkage"; "Canonical Model Gaps" #5).
- No cost field in any rollout; pricing computed via `internal/pricing/` with `provider="openai"` and `model=turn_context.payload.model`.
- Phase 1 Foundation (SOW-0001) delivers `internal/canonical/`, `internal/ingest/`, `internal/store/`, `internal/adapters/registry.go`, the `canonical.Adapter` interface, pricing catalog, fixture sanitization tooling, and CI gates that this SOW reuses unchanged.

Inferences:

- Modern format covers 99.3% of observed files (2,566 / 2,585); the legacy `.json` deferral is the right tradeoff for v1 and produces no false negatives — operators with old codex installs get one log entry per legacy file, not silent loss.
- Backfill of 2,566 modern files will be I/O-bound; expected wall-clock under 5 min single-threaded based on typical file sizes (10 KB - 5 MB per rollout).
- The unknown-variant tolerance contract is critical because the Rust upstream uses `#[serde(other)]` catch-alls throughout (`models.rs:901`); the Go decoder must mirror this behavior or every CLI release breaks the adapter.

Unknowns:

- Exact wall-clock perf on the operator's 2,566 modern rollouts — measured during implementation. Below 5 min single-thread per inference.
- Whether legacy `.json` ingestion is wanted in this SOW or deferred to a follow-up. Default per `adapter-codex.md`: deferred to a Phase 2.5 follow-up SOW; this SOW emits one `SourceError` per legacy file.
- Whether any sandbox mode beyond `workspace-write` / `danger-full-access` / `read-only` appears in real files. Spec records the three observed (68/30/2 proportions); resolved by `jq` aggregation when fixtures are curated.

### Acceptance Criteria

1. `internal/adapters/codex/` package compiles, lints clean, and is registered in `internal/adapters/registry.go`. **Verification**: `go build ./...` exits 0; `golangci-lint run` exits 0; `internal/adapters/registry_test.go` asserts the adapter is enumerable by name `"codex"`.
2. Scan + Tail correctly ingests every persisted `RolloutItem.type` variant listed in `adapter-codex.md` (`session_meta`, `turn_context`, `response_item`, `event_msg`, `compacted`) and every nested persisted `payload.type` (per `policy.rs:67-85` and `:135-220`). Unknown top-level `type` and unknown nested `payload.type` produce exactly one `SourceError` per variant per session. **Verification**: golden tests per scenario (see #5) plus a tolerance test feeding 10 synthetic unknown variants.
3. Turn boundary inference works for both the old (`turn_context`-only) and new (`task_started`/`task_complete` with `turn_id`) formats and for mixed sessions. **Verification**: two golden tests — one fixture from cli 0.61.0, one from cli ≥0.93.0 — both produce identical canonical `TurnStartedEvent`/`TurnFinalizedEvent` semantics (same number of turns, same boundaries).
4. Reasoning split is honored: `response_item.reasoning` produces canonical `OpKind='reasoning'` with `reasoning_kind` set from inspecting the record (`summary` if only `summary[]` is non-empty, `raw` if `content[]` carries text or `encrypted_content` is set). `event_msg.agent_reasoning*` produces a `LogEntry` only, never a duplicate op. **Verification**: golden test on a fixture with both forms in the same file asserts exactly one reasoning op per record-pair and the correct `reasoning_kind` discriminator.
5. Golden tests cover at minimum: (a) happy-path single-session no sub-agents (new format), (b) old-format session (cli 0.61.0) with `turn_context`-only boundaries, (c) sub-agent rollout file with `source.subagent.thread_spawn` link to parent, (d) fork rollout file with `forked_from_id`, (e) session with mid-stream `compacted` line + accompanying `response_item.context_compaction`, (f) session with `event_msg.exec_command_end` carrying truncated `aggregated_output` (Extended mode), (g) session with `turn_aborted` mid-turn (`reason="interrupted"`), (h) crash mid-stream — file with no `task_complete`, stale mtime (per state-machine rule 23 — synthetic finalize). **Verification**: each golden test reads a sanitized fixture under `testdata/codex/<scenario>/`, runs the adapter to completion, and diffs the emitted canonical event stream against a committed `.golden.json`.
6. Byte-offset cursor is durable across restart with zero duplicate events and zero gaps. Cursor handles file-truncation defensively (re-scan from 0 + `SourceError`). **Verification**: integration test that ingests half a fixture, persists cursor, restarts, ingests rest, asserts identical end state to a one-shot ingest; second integration test truncates a fixture mid-stream and asserts the re-scan path emits the expected `SourceError`.
7. Fuzz target on the JSONL line parser passes the SOW-0001 gate (`go test -fuzz=Fuzz... -fuzztime=30s` zero crashes). **Verification**: `internal/adapters/codex/parser_fuzz_test.go` is added; CI runs it on the standard fuzz budget.
8. Auto-discovery probe detects `~/.codex/sessions/` (and `$CODEX_HOME/sessions` when set) at startup, walks the `YYYY/MM/DD/` shards, registers the source automatically, and exposes counts in `/api/health`. The probe also detects the 19 legacy `.json` files but does NOT ingest them in this SOW (it emits one informational log per file). **Verification**: unit test on the probe with a tmpdir layout containing both modern and legacy files; manual run on the operator's workstation registers the real source and `/api/sources` reports both counts separately.

## Analysis

Sources checked:

- `.agents/sow/specs/adapter-codex.md` (full spec, all sections) — primary contract.
- `.agents/sow/specs/canonical-events.md` — target event types, including `OpKind='reasoning'` with `reasoning_kind` discriminator, `Kind='sub_agent'` and `'fork'` SessionKinds, `OpKind='compaction'`, `Status='truncated'` for codex Limited-mode payloads.
- `.agents/sow/specs/data-model.md` — SQLite schema, especially `sessions.extras_json.sandbox`, `turns.extras_json.codex_turn_id`, cross-format compatibility matrix.
- `.agents/sow/done/SOW-0002-20260526-cross-format-data-model-analysis.md` — analysis context confirming codex's append-only / non-fsync semantics and reasoning split.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — infrastructure the adapter plugs into.
- Real evidence on the operator's workstation: `~/.codex/sessions/` (2,566 modern + 19 legacy files as of 2026-05-26).
- Upstream source at `openai/codex @ 8a94430bb273623be42b68f144f1ab1df343bb53` — `codex-rs/rollout/{lib.rs,recorder.rs,list.rs,policy.rs}` and `codex-rs/protocol/src/{protocol.rs,models.rs}` per `adapter-codex.md` §"References".

Current state:

- SOW-0001 (in-progress) delivers canonical event types, SQLite store, ingest pipeline, adapter registry, pricing catalog, fixture sanitization tooling, CI gates, and the ai-agent v3/v2 adapters end-to-end. This SOW assumes that infrastructure is in place; if SOW-0001 is not yet completed, this SOW remains in `pending/`.
- No `internal/adapters/codex/` package exists yet (the bootstrap only documented the format).

Risks:

- **R1 — Legacy `.json` format deferral.** 19 files on the operator's workstation will not be ingested in this SOW. Mitigation: explicit one-time informational log per file; tracked as a Phase 2.5 follow-up SOW filed under "Lessons / Follow-Ups" at SOW close. Acceptance #8 verifies the probe surfaces the count so the operator can see what is being skipped.
- **R2 — Codex CLI version drift.** Observed range 0.61.0 → 0.125.0 across 2,566 files. Schema additions are common, removals rare. Mitigation: defensive parsing with `serde(other)`-equivalent in Go decoder; one `SourceError` per unknown variant per session; acceptance #2 pins this. The reasoning-split test and the old-vs-new turn-boundary test (acceptance #3, #4) exercise version-drift directly.
- **R3 — Turn boundary heuristic ambiguity.** When `task_started` and `turn_context` arrive in different orders, or when `turn_context.turn_id` is absent, the adapter falls back to "user message → next user message" heuristic (`adapter-codex.md` "Edge Cases" #3). Mitigation: explicit fixture covering the absent-turn_id case; the `LogEntry` channel surfaces ambiguity rather than swallowing it.
- **R4 — Sandbox / permission / policy metadata loss.** `turn_context` carries rich `approval_policy`, `sandbox_policy`, `permission_profile`, `network` metadata that has no canonical equivalent. Mitigation: store under `turns.extras_json.sandbox` snapshotted per turn and under `sessions.extras_json.sandbox` deferred from session_meta.source; spec already documents this in §"Canonical Model Gaps" #3.
- **R5 — Sensitive content in fixtures.** Real codex sessions contain operator names in `base_instructions.text`, `git.repository_url` like `git@github.com:netdata/...`, and free-form prompts. Mitigation: every committed fixture under `testdata/codex/` runs through `scripts/sanitize-fixture.sh` (built in SOW-0001 Chunk 5); CI's secret-scan gate is the safety net, not the primary control.

## Pre-Implementation Gate

Filled 2026-05-30. Evidence verified against ground truth (a readiness-briefing subagent's claims were re-checked file-by-file; two of its claims were corrected — see "Open Decisions" C#3 and "Spec deltas").

### Problem / model

Not a bug — an additive feature. Deliver a new `codex` adapter that projects OpenAI Codex CLI rollout JSONL (`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-…UUIDv7.jsonl`, default `~/.codex`) onto the canonical event model. The adapter is a pure, deterministic projection from persisted `RolloutItem` variants → canonical events, structurally mirroring the just-merged `claude_code` adapter (byte-offset JSONL tailing, fail-soft discovery, symlink containment, fuzz + golden tests). The genuinely codex-specific logic is concentrated in the per-file state machine (turn-boundary dual-format, reasoning split, tool-namespace heuristic, token rollup, sub-agent/fork linkage, compaction).

### Evidence reviewed

- `.agents/sow/specs/adapter-codex.md` (full, 552 lines) — primary contract; state-machine rules #1–24, edge cases #1–18, tabular summary.
- `internal/adapters/claude_code/*.go` (non-test) — the structural template; `adapter.go:19` (`const Format`), `adapter.go:193` (`init()→Register`).
- `internal/adapters/registry.go` — `Register`/`Get`/`Formats` self-registration contract (panics on dup/empty/nil; init-time only).
- `.agents/sow/specs/{adapter-contract.md,canonical-events.md,data-model.md}` — codex target fields (`reasoning_kind`, `Kind=fork|sub_agent`, `OpCompaction`, `Status=truncated`, `extras_json.sandbox`, `turns.extras_json.codex_turn_id|ttft_ms`) all pre-exist (SOW-0002).
- `cmd/ai-viewer-ingest/sources.go` — `autoDiscoverSources` probe table (`:88`), `claudeProjectsDir`/`countProjectDirs` pattern to mirror.
- SOW-0004 Requirements / Acceptance #1–8 / Risks R1–R5.
- Real data: `~/.codex/sessions/` — 2,566 modern `.jsonl` (sharded) + 19 legacy `.json` (root).

### Affected contracts & surfaces

- **NEW** package `internal/adapters/codex/` (file-for-file mirror of `claude_code`; see "Patterns to reuse").
- **ADDITIVE** `cmd/ai-viewer-ingest/sources.go`: a 4th probe (`$CODEX_HOME/sessions`, default `~/.codex/sessions`) + `countRolloutFiles`/`countLegacyJSON` helpers mirroring `countProjectDirs` (acceptance #8). The binary already blank-imports the adapters package set for side-effect registration; codex registers via its own `init()`.
- **ADDITIVE** `testdata/codex/<scenario>/` fixtures + golden files.
- **NO** change to `internal/canonical/`, `internal/ingest/` (writer/resolver/catalog), `internal/store/` schema (no migration), or sibling adapters. Every canonical field codex needs already exists.

### Spec deltas (LANDED before any test or code — committed with this gate)

1. `adapter-codex.md` rule #4 (token accounting): replaced the contradictory "delta-from-start of cumulative `total_token_usage`" with the **sum-of-per-call-`last_token_usage`** rollup (consistent with rule #17 + the "Token accounting nuance" para). Resolves the C#1 contradiction.
2. `adapter-codex.md` tabular summary (clean-EOF row): **removed** "clean EOF → `SessionFinalizedEvent(completed)`"; clean codex sessions now stay `running` (no per-session terminal signal). Stale-row threshold made explicit (`≥ 1 h`). Resolves C#3.
3. `canonical-events.md` codex bullet (status mapping): rewritten to the claude-code model — no clean finalize; only the synthetic `failed/incomplete` at ≥ 1 h stale. Removed the stray "24h" example (it had no basis in `adapter-codex.md`).
4. `data-model.md` status note: added `codex` to the "sources without a per-session terminal signal (claude-code, codex)" parenthetical.

### Patterns to reuse (verified claude_code → codex map)

- `doc.go` → rewrite for codex layout; `adapter.go` → `Format="codex"`, `init()→Register`, same `Scan→Tail` `scanCursor` handoff (load-bearing — keep verbatim).
- `cursor.go` → keep `Files` byte-offset map + `After` + truncation-defense **verbatim**; **drop** claude_code's sub-agent-deferral fields (`MetaSeen`/`Parked`/`Finalized`); **add** a `LegacyJSON` per-file `{ingested bool}` map.
- `parser.go` → keep the `unknownTypeError`/`errors.Is` dedup mechanism (codex needs it for BOTH unknown top-level `type` AND unknown nested `payload.type`); replace claude-code enum with codex `{timestamp,type,payload json.RawMessage}` envelope + discriminated payload sub-decode.
- `mapper.go`/`ops.go` → heaviest divergence (codex state machine, rules #1–24); keep `packSeq`, ts parsing, `firstUnknownType` dedup, the `advance(ts)` closure pattern.
- `scanner.go`/`tailer.go` → reuse `streamLines`/`readOneLine`/`drainToNewline`/oversized-line/byte-offset/truncation **verbatim**; replace projects-tree walk with a `YYYY/MM/DD` shard walker (`^rollout-.*\.jsonl$`); add new-date-shard-dir handling to the tailer; **drop** all meta/deferral machinery.
- `payloads.go` → reuse symlink-containment + `file://<abs>#L<line>` URI **verbatim**.

### Risk & blast radius

Purely additive; the only shared-surface touch is the additive `sources.go` probe. Premature-finalize risk (the bug class codex-review caught in claude_code) is **eliminated** by the C#3 decision (no clean-EOF finalize). Carries SOW Risks R1 (legacy `.json` deferral — 1 SourceError/file), R2 (CLI version drift 0.61→0.125 — `serde(other)`-equivalent tolerance, 1 SourceError per unknown variant per session), R3 (turn-boundary ambiguity — explicit fixtures), R4 (sandbox metadata → `extras_json`), R5 (fixture sensitive data — see below).

### Sensitive-data plan

Every committed fixture under `testdata/codex/` runs through `scripts/sanitize-fixture.sh`. Strip/pseudonymize: operator name in `base_instructions.text`; `git.repository_url` `git@github.com:netdata/…` → `git@github.com:example/example.git`; free-form `user_message`/prompt bodies; identity-revealing `cwd`; truncate `encrypted_content` (keep shape). Keep intact: schema shape, timestamps, token counts, `turn_id`/`call_id` correlation, `cli_version`, sandbox modes. Golden `expected.jsonl` rewrites the absolute root → `<ROOT>` placeholder (mirror claude_code `golden_test.go`). `scripts/gates.sh` secret-scan is the net, not the primary control.

### Implementation plan (chunked; each chunk = spec → failing tests → subagent impl → gates → integrate)

- **Chunk A** — types + `parser.go` (envelope + payload dispatch + unknown-variant tolerance) + `cursor.go` (byte-offset + `LegacyJSON`). Unit + fuzz seed.
- **Chunk B** — `mapper.go` + `ops.go` state machine (turn dual-format; reasoning split; tool-namespace heuristic; token rollup per C#1; dangling-op finalize at turn end; sub_agent/fork linkage; compaction). Heaviest; most unit tests.
- **Chunk C** — `scanner.go` + `tailer.go` (shard walker, byte-offset reuse, truncation defense, new-date-dir watch).
- **Chunk D** — `payloads.go` + `adapter.go` wiring + `init()` registration + `sources.go` auto-discovery probe + count helpers.
- **Chunk E** — fixtures (8 golden scenarios a–h) + `golden_test.go` + `parser_fuzz_test.go` + `adapter_restart_test.go` + cmd probe test.

### Validation plan (named test files → behaviors; mirrors claude_code test set)

- Acc #1 → `internal/adapters/registry_test.go` asserts `"codex"` enumerable + compile-time `var _ canonical.Adapter`.
- Acc #2 → `golden_test.go` (8 scenarios) + `scanner_test.go: TestScan_UnknownTypeTolerance`, `TestScan_UnknownPayloadTypeDedup` (≥10 synthetic unknowns, top-level AND nested; 1 SourceError per variant per session).
- Acc #3 → golden scenarios `b_old_turncontext` (cli 0.61) and `a_happy_new` (cli≥0.93) assert identical turn semantics; `mapper_test.go: TestMapper_TurnBoundaryOldVsNew`, `TestMapper_AbsentTurnIDFallback`.
- Acc #4 → `mapper_test.go: TestMapper_ReasoningKindSummary|_Raw|_EventReasoningIsLogOnlyNoDupOp`; golden with both forms in one file.
- Acc #5 → `golden_test.go` auto-discovers `testdata/codex/<scenario>/INPUT/`, diffs `expected.jsonl` (`-update-golden` regenerates). Scenarios: `a_happy_new`, `b_old_turncontext`, `c_subagent_threadspawn`, `d_fork`, `e_compaction`, `f_exec_truncated`, `g_turn_aborted`, `h_crash_stale`.
- Acc #6 → `adapter_restart_test.go: TestRestart_NoDupNoGap`, `TestScanThenTail_NoLossInWindow`; `scanner_test.go: TestScan_TruncationRescans`; `cursor_test.go` round-trip + `After`.
- Acc #7 → `parser_fuzz_test.go: FuzzParseLine`, `FuzzParseCursor` (30s budget; seed from sanitized real lines).
- Acc #8 → `cmd/ai-viewer-ingest/sources_test.go` (extend): tmpdir with modern shards + root legacy `.json`; assert source registered, modern + legacy counts separate; probe honors `$CODEX_HOME`.

### Artifact impact plan

Producer of codex canonical rows: the new adapter's `Scan` (backfill) + `Tail` (fsnotify). Refresh event: file append → fsnotify → byte-offset read. Repair path: cursor corruption or file truncation → full re-scan from offset 0 + `SourceError` (reuse claude_code truncation defense). Served by: existing presenter/REST (`/api/sessions`, `/api/ops`, `/api/health`, `/api/sources`) with **no new route** — codex rows flow through the same canonical schema. Discovery surface: `/api/sources` reports the codex source + modern/legacy counts (acceptance #8). No DB migration (schema unchanged).

### Open decisions — DECIDED by CTO (recorded; not escalated)

1. **Token accounting (C#1):** sum of per-call `last_token_usage` over the turn's attributed `token_count` events (session-level events with no `turn_id` attributed to the most-recently-active turn); cumulative `total_token_usage` → `CtxUsed` only. Spec rule #4 aligned. **Decided.**
2. **Session finalize (C#3):** codex follows the claude-code model — **no** `SessionFinalized(completed)` on clean EOF (sessions stay `running`, UI uses `last_activity_ts`); the only `SessionFinalized` is the synthetic `failed/incomplete` for a hanging-turn file mtime-stale ≥ 1 h (rule #23 / acceptance #5h kept). Eliminates premature-finalize risk; no acceptance criterion required clean-EOF→completed. **Decided.**
3. **Format string + source location:** `Format = "codex"`; source-flag form `codex:<sessions-dir>` where location = `$CODEX_HOME/sessions` (default `~/.codex/sessions`) — the directory walked, mirroring claude-code's location=walked-dir; cursor keys are `YYYY/MM/DD/rollout-*.jsonl` (and root legacy `*.json`) relative to it, stable across a `$CODEX_HOME` move. **Decided.**
4. **Legacy `.json` (R1):** `legacy_json_format=false` default; v1 emits exactly 1 informational `SourceError` per legacy file (cursor-suppressed thereafter), ingests none; a Phase-2.5 follow-up SOW is filed at close. **Decided.**

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
