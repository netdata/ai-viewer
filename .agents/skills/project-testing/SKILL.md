---
name: project-testing
description: Run, write, and maintain ai-viewer tests across Go backend and React frontend. Use when adding or changing tests, debugging CI failures, managing fixtures, or running benchmarks.
---

# Testing

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
| Go E2E (ingest → store → server) | `tests/e2e/*_test.go` | every commit |
| Frontend component | `frontend/src/**/*.test.tsx` | every commit |
| Frontend E2E | `frontend/tests/*.spec.ts` | subset every commit, full on `main` |
| Performance benchmark | `internal/adapters/<name>/bench_test.go` | every commit, fails on > 20% regression |

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

A PR cannot land if:

- Any Go test fails (with race detector enabled).
- Any frontend test fails.
- Any lint warning exists.
- Any benchmark regresses > 20% from baseline.
- Any committed fixture trips the secret scanner.
- Any spec is stale relative to code (the spec-sync skill helps here).

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
