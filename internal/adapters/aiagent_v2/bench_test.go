package aiagent_v2

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// BenchmarkScan_SyntheticCorpus exercises the v2 adapter's Scan path
// against a deterministic synthetic corpus that mirrors the
// approximate size distribution of the operator's real
// ~/.ai-agent/sessions directory (median ~10 KB compressed, p99
// ~1 MB), all staying below the 50 MiB streamer threshold so the
// bench measures the dominant small/medium-file fast path. The
// corpus is regenerated under b.TempDir() on each
// bench invocation so the run is hermetic and never touches operator
// data.
//
// The benchmark reports custom metrics in addition to the stock
// ns/op and B/s figures:
//
//   - files/sec        — files processed per second of measured wall time
//   - events/sec       — canonical events emitted per second
//   - peak_heap_mb     — peak runtime.MemStats.HeapInuse observed by a
//     background sampler; this is the metric adapter-aiagent-v2.md
//     §Memory talks about ("≤ 40 MB across 8 workers" for p99,
//     "MUST stream" for the 151 MB compressed outlier). We require
//     it to stay under 200 MiB on the synthetic corpus, which
//     deliberately stays BELOW the 50 MiB streamer threshold
//     (streamer behavior is covered by streamer_test.go). The bound
//     is intentionally generous against the
//     spec's worker-pool numbers because the bench is sequential
//     (one in-flight file at a time).
//
// Throughput reported in MB/s by `go test -bench` is the
// *decompressed* throughput (see b.SetBytes), which is the
// quantity adapter-aiagent-v2.md §Performance Considerations
// measures.
//
// The benchmark intentionally runs Scan with a freshly-allocated
// cursor (no dedup) so the measured cost is the worst-case
// "first backfill" scenario. Re-scan economics are validated by the
// cursor unit tests, not here.
func BenchmarkScan_SyntheticCorpus(b *testing.B) {
	root := b.TempDir()
	totalBytes, totalCompressed, fileCount := buildSyntheticCorpus(b, root)

	a, err := New(root, canonical.AdapterOptions{})
	if err != nil {
		b.Fatalf("new adapter: %v", err)
	}

	b.ReportAllocs()
	b.SetBytes(totalBytes)
	b.ResetTimer()

	var peakHeap uint64
	var lastEvents int64
	for i := 0; i < b.N; i++ {
		events, peak := runScan(b, a)
		lastEvents = events
		if peak > peakHeap {
			peakHeap = peak
		}
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds() / float64(b.N)
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(fileCount)/wallSec, "files/sec")
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap)/(1024*1024), "peak_heap_mb")
	b.ReportMetric(float64(totalCompressed)/(1024*1024), "corpus_compressed_mb")
	if peakHeap > 200*1024*1024 {
		b.Errorf("peak HeapInuse %d MiB exceeds 200 MiB cap per adapter-aiagent-v2.md §Memory", peakHeap/(1024*1024))
	}
}

