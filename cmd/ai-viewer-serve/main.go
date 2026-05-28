// Command ai-viewer-serve is the HTTP server that exposes the canonical
// SQLite store to the embedded React UI and to local debugging via curl.
//
// Lifecycle:
//
//   - parse CLI flags + configure slog
//   - open the canonical SQLite store read-only
//   - verify schema_meta.version matches the binary's expectation
//   - build the presenter + wire embedded frontend assets
//   - bind 127.0.0.1:7710 (or --bind) and serve until SIGTERM/SIGINT
//   - on signal: http.Server.Shutdown(30s) → close store → exit 0
//
// Read presenter.md, observability.md, deployment.md, and security.md
// before changing CLI flags or default paths.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
	"github.com/netdata/ai-viewer/internal/presenter"
	"github.com/netdata/ai-viewer/internal/store"
)

// frontendFS holds the Vite build output. scripts/build.sh writes the
// real index.html + assets/ under frontend_dist/ before a release build.
// On a clean checkout the directory holds only the tracked .gitkeep
// sentinel; the `all:` prefix embeds that dotfile so the binary always
// compiles even before the frontend is built (serveIndex then degrades to
// a not-built notice — see embeddedFrontend and presenter.md).
//
//go:embed all:frontend_dist
var frontendFS embed.FS

// defaultBind is the canonical localhost bind per deployment.md
// §"Port Allocation". Localhost-only is mandated by security.md
// §"Hard Rules"; v1 does not accept a non-localhost flag.
const defaultBind = "127.0.0.1:7710"

// shutdownTimeout is how long http.Server.Shutdown waits for in-flight
// handlers to drain. Per presenter.md §"Graceful Shutdown" the timeout
// is 30 s.
const shutdownTimeout = 30 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable main body. It returns the process exit code so
// `main()` is a one-liner and unit tests can drive run() with fake
// args. Unit coverage for parseFlags / assertLocalhost / version
// reporting lives in `main_test.go` (added in iter-3); the full
// lifecycle path is covered by the integration smoke.
func run(args []string, stdout, stderr *os.File) int {
	cfg, exitCode, ok := parseFlags(args, stderr)
	if !ok {
		return exitCode
	}

	logger, err := newLogger(cfg.logLevel, cfg.logFormat, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-serve: %v\n", err)
		return 2
	}
	slog.SetDefault(logger)

	dbPath, err := resolveDBPath(cfg.dbPath)
	if err != nil {
		logger.Error("ai-viewer-serve: failed to resolve --db", "err", err)
		return 1
	}
	stateDir, err := resolveStateDir(cfg.stateDir)
	if err != nil {
		logger.Error("ai-viewer-serve: failed to resolve --state-dir", "err", err)
		return 1
	}

	logger.Info("ai-viewer-serve starting",
		"db", dbPath,
		"state_dir", stateDir,
		"bind", cfg.bind,
		"version", versionString(cfg.version),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rs, err := store.OpenReader(ctx, dbPath, logger)
	if err != nil {
		logger.Error("ai-viewer-serve: failed to open store (read-only)",
			"db", dbPath, "err", err)
		return 1
	}
	defer func() { _ = rs.Close() }()

	if err := presenter.CheckSchema(rs.DB(), presenter.SchemaVersion); err != nil {
		logger.Error("ai-viewer-serve: schema version mismatch",
			"db", dbPath, "expected", presenter.SchemaVersion, "err", err)
		return 1
	}

	frontend := embeddedFrontend()

	// hub is the in-memory SSE fan-out. The serve binary owns it so it can
	// deliver a graceful-shutdown `disconnect` before closing it (see
	// serveHTTP). The notify poller (started below) is the only producer
	// into it; presenter handlers attach clients.
	hub := notify.New(notify.Options{})

	p, err := presenter.New(presenter.Options{
		DB:            rs.DB(),
		Logger:        logger,
		Version:       versionString(cfg.version),
		DBPath:        dbPath,
		StartedAt:     time.Now().UTC(),
		SchemaVersion: presenter.SchemaVersion,
		FrontendFS:    frontend,
		Hub:           hub,
	})
	if err != nil {
		logger.Error("ai-viewer-serve: presenter.New failed", "err", err)
		return 1
	}

	if err := serveHTTP(ctx, logger, cfg.bind, p); err != nil {
		logger.Error("ai-viewer-serve: server exited with error", "err", err)
		return 1
	}
	logger.Info("ai-viewer-serve stopped")
	return 0
}

// parseFlags assembles the CLI surface and returns either a populated
// config or the exit code to use. The third return discriminates
// "parsed ok" from "early exit".
func parseFlags(args []string, stderr *os.File) (parsed parsedFlags, exitCode int, ok bool) {
	fs := flag.NewFlagSet("ai-viewer-serve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", "", "SQLite path (default ~/.local/share/ai-viewer/index.db)")
	stateDir := fs.String("state-dir", "", "state directory (default ~/.local/share/ai-viewer)")
	bind := fs.String("bind", defaultBind, "address to bind; localhost-only in v1")
	logLevel := fs.String("log-level", "info", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "json", "log format (json|text)")
	showVersion := fs.Bool("version", false, "print version and exit")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-serve [flags]\n\n"+
			"Serves the canonical SQLite store over HTTP + SSE.\n\n"+
			"Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parsedFlags{}, 0, false
		}
		return parsedFlags{}, 2, false
	}

	if *showVersion {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-serve %s\n", versionString(false))
		return parsedFlags{}, 0, false
	}

	if err := assertLocalhost(*bind); err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-serve: %v\n", err)
		return parsedFlags{}, 2, false
	}

	return parsedFlags{
		dbPath:    *dbPath,
		stateDir:  *stateDir,
		bind:      *bind,
		logLevel:  *logLevel,
		logFormat: *logFormat,
	}, 0, true
}

