package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// BackfillStats summarizes a rollups-backfill run for logging + test
// assertions. All counts are over the rows actually upserted this run.
type BackfillStats struct {
	HourlyRows    int           // rollup_hourly rows written
	DailyRows     int           // rollup_daily rows written
	DaysProcessed int           // UTC days flushed (had ops and/or session starts)
	Elapsed       time.Duration // wall-clock duration of the backfill
}

// BackfillRollups recomputes rollup_hourly + rollup_daily from ops+sessions.
//
// now is the wall-clock cutoff in UTC microseconds (injected for deterministic
// tests). It applies the independent open-bucket cutoffs from
// data-model.md §"Open-bucket rule": rollup_hourly materializes every hour
// bucket strictly before floor(now,hour) — including the current day's
// already-closed hours — while rollup_daily materializes every day bucket
// strictly before floor(now,day), excluding the entire open day.
//
// To stay byte-identical to the incremental refresh (the property the diff
// gate asserts) it folds via the pure internal/rollups package, one UTC day
// per window. Memory stays bounded to a single day's ops: the work is
// processed day-by-day, and each day's ops are read and written inside that
// day's own transaction (the writer store pins a single connection, so a
// read cursor must never straddle a separate write transaction).
func BackfillRollups(ctx context.Context, db *sql.DB, now int64, logger *slog.Logger) (BackfillStats, error) {
	start := time.Now()
	openHourStart := rollups.BucketTS(now, rollups.Hourly)
	openDayStart := rollups.BucketTS(now, rollups.Daily)

	// Wipe both rollup tables before rebuilding so this run is a TRUE full
	// recompute, not an overwrite. The backfill is the recovery path "when
	// rollups are missing OR STALE" (ingester.md §One-shot backfill), but the
	// per-day upserts below (ON CONFLICT DO UPDATE) can only refresh rows the
	// recompute still produces — they cannot remove a row it no longer produces
	// (e.g. a dimension value that has since collapsed into __other__, or any row
	// from data that has since changed). Deleting first makes the backfill able
	// to repair stale artifacts, mirroring the incremental path's delete-then-
	// insert (rollup_refresh.go) and BackfillFTS's delete-before-rebuild
	// (fts_backfill.go). Only closed buckets are ever materialized and the open
	// bucket is never materialized, so clearing everything then rebuilding the
	// closed buckets below is correct. Runs in its own short transaction that
	// commits before the per-day loop, keeping the single writer connection
	// (SetMaxOpenConns(1)) uncontended.
	if err := truncateRollups(ctx, db); err != nil {
		return BackfillStats{}, err
	}

	startsByDay, err := loadSessionStarts(ctx, db, openHourStart)
	if err != nil {
		return BackfillStats{}, err
	}
	days, err := closedDays(ctx, db, openHourStart, startsByDay)
	if err != nil {
		return BackfillStats{}, err
	}

	bf := &backfiller{db: db, openHourStart: openHourStart, openDayStart: openDayStart, startsByDay: startsByDay}
	for _, day := range days {
		if err := bf.flushDay(ctx, day); err != nil {
			return BackfillStats{}, err
		}
	}

	stats := BackfillStats{
		HourlyRows:    bf.hourlyRows,
		DailyRows:     bf.dailyRows,
		DaysProcessed: bf.daysProcessed,
		Elapsed:       time.Since(start),
	}
	if stats.DaysProcessed == 0 {
		logger.Info("rollups-backfill: no closed ops or session starts; nothing to materialize",
			"open_hour_start", openHourStart, "open_day_start", openDayStart)
	} else {
		logger.Info("rollups-backfill: materialized closed-bucket rollups",
			"days", stats.DaysProcessed, "hourly_rows", stats.HourlyRows, "daily_rows", stats.DailyRows)
	}
	return stats, nil
}

// truncateRollups empties rollup_hourly and rollup_daily in one short
// transaction so BackfillRollups starts every run from a clean slate (see the
// caller comment for why a delete-before-rebuild is required for stale repair).
// The DELETEs are constant SQL (fixed table names, no parameters, no user
// input); the commit drains before the per-day loop opens its own transactions.
func truncateRollups(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rollups-backfill: begin truncate tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM rollup_hourly`); err != nil {
		return fmt.Errorf("rollups-backfill: clear rollup_hourly: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM rollup_daily`); err != nil {
		return fmt.Errorf("rollups-backfill: clear rollup_daily: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rollups-backfill: commit truncate: %w", err)
	}
	return nil
}

