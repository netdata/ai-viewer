package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// Default batching / resolver parameters. Override via Option.
const (
	// defaultBatchSize bounds one SQLite writer transaction. Source lifecycle
	// and liveness writes share the same single writer; live cold-rebuild
	// evidence showed very large scan/repair batches can starve the 30s tail
	// heartbeat and stale-tail writes. SOW-0118 measured raising this
	// (100→500) and found it REGRESSED throughput and triggered tail-stale:
	// with SetMaxOpenConns(1), larger batches make each flush hold the single
	// connection longer, worsening begin contention and starving liveness
	// writes. The real bottleneck is connection contention, not per-flush
	// overhead (see SOW-0118). 100 retained.
	defaultBatchSize = 10000
	// defaultBatchInterval is the max time between flushes when the batch hasn't
	// reached the size threshold. Keeps the UI seeing fresh data during a slow
	// tail without waiting for a full batch. Unchanged from the original 500ms.
	defaultBatchInterval       = 500 * time.Millisecond
	defaultResolverInterval    = 5 * time.Second
	defaultIngesterStopTimeout = 30 * time.Second
	finalResolverStopTimeout   = 5 * time.Second
	defaultTailStaleAfter      = 5 * time.Minute
	defaultTailWatchdogEvery   = 30 * time.Second
	defaultTailHeartbeatEvery  = 30 * time.Second
	defaultTailStateWriteWait  = 30 * time.Second
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

// WithTailLiveness overrides Tail heartbeat watchdog timings.
func WithTailLiveness(staleAfter, watchdogEvery, heartbeatPersistEvery time.Duration) Option {
	return func(i *Ingester) {
		if staleAfter > 0 {
			i.tailLiveness.staleAfter = staleAfter
		}
		if watchdogEvery > 0 {
			i.tailLiveness.watchdogEvery = watchdogEvery
		}
		if heartbeatPersistEvery > 0 {
			i.tailLiveness.heartbeatPersistEvery = heartbeatPersistEvery
		}
	}
}

// WithTailStateWriteTimeout overrides the maximum time a liveness state write
// may wait for SQLite before the liveness loop logs and retries later.
func WithTailStateWriteTimeout(d time.Duration) Option {
	return func(i *Ingester) {
		if d > 0 {
			i.tailLiveness.stateWriteTimeout = d
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
	// deferReadModels is the legacy default for source-scoped startup Scan
	// deferral. Normal daemon startup sets readModelDeferrals per source before
	// Submit; this default remains for isolated callers that opt in before
	// creating workers. Workers still commit canonical rows, source_progress,
	// aggregates, and repair debt while derived FTS/rollup refresh is deferred.
	deferReadModels        atomic.Bool
	readModelRebuildActive atomic.Bool
	// startupScanActive is true while any source's initial Scan is in progress
	// (set by the binary from scanDone). The resolver reads it via deferredNow
	// to skip its connection-monopolizing link passes during the scan (SOW-0118).
	// Initialized to TRUE so the resolver is deferred from its very first tick
	// (Start launches the resolver goroutine before main.go calls
	// SetStartupScanActive; without this the resolver's first pass runs before
	// the deferral is wired and monopolizes the connection for minutes on the
	// large DB).
	startupScanActive atomic.Bool
	// ingestionGen is bumped on every committed canonical batch (sessions OR
	// ops). The resolver reads it via ingestionGenNow to skip its O(all-rows)
	// link passes when nothing has been committed since the last pass — the
	// difference between a ~1-core idle burn and a near-zero idle daemon
	// (SOW-0117). It is monotonic and never reset.
	ingestionGen atomic.Int64
	// backfillMu serializes BackfillReadModels across the 5 source goroutines
	// that finish scanning concurrently. backfillDone is set only on success,
	// so a failed backfill (e.g. context cancelled during shutdown) allows the
	// next caller to retry. Without this, sync.Once would fail-once-forever
	// if the first caller's context was cancelled (SOW-0063).
	backfillMu   sync.Mutex
	backfillDone bool

	readModelDeferralMu  sync.Mutex
	readModelDeferrals   map[string]*atomic.Bool
	readModelRepairMu    sync.Mutex
	readModelRepairChans map[string]chan<- struct{}

	tailMu               sync.Mutex
	tailLiveness         tailLivenessConfig
	tailHeartbeats       map[string]tailHeartbeatState
	tailRestartChans     map[string]chan<- struct{}
	tailHeartbeatPersist chan tailHeartbeatPersistRequest
	tailStatePending     atomic.Int64

	// coalescer (SOW-0118): replaces per-source worker goroutines with a single
	// writer goroutine that owns the connection. nil = legacy per-source mode.
	coalescer *coalescer
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
		readModelDeferrals:     make(map[string]*atomic.Bool),
		readModelRepairChans:   make(map[string]chan<- struct{}),
		tailLiveness: tailLivenessConfig{
			staleAfter:            defaultTailStaleAfter,
			watchdogEvery:         defaultTailWatchdogEvery,
			heartbeatPersistEvery: defaultTailHeartbeatEvery,
			stateWriteTimeout:     defaultTailStateWriteWait,
		},
		tailHeartbeats:       make(map[string]tailHeartbeatState),
		tailRestartChans:     make(map[string]chan<- struct{}),
		tailHeartbeatPersist: make(chan tailHeartbeatPersistRequest, 1024),
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
	i.resolver.waitBeforeDBWork = i.waitForTailStatePriority
	i.resolver.ingestionGenNow = i.ingestionGen.Load
	i.resolver.sessionWatermarkNow = i.sessionLinkWatermark
	i.resolver.deferredNow = func() bool { return i.startupScanActive.Load() || i.readModelRebuildActive.Load() }
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.resolver.loop(ctx)
	}()
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.tailHeartbeatPersistenceLoop(ctx)
	}()
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.tailStaleWatchdogLoop(ctx)
	}()
	i.started = true

	// SOW-0118: the coalescer replaces per-source worker goroutines with a
	// single writer goroutine that owns the connection, eliminating the 80%+
	// begin-wait from 5 workers competing for SetMaxOpenConns(1).
	i.coalescer = newCoalescer(i.db, i.logger.With("subsystem", "ingest"), i.batchSize, i.batchInterval)
	i.coalescer.onCommittedBatch = i.bumpIngestionGen
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.coalescer.run(ctx)
	}()
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
		sourceID:               sourceID,
		sourceFormat:           format,
		location:               location,
		fts5IndexLogs:          i.resolveFTS5IndexLogs(sourceID),
		metaJSON:               i.resolveSourceMeta(sourceID),
		events:                 events,
		db:                     i.db,
		hwm:                    i.hwm,
		pricer:                 i.pricer,
		logger:                 i.logger.With("source_id", sourceID),
		batchSize:              i.batchSize,
		batchEvery:             i.batchInterval,
		now:                    i.now,
		deferReadModels:        i.readModelDeferralFlag(sourceID),
		readModelRebuildActive: &i.readModelRebuildActive,
		requestReadModelRepair: i.RequestSourceReadModelRepair,
		onCommittedBatch:       i.bumpIngestionGen,
		stageTiming:            newFlushStageTiming(sourceID, i.logger.With("subsystem", "ingest")),
		waitBeforeDBWork:       i.waitForTailStatePriority,
	}
	w.onErr = func(err error) {
		i.recordWorkerError(sourceID, err)
	}
	i.workers[sourceID] = w
	i.mu.Unlock()

	// SOW-0118: register the source with the coalescer (single writer goroutine)
	// instead of starting a per-source workerRuntime.run() goroutine. The
	// coalescer drains all sources through a merged channel and flushes in a
	// single transaction, eliminating the begin-wait.
	i.coalescer.register(sourceID, w, events)
	// Yield to the merger goroutine so it has a chance to start forwarding
	// events before the caller proceeds (Submit→Stop test patterns depend on
	// the coalescer being ready to receive; without this, the merger goroutine
	// might not be scheduled yet when Stop fires). SOW-0118.
	runtime.Gosched()
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

