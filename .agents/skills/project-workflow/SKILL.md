---
name: project-workflow
description: Master orchestration cycle for ai-viewer work — gap→plan→spec→test→code→gates→review→commit. The CTO writes implementation directly. Helper subagents are optional for bounded investigation, not the implementation norm. External reviewers are three milestone gates per meaningful feature, substantial batch, or SOW, not routine delegation.
---

# Workflow

## Purpose

This skill is the assistant's runtime checklist. The contract lives in `AGENTS.md` (the "Three Reviewer Gates" section is the operating model); the durable spec lives in `.agents/sow/specs/workflow.md`; this file is the operational pattern the assistant follows every time. Load it at the start of every meaningful task. If the assistant ever notices it has started writing code without consulting this skill, stop and restart from the top.

## Roles

| Role | Model | Job |
|---|---|---|
| **CTO (master assistant)** | me | Research, decide, update specs, write tests, implement code, run gates, verify, commit, and report. |
| **Helper subagents** | optional | Bounded read-only investigation or summarization when it saves context/time. They are not the default implementation path. |
| **External reviewers** | `glm`, `minimax`, `kimi`, `mimo`, `deepseek`, `qwen` | Milestone quality gates for gap analysis, implementation plan, and implementation on meaningful chunks. They do not design or implement the work. |

The CTO owns the work. External reviewers are acceptance gates, not the normal
work engine. Do not run reviewers during ordinary exploration or before the CTO
has personally completed the current stage artifact.

## The Cycle (Invariant Order)

```
1. Re-orient       →  read SOWs, specs, skills
2. Gap             →  analyze what must be true for the goal to be satisfied
3. Gap Review      →  reviewers vote NOTHING MORE CAN BE DONE
4. Plan            →  write the implementation plan
5. Plan Review     →  reviewers vote READY FOR IMPLEMENTATION
6. Spec            →  update or create specs FIRST
7. Test            →  write failing tests against the new spec
8. Code            →  CTO implements directly; make tests pass
9. Gates           →  run every automated quality gate locally
10. Impl Review    →  reviewers vote PRODUCTION GRADE
11. Discipline     →  run the Discipline Checklist
12. Commit         →  one commit per logical step; spec + tests + code + docs together
```

Skipping a step is a contract breach unless the SOW explicitly justifies the skip in writing.

## Step 1 — Re-orient (Compaction Protection)

Before any work — even what feels like a "quick" change — read in parallel:

- `AGENTS.md` (contract)
- `.agents/sow/current/` (active SOWs)
- `.agents/sow/pending/` (queued SOWs that might overlap)
- `.agents/sow/specs/index.md` and any spec the task touches
- `.agents/skills/project-workflow/SKILL.md` (this file)
- `.agents/skills/project-coding/SKILL.md`
- `.agents/skills/project-quality-gates/SKILL.md`
- `.agents/skills/project-delegation/SKILL.md` only when using helper subagents
- any other `project-*` skill matching the task domain

The assistant has likely just compacted or just started; assume nothing is in working memory.

## Step 2 — Gap Analysis

For meaningful goals/SOWs, first write down what must be true for the goal to be
satisfied. Include missing behavior, existing evidence, risks, edge cases,
affected contracts, tests, gates, docs/spec changes, migration or repair paths,
and unknowns still requiring evidence.

The CTO writes this analysis. Reviewers enrich and challenge it; they do not
replace it.

## Step 3 — Gap Review Gate

Run the gap analysis gate when the chunk is meaningful enough to require
external review. The positive vote is `NOTHING MORE CAN BE DONE`.

If reviewers find P0/P1/P2 gaps, fix the analysis or reject the finding with
evidence, then rerun the same gate with the same broad scope plus a short fix
note. Do not start planning until the gate converges.

## Step 4 — Implementation Plan

After the gap analysis is accepted, write the implementation plan: specs, tests,
files, code slices, gates, sequencing, risk controls, artifact updates, and
installation/rollout steps where relevant.

## Step 5 — Plan Review Gate

Run the plan review gate when the chunk is meaningful enough to require
external review. The positive vote is `READY FOR IMPLEMENTATION`.

If reviewers find P0/P1/P2 plan defects, fix the plan or reject the finding with
evidence, then rerun the same gate with the same broad scope plus a short fix
note. Do not write implementation code until the gate converges.

## Step 6 — Spec First

For any runtime-behavior change:

1. List specs that describe the affected behavior.
2. Update each spec to describe the **target** behavior, not the current behavior. The spec leads, the code follows.
3. If no spec covers the area, create one. Add it to `.agents/sow/specs/index.md` in the same commit.
4. Record the spec deltas in the SOW's Pre-Implementation Gate under a `Spec Deltas` heading.
5. Specs are not allowed to contain `TBD`, `N/A`, or "to be confirmed" unless the SOW explains why.

Trivial work (typo/format) skips spec updates. Anything else does not.

## Step 7 — Tests Second

Tests are the executable spec. Write them before implementation, watch them fail, then make them pass.

For each behavior in the updated spec:

1. Identify the test layer (unit / integration / E2E / fuzz / bench). See `project-testing` skill for the pyramid.
2. Write the failing test. Run it locally; confirm it fails for the right reason (not a typo, not a wiring bug — the actual behavior is missing).
3. For adapter parsers/decoders (untrusted-bytes parsing): include at least one fuzz target. (Canonical mapping has no parser to fuzz — its invariants are covered by `internal/canonical/property_test.go`.)
4. For performance-critical paths: include a benchmark with a baseline check.
5. For frontend behavior: include the component test AND the Playwright E2E that exercises it from the user's perspective.

A behavior without a failing test before the implementation lands is a defect.

## Step 8 — Code Directly

The CTO writes implementation directly after specs and failing tests exist. This
is the normal path. Use helper subagents only for bounded investigation or
summarization, not as implementation owners.

While implementing:

- Keep changes scoped to the active SOW/spec.
- Do not weaken tests to make code pass.
- Keep source readers read-only and fail closed on uncertainty.
- Apply `project-coding`, `project-testing`, and affected domain skills.
- Run focused tests frequently, then full gates before any claim of completion.

## Step 9 — Quality Gates

Run **every** gate from `project-quality-gates` skill locally. Not "the relevant ones" — every one. CI runs them all anyway; running them locally first saves a round-trip and surfaces flakiness before it taints history.

If any gate fails: fix root cause, do not weaken the gate. Lowering a threshold to make a gate pass is a contract breach.

## Step 10 — Implementation Review Gate

External reviewers are the implementation verification gate per completed
feature, substantial batch of work, or SOW. They are not the norm for every small
edit and they are not a substitute for the CTO doing implementation and
self-review first.

Run the gate when:

- The CTO believes the feature/batch/SOW is ready.
- Focused tests and relevant local gates are green.
- The diff has been read by the CTO.
- The review scope is meaningful, not a few incidental lines.

For high-risk work such as SOW-0097, run all six reviewers in parallel:
`glm`, `minimax`, `kimi`, `mimo`, `deepseek`, and `qwen`. Use the same broad
scope on follow-up rounds, adding only short notes about fixes already made.
The positive vote is `PRODUCTION GRADE`. Every reviewer claim is a claim, not
proof; verify file:line, repro, and spec before acting.

If a reviewer fails technically, follow `project-second-opinions`: do not retry
failed reviewers while accepted P0/P1/P2 findings already exist; fix or reject
those findings first, then rerun the whole batch. Retry a failed reviewer once
only when all successful reviewers are positive or P3-only and the missing vote
matters for closing the gate.

## Step 11 — Discipline Checklist

Before reporting completion to the operator:

- [ ] Specs reflect new behavior — same commit as code.
- [ ] Tests exist, pass, race-clean, coverage thresholds met.
- [ ] All quality gates green locally.
- [ ] Applicable external reviewer gate converged for the current stage; P0/P1/P2 findings fixed or rejected with evidence; only documented P3 findings remain.
- [ ] No new TODO/FIXME without a tracked SOW in `pending/`.
- [ ] `AGENTS.md`, skills, specs updated if a new pattern or gotcha emerged.
- [ ] No half-built features.
- [ ] No silent failures.
- [ ] Sensitive data scan clean.
- [ ] Diff reviewed by master assistant (not just subagent summary).

A "no" anywhere is a defect. Fix before reporting.

## Step 12 — Commit, Push, and Merge

One commit per logical step. Each commit ships spec + tests + code + docs together. Commit messages:

- Describe the change, not the commenter or AI tool.
- Reference the SOW ID.
- Never mention the assistant, vendor, or AI product.
- Use a HEREDOC for the body to preserve formatting.

Current Development-phase flow:

