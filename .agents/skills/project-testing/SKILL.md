---
name: project-testing
description: Run, write, and maintain ai-viewer tests across Go backend and React frontend. Use when adding or changing tests, debugging CI failures, managing fixtures, or running benchmarks.
---

# Testing

## Core Principle

**Untested code is broken code.** The operator does not manually test for the assistant. Every behavior the project ships has at least one automated test exercising it. Manual UI walkthroughs are diagnostics for the assistant; they are not proof of correctness. If a behavior cannot be tested, the design is wrong — refactor until it can be.

Tests are written **before** the implementation (see `project-workflow`). A failing test is the executable contract; the implementation makes it pass without weakening it.

## Test Commands

```bash
# All Go tests (run during regular dev)
go test ./...

# With race detector (required in CI; also run before commits touching concurrency)
go test -race ./...

# One adapter
go test ./internal/adapters/aiagent_v3

# Update golden files for a single adapter
go test ./internal/adapters/aiagent_v3 -update-golden

# Coverage
go test -cover ./...
go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out

# Benchmarks
go test ./internal/adapters/... -bench=. -benchmem

# Frontend
cd frontend && npm test                  # vitest watch mode
cd frontend && npm test -- --run         # one-shot
cd frontend && npm run e2e               # playwright headless
AI_VIEWER_E2E_PORT=17710 npm run e2e     # use a different local E2E port
cd frontend && npm run e2e -- --headed   # playwright debug
cd frontend && npm run e2e -- --ui       # playwright UI mode

# Full pre-commit check
./scripts/lint.sh && go test -race ./... && cd frontend && npm test -- --run && npm run lint
```

## Test Pyramid

| Layer | Lives in | Run per |
|---|---|---|
| Adapter unit | `internal/adapters/<name>/*_test.go` | every commit |
| Canonical/ingest/store unit | `internal/*/  *_test.go` | every commit |
| Presenter handler | `internal/presenter/*_test.go` | every commit |
| Go fuzz (parsers) | `internal/adapters/<name>/{fuzz_test,*_fuzz_test}.go` (canonical has no parser → no fuzz target) | seed corpus per push (deterministic), 5min/target explore nightly |
| Go property-based (canonical mapping) | `internal/canonical/property_test.go` (rapid-go) | every commit |
| Go E2E (ingest → store → server) | `tests/e2e/*_test.go` | every commit |
| Performance benchmark | `internal/adapters/aiagent_v2/bench_test.go`, `internal/adapters/claude_code/bench_test.go`, `internal/ingest/bench_test.go`, `internal/presenter/bench_test.go`, `internal/notify/bench_test.go` | local `scripts/check-bench.sh` (workstation gate; CI runs the compile-smoke), > 20% sec/op regression |
| Frontend component | `frontend/src/**/*.test.tsx` | every commit |
| Frontend E2E (Playwright) | `frontend/tests/*.spec.ts` | every commit |
| Frontend a11y (axe) | embedded in Playwright | every commit |
| Mutation testing (recommended) | `internal/canonical/`, `internal/ingest/`, adapters | quarterly |

## Coverage Thresholds (enforced)

| Scope | Threshold |
|---|---|
| Gated aggregate (`internal/*`, statement) | ≥ 80% |
| Per-package (`internal/*`, statement) | ≥ 80% (`/cmd/` excluded; branch deferred) |
| New code in the PR | ≥ 90% — **deferred** (SOW-0036) |
| Frontend component directory | ≥ 80% lines |

`scripts/check-coverage.sh` enforces **statement** coverage on the gated `internal/*` set (`/cmd/` excluded) against `coverage.out`. Branch coverage and new-code-in-PR ≥ 90% are deferred (see `quality-gates.md` "Go — Coverage"). Lowering a threshold to land a PR is a contract breach; either add tests or split the PR.

## Mandatory Test Kinds

