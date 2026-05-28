# SOW-0015 - Ingest worker FK constraint failures on real corpus

## Status

Status: completed

Sub-state: moved to current/ on 2026-05-27 after operator approval ("fix it"). Root-cause investigation completed by the master assistant before scheduling — see `### Confirmed root cause (2026-05-27, master investigation)` below, which REFINES and partially overturns the codex Chunk-11 primary hypothesis. The fix decision is recorded in `### Fix decision (CTO call, 2026-05-27)`.

Priority: P1 — blocks SOW-0001 Phase 1 acceptance criterion "full
backfill <60min completes successfully on operator's real corpus".
Live runs currently lose events to FK rejection on every batch where
the chain breaks.

## Requirements

### Purpose

Make `ai-viewer-ingest` complete a full backfill against the
operator's real `~/.ai-agent/sessions` corpus without dropping any
event to a FOREIGN KEY constraint failure. The fit-for-purpose
target is: the operator runs the ingester once on their workstation
and receives an SQLite database that contains every parseable event,
with parse errors surfaced via `/api/health`. Silent data loss is a
contract breach.

### User Request

Filed as a follow-up by the SOW-0001 Chunk 11 iteration 2 review
brief. The brief explicitly instructs the assistant to "file a
follow-up SOW, do NOT attempt to fix this in Chunk 11".

### Assistant Understanding

Facts:

- `payload_refs.op_id REFERENCES ops(id)` (migration
  `internal/store/migrations/0001_initial.sql:147`).
- `log_entries.op_id REFERENCES ops(id)` (migration
  `internal/store/migrations/0001_initial.sql:170`).
- Within ONE v3 ledger record the adapter emits `OpStarted` →
  `OpFinalized` → `PayloadRef[]` in order with monotonically
  increasing `SourceSeq` (subIdx 0, 1, 2, ...).
- The ingest worker batches events at 1000 events / 500 ms (see
  `internal/ingest/worker.go`).
- A real-corpus integration smoke run executed at the end of
  SOW-0001 Chunk 11 iter 1 produced repeated worker logs of the
  form:

  ```
  level=ERROR msg="worker: batch failed"
    err="apply event payload_ref seq=...:
         writer: insert payload_ref:
         constraint failed: FOREIGN KEY constraint failed (787)"
  ```

  on a fresh database. `log_entries` inserts fail with the same
  class of error.
- The FK fires immediately on a fresh database at seq=45060 (the
  first error batch observed in the smoke run). Subsequent batches
  trigger the same FK failure repeatedly.

Inferences:

- The schema's FK structure is correct: every child must reference
  an `ops` row that already exists.
- The bug is in the event-ordering, dedup, or commit path between
  the adapter, the worker, and the writer. The schema is not at
  fault.

Unknowns:

- The exact failing case has not been captured: which `op_id` was
  the orphan, which adapter emitted it, what events preceded it in
  the same source, and whether the parent `OpStarted` ever reached
  the writer.

### Acceptance Criteria

- Zero FK constraint failures on a 60-minute real-corpus ingest run
  against `~/.ai-agent/sessions`, captured in worker logs.
- A new regression test in `internal/ingest/` that loads the
  captured failing-case events into an empty SQLite store and
  asserts the entire batch commits without FK errors.
- A short follow-up note in `internal/ingest/` documentation or
  spec explaining the root cause, the fix, and why it does not
  recur.
- SOW-0001 Phase 1 acceptance criterion #3 (full backfill <60 min)
  passes on the operator's corpus.

## Analysis

Sources checked:

- `internal/store/migrations/0001_initial.sql` for the FK
  definitions.
- `internal/ingest/worker.go` for the batching behavior.
- `internal/adapters/aiagent_v3/` and `internal/adapters/aiagent_v2/`
  for the event-emission order.
- SOW-0001 Chunk 11 iter 1 implementation notes for the smoke run
  output.

Current state:

- Backfill against the real corpus fails repeatedly. The
  presenter's `/api/health` continues to answer (parse errors only
  surface in counter form right now), so the operator does not see
  a visible alarm — but events ARE being lost to the FK rejection.
  This is silent failure of the kind AGENTS.md §"No silent failures"
  forbids.

Risks:

- Schema-level fix (drop FK, defer FK, ON DELETE CASCADE) is the
  wrong instinct — it hides the bug. The FK is doing its job: it
  is catching a real ordering or dedup defect.
- Investigation needs structured logging of every event emit
  attempt + every writer insert attempt; this should be done with
  a small subset (e.g. first 100 v2 sessions) so the logs stay
  legible.

## Pre-Implementation Gate

Status: ready — root cause confirmed by master investigation on
2026-05-27 (below). Implementation plan finalized. Spec deltas land
first (ingester.md + data-model.md), then a failing regression test,
then migration + code.

### Confirmed root cause (2026-05-27, master investigation)

The codex Chunk-11 hypothesis correctly identified the scalar-HWM
dedup as the culprit but framed it as **v2-specific**. Direct code
reading shows the defect is **more general — it affects BOTH adapters**
and the precise trigger is the mismatch between a *per-source* scalar
high-water-mark and *per-file* SourceSeq sequencing under a single
aggregating `sourceID`.

Evidence (file:line, verified by reading master at the Chunk-11 merge):

- `internal/ingest/dedup.go:65-69` — `IsAfter` is a scalar compare:
  `return seq > c.hwm[sourceID]`. One HWM value per `sourceID`.
- `internal/ingest/worker.go:133` — every event with
  `!hwm.IsAfter(sourceID, seq) && seq != 0` is dropped before any SQL.
- `internal/ingest/worker.go:197-199` — after each commit the HWM is
  advanced to the batch's max SourceSeq.
- `cmd/ai-viewer-ingest/sources.go` + the Chunk-11 integration smoke —
  the ingester assigns ONE `sourceID` per source root, e.g.
  `aiagent_v3:/<root>/.ai-agent/sessions`, covering *all* session
  files under that directory.
- v2: `internal/adapters/aiagent_v2/mapper.go:625-642` —
  `SourceSeq = FNV-64(originId::path)`, a content hash. Non-monotonic
  by design (stable identity for idempotent re-scans).
- v3: `internal/adapters/aiagent_v3/mapper.go:11-12,58-62` —
  `SourceSeq = packSeq(ledgerSeq, subIdx) = ledgerSeq<<12 | subIdx`,
  documented as "monotonic per **file**". Each session file's ledger
  starts at seq 1, so `packSeq(1,0)` from file A and `packSeq(1,0)`
  from file B are equal, and across files the values are NOT
  monotonic.

Because one `sourceID` aggregates hundreds/thousands of independently
sequenced files, the scalar per-source HWM is wrong for BOTH formats:
once events from one file advance the HWM, lower-valued events from
another file (v3) or any lower-hash event (v2) are silently dropped.
When a dropped event is an `OpStartedEvent` (creating the `ops` row)
but a sibling/child event that happens to carry a higher SourceSeq
survives, the child's insert into `payload_refs` / `log_entries`
trips `FOREIGN KEY constraint failed (787)` because the parent `ops`
row was never written. This matches the Chunk-11 smoke producing FK
errors on BOTH v2 AND v3 sources — which the v2-only hypothesis could
not explain.

Writer insert semantics (verified, `internal/ingest/writer.go`):
- `sessions` (233), `turns` (325/347/375), `ops` (404) all use
  `ON CONFLICT ... DO UPDATE/NOTHING` — already idempotent.
