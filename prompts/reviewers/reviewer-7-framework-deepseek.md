# Reviewer 7: invariant framework design (deepseek-v4-pro)

**CLI**: `opencode run -m llm-netdata-cloud/deepseek-v4-pro --variant max --agent code-reviewer`
**Scope**: Invariant framework design (Go + SQL)

```
SCOPE-SPECIFIC BRIEF — REVIEWER 7

You are designing the INVARIANT FRAMEWORK — the Go interface and
SQL shape for the 11 checks. You are NOT reviewing any one
adapter. You are the framework architect.

The framework's job: express each of the 11 invariants as a Go
check that takes a database handle and returns a structured
Result. Run mode: from a CLI subcommand (against the live prod
DB) and from per-adapter fixture tests (in CI).

FILE PATHS (for context, not for review):

  internal/canonical/events.go  (canonical types)
  internal/store/store.go  (the *sql.DB handle)
  internal/store/migrations/*.sql  (the schema — for SQL design)
  internal/presenter/sessions_testseed_test.go  (existing per-adapter seed helpers — pattern to follow)
  internal/presenter/sources.go  (existing "thin / opt-in" pattern, SOW-0093)
  cmd/ai-viewer-ingest/main.go  (where the CLI subcommand will be wired)
  .agents/sow/specs/observability.md  (the spec the framework will live in)
  .agents/sow/specs/quality-gates.md  (the CI gate catalog)

DESIGN QUESTIONS:

  1. The Check interface. What's the right shape?
     Proposal: type Check interface { Name() string; Severity() Severity;
                                       Run(ctx, db) (Result, error) }
     where Result is { CheckName string; Sev Severity; Count int;
                       SampleIDs []string; Message string; RanAt time.Time }.
     Critique this. Is it the right shape? What's missing?

  2. The Runner. How do we run all 11 checks?
     - Parallel? Sequential? (Each check is one or a few SQL queries;
       total wall time matters for the CLI subcommand.)
     - Sampling: should each check run against the full DB or a
       sample? (For the live prod DB with 6M ops, a 10% sample
       is 600k rows — still slow for some queries. What's the
       right sampling strategy per check?)
     - Cancellation: ctx-cancel should stop mid-query. Is that
       enough, or do we need explicit timeouts per check?

  3. Per-adapter fixtures. Each adapter gets a fixture: a temp
     SQLite DB with a few real-shaped sessions, hand-curated to
     pass all 11 checks. The CI test loads each fixture and
     asserts all 11 pass. Then a deliberately-corrupted version
     of each fixture (e.g. delete a tool_response payload_ref)
     is run, and the corresponding check must fail.
     Design: how do we represent "deliberately-corrupted"? A
     separate corrupted fixture per check, or a mutation function
     applied to a clean fixture?

  4. Severity tiers. The SOW proposes P0 (data loss, fail-closed
     everywhere) / P1 (count mismatch, fail-closed on live DB,
     warn in CI) / P2 (design/docs, surface only). Where do the
     11 invariants fall on this scale? Justify each.

  5. CLI subcommand shape. `bin/ai-viewer-ingest check-invariants`
     should print a structured report. Format: JSON (machine-
     readable), text table (human-readable), or both? How does
     the operator filter to a single adapter or single check?

  6. The "/api/invariants" endpoint. Same data as the CLI sub,
     but as a REST endpoint. Caching? ETag? Or recompute on
     every request (the data is in the DB anyway)?

  7. The "/api/health" drift field. The existing /api/health
     envelope needs a `drift` field. What's the shape?
     Proposal: { p0_count, p1_count, p2_count, oldest_violation_ts }
     so the UI can show "3 P0 violations, oldest 2h ago".

  8. The hot-path before/after delta check. On every successful
     ingest cycle, we want to know "did this cycle introduce a
     new P0 violation?". The SOW proposes a lightweight check
     that compares pre-scan counts to post-scan counts. Design
     this: is it a separate Go struct, or does it reuse the
     same Check interface? Performance budget: <10ms.

  9. The "quarantine" mechanism (chunk 2 step d in the SOW).
     When a new P0 violation is detected on the hot path, the
     offending session/turn/op goes into a _quarantine table
     rather than into ops. Design the table shape. How does
     the operator "release" a quarantined item once they've
     triaged it?

  10. Migration story. The schema is v11. The framework doesn't
      need a migration. But the SOW mentions a possible future
      expansion: "cross-session consistency" (e.g. "every session
      referenced by a kind='session' op must exist"). Is this in
      scope for v1, or v2?

OUTPUT: a design document, not a findings report. Sections:
  §1. Check interface (Go code sketch)
  §2. Runner (with parallelism / sampling / cancellation)
  §3. Fixture policy (per-adapter + corrupted variants)
  §4. Severity assignment (per-invariant, justified)
  §5. CLI subcommand shape
  §6. REST endpoint shape
  §7. Health drift field shape
  §8. Hot-path delta check
  §9. Quarantine mechanism
  §10. Migration story
  Plus a "what the framework cannot catch" section — explicit
  list of things the 11 invariants miss, so the SOW can decide
whether to add more invariants in v2.
```
