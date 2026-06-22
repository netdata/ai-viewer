# SOW-0096 - Ingestion accuracy audit + invariant framework

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed; awaiting operator sign-off. Phase: Development (CTO-discretion review). This is a *measurement + framework* SOW, not a feature SOW. The deliverable is a list of known gaps, a per-adapter invariant set, and a Go+SQL+frontend mechanism that catches regressions on every CI run AND surfaces live-state drift in `/api/health`.

## Pre-Implementation Gate

### Problem / root-cause model

The CTO has a working app that ingests 5 harnesses (aiagent_v2, aiagent_v3, claude-code, codex, opencode) into a canonical SQLite store, and the operator's primary use case ("see what my AI agents did") is satisfied. But the CTO has no formal guarantee that what the canonical model claims to capture is actually what's on disk, per adapter. Live state inspection of the prod DB (528,177 sessions, 1,773,520 turns, 6,245,729 ops) reveals gaps the canonical model claims to close but doesn't:

- `aiagent_v2`: 1,343,664 llm ops vs 6,169+6,054 = 12,223 captured llm request+response refs. **99.1% of llm ops have no captured payload.** 1,062,490 tool ops vs 0 tool_request/tool_response refs. **0% of tool payloads captured.** 175,612 `kind='session'` ops, **0 with `child_session_id` populated** → 0% deterministic subagent link.
- `aiagent_v3`: 0 reasoning ops captured (0 of 46,300 llm ops). 78,210 tool ops vs 0 tool refs.
- `claude-code`: 65,278 tool ops, 31,171 tool_response refs, **0 tool_request refs**. 122,679 llm ops, 0 llm_request refs.
- `codex`: 646,034 tool ops vs 670,183 tool_request refs (more requests than ops — likely double-counted or pointing at non-tool ops) and 638,562 tool_response refs. 24,149 `kind='internal'` ops (probably user_input misclassified).
- `opencode`: 275,180 llm ops, 133,119 llm_response refs, **0 llm_request refs**. 461,940 tool ops, 455,103 tool_response refs, **0 tool_request refs**.
- **All 5 adapters**: 0% deterministic `child_session_id` on `kind='session'` ops. Subagent relationships are only discoverable via the soft-link `related` endpoint (SOW-0071) which is heuristic.

These are the *known unknown-unknowns* we found by asking the right questions. The unknown unknowns are whatever we don't know we're not capturing — the operator's 11 invariants are the test cases for finding them. The multi-reviewer investigation is the independent cross-check that catches what one perspective misses.

### Evidence reviewed

- **Live prod DB at `/opt/ai-viewer/data/index.db`** (528,177 sessions, 5,245,729 ops, 5 sources). The per-adapter breakdown above was extracted by hand-running SQL on this DB; the SOW contains the exact queries so they're reproducible.
- **Adapter source code** (all 5 in `internal/adapters/`). Each adapter is its own Go package; the canonical mapping is in `internal/canonical/events.go`. The `internal/adapters/<name>/mapper_*.go` files are the place where harness-specific events are turned into `Op` / `Turn` / `PayloadRef` structs.
- **Mirrored upstream repos at `/opt/baddisk/monitoring/repos/ai/`**:
  - `openai__codex/` — OpenAI codex CLI source. The harness we ingest as `codex` is the JSONL `rollout-*.jsonl` files written by the codex CLI.
  - `anthropics__claude-code/` — Anthropic's Claude Code CLI source. The harness we ingest as `claude-code` is the JSONL files at `~/.claude/projects/`.
  - `anomalyco__opencode/` — SST's opencode TUI source. The harness we ingest as `opencode` is the SQLite DB at `~/.local/share/opencode/opencode.db`.
  - `aiagent_v2` and `aiagent_v3` are the operator's *own* custom harnesses, not public upstream. Their data lives in JSONL files at `~/.ai-agent/sessions/`. No public mirror to cite.
- **Operator's 11 invariants** (this SOW's primary deliverable scope):
  1. Are turns detected? (per session)
  2. Are user prompts captured? (per turn)
  3. Is reasoning content captured? (per llm op)
  4. Is assistant output captured? (per llm op)
  5. Are tools captured? (per turn)
  6. Are tool request payloads captured? (per tool op)
  7. Are tool response payloads captured? (per tool op)
  8. Are LLM request errors captured? (per llm op)
  9. Are tool error responses captured? (per tool op)
  10. Are external subagents linked deterministically? (per `kind='session'` op)
  11. Is the turn viewer presenting all the captured information? (UI smoke)

