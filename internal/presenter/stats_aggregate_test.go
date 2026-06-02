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
	"github.com/netdata/ai-viewer/internal/rollups"
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
	metrics := []string{"cost", "tokens_in", "tokens_out", "calls", "failures", "duration_us", "sessions"}
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

// TestStatsAggregate_SingleClockReadAcrossBoundary pins the single-clock-read
// contract of aggregateSeries: the window (`to`) and the open-bucket cutoff
// (`openStart`) must derive from ONE p.now() reading, so a clock that crosses an
// hour boundary between two reads cannot desync them and drop the open bucket's
// live fold (closedHi = min(to, openStart)).
//
// We inject a COUNTER clock that returns DIFFERENT values on consecutive calls:
//   - read #1 (timeWindow): one µs BEFORE an hour boundary → to = boundary.
//   - read #2 (BucketTS):   just AFTER the boundary → BucketTS = boundary (next
//     hour). The op is seeded in the hour the FIRST read falls in (the hour
//     ending at `boundary`), so a correct single-read fold MUST include it.
//
// With the (buggy) two-read code, openStart comes from read #2 (== boundary)
// while `to` comes from read #1 (== boundary); the open-bucket guard
// `openStart >= from && openStart < to` becomes `boundary < boundary` → false,
// so the bucket is dropped and the op vanishes. The single-read fix makes read
// #2 irrelevant: both values come from read #1 and the op is always present.
func TestStatsAggregate_SingleClockReadAcrossBoundary(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	// An hour boundary; read #1 is 1µs before it, read #2 is just after it.
	boundary := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro()
	openHour := rollups.BucketTS(boundary-1, rollups.Hourly) // 11:00 — the hour read #1 lands in.

	// One op inside that open hour. Live-folded regardless of fast/live path.
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", openHour)
	seedRollupSession(t, db, "sboundary", "aiagent_v3:/p", "nedi", "anthropic", "/w1", openHour)
	seedRollupOp(t, db, rollupOpSpec{id: "oboundary", sess: "sboundary", seq: 1, kind: "llm",
		name: "claude-opus", model: "claude-opus", provider: "anthropic",
		start: openHour, dur: 0, status: "running", tokIn: 1, tokOut: 0, cost: 0.001})

	// Counter clock: 1st call = boundary-1µs (before the boundary, in the open
	// hour that holds the op); every LATER call = boundary+5µs (after, in the
	// NEXT hour). The later value is "poisoned": if aggregateSeries consulted the
	// clock a second time for openStart, openStart would be the next hour and the
	// open-bucket guard would drop the op. The single-read fix never consults it,
	// so the result is computed entirely from the first reading.
	var calls int
	p.nowFn = func() time.Time {
		calls++
		if calls == 1 {
			return time.UnixMicro(boundary - 1).UTC()
		}
		return time.UnixMicro(boundary + 5).UTC()
	}

	// Call aggregateSeries directly so ONLY its now() read(s) are exercised
	// (the HTTP path's parseSessionFilter now() read is outside this function).
	f := sessionFilter{group: groupRoot, sort: sortStartTS, order: "desc", limit: defaultLimit}
	f.forceAllSessions()
	series, err := p.aggregateSeries(context.Background(), f, rollups.Hourly,
		rollupDimension{dimension: "total"}, smCalls)
	if err != nil {
		t.Fatalf("aggregateSeries: %v", err)
	}
	if calls < 1 {
		t.Fatal("aggregateSeries never read the clock")
	}

	// The open hour (where read #1 lands) MUST be present with the one op. With
	// the two-read bug the boundary crossing drops this bucket entirely.
	bkt, ok := series[openHour]
	if !ok {
		t.Fatalf("open hour %d dropped — window/open-bucket desynced across the boundary (series=%+v)", openHour, series)
	}
	if v := bkt[""]; !floatEq(v, 1) {
		t.Errorf("open-hour calls = %v, want 1 (single clock read keeps the open bucket): %+v", v, bkt)
	}
	// And nothing must land in the next hour (read #2's bucket) — the data is
	// in read #1's hour, proving the fold tracks the single (first) reading.
	if _, ok := series[boundary]; ok {
		t.Errorf("bucket at the next hour %d must be empty (no op there): %+v", boundary, series)
	}
}