// BenchmarkTail_SyntheticAppend measures the steady-state cost the v2
// tailer pays per detected snapshot rewrite: stat → decompress → hash →
// parse → map → emit. It drives processOnce directly — the exact unit
// tailLoop invokes for each dirty file on a debounce flush
// (tailer.go:169) — rather than the full Tail goroutine, because the
// goroutine's wall time is dominated by non-deterministic OS inotify
// delivery latency, not the CPU work this benchmark is meant to track
// for regression. (The fsnotify→debounce→flush wiring itself is covered
// by TestTail_* in tailer_test.go; its latency is an OS property, not
// ours to benchmark.)
//
// v2 is whole-file: a producer rewrites <originId>.json.gz on every
// snapshot, so "appending new lines" maps to "rewrite the gzip with one
// more turn appended". The benchmark measures only the TAILER's per-rewrite
// CPU — stat → decompress → hash → parse → map → emit — NOT the producer's
// write. The file write is the agent process's job in production; here it is
// fenced out of the timed region with b.StopTimer/b.StartTimer (mirroring
// BenchmarkBatchInsert's store-open fencing) so the reported number is the
// steady-state tail-step cost, not disk-write jitter.
//
// DETERMINISM (was ~±200% run-to-run, benchstat reported `~`, SOW-0011
// review): two stabilizations.
//  1. FIXED content. Earlier each iteration rewrote DISTINCT high-entropy
//     padding, so every decompress+parse touched different bytes and the
//     per-op CPU itself varied. Now exactly TWO byte-identical fixed bodies
//     (variant 0/1) are pre-built before the timer and alternated A,B,A,B…:
//     consecutive contents always differ (so the content hash always
//     mismatches the cursor and the full decompress+parse+map+emit path runs
//     — a rewrite that re-hashed to the cursor would short-circuit at
//     scanner.go and measure nothing), while the work per iteration is
//     constant (only two distinct payloads, identical size and shape).
//  2. WRITE FENCED OUT. The dominant variance was per-iteration os.WriteFile
//     to the ext4-backed b.TempDir(). The write now runs under b.StopTimer;
//     the timed region is purely processOnce, which reads the just-written
//     file back from the page cache (fast, steady) and re-stats — with no
//     write inside the timed window, processOnce's post-read mtime check
//     never fires its retry, so the timed work is exactly one processFile.
//
// Reported metrics mirror BenchmarkScan_SyntheticCorpus:
//
//   - MB/s (b.SetBytes)   — DECOMPRESSED bytes mapped per second, the
//     same throughput quantity adapter-aiagent-v2.md §Performance
//     Considerations tracks.
//   - events/sec          — canonical events emitted per second.
//   - peak_heap_mb         — peak runtime HeapInuse during the measured
//     loop (background sampler, same machinery as runScan). The tailed
//     file stays well under the 50 MiB streamer threshold, so this is
//     the in-memory whole-file path; a generous 200 MiB cap guards a
//     gross allocation regression without being flaky.
//
// The seed write before ResetTimer establishes a cursor (so the first
// measured iteration already exercises the rewrite-detection path, not
// a first-sight emit) and is excluded from the timing.
func BenchmarkTail_SyntheticAppend(b *testing.B) {
	root := b.TempDir()
	const origin = "tail-bench"
	name := origin + ".json.gz"

	// Seed: write an initial snapshot and prime the cursor by processing
	// it once, so the measured loop only ever exercises the steady-state
	// "rewrite detected" path.
	// Channel cap generously exceeds the per-rewrite event count (a few
	// hundred for this fixture) so processOnce never blocks on send and
	// we can drain synchronously after each call — making the per-step
	// event count exact and the measurement free of a drainer goroutine.
	const chanCap = 8192
	writeBenchSnapshot(b, root, name, syntheticSnapshot(origin, 6, 200, 1))
	cur := newCursor()
	seedOut := make(chan canonical.Event, chanCap)
	if err := processOnce(context.Background(), root, sourceIDPrefix+root, name, &cur, seedOut, func(error) {}); err != nil {
		b.Fatalf("seed processOnce: %v", err)
	}

	// Pre-build the TWO fixed rewrite bodies (variant 0 and 1) ONCE, before
	// the timer. Turn/op COUNT and padding content are both held constant, so
	// every A iteration does identical work and every B iteration does
	// identical work; the only thing that changes between A and B is the
	// fixed pad string, which flips the content hash so processOnce never
	// short-circuits. JSON marshal + gzip is excluded from the timer.
	bodies := [2][]byte{
		mkGzipBytes(mustJSON(b, tailRewriteSnapshotFixed(origin, 0))),
		mkGzipBytes(mustJSON(b, tailRewriteSnapshotFixed(origin, 1))),
	}
	// Both variants decompress to the same byte count (identical shape, equal
	// pad length), so a single constant is the per-op decompressed size.
	decompressed := int64(len(mustJSON(b, tailRewriteSnapshotFixed(origin, 0))))

	out := make(chan canonical.Event, chanCap)
	var peakHeap atomic.Uint64
	samplerCtx, samplerCancel := context.WithCancel(context.Background())
	samplerDone := make(chan struct{})
	go heapSampler(samplerCtx, 50*time.Millisecond, &peakHeap, samplerDone)

	var totalBytes int64
	var lastEvents int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Rewrite the file in place with the next fixed body. The write is
		// the PRODUCER's action, not the tailer's, and is the dominant
		// disk-I/O variance source — fence it out of the timed region. The
		// content differs from the previous iteration (A↔B), so the content
		// hash mismatches the cursor and processOnce re-emits.
		b.StopTimer()
		if err := os.WriteFile(filepath.Join(root, name), bodies[i%2], 0o600); err != nil {
			b.Fatalf("rewrite %d: %v", i, err)
		}
		b.StartTimer()

		if err := processOnce(context.Background(), root, sourceIDPrefix+root, name, &cur, out, func(error) {}); err != nil {
			b.Fatalf("processOnce %d: %v", i, err)
		}
		// Synchronous non-blocking drain: count exactly what this step
		// emitted and keep the channel empty for the next iteration.
		lastEvents = 0
		for drainedOne := true; drainedOne; {
			select {
			case <-out:
				lastEvents++
			default:
				drainedOne = false
			}
		}
		totalBytes += decompressed
	}
	b.StopTimer()

	b.SetBytes(totalBytes / int64(max(b.N, 1)))
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
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
	b.ReportMetric(float64(peakHeap.Load())/(1024*1024), "peak_heap_mb")
	if peakHeap.Load() > 200*1024*1024 {
		b.Errorf("peak HeapInuse %d MiB exceeds 200 MiB cap for the whole-file tail path", peakHeap.Load()/(1024*1024))
	}
}

