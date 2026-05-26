# SOW-0002 - Cross-Format Data Model Analysis

## Status

Status: completed

Sub-state: analysis delivered 2026-05-26 in a single working session per operator direct request ("I expect you will do an analysis so that you build a data model that can satisfy all ingestion pipelines of all agents"). Lands in done/ on creation because the work and its artifacts were produced inline.

## Requirements

### Purpose

Establish an authoritative, evidence-based understanding of every source format ai-viewer must ingest (ai-agent v2, ai-agent v3, claude-code, codex, opencode), and produce a unified canonical data model that satisfies all five — before any production code is written. The per-client specs and the unified canonical spec become the contract that adapter implementations are judged against.

### User Request

Operator instruction on 2026-05-26 (verbatim where given):

> "I expect you will do an analysis so that you build a data model that can satisfy all ingestion pipelines of all agents, maintain a spec file per client and the final model."

### Assistant Understanding

Facts:

- Five source formats target ai-viewer Phase 1+: ai-agent v2 (legacy `.json.gz`), ai-agent v3 (split jsonl + payloads/), claude-code (cwd-scoped jsonl), codex (rollout `.jsonl`), opencode (single SQLite database).
- Operator's workstation has real-world fixtures for every format: 294,316 v2 files, 17,356 v3 jsonl files, 3,614 claude-code jsonl files, 2,585 codex rollouts, a 3.9 GB opencode.db.
- Upstream source code is available locally for every format: ai-agent at `~/src/ai-agent.git/`, claude-code (reverse-engineered) + codex + opencode at `/opt/baddisk/monitoring/repos/ai/`.
- The bootstrap adapter specs (`adapter-<name>.md`) were sketches; not evidence-based.
- The canonical-events.md and data-model.md from bootstrap were correct in shape but missing concepts surfaced by the actual formats (cache tokens, indefinite-running sessions, reasoning kind split, provider aliases, compaction as first-class, etc.).

Inferences:

- Five formats require five independent investigation streams. Parallelizing via subagents is the natural shape.
- The canonical model has to be **wider** than any single format, with NULL-able columns where adapters don't populate, rather than per-format extras_json bloat.

Unknowns (resolved by the analysis):

- Whether v3 already carries explicit child-side `parentSessionId` — RESOLVED: yes, 96.8% of sub-agents.
- Whether claude-code stores sub-agents inline or out-of-line — RESOLVED: out-of-line in `<sessionId>/subagents/agent-<agentId>.jsonl` with sidecar `.meta.json`.
- Whether codex files are append-only or atomically rewritten — RESOLVED: pure append, no atomic rename.
- Whether opencode's token counts are per-step or cumulative — RESOLVED: cumulative; adapter MUST compute deltas.
- Whether the canonical OpKind set covers every observed kind — RESOLVED: it didn't; `system` and `compaction` were added.

### Acceptance Criteria

1. One evidence-based adapter spec per format under `.agents/sow/specs/adapter-<name>.md`. Each spec replaces the bootstrap sketch with content cited from real files + upstream source. **Verification**: file diffs land in this SOW's commit; each spec exceeds 400 lines and cites file:line evidence.
2. Updated `.agents/sow/specs/canonical-events.md` absorbing every concept gap surfaced by the analysis. **Verification**: cross-walk table in the spec covers all five formats.
3. Updated `.agents/sow/specs/data-model.md` with the corresponding schema changes and a cross-format compatibility matrix. **Verification**: compatibility matrix lists every column × adapter combination explicitly.
4. Specs commit as one atomic change so the discipline contract's "spec deltas land first, in one commit" is satisfied.

## Analysis

### Methodology

Five parallel deep-research subagents (one per format), each tasked with: (a) reading real files + upstream source code, (b) writing the per-client spec from evidence, (c) reporting back any canonical-model gaps. The master assistant synthesized the returns into updated canonical specs.

