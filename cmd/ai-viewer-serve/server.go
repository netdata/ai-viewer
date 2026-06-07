package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/netdata/ai-viewer/internal/presenter"
)

// shutdownTimeout is how long http.Server.Shutdown waits for in-flight
// handlers to drain. Per presenter.md §"Graceful Shutdown" the timeout
// is 30 s.
const shutdownTimeout = 30 * time.Second

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
	waitListener   func() error
}

func runGracefulShutdown(logger *slog.Logger, timeout time.Duration, hooks serveShutdownHooks) error {
	hooks.stopPoller()
	hooks.waitPoller()
	hooks.shutdownSSE()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := hooks.shutdownServer(shutdownCtx); err != nil {
		logger.Warn("ai-viewer-serve: graceful shutdown error", "err", err)
	}
	return hooks.waitListener()
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
	srv := newHTTPServer(bind, p.Handler())
	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	poller := startNotifyPoller(ctx, p.RunNotifyPoller)
	errCh := startHTTPListener(logger, srv, bind)

	select {
	case <-sigCtx.Done():
		logger.Info("ai-viewer-serve received shutdown signal; draining")
	case err := <-errCh:
		poller.stopAndWait()
		return err
	}

	return runGracefulShutdown(logger, shutdownTimeout, serveShutdownHooks{
		stopPoller:     poller.stop,
		waitPoller:     poller.wait,
		shutdownSSE:    p.ShutdownSSE,
		shutdownServer: srv.Shutdown,
		waitListener: func() error {
			return <-errCh
		},
	})
}