// mustJSON marshals v or fails the benchmark. Keeps the body-building lines
// in BenchmarkTail_SyntheticAppend terse while still surfacing a marshal
// error instead of panicking.
func mustJSON(b *testing.B, v any) []byte {
	b.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		b.Fatalf("marshal: %v", err)
	}
	return raw
}

// tailRewriteSnapshotFixed builds one of TWO fixed rewrite bodies for the
// tail benchmark. variant must be 0 or 1; the two bodies are byte-identical
// in shape, op/turn count, and pad LENGTH, differing only in a single fixed
// marker byte in the pad so their gzip content hashes differ. Alternating
// the two (A,B,A,B…) guarantees every rewrite mismatches the previous
// content hash — forcing processOnce's full decompress+parse+map path — while
// keeping the per-iteration CPU constant (no per-iteration entropy), which is
// what removed the benchmark's content-driven variance. Shape matches a small
// active session a producer rewrites on each new operation.
func tailRewriteSnapshotFixed(origin string, variant int) snapshot {
	const (
		turns = 2
		ops   = 6
		pad   = 200
	)
	// A fixed, representative pad: a constant printable filler whose only
	// per-variant difference is the leading marker byte. Same length and same
	// (incompressible-enough) shape for both variants, so decompressed size
	// and parse cost are identical; only the hash differs.
	marker := byte('A' + variant) // 'A' or 'B'
	padBytes := make([]byte, pad)
	padBytes[0] = marker
	for j := 1; j < pad; j++ {
		// Deterministic, content-fixed filler (NOT seeded by iteration). The
		// span of printable values keeps gzip from collapsing it to near-zero
		// so the decompress path does representative work.
		padBytes[j] = byte(0x21 + (j*7)%94)
	}
	padStr := string(padBytes)

	snap := simpleSnapshot(2, origin)
	snap.OpTree.Turns = snap.OpTree.Turns[:0]
	for t := 0; t < turns; t++ {
		turn := turnNode{
			ID:        fmt.Sprintf("turn-%d", t+1),
			Index:     t + 1,
			StartedAt: 1700000000000 + int64(t*1000),
			EndedAt:   int64Ptr(1700000001000 + int64(t*1000)),
		}
		for o := 0; o < ops; o++ {
			turn.Ops = append(turn.Ops, operationNode{
				OpID:      fmt.Sprintf("op-%s-%d-%d", origin, t, o),
				Kind:      "tool",
				StartedAt: 1700000000100 + int64(t*1000+o),
				EndedAt:   int64Ptr(1700000000200 + int64(t*1000+o)),
				Status:    "ok",
				Attributes: map[string]json.RawMessage{
					"name": json.RawMessage(`"synthetic"`),
					"pad":  mustMarshal(padStr),
				},
				Accounting: []accountingEntry{
					{Type: "tool", CharactersIn: 100, CharactersOut: 200},
				},
			})
		}
		snap.OpTree.Turns = append(snap.OpTree.Turns, turn)
	}
	return snap
}

