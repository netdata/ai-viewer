package ingest

import (
	"log/slog"
	"sync"
	"time"
)

// flushStageTiming is durable, always-on per-stage wall-time visibility for
// the ingest worker's flush path (SOW-0118). The flush is the throughput-
// critical inner loop, and "for any performance, know where the time goes"
// requires knowing how wall time splits across apply / rollups / FTS /
// aggregates / notify / commit — not one aggregate number. Each stage's
// duration is added to an atomic-ish counter under a mutex (flush is
// single-goroutine per worker, but the summary is read from the health path).
//
// Overhead is one time.Now() + add per stage per flush (~tens of ns) —
// negligible against the millisecond-scale stages it measures. It is always
// on (not gated) so the breakdown is available in production at any time.
type flushStageTiming struct {
	mu     sync.Mutex
	count  int64
	stages map[string]time.Duration
	logged time.Time
	logger *slog.Logger
	source string
	every  time.Duration // log a summary at most this often
}

func newFlushStageTiming(source string, logger *slog.Logger) *flushStageTiming {
	return &flushStageTiming{
		stages: map[string]time.Duration{},
		logger: logger,
		source: source,
		every:  30 * time.Second,
	}
}

// stageTimer returns a function that, when called, adds the elapsed time since
// `start` (captured at call time of stageTimer) to the named stage. Usage:
//
//	defer t.stage("apply")()
//
// The returned closure captures `start` = time.Now() at stageTimer() call time
// so the deferred call measures the stage's wall time correctly even though
// defer binds the function value immediately.
func (t *flushStageTiming) stage(name string) func() {
	start := time.Now()
	return func() {
		t.add(name, time.Since(start))
	}
}

func (t *flushStageTiming) add(name string, d time.Duration) {
	t.mu.Lock()
	t.stages[name] += d
	t.count++
	now := time.Now()
	shouldLog := t.logged.IsZero() || now.Sub(t.logged) >= t.every
	if shouldLog {
		t.logged = now
	}
	snap := make(map[string]time.Duration, len(t.stages))
	total := time.Duration(0)
	for k, v := range t.stages {
		snap[k] = v
		total += v
	}
	count := t.count
	t.mu.Unlock()
	if shouldLog && t.logger != nil && count > 0 {
		attrs := []any{"source_id", t.source, "flushes", count}
		for _, k := range stableStageOrder {
			if v, ok := snap[k]; ok {
				pct := 0.0
				if total > 0 {
					pct = float64(v) / float64(total) * 100
				}
				attrs = append(attrs, k+"_us", v.Microseconds(), k+"_pct", pct)
			}
		}
		attrs = append(attrs, "total_us", total.Microseconds(), "per_flush_us", total.Microseconds()/count)
		t.logger.Info("ai-viewer-ingest: flush stage timing", attrs...)
	}
}

// stableStageOrder fixes the log field order so diffs are readable.
var stableStageOrder = []string{
	"begin", "apply", "rollups", "fts", "aggregates", "progress_notify", "commit",
}
