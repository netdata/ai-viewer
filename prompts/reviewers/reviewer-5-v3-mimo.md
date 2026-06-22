# Reviewer 5: aiagent_v3 adapter (mimo-v2.5-pro)

**CLI**: `opencode run -m llm-netdata-cloud/mimo-v2.5-pro --variant max --agent code-reviewer`
**Scope**: aiagent_v3 adapter

```
SCOPE-SPECIFIC BRIEF — REVIEWER 5

You are reviewing the aiagent_v3 ADAPTER. This is the newer version
of the operator's custom harness (the v2 was the older format).
The source data is also at ~/.ai-agent/sessions/ but in a different
file/format from v2.

NOTE: aiagent_v3 is the operator's OWN custom harness, not a
public open-source project. There is no mirrored upstream repo.
The mapper at internal/adapters/aiagent_v3/mapper_*.go is the
documentation.

FILE PATHS:

  internal/adapters/aiagent_v3/adapter.go
  internal/adapters/aiagent_v3/mapper_test.go  (yes, ONE test file)
  internal/adapters/aiagent_v3/cursor.go
  internal/adapters/aiagent_v3/coverage*.go
  internal/adapters/aiagent_v3/doc.go
  internal/adapters/aiagent_v3/fuzz_test.go
  internal/canonical/events.go

For comparison, also look at v2:
  internal/adapters/aiagent_v2/mapper_*.go  (the older mapper that v3 may have inherited or diverged from)

CTO'S KNOWN GAPS:

  - 0 reasoning ops captured (0 of 46,300 llm ops). Either the v3
    source has no reasoning blocks, or the mapper isn't reading
    them. The v2 mapper has 19,496 reasoning ops; v3 should have
    something similar. Find the discrepancy.

  - 78,210 tool ops vs 0 tool_request / 0 tool_response payload_refs.
    Same as v2: 0% of tool payloads captured.

  - 16,471 session ops, 0 with child_session_id.

  - 46,300 llm ops, 43,337+43,023=86,360 llm_request+llm_response
    refs. So llm payloads are mostly captured (94%) but tool
    payloads are 0%. Asymmetric — why?

KEY QUESTIONS:
  1. v3 has only ONE mapper test file (mapper_test.go), whereas
     v2 has many (mapper_*.go). Is v3 missing test coverage, or
     is v3's mapper genuinely simpler? Walk the v3 mapper in
     full and report the line count + how many branches it has.

  2. Reasoning: trace the v2 mapper's reasoning handling and
     compare to v3. Did v2 inherit the same code, or did v3 drop
     the reasoning branch? Cite both file:line pairs.

  3. Tool payload: v2 has 0 tool refs. v3 has 0 tool refs. Is
     this the same code path (a shared util), or two independent
     bugs that look identical? Find the shared util if any.

  4. SDK request/response: v3 has 46,268+46,266 = 92,534 SDK refs
     and 43,337+43,023 = 86,360 llm refs. What are SDK refs vs
     llm refs? When does each get emitted? Are they double-counted?

  5. Sub-agents: v3's source has the same parent-child question
     as v2. v3 is newer, so it might have explicit support that
     v2 doesn't, or vice versa.

OUTPUT: the structured findings report. v3 is structurally similar
to v2 but with key differences; the side-by-side comparison is
the most valuable part of your report.
```
