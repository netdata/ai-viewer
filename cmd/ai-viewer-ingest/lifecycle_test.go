package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
)

func TestRunAdapterStartsTailBeforeGlobalBackfillDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	events := make(chan canonical.Event, 1)
	adapter := &tailBeforeBackfillAdapter{
		scanDone:    make(chan struct{}, 1),
		tailStarted: make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAdapter(ctx, adapter, nil, events, silentLogger(), func() {
			adapter.scanDone <- struct{}{}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runAdapter did not stop after context cancellation")
		}
	})

	select {
	case <-adapter.scanDone:
	case <-time.After(time.Second):
		t.Fatal("scanDone was not called")
	}

	select {
	case <-adapter.tailStarted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Tail did not start before global backfillDone closed")
	}
}

func TestStartSourceRecordsStartFailedForUnknownAdapter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, db, ing := openLifecycleIngester(t)

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "unknown:" + t.TempDir(),
		format:   "unknown",
		location: t.TempDir(),
	}

	err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		func(string) (canonical.AdapterFactory, bool) { return nil, false },
	)
	if err == nil {
		t.Fatal("startSourceWithFactoryLookup returned nil for unknown adapter")
	}
	waitForScanOutcome(t, &scanWG)

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleStartFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleStartFailed)
	}
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want startup failure evidence")
	}
}

func TestStartSourceRecordsConstructFailed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, db, ing := openLifecycleIngester(t)

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}

	err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		func(string) (canonical.AdapterFactory, bool) {
			return func(string, canonical.AdapterOptions) (canonical.Adapter, error) {
				return nil, errors.New("construct failed")
			}, true
		},
	)
	if err == nil {
		t.Fatal("startSourceWithFactoryLookup returned nil for constructor failure")
	}
	waitForScanOutcome(t, &scanWG)

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleConstructFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleConstructFailed)
	}
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want constructor failure evidence")
	}
	assertNoSupervisorRegistrations(t, ing, src.id)
}

func TestStartSourceCleansSupervisorRegistrationsOnSubmitFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, db, ing := openLifecycleIngester(t)

	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	existingEvents := make(chan canonical.Event)
	if err := ing.Submit(src.id, existingEvents); err != nil {
		t.Fatalf("pre-submit existing source: %v", err)
	}
	defer close(existingEvents)

	adapter := &sourceLifecycleAdapter{}
	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		src,
		silentLogger(),
		singleAdapterLookup(adapter),
	)
	if err == nil {
		t.Fatal("startSourceWithFactoryLookup returned nil for duplicate submit failure")
	}
	waitForScanOutcome(t, &scanWG)

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleStartFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleStartFailed)
	}
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want submit failure evidence")
	}
	assertNoSupervisorRegistrations(t, ing, src.id)
}

func TestStartSourceRecordsScanAndTailLifecycle(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	adapter := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
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
		func(string) (canonical.AdapterFactory, bool) {
			return func(string, canonical.AdapterOptions) (canonical.Adapter, error) {
				return adapter, nil
			}, true
		},
	); err != nil {
		t.Fatalf("startSourceWithFactoryLookup: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, adapter.tailStarted, "tail start")

	if !waitForLifecycleState(t, db, src.id, string(ingest.SourceLifecycleTailing), time.Second) {
		state, _ := readLifecycleState(t, db, src.id)
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleTailing)
	}

	cancel()
	adapterWG.Wait()
}

func assertNoSupervisorRegistrations(t *testing.T, ing *ingest.Ingester, sourceID string) {
	t.Helper()
	if got := ingesterMapLen(t, ing, "tailRestartChans"); got != 0 {
		t.Fatalf("tail restart registrations = %d, want 0", got)
	}
	if got := ingesterMapLen(t, ing, "readModelRepairChans"); got != 0 {
		t.Fatalf("read-model repair registrations = %d, want 0", got)
	}
	if ing.RequestSourceReadModelRepair(sourceID) {
		t.Fatal("read-model repair request was accepted after source startup failed")
	}
}

func ingesterMapLen(t *testing.T, ing *ingest.Ingester, field string) int {
	t.Helper()
	v := reflect.ValueOf(ing)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		t.Fatalf("ingester is not a non-nil pointer")
	}
	f := v.Elem().FieldByName(field)
	if !f.IsValid() {
		t.Fatalf("ingester field %q not found", field)
	}
	if f.Kind() != reflect.Map {
		t.Fatalf("ingester field %q kind = %s, want map", field, f.Kind())
	}
	return f.Len()
}

