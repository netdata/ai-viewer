# SOW-0062 — `TestRefreshRollups_OtherStaleRowRemoval` is very slow under `-race` (corrected: NOT a hang/deadlock)

## Status

Status: completed

Originally filed as a "tx/pool deadlock" and "production-path deadlock risk" based on a misread truncated goroutine dump. **That framing was wrong.** Reproduction with a full goroutine dump + a generous timeout disproved it:

- **No-race run: PASSES in 5.6s.**
- **`-race` run with default 10m timeout: PASSES in 241.6s** (isolated) / **371.8s** (full ingest package).
- The full goroutine dump shows the test goroutine `[runnable]` mid-execution inside modernc's VDBE (`_sqlite3VdbeExec` → `_sqlite3VdbeFreeCursor` → `_sqlite3BtreeLeave`), i.e. ACTIVELY EXECUTING a SQLite statement in the apply loop — NOT blocked on any lock/chan/select. The `(*Tx).awaitDone` goroutine I originally flagged is the NORMAL context-watcher that exists for every open transaction; it is not the blocker.
- `scripts/test.sh` and CI use Go's default 10m test timeout → they PASS. This was never a CI failure.

**Real issue (rescoped):** the test writes ~12,600 canonical events (2,100 distinct cwds to exceed the hardcoded 2,000 rollup-collapse threshold) through modernc (Cgo-translated SQLite). Under `-race`'s 2-10x overhead that takes ~242s. It is not broken and poses **zero production risk** (the production path runs the same work in 5.6s). It just makes local `-race` iteration on the ingest package slow (~6 min) and was the reason SOW-0024's validation used a `-skip` (which was itself unnecessary — the default-timeout gate passes). The `-skip` is retired by this SOW.

A second test, `TestRefreshRollups_ParityWithBackfill`, writes the same 2,100 distinct cwds through BOTH the incremental and backfill paths and is the other large contributor to package `-race` time; it is fixed by the same change.

## Requirements

### Purpose

Make two pathologically-slow `-race` tests fast so local iteration on the ingest package drops from ~6 min to ~2 min, and retire the SOW-0024-era `-skip` workaround. **Not a correctness fix** — the tests pass under the default 10m timeout; they are just slow because they write ~12,600 real events solely to exceed a hardcoded 2,000-row collapse threshold.

### User Request

None directly. Discovered by the CTO during SOW-0024 verification (the `-skip` workaround). Originally misdiagnosed as a deadlock; corrected after a full goroutine-dump reproduction showed the test goroutine `[runnable]` executing SQLite (not blocked). Filed/rescoped to fix the real (slow-test) problem.

### Acceptance Criteria

1. `TestRefreshRollups_OtherStaleRowRemoval` and `TestRefreshRollups_ParityWithBackfill` PASS in single-digit seconds under `-race`. **Verification**: targeted `-run` timing.
2. `go test -race -count=1 ./internal/ingest/` (default 10m timeout) PASSES with NO `-skip`, in materially less than the prior 371.8s. **Verification**: full-package timing.
3. The refresh≡backfill byte-parity invariant (`rollup_parity_test.go`) stays green with the injected threshold. **Verification**: parity gate passes.
4. Production behavior unchanged (the seam defaults to the rollups 2000; no caller changes). **Verification**: no existing caller of `BackfillRollups` is modified; `go build`/existing tests green.

## Analysis

Sources checked: full goroutine dump (`go test -race -timeout 30s -run TestRefreshRollups_OtherStaleRowRemoval`), the no-race (5.6s) and race (241.6s isolated / 371.8s package) reproductions, `internal/rollups/rollups.go` (`Options.MaxRowsPerBucketDimension`), `internal/ingest/rollup_refresh.go:171` + `rollup_backfill.go:190,197` (the three `rollups.Rollup` call sites), `internal/ingest/rollup_refresh_test.go:527,748` (the two slow tests), `BackfillRollups` callers (~23, all 4-arg). Filed/rescoped 2026-06-14.

Risks:

- **R1 — Parity invariant.** A threshold that affects refresh but not backfill would silently break the refresh≡backfill byte-parity gate. Mitigation: thread the seam to BOTH paths (writer field + `BackfillOption`); the parity test sets both to the same value.
- **R2 — Production-behavior drift.** Mitigation: zero-value = rollups default 2000; no existing caller changed; additive variadic option.

