package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestWorker_FlushesAtBatchSize covers the size-based flush path: the
// worker accumulates up to batchSize events before opening a tx, so two
// distinct flushes land in the store when 2*batchSize events are sent.
func TestWorker_FlushesAtBatchSize(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2),
		WithBatchInterval(10*time.Second), // ensure size triggers, not timer
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 8)
	for n := 1; n <= 4; n++ {
		ch <- canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: uint64(n), Ts: int64(n * 100)},
			NativeID:  string(rune('a' - 1 + n)), RootNativeID: string(rune('a' - 1 + n)),
			Kind: canonical.KindRoot,
		}
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) == 4
	}) {
		t.Fatalf("expected 4 sessions; got %d", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}
	close(ch)
}

// TestWorker_FlushesAtInterval covers the time-based flush path: the
// worker waits batchInterval between the first event arriving and the
// flush firing.
func TestWorker_FlushesAtInterval(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(1000), // never trip on size
		WithBatchInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 4)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 100},
		NativeID:  "a", RootNativeID: "a", Kind: canonical.KindRoot,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) == 1
	}) {
		t.Fatalf("interval flush did not fire; sessions=%d", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}
}

// TestWorker_DedupHWMDropsReplays verifies the HWM-based dedup.
func TestWorker_DedupHWMDropsReplays(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	// Seed source_progress so HWM=10 is already in place.
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sources (id, format, location, created_at) VALUES ('aiagent_v3:/tmp', 'aiagent_v3', '/tmp', 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES ('aiagent_v3:/tmp', 10, 0, 0)`); err != nil {
		t.Fatalf("seed sp: %v", err)
	}

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2),
		WithBatchInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 8)
	// SourceSeq=5 — below HWM, dropped.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 5, Ts: 500},
		NativeID:  "dropped", RootNativeID: "dropped", Kind: canonical.KindRoot,
	}
	// SourceSeq=10 — equal to HWM, dropped.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 10, Ts: 1000},
		NativeID:  "also-dropped", RootNativeID: "also-dropped", Kind: canonical.KindRoot,
	}
	// SourceSeq=11 — written.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 11, Ts: 1100},
		NativeID:  "kept", RootNativeID: "kept", Kind: canonical.KindRoot,
	}
	// SourceSeq=12 — written; trips batchSize=2.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 12, Ts: 1200},
		NativeID:  "kept2", RootNativeID: "kept2", Kind: canonical.KindRoot,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) == 2
	}) {
		t.Fatalf("dedup did not run; sessions=%d", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}
	if scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='dropped'`) != 0 {
		t.Errorf("dropped event was written")
	}
	if got := i.HWM("aiagent_v3:/tmp"); got != 12 {
		t.Errorf("HWM after batch = %d, want 12", got)
	}
}

// TestWorker_SourceProgressUpdatesCursor verifies the cursor JSON makes
// it to source_progress.
func TestWorker_SourceProgressUpdatesCursor(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(2),
		WithBatchInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 4)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	ch <- canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 0, Ts: 1500},
		Cursor:    `{"file":"a.jsonl","offset":42}`,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM source_progress WHERE source_id='aiagent_v3:/tmp'`) > 0
	}) {
		t.Fatalf("source_progress row not created")
	}
	v := scanString(t, db, `SELECT IFNULL(cursor,'') FROM source_progress WHERE source_id='aiagent_v3:/tmp'`)
	if v != `{"file":"a.jsonl","offset":42}` {
		t.Fatalf("cursor not persisted; got %q", v)
	}
}

// TestWorker_EnsureSourceRowCreated verifies the sources row appears on
// first batch.
func TestWorker_EnsureSourceRowCreated(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 2)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sources WHERE id='aiagent_v3:/tmp'`) == 1
	}) {
		t.Fatalf("sources row not created")
	}
	if got := scanString(t, db, `SELECT format FROM sources WHERE id='aiagent_v3:/tmp'`); got != "aiagent_v3" {
		t.Errorf("format = %q, want aiagent_v3", got)
	}
}

