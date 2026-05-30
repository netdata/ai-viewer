package codex

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// flat .json files emit exactly one informational SourceError each (first
// sight) and are then suppressed via the cursor's LegacyJSON map (spec
// §"Legacy"; rule #24 deferral). A modern file with no session_meta on its
// first parseable line is skipped with a SourceError and its offset held at 0
// so a later append retries (rule #24). At EOF a hanging open turn is finalized
// failed/incomplete ONLY when the file mtime is stale ≥ 1 h (rule #23).
// Returns the final cursor.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	disc, err := discoverRollouts(root, onError)
	if err != nil {
		return start, err
	}
	cur := start
	if cur.Files == nil {
		cur = newCursor()
	}

	// Pre-resolve the root ONCE so the per-file containment open does not re-run
	// EvalSymlinks on the root for every file. A resolve failure here is
	// non-fatal: fall back to the unresolved root (the files were already
	// discovered, so the root exists; a degenerate resolve only loses the perf
	// optimisation, not correctness). Mirrors claude_code/scanAll.
	resolvedRoot := root
	if rr, rrErr := filepath.EvalSymlinks(filepath.Clean(root)); rrErr == nil {
		resolvedRoot = rr
	}

	// Emit one informational SourceError per legacy file the first time it is
	// seen, then record it in the cursor so it stays quiet thereafter (R1 / spec
	// §"Legacy"). The content is NOT ingested (Phase-2.5 follow-up).
	cur = reportLegacy(cur, disc.legacy, onError)

	emittedSinceProgress := 0
	lastProgress := time.Now()
	for _, r := range disc.modern {
		if ctx.Err() != nil {
			return cur, ctx.Err()
		}
		fc := cur.fileCursor(r.rel)
		updated, n, rerr := readRollout(ctx, resolvedRoot, r, sourceID, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return cur, rerr
			}
			onError(rerr)
			continue
		}
		cur = cur.withFile(r.rel, updated)
		emittedSinceProgress += n
		if emittedSinceProgress >= progressEveryEvents || time.Since(lastProgress) >= progressEveryDuration {
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return cur, perr
			}
			emittedSinceProgress = 0
			lastProgress = time.Now()
		}
	}

	if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
		return cur, perr
	}
	return cur, nil
}

// fileCursor returns the FileCursor for rel, or a zero cursor when absent.
func (c Cursor) fileCursor(rel string) FileCursor {
	if c.Files == nil {
		return FileCursor{}
	}
	return c.Files[rel]
}

