---
name: project-delegation
description: Orchestration patterns for ai-viewer — when and how to spawn subagents, how to keep the master assistant out of code-writing, parallelization rules, and verification of subagent output. Use whenever about to Edit/Write production source, before any non-trivial investigation, when spawning second-opinion reviewers, or when the assistant catches itself doing implementation work directly.
---

# Delegation

## The Hard Rule

The master assistant is the **orchestrator**, **QA lead**, **integrator**, and **reviewer**. The master assistant does **not** write production code. Code is produced by spawned subagents working from a written spec and failing tests.

This rule exists because:

- The master assistant's context is finite. Code-writing fills it with raw output that displaces decision history.
- Subagent output gets independently verified by the master before being trusted. Master-written code skips that verification step.
- Compaction destroys the master's working memory; subagents start with a fresh, self-contained context every time.
- Parallel subagents finish faster than serial master-context editing.

If the master assistant ever finds itself about to call `Edit` or `Write` on a production source file, stop and delegate.

## What "Production Code" Means Here

Files that go into a release artifact or affect runtime behavior:

- `cmd/**/*.go`
- `internal/**/*.go`
- `frontend/src/**`
- `frontend/tests/**`
- `scripts/*.sh` once they are used in CI
- `.github/workflows/**`
- Any SQL migration

What the master assistant **is** allowed to write directly:

- `AGENTS.md`
- `.agents/sow/specs/**`
- `.agents/skills/**`
- `.agents/sow/{pending,current,done}/*.md` (SOW files)
- `README.md`, `LICENSE`, top-level docs the assistant owns end-to-end
- Trivial typo / format fixes the assistant has visually verified in the file before editing
- A first-pass scaffold of a config file (e.g. a stub `.golangci.yml`) — subagent then iterates

When in doubt, delegate.

## Available Subagent Types

The Agent tool exposes specialized subagent types. For ai-viewer work:

| Subagent type | When to use |
|---|---|
| `general-purpose` | implementation work; multi-step coding tasks; anything that needs to write files. The default for code production. |
| `Explore` | read-only search; "where is X defined", "which files reference Y", quick file-by-pattern lookups. Bounded; doesn't read whole files. |
| `Plan` | designing an implementation plan before writing the SOW or a sub-plan inside a SOW. Doesn't write code. |
| `code-reviewer` | pre-merge review of recently changed code. Different from external second opinions. |
| `code-simplifier` | post-implementation refinement for clarity. Use after the implementation works. |
| `typescript-pro` | non-trivial TypeScript/type-system work in `frontend/`. |
| `deep-research` | multi-source research (online docs, mirrored repos, RFCs) when planning a new feature or adapter. |

For external second-opinion reviewers (codex, gemini, glm, kimi, mimo, qwen, minimax, deepseek), see `project-second-opinions` skill.

## Subagent Prompt Template (Implementation)

```text
[ROLE]
You are implementing a slice of ai-viewer per SOW `<path>`.

[CONTEXT — read before coding]
- AGENTS.md (contract)
- .agents/sow/specs/workflow.md (process)
- .agents/skills/project-coding/SKILL.md (forbidden patterns + invariants)
- .agents/skills/project-quality-gates/SKILL.md (gates you must satisfy)
- .agents/skills/project-go-backend/SKILL.md  (if Go)
- .agents/skills/project-frontend/SKILL.md    (if frontend)
- .agents/sow/specs/<relevant-spec>.md
- Failing test file(s): <path:line>

[TASK]
<one-paragraph description of the change, in terms of behavior>

[SPEC EXCERPT — implementation MUST honor]
<paste the relevant spec passages verbatim>

[ACCEPTANCE]
- All listed failing tests pass.
- `go test -race ./...` clean (or `npm test -- --run`).
- `golangci-lint run` clean (or `npm run lint`).
- All other gates in project-quality-gates skill green.
- No new TODO/FIXME without a linked SOW.
- No weakening of existing tests, no skip, no nolint suppression.

[CONSTRAINTS]
- Read-only on source filesystems (no write to ~/.ai-agent or similar).
- No outbound network calls.
- No `panic`, `log.Fatal`, `os.Exit` outside main init.
- No `any` / `interface{}` at function boundaries.
- Files ≤ 400 lines, functions ≤ 60 lines unless justified.
- Specs were updated FIRST and tests SECOND; do not edit them now.

[DELIVERABLE]
- The diff.
- A short report listing: files changed, gates run, gate results.
- A note for the master assistant on any judgment calls made.

[FORBIDDEN]
- Editing AGENTS.md, .agents/sow/specs/**, .agents/skills/** (master owns these).
- Editing the failing tests to make them pass artificially.
- Running external second-opinion reviewers (codex/glm/minimax/etc.). **The master runs the single mandatory review round on the final integrated state — the implementation subagent never runs reviewers.**
- Committing or pushing (master orchestrates git).
```

