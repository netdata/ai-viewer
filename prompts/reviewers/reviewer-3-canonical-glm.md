# Reviewer 3: canonical model coverage (glm-5.2-max)

**CLI**: `opencode run -m llm-netdata-cloud/glm-5.2-max --variant max --agent code-reviewer`
**Scope**: Canonical model + un-merged harness coverage

```
SCOPE-SPECIFIC BRIEF — REVIEWER 3

You are reviewing the CANONICAL MODEL — the Go types in
internal/canonical/events.go that the adapters emit into. Your job
has two parts:

PART A — CANONICAL MODEL COMPLETENESS

The 11 invariants each assume the canonical model has the right
fields. Read internal/canonical/events.go in full. For each of the
11 invariants, ask: does the canonical model have the field
required to verify the invariant? If not, the invariant cannot be
expressed as a SQL check against the existing schema.

Specifically:
  - "Are user prompts captured?" — does canonical.Op have a way to
    mark a user_input op? Check the kind enum.
  - "Is reasoning content captured?" — does canonical.Op carry
    reasoning_kind, reasoning_text, or similar?
  - "Are tool request payloads captured?" — does canonical.PayloadRef
    have a `kind` that distinguishes 'tool_request' from
    'tool_response'?
  - "Are external subagents linked deterministically?" — does
    canonical.Op have child_session_id (or equivalent)? Is it
    required (NOT NULL) or optional?
  - "Is the turn viewer presenting all the captured information?" —
    walk the canonical types and check that every field has a
    reason for existing. Are there fields we capture that nothing
    in the UI shows? (You'll need to spot-check the frontend
    types in frontend/src/api/types.ts against the canonical Go
    types.)

PART B — UN-MERGED HARNESS COVERAGE

The 5 currently-supported harnesses are aiagent_v2, aiagent_v3,
claude-code, codex, opencode. The mirrored repo /opt/baddisk/
monitoring/repos/ai/ has ~50+ AI agent projects. For each of the
following, decide: should we ingest it? If yes, what would the
adapter look like? If no, why not?

  - google-gemini/gemini-cli (Google's CLI for Gemini)
  - Aider-AI/aider (AI pair programming in the terminal)
  - block/goose (Block's open-source AI agent)
  - plandex-ai/plandex (AI coding agent in the terminal)
  - smtg-ai/claude-squad (multi-agent Claude orchestration)
  - aws/amazon-q-developer-cli (AWS's coding agent)
  - bytedance/trae-agent (ByteDance's coding agent)
  - bytedance/deer-flow (ByteDance's deep research framework)
  - CodebuffAI/codebuff (multi-file edits with AI)
  - charmbracelet/crush (Charm's terminal AI)
  - gptme/gptme (terminal AI assistant)
  - badlogic/pi-mono (multi-model agentic runtime)

For each, briefly answer: source format (JSONL? SQLite? other?),
session model (turns + ops? messages only?), and whether the
canonical model would need changes to ingest it.

KEY QUESTIONS:
  1. What fields exist in the canonical model that no current
     adapter populates? (If we built a 6th adapter tomorrow, what
     would it use? What would it NOT use?)
  2. What invariants in the operator's 11 are not expressible
     against the current schema? (And which would require a
     migration?)
  3. For un-merged harnesses: which 3-5 would give the highest
     analytical leverage for the operator? (Criteria: active
     community, distinct from what we have, doesn't duplicate.)

OUTPUT: the structured findings report, with the canonical model
analysis as Part A and the un-merged harness list as Part B.
```
