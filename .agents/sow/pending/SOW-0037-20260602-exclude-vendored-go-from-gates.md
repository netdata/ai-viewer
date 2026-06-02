# SOW-0037 - Exclude Vendored Go (frontend/node_modules) From All `./...` Gates

## Status

Status: open

Sub-state: filed 2026-06-02 from a defect found during SOW-0010 review. The acute instance (the coverage gate) was fixed in SOW-0010; this SOW closes the rest of the class. Awaiting operator approval.

## Requirements

### Purpose

Vendored Go that a frontend npm dependency ships (e.g. `flatted` ships a Go port at `frontend/node_modules/flatted/golang/pkg/flatted/flatted.go`) lives **inside** the Go module tree — there is no `go.mod` under `frontend/` — so `go list ./...` includes it and **every `./...`-based gate walks it**: `go test`, `go vet`, `gosec`, `govulncheck`.

- `golangci-lint` already excludes it (`.golangci.yml` `linters.exclusions.paths: frontend/node_modules`) — verified clean.
- SOW-0010's coverage gate now excludes it (`scripts/check-coverage.sh` gates only `/internal/`, not `/cmd/`).
- **Still walking it:** `gosec ./...` and `govulncheck ./...` (`scripts/lint.sh`), and any standalone `go vet ./...`.

These are **currently clean** only because `flatted` is a trivial serialization helper. A future vendored Go file with a gosec finding (e.g. an unhandled error, weak RNG) or a vuln would spuriously fail our gates, forcing us to pollute our own config with third-party suppressions. Close the class: route every `./...` gate through one "core packages only" list that excludes `node_modules`, so no third-party vendored Go can ever trip our gates.

### Assistant Understanding

- Evidence (2026-06-02): `go list ./...` includes `github.com/netdata/ai-viewer/frontend/node_modules/flatted/golang/pkg/flatted`; it appears in `coverage.out` at 0%. `golangci-lint run ./frontend/node_modules/flatted/...` → "0 issues" (excluded via `.golangci.yml`). `scripts/lint.sh` runs `gosec ... ./...` and `govulncheck ./...` (bare `./...`).
- Go's tool ignores dirs named `testdata` and those starting with `.`/`_`, but **not** `node_modules`; hence the leak.
- `gosec` supports `-exclude-dir`. `govulncheck` has no exclude flag → pass an explicit package list (`govulncheck $(go list ./... | grep -v /node_modules/)`), confirming it still analyzes all our reachable code.

### Acceptance Criteria

1. A single source of the "core" package set (a tiny helper, e.g. `go list ./... | grep -v '/node_modules/'`, or `gosec -exclude-dir`) is used by `gosec` and `govulncheck` in `scripts/lint.sh` and CI instead of bare `./...`. `golangci-lint` already excludes `node_modules` (verify, keep).
2. Determine whether `go vet` runs standalone anywhere (vs only via golangci-lint's `govet`, which is already excluded); if standalone, scope it the same way.
3. **Verification**: a deliberately gosec-trippable construct planted under a vendored-style path is NOT flagged (excluded), while the same construct under `internal/` IS flagged — proving the scoping excludes only vendored code, not our own.
4. `scripts/lint.sh` + `.github/workflows/ci.yml` updated; `quality-gates.md` documents the unified "exclude `node_modules` from every `./...` gate" principle; spec-drift clean.

## Analysis

Sources: SOW-0010 `done/` (the coverage-gate fix + the flatted evidence); `.golangci.yml` exclusions; `scripts/lint.sh` gosec/govulncheck invocations; Go package-discovery rules.

Risks:
- **R1 — govulncheck reachability**: passing an explicit package list must not drop any of our own packages from vuln analysis. Mitigation: derive the list from `go list ./...` minus `/node_modules/` only; assert the count equals all-packages-minus-vendored.
- **R2 — over-exclusion**: a `grep -v node_modules` that is too broad could skip a legitimately-named package. Mitigation: anchor on the path segment `/node_modules/`.

## Pre-Implementation Gate

Status: blocked (operator approval pending; backlog follow-up, not part of an active SOW)

(Filled when activated.)

## Implications And Decisions

No operator decisions required beyond approval to schedule. Exclusion mechanism (helper vs `-exclude-dir`) is a CTO call within scope.

## Execution Log

Pending.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