// TestStatsAggregate_LiveFoldOpWindowIgnoresSessionStart pins the P1 contract
// (rest-api.md §"Rollup fast path vs. live fold"): the live fold of OPS is bound
// ONLY by o.start_ts ∈ [from, to) — NEVER by the session's start_ts. The
// materialized rollup buckets every op by op.start_ts regardless of when its
// session started, so an op whose SESSION started before `from` but whose own
// start_ts is in-window MUST still be counted, or the live fold diverges from
// the rollup fast path (the AC#2 parity invariant).
//
// Fixture: a session that STARTED on day 24 (before `from`), carrying a closed
// op whose start_ts is on day 25 (inside the window). With the bug, loadFoldOps
// builds its session set via whereClause, which adds s.start_ts >= from and
// drops the day-24 session entirely — so the day-25 op vanishes from the live
// fold while the rollup still counts it. The fix makes the OPS session set apply
// only the session DIMENSION filters (not the s.start_ts window), so the op is
// counted on both paths and they agree.
func TestStatsAggregate_LiveFoldOpWindowIgnoresSessionStart(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day24 := time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC).UnixMicro()
	day25 := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	// Session starts on day 24; its closed op starts on day 25.
	sessStart := atOffset(day24, 8, 0)
	opStart := atOffset(day25, 9, 0)
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day24)
	seedRollupSession(t, db, "preWin", "aiagent_v3:/p", "nedi", "anthropic", "/w1", sessStart)
	seedRollupOp(t, db, rollupOpSpec{id: "preWinOp", sess: "preWin", seq: 1, kind: "llm",
		name: "claude-opus", model: "claude-opus", provider: "anthropic",
		start: opStart, dur: 1000, status: "completed", tokIn: 100, tokOut: 200, cost: 0.10})
	materializeRollups(t, db)

	// from = day 25 (AFTER the session start, AT/BEFORE the op start). The op's
	// bucket (day 25) is a CLOSED daily bucket relative to fixedTime (the 27th).
	from := i64(day25)

	// Fast path: time-only filter. The rollup folds the op by op.start_ts → day 25.
	fastQ := "bucket=daily&group_by=total&metric=calls&from=" + from
	codeFast, fast, env := getAggregate(t, p, fastQ)
	if codeFast != http.StatusOK {
		t.Fatalf("fast path status=%d env=%+v", codeFast, env)
	}
	fm := aggregateToMap(fast)
	if v := fm[day25][""]; !floatEq(v, 1) {
		t.Fatalf("fast path day25 calls = %v, want 1 (rollup counts the op by op.start_ts): %+v", v, fm)
	}

	// Forced live fold (sources filter naming the seeded source). With the bug
	// this DROPS the op (its day-24 session fails s.start_ts >= from), so the
	// day-25 bucket is empty and the assertion below FAILS. The fix keeps it.
	liveQ := fastQ + "&sources=aiagent_v3:/p"
	codeLive, live, env2 := getAggregate(t, p, liveQ)
	if codeLive != http.StatusOK {
		t.Fatalf("live path status=%d env=%+v", codeLive, env2)
	}
	lm := aggregateToMap(live)
	if v := lm[day25][""]; !floatEq(v, 1) {
		t.Fatalf("live fold day25 calls = %v, want 1 — op DROPPED because its session "+
			"started before `from` (live fold must bound by op.start_ts, NOT session.start_ts): %+v", v, lm)
	}
	// AC#2 parity: the two paths must agree exactly over the same data.
	assertAggregateEqual(t, fast, live)
}