func TestSourceSupervisorDoesNotTailWithoutDurableLifecycleState(t *testing.T) {
	restore := overrideSourceRestartBackoffForTest(5*time.Millisecond, 5*time.Millisecond)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	st, _, ing := openLifecycleIngester(t)

	scanStarted := make(chan struct{})
	unblockScan := make(chan struct{})
	adapter := &sourceLifecycleAdapter{
		scanStarted: scanStarted,
		scanBlock:   unblockScan,
		tailStarted: make(chan struct{}),
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
	waitForChannel(t, scanStarted, "scan start")
	if err := st.Close(); err != nil {
		t.Fatalf("close store before scan completion: %v", err)
	}
	close(unblockScan)

	select {
	case <-adapter.tailStarted:
		t.Fatal("Tail started even though lifecycle state could not be persisted")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	waitForScanOutcome(t, &scanWG)
	done := make(chan struct{})
	go func() {
		adapterWG.Wait()
		close(done)
	}()
	waitForChannel(t, done, "source supervisor shutdown")
}

func TestStartSourceRecordsStoppedWhenScanIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	_, db, ing := openLifecycleIngester(t)

	scanStarted := make(chan struct{})
	adapter := &sourceLifecycleAdapter{
		scanStarted: scanStarted,
		scanBlock:   make(chan struct{}),
		tailStarted: make(chan struct{}),
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
	waitForChannel(t, scanStarted, "scan start")
	cancel()
	waitForScanOutcome(t, &scanWG)
	adapterWG.Wait()

	state, _ := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleStopped) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleStopped)
	}
	select {
	case <-adapter.tailStarted:
		t.Fatal("Tail started after scan cancellation")
	default:
	}
}

func TestStartSourceRecoverableScanErrorStillTails(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	adapter := &sourceLifecycleAdapter{
		scanErr:     errors.New("partial scan failed"),
		tailStarted: make(chan struct{}),
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
	waitForChannel(t, adapter.tailStarted, "tail start after recoverable scan error")

	if !waitForLifecycleState(t, db, src.id, string(ingest.SourceLifecycleTailing), time.Second) {
		state, _ := readLifecycleState(t, db, src.id)
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleTailing)
	}
	_, lifecycleErr := readLifecycleState(t, db, src.id)
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want recoverable scan failure evidence")
	}

	cancel()
	adapterWG.Wait()
}

func TestStartSourceFatalScanErrorDoesNotTail(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)

	adapter := &sourceLifecycleAdapter{
		scanErr:     canonical.NewFatalScanError(errors.New("source schema unusable")),
		tailStarted: make(chan struct{}),
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
	adapterWG.Wait()

	state, lifecycleErr := readLifecycleState(t, db, src.id)
	if state != string(ingest.SourceLifecycleScanFailed) {
		t.Fatalf("lifecycle_state = %q, want %q", state, ingest.SourceLifecycleScanFailed)
	}
	if lifecycleErr == "" {
		t.Fatal("lifecycle_error is empty, want fatal scan evidence")
	}
	select {
	case <-adapter.tailStarted:
		t.Fatal("Tail started after fatal scan error")
	default:
	}
}

