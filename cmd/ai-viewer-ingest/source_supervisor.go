package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
)

var (
	sourceRestartBackoffBase          = time.Second
	sourceRestartBackoffMax           = 60 * time.Second
	sourceTailCancelGrace             = 5 * time.Second
	sourceTailRestartEscalateFailures = 100
	sourceTailRestartEscalateAfter    = 24 * time.Hour
)

type sourceSupervisor struct {
	ctx              context.Context
	src              configuredSource
	ing              *ingest.Ingester
	lookup           cursorLookup
	factory          canonical.AdapterFactory
	adapterLocation  string
	logger           *slog.Logger
	events           chan<- canonical.Event
	restartRequests  <-chan struct{}
	repairRequests   <-chan struct{}
	unregisterTail   func()
	unregisterRepair func()

	readModelMu             sync.Mutex
	readModelRepairPending  bool
	readModelRepairRunning  bool
	readModelRepairDone     chan struct{}
	consecutiveTailRestarts int
	firstTailRestartAt      time.Time
}

func (s *sourceSupervisor) run(initial canonical.Adapter, since canonical.Cursor, scanDone func()) {
	if s.unregisterTail != nil {
		defer s.unregisterTail()
	}
	if s.unregisterRepair != nil {
		defer s.unregisterRepair()
	}
	var scanDoneOnce sync.Once
	markStartupScanDone := func() {
		scanDoneOnce.Do(scanDone)
	}

	restart := s.runAttempt(initial, since, true, markStartupScanDone)
	backoff := sourceRestartBackoffBase
	for restart {
		if !s.recordRequired(s.tailRestartingUpdate()) {
			s.waitReadModelRepair()
			return
		}
		if !s.waitBackoff(backoff) {
			s.recordStopped()
			s.waitReadModelRepair()
			return
		}
		next, nextSince, err := s.constructRestartAdapter()
		if err != nil {
			s.logger.Error("ai-viewer-ingest: source restart adapter construction failed", "err", err)
			if !s.recordRequired(ingest.SourceLifecycleUpdate{
				State: ingest.SourceLifecycleTailRestarting,
				Error: err.Error(),
			}) {
				s.waitReadModelRepair()
				return
			}
			backoff = nextSourceBackoff(backoff)
			continue
		}
		restart = s.runAttempt(next, nextSince, false, func() {})
		backoff = nextSourceBackoff(backoff)
	}
	s.waitReadModelRepair()
}

func (s *sourceSupervisor) runAttempt(adapter canonical.Adapter, since canonical.Cursor, startup bool, scanDone func()) bool {
	if startup {
		at := nowMicros()
		if !s.recordRequired(ingest.SourceLifecycleUpdate{
			State:           ingest.SourceLifecycleScanning,
			AtUS:            at,
			ScanStartedAtUS: &at,
		}) {
			scanDone()
			return false
		}
	}
	s.logger.Info("ai-viewer-ingest: adapter scan starting", "resume", since != nil)
	if err := adapter.Scan(s.ctx, since, s.events); err != nil {
		if !startup {
			return s.handleRestartScanError(err)
		}
		if !s.handleScanError(err, scanDone) {
			return false
		}
		return s.runTail(adapter)
	}
	if s.ctx.Err() != nil {
		s.logger.Info("ai-viewer-ingest: adapter scan cancelled")
		scanDone()
		s.recordStopped()
		return false
	}
	if !s.handleScanComplete(startup, scanDone) {
		return false
	}
	return s.runTail(adapter)
}

func (s *sourceSupervisor) handleRestartScanError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || s.ctx.Err() != nil {
		s.logger.Info("ai-viewer-ingest: restart catch-up scan cancelled")
		s.recordStopped()
		return false
	}
	if canonical.IsFatalScanError(err) {
		at := nowMicros()
		s.logger.Error("ai-viewer-ingest: restart catch-up scan failed fatally", "err", err)
		s.recordRequired(ingest.SourceLifecycleUpdate{
			State:             ingest.SourceLifecycleScanFailed,
			AtUS:              at,
			ScanCompletedAtUS: &at,
			Error:             err.Error(),
		})
		return false
	}
	s.logger.Error("ai-viewer-ingest: restart catch-up scan failed", "err", err)
	return s.recordRequired(ingest.SourceLifecycleUpdate{
		State: ingest.SourceLifecycleTailRestarting,
		Error: err.Error(),
	})
}

