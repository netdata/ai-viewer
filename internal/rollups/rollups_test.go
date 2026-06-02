package rollups

import (
	"fmt"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// hourUS is one UTC hour in microseconds; dayUS is one UTC day.
const (
	hourUS = int64(3_600_000_000)
	dayUS  = int64(86_400_000_000)
)

// ptr is a tiny helper to take the address of an int64 literal for EndTS.
func ptr(v int64) *int64 { return &v }

// findRow locates the single RollupRow matching the natural key, failing the
// test if it is absent. Used by assertions that target one fan-out row.
func findRow(t *testing.T, rows []RollupRow, bucketTS int64, src, dim, val string) RollupRow {
	t.Helper()
	for _, r := range rows {
		if r.BucketTS == bucketTS && r.SourceFormat == src && r.Dimension == dim && r.DimensionValue == val {
			return r
		}
	}
	t.Fatalf("row not found: bucket=%d src=%q dim=%q val=%q (have %d rows)", bucketTS, src, dim, val, len(rows))
	return RollupRow{}
}

func TestBucketTS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		tsUS   int64
		bucket Bucket
		want   int64
	}{
		{"hour exact start", hourUS, Hourly, hourUS},
		{"hour mid", hourUS + 123, Hourly, hourUS},
		{"hour just before next", 2*hourUS - 1, Hourly, hourUS},
		{"hour zero", 0, Hourly, 0},
		{"day exact start", dayUS, Daily, dayUS},
		{"day mid", dayUS + 500, Daily, dayUS},
		{"day zero", 0, Daily, 0},
		// tsUS at/below 0 must floor toward negative infinity deterministically,
		// not toward zero (Go integer division truncates toward zero).
		{"hour negative mid", -1, Hourly, -hourUS},
		{"hour negative exact", -hourUS, Hourly, -hourUS},
		{"hour negative just below", -hourUS - 1, Hourly, -2 * hourUS},
		{"day negative mid", -1, Daily, -dayUS},
		{"day negative exact", -dayUS, Daily, -dayUS},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := BucketTS(tc.tsUS, tc.bucket); got != tc.want {
				t.Errorf("BucketTS(%d, %v) = %d, want %d", tc.tsUS, tc.bucket, got, tc.want)
			}
		})
	}
}

