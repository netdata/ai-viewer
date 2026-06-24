package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
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
	flushBatchWithMaxRollupRows(t, db, sourceID, format, now, batch, 0)
}

// flushBatchWithMaxRollupRows is flushBatch with an injected R1 collapse cap
// (writer.maxRollupRowsPerBucket). Tests that exercise the __other__
// tail-collapse set this small so they can use a small high-cardinality batch
// instead of 2000+ real events (SOW-0062). 0 keeps the rollups default (2000).
// When a test compares the incremental path to BackfillRollups, it MUST pass
// the SAME value to WithBackfillMaxRollupRowsPerBucket, or the
// refresh≡backfill byte-parity invariant breaks for collapse cases.
func flushBatchWithMaxRollupRows(t *testing.T, db *sql.DB, sourceID, format string, now int64, batch []canonical.Event, maxRollupRows int) {
	t.Helper()
	ctx := context.Background()
	wr := newWriter(sourceID, format, "/loc", NopPricer{})
	wr.now = func() int64 { return now }
	wr.maxRollupRowsPerBucket = maxRollupRows
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

// mutableClock is an injectable clock whose value the test advances between
// flushes, so a bucket open at one flush is closed at the next — the open→closed
// transition the carry-forward dirty set must survive. flushBatchReuse drives a
// SINGLE writer across batches (production reuses wr in worker.run), so the
// carry-forward state on the writer persists exactly as it does in production.
type mutableClock struct {
	mu  sync.RWMutex
	now int64
}

func (c *mutableClock) Now() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *mutableClock) Set(now int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type notifyRollbackFixture struct {
	db                *sql.DB
	wr                *writer
	w                 *worker
	hourH             int64
	day0              int64
	baselineNotify    int64
	baselineStatsRows int64
}

// flushBatchReuse runs one worker.flush over batch against db reusing the GIVEN
// writer (so its carry-forward dirtyRollupBuckets survive across calls), with the
// writer's now driving the open-bucket cutoff. Mirrors worker.run's per-batch
// flush → resetBatch sequence, the only path that exercises cross-batch carry.
func flushBatchReuse(t *testing.T, db *sql.DB, wr *writer, sourceID, format string, batch []canonical.Event) {
	t.Helper()
	ctx := context.Background()
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

// refreshOnlyReuse drives worker.refreshRollupsOnly (the idle materialization
// path) against the GIVEN reused writer, with the writer's now driving the
// open-bucket cutoff. Mirrors worker.run's idle-tick → resetBatch sequence — the
// no-events path that materializes carried buckets that have since closed.
func refreshOnlyReuse(t *testing.T, db *sql.DB, wr *writer) {
	t.Helper()
	ctx := context.Background()
	w := &worker{
		sourceID:     wr.sourceID,
		sourceFormat: wr.sourceFormat,
		location:     wr.location,
		db:           db,
		hwm:          newHWMCache(),
		pricer:       NopPricer{},
		logger:       silentLogger(),
		batchSize:    defaultBatchSize,
		batchEvery:   defaultBatchInterval,
	}
	if err := w.refreshRollupsOnly(ctx, wr); err != nil {
		t.Fatalf("worker.refreshRollupsOnly: %v", err)
	}
	wr.resetBatch()
}

// TestRefreshRollups_OpenHourMaterializedAfterClose pins the carry-forward bug
// (round-7 P1). An hour bucket whose ops ALL arrive while that hour is still the
// open hour is skipped at the first refresh (the open hour is never materialized)
// and — before the fix — was forgotten by resetBatch's clear(dirtyRollupBuckets),
// so after the hour closed it was never re-marked and never written to
// rollup_hourly. The all-sources fast path then silently undercounts it.
//
// The fix carries the dirty bucket forward across batches: a bucket skipped while
// open is RETAINED, and materialized on the first refresh after it closes. The
// proof is byte-parity with a fresh BackfillRollups at the FINAL now (which sees
// the bucket closed and materializes it).
func TestRefreshRollups_OpenHourMaterializedAfterClose(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"

	// Batch 1 at now=T inside hour H (H is the OPEN hour → H is skipped, retained).
	// Batch 2 at now=T+1h+ε: H is now closed, H+1 is the new open hour.
	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	nowAtBatch1 := ts(0, 10, 10) // open hour = H (10:00); H must NOT materialize yet.
	hourH1 := ts(0, 11, 0)
	hourH1End := ts(0, 11, 5)
	nowAtBatch2 := ts(0, 11, 1) // open hour = H+1 (11:00); H (10:00) is now closed.

	// ---- Store A: incremental path, ONE writer reused across the two batches. ----
	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)
	clk := &mutableClock{now: nowAtBatch1}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	// Batch 1: session + op entirely within the OPEN hour H.
	clk.Set(nowAtBatch1)
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/proj", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "sonnet", "anthropic", 10, 20, 0.1, false)...)
	flushBatchReuse(t, dbA, wr, src, format, batch1)

	// Premise: H is the open hour at batch 1, so it must NOT be materialized yet.
	if _, ok := readRollups(t, dbA, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; ok {
		t.Fatal("premise broken: open hour H materialized at batch 1 — the open hour is never materialized")
	}

	// Batch 2: a NEW op in hour H+1 at a now where H has closed. The carried H must
	// now materialize; H+1 (the new open hour) must be skipped+retained.
	clk.Set(nowAtBatch2)
	batch2 := []canonical.Event{
		sessionStartEvent(src, "sess-2", "claude", "/work/proj", hourH1, 100),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 101, Ts: hourH1},
			SessionNativeID: "sess-2", Seq: 1,
		},
	}
	batch2 = append(batch2, llmOpEvents(src, "sess-2", 1, 1, hourH1, hourH1End, "haiku", "anthropic", 1, 2, 0.05, false)...)
	flushBatchReuse(t, dbA, wr, src, format, batch2)

	// The previously-open hour H MUST now be present (carry-forward materialized it).
	if _, ok := readRollups(t, dbA, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; !ok {
		t.Fatal("hour H missing from rollup_hourly after it closed — carry-forward dropped it (round-7 P1)")
	}

	// ---- Store B: backfill over the SAME data at the FINAL now (H closed). ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude", "/work/proj", hourH)
	seedTurn(t, dbB, "turn-1", "sess-1", hourH)
	seedOp(t, dbB, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hourH, endTS: &hourHEnd, durationUS: hourHEnd - hourH, status: "completed",
		tokensIn: 10, tokensOut: 20, costUSD: 0.1,
	})
	seedSession(t, dbB, "sess-2", src, "claude", "/work/proj", hourH1)
	seedTurn(t, dbB, "turn-2", "sess-2", hourH1)
	seedOp(t, dbB, opSpec{
		id: "op-2", turnID: "turn-2", sessionID: "sess-2", seq: 1,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: hourH1, endTS: &hourH1End, durationUS: hourH1End - hourH1, status: "completed",
		tokensIn: 1, tokensOut: 2, costUSD: 0.05,
	})
	if _, err := BackfillRollups(context.Background(), dbB, nowAtBatch2, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	// The incremental store (carry-forward) must be byte-identical to the backfill
	// at the final now: H materialized, H+1 (still open) excluded from BOTH.
	assertRollupsIdentical(t, dbA, dbB)
}

// TestRefreshRollups_RefreshOnlyMaterializesClosedCarried pins the idle
// materialization primitive at the writer level (round-7 Part 2): after a batch
// leaves an open hour H carried in the dirty set, a refresh-only pass (no new
// events) at a later now where H has closed MUST materialize H and report it
// materialized, WITHOUT a further marking event. This is exactly what the worker's
// idle tick invokes when the batch is empty but buckets are pending. The
// refresh-only pass must NOT wipe the carried set itself (refreshRollups removes
// only the buckets it materialized).
func TestRefreshRollups_RefreshOnlyMaterializesClosedCarried(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"

	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	nowOpen := ts(0, 10, 10) // H is open → skipped + carried.
	// nowClosed is past midnight so BOTH H and its DAY (day0) are closed: this test
	// pins the hourly idle primitive + rollback safety, and asserts the carried set
	// is empty after a committed materialization. Under the carried-DAY model
	// (round-8) the day H belongs to is also carried, so the cutoff must close the
	// day too for "no longer pending" to hold (TestRefreshRollups_OpenDay…
	// covers the hour-closed-but-day-open case explicitly).
	nowClosed := ts(1, 0, 1)

	_, db := openTestStore(t)
	seedSource(t, db, src, format)
	clk := &mutableClock{now: nowOpen}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "m", "p", 1, 1, 0, false)...)
	flushBatchReuse(t, db, wr, src, format, batch)

	// H is open at the first flush → carried, not yet materialized.
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("expected H carried as pending after first flush (open hour)")
	}
	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; ok {
		t.Fatal("premise broken: open hour H materialized at first flush")
	}

	// First, prove the ROLLBACK-SAFETY property (the deferred-promote guard): a
	// refresh that materializes H but whose tx ROLLS BACK must keep H CARRIED, so a
	// failed idle pass (whose bucket's ops are committed by a prior batch) is
	// retried rather than silently dropped. Advance the clock past midnight so H AND
	// its day are closed (both carried sets materialize and stage removal together).
	clk.Set(nowClosed)
	ctx := context.Background()
	txRB, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := wr.refreshRollups(ctx, txRB); err != nil {
		t.Fatalf("refreshRollups: %v", err)
	}
	if !wr.rollupMaterializedThisRefresh {
		t.Fatal("refresh over a now-closed carried bucket reported no materialization")
	}
	// Removal is STAGED, not yet applied (mirrors production: promote runs only
	// after commit). H must still be carried until promote.
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("H removed from carried set before commit — the removal must be deferred to post-commit")
	}
	if err := txRB.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	wr.resetBatch() // discards the staged removals (pending hour+day maps) on rollback.
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("H dropped from carried set after a ROLLED-BACK materialization — round-7 P1 undercount reintroduced")
	}
	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; ok {
		t.Fatal("H present in rollup_hourly after rollback — the recompute must have rolled back")
	}

	// Now the committed path: refresh again, commit, then promote (exactly what
	// worker.refreshRollupsOnly does post-commit). H must materialize and leave
	// the carried set.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := wr.refreshRollups(ctx, tx); err != nil {
		t.Fatalf("refreshRollups: %v", err)
	}
	if !wr.rollupMaterializedThisRefresh {
		t.Fatal("refresh-only pass over a now-closed carried bucket reported no materialization")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	wr.promoteMaterializedRollupBuckets() // post-commit removal (as the worker does).

	// H is now materialized and no longer pending (promote removed it).
	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; !ok {
		t.Fatal("hour H not materialized by the refresh-only pass after it closed")
	}
	if wr.hasPendingRollupBuckets() {
		t.Fatal("carried bucket still pending after a COMMITTED materialization + promote — must be removed")
	}
}