func (s *sourceSupervisor) handleScanError(err error, scanDone func()) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.logger.Info("ai-viewer-ingest: adapter scan cancelled")
		scanDone()
		s.recordStopped()
		return false
	}
	at := nowMicros()
	s.logger.Error("ai-viewer-ingest: adapter scan failed", "err", err)
	if !s.recordRequired(ingest.SourceLifecycleUpdate{
		State:             ingest.SourceLifecycleScanFailed,
		AtUS:              at,
		ScanCompletedAtUS: &at,
		Error:             err.Error(),
	}) {
		scanDone()
		return false
	}
	recoverable := !canonical.IsFatalScanError(err)
	if recoverable {
		if !s.clearReadModelDeferral() {
			scanDone()
			return false
		}
	}
	scanDone()
	return recoverable
}

func (s *sourceSupervisor) handleScanComplete(startup bool, scanDone func()) bool {
	at := nowMicros()
	s.logger.Info("ai-viewer-ingest: adapter scan complete")
	if startup {
		if !s.recordRequired(ingest.SourceLifecycleUpdate{
			State:             ingest.SourceLifecycleScanComplete,
			AtUS:              at,
			ScanCompletedAtUS: &at,
		}) {
			scanDone()
			return false
		}
	}
	if !s.clearReadModelDeferral() {
		scanDone()
		return false
	}
	scanDone()
	return true
}

func (s *sourceSupervisor) runTail(adapter canonical.Adapter) bool {
	at := nowMicros()
	if !s.recordRequired(ingest.SourceLifecycleUpdate{
		State: ingest.SourceLifecycleTailStarting,
		AtUS:  at,
	}) {
		return false
	}
	s.logger.Info("ai-viewer-ingest: tail starting")
	at = nowMicros()
	hadRestarts := s.consecutiveTailRestarts > 0
	s.resetTailRestartEscalation()
	if !s.recordRequired(ingest.SourceLifecycleUpdate{
		State:                 ingest.SourceLifecycleTailing,
		AtUS:                  at,
		TailStartedAtUS:       &at,
		TailHeartbeatUS:       &at,
		ResetTailRestartCount: true,
		ClearLifecycleError:   hadRestarts,
	}) {
		return false
	}
	attemptCtx, cancelAttempt := context.WithCancel(s.ctx)
	defer cancelAttempt()
	done := make(chan error, 1)
	go func() {
		done <- runAdapterTail(attemptCtx, adapter, s.events)
	}()

	s.startReadModelRepairIfPending()

	for {
		select {
		case err := <-done:
			if err == nil {
				select {
				case <-s.restartRequests:
					return true
				default:
				}
			}
			return s.handleTailReturn(err)
		case <-s.repairRequests:
			s.setReadModelRepairPending(true)
			s.startReadModelRepairIfPending()
		case <-s.restartRequests:
			cancelAttempt()
			if ok, _ := s.waitTailReturn(done); !ok {
				s.recordTailCancelTimeout()
				return false
			}
			return true
		case <-s.ctx.Done():
			cancelAttempt()
			if ok, _ := s.waitTailReturn(done); !ok {
				s.recordTailCancelTimeout()
			} else {
				s.recordStopped()
			}
			return false
		}
	}
}

func runAdapterTail(ctx context.Context, adapter canonical.Adapter, events chan<- canonical.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = tailPanicError(recovered)
		}
	}()
	return adapter.Tail(ctx, events)
}

func tailPanicError(recovered any) error {
	switch v := recovered.(type) {
	case error:
		return fmt.Errorf("adapter Tail panic: %w", v)
	default:
		return fmt.Errorf("adapter Tail panic: %v", v)
	}
}

func (s *sourceSupervisor) handleTailReturn(err error) bool {
	if err == nil && s.ctx.Err() != nil {
		s.logger.Info("ai-viewer-ingest: adapter tail stopped")
		s.recordStopped()
		return false
	}
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && s.ctx.Err() != nil {
		s.logger.Info("ai-viewer-ingest: adapter tail cancelled")
		s.recordStopped()
		return false
	}
	if err == nil {
		err = errors.New("tail returned without error")
	}
	at := nowMicros()
	s.logger.Error("ai-viewer-ingest: adapter tail failed", "err", err)
	return s.recordRequired(ingest.SourceLifecycleUpdate{
		State:          ingest.SourceLifecycleTailFailed,
		AtUS:           at,
		TailFailedAtUS: &at,
		Error:          err.Error(),
	})
}

