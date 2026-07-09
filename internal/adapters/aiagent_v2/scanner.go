package aiagent_v2

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// snapshotExt is the required file extension for v2 snapshots.
const snapshotExt = ".json.gz"

// tmpSuffixPrefix is the substring producers use for the temp file
// during atomic-rename. Any filename containing it is skipped entirely.
const tmpSuffixPrefix = ".tmp-"

// streamerThresholdBytes is the compressed-size threshold above which
// the scanner routes the file to the streaming decoder instead of the
// whole-tree parser. Set to 50 MiB per
// `adapter-aiagent-v2.md` §Performance Considerations. Files under
// this threshold are decoded fully in memory; files above are walked
// via a streaming JSON decoder so peak heap stays bounded.
const streamerThresholdBytes int64 = 50 * 1024 * 1024

// progressEveryDuration provides a wall-clock bound for SourceProgressEvent
// checkpoints. Whichever threshold trips first wins.
const progressEveryDuration = 5 * time.Second

// scanProgressEveryFiles bounds cursor-checkpoint emission during backfill.
// Larger than the canonical 1000-files-per-checkpoint guidance because
// aiagent_v2's cursor is ~9 KB JSON for 482k sessions; emitting it every
// 1000 files allocates ~4 MB per scan (SOW-0094). The final cursor at
// scanAll's return still goes out, so the on-disk checkpoint is always
// persisted exactly once per scan.
const scanProgressEveryFiles = 50_000

// scanAll walks the root non-recursively, processes every `.json.gz`
// not in cursor's known-content set, and emits canonical events plus
// periodic SourceProgressEvents. Returns the final cursor.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	files, err := listSnapshots(root)
	if err != nil {
		return start, err
	}
	cur := start
	if cur.Files == nil {
		cur = newCursor()
	}
	processed := 0
	lastProgress := time.Now()

	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		updated, changed, perr := processFile(ctx, root, sourceID, name, cur.fileCursor(name), out, onError)
		if perr != nil {
			if errors.Is(perr, context.Canceled) || errors.Is(perr, context.DeadlineExceeded) {
				return cur, perr
			}
			onError(perr)
			continue
		}
		if changed {
			cur.withFile(name, updated)
		}
		processed++
		if processed >= scanProgressEveryFiles || time.Since(lastProgress) >= progressEveryDuration {
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return cur, perr
			}
			processed = 0
			lastProgress = time.Now()
		}
	}
	if err := emitProgress(ctx, sourceID, cur, out); err != nil {
		return cur, err
	}
	return cur, nil
}

// processFile handles one snapshot file end to end: stat, dedup,
// decompress, parse, map, emit. Returns the new FileCursor and a
// "changed" flag so the caller knows whether to persist the cursor
// update.
//
// Per `adapter-aiagent-v2.md` skip logic:
//  1. Stat the file. If size == 0, emit SourceErrorEvent (WRN) and
//     record the mtime so it is not retried until modified.
//  2. If cursor's mtime+size match → skip entirely.
//  3. Decompress, hash the bytes. If hash matches cursor → update
//     mtime+size only, do not re-emit.
//  4. Otherwise: parse, map, emit, persist new cursor.
func processFile(ctx context.Context, root, sourceID, name string, fc FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, bool, error) {
	full := filepath.Join(root, name)
	info, err := os.Stat(full)
	if err != nil {
		return fc, false, fmt.Errorf("stat %s: %w", full, err)
	}
	mtime := info.ModTime().UnixNano()
	size := info.Size()

	if size == 0 {
		// Zero-byte file: emit a warning, advance cursor so we do not
		// keep re-warning on every scan.
		if perr := emitZeroByteWarning(ctx, sourceID, full, out); perr != nil {
			return fc, false, perr
		}
		return FileCursor{LastMtime: mtime, LastSize: 0, ContentHash: fc.ContentHash}, true, nil
	}

	// Step 2: stat-only short circuit. If both mtime and size are
	// identical to the cursor, the file is byte-for-byte the same — no
	// need to decompress.
	if fc.LastMtime == mtime && fc.LastSize == size && fc.ContentHash != "" {
		return fc, false, nil
	}

	// Step 3+: decompress the file. We use the streaming decoder for
	// any file whose compressed size exceeds streamerThresholdBytes so
	// peak memory stays bounded under the operator's max ~151 MB
	// compressed outliers.
	useStreamer := size > streamerThresholdBytes
	originID := stripSnapshotExt(name)
	var (
		events       []canonical.Event
		contentHash  string
		emitFatalErr error
	)
	if useStreamer {
		events, contentHash, emitFatalErr = readSnapshotStreaming(ctx, full, sourceID, originID, root, name, onError)
	} else {
		events, contentHash, emitFatalErr = readSnapshotWhole(full, sourceID, originID, root, name, onError)
	}
	if emitFatalErr != nil {
		return fc, false, fmt.Errorf("read %s: %w", full, emitFatalErr)
	}
	// Content-hash short-circuit: mtime changed but the bytes didn't
	// (filesystem touch). Skip re-emission; just refresh mtime+size.
	if fc.ContentHash != "" && fc.ContentHash == contentHash {
		return FileCursor{ContentHash: contentHash, LastMtime: mtime, LastSize: size}, true, nil
	}
	for _, ev := range events {
		select {
		case <-ctx.Done():
			return fc, false, ctx.Err()
		case out <- ev:
		}
	}
	return FileCursor{ContentHash: contentHash, LastMtime: mtime, LastSize: size}, true, nil
}

