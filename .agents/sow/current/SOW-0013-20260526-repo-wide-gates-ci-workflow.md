# SOW-0013 - Repo-Wide Gates + Canonical CI Workflow: secrets scan, spec drift, gates.sh, ci.yml, branch protection, Dependabot, CodeQL

## Status

Status: in-progress

Sub-state: opened 2026-06-04 under the operator's standing backlog mandate ("proceed"). Predecessor SOWs 0009/0010/0011 (Go quality/security/bench) and 0012 (frontend quality stack) are ALL delivered, so the dependency is satisfied. Drafted greenfield 2026-05-26, but two artifacts already landed via those SOWs (`scripts/scan-secrets.sh` — SOW-0009/0017; `.github/workflows/ci.yml` with 5 jobs lint/test/frontend/embed-smoke/gates — SOW-0009..0012), so the real scope is the GAPS (reconciled in the Execution Log 2026-06-04): `spec-drift.sh`, `gates.sh`, `codeql.yml`+config, `dependabot.yml`, `workflows-checks.yaml`, registering required status checks, + the doc invariants. Depends on SOWs 0009 (Go quality stack), 0010 (Go security + fuzzing), 0011 (Go benchmarks + race stress), and 0012 (frontend quality stack) being delivered first OR landing in the same PR series. This SOW is the **integration** of all four — it produces the canonical `scripts/gates.sh`, the canonical `.github/workflows/ci.yml`, plus the cross-cutting gates (secrets scan, spec drift) and the supporting infrastructure (branch protection, Dependabot, CodeQL).

## Requirements

### Purpose

Lock down the repo-wide quality contract so that everything `quality-gates.md` documents is actually enforced — both at the operator's workstation pre-commit and on every CI push. The four predecessor SOWs land their language-specific gates; this SOW lands the cross-cutting gates that aren't language-specific (secrets, spec drift), the aggregate orchestrator (`scripts/gates.sh`), the canonical CI workflow (`.github/workflows/ci.yml`), and the surrounding infrastructure (branch protection enforcing required status checks, Dependabot for the three ecosystems we use, CodeQL for static security analysis). After this SOW, "local pass + CI fail" becomes investigatable as a defect rather than accepted as drift.

### User Request

Implicit in the bootstrap operating contract (`AGENTS.md` Quality Gates section and Operating Contract rule 4: "all configured quality gates must pass" before any work is reported done) and explicit in `quality-gates.md` Aggregate Scripts section: "`./scripts/gates.sh` — every gate above, in order, fail-fast. The canonical pre-commit gate" and "CI uses the same scripts so local and CI behavior cannot diverge". This SOW makes those statements true.

### Assistant Understanding

Facts:

- `quality-gates.md` lists `scripts/lint.sh`, `scripts/test.sh`, and `scripts/gates.sh` as aggregate scripts CI runs the same way locally. Performance target: full local `gates.sh` < 5 minutes on the operator's workstation.
- Cross-cutting gates documented in `quality-gates.md`: Secrets Scan (`scripts/scan-secrets.sh`) and Spec Drift (`scripts/spec-drift.sh`). Neither script exists yet.
- `project-specs-sync/SKILL.md` lists drift indicators that `scripts/spec-drift.sh` must lint: REST endpoints registered vs. `specs/rest-api.md`; SSE event types vs. `specs/sse-protocol.md`; SQLite columns in migrations vs. `specs/data-model.md`; canonical event fields vs. `specs/canonical-events.md`; adapter probes in discovery code vs. `specs/adapter-<name>.md`.
- Repo is `netdata/ai-viewer`, public on GitHub from day one (recorded as decision in SOW-0001). GitHub Actions is the CI platform (also recorded in SOW-0001).
- GitHub's branch protection API supports required status checks via `PATCH /repos/{owner}/{repo}/branches/{branch}/protection`. The required-checks list is keyed by job name; renaming a job without updating protection silently disables the check.
- Dependabot config (`.github/dependabot.yml`) supports Go modules, npm, and GitHub Actions ecosystems. Weekly cadence is the standard balance between freshness and noise.
- CodeQL via `github/codeql-action` supports Go and JavaScript/TypeScript out of the box; weekly schedule + per-push runs catch newly-disclosed query results.

