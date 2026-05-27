// Command backfillbench measures the wall-clock cost of a full
// read+parse pass over an ai-agent v2 sessions directory using the
// real v2 adapter. It is the operator-runnable companion to the
// hermetic `go test -bench=BenchmarkScan_SyntheticCorpus` in this
// package and produces the numbers SOW-0001 Chunk 9 evaluates
// against the 60-minute backfill gate.
//
// Read-only contract: the harness opens every file via the v2
// adapter's `ScanFile` helper, which uses `os.Open` (O_RDONLY) and
// never writes, deletes, renames, mtime-touches, or otherwise
// modifies the source tree. The harness itself only walks the
// directory via os.ReadDir and emits its output to stdout. It is
// safe to run against the operator's live `~/.ai-agent/sessions`.
//
// Run from the repository root:
//
//	go run ./internal/adapters/aiagent_v2/cmd/backfillbench \
//	    --root "$HOME/.ai-agent/sessions" \
//	    --workers 8 \
//	    --progress-interval 5s
//
// All canonical events are drained into /dev/null; no SQLite, no
// ingester. The point is to measure adapter cost in isolation. If
// the adapter's wall time blows the SOW-0001 60-minute gate this
// program prints the bottleneck breakdown and exits non-zero so the
// SOW pauses for redesign per its §Chunk 9 plan.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/aiagent_v2"
	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	snapshotExt     = ".json.gz"
	tmpSuffixPrefix = ".tmp-"
)

// metrics holds the running tallies the harness reports periodically.
// All fields are accessed under atomic.* to keep the worker goroutines
// lock-free; the reporter reads them concurrently.
type metrics struct {
	filesTotal      atomic.Int64
	filesProcessed  atomic.Int64
	filesSkippedTmp atomic.Int64
	filesSkippedDot atomic.Int64
	filesZeroByte   atomic.Int64
	filesErrored    atomic.Int64
	bytesCompressed atomic.Int64
	eventsEmitted   atomic.Int64
	streamedFiles   atomic.Int64 // files routed via the > 50 MiB streamer
	parseErrors     atomic.Int64
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("backfillbench: %v", err)
	}
}

func run() error {
	defaultRoot := filepath.Join(os.Getenv("HOME"), ".ai-agent", "sessions")
	root := flag.String("root", defaultRoot, "directory containing v2 .json.gz files (read-only)")
	workers := flag.Int("workers", runtime.NumCPU(), "number of worker goroutines processing files concurrently")
	progress := flag.Duration("progress-interval", 5*time.Second, "wall-clock interval between progress reports")
	maxFiles := flag.Int("max-files", 0, "if > 0, process at most this many files (for smoke tests)")
	noPageCacheWarn := flag.Bool("no-page-cache-warn", false, "suppress the cold-cache reminder banner")
	flag.Parse()

	if *root == "" {
		return errors.New("--root must be non-empty")
	}
	// #nosec G703 -- --root is an operator-supplied path; this is a
	// CLI bench tool whose purpose is to scan the operator's own
	// session directory. No untrusted-input boundary.
	info, err := os.Stat(*root)
	if err != nil {
		return fmt.Errorf("--root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--root %q is not a directory", *root)
	}
	if *workers < 1 {
		return errors.New("--workers must be >= 1")
	}
	if !*noPageCacheWarn {
		fmt.Fprintf(os.Stderr, "backfillbench: reading from %s (read-only). Cold page cache will skew the first run; consider `cat %s/*.json.gz > /dev/null` (read-only) to warm before benching.\n", *root, *root)
	}

	files, err := listSnapshotFiles(*root)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	if *maxFiles > 0 && len(files) > *maxFiles {
		files = files[:*maxFiles]
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	m := &metrics{}
	m.filesTotal.Store(int64(len(files)))

	// Pre-stat the corpus to count skip classes before the workers
	// start; mirrors what scanner.go's listSnapshots filter would do
	// but lets us report the skipped counts as part of the final
	// summary even though the helper handles them per-file.
	tmpSkipped, dotSkipped := countSkipped(*root)
	m.filesSkippedTmp.Store(tmpSkipped)
	m.filesSkippedDot.Store(dotSkipped)

	sourceID := "aiagent_v2:" + *root

	jobs := make(chan string, *workers*4)
	var wg sync.WaitGroup
	startWorkers(ctx, *workers, *root, sourceID, jobs, m, &wg)

	reporterDone := make(chan struct{})
	wallStart := time.Now()
	go reporter(ctx, m, wallStart, *progress, reporterDone)

	for _, name := range files {
		if ctx.Err() != nil {
			break
		}
		jobs <- name
	}
	close(jobs)
	wg.Wait()

	wallElapsed := time.Since(wallStart)
	cancel()
	<-reporterDone

	printFinal(m, wallElapsed, *workers, *root, runtime.NumCPU())
	if wallElapsed > 60*time.Minute {
		return fmt.Errorf("backfill wall time %s exceeds SOW-0001 60-minute gate", wallElapsed.Round(time.Second))
	}
	return nil
}

// listSnapshotFiles returns the sorted basenames of `.json.gz` files
// at root that are eligible for the v2 adapter (matches
// aiagent_v2.ListSnapshots filter rules: skip dotfiles, skip
// `.tmp-*` orphans). The list is built up front so the harness can
// shard it across workers via a channel and so progress reporting
// has a known denominator.
func listSnapshotFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isSnapshotName(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// countSkipped counts the .tmp-* orphans and other dotfile/tmp
// skipped entries up front so the final report reflects what would
// otherwise be invisible filter activity inside the adapter.
func countSkipped(root string) (tmpSkipped, dotSkipped int64) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.Contains(name, tmpSuffixPrefix) {
			tmpSkipped++
			continue
		}
		if !strings.HasSuffix(name, snapshotExt) {
			continue
		}
		if name[0] == '.' {
			dotSkipped++
		}
	}
	return tmpSkipped, dotSkipped
}

