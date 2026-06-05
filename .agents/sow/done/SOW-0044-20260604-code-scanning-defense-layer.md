# SOW-0044 - Code Scanning Defence Layer (CodeQL + Codacy)

## Status

Status: completed

Sub-state: completed 2026-06-05 after external review convergence and full
local gate validation.

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
- Codacy Cloud currently reports a noisy baseline at
  `1b4d777e05d8e6a792b411112a8f143f09c0260c`: 1,312 quality issues grouped as
  2 Error, 91 High, 778 Warning, and 441 Info; category counts include 61
  Security, 579 Complexity, 394 CodeStyle, 159 BestPractice, 44 ErrorProne, 32
  UnusedCode, 19 Comprehensibility, 12 Compatibility, and 12 Performance.
  Critical/high security findings total 61: 2 Critical and 59 High.
- GitHub repository secrets currently expose `CODACY_API_TOKEN` but not
  `CODACY_PROJECT_TOKEN`, so coverage upload must use Codacy account-token mode
  if wired in CI.

Inferences:

- CodeQL should not remain only a default workflow; it needs explicit query/security policy, required-check tracking, alert triage rules, and suppression rules with tracked rationale.
- Codacy should complement existing gates, not duplicate them noisily. Its best use here is maintainability and complexity trend visibility, security findings, duplication, and cloud-side quality reporting.
- Codacy coverage upload should reuse existing Go `coverage.out` and frontend `lcov` generation rather than introduce a second test path.

Unknowns:

- Whether a future Codacy Cloud import/promotion will be blocked by repository
  or organization policy; the local config can still be committed and validated
  even if Cloud import is partially blocked.
- Whether Codacy Cloud counts will match local tuned counts after the SOW-0046
  triage/import step. SOW-0044 deliberately leaves Cloud settings unchanged.

### Acceptance Criteria

1. CodeQL policy is hardened beyond the baseline SOW-0013 workflow: explicit supported languages, explicit matrix check names, query suite choice, suppression policy, and alert triage workflow documented in specs/skills.
2. Codacy local configuration exists and is tuned for this repository's Go + TypeScript/Vite stack, with irrelevant/noisy tools or patterns disabled only with evidence.
3. Codacy local analysis executes against the tuned available-tool set and produces a machine-readable before/after summary of enabled tools, enabled patterns, issue counts by severity/category, and disabled-noise rationale. If analyzers exit non-zero because findings or local tool errors exist, the exact counts and blockers are recorded instead of weakening the scanner.
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
  `coverage.out` and frontend `lcov.info`; upload from a non-required reporting
  job only when account/project tokens are available as GitHub secrets; no
  duplicate test path.
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

- Do not write Codacy token values, private account identifiers, personal data,
  or raw secret-bearing CLI output into the repo.
- Durable artifacts may record secret names (`CODACY_API_TOKEN`,
  `CODACY_PROJECT_TOKEN`), public repository coordinates needed for account-token
  mode, and aggregate counts; avoid raw private account metadata.
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
   when secrets are available. Keep the upload in a non-required
   `codacy-coverage` reporting job fed from existing artifacts, not inside the
   required `test` or `frontend` jobs. With current secret state, prefer
   account-token mode using `CODACY_API_TOKEN`,
   `CODACY_ORGANIZATION_PROVIDER=gh`, `CODACY_USERNAME=netdata`, and
   `CODACY_PROJECT_NAME=ai-viewer`.
6. Add local self-tests/config checks: Codacy config JSON validity, CodeQL
   YAML/actionlint validity, and a hermetic
   `scripts/test/codacy-coverage-upload-test.sh` for the Codacy coverage upload
   state machine.
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
- Codacy Cloud summaries captured via `codacy tools`, `codacy issues
  --overview`, and `codacy findings`; local config import into Cloud is deferred
  to SOW-0046 because the current Cloud issue set is still noisy.
- `actionlint .github/workflows/ci.yml .github/workflows/codeql.yml` passes.
- YAML/JSON config parse checks pass for all new config files.
- `scripts/test/codacy-coverage-upload-test.sh` covers token-mode selection,
  pull-request skip before token selection, missing or empty report
  combinations, partial/final sequencing, reporter bootstrap validation, and
  LCOV path normalization. The self-test is wired into local `scripts/gates.sh`
  and the CI `gates` job.
