package aiagent_v3

import (
	"bufio"
	"context"
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

// sessionDir is the subdirectory under the configured root that holds
// per-session ledger files. Spec §2.1.
const sessionDir = "session"

// ledgerExt is the required file extension for v3 ledgers. Spec §2.1.
const ledgerExt = ".jsonl"

// scanBufferMax is the maximum bufio.Scanner line length. v3 ledger
// lines can reach ~248 KB in real data (spec §6.5); 4 MB provides ample
// headroom while bounding pathological allocations.
const scanBufferMax = 4 * 1024 * 1024

// progressEveryEvents bounds how frequently SourceProgress checkpoints
// are emitted by record count. Spec §6.3 explicitly discourages one
// progress event per line.
const progressEveryEvents = 200

// progressEveryDuration bounds how frequently SourceProgress checkpoints
// are emitted by wall-clock. The looser of the two thresholds wins.
const progressEveryDuration = 5 * time.Second

// readFile parses one ledger file from the given starting cursor, emits
// canonical events to `out`, and returns the updated FileCursor and
// number of events emitted. Per-record parse errors are forwarded via
// onError and skipped; fatal I/O errors are returned wrapped.
//
// Holds back any trailing partial line (last byte != '\n') by advancing
// the cursor only past the last complete line — per spec §6.5.
func readFile(ctx context.Context, path, sourceID, sessionRoot string, start FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, int, error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from filtered directory scan under configured root
	if err != nil {
		return start, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return start, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	size := info.Size()
	cur := start

	// Truncation defense (spec §7.2): if size < cursor.size, the file
	// was shortened. Reset to full-scan; the ingester's SQL-layer
	// idempotent upserts (natural-identity keys) absorb any re-emitted
	// rows — there is no SourceSeq dedup gate. See ingester.md
	// §Dedup and Idempotency.
	if cur.Size > 0 && size < cur.Size {
		onError(fmt.Errorf("ledger %s shrank (size=%d, cursor.size=%d); rescanning from 0", path, size, cur.Size))
		cur = FileCursor{}
	}

	if cur.Offset >= size {
		// Nothing new; just refresh size in case the cursor was stale.
		cur.Size = size
		return cur, 0, nil
	}

	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return cur, 0, fmt.Errorf("seek %s @%d: %w", path, cur.Offset, err)
	}

	emitted, advanced, perr := streamLines(ctx, f, cur.Offset, size, path, sourceID, sessionRoot, &cur, out, onError)
	if perr != nil {
		return cur, emitted, perr
	}
	cur.Offset = advanced
	cur.Size = size
	return cur, emitted, nil
}

// streamLines reads from r as a sequence of '\n'-terminated JSON
// records, advancing offset only past lines confirmed to end with '\n'.
// Returns the number of canonical events emitted and the absolute file
// offset just past the last complete line consumed.
//
// fileSize is the file's size at the moment readFile took its snapshot;
// it is used solely so an oversized line (errLineTooLong) can skip past
// the offending record by jumping to EOF — otherwise the next Tail pass
// would re-scan the same oversized line and re-emit the same warning.
func streamLines(ctx context.Context, r io.Reader, startOffset, fileSize int64, path, sourceID, sessionRoot string, cur *FileCursor, out chan<- canonical.Event, onError func(error)) (int, int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	emitted := 0
	off := startOffset
	for {
		if err := ctx.Err(); err != nil {
			return emitted, off, err
		}
		line, err := readOneLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return emitted, off, nil
			}
			if errors.Is(err, errLineTooLong) {
				onError(fmt.Errorf("ledger %s @%d: line exceeds %d bytes; skipping", path, off, scanBufferMax))
				// Skip the oversized line entirely: jump to the current
				// file size so the next read does not re-scan the same
				// record and re-emit the same warning. Any bytes appended
				// after this snapshot are picked up on the next Tail pass
				// — the cost is dropping at most one trailing complete
				// line that landed between snapshot time and now, which
				// the snapshot did not see anyway.
				if fileSize > off {
					off = fileSize
				}
				return emitted, off, nil
			}
			return emitted, off, fmt.Errorf("read %s @%d: %w", path, off, err)
		}
		if len(line) == 0 {
			// readOneLine returns empty slice only on EOF; defensive.
			return emitted, off, nil
		}
		// line includes the trailing '\n'. Advance offset and parse.
		recBytes := line[:len(line)-1]
		off += int64(len(line))

		rec, skip, perr := parseLine(recBytes)
		if perr != nil {
			onError(fmt.Errorf("ledger %s @%d: %w", path, off-int64(len(line)), perr))
			continue
		}
		if skip {
			continue
		}
		events, mErr := mapRecord(rec, sourceID, sessionRoot)
		if mErr != nil {
			onError(fmt.Errorf("ledger %s @%d: map: %w", path, off-int64(len(line)), mErr))
			continue
		}
		if rec.Common.Seq > cur.LastSeq {
			cur.LastSeq = rec.Common.Seq
		}
		if recTsUs, err := parseTsToMicros(rec.Common.Ts); err == nil && recTsUs > cur.LastTsUs {
			cur.LastTsUs = recTsUs
		}
		if rec.SessionSummary != nil || rec.SessionError != nil {
			cur.SeenSummary = true
		}
		for _, ev := range events {
			select {
			case <-ctx.Done():
				return emitted, off, ctx.Err()
			case out <- ev:
				emitted++
			}
		}
	}
}

