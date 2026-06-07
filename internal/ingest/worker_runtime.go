package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// shutdownDrainTimeout caps how long a worker spends draining its
// channel during shutdown. The deadline applies to the final flush
// transaction and to active writes that outlive lifecycle cancellation.
const shutdownDrainTimeout = 10 * time.Second

// run blocks until ctx is cancelled or the events channel is closed.
// Returns when no more events will arrive. On ctx cancellation the
// worker drains any pending events and runs one final flush with its
// own short-lived context so SQL writes still succeed.
func (w *worker) run(ctx context.Context) {
	rt := newWorkerRuntime(w)
	defer rt.close()
	rt.run(ctx)
}

type workerRuntime struct {
	worker              *worker
	writer              *writer
	batch               []canonical.Event
	flushTimer          *time.Timer
	timerArmed          bool
	shutdownDrainCtx    context.Context
	shutdownDrainCancel context.CancelFunc
}

func newWorkerRuntime(w *worker) *workerRuntime {
	wr := newWriter(w.sourceID, w.sourceFormat, w.location, w.pricer)
	if w.now != nil {
		wr.now = w.now
	}
	rt := &workerRuntime{
		worker:     w,
		writer:     wr,
		batch:      make([]canonical.Event, 0, w.batchSize),
		flushTimer: time.NewTimer(w.batchEvery),
	}
	rt.stopTimer()
	return rt
}

func (rt *workerRuntime) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			rt.handleCancel(ctx)
			return
		case ev, ok := <-rt.worker.events:
			if !ok {
				rt.handleClose(ctx)
				return
			}
			rt.handleEvent(ctx, ev)
		case <-rt.flushTimer.C:
			rt.handleTimer(ctx)
		}
	}
}

func (rt *workerRuntime) close() {
	rt.stopTimer()
	if rt.shutdownDrainCancel != nil {
		rt.shutdownDrainCancel()
	}
}

func (rt *workerRuntime) handleCancel(ctx context.Context) {
	rt.drainBufferedEvents(ctx)
	rt.pullPendingEvent()
	rt.flushBatch(ctx, "ctx done")
	rt.idleRefresh(ctx)
}

func (rt *workerRuntime) handleClose(_ context.Context) {
	// Channel-close final flush intentionally uses the bounded shutdown-drain
	// context: parent cancellation can race this branch before SQL reaches BeginTx.
	drainCtx := rt.shutdownDrainContext()
	rt.flushBatchWithWriteContext(drainCtx, "channel closed")
	rt.idleRefreshWithWriteContext(drainCtx)
}

func (rt *workerRuntime) handleEvent(ctx context.Context, ev canonical.Event) {
	rt.appendEvent(ev)
	rt.armTimer()
	if len(rt.batch) >= rt.worker.batchSize {
		rt.flushBatch(ctx, "size")
	}
}

func (rt *workerRuntime) handleTimer(ctx context.Context) {
	rt.timerArmed = false
	if len(rt.batch) > 0 {
		rt.flushBatch(ctx, "interval")
		return
	}
	rt.idleRefresh(ctx)
	rt.rearmTimer()
}

func (rt *workerRuntime) drainBufferedEvents(ctx context.Context) {
	// len(channel) bounds this best-effort drain without select/default
	// randomness while still avoiding a wait for a producer that remains open.
	for len(rt.worker.events) > 0 {
		ev, ok := <-rt.worker.events
		if !ok {
			return
		}
		rt.appendEvent(ev)
		if len(rt.batch) >= rt.worker.batchSize {
			rt.flushBatch(ctx, "size on shutdown")
		}
	}
}

func (rt *workerRuntime) pullPendingEvent() {
	select {
	case ev, ok := <-rt.worker.events:
		if ok {
			rt.appendEvent(ev)
		}
	default:
	}
}

func (rt *workerRuntime) appendEvent(ev canonical.Event) {
	rt.batch = append(rt.batch, ev)
}

func (rt *workerRuntime) flushBatch(ctx context.Context, reason string) {
	if len(rt.batch) == 0 {
		return
	}
	writeCtx, cancel := rt.writeContext(ctx)
	defer cancel()
	rt.flushBatchWithWriteContext(writeCtx, reason)
}

func (rt *workerRuntime) flushBatchWithWriteContext(writeCtx context.Context, reason string) {
	if len(rt.batch) == 0 {
		return
	}
	if err := rt.worker.flush(writeCtx, rt.writer, rt.batch); err != nil {
		rt.worker.report(fmt.Errorf("flush (%s): %w", reason, err))
	}
	rt.batch = rt.batch[:0]
	rt.writer.resetBatch()
	rt.rearmTimer()
}

func (rt *workerRuntime) idleRefresh(ctx context.Context) {
	if !rt.writer.hasPendingRollupBuckets() {
		return
	}
	writeCtx, cancel := rt.writeContext(ctx)
	defer cancel()
	rt.idleRefreshWithWriteContext(writeCtx)
}

func (rt *workerRuntime) idleRefreshWithWriteContext(writeCtx context.Context) {
	if !rt.writer.hasPendingRollupBuckets() {
		return
	}
	if err := rt.worker.refreshRollupsOnly(writeCtx, rt.writer); err != nil {
		rt.worker.report(err)
	}
	rt.writer.resetBatch()
}

func (rt *workerRuntime) writeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return rt.shutdownDrainContext(), func() {}
	}
	return detachedWriteContext(ctx, shutdownDrainTimeout)
}

func detachedWriteContext(parent context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	writeCtx, cancel := context.WithCancel(context.WithoutCancel(parent))
	go func() {
		select {
		case <-parent.Done():
		case <-writeCtx.Done():
			return
		}

		// Active writes ignore immediate shutdown cancellation so accepted
		// batches are not dropped, but they still get the shutdown drain bound.
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-writeCtx.Done():
		}
	}()
	return writeCtx, cancel
}

func (rt *workerRuntime) shutdownDrainContext() context.Context {
	if rt.shutdownDrainCtx == nil {
		rt.shutdownDrainCtx, rt.shutdownDrainCancel = context.WithTimeout(context.Background(), shutdownDrainTimeout)
	}
	return rt.shutdownDrainCtx
}

func (rt *workerRuntime) armTimer() {
	if rt.timerArmed {
		return
	}
	rt.flushTimer.Reset(rt.worker.batchEvery)
	rt.timerArmed = true
}

func (rt *workerRuntime) rearmTimer() {
	rt.stopTimer()
	if rt.writer.hasPendingRollupBuckets() {
		rt.flushTimer.Reset(rt.worker.batchEvery)
		rt.timerArmed = true
	}
}

func (rt *workerRuntime) stopTimer() {
	if !rt.flushTimer.Stop() {
		select {
		case <-rt.flushTimer.C:
		default:
		}
	}
	rt.timerArmed = false
}
