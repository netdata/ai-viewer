package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/ingest"
)

// i64 renders an int64 µs timestamp as a query-string value.
func i64(v int64) string { return strconv.FormatInt(v, 10) }

// aggregateBody mirrors GET /api/stats/aggregate.
type aggregateBody struct {
	Buckets []struct {
		BucketTS int64 `json:"bucket_ts"`
		Series   []struct {
			Key   string  `json:"key"`
			Value float64 `json:"value"`
		} `json:"series"`
	} `json:"buckets"`
	Bucket string `json:"bucket"`
	Metric string `json:"metric"`
}

// getAggregate issues GET /api/stats/aggregate?<query> and decodes the body.
func getAggregate(t *testing.T, p *Presenter, query string) (int, aggregateBody, errorEnvelope) {
	t.Helper()
	return doStatsGet[aggregateBody](t, p, "/api/stats/aggregate", query)
}

// doStatsGet is the shared GET+decode helper for the two rollup endpoints.
func doStatsGet[T any](t *testing.T, p *Presenter, route, query string) (int, T, errorEnvelope) {
	t.Helper()
	path := route
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body T
	var env errorEnvelope
	raw := rr.Body.Bytes()
	if len(raw) > 0 {
		if rr.Code >= 400 {
			_ = json.Unmarshal(raw, &env)
		} else if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v (raw=%q)", err, raw)
		}
	}
	return rr.Code, body, env
}

// --- Rollup fixture anchored at fixedTime (2026-05-27 12:00:00 UTC) ----------
//
// openHourStart = 2026-05-27 12:00:00; openDayStart = 2026-05-27 00:00:00.
// The fixture spreads ops over two CLOSED days (the 25th + 26th), the current
// day's already-closed 11:00 hour, and the OPEN hour (an op at exactly `now`).
// It mixes models/providers/tools/agents/cwds, a failed op, and two
// source_formats so every (group_by × metric) combo has something to assert.

const (
	hourUS = int64(3_600_000_000)
	dayUS  = int64(86_400_000_000)
)

// rollupHour returns the µs timestamp `h` hours after `base` (a UTC bucket
// start), offset by `min` minutes so the op lands inside that hour's bucket.
func atOffset(base int64, hours, mins int) int64 {
	return base + int64(hours)*hourUS + int64(mins)*60_000_000
}

// seedRollupFixture seeds the multi-dimension fixture and returns the day/hour
// bucket anchors the tests assert against. day25/day26 are CLOSED daily
// buckets; openDay/openHour are the open buckets at fixedTime.
type rollupAnchors struct {
	day25, day26      int64 // closed UTC day starts
	openDay, openHour int64 // open day / open hour at fixedTime
	closedHourToday   int64 // 11:00 today: a closed hour within the open day
}