// TestStatsAggregate_LiveFoldOpenBucketIgnoresSessionStart is the open-bucket
// twin of the test above: the open-bucket live fold (taken on EVERY query, fast
// path or not) must also bound ops only by o.start_ts, never by the session's
// start. A session started in a CLOSED hour carries an op in the OPEN hour; both
// the default fast path and a forced live fold must count it in the open bucket.
func TestStatsAggregate_LiveFoldOpenBucketIgnoresSessionStart(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	// fixedTime = 2026-05-27 12:00:00 UTC → open hour starts at 12:00.
	openHour := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro()
	closedHour := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC).UnixMicro()
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", closedHour)
	// Session starts in the closed 10:00 hour; its op is in the open 12:00 hour.
	seedRollupSession(t, db, "openWin", "aiagent_v3:/p", "nedi", "anthropic", "/w1", closedHour)
	seedRollupOp(t, db, rollupOpSpec{id: "openWinOp", sess: "openWin", seq: 1, kind: "llm",
		name: "claude-opus", model: "claude-opus", provider: "anthropic",
		start: openHour, dur: 0, status: "running", tokIn: 9, tokOut: 0, cost: 0.009})
	materializeRollups(t, db)

	// from = open hour: only the open bucket is in-window. The open-bucket fold
	// runs on both the fast path and the forced live fold.
	from := i64(openHour)
	fastQ := "bucket=hourly&group_by=total&metric=calls&from=" + from
	_, fast, _ := getAggregate(t, p, fastQ)
	if v := aggregateToMap(fast)[openHour][""]; !floatEq(v, 1) {
		t.Fatalf("fast path open-hour calls = %v, want 1: %+v", v, aggregateToMap(fast))
	}

	// Forced live fold: with the bug the open-bucket fold's session set applies
	// s.start_ts >= from (== openHour), dropping the 10:00-started session, so the
	// open-hour op vanishes. The fix counts it.
	_, live, _ := getAggregate(t, p, fastQ+"&sources=aiagent_v3:/p")
	if v := aggregateToMap(live)[openHour][""]; !floatEq(v, 1) {
		t.Fatalf("live fold open-hour calls = %v, want 1 — open-bucket op DROPPED because its "+
			"session started before `from` (open-bucket fold must bound by op.start_ts): %+v", v, aggregateToMap(live))
	}
	assertAggregateEqual(t, fast, live)
}

