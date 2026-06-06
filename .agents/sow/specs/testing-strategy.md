# Testing Strategy

## TL;DR

Pyramid: many unit tests, fewer integration tests, a small but real E2E layer. Adapters are tested against sanitized real fixture files. CI runs everything on every commit; zero failures, zero lint warnings, before any merge.

## Layers

### 1. Adapter unit tests (Go)

For every adapter under `internal/adapters/<name>/`:

- **Fixture-based scan tests**: `Scan` over `testdata/<name>/<scenario>/` produces an event stream matching the golden JSON at `testdata/<name>/<scenario>/expected.jsonl`. Update with `go test ./internal/adapters/<name> -update-golden`.
- **Scenarios mandatory**:
  - `happy_path/` — single completed session, single turn.
  - `multi_turn/` — multiple turns.
  - `sub_agents/` — root + nested child sessions.
  - `tools/` — multiple tool calls.
  - `failures/` — failed turn, failed op, failed session.
  - `in_progress/` — session with unfinalized turn.
  - `cursor_resume/` — partial scan + resume from cursor.
- **Tail tests**: spawn `Tail` in a goroutine, write a new fixture into the temp dir mid-test, assert events arrive within 1 s.
- **Idempotency property**: re-scanning with the final cursor produces zero events.
- **Error tests**: malformed records produce `SourceError` events, do not crash, do not block other records.

### 2. Canonical/ingest unit tests (Go)

`internal/canonical/` and `internal/ingest/`:

- `EventBase`/`EventKind` accessor invariants, plus property tests (`internal/canonical/property_test.go`) that round-trip generated canonical events through the real ingest→store→presenter path. (Canonical has no encode/decode — events are constructed by adapters and never serialized.)
- Cursor parsing across all adapter formats.
- Ingest dedup: feed two copies of the same event stream, assert SQLite rows are written once.
- Ingest link resolution: parent arrives after child, assert eventual linking.
- Ingest backpressure: full events channel, assert no panic, no data loss within capacity.

### 3. Store unit tests (Go)

`internal/store/`:

- Migration tests: run every migration from empty DB to current, assert no errors.
- Schema invariants: required NOT NULL columns enforced, foreign keys honored.
- Query correctness: query helpers against a known dataset (built from `testdata/canonical/`).

### 4. Presenter handler tests (Go)

`internal/presenter/`:

- Per-endpoint table-driven tests: request → expected response shape.
- Filter parsing: every combination of query params.
- SSE: connect a test client, push events, assert delivery order and dedup.
- Error handling: malformed query → 400 with structured error.
- Auth: confirm bind defaults to 127.0.0.1 (when applicable).

### 5. End-to-end integration tests (Go)

`tests/e2e/` — full ingester → store → server pipeline:

- Spin up a temp `<sessions-dir>` containing fixtures from several adapters.
- Start ingester pointed at the temp dir + temp SQLite.
- Start server pointed at the same SQLite.
- HTTP-test each user-relevant flow: list sessions, fetch detail, fetch stats, open SSE subscription.
- Write a new fixture mid-test; assert SSE event arrives within 2 s.

### 6. Frontend tests

`frontend/`:

- **Vitest + RTL**: every page component has a render test against mocked API responses.
- **Playwright E2E** (`frontend/tests/`): one happy-path scenario per primary page, running against a real backend started by the test harness. Headless by default; `--headed` for debugging.

## Fixture Management

`testdata/<adapter>/<scenario>/` layout:

```text
testdata/aiagent_v3/happy_path/
├── INPUT/
│   ├── session/<sessionId>.jsonl
│   └── payloads/<sessionId>/turn-0001/llm-0001-request.http.gz
└── expected.jsonl              # one canonical event per line, newest first
```

**Sanitization rules** (mandatory before committing any fixture):

- Replace all `originId`/`sessionId` with stable test UUIDs (e.g. `00000000-0000-0000-0000-000000000001`).
- Strip user message contents → `"[REDACTED_USER_MESSAGE]"`.
- Strip tool I/O contents → `"[REDACTED_TOOL_OUTPUT]"`.
- Replace API URLs with `https://api.example.invalid/...`.
- Replace API keys with `[REDACTED_SECRET]`.
- Keep schema shape, timestamps (rebased to a fixed start), token counts, cost numbers.

