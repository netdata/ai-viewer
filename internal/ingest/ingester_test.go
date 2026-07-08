package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func TestNew_RejectsNilDB(t *testing.T) {
	t.Parallel()
	if _, err := New(nil); err == nil {
		t.Fatal("expected error on nil db")
	}
}

func TestStart_LoadsHWMFromStore(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	ctx := context.Background()

	// Seed source + source_progress.
	if _, err := db.ExecContext(ctx, `INSERT INTO sources (id, format, location, created_at) VALUES ('aiagent_v3:/tmp', 'aiagent_v3', '/tmp', 0)`); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO source_progress (source_id, last_seq, last_ts_us, updated_at) VALUES ('aiagent_v3:/tmp', 4242, 0, 0)`); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	if got := i.HWM("aiagent_v3:/tmp"); got != 4242 {
		t.Errorf("HWM = %d, want 4242", got)
	}
}

func TestStart_IsIdempotent(t *testing.T) {
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
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start (second): %v", err)
	}
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSubmit_BeforeStartReturnsError(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch := make(chan canonical.Event)
	close(ch)
	if err := i.Submit("aiagent_v3:/tmp", ch); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Submit before Start = %v, want ErrNotStarted", err)
	}
}

func TestSubmit_DuplicateSourceReturnsError(t *testing.T) {
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

	ch1 := make(chan canonical.Event)
	close(ch1)
	if err := i.Submit("aiagent_v3:/tmp", ch1); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	ch2 := make(chan canonical.Event)
	close(ch2)
	if err := i.Submit("aiagent_v3:/tmp", ch2); !errors.Is(err, ErrSourceAlreadySubmitted) {
		t.Errorf("duplicate Submit = %v, want ErrSourceAlreadySubmitted", err)
	}
}

func TestStop_DrainsPendingBatch(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ch := make(chan canonical.Event, 4)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: "aiagent_v3:/tmp", SourceSeq: 1, Ts: 1000},
		NativeID:  "sess-1", RootNativeID: "sess-1", Kind: canonical.KindRoot,
	}
	if err := i.Submit("aiagent_v3:/tmp", ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	close(ch)
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id='sess-1'`); got != 1 {
		t.Errorf("session row count = %d, want 1", got)
	}
}

func TestStop_RunsFinalResolverPassAfterWorkerDrain(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	const childNative = "child-session"

	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(100),
		WithBatchInterval(time.Hour),
		WithResolverInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ch := make(chan canonical.Event, 8)
	ch <- canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
		AgentName:    "parent-agent",
	}
	ch <- canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent",
		Seq:             1,
	}
	ch <- canonical.OpStartedEvent{
		EventBase:            canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID:      "parent",
		TurnSeq:              1,
		Seq:                  1,
		ParentOpSeq:          -1,
		Kind:                 canonical.OpSession,
		Name:                 "child-agent",
		ChildSessionNativeID: childNative,
	}
	ch <- canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1200},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "child-agent",
	}
	close(ch)

	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	parentID := canonicalSessionID(src, "parent")
	childID := canonicalSessionID(src, childNative)
	opID := canonicalOpID(canonicalTurnID(parentID, 1), 1)
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Fatalf("op child_session_id after Stop = %q, want %q", got, childID)
	}
}

