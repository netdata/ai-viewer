# SOW-0063 — scan too slow for real-world volume + stub-session pollution (CORRECTED: NOT event loss)

## Status

Status: open (BLOCKER — usability)

Sub-state: CORRECTION (2026-06-15). Originally filed as "intermittent canonical-event loss" based on the observation that many v3 sessions had 0 turns/ops. **That diagnosis was wrong.** The evidence (below) shows the events were never lost — the scanner simply hadn't reached those files yet (it processes them alphabetically and was ~33% through 316k when stopped). The "empty running" sessions are `requireSessionID` stubs for unscanned parents, not data loss. The real issues are: (1) the scan is too slow for real-world volume (~11h for 316k files), and (2) stub rows pollute the UI by looking identical to real empty/running sessions.

### Evidence that overturned the event-loss hypothesis

- **Alphabetical distribution.** v3 sessions with `op_count > 0` have native_id prefixes `0`-`3` only; sessions with `op_count = 0` span `0`-`f`. The scanner sorts filenames alphabetically and had reached the `3xxx` files (~33% of the 316k UUID namespace) when stopped. Every prefix `4`-`f` has ZERO sessions with ops.
- **Small-scale reproduction works perfectly.** Copying 20 files (including `d1be4245-...`, which showed 0 ops in production) into a temp root and running the full ingester: `d1be4245` lands correctly with `op_count=12, turn_count=5, status=completed`. Same file, same code, same pipeline — the only difference is it WAS reached in the small run.
- **`d1be4245` is a stub.** Its production row has `status=running, op_count=0, turn_count=0, last_activity_ts=<a child event's ts>` — the signature of a `requireSessionID` stub (created when a child's events reference an unscanned parent), not a real session_start.
- **Zero flush errors during normal operation.** All 17 ERROR-level log lines are from the `systemctl stop` (shutdown flush failures). No batch failures during the 72-minute scan.
- **The batch-drop-on-error path IS a latent risk** (code confirmed: `flushBatchWithWriteContext` clears the batch on ANY flush error, no retry) — but it did NOT fire here. It should be hardened but is not the cause of the empty sessions.

### Three real issues to fix (reframed)

1. **Scan speed is unacceptable for real-world volumes.** 316k ai-agent files at ~8 sessions/sec = ~11 hours. The bottleneck is SQLite write serialization (`SetMaxOpenConns(1)` + 5 concurrent workers + FTS reindex + rollup refresh per 1000-event batch). The operator's data has 316k files; the scan must finish in minutes, not hours, for the "install and view" experience to work.
2. **Stub-session pollution.** `requireSessionID` creates stub rows (`status=running, op_count=0, turn_count=0`) for sessions whose files haven't been scanned yet (out-of-order child→parent arrival). These are indistinguishable from real empty/running sessions in the UI. Need either: (a) a distinct `stub`/`empty` status, (b) an `ingested` boolean, or (c) deferred session creation. Operator explicitly requested an `empty` status distinct from `running` (2026-06-15 feedback).
3. **Batch-drop-on-flush-error (latent data-loss path).** `flushBatchWithWriteContext` (worker_runtime.go:151-154) drops the batch on ANY flush error — no retry, no re-queue. Did not fire here but is a correctness risk under contention/crash. Harden: retry with backoff, or re-queue the batch events.

## Requirements

### Purpose

Make ai-viewer usable on real-world agent-data volumes (100k+ session files): the initial scan finishes in minutes, stub/unscanned sessions are clearly distinguished from real ones, and the ingest pipeline never silently drops a batch.

### Acceptance Criteria

1. **Scan speed:** a 316k-file ai-agent v3 source ingests in under 10 minutes (target: under 5). **Verification**: timed ingest of the operator's real data; row counts + event counts match the adapter's emission counts for a sampled subset.
2. **Stub/empty status:** PARTIALLY ADDRESSED. The `empty`-session filter (hide sessions with 0 ops + 0 turns from the default list, `include_empty=1` to see them) is deployed and solves the UX problem. The canonical `abandoned` status already exists for "started but produced no turns" — the remaining work is to transition 0-op sessions to `abandoned` at finalize time (the writer currently leaves stubs as `running`). This is a lower-priority cosmetic improvement now that 0-op sessions are hidden by default.
3. **No silent batch drops:** a flush failure retries (bounded backoff) or re-queues the batch events rather than dropping them silently. **Verification**: a test that injects a transient flush failure asserts the events land on retry.

## Pre-Implementation Gate

(To be filled. The scan-speed investigation should profile WHERE the time goes: is it SQLite write serialization (one connection for 5 workers)? FTS reindex per batch? Rollup refresh per batch? Modernc Cgo overhead? The fix follows the profile — likely: defer FTS+rollup to a post-scan backfill, increase batch size, or parallelize reads.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

- **Follow the data, not the rationalization — and check the SIMPLEST hypothesis first.** Three wrong hypotheses (structural placeholders → event loss → incomplete scan) before the truth. The 5-minute check that found it: `SELECT substr(native_id,1,1), SUM(CASE WHEN op_count>0 THEN 1 ELSE 0 END) FROM sessions GROUP BY 1`. The alphabetical distribution was instantly diagnostic. Record: when sessions are "empty," first check whether the scan actually reached them (alphabetical/file-count correlation) before suspecting the pipeline.
- **The batch-drop-on-error path IS real** (code confirmed). It didn't fire here but it will under contention or crash. Harden it as part of this SOW.

## Requirements

### Purpose

Make ai-viewer usable on real-world agent-data volumes (100k+ session files): the initial scan finishes in minutes (not hours), stub/unscanned sessions are clearly distinguished from real ones, and the ingest pipeline never silently drops a batch.

### Profiling evidence (2026-06-15)

Timed scan of ai-agent v3 files (single source, no contention from other sources):

| Files | Wall time | Rate | Projection to 316k |
|---|---|---|---|
| 200 | ~1s | 200 files/s | ~26 min |
| 2000 | ~40s | 50 files/s | ~105 min |

**Super-linear degradation** (200→2000 files = 10x files but 40x time; rate drops from 200/s to 50/s). The INSERT path itself is fast; the bottleneck is `refreshBatchReadModels` which runs on EVERY 1000-event batch:
1. `refreshRollups` — recomputes each dirty hour bucket from scratch (reads ALL ops in that hour from the DB; a bucket with 5000 ops takes 5000 reads).
2. `refreshFTS` — DELETE+INSERT per dirty op into fts_ops (3 SQL statements per op).
3. `refreshAggregates` — SUM dirty session/turn counts (cheap; keep this).

As more data accumulates, dirty buckets grow, and each recompute reads more ops → the cost per batch grows with the dataset.

## Pre-Implementation Gate

### Problem / root-cause model

The per-batch read-model refresh (rollups + FTS) is O(ops-in-dirty-buckets) per batch, growing super-linearly with accumulated data. For a bulk historical scan, this work is redundant: the rollup tables and fts_ops index can be built ONCE after all data is inserted, via the existing `BackfillRollups` + `BackfillFTS` functions. Running them incrementally per-batch during the scan is the bottleneck.

### Design decision (CTO)

**Defer rollup + FTS refresh during the initial bulk scan; build them once post-scan.** The writer skips `refreshRollups` + `refreshFTS` during batch flush when a "defer read models" flag is set; `refreshAggregates` (cheap session-count update) still runs so the UI shows correct counts. When the adapter's `Scan` returns (historical drain complete), the binary clears the flag, runs `BackfillRollups` + `BackfillFTS`, then enters `Tail` with incremental refresh re-enabled.

This mirrors the existing pattern: `BackfillRollups` + `BackfillFTS` already exist as tested one-shot rebuild functions (`internal/ingest/rollup_backfill.go`, `internal/ingest/fts_backfill.go`). The change is: (a) a flag on the worker/writer that skips the two refresh calls, (b) a method on the ingester that runs both backfills, (c) the `cmd/ai-viewer-ingest` Scan→backfill→Tail wiring.

### Implementation plan

1. `internal/ingest/ingester.go`: add `deferReadModels atomic.Bool`; method `SetDeferReadModels(b bool)`. Thread to worker: `w.deferReadModels = &i.deferReadModels` in `Submit`.
2. `internal/ingest/worker.go` / `refreshBatchReadModels`: if `deferReadModels` is set, skip `refreshRollups` + `refreshFTS`; keep `refreshAggregates`.
3. `internal/ingest/ingester.go`: add `BackfillReadModels(ctx) error` — calls `BackfillFTS` + `BackfillRollups` against the DB, logging progress.
4. `cmd/ai-viewer-ingest/sources.go`: `runAdapter` gains an `ing *ingest.Ingester` parameter. After `adapter.Scan` returns: clear the defer flag (`ing.SetDeferReadModels(false)`), call `ing.BackfillReadModels(ctx)`, log the result, then proceed to `adapter.Tail`.
5. `cmd/ai-viewer-ingest/main.go`: set `ing.SetDeferReadModels(true)` before the source-start loop.
6. **Stub handling (AC#2):** the `empty` status for sessions with `turn_count=0 AND op_count=0`. The ingester sets `status='empty'` on session-finalize when the session has no work; the UI treats `empty` as de-prioritized. (Separate from stubs — after the scan completes, any remaining stub is a genuinely-empty or orphan session.)

### Validation plan

- Timed scan of 2000 files: target under 5s (was 40s). 316k: target under 5 min.
- `BackfillFTS` + `BackfillRollups` produce byte-identical results to the incremental path (the existing parity gate — `rollup_parity_test.go` — covers rollups; add an FTS parity check if one doesn't exist).
- Full `go test -race ./...` green.
- The `empty` status lands for scanned sessions with no ops.

## Analysis

Sources checked: live `/opt/ai-viewer/data/index.db` (post-mortem counts), `~/.ai-agent/sessions/session/d1be4245-*.jsonl` + `~/.ai-agent/sessions/payloads/d1be4245-*/turn-*NN` (source-of-truth file shapes), an in-package `TRACE_ROOT=… go test` over the v3 adapter (adapter emits the events; the loss is downstream), `journalctl -u ai-viewer-ingest` (no errors), `internal/adapters/aiagent_v3/{mapper.go,scanner.go,parser.go}` (dispatch handles turn/session_summary), `internal/ingest/writer.go` (`requireSessionID` creates stubs; `apply` propagates errors). Filed 2026-06-15.

Risks:

- **R1 — Every aggregate is unreliable until this is fixed.** Session counts, cost totals, token totals, topology edges all depend on ops/turns that are partially missing. The Statistics page's $419.63 / 206M tokens are under-counts. This blocks Milestone B (operator feedback) — the operator cannot judge the UX on wrong data.
- **R2 — Scale-dependent.** If the loss only fires at 316k-file scale, a unit test won't catch it; the gate needs a scale-reproducing integration harness or a leak detector (count emitted vs applied) that runs in CI.
- **R3 — Status-enum change.** Adding `empty` touches the sessions `status` enum (data-model.md §sessions), the presenter filters, and the UI. Bounded but cross-cutting.

## Pre-Implementation Gate

(To be filled when picked up. REQUIRED FIRST STEP: build the instrumented harness that counts adapter-emitted events vs writer-applied events vs committed rows for a fixed file subset — this LOCALIZES the loss before any fix is attempted. Do not guess; the evidence has already overturned two wrong hypotheses (first "structural placeholders", then "scanner skip"). A third guess wastes time.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

- **Follow the data, not the rationalization.** The CTO's first response to "empty ai-agent sessions" was to rationalize them as "structural child-session placeholders" / "abandoned test stubs" — both wrong. The operator's instinct ("ai-agent cannot have empty sessions") was correct. The 5-minute check that overturned it: run the adapter on the file in isolation and compare to the DB. Record: when the operator reports a data-integrity issue, the FIRST step is an adapter-isolation-vs-DB diff, not a defense of the current behavior.
- **The ingester at 99% CPU on 316k files is a stress condition.** Whatever the loss mechanism, it fires under that load. The gate must reproduce at scale or with a leak detector, not just a unit test.
