---
name: project-second-opinions
description: Invoke the 5-reviewer Production-Grade Loop (glm, mimo, minimax, qwen, deepseek) for code review, SOW review, design validation, and second opinions on ai-viewer work. The CTO runs reviewers; the implementer never does. Use before marking any non-trivial SOW completed and after major architectural changes.
---

# Second Opinions — the 5-Reviewer Production-Grade Loop

This skill is the runtime enforcement of the **Production-Grade Loop** defined in `AGENTS.md`. The contract lives in `AGENTS.md`; this file is the implementation. If the two ever disagree, `AGENTS.md` wins.

## When To Run

External second-opinion review is **mandatory** — not "encouraged" — for any non-trivial work. The assistant does not trust itself; review converges before "done" is uttered.

**The orchestrator (CTO / master assistant) runs review — exactly once per iteration, on the final integrated state — never the implementation subagent.** Review is the master's QA gate on code it did not author; an implementation subagent running reviewers on its own work both duplicates the master's mandatory round (the master will run it again → 2× the slow, costly review of identical code) and collapses the author/reviewer separation. Because spawned subagents inherit `AGENTS.md` (which mandates this review), the master MUST explicitly forbid reviewers in every implementation delegation prompt — see `project-delegation` skill, the `[FORBIDDEN]` block. If a subagent reports it "ran reviewers," that round does not substitute for the master's: treat the subagent's findings as a useful head start, then run the one official round on the final state and do not re-run beyond convergence.

Mandatory before marking any of these SOWs `completed`:

- Any code-producing SOW (new feature, bug fix beyond a one-liner, refactor).
- New adapter implementation.
- Schema change (touching `data-model.md`).
- Cross-cutting refactor (e.g. ingest pipeline change).
- Security-sensitive change.
- Any SOW spanning > 3 files of non-trivial logic.
- Any SOW the operator flags as important.

### The 5-reviewer set (CTO only)

The CTO runs **exactly these five reviewers in parallel** on every non-trivial code-producing PR:

| # | Reviewer | Invocation |
|---|---|---|
| 1 | `glm` | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.1" --agent code-reviewer "PROMPT"` |
| 2 | `mimo` | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --agent code-reviewer "PROMPT"` |
| 3 | `minimax` (fresh-context review pass; **never** the implementer instance) | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m3-coder" --agent code-reviewer "PROMPT"` |
| 4 | `qwen` | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.7-plus" --agent code-reviewer "PROMPT"` |
| 5 | `deepseek` | `timeout 1800 opencode run -m "llm-netdata-cloud/deepseek-v4-pro" --agent code-reviewer "PROMPT"` |

All five run in parallel (one Bash invocation each, batched in a single assistant turn). Foreground, with `timeout 1800`. The CTO is the only role that runs them.

`codex` and `gemini` from the previous default set are **deprecated** for production-grade review on this project; they are kept in the invocation table for ad-hoc SOW/spec review only.

### PRODUCTION GRADE vote

Each reviewer responds with one of two outcomes:

- `PRODUCTION GRADE` — ship it, no actionable findings.
- `NEEDS WORK` — one or more findings, each with file:line, severity (P0–P3), and a concrete fix proposal.

The CTO does not merge until 5/5 PRODUCTION GRADE, **or** until only P3 noise remains AND the CTO has recorded the P3 findings in the SOW under `## Reviews` with a disposition. P0/P1 always block. P2 always blocks unless explicitly waived by the CTO with a documented reason (rare).

### Stop conditions (P0/P1/P2/P3)

- **5/5 PRODUCTION GRADE, gates green, CI green** → CTO merges.
- **Any P0/P1 NEEDS WORK** → fix, push, re-trigger full 5-reviewer cycle. Iterate.
- **P2 NEEDS WORK** → fix in the same PR, re-trigger the full 5-reviewer cycle; merge only when 5/5 PG or only P3 noise remains.
- **P3 NEEDS WORK** → fix in the same PR, document in SOW `## Reviews`, merge with note when gates green and CI green.
- **Hard stall: 5+ cycles with new P0/P1 each round** → CTO writes a `## Regression` section in the SOW, opens a follow-up SOW in `.agents/sow/pending/`, and surfaces to the operator with a business-level recommendation. Do not loop forever.

