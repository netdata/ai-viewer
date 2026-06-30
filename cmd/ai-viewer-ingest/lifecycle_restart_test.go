package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
)

func TestRetrySourceLifecycleRetriesUntilRecordSucceeds(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(time.Millisecond, time.Millisecond)
	defer restore()

	var attempts int
	err := retrySourceLifecycle(context.Background(), silentLogger(), ingest.SourceLifecycleUpdate{
		State: ingest.SourceLifecycleStarting,
	}, func(context.Context, ingest.SourceLifecycleUpdate) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("transient lifecycle write %d", attempts)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retrySourceLifecycle: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestStartSourceRestartsAfterTailFailure(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	first := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
		tailErr:     errors.New("tail failed"),
	}
	second := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
	}
	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{first, second}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		factory.lookup,
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, first.tailStarted, "first tail start")
	waitForChannel(t, second.tailStarted, "second tail start after restart")

	if !waitForLifecycleState(t, db, src.id, string(ingest.SourceLifecycleTailing), 2*time.Second) {
		state, _ := readLifecycleState(t, db, src.id)
		t.Fatalf("lifecycle_state = %q, want restarted %q", state, ingest.SourceLifecycleTailing)
	}
	if got := readLifecycleInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id=?`, src.id); got != 0 {
		t.Fatalf("tail_restart_count after successful restart = %d, want 0", got)
	}
	if got := readLifecycleInt(t, db, `SELECT IFNULL(tail_failed_at, 0) FROM source_progress WHERE source_id=?`, src.id); got == 0 {
		t.Fatal("tail_failed_at = 0, want failure timestamp")
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceRestartsAfterUnexpectedTailNilReturn(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(time.Millisecond, time.Millisecond)
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	tailReturned := make(chan struct{})
	close(tailReturned)
	secondTailStarted := make(chan struct{})
	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{
		{
			tailStarted: make(chan struct{}),
			tailBlock:   tailReturned,
		},
		{
			tailStarted: secondTailStarted,
		},
	}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	if err := startSourceWithFactoryLookup(ctx, &adapterWG, &scanWG, ing, nil, src, silentLogger(), factory.lookup); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, secondTailStarted, "second tail start after unexpected nil return")

	if got := readLifecycleInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id=?`, src.id); got != 0 {
		t.Fatalf("tail_restart_count after successful restart = %d, want 0", got)
	}
	if got := readLifecycleInt(t, db, `SELECT IFNULL(tail_failed_at, 0) FROM source_progress WHERE source_id=?`, src.id); got == 0 {
		t.Fatal("tail_failed_at = 0, want failure timestamp for unexpected nil Tail return")
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceRecordsStoppedWhenScanReturnsNilAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	_, db, ing := openLifecycleIngester(t)

	scanStarted := make(chan struct{})
	scanBlock := make(chan struct{})
	tailStarted := make(chan struct{})
	adapter := &sourceLifecycleAdapter{
		scanStarted:     scanStarted,
		scanBlock:       scanBlock,
		scanNilOnCancel: true,
		tailStarted:     tailStarted,
	}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	if err := startSourceWithFactoryLookup(ctx, &adapterWG, &scanWG, ing, nil, src, silentLogger(), singleAdapterLookup(adapter)); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForChannel(t, scanStarted, "scan start")
	cancel()
	adapterWG.Wait()
	waitForScanOutcome(t, &scanWG)

	state, _ := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleStopped) {
		t.Fatalf("lifecycle_state = %q, want %q after cancelled Scan returned nil", state, ingest.SourceLifecycleStopped)
	}
	select {
	case <-tailStarted:
		t.Fatal("Tail started after Scan returned nil due to source cancellation")
	default:
	}
	close(scanBlock)
}

