package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// worker drains a single source's event channel and flushes batched
// transactions into the store. One worker exists per Submit() call.
type worker struct {
	sourceID     string
	sourceFormat string
	location     string
	// fts5IndexLogs is the resolved per-source FTS5 log-indexing flag the
	// ingester computed from WithFTS5IndexLogs (default true). ensureSourceRow
	// persists it on the sources row so the operator's choice survives daemon
	// restart. applyLogEntry reads it to gate fts_logs population (fts_ops is
	// always indexed regardless of this flag).
	fts5IndexLogs bool
	events        <-chan canonical.Event
	db            *sql.DB
	hwm           *hwmCache
	pricer        Pricer
	logger        *slog.Logger
	batchSize     int
	batchEvery    time.Duration
	// now supplies the wall-clock cutoff the incremental rollup refresh uses
	// to pick its open hour/day. Injectable for deterministic tests; defaults
	// to defaultNow when the worker is built without one.
	now func() int64
	// onErr is invoked for batch-level failures (commit errors, etc.).
	// Defaults to logger.Error if not set.
	onErr func(error)
}

// shutdownDrainTimeout caps how long a worker spends draining its
// channel during shutdown. The deadline applies to the final flush
// transaction; the spec mandates a graceful shutdown ceiling and this
// stays well under it.
const shutdownDrainTimeout = 10 * time.Second

// run blocks until ctx is cancelled or the events channel is closed.
// Returns when no more events will arrive. On ctx cancellation the
// worker drains any pending events and runs one final flush with its
// own short-lived context so SQL writes still succeed.
func (w *worker) run(ctx context.Context) {
	wr := newWriter(w.sourceID, w.sourceFormat, w.location, w.pricer)
	if w.now != nil {
		wr.now = w.now
	}
	batch := make([]canonical.Event, 0, w.batchSize)
	flushTimer := time.NewTimer(w.batchEvery)
	defer flushTimer.Stop()
	// Stop the timer until the first event lands so we don't fire on an
	// empty batch.
	if !flushTimer.Stop() {
		select {
		case <-flushTimer.C:
		default:
		}
	}
	timerArmed := false

	// stopTimer stops flushTimer and drains a possibly-already-fired tick so a
	// later Reset starts a clean interval (the standard time.Timer idiom).
	stopTimer := func() {
		if !flushTimer.Stop() {
			select {
			case <-flushTimer.C:
			default:
			}
		}
		timerArmed = false
	}

	// rearmTimer keeps the flush timer ticking while rollup buckets are carried
	// (open at the last refresh) so a bucket that closes during a lull is
	// materialized by the idle tick within ~one batchEvery interval; once the
	// carried set drains it leaves the timer STOPPED (no idle busy-spin —
	// ingester.md §"Incremental rollup refresh", round-7 Part 2).
	rearmTimer := func() {
		if wr.hasPendingRollupBuckets() {
			stopTimer()
			flushTimer.Reset(w.batchEvery)
			timerArmed = true
			return
		}
		stopTimer()
	}

	flush := func(reason string, flushCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := w.flush(flushCtx, wr, batch); err != nil {
			w.report(fmt.Errorf("flush (%s): %w", reason, err))
		}
		batch = batch[:0]
		wr.resetBatch()
		// Re-arm (or stop) the timer based on whether buckets are still carried:
		// a flush that left an open hour pending must keep ticking so the idle
		// refresh materializes it once it closes.
		rearmTimer()
	}

	// idleRefresh runs a dedicated rollup refresh with no events — the idle
	// materialization path for carried buckets that have since closed. Used on an
	// interval fire with an empty batch and on shutdown when the final batch is
	// empty but buckets are still pending.
	idleRefresh := func(refreshCtx context.Context) {
		if !wr.hasPendingRollupBuckets() {
			return
		}
		if err := w.refreshRollupsOnly(refreshCtx, wr); err != nil {
			w.report(err)
		}
		// Reset per-batch state after the refresh-only pass (mirrors the flush
		// closure's resetBatch). The carried dirtyRollupBuckets set is untouched by
		// resetBatch and promoteMaterializedRollupBuckets already applied any
		// committed removals; this clears the per-batch rollup notify flags so a
		// later real flush that touches no rollup does not inherit a stale "fire".
		wr.resetBatch()
	}

	for {
		select {
		case <-ctx.Done():
			// Drain whatever is sitting in the channel (best effort)
			// so events already produced by the adapter are not lost.
			// The shutdown flush uses its own context so the cancelled
			// parent does not abort BeginTx/Commit on a healthy DB.
			//
			// CAUTION: using `select { case ... default: }` here is
			// wrong — Go's select picks randomly among ready cases,
			// and `default` is always ready, so a buffered event would
			// be skipped 50% of the time. We instead pull via len(ch)
			// to bound the drain loop without relying on `default`.
			drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
			for len(w.events) > 0 {
				ev, ok := <-w.events
				if !ok {
					break
				}
				batch = append(batch, ev)
				if len(batch) >= w.batchSize {
					flush("size on shutdown", drainCtx)
				}
			}
			// After buffer is empty, consume the close-or-pending
			// signal — if the producer closed the channel before
			// shutdown, this reads the (zero, false) sentinel.
			select {
			case ev, ok := <-w.events:
				if ok {
					batch = append(batch, ev)
				}
			default:
			}
			flush("ctx done", drainCtx)
			// Final flush may have left buckets carried (or the final batch was
			// empty with buckets still pending): run the refresh-only pass so any
			// bucket closed by shutdown time is materialized before we exit.
			idleRefresh(drainCtx)
			drainCancel()
			return
		case ev, ok := <-w.events:
			if !ok {
				// Producer closed the channel. Use a fresh context for
				// the final flush so a parent cancellation that arrived
				// concurrently does not abort BeginTx.
				drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
				flush("channel closed", drainCtx)
				// Final flush may have left buckets carried (or the final batch was
				// empty with buckets still pending): materialize any closed-by-now
				// bucket before exit.
				idleRefresh(drainCtx)
				drainCancel()
				return
			}
			// No event-drop dedup: a per-source scalar high-water-mark is
			// structurally wrong here (one sourceID aggregates many
			// independently-sequenced files). Resume-skipping is the
			// adapter cursor's job; event-level idempotency is a SQL-layer
			// guarantee (idempotent upserts). See ingester.md §Dedup and
			// Idempotency. Every event flows to the writer.
			batch = append(batch, ev)
			if !timerArmed {
				flushTimer.Reset(w.batchEvery)
				timerArmed = true
			}
			if len(batch) >= w.batchSize {
				flush("size", ctx)
			}
		case <-flushTimer.C:
			timerArmed = false
			if len(batch) > 0 {
				flush("interval", ctx)
				break // flush() already re-armed/stopped the timer as needed.
			}
			// Empty batch but the timer fired: this is an idle materialization
			// tick. Run the refresh-only pass to materialize any carried bucket
			// that has since closed, then re-arm to keep ticking until the
			// carried set drains (rearmTimer self-terminates when empty).
			idleRefresh(ctx)
			rearmTimer()
		}
	}
}

