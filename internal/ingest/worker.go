package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
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
	// metaJSON is the resolved adapter-owned JSON metadata blob the ingester
	// computed from WithSourceMeta (default ""). ensureSourceRow persists it
	// on the sources.meta_json column; the empty string binds NULL — the
	// "not populated" signal the presenter honors by omitting the meta
	// field. SOW-0024.
	metaJSON   string
	events     <-chan canonical.Event
	db         *sql.DB
	hwm        *hwmCache
	pricer     Pricer
	logger     *slog.Logger
	batchSize  int
	batchEvery time.Duration
	// now supplies the wall-clock cutoff the incremental rollup refresh uses
	// to pick its open hour/day. Injectable for deterministic tests; defaults
	// to defaultNow when the worker is built without one.
	now func() int64
	// deferReadModels points at the ingester's bulk-scan flag (SOW-0063). When
	// true, refreshBatchReadModels skips refreshRollups + refreshFTS (the two
	// super-linear-in-volume refreshes) and runs only refreshAggregates (the
	// cheap session-count update). The binary clears this after the initial
	// Scan completes and runs BackfillReadModels to build the deferred models.
	deferReadModels *atomic.Bool
	// onErr is invoked for batch-level failures (commit errors, etc.).
	// Defaults to logger.Error if not set.
	onErr func(error)
}

// flush opens a transaction, applies every event in batch via the
// writer, refreshes aggregates, persists source_progress, and commits.
// On any error the transaction rolls back and the error is returned to
// the caller; the batch is dropped (we do not retry — see
// ingester.md §Failure Recovery).
func (w *worker) flush(ctx context.Context, wr *writer, batch []canonical.Event) error {
	wr.fts5IndexLogs = w.fts5IndexLogs
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer w.rollbackIfUncommitted(tx, &committed, "worker: rollback failed")

	if err := w.writeBatchRows(ctx, tx, wr, batch); err != nil {
		return err
	}
	if err := wr.refreshBatchReadModels(ctx, tx); err != nil {
		return err
	}
	if err := w.writeBatchProgressAndNotify(ctx, tx, wr, batch); err != nil {
		return err
	}
	if err := commitTx(tx, &committed, "commit"); err != nil {
		return err
	}
	w.promoteCommittedBatch(wr)
	return nil
}

func (w *worker) writeBatchRows(ctx context.Context, tx *sql.Tx, wr *writer, batch []canonical.Event) error {
	if err := ensureSourceRow(ctx, tx, w.sourceID, w.sourceFormat, w.location, w.fts5IndexLogs, w.metaJSON); err != nil {
		return err
	}
	for _, ev := range batch {
		if err := wr.apply(ctx, tx, ev); err != nil {
			return fmt.Errorf("apply event %s seq=%d: %w", ev.EventKind(), ev.EventSourceSeq(), err)
		}
	}
	return nil
}

func (w *worker) writeBatchProgressAndNotify(ctx context.Context, tx *sql.Tx, wr *writer, batch []canonical.Event) error {
	if err := upsertSourceProgress(ctx, tx, w.sourceID, wr.batchMaxSeq, batchMaxTs(batch), wr.lastCursor, wr.hasCursor); err != nil {
		return err
	}
	return wr.emitNotifyAndPrune(ctx, tx, time.Now().UTC().UnixMicro())
}

func (w *worker) promoteCommittedBatch(wr *writer) {
	wr.promotePendingMissDedup()
	wr.promoteMaterializedRollupBuckets()
	if wr.batchMaxSeq > 0 {
		w.hwm.Advance(w.sourceID, wr.batchMaxSeq)
	}
	w.logObservabilityErrs(wr)
}

func (w *worker) logObservabilityErrs(wr *writer) {
	if w.logger != nil {
		for _, oerr := range wr.drainObservabilityErrs() {
			w.logger.Warn("worker: observability hook failed",
				"source_id", w.sourceID,
				"err", oerr)
		}
	}
}