// SetDeferReadModels toggles the legacy bulk-scan fast-path default (SOW-0063).
// New source workers inherit this value, but existing source-owned deferral flags
// are not mutated. Normal daemon startup uses SetSourceReadModelsDeferred per
// source instead; this helper remains only for isolated/offline callers that set
// the default before Submit.
func (i *Ingester) SetDeferReadModels(b bool) {
	i.deferReadModels.Store(b)
}

// DeferReadModels reports whether the bulk-scan fast-path is active.
func (i *Ingester) DeferReadModels() bool { return i.deferReadModels.Load() }

// SetStartupScanActive toggles the startup-scan flag the resolver uses to defer
// its connection-monopolizing link passes while sources are still doing their
// initial Scan (SOW-0118). The binary sets it true before starting sources and
// false once every source's Scan has reached an outcome (scanDone).
func (i *Ingester) SetStartupScanActive(active bool) {
	i.startupScanActive.Store(active)
}

// IsStartupScanActive reports whether the initial multi-source Scan is still in
// progress. Source supervisors consult it to defer per-source read-model repair
// during the scan (SOW-0118): the repair monopolizes the single writer
// connection and starves the scan, and BackfillReadModels rebuilds read models
// once after the scan anyway.
func (i *Ingester) IsStartupScanActive() bool {
	return i.startupScanActive.Load()
}

