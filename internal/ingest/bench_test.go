package ingest

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/store"
)

// BenchmarkBatchInsert measures the batched-transaction write path the
// ingest worker pays per flush: BeginTx → ensureSourceRow → per-event
// writer.apply → refreshRollups → refreshFTS → refreshAggregates →
// upsertSourceProgress → emitNotify → pruneNotify → Commit
// (worker.flush). This is the throughput-critical inner loop of
// ingestion — every snapshot the adapters parse funnels through it.
//
// Each iteration flushes the SAME 530-event synthetic batch into a
// FRESH in-memory store so the measurement is the cost of writing one
// full batch from a clean schema (the worst case: no rows to upsert
// against, every index entry created). Building the batch and opening
// the store are excluded from the timer; only the flush is measured.
// 530 = buildSyntheticBatch's constants: 5 sessions (5 SessionStarted)
// + 5×5 turns (25 TurnStarted) + 5×5×10 op pairs (500 OpStarted/
// OpFinalized); matches the baseline's reported batch_events.
//
// The batch mirrors a realistic session shape — a handful of root
// sessions, each with several turns, each turn carrying llm + tool ops
// (start+finalize pairs) — so the per-event apply path exercises
// sessions, turns, ops, rollups, and FTS exactly as production does.
//
// Reported metrics mirror BenchmarkScan_SyntheticCorpus:
//
//   - events/sec     — canonical events committed per second.
//   - peak_heap_mb   — peak runtime HeapInuse during the measured loop
//     (same background-sampler machinery as the adapter bench), so a
//     batch-write allocation regression is visible alongside latency.
func BenchmarkBatchInsert(b *testing.B) {
	const src = "claude_code:/bench"
	const format = "claude_code"
	batch := buildSyntheticBatch(src)

	var peakHeap atomic.Uint64
	samplerCtx, samplerCancel := context.WithCancel(context.Background())
	samplerDone := make(chan struct{})
	go heapSamplerIngest(samplerCtx, 50*time.Millisecond, &peakHeap, samplerDone)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Fresh store per iteration: a batch flushed into an empty schema
		// is the worst-case (every row inserted, no upsert short-circuit).
		// Store open/close is setup, not the measured write.
		s := openBenchStore(b)
		w := &worker{
			sourceID: src, sourceFormat: format, location: "/bench",
			fts5IndexLogs: true,
			db:            s.DB(),
			hwm:           newHWMCache(),
			pricer:        NopPricer{},
			logger:        silentLogger(),
			batchSize:     len(batch),
			batchEvery:    time.Second,
		}
		wr := newWriter(w.sourceID, w.sourceFormat, w.location, w.pricer)
		b.StartTimer()

		if err := w.flush(context.Background(), wr, batch); err != nil {
			b.Fatalf("flush: %v", err)
		}

		b.StopTimer()
		_ = s.Close()
		b.StartTimer()
	}
	b.StopTimer()

	samplerCancel()
	<-samplerDone
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapInuse > peakHeap.Load() {
		peakHeap.Store(ms.HeapInuse)
	}

	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(len(batch))/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap.Load())/(1024*1024), "peak_heap_mb")
	b.ReportMetric(float64(len(batch)), "batch_events")
}