// TestRefreshRollups_RefreshOnlyNotifyErrorRollsBack pins the worker-level
// idle-refresh transaction boundary. If refreshRollupsOnly materializes carried
// buckets but notify insertion fails before commit, the rollup rows and notify
// rows must roll back together, and the carried buckets must survive resetBatch
// so the next idle refresh can retry them.
func TestRefreshRollups_RefreshOnlyNotifyErrorRollsBack(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"

	f := newNotifyRollbackFixture(t, src, format)
	ctx := context.Background()
	createNotifyAbortTrigger(t, ctx, f.db)
	if err := f.w.refreshRollupsOnly(ctx, f.wr); err == nil {
		t.Fatal("refreshRollupsOnly succeeded despite forced notify failure")
	}

	assertFailedNotifyRefreshRolledBack(t, f)
	dropNotifyAbortTrigger(t, ctx, f.db)
	if err := f.w.refreshRollupsOnly(ctx, f.wr); err != nil {
		t.Fatalf("retry refreshRollupsOnly: %v", err)
	}
	f.wr.resetBatch()

	assertRollupRowCount(t, f.db, "rollup_hourly", f.hourH, 1, "rollup_hourly after retry")
	assertRollupRowCount(t, f.db, "rollup_daily", f.day0, 1, "rollup_daily after retry")
	wantStats := f.baselineStatsRows + 1
	if got := scanInt(t, f.db, `SELECT COUNT(*) FROM notify WHERE kind='stats_invalidated'`); got != wantStats {
		t.Fatalf("stats_invalidated rows after retry = %d, want %d", got, wantStats)
	}
	if f.wr.hasPendingRollupBuckets() {
		t.Fatal("carried buckets still pending after successful retry")
	}
}

