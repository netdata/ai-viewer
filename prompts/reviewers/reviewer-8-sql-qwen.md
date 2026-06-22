# Reviewer 8: cross-adapter SQL + severity tiering (qwen3.7-plus)

**CLI**: `opencode run -m llm-netdata-cloud/qwen3.7-plus --variant max --agent code-reviewer`
**Scope**: The 11 invariants as Go + SQL with severity tiering

```
SCOPE-SPECIFIC BRIEF — REVIEWER 8

You are designing the PER-INVARIANT SQL CHECKS and the SEVERITY
TIERS. Reviewer 7 (deepseek) is designing the Go framework; you
are designing the SQL. The two must agree on the framework
interface, but your deliverable is the actual queries and the
severity classification.

FILE PATHS:

  internal/store/migrations/*.sql  (the schema — your queries go against this)
  internal/canonical/events.go  (canonical types — for semantic correctness)
  internal/store/schema_contract_test.go  (the existing test that pins the schema)
  .agents/sow/current/SOW-0096-20260622-ingestion-accuracy-audit.md  (the SOW)

Live prod DB for query testing:
  /opt/ai-viewer/data/index.db  (READ-ONLY queries to test your SQL)

YOUR DELIVERABLE — A TABLE OF 11 INVARIANTS × 5 ADAPTERS:

For each combination (e.g. invariant #6 × codex), provide:

  1. The SQL query (or Go-side check, if the check can't be SQL).
     The query must be a single statement that returns a count
     (or a list of violating IDs). It must use the v11 indexes
     and not require a full table scan unless explicitly justified.

  2. The expected value (e.g. "0 violations expected if everything
     is captured"). Use the live DB to compute the actual count
     right now — that's the baseline.

  3. The severity tier (P0/P1/P2) and the justification:
     - P0 = data loss. The canonical model claims to have this
       and the operator needs it; an absence is a bug. E.g. a
       tool op with no response is "the tool ran but we don't
       know what it returned" — that's a fundamental data loss.
     - P1 = count mismatch. The shape is right but the count
       doesn't match the source. E.g. we claim to capture
       100% of LLM request payloads but only have 12,223 of
       1,343,664 — that's a 99% gap, which IS P0 actually.
       Be precise about the threshold.
     - P2 = design / docs. The capture is by-design absent
       and the operator doesn't need it. E.g. v2's source
       might not have a `child_session_id` field — that's P2
       if true.

  4. The threshold. A perfect 1:1 isn't always the right target.
     E.g. reasoning is optional in some harnesses, so a 0% rate
     is fine for codex (?). What's the per-adapter "expected
     range" for each invariant? E.g. "tool_request coverage
     should be ≥ 95% for codex (a small number of tool_use
     blocks may legitimately lack a request body in some
     error paths)".

  5. The exact line in the mapper (if applicable) that explains
     the gap. For each P0 finding, the SQL alone isn't enough;
     you also need to point at the bug. If you can't find the
     bug, mark the finding as "needs deeper investigation" and
     list what you'd want a domain expert (Reviewer 4 for v2,
     Reviewer 6 for opencode, etc.) to confirm.

YOUR OUTPUT IS A TABLE WITH 55 ROWS (11 × 5). Plus:

  §A. Per-invariant general policy (the policy is the same
       across adapters; the threshold is per-adapter).
  §B. Per-adapter summary (which invariants are well-handled,
       which are broken, which are by-design absent).
  §C. The 5 most important findings, ordered by impact. These
       are the findings the CTO will prioritize.
  §D. The 5 least important findings, ordered by "noise". These
       are findings the CTO will mark as P3 / accept-as-known
       and not fix.
  §E. A test plan: for each finding, what test would catch a
       regression in CI? E.g. for v2's "99% of llm ops have no
       captured payload", the test is: "load the v2 fixture;
       assert that every llm op has at least one
       llm_request or llm_response payload_ref".

You are not designing the Go framework. You are designing the
SQL that the Go framework will execute. Stay focused on the
queries, the thresholds, and the severity classification.
```