- `payload_refs` (667) and `log_entries` (632/702/736) use plain
  `INSERT` — NOT idempotent. These are the only two tables that both
  (a) carry the FK to `ops` and (b) duplicate rows on re-emit.

### Fix decision (CTO call, 2026-05-27)

Two-part fix. Both parts are required; neither alone is sufficient.

1. **Remove the scalar-HWM event-drop dedup.** A per-source scalar
   high-water-mark is structurally incompatible with per-file
   sequencing under an aggregating `sourceID`, for every current and
   future multi-file adapter. Resume-skipping is already the adapter
   **cursor's** responsibility (v3 per-file byte offsets in
   `Cursor.Files`; v2 per-file content hashes), which Chunk 11 now
   loads on startup and passes to `Scan`. The scalar HWM is therefore
   redundant for resume AND wrong for dedup — delete the event-drop.
   The `SourceProgressEvent` SourceSeq=0 carve-out and cursor
   persistence are unaffected (cursor lives in `source_progress.cursor`,
   a different column from the now-removed `last_seq` watermark use).

2. **Make `payload_refs` and `log_entries` writes idempotent** so
   re-emission (restart resume of a changed file, or a Tail re-read on
   mtime advance) never duplicates rows. Migration `0003` adds a
   natural-identity uniqueness key to each table and the inserts become
   `ON CONFLICT DO NOTHING`. This promotes the project's "Idempotent
   ingest" invariant from a fragile dedup-layer optimization to a
   SQL-layer guarantee that holds regardless of event ordering.

After the fix, correctness rests on: (a) adapter cursors skip already
processed files/offsets on resume; (b) within one Scan each file is
emitted once in adapter order (`OpStarted` before its
`PayloadRef`/`LogEntry`), so the parent `ops` row always exists before
any child insert in the same batch; (c) SQL-level idempotency absorbs
any re-emission. No global watermark required.

Rejected alternatives:
- *Schema FK relaxation / defer / cascade* — hides the bug; the FK is
  correct (SOW "Out of scope").
- *Per-file HWM keyed by (sourceID, fileKey)* — the event carries no
  file key; would require an adapter-contract change and duplicates
  the cursor's existing per-file tracking.
- *Format-aware "monotonic" flag (v3 keeps HWM, v2 skips)* — rejected
  because v3 is ALSO non-monotonic at the source level (per-file
  ledgerSeq under one sourceID); the flag would leave v3 broken.
- *Tolerate missing-parent on child insert (skip/buffer)* — hides data
  loss; forbidden by AGENTS.md "No silent failures".

### Spec deltas to land FIRST (before tests/code)

- `.agents/sow/specs/ingester.md` — replace the scalar-HWM dedup
  description with: resume is cursor-driven (per-file); event-level
  idempotency is a SQL-layer guarantee via idempotent upserts on every
  table. Document that `source_progress.last_seq` is retained as an
  observability counter only (max SourceSeq seen), not a dedup gate.
- `.agents/sow/specs/data-model.md` — document migration `0003`: the
  natural-identity unique indexes on `payload_refs` and `log_entries`,
  and the `ON CONFLICT DO NOTHING` insert contract.

### Implementation plan (spec → test → code)

1. Spec deltas above (lead the commit).
2. Failing regression tests (RED):
   - v2 parent-below/child-above-HWM (the exact shape already
     specified in `### Primary hypothesis` below) — asserts the `ops`
     row exists, the `payload_refs` row exists, and no FK error logged.
   - v3 cross-file interleaving: two synthetic v3 files under one
     sourceID, file A drives the HWM high, file B's `OpStarted` +
     `PayloadRef` land in a later batch; assert both rows exist.
   - Re-scan idempotency: ingest the same fixture twice through the
     full worker; assert `payload_refs` / `log_entries` row counts are
     identical after the second pass (no duplicates).
3. Migration `0003_idempotent_children.sql` + writer `ON CONFLICT`.
4. Remove the scalar-HWM event-drop in `worker.go` / `dedup.go`;
   retain `last_seq` write for observability only (or drop its dedup
   use and keep the column). Update/replace the Chunk-7 dedup tests
   that asserted the old drop behavior — they were pinning the broken
   mechanism.
5. Real-corpus smoke: re-run `ai-viewer-ingest` against
   `~/.ai-agent/sessions`; assert zero FK errors in the worker log and
   non-zero `ops` + `payload_refs` row counts.

### Required investigation (superseded)

The original "add debug logging, run against 100 sessions, classify
against four hypotheses" investigation block below is SUPERSEDED by
the direct code-reading root-cause confirmation above. The four
hypotheses collapse into one: per-source scalar HWM vs per-file
sequencing. Hypothesis (4) "v2 adapter missing OpStarted on some code
path" was checked and rejected — v2 `mapOp` emits OpStarted for every
op kind including `system` (see `internal/adapters/aiagent_v2/mapper.go`).

The original blocked-gate text and four-hypothesis list are preserved
verbatim below for the audit trail.

---

## Pre-Implementation Gate (original, superseded 2026-05-27)

Status: blocked — needs investigation before the implementation
plan can be finalized. The SOW is filed in `pending/` so it is
visible; the next assistant to pick it up MUST start with the
"Required investigation" block below before writing any code.

### Primary hypothesis (added 2026-05-27, codex Chunk 11 iter-1 review)

**v2 non-monotonic SourceSeq + scalar HWM dedup drops valid events,
including the parent `OpStarted` row that subsequent `PayloadRef`
events require.**

Evidence chain (file:line citations):

- `internal/adapters/aiagent_v2/mapper.go:621-642`: every v2 event's
  `SourceSeq` is `FNV-64(originID + "::" + path)`. FNV-64 is a fast,
  uniformly-distributed 64-bit hash — values are effectively random
  in `[0, 2^63)`. The function is correct as a stable identifier (the
  same opTree node always hashes to the same value) but it has NO
  monotonicity guarantee across the events emitted from one v2 file.
- `internal/ingest/worker.go:133`: the dedup check is
  `if !w.hwm.IsAfter(ev.EventSourceID(), ev.EventSourceSeq()) && ev.EventSourceSeq() != 0 { continue }`.
  `hwm.IsAfter` (see `internal/ingest/dedup.go:65`) is a scalar
  `seq > c.hwm[sourceID]` comparison.
- `internal/ingest/worker.go:206-208`: after every committed batch
  the worker advances HWM to `wr.batchMaxSeq` — the largest
  `SourceSeq` it saw in that batch.

Failure mechanism:

1. First v2 batch from a fresh DB contains, say, an `OpStartedEvent`
   with `SourceSeq = 0xFFFFFFFFAAAA` (high random hash) and other
   events. The batch commits successfully.
2. HWM advances to `0xFFFFFFFFAAAA`.
3. A subsequent batch contains a follow-up `OpStartedEvent` for a
   DIFFERENT op whose path happens to hash to a smaller value, say
   `0x000000005555`. The dedup check `seq > hwm` returns false. The
   `OpStartedEvent` is silently dropped.
4. Later events emitted for the SAME op — `OpFinalizedEvent`,
   `PayloadRefEvent`, `LogEntryEvent` — may have other hashes; some
   pass the dedup, some don't.
5. Whichever child event passes the dedup hits the writer first.
   `PayloadRefEvent` inserts into `payload_refs` with a `op_id`
   derived from `canonicalOpID(turnID, opSeq)` — but no `ops` row
   exists for that opID because `OpStartedEvent` was dedup-dropped.
   FK fails: `FOREIGN KEY constraint failed (787)`.

This is the EXACT pattern seen in the Chunk 11 iter-1 smoke run.