func newNotifyRollbackFixture(t *testing.T, src, format string) notifyRollbackFixture {
	t.Helper()
	hourH := ts(0, 10, 0)
	_, db := openTestStore(t)
	seedSource(t, db, src, format)
	clk := &mutableClock{now: ts(0, 10, 10)}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	batch := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch = append(batch, llmOpEvents(src, "sess-1", 1, 1, hourH, ts(0, 10, 30), "m", "p", 1, 1, 0, false)...)
	flushBatchReuse(t, db, wr, src, format, batch)
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("premise broken: open hour/day must be carried after the first flush")
	}
	clk.Set(ts(1, 0, 1))

	return notifyRollbackFixture{
		db: db, wr: wr, w: newRefreshOnlyTestWorker(db, src, format),
		hourH: hourH, day0: ts(0, 0, 0),
		baselineNotify:    scanInt(t, db, `SELECT COUNT(*) FROM notify`),
		baselineStatsRows: scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='stats_invalidated'`),
	}
}

func newRefreshOnlyTestWorker(db *sql.DB, src, format string) *worker {
	return &worker{
		sourceID:     src,
		sourceFormat: format,
		location:     "/loc",
		db:           db,
		hwm:          newHWMCache(),
		pricer:       NopPricer{},
		logger:       silentLogger(),
		batchSize:    defaultBatchSize,
		batchEvery:   defaultBatchInterval,
	}
}

func createNotifyAbortTrigger(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
CREATE TRIGGER notify_stats_abort
BEFORE INSERT ON notify
WHEN NEW.kind = 'stats_invalidated'
BEGIN
	SELECT RAISE(ABORT, 'forced notify failure');
END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

func dropNotifyAbortTrigger(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER notify_stats_abort`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
}

func assertFailedNotifyRefreshRolledBack(t *testing.T, f notifyRollbackFixture) {
	t.Helper()
	assertRollupRowCount(t, f.db, "rollup_hourly", f.hourH, 0, "rollup_hourly after failed idle refresh")
	assertRollupRowCount(t, f.db, "rollup_daily", f.day0, 0, "rollup_daily after failed idle refresh")
	if got := scanInt(t, f.db, `SELECT COUNT(*) FROM notify`); got != f.baselineNotify {
		t.Fatalf("notify rows after failed idle refresh = %d, want %d", got, f.baselineNotify)
	}
	if !f.wr.hasPendingRollupBuckets() {
		t.Fatal("carried buckets dropped before resetBatch after failed idle refresh")
	}
	f.wr.resetBatch()
	if !f.wr.hasPendingRollupBuckets() {
		t.Fatal("carried buckets dropped after failed idle refresh resetBatch; retry would be lost")
	}
}

