package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// handleEvent classifies one fsnotify event. New directories (new date shards)
// are added to the watch set AND walked for any rollout files already present
// (the create-race window). Rollout writes mark the relative path dirty.
// Removes/renames are logged, not acted on (spec edge #13: codex does not
// rename; a manual rename leaves the old cursor entry stale).
//
// Cursor keys are derived against the RESOLVED root: every watched dir is
// Add()ed from the resolved-root walk, so fsnotify reports event paths under
// the resolved root. Keying with relPath(resolvedRoot, …) yields the SAME key
// the scan side records (discoverRollouts also keys against the resolved root),
// so scan and tail keys are identical for one file regardless of root
// symlinking.
func handleEvent(watcher *fsnotify.Watcher, resolvedRoot string, ev fsnotify.Event, watched, dirty map[string]struct{}, onError func(error)) {
	handler := tailEventHandler{
		watcher:      watcher,
		resolvedRoot: resolvedRoot,
		watched:      watched,
		dirty:        dirty,
		onError:      onError,
	}
	handler.handle(ev)
}

type tailEventHandler struct {
	watcher      *fsnotify.Watcher
	resolvedRoot string
	watched      map[string]struct{}
	dirty        map[string]struct{}
	onError      func(error)
}

func (h tailEventHandler) handle(ev fsnotify.Event) {
	if h.handleCreatedDirectory(ev) {
		return
	}
	if h.handleRemoveOrRename(ev) {
		return
	}
	h.markRolloutEvent(ev.Name)
}

func (h tailEventHandler) handleCreatedDirectory(ev fsnotify.Event) bool {
	if ev.Op&fsnotify.Create == 0 {
		return false
	}
	if !pathIsDirectory(ev.Name) {
		return false
	}
	if filepath.Base(ev.Name) == archivedSessionsDir {
		return true
	}
	addWatchTree(h.watcher, h.resolvedRoot, ev.Name, h.watched, h.onError)
	markExistingDirty(h.resolvedRoot, ev.Name, h.dirty, h.onError)
	return true
}

func pathIsDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (h tailEventHandler) handleRemoveOrRename(ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	if isModernRolloutPath(ev.Name) {
		h.onError(fmt.Errorf("codex: %s removed/renamed", relOrBase(h.resolvedRoot, ev.Name)))
	}
	return true
}

func (h tailEventHandler) markRolloutEvent(path string) {
	if !isModernRolloutPath(path) {
		return
	}
	rel, err := relPath(h.resolvedRoot, path)
	if err != nil {
		return
	}
	h.dirty[rel] = struct{}{}
}

func isModernRolloutPath(path string) bool {
	return modernNameRe.MatchString(filepath.Base(path))
}

// relOrBase returns the base-relative path for logging, falling back to the
// basename when the path is outside base. Mirrors claude_code.
func relOrBase(base, abs string) string {
	if rel, err := relPath(base, abs); err == nil {
		return rel
	}
	return filepath.Base(abs)
}

// resetDebounce restarts the debounce timer for one window. Mirrors claude_code.
func resetDebounce(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(debounceWindow)
}