func TestStop_RunsFinalResolverPassBeforeReturningWorkerErrors(t *testing.T) {
	t.Parallel()
	const src = "aiagent_v3:/tmp"
	const childNative = "child-session"

	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(4),
		WithBatchInterval(time.Hour),
		WithResolverInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ch := make(chan canonical.Event, 8)
	ch <- canonical.SessionStartedEvent{
		EventBase:    canonical.EventBase{SourceID: src, SourceSeq: 1, Ts: 1000},
		NativeID:     "parent",
		RootNativeID: "parent",
		Kind:         canonical.KindRoot,
		AgentName:    "parent-agent",
	}
	ch <- canonical.TurnStartedEvent{
		EventBase:       canonical.EventBase{SourceID: src, SourceSeq: 2, Ts: 1000},
		SessionNativeID: "parent",
		Seq:             1,
	}
	ch <- canonical.OpStartedEvent{
		EventBase:            canonical.EventBase{SourceID: src, SourceSeq: 3, Ts: 1100},
		SessionNativeID:      "parent",
		TurnSeq:              1,
		Seq:                  1,
		ParentOpSeq:          -1,
		Kind:                 canonical.OpSession,
		Name:                 "child-agent",
		ChildSessionNativeID: childNative,
	}
	ch <- canonical.SessionStartedEvent{
		EventBase:      canonical.EventBase{SourceID: src, SourceSeq: 4, Ts: 1200},
		NativeID:       childNative,
		RootNativeID:   "parent",
		ParentNativeID: "parent",
		Kind:           canonical.KindSubAgent,
		AgentName:      "child-agent",
	}
	if err := i.Submit(src, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !waitFor(time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sessions WHERE native_id IN ('parent', ?)`, childNative) == 2
	}) {
		t.Fatalf("valid batch did not commit before terminal batch; sessions=%d", scanInt(t, db, `SELECT COUNT(*) FROM sessions`))
	}

	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: src, SourceSeq: 5, Ts: 1300},
	}
	close(ch)

	err = i.Stop()
	if err == nil {
		t.Fatal("Stop returned nil, want worker batch error")
	}
	if !strings.Contains(err.Error(), "DROPPING") || !strings.Contains(err.Error(), "missing NativeID") {
		t.Fatalf("Stop error = %v, want dropped empty-native-id batch context", err)
	}

	parentID := canonicalSessionID(src, "parent")
	childID := canonicalSessionID(src, childNative)
	opID := canonicalOpID(canonicalTurnID(parentID, 1), 1)
	if got := scanString(t, db, `SELECT IFNULL(child_session_id,'') FROM ops WHERE id=?`, opID); got != childID {
		t.Fatalf("op child_session_id after Stop worker error = %q, want %q", got, childID)
	}
}

func TestStop_IsIdempotent(t *testing.T) {
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
	if err := i.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := i.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestStop_BeforeStartReturnsError(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Stop(); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Stop before Start = %v, want ErrNotStarted", err)
	}
}

func TestStopContext_BeforeStartReturnsErrNotStarted(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, err := i.StopContext(context.Background()); !errors.Is(err, ErrNotStarted) {
		t.Errorf("StopContext before Start = (%+v, %v), want ErrNotStarted", got, err)
	}
}

func TestStopContext_RespectsCallerDeadlineWhileWorkersStuck(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate a worker that cannot finish. StopContext must return on the
	// caller deadline instead of waiting forever on wg.Wait.
	i.wg.Add(1)
	t.Cleanup(i.wg.Done)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := i.StopContext(ctx)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("StopContext error = %v, want ErrShutdownTimeout (result=%+v)", err, got)
	}
	if got.Outcome != ShutdownTimeout {
		t.Fatalf("StopContext outcome = %q, want %q", got.Outcome, ShutdownTimeout)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("StopContext elapsed = %s, want bounded by caller deadline", elapsed)
	}
}

func TestStopContext_ConcurrentFollowerOutcomes(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	i.wg.Add(1)
	ownerDone := make(chan ShutdownResult, 1)
	ownerErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		result, err := i.StopContext(ctx)
		ownerDone <- result
		ownerErr <- err
	}()

	if !waitFor(250*time.Millisecond, func() bool {
		i.mu.Lock()
		state := i.stopState
		i.mu.Unlock()
		return state == stopStateStopping
	}) {
		t.Fatal("owning StopContext did not enter stopping state")
	}
	follower, err := i.StopContext(context.Background())
	if err != nil {
		t.Fatalf("StopContext follower error = %v", err)
	}
	if follower.Outcome != ShutdownAlreadyStopping {
		t.Fatalf("StopContext follower outcome = %q, want %q", follower.Outcome, ShutdownAlreadyStopping)
	}

	i.wg.Done()
	var owner ShutdownResult
	select {
	case owner = <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("owning StopContext did not return after worker release")
	}
	if err := <-ownerErr; err != nil {
		t.Fatalf("owning StopContext error = %v", err)
	}
	if owner.Outcome != ShutdownClean {
		t.Fatalf("owning StopContext outcome = %q, want %q", owner.Outcome, ShutdownClean)
	}

	after, err := i.StopContext(context.Background())
	if err != nil {
		t.Fatalf("StopContext after completion error = %v", err)
	}
	if after.Outcome != ShutdownAlreadyStopped {
		t.Fatalf("StopContext after completion outcome = %q, want %q", after.Outcome, ShutdownAlreadyStopped)
	}
}

func TestStopContext_ReplayOnlyWorkerErrorsClassifyReplayRequired(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	i.recordWorkerError("aiagent_v3:/tmp", fmt.Errorf("%w: replay", ErrReplayRequired))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := i.StopContext(ctx)
	if !errors.Is(err, ErrReplayRequired) {
		t.Fatalf("StopContext error = %v, want ErrReplayRequired", err)
	}
	if got.Outcome != ShutdownReplayRequired {
		t.Fatalf("StopContext outcome = %q, want %q", got.Outcome, ShutdownReplayRequired)
	}
}

func TestRecordWorkerErrorLogsReplayRequiredFields(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(logger))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	i.recordWorkerError("aiagent_v3:test-source", &replayRequiredError{
		reason:        "sqlite contention",
		pendingEvents: 3,
		cause:         context.DeadlineExceeded,
	})

	got := logs.String()
	for _, want := range []string{
		"msg=shutdown_replay_required",
		"source_id=aiagent_v3:test-source",
		"source_format=aiagent_v3",
		"outcome=replay_required",
		"pending_events=3",
		`reason="sqlite contention"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "payload") || strings.Contains(got, "location") {
		t.Fatalf("log includes raw payload/location field: %q", got)
	}
}

func TestStopContext_MixedWorkerErrorsClassifyWorkerFailure(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	i.recordWorkerError("aiagent_v3:/tmp/replay", fmt.Errorf("%w: replay", ErrReplayRequired))
	i.recordWorkerError("aiagent_v3:/tmp/drop", errors.New("permanent drop"))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := i.StopContext(ctx)
	if err == nil {
		t.Fatal("StopContext error = nil, want worker failure")
	}
	if got.Outcome != ShutdownWorkerFailure {
		t.Fatalf("StopContext outcome = %q, want %q", got.Outcome, ShutdownWorkerFailure)
	}
}

func TestStopContext_FiveSourcesCleanDrainWithinScaledBound(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1000),
		WithBatchInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for n := 1; n <= 5; n++ {
		sourceID := fmt.Sprintf("aiagent_v3:/tmp/source-%d", n)
		events := make(chan canonical.Event, 1)
		events <- canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: sourceID, SourceSeq: 1, Ts: int64(n)},
			NativeID:     fmt.Sprintf("session-%d", n),
			RootNativeID: fmt.Sprintf("session-%d", n),
			Kind:         canonical.KindRoot,
		}
		close(events)
		if err := i.Submit(sourceID, events); err != nil {
			t.Fatalf("Submit %s: %v", sourceID, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	got, err := i.StopContext(ctx)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("StopContext error = %v (result=%+v)", err, got)
	}
	if got.Outcome != ShutdownClean {
		t.Fatalf("StopContext outcome = %q, want %q", got.Outcome, ShutdownClean)
	}
	if elapsed > time.Second {
		t.Fatalf("StopContext elapsed = %s, want clean five-source drain within scaled bound", elapsed)
	}
	if got := scanInt(t, db, `SELECT COUNT(*) FROM sessions`); got != 5 {
		t.Fatalf("sessions = %d, want 5", got)
	}
}

func TestBoundedResolverContextCapsRemainingDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	ctx, cancelResolver, ok := boundedResolverContext(parent)
	defer cancelResolver()
	if !ok {
		t.Fatal("boundedResolverContext ok = false, want true")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("boundedResolverContext returned context without deadline")
	}
	if remaining := time.Until(deadline); remaining > 100*time.Millisecond {
		t.Fatalf("resolver context remaining = %s, want capped by parent deadline", remaining)
	}
}

func TestStopContext_StopsResolverLoopWithinDeadline(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithResolverInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	got, err := i.StopContext(ctx)
	if err != nil {
		t.Fatalf("StopContext error = %v (result=%+v)", err, got)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("StopContext elapsed = %s, want resolver loop to observe cancellation promptly", elapsed)
	}
}

func TestParseSourceID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in           string
		wantFormat   string
		wantLocation string
	}{
		{"aiagent_v3:/tmp/foo", "aiagent_v3", "/tmp/foo"},
		{"aiagent_v2:/tmp", "aiagent_v2", "/tmp"},
		{"nokind", "nokind", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		gotF, gotL := parseSourceID(c.in)
		if gotF != c.wantFormat || gotL != c.wantLocation {
			t.Errorf("parseSourceID(%q) = (%q, %q), want (%q, %q)", c.in, gotF, gotL, c.wantFormat, c.wantLocation)
		}
	}
}

