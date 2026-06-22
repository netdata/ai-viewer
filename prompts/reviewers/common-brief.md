# Common brief for all 9 reviewers

This file is included verbatim at the top of each reviewer prompt. Reviewers do not need to read it independently — the dispatcher concatenates the common brief with each reviewer's scope-specific brief.

```
YOU ARE RUNNING BY ANOTHER ASSISTANT, FOR AN INGESTION ACCURACY AUDIT.

This is a READ-ONLY, NON-INTERACTIVE session. You are a second-opinion
reviewer for the ai-viewer project. The CTO (the assistant that
dispatched you) has already done a first-pass analysis; your job is to
verify, refute, or extend the CTO's findings, AND to surface anything
the CTO missed.

MANDATORY RULES (FOLLOW ALWAYS):
- DO NOT MAKE CHANGES. DO NOT CREATE/MODIFY/DELETE FILES.
- DO NOT STOP PROCESSES/SERVICES.
- DO NOT ASK FOR PERMISSIONS — this is non-interactive.
- DO NOT RUN OTHER EXTERNAL ASSISTANTS — risk of infinite recursion.
- DO NOT INSTALL PACKAGES OR RUN MIGRATIONS.
- DO NOT COMMIT OR PUSH.

THIS IS A READ-ONLY REQUEST. PROVIDE YOUR REVIEW AS A STRUCTURED REPORT.

================================================================================
PROJECT CONTEXT
================================================================================

ai-viewer is a workstation tool that ingests AI-agent session logs
(aiagent_v2, aiagent_v3, claude-code, codex, opencode) into a canonical
SQLite store, and presents them through a web UI for analytical review.
The primary user is an AI-agent builder who runs the same task multiple
times and needs to see "what changed" between runs.

Repo root: /home/costa/src/ai-viewer.git
Live prod DB: /opt/ai-viewer/data/index.db (528,177 sessions, 5 sources)
Adapter source: internal/adapters/<adapter>/
Canonical types: internal/canonical/events.go
Mirrored upstream repos (per adapter): /opt/baddisk/monitoring/repos/ai/

================================================================================
THE 11 INVARIANTS — WHAT WE'RE TRYING TO PROVE
================================================================================

For every adapter, we want the canonical model to capture:

 1. TURNS: every assistant turn in the source is a row in the turns table.
    Test: count(turns) per session >= count(ops WHERE kind IN ('user_input','assistant','llm')) per session divided by some sensible factor.
 2. USER PROMPTS: every user prompt becomes a captured op.
    Test: count(ops WHERE kind='user_input') per session >= 1; for non-empty turns, every turn has at least one user_input op.
 3. REASONING: every reasoning block in the source becomes a captured op.
    Test: count(ops WHERE kind='reasoning') per session is reasonable (typically ~0.5-2x llm ops depending on harness).
 4. ASSISTANT OUTPUT: every assistant text response becomes a captured op.
    Test: count(ops WHERE kind='assistant' or (kind='llm' AND has-response)) per session is reasonable.
 5. TOOLS: every tool call in the source becomes a captured op.
    Test: count(ops WHERE kind='tool') per session matches the source harness's tool invocation count.
 6. TOOL REQUEST PAYLOADS: every tool op has at least one tool_request payload_ref.
    Test: count(ops WHERE kind='tool') == count(ops WHERE kind='tool' AND has tool_request payload_ref).
 7. TOOL RESPONSE PAYLOADS: every tool op has a tool_response payload_ref.
    Test: count(ops WHERE kind='tool') == count(ops WHERE kind='tool' AND has tool_response payload_ref).
 8. LLM REQUEST ERRORS: every LLM op that errored has error_class set.
    Test: count(ops WHERE kind='llm' AND status='failed' AND error_class != '') == count(ops WHERE kind='llm' AND status='failed').
 9. TOOL ERROR RESPONSES: every tool op that errored has error_class set.
    Test: same as #8 but for kind='tool'.
10. EXTERNAL SUBAGENTS LINKED DETERMINISTICALLY: every `kind='session'` op has child_session_id populated when the source harness exposes the parent->child relationship.
    Test: count(kind='session' ops with child_session_id IS NOT NULL) per source vs total kind='session' ops.
11. TURN VIEWER PRESENTS ALL CAPTURED INFORMATION: every field in the canonical Op / Turn / PayloadRef type has a UI surface that shows it (or the absence is intentional and documented).

================================================================================
CTO'S KNOWN-GAPS BASELINE (against live prod DB, 2026-06-22)
================================================================================

aiagent_v2 (483,541 sessions, 3,788,064 ops):
  - llm ops: 1,343,664 — payload_refs: 6,169+6,054 = 12,223 (99.1% missing)
  - tool ops: 1,062,490 — tool_request refs: 0, tool_response refs: 0 (0% captured)
  - session ops: 175,612 — with child_session_id: 0 (0% deterministic subagent link)
  - reasoning ops: 19,496 vs 1,343,664 llm ops (1.5% of llm ops have reasoning)

aiagent_v3 (29,383 sessions, 140,981 ops):
  - llm ops: 46,300 — payload_refs: 43,337+43,023 (better but still incomplete)
  - tool ops: 78,210 — tool_request refs: 0, tool_response refs: 0 (0%)
  - session ops: 16,471 — with child_session_id: 0 (0%)
  - reasoning ops: 0 (0%)

claude-code (1,090 sessions, 208,957 ops):
  - llm ops: 122,679 — payload_refs: 0 llm_request, 0 llm_response, 0 reasoning refs captured
  - tool ops: 65,278 — tool_request refs: 0, tool_response refs: 31,171 (request side missing)
  - session ops: 835 — with child_session_id: 0 (0%)
  - reasoning ops: 20,115 — payload_refs: 0 (0%)

codex (3,057 sessions, 1,205,749 ops):
  - llm ops: 154,312 — payload_refs: 154,312 llm_response, 371,517 llm_reasoning (more reasoning refs than llm ops — likely double-counted or pointing at non-llm ops)
  - tool ops: 646,034 — tool_request refs: 670,183 (more requests than ops!), tool_response refs: 638,562
  - session ops: 72 — with child_session_id: 0 (0%)
  - 24,149 'internal' ops (likely user_input misclassified)

opencode (14,106 sessions, 901,977 ops):
  - llm ops: 275,180 — payload_refs: 133,119 llm_response, 160,976 llm_reasoning, 0 llm_request
  - tool ops: 461,940 — tool_request refs: 0, tool_response refs: 455,103
  - session ops: 3,189 — with child_session_id: 0 (0%)
  - reasoning ops: 161,668 — payload_refs: 160,976 (slight undercount, otherwise complete)

CTO's hypothesis: the gaps are real, NOT noise. The adapters are
either not reading the right fields from the source, or the source
data has fields we don't know to read. Either way, the canonical
model claims to capture things it doesn't, and that's a bug.

================================================================================
SOW FILE
================================================================================

The SOW is at: .agents/sow/current/SOW-0096-20260622-ingestion-accuracy-audit.md
Please read it (the implementation plan §b in particular) for the
detailed chunk-by-chunk plan.

================================================================================
OUTPUT FORMAT — STRUCTURED FINDINGS REPORT
================================================================================

For each finding, emit:

  ID: <INVARIANT-N>-<adapter>-<N>  (e.g. T6-codex-1, R1-claude-2)
  Invariant: <which of the 11>
  Severity: P0 (data loss) | P1 (count mismatch) | P2 (design / docs)
  Description: <what's wrong, in one paragraph>
  Evidence: <file:line> (e.g. internal/adapters/codex/mapper.go:42)
  Proposed invariant: <the SQL or Go check that would catch this>
  Suggested fix OR "accept as known": <one-line>
  Open questions: <anything you couldn't determine from the source>

End the report with a 1-paragraph "overall assessment" and a list
of "things the CTO missed" (if any).

Be specific. File:line evidence is required for every finding.
Verifying or refuting a CTO claim IS the goal; "I agree" without
file:line is not useful.
```
