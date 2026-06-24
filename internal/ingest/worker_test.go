package ingest

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// TestWorker_CancelDrainsPendingBatch constructs the worker directly and
// cancels its run context while one event is already buffered in the in-memory
// batch. The batch size and interval cannot flush it first; persistence proves
// the ctx.Done shutdown path ran the final drain/flush.
func TestWorker_CancelDrainsPendingBatch(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ch := make(chan canonical.Event, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	w := newRuntimeTestWorker(db, src, "aiagent_v3", "/tmp")
	w.events = ch

	go func() {
		w.run(ctx)
		close(done)
	}()

	ch <- canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "cancel-drain",
		RootNativeID: "cancel-drain",
		Kind:         canonical.KindRoot,
	}

	if !waitFor(2*time.Second, func() bool {
		return len(ch) == 0
	}) {
		cancel()
		t.Fatal("worker did not consume the event into its pending batch")
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='cancel-drain'`); got != 0 {
		cancel()
		t.Fatalf("session persisted before cancellation = %d, want 0", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after context cancellation")
	}

	assertWorkerSessionProgress(t, db, w, src, "cancel-drain", 1, "cancel drain")
}

func newRuntimeTestWorker(db *sql.DB, src, format, location string) *worker {
	return &worker{
		sourceID:      src,
		sourceFormat:  format,
		location:      location,
		fts5IndexLogs: true,
		db:            db,
		hwm:           newHWMCache(),
		pricer:        NopPricer{},
		logger:        silentLogger(),
		batchSize:     100,
		batchEvery:    time.Hour,
		now:           fixedNow,
	}
}

func assertWorkerSessionProgress(t *testing.T, db *sql.DB, w *worker, src, nativeID string, wantSeq int64, label string) {
	t.Helper()
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id=?`, nativeID); got != 1 {
		t.Fatalf("session rows after %s = %d, want 1", label, got)
	}
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id=?`, src); got != wantSeq {
		t.Fatalf("source_progress.last_seq after %s = %d, want %d", label, got, wantSeq)
	}
	if got := w.hwm.Get(src); got != uint64(wantSeq) {
		t.Fatalf("worker HWM after %s = %d, want %d", label, got, wantSeq)
	}
}

// TestWorker_CancelDrainsBufferedChannel starts run with an already-canceled
// context and events still buffered in the source channel. Regardless of whether
// select first receives an event or the ctx.Done branch, shutdown must persist
// every buffered event and return without the producer closing the channel.
func TestWorker_CancelDrainsBufferedChannel(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	ch := make(chan canonical.Event, 4)
	for n := 1; n <= 3; n++ {
		nativeID := string(rune('a' - 1 + n))
		ch <- canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: src, SourceSeq: uint64(n), Ts: int64(n * 1000)},
			NativeID:     nativeID,
			RootNativeID: nativeID,
			Kind:         canonical.KindRoot,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	w := newRuntimeTestWorker(db, src, "aiagent_v3", "/tmp")
	w.events = ch

	go func() {
		w.run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit from already-canceled context")
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != 3 {
		t.Fatalf("session rows after canceled-context drain = %d, want 3", got)
	}
	assertWorkerProgress(t, db, w, src, 3, "canceled-context drain")
}

func assertWorkerProgress(t *testing.T, db *sql.DB, w *worker, src string, wantSeq int64, label string) {
	t.Helper()
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id=?`, src); got != wantSeq {
		t.Fatalf("source_progress.last_seq after %s = %d, want %d", label, got, wantSeq)
	}
	if got := w.hwm.Get(src); got != uint64(wantSeq) {
		t.Fatalf("worker HWM after %s = %d, want %d", label, got, wantSeq)
	}
}

