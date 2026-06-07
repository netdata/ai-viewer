# Quality Gates

## Purpose

The authoritative catalog of every automated gate enforced in ai-viewer's CI and local pre-commit. CI runs every gate as dedicated parallel jobs or cross-cutting steps (except the **benchmark regression gate**, which is local/workstation-only — its baseline is not comparable to CI-runner hardware; CI runs the bench compile-smoke + the gate self-test, see §Go — Benchmarks); the assistant runs the full local workstation aggregate (`./scripts/gates.sh`) before any commit. A gate failure is a defect, not a stylistic suggestion: fix the root cause, never weaken the gate.

The runtime companion to this spec is `.agents/skills/project-quality-gates/SKILL.md` (commands and ergonomics). This spec is the durable truth about *what* is enforced and *at what threshold*.

## Operating Rules

- Every gate listed here runs locally before any commit and in CI on every push — except the benchmark regression gate (`scripts/check-bench.sh`), which is local/workstation-only (its baseline is not comparable to CI-runner hardware; CI runs the bench compile-smoke + the gate self-test).
- Weakening a gate to land a PR is a contract breach. The remedy is fixing the root cause or splitting the PR.
- Adding a gate requires updating this spec, the skill, and CI in the same commit.
- Removing a gate requires an operator-approved SOW with stated replacement coverage.
- Local pass + CI fail divergence is investigated as a defect (typically environmental); never papered over by retrying CI.

## Gate Catalog

### Go — Module Tidiness

- `go mod tidy` followed by `git diff --exit-code go.mod go.sum` → zero
  module-file diffs.
- `scripts/lint.sh` and the CI `lint` job both run this before
  formatter/linter/security checks, so stale `go.mod`/`go.sum` cannot pass
  locally and fail only in CI.

### Go — Format

- `gofmt -l` over the tracked Go file list (`git ls-files -z -- '*.go'`) →
  zero unformatted tracked files.
- `goimports -l` over the same tracked Go file list → zero unformatted tracked
  files.
- Threshold: zero diffs.
- `scripts/lint.sh` mirrors CI's explicit standalone `gofmt` and
  `goimports@latest` checks before `golangci-lint`; both surfaces use tracked
  Go files only, so ignored/untracked local files such as
  `frontend/node_modules/**` cannot create local-only formatter failures. An
  empty tracked Go file list is fail-closed. The golangci formatters are defense
  in depth, not the only local formatter surface.
- `scripts/test/lint-test.sh` is the hermetic gate self-test for this contract:
  it plants an ignored/untracked malformed Go file in a temporary git repo and
  proves the formatter input set still comes only from
  `git ls-files -z -- '*.go'`. A regression back to `gofmt -l .` /
  `goimports -l .` must fail the self-test.

### Go — Vet

- `go vet ./...` → zero warnings.

### Go — Lint

- `golangci-lint run --timeout=5m` is the umbrella gate: with the formatters enabled it also enforces Go — Format, and the `govet` linter covers Go — Vet, so this single command is the authoritative lint surface. `scripts/lint.sh` runs it after the module-tidiness, standalone `gofmt`, standalone `goimports@latest`, and `go vet` checks that mirror the CI `lint` job; CI runs it via the version-pinned `golangci/golangci-lint-action`.
- golangci-lint is **v2**; `.golangci.yml` declares `version: "2"`. `gosimple` is NOT enabled — golangci v2 merged it into `staticcheck`. `gosec` is NOT a golangci linter here — it runs standalone (Go — Security) to avoid duplicate analysis.
- `.golangci.yml` enables linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `errorlint`, `gocritic`, `revive`, `gocyclo`, `misspell`, `nilerr`, `prealloc`, `unconvert`, `unparam`, `whitespace`, `bodyclose`, `noctx`; formatters: `gofmt`, `goimports`, `gofumpt`.
- `gocyclo` uses `min-complexity: 25` (not 15): the stream parsers/scanners/tailers and the SOW-0007-hardened ingest event loop sit legitimately at 16–24 cyclomatic, so complexity-15 produced ~23 false-positive-class findings on heavily-reviewed, hot-path code. 25 flags genuinely-egregious complexity (the one production outlier, `pricing.validateDoc`, was refactored under it) while preventing future creep.
- `_test.go` is excluded from the style/complexity linters (`gocyclo`, `noctx`, `unparam`, `prealloc`, `revive`, `gocritic`): table-driven tests are intentionally branchy, test setup uses context-less DB calls, and test helpers carry call-site-specific params. The bug-finders (`errcheck`, `staticcheck`, `govet`, `ineffassign`, `unused`, `nilerr`, `errorlint`, `bodyclose`) stay active on tests.
- `frontend/node_modules` is path-excluded (a transitive npm dependency ships a Go reference file that is not project code).
- Version is pinned in `.golangci-lint-version` (single source for CI +
  `scripts/lint.sh`). CI installs that exact pin through
  `golangci/golangci-lint-action`; local `scripts/lint.sh` fails when the
  installed `golangci-lint` binary cannot be parsed or differs from the pin.
- Threshold: zero warnings.

### Go — Security

- `gosec -severity medium -confidence medium ./...` → zero high/critical findings.
- `govulncheck ./...` → zero known vulnerabilities. Runs per push and on a nightly schedule for newly-disclosed CVEs.

### Go — Tests

- `go test -race -count=1 ./...` → all pass.
- `-count=1` defeats the test cache so CI always runs fresh.
- The per-push CI `test` job runs `go test -race -count=1 -timeout=25m ...` and
  sets the job's `timeout-minutes: 30` (SOW-0038). The default 10m/package was
  too tight for the slow `internal/ingest` rollup test under `-race` on a CI
  runner (effectively single-threaded there). **Invariant:** keep the job
  `timeout-minutes` strictly **above** the Go `-timeout`, so the Go per-package
  timeout fires first (printing a stack trace) and the GitHub job-kill is only a
  backstop. (`scripts/test.sh` does not yet pass `-timeout`; tracked as a
  SOW-0038 Followup.)

### Go — Coverage