A sanitization helper script lives at `scripts/sanitize-fixture.sh` (built during Phase 1 SOW).

## Coverage Targets

**Enforced gate** — statement coverage via `scripts/check-coverage.sh` (authority: `quality-gates.md` "Go — Coverage"): every gated `internal/*` package ≥ 80% statements AND their aggregate ≥ 80%; `/cmd/` (binaries + dev tools) is excluded (reported, not gated). Branch coverage and new-code-in-PR ≥ 90% are deferred (SOW-0036). Go has no first-class branch coverage. The Go toolchain also has no `//coverage:ignore` directive. Neither mechanism is used here.

Frontend coverage is formally gated by Vitest coverage (global aggregate plus
per-directory line floors for measured `src/components/` and `src/pages/`
directories), with Playwright E2E/axe covering route-level behavior.

Non-gated aspirational aims for Go packages (guide where to invest test effort;
not enforced):

- ingest / store / canonical: aim > 90%.
- adapter packages: aim > 85%.
- presenter handlers: aim > 80%.

## Codacy Coverage Upload

Codacy coverage is a reporting layer, not a second test path.

- Go coverage upload reuses the existing `coverage.out` generated by
  `scripts/test.sh` / the CI `test` job.
- Frontend coverage upload reuses the existing Vitest `lcov.info` generated by
  `npm run test -- --run --coverage` / the CI `frontend` job.
- CI uploads coverage only when the workflow event is not `pull_request` and
  Codacy credentials are available as GitHub secrets. The `codacy-coverage` job
  is skipped at the workflow job boundary on `pull_request` events, before
  checkout, artifact download, secret injection, or repository scripts can run.
  This prevents PR-controlled code from reading Codacy secrets. Repository-token
  mode uses `CODACY_PROJECT_TOKEN`; account-token mode uses `CODACY_API_TOKEN`,
  `CODACY_ORGANIZATION_PROVIDER`, `CODACY_USERNAME`, and `CODACY_PROJECT_NAME`.
  If both token secrets exist, repository-token mode wins and account-token
  variables are unset before the reporter runs. The upload script also refuses
  all Codacy coverage upload on `pull_request` events before token-mode
  selection as defense in depth, but PR coverage upload remains disabled until
  a future SOW designs a safe path.
- Token values are never written to the repository, test logs, SOWs, or specs.
  Durable artifacts may name the expected secret variables only.
- Coverage upload runs in the non-required `codacy-coverage` job after the
  required `test` and `frontend` jobs succeed. Those required jobs generate and
  upload coverage artifacts only; they never call Codacy directly.
- A missing Codacy token must not make any job fail unless Codacy coverage upload
  has explicitly been promoted to a required gate in `quality-gates.md` and
  branch protection.
- While reporting-only, missing artifacts, missing or empty coverage files,
  reporter bootstrap download failures, invalid bootstrap files, or Codacy
  upload failures emit annotations but do not fail the PR. They become hard
  failures only after a future SOW promotes Codacy coverage to a required
  context.
- The Codacy job uploads Go and frontend coverage as partial reports, then sends
  the required `final` notification after the partial upload attempts even if
  one partial command fails. Frontend LCOV is normalized from frontend-local
  `src/...` and `./src/...` paths to repository-root `frontend/src/...` paths
  before upload.
- The upload state machine lives in `scripts/codacy-coverage-upload.sh`, not as
  inline workflow shell. Its hermetic self-test
  `scripts/test/codacy-coverage-upload-test.sh` covers no-token skip,
  pull-request skip before token selection, project-token precedence, missing
  or empty report combinations, Go-only upload, frontend-only upload,
  both-reports upload with `final` after both partials, partial-upload failure
  with `final` still attempted, final failure annotation, reporter download
  failure, invalid reporter bootstrap, and frontend LCOV path normalization.
- The workflow downloads Codacy's recommended reporter bootstrap script to a
  temporary file with HTTP failure checking and retries, verifies the file is
  a non-empty shell script, checks it with `bash -n`, and then executes it; it
  must not use a process substitution that can turn a failed download into an
  empty successful script.

## Codacy Finding Triage Tests