func isSnapshotName(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	if strings.Contains(name, tmpSuffixPrefix) {
		return false
	}
	return strings.HasSuffix(name, snapshotExt)
}

// startWorkers fans out `workers` goroutines that each pull
// filenames from `jobs`, invoke aiagent_v2.ScanFile, drain the
// emitted events into a counter, and update the shared metrics.
// Each worker owns its own output channel so the v2 adapter's
// per-call channel-ownership contract is preserved.
func startWorkers(ctx context.Context, workers int, root, sourceID string, jobs <-chan string, m *metrics, wg *sync.WaitGroup) {
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			workerLoop(ctx, root, sourceID, jobs, m)
		}()
	}
}

func workerLoop(ctx context.Context, root, sourceID string, jobs <-chan string, m *metrics) {
	out := make(chan canonical.Event, 256)
	doneDrain := make(chan struct{})
	go func() {
		for ev := range out {
			m.eventsEmitted.Add(1)
			if _, ok := ev.(canonical.SourceErrorEvent); ok {
				m.parseErrors.Add(1)
			}
		}
		close(doneDrain)
	}()
	defer func() {
		close(out)
		<-doneDrain
	}()

	for name := range jobs {
		if ctx.Err() != nil {
			return
		}
		// name comes from os.ReadDir which returns base filenames only
		// (no path separators per Go docs); joining with the
		// operator-validated root cannot escape. G703 is a taint-
		// propagation false positive in this CLI context.
		full := filepath.Join(root, name) // #nosec G703
		info, sterr := os.Stat(full)      // #nosec G703
		if sterr != nil {
			m.filesErrored.Add(1)
			continue
		}
		if info.Size() == 0 {
			m.filesZeroByte.Add(1)
			m.filesProcessed.Add(1)
			continue
		}
		if info.Size() > aiagent_v2.StreamerThresholdBytes {
			m.streamedFiles.Add(1)
		}
		// Forcing a zero FileCursor means dedup is bypassed. That's
		// the desired worst-case "first backfill" workload for the
		// benchmark; cursor economics are exercised by the unit
		// tests.
		_, _, err := aiagent_v2.ScanFile(ctx, root, sourceID, name, aiagent_v2.FileCursor{}, out, func(err error) {
			m.parseErrors.Add(1)
			_ = err
		})
		if err != nil {
			m.filesErrored.Add(1)
			continue
		}
		m.bytesCompressed.Add(info.Size())
		m.filesProcessed.Add(1)
	}
}