- Existing local gates pass: `scripts/scan-secrets.sh`, `scripts/spec-drift.sh`,
  and final `scripts/gates.sh`.
- PR checks pass: required CI, CodeQL, any Codacy Cloud row if present, and the
  reviewer row.

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

### 2026-06-05 - Implementation

- Imported the current Codacy Cloud tool/pattern configuration into
  `.codacy/codacy.config.json` with 11 locally represented tools and 1,243
  enabled patterns. Added `.codacy/.gitignore` for generated local analyzer
  material.
- Pruned imported Semgrep patterns for languages Codacy did not detect in this
  repository and that do not exist in the tracked source tree outside
  dependencies: Python, Java, Ruby, Terraform, C#, PHP, Scala, Rust, Apex,
  Kotlin, Swift, C, and C++. The tuned config keeps detected-language and
  cross-language rules.
- Removed PMD and SQLint from the committed local Analysis CLI config after
  local validation showed they are not high-signal for this Go + TypeScript/Vite
  repository in their current Codacy runner form. PMD reported `issueCount: 0`,
  `errorCount: 17`, and `filesAnalyzed: 17` while emitting parse errors for the
  SQLite migrations and opencode fixture SQL, so it produced analyzer noise
  rather than findings. SQLint produced no issues because the local runner could
  not install its pinned `sqlint:0.2.1` dependency chain: the `pg_query-2.2.1`
  native extension failed to compile during `gem install`. The final committed
  local config has 9 tools and 625 enabled patterns; SOW-0046 owns any future
  SQL linting decision after critical/high triage.
- Tuned the Codacy local config with evidence-backed global exclusions for SOW
  lifecycle archives, symlinked instruction aliases, generated embed output,
  frontend dependency directories, coverage/build outputs, and local binary
  outputs.
- Hardened CodeQL by adding `.github/codeql/codeql-config.yml` and wiring
  `.github/workflows/codeql.yml` to load it. The config enables
  `security-extended` without disabling default queries and without adding
  suppressions.
- Added a non-required `codacy-coverage` reporting job that depends on the
  required `test` and `frontend` jobs, downloads their existing coverage
  artifacts, uploads each present non-empty Go/frontend report as a Codacy
  partial report, and sends Codacy's required `final` notification after at
  least one partial upload was attempted even if one partial command fails. A
  missing or empty Go report does not block uploading frontend LCOV, and a
  missing or empty frontend LCOV report does not block uploading Go coverage.
  The job supports
  `CODACY_PROJECT_TOKEN` repository-token mode and `CODACY_API_TOKEN`
  account-token mode, gives the project token explicit precedence if both token
  secrets exist, skips the entire job on `pull_request` events before checkout,
  artifact download, secret injection, or repository scripts can run, still
  refuses all Codacy coverage upload on `pull_request` events before token-mode
  selection as defense in depth, skips cleanly when neither token is present,
  normalizes frontend LCOV paths from
  `src/...` or absolute `/.../frontend/src/...` paths to `frontend/src/...`, and
  emits annotations but exits successfully for expected reporting failures while
  Codacy remains reporting-only.
- Extracted the Codacy upload state machine into
  `scripts/codacy-coverage-upload.sh` and added the hermetic
  `scripts/test/codacy-coverage-upload-test.sh` self-test. The self-test covers
  token-mode selection, PR skip before token selection, project-token precedence,
  missing or empty report combinations, partial/final sequencing, reporter
  bootstrap validation, and LCOV normalization.
- Wired `scripts/test/codacy-coverage-upload-test.sh` into local
  `scripts/gates.sh` and the CI `gates` job, and made CI fail closed when either
  the upload script or its self-test is absent.
- Download of Codacy's reporter bootstrap now uses a temporary file, HTTP
  failure checking, retries, a non-empty shell-script guard, and `bash -n`
  before execution; no process substitution is used.
- Added LCOV output to the existing Vitest coverage reporters so frontend
  coverage can be uploaded without a duplicate test path.
- Updated `docs/setup.md` with CodeQL config and Codacy coverage setup notes.
- Filed follow-up `SOW-0046-20260605-codacy-critical-high-triage.md` for the
  remaining Error/High security findings and complexity backlog before Codacy is
  considered for required branch protection.

## Validation

- CodeQL YAML/config validation passed with `actionlint
  .github/workflows/codeql.yml` and YAML parsing.