// TestRollupGolden builds a hand-crafted input and asserts the exact output
// slice. It covers: the six-dimension fan-out from one op, tool-id formatting
// with and without a namespace, a running op (EndTS nil) contributing 0
// duration, failures, two source formats, and two hour buckets.
func TestRollupGolden(t *testing.T) {
	t.Parallel()

	// Bucket A = hour 100; bucket B = hour 101 (the next hour).
	bucketA := 100 * hourUS
	bucketB := 101 * hourUS

	ops := []OpRow{
		// Op 1: an llm call in bucket A, source claude_code, closed (duration 1000).
		{
			StartTS:          bucketA + 10,
			EndTS:            ptr(bucketA + 1010),
			DurationUS:       1000,
			SourceFormat:     "claude_code",
			Kind:             "llm",
			Model:            "claude",
			Provider:         "anthropic",
			AgentName:        "main",
			Cwd:              "/repo",
			CostUSD:          0.5,
			TokensIn:         100,
			TokensOut:        200,
			TokensCacheRead:  10,
			TokensCacheWrite: 5,
			Failed:           false,
		},
		// Op 2: a tool call WITH namespace in bucket A, same source, failed,
		// running (EndTS nil) so duration contributes 0 even though DurationUS
		// is set (running ops never add duration).
		{
			StartTS:       bucketA + 20,
			EndTS:         nil,
			DurationUS:    9999,
			SourceFormat:  "claude_code",
			Kind:          "tool",
			ToolNamespace: "mcp:fs",
			ToolName:      "read",
			Provider:      "anthropic",
			AgentName:     "main",
			Cwd:           "/repo",
			CostUSD:       0,
			TokensIn:      1,
			Failed:        true,
		},
		// Op 3: a tool call WITHOUT namespace in bucket B, source codex, closed.
		{
			StartTS:       bucketB + 30,
			EndTS:         ptr(bucketB + 2030),
			DurationUS:    2000,
			SourceFormat:  "codex",
			Kind:          "tool",
			ToolNamespace: "",
			ToolName:      "bash",
			Provider:      "openai",
			AgentName:     "worker",
			Cwd:           "/other",
			CostUSD:       0.1,
			TokensOut:     7,
			Failed:        false,
		},
	}

	starts := []SessionStart{
		// Session start in bucket A, claude_code, agent main, cwd /repo.
		{StartTS: bucketA + 5, SourceFormat: "claude_code", AgentName: "main", Cwd: "/repo"},
	}

	got := Rollup(ops, starts, Hourly, Options{})

	// Build the expected rows by hand, then sort with the same comparator the
	// implementation must use so the golden is order-independent to author.
	want := []RollupRow{
		// --- bucket A, claude_code ---
		// total: ops 1+2 metrics + 1 session_start.
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "total", DimensionValue: "",
			OpCount: 2, TokensIn: 101, TokensOut: 200, TokensCacheRead: 10, TokensCacheWrite: 5,
			CostUSD: 0.5, Failures: 1, DurationUS: 1000, SessionStarts: 1,
		},
		// model row: only op 1 (kind=llm).
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "model", DimensionValue: "claude",
			OpCount: 1, TokensIn: 100, TokensOut: 200, TokensCacheRead: 10, TokensCacheWrite: 5,
			CostUSD: 0.5, Failures: 0, DurationUS: 1000, SessionStarts: 0,
		},
		// provider row: ops 1+2 (both anthropic).
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "provider", DimensionValue: "anthropic",
			OpCount: 2, TokensIn: 101, TokensOut: 200, TokensCacheRead: 10, TokensCacheWrite: 5,
			CostUSD: 0.5, Failures: 1, DurationUS: 1000, SessionStarts: 0,
		},
		// tool row: only op 2 (kind=tool), id "mcp:fs.read".
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "tool", DimensionValue: "mcp:fs.read",
			OpCount: 1, TokensIn: 1, TokensOut: 0, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0, Failures: 1, DurationUS: 0, SessionStarts: 0,
		},
		// agent row: ops 1+2 + session_start.
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "agent", DimensionValue: "main",
			OpCount: 2, TokensIn: 101, TokensOut: 200, TokensCacheRead: 10, TokensCacheWrite: 5,
			CostUSD: 0.5, Failures: 1, DurationUS: 1000, SessionStarts: 1,
		},
		// cwd row: ops 1+2 + session_start.
		{
			BucketTS: bucketA, SourceFormat: "claude_code", Dimension: "cwd", DimensionValue: "/repo",
			OpCount: 2, TokensIn: 101, TokensOut: 200, TokensCacheRead: 10, TokensCacheWrite: 5,
			CostUSD: 0.5, Failures: 1, DurationUS: 1000, SessionStarts: 1,
		},

		// --- bucket B, codex ---
		// total: op 3.
		{
			BucketTS: bucketB, SourceFormat: "codex", Dimension: "total", DimensionValue: "",
			OpCount: 1, TokensIn: 0, TokensOut: 7, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0.1, Failures: 0, DurationUS: 2000, SessionStarts: 0,
		},
		// provider row: op 3 (openai).
		{
			BucketTS: bucketB, SourceFormat: "codex", Dimension: "provider", DimensionValue: "openai",
			OpCount: 1, TokensIn: 0, TokensOut: 7, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0.1, Failures: 0, DurationUS: 2000, SessionStarts: 0,
		},
		// tool row: op 3, id "bash" (no namespace → bare name).
		{
			BucketTS: bucketB, SourceFormat: "codex", Dimension: "tool", DimensionValue: "bash",
			OpCount: 1, TokensIn: 0, TokensOut: 7, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0.1, Failures: 0, DurationUS: 2000, SessionStarts: 0,
		},
		// agent row: op 3 (worker).
		{
			BucketTS: bucketB, SourceFormat: "codex", Dimension: "agent", DimensionValue: "worker",
			OpCount: 1, TokensIn: 0, TokensOut: 7, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0.1, Failures: 0, DurationUS: 2000, SessionStarts: 0,
		},
		// cwd row: op 3 (/other).
		{
			BucketTS: bucketB, SourceFormat: "codex", Dimension: "cwd", DimensionValue: "/other",
			OpCount: 1, TokensIn: 0, TokensOut: 7, TokensCacheRead: 0, TokensCacheWrite: 0,
			CostUSD: 0.1, Failures: 0, DurationUS: 2000, SessionStarts: 0,
		},
	}
	sortRows(want)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Rollup golden mismatch (-want +got):\n%s", diff)
	}
}

