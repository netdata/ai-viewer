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
|---|---|---|
| `ci` | `.github/workflows/ci.yml` | lint, test, frontend, embed-smoke, gates |
| `codeql` | `.github/workflows/codeql.yml` | Go + JS/TS + Actions static security analysis |
| nightly | `fuzz-nightly.yml`, `race-stress-nightly.yml`, `govulncheck-nightly.yml` | scheduled exploration / CVE refresh (not merge gates) |

Dependency freshness is automated by `.github/dependabot.yml` (Go modules, npm,
GitHub Actions; weekly; minor/patch grouped).

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
