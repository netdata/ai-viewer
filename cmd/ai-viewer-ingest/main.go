// Command ai-viewer-ingest is the daemon that watches configured
// session-snapshot sources, parses them via internal/adapters, and
// writes canonical rows into the SQLite store.
//
// Lifecycle:
//
//   - parse CLI flags + configure slog
//   - ensure state-dir + db parent dir exist
//   - open the canonical SQLite store read-write (runs migrations)
//   - construct pricing.Pricer + ingest.Ingester (Chunk 11 wiring)
//   - resolve the source list (explicit --source flags replace
//     auto-discovery)
//   - per source: instantiate the adapter via the registry, spawn
//     scan + tail goroutines feeding into the ingester
//   - on SIGTERM/SIGINT: cancel ingest context → wait for graceful
//     shutdown → close the store → exit 0
//
// Read ingester.md, adapter-contract.md, deployment.md, pricing.md and
// security.md before changing CLI flags or auto-discovery defaults.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"

	// net/http/pprof registers /debug/pprof/* handlers on
	// http.DefaultServeMux when imported with a blank identifier.
	// SOW-0094: enables heap + goroutine + profile capture for the
	// ingest memory-leak investigation. No runtime cost when
	// --pprof is not set (no server is started). Gated on the operator-
	// supplied --pprof flag (default empty); gosec G108 is a false positive
	// because the endpoint is bound to the operator's loopback only and
	// never started unless --pprof is passed explicitly.
	//nolint:gosec // operator-gated, loopback-only, off by default
	_ "net/http/pprof" //#nosec G108 -- operator-gated, loopback-only, off by default
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	// Side-effect imports: each adapter package registers its factory
	// with internal/adapters via init(). Without these blank imports
	// the registry would be empty at runtime.
	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	_ "github.com/netdata/ai-viewer/internal/adapters/aiagent_v3"
	_ "github.com/netdata/ai-viewer/internal/adapters/claude_code"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/pricing"
	"github.com/netdata/ai-viewer/internal/store"
)

// adapterEventChanSize is the buffered channel capacity between an
// adapter and the ingester worker. The 1024 cap matches the
// adapter-contract: bursty Scan emissions are flow-controlled by the
// channel; sustained throughput is bounded by the ingester's
// 1000-events-or-500ms batching policy.
const adapterEventChanSize = 1024

// adapterContextGracePeriod is the time the binary waits for adapter
// goroutines to drain after the ingester has stopped. Bounded so a
// stuck Tail cannot hold up shutdown forever.
const adapterContextGracePeriod = 5 * time.Second

// readModelBackfillStartupTimeout bounds the derived-data repair that runs
// between historical Scan and live Tail. Primary canonical ingestion must not
// stay offline indefinitely because FTS/rollup rebuilds are expensive or stuck.
const (
	readModelBackfillStartupTimeout = 5 * time.Minute
	ingesterStopTimeout             = 30 * time.Second
	backfillCancelWait              = 5 * time.Second
)

var writerStoreCloseTimeout = 5 * time.Second