Inferences:

- The < 5 min local `gates.sh` target is achievable for Phase 1 surface (small Go codebase, small frontend) but requires careful sequencing — slow gates (fuzzing, bundle build) run last so fast feedback comes first. Profiling is mandatory; if any single gate exceeds 60 s in isolation it gets called out as a candidate for parallelization.
- CI total wall-clock < 8 min is achievable by splitting into parallel jobs (`lint`, `test`, `frontend`, `gates`) where each consumes < 5 min, with the longest-running job being the wall-clock floor.
- Required status checks are brittle to job renames. Mitigation: commit a small `.github/workflows-checks.yaml` (operator-readable, not consumed by Actions) that documents the current required check names; any SOW that renames a job is required to update both files and re-run `gh api -X PATCH` to update protection in the same commit.
- CodeQL on a TypeScript + Go repo produces some false positives. Mitigation: suppressions go in `.github/codeql/config.yml` with linked-issue rationale, never in source comments without a tracking issue.
- "Local pass + CI fail" is almost always one of three causes: environment (different Go version, different Node version, different OS), test isolation (test cache, leftover state), or timing (slower CI hardware). The aggregate-script invariant means CI runs the same commands; remaining divergence is investigatable from the asymmetric input.

Unknowns:

- Exact current required-status-check names will be known only once SOWs 0009–0012 land and CI is wired. Names are committed to `.github/workflows-checks.yaml` on this SOW's delivery, and the `gh api -X PATCH` invocation that registers them happens once after merge (one-time op).
- Whether `gh api` patching of branch protection requires a fine-grained PAT or works with the default `GITHUB_TOKEN`; to be verified at implementation. If the default token lacks the scope, the SOW documents the one-time PAT-based setup the operator runs.
- Whether spec-drift.sh's pattern-matching for "endpoints registered in `internal/presenter/`" needs structural parsing (AST) or simple grep is sufficient; to be decided during implementation based on the actual call site shapes.

### Acceptance Criteria

