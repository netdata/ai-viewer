package claude_code

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// markExistingDirty walks a newly-created directory and marks every transcript
// or meta file already present as dirty for the next flush.
func markExistingDirty(base, dir string, dirty, metaDirty map[string]struct{}, onError func(error)) {
	marker := existingDirtyMarker{
		base:    base,
		dirty:   tailDirtySets{transcripts: dirty, metas: metaDirty},
		onError: ensureOnError(onError),
	}
	_ = filepath.WalkDir(dir, marker.walk)
}

type existingDirtyMarker struct {
	base    string
	dirty   tailDirtySets
	onError func(error)
}

func (m existingDirtyMarker) walk(path string, d os.DirEntry, err error) error {
	if err != nil {
		return m.walkError(path, d, err)
	}
	if d.IsDir() {
		return nil
	}
	m.markPath(path, d.Name())
	return nil
}

func (m existingDirtyMarker) walkError(path string, d os.DirEntry, err error) error {
	if !os.IsNotExist(err) {
		m.onError(fmt.Errorf("claude_code: walk new dir %s: %w", path, err))
	}
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (m existingDirtyMarker) markPath(path, name string) {
	rel, err := relPath(m.base, path)
	if err != nil {
		return
	}
	m.dirty.mark(name, rel)
}

// addWatchTree walks dir and Add()s every subdirectory not already watched.
func addWatchTree(watcher *fsnotify.Watcher, resolvedRoot, dir string, watched map[string]struct{}, onError func(error)) {
	adder := watchTreeAdder{
		watcher:      watcher,
		resolvedRoot: resolvedRoot,
		watched:      watched,
		onError:      ensureOnError(onError),
	}
	_ = filepath.WalkDir(dir, adder.walk)
}

type watchTreeAdder struct {
	watcher      *fsnotify.Watcher
	resolvedRoot string
	watched      map[string]struct{}
	onError      func(error)
}

func (a watchTreeAdder) walk(path string, d os.DirEntry, err error) error {
	if err != nil {
		return a.walkError(path, d, err)
	}
	if !d.IsDir() {
		return nil
	}
	return a.addDir(path)
}

func (a watchTreeAdder) walkError(path string, d os.DirEntry, err error) error {
	if !os.IsNotExist(err) {
		a.onError(fmt.Errorf("claude_code: walk watch tree %s: %w", path, err))
	}
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (a watchTreeAdder) addDir(path string) error {
	if !withinSourceRoot(a.resolvedRoot, path, a.onError) {
		return filepath.SkipDir
	}
	if _, ok := a.watched[path]; ok {
		return nil
	}
	if err := a.watcher.Add(path); err != nil {
		a.onError(fmt.Errorf("claude_code: watch %s: %w", path, err))
		return nil
	}
	a.watched[path] = struct{}{}
	return nil
}
