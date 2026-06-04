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

- `quality-gates.md` lists `scripts/gates.sh` as the full local workstation aggregate. CI enforces the same gate contract through dedicated parallel jobs plus the cross-cutting `gates` job, not by re-running the full serial aggregate.
- Cross-cutting gates documented in `quality-gates.md`: Secrets Scan (`scripts/scan-secrets.sh`) and Spec Drift (`scripts/spec-drift.sh`). The secrets scanner already exists; this SOW lands spec-drift, the local aggregate, and the CI fail-closed wiring around both.
- `project-specs-sync/SKILL.md` lists drift indicators that `scripts/spec-drift.sh` must lint: REST endpoints registered vs. `specs/rest-api.md`; SSE event types vs. `specs/sse-protocol.md`; SQLite columns in migrations vs. `specs/data-model.md`; canonical event fields vs. `specs/canonical-events.md`; adapter probes in discovery code vs. `specs/adapter-<name>.md`.
- Repo is `netdata/ai-viewer`, public on GitHub from day one (recorded as decision in SOW-0001). GitHub Actions is the CI platform (also recorded in SOW-0001).
- GitHub's branch protection API updates the full protection rule via `PUT /repos/{owner}/{repo}/branches/{branch}/protection`; the nested status-check-only endpoint uses `PATCH /repos/{owner}/{repo}/branches/{branch}/protection/required_status_checks`. The required-checks list is keyed by job name; renaming a job without updating protection silently disables the check.
- Dependabot config (`.github/dependabot.yml`) supports Go modules, npm, and GitHub Actions ecosystems. Weekly cadence is the standard balance between freshness and noise.
- CodeQL via `github/codeql-action` supports Go and JavaScript/TypeScript out of the box; weekly schedule + per-push runs catch newly-disclosed query results.

Inferences:

- The original < 5 min local `gates.sh` target is not met by the complete gate set: `scripts/test.sh` alone exceeds it on the workstation. The correct technical decision is to keep the aggregate complete, record the measured runtime, and track a fast/parallel profile in follow-up SOW-0043 rather than dropping or weakening gates.
- CI total wall-clock remains controlled by splitting expensive gates into parallel jobs (`lint`, `test`, `frontend`, `embed-smoke`, `gates`) plus CodeQL matrix jobs; the longest-running job is the wall-clock floor.
- Required status checks are brittle to job renames. Mitigation: commit a small `.github/workflows-checks.yaml` (operator-readable, not consumed by Actions) that documents the current required check names; any SOW that renames a job is required to update both files and re-run the full-rule `gh api -X PUT` update in the same commit.
- CodeQL on a TypeScript + Go repo produces some false positives. Mitigation: suppressions go in `.github/codeql/config.yml` with linked-issue rationale, never in source comments without a tracking issue.
- "Local pass + CI fail" is almost always one of three causes: environment (different Go version, different Node version, different OS), test isolation (test cache, leftover state), or timing (slower CI hardware). The aggregate-script invariant means CI runs the same commands; remaining divergence is investigatable from the asymmetric input.

Unknowns:

- Exact current required-status-check names will be known only once SOWs 0009–0012 land and CI is wired. Names are committed to `.github/workflows-checks.yaml` on this SOW's delivery, and the full-rule `gh api -X PUT` invocation that registers them happens once after merge (one-time op).
- Whether `gh api` updating of branch protection requires a fine-grained PAT or works with the default `GITHUB_TOKEN`; to be verified at implementation. If the default token lacks the scope, the SOW documents the one-time PAT-based setup the operator runs.
- Whether spec-drift.sh's pattern-matching for "endpoints registered in `internal/presenter/`" needs structural parsing (AST) or simple grep is sufficient; to be decided during implementation based on the actual call site shapes.

### Acceptance Criteria

1. **`scripts/scan-secrets.sh` exists and enforces zero hits.** Implements the secret and operator-PII pattern set from `quality-gates.md`: operator identity derived at runtime from git/user metadata, `sk-…` / `sk-ant-…`, Slack `xox…`, AWS `AKIA…`, bearer tokens, GitHub PATs, and GitLab PATs. It scans every tracked file, including `.gz` payloads and symlink target strings, with only the documented per-token synthetic `EXAMPLE` exemption for secret-shape fixtures. **Verification**: script and self-test exit 0 on the delivery commit; planted synthetic fixtures confirm each rule class and fail-closed path catches its intended class.
2. **`scripts/spec-drift.sh` exists and enforces zero drift.** Implements the five drift indicators from `quality-gates.md` and `project-specs-sync/SKILL.md`: REST verb+path endpoints, SSE event types, SQLite table names and table-scoped `table.column` pairs, canonical event fields, adapter probes. Each indicator has a clear code→spec or spec→code report; one-direction exemptions are explicitly documented in `quality-gates.md`. **Verification**: script exits 0 on the delivery commit; the synthetic self-test confirms each indicator catches the intended drift class.
3. **`scripts/gates.sh` is the canonical local aggregate.** Runs every local gate from `quality-gates.md` in order, fail-fast, with clear section headers and per-section timing. Performance outcome is measured honestly: the complete aggregate currently exceeds the original 5-minute target because the Go `-race` suite is the long pole; no gate is dropped or weakened, and SOW-0043 tracks the fast/parallel profile. **Verification**: timed run captured in `## Validation`; per-section timings logged.
4. **`.github/workflows/ci.yml` is the canonical CI workflow.** Job split: `lint`, `test`, `frontend`, `embed-smoke`, `gates`, running in parallel where independent. Caching configured for Go, npm, and golangci-lint. CI invokes the same underlying scripts/gates where practical and otherwise runs equivalent dedicated steps; the `gates` job is cross-cutting only and fail-closed for required scripts (`scan-secrets`, `spec-drift`, `scan-ai-attribution`, and local `gates.sh` presence/syntax). Required mature repo prerequisites for `lint`, `test`, `frontend`, and `embed-smoke` fail closed instead of producing green skip/no-op jobs. **Verification**: workflow file present; first push runs successfully; per-job timing captured.
5. **Required status checks registered on `master`.** A one-time setup invocation of `gh api -X PUT /repos/netdata/ai-viewer/branches/master/protection` registers the five `ci.yml` job names plus the three explicit CodeQL matrix job names. **Verification**: `gh api /repos/netdata/ai-viewer/branches/master/protection` shows the eight required checks; an intentional failing CI on a test PR blocks merge.
6. **Required-check names documented in `.github/workflows-checks.yaml`.** Operator-readable, not consumed by Actions, lists current required check names. Any future SOW that renames a job is required to update this file and re-run the full-rule `gh api -X PUT` invocation in the same commit. **Verification**: file present; contents match the names registered via `gh api`; this requirement is added to `project-quality-gates/SKILL.md`.
7. **`.github/dependabot.yml` configured for three ecosystems.** Go modules (root `go.mod`), npm (`/frontend`), GitHub Actions (`.github/workflows/`). Weekly cadence. Grouping for minor/patch updates to reduce PR noise. **Verification**: file present; first weekly run produces PRs for any pending updates within 7 days of merge.
8. **`.github/workflows/codeql.yml` configured for Go + TypeScript + GitHub Actions.** Per-push on master + PR + weekly schedule. Suppressions (if any) in `.github/codeql/config.yml` with linked-issue rationale. **Verification**: workflow file present; first run completes with zero high-severity findings; any suppression has a tracking issue.
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
- **R3 — required-status-checks regression on job rename**: if a future SOW renames a CI job (e.g. `lint` → `go-lint`) without updating the branch protection `contexts` array, the rename silently disables the required check — PRs merge without the gate running. Mitigation: `.github/workflows-checks.yaml` documents the current required check names and is a tracked file; `project-quality-gates/SKILL.md` adds an "Adding or Renaming a CI Job" subsection that mandates updating both the workflow and the protection rule in the same commit, with the `gh api -X PUT` invocation captured in the commit body.
- **R4 — Dependabot PR noise**: three ecosystems × weekly = potentially 5–15 PRs/week. Mitigation: grouping minor/patch updates per ecosystem reduces this to ~3 PRs/week; major updates remain individual PRs (they may need a SOW per `AGENTS.md` library version policy).
- **R5 — spec-drift.sh false positives**: REST endpoint discovery via grep on `internal/presenter/` may produce false positives for handler-factory patterns. Mitigation: if grep proves insufficient, the script uses Go's `go/ast` package to parse handler registrations structurally; the decision is made during implementation based on actual call site shapes. False positives are NOT silenced — they indicate a code structure that the script should learn to recognize.
- **R6 — secret-scanner false positives on legitimate token-shaped fixtures**: sanitizer dirty inputs may need secret-shaped tokens, while real secrets must still fail everywhere. Mitigation: generic secret shapes are exempted only when the matched token itself carries the synthetic `EXAMPLE` marker; operator identity is never exempted; placeholders such as `[REDACTED_SECRET]` and provider hostnames are sanitizer concerns, not broad line-level scan exemptions.
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
- One-time external operation: `gh api -X PUT /repos/netdata/ai-viewer/branches/master/protection` to register required status checks with the full branch-protection rule.
- Modified: `scripts/lint.sh`, `scripts/test.sh` (extended to integrate with the new orchestrator if needed).
- Modified docs: `quality-gates.md` (add the aggregate-script and CI-mirror invariants explicitly), `project-quality-gates/SKILL.md` (add local-pass invariant note + "Adding or Renaming a CI Job" subsection), `project-specs-sync/SKILL.md` (cross-reference `scripts/spec-drift.sh` as automated check).

Existing patterns to reuse:

- `actions/cache` patterns for Go module cache and npm cache from official GitHub Actions docs.
- `golangci/golangci-lint-action` for golangci-lint with its built-in cache.
- `github/codeql-action/init` + `analyze` standard pattern from CodeQL docs.
- Dependabot grouping config from official Dependabot docs.
- Branch protection full-rule update pattern from the official GitHub REST docs.
- The pattern of "operator-readable yaml file documenting CI state" is a defense-in-depth idea borrowed from larger Netdata repos where similar drift bit the team.

Spec deltas to land before any test or code:

- `.agents/sow/specs/quality-gates.md`: small additions under "Aggregate Scripts" and a new "CI Workflow Mirror Invariant" subsection stating that CI runs the same `scripts/*.sh` the operator runs locally; under "Adding or Removing Gates", add a "Renaming a CI Job" bullet pointing to the workflow-checks file requirement.
- `.agents/skills/project-quality-gates/SKILL.md`: add a "Local Pass + CI Fail Invariant" section; add a "Renaming a CI Job" subsection under "Adding a New Gate".
- `.agents/skills/project-specs-sync/SKILL.md`: under "Spec Drift Detection", replace "Future work (Phase 2+): a `scripts/spec-drift.sh` …" with the actual script reference and the indicators it covers.
- No new spec files expected.

Risk and blast radius:

- Local-only impact for the scripts themselves. CI workflow changes affect every PR going forward — broken `ci.yml` blocks all PRs. Mitigation: workflow lands in a feature branch, tested via `act` (local GitHub Actions runner) or a draft PR before merging; first push after merge is monitored.
- Branch protection changes are reversible via `gh api -X PUT` with the full rule payload, but during the window where protection is misconfigured, PRs may merge without gates — change is staged: workflow first (verified green on master), then protection registration second.
- Dependabot config typically opens PRs within minutes of merge; first wave may surface latent dependency issues. Mitigation: SOW close includes a sweep of the first Dependabot PRs to confirm they're addressable, not failing outright.

Sensitive data handling plan:

- Scripts written here scan for secrets; they are the safety net, not the source of secrets. Test inputs during implementation (planted secrets to verify patterns) live in `/tmp/` and are never committed.
- CodeQL results may surface real findings that include code snippets; the workflow's `upload-sarif` step is the standard pattern and stores results in GitHub's security tab (not in committed files).
- Branch protection setup invocations include the repo name but no secrets; documenting the `gh api` call in the SOW is safe.

Implementation plan (ordered chunks):

1. **Land spec deltas first** (per `project-specs-sync` ordering): update `quality-gates.md`, `project-quality-gates/SKILL.md`, `project-specs-sync/SKILL.md` to describe target state before any script exists.
2. **Write `scripts/scan-secrets.sh`**: bash script implementing operator-identity detection plus explicit token-shape rules (OpenAI/Anthropic, Slack, AWS, bearer, GitHub PATs, GitLab PATs), with per-token `EXAMPLE` fixture exemption and a fail-closed self-test.
3. **Write `scripts/spec-drift.sh`**: bash + awk helpers implementing the five drift indicators. Start with grep/awk extraction for the line-oriented surfaces; escalate to `go/ast` parsing only if the actual code shape proves grep/awk insufficient. Plant synthetic mismatches in a hermetic self-test to verify each indicator; remove all planted mismatches before commit.
4. **Write `scripts/gates.sh`**: orchestrator that runs every gate in order, fail-fast, with section headers, per-section timing, and a final summary. Slow gates run last so fast feedback arrives early. Profile total runtime and record the measured long pole; a fast/parallel profile is follow-up work if the complete gate exceeds the original target.
5. **Land `.github/workflows/ci.yml`**: job split `lint` / `test` / `frontend` / `gates`, parallel where independent, caching configured per ecosystem. Each job invokes the corresponding `scripts/*.sh`. Test via `act` or draft PR before merging to master.
6. **Land `.github/dependabot.yml`**: three ecosystems, weekly cadence, minor/patch grouping per ecosystem.
7. **Land `.github/workflows/codeql.yml` + `.github/codeql/config.yml` (if suppression needed)**: Go + JavaScript/TypeScript, per-push + weekly schedule.
8. **Land `.github/workflows-checks.yaml`**: operator-readable file documenting current required check names. Lists the five job names from `ci.yml` plus the CodeQL matrix job names that should also be required.
9. **One-time branch protection setup**: after `ci.yml` is green on master, run `gh api -X PUT /repos/netdata/ai-viewer/branches/master/protection` to register required checks via the full protection rule. Verify via `gh api /repos/netdata/ai-viewer/branches/master/protection`. Capture the invocation in `## Validation` and in `docs/setup.md`.
10. **Synthetic drift test**: plant a temporary mismatch (e.g. an endpoint in `internal/presenter/` not in `specs/rest-api.md`); verify `spec-drift.sh` catches it; remove the planted mismatch before committing the SOW close.
11. **Measure CI total wall-clock** on a representative PR and capture in `## Validation`.
12. **External review round**: at least three reviewers (per `project-second-opinions/SKILL.md`), prompt = "review SOW-0013 changes for: gate completeness, CI workflow correctness, branch protection coverage, spec drift indicator coverage, secret scanner false-positive risk, unwanted side effects". Iterate until convergence.
13. **Mark SOW completed and move to `done/`** in the same commit as the final implementation.

Validation plan:

- Each new script exits 0 on the delivery commit (evidence: command + output in `## Validation`).
- `scripts/gates.sh` total runtime captured locally with per-section timings. If the complete aggregate exceeds the original 5-minute target, the SOW records the measured long pole and the follow-up SOW that owns fast/parallel feedback.
- CI workflow runs successfully on the delivery PR; total wall-clock captured (evidence: GitHub Actions URL).
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
- **Ordering note:** the one-time branch-protection `gh api -X PUT` full-rule update (chunk 9) runs AFTER `ci.yml`+`codeql.yml` are green on `master`, as its own post-merge step — registering a not-yet-present check name would block all PRs.

(Subsequent chunk entries appended below as work proceeds.)

### 2026-06-04 — Gap implementation (chunks 1–8 + README/docs)

Implemented the remaining gaps. Spec→test→code ordering honored: spec deltas landed first, then the detector self-test fixtures + the detector, then the aggregate.

