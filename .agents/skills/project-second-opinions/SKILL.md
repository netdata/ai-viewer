---
name: project-second-opinions
description: >
  Run ai-viewer's external reviewer gates. Use on meaningful chunks of work,
  at least per SOW and per substantial SOW milestone: gap analysis,
  implementation plan, and implementation review. Reviewers are glm, minimax,
  kimi, mimo, deepseek, and qwen. They are gates, not implementers.
---

# Second Opinions — Three Reviewer Gates

External reviewers are a quality gate, not an implementation strategy. The CTO
does the gap analysis, planning, coding, tests, gates, and self-review first.
Reviewers then look for what was missed.

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

## When To Run

Run reviewers on meaningful chunks:

- at least once per SOW for each applicable gate;
- per substantial milestone inside a complex SOW;
- when the goal is important enough that missing a gap, plan issue, or side effect
  would be costly.

Do not run reviewers:

- for every line or tiny edit;
- as a substitute for CTO analysis;
- after batching many unrelated SOWs together;
- when the assistant has not yet personally reviewed the goal, plan, code, tests,
  and gate results for the current stage.

## Gate 1 — Gap Analysis

Use after the CTO has analyzed the goal and before writing the implementation
plan.

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
- Re-run the same gate with the same broad scope after fixes. Add only short notes
  about fixes already made; do not narrow the prompt to "review the fixes".
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

Focus on what else could reasonably be done to achieve the goal. Look for
missing requirements, edge cases, source evidence, tests, gates, security risks,
performance risks, operational risks, and unwanted side effects.

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
2. Confirm the CTO has completed the work for that stage before reviewer use.
3. Compose a neutral prompt using the matching template.
4. Run all six reviewers in parallel.
5. Wait for all to return or fail technically.
6. Synthesize findings:
   - List each unique finding once, attributed to which reviewer flagged it.
   - Classify: P0/P1/P2/P3.
   - Verify every claim.
7. If P0/P1/P2 findings exist, address or reject them with evidence before any
   retry of technically failed reviewers.
8. If all successful reviewers are positive or P3-only, retry each technically
   failed reviewer once if the missing vote matters for closing the gate.
9. Re-run the same gate with the same broad scope plus a short note of fixes.
10. Repeat until positive votes converge, all remaining non-positive claims are
   rejected with evidence, or technically failed reviewers have exhausted their
   single retry for this gate.
11. Record the review history and disposition in the SOW or relevant work ledger.

## Anti-Patterns

- **Narrowing scope on follow-up reviews.** Leaves the rest unreviewed. Always use the same prompt.
- **Running reviewers before doing CTO work.** Reviewers enrich and verify; they do not replace analysis, planning, implementation, or self-review.
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