1. **`scripts/scan-secrets.sh` exists and enforces zero hits.** Implements the secret pattern set from `quality-gates.md`: `[A-Za-z0-9_-]{32,}` near keywords `key|token|secret|password|bearer`; `sk-[A-Za-z0-9]+` (OpenAI); `xox[bpas]-[A-Za-z0-9-]+` (Slack); `AKIA[0-9A-Z]{16}` (AWS). Allow-list for `[REDACTED_SECRET]` and other documented placeholders. Scans `testdata/`, `internal/`, `cmd/`, `frontend/src/`, `frontend/tests/`. **Verification**: script exits 0 on the delivery commit; a planted secret in a scratch file during dev confirms each pattern catches its intended class.
2. **`scripts/spec-drift.sh` exists and enforces zero drift.** Implements the five drift indicators from `quality-gates.md` and `project-specs-sync/SKILL.md`: REST endpoints, SSE event types, SQLite columns, canonical event fields, adapter probes. Each indicator has a clear "found in code but not spec" and "found in spec but not code" report. **Verification**: script exits 0 on the delivery commit; a planted spec/code mismatch (synthetic test, removed before commit) confirms each indicator catches the intended drift class.
3. **`scripts/gates.sh` is the canonical aggregate.** Runs every gate from `quality-gates.md` in order, fail-fast, with clear section headers and per-section timing. Performance: completes in under 5 minutes on the operator's workstation for the Phase 1 surface size. **Verification**: timed run captured in `## Validation`; per-section timings logged.
4. **`.github/workflows/ci.yml` is the single CI workflow.** Job split: `lint`, `test`, `frontend`, `gates`, running in parallel where independent. Caching configured for: Go module cache (`actions/cache` keyed on `go.sum`), npm cache (`actions/setup-node` with `cache: 'npm'`), golangci-lint cache (per `golangci/golangci-lint-action` docs). CI invokes the same `scripts/*.sh` the operator runs locally. **Verification**: workflow file present; first push runs successfully; per-job timing captured; total wall-clock < 8 min.
5. **Required status checks registered on `master`.** A one-time setup invocation of `gh api -X PATCH /repos/netdata/ai-viewer/branches/master/protection` registers the four job names as required. **Verification**: `gh api /repos/netdata/ai-viewer/branches/master/protection` shows the four required checks; an intentional failing CI on a test PR blocks merge.
6. **Required-check names documented in `.github/workflows-checks.yaml`.** Operator-readable, not consumed by Actions, lists current required check names. Any future SOW that renames a job is required to update this file and re-run the `gh api -X PATCH` invocation in the same commit. **Verification**: file present; contents match the names registered via `gh api`; this requirement is added to `project-quality-gates/SKILL.md`.
7. **`.github/dependabot.yml` configured for three ecosystems.** Go modules (root `go.mod`), npm (`/frontend`), GitHub Actions (`.github/workflows/`). Weekly cadence. Grouping for minor/patch updates to reduce PR noise. **Verification**: file present; first weekly run produces PRs for any pending updates within 7 days of merge.
8. **`.github/workflows/codeql.yml` configured for Go + TypeScript.** Per-push on master + weekly schedule. Suppressions (if any) in `.github/codeql/config.yml` with linked-issue rationale. **Verification**: workflow file present; first run completes with zero high-severity findings; any suppression has a tracking issue.
9. **Local-pass invariant documented and enforced.** `scripts/gates.sh` and the CI workflow invoke the same underlying scripts. Any divergence (local pass + CI fail) is investigated as a defect, not retried. The invariant is captured in `project-quality-gates/SKILL.md`. **Verification**: spec update in same commit as the scripts.
10. **Specs updated in the same commit as the implementation.** `quality-gates.md` adds the cross-cutting gate commands; `project-quality-gates/SKILL.md` adds the local-pass invariant note; `project-specs-sync/SKILL.md` cross-references `scripts/spec-drift.sh` as the automated check. **Verification**: diff of specs alongside scripts and workflow files.

## Analysis

Sources checked (at SOW drafting):

- `.agents/sow/specs/quality-gates.md` — full gate catalog including Secrets Scan, Spec Drift, Aggregate Scripts, Performance Target sections.
- `.agents/skills/project-quality-gates/SKILL.md` — exact commands, "When a Gate Fails" protocol, "Adding a New Gate" protocol.
- `.agents/skills/project-specs-sync/SKILL.md` — drift indicators and detection plan; cross-references the future `scripts/spec-drift.sh`.
- `AGENTS.md` — Quality Gates table, Operating Contract rules 4 (untested ≡ broken) and 6 (no silent failures).
- `.agents/sow/current/SOW-0001-phase-1-foundation.md` — Chunk 2 (CI scaffolding) noted that the full gate set lands in SOW-0009–0013; this SOW is the integration.
- GitHub REST API documentation for branch protection: `repos/{owner}/{repo}/branches/{branch}/protection` (`required_status_checks.contexts`).
- Dependabot `.github/dependabot.yml` schema (current stable).
- GitHub CodeQL action documentation (current stable).

Current state of CI/gates tooling in the repo:

- No `scripts/scan-secrets.sh`, no `scripts/spec-drift.sh`, no `scripts/gates.sh` yet.
- No `.github/workflows/ci.yml` yet (Chunk 2 of SOW-0001 lands a minimal scaffold; this SOW is the canonical version).
- No `.github/dependabot.yml`, no `.github/workflows/codeql.yml`, no `.github/codeql/config.yml`, no `.github/workflows-checks.yaml` yet.
- No branch protection configured on `master` (the repo currently allows direct pushes to master per default GitHub settings).

