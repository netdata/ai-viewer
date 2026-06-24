package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	opencodeBenchScanSessions = 256
	// Each synthetic session emits session, assistant turn, user input, LLM op,
	// text, task, tool payload, and child-session events.
	opencodeBenchEventsPerSession = 11
	// Scan includes the seed corpus plus the six-session second-batch remainder.
	opencodeBenchScanRemainderSessions = 6
	opencodeBenchScanProgressEvents    = 2
	opencodeBenchTailProgressEvents    = 1
	// Scan emits all seed sessions plus the second-batch remainder, plus SourceProgress.
	opencodeBenchScanExpectedEvents = int64((opencodeBenchScanSessions+opencodeBenchScanRemainderSessions)*opencodeBenchEventsPerSession + opencodeBenchScanProgressEvents)
	// Tail emits 2x events: the idempotent boundary re-scan of the seed session
	// plus the forward delta of the appended session, plus SourceProgress.
	opencodeBenchTailExpectedEvents = int64(2*opencodeBenchEventsPerSession + opencodeBenchTailProgressEvents)
	opencodeBenchTailSessionOrdinal = 10_000
)

// BenchmarkOpencodeScan_SyntheticDB exercises a cold historical backfill over a
// deterministic synthetic opencode SQLite database. The fixture is schema-shaped
// but contains only fake ids and generic content.
func BenchmarkOpencodeScan_SyntheticDB(b *testing.B) {
	path := seedOpencodeBenchDB(b, b.TempDir(), opencodeBenchScanSessions)
	totalBytes := opencodeBenchDBSize(b, path)

	b.ReportAllocs()
	b.SetBytes(totalBytes)
	b.ResetTimer()

	var peakHeap uint64
	var lastEvents int64
	for i := 0; i < b.N; i++ {
		events, peak := runOpencodeBenchScan(b, path)
		lastEvents = events
		if peak > peakHeap {
			peakHeap = peak
		}
		assertOpencodeBenchEvents(b, "scan", lastEvents, opencodeBenchScanExpectedEvents)
	}
	b.StopTimer()

	reportOpencodeBenchMetrics(b, lastEvents, peakHeap, opencodeBenchScanSessions)
}

// BenchmarkOpencodeTail_SyntheticAppend measures one deterministic poll cycle
// after a scan cursor exists and one new session tree has already been appended.
// Each iteration replays that same deterministic append from the same seeded
// cursor; this is not a sustained writer/append workload.
func BenchmarkOpencodeTail_SyntheticAppend(b *testing.B) {
	setup := prepareOpencodeBenchTail(b, b.TempDir())

	b.ReportAllocs()

	peakHeap, stopSampler := startOpencodeBenchHeapSampler()
	var stopSamplerOnce sync.Once
	defer stopSamplerOnce.Do(stopSampler)

	b.ResetTimer()

	var lastEvents int64
	for i := 0; i < b.N; i++ {
		lastEvents = runOpencodeBenchPoll(b, setup)
		assertOpencodeBenchEvents(b, "tail append", lastEvents, opencodeBenchTailExpectedEvents)
	}
	b.StopTimer()

	stopSamplerOnce.Do(stopSampler)
	opencodeBenchRecordCurrentHeap(peakHeap)
	reportOpencodeBenchTailMetrics(b, lastEvents, peakHeap.Load())
}

type opencodeBenchTailSetup struct {
	db         *sqlDBWithSchema
	sourceID   string
	seedCursor Cursor
}

type sqlDBWithSchema struct {
	db     *sql.DB
	schema schemaSet
}

