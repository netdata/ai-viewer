package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// debounceWindow coalesces rapid Write events per flush cycle (spec
// §"Watch Strategy"). Mirrors claude_code.
const debounceWindow = 50 * time.Millisecond

// debounceMaxEntries bounds the dirty set before a forced flush.
const debounceMaxEntries = 4096

// tailTickInterval drives periodic SourceProgress emission and rescans for new
// shard directories (new YYYY/MM/DD created daily) that fsnotify
// (non-recursive on Linux) may have missed. Spec §"Watch Strategy" specifies a
// periodic full sweep; 5 s matches claude_code's cadence (a new-date-dir is
// also picked up immediately via the Create event handler — the tick is the
// slow-filesystem backstop).
const tailTickInterval = 5 * time.Second

// tailLoop runs the fsnotify event loop until ctx is cancelled. fsnotify is not
// recursive on Linux, so the loop walks the tree at startup and Add()s the root
// plus every YYYY, YYYY/MM, YYYY/MM/DD shard directory, and re-walks on a tick
// to pick up new date dirs created since the last walk (spec §"Watch
// Strategy"). The adapter owns the watcher lifecycle.
func tailLoop(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) error {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("codex: fsnotify watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// security.md §"Hard Rules" — read-only on sources, never mkdir. A missing
	// root surfaces a SourceError and returns cleanly so the daemon keeps running
	// for other sources.
	if _, statErr := os.Stat(root); statErr != nil {
		onError(fmt.Errorf("codex: sessions root %s not present (read-only on sources, no mkdir): %w", root, statErr))
		return nil
	}
	// Resolve the root through symlinks ONCE so every watched dir can be checked
	// for containment against it (security.md §6): a symlinked directory inside
	// the tree that points outside the resolved root is never Add()ed.
	resolvedRoot, rrErr := filepath.EvalSymlinks(filepath.Clean(root))
	if rrErr != nil {
		onError(fmt.Errorf("codex: cannot resolve sessions root %s; tail disabled for this source: %w", root, rrErr))
		return nil
	}
	watched := map[string]struct{}{}
	// Walk the RESOLVED root: filepath.WalkDir does not descend INTO a symlinked
	// walk-root, so walking the unresolved root would Add() zero directories
	// under a legitimately-symlinked sessions root. handleEvent still passes
	// newly-created dirs (real paths) as they appear; fsnotify dedups overlap.
	addWatchTree(watcher, resolvedRoot, resolvedRoot, watched, onError)

	dirty := make(map[string]struct{}, 16)

	// Initial catch-up (spec §"Watch Strategy"): the watch is now established,
	// but bytes appended to a known file BETWEEN Scan finishing and this point
	// arrived before the watch and would otherwise only be read on the next WRITE
	// event (which may never come for an idle session). Read every known file
	// from its cursor offset to current EOF once, up front. Re-emission of an
	// already-consumed line is absorbed by the ingester's idempotent upserts.
	if perr := catchUpFromCursor(ctx, resolvedRoot, root, sourceID, &cur, out, onError); perr != nil {
		return perr
	}

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
			handleEvent(watcher, resolvedRoot, ev, watched, dirty, onError)
			if len(dirty) >= debounceMaxEntries {
				if perr := flushDirty(ctx, resolvedRoot, root, sourceID, dirty, &cur, out, onError); perr != nil {
					return perr
				}
				dirty = make(map[string]struct{}, 16)
				continue
			}
			if len(dirty) > 0 {
				resetDebounce(debounce)
			}
		case werr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			onError(fmt.Errorf("codex: watcher: %w", werr))
		case <-debounce.C:
			if perr := flushDirty(ctx, resolvedRoot, root, sourceID, dirty, &cur, out, onError); perr != nil {
				return perr
			}
			dirty = make(map[string]struct{}, 16)
		case <-tick.C:
			// Re-walk to Add() any shard directory created since startup (new
			// YYYY/MM/DD date dir). codex creates a new date dir daily; the Create
			// handler picks it up immediately, and this tick is the slow-filesystem
			// backstop. Walk the RESOLVED root so a symlinked sessions root is fully
			// descended.
			addWatchTree(watcher, resolvedRoot, resolvedRoot, watched, onError)
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return perr
			}
		}
	}
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
	disc, derr := discoverRollouts(root, onError)
	if derr != nil {
		// A discovery failure is non-fatal for Tail: surface it and continue into
		// the watch loop (steady-state WRITE events still drive reads).
		onError(fmt.Errorf("codex: tail catch-up discovery: %w", derr))
		return nil
	}
	if len(disc.modern) == 0 {
		return nil
	}
	dirty := make(map[string]struct{}, len(disc.modern))
	for _, r := range disc.modern {
		dirty[r.rel] = struct{}{}
	}
	return flushDirty(ctx, resolvedRoot, root, sourceID, dirty, cur, out, onError)
}

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
	// A newly created directory must be watched (fsnotify is non-recursive).
	// Files written into it BEFORE we Add() the watch would be missed, so we also
	// walk the new dir and mark any rollouts already present as dirty. Subsequent
	// writes arrive via the watch.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			// Prune the archive subtree; never watch it.
			if filepath.Base(ev.Name) == archivedSessionsDir {
				return
			}
			addWatchTree(watcher, resolvedRoot, ev.Name, watched, onError)
			markExistingDirty(resolvedRoot, ev.Name, dirty, onError)
			return
		}
	}
	base := filepath.Base(ev.Name)
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if modernNameRe.MatchString(base) {
			onError(fmt.Errorf("codex: %s removed/renamed", relOrBase(resolvedRoot, ev.Name)))
		}
		return
	}
	// Only modern rollout files under a shard dir are tailed; legacy flat .json
	// at the root and all ignored names (sqlite, history, session_index) are
	// dropped here.
	if !modernNameRe.MatchString(base) {
		return
	}
	rel, err := relPath(resolvedRoot, ev.Name)
	if err != nil {
		return
	}
	dirty[rel] = struct{}{}
}

