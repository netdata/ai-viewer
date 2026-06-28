# SOW-0115 - Reviewer Waste Controls

## Status

Status: completed

Sub-state: process docs updated and validated.

## Requirements

### Purpose

Prevent external reviewer gates from becoming a high-cost discovery loop. The
process must preserve review quality while minimizing wasted model calls,
wall-clock delay, and operator waiting time.

### User Request

The operator stated that the 19+ reviewer-round waste must not happen again.
`AGENTS.md` and related skills must explicitly explain the waste, the delay, the
false framing of using reviewers for discovery, and the correct process. The
external-review skill must be mandatory from `AGENTS.md`, minimal waste must be
reinforced, and the documents must not contradict each other.

### Assistant Understanding

Facts:

- The reviewer set has six models, so each full round spends six external model
  invocations.
- A 19-round gate is roughly 114 external reviewer invocations before retries or
  technical failures.
- The failure mode was not the existence of reviewers. The failure mode was
  using reviewers as a discovery engine instead of completing local analysis and
  class-sweeps before reruns.

Inferences:

- The durable process must make reviewer readiness and round budgeting explicit,
  otherwise future sessions may repeat the same pattern after compaction.
- Specs must be updated alongside `AGENTS.md` and skills because they are durable
  process memory.

Unknowns:

- None that block this process correction.

### Acceptance Criteria

- `AGENTS.md` makes `project-second-opinions` mandatory before external reviewer
  invocation.
- `project-second-opinions` defines readiness, blocker class-sweeps, round
  budget, P3-only behavior, and the waste explanation.
- `project-workflow`, `project-delegation`, and process specs agree with
  `AGENTS.md`.
- Consistency searches find no stale unlimited-iteration or reviewer-discovery
  framing that contradicts the new policy.
- Sensitive-data and formatting checks pass for the changed artifacts.

## Analysis

Sources checked:

- `AGENTS.md`
- `.agents/skills/project-second-opinions/SKILL.md`
- `.agents/skills/project-workflow/SKILL.md`
- `.agents/skills/project-delegation/SKILL.md`
- `.agents/skills/project-specs-sync/SKILL.md`
- `.agents/sow/specs/second-opinions.md`
- `.agents/sow/specs/workflow.md`

Current state:

- `AGENTS.md` already had external reviewer gates, but the prior wording did not
  explain the model-call cost or block repeated reviewer rounds after blockers.
- `project-second-opinions` still allowed an unbounded "repeat until positive"
  loop before this SOW.
- Process specs still had older iteration wording that could contradict the new
  waste controls.

Risks:

- Over-tightening could discourage reviewers for genuinely risky work. The
  update avoids that by keeping the three gates and defining readiness and
  blocker handling, not by removing review.
- Under-tightening would leave the old failure mode available. The update closes
  that by making the second-opinions skill mandatory and adding a hard round
  budget.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The root cause was false framing: treating reviewers as the place to discover
  missing requirements and code paths.
- Correct framing: the CTO performs discovery, writes the stage artifact, runs
  local self-review, and only then asks reviewers to challenge the completed
  artifact.

Evidence reviewed:

- Existing `AGENTS.md` reviewer gate sections.
- Existing project skills for workflow, external reviews, delegation, and specs
  sync.
- Existing process specs for workflow and second opinions.

Affected contracts and surfaces:

- `AGENTS.md` operating contract.
- Runtime project skills used before external reviews and normal workflow.
- Durable process specs for workflow and second-opinion review.
- SOW audit trail.

Existing patterns to reuse:

- `AGENTS.md` as the top-level contract.
- `.agents/skills/project-second-opinions/SKILL.md` as the runtime reviewer
  pattern.
- `.agents/sow/specs/*` as durable process memory.

Risk and blast radius:

- Process-only documentation change. No runtime application behavior changes.
- Main risk is contradiction between contract, skill, and specs; validation
  focuses on cross-document search.

Sensitive data handling plan:

- The SOW and docs contain process descriptions only. No raw secrets,
  credentials, bearer tokens, SNMP communities, customer names, personal data,
  non-private customer-identifying IPs, private endpoints, or proprietary
  incident details are recorded.

Implementation plan:

1. Update `AGENTS.md` to explain reviewer waste, make the second-opinions skill
   mandatory, and define readiness, class-sweep, P3-only, and round-budget rules.
2. Update `project-second-opinions` with the mandatory readiness checklist,
   round budget, class-sweep workflow, and anti-patterns.
3. Update `project-workflow` and `project-delegation` to point to the mandatory
   skill and prevent bypassing the waste controls.
4. Update process specs so durable memory matches the runtime skills.
5. Run consistency, formatting, SOW-audit, and sensitive-data checks.

Validation plan:

- Search for stale reviewer-discovery and unlimited-iteration wording across
  `AGENTS.md`, project skills, and process specs.
- Run `git diff --check` on changed artifacts.
- Run `.agents/sow/audit.sh`.
- Search changed artifacts for the operator's personal name.

Artifact impact plan:

- AGENTS.md: updated.
- Runtime project skills: updated `project-second-opinions`, `project-workflow`,
  and `project-delegation`.
- Specs: updated `second-opinions.md` and `workflow.md`.
- End-user/operator docs: unaffected because this is internal assistant process.
- End-user/operator skills: unaffected because this is repository runtime
  process, not an exported user skill.
