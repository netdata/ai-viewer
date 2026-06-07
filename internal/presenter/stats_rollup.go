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
//
// Parsing of ?metric/?group_by/?dimension/?bucket and the rollup SQL-table /
// column builders live in stats_rollup_defs.go to keep both files under the
// 400-line maintainability ceiling.

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
// of a partially-included bucket and break fast-path↔live-fold parity.
//
// The `metric` decides whether the fold also needs SESSION STARTS: for
// metric=sessions (session_starts) the fold must load the window's session
// starts (bucketed by s.start_ts) and feed them to rollups.Rollup's `starts`
// input, mirroring how the materialized rollup attributes session_starts. For
// every op-level metric, starts is nil (no extra query — the op-level metrics do
// not use session_starts). The cursors are fully drained before the fold.
func (p *Presenter) foldOpsWindow(ctx context.Context, f sessionFilter, b rollups.Bucket, m statsMetric, lower, upper int64) ([]rollups.RollupRow, error) {
	ops, err := p.loadFoldOps(ctx, f, lower, upper)
	if err != nil {
		return nil, err
	}
	var starts []rollups.SessionStart
	if m == smSessions {
		if starts, err = p.loadFoldSessionStarts(ctx, f, lower, upper); err != nil {
			return nil, err
		}
	}
	return rollups.Rollup(ops, starts, b, rollups.Options{}), nil
}

// loadFoldOps reads the ops for the fold. It restricts to the filtered session
// set by the session DIMENSION filters ONLY (whereClauseNoTimeWindow:
// group/agents/models/status/sources/q + the tools EXISTS subquery), and bounds
// the op window by o.start_ts >= lower (and < upper when upper>0). The op window
// is the SOLE time bound: the session's start_ts is deliberately NOT constrained
// here, because the rollup buckets every op by op.start_ts regardless of when its
// session started, so bounding the session set by s.start_ts would drop an
// in-window op whose session started before `from` and diverge from the
// op-bucketed rollup (rest-api.md §"Rollup fast path vs. live fold" — the AC#2
// parity invariant). The column list + scan mirror the ingest backfill's
// scanOpRow (a deliberate small duplicate to avoid coupling presenter↔ingest).
// The cursor is fully drained before return.
func (p *Presenter) loadFoldOps(ctx context.Context, f sessionFilter, lower, upper int64) ([]rollups.OpRow, error) {
	where, args := f.whereClauseNoTimeWindow("s")
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

// loadFoldSessionStarts reads the session starts feeding the metric=sessions
// live fold: the sessions whose s.start_ts ∈ [lower, upper) (upper==0 → no upper
// bound), constrained by the session DIMENSION filters
// (whereClauseNoTimeWindow). Unlike ops, session_starts ARE bucketed by
// s.start_ts, so the [lower, upper) window IS their correct time bound — the same
// half-open window the op fold and addFoldToSeries apply by BucketTS — which keeps
// the live fold byte-consistent with the materialized session_starts column. The
// column list + scan mirror internal/ingest.sessionStartSelectFrom /
// scanSessionStart (a deliberate small duplicate to avoid coupling
// presenter↔ingest), IFNULL-collapsing nullable agent_name/cwd to "" so the
// fold's empty-string skip matches the ingest path. The cursor is fully drained
// before return.
func (p *Presenter) loadFoldSessionStarts(ctx context.Context, f sessionFilter, lower, upper int64) ([]rollups.SessionStart, error) {
	where, args := f.whereClauseNoTimeWindow("s")
	// `where` is ALWAYS `?`-parameterized (filters.go); a future edit that
	// interpolates a value here would break that injection-safety invariant.
	q := `
SELECT s.start_ts, src.format, IFNULL(s.agent_name,''), IFNULL(s.cwd,'')
FROM sessions s
JOIN sources src ON s.source_id = src.id
WHERE ` + where + ` AND s.start_ts >= ?` // #nosec G201 G202 -- static SQL + ?-placeholders; where is parameterized (filters.go)
	qArgs := append(append([]any{}, args...), lower)
	if upper > 0 {
		q += ` AND s.start_ts < ?`
		qArgs = append(qArgs, upper)
	}
	rows, err := p.db.QueryContext(ctx, q, qArgs...) // #nosec G201 G701 -- static SQL + ?-placeholders; values bound via args
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var starts []rollups.SessionStart
	for rows.Next() {
		ss, err := scanRollupSessionStart(rows)
		if err != nil {
			return nil, err
		}
		starts = append(starts, ss)
	}
	return starts, rows.Err()
}

// scanRollupSessionStart maps one sessions⋈sources row into a
// rollups.SessionStart. It is a presenter-local replica of
// internal/ingest.scanSessionStart (intentionally duplicated to avoid coupling
// presenter↔ingest): the query IFNULLs the nullable agent_name/cwd, so the scan
// targets plain strings. Keeping the exact same mapping is what makes the
// session_starts live fold byte-consistent with the materialized rollups.
func scanRollupSessionStart(rows *sql.Rows) (rollups.SessionStart, error) {
	var ss rollups.SessionStart
	if err := rows.Scan(&ss.StartTS, &ss.SourceFormat, &ss.AgentName, &ss.Cwd); err != nil {
		return rollups.SessionStart{}, err
	}
	return ss, nil
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