func seedRollupFixture(t *testing.T, db *sql.DB) rollupAnchors {
	t.Helper()
	now := fixedTime.UnixMicro()
	a := rollupAnchors{
		day25:           time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro(),
		day26:           time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC).UnixMicro(),
		openDay:         time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC).UnixMicro(),
		openHour:        time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro(),
		closedHourToday: time.Date(2026, 5, 27, 11, 0, 0, 0, time.UTC).UnixMicro(),
	}
	if a.openHour != now {
		t.Fatalf("fixture assumes fixedTime is on the hour: openHour=%d now=%d", a.openHour, now)
	}

	// Two sources of different formats.
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", a.day25)
	seedSource(t, db, "codex:/p", "codex", "/p", a.day25)

	// Day 25 (closed): two ops in one session, anthropic/claude, shell.Bash.
	seedRollupSession(t, db, "s25", "aiagent_v3:/p", "nedi", "anthropic", "/w1", atOffset(a.day25, 9, 0))
	seedRollupOp(t, db, rollupOpSpec{id: "o25a", sess: "s25", seq: 1, kind: "llm", name: "claude-opus", model: "claude-opus",
		provider: "anthropic", start: atOffset(a.day25, 9, 0), dur: 1000, status: "completed",
		tokIn: 100, tokOut: 200, cost: 0.10})
	seedRollupOp(t, db, rollupOpSpec{id: "o25b", sess: "s25", seq: 2, kind: "tool", name: "Bash", toolNS: "shell",
		start: atOffset(a.day25, 9, 30), dur: 300, status: "completed", tokIn: 10, tokOut: 20, cost: 0.01})

	// Day 26 (closed): a gpt op on codex + a FAILED tool op, agent "worker".
	seedRollupSession(t, db, "s26", "codex:/p", "worker", "openai", "/w2", atOffset(a.day26, 14, 0))
	seedRollupOp(t, db, rollupOpSpec{id: "o26a", sess: "s26", seq: 1, kind: "llm", name: "gpt-5", model: "gpt-5",
		provider: "openai", start: atOffset(a.day26, 14, 0), dur: 2000, status: "completed",
		tokIn: 50, tokOut: 60, cost: 0.05})
	seedRollupOp(t, db, rollupOpSpec{id: "o26b", sess: "s26", seq: 2, kind: "tool", name: "Read", toolNS: "fs",
		start: atOffset(a.day26, 14, 10), dur: 100, status: "failed", tokIn: 5, tokOut: 5, cost: 0.0})

	// Closed hour TODAY (11:00) — a closed hour inside the still-open day.
	seedRollupSession(t, db, "s27a", "aiagent_v3:/p", "nedi", "anthropic", "/w1", a.closedHourToday)
	seedRollupOp(t, db, rollupOpSpec{id: "o27a", sess: "s27a", seq: 1, kind: "llm", name: "claude-opus", model: "claude-opus",
		provider: "anthropic", start: a.closedHourToday, dur: 500, status: "completed",
		tokIn: 70, tokOut: 80, cost: 0.07})

	// OPEN hour (op at exactly `now`): claude on anthropic, agent nedi.
	seedRollupSession(t, db, "s27open", "aiagent_v3:/p", "nedi", "anthropic", "/w1", a.openHour)
	seedRollupOp(t, db, rollupOpSpec{id: "o27open", sess: "s27open", seq: 1, kind: "llm", name: "claude-opus", model: "claude-opus",
		provider: "anthropic", start: a.openHour, dur: 0, status: "running",
		tokIn: 9, tokOut: 0, cost: 0.009})
	return a
}

// seedRollupSession seeds one root session with one turn (turn id = sess+"-t").
func seedRollupSession(t *testing.T, db *sql.DB, id, sourceID, agent, provider, cwd string, startTS int64) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, agent_name, provider, cwd, status,
    start_ts, last_activity_ts)
VALUES (?, ?, ?, ?, 'root', ?, ?, ?, 'completed', ?, ?)`,
		id, sourceID, id, id, agent, provider, cwd, startTS, startTS); err != nil {
		t.Fatalf("seed rollup session %s: %v", id, err)
	}
	if _, err := db.Exec(`
INSERT INTO turns (id, session_id, seq, start_ts, status) VALUES (?, ?, 1, ?, 'completed')`,
		id+"-t", id, startTS); err != nil {
		t.Fatalf("seed rollup turn for %s: %v", id, err)
	}
}

type rollupOpSpec struct {
	id, sess, kind, name string
	seq                  int64 // unique within the session's turn (UNIQUE(turn_id, seq))
	model, provider      string
	toolNS               string
	start, dur           int64
	status               string
	tokIn, tokOut        int64
	cost                 float64
}

// seedRollupOp inserts one op. A running op (dur==0/status running) has NULL
// end_ts and NULL duration_us, mirroring scanRollupOpRow's contract. seq must
// be unique within the session's turn.
func seedRollupOp(t *testing.T, db *sql.DB, o rollupOpSpec) {
	t.Helper()
	nullStr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	var endTS, dur any
	if o.dur > 0 {
		endTS = o.start + o.dur
		dur = o.dur
	}
	if _, err := db.Exec(`
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, tool_namespace, model, provider,
    start_ts, end_ts, duration_us, status, tokens_in, tokens_out, cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.id, o.sess+"-t", o.sess, o.seq, o.kind, o.name, nullStr(o.toolNS), nullStr(o.model), nullStr(o.provider),
		o.start, endTS, dur, o.status, o.tokIn, o.tokOut, o.cost); err != nil {
		t.Fatalf("seed rollup op %s: %v", o.id, err)
	}
}

