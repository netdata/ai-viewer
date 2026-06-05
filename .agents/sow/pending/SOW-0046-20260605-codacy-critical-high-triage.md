# SOW-0046 - Codacy Critical/High Findings Triage

## Status

Status: open

Sub-state: filed by SOW-0044 validation. Not blocking SOW-0044 because the
scanner layer is being installed as reporting/tuning first; these findings must
be triaged before Codacy can become a required merge gate.

## Requirements

### Purpose

Turn Codacy's critical/high security and maintainability findings into a
high-signal, actionable baseline so ai-viewer can use scanner output without
drowning maintainers in false positives.

### User Request

Follow-up from the requested CodeQL + Codacy defence layer: use code scanning to
know whether the project is keeping complexity low, increasing maintainability,
and checking security.

### Assistant Understanding

Facts:

- SOW-0044 imported the current Codacy Cloud configuration and ran local Codacy
  analysis against available local tools.
- The final SOW-0044 tuned local available-tool run reported 984 existing
  issues: 4 Error, 95 High, 705 Warning, and 180 Info.
- The SOW-0044 tuned local config has 9 tools and 625 enabled patterns after
  pruning imported Semgrep patterns for absent source languages and removing
  PMD/SQLint from the committed local Analysis CLI config.
- The measured categories were 99 Security, 605 Complexity, 120 BestPractice,
  114 CodeStyle, 43 Comprehensibility, and 3 ErrorProne.
- The largest issue buckets are Lizard complexity, markdownlint style, agent
  instruction linting, and ESLint security rules.
- The high/critical security bucket includes Semgrep and ESLint findings across
  workflow configuration, frontend scripts, production frontend code, and tests.
- Several findings appear likely to be false positives or out-of-scope for
  runtime risk, but that is a working theory until code/context triage proves it.

Inferences:

- Codacy should remain reporting-only until critical/high findings are fixed,
  proven false-positive with evidence, or converted into scoped suppressions.
- Complexity findings need a separate maintainability judgment: some tests and
  renderers may be intentionally dense, but production paths should be kept
  small and readable.

Unknowns:

- Which high/critical security findings are true runtime vulnerabilities versus
  scanner false positives.
- Whether Cloud-side issue counts match local issue counts after SOW-0044
  exclusions are imported.

### Acceptance Criteria

1. Every Codacy Error/High security finding present after SOW-0044 is either
   fixed, proven false-positive with file/line evidence, or tracked in a more
   specific SOW with owner/context.
2. No broad pattern disablement is used to hide true runtime risk.
3. If a pattern is noisy, suppression is path- and tool-scoped with rationale
   recorded in the SOW and Codacy configuration.
4. Complexity findings are grouped by production code, tests, scripts, and docs;
   production-code complexity above threshold is either reduced or explicitly
   justified.
5. Local Codacy analysis and Codacy Cloud summaries show a materially smaller
   critical/high backlog, and the remaining backlog is documented.
6. Specs, skills, and docs are updated if scanner policy changes.
7. Full local gates and external review converge before closing.

## Analysis

Sources checked:

- SOW-0044 tuned local Codacy aggregate, generated 2026-06-05 with:
  `codacy-analysis analyze . --tool Trivy --tool jackson --tool Semgrep --tool
  shellcheck --tool Agentlinter --tool Lizard --tool Stylelint --tool
  markdownlint --tool ESLint8 --install-dependencies --parallel-tools 4
  --output-format json`.
- `.codacy/codacy.config.json`
- `.agents/sow/specs/security.md`
- `.agents/sow/specs/quality-gates.md`

Current state:

- Codacy is useful as visibility, but too noisy to require in branch protection.
- Local validation already proves the issue set is too large for opportunistic
  cleanup inside SOW-0044 without changing that SOW's purpose.
- SOW-0044 review identified retained framework-specific Semgrep patterns for
  frameworks not currently present in the repository (for example Kubernetes,
  Docker Compose, Argo, AWS CDK, Angular, Express, Deno, Visualforce, PL/SQL,
  Sequelize, Passport, and Intercom). SOW-0046 must decide pattern-by-pattern
  whether to prune them or keep them as forward-looking security coverage with
  measured evidence.

Durable SOW-0044 baseline:

| Scope | Issues | Error | High | Warning | Info |
|---|---:|---:|---:|---:|---:|
| Local available-tool run before exclusions | 1,273 | 4 | 95 | 729 | 445 |
| Tuned local available-tool run after exclusions | 984 | 4 | 95 | 705 | 180 |

| Config state | Enabled tools | Enabled patterns |
|---|---:|---:|
| Imported Cloud config | 11 | 1,243 |
| Tuned local config after exclusions, language pruning, and PMD/SQLint removal | 9 | 625 |

| Tuned category | Count |
|---|---:|
| Security | 99 |
| Complexity | 605 |
| BestPractice | 120 |
| CodeStyle | 114 |
| Comprehensibility | 43 |
| ErrorProne | 3 |

Risks:

- Treating false positives as equal to real vulnerabilities wastes engineering
  time.
- Suppressing patterns too broadly can hide future real vulnerabilities.
- Fixing complexity mechanically can reduce readability if refactors are not
  driven by actual ownership boundaries.

## Pre-Implementation Gate

Status: not started (pending prioritization).

## Plan

To be filled when activated.

## Validation

Pending.

## Reviews

Pending.

## Outcome

Pending.
