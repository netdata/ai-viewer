package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// SourceReadModelRepairStats summarizes a source-scoped repair pass over rows
// that were committed while read-model refresh was deferred.
type SourceReadModelRepairStats struct {
	FTSOpRows     int
	FTSLogRows    int
	HourlyBuckets int
	DailyBuckets  int
	Elapsed       time.Duration
}

// RepairSourceReadModels rebuilds read-model rows affected by one source's
// committed primary rows. It is the per-source sibling of BackfillReadModels:
// FTS repair is source-scoped, while rollup repair recomputes the touched
// (bucket, source_format) cells because rollup tables are keyed by format.
func (i *Ingester) RepairSourceReadModels(ctx context.Context, sourceID string) (SourceReadModelRepairStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sourceID == "" {
		return SourceReadModelRepairStats{}, errors.New("ingest: source read-model repair source id is empty")
	}
	start := time.Now()

	i.backfillMu.Lock()
	defer i.backfillMu.Unlock()

	sourceFormat, err := sourceFormatForReadModelRepair(ctx, i.db, sourceID)
	if err != nil {
		return SourceReadModelRepairStats{}, err
	}
	if sourceFormat == "" {
		return SourceReadModelRepairStats{}, nil
	}

	if i.logger != nil {
		i.logger.Info("ai-viewer-ingest: repairing source read models",
			"source_id", sourceID, "source_format", sourceFormat)
	}

	ftsOps, err := repairSourceFTSOps(ctx, i.db, sourceID)
	if err != nil {
		return SourceReadModelRepairStats{}, fmt.Errorf("repair source FTS ops: %w", err)
	}
	ftsLogs, err := repairSourceFTSLogs(ctx, i.db, sourceID)
	if err != nil {
		return SourceReadModelRepairStats{}, fmt.Errorf("repair source FTS logs: %w", err)
	}
	hourly, daily, err := repairSourceRollups(ctx, i.db, sourceID, sourceFormat, i.now(), i.pricer)
	if err != nil {
		return SourceReadModelRepairStats{}, fmt.Errorf("repair source rollups: %w", err)
	}

	stats := SourceReadModelRepairStats{
		FTSOpRows:     ftsOps,
		FTSLogRows:    ftsLogs,
		HourlyBuckets: hourly,
		DailyBuckets:  daily,
		Elapsed:       time.Since(start),
	}
	if i.logger != nil {
		i.logger.Info("ai-viewer-ingest: source read models repaired",
			"source_id", sourceID,
			"source_format", sourceFormat,
			"fts_op_rows", stats.FTSOpRows,
			"fts_log_rows", stats.FTSLogRows,
			"hourly_buckets", stats.HourlyBuckets,
			"daily_buckets", stats.DailyBuckets,
			"elapsed_s", stats.Elapsed.Seconds())
	}
	return stats, nil
}

func sourceFormatForReadModelRepair(ctx context.Context, db *sql.DB, sourceID string) (string, error) {
	var format string
	err := db.QueryRowContext(ctx, `SELECT format FROM sources WHERE id = ?`, sourceID).Scan(&format)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("repair source read models: load source format: %w", err)
	}
	return format, nil
}

func repairSourceRollups(ctx context.Context, db *sql.DB, sourceID, sourceFormat string, now int64, pricer Pricer) (int, int, error) {
	openHourStart := rollups.BucketTS(now, rollups.Hourly)
	openDayStart := rollups.BucketTS(now, rollups.Daily)

	hours, err := loadSourceRollupBuckets(ctx, db, sourceHourlyRollupBucketsQuery, hourSpanUS, sourceID, openHourStart)
	if err != nil {
		return 0, 0, fmt.Errorf("load hourly buckets: %w", err)
	}
	days, err := loadSourceRollupBuckets(ctx, db, sourceDailyRollupBucketsQuery, daySpanUS, sourceID, openDayStart)
	if err != nil {
		return 0, 0, fmt.Errorf("load daily buckets: %w", err)
	}

	wr := newWriter(sourceID, sourceFormat, "", pricer)
	wr.now = func() int64 { return now }
	if err := repairRollupBuckets(ctx, db, wr, rollups.Hourly, hours); err != nil {
		return 0, 0, err
	}
	if err := repairRollupBuckets(ctx, db, wr, rollups.Daily, days); err != nil {
		return 0, 0, err
	}
	return len(hours), len(days), nil
}

const sourceHourlyRollupBucketsQuery = `
SELECT DISTINCT bucket
FROM (
    SELECT (o.start_ts / ?) * ? AS bucket
    FROM ops o
    JOIN sessions s ON o.session_id = s.id
    WHERE s.source_id = ? AND o.start_ts < ?
    UNION ALL
    SELECT (s.start_ts / ?) * ? AS bucket
    FROM sessions s
    WHERE s.source_id = ? AND s.start_ts < ?
)
ORDER BY bucket ASC`

const sourceDailyRollupBucketsQuery = `
SELECT DISTINCT bucket
FROM (
    SELECT (o.start_ts / ?) * ? AS bucket
    FROM ops o
    JOIN sessions s ON o.session_id = s.id
    WHERE s.source_id = ? AND o.start_ts < ?
    UNION ALL
    SELECT (s.start_ts / ?) * ? AS bucket
    FROM sessions s
    WHERE s.source_id = ? AND s.start_ts < ?
)
ORDER BY bucket ASC`

func loadSourceRollupBuckets(ctx context.Context, db *sql.DB, query string, spanUS int64, sourceID string, openFrom int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx, query, spanUS, spanUS, sourceID, openFrom, spanUS, spanUS, sourceID, openFrom)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var buckets []int64
	for rows.Next() {
		var bucket int64
		if err := rows.Scan(&bucket); err != nil {
			return nil, err
		}
		buckets = append(buckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buckets, nil
}

func repairRollupBuckets(ctx context.Context, db *sql.DB, wr *writer, bucket rollups.Bucket, buckets []int64) error {
	spanUS := hourSpanUS
	if bucket == rollups.Daily {
		spanUS = daySpanUS
	}
	for _, bucketStart := range buckets {
		if err := repairRollupBucket(ctx, db, wr, bucket, bucketStart, spanUS); err != nil {
			return err
		}
	}
	return nil
}

func repairRollupBucket(ctx context.Context, db *sql.DB, wr *writer, bucket rollups.Bucket, bucketStart, spanUS int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("source rollup repair: begin %s bucket %d: %w", rollupTable(bucket), bucketStart, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := wr.recomputeBucket(ctx, tx, bucketStart, bucketStart+spanUS, bucket); err != nil {
		return fmt.Errorf("source rollup repair: recompute %s bucket %d: %w", rollupTable(bucket), bucketStart, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("source rollup repair: commit %s bucket %d: %w", rollupTable(bucket), bucketStart, err)
	}
	return nil
}