// TestRollupDeterministicOutput asserts the output is sorted by the documented
// key and that the same input yields a byte-identical slice across calls. This
// pins the property that makes the Chunk-6 backfill-vs-incremental diff gate
// well-defined.
func TestRollupDeterministicOutput(t *testing.T) {
	t.Parallel()

	ops := []OpRow{
		{StartTS: 5 * hourUS, EndTS: ptr(5*hourUS + 1), DurationUS: 1, SourceFormat: "codex", Kind: "tool", ToolName: "z", Provider: "openai"},
		{StartTS: 5 * hourUS, EndTS: ptr(5*hourUS + 1), DurationUS: 1, SourceFormat: "claude_code", Kind: "llm", Model: "a", Provider: "anthropic"},
		{StartTS: 5 * hourUS, EndTS: ptr(5*hourUS + 1), DurationUS: 1, SourceFormat: "codex", Kind: "tool", ToolName: "a", Provider: "openai"},
	}

	first := Rollup(ops, nil, Hourly, Options{})

	// Output must be sorted by (BucketTS, SourceFormat, Dimension, DimensionValue).
	if !sort.SliceIsSorted(first, func(i, j int) bool { return less(first[i], first[j]) }) {
		t.Fatalf("output is not sorted by the documented key")
	}

	// Re-running with the same input must produce an identical slice.
	second := Rollup(ops, nil, Hourly, Options{})
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("Rollup is not deterministic across calls (-first +second):\n%s", diff)
	}
}