// errLineTooLong signals that a single ledger line exceeded
// scanBufferMax. The caller surfaces this via opts.OnError and skips
// the remainder of the file (the offset can't safely advance because we
// don't know where the line ends).
var errLineTooLong = errors.New("aiagent_v3: line exceeds scan buffer")

// readOneLine reads one '\n'-terminated record from br. Returns the
// line WITH the trailing '\n' (so callers can advance offset by len()).
// Returns io.EOF when no complete line is available — and explicitly
// does NOT return a partial trailing line, implementing the spec §6.5
// hold-back invariant.
func readOneLine(br *bufio.Reader) ([]byte, error) {
	buf := make([]byte, 0, 256)
	for {
		chunk, err := br.ReadSlice('\n')
		if err == nil {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				return nil, errLineTooLong
			}
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				// Drain rest of the line so the next read starts cleanly.
				if drainErr := drainToNewline(br); drainErr != nil {
					return nil, drainErr
				}
				return nil, errLineTooLong
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			// Partial line at EOF: do not return it. The caller will
			// re-enter on the next Tail/Scan pass after more bytes
			// arrive (or surface a SourceError after the idle window).
			return nil, io.EOF
		}
		return nil, err
	}
}

func drainToNewline(br *bufio.Reader) error {
	for {
		_, err := br.ReadSlice('\n')
		if err == nil {
			return nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return err
	}
}

// scanAll walks <root>/session/ and reads every .jsonl file from its
// cursor offset to EOF, emitting events and periodic SourceProgress.
// Returns the final cursor.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	files, err := listLedgers(root)
	if err != nil {
		return start, err
	}
	cur := start
	if cur.Files == nil {
		cur = newCursor()
	}
	emittedSinceProgress := 0
	lastProgress := time.Now()

	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		full := filepath.Join(root, sessionDir, name)
		fc := cur.fileCursor(name)
		updated, n, rerr := readFile(ctx, full, sourceID, root, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return cur, rerr
			}
			onError(rerr)
			continue
		}
		cur = cur.withFile(name, updated)
		emittedSinceProgress += n
		if emittedSinceProgress >= progressEveryEvents || time.Since(lastProgress) >= progressEveryDuration {
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return cur, perr
			}
			emittedSinceProgress = 0
			lastProgress = time.Now()
		}
	}
	// Final progress at end of Scan so the ingester always sees the latest
	// cursor even when no per-batch threshold was hit.
	if err := emitProgress(ctx, sourceID, cur, out); err != nil {
		return cur, err
	}
	return cur, nil
}

// listLedgers returns the sorted basenames of `.jsonl` files under
// <root>/session/. Stable order keeps replay deterministic.
func listLedgers(root string) ([]string, error) {
	dir := filepath.Join(root, sessionDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ledgerExt) {
			continue
		}
		// Spec §2.1: ignore in-flight .tmp files; ledgers never have a
		// .tmp suffix but defend against future producer changes.
		if strings.Contains(name, ".tmp-") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// fileSize stats <root>/session/<name> and returns its current byte size.
// Returns 0 when the file does not exist (e.g. the caller's snapshot path
// raced an unlink); callers treat that as "no progress to record".
func fileSize(root, name string) (int64, error) {
	info, err := os.Stat(filepath.Join(root, sessionDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// emitProgress publishes a SourceProgressEvent with the current cursor.
// The ts is the wall-clock at emission so the ingester can correlate
// progress events with real time. Bails out early on cancelled ctx so
// shutdown paths never publish stale checkpoints.
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
