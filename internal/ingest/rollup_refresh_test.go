package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// fixedNow is the deterministic wall-clock cutoff shared by the incremental
// refresh and the parity BackfillRollups call so both apply the SAME
// open-bucket boundary. It is two days after baseDay (catalog_test.go's ts
// helper), so every bucket the tests touch on day0/day1 is closed.
func fixedNow() int64 { return ts(2, 12, 0) }

// flushBatch runs one worker.flush over batch against db with a FIXED clock,
// so the incremental rollup-refresh hook materializes closed buckets with a
// reproducible cutoff. It mirrors the production flush path (apply loop →
// refreshRollups → refreshAggregates → progress → notify → commit) exactly.
func flushBatch(t *testing.T, db *sql.DB, sourceID, format string, now int64, batch []canonical.Event) {
	t.Helper()
	ctx := context.Background()
	wr := newWriter(sourceID, format, "/loc", NopPricer{})
	wr.now = func() int64 { return now }
	w := &worker{
		sourceID:     sourceID,
		sourceFormat: format,
		location:     "/loc",
		db:           db,
		hwm:          newHWMCache(),
		pricer:       NopPricer{},
		logger:       silentLogger(),
		batchSize:    defaultBatchSize,
		batchEvery:   defaultBatchInterval,
	}
	if err := w.flush(ctx, wr, batch); err != nil {
		t.Fatalf("worker.flush: %v", err)
	}
	wr.resetBatch()
}

// llmOpEvents returns the OpStarted+OpFinalized pair for one closed llm op on
// (session, turn). Tokens/cost/duration are set so the rollup metrics are
// non-trivial. The finalize Ts is the END (spec-conformant: a finalize sorts
// after its start), and EndTs drives duration = EndTs-start_ts.
func llmOpEvents(src, sess string, turnSeq, seq int, start, end int64, model, provider string, tokensIn, tokensOut int64, cost float64, failed bool) []canonical.Event {
	status := "completed"
	if failed {
		status = "failed"
	}
	return []canonical.Event{
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: uint64(seq*2 - 1), Ts: start},
			SessionNativeID: sess, TurnSeq: turnSeq, Seq: seq, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Model: model, Provider: provider,
		},
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: uint64(seq * 2), Ts: end},
			SessionNativeID: sess, TurnSeq: turnSeq, Seq: seq, Status: status, EndTs: end,
			TokensIn: tokensIn, TokensOut: tokensOut, CostUSD: cost,
		},
	}
}

// sessionStartEvent returns one SessionStarted for sess at startTS with the
// given agent/cwd, so the session_starts metric attributes to its bucket.
func sessionStartEvent(src, sess, agent, cwd string, startTS int64, seq uint64) canonical.Event {
	return canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: seq, Ts: startTS},
		NativeID:     sess,
		RootNativeID: sess,
		Kind:         canonical.KindRoot,
		AgentName:    agent,
		Cwd:          cwd,
	}
}

