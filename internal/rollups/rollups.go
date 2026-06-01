package rollups

import "sort"

// Bucket is the time-bucket granularity for a rollup fold.
type Bucket int

const (
	// Hourly buckets a timestamp to its UTC hour start.
	Hourly Bucket = iota
	// Daily buckets a timestamp to its UTC day start.
	Daily
)

// Bucket spans in microseconds (UTC, integer math — no time package, no DST).
const (
	hourSpanUS = int64(3_600_000_000)
	daySpanUS  = int64(86_400_000_000)
)

// otherValue is the dimension_value of the synthetic tail-collapse row created
// when a (bucket, source_format, dimension) group exceeds the row cap (R1
// safety bound, data-model.md §"R1 safety bound").
const otherValue = "__other__"

// defaultMaxRowsPerBucketDimension is the per-(bucket, source_format,
// dimension) row cap applied when Options.MaxRowsPerBucketDimension is <= 0.
// Mirrors data-model.md §"R1 safety bound" (maxRollupRowsPerBucket default
// 2000).
const defaultMaxRowsPerBucketDimension = 2000

// Dimension names. These mirror the rollup_* tables' dimension column
// (data-model.md §"Rollup tables").
const (
	dimTotal    = "total"
	dimModel    = "model"
	dimProvider = "provider"
	dimTool     = "tool"
	dimAgent    = "agent"
	dimCwd      = "cwd"
)

// OpRow is one canonical op projected into the fields the rollup fold needs.
// Fields are denormalized from the op's session where noted (AgentName, Cwd).
// The DB-reading layer (Chunk 4/5) maps store rows into this struct.
type OpRow struct {
	StartTS      int64  // op start, UTC microseconds; decides the bucket.
	EndTS        *int64 // op end; nil or <= StartTS means still running.
	DurationUS   int64  // closed-op duration; contributes 0 while running.
	SourceFormat string // 'aiagent_v3'|'aiagent_v2'|'claude_code'|'codex'|'opencode'.

	Kind          string // 'llm'|'tool'|'session'|'reasoning'|'internal'|'system'|'compaction'.
	Model         string // populated for kind='llm'.
	Provider      string // canonical provider, any kind.
	ToolNamespace string // kind='tool': 'mcp:<server>'|'shell'|'fs'|... (may be "").
	ToolName      string // kind='tool': tool name.
	AgentName     string // session's agent_name (denormalized).
	Cwd           string // session's cwd (denormalized).

	CostUSD          float64
	TokensIn         int64
	TokensOut        int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	Failed           bool // true when the op's status is a failure.
}

// SessionStart carries the minimum needed to attribute the additive
// session_starts metric: each session is counted once, in the bucket of its
// start_ts, on the total/agent/cwd dimensions only.
type SessionStart struct {
	StartTS      int64
	SourceFormat string
	AgentName    string
	Cwd          string
}

// RollupRow mirrors one row of the rollup_hourly / rollup_daily tables
// (data-model.md §"Rollup tables"). Every metric field is SUM-additive.
type RollupRow struct {
	BucketTS       int64
	SourceFormat   string
	Dimension      string
	DimensionValue string

	OpCount          int64
	TokensIn         int64
	TokensOut        int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	CostUSD          float64
	Failures         int64
	DurationUS       int64
	SessionStarts    int64
}

// Options tunes the fold.
type Options struct {
	// MaxRowsPerBucketDimension is the per-(bucket, source_format, dimension)
	// row cap for the R1 tail-collapse. <= 0 means the package default
	// (defaultMaxRowsPerBucketDimension). The 'total' dimension (single value)
	// is never collapsed.
	MaxRowsPerBucketDimension int
}

// BucketTS floors tsUS to the start of its UTC bucket. Integer math only; the
// result floors toward negative infinity so tsUS <= 0 is deterministic (Go's
// integer division truncates toward zero, which would put -1 in bucket 0 — we
// correct that so a negative timestamp lands in the bucket strictly at or below
// it).
func BucketTS(tsUS int64, bucket Bucket) int64 {
	span := hourSpanUS
	if bucket == Daily {
		span = daySpanUS
	}
	q := tsUS / span
	if tsUS%span != 0 && tsUS < 0 {
		q--
	}
	return q * span
}

// rowKey is the natural identity of a rollup row (its PRIMARY KEY in the
// rollup_* tables).
type rowKey struct {
	bucketTS       int64
	sourceFormat   string
	dimension      string
	dimensionValue string
}

// Rollup folds ops and session starts into deterministic, additive RollupRows
// for the given bucket granularity.
//
// Per-op fan-out (within the op's source_format and bucket): total, model
// (kind='llm', Model!=""), provider (Provider!=""), tool (kind='tool', tool-id
// non-empty), agent (AgentName!=""), cwd (Cwd!=""). session_starts is added to
// the total/agent/cwd rows of each session's start bucket (never to
// model/provider/tool). The output is sorted by (bucket_ts, source_format,
// dimension, dimension_value) and tail-collapsed per Options.
func Rollup(ops []OpRow, starts []SessionStart, bucket Bucket, opts Options) []RollupRow {
	acc := make(map[rowKey]*RollupRow, len(ops)*4)
	for i := range ops {
		foldOp(acc, &ops[i], bucket)
	}
	for i := range starts {
		foldSessionStart(acc, &starts[i], bucket)
	}

	rows := make([]RollupRow, 0, len(acc))
	for _, r := range acc {
		rows = append(rows, *r)
	}
	rows = collapse(rows, capOrDefault(opts))
	sortRows(rows)
	return rows
}