// TestWorkerRuntime_FlushBatchUsesShutdownDrainAfterLifecycleCancel pins the
// branch where a size or interval flush is reached after the lifecycle context
// is already canceled. That branch must switch to the bounded shutdown-drain
// context before opening the transaction, so the pending batch is not lost to a
// canceled parent context.
func TestWorkerRuntime_FlushBatchUsesShutdownDrainAfterLifecycleCancel(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	var errs []error
	w := newRuntimeTestWorker(db, src, "aiagent_v3", "/tmp")
	w.onErr = func(err error) {
		errs = append(errs, err)
	}
	rt := newWorkerRuntime(w)
	defer rt.close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rt.appendEvent(canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "canceled-branch-drain",
		RootNativeID: "canceled-branch-drain",
		Kind:         canonical.KindRoot,
	})
	rt.flushBatch(ctx, "size after cancel")

	if len(errs) > 0 {
		t.Fatalf("shutdown-drain flush reported error: %v", errs[0])
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='canceled-branch-drain'`); got != 1 {
		t.Fatalf("session rows after shutdown-drain flush = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id=?`, src); got != 1 {
		t.Fatalf("source_progress.last_seq after shutdown-drain flush = %d, want 1", got)
	}
	if got := w.hwm.Get(src); got != 1 {
		t.Fatalf("worker HWM after shutdown-drain flush = %d, want 1", got)
	}
}

// TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace pins the
// time-of-check/time-of-use shutdown race where writeContext observes an active
// lifecycle context, then shutdown cancellation arrives before SQL starts. Once
// an event is accepted into the workerRuntime batch, that event must not be
// dropped because the lifecycle context flips during the flush handoff.
func TestWorkerRuntime_FlushBatchSurvivesLifecycleCancellationRace(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	var errs []error
	w := newRuntimeTestWorker(db, src, "aiagent_v3", "/tmp")
	w.onErr = func(err error) {
		errs = append(errs, err)
	}
	rt := newWorkerRuntime(w)
	defer rt.close()

	rt.appendEvent(canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "size-cancellation-race",
		RootNativeID: "size-cancellation-race",
		Kind:         canonical.KindRoot,
	})
	ctx, cancel := context.WithCancel(context.Background())
	writeCtx, cancelWrite := rt.writeContext(ctx)
	defer cancelWrite()
	cancel()
	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("parent context Err() = %v, want %v", err, context.Canceled)
	}

	rt.flushBatchWithWriteContext(writeCtx, "size cancellation race")

	if len(errs) > 0 {
		t.Errorf("size flush reported error after lifecycle cancellation race: %v", errs[0])
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='size-cancellation-race'`); got != 1 {
		t.Errorf("session rows after lifecycle cancellation race = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT IFNULL(MAX(last_seq), -1) FROM source_progress WHERE source_id=?`, src); got != 1 {
		t.Errorf("source_progress.last_seq after lifecycle cancellation race = %d, want 1", got)
	}
	if got := w.hwm.Get(src); got != 1 {
		t.Errorf("worker HWM after lifecycle cancellation race = %d, want 1", got)
	}
}

func TestWorkerRuntime_RecoveredFlushRetryDoesNotInvokeOnErr(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	var errs []error
	w := newRuntimeTestWorker(db, src, "aiagent_v3", "/tmp")
	w.onErr = func(err error) {
		errs = append(errs, err)
	}
	rt := newWorkerRuntime(w)
	defer rt.close()

	rt.appendEvent(canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "retry-then-success",
		RootNativeID: "retry-then-success",
		Kind:         canonical.KindRoot,
	})
	attempts := 0
	rt.flush = func(context.Context, *writer, []canonical.Event) error {
		attempts++
		if attempts == 1 {
			return errors.New("transient flush failure")
		}
		return nil
	}

	rt.flushBatchWithWriteContext(context.Background(), "test retry")

	if attempts != 2 {
		t.Fatalf("flush attempts = %d, want one retry then success", attempts)
	}
	if len(errs) > 0 {
		t.Fatalf("recovered retry invoked onErr: %v", errs[0])
	}
	if len(rt.batch) != 0 {
		t.Fatalf("batch len after recovered retry = %d, want 0", len(rt.batch))
	}
}

// TestDetachedWriteContext pins the unbounded context.WithoutCancel risk: active
// writes detach from lifecycle cancellation, but shutdown must still arm the
// bounded drain timeout while preserving parent context values.
func TestDetachedWriteContext(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	key := contextKey{}
	parentValue := "preserved-value"
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key, parentValue))

	grace := 500 * time.Millisecond
	writeCtx, cancelWrite := detachedWriteContext(parent, grace)
	defer cancelWrite()

	if got := writeCtx.Value(key); got != parentValue {
		t.Fatalf("write context value = %v, want %v", got, parentValue)
	}

	graceStarted := time.Now()
	cancelParent()
	probeContextNotCanceledBeforeGrace(t, writeCtx, graceStarted, grace, "write context canceled before bounded shutdown grace elapsed after parent cancellation")

	waitForContextDoneAfterGrace(t, writeCtx, graceStarted, grace, "write context canceled before bounded shutdown grace elapsed after parent cancellation")
	if err := writeCtx.Err(); err == nil {
		t.Fatal("write context Err() is nil after Done closed")
	}
}

