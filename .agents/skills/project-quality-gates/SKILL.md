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

### Go — Formatting

```bash
gofmt -l .                          # zero output expected
goimports -l .                      # zero output expected
```

Threshold: zero diffs. Auto-fix locally with `gofmt -w` and `goimports -w` before commit.

### Go — Vet

```bash
go vet ./...
```

Threshold: zero warnings.

### Go — Lint (golangci-lint)

```bash
golangci-lint run --timeout=5m      # umbrella: also enforces fmt + vet
```

golangci-lint is **v2** (`.golangci.yml` has `version: "2"`). `golangci-lint run` is the umbrella gate — with the formatters enabled it enforces Go — Format, and the `govet` linter covers Go — Vet, so a clean `run` means fmt+vet+lint all pass. (In golangci-lint v2, `golangci-lint run` REPORTS formatter violations — gofmt/goimports/gofumpt — as failures; `golangci-lint fmt` is the separate auto-fix path. `run` enforces formatting; it is not merely a linter-only pass. Note: `--enable-only <formatter>` is rejected because formatters are configured under `formatters:`, not enabled as linters — this does not mean `run` skips them.) Run it locally via `./scripts/lint.sh` (which then runs the standalone security tools). The version is pinned in `.golangci-lint-version` (single source for CI + `scripts/lint.sh`); CI runs it via `golangci/golangci-lint-action` at that pinned version.

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

## Verifying CI Before Merge (do not trust `--watch` exit code)

Branch protection on this repo has `required_status_checks: null` (no *required* checks — see AGENTS.md). Consequence: **`gh pr checks <pr> --watch` can exit 0 even when a check FAILED**, because it only fails on *required* checks. Never gate a merge on the `--watch` exit code alone. Before `gh pr merge`, run `gh pr checks <pr>` and confirm EVERY row reads `pass` (no `fail`/`pending`). A green-looking `--watch` exit with a red `lint`/`gosec` is exactly how a failing build reaches master here.

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

### Go — Benchmarks

```bash
scripts/check-bench.sh             # count=6, benchstat vs bench/baseline.txt, > 20% sec/op gate
scripts/test/check-bench-test.sh   # hardware-independent self-test of the gate's benchstat parser
```

Marked benchmarks (`func BenchmarkXxx`) exist for the 5 performance-critical paths: adapter `Scan`, adapter `Tail`, SQLite batch insert, REST query path, SSE fanout. (No canonical encode/decode benchmark — canonical events are constructed directly, never serialized.)