// materializeRollups runs the ingest backfill against the same store, so the
// presenter's fast path reads real materialized closed-bucket rows.
func materializeRollups(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := ingest.BackfillRollups(context.Background(), db, fixedTime.UnixMicro(),
		slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
}

// TestStatsAggregate_FastPathMatchesLiveFold is the core parity test: for every
// (group_by × metric) combo, the rollup fast path (a time-only query, no cross
// filters) must equal a live fold of the SAME range. The live fold is forced by
// the same query restricted to the seeded source set via a status filter that
// matches every seeded session ('completed' OR 'running' would change the set,
// so we instead diff the fast path against a hand-independent recomputation by
// folding every op in-process). Here we compare the fast path against the
// live-fold code path directly by toggling a sources filter that selects ALL
// sources (which forces the live path while keeping the same data).
func TestStatsAggregate_FastPathMatchesLiveFold(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedRollupFixture(t, db)
	materializeRollups(t, db)

	groupBys := []string{"total", "model", "provider", "tool", "agent", "cwd", "source_format"}
	metrics := []string{"cost", "tokens_in", "tokens_out", "calls", "failures", "duration_us"}
	buckets := []string{"daily", "hourly"}

	for _, bucket := range buckets {
		for _, gb := range groupBys {
			for _, m := range metrics {
				name := bucket + "/" + gb + "/" + m
				t.Run(name, func(t *testing.T) {
					// Fast path: time-only (no cross filters).
					fastQ := "bucket=" + bucket + "&group_by=" + gb + "&metric=" + m
					codeFast, fast, env := getAggregate(t, p, fastQ)
					if codeFast != http.StatusOK {
						t.Fatalf("fast path status=%d env=%+v", codeFast, env)
					}
					// Live fold: same query + a sources filter naming BOTH
					// seeded sources. That forces the live-fold path (sources
					// binds source_id) over the identical data.
					liveQ := fastQ + "&sources=aiagent_v3:/p,codex:/p"
					codeLive, live, env2 := getAggregate(t, p, liveQ)
					if codeLive != http.StatusOK {
						t.Fatalf("live path status=%d env=%+v", codeLive, env2)
					}
					assertAggregateEqual(t, fast, live)
				})
			}
		}
	}
}

// assertAggregateEqual asserts two aggregate bodies carry the same buckets and
// per-(bucket,key) values (within float tolerance).
func assertAggregateEqual(t *testing.T, a, b aggregateBody) {
	t.Helper()
	am := aggregateToMap(a)
	bm := aggregateToMap(b)
	if len(am) != len(bm) {
		t.Fatalf("bucket count: fast=%d live=%d (fast=%+v live=%+v)", len(am), len(bm), a.Buckets, b.Buckets)
	}
	for bucketTS, aKeys := range am {
		bKeys, ok := bm[bucketTS]
		if !ok {
			t.Fatalf("bucket %d present in fast, absent in live", bucketTS)
		}
		if len(aKeys) != len(bKeys) {
			t.Fatalf("bucket %d series count: fast=%d live=%d", bucketTS, len(aKeys), len(bKeys))
		}
		for k, av := range aKeys {
			if bv := bKeys[k]; !floatEq(av, bv) {
				t.Fatalf("bucket %d key %q: fast=%v live=%v", bucketTS, k, av, bv)
			}
		}
	}
}

func aggregateToMap(a aggregateBody) map[int64]map[string]float64 {
	out := make(map[int64]map[string]float64)
	for _, b := range a.Buckets {
		m := make(map[string]float64)
		for _, s := range b.Series {
			m[s.Key] = s.Value
		}
		out[b.BucketTS] = m
	}
	return out
}

// TestStatsAggregate_HandComputedClosedAndOpen pins concrete numbers: the
// closed daily buckets come from the materialized rollups; the open day is
// folded live. group_by=total, metric=cost.
func TestStatsAggregate_HandComputedClosedAndOpen(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)

	code, body, env := getAggregate(t, p, "bucket=daily&group_by=total&metric=cost")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	got := aggregateToMap(body)
	// Day 25 total cost: 0.10 + 0.01 = 0.11.
	if v := got[a.day25][""]; !floatEq(v, 0.11) {
		t.Errorf("day25 total cost = %v, want 0.11", v)
	}
	// Day 26 total cost: 0.05 + 0.0 = 0.05.
	if v := got[a.day26][""]; !floatEq(v, 0.05) {
		t.Errorf("day26 total cost = %v, want 0.05", v)
	}
	// Open day total cost (live-folded: 0.07 closed-hour + 0.009 open-hour).
	if v := got[a.openDay][""]; !floatEq(v, 0.079) {
		t.Errorf("openDay total cost = %v, want 0.079", v)
	}
}

