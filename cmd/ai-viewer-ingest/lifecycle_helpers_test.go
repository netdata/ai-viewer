package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/store"
)

type tailBeforeBackfillAdapter struct {
	scanDone    chan struct{}
	tailStarted chan struct{}
}

func (a *tailBeforeBackfillAdapter) Name() string   { return "tail-before-backfill" }
func (a *tailBeforeBackfillAdapter) Format() string { return "tail-before-backfill" }

func (a *tailBeforeBackfillAdapter) Scan(context.Context, canonical.Cursor, chan<- canonical.Event) error {
	return nil
}

func (a *tailBeforeBackfillAdapter) Tail(ctx context.Context, _ chan<- canonical.Event) error {
	close(a.tailStarted)
	<-ctx.Done()
	return ctx.Err()
}

func (a *tailBeforeBackfillAdapter) ParseCursor(string) (canonical.Cursor, error) {
	return nil, errors.New("not used")
}

type sourceLifecycleAdapter struct {
	name            string
	scanStarted     chan struct{}
	scanStartedOnce sync.Once
	scanBlock       <-chan struct{}
	scanNilOnCancel bool
	scanEvents      []canonical.Event
	scanEventDelay  time.Duration
	scanErr         error
	tailErr         error
	tailStarted     chan struct{}
	tailStartedOnce sync.Once
	tailEvents      []canonical.Event
	tailBlock       <-chan struct{}
	tailLateEvents  []canonical.Event
	tailLateSend    chan struct{}
}

func (a *sourceLifecycleAdapter) Name() string {
	if a.name == "" {
		return "source-lifecycle"
	}
	return a.name
}

func (a *sourceLifecycleAdapter) Format() string { return "source-lifecycle" }