// TestStatsAggregate_SessionsMetric pins the P2 contract (rest-api.md §GET
// /api/stats/aggregate — the `sessions` metric): metric=sessions reads the
// additive session_starts column, attributed to each session's start bucket by
// session.start_ts (NOT op.start_ts). Both the closed-bucket fast path (which
// SUMs the stored session_starts) and the open-bucket live fold (which loads the
// window's session starts and folds them via rollups.Rollup's `starts` input)
// must agree and match a hand count — and the value must be the session COUNT,
// not op_count.
//
// Fixture (group_by=total, daily): two sessions start on day 25 (with 1 op each),
// one session starts on day 26, and two sessions start in the OPEN day (the 27th)
// — one in the closed 11:00 hour, one in the open 12:00 hour. Expected per-day
// session_starts: day25=2, day26=1, openDay=2.
func TestStatsAggregate_SessionsMetric(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day25 := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	day26 := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC).UnixMicro()
	openDay := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC).UnixMicro()
	closedHourToday := time.Date(2026, 5, 27, 11, 0, 0, 0, time.UTC).UnixMicro()
	openHour := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro()
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day25)

	// Day 25: two sessions (each with a closed op so they also have op rows).
	for i, ss := range []int64{atOffset(day25, 9, 0), atOffset(day25, 15, 0)} {
		id := "d25-" + i64(int64(i))
		seedRollupSession(t, db, id, "aiagent_v3:/p", "nedi", "anthropic", "/w1", ss)
		seedRollupOp(t, db, rollupOpSpec{id: id + "-op", sess: id, seq: 1, kind: "llm",
			name: "claude-opus", model: "claude-opus", provider: "anthropic",
			start: ss, dur: 100, status: "completed", cost: 0.01})
	}
	// Day 26: one session.
	seedRollupSession(t, db, "d26", "aiagent_v3:/p", "worker", "openai", "/w2", atOffset(day26, 14, 0))
	seedRollupOp(t, db, rollupOpSpec{id: "d26-op", sess: "d26", seq: 1, kind: "llm",
		name: "gpt-5", model: "gpt-5", provider: "openai", start: atOffset(day26, 14, 0),
		dur: 200, status: "completed", cost: 0.02})
	// Open day: one session in the closed 11:00 hour, one in the open 12:00 hour.
	seedRollupSession(t, db, "d27-closed", "aiagent_v3:/p", "nedi", "anthropic", "/w1", closedHourToday)
	seedRollupSession(t, db, "d27-open", "aiagent_v3:/p", "nedi", "anthropic", "/w1", openHour)
	materializeRollups(t, db)

	// Daily group_by=total: per-day session-start counts.
	code, body, env := getAggregate(t, p, "bucket=daily&group_by=total&metric=sessions")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if body.Metric != "sessions" {
		t.Fatalf("echoed metric = %q, want \"sessions\"", body.Metric)
	}
	got := aggregateToMap(body)
	for _, c := range []struct {
		day  int64
		want float64
		name string
	}{
		{day25, 2, "day25"},
		{day26, 1, "day26"},
		{openDay, 2, "openDay (1 closed-hour + 1 open-hour session start, live-folded)"},
	} {
		if v := got[c.day][""]; !floatEq(v, c.want) {
			t.Errorf("%s session_starts = %v, want %v: %+v", c.name, v, c.want, got)
		}
	}

	// Parity: the closed-bucket fast path and the forced live fold must agree on
	// EVERY bucket's session_starts (the live fold loads session starts by
	// s.start_ts and folds them through rollups.Rollup's `starts` input).
	_, live, env2 := getAggregate(t, p, "bucket=daily&group_by=total&metric=sessions&sources=aiagent_v3:/p")
	if env2.Error.Code != "" {
		t.Fatalf("live env=%+v", env2)
	}
	assertAggregateEqual(t, body, live)

	// sessions != calls: day25 has 2 sessions but also 2 ops; day26 has 1 session
	// and 1 op. Prove the value is session_starts, not op_count, where they differ
	// — use the open day: 2 session starts but only ZERO closed ops (both sessions'
	// only "activity" is their start; neither has an op), and 1 op total? No — make
	// the distinction explicit via group_by=agent below instead.
	//
	// Concretely here: openDay sessions=2 while openDay calls=0 (neither open-day
	// session carries an op). That alone proves sessions reads session_starts.
	_, callsBody, _ := getAggregate(t, p, "bucket=daily&group_by=total&metric=calls")
	if v := aggregateToMap(callsBody)[openDay][""]; !floatEq(v, 0) {
		t.Fatalf("openDay calls = %v, want 0 (no ops on the open day) — fixture invariant", v)
	}
	if v := got[openDay][""]; !floatEq(v, 2) {
		t.Fatalf("openDay sessions = %v, want 2 — sessions metric must read session_starts, not op_count", v)
	}
}