// TestAdditivityDailyEqualsHourly is the core invariant: a day's daily rollup
// equals the element-wise SUM of that day's hourly rollups, grouped by
// (source_format, dimension, dimension_value).
func TestAdditivityDailyEqualsHourly(t *testing.T) {
	t.Parallel()

	base := 200 * dayUS // an arbitrary day start.
	// Spread ops across several distinct hours within the same UTC day, with a
	// mix of kinds, sources, failures, running ops, and repeated dimension
	// values so the sums are non-trivial.
	ops := []OpRow{
		{StartTS: base + 0*hourUS + 1, EndTS: ptr(base + 0*hourUS + 101), DurationUS: 100, SourceFormat: "claude_code", Kind: "llm", Model: "m1", Provider: "anthropic", AgentName: "a1", Cwd: "/c1", CostUSD: 0.10, TokensIn: 10, TokensOut: 20, TokensCacheRead: 1, TokensCacheWrite: 2},
		{StartTS: base + 0*hourUS + 2, EndTS: nil, DurationUS: 5000, SourceFormat: "claude_code", Kind: "tool", ToolName: "read", ToolNamespace: "fs", Provider: "anthropic", AgentName: "a1", Cwd: "/c1", Failed: true, TokensIn: 3},
		{StartTS: base + 3*hourUS + 9, EndTS: ptr(base + 3*hourUS + 309), DurationUS: 300, SourceFormat: "codex", Kind: "llm", Model: "m2", Provider: "openai", AgentName: "a2", Cwd: "/c2", CostUSD: 0.25, TokensIn: 5, TokensOut: 6},
		{StartTS: base + 3*hourUS + 50, EndTS: ptr(base + 3*hourUS + 80), DurationUS: 30, SourceFormat: "claude_code", Kind: "llm", Model: "m1", Provider: "anthropic", AgentName: "a1", Cwd: "/c1", CostUSD: 0.05, TokensIn: 1, TokensOut: 2},
		{StartTS: base + 23*hourUS + 100, EndTS: ptr(base + 23*hourUS + 700), DurationUS: 600, SourceFormat: "codex", Kind: "tool", ToolName: "bash", Provider: "openai", AgentName: "a2", Cwd: "/c2", Failed: true, CostUSD: 0.01, TokensOut: 9},
	}
	starts := []SessionStart{
		{StartTS: base + 0*hourUS + 1, SourceFormat: "claude_code", AgentName: "a1", Cwd: "/c1"},
		{StartTS: base + 3*hourUS + 5, SourceFormat: "codex", AgentName: "a2", Cwd: "/c2"},
		{StartTS: base + 23*hourUS + 5, SourceFormat: "codex", AgentName: "a2", Cwd: "/c2"},
	}

	hourly := Rollup(ops, starts, Hourly, Options{})
	daily := Rollup(ops, starts, Daily, Options{})

	// Sum the hourly rows by (source, dimension, value) — collapsing the
	// bucket axis — and compare to the daily rows keyed identically.
	type natKey struct{ src, dim, val string }
	sumHourly := map[natKey]RollupRow{}
	for _, r := range hourly {
		k := natKey{r.SourceFormat, r.Dimension, r.DimensionValue}
		acc := sumHourly[k]
		acc.OpCount += r.OpCount
		acc.TokensIn += r.TokensIn
		acc.TokensOut += r.TokensOut
		acc.TokensCacheRead += r.TokensCacheRead
		acc.TokensCacheWrite += r.TokensCacheWrite
		acc.CostUSD += r.CostUSD
		acc.Failures += r.Failures
		acc.DurationUS += r.DurationUS
		acc.SessionStarts += r.SessionStarts
		sumHourly[k] = acc
	}

	if len(daily) != len(sumHourly) {
		t.Fatalf("daily has %d rows, summed-hourly has %d distinct keys", len(daily), len(sumHourly))
	}
	for _, d := range daily {
		k := natKey{d.SourceFormat, d.Dimension, d.DimensionValue}
		h, ok := sumHourly[k]
		if !ok {
			t.Fatalf("daily key %+v has no matching hourly sum", k)
		}
		// All daily rows share the same bucket (the single day); assert it.
		if d.BucketTS != base {
			t.Errorf("daily bucket = %d, want %d", d.BucketTS, base)
		}
		if d.OpCount != h.OpCount || d.TokensIn != h.TokensIn || d.TokensOut != h.TokensOut ||
			d.TokensCacheRead != h.TokensCacheRead || d.TokensCacheWrite != h.TokensCacheWrite ||
			d.CostUSD != h.CostUSD || d.Failures != h.Failures || d.DurationUS != h.DurationUS ||
			d.SessionStarts != h.SessionStarts {
			t.Errorf("additivity broken for %+v:\n daily=%+v\n Σhourly=%+v", k, d, h)
		}
	}
}