**Chunk 1 — spec deltas (AC#9/#10).**
- `quality-gates.md`: rewrote §Spec Drift to describe `scripts/spec-drift.sh` as LANDED with the 5 indicators + their ACTUAL code/spec locations (the drafted refs `internal/presenter/sse.go` and `internal/ingest/discover.go` were STALE — neither exists; SSE kinds live in `internal/presenter/events_sse.go`, adapter probes in `cmd/ai-viewer-ingest/sources.go`). Added the REST Phase-2/"not registered" exemption, the SQLite column code→spec direction rationale, and the self-test note. Updated §Aggregate Scripts (gates.sh + spec-drift.sh LANDED, gates.sh composition + slow-last ordering). Added §"CI Workflow Mirror Invariant" + a "Renaming a CI Job" subsection. Revised §Performance Target to the MEASURED reality (test.sh long pole > 5 min; gate kept complete; follow-up SOW tracks --fast/parallelize).
- `project-quality-gates/SKILL.md`: rewrote §Spec Drift (real locations + the stale-path note), flipped the Aggregate-Scripts current-state to "all exist", added "Local Pass + CI Fail Invariant" + "Renaming a CI Job" sections, fixed the "once that aggregator lands" stale note, updated the Performance Note to measured reality.
- `project-specs-sync/SKILL.md`: replaced the "Future work … `scripts/spec-drift.sh`" note with the real reference + an indicator table; kept the manual-audit fallback for prose.

**Chunk 3 — `scripts/spec-drift.sh` (AC#2)** + `scripts/test/spec-drift-test.sh`. grep/awk for all 5 indicators (CTO decision: the surfaces are line-oriented + regular — mux.HandleFunc literals joined to handler method guards, `case "<kind>"` strings, `EventKind = "<value>"` consts, SQL CREATE/ALTER, `format: "<name>"` structs — so no `go/ast`; documented inline). Bidirectional per indicator except the two documented one-direction exemptions (REST Phase-2 spec→code; data-model column spec→code prose). Fail-closed (exit non-zero, names indicator + token). Self-test plants every unidirectional indicator and both sides of every bidirectional indicator in a throwaway repo copy + asserts the clean copy passes + proves the REST Phase-2 exemption is both conditional (unmarked→drift) and effective (marked→exempt). After review and parser/extractor-hardening fixes, REST drift is checked as **verb+path**, not path-only, method extraction failures fail closed, data-model column drift is table-scoped via `table.column` pairs, optional no-match extractors preserve indicator-specific diagnostics, and extractor-empty drift cases are pinned: **25/25 cases pass**.
  - **Live-repo result: exit 0 — ZERO real drift across all 5 indicators.** Validated against the real tree: migration table names and table-scoped SQL column pairs are documented in data-model.md; canonical kinds byte-identical; every registered route+verb pair is documented (2 spec-only endpoints both carry the Phase-2/"not registered" marker); SSE kinds match (resync = §Reconnect control frame); all 5 discovery formats have an `adapter-<name>.md` naming the probe path.

**Chunk 4 — `scripts/gates.sh` (AC#3).** Composes the existing scripts, fail-fast, section headers + per-section wall-clock + a final timed summary, slow gates LAST (lint.sh → scan-secrets+self-test → scan-ai-attribution → spec-drift+self-test → systemd unit lint when present → build.sh → test.sh+check-coverage.sh → adapter fuzz seed corpus → frontend E2E/axe → benchmark self-test + local regression gate).
  - **CTO decision on the < 5 min target (AC#3 / R1):** MEASURED `scripts/test.sh` alone = **6m38s** (Go `-race` long pole: `aiagent_v2` ≈123 s, `internal/canonical` ≈62 s), so the full `gates.sh` ≈ 7–8 min — ABOVE the 5-min target. Per the SOW mandate, NO gate was dropped or weakened; the measured total + long pole are documented in the SOW validation record and Performance-Target spec, and a `--fast`/parallelize follow-up is filed as **SOW-0043** (`.agents/sow/pending/`). The full measured `gates.sh` total is in the Validation section.

**Chunk 6 — `.github/dependabot.yml` (AC#7).** gomod (`/`), npm (`/frontend`), github-actions (`/`); weekly Monday 06:00 UTC; minor+patch grouped per ecosystem; major bumps individual (AGENTS.md library-version policy).

**Chunk 7 — `.github/workflows/codeql.yml` (AC#8).** Languages `go` (autobuild), `javascript-typescript` (none), `actions` (none); per-push + PR on master + weekly Monday 07:00 UTC; `github/codeql-action/{init,analyze}@v4`; `security-events: write`; conditional-skip mirrors ci.yml; `@v6` action pins match ci.yml's style. NO `config.yml` — no suppression needed today (the no-inline-suppression-without-issue rule is documented in the header). actionlint clean.

**Chunk 8 — `.github/workflows-checks.yaml` (AC#6).** Operator-readable (NOT consumed by Actions). Records the 5 ci.yml job names (`lint`, `test`, `frontend`, `embed-smoke`, `gates`) + the CodeQL analyze job as the required-check contract; states the same-commit rename rule (workflow + this file + the `gh api` PATCH) and points at docs/setup.md. CTO call: CodeQL IS to be required (static security analysis must pass before merge).

**README + `docs/setup.md`.** Added `ci.yml` + `codeql.yml` status badges to README. Created `docs/setup.md` documenting the one-time `gh api -X PUT …/branches/master/protection` full-rule update that registers the required checks (orchestrator runs it post-merge; AGENTS.md branch-protection shape, token-scope note, verify step, rename procedure).

Verification (chunks above): `actionlint` clean on ci.yml + codeql.yml; YAML sanity OK on dependabot.yml + codeql.yml + workflows-checks.yaml + ci.yml; `shellcheck -x` clean on the new scripts; scan-secrets exit 0; spec-drift exit 0 + self-test expanded from 13 cases to 25 cases through review, parser hardening, and extractor fail-closed fixes. Commit, branch-protection setup, CI measurement, final review convergence, and SOW close remain orchestrator-owned steps.

### 2026-06-04 — Orchestrator review fix pass

Reviewed the staged SOW-0013 implementation against the gate catalog and found three defects before commit:

- `scripts/gates.sh` claimed to run every gate but omitted explicit adapter fuzz seed target lock, Playwright E2E/axe, systemd unit lint, and the local benchmark regression gate.
- `.github/workflows/ci.yml` ran the full serial `scripts/gates.sh` inside the `gates` job, duplicating the dedicated parallel jobs and breaking the CI wall-clock model.
- Durable docs still contained stale "SOW-0013 planned" text for `gates.sh` / `spec-drift.sh`.

CTO decisions and fixes:

- `scripts/gates.sh` is now the **full local workstation aggregate**: lint; secrets self-test + scan; AI-attribution scan; spec-drift self-test + live detector; systemd unit lint when present; build; test; coverage; deterministic adapter fuzz seed corpus + exact target-set lock; frontend Playwright E2E (which includes axe specs); benchmark gate self-test + `scripts/check-bench.sh`.
- CI keeps expensive gates parallel. The CI `gates` job is now cross-cutting only: secrets + scanner self-test (fail-closed), spec-drift + detector self-test (fail-closed, self-test first), local `scripts/gates.sh` presence + syntax check, AI-attribution scan when present, and systemd unit lint when present. It no longer runs full `scripts/gates.sh`.
- CodeQL matrix jobs now have explicit required-check names (`CodeQL (go)`, `CodeQL (javascript-typescript)`, `CodeQL (actions)`) instead of one ambiguous matrix context. `.github/workflows-checks.yaml` and `docs/setup.md` record all three.
- `AGENTS.md`, `quality-gates.md`, `specs/index.md`, `project-quality-gates`, and `project-testing` were swept for stale SOW-0013 planned text and updated to the landed-state wording.
- Filed follow-up **SOW-0044** (`.agents/sow/pending/SOW-0044-20260604-code-scanning-defense-layer.md`) for the requested post-SOW-0013 CodeQL + Codacy defence layer. It is intentionally separate from this SOW so Codacy configuration, coverage upload, CodeQL policy hardening, and noise tuning get their own evidence and acceptance criteria.
- First full local `scripts/gates.sh` rerun exposed one deterministic-test defect: `cmd/ai-viewer-ingest/main_test.go` isolated `HOME` and `CLAUDE_CONFIG_DIR`, but not the newer `CODEX_HOME`, `OPENCODE_DB`, and `XDG_DATA_HOME` discovery inputs. Fixed the auto-discovery tests to reuse the existing helper that clears every discovery override before asserting implicit source counts.
- First external review round found stale SOW acceptance text, optional CI spec-drift wiring, reversed CI spec-drift self-test order, stale `workflows-checks.yaml` wording, and incomplete self-test direction coverage. Fixed by making spec-drift + local aggregate required in CI, running spec-drift self-test before live drift detection, updating required-check wording, removing dead REST normalization in the detector, expanding the self-test to 13 cases, and amending this SOW to the measured complete-gate reality.
- Second external review round found a real REST-method blind spot in `spec-drift.sh` (path-only comparison), fail-open fixture copying in `scripts/test/spec-drift-test.sh`, fragile deferred-marker/heading matching, stale branch-protection skill wording, incomplete codex discovery test isolation, a local Playwright setup prerequisite not documented, and non-blocking maintainability items (CI presence-check self-test, single-source fuzz target list, checkout-depth optimization). Fixed the blocking items in code/docs/specs; filed SOW-0045 for the non-blocking hardening items.
- Third external review round found one real blocking shell issue: multi-command helper functions in `scripts/gates.sh` could hide an early failed subcommand because `section()` invokes commands in an `||` context, suppressing Bash `errexit` inside helper functions. Fixed with explicit `|| return $?` handling inside the helpers. The same round found AI-attribution was documented as a gate but still optional in CI/local wiring; fixed by making it fail-closed in both places.
- Fifth external review round found the data-model drift detector was still checking bare column names globally rather than table-scoped `table.column` pairs. Fixed the detector and self-test; the stricter detector exposed real existing data-model spec drift for `sources.fts5_index_logs` and the `fts_ops` / `fts_logs` FTS5 columns, which was fixed by adding those SQL schema entries to `data-model.md`.
- A later final-gate attempt exposed intermittent line-state fragility in the SQL extractor after the table-scoped fix. `extract_sql_column_pairs()` was hardened to parse semicolon-delimited SQL statements instead of relying on line-local create state, and `scripts/test/spec-drift-test.sh` added `data-model::statement_boundary_create` to pin two `CREATE TABLE` statements on one physical line.
- Post-hardening stress runs exposed the real remaining flake source: membership checks used `printf '%s\n' "$set" | grep -q` under `set -o pipefail`. When `grep -q` found a match early it could close the pipe, make `printf` exit with SIGPIPE, and turn a real match into a false drift report. Fixed by adding a `contains_line` helper that feeds `grep -qxF` through a here-string, eliminating the producer pipe.

## Validation

### 2026-06-04 — Local Workstation Gates

Commands and evidence:

- `go test -race -count=1 ./cmd/ai-viewer-ingest` — PASS after the test-isolation fix.
- `bash scripts/gates.sh` — PASS, every quality gate green.

`scripts/gates.sh` summary from the passing run:

- `lint.sh` (Go + frontend static analysis): PASS, 22 s.
- `scan-secrets self-test`: PASS, 1 s.
- `scan-secrets`: PASS, 12 s, no secrets or operator-PII in 817 tracked files.
- `scan-ai-attribution`: PASS, 0 s.
- `spec-drift self-test`: PASS, 3 s, 13/13 synthetic cases at the time of the full aggregate run (expanded to the current 25/25 by later review, parser-hardening, and extractor-empty fixes).
- `spec-drift`: PASS, 0 s, no drift across REST, SSE, data-model, canonical, adapter-probes.
- `systemd units`: PASS, 0 s.
- `build.sh`: PASS, 6 s.
- `test.sh`: PASS, 352 s; Go `-race` clean and frontend Vitest clean.
- `check-coverage.sh`: PASS, 0 s; gated `internal/*` aggregate 90.5% statements, every gated package >= 80%.
- `adapter fuzz seed corpus`: PASS, 1 s; target-set lock matched the expected adapter fuzz matrix.
- `frontend E2E + axe`: PASS, 5 s; 51 Playwright chromium tests, zero serious/critical axe violations.
- `benchmark regression gate`: PASS, 57 s; benchmark self-test 8/8 and no significant `sec/op` regression > 20%.

Total local aggregate runtime: 459 s. This exceeds the original 5-minute target; no gate was weakened. Follow-up SOW-0043 owns the fast/parallel local profile.

### 2026-06-04 — Post-review focused validation

Commands and evidence after review-round-2 fixes:

- `bash -n scripts/gates.sh scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — PASS.
- `shellcheck -x scripts/gates.sh scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — PASS.
- `bash scripts/test/spec-drift-test.sh && bash scripts/spec-drift.sh` — PASS; 25/25 self-test cases; live tree reports no drift across REST, SSE, data-model, canonical, adapter-probes.
- `go test -race -count=1 ./cmd/ai-viewer-ingest` — PASS; codex auto-discovery tests now clear all competing adapter env overrides.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml .github/workflows/fuzz-nightly.yml .github/workflows/race-stress-nightly.yml .github/workflows/govulncheck-nightly.yml` — PASS.
- YAML parse for `.github/dependabot.yml`, `.github/workflows-checks.yaml`, `.github/workflows/codeql.yml`, `.github/workflows/ci.yml` — PASS.
- `bash scripts/scan-secrets.sh` — PASS; no secrets or operator-PII in 817 tracked files.
- `bash scripts/scan-ai-attribution.sh` — PASS.
- `git diff --check` — PASS.
- Targeted temp-copy probe of `scripts/gates.sh` helper failure handling — PASS;
  a forced failure inside `run_fuzz_seed_gate` exited non-zero and reported
  `adapter fuzz seed corpus FAILED`, proving helper subcommand failures are no
  longer masked by the `section()` wrapper.
- After SQL parser hardening: `bash -n` and `shellcheck -x` passed for
  `scripts/gates.sh`, `scripts/spec-drift.sh`,
  `scripts/test/spec-drift-test.sh`, and `scripts/scan-ai-attribution.sh`.
- After SQL parser hardening: `bash scripts/test/spec-drift-test.sh && bash
  scripts/spec-drift.sh` passed with the then-current 17/17 self-test cases and
  no live drift.
- After CI prerequisite and extractor hardening: `bash -n` passed for the shell
  scripts; `shellcheck -x` passed for `scripts/gates.sh`,
  `scripts/spec-drift.sh`, `scripts/test/spec-drift-test.sh`, and
  `scripts/scan-ai-attribution.sh`; `actionlint` passed for `ci.yml` and
  `codeql.yml`; YAML parse passed for Dependabot/workflow/check files; `git
  diff --check` passed.
- After extractor hardening: `bash scripts/test/spec-drift-test.sh` passed with
  25/25 cases, including REST no-registration, SSE/canonical/adapter
  extractor-empty, data-model no-CREATE-TABLE, and table-scoped SQL
  fail-closed diagnostics; `bash scripts/spec-drift.sh` passed with no live
  drift.
- Stability check after extractor hardening: `for i in $(seq 1 20); do bash
  scripts/spec-drift.sh; done` passed 20/20, and `for i in $(seq 1 5); do bash
  scripts/test/spec-drift-test.sh; done` passed 5/5.
- `bash scripts/scan-ai-attribution.sh && bash scripts/scan-secrets.sh` passed
  after the CI/extractor fixes; secret scan reported no secrets or operator-PII
  in 817 tracked files.
- `go test -race -count=1 ./cmd/ai-viewer-ingest` passed after the
  CI/extractor fixes.
- Stability check: `for i in $(seq 1 10); do bash scripts/spec-drift.sh; done`
  and `for i in $(seq 1 10); do bash scripts/test/spec-drift-test.sh; done`
  both passed.
- After fixing the `grep -q` / `pipefail` SIGPIPE false-positive path:
  `for i in $(seq 1 50); do bash scripts/spec-drift.sh; done` passed, and
  `for i in $(seq 1 10); do bash scripts/test/spec-drift-test.sh; done`
  passed.
- Broader focused validation after parser hardening passed: `actionlint` on all
  workflows, YAML parse for Dependabot/workflows/checks files, `bash
  scripts/scan-ai-attribution.sh && bash scripts/scan-secrets.sh`,
  `go test -race -count=1 ./cmd/ai-viewer-ingest`, and `git diff --check`.
- Full `bash scripts/gates.sh` after parser hardening initially passed every
  section through frontend E2E/axe, then failed only at the final local
  benchmark regression gate. Evidence showed unrelated workstation load, not a
  SOW-specific regression: `uptime` reported load average 62.94 and `ps` showed
  concurrent `github.com/netdata/plugin-ipc` fuzz workers plus other CPU-heavy
  processes. A standalone `scripts/check-bench.sh` rerun under that same load
  failed all five benchmark families, confirming the result was
  environment-load contamination.
- Final full `bash scripts/gates.sh` after review convergence: PASS, total
  505 s. Per-section summary: lint 25 s; scan-secrets self-test 1 s;
  scan-secrets 13 s; scan-ai-attribution 0 s; spec-drift self-test 27 s
  (25/25 cases); spec-drift 1 s; systemd units 0 s; build 6 s; test.sh 369 s
  (Go race-clean + frontend Vitest 623 tests); check-coverage 0 s (gated
  internal aggregate 90.5% statements, every gated package >= 80%); adapter fuzz
  seed corpus 1 s; frontend E2E + axe 6 s (51 Playwright Chromium tests);
  benchmark regression gate 56 s (bench self-test 8/8; no significant sec/op
  regression > 20%). The complete gate remains above the original 5-minute
  target; no gate was weakened, and SOW-0043 owns fast/parallel feedback.

Pending validation before closing:

- CI run URL after push.
- Branch protection state after post-merge required-check registration.
- External reviewer convergence. **Done:** round 17 found no blocking code,
  security, spec-sync, artifact, or local/CI parity defects. Remaining notes are
  close-out bookkeeping items below.
- Final full `bash scripts/gates.sh` run after the latest fixes. **Done:** pass
  at 508 s.
- Outcome and lessons sections remain pending until CI passes and branch
  protection is registered.

## Reviews

### 2026-06-04 — External review round 1

Reviewers run in parallel with a read-only prompt covering the whole SOW-0013
implementation and the SOW file:

- `codex`: actionable findings — stale/unmet SOW acceptance criteria, CI
  spec-drift optional-skip hole, stale `workflows-checks.yaml` `gates.sh`
  wording, incomplete spec-drift self-test direction coverage. Also noted the
  staged index was older than the worktree; this is handled by explicit staging
  after final fixes.
- `glm`: actionable findings — CI ran spec-drift before its self-test, stale
  `workflows-checks.yaml` wording, minor dead REST normalization in the
  spec-drift detector.
- `gemini`: exited without useful output; not counted.
- `qwen`: exited without a useful final report; not counted.

Resolution:

- CI `gates` job now fail-closes when `scripts/spec-drift.sh`,
  `scripts/test/spec-drift-test.sh`, or `scripts/gates.sh` is absent.
- CI now syntax-checks `scripts/gates.sh` without running the full serial
  aggregate.
- CI now runs spec-drift self-test before live spec-drift detection.
- `.github/workflows-checks.yaml`, `quality-gates.md`, and
  `project-quality-gates` now describe the actual cross-cutting `gates` job.
- `scripts/spec-drift.sh` no longer carries the dead REST `:ref` normalization
  on code routes.
- `scripts/test/spec-drift-test.sh` expanded from 8 to 13 cases to prove every
  unidirectional indicator and both sides of each bidirectional indicator.
- SOW acceptance criteria amended to the measured complete-gate reality: the
  full local aggregate is complete and currently above 5 minutes; SOW-0043 owns
  fast/parallel feedback.

Follow-up review round required after these fixes; do not close before
reviewers converge.

### 2026-06-04 — External review round 2

Reviewers run in parallel with the same broad, read-only scope as round 1, plus
the short note of fixes applied:

- `codex`: actionable findings — stale staged index risk, stale required-check
  skill/spec wording, REST method drift not detected, incomplete codex discovery
  test isolation outside `main_test.go`, and missing local Playwright setup
  prerequisite.
- `glm`: actionable findings — latent `:ref` normalization mismatch, stale
  required-check wording, timing mismatch between `gates.sh` header and this SOW,
  and non-blocking checkout-depth optimization. It also noted `strict: true` in
  branch protection; this is intentional and retained.
- `deepseek`: actionable findings — fail-open fixture copies in
  `spec-drift-test.sh`, fragile inline deferred-marker regex, exact heading match
  with no trailing-whitespace tolerance, missing self-test for CI presence checks,
  comment indentation, and duplicated fuzz target lists.
- `qwen`: exited without a useful final report; not counted.

Resolution:

- `scripts/spec-drift.sh` now compares REST **verb+normalized path** pairs. It
  joins `mux.HandleFunc("/api/…", p.<handler>)` registrations to handler
  `r.Method` gates across non-test presenter files, treats `HEAD` as implicit
  `GET` parity, normalizes `{id}` and single-value `:ref` to `:id`, keeps catalog
  group normalization separate, and records method-extraction failures from the
  main shell context so they cannot disappear inside command substitution.
- `scripts/test/spec-drift-test.sh` now fail-closes fixture creation when
  presenter sources, migration SQL, or adapter specs are missing. It expanded to
  16 cases at that time, including REST method mismatch, missing method-gate extraction, and
  a table-scoped data-model column false-negative regression.
- The REST deferred-marker vocabulary is a named detector variable, and REST
  section matching tolerates trailing whitespace on headings.
- `cmd/ai-viewer-ingest/discovery_test.go` now clears every discovery env
  override before codex auto-discovery tests that call `resolveSources(nil, ...)`.
- `project-quality-gates`, `project-coding`, `project-specs-sync`,
  `project-workflow`, and `workflow.md` no longer describe SOW-0013 as future
  work or branch protection as permanently having `required_status_checks: null`.
- `docs/setup.md` records the one-time local Playwright browser prerequisite for
  `scripts/gates.sh`.
- `scripts/gates.sh` points to SOW validation for per-run timing instead of
  carrying a conflicting hard-coded total.
- `.github/workflows/ci.yml` comment indentation fixed.
- Filed **SOW-0045** for the accepted non-blocking hardening work: CI
  presence-check self-test, single-source fuzz target list, and checkout-depth
  optimization.

Follow-up review round required after these fixes; do not close before
reviewers converge.

### 2026-06-04 — External review round 3

Reviewers run in parallel with the same broad, read-only scope as rounds 1 and 2,
plus the short note of review-round-2 fixes applied:

- `codex`: actionable findings — `scripts/gates.sh` helper functions could hide
  failed subcommands because `section()` invokes the helper in an `||` context,
  and AI-attribution gate status was inconsistent (documented as a gate but
  optional in CI/local wiring). It also reported no actionable CodeQL matrix
  issue and cited official CodeQL docs for `actions` language support.
- `glm`: no blocking findings. Noted low/accepted items: duplicated fuzz target
  list (already SOW-0045), REST method extraction is intentionally
  pattern-specific but fail-closed, bash nameref requires modern Bash (acceptable
  for CI/workstation), and SOW outcome/lessons remain pending until close.
- `deepseek`: reported `actions/cache@v5` as potentially missing, CodeQL Go skip
  as a latent concern if `go.mod` were removed, the stale "four current jobs"
  workflow comment, duplicated fuzz target list, and lowercase SQL identifier
  assumption. `actions/cache@v5` was verified directly against GitHub as
  `refs/tags/v5`, so this is a false positive. CodeQL Go is retained because Go
  is a fixed project tech-stack choice for ai-viewer; a repo split/removal of
  `go.mod` would be a future SOW. Fuzz-list and checkout-depth hardening remain
  tracked in SOW-0045; SQL lowercase convention is current project practice.
- `qwen`: hung without a final report after repeated polls; the exact reviewer
  PIDs started by this round were stopped (`timeout` wrapper + child only) to avoid
  leaving a stale process running. Not counted.

Resolution:

- `scripts/gates.sh` multi-command helpers now explicitly return on subcommand
  failure, so `go test -run='^Fuzz'`, fuzz target-list extraction,
  `npm run e2e`, benchmark self-test, and benchmark gate failures cannot be
  masked by later successful commands.
- `scripts/scan-ai-attribution.sh` is now a required fail-closed gate locally and
  in CI. `.github/workflows/ci.yml`, `quality-gates.md`,
  `project-quality-gates`, and SOW-0045 wording were updated accordingly.
- `.github/workflows/ci.yml` stale "four current jobs" comment was corrected.
- Verified `actions/cache@v5` exists through GitHub API:
  `gh api repos/actions/cache/git/ref/tags/v5 --jq '.ref'` returned
  `refs/tags/v5`.

Follow-up review round required after these fixes; do not close before reviewers
converge.

### 2026-06-04 — External review round 4

Reviewers run in parallel with the same broad, read-only scope as rounds 1-3,
plus the short note of review-round-3 fixes applied:

- `codex`: actionable finding — `scripts/scan-ai-attribution.sh` still masked
  `grep` errors with `|| true`, even though the gate is now mandatory and
  fail-closed. Also noted stale comments in `.github/workflows-checks.yaml` and
  `.github/workflows/ci.yml` that still implied AI-attribution or cross-cutting
  gate files could be optional.
- `glm`: no blocking findings. Noted low/accepted items: duplicated fuzz target
  list and checkout-depth optimization (already SOW-0045), the deliberate
  `require_file ... || return 0` cascade-stop pattern in `spec-drift.sh`, and
  SOW review-entry ordering.
- `deepseek`: no blocking findings. Noted low/accepted items: Dependabot
  GitHub Actions noise, CI presence-check self-test (already SOW-0045), and
  latent formatting assumptions in `spec-drift.sh` that match current project
  conventions.

Resolution:

- `scripts/scan-ai-attribution.sh` now validates all required scan roots before
  scanning, treats `grep` exit 0 as hits, treats exit 1 as clean no-match, and
  fails on any `grep` exit greater than 1.
- `.github/workflows-checks.yaml` now describes AI-attribution as fail-closed;
  only systemd lint is optional by presence.
- `.github/workflows/ci.yml` now separates the old spec-first skip pattern for
  Go/frontend source trees from SOW-0013's mandatory cross-cutting gate files.

Validation after the round-4 fixes:

- `bash -n scripts/scan-ai-attribution.sh scripts/gates.sh scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — passed.
- `shellcheck -x scripts/scan-ai-attribution.sh scripts/gates.sh scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — passed.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml .github/workflows/fuzz-nightly.yml .github/workflows/race-stress-nightly.yml .github/workflows/govulncheck-nightly.yml` — passed.
- `bash scripts/scan-ai-attribution.sh && bash scripts/scan-secrets.sh` — passed; no AI-reviewer attribution and no secrets/operator-PII in 817 tracked files.
- Negative probe with a fake `grep` returning exit 2 made
  `scripts/scan-ai-attribution.sh` fail with `AI-reviewer attribution scan failed with grep exit 2`.
- `bash scripts/test/spec-drift-test.sh && bash scripts/spec-drift.sh` — passed; 16/16 self-test cases at that time and no live drift.
- `go test -race -count=1 ./cmd/ai-viewer-ingest` — passed.
- `git diff --check` — passed.

Follow-up review round required after these fixes; do not close before reviewers
converge.

### 2026-06-04 — External review round 5

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus the short note of review-round-4 fixes applied:

- `codex`: actionable findings — the staged index was stale and
  SOW-0044/SOW-0045 were untracked; `scripts/spec-drift.sh` checked SQLite
  column names globally rather than table-scoped `table.column` pairs; SOW
  acceptance criteria still described the old secrets/AI-attribution contract.
  It also noted a low false-positive edge in the AI-attribution scanner around
  legitimate "per codex" domain wording.
- `glm`: no blocking findings. Noted maintainability items already tracked in
  SOW-0045: duplicated fuzz target list and Bash nameref portability.
- `deepseek`: no blocking findings. Noted Dependabot/CI noise and confirmed the
  current implementation and live focused checks were green.

Resolution:

- Staging is deferred until final validation; the commit will stage exact file
  paths only, including SOW-0044 and SOW-0045.
- `scripts/spec-drift.sh` now extracts and compares migration `table.column`
  pairs against `data-model.md` SQL schema blocks. A global column-name mention
  under another table no longer satisfies the code→spec check.
- `scripts/test/spec-drift-test.sh` now includes
  `data-model::common_column_wrong_table`, which adds `sources.provider` while
  `provider` is documented elsewhere; the detector must report the owning pair.
- The stricter detector exposed real live spec drift: `sources.fts5_index_logs`
  and all `fts_ops` / `fts_logs` FTS5 columns were described in prose but not in
  SQL schema blocks. `data-model.md` now includes the current `sources`
  `fts5_index_logs` column and explicit FTS5 virtual-table DDL blocks.
- SOW acceptance criteria, `quality-gates.md`, and `project-quality-gates` now
  describe the current secrets scanner, mandatory AI-attribution gate, and
  table-scoped data-model drift contract.
- The AI-attribution false-positive edge is accepted as non-blocking and tracked
  in SOW-0045.

Validation after the round-5 fixes:

- `bash -n scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — passed.
- `shellcheck -x scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — passed.
- `bash scripts/test/spec-drift-test.sh && bash scripts/spec-drift.sh` — passed;
  16/16 self-test cases at that time and no live drift.

Follow-up review round required after these fixes; do not close before reviewers
converge.

### 2026-06-04 — External review round 6

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus the short note of review-round-5 fixes applied:

- `codex`: no blocking findings. Noted a low false-positive path where
  `scripts/scan-ai-attribution.sh` walks `cmd/` recursively and could scan
  ignored generated embed output under `cmd/ai-viewer-serve/frontend_dist/` after
  a local build.
- `glm`: no blocking findings. It initially observed one transient
  spec-drift-test failure, reran the test, and confirmed 16/16 cases passed at that time.
  Other low findings were already tracked in SOW-0045 or commit housekeeping.
- `deepseek`: no blocking findings. Confirmed gate completeness, CI
  fail-closed wiring, the then-current 16-case spec-drift suite, secrets/AI-attribution
  scanner behavior, docs/spec/skill sync, CodeQL, and Dependabot.

Resolution:

- The generated-output AI-attribution false-positive risk was added explicitly
  to SOW-0045 alongside the existing source-format wording false-positive
  follow-up. It is non-blocking because the current gate is clean, CI starts from
  a clean checkout, and SOW-0045 owns scanner tuning.
- Commit housekeeping remains pending until final validation; exact-path staging
  will include SOW-0044 and SOW-0045.

Round-6 convergence was superseded by the later SQL parser-boundary hardening,
which added a 17th spec-drift self-test case and required another review round.

### 2026-06-04 — External review round 7

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus the short note of SQL parser hardening, 17-case self-test validation, and
benchmark-load evidence:

- `codex`: actionable findings — SOW ledger still had stale 16/16 references
  while the current self-test has 17 cases; SOW-0044/SOW-0045 were still
  untracked and must be staged explicitly; `AGENTS.md` still described the
  secrets scan as only `testdata/` plus committed source instead of every tracked
  file; final full-gates proof remains required after benchmark load clears.
- `glm`: no blocking implementation findings. Noted the same 16-vs-17 SOW
  inconsistency as low severity, and accepted the benchmark-load failure as a
  local-workstation validation blocker rather than a code regression.
- `deepseek`: no blocking implementation findings. Reconfirmed fail-closed CI
  wiring, SQL statement-boundary self-test coverage, docs/spec/skill sync, and
  follow-up SOW coverage. Low findings remain tracked in SOW-0045.

Resolution:

- SOW-0013 now records the current 17-case spec-drift suite and distinguishes
  older 16-case review history from the later parser-boundary hardening.
- `AGENTS.md` now says the secrets scanner scans every tracked file for secrets
  and operator identity, matching `quality-gates.md` and `scan-secrets.sh`.
- `scripts/spec-drift.sh` now uses a `contains_line` here-string helper for
  every set-membership check, closing the `grep -q` / `pipefail` SIGPIPE false
  positive found during post-review stress runs.
- SOW-0044 and SOW-0045 remain pending artifacts and will be staged by explicit
  path with the rest of the final file set.
- Final full `scripts/gates.sh` proof remains pending until the unrelated
  workstation benchmark load clears.

Round-7 convergence was superseded by later CI prerequisite and extractor
fail-closed hardening.

### 2026-06-04 — External review round 8

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the SOW/AGENTS fixes, `contains_line` helper, 17-case validation,
and benchmark-load evidence:

- `codex`: actionable findings — required CI jobs (`lint`, `test`, `frontend`,
  `embed-smoke`) still had bootstrap-era green skip/no-op paths when mature repo
  prerequisites disappeared; optional no-match extractor pipelines in
  `scripts/spec-drift.sh` could exit under `pipefail` before emitting the
  indicator-specific diagnostic; `scripts/test/spec-drift-test.sh` still used
  the `printf | grep -q` flake pattern; SOW-0044/SOW-0045 remain untracked until
  exact-path staging.
- `glm`: no blocking implementation findings. Confirmed syntax, actionlint,
  YAML parsing, 17-case spec-drift self-test at that time, live spec-drift,
  AI-attribution, and secret scans. Low portability/maintainability notes remain
  non-blocking.
- `deepseek`: no blocking implementation findings. Reconfirmed SOW ledger,
  spec/code paths, shellcheck/actionlint, 17-case spec-drift self-test at that
  time, and follow-up SOW coverage.

Resolution:

- `.github/workflows/ci.yml` now fails closed when required prerequisites are
  missing for `lint`, `test`, `frontend`, and `embed-smoke`; frontend E2E script
  and Go benchmark presence are also required gates rather than optional skips.
- `.agents/sow/specs/quality-gates.md` and
  `.agents/skills/project-quality-gates/SKILL.md` now record that mature
  required CI jobs are not bootstrap probes and must not reintroduce green
  skip/no-op paths.
- `scripts/spec-drift.sh` now uses explicit fail-closed helpers for optional
  extractor pipelines: grep exit 1 becomes an empty set or marker-count `0` so
  the detector emits the indicator-specific drift diagnostic and continues;
  grep/awk/sed errors still fail closed.
- `scripts/test/spec-drift-test.sh` now uses here-string membership checks and
  adds extractor-empty fail-closed cases for REST registration extraction, SSE
  code/spec surfaces, data-model CREATE TABLE extraction, canonical code/spec
  surfaces, and adapter discovery probes. The current self-test suite is
  **25/25 cases**.
- Focused validation after the fixes passed: `bash -n`, `shellcheck -x`,
  `actionlint`, YAML parse, `git diff --check`, live spec-drift, and the 25-case
  spec-drift self-test.

Round 8 was superseded by review round 9, which found two remaining REST
extractor suppressions and one data-model empty-extraction gap.

### 2026-06-04 — External review round 9

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the CI/extractor hardening and 22-case validation:

- `codex`: no confirmed gate-logic bug. Actionable close-out findings:
  SOW-0013 remained `in-progress`, final full `scripts/gates.sh` proof was still
  pending after a benchmark-load failure, the SOW still said 22/22 cases, and
  SOW-0044/SOW-0045 were untracked.
- `glm`: no blocking implementation findings. Low findings: the SOW self-test
  count was stale, duplicated fuzz target list and generated-output
  AI-attribution false-positive risk were already tracked in SOW-0045.
- `deepseek`: actionable findings — two REST extraction paths in
  `scripts/spec-drift.sh` still used `|| true`, the data-model migration-table
  extractor did not report an empty CREATE TABLE set, and the SOW still said
  22/22 cases.

Resolution:

- `scripts/spec-drift.sh` has no remaining `|| true` suppressions.
- REST registration extraction now uses `extract_rest_registrations()`: grep
  no-match becomes an empty registration set and reaches the detector's own
  `no REST verb+path pairs extracted` diagnostic; real grep/sed errors fail.
- REST deferred-section marker counting now uses
  `count_deferred_rest_markers()`: grep no-match returns `0`; awk or real grep
  errors fail.
- Data-model migration table extraction now uses `grep_o_or_empty` and reports
  `no CREATE TABLE statements found in internal/store/migrations/ (extraction
  failed — investigate)` when no migration table anchors are found.
- `scripts/test/spec-drift-test.sh` now includes
  `rest::no_registrations_extracted_fail_closed` and
  `data-model::no_create_tables_fail_closed`; the current self-test suite is
  **25/25 cases**.

Focused validation after the round-9 fixes:

- `rg -n '\|\| true' scripts/spec-drift.sh` — PASS by no matches.
- `git diff --check -- scripts/spec-drift.sh scripts/test/spec-drift-test.sh`
  — PASS.
- `bash -n scripts/spec-drift.sh scripts/test/spec-drift-test.sh` — PASS.
- `shellcheck -x scripts/spec-drift.sh scripts/test/spec-drift-test.sh` —
  PASS.
- `bash scripts/test/spec-drift-test.sh` — PASS; 25 passed, 0 failed.
- `bash scripts/spec-drift.sh` — PASS; no live drift across REST, SSE,
  data-model, canonical, adapter-probes.

Follow-up review round required after these fixes; do not close before reviewers
converge.

### 2026-06-04 — External review round 10

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the round-9 fail-closed hardening and 25-case validation:

- `codex`: no code-level correctness bug. Close-out findings: final full
  `scripts/gates.sh` proof is still required, the current index is unsafe for
  commit until exact-path staging is refreshed, and the SOW review log had round
  3 before round 2.
- `glm`: no blocking findings. Low findings: SOW-0044/SOW-0045 are untracked
  until exact-path staging, duplicated fuzz target list is already tracked in
  SOW-0045, Bash nameref portability is accepted for the project Linux/CI
  target, and marker-count temp-file overhead is negligible.
- `deepseek`: no high/critical findings. Low findings: Dependabot labels are a
  future triage polish item, duplicated fuzz target list is tracked in SOW-0045,
  and first CodeQL run on `master` must verify whether a `.github/codeql/config.yml`
  suppression file is needed.

Resolution:

- Review-log chronology fixed: round 2 now appears before round 3.
- Staging remains intentionally deferred until final validation; final staging
  must use exact file paths and include SOW-0044 and SOW-0045.
- Final full `scripts/gates.sh` proof remains the next close-out gate; do not
  close before it passes on an idle-enough workstation.

Reviewer convergence status: no new actionable code changes requested in round
10. Remaining items are close-out validation/staging/CI/branch-protection steps
already required by this SOW.

### 2026-06-04 — PR CI failure: placeholder commit identity

After the first push of PR #42, the CI `gates` job failed in
`scripts/scan-secrets.sh` while the local full aggregate had passed before the
commit. Evidence:

- GitHub Actions job `79601453311` (`gates`) failed after 1m6s.
- The failing step was the live secrets scan.
- The current delivery commit intentionally used neutral placeholder metadata
  (`user <user@example.invalid>`) to avoid writing personal commit metadata into
  the repository.
- The scanner derived Rule-1 identity from `git log --format='%ae%n%an'` and
  treated that placeholder identity as an operator identity, producing broad
  false positives on normal generic text such as `user` and `/home/user`.

Root cause:

- Rule 1 correctly derives real operator identity from repository history and
  local git metadata, but it did not distinguish real identity from synthetic
  placeholder identities used only as neutral commit metadata.
- This made a privacy-preserving commit identity become a ban-list token.

Spec delta before the fix:

- `.agents/sow/specs/quality-gates.md` now states that exact synthetic
  placeholder identities (`user`, `user@example.invalid`, case-insensitive) are
  ignored only while deriving the Rule-1 ban-list.
- `.agents/skills/project-quality-gates/SKILL.md` mirrors the runtime rule.
- The scanner must still fail closed if no non-placeholder identity remains
  after filtering, and Rule 1 remains never allow-listed for tracked content.

Validation plan for the fix:

- Add scanner self-tests proving a repo with both a real synthetic sentinel
  identity and placeholder commit metadata passes when tracked files contain
  normal `user` and `/home/user` text.
- Add a scanner self-test proving a repo with only placeholder commit metadata
  fails closed because Rule 1 would otherwise be disabled.
- Run focused shell syntax/lint/tests, the live scanner, the full local
  `scripts/gates.sh`, and another external review round before the follow-up
  commit.

Resolution:

- `scripts/scan-secrets.sh` now filters only exact placeholder metadata values
  (`user`, `user@example.invalid`, case-insensitive) before partitioning Rule-1
  derivation inputs into emails and names.
- The filter is derivation-only; tracked file content is still scanned by the
  normal Rule-1/Rule-2 matchers.
- Empty Rule-1 ban-list detection remains fail-closed and now runs after
  placeholder filtering.
- `scripts/test/scan-secrets-test.sh` now has 23 cases, adding:
  - `clean::mixed_case_placeholder_metadata_ignored_with_real_identity`
  - `failclosed::mixed_case_placeholder_only_identity_empty_rule1`

Focused validation after the fix:

- `bash -n scripts/scan-secrets.sh scripts/test/scan-secrets-test.sh` — PASS.
- `shellcheck -x scripts/scan-secrets.sh scripts/test/scan-secrets-test.sh` —
  PASS.
- `bash scripts/test/scan-secrets-test.sh` — PASS; 23 passed, 0 failed.
- `bash scripts/scan-secrets.sh` — PASS; 819 tracked files scanned, 16 `.gz`
  archives decompressed.
- `git diff --check HEAD` — PASS.

Full local aggregate after the fix:

- `bash scripts/gates.sh` — PASS; total 517s.
- Section timings: lint 23s, scan-secrets self-test 1s, live scan 14s,
  AI-attribution 0s, spec-drift self-test 26s, live spec-drift 1s, systemd 0s,
  build 6s, `test.sh` 377s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 6s, benchmark regression gate 62s.
- Go coverage gate: internal aggregate 90.6%; all gated internal packages >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-04 — External review round 11

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus the placeholder-identity CI-fix note and the full local `gates.sh` evidence:

- `glm`: no blocking findings. Confirmed the placeholder fix is narrow,
  derivation-only, fail-closed, and covered by the mixed real+placeholder and
  placeholder-only tests. Low notes: the placeholder filter is intentionally
  hardcoded, checkout-depth optimization remains tracked, and long scripts remain
  follow-up maintenance work.
- `deepseek`: no blocking findings. Confirmed the placeholder fix, 23 scanner
  cases, spec/skill sync, and no Rule-2 or content allow-list side effects. Low
  note: add explicit mixed-case placeholder metadata coverage.
- `codex`: actionable findings:
  1. CI/local mirror invariant was still false because CI `lint` ran
     `go mod tidy`, standalone `gofmt`, standalone `goimports@latest`, and
     `go vet`, while local `scripts/lint.sh` only relied on `golangci-lint` for
     fmt/vet and did not run module tidiness.
  2. Branch protection is not active yet (`required_status_checks: null`), which
     is expected while this SOW is in progress but remains a close-out blocker.
  3. Data-model spec-drift spec→code table-name coverage was narrower than the
     spec advertised: `scripts/spec-drift.sh` checked only `### <table>` schema
     headings, not table names declared inside SQL schema blocks under prose
     headings.
  4. Placeholder identity filtering needs explicit mixed-case metadata coverage.

Resolution plan before the next review round:

- Update `scripts/lint.sh` so the local static-analysis entrypoint mirrors the
  CI `lint` job's module-tidiness, standalone `gofmt`, standalone
  `goimports@latest`, and `go vet` steps before `golangci-lint`.
- Update `scripts/spec-drift.sh` so data-model spec→code table-name checking
  includes table names extracted from SQL schema blocks, not only schema
  headings.
- Add self-tests for the SQL-block-only spec table drift and mixed-case
  placeholder metadata handling.
- Keep branch-protection registration as the required post-merge close-out step.

Resolution after the round-11 fixes:

- `scripts/lint.sh` now mirrors the CI `lint` job's Go preflight before
  `golangci-lint`: `go mod tidy`, `git diff --exit-code go.mod go.sum`,
  standalone `gofmt -l .`, standalone `goimports@latest -l .`, and
  `go vet ./...`.
- `.agents/sow/specs/quality-gates.md` and
  `.agents/skills/project-quality-gates/SKILL.md` now document module
  tidiness and the local/CI formatter/vet parity contract.
- `scripts/spec-drift.sh` now extracts table names from both data-model schema
  headings and fenced SQL `CREATE TABLE` / `CREATE VIRTUAL TABLE` statements,
  so a spec-only SQL table under a prose heading cannot escape the spec→code
  drift check.
- `scripts/test/spec-drift-test.sh` now includes
  `data-model::sql_block_table_under_prose_heading_not_in_migrations`; the
  current self-test suite is **26/26 cases**.
- `scripts/test/scan-secrets-test.sh` now uses mixed-case placeholder metadata
  in the real+placeholder and placeholder-only cases; the current self-test
  suite is **23/23 cases**.
- Branch-protection registration remains the post-merge close-out blocker
  because the required check names must exist on `master` before they can be
  registered safely.

Focused validation after the round-11 fixes:

- `bash -n scripts/lint.sh scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/spec-drift-test.sh scripts/test/scan-secrets-test.sh` — PASS.
- `shellcheck -x scripts/lint.sh scripts/scan-secrets.sh
  scripts/spec-drift.sh scripts/test/spec-drift-test.sh
  scripts/test/scan-secrets-test.sh` — PASS.
- `git diff --check HEAD` — PASS.
- `bash scripts/test/spec-drift-test.sh` — PASS; 26 passed, 0 failed.
- `bash scripts/test/scan-secrets-test.sh` — PASS; 23 passed, 0 failed.
- `bash scripts/spec-drift.sh` — PASS; no drift across REST, SSE,
  data-model, canonical, adapter-probes.
- `bash scripts/scan-secrets.sh` — PASS; 819 tracked files scanned, 16 `.gz`
  archives decompressed.
- `bash scripts/lint.sh` — PASS, including module tidiness, standalone
  `gofmt`, standalone `goimports`, `go vet`, `golangci-lint`, standalone
  `gosec`, `govulncheck`, and frontend static checks.

Full local aggregate after the round-11 fixes:

- `bash scripts/gates.sh` — PASS; total 519s.
- Section timings: lint 31s, scan-secrets self-test 2s, live scan 14s,
  AI-attribution 0s, spec-drift self-test 29s, live spec-drift 1s, systemd 0s,
  build 6s, `test.sh` 370s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 7s, benchmark regression gate 58s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-04 — External review round 12

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the round-11 fixes, the 26-case spec-drift self-test, the
23-case scanner self-test, and the 519s full local aggregate evidence:

- `glm`: no blocking findings. Confirmed the SOW-0013 implementation is
  fail-closed, well-tested, and internally consistent. Close-out notes:
  post-merge branch protection still pending, `Outcome`/`Lessons` still
  pending, and follow-up SOWs must be staged explicitly.
- `deepseek`: no blocking correctness or security findings. Medium
  maintainability notes remain tracked follow-ups: duplicated SQL awk
  infrastructure and fuzz target list triple-bookkeeping. Low close-out notes:
  update a stale `gates.sh` comment, keep SOW close-out pending until CI and
  branch protection are recorded.
- `codex`: actionable findings:
  1. Local/CI standalone formatter parity still allowed local-only false
     positives because `gofmt -l .` and `goimports -l .` walked ignored and
     untracked local files, while CI checks out only tracked files.
  2. Local `scripts/lint.sh` warned on a `golangci-lint` version mismatch
     instead of failing, so local and CI could run different lint engines.
  3. `scripts/scan-secrets.sh` reported raw offending lines and raw secret
     tokens on failure, which would make CI logs a secondary leak surface.
  4. SOW-0037 still described formatter walks as part of the open vendored-Go
     problem even though this slice fixes standalone formatters in SOW-0013.

Resolution after the round-12 fixes:

- `scripts/lint.sh` now derives a NUL-delimited tracked Go file list with
  `git ls-files -z -- '*.go'` and runs standalone `gofmt`/`goimports` only on
  that list. The gate fails closed when the tracked Go file list is empty or
  when a formatter fails for any file.
- The CI `lint` job uses the same tracked Go file list for standalone
  `gofmt`/`goimports`, so ignored/untracked local files such as
  `frontend/node_modules/**` cannot create local-only formatter failures.
- `scripts/lint.sh` now fails when the installed `golangci-lint` version cannot
  be parsed or differs from `.golangci-lint-version`. The local version in this
  run matched the pin (`v2.11.4`).
- `scripts/scan-secrets.sh` now reports path, line, rule, and a redacted
  summary marker only. It never prints the raw matched operator identity, raw
  offending line, or raw secret token.
- `scripts/test/scan-secrets-test.sh` now has
  `redact::diagnostics_hide_raw_values`, proving diagnostics keep
  path/line/rule evidence while not echoing raw synthetic identity or secret
  values.
- `.agents/sow/specs/quality-gates.md` and
  `.agents/skills/project-quality-gates/SKILL.md` document the tracked-file
  formatter contract, hard local `golangci-lint` pin enforcement, and redacted
  scanner diagnostics.
- `.agents/sow/pending/SOW-0037-20260602-exclude-vendored-go-from-gates.md`
  now records that standalone formatters are fixed in SOW-0013 and that the
  remaining work is package-level `./...` gate scoping.

Focused validation after the round-12 fixes:

- `bash -n scripts/lint.sh scripts/scan-secrets.sh
  scripts/test/scan-secrets-test.sh` — PASS.
- `shellcheck -x scripts/lint.sh scripts/scan-secrets.sh
  scripts/test/scan-secrets-test.sh` — PASS.
- `actionlint .github/workflows/ci.yml` — PASS.
- `git diff --check HEAD` — PASS.
- `bash scripts/test/scan-secrets-test.sh` — PASS; 24 passed, 0 failed.
- `bash scripts/scan-secrets.sh` — PASS; 819 tracked files scanned, 16 `.gz`
  archives decompressed.
- `bash scripts/lint.sh` — PASS, including module tidiness, standalone
  tracked-file `gofmt` over 346 Go files, standalone tracked-file `goimports`
  over 346 Go files, `go vet`, pinned-version `golangci-lint`, standalone
  `gosec`, `govulncheck`, and frontend static checks.

Full local aggregate after the round-12 fixes:

- `bash scripts/gates.sh` — PASS; total 516s.
- Section timings: lint 28s, scan-secrets self-test 1s, live scan 14s,
  AI-attribution 0s, spec-drift self-test 28s, live spec-drift 1s, systemd 0s,
  build 6s, `test.sh` 373s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 6s, benchmark regression gate 58s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-05 — External review round 13

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the round-12 fixes, the 24-case scanner self-test, the
26-case spec-drift self-test, and the 516s full local aggregate evidence:

- `glm`: no blocking correctness, security, spec/code, or local/CI parity
  findings. Low non-blocking notes: grep error-suppression in the scanner is a
  theoretical edge; duplicated SQL parsing remains maintainability debt already
  tracked; redacted secret diagnostics intentionally expose only match length.
- `deepseek`: no correctness or security blockers. Low notes: a latent
  `pipefail` edge in the spec-drift failure reporter, the remaining `go vet
  ./...` vendored-Go scope already covered by SOW-0037, and close-out evidence
  still pending.
- `codex`: one actionable gap and two close-out notes:
  1. The tracked-file formatter behavior lacked a hermetic regression self-test
     that plants ignored/untracked malformed Go files.
  2. Short comments still described `scripts/lint.sh` as only
     `golangci`/`gosec`/`govulncheck`, not the full module tidy + formatter +
     vet + lint + security surface.
  3. CI URL, branch protection, `Outcome`, and `Lessons Extracted` are correctly
     still pending and must not be marked complete before they exist.

Resolution after the round-13 fixes:

- Added `scripts/test/lint-test.sh`, a hermetic self-test that creates a
  temporary git repo, tracks a valid Go file, plants ignored and untracked
  malformed Go files, proves `gofmt -l .` would fail, then proves formatter
  checks over `git ls-files -z -- '*.go'` pass.
- The self-test statically fails if `scripts/lint.sh` or `.github/workflows/ci.yml`
  regress to exact-dot formatter walks (`gofmt -l .`, `gofmt -l "."`,
  `goimports -l .`, or `goimports -l '.'`) or stop deriving formatter inputs
  from `git ls-files -z -- '*.go'`. It still allows the current per-file
  `./$f` form.
- Wired `scripts/test/lint-test.sh` into `scripts/gates.sh` as the first fast
  section, before `scripts/lint.sh`, so formatter-scope regressions fail with a
  narrow diagnostic.
- Wired the same self-test into the CI `gates` job with fail-closed presence
  checks.
- Updated `scripts/gates.sh`, `AGENTS.md`,
  `.agents/sow/specs/quality-gates.md`, and
  `.agents/skills/project-quality-gates/SKILL.md` so short descriptions match
  the current `scripts/lint.sh` contract.

Focused validation after the round-13 fixes:

- `bash -n scripts/test/lint-test.sh scripts/gates.sh scripts/lint.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `shellcheck -x scripts/test/lint-test.sh scripts/gates.sh scripts/lint.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `bash scripts/test/lint-test.sh` — PASS; 3 passed, 0 failed.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml` — PASS.
- `git diff --check HEAD` — PASS.

Full local aggregate after the round-13 fixes:

- `bash scripts/gates.sh` — PASS; total 519s.
- Section timings: lint formatter-scope self-test 0s, lint 28s,
  scan-secrets self-test 1s, live scan 14s, AI-attribution 0s,
  spec-drift self-test 27s, live spec-drift 1s, systemd 0s, build 6s,
  `test.sh` 374s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 6s, benchmark regression gate 61s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-05 — External review round 14

Reviewers run in parallel with the same broad, read-only scope as prior rounds,
plus notes about the round-13 fixes, the new formatter-scope self-test, the
24-case scanner self-test, the 26-case spec-drift self-test, and the 519s full
local aggregate evidence:

- `codex`: actionable findings:
  1. The CI `gates` job ran `scripts/test/lint-test.sh` after checkout without
     an explicit Go setup step, so the self-test depended on the runner image's
     incidental `gofmt` availability.
  2. `scripts/test/lint-test.sh` was still untracked before staging, so the live
     scanner's `git ls-files` surface did not yet include it.
  3. The self-test's static detector caught exact-dot formatter walks but not
     equivalent directory walks such as `gofmt -l ./` or `gofmt -l ./...`.
- `deepseek`: no blocking correctness or security findings. Medium close-out
  note: ensure `scripts/test/lint-test.sh` is explicitly staged so the
  fail-closed CI presence check can succeed.
- `glm`: no blocking correctness, security, spec/code, or local/CI parity
  findings. One proposed medium issue claimed bare command-substitution
  assignments lose the command status under `set -e`; local Bash evidence
  rejected that as a false positive (`x=$(false)` exits non-zero, while
  `local x=$(false)` masks the status; the script uses the bare form).

Resolution after the round-14 fixes:

- `.github/workflows/ci.yml` now sets up Go with `actions/setup-go@v6` and
  `go-version-file: go.mod` before running the `gates` job's formatter-scope
  self-test. The gate no longer relies on whatever `ubuntu-latest` happens to
  preinstall.
- `scripts/test/lint-test.sh` now flags formatter directory walks using `.`,
  `./`, and `./...`, including quoted variants. It still allows the production
  per-file form `gofmt -l "./$f"` / `goimports -l "./$f"`.
- The SOW-0013 file set was staged explicitly by path, including
  `scripts/test/lint-test.sh`. The live scanner then covered 820 tracked files,
  confirming the new self-test is inside the tracked scan surface.
- The Bash assignment finding was not changed in code because the actual shell
  behavior contradicts the finding for the bare assignment form used here.

Focused validation after the round-14 fixes:

- `bash -n scripts/test/lint-test.sh scripts/gates.sh scripts/lint.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `shellcheck -x scripts/test/lint-test.sh scripts/gates.sh scripts/lint.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `bash scripts/test/lint-test.sh` — PASS; 3 passed, 0 failed.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml` — PASS.
- `git diff --check HEAD` — PASS.
- Bash assignment behavior check — PASS; `bash -lc 'set -e; x=$(false)'`
  exited 1, while the `local x=$(false)` control case masked the status.

Full local aggregate after the round-14 fixes:

- First staged `bash scripts/gates.sh` run passed every section through
  frontend E2E/axe, then failed only at the final local benchmark regression
  gate while the workstation load average was 20.92 / 15.15 / 11.55. A
  standalone `scripts/check-bench.sh` rerun passed immediately after, confirming
  transient benchmark noise rather than a SOW-0013 runtime regression.
- Second staged `bash scripts/gates.sh` run — PASS; total 539s.
- Section timings: lint formatter-scope self-test 0s, lint 36s,
  scan-secrets self-test 1s, live scan 16s, AI-attribution 0s,
  spec-drift self-test 34s, live spec-drift 1s, systemd 0s, build 6s,
  `test.sh` 374s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 6s, benchmark regression gate 64s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-05 — External review round 15

Reviewers were run with the same broad, read-only scope as prior rounds, plus
notes about the round-14 fixes, the staged scanner coverage of
`scripts/test/lint-test.sh`, and the 539s full local aggregate evidence:

- `codex`: actionable findings:
  1. `scripts/test/lint-test.sh` still allowed the `goimports` behavioral proof
     to skip when `goimports` was absent. The CI `gates` job now sets up Go, but
     that job runs the self-test before `scripts/lint.sh` installs `goimports`,
     so the self-test needed to resolve or install `goimports` fail-closed.
  2. `scripts/lint.sh` failed closed on unparsable `golangci-lint version`
     output, but the intended custom diagnostic was unreachable because the
     parse pipeline exited under `set -euo pipefail` before the empty-version
     branch.
- `glm`: no blocking correctness, security, spec/code, or local/CI parity
  findings. Low/info notes were accepted as non-actionable: placeholder content
  in a scanner fixture, duplicated formatter-loop structure between production
  and test, Linux-only `/dev/stderr`, and SQL block-comment drift outside the
  project migration style.
- `deepseek`: the process handle disappeared before a final review message was
  captured. This is not counted as convergence evidence; the next round must
  include a fresh reviewer run.

Resolution after the round-15 fixes:

- `scripts/test/lint-test.sh` no longer skips the `goimports` behavioral proof.
  It first uses an existing `goimports`, then falls back to the Go install path,
  then installs `golang.org/x/tools/cmd/goimports@latest` fail-closed if needed.
  The behavior test now proves both broad `gofmt -l .` and broad
  `goimports -l .` trip on malformed ignored/untracked local Go files before
  proving tracked-file formatter calls pass.
- `scripts/lint.sh` now guards only the `golangci-lint version` parse pipeline
  with `|| true`, preserving hard failure while making the intended custom
  unparsable-version diagnostic reachable.
- Generated coverage-report PNGs were removed from `frontend/coverage/`. The
  verification left dependency PNG assets under `frontend/node_modules/` alone
  and found no tracked PNGs and no project PNGs outside dependencies.

Focused validation after the round-15 fixes:

- `bash -n scripts/test/lint-test.sh scripts/lint.sh scripts/gates.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `shellcheck -x scripts/test/lint-test.sh scripts/lint.sh scripts/gates.sh
  scripts/scan-secrets.sh scripts/spec-drift.sh
  scripts/test/scan-secrets-test.sh scripts/test/spec-drift-test.sh` — PASS.
- `bash scripts/test/lint-test.sh` — PASS; 3 passed, 0 failed.
- PNG cleanup verification — PASS; `git ls-files '*.png' '*.PNG'` found no
  tracked PNGs, and `find . -name '*.png' -not -path './.git/*' -not -path
  './frontend/node_modules/*'` found no project PNGs outside dependencies.
- `bash scripts/lint.sh` — PASS, including module tidiness, standalone
  tracked-file `gofmt` over 346 Go files, standalone tracked-file `goimports`
  over 346 Go files, `go vet`, pinned-version `golangci-lint`, standalone
  `gosec`, `govulncheck`, and frontend static checks.

Full local aggregate after the round-15 fixes:

- `bash scripts/gates.sh` — PASS; total 502s.
- Section timings: lint formatter-scope self-test 0s, lint 27s,
  scan-secrets self-test 2s, live scan 13s, AI-attribution 0s,
  spec-drift self-test 26s, live spec-drift 1s, systemd 0s, build 6s,
  `test.sh` 362s, Go coverage gate 0s, adapter fuzz seed 1s,
  frontend E2E+axe 5s, benchmark regression gate 59s.
- Go race/coverage: PASS. The long pole was `internal/ingest` under race at
  352s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-05 — External review round 16

Reviewers were run with the same broad, read-only scope as prior rounds, plus
notes about the round-15 fixes, the PNG cleanup, and the 502s full local
aggregate evidence:

- `codex`: actionable finding — `scripts/scan-secrets.sh` filtered exact
  placeholder commit metadata, but still added `basename "$HOME")` to the
  Rule-1 home-stem ban-list unconditionally. On a generic runner where
  `HOME=/home/user`, committed placeholder paths such as `/home/user/...` would
  be flagged as operator-home leaks even though they are intentionally portable
  fixture/documentation placeholders.
- `glm`: no blocking findings. Low notes were accepted as non-blocking:
  `CREATE TABLE IF NOT EXISTS` extraction is not needed by current migrations
  and remains fail-closed if drift appears; exact placeholder identity handling
  is intentionally narrow.
- `deepseek`: no blocking findings. Low notes were accepted as close-out
  housekeeping: tracked follow-up SOW artifacts must remain staged, historical
  self-test counts in prior review entries are not rewritten, and outcome/lesson
  sections remain pending until completion.

Spec delta before the fix:

- `.agents/sow/specs/quality-gates.md` now states that the exact neutral home
  stem `user` is ignored when it comes only from `$HOME`, so placeholder paths
  like `/home/user/...` stay portable across generic dev VMs.
- `.agents/skills/project-quality-gates/SKILL.md` mirrors the runtime rule.
- The rule remains derivation-only: tracked content is still scanned normally,
  real derived operator identities are never allow-listed, and an empty Rule-1
  ban-list remains a fail-closed error.

Resolution after the round-16 fix:

- `scripts/test/scan-secrets-test.sh` added
  `clean::placeholder_home_passes_with_home_user`, which runs the scanner with
  `HOME=/home/user` and tracks both `/home/user/...` and URL-encoded
  `%2Fhome%2Fuser%2F...` placeholder content.
- The new scanner self-test failed before the implementation fix, proving the
  reviewer finding was real: 24 scanner cases passed and the new case failed on
  the placeholder home examples.
- `scripts/scan-secrets.sh` now skips only the exact neutral `$HOME` stem
  `user` when it is derived from HOME. Non-placeholder git-derived `user` stems
  still remain Rule-1 inputs, so this is not a broad allow-list.
- Generated coverage-report PNGs were removed again after the full gate
  regenerated them. The verification left dependency PNG assets under
  `frontend/node_modules/` alone and found no tracked PNGs and no project PNGs
  outside dependencies.

Focused validation after the round-16 fix:

- `bash -n scripts/scan-secrets.sh scripts/test/scan-secrets-test.sh` — PASS.
- `shellcheck -x scripts/scan-secrets.sh scripts/test/scan-secrets-test.sh` —
  PASS.
- `bash scripts/test/scan-secrets-test.sh` — PASS; 25 passed, 0 failed.
- `bash scripts/scan-secrets.sh` — PASS; 820 tracked files scanned, 16 `.gz`
  archives decompressed.

Full local aggregate after the round-16 fix:

- `bash scripts/gates.sh` — PASS; total 508s.
- Section timings: lint formatter-scope self-test 0s, lint 29s,
  scan-secrets self-test 1s, live scan 13s, AI-attribution 0s,
  spec-drift self-test 27s, live spec-drift 1s, systemd units 0s, build 5s,
  `test.sh` 369s, Go coverage gate 1s, adapter fuzz seed 0s,
  frontend E2E+axe 7s, benchmark regression gate 55s.
- Go race/coverage: PASS. The long pole was `internal/ingest` under race at
  357s.
- Go coverage gate: internal aggregate 90.5%; all gated internal packages
  >=80%.
- Frontend Vitest: 623 tests passed; line coverage 94.69%.
- Playwright E2E+axe: 51 tests passed.
- Benchmark gate: PASS; no significant `sec/op` regression >20%.

### 2026-06-05 — External review round 17

Reviewers were run with the same broad, read-only scope as prior rounds, plus
notes about the round-16 HOME-placeholder fix, the 25-case scanner self-test, the
PNG cleanup, and the 508s full local aggregate evidence:

- `codex`: no blocking code, security, local/CI parity, generated-artifact, or
  unwanted-side-effect findings. It verified the scanner HOME-placeholder change
  is derivation-only, empty Rule 1 remains fail-closed, the repository history
  has non-placeholder identity inputs for CI, setup-go provides the Go install
  bin path for `goimports`, and no staged/tracked image artifacts exist. Only
  close-out note: update this SOW so reviewer convergence is no longer marked
  pending.
- `glm`: no blocking findings. It verified scanner 25/25, spec-drift 26/26,
  lint self-test 3/3, live scan/spec-drift, shellcheck, actionlint, diff-check,
  no PNGs, scanner HOME placeholder safety, spec/skill sync, SOW-0037
  cross-reference accuracy, and no unrelated production source changes. Only
  close-out note: update this SOW's pending-validation section after convergence.
- `deepseek`: no blocking findings. It verified scanner HOME placeholder safety,
  diagnostic redaction, local/CI formatter parity, spec/code synchronization,
  no generated artifacts, and SOW-0037 scope. One non-blocking CI style note was
  accepted as no-op: the `gates` job's Go setup is unconditional, which is fine
  because this repository requires `go.mod` for mature gates and other jobs fail
  closed on missing Go module prerequisites.

Resolution after round 17:

- No code, script, workflow, spec, or skill changes were required.
- This SOW's pending-validation summary was updated to mark external reviewer
  convergence done.
- Remaining close-out work is outside the staged code delta: push, record the CI
  run URL, confirm every CI row passes, merge, register branch-protection
  required checks, and then fill `Outcome`/`Lessons Extracted` before moving this
  SOW to completed.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- SOW-0043 — `gates.sh --fast` profile / parallelization for the measured full
  local aggregate runtime overage.
- SOW-0044 — CodeQL + Codacy defence layer requested after SOW-0013.
- SOW-0045 — gate contract hardening follow-ups from review round 2.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