func (a *sourceLifecycleAdapter) Scan(ctx context.Context, _ canonical.Cursor, out chan<- canonical.Event) error {
	if a.scanStarted != nil {
		a.scanStartedOnce.Do(func() { close(a.scanStarted) })
	}
	for _, ev := range a.scanEvents {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.scanEventDelay > 0 {
		timer := time.NewTimer(a.scanEventDelay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	if a.scanBlock != nil {
		select {
		case <-a.scanBlock:
		case <-ctx.Done():
			if a.scanNilOnCancel {
				return nil
			}
			return ctx.Err()
		}
	}
	return a.scanErr
}

func (a *sourceLifecycleAdapter) Tail(ctx context.Context, out chan<- canonical.Event) error {
	if a.tailStarted != nil {
		a.tailStartedOnce.Do(func() { close(a.tailStarted) })
	}
	for _, ev := range a.tailEvents {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if a.tailErr != nil {
		return a.tailErr
	}
	if a.tailBlock != nil {
		<-a.tailBlock
		if a.tailLateSend != nil {
			close(a.tailLateSend)
		}
		for _, ev := range a.tailLateEvents {
			out <- ev
		}
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (a *sourceLifecycleAdapter) ParseCursor(string) (canonical.Cursor, error) {
	return nil, nil
}

func singleAdapterLookup(adapter canonical.Adapter) adapterFactoryLookup {
	return func(string) (canonical.AdapterFactory, bool) {
		return func(string, canonical.AdapterOptions) (canonical.Adapter, error) {
			return adapter, nil
		}, true
	}
}

type restartFactory struct {
	mu       sync.Mutex
	adapters []*sourceLifecycleAdapter
	calls    int
}

func (f *restartFactory) lookup(string) (canonical.AdapterFactory, bool) {
	return func(string, canonical.AdapterOptions) (canonical.Adapter, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.calls >= len(f.adapters) {
			return nil, errors.New("unexpected adapter construction")
		}
		adapter := f.adapters[f.calls]
		f.calls++
		return adapter, nil
	}, true
}

func openLifecycleIngester(t *testing.T) (*store.Store, *sql.DB, *ingest.Ingester) {
	t.Helper()
	return openLifecycleIngesterWithOptions(t)
}

func openLifecycleIngesterWithOptions(t *testing.T, opts ...ingest.Option) (*store.Store, *sql.DB, *ingest.Ingester) {
	t.Helper()
	st, err := store.OpenWriter(context.Background(), filepath.Join(t.TempDir(), "index.db"), silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	baseOpts := []ingest.Option{
		ingest.WithLogger(silentLogger()),
		ingest.WithBatchInterval(10 * time.Millisecond),
	}
	baseOpts = append(baseOpts, opts...)
	ing, err := ingest.New(st.DB(), baseOpts...)
	if err != nil {
		t.Fatalf("ingest.New: %v", err)
	}
	if err := ing.Start(context.Background()); err != nil {
		t.Fatalf("ingest.Start: %v", err)
	}
	t.Cleanup(func() { _ = ing.Stop() })
	return st, st.DB(), ing
}

func waitForScanOutcome(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	waitForChannel(t, done, "scan outcome")
}

func waitForChannel(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not happen", name)
	}
}

func waitForLifecycleState(t *testing.T, db *sql.DB, sourceID, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, _ := readLifecycleState(t, db, sourceID)
		if state == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := readLifecycleState(t, db, sourceID)
	return state == want
}

func readLifecycleState(t *testing.T, db *sql.DB, sourceID string) (string, string) {
	t.Helper()
	var state string
	var lifecycleErr sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT lifecycle_state, lifecycle_error FROM source_progress WHERE source_id=?`,
		sourceID,
	).Scan(&state, &lifecycleErr); err != nil {
		t.Fatalf("read lifecycle state for %q: %v", sourceID, err)
	}
	return state, lifecycleErr.String
}

func readLifecycleInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var got int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatalf("query lifecycle int: %v", err)
	}
	return got
}

func waitForLifecycleInt(t *testing.T, db *sql.DB, query string, want int64, timeout time.Duration, args ...any) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if readLifecycleInt(t, db, query, args...) == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return readLifecycleInt(t, db, query, args...) == want
}

func waitForLifecycleString(t *testing.T, db *sql.DB, query, sourceID, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if readLifecycleString(t, db, query, sourceID) == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return readLifecycleString(t, db, query, sourceID) == want
}

func readLifecycleString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var got string
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatalf("query lifecycle string: %v", err)
	}
	return got
}

func readModelTailEvents(sourceID string) []canonical.Event {
	return readModelEvents(sourceID, "tail-read-model-session", "tail-llm", `{"tail":1}`, 1_700_000_000_000_000)
}

func readModelScanEvents(sourceID string) []canonical.Event {
	return readModelEvents(sourceID, "scan-read-model-session", "scan-llm", `{"scan":1}`, 1_700_000_100_000_000)
}

func readModelEvents(sourceID, sessionID, opName, cursor string, ts int64) []canonical.Event {
	return []canonical.Event{
		canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: sourceID, SourceSeq: 1, Ts: ts},
			NativeID:     sessionID,
			RootNativeID: sessionID,
			Kind:         canonical.KindRoot,
			AgentName:    "agent",
		},
		canonical.TurnStartedEvent{
			EventBase:       canonical.EventBase{SourceID: sourceID, SourceSeq: 2, Ts: ts + 1},
			SessionNativeID: sessionID,
			Seq:             1,
		},
		canonical.OpStartedEvent{
			EventBase:       canonical.EventBase{SourceID: sourceID, SourceSeq: 3, Ts: ts + 2},
			SessionNativeID: sessionID,
			TurnSeq:         1,
			Seq:             1,
			ParentOpSeq:     -1,
			Kind:            canonical.OpLLM,
			Name:            opName,
			Model:           "model",
			Provider:        "provider",
		},
		canonical.SourceProgressEvent{
			EventBase: canonical.EventBase{SourceID: sourceID, SourceSeq: 4, Ts: ts + 3},
			Cursor:    cursor,
		},
	}
}