// TestSessionStartsDimensions asserts a session start is counted exactly once,
// in its start bucket, on the total/agent/cwd rows only — never on
// model/provider/tool rows.
func TestSessionStartsDimensions(t *testing.T) {
	t.Parallel()

	bucket := 7 * hourUS
	// One llm op so model/provider rows exist, plus one session start sharing
	// the same agent and cwd.
	ops := []OpRow{
		{StartTS: bucket + 1, EndTS: ptr(bucket + 2), DurationUS: 1, SourceFormat: "opencode", Kind: "llm", Model: "glm", Provider: "z", AgentName: "ag", Cwd: "/w"},
	}
	starts := []SessionStart{
		{StartTS: bucket + 3, SourceFormat: "opencode", AgentName: "ag", Cwd: "/w"},
	}
	rows := Rollup(ops, starts, Hourly, Options{})

	// total/agent/cwd carry session_starts=1.
	if r := findRow(t, rows, bucket, "opencode", "total", ""); r.SessionStarts != 1 {
		t.Errorf("total session_starts = %d, want 1", r.SessionStarts)
	}
	if r := findRow(t, rows, bucket, "opencode", "agent", "ag"); r.SessionStarts != 1 {
		t.Errorf("agent session_starts = %d, want 1", r.SessionStarts)
	}
	if r := findRow(t, rows, bucket, "opencode", "cwd", "/w"); r.SessionStarts != 1 {
		t.Errorf("cwd session_starts = %d, want 1", r.SessionStarts)
	}
	// model/provider carry session_starts=0 (op-only dimensions).
	if r := findRow(t, rows, bucket, "opencode", "model", "glm"); r.SessionStarts != 0 {
		t.Errorf("model session_starts = %d, want 0", r.SessionStarts)
	}
	if r := findRow(t, rows, bucket, "opencode", "provider", "z"); r.SessionStarts != 0 {
		t.Errorf("provider session_starts = %d, want 0", r.SessionStarts)
	}
}

// TestSessionStartOnlyNoOps asserts a session start with no ops still produces
// total/agent/cwd rows (so "sessions started" is queryable even for empty
// sessions), and produces no model/provider/tool rows.
func TestSessionStartOnlyNoOps(t *testing.T) {
	t.Parallel()

	bucket := 9 * hourUS
	starts := []SessionStart{
		{StartTS: bucket + 1, SourceFormat: "codex", AgentName: "solo", Cwd: "/only"},
	}
	rows := Rollup(nil, starts, Hourly, Options{})

	if len(rows) != 3 {
		t.Fatalf("want exactly 3 rows (total/agent/cwd), got %d: %+v", len(rows), rows)
	}
	for _, dimVal := range [][2]string{{"total", ""}, {"agent", "solo"}, {"cwd", "/only"}} {
		r := findRow(t, rows, bucket, "codex", dimVal[0], dimVal[1])
		if r.SessionStarts != 1 || r.OpCount != 0 {
			t.Errorf("%s row = %+v, want session_starts=1 op_count=0", dimVal[0], r)
		}
	}
}

