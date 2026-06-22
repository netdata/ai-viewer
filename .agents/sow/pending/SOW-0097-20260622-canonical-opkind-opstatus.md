# SOW-0097 - Canonical OpKind + OpStatus extension (SOW-0096 chunk 0a + 0b)

## Status

Status: open (proposed 2026-06-22)
Sub-state: CTO-proposed; awaiting operator sign-off. This is a follow-up to SOW-0096 (the ingestion accuracy audit). Reviewers 1-4 of SOW-0096 caught the canonical-contract defects that this SOW fixes.

## Pre-Implementation Gate

### Problem / root-cause model

The SOW-0096 reviewers 3 (canonical via glm) and 4 (v2 via minimax) independently caught the same canonical-contract defect: the `OpKind` enum in `internal/canonical/events.go:77-92` defines seven op kinds (`llm`, `tool`, `session`, `reasoning`, `internal`, `system`, `compaction`) but NOT `user_input` or `assistant`. The live DB confirms: 0 ops of either kind, across all 5 adapters.

This is the structural reason SOW-0096 invariants #2 ("user prompts captured") and #4 ("assistant output captured") cannot be expressed as SQL queries against the current schema. The CTO's original SOW-0096 baseline table listed `count(ops WHERE kind='user_input')` as if it were a runnable query — it returns 0 for *every* adapter by construction, so the "0% user prompt capture" was meaningless noise. Reviewer 1 (codex) also caught that the 24,149 codex `internal` ops are **intentional** user inputs, not a misclassification — codex has no first-class `user_input` kind to use, so it overloads `internal` (with `name='user_input'`). The fix is not to reclassify them; it's to give the canonical model the missing kinds.

Reviewer 3 (T8-canonical-1) also caught a related gap: there is no typed `OpStatus` enum. Ops and turns carry bare `string` status. The live data already shows the canonical literals `completed` / `failed` / `running` in use, but the contract is unenforced — adapters could emit any string and invariant #8 (`status = 'failed'`) would silently miss it. The existing `SessionStatus` enum (`internal/canonical/events.go:49-69`) is the pattern to mirror.

This SOW is the foundation: every per-adapter SOW (0099-0102) depends on it.

### Evidence reviewed

- **Live prod DB** (`/opt/ai-viewer/data/index.db`): 0 ops of `kind='user_input'`, 0 ops of `kind='assistant'`. Confirmed across all 5 adapters. Reviewed again after Reviewers 3 + 4 flagged the gap.
- **Canonical `OpKind` enum** at `internal/canonical/events.go:77-92`. Seven constants, no `user_input` / `assistant`. Adjacent enum `SessionStatus` at `internal/canonical/events.go:49-69` (already typed, the pattern to mirror).
- **Adapter usages** of `kind`:
  - `internal/adapters/codex/ops_response.go:119`, `:160`, `:171` — codex emits `kind='internal', name='user_input'` for user prompts.
  - `internal/adapters/aiagent_v2/` — no user-prompt or assistant-specific kind; user prompts are stored in `turns.extras_json` (always NULL) and assistant output is in `ops.payload_refs` (99% missing per SOW-0096 triage).
  - `internal/adapters/opencode/mapper_emitters.go:24-28` — assistant text → `llm_response` PayloadRef, not an op at all.
  - `internal/adapters/claude_code/ops.go:34-58` — assistant output flows through `mapAssistant` but no `OpAssistant` is emitted; the LLM op's tokens are recorded but the text body is dropped (Reviewer 2 A4-claude-1).