- Coverage workflow validation passed with `actionlint .github/workflows/ci.yml`.
- Frontend coverage generation passed in the delegated slice:
  `npm run test -- --run --coverage` produced `frontend/coverage/lcov.info`.
- Codacy local inspect with the final tuned config reports 9 locally ready
  tools and 0 unavailable tools: Agentlinter, ESLint8, Jackson Linter, Lizard,
  Opengrep, ShellCheck, Stylelint, Trivy, and markdownlint.
- Codacy local analysis of the 9 ready tools after exclusions and
  language-pattern/tool pruning ran and produced a measured reporting baseline:
  984 issues, grouped as 4 Error, 95 High, 705 Warning, and 180 Info.
  Categories: 99 Security, 605 Complexity, 120 BestPractice, 114 CodeStyle, 43
  Comprehensibility, and 3 ErrorProne. Tool counts: Lizard 605, Agentlinter 136,
  markdownlint 115, ESLint8 91, Stylelint 22, Opengrep/Semgrep 8, ShellCheck 7,
  Trivy 0, Jackson Linter 0. Analyzer errors remain: ESLint8 1 and Semgrep 1.
  The run exits non-zero because findings/errors exist; this is why Codacy is
  not a required branch-protection status in this SOW.
- Removed-tool evidence was recorded from local Codacy probes: PMD analyzed the
  tracked SQL set with 0 issues, 17 analyzer errors, and 17 files analyzed
  because it could not parse the repository's SQLite migrations and opencode
  fixture SQL; SQLint had 0 issues and was unavailable after install because
  `gem install sqlint:0.2.1` failed compiling `pg_query-2.2.1`. These removals
  do not mean SQL is out of scope; SOW-0046 decides whether to restore a SQL
  analyzer or keep relying on retained Semgrep SQL-injection patterns plus the
  existing SQLite tests.
- Codacy Cloud integration was verified without changing Cloud settings:
  repository present, public, default branch `master`, 12 enabled Cloud tools,
  1,312 quality issues, 61 critical/high security findings, 44% complex files,
  and 38% duplication at the SOW-0013 merge commit. Cloud import/promotion is
  deferred to SOW-0046 so noisy findings are triaged before external settings
  are changed.
- Fast config validation after review fixes passed: `actionlint`, workflow YAML
  parsing, CodeQL config YAML parsing, `jq empty .codacy/codacy.config.json`,
  `frontend npm run check:coverage-config`.
- Codacy coverage upload state-machine and workflow-boundary validation passed:
  `bash scripts/test/codacy-coverage-upload-test.sh` reported 97/97 assertions
  passing. The added workflow assertion verifies `codacy-coverage` has a
  job-level `if:` before `steps:`, excludes `pull_request`, preserves the
  `test`/`frontend` success dependencies, and places that guard before
  checkout, artifact download, Codacy secret references, and repository script
  execution. The script-level assertions verify both account-token and
  project-token PR cases skip before token-mode selection, reporter download,
  or reporter execution; empty-report assertions verify zero-byte Go/frontend
  coverage reports are annotated, not uploaded, and do not block uploading the
  other non-empty report.
- Shell syntax/lint validation passed for the new upload surfaces:
  `bash -n scripts/gates.sh scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh` and `shellcheck
  scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh scripts/gates.sh`.
- Post-extraction focused validation passed: `actionlint
  .github/workflows/ci.yml .github/workflows/codeql.yml`, workflow/CodeQL YAML
  parsing, `jq empty .codacy/codacy.config.json`, `frontend npm run
  check:coverage-config`, `scripts/spec-drift.sh`, `scripts/scan-secrets.sh`,
  and `git diff --check`.
- Post-PR-boundary focused validation passed: `bash
  scripts/test/codacy-coverage-upload-test.sh`, `bash -n scripts/gates.sh
  scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh`, `shellcheck
  scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh scripts/gates.sh`, `actionlint
  .github/workflows/ci.yml .github/workflows/codeql.yml`, workflow/CodeQL YAML
  parsing, `jq empty .codacy/codacy.config.json`, `frontend npm run
  check:coverage-config`, `scripts/spec-drift.sh`, `scripts/scan-secrets.sh`,
  and `git diff --check`.