Codacy security/maintainability triage is scanner policy, but code changes made
to close findings still need project-native tests:

- Runtime behavior touched while closing a Codacy finding is covered by the
  normal Go/Frontend test layer for that surface before implementation changes
  land.
- False-positive suppressions on runtime source require executable evidence
  where practical. For example, a SQL-construction finding on a presenter query
  is paired with a handler/query test proving malicious filter values remain
  bound as parameters and do not change SQL structure.
- Test-only or tooling-only Codacy exclusions are backed by the existing tests
  for those paths: frontend tests remain under ESLint/typecheck/Vitest/Playwright,
  frontend scripts keep their own self-tests, shell scripts remain under
  ShellCheck where applicable, and repository-wide secrets/spec-drift gates still
  scan tracked files.
- Codacy configuration changes are validated by a hermetic config self-test, not
  just by `jq empty`. The self-test covers `.codacy/codacy.config.json`
  tool/pattern settings, its local Analysis CLI `exclude` mirror, and
  `.codacy.yml` Cloud path-exclusion settings, protecting against accidental
  broad runtime exclusions and against removing high-signal security patterns
  without a SOW rationale.

## CI Gates

The authoritative CI gate catalog and branch-protection contract lives in
`quality-gates.md`. This testing-strategy spec records testing-specific
requirements and the test surfaces each CI job exercises.

Current CI testing surfaces on `master` and PRs to `master`:

- `test`: Go build, `go test -race -count=1 -timeout=25m -coverprofile=coverage.out -covermode=atomic ./...`, deterministic adapter fuzz seed corpus, Go coverage artifact upload and threshold enforcement, coverage-gate self-test, benchmark compile smoke, and benchmark-gate self-test.
- `frontend`: ESLint, TypeScript typecheck, Vitest unit/component run with coverage and native per-directory thresholds, coverage artifact upload, coverage-config verifier/self-tests, Playwright E2E/axe, bundle-size self-test, and enforced bundle-size gate.
- `embed-smoke`: embedded frontend/server binary build, served UI smoke, and `/api/health` smoke.
- `gates`: cross-cutting testing infrastructure checks including lint-test, secrets scanner self-test/scan, spec-drift self-test/scan, Codacy coverage-upload self-test, Codacy config self-test, AI-attribution scan, local aggregate syntax check, and optional systemd unit lint.
- `codeql`: required CodeQL matrix jobs for `go`, `javascript-typescript`, and `actions`.
- `codacy-coverage`: non-required reporting job that uploads coverage artifacts through `scripts/codacy-coverage-upload.sh` when a usable Codacy token is available.

Nightly workflows run scheduled fuzz exploration, race stress, and
vulnerability refresh; they surface regressions on cadence and do not replace
per-PR gates.

## Performance Regression Tests

A benchmark suite covering the performance-critical paths (9 benchmarks across 6 packages): ai-agent v2 adapter `Scan` + `Tail` (`internal/adapters/aiagent_v2/bench_test.go`), claude-code adapter `Scan` + `Tail` (`internal/adapters/claude_code/bench_test.go`), Codex adapter `Scan` + `Tail` (`internal/adapters/codex/bench_test.go`), SQLite batch insert (`internal/ingest`), REST query (`internal/presenter`), SSE fanout (`internal/notify`).

The committed baseline is `bench/baseline.txt` (`-count=6`, with `goos/goarch/pkg/cpu` config lines). `scripts/check-bench.sh` runs `benchstat` against it and fails on a statistically-significant > 20% sec/op regression. It is a **local/workstation gate** — the baseline is not comparable to GitHub-runner hardware, so CI runs only the bench compile-smoke + the gate's self-test, not the regression comparison. Baseline refresh requires an explicit SOW (no auto-update). See `quality-gates.md` §Go — Benchmarks.

## Local Test Commands

```bash
go test ./...                            # all Go tests
go test -race ./...                      # race detector (CI)
go test ./internal/adapters/aiagent_v3   # one adapter
go test ./internal/adapters/aiagent_v3 -update-golden   # refresh expected.jsonl
go test ./... -bench=. -benchmem         # benchmarks

cd frontend && npm test                  # vitest
cd frontend && npm run e2e               # playwright
cd frontend && npm run e2e -- --headed   # debug
```
