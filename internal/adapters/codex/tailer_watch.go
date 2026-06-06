package codex

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// markExistingDirty walks a newly-created directory and marks every modern
// rollout file already present as dirty, so the next flush reads content
// written into the dir before the watch was added (the create-race window). The
// periodic tick's addWatchTree handles dirs; this handles the files already
// inside them. base is the RESOLVED root so the keys it records match the scan
// cursor keys. A non-IsNotExist walk error over an unreadable subtree is
// surfaced via onError and the walk continues past it.
func markExistingDirty(base, dir string, dirty map[string]struct{}, onError func(error)) {
	marker := existingDirtyMarker{
		base:     base,
		startDir: dir,
		dirty:    dirty,
		onError:  nilSafeOnError(onError),
	}
	_ = filepath.WalkDir(dir, marker.walk)
}

type existingDirtyMarker struct {
	base     string
	startDir string
	dirty    map[string]struct{}
	onError  func(error)
}

func (m existingDirtyMarker) walk(path string, d os.DirEntry, err error) error {
	if err != nil {
		return m.walkError(path, d, err)
	}
	if d.IsDir() {
		return m.walkDir(path, d.Name())
	}
	m.markPath(path, d.Name())
	return nil
}

func (m existingDirtyMarker) walkError(path string, d os.DirEntry, err error) error {
	if !os.IsNotExist(err) {
		m.onError(fmt.Errorf("codex: walk new dir %s: %w", path, err))
	}
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (m existingDirtyMarker) walkDir(path, name string) error {
	if name == archivedSessionsDir && path != m.startDir {
		return filepath.SkipDir
	}
	return nil
}

func (m existingDirtyMarker) markPath(path, name string) {
	if !modernNameRe.MatchString(name) {
		return
	}
	rel, err := relPath(m.base, path)
	if err != nil {
		return
	}
	m.dirty[rel] = struct{}{}
}

// addWatchTree walks dir and Add()s every subdirectory not already watched.
// Errors adding a single dir are surfaced via onError but do not abort the
// walk. fsnotify de-duplicates Add() of an already-watched path, but the
// watched set avoids the syscall churn on every tick. resolvedRoot is the
// symlink-resolved sessions root: a directory that resolves outside it (a
// planted symlink escaping the source) is refused with a SourceError and not
// watched (security.md §6). The archive subtree is pruned (never watched). A
// non-IsNotExist walk error over an unreadable subtree is surfaced via onError
// and the walk continues past it. Mirrors claude_code.
func addWatchTree(watcher *fsnotify.Watcher, resolvedRoot, dir string, watched map[string]struct{}, onError func(error)) {
	adder := watchTreeAdder{
		watcher:      watcher,
		resolvedRoot: resolvedRoot,
		startDir:     dir,
		watched:      watched,
		onError:      nilSafeOnError(onError),
	}
	_ = filepath.WalkDir(dir, adder.walk)
}

type watchTreeAdder struct {
	watcher      *fsnotify.Watcher
	resolvedRoot string
	startDir     string
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
	return a.addDir(path, d.Name())
}

func (a watchTreeAdder) walkError(path string, d os.DirEntry, err error) error {
	if !os.IsNotExist(err) {
		a.onError(fmt.Errorf("codex: walk watch tree %s: %w", path, err))
	}
	if d != nil && d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

func (a watchTreeAdder) addDir(path, name string) error {
	if name == archivedSessionsDir && path != a.startDir {
		return filepath.SkipDir
	}
	if !withinSourceRoot(a.resolvedRoot, path, a.onError) {
		return filepath.SkipDir
	}
	if _, ok := a.watched[path]; ok {
		return nil
	}
	if err := a.watcher.Add(path); err != nil {
		a.onError(fmt.Errorf("codex: watch %s: %w", path, err))
		return nil
	}
	a.watched[path] = struct{}{}
	return nil
}

func nilSafeOnError(onError func(error)) func(error) {
	if onError != nil {
		return onError
	}
	return func(error) {}
}
