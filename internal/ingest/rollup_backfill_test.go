package ingest

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Fixed deterministic clocks. All buckets in the tests are derived from these
// so the closed/open boundary is reproducible regardless of wall time.
//
//   - baseDay is 2026-05-20 00:00:00 UTC (a clean UTC day start).
//   - The "now" used per test is stated inline; helpers below convert a
//     human (day-offset, hour, minute) into a UTC microsecond timestamp.
var baseDay = time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)

// ts builds a UTC-microsecond timestamp at baseDay + dayOffset days, at the
// given hour:minute. Seconds are zero so hour buckets are unambiguous.
func ts(dayOffset, hour, minute int) int64 {
	return baseDay.AddDate(0, 0, dayOffset).
		Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute).
		UnixMicro()
}

// seedSource inserts one sources row with the given canonical id+format.
func seedSource(t *testing.T, db *sql.DB, id, format string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, enabled, parse_errors, created_at)
		 VALUES (?, ?, '/loc', 1, 0, 1)`, id, format); err != nil {
		t.Fatalf("seed source %s: %v", id, err)
	}
}

// seedSession inserts one sessions row (and is the parent for ops via a turn).
// agentName/cwd empty-string => NULL column (mirrors the production writer).
func seedSession(t *testing.T, db *sql.DB, id, sourceID, agentName, cwd string, startTS int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, agent_name, cwd,
                      status, start_ts, last_activity_ts)
VALUES (?, ?, ?, ?, 'root', ?, ?, 'completed', ?, ?)`,
		id, sourceID, id, id, nullIfEmpty(agentName), nullIfEmpty(cwd), startTS, startTS); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// seedTurn inserts the turn parent that ops FK-reference.
func seedTurn(t *testing.T, db *sql.DB, id, sessionID string, startTS int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO turns (id, session_id, seq, start_ts, status) VALUES (?, ?, 1, ?, 'completed')`,
		id, sessionID, startTS); err != nil {
		t.Fatalf("seed turn %s: %v", id, err)
	}
}

// opSpec is the minimal op shape the rollup reader consumes. seq is the
// per-turn sequence; it must be unique within a turn (UNIQUE(turn_id, seq)).
// When 0 it derives from start_ts so multiple ops on the same turn never
// collide (start_ts is distinct per op across all tests here).
type opSpec struct {
	id                                                  string
	turnID, sessionID                                   string
	seq                                                 int
	kind, name, toolNS, model, provider                 string
	startTS                                             int64
	endTS                                               *int64
	durationUS                                          int64
	status                                              string
	tokensIn, tokensOut, tokensCacheRead, tokensCacheWr int64
	costUSD                                             float64
}

// seedOp inserts one ops row honouring the FK chain (source->session->turn).
func seedOp(t *testing.T, db *sql.DB, o opSpec) {
	t.Helper()
	seq := o.seq
	if seq == 0 {
		// Derive a unique-per-turn seq from the (distinct) start microsecond.
		// SQLite seq is INTEGER; the modulo keeps it small without colliding
		// across the few ops any single turn carries in these tests.
		seq = int(o.startTS % 1_000_000_000)
	}
	var endTS any
	if o.endTS != nil {
		endTS = *o.endTS
	}
	var durUS any
	if o.durationUS != 0 {
		durUS = o.durationUS
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, tool_namespace, model, provider,
                 start_ts, end_ts, duration_us, status,
                 tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.id, o.turnID, o.sessionID, seq, o.kind, o.name,
		nullIfEmpty(o.toolNS), nullIfEmpty(o.model), nullIfEmpty(o.provider),
		o.startTS, endTS, durUS, o.status,
		o.tokensIn, o.tokensOut, o.tokensCacheRead, o.tokensCacheWr, o.costUSD); err != nil {
		t.Fatalf("seed op %s: %v", o.id, err)
	}
}

// nullIfEmpty is defined in writer.go and reused here.

// rollupKey identifies one rollup row for assertion lookups.
type rollupKey struct {
	bucketTS       int64
	sourceFormat   string
	dimension      string
	dimensionValue string
}

// rollupVals holds the metric columns for a single rollup row.
type rollupVals struct {
	opCount                                             int64
	tokensIn, tokensOut, tokensCacheRead, tokensCacheWr int64
	costUSD                                             float64
	failures, durationUS, sessionStarts                 int64
}

// readRollups loads an entire rollup_{hourly,daily} table into a keyed map.
func readRollups(t *testing.T, db *sql.DB, table string) map[rollupKey]rollupVals {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT bucket_ts, source_format, dimension, dimension_value,
       op_count, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write,
       cost_usd, failures, duration_us, session_starts
FROM `+table)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[rollupKey]rollupVals)
	for rows.Next() {
		var k rollupKey
		var v rollupVals
		if err := rows.Scan(&k.bucketTS, &k.sourceFormat, &k.dimension, &k.dimensionValue,
			&v.opCount, &v.tokensIn, &v.tokensOut, &v.tokensCacheRead, &v.tokensCacheWr,
			&v.costUSD, &v.failures, &v.durationUS, &v.sessionStarts); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err %s: %v", table, err)
	}
	return out
}

func bucketCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	return int(scanInt(t, db, `SELECT COUNT(*) FROM `+table))
}

// T1: a fully-closed past hour with one llm op + one tool op produces the
// expected total/model/provider/tool/agent/cwd rows and session_starts on
// total/agent/cwd only.
func TestBackfillRollups_BasicCorrectness(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "claude_code:/loc", "claude_code")
	// Session starts at 09:00 so its session_start lands in the SAME hour
	// bucket (09:00) as the ops below — keeping every assertion on one bucket.
	startSess := ts(0, 9, 0) // 2026-05-20 09:00 UTC
	seedSession(t, db, "sess-1", "claude_code:/loc", "claude", "/work/proj", startSess)
	seedTurn(t, db, "turn-1", "sess-1", startSess)

	// llm op at 09:15, closed, 1_000_000us duration, tokens + cost.
	op1End := ts(0, 9, 30)
	seedOp(t, db, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: ts(0, 9, 15), endTS: &op1End, durationUS: 1_000_000, status: "completed",
		tokensIn: 100, tokensOut: 200, tokensCacheRead: 10, tokensCacheWr: 5, costUSD: 0.5,
	})
	// tool op at 09:20, closed, 250_000us duration.
	op2End := ts(0, 9, 25)
	seedOp(t, db, opSpec{
		id: "op-2", turnID: "turn-1", sessionID: "sess-1",
		kind: "tool", name: "Read", toolNS: "fs", provider: "anthropic",
		startTS: ts(0, 9, 20), endTS: &op2End, durationUS: 250_000, status: "completed",
		tokensIn: 1, tokensOut: 2, costUSD: 0.01,
	})

	now := ts(2, 12, 0) // two days later → everything is closed
	stats, err := BackfillRollups(context.Background(), db, now, silentLogger())
	if err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	if stats.HourlyRows == 0 || stats.DailyRows == 0 {
		t.Fatalf("stats report zero rows: %+v", stats)
	}

	h := readRollups(t, db, "rollup_hourly")
	hourBucket := ts(0, 9, 0) // 09:00 UTC bucket

	// total row = SUM of both ops.
	total := h[rollupKey{hourBucket, "claude_code", "total", ""}]
	if total.opCount != 2 || total.tokensIn != 101 || total.tokensOut != 202 ||
		total.tokensCacheRead != 10 || total.tokensCacheWr != 5 ||
		total.durationUS != 1_250_000 || total.failures != 0 || total.sessionStarts != 1 {
		t.Fatalf("total row wrong: %+v", total)
	}
	if total.costUSD < 0.50999 || total.costUSD > 0.51001 {
		t.Fatalf("total cost = %v, want ~0.51", total.costUSD)
	}

	// model row (llm op only).
	model := h[rollupKey{hourBucket, "claude_code", "model", "sonnet"}]
	if model.opCount != 1 || model.tokensIn != 100 || model.durationUS != 1_000_000 {
		t.Fatalf("model row wrong: %+v", model)
	}
	// provider row (both ops carry provider=anthropic).
	prov := h[rollupKey{hourBucket, "claude_code", "provider", "anthropic"}]
	if prov.opCount != 2 {
		t.Fatalf("provider row opCount = %d, want 2", prov.opCount)
	}
	// tool row uses "<ns>.<name>".
	tool := h[rollupKey{hourBucket, "claude_code", "tool", "fs.Read"}]
	if tool.opCount != 1 || tool.durationUS != 250_000 {
		t.Fatalf("tool row wrong: %+v", tool)
	}
	// agent + cwd rows carry both ops AND session_starts=1.
	agent := h[rollupKey{hourBucket, "claude_code", "agent", "claude"}]
	if agent.opCount != 2 || agent.sessionStarts != 1 {
		t.Fatalf("agent row wrong: %+v", agent)
	}
	cwd := h[rollupKey{hourBucket, "claude_code", "cwd", "/work/proj"}]
	if cwd.opCount != 2 || cwd.sessionStarts != 1 {
		t.Fatalf("cwd row wrong: %+v", cwd)
	}

	// session_starts must NOT appear on model/provider/tool rows.
	if model.sessionStarts != 0 || prov.sessionStarts != 0 || tool.sessionStarts != 0 {
		t.Fatalf("session_starts leaked onto model/provider/tool: m=%d p=%d t=%d",
			model.sessionStarts, prov.sessionStarts, tool.sessionStarts)
	}
}