- Post-script-level PR skip focused validation passed: `bash
  scripts/test/codacy-coverage-upload-test.sh` reported 79/79 assertions
  passing; `bash -n scripts/gates.sh scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh`, `shellcheck
  scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh scripts/gates.sh`, `actionlint
  .github/workflows/ci.yml .github/workflows/codeql.yml`, workflow/CodeQL YAML
  parsing, `jq empty .codacy/codacy.config.json`, `frontend npm run
  check:coverage-config`, `scripts/spec-drift.sh`, `scripts/scan-secrets.sh`,
  and `git diff --check` all passed.
- Post-empty-report/parameter-cleanup focused validation passed: `bash
  scripts/test/codacy-coverage-upload-test.sh` reported 97/97 assertions
  passing; `bash -n scripts/gates.sh scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh`, `shellcheck
  scripts/codacy-coverage-upload.sh
  scripts/test/codacy-coverage-upload-test.sh scripts/gates.sh`, `actionlint
  .github/workflows/ci.yml .github/workflows/codeql.yml`, workflow/CodeQL YAML
  parsing, `jq empty .codacy/codacy.config.json`, `frontend npm run
  check:coverage-config`, `scripts/spec-drift.sh`, `scripts/scan-secrets.sh`,
  and `git diff --check` all passed.
- Full local workstation gate passed: `./scripts/gates.sh` completed with
  `[PASS] gates.sh: every quality gate green` in 509s. Covered gates: lint
  formatter self-test, Go/frontend static analysis, secrets scan, AI-attribution
  scan, spec drift, Codacy coverage upload self-test, systemd unit lint, build
  and bundle-size gate, Go race tests with coverage, Go coverage threshold,
  adapter fuzz seed corpus, frontend E2E plus axe, and benchmark regression.
  Generated coverage PNG report assets were removed after the run.
- Pre-commit staged-index validation passed: `scripts/scan-secrets.sh` reported
  no secrets or operator PII across 826 tracked files after the new files were
  staged, and `git diff --cached --check` reported no whitespace errors.

Machine-readable sanitized local Codacy baseline:

```json
{
  "sow": "SOW-0044",
  "generatedAt": "2026-06-05",
  "config": {
    "importedCloud": {
      "enabledTools": 11,
      "enabledPatterns": 1243
    },
    "committedLocal": {
      "enabledTools": 9,
      "enabledPatterns": 625,
      "exclude": [
        ".agents/sow/current/**",
        ".agents/sow/done/**",
        ".agents/sow/pending/**",
        "CLAUDE.md",
        "GEMINI.md",
        "cmd/ai-viewer-serve/frontend_dist/**",
        "frontend/node_modules/**",
        "frontend/coverage/**",
        "frontend/dist/**",
        "frontend/playwright-report/**",
        "frontend/test-results/**",
        "bin/**"
      ],
      "removedLocalTools": [
        "PMD",
        "SQLint"
      ],
      "removedLocalToolEvidence": {
        "PMD": {
          "issueCount": 0,
          "errorCount": 17,
          "filesAnalyzed": 17,
          "reason": "PMDException parse errors on SQLite migrations and opencode fixture SQL; no findings produced"
        },
        "SQLint": {
          "issueCount": 0,
          "ready": false,
          "reason": "sqlint:0.2.1 install failed because pg_query-2.2.1 native extension did not compile"
        }
      }
    }
  },
  "localAnalysis": {
    "commandScope": [
      "Trivy",
      "jackson",
      "Semgrep",
      "shellcheck",
      "Agentlinter",
      "Lizard",
      "Stylelint",
      "markdownlint",
      "ESLint8"
    ],
    "issues": {
      "total": 984,
      "bySeverity": {
        "Error": 4,
        "High": 95,
        "Warning": 705,
        "Info": 180
      },
      "byCategory": {
        "Security": 99,
        "Complexity": 605,
        "BestPractice": 120,
        "CodeStyle": 114,
        "Comprehensibility": 43,
        "ErrorProne": 3
      },
      "byTool": {
        "Agentlinter": 136,
        "ESLint8": 91,
        "Jackson Linter": 0,
        "Lizard": 605,
        "markdownlint": 115,
        "Opengrep": 8,
        "ShellCheck": 7,
        "Stylelint": 22,
        "Trivy": 0
      }
    },
    "analyzerErrors": [
      {
        "toolId": "Semgrep",
        "phase": "toolInvoke",
        "kind": "Syntax error",
        "level": "warning",
        "filePath": "scripts/scan-secrets.sh"
      },
      {
        "toolId": "ESLint8",
        "phase": "toolConfig",
        "kind": "InvalidRuleConfig",
        "level": "warning"
      }
    ]
  },
  "cloudBaselineAtSow0013Merge": {
    "commit": "1b4d777e05d8e6a792b411112a8f143f09c0260c",
    "qualityIssues": 1312,
    "criticalHighSecurityFindings": 61,
    "complexFilesPercent": 44,
    "duplicationPercent": 38
  },
  "followUp": "SOW-0046"
}
```