// markExistingDirty walks a newly-created directory and marks every modern
// rollout file already present as dirty, so the next flush reads content
// written into the dir before the watch was added (the create-race window). The
// periodic tick's addWatchTree handles dirs; this handles the files already
// inside them. base is the RESOLVED root so the keys it records match the scan
// cursor keys. A non-IsNotExist walk error over an unreadable subtree is
// surfaced via onError and the walk continues past it.
func markExistingDirty(base, dir string, dirty map[string]struct{}, onError func(error)) {
	if onError == nil {
		onError = func(error) {}
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				onError(fmt.Errorf("codex: walk new dir %s: %w", path, err))
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == archivedSessionsDir && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !modernNameRe.MatchString(d.Name()) {
			return nil
		}
		rel, rerr := relPath(base, path)
		if rerr != nil {
			return nil
		}
		dirty[rel] = struct{}{}
		return nil
	})
}

// relOrBase returns the base-relative path for logging, falling back to the
// basename when the path is outside base. Mirrors claude_code.
func relOrBase(base, abs string) string {
	if rel, err := relPath(base, abs); err == nil {
		return rel
	}
	return filepath.Base(abs)
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
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				onError(fmt.Errorf("codex: walk watch tree %s: %w", path, err))
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == archivedSessionsDir && path != dir {
			return filepath.SkipDir
		}
		if !withinSourceRoot(resolvedRoot, path, onError) {
			return filepath.SkipDir
		}
		if _, ok := watched[path]; ok {
			return nil
		}
		if addErr := watcher.Add(path); addErr != nil {
			onError(fmt.Errorf("codex: watch %s: %w", path, addErr))
			return nil
		}
		watched[path] = struct{}{}
		return nil
	})
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

// flushDirty re-reads every dirty rollout from its cursor offset, updating the
// shared cursor, and emits a SourceProgress checkpoint at the end. Each file is
// read via readRollout so the offset advance, partial-line hold-back,
// truncation defense, rule-#24 skip, and EOF stale-finalize are identical to
// the Scan path. A rel that no longer maps to a recognized rollout is skipped.
func flushDirty(ctx context.Context, resolvedRoot, root, sourceID string, dirty map[string]struct{}, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	if len(dirty) == 0 {
		return nil
	}
	names := make([]string, 0, len(dirty))
	for n := range dirty {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, rel := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, ok := rolloutForRel(resolvedRoot, rel)
		if !ok {
			continue
		}
		fc := cur.fileCursor(rel)
		updated, _, rerr := readRollout(ctx, resolvedRoot, r, sourceID, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return rerr
			}
			onError(rerr)
			continue
		}
		*cur = cur.withFile(rel, updated)
	}
	return emitProgress(ctx, sourceID, *cur, out)
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
