# SOW-0044 - Code Scanning Defence Layer (CodeQL + Codacy)

## Status

Status: open

Sub-state: active 2026-06-05. SOW-0013 is completed and `master` has green
post-merge `ci` and `codeql` runs at `1b4d777`; branch protection requires the
five CI jobs plus the three explicit CodeQL matrix jobs. This SOW is now the
active follow-up requested after SOW-0013.

## Requirements

### Purpose

Add a high-signal code-scanning defence layer that helps keep ai-viewer secure, low-complexity, maintainable, and honest about quality trends without drowning the project in noisy findings.

### User Request

After SOW-0013 finishes, add defences with code scanning using CodeQL and Codacy so the project can see whether it is doing a good job on complexity, maintainability, and security.

### Assistant Understanding

Facts:

- SOW-0013 added a baseline `.github/workflows/codeql.yml` workflow and required CodeQL status checks.
- The project already enforces local/CI gates for Go lint/security, frontend lint/types/tests/E2E/a11y, coverage, fuzz seed corpus, benchmark regression, secrets, AI-attribution, and spec drift.
- Codacy setup needs project-specific tuning: broad defaults first, then noise reduction using measured findings.
- Coverage upload to Codacy requires repository presence in Codacy and a project/API token configured as a GitHub secret.
- Codacy Cloud is reachable for `netdata/ai-viewer`; the repository is public,
  default branch is `master`, and Codacy already detects CSS, Go, HTML,
  JavaScript, JSON, Markdown, Shell, SQL, TypeScript, and YAML.
- Codacy Cloud currently reports a noisy baseline: 1,312 quality issues grouped
  across CodeStyle, Complexity, BestPractice, Security, ErrorProne, and related
  categories; 61 critical/high security findings; many findings appear to hit
  tests, tooling scripts, generated-style HTML strings, or broad SCA/CICD rules
  that need tuning before becoming a useful gate.
- GitHub repository secrets currently expose `CODACY_API_TOKEN` but not
  `CODACY_PROJECT_TOKEN`, so coverage upload must use Codacy account-token mode
  if wired in CI.

Inferences:

- CodeQL should not remain only a default workflow; it needs explicit query/security policy, required-check tracking, alert triage rules, and suppression rules with tracked rationale.
- Codacy should complement existing gates, not duplicate them noisily. Its best use here is maintainability and complexity trend visibility, security findings, duplication, and cloud-side quality reporting.
- Codacy coverage upload should reuse existing Go `coverage.out` and frontend `lcov` generation rather than introduce a second test path.

Unknowns:

- Which Codacy Cloud tools and organization-level coding standards are already enforced for the repository.
- Whether Codacy Cloud import will be blocked by organization-level coding
  standards; the local config can still be committed and validated even if Cloud
  import is partially blocked.
- Whether Codacy coverage upload accepts the existing Go `coverage.out` and
  frontend `lcov.info` paths in one account-token workflow without requiring a
  repository project token.

### Acceptance Criteria

1. CodeQL policy is hardened beyond the baseline SOW-0013 workflow: explicit supported languages, explicit matrix check names, query suite choice, suppression policy, and alert triage workflow documented in specs/skills.
2. Codacy local configuration exists and is tuned for this repository's Go + TypeScript/Vite stack, with irrelevant/noisy tools or patterns disabled only with evidence.
3. Codacy local analysis runs successfully and produces a machine-readable before/after summary of enabled tools, enabled patterns, issue counts by severity/category, and disabled-noise rationale.
4. Codacy Cloud integration is verified if the repo is present and credentials are available; otherwise, the SOW records the exact missing external setup without blocking local configuration.
5. Coverage upload to Codacy is wired only if `CODACY_PROJECT_TOKEN` or equivalent GitHub secret is available; Go and frontend coverage reports come from the existing test commands.
6. CI exposes CodeQL + Codacy outcomes without weakening existing gates. Any new required status checks are recorded in `.github/workflows-checks.yaml` and branch protection setup docs.
7. Specs, runtime skills, and operator docs are updated in the same commit.
8. All repo gates pass and external review converges before completion.

