package ingest

import (
	"context"
	"fmt"
	"time"
)

type tailLivenessConfig struct {
	staleAfter            time.Duration
	watchdogEvery         time.Duration
	heartbeatPersistEvery time.Duration
}

type tailHeartbeatState struct {
	lastUS      int64
	persistedUS int64
}

type tailHeartbeatPersistRequest struct {
	sourceID string
	atUS     int64
}

// RegisterTailRestart registers a coalesced restart channel for a source
// supervisor. The returned function unregisters the channel.
func (i *Ingester) RegisterTailRestart(sourceID string, ch chan<- struct{}) func() {
	if sourceID == "" || ch == nil {
		return func() {}
	}
	i.tailMu.Lock()
	i.tailRestartChans[sourceID] = ch
	i.tailMu.Unlock()
	return func() {
		i.tailMu.Lock()
		if i.tailRestartChans[sourceID] == ch {
			delete(i.tailRestartChans, sourceID)
		}
		i.tailMu.Unlock()
	}
}

// RecordTailHeartbeat records live tail liveness without blocking the adapter
// Tail loop. Persistence is throttled and handled by the ingester worker loop.
func (i *Ingester) RecordTailHeartbeat(sourceID string) {
	if sourceID == "" {
		return
	}
	at := i.now()
	shouldPersist := false
	i.tailMu.Lock()
	state := i.tailHeartbeats[sourceID]
	state.lastUS = at
	if state.persistedUS == 0 || at-state.persistedUS >= i.tailLiveness.heartbeatPersistEvery.Microseconds() {
		state.persistedUS = at
		shouldPersist = true
	}
	i.tailHeartbeats[sourceID] = state
	i.tailMu.Unlock()
	if !shouldPersist {
		return
	}
	select {
	case i.tailHeartbeatPersist <- tailHeartbeatPersistRequest{sourceID: sourceID, atUS: at}:
	default:
		i.logger.Warn("tail heartbeat persistence queue full", "source_id", sourceID)
	}
}

func (i *Ingester) tailLivenessLoop(ctx context.Context) {
	ticker := time.NewTicker(i.tailLiveness.watchdogEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-i.tailHeartbeatPersist:
			if err := i.persistTailHeartbeat(ctx, req); err != nil {
				i.logger.Warn("tail heartbeat persist failed", "source_id", req.sourceID, "err", err)
			}
		case <-ticker.C:
			if err := i.checkTailStaleness(ctx); err != nil {
				i.logger.Warn("tail staleness check failed", "err", err)
			}
		}
	}
}

func (i *Ingester) persistTailHeartbeat(ctx context.Context, req tailHeartbeatPersistRequest) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tail heartbeat begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE source_progress
SET tail_heartbeat_at = ?,
    lifecycle_state = ?,
    lifecycle_state_at = ?
WHERE source_id = ?
  AND lifecycle_state = ?
`, req.atUS, string(SourceLifecycleTailing), req.atUS, req.sourceID, string(SourceLifecycleTailStale))
	if err != nil {
		return fmt.Errorf("tail heartbeat update: %w", err)
	}
	stateChanged, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("tail heartbeat rows affected: %w", err)
	}
	if stateChanged == 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE source_progress
SET tail_heartbeat_at = ?
WHERE source_id = ?
  AND lifecycle_state = ?
`, req.atUS, req.sourceID, string(SourceLifecycleTailing)); err != nil {
			return fmt.Errorf("tail heartbeat refresh: %w", err)
		}
	}
	if stateChanged > 0 {
		if err := insertSourceStatusNotify(ctx, tx, req.sourceID, req.atUS); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tail heartbeat commit: %w", err)
	}
	return nil
}

func (i *Ingester) checkTailStaleness(ctx context.Context) error {
	rows, err := i.db.QueryContext(ctx, `
SELECT source_id, COALESCE(tail_heartbeat_at, tail_started_at, lifecycle_state_at, 0)
FROM source_progress
WHERE lifecycle_state = 'tailing'
`)
	if err != nil {
		return fmt.Errorf("tail staleness query: %w", err)
	}

	now := i.now()
	staleAfterUS := i.tailLiveness.staleAfter.Microseconds()
	var stale []string
	for rows.Next() {
		var sourceID string
		var persistedLast int64
		if err := rows.Scan(&sourceID, &persistedLast); err != nil {
			_ = rows.Close()
			return fmt.Errorf("tail staleness scan: %w", err)
		}
		last := i.liveTailHeartbeat(sourceID, persistedLast)
		if last > 0 && now-last <= staleAfterUS {
			continue
		}
		stale = append(stale, sourceID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("tail staleness close rows: %w", err)
	}
	for _, sourceID := range stale {
		if err := i.markTailStale(ctx, sourceID, now); err != nil {
			return err
		}
		i.enqueueTailRestart(sourceID)
	}
	return nil
}

func (i *Ingester) liveTailHeartbeat(sourceID string, fallback int64) int64 {
	i.tailMu.Lock()
	defer i.tailMu.Unlock()
	if state, ok := i.tailHeartbeats[sourceID]; ok && state.lastUS > fallback {
		return state.lastUS
	}
	return fallback
}

func (i *Ingester) markTailStale(ctx context.Context, sourceID string, atUS int64) error {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tail stale begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
UPDATE source_progress
SET lifecycle_state = ?,
    lifecycle_state_at = ?,
    lifecycle_error = ?
WHERE source_id = ?
  AND lifecycle_state = ?
`, string(SourceLifecycleTailStale), atUS, "tail heartbeat stale", sourceID, string(SourceLifecycleTailing))
	if err != nil {
		return fmt.Errorf("tail stale update: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("tail stale rows affected: %w", err)
	}
	if changed > 0 {
		if err := insertSourceStatusNotify(ctx, tx, sourceID, atUS); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tail stale commit: %w", err)
	}
	return nil
}

func (i *Ingester) enqueueTailRestart(sourceID string) {
	i.tailMu.Lock()
	ch := i.tailRestartChans[sourceID]
	i.tailMu.Unlock()
	if ch == nil {
		i.logger.Warn("tail stale source has no restart channel", "source_id", sourceID)
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}
