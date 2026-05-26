# Second Opinions (External LLM Review)

## TL;DR

The assistant may — and should, for non-trivial work — consult external LLMs (codex, gemini, glm, kimi, mimo, minimax, qwen) for second opinions, code reviews, SOW reviews, and design validation. The full invocation patterns and prompts live in `.agents/skills/project-second-opinions/SKILL.md`. Always run multiple reviewers in parallel, always show the user the prompts before running.

## When to Run External Reviewers

Mandatory:

- Before marking any SOW as `completed`.
- When making non-trivial schema changes (touching `data-model.md`).
- When introducing a new adapter (review the spec + the implementation).
- When making security-sensitive changes (anything touching `security.md`).
- When the user explicitly requests a second opinion.

Recommended:

- After every major Phase milestone.
- When making cross-cutting refactors.
- When in doubt about a tradeoff.

Not needed:

- Trivial fixes (typos, formatting).
- Mechanical changes within a single small file.
- Internal refactors with no external surface change.

## What to Ask

For each review type, send a clear, unbiased prompt. The full prompt templates live in `project-second-opinions/SKILL.md`. Summary:

- **SOW review**: "Read SOW X. Is the plan complete? Are the risks correctly identified? Do you see anything missing? Do not implement; review only."
- **Code review**: "Read PR/diff X. Look for bugs, security issues, separation-of-concerns violations, missing test coverage, unwanted side effects."
- **Design validation**: "Read spec X. Is the design coherent? Are there obvious failure modes? Anything you would do differently?"

Critical neutrality rules:

- Provide full context without embedded assumptions.
- Do not include the assistant's own conclusion (let the reviewer reach its own).
- Always ask reviewers to identify **unwanted side effects** and **security issues** explicitly.
- Show prompts to the user before running.

## How to Run

All commands are documented in `project-second-opinions/SKILL.md` with the exact invocation flags. The short version (from the user's global instructions):

| Reviewer | Command |
|---|---|
| codex | `timeout 1800 codex exec "PROMPT" --skip-git-repo-check` |
| gemini | `timeout 1800 gemini -p "PROMPT"` |
| claude (self) | `CLAUDECODE="" timeout 1800 claude -p "PROMPT"` |
| glm | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.1" --agent code-reviewer "PROMPT"` |
| kimi | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.6" --agent code-reviewer "PROMPT"` |
| mimo | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --agent code-reviewer "PROMPT"` |
| qwen | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.6-plus" --agent code-reviewer "PROMPT"` |
| minimax | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m2.7-coder" --agent code-reviewer "PROMPT"` |
| deepseek | `timeout 1800 opencode run -m "deepseek/deepseek-v4-pro" --agent code-reviewer "PROMPT"` |

Always:

- Use timeout 1800 (30 minutes); reviewers may take a while.
- Run in parallel (multiple Bash invocations in one batch).
- Run in foreground (no `&`), no background flag.

## Iteration

Iterate until reviewers find nothing actionable. The skill explains why:

> LLMs stop reviewing when they think requirements are satisfied, not when the codebase is exhausted. A follow-up review scoped to "review the fixes" leaves the rest unreviewed.

Therefore: **never narrow the scope between repeated reviews.** Use the same prompt, adding only short notes about fixes implemented. Repeat until clean.

## Critical Safety Rule

**If this assistant has been spawned BY a reviewer (i.e. this assistant is a reviewer itself), it MUST NOT invoke external reviewers.** Causes infinite recursion. The skill describes how to detect:

- Check the spawn context for review-prompt phrases ("Review", "Second opinion", "READ-ONLY REQUEST").
- If detected: refuse to run external reviewers, complete the review work directly.

## Recording Reviews

For every reviewer round triggered during a SOW:

- Record the prompt used (in the SOW under `## Reviews`).
- Record the reviewer's findings (paraphrased; full output too noisy for a SOW).
- Record what was changed in response.

This becomes part of the SOW's audit trail and helps future SOWs learn which reviewers catch which classes of issues.
