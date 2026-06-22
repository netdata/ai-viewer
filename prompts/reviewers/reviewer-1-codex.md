# Reviewer 1: codex adapter (OpenAI gpt-5.5)

**CLI**: `codex2 exec` (OpenAI gpt-5.5)
**Scope**: codex adapter + OpenAI codex source

```
SCOPE-SPECIFIC BRIEF — REVIEWER 1

You are reviewing the codex ADAPTER and the OpenAI codex SOURCE
together. The ai-viewer project ingests codex JSONL files
(rollout-*.jsonl) into a canonical SQLite store via the codex
adapter. Your job: figure out what the codex CLI actually emits,
what we capture, and what we miss.

FILE PATHS (relative to repo root /home/costa/src/ai-viewer.git):

Our codex adapter:
  internal/adapters/codex/adapter.go
  internal/adapters/codex/mapper_finalize.go
  internal/adapters/codex/mapper_state.go
  internal/adapters/codex/mapper_turn.go
  internal/adapters/codex/ops_*.go (ops_event.go, ops_enrich.go, ops_response.go, ops_tools.go)
  internal/adapters/codex/discovery.go
  internal/adapters/codex/doc.go
  internal/adapters/codex/containment_branch_test.go
  internal/adapters/codex/coverage_branch_test.go

Mirrored codex source:
  /opt/baddisk/monitoring/repos/ai/openai__codex/

Live prod DB snapshots (read-only queries you can run):
  sqlite3 /opt/ai-viewer/data/index.db "..."
  - 'SELECT kind, COUNT(*) FROM ops o JOIN sessions sess ON sess.id = o.session_id JOIN sources s ON s.id = sess.source_id WHERE s.format = 'codex' GROUP BY kind'
  - 'SELECT pr.kind, COUNT(*) FROM payload_refs pr ... WHERE s.format = 'codex' GROUP BY pr.kind'
  - 'SELECT * FROM ops o ... WHERE o.kind = 'internal' LIMIT 5'  (the 24,149 'internal' ops are suspicious)

CTO'S KNOWN GAPS (verify, refute, or extend):
  - 24,149 'internal' ops in codex data — what are these? user_input misclassified?
  - 670,183 tool_request refs vs 646,034 tool ops (more requests than ops — likely double-counted or pointing at non-tool ops; check if a tool_request can be on a non-tool op kind, or if multiple tool_requests can attach to the same op)
  - 638,562 tool_response refs vs 646,034 tool ops (slight undercount — is one tool op intentionally missing its response, or did a tool op get 0 responses?)
  - 371,517 llm_reasoning refs vs 154,312 llm ops (more reasoning than llm — same double-counting question)
  - 154,312 llm_response refs (1:1 with llm ops, looks correct)
  - 0/72 session ops with child_session_id (no deterministic subagent link — does the codex JSONL even contain a child_session_id field? if not, this gap is "by source design" and should be documented as accepted)

KEY QUESTIONS:
  1. Read the codex JSONL schema (look at openai__codex for the writer side; look at our mapper_state.go for the reader side). What fields exist in the source that we DON'T read?
  2. The 24,149 'internal' ops: walk through the mapper and find the code path that produces 'internal' kind. Is it user_input, or something else? Cite the file:line.
  3. The tool_request double-counting: walk through how a tool_use block in the codex response becomes a tool op AND a tool_request payload_ref. If both come from the same source event, why are the counts not 1:1?
  4. The 'session' ops: do codex rollouts actually have a sub-agent / fork relationship, or does codex never have sub-agents? If the latter, the 0% is correct and should be documented.
  5. Reasoning capture: codex has 371k reasoning refs but only 154k llm ops. Is reasoning captured at the right granularity?

OUTPUT: the structured findings report described in the common brief.
The CTO's known gaps are the hypothesis; you verify or refute them.
```
