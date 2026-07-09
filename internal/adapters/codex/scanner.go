package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// staleAfter is the file-mtime age beyond which a hanging open turn at EOF is
// synthetically finalized failed/incomplete (spec rule #23, SOW C#3: "≥ 1 h").
// A fresher file keeps its open turn running for the next append.
const staleAfter = time.Hour

// progressEveryEvents bounds how frequently SourceProgress is emitted by record
// count (spec §"Watch Strategy"; mirrors claude_code's progress cadence).
const progressEveryEvents = 200

// progressEveryDuration bounds SourceProgress emission by wall-clock.
const progressEveryDuration = 5 * time.Second

// scanAll walks the sessions root and reads every modern rollout from its
// cursor offset to EOF, emitting events and periodic SourceProgress. Legacy
// flat .json files are static historical snapshots; scan ingests each valid
// file once and records it in the cursor's LegacyJSON map. A modern file with no session_meta on its
// first parseable line is skipped with a SourceError and its offset held at 0
// so a later append retries (rule #24). At EOF a hanging open turn is finalized
// failed/incomplete ONLY when the file mtime is stale ≥ 1 h (rule #23).
// Returns the final cursor.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	disc, err := discoverRollouts(root, onError)
	if err != nil {
		return start, err
	}
	cur := normalizeScanCursor(start)
	resolvedRoot := resolveScanRoot(root)
	cur, err = scanLegacyRollouts(ctx, resolvedRoot, sourceID, disc.legacy, cur, out, onError)
	if err != nil {
		return cur, err
	}
	cur, err = scanModernRollouts(ctx, resolvedRoot, sourceID, disc.modern, cur, out, onError)
	if err != nil {
		return cur, err
	}
	if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
		return cur, perr
	}
	return cur, nil
}

func normalizeScanCursor(start Cursor) Cursor {
	if start.Files == nil {
		return newCursor()
	}
	return start
}

func resolveScanRoot(root string) string {
	if rr, err := filepath.EvalSymlinks(filepath.Clean(root)); err == nil {
		return rr
	}
	return root
}

func scanModernRollouts(ctx context.Context, resolvedRoot, sourceID string, rollouts []rollout, cur Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	progress := newScanProgress()
	seenIDs := make(map[string]string) // SOW-0022: cross-file duplicate-id tracking
	for _, r := range rollouts {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		fc := cur.fileCursor(r.rel)
		skip, skipErr := skipUnchangedEOFFinalizedRollout(r, fc)
		if skipErr != nil {
			onError(skipErr)
			continue
		}
		if skip {
			cur.withFile(r.rel, fc)
			continue
		}
		updated, n, err := readRollout(ctx, resolvedRoot, r, sourceID, fc, out, onError, seenIDs)
		if err != nil {
			if isContextStop(err) {
				return cur, err
			}
			onError(err)
			continue
		}
		cur.withFile(r.rel, updated)
		if err := progress.record(ctx, sourceID, cur, n, out); err != nil {
			return cur, err
		}
	}
	return cur, nil
}