// TestRefreshRollups_NotifyNotFiredOnMerePendingCarry pins round-7 P1b: a batch
// that touches NO rollup input must NOT emit a rollup-driven stats_invalidated
// merely because a previously-open bucket is still CARRIED (pending). Under the
// carry-forward set, len(dirtyRollupBuckets) > 0 while an open bucket is pending,
// so keying the notify off the set length would fire every batch. The fix keys it
// off rollupTouchedThisBatch ∪ rollupMaterializedThisRefresh instead.
//
// Batch 1 marks the open hour H (touched → exactly one stats_invalidated). Batch 2
// carries ONLY a SourceProgressEvent (no session/op write, no rollup mark) while H
// is still open: affectedSessionIDs empty, nothing touched, nothing materialized →
// NO new stats_invalidated.
func TestRefreshRollups_NotifyNotFiredOnMerePendingCarry(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"

	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	nowOpen := ts(0, 10, 10) // H stays the open hour across BOTH batches → carried.

	_, db := openTestStore(t)
	seedSource(t, db, src, format)
	clk := &mutableClock{now: nowOpen}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	// Batch 1: a session + op in the open hour H → marks H (touched) → 1 notify.
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "m", "p", 1, 1, 0, false)...)
	flushBatchReuse(t, db, wr, src, format, batch1)

	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='stats_invalidated'`); got != 1 {
		t.Fatalf("after batch 1: stats_invalidated count = %d, want 1", got)
	}
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("premise broken: H must be carried (open) after batch 1")
	}

	// Batch 2: ONLY a SourceProgressEvent — no session/op write, no rollup mark.
	// H is STILL open (clock unchanged) so nothing materializes. The carried H
	// must NOT, by itself, trigger a stats_invalidated.
	batch2 := []canonical.Event{
		canonical.SourceProgressEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 0, Ts: hourH},
			Cursor:    `{"file":"a.jsonl","offset":1}`,
		},
	}
	flushBatchReuse(t, db, wr, src, format, batch2)

	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='stats_invalidated'`); got != 1 {
		t.Fatalf("after batch 2 (mere pending carry): stats_invalidated count = %d, want 1 (must NOT fire on a carried-open bucket alone)", got)
	}
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
// >cap distinct cwds in one bucket to exercise __other__ collapse parity.
//
// The R1 collapse cap is injected small (testMaxRows=20) on BOTH paths — the
// writer field (store A incremental) and the BackfillRollups option (store B) —
// set to the SAME value so the refresh≡backfill byte-parity invariant holds.
// This lets the test use a small high-cardinality batch (distinctCwds=30)
// instead of 2000+ real events; the collapse logic is independent of the cap's
// magnitude (SOW-0062).
func TestRefreshRollups_ParityWithBackfill(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()
	const testMaxRows = 20 // injected R1 cap (default 2000); same on both paths. SOW-0062.

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
	// >cap distinct cwds in ONE hour bucket (11:00) to exercise __other__.
	// Each distinct cwd is a separate session+turn+op so the cwd dimension in
	// that bucket exceeds the cap and collapses its tail.
	collapseHourStart := ts(0, 11, 0)
	collapseEnd := ts(0, 11, 5)
	const distinctCwds = 30
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
	flushBatchWithMaxRollupRows(t, dbA, src, format, now, batch, testMaxRows)

	// ---- Store B: backfill path (seed identical ops/sessions, then backfill). ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude", "/work/proj", startA)
	seedTurn(t, dbB, "turn-1", "sess-1", startA)
	seedOp(t, dbB, opSpec{
		id: "op-eq-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: eqStart, endTS: &eqEnd, durationUS: eqEnd - eqStart, status: "completed",
		tokensIn: 100, tokensOut: 200, costUSD: 0.1,
	})
	seedOp(t, dbB, opSpec{
		id: "op-eq-2", turnID: "turn-1", sessionID: "sess-1", seq: 2,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: eqStart, endTS: &eqEnd, durationUS: eqEnd - eqStart, status: "failed",
		tokensIn: 7, tokensOut: 9, costUSD: 0.2,
	})
	op3End := ts(0, 10, 5)
	seedOp(t, dbB, opSpec{
		id: "op-3", turnID: "turn-1", sessionID: "sess-1", seq: 3,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: ts(0, 10, 0), endTS: &op3End, durationUS: op3End - ts(0, 10, 0), status: "completed",
		tokensIn: 1, tokensOut: 2, costUSD: 0.05,
	})
	for i := 0; i < distinctCwds; i++ {
		sess := fmt.Sprintf("csess-%d", i)
		cwd := fmt.Sprintf("/work/c%05d", i)
		turn := "cturn-" + sess
		seedSession(t, dbB, sess, src, "claude", cwd, collapseHourStart)
		seedTurn(t, dbB, turn, sess, collapseHourStart)
		seedOp(t, dbB, opSpec{
			id: "cop-" + sess, turnID: turn, sessionID: sess, seq: 1,
			kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
			startTS: collapseHourStart, endTS: &collapseEnd, durationUS: collapseEnd - collapseHourStart,
			status: "completed", tokensIn: 1, tokensOut: 1, costUSD: 0.001,
		})
	}
	// SAME testMaxRows on the backfill path — the refresh≡backfill parity
	// invariant requires both paths to collapse at the same cap.
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger(),
		WithBackfillMaxRollupRowsPerBucket(testMaxRows)); err != nil {
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
//
// The R1 collapse cap is injected small (testMaxRows=20) so the test can use a
// small high-cardinality batch (extra=30) instead of 2000+ real events — the
// ranking (op_count DESC, dimension_value ASC) is what the assertion keys on,
// and it is independent of the cap's magnitude, so a small cap exercises the
// same delete-then-insert collapse path (SOW-0062).
func TestRefreshRollups_OtherStaleRowRemoval(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()
	const testMaxRows = 20 // injected R1 cap (default 2000); see SOW-0062.

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
	flushBatchWithMaxRollupRows(t, db, src, format, now, first, testMaxRows)

	if _, ok := readRollups(t, db, "rollup_hourly")[rollupKey{hour, format, "cwd", "/work/lonely"}]; !ok {
		t.Fatal("premise broken: lonely cwd row not present after first batch")
	}

	// Batch 2: 30 more distinct cwds with HIGHER op_count than lonely (each 2
	// ops) so the rank pushes /work/lonely into the tail → collapses to __other__.
	// extra > testMaxRows so the cwd dimension exceeds the cap and collapses.
	second := []canonical.Event{}
	seq := uint64(1000)
	const extra = 30
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
	flushBatchWithMaxRollupRows(t, db, src, format, now, second, testMaxRows)

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
	seedOp(t, dbB, opSpec{
		id: "a-op", turnID: "a-turn", sessionID: "a-sess", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hour, endTS: &end, durationUS: end - hour, status: "completed",
		tokensIn: aTokensIn, tokensOut: aTokensOut, costUSD: aCost,
	})
	seedSession(t, dbB, "b-sess", srcB, "claude", "/work/proj-b", hour)
	seedTurn(t, dbB, "b-turn", "b-sess", hour)
	seedOp(t, dbB, opSpec{
		id: "b-op", turnID: "b-turn", sessionID: "b-sess", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hour, endTS: &end, durationUS: end - hour, status: "failed",
		tokensIn: bTokensIn, tokensOut: bTokensOut, costUSD: bCost,
	})
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

// TestRefreshRollups_SessionUpdatedReattributesAgent pins F1: agent_name/cwd are
// session-denormalized rollup inputs (the fold reads sessions.agent_name/cwd via
// the join). A SessionUpdated that changes agent_name in a LATER batch — after the
// session's ops have already been rolled up incrementally — must re-attribute
// EVERY hour bucket that holds one of the session's ops, plus the session-start
// bucket (for session_starts). The proof is byte-parity with a fresh
// BackfillRollups over the FINAL session state (the new agent name): if
// applySessionUpdated failed to dirty the op buckets, the incremental store would
// keep the old agent's dimension rows and diverge from the backfill.
func TestRefreshRollups_SessionUpdatedReattributesAgent(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	// ---- Store A: incremental path. ----
	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)

	// Session starts at day0 08:00 with the OLD agent name; its ops land in TWO
	// distinct closed hours (09:00 and 10:00) so the multi-bucket re-attribution
	// is exercised (a session's ops span many buckets — F1's core hazard).
	startA := ts(0, 8, 0)
	hour1Start, hour1End := ts(0, 9, 0), ts(0, 9, 30)
	hour2Start, hour2End := ts(0, 10, 0), ts(0, 10, 20)
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "old-agent", "/work/proj", startA, 100),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 101, Ts: startA},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hour1Start, hour1End, "sonnet", "anthropic", 100, 200, 0.1, false)...)
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 2, hour2Start, hour2End, "haiku", "anthropic", 5, 7, 0.05, true)...)
	flushBatch(t, dbA, src, format, now, batch1)

	// Premise: the OLD agent's dimension rows exist in both hour buckets before
	// the update, so a missing re-attribution would leave a stale row.
	preH := readRollups(t, dbA, "rollup_hourly")
	if _, ok := preH[rollupKey{hour1Start, format, "agent", "old-agent"}]; !ok {
		t.Fatal("premise broken: old-agent row missing in hour1 before update")
	}
	if _, ok := preH[rollupKey{hour2Start, format, "agent", "old-agent"}]; !ok {
		t.Fatal("premise broken: old-agent row missing in hour2 before update")
	}

	// Later batch: a SessionUpdated repairs the agent name (e.g. claude_code
	// re-emitting agent metadata). No new ops — only the metadata changes.
	batch2 := []canonical.Event{
		canonical.SessionUpdatedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 200, Ts: ts(0, 10, 30)},
			NativeID:  "sess-1",
			AgentName: "new-agent",
		},
	}
	flushBatch(t, dbA, src, format, now, batch2)

	// ---- Store B: backfill over the FINAL state (session carries new-agent). ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "new-agent", "/work/proj", startA)
	seedTurn(t, dbB, "turn-1", "sess-1", startA)
	seedOp(t, dbB, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hour1Start, endTS: &hour1End, durationUS: hour1End - hour1Start, status: "completed",
		tokensIn: 100, tokensOut: 200, costUSD: 0.1,
	})
	seedOp(t, dbB, opSpec{
		id: "op-2", turnID: "turn-1", sessionID: "sess-1", seq: 2,
		kind: "llm", name: "chat", model: "haiku", provider: "anthropic",
		startTS: hour2Start, endTS: &hour2End, durationUS: hour2End - hour2Start, status: "failed",
		tokensIn: 5, tokensOut: 7, costUSD: 0.05,
	})
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	// The incremental store must now be byte-identical to the backfill — both
	// agent dimension rows re-attributed to new-agent, old-agent gone from BOTH
	// op buckets AND the session-start bucket.
	assertRollupsIdentical(t, dbA, dbB)

	// Explicit guards (so a regression names the exact failure, not just "diff").
	postH := readRollups(t, dbA, "rollup_hourly")
	for _, h := range []int64{hour1Start, hour2Start} {
		if _, ok := postH[rollupKey{h, format, "agent", "old-agent"}]; ok {
			t.Fatalf("stale old-agent row survived in hour bucket %d — op bucket not re-attributed", h)
		}
		if _, ok := postH[rollupKey{h, format, "agent", "new-agent"}]; !ok {
			t.Fatalf("new-agent row missing in hour bucket %d — op bucket not re-attributed", h)
		}
	}
	// The session-start bucket (08:00) must move session_starts onto new-agent.
	if v := postH[rollupKey{startA, format, "agent", "new-agent"}]; v.sessionStarts != 1 {
		t.Fatalf("session-start bucket agent=new-agent session_starts = %d, want 1", v.sessionStarts)
	}
	if _, ok := postH[rollupKey{startA, format, "agent", "old-agent"}]; ok {
		t.Fatal("stale old-agent session_starts row survived in the start bucket")
	}
}