Why v3 likely escapes the same trap (but the smoke shows v3 errors
too — caveat):

- `internal/adapters/aiagent_v3/mapper.go` packs `ledgerSeq << 12 |
  subIdx` into SourceSeq. ledgerSeq is monotonic per file and subIdx
  is a sub-event counter. Two events from the same v3 file produce
  monotonically increasing SourceSeq values, so a scalar HWM never
  rejects a later event.
- HOWEVER, the iter-1 smoke captured 5 v3 FK errors as well. Either
  (a) v3 has a separate ordering defect (e.g. ledgerSeq is not
  strictly increasing across files, OR the cross-batch ordering of
  events from different sessions allows a child to land before its
  parent and the writer dropped the parent for another reason), or
  (b) the FK failures cited in the smoke are dominated by v2 and
  the v3 ones are a different class. The investigation must classify
  each failure by adapter before claiming v2 fix alone closes the
  SOW.

Acceptance criterion (concrete, to be added to the regression
suite):

- A new test in `internal/ingest/worker_test.go` (or a new
  `dedup_v2_test.go`) MUST be designed so that the parent
  (`OpStartedEvent`) falls BELOW the worker's high-water-mark and
  the child (`PayloadRefEvent`) falls ABOVE it. That asymmetry is
  the only sequence that actually surfaces the FK bug — if BOTH
  parent and child are below HWM, both get dedup-dropped and the
  test passes by accident, proving nothing. (Codex iter-3 P2#4
  flagged the earlier draft test for exactly this design flaw.)
- Concrete shape (sourceID `s`):
  1. **Batch 1** carries a single warm-up event whose
     `SourceSeq = 0x000000FFFFFF` — chosen specifically to seed the
     scalar HWM at that value after a successful commit.
  2. **Batch 2** carries the synthetic op chain:
     - `OpStartedEvent` for op `O`,
       `SourceSeq = 0x0000000000A0` → BELOW the HWM. Under the bug
       this event is dedup-dropped and the `ops` row is never
       inserted.
     - `PayloadRefEvent` for op `O`,
       `SourceSeq = 0x0000FFFFFF00` → ABOVE the HWM. Under the bug
       this event reaches the writer and trips
       `FOREIGN KEY constraint failed (787)` because the parent
       `ops` row does not exist.
- Assertions (post-run, after the worker commits or rolls back):
  - `SELECT COUNT(*) FROM ops WHERE id = canonicalOpID(O)` MUST be
    `1`. Under the bug it is `0` because OpStarted was dropped.
  - `SELECT COUNT(*) FROM payload_refs WHERE op_id = canonicalOpID(O)`
    MUST be `1`. Under the bug it is `0` because the insert failed
    on FK rollback.
  - Worker logs MUST NOT contain `FOREIGN KEY constraint failed`.
- Today this test FAILS three ways simultaneously (ops count 0,
  payload_refs count 0, FK error in the log). After the fix all
  three assertions hold. The first assertion (`ops` row exists)
  is the critical one — it proves the parent event was NOT
  dedup-dropped — which is the actual class of bug. Absence-of-FK
  alone is insufficient because a buggy fix that dedup-drops BOTH
  parent and child would also produce zero FK errors.

Hypotheses to investigate after the primary is confirmed or
rejected (mutually compatible — the cause could be one or several):