// TestRefreshRollups_ParityWithBackfill is the core test: events driven through
// the incremental hook must produce byte-identical rollup_hourly + rollup_daily
// to BackfillRollups over the SAME ops/sessions and the SAME now. It includes
// EQUAL-start_ts ops (different ids) to exercise the (start_ts, id) tiebreak and
// >2000 distinct cwds in one bucket to exercise __other__ collapse parity.
func TestRefreshRollups_ParityWithBackfill(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	// ---- Store A: incremental path (driven through the writer). ----
	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)
	// Session start at day0 08:00 (closed hour, closed day).
	startA := ts(0, 8, 0)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/proj", startA, 100),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 101, Ts: startA},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	// Two llm ops with EQUAL start_ts (09:15) but different seq/id → tiebreak.
	eqStart := ts(0, 9, 15)
	eqEnd := ts(0, 9, 30)
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, eqStart, eqEnd, "sonnet", "anthropic", 100, 200, 0.1, false)...)
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 2, eqStart, eqEnd, "sonnet", "anthropic", 7, 9, 0.2, true)...)
	// A third op in a different closed hour the same day (10:00).
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 3, ts(0, 10, 0), ts(0, 10, 5), "haiku", "anthropic", 1, 2, 0.05, false)...)
	// >2000 distinct cwds in ONE hour bucket (11:00) to exercise __other__.
	// Each distinct cwd is a separate session+turn+op so the cwd dimension in
	// that bucket exceeds the 2000 cap and collapses its tail.
	collapseHourStart := ts(0, 11, 0)
	collapseEnd := ts(0, 11, 5)
	const distinctCwds = 2100
	seqBase := uint64(1000)
	for i := 0; i < distinctCwds; i++ {
		sess := fmt.Sprintf("csess-%d", i)
		cwd := fmt.Sprintf("/work/c%05d", i)
		batch = append(batch, sessionStartEvent(src, sess, "claude", cwd, collapseHourStart, seqBase))
		seqBase++
		batch = append(batch, canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seqBase, Ts: collapseHourStart},
			SessionNativeID: sess, Seq: 1,
		})
		seqBase++
		op := llmOpEvents(src, sess, 1, 1, collapseHourStart, collapseEnd, "sonnet", "anthropic", 1, 1, 0.001, false)
		// Re-stamp SourceSeq to stay globally increasing (cosmetic; not a gate).
		batch = append(batch,
			withSeq(op[0], seqBase),
			withSeq(op[1], seqBase+1),
		)
		seqBase += 2
	}
	flushBatch(t, dbA, src, format, now, batch)

	// ---- Store B: backfill path (seed identical ops/sessions, then backfill). ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude", "/work/proj", startA)
	seedTurn(t, dbB, "turn-1", "sess-1", startA)
	seedOp(t, dbB, opSpec{id: "op-eq-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: eqStart, endTS: &eqEnd, durationUS: eqEnd - eqStart, status: "completed",
		tokensIn: 100, tokensOut: 200, costUSD: 0.1})
	seedOp(t, dbB, opSpec{id: "op-eq-2", turnID: "turn-1", sessionID: "sess-1", seq: 2,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: eqStart, endTS: &eqEnd, durationUS: eqEnd - eqStart, status: "failed",
		tokensIn: 7, tokensOut: 9, costUSD: 0.2})
	op3End := ts(0, 10, 5)
	seedOp(t, dbB, opSpec{id: "op-3", turnID: "turn-1", sessionID: "sess-1", seq: 3,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: ts(0, 10, 0), endTS: &op3End, durationUS: op3End - ts(0, 10, 0), status: "completed",
		tokensIn: 1, tokensOut: 2, costUSD: 0.05})
	for i := 0; i < distinctCwds; i++ {
		sess := fmt.Sprintf("csess-%d", i)
		cwd := fmt.Sprintf("/work/c%05d", i)
		turn := "cturn-" + sess
		seedSession(t, dbB, sess, src, "claude", cwd, collapseHourStart)
		seedTurn(t, dbB, turn, sess, collapseHourStart)
		seedOp(t, dbB, opSpec{id: "cop-" + sess, turnID: turn, sessionID: sess, seq: 1,
			kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
			startTS: collapseHourStart, endTS: &collapseEnd, durationUS: collapseEnd - collapseHourStart,
			status: "completed", tokensIn: 1, tokensOut: 1, costUSD: 0.001})
	}
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	assertRollupsIdentical(t, dbA, dbB)
}

// withSeq returns a copy of ev with its SourceSeq overwritten. Only used to
// keep the synthetic high-cardinality batch's SourceSeq monotonic — the value
// is an observability counter, never a gate.
func withSeq(ev canonical.Event, seq uint64) canonical.Event {
	switch e := ev.(type) {
	case canonical.OpStartedEvent:
		e.SourceSeq = seq
		return e
	case canonical.OpFinalizedEvent:
		e.SourceSeq = seq
		return e
	default:
		return ev
	}
}

