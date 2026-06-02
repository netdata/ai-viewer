package presenter

import (
	"context"
	"database/sql"
	"sort"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// Shared machinery for the two rollup-backed endpoints (/api/stats/aggregate,
// /api/stats/top). Both compute their per-(bucket,key) metric through the SAME
// pure internal/rollups fold for any live/open portion, so an open bucket and a
// live-folded range are byte-consistent with the materialized closed-bucket
// rollups by construction (rest-api.md §"Rollup fast path vs. live fold").
//
// The SQL fragments below concatenate the parameterized `where` fragment built
// by sessionFilter.whereClause into otherwise-static SQL; every operator value
// is bound via a `?` placeholder (filters.go), never interpolated. gosec's
// taint analysis (G201/G202) cannot prove that, so each query carries an inline
// suppression, matching the precedent in stats_breakdowns.go.

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

// closedSeries is the closed-bucket fast-path result: per bucket_ts, a map of
// series key → summed metric value. The open bucket(s) are merged on top by the
// caller after a live fold.
type closedSeries map[int64]map[string]float64

// loadClosedRollups runs the closed-bucket fast-path query and accumulates the
// per-(bucket,key) sums. lower/upper bound bucket_ts; upper is the open-bucket
// cutoff so closed buckets never overlap the live-folded open window. The
// cursor is fully drained before return (drain-before-next-query discipline).
func (p *Presenter) loadClosedRollups(ctx context.Context, b rollups.Bucket, dim rollupDimension, m statsMetric, lower, upper int64) (closedSeries, error) {
	q := rollupClosedQuery(rollupTableName(b), m.rollupColumn(), dim.keyBySrc)
	rows, err := p.db.QueryContext(ctx, q, dim.dimension, lower, upper) // #nosec G201 G701 -- static SQL + literal columns; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(closedSeries)
	for rows.Next() {
		var bucketTS int64
		var key string
		var val float64
		if err := rows.Scan(&bucketTS, &key, &val); err != nil {
			return nil, err
		}
		if out[bucketTS] == nil {
			out[bucketTS] = make(map[string]float64)
		}
		out[bucketTS][key] += val
	}
	return out, rows.Err()
}

// foldOpsWindow reads the filtered ops whose start_ts is in [lower, upper) and
// folds them through internal/rollups, so the result is byte-consistent with
// the materialized rollups. upper==0 means "no upper bound" (read up to now —
// there are no future ops). The window bounds MUST be bucket-aligned (0, from
// at a bucket boundary, openStart, …): the caller filters the FOLDED rows by
// their BucketTS, and clipping ops at a non-bucket-aligned upper would drop part
// of a partially-included bucket and break fast-path↔live-fold parity. starts
// is passed nil: session_starts is not a selectable metric here, and the
// op-level metrics do not use it. The cursor is fully drained before the fold.
func (p *Presenter) foldOpsWindow(ctx context.Context, f sessionFilter, b rollups.Bucket, lower, upper int64) ([]rollups.RollupRow, error) {
	ops, err := p.loadFoldOps(ctx, f, lower, upper)
	if err != nil {
		return nil, err
	}
	return rollups.Rollup(ops, nil, b, rollups.Options{}), nil
}

// loadFoldOps reads the ops for the fold. It restricts to the filtered session
// set EXACTLY as handleStats does (o.session_id IN (<sessions WHERE filter>)),
// and bounds the op window by o.start_ts >= lower (and < upper when upper>0).
// The column list + scan mirror the ingest backfill's scanOpRow (a deliberate
// small duplicate to avoid coupling presenter↔ingest). The cursor is fully
// drained before return.
func (p *Presenter) loadFoldOps(ctx context.Context, f sessionFilter, lower, upper int64) ([]rollups.OpRow, error) {
	where, args := f.whereClause("s")
	sessionSet := "SELECT s.id FROM sessions s WHERE " + where
	// Column order matches scanRollupOpRow exactly. INNER JOINs: FK integrity
	// guarantees every op has a session and source. `where`/`sessionSet` come
	// from filters.go and are ALWAYS `?`-parameterized (no string interpolation
	// of user input); a future edit that interpolates a value here would break
	// that injection-safety invariant.
	q := `
SELECT o.start_ts, o.end_ts, o.duration_us, src.format,
       o.kind, o.model, o.provider, o.tool_namespace, o.name,
       s.agent_name, s.cwd,
       o.cost_usd, o.tokens_in, o.tokens_out, o.tokens_cache_read, o.tokens_cache_write,
       o.status
FROM ops o
JOIN sessions s ON o.session_id = s.id
JOIN sources  src ON s.source_id = src.id
WHERE o.session_id IN (` + sessionSet + `) AND o.start_ts >= ?` // #nosec G201 G202 -- static SQL + ?-placeholders; sessionSet is parameterized (filters.go)
	qArgs := append(append([]any{}, args...), lower)
	if upper > 0 {
		q += ` AND o.start_ts < ?`
		qArgs = append(qArgs, upper)
	}
	// Deterministic fold order: the ingester folds ops ORDER BY o.start_ts ASC,
	// o.id ASC (rollup_backfill.go / rollup_refresh.go). Float SUMs (cost_usd)
	// are addition-order sensitive, so the live fold MUST feed ops in the SAME
	// order or it can diverge from the materialized rollup. This keeps the
	// fast-path vs live-fold parity byte-identical.
	q += `
ORDER BY o.start_ts ASC, o.id ASC`
	rows, err := p.db.QueryContext(ctx, q, qArgs...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ops []rollups.OpRow
	for rows.Next() {
		op, err := scanRollupOpRow(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// scanRollupOpRow maps one ops⋈sessions⋈sources row into a rollups.OpRow. It is
// a presenter-local replica of internal/ingest.scanOpRow (intentionally
// duplicated to avoid coupling presenter↔ingest): nullable text → "", NULL
// end_ts → running (nil pointer), NULL duration_us → 0, status=='failed' →
// Failed. Keeping the exact same mapping is what makes the live/open fold
// byte-consistent with the materialized rollups.
func scanRollupOpRow(rows *sql.Rows) (rollups.OpRow, error) {
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
	// "failed" is the single failure status across the codebase (matches
	// internal/rollups' fold contract and internal/ingest.failedStatus).
	op.Failed = status == "failed"
	return op, nil
}

// addFoldToSeries projects a folded RollupRow set onto the per-(bucket,key)
// metric series for the requested dimension, merging into dst (the closed-bucket
// fast-path result, or a fresh map on the live-fold path). Only rows of the
// requested rollup dimension whose BucketTS is in the half-open window
// [winLo, winHi) contribute (winHi==0 means "no upper bound"); this is the
// SAME bucket_ts predicate the fast-path rollup query applies, so a folded
// (open or live) range stays byte-consistent with the materialized closed
// buckets even when from/to are not bucket-aligned. The series key is
// source_format when keyBySrc (reading the 'total' dimension), else the row's
// dimension_value.
func addFoldToSeries(dst closedSeries, folded []rollups.RollupRow, dim rollupDimension, m statsMetric, winLo, winHi int64) {
	for i := range folded {
		r := &folded[i]
		if r.Dimension != dim.dimension {
			continue
		}
		if r.BucketTS < winLo || (winHi > 0 && r.BucketTS >= winHi) {
			continue
		}
		key := r.DimensionValue
		if dim.keyBySrc {
			key = r.SourceFormat
		}
		if dst[r.BucketTS] == nil {
			dst[r.BucketTS] = make(map[string]float64)
		}
		dst[r.BucketTS][key] += m.value(r)
	}
}

// sortedBucketTS returns the bucket timestamps of s in ascending order, so the
// aggregate response lists buckets deterministically.
func sortedBucketTS(s closedSeries) []int64 {
	out := make([]int64, 0, len(s))
	for ts := range s {
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortedSeries flattens one bucket's key→value map into a slice ordered by
// value desc then key asc (the stable order the wire contract specifies for
// top, reused for aggregate so a bucket's series is deterministic).
func sortedSeries(m map[string]float64) []seriesItem {
	items := make([]seriesItem, 0, len(m))
	for k, v := range m {
		items = append(items, seriesItem{Key: k, Value: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].Key < items[j].Key
	})
	return items
}

// seriesItem is one {key,value} pair in an aggregate bucket's series or a top
// ranking. value carries the selected metric (a float; integer metrics like
// op_count/tokens serialize without a fractional part).
type seriesItem struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
}