## Analysis

Sources checked:

- `configure-codacy` skill: local configuration through `.codacy/codacy.config.json`, broad init, measured noise reduction, optional Cloud import.
- `codacy-analysis-cli` skill: local analysis CLI workflow and JSON output requirements.
- `setup-coverage` skill: Codacy coverage upload prerequisites and existing coverage-report reuse.
- `.agents/sow/current/SOW-0013-20260526-repo-wide-gates-ci-workflow.md`: baseline CodeQL workflow and required-check infrastructure.

Current state:

- CodeQL baseline is staged in SOW-0013.
- Codacy is not configured in this repository yet.

Risks:

- Too many Codacy patterns can create noise and train maintainers to ignore findings.
- Importing Codacy config to Cloud without checking organization standards may fail or silently not apply.
- Coverage upload can leak repository metadata if tokens are mishandled; secrets must live only in GitHub/Codacy secret stores, never durable artifacts.
- Duplicate gates can waste CI time; Codacy should report quality/security trends and fail on selected high-confidence classes only after evidence.

## Pre-Implementation Gate

Status: ready.

Problem / root-cause model:

- SOW-0013 made CodeQL present and required, but still baseline/default: there
  is no explicit CodeQL query-suite policy, suppression policy file, or
  code-scanning operating contract in the specs.
- Codacy Cloud is already active but too noisy to trust as a blocking signal:
  current Cloud data reports 1,312 quality issues and 61 critical/high security
  findings. A noisy scanner trains maintainers to ignore it, which is worse than
  no scanner. The correct order is measured baseline → tune exclusions/tools →
  validate locally → import/verify Cloud where credentials and org policy allow
  it → only then consider hard enforcement.
- Coverage data exists locally through the existing Go and frontend gates, but
  Codacy coverage upload is not wired. The repo has `CODACY_API_TOKEN`, not
  `CODACY_PROJECT_TOKEN`, so upload must use account-token mode and must never
  write token values to disk.

Evidence reviewed:

- `.github/workflows/codeql.yml`: baseline CodeQL matrix exists for `go`,
  `javascript-typescript`, and `actions`; workflow currently runs default
  queries with no config file.
- `.github/workflows/ci.yml`: existing `test` and `frontend` jobs generate Go
  `coverage.out` and frontend coverage artifacts; no Codacy coverage upload
  step exists.
- `.github/workflows-checks.yaml` and branch-protection API: required checks are
  `lint`, `test`, `frontend`, `embed-smoke`, `gates`, `CodeQL (go)`,
  `CodeQL (javascript-typescript)`, and `CodeQL (actions)`.
- `codacy repository gh netdata ai-viewer --output json`: repo is present in
  Codacy Cloud, public, default branch `master`, last updated on the SOW-0013
  close-out merge.
- `codacy tools gh netdata ai-viewer --output json`: Cloud tools currently
  include enabled SQLint, Trivy, TSQLLint, Jackson Linter, Opengrep, ShellCheck,
  Agentlinter, Lizard, PMD, Stylelint, markdownlint, and ESLint; several local
  Go tools are disabled in Cloud because this repo already gates them locally.
- `codacy issues gh netdata ai-viewer --overview --output json`: 1,312 Cloud
  issues before tuning.
- `codacy findings gh netdata ai-viewer --severities Critical,High --output
  json`: 61 critical/high findings before tuning.
- `codacy-analysis discover`: local stack detection reports CSS, Go, HTML, JSON,
  JavaScript, Markdown, SQL, Shell, TypeScript, YAML; React/React DOM 19.2.6;
  ESLint 9.39.4; Playwright 1.60.0; TypeScript 6.0.3; Vite 8.0.14; Vitest
  4.1.7.
- GitHub official CodeQL docs, checked 2026-06-05:
  `security-extended` adds lower-precision/lower-severity queries over
  `default`, and advanced setup can use a custom config file plus explicit
  query suites.
