# SOW-0062 — `TestRefreshRollups_OtherStaleRowRemoval` hangs under `-race` (pre-existing on master)

## Status

Status: open

Sub-state: pre-existing master defect, discovered during SOW-0024 verification. Filed by the CTO under Hard Rule #9 (tech debt is tracked, not silently ignored). NOT caused by SOW-0024 — confirmed by stashing all SOW-0024 changes and reproducing the hang on clean master (stuck in `database/sql.(*Tx).beginDC` → `awaitDone`, a tx/pool deadlock).

## Requirements

### Purpose

Restore `go test -race ./...` to green on master. Today the ingest package cannot complete a full `-race` run because `TestRefreshRollups_OtherStaleRowRemoval` hangs indefinitely (no timeout reached — a genuine deadlock). This blocks the full-test gate and the coverage gate (which must `-skip` this test to complete).

### User Request

None directly. Discovered by the CTO during SOW-0024; the operator was surfaced a note about it. Filed to keep the gate honest — a hanging test on master is a real defect even if it predates the current work.

### Assistant Understanding

Facts:

- The test (`internal/ingest/rollup_refresh_test.go:797`) builds a batch of `extra = 2100` sessions × ~6 canonical events each (~12 600 events) and calls `flushBatch` (line 22-42), which runs `worker.flush(ctx, wr, batch)` with `ctx = context.Background()` (no deadline).
- `flushBatch`'s worker (lines 27-37) is constructed WITHOUT the `now`, `fts5IndexLogs`, or `metaJSON` fields that production (`Ingester.Submit`) sets. (Pre-existing; unrelated to SOW-0024's `metaJSON` addition — the field was already absent before.)
- Under `-race`, the goroutine trace shows a `database/sql.(*Tx).beginDC` call blocked in `awaitDone` (chan receive) for >1 minute. The flush path reaches `refreshBatchReadModels` → `refreshFTS` → `reindexOp` (`internal/ingest/fts_refresh.go:98`), i.e. the FTS5 reindex over the large batch.
- Reproduces in isolation: `go test -race -run TestRefreshRollups_OtherStaleRowRemoval ./internal/ingest/...` hangs on clean master (all SOW-0024 changes stashed with `-u`).

Inferences:

- The deadlock is most likely a connection-pool/transaction interaction in the FTS reindex path under the race detector (a second `BeginTx`/`ExecContext` blocking on a connection held by an outer transaction, or a modernc/sqlite serialization point). The `context.Background()` (no deadline) is what turns a slow operation into an indefinite hang.
- The batch size (12 600 events) makes the FTS reindex the dominant cost; under `-race` the overhead pushes whatever contention exists into a deadlock rather than just slowness.

Unknowns (resolve in the gate):

- Whether the deadlock is in modernc's connection serialization, a leaked transaction in the test fixture, or the FTS reindex opening a nested transaction on a saturated pool.
- Whether the fix is: (a) bound the test's `flushBatch` context with a timeout so a stall fails the test instead of hanging (minimal, makes CI honest), (b) reduce the batch size, (c) fix an actual leaked-tx/pool bug in the FTS reindex path, or (d) some combination. The gate must determine whether this is a TEST-only issue or a real production-path deadlock risk (if `flushBatch` mirrors the production flush exactly, a deadlock here could imply one in production under load — that must be ruled out).

### Acceptance Criteria

1. `go test -race -count=1 ./internal/ingest/...` completes (pass or fail) within a bounded time without hanging. **Verification**: CI / local run terminates.
2. Root cause identified (test-only fixture issue vs production-path deadlock risk) and documented in the SOW gate. **Verification**: the gate names the cause with file:line evidence.
3. If the cause implies a production-path risk, that risk is addressed (not just the test patched). **Verification**: a test that reproduces the contention pattern passes.
4. The coverage gate and `scripts/test.sh` run without a `-skip` workaround. **Verification**: `scripts/test.sh` completes green.

## Analysis

Sources checked: `internal/ingest/rollup_refresh_test.go` (`flushBatch` line 22-42, the test at 797), `internal/ingest/fts_refresh.go:98` (`reindexOp`), `internal/ingest/worker.go` (`flush`, `refreshBatchReadModels`), the SOW-0024 stash-and-reproduce run (2026-06-14). Filed 2026-06-14.

Risks:

- **R1 — Production-path deadlock risk.** `flushBatch` mirrors the production `worker.flush` path. If the hang is a real tx/pool contention (not a test-fixture artifact), the same deadlock could fire in production under a large batch + race-free runtime. The gate MUST rule this out or fix it. This is the serious case.
- **R2 — Silent gate degradation.** As long as this hangs, the full-test and coverage gates require a `-skip` workaround, which masks any NEW hang in the same package. The fix restores the gate's honesty.

## Pre-Implementation Gate

(To be filled when this SOW is picked up. Required before moving to `current/`. The gate MUST begin by reproducing the hang on clean master and capturing the full goroutine dump (`go test -race -run TestRefreshRollups_OtherStaleRowRemoval -v` then `kill -SIGQUIT` or `Ctrl+\` for a stack dump, or run under `go test -timeout 60s` and read the timeout dump), then classify the blocking goroutine.)

## Implementation

(Empty placeholder.)

## Validation

(Empty placeholder.)

## Reviews

(Empty placeholder.)

## Outcome

Pending.

## Lessons / Follow-Ups

Pending.