- **`OpStatus` usage**: 0 typed constants. Adapter emissions include `completed` (13 sites), `failed` (2), `cancelled` (1), and the aiagent source format's `ok` literal (31 sites, but appears to be normalized — verified live: 0 `status='ok'` rows in the v2 data).
- **Schema** (`internal/store/migrations/`): the `ops.kind` column is `TEXT` (no CHECK constraint). Adding new enum values is additive (no migration needed for the *enum* itself). The `op_status_index` does not exist; the v11 indexes cover kind but not status.
- **SOW-0096 review record** at `SOW-0096-review-triage.md` — the verified bugs that this SOW fixes (#1 canonical enum missing, #14 no typed OpStatus enum).
- **SOW-0096 itself**: the corrected schema-impact line in the main SOW now reads "Canonical contract: invariants #2 and #4 require an explicit decision on op kinds before the invariant SQL can be written."

### Affected contracts and surfaces

- **New**: `OpUserInput` and `OpAssistant` constants in `OpKind` enum (`internal/canonical/events.go:77-92`).
- **New**: `OpStatus` typed string enum with `StatusOpCompleted`, `StatusOpFailed`, `StatusOpCancelled`, `StatusOpRunning` constants in `internal/canonical/events.go` (adjacent to the existing `SessionStatus` enum).
- **New**: `TurnStatus` typed string enum with the same four constants, mirroring `OpStatus` (turns have status too — SOW-0096 invariant #8 is LLM-only but turns can also be `failed`).
- **New**: `internal/store/migrations/0012_canonical_opkinds.sql` — adds the two `OpKind` constants via a comment (SQLite stores kind as TEXT, no enum type) + adds a CHECK constraint on `ops.kind` and `ops.status` enumerating the legal values. The constraint catches adapter-side non-canonical emissions at insert time.
- **New**: `internal/store/migration_0012_canonical_opkinds_test.go` — applies the migration, verifies the CHECK constraint rejects illegal values.
- **New**: `OpUserInput` and `OpAssistant` payload-kind constants in `PayloadKind` enum (if not already present) at `internal/canonical/events.go:345-347` — the new op kinds may need payload kinds to carry their bodies (or the existing `text` kind may suffice; this is an open decision).
- **Modified**: `internal/canonical/events.go` — add the new constants + the new `OpStatus` / `TurnStatus` enums.
- **Modified**: `internal/store/schema_contract_test.go` — update the `ops.kind` and `ops.status` legal-values list.
- **Modified**: `internal/adapters/codex/ops_response.go` — change `kind='internal', name='user_input'` to `kind='user_input'`. Update the 3 emission sites at `:119`, `:160`, `:171`.
- **Modified**: `internal/adapters/codex/ops_response.go` (assistant text path, if any) — add `kind='assistant'` emission. (May be deferred to SOW-0101 if codex assistant handling is larger.)
- **Modified**: `internal/adapters/aiagent_v2/` — emit `kind='user_input'` for user prompts (mapper changes; producer-side gap may also need a follow-up SOW if v2 doesn't have the prompt text in the source).
- **Modified**: `internal/adapters/aiagent_v3/` — same as v2.
- **Modified**: `internal/adapters/claude_code/` — emit `kind='user_input'` for user prompts + `kind='assistant'` for assistant text. The mapper currently drops both bodies (per Reviewer 2); this SOW only fixes the kind emission, the body capture is a SOW-0100 chunk.
- **Modified**: `internal/adapters/opencode/` — emit `kind='user_input'` + `kind='assistant'`. Per Reviewer 6 (pending), the opencode request-side gap may be source-side; only the kinds-without-body case is in this SOW.
- **Modified**: `internal/presenter/presenter.go` (SchemaVersion = 11 → 12) — bump for the new migration.
- **No frontend changes** in this SOW. Surfacing `OpUserInput` / `OpAssistant` in the UI is part of SOW-0103 (captured-but-unsurfaced fields + new op kinds).
- **No new tests** beyond the migration test. The per-adapter adapter changes add their own tests as part of the per-adapter SOWs (0099-0102).

### Spec deltas to land before any test or code

1. `.agents/sow/specs/canonical-events.md` — document the new `OpUserInput` and `OpAssistant` constants in the `OpKind` enum. Document the new `OpStatus` / `TurnStatus` enums.
2. `.agents/sow/specs/canonical-events.md` — update the per-adapter matrix: which adapter emits `user_input` and `assistant` after this SOW lands. The matrix is the contract the SOW-0096 invariants will verify.
3. `.agents/sow/specs/data-model.md` — document the new CHECK constraints on `ops.kind` and `ops.status` from migration 0012.
4. `.agents/sow/specs/index.md` — TOC update.

### Existing patterns to reuse

- **`SessionStatus` enum** at `internal/canonical/events.go:49-69` is the exact pattern to mirror for `OpStatus` / `TurnStatus`. Same `type XxxStatus string` declaration, same `const (...)` block, same `String()` method.
- **Migration pattern** at `internal/store/migrations/0011_topology_sort_indexes.sql` — the existing `CREATE INDEX` + `ALTER TABLE` pattern. The new migration 0012 follows the same shape.
- **Schema contract test** at `internal/store/schema_contract_test.go` — the existing test pins the `ops.kind` legal values. The new migration's CHECK constraint + the test update pin the new legal values.
- **Existing adapter test fixtures** at `internal/adapters/codex/coverage_branch_test.go` and equivalents — when the codex adapter changes from `kind='internal'` to `kind='user_input'`, the existing tests that assert `kind='internal'` need to be updated. The test files are the natural place to verify the new contract.

### Risk and blast radius

- **Risk: existing queries that filter on `kind='internal'` break.** Migration 0012 will RE-classify the 24,149 codex `internal/user_input` rows to `user_input` (or leave them as `internal` and stop emitting new ones in that category — that's a per-adapter SOW decision; default is to migrate the data so the canonical model is honest). The CTO's preferred approach: change the data so the schema and the data match.
  - **Mitigation**: the migration runs in a transaction; if the reclassification is slow on the 24,149 rows, batch it. For 24k rows, the migration is sub-second.
- **Risk: existing SQL queries in the presenter / store that hardcode `kind='internal'`** break. Example: `internal/presenter/stats_rollup_defs.go` or similar may group ops by `kind` and the new op kinds would be a new group.
  - **Mitigation**: every rollup / stats query that does `GROUP BY kind` is reviewed as part of this SOW and the test suite catches the breakage. The `withSeed` integration tests will reveal any missed sites.
- **Risk: invariants #2 and #4 are now expressible but the SQL needs to be re-written.** SOW-0096 reviewers 7 and 8 (framework and SQL) are pending. This SOW makes invariants #2 and #4 expressible; the next pass writes the actual SQL.
  - **Mitigation**: this SOW is explicitly the foundation; the SQL work follows.
- **Blast radius**: 1 new migration, 1 new test, 1 enum extension in the canonical model, 1 typed enum addition, 4-5 adapter files changed (one per adapter for `user_input`, plus codex's 3 emission sites). All additive. No existing behavior changes except the data reclassification (24,149 rows moved from `internal` to `user_input`).

### Sensitive data handling

- The migration reclassifies 24,149 ops in place. No data leaves the DB; the op_id and session_id are preserved; only the `kind` column changes. No sensitive data is touched.
- The CHECK constraint on `status` is structural; no PII is exposed.
- The new enum constants are code-only; no new fields, no new payload_refs.

### Implementation plan

**Chunk 1 — Canonical model + DB constraint** (the foundation):

a. Add `OpUserInput OpKind = "user_input"` and `OpAssistant OpKind = "assistant"` to the `OpKind` const block.
b. Add `OpStatus` / `TurnStatus` typed string enums + 4 constants each, mirroring `SessionStatus`.
c. Add the new `PayloadKind` constants if needed (deferred decision; default: `text` works for both).
d. Write migration 0012: `ALTER TABLE ops ADD CONSTRAINT chk_ops_kind CHECK (kind IN ('llm', 'tool', 'session', 'reasoning', 'internal', 'system', 'compaction', 'user_input', 'assistant'));` Same for `status`. Plus an UPDATE that reclassifies the 24,149 codex `internal/user_input` rows to `user_input`.
e. Update `schema_contract_test.go` to include the new legal values + the CHECK constraints.
f. Bump `SchemaVersion` to 12.

**Chunk 2 — Per-adapter kind emission** (the minimum to make invariants #2 and #4 work):

a. codex: change 3 emission sites to `kind='user_input'`. (SOW-0101 chunk will do the deeper claude-style body capture.)
b. aiagent_v2: emit `kind='user_input'` where the producer's user prompt is present. If the producer doesn't carry the prompt text (per Reviewer 4 T6-v2-1), emit the op without the body — a `user_input` op with no payload_ref is still a better signal than the current "0 user_input ops" (which the baseline narrative mis-read as "0% user prompt capture").
c. aiagent_v3: same as v2.
d. claude-code: emit `kind='user_input'` + `kind='assistant'`. The mapper currently drops both bodies; the kind emission is a partial fix that SOW-0100 will complete.
e. opencode: same as claude-code. Reviewer 6 (pending) determines if the request body is in the source.

**Chunk 3 — Documentation + verification**:

a. Update `canonical-events.md` and `data-model.md` with the new enum values + the per-adapter matrix (which adapter emits `user_input` / `assistant` after this SOW).
b. Re-run the live-DB SQL: `count(ops WHERE kind='user_input')` should now be > 0 (expected: 24,149 from the migration, plus whatever the adapter updates emit). `count(ops WHERE kind='assistant')` is TBD.
c. Update `SOW-0096-review-triage.md` to mark this gap (T2-canonical-1, T4-canonical-1, SOW-canonical-1) as resolved.

**Chunk 4 — Sanity tests**:

a. `internal/store/migration_0012_canonical_opkinds_test.go` — applies the migration, asserts the CHECK constraint rejects `kind='foo'`, asserts the reclassification moved 24,149 rows.
b. The existing per-adapter `withSeed` integration tests should still pass (after the kind emissions are updated to match the new contract).
c. New test: `internal/canonical/opkind_test.go` — exhaustive table of all `OpKind` constants → expected string, to catch future enum-value drift.

### Multi-reviewer plan (post-fix)

Per operator directive 2026-06-22, the remaining SOW-0096 reviewers (5 v3, 6 opencode, 7 framework, 8 SQL) are paused until SOW-0097 lands. After SOW-0097 ships:
- Reviewer 5 (v3): the v3 mapper is examined in light of the new `user_input` kind. Is the v3 producer carrying the prompt text in the source? If yes, the mapper can emit both the op and the body.
- Reviewer 6 (opencode): the opencode SQLite schema is examined. Are request bodies in the source?
- Reviewer 7 (framework): the framework design is updated to use the new op kinds in invariants #2 and #4.
- Reviewer 8 (SQL): the SQL for invariants #2 and #4 is written.

### Validation plan (named test files + behaviors)

- `internal/store/migration_0012_canonical_opkinds_test.go` — the migration test. Verifies the schema after migration, the CHECK constraints, the data reclassification.
- `internal/canonical/opkind_test.go` — exhaustive table-driven test that all `OpKind` constants round-trip through `String()` and that the `OpStatus` / `TurnStatus` enums are well-formed.
- `internal/store/schema_contract_test.go` (modified) — the legal-values list now includes `user_input`, `assistant`, and the four `OpStatus` / `TurnStatus` literals.
- The 5 adapter test suites (codex, claude_code, aiagent_v2, aiagent_v3, opencode) — existing `withSeed` tests still pass after the kind emission updates. (No new tests in this SOW; the per-adapter SOWs add their own.)
- End-to-end: re-run the live-DB SQL after deploy. The numbers should match the per-adapter matrix in the spec.

### Artifact impact plan

**New files**:
- `internal/store/migrations/0012_canonical_opkinds.sql`
- `internal/store/migration_0012_canonical_opkinds_test.go`
- `internal/canonical/opkind_test.go`

**Modified files** (additive only):
- `internal/canonical/events.go` — add `OpUserInput`, `OpAssistant`, `OpStatus` enum, `TurnStatus` enum.
- `internal/store/schema_contract_test.go` — update legal values.
- `internal/presenter/presenter.go` — `SchemaVersion = 11 → 12`.
- `internal/adapters/codex/ops_response.go` — change 3 emission sites.
- `internal/adapters/aiagent_v2/mapper_*.go` — emit `user_input` where the producer carries the prompt.
- `internal/adapters/aiagent_v3/mapper_*.go` — same.
- `internal/adapters/claude_code/ops_*.go` — emit `user_input` + `assistant`.
- `internal/adapters/opencode/mapper_*.go` — emit `user_input` + `assistant`.
- `.agents/sow/specs/canonical-events.md` — new enum + per-adapter matrix.
- `.agents/sow/specs/data-model.md` — CHECK constraints.
- `.agents/sow/specs/index.md` — TOC.
- `AGENTS.md` — Hard-Won Lessons append (canonical enum extension pattern).

**Schema impact**: ADDITIVE. Migration 0012. CHECK constraints catch future adapter-side non-canonical emissions.

### Open decisions

1. **`PayloadKind` for `user_input` / `assistant` ops** — does the operator want a new `PayloadKind` constant (e.g. `text` works, but is it the right name?) or reuse an existing one? Default: `text` (existing). Override in operator feedback.
2. **Adapter order** — chunk 2 updates 5 adapters. Should all 5 land in one PR, or one PR per adapter? Default: one PR for the canonical model + migration (chunks 1, 3, 4) + one PR per adapter for the kind emission (5 PRs). The per-adapter PRs are the natural unit for per-adapter SOWs (0099-0102).
3. **Reclassification of existing 24,149 rows** — done as part of migration 0012 (in the same PR as the enum extension). Alternative: leave the rows as `internal` and stop emitting new ones, with a follow-up migration. Default: reclassify in 0012 (cleaner; the schema and the data match from day 1).
4. **Whether to make `OpStatus` per-op, per-turn, or both** — the SOW proposes both. Alternative: only `OpStatus` and have turns inherit. Default: both, because turns can also be `failed` (turn-level cache miss, turn-level tool error, etc.) and the existing `TurnFinalizedEvent.Status` is already a field.

### Out of scope (deferred)

- **Capturing the actual user prompt / assistant output text** — this is per-adapter work. codex has the data and needs the mapper; aiagent_v2/v3 need a producer-side fix (which is a separate SOW); claude-code and opencode have the data in the source and need the mapper. All deferred to SOW-0099..0102.
- **Surfacing `user_input` / `assistant` in the UI** — SOW-0103.
- **The SOW-0096 invariant framework itself** — still pending Reviewer 7 (framework design) and Reviewer 8 (SQL). This SOW makes invariants #2 and #4 expressible; the next pass writes the actual checks.
- **Per-adapter payload_ref fixes** for the claude-code and opencode request-side gaps — SOW-0100 and SOW-0101.
- **Producer-side fix for aiagent_v2/v3 user prompt capture** — needs the operator's own harness-side work; the CTO can advise but not implement.