### Affected contracts and surfaces

- **New**: `internal/invariants/` — a Go package holding the 11 invariant checks. Each invariant is a struct implementing a common interface (`Check(ctx, db) Result`). Run mode: in-process from a CLI subcommand AND from the presenter's `/api/health` and a new `/api/invariants` endpoint.
- **New**: `bin/ai-viewer-ingest check-invariants` subcommand — runs all 11 checks against the live prod DB and prints a structured report. CI runs the same against fixture per-adapter DBs.
- **New**: `bin/ai-viewer-serve` — `/api/invariants` endpoint returning the same structured report.
- **New**: `internal/invariants/fixtures/` — one minimal but representative real-session fixture per adapter (5 fixtures). The CI invariant test loads each fixture into a temp DB and runs the 11 checks; any failure breaks the build.
- **New**: `internal/invariants/checks_test.go` — the 11 invariant implementations, each with at least 1 positive test (fixture passes) and 1 negative test (deliberately-corrupted fixture fails).
- **Modified**: `internal/ingest/ingester.go` — on every successful scan, run the 11 checks; surface failures via `IngestError` (the existing error-surfacing path, SOW-0082). The P0 invariants (e.g. tool_response without payload) are fail-closed by default: a new ingest cycle that would introduce a P0 violation gets quarantined for operator inspection rather than silently polluting the DB.
- **Modified**: `cmd/ai-viewer-ingest/main.go` — register the `check-invariants` subcommand.
- **Modified**: `internal/presenter/presenter.go` — register `/api/invariants` and surface P0/P1 counts in `/api/health`.
- **Modified**: `frontend/src/components/AppTopbar.tsx` (or similar) — small "Drift" indicator when `/api/health` shows non-zero invariant failures. Click → `/api/invariants` JSON or a dedicated `/drift` page.
- **Modified**: `.github/workflows/ci.yml` — add an `invariants` job that runs the 11 checks against per-adapter fixtures on every PR.
- **No DB migration** (the schema is sufficient; the gaps are adapter-side, not schema-side).
- **No frontend feature work** beyond the optional drift indicator (TBD based on operator preference).

### Spec deltas to land before any test or code

1. `.agents/sow/specs/canonical-events.md` — explicit per-adapter matrix: for each of {aiagent_v2, aiagent_v3, claude-code, codex, opencode}, document which op kinds, payload kinds, error classes, and subagent-link fields the adapter *claims* to capture. The SOW is the source of this matrix; the spec records it as the contract the invariants verify.
2. `.agents/sow/specs/observability.md` — add the invariant framework as a new observability surface: `/api/invariants`, the `check-invariants` subcommand, the CI job, and the per-adapter fixture policy.
3. `.agents/sow/specs/adapter-contract.md` — extend with a "completeness contract" section: each adapter MUST populate the canonical fields the operator's 11 invariants require, OR explicitly document the gap as known and intentional. The SOW's gap list is the first cut of this documentation.
4. `.agents/sow/specs/index.md` — TOC update.

### Existing patterns to reuse

- **Test seed helpers** in `internal/presenter/sessions_testseed_test.go` — the existing per-adapter `seedGraph` / `seedTwoSessionsDisjointTools` helpers are the natural starting point for the per-adapter fixtures. The fixtures extend these with a known payload set.
- **Health endpoint** in `internal/presenter/health.go` — the existing `/api/health` envelope (status, version, uptime, db_ok, sources) is the natural place to add a `drift` field. Pattern: append, don't replace.
- **CLI subcommand pattern** in `cmd/ai-viewer-ingest/main.go` — `rollups-backfill`, `fts-content-backfill`, `reprice` are the existing subcommands. `check-invariants` follows the same shape.
- **Error envelope** in `internal/presenter/errors.go` — `writeJSONError` + `errorEnvelope` are the standard REST error shape; `/api/invariants` uses the same envelope on hard failures.
- **Coverage gate** in `internal/store/migrations/` — the per-migration `*_test.go` pattern (migration must apply AND re-apply idempotently AND leave the schema in the contracted state) is the right shape for "each invariant must have a fixture test that pins the contract".

### Risk and blast radius

