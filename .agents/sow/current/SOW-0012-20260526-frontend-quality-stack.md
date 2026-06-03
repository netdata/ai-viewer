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

### 2026-06-03 — Chunk B: add jsx-a11y + import ESLint plugins (delegated)

Plugins added (exact pins, native ESLint-9 flat-config — NO FlatCompat needed;
both ship `flatConfigs.*`):
- `eslint-plugin-jsx-a11y` 6.10.2 — `jsxA11y.flatConfigs.recommended`, scoped to
  `**/*.tsx`.
- `eslint-plugin-import` 2.32.0 — `importPlugin.flatConfigs.recommended` +
  `.typescript`, applied via `extends` on the TS/TSX block.
- `eslint-import-resolver-typescript` 4.4.5 — wired through
  `settings['import/resolver'].typescript` so `import/no-unresolved` follows
  tsconfig paths and `.ts`/`.tsx` specifiers (no false unresolved on type-only
  or extensionless imports).

`tseslint.config()` deprecation fix (AC item 4 / TS 6387): typescript-eslint's
`config()` helper carries `@deprecated` (its `.d.ts` points to
typescript-eslint.io/.../#config-deprecated — ESLint core now owns this). Migrated
the config builder to ESLint core's `defineConfig()` + `globalIgnores()` from
`eslint/config` (verified exported by the pinned eslint 9.39.4). `tseslint` is
retained only for its parser/plugin + shared `recommendedTypeChecked` preset.

import/recommended ships `no-named-as-default`, `no-named-as-default-member`,
`no-duplicates` as `'warn'`; under the zero-warnings gate a warning fails the
build, so they are promoted to `'error'` in the config for intent-clarity (no
"green build with warnings" ambiguity).

19 lint problems surfaced; triaged on ground truth (decisions are CTO calls
under AGENTS.md rule 1):

Fixed at source (genuine, idiomatic fixes):
- `import/no-named-as-default` on `AxeBuilder` (×3: tests/a11y.spec.ts,
  tests/stats-a11y.spec.ts, tests/viz-a11y.spec.ts). `@axe-core/playwright`
  exports `AxeBuilder` as BOTH default and a same-named named export
  (`export { AxeBuilder, AxeBuilder as default }`); its own README uses the
  NAMED form. Switched the three default imports to
  `import { AxeBuilder } from '@axe-core/playwright'` — matches upstream docs,
  clears the rule. (E2E specs, not src/, but the gate is `eslint .` whole-repo.)

Fixed via scoped RULE CONFIG (deliberate, correct WAI-ARIA pattern — NOT a
silencing disable):
- `jsx-a11y/no-noninteractive-tabindex` (×4: SessionsList.tsx:67, Sources.tsx:70,
  LogsTab.tsx:67, EventList.tsx:51). All four are `tabIndex={0}` on a
  `role="region"` scroll container — the WAI-ARIA Authoring-Practices scrollable-
  region pattern (a keyboard-only user must be able to focus + arrow-scroll an
  overflow region). The rule's `roles` option exists for exactly this; added
  `'region'` (alongside the default `'tabpanel'`) to its allow-list. Rule stays
  fully active for every other element/role.

Scoped per-line disables (genuine rule false-positives for documented patterns;
each carries an inline rationale; none weakens a rule globally):
- Tabs.tsx:75 — `jsx-a11y/interactive-supports-focus`. WAI-ARIA tabs pattern
  (already implemented here) puts the ArrowLeft/Right/Home/End handler on the
  `role="tablist"` container (event delegation) while focus lives on the tab
  buttons via ROVING tabindex (selected=0, others=-1). The container is
  deliberately NOT a tab stop; the rule cannot model roving-tabindex delegation.
- SpanDetailDrawer.tsx:170 — `jsx-a11y/no-static-element-interactions` +
  `no-noninteractive-element-interactions`. The backdrop `onMouseDown` is a
  pointer-only "click-outside-to-dismiss" convenience; the dialog already has a
  COMPLETE keyboard path (Escape closes — line 117; Tab/Shift+Tab focus trap;
  a real close `<button>` — line 196).
- TimelineTab/TimelineRenderer.tsx:589 — `jsx-a11y/click-events-have-key-events`
  + `no-noninteractive-element-interactions`. The `<canvas>` wrapper click is a
  pointer-only pixel hit-test; the canvas's keyboard/SR path is the
  visually-hidden focusable `<button>` list (line 616, SOW-0006 AC#5). Clean
  false-positive. Tracked for Chunk D `viz/<chart>/a11y.md`.
- TraceTab/Waterfall.tsx:463 (`WaterfallCanvas`) — same two rules. NOTE: unlike
  the timeline, the Canvas-mode waterfall has NO keyboard fallback list, so this
  is a REAL keyboard-access gap in Canvas mode (used only above
  `SVG_SPAN_CEILING`; the default SVG renderer at line 148 IS fully keyboard
  accessible — `role="button"`/`tabIndex=0`/`onKeyDown` per span). The disable
  unblocks the gate but the gap is explicitly FLAGGED for Chunk D
  (`viz/<chart>/a11y.md` waiver + follow-up), not silently accepted.
- TopologyTab/TopologyTab.tsx:18 and Topology/Topology.tsx:20 — `import/default`
  on `import ForceWorker from '...forceWorker?worker'`. Vite's `?worker` suffix
  is a build-time virtual module whose default export (the Worker constructor)
  is SYNTHESIZED by Vite; eslint-plugin-import resolves the suffix-stripped
  `forceWorker.ts` (named exports only, no default) and so false-positives. The
  type side is already correct via `vite/client` ambient types. Per-line disable
  with rationale.

Config-file self-lint (eslint.config.ts is in the lint set): the import +
type-checked rules flagged the config's own untyped-plugin access
(`importPlugin.flatConfigs` is `any` — the plugin ships no `.d.ts`) and the
`tseslint.configs` named-vs-default heuristic. Resolved two ways: (1) an ambient
shim `frontend/src/types/eslint-plugin-jsx-a11y.d.ts` (`declare module
'eslint-plugin-jsx-a11y'`) gives jsx-a11y a type so `tsc --noEmit` resolves it —
the typecheck gate covers `eslint.config.ts` (`tsc --listFilesOnly` confirms it
is in the program) and exits 0; (2) a final `files: ['eslint.config.ts']`-scoped
block turns off the three `no-unsafe-*` + `import/no-named-as-default-member`
for the config file's own untyped-plugin / `tseslint.configs` access, with app
source keeping full coverage.

Known non-gating divergence (verified by the orchestrator): the editor TS
language service still reports TS7016 on the jsx-a11y import at
`eslint.config.ts:7` — it does not load the `src/types/` ambient shim that
`tsc -p tsconfig.json` resolves cleanly. The authoritative `tsc --noEmit` gate
(which CI runs and which covers `eslint.config.ts` + the shim) is GREEN; this is
an LSP-only artifact, in the same class as the gopls modernize hints. Follow-up
(see `## Followup`): make the shim LSP-resolvable (a `/// <reference>` in
`eslint.config.ts`, or a top-level `types/` dir) so the editor matches `tsc`.

Chunk-D handoff (viz a11y waivers): TimelineRenderer canvas (has fallback →
clean waiver) and WaterfallCanvas (NO fallback → waiver MUST record the real gap
+ a follow-up to add a focusable-span fallback list mirroring TimelineRenderer
line 616 / the SVG waterfall). FlameGraph canvas (FlameGraph.tsx:218) has the
SAME real gap as WaterfallCanvas BUT is NOT lint-flagged because its `onClick`
sits on the `<canvas>` element (jsx-a11y treats `<canvas>` as interactive/
embedded) rather than a `<div>` wrapper — so the lint gate is a BLIND SPOT for
canvas-mode keyboard access. A whole-codebase sweep confirmed exactly four
flagged canvas/static-interaction sites (the four disables) and these unflagged
canvas-on-`<canvas>` sites; Chunk D must audit FlameGraph + both canvas modes
for the keyboard-fallback gap, not just the lint-flagged lines.

### 2026-06-03 — Chunk C: per-directory Vitest coverage (delegated)

- Mechanism: **native** Vitest 4.1.7 glob-keyed `coverage.thresholds` (no wrapper
  script) — verified in the installed source + empirically (a 95% probe on the
  86.76% `SpanDetailDrawer` dir exits 1 with `ERROR: Coverage for lines …`).
  `vitest.config.ts` adds `PER_DIR_LINES=80` + `PER_DIR_GLOBS` (13 measured dirs)
  spread into `thresholds`; the global aggregate floor is unchanged.
- **Vacuous-pass guardrail:** an empty glob group's lines pct is `"Unknown"` and
  `"Unknown" < 80` is false → a glob matching zero files vacuously PASSES.
  Per-dir keys are therefore kept in lockstep with `coverage.include` (measured
  dirs only); stub/placeholder dirs are in neither. Verified: each of the 13
  dirs has 2 refs (glob + include); the live run reports real % for each (not
  `Unknown`).
- Real finding (fixed, not threshold-lowering): `src/pages/Topology/` had a test
  (96.7%) but was MISSING from `coverage.include` → unmeasured; added to both
  `include` and the per-dir gate. All 13 dirs already ≥ 80% lines (no test gaps).
- New `frontend/scripts/check-coverage-thresholds.test.sh` — hermetic self-test
  (throwaway ~50%-lines fixture) proving the native gate fails-closed (exit 1 +
  names the dir) under-floor and passes above; wired as a CI step + a
  `check:coverage-thresholds:selftest` npm script (mirrors the Chunk-A bundle
  self-test). `frontend/.gitignore` guards the fixture dir. Specs + skills synced.
- Orchestrator verification (run myself): self-test 2/2; `npm run test --
  --run --coverage` exit 0 with per-dir active (623 tests, dirs non-vacuous);
  `lint --max-warnings=0` + `typecheck` green; Chunk-A bundle self-test 8/8;
  `actionlint` clean; secret + AI-attribution scans clean.

### 2026-06-03 — Chunk D: Playwright scenario completeness + axe a11y + viz waivers (delegated)

- AC#4 scenarios — 3 already existed (`deep-link.spec.ts` = session-detail load,
  `routes.spec.ts` = sources panel, `theme.spec.ts` = theme toggle covering BOTH
  OS `prefers-color-scheme` AND manual localStorage override). 2 were ADDED:
  - `tests/sessions-filter.spec.ts` — drives the FilterBar agents input; asserts
    the list narrows (URL carries `?agents=`, every Agent cell matches), a
    non-matching term collapses to the empty state, Clear restores the full list.
    Agent name is runtime-derived from `/api/sessions` (not hard-coded).
  - `tests/sse-update.spec.ts` — DETERMINISTIC (no timing-luck). The product is
    read-only with no writer, so a fake `EventSource` is installed via
    `addInitScript` BEFORE app scripts, captures the `/api/events` instance, and
    the test dispatches a controlled `session_changed` frame. The real
    `SseConnection` listener runs the documented `['sessions']` invalidation → a
    fresh `GET /api/sessions`; the test asserts that second GET fires (request
    counter) + zero pageerrors. `realtime`/`viz-sse` still prove the real stream
    opens at the network level.
- AC#5 axe — added `npm run e2e:a11y` (the 3 axe specs). Closed a REAL gap: the
  `/sessions/:id` **Logs** tab had NO axe scan; added one (both themes). Axe now
  covers every route (/, /sessions/:id overview+logs+trace+timeline+topology,
  /sources, /stats, /topology). Zero serious/critical.
- Playwright tuning — global `retries: 0` (was `CI?2:0`; blanket retries mask
  real flakiness) + `timeout: 15_000`; SSE flows opt into `retries: 1` + `30_000`
  via `test.describe.configure` (scoped to sse-update/realtime/viz-sse only — the
  EventSource open is the one legitimately-slow checkpoint). Two projects:
  `chromium` (gating, `testIgnore: **/quarantine/**`) + `quarantine` (own dir).
  `npm run e2e`/`e2e:a11y` name `--project=chromium` so a bare run never folds
  quarantine into the gate. Quarantine empty on delivery (`.gitkeep` + README);
  no `test.skip` anywhere.
- viz a11y waivers — `src/viz/{waterfall,flamegraph,timeline,topology}/a11y.md`:
  - waterfall + flamegraph DOCUMENT the real Canvas-mode keyboard gap (above
    `SVG_SPAN_CEILING`=400 the SVG→Canvas switch drops the focusable-span path;
    flamegraph also notes the lint gate is blind to it — `onClick` is on the
    `<canvas>` element itself). The FIX is the `## Followup` item, not this chunk.
  - timeline + topology: NO gap (Canvas mode has a visually-hidden focusable
    `<button>`/`<ul>` fallback list) — documented as clean false-positives.
  - Policy documented: per-selector `AxeBuilder.exclude()` only, never a global
    `disableRules`. No exclusions are needed today (all routes are axe-clean).
- Spec sync — `quality-gates.md` (Frontend E2E + Accessibility rows) +
  `project-frontend` skill (deterministic-SSE pattern, retry/quarantine rules,
  e2e:a11y, viz-a11y-waiver convention).
- Orchestrator verification (run myself): read the new specs (sse-update
  determinism confirmed — drives the client seam + asserts the network refetch,
  no sleeps) + the config/waiver diffs; `bash scripts/build.sh` BUILD_OK;
  `npm run e2e` **43 passed**; `npm run e2e:a11y` **21 passed** (zero
  serious/critical, both themes, incl. the new Logs-tab scan);
  `npm run e2e:quarantine` boots/seeds/tears-down clean (no tests);
  `lint --max-warnings=0` + `typecheck` green; per-dir coverage gate (Chunk C)
  exit 0; Chunk-A bundle self-test 8/8; `actionlint` clean; secret +
  AI-attribution scans clean (794 tracked files).

### 2026-06-03 — Chunk E: scripts/lint.sh frontend static-analysis section (delegated)

- Extended `scripts/lint.sh` with a build-free, fail-fast frontend section after
  the Go section. CTO decision (made up-front, not punted): lint.sh is the
  build-free static-analysis entrypoint, so the frontend section runs analysis +
  gate-LOGIC self-tests only, never a build. Order: presence-skip (no
  `frontend/package.json` → skip clean) → deps only if `node_modules` missing
  (reuses build.sh's `npm ci`/`npm install` fallback) → `npm run lint --
  --max-warnings 0` → `npm run typecheck` → bundle-size self-test → per-dir
  coverage self-test. Runs in a `( … )` subshell so the parent's `set -e` aborts
  on any failure (fail-fast).
- The REAL bundle-size-vs-built-manifest gate (`npm run check:bundle-size`) and
  the REAL coverage run already run in CI's `frontend` job (`ci.yml` ~404 / ~509)
  + after the build; NOT duplicated into lint.sh. `build.sh` left untouched
  (verified the real gate is already deterministically wired in CI).
- Mirrors the existing `run()`/`set -euo pipefail`/color convention; the Go
  section + `run()` helper are unchanged (the only Go-region edit is the final
  `[ok]` echo reworded). The pre-existing SC2059 info-note on the `run()` printf
  is not introduced here.
- Spec sync: `quality-gates.md` (lint.sh paragraph → Go + frontend, build-free vs
  real-gate distinction; Frontend Lint/Type-Check local-runner notes),
  `project-quality-gates` skill (aggregate-scripts block), `project-frontend`
  skill (local aggregate-runner pointer), `AGENTS.md` "Build, Test, Run" one-liner.
- Orchestrator verification (run myself): reviewed the full lint.sh diff
  (build-free, fail-fast, Go section/run() untouched) + AGENTS.md (accurate, no
  overclaim that lint.sh runs the real gates); ran `bash scripts/lint.sh`
  end-to-end → **EXIT=0** (golangci-lint + gosec + govulncheck clean; frontend
  eslint + tsc + bundle self-test 8/8 + coverage self-test 2/2 clean). Fail-fast
  confirmed by the inject-a-failure-then-revert proof (downstream steps did not
  run; the script aborted non-zero).

### 2026-06-03 — Chunk F: external-review findings addressed (F1–F10, delegated)

Ten verified external-review findings on the Chunk A–E surface, fixed
spec→test→code (gate fixes extended the gate's self-test FIRST):

- **F1 [P1] bundle-size gate fail-open on transitive static imports**
  (`frontend/scripts/check-bundle-size.js`). The gate budgeted only each entry's
  own `file`; a Rollup-split SHARED chunk (neither `isEntry` nor
  `isDynamicEntry`, reachable via an entry's `imports[]`) was swept into
  "ungated", so a tiny lazy route statically importing a huge shared chunk
  PASSED. Fix: budget each MAIN/LAZY entry against the gz sum of its file PLUS the
  transitive closure of its static `imports` (recursive, de-duped within one
  entry's closure; `dynamicImports` NOT followed). A file pulled into a closure is
  no longer re-listed as ungated. New self-test cases (`check-bundle-size.test.sh`):
  closure-over-budget FAILs (50 KB route + 230 KB static import > 200), diamond
  shared chunk de-dups to one count (PASS), and `dynamicImports` are not folded
  into the main budget (PASS). All three FAIL against the old gate (verified).
- **F2 [P2] gate passed with no main entry** (same file). The zero-chunks guard
  was `main==0 && lazy==0`; a manifest with lazy chunks but no `isEntry` (broken
  SPA build) passed. Fix: require `mainChunks.length >= 1`, else fail-closed
  (exit 2). New self-test case: lazy-only manifest → exit 2.
- **F10 [P3] non-object-manifest case unpinned** (same file). A `[]` manifest is
  caught by the existing `Array.isArray` guard but had no test; added a case
  asserting exit 2.
- **F3 [P2] coverage self-test did not validate the REAL config.** Added a new
  real-config verifier `frontend/scripts/check-coverage-config.mjs` (+ `npm run
  check:coverage-config`). The two dir lists (`COVERAGE_INCLUDE`, `PER_DIR_GLOBS`,
  `PER_DIR_LINES`) were extracted into a shared `frontend/vitest.coverage.mjs`
  (typed via `vitest.coverage.d.mts`) that BOTH `vitest.config.ts` AND the
  verifier import — they cannot diverge. The verifier fails closed (exit 1,
  naming the offender) if (a) any per-dir glob matches ZERO files on disk (the
  vacuous-`"Unknown"`-pass trap) or (b) any measured component/page dir lacks a
  per-dir floor. Wired into `scripts/lint.sh`'s frontend section + a dedicated CI
  `frontend` step. The throwaway self-test is retained and re-described as proving
  the MECHANISM; specs/skills corrected (it does not read the real config).
- **F4 [P2] axe did not cover every route.** Added axe scans (both themes) for
  `/tools`, `/models`, `/agents` (ComingSoon stubs) and `/no-such-route`
  (NotFound) to `tests/a11y.spec.ts`. After this, every App.tsx route is
  axe-covered. **The NotFound scan surfaced a REAL serious violation**
  (`link-in-text-block`, both themes): the "Back to sessions" link sits in muted
  `--text-secondary` text with global `text-decoration: none`, so it is
  distinguished only by color at 1.17:1 contrast (< 3:1). Fixed at source
  (`NotFound.module.css`: persist an underline on the in-note link). e2e:a11y now
  29/29 (was 27 pass + 2 fail).
- **F5 [P2] docs claimed build.sh/test.sh run the real gates; they didn't.** Made
  the claim true: `scripts/build.sh` now runs `npm run check:bundle-size` on the
  just-built `dist/`; `scripts/test.sh` (normal mode) now runs the frontend
  `npm run test -- --run --coverage` after the Go suite (reusing build.sh's
  deps-if-missing pattern; the `--stress` branch stays Go-only). Specs/AGENTS/
  skills updated to the now-true wiring.
- **F6 [P2] missing `engines`.** Added `"engines": { "node": ">=20.11.0" }` to
  `frontend/package.json` (`eslint.config.ts` uses `import.meta.dirname`).
- **F7 [P3] quarantine README overclaimed "still run".** Reworded: quarantined
  specs are a manual/diagnostic `npm run e2e:quarantine` (non-gating); CI runs
  only the gating `e2e` today; a `continue-on-error: true` CI step is to be added
  when the dir is first populated. No false "runs in CI" claim.
- **F8 [P3] lint.sh redundant `--max-warnings 0`.** The package.json `lint` script
  already bakes it in; lint.sh now calls bare `npm run lint` (single source of
  truth). Unified the spelling to `--max-warnings=0` across specs/skills.
- **F9 [P3] SSE-test brittleness note.** Added a one-line comment near the
  `addInitScript` call in `tests/sse-update.spec.ts` warning that hoisting/
  capturing `EventSource` at module top-level would bypass the fake (no code
  change).

ESLint friction from the new `.mjs`/`.d.mts` root files (the type-aware rule set
has no parserOptions for non-`.{ts,tsx}` files): resolved by adding
`vitest.coverage.mjs` + `**/*.d.mts` to `eslint.config.ts`'s `globalIgnores`
(same class as the already-ignored `scripts/` Node tooling — config-data, no app
logic, type-checked via its `vitest.config.ts` import). `tsconfig.json` left
pristine.

AC reconciliation (no AC text changed, per the standing rule): the verifier and
shared-list files are `.mjs`/`.d.mts` (not `.js`), in the same documented
`.js`→`.ts(/.mjs)` tooling-extension class already reconciled at line ~195 for
`eslint.config.ts`; recorded here, not by editing AC#1/#3.

Orchestrator finalization: pending (this entry written by the implementer for the
orchestrator to review + fold into `## Validation`/`## Reviews`).

### 2026-06-03 — Chunk F round-2 fixes (R3-1..R3-5, delegated)

Five verified round-2 external-review findings on the Chunk F surface, fixed
spec→test→code (gate fixes extended the gate's self-test FIRST):

- **R3-1 [P2] coverage-config verifier fail-open on a broad include glob, and it
  was not self-tested** (`frontend/scripts/check-coverage-config.mjs`). The old
  `includeEntryToDir` returned `null` for an include entry under a per-dir root
  whose first segment was `*`/`**` (e.g. a future `src/pages/**/*.{ts,tsx}`),
  silently ignoring it — so the lockstep check derived ZERO dirs from it while
  Vitest would MEASURE the page files, a missing-floor hole undetectable by the
  verifier. Fixes: (i) the check logic is now the EXPORTED PURE function
  `checkCoverageConfig({ include, perDirGlobs, frontendDir, perDirRoots })` →
  `string[]` (the CLI block imports the REAL lists from `vitest.coverage.mjs`,
  calls it with the real frontend dir, prints errors, exits 1 if any — real-config
  behavior unchanged, still PASS: 13 floors / 13 measured dirs). (ii) FAIL CLOSED
  on the unsupported broad shape — an include entry under `src/components/` or
  `src/pages/` with a `*`/`**` first segment now returns a named error (replace
  with explicit per-dir entries, or extend the verifier) instead of being ignored.
  (iii) `hasSourceFile` no longer counts `*.d.ts` as source (`&& !n.endsWith('.d.ts')`),
  so a dir whose only `.ts` is a declaration is correctly flagged vacuous. (iv) New
  hermetic self-test `frontend/scripts/check-coverage-config.test.sh` (+ `npm run
  check:coverage-config:selftest`) drives the exported function against a throwaway
  fixture tree under `frontend/` and asserts errors for vacuity, lockstep, the broad
  `src/pages/**` shape, and a `.d.ts`-only dir (and a `*.test.ts`-only dir), and NO
  errors for a clean config — 6/6 assertions pass. Wired into `scripts/lint.sh`
  (step 8b) + a dedicated CI `frontend` step ("Self-test coverage-config verifier").
- **R3-2 [P2] bundle-size spec/skill described the OLD pre-F1 model.** Both
  `.agents/sow/specs/quality-gates.md` (§Frontend — Bundle Size) and
  `.agents/skills/project-quality-gates/SKILL.md` said shared chunks are "reported
  but not gated". Corrected to the F1 closure model: a MAIN/LAZY entry's budget is
  the gz sum of its `.file` PLUS the transitive closure of its static `imports`
  (deduped within one closure; `dynamicImports` not followed); only JS neither
  classified nor inside any gated closure (e.g. a `?worker` bundle absent from the
  manifest) is "ungated". Durable-memory wording made precise so a future cleanup
  cannot reintroduce the fail-open.
- **R3-3 [P3] a11y doc route-list + CI comment drift.**
  `.agents/sow/specs/quality-gates.md` (§Frontend — Accessibility) now lists every
  `App.tsx` route axe-scans (added `/tools`, `/models`, `/agents`, and the NotFound
  catch-all on an unknown path), states "every route declared in `App.tsx` is
  axe-covered", and notes `tests/a11y.spec.ts` carries the stub + NotFound scans.
  `.github/workflows/ci.yml` SOW-0012 deferred-note updated: bundle-size gate +
  axe-every-route + the coverage-config verifier are LANDED (was "axe … remaining");
  the SOW-0013 line is unchanged.
- **R3-4 [P3 / AC#2] `tsconfig.json` missing `noImplicitOverride`.** Added
  `"noImplicitOverride": true` to `frontend/tsconfig.json` compilerOptions (SOW-0012
  AC#2 requires it). `npm run typecheck` exits 0 — no subclass-override violation
  surfaced (the frontend has no class-override source), so the flag is added without
  a source change.
- **R3-5 [P3] staticClosure missing-import-key fail-closed path unpinned.** Added
  self-test case (d6) to `frontend/scripts/check-bundle-size.test.sh`: a manifest
  whose isEntry entry `imports: ["missing-key"]` (no such manifest entry) → gate
  exits 2 (the closure walker's broken-import-graph guard). Self-test now 14/14.

Orchestrator finalization: pending (this entry written by the implementer for the
orchestrator to review + fold into `## Validation`/`## Reviews`).

### 2026-06-03 — Chunk F round-3 fixes (R4-1..R4-4, delegated)

Four verified round-3 external-review findings on the Chunk F surface, fixed
spec→test→code (gate fixes extended the gate's self-test FIRST):

- **R4-1 [P2] coverage gate could pass while shipped source was completely
  unmeasured** (disk-completeness). Source dirs/flat-files under
  `src/components/`/`src/pages/` in NEITHER `COVERAGE_INCLUDE` nor a per-dir floor
  silently escaped ALL coverage AND the verifier (Layout, StatCard, the
  Agents/Models/Tools stubs, NotFound.tsx, and the flat ComingSoon.tsx). Fixes:
  (a) `vitest.coverage.mjs` now MEASURES the tested flat file
  `src/components/ComingSoon.tsx` (it has `ComingSoon.test.tsx`; 100% covered, real
  coverage stays green); flat files carry no per-dir floor by design.
  (b) New explicit `export const COVERAGE_EXCLUDED` ledger lists every intentionally
  unmeasured source dir/flat-file with an honest rationale — `Layout` + `StatCard`
  as REAL components with Vitest-unit coverage DEFERRED to a tracked follow-up (the
  orchestrator will file the pending SOW; Playwright exercises them today), the
  `Agents`/`Models`/`Tools` Phase-3 `<ComingSoon/>` wrappers, and the trivial
  `NotFound.tsx` 404. (c) The verifier (`check-coverage-config.mjs`, the exported
  pure fn) gained a DISK-COMPLETENESS check: it enumerates every immediate source
  dir and every flat `.ts`/`.tsx` file under the two roots and FAILS CLOSED (named
  error: "<path> exists on disk but is in neither COVERAGE_INCLUDE nor
  COVERAGE_EXCLUDED …") unless each is measured or excluded; the fn now also imports
  `COVERAGE_EXCLUDED`. (d) Self-test extended (6→12 cases): a source dir in neither
  list → named error; a flat source file in neither → error; a dir/file in
  COVERAGE_EXCLUDED → no error; the clean case still 0 errors. (e) Doc OVERCLAIM
  fixed in `project-quality-gates`/`project-frontend` skills + `quality-gates.md`:
  the verifier catches THREE classes (non-vacuity, lockstep-missing-floor,
  disk-completeness), and Layout/StatCard are described as real components with
  Vitest-unit coverage deferred — not placeholders.
- **R4-2 [P3] broad-glob rejection not normalized** (`check-coverage-config.mjs`).
  COVERAGE_INCLUDE / COVERAGE_EXCLUDED entries are now NORMALIZED before
  classification (strip a leading `./`, collapse repeated `/`, reject any `.`/`..`
  path segment → named malformed-entry error), and the first-segment broad-shape
  rejection is now metachar-based (`* ? [ ] { }`), not only the exact `*`/`**`
  strings. New self-test cases: `./src/pages/**/*.{ts,tsx}` and `src/pages/*o/**`
  are both caught as broad/unsupported; a `..`-segment entry is caught as malformed.
- **R4-3 [Low→fix, minimax] check-bundle-size.js silently dropped non-string
  imports.** `staticClosure`'s `imports` loop previously skipped a non-string
  element via `if (typeof imp === 'string')`. Vite always emits string keys, so a
  non-string element is a ManifestChunk-contract violation — now FAIL CLOSED (fatal,
  exit 2) rather than silently undercount the closure. New self-test case (d7): an
  entry whose `imports` holds a number → exit 2. Self-test now 15/15.
- **R4-4 [P3, minimax] .gitignore the new self-test temp dir + max-warnings
  spelling.** Added `.coverage-config-selftest.*` to `frontend/.gitignore` (mirrors
  the `.coverage-selftest.*` guard; confirmed it matches the test's `mktemp`
  template). Unified the `--max-warnings` spelling: the `frontend/package.json`
  `lint` script now reads `--max-warnings=0` (matching every doc/skill/spec
  reference, which already used the `=0` form).

Orchestrator finalization: pending (this entry written by the implementer for the
orchestrator to review + fold into `## Validation`/`## Reviews`).

### 2026-06-03 — Chunk F round-4 fixes (R5-1..R5-3, delegated)

Three verified round-4 external-review findings on the Chunk F surface, fixed
spec→test→code (gate fixes extended the gate's self-test FIRST):

- **R5-1 [P2] coverage-config verifier could be fooled by a NARROW per-dir include
  (sibling escapes)** (`frontend/scripts/check-coverage-config.mjs`). The verifier
  derived the measured dir from the FIRST SEGMENT of a COVERAGE_INCLUDE entry, then
  treated the WHOLE dir as accounted-for. A hypothetical narrow include like
  `src/components/Foo/Foo.tsx` (names a specific file, not the whole dir) would mark
  `src/components/Foo` measured while a sibling `src/components/Foo/helper.ts`
  escaped BOTH Vitest's instrumentation (the narrow glob matches only `Foo.tsx`)
  AND the disk-completeness check. No current instance (every per-dir include is the
  whole-dir `<root>/<Dir>/**/*.{ts,tsx}` shape), but the verifier now ENFORCES it.
  Fix: in the per-dir classification, for a normalized entry under a per-dir root
  whose first segment names a literal dir, if the entry has MORE segments after
  `<Dir>` the one immediately after it MUST be `**` (the whole-dir glob); anything
  else (a filename or subpath) pushes a NAMED fail-closed error ("requires a
  whole-directory include shape `<root>/<Dir>/**/*.{ts,tsx}` so no sibling source
  escapes measurement"). Preserves the existing working cases: a flat file directly
  under a root (`src/components/ComingSoon.tsx`), whole-dir `<Dir>/**/...`, and
  entries not under a per-dir root (src/state, src/lib, src/api, src/viz) are
  unaffected. The CLI count block mirrors the same skip so its measured-dir count
  stays equal to the function's `measuredDirs`. New self-test case (k):
  `src/components/Foo/Foo.tsx` (+ a `Foo` dir on disk with `Foo.tsx` and a sibling
  `helper.ts`) → named narrow-shape error; all existing cases stay green (13/13).
  REAL config still PASSES unchanged (13 floors / 13 measured dirs / 6 excluded).
- **R5-2 [P3] bundle gate silently ignored a non-array `imports`/`dynamicImports`**
  (`frontend/scripts/check-bundle-size.js`). The closure walker validated
  `imports[]` ELEMENTS only when `imports` was already an array; a present-but-non-
  array `imports` (e.g. `imports: "_shared.js"`) was silently ignored → an
  undercount that could vacuously pass the budget. The gate's policy is fail-closed
  on manifest-contract violations, so a new up-front pass over EVERY chunk now exits
  2 (named) when `imports` or `dynamicImports` is present but not an array. New
  self-test cases (d8) `imports: "_shared.js"` → exit 2 and (d9)
  `dynamicImports: "src/Route.tsx"` → exit 2; self-test now 17/17. (Per-element
  non-string `imports` validation stays in the closure walker, where keys resolve;
  per the finding, no per-target dynamicImport existence validation was added this
  round.)
- **R5-3 [P3] durable docs/comments still described older gate behavior.** Swept +
  fixed (durable memory; a stale model invites regression): (i) `project-frontend`
  skill (§Bundle Size) now documents the manifest-driven transitive static-import
  CLOSURE model (entry budget = file + its static-imports closure; de-dup within a
  closure; `dynamicImports` not followed; shared chunks gated under the importing
  entry) + the fail-closed contract-violation list. (ii) `scripts/lint.sh` (step
  8/8b comments) + `.github/workflows/ci.yml` coverage-config step name/comment now
  say DISK-COMPLETENESS + whole-dir include shape, not just "non-vacuity +
  lockstep". (iii) The CI Lint step drops the redundant `-- --max-warnings=0` (the
  `lint` npm script bakes it in) so CI matches `scripts/lint.sh`'s single source of
  truth; the stale "CI runs `npm run lint -- --max-warnings=0`" wording in
  `project-frontend` §Lint is corrected. (iv) `quality-gates.md` §Frontend — Lint
  canonical invocation is now `npm run lint` (the script owns the flag); the same
  drift class fixed in `project-quality-gates` + `project-delegation` skills.

AC#6 reconciliation (no AC text changed, per the standing rule): AC#6's planning
wording says the bundle gate "reads `dist/assets/*.js`". The delivered gate is
MANIFEST-CLOSURE-driven (classifies chunks from `dist/.vite/manifest.json`'s
`ManifestChunk` flags and budgets each entry's transitive static-import closure),
taking a dist-DIR arg — the dist-glob→manifest divergence was already reconciled in
the Chunk A open entry above (2026-06-03, "the gate takes a dist-DIR arg … rather
than the old `dist/assets/*.js` glob"); recorded here, not by editing AC#6.

Orchestrator finalization: pending (this entry written by the implementer for the
orchestrator to review + fold into `## Validation`/`## Reviews`).

## Validation

(Filled at SOW close. Each acceptance criterion gets evidence: command + output summary, CI run URL, reviewer finding summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- **eslint.config.ts shim LSP-resolvability** (Chunk B): the jsx-a11y ambient
  shim in `frontend/src/types/` is resolved by `tsc -p tsconfig.json` (the
  typecheck gate is green) but the editor TS language service still shows TS7016
  on the import. Non-gating (LSP-only). Make it editor-resolvable too — a
  `/// <reference>` in `eslint.config.ts` or a top-level `types/` dir — so the
  editor matches `tsc`.
- **Canvas-mode keyboard-access gap** (found in Chunk B, pre-existing): the
  `WaterfallCanvas` (`Waterfall.tsx`, above `SVG_SPAN_CEILING`) and `FlameGraph`
  (`FlameGraph.tsx`) canvas renderers have no keyboard/SR span-selection path —
  unlike the SVG renderers and `TimelineRenderer` (which expose a visually-hidden
  focusable `<button>` list). The lint gate is blind to the FlameGraph case (its
  `onClick` sits on `<canvas>`, which jsx-a11y treats as interactive). Chunk D
  documents the waivers in `src/viz/<chart>/a11y.md`; the actual FIX (add a
  focusable-span fallback list to both canvas renderers, mirroring
  `TimelineRenderer`) is tracked here as a follow-up beyond the waiver.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
