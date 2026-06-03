# SOW-0012 - Frontend Quality Stack: ESLint flat config, strict TS, Vitest coverage, Playwright + axe, bundle budget

## Status

Status: in-progress

Sub-state: opened 2026-06-03 under the operator's standing backlog mandate (blanket sign-off; the SOW has no open decisions requiring operator input — see "Open decisions"). The SOW-0001 Chunk 14 dependency is LANDED (the `frontend/` scaffolding exists). Drafted greenfield, but the frontend now exists with partial config, so the real scope is the GAPS — reconciled in the Execution Log (2026-06-03). Lands the frontend half of `quality-gates.md` so every Frontend gate is enforced locally + in CI.

## Requirements

### Purpose

Stand up the full frontend quality stack for ai-viewer so every Frontend row in `.agents/sow/specs/quality-gates.md` is actually enforced — not just documented. The bootstrap and SOW-0001 specify the gates and the runtime skill explains the commands; this SOW lands the configs, the package scripts, the supporting scripts, and the CI wiring that make the gates real. Without this SOW, "zero ESLint warnings" and "≥ 80% coverage" remain aspirational text in a markdown file.

### User Request

Implicit in the bootstrap operating contract (`AGENTS.md` rules 4 and 5: "untested ≡ broken", "every behavior has an automated test", "coverage thresholds are enforced in CI") and explicit in `.agents/sow/specs/quality-gates.md` Frontend rows. The repo cannot ship frontend code without this stack present and green.

### Assistant Understanding

Facts:

- `quality-gates.md` lists eight Frontend gates: Lint, Type Check, Unit/Component, E2E, Accessibility, Bundle Size, plus the aggregate `scripts/lint.sh` extension and the CI workflow. Each has a stated command and threshold.
- `project-frontend/SKILL.md` already commits to: ESLint flat config with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks` (zero warnings), `strict` TypeScript with `noUncheckedIndexedAccess` and `exactOptionalPropertyTypes`, Vitest + React Testing Library, Playwright for E2E, CSS Modules + custom properties (no Tailwind, no CSS-in-JS), D3 isolated under `viz/`.
- `project-quality-gates/SKILL.md` documents the exact commands the assistant must run locally and CI must mirror.
- SOW-0001 Chunks 14–18 deliver the Vite + React + TS scaffolding, the pages (SessionsList, SessionDetail Overview + Logs, Sources), the SSE integration, and the build pipeline — but Chunk 14's scope is the app code, not the quality stack around it.
- ESLint 9.x flat config (`eslint.config.js`) is the latest stable shape; the deprecated `.eslintrc.*` format is end-of-life. Plugin support for flat config varies per plugin and must be verified at implementation time.
- Playwright supports `@axe-core/playwright` for in-test accessibility assertions; axe rule sets are configurable and serious/critical violations are the documented threshold.
- The bundle size gate's threshold (main chunk ≤ 500 KB gz, per-route lazy chunks ≤ 200 KB gz) requires a small custom script because `vite build` emits raw sizes and the gate measures gzipped output.

Inferences:

- Total frontend gate runtime on CI should fit comfortably under 3 minutes given the Phase 1 surface size (3 pages, ~12 components). If it exceeds 3 minutes once the codebase grows, that is a future SOW to parallelize, not a reason to weaken the gate.
- Some ESLint plugins (notably `eslint-plugin-import` and `eslint-plugin-jsx-a11y`) historically lagged on flat-config support. Implementation must verify each plugin against its latest release notes and pin known-working versions; falling back to `FlatCompat` is acceptable only as documented bridge code, not a permanent state.
- D3-generated SVG inside `src/viz/` may trip axe rules around ARIA labelling or color contrast. The mitigation is to document each waiver with rationale in `src/viz/<chart>/a11y.md` and exclude via axe options scoped to specific selectors — never globally disabling rules.
- Playwright flake risk on CI hardware (GitHub Actions runners are slower than the workstation) is real. The mitigation is selectively increased per-test timeout and `retries: 1` for known-flaky checkpoints, with every flaky pattern tracked in a SOW so the test is fixed, not papered over.

Unknowns:

- Exact bundle size of the Phase 1 surface once it exists — to be measured during implementation. If the main chunk exceeds 500 KB gz on a 3-page app, that is a defect to investigate (likely a dependency that should have been code-split), not a threshold to raise.
- Whether `eslint-plugin-import` and `eslint-plugin-jsx-a11y` ship native flat-config support at the time of implementation; to be checked against npm and each plugin's README before pinning versions.
- Whether Vitest's per-directory coverage threshold mechanism (`coverage.thresholds.perFile` vs custom collector) needs a script wrapper to enforce per-component-directory ≥ 80% lines; to be verified against the latest Vitest release notes.

### Acceptance Criteria

1. **ESLint flat config present and clean.** `frontend/eslint.config.js` exists with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import`. `npm run lint -- --max-warnings=0` exits 0 on the Phase 1 codebase. **Verification**: gate runs locally and in CI, both green, on the SOW's own delivery commit.
2. **TypeScript strict config enforced.** `frontend/tsconfig.json` sets `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`, `noUnusedLocals`, `noUnusedParameters`. `npm run typecheck` (invoking `tsc --noEmit`) exits 0. **Verification**: gate runs locally and in CI; intentional violation test (during dev) confirms each flag actually rejects bad code.
3. **Vitest coverage with per-directory thresholds.** `npm run test -- --run --coverage` produces an HTML coverage report and enforces ≥ 80% lines per component directory under `src/components/` and `src/pages/`. Failing thresholds exits non-zero. **Verification**: gate runs locally and in CI; CI artifact upload contains the HTML report.
4. **Playwright E2E suite covers the listed flows.** Scenarios: sessions list filter, session detail load, sources panel, real-time SSE update (using a deterministic fixture), theme toggle (verifies OS-prefers-color-scheme detection AND manual override persisted via `localStorage`). All scenarios pass; flaky scenarios are quarantined into `frontend/tests/quarantine/` with a linked SOW filename inline in the test file header. `test.skip` is forbidden — quarantined tests still run, they just don't gate merge until the linked SOW resolves. **Verification**: `npm run e2e` exits 0; quarantine directory contains zero tests on delivery.
5. **Accessibility enforced via `@axe-core/playwright`.** Every Playwright route runs an axe scan; zero serious/critical violations is the gate. Documented waivers (for third-party D3 output) live in `src/viz/<chart>/a11y.md` and are applied via per-selector axe options, never global disables. **Verification**: `npm run e2e:a11y` exits 0; waiver docs exist for every excluded selector.
6. **Bundle size budget enforced via custom script.** `frontend/scripts/check-bundle-size.js` reads `dist/assets/*.js`, computes gzipped sizes, fails if main chunk > 500 KB gz or any per-route lazy chunk > 200 KB gz. CI uploads the bundle analyzer report on every PR. **Verification**: script runs locally after `vite build` and in CI; CI artifact contains the analyzer report.
7. **`scripts/lint.sh` extended to aggregate frontend gates.** The repo-wide `scripts/lint.sh` invokes the frontend lint + typecheck + bundle-size checks in sequence, fail-fast, with clear section headers. **Verification**: script exits 0 on the delivery commit; failure of any sub-gate halts the script and reports which gate failed.
8. **Total frontend gate runtime < 3 minutes on CI.** Measured as wall-clock from job start to job finish on the standard GitHub Actions runner (ubuntu-latest, 4-core). **Verification**: CI workflow timing on the delivery PR is captured in the SOW's `## Validation` section.
9. **Specs updated in the same commit as the implementation.** `quality-gates.md` already documents the targets; this SOW does not change them but confirms each command matches reality. `project-frontend/SKILL.md` is updated only if implementation reveals a divergence (e.g. plugin substitution). **Verification**: diff of specs alongside config files in each commit.

## Analysis

Sources checked (at SOW drafting):

- `.agents/sow/specs/quality-gates.md` — Frontend rows: Lint, Type Check, Unit/Component, E2E, Accessibility, Bundle Size, aggregate scripts.
- `.agents/skills/project-quality-gates/SKILL.md` — exact commands the assistant must run locally.
- `.agents/skills/project-frontend/SKILL.md` — committed file conventions, TypeScript strict flags, ESLint flat-config plan, Vitest + Playwright stack, CSS Modules + custom properties, D3 isolated under `viz/`.
- `.agents/skills/project-specs-sync/SKILL.md` — spec-first ordering; specs lead tests lead code.
- `AGENTS.md` — Quality Gates section (table summary) and Operating Contract rules 4 (untested ≡ broken) and 5 (coverage thresholds enforced in CI).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunk 14 — frontend scaffolding scope (the prerequisite this SOW depends on).
- `.agents/sow/specs/index.md` — where new specs land if any are created.