// reporter prints a one-line progress snapshot every interval until
// ctx is cancelled. Lines go to stderr so the final summary on
// stdout is easy to capture into baseline.txt.
func reporter(ctx context.Context, m *metrics, start time.Time, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed := m.filesProcessed.Load()
			total := m.filesTotal.Load()
			elapsed := time.Since(start).Seconds()
			if elapsed <= 0 {
				elapsed = 1
			}
			fps := float64(processed) / elapsed
			mbCompressed := float64(m.bytesCompressed.Load()) / (1024 * 1024)
			mbPerSec := mbCompressed / elapsed
			events := m.eventsEmitted.Load()
			eps := float64(events) / elapsed
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			rss := float64(ms.Sys) / (1024 * 1024)
			fmt.Fprintf(os.Stderr,
				"[%s] files=%d/%d (%.1f%%) %.0f files/s  %.1f MB/s compressed  %.0f events/s  rss=%.0f MB  errs=%d  zero=%d\n",
				formatHMS(time.Since(start)),
				processed, total,
				percent(processed, total),
				fps, mbPerSec, eps, rss,
				m.parseErrors.Load(), m.filesZeroByte.Load(),
			)
		}
	}
}

// printFinal writes the end-of-run summary to stdout in a format
// suitable for capture into `bench/baseline.txt`. Numbers are
// printed twice — once as a human-readable block and once as a
// single-line digest mirroring the baseline.txt convention from
// quality-gates.md so a future run can grep for the same fields.
func printFinal(m *metrics, wall time.Duration, workers int, root string, ncpu int) {
	processed := m.filesProcessed.Load()
	bytesC := m.bytesCompressed.Load()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	mbC := float64(bytesC) / (1024 * 1024)
	wallSec := wall.Seconds()
	if wallSec <= 0 {
		wallSec = 1
	}
	fps := float64(processed) / wallSec
	mbps := mbC / wallSec
	rssMB := float64(ms.Sys) / (1024 * 1024)

	fmt.Println()
	fmt.Println("BackfillV2_RealCorpus summary")
	fmt.Println("=============================")
	fmt.Printf("root              : %s\n", root)
	fmt.Printf("workers           : %d (NumCPU=%d)\n", workers, ncpu)
	fmt.Printf("wall_time         : %s\n", wall.Round(time.Millisecond))
	fmt.Printf("files_scanned     : %d\n", m.filesTotal.Load())
	fmt.Printf("files_processed   : %d\n", processed)
	fmt.Printf("files_zero_byte   : %d\n", m.filesZeroByte.Load())
	fmt.Printf("files_skipped_tmp : %d (.tmp-*)\n", m.filesSkippedTmp.Load())
	fmt.Printf("files_skipped_dot : %d (dotfiles)\n", m.filesSkippedDot.Load())
	fmt.Printf("files_errored     : %d (stat/scan I/O error)\n", m.filesErrored.Load())
	fmt.Printf("streamed_files    : %d (compressed > %d bytes)\n", m.streamedFiles.Load(), aiagent_v2.StreamerThresholdBytes)
	fmt.Printf("parse_errors      : %d (per-record SourceErrorEvents + onError)\n", m.parseErrors.Load())
	fmt.Printf("events_emitted    : %d\n", m.eventsEmitted.Load())
	fmt.Printf("bytes_compressed  : %d (%.2f GB)\n", bytesC, float64(bytesC)/(1024*1024*1024))
	fmt.Printf("files_per_sec     : %.1f\n", fps)
	fmt.Printf("throughput_MB_s   : %.2f (compressed)\n", mbps)
	fmt.Printf("peak_rss_MB       : %.1f\n", rssMB)

	// One-line digest matching the BackfillV2_RealCorpus row format
	// described in bench/README.md so a future run can paste it
	// directly into bench/baseline.txt.
	fmt.Printf("\nBackfillV2_RealCorpus    wall=%s files=%d files_per_sec=%.0f bytes=%dB throughput=%.0fMB/s peak_rss=%.0fMB workers=%d\n",
		wall.Round(time.Second),
		processed,
		fps,
		bytesC,
		mbps,
		rssMB,
		workers,
	)
}

func percent(n, total int64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func formatHMS(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	s := int(d.Seconds()) - h*3600 - m*60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
