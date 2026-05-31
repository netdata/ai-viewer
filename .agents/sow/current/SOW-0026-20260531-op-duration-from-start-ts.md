# SOW-0026 - op duration_us computed from finalize-event Ts instead of start_ts

## Status

Status: in-progress

Sub-state: gate ready; on branch `sow-0026-op-duration` (off master). Spec deltas landed; delegating test/fix/migration. Discovered 2026-05-31 during SOW-0006 (APM tracing UI) Trace-tab visual review: every op's stored `ops.duration_us` is `0` while `end_ts - start_ts` is non-zero. Root-caused to the ingest writer. This is a prerequisite for SOW-0006 (the Trace event-list Duration column and the Topology `metric=duration` node sizing both read this column) and for SOW-0007 analytics (the `catalog_*.total_duration_us` rollups accumulate it).

## Requirements

### Purpose

Make `ops.duration_us` correct: it must equal `end_ts - start_ts` (the authoritative op start and end), exactly as `data-model.md` already specifies. Today the ingest writer computes it from the wrong operand, so every spec-conformant adapter persists `duration_us = 0`. This corrupts the Trace UI duration column, the Topology duration metric, and the analytics duration rollups. Fix the writer, backfill existing rows and rollups, and make the test fixtures spec-conformant so the bug cannot reappear unseen.

### User Request

Implied by `data-model.md:140` (`duration_us … end_ts - start_ts when both known`) and surfaced by the SOW-0006 Trace-tab visual review (operator saw the waterfall durations correct but the event-list Duration column reading `0µs`). Operator was shown the evidence and the fix plan; proceeding under the standing delegated-CTO mandate (bug blocking an already-approved SOW).

### Assistant Understanding

Facts:

- `internal/ingest/writer.go` `applyOpFinalized` computes `durUS = ev.EndTs - ev.Ts` (the line guarded by `if ev.EndTs > 0 && ev.Ts > 0 && ev.EndTs >= ev.Ts`).
- `ev.Ts` is `OpFinalizedEvent.EventBase.Ts` — the canonical event timestamp. `internal/canonical/events.go` `EventBase.Ts` doc: "event timestamp in UNIX-microseconds (UTC). The ingester orders events by Ts within a session." A finalize event therefore carries a `Ts` ≈ the op END (it sorts AFTER its OpStarted). `OpFinalizedEvent.EndTs` is the op end too. So `EndTs - Ts ≈ 0` for any spec-conformant adapter.
- The op START is carried by `OpStartedEvent.Ts` and persisted into `ops.start_ts` (writer op-insert, `start_ts = ev.Ts` on the OpStarted path).
- Live evidence (seeded aiagent_v3 fixture DB): 6 ops, all `duration_us = 0`, while `end_ts - start_ts` ∈ {3 000 000, 300 000, 3 000 000, 23 000 000, 3 000 000, 1 900 000} µs.
- `data-model.md:140` already documents the intended semantics: `duration_us INTEGER, -- end_ts - start_ts when both known`. The code violates its own spec.
- The bug was masked because the writer/catalog tests construct fixtures with `OpFinalizedEvent.Ts == OpStartedEvent.Ts` (e.g. `internal/ingest/catalog_idempotency_test.go:81` OpStarted `Ts:1100`, `:86` OpFinalized `Ts:1100, EndTs:1300`). With `Ts` mis-set to the START, `EndTs - Ts` coincidentally equals the real duration, so the tests pass. Real adapters set `Finalized.Ts = end`, exposing the bug.
- Consumers of `ops.duration_us`: `internal/presenter/session_detail_ops.go:128` (Trace event-list Duration), `internal/presenter/session_topology.go:180,204` (Topology `metric=duration` node sizing), and the catalog rollups `catalog_models.total_duration_us` / `catalog_tools.total_duration_us` accumulated in `internal/ingest/catalog.go` (read at `:251/:255`, delta at `:273`, applied `:315/:336`).
- A surface that does NOT use the column and is therefore correct: `internal/presenter/stats.go:135` ("duration_us sums (end_ts - start_ts)") derives duration directly from start/end. The frontend Trace waterfall (`frontend/src/viz/trace.ts`) also derives from start/end. This is why two surfaces silently disagree.

Inferences:

- The fix is to compute duration from the op's persisted `start_ts` (the authoritative start), not from the finalize event's `Ts`. The writer already reads `start_ts` inside the pricing branch of `applyOpFinalized`; that read generalizes.
- Within a session the resolver orders events by `Ts`, and `OpStarted.Ts (start) < OpFinalized.Ts (end)`, so the OpStarted UPSERT is applied before the OpFinalized UPDATE — `start_ts` is present when duration is computed. For a genuinely orphaned finalize (no prior/recorded start), `start_ts` is absent and duration must stay NULL rather than be guessed.
- Existing persisted DBs (and the catalog rollups) carry the wrong values; a backfill migration is required so the fix is not "new rows only".
- Catalog `total_duration_us` is a pure additive SUM of member ops' `duration_us` grouped by the op's final catalog identity (provider+model for models, namespace+name for tools). Unlike `call_count` (which has identity-migration subtlety), a duration rollup can be recomputed deterministically as `SUM(duration_us)` over the matching ops, so the migration can recompute it directly from the corrected `ops` rows.

Unknowns:

- Exact catalog grouping keys and op-kind/status filters used when booking `total_duration_us` — must be read from `catalog.go` so the migration's recompute SUM matches the live accumulation 1:1 (resolved in the gate's implementation step by the subagent reading `catalog.go`; the recompute must reproduce the same membership the incremental path books).

### Acceptance Criteria

1. `ops.duration_us = end_ts - start_ts` for every op whose start and end are both known, for all adapters, regardless of `OpFinalizedEvent.Ts`. **Verification**: a new ingester test emits `OpStarted{Ts:S}` + `OpFinalized{Ts:E, EndTs:E}` with `S < E` and asserts `ops.duration_us == E - S`; the test FAILS on current code (would read 0) and passes after the fix.
2. An orphan `OpFinalized` with no recorded start leaves `duration_us` NULL (not 0-as-real, not a guess). **Verification**: an ingester test asserts NULL duration for a finalize with no matching start.
3. Existing persisted data is corrected: migration `0005` backfills `ops.duration_us` and recomputes `catalog_models.total_duration_us` / `catalog_tools.total_duration_us` from the corrected ops. **Verification**: a migration test seeds a pre-0005 DB with `duration_us=0` rows + stale catalog totals, runs migrations, and asserts both the per-op column and the rollups are corrected.
4. Test fixtures are spec-conformant: writer/catalog tests set `OpFinalizedEvent.Ts = EndTs` (the finalize/end time), and assertions still hold because duration now derives from `start_ts`. **Verification**: `go test ./internal/ingest/...` green with the conformant fixtures; the masking pattern (`Finalized.Ts == OpStarted.Ts`) is gone from duration-relevant fixtures.
5. Specs reconciled in the same commit: `data-model.md` clarifies the source of `duration_us` (persisted `start_ts`, not the finalize event Ts) and the rollup recompute; `ingester.md` documents the OpFinalized duration computation. **Verification**: `scripts/spec-drift.sh` clean.
6. All quality gates green; external review converged. **Verification**: `scripts/gates.sh` + `## Reviews`.

## Analysis

Sources checked:

- `internal/ingest/writer.go` (applyOpFinalized duration computation + op-insert start_ts).
- `internal/canonical/events.go` (EventBase.Ts, OpStartedEvent, OpFinalizedEvent semantics).
- `internal/ingest/catalog.go`, `internal/ingest/catalog_migrate.go` (total_duration_us accumulation).
- `internal/presenter/{session_detail_ops.go,session_topology.go,stats.go}` (consumers).
- `internal/adapters/aiagent_v3/ops.go` (real adapter sets EndTs; base.Ts = finalize time).
- `internal/ingest/catalog_idempotency_test.go` (masking fixtures: Finalized.Ts == OpStarted.Ts).
- `.agents/sow/specs/{data-model.md:140,307,322, ingester.md:288-294}`.
- Live seeded DB `/tmp/.../index.db` (6 ops, duration_us=0, end-start non-zero).

Current state:

- `writer.go` `durUS = ev.EndTs - ev.Ts` → 0 for spec-conformant adapters.
- `data-model.md:140` already says `end_ts - start_ts when both known` — code/spec drift.
- Tests pass only because fixtures mis-set `Finalized.Ts` to the start.

