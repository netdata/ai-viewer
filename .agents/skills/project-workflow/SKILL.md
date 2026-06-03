---
name: project-workflow
description: Master orchestration cycle for ai-viewer work — the spec→test→code→review→gates→commit sequence enforced on every non-trivial task. Use at the start of any SOW work, before any Edit/Write in the project, after every milestone, and whenever the assistant catches itself about to skip a step. The single source of truth for "how we work here".
---

# Workflow

## Purpose

This skill is the assistant's runtime checklist. The contract lives in `AGENTS.md`; the durable spec lives in `.agents/sow/specs/workflow.md`; this file is the operational pattern the assistant follows every time. Load it at the start of every meaningful task. If the assistant ever notices it has started writing code without consulting this skill, stop and restart from the top.

## The Cycle (Invariant Order)

```
1. Re-orient    →  read SOWs, specs, skills
2. Spec         →  update or create specs FIRST
3. Test         →  write failing tests against the new spec
4. Code         →  delegate to subagent; make tests pass
5. Gates        →  run every automated quality gate locally
6. Review       →  external second opinions; iterate until converged
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

Production code is written by subagents, not in master context. See `project-delegation` skill.

The delegation prompt to a subagent contains, at minimum:

- The SOW reference (file path).
- The relevant spec excerpt the implementation must honor (quote verbatim).
- The failing tests the implementation must make pass (file paths, test names).
- The quality gates the change must satisfy (`./scripts/gates.sh` once it exists).
- The forbidden patterns from `project-coding` skill.
- An explicit instruction to not weaken or skip tests.

After the subagent returns, the master assistant:

- Reads the actual diff (never trusts the summary).
- Runs the failing tests to confirm they now pass.
- Runs the gates to confirm nothing else broke.
- Decides whether to accept, ask for changes, or restart with a sharper prompt.

## Step 5 — Quality Gates

Run **every** gate from `project-quality-gates` skill locally. Not "the relevant ones" — every one. CI runs them all anyway; running them locally first saves a round-trip and surfaces flakiness before it taints history.

If any gate fails: fix root cause, do not weaken the gate. Lowering a threshold to make a gate pass is a contract breach.

## Step 6 — External Review

For non-trivial SOWs (anything beyond typos or single-line trivial fixes): run external second opinions in parallel per `project-second-opinions` skill.

Minimum: three reviewers, same prompt, parallel execution. Iterate until reviewers converge with no new actionable findings. Record the rounds in the SOW under `## Reviews`.

Anti-pattern: narrowing scope on follow-up review rounds to "review just my fixes". Always use the same broad scope plus a short note of fixes. This catches issues the first round did not surface.

## Step 7 — Discipline Checklist

Before reporting completion to the operator:

- [ ] Specs reflect new behavior — same commit as code.
- [ ] Tests exist, pass, race-clean, coverage thresholds met.
- [ ] All quality gates green locally.
- [ ] External review converged for non-trivial work.
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
5. Run external reviewers (Step 6) on non-trivial PRs.
6. After convergence, **merge yourself**: `gh pr merge <num> --merge --delete-branch`.
7. `git checkout master && git pull` so the local master tracks.

No operator approval step. The operator gates SOWs, not PRs.

After merge:

- Run gates again on the committed state (paranoia).
- If the SOW step is complete, update the SOW's `## Implementation` section with the commit ref and evidence.
- If the SOW is complete, move from `current/` to `done/` with `Status: completed` in the same commit (next PR).

## Reporting to the Operator

Compact, honest, evidence-based.

- TL;DR (2-3 sentences).
- Bullet points of what changed.
- File paths with line numbers as evidence.
- Gates: which ran, what they reported.
- Reviewers: which ran, what they found, what was done about it.
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
- "External review is overkill for this." → contract breach unless SOW says trivial.
- "I'll fix the lint warning later." → contract breach.
- "Adding `t.Skip` until I have time." → contract breach unless linked to an issue + SOW.
- "Let me lower the coverage threshold for now." → contract breach.
- **"PR is open, awaiting your approval."** → contract breach. The operator does not approve PRs. After external review converges, the assistant merges via `gh pr merge --merge --delete-branch`.
- **"Branch protection requires your review."** → contract breach. Branch protection on operator repos uses `enforce_admins=true` + NO required_pull_request_reviews block. If protection is misconfigured with a manual-review gate, fix the config; do not bring it to the operator.

If the assistant catches itself doing any of these, stop, restart from Step 1, and update this skill if a new anti-pattern needs naming.

## Cross-References

- Contract: `AGENTS.md`
- Durable spec: `.agents/sow/specs/workflow.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Delegation: `.agents/skills/project-delegation/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Testing: `.agents/skills/project-testing/SKILL.md`
- Specs: `.agents/skills/project-specs-sync/SKILL.md`
- Reviews: `.agents/skills/project-second-opinions/SKILL.md`