// TestOtherCollapse asserts the R1 tail-collapse: when distinct dimension
// values in a (bucket, source, dimension) group exceed the cap, the top-N by
// (op_count desc, dimension_value asc) are kept and the remaining tail folds
// into one "__other__" row whose metrics equal the Σ of the collapsed tail.
// The total dimension is never collapsed. Determinism is asserted by a second
// run.
func TestOtherCollapse(t *testing.T) {
	t.Parallel()

	bucket := 11 * hourUS
	// Build N distinct cwds with strictly decreasing op_count so the top-N is
	// unambiguous. cwd-0 has the most ops, cwd-(N-1) the fewest.
	const n = 6
	cap := 3
	var ops []OpRow
	for i := 0; i < n; i++ {
		// op_count for cwd-i is (n - i): cwd-0 → 6 ops, cwd-5 → 1 op.
		for j := 0; j < n-i; j++ {
			ops = append(ops, OpRow{
				StartTS:      bucket + int64(i*10+j),
				EndTS:        ptr(bucket + int64(i*10+j) + 1),
				DurationUS:   1,
				SourceFormat: "codex",
				Kind:         "internal", // no model/provider/tool fan-out; isolates cwd.
				Cwd:          fmt.Sprintf("/cwd-%d", i),
				CostUSD:      float64(i),
				TokensIn:     int64(i),
			})
		}
	}

	rows := Rollup(ops, nil, Hourly, Options{MaxRowsPerBucketDimension: cap})

	// Collect the cwd rows.
	var cwdRows []RollupRow
	for _, r := range rows {
		if r.Dimension == "cwd" {
			cwdRows = append(cwdRows, r)
		}
	}
	// Expect exactly cap+? — top-3 kept + 1 __other__ = 4 rows. But cap counts
	// total rows in the group INCLUDING __other__: keep top (cap-1) and fold the
	// rest, so the group never exceeds cap rows.
	if len(cwdRows) != cap {
		t.Fatalf("want exactly %d cwd rows after collapse, got %d: %+v", cap, len(cwdRows), cwdRows)
	}

	// The kept rows must be cwd-0 and cwd-1 (highest op_count) plus __other__.
	kept := map[string]RollupRow{}
	for _, r := range cwdRows {
		kept[r.DimensionValue] = r
	}
	if _, ok := kept["/cwd-0"]; !ok {
		t.Errorf("expected /cwd-0 (highest op_count) to be kept; got keys %v", keysOf(kept))
	}
	if _, ok := kept["/cwd-1"]; !ok {
		t.Errorf("expected /cwd-1 to be kept; got keys %v", keysOf(kept))
	}
	other, ok := kept["__other__"]
	if !ok {
		t.Fatalf("expected an __other__ row; got keys %v", keysOf(kept))
	}

	// __other__ metrics must equal the Σ of the collapsed tail (cwd-2..cwd-5).
	var wantOpCount, wantTokensIn int64
	var wantCost float64
	for i := 2; i < n; i++ {
		wantOpCount += int64(n - i) // ops per cwd-i
		wantTokensIn += int64(i) * int64(n-i)
		wantCost += float64(i) * float64(n-i)
	}
	if other.OpCount != wantOpCount {
		t.Errorf("__other__ op_count = %d, want %d", other.OpCount, wantOpCount)
	}
	if other.TokensIn != wantTokensIn {
		t.Errorf("__other__ tokens_in = %d, want %d", other.TokensIn, wantTokensIn)
	}
	if other.CostUSD != wantCost {
		t.Errorf("__other__ cost_usd = %v, want %v", other.CostUSD, wantCost)
	}

	// Additivity preserved: Σ of cwd rows (incl __other__) equals the total row.
	total := findRow(t, rows, bucket, "codex", "total", "")
	var sumCwdOps int64
	for _, r := range cwdRows {
		sumCwdOps += r.OpCount
	}
	if sumCwdOps != total.OpCount {
		t.Errorf("Σ cwd op_count = %d, want total op_count %d", sumCwdOps, total.OpCount)
	}

	// total must NEVER be collapsed (it has a single value, but assert it is present).
	if total.DimensionValue != "" {
		t.Errorf("total row value = %q, want empty", total.DimensionValue)
	}

	// Determinism: same input → identical kept set + identical slice.
	again := Rollup(ops, nil, Hourly, Options{MaxRowsPerBucketDimension: cap})
	if diff := cmp.Diff(rows, again); diff != "" {
		t.Errorf("collapse is not deterministic (-first +second):\n%s", diff)
	}
}

// TestOtherCollapseTieBreak asserts the secondary sort key (dimension_value
// asc) decides which equal-op_count values are kept, so the top-N is fully
// deterministic even under ties.
func TestOtherCollapseTieBreak(t *testing.T) {
	t.Parallel()

	bucket := 13 * hourUS
	cap := 2 // keep top-1 + __other__.
	// Three cwds with IDENTICAL op_count (1 each). Tie broken by value asc:
	// "/a" kept, "/b" and "/c" collapse into __other__.
	ops := []OpRow{
		{StartTS: bucket + 1, EndTS: ptr(bucket + 2), DurationUS: 1, SourceFormat: "codex", Kind: "internal", Cwd: "/c"},
		{StartTS: bucket + 3, EndTS: ptr(bucket + 4), DurationUS: 1, SourceFormat: "codex", Kind: "internal", Cwd: "/a"},
		{StartTS: bucket + 5, EndTS: ptr(bucket + 6), DurationUS: 1, SourceFormat: "codex", Kind: "internal", Cwd: "/b"},
	}
	rows := Rollup(ops, nil, Hourly, Options{MaxRowsPerBucketDimension: cap})

	kept := map[string]bool{}
	for _, r := range rows {
		if r.Dimension == "cwd" {
			kept[r.DimensionValue] = true
		}
	}
	if !kept["/a"] {
		t.Errorf("tie-break: expected lexically-smallest /a to be kept; kept=%v", kept)
	}
	if !kept["__other__"] {
		t.Errorf("tie-break: expected __other__ row; kept=%v", kept)
	}
	if kept["/b"] || kept["/c"] {
		t.Errorf("tie-break: /b and /c should have collapsed; kept=%v", kept)
	}
}

