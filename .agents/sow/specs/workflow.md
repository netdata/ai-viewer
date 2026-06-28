# Development Workflow

## Purpose

This spec describes **how** ai-viewer is built: the development workflow, the discipline contract, and the gates between idea and delivered code. It is the durable record of the process so future-assistant cannot regress it after compaction or after a session boundary.

The contract counterpart is `AGENTS.md` (top-level invariants). The runtime checklist counterpart is `.agents/skills/project-workflow/SKILL.md`. Quality-gate details live in `quality-gates.md`. This document is the *why* and *what*; the skill is the *how*.

## Roles

- **Operator** — owns product direction, UX feedback, sign-off on SOWs, risk acceptance, and destructive-operation approval. Does not see technical detail (see "What the operator sees" in `AGENTS.md`).
- **CTO (master assistant)** — owns technical decisions, orchestration, implementation, tests, integration, QA, claim verification, and long-term-memory hygiene.
- **Helper subagents** — optional bounded read-only investigators or summarizers. They do not own implementation, tests, commits, or review gates.
- **External reviewers** — six independent reviewers, run in parallel for meaningful gates: `glm`, `minimax`, `kimi`, `mimo`, `deepseek`, and `qwen`. They vote on gap analysis, implementation plan, or implementation review. The CTO verifies every claim. See `AGENTS.md` "Three Reviewer Gates" and `.agents/sow/specs/second-opinions.md`.

## The Invariant Cycle

Every non-trivial change follows this order. Steps are mandatory unless the SOW explicitly justifies skipping one in writing.

```
Re-orient → Gap → Gap Review → Plan → Plan Review → Spec → Test → Code → Gates → Implementation Review → Discipline → Commit
```

### Re-orient

The assistant assumes nothing is in working memory. Before any work:

- Read `AGENTS.md`.
- Read `.agents/sow/current/` and `.agents/sow/pending/`.
- Read `.agents/sow/specs/index.md` and every spec the task touches.
- Load `project-workflow`, `project-coding`, `project-quality-gates`, plus any domain skill (`project-go-backend`, `project-frontend`, `project-adapters`, etc.). Load `project-delegation` only when using helper subagents.

### Gap Analysis

For meaningful goals/SOWs, the CTO first derives what must be true for the goal
to be satisfied: required behavior, known gaps, risks, edge cases, tests, gates,
spec/doc changes, migration or repair paths, and evidence still needed.

The gap analysis is reviewed through the external reviewer gate when the chunk
is meaningful. The positive vote is `NOTHING MORE CAN BE DONE`.

### Implementation Plan

After the gap analysis is accepted, the CTO writes a concrete implementation
plan: files, specs, tests, code slices, gates, rollout/installation steps, risk
controls, and sequencing.

The plan is reviewed through the external reviewer gate when the chunk is
meaningful. The positive vote is `READY FOR IMPLEMENTATION`.

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

Implementation is written directly by the CTO after specs and failing tests. The CTO:

- Keeps changes scoped to the accepted goal/gap/plan.
- Makes the failing tests pass without weakening them.
- Reads the full diff.
- Runs the failing tests to confirm they now pass.
- Runs all quality gates to confirm nothing else broke.

Helper subagents may support investigation, but they do not own implementation.

### Gates

All gates listed in `quality-gates.md` run locally before reporting any work done. Local execution uses the full `scripts/gates.sh` aggregate; CI enforces the same gate contract through dedicated parallel jobs plus the cross-cutting `gates` job, with deliberate differences documented in `quality-gates.md`. Local-pass + CI-fail divergence outside those documented differences is investigated as a defect.

Weakening a gate to make it pass is a contract breach. Fix the root cause.

### External Review Gates

External reviewers run on meaningful chunks, at least per SOW and per
substantial milestone for complex SOWs. The reviewers (`glm`, `minimax`, `kimi`,
`mimo`, `deepseek`, `qwen`) run in parallel. The three gate votes are:

- Gap analysis: `NOTHING MORE CAN BE DONE` or `NEEDS WORK`.
- Implementation plan: `READY FOR IMPLEMENTATION` or `NEEDS WORK`.
- Implementation review: `PRODUCTION GRADE` or `NEEDS WORK`.

