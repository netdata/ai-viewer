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

	"github.com/netdata/ai-viewer/internal/canonical"
)

// scanBufferMax bounds a single rollout line. Real codex lines can be large
// (base64 encrypted_content reasoning blocks, big tool I/O); live SOW-0097
// evidence found valid lines up to about 14 MiB, so the cap leaves headroom
// while bounding pathological allocations.
const scanBufferMax = 16 * 1024 * 1024

// streamReader is the per-line reader buffer size; matches claude_code.
const streamReaderSize = 64 * 1024

// errLineTooLong signals that a single rollout line exceeded scanBufferMax. The
// caller surfaces it via onError, drains just that oversized line up to its
// terminating newline, and CONTINUES reading the rest of the file (spec
// adapter-codex.md "Atomicity" partial-line handling; edge #7) — it does NOT
// skip to EOF. Reused verbatim from claude_code.
var errLineTooLong = errors.New("codex: line exceeds scan buffer")

// streamResult bundles what one rollout-file stream produced: the emitted-event
// count and the absolute offset just past the last complete line consumed (the
// durable resume key). The caller derives EOF-fullness from advanced >= size
// and the rule-#24 session_meta check from a separate first-record probe, so no
// further fields are carried here.
type streamResult struct {
	emitted  int
	advanced int64
}

// streamLines reads '\n'-terminated JSON records from r (positioned at offset
// 0), mapping each via the file's mapper to rebuild turn/op inference state
// deterministically. Events are emitted ONLY for records whose line begins at
// or after emitFrom, so a resume replays prior bytes to rebuild counters but
// emits nothing already seen (zero dup, zero gap — acceptance #6). Returns the
// emitted-event count and the absolute offset just past the last complete line
// consumed. A partial trailing line (no final '\n') is held back so the offset
// only ever advances past complete lines (spec "Atomicity").
//
// dedup is the per-file unknown-variant seen-set (rule #2: exactly one
// SourceError per distinct unknown top-level `type` OR nested `payload.type`
// per session). It lives in the scanner (not the mapper) because the parser
// returns the variant via a typed error before the mapper is reached; the
// scanner owns surfacing-once. The caller applies rule #24 via a separate
// first-record probe (firstRecordIsSessionMeta), so streamLines does not track
// session_meta presence itself.
//
// The byte-offset/oversized-line/partial-line mechanics are reused verbatim
// from claude_code/scanner.go (the load-bearing tail invariants).
func streamLines(ctx context.Context, r io.Reader, emitFrom int64, rel string, mapper *fileMapper, dedup *unknownDedup, out chan<- canonical.Event, onError func(error)) (streamResult, error) {
	br := bufio.NewReaderSize(r, streamReaderSize)
	var res streamResult
	off := int64(0)
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			res.advanced = off
			return res, err
		}
		line, consumed, err := readOneLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				res.advanced = off
				return res, nil
			}
			if errors.Is(err, errLineTooLong) {
				// Surface exactly one SourceError for the oversized line, advance
				// past the drained bytes (up to and including its terminating
				// newline, or to EOF when it is the file's trailing line), and
				// CONTINUE reading subsequent records (edge #7). Jumping to EOF
				// here would silently discard every later valid record.
				if off >= emitFrom {
					onError(fmt.Errorf("rollout %s @%d: line exceeds %d bytes; skipping", rel, off, scanBufferMax))
				}
				off += consumed
				continue
			}
			res.advanced = off
			return res, fmt.Errorf("read %s @%d: %w", rel, off, err)
		}
		if len(line) == 0 {
			res.advanced = off
			return res, nil
		}
		recBytes := line[:len(line)-1]
		lineStart := off
		off += int64(len(line))
		lineNo++
		emit := lineStart >= emitFrom

		rec, skip, perr := parseLine(recBytes)
		if perr != nil {
			if emit && shouldSurfaceParseError(dedup, perr) {
				onError(fmt.Errorf("rollout %s @%d: %w", rel, lineStart, perr))
			}
			continue
		}
		if skip {
			continue
		}
		// mapRecord always runs so the per-file turn/op inference counters
		// advance during a resume replay; only events at/after emitFrom are
		// sent. setLineNo anchors PayloadRef "#L<line>" at the owning record.
		mapper.setLineNo(lineNo)
		events, mErr := mapper.mapRecord(rec)
		if mErr != nil {
			if emit {
				onError(fmt.Errorf("rollout %s @%d: map: %w", rel, lineStart, mErr))
			}
			continue
		}
		if !emit {
			continue
		}
		for _, ev := range events {
			select {
			case <-ctx.Done():
				res.advanced = off
				return res, ctx.Err()
			case out <- ev:
				res.emitted++
			}
		}
	}
}

// readOneLine reads one '\n'-terminated record from br, returning the line WITH
// the trailing '\n' so callers can advance offset by len(). Returns io.EOF
// (with consumed=0) when no complete line is available — it never returns a
// partial trailing line, implementing the hold-back invariant (spec
// "Atomicity"). On errLineTooLong it returns the number of bytes drained up to
// AND including the next '\n' (or to EOF when the oversized line is the file's
// trailing bytes) so the caller can advance past the skipped line and continue.
// consumed is meaningful only for the errLineTooLong and nil-error cases; it is
// 0 for io.EOF and other errors. Reused verbatim from claude_code.
func readOneLine(br *bufio.Reader) ([]byte, int64, error) {
	buf := make([]byte, 0, 256)
	for {
		chunk, err := br.ReadSlice('\n')
		if err == nil {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				return nil, int64(len(buf)), errLineTooLong
			}
			return buf, int64(len(buf)), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				// Drain the rest of the oversized line and report total bytes
				// consumed so the caller advances past it and continues.
				drained, drainErr := drainToNewline(br)
				if drainErr != nil && !errors.Is(drainErr, io.EOF) {
					return nil, 0, drainErr
				}
				return nil, int64(len(buf)) + drained, errLineTooLong
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			// Partial line at EOF: do not return it (hold-back).
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}
}

