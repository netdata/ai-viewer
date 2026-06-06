package claude_code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

type tailRuntime struct {
	ctx          context.Context
	root         string
	resolvedRoot string
	sourceID     string
	cur          Cursor
	out          chan<- canonical.Event
	onError      func(error)
	watcher      *fsnotify.Watcher
	watched      map[string]struct{}
	dirty        tailDirtySets
	def          *tailDeferral
	debounce     *time.Timer
	tick         *time.Ticker
}

func newTailRuntime(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) (*tailRuntime, error) {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("claude_code: fsnotify watcher: %w", err)
	}
	rt := &tailRuntime{
		ctx:      ctx,
		root:     root,
		sourceID: sourceID,
		cur:      cur,
		out:      out,
		onError:  ensureOnError(onError),
		watcher:  watcher,
		watched:  map[string]struct{}{},
		dirty:    newTailDirtySets(),
		def:      newTailDeferral(),
	}
	if !rt.prepareRoot() {
		rt.close()
		return nil, nil
	}
	rt.addWatchTree(rt.resolvedRoot)
	rt.restoreDeferral()
	return rt, nil
}

func (rt *tailRuntime) prepareRoot() bool {
	if _, statErr := os.Stat(rt.root); statErr != nil {
		rt.onError(fmt.Errorf("claude_code: projects root %s not present (read-only on sources, no mkdir): %w", rt.root, statErr))
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(rt.root))
	if err != nil {
		rt.onError(fmt.Errorf("claude_code: cannot resolve projects root %s; tail disabled for this source: %w", rt.root, err))
		return false
	}
	rt.resolvedRoot = resolvedRoot
	return true
}

func (rt *tailRuntime) close() {
	_ = rt.watcher.Close()
}

func (rt *tailRuntime) restoreDeferral() {
	rt.def.restoreFinalized(rt.cur.finalizedSet())
	rt.def.restoreParked(rt.cur.Parked)
}

func (rt *tailRuntime) run() error {
	if err := rt.catchUp(); err != nil {
		return err
	}
	rt.startTimers()
	defer rt.stopTimers()
	return rt.selectLoop()
}

func (rt *tailRuntime) catchUp() error {
	return catchUpFromCursor(rt.ctx, rt.resolvedRoot, rt.root, rt.sourceID, &rt.cur, rt.def, rt.out, rt.onError)
}

func (rt *tailRuntime) startTimers() {
	rt.debounce = stoppedDebounceTimer()
	rt.tick = time.NewTicker(tailTickInterval)
}

func (rt *tailRuntime) stopTimers() {
	rt.debounce.Stop()
	rt.tick.Stop()
}

func stoppedDebounceTimer() *time.Timer {
	timer := time.NewTimer(debounceWindow)
	if !timer.Stop() {
		<-timer.C
	}
	return timer
}

func (rt *tailRuntime) selectLoop() error {
	for {
		done, err := rt.selectOnce()
		if done || err != nil {
			return err
		}
	}
}

func (rt *tailRuntime) selectOnce() (bool, error) {
	select {
	case <-rt.ctx.Done():
		return true, nil
	case ev, ok := <-rt.watcher.Events:
		return rt.handleWatcherEventRead(ev, ok)
	case err, ok := <-rt.watcher.Errors:
		return rt.handleWatcherErrorRead(err, ok)
	case <-rt.debounce.C:
		return false, rt.flushNow()
	case <-rt.tick.C:
		return false, rt.onTick()
	}
}

func (rt *tailRuntime) handleWatcherEventRead(ev fsnotify.Event, ok bool) (bool, error) {
	if !ok {
		return true, nil
	}
	return false, rt.onWatcherEvent(ev)
}

func (rt *tailRuntime) handleWatcherErrorRead(err error, ok bool) (bool, error) {
	if !ok {
		return true, nil
	}
	rt.onWatcherError(err)
	return false, nil
}

func (rt *tailRuntime) onWatcherEvent(ev fsnotify.Event) error {
	handleEvent(rt.watcher, rt.resolvedRoot, rt.root, ev, rt.watched, rt.dirty.transcripts, rt.dirty.metas, rt.onError)
	if rt.dirty.len() >= debounceMaxEntries {
		return rt.flushNow()
	}
	if rt.dirty.any() {
		resetDebounce(rt.debounce)
	}
	return nil
}

func (rt *tailRuntime) onWatcherError(err error) {
	rt.onError(fmt.Errorf("claude_code: watcher: %w", err))
}

func (rt *tailRuntime) flushNow() error {
	err := rt.newFlush().flushDirty(rt.dirty.transcripts, rt.dirty.metas)
	rt.dirty = newTailDirtySets()
	return err
}

func (rt *tailRuntime) newFlush() *tailFlush {
	return newTailFlush(rt.ctx, rt.resolvedRoot, rt.root, rt.sourceID, &rt.cur, rt.def, rt.out, rt.onError)
}

func (rt *tailRuntime) onTick() error {
	rt.addWatchTree(rt.resolvedRoot)
	return emitProgress(rt.ctx, rt.sourceID, rt.cur, rt.out)
}

func (rt *tailRuntime) addWatchTree(dir string) {
	addWatchTree(rt.watcher, rt.resolvedRoot, dir, rt.watched, rt.onError)
}

// catchUpFromCursor reads every currently-discovered transcript and changed
// meta once at Tail startup, through the steady-state flush path.
func catchUpFromCursor(ctx context.Context, resolvedRoot, root, sourceID string, cur *Cursor, def *tailDeferral, out chan<- canonical.Event, onError func(error)) error {
	transcripts, err := discoverTranscripts(root, onError)
	if err != nil {
		onError(fmt.Errorf("claude_code: tail catch-up discovery: %w", err))
		return nil
	}
	if len(transcripts) == 0 {
		return nil
	}
	dirty := dirtyFromTranscripts(transcripts)
	metaDirty := dirtyFromMetaHashes(root, resolvedRoot, onError)
	return newTailFlush(ctx, resolvedRoot, root, sourceID, cur, def, out, onError).flushDirty(dirty, metaDirty)
}

func dirtyFromTranscripts(transcripts []transcript) map[string]struct{} {
	dirty := make(map[string]struct{}, len(transcripts))
	for _, t := range transcripts {
		dirty[t.rel] = struct{}{}
	}
	return dirty
}

func dirtyFromMetaHashes(root, resolvedRoot string, onError func(error)) map[string]struct{} {
	dirty := make(map[string]struct{})
	for rel := range metaHashes(root, resolvedRoot, onError) {
		dirty[rel] = struct{}{}
	}
	return dirty
}