// stateDirPerm is the permission bits applied when the binary creates
// its state directory + DB parent directory. 0o750 keeps the operator
// owning read/write/execute, the operator's group read/execute, and
// no other-user access.
const stateDirPerm os.FileMode = 0o750

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entrypoint. Returns the process exit code so
// main() is a one-liner.
func run(args []string, stdout, stderr *os.File) int {
	rootCtx, stopSignals := signalContext(context.Background())
	defer stopSignals()

	// Thin subcommand dispatch. Existing invocations pass only flags
	// (leading '-'), so the daemon path below is preserved. A bare
	// subcommand first arg routes to a one-shot helper. The subcommand
	// dispatch is split into a small table so the surrounding `run` stays
	// below the cyclomatic-complexity gate (SOW-0089 chunk 5b).
	if exitCode, ok := dispatchSubcommand(rootCtx, args, stdout, stderr); ok {
		return exitCode
	}

	cfg, exitCode, ok := parseFlags(args, stderr)
	if !ok {
		return exitCode
	}

	logger, err := newLogger(cfg.logLevel, cfg.logFormat, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest: %v\n", err)
		return 2
	}
	slog.SetDefault(logger)

	dbPath, err := resolveDBPath(cfg.dbPath)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to resolve --db", "err", err)
		return 1
	}
	stateDir, err := resolveStateDir(cfg.stateDir)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to resolve --state-dir", "err", err)
		return 1
	}
	if err := os.MkdirAll(stateDir, stateDirPerm); err != nil {
		logger.Error("ai-viewer-ingest: failed to create state dir", "dir", stateDir, "err", err)
		return 1
	}

	// Multi-process lockout (SOW-0094 chunk 3): only one ai-viewer-ingest
	// may run per state_dir at a time. The DB is a single-writer SQLite
	// file; a second ingester stepping in would either get SQLITE_BUSY
	// forever or, worse, silently corrupt the WAL. The historic failure
	// mode was the operator launching a second ingester from a debug
	// shell while the systemd one was still running — the leak hunt in
	// commit 91eb1b8 was triggered by three concurrent processes all
	// racing on the same DB.
	releaseLock, err := acquireSingleInstanceLock(stateDir, logger)
	if err != nil {
		return 1
	}
	defer releaseLock()
	if err := os.MkdirAll(filepath.Dir(dbPath), stateDirPerm); err != nil {
		logger.Error("ai-viewer-ingest: failed to create db parent dir",
			"dir", filepath.Dir(dbPath), "err", err)
		return 1
	}

	logger.Info(
		"ai-viewer-ingest starting",
		"db", dbPath,
		"state_dir", stateDir,
		"version", versionString(),
	)

	// Optional pprof endpoint for memory-leak investigation (SOW-0094).
	// Default off; gated on --pprof=<addr> to keep production deployments
	// unexposed. The endpoint serves /debug/pprof/heap, /debug/pprof/
	// goroutine, /debug/pprof/profile, etc. — the standard set.
	if cfg.pprofAddr != "" {
		go func() {
			logger.Info("ai-viewer-ingest pprof enabled", "addr", cfg.pprofAddr)
			// http.DefaultServeMux is registered with net/http/pprof in
			// init(); we just need a server bound to the operator-supplied
			// address. We do not use net/http.ListenAndServe because we
			// want an explicit ReadHeaderTimeout to avoid slowloris attacks
			// on the local-only port.
			srv := &http.Server{
				Addr:              cfg.pprofAddr,
				Handler:           http.DefaultServeMux,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Warn("ai-viewer-ingest: pprof server stopped", "err", err)
			}
		}()
	}

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()

	ws, err := openWriterStore(ctx, dbPath, logger)
	if err != nil {
		logIngestStartupSignalIfCanceled(rootCtx, logger)
		logger.Error("ai-viewer-ingest: failed to open store", "db", dbPath, "err", err)
		return 1
	}
	defer func() {
		if ws != nil {
			_ = closeStoreWithTimeout(ws, writerStoreCloseTimeout, logger)
		}
	}()

	pricer, err := pricing.New()
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to load embedded pricing data", "err", err)
		return 1
	}
	logger.Info(
		"ai-viewer-ingest pricing loaded",
		"hits", pricer.Stats().Hits,
		"miss_provider_model", pricer.Stats().MissProviderModel,
	)

	ing, err := ingest.New(
		ws.DB(),
		ingest.WithLogger(logger),
		ingest.WithPricer(pricer),
	)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to construct ingester", "err", err)
		return 1
	}
	if err := startIngester(ing, ctx); err != nil {
		logIngestStartupSignalIfCanceled(rootCtx, logger)
		logger.Error("ai-viewer-ingest: ingester start failed", "err", err)
		return 1
	}

	sources, err := resolveSources(cfg.sources, logger)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to resolve sources", "err", err)
		_ = ing.Stop()
		return 1
	}
	if len(sources) == 0 {
		logger.Warn("ai-viewer-ingest: no sources configured or discovered; idling",
			"hint", "use --source format:location or create one of the auto-discovery paths")
	}

	// Register per-source adapter-owned metadata before Submit so the worker
	// resolves it via WithSourceMeta on first flush (SOW-0024). The ingester
	// option is applied to the already-constructed ingester; this is safe
	// because no workers exist yet at this point (the loop below calls Submit,
	// which is the first point a worker goroutine starts) and the resolver
	// goroutine started by ing.Start reads only the source_progress HWM cache,
	// not the sourceMetaOverrides map. Empty metaJSON values are skipped — the
	// worker binds NULL for those sources (the omit-when-NULL contract).
	for _, src := range sources {
		if src.metaJSON == "" {
			continue
		}
		ingest.WithSourceMeta(src.id, src.metaJSON)(ing)
	}
	if err := ing.ReconcileSourceLifecycles(ctx, sourceRegistrations(sources)); err != nil {
		logger.Error("ai-viewer-ingest: failed to reconcile source lifecycle state", "err", err)
		_ = ing.Stop()
		return 1
	}

	adapterCtx, cancelAdapters := context.WithCancel(ctx)
	defer cancelAdapters()

	// cursorLookup is wired against the writer's DB handle. The reads
	// happen before the worker is started for that source, so they
	// cannot contend with the writer's own batch transactions.
	lookup := sqlCursorLookup{db: ws.DB()}

	// scanWG waits for all sources' Scan() to reach an outcome. The post-scan
	// read-model backfill runs as background reconciliation after ALL startup
	// scans finish. It does not gate per-source Tail startup.
	//
	// CRITICAL: scanWG.Add must happen BEFORE scanWG.Wait. The Wait goroutine
	// below starts immediately; if Add runs later (in the for loop below),
	// the Wait may see counter=0 and fire prematurely (the root cause of the
	// FTS backfill firing while scans are still running). Pre-add all counters
	// here, then the goroutine in startSourceWithFactoryLookup does NOT Add.
	var scanWG sync.WaitGroup
	scanWG.Add(len(sources))
	scanDone := make(chan struct{})
	go func() {
		scanWG.Wait()
		close(scanDone)
	}()

	// SOW-0118: defer the resolver's link passes while any source is still
	// doing its initial Scan. The resolver's slow link queries monopolize the
	// single writer connection and starve every worker flush (measured: 68–93%
	// flush time blocked in BeginTx, resolver holding the connection 10/10
	// samples). Linkage is eventually-consistent; one resolver pass after the
	// scan settles links all scan-time orphans. Cleared here when scanDone
	// fires; the post-scan read-model rebuild (readModelRebuildActive) keeps
	// the resolver deferred until that settles too.
	ing.SetStartupScanActive(len(sources) > 0)
	go func() {
		<-scanDone
		ing.SetStartupScanActive(false)
	}()

	_, backfillWait := startPostScanBackfill(ctx, scanDone, len(sources) > 0, ing, logger, readModelBackfillStartupTimeout)

	var adapterWG sync.WaitGroup
	for _, src := range sources {
		if err := startSource(adapterCtx, &adapterWG, &scanWG, ing, lookup, src, logger); err != nil {
			logger.Warn("ai-viewer-ingest: source skipped",
				"source", src.id, "err", err)
			continue
		}
	}

	<-rootCtx.Done()
	stopSignals()
	shutdownStarted := logIngestShutdownStart(logger)

	result, ok := shutdownIngestRuntime(logger, cancelAdapters, &adapterWG, ing, backfillWait, ws)
	if !ok {
		logIngestShutdownBoundedGuard(logger, result, shutdownStarted, "runtime")
		ws = nil
		return 1
	}
	ws = nil
	logIngestShutdownTerminal(logger, result, shutdownStarted)
	return 0
}