// T2: an op in the open hour is absent from rollup_hourly; an earlier closed
// hour the same day is present.
func TestBackfillRollups_OpenHourExclusion(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "codex:/loc", "codex")
	seedSession(t, db, "sess-1", "codex:/loc", "agent", "/w", ts(0, 1, 0))
	seedTurn(t, db, "turn-1", "sess-1", ts(0, 1, 0))

	// now = 2026-05-20 14:30 → open hour bucket is 14:00.
	now := ts(0, 14, 30)

	// closed op at 09:05 (bucket 09:00).
	cEnd := ts(0, 9, 10)
	seedOp(t, db, opSpec{id: "op-closed", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "c", model: "m", provider: "p",
		startTS: ts(0, 9, 5), endTS: &cEnd, durationUS: 1, status: "completed"})
	// open-hour op at 14:10 (bucket 14:00 == openHourStart) → excluded.
	oEnd := ts(0, 14, 20)
	seedOp(t, db, opSpec{id: "op-open", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "o", model: "m", provider: "p",
		startTS: ts(0, 14, 10), endTS: &oEnd, durationUS: 1, status: "completed"})

	if _, err := BackfillRollups(context.Background(), db, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	h := readRollups(t, db, "rollup_hourly")
	if _, ok := h[rollupKey{ts(0, 9, 0), "codex", "total", ""}]; !ok {
		t.Fatal("closed-hour 09:00 total row missing")
	}
	if _, ok := h[rollupKey{ts(0, 14, 0), "codex", "total", ""}]; ok {
		t.Fatal("open-hour 14:00 total row present — open hour must be excluded")
	}
}

// T3: with now = day2 14:30, an op at 09:00 the same day is in rollup_hourly
// but the open day has NO rollup_daily row; an op the previous day is in BOTH.
func TestBackfillRollups_OpenDayVsClosedHour(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "claude_code:/loc", "claude_code")
	seedSession(t, db, "sess-1", "claude_code:/loc", "claude", "/w", ts(0, 0, 0))
	seedTurn(t, db, "turn-1", "sess-1", ts(0, 0, 0))

	now := ts(2, 14, 30) // open day = day2 00:00, open hour = day2 14:00

	// op on day1 (fully closed day) at 10:00.
	d1End := ts(1, 10, 5)
	seedOp(t, db, opSpec{id: "op-d1", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "a", model: "m", provider: "p",
		startTS: ts(1, 10, 0), endTS: &d1End, durationUS: 1, status: "completed"})
	// op on day2 (open day) at 09:00 — a CLOSED hour of the OPEN day.
	d2End := ts(2, 9, 5)
	seedOp(t, db, opSpec{id: "op-d2", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "b", model: "m", provider: "p",
		startTS: ts(2, 9, 0), endTS: &d2End, durationUS: 1, status: "completed"})

	if _, err := BackfillRollups(context.Background(), db, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	h := readRollups(t, db, "rollup_hourly")
	d := readRollups(t, db, "rollup_daily")

	// day2 09:00 closed hour present in hourly.
	if _, ok := h[rollupKey{ts(2, 9, 0), "claude_code", "total", ""}]; !ok {
		t.Fatal("day2 09:00 closed-hour row missing from rollup_hourly")
	}
	// day1 present in BOTH hourly and daily.
	if _, ok := h[rollupKey{ts(1, 10, 0), "claude_code", "total", ""}]; !ok {
		t.Fatal("day1 10:00 missing from rollup_hourly")
	}
	if _, ok := d[rollupKey{ts(1, 0, 0), "claude_code", "total", ""}]; !ok {
		t.Fatal("day1 daily total row missing from rollup_daily")
	}
	// day2 (open day) has NO daily row.
	if _, ok := d[rollupKey{ts(2, 0, 0), "claude_code", "total", ""}]; ok {
		t.Fatal("open day2 daily row present — open day must be excluded from rollup_daily")
	}
}