Always quote the spec passages verbatim in the prompt — never let the subagent infer behavior from the file name.

**The `[FORBIDDEN]` block is non-optional in every implementation prompt — especially the "do not run external reviewers" line.** A spawned subagent inherits `AGENTS.md`, which mandates external review before any work is called done. So an implementation prompt that *omits* the carve-out does not make the subagent skip review — it makes the subagent dutifully run codex/glm/minimax **itself**, and then the master runs them **again** on the same final state. That double-review is pure waste (slow, costly) and muddies ownership: review is the master's QA gate on code it did not write, not a step the author runs on its own work. Always paste the `[FORBIDDEN]` block; if a prompt is trimmed, the reviewer line is the one that must survive.

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
- Running multiple external reviewers on the same artifact.
- Scaffolding two unrelated packages.
- Running gates on backend and frontend simultaneously when the master needs both reports.

Anti-pattern: serial Agent calls when there's no dependency. Costs walltime and dilutes parallelism budget.

## Verifying Subagent Output

The subagent's summary is a claim, not proof. The master verifies before trusting.

After every subagent run:

1. **Read the diff with `git diff` or `git status` + `Read`.** The summary may understate or overstate.
2. **Run the failing tests the subagent was supposed to make pass.** Confirm they now pass.
3. **Run the gates the subagent claimed green.** Trust but verify.
4. **Look for collateral damage.** Did the subagent touch files it wasn't supposed to? Did it weaken any test? Did it suppress a linter?
5. **Read the new code for the obvious gotchas in `project-coding` skill** — bounded buffers, error wrapping, no silent failures, no panic, etc.

If anything is off: send the subagent a follow-up message with the specific issue (don't restart from scratch — agents continue via SendMessage). If the subagent's approach is fundamentally wrong: stop, refine the prompt, spawn a new agent.

## When the Master Must Pause and Surface to the Operator

After delegation, if any of the following happens, do not silently retry — surface to the operator:

- Subagent claims acceptance criteria met but gates fail when master re-runs them.
- Subagent introduced changes outside its scope without explanation.
- Subagent could not satisfy the constraints (e.g. tests can't be made green without violating a constraint) — there's likely a real design problem.
- Reviewer flagged a concern the assistant cannot resolve with evidence.

In all other cases — naming, library choice, refactor strategy — decide as CTO and proceed.

## Cost / Token Discipline

Delegation is the right default, not a token optimization. Do not refuse to spawn a subagent because "it's quick to just do here". The cost of a bug from master-context editing is higher than the token cost of delegating.

Conversely, do not spawn a subagent for a one-line typo fix. Trivial verified edits stay in the master.

## Anti-Patterns

- **Editing production Go/TS files in master context** because "it's faster". Contract breach.
- **Spawning a subagent without quoting the spec excerpt.** Subagent guesses behavior, drifts.
- **Trusting the subagent's summary without reading the diff.** That's how regressions enter the codebase.
- **Serial subagent calls when parallel was possible.** Wastes the operator's time.
- **Restarting a stuck subagent from scratch when SendMessage would refine.** Loses momentum.
- **Letting an implementation subagent run external reviewers.** Two failure modes: (1) a *reviewer* subagent spawning reviewers is infinite recursion; (2) an *implementation* subagent running codex/glm/minimax duplicates the master's mandatory round on the same final state — slow, costly, and it makes the author grade its own homework instead of the master QA-gating code it did not write. Only the master runs reviewers. The implementation prompt's `[FORBIDDEN]` block must say so (see the template above).

## Cross-References

- Contract: `AGENTS.md` (Delegation Protocol section)
- Workflow: `.agents/skills/project-workflow/SKILL.md`
- Coding rules: `.agents/skills/project-coding/SKILL.md`
- Gates: `.agents/skills/project-quality-gates/SKILL.md`
- Reviewers: `.agents/skills/project-second-opinions/SKILL.md`