func isContextStop(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type scanProgress struct {
	emittedSinceProgress int
	lastProgress         time.Time
}

func newScanProgress() scanProgress {
	return scanProgress{lastProgress: time.Now()}
}

func (p *scanProgress) record(ctx context.Context, sourceID string, cur Cursor, emitted int, out chan<- canonical.Event) error {
	p.emittedSinceProgress += emitted
	if p.emittedSinceProgress < progressEveryEvents && time.Since(p.lastProgress) < progressEveryDuration {
		return nil
	}
	if err := emitProgress(ctx, sourceID, cur, out); err != nil {
		return err
	}
	p.emittedSinceProgress = 0
	p.lastProgress = time.Now()
	return nil
}

// fileCursor returns the FileCursor for rel, or a zero cursor when absent.
func (c Cursor) fileCursor(rel string) FileCursor {
	if c.Files == nil {
		return FileCursor{}
	}
	return c.Files[rel]
}

// readRollout parses one modern rollout from its cursor offset to EOF, emits
// canonical events, and returns the updated FileCursor and emitted-event count.
// Partial trailing lines are held back (offset advances only past complete
// lines, spec "Atomicity"). Truncation (size < cursor.size) re-scans from 0
// with a SourceError (spec §"Cursor" restart logic). A file with no
// session_meta on its first parseable line is skipped with a SourceError and
// its offset held at 0 (rule #24). At EOF a hanging open turn is finalized
// failed/incomplete ONLY when the mtime is stale ≥ 1 h (rule #23); a fresh file
// leaves the turn open. resolvedRoot is the symlink-resolved sessions root,
// threaded into the containment open.
func readRollout(ctx context.Context, resolvedRoot string, r rollout, sourceID string, start FileCursor, out chan<- canonical.Event, onError func(error), seenIDs map[string]string) (FileCursor, int, error) {
	opened, err := openRolloutForRead(resolvedRoot, r)
	if err != nil {
		return start, 0, err
	}
	defer func() { _ = opened.file.Close() }()

	cur := resetTruncatedRolloutCursor(r, opened.size, start, onError)
	hasMeta, err := requireFirstSessionMeta(opened.file, r, opened.size, onError)
	if err != nil {
		return start, 0, err
	}
	if !hasMeta {
		return start, 0, nil
	}

	mapper := newRolloutMapper(resolvedRoot, sourceID, r)
	// Duplicate-id disambiguation (SOW-0022, edge #14): if this rollout's
	// filename-derived id was already seen on a different file, append
	// ":<basename>" to the mapper's NativeID so the two same-id rollouts
	// become two distinct canonical sessions. The mapper overrides this from
	// session_meta when the meta is read, preserving the suffix.
	fileID := mapper.nativeID // the filename-derived id (pre-meta anchor)
	basename := strings.TrimSuffix(filepath.Base(r.abs), modernExt)
	if fileID != "" && seenIDs != nil {
		if _, dup := seenIDs[fileID]; dup {
			mapper.disambiguateSuffix = basename
			onError(fmt.Errorf("codex: duplicate session id %q in %s — disambiguating as %s:%s", fileID, r.rel, fileID, basename))
		} else {
			seenIDs[fileID] = r.rel
		}
	}
	dedup := newUnknownDedup()
	res, perr := streamLines(ctx, opened.file, clampedEmitFrom(cur.Offset, opened.size), r.rel, mapper, dedup, out, onError)
	if perr != nil {
		cur.Offset = res.advanced
		return cur, res.emitted, perr
	}
	cur = updateCursorAfterStream(cur, opened, mapper, res)
	emittedContent := res.emitted > 0
	return finalizeRolloutAtEOF(ctx, opened, cur, mapper, res, emittedContent, out)
}

type openedRollout struct {
	file    *os.File
	info    os.FileInfo
	size    int64
	mtimeUs int64
}

func openRolloutForRead(resolvedRoot string, r rollout) (*openedRollout, error) {
	resolvedAbs, ok, err := withinResolvedRoot(resolvedRoot, r.abs)
	if err != nil {
		return nil, fmt.Errorf("codex: cannot resolve %s for containment; skipping: %w", r.abs, err)
	}
	if !ok {
		return nil, fmt.Errorf("codex: %s resolves outside the sessions root; skipping (symlink escape)", r.rel)
	}
	f, err := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked resolved path from a filtered scan under the configured read-only sessions root
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", r.abs, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat %s: %w", r.abs, err)
	}
	return &openedRollout{file: f, info: info, size: info.Size(), mtimeUs: info.ModTime().UnixMicro()}, nil
}

func resetTruncatedRolloutCursor(r rollout, size int64, cur FileCursor, onError func(error)) FileCursor {
	if cur.Size > 0 && size < cur.Size {
		onError(fmt.Errorf("rollout %s shrank (size=%d, cursor.size=%d); rescanning from 0", r.rel, size, cur.Size))
		return FileCursor{}
	}
	return cur
}

func requireFirstSessionMeta(f *os.File, r rollout, size int64, onError func(error)) (bool, error) {
	hasMeta, err := firstRecordIsSessionMeta(f, size)
	if err != nil {
		return false, fmt.Errorf("probe %s: %w", r.rel, err)
	}
	if !hasMeta {
		onError(fmt.Errorf("rollout %s has no session_meta on its first line; skipping (rule #24, offset held at 0)", r.rel))
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("seek %s: %w", r.rel, err)
	}
	return true, nil
}

func newRolloutMapper(resolvedRoot, sourceID string, r rollout) *fileMapper {
	return newFileMapper(mapperConfig{
		sourceID: sourceID,
		absPath:  r.abs,
		root:     resolvedRoot,
		nativeID: nativeIDForRollout(r),
	})
}

func clampedEmitFrom(offset, size int64) int64 {
	if offset > size {
		return size
	}
	return offset
}

func updateCursorAfterStream(cur FileCursor, opened *openedRollout, mapper *fileMapper, res streamResult) FileCursor {
	cur.Offset = res.advanced
	cur.Size = opened.size
	cur.MtimeUs = opened.mtimeUs
	if mapper.lastTsUs > 0 {
		cur.LastTsUs = mapper.lastTsUs
	}
	return cur
}