// assertRollupsIdentical fails unless rollup_hourly AND rollup_daily are
// row-for-row identical between the two stores (every column incl. cost_usd).
func assertRollupsIdentical(t *testing.T, dbIncremental, dbBackfill *sql.DB) {
	t.Helper()
	for _, table := range []string{"rollup_hourly", "rollup_daily"} {
		inc := readRollups(t, dbIncremental, table)
		bf := readRollups(t, dbBackfill, table)
		if len(inc) != len(bf) {
			t.Fatalf("%s row count mismatch: incremental=%d backfill=%d", table, len(inc), len(bf))
		}
		for k, v := range bf {
			got, ok := inc[k]
			if !ok {
				t.Fatalf("%s: incremental missing backfill row %+v", table, k)
			}
			if got != v {
				t.Fatalf("%s row %+v differs:\n incremental=%+v\n backfill   =%+v", table, k, got, v)
			}
		}
	}
}

// TestRefreshRollups_OpenBucketExclusion: a batch touching the open hour and the
// open day must not materialize the open hour (no hourly row) nor the open day
// (no daily row), while a closed hour of the open day DOES appear in hourly.
func TestRefreshRollups_OpenBucketExclusion(t *testing.T) {
	const src = "codex:/loc"
	const format = "codex"
	// now = day2 14:30 → open hour = day2 14:00, open day = day2 00:00.
	now := ts(2, 14, 30)

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "agent", "/w", ts(2, 9, 0), 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: ts(2, 9, 0)},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	// closed hour of the OPEN day (day2 09:00).
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, ts(2, 9, 0), ts(2, 9, 5), "m", "p", 1, 1, 0, false)...)
	// open-hour op (day2 14:10 → bucket 14:00 == openHourStart) → excluded.
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 2, ts(2, 14, 10), ts(2, 14, 20), "m", "p", 1, 1, 0, false)...)

	flushBatch(t, db, src, format, now, batch)

	h := readRollups(t, db, "rollup_hourly")
	d := readRollups(t, db, "rollup_daily")
	if _, ok := h[rollupKey{ts(2, 9, 0), format, "total", ""}]; !ok {
		t.Fatal("closed hour day2 09:00 missing from rollup_hourly")
	}
	if _, ok := h[rollupKey{ts(2, 14, 0), format, "total", ""}]; ok {
		t.Fatal("open hour day2 14:00 present — must be excluded")
	}
	if _, ok := d[rollupKey{ts(2, 0, 0), format, "total", ""}]; ok {
		t.Fatal("open day day2 00:00 present in rollup_daily — must be excluded")
	}
}

// TestRefreshRollups_ReplayIdempotent: applying the same batch twice (re-emitted
// events) leaves the rollups identical (delete-then-insert recompute).
func TestRefreshRollups_ReplayIdempotent(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	makeBatch := func() []canonical.Event {
		b := []canonical.Event{
			sessionStartEvent(src, "sess-1", "claude", "/w", ts(0, 8, 0), 1),
			canonical.TurnStartedEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: ts(0, 8, 0)},
				SessionNativeID: "sess-1", Seq: 1,
			},
		}
		b = append(b, llmOpEvents(src, "sess-1", 1, 1, ts(0, 9, 0), ts(0, 9, 30), "m", "p", 10, 20, 0.5, false)...)
		return b
	}

	flushBatch(t, db, src, format, now, makeBatch())
	h1 := readRollups(t, db, "rollup_hourly")
	d1 := readRollups(t, db, "rollup_daily")

	flushBatch(t, db, src, format, now, makeBatch())
	h2 := readRollups(t, db, "rollup_hourly")
	d2 := readRollups(t, db, "rollup_daily")

	if len(h1) != len(h2) || len(d1) != len(d2) {
		t.Fatalf("row counts changed on replay: hourly %d->%d daily %d->%d", len(h1), len(h2), len(d1), len(d2))
	}
	for k, v := range h1 {
		if h2[k] != v {
			t.Fatalf("hourly row %+v changed on replay: %+v -> %+v", k, v, h2[k])
		}
	}
	for k, v := range d1 {
		if d2[k] != v {
			t.Fatalf("daily row %+v changed on replay: %+v -> %+v", k, v, d2[k])
		}
	}
}

