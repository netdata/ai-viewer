package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// TestResolver_LinksOrphanWhenParentArrives covers the cross-session
// linkage flow: a sub-agent session whose parent has not yet been
// ingested is inserted with parent_session_id = NULL, and the resolver
// pass backfills the link once the parent lands.
func TestResolver_LinksOrphanWhenParentArrives(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()

	i, err := New(db, WithLogger(silentLogger()),
		WithBatchSize(2), WithBatchInterval(50*time.Millisecond),
		WithResolverInterval(time.Minute), // resolver loop won't run; we call linkOrphans manually
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	ch := make(chan canonical.Event, 4)
	// Child arrives first; parent is still absent.
	ch <- canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:       "child",
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "sub",
	}
	// Force the writer to flush the first batch (need 2 events).
	ch <- canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 0, Ts: 1500},
		Cursor:    `{"k":"v"}`,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait until child has been written.
	if !waitFor(5*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='child'`) == 1
	}) {
		cnt := scanInt(t, db, `SELECT COUNT(*) FROM sessions`)
		t.Fatalf("child session not written after 5s; total sessions=%d", cnt)
	}
	if got := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='child'`); got != "" {
		t.Errorf("child has parent before resolver: %q", got)
	}

	// Now the parent lands. The writer routes it through the same source.
	ch <- canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 2, Ts: 2000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
		AgentName:    "root",
	}
	ch <- canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 0, Ts: 2500},
		Cursor:    `{"k":"v2"}`,
	}
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='parent'`) == 1
	}) {
		t.Fatalf("parent session not written")
	}
	close(ch)

	// Run the resolver pass synchronously.
	if err := i.ResolveOrphans(ctx); err != nil {
		t.Fatalf("ResolveOrphans: %v", err)
	}
	parentLink := scanString(t, db, `SELECT IFNULL(parent_session_id,'') FROM sessions WHERE native_id='child'`)
	if parentLink == "" {
		t.Errorf("child still orphan after resolver")
	}
	wantParentID := canonicalSessionID("aiagent_v3:/tmp", "parent")
	if parentLink != wantParentID {
		t.Errorf("child parent_session_id = %q, want %q", parentLink, wantParentID)
	}
}

func TestResolver_NoOpWhenNoOrphans(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), time.Minute)
	if err := r.linkOrphans(context.Background()); err != nil {
		t.Fatalf("linkOrphans on empty db: %v", err)
	}
}

func TestResolver_StopIdempotent(t *testing.T) {
	t.Parallel()
	r := newResolver(nil, silentLogger(), time.Minute)
	r.Stop()
	r.Stop() // should not panic
}

func TestResolver_LoopExitsOnCtxCancel(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.loop(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit on ctx cancel")
	}
}

func TestResolver_LoopExitsOnStop(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	r := newResolver(db, silentLogger(), 10*time.Millisecond)
	done := make(chan struct{})
	go func() {
		r.loop(context.Background())
		close(done)
	}()
	r.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit on Stop")
	}
}

func TestNewResolver_DefaultsInterval(t *testing.T) {
	t.Parallel()
	r := newResolver(nil, silentLogger(), 0)
	if r.interval != 5*time.Second {
		t.Errorf("default interval = %v, want 5s", r.interval)
	}
}

func TestResolver_NilDB(t *testing.T) {
	t.Parallel()
	r := newResolver(nil, silentLogger(), time.Minute)
	if err := r.linkOrphans(context.Background()); err == nil {
		t.Fatal("expected error on nil db")
	}
}