func (w *writer) refreshBatchReadModels(ctx context.Context, tx *sql.Tx) error {
	// Bulk-scan fast path (SOW-0063): skip the two super-linear refreshes
	// (rollups + FTS) during the initial scan; the binary backfills them once
	// after Scan returns. refreshAggregates (cheap session-count update) still
	// runs so the UI shows correct counts during the scan.
	if w.deferReadModels != nil && w.deferReadModels.Load() {
		return refreshAggregates(ctx, tx, w.dirtyTurnIDs, w.dirtySessionIDs)
	}
	if err := w.refreshRollups(ctx, tx); err != nil {
		return err
	}
	if err := w.refreshFTS(ctx, tx); err != nil {
		return err
	}
	return refreshAggregates(ctx, tx, w.dirtyTurnIDs, w.dirtySessionIDs)
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
	wr.clearRollupNotifySignals()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx (idle refresh): %w", err)
	}
	committed := false
	defer w.rollbackIfUncommitted(tx, &committed, "worker: rollback failed (idle refresh)")

	if err := wr.refreshRollups(ctx, tx); err != nil {
		return fmt.Errorf("idle refresh: %w", err)
	}
	if !wr.rollupMaterializedThisRefresh {
		return nil
	}
	if err := wr.emitIdleRefreshNotify(ctx, tx); err != nil {
		return err
	}
	if err := commitTx(tx, &committed, "commit (idle refresh)"); err != nil {
		return err
	}
	wr.promoteMaterializedRollupBuckets()
	return nil
}

func (w *writer) clearRollupNotifySignals() {
	w.rollupTouchedThisBatch = false
	w.rollupMaterializedThisRefresh = false
}

func (w *writer) emitIdleRefreshNotify(ctx context.Context, tx *sql.Tx) error {
	commitTS := time.Now().UTC().UnixMicro()
	if err := w.emitNotify(ctx, tx, commitTS); err != nil {
		return fmt.Errorf("idle refresh notify: %w", err)
	}
	if err := pruneNotify(ctx, tx, commitTS); err != nil {
		return fmt.Errorf("idle refresh prune: %w", err)
	}
	return nil
}

func (w *writer) emitNotifyAndPrune(ctx context.Context, tx *sql.Tx, commitTS int64) error {
	if err := w.emitNotify(ctx, tx, commitTS); err != nil {
		return err
	}
	return pruneNotify(ctx, tx, commitTS)
}

func (w *worker) rollbackIfUncommitted(tx *sql.Tx, committed *bool, msg string) {
	if *committed {
		return
	}
	if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && w.logger != nil {
		w.logger.Warn(msg, "err", rbErr)
	}
}

func commitTx(tx *sql.Tx, committed *bool, msg string) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	*committed = true
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
// on src.fts5_index_logs = 1 (data-model.md §Full-text search). metaJSON is
// the resolved per-source adapter-owned JSON metadata blob (SOW-0024); the
// empty string binds NULL to the column — the absence = "not populated" signal
// the presenter honors by omitting the meta field. It is persisted on both
// insert and conflict-update, mirroring fts5IndexLogs, for the same
// restart-reasserts-runtime-truth reason.
func ensureSourceRow(ctx context.Context, tx *sql.Tx, sourceID, format, location string, fts5IndexLogs bool, metaJSON string) error {
	ftsFlag := 0
	if fts5IndexLogs {
		ftsFlag = 1
	}
	metaArg := sql.NullString{String: metaJSON, Valid: metaJSON != ""}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sources (id, format, location, enabled, fts5_index_logs, meta_json, created_at)
VALUES (?, ?, ?, 1, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    location        = excluded.location,
    fts5_index_logs = excluded.fts5_index_logs,
    meta_json       = excluded.meta_json
`, sourceID, format, location, ftsFlag, metaArg, time.Now().UTC().UnixMicro()); err != nil {
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