// TestRefreshRollups_OtherStaleRowRemoval: a cwd that first has its OWN row in a
// bucket must NOT leave a stale per-value row after later batches add enough
// distinct cwds to collapse it into __other__. Delete-then-insert is what makes
// this hold (an overwrite-only upsert would strand the old row).
func TestRefreshRollups_OtherStaleRowRemoval(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	hour := ts(0, 9, 0)
	end := ts(0, 9, 5)

	// Batch 1: a single distinct cwd → it gets its OWN cwd row.
	first := []canonical.Event{
		sessionStartEvent(src, "lonely", "claude", "/work/lonely", hour, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hour},
			SessionNativeID: "lonely", Seq: 1,
		},
	}
	first = append(first, llmOpEvents(src, "lonely", 1, 1, hour, end, "m", "p", 1, 1, 0, false)...)
	flushBatch(t, db, src, format, now, first)

	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hour, format, "cwd", "/work/lonely"}]; !ok {
		t.Fatal("premise broken: lonely cwd row not present after first batch")
	}

	// Batch 2: 2100 more distinct cwds with HIGHER op_count than lonely (each 2
	// ops) so the rank pushes /work/lonely into the tail → collapses to __other__.
	second := []canonical.Event{}
	seq := uint64(1000)
	const extra = 2100
	for i := 0; i < extra; i++ {
		sess := fmt.Sprintf("e-%d", i)
		cwd := fmt.Sprintf("/work/e%05d", i)
		second = append(second, sessionStartEvent(src, sess, "claude", cwd, hour, seq))
		seq++
		second = append(second, canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: seq, Ts: hour},
			SessionNativeID: sess, Seq: 1,
		})
		seq++
		// two ops so op_count=2 > lonely's 1, ranking lonely into the tail.
		o1 := llmOpEvents(src, sess, 1, 1, hour, end, "m", "p", 1, 1, 0, false)
		o2 := llmOpEvents(src, sess, 1, 2, hour, end, "m", "p", 1, 1, 0, false)
		second = append(second, withSeq(o1[0], seq), withSeq(o1[1], seq+1))
		seq += 2
		second = append(second, withSeq(o2[0], seq), withSeq(o2[1], seq+1))
		seq += 2
	}
	flushBatch(t, db, src, format, now, second)

	h := readRollups(t, db, "rollup_hourly")
	if _, ok := h[rollupKey{hour, format, "cwd", "/work/lonely"}]; ok {
		t.Fatal("stale /work/lonely cwd row survived collapse — delete-then-insert recompute failed")
	}
	if _, ok := h[rollupKey{hour, format, "cwd", "__other__"}]; !ok {
		t.Fatal("__other__ cwd row missing after collapse")
	}
}

