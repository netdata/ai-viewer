package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Default batching / resolver parameters. Override via Option.
const (
	// defaultBatchSize is the events-per-batch flush threshold. Larger batches
	// reduce per-tx overhead (the BeginTx + Commit round-trip is the dominant
	// cost per batch on modernc's serialized single connection). 5000 is a good
	// balance: a 5000-event tx is still well under SQLite's 500ms busy_timeout,
	// but reduces the tx count 5x vs the old 1000. The deferred-read-models
	// fast path (SOW-0063) makes each batch cheap (INSERTs + aggregate refresh
	// only, no FTS/rollup recompute), so the larger batch is purely a win.
	defaultBatchSize = 5000
	// defaultBatchInterval is the max time between flushes when the batch hasn't
	// reached the size threshold. Keeps the UI seeing fresh data during a slow
	// tail without waiting for a full batch. Unchanged from the original 500ms.
	defaultBatchInterval       = 500 * time.Millisecond
	defaultResolverInterval    = 5 * time.Second
	defaultIngesterStopTimeout = 30 * time.Second
	finalResolverStopTimeout   = 5 * time.Second
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

// ErrShutdownTimeout is returned when StopContext cannot finish inside the
// caller's deadline.
var ErrShutdownTimeout = errors.New("ingest: shutdown timeout")

// ErrReplayRequired is returned by shutdown drain paths that deliberately leave
// source progress unadvanced so the next run replays uncommitted source records.
var ErrReplayRequired = errors.New("ingest: replay required")

type replayRequiredError struct {
	reason        string
	pendingEvents int
	cause         error
}

func (e *replayRequiredError) Error() string {
	return fmt.Sprintf("%s: flush (%s) could not commit %d events before shutdown deadline: %v",
		ErrReplayRequired, e.reason, e.pendingEvents, e.cause)
}

func (e *replayRequiredError) Unwrap() error {
	return e.cause
}

func (e *replayRequiredError) Is(target error) bool {
	return target == ErrReplayRequired
}

// ShutdownOutcome classifies bounded shutdown results.
type ShutdownOutcome string

const (
	// ShutdownClean means every worker and the final resolver pass completed.
	ShutdownClean ShutdownOutcome = "clean"
	// ShutdownReplayRequired means at least one worker preserved uncommitted work for the next run.
	ShutdownReplayRequired ShutdownOutcome = "replay_required"
	// ShutdownTimeout means workers did not drain before the caller's deadline.
	ShutdownTimeout ShutdownOutcome = "timeout"
	// ShutdownWorkerFailure means a worker returned a non-replayable error.
	ShutdownWorkerFailure ShutdownOutcome = "worker_failure"
	// ShutdownResolverTimeout means the final resolver pass exceeded its shutdown budget.
	ShutdownResolverTimeout ShutdownOutcome = "resolver_timeout"
	// ShutdownAlreadyStopping means another caller owns the active shutdown.
	ShutdownAlreadyStopping ShutdownOutcome = "already_stopping"
	// ShutdownAlreadyStopped means shutdown already completed.
	ShutdownAlreadyStopped ShutdownOutcome = "already_stopped"
)

// ShutdownResult is the typed StopContext result consumed by commands and tests.
type ShutdownResult struct {
	Outcome ShutdownOutcome
}

type stopState uint8

const (
	stopStateIdle stopState = iota
	stopStateStopping
	stopStateStopped
)

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
// sources.fts5_index_logs column default. The worker persists the flag on the
// sources row and applyLogEntry reads it to gate fts_logs population (fts_ops
// is always indexed; data-model.md §Full-text search).
func WithFTS5IndexLogs(sourceID string, enabled bool) Option {
	return func(i *Ingester) {
		i.fts5IndexLogsOverrides[sourceID] = enabled
	}
}

// WithSourceMeta registers an adapter-owned JSON metadata blob to persist on
// the sources.meta_json column for sourceID (SOW-0024). The blob is the
// general per-source metadata surface: any adapter can populate it with
// source-native metadata that has no canonical-column analog, and the
// presenter renders it verbatim under each source in /api/health and
// /api/sources (omitted when NULL). The override is the runtime source of
// truth and is re-asserted on the sources row at every batch flush, so a
// daemon restart re-applies the configured value. The empty string is a
// no-op (the resolver returns "" and the worker binds NULL to the column —
// the absence = "not populated" signal the presenter honors by omitting
// the field). Marshalling is the caller's responsibility: opencode's
// auto-discovery probe is the only consumer today, and it json.Marshals the
// ProbeStatus result before calling this option.
//
// Ordering contract: this option must be applied BEFORE the first Submit
// for the given sourceID (Submit copies the resolved value into the worker
// under i.mu; the resolver goroutine does not read this map). Applying it
// concurrently with, or after, a Submit for the same sourceID is a data
// race on the overrides map. The production caller (cmd/ai-viewer-ingest)
// applies all WithSourceMeta registrations in the main goroutine before the
// startSource loop; do the same. (Constructor-only application via
// ingest.New's variadic ...Option, like the other With* options, avoids the
// concern entirely and is preferred where the metadata is known at New
// time.)
func WithSourceMeta(sourceID, metaJSON string) Option {
	return func(i *Ingester) {
		i.sourceMetaOverrides[sourceID] = metaJSON
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

	mu        sync.Mutex
	started   bool
	stopped   bool
	stopState stopState
	cancelFn  context.CancelFunc
	wg        sync.WaitGroup
	workers   map[string]*worker
	ctx       context.Context
	errMu     sync.Mutex
	errs      []error

	formatOverrides   map[string]string
	locationOverrides map[string]string
	// fts5IndexLogsOverrides maps sourceID → whether its FTS5 log index should
	// be populated. Set by WithFTS5IndexLogs; absence resolves to the default
	// (true) in resolveFTS5IndexLogs. Persisted on the sources row by the worker
	// and read by applyLogEntry to gate fts_logs population.
	fts5IndexLogsOverrides map[string]bool
	// sourceMetaOverrides maps sourceID → the adapter-owned JSON metadata blob
	// to persist on sources.meta_json. Set by WithSourceMeta; absence resolves
	// to "" in resolveSourceMeta (the worker binds NULL to the column — the
	// absence = "not populated" signal). Persisted on the sources row by the
	// worker; rendered verbatim by the presenter under /api/health and
	// /api/sources (SOW-0024, data-model.md §sources).
	sourceMetaOverrides map[string]string
	// deferReadModels is the bulk-scan fast-path flag (SOW-0063). When true,
	// the worker skips refreshRollups + refreshFTS during batch flush — the two
	// read-model refreshes that are super-linear in accumulated data volume.
	// refreshAggregates (cheap session-count update) still runs so the UI shows
	// correct counts. The binary sets this true before Scan, clears it after
	// Scan returns, and runs BackfillReadModels (BackfillFTS + BackfillRollups)
	// to build the deferred read models once. During Tail (steady state),
	// incremental refresh runs as normal. atomic so the main goroutine can
	// toggle it while the worker goroutine reads it without a lock.
	deferReadModels atomic.Bool
	// backfillMu serializes BackfillReadModels across the 5 source goroutines
	// that finish scanning concurrently. backfillDone is set only on success,
	// so a failed backfill (e.g. context cancelled during shutdown) allows the
	// next caller to retry. Without this, sync.Once would fail-once-forever
	// if the first caller's context was cancelled (SOW-0063).
	backfillMu   sync.Mutex
	backfillDone bool
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
		sourceMetaOverrides:    make(map[string]string),
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
		sourceID:        sourceID,
		sourceFormat:    format,
		location:        location,
		fts5IndexLogs:   i.resolveFTS5IndexLogs(sourceID),
		metaJSON:        i.resolveSourceMeta(sourceID),
		events:          events,
		db:              i.db,
		hwm:             i.hwm,
		pricer:          i.pricer,
		logger:          i.logger.With("source_id", sourceID),
		batchSize:       i.batchSize,
		batchEvery:      i.batchInterval,
		now:             i.now,
		deferReadModels: &i.deferReadModels,
	}
	w.onErr = func(err error) {
		i.recordWorkerError(sourceID, err)
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

// Stop cancels the workers' context, waits for every worker to drain within the
// bounded default timeout, runs one final resolver pass over the committed rows,
// stops the resolver, and returns. Safe to call multiple times.
func (i *Ingester) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultIngesterStopTimeout)
	defer cancel()
	result, err := i.StopContext(ctx)
	switch result.Outcome {
	case ShutdownClean, ShutdownReplayRequired, ShutdownAlreadyStopping, ShutdownAlreadyStopped:
		return nil
	default:
		return err
	}
}

// StopContext is the bounded shutdown API. It preserves uncommitted batches for
// replay when ctx expires and returns typed owner/follower outcomes so callers
// can map shutdown to process exit codes without parsing error text.
func (i *Ingester) StopContext(ctx context.Context) (ShutdownResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	i.mu.Lock()
	if !i.started {
		i.mu.Unlock()
		return ShutdownResult{}, ErrNotStarted
	}
	switch i.stopState {
	case stopStateStopping:
		i.mu.Unlock()
		return ShutdownResult{Outcome: ShutdownAlreadyStopping}, nil
	case stopStateStopped:
		i.mu.Unlock()
		return ShutdownResult{Outcome: ShutdownAlreadyStopped}, nil
	}
	i.stopped = true
	i.stopState = stopStateStopping
	cancel := i.cancelFn
	resolver := i.resolver
	i.mu.Unlock()

	defer func() {
		i.mu.Lock()
		i.stopState = stopStateStopped
		i.mu.Unlock()
	}()

	if cancel != nil {
		cancel()
	}

	workersDone := make(chan struct{})
	go func() {
		i.wg.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-ctx.Done():
		if resolver != nil {
			resolver.Stop()
		}
		return ShutdownResult{Outcome: ShutdownTimeout}, ErrShutdownTimeout
	}

	var resolverErr error
	if resolver != nil {
		resolverCtx, cancelResolver, ok := boundedResolverContext(ctx)
		if !ok {
			resolver.Stop()
			if i.logger != nil {
				i.logger.Warn("shutdown_resolver_timeout",
					"remaining_ms", 0,
					"timeout_ms", finalResolverStopTimeout.Milliseconds())
			}
			return ShutdownResult{Outcome: ShutdownResolverTimeout}, ErrShutdownTimeout
		}
		resolverErr = resolver.linkOrphans(resolverCtx)
		cancelResolver()
		resolver.Stop()
	}
	replayOnly, workerErr := i.workerError()
	if workerErr != nil && resolverErr != nil {
		return ShutdownResult{Outcome: ShutdownWorkerFailure},
			errors.Join(workerErr, fmt.Errorf("final resolver: %w", resolverErr))
	}
	if workerErr != nil {
		if replayOnly {
			return ShutdownResult{Outcome: ShutdownReplayRequired}, workerErr
		}
		return ShutdownResult{Outcome: ShutdownWorkerFailure}, workerErr
	}
	if resolverErr != nil {
		if errors.Is(resolverErr, context.DeadlineExceeded) || errors.Is(resolverErr, context.Canceled) {
			if i.logger != nil {
				i.logger.Warn("shutdown_resolver_timeout",
					"remaining_ms", remainingMillis(ctx),
					"timeout_ms", finalResolverStopTimeout.Milliseconds())
			}
			return ShutdownResult{Outcome: ShutdownResolverTimeout}, ErrShutdownTimeout
		}
		return ShutdownResult{Outcome: ShutdownWorkerFailure}, resolverErr
	}
	return ShutdownResult{Outcome: ShutdownClean}, nil
}

func boundedResolverContext(parent context.Context) (context.Context, context.CancelFunc, bool) {
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, func() {}, false
		}
		if remaining < finalResolverStopTimeout {
			ctx, cancel := context.WithTimeout(parent, remaining)
			return ctx, cancel, true
		}
	}
	ctx, cancel := context.WithTimeout(parent, finalResolverStopTimeout)
	return ctx, cancel, true
}