// TestStatsAggregate_OpenHourInclusion asserts an op in the current open HOUR
// appears in the open-hour bucket (live folded) while closed hours come from
// the rollups. metric=calls (op_count) makes the per-bucket counts obvious.
func TestStatsAggregate_OpenHourInclusion(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)

	code, body, env := getAggregate(t, p, "bucket=hourly&group_by=total&metric=calls")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	got := aggregateToMap(body)
	// Closed hour today (11:00) has exactly the one llm op.
	if v := got[a.closedHourToday][""]; !floatEq(v, 1) {
		t.Errorf("closed-hour-today calls = %v, want 1", v)
	}
	// Open hour (12:00) has exactly the one running op, folded live.
	if v := got[a.openHour][""]; !floatEq(v, 1) {
		t.Errorf("open-hour calls = %v, want 1", v)
	}
}

// TestStatsAggregate_FoldOrderDeterminism pins F4: the live-fold ops query MUST
// feed ops to the rollup fold in the SAME deterministic order the ingester uses
// (o.start_ts ASC, o.id ASC — rollup_backfill.go / rollup_refresh.go), or the
// floating-point cost SUM can differ from the materialized rollup by addition
// order. Float addition is NOT associative.
//
// All ops share one start_ts (so the only tiebreak is o.id ASC) and are INSERTED
// in a scrambled rowid order (op_c, op_a, op_b). Costs are chosen so the sum is
// order-sensitive: in id-ASC order [op_a=1e16, op_b=1.0, op_c=-1e16] the 1.0 is
// swallowed by 1e16 then cancelled to 0.0; in rowid order [op_c, op_a, op_b] the
// 1.0 survives as 1.0. The materialized rollup folds in id-ASC order (→0.0), so
// the live fold must too. Without the ORDER BY, SQLite returns rowid order
// (→1.0) and the parity below fails.
func TestStatsAggregate_FoldOrderDeterminism(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	start := atOffset(day, 9, 0)
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day)
	seedRollupSession(t, db, "sess", "aiagent_v3:/p", "nedi", "anthropic", "/w1", start)
	// INSERT order (rowid) is c, a, b; all share `start`. Costs are deliberately
	// float-order-sensitive (1e16 absorbs/cancels the 1.0 depending on order).
	costByID := map[string]float64{"opc": -1e16, "opa": 1e16, "opb": 1.0}
	for i, id := range []string{"opc", "opa", "opb"} {
		seedRollupOp(t, db, rollupOpSpec{id: id, sess: "sess", seq: int64(i + 1), kind: "llm",
			name: "claude-opus", model: "claude-opus", provider: "anthropic",
			start: start, dur: 100, status: "completed", cost: costByID[id]})
	}
	materializeRollups(t, db)

	// Fast path (reads the materialized rollup, folded id-ASC → 0.0).
	const q = "bucket=daily&group_by=total&metric=cost"
	_, fast, env := getAggregate(t, p, q)
	if env.Error.Code != "" {
		t.Fatalf("fast env=%+v", env)
	}
	// Live fold (forced via a sources filter selecting the one source). With the
	// ORDER BY it folds id-ASC → 0.0; without it, rowid order → 1.0.
	_, live, env2 := getAggregate(t, p, q+"&sources=aiagent_v3:/p")
	if env2.Error.Code != "" {
		t.Fatalf("live env=%+v", env2)
	}
	fastV := aggregateToMap(fast)[day][""]
	liveV := aggregateToMap(live)[day][""]
	if fastV != liveV {
		t.Fatalf("fold order non-determinism: fast=%v live=%v (must be byte-identical — needs ORDER BY o.start_ts, o.id)", fastV, liveV)
	}
	// Concretely: the id-ASC fold cancels to exactly 0.0.
	if fastV != 0.0 {
		t.Errorf("fast=%v, want 0.0 (1e16 + 1.0 + -1e16 folded in id-ASC order)", fastV)
	}
}

