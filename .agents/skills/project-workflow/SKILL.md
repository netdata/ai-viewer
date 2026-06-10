---
name: project-workflow
description: Master orchestration cycle for ai-viewer work — the spec→test→code→review→gates→commit sequence enforced on every non-trivial task, operating under the Production-Grade Loop (implementer=minimax, 5 reviewers=glm/mimo/minimax/qwen/deepseek). Use at the start of any SOW work, before any Edit/Write in the project, after every milestone, and whenever the assistant catches itself about to skip a step. The single source of truth for "how we work here".
---

# Workflow

## Purpose

This skill is the assistant's runtime checklist. The contract lives in `AGENTS.md` (the "Production-Grade Loop" section is the operating model); the durable spec lives in `.agents/sow/specs/workflow.md`; this file is the operational pattern the assistant follows every time. Load it at the start of every meaningful task. If the assistant ever notices it has started writing code without consulting this skill, stop and restart from the top.

## Roles (per the Production-Grade Loop)

| Role | Model | Job |
|---|---|---|
| **CTO (master assistant)** | me | orchestrate, decide, verify reviewer claims, integrate, merge, report. Does not write production code. |
| **Implementer** | `minimax` (fresh subagent) | code + tests + specs as delegated. The single producer of code. |
| **Reviewers** | `glm`, `mimo`, `minimax` (fresh-context, never the implementer instance), `qwen`, `deepseek` (5 in parallel) | vote `PRODUCTION GRADE` or `NEEDS WORK` with P0–P3 findings. |

**Implementer ≠ Reviewer.** The `minimax` instance that implements a SOW is **not** the same instance that reviews it. The CTO is the only role that runs reviewers. The CTO is the only role that verifies reviewer claims. See `AGENTS.md` for the full contract and `.agents/skills/project-second-opinions/SKILL.md` for reviewer invocation.

## The Cycle (Invariant Order)

```
1. Re-orient    →  read SOWs, specs, skills
2. Spec         →  update or create specs FIRST
3. Test         →  write failing tests against the new spec
4. Code         →  delegate to minimax implementer; make tests pass
5. Gates        →  run every automated quality gate locally
6. Review       →  CTO runs 5-reviewer Production-Grade Loop; verify claims; iterate
7. Discipline   →  run the Discipline Checklist
8. Commit       →  one commit per logical step; spec + tests + code + docs together
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
- `.agents/skills/project-delegation/SKILL.md`
- any other `project-*` skill matching the task domain

The assistant has likely just compacted or just started; assume nothing is in working memory.

## Step 2 — Spec First

For any runtime-behavior change:

1. List specs that describe the affected behavior.
2. Update each spec to describe the **target** behavior, not the current behavior. The spec leads, the code follows.
3. If no spec covers the area, create one. Add it to `.agents/sow/specs/index.md` in the same commit.
4. Record the spec deltas in the SOW's Pre-Implementation Gate under a `Spec Deltas` heading.
5. Specs are not allowed to contain `TBD`, `N/A`, or "to be confirmed" unless the SOW explains why.

Trivial work (typo/format) skips spec updates. Anything else does not.

## Step 3 — Tests Second

Tests are the executable spec. Write them before implementation, watch them fail, then make them pass.

For each behavior in the updated spec:

1. Identify the test layer (unit / integration / E2E / fuzz / bench). See `project-testing` skill for the pyramid.
2. Write the failing test. Run it locally; confirm it fails for the right reason (not a typo, not a wiring bug — the actual behavior is missing).
3. For adapter parsers/decoders (untrusted-bytes parsing): include at least one fuzz target. (Canonical mapping has no parser to fuzz — its invariants are covered by `internal/canonical/property_test.go`.)
4. For performance-critical paths: include a benchmark with a baseline check.
5. For frontend behavior: include the component test AND the Playwright E2E that exercises it from the user's perspective.

A behavior without a failing test before the implementation lands is a defect.

## Step 4 — Code via Delegation

Production code is written by the **`minimax` implementer** (a fresh-context subagent), not in master context. See `project-delegation` skill for the full protocol.

The delegation prompt to the implementer contains, at minimum:

- The SOW reference (file path).
- The relevant spec excerpt the implementation must honor (quote verbatim).
- The failing tests the implementation must make pass (file paths, test names).
- The quality gates the change must satisfy (`./scripts/gates.sh`).
- The forbidden patterns from `project-coding` skill.
- An explicit instruction to not weaken or skip tests.
- An explicit `[FORBIDDEN]` block stating the implementer must NOT run external reviewers — the CTO does that.

After the implementer returns, the master assistant (CTO):

- Reads the actual diff (never trusts the summary).
- Runs the failing tests to confirm they now pass.
- Runs the gates to confirm nothing else broke.
- Confirms automated-reviewer findings (cubic, codacy) are addressed in the diff.
- Decides whether to accept, ask for changes, or restart with a sharper prompt.
- Logs the implementer model in the SOW (default `llm-netdata-cloud/minimax-m3-coder`; backup rotation per `AGENTS.md`).

## Step 5 — Quality Gates

Run **every** gate from `project-quality-gates` skill locally. Not "the relevant ones" — every one. CI runs them all anyway; running them locally first saves a round-trip and surfaces flakiness before it taints history.

If any gate fails: fix root cause, do not weaken the gate. Lowering a threshold to make a gate pass is a contract breach.

## Step 6 — Production-Grade Loop (5-Reviewer Cycle)

For non-trivial SOWs (anything beyond typos or single-line trivial fixes): the CTO runs the 5-reviewer Production-Grade Loop per `project-second-opinions` skill.

Mandatory:

- **Exactly 5 reviewers** in parallel: `glm`, `mimo`, `minimax` (fresh-context), `qwen`, `deepseek`.
- Same prompt across iterations; never narrow scope on follow-up rounds.
- Each reviewer votes `PRODUCTION GRADE` or `NEEDS WORK` with P0–P3 findings.
- The CTO verifies every claim (read file:line, run the repro, cross-check the spec) before acting.
- P0/P1 → fix and re-trigger the cycle. P2 → fix in the same PR. P3 → document in SOW.
- Stop: 5/5 PG (or only P3 noise) + gates green + CI green → CTO merges.

Anti-pattern: narrowing scope on follow-up review rounds to "review just my fixes". Always use the same broad scope plus a short note of fixes. This catches issues the first round did not surface.

## Step 7 — Discipline Checklist

Before reporting completion to the operator:

- [ ] Specs reflect new behavior — same commit as code.
- [ ] Tests exist, pass, race-clean, coverage thresholds met.
- [ ] All quality gates green locally.
- [ ] External review converged for non-trivial work (5/5 PG or only P3 noise with documented disposition).
- [ ] No new TODO/FIXME without a tracked SOW in `pending/`.
- [ ] `AGENTS.md`, skills, specs updated if a new pattern or gotcha emerged.
- [ ] No half-built features.
- [ ] No silent failures.
- [ ] Sensitive data scan clean.
- [ ] Diff reviewed by master assistant (not just subagent summary).

A "no" anywhere is a defect. Fix before reporting.

## Step 8 — Commit, PR, and Merge

One commit per logical step. Each commit ships spec + tests + code + docs together. Commit messages:

- Describe the change, not the commenter or AI tool.
- Reference the SOW ID.
- Never mention the assistant, vendor, or AI product.
- Use a HEREDOC for the body to preserve formatting.

Standard PR flow for any change touching master:

1. Work on a feature branch (`git checkout -b <slug>`).
2. Commit with the discipline above.
3. Push the branch (`git push -u origin <branch>`).
4. Open a PR (`gh pr create --base master --head <branch>`).
5. Run the 5-reviewer Production-Grade Loop (Step 6) on non-trivial PRs.
6. After convergence (5/5 PG or only P3 noise), **merge yourself**: `gh pr merge <num> --merge --delete-branch`.
7. `git checkout master && git pull` so the local master tracks.

No operator approval step. The operator gates SOWs, not PRs.

After merge:

- Run gates again on the committed state (paranoia).
- If the SOW step is complete, update the SOW's `## Implementation` section with the commit ref and evidence.
- If the SOW is complete, move from `current/` to `done/` with `Status: completed` in the same commit (next PR).