var (
	signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	}
	openWriterStore = store.OpenWriter
	startIngester   = func(ing *ingest.Ingester, ctx context.Context) error {
		return ing.Start(ctx)
	}
)

func logIngestStartupSignalIfCanceled(ctx context.Context, logger *slog.Logger) {
	if ctx.Err() != nil {
		_ = logIngestShutdownStart(logger)
	}
}

func logIngestShutdownStart(logger *slog.Logger) time.Time {
	started := time.Now()
	logger.Info("shutdown_start",
		"subsystem", "ingest",
		"signal", "SIGTERM/SIGINT",
		"timeout_ms", ingesterStopTimeout.Milliseconds())
	return started
}

func logIngestShutdownBoundedGuard(logger *slog.Logger, result ingest.ShutdownResult, started time.Time, phase string) {
	logger.Error("shutdown_bounded_guard",
		"subsystem", "ingest",
		"phase", phase,
		"elapsed_ms", time.Since(started).Milliseconds(),
		"outcome", result.Outcome)
}

func logIngestShutdownTerminal(logger *slog.Logger, result ingest.ShutdownResult, started time.Time) {
	if result.Outcome == ingest.ShutdownReplayRequired {
		logger.Warn("shutdown_replay_required",
			"subsystem", "ingest",
			"elapsed_ms", time.Since(started).Milliseconds(),
			"outcome", result.Outcome)
	} else {
		logger.Info("shutdown_clean",
			"subsystem", "ingest",
			"elapsed_ms", time.Since(started).Milliseconds(),
			"outcome", result.Outcome)
	}
}

