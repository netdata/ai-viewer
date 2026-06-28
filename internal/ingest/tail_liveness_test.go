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