// backfiller carries per-run state. It is not safe for concurrent use; one
// instance serves one BackfillRollups call.
type backfiller struct {
	db            *sql.DB
	openHourStart int64
	openDayStart  int64

	// startsByDay maps a UTC-day bucket → that day's session starts.
	startsByDay map[int64][]rollups.SessionStart

	hourlyRows    int
	dailyRows     int
	daysProcessed int
}

// flushDay materializes one UTC day in a single transaction: every closed
// hourly bucket of the day into rollup_hourly, and (only when the day is fully
// closed) the daily bucket into rollup_daily. The day's ops are read inside
// the same transaction and the cursor is fully drained before any upsert, so
// the single writer connection is never contended.
func (bf *backfiller) flushDay(ctx context.Context, day int64) error {
	tx, err := bf.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rollups-backfill: begin tx for day %d: %w", day, err)
	}
	// Rollback is a no-op once the tx commits; it guards every error return.
	defer func() { _ = tx.Rollback() }()

	ops, err := loadDayOps(ctx, tx, day, bf.openHourStart)
	if err != nil {
		return fmt.Errorf("rollups-backfill: load ops for day %d: %w", day, err)
	}
	starts := bf.startsByDay[day]

	hourly := rollups.Rollup(ops, starts, rollups.Hourly, rollups.Options{})
	if err := upsertRollups(ctx, tx, rollups.Hourly, hourly); err != nil {
		return fmt.Errorf("rollups-backfill: upsert hourly for day %d: %w", day, err)
	}

	var daily []rollups.RollupRow
	if day < bf.openDayStart { // exclude the open day from rollup_daily
		daily = rollups.Rollup(ops, starts, rollups.Daily, rollups.Options{})
		if err := upsertRollups(ctx, tx, rollups.Daily, daily); err != nil {
			return fmt.Errorf("rollups-backfill: upsert daily for day %d: %w", day, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rollups-backfill: commit day %d: %w", day, err)
	}

	bf.hourlyRows += len(hourly)
	bf.dailyRows += len(daily)
	bf.daysProcessed++
	return nil
}

// daySpanUS is one UTC day in microseconds — the window width for a day's ops.
const daySpanUS = int64(86_400_000_000)

// closedDaysQuery returns the distinct UTC-day buckets that contain at least
// one closed-bucket op. The fold is integer math (no time/DST), matching
// rollups.BucketTS. The cursor is fully drained before any write begins.
const closedDaysQuery = `
SELECT DISTINCT (o.start_ts / ?) * ? AS day
FROM ops o
WHERE o.start_ts < ?
ORDER BY day ASC`

// closedDays returns the sorted union of (a) UTC days that contain closed ops
// and (b) UTC days carrying session starts but no ops, so a session-start-only
// day still produces its total/agent/cwd rows.
func closedDays(ctx context.Context, db *sql.DB, openHourStart int64, startsByDay map[int64][]rollups.SessionStart) ([]int64, error) {
	rows, err := db.QueryContext(ctx, closedDaysQuery, daySpanUS, daySpanUS, openHourStart)
	if err != nil {
		return nil, fmt.Errorf("rollups-backfill: query closed days: %w", err)
	}
	defer func() { _ = rows.Close() }()

	seen := make(map[int64]struct{})
	var days []int64
	for rows.Next() {
		var day int64
		if err := rows.Scan(&day); err != nil {
			return nil, fmt.Errorf("rollups-backfill: scan day: %w", err)
		}
		if _, ok := seen[day]; !ok {
			seen[day] = struct{}{}
			days = append(days, day)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rollups-backfill: iterate days: %w", err)
	}

	// Fold in session-start-only days (those with starts but no ops).
	for day := range startsByDay {
		if _, ok := seen[day]; !ok {
			seen[day] = struct{}{}
			days = append(days, day)
		}
	}
	slices.Sort(days)
	return days, nil
}

// rollupOpSelectFrom is the SELECT + FROM/JOIN prefix shared by every
// ops→rollups.OpRow query (the one-shot backfill's dayOpsQuery and the
// incremental refresh's window query). Sharing the exact column list + join
// shape is REQUIRED for the backfill-vs-incremental byte-diff gate: both paths
// must read identical columns in an identical physical row order so the
// float-summed cost_usd folds bit-for-bit the same. Callers append their own
// WHERE + ORDER BY (both MUST order by o.start_ts ASC, o.id ASC — see
// dayOpsQuery). The JOINs are INNER: FK integrity guarantees every op has a
// session and source. The column order matches scanOpRow exactly.
const rollupOpSelectFrom = `
SELECT o.start_ts, o.end_ts, o.duration_us, src.format,
       o.kind, o.model, o.provider, o.tool_namespace, o.name,
       s.agent_name, s.cwd,
       o.cost_usd, o.tokens_in, o.tokens_out, o.tokens_cache_read, o.tokens_cache_write,
       o.status
FROM ops o
JOIN sessions s ON o.session_id = s.id
JOIN sources  src ON s.source_id = src.id`

// dayOpsQuery selects one UTC day's closed-bucket ops joined to their session
// and source. The upper bound is min(dayEnd, openHourStart) so the open hour
// of the open day is excluded even within its own day window. The `o.id ASC`
// tiebreak is MANDATORY: equal-start_ts ops must fold in a deterministic total
// order, else the backfill DB and the replayed-incremental DB sum cost_usd in
// different physical orders and the byte-diff gate fails by float ULPs.
const dayOpsQuery = rollupOpSelectFrom + `
WHERE o.start_ts >= ? AND o.start_ts < ?
ORDER BY o.start_ts ASC, o.id ASC`

// loadDayOps reads every closed-bucket op for the given UTC day within the
// caller's transaction, fully draining the cursor (so it never straddles the
// later upserts on the single writer connection).
func loadDayOps(ctx context.Context, tx *sql.Tx, day, openHourStart int64) ([]rollups.OpRow, error) {
	// Clamp the open day's window to the open-hour cutoff.
	upper := min(day+daySpanUS, openHourStart)
	rows, err := tx.QueryContext(ctx, dayOpsQuery, day, upper)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ops []rollups.OpRow
	for rows.Next() {
		op, err := scanOpRow(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ops, nil
}

// failedStatus is the single op status that counts as a failure across the
// codebase (stats_breakdowns.go, session_topology.go, aggregates.go).
const failedStatus = "failed"

// scanOpRow maps one ops⋈sessions⋈sources row into a rollups.OpRow. It is the
// SHARED reader contract for both the backfill and the incremental refresh
// (Chunk 5): nullable text columns collapse to "", a NULL end_ts means a
// running op (nil pointer), and a NULL duration_us means 0.
func scanOpRow(rows *sql.Rows) (rollups.OpRow, error) {
	var (
		op         rollups.OpRow
		endTS      sql.NullInt64
		durationUS sql.NullInt64
		model      sql.NullString
		provider   sql.NullString
		toolNS     sql.NullString
		agentName  sql.NullString
		cwd        sql.NullString
		status     string
	)
	if err := rows.Scan(
		&op.StartTS, &endTS, &durationUS, &op.SourceFormat,
		&op.Kind, &model, &provider, &toolNS, &op.ToolName,
		&agentName, &cwd,
		&op.CostUSD, &op.TokensIn, &op.TokensOut, &op.TokensCacheRead, &op.TokensCacheWrite,
		&status,
	); err != nil {
		return rollups.OpRow{}, err
	}
	if endTS.Valid {
		v := endTS.Int64
		op.EndTS = &v
	}
	if durationUS.Valid {
		op.DurationUS = durationUS.Int64
	}
	op.Model = model.String
	op.Provider = provider.String
	op.ToolNamespace = toolNS.String
	op.AgentName = agentName.String
	op.Cwd = cwd.String
	op.Failed = status == failedStatus
	return op, nil
}

// sessionStartSelectFrom is the SELECT + FROM/JOIN prefix shared by the
// backfill's sessionStartsQuery and the incremental refresh's window query.
// The column order matches scanSessionStart exactly. Callers append their own
// WHERE clause. IFNULL collapses NULL agent_name/cwd to "" so the fold's
// empty-string check matches the ops path.
const sessionStartSelectFrom = `
SELECT s.start_ts, src.format, IFNULL(s.agent_name,''), IFNULL(s.cwd,'')
FROM sessions s
JOIN sources src ON s.source_id = src.id`

// sessionStartsQuery selects every session whose start is in a closed hour, so
// the additive session_starts metric is attributed to the bucket of each
// session's start. Bounded to one row per session.
const sessionStartsQuery = sessionStartSelectFrom + `
WHERE s.start_ts < ?`

// scanSessionStart maps one sessions⋈sources row into a rollups.SessionStart.
// Shared by loadSessionStarts (backfill) and the incremental refresh so both
// read the same columns in the same shape. Both queries IFNULL the nullable
// text columns, so the scan targets plain strings.
func scanSessionStart(rows *sql.Rows) (rollups.SessionStart, error) {
	var ss rollups.SessionStart
	if err := rows.Scan(&ss.StartTS, &ss.SourceFormat, &ss.AgentName, &ss.Cwd); err != nil {
		return rollups.SessionStart{}, err
	}
	return ss, nil
}

// loadSessionStarts preloads all closed session starts, bucketed by their UTC
// day so flushDay can fold the matching day's starts. Sessions are bounded
// (one row each), so the map stays small. The cursor is fully drained before
// any transaction opens.
func loadSessionStarts(ctx context.Context, db *sql.DB, openHourStart int64) (map[int64][]rollups.SessionStart, error) {
	rows, err := db.QueryContext(ctx, sessionStartsQuery, openHourStart)
	if err != nil {
		return nil, fmt.Errorf("rollups-backfill: query session starts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byDay := make(map[int64][]rollups.SessionStart)
	for rows.Next() {
		ss, err := scanSessionStart(rows)
		if err != nil {
			return nil, fmt.Errorf("rollups-backfill: scan session start: %w", err)
		}
		day := rollups.BucketTS(ss.StartTS, rollups.Daily)
		byDay[day] = append(byDay[day], ss)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rollups-backfill: iterate session starts: %w", err)
	}
	return byDay, nil
}

// rollupUpsertSQL is the idempotent overwrite for one rollup row. The table
// name is a compile-time constant chosen by rollupTable(bucket) — never user
// input — so the formatted query is injection-safe; all VALUES are ?-bound.
const rollupUpsertSQL = `
INSERT INTO %s (bucket_ts,source_format,dimension,dimension_value,op_count,tokens_in,tokens_out,tokens_cache_read,tokens_cache_write,cost_usd,failures,duration_us,session_starts)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(bucket_ts,source_format,dimension,dimension_value) DO UPDATE SET
  op_count=excluded.op_count, tokens_in=excluded.tokens_in, tokens_out=excluded.tokens_out,
  tokens_cache_read=excluded.tokens_cache_read, tokens_cache_write=excluded.tokens_cache_write,
  cost_usd=excluded.cost_usd, failures=excluded.failures, duration_us=excluded.duration_us,
  session_starts=excluded.session_starts`

// rollupTable maps a bucket granularity to its destination table name. The
// returned value is a fixed string literal, never user input.
func rollupTable(bucket rollups.Bucket) string {
	if bucket == rollups.Daily {
		return "rollup_daily"
	}
	return "rollup_hourly"
}

// upsertRollups idempotently overwrites each row into the bucket's table within
// the caller's transaction. A prepared statement amortizes the per-row cost
// across a day's worth of rows.
func upsertRollups(ctx context.Context, tx *sql.Tx, bucket rollups.Bucket, rows []rollups.RollupRow) error {
	if len(rows) == 0 {
		return nil
	}
	// #nosec G201 -- table is rollupTable(bucket), a fixed literal switch on a
	// Go enum (never user input); all VALUES are ?-bound parameters.
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(rollupUpsertSQL, rollupTable(bucket)))
	if err != nil {
		return fmt.Errorf("prepare %s upsert: %w", rollupTable(bucket), err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range rows {
		r := &rows[i]
		if _, err := stmt.ExecContext(ctx,
			r.BucketTS, r.SourceFormat, r.Dimension, r.DimensionValue,
			r.OpCount, r.TokensIn, r.TokensOut, r.TokensCacheRead, r.TokensCacheWrite,
			r.CostUSD, r.Failures, r.DurationUS, r.SessionStarts,
		); err != nil {
			return fmt.Errorf("upsert %s row (%d,%s,%s,%s): %w",
				rollupTable(bucket), r.BucketTS, r.SourceFormat, r.Dimension, r.DimensionValue, err)
		}
	}
	return nil
}