Risks:

- **R1 — gate runtime > 5 min locally**: if total `gates.sh` exceeds 5 min on the workstation, the operator's pre-commit feedback loop degrades. Mitigation: each gate's runtime is profiled during implementation; the slowest gates (fuzz, bundle build) run last so the fast-fail signal arrives early; if total exceeds 5 min, parallelization within `gates.sh` is added (background jobs with `wait`) before any gate is dropped or weakened. The target is documented; exceeding it triggers a follow-up SOW to profile and parallelize, never to lower the bar.
- **R2 — CodeQL false positives**: TypeScript + Go cross-stack analysis can produce queries that don't apply (e.g. "untrusted input flowing into HTML" on test-fixture loaders). Mitigation: suppressions live in `.github/codeql/config.yml` and each suppression has a linked GitHub issue documenting the rationale and the conditions under which the suppression would be removed. Inline source comments (`// codeql[]`) without a tracking issue are forbidden.
- **R3 — required-status-checks regression on job rename**: if a future SOW renames a CI job (e.g. `lint` → `go-lint`) without updating the branch protection `contexts` array, the rename silently disables the required check — PRs merge without the gate running. Mitigation: `.github/workflows-checks.yaml` documents the current required check names and is a tracked file; `project-quality-gates/SKILL.md` adds an "Adding or Renaming a CI Job" subsection that mandates updating both the workflow and the protection rule in the same commit, with the `gh api -X PATCH` invocation captured in the commit body.
- **R4 — Dependabot PR noise**: three ecosystems × weekly = potentially 5–15 PRs/week. Mitigation: grouping minor/patch updates per ecosystem reduces this to ~3 PRs/week; major updates remain individual PRs (they may need a SOW per `AGENTS.md` library version policy).
- **R5 — spec-drift.sh false positives**: REST endpoint discovery via grep on `internal/presenter/` may produce false positives for handler-factory patterns. Mitigation: if grep proves insufficient, the script uses Go's `go/ast` package to parse handler registrations structurally; the decision is made during implementation based on actual call site shapes. False positives are NOT silenced — they indicate a code structure that the script should learn to recognize.
- **R6 — secret-scanner false positives on legitimate long strings**: hashes, UUIDs, base64-encoded test fixtures may match `[A-Za-z0-9_-]{32,}`. Mitigation: the allow-list is keyword-anchored (must be near `key|token|secret|password|bearer` to match the broad pattern); specific patterns (`sk-`, `xox`, `AKIA`) match without keyword anchoring. Documented placeholders (`[REDACTED_SECRET]`, `example.invalid`) are allow-listed.
- **R7 — `gh api` PAT scope**: branch protection writes may require fine-grained PAT with `Administration:write` scope rather than the default `GITHUB_TOKEN`. Mitigation: tested during implementation; if PAT required, the one-time setup is documented in the SOW's `## Validation` and in `docs/setup.md` so the operator can run it after first merge.

## Pre-Implementation Gate