// TestWorker_OnErrCallbackFires asserts that batch-level failures route
// through the configured onError callback.
func TestWorker_OnErrCallbackFires(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	errs := make(chan error, 1)
	// Construct worker directly so we can wire onErr.
	w := &worker{
		sourceID: "aiagent_v3:/tmp", sourceFormat: "aiagent_v3", location: "/tmp",
		db: db, hwm: i.hwm, pricer: NopPricer{}, logger: silentLogger(),
		batchSize: 1, batchEvery: time.Second,
		onErr: func(err error) {
			select {
			case errs <- err:
			default:
			}
		},
	}
	// Force a failure: empty native id.
	wr := newWriter(w.sourceID, w.sourceFormat, w.location, w.pricer)
	tx, _ := db.BeginTx(ctx, nil)
	err = wr.apply(ctx, tx, canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: w.sourceID, SourceSeq: 1, Ts: 0},
		// NativeID intentionally empty.
	})
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("expected error on empty NativeID")
	}
	// Drive onErr through the worker.report path.
	w.report(err)
	select {
	case got := <-errs:
		if got == nil {
			t.Errorf("expected non-nil error from onErr")
		}
	case <-time.After(time.Second):
		t.Fatal("onErr not invoked")
	}
}

// TestWorker_FlushPromotesPendingMissDedupAfterCommit pins codex iter-10
// P2: the rollback/dedup writer-level tests call promotePendingMissDedup
// manually, so a developer who removed wr.promotePendingMissDedup from
// worker.flush (worker.go:204) would still see them pass. This test
// drives the *worker* end-to-end for two batches that each carry the
// SAME missing (provider, model) tuple; only ONE WRN row may land,
// proving the lifetime dedup map was populated by the worker's
// post-commit promotion call. Mutation check (verified iter-11):
// commenting out wr.promotePendingMissDedup() fails this test with
// "expected 1 WRN row after two committed batches, got 2".
func TestWorker_FlushPromotesPendingMissDedupAfterCommit(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	pricer := &fakeDetailPricer{miss: "unknown_provider_model"}
	i, err := New(db,
		WithLogger(silentLogger()),
		WithPricer(pricer),
		WithBatchSize(3),                  // one batch per triplet of events
		WithBatchInterval(10*time.Second), // size triggers, not the timer
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 8)
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	// Batch 1: session + op start + op finalized for an unknown
	// (provider, model). The OpFinalized triggers emitPricingMiss
	// which writes ONE WRN row and stages a pendingMissDedup entry.
	// The flush at batchSize=3 commits, then promotePendingMissDedup
	// runs (worker.go:204) and copies the staged entry into the
	// lifetime map.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	ch <- canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 1100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup-vendor", Model: "doesnotexist-1",
	}
	ch <- canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 3, Ts: 1200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 1,
		TokensIn: 100, TokensOut: 50, EndTs: 1200, Status: "completed",
	}

	// Wait for batch 1 to durably commit: one op row and one WRN row.
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops`) == 1 &&
			scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`) == 1
	}) {
		t.Fatalf("batch 1 did not commit; ops=%d WRN=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM ops`),
			scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`))
	}

	// Batch 2: same SESSION, same (provider, model), new op. If the
	// worker dropped its promotePendingMissDedup() call, the lifetime
	// dedup map would still be empty here and emitPricingMiss would
	// write a SECOND WRN row.
	ch <- canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 4, Ts: 2100},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2, ParentOpSeq: -1,
		Kind: canonical.OpLLM, Name: "call",
		Provider: "madeup-vendor", Model: "doesnotexist-1",
	}
	ch <- canonical.OpFinalizedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 5, Ts: 2200},
		SessionNativeID: "s", TurnSeq: 1, Seq: 2,
		TokensIn: 100, TokensOut: 50, EndTs: 2200, Status: "completed",
	}
	// Third event fills the size=3 batch and triggers flush #2.
	ch <- canonical.OpStartedEvent{
		EventBase:       canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 6, Ts: 2300},
		SessionNativeID: "s", TurnSeq: 1, Seq: 3, ParentOpSeq: -1,
		Kind: canonical.OpTool, Name: "noop",
	}

	// Wait for batch 2 to commit (ops count grows from 1 to 3).
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops`) == 3
	}) {
		t.Fatalf("batch 2 did not commit; ops=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM ops`))
	}

	// The exact assertion the mutation test relies on. With the fix,
	// the second flush's writer (the worker reuses the same *writer
	// across batches) already has the (madeup-vendor, doesnotexist-1)
	// key in its lifetime pricingMissDedup map → emitPricingMiss
	// short-circuits → only the batch-1 WRN row exists.
	if got := scanInt(t, db, `SELECT COUNT(*) FROM log_entries WHERE severity='WRN'`); got != 1 {
		t.Errorf("expected 1 WRN row after two committed batches, got %d (worker.flush must call promotePendingMissDedup after tx.Commit; see worker.go:204)", got)
	}
	// parse_errors must also stay at 1 — emitPricingMiss bumps it
	// alongside the WRN insert, and the dedup must suppress both.
	if got := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); got != 1 {
		t.Errorf("expected parse_errors=1 after two committed batches, got %d", got)
	}
}