func shutdownIngestRuntime(
	logger *slog.Logger,
	cancelAdapters context.CancelFunc,
	adapterWG *sync.WaitGroup,
	ing shutdownIngester,
	backfillWait <-chan struct{},
	ws *store.Store,
) (ingest.ShutdownResult, bool) {
	cancelAdapters()
	adaptersDone := make(chan struct{})
	go func() {
		waitWithTimeout(adapterWG, adapterContextGracePeriod, logger)
		close(adaptersDone)
	}()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), ingesterStopTimeout)
	result, err := ing.StopContext(stopCtx)
	cancelStop()
	<-adaptersDone
	if err != nil && result.Outcome != ingest.ShutdownReplayRequired {
		logger.Warn("ai-viewer-ingest: ingester stop reported error",
			"outcome", result.Outcome,
			"err", err)
		return result, false
	}
	backfillWaitStarted := time.Now()
	if !waitChannelWithTimeout(backfillWait, backfillCancelWait) {
		logger.Error("shutdown_backfill_timeout",
			"elapsed_ms", time.Since(backfillWaitStarted).Milliseconds(),
			"timeout_ms", backfillCancelWait.Milliseconds())
		return result, false
	}
	if !closeStoreWithTimeout(ws, writerStoreCloseTimeout, logger) {
		return result, false
	}
	return result, true
}

type shutdownIngester interface {
	StopContext(context.Context) (ingest.ShutdownResult, error)
}

// ingestConfig holds the parsed CLI flag values for the ingester.
type ingestConfig struct {
	dbPath    string
	stateDir  string
	sources   []string
	logLevel  string
	logFormat string
	pprofAddr string
}

// parseFlags assembles the CLI surface and returns either a populated
// config or the exit code to use. The third return discriminates
// "parsed ok" from "early exit".
func parseFlags(args []string, stderr *os.File) (ingestConfig, int, bool) {
	fs := flag.NewFlagSet("ai-viewer-ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", "", "SQLite path (default ~/.local/share/ai-viewer/index.db)")
	stateDir := fs.String("state-dir", "", "state directory (default ~/.local/share/ai-viewer)")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "json", "log format (json|text)")
	showVersion := fs.Bool("version", false, "print version and exit")
	pprofAddr := fs.String("pprof", "", "if set, expose net/http/pprof endpoints on the given listen address (e.g. 127.0.0.1:6060). Default empty = disabled. Used by SOW-0094 to capture leak evidence; off by default in production.")
	sources := newRepeatableFlag()
	fs.Var(sources, "source", "add a source in the form <format>:<location>; may be repeated. "+
		"Explicit --source flags replace auto-discovery.")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-ingest [flags]\n\n"+
			"Watches configured session-snapshot sources and writes canonical events\n"+
			"into the SQLite store at --db.\n\n"+
			"Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ingestConfig{}, 0, false
		}
		return ingestConfig{}, 2, false
	}

	if *showVersion {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest %s\n", versionString())
		return ingestConfig{}, 0, false
	}

	return ingestConfig{
		dbPath:    *dbPath,
		stateDir:  *stateDir,
		sources:   sources.values(),
		logLevel:  *logLevel,
		logFormat: *logFormat,
		pprofAddr: *pprofAddr,
	}, 0, true
}

// repeatableFlag implements flag.Value for a string slice populated by
// repeating the same CLI flag.
type repeatableFlag struct {
	v []string
}

func newRepeatableFlag() *repeatableFlag { return &repeatableFlag{} }