1. **Batch split rollback**: an `OpStarted` lands in batch A and
   the corresponding `PayloadRef` lands in batch B. Batch A failed
   to commit (e.g. on a different row's FK or constraint) and was
   rolled back, but batch B's PayloadRef proceeds to a writer
   transaction that has no record of the op.

2. **Concurrent adapter goroutine canonicalOpID collision**: two
   adapter goroutines emit events whose `canonicalOpID` collide,
   the first OpStarted is deduped, and a follow-up child references
   the deduped op that the writer never inserted.

3. **OpStarted dedup race** (subsumed by the primary hypothesis
   above but kept here for completeness): the writer's HWM was
   advanced before the OpStarted committed; a follow-up `PayloadRef`
   in a new batch is the first event the writer sees for that
   `op_id` because the OpStarted was deduped against the (stale)
   HWM.

4. **v2 adapter missing OpStarted on some code path**: the v2
   adapter's `mapOp` may not emit `OpStarted` for some `kind`
   (e.g. `system` ops). The PayloadRef then references an op that
   was never created.

Note on fix scope: the v2 HWM defect is non-trivial to fix.
Candidates include (a) per-source-format dedup strategies (v2 uses
a different key entirely, e.g. set membership instead of scalar
HWM), (b) changing v2 SourceSeq to a monotonic counter (breaks the
"stable identifier" property the v2 adapter relies on for idempotent
replays — needs careful audit), or (c) splitting the dedup key from
the HWM advance so a smaller-hash event is not blocked by a larger
prior hash. The fix is OUT OF SCOPE for SOW-0001 Chunk 11 per the
Chunk 11 iter-3 brief.

Evidence reviewed (so far):

- The five-minute integration smoke output from SOW-0001 Chunk 11
  iter 1 — needs to be re-captured with structured per-event +
  per-insert logging.
- `internal/adapters/aiagent_v2/mapper.go` — needs a dedicated
  code-path audit for the four hypotheses above.

Affected contracts and surfaces:

- `internal/ingest/worker.go` — batch boundary and retry logic.
- `internal/store/writer.go` — FK-enforced insert paths.
- `internal/adapters/aiagent_v2/` — event-emission ordering.
- `internal/adapters/aiagent_v3/` — likely correct, but verify.
- `/api/health.parse_errors` — must reflect FK failures so silent
  loss becomes visible.

Existing patterns to reuse:

- `internal/ingest/worker_test.go` for the regression-test pattern.
- `internal/adapters/aiagent_v2/golden_test.go` for the
  failing-case fixture pattern.

Risk and blast radius:

- Investigation: low. Just adds logging.
- Fix: depends on root cause. A batch-split fix touches `worker.go`
  (boundary logic). A dedup-race fix touches `writer.go` (HWM
  advancement ordering). An adapter omission fix touches the
  adapter only.

Sensitive data handling plan:

- The captured failing-case fixture MUST be sanitized per the
  AGENTS.md "Sensitive Data In Durable Artifacts" rules before it
  lands under `testdata/`. Originals stay in the operator's
  scratch directory; only the redacted form is committed.

Required investigation (before fix):

1. Add structured DEBUG-level logging: every event emitted (adapter
   + canonicalOpID + SourceSeq + kind), every writer insert attempt
   (table + op_id + outcome).
2. Run `ai-viewer-ingest` against the first ~100 v2 sessions of the
   operator's corpus on an empty database; capture the full log to
   a file.
3. Identify the first failing case: the offending `op_id`, the full
   event chain that produced it, and the state of the writer's HWM
   at the time of failure.
4. Classify against the four hypotheses above.

Out of scope:

- The schema's FK structure. The FK is correct; this is an
  event-ordering or dedup defect.
- Phase 2 features (auth, remote deployment, etc.).

## Implementation (2026-05-27)

Branch: `sow-0015-ingest-dedup-idempotency`.

### Files changed

Specs (landed first):
- `.agents/sow/specs/ingester.md` — replaced §Dedup with §"Dedup and
  Idempotency": no scalar-HWM event-drop; resume is cursor-driven;
  event-level idempotency is a SQL-layer guarantee; `last_seq` is an
  observability counter only. Updated lifecycle/Start/Batching wording.
- `.agents/sow/specs/data-model.md` — documented migration `0003`
  (`idx_payload_refs_identity`, `idx_log_entries_identity`), the
  `ON CONFLICT DO NOTHING` insert contract, the `last_seq`
  observability-only role, the schema-version bump to `3`, the migration
  history, and the disposable-DB note (one-time `rm index.db` if an
  operator already has pre-`0003` duplicates).

Migration:
- `internal/store/migrations/0003_idempotent_children.sql` — new. Two
  natural-identity UNIQUE indexes + `INSERT OR REPLACE schema_meta
  version='3'`.

Code:
- `internal/ingest/writer.go` — `payload_refs` INSERT now
  `ON CONFLICT (op_id, kind, location_uri) DO NOTHING`; all three
  `log_entries` INSERTs append a shared `logEntryOnConflict` clause
  matching the expression index.
- `internal/ingest/worker.go` — removed the three `hwm.IsAfter` event-drop
  checks (main loop + two shutdown-drain copies). Every event now flows to
  the writer. `batchMaxSeq` tracking and `Advance`/`last_seq` persistence
  retained for observability.
- `internal/ingest/dedup.go` — removed unused `IsAfter`; re-documented
  `hwmCache` as an observability counter (not a dedup gate).
- `internal/ingest/doc.go` — rewrote the §Dedup package doc.
- `internal/presenter/presenter.go` — `SchemaVersion` const `1 → 3` in
  lockstep with migration 0003 (server refuses to start on mismatch).

Tests:
- `internal/ingest/dedup_orphan_test.go` — new. Four regression tests
  (v2 parent-below/child-above, v3 cross-file interleaving, re-scan
  idempotency, two-interleaved-files-both-persist).
- `internal/ingest/dedup_test.go` — removed `TestHWMCache_IsAfter`
  (method deleted); kept Load/Advance/Get counter tests.
- `internal/ingest/worker_test.go` — replaced `TestWorker_DedupHWMDropsReplays`
  (pinned the broken drop) with `TestWorker_LowSeqEventsNotDropped`
  (pins the new no-drop contract + counter advance).
- `internal/store/{store_test.go,migrations_test.go}` —
  `expectedMigrations 2 → 3`, `schema_meta.version "1" → "3"`.
- `internal/store/schema_contract_test.go` — added the two new UNIQUE
  indexes to the `payload_refs` / `log_entries` contract.

### dedup_test.go disposition

`TestHWMCache_IsAfter` deleted (the `IsAfter` method it tested is gone).
`TestWorker_DedupHWMDropsReplays` (worker_test.go) replaced — it pinned
the broken scalar-HWM drop. Net test count for the dedup/idempotency
behavior INCREASED: 1 removed unit test + 1 replaced worker test, against
4 new regression tests + 1 replacement worker test. Lost positive
coverage (two interleaved files both persisting) is covered by
`TestWorker_TwoInterleavedFilesBothPersist`.

### Natural-key choices + justification

- `payload_refs`: UNIQUE `(op_id, kind, location_uri)`. Verified against
  both adapters: each op emits at most one request and one response
  payload, each with a distinct `kind` (`payloadKindForSide` in v2;
  per-ref `kind` in v3). Replaying the same payload collides; distinct
  payloads do not. No wider key needed.
- `log_entries`: UNIQUE expression index over
  `(COALESCE(session_id,''), COALESCE(source_id,''), COALESCE(op_id,''),
  ts, severity, source, message)`. COALESCE sentinels because raw SQL
  NULLs are distinct in a UNIQUE index — without them, re-emitted
  source-level (session NULL) parse-error / pricing-miss rows would
  duplicate. `message` indexed directly (no `message_hash` column):
  log_entries holds only short structured lines (parse errors,
  pricing-miss warnings); payload bodies live in `payload_refs`, never
  here, so the b-tree stays small and a hash column would add
  schema/write complexity for no benefit.

### Failing-then-passing evidence

RED (against master, before the fix) — `go test -run '<4 tests>' ./internal/ingest/`:

```
--- FAIL: TestWorker_TwoInterleavedFilesBothPersist
    interleaved files did not both persist: ops=1 payload_refs=0 (want 2/2)
--- FAIL: TestWorker_OrphanFK_V2ParentBelowChildAbove
    orphan FK bug present: ops=0 payload_refs=0 (want 1/1)
--- FAIL: TestWorker_OrphanFK_V3CrossFileInterleaving
    v3 cross-file orphan FK bug present: ops=0 payload_refs=0 (want 1/1)
```

(`TestWorker_ReScanIdempotency` passed against master only because the
scalar HWM masked the re-emission; a mutation check — neutering the 0003
indexes — makes it FAIL `pass 1 payload_refs = 0`, proving the SQL-layer
dedup now does the work.)

GREEN (after the fix):

```
--- PASS: TestWorker_ReScanIdempotency
--- PASS: TestWorker_TwoInterleavedFilesBothPersist
--- PASS: TestWorker_LowSeqEventsNotDropped
--- PASS: TestWorker_OrphanFK_V2ParentBelowChildAbove
--- PASS: TestWorker_OrphanFK_V3CrossFileInterleaving
ok  github.com/netdata/ai-viewer/internal/ingest
```

### Real-corpus smoke

Built the ingest binary and ran it against `~/.ai-agent/sessions` on a
FRESH temp DB (`--db`/`--state-dir` under a scratch dir; the operator's
real DB untouched), ~70–75 s each, for both formats:

| source | FK failures | batch failures | sessions | ops | payload_refs | log_entries |
|---|---|---|---|---|---|---|
| aiagent_v2 | 0 | 0 | 8 544 | 63 556 | 55 | 186 295 |
| aiagent_v3 | 0 | 0 | 14 597 | 72 218 | 90 351 | 9 056 |

`schema_meta.version=3`; all three migrations applied. v2's low
payload_refs is expected (v2 mostly uses inline payloads, not ref-form —
ref extraction is deferred per data-model.md §Canonical Model Gaps). The
acceptance criterion is met on both formats: zero FK constraint failures,
non-zero `ops` and `payload_refs`.

### Gate results

- `go mod tidy` — no diff.
- `gofmt -l .` / `goimports -l .` — zero output.
- `go vet ./...` — clean.
- `go build ./...` — both binaries build.
- `go test -race -count=1 ./...` — all pass.
- Coverage: `internal/ingest` 91.8%, `internal/store` 90.9% (both ≥ 90).
- `golangci-lint run --timeout=5m` — 0 issues.
- `gosec -severity medium -confidence medium ./...` — 0 issues.
- `shellcheck -x -s bash scripts/*.sh scripts/lib/*.sh scripts/test/*.sh` — clean.
- `sanitize-fixture-test.sh` 13/13, `pricing-merge-test.sh` 61/61,
  `refresh-pricing-test.sh` 20/20.

No new `//nolint`/`//nosec` added.

### Iteration 2 (review findings, 2026-05-27)

Three reviewers ran on iter-1: codex (1 P2 + 2 P3), glm (2 low-risk P2,
both doc-only), minimax (CLEAN). Every actionable finding addressed below.

- **iter2-1 — P2 (codex): `log_entries` idempotency key omitted `turn_id`
  → false-dedup / silent data loss.** codex flagged that
  `idx_log_entries_identity` and the matching `logEntryOnConflict` target
  excluded `turn_id`, while `applyLogEntry` (`internal/ingest/writer.go`
  ~705) persists `turn_id` for `TurnSeq > 0` and leaves `op_id` NULL when
  `OpSeq == 0`. v3 emits turn-scoped warnings/errors with `turn_id` set
  but no op (`internal/adapters/aiagent_v3/mapper.go` ~198). So two
  genuinely distinct logs in the same session, different turns, identical
  `ts/severity/source/message`, `op_id` NULL, collided — the second was
  silently dropped, the exact data-loss class this SOW exists to kill.
  Notably minimax had cleared the change and glm had verified the
  `log_entries` key as "correct"; only codex caught this.
  Fix: added `COALESCE(turn_id, '')` (positioned after the `op_id`
  COALESCE) to the expression index in
  `internal/store/migrations/0003_idempotent_children.sql` (edited in
  place — 0003 is uncommitted), and to the `logEntryOnConflict` ON CONFLICT
  target in `internal/ingest/writer.go` — character-for-character matched
  to the index expression (SQLite requires the conflict target to equal
  the index expression). Synced the `data-model.md` index definition.
  `internal/store/schema_contract_test.go` now expects four leading empty
  strings (session, source, op, turn COALESCE) instead of three.
- **iter2-2 — P3 (codex): stale "monotonic-per-source dedup key" wording.**
  Updated `.agents/sow/specs/canonical-events.md:11` and the
  `EventSourceSeq()` interface-method comment in
  `internal/canonical/events.go` (~104, distinct from the already-fixed
  `EventBase.SourceSeq` field comment) to "monotonic per file;
  observability counter, NOT a dedup key".
- **iter2-3 — P3 (codex): `/api/health` spec `schema_version`/`last_seq`
  drift.** `schema_version` `1 → 3` in `.agents/sow/specs/rest-api.md`
  and `.agents/sow/specs/observability.md` (matches
  `presenter.SchemaVersion = 3`); reworded `last_seq` from "opaque
  high-water mark" to "per-source observability counter = max SourceSeq
  seen; NOT a dedup gate" in both.
- **iter2-4 — P2 (glm, doc-only): `payload_refs` `DO NOTHING` keeps old
  metadata.** glm assessed LOW risk (payloads are immutable once written;
  a rewritten payload lands at a new `location_uri`/ledger record, never
  an in-place mutation). Documented as an intentional limitation in
  `.agents/sow/specs/data-model.md` near the migration-0003
  `idx_payload_refs_identity` block, with the `DO UPDATE` escape hatch
  noted for a hypothetical future in-place-mutating adapter. No code
  change.

New regression test `TestWorker_LogEntryTurnScopedNotFalseDeduped`
(`internal/ingest/dedup_orphan_test.go`): two turn-scoped logs (op_id
NULL, different turns, identical ts/severity/source/message) must BOTH
persist, plus a true re-emit (identical turn_id) must still collapse to
one row.

- RED (mutation: `turn_id` removed from index + conflict target):
  `log_entries = 1, want 2 (turn_id missing from idempotency key)` — the
  second turn's log false-deduped. Confirms the fix is load-bearing.
- GREEN (fix in place): `--- PASS: TestWorker_LogEntryTurnScopedNotFalseDeduped`;
  COUNT(*) == 2 distinct rows, re-emit stays at 2.

Iter-2 gate results (from `$REPO_ROOT`):

- `gofmt -l .` / `goimports -l .` — zero output.
- `go vet ./...`, `go build ./...` — clean; both binaries build.
- `go test -race -count=1 ./...` — all pass (new turn_id test included).
- Coverage: `internal/ingest` 91.8%, `internal/store` 90.9% (both ≥ 90).
- `golangci-lint run --timeout=5m` — 0 issues.
- `gosec ./...` — 0 issues (55 files, 0 findings).

Iter-2 real-corpus smoke (fresh temp DB under a scratch dir, operator's
real DB untouched, ~75 s each, with the turn_id index change applied):

| source | FK failures | error batches | sessions | ops | payload_refs | log_entries | schema |
|---|---|---|---|---|---|---|---|
| aiagent_v2 | 0 | 0 | 8 682 | 64 630 | 55 | 189 524 | 3 |
| aiagent_v3 | 0 | 0 | 15 516 | 78 351 | 98 409 | 9 881 | 3 |

Still zero FK constraint failures on both formats after the index change;
counts are marginally higher than iter-1 (corpus grew between runs). The
ON CONFLICT target matches the `idx_log_entries_identity` index
expression character-for-character.

### Iteration 3 (spec-drift sweep, 2026-05-27)

iter-2 reviewers: minimax CLEAN; glm found 2 doc-only spec-drift spots
(ingester.md §Layer 2 index prose still omitted `turn_id`; rest-api.md
`/api/sources` still called `last_seq` a "high-water mark"); codex
timed out (stdin-detach hang, exit 124 — re-run in iter-3 with stdin
redirected from /dev/null). Master applied the spec fixes directly
(`.agents/sow/specs/**` is master-writable) and, while sweeping, found
THREE more stale HWM-as-dedup mentions the reviewers had not flagged:

- `ingester.md` §Layer 2 — added `COALESCE(turn_id,'')` to the
  log_entries index prose + rationale.
- `rest-api.md` `/api/sources` — `last_seq` reworded to "observability
  counter = max SourceSeq seen; NOT a dedup gate and NOT a portable
  event count".
- `adapter-aiagent-v2.md:247,321` — two passages still said "the
  ingester's high-water-mark logic deduplicates" / "compares each event
  against the per-source high-water-mark ... only NEW events pass
  through". Rewritten to the SQL-layer-idempotency model (SOW-0015).
- `adapter-aiagent-v3.md:576` — "`cursor.lastSeq` is the high-water mark
  per session: events with `SourceSeq <= hwm` are discarded by the
  ingester" rewritten to cursor-driven byte-offset resume + SQL-layer
  idempotency.

After iter-3 the only remaining `high-water` mentions in specs are
`ingester.md`'s "Why no scalar high-water-mark" explanation (correct).
No code changed in iter-3 — doc-only. Gates re-run green; real-corpus
smoke unaffected (code identical to iter-2).

### Iteration 4 (extras_json key + comprehensive HWM-wording sweep, 2026-05-27)

iter-3 reviewers: codex found 1 real P2 (`extras_json` false-dedup) + P3
spec/comment drift; glm reported convergence + a P3 stale-wording note.
This iteration closes the P2 AND sweeps every stale HWM / dedup-as-gate
mention so future rounds find nothing.

**iter4-1 — P2 (codex): `log_entries` idempotency key omitted
`extras_json` → false-dedup / silent data loss.** `applyLogEntry`
(`internal/ingest/writer.go`) persists `extras_json`, but the
`idx_log_entries_identity` index + `logEntryOnConflict` target excluded
it. v2 stores the source `path` in extras
(`internal/adapters/aiagent_v2/mapper.go`), so two logs on the same owner
with identical (ts, severity, source, message) but different extras
collapsed under `ON CONFLICT DO NOTHING` — the same data-loss class as the
iter-2 `turn_id` bug. Verified `extras_json` was the ONLY persisted
`log_entries` content column outside the key; adding it makes the key
cover EVERY persisted column, so a duplicate is now a byte-identical row.
Fix:
- Added `COALESCE(extras_json, '')` as the LAST keyed column to
  `idx_log_entries_identity` in
  `internal/store/migrations/0003_idempotent_children.sql` (edited in
  place — 0003 uncommitted) and to the `logEntryOnConflict` ON CONFLICT
  target in `internal/ingest/writer.go`, character-for-character matched.
- `internal/store/schema_contract_test.go`: `idx_log_entries_identity`
  expected cols gained a trailing `""` (9 entries: 4 leading COALESCE,
  `ts, severity, source, message`, 1 trailing COALESCE).
- Synced `data-model.md` + `ingester.md` index definitions.
- **Invariant made explicit** to prevent this exact regression class: a
  comment above `logEntryOnConflict` in `writer.go` and a mirror in
  `data-model.md` state the conflict target MUST list every persisted
  `log_entries` content column, and any new column must be added to both
  the index and the ON CONFLICT target in the same commit.

New regression test `TestWorker_LogEntryExtrasNotFalseDeduped`
(`internal/ingest/dedup_orphan_test.go`): two session-scoped logs, same
owner/ts/severity/source/message, DIFFERENT `Extras` (`path`) → BOTH
persist (COUNT == 2); plus a true re-emit (identical extras) collapses to
1 (stays at 2 total). `marshalExtras` uses `json.Marshal` on
`map[string]any`, whose key-sorted output makes identical maps yield
byte-identical JSON, so re-emits still collapse.

- RED (mutation: `extras_json` removed from index + conflict target):
  `extras-distinct logs false-deduped: log_entries = 1, want 2 (extras_json
  missing from idempotency key)` — confirms the fix is load-bearing.
- GREEN (fix in place): `--- PASS: TestWorker_LogEntryExtrasNotFalseDeduped`.

**iter4-2 — comprehensive stale-wording sweep.** Fixed every mention that
described the removed per-source scalar HWM as an active dedup gate, the
never-implemented "dedup by (source_id, source_seq)" model, or v2
SourceSeq as "monotonic per source". The correct model: resume is
cursor-driven (per-file); event-level dedup is a SQL-layer guarantee via
idempotent upserts keyed on each table's natural identity; `last_seq` /
the `hwmCache` is an observability counter (max SourceSeq seen), never a
gate.

Specs changed:
- `adapter-contract.md` — Cursor.After "HWM checks" → "resume-ordering
  comparison".
- `adapter-aiagent-v2.md` — `SourceSeq` "monotonic per source" →
  "stable per-(file, opTree-node) identifier ... NOT monotonic per source
  and NOT a dedup gate"; "the ingester's HWM dedup absorbs duplicates" →
  "SQL-layer idempotent upserts"; "the ingester's HWM and stable
  SourceSeq ensure no duplication" → "SQL-layer idempotent upserts (keyed
  on natural identity)".
- `architecture.md` — box "dedup by (source_id, source_seq)" → "idempotent
  upserts (natural-identity dedup at the SQL layer)"; failure-handling
  "dedup is by (source_id, source_seq)" → "natural-identity idempotent
  upserts ... (source_id, source_seq) is NOT the dedup key".
- `ingester.md` — §Layer 2 log_entries index prose gained
  `COALESCE(extras_json,'')` + the byte-identical-row rationale.
- `data-model.md` — index definition + invariant (above).

Code comments changed:
- `internal/canonical/adapter.go` — package-level "deduped by the ingester
  via SourceSeq" → "made idempotent ... via SQL-layer natural-identity
  upserts (NOT a SourceSeq dedup gate)"; Cursor.After "high-water-mark
  checks on resume" → "resume-ordering comparison".
- `internal/canonical/doc.go` — "enforces dedup and ordering by (SourceID,
  SourceSeq)" → SQL-layer natural-identity idempotency; SourceSeq is an
  observability counter / replay identifier, not a dedup key.