// TestRefreshRollups_SessionUpdatedReattributesCwd pins F1 for the cwd dimension:
// a SessionUpdated changing cwd (with agent unchanged) must re-attribute the
// session's op buckets to the new cwd, byte-identical to a fresh backfill over the
// final state. Guards the `ev.Cwd != ""` half of the dirty-marking trigger.
func TestRefreshRollups_SessionUpdatedReattributesCwd(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)

	startA := ts(0, 8, 0)
	hourStart, hourEnd := ts(0, 9, 0), ts(0, 9, 30)
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/old", startA, 100),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 101, Ts: startA},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hourStart, hourEnd, "sonnet", "anthropic", 10, 20, 0.1, false)...)
	flushBatch(t, dbA, src, format, now, batch1)

	batch2 := []canonical.Event{
		canonical.SessionUpdatedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 200, Ts: ts(0, 9, 45)},
			NativeID:  "sess-1",
			Cwd:       "/work/new",
		},
	}
	flushBatch(t, dbA, src, format, now, batch2)

	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude", "/work/new", startA)
	seedTurn(t, dbB, "turn-1", "sess-1", startA)
	seedOp(t, dbB, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hourStart, endTS: &hourEnd, durationUS: hourEnd - hourStart, status: "completed",
		tokensIn: 10, tokensOut: 20, costUSD: 0.1,
	})
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	assertRollupsIdentical(t, dbA, dbB)

	postH := readRollups(t, dbA, "rollup_hourly")
	if _, ok := postH[rollupKey{hourStart, format, "cwd", "/work/old"}]; ok {
		t.Fatal("stale /work/old cwd row survived — op bucket not re-attributed on cwd change")
	}
	if _, ok := postH[rollupKey{hourStart, format, "cwd", "/work/new"}]; !ok {
		t.Fatal("/work/new cwd row missing — op bucket not re-attributed on cwd change")
	}
}