Total spec word count produced: ~29,500 words across 7 files (5 adapter specs + canonical-events.md + data-model.md).

### Findings — Per-Adapter Summary

**ai-agent v3** (spec: `adapter-aiagent-v3.md`, 719 lines, 6,765 words):

- `ts` is ISO-8601 string, not int µs (the bootstrap sketch claimed int). Adapter converts at boundary.
- `parentSessionId` already on child `session_start` (96.8% observed). Adapter has a fast-path today.
- `originId` is the root session id (not just alias for sessionId). Canonical `root_session_id` comes directly from `originId`.
- Op kinds observed: `llm | tool | session | system` (no `reasoning` or `internal`). Canonical needs both `system` AND retained `internal`/`reasoning` for other adapters.
- Aborted `.gz.tmp-*` payload artifacts exist; adapter trusts only `payloadRefs[].path`.
- Canonical gaps surfaced: cache tokens, abandoned/interrupted statuses, sha256 on payloads, callPath, finalReport/pluginMetas extras, history_compaction differentiation.

**ai-agent v2** (spec: `adapter-aiagent-v2.md`, 456 lines, 5,471 words):

- Top-level shape is `{version, reason, opTree}` only — three fields. Most metadata lives inside `opTree`.
- Sub-agent sessions are **NOT** independently persisted; embedded in parent opTree only.
- All descendants of a root share the same filename (`<originTxnId>.json.gz`); last writer wins.
- Two schema versions on disk (`version: 1` and `version: 2`); adapter tolerates both.
- 60-min backfill perf target NOT at risk: 294K files, 25.4 GB total, p50 10 KB, p99 1.2 MB, max 151 MB compressed; 5.5 min single-thread. Files > 50 MB require streaming gzip + streaming JSON decode.
- Op kind `system` is real and frequent. Tool accounting uses chars, not bytes (canonical needs `chars_in`/`chars_out` columns).
- 29 zero-byte files + 2 orphaned `.tmp-*` files observed; adapter skips both.

**claude-code** (spec: `adapter-claude-code.md`, 795 lines, 6,871 words):

- Sub-agent linkage via filesystem layout (sidecar `<sessionId>/subagents/agent-<id>.jsonl` + `.meta.json::toolUseId`), NOT inline. Critical: sub-agent sessionId equals parent's → adapter synthesizes `NativeID = <parentSessionId>:agent:<agentId>`.
- Zero cost data anywhere; ai-viewer computes via `pricing.go`.
- No native session-end signal; sessions resumable indefinitely. Adapter never emits `SessionFinalizedEvent` for claude-code; canonical adds the `running` indefinite state.
- Compaction is explicit (`system.subtype="compact_boundary"`) with `compactMetadata`. Canonical adds `OpKind='compaction'` first-class.
- No native turn/op boundaries; adapter synthesizes turns from message-chain pivots.
- Encoded-cwd algorithm: `cwd.replace(/[^a-zA-Z0-9]/g, '-')` + hash suffix when sanitized > 200 chars. Lossy; authoritative cwd lives in each record's `cwd` field. Canonical promotes `sessions.cwd` to a first-class column.

**codex** (spec: `adapter-codex.md`, 551 lines, 4,950 words; upstream commit `openai/codex@8a94430b`):

- Layout is BOTH sharded AND flat. Modern: `~/.codex/sessions/YYYY/MM/DD/rollout-...UUIDv7.jsonl`. Legacy (pre-mid-2025, 19 files): flat `~/.codex/sessions/rollout-YYYY-MM-DD-uuid.json`. Upstream code only handles `.jsonl`. Bootstrap sketch was wrong on both counts.
- Pure append-only writes, NO atomic rename. Watcher byte-tails JSONL.
- Filename uses LOCAL time, body uses UTC. Adapter trusts the body timestamp.
- Turn boundary evolved (`turn_context` → `task_started`/`task_complete` with `turn_id`). Adapter accepts both.
- Persistence has Limited vs Extended modes; `exec_command_end.stdout/stderr` are blanked at write time per `policy.rs:51-59`.
- Reasoning split: `agent_reasoning` (summary) vs `agent_reasoning_raw_content` (raw). Canonical adds `ops.reasoning_kind = 'summary' | 'raw'`.
- Sub-agents/forks: separate rollout files linked via `parent_thread_id` or `forked_from_id`. Canonical adds `Kind='fork'` session kind.
- No cost field; pricing computed.