- SOW lifecycle: this SOW records the direct operator-approved process change and
  is completed with the documentation update.

Open-source reference evidence:

- None used; this is internal process correction, not an external protocol or
  implementation pattern.

Open decisions:

- None. The operator explicitly requested the process change.

## Implications And Decisions

1. Decision: keep external reviewers as gates, but require readiness evidence
   before each run.
   - Classification: long-term-best.
   - Reasoning: this preserves review quality while preventing reviewers from
     becoming a discovery loop.

2. Decision: add a hard round budget.
   - Classification: long-term-best.
   - Reasoning: without a stop rule, "iterate until clean" can become unlimited
     spending. The new rule forces local review and changed approach before
     more rounds.

3. Decision: do not run external reviewers for this process correction.
   - Classification: surgical.
   - Reasoning: the change is specifically about preventing reviewer waste, is
     process-only, and was validated by local consistency checks. Running six
     external reviewers here would contradict the minimal-waste principle.

## Plan

1. Patch contract and runtime skills.
2. Patch durable process specs.
3. Validate formatting, consistency, SOW audit, and sensitive-data handling.

## Execution Log

### 2026-06-28

- Updated `AGENTS.md` reviewer gate contract and discipline checklist.
- Updated `project-second-opinions` with readiness, round budget, class-sweeps,
  P3-only handling, workflow changes, and anti-patterns.
- Updated `project-workflow` to require the second-opinions checklist before gap,
  plan, and implementation review gates.
- Updated `project-delegation` so helper investigations support evidence
  gathering before expensive external gates but never replace them.
- Updated `second-opinions.md` and `workflow.md` specs to remove stale unlimited
  iteration semantics.

## Validation

Acceptance criteria evidence:

- `AGENTS.md` now mandates `project-second-opinions` before external reviewer
  invocation and records reviewer waste controls.
- `project-second-opinions` now defines mandatory readiness, blocker class
  sweeps, a reviewer round budget, P3-only behavior, and anti-patterns.
- `project-workflow`, `project-delegation`, `second-opinions.md`, and
  `workflow.md` now agree with the top-level contract.

Tests or equivalent validation:

- `rg` consistency scan across `AGENTS.md`, `.agents/skills`, and
  `.agents/sow/specs` found no stale "discover what else" or unlimited
  "repeat until" wording. Remaining hits are the intentional new policy text.
- `git diff --check -- AGENTS.md .agents/skills/project-second-opinions/SKILL.md .agents/skills/project-workflow/SKILL.md .agents/skills/project-delegation/SKILL.md .agents/sow/specs/second-opinions.md .agents/sow/specs/workflow.md` passed.
- `.agents/sow/audit.sh` passed the active SOW pre-implementation gate and
  sensitive-data guardrail. It still reports historical framework/status warnings
  unrelated to this SOW.
- `rg -n "<operator-personal-name>"` against changed artifacts returned no
  matches.

Real-use evidence:

- The next external reviewer run now has to load `project-second-opinions`,
  complete readiness evidence, and obey the round budget before invocation.

Reviewer findings:

- External reviewers were not run for this process correction. The reason is
  recorded in "Implications And Decisions": running a six-reviewer gate to add a
  waste-prevention rule would contradict the minimal-waste principle.

Same-failure scan:

- Searched for stale discovery/iteration phrases across `AGENTS.md`,
  `.agents/skills`, and `.agents/sow/specs`; patched the stale process-spec and
  prompt wording found during the scan.

Sensitive data gate:

- Durable artifacts contain no raw secrets, credentials, bearer tokens, SNMP
  communities, customer names, personal data, non-private customer-identifying
  IPs, private endpoints, or proprietary incident details.

Artifact maintenance gate:

- AGENTS.md: updated.
- Runtime project skills: updated `project-second-opinions`, `project-workflow`,
  and `project-delegation`.
- Specs: updated `second-opinions.md` and `workflow.md`.
- End-user/operator docs: not affected; internal assistant process only.
- End-user/operator skills: not affected; internal repository skills only.
- SOW lifecycle: completed SOW created in `.agents/sow/done/` for audit trail.

Specs update:

- Updated `.agents/sow/specs/second-opinions.md`.
- Updated `.agents/sow/specs/workflow.md`.

Project skills update:

- Updated `.agents/skills/project-second-opinions/SKILL.md`.
- Updated `.agents/skills/project-workflow/SKILL.md`.
- Updated `.agents/skills/project-delegation/SKILL.md`.

End-user/operator docs update:

- None; this is assistant runtime process.

End-user/operator skills update:

- None; no exported operator skill changed.

Lessons:

- External reviewers must challenge completed artifacts. They must not be used
  to build the artifact through repeated rounds.
- Accepted blocker findings imply possible issue classes. The CTO must sweep the
  class locally before spending another reviewer round.

Follow-up mapping:

- No follow-up SOW required.

## Outcome

Completed. Reviewer waste controls are now durable in the contract, runtime
skills, process specs, and this SOW.

## Lessons Extracted

- Minimal waste is part of quality. Review depth is not measured by the number
  of rounds; it is measured by whether local analysis, self-review, and external
  gates each do their proper job.

## Followup

None.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend regression content above the original SOW narrative.