## Pre-Implementation Gate

### Problem / root-cause model (corrected)

Two ingest tests are pathologically slow under `-race` because they write ~12,600 real canonical events (2,100 distinct cwds) solely to exceed the hardcoded `maxRollupRowsPerBucket = 2000` collapse threshold (`internal/rollups/rollups.go:30`) and exercise the `__other__` tail-collapse path. modernc (Cgo-translated SQLite) under `-race`'s 2-10x overhead turns that into ~242s (isolated) / ~372s (full package). Not a deadlock, not a correctness bug, not a production risk.

### Evidence reviewed

- Full goroutine dump (`go test -race -timeout 30s -run TestRefreshRollups_OtherStaleRowRemoval`): test goroutine `[runnable]` in `_sqlite3VdbeExec`/`_sqlite3BtreeLeave` (executing, not blocked). The `[chan receive]` `(*Tx).awaitDone` goroutine is the normal per-tx context-watcher.
- `go test -timeout 30s ./internal/ingest/ -run TestRefreshRollups_OtherStaleRowRemoval` (no `-race`): PASS in 5.6s.
- `go test -race -timeout 300s ./internal/ingest/ -run TestRefreshRollups_OtherStaleRowRemoval`: PASS in 241.6s.
- `go test -race -count=1 ./internal/ingest/` (default 10m timeout): PASS in 371.8s.
- `internal/rollups/rollups.go:26-30,99-103,161-164`: `Options.MaxRowsPerBucketDimension` (default 2000) — already injectable at the fold level.
- `internal/ingest/rollup_refresh.go:171` and `rollup_backfill.go:190,197`: the three `rollups.Rollup(..., rollups.Options{})` call sites — all pass an empty `Options` (→ default 2000).
- `internal/ingest/rollup_refresh_test.go:748` (`TestRefreshRollups_OtherStaleRowRemoval`) and `:527` (`TestRefreshRollups_ParityWithBackfill`): the two slow tests; both build 2,100-cwd batches.
- `BackfillRollups` (`rollup_backfill.go:41`): ~23 callers, all 4-arg — a variadic `...BackfillOption` is backward-compatible.

### Affected contracts and surfaces

- `internal/ingest/writer.go`: add `maxRollupRowsPerBucket int` field (zero-value = use rollups default 2000; production behavior unchanged).
- `internal/ingest/rollup_refresh.go:171`: pass `rollups.Options{MaxRowsPerBucketDimension: w.maxRollupRowsPerBucket}` (was empty Options).
- `internal/ingest/rollup_backfill.go`: `BackfillRollups` gains a variadic `...BackfillOption`; new `BackfillOption` type + `WithBackfillMaxRollupRowsPerBucket(n int)`; `backfiller` gains the field; the two backfill `rollups.Rollup` call sites use it. Default-preserving (0 → rollups default).
- **Parity invariant (critical):** the refresh-vs-backfill byte-parity (`data-model.md` §Rollup tables; the `rollup_parity_test.go` gate) requires BOTH paths to use the SAME threshold. The writer field (refresh) and the BackfillRollups option (backfill) are the two seams; the parity test sets BOTH to the same small value, so refresh≡backfill holds. A divergent threshold across the two paths would silently break parity — the reason this fix threads both, not just the writer.
- Two tests shrink: `OtherStaleRowRemoval` (threshold 20, `extra` 2100→30) and `ParityWithBackfill` (threshold 20, `distinctCwds` 2100→30, both store A incremental + store B backfill set to 20).

### Spec deltas to land before any test or code

- `.agents/sow/specs/data-model.md` §Rollup tables / R1 safety bound: note that the 2000 default is overridable on both the incremental refresh and the one-shot backfill paths (test/local-tuning seam; default unchanged). No behavioral change to document beyond the seam's existence.

### Existing patterns to reuse

- `rollups.Options{MaxRowsPerBucketDimension}` — already the fold-level injection point; this SOW threads a value to it from the ingest caller.
- Variadic functional options (the codebase uses `ingest.With*` options for the Ingester; `BackfillOption` mirrors that pattern for `BackfillRollups`).

### Risk and blast radius

