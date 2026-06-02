package ingest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// hourSpanUS is one UTC hour in microseconds — the width of an hourly bucket's
// op window. Mirrors rollups' internal span (kept here because that const is
// package-private). daySpanUS is shared from rollup_backfill.go.
const hourSpanUS = int64(3_600_000_000)

// refreshRollups recomputes rollup_hourly + rollup_daily for exactly the
// buckets this batch touched, inside the caller's batch transaction (so the
// rollups commit atomically with the ops/sessions they summarize, and a crash
// can never leave them partially applied — ingester.md §"Incremental rollup
// refresh").
//
// It must produce byte-identical tables to the one-shot BackfillRollups over
// the same data and the same `now` (the Chunk-6 diff gate asserts this), so it
// shares BackfillRollups' pure rollups fold, its column-reader (scanOpRow /
// scanSessionStart) and the SAME open-bucket cutoffs from data-model.md
// §"Open-bucket rule": rollup_hourly materializes every hour < floor(now,hour);
// rollup_daily materializes every day < floor(now,day) (the open day is never
// derived, even from its closed hours). Hours and days are each driven by an
// INDEPENDENT carried set (dirtyRollupBuckets / dirtyRollupDays), both marked by
// markDirtyRollupBucket: a day is never derived from the dirty hours, so it stays
// tracked after its hours materialize and is itself materialized once IT closes
// (round-8 P1 — otherwise a day whose hours all close while the day is still open
// would be stranded after midnight with no further events).
//
// Recompute is DELETE-then-INSERT per (bucket, source_format), scoped to THIS
// writer's source_format, NOT an overwrite: a dimension value that has since
// fallen into the __other__ tail-collapse must not strand its old per-value
// row. Both halves are scoped to w.sourceFormat: the DELETE clears the
// (bucket, source_format) cell and the re-read re-aggregates EVERY source_id of
// that format in the window — identical to BackfillRollups, which folds ops by
// src.format (the rollup PK is keyed by source_format, never source_id). So two
// sources sharing a source_format in the same bucket (e.g. two --source
// locations of the same format) are summed into one cell, exactly as the
// backfill computes it. A "sibling source" left untouched is therefore a
// sibling of a DIFFERENT source_format; same-format sources are aggregated
// together, by design.
//
// Single-writer discipline (store.OpenWriter pins SetMaxOpenConns(1)): every
// read fully drains its cursor into a slice BEFORE any write on this tx, so a
// read cursor never straddles a write on the one pinned connection.
func (w *writer) refreshRollups(ctx context.Context, tx *sql.Tx) error {
	if len(w.dirtyRollupBuckets) == 0 && len(w.dirtyRollupDays) == 0 {
		return nil
	}
	now := w.now()
	openHourStart := rollups.BucketTS(now, rollups.Hourly)
	openDayStart := rollups.BucketTS(now, rollups.Daily)

	// HOUR pass. Snapshot the dirty hours BEFORE iterating: the loop stages removal
	// of materialized (closed) hours, and mutating the map during range over it is a
	// Go hazard. The snapshot also lets a hour skipped-because-open be RETAINED (left
	// in the map) and carried to the next batch/refresh — without it, a bucket whose
	// ops all arrive during its own open hour would be skipped while open and then
	// never re-marked, leaving the closed bucket permanently un-materialized
	// (round-7 P1).
	dirtyHours := make([]int64, 0, len(w.dirtyRollupBuckets))
	for h := range w.dirtyRollupBuckets {
		dirtyHours = append(dirtyHours, h)
	}
	for _, h := range dirtyHours {
		if h >= openHourStart {
			continue // open hour: never materialized; RETAINED in the dirty set.
		}
		if err := w.recomputeBucket(ctx, tx, h, h+hourSpanUS, rollups.Hourly); err != nil {
			return fmt.Errorf("rollups-refresh: hourly bucket %d: %w", h, err)
		}
		// Closed and materialized IN THIS TX — stage its removal from the carried
		// set, applied only AFTER the tx commits (promoteMaterializedRollupBuckets).
		// Deferring the delete keeps a materialized-then-rolled-back bucket CARRIED
		// (resetBatch discards the staged removal on rollback) so it is retried —
		// without this, an idle refresh-only pass whose commit fails would lose a
		// closed bucket whose DB row never landed (round-7 P1 undercount). The
		// carried set then holds only open/pending hours, so memory stays bounded.
		w.pendingMaterializedBuckets[h] = struct{}{}
		w.rollupMaterializedThisRefresh = true
	}

	// DAY pass — mirrors the hour pass exactly, over the INDEPENDENTLY-carried
	// dirtyRollupDays set (NOT derived from the dirty hours). Marking the day in
	// markDirtyRollupBucket and carrying it here means a day stays tracked even after
	// all of its hours have materialized and left the hour set, so a day that closes
	// during a lull is still materialized by a later refresh/idle pass — the daily
	// fix for the round-7 open→closed gap (round-8 P1). A day still open at refresh
	// time is RETAINED; a closed day is materialized and its removal staged for
	// post-commit promotion (rollback-safe, identical to hours).
	dirtyDays := make([]int64, 0, len(w.dirtyRollupDays))
	for d := range w.dirtyRollupDays {
		dirtyDays = append(dirtyDays, d)
	}
	for _, d := range dirtyDays {
		if d >= openDayStart {
			continue // open day: never materialized in rollup_daily; RETAINED.
		}
		if err := w.recomputeBucket(ctx, tx, d, d+daySpanUS, rollups.Daily); err != nil {
			return fmt.Errorf("rollups-refresh: daily bucket %d: %w", d, err)
		}
		w.pendingMaterializedDays[d] = struct{}{}
		w.rollupMaterializedThisRefresh = true
	}
	return nil
}

