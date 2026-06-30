---
name: project-second-opinions
description: >
  Run ai-viewer's external reviewer gates. Use on meaningful chunks of work,
  at least per SOW and per substantial SOW milestone: gap analysis,
  implementation plan, and implementation review. Reviewers are glm, minimax,
  kimi, mimo, deepseek, and qwen. They are gates, not implementers.
---

# Second Opinions — Three Reviewer Gates

## Non-Negotiable Open-Ended Review Rule

The only valid review is open-ended over the whole milestone/gate scope.
Targeted class verification is necessary during claim verification, but it is
not a substitute for a full open-ended review.

When external reviewers find any real P0/P1/P2 issue:

1. Verify the exact claim.
2. During verification, identify the issue class and count every occurrence of
   that class across the whole milestone scope.
3. Then perform a fresh, unbiased, open-ended review of the entire milestone
   from scratch. Do not limit this review to the cited finding, the issue
   class, prior fixes, or prior reviewer rounds.
4. Fix or reject every real issue from both the targeted verification and the
   open-ended review before rerunning external reviewers.

The common failure mode is biased limited review: a reviewer reports "X is
wrong", the CTO checks only X-like issues, reruns reviewers, and reviewers then
find unrelated Y/Z issues. That turns external reviewers into scouts. Stop and
perform the whole-milestone open-ended review before any rerun.

External reviewers are a quality gate, not an implementation strategy. The CTO
does the gap analysis, planning, coding, tests, gates, and self-review first.
Reviewers then look for what was missed.

Minimal waste is part of correctness. One reviewer round runs six external
models. A 19-round gate is roughly 114 model invocations before retries,
timeouts, or failed outputs. That is not rigor; it means the CTO used reviewers
as a discovery engine. That false framing is forbidden here. The CTO discovers,
researches, writes, and self-reviews the stage artifact first; reviewers
challenge that completed artifact.

The contract lives in `AGENTS.md`; this skill is the runtime pattern. If they
disagree, `AGENTS.md` wins.

## Reviewer Set

Run these six reviewers for gate reviews:

| Reviewer | Invocation |
|---|---|
| `glm` | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.2-max" --variant max --agent code-reviewer "PROMPT"` |
| `minimax` | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m3-coder" --variant max --agent code-reviewer "PROMPT"` |
| `kimi` | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.7-code" --variant max --agent code-reviewer "PROMPT"` |
| `mimo` | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --variant max --agent code-reviewer "PROMPT"` |
| `deepseek` | `timeout 1800 opencode run -m "llm-netdata-cloud/deepseek-v4-pro" --variant max --agent code-reviewer "PROMPT"` |
| `qwen` | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.7-plus" --variant max --agent code-reviewer "PROMPT"` |

Run them in parallel, foreground, from the repository root. Use relative paths
in prompts.

## Mandatory Readiness Before Any Reviewer Run

Before running external reviewers, complete this checklist and record the
evidence in the SOW or work ledger. If any item is missing, do not run reviewers
yet.

- The original goal is written down.
- The current stage artifact is written down: gap analysis, implementation plan,
  or implementation evidence.
- The CTO has locally checked the relevant SOWs, specs, code, tests,
  migrations, fixtures, install/runtime behavior, pending SOW conflicts, and
  operational recovery paths for the stage.
- Known risks, unknowns, rejected alternatives, and sensitive-data handling are
  explicit.
- The CTO has performed a local self-review and expects a clean reviewer round,
  with at most one surprise follow-up.
- The reviewer prompt uses the same broad scope for every round, relative paths,
  and neutral wording.

Gate-specific minimums:

- Gap analysis: the SOW or notes name the root-cause model, affected contracts,
  data/schema/API/UI surfaces, edge cases, tests, spec deltas, migration/repair
  paths, install/recovery checks, and explicit out-of-scope items.
- Implementation plan: every accepted gap maps to planned specs, tests, code
  files, sequencing, rollback/recovery, validation, and risk controls.
- Implementation review: specs, tests, code, docs, and migrations are complete;
  focused tests and local gates have run; and the CTO has read the diff.

## When To Run

Run reviewers on meaningful chunks:

- at least once per SOW for each applicable gate;
- per substantial milestone inside a complex SOW;
- when the goal is important enough that missing a gap, plan issue, or side effect
  would be costly.

Do not run reviewers:

- for every line or tiny edit;
- as a substitute for CTO analysis;
- when the artifact is exploratory or the CTO expects reviewers to discover
  basic requirements, code paths, tests, or contracts;
- after batching many unrelated SOWs together;
- when the assistant has not yet personally reviewed the goal, plan, code, tests,
  and gate results for the current stage.

