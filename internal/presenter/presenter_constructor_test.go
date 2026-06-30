package presenter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
	"github.com/netdata/ai-viewer/internal/store"
)

func TestNewResolvePresenterLogger(t *testing.T) {
	var defaultBuf bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&defaultBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(oldDefault) })

	resolvePresenterLogger(nil).Info("default logger path")
	if got := defaultBuf.String(); !strings.Contains(got, `"subsystem":"presenter"`) {
		t.Fatalf("default logger output = %q, want subsystem=presenter", got)
	}

	var customBuf bytes.Buffer
	custom := slog.New(slog.NewJSONHandler(&customBuf, nil))
	resolvePresenterLogger(custom).Info("custom logger path")
	if got := customBuf.String(); !strings.Contains(got, `"subsystem":"presenter"`) {
		t.Fatalf("custom logger output = %q, want subsystem=presenter", got)
	}
}

func TestNewResolvePresenterClock(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 7, 10, 30, 0, 0, time.FixedZone("fixture", 3600))
	customClock := resolvePresenterClock(func() time.Time { return fixed })
	if got := customClock(); !got.Equal(fixed) || got.Location() != fixed.Location() {
		t.Fatalf("custom clock = %v (%s), want %v (%s)", got, got.Location(), fixed, fixed.Location())
	}

	defaultClock := resolvePresenterClock(nil)
	if got := defaultClock(); got.Location() != time.UTC {
		t.Fatalf("default clock location = %s, want UTC", got.Location())
	}
}

func TestNewResolvePresenterScalarDefaults(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	calls := 0
	clock := func() time.Time {
		calls++
		return fixed
	}
	if got := resolvePresenterStartedAt(time.Time{}, clock); !got.Equal(fixed) {
		t.Fatalf("zero StartedAt = %v, want %v", got, fixed)
	}
	if calls != 1 {
		t.Fatalf("clock calls after zero StartedAt = %d, want 1", calls)
	}

	explicitStartedAt := fixed.Add(-time.Hour)
	if got := resolvePresenterStartedAt(explicitStartedAt, clock); !got.Equal(explicitStartedAt) {
		t.Fatalf("explicit StartedAt = %v, want %v", got, explicitStartedAt)
	}
	if calls != 1 {
		t.Fatalf("clock calls after explicit StartedAt = %d, want still 1", calls)
	}

	if got := resolvePresenterSchemaVersion(0); got != SchemaVersion {
		t.Fatalf("zero SchemaVersion = %d, want %d", got, SchemaVersion)
	}
	if got := resolvePresenterSchemaVersion(99); got != 99 {
		t.Fatalf("explicit SchemaVersion = %d, want 99", got)
	}
}

func TestNewResolvePresenterHubAndPollInterval(t *testing.T) {
	t.Parallel()

	customHub := notify.New(notify.Options{ChannelCap: 4})
	if got := resolvePresenterHub(customHub); got != customHub {
		t.Fatal("custom hub was not preserved")
	}
	if got := resolvePresenterHub(nil); got == nil {
		t.Fatal("nil hub defaulted to nil")
	}

	if got := resolveNotifyPollInterval(0); got != defaultNotifyPollInterval {
		t.Fatalf("zero poll interval = %s, want %s", got, defaultNotifyPollInterval)
	}
	if got := resolveNotifyPollInterval(-time.Second); got != defaultNotifyPollInterval {
		t.Fatalf("negative poll interval = %s, want %s", got, defaultNotifyPollInterval)
	}
	if got := resolveNotifyPollInterval(25 * time.Millisecond); got != 25*time.Millisecond {
		t.Fatalf("positive poll interval = %s, want 25ms", got)
	}
}

func TestNewWiresResolvedDefaultsAndHubCleanup_LifecycleDefaults(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	hub := notify.New(notify.Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	p := newConstructorTestPresenter(t, fixed, hub)

	if p.startedAt != fixed {
		t.Fatalf("StartedAt = %v, want %v", p.startedAt, fixed)
	}
	if p.schemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", p.schemaVersion, SchemaVersion)
	}
	if got := p.now(); !got.Equal(fixed) {
		t.Fatalf("now = %v, want %v", got, fixed)
	}
	if got := p.notifyNow(); !got.Equal(fixed) {
		t.Fatalf("notifyNow = %v, want %v", got, fixed)
	}
}

func TestSchemaVersionTracksSourceRepairLivenessMigration(t *testing.T) {
	t.Parallel()

	if SchemaVersion != 14 {
		t.Fatalf("SchemaVersion = %d, want 14 for source repair liveness migration", SchemaVersion)
	}
}

func TestNewWiresResolvedDefaultsAndHubCleanup_RealtimeDefaults(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	hub := notify.New(notify.Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	p := newConstructorTestPresenter(t, fixed, hub)

	if p.hub != hub {
		t.Fatal("custom hub was not wired")
	}
	if p.subs == nil {
		t.Fatal("subscription manager is nil")
	}
	if p.notifyPollInterval != defaultNotifyPollInterval {
		t.Fatalf("notifyPollInterval = %s, want %s", p.notifyPollInterval, defaultNotifyPollInterval)
	}
	if p.sseKeepalive != defaultSSEKeepalive {
		t.Fatalf("sseKeepalive = %s, want %s", p.sseKeepalive, defaultSSEKeepalive)
	}
	if p.statsCoalesce == nil {
		t.Fatal("statsCoalesce is nil")
	}
}

func TestNewWiresResolvedDefaultsAndHubCleanup_HubRemovalCleansState(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	hub := notify.New(notify.Options{ChannelCap: 4, ReplayBuffer: 4, Retention: time.Second})
	p := newConstructorTestPresenter(t, fixed, hub)
	id, err := p.subs.create(subscriptionFilter{base: sessionFilter{group: groupAll}})
	if err != nil {
		t.Fatalf("subs.create: %v", err)
	}
	if !hub.Has(id) {
		t.Fatalf("subscription %q was not added to the hub", id)
	}
	p.notifyMu.Lock()
	p.statsCoalesce[id] = fixed
	p.notifyMu.Unlock()

	hub.Remove(id)
	if p.subs.has(id) {
		t.Fatalf("subscription %q remained in registry after hub removal", id)
	}
	if got := statsCoalesceLen(p); got != 0 {
		t.Fatalf("statsCoalesce entries after hub removal = %d, want 0", got)
	}
}

func newConstructorTestPresenter(t *testing.T, fixed time.Time, hub *notify.Hub) *Presenter {
	t.Helper()

	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	p, err := New(Options{
		DB:                 s.DB(),
		Now:                func() time.Time { return fixed },
		Hub:                hub,
		NotifyPollInterval: -time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.db != s.DB() {
		t.Fatal("presenter DB was not preserved")
	}
	return p
}