// TestRefreshRollups_MultiSourceIsolation: two writers/sources writing the SAME
// hour bucket keep separate (bucket, source_format) rows; one source's refresh
// must not delete the other's rows.
func TestRefreshRollups_MultiSourceIsolation(t *testing.T) {
	const srcA, fmtA = "claude_code:/loc", "claude_code"
	const srcB, fmtB = "codex:/loc", "codex"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, srcA, fmtA)
	seedSource(t, db, srcB, fmtB)

	hour := ts(0, 9, 0)
	end := ts(0, 9, 5)

	batchA := []canonical.Event{
		sessionStartEvent(srcA, "a-sess", "claude", "/w", hour, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: srcA, SourceSeq: 2, Ts: hour}, SessionNativeID: "a-sess", Seq: 1},
	}
	batchA = append(batchA, llmOpEvents(srcA, "a-sess", 1, 1, hour, end, "m", "p", 5, 0, 0, false)...)
	flushBatch(t, db, srcA, fmtA, now, batchA)

	batchB := []canonical.Event{
		sessionStartEvent(srcB, "b-sess", "agent", "/w", hour, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: srcB, SourceSeq: 2, Ts: hour}, SessionNativeID: "b-sess", Seq: 1},
	}
	batchB = append(batchB, llmOpEvents(srcB, "b-sess", 1, 1, hour, end, "m", "p", 9, 0, 0, false)...)
	flushBatch(t, db, srcB, fmtB, now, batchB)

	h := readRollups(t, db, "rollup_hourly")
	a := h[rollupKey{hour, fmtA, "total", ""}]
	b := h[rollupKey{hour, fmtB, "total", ""}]
	if a.tokensIn != 5 {
		t.Fatalf("claude_code total tokensIn = %d, want 5 (source B refresh deleted it?)", a.tokensIn)
	}
	if b.tokensIn != 9 {
		t.Fatalf("codex total tokensIn = %d, want 9", b.tokensIn)
	}
}

// TestRefreshRollups_AggregatesMultipleSourcesSameFormat pins the by-format
// recompute: two DISTINCT sources (distinct source_ids / locations) that share
// ONE source_format and both write the SAME closed hour bucket must aggregate
// into a single (bucket, format) cell — exactly as BackfillRollups folds ops by
// src.format. This is the documented two-`--source` config (e.g.
// claude_code:/a + claude_code:/b). It would fail if the incremental refresh
// DELETEd the whole (bucket, format) cell then re-read only the current
// writer's source_id, silently dropping the sibling's contribution.
func TestRefreshRollups_AggregatesMultipleSourcesSameFormat(t *testing.T) {
	const format = "claude_code"
	const srcA = "claude_code:/a"
	const srcB = "claude_code:/b"
	now := fixedNow()

	hour := ts(0, 9, 0)
	end := ts(0, 9, 5)

	// Per-source op metrics (distinct so the SUM is unambiguous).
	const (
		aTokensIn, aTokensOut int64   = 100, 200
		bTokensIn, bTokensOut int64   = 7, 9
		aCost                 float64 = 0.10
		bCost                 float64 = 0.20
	)

	// ---- Store A: incremental path — drive BOTH sources through the writer,
	// each via its own flushBatch (one worker per source), same format/hour.
	_, dbA := openTestStore(t)
	seedSource(t, dbA, srcA, format)
	seedSource(t, dbA, srcB, format)

	batchA := []canonical.Event{
		sessionStartEvent(srcA, "a-sess", "claude", "/work/proj-a", hour, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: srcA, SourceSeq: 2, Ts: hour}, SessionNativeID: "a-sess", Seq: 1},
	}
	batchA = append(batchA, llmOpEvents(srcA, "a-sess", 1, 1, hour, end, "sonnet", "anthropic", aTokensIn, aTokensOut, aCost, false)...)
	flushBatch(t, dbA, srcA, format, now, batchA)

	batchB := []canonical.Event{
		sessionStartEvent(srcB, "b-sess", "claude", "/work/proj-b", hour, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: srcB, SourceSeq: 2, Ts: hour}, SessionNativeID: "b-sess", Seq: 1},
	}
	batchB = append(batchB, llmOpEvents(srcB, "b-sess", 1, 1, hour, end, "sonnet", "anthropic", bTokensIn, bTokensOut, bCost, true)...)
	flushBatch(t, dbA, srcB, format, now, batchB)

	// The shared (bucket, format, total) cell must hold BOTH sources summed.
	total := readRollups(t, dbA, "rollup_hourly")[rollupKey{hour, format, "total", ""}]
	if total.opCount != 2 {
		t.Fatalf("total op_count = %d, want 2 (both sources' ops aggregated; sibling source's op was wiped?)", total.opCount)
	}
	if total.tokensIn != aTokensIn+bTokensIn {
		t.Fatalf("total tokens_in = %d, want %d (sum of both sources)", total.tokensIn, aTokensIn+bTokensIn)
	}
	if total.tokensOut != aTokensOut+bTokensOut {
		t.Fatalf("total tokens_out = %d, want %d (sum of both sources)", total.tokensOut, aTokensOut+bTokensOut)
	}
	if total.costUSD != aCost+bCost {
		t.Fatalf("total cost_usd = %v, want %v (sum of both sources)", total.costUSD, aCost+bCost)
	}
	if total.failures != 1 {
		t.Fatalf("total failures = %d, want 1 (source B's op is failed)", total.failures)
	}
	if total.sessionStarts != 2 {
		t.Fatalf("total session_starts = %d, want 2 (one per source)", total.sessionStarts)
	}

	// ---- Store B: backfill path — seed the IDENTICAL ops/sessions for both
	// sources, then BackfillRollups over the same data + same now.
	_, dbB := openTestStore(t)
	seedSource(t, dbB, srcA, format)
	seedSource(t, dbB, srcB, format)
	seedSession(t, dbB, "a-sess", srcA, "claude", "/work/proj-a", hour)
	seedTurn(t, dbB, "a-turn", "a-sess", hour)
	seedOp(t, dbB, opSpec{id: "a-op", turnID: "a-turn", sessionID: "a-sess", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hour, endTS: &end, durationUS: end - hour, status: "completed",
		tokensIn: aTokensIn, tokensOut: aTokensOut, costUSD: aCost})
	seedSession(t, dbB, "b-sess", srcB, "claude", "/work/proj-b", hour)
	seedTurn(t, dbB, "b-turn", "b-sess", hour)
	seedOp(t, dbB, opSpec{id: "b-op", turnID: "b-turn", sessionID: "b-sess", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hour, endTS: &end, durationUS: end - hour, status: "failed",
		tokensIn: bTokensIn, tokensOut: bTokensOut, costUSD: bCost})
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	// Incremental (two same-format sources) must be byte-identical to backfill.
	assertRollupsIdentical(t, dbA, dbB)
}