## Reporting to the Operator

Compact, honest, business-outcome-only. The operator does not see file paths, design rationale, test names, or gate command output — those live in the SOW `## Reviews` and the PR description. See `AGENTS.md` "What the operator sees" for the full contract.

- TL;DR (2-3 sentences, business outcomes only).
- SOW id and one-line description.
- PR link + state (open / merged / blocked).
- Reviewer verdicts (PRODUCTION GRADE count: `4/5`, `5/5`).
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

**PR merges are NOT a pause point.** The operator does not review or approve pull requests. After external review converges, the assistant merges the PR itself via `gh pr merge <num> --merge --delete-branch` and continues. Writing "PR open, awaiting your approval" is a contract breach.

## Anti-Patterns the Assistant Must Avoid

- "I'll write the tests after the code." → contract breach.
- "I'll update the spec at the end." → contract breach.
- "Let me just edit this Go file directly." → contract breach unless trivial verified typo.
- "The operator can test the UI to confirm." → contract breach.
- "External review is overkill for this." → contract breach unless SOW says trivial. The 5-reviewer Production-Grade Loop is the default for any non-trivial change.
- "I'll fix the lint warning later." → contract breach.
- "Adding `t.Skip` until I have time." → contract breach unless linked to an issue + SOW.
- "Let me lower the coverage threshold for now." → contract breach.
- **"PR is open, awaiting your approval."** → contract breach. The operator does not approve PRs. After external review converges, the assistant merges via `gh pr merge --merge --delete-branch`.
- **"Branch protection requires your review."** → contract breach. Branch protection on operator repos uses `enforce_admins=true` + NO required_pull_request_reviews block. If protection is misconfigured with a manual-review gate, fix the config; do not bring it to the operator.

If the assistant catches itself doing any of these, stop, restart from Step 1, and update this skill if a new anti-pattern needs naming.

## Cross-References

- Contract: `AGENTS.md` "Production-Grade Loop" section (the single source of truth).
- Durable spec: `.agents/sow/specs/workflow.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Delegation: `.agents/skills/project-delegation/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Testing: `.agents/skills/project-testing/SKILL.md`
- Specs: `.agents/skills/project-specs-sync/SKILL.md`
- Reviews: `.agents/skills/project-second-opinions/SKILL.md`
