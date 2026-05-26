# SOW-0009 - Go Static Analysis Stack

## Status

Status: open

Sub-state: drafted 2026-05-26 alongside SOW-0010 and SOW-0011 to land the Go-side quality gates spec'd in `.agents/sow/specs/quality-gates.md`. Awaiting operator approval. Prerequisite: SOW-0001 Chunk 2 (CI scaffolding workflow file) must be in place so this SOW extends an existing workflow rather than creating one from scratch.

## Requirements

### Purpose

Land a complete, locked-down Go static analysis chain that enforces every Go format/lint/static/security gate listed in `.agents/sow/specs/quality-gates.md` sections "Go — Format", "Go — Vet", "Go — Lint", and "Go — Security". The chain runs identically in CI and locally via `./scripts/lint.sh`, fails fast on any non-zero finding, and is version-pinned so reviewers see the same warnings the author saw. This SOW closes the gap between the spec (which describes the gates) and the running system (which currently has none of them wired).

### User Request

The operator's standing instruction is "every quality gate that can be automated, is automated" (`AGENTS.md` ownership model). The Quality Gates table in `AGENTS.md` and the full catalog in `.agents/sow/specs/quality-gates.md` enumerate the Go static analysis gates; they are not yet enforced because the Phase 1 CI scaffolding (SOW-0001 Chunk 2) only stubs the workflow. This SOW operationalizes the static analysis half of that table.

### Assistant Understanding

Facts:

- `.agents/sow/specs/quality-gates.md` lines 19-39 enumerate every linter and threshold expected. They are spec, not aspiration.
- `AGENTS.md` Quality Gates table (lines 102-122) commits the project to zero lint warnings, zero high/critical security findings, zero `go vet` warnings.
- `.agents/skills/project-quality-gates/SKILL.md` lines 34-50 list the same linters plus the exact commands.
- No `.golangci.yml`, `scripts/lint.sh`, or static-analysis CI step currently exists in the repo.
- The project ships under `golangci-lint` (the spec already chose it; no alternative is on the table).

Inferences:

- Many of the listed linters (gosec, errorlint, gocritic, gofumpt, gocyclo, unparam, prealloc) are stricter than stock `golangci-lint` defaults; the config file must enable each explicitly to avoid silent drift.
- `govulncheck` will produce findings the team has no control over (newly-disclosed CVEs in transitive dependencies); the per-push gate must coexist with a nightly schedule so a fresh CVE does not block an unrelated PR within the same hour.
- CI runtime budget for the full Go lint + static + security step should land under ~2 minutes on GitHub-hosted runners with the module + build cache warm; cold runs (cache miss) can reach ~5 minutes.
- The contract forbids un-linked `//nolint` directives (`project-quality-gates` skill, "Operating Rule" section, lines 8-11). Every suppression must point at an issue or SOW.

Unknowns:

- The exact `golangci-lint` minor version available at implementation time; pin to whatever is latest stable at that moment per the project's "always pin to latest stable" library policy.
- Whether `gofumpt` and `gofmt` produce overlapping or conflicting diffs in practice on the bootstrap codebase; the implementer measures during Chunk 1 and disables the redundant one if so (spec'd config keeps both, but if `gofumpt` is a strict superset of `gofmt` in the version used, the lint step can run `gofumpt -l` only and the script `scripts/lint.sh` runs both for belt-and-suspenders local feedback).
- Whether any `gosec` rule produces unavoidable false positives on the canonical event encoder or the SQLite query builders; if so, those nolints must be linked to a tracking SOW per the rule above.

### Acceptance Criteria

1. `.golangci.yml` exists at repo root and explicitly enables every linter named in `.agents/sow/specs/quality-gates.md` "Go — Lint" (govet, errcheck, staticcheck, unused, gosimple, ineffassign, gosec, revive, gofmt, goimports, bodyclose, noctx, errorlint, gocritic, gocyclo with max 15, gofumpt, misspell, nilerr, prealloc, unconvert, unparam, whitespace) with project-specific tuning recorded in inline comments. **Verification**: `golangci-lint linters` against the config shows every listed linter under "Enabled".
2. `scripts/lint.sh` exists, is executable, and runs in order: `gofmt -l`, `goimports -l`, `go vet ./...`, `golangci-lint run --timeout=5m`, `gosec -severity medium -confidence medium ./...`, `govulncheck ./...`. Fail-fast on any non-zero. **Verification**: `bash -n scripts/lint.sh` parses clean; manual run on the in-progress codebase exits 0 with no output.
3. `gosec` is wired with `-severity medium -confidence medium` and produces zero high/critical findings on the current tree. **Verification**: run output captured in the SOW Validation section.
4. `govulncheck` runs per push AND on a separate nightly scheduled workflow. **Verification**: two workflow files (or one workflow with two jobs guarded by `on.push` and `on.schedule`) exist under `.github/workflows/` and both trigger correctly per `gh workflow view`.
5. CI workflow extends the existing scaffold from SOW-0001 Chunk 2 with steps that invoke `scripts/lint.sh`. Caching is configured so a warm CI run completes the full Go static stack in under 2 minutes wall-clock. **Verification**: timed CI run logged in the SOW Validation section.
6. Each linter has its version effectively pinned through `.golangci-lint-version` (or an equivalent file the CI workflow reads) so a `golangci-lint` upgrade is an intentional, reviewable diff. **Verification**: the version file is committed and the CI workflow installs that exact version.
7. Zero un-linked `//nolint` directives in the codebase. **Verification**: `grep -RnE '//\s*nolint([^:]|$)' --include='*.go' .` returns no hits; any present `//nolint:rule // reason: <link>` is reviewed and the link is live.
8. `AGENTS.md` Quality Gates table and `.agents/skills/project-quality-gates/SKILL.md` need no edits beyond cross-references (they already enumerate these gates); if implementation discovers a divergence, the spec/skill is updated in the same commit per the spec-drift contract.

## Analysis

Sources checked:

- `.agents/sow/specs/quality-gates.md` (authoritative gate list with thresholds).
- `.agents/skills/project-quality-gates/SKILL.md` (runtime catalog with commands).
- `AGENTS.md` Quality Gates table.
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` (Chunk 2 establishes the CI workflow this SOW extends; Chunks 6-10 will be the first non-trivial Go code the gates run against).
- Sibling-project precedent: `~/src/ai-agent.git/.golangci.yml` and `~/src/netdata-ktsaou.git/.golangci.yml` for tuning conventions on similar Go codebases.

Current state:

- No `.golangci.yml`. No `scripts/lint.sh`. No CI lint step beyond whatever SOW-0001 Chunk 2 lands (a stub that runs `go vet` only, by current plan).
- No Go source yet beyond what SOW-0001 will add; the lint stack will be exercised against fresh code from the start, which is the cheapest moment to enforce strict rules.

Risks:

- **R1 — linter false positives blocking unrelated PRs**: gocritic and gosec are known for opinionated warnings on idiomatic Go. Mitigation: per-rule disable (with comment justifying why) is allowed in `.golangci.yml`; per-line `//nolint` is allowed only with a linked SOW/issue per the operating rule. Implementer documents the disable list in the config file inline.
- **R2 — govulncheck flapping on freshly-disclosed CVEs**: a CVE in `golang.org/x/net` disclosed Tuesday morning blocks every Tuesday-afternoon PR until a patched version is available. Mitigation: the per-push job is advisory-only for unrelated dependency CVEs; the nightly job is the gate that fails the build. The implementer decides during Chunk 4 whether to split via two jobs or via `continue-on-error` on the push job (CTO call; default to two jobs for clarity).
- **R3 — golangci-lint upgrade silently changing behavior**: a `golangci-lint` upgrade can enable new analyzers or change defaults. Mitigation: version pin per Acceptance Criterion 6; upgrades land as their own visible PR.
- **R4 — `gofumpt` vs `gofmt` redundancy**: `gofumpt` is stricter `gofmt`; running both wastes CI seconds. Mitigation: `scripts/lint.sh` runs both (local feedback is cheap); CI `.golangci.yml` enables both but the actual lint step uses `gofumpt` (stricter superset). Implementer confirms current-version behavior.
- **R5 — performance budget**: with caching cold, the stack can exceed 5 minutes; the spec budgets < 5 min for the entire `gates.sh`. Mitigation: GitHub Actions `actions/setup-go` cache for module + build, `actions/cache` for golangci-lint analysis cache. Measure and tune.

## Pre-Implementation Gate

Status: blocked (operator approval pending)

Problem / root-cause model:

- The project committed in `AGENTS.md` to a strict Go static analysis stack but has not yet implemented it. Every new Go line shipped without the stack accumulates undetected defects (errcheck misses, gosec misses, gocyclo creep). The cheapest moment to install strict tooling is before the codebase grows.

Evidence reviewed:

- `.agents/sow/specs/quality-gates.md` lines 19-39 (gate list).
- `AGENTS.md` lines 102-122 (committed thresholds).
- `.agents/skills/project-quality-gates/SKILL.md` lines 16-50 (commands and config requirements).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` Chunk 2 (CI scaffolding is prerequisite, not duplicate scope).

Affected contracts and surfaces:

- New: `.golangci.yml`, `scripts/lint.sh`, `.golangci-lint-version` (or equivalent), one new CI job step or workflow.
- Modified: existing CI workflow file from SOW-0001 Chunk 2 (extension only, no rewrite).
- Unaffected: production Go source, fixtures, frontend.

Existing patterns to reuse:

- Sibling repos `~/src/ai-agent.git/` and `~/src/netdata-ktsaou.git/` have working `.golangci.yml` files; the implementer reviews them for tuning conventions but does not copy verbatim (ai-viewer's gate spec is stricter, so the config is bespoke).
- `AGENTS.md` "Transparency in scripts" pattern (the `run()` helper) for `scripts/lint.sh` output ergonomics.

Risk and blast radius:

- Local-only impact at first run (no production system to break).
- Wrong threshold or missing linter is recoverable by editing `.golangci.yml` and re-running.
- The lint stack itself does not change runtime behavior; the risk is solely about developer ergonomics (false positives) and CI runtime budget.

Sensitive data handling plan:

- `.golangci.yml`, `scripts/lint.sh`, and CI workflow are all public artifacts; no sensitive data involved.
- `gosec` output never contains user data (it inspects source, not runtime).
- Implementer confirms before commit that no inline comment in the config references an internal customer name or URL.

Implementation plan:

1. **Spec read-back**: re-read `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md` at implementation start to confirm no edits landed between SOW approval and execution.
2. **`.golangci.yml`**: author the config with every linter explicitly enabled, gocyclo max 15, per-rule tuning documented inline.
3. **`scripts/lint.sh`**: author the aggregated script using the `run()` transparency helper.
4. **`.golangci-lint-version`**: pin the exact version.
5. **CI extension**: add a "Go static analysis" job that installs the pinned `golangci-lint`, runs `scripts/lint.sh`, and caches the module + golangci-lint analysis cache.
6. **`govulncheck` nightly workflow**: create `.github/workflows/govulncheck-nightly.yml` (or extend ci.yml with a scheduled job) running daily at a low-traffic UTC hour.
7. **Local exercise**: run `scripts/lint.sh` on the current tree; address any finding. Capture timing.
8. **CI exercise**: push a PR; capture CI timing; iterate on caching until under 2 min warm.
9. **Spec/skill cross-reference verification**: confirm `.agents/sow/specs/quality-gates.md` and `.agents/skills/project-quality-gates/SKILL.md` accurately describe what landed; update in the same commit if drift is found.
10. **External review** (per `project-second-opinions`): at least three reviewers in parallel on the diff.
11. **Address findings**, re-review, mark SOW completed.

Validation plan:

- Acceptance Criteria 1-8 each have a named verification method; evidence captured in the SOW Validation section at close.
- `scripts/lint.sh` runs locally with zero output (zero is the gate).
- CI runs the same script with the same result.
- Timing measurement: warm run < 2 min; cold run logged for future tuning.
- External reviewers (project-second-opinions skill) confirm no gate is silently weakened and no `//nolint` lacks a link.

Artifact impact plan:

- `AGENTS.md`: no change expected (the contract already lists these gates).
- Specs: `.agents/sow/specs/quality-gates.md` only if implementation discovers a needed correction (e.g. a linter renamed upstream).
- Runtime project skills: `.agents/skills/project-quality-gates/SKILL.md` updated only if commands or thresholds drift from what landed.
- End-user docs: none affected (this is internal tooling).
- SOW lifecycle: on success, this SOW moves to `.agents/sow/done/` with `Status: completed` in the same commit as the final configuration.

Open-source reference evidence:

- `golangci/golangci-lint @ <pinned-version>` upstream config examples will be consulted during Chunk 2; cite the exact tag in the SOW execution log when used.
- No workstation absolute paths to external OSS are recorded here.

Open decisions:

- None blocking implementation. Tuning details (per-rule disables, gofumpt-vs-gofmt redundancy, govulncheck advisory-vs-blocking on push) are CTO calls inside the SOW scope.

## Implications And Decisions

No operator decisions required. All choices in this SOW are technical and within the assistant's autonomous scope per the ownership model.

## Plan

1. `.golangci.yml` + version pin (low risk).
2. `scripts/lint.sh` (low risk; tooling only).
3. CI workflow extension + caching (medium risk; CI runtime sensitivity).
4. `govulncheck` nightly workflow (low risk; isolated job).
5. Exercise + tuning iteration (medium risk; false positives may force config changes).
6. External review + convergence.

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