- `internal/adapters/aiagent_v3/{cursor.go,adapter.go,tailer.go}` — After
  "high-water-mark checks" → "resume-ordering comparison"; sourceID
  "dedup keyed on (SourceID, SourceSeq)" → SQL-layer natural identity;
  flushDirty "new high-water-mark" → "new max SourceSeq (observability
  counter)".
- `internal/adapters/aiagent_v2/{mapper.go,doc.go}` — "enabling the
  ingester's HWM dedup" / "SourceSeq HWM absorbs duplicates" → "SQL-layer
  idempotent upserts".
- `internal/presenter/{sources.go,health.go}` + the matching test
  comments (`sources_test.go`, `health_test.go`) — `last_seq` "opaque
  per-source high-water mark" → "observability counter (max SourceSeq
  seen); NOT a dedup gate".
- `internal/store/migrations/0002_source_progress.sql` — header reworded
  `last_seq` from "monotonic-watermark used by the ingester to discard
  re-emitted events" to "observability sequence counter ... NOT a dedup
  gate".
- `internal/ingest/{writer.go,ingester.go}` — `batchMaxSeq` field doc,
  writer struct doc, and `Ingester.HWM()` method doc now state these are
  the observability counter (max SourceSeq seen), NOT a dedup gate.

**Deliberate no-rename (documented):** the internal `hwmCache` type, its
fields, and `Ingester.HWM()` were NOT renamed — that would be out-of-scope
churn touching unrelated call sites and tests. Their doc comments now make
the observability-counter role explicit (`dedup.go` struct doc already
said "It is NOT a dedup gate"; `ingester.go` HWM() doc updated this
iteration). `dedup.go`'s top doc remains the authority for the `hwm*`
identifiers.

