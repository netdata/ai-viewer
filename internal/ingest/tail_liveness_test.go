package ingest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestTailHeartbeatPersistsWithThrottle(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	var now atomic.Int64
	now.Store(1_000_000)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithNow(func() int64 { return now.Load() }),
		WithTailLiveness(time.Minute, time.Hour, 30*time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := int64(900_000)
	if err := i.RecordSourceLifecycle(ctx, "src-tail", "codex", "/tmp/src-tail", SourceLifecycleUpdate{
		State:           SourceLifecycleTailing,
		AtUS:            started,
		TailStartedAtUS: &started,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = i.Stop() })

	i.RecordTailHeartbeat("src-tail")
	if !waitFor(time.Second, func() bool {
		return scanInt(t, db, `SELECT IFNULL(tail_heartbeat_at, 0) FROM source_progress WHERE source_id='src-tail'`) == 1_000_000
	}) {
		t.Fatal("first heartbeat was not persisted")
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id='src-tail'`); got != 1 {
		t.Fatalf("source_status_changed rows after tailing heartbeat = %d, want only initial lifecycle transition", got)
	}

	now.Store(11_000_000)
	i.RecordTailHeartbeat("src-tail")
	time.Sleep(50 * time.Millisecond)
	if got := scanInt(t, db, `SELECT IFNULL(tail_heartbeat_at, 0) FROM source_progress WHERE source_id='src-tail'`); got != 1_000_000 {
		t.Fatalf("tail_heartbeat_at = %d, want throttled value 1000000", got)
	}

	now.Store(32_000_000)
	i.RecordTailHeartbeat("src-tail")
	if !waitFor(time.Second, func() bool {
		return scanInt(t, db, `SELECT IFNULL(tail_heartbeat_at, 0) FROM source_progress WHERE source_id='src-tail'`) == 32_000_000
	}) {
		t.Fatal("post-throttle heartbeat was not persisted")
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id='src-tail'`); got != 1 {
		t.Fatalf("source_status_changed rows after second tailing heartbeat = %d, want no heartbeat-only notify", got)
	}
}

func TestTailWatchdogMarksStaleAndRequestsRestart(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	var now atomic.Int64
	now.Store(10_000_000)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithNow(func() int64 { return now.Load() }),
		WithTailLiveness(time.Second, 10*time.Millisecond, time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	old := int64(1_000_000)
	if err := i.RecordSourceLifecycle(ctx, "src-stale", "codex", "/tmp/src-stale", SourceLifecycleUpdate{
		State:           SourceLifecycleTailing,
		AtUS:            old,
		TailStartedAtUS: &old,
		TailHeartbeatUS: &old,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}
	restart := make(chan struct{}, 1)
	unregister := i.RegisterTailRestart("src-stale", restart)
	defer unregister()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = i.Stop() })

	select {
	case <-restart:
	case <-time.After(time.Second):
		t.Fatal("tail watchdog did not request restart")
	}
	if !waitFor(time.Second, func() bool {
		return scanString(t, db, `SELECT lifecycle_state FROM source_progress WHERE source_id='src-stale'`) == string(SourceLifecycleTailStale)
	}) {
		state := scanString(t, db, `SELECT lifecycle_state FROM source_progress WHERE source_id='src-stale'`)
		t.Fatalf("lifecycle_state = %q, want %q", state, SourceLifecycleTailStale)
	}
}

func TestTailLivenessWriteContextHasConfiguredDeadline(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithTailStateWriteTimeout(25*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := i.tailStateWriteContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("tail state write context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
		t.Fatalf("tail state write deadline remaining = %v, want small positive duration", remaining)
	}
}

func TestTailStateWriteContextMarksPendingUntilCancel(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, cancel := i.tailStateWriteContext(context.Background())
	if got := i.tailStatePending.Load(); got != 1 {
		t.Fatalf("tailStatePending after context creation = %d, want 1", got)
	}
	cancel()
	cancel()
	if got := i.tailStatePending.Load(); got != 0 {
		t.Fatalf("tailStatePending after idempotent cancel = %d, want 0", got)
	}
}

func TestTailWatchdogRetriesAfterTemporaryWriteBlock(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	var now atomic.Int64
	now.Store(10_000_000)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithNow(func() int64 { return now.Load() }),
		WithTailLiveness(time.Second, 10*time.Millisecond, time.Second),
		WithTailStateWriteTimeout(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	old := int64(1_000_000)
	if err := i.RecordSourceLifecycle(ctx, "src-stale-retry", "codex", "/tmp/src-stale-retry", SourceLifecycleUpdate{
		State:           SourceLifecycleTailing,
		AtUS:            old,
		TailStartedAtUS: &old,
		TailHeartbeatUS: &old,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}
	restart := make(chan struct{}, 1)
	unregister := i.RegisterTailRestart("src-stale-retry", restart)
	defer unregister()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = i.Stop() })

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("hold tx: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	select {
	case <-restart:
		t.Fatal("restart was requested while stale state could not be persisted")
	default:
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release tx: %v", err)
	}

	select {
	case <-restart:
	case <-time.After(time.Second):
		t.Fatal("tail watchdog did not retry restart request after write block cleared")
	}
	if !waitFor(time.Second, func() bool {
		return scanString(t, db, `SELECT lifecycle_state FROM source_progress WHERE source_id='src-stale-retry'`) == string(SourceLifecycleTailStale)
	}) {
		state := scanString(t, db, `SELECT lifecycle_state FROM source_progress WHERE source_id='src-stale-retry'`)
		t.Fatalf("lifecycle_state = %q, want %q", state, SourceLifecycleTailStale)
	}
}

func TestTailHeartbeatRetriesAfterTemporaryWriteBlock(t *testing.T) {
	t.Parallel()

	_, db := openTestStore(t)
	ctx := context.Background()
	var now atomic.Int64
	now.Store(1_000_000)
	i, err := New(db,
		WithLogger(silentLogger()),
		WithNow(func() int64 { return now.Load() }),
		WithTailLiveness(time.Minute, time.Hour, 30*time.Second),
		WithTailStateWriteTimeout(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := int64(900_000)
	if err := i.RecordSourceLifecycle(ctx, "src-heartbeat-retry", "codex", "/tmp/src-heartbeat-retry", SourceLifecycleUpdate{
		State:           SourceLifecycleTailing,
		AtUS:            started,
		TailStartedAtUS: &started,
		TailHeartbeatUS: &started,
	}); err != nil {
		t.Fatalf("RecordSourceLifecycle: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = i.Stop() })

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("hold tx: %v", err)
	}
	i.RecordTailHeartbeat("src-heartbeat-retry")
	time.Sleep(80 * time.Millisecond)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release tx: %v", err)
	}

	now.Store(1_100_000)
	i.RecordTailHeartbeat("src-heartbeat-retry")
	if !waitFor(time.Second, func() bool {
		return scanInt(t, db, `SELECT IFNULL(tail_heartbeat_at, 0) FROM source_progress WHERE source_id='src-heartbeat-retry'`) == 1_100_000
	}) {
		got := scanInt(t, db, `SELECT IFNULL(tail_heartbeat_at, 0) FROM source_progress WHERE source_id='src-heartbeat-retry'`)
		t.Fatalf("tail_heartbeat_at = %d, want retry value 1100000 after first write timed out", got)
	}
}