Current state of frontend tooling in the repo:

- No `frontend/` directory yet — created by SOW-0001 Chunk 14.
- No `eslint.config.js`, no `tsconfig.json` with strict flags, no `vitest.config.ts` with coverage thresholds, no Playwright config, no axe wiring, no bundle-size script.
- No `.github/workflows/ci.yml` yet — created by SOW-0013 (the repo-wide integration SOW that this SOW's frontend job feeds into).

Risks:

- **R1 — ESLint flat-config plugin maturity**: some plugins (notably `eslint-plugin-import`, `eslint-plugin-jsx-a11y` historically) lagged on flat-config support. Mitigation: at implementation time, check the latest npm version and the plugin's README for native flat-config support; if missing, use `FlatCompat` as a documented bridge with an inline comment pointing to the plugin's open issue tracking flat-config support. Pin known-working versions to avoid passive regressions.
- **R2 — axe failures on third-party D3 SVG output**: the topology view and timeline view will render D3 SVGs that may trip axe rules around ARIA labelling, role attributes, or color contrast for stroke patterns. Mitigation: every excluded selector gets a documented waiver in `src/viz/<chart>/a11y.md` explaining the rationale and the upstream issue tracking a fix; waivers are reviewed quarterly. Global axe rule disables are forbidden.
- **R3 — Playwright flakiness on slow CI hardware**: GitHub Actions runners are roughly 2× slower than the workstation; timing-sensitive tests (SSE update arrival within 2s) may flake. Mitigation: per-test `timeout` budgets are generous (15 s default, 30 s for SSE); `retries: 1` is configured for the SSE flow specifically; any test pattern that flakes 3+ times gets a SOW to fix the root cause (deterministic fixture, fake clock, etc.). Flaky tests are quarantined, never `test.skip`-ed.
- **R4 — bundle size on first measurement**: if the Phase 1 surface measures above 500 KB gz main chunk on first build, the response is to investigate (likely an accidentally bundled large dependency, e.g. D3 in a non-lazy import), not to raise the threshold. The threshold was set knowing Phase 1's surface is small.
- **R5 — gate runtime drift**: as the codebase grows, total gate runtime trends up. Mitigation: the < 3 min target is documented; CI captures per-job timing; if a single PR pushes total runtime over 3 min, the SOW to parallelize is filed before merging that PR.
- **R6 — Vitest per-directory coverage threshold mechanism**: if Vitest doesn't natively support per-component-directory thresholds at the time of implementation, a small wrapper script reads `coverage-final.json` and enforces the threshold per directory. The mechanism is verified against the latest Vitest release notes during implementation.

## Pre-Implementation Gate

Status: ready (SOW-0001 Chunk 14 landed; opened under the operator's standing backlog mandate). Current-state reconciliation + revised remaining-chunk plan in the Execution Log (2026-06-03).

Problem / root-cause model:

- The frontend quality gates documented in `quality-gates.md` are presently aspirational text — no `eslint.config.js`, no strict TS, no Vitest coverage thresholds, no Playwright, no axe, no bundle script exist in the repo. Without enforced gates, the operating contract's "untested ≡ broken" rule cannot be honored for frontend code.

Evidence reviewed:

- `quality-gates.md` Frontend rows (Lint, Type Check, Unit/Component, E2E, Accessibility, Bundle Size).
- `project-quality-gates/SKILL.md` exact commands.
- `project-frontend/SKILL.md` stack commitments.
- ESLint 9.x flat config documentation (current stable shape at time of SOW drafting; to be re-verified at implementation).
- `@axe-core/playwright` API documentation (current stable).
- Vite documentation on `build.rollupOptions.output.manualChunks` for code-splitting (basis of the per-route lazy-chunk budget).

Affected contracts and surfaces:

- New: `frontend/eslint.config.js`, `frontend/tsconfig.json` (strict), `frontend/vitest.config.ts` (coverage thresholds), `frontend/playwright.config.ts`, `frontend/tests/` (E2E + a11y tests), `frontend/scripts/check-bundle-size.js`.
- Modified: `frontend/package.json` (scripts: `lint`, `typecheck`, `test`, `e2e`, `e2e:a11y`, `build:analyze`; devDependencies), `scripts/lint.sh` (extended to aggregate frontend gates).
- Not modified: `quality-gates.md` (already authoritative; no spec changes expected unless implementation reveals a divergence), `project-frontend/SKILL.md` (modified only if a plugin substitution is required).

Existing patterns to reuse:

- ESLint 9 flat-config examples from official ESLint docs; React + TypeScript reference projects from the React docs.
- `@axe-core/playwright` README example for per-route axe injection.
- Vitest's `coverage.thresholds` reporter for native per-file/per-directory enforcement.
- The pattern of "extend `scripts/lint.sh` with a clearly-labelled section per language" mirrors the Go side that lands in SOW-0009.

Spec deltas to land before any test or code:

- `.agents/sow/specs/quality-gates.md`: no diff expected — this SOW implements what the spec already states. If implementation reveals a divergence (e.g. a plugin missing flat-config support requiring a substitution), the spec is updated in the same commit with the substitution noted under the relevant gate.
- `.agents/skills/project-frontend/SKILL.md`: no diff expected — the skill already commits to flat config, strict TS, Vitest + Playwright. If implementation substitutes a plugin or pins a specific version, the skill is updated to record the choice.
- No new spec files expected. If during implementation a new convention emerges (e.g. a documented pattern for per-`viz/<chart>/` a11y waiver files), that pattern is added to `project-frontend/SKILL.md`.

Risk and blast radius:

- Local-only impact (workstation tool, no production deployment in scope). Worst case: a gate is too strict and blocks unrelated PRs — the fix is to tune the gate, not to weaken it, after evidence is gathered.
- Plugin version pinning may cause friction if Dependabot opens a major bump that breaks flat-config compatibility; SOW-0013 sets up Dependabot to surface these early.

Sensitive data handling plan:

- Playwright tests use deterministic fixture data only — no real session content, no operator names, no IP addresses. Fixtures live under `frontend/tests/fixtures/` and follow the same sanitization rules as `testdata/` per `AGENTS.md`. The secret scanner from SOW-0013 covers this directory too.
- Coverage reports and bundle analyzer artifacts uploaded by CI may contain file paths but no source content; verified safe.

Implementation plan (ordered chunks):

1. **Verify plugin flat-config support and pin versions**: at implementation time, check current npm versions of `eslint`, `@typescript-eslint/eslint-plugin`, `@typescript-eslint/parser`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import` for flat-config support. Pin known-working versions in `frontend/package.json`. Document any `FlatCompat` usage inline.
2. **Land `frontend/eslint.config.js`** with the verified plugin set; add `npm run lint` script; run against the Phase 1 codebase and fix every warning at the source.
3. **Tighten `frontend/tsconfig.json`** to enable every strict flag listed in the spec; add `npm run typecheck` script; run and fix every error at the source.
4. **Configure Vitest coverage** in `frontend/vitest.config.ts`: enable coverage reporter (`v8`), set per-directory thresholds ≥ 80% lines for `src/components/**` and `src/pages/**`. If native per-directory thresholds aren't available in the current Vitest, write a small wrapper script `frontend/scripts/check-coverage.js` that reads `coverage-final.json` and enforces.
5. **Configure Playwright** in `frontend/playwright.config.ts`: headless, single-browser (Chromium) for default `npm run e2e`, all-browser matrix for nightly CI. Per-test timeouts: 15 s default, 30 s for SSE flow. `retries: 1` only for the SSE scenario, documented inline. Land the five listed scenarios: sessions-list filter, session-detail load, sources panel, real-time SSE update (deterministic fixture), theme toggle (OS-match + manual override).
6. **Wire `@axe-core/playwright`**: per-route axe injection in a shared test fixture; threshold = zero serious/critical violations. Document waivers per `src/viz/<chart>/a11y.md`.
7. **Land `frontend/scripts/check-bundle-size.js`**: reads `dist/assets/*.js`, computes gzipped sizes using Node's `zlib.gzipSync`, fails if main chunk > 500 KB gz or per-route lazy chunk > 200 KB gz. Add `npm run build:analyze` for the analyzer report.
8. **Extend `scripts/lint.sh`** to aggregate frontend lint + typecheck + bundle-size checks alongside the Go gates (the Go side lands in SOW-0009; this SOW adds the frontend section). Fail-fast, clear section headers.
9. **Provide the CI workflow stanzas** that SOW-0013 will integrate into `.github/workflows/ci.yml`: `frontend-lint`, `frontend-typecheck`, `frontend-test`, `frontend-e2e`, `frontend-bundle-size` jobs with appropriate npm caching. The actual workflow file lands in SOW-0013.
10. **Measure total frontend gate runtime on CI** and record in the SOW's `## Validation`. If > 3 minutes, file follow-up SOW to parallelize.
11. **External review round**: at least three reviewers (per `project-second-opinions/SKILL.md`), prompt = "review SOW-0012 changes for: gate completeness, threshold soundness, CI integration correctness, spec drift, unwanted side effects". Iterate until convergence.
12. **Mark SOW completed and move to `done/`** in the same commit as the final implementation.

Validation plan:

- Each gate runs locally and exits 0 on the delivery commit (evidence: command + output captured in `## Validation`).
- CI runs every gate on the delivery PR and exits 0 (evidence: PR check status).
- Intentional-violation tests during implementation confirm each strict TS flag, each ESLint rule, each axe threshold, and the bundle budget actually reject bad inputs (evidence: dev-time commit fragments cited in `## Implementation`).
- Total CI frontend job runtime captured (evidence: GitHub Actions timing).
- Reviewer findings addressed; reviewers re-run with the same scope plus fix notes until no actionable findings remain.

Artifact impact plan:

- `AGENTS.md`: no expected change. The Quality Gates table is a summary, not the authoritative catalog.
- `.agents/sow/specs/quality-gates.md`: no diff expected; updated only if implementation reveals a divergence from the documented commands/thresholds.
- `.agents/skills/project-frontend/SKILL.md`: updated only if plugin substitution or version pinning rationale needs to be recorded.
- `.agents/skills/project-quality-gates/SKILL.md`: no diff expected.
- `README.md`: no diff expected (CI badges land in SOW-0013).
- New: `frontend/eslint.config.js`, `frontend/tsconfig.json` (or strict-flag update if Chunk 14 produced a permissive one), `frontend/vitest.config.ts` (or coverage-config update), `frontend/playwright.config.ts`, `frontend/tests/` (E2E + a11y), `frontend/scripts/check-bundle-size.js`, `frontend/scripts/check-coverage.js` (if needed).
- `scripts/lint.sh`: extended with frontend section.

Open-source reference evidence:

- ESLint 9 flat-config examples: `eslint/eslint @ HEAD` — `docs/src/use/configure/configuration-files.md`.
- `@axe-core/playwright`: `dequelabs/axe-core-npm @ HEAD` — `packages/playwright/README.md`.
- Vitest coverage: `vitest-dev/vitest @ HEAD` — `docs/guide/coverage.md`.
- Vite manual chunks: `vitejs/vite @ HEAD` — `docs/guide/build.md` (Chunking Strategy section).

Open decisions:

- None requiring operator input. All choices fall within the assistant's CTO authority per `AGENTS.md` rule 1.

## Implications And Decisions

No operator decisions are required for this SOW. All choices are within the assistant's technical authority and are documented in the `quality-gates.md` spec and the `project-frontend` skill. If implementation surfaces a question that materially changes scope (e.g. a plugin substitution that changes the rule surface significantly), an addendum is written and the operator is asked.

## Plan

See `Pre-Implementation Gate / Implementation plan` above. Twelve chunks, expected to land in 3–5 commits with the final commit moving the SOW to `done/`.

## Execution Log

### 2026-06-03 — Open + current-state reconciliation

Opened under the operator's standing backlog mandate (blanket sign-off for the
whole pending backlog; SOW-0008 stays last). The SOW was drafted greenfield
("no frontend/ directory yet"), but the frontend scaffolding (SOW-0001 Chunk 14)
plus later SOWs (0006 trace UI, 0007 stats) have since landed, so several gates
already exist. Verified current state on master (`bfa8b98`):

- **LANDED (no work):** `frontend/tsconfig.json` strict flags (`strict`,
  `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, …); `npm run
  typecheck` exists. The per-push CI `frontend` job already runs lint, typecheck,
  unit+coverage, Playwright+axe, and a bundle-size REPORT.
- **PARTIAL:** `frontend/eslint.config.ts` (note: `.ts`, not the `.js` the AC
  names — a divergence to record in the spec/skill) exists with the
  typescript-eslint + react + react-hooks stack but is MISSING
  `eslint-plugin-jsx-a11y` + `eslint-plugin-import`. `frontend/vitest.config.ts`
  has aggregate coverage thresholds but not per-directory. `playwright.config.ts`
  + axe tests exist; the 5 AC scenarios + per-test SSE timeout/`retries:1`
  tuning + an `e2e:a11y` script need confirming/adding.
- **NOT-STARTED:** `frontend/scripts/check-bundle-size.js` (gzip-budget GATE; CI
  has only a report today); the `scripts/lint.sh` frontend section (Go-only).

Remaining chunks (revised from the greenfield 12-chunk plan):
- A. `frontend/scripts/check-bundle-size.js` + self-test; upgrade the CI
  bundle-size report → enforced gate. Self-contained; mirrors
  `scripts/check-coverage.sh` + `scripts/test/check-coverage-test.sh`.
- B. Add `eslint-plugin-jsx-a11y` + `eslint-plugin-import` (verify flat-config
  support; pin); fix every new warning at the source.
- C. Per-directory Vitest coverage (native per-dir `thresholds`, else a
  `frontend/scripts/check-coverage.js` wrapper).
- D. Confirm/complete the 5 Playwright scenarios; add `e2e:a11y`; tune the SSE
  test timeout/`retries:1`; document `src/viz/<chart>/a11y.md` waivers.
- E. Extend `scripts/lint.sh` with a frontend section (lint + typecheck +
  bundle-size), fail-fast.
- F. Spec sync (eslint.config `.ts` vs `.js`); all gates; ≥3 external reviewers
  to convergence; PR; self-merge.

(Per-chunk commit refs + evidence appended below as work proceeds.)

### 2026-06-03 — Chunk A: bundle-size REPORT → enforced GATE (delegated)

- `frontend/scripts/check-bundle-size.js` (new): manifest-driven gate. Reads
  `dist/.vite/manifest.json`; `isEntry` ⇒ main (≤ 500 KB gz), `isDynamicEntry`
  ⇒ per-route lazy (≤ 200 KB gz). Non-entry / `?worker` chunks are reported but
  not gated. Fail-closed (exit 2) on missing/empty dist, missing/invalid
  manifest, zero classified chunks, or a manifest file absent on disk; exit 1 on
  a budget violation; exit 0 within budget.
- `frontend/scripts/check-bundle-size.test.sh` (new): 8-assertion self-test with
  high-entropy (incompressible) fixtures so the gzipped budgets are genuinely
  exercised; covers all three exit codes.
- `frontend/vite.config.ts`: `build.manifest: true`. `frontend/package.json`:
  `check:bundle-size` + `check:bundle-size:selftest` scripts.
- `frontend/eslint.config.ts`: ignore `scripts/` (standalone Node tooling
  outside the app tsconfig project; exercised by its own self-test).
- `.github/workflows/ci.yml` `frontend` job: hermetic self-test step + the size
  report uploaded BEFORE the enforcing gate (retained on failure) + the gate.
- Specs synced: `quality-gates.md` §Frontend — Bundle Size, `project-quality-
  gates` + `project-frontend` skills (manifest classification, fail-closed,
  dist-dir arg, "never raise the threshold").

Verified locally (orchestrator-run, not the subagent's word): self-test 8/8;
real-build gate PASS (main 131.5 KB gz / 500; worker 6.0 KB ungated; no lazy
chunks yet); `npm run lint --max-warnings=0` + `npm run typecheck` green;
`actionlint` clean; secret + AI-attribution scans clean. Spec-divergence
recorded: the gate takes a dist-DIR arg (fail-closed on empty) rather than the
old `dist/assets/*.js` glob. The `tseslint.config` LSP deprecation in
eslint.config.ts is pre-existing + non-gating (folded into Chunk B).

## Validation

(Filled at SOW close. Each acceptance criterion gets evidence: command + output summary, CI run URL, reviewer finding summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