Final sweep grep (`grep -rniE "high-water|\bHWM\b|monotonic per
source|dedup.*by.*source_seq|deduped by the ingester via SourceSeq"
.agents/sow/specs internal cmd --include='*.go' --include='*.md'`) — every
remaining hit is one of: (a) `ingester.md` §"Why no scalar
high-water-mark" explanation + `adapter-aiagent-v2.md:321` /
`adapter-aiagent-v3.md:576` / `doc.go:21` / `dedup.go:15` / `worker.go:124`
correct "there is no HWM gate" framing; (b) internal `hwm*` identifiers
with corrected observability-counter doc comments
(`dedup.go`, `worker.go`, `ingester.go`); (c) test files
(`dedup_orphan_test.go`, `worker_test.go`, `ingester_test.go`,
`error_paths_test.go`) describing the OLD bug shape they pin against or
calling the retained `HWM()` accessor; (d) the observability-counter
framing in `architecture.md:123` / presenter / migration `0002`; plus the
unrelated `aiagent_v2/bench_test.go:94` "live-bytes high-water mark"
(Go-heap memory, not the dedup HWM). No stale gate-wording remains.

iter-4 gate results (from `$REPO_ROOT`):
- `gofmt -l .` / `goimports -l .` — zero output.
- `go vet ./...`, `go build ./...` — clean; both binaries build.
- `go test -race -count=1 ./...` — all pass (new extras_json test +
  updated schema_contract_test included).
- Coverage: `internal/ingest` 91.8%, `internal/store` 90.9% (both ≥ 90).
- `golangci-lint run --timeout=5m` — 0 issues.
- `gosec ./...` — 0 issues (55 files, 0 findings).

No new `//nolint`/`//nosec` added. ON CONFLICT target matches
`idx_log_entries_identity` character-for-character (9 expressions, same
order).

iter-4 real-corpus smoke (fresh temp DB + state-dir under a scratch dir,
operator's real DB untouched; each format ingested to quiescence then the
specific ingest PID stopped, with the extras_json index change applied):

| source | FK failures | batch failures | sessions | ops | payload_refs | log_entries | schema |
|---|---|---|---|---|---|---|---|
| aiagent_v2 | 0 | 0 | 14 445 | 111 673 | 90 | 320 552 | 3 |
| aiagent_v3 | 0 | 0 | 21 687 | 135 440 | 170 922 | 17 098 | 3 |

Still zero FK constraint failures and zero batch failures on both formats
after the extras_json index change. Counts are higher than iter-2 (corpus
grew between runs).

