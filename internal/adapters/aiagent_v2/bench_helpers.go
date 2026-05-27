package aiagent_v2

import (
	"context"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// ScanFile parses one v2 snapshot file at <root>/<name> and emits its
// canonical events to `out`. It exposes the per-file fast path the
// scanner uses internally (decompress + parse + map + size-bounded
// streaming for files above the 50 MiB threshold) so external
// benchmarking and diagnostic harnesses can drive the adapter at
// file granularity without paying for a full directory walk and
// without re-implementing the read-only file handling that
// adapter-aiagent-v2.md defines.
//
// Read-only contract: this function opens the file with the same
// O_RDONLY path that processFile uses; it never writes, deletes,
// renames, or mutates anything under `root`.
//
// Cursor handling: callers that want stat-only / content-hash dedup
// must supply a non-zero FileCursor. Passing the zero value forces a
// fresh decompress+parse, which is the desired behaviour for raw
// throughput measurements where dedup would mask the cost we want to
// measure.
//
// Concurrency: the helper is safe to call from multiple goroutines
// concurrently provided each goroutine drives a disjoint subset of
// files (no shared cursor mutation). The function does not mutate
// any package-level state.
//
// Out-of-band errors: per-record parse errors are surfaced via
// onError. Envelope-level corruption (gzip header, malformed JSON)
// is surfaced as a SourceErrorEvent inside the returned events when
// the underlying read path produces one; the function never returns
// an error for these cases. Fatal I/O errors (stat / open failure)
// are returned as the second return value.
//
// The first return value is the post-call FileCursor capturing the
// content hash, mtime, and size — operators wiring this into a
// resume loop can pass it back on the next call.
//
// This function is not part of the canonical.Adapter interface and
// is not used by the ingester. It is a stable seam for bench
// tooling; do not call it from production paths.
func ScanFile(ctx context.Context, root, sourceID, name string, prev FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, bool, error) {
	if onError == nil {
		onError = func(error) {}
	}
	return processFile(ctx, root, sourceID, name, prev, out, onError)
}

// ListSnapshots returns the sorted basenames of `*.json.gz` files at
// the root that are not `.tmp-*` orphans. Mirrors the directory walk
// the scanner performs internally. Exported solely for bench tooling
// (see ScanFile). Read-only.
func ListSnapshots(root string) ([]string, error) {
	return listSnapshots(root)
}

// StreamerThresholdBytes is the compressed-size threshold above
// which ScanFile routes through the streaming decoder. Exported so
// bench harnesses can report how many files exceeded the threshold.
const StreamerThresholdBytes = streamerThresholdBytes