Threshold: a statistically-significant **> 20% sec/op** regression for any individual benchmark fails `scripts/check-bench.sh` (the `geomean` aggregate + custom `ReportMetric` values are not gated; benchstat's `~` neutralizes noisy benchmarks). It is a **local/workstation** gate — `bench/baseline.txt` is workstation-measured (carries the commit SHA + `goos/goarch/pkg/cpu` config lines) and is not comparable to GitHub-runner hardware, so CI runs only the bench compile-smoke + the gate self-test, not the regression gate itself. Baseline refresh requires an explicit SOW (no auto-update).

### Go — Race + Stress

```bash
go test -race -count=10 ./...       # local pre-commit on concurrency-touching changes
```

For changes to ingest pipeline, SSE hub, or anything with channels/goroutines: run `scripts/test.sh --stress 10` locally; CI runs `-count=1` per push (the `test` job) and `-count=10` race stress nightly (`race-stress-nightly.yml`).

### Frontend — Lint

```bash
cd frontend && npm run lint -- --max-warnings=0
```

ESLint flat config with `@typescript-eslint`, `eslint-plugin-react`, `eslint-plugin-react-hooks`, `eslint-plugin-jsx-a11y`, `eslint-plugin-import`. Threshold: zero warnings.

### Frontend — Type Check

```bash
cd frontend && npm run typecheck    # invokes `tsc --noEmit`
```

`tsconfig.json` enforces: `strict`, `noUncheckedIndexedAccess`, `exactOptionalPropertyTypes`, `noImplicitOverride`, `noFallthroughCasesInSwitch`, `noUnusedLocals`, `noUnusedParameters`.

Threshold: zero errors.

### Frontend — Unit/Component Tests

```bash
cd frontend && npm run test -- --run --coverage
```

Vitest + React Testing Library. Threshold: all pass, ≥ 80% lines per component directory.

### Frontend — E2E

```bash
cd frontend && npm run e2e
```

Playwright headless. One scenario per primary user flow at minimum: sessions list filter, session detail load, sources panel, real-time update via SSE, theme toggle.

Threshold: all pass. Flaky tests are quarantined into a separate suite with a linked SOW to fix; never marked `test.skip`.

### Frontend — Accessibility

```bash
cd frontend && npm run e2e:a11y     # Playwright + @axe-core/playwright
```

axe-core runs on every Playwright route. Threshold: zero serious or critical violations.

### Frontend — Bundle Size

```bash
cd frontend && npm run build
node scripts/check-bundle-size.js dist/assets/*.js
```

Threshold: main chunk ≤ 500 KB gzipped. Per-route lazy chunks ≤ 200 KB gzipped each. Exceeding requires a SOW.

### Secrets Scan

```bash
scripts/scan-secrets.sh             # grep over testdata/ and src for common patterns
```

Patterns checked: `[A-Za-z0-9_-]{32,}` near keywords `key|token|secret|password|bearer`; `sk-[A-Za-z0-9]+` (OpenAI); `xox[bpas]-[A-Za-z0-9-]+` (Slack); `AKIA[0-9A-Z]{16}` (AWS); plus a configurable allow-list for obvious test placeholders like `[REDACTED_SECRET]`.

Threshold: zero hits.

### AI-Attribution Scan

```bash
scripts/scan-ai-attribution.sh
```

Greps `cmd/`, `internal/`, `scripts/` (tests included) for comments that attribute code to an external AI reviewer by name — a reviewer name adjacent to an iteration/priority tag (`<name> iter-N`, `<name> P<digit>`) or an attribution verb (`per`/`pins <name>`, `<name> flagged/…`). The pattern REQUIRES a reviewer name, so legitimate domain terms (priced model names like `gemini-2.5-pro`, the `codex`/`opencode` session formats the tool ingests, the deepseek redaction rule) never match; the script self-excludes (it enumerates the names in its own pattern). Enforces the no-AI-attribution rule on the public repo — the work stands on its own. Runs in the CI `gates` job.

Threshold: zero hits.

### Spec Drift

```bash
scripts/spec-drift.sh    # planned (SOW-0013); manual spec↔code audit until it lands
```

Lints common drift indicators:

- REST endpoints registered in `internal/presenter/` vs. endpoints listed in `specs/rest-api.md`.
- SSE event types in `internal/presenter/sse.go` vs. `specs/sse-protocol.md`.
- SQLite columns in `internal/store/migrations/` vs. `specs/data-model.md`.
- Canonical event fields in `internal/canonical/events.go` vs. `specs/canonical-events.md`.
- Adapter probes in `internal/ingest/discover.go` vs. `specs/adapter-<name>.md`.

Threshold: zero drift.

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
./scripts/lint.sh         # Go: golangci umbrella + standalone gosec + govulncheck (SOW-0009; EXISTS)
./scripts/test.sh         # all tests + coverage + race (SOW-0010; EXISTS)
./scripts/check-coverage.sh  # statement coverage gate, internal/* >= 80% (SOW-0010; EXISTS)
./scripts/gates.sh        # every gate above, in order, fail-fast (SOW-0013; PLANNED)
```

Current state: `scripts/lint.sh`, `scripts/test.sh`, and `scripts/check-coverage.sh` exist (SOW-0009/0010). The canonical `scripts/gates.sh` aggregator (SOW-0013) is NOT yet present. Until it lands, run `scripts/lint.sh` + `scripts/test.sh` + `scripts/check-coverage.sh` plus the individual gate commands from this catalog before every commit. CI today enforces each gate as a dedicated job (`lint` via the pinned `golangci-lint-action` + standalone gosec/govulncheck, `test`, `frontend`, `embed-smoke`, `gates`); SOW-0013 will make a single `gates.sh` and CI invoke the same underlying steps so local and CI behavior cannot diverge.

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
4. Wire it into CI as a dedicated job, and into `scripts/gates.sh` once that aggregator lands (SOW-0013).
5. Update `AGENTS.md` if the gate adds a top-level commitment.

## Removing a Gate

Removing a gate requires a SOW with: evidence the gate is wrong or obsolete, what replaces it, what risk class is now unprotected. Operator-approved before removal.

## Performance Note

Local full-gate runs should complete in under 5 minutes on the operator's workstation. Once the aggregate `scripts/gates.sh` lands (SOW-0013), if it exceeds that, profile and parallelize before adding more gates.

## Cross-References

- Contract: `AGENTS.md` (Quality Gates section)
- Spec: `.agents/sow/specs/quality-gates.md`
- Test details: `.agents/skills/project-testing/SKILL.md`
- Workflow: `.agents/skills/project-workflow/SKILL.md`