// String implements flag.Value.
func (r *repeatableFlag) String() string { return strings.Join(r.v, ",") }

// Set implements flag.Value.
func (r *repeatableFlag) Set(s string) error {
	if s == "" {
		return errors.New("--source value must be non-empty")
	}
	r.v = append(r.v, s)
	return nil
}

func (r *repeatableFlag) values() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.v...)
}

// newLogger configures slog according to the operator's CLI flags.
// JSON is the default for production daemons; text is offered for
// human-readable local debugging.
func newLogger(levelName, format string, w *os.File) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(levelName) {
	case "", "info":
		lvl = slog.LevelInfo
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown --log-level %q", levelName)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "", "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("unknown --log-format %q", format)
	}
	return slog.New(h).With("binary", "ai-viewer-ingest"), nil
}

// resolveDBPath expands and absolutises --db. Empty input defaults to
// ~/.local/share/ai-viewer/index.db per deployment.md.
func resolveDBPath(p string) (string, error) {
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home: %w", err)
		}
		p = filepath.Join(home, ".local", "share", "ai-viewer", "index.db")
	}
	return filepath.Abs(p)
}

// resolveStateDir expands and absolutises --state-dir.
func resolveStateDir(p string) (string, error) {
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home: %w", err)
		}
		p = filepath.Join(home, ".local", "share", "ai-viewer")
	}
	return filepath.Abs(p)
}

// nowMicros is the source of unix-microsecond timestamps used by the
// OnError → SourceErrorEvent path. Kept as a package-level seam so
// tests can swap it for a deterministic clock if needed.
var nowMicros = func() int64 { return time.Now().UnixMicro() }

// waitWithTimeout waits up to d for wg to drain. Returns when wg
// either reaches zero or the timer fires.
func waitWithTimeout(wg *sync.WaitGroup, d time.Duration, logger *slog.Logger) {
	started := time.Now()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(d):
		logger.Warn("shutdown_adapter_grace_expired",
			"elapsed_ms", time.Since(started).Milliseconds(),
			"grace_ms", d.Milliseconds())
	}
}

func waitChannelWithTimeout(done <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

var (
	closeWriterStoreMu sync.RWMutex
	closeWriterStore   = func(st *store.Store) error {
		return st.Close()
	}
)

func closeWriterStoreWithHook(st *store.Store) error {
	closeWriterStoreMu.RLock()
	fn := closeWriterStore
	closeWriterStoreMu.RUnlock()
	return fn(st)
}

func closeStoreWithTimeout(st *store.Store, d time.Duration, logger *slog.Logger) bool {
	if st == nil {
		return true
	}
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- closeWriterStoreWithHook(st)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			logger.Error("shutdown_store_close_error",
				"store_role", "writer",
				"elapsed_ms", time.Since(started).Milliseconds(),
				"err", err)
			return false
		}
		return true
	case <-timer.C:
		logger.Error("shutdown_store_close_timeout",
			"store_role", "writer",
			"elapsed_ms", time.Since(started).Milliseconds(),
			"timeout_ms", d.Milliseconds())
		return false
	}
}

type readModelBackfiller interface {
	BackfillReadModels(context.Context) error
}