func (s *sourceSupervisor) waitTailReturn(done <-chan error) (bool, error) {
	timer := time.NewTimer(sourceTailCancelGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return true, err
	case <-timer.C:
		return false, nil
	}
}

func (s *sourceSupervisor) recordTailCancelTimeout() {
	at := nowMicros()
	s.logger.Error("ai-viewer-ingest: adapter tail did not stop after cancellation",
		"grace", sourceTailCancelGrace.String())
	s.recordWithSupervisorContext(ingest.SourceLifecycleUpdate{
		State:          ingest.SourceLifecycleTailFailed,
		AtUS:           at,
		TailFailedAtUS: &at,
		Error:          "tail did not stop after cancellation",
	})
}

func (s *sourceSupervisor) constructAdapter() (canonical.Adapter, error) {
	return s.factory(s.adapterLocation, canonical.AdapterOptions{
		Logger:          s.logger,
		SourceID:        s.src.id,
		OnError:         newOnErrorHandler(s.ctx, s.src.id, s.events, s.logger),
		OnTailHeartbeat: s.tailHeartbeat,
	})
}

func (s *sourceSupervisor) tailHeartbeat() {
	if s.ing == nil {
		return
	}
	s.ing.RecordTailHeartbeat(s.src.id)
}

func (s *sourceSupervisor) clearReadModelDeferral() bool {
	if s.ing == nil {
		return true
	}
	wasDeferred := s.ing.SetSourceReadModelsDeferred(s.src.id, false)
	if !wasDeferred {
		return true
	}
	s.setReadModelRepairPending(true)
	return s.recordRequired(ingest.SourceLifecycleUpdate{
		ReadModelState:               ingest.ReadModelRepairPending,
		ReadModelStateTransitionOnly: true,
		ClearReadModelError:          true,
	})
}

func (s *sourceSupervisor) setReadModelRepairPending(pending bool) {
	s.readModelMu.Lock()
	s.readModelRepairPending = pending
	s.readModelMu.Unlock()
}

func (s *sourceSupervisor) startReadModelRepairIfPending() {
	if s.ing == nil {
		return
	}
	s.readModelMu.Lock()
	if !s.readModelRepairPending || s.readModelRepairRunning {
		s.readModelMu.Unlock()
		return
	}
	s.readModelRepairRunning = true
	done := make(chan struct{})
	s.readModelRepairDone = done
	s.readModelMu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			s.readModelMu.Lock()
			s.readModelRepairRunning = false
			s.readModelMu.Unlock()
		}()
		s.repairReadModelsLoop()
	}()
}

func (s *sourceSupervisor) repairReadModelsLoop() {
	backoff := sourceRestartBackoffBase
	for {
		s.readModelMu.Lock()
		pending := s.readModelRepairPending
		s.readModelMu.Unlock()
		if !pending || s.ing == nil || s.ctx.Err() != nil {
			return
		}
		if s.repairReadModelsOnce() {
			return
		}
		if !s.waitBackoff(backoff) {
			return
		}
		backoff = nextSourceBackoff(backoff)
	}
}

func (s *sourceSupervisor) repairReadModelsOnce() bool {
	at := nowMicros()
	if !s.recordRequired(ingest.SourceLifecycleUpdate{
		ReadModelState:      ingest.ReadModelRepairing,
		AtUS:                at,
		RepairStartedAtUS:   &at,
		RepairAttemptsDelta: 1,
	}) {
		return false
	}
	repairCtx, cancel := sourceReadModelRepairContext(s.ctx)
	_, err := s.ing.RepairSourceReadModels(repairCtx, s.src.id)
	cancel()
	if err != nil {
		if s.ctx.Err() != nil {
			s.recordInterruptedReadModelRepair(err)
			return true
		}
		at = nowMicros()
		state := ingest.ReadModelRepairFailed
		if errors.Is(err, context.DeadlineExceeded) {
			state = ingest.ReadModelRepairTimeout
		}
		if !s.recordRequired(ingest.SourceLifecycleUpdate{
			ReadModelState:   state,
			AtUS:             at,
			RepairFailedAtUS: &at,
			ReadModelError:   err.Error(),
		}) {
			return false
		}
		return false
	}
	at = nowMicros()
	if !s.recordRequired(ingest.SourceLifecycleUpdate{
		ReadModelState:               ingest.ReadModelReady,
		AtUS:                         at,
		RepairCompletedAtUS:          &at,
		ResetReadModelRepairAttempts: true,
		ClearReadModelError:          true,
	}) {
		return false
	}
	s.setReadModelRepairPending(false)
	return true
}

func sourceReadModelRepairContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithCancel(parent)
}

func (s *sourceSupervisor) recordInterruptedReadModelRepair(err error) {
	at := nowMicros()
	update := ingest.SourceLifecycleUpdate{
		ReadModelState: ingest.ReadModelRepairPending,
		AtUS:           at,
		ReadModelError: err.Error(),
	}
	s.recordWithSupervisorContext(update)
}

func (s *sourceSupervisor) constructRestartAdapter() (canonical.Adapter, canonical.Cursor, error) {
	adapter, err := s.constructAdapter()
	if err != nil {
		return nil, nil, err
	}
	return adapter, loadSourceCursor(s.ctx, adapter, s.lookup, s.src.id, s.logger), nil
}

func (s *sourceSupervisor) waitBackoff(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *sourceSupervisor) recordRequired(update ingest.SourceLifecycleUpdate) bool {
	return recordSourceLifecycleWithRetry(s.ctx, s.ing, s.src, s.logger, update) == nil
}

func (s *sourceSupervisor) recordWithSupervisorContext(update ingest.SourceLifecycleUpdate) {
	if s.ctx.Err() == nil {
		s.recordRequired(update)
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), sourceTailCancelGrace)
	defer cancel()
	_ = recordSourceLifecycleWithRetry(recordCtx, s.ing, s.src, s.logger, update)
}

func (s *sourceSupervisor) recordStopped() {
	update := ingest.SourceLifecycleUpdate{
		State:               ingest.SourceLifecycleStopped,
		ClearLifecycleError: true,
	}
	s.recordWithSupervisorContext(update)
}

func (s *sourceSupervisor) waitReadModelRepair() {
	s.readModelMu.Lock()
	done := s.readModelRepairDone
	s.readModelMu.Unlock()
	if done == nil {
		return
	}
	<-done
}

func (s *sourceSupervisor) tailRestartingUpdate() ingest.SourceLifecycleUpdate {
	now := time.Now()
	s.consecutiveTailRestarts++
	if s.firstTailRestartAt.IsZero() {
		s.firstTailRestartAt = now
	}
	update := ingest.SourceLifecycleUpdate{
		State:            ingest.SourceLifecycleTailRestarting,
		TailRestartDelta: 1,
	}
	if s.consecutiveTailRestarts >= sourceTailRestartEscalateFailures ||
		now.Sub(s.firstTailRestartAt) >= sourceTailRestartEscalateAfter {
		update.Error = fmt.Sprintf("tail restart has failed %d consecutive times", s.consecutiveTailRestarts)
	}
	return update
}

func (s *sourceSupervisor) resetTailRestartEscalation() {
	s.consecutiveTailRestarts = 0
	s.firstTailRestartAt = time.Time{}
}

func nextSourceBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > sourceRestartBackoffMax {
		return sourceRestartBackoffMax
	}
	return next
}

func recordSourceLifecycle(ctx context.Context, ing *ingest.Ingester, src configuredSource, logger *slog.Logger, update ingest.SourceLifecycleUpdate) error {
	if ing == nil {
		return nil
	}
	if err := ing.RecordSourceLifecycle(ctx, src.id, src.format, src.location, update); err != nil {
		if logger != nil {
			logger.Error("ai-viewer-ingest: source lifecycle update failed", "err", err)
		}
		return err
	}
	return nil
}

func recordSourceLifecycleWithRetry(ctx context.Context, ing *ingest.Ingester, src configuredSource, logger *slog.Logger, update ingest.SourceLifecycleUpdate) error {
	return retrySourceLifecycle(ctx, logger, update, func(ctx context.Context, update ingest.SourceLifecycleUpdate) error {
		return recordSourceLifecycle(ctx, ing, src, logger, update)
	})
}

func retrySourceLifecycle(ctx context.Context, logger *slog.Logger, update ingest.SourceLifecycleUpdate, record func(context.Context, ingest.SourceLifecycleUpdate) error) error {
	backoff := sourceRestartBackoffBase
	for {
		err := record(ctx, update)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if logger != nil {
			logger.Warn("ai-viewer-ingest: source lifecycle update failed; retrying",
				"err", err,
				"backoff", backoff.String())
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return err
		}
		backoff = nextSourceBackoff(backoff)
	}
}