// TestStatsAggregate_ToBoundaryExclusive pins F3: the stats window is half-open
// (from <= bucket_ts < to), so a bucket whose bucket_ts is EXACTLY `to` is
// EXCLUDED. The frontend serializes `to` as exclusive (frontend/src/state/
// filters.ts: "exclusive/now upper bound"), so timeWindow must not add +1 (the
// old bug made a bucket at `to` wrongly included). The default (no `to`) still
// includes the open bucket.
func TestStatsAggregate_ToBoundaryExclusive(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)

	// to == closedHourToday (11:00): that bucket's bucket_ts == to, so half-open
	// [from, to) EXCLUDES it. The 09:00/14:00 closed hours on earlier days are
	// before `to`, so they remain.
	q := "bucket=hourly&group_by=total&metric=calls&to=" + i64(a.closedHourToday)
	code, body, env := getAggregate(t, p, q)
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	got := aggregateToMap(body)
	if _, ok := got[a.closedHourToday]; ok {
		t.Errorf("bucket at exactly `to` (%d) must be EXCLUDED (half-open window): %+v", a.closedHourToday, got)
	}
	if _, ok := got[a.openHour]; ok {
		t.Errorf("open hour (%d) is after `to` and must be excluded: %+v", a.openHour, got)
	}
	// A bucket strictly below `to` (day25's 09:00 hour) is still present.
	day25Hour := atOffset(a.day25, 9, 0)
	day25Bucket := time.UnixMicro(day25Hour).Truncate(time.Hour).UnixMicro()
	if _, ok := got[day25Bucket]; !ok {
		t.Errorf("bucket strictly below `to` must remain: want %d in %+v", day25Bucket, got)
	}

	// Default (no `to`) still includes the open hour (live-folded).
	_, dflt, _ := getAggregate(t, p, "bucket=hourly&group_by=total&metric=calls")
	if _, ok := aggregateToMap(dflt)[a.openHour]; !ok {
		t.Errorf("default to=now must still include the open hour %d: %+v", a.openHour, dflt.Buckets)
	}
}