// T4: running BackfillRollups twice with the same now yields byte-identical
// table contents.
func TestBackfillRollups_Idempotent(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "claude_code:/loc", "claude_code")
	seedSession(t, db, "sess-1", "claude_code:/loc", "claude", "/w", ts(0, 8, 0))
	seedTurn(t, db, "turn-1", "sess-1", ts(0, 8, 0))
	for i, hm := range [][2]int{{9, 0}, {9, 30}, {10, 0}} {
		end := ts(0, hm[0], hm[1]+1)
		seedOp(t, db, opSpec{
			id: "op-" + string(rune('a'+i)), turnID: "turn-1", sessionID: "sess-1",
			kind: "llm", name: "c", model: "m", provider: "p",
			startTS: ts(0, hm[0], hm[1]), endTS: &end, durationUS: 100, status: "completed",
			tokensIn: 7, costUSD: 0.1,
		})
	}
	now := ts(2, 0, 0)

	if _, err := BackfillRollups(context.Background(), db, now, silentLogger()); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	h1 := readRollups(t, db, "rollup_hourly")
	d1 := readRollups(t, db, "rollup_daily")

	if _, err := BackfillRollups(context.Background(), db, now, silentLogger()); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	h2 := readRollups(t, db, "rollup_hourly")
	d2 := readRollups(t, db, "rollup_daily")

	if len(h1) != len(h2) || len(d1) != len(d2) {
		t.Fatalf("row counts changed across runs: hourly %d->%d daily %d->%d",
			len(h1), len(h2), len(d1), len(d2))
	}
	for k, v := range h1 {
		if h2[k] != v {
			t.Fatalf("hourly row %+v changed: %+v -> %+v", k, v, h2[k])
		}
	}
	for k, v := range d1 {
		if d2[k] != v {
			t.Fatalf("daily row %+v changed: %+v -> %+v", k, v, d2[k])
		}
	}
}

// T5: two sources produce rows split by source_format.
func TestBackfillRollups_MultiSourceSplit(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "claude_code:/loc", "claude_code")
	seedSource(t, db, "codex:/loc", "codex")
	seedSession(t, db, "sess-cc", "claude_code:/loc", "claude", "/w", ts(0, 8, 0))
	seedSession(t, db, "sess-cx", "codex:/loc", "codex-agent", "/w", ts(0, 8, 0))
	seedTurn(t, db, "turn-cc", "sess-cc", ts(0, 8, 0))
	seedTurn(t, db, "turn-cx", "sess-cx", ts(0, 8, 0))

	ccEnd := ts(0, 9, 5)
	seedOp(t, db, opSpec{id: "op-cc", turnID: "turn-cc", sessionID: "sess-cc",
		kind: "llm", name: "a", model: "m", provider: "p",
		startTS: ts(0, 9, 0), endTS: &ccEnd, durationUS: 1, status: "completed", tokensIn: 5})
	cxEnd := ts(0, 9, 5)
	seedOp(t, db, opSpec{id: "op-cx", turnID: "turn-cx", sessionID: "sess-cx",
		kind: "llm", name: "b", model: "m", provider: "p",
		startTS: ts(0, 9, 0), endTS: &cxEnd, durationUS: 1, status: "completed", tokensIn: 9})

	if _, err := BackfillRollups(context.Background(), db, ts(2, 0, 0), silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	h := readRollups(t, db, "rollup_hourly")
	cc := h[rollupKey{ts(0, 9, 0), "claude_code", "total", ""}]
	cx := h[rollupKey{ts(0, 9, 0), "codex", "total", ""}]
	if cc.tokensIn != 5 || cx.tokensIn != 9 {
		t.Fatalf("source split wrong: claude_code tokensIn=%d (want 5), codex tokensIn=%d (want 9)",
			cc.tokensIn, cx.tokensIn)
	}
}

// T6: a failed op increments failures; a completed op does not.
func TestBackfillRollups_FailedStatus(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "codex:/loc", "codex")
	seedSession(t, db, "sess-1", "codex:/loc", "a", "/w", ts(0, 8, 0))
	seedTurn(t, db, "turn-1", "sess-1", ts(0, 8, 0))

	fEnd := ts(0, 9, 5)
	seedOp(t, db, opSpec{id: "op-fail", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "a", model: "m", provider: "p",
		startTS: ts(0, 9, 0), endTS: &fEnd, durationUS: 1, status: "failed"})
	cEnd := ts(0, 9, 10)
	seedOp(t, db, opSpec{id: "op-ok", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "b", model: "m", provider: "p",
		startTS: ts(0, 9, 8), endTS: &cEnd, durationUS: 1, status: "completed"})

	if _, err := BackfillRollups(context.Background(), db, ts(2, 0, 0), silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	total := readRollups(t, db, "rollup_hourly")[rollupKey{ts(0, 9, 0), "codex", "total", ""}]
	if total.opCount != 2 || total.failures != 1 {
		t.Fatalf("failed-status wrong: opCount=%d failures=%d (want 2,1)", total.opCount, total.failures)
	}
}