- **Low.** Additive, default-preserving (zero-value = rollups default = current behavior). No schema, no API-shape change to existing callers (variadic option is backward-compatible). The parity invariant is preserved by construction (both paths take the same value).

### Sensitive data handling

None. Test-only event counts and cwds.

### Implementation plan

1. `writer`: add field `maxRollupRowsPerBucket int`.
2. `rollup_refresh.go:171`: use `rollups.Options{MaxRowsPerBucketDimension: w.maxRollupRowsPerBucket}`.
3. `rollup_backfill.go`: add `BackfillOption` type + `WithBackfillMaxRollupRowsPerBucket(n int) BackfillOption`; `BackfillRollups` gains `...BackfillOption` and applies them to the `backfiller`; `backfiller` gains the field; the two `rollups.Rollup` call sites (190, 197) pass `rollups.Options{MaxRowsPerBucketDimension: bf.maxRollupRowsPerBucket}`.
4. Shrink `TestRefreshRollups_OtherStaleRowRemoval`: threshold 20, `extra` 2100→30; set the writer field via a test-aware flush. Assertion invariants preserved (`/work/lonely` collapses into `__other__` because the 30 extras each outrank it; 30 > 20 → tail collapses).
5. Shrink `TestRefreshRollups_ParityWithBackfill`: threshold 20, `distinctCwds` 2100→30; store A sets the writer field to 20; store B calls `BackfillRollups(..., WithBackfillMaxRollupRowsPerBucket(20))`. Both paths collapse identically → parity holds.

### Validation plan

- `go test -race -count=1 -run 'TestRefreshRollups_OtherStaleRowRemoval|TestRefreshRollups_ParityWithBackfill' ./internal/ingest/`: PASSES in single-digit seconds (was ~242s + ~120s).
- `go test -race -count=1 ./internal/ingest/` (default 10m timeout, NO `-skip`): PASSES well under the previous 371.8s.
- The existing parity gate (`rollup_parity_test.go`) stays green — confirms refresh≡backfill with the injected threshold.
- Full gates run WITHOUT the SOW-0024-era `-skip`.

### Open decisions

- **Operator-facing CLI flag for the threshold?** NO this SOW. The seam exists; exposing it is a config-surface decision for a future SOW. Default stays 2000.
- **5-reviewer cycle?** CTO-discretion. Additive + default-preserving + test-ergonomics; NOT schema/security/adapter/cross-cutting-refactor. Likely skip.

### Artifact impact plan

- `internal/ingest/writer.go` — add field.
- `internal/ingest/rollup_refresh.go` — 1 call site.
- `internal/ingest/rollup_backfill.go` — option type + variadic + backfiller field + 2 call sites.
- `internal/ingest/rollup_refresh_test.go` — shrink 2 tests (+ a small test-aware flush seam).
- `.agents/sow/specs/data-model.md` — note the seam (1-2 lines).
- `.agents/sow/done/SOW-0024-*` — correct the "pre-existing rollup test hang" lesson wording (it was never a hang).

## Implementation

Implemented 2026-06-14 (Phase: Development, dev-phase workflow: direct-to-master; CTO-coded per the 2026-06-14 operator directive). The SOW was originally filed as a "deadlock" and **rescoped** after reproduction disproved that: it was a slow test, not a deadlock. The fix is an additive, default-preserving, parity-preserving seam.

- `internal/ingest/writer.go`: added `maxRollupRowsPerBucket int` field (zero = rollups default 2000; production unchanged).
- `internal/ingest/rollup_refresh.go:171`: passes `rollups.Options{MaxRowsPerBucketDimension: w.maxRollupRowsPerBucket}` (was empty Options).
- `internal/ingest/rollup_backfill.go`: added `BackfillOption` type + `WithBackfillMaxRollupRowsPerBucket(n int)`; `BackfillRollups` gained a variadic `...BackfillOption` (backward-compatible — all ~23 existing callers unchanged); `backfiller` gained the field; the two `rollups.Rollup` call sites (hourly + daily) pass it. Both paths now honor the same seam.
- `internal/ingest/rollup_refresh_test.go`: extracted `flushBatchWithMaxRollupRows` (real impl; `flushBatch` is now a thin wrapper with 0, so no other caller changes). Shrunk `TestRefreshRollups_OtherStaleRowRemoval` (threshold 20, `extra` 2100→30) and `TestRefreshRollups_ParityWithBackfill` (threshold 20, `distinctCwds` 2100→30; store A via the writer field, store B via `WithBackfillMaxRollupRowsPerBucket` — same value on both, preserving the refresh≡backfill parity invariant).
- `.agents/sow/specs/data-model.md` §R1 safety bound: noted the cap is overridable on both materialization paths (seam; default unchanged; parity requires both paths use the same value).

