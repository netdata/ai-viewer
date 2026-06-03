# Quality Gates

## Purpose

The authoritative catalog of every automated gate enforced in ai-viewer's CI and local pre-commit. CI runs every gate as a dedicated job (except the **benchmark regression gate**, which is local/workstation-only — its baseline is not comparable to CI-runner hardware; CI runs the bench compile-smoke + the gate self-test, see §Go — Benchmarks); the assistant runs the same gates locally before any commit (today via the individual gate commands — a single `./scripts/gates.sh` aggregator is planned, see §Aggregate Scripts). A gate failure is a defect, not a stylistic suggestion: fix the root cause, never weaken the gate.

The runtime companion to this spec is `.agents/skills/project-quality-gates/SKILL.md` (commands and ergonomics). This spec is the durable truth about *what* is enforced and *at what threshold*.

## Operating Rules

- Every gate listed here runs locally before any commit and in CI on every push — except the benchmark regression gate (`scripts/check-bench.sh`), which is local/workstation-only (its baseline is not comparable to CI-runner hardware; CI runs the bench compile-smoke + the gate self-test).
- Weakening a gate to land a PR is a contract breach. The remedy is fixing the root cause or splitting the PR.
- Adding a gate requires updating this spec, the skill, and CI in the same commit.
- Removing a gate requires an operator-approved SOW with stated replacement coverage.
- Local pass + CI fail divergence is investigated as a defect (typically environmental); never papered over by retrying CI.

## Gate Catalog

### Go — Format

- `gofmt -l .` → zero output.
- `goimports -l .` → zero output.
- Threshold: zero diffs.

### Go — Vet

- `go vet ./...` → zero warnings.

### Go — Lint

- `golangci-lint run --timeout=5m` is the umbrella gate: with the formatters enabled it also enforces Go — Format, and the `govet` linter covers Go — Vet, so this single command is the authoritative lint surface. `scripts/lint.sh` runs it (then the standalone security tools); CI runs it via the version-pinned `golangci/golangci-lint-action`.
- golangci-lint is **v2**; `.golangci.yml` declares `version: "2"`. `gosimple` is NOT enabled — golangci v2 merged it into `staticcheck`. `gosec` is NOT a golangci linter here — it runs standalone (Go — Security) to avoid duplicate analysis.
- `.golangci.yml` enables linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `errorlint`, `gocritic`, `revive`, `gocyclo`, `misspell`, `nilerr`, `prealloc`, `unconvert`, `unparam`, `whitespace`, `bodyclose`, `noctx`; formatters: `gofmt`, `goimports`, `gofumpt`.
- `gocyclo` uses `min-complexity: 25` (not 15): the stream parsers/scanners/tailers and the SOW-0007-hardened ingest event loop sit legitimately at 16–24 cyclomatic, so complexity-15 produced ~23 false-positive-class findings on heavily-reviewed, hot-path code. 25 flags genuinely-egregious complexity (the one production outlier, `pricing.validateDoc`, was refactored under it) while preventing future creep.
- `_test.go` is excluded from the style/complexity linters (`gocyclo`, `noctx`, `unparam`, `prealloc`, `revive`, `gocritic`): table-driven tests are intentionally branchy, test setup uses context-less DB calls, and test helpers carry call-site-specific params. The bug-finders (`errcheck`, `staticcheck`, `govet`, `ineffassign`, `unused`, `nilerr`, `errorlint`, `bodyclose`) stay active on tests.
- `frontend/node_modules` is path-excluded (a transitive npm dependency ships a Go reference file that is not project code).
- Version is pinned in `.golangci-lint-version` (single source for CI + `scripts/lint.sh`).
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

