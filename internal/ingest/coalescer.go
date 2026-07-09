package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// coalescer replaces the per-source worker goroutines with a SINGLE writer
// goroutine that owns the SQLite connection (SOW-0118). Today, 5 source workers
// each call BeginTx independently, competing for the one connection
// (SetMaxOpenConns(1)) — 80%+ of their time is spent in begin-wait (waiting for
// another source's flush to finish). The coalescer drains ALL sources through a
// merged channel and flushes in a SINGLE transaction, eliminating the
// begin-wait entirely.
//
// Fairness is inherent: a tail event arriving during a bulk scan is batched
// with whatever scan events are pending and flushed in the next batch
// (≤ batchInterval). It never waits behind a separate scan flush (the 80%
// begin-wait that caused single-message spikes).
type coalescer struct {
	db         *sql.DB
	logger     *slog.Logger
	batchSize  int
	batchEvery time.Duration

	mu      sync.Mutex // guards workers map
	workers map[string]*coalescedSource

	merged chan taggedEvent
	stop   chan struct{}
	wg     sync.WaitGroup

	onCommittedBatch func()
}

// coalescedSource holds a source's persistent writer + the worker (for its
// methods like flushBody, promoteCommittedBatch). The writer is reused across
// flushes (its dirty-rollup carry-forward state survives).
type coalescedSource struct {
	worker *worker
	writer *writer
}

// taggedEvent carries the source identity alongside the event so the coalescer
// can dispatch to the correct per-source writer.
type taggedEvent struct {
	sourceID string
	ev       canonical.Event
}

func newCoalescer(db *sql.DB, logger *slog.Logger, batchSize int, batchEvery time.Duration) *coalescer {
	return &coalescer{
		db:         db,
		logger:     logger,
		batchSize:  batchSize,
		batchEvery: batchEvery,
		workers:    make(map[string]*coalescedSource),
		merged:     make(chan taggedEvent, 4096), // large buffer to absorb scan bursts
		stop:       make(chan struct{}),
	}
}

// register adds a source's worker + event channel to the coalescer. A merger
// goroutine forwards events from the per-source channel to the merged channel.
func (c *coalescer) register(sourceID string, w *worker, events <-chan canonical.Event) {
	wr := newWriter(w.sourceID, w.sourceFormat, w.location, w.pricer)
	if w.now != nil {
		wr.now = w.now
	}
	wr.deferReadModels = w.deferReadModels
	wr.readModelRebuildActive = w.readModelRebuildActive

	c.mu.Lock()
	c.workers[sourceID] = &coalescedSource{worker: w, writer: wr}
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for ev := range events {
			select {
			case c.merged <- taggedEvent{sourceID: sourceID, ev: ev}:
			case <-c.stop:
				return
			}
		}
	}()
}

// run is the coalescer's main loop — drains the merged channel, batches across
// sources, flushes in a single transaction.
func (c *coalescer) run(ctx context.Context) {
	defer c.wg.Done()
	c.wg.Add(1)

	batch := make([]taggedEvent, 0, c.batchSize)
	timer := time.NewTimer(c.batchEvery)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	flush := func(reason string) {
		if len(batch) == 0 {
			return
		}
		if err := c.flushBatch(ctx, batch, reason); err != nil && c.logger != nil {
			c.logger.Warn("coalescer: flush failed", "reason", reason, "events", len(batch), "err", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			for len(c.merged) > 0 {
				batch = append(batch, <-c.merged)
			}
			flush("ctx done")
			return

		case te := <-c.merged:
			batch = append(batch, te)
			if len(batch) >= c.batchSize {
				flush("size")
				timer.Stop()
			} else if len(batch) == 1 {
				timer.Reset(c.batchEvery)
			}

		case <-timer.C:
			flush("interval")
		}
	}
}

// shutdown signals merger goroutines to stop and waits.
func (c *coalescer) shutdown() {
	close(c.stop)
	c.wg.Wait()
}

// flushBatch applies a batch of events from multiple sources in a SINGLE
// transaction. Events are grouped by source; each source's persistent writer is
// applied within the shared tx, then the per-source read-model refresh, progress,
// and notify run within the same tx so they commit atomically.
func (c *coalescer) flushBatch(ctx context.Context, batch []taggedEvent, reason string) error {
	if len(batch) == 0 {
		return nil
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("coalescer: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Group events by source (preserve first-seen order).
	bySource := make(map[string][]canonical.Event, 8)
	order := make([]string, 0, 8)
	for _, te := range batch {
		if _, ok := bySource[te.sourceID]; !ok {
			order = append(order, te.sourceID)
		}
		bySource[te.sourceID] = append(bySource[te.sourceID], te.ev)
	}

	// Apply each source's events within the shared tx.
	appliedSources := make([]string, 0, len(order))
	for _, sourceID := range order {
		events := bySource[sourceID]
		c.mu.Lock()
		cs, ok := c.workers[sourceID]
		c.mu.Unlock()
		if !ok {
			continue // source unregistered mid-batch; skip
		}
		w, wr := cs.worker, cs.writer
		wr.fts5IndexLogs = w.fts5IndexLogs
		wr.beginTx(tx)
		if err := w.flushBody(ctx, tx, wr, events); err != nil {
			wr.endTx()
			return fmt.Errorf("coalescer: source %s: %w", sourceID, err)
		}
		wr.endTx()
		appliedSources = append(appliedSources, sourceID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("coalescer: commit (%s): %w", reason, err)
	}
	committed = true

	// Post-commit: per-source promotion (HWM, dedup, rollup buckets, repair).
	for _, sourceID := range appliedSources {
		c.mu.Lock()
		cs, ok := c.workers[sourceID]
		c.mu.Unlock()
		if !ok {
			continue
		}
		cs.worker.promoteCommittedBatch(cs.writer)
		cs.writer.resetBatch()
	}

	if c.onCommittedBatch != nil {
		c.onCommittedBatch()
	}

	return nil
}