// capOrDefault resolves the effective per-group row cap.
func capOrDefault(opts Options) int {
	if opts.MaxRowsPerBucketDimension > 0 {
		return opts.MaxRowsPerBucketDimension
	}
	return defaultMaxRowsPerBucketDimension
}

// foldOp adds one op's metrics to every dimension row it contributes to.
func foldOp(acc map[rowKey]*RollupRow, op *OpRow, bucket Bucket) {
	b := BucketTS(op.StartTS, bucket)
	addOpMetrics(acc, rowKey{b, op.SourceFormat, dimTotal, ""}, op)
	if op.Kind == "llm" && op.Model != "" {
		addOpMetrics(acc, rowKey{b, op.SourceFormat, dimModel, op.Model}, op)
	}
	if op.Provider != "" {
		addOpMetrics(acc, rowKey{b, op.SourceFormat, dimProvider, op.Provider}, op)
	}
	if op.Kind == "tool" {
		if id := toolID(op.ToolNamespace, op.ToolName); id != "" {
			addOpMetrics(acc, rowKey{b, op.SourceFormat, dimTool, id}, op)
		}
	}
	if op.AgentName != "" {
		addOpMetrics(acc, rowKey{b, op.SourceFormat, dimAgent, op.AgentName}, op)
	}
	if op.Cwd != "" {
		addOpMetrics(acc, rowKey{b, op.SourceFormat, dimCwd, op.Cwd}, op)
	}
}

// addOpMetrics accumulates one op's additive metrics into the row at key,
// creating the row on first touch. session_starts is left untouched here (it is
// a session-level metric).
func addOpMetrics(acc map[rowKey]*RollupRow, key rowKey, op *OpRow) {
	r := rowAt(acc, key)
	r.OpCount++
	r.TokensIn += op.TokensIn
	r.TokensOut += op.TokensOut
	r.TokensCacheRead += op.TokensCacheRead
	r.TokensCacheWrite += op.TokensCacheWrite
	r.CostUSD += op.CostUSD
	if op.Failed {
		r.Failures++
	}
	if running(op) {
		return // running ops contribute 0 duration (data-model.md §Rollup tables).
	}
	r.DurationUS += op.DurationUS
}

// foldSessionStart adds session_starts=1 to the total/agent/cwd rows of the
// session's start bucket. It never touches model/provider/tool rows.
func foldSessionStart(acc map[rowKey]*RollupRow, s *SessionStart, bucket Bucket) {
	b := BucketTS(s.StartTS, bucket)
	rowAt(acc, rowKey{b, s.SourceFormat, dimTotal, ""}).SessionStarts++
	if s.AgentName != "" {
		rowAt(acc, rowKey{b, s.SourceFormat, dimAgent, s.AgentName}).SessionStarts++
	}
	if s.Cwd != "" {
		rowAt(acc, rowKey{b, s.SourceFormat, dimCwd, s.Cwd}).SessionStarts++
	}
}

// rowAt returns the row at key, creating an empty one (with the key's identity
// fields set) on first touch.
func rowAt(acc map[rowKey]*RollupRow, key rowKey) *RollupRow {
	if r, ok := acc[key]; ok {
		return r
	}
	r := &RollupRow{
		BucketTS:       key.bucketTS,
		SourceFormat:   key.sourceFormat,
		Dimension:      key.dimension,
		DimensionValue: key.dimensionValue,
	}
	acc[key] = r
	return r
}

// running reports whether an op is still in flight (no usable end). Such ops
// contribute 0 duration.
func running(op *OpRow) bool {
	return op.EndTS == nil || *op.EndTS <= op.StartTS
}

// toolID builds the tool dimension value: "<namespace>.<name>", or "<name>"
// when namespace is empty (the same convention as the rollup spec and topology
// node ids, data-model.md §Rollup tables — Per-op fan-out).
func toolID(namespace, name string) string {
	if name == "" {
		return ""
	}
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

// less is the canonical row ordering: (bucket_ts, source_format, dimension,
// dimension_value), all ascending. It defines the deterministic output order
// and the diff-gate baseline.
func less(a, b RollupRow) bool {
	if a.BucketTS != b.BucketTS {
		return a.BucketTS < b.BucketTS
	}
	if a.SourceFormat != b.SourceFormat {
		return a.SourceFormat < b.SourceFormat
	}
	if a.Dimension != b.Dimension {
		return a.Dimension < b.Dimension
	}
	return a.DimensionValue < b.DimensionValue
}

// sortRows sorts rows in place by the canonical key.
func sortRows(rows []RollupRow) {
	sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
}