// flush opens a transaction, applies every event in batch via the
// writer, refreshes aggregates, persists source_progress, and commits.
// On any error the transaction rolls back and the error is returned to
// the caller; the batch is dropped (we do not retry — see
// ingester.md §Failure Recovery).
func (w *worker) flush(ctx context.Context, wr *writer, batch []canonical.Event) error {
	// Give the writer this source's resolved FTS5 log-indexing flag for the
	// batch (mirrors how run() threads wr.now). Set here rather than via
	// newWriter so the writer constructor signature stays unchanged (the ~6
	// test callers of newWriter need no edit) and the flag has a reader.
	wr.fts5IndexLogs = w.fts5IndexLogs
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && w.logger != nil {
				w.logger.Warn("worker: rollback failed", "err", rbErr)
			}
		}
	}()

	// Ensure the source row exists; subsequent FK references and
	// source_progress depend on it. The resolved fts5IndexLogs flag is
	// persisted here (the ingester option is the runtime source of truth, so a
	// daemon restart re-asserts it). The in-memory flag (w.fts5IndexLogs →
	// wr.fts5IndexLogs) gates incremental fts_logs indexing in the writer's
	// applyLogEntry path; the persisted sources.fts5_index_logs column is read
	// by BackfillFTS (indexableLogsForFTSQuery) and by GET /api/search
	// (presenter searchLogs).
	if err := ensureSourceRow(ctx, tx, w.sourceID, w.sourceFormat, w.location, w.fts5IndexLogs); err != nil {
		return err
	}

	for _, ev := range batch {
		if err := wr.apply(ctx, tx, ev); err != nil {
			return fmt.Errorf("apply event %s seq=%d: %w", ev.EventKind(), ev.EventSourceSeq(), err)
		}
	}

	// Recompute the time-bucketed rollups for the buckets this batch touched,
	// in THIS tx so they commit atomically with the ops/sessions they
	// summarize (ingester.md §"Incremental rollup refresh"). Runs after the
	// per-event apply loop (the dirty-bucket set is now complete) and before
	// the catalog/aggregate refresh — order among the three is irrelevant
	// since each reads the now-final ops/sessions rows.
	if err := wr.refreshRollups(ctx, tx); err != nil {
		return err
	}

	// Rebuild fts_ops for the ops this batch wrote, in THIS tx so the search
	// index commits atomically with the ops it indexes (mirrors refreshRollups).
	// fts_logs is maintained inline in applyLogEntry (append-only). Runs after
	// the apply loop so each op's FINAL persisted columns (incl. finalize error
	// text) are indexed.
	if err := wr.refreshFTS(ctx, tx); err != nil {
		return err
	}

	if err := refreshAggregates(ctx, tx, wr.dirtyTurnIDs, wr.dirtySessionIDs); err != nil {
		return err
	}

	if err := upsertSourceProgress(ctx, tx, w.sourceID, wr.batchMaxSeq, batchMaxTs(batch), wr.lastCursor, wr.hasCursor); err != nil {
		return err
	}

	// Append the batch's notify change-log rows and prune stale ones,
	// both inside this same tx so they commit atomically with the data
	// (a rollback above leaves the notify table untouched) and remain
	// the ingester's writes (serve is read-only against notify). commitTS
	// is a single timestamp shared by every notify row in this batch.
	commitTS := time.Now().UTC().UnixMicro()
	if err := wr.emitNotify(ctx, tx, commitTS); err != nil {
		return err
	}
	if err := pruneNotify(ctx, tx, commitTS); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	// Promote per-batch pending pricing-miss dedup keys into the
	// lifetime map ONLY after the tx durably commits. If commit fails
	// (above) or any earlier step returned an error, the deferred
	// rollback fires and resetBatch (run by the caller after flush
	// returns) discards pendingMissDedup so the next batch with the
	// same (provider, model, missKind) re-emits the (now-missing)
	// warning.
	wr.promotePendingMissDedup()
	// Apply the carried-set removals for buckets materialized in this committed
	// tx (deferred from refreshRollups so a rolled-back materialization keeps its
	// bucket carried — round-7 P1). MUST run before the caller's resetBatch.
	wr.promoteMaterializedRollupBuckets()

	if wr.batchMaxSeq > 0 {
		w.hwm.Advance(w.sourceID, wr.batchMaxSeq)
	}
	// Surface any best-effort observability errors the writer
	// collected during the batch (e.g. failed pricing-miss WRN
	// inserts). These do NOT fail the batch — the op rows still
	// committed — but suppressing them silently violates the "no
	// silent failures" rule, so we structured-log each one with
	// the source identity.
	if w.logger != nil {
		for _, oerr := range wr.drainObservabilityErrs() {
			w.logger.Warn("worker: observability hook failed",
				"source_id", w.sourceID,
				"err", oerr)
		}
	}
	return nil
}

