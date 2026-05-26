package aiagent_v2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// debounceWindow is the coalescing window applied per file when
// multiple fsnotify Write/Rename events arrive in rapid succession.
// An active v2 session can rewrite its `.json.gz` dozens of times per
// minute; the debouncer ensures at most one parse per file per window.
// Spec §Watch Strategy.
const debounceWindow = 250 * time.Millisecond

// debounceMaxEntries bounds the per-flush dirty map. Bursty inputs
// trigger an early flush to keep memory bounded.
const debounceMaxEntries = 1024

// tailTickInterval drives the periodic SourceProgress emission so the
// cursor persists even when no fsnotify events arrive. Matches the
// v3 adapter's cadence so operators see consistent timing.
const tailTickInterval = 5 * time.Second

// tailLoop runs the fsnotify event loop until ctx is cancelled. The
// adapter's Tail method delegates here. Owns the watcher lifecycle.
//
// Per spec, on every dirty-file flush:
//  1. Process the file once with the current cursor.
//  2. Re-stat: if mtime advanced during the read, re-process exactly
//     ONCE more. Never loop — a fast producer would starve the
//     watcher otherwise.
func tailLoop(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) error {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("aiagent_v2: fsnotify watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// 0o750 matches the v3 adapter: owner full, group read+exec, world
	// none. Created defensively when the operator points the ingester
	// at a fresh sessions directory.
	if mkErr := os.MkdirAll(root, 0o750); mkErr != nil {
		return fmt.Errorf("aiagent_v2: ensure %s: %w", root, mkErr)
	}
	if addErr := watcher.Add(root); addErr != nil {
		return fmt.Errorf("aiagent_v2: watch %s: %w", root, addErr)
	}

	dirty := make(map[string]struct{}, 16)
	debounce := time.NewTimer(debounceWindow)
	defer debounce.Stop()
	if !debounce.Stop() {
		<-debounce.C
	}
	tick := time.NewTicker(tailTickInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			name := tailableName(root, ev)
			if name == "" {
				continue
			}
			if ev.Op&fsnotify.Remove != 0 {
				// Removal does not require a reread; drop any pending
				// dirty entry so a quick rename-into-place still
				// triggers a clean re-process.
				delete(dirty, name)
				continue
			}
			dirty[name] = struct{}{}
			if len(dirty) >= debounceMaxEntries {
				if perr := flushDirty(ctx, root, sourceID, dirty, &cur, out, onError); perr != nil {
					return perr
				}
				dirty = make(map[string]struct{}, 16)
				continue
			}
			resetDebounce(debounce)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			onError(fmt.Errorf("aiagent_v2: watcher: %w", err))
		case <-debounce.C:
			if perr := flushDirty(ctx, root, sourceID, dirty, &cur, out, onError); perr != nil {
				return perr
			}
			dirty = make(map[string]struct{}, 16)
		case <-tick.C:
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return perr
			}
		}
	}
}

// tailableName returns the file basename if the fsnotify event
// targets a `.json.gz` snapshot under root; "" otherwise. Skips
// `.tmp-*` orphans, directory events, and anything outside root.
func tailableName(root string, ev fsnotify.Event) string {
	name := filepath.Base(ev.Name)
	if !isSnapshotName(name) {
		return ""
	}
	// Ignore events for files in subdirectories (v3 lives at session/
	// and payloads/; we never want to act on them).
	dir := filepath.Dir(ev.Name)
	if dir != root && !strings.HasPrefix(dir+string(filepath.Separator), root+string(filepath.Separator)) {
		return ""
	}
	if dir != root {
		return ""
	}
	return name
}

// resetDebounce restarts the debounce timer. Drains any pending tick
// first so the next fire is exactly one window from now.
func resetDebounce(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(debounceWindow)
}

// flushDirty re-processes every dirty file and emits a SourceProgress
// checkpoint at the end. Implements the "re-read once if mtime
// advances during the read" rule from
// `adapter-aiagent-v2.md` §Watch Strategy.
func flushDirty(ctx context.Context, root, sourceID string, dirty map[string]struct{}, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	if len(dirty) == 0 {
		return nil
	}
	names := make([]string, 0, len(dirty))
	for n := range dirty {
		names = append(names, n)
	}
	sortStrings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if perr := processOnce(ctx, root, sourceID, name, cur, out, onError); perr != nil {
			if errors.Is(perr, context.Canceled) || errors.Is(perr, context.DeadlineExceeded) {
				return perr
			}
			onError(perr)
		}
	}
	return emitProgress(ctx, sourceID, *cur, out)
}

// processOnce runs processFile and, if the file's mtime advanced
// during the read, retries exactly ONCE more. The single retry
// captures the "writer flipped the file during our read" race
// without looping.
func processOnce(ctx context.Context, root, sourceID, name string, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	full := filepath.Join(root, name)
	preInfo, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", full, err)
	}
	preMtime := preInfo.ModTime().UnixNano()
	fc := cur.fileCursor(name)
	updated, changed, perr := processFile(ctx, root, sourceID, name, fc, out, onError)
	if perr != nil {
		return perr
	}
	if changed {
		*cur = cur.withFile(name, updated)
	}
	// Re-stat post-read; if mtime advanced, retry ONCE.
	postInfo, err := os.Stat(full)
	if err != nil {
		// File may have been atomically renamed away mid-read; not
		// fatal.
		return nil
	}
	if postInfo.ModTime().UnixNano() == preMtime {
		return nil
	}
	updated, changed, perr = processFile(ctx, root, sourceID, name, cur.fileCursor(name), out, onError)
	if perr != nil {
		return perr
	}
	if changed {
		*cur = cur.withFile(name, updated)
	}
	return nil
}

// sortStrings is a tiny insertion sort to keep the dependency surface
// of tailer.go small. Names lists are typically << 16.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j-1] > s[j] {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}
