# Second Opinions (External LLM Review)

## TL;DR

The CTO runs external reviewer gates on meaningful chunks of work, at least per
SOW and per substantial milestone for complex SOWs. Reviewers are `glm`,
`minimax`, `kimi`, `mimo`, `deepseek`, and `qwen`.

There are three gates:

- **Gap analysis**: reviewers vote `NOTHING MORE CAN BE DONE` or `NEEDS WORK`.
- **Implementation plan**: reviewers vote `READY FOR IMPLEMENTATION` or `NEEDS WORK`.
- **Implementation review**: reviewers vote `PRODUCTION GRADE` or `NEEDS WORK`.

The CTO verifies every reviewer claim before acting. P0/P1/P2 findings are fixed
or rejected with evidence. Only P3 cosmetic findings may be documented and left.
Minimal waste is part of the gate: reviewers are expensive external checks over
completed CTO artifacts, not the discovery engine for requirements or code
paths.
After a real P0/P1/P2 finding, the CTO must verify that issue class and count
all occurrences, then perform a fresh open-ended review of the whole milestone
from scratch before rerunning reviewers. A class-only review is biased and
insufficient.
The full invocation patterns and prompts live in
`.agents/skills/project-second-opinions/SKILL.md`.

## When To Run External Reviewers

Run reviewers:

- at least once per SOW for each applicable gate;
- per substantial milestone inside complex SOWs;
- for schema, adapter, security, ingestion, or cross-cutting changes;
- when the user explicitly asks for external review.

Do not run reviewers:

- for every line or tiny edit;
- before the CTO has done their own gap analysis, plan, implementation, and
  self-review for the stage;
- when the current artifact is still exploratory or the CTO expects reviewers to
  discover basic requirements, contracts, code paths, or tests;
- after batching many unrelated SOWs together;
- for trivial fixes, formatting, or mechanical changes with no behavior change.

## Mandatory Readiness And Waste Control

Before any external reviewer run, the CTO must load
`.agents/skills/project-second-opinions/SKILL.md`, complete its reviewer-readiness
checklist, and record the evidence in the SOW or work ledger.

The readiness checklist proves that:

- the original goal and current stage artifact are written down;
- relevant SOWs, specs, code, tests, migrations, fixtures, install/runtime
  behavior, pending SOW conflicts, and recovery paths were checked locally;
- known risks, unknowns, rejected alternatives, and sensitive-data handling are
  explicit;
- the CTO self-reviewed the artifact and expects a clean round.

External review rounds have a budget:

- Round 1 is the normal gate.
- Round 2 is allowed only after accepted P0/P1/P2 findings have been generalized
  into issue classes, those classes have been fully verified, and a fresh
  open-ended whole-milestone review has been completed.
- Round 3 is exceptional: if round 2 still has accepted P0/P1/P2 findings, the
  CTO stops, writes a short SOW waste analysis, changes the approach or splits
  the milestone, performs another full open-ended local review, and only then
  runs one more broad-scope round.
- A fourth blocker round for the same gate is forbidden without an
  operator-visible status report and a changed approach.
- P3-only comments do not reopen a converged gate unless verification shows they
  imply a real P0/P1/P2 class.

A 19-round gate is roughly 114 reviewer invocations before retries or technical
failures. That is a process failure: the CTO used reviewers for discovery
instead of completing discovery, targeted class verification, and open-ended
milestone review locally first.

## What to Ask

For each gate, send a clear, unbiased prompt. The full prompt templates live in
`project-second-opinions/SKILL.md`. Summary:

- **Gap gate**: original goal + CTO gap analysis + evidence. Ask whether the
  completed analysis still misses any material path to the goal.
- **Plan gate**: original goal + accepted gap analysis + CTO plan. Ask whether
  the plan is complete, safe, testable, and likely to work.
- **Implementation gate**: original goal + accepted gap analysis + accepted plan
  + diff/tests/spec changes/gate results. Ask whether the implementation is
  correct, complete, and side-effect-free.

Critical neutrality rules:

- Provide full context without steering the answer.
- Include the CTO's artifact for the current gate; reviewers are judging it.
- Always ask reviewers to identify **unwanted side effects** and **security issues** explicitly.
- The CTO does not show review prompts to the operator before running — review is a technical gate, not an operator gate. (This replaces the older "show the user the prompts" rule; the operator sees business outcomes only per `AGENTS.md`.)