// refreshRollupsOnly runs a DEDICATED rollup refresh with NO events — the idle
// materialization path (ingester.md §"Incremental rollup refresh", round-7 Part
// 2). When ingestion goes quiet with buckets carried (open at the last flush),
// an hour that later closes must still be materialized without waiting for a new
// event. This opens its own tx and calls ONLY wr.refreshRollups (NOT w.flush,
// which would also rewrite aggregates / source_progress / FTS for an empty
// batch), emits exactly one stats_invalidated IFF it materialized ≥1 bucket
// (reusing the same notify path as flush), prunes, and commits. wr.now drives the
// open-hour cutoff so a bucket closed since the last tick is now materialized. On
// any error the tx rolls back and the error is returned; the carried set is left
// intact for the next tick (refreshRollups only STAGES the removal of a
// materialized bucket — promoteMaterializedRollupBuckets applies it post-commit —
// so a rolled-back recompute keeps the bucket carried and is naturally retried).
func (w *worker) refreshRollupsOnly(ctx context.Context, wr *writer) error {
	// Clear the per-batch rollup notify signals so this pass's notify reflects
	// ONLY what it materializes (the carried dirtyRollupBuckets set is untouched —
	// it is writer-lifetime state that refreshRollups prunes as buckets close).
	wr.rollupTouchedThisBatch = false
	wr.rollupMaterializedThisRefresh = false

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx (idle refresh): %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && w.logger != nil {
				w.logger.Warn("worker: rollback failed (idle refresh)", "err", rbErr)
			}
		}
	}()

	if err := wr.refreshRollups(ctx, tx); err != nil {
		return fmt.Errorf("idle refresh: %w", err)
	}

	// Nothing closed since the last tick (carried buckets are all still open):
	// refreshRollups wrote no rows, so roll back the empty tx (the deferred
	// Rollback fires) and skip the notify/prune/commit. This keeps an
	// open-but-pending lull from churning an empty prune-only tx every interval;
	// the buckets stay carried and the timer keeps ticking until one closes.
	if !wr.rollupMaterializedThisRefresh {
		return nil
	}

	// Emit exactly one stats_invalidated — this pass materialized a bucket. No
	// events were applied, so affectedSessionIDs is empty and rollupTouchedThisBatch
	// is false — emitNotify therefore fires solely on rollupMaterializedThisRefresh.
	commitTS := time.Now().UTC().UnixMicro()
	if err := wr.emitNotify(ctx, tx, commitTS); err != nil {
		return fmt.Errorf("idle refresh notify: %w", err)
	}
	if err := pruneNotify(ctx, tx, commitTS); err != nil {
		return fmt.Errorf("idle refresh prune: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit (idle refresh): %w", err)
	}
	committed = true
	// Apply the carried-set removals for buckets materialized in this committed
	// tx (deferred from refreshRollups). On the early no-materialization return
	// above the pending map is empty, so this is a harmless no-op there.
	wr.promoteMaterializedRollupBuckets()
	return nil
}

