# Repository CI Setup (one-time / maintainer)

This document records the **one-time GitHub setup** that wires the CI workflows
into branch protection. It is for the assistant/maintainer operating the repo,
not for end users (end users want [runbook.md](runbook.md)).

The day-to-day quality contract lives in
[`.agents/sow/specs/quality-gates.md`](../.agents/sow/specs/quality-gates.md);
this file only covers the GitHub-side plumbing that cannot be expressed in a
committed workflow file.

## CI workflows (committed, no setup needed)

These run automatically once merged to `master`:

| Workflow | File | What it gates |
| --- | --- | --- |
| `ci` | `.github/workflows/ci.yml` | lint, test, frontend, embed-smoke, gates |
| `codeql` | `.github/workflows/codeql.yml` | Go + JS/TS + Actions static security |
| nightly | nightly workflow files | scheduled exploration / CVE refresh |

Dependency freshness is automated by `.github/dependabot.yml` (Go modules, npm,
GitHub Actions; weekly; minor/patch grouped).

CodeQL uses the built-in `security-extended` query suite in addition to the
default queries. Suppressions must be scoped in `.github/codeql/codeql-config.yml`
and backed by a tracking SOW/issue; inline suppressions without tracked rationale
are not allowed.
The current policy file contains only the `security-extended` query-suite
selection and no suppressions.

## Codacy coverage reporting

The non-required `codacy-coverage` job uploads existing coverage reports to
Codacy when either GitHub secret `CODACY_PROJECT_TOKEN` or `CODACY_API_TOKEN` is
configured. The required `test` and `frontend` jobs only generate and upload
artifacts:

- Go: `coverage.out`, uploaded with Codacy's Go parser.
- Frontend: `frontend/coverage/lcov.info`, normalized from frontend-local
  `src/...` paths to repository-root `frontend/src/...` paths and uploaded with
  Codacy's LCOV parser.

`CODACY_PROJECT_TOKEN` uses Codacy repository-token mode. `CODACY_API_TOKEN`
uses account-token mode with repository metadata only:
`CODACY_ORGANIZATION_PROVIDER=gh`, `CODACY_USERNAME=netdata`, and
`CODACY_PROJECT_NAME=ai-viewer`. If both token secrets exist, repository-token
mode wins and account-token variables are unset before the reporter runs. If
neither token is present, CI logs a skip message and continues. The whole
`codacy-coverage` job is skipped on `pull_request` events before checkout,
artifact download, secret injection, or repository scripts can run, so PR
coverage upload is intentionally disabled until a future SOW designs a safe
path.

While Codacy is reporting-only, missing artifacts, missing or empty coverage
files, reporter download failures, invalid bootstrap files, and Codacy upload
failures emit GitHub annotations and exit successfully so they do not fail the
PR. The job uploads each present non-empty Go/frontend coverage report as a
partial report. A missing or empty report is annotated but does not block
uploading the other report. If at least one partial upload is attempted, the job
sends Codacy's required `final` notification after the partial attempts even if
one partial command fails. A future SOW must explicitly promote Codacy to branch
protection before these become merge blockers.

The workflow downloads Codacy's recommended coverage reporter bootstrap script
with `curl -fsSL --retry` into a temporary file before execution, so download or
HTTP errors do not produce an empty no-op script. The workflow also verifies the
bootstrap file is a non-empty shell script and passes `bash -n` before
execution. The Codacy bootstrap script is still a remote execution surface;
Codacy documents it as the recommended path and documents that it validates the
downloaded reporter binary checksum. That checksum validation is Codacy's
upstream behavior, not a local guarantee added by this workflow.

Codacy is intentionally not a required branch-protection status yet. SOW-0044
landed it as a visibility layer; SOW-0046 tracks the critical/high and
complexity triage required before promoting Codacy to a merge blocker.

### Codacy quality/security triage

The importable Codacy tool/pattern configuration lives in
`.codacy/codacy.config.json`. Codacy Cloud path exclusions live in
`.codacy.yml`, which is the Codacy-documented path policy file. The local
Codacy Analysis CLI does not consume `.codacy.yml`, so root YAML
`exclude_paths` are mirrored in the JSON top-level `exclude` list. Those root
exclusions are limited to non-runtime SOW work-ledger files, duplicate
instruction symlinks, generated artifacts, dependencies, coverage/build output,
and local test output. Tool-scoped YAML exclusions are mirrored only into the
same tool's JSON `exclude` array. Frontend tests/test support and standalone
frontend scripts have different replacement gates: tests/test support are
covered by native frontend test/static gates, while standalone scripts rely on
their dedicated self-tests/build integration plus repository-wide
secrets/spec-drift checks.
After editing either file, run the hermetic config guard before importing
anything into Codacy Cloud:

```bash
scripts/test/codacy-config-test.sh
```

The guard validates JSON/YAML shape, keeps repository-wide non-runtime
work-ledger, duplicate symlink, generated/local artifact exclusions separate
from tool-scoped test/tooling exclusions, protects runtime source paths from
accidental broad exclusions, keeps high-signal security patterns enabled, and
requires documented rationale for local-only Cloud-noise removals.

Codacy Cloud does not automatically read the committed `.codacy/` directory.
After local gates and review pass, import the tuned configuration, trigger
Cloud reanalysis, and compare the before/after summaries:

```bash
codacy tools gh netdata ai-viewer --import -y
codacy repository gh netdata ai-viewer --reanalyze
codacy issues gh netdata ai-viewer --overview --output json
codacy findings gh netdata ai-viewer --severities Critical,High --output json
```

While Codacy is reporting-only, this import/reanalysis flow is an operational
visibility step, not a branch-protection gate. A future SOW must explicitly
promote Codacy quality/security checks to required status after the
critical/high backlog is fixed or explicitly triaged.

## Local gate prerequisites

`./scripts/gates.sh` runs the frontend Playwright E2E/axe gate. On a fresh
workstation, install the browser runtime once after `npm ci`:

```bash
cd frontend
npx playwright install --with-deps chromium
```

On distributions where `--with-deps` needs elevated package-manager access, run
the equivalent OS dependency install manually, then run
`npx playwright install chromium`. The gate does not install browsers on demand;
missing browsers are a workstation setup issue, not a passing quality state.

## One-time: register required status checks on `master`

Branch protection's required-checks list is keyed by **job name** and is NOT a
committed file — it lives in GitHub's API state. After `ci.yml` + `codeql.yml`
are green on `master` at least once (so the check names exist), register them as
required.

The canonical list of names is in
[`.github/workflows-checks.yaml`](../.github/workflows-checks.yaml). The
invocation (run once, by a maintainer with admin on the repo):

```bash
gh api -X PUT /repos/netdata/ai-viewer/branches/master/protection \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": [
      "lint",
      "test",
      "frontend",
      "embed-smoke",
      "gates",
      "CodeQL (go)",
      "CodeQL (javascript-typescript)",
      "CodeQL (actions)"
    ]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
JSON
```

This matches the canonical branch protection in `AGENTS.md`
(§"Branch Protection and Merge Workflow"): `enforce_admins: true`,
`allow_force_pushes: false`, `allow_deletions: false`,
`required_pull_request_reviews: null` (no manual-approval gate — external LLM
reviewers + the discipline checklist are the quality gate, see the
`project-second-opinions` skill).

### Token scope

The PUT requires admin on the repo. The default `GITHUB_TOKEN` inside Actions
does **not** carry `Administration: write`, so this is run from a maintainer's
`gh` session (which has it via the logged-in account) — NOT from a workflow. If
running from automation, use a fine-grained PAT with `Administration: write` on
`netdata/ai-viewer`.

### Verify

```bash
gh api /repos/netdata/ai-viewer/branches/master/protection \
  --jq '.required_status_checks.contexts'
# expect: ["lint","test","frontend","embed-smoke","gates","CodeQL (go)","CodeQL (javascript-typescript)","CodeQL (actions)"]
```

A test PR that intentionally fails one check (e.g. a lint violation) must then
be **blocked from merge** — that is the proof the registration took effect.

## Renaming or adding a CI job later

Renaming a job silently disables its required check (protection keys by name).
So any change that renames a CI job MUST, in the **same commit**:

1. rename the job in the workflow file,
2. update [`.github/workflows-checks.yaml`](../.github/workflows-checks.yaml), and
3. re-run the `gh api -X PUT …` above with the updated `contexts`.

Adding a new gate job follows the same path (add it to `ci.yml`, to
`workflows-checks.yaml`, and to the `contexts` array). See
`quality-gates.md` §"Renaming a CI Job" and the `project-quality-gates` skill.