// writeBenchSnapshot marshals + gzips a snapshot to <root>/<name>
// without a *testing.T (the bench helpers take *testing.B). Mirrors
// writeSnapshot in helpers_test.go.
func writeBenchSnapshot(b *testing.B, root, name string, snap snapshot) {
	b.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		b.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		b.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), mkGzipBytes(body), 0o600); err != nil {
		b.Fatalf("write: %v", err)
	}
}

// runScan drives one Scan invocation start-to-finish, draining the
// event channel concurrently so the adapter never blocks on send.
// Returns the count of canonical events emitted and the peak heap
// in-use (runtime.MemStats.HeapInuse) observed by a background
// sampler that polls every 50 ms during the call. HeapInuse is the
// live-bytes high-water mark inside the Go runtime's heap; it is
// the right metric to compare against adapter-aiagent-v2.md
// §Memory because it tracks the streamer's bounded-allocation
// promise without including arena slack the OS has not yet
// reclaimed.
func runScan(b *testing.B, a *Adapter) (int64, uint64) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan canonical.Event, 1024)

	var count int64
	doneDrain := make(chan struct{})
	go func() {
		for range out {
			count++
		}
		close(doneDrain)
	}()

	var peakHeap atomic.Uint64
	samplerDone := make(chan struct{})
	go heapSampler(ctx, 50*time.Millisecond, &peakHeap, samplerDone)

	if err := a.Scan(ctx, nil, out); err != nil {
		b.Fatalf("scan: %v", err)
	}
	close(out)
	<-doneDrain
	cancel()
	<-samplerDone

	// One final reading so even a sub-50 ms peak after the last
	// sampler tick is captured.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapInuse > peakHeap.Load() {
		peakHeap.Store(ms.HeapInuse)
	}
	return count, peakHeap.Load()
}