// TestDetachedWriteContextParentDeadlineStartsShutdownGrace pins parent
// deadline expiry as a lifecycle signal, not as the active write deadline.
// The write context must survive the parent deadline long enough to use the
// bounded shutdown grace, then close when that grace expires.
func TestDetachedWriteContextParentDeadlineStartsShutdownGrace(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	key := contextKey{}
	parentValue := "preserved-deadline-value"
	grace := 500 * time.Millisecond
	parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), key, parentValue), 20*time.Millisecond)
	defer cancelParent()
	deadlineAt, ok := parent.Deadline()
	if !ok {
		t.Fatal("parent context has no deadline")
	}

	writeCtx, cancelWrite := detachedWriteContext(parent, grace)
	defer cancelWrite()

	if got := writeCtx.Value(key); got != parentValue {
		t.Fatalf("write context value = %v, want %v", got, parentValue)
	}

	select {
	case <-parent.Done():
	case <-time.After(time.Second):
		t.Fatal("parent context deadline did not expire")
	}
	if err := parent.Err(); err != context.DeadlineExceeded {
		t.Fatalf("parent Err() = %v, want %v", err, context.DeadlineExceeded)
	}

	probeContextNotCanceledBeforeGrace(t, writeCtx, deadlineAt, grace, "write context canceled before bounded shutdown grace elapsed after parent deadline")

	waitForContextDoneAfterGrace(t, writeCtx, deadlineAt, grace, "write context canceled before bounded shutdown grace elapsed after parent deadline")
	if err := writeCtx.Err(); err == nil {
		t.Fatal("write context Err() is nil after Done closed")
	}
}

func probeContextNotCanceledBeforeGrace(t *testing.T, ctx context.Context, graceStarted time.Time, grace time.Duration, msg string) {
	t.Helper()
	select {
	case <-ctx.Done():
		if time.Since(graceStarted) < grace {
			t.Fatal(msg)
		}
	default:
	}
}

func waitForContextDoneAfterGrace(t *testing.T, ctx context.Context, graceStarted time.Time, grace time.Duration, msg string) {
	t.Helper()
	select {
	case <-ctx.Done():
		if time.Since(graceStarted) < grace {
			t.Fatal(msg)
		}
	case <-time.After(grace + time.Second):
		t.Fatal("write context did not close after bounded shutdown grace")
	}
}