// buildSyntheticBatch constructs a deterministic 530-event canonical
// batch: rootSessions root sessions, each with turnsPerSession turns,
// each turn with opsPerTurn llm+tool op pairs. With rootSessions=5,
// turnsPerSession=5, opsPerTurn=10 that is 5 + (5×5) + (5×5×10×2) =
// 530 events. SourceSeq is globally
// monotonic across the batch so the writer's ordering assumptions hold
// and source_progress advances to a stable max. Timestamps are spaced
// so every op closes (EndTs > start) and lands in a deterministic
// rollup bucket.
func buildSyntheticBatch(src string) []canonical.Event {
	const (
		rootSessions     = 5
		turnsPerSession  = 5
		opsPerTurn       = 10
		baseTS           = int64(1_700_000_000_000_000) // µs
		opStrideUS       = int64(1_000)
		opDurationUS     = int64(500)
		tokensInPerOp    = int64(120)
		tokensOutPerOp   = int64(240)
		costPerOp        = 0.0001
		turnStrideUS     = int64(1_000_000)
		sessionStrideUS  = int64(60_000_000)
		modelName        = "claude-opus-4-7"
		providerName     = "anthropic"
		toolName         = "Bash"
		toolNamespaceArg = "shell"
	)
	var (
		events []canonical.Event
		seq    uint64
	)
	next := func() uint64 { seq++; return seq }

	for sIdx := 0; sIdx < rootSessions; sIdx++ {
		sessTS := baseTS + int64(sIdx)*sessionStrideUS
		sess := fmt.Sprintf("bench-sess-%03d", sIdx)
		events = append(events, canonical.SessionStartedEvent{
			EventBase:    canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: sessTS},
			NativeID:     sess,
			RootNativeID: sess,
			Kind:         canonical.KindRoot,
			AgentName:    "claude",
			Model:        modelName,
			Cwd:          "/work/proj",
		})
		for tIdx := 0; tIdx < turnsPerSession; tIdx++ {
			turnTS := sessTS + int64(tIdx)*turnStrideUS
			turnSeq := tIdx + 1
			events = append(events, canonical.TurnStartedEvent{
				EventBase:       canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: turnTS},
				SessionNativeID: sess, Seq: turnSeq,
			})
			for oIdx := 0; oIdx < opsPerTurn; oIdx++ {
				opStart := turnTS + int64(oIdx)*opStrideUS
				opEnd := opStart + opDurationUS
				opSeq := oIdx + 1
				// Alternate llm / tool ops so both apply paths run.
				if oIdx%2 == 0 {
					events = append(events,
						canonical.OpStartedEvent{
							EventBase:       canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: opStart},
							SessionNativeID: sess, TurnSeq: turnSeq, Seq: opSeq, ParentOpSeq: -1,
							Kind: canonical.OpLLM, Name: "chat", Model: modelName, Provider: providerName,
						},
						canonical.OpFinalizedEvent{
							EventBase:       canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: opEnd},
							SessionNativeID: sess, TurnSeq: turnSeq, Seq: opSeq, Status: "completed", EndTs: opEnd,
							TokensIn: tokensInPerOp, TokensOut: tokensOutPerOp, CostUSD: costPerOp,
						},
					)
				} else {
					events = append(events,
						canonical.OpStartedEvent{
							EventBase:       canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: opStart},
							SessionNativeID: sess, TurnSeq: turnSeq, Seq: opSeq, ParentOpSeq: -1,
							Kind: canonical.OpTool, Name: toolName, ToolNamespace: toolNamespaceArg,
						},
						canonical.OpFinalizedEvent{
							EventBase:       canonical.EventBase{SourceID: src, SourceSeq: next(), Ts: opEnd},
							SessionNativeID: sess, TurnSeq: turnSeq, Seq: opSeq, Status: "completed", EndTs: opEnd,
						},
					)
				}
			}
		}
	}
	return events
}

// openBenchStore opens a fresh in-memory writer-side store for one bench
// iteration. Mirrors openTestStore but takes *testing.B and does not
// register a t.Cleanup (each iteration closes its store explicitly so
// the open connections do not accumulate across b.N).
func openBenchStore(b *testing.B) *store.Store {
	b.Helper()
	s, err := store.OpenWriter(context.Background(), ":memory:", silentLogger())
	if err != nil {
		b.Fatalf("store.OpenWriter: %v", err)
	}
	return s
}

// heapSamplerIngest polls runtime.MemStats every interval and records the
// maximum HeapInuse observed, exiting when ctx is cancelled. Mirrors the
// adapter bench's heapSampler (kept package-local rather than exported
// from a shared bench helper to avoid widening the production surface).
func heapSamplerIngest(ctx context.Context, interval time.Duration, peak *atomic.Uint64, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var ms runtime.MemStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtime.ReadMemStats(&ms)
			cur := ms.HeapInuse
			for {
				old := peak.Load()
				if cur <= old {
					break
				}
				if peak.CompareAndSwap(old, cur) {
					break
				}
			}
		}
	}
}