## Reviews

### 2026-06-05 - Review round 1

- Reviewer A found that a failed Codacy partial upload could prevent the
  required `final` notification from running; fixed by accumulating upload
  failures and attempting `final` after both partial upload attempts.
- Reviewer A found missing coverage-artifact directories could make `find`
  fail before an annotation was emitted; fixed with explicit directory creation
  before report discovery.
- Reviewer A found token mode ambiguity when both Codacy token secrets exist;
  fixed by giving `CODACY_PROJECT_TOKEN` precedence and unsetting account-token
  variables in that path.
- Reviewer A found sensitive-data wording that could be read as forbidding
  public repository coordinates needed by account-token mode; fixed by
  distinguishing token/private account data from public repository metadata.
- Reviewer B found no blocker; it noted that `codacy-coverage` may show as
  skipped when required upstream jobs fail, which is accepted while the job is
  reporting-only.
- Reviewer C found that expected reporting failures should annotate and exit
  successfully, and that the CodeQL config path should avoid a redundant `./`;
  both were fixed.

### 2026-06-05 - Review round 2

- Reviewers found no required-gate regression, no secret-handling issue, and no
  CodeQL wiring bug.
- Reviewers requested a shallow checkout for the reporting job and no persisted
  checkout credentials before remote reporter execution; fixed with
  `fetch-depth: 1` and `persist-credentials: false`.
- Reviewers requested that unexpected upload-step script failures remain
  visible; fixed by removing `continue-on-error` from the upload step while
  expected reporting failures still annotate and exit successfully.
- Reviewers requested stronger validation of Codacy's reporter bootstrap; fixed
  with a non-empty shell-script check and `bash -n` before execution.
- Reviewers requested explicit `frontend/node_modules/**` exclusion in the
  Codacy config; fixed and local analysis was rerun with unchanged issue counts.
- Reviewers found stale security-spec text claiming `golangci-lint` runs
  `gosec` and `npm audit` is a CI gate; fixed to match the actual standalone
  `gosec`, `govulncheck`, Dependabot, and scanner-visibility contract.
- Reviewers requested durable machine-readable validation evidence; added the
  sanitized JSON baseline above.
- Reviewers identified framework-specific Semgrep patterns that may be absent
  from the current stack; recorded for SOW-0046 triage instead of silently
  pruning without a focused finding review.

### 2026-06-05 - Review round 3

- Reviewers found that the checkout hardening intended for the non-required
  `codacy-coverage` job had landed on the required `test` job instead. Fixed by
  restoring the `test` checkout to `fetch-depth: 0` and applying
  `fetch-depth: 1` plus `persist-credentials: false` to `codacy-coverage`.
- Reviewers found that a missing Go or frontend coverage report would skip all
  upload work, including a present report from the other stack. Fixed so missing
  reports emit annotations, any present report is uploaded as a partial report,
  and Codacy `final` is sent only when at least one partial upload was
  attempted.

### 2026-06-05 - Review round 4

- Reviewers found the Codacy upload state machine needed direct self-test
  coverage instead of relying on workflow review. Fixed by extracting
  `scripts/codacy-coverage-upload.sh`, adding the hermetic self-test, and wiring
  the self-test into local and CI gates.
- Reviewers found account-token mode should not run on `pull_request` events.
  Fixed by preferring `CODACY_PROJECT_TOKEN`, unsetting account-token variables
  in project-token mode, and skipping account-token mode on pull requests.
- Reviewers found stale CI wording in `testing-strategy.md` that described the
  older branch/gate contract. Fixed by replacing it with the current job-level
  testing surfaces and a pointer to the authoritative `quality-gates.md`
  contract.
- Reviewers found frontend LCOV normalization did not cover `SF:./src/...`.
  Fixed with a normalization case and matching self-test assertion.
- Reviewers noted the self-test did not exercise the both-reports success path.
  Fixed with a Go+frontend happy-path case that verifies both partial uploads,
  `final` after the partials, and no missing-report annotations.
