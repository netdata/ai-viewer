package codex

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type tailFlush struct {
	ctx          context.Context
	resolvedRoot string
	sourceID     string
	cur          *Cursor
	out          chan<- canonical.Event
	onError      func(error)
}

func newTailFlush(ctx context.Context, resolvedRoot, sourceID string, cur *Cursor, out chan<- canonical.Event, onError func(error)) *tailFlush {
	return &tailFlush{
		ctx:          ctx,
		resolvedRoot: resolvedRoot,
		sourceID:     sourceID,
		cur:          cur,
		out:          out,
		onError:      onError,
	}
}

// flushDirty re-reads every dirty rollout from its cursor offset, updating the
// shared cursor, and emits a SourceProgress checkpoint at the end. Each file is
// read via readRollout so the offset advance, partial-line hold-back,
// truncation defense, rule-#24 skip, and EOF stale-finalize are identical to
// the Scan path. A rel that no longer maps to a recognized rollout is skipped.
func flushDirty(ctx context.Context, resolvedRoot, sourceID string, dirty map[string]struct{}, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	return newTailFlush(ctx, resolvedRoot, sourceID, cur, out, onError).flush(dirty)
}

func (f *tailFlush) flush(dirty map[string]struct{}) error {
	if len(dirty) == 0 {
		return nil
	}
	if err := f.processDirtyRollouts(dirty); err != nil {
		return err
	}
	return emitProgress(f.ctx, f.sourceID, *f.cur, f.out)
}

func (f *tailFlush) processDirtyRollouts(dirty map[string]struct{}) error {
	for _, rel := range slices.Sorted(maps.Keys(dirty)) {
		if err := f.processDirtyRollout(rel); err != nil {
			return err
		}
	}
	return nil
}

func (f *tailFlush) processDirtyRollout(rel string) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	r, ok := rolloutForRel(f.resolvedRoot, rel)
	if !ok {
		return nil
	}
	return f.readAndRecordRollout(r)
}

func (f *tailFlush) readAndRecordRollout(r rollout) error {
	fc := f.cur.fileCursor(r.rel)
	updated, _, err := readRollout(f.ctx, f.resolvedRoot, r, f.sourceID, fc, f.out, f.onError)
	if err != nil {
		return f.handleRolloutError(err)
	}
	*f.cur = f.cur.withFile(r.rel, updated)
	return nil
}

func (f *tailFlush) handleRolloutError(err error) error {
	if isContextStop(err) {
		return err
	}
	f.onError(err)
	return nil
}

// rolloutForRel reconstructs a rollout descriptor from a root-relative path. A
// modern rollout is "YYYY/MM/DD/rollout-….jsonl". The abs path is built under
// the RESOLVED root so the containment open in readRollout resolves cleanly.
// Returns false when rel is not a recognized modern rollout (a legacy .json, an
// ignored name, a path with no rollout basename, OR a rollout-*.jsonl at the
// wrong shard depth — F8: only the YYYY/MM/DD layout is ingested, so a stray
// file directly under the root is not tailed even if a Write event fires on it).
func rolloutForRel(resolvedRoot, rel string) (rollout, bool) {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	if !modernNameRe.MatchString(base) || !hasShardDepth(rel) {
		return rollout{}, false
	}
	abs := filepath.Join(resolvedRoot, filepath.FromSlash(rel))
	return rollout{rel: rel, abs: abs}, true
}
