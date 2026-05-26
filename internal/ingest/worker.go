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
	events       <-chan canonical.Event
	db           *sql.DB
	hwm          *hwmCache
	pricer       Pricer
	logger       *slog.Logger
	batchSize    int
	batchEvery   time.Duration
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

	flush := func(reason string, flushCtx context.Context) {
		if len(batch) == 0 {
			return
		}
		if err := w.flush(flushCtx, wr, batch); err != nil {
			w.report(fmt.Errorf("flush (%s): %w", reason, err))
		}
		batch = batch[:0]
		wr.resetBatch()
		if timerArmed {
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			timerArmed = false
		}
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
				if !w.hwm.IsAfter(ev.EventSourceID(), ev.EventSourceSeq()) && ev.EventSourceSeq() != 0 {
					continue
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
					// Mirror the main-loop dedup: drop events at or
					// below HWM; the seq==0 carve-out is for SourceProgress
					// which the main loop also lets through.
					if !w.hwm.IsAfter(ev.EventSourceID(), ev.EventSourceSeq()) && ev.EventSourceSeq() != 0 {
						break
					}
					batch = append(batch, ev)
				}
			default:
			}
			flush("ctx done", drainCtx)
			drainCancel()
			return
		case ev, ok := <-w.events:
			if !ok {
				// Producer closed the channel. Use a fresh context for
				// the final flush so a parent cancellation that arrived
				// concurrently does not abort BeginTx.
				drainCtx, drainCancel := context.WithTimeout(context.Background(), shutdownDrainTimeout)
				flush("channel closed", drainCtx)
				drainCancel()
				return
			}
			if !w.hwm.IsAfter(ev.EventSourceID(), ev.EventSourceSeq()) && ev.EventSourceSeq() != 0 {
				// Dropped by dedup. SourceProgressEvent uses SourceSeq=0
				// by convention; let it through so the cursor advances.
				continue
			}
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
			flush("interval", ctx)
		}
	}
}

// flush opens a transaction, applies every event in batch via the
// writer, refreshes aggregates, persists source_progress, and commits.
// On any error the transaction rolls back and the error is returned to
// the caller; the batch is dropped (we do not retry — see
// ingester.md §Failure Recovery).
func (w *worker) flush(ctx context.Context, wr *writer, batch []canonical.Event) error {
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
	// source_progress depend on it.
	if err := ensureSourceRow(ctx, tx, w.sourceID, w.sourceFormat, w.location); err != nil {
		return err
	}

	for _, ev := range batch {
		if err := wr.apply(ctx, tx, ev); err != nil {
			return fmt.Errorf("apply event %s seq=%d: %w", ev.EventKind(), ev.EventSourceSeq(), err)
		}
	}

	if err := refreshAggregates(ctx, tx, wr.dirtyTurnIDs, wr.dirtySessionIDs); err != nil {
		return err
	}

	if err := upsertSourceProgress(ctx, tx, w.sourceID, wr.batchMaxSeq, batchMaxTs(batch), wr.lastCursor, wr.hasCursor); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	if wr.batchMaxSeq > 0 {
		w.hwm.Advance(w.sourceID, wr.batchMaxSeq)
	}
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
// ingester creates one row per source on first batch flush.
func ensureSourceRow(ctx context.Context, tx *sql.Tx, sourceID, format, location string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sources (id, format, location, enabled, created_at)
VALUES (?, ?, ?, 1, ?)
ON CONFLICT (id) DO UPDATE SET
    location = excluded.location
`, sourceID, format, location, time.Now().UTC().UnixMicro()); err != nil {
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
	var max int64
	for _, ev := range batch {
		if ts := ev.EventTs(); ts > max {
			max = ts
		}
	}
	return max
}
