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
	// Thin subcommand dispatch. Existing invocations pass only flags
	// (leading '-'), so the daemon path below is preserved. A bare
	// "rollups-backfill" first arg routes to the one-shot recompute.
	if len(args) > 0 && args[0] == "rollups-backfill" {
		return runBackfill(args[1:], stdout, stderr)
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
	if err := os.MkdirAll(filepath.Dir(dbPath), stateDirPerm); err != nil {
		logger.Error("ai-viewer-ingest: failed to create db parent dir",
			"dir", filepath.Dir(dbPath), "err", err)
		return 1
	}

	logger.Info("ai-viewer-ingest starting",
		"db", dbPath,
		"state_dir", stateDir,
		"version", versionString(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws, err := store.OpenWriter(ctx, dbPath, logger)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to open store", "db", dbPath, "err", err)
		return 1
	}
	defer func() { _ = ws.Close() }()

	pricer, err := pricing.New()
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to load embedded pricing data", "err", err)
		return 1
	}
	logger.Info("ai-viewer-ingest pricing loaded",
		"hits", pricer.Stats().Hits,
		"miss_provider_model", pricer.Stats().MissProviderModel,
	)

	ing, err := ingest.New(ws.DB(),
		ingest.WithLogger(logger),
		ingest.WithPricer(pricer),
	)
	if err != nil {
		logger.Error("ai-viewer-ingest: failed to construct ingester", "err", err)
		return 1
	}
	if err := ing.Start(ctx); err != nil {
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

	adapterCtx, cancelAdapters := context.WithCancel(ctx)
	defer cancelAdapters()

	// cursorLookup is wired against the writer's DB handle. The reads
	// happen before the worker is started for that source, so they
	// cannot contend with the writer's own batch transactions.
	lookup := sqlCursorLookup{db: ws.DB()}

	var adapterWG sync.WaitGroup
	for _, src := range sources {
		if err := startSource(adapterCtx, &adapterWG, ing, lookup, src, logger); err != nil {
			logger.Warn("ai-viewer-ingest: source skipped",
				"source", src.id, "err", err)
			continue
		}
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-sigCtx.Done()
	logger.Info("ai-viewer-ingest received shutdown signal; draining")

	cancelAdapters()
	waitWithTimeout(&adapterWG, adapterContextGracePeriod, logger)

	if err := ing.Stop(); err != nil {
		logger.Warn("ai-viewer-ingest: ingester stop reported error", "err", err)
	}
	logger.Info("ai-viewer-ingest stopped")
	return 0
}

// ingestConfig holds the parsed CLI flag values for the ingester.
type ingestConfig struct {
	dbPath    string
	stateDir  string
	sources   []string
	logLevel  string
	logFormat string
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
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(d):
		logger.Warn("ai-viewer-ingest: adapter goroutines did not drain within grace period",
			"grace_period", d)
	}
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