// recomputeBucket rebuilds one (bucket, granularity) for this writer's
// source_format: DELETE the bucket's rows for this source_format, re-read ALL
// of that format's ops + session-starts in [bucketStart, bucketEnd), fold them
// with the pure rollups package, and INSERT the result. DELETE and re-read are
// both scoped by source_format (symmetric), so the cell is rebuilt from every
// source_id of the format — matching BackfillRollups. The DELETE runs first so
// the subsequent upsert never hits a conflict (the ON CONFLICT clause is then a
// harmless no-op), keeping a single write path. Reads are fully drained into
// slices before the writes (single-connection discipline).
func (w *writer) recomputeBucket(ctx context.Context, tx *sql.Tx, bucketStart, bucketEnd int64, bucket rollups.Bucket) error {
	ops, err := w.loadWindowOps(ctx, tx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	starts, err := w.loadWindowSessionStarts(ctx, tx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}

	if err := w.deleteBucketRows(ctx, tx, bucket, bucketStart); err != nil {
		return err
	}
	rows := rollups.Rollup(ops, starts, bucket, rollups.Options{})
	if err := upsertRollups(ctx, tx, bucket, rows); err != nil {
		return err
	}
	return nil
}

// windowOpsQuery reads ALL ops of THIS source_format whose start falls in
// [?, ?), in the SAME deterministic (start_ts, id) order as the backfill so
// float folds match bit-for-bit. Format-scoped via src.format (rollupOpSelectFrom
// already JOINs sources src) — INTENTIONALLY aggregating every source_id of the
// format, matching the by-source_format rollup PK and BackfillRollups, and
// symmetric with the by-source_format DELETE.
var windowOpsQuery = rollupOpSelectFrom + `
WHERE src.format = ? AND o.start_ts >= ? AND o.start_ts < ?
ORDER BY o.start_ts ASC, o.id ASC`

// loadWindowOps reads this source_format's ops in [start, end) into a slice,
// fully draining the cursor before the caller writes (single-connection
// discipline).
func (w *writer) loadWindowOps(ctx context.Context, tx *sql.Tx, start, end int64) ([]rollups.OpRow, error) {
	rows, err := tx.QueryContext(ctx, windowOpsQuery, w.sourceFormat, start, end)
	if err != nil {
		return nil, fmt.Errorf("rollups-refresh: query window ops: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ops []rollups.OpRow
	for rows.Next() {
		op, err := scanOpRow(rows)
		if err != nil {
			return nil, fmt.Errorf("rollups-refresh: scan window op: %w", err)
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rollups-refresh: iterate window ops: %w", err)
	}
	return ops, nil
}

// windowSessionStartsQuery reads ALL session starts of THIS source_format whose
// start_ts falls in [?, ?). Format-scoped via src.format (sessionStartSelectFrom
// JOINs sources src), aggregating every source_id of the format to match
// BackfillRollups and the by-source_format DELETE; reuses the shared column
// shape so scanSessionStart maps it identically to the backfill.
var windowSessionStartsQuery = sessionStartSelectFrom + `
WHERE src.format = ? AND s.start_ts >= ? AND s.start_ts < ?`

// loadWindowSessionStarts reads this source_format's session starts in
// [start, end) into a slice, fully draining the cursor before the caller writes.
func (w *writer) loadWindowSessionStarts(ctx context.Context, tx *sql.Tx, start, end int64) ([]rollups.SessionStart, error) {
	rows, err := tx.QueryContext(ctx, windowSessionStartsQuery, w.sourceFormat, start, end)
	if err != nil {
		return nil, fmt.Errorf("rollups-refresh: query window session starts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var starts []rollups.SessionStart
	for rows.Next() {
		ss, err := scanSessionStart(rows)
		if err != nil {
			return nil, fmt.Errorf("rollups-refresh: scan window session start: %w", err)
		}
		starts = append(starts, ss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rollups-refresh: iterate window session starts: %w", err)
	}
	return starts, nil
}

// rollupDeleteSQL removes one bucket's rows for one source_format before the
// recompute reinserts them. Scoping to source_format keeps a DIFFERENT-format
// sibling's rows in the same bucket intact (same-format sources share the cell
// and are re-aggregated together by the by-format re-read — symmetric with this
// DELETE). The table name is rollupTable(bucket), a fixed literal (never user
// input); both VALUES are ?-bound.
const rollupDeleteSQL = `DELETE FROM %s WHERE bucket_ts = ? AND source_format = ?`

// deleteBucketRows clears this source_format's rows for the given bucket so the
// recompute's INSERT starts from a clean slate (delete-then-insert, NOT
// overwrite — a value that fell into __other__ must not strand its old row).
// Symmetric with the by-source_format window re-read in recomputeBucket.
func (w *writer) deleteBucketRows(ctx context.Context, tx *sql.Tx, bucket rollups.Bucket, bucketStart int64) error {
	// #nosec G201 -- table is rollupTable(bucket), a fixed literal switch on a
	// Go enum (never user input); both predicate values are ?-bound.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(rollupDeleteSQL, rollupTable(bucket)), bucketStart, w.sourceFormat); err != nil {
		return fmt.Errorf("rollups-refresh: delete %s bucket %d: %w", rollupTable(bucket), bucketStart, err)
	}
	return nil
}
