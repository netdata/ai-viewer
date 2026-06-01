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
// derived, even from its closed hours).
//
// Recompute is DELETE-then-INSERT per (bucket, source_format), scoped to THIS
// writer's source_format, NOT an overwrite: a dimension value that has since
// fallen into the __other__ tail-collapse must not strand its old per-value
// row. Scoping to w.sourceFormat keeps a sibling source's rows in the same
// bucket untouched.
//
// Single-writer discipline (store.OpenWriter pins SetMaxOpenConns(1)): every
// read fully drains its cursor into a slice BEFORE any write on this tx, so a
// read cursor never straddles a write on the one pinned connection.
func (w *writer) refreshRollups(ctx context.Context, tx *sql.Tx) error {
	if len(w.dirtyRollupBuckets) == 0 {
		return nil
	}
	now := w.now()
	openHourStart := rollups.BucketTS(now, rollups.Hourly)
	openDayStart := rollups.BucketTS(now, rollups.Daily)

	// Collect the affected days while recomputing each closed dirty hour. A
	// dirty hour at/after the open-hour cutoff is skipped (the open hour is
	// never materialized), but its DAY is still considered — its other,
	// already-closed hours may need a daily recompute.
	days := make(map[int64]struct{}, len(w.dirtyRollupBuckets))
	for h := range w.dirtyRollupBuckets {
		days[rollups.BucketTS(h, rollups.Daily)] = struct{}{}
		if h >= openHourStart {
			continue // open hour: never materialized.
		}
		if err := w.recomputeBucket(ctx, tx, h, h+hourSpanUS, rollups.Hourly); err != nil {
			return fmt.Errorf("rollups-refresh: hourly bucket %d: %w", h, err)
		}
	}
	for d := range days {
		if d >= openDayStart {
			continue // open day: never materialized in rollup_daily.
		}
		if err := w.recomputeBucket(ctx, tx, d, d+daySpanUS, rollups.Daily); err != nil {
			return fmt.Errorf("rollups-refresh: daily bucket %d: %w", d, err)
		}
	}
	return nil
}

// recomputeBucket rebuilds one (bucket, granularity) for this writer's source:
// DELETE the bucket's rows for this source_format, re-read this source's ops +
// session-starts in [bucketStart, bucketEnd), fold them with the pure rollups
// package, and INSERT the result. The DELETE runs first so the subsequent
// upsert never hits a conflict (the ON CONFLICT clause is then a harmless
// no-op), keeping a single write path. Reads are fully drained into slices
// before the writes (single-connection discipline).
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

// windowOpsQuery reads THIS source's ops whose start falls in [?, ?), in the
// SAME deterministic (start_ts, id) order as the backfill so float folds match
// bit-for-bit. Source-scoped via s.source_id (ops carry no source_id; the join
// to sessions provides it).
var windowOpsQuery = rollupOpSelectFrom + `
WHERE s.source_id = ? AND o.start_ts >= ? AND o.start_ts < ?
ORDER BY o.start_ts ASC, o.id ASC`

// loadWindowOps reads this source's ops in [start, end) into a slice, fully
// draining the cursor before the caller writes (single-connection discipline).
func (w *writer) loadWindowOps(ctx context.Context, tx *sql.Tx, start, end int64) ([]rollups.OpRow, error) {
	rows, err := tx.QueryContext(ctx, windowOpsQuery, w.sourceID, start, end)
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

// windowSessionStartsQuery reads THIS source's session starts whose start_ts
// falls in [?, ?). Source-scoped via s.source_id; reuses the shared column
// shape so scanSessionStart maps it identically to the backfill.
var windowSessionStartsQuery = sessionStartSelectFrom + `
WHERE s.source_id = ? AND s.start_ts >= ? AND s.start_ts < ?`

// loadWindowSessionStarts reads this source's session starts in [start, end)
// into a slice, fully draining the cursor before the caller writes.
func (w *writer) loadWindowSessionStarts(ctx context.Context, tx *sql.Tx, start, end int64) ([]rollups.SessionStart, error) {
	rows, err := tx.QueryContext(ctx, windowSessionStartsQuery, w.sourceID, start, end)
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
// recompute reinserts them. Scoping to source_format keeps sibling sources'
// rows in the same bucket intact. The table name is rollupTable(bucket), a
// fixed literal (never user input); both VALUES are ?-bound.
const rollupDeleteSQL = `DELETE FROM %s WHERE bucket_ts = ? AND source_format = ?`

// deleteBucketRows deletes this source's rows for the given bucket so the
// recompute's INSERT starts from a clean slate (delete-then-insert, NOT
// overwrite — a value that fell into __other__ must not strand its old row).
func (w *writer) deleteBucketRows(ctx context.Context, tx *sql.Tx, bucket rollups.Bucket, bucketStart int64) error {
	// #nosec G201 -- table is rollupTable(bucket), a fixed literal switch on a
	// Go enum (never user input); both predicate values are ?-bound.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(rollupDeleteSQL, rollupTable(bucket)), bucketStart, w.sourceFormat); err != nil {
		return fmt.Errorf("rollups-refresh: delete %s bucket %d: %w", rollupTable(bucket), bucketStart, err)
	}
	return nil
}
