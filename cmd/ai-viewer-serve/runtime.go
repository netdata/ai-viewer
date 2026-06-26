package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
	"github.com/netdata/ai-viewer/internal/presenter"
	"github.com/netdata/ai-viewer/internal/store"
)

type serveRuntime struct {
	store     *store.Store
	presenter *presenter.Presenter
	mu        sync.Mutex
	closed    bool
}

func (rt *serveRuntime) close() error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	st := rt.store
	rt.store = nil
	rt.mu.Unlock()
	if st != nil {
		return st.Close()
	}
	return nil
}

func newServeRuntime(
	ctx context.Context,
	logger *slog.Logger,
	cfg parsedFlags,
	dbPath string,
) (*serveRuntime, error) {
	rs, err := store.OpenReader(ctx, dbPath, logger)
	if err != nil {
		logger.Error("ai-viewer-serve: failed to open store (read-only)",
			"db", dbPath, "err", err)
		return nil, err
	}
	rt := &serveRuntime{store: rs}
	if err := presenter.CheckSchema(ctx, rs.DB(), presenter.SchemaVersion); err != nil {
		if closeErr := rt.close(); closeErr != nil {
			logger.Warn("ai-viewer-serve: store close after schema failure failed", "err", closeErr)
		}
		logger.Error("ai-viewer-serve: schema version mismatch",
			"db", dbPath, "expected", presenter.SchemaVersion, "err", err)
		return nil, err
	}
	p, err := newServePresenter(logger, cfg, dbPath, rs)
	if err != nil {
		if closeErr := rt.close(); closeErr != nil {
			logger.Warn("ai-viewer-serve: store close after presenter failure failed", "err", closeErr)
		}
		logger.Error("ai-viewer-serve: presenter.New failed", "err", err)
		return nil, err
	}
	rt.presenter = p
	return rt, nil
}

func newServePresenter(
	logger *slog.Logger,
	cfg parsedFlags,
	dbPath string,
	rs *store.Store,
) (*presenter.Presenter, error) {
	// The serve binary owns the hub so graceful shutdown can deliver
	// disconnect before closing all SSE streams.
	hub := notify.New(notify.Options{})
	return presenter.New(presenter.Options{
		DB:            rs.DB(),
		Logger:        logger,
		Version:       versionString(cfg.version),
		DBPath:        dbPath,
		StartedAt:     time.Now().UTC(),
		SchemaVersion: presenter.SchemaVersion,
		FrontendFS:    embeddedFrontend(),
		Hub:           hub,
	})
}