// reportLegacy emits one informational SourceError per not-yet-seen legacy file
// and returns a cursor recording each as seen (suppression). The receiver is
// not mutated. Deterministic order (the caller sorts the basenames).
func reportLegacy(cur Cursor, legacy []string, onError func(error)) Cursor {
	for _, base := range legacy {
		if cur.legacyIngested(base) {
			continue
		}
		onError(fmt.Errorf("codex: legacy flat .json rollout %q is not ingested in v1 (legacy_json_format=false); a Phase-2.5 follow-up may add support", base))
		cur = cur.withLegacyIngested(base)
	}
	return cur
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
func readRollout(ctx context.Context, resolvedRoot string, r rollout, sourceID string, start FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, int, error) {
	// Containment guard on EVERY rollout open (security.md §6): a *.jsonl symlink
	// planted in a watched shard dir after Tail starts would otherwise be opened.
	// The resolved path is containment-checked before it is opened, and the
	// RESOLVED path is what gets opened (not the original symlink). The window
	// between the check and the open is an accepted limitation for this localhost,
	// read-only tool (no O_NOFOLLOW hardening this round; F9). A refused path
	// surfaces a SourceError (the caller logs the returned error) and is skipped.
	resolvedAbs, ok, cerr := withinResolvedRoot(resolvedRoot, r.abs)
	if cerr != nil {
		return start, 0, fmt.Errorf("codex: cannot resolve %s for containment; skipping: %w", r.abs, cerr)
	} else if !ok {
		return start, 0, fmt.Errorf("codex: %s resolves outside the sessions root; skipping (symlink escape)", r.rel)
	}
	f, err := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked RESOLVED path (withinResolvedRoot) from a filtered scan under the configured read-only sessions root
	if err != nil {
		return start, 0, fmt.Errorf("open %s: %w", r.abs, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return start, 0, fmt.Errorf("stat %s: %w", r.abs, err)
	}
	size := info.Size()
	mtimeUs := info.ModTime().UnixMicro()
	cur := start

	// Truncation defense (spec §"Cursor"): a shrunken file is re-scanned from 0;
	// SQL-layer idempotent upserts absorb any re-emitted rows. Codex never
	// truncates, so this means a manual operator delete + recreate.
	if cur.Size > 0 && size < cur.Size {
		onError(fmt.Errorf("rollout %s shrank (size=%d, cursor.size=%d); rescanning from 0", r.rel, size, cur.Size))
		cur = FileCursor{}
	}

	// Rule #24: require a session_meta on the file's first parseable line. Codex
	// always writes session_meta first (recorder.rs), so a first record that is
	// NOT session_meta means the file is corrupt / a pre-write crash. Skip it
	// with a SourceError and hold the offset at 0 so a later append retries. The
	// probe reads from absolute offset 0 (independent of the resume offset) since
	// session_meta is line 1 and may already be below the cursor on a resume.
	hasMeta, probeErr := firstRecordIsSessionMeta(f, size)
	if probeErr != nil {
		return start, 0, fmt.Errorf("probe %s: %w", r.rel, probeErr)
	}
	if !hasMeta {
		onError(fmt.Errorf("rollout %s has no session_meta on its first line; skipping (rule #24, offset held at 0)", r.rel))
		// Hold offset at 0; do not record size so a later append re-probes.
		return start, 0, nil
	}
	if _, serr := f.Seek(0, io.SeekStart); serr != nil {
		return start, 0, fmt.Errorf("seek %s: %w", r.rel, serr)
	}

	emitFrom := cur.Offset
	mapper := newFileMapper(mapperConfig{
		sourceID: sourceID,
		absPath:  r.abs,
		// root is the already-symlink-resolved sessions root this file was
		// opened under (containment-checked above); the mapper uses it to build
		// containment-verified PayloadRef file:// URIs without re-resolving the
		// root per ref (security.md §6).
		root:     resolvedRoot,
		nativeID: nativeIDForRollout(r),
	})
	dedup := newUnknownDedup()

	// Even when the file is fully consumed (offset >= size) we replay from offset
	// 0 with the emit-gate set to size (emit NOTHING) so the per-file turn/op
	// inference counters are rebuilt deterministically — codex has no native
	// turn/op numbers, so a resume produces the SAME Seqs only by replaying the
	// chain from the start (acceptance #6). emitFrom is clamped to <= size.
	if emitFrom > size {
		emitFrom = size
	}

	res, perr := streamLines(ctx, f, emitFrom, r.rel, mapper, dedup, out, onError)
	if perr != nil {
		// Record the offset reached even on cancellation so a follow-up resumes
		// from completed work (only fully-consumed lines advance the offset).
		cur.Offset = res.advanced
		return cur, res.emitted, perr
	}
	cur.Offset = res.advanced
	cur.Size = size
	cur.MtimeUs = mtimeUs
	if mapper.lastTsUs > 0 {
		cur.LastTsUs = mapper.lastTsUs
	}

	// EOF-finalize (rule #23, spec edge #3, F1): when the file is FULLY read, ask
	// the mapper to finalize a hanging open turn. The mapper owns the open-turn
	// decision and splits on format + staleness (mapper_finalize.go):
	//   - OLD-format open turn (turn_context-only): closed COMPLETED at EOF
	//     regardless of staleness (spec edge #3 "close at EOF"); no
	//     SessionFinalized (SOW C#3).
	//   - NEW-format open turn: closed failed/incomplete + SessionFinalized ONLY
	//     when the mtime is stale ≥ 1 h (rule #23); a fresh file leaves it open.
	//   - clean end / no open turn: nothing (stays running, SOW C#3).
	// This is called UNCONDITIONALLY at full-read EOF (not only when stale) and
	// passed the stale bool, so the OLD-format completed-close fires on fresh files
	// too (F1). The synthetic end timestamp is the file mtime in micros.
	fullyRead := res.advanced >= size
	if fullyRead {
		stale := time.Since(info.ModTime()) >= staleAfter
		for _, ev := range mapper.finalizeAtEOF(stale, mtimeUs) {
			select {
			case <-ctx.Done():
				return cur, res.emitted, ctx.Err()
			case out <- ev:
				res.emitted++
			}
		}
	}
	return cur, res.emitted, nil
}

// firstRecordIsSessionMeta reports whether the file's first non-blank,
// parseable line is a session_meta record (rule #24). It reads from absolute
// offset 0 (the caller seeks back afterwards). A blank or known-skip line is
// passed over; the first line that parses to a concrete record decides. A file
// with no parseable record at all (only blanks, or a single oversized line, or
// nothing but parse errors) returns false so an empty/corrupt file is treated
// as "no session_meta" and held at offset 0. size bounds the oversized-line
// probe so a hostile first line cannot force an unbounded read.
func firstRecordIsSessionMeta(f *os.File, size int64) (bool, error) {
	if size == 0 {
		return false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	br := bufio.NewReaderSize(f, streamReaderSize)
	for {
		line, _, err := readOneLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			if errors.Is(err, errLineTooLong) {
				// An oversized first line is not a session_meta the adapter can use;
				// keep probing past it for a later session_meta (none expected, but
				// be forgiving rather than abort the whole file).
				continue
			}
			return false, err
		}
		if len(line) == 0 {
			return false, nil
		}
		rec, skip, perr := parseLine(line[:len(line)-1])
		if perr != nil {
			// A malformed first line is not a usable session_meta; the file is
			// corrupt for rule-#24 purposes.
			return false, nil
		}
		if skip {
			continue
		}
		return rec.Type() == recSessionMeta, nil
	}
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