## Reviewer Round Budget

The normal result is one reviewer round. A second round is allowed when the
first round finds real P0/P1/P2 surprises and the CTO has performed both the
targeted class verification and the whole-milestone open-ended review described
below.

A third round is exceptional. If round 2 still returns accepted P0/P1/P2
findings for the same gate, stop the reviewer loop, write a short waste analysis
in the SOW explaining why open-ended self-review missed them, change the review
approach or split the milestone, perform another full open-ended local review,
then run one more broad-scope round.

A fourth blocker round for the same gate is forbidden unless the operator has
received a status report and the CTO has changed the approach. Buying more
six-reviewer rounds while reviewers are still discovering basic gaps is a
process failure, not diligence.

P3-only comments do not reopen a converged gate. Record them as cosmetic or
cleanup notes unless verification shows they imply a real P0/P1/P2 class.

## After A NEEDS WORK Result

Findings are evidence that the CTO may have missed a class of issues, not a
mechanical todo list.

For each accepted P0/P1/P2 finding:

1. Verify the exact claim against file/spec/SOW evidence.
2. Identify the broader class of possible misses during verification.
3. Search the whole milestone/gate scope and count every occurrence of that
   class.
4. Update the gap analysis, plan, specs, tests, code, docs, or migration notes
   for the whole class, not only the cited line.

After all accepted P0/P1/P2 findings have been verified:

5. Perform a fresh, unbiased, open-ended review of the entire milestone/gate
   scope from scratch. This review is not scoped to the accepted findings, their
   classes, the lines changed, prior reviewer comments, or the latest fixes.
6. Update the stage artifact for every real issue found by that open-ended
   review.
7. Record both the targeted class verification and the whole-milestone
   open-ended review in the SOW or work ledger before any rerun.

Examples:

- Missing migration default -> check every migration default, insert path, API
  serialization, schema contract test, stale comment, and upgrade path.
- Missing lifecycle transition -> check every state transition, retry path,
  repair path, status panel, and persisted state.
- Adapter nil-safety issue -> check every source event variant, absent field,
  corrupt fixture, and parity/error-surfacing path.

## Gate 1 — Gap Analysis

Use after the CTO has completed a local analysis of the goal and before writing
the implementation plan. Reviewers verify a completed gap analysis; they are not
how the CTO discovers the project.

Reviewer input:

- original goal;
- CTO gap analysis;
- relevant SOW/spec/code evidence;
- constraints, risks, and known unknowns.

Reviewer vote:

- `NOTHING MORE CAN BE DONE` — the gap analysis is complete enough to plan from.
- `NEEDS WORK` — more gaps, risks, checks, tests, specs, or evidence are needed.

## Gate 2 — Implementation Plan

Use after the CTO has written a concrete plan and before implementing.

Reviewer input:

- original goal;
- accepted gap analysis;
- CTO implementation plan;
- planned specs, tests, files, gates, rollout/installation steps, and risk
  controls.

Reviewer vote:

- `READY FOR IMPLEMENTATION` — the plan is coherent, complete, and low-risk enough
  to execute.
- `NEEDS WORK` — the plan misses work, has bad sequencing, weak tests, unclear
  contracts, or likely side effects.

## Gate 3 — Implementation

Use after the CTO has implemented, self-reviewed, and run the relevant tests and
local gates.

Reviewer input:

- original goal;
- accepted gap analysis;
- accepted implementation plan;
- diff/code/tests/spec changes;
- local test and gate results.

Reviewer vote:

- `PRODUCTION GRADE` — implementation and tests satisfy the goal and plan with no
  actionable findings.
- `NEEDS WORK` — correctness, completeness, side-effect, security, performance,
  maintainability, or test issues remain.

## Severity And Stop Conditions

Findings carry severity:

- **P0** — correctness bug, data loss, security issue, race, or direct goal
  failure. Blocks progress.
- **P1** — design defect, missing contract, missing error path, or missing test on
  required behavior. Blocks progress.
- **P2** — maintainability, completeness, performance, or important quality
  issue. Blocks progress; fix or reject with evidence.
- **P3** — cosmetic, wording, minor preference, or non-blocking alternative. May
  be documented and left.

For every gate:

- P0/P1/P2 findings are fixed, or rejected as false positives/hallucinations with
  evidence.
- P3 findings may be fixed or documented.
- Before any rerun after accepted P0/P1/P2 findings, complete and record both
  the targeted class verification and the whole-milestone open-ended review from
  "After A NEEDS WORK Result".
- Re-run the same gate with the same broad scope after fixes. Add only short
  notes about fixes already made; do not narrow the prompt to "review the
  fixes".