// TestEmptyInput asserts empty inputs yield an empty (non-nil-safe) slice.
func TestEmptyInput(t *testing.T) {
	t.Parallel()

	if got := Rollup(nil, nil, Hourly, Options{}); len(got) != 0 {
		t.Errorf("Rollup(nil, nil) = %+v, want empty", got)
	}
	if got := Rollup([]OpRow{}, []SessionStart{}, Daily, Options{}); len(got) != 0 {
		t.Errorf("Rollup([], []) = %+v, want empty", got)
	}
}

// TestOpAllEmptyDimsOnlyTotal asserts an op with every dimension field empty
// (no model/provider/tool/agent/cwd) contributes ONLY to the total row.
func TestOpAllEmptyDimsOnlyTotal(t *testing.T) {
	t.Parallel()

	bucket := 15 * hourUS
	ops := []OpRow{
		// kind=internal, no model/provider/tool/agent/cwd.
		{StartTS: bucket + 1, EndTS: ptr(bucket + 11), DurationUS: 10, SourceFormat: "aiagent_v3", Kind: "internal", CostUSD: 0.3},
	}
	rows := Rollup(ops, nil, Hourly, Options{})

	if len(rows) != 1 {
		t.Fatalf("want exactly 1 row (total only), got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Dimension != "total" || r.DimensionValue != "" || r.OpCount != 1 || r.DurationUS != 10 || r.CostUSD != 0.3 {
		t.Errorf("total row = %+v, unexpected", r)
	}
}

// TestLLMWithoutModel asserts an llm op with an empty Model produces no model
// row (the model fan-out is conditional on a non-empty model), but still
// contributes to total/provider.
func TestLLMWithoutModel(t *testing.T) {
	t.Parallel()

	bucket := 17 * hourUS
	ops := []OpRow{
		{StartTS: bucket + 1, EndTS: ptr(bucket + 2), DurationUS: 1, SourceFormat: "codex", Kind: "llm", Model: "", Provider: "openai"},
	}
	rows := Rollup(ops, nil, Hourly, Options{})

	for _, r := range rows {
		if r.Dimension == "model" {
			t.Errorf("unexpected model row for empty-model llm op: %+v", r)
		}
	}
	// provider + total still present.
	findRow(t, rows, bucket, "codex", "total", "")
	findRow(t, rows, bucket, "codex", "provider", "openai")
}

// TestToolKindWithoutToolName asserts a tool op whose ToolName is empty (and
// namespace empty) produces no tool row (empty tool-id is not a meaningful
// dimension value), but still contributes to total.
func TestToolKindWithoutToolName(t *testing.T) {
	t.Parallel()

	bucket := 19 * hourUS
	ops := []OpRow{
		{StartTS: bucket + 1, EndTS: ptr(bucket + 2), DurationUS: 1, SourceFormat: "codex", Kind: "tool", ToolName: "", ToolNamespace: ""},
	}
	rows := Rollup(ops, nil, Hourly, Options{})

	for _, r := range rows {
		if r.Dimension == "tool" {
			t.Errorf("unexpected tool row for empty tool-id op: %+v", r)
		}
	}
	findRow(t, rows, bucket, "codex", "total", "")
}

// keysOf returns the keys of a RollupRow map for diagnostic messages.
func keysOf(m map[string]RollupRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
