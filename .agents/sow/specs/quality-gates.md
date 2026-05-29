# Quality Gates

## Purpose

The authoritative catalog of every automated gate enforced in ai-viewer's CI and local pre-commit. CI runs every gate; the assistant runs the same gates locally via `./scripts/gates.sh` before any commit. A gate failure is a defect, not a stylistic suggestion: fix the root cause, never weaken the gate.

The runtime companion to this spec is `.agents/skills/project-quality-gates/SKILL.md` (commands and ergonomics). This spec is the durable truth about *what* is enforced and *at what threshold*.

## Operating Rules

- Every gate listed here runs locally before any commit and in CI on every push.
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

- `golangci-lint run --timeout=5m`.
- `.golangci.yml` enables: `govet`, `errcheck`, `staticcheck`, `unused`, `gosimple`, `ineffassign`, `gosec`, `revive`, `gofmt`, `goimports`, `bodyclose`, `noctx`, `errorlint`, `gocritic`, `gocyclo` (max 15), `gofumpt`, `misspell`, `nilerr`, `prealloc`, `unconvert`, `unparam`, `whitespace`.
- Threshold: zero warnings.

### Go — Security

- `gosec -severity medium -confidence medium ./...` → zero high/critical findings.
- `govulncheck ./...` → zero known vulnerabilities. Runs per push and on a nightly schedule for newly-disclosed CVEs.

### Go — Tests

- `go test -race -count=1 ./...` → all pass.
- `-count=1` defeats the test cache so CI always runs fresh.

### Go — Coverage

- `go test -race -coverprofile=coverage.out -covermode=atomic ./...`
- Thresholds:
  - Repository-wide lines ≥ 80%.
  - Per-package on changed code ≥ 80% lines, ≥ 70% branches.
  - New code in the PR ≥ 90% lines.
- Enforcement: `scripts/check-coverage.sh` parses `coverage.out` plus the PR diff. Failing thresholds blocks merge.

### Go — Fuzzing

- Every adapter parser exposes at least one `FuzzXxx` target.
- Every canonical decoder exposes at least one `FuzzXxx` target.
- CI: 30 seconds per target per push, 5 minutes per target nightly.
- Crashes from nightly runs are auto-filed as issues by the CI workflow.
- Threshold: zero crashes per run.

### Go — Property-Based Tests

- `internal/canonical/property_test.go` uses property-based assertions for canonical mapping invariants (idempotency, ordering, structural equality after round-trip).
- Threshold: all properties hold.

### Go — Benchmarks

- Marked benchmarks for: adapter `Scan`, adapter `Tail`, canonical event encode/decode, SQLite batch insert, REST query path, SSE fanout.
- `go test -run=^$ -bench=. -benchmem -count=5 ./... > bench-current.txt`
- `benchstat bench/baseline.txt bench-current.txt`
- Threshold: ≤ 20% regression in any metric vs `bench/baseline.txt`. Baseline updates only on explicit SOW approval.

### Go — Race Stress

- Concurrency-touching changes: `-count=10` locally before commit.
- CI: `-count=3` per push, `-count=20` nightly.

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

- After `vite build`, `scripts/check-bundle-size.js` measures gzipped bundle.
- Thresholds: main chunk ≤ 500 KB gzipped; per-route lazy chunks ≤ 200 KB gzipped.
- Exceeding requires a SOW with justification.

### Secrets + Operator-PII Scan

`scripts/scan-secrets.sh` scans **every tracked file in the repository** (not just
`testdata/`) and exits non-zero on any hit. It enforces two rule classes:

1. **Operator identity (banned everywhere, zero tolerance).** The operator's real
   email addresses, real home path, and given/sur-name. These must never appear in
   any tracked file — including the sanitizer's `INPUT/` fixtures. The literal
   patterns are defined inside the script itself, which **excludes its own path**
   from the scan (a scanner necessarily contains the patterns it hunts for). Name
   matching is word-bounded so unrelated tokens (e.g. `cost_usd`) never match.
2. **Generic secret shapes (banned everywhere except sanitizer test inputs).**
   `sk-…` / `sk-ant-…` (OpenAI/Anthropic keys), `xox[bpas]-…` (Slack),
   `AKIA[0-9A-Z]{16}` (AWS), and `Bearer <high-entropy>` tokens. (Public provider
   *hostnames* like `api.anthropic.com` are NOT secrets and are not scanned — they
   are a sanitizer concern; fixtures replace them with `*.example.invalid`.) Secret
   shapes are banned in all real artifacts but **allowed only** under
   `scripts/test/fixtures/*/INPUT/**` — the dirty inputs
   whose entire purpose is to exercise `sanitize-fixture.sh`'s redaction. Those
   inputs must be **synthetic** (rule class 1 still applies to them, so they may
   never carry the operator's real identity). Known placeholders are allow-listed:
   `[REDACTED_*]`, `*.example.invalid`, RFC-2606 reserved `example.com`, and the
   synthetic `sk-ant-EXAMPLE…` key shape.

The allow-list is applied **per matched token, never per line** — a real secret on
the same line as a placeholder is still flagged. The allow-list applies **only to
rule class 2**; rule class 1 (operator identity) is never exempted under any
circumstances.

- **Threshold:** zero hits.
- **Fail-closed in CI.** The `gates` job runs the scanner and **fails when the
  script is absent** — a missing scanner is a missing gate, not a pass. (Only the
  genuinely-optional aggregate `scripts/gates.sh` may be skipped when absent.)
- **Negative self-test.** The scanner ships with a test that plants an
  operator-identity string in a temp file and asserts the scan flags it, so the
  enforcement itself cannot silently rot.

### Spec Drift

- `scripts/spec-drift.sh` lints common drift indicators:
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

- `./scripts/lint.sh` — all formatting + lint + static + security.
- `./scripts/test.sh` — all tests + coverage + race.
- `./scripts/gates.sh` — every gate above, in order, fail-fast. The canonical pre-commit gate.

CI uses the same scripts so local and CI behavior cannot diverge.

## Performance Target

Full local `./scripts/gates.sh` completes in under 5 minutes on the operator's workstation. If it exceeds, profile and parallelize before adding more gates.

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
- **Coverage thresholds**: prevents the "I tested the happy path" gap; new-code ≥ 90% prevents coverage erosion.
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
