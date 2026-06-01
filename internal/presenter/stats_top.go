package presenter

import (
	"context"
	"net/http"
	"strconv"

	"github.com/netdata/ai-viewer/internal/rollups"
)

// topN bounds for ?n= (rest-api.md §GET /api/stats/top).
const (
	defaultTopN = 20
	maxTopN     = 200
)

// topResponse is the JSON envelope of GET /api/stats/top: the echoed dimension
// and metric plus the value-desc ranking (rest-api.md §GET /api/stats/top).
type topResponse struct {
	Dimension string       `json:"dimension"`
	Metric    string       `json:"metric"`
	Items     []seriesItem `json:"items"`
}

// handleStatsTop answers GET /api/stats/top: the highest-metric dimension
// values over the window. Sums the dimension's rollup rows over the closed
// window (fast path) or live-folds them (cross-filter/sources path), folds the
// open bucket live on top, then ORDERs value-desc and takes the top n. Mirrors
// handleStatsAggregate's guard/parse/timeout/error-mapping shape.
func (p *Presenter) handleStatsTop(w http.ResponseWriter, r *http.Request) {
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
	dim, ok := parseTopDimension(q.Get("dimension"))
	if !ok {
		p.writeBadEnum(w, r, "dimension")
		return
	}
	metric, ok := parseMetric(q.Get("metric"))
	if !ok {
		p.writeBadEnum(w, r, "metric")
		return
	}
	n, err := parseTopN(q.Get("n"))
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}
	f, err := parseSessionFilter(q, p.now())
	if err != nil {
		p.writeBadFilter(w, r, err)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	totals, err := p.topTotals(ctx, f, bucket, dim, metric)
	if err != nil {
		p.writeDBError(w, r, ctx, "stats.top", err)
		return
	}

	items := sortedSeries(totals)
	if len(items) > n {
		items = items[:n]
	}
	if items == nil {
		items = []seriesItem{}
	}
	resp := topResponse{Dimension: dim.dimension, Metric: metricName(metric), Items: items}
	writeJSON(w, r, p.logger, http.StatusOK, resp)
}

// topTotals sums the requested dimension's metric per key across the whole
// [from, to) window (closed buckets + the open bucket), collapsing the
// per-bucket series the aggregate machinery produces into one key→value map.
// Reusing aggregateSeries keeps the fast-path/live-fold/open-fold split — and
// thus the parity contract — identical between the two endpoints.
func (p *Presenter) topTotals(ctx context.Context, f sessionFilter, bucket rollups.Bucket, dim rollupDimension, metric statsMetric) (map[string]float64, error) {
	perBucket, err := p.aggregateSeries(ctx, f, bucket, dim, metric)
	if err != nil {
		return nil, err
	}
	totals := make(map[string]float64)
	for _, byKey := range perBucket {
		for k, v := range byKey {
			totals[k] += v
		}
	}
	return totals, nil
}

// parseTopN parses ?n= and clamps it to [1, maxTopN] (rest-api.md: n default
// 20, max 200; 0 → 1, 999 → 200). A non-integer is a BAD_REQUEST so a client
// bug surfaces rather than silently defaulting.
func parseTopN(s string) (int, error) {
	if s == "" {
		return defaultTopN, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, wrapBadFilter("n must be an integer")
	}
	if n < 1 {
		return 1, nil
	}
	if n > maxTopN {
		return maxTopN, nil
	}
	return n, nil
}
