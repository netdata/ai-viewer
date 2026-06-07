package presenter

import (
	"github.com/netdata/ai-viewer/internal/rollups"
)

// Parsing + SQL-table/column definitions for the rollup-backed endpoints.
// The DB I/O (closed-bucket query, live fold) and series projection helpers
// live in stats_rollup.go; this file contains the pure, side-effect-free
// pieces those helpers depend on (enum parsers, dimension/metric mapping,
// table-name and SQL-fragment builders). Splitting them keeps each file
// under the 400-line maintainability ceiling without changing behavior.

// statsMetric is one selectable metric column. The wire enum value maps to the
// rollup column SUM-ed on the fast path AND to the rollups.RollupRow field the
// fold reads on the live/open path; the two MUST stay aligned (data-model.md
// §"Rollup tables"). `calls` maps to op_count (rest-api.md).
type statsMetric int

const (
	smCost statsMetric = iota
	smTokensIn
	smTokensOut
	smCalls
	smFailures
	smDurationUS
	smSessions
)

// parseMetric maps the ?metric= enum to a statsMetric, defaulting to cost.
// Unknown values yield a BAD_REQUEST (ok=false), mirroring how the codebase
// rejects unknown enums elsewhere (parseScalarFilters).
func parseMetric(v string) (statsMetric, bool) {
	switch v {
	case "", "cost":
		return smCost, true
	case "tokens_in":
		return smTokensIn, true
	case "tokens_out":
		return smTokensOut, true
	case "calls":
		return smCalls, true
	case "failures":
		return smFailures, true
	case "duration_us":
		return smDurationUS, true
	case "sessions":
		return smSessions, true
	default:
		return 0, false
	}
}

// rollupColumn is the rollup_* table column SUM-ed for this metric on the fast
// path. The value is a fixed literal chosen by a Go switch (never user input),
// so the formatted query stays injection-safe; all other values are ?-bound.
func (m statsMetric) rollupColumn() string {
	switch m {
	case smTokensIn:
		return "tokens_in"
	case smTokensOut:
		return "tokens_out"
	case smCalls:
		return "op_count"
	case smFailures:
		return "failures"
	case smDurationUS:
		return "duration_us"
	case smSessions:
		return "session_starts"
	default: // smCost
		return "cost_usd"
	}
}

// value extracts this metric's number from a folded rollup row. The fast-path
// SUM and this extraction read the same logical column, so the two paths agree.
func (m statsMetric) value(r *rollups.RollupRow) float64 {
	switch m {
	case smTokensIn:
		return float64(r.TokensIn)
	case smTokensOut:
		return float64(r.TokensOut)
	case smCalls:
		return float64(r.OpCount)
	case smFailures:
		return float64(r.Failures)
	case smDurationUS:
		return float64(r.DurationUS)
	case smSessions:
		return float64(r.SessionStarts)
	default: // smCost
		return r.CostUSD
	}
}

// rollupDimension names the rollups.RollupRow.Dimension a group_by/dimension
// value reads, plus whether the series key is the row's source_format (true for
// group_by=source_format, which reads the 'total' dimension keyed by
// source_format) rather than its dimension_value.
type rollupDimension struct {
	dimension string // rollup 'dimension' column value
	keyBySrc  bool   // key the series by source_format instead of dimension_value
}

// parseGroupBy maps the ?group_by= enum (aggregate) to a rollupDimension,
// defaulting to total. Unknown values yield ok=false → BAD_REQUEST.
func parseGroupBy(v string) (rollupDimension, bool) {
	switch v {
	case "", "total":
		return rollupDimension{dimension: "total"}, true
	case "source_format":
		return rollupDimension{dimension: "total", keyBySrc: true}, true
	case "model", "provider", "tool", "agent", "cwd":
		return rollupDimension{dimension: v}, true
	default:
		return rollupDimension{}, false
	}
}

// parseTopDimension maps the ?dimension= enum (top) to a rollupDimension. Top
// supports only the op/session dimensions — NOT total/source_format
// (rest-api.md §GET /api/stats/top). Default model. Unknown → ok=false.
func parseTopDimension(v string) (rollupDimension, bool) {
	switch v {
	case "", "model", "provider", "tool", "agent", "cwd":
		d := v
		if d == "" {
			d = "model"
		}
		return rollupDimension{dimension: d}, true
	default:
		return rollupDimension{}, false
	}
}

// parseBucket maps the ?bucket= enum to a rollups.Bucket, defaulting to daily.
// Unknown values yield ok=false → BAD_REQUEST.
func parseBucket(v string) (rollups.Bucket, bool) {
	switch v {
	case "", "daily":
		return rollups.Daily, true
	case "hourly":
		return rollups.Hourly, true
	default:
		return 0, false
	}
}

// isRollupFastPath reports whether the filter is expressible by the long-form
// rollup tables: only `from`/`to` may be set. ANY cross-dimension filter
// (agents/models/tools/status/q) forces the live fold (the rollups deliberately
// avoid the cross-product). `sources` ALSO forces the live fold: it binds to
// sessions.source_id (parseSessionFilter), which is strictly finer than the
// rollups' source_format key (one format spans many source_ids — see the
// "<format>:/<location>" ids in data-model.md), so it cannot be expressed on
// the rollups without loss. Forcing the live fold keeps the fast path and the
// live fold byte-identical, the correctness contract in rest-api.md
// §"Rollup fast path vs. live fold".
func (f sessionFilter) isRollupFastPath() bool {
	return len(f.agents) == 0 && len(f.models) == 0 && len(f.tools) == 0 &&
		len(f.status) == 0 && len(f.source) == 0 && f.q == ""
}

// rollupClosedQuery selects the closed-bucket rows for one rollup dimension,
// grouping by (bucket_ts, series-key) so each (bucket, key) yields one summed
// metric. The series key is source_format when keyBySrc, else dimension_value.
// Bounds: bucket_ts ∈ [lower, upper) with upper clamped to the open-bucket
// cutoff so the open bucket(s) are never double-counted (they are folded live).
// table and metricCol are fixed literals chosen by Go switches (rollupTableName
// / statsMetric.rollupColumn) — never user input; from/to are ?-bound.
func rollupClosedQuery(table, metricCol string, keyBySrc bool) string {
	keyCol := "dimension_value"
	if keyBySrc {
		keyCol = "source_format"
	}
	// #nosec G201 -- table, metricCol, keyCol are fixed literals chosen by Go
	// switches (never user input); bucket_ts bounds + dimension are ?-bound.
	return `
SELECT bucket_ts, ` + keyCol + ` AS series_key, SUM(` + metricCol + `)
FROM ` + table + `
WHERE dimension = ? AND bucket_ts >= ? AND bucket_ts < ?
GROUP BY bucket_ts, series_key`
}

// rollupTableName maps a bucket granularity to its table. Fixed literal switch
// on a Go enum (never user input), so callers may format it into SQL safely.
func rollupTableName(b rollups.Bucket) string {
	if b == rollups.Daily {
		return "rollup_daily"
	}
	return "rollup_hourly"
}