// drainToNewline reads and discards bytes from br up to and including the next
// '\n', returning the number of bytes consumed. On io.EOF (the oversized line
// runs to the end of the file with no trailing newline) it returns the bytes
// consumed so far together with io.EOF so the caller can advance the offset to
// EOF; the next read then reports io.EOF cleanly. Reused verbatim from
// claude_code.
func drainToNewline(br *bufio.Reader) (int64, error) {
	var consumed int64
	for {
		chunk, err := br.ReadSlice('\n')
		consumed += int64(len(chunk))
		if err == nil {
			return consumed, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return consumed, err
	}
}

// unknownDedup is the per-file seen-set that bounds unknown-variant SourceErrors
// to exactly one per distinct unknown top-level `type` OR nested
// "<owner>/<payload.type>" per session (spec rule #2, acceptance #2). The
// dedup lives in the scanner because parseLine returns the offending variant
// via a typed error (parser.go: unknownTypeError / unknownPayloadTypeError)
// before the mapper is reached, so the scanner owns surfacing-once. The two
// sentinel families use distinct key spaces so a top-level name never collides
// with a nested name.
type unknownDedup struct {
	seen map[string]struct{}
}

// newUnknownDedup constructs an empty per-file dedup set.
func newUnknownDedup() *unknownDedup {
	return &unknownDedup{seen: map[string]struct{}{}}
}

// first reports whether key is the first occurrence on this file, recording it.
func (d *unknownDedup) first(key string) bool {
	if d == nil {
		return true
	}
	if d.seen == nil {
		d.seen = map[string]struct{}{}
	}
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	return true
}

// shouldSurfaceParseError reports whether a per-line parse error should be
// forwarded to onError. Unknown top-level `type` and unknown nested
// `payload.type` errors are deduped to one per distinct variant per file (spec
// rule #2, acceptance #2) via the per-file dedup set; all other parse errors
// (malformed JSON, missing type, decode failures) surface every time because
// each describes a distinct broken line, not a repeated known-unknown variant.
// Mirrors claude_code's shouldSurfaceParseError, extended for codex's second
// (nested) unknown family.
func shouldSurfaceParseError(dedup *unknownDedup, perr error) bool {
	var ute *unknownTypeError
	if errors.As(perr, &ute) {
		return dedup.first("type:" + ute.Type)
	}
	var upe *unknownPayloadTypeError
	if errors.As(perr, &upe) {
		return dedup.first("payload:" + upe.Owner + "/" + upe.Type)
	}
	return true
}

// withinResolvedRoot reports whether abs resolves (through symlinks) to a path
// inside resolvedRoot (security.md §6 "No symlink traversal escape"), for
// callers that have ALREADY resolved the sessions root once (the directory-walk
// hot path: every discovered rollout shares one resolved root, so re-running
// EvalSymlinks on the root per file is wasted work). resolvedRoot MUST be the
// output of filepath.EvalSymlinks on the configured root; only abs is resolved
// here. Returns:
//   - (resolvedAbs, true, nil)  — abs resolves to a path under the root.
//   - ("", false, nil)          — abs resolves outside the root (escape).
//   - ("", false, err)          — the path could not be resolved.
//
// Reused verbatim from claude_code (the single-shot resolveWithinRoot wrapper
// is added by Chunk D's payloadURI when it needs to resolve the root per call).
func withinResolvedRoot(resolvedRoot, abs string) (string, bool, error) {
	resolvedAbs, err := evalSymlinksAllowingTail(filepath.Clean(abs))
	if err != nil {
		return "", false, fmt.Errorf("resolve path %q: %w", abs, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedAbs)
	if err != nil {
		return "", false, fmt.Errorf("relative %q under %q: %w", resolvedAbs, resolvedRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false, nil
	}
	return resolvedAbs, true, nil
}

// evalSymlinksAllowingTail resolves symlinks in abs, tolerating a not-yet-
// created leaf/tail: it walks up to the deepest existing ancestor, resolves
// that, and re-joins the non-existent remainder. A non-existent path cannot be
// a symlink itself, so judging it by its resolved parent is sound. Reused
// verbatim from claude_code.
func evalSymlinksAllowingTail(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		// Reached the filesystem root without an existing ancestor.
		return abs, nil
	}
	resolvedParent, perr := evalSymlinksAllowingTail(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

// withinSourceRoot reports whether abs resolves (through symlinks) to a path
// inside resolvedRoot. On escape it surfaces a SourceError via onError and
// returns false; on a resolve error it likewise surfaces and returns false, so
// a path that cannot be safely resolved is skipped rather than read. Mirrors
// claude_code's withinSourceRoot.
func withinSourceRoot(resolvedRoot, abs string, onError func(error)) bool {
	resolved, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil {
		onError(fmt.Errorf("codex: cannot resolve %s for containment; skipping: %w", abs, err))
		return false
	}
	if !ok {
		onError(fmt.Errorf("codex: %s resolves to %s outside the sessions root; skipping (symlink escape)", abs, resolved))
		return false
	}
	return true
}
