---
name: project-delegation
description: Helper-subagent patterns for ai-viewer. Use when bounded read-only investigation, summarization, or parallel context gathering would help. Do not use this skill to delegate implementation by default, and do not use helper subagents as external reviewer gates.
---

# Delegation

## The Rule

The CTO owns implementation. Helper subagents are optional tools for bounded
investigation or summarization. They do not own code, tests, plans, commits, or
reviews.

Use helpers when:

- many files need read-only inspection;
- independent investigations can run in parallel;
- a concise evidence summary would preserve master context;
- the task is exploratory and has a clear, bounded question.

Do not use helpers when:

- the CTO has not yet done the core thinking;
- the task is normal implementation work;
- the output would be accepted without local verification;
- the job is an external reviewer gate. Use `project-second-opinions` for that.

## What "Production Code" Means Here

Files that go into a release artifact or affect runtime behavior:

- `cmd/**/*.go`
- `internal/**/*.go`
- `frontend/src/**`
- `frontend/tests/**`
- `scripts/*.sh` once they are used in CI
- `.github/workflows/**`
- Any SQL migration

The CTO may edit production code directly after the spec/test/code workflow
allows it. This skill is not a restriction on direct implementation.

## Available Subagent Types

The Agent tool exposes specialized subagent types. For ai-viewer work:

| Subagent type | When to use |
|---|---|
| `general-purpose` | Broad read-only investigation or summarization when specialized helpers are not needed. Not the default code producer. |
| `Explore` | read-only search; "where is X defined", "which files reference Y", quick file-by-pattern lookups. Bounded; doesn't read whole files. |
| `Plan` | Optional plan critique or decomposition input. CTO owns the plan. |
| `code-reviewer` | Local helper review for quick feedback. This does not replace external reviewer gates. |
| `code-simplifier` | Optional readability suggestions. CTO verifies before editing. |
| `typescript-pro` | Optional TypeScript/type-system advice for `frontend/`. CTO owns any edits. |
| `deep-research` | multi-source research (online docs, mirrored repos, RFCs) when planning a new feature or adapter. |

For external reviewer gates, see `project-second-opinions`.

## Subagent Prompt Template (Investigation)

```text
[ROLE]
Read-only investigation for ai-viewer.

[QUESTION]
<one specific question>

[SCOPE]
<file paths, packages, or globs>

[DELIVERABLE]
- Direct answer with file:line evidence.
- Anything surprising or worth a follow-up SOW.
- Under 300 words.
```

For investigations, prefer the `Explore` subagent (fast, bounded) over `general-purpose` unless the investigation needs whole-file reads.

## Parallelization

Independent subtasks → parallel Agent calls in a single message. Examples that should always run parallel:

- Reading multiple unrelated files for orientation.
- Investigating two adapters' source formats.
- Comparing independent implementation options.
- Summarizing large specs or fixture sets.
- Running gates on backend and frontend simultaneously when the master needs both reports.

Anti-pattern: serial Agent calls when there's no dependency. Costs walltime and dilutes parallelism budget.

## Verifying Subagent Output

The subagent's summary is a claim, not proof. The CTO verifies before trusting.

After every subagent run:

1. Read the cited files/lines yourself.
2. Check that the answer stayed within scope.
3. Treat surprises as leads to verify, not as facts.
4. If the helper produced draft text or code, review and own it before applying.
5. Never report a helper summary as evidence unless the CTO has verified it.

If anything is off, refine the prompt or do the investigation locally.

## When the Master Must Pause and Surface to the Operator

After helper use, surface to the operator only when the issue is a real product,
risk, or destructive-operation decision:

- Subagent introduced changes outside its scope without explanation.
- A helper finding materially changes the SOW scope.
- Reviewer flagged a concern the assistant cannot resolve with evidence.

In all other cases — naming, library choice, refactor strategy — decide as CTO and proceed.

## Cost / Token Discipline

Helper use is a judgment call, not a default. Use helpers when they improve
coverage or reduce context noise. Do not use them to avoid CTO responsibility.
Do not spawn a helper for a one-line typo fix or a question that can be answered
with a quick local search.

Use bounded helper investigations to finish the CTO's evidence gathering before
external reviewer gates when that reduces risk or context load. This is not a
replacement for the `project-second-opinions` readiness checklist, and helper
subagents never vote on gate outcomes. Their output is raw evidence for the CTO
to verify before spending a six-reviewer round.

## Anti-Patterns

- **Delegating implementation by default.** The CTO writes implementation directly.
- **Spawning a subagent with an unbounded question.** It returns noise, not evidence.
- **Trusting the subagent's summary without checking evidence.** That's how bad assumptions enter the codebase.
- **Serial subagent calls when parallel was possible.** Wastes the operator's time.
- **Restarting a stuck subagent from scratch when SendMessage would refine.** Loses momentum.
- **Using helper subagents as external reviewer gates.** Gate reviews are run through `project-second-opinions`.
- **Skipping local evidence gathering and buying external reviewer rounds.**
  Helpers can cheaply gather bounded evidence; external reviewers are expensive
  gates over completed artifacts.

## Cross-References

- Contract: `AGENTS.md` "Three Reviewer Gates" section (the single source of truth).
- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding rules: `.agents/skills/project-coding/SKILL.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Reviewers: `.agents/skills/project-second-opinions/SKILL.md`