## How to Run

All commands are documented in `.agents/skills/project-second-opinions/SKILL.md` with the exact invocation flags. The short version:

### Reviewer Set

| Reviewer | Command |
|---|---|
| `glm` | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.2-max" --variant max --agent code-reviewer "PROMPT"` |
| `minimax` | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m3-coder" --variant max --agent code-reviewer "PROMPT"` |
| `kimi` | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.7-code" --variant max --agent code-reviewer "PROMPT"` |
| `mimo` | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --variant max --agent code-reviewer "PROMPT"` |
| `deepseek` | `timeout 1800 opencode run -m "llm-netdata-cloud/deepseek-v4-pro" --variant max --agent code-reviewer "PROMPT"` |
| `qwen` | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.7-plus" --variant max --agent code-reviewer "PROMPT"` |

Always:

- Use timeout 1800 (30 minutes); reviewers may take a while.
- Run in parallel (multiple Bash invocations in one batch).
- Run in foreground (no `&`), no background flag.

## Iteration

Iterate within the round budget until the gate converges. The reason for keeping
scope broad is:

> LLMs stop reviewing when they think requirements are satisfied, not when the codebase is exhausted. A follow-up review scoped to "review the fixes" leaves the rest unreviewed.

Therefore: **never narrow the scope between repeated reviews.** Use the same
prompt, adding only short notes about fixes implemented.

Before any rerun after accepted P0/P1/P2 findings, the CTO must:

1. Verify the exact claim.
2. Identify the broader class of possible misses during verification.
3. Search the whole milestone scope and count every occurrence of that class.
4. Update the relevant artifact for the whole class, not only the cited line.
5. Perform a fresh, unbiased, open-ended review of the whole milestone from
   scratch. This review is not limited to the cited issue, its class, prior
   fixes, or prior reviewer rounds.
6. Update the artifact for every real issue found by the open-ended review.
7. Record the targeted class verification, open-ended milestone review, and
   disposition in the SOW or work ledger.

Do not spend repeated six-reviewer rounds while reviewers are still discovering
basic gaps. Stop and improve the CTO review process.

## Technical Reviewer Failures

Technical reviewer failures are timeouts, empty output, truncated output,
malformed output, command failures, or any response that cannot be interpreted as
a real review.

Policy:

- If any successful reviewer found P0/P1/P2 findings that the CTO accepts as real
  or not yet disproven, do not retry failed reviewers in that round. Fix or
  reject the blocking findings first, then rerun the whole reviewer batch,
  including reviewers that failed.
- Retry failed reviewers immediately only when all successful reviewers voted
  positively, or found only P3 findings, and the missing votes matter for closing
  the gate.
- Retry a failed reviewer once. If the retry also fails technically, record the
  failure, ignore that reviewer for the current gate, and move on.
- The skip is local to the current gate after the initial technical failure plus
  one retry. Try the reviewer again on a later task or later gate; failures are
  usually transient.

## Critical Safety Rule

**If this assistant has been spawned BY a reviewer (i.e. this assistant is a reviewer itself), it MUST NOT invoke external reviewers.** Causes infinite recursion. The skill describes how to detect:

- Check the spawn context for review-prompt phrases ("Review", "Second opinion", "READ-ONLY REQUEST").
- If detected: refuse to run external reviewers, complete the review work directly.

## Recording Reviews

For every reviewer gate during a SOW, record in the SOW under `## Reviews` or a
stage-specific review subsection:

- Reviewer-readiness checklist evidence.
- Reviewer attribution and vote.
- Technical reviewer failures and any single retry attempted.
- The CTO's claim-verification verdict for each finding.
- Targeted class verifications and open-ended milestone reviews performed
  before reruns after accepted P0/P1/P2 findings.
- The fix applied (or "rejected — false positive" with evidence).
- The final gate outcome.

Do not record full prompts or raw reviewer output in the SOW — those are technical detail. The SOW `## Reviews` is an audit trail of outcomes and verdicts, not a technical dump.

This becomes part of the SOW's audit trail and helps future SOWs learn which reviewers catch which classes of issues.
