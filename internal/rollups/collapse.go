package rollups

import "sort"

// collapse applies the R1 high-cardinality safety bound (data-model.md §"R1
// safety bound"). Within each (bucket_ts, source_format, dimension) group,
// when the number of distinct dimension values would exceed maxRows, the
// lowest-metric tail is folded into a single dimension_value="__other__" row so
// an unbounded dimension (most plausibly cwd) cannot explode the table.
//
// The 'total' dimension has exactly one value ("") and is never collapsed; this
// is enforced structurally because its group can never exceed a cap >= 1.
//
// Determinism of the kept set: rows are ranked by op_count DESC, then
// dimension_value ASC (the stable secondary key). The top (maxRows-1) survive
// and the remainder collapse, leaving at most maxRows rows in the group
// (kept + the single __other__ row). Additivity is preserved: __other__ carries
// the element-wise sum of the collapsed tail's metrics.
//
// maxRows is assumed >= 1 (Rollup resolves <= 0 to the package default before
// calling). When maxRows <= 1 a group with two or more values keeps zero
// concrete rows and folds everything into __other__.
func collapse(rows []RollupRow, maxRows int) []RollupRow {
	groups := groupByBucketSourceDimension(rows)
	if !anyGroupExceeds(groups, maxRows) {
		return rows // common case: nothing to collapse, no allocation churn.
	}

	out := make([]RollupRow, 0, len(rows))
	for _, g := range groups {
		out = append(out, collapseGroup(rows, g, maxRows)...)
	}
	return out
}

// groupKey identifies a collapse group: one (bucket, source_format, dimension).
type groupKey struct {
	bucketTS     int64
	sourceFormat string
	dimension    string
}

// groupByBucketSourceDimension maps each group key to the indices of its rows
// in the input slice, preserving input order within a group.
func groupByBucketSourceDimension(rows []RollupRow) map[groupKey][]int {
	groups := make(map[groupKey][]int)
	for i := range rows {
		k := groupKey{rows[i].BucketTS, rows[i].SourceFormat, rows[i].Dimension}
		groups[k] = append(groups[k], i)
	}
	return groups
}

// anyGroupExceeds reports whether any group has more rows than the cap (the
// fast-path guard).
func anyGroupExceeds(groups map[groupKey][]int, maxRows int) bool {
	for _, idxs := range groups {
		if len(idxs) > maxRows {
			return true
		}
	}
	return false
}

// collapseGroup returns the rows for one group after the tail-collapse. Groups
// at or under the cap are returned unchanged; over-cap groups return the top
// (maxRows-1) ranked rows plus one __other__ row carrying the summed tail.
func collapseGroup(rows []RollupRow, idxs []int, maxRows int) []RollupRow {
	if len(idxs) <= maxRows {
		out := make([]RollupRow, 0, len(idxs))
		for _, i := range idxs {
			out = append(out, rows[i])
		}
		return out
	}

	ranked := append([]int(nil), idxs...)
	sort.Slice(ranked, func(a, b int) bool { return rankLess(rows[ranked[a]], rows[ranked[b]]) })

	keep := maxRows - 1 // reserve one slot for the __other__ row.
	out := make([]RollupRow, 0, maxRows)
	for _, i := range ranked[:keep] {
		out = append(out, rows[i])
	}

	other := rows[ranked[keep]] // seed identity from the first collapsed row...
	other.DimensionValue = otherValue
	zeroMetrics(&other)
	for _, i := range ranked[keep:] {
		addMetrics(&other, rows[i])
	}
	out = append(out, other)
	return out
}

// rankLess orders rows for tail-collapse selection: op_count DESC, then
// dimension_value ASC. Higher op_count ranks first; ties break by the lexically
// smallest dimension_value so the kept set is fully deterministic.
func rankLess(a, b RollupRow) bool {
	if a.OpCount != b.OpCount {
		return a.OpCount > b.OpCount
	}
	return a.DimensionValue < b.DimensionValue
}

// zeroMetrics clears every additive metric on r, leaving identity fields intact.
func zeroMetrics(r *RollupRow) {
	r.OpCount = 0
	r.TokensIn = 0
	r.TokensOut = 0
	r.TokensCacheRead = 0
	r.TokensCacheWrite = 0
	r.CostUSD = 0
	r.Failures = 0
	r.DurationUS = 0
	r.SessionStarts = 0
}

// addMetrics adds src's additive metrics into dst (identity fields untouched).
func addMetrics(dst *RollupRow, src RollupRow) {
	dst.OpCount += src.OpCount
	dst.TokensIn += src.TokensIn
	dst.TokensOut += src.TokensOut
	dst.TokensCacheRead += src.TokensCacheRead
	dst.TokensCacheWrite += src.TokensCacheWrite
	dst.CostUSD += src.CostUSD
	dst.Failures += src.Failures
	dst.DurationUS += src.DurationUS
	dst.SessionStarts += src.SessionStarts
}