- Reviewers found no blocker in the final CodeQL or Codacy workflow wiring.
  Framework-specific Codacy pattern pruning remains tracked in SOW-0046.

### 2026-06-05 - Review round 5

- Reviewers reran the full SOW-0044 scope after the both-reports happy-path
  test was added. Three reviewers found no blockers and confirmed the SOW can
  close after full local gates pass.
- One reviewer reported that Codacy `final` failure used a file-scoped
  annotation. This was a false positive: `scripts/codacy-coverage-upload.sh`
  uses the generic `annotate_error "Codacy coverage final notification failed"`
  path, and `test_final_failure_annotation` checks the exact generic
  `::error::Codacy coverage final notification failed` output.
- Non-blocking observations remain: some framework-specific Codacy patterns are
  intentionally deferred to SOW-0046, and the normalized frontend LCOV variable
  name can be revisited only if future maintenance makes it confusing.

### 2026-06-05 - Review round 6

- One reviewer found a real PR-secret boundary issue: even though the upload
  script skipped account-token mode on `pull_request`, the workflow still
  injected Codacy secrets into a job that checked out and executed
  PR-controlled repository scripts. Fixed by moving the `pull_request`
  exclusion to the `codacy-coverage` job-level `if:` so the job is skipped
  before checkout, artifact download, secret injection, or repository script
  execution. GitHub's official Actions documentation describes
  `jobs.<job_id>.if` as the job-level condition that prevents the job from
  running when false.
- Added a hermetic workflow-boundary assertion to
  `scripts/test/codacy-coverage-upload-test.sh`; the self-test now fails if the
  PR exclusion is missing, appears after `steps:`, omits the existing
  `test`/`frontend` success dependencies, or is placed after checkout,
  artifact-download, Codacy secret, or repository-script markers.

### 2026-06-05 - Review round 7

- Reviewers reran the full SOW-0044 scope after the workflow-level PR boundary
  fix. They confirmed the job-level `if:` resolves the immediate PR secret
  exposure risk.
- One reviewer found a real defense-in-depth/spec-consistency issue: the upload
  script still selected project-token mode when run directly with
  `GITHUB_EVENT_NAME=pull_request`, while the specs said PR coverage upload is
  disabled until a future safe path exists. Fixed by making
  `scripts/codacy-coverage-upload.sh` exit before any token-mode selection on
  `pull_request` events, regardless of which Codacy token variables are
  present.
- Extended `scripts/test/codacy-coverage-upload-test.sh` to prove account-token
  and project-token PR invocations both skip before reporter download/execution,
  and that project-token precedence remains unchanged on non-PR events.

### 2026-06-05 - Review round 8

- Reviewers reran the full SOW-0044 scope after the script-level PR skip fix
  and found no blockers. One reviewer raised two low-maintenance findings:
  zero-byte coverage files should not be uploaded, and `upload_reports()` should
  not depend on implicit global report variables.
- Fixed both findings by treating zero-byte Go/frontend coverage files as
  missing-or-empty annotations, skipping upload of that report, allowing the
  other non-empty report to upload, skipping reporter download when both reports
  are empty, and passing explicit report paths into `upload_reports()`.
- Extended the hermetic self-test to 97 assertions covering empty Go,
  empty frontend, and both-empty combinations.

### 2026-06-05 - Review round 9

- Final reviewers reran the full SOW-0044 scope after the empty-report and
  explicit-parameter cleanup. Reviewers found no code or security blockers and
  verified the job-level PR secret boundary, script-level PR skip, empty-report
  behavior, explicit `upload_reports()` parameters, and CodeQL config wiring.
- One reviewer found the PMD/SQLint removal evidence too thin for the SOW
  acceptance criteria. Fixed by recording the concrete PMD and SQLint probe
  evidence above and in the sanitized machine-readable baseline.
- One reviewer suggested documenting why the `codacy-coverage` job uses
  `always()` in its job-level `if:` condition. Fixed with an inline workflow
  comment.

## Outcome

Completed. SOW-0044 adds an explicit CodeQL `security-extended` policy, a tuned
local Codacy configuration, a non-required Codacy coverage reporting job with
secret-safe PR boundaries, hermetic upload-script self-tests wired into local
and CI gates, updated specs/skills/docs, and a measured Codacy baseline.
Codacy remains reporting-only until SOW-0046 triages the remaining
critical/high security and complexity findings.