// readSnapshotWhole decompresses the file into memory, hashes the
// decompressed bytes, and parses + maps in one pass. Returns the
// canonical events and content hash. Per-record parse errors are
// surfaced via onError; an envelope-level parse failure becomes a
// SourceErrorEvent and the function returns (nil events, hash, nil)
// so the caller still persists the cursor.
func readSnapshotWhole(path, sourceID, originID, sessionsRoot, filename string, onError func(error)) ([]canonical.Event, string, error) {
	f, err := os.Open(path) // #nosec G304 -- path constrained by listSnapshots
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		// A corrupt gzip header is non-fatal; surface as a parse error
		// so the rest of the directory keeps processing.
		onError(fmt.Errorf("aiagent_v2: gzip header %s: %w", path, err))
		return nil, "", nil
	}
	defer func() { _ = gz.Close() }()
	data, err := io.ReadAll(gz)
	if err != nil {
		onError(fmt.Errorf("aiagent_v2: decompress %s: %w", path, err))
		return nil, "", nil
	}
	hash := sha256Hex(data)
	snap, err := parseSnapshot(data)
	if err != nil {
		onError(fmt.Errorf("aiagent_v2: parse %s: %w", path, err))
		return nil, hash, nil
	}
	events := mapSnapshot(snap, sourceID, originID, sessionsRoot, filename, onError)
	return events, hash, nil
}

// emitZeroByteWarning emits a SourceErrorEvent with severity WRN for
// a zero-byte snapshot file. Per `adapter-aiagent-v2.md` §Edge Cases
// item 1, these are producer crash artifacts that should be surfaced
// once and then skipped via the cursor mtime entry.
func emitZeroByteWarning(ctx context.Context, sourceID, path string, out chan<- canonical.Event) error {
	ev := canonical.SourceErrorEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        time.Now().UnixMicro(),
		},
		File:    path,
		Offset:  -1,
		Message: "zero-byte snapshot file (producer crash artifact); skipping",
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- ev:
		return nil
	}
}

// listSnapshots returns the sorted basenames of `*.json.gz` files at
// the root that are not `.tmp-*` orphans. Stable order keeps replay
// deterministic.
func listSnapshots(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			// v3 subdirectories live alongside v2 root files; skip them
			// (the v3 adapter owns them).
			continue
		}
		name := e.Name()
		if !isSnapshotName(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// isSnapshotName returns true for filenames that are eligible v2
// snapshots: end in `.json.gz`, do not contain the `.tmp-` infix, and
// are not hidden / dotfiles.
func isSnapshotName(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	if strings.Contains(name, tmpSuffixPrefix) {
		return false
	}
	return strings.HasSuffix(name, snapshotExt)
}

// stripSnapshotExt returns the originId portion of a snapshot
// filename (basename minus `.json.gz`).
func stripSnapshotExt(name string) string {
	return strings.TrimSuffix(name, snapshotExt)
}

// sha256Hex returns the lowercase hex digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// emitProgress publishes a SourceProgressEvent with the current
// cursor. Mirrors the v3 adapter's helper.
func emitProgress(ctx context.Context, sourceID string, cur Cursor, out chan<- canonical.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ev := canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        time.Now().UnixMicro(),
		},
		Cursor: cur.String(),
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- ev:
		return nil
	}
}