// TestRefreshRollups_SessionStartsAttribution: a session_start lands on
// total/agent/cwd of its start bucket only — never model/provider/tool.
func TestRefreshRollups_SessionStartsAttribution(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start := ts(0, 9, 0)
	end := ts(0, 9, 5)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/proj", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
	}
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, start, end, "sonnet", "anthropic", 1, 1, 0, false)...)
	flushBatch(t, db, src, format, now, batch)

	h := readRollups(t, db, "rollup_hourly")
	bucket := ts(0, 9, 0)
	if v := h[rollupKey{bucket, format, "total", ""}]; v.sessionStarts != 1 {
		t.Fatalf("total session_starts = %d, want 1", v.sessionStarts)
	}
	if v := h[rollupKey{bucket, format, "agent", "claude"}]; v.sessionStarts != 1 {
		t.Fatalf("agent session_starts = %d, want 1", v.sessionStarts)
	}
	if v := h[rollupKey{bucket, format, "cwd", "/work/proj"}]; v.sessionStarts != 1 {
		t.Fatalf("cwd session_starts = %d, want 1", v.sessionStarts)
	}
	if v := h[rollupKey{bucket, format, "model", "sonnet"}]; v.sessionStarts != 0 {
		t.Fatalf("model session_starts leaked = %d, want 0", v.sessionStarts)
	}
	if v := h[rollupKey{bucket, format, "provider", "anthropic"}]; v.sessionStarts != 0 {
		t.Fatalf("provider session_starts leaked = %d, want 0", v.sessionStarts)
	}
}