### Claim verification (CRITICAL — CTO's job)

Reviewer findings are **claims, not findings**. The CTO verifies every claim before acting on it. Verification steps:

1. **Read the file:line the reviewer cited.** Does the code actually do what the reviewer said?
2. **Run the repro.** If the reviewer says "this race fires under X", construct X and run it.
3. **Cross-check with the spec.** If the reviewer says "violates SPEC Y §3.2", open the spec and confirm.
4. **Decide**: real bug (fix), false positive (reject with evidence in the SOW `## Reviews`), disputed (escalate).

Acting on unverified claims causes two failure modes: (a) implementing phantom bugs that don't exist, (b) ignoring real bugs because the reviewer "sounded uncertain". Verify, then act. The CTO is the only one who decides.

Skip the 5-reviewer cycle only:

- Typo / format-only changes the assistant has visually verified.
- Mechanical renames with no behavior change.
- Doc-only updates with no spec/runtime impact.

The bar to skip is high. When in doubt, run the cycle.

## Safety Rule (CRITICAL)

If this assistant has been spawned BY a reviewer (i.e. this assistant is itself being run for review), it MUST NOT invoke any external reviewer. Causes infinite recursion.

Detection signals (any of these → do not run reviewers):

- Prompt phrases: "Review", "Second opinion", "READ-ONLY REQUEST", "YOU ARE RUNNING BY ANOTHER ASSISTANT".
- Explicit instruction to not run external assistants.
- Environment variable `AI_AGENT_REVIEWER=1` or similar.

When detected, complete the review work directly and return.

**This rule applies to the `minimax` review pass too**: when running as a reviewer, `minimax` is a fresh-context read-only session. If the prompt contains any of the above signals (it always does — the review prompt is "YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION"), the reviewer MUST NOT spawn further reviewers. The CTO is the only role that may spawn the 5-reviewer cycle.

## Invocation Patterns (legacy / ad-hoc)

The Production-Grade Loop supersedes the previous default set. The 5 reviewers above are mandatory for any non-trivial code-producing PR. The reviewers below remain available for **ad-hoc SOW/spec review** (one-off, off the production loop), where the CTO may pick a smaller subset:

| Reviewer | Command |
|---|---|
| codex (ad-hoc only) | `timeout 1800 codex exec "PROMPT" --skip-git-repo-check` |
| gemini (ad-hoc only) | `timeout 1800 gemini -p "PROMPT"` |
| claude (Anthropic) (ad-hoc only) | `CLAUDECODE="" timeout 1800 claude -p "PROMPT"` |
| kimi (ad-hoc only) | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.6" --agent code-reviewer "PROMPT"` |

The five production reviewers (`glm`, `mimo`, `minimax`-fresh, `qwen`, `deepseek`) run mandatorily on every non-trivial PR. For ad-hoc SOW/spec pre-review, the CTO may invoke any reviewer (including the five production reviewers) at their discretion. Ad-hoc rounds are independent of the production-loop run. If a production reviewer is unavailable for a cycle (litellm error, model deprecated, timeout), the CTO retries once, then substitutes from the ad-hoc set (`codex`, `gemini`, `claude`, `kimi`) and logs the substitution in the SOW `## Reviews` with the reason. Two or more simultaneous unavailability → operator surface as a hard stall.

`cd` into the project root before running. Use relative paths (some reviewers stumble on arbitrary absolute paths).

## Ad-hoc Reviewer Set (off the production loop)

- **SOW / design review**: codex + gemini + mimo in parallel.
- **Security-focused review**: minimax + deepseek.

## Prompt Templates

### Code review (Production-Grade Loop)

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Please review the following change in this repository:

<diff or files to review, with file paths>

Vote ONE of:
- PRODUCTION GRADE — ship it, no actionable findings.
- NEEDS WORK — list findings below, each with file:line, severity (P0/P1/P2/P3), and a concrete fix proposal.