func TestSourceTailCommitsWhileAnotherSourceScanIsBlocked(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)
	ing.SetDeferReadModels(true)

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(2)

	blockedScanStarted := make(chan struct{})
	blocked := &sourceLifecycleAdapter{
		scanStarted: blockedScanStarted,
		scanBlock:   make(chan struct{}),
		tailStarted: make(chan struct{}),
	}
	srcBlocked := configuredSource{
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
		srcBlocked,
		silentLogger(),
		singleAdapterLookup(blocked),
	); err != nil {
		t.Fatalf("start blocked source: %v", err)
	}
	waitForChannel(t, blockedScanStarted, "blocked source scan start")

	srcLive := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	live := &sourceLifecycleAdapter{
		tailStarted:    make(chan struct{}),
		scanEvents:     readModelScanEvents(srcLive.id),
		scanEventDelay: 50 * time.Millisecond,
		tailEvents:     readModelTailEvents(srcLive.id),
	}
	if err := startSourceWithFactoryLookup(
		ctx,
		&adapterWG,
		&scanWG,
		ing,
		nil,
		srcLive,
		silentLogger(),
		singleAdapterLookup(live),
	); err != nil {
		t.Fatalf("start live source: %v", err)
	}
	waitForChannel(t, live.tailStarted, "live source tail start")

	if !waitForLifecycleString(t, db, `SELECT IFNULL(cursor, '') FROM source_progress WHERE source_id=?`, srcLive.id, `{"tail":1}`, 2*time.Second) {
		got := readLifecycleString(t, db, `SELECT IFNULL(cursor, '') FROM source_progress WHERE source_id=?`, srcLive.id)
		t.Fatalf("live source cursor = %q, want tail event committed while other source scan is blocked", got)
	}
	if !waitForLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='tail-llm'`, 1, 2*time.Second) {
		got := readLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='tail-llm'`)
		t.Fatalf("tail FTS rows = %d, want 1 while another source scan is blocked", got)
	}
	if !waitForLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`, 1, 2*time.Second) {
		got := readLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`)
		t.Fatalf("scan FTS rows = %d, want 1 after source scan completed while another source scan is blocked", got)
	}
	if !waitForLifecycleInt(t, db, `SELECT IFNULL(SUM(op_count), 0) FROM rollup_hourly WHERE source_format='codex' AND dimension='total'`, 2, 2*time.Second) {
		got := readLifecycleInt(t, db, `SELECT IFNULL(SUM(op_count), 0) FROM rollup_hourly WHERE source_format='codex' AND dimension='total'`)
		t.Fatalf("rollup_hourly total op_count = %d, want 2 after source repair includes Scan and Tail ops", got)
	}
	if !waitForLifecycleString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id=?`, srcLive.id, string(ingest.ReadModelReady), 2*time.Second) {
		got := readLifecycleString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id=?`, srcLive.id)
		t.Fatalf("read_model_state = %q, want %q", got, ingest.ReadModelReady)
	}
	select {
	case <-blocked.tailStarted:
		t.Fatal("blocked source reached Tail before its Scan unblocked")
	default:
	}

	cancel()
	adapterWG.Wait()
}

func TestSourceReadModelRepairRequestRepairsDurablePendingDebt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, db, ing := openLifecycleIngester(t)
	ing.SetDeferReadModels(false)

	var adapterWG sync.WaitGroup
	var scanWG sync.WaitGroup
	scanWG.Add(1)
	src := configuredSource{
		id:       "codex:" + t.TempDir(),
		format:   "codex",
		location: t.TempDir(),
	}
	adapter := &sourceLifecycleAdapter{
		tailStarted: make(chan struct{}),
		scanEvents:  readModelScanEvents(src.id),
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
		t.Fatalf("start source: %v", err)
	}
	waitForScanOutcome(t, &scanWG)
	waitForChannel(t, adapter.tailStarted, "tail start")

	if !waitForLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`, 1, 2*time.Second) {
		got := readLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`)
		t.Fatalf("initial FTS rows = %d, want 1", got)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM fts_ops WHERE name='scan-llm'`); err != nil {
		t.Fatalf("delete FTS row: %v", err)
	}
	if err := ing.RecordSourceLifecycle(context.Background(), src.id, src.format, src.location, ingest.SourceLifecycleUpdate{
		ReadModelState: ingest.ReadModelRepairPending,
		AtUS:           time.Now().UTC().UnixMicro(),
	}); err != nil {
		t.Fatalf("record durable repair pending: %v", err)
	}
	if !ing.RequestSourceReadModelRepair(src.id) {
		t.Fatal("repair request was not accepted")
	}

	if !waitForLifecycleString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id=?`, src.id, string(ingest.ReadModelReady), 2*time.Second) {
		got := readLifecycleString(t, db, `SELECT read_model_state FROM source_progress WHERE source_id=?`, src.id)
		t.Fatalf("read_model_state = %q, want %q", got, ingest.ReadModelReady)
	}
	if !waitForLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`, 1, 2*time.Second) {
		got := readLifecycleInt(t, db, `SELECT COUNT(*) FROM fts_ops WHERE name='scan-llm'`)
		t.Fatalf("repaired FTS rows = %d, want 1", got)
	}

	cancel()
	adapterWG.Wait()
}