- **Risk: false positives in CI**. An invariant check that's overly strict (e.g. "every LLM op MUST have a `request` payload_ref") blocks every PR the moment a single fixture is missing one. Mitigation: per-invariant severity tiering (see Implementation plan §b). P0 invariants are fail-closed (data loss); P1 are warn-only in CI, fail-closed against the live prod DB; P2 are documentation-only.
- **Risk: perf impact of invariant checks on the hot ingest path**. The 11 checks are 11 SQL queries; against a 6M-row ops table, even index-driven queries are 10-50ms each → 110-550ms added per scan. Mitigation: run the checks on a *sample* (10% of sessions per cycle) plus the full live-DB check via the `check-invariants` subcommand, not the hot path. The hot path gets a lightweight "no new P0 introduced" check (compare before/after deltas) at <10ms.
- **Risk: spec drift on the "what we claim to capture" matrix**. If the matrix says aiagent_v2 captures tool requests but actually doesn't, the invariant will fail every CI run, and the team will be tempted to either weaken the invariant OR to "fix" the adapter without surfacing the gap. Mitigation: the matrix is the contract; the gap is the actionable item, not a fix. An invariant failure IS the answer, not a bug.
- **Risk: reviewers' findings may be wrong**. The 9 reviewers are LLMs; their findings have the same truth value as my own analysis. CTO must verify every claim (per the `project-second-opinions` skill's "Claim verification" section) before committing fixes.
- **Blast radius**: additive everywhere. The new invariant package is a new directory; no existing file is modified except for the 4 listed in "Affected contracts and surfaces". The fail-closed P0 path is the only behavior change to the hot ingest path, and it quarantines rather than rejects (so existing successful ingests continue to work).

### Sensitive data handling

- The 11 invariants run against the live prod DB. The check results are JSON; they include per-adapter counts and per-session/turn/op IDs but NEVER payload content (the invariants are count + structural, not content). The `/api/invariants` response carries IDs only.
- The fixture DBs under `internal/invariants/fixtures/` are sanitized subsets of real sessions (already the pattern in `internal/presenter/sessions_testseed_test.go`). No raw API keys, no operator PII.
- The new `/api/invariants` endpoint follows the existing v1 auth stance (no auth, localhost-only, per AGENTS.md "Production Scope").

### Implementation plan

**Chunk 0 — Canonical model + 5 follow-up SOWs (the fix pass, per CTO triage of reviewers 1-4)**:

a. `SOW-0097`: extend canonical `OpKind` enum with `OpUserInput` and `OpAssistant`. Migration 0012. Update 5 adapters to emit the new kinds. Pin via `schema_contract_test.go` and the existing test suite. (The canonical contract change that makes invariants #2 and #4 expressible.)
b. `SOW-0098`: add typed `OpStatus` enum (mirroring `SessionStatus`). Update 5 adapters to use the canonical literals. Pin via schema contract test. (The canonical contract change that makes invariant #8 robust.)
c. `SOW-0099`: aiagent_v2 fixes — 100% failed LLM ops lack `error_class` (Reviewer 4 P1); 1,186,802 `system` ops with empty `name` (Reviewer 4 P2). Both are mapper-side fixes.
d. `SOW-0100`: claude-code fixes — capture `tool_request` / `llm_response` / `llm_reasoning` payload_refs (Reviewer 2 P0/P1); emit `status='failed'` on LLM ops when `api_error` is observed (Reviewer 2 P1); 70% subagent-link rate (Reviewer 2 sidecar dependency P1).
e. `SOW-0101`: codex fixes — capture `tool_response` for end-event-finalized tools (Reviewer 1 P0); decode `parent_thread_id` as subagent-link fallback (Reviewer 1 P1); 24,149 `internal` ops reclassified to `user_input` (depends on SOW-0097).
f. `SOW-0102`: opencode fixes — determine if 0 `llm_request` / 0 `tool_request` is source-side or mapper-side (Reviewer 6 pending — need a fact-finding pass first).
g. `SOW-0103`: UX fix — surface the 6 captured-but-unsurfaced fields (Reviewer 3 T11-canonical-1..4): `reasoning_kind`, `bytes_in`/`bytes_out`, `chars_in`/`chars_out`, turn-level `tokens_cache_read`/`tokens_cache_write`/`error_class`, `provider_alias`, `call_path`, `sha256`.

**Chunk 1 — Spec + remaining reviewer investigation (this SOW's first deliverable)**:

a. Update `canonical-events.md`, `observability.md`, `adapter-contract.md`, `index.md` with the per-adapter matrix + invariant framework contracts. (The per-adapter matrix is the authoritative reference for which adapter emits which op kinds + payload_refs; the SOW-0096-review-triage.md baseline table is the v0 of that matrix.)
b. Move this SOW from `pending/` to `current/`. (Already done.)
c. Run the 9 external reviewers (per the §"Multi-reviewer plan" below) in parallel. Each gets the same SOW + a scope-specific brief. Each returns a structured findings report. **Status: 4 of 9 done (codex, claude, canonical, v2). Paused per operator directive 2026-06-22 ("fix the issues first") — reviewers 5-8 will be re-dispatched against the post-fix state after SOW-0097..SOW-0103 land.**
d. Triage findings → produce a "v1 invariant set" document: per-adapter, per-invariant, with severity (P0/P1/P2), the SQL/Go test, and the expected fix-or-document decision. **Status: triage v1 written (see SOW-0096-review-triage.md); the corrected baseline replaces the original SOW baseline.**

**Chunk 2 — Invariant framework + per-adapter fixtures**:

a. Create `internal/invariants/` with: 11 check implementations, a `Run(ctx, db) []Result` runner, a structured `Result` type (check name, severity, count, sample IDs, message).
b. Create `internal/invariants/fixtures/` with 5 minimal-but-representative real-session fixtures (one per adapter).
c. Create `internal/invariants/checks_test.go` with 11 positive + 11 negative tests (one of each per invariant) plus 5 fixture tests (the fixture must pass all 11 checks).
d. Wire the framework into:
  - `bin/ai-viewer-ingest check-invariants` (CLI subcommand, against live prod DB)
  - `bin/ai-viewer-serve` `/api/invariants` (REST, returns the same JSON)
  - `bin/ai-viewer-serve` `/api/health` (adds a `drift` field: counts of P0/P1/P2 violations)
  - `internal/ingest/ingester.go` (lightweight before/after delta check, fail-closed on new P0)
  - `.github/workflows/ci.yml` (new `invariants` job, fixture-based)
e. Update `AGENTS.md` Hard-Won Lessons section with the invariant framework + per-adapter gotchas the reviewers uncover.

**Chunk 3 — Operator-facing drift surface (TBD based on operator preference)**:

a. Decide between (i) small "Drift" indicator in the topbar + a dedicated `/drift` page, (ii) `/api/invariants` JSON only, (iii) email/webhook notification. The SOW proposes (i) as the default; operator picks.
b. Implement the chosen surface.
c. Acceptance: an operator opening the app sees a "3 P0 violations in codex adapter" indicator the moment the live data drifts.

**Chunk 4 — Per-adapter gap remediation (driven by chunk 1 triage)**:

a. For each P0 gap the reviewers find, write a follow-up SOW in `.agents/sow/pending/` describing the fix (e.g. "SOW-0097: ingest tool request payloads in aiagent_v2 adapter").
b. For each P1 gap, fix in-place with the per-adapter adapter maintainer's standard process.
c. For each P2 gap, document as known and intentional in the per-adapter matrix.

### Multi-reviewer plan (chunk 1 step c)

The 9 reviewers run in sequence (one CLI call each, 30-minute timeout per the `~/.AGENTS.md` protocol) with self-contained prompts. Each prompt carries:

- This SOW file as the contract
- The CTO's 11 invariants (verbatim)
- The CTO's known-gaps baseline (the per-adapter numbers above) so the reviewer can verify or reject
- A scope-specific brief naming the exact adapter the reviewer is responsible for
- File paths (relative, per the `~/.AGENTS.md` protocol) to the mirrored harness source AND the corresponding `internal/adapters/<name>/` package
- A required output format (structured findings report: per-finding `ID | Severity (P0/P1/P2) | Description | Evidence (file:line) | Proposed invariant (SQL/Go sketch) | Suggested fix or "accept as known"`)
- The mandatory `~/.AGENTS.md` no-changes protocol (read-only, no permission requests, no recursion)

The 9 reviewers and their scopes:

| # | CLI | Model | Scope |
|---|---|---|---|
| 1 | `codex2` | OpenAI gpt-5.5 | **codex adapter** + mirrored `openai__codex/` source. Specifically: why 670,183 tool_request refs vs 646,034 tool ops? What does codex actually emit that we're not capturing? |
| 2 | `claude` | claude-opus-4.8 | **claude-code adapter** + mirrored `anthropics__claude-code/` source. Why 0 tool_request refs but 31,171 tool_response refs? What does Claude Code's JSONL actually contain? |
| 3 | `gemini` | gemini-3.1-pro | **Canonical model coverage**. Does the canonical `Op` / `Turn` / `PayloadRef` shape cover the union of what all 5 harnesses emit? What are we missing structurally? Also: should we ingest un-merged harnesses (gemini-cli, aider, goose, plandex, CodebuffAI/codebuff, bytedance/trae-agent, etc.)? |
| 4 | `opencode run` | glm-5.2-max | **aiagent_v2 adapter** (the operator's own custom harness). Why 0% payload capture for llm + tool? Is the harness's JSONL shape different from what the mapper expects, or is the mapper just not reading the right fields? |
| 5 | `opencode run` | minimax-m3-coder | **aiagent_v3 adapter**. Why 0 reasoning ops? Why 0 tool refs? What's different from aiagent_v2 structurally? |
| 6 | `opencode run` | mimo-v2.5-pro | **opencode adapter** + mirrored `anomalyco__opencode/` source. Why 0 llm_request refs but 133,119 llm_response refs? Does the opencode SQLite schema have a "request" side we're not reading? |
| 7 | `opencode run` | kimi-k2.7-code | **Invariant framework design**. Given the 11 invariants + the canonical shape, what is the right Go interface for the checks? How should the live-DB check be structured? What should the CI vs live split be? |
| 8 | `opencode run` | deepseek-v4-pro | **Cross-adapter consistency + severity tiering**. The 11 invariants as Go+SQL. Per-invariant: is this fail-closed in CI? in live? what's the right threshold for "warning" vs "fail"? |
| 9 | `opencode run` | qwen3.7-plus | **Operator UX**. Does the turn viewer (TurnView + UnifiedView) present all the captured information? Are there fields we capture that no UI surface uses? Are there fields the operator would want to see that we don't capture? Walk the UI against the canonical model. |

The SOW ships with a `prompts/` directory containing the 9 self-contained reviewer prompts; the chunk-1 step is "run all 9 + triage + produce v1 invariant set".

### Validation plan (named test files + behaviors)

- `internal/invariants/checks.go` — the 11 check implementations.
- `internal/invariants/checks_test.go` — 11 positive + 11 negative tests (one of each per invariant). Pattern: load a minimal fixture, run the check, assert pass; deliberately corrupt the fixture, run the check, assert fail.
- `internal/invariants/fixtures/<adapter>/seed.go` — one fixture per adapter, with helper functions for the test suite.
- `internal/invariants/fixtures/<adapter>/seed_test.go` — 5 fixture tests (one per adapter fixture, asserting "the fixture itself passes all 11 checks").
- `cmd/ai-viewer-ingest/check_invariants.go` + `check_invariants_test.go` — the CLI subcommand; its test runs against an in-memory DB and asserts the JSON output shape.
- `internal/presenter/health.go` + `health_test.go` — `/api/health` includes the `drift` field; the test asserts the field is present and the count is right.
- `internal/presenter/invariants.go` + `invariants_test.go` — the `/api/invariants` endpoint.
- End-to-end: `bin/ai-viewer-ingest check-invariants` against `/opt/ai-viewer/data/index.db` must return a structured report; each per-adapter section must show a non-zero count of findings (because the live data has the gaps above). The output is the durable evidence that the framework works.

### Artifact impact plan

**New files**:
- `internal/invariants/checks.go`
- `internal/invariants/checks_test.go`
- `internal/invariants/runner.go`
- `internal/invariants/runner_test.go`
- `internal/invariants/result.go`
- `internal/invariants/fixtures/aiagent_v2/seed.go`
- `internal/invariants/fixtures/aiagent_v3/seed.go`
- `internal/invariants/fixtures/claude_code/seed.go`
- `internal/invariants/fixtures/codex/seed.go`
- `internal/invariants/fixtures/opencode/seed.go`
- `cmd/ai-viewer-ingest/check_invariants.go`
- `cmd/ai-viewer-ingest/check_invariants_test.go`
- `internal/presenter/invariants.go`
- `internal/presenter/invariants_test.go`
- `.agents/sow/done/SOW-0096-screenshots/` (drift indicator, if chunk 3 lands)
- `prompts/reviewer-codex.md`, `prompts/reviewer-claude.md`, `prompts/reviewer-gemini.md`, `prompts/reviewer-glm.md`, `prompts/reviewer-minimax.md`, `prompts/reviewer-mimo.md`, `prompts/reviewer-kimi.md`, `prompts/reviewer-deepseek.md`, `prompts/reviewer-qwen.md` — the 9 self-contained reviewer prompts (committed for traceability per the SOW sign-off pattern).

**Modified files** (additive only):
- `.agents/sow/specs/canonical-events.md` — per-adapter matrix
- `.agents/sow/specs/observability.md` — invariant framework section
- `.agents/sow/specs/adapter-contract.md` — completeness contract
- `.agents/sow/specs/index.md` — TOC
- `cmd/ai-viewer-ingest/main.go` — register `check-invariants` subcommand
- `internal/presenter/presenter.go` — register `/api/invariants`, surface drift in `/api/health`
- `internal/ingest/ingester.go` — before/after delta check on the hot path
- `.github/workflows/ci.yml` — new `invariants` job
- `AGENTS.md` — Hard-Won Lessons append (per-adapter gotchas the reviewers uncover)
- `frontend/src/components/AppTopbar.tsx` (chunk 3, optional) — drift indicator

**Schema impact** (corrected post-review): **SQL schema: none** (v11 is sufficient; the gaps are adapter-side). **Canonical contract: invariants #2 ("user prompts captured") and #4 ("assistant output captured") require an explicit decision on op kinds** before the invariant SQL can be written — the canonical `OpKind` enum (`internal/canonical/events.go:77-92`) defines `llm`, `tool`, `session`, `reasoning`, `internal`, `system`, `compaction` but NOT `user_input` or `assistant`, and no adapter emits them. The DB has 0 ops of either kind. Two options: (a) extend the enum to add `OpUserInput` and `OpAssistant` and update the adapters to emit them, or (b) re-frame the invariants to use payload_ref presence (every LLM op should have a request payload_ref whose decoded `messages[N].role == 'user'`). This decision is Reviewer 7's call (framework design). See `SOW-0096-review-triage.md` for the corrected baseline + verification.

Additionally: a typed `OpStatus` enum (mirroring the existing `SessionStatus` enum at `internal/canonical/events.go:49-69`) is missing — ops and turns carry bare `string` status, and the live data already shows the canonical literals `completed` / `failed` / `running` in use. The 11 invariants assume the canonical literals are uniformly emitted; an enum would let the SOW's CI tests pin the contract.

### Open decisions

1. **Drift indicator UX** (chunk 3) — topbar pill vs. dedicated page vs. JSON-only. Default proposal: topbar pill linking to `/api/invariants` (a JSON page) for v1, with a dedicated `/drift` page as a follow-up if the operator wants richer visualization.
2. **P0 fail-closed behavior on the hot path** (chunk 2 step d) — quarantine (insert into a `_quarantine` table for operator review) vs. skip-the-offending-session (continue the rest of the scan) vs. abort-the-whole-scan. Default proposal: quarantine, on the principle that "no silent failures" (Hard Rule #6) is more important than "no data left behind" (the data is still in the harness's source-of-truth JSONL; quarantine is reversible).
3. **Per-adapter fixture policy** (chunk 2 step b) — minimal (one session per adapter, enough to exercise all 11 checks) vs. comprehensive (a week's worth of real sessions, anonymized). Default proposal: minimal, with the live-DB check via the CLI subcommand as the "comprehensive" path. CI runs fast; the operator runs comprehensive on demand.

### Out of scope (deferred to v2)

- **Auto-remediation**: when a P0 violation is detected, automatically re-ingest the offending session from the source-of-truth JSONL. Deferred; the quarantine + operator review path is enough for v1.
- **Per-harness content parity checks** (e.g. "this codex session's last user prompt should appear verbatim in the fts_content index"). Deferred; SOW-0091's fts_content backfill is the related work, and these checks would be a v2 expansion.
- **Webhooks / notifications** when drift is detected. Deferred; the operator can run `check-invariants` on a cron and forward the output to anything.
- **Cross-session consistency** (e.g. "every session referenced by a `kind='session'` op must exist"). Deferred; the existing SOW-0071 related endpoint covers soft links; the hard-link check is a v2 addition.
- **Ingesting un-merged harnesses** (gemini-cli, aider, goose, plandex, codebuff, trae-agent, etc.). Deferred; the gemini reviewer's "should we" question is the input to that decision.
