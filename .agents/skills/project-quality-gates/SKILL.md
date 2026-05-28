---
name: project-quality-gates
description: Catalog of every automated quality gate ai-viewer enforces — commands, thresholds, and what to do when a gate fails. Use before claiming any work done, before any commit, when adding a new gate, or when investigating a CI failure. The runtime companion to .agents/sow/specs/quality-gates.md.
---

# Quality Gates

## Operating Rule

Every gate listed here runs in CI on every push. The assistant runs them all locally before reporting work done. If a gate fails: fix the root cause. **Never weaken a gate to make it pass.** Lowering a threshold, marking a test skipped, suppressing a linter, or adding a `// nolint` is a contract breach unless the SOW explicitly justifies the suppression with a linked issue.

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
golangci-lint run --timeout=5m
```

`.golangci.yml` enables at minimum: `govet`, `errcheck`, `staticcheck`, `unused`, `gosimple`, `ineffassign`, `gosec`, `revive`, `gofmt`, `goimports`, `bodyclose`, `noctx`, `errorlint`, `gocritic`, `gocyclo` (≤ 15), `gofumpt`, `misspell`, `nilerr`, `prealloc`, `unconvert`, `unparam`, `whitespace`.

Threshold: zero warnings.

### Go — Security

```bash
gosec -severity medium -confidence medium ./...
govulncheck ./...
```

Threshold: zero high/critical findings from gosec; zero known vulnerabilities from govulncheck. Govulncheck runs in CI on a schedule plus every push so newly-disclosed CVEs surface fast.

**GOTCHA — standalone `gosec@latest` ≠ golangci's bundled gosec.** CI's "Go — Security" step installs and runs `gosec@latest` STANDALONE; that version ships newer analyzers (e.g. **G705** XSS-taint) that golangci-lint's older *bundled* gosec does not have. So `golangci-lint run` returning 0 does NOT mean `gosec` passes — they are different gates. Always run the standalone `gosec -severity medium -confidence medium ./...` locally (install with `go install github.com/securego/gosec/v2/cmd/gosec@latest`) AND `goimports -l` AND `govulncheck ./...` before pushing — not just `gofmt`/`vet`/`golangci-lint`/`test`/`build`. (govulncheck exits 0 when a required-but-uncalled module has a CVE — "your code doesn't appear to call" — that is a pass.)

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

- Repository-wide: ≥ 80% lines covered.
- Per-package on changed code: ≥ 80% lines, ≥ 70% branches.
- New code in the PR: ≥ 90% lines covered.

Enforcement: `scripts/check-coverage.sh` parses `coverage.out` and fails if thresholds not met. New code coverage is computed via `go test` + diff scope.

### Go — Fuzzing

```bash
go test -fuzz=Fuzz -fuzztime=30s ./internal/adapters/...
go test -fuzz=Fuzz -fuzztime=30s ./internal/canonical/...
```

Every adapter and canonical decoder MUST expose at least one `FuzzXxx` target. CI runs each fuzz target for 30 seconds per push and for 5 minutes on nightly schedule. Crashes from nightly runs are auto-filed as GitHub issues (config in CI workflow).

Threshold: zero crashes per run. Crashes block merge.

### Go — Benchmarks

```bash
go test -run=^$ -bench=. -benchmem -count=5 ./... > bench-current.txt
benchstat bench/baseline.txt bench-current.txt
```

Marked benchmarks (`func BenchmarkXxx`) exist for: adapter `Scan`, adapter `Tail`, canonical event encoding, SQLite batch insert, REST query path, SSE fanout.

Threshold: ≤ 20% regression in any metric vs. `bench/baseline.txt`. The baseline updates only when a SOW explicitly accepts a regression with justification.

### Go — Race + Stress

```bash
go test -race -count=10 ./...       # local pre-commit on concurrency-touching changes
```

For changes to ingest pipeline, SSE hub, or anything with channels/goroutines: run `-count=10` locally; CI runs `-count=3` on every push and `-count=20` nightly.

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
scripts/spec-drift.sh
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
./scripts/lint.sh         # all formatting + lint + static + security
./scripts/test.sh         # all tests + coverage + race
./scripts/gates.sh        # every gate above, in order, fail-fast
```

`scripts/gates.sh` is the canonical pre-commit gate. The assistant runs it locally before every commit. CI runs the same gates from the same scripts so local and CI behavior cannot diverge.

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
4. Bake it into `scripts/gates.sh`.
5. Update `AGENTS.md` if the gate adds a top-level commitment.

## Removing a Gate

Removing a gate requires a SOW with: evidence the gate is wrong or obsolete, what replaces it, what risk class is now unprotected. Operator-approved before removal.

## Performance Note

Local full-gate runs should complete in under 5 minutes on the operator's workstation. If `scripts/gates.sh` exceeds that, profile and parallelize before adding more gates.

## Cross-References

- Contract: `AGENTS.md` (Quality Gates section)
- Spec: `.agents/sow/specs/quality-gates.md`
- Test details: `.agents/skills/project-testing/SKILL.md`
- Workflow: `.agents/skills/project-workflow/SKILL.md`