Risks:

- **R1 — Migration over real data.** Backfilling `ops.duration_us` and recomputing catalog rollups touches the operator's index.db. Mitigation: the index.db is a DERIVED cache (re-ingestable from sources); the backfill only corrects derived columns (no source data, no destructive drop); migration is additive UPDATEs guarded by `end_ts >= start_ts`; a migration test pins correctness.
- **R2 — Catalog recompute drift.** The recompute SUM must match the incremental accumulation's membership exactly, or rollups diverge. Mitigation: subagent reads `catalog.go` to mirror the exact grouping/filter; migration test compares recomputed totals against a fresh-ingest of the same events (the incremental path) to prove parity.
- **R3 — Orphan finalize.** A finalize without a start must not fabricate a duration. Mitigation: NULL when `start_ts` absent/zero; explicit test (AC#2).
- **R4 — Hidden additional consumers.** Another reader might depend on the (wrong) 0. Mitigation: same-failure scan for `duration_us` / `DurationUS` across the repo before close.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- `internal/ingest/writer.go` `applyOpFinalized` computes `duration_us = OpFinalizedEvent.EndTs - OpFinalizedEvent.Ts`. Per the canonical contract (`events.go` EventBase.Ts: events ordered by Ts; finalize sorts after start), `OpFinalizedEvent.Ts ≈ end`, so `EndTs - Ts ≈ 0`. The authoritative start lives in `ops.start_ts` (from `OpStartedEvent.Ts`). Correct formula: `duration_us = EndTs - start_ts`. Evidence: seeded DB has 6 ops with `duration_us=0` and non-zero `end_ts-start_ts`; `data-model.md:140` already specifies `end_ts - start_ts`; the only reason CI is green is that `catalog_idempotency_test.go` and peers set `Finalized.Ts == OpStarted.Ts`, masking the defect.

Evidence reviewed:

- `internal/ingest/writer.go` applyOpFinalized (`durUS = ev.EndTs - ev.Ts`) + the OpStarted insert writing `start_ts`.
- `internal/canonical/events.go` EventBase.Ts / OpStartedEvent / OpFinalizedEvent.
- `internal/ingest/catalog_idempotency_test.go:81,86,92,97` (Finalized.Ts == OpStarted.Ts; assertion `total_duration_us == 400 (1500-1100)` at `:385`).
- `internal/presenter/session_detail_ops.go:128`, `internal/presenter/session_topology.go:180,204`, `internal/presenter/stats.go:135`.
- `internal/ingest/catalog.go:247-336` (total_duration_us read/delta/apply).
- `.agents/sow/specs/data-model.md:140,307,322`; `.agents/sow/specs/ingester.md:288-294`.
- Live seeded DB query (6 ops; duration_us=0).

Affected contracts and surfaces:

- `internal/ingest/writer.go` (duration computation).
- `internal/store/migrations/0005_*.sql` (new backfill + rollup recompute).
- `.agents/sow/specs/data-model.md`, `.agents/sow/specs/ingester.md` (duration semantics + rollup recompute note).
- Tests: `internal/ingest/*_test.go` (conformant fixtures + new pinning tests + migration test).
- No public REST contract change (the JSON field `duration_us` already exists; only its VALUE becomes correct). No frontend change required — `session_detail_ops` already exposes the field; the Trace event list and Topology metric become correct automatically.

Existing patterns to reuse:

- Write-time computation already lives in `applyOpFinalized`; the `start_ts` read already exists in its pricing branch — generalize it.
- Migration pattern: `internal/store/migrations/000{2,3,4}_*.sql` (numbered, embedded, idempotent-friendly). `catalog_migrate.go` shows how rollups are recomputed/migrated in Go for reference, but the duration rollup recompute is a pure SQL `SUM` grouped by catalog identity.
- `internal/ingest/helpers_test.go` apply/seed helpers for the new tests.

Risk and blast radius:

- See Analysis R1–R4. Blast radius is the ingest writer + one additive migration + the catalog duration rollup + ingest tests. No adapter changes, no canonical event-shape change, no REST/JSON shape change, no frontend change.

Sensitive data handling plan:

- No secrets/PII involved. Tests use synthetic timestamps/ids. Migration touches only derived numeric columns. SOW evidence uses synthetic values only.

Implementation plan (subagent-produced code; master writes specs + this SOW):

1. **Spec deltas (master).** `data-model.md`: clarify `duration_us` is computed at finalize as `end_ts - start_ts` from the persisted `start_ts` (NOT the finalize event Ts), NULL when start unknown; note migration 0005 backfill + rollup recompute. `ingester.md`: document the OpFinalized duration computation + the masking-fixture lesson.
2. **Failing tests (subagent).** Add ingester tests: (a) spec-conformant `OpFinalized{Ts:E,EndTs:E}` with start `S<E` ⇒ `duration_us == E-S` (fails on current code); (b) orphan finalize ⇒ NULL; (c) migration test: pre-0005 DB with `duration_us=0` + stale catalog totals ⇒ post-migration corrected per-op + rollups; (d) parity test: migration-recomputed totals == fresh-ingest incremental totals for the same events.
3. **Writer fix (subagent).** In `applyOpFinalized`, read the op's persisted `start_ts` unconditionally (fold with the existing pricing lookup) and set `duration_us = EndTs - start_ts` when both known and `EndTs >= start_ts`, else leave unchanged/NULL. Preserve the catalog delta semantics (the delta reads duration before/after; now both reflect correct values).
4. **Migration 0005 (subagent).** `UPDATE ops SET duration_us = end_ts - start_ts WHERE start_ts IS NOT NULL AND end_ts IS NOT NULL AND end_ts >= start_ts;` then recompute `catalog_models.total_duration_us` and `catalog_tools.total_duration_us` as `SUM(ops.duration_us)` over the matching member ops (mirror catalog.go's exact grouping/filter — subagent reads catalog.go to confirm).
5. **Conformant fixtures (subagent).** Update duration-relevant writer/catalog test fixtures to set `Finalized.Ts = EndTs`; verify assertions unchanged (duration now derives from start_ts).
6. **Gates + spec-drift + same-failure scan (master).**

Validation plan:

- New ingester tests (AC#1–4) named in `internal/ingest/` (e.g. `op_duration_test.go`, `migration_0005_test.go`).
- `go test -race ./internal/ingest/...`, full `scripts/gates.sh`, `scripts/spec-drift.sh`.
- Manual: rebuild binary, re-seed e2e DB, curl `/api/sessions/:id` and confirm `duration_us` non-zero + Trace event-list Duration column populated + Topology `metric=duration` differentiates nodes.
- Same-failure scan: `grep -rn "duration_us\|DurationUS\|\.Ts\b.*EndTs\|EndTs.*\.Ts"` to ensure no other site repeats the `EndTs - Ts` pattern.

Artifact impact plan:

- AGENTS.md: no change (no new convention; lesson captured in ingester.md + SOW).
- Runtime project skills: no change.
- Specs: `data-model.md`, `ingester.md` updated (AC#5).
- End-user/operator docs: no change (internal correctness fix; no operator-facing surface change beyond correct numbers).
- End-user/operator skills: none.
- SOW lifecycle: on success, `Status: completed` + move to `.agents/sow/done/` in the implementing commit; this SOW unblocks SOW-0006 continuation.

Open-source reference evidence:

- None required; this is an internal data-model correctness fix grounded in the project's own canonical contract and specs.

Open decisions:

- **D1 (resolved, CTO):** Fix location — **write-time compute from persisted `start_ts`** (chosen) vs SQLite **generated column** `duration_us GENERATED ALWAYS AS (end_ts - start_ts)` (rejected). Generated column is the purest single-source-of-truth and structurally unforgeable, but converting an existing column to generated requires a full `ops` table rebuild (CREATE+INSERT SELECT+drop+rename) — higher migration blast radius (indexes, large table) for a workstation tool. The write-time fix is surgical (~lines), matches the existing write-time-computation pattern in `applyOpFinalized`, and needs only an additive backfill UPDATE. Chosen for minimal blast radius + pattern consistency. Generated-column reconsidered if a future SOW rebuilds `ops` for other reasons.

## Implications And Decisions

1. **Bug, not redesign.** `data-model.md:140` already prescribes `end_ts - start_ts`; this restores code↔spec agreement. No product/UX decision required.
2. **Fix-first sequencing (CTO decision).** SOW-0006's Trace event-list Duration and Topology `metric=duration` both consume `ops.duration_us`; this fix lands before the SOW-0006 frontend continues, so those surfaces are built/verified on correct data.
3. **Migration corrects derived data only.** index.db is a re-ingestable cache; backfill is non-destructive. No operator risk-acceptance gate needed.

## Plan

1. Spec deltas (data-model.md, ingester.md) — master.
2. Failing + orphan + migration + parity tests — subagent.
3. Writer fix (EndTs - start_ts) — subagent.
4. Migration 0005 (backfill + rollup recompute) — subagent.
5. Spec-conformant fixtures — subagent.
6. Gates + spec-drift + same-failure scan + external review + merge — master.

## Execution Log

### 2026-05-31

- Created SOW from SOW-0006 Trace-tab visual review finding. Root-cause + blast-radius investigation complete (evidence in Analysis/Gate). Gate filled, ready.
- Implemented (subagent): writer.go derives duration from persisted start_ts; migration 0005 (backfill + catalog recompute); spec-conformant fixtures; tests (op_duration_test, migration_0005 test). Committed `d80de5c`. Backend gates green (race; golangci-lint 0; gosec 0; coverage 88.5% ingest / 90.9% store; secret + attribution scans clean). PR #31.
- Review round 1 (codex+glm+minimax) addressed in a second commit (see ## Reviews); orchestrator re-verified `go test -race` + lint green on ingest/store/presenter.

## Validation

Pending (completed at close: AC evidence + final gate run + round-2 review convergence).

## Reviews

### Round 1 — 2026-05-31 (codex + glm + minimax, parallel, read-only) on commit `d80de5c`

All three: NO P1, NO security, NO regression; core fix (duration from persisted start_ts) + migration 0005 grouping confirmed correct. Findings, each adjudicated against code:

- **P2 (codex) — end_ts/duration_us inconsistency on zero/skewed-EndTs re-finalize.** Verified real: UPDATE bound `end_ts = nullIfZero(ev.EndTs)` unconditionally, so a corrective re-finalize carrying `EndTs=0` clobbered a good `end_ts→NULL` while `duration_us` (COALESCE-preserved) stayed non-NULL — violating `duration_us = end_ts - start_ts`. **Fixed**: end_ts + duration_us computed from ONE validity gate, both written via COALESCE (`end_ts = COALESCE(?, end_ts)`), so a zero/skewed end preserves both. Test strengthened: end_ts preservation + a clock-skew (end<start) re-finalize scenario.
- **P2 (glm) — migration catalog_models recompute imperfectly mirrored catalog.go.** Verified harmless (spurious `o.name IS NOT NULL` always true; non-empty guard implied by join to non-empty PKs). **Fixed**: removed the spurious clause; added explicit `catalog_models.provider <> '' AND name <> ''` (mirrors catalog.go:296). Tools recompute unchanged (catalog.go tool path has no non-empty guard — adding one would diverge).
- **P2 (minimax) — stats.go:135 comment.** minimax's suggested wording ("sums ops.duration_us") was factually WRONG — `statsTotals` sums SESSION `s.end_ts - s.start_ts`, not ops.duration_us. **Fixed** with an accurate, disambiguating comment (no-false-information rule).
- **P3 (codex)** — data-model.md migration history missing 0005 → added (data-only, schema-version-neutral). **P3 (codex)** — presenter.go SchemaVersion comment implied all migrations bump it → clarified (schema-shape vs data-only). **P3 (codex)** — 4 remaining masking fixtures (catalog_test.go, pricing_integration_test.go ×3) → set `Finalized.Ts=EndTs`; grep confirms none remain. **P3 (minimax)** — stale test comments → modernized. **P3 (glm)** — added `migration_live_parity_test.go` (live-ingest totals == migration-recompute totals).

Post-fix (orchestrator, ground truth): `go test -race` green on ingest/store/presenter; golangci-lint 0; gosec 0. Round-2 re-review pending (same scope + these fix notes).

## Outcome

Pending.

## Lessons Extracted

Pending. (Provisional: a test fixture that mis-sets an event field can mask a production bug indefinitely; fixtures must be spec-conformant — `Finalized.Ts` is the finalize time, not the start.)

## Followup

None yet.

## Regression Log

None yet.
