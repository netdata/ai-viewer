# SOW-0044 - Code Scanning Defence Layer (CodeQL + Codacy)

## Status

Status: open

Sub-state: filed as the follow-up requested after SOW-0013 completes. This SOW is not started; it depends on SOW-0013 landing the baseline CI/CodeQL infrastructure first.

## Requirements

### Purpose

Add a high-signal code-scanning defence layer that helps keep ai-viewer secure, low-complexity, maintainable, and honest about quality trends without drowning the project in noisy findings.

### User Request

After SOW-0013 finishes, add defences with code scanning using CodeQL and Codacy so the project can see whether it is doing a good job on complexity, maintainability, and security.

### Assistant Understanding

Facts:

- SOW-0013 adds a baseline `.github/workflows/codeql.yml` workflow and required CodeQL status checks.
- The project already enforces local/CI gates for Go lint/security, frontend lint/types/tests/E2E/a11y, coverage, fuzz seed corpus, benchmark regression, secrets, AI-attribution, and spec drift.
- Codacy setup needs project-specific tuning: broad defaults first, then noise reduction using measured findings.
- Coverage upload to Codacy requires repository presence in Codacy and a project/API token configured as a GitHub secret.

Inferences:

- CodeQL should not remain only a default workflow; it needs explicit query/security policy, required-check tracking, alert triage rules, and suppression rules with tracked rationale.
- Codacy should complement existing gates, not duplicate them noisily. Its best use here is maintainability and complexity trend visibility, security findings, duplication, and cloud-side quality reporting.
- Codacy coverage upload should reuse existing Go `coverage.out` and frontend `lcov` generation rather than introduce a second test path.

Unknowns:

- Whether `netdata/ai-viewer` is already added to Codacy Cloud.
- Which Codacy Cloud tools and organization-level coding standards are already enforced for the repository.
- Whether the required Codacy token is already available to GitHub Actions.

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

Status: not started (pending SOW-0013 completion).

## Plan

To be filled when this SOW starts.

## Execution Log

### 2026-06-04 - Filed

- Filed as the tracked follow-up for code-scanning defences after SOW-0013.
- Scope deliberately excludes SOW-0013's baseline CodeQL workflow so SOW-0013 can close cleanly first.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.
