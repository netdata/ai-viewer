package aiagent_v2

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// readSnapshotStreaming handles files whose compressed size exceeds
// streamerThresholdBytes. The strategy is intentionally simple: tee
// the decompressed stream through a sha256 hasher into a streaming
// JSON decoder. The decoder still builds the full opTree in memory,
// but it allocates incrementally rather than via a single
// `io.ReadAll`, which avoids the worst-case 2× peak observed when a
// 1+ GB decompressed buffer is held alongside the parsed tree.
//
// A future optimization could walk the JSON token-by-token and emit
// events incrementally per turn / step, eliminating the in-memory
// tree entirely. The current implementation prioritises correctness
// parity with the whole-tree path (verified by TestStreamer_*); the
// SAX-walk replacement lands when profiling shows the bounded
// allocation is still too high.
//
// Verified equivalence: TestStreamer_AgreesWithNonStreaming builds
// one fixture, runs both paths against it, and asserts byte-identical
// event slices. Any divergence breaks the test before merge.
func readSnapshotStreaming(ctx context.Context, path, sourceID, originID, sessionsRoot, filename string, onError func(error)) ([]canonical.Event, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	f, err := os.Open(path) // #nosec G304 -- path constrained by listSnapshots
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		onError(fmt.Errorf("aiagent_v2: streamer gzip header %s: %w", path, err))
		return nil, "", nil
	}
	defer func() { _ = gz.Close() }()

	h := sha256.New()
	tee := io.TeeReader(gz, h)

	snap, perr := parseSnapshotStream(tee)
	if perr != nil {
		// Drain the rest of the stream so the hash covers the full
		// payload; matches the whole-tree path's content-hash contract.
		if drainErr := drainToHash(tee, onError, path); drainErr != nil {
			return nil, finalizeHash(h), nil
		}
		onError(fmt.Errorf("aiagent_v2: streamer parse %s: %w", path, perr))
		return nil, finalizeHash(h), nil
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		onError(fmt.Errorf("aiagent_v2: streamer drain %s: %w", path, err))
	}
	hashHex := finalizeHash(h)
	events := mapSnapshot(snap, sourceID, originID, sessionsRoot, filename, onError)
	return events, hashHex, nil
}

// drainToHash discards any trailing bytes after a streaming parse
// error so the cursor's content hash still represents the complete
// decompressed payload. Surfaces an onError when the drain itself
// fails; returns the error so the caller can treat it as terminal.
func drainToHash(r io.Reader, onError func(error), path string) error {
	if _, err := io.Copy(io.Discard, r); err != nil {
		onError(fmt.Errorf("aiagent_v2: streamer drain after parse error %s: %w", path, err))
		return err
	}
	return nil
}

// finalizeHash converts an in-flight sha256 state into its hex digest.
// Extracted so the two error paths (success + drain-after-failure)
// share the formatting.
func finalizeHash(h hash.Hash) string {
	return hex.EncodeToString(h.Sum(nil))
}