- Marked benchmarks for the 5 performance-critical paths: adapter `Scan` + adapter `Tail` (`internal/adapters/aiagent_v2`), SQLite batch insert (`internal/ingest` `worker.flush`), REST query path (`internal/presenter` `handleSessionsList`), SSE fanout (`internal/notify` `Hub.Deliver`). There is **no canonical encode/decode** benchmark — canonical events are constructed directly by adapters and never serialized (`internal/canonical` has no encoders/decoders).
- `scripts/check-bench.sh` runs `go test -run=^$ -bench=. -benchmem -count=6` over the 4 benchmark packages (5 benchmarks; adapter `Scan` + `Tail` share `aiagent_v2`) and compares to `bench/baseline.txt` via `benchstat` (`-count=6` is benchstat's minimum for a 0.95 confidence interval).
- Threshold: a **statistically-significant > 20% sec/op regression for any individual benchmark** fails the gate. Only **sec/op** is gated — the custom `ReportMetric` values (B/s, events/sec, peak_heap_mb, …) are informational (peak_heap_mb is benchtime-sensitive), and the per-block `geomean` aggregate is excluded (a noisy benchmark moves it without any single benchmark significantly regressing). Self-tested by `scripts/test/check-bench-test.sh`.
- **`check-bench.sh` is a local/workstation gate**, not a CI gate: `bench/baseline.txt` is workstation-measured and is not comparable to GitHub-runner hardware. CI keeps the bench compile-smoke (`-benchtime=1x`, artifact-uploaded for trend) and runs the hardware-independent gate self-test; a runner-baselined CI regression gate is a deferred follow-up. `bench/baseline.txt` carries the implementing commit SHA and the `goos/goarch/pkg/cpu` config lines (benchstat groups by config — baseline and current must share it). Baseline refresh requires an explicit SOW (no auto-update).

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

- `npm run lint -- --max-warnings=0`.
- ESLint flat config with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import`.
- Threshold: zero warnings.

### Frontend — Type Check

- `npm run typecheck` (invokes `tsc --noEmit`).
- `tsconfig.json` enforces `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`, `noUnusedLocals`, `noUnusedParameters`.
- Threshold: zero errors.

### Frontend — Unit/Component

- `npm run test -- --run --coverage`.
- Vitest + React Testing Library.
- Threshold: all pass, ≥ 80% lines per component directory.

### Frontend — E2E

- `npm run e2e` (Playwright headless).
- Coverage: every primary user flow plus error states (network failure, empty list, malformed SSE event).
- Flaky tests are quarantined into a separate suite linked to a SOW; never marked `test.skip`.
- Threshold: all pass.

### Frontend — Accessibility

- `@axe-core/playwright` runs on every Playwright route.
- Threshold: zero serious/critical violations.

### Frontend — Bundle Size

- After `vite build`, `frontend/scripts/check-bundle-size.js` measures the gzipped size of every emitted JS chunk and gates it (SOW-0012). It is an enforced gate, not a report.
- **Chunk classification is manifest-driven, not filename-heuristic.** `vite.config.ts` sets `build.manifest: true`, so Vite emits `dist/.vite/manifest.json` with a per-chunk `isEntry` / `isDynamicEntry` flag (Vite's documented `ManifestChunk` contract). The gate reads that manifest:
  - **Main chunk** = a JS chunk with `isEntry: true` (the HTML `<script type="module">` entry). Budget: ≤ 500 KB gzipped.
  - **Per-route lazy chunk** = a JS chunk with `isDynamicEntry: true` (emitted by a `React.lazy`/dynamic `import()` route split). Budget: ≤ 200 KB gzipped each.
  - **Other JS** (non-entry shared chunks split out by Rollup, and `?worker` bundles such as `forceWorker-*.js` that are instantiated via `new Worker()` and are not referenced from `index.html`) is **measured and reported for visibility but not gated** — the two budgets above are defined only for HTML entry and route-lazy chunks. A future budget for worker/shared chunks is a separate SOW.
- Invocation: `node frontend/scripts/check-bundle-size.js [distDir]` (default `distDir` = `frontend/dist`). The optional arg lets the self-test point the gate at a synthetic fixture dir. Thresholds are named constants in the script.
- **Fail-closed (no silent pass):** a missing or empty `distDir`, a missing/invalid `.vite/manifest.json`, or zero JS chunks in the manifest each exit non-zero. The gate never certifies "within budget" without measuring real chunks.
- Self-tested by `frontend/scripts/check-bundle-size.test.sh` (synthetic fixture dirs with high-entropy/incompressible JS so the gzipped budgets are actually exercised): a main chunk over 500 KB gz fails, a lazy chunk over 200 KB gz fails, all-under-budget passes, and a missing/empty dist fails.
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
   literal is committed in the scanner**. The scan is **fail-closed**: if no
   identity can be derived (and, when the repo has commits, if `git log` fails
   unexpectedly) it exits non-zero rather than running with Rule 1 disabled or on a
   partial ban-list. Name matching is word-bounded so unrelated tokens (e.g.
   `cost_usd`) never match.
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

- **Threshold:** zero hits.
- **Fail-closed in CI.** The `gates` job runs the scanner and **fails when the
  script is absent** — a missing scanner is a missing gate, not a pass. (Only the
  genuinely-optional aggregate `scripts/gates.sh` may be skipped when absent.)
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

- `scripts/spec-drift.sh` (planned, SOW-0013; manual spec↔code audit until it lands) will lint common drift indicators:
  - REST endpoints registered in `internal/presenter/` vs. `specs/rest-api.md`.
  - SSE event types in `internal/presenter/sse.go` vs. `specs/sse-protocol.md`.
  - SQLite columns in `internal/store/migrations/` vs. `specs/data-model.md`.
  - Canonical event fields in `internal/canonical/events.go` vs. `specs/canonical-events.md`.
  - Adapter probes in `internal/ingest/discover.go` vs. `specs/adapter-<name>.md`.
- Threshold: zero drift.

### Build

- `./scripts/build.sh` builds frontend, embeds, builds both Go binaries.
- Threshold: clean build, both binaries present, sizes within expected range.

### Mutation Testing (Recommended, Not Per-Commit)

- `go-mutesting ./internal/...` quarterly on critical paths (`internal/canonical`, `internal/ingest`, adapters).
- Surviving mutants are treated as test gaps and filed as SOWs.

## Aggregate Scripts

CI enforces every gate above as a **dedicated job** (`lint`, `test`, `frontend`,
`embed-smoke`, `gates`) that invokes the tools directly — see
`.github/workflows/ci.yml`. The implemented helper scripts today are
`scripts/build.sh` (frontend + embed + both binaries), `scripts/dev.sh`,
`scripts/embed-smoke.sh`, `scripts/e2e-serve.sh`, `scripts/sanitize-fixture.sh`,
`scripts/scan-secrets.sh` (+ `scripts/test/scan-secrets-test.sh`),
`scripts/scan-ai-attribution.sh`, and `scripts/install-systemd-user.sh`.

`scripts/lint.sh` (SOW-0009) **is present**: it is the local mirror of the
CI Go lint + security gates — `golangci-lint run` (the umbrella, driven by
`.golangci.yml` at the version pinned in `.golangci-lint-version`) then
standalone `gosec` and `govulncheck`. CI enforces the same set via the
version-pinned `golangci/golangci-lint-action` plus its standalone
gosec/govulncheck steps (CI keeps the cached action rather than invoking
`scripts/lint.sh`, to preserve golangci's analysis cache). `scripts/test.sh` (Go
tests + coverage profile) and `scripts/check-coverage.sh` (the statement
coverage gate) exist (SOW-0010). The single-command aggregator `scripts/gates.sh`
(runs *every* gate, fail-fast) is a planned convenience (SOW-0013) and is **not
yet present**. Until it lands, run the individual scripts/gates (or rely on the
per-gate CI jobs). The `gates` CI job detects `scripts/gates.sh` and skips it
gracefully when absent. The one exception is the security scanner: it is
**fail-closed** (§Secrets + Operator-PII Scan) — CI fails the `gates` job if
`scripts/scan-secrets.sh` or its self-test is missing, never skips it.

## Performance Target

When the `scripts/gates.sh` aggregator lands, a full local run should complete in
under 5 minutes on the operator's workstation; if it exceeds, profile and
parallelize before adding more gates. Today the equivalent is the set of CI jobs,
which run in parallel.

## When a Gate Fails

1. Read the failure output. Do not guess.
2. Reproduce locally if it was CI-only.
3. Fix the root cause in code, test, or spec.
4. Never weaken the gate. Lowering thresholds, suppressing warnings, or skipping tests is a contract breach.
5. If the gate itself is wrong (e.g. a new false-positive lint rule), file a SOW to update the gate config with evidence.
6. If the gate must be temporarily relaxed (extreme cases): the SOW must include a `Gate Suppression` section with reason, scope, expiry date, and the issue tracking restoration.

## Adding or Removing Gates

- **Add**: when a class of bug or risk would not have been caught by existing gates, design a new gate. Update this spec + the runtime skill + CI (and `scripts/gates.sh` once that aggregator exists) in the same commit. Update `AGENTS.md` if it adds a top-level commitment.
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