- Codacy official docs, checked 2026-06-05: repository `.codacy.yml` affects
  Cloud analysis and default-branch precedence; Codacy coverage upload needs
  either `CODACY_PROJECT_TOKEN` or account-token variables; the recommended
  coverage reporter path supports CI upload.

Affected contracts and surfaces:

- `.github/workflows/codeql.yml`: add explicit config-file use and query policy.
- New `.github/codeql/codeql-config.yml`: central CodeQL policy/suppression
  file. Initial policy uses high-signal CodeQL query suites and no broad path
  suppression unless evidence requires it.
- New or updated Codacy local config under `.codacy/`: committed local Analysis
  CLI configuration plus a `.codacy/.gitignore` that excludes generated tool
  material.
- Optional `.codacy.yml`: only if Cloud-level path exclusions are needed for
  generated artifacts and local config import cannot express the Cloud behavior
  safely.
- `.github/workflows/ci.yml`: optional Codacy coverage upload steps gated on
  available secrets and existing coverage output; must not weaken existing jobs.
- `.github/workflows-checks.yaml` and `docs/setup.md`: update only if a new
  required status context is intentionally introduced. Default decision: do not
  add Codacy as a required status until issue noise is tuned and measured.
- `.agents/sow/specs/quality-gates.md`,
  `.agents/skills/project-quality-gates/SKILL.md`,
  `.agents/sow/specs/security.md`, and `.agents/sow/specs/testing-strategy.md`:
  document CodeQL/Codacy policy, suppression rules, and coverage-upload
  contract.
- `README.md` / `docs/setup.md`: update operator-facing setup notes only for
  non-secret configuration and required GitHub secrets.

Spec deltas to land before tests or implementation:

- `quality-gates.md`: add a "Code Scanning Defence Layer" subsection describing
  CodeQL query-suite policy, Codacy local/cloud signal role, suppression rules,
  and why Codacy is initially reporting/tuning rather than an extra hard gate.
- `security.md`: add the CodeQL/Codacy triage contract for SAST/SCA/CICD
  findings: critical/high security findings are either fixed, proven
  false-positive with evidence, or tracked in a SOW; never silently ignored.
- `testing-strategy.md`: add Codacy coverage-upload policy: reuse existing Go
  `coverage.out` and frontend `lcov.info`; upload only when account/project
  tokens are available as GitHub secrets; no duplicate test path.
- `project-quality-gates/SKILL.md`: add runtime commands for CodeQL/Codacy
  checks, local Codacy analysis, coverage upload verification, and suppression
  hygiene.

Existing patterns to reuse:

- SOW-0013's workflow-check contract: any required check name lives in
  `.github/workflows-checks.yaml` and branch protection is updated via GitHub
  API after the workflow is green on `master`.
- Existing test jobs generate the coverage data; do not add parallel duplicate
  test commands just for Codacy.
- Existing scanner policy: noisy findings are tuned by evidence, not hidden by
  broad unreviewed exclusions.

Risk and blast radius:

- **R1 — noisy Codacy hard gate.** Making Codacy required before tuning would
  block useful PRs and teach maintainers to ignore it. Mitigation: keep Codacy
  as measured/reporting until the tuned issue set is high-signal; record any
  future required context as a separate explicit decision.
- **R2 — Cloud import side effects.** Importing local Codacy tool/pattern config
  changes repository settings outside git and may conflict with organization
  coding standards. Mitigation: capture before/after tool and issue summaries;
  import only after local config passes; record blocked imports explicitly.
- **R3 — secret handling.** Coverage upload tokens must never be written to disk
  or logs. Mitigation: use GitHub secrets only; record secret names, not values;
  rerun `scripts/scan-secrets.sh`.
- **R4 — duplicate CI runtime.** Running tests twice for coverage upload would
  slow the already-long `test` path. Mitigation: upload artifacts generated by
  existing jobs.