// TestRefreshRollups_SessionUpdatedNoMetadataNoBucketWork pins the negative case:
// a SessionUpdated carrying neither agent_name nor cwd (e.g. a status-only update)
// must NOT trigger the op-bucket dirty-marking scan — its only rollup-relevant
// inputs are unchanged, so the rollup tables stay byte-identical. This guards the
// `ev.AgentName != "" || ev.Cwd != ""` gate against firing on every SessionUpdated.
func TestRefreshRollups_SessionUpdatedNoMetadataNoBucketWork(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	startA := ts(0, 8, 0)
	hourStart, hourEnd := ts(0, 9, 0), ts(0, 9, 30)
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/proj", startA, 100),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 101, Ts: startA},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hourStart, hourEnd, "sonnet", "anthropic", 10, 20, 0.1, false)...)
	flushBatch(t, db, src, format, now, batch1)

	before := readRollups(t, db, "rollup_hourly")

	// Status-only update: no agent, no cwd.
	batch2 := []canonical.Event{
		canonical.SessionUpdatedEvent{
			EventBase: canonical.EventBase{SourceID: src, SourceSeq: 200, Ts: ts(0, 9, 45)},
			NativeID:  "sess-1",
			Status:    "completed",
		},
	}
	flushBatch(t, db, src, format, now, batch2)

	after := readRollups(t, db, "rollup_hourly")
	if len(before) != len(after) {
		t.Fatalf("row count changed on metadata-free SessionUpdated: %d -> %d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("rollup row %+v changed on metadata-free SessionUpdated: %+v -> %+v", k, v, after[k])
		}
	}
}