// TestStatsAggregate_SourceFormatAndTotalShapes covers the two special
// group_by values' key shapes.
func TestStatsAggregate_SourceFormatAndTotalShapes(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)

	// group_by=total → single series entry keyed "".
	_, totalBody, _ := getAggregate(t, p, "bucket=daily&group_by=total&metric=calls")
	tm := aggregateToMap(totalBody)
	for _, keys := range tm {
		if _, ok := keys[""]; !ok || len(keys) != 1 {
			t.Fatalf("group_by=total must key by \"\": %+v", keys)
		}
	}

	// group_by=source_format → keyed by format. Day 25 is aiagent_v3 only.
	_, srcBody, _ := getAggregate(t, p, "bucket=daily&group_by=source_format&metric=calls")
	sm := aggregateToMap(srcBody)
	if _, ok := sm[a.day25]["aiagent_v3"]; !ok {
		t.Errorf("day25 source_format series missing aiagent_v3: %+v", sm[a.day25])
	}
	if _, ok := sm[a.day26]["codex"]; !ok {
		t.Errorf("day26 source_format series missing codex: %+v", sm[a.day26])
	}
}

// TestStatsAggregate_FilteredLiveFold asserts a cross-filter forces the live
// path and applies the filter: agents=worker with group_by=model returns only
// worker's models (gpt-5 on day 26), never nedi's claude-opus.
func TestStatsAggregate_FilteredLiveFold(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	seedRollupFixture(t, db)
	materializeRollups(t, db)

	code, body, env := getAggregate(t, p, "bucket=daily&group_by=model&metric=calls&agents=worker")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	got := aggregateToMap(body)
	for ts, keys := range got {
		if _, ok := keys["claude-opus"]; ok {
			t.Errorf("bucket %d leaked nedi's claude-opus into agents=worker: %+v", ts, keys)
		}
	}
	// gpt-5 (worker's model) must be present.
	var sawGPT bool
	for _, keys := range got {
		if _, ok := keys["gpt-5"]; ok {
			sawGPT = true
		}
	}
	if !sawGPT {
		t.Errorf("agents=worker group_by=model missing gpt-5: %+v", got)
	}
}

// TestStatsAggregate_MidBucketWindowParity pins the windowing contract and its
// parity: a bucket is included iff its bucket_ts ∈ [from, to) — selection is by
// BUCKET START, and within an included bucket the WHOLE bucket's data is
// returned (the whole-bucket rollup semantics; rollups cannot sub-select ops
// inside a bucket without breaking fast↔live parity). The fast path and live
// fold must agree on this even when from/to fall mid-bucket.
//
//   - from = day26 + 6h (mid-day): day26's bucket_ts (00:00) < from → day26 is
//     EXCLUDED, even though its 14:00 ops are after `from`. day25 also excluded.
//   - A from at exactly the day26 boundary includes day26 with its whole cost.
func TestStatsAggregate_MidBucketWindowParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)

	midFrom := a.day26 + 6*hourUS
	to := a.day26 + 18*hourUS
	midQ := "bucket=daily&group_by=total&metric=cost&from=" + i64(midFrom) + "&to=" + i64(to)

	_, fast, env := getAggregate(t, p, midQ)
	if env.Error.Code != "" {
		t.Fatalf("fast env=%+v", env)
	}
	_, live, _ := getAggregate(t, p, midQ+"&sources=aiagent_v3:/p,codex:/p")
	assertAggregateEqual(t, fast, live) // parity holds even mid-bucket.

	mid := aggregateToMap(fast)
	if len(mid) != 0 {
		t.Errorf("mid-bucket from must exclude day25 AND day26 (both bucket_ts < from): %+v", mid)
	}

	// Boundary-aligned from includes day26 with its WHOLE-bucket cost (0.05).
	alignedQ := "bucket=daily&group_by=total&metric=cost&from=" + i64(a.day26) + "&to=" + i64(to)
	_, aligned, _ := getAggregate(t, p, alignedQ)
	got := aggregateToMap(aligned)
	if _, ok := got[a.day25]; ok {
		t.Errorf("day25 must stay excluded (bucket_ts < from): %+v", got)
	}
	if v := got[a.day26][""]; !floatEq(v, 0.05) {
		t.Errorf("day26 total cost = %v, want 0.05 (whole bucket, boundary-aligned from)", v)
	}
}

