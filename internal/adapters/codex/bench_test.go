package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	codexBenchScanFiles                = 32
	codexBenchScanExpectedEvents int64 = 676
	codexBenchTailExpectedEvents int64 = 21
)

// BenchmarkCodexScan_SyntheticCorpus exercises first backfill over a
// deterministic codex_home/sessions/YYYY/MM/DD/rollout-*.jsonl tree. The corpus
// is created under b.TempDir() and contains only fake UUIDs plus generic
// message/tool content.
func BenchmarkCodexScan_SyntheticCorpus(b *testing.B) {
	root := b.TempDir()
	totalBytes := buildCodexBenchCorpus(b, root)

	var scanErrors codexBenchErrorRecorder
	a, err := New(root, canonical.AdapterOptions{OnError: scanErrors.onError})
	if err != nil {
		b.Fatalf("new adapter: %v", err)
	}

	b.ReportAllocs()
	b.SetBytes(totalBytes)
	b.ResetTimer()

	var peakHeap uint64
	var lastEvents int64
	for i := 0; i < b.N; i++ {
		scanErrors.reset()
		events, peak := runCodexBenchScan(b, a)
		lastEvents = events
		if peak > peakHeap {
			peakHeap = peak
		}
		scanErrors.assertEmpty(b, "scan")
		assertCodexBenchEventCount(b, "scan", lastEvents, codexBenchScanExpectedEvents)
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(codexBenchScanFiles)/wallSec, "files/sec")
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap)/(1024*1024), "peak_heap_mb")
}

// BenchmarkCodexTail_SyntheticAppend measures the deterministic Tail flush path
// after fsnotify/debounce has marked a rollout dirty. The producer-side append
// is materialized before ResetTimer; the timed work is only flushDirty reading
// from the seeded cursor and emitting the appended turn.
func BenchmarkCodexTail_SyntheticAppend(b *testing.B) {
	root := b.TempDir()
	setup := prepareCodexBenchTail(b, root)

	b.ReportAllocs()
	// Codex tail replays the whole file from offset 0 to rebuild mapper state.
	b.SetBytes(setup.totalBytes)

	peakHeap, stopSampler := startCodexBenchTailHeapSampler()
	defer stopSampler()

	b.ResetTimer()

	lastEvents := runCodexBenchTailFlushes(b, setup)

	b.StopTimer()

	stopSampler()
	codexBenchRecordCurrentHeap(peakHeap)
	reportCodexBenchTailMetrics(b, lastEvents, peakHeap.Load())
}

type codexBenchTailSetup struct {
	resolvedRoot string
	sourceID     string
	dirty        map[string]struct{}
	seedCursor   Cursor
	out          chan canonical.Event
	totalBytes   int64
}

func prepareCodexBenchTail(b *testing.B, root string) codexBenchTailSetup {
	b.Helper()
	id := uuid7(90)
	rel, path := codexBenchShardPath(root, 0, id)
	seedBody := codexBenchSeedRollout(id)
	appendBody := codexBenchAppendTurn("bench-turn-a", "call-a", "variant-a")
	writeCodexBenchFile(b, path, seedBody)

	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		b.Fatalf("resolve root: %v", err)
	}
	sourceID := sourceIDPrefix + root
	seedCursor := codexBenchSeedCursor(b, resolvedRoot, sourceID, rel)
	appendCodexBenchFile(b, path, appendBody)
	return codexBenchTailSetup{
		resolvedRoot: resolvedRoot,
		sourceID:     sourceID,
		dirty:        map[string]struct{}{rel: {}},
		seedCursor:   seedCursor,
		out:          make(chan canonical.Event, 4096),
		totalBytes:   int64(len(seedBody) + len(appendBody)),
	}
}

func startCodexBenchTailHeapSampler() (*atomic.Uint64, func()) {
	var peakHeap atomic.Uint64
	samplerCtx, samplerCancel := context.WithCancel(context.Background())
	samplerDone := make(chan struct{})
	go codexBenchHeapSampler(samplerCtx, 50*time.Millisecond, &peakHeap, samplerDone)
	stop := func() {
		samplerCancel()
		<-samplerDone
	}
	return &peakHeap, stop
}

func runCodexBenchTailFlushes(b *testing.B, setup codexBenchTailSetup) int64 {
	b.Helper()
	var flushErrors codexBenchErrorRecorder
	var lastEvents int64
	for i := 0; i < b.N; i++ {
		cur := setup.seedCursor
		flushErrors.reset()

		if err := flushDirty(context.Background(), setup.resolvedRoot, setup.sourceID, setup.dirty, &cur, setup.out, flushErrors.onError); err != nil {
			b.Fatalf("flush append %d: %v", i, err)
		}
		flushErrors.assertEmpty(b, "flush append")
		lastEvents = drainCodexBenchEventsExact(b, setup.out, codexBenchTailExpectedEvents)
		assertCodexBenchEventCount(b, "tail append", lastEvents, codexBenchTailExpectedEvents)
	}
	return lastEvents
}

func reportCodexBenchTailMetrics(b *testing.B, lastEvents int64, peakHeap uint64) {
	b.Helper()
	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap)/(1024*1024), "peak_heap_mb")
}

func buildCodexBenchCorpus(b *testing.B, root string) int64 {
	b.Helper()
	var total int64
	for i := 0; i < codexBenchScanFiles; i++ {
		id := uuid7(i)
		_, path := codexBenchShardPath(root, i, id)
		body := codexBenchCompleteRollout(id, i)
		writeCodexBenchFile(b, path, body)
		total += int64(len(body))
	}
	return total
}

