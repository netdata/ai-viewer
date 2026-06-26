---
name: project-quality-gates
description: Catalog of every automated quality gate ai-viewer enforces — commands, thresholds, and what to do when a gate fails. Use before claiming any work done, before any commit, when adding a new gate, or when investigating a CI failure. The runtime companion to .agents/sow/specs/quality-gates.md.
---

# Quality Gates

## Operating Rule

Every gate listed here runs in CI on every push — except the benchmark regression gate (`scripts/check-bench.sh`), which is a local/workstation gate (its baseline is not comparable to CI-runner hardware; CI runs the bench compile-smoke + the gate's hardware-independent self-test). The assistant runs them all locally before reporting work done. If a gate fails: fix the root cause. **Never weaken a gate to make it pass.** Lowering a threshold or marking a test skipped to land a PR is a contract breach.

**nolint policy** (two cases, both require a reason — never a bare `//nolint`):

- **Deferred-fix suppression** (the finding is real but fixing it now is out of scope): forbidden unless the active SOW justifies it AND the directive links the tracking issue/SOW — `//nolint:rule // <reason>; see SOW-XXXX`.
- **Permanent-architectural suppression** (the finding is a verified false-positive for a deliberate, durable pattern — e.g. `nilerr` on a `return nil` that intentionally lets a stricter downstream check win, or on poll-loop rotation-tolerance): allowed with a `//nolint:rule // <reason>` stating *why the pattern is correct*. No tracking link is required because there is nothing to fix later. The reason must be specific; "false positive" alone is not enough. Prefer a config-level exclusion (with a YAML rationale comment) when a whole file or rule class is involved. Bare/unexplained `//nolint` is always a contract breach.

When a new pattern emerges that warrants enforcement, add it here AND to `.agents/sow/specs/quality-gates.md` AND to CI in the same commit.

## Gate Catalog

### Go — Module Tidiness

```bash
go mod tidy
git diff --exit-code go.mod go.sum
```

Threshold: zero diffs. `scripts/lint.sh` mirrors the CI `lint` job and runs
this before formatter/linter/security checks. A stale module graph is a local
gate failure, not a CI-only surprise.

### Go — Formatting

```bash
gofmt -l <tracked Go files>         # zero unformatted tracked files expected
goimports -l <tracked Go files>     # zero unformatted tracked files expected
```

Threshold: zero diffs. `scripts/lint.sh` runs standalone `gofmt` and
`goimports@latest` exactly like CI before `golangci-lint`; both surfaces derive
the input set from `git ls-files -z -- '*.go'`, so ignored/untracked local files
such as `frontend/node_modules/**` cannot cause local-only formatter failures.
An empty tracked Go file list is fail-closed. Golangci's formatters are defense
in depth. Auto-fix locally with `gofmt -w` and `goimports -w` before commit.
`scripts/test/lint-test.sh` is the hermetic self-test for the tracked-file
formatter contract; it plants ignored/untracked malformed Go and must fail if
the formatter gate regresses to walking `.`.

### Go — Vet

```bash
go vet ./...
```

Threshold: zero warnings.

### Go — Lint (golangci-lint)

```bash
golangci-lint run --timeout=5m      # umbrella: also enforces fmt + vet
```

golangci-lint is **v2** (`.golangci.yml` has `version: "2"`). `golangci-lint run` is the umbrella gate — with the formatters enabled it enforces Go — Format, and the `govet` linter covers Go — Vet, so a clean `run` means fmt+vet+lint all pass. (In golangci-lint v2, `golangci-lint run` REPORTS formatter violations — gofmt/goimports/gofumpt — as failures; `golangci-lint fmt` is the separate auto-fix path. `run` enforces formatting; it is not merely a linter-only pass. Note: `--enable-only <formatter>` is rejected because formatters are configured under `formatters:`, not enabled as linters — this does not mean `run` skips them.) Run it locally via `./scripts/lint.sh` after the module-tidiness, standalone `gofmt`, standalone `goimports@latest`, and `go vet` checks that mirror the CI `lint` job. The version is pinned in `.golangci-lint-version` (single source for CI + `scripts/lint.sh`); CI runs it via `golangci/golangci-lint-action` at that pinned version, and local `scripts/lint.sh` fails when the installed binary cannot be parsed or differs from the pin.

`.golangci.yml` enables linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `errorlint`, `gocritic`, `revive`, `gocyclo`, `misspell`, `nilerr`, `prealloc`, `unconvert`, `unparam`, `whitespace`, `bodyclose`, `noctx`; formatters: `gofmt`, `goimports`, `gofumpt`. NOT enabled: `gosimple` (v2 merged it into `staticcheck`) and `gosec` (runs standalone — see Go — Security — to avoid duplicate analysis).

Tuning (rationale in the `.golangci.yml` inline comments): `gocyclo` uses `min-complexity: 25` (the stream parsers/scanners/tailers and the SOW-0007 ingest event loop legitimately sit at 16–24; 15 is false-positive churn on reviewed hot-path code). `_test.go` is excluded from the style/complexity linters (`gocyclo`, `noctx`, `unparam`, `prealloc`, `revive`, `gocritic`) but NOT from the bug-finders. `frontend/node_modules` is path-excluded (a transitive npm dep ships a non-project Go file).

Threshold: zero warnings.

### Go — Security

```bash
gosec -severity medium -confidence medium ./...
govulncheck ./...
```

Threshold: zero high/critical findings from gosec; zero known vulnerabilities from govulncheck. Govulncheck runs in CI on a schedule plus every push so newly-disclosed CVEs surface fast.

**GOTCHA — standalone pinned `gosec` ≠ golangci's bundled gosec.** CI's "Go — Security" step and `scripts/lint.sh` both install and run a **pinned standalone** `gosec@v2.26.1`; that version ships newer analyzers (e.g. **G705** XSS-taint) that golangci-lint's older *bundled* gosec does not have (which is why `gosec` is NOT enabled as a golangci linter — it runs standalone). So `golangci-lint run` returning 0 does NOT mean `gosec` passes — they are different gates. Always run the standalone `gosec -severity medium -confidence medium ./...` locally (install the pin: `go install github.com/securego/gosec/v2/cmd/gosec@v2.26.1`, or just run `./scripts/lint.sh`, which installs/verifies the pinned gosec + runs golangci + govulncheck) AND `govulncheck ./...` before pushing — not just `gofmt`/`vet`/`golangci-lint`/`test`/`build`. (govulncheck exits 0 when a required-but-uncalled module has a CVE — "your code doesn't appear to call" — that is a pass.)

**Suppressions:** gosec honors the **hash** form `// #nosec G705 -- justification` (NOT `//nosec`). Per the Operating Rule, any `#nosec`/`//nolint` MUST be a verified false positive AND justified in the active SOW. Prefer restructuring to the gosec-clean pattern of a sibling handler over suppressing; suppress only when the finding is provably impossible (e.g. body is trusted embedded build output served on an exact-match route).

## Verifying CI Before PR Merge (do not trust `--watch` exit code)

When GA or explicit-PR flow is active, branch protection may still have no
required status checks before SOW-0013's post-merge setup runs. After setup, the
required contexts are recorded in `.github/workflows-checks.yaml` and
`docs/setup.md`. In both states, never gate a merge on
`gh pr checks <pr> --watch` alone: it fails only on required checks, so a
non-required red row can still leave `--watch` green. Before `gh pr merge`, run
`gh pr checks <pr>` and confirm EVERY row reads `pass` (no `fail`/`pending`).

### Go — Tests

```bash
go test -race -count=1 ./...
```

Threshold: all pass with race detector enabled. `-count=1` defeats test cache (CI must run fresh).

### Go — Coverage

```bash
go test -race -coverprofile=coverage.out -covermode=atomic ./...
go tool cover -func=coverage.out | tail -1
```

Thresholds:

- **Statement** coverage (Go has no native branch coverage; branch is deferred). Gated set = every `internal/*` package (gated **iff** the import path contains `/internal/` and not `/cmd/`): each ≥ 80% AND their aggregate ≥ 80%. Excluded (reported, not gated): `/cmd/` (binaries + nested dev tools) and any non-`internal/` path, e.g. vendored Go a frontend npm dep ships under `frontend/node_modules/`.
- New code in the PR: ≥ 90% lines — **deferred (SOW-0036)**, not yet enforced (see Enforcement note below).

Enforcement: `scripts/check-coverage.sh coverage.out` fails if any gated (`internal/*`) package or the gated aggregate is < 80% statements; it runs as a build-failing CI step (the `test` job) and as the local pre-commit gate, with synthetic-fixture self-tests (`scripts/test/check-coverage-test.sh`). New-code-in-PR ≥ 90% is deferred to a follow-up SOW (diff↔coverage intersector).

### Go — Fuzzing

```bash
go test -run='^Fuzz' ./internal/adapters/...                                    # per-push: seed corpus, deterministic
go test -run='^$' -fuzz='^FuzzParseSnapshot$' -fuzztime=5m ./internal/adapters/aiagent_v2/   # nightly: explore (one target per pkg)
```

Every adapter parser exposes at least one `FuzzXxx` target (10 across the 5 adapter packages). `internal/canonical` has **no** fuzz target — it owns no parsers/decoders. CI per-push runs the seed corpus deterministically (`-run='^Fuzz'`, PR-blocking); `fuzz-nightly.yml` runs `-fuzz -fuzztime=5m` per target (non-blocking, uploads any crash reproducer). Commit a found reproducer under `testdata/fuzz/<Target>/` to make it a deterministic per-push regression. Auto-filing a GitHub issue on crash is a deferred follow-up.

Threshold: zero crashes per run. A seed-corpus crash blocks merge.

### Go — Ingestion Parity

```bash
scripts/test/check-ingestion-parity-test.sh   # hermetic wrapper self-test
scripts/check-ingestion-parity.sh --fixtures  # source-vs-canonical parity fixture gate
go test -count=1 ./internal/parity ./internal/ingest ./cmd/ai-viewer-ingest -run 'Parity|Source|Manifest|Diff|Canonical|Matrix|CheckParity'
go test -run='^Fuzz' ./internal/parity       # deterministic parity fuzz seed corpus
```

SOW-0097 gate. The wrapper is the named local/CI fixture gate over the parity
source extractors, canonical extractor, diff engine, ingest parity tests,
matrix drift tests, `ai-viewer-ingest check-parity` CLI tests, and
`internal/parity` fuzz seeds. It exists so future agents run one durable gate
instead of remembering scattered package-level commands.

Threshold: exit zero only when every fixture parity check is clean. Missing
wrapper/script, invalid wrapper mode, missing required parity package, malformed
fixture, source extractor error, canonical extractor error, source/canonical
mismatch, unverifiable artifact, matrix drift or open matrix gap, parity fuzz
seed crash, or failing CLI mismatch test is a gate failure.

Live local diagnostic entry point:

```bash
ai-viewer-ingest check-parity --db /opt/ai-viewer/data/index.db --source <adapter:path> ...
```

The live full gate is workstation-only because it reads private local sources.
Do not publish live manifests or raw payload bodies. Until SOW-0097 closes, the
advanced live controls (streaming manifests, snapshot mutation detection,
resume, sample mode, timeout, memory cap) remain open requirements, not a reason
to claim full live parity done.

### Go — Benchmarks

```bash
scripts/check-bench.sh             # count=6, -cpu=1, -p=1, benchstat vs bench/baseline.txt, > 20% sec/op gate
scripts/test/check-bench-test.sh   # hardware-independent self-test of the gate's benchstat parser
```

Marked benchmarks (`func BenchmarkXxx`) exist for the 11 performance-critical paths: ai-agent v2 adapter `Scan`, ai-agent v2 adapter `Tail`, claude-code adapter `Scan`, claude-code adapter `Tail`, Codex adapter `Scan`, Codex adapter `Tail`, Opencode adapter `Scan`, Opencode adapter `Tail`, SQLite batch insert, REST query path, SSE fanout. (No canonical encode/decode benchmark — canonical events are constructed directly, never serialized.)

Threshold: a statistically-significant **> 20% sec/op** regression for any individual benchmark fails `scripts/check-bench.sh` (the `geomean` aggregate + custom `ReportMetric` values are not gated; benchstat's `~` neutralizes noisy benchmarks). In real local/workstation mode, the same benchmark name must regress on the script's second benchmark attempt before the gate exits red; a first-run-only regression, or disjoint first/second-attempt regression sets, is reported as local measurement noise and exits green. The retry is not a threshold change: both attempts compare against the same checked-in `bench/baseline.txt` with the same `-count=6`, `-cpu=1`, `-p=1`, and parser, while compare-file mode remains single-pass so the self-test still proves a real >20% regression exits non-zero. The script runs the serial hot-path suite with `go test -p=1 -run=^$ -bench=. -benchmem -count=6 -cpu=1`; do not remove `-cpu=1` or `-p=1` unless a later explicit SOW proves the suite should measure multi-P or cross-package scheduler behavior. `-cpu=1` pins the benchmark binary's Go benchmark CPU list; `-p=1` serializes package benchmark binaries so the gate does not create cross-package contention. Real benchmark attempts fail closed with exit 2 before sampling when the 1-minute host load is at or above `0.50 * effective_GOMAXPROCS`, when `/proc/loadavg` is unavailable or malformed, or when the effective CPU divisor cannot be resolved. Passing and failing runs emit compact diagnostics (Go version, effective `GOMAXPROCS`, benchmark CPU setting, package parallelism, busy-host threshold, package list, baseline/current paths, and load averages) without process command lines. It is a **local/workstation** gate — `bench/baseline.txt` is workstation-measured (carries benchmark-code provenance: an implementing commit SHA when available, or a same-commit `git blame` note when benchmark code and baseline land together, plus the exact command and `goos/goarch/pkg/cpu` config lines) and is not comparable to GitHub-runner hardware, so CI runs only the bench compile-smoke + the gate self-test, not the regression gate itself. Baseline refresh requires an explicit SOW (no auto-update); SOW-0058 owns the original `-cpu=1` baseline refresh.

Fail-closed benchmark-gate behavior is part of the contract: missing, empty, or
benchmarkless baseline, missing/empty current output, failed `go test`, failed
`benchstat`, dropped/renamed baseline benchmark, disjoint benchmark config
groups, busy-host preflight failure, unavailable/malformed loadavg source, and
unavailable effective CPU divisor all exit non-zero. The self-test must
dynamically exercise both compare-file parser behavior and real-mode
retry/error behavior with hermetic fakes, including disjoint first/second-attempt
regression sets and busy-host preflight cases; static string assertions alone
are not enough for the retry path.

CI's `Require benchmarks` compile-smoke presence check must normalize both
suffixed Go benchmark rows (`BenchmarkName-N`) and unsuffixed `-cpu=1` rows
(`BenchmarkName`) from `bench/baseline.txt`, then compare those logical names
against the implemented `func BenchmarkXxx` set. The benchmark self-test must
assert this parser contract, because a workstation baseline refresh can
otherwise pass local gates and fail only in CI.

Gated benchmarks must isolate the intended hot path from helper-goroutine
scheduler noise. For serial production paths (for example SSE `Hub.Deliver`
fanout), use deterministic buffering/pre-seeding in the fixture instead of
background helper goroutines inside the timed environment; otherwise the local
workstation gate can fail on unchanged code under ordinary desktop/VM load.
For very small serial hot paths, a benchmark operation may run a deterministic
fixed batch to amortize timer/scheduler noise, but it must keep reporting the
per-hot-path-unit metric (for example deliveries/sec) for human interpretation.

### Go — Race + Stress

```bash
go test -race -count=10 ./...       # local pre-commit on concurrency-touching changes
```

For changes to ingest pipeline, SSE hub, or anything with channels/goroutines: run `scripts/test.sh --stress 10` locally; CI runs `-count=1` per push (the `test` job) and `-count=10` race stress nightly (`race-stress-nightly.yml`).

### Frontend — Lint

```bash
cd frontend && npm run lint   # the `lint` script bakes in --max-warnings=0
```

ESLint flat config with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import`. Threshold: zero warnings (the `lint` npm script owns `--max-warnings=0`; `scripts/lint.sh` and CI run plain `npm run lint`).

### Frontend — Type Check

```bash
cd frontend && npm run typecheck    # invokes `tsc --noEmit`
```

`tsconfig.json` enforces: `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`, `noUnusedLocals`, `noUnusedParameters`.

Threshold: zero errors.

### Frontend — Unit/Component Tests

```bash
cd frontend && npm run test -- --run --coverage          # REAL gate (run by scripts/test.sh after the Go suite, and in CI)
cd frontend && npm run check:coverage-config              # verify the REAL per-dir floors (RAW-canonical shape + non-vacuity + BIDIRECTIONAL lockstep + disk-completeness + whole-dir include shape)
cd frontend && npm run check:coverage-thresholds:selftest # hermetic gate-MECHANISM self-test (throwaway fixture)
```

Vitest + React Testing Library. Threshold: all pass; global aggregate floor (≥ 80% lines/stmts/funcs, ≥ 75% branches) **plus a per-directory ≥ 80% lines floor** for every measured dir under `src/components/` and `src/pages/`.

- **Per-dir mechanism = Vitest NATIVE glob-keyed `coverage.thresholds`** (SOW-0012; Vitest ≥ 4, verified on 4.1.7) — `vitest.config.ts` has `'src/components/<Dir>/**': { lines: 80 }` per measured dir. A group below 80% lines fails the run (exit 1: `ERROR: Coverage for lines (NN%) does not meet "<glob>" threshold (80%)`). NO wrapper script; a shared `PER_DIR_LINES` const ties the global + per-dir floors together.
- **Shared lists (F3):** `PER_DIR_GLOBS`, `COVERAGE_INCLUDE`, `COVERAGE_EXCLUDED`, `PER_DIR_LINES` live in `frontend/vitest.coverage.mjs`; `vitest.config.ts` imports the measuring lists and `check-coverage-config.mjs` imports all of them (no second copy to drift). `.mjs` (no TS loader for the Node verifier) + a co-located `vitest.coverage.d.mts` for the config's typecheck.
- **Gotcha — empty glob group vacuously PASSES:** an unmatched glob's lines pct is `"Unknown"` and `"Unknown" < 80` is `false`. So add a per-dir key ONLY for a dir in `coverage.include`, AND every measured component/page dir MUST have a per-dir key — the gated set and the measured set are EQUAL (`gatedDirs === measuredDirs`), enforced both ways. When you implement+test a new component/page dir, add it to BOTH `COVERAGE_INCLUDE` AND `PER_DIR_GLOBS` in `vitest.coverage.mjs`, else the per-dir gate silently skips it (or, the reverse, a floor for an unmeasured/excluded dir is a no-op that can never fire). Dirs/files that are intentionally NOT measured go in `COVERAGE_EXCLUDED` (in `vitest.coverage.mjs`) with a per-entry rationale: `Layout` + `StatCard` are REAL components whose dedicated Vitest unit coverage is deferred (Playwright exercises them today) — NOT placeholders; the `Agents`/`Models`/`Tools` Phase-3 stubs are bare `<ComingSoon/>` wrappers; `NotFound.tsx` is the trivial 404. (`ComingSoon.tsx` itself IS measured — it has a unit test.) **`npm run check:coverage-config` catches THREE things** on the REAL lists, failing closed and naming the offender: (1) **non-vacuity** — a per-dir glob matching zero source files; (2) **BIDIRECTIONAL lockstep** — a measured dir with no per-dir floor (measured ⊄ gated) AND a per-dir floor whose dir is not measured (excluded or absent from include; gated ⊄ measured — a no-op threshold group, R8-2); (3) **disk-completeness** — a source dir/flat-file under `src/components/`/`src/pages/` in NEITHER `COVERAGE_INCLUDE` nor `COVERAGE_EXCLUDED` (so shipped source cannot silently escape both coverage and the verifier). It also fails closed on a broad whole-root include glob (e.g. `src/pages/**/*.{ts,tsx}`), on a per-dir include that is **not the EXACT canonical whole-dir shape** `<root>/<Dir>/**/*.{ts,tsx}` (a bare dir, an extension-narrowed `**/*.tsx`/`**/*.ts`, a narrow filename, or a deeper subpath — all reject, so a sibling source cannot escape a "measured" dir), on a **`PER_DIR_GLOBS` entry that is not the EXACT canonical threshold shape `<root>/<Dir>/**`** (a bare dir like `src/components/Foo`, or a deeper/narrower glob — a Vitest threshold KEY must end in `/**` to match file paths, so a bare-dir key matches nothing and that floor vacuously passes; only a canonical entry contributes its dir to the gated set), and on a malformed `.`/`..` path segment. **Both per-dir-root shape checks compare the RAW string, not a normalized one (R8-1):** `vitest.config.ts` hands the RAW strings to Vitest (`PER_DIR_GLOBS` → picomatch threshold keys vs clean `relative(root,file)` paths; `COVERAGE_INCLUDE` → tinyglobby selector), so an entry that is canonical only AFTER normalization (a leading `./`, a repeated `//`, or a trailing `/`) is REJECTED — a `//`/trailing-`/` key matches nothing (vacuous floor) and the `./` form is version-fragile. So write per-dir-root entries EXACTLY canonical: `src/components/<Dir>/**` and `src/components/<Dir>/**/*.{ts,tsx}` — no `./`, no `//`, no trailing `/`.
- HTML report at `frontend/coverage/` (CI artifact
  `frontend-coverage-<run_id>`); `json` reporter emits
  `coverage/coverage-final.json`; `lcov` reporter emits `coverage/lcov.info`
  for Codacy coverage upload.
- **Two guards, do not conflate:** the MECHANISM self-test (`scripts/check-coverage-thresholds.test.sh`) proves Vitest's glob-keyed threshold still fails closed on a throwaway 50%-lines fixture — it does NOT read the real config. The real-config verifier (`scripts/check-coverage-config.mjs`) enforces the real lists' non-vacuity + BIDIRECTIONAL lockstep (`gatedDirs === measuredDirs`) + disk-completeness + RAW-canonical per-dir-root shapes (per-dir include exactly `<root>/<Dir>/**/*.{ts,tsx}`, per-dir floor exactly `<root>/<Dir>/**`, both as the RAW string — no `./`/`//`/trailing-`/` laundering); its OWN decision logic has a hermetic self-test (`scripts/check-coverage-config.test.sh`, `npm run check:coverage-config:selftest`). All are dedicated CI `frontend` steps and run in `scripts/lint.sh`.
- A dir under the floor is a finding to fix with tests — never lower the threshold.

### Frontend — E2E

```bash
cd frontend && npm run e2e
```

Playwright headless. One scenario per primary user flow at minimum: sessions list filter, session detail load, sources panel, real-time update via SSE, theme toggle.

Default bind is `127.0.0.1:7710`. For local workstations where the installed
ai-viewer service already owns that port, set `AI_VIEWER_E2E_PORT=<free-port>`;
`scripts/gates.sh` auto-selects a fallback for the aggregate gate. Keep
`reuseExistingServer: false`: E2E must test the freshly seeded server, never an
arbitrary existing process or live DB.

Threshold: all pass. Flaky tests are quarantined into a separate suite with a linked SOW to fix; never marked `test.skip`.

### Frontend — Accessibility

```bash
cd frontend && npm run e2e:a11y     # Playwright + @axe-core/playwright
```

axe-core runs on every Playwright route. Threshold: zero serious or critical violations.

### Frontend — Bundle Size

```bash
cd frontend && npm run build
npm run check:bundle-size            # node scripts/check-bundle-size.js (defaults to ./dist)
npm run check:bundle-size:selftest   # synthetic-fixture self-test of the gate
```

Enforced gate (SOW-0012), not a report. Classification is **manifest-driven**: `vite.config.ts` sets `build.manifest: true`; the gate reads `dist/.vite/manifest.json` and gates by Vite's `ManifestChunk` flags — `isEntry` chunks are the **main chunk** (≤ 500 KB gz), `isDynamicEntry` chunks are **per-route lazy chunks** (≤ 200 KB gz each). **Each MAIN/LAZY entry's budget is its transitive static-import CLOSURE** — the gz sum of the entry's `.file` PLUS the transitive closure of its static `imports` (deduped within one entry's closure; `dynamicImports` are separately-budgeted lazy chunks and are NOT followed). So a Rollup-split shared chunk (neither `isEntry` nor `isDynamicEntry`, reachable only via an entry's `imports[]`) IS gated under the entry that pulls it in (the F1 fail-open this closed). Only JS that is neither classified nor inside any gated entry's closure (e.g. a `?worker` bundle like `forceWorker-*.js` absent from the manifest) is reported "ungated", never gated. The script takes an optional dist-dir arg (default `./dist`) so the self-test can point it at a fixture. **Fail-closed — the complete case list (each exits non-zero, never a silent pass):** a missing/empty dist; a missing/invalid/non-object manifest (including a JSON array); zero JS chunks; no MAIN (`isEntry`) entry; **any** manifest entry (JS **or** non-JS, e.g. CSS) whose `file` is absent on disk (an up-front sweep over every chunk); a JS chunk flagged **both** `isEntry` **and** `isDynamicEntry` (mutually exclusive — a both-flagged route chunk would be under-budgeted as MAIN); a non-array `imports` **or** `dynamicImports`; a non-string element in an `imports` array; a static `imports` element referencing a missing/invalid manifest key; a non-string element in a `dynamicImports` array; a `dynamicImports` element referencing a missing manifest key; and a `dynamicImports` element referencing a **JS** chunk that is **not** `isDynamicEntry`. (The last three close the lazy-chunk half of the contract: a mis-flagged/dangling dynamicImport target would otherwise slip to "ungated" instead of being budgeted as the lazy chunk it is.) Thresholds are named constants in the script. Exceeding a budget requires a SOW — never raise the threshold.

### Secrets Scan

```bash
scripts/scan-secrets.sh             # scans every tracked file for secrets/operator identity
scripts/test/scan-secrets-test.sh   # hermetic self-test for rule classes + fail-closed paths
```

Patterns checked:

- Operator identity, derived at runtime from git/user metadata and `$HOME`; no
  literal operator identity is committed in the scanner. Synthetic placeholder
  commit identities used only to avoid personal commit metadata (currently exact
  neutral `user` placeholders plus the matching placeholder email form,
  case-insensitive) are ignored as Rule-1
  derivation inputs. The exact neutral home stem `user` is also ignored when it
  comes only from `$HOME`, so committed placeholder paths such as
  `/home/user/...` stay portable across generic dev VMs. Tracked content is
  still scanned normally; real derived operator identities are never
  allow-listed. After placeholder filtering, an empty Rule-1 ban-list is a
  fail-closed error. This rule is never allow-listed.
- Generic secret shapes: `sk-…` / `sk-ant-…`, Slack `xox…`, AWS `AKIA…`,
  bearer tokens, GitHub PATs, GitHub fine-grained PATs, and GitLab PATs.
  Secret-shape tokens are flagged in every tracked file, including fixtures; the
  only exemption is a per-token synthetic `EXAMPLE` marker used by dirty
  sanitizer fixtures.
- `.gz` archives are decompressed and scanned; malformed archives are scanned
  raw and reported. Tracked symlinks are scanned as target-path strings.
- Diagnostics are redacted: failure output reports path, line, rule, and a
  redacted/summary marker only; it never prints the raw matched operator
  identity, offending line, or secret token.

Threshold: zero hits.

### AI-Attribution Scan

```bash
scripts/scan-ai-attribution.sh
```

Greps `cmd/`, `internal/`, `scripts/`, `frontend/src/`, and `frontend/tests/`
for comments that attribute code to an external AI reviewer by name — a reviewer
name adjacent to an iteration/priority tag (`<name> iter-N`, `<name> P<digit>`)
or an attribution verb (`per`/`pins <name>`, `<name> flagged/…`). The pattern
REQUIRES a reviewer name, so bare domain terms usually do not match; the script
self-excludes (it enumerates the names in its own pattern). Enforces the
no-AI-attribution rule on the public repo — the work stands on its own. Runs
fail-closed in local `scripts/gates.sh` and the CI `gates` job: required scan
roots must exist, grep exit 0/1 are handled explicitly, and grep exit >1 fails.

Threshold: zero hits.

### Spec Drift

```bash
scripts/test/check-contract-matrix-test.sh # hermetic self-test for the field-contract helper
scripts/spec-drift.sh              # the 6 spec↔code drift indicators; exit 0 = no drift
scripts/test/spec-drift-test.sh    # hermetic self-test: plants each drift class + a clean case
```

Fail-closed (exits non-zero naming the offending indicator + token). Five
indicators are grep/awk-based because their surfaces are line-oriented; the
contract-matrix indicator delegates to a small Go helper for Go AST JSON tags
and restricted TypeScript interface parsing. Six indicators, the
**actual** code/spec locations (the old skill named stale paths —
`internal/presenter/sse.go`, `internal/ingest/discover.go` — neither exists):

- **REST endpoints** — `mux.HandleFunc("/api/…", p.<handler>)` in `internal/presenter/presenter.go` plus each handler's in-body `r.Method` guard in the presenter package, vs. `### <VERB> /api/…` headings in `.agents/sow/specs/rest-api.md`. The compared token is `<VERB> <normalized-path>`, not path alone; `{id}`↔`:id`, single-value `:ref`↔`:id`, and catalog groups are normalized. `HEAD` is implicit parity for `GET`. Keep registered handler method gates directly visible as `r.Method != http.Method...` comparisons inside the handler body; extracting the guard into a helper is treated as method-extraction failure and correctly fails closed. **Exemption:** a spec endpoint whose section is marked *not registered / Phase 2 / not implemented* is NOT drift — documenting a future route ahead of its handler is allowed; every *registered* route+verb pair must be documented (code→spec unconditional).
- **SSE event types** — the wire kinds in `internal/presenter/events_sse.go` (`eventPayload` `case "<kind>"` arms + `event:`/control frames; `stats_invalidated` is the `default` arm, sourced from `subscription_filter.go`) vs. the event-type headings in `.agents/sow/specs/sse-protocol.md`. `resync` is a reconnect-control frame (spec §Reconnect Behavior), treated as known.
- **SQLite columns** — every `table.column` pair from `internal/store/migrations/*.sql` (`CREATE TABLE`, `CREATE VIRTUAL TABLE … fts5(…)`, `ALTER … ADD COLUMN`) vs. SQL schema blocks in `.agents/sow/specs/data-model.md`. Column direction is **code→spec** and table-scoped: a global mention of the same column name under another table is not enough. Table names are bidirectional.
- **Canonical event kinds** — `EvXxx EventKind = "<value>"` in `internal/canonical/events.go` vs. the identical fenced block in `.agents/sow/specs/canonical-events.md`. Bidirectional, exact.
- **Adapter discovery probes** — `format: "<name>"` probe structs in `cmd/ai-viewer-ingest/sources.go` vs. the matching `.agents/sow/specs/adapter-<name>.md` (underscore→hyphen) and that spec mentioning the default probe path.
- **Contract matrix** — `testdata/contracts/field-matrix.yaml` vs. presenter JSON tags in `internal/presenter/*.go`, API TypeScript interfaces in `frontend/src/api/types.ts`, `test_refs`, include-token policy, and payload-kind `artifact_class` normalization.

Threshold: zero drift — via genuine agreement, never by weakening an indicator. Real drift on the live tree is reported + adjudicated (fix spec or code), not masked.

CI fail-closed rule: `.github/workflows/ci.yml` must fail if
`scripts/spec-drift.sh`, `scripts/test/spec-drift-test.sh`,
`scripts/check-contract-matrix.sh`, or
`scripts/test/check-contract-matrix-test.sh` is absent. The `gates` job runs
the contract-matrix self-test and spec-drift self-test **before** the live
detector, matching `scripts/gates.sh`; a detector that cannot catch planted
drift is a failed gate, not an all-clear.

### Build

```bash
./scripts/build.sh
```

Builds frontend, embeds, builds both Go binaries. Threshold: clean build, both binaries present, expected size range.

### Mutation Testing (optional, recommended)

```bash
go-mutesting ./internal/...         # quarterly or on critical paths
```

Mutation testing surfaces tests that pass even when the code is broken. Not enforced per-commit; run quarterly on critical paths (`internal/canonical`, `internal/ingest`, adapters) and treat surviving mutants as test gaps to file as SOWs.

## Aggregate Scripts

```bash
./scripts/lint.sh         # build-free module/static analysis: Go module tidy + tracked-file formatters + vet + golangci + gosec + govulncheck AND frontend eslint/tsc/bundle-size/coverage self-tests; fail-fast; frontend section skipped when frontend/ is absent
./scripts/test.sh         # ALL tests: Go (race + coverage profile) then, in normal mode, the frontend Vitest run (real per-dir coverage gate); skips frontend when absent (SOW-0010/0012; EXISTS)
./scripts/build.sh        # frontend build + the REAL bundle-size gate on the built dist/ + embed + both Go binaries (SOW-0012; EXISTS)
./scripts/check-coverage.sh  # Go statement coverage gate, internal/* >= 80% (SOW-0010; EXISTS)
./scripts/check-contract-matrix.sh # DB/API/TypeScript/UI field-contract matrix helper (SOW-0105; EXISTS)
./scripts/spec-drift.sh   # the 6 spec↔code drift indicators, fail-closed (SOW-0013/SOW-0105; EXISTS)
./scripts/check-ingestion-parity.sh --fixtures # deterministic source-vs-canonical parity fixture gate (SOW-0097; EXISTS)
./scripts/test/codacy-config-test.sh  # Codacy tool/pattern + path policy self-test (SOW-0046; EXISTS)
./scripts/gates.sh        # every gate above, in order, fail-fast, per-section timing (SOW-0013; EXISTS)
```

`scripts/lint.sh` is the build-free module/static-analysis entrypoint: it runs Go module tidiness, tracked-file Go formatter checks, Go vet/lint/security checks, frontend static checks, the coverage-config verifier, and hermetic gate-logic self-tests. It does NOT run the REAL bundle-size-vs-built-manifest gate (`npm run check:bundle-size`, needs a `vite build`) or the REAL coverage run (`npm run test -- --run --coverage`); those live in the CI `frontend` job and in `scripts/build.sh` (which runs `check:bundle-size` on the just-built `dist/`) and `scripts/test.sh` (which runs the frontend `npm run test -- --run --coverage` after the Go suite, in normal mode). `scripts/lint.sh`'s frontend section ensures `frontend/node_modules` is present by reusing `scripts/build.sh`'s `npm ci` / `npm install` fallback, but only when it is missing (so a warm tree stays fast).

Current state: `scripts/lint.sh`, `scripts/test.sh`, `scripts/check-coverage.sh`, `scripts/spec-drift.sh`, `scripts/check-contract-matrix.sh`, `scripts/check-ingestion-parity.sh`, `scripts/codacy-coverage-upload.sh`, `scripts/test/lint-test.sh`, `scripts/test/check-contract-matrix-test.sh`, `scripts/test/check-ingestion-parity-test.sh`, `scripts/test/codacy-coverage-upload-test.sh`, `scripts/test/codacy-config-test.sh`, `scripts/test/install-system-test.sh`, and the canonical `scripts/gates.sh` aggregate all exist (SOW-0009/0010/0012/0013/0044/0046/0097/0105). `scripts/gates.sh` is the full local workstation gate: fail-fast, per-section timing, and complete coverage of the local gate catalog including the lint formatter-scope self-test, contract-matrix self-test, ingestion parity fixture gate, Codacy coverage-upload self-test, Codacy config self-test, install-system self-test, fuzz seed corpus, Playwright E2E/axe, and the local benchmark regression gate. The benchmark regression gate runs immediately after the cheap lint formatter-scope self-test and before lint/static analysis, build, `-race`, fuzz, and Playwright sections so the local workstation benchmark measures code behavior, not aggregate-created residual load. CI enforces equivalent gates as dedicated parallel jobs (`lint` via the same module-tidiness + standalone gofmt/goimports/vet checks, the pinned `golangci-lint-action`, and standalone gosec/govulncheck; `test`; `frontend`; `embed-smoke`) plus a cross-cutting `gates` job (secrets/spec-drift/contract-matrix/ingestion-parity/lint-test/Codacy coverage-upload self-test/Codacy config self-test/install-system self-test/AI-attribution fail-closed, contract-matrix, spec-drift, and ingestion-parity self-tests before live detectors, local aggregate presence + syntax check, optional systemd helper) and explicit CodeQL matrix jobs — see "Local Pass + CI Fail Invariant" below.

Required CI jobs are **not** bootstrap probes anymore. They fail closed when
their mature repo prerequisites disappear: `lint`/`test` require Go module files
and their gate scripts; `frontend` requires frontend package files, Go module
files, `scripts/build.sh`, and the `e2e` npm script; `embed-smoke` requires the
build/smoke/seed-marker scripts plus Go/frontend package files. Do not re-add
green "skip notice" paths for these gates. Only optional helper checks such as
systemd unit lint may skip when their helper file is absent.

## Local Pass + CI Fail Invariant

`scripts/gates.sh` and `.github/workflows/ci.yml` enforce the **same gate contract**, but CI keeps expensive gates parallel instead of re-running the full local serial aggregate. So a green local `gates.sh` followed by a red CI is an *investigatable defect*, never accepted drift and never "just re-run CI". The deliberate, documented divergences are narrow: CI's `lint` job uses the version-pinned `golangci/golangci-lint-action` (cache) at the **same** pin as `scripts/lint.sh` (`.golangci-lint-version`); the benchmark regression gate is local-only (workstation baseline) while CI runs only bench compile-smoke + gate self-test; and the CI `gates` job runs the cross-cutting scans plus required-script presence/syntax checks rather than the full aggregate. Anything else that disagrees is environment (Go/Node version, OS), test isolation (stale cache, leftover state), or timing — debug from the asymmetric input. Before merge, also confirm EVERY CI row reads `pass` (see "Verifying CI Before Merge" — `--watch` can exit 0 on a failed non-required check).

## Renaming a CI Job

The `ci.yml` job IDs (`lint`, `test`, `frontend`, `embed-smoke`, `gates`, plus each explicit CodeQL matrix job name: `CodeQL (go)`, `CodeQL (javascript-typescript)`, `CodeQL (actions)`) are the branch-protection **required-status-check** contract. The current names are recorded in `.github/workflows-checks.yaml` (operator-readable, NOT consumed by Actions). Renaming a job silently disables its required check (protection keys by name). So any SOW that renames a job MUST, in the **same commit**: (1) rename it in `ci.yml` or `codeql.yml`, (2) update `.github/workflows-checks.yaml`, and (3) re-run the full branch-protection `gh api -X PUT …/branches/master/protection` invocation documented in `docs/setup.md`. GitHub's `PATCH` endpoint is only for the nested `/protection/required_status_checks` update, not the full protection rule. This is the §"Adding a New Gate" rule's sibling for renames.

## Code Scanning Defence Layer

### CodeQL

Runtime checks:

```bash
actionlint .github/workflows/codeql.yml
gh run list --workflow codeql --branch master --limit 5
gh api /repos/netdata/ai-viewer/code-scanning/alerts --jq 'map(select(.state=="open")) | length'
```

Policy:

- Required contexts are `CodeQL (go)`, `CodeQL (javascript-typescript)`, and
  `CodeQL (actions)`.
- CodeQL config lives under `.github/codeql/` once SOW-0044 lands. Query-suite
  changes are SOW changes, not drive-by workflow edits.
- Suppressions are query/path scoped in the config file with a SOW/issue
  rationale. Inline `// codeql[...]` suppressions require explicit SOW evidence.
- Critical/high alerts are fixed, proven false-positive, or tracked before a SOW
  closes.

### Codacy

Runtime checks:

```bash
codacy repository gh netdata ai-viewer --output json
codacy tools gh netdata ai-viewer --output json
codacy issues gh netdata ai-viewer --overview --output json
codacy findings gh netdata ai-viewer --severities Critical,High --output json
codacy issues gh netdata ai-viewer --branch master --severities Critical,High --output json
codacy-analysis discover --output-format json --output /tmp/ai-viewer-codacy-discover.json
codacy-analysis analyze --inspect --output-format json
codacy-analysis analyze --install-dependencies --output-format json --output /tmp/ai-viewer-codacy-analysis.json
scripts/test/codacy-config-test.sh
```

Policy:

- Codacy starts as a measured reporting/tuning surface unless an active SOW
  explicitly promotes a tuned context to branch protection.
- Local config under `.codacy/` may be committed as a measured reporting/tuning
  baseline before it is high-signal, but it must not become a required context
  until critical/high findings and noisy patterns are triaged. Generated tool
  material under `.codacy/generated/` stays ignored.
- Codacy Cloud path exclusions live in repository-root `.codacy.yml`. Keep that
  surface separate from the Cloud-imported tool/pattern config in
  `.codacy/codacy.config.json`. Because Codacy ignores UI ignored-file settings
  when `.codacy.yml` exists, root `exclude_paths` must carry the repository-wide
  non-runtime SOW work-ledger, duplicate instruction symlink, generated artifact,
  dependency, coverage, build-output, local binary-output, and local
  test-output exclusions explicitly. The local Analysis CLI consumes the JSON
  config but not `.codacy.yml`, so root YAML exclusions are mirrored only in the
  JSON top-level `exclude` list; tool-scoped YAML exclusions such as
  `engines.eslint-8.exclude_paths` are mirrored only into that tool's JSON
  `exclude` array.
- Codacy exclusion rationale must name the actual replacement gates. Frontend
  tests and test support may cite native ESLint/TypeScript/Vitest/Playwright
  where applicable; standalone frontend scripts may cite their dedicated
  self-tests/build integration and repository-wide secrets/spec-drift gates when
  the frontend ESLint/TypeScript configs intentionally ignore `scripts/`.
- Cloud import through `codacy tools gh netdata ai-viewer --import -y` is allowed
  only after before/after tool and issue summaries are captured. If organization
  coding standards block changes, record the exact blocker in the SOW.
- Treat local Analysis CLI and Cloud data as separate sources until Cloud
  reanalysis completes after import. Local config changes do not affect Codacy
  Cloud by being committed; the import step is the only sync path.
- Run `scripts/test/codacy-config-test.sh` after any `.codacy/codacy.config.json`
  or `.codacy.yml` change. The self-test must stay hermetic: it validates
  JSON/YAML shape, keeps repository-wide non-runtime SOW work-ledger, duplicate
  instruction symlink, generated/local artifact exclusions separate from
  tool-scoped test/tooling exclusions, forbids accidental broad runtime-source
  exclusions, proves retained high-signal security patterns remain enabled, and
  requires documented rationale for local-only Cloud-noise removals such as
  PMD/SQLint and for any path-scoped tool exclusion.
- Coverage upload reuses existing Go/frontend reports. Use GitHub secret names
  only (`CODACY_PROJECT_TOKEN` or account-token mode variables); never write
  token values to disk.
- Coverage upload belongs in the non-required `codacy-coverage` job, fed from
  the existing `go-coverage-<run_id>` and `frontend-coverage-<run_id>`
  artifacts. Do not put Codacy upload steps inside the required `test` or
  `frontend` jobs while Codacy is reporting-only.
- Coverage upload token behavior is fail-open while reporting-only, but Codacy
  secrets are never passed to `pull_request` execution of repository code. The
  workflow skips the entire `codacy-coverage` job on `pull_request` events
  before checkout, artifact download, secret injection, or repository scripts
  can run. With no token on a non-PR event, CI logs a skip; with
  `CODACY_PROJECT_TOKEN` or `CODACY_API_TOKEN` present, missing artifacts,
  missing or empty coverage reports, reporter download failures, invalid
  bootstrap files, and upload failures emit annotations and exit successfully.
  If both token secrets exist, `CODACY_PROJECT_TOKEN` wins and account-token
  variables are unset before the reporter runs. The upload script also refuses
  all Codacy coverage upload on `pull_request` events before token-mode
  selection as defense in depth. PR coverage upload stays disabled until a
  future SOW designs a safe path. These become hard failures only after a future
  SOW promotes Codacy coverage to a required branch-protection context.
- The upload logic lives in `scripts/codacy-coverage-upload.sh`; do not grow an
  inline workflow shell state machine. Run
  `scripts/test/codacy-coverage-upload-test.sh` after any change to the upload
  script or the `codacy-coverage` job wiring. The self-test is part of local
  `scripts/gates.sh` and the CI `gates` job, so it must stay hermetic and fast.
- Upload each present non-empty Go/frontend coverage report as a partial report.
  A missing or empty Go report does not block uploading frontend LCOV, and a
  missing or empty frontend LCOV report does not block uploading Go coverage. If
  both reports are missing or empty, annotate and exit successfully before
  downloading the reporter. If at least one partial upload is attempted, send
  Codacy's required `final` notification after the partial upload attempts even
  if one partial command fails. Normalize frontend LCOV source paths from
  frontend-local `src/...` to repository-root `frontend/src/...` before upload.
- Do not use `bash <(curl -Ls https://coverage.codacy.com/get.sh) ...` in CI.
  Download the reporter bootstrap with `curl -fsSL --retry ... -o "$(mktemp)"`
  verify the file is a non-empty shell script, run `bash -n`, and execute the
  temporary file. Process substitution can turn a failed download into an empty
  script that exits successfully. Codacy's documented reporter checksum
  validation is upstream behavior, not a local guarantee from this workflow.
- Disable tools/patterns only with evidence. Broad "too noisy" disables without
  path/category counts are not acceptable.
- Test/tooling path exclusions must cite the stronger project-native gate that
  still covers those files. Runtime source paths are not excluded merely because
  a scanner pattern is noisy; prefer a code fix or a line/rule suppression with
  executable evidence.

## When a Gate Fails

1. Read the failure output. Do not guess.
2. Identify the root cause. Reproduce locally if CI-only.
3. Fix the root cause in the code, the test, or the spec — whichever is genuinely wrong.
4. Do **not** weaken the gate, lower a threshold, suppress the warning, or skip the test.
5. If the gate itself is wrong (e.g. a new false-positive lint rule), file a SOW to update the gate config with evidence; do not silently lower it.
6. If the gate must be temporarily relaxed (extreme cases): the SOW must include a `Gate Suppression` section with reason, scope, expiry date, and the issue tracking restoration.

## Adding a New Gate

When a new class of bug or risk is discovered:

1. Determine whether existing gates would have caught it. If yes, the gate has a hole — fix the hole.
2. If no existing gate covers it, design a new one. Specify command, threshold, scope.
3. Add it to this skill, to `.agents/sow/specs/quality-gates.md`, and to CI in the same commit.
4. Wire it into CI as a dedicated job, and into `scripts/gates.sh` (the aggregate), in the same commit.
5. Update `AGENTS.md` if the gate adds a top-level commitment.

## Removing a Gate

Removing a gate requires a SOW with: evidence the gate is wrong or obsolete, what replaces it, what risk class is now unprotected. Operator-approved before removal.

## Performance Note

Local full `scripts/gates.sh` runs **target** under 5 minutes. Measured reality (SOW-0013): the Go `-race` suite (`scripts/test.sh`, dominated by `internal/ingest`) is the long pole and pushes the full aggregate above 5 min. The gate stays correct and complete — never drop or weaken a gate to hit the target. The measured total + long pole are in the `gates.sh` header; a `--fast` profile / internal parallelization is a tracked follow-up SOW.

## Cross-References

- Contract: `AGENTS.md` (Quality Gates section)
- Spec: `.agents/sow/specs/quality-gates.md`
- Test details: `.agents/skills/project-testing/SKILL.md`
- Workflow: `.agents/skills/project-workflow/SKILL.md`
