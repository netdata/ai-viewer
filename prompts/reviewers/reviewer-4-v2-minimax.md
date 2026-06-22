# Reviewer 4: aiagent_v2 adapter (minimax-m3-coder)

**CLI**: `opencode run -m llm-netdata-cloud/minimax-m3-coder --variant max --agent code-reviewer`
**Scope**: aiagent_v2 adapter

```
SCOPE-SPECIFIC BRIEF — REVIEWER 4

You are reviewing the aiagent_v2 ADAPTER. The aiagent_v2 adapter
ingests JSONL session files written by the operator's custom
aiagent v2 harness. The source lives at ~/.ai-agent/sessions/.

NOTE: aiagent_v2 is the operator's OWN custom harness, not a
public open-source project. There is no mirrored upstream repo
to consult. The source of truth is the JSONL files on disk; the
mapper at internal/adapters/aiagent_v2/mapper_*.go is the
ONLY documentation of what we capture.

FILE PATHS (relative to repo root /home/costa/src/ai-viewer.git):

  internal/adapters/aiagent_v2/adapter.go
  internal/adapters/aiagent_v2/mapper_*.go  (all mapper files)
  internal/adapters/aiagent_v2/cmd/  (if any sub-commands)
  internal/adapters/aiagent_v2/coverage*.go  (the existing tests)
  internal/adapters/aiagent_v2/doc.go  (if present)
  internal/canonical/events.go  (the canonical types we map into)

CTO'S KNOWN GAPS (verify, refute, or extend — these are the
biggest of the 5 adapters):

  - 1,343,664 llm ops but only 6,169+6,054 = 12,223 llm_request+
    llm_response payload_refs. **99.1% of llm ops have no captured
    payload.** This is the most severe gap in the system. Find
    the line in the mapper that decides whether to write a
    payload_ref for an llm op. What's the gate? Is the JSONL
    missing the field, or is the mapper not reading it?

  - 1,062,490 tool ops but 0 tool_request / 0 tool_response
    payload_refs. **0% of tool payloads captured.** Same question:
    where is the gate, and is the source data present?

  - 19,496 reasoning ops vs 1,343,664 llm ops (1.5%). Claude
    Code has 16.4%, codex has 240%, opencode has 58%. 1.5% looks
    way too low for v2. Are we filtering reasoning out, or is
    v2's source data not carrying reasoning?

  - 175,612 session ops, 0 with child_session_id. Does v2 even
    have sub-agents, or is this a real gap?

  - Top error_class is 'none' (most ops), then 'internal', 'io',
    'system_error'. Are these real error classes from the source,
    or are we synthesising them? If synthesised, where?

KEY QUESTIONS:
  1. Read mapper_ops.go and mapper_payload.go end-to-end. The
     payload-emission function (whatever it's called — look for
     where PayloadRef is created) is the heart of this. Why is
     it gated so tightly that 99% of llm ops don't get a payload?
     Is it:
       (a) A bug — the gate reads the wrong field
       (b) A design choice — the source data doesn't carry
           request/response pairs in a way we know how to read
       (c) An oversight — the mapper has the code but a bug
           prevents the branch from firing
  2. Look at the JSONL schema (find a sample by listing
     ~/.ai-agent/sessions/ — these are real files; you can read
     one). What's in it? Specifically: is there a "request" or
     "payload" field that we're not reading? Is there a
     "tool_input" / "tool_output" pair that we should be
     emitting payload_refs for?
  3. Reasoning: the source schema likely has a `thinking` or
     `reasoning` block. Does the mapper read it? Cite the field
     name and the mapper line that handles (or ignores) it.
  4. Sub-agents: v2's source has spawns somehow. Where? Does the
     JSONL carry the parent-child relationship, or is it implicit?

OUTPUT: the structured findings report. The v2 adapter is the
biggest source of gaps in the system; this is the most impactful
reviewer slot.
```