// TestStatsAggregate_FastPathReadsRollups proves the fast path SERVES the
// materialized rollup table (not a silent live fold): with the rollup tables
// emptied but ops intact, the fast path returns ONLY the live open bucket,
// while the live-fold path (sources filter) still returns every bucket. The
// asymmetry can only happen if the fast path reads rollup_* for closed buckets.
func TestStatsAggregate_FastPathReadsRollups(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	a := seedRollupFixture(t, db)
	materializeRollups(t, db)
	// Wipe the materialized closed-bucket rows; keep ops.
	if _, err := db.Exec(`DELETE FROM rollup_daily`); err != nil {
		t.Fatalf("wipe rollup_daily: %v", err)
	}

	_, fast, _ := getAggregate(t, p, "bucket=daily&group_by=total&metric=calls")
	fm := aggregateToMap(fast)
	if _, ok := fm[a.day25]; ok {
		t.Errorf("fast path returned a closed bucket after rollups wiped: %+v", fm)
	}
	if _, ok := fm[a.openDay]; !ok {
		t.Errorf("fast path lost the live open-day bucket: %+v", fm)
	}

	// The live fold (forced via sources) reconstructs every bucket from ops.
	_, live, _ := getAggregate(t, p, "bucket=daily&group_by=total&metric=calls&sources=aiagent_v3:/p,codex:/p")
	lm := aggregateToMap(live)
	if _, ok := lm[a.day25]; !ok {
		t.Errorf("live fold missing day25 (should rebuild from ops): %+v", lm)
	}
}

// TestStatsAggregate_Empty asserts an empty DB returns 200 with empty buckets.
func TestStatsAggregate_Empty(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body, _ := getAggregate(t, p, "")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if body.Buckets == nil {
		t.Fatal("buckets must serialize as [] not null")
	}
	if len(body.Buckets) != 0 {
		t.Fatalf("buckets = %+v, want empty", body.Buckets)
	}
	if body.Bucket != "daily" || body.Metric != "cost" {
		t.Fatalf("defaults: bucket=%q metric=%q", body.Bucket, body.Metric)
	}
}

// seedSubAgentSession seeds one kind='sub_agent' session (a child of parentID)
// with one turn, so a rollup test can prove the stats endpoints aggregate over
// ALL session kinds — not just roots. Mirrors seedRollupSession but with a
// non-root kind + a parent link.
func seedSubAgentSession(t *testing.T, db *sql.DB, id, parentID, sourceID, agent, provider, cwd string, startTS int64) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO sessions (id, source_id, native_id, parent_session_id, root_session_id, kind,
    agent_name, provider, cwd, status, start_ts, last_activity_ts)
VALUES (?, ?, ?, ?, ?, 'sub_agent', ?, ?, ?, 'completed', ?, ?)`,
		id, sourceID, id, parentID, parentID, agent, provider, cwd, startTS, startTS); err != nil {
		t.Fatalf("seed sub-agent session %s: %v", id, err)
	}
	if _, err := db.Exec(`