// bumpIngestionGen advances the ingestion generation counter. Called on every
// committed canonical batch (sessions OR ops) via the worker's onCommittedBatch
// hook. The resolver reads it to skip its O(all-rows) link passes when nothing
// has been committed since its last pass (SOW-0117): the difference between a
// ~1-core idle burn and a near-zero idle daemon.
func (i *Ingester) bumpIngestionGen() {
	i.ingestionGen.Add(1)
}

// sessionLinkWatermark returns MAX(sessions.last_activity_ts), a cheap
// covering-index probe (idx_sessions_activity) the resolver uses as a
// CONSERVATIVE signal for whether the two SESSION link passes (linkParents,
// linkRoots) need to run. It advances when a session is inserted/updated, and
// also when a session gains an op with a newer end_ts (the aggregate refresh at
// aggregates.go:75 sets last_activity_ts = MAX(existing, MAX(ops.end_ts))). It is
// therefore an OVER-APPROXIMATION of "a session changed": the session passes
// may run a little more often than strictly needed (harmless — they find
// nothing), but they never miss a real session change, because every path that
// could need re-rooting advances last_activity_ts. This makes the gate effective
// exactly where it matters — idle (no commits) and resume scans of unchanged
// sessions (the common restart case) — where MAX(last_activity_ts) is stable so
// the session passes (incl. the O(sessions) linkRoots recursive CTE) are skipped
// (SOW-0117). Returns an error on query failure; the resolver falls back to
// running all passes.
func (i *Ingester) sessionLinkWatermark(ctx context.Context) (int64, error) {
	var wm sql.NullInt64
	err := i.db.QueryRowContext(ctx, `SELECT MAX(last_activity_ts) FROM sessions`).Scan(&wm)
	if err != nil {
		return 0, fmt.Errorf("resolver session watermark: %w", err)
	}
	return wm.Int64, nil
}

// SetSourceReadModelsDeferred overrides the read-model fast-path for one
// source. It is safe to call before or after Submit; Submit will use the same
// per-source flag. The returned value is the previous state, so callers can
// decide whether clearing the flag requires a source-scoped repair pass.
func (i *Ingester) SetSourceReadModelsDeferred(sourceID string, deferred bool) bool {
	flag := i.readModelDeferralFlag(sourceID)
	wasDeferred := flag.Load()
	flag.Store(deferred)
	return wasDeferred
}