func (w *worker) report(err error) {
	if w.onErr != nil {
		w.onErr(err)
		return
	}
	if w.logger != nil {
		w.logger.Error("worker: batch failed",
			"source_id", w.sourceID,
			"err", err)
	}
}

// ensureSourceRow inserts the source row if it does not yet exist. The
// ingester creates one row per source on first batch flush. fts5IndexLogs is
// the resolved per-source FTS5 log-indexing flag; it is persisted on both
// insert and conflict-update because the ingester option is the runtime source
// of truth (a daemon restart re-asserts the configured value over whatever a
// prior run stored). The persisted flag gates fts_logs indexing: the FTS
// backfill (indexableLogsForFTSQuery) and /api/search (searchLogs) both filter
// on src.fts5_index_logs = 1 (data-model.md §Full-text search).
func ensureSourceRow(ctx context.Context, tx *sql.Tx, sourceID, format, location string, fts5IndexLogs bool) error {
	ftsFlag := 0
	if fts5IndexLogs {
		ftsFlag = 1
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sources (id, format, location, enabled, fts5_index_logs, created_at)
VALUES (?, ?, ?, 1, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    location        = excluded.location,
    fts5_index_logs = excluded.fts5_index_logs
`, sourceID, format, location, ftsFlag, time.Now().UTC().UnixMicro()); err != nil {
		return fmt.Errorf("ensure source row: %w", err)
	}
	return nil
}

// upsertSourceProgress advances source_progress.last_seq and optionally
// the cursor. seq may be zero when a batch contained only
// SourceProgressEvents (which carry no SourceSeq); in that case
// last_seq is left unchanged and only cursor + updated_at advance.
func upsertSourceProgress(ctx context.Context, tx *sql.Tx, sourceID string, seq uint64, lastTs int64, cursor string, hasCursor bool) error {
	if seq == 0 && !hasCursor {
		// Nothing to checkpoint.
		return nil
	}
	// Clamp the uint64 to MaxInt64 so SQLite's INTEGER column can carry
	// it. Adapter SourceSeq is bit-packed (ledgerSeq << 12 | subIdx)
	// and never approaches 2^63 in realistic data; the clamp is
	// defence in depth and silences G115.
	seqI64 := int64(seq & 0x7FFFFFFFFFFFFFFF)
	if hasCursor {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO source_progress (source_id, last_seq, last_ts_us, cursor, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (source_id) DO UPDATE SET
    last_seq   = MAX(source_progress.last_seq, excluded.last_seq),
    last_ts_us = MAX(source_progress.last_ts_us, excluded.last_ts_us),
    cursor     = excluded.cursor,
    updated_at = excluded.updated_at
`, sourceID, seqI64, lastTs, cursor, time.Now().UTC().UnixMicro()); err != nil {
			return fmt.Errorf("source_progress upsert (cursor): %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (source_id) DO UPDATE SET
    last_seq   = MAX(source_progress.last_seq, excluded.last_seq),
    last_ts_us = MAX(source_progress.last_ts_us, excluded.last_ts_us),
    updated_at = excluded.updated_at
`, sourceID, seqI64, lastTs, time.Now().UTC().UnixMicro()); err != nil {
		return fmt.Errorf("source_progress upsert: %w", err)
	}
	return nil
}

// batchMaxTs returns the maximum Ts across the events in batch. Used
// only as a diagnostics aid in source_progress.last_ts_us.
func batchMaxTs(batch []canonical.Event) int64 {
	var maxTs int64
	for _, ev := range batch {
		if ts := ev.EventTs(); ts > maxTs {
			maxTs = ts
		}
	}
	return maxTs
}
