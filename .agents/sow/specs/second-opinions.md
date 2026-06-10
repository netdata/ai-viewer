# Second Opinions (External LLM Review)

## TL;DR

The CTO runs the **5-reviewer Production-Grade Loop** (see `AGENTS.md`) on every non-trivial PR. The five reviewers are `glm`, `mimo`, `minimax` (fresh-context, never the implementer instance), `qwen`, `deepseek`. Each votes `PRODUCTION GRADE` or `NEEDS WORK` with P0–P3 findings. The CTO verifies every claim before acting.

Ad-hoc reviews (SOW/spec/design pre-review, off the production loop) may pick a smaller subset from the legacy list (`codex`, `gemini`, `claude`, `glm`, `kimi`, `mimo`, `qwen`, `minimax`, `deepseek`) at the CTO's discretion.

The full invocation patterns and prompts live in `.agents/skills/project-second-opinions/SKILL.md`. The CTO composes and runs review prompts without operator preview — the operator sees business outcomes only.

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
- The CTO does not show review prompts to the operator before running — review is a technical gate, not an operator gate. (This replaces the older "show the user the prompts" rule; the operator sees business outcomes only per `AGENTS.md`.)

## How to Run

All commands are documented in `.agents/skills/project-second-opinions/SKILL.md` with the exact invocation flags. The short version:

### Production-Grade Loop (mandatory, 5 reviewers in parallel)

| Reviewer | Command |
|---|---|
| `glm` | `timeout 1800 opencode run -m "llm-netdata-cloud/glm-5.1" --agent code-reviewer "PROMPT"` |
| `mimo` | `timeout 1800 opencode run -m "llm-netdata-cloud/mimo-v2.5-pro" --agent code-reviewer "PROMPT"` |
| `minimax` (fresh-context review pass; **never** the implementer instance) | `timeout 1800 opencode run -m "llm-netdata-cloud/minimax-m3-coder" --agent code-reviewer "PROMPT"` |
| `qwen` | `timeout 1800 opencode run -m "llm-netdata-cloud/qwen3.6-plus" --agent code-reviewer "PROMPT"` |
| `deepseek` | `timeout 1800 opencode run -m "llm-netdata-cloud/deepseek-v4-pro" --agent code-reviewer "PROMPT"` |

### Ad-hoc (off the production loop; CTO's discretion)

| Reviewer | Command |
|---|---|
| `codex` (ad-hoc only) | `timeout 1800 codex exec "PROMPT" --skip-git-repo-check` |
| `gemini` (ad-hoc only) | `timeout 1800 gemini -p "PROMPT"` |
| `claude` (ad-hoc only) | `CLAUDECODE="" timeout 1800 claude -p "PROMPT"` |
| `kimi` (ad-hoc only) | `timeout 1800 opencode run -m "llm-netdata-cloud/kimi-k2.6" --agent code-reviewer "PROMPT"` |

`codex`, `gemini`, `claude`, and `kimi` are **deprecated for production-grade review** on this project; they remain available for one-off SOW/spec pre-review where the CTO may pick a smaller subset.

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