func TestStartSourceRestartCatchupScanFailureRetriesWithoutTail(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(10*time.Millisecond, 20*time.Millisecond)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	first := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
		tailErr:     errors.New("tail failed"),
	}
	second := &sourceLifecycleAdapter{
		scanStarted: make(chan struct{}),
		scanErr:     errors.New("catch-up scan failed"),
		tailStarted: make(chan struct{}),
	}
	third := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
	}
	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{first, second, third}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		factory.lookup,
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, first.tailStarted, "first tail start")
	waitForChannel(t, second.scanStarted, "restart catch-up scan")

	select {
	case <-second.tailStarted:
		t.Fatal("Tail started after failed restart catch-up Scan")
	case <-time.After(50 * time.Millisecond):
	}
	waitForChannel(t, third.tailStarted, "third tail start after catch-up retry")
	if !waitForLifecycleState(t, db, src.id, string(ingest.SourceLifecycleTailing), 2*time.Second) {
		state, _ := readLifecycleState(t, db, src.id)
		t.Fatalf("lifecycle_state = %q, want restarted %q", state, ingest.SourceLifecycleTailing)
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceRestartCatchupFatalScanStopsWithoutTail(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(time.Millisecond, time.Millisecond)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	first := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
		tailErr:     errors.New("tail failed"),
	}
	second := &sourceLifecycleAdapter{
		scanStarted: make(chan struct{}),
		scanErr:     canonical.NewFatalScanError(errors.New("source schema unusable")),
		tailStarted: make(chan struct{}),
	}
	third := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
	}
	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{first, second, third}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(ctx, &adapterWG, &scanWG, ing, nil, src, silentLogger(), factory.lookup); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, first.tailStarted, "first tail start")
	waitForChannel(t, second.scanStarted, "restart catch-up scan")

	done := make(chan struct{})
	go func() {
		adapterWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-second.tailStarted:
		t.Fatal("Tail started after fatal restart catch-up Scan")
	case <-third.tailStarted:
		t.Fatal("restart continued after fatal restart catch-up Scan")
	case <-time.After(2 * time.Second):
		t.Fatal("source supervisor did not stop after fatal restart catch-up Scan")
	}

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleScanFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleScanFailed)
	}
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want fatal restart scan evidence")
	}
}

func TestStartSourceEscalatesSustainedTailRestartFailures(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(10*time.Millisecond, 20*time.Millisecond)
	defer restore()
	oldThreshold := sourceTailRestartEscalateFailures
	oldDuration := sourceTailRestartEscalateAfter
	sourceTailRestartEscalateFailures = 2
	sourceTailRestartEscalateAfter = time.Hour
	defer func() {
		sourceTailRestartEscalateFailures = oldThreshold
		sourceTailRestartEscalateAfter = oldDuration
	}()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{
		{tailStarted: make(chan struct{}), tailErr: errors.New("tail failed 1")},
		{tailStarted: make(chan struct{}), tailErr: errors.New("tail failed 2")},
		{tailStarted: make(chan struct{}), tailErr: errors.New("tail failed 3")},
	}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		factory.lookup,
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	if !waitForLifecycleString(t, db, `SELECT IFNULL(lifecycle_error, '') FROM source_progress WHERE source_id=?`, src.id, "tail restart has failed 2 consecutive times", 2*time.Second) {
		got := readLifecycleString(t, db, `SELECT IFNULL(lifecycle_error, '') FROM source_progress WHERE source_id=?`, src.id)
		t.Fatalf("lifecycle_error = %q, want sustained restart escalation", got)
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceRecordsTailFailedWhenTailIgnoresShutdownCancellation(t *testing.T) {
	oldGrace := sourceTailCancelGrace
	sourceTailCancelGrace = 20 * time.Millisecond
	defer func() { sourceTailCancelGrace = oldGrace }()

	ctx, cancel := context.WithCancel(context.Background())
	_, db, ing := openLifecycleIngester(t)

	tailStarted := make(chan struct{})
	unblockTail := make(chan struct{})
	t.Cleanup(func() { close(unblockTail) })
	adapter := &sourceLifecycleAdapter{
		tailStarted: tailStarted,
		tailBlock:   unblockTail,
	}
	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		singleAdapterLookup(adapter),
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, tailStarted, "tail start")

	cancel()
	adapterWG.Wait()

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleTailFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleTailFailed)
	}
	if lifecycleErr != "tail did not stop after cancellation" {
		t.Fatalf("lifecycle_error = %q, want tail cancellation timeout evidence", lifecycleErr)
	}
	if got := readLifecycleInt(t, db, `SELECT COUNT(*) FROM notify WHERE kind='source_status_changed' AND source_id=?`, src.id); got == 0 {
		t.Fatal("source_status_changed notify row missing for tail cancellation timeout")
	}
}

