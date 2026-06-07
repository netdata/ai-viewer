package presenter

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

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

type statsTopRequest struct {
	bucket rollups.Bucket
	dim    rollupDimension
	metric statsMetric
	n      int
	filter sessionFilter
}

type statsTopParseError struct {
	badEnum   string
	filterErr error
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

	params, parseErr := parseStatsTopRequest(r.URL.Query(), p.now())
	if parseErr != nil {
		p.writeStatsTopParseError(w, r, parseErr)
		return
	}

	ctx, cancel := withQueryTimeout(r.Context())
	defer cancel()

	totals, err := p.topTotals(ctx, params.filter, params.bucket, params.dim, params.metric)
	if err != nil {
		p.writeDBError(ctx, w, r, "stats.top", err)
		return
	}

	writeJSON(w, r, p.logger, buildStatsTopResponse(totals, params.dim, params.metric, params.n))
}

func parseStatsTopRequest(q url.Values, now time.Time) (statsTopRequest, *statsTopParseError) {
	bucket, ok := parseBucket(q.Get("bucket"))
	if !ok {
		return statsTopRequest{}, &statsTopParseError{badEnum: "bucket"}
	}
	dim, ok := parseTopDimension(q.Get("dimension"))
	if !ok {
		return statsTopRequest{}, &statsTopParseError{badEnum: "dimension"}
	}
	metric, ok := parseMetric(q.Get("metric"))
	if !ok {
		return statsTopRequest{}, &statsTopParseError{badEnum: "metric"}
	}
	n, err := parseTopN(q.Get("n"))
	if err != nil {
		return statsTopRequest{}, &statsTopParseError{filterErr: err}
	}
	f, err := parseSessionFilter(q, now)
	if err != nil {
		return statsTopRequest{}, &statsTopParseError{filterErr: err}
	}
	// Rollup-backed stats aggregate over ALL sessions (root + sub-agent); the
	// group distinction is a session-LIST concern and does not apply here.
	// Forcing group=all keeps the fast path (all-session rollups) consistent
	// with the live fold (rest-api.md §"Rollup fast path vs. live fold").
	f.forceAllSessions()

	return statsTopRequest{bucket: bucket, dim: dim, metric: metric, n: n, filter: f}, nil
}

func (p *Presenter) writeStatsTopParseError(w http.ResponseWriter, r *http.Request, err *statsTopParseError) {
	if err.badEnum != "" {
		p.writeBadEnum(w, r, err.badEnum)
		return
	}
	p.writeBadFilter(w, r, err.filterErr)
}

func buildStatsTopResponse(totals map[string]float64, dim rollupDimension, metric statsMetric, n int) topResponse {
	items := sortedSeries(totals)
	if len(items) > n {
		items = items[:n]
	}
	if items == nil {
		items = []seriesItem{}
	}
	return topResponse{Dimension: dim.dimension, Metric: metricName(metric), Items: items}
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