INSERT INTO turns (id, session_id, seq, start_ts, status) VALUES (?, ?, 1, ?, 'completed')`,
		id+"-t", id, startTS); err != nil {
		t.Fatalf("seed sub-agent turn for %s: %v", id, err)
	}
}

// TestStatsAggregate_IncludesSubAgentSessions pins F2: the rollup-backed stats
// endpoints aggregate over ALL sessions (root + sub-agent), not just roots. The
// materialized rollups fold every op regardless of session kind, so the live
// fold MUST match — handleStatsAggregate forces group=all so whereClause omits
// the s.kind='root' constraint. Without that, the fast path (all ops) and the
// live fold (root-only under the default group=root) disagree.
//
// Fixture: a root op (claude-opus, $0.10) and a sub-agent op (gpt-5, $0.05) in
// the SAME closed day. Both the fast path and the live fold must report the
// sub-agent's $0.05 — and the day-total must be $0.15.
func TestStatsAggregate_IncludesSubAgentSessions(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day)
	// Root session + op.
	seedRollupSession(t, db, "root1", "aiagent_v3:/p", "nedi", "anthropic", "/w1", atOffset(day, 9, 0))
	seedRollupOp(t, db, rollupOpSpec{id: "rootOp", sess: "root1", seq: 1, kind: "llm", name: "claude-opus",
		model: "claude-opus", provider: "anthropic", start: atOffset(day, 9, 0), dur: 1000,
		status: "completed", tokIn: 100, tokOut: 200, cost: 0.10})
	// Sub-agent session (child of root1) + op, same closed day.
	seedSubAgentSession(t, db, "sub1", "root1", "aiagent_v3:/p", "worker", "openai", "/w1", atOffset(day, 9, 30))
	seedRollupOp(t, db, rollupOpSpec{id: "subOp", sess: "sub1", seq: 1, kind: "llm", name: "gpt-5",
		model: "gpt-5", provider: "openai", start: atOffset(day, 9, 30), dur: 2000,
		status: "completed", tokIn: 50, tokOut: 60, cost: 0.05})
	materializeRollups(t, db)

	// group_by=model so the sub-agent's gpt-5 is a distinct, assertable key.
	const fastQ = "bucket=daily&group_by=model&metric=cost"
	codeFast, fast, env := getAggregate(t, p, fastQ)
	if codeFast != http.StatusOK {
		t.Fatalf("fast path status=%d env=%+v", codeFast, env)
	}
	codeLive, live, env2 := getAggregate(t, p, fastQ+"&sources=aiagent_v3:/p")
	if codeLive != http.StatusOK {
		t.Fatalf("live path status=%d env=%+v", codeLive, env2)
	}
	// The sub-agent op must appear in BOTH paths (this is what F2 guarantees).
	for _, c := range []struct {
		name string
		body aggregateBody
	}{{"fast", fast}, {"live", live}} {
		m := aggregateToMap(c.body)
		if v := m[day]["gpt-5"]; !floatEq(v, 0.05) {
			t.Errorf("%s path: sub-agent gpt-5 cost = %v, want 0.05 (sub-agent ops must be aggregated): %+v", c.name, v, m[day])
		}
		if v := m[day]["claude-opus"]; !floatEq(v, 0.10) {
			t.Errorf("%s path: root claude-opus cost = %v, want 0.10: %+v", c.name, v, m[day])
		}
	}
	// And the two paths must agree exactly.
	assertAggregateEqual(t, fast, live)

	// /api/stats/top must also rank the sub-agent's model.
	codeTop, top, envTop := getTop(t, p, "dimension=model&metric=cost&n=200")
	if codeTop != http.StatusOK {
		t.Fatalf("top status=%d env=%+v", codeTop, envTop)
	}
	tm := topToMap(top)
	if v := tm["gpt-5"]; !floatEq(v, 0.05) {
		t.Errorf("top: sub-agent gpt-5 cost = %v, want 0.05 (sub-agent ops must be ranked): %+v", v, top.Items)
	}
}

// TestStatsAggregate_BadParams covers the enum + method guards.
func TestStatsAggregate_BadParams(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	for _, q := range []string{"bucket=weekly", "group_by=nonsense", "metric=bogus"} {
		code, _, env := getAggregate(t, p, q)
		if code != http.StatusBadRequest || env.Error.Code != CodeBadRequest {
			t.Errorf("%q: status=%d code=%q, want 400/BAD_REQUEST", q, code, env.Error.Code)
		}
	}

	// 405 on POST.
	req := httptest.NewRequest(http.MethodPost, "/api/stats/aggregate", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d, want 405", rr.Code)
	}

	// HEAD ok with empty body.
	headReq := httptest.NewRequest(http.MethodHead, "/api/stats/aggregate", nil)
	headRR := httptest.NewRecorder()
	p.Handler().ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK || headRR.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d bodyLen=%d, want 200/0", headRR.Code, headRR.Body.Len())
	}
}