// startPostScanBackfill is the retained all-sources reconciliation pass. Its
// readModelsDeferred flag answers whether the startup run enabled source-scoped
// Scan deferral; the returned done channel is no longer passed to per-source
// Tail startup.
func startPostScanBackfill(shutdownCtx context.Context, scanDone <-chan struct{}, readModelsDeferred bool, backfiller readModelBackfiller, logger *slog.Logger, timeout time.Duration) (<-chan struct{}, <-chan struct{}) {
	backfillDone := make(chan struct{})
	backfillWait := make(chan struct{})
	go func() {
		started := time.Now()
		defer close(backfillDone)
		defer close(backfillWait)
		select {
		case <-scanDone:
		case <-shutdownCtx.Done():
			if logger != nil {
				logger.Info("shutdown_backfill_cancelled",
					"phase", "before_scan_done",
					"elapsed_ms", time.Since(started).Milliseconds())
			}
			return
		}
		if !readModelsDeferred {
			return
		}
		backfillCtx, cancel := context.WithTimeout(shutdownCtx, timeout)
		defer cancel()

		if logger != nil {
			logger.Info("ai-viewer-ingest: all scans complete; backfilling read models",
				"timeout", timeout.String())
		}
		if err := backfiller.BackfillReadModels(backfillCtx); err != nil {
			if logger == nil {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(backfillCtx.Err(), context.DeadlineExceeded) {
				logger.Error("ai-viewer-ingest: read-model backfill timed out; tailing will continue without completed read-model backfill",
					"timeout", timeout.String(),
					"err", err)
				return
			}
			if errors.Is(err, context.Canceled) || errors.Is(backfillCtx.Err(), context.Canceled) {
				logger.Warn("shutdown_backfill_cancelled",
					"phase", "backfill",
					"elapsed_ms", time.Since(started).Milliseconds(),
					"err", err)
				return
			}
			logger.Error("ai-viewer-ingest: read-model backfill failed; tailing will continue without completed read-model backfill",
				"err", err)
		}
	}()
	return backfillDone, backfillWait
}

// versionString returns the binary's build-time version. When the
// `-ldflags=-X` mechanism is added in a later chunk this is the only
// function that needs to change.
func versionString() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				rev := s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return rev
			}
		}
	}
	return "dev"
}

// dispatchSubcommand handles the bare-subcommand routes (a single first
// arg matching a known helper name). Returns (exitCode, handled). For any
// other input — flags-only, unknown subcommand — handled=false so the
// caller falls through to the daemon path. Extracted from run to keep
// cyclomatic complexity below the project gate.
func dispatchSubcommand(ctx context.Context, args []string, stdout, stderr *os.File) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "check-parity":
		return runCheckParity(ctx, args[1:], stdout, stderr), true
	case "rollups-backfill":
		return runBackfill(ctx, args[1:], stdout, stderr), true
	case "fts-content-backfill":
		return runBackfillFTSContent(ctx, args[1:], stdout, stderr), true
	case "reprice":
		return runReprice(ctx, args[1:], stdout, stderr), true
	default:
		return 0, false
	}
}

// acquireSingleInstanceLock takes an exclusive flock on
// "<state_dir>/ingester.lock" and returns a release function the caller
// MUST defer. Returns a non-nil error if another ingester already holds
// the lock; the operator should either stop the other instance or remove
// the stale lockfile manually.
//
// The lockfile lives INSIDE state_dir (not alongside it) because the
// systemd unit ships with ProtectSystem=strict + ReadWritePaths=
// /opt/ai-viewer/data. A sibling lockfile at "<state_dir>.lock" would
// fail with EROFS since the parent dir of state_dir is read-only.
//
// The lock is released by the OS on process exit (SIGTERM, SIGKILL,
// uncaught panic) so a crash never leaves a stale lockfile behind. The
// lockfile path is fixed per state_dir, so the operator can identify
// what holds it (an integration with `fuser` / `lsof` would be nice
// but is out of scope for v1).
func acquireSingleInstanceLock(stateDir string, logger *slog.Logger) (release func(), err error) {
	lockPath := filepath.Join(stateDir, "ingester.lock")
	// 0600 keeps the lockfile readable/writable only by the ingester's
	// own uid. gosec G302 wants a stricter mode (and is right); the
	// historical 0640 was a placeholder. The lockfile holds no content
	// — flock is the only state — so 0600 is the correct mode. Path
	// is "<state_dir>/ingester.lock", constructed from a known
	// suffix under a directory already on ReadWritePaths; gosec G304
	// and G302 are false positives here.
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600) //#nosec G304 G302 -- fixed suffix under ReadWritePath, owner-only mode
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to open lock file", "path", lockPath, "err", err)
		return nil, err
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		logger.Error("ai-viewer-ingest: another ingester already holds the lock on this state_dir",
			"path", lockPath,
			"hint", "if no other ingester is running, remove the lock file manually",
			"err", err)
		return nil, err
	}
	// Release the flock + close the fd. Best-effort: the OS releases
	// the flock on process exit anyway, and Close on an already-open fd
	// is safe. Both errors ignored because there is nothing meaningful
	// the caller can do if Unlock or Close fail at process shutdown.
	release = func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}
	return release, nil
}