// parsedFlags is the runtime view of the CLI surface used by run().
type parsedFlags struct {
	dbPath    string
	stateDir  string
	bind      string
	logLevel  string
	logFormat string
	version   bool
}

// newLogger configures slog according to the operator's CLI flags.
// JSON is the default for production daemons (per observability.md
// §"Structured Logging"); text is offered for human-readable local
// debugging.
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
	return slog.New(h).With("binary", "ai-viewer-serve"), nil
}

// resolveDBPath expands ~ and returns an absolute path. Empty input
// defaults to ~/.local/share/ai-viewer/index.db per deployment.md.
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

// resolveStateDir expands ~ and returns an absolute path. Empty input
// defaults to ~/.local/share/ai-viewer per deployment.md.
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

// assertLocalhost refuses any non-loopback bind in v1 per security.md
// §"Hard Rules" #3. The Phase 2 SOW will introduce auth and a
// `--allow-non-localhost` flag; until then we hard-fail.
//
// Accepted hosts: literal "127.0.0.1" and "::1". The string "localhost"
// is REJECTED because the actual address it resolves to is determined
// by /etc/hosts (or NSS/DNS) at bind time — an attacker or
// misconfiguration that points localhost at a non-loopback IP would
// silently expose the server. An empty host is also rejected because
// the Go HTTP server treats ":7710" as 0.0.0.0:7710 — bound to every
// interface. The operator must spell out a literal loopback IP so the
// security posture is unambiguous.
func assertLocalhost(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--bind %q is not a valid host:port: %w", addr, err)
	}
	switch host {
	case "127.0.0.1", "::1":
		return nil
	case "":
		return fmt.Errorf("--bind %q rejected: empty host binds every interface; "+
			"use 127.0.0.1:<port> or [::1]:<port> (security.md §Hard Rules)", addr)
	case "localhost":
		return fmt.Errorf("--bind %q rejected: literal 'localhost' is not accepted because "+
			"/etc/hosts may resolve it to a non-loopback IP; "+
			"use 127.0.0.1:<port> or [::1]:<port> (security.md §Hard Rules)", addr)
	}
	return fmt.Errorf("--bind %q rejected: v1 binds localhost only — "+
		"only literal 127.0.0.1 and ::1 are accepted (security.md §Hard Rules)", addr)
}

// embeddedFrontend returns the embedded frontend FS. It cannot fail: the
// `all:` embed always captures the tracked .gitkeep sentinel, so frontendFS
// is never empty even on a clean checkout where scripts/build.sh has not yet
// written the real index.html + assets/. A binary built without running the
// frontend build must still start and serve /api; when index.html is absent
// the presenter's serveIndex degrades to a built-in "UI not built" notice at
// GET / (presenter.md §"serveIndex contract"). An unbuilt UI is therefore a
// recoverable dev-time state the caller never treats as fatal.
func embeddedFrontend() fs.FS {
	return frontendFS
}

// serveHTTP wires up the HTTP server, the read-only notify poller, signal
// handling, and graceful shutdown. Returns an error only when the listener
// itself fails catastrophically; clean shutdown is reported as nil.
//
// WriteTimeout is intentionally left unset (0): a global write deadline
// would kill long-lived /api/events SSE streams. Normal handlers stay
// bounded by the presenter's 30 s per-request query context, and the SSE
// handler clears its own write deadline per connection (presenter.md
// §Middlewares).
//
// Graceful-shutdown order (presenter.md §Graceful Shutdown): on signal we
// (1) stop the notify poller, (2) deliver a `disconnect` to every SSE
// client and close the hub so the long-lived stream goroutines unblock and
// return, then (3) http.Server.Shutdown drains the now-returning handlers
// within shutdownTimeout. The store is closed by run()'s defer afterwards.
func serveHTTP(ctx context.Context, logger *slog.Logger, bind string, p *presenter.Presenter) error {
	srv := &http.Server{
		Addr:              bind,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start the read-only notify poller; its context is cancelled at
	// shutdown so the goroutine returns before the process exits.
	pollerCtx, stopPoller := context.WithCancel(ctx)
	defer stopPoller()
	pollerDone := make(chan struct{})
	go func() {
		p.RunNotifyPoller(pollerCtx)
		close(pollerDone)
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("ai-viewer-serve listening", "bind", bind)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case <-sigCtx.Done():
		logger.Info("ai-viewer-serve received shutdown signal; draining")
	case err := <-errCh:
		stopPoller()
		<-pollerDone
		return err
	}

	// Stop the poller, then signal SSE clients (disconnect + close hub
	// channels) so the long-lived stream handlers return and Server.Shutdown
	// can drain them.
	stopPoller()
	<-pollerDone
	p.ShutdownSSE()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("ai-viewer-serve: graceful shutdown error", "err", err)
	}
	// Drain the listener goroutine.
	if err := <-errCh; err != nil {
		return err
	}
	return nil
}

// versionString returns the binary's build-time version. When the
// embed/`-ldflags=-X` mechanism is added in a later chunk this is the
// only function that needs to change.
func versionString(_ bool) string {
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
