package ingest

import (
	"context"
	"errors"
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
	i, err := New(db,
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
	i, err := New(db,
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
	i, err := New(db,
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