// TestWorkerRuntime_HandleCloseUsesShutdownDrainForFinalFlush pins the
// producer-channel-close path. Even when the lifecycle context is still active
// at the branch point, the final flush must use the bounded shutdown-drain
// context because parent cancellation can race the close before SQL work starts.
func TestWorkerRuntime_HandleCloseUsesShutdownDrainForFinalFlush(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"

	_, db := openTestStore(t)
	var errs []error
	w := &worker{
		sourceID:      src,
		sourceFormat:  "aiagent_v3",
		location:      "/tmp",
		fts5IndexLogs: true,
		db:            db,
		hwm:           newHWMCache(),
		pricer:        NopPricer{},
		logger:        silentLogger(),
		batchSize:     100,
		batchEvery:    time.Hour,
		now:           fixedNow,
		onErr: func(err error) {
			errs = append(errs, err)
		},
	}
	rt := newWorkerRuntime(w)
	defer rt.close()

	rt.appendEvent(canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "close-branch-drain",
		RootNativeID: "close-branch-drain",
		Kind:         canonical.KindRoot,
	})
	rt.handleClose(context.Background())

	if len(errs) > 0 {
		t.Fatalf("shutdown-drain final flush reported error: %v", errs[0])
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='close-branch-drain'`); got != 1 {
		t.Fatalf("session rows after close final flush = %d, want 1", got)
	}
	if got := scanInt(t, db, `SELECT last_seq FROM source_progress WHERE source_id=?`, src); got != 1 {
		t.Fatalf("source_progress.last_seq after close final flush = %d, want 1", got)
	}
	if got := w.hwm.Get(src); got != 1 {
		t.Fatalf("worker HWM after close final flush = %d, want 1", got)
	}
	if rt.shutdownDrainCtx == nil {
		t.Fatalf("handleClose final flush used lifecycle context; want bounded shutdown-drain context")
	}
}

// TestWorkerRuntime_CanceledParentShutdownIdleRefreshMaterializesClosedBuckets
// pins the shutdown idle-refresh path. A canceled parent must select the bounded
// shutdown-drain context before refreshing carried rollup buckets, so closed
// hour/day buckets are materialized instead of being lost to parent cancellation.
func TestWorkerRuntime_CanceledParentShutdownIdleRefreshMaterializesClosedBuckets(t *testing.T) {
	t.Parallel()
	const src = "claude_code:/loc"
	const format = "claude_code"

	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	day0 := ts(0, 0, 0)
	clk := &mutableClock{now: ts(0, 10, 10)} // open hour/day -> carried, not materialized.
	var errs []error
	w := newRuntimeTestWorker(db, src, format, "/loc")
	w.batchSize = 1000
	w.now = clk.Now
	w.onErr = func(err error) {
		errs = append(errs, err)
	}
	rt := newWorkerRuntime(w)
	defer rt.close()

	events := []canonical.Event{
		sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1),
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
			SessionNativeID: "sess-1", Seq: 1,
		},
	}
	events = append(events, llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "m", "p", 1, 1, 0, false)...)
	for _, ev := range events {
		rt.appendEvent(ev)
	}
	rt.flushBatch(context.Background(), "seed carried rollups")

	assertRuntimeCarriedRollups(t, db, rt, errs, hourH, day0)

	clk.Set(ts(1, 0, 1)) // close both the carried hour and day.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt.handleCancel(ctx)

	assertShutdownIdleRefreshMaterialized(t, db, rt, errs, hourH, day0)
}

func assertRuntimeCarriedRollups(t *testing.T, db *sql.DB, rt *workerRuntime, errs []error, hourH, day0 int64) {
	t.Helper()
	if len(errs) > 0 {
		t.Fatalf("seed flush reported error: %v", errs[0])
	}
	if !rt.writer.hasPendingRollupBuckets() {
		t.Fatal("expected open hour/day carried after seed flush")
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH); got != 0 {
		t.Fatalf("open hour materialized during seed flush = %d, want 0", got)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_daily WHERE bucket_ts=? AND dimension='total'`, day0); got != 0 {
		t.Fatalf("open day materialized during seed flush = %d, want 0", got)
	}
}

func assertShutdownIdleRefreshMaterialized(t *testing.T, db *sql.DB, rt *workerRuntime, errs []error, hourH, day0 int64) {
	t.Helper()
	if len(errs) > 0 {
		t.Fatalf("shutdown idle refresh reported error: %v", errs[0])
	}
	if rt.shutdownDrainCtx == nil {
		t.Fatal("shutdown idle refresh used lifecycle context; want bounded shutdown-drain context")
	}
	assertRollupRowCount(t, db, "rollup_hourly", hourH, 1, "shutdown idle refresh hourly")
	assertRollupRowCount(t, db, "rollup_daily", day0, 1, "shutdown idle refresh daily")
	if rt.writer.hasPendingRollupBuckets() {
		t.Fatal("carried rollup buckets still pending after shutdown idle refresh")
	}
}

func assertRollupRowCount(t *testing.T, db *sql.DB, table string, bucket int64, want int64, label string) {
	t.Helper()
	var query string
	switch table {
	case "rollup_hourly":
		query = `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`
	case "rollup_daily":
		query = `SELECT COUNT(*) FROM rollup_daily WHERE bucket_ts=? AND dimension='total'`
	default:
		t.Fatalf("unsupported rollup table %q", table)
	}
	if got := scanInt(t, db, query, bucket); got != want {
		t.Fatalf("%s rows = %d, want %d", label, got, want)
	}
}