### Iteration 5 (final doc sweep, 2026-05-27)

iter-4 reviewers: codex flagged the remaining `SourceSeq`-semantics doc/comment
drift (doc-only — NOT a code defect; the fix is correct and complete); glm +
minimax reported convergence (code correct & complete). This iteration is a
comment/prose-only sweep that fixes every spot describing `SourceSeq` as a
dedup gate, ordering key, or unconditionally "monotonic per source/file" so no
future review round surfaces another. **No executable code, SQL, or test logic
changed.**

Codex iter-4 attribution (the spots this iteration closes):
- **P2** — stale "deduped via `SourceSeq`" framing in
  `adapter-contract.md:75` and the "for dedup" / "monotonic per (source,
  session)" contract framing in `adapter-aiagent-v3.md:504`.
- **P3** — `aiagent_v3/mapper.go` comment claiming the packed `SourceSeq`
  "lets the ingester compute global ordering without an extra column".
- **P3** — `canonical/events.go` monotonicity overstatement ("monotonic per
  file" applied to both accessor + field doc, which is false for v2's content
  hash).

The canonical truth re-stated: `SourceSeq` is a deterministic, stable-across-
rescans per-event identifier. v3 packs a monotonic-per-FILE sequence
(`ledgerSeq<<12 | subIdx`); v2 is `FNV-64(originId::path)` — a content hash,
not monotonic even within a file. It is an observability counter (max seen
recorded in `source_progress.last_seq`, surfaced via `/api/health`), NOT a
dedup gate or ordering key. Dedup is a SQL-layer guarantee (every writer table
has a natural-identity `ON CONFLICT` key); resume after restart is the adapter
Cursor's per-file job.

8 spots fixed:

Specs (.md):
1. `adapter-contract.md:75` — Scan/Tail re-emission now "absorbed by the
   ingester's SQL-layer idempotent upserts (natural-identity keys), NOT by a
   `SourceSeq` gate".
2. `adapter-aiagent-v3.md:504` (§5.4) — reframed "stable `SourceSeq` for dedup
   ... monotonic per (source, session)" → deterministic per-(session,
   ledger-line) identifier, observability counter + log-attribution aid, NOT a
   dedup gate or cross-source ordering key. Packing example
   (`ledger_seq * 1000 + sub_event_index`) preserved.
3. `adapter-claude-code.md:507` — table row "monotonic per-source counter" →
   "deterministic per-event identifier (stable across rescans); observability
   counter, not a dedup gate" (adapter not built yet; keeps the contract
   statement accurate).
4. `adapter-opencode.md:380` — "`SourceSeq = monotonic counter`" →
   "deterministic per-event identifier (stable across rescans; observability
   counter, not a dedup gate)".
5. `adapter-opencode.md:485` — compaction re-emit "the ingester deduplicates by
   SourceSeq" → "absorbs the re-emission via SQL-layer idempotent upserts, not
   a `SourceSeq` gate".

Code comments (.go — comment text only):
6. `internal/adapters/aiagent_v3/mapper.go:11-14` — dropped the "lets the
   ingester compute global ordering without an extra column" claim; now states
   the packed value is a stable per-event identifier (observability counter +
   log attribution) and "the ingester does NOT use it for ordering or dedup".
7. `internal/canonical/events.go` — both `EventSourceSeq()` accessor doc and
   the `EventBase.SourceSeq` field doc reworded from "monotonic per file" to
   the full truth: v3 packs a monotonic-per-file sequence, v2 is a content hash
   (not monotonic); observability counter, NOT a dedup gate or ordering key.
8. `internal/ingest/writer.go:487` — pricing-lookup `sql.ErrNoRows` comment
   "ingested in a prior batch that's been pruned, or skipped for dedup" →
   "ingested in a prior batch, or not yet arrived in this scan" (there is no
   event-drop dedup anymore — "skipped for dedup" was stale).

Post-sweep grep (`grep -rniE "SourceSeq.*(dedup|ordering|monotonic|high.water|HWM|watermark)|(deduped|dedups|deduplicat).*SourceSeq|for dedup\b|global ordering|monotonic per source" .agents/sow/specs internal cmd --include="*.go" --include="*.md" | grep -v "_test.go"`):