- **R5 — CodeQL false positives from broad suites.** `security-extended` may add
  lower-precision findings. Mitigation: start with security-focused suites, keep
  suppressions path/query scoped with linked SOW/issue rationale, never blanket
  disable a language.

Sensitive data handling plan:

- Do not write Codacy token values, account usernames, personal data, or raw
  secret-bearing CLI output into the repo.
- Durable artifacts may record secret names (`CODACY_API_TOKEN`,
  `CODACY_PROJECT_TOKEN`) and aggregate counts; avoid raw account metadata.
- Keep Codacy issue exports in `/tmp` unless a sanitized summary is needed.
- Run `scripts/scan-secrets.sh` before every commit.

Implementation plan:

1. Move this SOW to `current/` and commit the completed Pre-Implementation Gate
   plus spec deltas.
2. Generate/fetch Codacy local configuration from Cloud if possible; otherwise
   initialize broad local config with Analysis CLI. Save before/broad/tuned
   machine-readable summaries under a non-sensitive tracked path only if needed;
   raw results stay in `/tmp`.
3. Run local Codacy analysis with install-dependencies and JSON output; classify
   findings by tool/category/severity/path, then tune config by evidence.
4. Harden CodeQL policy through `.github/codeql/codeql-config.yml` and workflow
   wiring. Default query-suite decision: use `security-extended` for the first
   hardened pass; avoid `security-and-quality` as a required blocking surface
   until measured because this repo already has extensive quality gates.
5. Add Codacy coverage upload using existing Go/frontend coverage outputs only
   when secrets are available. With current secret state, prefer account-token
   mode using `CODACY_API_TOKEN`, `CODACY_ORGANIZATION_PROVIDER=gh`,
   `CODACY_USERNAME=netdata`, and `CODACY_PROJECT_NAME=ai-viewer`.
6. Add local self-tests/config checks where practical: Codacy config JSON
   validity, CodeQL YAML/actionlint validity, workflow secret-gating logic.
7. Run focused validation, then full `scripts/gates.sh`.
8. Run external review with at least three reviewers; iterate until no
   actionable findings remain.
9. Push PR, verify every PR check row passes, merge, verify post-merge master
   runs, update this SOW to completed, move it to `done/`.

Validation plan:

- `codacy-analysis discover --output-format json` succeeds.
- `codacy-analysis analyze --inspect --output-format json` succeeds with the
  tuned config and no unexpected unavailable required tools.
- `codacy-analysis analyze --install-dependencies --output-format json --output
  /tmp/ai-viewer-codacy-tuned.json` succeeds or records exact unsupported-tool
  blockers without weakening existing gates.
- Codacy Cloud before/after summaries captured via `codacy tools`, `codacy
  issues --overview`, and `codacy findings`.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml` passes.
- YAML/JSON config parse checks pass for all new config files.
- Existing local gates pass: `scripts/scan-secrets.sh`, `scripts/spec-drift.sh`,
  and final `scripts/gates.sh`.
- PR checks pass: required CI, CodeQL, Codacy Cloud row, reviewer row.

Open decisions:

- No operator decision required. Technical decisions are within CTO authority.
- If Codacy Cloud import is blocked by organization-level policy, record the
  exact blocker and complete the local/repo-side configuration without waiting
  on the operator.

## Plan

See the Pre-Implementation Gate. Work proceeds spec-first, then test/config
validation, then implementation, review, full gates, PR, merge, and SOW
close-out.

## Execution Log

### 2026-06-04 - Filed

- Filed as the tracked follow-up for code-scanning defences after SOW-0013.
- Scope deliberately excludes SOW-0013's baseline CodeQL workflow so SOW-0013 can close cleanly first.

### 2026-06-05 - Activated

- Moved to `current/` after SOW-0013 completed.
- Verified `master` state: post-merge `ci` and `codeql` green at `1b4d777`.
- Verified Codacy Cloud is reachable for `netdata/ai-viewer`.
- Verified only `CODACY_API_TOKEN` is currently available as a GitHub secret, so
  coverage upload must use account-token mode unless a project token is added
  later.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.