- **Every adapter exposes at least one `FuzzXxx` target** covering its parse path.
- **`internal/canonical` exposes no `FuzzXxx` target** — it owns no decoders; all untrusted-bytes parsing (and thus all fuzzing) lives in the adapters.
- **Performance-critical paths have benchmarks** (`bench/baseline.txt` baseline). `scripts/check-bench.sh` runs `-count=6` (benchstat's 0.95-CI minimum) and gates a statistically-significant > 20% sec/op regression per benchmark; it is a **local/workstation** gate (the workstation baseline is not comparable to CI-runner hardware), so CI runs only the bench compile-smoke + the gate's hardware-independent self-test.
- **Concurrency-touching code is stress-tested with `scripts/test.sh --stress 10` (`-race -count=10`) locally**; CI runs `-count=1` per push and `-count=10` race stress nightly.
- **Frontend E2E covers golden paths AND error states** (network failure, empty list, malformed SSE event). axe runs on every route.
- **Every UI change has at least one Playwright assertion** that proves the behavior end-to-end. Component tests alone are not sufficient for user-visible behavior.

## Fixture Management

Adapter fixtures live under `testdata/<adapter>/<scenario>/INPUT/` with expected canonical events at `testdata/<adapter>/<scenario>/expected.jsonl`.

**Sanitization is mandatory** (see `.agents/sow/specs/security.md` and `testing-strategy.md`):

- Replace `originId`/`sessionId` with stable test UUIDs.
- Strip user messages → `[REDACTED_USER_MESSAGE]`.
- Strip tool I/O → `[REDACTED_TOOL_OUTPUT]`.
- Replace API URLs with `https://api.example.invalid/...`.
- Replace API keys with `[REDACTED_SECRET]`.

Use `scripts/sanitize-fixture.sh <input-file> <output-dir>` (built during Phase 1).

CI grep-scans `testdata/` for common secret patterns and fails on hits.

## Writing a New Test

For Go:

```go
func TestX(t *testing.T) {
    t.Parallel()
    // arrange
    // act
    // assert with cmp.Diff for structs/slices
}
```

For frontend:

```tsx
import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';

describe('SessionRow', () => {
  it('renders status badge', () => {
    render(<SessionRow session={mockFailedSession} />);
    expect(screen.getByRole('status', { name: /failed/i })).toBeInTheDocument();
  });
});
```

## CI Gates (zero tolerance)

The full catalog lives in `project-quality-gates`. A PR cannot land if any gate fails:

- Any Go test fails (race detector enabled).
- Any frontend test or Playwright spec fails.
- Any lint warning exists (Go or frontend).
- Coverage threshold missed on changed code.
- Any fuzz target crashed during its run.
- Any benchmark regresses > 20% from baseline.
- Any committed fixture trips the secret scanner.
- Any spec is stale relative to code (`scripts/spec-drift.sh` catches the structural indicators; manual audit covers prose).
- Axe accessibility violations at serious/critical level.
- Frontend bundle exceeds size budget.

**Never** add `t.Skip`, `// nolint`, `it.skip`, or `test.skip` to land a PR. Either fix the underlying issue or open a SOW that explicitly justifies the suppression with an expiry condition.

## Debugging Failing Tests

1. Run the single failing test with `-v`: `go test -v -run TestName ./pkg`.
2. For race conditions: re-run with `-count=10 -race`.
3. For flaky frontend E2E: re-run with `--headed --debug` to step through.
4. Check the CI log for the full output; many local-pass / CI-fail issues are platform-specific (path separators, time zones).

## Adding a New Test Scenario for an Adapter

1. Capture a real session (in the operator's local environment) demonstrating the scenario.
2. Sanitize via `scripts/sanitize-fixture.sh`.
3. Place under `testdata/<adapter>/<scenario>/INPUT/`.
4. Run `go test ./internal/adapters/<adapter> -update-golden` to generate `expected.jsonl`.
5. **Review the golden file manually.** Does every event look right? Are timestamps reasonable? Are sub-agent links correct?
6. Commit fixture + golden + a test case in `adapter_test.go` if not auto-discovered.

## Reporting Honest Test Status

When reporting to the operator, never say "code works" without:

- The test file paths covering the new behavior.
- The gate command outputs (`./scripts/gates.sh` summary plus any focused reruns used for diagnosis).
- The external review status.

Honest phrasings:

- "Code written, tests passing, gates green, review pending." — work is mid-flight.
- "Code written, tests passing, gates green, review converged — ready." — work is done.
- "Code written, behavior X not yet covered by a test — not ready." — work is incomplete.

## Cross-References

- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Spec: `.agents/sow/specs/testing-strategy.md`