- Apply the "Reviewer Round Budget"; repeated blocker rounds without a changed
  approach are waste, not rigor.
- Stop only when every real reviewer response is positive for that gate, or every
  non-positive response is verified as false-positive/noise with evidence.
  Reviewers that fail technically after the allowed single retry are recorded
  and skipped for the current gate only.

## Technical Reviewer Failures

Technical failures include timeouts, empty output, truncated output, malformed
output, command failures, or any response that cannot be interpreted as a real
review.

When a reviewer batch returns:

- If any successful reviewer found P0/P1/P2 findings that the CTO accepts as real
  or not yet disproven, do not retry failed reviewers in that round. Fix or
  reject the blocking findings first, then rerun the whole reviewer batch,
  including the reviewers that failed.
- Retry failed reviewers immediately only when all successful reviewers voted
  positively, or found only P3 findings, and the missing votes matter for closing
  the gate.
- Retry a failed reviewer once. If the retry also fails technically, record the
  failure, temporarily remove that reviewer from the required votes for the
  current gate only, and move on.
- The skip is local to the current gate after the initial technical failure plus
  one retry. Try the reviewer again on a later task or later gate; failures are
  usually transient.

## Claim Verification

Reviewer findings are claims, not facts. The CTO verifies every claim before
acting:

1. Read the cited file/spec/SOW lines.
2. Run or construct the repro when applicable.
3. Cross-check against the goal and accepted gate artifacts.
4. Decide: real finding, false positive, hallucination, or disputed.
5. Record the disposition in the SOW or working notes for the gate.

Acting on unverified claims creates churn and can add bugs. Ignoring real claims
because a reviewer sounded uncertain is equally bad. Verify, then decide.

## Safety Rule (CRITICAL)

If this assistant has been spawned BY a reviewer (i.e. this assistant is itself being run for review), it MUST NOT invoke any external reviewer. Causes infinite recursion.

Detection signals (any of these → do not run reviewers):

- Prompt phrases: "Review", "Second opinion", "READ-ONLY REQUEST", "YOU ARE RUNNING BY ANOTHER ASSISTANT".
- Explicit instruction to not run external assistants.
- Environment variable `AI_AGENT_REVIEWER=1` or similar.

When detected, complete the review work directly and return.

This rule applies to every reviewer, including `minimax`. A reviewer must review
only and must never spawn another reviewer.

## Prompt Templates

### Gap Analysis Gate

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Review this goal and CTO gap analysis.

Goal:
<original goal>

CTO gap analysis:
<gap analysis>

Evidence:
<SOW/spec/code/test references>

Vote ONE of:
- NOTHING MORE CAN BE DONE — no reasonable missing gap, risk, test, check, spec, or evidence remains.
- NEEDS WORK — list findings with severity P0/P1/P2/P3, evidence, and concrete additions.

Verify whether the completed gap analysis still misses any material path to the
goal. Look for missing requirements, edge cases, source evidence, tests, gates,
security risks, performance risks, operational risks, and unwanted side effects.

MANDATORY RULES (FOLLOW ALWAYS):
- DO NOT MAKE CHANGES, DO NOT CREATE/MODIFY/DELETE FILES, DO NOT STOP PROCESSES/SERVICES.
- DO NOT ASK FOR PERMISSIONS - THIS IS A NON INTERACTIVE SESSION.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS. RISK OF INFINITE RECURSION.

THIS IS A READ-ONLY REQUEST. PROVIDE YOUR REVIEW.
```

### Implementation Plan Gate

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Review this implementation plan against the original goal and accepted gap analysis.

Goal:
<original goal>

Accepted gap analysis:
<gap analysis>

CTO implementation plan:
<plan>

Vote ONE of:
- READY FOR IMPLEMENTATION — the plan is complete, coherent, testable, and unlikely to create avoidable side effects.
- NEEDS WORK — list findings with severity P0/P1/P2/P3, evidence, and concrete plan changes.

Check scope, sequencing, specs, tests, gates, migrations, data repair, security,
performance, rollback/installation, and unwanted side effects.

MANDATORY RULES (FOLLOW ALWAYS):
- DO NOT MAKE CHANGES, DO NOT CREATE/MODIFY/DELETE FILES, DO NOT STOP PROCESSES/SERVICES.
- DO NOT ASK FOR PERMISSIONS - THIS IS A NON INTERACTIVE SESSION.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS. RISK OF INFINITE RECURSION.

THIS IS A READ-ONLY REQUEST.
```

### Implementation Review Gate

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Review this implementation against the original goal, accepted gap analysis, and
accepted implementation plan.

