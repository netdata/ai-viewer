package opencode

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the BATCHED delta→emit→checkpoint processor (SOW-0005 P1.1,
// data-loss fix). It is split out of tailer_changes.go to keep each file ≤400
// lines. The checkpoint-after-emit invariant is documented on processChanges
// (tailer_changes.go): a SourceProgress checkpoint carrying cursor W is emitted
// ONLY after every session affected by rows ≤ W has been reloaded, mapped, and
// emitted.
//
// A batch spans the tracked tables with ONE shared row budget (≈progressEveryRows
// rows TOTAL, not per-table) so a session touched by several tables in the same
// run is reloaded+emitted ONCE per batch (cross-table dedupe), exactly as the
// pre-P1.1 single-pass did — only the checkpoint timing changed. A part's owning
// session is its REQUIRED denormalized session_id (resolvePartSession), so the
// batch no longer threads a message→session map (the old-schema fallback that
// needed one was unreachable and removed — SOW-0005 round-6 P3-2).

// batchProcessor drives the batched delta→emit→checkpoint loop for one run. It
// owns the running committed cursor (advanced only after a batch's sessions are
// emitted AND checkpointed) and the per-table scan position (the watermark each
// table has been paged to so far, which may be ahead of committed mid-batch).
type batchProcessor struct {
	db       *sql.DB
	schema   schemaSet
	sourceID string
	out      chan<- canonical.Event
	logger   *slog.Logger
	onError  func(error)

	// committed is the last cursor whose every affected session has been emitted.
	// It is what a restart safely resumes from; it advances ONLY in commitBatch,
	// after reloadAndEmit succeeds.
	committed Cursor
	// scanned tracks, per table, the watermark paging has reached. It starts at
	// committed and runs ahead of it within a batch; commitBatch promotes it into
	// committed once the batch's sessions are emitted.
	scanned map[string]TableWatermark
	// done marks tables fully paged (a short page was seen), so later batches skip
	// them.
	done map[string]bool
	// advanced reports whether any watermark advanced across the whole run.
	advanced bool
}

// run pages every tracked table forward in bounded cross-table batches, emitting
// and checkpointing each batch before the next. It loops until every table is
// fully paged.
func (bp *batchProcessor) run(ctx context.Context) error {
	bp.scanned = map[string]TableWatermark{}
	bp.done = map[string]bool{}
	for _, table := range trackedTables {
		bp.scanned[table] = bp.committed.Tables[table]
	}
	for !bp.allDone() {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := bp.collectBatch(ctx)
		if err != nil {
			return err
		}
		if batch.rowCount == 0 {
			return nil // nothing left across any table
		}
		if err := bp.commitBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

// allDone reports whether every tracked table has been fully paged.
func (bp *batchProcessor) allDone() bool {
	for _, table := range trackedTables {
		if !bp.done[table] {
			return false
		}
	}
	return true
}

// batchResult is one cross-table batch's outcome: the affected session ids
// (first-seen order, deduped across the tables in this batch) and the row count
// (0 ⇒ nothing left). The advanced per-table watermarks live in bp.scanned.
type batchResult struct {
	affected []string
	rowCount int
}

// collectBatch pages the tracked tables in order, accumulating rows into ONE
// shared affected set until the shared budget (progressEveryRows rows total) is
// reached or every table is exhausted. A table that returns a short page is
// marked done so subsequent batches skip it. The per-table watermark advances in
// bp.scanned as pages are read; commitBatch promotes it into committed.
func (bp *batchProcessor) collectBatch(ctx context.Context) (batchResult, error) {
	bc := &batchCollector{bp: bp, affected: newAffectedSet()}
	for _, table := range trackedTables {
		if err := bc.collectTable(ctx, table); err != nil {
			return bc.result(), err
		}
		if bc.budgetSpent() {
			break
		}
	}
	return bc.result(), nil
}

type batchCollector struct {
	bp       *batchProcessor
	affected *affectedSet
	total    int
}

func (bc *batchCollector) collectTable(ctx context.Context, table string) error {
	if bc.bp.done[table] {
		return nil
	}
	sink := &warnSink{}
	onRow := deltaRowHandler(table, bc.bp.schema[table], bc.affected, sink.collect)
	query := bc.bp.schema[table].buildSelect()
	for !bc.budgetSpent() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if done, err := bc.collectPage(ctx, table, query, onRow, sink); done || err != nil {
			return err
		}
	}
	return nil
}

func (bc *batchCollector) collectPage(ctx context.Context, table, query string, onRow func(*sql.Rows) (rowKey, error), sink *warnSink) (bool, error) {
	page, err := scanOnePage(ctx, bc.bp.db, query, bc.bp.scanned[table], onRow, sink, bc.bp.onError)
	if err != nil {
		return false, err
	}
	bc.total += page.n
	if page.n > 0 {
		bc.bp.scanned[table] = page.watermark
	}
	if page.n < deltaPageLimit {
		bc.bp.done[table] = true
		return true, nil
	}
	return false, nil
}

func (bc *batchCollector) budgetSpent() bool {
	return bc.total >= progressEveryRows
}

func (bc *batchCollector) result() batchResult {
	return batchResult{affected: bc.affected.ids(), rowCount: bc.total}
}

// commitBatch reloads+emits the batch's affected sessions, THEN advances the
// committed cursor to the scanned watermark for every table that moved and
// checkpoints — the checkpoint-after-emit invariant. The watermark is promoted
// only when it genuinely moved (watermarkAdvanced), so a re-observed batch never
// spuriously flips `advanced`; a checkpoint is emitted only when something
// advanced.
func (bp *batchProcessor) commitBatch(ctx context.Context, batch batchResult) error {
	if len(batch.affected) > 0 {
		if err := reloadAndEmit(ctx, bp.db, bp.schema, bp.sourceID, batch.affected, bp.out, bp.logger, bp.onError); err != nil {
			return err
		}
	}
	moved := false
	for _, table := range trackedTables {
		if watermarkAdvanced(bp.committed.Tables[table], bp.scanned[table]) {
			bp.committed = bp.committed.withTable(table, bp.scanned[table])
			moved = true
		}
	}
	if moved {
		bp.advanced = true
		return emitProgress(ctx, bp.sourceID, bp.committed, bp.out)
	}
	return nil
}