// T7: a running op (end_ts NULL) in a closed hour counts in op_count but adds
// 0 to duration_us.
func TestBackfillRollups_RunningOp(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "codex:/loc", "codex")
	seedSession(t, db, "sess-1", "codex:/loc", "a", "/w", ts(0, 8, 0))
	seedTurn(t, db, "turn-1", "sess-1", ts(0, 8, 0))

	// running op: end_ts NULL, duration_us NULL.
	seedOp(t, db, opSpec{id: "op-run", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "a", model: "m", provider: "p",
		startTS: ts(0, 9, 0), endTS: nil, durationUS: 0, status: "running"})
	// closed op same hour with real duration.
	cEnd := ts(0, 9, 30)
	seedOp(t, db, opSpec{id: "op-done", turnID: "turn-1", sessionID: "sess-1",
		kind: "llm", name: "b", model: "m", provider: "p",
		startTS: ts(0, 9, 20), endTS: &cEnd, durationUS: 600_000, status: "completed"})

	if _, err := BackfillRollups(context.Background(), db, ts(2, 0, 0), silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}
	total := readRollups(t, db, "rollup_hourly")[rollupKey{ts(0, 9, 0), "codex", "total", ""}]
	if total.opCount != 2 {
		t.Fatalf("running op not counted: opCount=%d, want 2", total.opCount)
	}
	if total.durationUS != 600_000 {
		t.Fatalf("running op leaked duration: durationUS=%d, want 600000", total.durationUS)
	}
}

// T8: a session whose start is in a closed day with ZERO ops still yields
// session_starts rows (total/agent/cwd) and a daily row.
func TestBackfillRollups_SessionStartOnlyDay(t *testing.T) {
	_, db := openTestStore(t)
	seedSource(t, db, "claude_code:/loc", "claude_code")
	// session starts on day1 (a fully-closed day) but has no ops at all.
	seedSession(t, db, "sess-empty", "claude_code:/loc", "claude", "/work/proj", ts(1, 11, 0))
	// (no turn, no ops)

	now := ts(3, 0, 0) // day1 is closed
	if _, err := BackfillRollups(context.Background(), db, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	h := readRollups(t, db, "rollup_hourly")
	hourBucket := ts(1, 11, 0)
	total := h[rollupKey{hourBucket, "claude_code", "total", ""}]
	if total.sessionStarts != 1 || total.opCount != 0 {
		t.Fatalf("hourly session-start total wrong: %+v (want sessionStarts=1, opCount=0)", total)
	}
	if a := h[rollupKey{hourBucket, "claude_code", "agent", "claude"}]; a.sessionStarts != 1 {
		t.Fatalf("hourly agent session_starts = %d, want 1", a.sessionStarts)
	}
	if c := h[rollupKey{hourBucket, "claude_code", "cwd", "/work/proj"}]; c.sessionStarts != 1 {
		t.Fatalf("hourly cwd session_starts = %d, want 1", c.sessionStarts)
	}
	// Closed day → daily row with session_starts=1.
	d := readRollups(t, db, "rollup_daily")
	if dt := d[rollupKey{ts(1, 0, 0), "claude_code", "total", ""}]; dt.sessionStarts != 1 {
		t.Fatalf("daily session-start total = %d, want 1", dt.sessionStarts)
	}
}

// T9: empty DB → no error, zero rows.
func TestBackfillRollups_EmptyDB(t *testing.T) {
	_, db := openTestStore(t)
	stats, err := BackfillRollups(context.Background(), db, ts(2, 0, 0), silentLogger())
	if err != nil {
		t.Fatalf("BackfillRollups(empty): %v", err)
	}
	if stats.HourlyRows != 0 || stats.DailyRows != 0 || stats.DaysProcessed != 0 {
		t.Fatalf("empty DB produced non-zero stats: %+v", stats)
	}
	if n := bucketCount(t, db, "rollup_hourly"); n != 0 {
		t.Fatalf("rollup_hourly has %d rows, want 0", n)
	}
	if n := bucketCount(t, db, "rollup_daily"); n != 0 {
		t.Fatalf("rollup_daily has %d rows, want 0", n)
	}
}
