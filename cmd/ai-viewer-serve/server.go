package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/netdata/ai-viewer/internal/presenter"
)

// shutdownTimeout is how long http.Server.Shutdown waits for in-flight
// handlers to drain. Per presenter.md §"Graceful Shutdown" the timeout
// is 30 s.
const (
	shutdownTimeout         = 30 * time.Second
	notifyPollerWaitTimeout = 5 * time.Second
	storeCloseTimeout       = 5 * time.Second
)

func newHTTPServer(bind string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              bind,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func normalizeServerError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func startHTTPListener(logger *slog.Logger, srv *http.Server, bind string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("ai-viewer-serve listening", "bind", bind)
		errCh <- normalizeServerError(srv.ListenAndServe())
	}()
	return errCh
}

type notifyPollerRuntime struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

func startNotifyPoller(ctx context.Context, run func(context.Context)) notifyPollerRuntime {
	pollerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		run(pollerCtx)
		close(done)
	}()
	return notifyPollerRuntime{cancel: cancel, done: done}
}

func (p notifyPollerRuntime) stop() {
	p.cancel()
}

func (p notifyPollerRuntime) wait() {
	<-p.done
}

func (p notifyPollerRuntime) stopAndWait() {
	p.stop()
	p.wait()
}

type serveShutdownHooks struct {
	stopPoller     func()
	waitPoller     func()
	shutdownSSE    func()
	shutdownServer func(context.Context) error
	closeStore     func() error
	waitListener   func() error
}

type serveShutdownTimeouts struct {
	notifyPollerWait time.Duration
	httpShutdown     time.Duration
	storeClose       time.Duration
}

func runGracefulShutdown(logger *slog.Logger, timeouts serveShutdownTimeouts, hooks serveShutdownHooks) (bool, error) {
	clean := true
	hooks.stopPoller()
	if !runWithTimeout(timeouts.notifyPollerWait, hooks.waitPoller) {
		clean = false
		logger.Warn("ai-viewer-serve: notify poller did not stop before shutdown timeout",
			"timeout", timeouts.notifyPollerWait.String())
	}
	hooks.shutdownSSE()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeouts.httpShutdown)
	defer cancel()
	if err := hooks.shutdownServer(shutdownCtx); err != nil {
		clean = false
		logger.Warn("ai-viewer-serve: graceful shutdown error", "err", err)
	}
	if hooks.closeStore != nil {
		storeCloseStarted := time.Now()
		if ok, err := runErrWithTimeout(timeouts.storeClose, hooks.closeStore); !ok {
			clean = false
			logger.Warn("shutdown_store_close_timeout",
				"store_role", "reader",
				"elapsed_ms", time.Since(storeCloseStarted).Milliseconds(),
				"timeout_ms", timeouts.storeClose.Milliseconds())
		} else if err != nil {
			clean = false
			logger.Warn("shutdown_store_close_error",
				"store_role", "reader",
				"elapsed_ms", time.Since(storeCloseStarted).Milliseconds(),
				"err", err)
		}
	}
	err := hooks.waitListener()
	if err != nil {
		clean = false
	}
	return clean, err
}

func runWithTimeout(timeout time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func runErrWithTimeout(timeout time.Duration, fn func() error) (bool, error) {
	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return true, err
	case <-timer.C:
		return false, nil
	}
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
// return, (3) http.Server.Shutdown drains the now-returning handlers within
// shutdownTimeout, then (4) the read-only store is closed under a bounded
// timer. run()'s deferred close is an idempotent fallback.
func serveHTTP(ctx context.Context, logger *slog.Logger, bind string, p *presenter.Presenter, closeStore func() error, releaseSignals func()) error {
	srv := newHTTPServer(bind, p.Handler())
	poller := startNotifyPoller(ctx, p.RunNotifyPoller)
	errCh := startHTTPListener(logger, srv, bind)

	select {
	case <-ctx.Done():
		if releaseSignals != nil {
			releaseSignals()
		}
		shutdownStarted := time.Now()
		logger.Info("shutdown_start",
			"subsystem", "serve",
			"signal", "SIGTERM/SIGINT",
			"timeout_ms", shutdownTimeout.Milliseconds())
		clean, err := runGracefulShutdown(logger, serveShutdownTimeouts{
			notifyPollerWait: notifyPollerWaitTimeout,
			httpShutdown:     shutdownTimeout,
			storeClose:       storeCloseTimeout,
		}, serveShutdownHooks{
			stopPoller:     poller.stop,
			waitPoller:     poller.wait,
			shutdownSSE:    p.ShutdownSSE,
			shutdownServer: srv.Shutdown,
			closeStore:     closeStore,
			waitListener: func() error {
				return <-errCh
			},
		})
		if err == nil && clean {
			logger.Info("shutdown_clean",
				"subsystem", "serve",
				"elapsed_ms", time.Since(shutdownStarted).Milliseconds(),
				"outcome", "clean")
		}
		return err
	case err := <-errCh:
		poller.stopAndWait()
		return err
	}
}
