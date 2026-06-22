# Reviewer 2: claude-code adapter (claude-opus-4.8)

**CLI**: `claude -p` (claude-opus-4.8)
**Scope**: claude-code adapter + Anthropic Claude Code source

```
SCOPE-SPECIFIC BRIEF — REVIEWER 2

You are reviewing the claude-code ADAPTER and the Anthropic Claude
Code CLI SOURCE together. The ai-viewer project ingests Claude Code
JSONL files (~/.claude/projects/) into a canonical SQLite store
via the claude-code adapter. Your job: figure out what Claude Code
actually emits, what we capture, and what we miss.

FILE PATHS (relative to repo root /home/costa/src/ai-viewer.git):

Our claude-code adapter:
  internal/adapters/claude_code/adapter.go
  internal/adapters/claude_code/mapper_*.go
  internal/adapters/claude_code/cursor.go
  internal/adapters/claude_code/bench_types_test.go
  internal/adapters/claude_code/golden_test.go
  internal/adapters/claude_code/helpers_test.go
  internal/adapters/claude_code/bench_test.go

Mirrored Claude Code source:
  /opt/baddisk/monitoring/repos/ai/anthropics__claude-code/

Live prod DB snapshots (read-only queries):
  sqlite3 /opt/ai-viewer/data/index.db "..."
  - per-kind op counts for claude-code
  - per-kind payload_refs counts for claude-code
  - 'SELECT * FROM ops o JOIN sessions sess ... WHERE s.format = 'claude-code' AND o.kind = 'tool' LIMIT 3' (sample a tool op to see what payload_refs it has)

CTO'S KNOWN GAPS (verify, refute, or extend):
  - 0 tool_request payload_refs vs 31,171 tool_response refs. This is a perfect inverse of "expected" — every tool op has a response but no request. Either the source has no tool_request concept (the JSONL is request-less), OR we're not reading the request field. Cite the relevant JSONL schema and the mapper code.
  - 0 llm_request / 0 llm_response / 0 reasoning payload_refs. For 122,679 llm ops + 20,115 reasoning ops. Are these being captured as something else (e.g. 'log' kind, not payload_refs)?
  - 0/835 session ops with child_session_id. Does Claude Code have a sub-agent concept (it does — Task tool spawns sub-agents). Why is the deterministic link missing?
  - 122,679 llm ops but no captured request/response — are we capturing the prompt + response in a different field (e.g. extras_json)?

KEY QUESTIONS:
  1. Read the Claude Code JSONL schema (look at anthropics__claude-code for the writer side, and our mapper for the reader side). What does the schema look like? Specifically: is there a `tool_use` request block? A `tool_result` response block? How are they keyed?
  2. Why are 0 tool_request refs captured but 31,171 tool_response refs are? Find the exact line in our mapper that decides to skip tool_request but capture tool_response. Is the source missing the request, or are we filtering it out?
  3. Same question for llm_request: does the JSONL have a request block, or are we inferring "request" from a different signal?
  4. Reasoning: Claude Code has thinking blocks. Does the mapper handle them, or skip them? Look for `thinking` or `reasoning` in the source.
  5. Sub-agents: Claude Code's Task tool spawns sub-agents. Does the JSONL carry a parent->child edge, or is the link implicit (the child just shows up in the same project)? If implicit, document the gap; if explicit, find the field and find where we drop it.

OUTPUT: the structured findings report.
```
