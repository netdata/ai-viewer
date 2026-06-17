# SOW-0040 - Vitest unit coverage for Layout + StatCard (close the SOW-0012 coverage exclusions)

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: filed 2026-06-03 as a tracked follow-up of SOW-0012 (frontend quality stack). SOW-0012's coverage-config disk-completeness check requires every source dir/flat-file under `src/components/` and `src/pages/` to be either measured or in `COVERAGE_EXCLUDED`. Two REAL components — `src/components/Layout` and `src/components/StatCard` — were placed in `COVERAGE_EXCLUDED` (exercised end-to-end by Playwright, but lacking dedicated Vitest unit tests). This SOW pays that debt: add unit tests and move both from `COVERAGE_EXCLUDED` into the measured + per-dir-gated set.

## Requirements

### Purpose

Eliminate the two real-component exclusions SOW-0012 had to record so the per-directory Vitest coverage gate covers the entire implemented component surface (`AGENTS.md` rule 5: every shipped behavior has an automated test; coverage thresholds enforced in CI). The app shell (`Layout`) and the stat tile (`StatCard`) are real, shipping UI; they should meet the same ≥ 80% per-dir line floor every other component dir meets, not lean solely on E2E.

### User Request

Implicit follow-up created by the assistant during SOW-0012 review convergence (operator's standing backlog mandate). No new operator request; this tracks deferred test debt so it is not silently lost.

### Assistant Understanding

Facts:

- `frontend/vitest.coverage.mjs` lists `src/components/Layout` and `src/components/StatCard` in `COVERAGE_EXCLUDED` with the rationale "real component, Vitest unit deferred to a tracked follow-up; exercised by Playwright today."
- `frontend/scripts/check-coverage-config.mjs` (SOW-0012) fails closed if a source dir is in neither `COVERAGE_INCLUDE` nor `COVERAGE_EXCLUDED`, so the exclusions are explicit and enforced, not silent.
- `Layout.tsx` is the app shell (header brand + primary `NavLink` nav + `ThemeToggle` + `FilterBar` + routed `<Outlet/>`); `StatCard.tsx` is a presentational tile used by `/stats`.
- The per-dir floor mechanism (`PER_DIR_GLOBS` in `vitest.coverage.mjs`) and the React Testing Library + Vitest harness already exist (every other component dir is tested this way).

Inferences:

- Both components are low-logic/presentational, so reaching ≥ 80% lines with RTL render tests (nav links present, active-route class, theme toggle wiring for Layout; label/value/variant rendering for StatCard) is straightforward.
- No production-code change is expected — this is test-only plus the two-list bookkeeping move.

Unknowns:

- Whether `Layout`'s router/context dependencies (`react-router` `Outlet`, theme/filter state) need light test harness wrapping; to be confirmed when writing the tests (mirror existing component tests that already render router-aware components).

### Acceptance Criteria

1. **Layout unit-tested + gated.** `src/components/Layout` has a Vitest + RTL test covering its rendered nav, theme control, and outlet wiring; it is moved from `COVERAGE_EXCLUDED` into `COVERAGE_INCLUDE` + `PER_DIR_GLOBS` and meets the ≥ 80% per-dir line floor. **Verification:** `npm run test -- --run --coverage` green with the new per-dir floor active; `npm run check:coverage-config` shows Layout measured (no longer excluded).
2. **StatCard unit-tested + gated.** Same treatment for `src/components/StatCard`. **Verification:** as above.
3. **Exclusion ledger shrinks honestly.** After this SOW, `COVERAGE_EXCLUDED` contains only genuine no-logic stubs (`Agents`/`Models`/`Tools` Phase-3 `<ComingSoon/>` wrappers, `NotFound.tsx`); Layout/StatCard are gone from it. The disk-completeness check still passes (every dir measured-or-excluded). **Verification:** `npm run check:coverage-config:selftest` + the real verifier both green.
4. **Specs synced.** `quality-gates.md` + `project-frontend`/`project-quality-gates` skills no longer describe Layout/StatCard as deferred. **Verification:** spec/skill diff in the same commit.

## Analysis

Sources checked:

- `frontend/vitest.coverage.mjs` (`COVERAGE_EXCLUDED` ledger), `frontend/scripts/check-coverage-config.mjs` (disk-completeness), `frontend/src/components/{Layout,StatCard}/`.
- SOW-0012 (`.agents/sow/current|done/SOW-0012-…`) Chunk F round-3 fixes (R4-1).

Current state:

- Layout + StatCard ship and render but carry no dedicated Vitest unit test; they are covered only transitively by Playwright E2E. The per-dir line floor does not apply to them (they are excluded).

Risks:

- Low. Test-only work plus a two-list move. Risk is limited to flaky/over-mocked component tests — mitigated by mirroring the existing RTL render-test convention. No runtime/product behavior changes.

## Pre-Implementation Gate

Status: blocked

(Filled when this SOW is approved and moved to `current/`. Test-only scope; no runtime contract change anticipated. The move from `COVERAGE_EXCLUDED` to `COVERAGE_INCLUDE`+`PER_DIR_GLOBS` must keep the coverage-config verifier green.)

## Implications And Decisions

None yet (no open decisions; pending operator prioritization under the backlog).

## Plan

1. Write `Layout.test.tsx` + `StatCard.test.tsx` (RTL render tests to ≥ 80% lines), mirroring existing component tests.
2. Move both dirs from `COVERAGE_EXCLUDED` to `COVERAGE_INCLUDE` + `PER_DIR_GLOBS`; run the real coverage + the coverage-config verifier + its self-test.
3. Sync `quality-gates.md` + skills (drop the deferred-coverage note). External review per `project-second-opinions`; converge; PR; self-merge.

## Execution Log

(none yet)

## Validation

(filled at close)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