type eofFinalizeMode uint8

const (
	eofFinalizeNone eofFinalizeMode = iota
	eofFinalizeSuppress
	eofFinalizeEmit
)

func finalizeRolloutAtEOF(ctx context.Context, opened *openedRollout, cur FileCursor, mapper *fileMapper, res streamResult, emittedContent bool, out chan<- canonical.Event) (FileCursor, int, error) {
	switch eofMode(cur, opened.size, res.advanced, emittedContent) {
	case eofFinalizeNone:
		return cur, res.emitted, nil
	case eofFinalizeSuppress:
		return advanceSuppressedEOFMarker(cur, opened.size, emittedContent), res.emitted, nil
	default:
		emitted, err := emitEOFFinalizeEvents(ctx, opened, mapper, out)
		res.emitted += emitted
		if err != nil {
			return cur, res.emitted, err
		}
		if mapper.eofFinalized {
			cur.EOFFinalizedSize = opened.size
		}
		return cur, res.emitted, nil
	}
}

func eofMode(cur FileCursor, size, advanced int64, emittedContent bool) eofFinalizeMode {
	if advanced < size {
		return eofFinalizeNone
	}
	if eofAlreadyFinalized(cur, size) || eofGrewWithoutContent(cur, size, emittedContent) {
		return eofFinalizeSuppress
	}
	return eofFinalizeEmit
}

func eofAlreadyFinalized(cur FileCursor, size int64) bool {
	return cur.EOFFinalizedSize > 0 && cur.EOFFinalizedSize == size
}

func eofGrewWithoutContent(cur FileCursor, size int64, emittedContent bool) bool {
	return cur.EOFFinalizedSize > 0 && size > cur.EOFFinalizedSize && !emittedContent
}

func advanceSuppressedEOFMarker(cur FileCursor, size int64, emittedContent bool) FileCursor {
	if eofGrewWithoutContent(cur, size, emittedContent) {
		cur.EOFFinalizedSize = size
	}
	return cur
}

func emitEOFFinalizeEvents(ctx context.Context, opened *openedRollout, mapper *fileMapper, out chan<- canonical.Event) (int, error) {
	stale := time.Since(opened.info.ModTime()) >= staleAfter
	emitted := 0
	for _, ev := range mapper.finalizeAtEOF(stale, opened.mtimeUs) {
		select {
		case <-ctx.Done():
			return emitted, ctx.Err()
		case out <- ev:
			emitted++
		}
	}
	return emitted, nil
}

// firstRecordIsSessionMeta reports whether the file's first non-blank,
// parseable line is a session_meta record (rule #24). It reads from absolute
// offset 0 (the caller seeks back afterwards). A blank or known-skip line is
// passed over; the first line that parses to a concrete record decides. A file
// with no parseable record at all (only blanks, or a single oversized line, or
// nothing but parse errors) returns false so an empty/corrupt file is treated as
// "no session_meta" and held at offset 0. size only short-circuits empty files;
// readOneLine bounds each allocation while draining oversized lines to newline or
// EOF, so a hostile first line cannot force unbounded memory growth.
func firstRecordIsSessionMeta(f *os.File, size int64) (bool, error) {
	if size == 0 {
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	return scanFirstRecordForSessionMeta(bufio.NewReaderSize(f, streamReaderSize))
}

func scanFirstRecordForSessionMeta(br *bufio.Reader) (bool, error) {
	for {
		line, err := nextProbeLine(br)
		if err != nil {
			return false, err
		}
		if line == nil {
			return false, nil
		}
		isMeta, decided := probeLineSessionMeta(line)
		if decided {
			return isMeta, nil
		}
	}
}

func nextProbeLine(br *bufio.Reader) ([]byte, error) {
	for {
		line, _, err := readOneLine(br)
		if err == nil {
			if len(line) == 0 {
				return nil, nil
			}
			return line, nil
		}
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if !errors.Is(err, errLineTooLong) {
			return nil, err
		}
	}
}

func probeLineSessionMeta(line []byte) (bool, bool) {
	rec, skip, err := parseLine(line[:len(line)-1])
	if err != nil {
		// Rule #24 treats a malformed first concrete line as "not session_meta"
		// so the scan holds offset 0 and a later append can repair the file.
		return false, true
	}
	if skip {
		return false, false
	}
	return rec.Type() == recSessionMeta, true
}

// emitProgress publishes a SourceProgressEvent with the current cursor. Mirrors
// claude_code.
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
