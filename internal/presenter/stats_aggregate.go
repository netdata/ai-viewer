package presenter

import (
	"context"
	"net/http"
	"time"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// aggregateResponse is the JSON envelope of GET /api/stats/aggregate: one entry
// per time bucket carrying that bucket's per-group_by-value series, plus the
// echoed bucket granularity and metric (rest-api.md §GET /api/stats/aggregate).
type aggregateResponse struct {
	Buckets []aggregateBucket `json:"buckets"`
	Bucket  string            `json:"bucket"`
	Metric  string            `json:"metric"`
}

// aggregateBucket is one time bucket and its series.
type aggregateBucket struct {
	BucketTS int64        `json:"bucket_ts"`
	Series   []seriesItem `json:"series"`
}

// handleStatsAggregate answers GET /api/stats/aggregate: a time-series
// aggregate for the dashboard's line charts. Closed buckets are summed from the
// materialized rollups (fast path) or live-folded (when a cross-dimension or
// sources filter is present); the still-open bucket(s) are ALWAYS folded live
// over ops and merged on top, so every bucket is byte-consistent with the
// materialized closed-bucket rollups (rest-api.md §"Rollup fast path vs. live
// fold"). Mirrors handleStats's guard/parse/timeout/error-mapping shape.
func (p *Presenter) handleStatsAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	q := r.URL.Query()
	bucket, ok := parseBucket(q.Get("bucket"))
	if !ok {
		p.writeBadEnum(w, r, "bucket")
		return
	}
	dim, ok := parseGroupBy(q.Get("group_by"))
	if !ok {
		p.writeBadEnum(w, r, "group_by")
		return
	}
	metric, ok := parseMetric(q.Get("metric"))
	if !ok {
		p.writeBadEnum(w, r, "metric")
		return
	}
	f, err := parseSessionFilter(q, p.now())
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	// Rollup-backed stats aggregate over ALL sessions (root + sub-agent); the
	// group distinction is a session-LIST concern and does not apply here.
	// Forcing group=all keeps the fast path (all-session rollups) consistent
	// with the live fold (rest-api.md §"Rollup fast path vs. live fold").
	f.forceAllSessions()

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	series, err := p.aggregateSeries(ctx, f, bucket, dim, metric)
	if err != nil {
		p.writeDBError(ctx, w, r, "stats.aggregate", err)
		return
	}

	resp := aggregateResponse{Buckets: buildAggregateBuckets(series), Bucket: bucketName(bucket), Metric: metricName(metric)}
	writeJSON(w, r, p.logger, resp)
}

// aggregateSeries computes the per-(bucket,key) metric series across the
// requested window. A bucket is included iff its bucket_ts ∈ [from, to). The
// closed portion (bucket_ts < openStart) comes from the materialized rollups
// (fast path) or a live fold (cross-filter/sources path); the single open
// bucket (bucket_ts == openStart) is ALWAYS folded live over ops and merged on
// top. Both portions apply the identical bucket_ts window, so the open/live
// fold stays byte-consistent with the materialized closed buckets even when
// from/to fall mid-bucket (rest-api.md §"Rollup fast path vs. live fold").
func (p *Presenter) aggregateSeries(ctx context.Context, f sessionFilter, bucket rollups.Bucket, dim rollupDimension, metric statsMetric) (closedSeries, error) {
	// Single clock read so the window and the open-bucket cutoff are consistent
	// across a boundary crossing: two separate p.now() calls could straddle an
	// hour/day boundary, desyncing `to` from `openStart` and dropping the
	// just-open bucket's live fold (closedHi = min(to, openStart)).
	now := p.now()
	from, to := f.timeWindow(now)
	openStart := rollups.BucketTS(now.UnixMicro(), bucket)
	closedHi := minInt64(to, openStart) // closed buckets: bucket_ts ∈ [from, closedHi)

	var series closedSeries
	if f.isRollupFastPath() {
		var err error
		if series, err = p.loadClosedRollups(ctx, bucket, dim, metric, from, closedHi); err != nil {
			return nil, err
		}
	} else {
		// Live fold of the closed window. Read ops up to (bucket-aligned)
		// openStart and keep folded rows whose bucket_ts ∈ [from, closedHi) —
		// the SAME predicate the fast-path rollup query applies.
		series = make(closedSeries)
		if closedHi > from {
			folded, err := p.foldOpsWindow(ctx, f, bucket, metric, from, openStart)
			if err != nil {
				return nil, err
			}
			addFoldToSeries(series, folded, dim, metric, from, closedHi)
		}
	}

	// Open bucket: include only when openStart ∈ [from, to). Fold ALL ops at
	// or after openStart (no upper clip — there are no future ops, so this is
	// exactly the open bucket's data, the whole-bucket semantics a rollup
	// row carries) and keep the bucket_ts == openStart rows.
	if openStart >= from && openStart < to {
		folded, err := p.foldOpsWindow(ctx, f, bucket, metric, openStart, 0)
		if err != nil {
			return nil, err
		}
		addFoldToSeries(series, folded, dim, metric, openStart, openStart+1)
	}
	return series, nil
}

