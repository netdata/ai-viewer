package codex

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
	dirty        map[string]struct{}
	debounce     *time.Timer
	tick         *time.Ticker
}

func newTailRuntime(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) (*tailRuntime, error) {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("codex: fsnotify watcher: %w", err)
	}
	rt := &tailRuntime{
		ctx:      ctx,
		root:     root,
		sourceID: sourceID,
		cur:      cur,
		out:      out,
		onError:  onError,
		watcher:  watcher,
		watched:  map[string]struct{}{},
		dirty:    newDirtySet(),
	}
	if !rt.prepareRoot() {
		rt.close()
		return nil, nil
	}
	rt.addWatchTree(rt.resolvedRoot)
	return rt, nil
}

func (rt *tailRuntime) prepareRoot() bool {
	if _, statErr := os.Stat(rt.root); statErr != nil {
		rt.onError(fmt.Errorf("codex: sessions root %s not present (read-only on sources, no mkdir): %w", rt.root, statErr))
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(rt.root))
	if err != nil {
		rt.onError(fmt.Errorf("codex: cannot resolve sessions root %s; tail disabled for this source: %w", rt.root, err))
		return false
	}
	rt.resolvedRoot = resolvedRoot
	return true
}

func (rt *tailRuntime) close() {
	_ = rt.watcher.Close()
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
	return catchUpFromCursor(rt.ctx, rt.resolvedRoot, rt.root, rt.sourceID, &rt.cur, rt.out, rt.onError)
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
	handleEvent(rt.watcher, rt.resolvedRoot, ev, rt.watched, rt.dirty, rt.onError)
	if len(rt.dirty) >= debounceMaxEntries {
		return rt.flushNow()
	}
	if len(rt.dirty) > 0 {
		resetDebounce(rt.debounce)
	}
	return nil
}

func (rt *tailRuntime) onWatcherError(err error) {
	rt.onError(fmt.Errorf("codex: watcher: %w", err))
}

func (rt *tailRuntime) flushNow() error {
	err := flushDirty(rt.ctx, rt.resolvedRoot, rt.sourceID, rt.dirty, &rt.cur, rt.out, rt.onError)
	rt.dirty = newDirtySet()
	return err
}

func (rt *tailRuntime) onTick() error {
	rt.addWatchTree(rt.resolvedRoot)
	return emitProgress(rt.ctx, rt.sourceID, rt.cur, rt.out)
}

func (rt *tailRuntime) addWatchTree(dir string) {
	addWatchTree(rt.watcher, rt.resolvedRoot, dir, rt.watched, rt.onError)
}

func newDirtySet() map[string]struct{} {
	return make(map[string]struct{}, 16)
}

// catchUpFromCursor reads every currently-discovered modern rollout from its
// cursor offset to current EOF, once, at Tail startup (spec §"Watch
// Strategy"). It closes the Scan→Tail window: bytes appended before the watch
// was established are read here rather than waiting for a future WRITE event.
// It reuses flushDirty so the offset advance, partial-line hold-back, and
// SourceProgress checkpoint are identical to the steady-state path. A file
// already fully consumed by Scan re-reads zero new bytes (offset == size) and
// emits nothing. Legacy files are NOT re-reported here (Scan already emitted
// the one-time SourceError; the cursor suppresses them).
func catchUpFromCursor(ctx context.Context, resolvedRoot, root, sourceID string, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	disc, err := discoverRollouts(root, onError)
	if err != nil {
		onError(fmt.Errorf("codex: tail catch-up discovery: %w", err))
		return nil
	}
	return flushDirty(ctx, resolvedRoot, sourceID, dirtyFromRollouts(disc.modern), cur, out, onError)
}

func dirtyFromRollouts(rollouts []rollout) map[string]struct{} {
	dirty := make(map[string]struct{}, len(rollouts))
	for _, r := range rollouts {
		dirty[r.rel] = struct{}{}
	}
	return dirty
}