Look for:
- Correctness bugs
- Race conditions
- Security issues (input validation, path traversal, injection)
- Separation-of-concerns violations
- Missing test coverage
- Unwanted side effects in code paths not directly related to the change goal
- Performance regressions
- Stylistic violations of the project's coding standards (see .agents/skills/project-coding/SKILL.md)

MANDATORY RULES (FOLLOW ALWAYS):
- DO NOT MAKE CHANGES, DO NOT CREATE/MODIFY/DELETE FILES, DO NOT STOP PROCESSES/SERVICES.
- DO NOT ASK FOR PERMISSIONS - THIS IS A NON INTERACTIVE SESSION.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS. RISK OF INFINITE RECURSION.

THIS IS A READ-ONLY REQUEST. PROVIDE YOUR REVIEW.
```

### SOW review (ad-hoc)

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Vote ONE of:
- PRODUCTION GRADE — ship it, no actionable findings.
- NEEDS WORK — list findings below, each with file:line, severity (P0/P1/P2/P3), and a concrete fix proposal.

Please review SOW file: .agents/sow/<pending|current>/<name>.md

Check:
- Is the problem clearly stated?
- Are the risks correctly identified?
- Is the implementation plan complete?
- Are the affected contracts and surfaces captured?
- Are the validation steps sufficient?
- Anything missing or wrong?
- Any sensitive data accidentally exposed?

MANDATORY RULES:
- DO NOT MAKE CHANGES.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS.

THIS IS A READ-ONLY REQUEST.
```

### Spec/design review (ad-hoc)

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Please review the design spec at: .agents/sow/specs/<file>.md

Check:
- Is the design coherent and complete?
- Are there obvious failure modes not addressed?
- Anything you would do differently?
- Are there standard patterns in the industry that are being ignored?

MANDATORY RULES:
- DO NOT MAKE CHANGES.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS.

THIS IS A READ-ONLY REQUEST.
```

## Workflow

1. Decide the review type (production PR cycle / ad-hoc SOW / ad-hoc spec).
2. Pick the reviewer set: 5 reviewers for production PR; smaller subset for ad-hoc.
3. Compose the prompt (use a template; keep neutral, no embedded conclusions).
4. Run all reviewers in parallel (multiple Bash tool calls in one turn).
5. Wait for all to return.
6. Synthesize findings:
   - List each unique finding once, attributed to which reviewer flagged it.
   - Classify: P0/P1/P2/P3.
   - **Verify every claim** (read the file:line, run the repro, cross-check spec).
7. Address findings (code changes + new tests where applicable). Reject false positives with evidence.
8. **Re-run the same reviewers with the same scope** plus a short note of fixes applied.
9. Repeat until no actionable P0/P1 findings remain. P2 fixed in PR. P3 documented in SOW.
10. Record the review history in the SOW under `## Reviews` with reviewer attribution, the CTO's claim-verification verdict, the fix applied (or "rejected — false positive" with evidence), and the final PRODUCTION GRADE count (e.g. `5/5 PG after fix`).
11. CTO merges via `gh pr merge <num> --merge --delete-branch` once gates green, CI green, and 5/5 PG (or only P3 noise remains).

## Anti-Patterns

- **Narrowing scope on follow-up reviews.** Leaves the rest unreviewed. Always use the same prompt.
- **Fewer than 5 reviewers on a production PR.** Single-reviewer or 3-reviewer blind spots are real. The 5-reviewer cycle is mandatory.
- **Editing the prompt to be less neutral after a reviewer disagreed.** The disagreement is data, not something to argue with.
- **Running reviewers in background and forgetting.** Use foreground. The harness handles parallelism.
- **Pre-screening: "skip review because I'm confident".** That's exactly when you need the review.
- **Acting on unverified claims.** Always run the verification steps before implementing a fix.
- **Reporting work "done" before review convergence.** The honest phrasing while review is pending is "code written, gates green, review pending (X/5 PG)".

## Cross-References

- Contract: `AGENTS.md` "Production-Grade Loop" section (the single source of truth).
- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Delegation: `.agents/skills/project-delegation/SKILL.md`
- Spec: `.agents/sow/specs/second-opinions.md`