**opencode** (spec: `adapter-opencode.md`, 567 lines, 5,586 words; upstream commit `anomalyco/opencode@2b3ddf9f`):

- Storage is a single SQLite DB (3.9 GB on workstation, 20 migrations applied).
- Schema is `session → message → part` with no native `turn` or `op` concept. Adapter synthesizes: Turn = assistant message; LLM-Op = `step-start`/`step-finish` pair; Tool-Op = tool part nested under current step.
- **`step-finish` token counts are CUMULATIVE within a message, not per-step.** Adapter MUST compute deltas before emitting (canonical events carry deltas only).
- Sub-agent linkage dual (`session.parent_id` + `part.data.state.metadata.sessionId` on `task` tool parts; 100% consistent observed). Adapter prefers `parent_id`.
- IDs are Sonyflake-style time-prefixed; lexicographic sort = time sort. Cursor uses `MAX(id)` (PK-indexed O(log n)).
- Schema evolves between opencode versions. Adapter reads `PRAGMA table_info` at startup; tolerates missing columns.
- Multi-provider with user-defined provider aliases (`llm-netdata-cloud`, etc.). Canonical adds `sessions.provider_alias` + `catalog_providers` table.
- Polling cadence: 2s idle / 500ms active / 250ms after WAL-mtime fsnotify change.

### Findings — Canonical Model Updates

Updates landed in `canonical-events.md` and `data-model.md`:

| Canonical change | Driven by |
|---|---|
| `OpKind` adds `system`, `compaction` | v3 + v2 (system), claude-code + codex (compaction) |
| `SessionStatus` adds `abandoned`, `interrupted`; documents indefinite-`running` | v3 + v2 (orphans/mid-turn deaths), claude-code (no terminal signal) |
| `SessionKind` adds `fork` | codex (forked_from_id) |
| `TurnFinalized` / `OpFinalized` add `tokens_cache_read`, `tokens_cache_write` | v3, claude-code, opencode |
| `OpStarted` adds `provider_alias`, `reasoning_kind` | opencode (alias), codex (reasoning split) |
| `OpFinalized` adds `chars_in`, `chars_out` | v2 (tool accounting uses chars) |
| `OpFinalized` adds `Status='truncated'` | codex limited-mode payloads |
| `SessionStarted` adds `cwd`, `call_path`, `RootNativeID` explicit, `ParentOpKey` | claude-code + codex + opencode (cwd); v3 (callPath); v3 (originId == root) |
| `PayloadRef` adds `SHA256` | v3 records it; future codex/opencode may |
| `sessions.last_activity_ts` column | UI needs "stale running" filter for claude-code indefinite sessions |
| `catalog_providers` table | opencode multi-provider |
| `catalog_cwds` table | UI grouping for claude-code/codex/opencode |
| Cross-format compatibility matrix | Documents which adapter populates which column |

### Sources Checked

- Real files: 10 random sessions per format × 5 formats = 50 files inspected.
- Upstream source code: ai-agent v3 evidence writer/reader; ai-agent v2 persistence.ts/session-tree.ts; codex `openai/codex@8a94430b` rollout recorder + policy; opencode `anomalyco/opencode@2b3ddf9f` Drizzle schema + 20 migrations; claude-code via three reverse-engineered forks + frozen TS sourcemap mirror.
- Existing specs: bootstrap sketches under `.agents/sow/specs/adapter-*.md`; the now-updated `canonical-events.md` and `data-model.md`.

### Risks