// TestWorker_LowSeqEventsNotDropped pins SOW-0015's new contract: the
// per-source last_seq counter is NOT a dedup gate. Events whose
// SourceSeq is at or below a previously-persisted last_seq still flow to
// the writer (one sourceID aggregates many per-file sequences, so a
// scalar watermark cannot dedup). All four sessions persist, and the
// counter advances to the batch maximum. SQL-layer idempotency — not a
// watermark — prevents duplicate rows on re-scan (see
// TestWorker_ReScanIdempotency).
func TestWorker_LowSeqEventsNotDropped(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	// Seed source_progress so the observability counter starts at 10.
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO sources (id, format, location, created_at) VALUES ('aiagent_v3:/tmp', 'aiagent_v3', '/tmp', 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES ('aiagent_v3:/tmp', 10, 0, 0)`); err != nil {
		t.Fatalf("seed sp: %v", err)
	}

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(4),
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
	// SourceSeq=5 — at/below the seeded counter; MUST still be written.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 5, Ts: 500},
		NativeID:  "below-counter", RootNativeID: "below-counter", Kind: canonical.KindRoot,
	}
	// SourceSeq=10 — equal to the seeded counter; MUST still be written.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 10, Ts: 1000},
		NativeID:  "equal-counter", RootNativeID: "equal-counter", Kind: canonical.KindRoot,
	}
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 11, Ts: 1100},
		NativeID:  "above1", RootNativeID: "above1", Kind: canonical.KindRoot,
	}
	// SourceSeq=12 — trips batchSize=4.
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 12, Ts: 1200},
		NativeID:  "above2", RootNativeID: "above2", Kind: canonical.KindRoot,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions`) == 4
	}) {
		t.Fatalf("low-seq events were dropped; sessions=%d, want 4", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}
	if scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='below-counter'`) != 1 {
		t.Errorf("event below the counter was dropped (SOW-0015 regression)")
	}
	if got := i.HWM("aiagent_v3:/tmp"); got != 12 {
		t.Errorf("observability counter after batch = %d, want 12", got)
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

func TestIngesterStopReturnsWorkerErrors(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()), WithBatchSize(1), WithBatchInterval(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := make(chan canonical.Event, 1)
	if err := i.Submit("aiagent_v3:/tmp", events); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	events <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 0},
	}
	close(events)

	err = i.Stop()
	if err == nil {
		t.Fatal("Stop returned nil, want worker batch error")
	}
	if !strings.Contains(err.Error(), "DROPPING") {
		t.Fatalf("Stop error = %v, want dropped-batch context", err)
	}
}

// TestWorker_IdleTickMaterializesClosedBucket pins the round-7 idle
// materialization tick (Part 2). Ops arrive entirely within the OPEN hour H, then
// ingestion goes quiet (no further events). When the injected clock advances past
// H's close, the worker's idle tick must run a refresh-only pass and materialize H
// into rollup_hourly WITHOUT any new event — driving the real worker.run timer.
// The timer must keep firing while a bucket is pending (open at last flush) and
// self-terminate once the carried set drains.
func TestWorker_IdleTickMaterializesClosedBucket(t *testing.T) {
	t.Parallel()
	const src = "claude_code:/loc"
	const format = "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	clk := &mutableClock{now: ts(0, 10, 10)} // open hour = H → H carried, not materialized.

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(1000),                    // never trip on size
		WithBatchInterval(20*time.Millisecond), // short idle tick
		WithNow(clk.Now),
		WithSourceFormat(src, format),
		WithLocation(src, "/loc"),
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
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	// One op entirely inside the open hour H, then NO further events.
	ch <- sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1)
	ch <- canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
		SessionNativeID: "sess-1", Seq: 1,
	}
	for _, ev := range llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "m", "p", 1, 1, 0, false) {
		ch <- ev
	}

	// Wait for the first interval flush to commit the op (H open → not materialized).
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops`) == 1
	}) {
		t.Fatalf("first flush did not commit the op; ops=%d", scanInt(t, db, `SELECT COUNT(*) FROM ops`))
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH); got != 0 {
		t.Fatalf("open hour H materialized before it closed (count=%d) — must wait for close", got)
	}

	// Close H by advancing the clock; the idle tick (no new events) must materialize it.
	clk.Set(ts(0, 11, 1))
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH) == 1
	}) {
		t.Fatalf("idle tick did not materialize closed hour H; rollup_hourly total rows for H=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH))
	}
}

