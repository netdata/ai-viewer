package ingest

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Default batching / resolver parameters. Override via Option.
const (
	defaultBatchSize        = 1000
	defaultBatchInterval    = 500 * time.Millisecond
	defaultResolverInterval = 5 * time.Second
)

// defaultNow is the production wall-clock the incremental rollup refresh
// uses to pick its open-bucket cutoffs: UTC microseconds, matching every
// other timestamp in the store.
func defaultNow() int64 { return time.Now().UTC().UnixMicro() }

// ErrSourceAlreadySubmitted is returned by Submit when the caller
// re-submits the same source ID. The ingester serialises one worker per
// source ID; submitting again would race two workers on the same source.
var ErrSourceAlreadySubmitted = errors.New("ingest: source already submitted")

// ErrNotStarted is returned when Submit/Stop is called before Start.
var ErrNotStarted = errors.New("ingest: ingester not started")

// Option configures the Ingester at construction time.
type Option func(*Ingester)

// WithLogger overrides the default slog.Default() logger.
func WithLogger(l *slog.Logger) Option {
	return func(i *Ingester) {
		if l != nil {
			i.logger = l
		}
	}
}

// WithPricer overrides the default NopPricer.
func WithPricer(p Pricer) Option {
	return func(i *Ingester) {
		if p != nil {
			i.pricer = p
		}
	}
}

// WithBatchSize overrides the default flush-size threshold.
func WithBatchSize(n int) Option {
	return func(i *Ingester) {
		if n > 0 {
			i.batchSize = n
		}
	}
}

// WithBatchInterval overrides the default flush-interval threshold.
func WithBatchInterval(d time.Duration) Option {
	return func(i *Ingester) {
		if d > 0 {
			i.batchInterval = d
		}
	}
}

// WithResolverInterval overrides the default parent-link resolver tick.
func WithResolverInterval(d time.Duration) Option {
	return func(i *Ingester) {
		if d > 0 {
			i.resolverInterval = d
		}
	}
}

// WithNow overrides the wall-clock the incremental rollup refresh reads to
// pick the open-bucket cutoffs (UTC microseconds). The default is the real
// clock; tests inject a fixed value so the incremental refresh and
// BackfillRollups apply the SAME closed-bucket boundary (the property the
// Chunk-6 byte-diff gate asserts). A nil func is ignored.
func WithNow(now func() int64) Option {
	return func(i *Ingester) {
		if now != nil {
			i.now = now
		}
	}
}

// WithSourceFormat registers the user-facing format string for sourceID.
// The default extracts the prefix from "format:location" (the adapter
// SourceID convention); callers that use a different SourceID shape must
// register the format explicitly so sources.format is populated correctly.
func WithSourceFormat(sourceID, format string) Option {
	return func(i *Ingester) {
		i.formatOverrides[sourceID] = format
	}
}

// WithLocation registers the user-facing location string for sourceID.
// Default behavior mirrors WithSourceFormat: extract the suffix from
// "format:location".
func WithLocation(sourceID, location string) Option {
	return func(i *Ingester) {
		i.locationOverrides[sourceID] = location
	}
}

// WithFTS5IndexLogs registers whether the per-source FTS5 log index should be
// populated for sourceID. Mirrors WithSourceFormat/WithLocation: the override
// is the runtime source of truth and is re-asserted on the sources row at every
// batch flush, so a daemon restart re-applies the configured value. When no
// override is registered for a source the resolver defaults to true (opt-OUT —
// logs are indexed unless the operator disables it), matching the
// sources.fts5_index_logs column default. This step only PERSISTS the flag; no
// FTS index population reads it yet (data-model.md §Full-text search).
func WithFTS5IndexLogs(sourceID string, enabled bool) Option {
	return func(i *Ingester) {
		i.fts5IndexLogsOverrides[sourceID] = enabled
	}
}

// Ingester wires source-format adapters to the SQLite store. One
// instance owns one writer-side *sql.DB.
type Ingester struct {
	db               *sql.DB
	logger           *slog.Logger
	pricer           Pricer
	batchSize        int
	batchInterval    time.Duration
	resolverInterval time.Duration
	// now is the wall-clock source the incremental rollup refresh reads to
	// pick its open-bucket cutoffs. Injectable via WithNow for deterministic
	// tests; defaults to the real UTC-microsecond clock.
	now func() int64

	hwm      *hwmCache
	resolver *resolver

	mu       sync.Mutex
	started  bool
	stopped  bool
	cancelFn context.CancelFunc
	wg       sync.WaitGroup
	workers  map[string]*worker
	ctx      context.Context

	formatOverrides   map[string]string
	locationOverrides map[string]string
	// fts5IndexLogsOverrides maps sourceID → whether its FTS5 log index should
	// be populated. Set by WithFTS5IndexLogs; absence resolves to the default
	// (true) in resolveFTS5IndexLogs. Persisted on the sources row by the worker
	// (config plumbing only — no FTS population reads it yet).
	fts5IndexLogsOverrides map[string]bool
}