func seedOpencodeBenchDB(b *testing.B, dir string, n int) string {
	b.Helper()
	path, rw := newEmptyDB(b, dir, "opencode.db")
	ts := int64(1000)
	for i := 1; i <= n; i++ {
		sid := fmtID("ses", i)
		mid := fmtID("msg", i)
		childID := fmtID("ses_child", i)
		insertSession(b, rw, sid, "", ts, ts, 0)
		ts++
		insertAssistantMessage(b, rw, mid, sid, ts, ts, int64(10*i), int64(5*i))
		insertPart(b, rw, fmtID("prt_01_ss", i), mid, sid, ts, ts, stepStartBody())
		ts++
		insertPart(b, rw, fmtID("prt_02_sf", i), mid, sid, ts, ts, stepFinishBody(int64(10*i), int64(5*i), 0.01))
		ts++
		insertPart(b, rw, fmtID("prt_03_tx", i), mid, sid, ts, ts, textBody("answer"))
		ts++
		taskStart := ts
		taskEnd := ts + 1
		insertPart(b, rw, fmtID("prt_04_task", i), mid, sid, taskStart, taskEnd, opencodeBenchTaskBody(b, i, childID, taskStart, taskEnd))
		ts += 2
	}
	if err := rw.Close(); err != nil {
		b.Fatalf("close rw: %v", err)
	}
	return path
}

func prepareOpencodeBenchTail(b *testing.B, dir string) opencodeBenchTailSetup {
	b.Helper()
	path := seedOpencodeBenchDB(b, dir, 1)
	seedCursor := opencodeBenchScanCursor(b, path)
	appendOpencodeBenchSession(b, path, opencodeBenchTailSessionOrdinal)
	db, schema := introspect(b, path)
	return opencodeBenchTailSetup{
		db:         &sqlDBWithSchema{db: db, schema: schema},
		sourceID:   "opencode:" + path,
		seedCursor: seedCursor,
	}
}

func runOpencodeBenchScan(b *testing.B, path string) (int64, uint64) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event, 64)
	stopCounter := startOpencodeBenchEventCounter(out)
	var peakHeap atomic.Uint64
	done := make(chan struct{})
	go opencodeBenchHeapSampler(ctx, 50*time.Millisecond, &peakHeap, done)

	var errs opencodeBenchErrorRecorder
	_, err := scanLoop(ctx, path, "opencode:"+path, newCursor(), out, silentLogger(), errs.onError)
	events := stopCounter()
	cancel()
	<-done
	opencodeBenchRecordCurrentHeap(&peakHeap)
	if err != nil {
		b.Fatalf("scanLoop: %v", err)
	}
	errs.assertEmpty(b, "scan")
	return events, peakHeap.Load()
}

func opencodeBenchScanCursor(b *testing.B, path string) Cursor {
	b.Helper()
	out := make(chan canonical.Event, 64)
	stopCounter := startOpencodeBenchEventCounter(out)
	var errs opencodeBenchErrorRecorder
	cur, err := scanLoop(ctxBG(), path, "opencode:"+path, newCursor(), out, silentLogger(), errs.onError)
	events := stopCounter()
	if err != nil {
		b.Fatalf("seed scan: %v", err)
	}
	errs.assertEmpty(b, "seed scan")
	assertOpencodeBenchEvents(b, "seed scan", events, int64(opencodeBenchEventsPerSession+opencodeBenchTailProgressEvents))
	return cur
}

func runOpencodeBenchPoll(b *testing.B, setup opencodeBenchTailSetup) int64 {
	b.Helper()
	cur := setup.seedCursor
	st := newPollState(true)
	out := make(chan canonical.Event, 64)
	stopCounter := startOpencodeBenchEventCounter(out)
	var errs opencodeBenchErrorRecorder
	_, err := pollOnce(ctxBG(), testPollRequest(setup.db.db, setup.db.schema, &cur, setup.sourceID, &st, out, errs.onError))
	events := stopCounter()
	if err != nil {
		b.Fatalf("pollOnce: %v", err)
	}
	errs.assertEmpty(b, "tail append")
	return events
}

func appendOpencodeBenchSession(b *testing.B, path string, ordinal int) {
	b.Helper()
	rw, err := openRWAgain(b, path)
	if err != nil {
		b.Fatalf("open rw append: %v", err)
	}
	defer func() {
		if err := rw.Close(); err != nil {
			b.Fatalf("close rw append: %v", err)
		}
	}()
	sid := fmtID("ses", ordinal)
	mid := fmtID("msg", ordinal)
	insertSession(b, rw, sid, "", 100_000, 100_000, 0)
	insertAssistantMessage(b, rw, mid, sid, 100_001, 100_001, 11, 5)
	insertPart(b, rw, fmtID("prt_01_ss", ordinal), mid, sid, 100_001, 100_001, stepStartBody())
	insertPart(b, rw, fmtID("prt_02_sf", ordinal), mid, sid, 100_002, 100_002, stepFinishBody(11, 5, 0.01))
	insertPart(b, rw, fmtID("prt_03_tx", ordinal), mid, sid, 100_003, 100_003, textBody("answer"))
	insertPart(b, rw, fmtID("prt_04_task", ordinal), mid, sid, 100_004, 100_005, opencodeBenchTaskBody(b, ordinal, fmtID("ses_child", ordinal), 100_004, 100_005))
}

