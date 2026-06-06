package claude_code

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type tailDirtySets struct {
	transcripts map[string]struct{}
	metas       map[string]struct{}
}

func newTailDirtySets() tailDirtySets {
	return tailDirtySets{
		transcripts: make(map[string]struct{}, 16),
		metas:       make(map[string]struct{}, 16),
	}
}

func (d tailDirtySets) len() int {
	return len(d.transcripts) + len(d.metas)
}

func (d tailDirtySets) any() bool {
	return d.len() > 0
}

func (d *tailDirtySets) mark(base, rel string) {
	switch {
	case strings.HasSuffix(base, metaExt):
		d.metas[rel] = struct{}{}
	case strings.HasSuffix(base, transcriptExt):
		d.transcripts[rel] = struct{}{}
	}
}

// handleEvent classifies one fsnotify event. New directories are watched and
// existing files inside them are marked dirty for race-window catch-up.
func handleEvent(watcher *fsnotify.Watcher, resolvedRoot, root string, ev fsnotify.Event, watched, dirty, metaDirty map[string]struct{}, onError func(error)) {
	_ = root // cursor keys are resolvedRoot-relative; root documents the source.
	handler := tailEventHandler{
		watcher:      watcher,
		resolvedRoot: resolvedRoot,
		watched:      watched,
		dirty:        tailDirtySets{transcripts: dirty, metas: metaDirty},
		onError:      ensureOnError(onError),
	}
	handler.handle(ev)
}

type tailEventHandler struct {
	watcher      *fsnotify.Watcher
	resolvedRoot string
	watched      map[string]struct{}
	dirty        tailDirtySets
	onError      func(error)
}

func (h tailEventHandler) handle(ev fsnotify.Event) {
	if h.handleCreatedDirectory(ev) {
		return
	}
	if h.handleRemoveOrRename(ev) {
		return
	}
	h.markFileEvent(ev.Name)
}

func (h tailEventHandler) handleCreatedDirectory(ev fsnotify.Event) bool {
	if ev.Op&fsnotify.Create == 0 {
		return false
	}
	info, err := os.Stat(ev.Name)
	if err != nil || !info.IsDir() {
		return false
	}
	addWatchTree(h.watcher, h.resolvedRoot, ev.Name, h.watched, h.onError)
	markExistingDirty(h.resolvedRoot, ev.Name, h.dirty.transcripts, h.dirty.metas, h.onError)
	return true
}

func (h tailEventHandler) handleRemoveOrRename(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	base := filepath.Base(ev.Name)
	if strings.HasSuffix(base, transcriptExt) || strings.HasSuffix(base, metaExt) {
		h.onError(fmt.Errorf("claude_code: %s removed/renamed", relOrBase(h.resolvedRoot, ev.Name)))
	}
	return true
}

func (h tailEventHandler) markFileEvent(path string) {
	rel, err := relPath(h.resolvedRoot, path)
	if err != nil {
		return
	}
	h.dirty.mark(filepath.Base(path), rel)
}

// relOrBase returns the base-relative path for logging, falling back to the
// basename when the path is outside base.
func relOrBase(base, abs string) string {
	if rel, err := relPath(base, abs); err == nil {
		return rel
	}
	return filepath.Base(abs)
}

// resetDebounce restarts the debounce timer for one window.
func resetDebounce(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(debounceWindow)
}
