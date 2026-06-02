# SOW-0010 - Test Infrastructure and Coverage Enforcement

## Status

Status: open

Sub-state: drafted 2026-05-26 alongside SOW-0009 and SOW-0011 to land the Go-side quality gates spec'd in `.agents/sow/specs/quality-gates.md`. Awaiting operator approval. Prerequisite: SOW-0001 Chunk 2 (CI scaffolding workflow file) must be in place so this SOW extends an existing workflow rather than creating one from scratch.

## Requirements

### Purpose

Land the Go test execution + coverage threshold enforcement layer. After this SOW, every push to the repo runs the full Go test suite with the race detector, computes line coverage with atomic counters, parses the PR diff to compute new-code coverage, and fails the build if any of the four thresholds in `.agents/sow/specs/quality-gates.md` "Go — Coverage" are missed. Race stress runs are wired at the cadences spec'd in "Go — Race Stress". Coverage reports are published as CI artifacts and as PR comments so the operator and reviewers see the deltas without leaving GitHub.

### User Request

`AGENTS.md` Hard Rule 5 — "Untested ≡ broken" — combined with the Quality Gates table commit lines 109-110: "Go test → all pass" and "Go coverage → ≥ 80% lines, ≥ 70% branches per package; new code ≥ 90%". The operator does not manually test code for the assistant; coverage is the executable proof of the test pyramid. This SOW operationalizes the test-execution and coverage half of the Quality Gates table.

### Assistant Understanding

Facts:

- `.agents/sow/specs/quality-gates.md` lines 41-77 define the test, coverage, and race-stress thresholds (repo-wide ≥ 80% lines; per-package changed code ≥ 80% lines and ≥ 70% branches; PR new code ≥ 90% lines; race `-count=10` local, `-count=3` per push, `-count=20` nightly).
- `.agents/skills/project-testing/SKILL.md` lines 64-72 mirror those thresholds and identify `scripts/check-coverage.sh` as the enforcement script.
- `.agents/skills/project-quality-gates/SKILL.md` lines 60-104 list the exact commands (`go test -race -coverprofile=coverage.out -covermode=atomic ./...`, `go tool cover`, race-stress commands).
- Go's built-in coverage (`-covermode=atomic`) reports **line** coverage. Branch coverage is not first-class in the standard toolchain.
- The repo currently has no `scripts/test.sh`, no `scripts/check-coverage.sh`, no coverage thresholds enforced in CI.

Inferences:

- The 70% branch-coverage threshold cannot be enforced with stock `go test` alone. Options for the implementer to evaluate during Chunk 1 (CTO decision, recorded in execution log): (a) treat "branch coverage" as an alias for `covermode=atomic` line coverage on conditional blocks via an AST-walker post-processor; (b) use a third-party tool (e.g. `gocov`, `gobco`); (c) hold branch coverage as an aspirational gate and ship only line coverage in this SOW with a follow-up SOW for branch coverage. The implementer chooses based on tool maturity at implementation time; the choice is documented in the execution log and reflected back into the spec if (c) is selected.
- Diff-coverage (new-code-in-PR ≥ 90%) requires parsing the PR diff against `coverage.out`; `gocovsh` and `diff-cover` (Python) are mature options; rolling a small Go helper is also acceptable. CTO decision during Chunk 2.
- Coverage report as a PR comment is best implemented via a CI job that uploads the HTML report as an artifact AND posts a comment with the summary; `actions/github-script` or `marocchino/sticky-pull-request-comment` are the common patterns.
- `goyek` and similar task-runner frameworks are out of scope per `.agents/skills/project-coding/SKILL.md` "prefer plain scripts" convention; this SOW uses raw bash scripts.

Unknowns:

- Whether the in-progress codebase (SOW-0001) will have packages so small that a single uncovered helper drops the package below 80%. Mitigation: thresholds are a CI gate, not an authoring constraint; if a real package hits this in Phase 1, the fix is more tests or a justified suppression SOW.
- Whether nightly `-count=20` race stress runs flake on GitHub-hosted runners (shared CPU). Mitigation: if flake rate exceeds 1%, the implementer reduces to `-count=10` nightly and files a follow-up SOW to debug the flake source rather than weakening the spec silently.

### Acceptance Criteria

1. `scripts/test.sh` exists, executable, runs `go test -race -coverprofile=coverage.out -covermode=atomic -count=1 ./...`. **Verification**: `bash -n scripts/test.sh` parses clean; run on the current tree exits 0; `coverage.out` is produced.
2. `scripts/check-coverage.sh` exists, executable, takes `coverage.out` plus the PR diff (or no diff for full-tree mode) and enforces all four thresholds: repo-wide ≥ 80% lines; per-changed-package ≥ 80% lines and ≥ 70% branches; new-code-in-PR ≥ 90% lines. Output is human-readable and actionable (names the failing package and the missing percentage). **Verification**: synthetic input fixtures with known coverage exercise each threshold; script exits non-zero on each miss and zero when all pass.
3. Branch coverage strategy decided and documented in the SOW execution log per the CTO note above; if option (c) is chosen, this SOW updates `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md` to label branch coverage as "deferred to SOW-XXXX" with a tracked follow-up SOW. **Verification**: spec/skill diff in the same commit; tracked follow-up SOW in `.agents/sow/pending/` if applicable.
4. Race stress is wired at three cadences: `-count=10` documented as the local pre-commit command for concurrency-touching changes (in `scripts/test.sh` as a flag or separate `scripts/test-stress.sh`); `-count=3` per push in CI; `-count=20` nightly in CI. **Verification**: CI workflow file shows both schedules; local script supports the stress flag.
5. CI publishes the HTML coverage report (`go tool cover -html=coverage.out -o coverage.html`) as a per-PR artifact retained for 14 days. **Verification**: PR run shows the artifact in the "Artifacts" section.
6. CI posts a sticky PR comment summarizing: repo-wide coverage, per-package coverage for packages touched by the PR, new-code coverage. Comment updates in place on subsequent pushes (no comment spam). **Verification**: open a sample PR; verify single comment that updates across pushes.
7. README gains a coverage badge driven by the repo-wide line coverage from the latest main-branch run. (Optional but recommended; if the badge service requires anything beyond what the workflow already produces, the implementer may defer this to a follow-up SOW.) **Verification**: badge renders in README and updates after a main-branch run.
8. `./scripts/gates.sh` invokes `scripts/test.sh` followed by `scripts/check-coverage.sh` (with empty-diff = full-tree mode) so the canonical local pre-commit gate enforces coverage identically to CI. **Verification**: `scripts/gates.sh` exit code = 0 on a passing tree; non-zero on a synthetic failing tree.

## Analysis

Sources checked:

- `.agents/sow/specs/quality-gates.md` (authoritative coverage thresholds and race-stress cadences).
- `.agents/sow/specs/testing-strategy.md` (referenced in `AGENTS.md`; consulted for any conflicting threshold).
- `.agents/skills/project-testing/SKILL.md` (test commands and mandatory test kinds).
- `.agents/skills/project-quality-gates/SKILL.md` (runtime catalog).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` (Chunk 2 CI scaffolding is the prerequisite hook point).

Current state:

- No `scripts/test.sh`, no `scripts/check-coverage.sh`, no coverage enforcement of any kind. Phase 1 SOW-0001 plans to land `go test` in CI but does not promise the coverage gate.
- Race-stress wiring at any cadence is absent.

Risks:

- **R1 — branch coverage tool immaturity**: Go's standard toolchain reports line coverage only. Third-party branch-coverage tools have varying maintenance status. Mitigation: CTO decision in Chunk 1 with three named options; document choice in execution log; do not ship a half-built branch coverage gate.
- **R2 — diff-coverage tooling**: rolling a Go helper is small but the wrong abstraction if `diff-cover` or `gocovsh` already handles it. Mitigation: implementer surveys both at Chunk 2 start; default is the smallest dependency footprint that meets the spec.
- **R3 — `-count=20` flake under shared-CPU CI**: race detector under high stress can surface real races but can also flake on contended CI runners. Mitigation: nightly schedule, not blocking per-push; auto-file issue on flake recurrence (Acceptance Criterion in SOW-0011 covers fuzz-crash auto-issue filing; same pattern can extend here).
- **R4 — coverage threshold tripping on tiny packages**: a 4-line package with one uncovered branch drops below 80%. Mitigation: thresholds are intentional; the fix is tests, not threshold lowering. If a package is genuinely too small to be meaningfully covered, the implementer files a follow-up SOW with a per-package waiver narrative — never a silent threshold edit.
- **R5 — local-vs-CI coverage divergence**: `-coverprofile` can produce slightly different counts on different Go minor versions. Mitigation: pin the Go version in CI and document the local `go version` expectation in `scripts/test.sh`.

## Pre-Implementation Gate

Status: blocked (operator approval pending)

Problem / root-cause model:

- The "Untested ≡ broken" contract is unenforceable without an automated coverage gate. Today the project commits to thresholds in spec but has no script that fails the build when those thresholds are missed. Every PR shipped without the gate erodes the safety net.

Evidence reviewed:

- `.agents/sow/specs/quality-gates.md` lines 41-77 (thresholds and commands).
- `.agents/skills/project-testing/SKILL.md` lines 14-46 (test commands).
- `.agents/skills/project-quality-gates/SKILL.md` lines 60-104 (runtime catalog).
- `AGENTS.md` Hard Rule 5 (Untested ≡ broken) and Quality Gates table.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunk 2 (prerequisite).

Affected contracts and surfaces:

- New: `scripts/test.sh`, `scripts/check-coverage.sh`, `scripts/test-stress.sh` (or stress flag in `scripts/test.sh`), CI job extensions for coverage publication and PR comment.
- Modified: existing CI workflow from SOW-0001 Chunk 2 (extension); `scripts/gates.sh` (this SOW or a sibling SOW; if scripts/gates.sh does not yet exist as of execution, this SOW creates it as a thin wrapper calling lint.sh + test.sh + check-coverage.sh).
- Unaffected: production Go source (no behavior change), fixtures, frontend.

Existing patterns to reuse:

- `AGENTS.md` "Transparency in scripts" pattern for `scripts/test.sh` and `scripts/check-coverage.sh` output ergonomics.
- Sibling repos `~/src/ai-agent.git/scripts/` for any prior test-script conventions (read-only reference).
- GitHub Actions `marocchino/sticky-pull-request-comment` is the dominant pattern for sticky PR comments; cite the upstream commit when pinning.

Risk and blast radius:

- Local-only impact (workstation tool + CI workflow).
- Wrong threshold or wrong diff-parsing logic is recoverable by editing the script; no production data at risk.
- A spuriously-failing coverage gate can block legitimate PRs; the operator's stated preference is "fix the root cause, never weaken the gate" (`AGENTS.md` and `project-quality-gates` skill).

Sensitive data handling plan:

- Scripts, CI workflows, and coverage reports are all public artifacts; no sensitive data involved.
- Coverage HTML reports include source code excerpts; the source itself is public, so no additional redaction is needed.
- Implementer confirms no inline comment in scripts references internal data.

Implementation plan:

1. **Spec read-back**: re-read `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-testing/SKILL.md` at start; confirm no drift since SOW approval.
2. **Branch coverage decision (CTO call, documented in execution log)**: pick option (a), (b), or (c) from the Inferences list. If (c), file the follow-up SOW in `.agents/sow/pending/` in the same commit.
3. **`scripts/test.sh`**: author with race + coverprofile + atomic mode; document the local stress invocation.
4. **`scripts/check-coverage.sh`**: author with the four-threshold check; include a `--full-tree` mode (no diff input) and a `--diff` mode (PR diff input). Output is line-precise and actionable.
5. **Diff-coverage tooling choice**: pick `diff-cover`, `gocovsh`, or a tiny in-tree Go helper; pin version; document in execution log.
6. **CI extension — coverage job**: add a job that runs `scripts/test.sh`, runs `scripts/check-coverage.sh --diff`, uploads coverage.html as artifact, and posts a sticky PR comment.
7. **CI extension — race stress**: add `-count=3` per push (extend the existing test step or add a second step); add `-count=20` nightly via scheduled workflow (or scheduled job in ci.yml).
8. **`scripts/gates.sh`**: ensure it calls `scripts/test.sh` and `scripts/check-coverage.sh --full-tree` after `scripts/lint.sh` (lint.sh comes from SOW-0009).
9. **README coverage badge**: implement if low-friction; defer with a tracked SOW if it requires non-trivial third-party setup.
10. **Local exercise**: run `scripts/test.sh` and `scripts/check-coverage.sh` against the current tree; iterate.
11. **CI exercise**: open a sample PR; verify the comment posts, the artifact uploads, the thresholds fail correctly on a synthetic miss.
12. **Spec / skill sync**: update `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md` if any threshold or command differs from what landed (it should not, but the spec-drift contract makes this a mandatory check).
13. **External review** (per `project-second-opinions`): at least three reviewers in parallel on the diff.
14. **Address findings**, re-review, mark SOW completed.

Validation plan:

- Acceptance Criteria 1-8 each have a named verification method.
- Synthetic input fixtures with known coverage exercise each threshold-miss path in `scripts/check-coverage.sh` (this is testing the test infrastructure itself; the assistant must not skip this).
- CI run on a real PR captures: timing, artifact presence, comment correctness.
- External reviewers confirm: no threshold silently weakened, no `t.Skip` introduced, branch-coverage decision is honest.

Artifact impact plan:

- `AGENTS.md`: no change expected.
- Specs: `.agents/sow/specs/quality-gates.md` updated only if branch-coverage option (c) is chosen, or if any command/threshold differs from what landed.
- Runtime project skills: `.agents/skills/project-quality-gates/SKILL.md` and `.agents/skills/project-testing/SKILL.md` reviewed for command accuracy; updated in the same commit if any drift.
- End-user docs: README updated if the coverage badge lands.
- SOW lifecycle: on success, this SOW moves to `.agents/sow/done/` with `Status: completed` in the same commit as the final scripts and CI updates. A follow-up branch-coverage SOW is opened in `.agents/sow/pending/` if option (c) is chosen.

Open-source reference evidence:

- `marocchino/sticky-pull-request-comment @ <pinned-tag>` and the diff-coverage tool chosen in Chunk 5 will be cited in the execution log with exact tag/commit.
- No workstation absolute paths to external OSS recorded here.

Open decisions:

- Branch coverage tooling option (a / b / c) — CTO call inside the SOW, recorded in execution log.
- Diff-coverage tooling (third-party vs in-tree helper) — CTO call inside the SOW, recorded in execution log.

## Implications And Decisions

No operator decisions required. The two open items in the Pre-Implementation Gate are CTO calls within the assistant's autonomous scope per the ownership model.

## Plan

1. Branch coverage decision (low effort, gates everything else).
2. `scripts/test.sh` (low risk).
3. `scripts/check-coverage.sh` with synthetic-fixture self-tests (medium risk; the script is itself code that needs to be correct).
4. CI coverage job + artifact + sticky comment (medium risk; CI integration).
5. CI race-stress wiring at three cadences (low risk; mechanical).
6. `scripts/gates.sh` integration (low risk).
7. README badge (low effort or deferred).
8. Exercise + tuning iteration.
9. External review + convergence.

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