func TestStartSourceLateTailSendAfterCancelTimeoutDoesNotPanic(t *testing.T) {
	oldGrace := sourceTailCancelGrace
	sourceTailCancelGrace = 20 * time.Millisecond
	defer func() { sourceTailCancelGrace = oldGrace }()

	ctx, cancel := context.WithCancel(context.Background())
	_, _, ing := openLifecycleIngester(t)

	tailStarted := make(chan struct{})
	unblockTail := make(chan struct{})
	lateSend := make(chan struct{})
	adapter := &sourceLifecycleAdapter{
		tailStarted:    tailStarted,
		tailBlock:      unblockTail,
		tailLateSend:   lateSend,
		tailLateEvents: []canonical.Event{canonical.SourceProgressEvent{Cursor: `{"late":true}`}},
	}
	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(ctx, &adapterWG, &scanWG, ing, nil, src, silentLogger(), singleAdapterLookup(adapter)); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, tailStarted, "tail start")

	cancel()
	adapterWG.Wait()
	close(unblockTail)
	waitForChannel(t, lateSend, "late tail send")
	time.Sleep(50 * time.Millisecond)
}

func TestStartSourceRestartsAfterWatchdogStaleRequest(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(time.Millisecond, time.Millisecond)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngesterWithOptions(t,
		ingest.WithTailLiveness(20*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond),
		ingest.WithTailStateWriteTimeout(time.Second),
	)

	first := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
	}
	second := &sourceLifecycleAdapter{
		scanStarted: make(chan struct{}),
		tailStarted: make(chan struct{}),
	}
	factory := &restartFactory{adapters: []*sourceLifecycleAdapter{first, second}}

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	if err := startSourceWithFactoryLookup(ctx, &adapterWG, &scanWG, ing, nil, src, silentLogger(), factory.lookup); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, first.tailStarted, "first tail start")
	waitForChannel(t, second.scanStarted, "watchdog restart catch-up Scan")
	waitForChannel(t, second.tailStarted, "watchdog restart Tail")

	if !waitForLifecycleState(t, db, src.id, string(ingest.SourceLifecycleTailing), 2*time.Second) {
		state, _ := readLifecycleState(t, db, src.id)
		t.Fatalf("lifecycle_state = %q, want restarted %q", state, ingest.SourceLifecycleTailing)
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceSuccessfulTailResetsPersistedRestartCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	for i := 0; i < 7; i++ {
		if err := ing.RecordSourceLifecycle(ctx, src.id, src.format, src.location, ingest.SourceLifecycleUpdate{
			State:            ingest.SourceLifecycleTailRestarting,
			TailRestartDelta: 1,
		}); err != nil {
			t.Fatalf("seed tail_restart_count: %v", err)
		}
	}
	if got := readLifecycleInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id=?`, src.id); got != 7 {
		t.Fatalf("seed tail_restart_count = %d, want 7", got)
	}

	adapter := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
	}
	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		singleAdapterLookup(adapter),
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, adapter.tailStarted, "tail start")

	if !waitForLifecycleInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id=?`, 0, 2*time.Second, src.id) {
		got := readLifecycleInt(t, db, `SELECT tail_restart_count FROM source_progress WHERE source_id=?`, src.id)
		t.Fatalf("tail_restart_count after successful tail = %d, want 0", got)
	}

	cancel()
	adapterWG.Wait()
}

func TestNextSourceBackoffDoublesAndCaps(t *testing.T) {
	oldMax := sourceRestartBackoffMax
	sourceRestartBackoffMax = 60 * time.Second
	defer func() { sourceRestartBackoffMax = oldMax }()

	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "one second doubles", in: time.Second, want: 2 * time.Second},
		{name: "below cap doubles to cap", in: 30 * time.Second, want: 60 * time.Second},
		{name: "at cap stays capped", in: 60 * time.Second, want: 60 * time.Second},
		{name: "above cap returns cap", in: 90 * time.Second, want: 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextSourceBackoff(tt.in); got != tt.want {
				t.Fatalf("nextSourceBackoff(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func overrideSourceRestartBackoffForTest(base, max time.Duration) func() {
	oldBase := sourceRestartBackoffBase
	oldMax := sourceRestartBackoffMax
	sourceRestartBackoffBase = base
	sourceRestartBackoffMax = max
	return func() {
		sourceRestartBackoffBase = oldBase
		sourceRestartBackoffMax = oldMax
	}
}