Goal:
<original goal>

Accepted gap analysis:
<gap analysis>

Accepted implementation plan:
<plan>

Implementation evidence:
<diff/code/tests/spec changes/gate results>

Vote ONE of:
- PRODUCTION GRADE — implementation and tests satisfy the goal and plan with no actionable findings.
- NEEDS WORK — list findings with severity P0/P1/P2/P3, evidence, and concrete fixes.

Check correctness, completeness, side effects, missing tests, weakened tests,
security, performance, maintainability, specs, docs, migrations, and operational
behavior.

MANDATORY RULES (FOLLOW ALWAYS):
- DO NOT MAKE CHANGES, DO NOT CREATE/MODIFY/DELETE FILES, DO NOT STOP PROCESSES/SERVICES.
- DO NOT ASK FOR PERMISSIONS - THIS IS A NON INTERACTIVE SESSION.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS. RISK OF INFINITE RECURSION.

THIS IS A READ-ONLY REQUEST.
```

## Workflow

1. Decide the gate: gap analysis, implementation plan, or implementation review.
2. Complete and record the "Mandatory Readiness Before Any Reviewer Run"
   checklist.
3. Compose a neutral prompt using the matching template and the same broad scope
   as prior rounds for this gate.
4. Run all six reviewers in parallel.
5. Wait for all to return or fail technically.
6. Synthesize findings:
   - List each unique finding once, attributed to which reviewer flagged it.
   - Classify: P0/P1/P2/P3.
   - Verify every claim.
7. If accepted P0/P1/P2 findings exist, perform the targeted class verification
   and the whole-milestone open-ended review from "After A NEEDS WORK Result",
   then address or reject them with evidence before any retry of technically
   failed reviewers.
8. If all successful reviewers are positive or P3-only, retry each technically
   failed reviewer once if the missing vote matters for closing the gate.
9. Re-run the same gate with the same broad scope plus a short note of fixes
   only after readiness remains true, targeted verification is recorded, and the
   open-ended milestone review is recorded.
10. Apply the "Reviewer Round Budget". Do not buy repeated six-reviewer rounds
    as a substitute for local review.
11. Stop when positive votes converge, all remaining non-positive claims are
    rejected with evidence, only P3 remains documented, or technically failed
    reviewers have exhausted their single retry for this gate.
12. Record the readiness evidence, review history, targeted class
    verifications, open-ended milestone reviews, and dispositions in the SOW or
    relevant work ledger.

## Anti-Patterns

- **Narrowing scope on follow-up reviews.** Leaves the rest unreviewed. Always use the same prompt.
- **Using reviewers as discovery.** The CTO must discover the requirements,
  contracts, tests, and risks locally first; reviewers challenge the completed
  stage artifact.
- **Running reviewers before doing CTO work.** Reviewers enrich and verify; they do not replace analysis, planning, implementation, or self-review.
- **Patch-and-rerun after blockers.** Accepted P0/P1/P2 findings require
  targeted class verification plus a fresh open-ended whole-milestone review
  before another six-reviewer round.
- **Limited-scope post-finding review.** Reviewing only the cited issue class is
  biased review. The class check happens inside claim verification; the
  mandatory pre-rerun review is open-ended across the whole milestone.
- **Rerunning after P3-only comments.** P3-only results are not a blocker unless
  verification shows they imply a real P0/P1/P2 class.
- **Treating more rounds as rigor.** A fourth blocker round for the same gate is
  forbidden without operator-visible status and a changed approach.
- **Running reviewers for tiny edits.** Review meaningful chunks: at least per SOW, more often only per substantial milestone.
- **Batching unrelated SOWs into one gate.** Reviewers need a coherent goal and scope.
- **Editing the prompt to be less neutral after a reviewer disagreed.** The disagreement is data, not something to argue with.
- **Running reviewers in background and forgetting.** Use foreground. The harness handles parallelism.
- **Retrying failed reviewers while accepted P0/P1/P2 findings already exist.** Fix the real findings first, then rerun the whole batch.
- **Retrying a technically failed reviewer more than once for the same gate.** One retry is enough; after the second technical failure, record and move on.
- **Acting on unverified claims.** Always run the verification steps before implementing a fix.
- **Treating P2 as optional.** P2 blocks progress unless rejected with evidence. Only P3 can be left as cosmetic.
- **Reporting work done before the applicable gate converges.**

## Cross-References

- Contract: `AGENTS.md` "Three Reviewer Gates" section (the single source of truth).
- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Helper subagents: `.agents/skills/project-delegation/SKILL.md`
- Spec: `.agents/sow/specs/second-opinions.md`