## Validation

All gates green 2026-06-14. **No `-skip` anywhere** — the workaround that motivated this SOW is retired.

- `TestRefreshRollups_OtherStaleRowRemoval` under `-race`: **1.4s** (was 241.6s).
- `TestRefreshRollups_ParityWithBackfill` under `-race`: **1.2s** (was ~120s).
- Full `go test -race -count=1 ./internal/ingest/` (default 10m timeout, NO `-skip`): **PASS in 23.1s** (was 371.8s — 16x faster).
- Full `go test -race -count=1 ./...` (all packages, NO `-skip`): PASS.
- Parity gate (`rollup_parity_test.go`): PASS — refresh≡backfill invariant holds with the injected threshold.
- Coverage gate: PASS — gated aggregate 91.4%; ingest 87.2% (was 87.1%).
- `golangci-lint run ./internal/ingest/... ./internal/rollups/...`: 0 issues. `go vet ./...`: clean. `gofmt`: clean.
- `scripts/spec-drift.sh`: PASS. `scripts/scan-secrets.sh`: PASS (914 files).

## Reviews

Phase: Development — 5-reviewer cycle is CTO-discretion. This change is additive + default-preserving + test-ergonomics (a seam that defaults to current behavior), NOT schema/security/new-adapter/cross-cutting-refactor. No existing caller of `BackfillRollups` changed; the parity invariant is preserved by construction (both paths take the same value). CTO judged it low-risk and **skipped** the 5-reviewer cycle. Verified by: (a) the parity gate (`rollup_parity_test.go`) passing confirms refresh≡backfill still holds with the injected threshold; (b) the variadic option is backward-compatible (build + all existing callers unchanged); (c) the full test suite passes under `-race` with no skip.

## Outcome

Delivered. Two pathologically-slow `-race` tests now run in ~1.3s each; the full ingest package under `-race` dropped from 371.8s to 23.1s (16x). The SOW-0024-era `-skip` workaround is retired — `go test -race ./...` and `scripts/test.sh` now run clean with no exclusions. The original "production deadlock" framing was disproved (corrected in the Status/Requirements/Gateway above): it was always a slow test, never a deadlock, never a production risk. The seam (`writer.maxRollupRowsPerBucket` + `WithBackfillMaxRollupRowsPerBucket`) is available for a future operator-facing CLI flag if high-cardinality tuning is ever needed (out of scope here; default stays 2000).

## Lessons / Follow-Ups

- **Read the FULL goroutine dump before claiming a deadlock.** A goroutine shown `[chan receive]` in `database/sql.(*Tx).awaitDone` is the NORMAL per-transaction context-watcher that exists for every open tx — it is not the blocker. The actual test goroutine was `[runnable]` (executing SQLite), which means "slow", not "deadlocked". The misread caused SOW-0062 to be filed as a P0 "production deadlock risk" and led the operator to prioritize it over Milestone A — a costly framing error. Recorded in SOW-0024 lessons too. (Discipline convention: when a test "hangs", first check whether it passes with a generous timeout and no `-race`; only then suspect a deadlock.)
- **Tests that exist solely to exceed a hardcoded threshold are a speed liability.** `maxRollupRowsPerBucket=2000` forced 2,100-cwd batches. Making such thresholds injectable (even when only tests use the seam) keeps the integration test real without the 2000x event cost. The same pattern applies anywhere a test exists only to cross a constant boundary.
- **The CTO-coded directive is working.** This SOW (investigation + diagnosis + fix + gate verification) was done entirely in the master context per the 2026-06-14 operator directive, with no implementer delegation. The diagnosis (misread-dump correction) is exactly the kind of evidence-based reasoning that benefits from staying in one context end-to-end.