// TestRefreshRollups_SessionStartedRepairsStubMetadataAndStart pins the twin of
// F1 for applySessionStarted (round-6 P2). When an op references a not-yet-seen
// session, requireSessionID inserts a metadata-EMPTY stub (start_ts = the op's
// ts, empty agent/cwd). A LATER SessionStarted then REPAIRS agent_name/cwd via
// the UPSERT's COALESCE(NULLIF(...)) AND can move start_ts EARLIER via MIN. Both
// invalidate already-materialized rollup rows that applySessionStarted, before
// this fix, never re-marked:
//   - metadata repair → every hour bucket holding one of the session's ops kept
//     STALE empty-agent/empty-cwd dimension rows (the exact class F1 fixed for
//     applySessionUpdated), and
//   - the MIN start-move → the OLD start bucket kept a phantom session_starts and
//     the NEW start bucket lacked it.
//
// The proof is byte-parity with a fresh BackfillRollups over the FINAL session
// state (repaired agent/cwd, earlier start). The batches are ordered so the op
// is rolled up under the empty stub BEFORE the repairing SessionStarted arrives,
// which is the only way to exercise the incremental-only hazard.
func TestRefreshRollups_SessionStartedRepairsStubMetadataAndStart(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"
	now := fixedNow()

	// hOp is BOTH the stub's start (requireSessionID sets start_ts = op ts) and
	// the op's hour bucket; hStart is the repairing SessionStarted's ts, EARLIER
	// than hOp so MIN moves the session start back a bucket.
	hStart := ts(0, 8, 0)
	hOpStart, hOpEnd := ts(0, 10, 0), ts(0, 10, 20)

	// ---- Store A: incremental path. ----
	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)

	// Batch 1: an op for sess-1 with NO prior SessionStarted. applyOpStarted →
	// requireSessionID stubs sess-1 (start_ts = hOpStart, agent/cwd empty), and the
	// op folds into the hOpStart bucket under the empty agent/cwd dimensions.
	batch1 := llmOpEvents(src, "sess-1", 1, 1, hOpStart, hOpEnd, "sonnet", "anthropic", 100, 200, 0.1, false)
	flushBatch(t, dbA, src, format, now, batch1)

	// Premise: the empty stub produced NO agent/cwd dimension rows for the op (the
	// fold emits an agent/cwd row only when the value is non-empty), and the stub's
	// session_starts landed on the op bucket = hOpStart (the stub's start_ts), on
	// the total dimension only. A missing re-mark on repair would leave the op
	// bucket WITHOUT the repaired agent/cwd rows and WITH the phantom start.
	preH := readRollups(t, dbA, "rollup_hourly")
	if _, ok := preH[rollupKey{hOpStart, format, "agent", "claude-x"}]; ok {
		t.Fatal("premise broken: claude-x agent row present before repair (stub should be empty)")
	}
	if v := preH[rollupKey{hOpStart, format, "total", ""}]; v.sessionStarts != 1 {
		t.Fatalf("premise broken: stub session_starts on op bucket = %d, want 1", v.sessionStarts)
	}
	if v := preH[rollupKey{hOpStart, format, "total", ""}]; v.opCount != 1 {
		t.Fatalf("premise broken: op_count on op bucket = %d, want 1", v.opCount)
	}

	// Batch 2: SessionStarted REPAIRS agent/cwd and moves start EARLIER (hStart <
	// hOpStart → MIN). No new ops — only the metadata + start change.
	batch2 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude-x", "/w", hStart, 100),
	}
	flushBatch(t, dbA, src, format, now, batch2)

	// ---- Store B: backfill over the FINAL state (repaired agent/cwd, earlier
	// start). The op keeps its own start bucket. ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude-x", "/w", hStart)
	seedTurn(t, dbB, "turn-1", "sess-1", hStart)
	seedOp(t, dbB, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hOpStart, endTS: &hOpEnd, durationUS: hOpEnd - hOpStart, status: "completed",
		tokensIn: 100, tokensOut: 200, costUSD: 0.1,
	})
	if _, err := BackfillRollups(context.Background(), dbB, now, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	// Byte-parity: the incremental store must match the backfill exactly — op
	// bucket re-attributed to claude-x//w, empty dimensions gone, and the
	// session_starts moved from the stub bucket (hOpStart) to the repaired start
	// bucket (hStart).
	assertRollupsIdentical(t, dbA, dbB)

	// Explicit guards so a regression names the exact failure, not just "diff".
	postH := readRollups(t, dbA, "rollup_hourly")
	// The op bucket must now carry the repaired agent/cwd dimension rows (metadata
	// repair re-folded the op), which were absent under the empty stub.
	if _, ok := postH[rollupKey{hOpStart, format, "agent", "claude-x"}]; !ok {
		t.Fatal("claude-x agent row missing in op bucket — op bucket not re-attributed on stub repair")
	}
	if _, ok := postH[rollupKey{hOpStart, format, "cwd", "/w"}]; !ok {
		t.Fatal("/w cwd row missing in op bucket — op bucket not re-attributed on stub repair")
	}
	// session_starts must MOVE from the old stub bucket (hOpStart) to the new start
	// bucket (hStart) once start_ts is pulled earlier by MIN.
	if v := postH[rollupKey{hStart, format, "total", ""}]; v.sessionStarts != 1 {
		t.Fatalf("new start bucket total session_starts = %d, want 1 (start-move not re-marked)", v.sessionStarts)
	}
	if _, ok := postH[rollupKey{hStart, format, "agent", "claude-x"}]; !ok {
		t.Fatal("new start bucket missing claude-x agent session_starts row (start-move not re-marked)")
	}
	if v := postH[rollupKey{hOpStart, format, "total", ""}]; v.sessionStarts != 0 {
		t.Fatalf("old stub bucket total session_starts = %d, want 0 (phantom start not cleared)", v.sessionStarts)
	}
}

