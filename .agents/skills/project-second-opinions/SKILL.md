---
name: project-second-opinions
description: Invoke external LLMs (codex, gemini, glm, kimi, mimo, minimax, qwen, deepseek) for code review, SOW review, design validation, and second opinions on ai-viewer work. Use before marking any non-trivial SOW completed and after major architectural changes.
---

# Second Opinions

## When To Run

External second-opinion review is **mandatory** — not "encouraged" — for any non-trivial work. The assistant does not trust itself; review converges before "done" is uttered.

Mandatory before marking any of these SOWs `completed`:

- Any code-producing SOW (new feature, bug fix beyond a one-liner, refactor).
- New adapter implementation.
- Schema change (touching `data-model.md`).
- Cross-cutting refactor (e.g. ingest pipeline change).
- Security-sensitive change.
- Any SOW spanning > 3 files of non-trivial logic.
- Any SOW the operator flags as important.

Mandatory minimum standard:

- **At least three reviewers in parallel** for code review (default set: codex + gemini + glm + qwen).
- **Same prompt across iterations**; never narrow scope on follow-up rounds.
- **Iterate until reviewers converge** with no new actionable findings.
- **Record every round in the SOW** under `## Reviews` with reviewer attribution and resolution.

Skip only:

- Typo / format-only changes the assistant has visually verified.
- Mechanical renames with no behavior change.
- Doc-only updates with no spec/runtime impact.

The bar to skip is high. When in doubt, run reviewers.

## Safety Rule (CRITICAL)

If this assistant has been spawned BY a reviewer (i.e. this assistant is itself being run for review), it MUST NOT invoke any external reviewer. Causes infinite recursion.

Detection signals (any of these → do not run reviewers):

- Prompt phrases: "Review", "Second opinion", "READ-ONLY REQUEST", "YOU ARE RUNNING BY ANOTHER ASSISTANT".
- Explicit instruction to not run external assistants.
- Environment variable `AI_AGENT_REVIEWER=1` or similar.

When detected, complete the review work directly and return.

## Invocation Patterns

All commands use `timeout 1800` (30 minutes max wait). Run multiple reviewers in parallel (one Bash invocation per reviewer, batched in one assistant turn). Run in foreground (no `&`, no `run_in_background`).

| Reviewer | Command |
|---|---|
| codex | `timeout 1800 codex exec "PROMPT" --skip-git-repo-check` |
| gemini | `timeout 1800 gemini -p "PROMPT"` |
| claude (Anthropic) | `CLAUDECODE="" timeout 1800 claude -p "PROMPT"` |
| glm | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.1" --agent code-reviewer "PROMPT"` |
| kimi | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.6" --agent code-reviewer "PROMPT"` |
| mimo | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --agent code-reviewer "PROMPT"` |
| qwen | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.6-plus" --agent code-reviewer "PROMPT"` |
| minimax | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m2.7-coder" --agent code-reviewer "PROMPT"` |
| deepseek | `timeout 1800 opencode run -m "deepseek/deepseek-v4-pro" --agent code-reviewer "PROMPT"` |

`cd` into the project root before running. Use relative paths (some reviewers stumble on arbitrary absolute paths).

## Default Reviewer Set

- **Code review** (PR-style): codex + gemini + glm + qwen in parallel. Four reviewers triangulate well; more becomes noise.
- **SOW / design review**: codex + gemini + mimo in parallel.
- **Security-focused review**: add minimax + deepseek.

## Prompt Templates

### Code review

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

Please review the following change in this repository:

<diff or files to review, with file paths>

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

### SOW review

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR A SECOND OPINION:

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

### Spec/design review

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

1. Decide the review type (code / SOW / spec).
2. Pick the reviewer set.
3. Compose the prompt (use a template; keep neutral, no embedded conclusions).
4. **Show the prompt to the user before running.**
5. Run all reviewers in parallel (multiple Bash tool calls in one turn).
6. Wait for all to return.
7. Synthesize findings:
   - List each unique finding once, attributed to which reviewer flagged it.
   - Classify: bug, design concern, style, false positive.
   - Decide which to act on.
8. Address findings (code changes + new tests where applicable).
9. **Re-run the same reviewers with the same scope** plus a short note of fixes applied.
10. Repeat until no actionable findings remain.
11. Record the review history in the SOW under `## Reviews`.

## Anti-Patterns

- **Narrowing scope on follow-up reviews.** Leaves the rest unreviewed. Always use the same prompt.
- **One reviewer only for important work.** Single-reviewer blind spots are real. Minimum three for code-producing SOWs.
- **Editing the prompt to be less neutral after a reviewer disagreed.** The disagreement is data, not something to argue with.
- **Running reviewers in background and forgetting.** Use foreground. The harness handles parallelism.
- **Pre-screening: "skip review because I'm confident".** That's exactly when you need the review.
- **Reporting work "done" before review convergence.** The honest phrasing while review is pending is "code written, gates green, review pending".

## Cross-References

- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding: `.agents/skills/project-coding/SKILL.md`
- Delegation: `.agents/skills/project-delegation/SKILL.md`
- Spec: `.agents/sow/specs/second-opinions.md`
