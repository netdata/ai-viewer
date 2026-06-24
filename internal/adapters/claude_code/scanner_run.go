package claude_code

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// scanAll walks the projects root and reads every transcript from its cursor
// offset to EOF, emitting events and periodic SourceProgress. Orphan-root
// sessions are synthesized before child transcripts are processed.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	transcripts, err := discoverTranscripts(root, onError)
	if err != nil {
		return start, err
	}
	runner := newScanRunner(ctx, root, sourceID, start, out, ensureOnError(onError))
	return runner.run(transcripts)
}

type scanRunner struct {
	ctx                  context.Context
	root                 string
	resolvedRoot         string
	sourceID             string
	start                Cursor
	cur                  Cursor
	out                  chan<- canonical.Event
	onError              func(error)
	metaCache            map[string]metaMap
	def                  *tailDeferral
	emittedSinceProgress int
	lastProgress         time.Time
}

func newScanRunner(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) *scanRunner {
	return &scanRunner{
		ctx:          ctx,
		root:         root,
		resolvedRoot: resolveScanRoot(root),
		sourceID:     sourceID,
		start:        start,
		cur:          cursorOrNew(start),
		out:          out,
		onError:      onError,
		metaCache:    map[string]metaMap{},
		def:          restoredTailDeferral(start),
		lastProgress: time.Now(),
	}
}

func resolveScanRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(root)); err == nil {
		return resolved
	}
	return root
}

func cursorOrNew(start Cursor) Cursor {
	if start.Files != nil {
		return start
	}
	return newCursor()
}

func restoredTailDeferral(start Cursor) *tailDeferral {
	def := newTailDeferral()
	def.restoreFinalized(start.finalizedSet())
	def.restoreParked(start.Parked)
	return def
}

func (s *scanRunner) run(transcripts []transcript) (Cursor, error) {
	if err := emitOrphanRoots(s.ctx, s.resolvedRoot, s.sourceID, transcripts, s.out); err != nil {
		return s.cur, err
	}
	if err := s.processTranscripts(transcripts); err != nil {
		return s.cur, err
	}
	if err := pairCompletedFinalizations(s.ctx, s.sourceID, s.def, s.out); err != nil {
		return s.cur, err
	}
	if err := s.repairChangedMetas(); err != nil {
		return s.cur, err
	}
	s.persistDeferral()
	if err := emitProgress(s.ctx, s.sourceID, s.cur, s.out); err != nil {
		return s.cur, err
	}
	return s.cur, nil
}

func (s *scanRunner) processTranscripts(transcripts []transcript) error {
	for _, tr := range transcripts {
		if err := s.processTranscript(tr); err != nil {
			return err
		}
	}
	return nil
}

func (s *scanRunner) processTranscript(tr transcript) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	mm := s.metasFor(tr)
	fc := s.cur.fileCursor(tr.rel)
	updated, n, mapper, err := readTranscript(s.ctx, s.root, tr, s.sourceID, mm, fc, s.out, s.onError)
	if err != nil {
		return s.handleTranscriptError(err)
	}
	s.cur = s.cur.withFile(tr.rel, updated)
	collectAgentDeferral(mapper, tr, s.def.pending, s.def.completed, s.def.finalized)
	s.emittedSinceProgress += n
	return s.maybeEmitProgress()
}

func (s *scanRunner) metasFor(tr transcript) metaMap {
	if tr.sessionDir == "" {
		return metaMap{}
	}
	cached, ok := s.metaCache[tr.sessionDir]
	if ok {
		return cached
	}
	cached = readSessionMetas(s.resolvedRoot, tr.sessionDir, s.onError)
	s.metaCache[tr.sessionDir] = cached
	return cached
}

func (s *scanRunner) handleTranscriptError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.onError(err)
	return nil
}

func (s *scanRunner) maybeEmitProgress() error {
	if !s.shouldEmitProgress() {
		return nil
	}
	s.persistDeferral()
	if err := emitProgress(s.ctx, s.sourceID, s.cur, s.out); err != nil {
		return err
	}
	s.emittedSinceProgress = 0
	s.lastProgress = time.Now()
	return nil
}

func (s *scanRunner) shouldEmitProgress() bool {
	return s.emittedSinceProgress >= progressEveryEvents || time.Since(s.lastProgress) >= progressEveryDuration
}

func (s *scanRunner) repairChangedMetas() error {
	hashes := metaHashes(s.root, s.resolvedRoot, s.onError)
	err := repairChangedMetas(s.ctx, s.sourceID, s.root, s.resolvedRoot, s.start.MetaSeen, hashes, s.out, s.onError)
	if err != nil {
		return err
	}
	for rel, h := range hashes {
		s.cur = s.cur.withMetaSeen(rel, h)
	}
	return nil
}

func (s *scanRunner) persistDeferral() {
	s.cur = s.cur.withParked(s.def.parkedSnapshot())
	s.cur = s.cur.withFinalized(s.def.finalizedSnapshot())
}

// agentOpFinalize captures the parent session and op location of a deferred
// Agent op (spec §8.1).
type agentOpFinalize struct {
	parentNativeID string
	ref            agentOpRef
}

// collectAgentDeferral folds one transcript's mapper state into the cross-file
// Agent-op deferral maps (spec §8.1).
func collectAgentDeferral(mapper *fileMapper, tr transcript, pending map[string]agentOpFinalize, completed map[string]completionState, finalized map[string]struct{}) {
	if mapper == nil {
		return
	}
	for childID := range mapper.agentOpsResolved {
		delete(pending, childID)
		delete(completed, childID)
		if finalized != nil {
			finalized[childID] = struct{}{}
		}
	}
	for childID, ref := range mapper.agentOps {
		if _, done := finalized[childID]; done {
			continue
		}
		pending[childID] = agentOpFinalize{parentNativeID: mapper.nativeID, ref: ref}
	}
	if tr.kind != canonical.KindSubAgent {
		return
	}
	recordChildCompletion(mapper, tr, completed)
}

func recordChildCompletion(mapper *fileMapper, tr transcript, completed map[string]completionState) {
	currentlyComplete := mapper.fullyRead && mapper.lastRecordAssistantText
	// Completion state has three cases:
	// ADD only when the child is fully read, ends in assistant text, and that
	// terminal assistant-text was newly emitted in this pass.
	// RETRACT when a re-read child is no longer complete, so stale completion
	// state cannot finalize the parent.
	// Replay below the emit gate is a no-op, so Scan/Tail restart replay rebuilds
	// mapper state without double-finalizing a parent Agent op.
	switch {
	case currentlyComplete && mapper.lastRecordEmitted:
		completed[tr.nativeID] = completionState{tsUs: mapper.lastAssistantTextTsUs}
	case !currentlyComplete:
		delete(completed, tr.nativeID)
	}
}

// emitProgress publishes a SourceProgressEvent with the current cursor.
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