// TestRefreshRollups_Notify: a batch that changes rollups emits exactly one
// stats_invalidated row.
func TestRefreshRollups_Notify(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start := ts(0, 9, 0)
	end := ts(0, 9, 5)
	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
	}
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, start, end, "m", "p", 1, 1, 0, false)...)
	flushBatch(t, db, src, format, now, batch)

	n := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='stats_invalidated'`)
	if n != 1 {
		t.Fatalf("stats_invalidated count = %d, want exactly 1", n)
	}
}

// TestRefreshRollups_OpFinalizedRefreshesStartBucket: finalizing an op (adding
// duration + flipping to failed) in a LATER batch than its OpStarted must update
// that hour's duration_us / failures — proving OpFinalized marks the op's START
// bucket, not ev.Ts (the END).
func TestRefreshRollups_OpFinalizedRefreshesStartBucket(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	start := ts(0, 9, 0)
	end := ts(0, 9, 30)

	// Batch 1: SessionStarted + TurnStarted + OpStarted only (op still running).
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: start},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Model: "m", Provider: "p",
		},
	}
	flushBatch(t, db, src, format, now, batch1)

	bucket := ts(0, 9, 0)
	running := readRollups(t, db, "rollup_hourly")[rollupKey{bucket, format, "total", ""}]
	if running.opCount != 1 {
		t.Fatalf("after OpStarted: opCount = %d, want 1", running.opCount)
	}
	if running.durationUS != 0 || running.failures != 0 {
		t.Fatalf("running op should have 0 duration/failures, got dur=%d fail=%d", running.durationUS, running.failures)
	}

	// Batch 2: OpFinalized (END at 09:30, status failed). Its bucket marking must
	// use the PERSISTED start_ts (09:00 bucket), not ev.Ts (09:30, same bucket
	// here but the point is the START bucket gets recomputed with the new
	// duration/failure).
	batch2 := []canonical.Event{
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: end},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, Status: "failed", EndTs: end,
			TokensIn: 10, TokensOut: 20, CostUSD: 0.5,
		},
	}
	flushBatch(t, db, src, format, now, batch2)

	final := readRollups(t, db, "rollup_hourly")[rollupKey{bucket, format, "total", ""}]
	if final.opCount != 1 {
		t.Fatalf("after OpFinalized: opCount = %d, want 1", final.opCount)
	}
	if final.durationUS != end-start {
		t.Fatalf("after OpFinalized: durationUS = %d, want %d", final.durationUS, end-start)
	}
	if final.failures != 1 {
		t.Fatalf("after OpFinalized: failures = %d, want 1", final.failures)
	}
	if final.tokensIn != 10 {
		t.Fatalf("after OpFinalized: tokensIn = %d, want 10", final.tokensIn)
	}
}

// TestRefreshRollups_NoDirtyBucketsNoWork: a batch that touches no rollup input
// (e.g. only a SessionFinalized for an existing session) leaves the rollup
// tables untouched and emits no stats_invalidated for rollups.
func TestRefreshRollups_OpFinalizedAcrossHourMarksStartBucket(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	// OpStarted at 09:50, OpFinalized END at 10:10 — start bucket 09:00, end
	// bucket 10:00 differ. Finalize MUST recompute the 09:00 (start) bucket.
	start := ts(0, 9, 50)
	end := ts(0, 10, 10)

	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", start, 1),
		canonical.TurnStartedEvent{EventBase: canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: start}, SessionNativeID: "sess-1", Seq: 1},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: start},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
			Kind: canonical.OpLLM, Name: "chat", Model: "m", Provider: "p",
		},
	}
	flushBatch(t, db, src, format, now, batch1)

	batch2 := []canonical.Event{
		canonical.OpFinalizedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: end},
			SessionNativeID: "sess-1", TurnSeq: 1, Seq: 1, Status: "completed", EndTs: end,
		},
	}
	flushBatch(t, db, src, format, now, batch2)

	startBucket := ts(0, 9, 0)
	v := readRollups(t, db, "rollup_hourly")[rollupKey{startBucket, format, "total", ""}]
	if v.durationUS != end-start {
		t.Fatalf("start-bucket duration_us = %d, want %d (finalize must refresh the START bucket %d)", v.durationUS, end-start, startBucket)
	}
}