// TestRefreshRollups_OpenDayMaterializedAfterMidnight pins the DAILY sibling of
// the round-7 hourly carry-forward bug (round-8 P1). The hourly fix carries the
// open HOUR forward; days, however, were DERIVED per-refresh from the dirty
// hours. So a day D whose ops all arrive during one of its hours H, where H then
// closes and is materialized (leaving the carried HOUR set) while D is STILL the
// open day, loses its only handle: the carried hour set drains, the daily
// recompute is skipped (D open), and after midnight — with no further events —
// NOTHING materializes rollup_daily for D. The all-sources bucket=daily fast path
// (the Stats UI default) reads closed days only from rollup_daily, so D silently
// undercounts.
//
// The fix mirrors the hourly carry-forward for days: every rollup-affecting event
// marks BOTH its hour AND its day in independent writer-lifetime carried sets, and
// the idle tick stays armed while EITHER pending hours OR pending days remain. So
// D stays tracked after its hours materialize, and is materialized on the first
// refresh after D closes. The proof is byte-parity with a fresh BackfillRollups at
// the final now (which sees D closed and materializes it).
//
// Sequence (the precise open→closed gap the carried day must survive):
//   - Batch 1 at now=day0 10:10: op in hour H=day0 10:00. H is the OPEN hour and
//     day0 is the OPEN day → neither materializes; both are carried.
//   - Refresh-only at now=day0 11:01: H (10:00) is now CLOSED → materialized and
//     promoted OUT of the carried HOUR set. day0 is STILL OPEN → its daily row is
//     skipped. Under the OLD (derive-days-from-hours) code the carried HOUR set is
//     now empty, so day0 has no handle left.
//   - Refresh-only at now=day1 00:01: day0 is now CLOSED. OLD code: dirty set empty
//     → refreshRollups returns early and day0 is never written (RED). NEW code: day0
//     was independently carried, is closed now → materialized (GREEN).
func TestRefreshRollups_OpenDayMaterializedAfterMidnight(t *testing.T) {
	const src = "claude_code:/loc"
	const format = "claude_code"

	hourH := ts(0, 10, 0)      // op hour on day0.
	hourHEnd := ts(0, 10, 30)  //
	day0 := ts(0, 0, 0)        // the day that must not be stranded.
	nowBatch1 := ts(0, 10, 10) // open hour=10:00, open day=day0 → H & day0 carried.
	nowHourClosed := ts(0, 11, 1)
	nowAfterMidnight := ts(1, 0, 1) // day0 now closed; day1 00:00 is the open day.

	// ---- Store A: incremental path, ONE writer reused across batches+idle passes. ----
	_, dbA := openTestStore(t)
	seedSource(t, dbA, src, format)
	clk := &mutableClock{now: nowBatch1}
	wr := newWriter(src, format, "/loc", NopPricer{})
	wr.now = clk.Now

	// Batch 1: session + op entirely within the open hour H of the open day day0.
	batch1 := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/work/proj", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	batch1 = append(batch1, llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "sonnet", "anthropic", 10, 20, 0.1, false)...)
	flushBatchReuse(t, dbA, wr, src, format, batch1)

	// Premise: open hour H and open day day0 must NOT be materialized at batch 1.
	if _, ok := readRollups(t, dbA, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; ok {
		t.Fatal("premise broken: open hour H materialized at batch 1")
	}
	if _, ok := readRollups(t, dbA, "rollup_daily")[rollupKey{day0, format, "total", ""}]; ok {
		t.Fatal("premise broken: open day day0 materialized at batch 1")
	}

	// Idle pass 1 at now=day0 11:01: H closes (materialize+promote out of the HOUR
	// set) while day0 stays OPEN. This is the step that, under the old code, dropped
	// day0's only handle.
	clk.Set(nowHourClosed)
	refreshOnlyReuse(t, dbA, wr)

	if _, ok := readRollups(t, dbA, "rollup_hourly")[rollupKey{hourH, format, "total", ""}]; !ok {
		t.Fatal("hour H missing from rollup_hourly after it closed")
	}
	if _, ok := readRollups(t, dbA, "rollup_daily")[rollupKey{day0, format, "total", ""}]; ok {
		t.Fatal("premise broken: day0 materialized while still the open day")
	}
	// THE FIX'S CORE INVARIANT: day0 must still be carried (so the idle timer stays
	// armed) even though the carried HOUR set is now empty.
	if !wr.hasPendingRollupBuckets() {
		t.Fatal("day0 dropped from the carried set after its hour materialized while the day was still open — round-8 P1 (idle timer would stop and day0 would never materialize)")
	}

	// Idle pass 2 at now=day1 00:01: day0 is now closed, NO new events. The carried
	// day must materialize.
	clk.Set(nowAfterMidnight)
	refreshOnlyReuse(t, dbA, wr)

	if _, ok := readRollups(t, dbA, "rollup_daily")[rollupKey{day0, format, "total", ""}]; !ok {
		t.Fatal("day0 missing from rollup_daily after midnight — carried-day drop (round-8 P1 undercount)")
	}

	// ---- Store B: backfill over the SAME data at the FINAL now (day0 closed). ----
	_, dbB := openTestStore(t)
	seedSource(t, dbB, src, format)
	seedSession(t, dbB, "sess-1", src, "claude", "/work/proj", hourH)
	seedTurn(t, dbB, "turn-1", "sess-1", hourH)
	seedOp(t, dbB, opSpec{
		id: "op-1", turnID: "turn-1", sessionID: "sess-1", seq: 1,
		kind: "llm", name: "chat", model: "sonnet", provider: "anthropic",
		startTS: hourH, endTS: &hourHEnd, durationUS: hourHEnd - hourH, status: "completed",
		tokensIn: 10, tokensOut: 20, costUSD: 0.1,
	})
	if _, err := BackfillRollups(context.Background(), dbB, nowAfterMidnight, silentLogger()); err != nil {
		t.Fatalf("BackfillRollups: %v", err)
	}

	// The incremental store (carried day + hour) must be byte-identical to the
	// backfill at the final now: H materialized in rollup_hourly, day0 materialized
	// in rollup_daily.
	assertRollupsIdentical(t, dbA, dbB)
}