func opencodeBenchTaskBody(b *testing.B, ordinal int, childID string, startMs, endMs int64) string {
	b.Helper()
	return opencodeBenchJSON(b, map[string]any{
		"type":   "tool",
		"callID": fmtID("call", ordinal),
		"tool":   "task",
		"state": map[string]any{
			"status":   "completed",
			"title":    "Synthetic task",
			"input":    map[string]any{"description": "synthetic child task", "prompt": "summarize fake fixture"},
			"output":   "synthetic child task complete",
			"metadata": map[string]any{"sessionId": childID},
			"time":     map[string]any{"start": startMs, "end": endMs},
		},
	})
}

func opencodeBenchJSON(b *testing.B, value any) string {
	b.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		b.Fatalf("marshal bench JSON: %v", err)
	}
	return string(body)
}

type opencodeBenchErrorRecorder struct {
	errs []error
}

func (r *opencodeBenchErrorRecorder) onError(err error) {
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *opencodeBenchErrorRecorder) assertEmpty(b *testing.B, phase string) {
	b.Helper()
	if len(r.errs) > 0 {
		b.Fatalf("%s emitted %d error(s), first: %v", phase, len(r.errs), r.errs[0])
	}
}

func assertOpencodeBenchEvents(b *testing.B, phase string, got, want int64) {
	b.Helper()
	if got != want {
		b.Fatalf("%s emitted %d events, want %d", phase, got, want)
	}
}

func startOpencodeBenchEventCounter(ch <-chan canonical.Event) func() int64 {
	done := make(chan struct{})
	counted := make(chan int64, 1)
	go func() {
		var n int64
		for {
			select {
			case <-ch:
				n++
			case <-done:
				for {
					select {
					case <-ch:
						n++
					default:
						counted <- n
						return
					}
				}
			}
		}
	}()
	return func() int64 {
		close(done)
		return <-counted
	}
}

func opencodeBenchDBSize(b *testing.B, path string) int64 {
	b.Helper()
	info, err := os.Stat(path)
	if err != nil {
		b.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func startOpencodeBenchHeapSampler() (*atomic.Uint64, func()) {
	var peakHeap atomic.Uint64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go opencodeBenchHeapSampler(ctx, 50*time.Millisecond, &peakHeap, done)
	stop := func() {
		cancel()
		<-done
	}
	return &peakHeap, stop
}

func opencodeBenchHeapSampler(ctx context.Context, interval time.Duration, peak *atomic.Uint64, done chan<- struct{}) {
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
			opencodeBenchRecordHeap(peak, ms.HeapInuse)
		}
	}
}

func opencodeBenchRecordCurrentHeap(peak *atomic.Uint64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	opencodeBenchRecordHeap(peak, ms.HeapInuse)
}

func opencodeBenchRecordHeap(peak *atomic.Uint64, cur uint64) {
	for {
		old := peak.Load()
		if cur <= old {
			return
		}
		if peak.CompareAndSwap(old, cur) {
			return
		}
	}
}

func reportOpencodeBenchMetrics(b *testing.B, lastEvents int64, peakHeap uint64, sessions int) {
	b.Helper()
	wallSec := opencodeBenchWallSeconds(b)
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(sessions)/wallSec, "sessions/sec")
	b.ReportMetric(float64(peakHeap)/(1024*1024), "peak_heap_mb")
}

func reportOpencodeBenchTailMetrics(b *testing.B, lastEvents int64, peakHeap uint64) {
	b.Helper()
	wallSec := opencodeBenchWallSeconds(b)
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap)/(1024*1024), "peak_heap_mb")
}

func opencodeBenchWallSeconds(b *testing.B) float64 {
	b.Helper()
	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		return 1e-9
	}
	return wallSec
}