// TestStatsAggregate_SessionsMetricDimensions pins the dimension behavior of the
// sessions metric (rest-api.md §GET /api/stats/aggregate): session_starts is
// meaningful for group_by ∈ {total, agent, cwd} and is 0 for {model, provider,
// tool} — exactly as the rollup stores it (the fold never attributes
// session_starts to model/provider/tool rows). Returning the stored 0 for those
// dims is correct, NOT a rejection.
func TestStatsAggregate_SessionsMetricDimensions(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day25 := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	openDay := time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC).UnixMicro()
	openHour := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro()
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day25)
	// Closed-day session under agent "nedi", cwd "/w1".
	seedRollupSession(t, db, "agA", "aiagent_v3:/p", "nedi", "anthropic", "/w1", atOffset(day25, 9, 0))
	seedRollupOp(t, db, rollupOpSpec{id: "agA-op", sess: "agA", seq: 1, kind: "llm",
		name: "claude-opus", model: "claude-opus", provider: "anthropic",
		start: atOffset(day25, 9, 0), dur: 100, status: "completed", cost: 0.01})
	// Open-hour session under agent "worker", cwd "/w2" (exercises the live fold).
	seedRollupSession(t, db, "agB", "aiagent_v3:/p", "worker", "openai", "/w2", openHour)
	materializeRollups(t, db)

	// group_by=agent: session_starts attributed per agent (closed + open day).
	_, agentBody, _ := getAggregate(t, p, "bucket=daily&group_by=agent&metric=sessions")
	am := aggregateToMap(agentBody)
	if v := am[day25]["nedi"]; !floatEq(v, 1) {
		t.Errorf("day25 agent=nedi session_starts = %v, want 1: %+v", v, am[day25])
	}
	if v := am[openDay]["worker"]; !floatEq(v, 1) {
		t.Errorf("openDay agent=worker session_starts = %v, want 1 (live-folded): %+v", v, am[openDay])
	}

	// group_by=cwd: same attribution by cwd.
	_, cwdBody, _ := getAggregate(t, p, "bucket=daily&group_by=cwd&metric=sessions")
	cm := aggregateToMap(cwdBody)
	if v := cm[day25]["/w1"]; !floatEq(v, 1) {
		t.Errorf("day25 cwd=/w1 session_starts = %v, want 1: %+v", v, cm[day25])
	}

	// group_by ∈ {model, provider, tool}: 0 (session_starts is never attributed
	// there). The fast path SUMs a stored 0; the live fold folds 0 — both yield
	// EMPTY series for the sessions metric (no key carries a non-zero value).
	for _, gb := range []string{"model", "provider", "tool"} {
		_, b, env := getAggregate(t, p, "bucket=daily&group_by="+gb+"&metric=sessions")
		if env.Error.Code != "" {
			t.Fatalf("group_by=%s metric=sessions must NOT be rejected, env=%+v", gb, env)
		}
		for ts, keys := range aggregateToMap(b) {
			for k, v := range keys {
				if v != 0 {
					t.Errorf("group_by=%s metric=sessions: bucket %d key %q = %v, want 0 (session_starts not attributed to %s)", gb, ts, k, v, gb)
				}
			}
		}
	}
}

// TestStatsTop_SessionsMetric asserts /api/stats/top also serves the sessions
// metric: ranking agents by their session_starts over the window (closed + open).
func TestStatsTop_SessionsMetric(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	day25 := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC).UnixMicro()
	openHour := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC).UnixMicro()
	seedSource(t, db, "aiagent_v3:/p", "aiagent_v3", "/p", day25)
	// nedi: 2 session starts (one closed day, one open hour). worker: 1.
	seedRollupSession(t, db, "n1", "aiagent_v3:/p", "nedi", "anthropic", "/w1", atOffset(day25, 9, 0))
	seedRollupSession(t, db, "n2", "aiagent_v3:/p", "nedi", "anthropic", "/w1", openHour)
	seedRollupSession(t, db, "w1", "aiagent_v3:/p", "worker", "openai", "/w2", atOffset(day25, 10, 0))
	materializeRollups(t, db)

	code, body, env := getTop(t, p, "dimension=agent&metric=sessions&n=200")
	if code != http.StatusOK {
		t.Fatalf("status=%d env=%+v", code, env)
	}
	if body.Metric != "sessions" {
		t.Fatalf("echoed metric = %q, want \"sessions\"", body.Metric)
	}
	tm := topToMap(body)
	if v := tm["nedi"]; !floatEq(v, 2) {
		t.Errorf("top agent nedi session_starts = %v, want 2: %+v", v, body.Items)
	}
	if v := tm["worker"]; !floatEq(v, 1) {
		t.Errorf("top agent worker session_starts = %v, want 1: %+v", v, body.Items)
	}
	// nedi (2) ranks above worker (1).
	if len(body.Items) >= 2 && body.Items[0].Key != "nedi" {
		t.Errorf("ranking: top item = %q, want nedi (2 > 1): %+v", body.Items[0].Key, body.Items)
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
