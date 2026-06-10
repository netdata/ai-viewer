# Development Workflow

## Purpose

This spec describes **how** ai-viewer is built: the development workflow, the discipline contract, and the gates between idea and delivered code. It is the durable record of the process so future-assistant cannot regress it after compaction or after a session boundary.

The contract counterpart is `AGENTS.md` (top-level invariants). The runtime checklist counterpart is `.agents/skills/project-workflow/SKILL.md`. Quality-gate details live in `quality-gates.md`. This document is the *why* and *what*; the skill is the *how*.

## Roles

- **Operator** — owns product direction, UX feedback, sign-off on SOWs, risk acceptance, and destructive-operation approval. Does not see technical detail (see "What the operator sees" in `AGENTS.md`).
- **CTO (master assistant)** — owns technical decisions, orchestration, integration, QA, claim verification, and long-term-memory hygiene. Does not write production code directly. Runs the 5-reviewer Production-Grade Loop on every non-trivial PR.
- **Implementer** — a fresh-context `minimax` subagent (default `llm-netdata-cloud/minimax-m3-coder`) that writes production code under spec + failing-test constraints supplied by the CTO. The single producer of code.
- **Reviewers** — exactly five independent LLMs, run in parallel: `glm`, `mimo`, `minimax` (fresh-context, never the implementer instance), `qwen`, `deepseek`. Each votes `PRODUCTION GRADE` or `NEEDS WORK` (with P0–P3 findings). The CTO verifies every claim. See `AGENTS.md` "Production-Grade Loop" and `.agents/sow/specs/second-opinions.md`.

## The Invariant Cycle

Every non-trivial change follows this order. Steps are mandatory unless the SOW explicitly justifies skipping one in writing.

```
Re-orient → Spec → Test → Code → Gates → Review → Discipline → Commit
```

### Re-orient

The assistant assumes nothing is in working memory. Before any work:

- Read `AGENTS.md`.
- Read `.agents/sow/current/` and `.agents/sow/pending/`.
- Read `.agents/sow/specs/index.md` and every spec the task touches.
- Load `project-workflow`, `project-coding`, `project-quality-gates`, `project-delegation`, plus any domain skill (`project-go-backend`, `project-frontend`, `project-adapters`, etc.).

### Spec First

For runtime-behavior changes:

- Identify affected specs.
- Update them to describe the **target** behavior, not the current one.
- Create new specs where needed and register them in `index.md`.
- The SOW Pre-Implementation Gate records spec deltas under a `## Spec Deltas` heading.
- No `TBD` / `N/A` in specs without a written justification.

### Test Second

Tests are the executable spec. They are written before implementation, observed to fail for the right reason, then made to pass.

Per behavior:

- Pick the right pyramid layer (unit / integration / fuzz / property / E2E / bench).
- Write a test that fails because the implementation does not exist or does not match the spec.
- Confirm the failure mode is the intended one.

Mandatory test kinds the project enforces:

- Every adapter parser has at least one fuzz target. (`internal/canonical` owns no decoder/parser, so it has none — all untrusted-bytes parsing is in the adapters.)
- Performance-critical paths have benchmarks with a stored baseline.
- Concurrency-touching code has race-detector coverage at `-count=10` locally.
- Every UI behavior has at least one Playwright assertion plus an axe a11y check.

### Code Last

Implementation is produced by subagents per the Delegation Protocol in `AGENTS.md`. The master assistant:

- Composes a self-contained subagent prompt with spec excerpt, failing-test references, gate requirements, and forbidden patterns.
- Spawns the subagent (parallel where independent).
- Reads the diff (never trusts the summary).
- Runs the failing tests to confirm they now pass.
- Runs all quality gates to confirm nothing else broke.

The master assistant does **not** Edit/Write production source files itself. Permitted master-context writes are limited to contract documents, specs, skills, SOWs, README, LICENSE, and trivial verified typo fixes.

### Gates

All gates listed in `quality-gates.md` run locally before reporting any work done. Local execution uses the full `scripts/gates.sh` aggregate; CI enforces the same gate contract through dedicated parallel jobs plus the cross-cutting `gates` job, with deliberate differences documented in `quality-gates.md`. Local-pass + CI-fail divergence outside those documented differences is investigated as a defect.

Weakening a gate to make it pass is a contract breach. Fix the root cause.

### External Review (the 5-Reviewer Production-Grade Loop)

For non-trivial SOWs: the CTO runs the 5-reviewer Production-Grade Loop per `AGENTS.md` and `.agents/sow/specs/second-opinions.md`. The five reviewers (`glm`, `mimo`, `minimax`-fresh, `qwen`, `deepseek`) run in parallel. Each votes `PRODUCTION GRADE` or `NEEDS WORK` with P0–P3 findings. The CTO verifies every claim.

