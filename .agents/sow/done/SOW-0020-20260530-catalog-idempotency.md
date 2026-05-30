# SOW-0020 - catalog rollups must be idempotent under event re-emission

## Status

Status: closed

Sub-state: SUPERSEDED 2026-05-30 by SOW-0004 (codex adapter), which absorbed this catalog-idempotency-under-re-emission fix as a prerequisite: `onOpStarted` counts a call once per op (insert-signal) and migrates the contribution on an identity change keyed on the effective post-upsert identity (empty→prior); `onOpFinalized` applies a now-minus-prior delta. The fix is adapter-agnostic and benefits aiagent_v2/v3 + claude_code + codex. See SOW-0004 `## Reviews` rounds 4–6 and `internal/ingest/{catalog.go,catalog_migrate.go,writer.go}` + `catalog_idempotency_test.go`. Moved to `done/` as a closed (superseded) record. Originally: proposed during SOW-0003 Round-6 review as an independent ingester-layer correctness fix.

## Requirements

### Purpose

Make the `catalog_*` rollup tables idempotent under canonical-event RE-EMISSION, so a
re-read of an already-ingested record can never inflate the aggregate stats the UI shows
(per-agent / per-cwd session counts, per-model / per-tool / per-provider call counts, and
the token / cost / duration totals). Today the main rows are idempotent (SOW-0015: natural-
identity `ON CONFLICT DO UPDATE/COALESCE`) but the catalog writers `... + 1` / `... + ?` on
conflict, so any path that re-emits an already-counted event double-counts the catalog.

### User Request

Spun out of SOW-0003 Round-6: codex found the claude-code late-meta repair double-counting
the catalog. That specific trigger was removed in SOW-0003, but the underlying catalog
non-idempotency is ingester-wide and must be fixed centrally.

### Assistant Understanding

Facts (verified 2026-05-30):
- `internal/ingest/catalog.go` increments on conflict in every writer:
  `catalog_agents.session_count + 1` (:42), `catalog_cwds.session_count + 1` (:54),
  `catalog_models.call_count + 1` (:91), `catalog_tools.call_count + 1` (:108),
  `catalog_providers.call_count + 1` (:228), and the `total_tokens_* / total_cost_usd /
  total_duration_us + ?` rollups (:161-211).
- The main-row writers (`sessions`/`turns`/`ops`/`payload_refs`/`log_entries`) are idempotent
  via natural-identity conflict handling (SOW-0015 Layer 1/2; `ingester.md`).
- Re-emission paths that hit the catalog with emission ENABLED: the defensive
  truncation-rescan (`readTranscript` resets the cursor to 0 when a file shrinks — present in
  the claude_code adapter and any future adapter with the same defense) and potentially any
  future adapter repair path. The emit-suppressed catch-up replay (emitFrom=size) does NOT
  hit the catalog. SOW-0003's late-meta `forceFromZero` re-emit (the original trigger) was
  removed in SOW-0003 Round-6, so this SOW is about the remaining/structural exposure.

Inferences:
- The "idempotent ingest" invariant SOW-0015 established for main rows should extend to the
  catalog: re-emitting an event already counted must be a no-op for the rollups.

Unknowns (resolve during Pre-Implementation Gate):
- Cleanest mechanism: (a) a per-(catalog-key, source-event-identity) "already counted" guard
  table the catalog consults before incrementing; (b) recompute rollups from the main rows
  (a materialized view / periodic recompute) instead of incremental increments; (c) make the
  increment conditional on the main-row INSERT having actually inserted (not updated), i.e.
  only count on first sight. Option (c) is likely simplest: the catalog increment should fire
  only when the triggering main-row write was a genuine INSERT, not an ON CONFLICT UPDATE.

### Acceptance Criteria

1. Re-emitting any already-ingested `SessionStarted` / `OpStarted` / `OpFinalized` leaves
   every `catalog_*` aggregate unchanged. Verification: an ingester test that submits a batch
   twice and asserts identical catalog rows after the second submit.
2. The truncation-rescan path (file shrinks → re-read from 0 with emission) does not inflate
   the catalog. Verification: a test that ingests, truncates+rewrites a fixture, re-ingests,
   asserts catalog totals match a single clean ingest.
3. First-sight counting is preserved (a genuinely new agent/model/tool/cwd/provider is still
   counted exactly once). Verification: existing catalog tests stay green.
4. `ingester.md` documents the catalog-idempotency rule alongside the Layer 1/2/3 section.

## Analysis

(To be completed when moved to current/ with the Pre-Implementation Gate.)

## Pre-Implementation Gate

(To be filled before implementation.)