```
.agents/sow/specs/adapter-contract.md:75:The ingester calls `Scan` first, then `Tail`. The adapter MUST handle the case where new data arrives *during* `Scan`: those events should be picked up by `Tail`; any resulting re-emission is absorbed by the ingester's SQL-layer idempotent upserts (natural-identity keys), NOT by a `SourceSeq` gate. See `ingester.md` §Dedup and Idempotency.
.agents/sow/specs/ingester.md:85:4. `UPDATE source_progress SET last_seq=MAX(last_seq, batch_max_seq), cursor=last_cursor, updated_at=now()`. `last_seq` is an observability counter (max `SourceSeq` seen), not a dedup gate — see §Dedup and Idempotency.
.agents/sow/specs/ingester.md:99:A per-source scalar high-water-mark assumes `SourceSeq` is monotonic
.agents/sow/specs/data-model.md:367:- `last_seq` records the maximum `SourceSeq` observed per source, advanced atomically with the batch that wrote the matching events. It is an **observability counter** surfaced via `/api/health`; the ingester does NOT read it as a dedup gate. A per-source scalar watermark is structurally wrong here because one `sourceID` aggregates many independently-sequenced files (`SourceSeq` is per-file, not per-source) — see `ingester.md` §Dedup and Idempotency.
.agents/sow/specs/data-model.md:368:- `last_ts_us` records the Ts of the most recent observed event for diagnostics; the ingester does not use it for dedup.
.agents/sow/specs/canonical-events.md:11:Defined in `internal/canonical/events.go`. All events carry: `SourceID string`, `SourceSeq uint64` (monotonic per file; observability counter, NOT a dedup key — see §Ordering Guarantees, §Idempotency, and ingester.md §Dedup and Idempotency), `Ts int64` (microseconds UTC).
.agents/sow/specs/canonical-events.md:269:- `SourceSeq` is monotonic **per file**, not per source. The ingester records the max seen per source in `source_progress.last_seq` as an observability counter only — it is NOT used for ordering or dedup.
.agents/sow/specs/canonical-events.md:273:- `SourceSeq` is NOT a dedup gate: one source aggregates many independently-sequenced files, so a per-source scalar watermark would drop valid events (SOW-0015). Idempotency is enforced at the SQL layer — every writer table uses idempotent upserts keyed on a natural identity, so re-emitted events never duplicate rows. See `ingester.md` §Dedup and Idempotency.
.agents/sow/specs/rest-api.md:56:observability counter = max SourceSeq seen; NOT a dedup gate and NOT a
internal/presenter/sources.go:22:// SourceSeq seen); NOT a dedup gate and NOT a portable event count. See
internal/presenter/sources.go:42:// observability counter (max SourceSeq seen; NOT a dedup gate). The
.agents/sow/specs/adapter-aiagent-v3.md:504:Every emitted canonical event carries a stable `SourceSeq` — a deterministic per-(session, ledger-line) identifier (monotonic per file/session, stable across rescans). It is an observability counter and log-attribution aid, NOT a dedup gate or cross-source ordering key (dedup is a SQL-layer guarantee; see `ingester.md` §Dedup and Idempotency). The adapter constructs it from the ledger's `(sessionId, seq)` pair using a deterministic packing — for example `SourceSeq = ledger_seq * 1000 + sub_event_index` where `sub_event_index` orders sub-events from one ledger record (turn_start → op[0]_start → op[0]_finalize → payload_ref[0] → ... → turn_end). Exact packing decided at implementation time.
.agents/sow/specs/adapter-aiagent-v3.md:576:- The cursor's per-file byte `Offset` is the resume mechanism: on restart `Scan(since=cursor)` seeks past already-read bytes so completed records are not re-emitted. Resume is cursor-driven, not gated by a per-source `SourceSeq` high-water-mark (removed in SOW-0015; a scalar HWM cannot work when one `sourceID` aggregates many files whose `ledgerSeq` each restart at 1).
.agents/sow/specs/adapter-claude-code.md:507:| `SourceSeq` | adapter's deterministic per-event identifier (stable across rescans); observability counter, not a dedup gate |
.agents/sow/specs/adapter-aiagent-v2.md:247:The translation is harder than v3 because the v2 file is a **snapshot of full state at one moment**, not an append-only ledger. The adapter therefore behaves as a deterministic projection: for each file scan, walk the full opTree and emit one canonical event per node. Replaying the same file produces the same events; the ingester is idempotent at the SQL layer (every table upserts on a natural identity, so re-emitted rows never duplicate — see `ingester.md` §Dedup and Idempotency and SOW-0015). The deterministic `SourceSeq` is a stable per-node identifier and an observability counter, **not** a dedup gate.
.agents/sow/specs/adapter-aiagent-v2.md:253:- `SourceSeq`: a stable per-(file, opTree-node) identifier (deterministic across rescans), NOT monotonic per source and NOT a dedup gate — see `ingester.md` §Dedup and Idempotency. Strategy: for each (file, opTree-node) emit a `SourceSeq` computed as `xxhash64("v2:" + <originId> + ":" + <pathInTree>)` where `pathInTree` is the opTree's stable path (`opTree.id` and concatenated turn/step/op indices, e.g. `"T:0/O:0"`, `"S:0/O:1"`, `"T:1/O:2/child/T:0/O:0"`). xxhash64 over uint64 is collision-safe within 2^32 events per source.
.agents/sow/specs/adapter-aiagent-v2.md:321:Because event `SourceSeq` is computed deterministically from `(originId, opTreePath)`, replaying produces the SAME `SourceSeq` values as the original emission. Re-emitted events upsert onto the same canonical rows (every writer table has a natural-identity key with `ON CONFLICT`), so duplicates collapse at the SQL layer — there is no per-source high-water-mark gate (a scalar HWM is incompatible with one `sourceID` aggregating many per-file-sequenced files; see `ingester.md` §Dedup and Idempotency and SOW-0015). Therefore the adapter is safe to re-emit every event on every scan; this is the deliberate trade-off for v2's snapshot-not-ledger nature.
internal/presenter/health.go:45:// SourceSeq seen) from source_progress.last_seq; NOT a dedup gate.
internal/canonical/doc.go:11:// on its natural identity (NOT a SourceSeq dedup gate; see ingester.md
.agents/sow/specs/adapter-opencode.md:380:- `SourceSeq = deterministic per-event identifier` (stable across rescans; observability counter, not a dedup gate — see Idempotency)
internal/canonical/adapter.go:15:// upserts (NOT a SourceSeq dedup gate; see ingester.md §Dedup and
.agents/sow/specs/observability.md:51:      "last_seq":12345          // per-source observability counter (max SourceSeq seen); NOT a dedup gate
```

Every remaining hit is acceptable: (a) the `ingester.md:95-108` §"Why no scalar
high-water-mark" explanation, which itself states `SourceSeq` is NOT monotonic
across a source and distinguishes the v3 per-file packing from the v2 content
hash; (b) `canonical-events.md` / `adapter-aiagent-v2.md` / `adapter-aiagent-v3.md:576`
correctly-negated framing ("NOT a dedup gate", "NOT monotonic per source",
"per file, not per source"); (c) presenter / health / `observability.md` /
`rest-api.md` observability-counter comments; (d) `canonical/doc.go` +
`canonical/adapter.go` "NOT a SourceSeq dedup gate" negations. No stale
gate/ordering wording remains. (The `aiagent_v2/bench_test.go:94` Go-heap
"live-bytes high-water mark" is filtered out by `grep -v "_test.go"`.)

iter-5 gate results (from `$REPO_ROOT`; doc/comment-only edits, so no
gosec/shellcheck/real-corpus re-run needed — confirming build + test pass
proves the comment edits broke nothing):
- `gofmt -l .` / `goimports -l .` — zero output.
- `go vet ./...`, `go build ./...` — clean; both binaries build (`BUILD OK`).
- `go test -race -count=1 ./...` — all packages pass.
- `golangci-lint run --timeout=5m` — 0 issues.

## Reviews

### Round 1 (2026-05-27) — codex + glm + minimax
- minimax: no actionable findings (iter-1) → confirmed convergence.
- glm: 2 low-risk doc P2 (message-in-key, payload_refs DO-NOTHING
  metadata) — assessed acceptable; documented.
- codex: 1 real P2 (log_entries key omitted `turn_id` → false-dedup
  data loss) + 2 P3 spec drift. The P2 is the standout — caught a
  data-loss path minimax and glm had both cleared. All addressed in
  iteration 2.

### Round 2 (2026-05-27) — codex + glm + minimax
- minimax: no actionable findings — convergence reached.
- glm: 2 doc-only spec-drift spots — fixed in iteration 3.
- codex: timed out (stdin hang); re-run in Round 3.

### Round 3 (2026-05-27) — codex + glm (post spec-drift sweep)
- codex: NOT converged. 1 real P2 — `log_entries` idempotency key
  omitted `extras_json` (persisted but unkeyed → false-dedup of two logs
  identical except for extras, e.g. v2's `path`), same class as the
  iter-2 `turn_id` bug. Plus P3 spec/comment drift (`adapter-aiagent-v2.md`
  "monotonic per source"; stale HWM comments in `canonical/doc.go`,
  `canonical/adapter.go`, `aiagent_v3/adapter.go`,
  `migrations/0002_source_progress.sql`). All addressed in iteration 4.
- glm: convergence on the core mechanism (HWM event-drop removed,
  cursor-driven resume, migration 0003 + ON CONFLICT match, schema=3);
  flagged `extras_json` as a defensible trade-off (P3, not blocking) and
  noted a P3 source-level log re-emit coverage gap. The extras_json item
  is the same one codex rated P2; iter-4 closes it with a load-bearing
  regression test, so the trade-off no longer exists.

### Round 4 (2026-05-27) — codex + glm + minimax (post extras_json fix + full sweep)
- minimax: convergence reached — code correct & complete; no actionable
  findings on the fix (HWM event-drop removed, cursor-driven resume, migration
  0003 + ON CONFLICT match incl. `extras_json`, schema=3).
- glm: convergence reached — confirms the core mechanism is correct and the
  iter-4 `extras_json` regression test closes the last data-loss path; no
  blocking findings.
- codex: confirmed the CODE fix is correct; only remaining items were doc-only
  stale `SourceSeq`-semantics wording — P2 in `adapter-contract.md:75` /
  `adapter-aiagent-v3.md:504`, P3 the `aiagent_v3/mapper.go` "global ordering"
  claim, and P3 the `canonical/events.go` "monotonic per file" overstatement
  (false for v2's content hash). All 8 spots addressed in iteration 5 (doc /
  comment-only; no executable code changed). Post-sweep grep in iter-5 proves
  no stale gate/ordering wording remains.

## Outcome

(Filled when the SOW is closed.)