// New constructs an Ingester. The db must be writable (use
// store.OpenWriter). Functional options override defaults.
func New(db *sql.DB, opts ...Option) (*Ingester, error) {
	if db == nil {
		return nil, errors.New("ingest.New: nil db")
	}
	i := &Ingester{
		db:                     db,
		logger:                 slog.Default(),
		pricer:                 NopPricer{},
		batchSize:              defaultBatchSize,
		batchInterval:          defaultBatchInterval,
		resolverInterval:       defaultResolverInterval,
		now:                    defaultNow,
		hwm:                    newHWMCache(),
		workers:                make(map[string]*worker),
		formatOverrides:        make(map[string]string),
		locationOverrides:      make(map[string]string),
		fts5IndexLogsOverrides: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(i)
	}
	i.logger = i.logger.With("subsystem", "ingest")
	return i, nil
}

// Start loads the HWM cache from source_progress and launches the
// background resolver. Idempotent: calling twice is a no-op on the
// second call.
func (i *Ingester) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started {
		return nil
	}
	if err := i.hwm.Load(ctx, i.db); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	i.ctx = ctx
	i.cancelFn = cancel
	i.resolver = newResolver(i.db, i.logger, i.resolverInterval)
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.resolver.loop(ctx)
	}()
	i.started = true
	return nil
}

// Submit attaches a source's event channel to the ingester. A worker
// goroutine starts immediately and drains the channel. Returns
// ErrSourceAlreadySubmitted on duplicate sourceID.
func (i *Ingester) Submit(sourceID string, events <-chan canonical.Event) error {
	i.mu.Lock()
	if !i.started {
		i.mu.Unlock()
		return ErrNotStarted
	}
	if i.stopped {
		i.mu.Unlock()
		return errors.New("ingest: ingester stopped")
	}
	if _, ok := i.workers[sourceID]; ok {
		i.mu.Unlock()
		return ErrSourceAlreadySubmitted
	}
	format, location := i.deriveSourceFields(sourceID)
	w := &worker{
		sourceID:      sourceID,
		sourceFormat:  format,
		location:      location,
		fts5IndexLogs: i.resolveFTS5IndexLogs(sourceID),
		events:        events,
		db:            i.db,
		hwm:           i.hwm,
		pricer:        i.pricer,
		logger:        i.logger.With("source_id", sourceID),
		batchSize:     i.batchSize,
		batchEvery:    i.batchInterval,
		now:           i.now,
	}
	i.workers[sourceID] = w
	ctx := i.ctx
	i.mu.Unlock()
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		w.run(ctx)
	}()
	return nil
}

// Stop cancels the workers' context, waits for every worker to drain
// its pending batch, stops the resolver, and returns. Safe to call
// multiple times.
func (i *Ingester) Stop() error {
	i.mu.Lock()
	if !i.started {
		i.mu.Unlock()
		return ErrNotStarted
	}
	if i.stopped {
		i.mu.Unlock()
		return nil
	}
	i.stopped = true
	cancel := i.cancelFn
	resolver := i.resolver
	i.mu.Unlock()
	if resolver != nil {
		resolver.Stop()
	}
	if cancel != nil {
		cancel()
	}
	i.wg.Wait()
	return nil
}

// HWM returns the current per-source observability counter (max
// SourceSeq seen) for sourceID — surfaced via /api/health and used by
// tests to assert the counter advanced. It is NOT a dedup gate; the name
// is retained to avoid out-of-scope churn (SOW-0015). See hwmCache.
func (i *Ingester) HWM(sourceID string) uint64 {
	return i.hwm.Get(sourceID)
}

// ResolveOrphans triggers one resolver pass synchronously. Useful for
// tests that want to drive parent linkage without waiting for the
// background ticker.
func (i *Ingester) ResolveOrphans(ctx context.Context) error {
	if i.resolver == nil {
		return nil
	}
	return i.resolver.linkOrphans(ctx)
}

// deriveSourceFields returns (format, location) for sourceID. Format
// and location overrides win over the parsed values.
func (i *Ingester) deriveSourceFields(sourceID string) (format, location string) {
	format = i.formatOverrides[sourceID]
	location = i.locationOverrides[sourceID]
	if format != "" && location != "" {
		return
	}
	parsedFormat, parsedLoc := parseSourceID(sourceID)
	if format == "" {
		format = parsedFormat
	}
	if location == "" {
		location = parsedLoc
	}
	return
}

// resolveFTS5IndexLogs returns whether sourceID's FTS5 log index should be
// populated. A registered WithFTS5IndexLogs override wins; absence resolves to
// true (opt-OUT default — logs are indexed unless the operator disables it),
// matching the sources.fts5_index_logs column default. The worker persists the
// resolved value on the sources row; nothing reads it yet (config plumbing).
func (i *Ingester) resolveFTS5IndexLogs(sourceID string) bool {
	if v, ok := i.fts5IndexLogsOverrides[sourceID]; ok {
		return v
	}
	return true
}

// parseSourceID splits "format:location" into its two parts. Returns
// (sourceID, "") when no ':' is present so the ingester degrades
// gracefully on malformed ids.
func parseSourceID(s string) (format, location string) {
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}
