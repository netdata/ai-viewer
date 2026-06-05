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

```
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

Non-gated aspirational aims (guide where to invest test effort; not enforced):

- ingest / store / canonical: aim > 90%.
- adapter packages: aim > 85%.
- presenter handlers: aim > 80%.
- Frontend: not formally measured (Vitest's c8 reports it but we don't gate on it; component tests + E2E are enough).

## Codacy Coverage Upload

Codacy coverage is a reporting layer, not a second test path.

- Go coverage upload reuses the existing `coverage.out` generated by
  `scripts/test.sh` / the CI `test` job.
- Frontend coverage upload reuses the existing Vitest `lcov.info` generated by
  `npm run test -- --run --coverage` / the CI `frontend` job.
- CI uploads coverage only when Codacy credentials are available as GitHub
  secrets. Repository-token mode uses `CODACY_PROJECT_TOKEN`; account-token mode
  uses `CODACY_API_TOKEN`, `CODACY_ORGANIZATION_PROVIDER`, `CODACY_USERNAME`, and
  `CODACY_PROJECT_NAME`.
- Token values are never written to the repository, test logs, SOWs, or specs.
  Durable artifacts may name the expected secret variables only.
- A missing Codacy token must not make the test jobs fail unless Codacy coverage
  upload has explicitly been promoted to a required gate in
  `quality-gates.md` and branch protection.

## CI Gates

Every commit on every branch:

1. `go vet ./...`
2. `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0`
3. `go test -race ./...`
4. `npm --prefix frontend run lint`
5. `npm --prefix frontend test`
6. `scripts/build.sh` (builds frontend + Go binaries)
7. Playwright E2E (subset of critical paths; the full set runs on `main`)

Zero warnings, zero failures, zero skipped tests without a referenced GitHub issue.

## Performance Regression Tests

A benchmark suite covering the performance-critical paths (5 benchmarks across 4 packages): adapter `Scan` + `Tail` (`internal/adapters/aiagent_v2/bench_test.go`), SQLite batch insert (`internal/ingest`), REST query (`internal/presenter`), SSE fanout (`internal/notify`).

The committed baseline is `bench/baseline.txt` (`-count=6`, with `goos/goarch/pkg/cpu` config lines). `scripts/check-bench.sh` runs `benchstat` against it and fails on a statistically-significant > 20% sec/op regression. It is a **local/workstation gate** — the baseline is not comparable to GitHub-runner hardware, so CI runs only the bench compile-smoke + the gate's self-test, not the regression comparison. Baseline refresh requires an explicit SOW (no auto-update). See `quality-gates.md` §Go — Benchmarks.

## Local Test Commands

```
go test ./...                            # all Go tests
go test -race ./...                      # race detector (CI)
go test ./internal/adapters/aiagent_v3   # one adapter
go test ./internal/adapters/aiagent_v3 -update-golden   # refresh expected.jsonl
go test ./... -bench=. -benchmem         # benchmarks

cd frontend && npm test                  # vitest
cd frontend && npm run e2e               # playwright
cd frontend && npm run e2e -- --headed   # debug
```