func TestWithPricer_Override(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	custom := &fakePricer{ret: 7.5}
	i, err := New(db, WithLogger(silentLogger()), WithPricer(custom))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := i.pricer.(*fakePricer); !ok {
		t.Errorf("pricer was not overridden: %T", i.pricer)
	}
	// nil Pricer should be ignored (keeps default).
	def, err := New(db, WithLogger(silentLogger()), WithPricer(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := def.pricer.(NopPricer); !ok {
		t.Errorf("nil Pricer override should be ignored, got %T", def.pricer)
	}
}

func TestWithOptionsIgnoreZeroValues(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(nil),         // nil logger ignored
		WithBatchSize(0),        // zero ignored
		WithBatchInterval(0),    // zero ignored
		WithResolverInterval(0), // zero ignored
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if i.batchSize != defaultBatchSize {
		t.Errorf("batchSize = %d, want %d", i.batchSize, defaultBatchSize)
	}
	if i.batchInterval != defaultBatchInterval {
		t.Errorf("batchInterval = %v, want %v", i.batchInterval, defaultBatchInterval)
	}
	if i.resolverInterval != defaultResolverInterval {
		t.Errorf("resolverInterval = %v, want %v", i.resolverInterval, defaultResolverInterval)
	}
}

func TestNew_DefaultBatchSizeMatchesSpec(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db, WithLogger(silentLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if i.batchSize != defaultBatchSize {
		t.Fatalf("batchSize = %d, want spec default %d", i.batchSize, defaultBatchSize)
	}
}

func TestResolveOrphans_NilResolver(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Before Start, resolver is nil.
	if err := i.ResolveOrphans(context.Background()); err != nil {
		t.Errorf("ResolveOrphans before Start: %v", err)
	}
}

func TestSubmit_AfterStopReturnsError(t *testing.T) {
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
	if err := i.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	ch := make(chan canonical.Event)
	close(ch)
	if err := i.Submit("aiagent_v3:/tmp", ch); err == nil {
		t.Errorf("expected error after Stop")
	}
}

func TestWithSourceFormat_OverridesParsing(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithSourceFormat("custom-id", "my-format"),
		WithLocation("custom-id", "/data"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f, l := i.deriveSourceFields("custom-id")
	if f != "my-format" || l != "/data" {
		t.Errorf("deriveSourceFields = (%q, %q), want (my-format, /data)", f, l)
	}
}

// submitOneSessionAndWaitForSource submits a single SessionStartedEvent for
// sourceID and blocks until ensureSourceRow has materialised the sources row.
// Shared by the fts5_index_logs persistence tests so each asserts only the
// column value, not the plumbing. The channel is closed by the helper after the
// row appears so the worker drains and stops cleanly under i.Stop().
func submitOneSessionAndWaitForSource(t *testing.T, i *Ingester, db *sql.DB, sourceID string) {
	t.Helper()
	ch := make(chan canonical.Event, 1)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: sourceID, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	if err := i.Submit(sourceID, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT COUNT(*) FROM sources WHERE id=?`, sourceID) == 1
	}) {
		t.Fatalf("sources row for %q not created", sourceID)
	}
	close(ch)
}

// TestWithFTS5IndexLogs_PersistsZeroWhenDisabled pins the opt-out path: a source
// registered with WithFTS5IndexLogs(id, false) has its sources row persisted
// with fts5_index_logs = 0. The persisted flag gates fts_logs indexing: the FTS
// backfill and /api/search both filter on src.fts5_index_logs = 1, so a disabled
// source is excluded from both.
func TestWithFTS5IndexLogs_PersistsZeroWhenDisabled(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const sourceID = "aiagent_v3:/tmp"
	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
		WithFTS5IndexLogs(sourceID, false),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	submitOneSessionAndWaitForSource(t, i, db, sourceID)

	if got := scanInt(t, db, `SELECT fts5_index_logs FROM sources WHERE id=?`, sourceID); got != 0 {
		t.Errorf("fts5_index_logs = %d, want 0 (WithFTS5IndexLogs(false))", got)
	}
}

// TestFTS5IndexLogs_DefaultsToOneWithoutOption pins the opt-out DEFAULT: with no
// WithFTS5IndexLogs option for the source, the persisted sources row carries
// fts5_index_logs = 1 (the ingester resolves the absence of an override to the
// indexed-by-default value, matching the migration column default).
func TestFTS5IndexLogs_DefaultsToOneWithoutOption(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const sourceID = "aiagent_v3:/tmp"
	i, err := New(
		db,
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

	submitOneSessionAndWaitForSource(t, i, db, sourceID)

	if got := scanInt(t, db, `SELECT fts5_index_logs FROM sources WHERE id=?`, sourceID); got != 1 {
		t.Errorf("fts5_index_logs = %d, want 1 (default, no WithFTS5IndexLogs option)", got)
	}
}

// TestWithFTS5IndexLogs_ReassertsOnRestart pins the daemon-restart contract: the
// ingester option is the runtime source of truth, so ensureSourceRow's
// ON CONFLICT updates fts5_index_logs to the resolved value even when a prior
// row persisted the opposite. We seed a row with fts5_index_logs=0, then run an
// ingester WITHOUT the option (resolves to the default 1) and assert the row is
// re-asserted to 1.
func TestWithFTS5IndexLogs_ReassertsOnRestart(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const sourceID = "aiagent_v3:/tmp"
	ctx := context.Background()

	// Prior run persisted the opt-out (0).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, fts5_index_logs, created_at) VALUES (?, 'aiagent_v3', '/tmp', 0, 1000)`,
		sourceID); err != nil {
		t.Fatalf("seed prior source row: %v", err)
	}

	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	// The sources row already exists (seeded with 0), so a row-count wait would
	// pass before the batch flush runs ensureSourceRow's ON CONFLICT UPDATE.
	// Wait on the column flipping to the re-asserted value instead.
	ch := make(chan canonical.Event, 1)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: sourceID, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	if err := i.Submit(sourceID, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanInt(t, db, `SELECT fts5_index_logs FROM sources WHERE id=?`, sourceID) == 1
	}) {
		got := scanInt(t, db, `SELECT fts5_index_logs FROM sources WHERE id=?`, sourceID)
		t.Fatalf("fts5_index_logs = %d, want 1 (ingester re-asserts default on restart)", got)
	}
}

// blobRoundTrip asserts s unmarshals to a map[string]any with the four
// opencode keys (SOW-0024). Shared by the persistence + reassert tests so
// each asserts only its own slice of the contract.
const sampleOpencodeMetaJSON = `{"session_count":42,"message_count":1200,"part_count":3400,"latest_migration":"20260510033149_session_usage"}`

// TestIngester_PersistsSourceMeta pins the write path (SOW-0024 AC#2 write
// half). Two sources, one with WithSourceMeta(blob) and one without. The first
// flush persists the blob verbatim; the second source's column is NULL (the
// "not populated" signal).
func TestIngester_PersistsSourceMeta(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const withMeta = "aiagent_v3:/tmp/with"
	const withoutMeta = "aiagent_v3:/tmp/without"
	ctx := context.Background()

	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
		WithSourceMeta(withMeta, sampleOpencodeMetaJSON),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	submitOneSessionAndWaitForSource(t, i, db, withMeta)
	submitOneSessionAndWaitForSource(t, i, db, withoutMeta)

	// Source with the option: meta_json round-trips verbatim.
	if got := scanString(t, db, `SELECT meta_json FROM sources WHERE id=?`, withMeta); got != sampleOpencodeMetaJSON {
		t.Errorf("meta_json = %q, want %q (WithSourceMeta blob round-trip)", got, sampleOpencodeMetaJSON)
	}
	// Source without the option: meta_json is NULL (not "", which would render
	// as the empty object — the omit-when-NULL contract).
	var nullCheck sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT meta_json FROM sources WHERE id=?`, withoutMeta).Scan(&nullCheck); err != nil {
		t.Fatalf("select meta_json (unregistered source): %v", err)
	}
	if nullCheck.Valid {
		t.Errorf("meta_json = %q on the unregistered source, want NULL (absence = not populated)", nullCheck.String)
	}
}

// TestIngester_SourceMetaReassertsOnRestart pins the daemon-restart
// re-assertion contract (SOW-0024): the WithSourceMeta option is the runtime
// source of truth, so ensureSourceRow's ON CONFLICT updates meta_json to the
// resolved value even when a prior run stored a different blob. A seeded
// row with a stale blob is re-asserted to the new option value on the next
// batch flush.
func TestIngester_SourceMetaReassertsOnRestart(t *testing.T) {
	t.Parallel()
	_, db := openTestStore(t)
	const sourceID = "aiagent_v3:/tmp"
	const stale = `{"session_count":1,"message_count":2,"part_count":3,"latest_migration":"old"}`
	const fresh = `{"session_count":99,"message_count":100,"part_count":101,"latest_migration":"fresh"}`
	ctx := context.Background()

	// Seed a row with a stale blob (the prior run's value).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, meta_json, created_at) VALUES (?, 'aiagent_v3', '/tmp', ?, 1000)`,
		sourceID, stale); err != nil {
		t.Fatalf("seed prior source row: %v", err)
	}

	i, err := New(
		db,
		WithLogger(silentLogger()),
		WithBatchSize(1),
		WithBatchInterval(time.Second),
		WithSourceMeta(sourceID, fresh),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := i.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = i.Stop() }()

	// The sources row already exists, so a row-count wait would pass before the
	// batch flush runs ensureSourceRow's ON CONFLICT UPDATE. Wait on the column
	// flipping to the re-asserted value instead.
	ch := make(chan canonical.Event, 1)
	ch <- canonical.SessionStartedEvent{
		EventBase: canonical.EventBase{SourceID: sourceID, SourceSeq: 1, Ts: 1000},
		NativeID:  "s", RootNativeID: "s", Kind: canonical.KindRoot,
	}
	if err := i.Submit(sourceID, ch); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	defer close(ch)

	if !waitFor(2*time.Second, func() bool {
		return scanString(t, db, `SELECT meta_json FROM sources WHERE id=?`, sourceID) == fresh
	}) {
		got := scanString(t, db, `SELECT meta_json FROM sources WHERE id=?`, sourceID)
		t.Fatalf("meta_json = %q, want %q (ingester re-asserts WithSourceMeta on restart)", got, fresh)
	}
}