// buildAggregateBuckets renders the per-bucket series in ascending bucket_ts
// order, each series ordered value-desc / key-asc. Returns a non-nil empty
// slice so an empty result serialises as [] not null.
func buildAggregateBuckets(series closedSeries) []aggregateBucket {
	buckets := make([]aggregateBucket, 0, len(series))
	for _, ts := range sortedBucketTS(series) {
		buckets = append(buckets, aggregateBucket{BucketTS: ts, Series: sortedSeries(series[ts])})
	}
	return buckets
}

// timeWindow returns the effective half-open [from, to) the aggregate/top
// endpoints scan, branching on whether the operator SUPPLIED `to` (f.toRaw is
// the value before parseSessionFilter's now-default — non-nil iff ?to was
// given):
//
//   - ?to SUPPLIED: it is EXCLUSIVE (the frontend serializes `to` as an
//     exclusive upper bound — frontend/src/state/filters.ts) and the stats
//     window is half-open `from <= bucket_ts < to` (rest-api.md §"Bucket-window
//     semantics"), so it is used verbatim — NO +1. A bucket whose bucket_ts is
//     exactly `to` is excluded, which also keeps the fast path (bucket_ts < to)
//     byte-identical to the live fold.
//   - ?to OMITTED (default = now, rest-api.md §Conventions): the window must
//     cover the present instant INCLUSIVE of the currently-open bucket (which
//     starts at or before now and is always live-folded). The half-open upper
//     bound is therefore now+1µs (≡ bucket_ts <= now), so the open bucket is
//     included even when now lands exactly on a bucket boundary (now ==
//     openStart). Using a bare `now` would wrongly drop the open bucket at that
//     boundary instant.
//
// from defaults to 0 (all history) when ?from is omitted. This window is the
// rollup endpoints' own half-open contract, distinct from the session list's
// whereClause (start_ts <= to, inclusive).
func (f sessionFilter) timeWindow(now time.Time) (int64, int64) {
	var from int64
	if f.from != nil {
		from = *f.from
	}
	if f.toRaw != nil {
		return from, *f.toRaw // operator-supplied: exclusive, verbatim.
	}
	return from, now.UnixMicro() + 1 // default now: include the open bucket (bucket_ts <= now).
}

// bucketName / metricName echo the resolved enums back on the wire.
func bucketName(b rollups.Bucket) string {
	if b == rollups.Daily {
		return "daily"
	}
	return "hourly"
}

func metricName(m statsMetric) string {
	switch m {
	case smTokensIn:
		return "tokens_in"
	case smTokensOut:
		return "tokens_out"
	case smCalls:
		return "calls"
	case smFailures:
		return "failures"
	case smDurationUS:
		return "duration_us"
	case smSessions:
		return "sessions"
	default:
		return "cost"
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
