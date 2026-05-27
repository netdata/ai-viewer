# SOW-0015 - Ingest worker FK constraint failures on real corpus

## Status

Status: open

Sub-state: filed by SOW-0001 Chunk 11 iteration 2 master review; not yet triaged or scheduled.

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

## Reviews

(Filled by reviewers when the SOW moves out of `pending/`.)

## Outcome

(Filled when the SOW is closed.)