Status: ready (SOWs 0009–0012 delivered; opened under the operator's standing backlog mandate + "proceed". Current-state reconciled 2026-06-04 — see Execution Log.)

Problem / root-cause model:

- `quality-gates.md` documents the gate catalog and `project-quality-gates/SKILL.md` documents the commands, but the cross-cutting infrastructure (aggregate scripts, CI workflow, branch protection, Dependabot, CodeQL) does not exist. Without this infrastructure, the "every gate runs in CI on every push" commitment cannot be honored, and the "local pass + CI fail is investigated as a defect" invariant cannot exist (because local and CI don't yet share a code path).

Evidence reviewed:

- `quality-gates.md` Aggregate Scripts section: explicitly names `scripts/lint.sh`, `scripts/test.sh`, `scripts/gates.sh` and states "CI uses the same scripts so local and CI behavior cannot diverge".
- `quality-gates.md` Secrets Scan and Spec Drift sections: specify patterns and indicators.
- `project-specs-sync/SKILL.md` Spec Drift Detection section: lists drift indicators and references the future `scripts/spec-drift.sh`.
- `AGENTS.md` Quality Gates section: top-level commitment.
- GitHub branch protection API documentation.
- GitHub Dependabot config schema.
- `github/codeql-action` README.

Affected contracts and surfaces:

- New scripts: `scripts/scan-secrets.sh`, `scripts/spec-drift.sh`, `scripts/gates.sh` (the canonical aggregate).
- New CI: `.github/workflows/ci.yml` (canonical), `.github/workflows/codeql.yml`, `.github/dependabot.yml`, `.github/codeql/config.yml` (if any suppression needed), `.github/workflows-checks.yaml` (operator-readable required-check names).
- One-time external operation: `gh api -X PATCH /repos/netdata/ai-viewer/branches/master/protection` to register required status checks.
- Modified: `scripts/lint.sh`, `scripts/test.sh` (extended to integrate with the new orchestrator if needed).
- Modified docs: `quality-gates.md` (add the aggregate-script and CI-mirror invariants explicitly), `project-quality-gates/SKILL.md` (add local-pass invariant note + "Adding or Renaming a CI Job" subsection), `project-specs-sync/SKILL.md` (cross-reference `scripts/spec-drift.sh` as automated check).

Existing patterns to reuse:

- `actions/cache` patterns for Go module cache and npm cache from official GitHub Actions docs.
- `golangci/golangci-lint-action` for golangci-lint with its built-in cache.
- `github/codeql-action/init` + `analyze` standard pattern from CodeQL docs.
- Dependabot grouping config from official Dependabot docs.
- Branch protection patching pattern from the `gh` CLI docs.
- The pattern of "operator-readable yaml file documenting CI state" is a defense-in-depth idea borrowed from larger Netdata repos where similar drift bit the team.

Spec deltas to land before any test or code:

- `.agents/sow/specs/quality-gates.md`: small additions under "Aggregate Scripts" and a new "CI Workflow Mirror Invariant" subsection stating that CI runs the same `scripts/*.sh` the operator runs locally; under "Adding or Removing Gates", add a "Renaming a CI Job" bullet pointing to the workflow-checks file requirement.
- `.agents/skills/project-quality-gates/SKILL.md`: add a "Local Pass + CI Fail Invariant" section; add a "Renaming a CI Job" subsection under "Adding a New Gate".
- `.agents/skills/project-specs-sync/SKILL.md`: under "Spec Drift Detection", replace "Future work (Phase 2+): a `scripts/spec-drift.sh` …" with the actual script reference and the indicators it covers.
- No new spec files expected.

Risk and blast radius:

- Local-only impact for the scripts themselves. CI workflow changes affect every PR going forward — broken `ci.yml` blocks all PRs. Mitigation: workflow lands in a feature branch, tested via `act` (local GitHub Actions runner) or a draft PR before merging; first push after merge is monitored.
- Branch protection changes are reversible via `gh api -X PATCH` but during the window where protection is misconfigured, PRs may merge without gates — change is staged: workflow first (verified green on master), then protection registration second.
- Dependabot config typically opens PRs within minutes of merge; first wave may surface latent dependency issues. Mitigation: SOW close includes a sweep of the first Dependabot PRs to confirm they're addressable, not failing outright.

Sensitive data handling plan:

- Scripts written here scan for secrets; they are the safety net, not the source of secrets. Test inputs during implementation (planted secrets to verify patterns) live in `/tmp/` and are never committed.
- CodeQL results may surface real findings that include code snippets; the workflow's `upload-sarif` step is the standard pattern and stores results in GitHub's security tab (not in committed files).
- Branch protection setup invocations include the repo name but no secrets; documenting the `gh api` call in the SOW is safe.

Implementation plan (ordered chunks):

1. **Land spec deltas first** (per `project-specs-sync` ordering): update `quality-gates.md`, `project-quality-gates/SKILL.md`, `project-specs-sync/SKILL.md` to describe target state before any script exists.
2. **Write `scripts/scan-secrets.sh`**: bash script implementing the four pattern classes (keyword-anchored entropy, OpenAI, Slack, AWS) with documented allow-list. Plant test secrets in a scratch file to verify each pattern catches its class; remove before commit.
3. **Write `scripts/spec-drift.sh`**: bash + small awk/jq helpers implementing the five drift indicators. Start with grep-based extraction; escalate to `go/ast` parsing if grep proves insufficient (decision documented inline). Plant synthetic mismatches to verify each indicator; remove before commit.
4. **Write `scripts/gates.sh`**: orchestrator that runs every gate in order, fail-fast, with section headers, per-section timing, and a final summary. Slow gates run last so fast feedback arrives early. Profile total runtime against the < 5 min target.
5. **Land `.github/workflows/ci.yml`**: job split `lint` / `test` / `frontend` / `gates`, parallel where independent, caching configured per ecosystem. Each job invokes the corresponding `scripts/*.sh`. Test via `act` or draft PR before merging to master.
6. **Land `.github/dependabot.yml`**: three ecosystems, weekly cadence, minor/patch grouping per ecosystem.
7. **Land `.github/workflows/codeql.yml` + `.github/codeql/config.yml` (if suppression needed)**: Go + JavaScript/TypeScript, per-push + weekly schedule.
8. **Land `.github/workflows-checks.yaml`**: operator-readable file documenting current required check names. Lists the four job names from `ci.yml` plus any CodeQL job that should also be required.
9. **One-time branch protection setup**: after `ci.yml` is green on master, run `gh api -X PATCH /repos/netdata/ai-viewer/branches/master/protection` to register required checks. Verify via `gh api /repos/netdata/ai-viewer/branches/master/protection`. Capture the invocation in `## Validation` and in `docs/setup.md`.
10. **Synthetic drift test**: plant a temporary mismatch (e.g. an endpoint in `internal/presenter/` not in `specs/rest-api.md`); verify `spec-drift.sh` catches it; remove the planted mismatch before committing the SOW close.
11. **Measure CI total wall-clock** on a representative PR and capture in `## Validation`.
12. **External review round**: at least three reviewers (per `project-second-opinions/SKILL.md`), prompt = "review SOW-0013 changes for: gate completeness, CI workflow correctness, branch protection coverage, spec drift indicator coverage, secret scanner false-positive risk, unwanted side effects". Iterate until convergence.
13. **Mark SOW completed and move to `done/`** in the same commit as the final implementation.

Validation plan:

- Each new script exits 0 on the delivery commit (evidence: command + output in `## Validation`).
- `scripts/gates.sh` total runtime captured locally; < 5 min on workstation (evidence: timed run).
- CI workflow runs successfully on the delivery PR; total wall-clock captured; < 8 min (evidence: GitHub Actions URL).
- Branch protection shows the registered required-check names (evidence: `gh api` output).
- Dependabot config validates (evidence: GitHub Actions surface shows "Dependabot is active").
- CodeQL workflow runs successfully on first push; zero high-severity findings (evidence: GitHub security tab snapshot or workflow URL).
- Synthetic drift test confirms spec-drift.sh catches each indicator class (evidence: test fragment cited in `## Implementation`).
- Reviewer findings addressed; reviewers re-run with the same scope plus fix notes until no actionable findings remain.

Artifact impact plan:

- `AGENTS.md`: no expected change. The Quality Gates table is a summary; the authoritative catalog is `quality-gates.md`.
- `.agents/sow/specs/quality-gates.md`: updated with "CI Workflow Mirror Invariant" subsection and "Renaming a CI Job" bullet under "Adding or Removing Gates".
- `.agents/skills/project-quality-gates/SKILL.md`: updated with "Local Pass + CI Fail Invariant" section and "Renaming a CI Job" subsection.
- `.agents/skills/project-specs-sync/SKILL.md`: drift-detection section updated to reference the now-existing `scripts/spec-drift.sh`.
- `README.md`: add CI badge for the new `ci.yml` workflow; add CodeQL badge.
- New: `scripts/scan-secrets.sh`, `scripts/spec-drift.sh`, `scripts/gates.sh`, `.github/workflows/ci.yml`, `.github/workflows/codeql.yml`, `.github/codeql/config.yml` (if needed), `.github/dependabot.yml`, `.github/workflows-checks.yaml`, `docs/setup.md` (if not already present, captures one-time branch protection invocation).

Open-source reference evidence:

- GitHub Actions caching: `actions/cache @ HEAD` — `README.md` (Examples section).
- golangci-lint Action: `golangci/golangci-lint-action @ HEAD` — `README.md` (Caching section).
- CodeQL Action: `github/codeql-action @ HEAD` — `README.md` (Configuration section).
- Dependabot config schema: `github/dependabot-core @ HEAD` — `docs/dependabot.md` (schema reference).
- Branch protection API: GitHub REST API docs at `docs.github.com/en/rest/branches/branch-protection` (not a mirrored repo; cited as public docs URL).

Open decisions:

- None requiring operator input. The branch protection one-time setup is documented and the operator approves at SOW close.

## Implications And Decisions

No operator decisions are required for this SOW. All choices fall within the assistant's technical authority and are documented in `quality-gates.md` and the runtime skills. The one-time branch protection setup happens after this SOW merges and is captured in `docs/setup.md` so the operator can audit it.

## Plan

See `Pre-Implementation Gate / Implementation plan` above. Thirteen chunks, expected to land in 4–6 commits with the final commit moving the SOW to `done/` and the one-time branch protection setup running after merge.

## Execution Log

### 2026-06-04 — Open + current-state reconciliation

Opened under the operator's standing backlog mandate ("proceed"); predecessor SOWs 0009/0010/0011/0012 are all in `done/`, so the Pre-Implementation Gate dependency is satisfied. Reconciled the drafted plan (which assumed a greenfield repo) against the current `master` (`0e3ffc1`):

- **Already landed (no work):** `scripts/scan-secrets.sh` (SOW-0009/0017 — the secret + operator-PII scanner; the AC#1 patterns + allow-list exist and the gate is wired into CI's `gates` job); `.github/workflows/ci.yml` (canonical CI; jobs `lint`, `test`, `frontend`, `embed-smoke`, `gates` — note FIVE jobs, not the 4 the draft assumed: `embed-smoke` was added in SOW-0001 Chunk 17). AC#1 + AC#4 are substantially met; this SOW verifies them and fills the rest.
- **Remaining gaps (this SOW):** `scripts/spec-drift.sh` (AC#2 — 5 indicators), `scripts/gates.sh` (AC#3 — aggregate orchestrator), `.github/workflows/codeql.yml` + `.github/codeql/config.yml` (AC#8), `.github/dependabot.yml` (AC#7), `.github/workflows-checks.yaml` (AC#6), register required status checks on `master` (AC#5 — currently NONE; this also retires the project-wide "no required checks / self-merge-past-non-required-Codacy" posture), and the doc invariants (AC#9/#10). README CI/CodeQL badges.
- **Required-check names to register (AC#5/#6):** the five `ci.yml` job names — `lint`, `test`, `frontend`, `embed-smoke`, `gates` (+ the CodeQL job once `codeql.yml` lands, if it is to be required).
- **Ordering note:** the one-time branch-protection `gh api -X PATCH` (chunk 9) runs AFTER `ci.yml`+`codeql.yml` are green on `master`, as its own post-merge step — registering a not-yet-present check name would block all PRs.

(Subsequent chunk entries appended below as work proceeds.)

## Validation

(Filled at SOW close. Each acceptance criterion gets evidence: command + output summary, CI run URL, branch protection state, reviewer finding summary.)

## Reviews

(Filled as external reviewers run. One sub-section per round.)

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