- **R1 — Spec drift between this analysis and adapter implementations.** Mitigation: `scripts/spec-drift.sh` (built in SOW-0013) lints registered REST endpoints, SSE event types, SQLite columns, canonical event fields, and adapter probes against their corresponding specs. Per `project-specs-sync` skill, specs change first.
- **R2 — Source-format evolution.** Each upstream may add new record types or change semantics. Mitigation: adapters tolerate unknown fields (open-schema parsing); fuzz targets on every parser (SOW-0011) catch new shapes.
- **R3 — Canonical model still misses something.** Mitigation: SOW-0001 Chunk 1 includes a check that every adapter compiles against the canonical event types without `interface{}` escape hatches.

## Pre-Implementation Gate

(N/A — the work was the analysis itself; no implementation chunks. The analysis output IS the SOW's deliverable.)

## Implementation

### Chunks delivered (single commit, 2026-05-26)

1. **5 adapter specs rewritten from evidence**:
   - `.agents/sow/specs/adapter-aiagent-v3.md` (719 lines)
   - `.agents/sow/specs/adapter-aiagent-v2.md` (456 lines)
   - `.agents/sow/specs/adapter-claude-code.md` (795 lines)
   - `.agents/sow/specs/adapter-codex.md` (551 lines)
   - `.agents/sow/specs/adapter-opencode.md` (567 lines)
2. **Canonical model updated**:
   - `.agents/sow/specs/canonical-events.md` — full rewrite incorporating gaps.
   - `.agents/sow/specs/data-model.md` — full rewrite with cross-format compatibility matrix and sub-agent linkage strategy section.
3. **specs/index.md** updated with the new cross-format section.

### Deviations from plan

None.

## Validation

- Per-spec validation: each subagent independently reported cross-walk gaps; the master synthesized them into canonical updates. No gap was left undocumented in either the canonical events spec or the data-model spec.
- Cross-walk consistency: every concept observed in ≥2 adapters is a first-class column; concepts in 1 adapter live in `extras_json`.
- The Cross-Format Compatibility Matrix in `data-model.md` documents `✓` / `~` / `n/a` for every column × adapter combination, so future-assistant can see at a glance what to populate.

## Reviews

External review TO BE RUN on the canonical specs before SOW-0001 Chunk 2 (CI scaffolding) lands. Recommendation: spec/design review pattern (codex + gemini + mimo in parallel per the second-opinions skill); not blocking the move of SOW-0001 to `current/` but blocking the first adapter implementation.

This SOW marks completed on the strength of evidence-based per-format research + canonical synthesis. External reviewers will be invoked on the produced specs in the next session.

## Outcome

ai-viewer now has a complete, evidence-based, durable description of every source format and how each maps to a unified canonical model. Every downstream SOW (M2-M5 adapters, UI, statistics) can plan against this contract instead of re-discovering the formats. Future-assistant after compaction or in a new session can read the per-client specs + the canonical model and have full ground truth.

## Lessons / Follow-Ups

- **Phase 1 Acceptance Criterion #3** (60-min backfill) is NOT at risk for v2 based on the analysis (single-thread bench shows 5.5 min; parallelized < 60 min comfortably). The v3 adapter's append-only nature means backfill scales with file count more than size.
- **Cumulative-to-delta** conversion in opencode is the kind of source quirk that, if missed, would silently triple-count tokens. The canonical events spec now documents the adapter responsibility explicitly so the implementing assistant cannot forget.
- **Sub-agent linkage** varies wildly across formats. The canonical model's `parent_session_id` + `root_session_id` columns work for all five — but each adapter implements a different resolver. The cross-walk table in `data-model.md` is the single source of truth.
- **Cache tokens** are critical for cost analysis (prompt caching is ~10× cheaper than full input). Adding them as first-class columns was non-optional once two adapters surfaced them.
- Next follow-up: run external spec/design reviewers on the produced canonical specs before adapter implementation begins.
