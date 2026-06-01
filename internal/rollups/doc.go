// Package rollups computes the time-bucketed, additive, long-form rollup rows
// that back the statistics dashboard (data-model.md §"Rollup tables
// (SOW-0007)").
//
// It is a PURE package: no SQL, no DB I/O, no clock. The package OWNS its input
// structs (OpRow, SessionStart); the DB-reading code in the ingest/backfill
// layer maps store rows into these and writes the returned RollupRow values
// into rollup_hourly / rollup_daily.
//
// The fold is deterministic: identical input yields a byte-identical slice
// (sorted by bucket_ts, source_format, dimension, dimension_value). That
// determinism is what makes the backfill-vs-incremental diff gate well-defined
// (quality-gates.md §"Rollup correctness diff").
//
// Correctness invariant — ADDITIVITY: every metric column is SUM-additive, so a
// day's Daily rollup equals the element-wise sum of that day's Hourly rollups,
// and any [from,to) aggregate equals the sum of the buckets it covers. Distinct
// counts are therefore intentionally absent (they cannot be summed across
// buckets); the additive session_starts metric is stored instead.
package rollups
