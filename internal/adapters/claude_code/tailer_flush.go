package claude_code

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type tailFlush struct {
	ctx          context.Context
	resolvedRoot string
	root         string
	sourceID     string
	cur          *Cursor
	def          *tailDeferral
	out          chan<- canonical.Event
	onError      func(error)
}

func newTailFlush(ctx context.Context, resolvedRoot, root, sourceID string, cur *Cursor, def *tailDeferral, out chan<- canonical.Event, onError func(error)) *tailFlush {
	return &tailFlush{
		ctx:          ctx,
		resolvedRoot: resolvedRoot,
		root:         root,
		sourceID:     sourceID,
		cur:          cur,
		def:          def,
		out:          out,
		onError:      ensureOnError(onError),
	}
}

func (f *tailFlush) flushDirty(dirty, metaDirty map[string]struct{}) error {
	if err := f.flushChangedMetas(metaDirty); err != nil {
		return err
	}
	if len(dirty) == 0 {
		return f.finishMetaOnlyFlush(metaDirty)
	}
	if err := f.processDirtyTranscripts(dirty); err != nil {
		return err
	}
	if err := f.finishDeferral(); err != nil {
		return err
	}
	return emitProgress(f.ctx, f.sourceID, *f.cur, f.out)
}

func (f *tailFlush) finishMetaOnlyFlush(metaDirty map[string]struct{}) error {
	if len(metaDirty) == 0 {
		return nil
	}
	return emitProgress(f.ctx, f.sourceID, *f.cur, f.out)
}

func (f *tailFlush) flushChangedMetas(metaDirty map[string]struct{}) error {
	rels := slices.Sorted(maps.Keys(metaDirty))
	startMetaSeen := f.cur.MetaSeen
	currentHashes := f.collectCurrentMetaHashes(rels)
	if err := repairChangedMetas(f.ctx, f.sourceID, f.root, f.resolvedRoot, startMetaSeen, currentHashes, f.out, f.onError); err != nil {
		return err
	}
	f.recordChangedMetaHashes(rels, currentHashes)
	return nil
}

func (f *tailFlush) collectCurrentMetaHashes(rels []string) map[string]string {
	currentHashes := make(map[string]string, len(rels))
	for _, rel := range rels {
		raw, ok := readMetaForRepair(rel, f.root, f.resolvedRoot, f.onError)
		if ok {
			currentHashes[rel] = hashBytes(raw)
		}
	}
	return currentHashes
}

func (f *tailFlush) recordChangedMetaHashes(rels []string, currentHashes map[string]string) {
	for _, rel := range rels {
		h, ok := currentHashes[rel]
		if ok && f.cur.metaSeen(rel) != h {
			*f.cur = f.cur.withMetaSeen(rel, h)
		}
	}
}

func (f *tailFlush) processDirtyTranscripts(dirty map[string]struct{}) error {
	metaCache := map[string]metaMap{}
	for _, rel := range slices.Sorted(maps.Keys(dirty)) {
		if err := f.processDirtyTranscript(rel, metaCache); err != nil {
			return err
		}
	}
	return nil
}

func (f *tailFlush) processDirtyTranscript(rel string, metaCache map[string]metaMap) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	t, ok := transcriptForRel(f.root, rel)
	if !ok {
		return nil
	}
	return f.readAndRecordTranscript(t, metaCache)
}

func (f *tailFlush) readAndRecordTranscript(t transcript, metaCache map[string]metaMap) error {
	fc := f.cur.fileCursor(t.rel)
	updated, _, mapper, err := readTranscript(f.ctx, f.root, t, f.sourceID, f.metasFor(t, metaCache), fc, f.out, f.onError)
	if err != nil {
		return f.handleTranscriptError(err)
	}
	*f.cur = f.cur.withFile(t.rel, updated)
	f.foldDeferral(mapper, t)
	return nil
}

func (f *tailFlush) metasFor(t transcript, metaCache map[string]metaMap) metaMap {
	if t.sessionDir == "" {
		return metaMap{}
	}
	cached, ok := metaCache[t.sessionDir]
	if !ok {
		cached = readSessionMetas(f.resolvedRoot, t.sessionDir, f.onError)
		metaCache[t.sessionDir] = cached
	}
	return cached
}

func (f *tailFlush) handleTranscriptError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	f.onError(err)
	return nil
}

func (f *tailFlush) foldDeferral(mapper *fileMapper, t transcript) {
	if f.def != nil {
		collectAgentDeferral(mapper, t, f.def.pending, f.def.completed)
	}
}

func (f *tailFlush) finishDeferral() error {
	if f.def == nil {
		return nil
	}
	if err := pairCompletedFinalizations(f.ctx, f.sourceID, f.def, f.out); err != nil {
		return err
	}
	*f.cur = f.cur.withParked(f.def.parkedSnapshot())
	*f.cur = f.cur.withFinalized(f.def.finalizedSnapshot())
	return nil
}