// heapSampler polls runtime.MemStats every interval and records the
// maximum HeapInuse observed. Exits when ctx is cancelled. The
// channel close signals the caller that no further updates will
// happen.
func heapSampler(ctx context.Context, interval time.Duration, peak *atomic.Uint64, done chan<- struct{}) {
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

// buildSyntheticCorpus writes ~1000 .json.gz fixtures into root and
// returns (decompressed_bytes, compressed_bytes, file_count).
//
// Distribution scales the operator's real ~294K-file / 25 GB corpus
// from adapter-aiagent-v2.md §Sizing reality down to ~1000 files
// while staying BELOW the streamer threshold so the bench measures
// the small-and-medium file fast path that dominates real
// throughput (>99.9% of operator files):
//
//	~990 files at the ~10 KB compressed median band
//	~10  files at the ~200 KB to ~1.2 MB compressed p99 band
//
// The bench deliberately does NOT include files above the 50 MiB
// streamer threshold: streamer correctness is exercised by
// TestStreamer_AgreesWithNonStreaming and
// TestStreamer_LargePayloadOverThreshold (in streamer_test.go),
// and streamer throughput is exercised end-to-end by the real-data
// harness under scripts/bench-v2-backfill.sh (which sees the real
// >50 MiB outliers in the operator's directory). The synthetic
// bench's role is regression detection on the hot path — the
// 200 MiB heap cap below depends on that scope.
//
// All fixtures share the canonical v2 envelope shape so the parser
// and mapper exercise the same code paths as production. UUIDs and
// timestamps are deterministic per file index so the corpus is
// byte-reproducible across runs.
func buildSyntheticCorpus(b *testing.B, root string) (decompressed, compressed int64, count int) {
	b.Helper()
	// Sample classes: (count, ops-per-turn, padSize-per-op, turn-count).
	// padSize uses the high-entropy printable-ASCII generator from
	// streamer_test.go so files do not deflate to near-zero. The
	// per-class parameters target the size bands described above
	// after gzip; "small" lands at ~10 KB compressed, "large" lands
	// at a few hundred KB.
	classes := []struct {
		n       int
		ops     int
		pad     int
		turns   int
		bracket string
	}{
		{n: 990, ops: 3, pad: 200, turns: 1, bracket: "small"},
		{n: 10, ops: 60, pad: 2000, turns: 4, bracket: "large"},
	}

	idx := 0
	for _, c := range classes {
		for i := 0; i < c.n; i++ {
			origin := fmt.Sprintf("synthetic-%06d-%s", idx, c.bracket)
			snap := syntheticSnapshot(origin, c.ops, c.pad, c.turns)
			body, err := json.Marshal(snap)
			if err != nil {
				b.Fatalf("marshal %s: %v", origin, err)
			}
			decompressed += int64(len(body))
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, werr := zw.Write(body); werr != nil {
				b.Fatalf("gzip write %s: %v", origin, werr)
			}
			if cerr := zw.Close(); cerr != nil {
				b.Fatalf("gzip close %s: %v", origin, cerr)
			}
			path := filepath.Join(root, origin+".json.gz")
			if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
				b.Fatalf("write %s: %v", origin, err)
			}
			compressed += int64(buf.Len())
			idx++
		}
	}
	return decompressed, compressed, idx
}

// syntheticSnapshot constructs a deterministic v2 envelope for the
// bench corpus. The shape mirrors a single root session with `turns`
// turns each carrying `ops` operations padded with high-entropy
// bytes so the resulting JSON is incompressible enough to land at
// the target size. The traceId is derived from origin so the
// adapter's mapper produces stable session ids.
func syntheticSnapshot(origin string, ops, padSize, turnCount int) snapshot {
	snap := simpleSnapshot(2, origin)
	snap.OpTree.Turns = snap.OpTree.Turns[:0]
	for t := 0; t < turnCount; t++ {
		turn := turnNode{
			ID:        fmt.Sprintf("turn-%d", t+1),
			Index:     t + 1,
			StartedAt: 1700000000000 + int64(t*1000),
			EndedAt:   int64Ptr(1700000001000 + int64(t*1000)),
		}
		for i := 0; i < ops; i++ {
			pad := highEntropyString(t*ops+i, padSize)
			turn.Ops = append(turn.Ops, operationNode{
				OpID:      fmt.Sprintf("op-%s-%d-%d", origin, t, i),
				Kind:      "tool",
				StartedAt: 1700000000100 + int64(t*1000+i),
				EndedAt:   int64Ptr(1700000000200 + int64(t*1000+i)),
				Status:    "ok",
				Attributes: map[string]json.RawMessage{
					"name": json.RawMessage(`"synthetic"`),
					"pad":  mustMarshal(pad),
				},
				Accounting: []accountingEntry{
					{Type: "tool", CharactersIn: 100, CharactersOut: 200},
				},
			})
		}
		snap.OpTree.Turns = append(snap.OpTree.Turns, turn)
	}
	return snap
}