func (i *Ingester) readModelDeferralFlag(sourceID string) *atomic.Bool {
	i.readModelDeferralMu.Lock()
	defer i.readModelDeferralMu.Unlock()
	if flag, ok := i.readModelDeferrals[sourceID]; ok {
		return flag
	}
	flag := &atomic.Bool{}
	flag.Store(i.deferReadModels.Load())
	i.readModelDeferrals[sourceID] = flag
	return flag
}

// BackfillReadModels rebuilds the FTS index + rollup tables from committed data
// (SOW-0063/SOW-0114). The daemon runs it after every startup Scan reaches an
// outcome as retained all-sources reconciliation, while sources may already be in
// Tail. It only raises the global rebuild-active flag; source-owned deferral and
// repair state are cleared by source-scoped repair, because Tail batches can
// commit fresh repair debt while this rebuild is streaming. Serialized by a mutex
// so concurrent callers don't both truncate fts_ops. Allows retry on failure:
// backfillDone is set only on success.
func (i *Ingester) BackfillReadModels(ctx context.Context) error {
	i.backfillMu.Lock()
	defer i.backfillMu.Unlock()
	if i.backfillDone {
		return nil
	}
	i.readModelRebuildActive.Store(true)
	defer i.readModelRebuildActive.Store(false)
	now := defaultNow()
	if i.now != nil {
		now = i.now()
	}
	if i.logger != nil {
		i.logger.Info("ai-viewer-ingest: backfilling read models (FTS + rollups)")
	}
	ftsStats, err := backfillFTSWithYield(ctx, i.db, i.logger, i.waitForTailStatePriority)
	if err != nil {
		return i.failBackfillReadModels(ctx, fmt.Errorf("backfill FTS: %w", err))
	}
	rollupStats, err := BackfillRollups(ctx, i.db, now, i.logger, withBackfillYield(i.waitForTailStatePriority))
	if err != nil {
		return i.failBackfillReadModels(ctx, fmt.Errorf("backfill rollups: %w", err))
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

func (i *Ingester) failBackfillReadModels(ctx context.Context, cause error) error {
	repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	sourceIDs, err := i.markAllSourceReadModelsRepairPending(repairCtx)
	for _, sourceID := range sourceIDs {
		i.RequestSourceReadModelRepair(sourceID)
	}
	if i.logger != nil {
		i.logger.Error("ai-viewer-ingest: read-model backfill failed; sources marked repair_pending",
			"source_count", len(sourceIDs),
			"err", cause)
	}
	if err != nil {
		return errors.Join(cause, fmt.Errorf("mark all source read models repair pending: %w", err))
	}
	return cause
}

func (i *Ingester) markAllSourceReadModelsRepairPending(ctx context.Context) ([]string, error) {
	rows, err := i.db.QueryContext(ctx, `SELECT id, format, location FROM sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load sources: %w", err)
	}
	var sourceIDs []string
	var sources []SourceRegistration
	for rows.Next() {
		var src SourceRegistration
		if err := rows.Scan(&src.ID, &src.Format, &src.Location); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sourceIDs = append(sourceIDs, src.ID)
		sources = append(sources, src)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate sources: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close sources: %w", err)
	}
	if len(sourceIDs) == 0 {
		return sourceIDs, nil
	}
	tsUS := defaultNow()
	if i.now != nil {
		tsUS = i.now()
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return sourceIDs, fmt.Errorf("mark repair_pending begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, src := range sources {
		if err := ensureSourceProgressLifecycleRow(ctx, tx, src.ID, tsUS); err != nil {
			return sourceIDs, err
		}
		changed, err := updateReadModelColumns(ctx, tx, src.ID, src.Location, tsUS, SourceLifecycleUpdate{
			ReadModelState:               ReadModelRepairPending,
			ReadModelStateTransitionOnly: true,
			ClearReadModelError:          true,
		})
		if err != nil {
			return sourceIDs, err
		}
		if changed {
			if err := insertSourceStatusNotify(ctx, tx, src.ID, tsUS); err != nil {
				return sourceIDs, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return sourceIDs, fmt.Errorf("mark repair_pending commit: %w", err)
	}
	return sourceIDs, nil
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