func codexBenchShardPath(root string, ordinal int, id string) (rel, path string) {
	day := 20 + ordinal%4
	hour := 10 + ordinal%10
	rel = fmt.Sprintf("2025/11/%02d/rollout-2025-11-%02dT%02d-00-00-%s.jsonl", day, day, hour, id)
	return rel, filepath.Join(root, filepath.FromSlash(rel))
}

func codexBenchCompleteRollout(id string, ordinal int) []byte {
	body := codexBenchSeedRollout(id)
	body = append(body, codexBenchAppendTurn(
		fmt.Sprintf("bench-turn-%02d", ordinal),
		fmt.Sprintf("call-%02d", ordinal),
		fmt.Sprintf("scan-%02d", ordinal),
	)...)
	return body
}

func codexBenchSeedRollout(id string) []byte {
	return []byte(metaLine(id, `"exec"`) + "\n")
}

func codexBenchAppendTurn(turnID, callID, variant string) []byte {
	lines := []string{
		`{"timestamp":"` + tsCtx + `","type":"turn_context","payload":{"turn_id":"` + turnID + `","model":"gpt-5.1-codex-max","effort":"medium","approval_policy":"never","sandbox_policy":{"type":"read-only"}}}`,
		`{"timestamp":"` + tsItem + `","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turnID + `","started_at":1763664000,"model_context_window":200000}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic request ` + variant + `"}]}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"synthetic reasoning ` + variant + `"}]}}`,
		`{"timestamp":"` + tsItem + `","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"cat /example/input.txt\"}","call_id":"` + callID + `"}}`,
		`{"timestamp":"` + tsEvent + `","type":"response_item","payload":{"type":"function_call_output","call_id":"` + callID + `","output":"synthetic command output ` + variant + `"}}`,
		`{"timestamp":"` + tsDone + `","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"synthetic answer ` + variant + `"}]}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"token_count","turn_id":"` + turnID + `","info":{"total_token_usage":{"total_tokens":1200},"last_token_usage":{"input_tokens":300,"cached_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":80}},"model_context_window":200000}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"agent_message","message":"synthetic answer ` + variant + `","phase":"final_answer"}}`,
		`{"timestamp":"` + tsDone + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turnID + `","completed_at":"` + tsDone + `","duration_ms":1000,"time_to_first_token_ms":250}}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func codexBenchSeedCursor(b *testing.B, resolvedRoot, sourceID, rel string) Cursor {
	b.Helper()
	cur := newCursor()
	out := make(chan canonical.Event, 16)
	var seedErrors codexBenchErrorRecorder
	if err := flushDirty(context.Background(), resolvedRoot, sourceID, map[string]struct{}{rel: {}}, &cur, out, seedErrors.onError); err != nil {
		b.Fatalf("seed cursor: %v", err)
	}
	seedErrors.assertEmpty(b, "seed cursor")
	_ = drainCodexBenchEvents(out)
	return cur
}

func runCodexBenchScan(b *testing.B, a *Adapter) (int64, uint64) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan canonical.Event, 4096)
	done := make(chan int64, 1)
	go func() {
		done <- drainCodexBenchEventsUntilClosed(out)
	}()

	var peakHeap atomic.Uint64
	samplerDone := make(chan struct{})
	go codexBenchHeapSampler(ctx, 50*time.Millisecond, &peakHeap, samplerDone)
	stopSampler := func() {
		cancel()
		<-samplerDone
		codexBenchRecordCurrentHeap(&peakHeap)
	}

	if err := a.Scan(ctx, nil, out); err != nil {
		close(out)
		<-done
		stopSampler()
		b.Fatalf("scan: %v", err)
	}
	close(out)
	events := <-done
	stopSampler()
	return events, peakHeap.Load()
}

type codexBenchErrorRecorder struct {
	errs []error
}

func (r *codexBenchErrorRecorder) onError(err error) {
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *codexBenchErrorRecorder) reset() {
	r.errs = r.errs[:0]
}

func (r *codexBenchErrorRecorder) assertEmpty(b *testing.B, phase string) {
	b.Helper()
	if len(r.errs) > 0 {
		b.Fatalf("%s emitted %d error(s), first: %v", phase, len(r.errs), r.errs[0])
	}
}

func assertCodexBenchEventCount(b *testing.B, phase string, got, want int64) {
	b.Helper()
	if got != want {
		b.Fatalf("%s emitted %d events, want %d", phase, got, want)
	}
}

func writeCodexBenchFile(b *testing.B, path string, body []byte) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

func appendCodexBenchFile(b *testing.B, path string, body []byte) {
	b.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		b.Fatalf("open append %s: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			b.Fatalf("close append %s: %v", path, err)
		}
	}()
	if _, err := f.Write(body); err != nil {
		b.Fatalf("append %s: %v", path, err)
	}
}

func drainCodexBenchEvents(ch chan canonical.Event) int64 {
	var n int64
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

func drainCodexBenchEventsExact(b *testing.B, ch chan canonical.Event, want int64) int64 {
	b.Helper()
	for i := int64(0); i < want; i++ {
		select {
		case <-ch:
		default:
			b.Fatalf("drained %d events, want %d", i, want)
		}
	}
	select {
	case <-ch:
		b.Fatalf("drained more than %d events", want)
	default:
	}
	return want
}

func drainCodexBenchEventsUntilClosed(ch chan canonical.Event) int64 {
	var n int64
	for range ch {
		n++
	}
	return n
}

func codexBenchHeapSampler(ctx context.Context, interval time.Duration, peak *atomic.Uint64, done chan<- struct{}) {
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
			codexBenchRecordHeap(peak, ms.HeapInuse)
		}
	}
}

func codexBenchRecordCurrentHeap(peak *atomic.Uint64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	codexBenchRecordHeap(peak, ms.HeapInuse)
}

func codexBenchRecordHeap(peak *atomic.Uint64, cur uint64) {
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