1. Work directly on `master` unless the operator explicitly asks for a branch/PR.
2. Run the applicable reviewer gate before closing meaningful work. For the implementation gate, converge on `PRODUCTION GRADE` before commit, push, or install.
3. Commit only the specific files in scope.
4. Push `master`.
5. Read CI/Codacy/CodeQL/cubic output and address real findings.

GA/explicit-PR flow:

1. Work on a feature branch (`git checkout -b <slug>`).
2. Commit with the discipline above.
3. Push the branch (`git push -u origin <branch>`).
4. Open a PR (`gh pr create --base master --head <branch>`).
5. Run the implementation review gate when the feature/batch/SOW is ready.
6. After convergence (`PRODUCTION GRADE` or only documented P3 noise), **merge yourself**: `gh pr merge <num> --merge --delete-branch`.
7. `git checkout master && git pull` so the local master tracks.

No operator approval step. The operator gates SOWs, not PRs.

After push/merge:

- Run gates again on the committed state (paranoia).
- If the SOW step is complete, update the SOW's `## Implementation` section with the commit ref and evidence.
- If the SOW is complete, move from `current/` to `done/` with `Status: completed` in the same commit (next PR).

## Reporting to the Operator

Compact, honest, business-outcome-only. The operator does not see file paths, design rationale, test names, or gate command output — those live in the SOW `## Reviews` and the PR description. See `AGENTS.md` "What the operator sees" for the full contract.

- TL;DR (2-3 sentences, business outcomes only).
- SOW id and one-line description.
- Commit/PR link + state (pushed / open / merged / blocked).
- Reviewer verdicts for the current gate.
- Gate status (green / red).
- Blocker (if any), with the question or decision needed.
- Next: what's queued, what's blocked, what needs operator input.

Never report code as "working" or "ready" if any step above is incomplete. The honest phrasings are:

- "Code written, tests passing, gates green, review pending."
- "Code written, tests passing, gates green, review converged — ready for the operator."
- "Code written, tests not yet covering X — not ready."

## When to Pause and Surface

Pause and ask the operator only when:

- A genuine product or business tradeoff exists that the assistant cannot decide as CTO.
- A destructive operation is required (mass file delete, force push, dropping a table).
- A finding materially changes the SOW scope.
- A second-opinion reviewer raises a concern the assistant cannot resolve with evidence.

Do not pause for technical preference, library choice, naming, or refactor strategy. Those are assistant decisions.

**PR merges are NOT a pause point when PR flow is active.** The operator does
not review or approve pull requests. After external review converges, the
assistant merges the PR itself via `gh pr merge <num> --merge --delete-branch`
and continues. Writing "PR open, awaiting your approval" is a contract breach.

## Anti-Patterns the Assistant Must Avoid

- "I'll write the tests after the code." → contract breach.
- "I'll update the spec at the end." → contract breach.
- "Reviewers can find the gaps for me before I do my own analysis." → contract breach. The CTO does gap analysis, plan, implementation, and self-review first.
- "The operator can test the UI to confirm." → contract breach.
- "Run reviewers on every small edit." → wasteful. Review meaningful chunks: at least per SOW, more often only per substantial milestone.
- "Batch many unrelated SOWs into one reviewer gate." → misses scope. Reviewers must evaluate a coherent goal/chunk.
- "I'll fix the lint warning later." → contract breach.
- "Adding `t.Skip` until I have time." → contract breach unless linked to an issue + SOW.
- "Let me lower the coverage threshold for now." → contract breach.
- **"PR is open, awaiting your approval."** → contract breach. The operator does not approve PRs. When PR flow is active and external review converges, the assistant merges via `gh pr merge --merge --delete-branch`.
- **"Branch protection requires your review."** → contract breach. Branch protection on operator repos uses `enforce_admins=true` + NO required_pull_request_reviews block. If protection is misconfigured with a manual-review gate, fix the config; do not bring it to the operator.

If the assistant catches itself doing any of these, stop, restart from Step 1, and update this skill if a new anti-pattern needs naming.

## Cross-References

- Contract: `AGENTS.md` "Three Reviewer Gates" section (the single source of truth).
- Durable spec: `.agents/sow/specs/workflow.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Helper subagents: `.agents/skills/project-delegation/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Testing: `.agents/skills/project-testing/SKILL.md`
- Specs: `.agents/skills/project-specs-sync/SKILL.md`
- Reviews: `.agents/skills/project-second-opinions/SKILL.md`
