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
- after batching many unrelated SOWs together;
- for trivial fixes, formatting, or mechanical changes with no behavior change.

## What to Ask

For each gate, send a clear, unbiased prompt. The full prompt templates live in
`project-second-opinions/SKILL.md`. Summary:

- **Gap gate**: original goal + CTO gap analysis + evidence. Ask what else could
  reasonably be done to achieve the goal.
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

Iterate until the gate converges. The reason:

> LLMs stop reviewing when they think requirements are satisfied, not when the codebase is exhausted. A follow-up review scoped to "review the fixes" leaves the rest unreviewed.

Therefore: **never narrow the scope between repeated reviews.** Use the same
prompt, adding only short notes about fixes implemented. Repeat until the gate's
positive vote is reached, or every non-positive vote is rejected with evidence.

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

- Reviewer attribution and vote.
- Technical reviewer failures and any single retry attempted.
- The CTO's claim-verification verdict for each finding.
- The fix applied (or "rejected — false positive" with evidence).
- The final gate outcome.

Do not record full prompts or raw reviewer output in the SOW — those are technical detail. The SOW `## Reviews` is an audit trail of outcomes and verdicts, not a technical dump.

This becomes part of the SOW's audit trail and helps future SOWs learn which reviewers catch which classes of issues.