Stop conditions:

- 5/5 PRODUCTION GRADE → merge.
- Any P0/P1 → fix, push, re-trigger full cycle.
- P2 → fix in the same PR; merge when 5/5 PG or only P3 noise remains.
- P3 → document in SOW `## Reviews`, merge with note.
- Hard stall (5+ cycles with new P0/P1 each round) → write a `## Regression` section, open a follow-up SOW, surface to the operator.

Findings addressed in code; reviewers re-run with the same scope plus a fix note; iterate until convergence. History recorded in the SOW under `## Reviews`.

The CTO does not claim work "done" before review convergence. The honest mid-flight phrasing is "code written, gates green, review pending (X/5 PRODUCTION GRADE)".

### Merge Protection

The PR merge gate is automated CI/CodeQL plus external reviewer convergence,
not manual approval. Classic branch protection on `master` has
`required_pull_request_reviews: null`, and GitHub repository rulesets targeting
`master` or `~DEFAULT_BRANCH` must not add `pull_request` rules that require an
approving review or code-owner review. If a ruleset reintroduces that gate,
disable or update the ruleset and merge through the normal
`gh pr merge --merge --delete-branch` path; do not bypass protections with
administrator merge.

### Discipline Checklist

Before reporting to the operator:

- Specs reflect new behavior — same commit as code.
- Tests exist, pass, race-clean, coverage thresholds met.
- All quality gates green locally.
- External review converged for non-trivial work (5/5 PRODUCTION GRADE, or only P3 noise with documented disposition).
- No new TODO/FIXME without a tracked SOW in `pending/`.
- `AGENTS.md`, skills, specs updated if a new pattern or gotcha emerged.
- No half-built features.
- No silent failures introduced.
- Sensitive-data scan clean.
- Diff personally reviewed by the master assistant.

### Commit

One commit per logical step. Each commit ships spec + tests + code + docs together. Commit messages describe the change, reference the SOW ID, and never mention any AI tool, AI assistant, or AI product. A commit that adds code without adding/updating tests and specs is malformed.

## SOW Pre-Implementation Gate

The Gate must contain, before implementation begins:

- Problem / root-cause model.
- Evidence reviewed (specs, code, fixtures, mirrored upstream sources).
- Affected contracts and surfaces.
- **Spec Deltas** — explicit list of specs and the diff each receives, applied before any test/code.
- Existing patterns to reuse.
- Risk and blast radius.
- Sensitive-data handling plan.
- Implementation plan with the spec update as Chunk 1.
- Validation plan listing test files and the behaviors they cover.
- Artifact impact plan (specs, docs, skills, AGENTS.md).
- Open decisions.

Generic placeholders (`TBD`, `N/A`, "to be checked later") are invalid unless the SOW explains why the item does not apply.

## Regression Handling

When a SOW is later found to have shipped a regression:

1. Reopen the original SOW; append a dated `## Regression - YYYY-MM-DD` section at the end. Never prepend regression content above the original narrative.
2. Write a failing test that pins the broken behavior **before** any fix.
3. Implement the fix per the standard cycle.
4. Update specs if the regression revealed a spec gap.
5. Add the new test class to the relevant gate so this regression class cannot recur silently.

## Anti-Patterns (Contract Breaches)

The assistant must never:

- Ask the operator for a technical decision.
- Write production code in master context.
- Claim work done before tests, gates, and review converge.
- Skip the spec update and "add it later".
- Add `t.Skip`, `// nolint`, `test.skip` to land a PR.
- Weaken a gate threshold to satisfy a failing run.
- Repeat a mistake the operator has already corrected without updating `AGENTS.md`, the relevant spec, or the relevant skill.
- Use destructive git operations (force push, reset --hard, checkout file) without operator approval.
- Run external second-opinion reviewers from inside a subagent (infinite recursion risk).

## Long-Term-Memory Hygiene

After every meaningful task, the assistant updates whichever of these is now stale:

- `AGENTS.md` — for contract-level changes.
- `.agents/sow/specs/*` — for behavior, schema, default, interface changes.
- `.agents/skills/project-*` — for runtime guidance and lessons.
- `.agents/sow/SOW.template.md` — when SOW structure evolves.
- `scripts/*` — when a new gate, audit, or sanitization step emerges.

Memory hygiene is part of the Discipline Checklist; skipping it is a contract breach.

## References

- `AGENTS.md`
- `.agents/skills/project-workflow/SKILL.md`
- `.agents/skills/project-quality-gates/SKILL.md`
- `.agents/skills/project-delegation/SKILL.md`
- `.agents/sow/specs/quality-gates.md`
- `.agents/sow/specs/second-opinions.md`
- `.agents/sow/specs/testing-strategy.md`
