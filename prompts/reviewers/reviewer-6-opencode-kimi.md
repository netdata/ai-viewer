# Reviewer 6: opencode adapter (kimi-k2.7-code)

**CLI**: `opencode run -m llm-netdata-cloud/kimi-k2.7-code --variant max --agent code-reviewer`
**Scope**: opencode adapter + SST opencode source

```
SCOPE-SPECIFIC BRIEF — REVIEWER 6

You are reviewing the opencode ADAPTER and the SST opencode
SOURCE together. ai-viewer ingests opencode's local SQLite DB
(~/.local/share/opencode/opencode.db) via the opencode adapter.
Your job: figure out what opencode actually stores, what we
capture, and what we miss.

FILE PATHS:

Our opencode adapter:
  internal/adapters/opencode/adapter.go
  internal/adapters/opencode/conn.go
  internal/adapters/opencode/conn_test.go
  internal/adapters/opencode/cursor.go
  internal/adapters/opencode/mapper_emitters.go
  internal/adapters/opencode/mapper_ops.go
  internal/adapters/opencode/mapper_parts.go
  internal/adapters/opencode/mapper_tools.go
  internal/adapters/opencode/mapper_turn.go
  internal/adapters/opencode/mapper_test.go
  internal/adapters/opencode/conn_dsn_test.go
  internal/adapters/opencode/cursor_regression_test.go
  internal/adapters/opencode/adapter_lifecycle_test.go

Mirrored opencode source:
  /opt/baddisk/monitoring/repos/ai/anomalyco__opencode/

CTO'S KNOWN GAPS:

  - 275,180 llm ops, 133,119 llm_response refs, 160,976 llm_reasoning
    refs, 0 llm_request refs. **The "request" side is missing entirely.**
    Does opencode's SQLite schema have a "request" / "input" side that
    we're not reading, or is "response" the only thing the schema
    stores?

  - 461,940 tool ops, 455,103 tool_response refs, 0 tool_request
    refs. Same pattern: response side only.

  - 161,668 reasoning ops, 160,976 reasoning refs (slight undercount,
    otherwise complete). This is the only adapter where reasoning
    capture is genuinely good.

  - 3,189 session ops, 0 with child_session_id. Does opencode
    have sub-agents / parent-child in its data model?

  - The 100% perfect 1:1 llm_response:llm_op is a 0.48 ratio
    (133,119 / 275,180). Are half the llm ops not getting a
    response? Or is llm_response being emitted for non-llm ops
    (e.g. reasoning)?

KEY QUESTIONS:
  1. Read anomalyco__opencode to find the SQLite schema. What
     tables exist? Specifically: is there a separate "request"
     table or column, or does the schema only store the
     response/output side of an interaction?
  2. Look at mapper_parts.go (parts are opencode's per-message
     content chunks, similar to parts in OpenAI's API). What's
     the part-kind enum? Are there 'request' and 'response'
     parts, or just 'response' / 'text' / 'tool'?
  3. The "request" gap: is the source missing the request, or
     are we emitting it as a different kind? E.g. is the
     "request" the previous turn's "response"?
  4. Sub-agents: opencode has 'agent' and 'subtask' concepts.
     Does the schema capture parent-child, or only top-level
     sessions?
  5. Reasoning: opencode is the only adapter where reasoning is
     well-captured. What does the mapper do right that v2/v3
     don't?

OUTPUT: the structured findings report. The request-side gap is
the most interesting; find out if it's a source-data gap or a
mapper gap.
```