Before every external reviewer run, the CTO loads
`.agents/skills/project-second-opinions/SKILL.md`, completes its
reviewer-readiness checklist, and records the evidence in the SOW or work
ledger. Reviewers verify completed stage artifacts. They are not the way the CTO
discovers requirements, code paths, contracts, tests, migrations, or operational
risks.

Stop conditions:

- P0/P1/P2 findings → verify, generalize to a class of possible misses, perform
  and record a local class sweep, then fix or reject with evidence before any
  re-run of the same gate.
- P3 findings → fix or document; P3-only comments do not reopen a converged
  gate unless verification shows a real P0/P1/P2 class.
- Positive votes from all available reviewers, or every non-positive vote verified as false-positive/noise → advance to the next stage.
- Round budget: one round is normal; a second is allowed after a class sweep; a
  third is exceptional and requires a short SOW waste analysis plus full local
  review first; a fourth blocker round is forbidden without an
  operator-visible status report and a changed approach.
- Repeated hard stall → stop buying six-reviewer rounds, record the waste
  analysis in the SOW, and surface a business-level recommendation.

Technical reviewer failures are handled pragmatically:

- If successful reviewers already found accepted or not-yet-disproven P0/P1/P2
  findings, do not retry failed reviewers in that round. Fix or reject the
  blocking findings first, then rerun the whole reviewer batch, including the
  reviewers that failed.
- If successful reviewers found nothing or only P3 findings, retry each failed
  reviewer once when the missing vote matters.
- If the retry also fails, record the technical failure, ignore that reviewer for
  the current gate, and try it again on a later task or later gate.

Findings are addressed in the relevant artifact for that gate: gap analysis,
plan, specs, tests, code, docs, or gates. Reviewers re-run with the same scope
plus a fix note only after readiness remains true and any blocker class sweep is
recorded. History is recorded in the SOW under `## Reviews`.

The CTO does not claim work "done" before the applicable gate converges.

### Commit / Merge Protection

During the active Development phase, work goes directly to `master`. The CTO
runs the applicable reviewer gates before closing meaningful work, commits only
specific in-scope files, pushes `master`, then reads CI/Codacy/CodeQL/cubic
output and addresses real findings.

When GA or explicit-PR flow is active, the PR merge gate is automated CI/CodeQL
plus external reviewer convergence, not manual approval. Classic branch
protection on `master` has `required_pull_request_reviews: null`, and GitHub
repository rulesets targeting `master` or `~DEFAULT_BRANCH` must not add
`pull_request` rules that require an approving review or code-owner review. If a
ruleset reintroduces that gate, disable or update the ruleset and merge through
the normal `gh pr merge --merge --delete-branch` path; do not bypass protections
with administrator merge.

### Discipline Checklist

Before reporting to the operator:

- Specs reflect new behavior — same commit as code.
- Tests exist, pass, race-clean, coverage thresholds met.
- All quality gates green locally.
- Applicable external reviewer gate converged; reviewer-readiness checklist
  recorded before the run; P0/P1/P2 findings fixed or rejected with evidence;
  blocker class sweeps recorded before reruns; only documented P3 findings
  remain.
- Reviewer waste controls followed: no reviewer use for discovery, no immediate
  patch-and-rerun after blockers, no P3-only rerun, no fourth blocker round
  without operator-visible status and a changed approach.
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
- Delegate implementation by default, or write code before the accepted specs,
  tests, and applicable reviewer gates are ready.
- Claim work done before tests, gates, and review converge.
- Use external reviewers as the discovery engine for requirements, code paths,
  contracts, tests, migrations, or operational risks.
- Treat repeated reviewer rounds as rigor. One round is six external model runs;
  repeated blocker rounds mean the CTO must stop, self-review, and change the
  approach.
- Patch only a cited blocker line and immediately rerun reviewers instead of
  performing the required class sweep.
- Rerun reviewers for P3-only comments unless verification shows a real
  P0/P1/P2 class.
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