func remainingMillis(ctx context.Context) int64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline).Milliseconds()
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (i *Ingester) recordWorkerError(sourceID string, err error) {
	if err == nil {
		return
	}
	i.errMu.Lock()
	i.errs = append(i.errs, err)
	i.errMu.Unlock()
	if i.logger != nil {
		if errors.Is(err, ErrReplayRequired) {
			sourceFormat, _, _ := strings.Cut(sourceID, ":")
			attrs := []any{
				"source_id", sourceID,
				"source_format", sourceFormat,
				"outcome", ShutdownReplayRequired,
				"err", err,
			}
			var replayErr *replayRequiredError
			if errors.As(err, &replayErr) {
				attrs = append(attrs,
					"pending_events", replayErr.pendingEvents,
					"reason", replayErr.reason)
			}
			i.logger.Warn("shutdown_replay_required", attrs...)
			return
		}
		i.logger.Error("worker: batch failed",
			"source_id", sourceID,
			"err", err)
	}
}

func (i *Ingester) workerError() (bool, error) {
	i.errMu.Lock()
	defer i.errMu.Unlock()
	if len(i.errs) == 0 {
		return false, nil
	}
	replayOnly := true
	for _, err := range i.errs {
		if !errors.Is(err, ErrReplayRequired) {
			replayOnly = false
			break
		}
	}
	return replayOnly, fmt.Errorf("ingest worker errors: %w", errors.Join(i.errs...))
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

// SetDeferReadModels toggles the bulk-scan fast-path (SOW-0063). When true,
// the worker skips refreshRollups + refreshFTS during batch flush; the binary
// calls BackfillReadModels after the scan to build them once. Thread-safe
// (atomic).
func (i *Ingester) SetDeferReadModels(b bool) { i.deferReadModels.Store(b) }

// DeferReadModels reports whether the bulk-scan fast-path is active.
func (i *Ingester) DeferReadModels() bool { return i.deferReadModels.Load() }

// BackfillReadModels rebuilds the FTS index + rollup tables from the committed
// data (SOW-0063). Called by the binary after the initial Scan completes and
// before Tail starts, so the read models deferred during the bulk scan are
// built once in a single pass rather than incrementally per-batch (which is
// super-linear in data volume). Uses the existing tested BackfillFTS +
// BackfillRollups functions. Serialized by a mutex so concurrent source
// goroutines don't both truncate fts_ops (which would deadlock on the single
// SQLite connection). Allows retry on failure: backfillDone is set only on
// success.
func (i *Ingester) BackfillReadModels(ctx context.Context) error {
	i.backfillMu.Lock()
	defer i.backfillMu.Unlock()
	if i.backfillDone {
		return nil
	}
	i.deferReadModels.Store(false)
	now := defaultNow()
	if i.now != nil {
		now = i.now()
	}
	if i.logger != nil {
		i.logger.Info("ai-viewer-ingest: backfilling read models (FTS + rollups)")
	}
	ftsStats, err := BackfillFTS(ctx, i.db, i.logger)
	if err != nil {
		return fmt.Errorf("backfill FTS: %w", err)
	}
	rollupStats, err := BackfillRollups(ctx, i.db, now, i.logger)
	if err != nil {
		return fmt.Errorf("backfill rollups: %w", err)
	}
	i.backfillDone = true
	if i.logger != nil {
		i.logger.Info("ai-viewer-ingest: read models backfilled",
			"fts_op_rows", ftsStats.OpRows,
			"fts_log_rows", ftsStats.LogRows,
			"hourly_rows", rollupStats.HourlyRows,
			"daily_rows", rollupStats.DailyRows,
			"days", rollupStats.DaysProcessed,
			"elapsed_s", rollupStats.Elapsed.Seconds())
	}
	return nil
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
// resolved value on the sources row, where it gates fts_logs indexing: the FTS
// backfill and /api/search both filter on src.fts5_index_logs = 1.
func (i *Ingester) resolveFTS5IndexLogs(sourceID string) bool {
	if v, ok := i.fts5IndexLogsOverrides[sourceID]; ok {
		return v
	}
	return true
}

// resolveSourceMeta returns the adapter-owned JSON metadata blob to persist on
// sources.meta_json for sourceID. A registered WithSourceMeta override wins;
// absence (or an empty string) resolves to "" so the worker binds NULL to the
// column — the absence = "not populated" signal the presenter honors by
// omitting the field. SOW-0024.
func (i *Ingester) resolveSourceMeta(sourceID string) string {
	return i.sourceMetaOverrides[sourceID]
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