// TestWorker_IdleTickMaterializesClosedDayAfterMidnight pins the DAILY sibling
// (round-8 P1) end-to-end through the real worker.run idle loop. Ops arrive
// entirely within hour H of day0 while day0 is the open day, then ingestion goes
// quiet. The clock first advances past H's close (the hour materializes and leaves
// the carried hour set while day0 stays open), then past midnight (day0 closes).
// The worker's idle timer MUST stay armed across the hour→day transition — i.e.
// hasPendingRollupBuckets must keep returning true while only a DAY is pending —
// and materialize day0 into rollup_daily WITHOUT any new event. Before the fix the
// timer stopped once the carried hour set drained and day0 was never written.
func TestWorker_IdleTickMaterializesClosedDayAfterMidnight(t *testing.T) {
	t.Parallel()
	const src = "claude_code:/loc"
	const format = "claude_code"
	_, db := openTestStore(t)
	seedSource(t, db, src, format)

	hourH := ts(0, 10, 0)
	hourHEnd := ts(0, 10, 30)
	day0 := ts(0, 0, 0)
	clk := &mutableClock{now: ts(0, 10, 10)} // open hour=10:00, open day=day0 → both carried.

	i, err := New(db,
		WithLogger(silentLogger()),
		WithBatchSize(1000),                    // never trip on size
		WithBatchInterval(20*time.Millisecond), // short idle tick
		WithNow(clk.Now),
		WithSourceFormat(src, format),
		WithLocation(src, "/loc"),
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
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	// One op entirely inside the open hour H of the open day day0, then NO further events.
	ch <- sessionStartEvent(src, "sess-1", "claude", "/w", hourH, 1)
	ch <- canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: hourH},
		SessionNativeID: "sess-1", Seq: 1,
	}
	for _, ev := range llmOpEvents(src, "sess-1", 1, 1, hourH, hourHEnd, "m", "p", 1, 1, 0, false) {
		ch <- ev
	}

	// First interval flush commits the op (H open, day0 open → neither materialized).
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM ops`) == 1
	}) {
		t.Fatalf("first flush did not commit the op; ops=%d", scanInt(t, db, `SELECT COUNT(*) FROM ops`))
	}

	// Step 1: close H (day0 still open). The idle tick materializes H; day0 must NOT
	// appear yet, and the timer must stay armed because the DAY is still pending.
	clk.Set(ts(0, 11, 1))
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH) == 1
	}) {
		t.Fatalf("idle tick did not materialize closed hour H; rollup_hourly total rows for H=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM rollup_hourly WHERE bucket_ts=? AND dimension='total'`, hourH))
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM rollup_daily WHERE bucket_ts=? AND dimension='total'`, day0); got != 0 {
		t.Fatalf("open day day0 materialized while still open (count=%d) — must wait for midnight", got)
	}

	// Step 2: cross midnight (day0 closes). The idle tick — still armed because a DAY
	// was pending after the hour drained — must materialize day0 into rollup_daily
	// with NO new events. This is the round-8 P1 proof: before the fix the timer had
	// stopped and day0 was stranded.
	clk.Set(ts(1, 0, 1))
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM rollup_daily WHERE bucket_ts=? AND dimension='total'`, day0) == 1
	}) {
		t.Fatalf("idle tick did not materialize closed day day0 after midnight; rollup_daily total rows for day0=%d",
			scanInt(t, db, `SELECT COUNT(*) FROM rollup_daily WHERE bucket_ts=? AND dimension='total'`, day0))
	}
}

// TestWorker_FlushPromotesPendingMissDedupAfterCommit pins that
// the rollback/dedup writer-level tests call promotePendingMissDedup
// manually, so a developer who removed wr.promotePendingMissDedup from
// worker.flush would still see them pass. This test
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
	// runs and copies the staged entry into the lifetime map.
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
		t.Errorf("expected 1 WRN row after two committed batches, got %d (worker.flush must call promotePendingMissDedup after tx.Commit)", got)
	}
	// parse_errors must also stay at 1 — emitPricingMiss bumps it
	// alongside the WRN insert, and the dedup must suppress both.
	if got := scanInt(t, db, `SELECT parse_errors FROM sources WHERE id='aiagent_v3:/tmp'`); got != 1 {
		t.Errorf("expected parse_errors=1 after two committed batches, got %d", got)
	}
}