- `scripts/test.sh` → `go test -race -coverprofile=coverage.out -covermode=atomic -count=1 ./...`.
- **Metric: statement coverage** (Go's `-covermode=atomic`). Go has **no first-class branch coverage**; the branch threshold is **deferred** (revisit only if a mature branch-coverage tool emerges). Statement coverage is the enforced metric.
- Thresholds (statement), enforced by `scripts/check-coverage.sh`:
  - **Gated set = every `internal/*` package** (the unit-testable core): a package is gated **iff its import path contains `/internal/` and not `/cmd/`**; each ≥ 80%, and their aggregate ≥ 80%.
  - **`/cmd/` is excluded** from the gate: the binaries (`cmd/ai-viewer-{ingest,serve}` — `main()`/flag/signal wiring, covered by Playwright E2E + embed-smoke + cmd binary tests) and the dev-only tools (`internal/adapters/aiagent_v2/cmd/{genfixtures,backfillbench}`). Reported for visibility, not gated.
  - **Non-`internal/` paths are also excluded**: e.g. vendored Go that a frontend npm dependency ships under `frontend/node_modules/` (`flatted/golang/...`). `go test ./...` compiles+covers it (there is no `go.mod` under `frontend/`), but it is not our code, so the `/internal/`-positive predicate keeps it out of the gate.
  - **New-code-in-PR ≥ 90%: deferred to a follow-up SOW** (needs a diff↔coverage intersector + self-tests); the per-package + aggregate gate is the shipped base.
- Enforcement: CI runs `scripts/check-coverage.sh coverage.out` as a build-failing step in the `test` job (after the coverage artifact uploads); the same script is the local pre-commit gate. `check-coverage.sh` has synthetic-fixture self-tests (`scripts/test/check-coverage-test.sh`).

### Go — Fuzzing

- Every adapter parser exposes at least one `FuzzXxx` target (10 targets across the 5 adapter packages). `internal/canonical` has **no** fuzz target — it owns no parsers/decoders (pure event types; all untrusted-bytes parsing lives in adapters).
- **Per-push (deterministic, PR-blocking)**: `go test -run='^Fuzz' ./internal/adapters/...` runs every target's seed corpus (`f.Add` inputs + any committed `testdata/fuzz/` reproducers) as normal subtests. No `-fuzz` exploration runs per push — it is non-deterministic (a crash found in unchanged code would block an unrelated PR, and pass/fail would flip run-to-run).
- **Nightly (exploration, non-blocking)**: `fuzz-nightly.yml` runs `go test -fuzz=<Target> -fuzztime=5m` per target (matrix, one target per package per invocation). A crash fails that target's job and uploads the reproducer as an artifact; commit it under `testdata/fuzz/<Target>/` so the per-push gate reproduces it deterministically until fixed. Auto-filing a GitHub issue on crash is a deferred follow-up (job-failure visibility + the artifact suffice).
- Threshold: zero crashes per run.

### Go — Property-Based Tests

- `internal/canonical/property_test.go` uses property-based assertions for canonical mapping invariants (idempotency, ordering, structural equality after round-trip).
- Threshold: all properties hold.

### Go — Benchmarks

- Marked benchmarks for the 11 performance-critical paths: ai-agent v2 adapter `Scan` + adapter `Tail` (`internal/adapters/aiagent_v2`), claude-code adapter `Scan` + adapter `Tail` (`internal/adapters/claude_code`), Codex adapter `Scan` + adapter `Tail` (`internal/adapters/codex`), Opencode adapter `Scan` + adapter `Tail` (`internal/adapters/opencode`), SQLite batch insert (`internal/ingest` `worker.flush`), REST query path (`internal/presenter` `handleSessionsList`), SSE fanout (`internal/notify` `Hub.Deliver`). There is **no canonical encode/decode** benchmark — canonical events are constructed directly by adapters and never serialized (`internal/canonical` has no encoders/decoders).
- `scripts/check-bench.sh` runs `go test -run=^$ -bench=. -benchmem -count=6 -cpu=1` over the 7 benchmark packages (11 benchmarks; adapter `Scan` + `Tail` share `aiagent_v2`, claude-code adapter `Scan` + `Tail` share `claude_code`, Codex adapter `Scan` + `Tail` share `codex`, and Opencode adapter `Scan` + `Tail` share `opencode`) and compares to `bench/baseline.txt` via `benchstat` (`-count=6` is benchstat's minimum for a 0.95 confidence interval). The baselined benchmarks are serial hot-path checks, so the gate pins Go's benchmark CPU list to `1` instead of inheriting workstation-wide `GOMAXPROCS` scheduler noise.
- Threshold: a **statistically-significant > 20% sec/op regression for any individual benchmark** fails the gate. In real local/workstation mode, the **same benchmark name** must regress on a second benchmark run before the gate exits red; a first-run-only regression, or disjoint first/second-attempt regression sets, is reported as a local-noise warning and the gate exits green. The retry does not widen the threshold, skip the benchmark suite, or refresh the baseline: both attempts use the same `bench/baseline.txt`, `-count=6`, `-cpu=1`, and `benchstat` parser. Compare-file mode (`scripts/check-bench.sh BASE CURRENT`) remains single-pass and fails immediately, so the hardware-independent self-test can prove a real >20% `sec/op` regression still exits non-zero. Only **sec/op** is gated — the custom `ReportMetric` values (B/s, events/sec, peak_heap_mb, …) are informational (peak_heap_mb is benchtime-sensitive), and the per-block `geomean` aggregate is excluded (a noisy benchmark moves it without any single benchmark significantly regressing). Self-tested by `scripts/test/check-bench-test.sh`.
- Fail-closed cases: missing, empty, or benchmarkless baseline, missing or empty current output, failed benchmark command, failed `benchstat`, a dropped/renamed baseline benchmark, or disjoint benchmark config groups all exit non-zero. `scripts/test/check-bench-test.sh` must dynamically exercise compare-file parser behavior and real-mode retry/error behavior with hermetic fakes, including disjoint first/second-attempt regression sets, without running the expensive benchmark suite.
- Gated benchmarks must measure the intended hot path without helper-goroutine scheduler noise in the timed region. If the production path is serial (for example SSE `Hub.Deliver` fanout), the benchmark fixture uses deterministic buffering/pre-seeding instead of background helper goroutines to keep the fast path open; otherwise the local workstation gate can fail on unchanged code under ordinary desktop/VM load. Very small serial hot-path benchmarks may amortize a deterministic fixed batch inside each benchmark operation when a single call is too small to be a stable local `sec/op` signal; they still report the per-hot-path-unit metric (for example deliveries/sec) so the underlying operation remains visible.
- Real benchmark runs print compact diagnostic context before measuring: Go version, effective `GOMAXPROCS`, benchmark `-cpu` setting, package list, baseline path, temporary current path, and `/proc/loadavg` values when available. These diagnostics do not change pass/fail status; they make a red local benchmark gate auditable without exposing process command lines or sensitive data.
- **`check-bench.sh` is a local/workstation gate**, not a CI gate: `bench/baseline.txt` is workstation-measured and is not comparable to GitHub-runner hardware. CI keeps the bench compile-smoke (`-benchtime=1x`, artifact-uploaded for trend) and runs the hardware-independent gate self-test; a runner-baselined CI regression gate is a deferred follow-up. `bench/baseline.txt` carries benchmark-code provenance (an implementing commit SHA when the benchmark code predates the baseline, or a same-commit `git blame` note when benchmark code and baseline land together), the exact benchmark command, and the `goos/goarch/pkg/cpu` config lines (benchstat groups by config — baseline and current must share them). Baseline refresh requires an explicit SOW (no auto-update); SOW-0058 refreshes the workstation baseline for the `-cpu=1` serial-hot-path contract.
- CI's `Require benchmarks` compile-smoke presence check extracts required
  benchmark names from `bench/baseline.txt` and implemented benchmark names from
  `*_test.go`. That extractor must accept both Go benchmark row forms:
  suffixed rows emitted by multi-CPU benchmark runs (`BenchmarkName-N`) and
  unsuffixed rows emitted by `-cpu=1` (`BenchmarkName`). It must normalize both
  to the same logical benchmark name before comparing the expected 11 names.
  `scripts/test/check-bench-test.sh` self-tests this CI parser contract so a
  baseline refresh cannot make local gates green while CI fails on benchmark-row
  shape.

### Go — Rollup Correctness Diff (SOW-0007)

- A CI test that, over the same fixture, runs (a) a full backfill-from-scratch (`rollups-backfill`, `ingester.md` §Rollup Refresh) and (b) an incremental refresh starting from an empty DB, then sorts and diffs the two `rollup_hourly` tables — and the two `rollup_daily` tables — **byte-for-byte**. Passes only when the backfill and incremental results are identical.
- This is the executable form of the additivity invariant (`data-model.md` §Rollup tables): a closed bucket's value must be path-independent, so backfill and incremental cannot diverge.
- Gates any commit touching the rollups package.
- Threshold: zero diff between the two materializations.

### Go — Aggregate / Search Perf (SOW-0007, best-effort)

- `GET /api/stats/aggregate` p95 < 200 ms over a 1M-op fixture.
- `GET /api/search` p95 < 500 ms over a 10M-log fixture.
- These follow the same convention as SOW-0006's E2E perf budgets: where a large deterministic fixture is not yet available, the measurement is **annotation-logged in CI** (recorded, surfaced, trended) rather than hard-failing the build; it becomes a hard gate once the deterministic fixture exists. The `≤ 20%`-vs-baseline bench-regression rule (§Go — Benchmarks) still applies to whatever rollup/search benchmarks are baselined in `bench/baseline.txt`.

### Go — Race Stress

- Concurrency-touching changes: `scripts/test.sh --stress 10` (`-race -count=10`) locally before commit.
- CI: per-push runs `-race -count=1` (the `test` job — kept at 1 so PR feedback stays fast); race **stress** runs nightly via `race-stress-nightly.yml` (`scripts/test.sh --stress 10`, scheduled, not a per-push gate). Per-push `-count>1` is gated on first speeding up the slow `internal/ingest` rollup test (`TestRefreshRollups_OtherStaleRowRemoval` — several minutes under `-race`, grown since SOW-0009; re-time before raising the count) (SOW-0009 Followup); until then the marginal added race-coverage per push does not justify the latency.

### Frontend — Lint

- `npm run lint` (the `lint` npm script bakes in `--max-warnings=0` — the single
  source of truth for the flag; neither `scripts/lint.sh` nor the CI Lint step
  re-passes it). Run locally via the build-free `scripts/lint.sh` frontend section
  (alongside Go lint), or standalone from `frontend/`.
- ESLint flat config (`frontend/eslint.config.ts` — `.ts`, not the `.js` some older notes name) with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import` (resolver: `eslint-import-resolver-typescript`).
- Built with ESLint core's `defineConfig()` + `globalIgnores()` (`eslint/config`), not the `@deprecated` `tseslint.config()` helper. jsx-a11y + import use native flat-config (`flatConfigs.*`) — no `FlatCompat`. import/recommended's three `'warn'` rules are promoted to `'error'`. `jsx-a11y/no-noninteractive-tabindex` allows `role="region"` (scrollable-region pattern). Untyped-plugin friction handled without `any`: an ambient `src/types/eslint-plugin-jsx-a11y.d.ts` shim + a narrow `Plugins[string]` cast for react-hooks + a config-file-scoped relaxation block (details in `project-frontend` skill §Lint).
- Threshold: zero warnings.

### Frontend — Type Check

- `npm run typecheck` (invokes `tsc --noEmit`). Also run locally by the
  `scripts/lint.sh` frontend section (build-free).
- `tsconfig.json` enforces `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`, `noUnusedLocals`, `noUnusedParameters`.
- Threshold: zero errors.

### Frontend — Unit/Component

- `npm run test -- --run --coverage`.
- Vitest + React Testing Library.
- Threshold: all pass; **global** aggregate floor (≥ 80% lines/statements/functions, ≥ 75% branches) **plus a per-directory ≥ 80% lines floor** for every measured directory under `src/components/` and `src/pages/`.
- **Per-directory mechanism = Vitest's NATIVE glob-keyed `coverage.thresholds`** (SOW-0012 Chunk C; Vitest ≥ 4, verified against the installed 4.1.7). `frontend/vitest.config.ts` lists one glob key per measured dir (`'src/components/<Dir>/**': { lines: 80 }`, `'src/pages/<Dir>/**': { lines: 80 }`); Vitest aggregates each glob group's matched files into one coverage map and **fails the run (exit 1)** if a group's lines % is below the floor, emitting `ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)`. **No wrapper script** — the floor lives in the config the same command already runs. A shared `PER_DIR_LINES` constant keeps the global floor and every per-dir group in lockstep.
- **Glob keys track the measured dirs only — lockstep is BIDIRECTIONAL.** A glob group that matches **zero files** has lines pct `"Unknown"`, which **vacuously PASSES** (`"Unknown" < 80` is `false`). So a per-dir key is added **only** for a dir that is in `coverage.include`, **and** every measured component/page dir has a per-dir key — i.e. the gated set and the measured set are **EQUAL** (`gatedDirs === measuredDirs`), enforced both ways by the verifier (R8-2). Adding a per-dir key for a dir that is **not** measured (excluded, or absent from `include`) is a no-op floor that can never fire; omitting a measured dir's key leaves it gated only by the weaker global aggregate. The global floor still gates every included file in aggregate.
- **Intentional non-measurement is an explicit ledger, not an omission.** Source dirs/files under `src/components/`/`src/pages/` that are deliberately not measured live in `COVERAGE_EXCLUDED` (in `vitest.coverage.mjs`) **with a per-entry rationale**. Today: `Layout` + `StatCard` are **real** components whose dedicated Vitest **unit** coverage is **deferred** to a tracked follow-up (Playwright E2E exercises them on every route / on `/stats` respectively — they are **not** placeholders); the `Agents`/`Models`/`Tools` pages are bare Phase-3 `<ComingSoon/>` wrappers (no logic of their own); `NotFound.tsx` is the trivial 404 (axe-covered via E2E). The shared `ComingSoon.tsx` component itself **is measured** (it has a unit test) and carries no per-dir floor because it is a flat file, not a dir.
- The HTML report (`frontend/coverage/`) is produced by the `html` reporter and
  uploaded as a CI artifact (`frontend-coverage-<run_id>`); the `json` reporter
  emits `coverage/coverage-final.json`; the `lcov` reporter emits
  `coverage/lcov.info` for Codacy coverage upload.
- **Shared dir lists (SOW-0012 review F3).** `PER_DIR_GLOBS`, `COVERAGE_INCLUDE`, `COVERAGE_EXCLUDED`, and `PER_DIR_LINES` live in `frontend/vitest.coverage.mjs`; `vitest.config.ts` imports the measuring lists and the config verifier below imports all of them, so the gate Vitest enforces and the checks against it cannot read different lists. (`.mjs` so the standalone Node verifier needs no TS loader; typed for `vitest.config.ts` via the co-located `vitest.coverage.d.mts`.)
- **Two independent guards, distinct jobs:**
  - **Gate-MECHANISM self-test:** `frontend/scripts/check-coverage-thresholds.test.sh` (`npm run check:coverage-thresholds:selftest`) runs Vitest on a **throwaway fixture** project (its own config + a known 50%-lines dir) and asserts the native glob-keyed threshold **fails closed** (exit 1, naming the dir) under the floor and passes above it. It proves the **Vitest mechanism** on the installed version still fails closed — it does **not** read the real config, so it cannot catch a real-config glob that matches zero files or a measured dir with no floor.
  - **Real-config verifier:** `frontend/scripts/check-coverage-config.mjs` (`npm run check:coverage-config`) reads the REAL shared lists and **fails closed (exit 1, naming the offender)** on **three** classes of defect: **(a) per-dir floor shape + non-vacuity** — a `PER_DIR_GLOBS` entry that is not the EXACT canonical threshold shape `<root>/<Dir>/**` (detailed below) or, if canonical, matches **zero** real source files on disk (the vacuous-`"Unknown"`-pass trap); **(b) lockstep (BIDIRECTIONAL)** — a **measured** dir under `src/components/`/`src/pages/` (a `COVERAGE_INCLUDE` entry with ≥ 1 non-test `.ts`/`.tsx`) with **no** per-dir floor (measured ⊄ gated), **and** (R8-2) a `PER_DIR_GLOBS` floor whose dir is **not** measured (it is in `COVERAGE_EXCLUDED`, or absent from `COVERAGE_INCLUDE`) — a threshold group over a dir Vitest never instruments is a vacuous no-op that can never fire (gated ⊄ measured); together these force `gatedDirs === measuredDirs`; **(c) disk-completeness** — a source dir, or a flat `.ts`/`.tsx` file, under either root that is in **neither** `COVERAGE_INCLUDE` **nor** `COVERAGE_EXCLUDED` (so shipped source cannot silently escape both coverage **and** checks (a)/(b)). It also fails closed on an unsupported broad whole-root include glob (a first segment containing a glob metachar, e.g. `src/pages/**/*.{ts,tsx}` or `src/pages/*o/**`), on a per-dir include that is **not the EXACT canonical whole-dir shape** `<root>/<Dir>/**/*.{ts,tsx}` (a bare dir, an extension-narrowed `**/*.tsx`/`**/*.ts`, a narrow filename `**/Foo.tsx`, or a deeper subpath — each would let Vitest instrument **less** than the whole dir while disk-completeness marks `<Dir>` measured, so a sibling source escapes; an exact match is the tightest rule, it cannot be narrowed), on a `PER_DIR_GLOBS` entry that is **not the EXACT canonical threshold shape** `<root>/<Dir>/**` (a bare dir like `src/components/Foo`, or a deeper/narrower glob — a Vitest threshold KEY must end in `/**` to match file paths, so a bare-dir key matches **nothing** and that floor **vacuously passes**; only a canonical entry contributes its dir to the gated set, so a malformed floor cannot masquerade as gating its dir), and on a malformed entry with a `.`/`..` path segment. **The per-dir-root shape checks (both `PER_DIR_GLOBS` and per-dir `COVERAGE_INCLUDE`) compare the RAW string, not a normalized one (R8-1):** `vitest.config.ts` hands the RAW strings to Vitest — `PER_DIR_GLOBS` keys are matched by **picomatch** against clean `relative(root,file)` paths and `COVERAGE_INCLUDE` feeds the **tinyglobby** selector — so a string that is canonical only **after** normalization (a leading `./`, a repeated `//`, or a trailing `/`) is rejected: a `//`/trailing-`/` threshold key matches **nothing** (its floor vacuously passes) and the `./`-tolerant form is fragile across picomatch/tinyglobby versions. Build-free (`node:fs` only); normalization is still used to **detect** `.`/`..` segments and to **derive** the dir, but it must **not launder** a raw entry into passing the shape check. Its own decision logic has a hermetic self-test (`frontend/scripts/check-coverage-config.test.sh`, `npm run check:coverage-config:selftest`). Runs in `scripts/lint.sh`'s frontend section **and** as a dedicated CI step in the `frontend` job; mirrors the self-test wiring.
- A dir under the floor is a finding to close with tests (or, if genuinely large, to escalate) — **never** lower the threshold to make a dir pass.

### Frontend — E2E

- `npm run e2e` (Playwright headless) — runs the **gating** `chromium` project against the EMBEDDED SPA served by the built `ai-viewer-serve` binary on a deterministically seeded temp DB (`scripts/e2e-serve.sh`), never a bare `vite preview`.
- Coverage: every primary user flow plus error states (network failure, empty list, malformed SSE event). The five AC#4 scenarios (SOW-0012) and the spec that covers each:
  - **sessions-list filter** — `tests/sessions-filter.spec.ts` (FilterBar agents filter narrows the list, URL carries `?agents=`, a non-matching term collapses to the empty state, Clear restores the full list; the agent name is runtime-derived from `/api/sessions`).
  - **session-detail load** — `tests/deep-link.spec.ts` (hard nav to `/sessions/<id>` renders the detail Overview via the SPA fallback).
  - **sources panel** — `tests/routes.spec.ts` (`/sources` renders the table + health badge).
  - **real-time SSE update (deterministic)** — `tests/sse-update.spec.ts` (a controlled `session_changed` frame, injected via a fake `EventSource` installed before app scripts, drives the documented `['sessions']` invalidation → a fresh `GET /api/sessions` refetch — deterministic, not timing-luck; the server is read-only so no writer is needed). `tests/realtime.spec.ts` + `tests/viz-sse.spec.ts` additionally assert the REAL stream opens at the network level.
  - **theme toggle (OS + override)** — `tests/theme.spec.ts` (explicit Dark/Light persisted to `localStorage.aiViewerTheme` and reapplied after reload; Auto clears the override and follows `prefers-color-scheme` via `emulateMedia`).
- **Per-test timeout / retries (SOW-0012 AC#4 / R3):** the global config sets `timeout: 15_000` and `retries: 0` (no blanket retries). The SSE flows opt into `test.describe.configure({ retries: 1, timeout: 30_000 })` (the EventSource open is the slowest checkpoint and the one place CI-runner slowness can transiently miss); every other spec stays deterministic with no retries.
- **Quarantine (never `test.skip`):** a genuinely flaky spec is MOVED to `frontend/tests/quarantine/` with a linked SOW filename in its file header — not silenced. The gating `chromium` project excludes that directory (`testIgnore: '**/quarantine/**'`), so a quarantined spec stops blocking merge automatically; it still RUNS via `npm run e2e:quarantine` (`--project=quarantine`, non-gating, diagnostic). The directory is **empty on delivery** (`.gitkeep` + a `README.md` documenting the policy). Quarantine policy lives in `frontend/tests/quarantine/README.md`.
- Threshold: all pass.

### Frontend — Accessibility

- `npm run e2e:a11y` (SOW-0012) runs the axe specs (`tests/a11y.spec.ts`, `tests/viz-a11y.spec.ts`, `tests/stats-a11y.spec.ts`) on the gating `chromium` project; `@axe-core/playwright` runs an axe scan on **every `App.tsx` route** under both themes: `/`, `/sessions/:id` (overview + **logs** + trace + topology + timeline tabs), `/sources`, `/stats`, `/topology`, the Phase-3 ComingSoon stub routes `/tools`, `/models`, `/agents`, and the NotFound catch-all (an unknown path, e.g. `/no-such-route`, that the `*` route renders). The stub + NotFound scans (both themes) live in `tests/a11y.spec.ts`; the Logs-tab scan (SOW-0012) closed the one detail tab the prior specs missed. After SOW-0012, every route declared in `App.tsx` is axe-covered. (The full E2E run `npm run e2e` includes these specs too.)
- Threshold: zero serious/critical violations.
- **Waivers are per-selector, never global.** A D3/canvas chart that needs an axe exclusion (or has a known a11y limitation) documents it in `frontend/src/viz/<chart>/a11y.md` (`waterfall`, `flamegraph`, `timeline`, `topology`); any exclusion is applied via `new AxeBuilder({ page }).exclude('<selector>')` in the test, never `disableRules` across the whole page. Two charts carry a **documented known limitation** (tracked in **SOW-0041** (pending); originally noted in SOW-0012 `## Followup`, fix deferred): the `WaterfallCanvas` and `FlameGraph` Canvas renderers (above `SVG_SPAN_CEILING`) have no focusable-span fallback, so Canvas-mode keyboard span-selection is missing — and the lint gate is **blind** to the FlameGraph case because its `onClick` sits on the `<canvas>` element. The Timeline and Topology Canvas renderers DO provide a focusable `<button>` fallback list, so they have no gap. axe cannot see inside `<canvas>`, so neither limitation is caught by the axe gate.

### Frontend — Bundle Size

- After `vite build`, `frontend/scripts/check-bundle-size.js` measures the gzipped size of every emitted JS chunk and gates it (SOW-0012). It is an enforced gate, not a report.
- **Chunk classification is manifest-driven, not filename-heuristic.** `vite.config.ts` sets `build.manifest: true`, so Vite emits `dist/.vite/manifest.json` with a per-chunk `isEntry` / `isDynamicEntry` flag (Vite's documented `ManifestChunk` contract). The gate reads that manifest:
  - **Main chunk** = a JS chunk with `isEntry: true` (the HTML `<script type="module">` entry). Budget: ≤ 500 KB gzipped.
  - **Per-route lazy chunk** = a JS chunk with `isDynamicEntry: true` (emitted by a `React.lazy`/dynamic `import()` route split). Budget: ≤ 200 KB gzipped each.
  - **A MAIN/LAZY entry's budget is its transitive static-import CLOSURE, not its `file` alone.** The gate sums the gzipped size of the entry's own `.file` PLUS the transitive closure of its static `imports` (walk `manifest[key].imports` recursively; each reached key contributes its `.file`; files are de-duplicated *within* one entry's closure so a diamond import graph counts a shared chunk once; each entry's closure is summed independently, so a chunk shared by two entries is budgeted under each — that is the real per-route transfer cost). `dynamicImports` are **not** followed into a closure: they point at separately-budgeted lazy chunks the browser does not fetch on the entry's initial load. They ARE validated up front, though (see Fail-closed below), so a mis-flagged lazy chunk cannot slip to "ungated" instead of being budgeted. This is why a Rollup-split SHARED chunk (neither `isEntry` nor `isDynamicEntry`, reachable only via an entry's `imports[]`) **is** gated under the entry that pulls it in — a tiny lazy route that statically imports a huge shared chunk cannot slip through as "ungated" (the SOW-0012 F1 fail-open this closure model closed).
  - **Only genuinely un-classified, un-reached JS is "ungated".** A JS file under `dist/assets/` that is neither classified as an entry NOR inside any gated entry's static-import closure (e.g. a `?worker` bundle such as `forceWorker-*.js` instantiated via `new Worker()` and absent from the manifest, or a chunk only ever reached via `dynamicImports`) is **measured and reported for visibility but not gated** — the two budgets above are defined only for HTML-entry and route-lazy chunks and what they statically pull in. A future budget for worker-only chunks is a separate SOW.
- Invocation: `node frontend/scripts/check-bundle-size.js [distDir]` (default `distDir` = `frontend/dist`). The optional arg lets the self-test point the gate at a synthetic fixture dir. Thresholds are named constants in the script.
- **Fail-closed (no silent pass) — complete case list:** each of the following exits non-zero — a missing or empty `distDir`; a missing/invalid/non-object `.vite/manifest.json` (including a JSON array); zero classified JS chunks; no MAIN (`isEntry`) chunk; **any** manifest entry (JS **or** non-JS, e.g. a CSS chunk) whose `.file` is absent on disk (an up-front sweep over every chunk, not just gated JS); a JS chunk flagged **both** `isEntry` **and** `isDynamicEntry` (mutually exclusive — a both-flagged route chunk would be under-budgeted as MAIN); a static-import graph referencing a missing/invalid chunk key; an `imports`/`dynamicImports` present but **not an array**; a **non-string** element in an `imports` array; a **non-string** element in a `dynamicImports` array; a `dynamicImports` element referencing a **missing** manifest key; and a `dynamicImports` element referencing a **JS** chunk that is **not** `isDynamicEntry`. (The `dynamicImports` element/target checks close the lazy-chunk half of the contract — a mis-flagged or dangling target would otherwise slip to "ungated" instead of being budgeted as the lazy chunk it is.) The gate never certifies "within budget" without measuring real chunks.
- Self-tested by `frontend/scripts/check-bundle-size.test.sh` (synthetic fixture dirs with high-entropy/incompressible JS so the gzipped budgets are actually exercised): a main chunk (and a main+static-import closure) over 500 KB gz fails, a lazy chunk (and a lazy+static-import closure) over 200 KB gz fails, a closure de-dups a doubly-imported shared chunk, `dynamicImports` are not folded into the importing entry's budget, all-under-budget passes, and every fail-closed case above (missing/empty dist, bad/array manifest, zero JS chunks, no main entry, a JS-or-non-JS entry whose `.file` is absent on disk, a both-`isEntry`-and-`isDynamicEntry` JS chunk, missing static-import key, non-string `imports` element, non-array `imports`/`dynamicImports`, non-string/missing-key/non-`isDynamicEntry` `dynamicImports` target) exits non-zero.
- CI runs the gate in the `frontend` job after the build (failing the job on a violation) and still uploads the gzipped/raw size report artifact; a dedicated self-test step keeps the gate script itself from silently rotting.
- Exceeding a budget requires a SOW with justification — never raise the threshold to land a chunk.

### Secrets + Operator-PII Scan

`scripts/scan-secrets.sh` scans **every tracked file in the repository** (not just
`testdata/`) and exits non-zero on any hit. It enforces two rule classes:

1. **Operator identity (banned everywhere, zero tolerance).** The operator's real
   email addresses, real home path, and given/sur-name. These must never appear in
   any tracked file — including the sanitizer's `INPUT/` fixtures. The ban-list is
   **derived at runtime from the repository's own git author metadata** — `git log`
   author emails + names, unioned with `git config user.email`/`user.name`; home
   stems from each email local-part, each name, and `$HOME` — so **no operator
   literal is committed in the scanner**. Synthetic placeholder commit identities
   used only to avoid personal commit metadata (currently the exact values `user`
   and `user@example.invalid`, matched case-insensitively) are ignored while
   deriving the Rule-1 ban-list. The exact neutral home stem `user` is also
   ignored when it comes only from `$HOME`, so committed placeholder paths such
   as `/home/user/...` stay portable across generic dev VMs. Filtering is
   intentionally narrow and applies only to derivation inputs, not to tracked
   file content; real derived operator identities are never allow-listed. The
   scan is **fail-closed**: if no non-placeholder identity can be derived after
   filtering (and, when the repo has commits, if `git log` fails unexpectedly)
   it exits non-zero rather than running with Rule 1 disabled or on a partial
   ban-list. Name matching is word-bounded so unrelated tokens (e.g. `cost_usd`)
   never match.
2. **Generic secret shapes.** `sk-…` / `sk-ant-…` (OpenAI/Anthropic keys),
   `xox[bpas]-…` (Slack), `AKIA[0-9A-Z]{16}` (AWS), `Bearer <high-entropy>`
   tokens, and VCS PATs (`ghp_…`, `github_pat_…`, `glpat-…`). (Public provider
   *hostnames* like `api.anthropic.com` are NOT secrets and are not scanned —
   they are a sanitizer concern; fixtures rewrite them to `*.example.invalid`.) A
   secret-shape token is flagged **everywhere**, with a single exemption: a token
   carrying the synthetic marker `EXAMPLE` (e.g. `sk-ant-EXAMPLE…`) — the
   convention for the dirty inputs that exercise `sanitize-fixture.sh`'s
   redaction. A secret-shape token WITHOUT `EXAMPLE` is flagged even under
   `scripts/test/fixtures/*/INPUT/**`, so "synthetic only" for fixtures is
   enforced by the gate, not merely policy.

The `EXAMPLE` exemption is matched **per token, never per line** — a real secret
on the same line as a placeholder is still flagged — and applies **only to rule
class 2**; rule class 1 (operator identity) is never exempted under any
circumstances. `.gz` archives are decompressed and scanned; a malformed archive
is scanned raw and its decompression failure reported (never silently skipped).
Tracked symlinks are scanned by their target-path string, not the dereferenced
target.

Diagnostics are intentionally redacted: failures report the repo path, line
number, rule label, and a redacted/summary marker only. The scanner never prints
the raw matched operator identity, raw offending line, or raw secret token into
local or CI logs.

- **Threshold:** zero hits.
- **Fail-closed in CI.** The `gates` job runs the scanner and **fails when the
  scanner or its self-test is absent** — a missing scanner is a missing gate, not
  a pass. Required gate infrastructure (`scripts/spec-drift.sh`,
  `scripts/test/spec-drift-test.sh`, `scripts/scan-ai-attribution.sh`, and the
  local aggregate `scripts/gates.sh`) is also presence-checked fail-closed in
  the same job; only optional helper gates such as systemd lint may skip when
  absent.
- **Negative self-test.** The scanner ships with a test that plants an
  operator-identity string in a temp file and asserts the scan flags it, so the
  enforcement itself cannot silently rot.
- **FTS5 fixture sanitization (SOW-0007).** Log-message fixtures committed to
  `testdata/` MUST be sanitized via `scripts/sanitize-fixture.sh` before commit —
  `fts_logs` indexes the message body verbatim (`data-model.md` §Full-text search),
  so an unsanitized log line would carry real message text into both the fixture
  and any index built over it. The secret scanner is the safety net, not a
  substitute for sanitizing at write-time (AGENTS.md §Sensitive Data).

### Spec Drift

`scripts/spec-drift.sh` lints five spec↔code drift indicators and **exits
non-zero on any drift, naming the offending indicator + the specific
code/spec token**. Each indicator is **bidirectional** (code-not-in-spec AND
spec-not-in-code) except where an intentional one-direction exemption is
documented below. It is grep/awk-based (the code and spec surfaces are
line-oriented and regular — route literals, `case "<kind>"` strings,
`EventKind = "<value>"` consts, SQL `CREATE TABLE`/`ALTER … ADD COLUMN`, and
`format: "<name>"` discovery structs; no `go/ast` parse is required). The five
indicators and their authoritative code/spec locations:

- **REST endpoints** — `mux.HandleFunc("/api/…", p.<handler>)` registrations in
  `internal/presenter/presenter.go`, plus each handler's `r.Method` guard in the
  presenter package, vs. `### <VERB> /api/…` headings in
  `.agents/sow/specs/rest-api.md`.
  The compared token is **`<VERB> <normalized-path>`**, not path alone, so a
  handler accepting `GET` while the spec says `POST` is drift. `HEAD` is treated
  as implicit parity for handlers that support `GET`, matching the REST spec and
  Go handler behavior. Path wildcards are normalized (`{id}` ↔ `:id`,
  single-value `:ref` ↔ `:id`, `{tools,models,agents}` ↔ the catalog group).
  **One-direction exemption:** a spec-only endpoint whose section is explicitly
  marked **not registered / Phase 2 / not implemented** (today
  `GET /api/catalog/{…}` and `GET /api/payloads/:ref`) is NOT drift — a viewer
  must not advertise a route it does not serve, so documenting a future route
  ahead of its handler is allowed. Every *registered* route+verb pair MUST be
  documented (code→spec is unconditional).
- **SSE event types** — the wire kinds in `internal/presenter/events_sse.go`
  (the `eventPayload` `case "<kind>"` arms, the `event: <kind>` writes, and the
  `event: resync` / `: keepalive` control frames; `stats_invalidated` is the
  `default` arm and is sourced from `subscription_filter.go`) vs. the event-type
  headings in `.agents/sow/specs/sse-protocol.md`. `resync` is a reconnect-control frame
  documented under §Reconnect Behavior rather than a §Event-Types heading; the
  indicator treats it as a known control frame.
- **SQLite columns** — every **`table.column`** pair from
  `internal/store/migrations/*.sql` (`CREATE TABLE` columns, `CREATE VIRTUAL
  TABLE … USING fts5(…)` tokens, and `ALTER TABLE … ADD COLUMN`) plus every
  table name, vs. `.agents/sow/specs/data-model.md`. Direction: **code→spec** — every
  migration table + column pair MUST be documented in the corresponding SQL
  schema block for that table in `data-model.md` (the direction that catches the
  real "added a column to a table, forgot to document it" drift). A global
  mention of the same column name under another table is not enough. Table names
  are checked bidirectionally from two spec surfaces: `CREATE [VIRTUAL] TABLE`
  names extracted from SQL schema blocks, and any `### <table>` schema heading.
  Therefore a spec-only SQL table declared under a prose heading (for example an
  FTS table in a shared "Full-text search" section) is still drift if no
  migration creates it. Column spec→code is not enforced as drift because the
  spec legitimately names many column identifiers in prose.
- **Canonical event kinds** — the `EvXxx EventKind = "<value>"` discriminator
  constants in `internal/canonical/events.go` vs. the identical fenced block in
  `.agents/sow/specs/canonical-events.md`. Bidirectional and exact (the value set must match
  byte-for-byte).
- **Adapter discovery probes** — the `format: "<name>"` probe structs in
  `cmd/ai-viewer-ingest/sources.go` (the auto-discovery probe list) vs. the
  existence of the corresponding `.agents/sow/specs/adapter-<name>.md` and that spec
  mentioning the adapter's default probe path. Format→spec-file name maps
  underscore→hyphen (`aiagent_v3` → `adapter-aiagent-v3.md`, `claude-code` →
  `adapter-claude-code.md`).

- **Fail-closed in CI.** The `gates` job fails if `scripts/spec-drift.sh` or
  `scripts/test/spec-drift-test.sh` is absent, then runs the self-test **before**
  the live detector. A detector that cannot catch planted drift is a failed gate,
  not an all-clear.
- **Self-test:** `scripts/test/spec-drift-test.sh` plants synthetic mismatches in
  a throwaway copy of the repo. It covers every one-direction indicator and both
  sides of each bidirectional indicator (REST path and REST method, SSE,
  data-model table names and table-scoped column pairs, canonical event kinds,
  adapter probe files/anchors), then asserts the clean copy exits 0 — so the
  detector itself cannot silently rot. Fixture creation is fail-closed: if
  required migration SQL or adapter spec inputs are absent, the self-test fails
  instead of building a vacuous copy. (Mirrors the `scan-secrets`/coverage gate
  self-test discipline.)
- **Threshold:** zero drift. Exit 0 must come from genuine spec↔code agreement,
  never from weakening an indicator. Real drift found on the live tree is
  reported and adjudicated (fix the spec or the code), not masked.

### Build

- `./scripts/build.sh` builds frontend, embeds, builds both Go binaries.
- Threshold: clean build, both binaries present, sizes within expected range.

### Mutation Testing (Recommended, Not Per-Commit)

- `go-mutesting ./internal/...` quarterly on critical paths (`internal/canonical`, `internal/ingest`, adapters).
- Surviving mutants are treated as test gaps and filed as SOWs.

## Aggregate Scripts

CI enforces the gate catalog as **dedicated parallel jobs** (`lint`, `test`,
`frontend`, `embed-smoke`) plus the cross-cutting `gates` job — see
`.github/workflows/ci.yml`. The implemented helper scripts today are
`scripts/build.sh` (frontend + embed + both binaries), `scripts/dev.sh`,
`scripts/embed-smoke.sh`, `scripts/e2e-serve.sh`, `scripts/sanitize-fixture.sh`,
`scripts/scan-secrets.sh` (+ `scripts/test/scan-secrets-test.sh`),
`scripts/scan-ai-attribution.sh`, `scripts/spec-drift.sh`
(+ `scripts/test/spec-drift-test.sh`), `scripts/test/lint-test.sh`,
`scripts/codacy-coverage-upload.sh`
(+ `scripts/test/codacy-coverage-upload-test.sh`), `scripts/gates.sh`,
`scripts/check-bench.sh`, and `scripts/install-systemd-user.sh`.

`scripts/lint.sh` (SOW-0009; frontend section added SOW-0012) **is present**: it
is the local, **build-free** module/static-analysis entrypoint mirroring CI's
static gates. Its Go section mirrors the CI `lint` job — `go mod tidy`
followed by `git diff --exit-code go.mod go.sum`, standalone `gofmt` and
standalone `goimports@latest` over the tracked Go file list, `go vet`,
`golangci-lint run` (the umbrella, driven by `.golangci.yml` at the version
pinned in `.golangci-lint-version`, with local pin mismatch treated as a hard
failure), then standalone `gosec` and `govulncheck`.
Its frontend section (skipped cleanly when `frontend/` is absent) mirrors the
build-free static gates of the CI `frontend` job, fail-fast in order: ensure
deps are present (reusing `scripts/build.sh`'s `npm ci`/`npm install` fallback,
but only when `node_modules` is missing — this is a fast analysis pass, not a
build), `npm run lint` (the `lint` npm script bakes in `--max-warnings=0` — the
single source of truth for the flag; lint.sh does not re-pass it), `npm run
typecheck`, the bundle-size **gate-logic self-test** (`npm run
check:bundle-size:selftest`), the **coverage-config verifier** (`npm run
check:coverage-config` — checks the REAL per-dir floors, see §Frontend —
Unit/Component), then the per-dir coverage **gate-logic self-test** (`npm run
check:coverage-thresholds:selftest`). `scripts/lint.sh` does **not** run the REAL
bundle-size-vs-built-manifest gate (`npm run check:bundle-size`) or the REAL
coverage run (`npm run test -- --run --coverage`): those need a build / full test
run and live in the CI `frontend` job — and in `scripts/build.sh` (which runs
`npm run check:bundle-size` on the just-built `dist/`) / `scripts/test.sh` (which
runs the frontend `npm run test -- --run --coverage` after the Go suite) — not in
this build-free entrypoint. CI enforces the Go set via
the version-pinned `golangci/golangci-lint-action` plus its standalone
gosec/govulncheck steps (CI keeps the cached action rather than invoking
`scripts/lint.sh`, to preserve golangci's analysis cache). `scripts/test.sh` (Go
tests + coverage profile) and `scripts/check-coverage.sh` (the statement
coverage gate) exist (SOW-0010).

`scripts/gates.sh` (SOW-0013) **is present**: the single-command local
workstation aggregate that runs *every local gate* in order, **fail-fast**, with
a section header and per-section wall-clock timing per gate and a final timed
summary. It composes existing scripts and commands — `scripts/lint.sh` (Go +
frontend static analysis), `scripts/scan-secrets.sh` + its self-test,
`scripts/scan-ai-attribution.sh`, `scripts/spec-drift.sh` + its self-test,
`scripts/test/codacy-coverage-upload-test.sh`,
`scripts/test/codacy-config-test.sh`,
`scripts/test/systemd-units-test.sh` when present, `scripts/build.sh` (build +
real bundle-size gate + embed + both binaries), `scripts/test.sh` +
`scripts/check-coverage.sh` (Go race suite + coverage + frontend Vitest), the
deterministic adapter fuzz seed corpus + target-set lock, frontend Playwright
E2E (including the axe a11y specs), and the local benchmark gate self-test +
`scripts/check-bench.sh`. Fast static-analysis/spec/security gates still run
first so quick failures surface early; the local benchmark regression gate runs
after the build and before the long CPU-heavy `-race` and Playwright sections so
the workstation benchmark measures code behavior rather than residual thermal or
load state created by the aggregate itself.

The CI `gates` job does **not** run the full serial `scripts/gates.sh`
aggregate. CI keeps the expensive gates parallel in their dedicated jobs and
uses the `gates` job for cross-cutting repo scans and required gate
infrastructure checks: secrets + scanner self-test (fail-closed), lint
formatter-scope self-test (fail-closed), spec drift + detector self-test
(fail-closed, self-test first), AI-attribution scan (fail-closed),
Codacy coverage-upload self-test, Codacy config self-test, `scripts/gates.sh`
presence + syntax check, `scripts/test/codacy-config-test.sh` presence +
syntax check,
and systemd unit lint when present.

The mature required jobs fail closed on their prerequisites. `lint` and `test`
require the Go module files and the gate scripts they execute; `frontend`
requires the frontend package files, Go module files, `scripts/build.sh`, and
the `e2e` npm script; `embed-smoke` requires the Go/frontend package files plus
the build, smoke, and seed-marker scripts. A PR that removes a required gate
surface fails the corresponding required job; it never turns the job into a
green no-op. Optional helper-only checks (currently systemd unit lint) are the
only checks that may skip when their helper script is absent.

## CI Workflow Mirror Invariant

`.github/workflows/ci.yml` and local `scripts/gates.sh` enforce the **same gate
contract**, but not through one identical serial command. Local `gates.sh` is
the full workstation aggregate; CI runs equivalent commands as parallel jobs so
wall-clock stays bounded by the longest job instead of the serial sum. The
deliberate divergences are documented and narrow: CI's `lint` job uses the
version-pinned `golangci/golangci-lint-action` (to preserve its analysis cache)
rather than shelling `scripts/lint.sh`, but at the **same** pinned version
(`.golangci-lint-version`); the **benchmark regression gate** is local-only
(workstation baseline, see §Go — Benchmarks) while CI runs the bench
compile-smoke + gate self-test; and CI runs the local aggregate's cross-cutting
scan subset plus required-script presence/syntax checks in the dedicated `gates`
job rather than re-running the full serial aggregate. When local and CI disagree
outside those documented differences, the cause is environment (Go/Node version,
OS), test isolation (stale cache, leftover state), or timing — debuggable from
the asymmetric input, never papered over by re-running CI.

## Code Scanning Defence Layer

Code scanning is a defence layer on top of the local gates above, not a
replacement for them.

### CodeQL

`.github/workflows/codeql.yml` runs CodeQL on every push to `master`, every PR to
`master`, a weekly schedule, and manual dispatch. It analyzes the repository's
supported CodeQL surfaces:

- `go` — backend and scripts written in Go.
- `javascript-typescript` — frontend TypeScript/JavaScript; TypeScript is
  analyzed through the JavaScript extractor with TypeScript enabled.
- `actions` — GitHub Actions workflow YAML and action metadata.

The job names `CodeQL (go)`, `CodeQL (javascript-typescript)`, and
`CodeQL (actions)` are required branch-protection contexts. CodeQL uses an
explicit repository config file under `.github/codeql/` once SOW-0044 lands. The
policy is:

- Start from the security-focused CodeQL suites, not broad quality-only findings.
  The project already has strict lint, test, coverage, fuzz, benchmark, a11y,
  bundle, secret, and spec-drift gates; CodeQL's role is semantic security
  analysis and workflow/CICD risk.
- Query-suite changes are code-reviewed SOW changes. Moving from default /
  `security-extended` to broader `security-and-quality` requires measured noise
  evidence and an explicit SOW note.
- Suppressions live in the CodeQL config file as query/path scoped exclusions
  with a SOW or issue reference explaining the false positive. Inline
  `// codeql[...]` suppressions are forbidden unless the active SOW proves that
  a config-level exclusion cannot express the scope.
- A critical/high CodeQL alert is never silently ignored: it is fixed, proven
  false-positive with evidence, or tracked in a follow-up SOW before completion.

### Codacy

Codacy is used for maintainability, complexity, duplication, security, and
coverage visibility. It must be tuned from measured findings; a noisy Codacy
configuration is treated as a defect because it trains maintainers to ignore the
signal.

The project maintains three Codacy surfaces:

- Local Analysis CLI configuration under `.codacy/` for reproducible local
  analysis and machine-readable before/after summaries. The local CLI consumes
  `.codacy/codacy.config.json`. Its top-level `exclude` list is limited to the
  repository-wide non-runtime SOW work-ledger, duplicate instruction symlink,
  generated artifact, dependency, coverage, build-output, local binary-output,
  and local test-output exclusions also present in `.codacy.yml`; any
  test/tooling path exclusion is scoped to the specific local tool entry, not
  hidden globally from Semgrep, Trivy, Lizard, or other tools.
- Codacy Cloud configuration, imported/verified through the Codacy Cloud CLI
  when credentials and organization policy allow it. The Cloud import consumes
  `.codacy/codacy.config.json` for tool/pattern settings.
- `.codacy.yml` at repository root for Codacy path exclusions. This is the
  path-scope policy surface documented by Codacy. Because Codacy ignores UI
  ignored-file settings when this file exists, repository-wide non-runtime
  SOW work-ledger files, duplicate instruction symlinks, generated artifacts,
  dependencies, coverage/build output, local binary output, and local test
  output must be listed explicitly under root `exclude_paths`. Tool-scoped
  Cloud ignores, such as
  `engines.eslint-8.exclude_paths`, are mirrored only into that same tool's JSON
  `exclude` array for local CLI parity.

Approved Codacy path exclusions must record the actual replacement coverage,
not a generic "covered elsewhere" claim. Frontend tests and test support remain
under native ESLint/TypeScript/Vitest/Playwright gates where applicable.
Standalone frontend scripts are covered by their dedicated script self-tests,
build integration, and repository-wide secrets/spec-drift gates when the
frontend ESLint/TypeScript configs intentionally ignore them.

Codacy is **not** automatically a hard required branch-protection gate merely
because the Cloud check exists. It becomes a required context only after the
tuned issue set is high-signal and the required-check name is recorded in
`.github/workflows-checks.yaml` with branch protection updated through the same
post-merge API flow used by SOW-0013.

Tuning rules:

- Disable a tool or pattern only with evidence from local/Cloud analysis. The
  evidence records category/severity/path patterns and why an existing project
  gate or a more precise Codacy rule already covers the risk.
- Generated artifacts, dependency directories, coverage HTML, and test fixtures
  may be excluded only when the exclusion is narrower than the relevant risk.
- Security findings are triaged under `security.md`; critical/high findings are
  fixed, proven false-positive, or tracked.
- Codacy triage records local Analysis CLI and Cloud data separately. Local
  results are reproducible from `.codacy/codacy.config.json`; Cloud results are
  authoritative for the external dashboard only after the tuned config is
  imported and reanalysis completes.
- The Codacy config is a maintained gate surface. The repository carries a
  hermetic config self-test that validates JSON/YAML shape, keeps
  repository-wide non-runtime SOW work-ledger, duplicate instruction symlink,
  generated/local artifact exclusions separate from tool-scoped test/tooling
  exclusions, proves the high-signal security patterns remain enabled, and fails
  if local-only removals such as PMD/SQLint or path-scoped tool exclusions lose
  their documented rationale. That self-test runs in `scripts/gates.sh` and the
  CI `gates` job.
- Test and tooling paths may be excluded from a Codacy tool only when the active
  SOW records a stronger project-native gate for those paths. Runtime frontend
  and Go source paths stay analyzable unless a line-level or rule/path-scoped
  false-positive disposition is recorded.
- Coverage upload reuses existing Go and frontend coverage reports; it does not
  run a second test path.
- Coverage upload lives in the non-required `codacy-coverage` job. The required
  `test` and `frontend` jobs only generate and upload artifacts; they never call
  Codacy directly.
- Coverage upload accepts either `CODACY_PROJECT_TOKEN` repository-token mode or
  `CODACY_API_TOKEN` account-token mode, but the workflow skips the entire
  `codacy-coverage` job on `pull_request` events before checkout, artifact
  download, secret injection, or repository scripts can run. This prevents
  PR-controlled code from reading Codacy secrets. If both secrets exist,
  `CODACY_PROJECT_TOKEN` wins and account-token variables are unset before the
  reporter runs. The upload script also refuses all Codacy coverage upload on
  `pull_request` events before token-mode selection as defense in depth. PR
  coverage upload remains disabled until a future SOW designs a safe path. With
  no usable token,
  `codacy-coverage` logs a skip. With a usable token present on a non-PR event,
  missing artifacts, missing or empty reports, reporter download failures, and
  Codacy upload failures emit GitHub annotations and exit successfully. The job
  is configured fail-soft until Codacy is explicitly promoted to a required
  branch-protection context.
- Coverage upload orchestration lives in `scripts/codacy-coverage-upload.sh`,
  and `scripts/test/codacy-coverage-upload-test.sh` is the hermetic self-test
  for the workflow state machine. The test covers token-mode selection, missing
  or empty report combinations, partial/final sequencing, reporter bootstrap
  validation, and LCOV path normalization; actionlint/YAML parsing alone is not
  sufficient validation for this job. The self-test runs in local
  `scripts/gates.sh` and in the CI `gates` job.
- The `codacy-coverage` job uploads each present non-empty Go/frontend coverage
  report as a partial report. A missing or empty Go report does not block
  uploading frontend LCOV, and a missing or empty frontend LCOV report does not
  block uploading Go coverage. If both reports are missing or empty, the job
  emits annotations and exits successfully without downloading the reporter. If
  at least one partial upload is attempted, the job sends Codacy's required
  `final` notification after the partial upload attempts even if one partial
  command fails. Frontend LCOV is normalized to repository-root paths
  (`frontend/src/...`) before upload because Vitest emits frontend-local
  `src/...` paths.
- Codacy's recommended coverage reporter path is a remote bootstrap script. The
  workflow downloads it with HTTP failure checking and retries into a temporary
  file, verifies the file is a non-empty shell script, runs `bash -n`, and only
  then executes it; process substitution such as `bash <(curl -Ls ...)` is
  forbidden because a failed download can become an empty successful script.
  Codacy documents that the bootstrap script validates the downloaded reporter
  binary checksum; that checksum validation is Codacy's upstream behavior, not a
  local guarantee added by this workflow. The residual remote-bootstrap
  supply-chain risk is accepted only for coverage upload, and the local gates
  remain the authoritative merge blockers until Codacy is explicitly promoted.

**Renaming a CI Job.** The job IDs in `ci.yml` (`lint`, `test`, `frontend`,
`embed-smoke`, `gates`) plus each explicit CodeQL matrix job name (`CodeQL (go)`,
`CodeQL (javascript-typescript)`, `CodeQL (actions)`) are the contract for the
branch-protection **required status checks**. The current required-check names
are recorded in `.github/workflows-checks.yaml` (operator-readable, NOT consumed
by Actions). Renaming a job silently disables its required check (protection
keys by job name). Therefore any SOW that renames a CI job MUST, in the **same
commit**: (1) rename the job in `ci.yml` or `codeql.yml`, (2) update
`.github/workflows-checks.yaml`, and (3) re-run the branch-protection `gh api -X
PUT …/branches/master/protection` full-rule update that registers the new name
(the invocation is documented in `docs/setup.md`). GitHub's status-check-only
endpoint uses `PATCH …/protection/required_status_checks`; the full protection
rule endpoint does not.

## Performance Target

A full local `scripts/gates.sh` run **targets** under 5 minutes on the
operator's workstation. **Measured reality (SOW-0013):** the Go `-race` suite
alone (`scripts/test.sh`, dominated by `internal/ingest`) is the long pole and
pushes the full aggregate **above** the 5-minute target. The gate is kept
**correct and complete** — no gate is dropped or weakened to hit the target.
The measured total + the long pole are recorded in the `scripts/gates.sh`
header and the SOW-0013 Execution Log; a `gates.sh --fast` profile and/or
internal parallelization is a tracked follow-up SOW (`.agents/sow/pending/`),
never a reason to lower the bar. CI runs the equivalent gates as parallel jobs,
so CI wall-clock is the longest single job, not the serial sum.

## When a Gate Fails

1. Read the failure output. Do not guess.
2. Reproduce locally if it was CI-only.
3. Fix the root cause in code, test, or spec.
4. Never weaken the gate. Lowering thresholds, suppressing warnings, or skipping tests is a contract breach.
5. If the gate itself is wrong (e.g. a new false-positive lint rule), file a SOW to update the gate config with evidence.
6. If the gate must be temporarily relaxed (extreme cases): the SOW must include a `Gate Suppression` section with reason, scope, expiry date, and the issue tracking restoration.

## Adding or Removing Gates

- **Add**: when a class of bug or risk would not have been caught by existing gates, design a new gate. Update this spec + the runtime skill + CI + `scripts/gates.sh` in the same commit. Update `AGENTS.md` if it adds a top-level commitment.
- **Remove**: requires an operator-approved SOW with: evidence the gate is wrong or obsolete, what replaces it, what risk class is now unprotected.

## Why These Specific Gates

- **Lint + static + security at zero warnings**: standard Go quality bar; cheap to enforce; high signal.
- **Coverage thresholds**: prevents the "I tested the happy path" gap; new-code ≥ 90% (deferred — SOW-0036) will prevent coverage erosion once a diff↔coverage intersector lands.
- **Fuzz on parsers/decoders**: ai-viewer ingests untrusted JSON from disk; fuzz catches the panics and unbounded allocations static analysis misses.
- **Bench regression**: the perf target (full backfill of 294K v2 files under 60 min) is fragile to per-event regressions; 20% per-bench is a sensitive early signal.
- **Race stress**: ingest pipeline + SSE hub are concurrent; race detector on every run is the minimum bar.
- **Frontend a11y**: ai-viewer is a viewer for the operator's work; if it's not accessible, it betrays the "fast, beautiful, low-friction" goal.
- **Bundle size**: embedded in the Go binary; runaway bundle hurts cold-start and download.
- **Spec drift**: specs are the assistant's durable memory; drift silently corrupts future SOWs.
- **Secrets scan**: fixtures contain real-snapshot shapes; the safety net is mandatory because the operator's sessions are sensitive.

## References

- `AGENTS.md` — Quality Gates section
- `.agents/skills/project-quality-gates/SKILL.md`
- `.agents/skills/project-testing/SKILL.md`
- `.agents/sow/specs/workflow.md`
- `.agents/sow/specs/testing-strategy.md`
- `.agents/sow/specs/security.md`
