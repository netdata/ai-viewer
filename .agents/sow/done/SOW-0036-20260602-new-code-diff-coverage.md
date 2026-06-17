# SOW-0036 - New-Code-in-PR Diff Coverage Gate

## Status

Status: deferred — internal quality; no user-visible impact (2026-06-17)

Sub-state: filed 2026-06-02 as the deferred half of SOW-0010 (test infrastructure + coverage). SOW-0010 shipped the per-package + repo-wide **statement** coverage gate (`scripts/check-coverage.sh`, `internal/*` ≥ 80%); the **new-code-in-PR ≥ 90%** threshold was deferred here because it needs a diff↔coverage intersector plus its own self-tests — a self-contained, testable addition best done with focus. Awaiting operator approval.

## Requirements

### Purpose

Add a gate that fails a PR when the lines it ADDS/changes are covered below 90% (statement). This catches new untested code that the per-package aggregate gate can mask (a large well-covered package absorbs a small untested addition). Complements, does not replace, SOW-0010's per-package gate.

### Assistant Understanding

- SOW-0010 delivered `scripts/test.sh` (race + `-coverprofile=coverage.out -covermode=atomic`) and `scripts/check-coverage.sh` (per-package + aggregate statement gate on `internal/*`, `/cmd/` excluded, self-tested). `quality-gates.md` "Go — Coverage" labels new-code ≥ 90% as deferred to this SOW.
- The gate needs: (a) the added/changed lines from `git diff <base>...HEAD` (use `--unified=0` to get exact added line ranges), (b) the per-line covered/uncovered status from `coverage.out` (profile format: `file:sLine.sCol,eLine.eCol numStmts count` — a statement block is covered iff `count > 0`), (c) intersect added lines with covered statement blocks → new-code coverage %.
- Tooling options (CTO call at implementation): a small in-tree Go helper (zero deps; parses the profile + `git diff`), `diff-cover` (Python dep), or `gocovsh` (TUI, not a gate). Default to the in-tree Go helper for the smallest dependency footprint, mirroring the project's "plain tools" convention.

### Acceptance Criteria

1. A new-code coverage computor (in-tree Go helper preferred) takes `coverage.out` + a base ref, computes the statement coverage of added/changed lines in the PR diff, and fails when < 90%. Excludes exactly the set SOW-0010's gate excludes — a path is gated **iff** it contains `/internal/` and not `/cmd/` (so `/cmd/` binaries + dev-tools AND non-internal vendored Go under `frontend/node_modules/` are excluded) — and excludes pure-comment/blank additions.
2. Self-tests with synthetic diffs + profiles exercise pass + below-threshold miss (the gate is itself code that must be correct).
3. Wired into CI on `pull_request` (base = the PR base; build-failing) and documented as a local command. Statement-based (consistent with SOW-0010; branch coverage remains deferred — Go has no native branch coverage).
4. `quality-gates.md` + `project-quality-gates` + `project-testing` updated: new-code ≥ 90% moves from "deferred" to enforced; spec-drift clean.

## Analysis

Sources: SOW-0010 (`done/` after merge) — the shipped `check-coverage.sh` + the deferral note; `quality-gates.md` "Go — Coverage"; Go coverage profile format.

Risks:
- **R1 — diff base selection in CI**: `pull_request` events provide the base SHA; merge-base vs base-ref differences can mis-scope added lines. Mitigation: use `git merge-base origin/<base> HEAD` and `--unified=0`; self-test the line-range parser.
- **R2 — generated/excluded files**: added lines in `/cmd/`, non-internal vendored Go (e.g. `frontend/node_modules/`), generated code, or test files must be excluded consistently with SOW-0010's gated set. Mitigation: reuse SOW-0010's gated-set predicate — gated **iff** the path contains `/internal/` and not `/cmd/`. Do NOT reintroduce a `/cmd/`-only exclusion (that re-opens the vendored-Go defect SOW-0010 round 3 fixed).

## Pre-Implementation Gate

Status: blocked (operator approval pending; this is a backlog follow-up, not part of an active SOW)

(Filled when activated.)

## Implications And Decisions

No operator decisions required beyond approval to schedule. Tooling choice (in-tree helper vs `diff-cover`) is a CTO call within scope.

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
