package claude_code

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

// debounceWindow coalesces rapid Write events per flush cycle (spec §6.4).
const debounceWindow = 50 * time.Millisecond

// debounceMaxEntries bounds the dirty set before a forced flush.
const debounceMaxEntries = 4096

// tailTickInterval drives periodic SourceProgress emission and rescans for
// new directories that fsnotify (non-recursive on Linux) may have missed.
const tailTickInterval = 5 * time.Second

// tailLoop runs the fsnotify event loop until ctx is cancelled. fsnotify is
// not recursive on Linux, so the loop walks the tree at startup and Add()s
// every directory it cares about (the root, every project dir, every
// session dir, every subagents/ dir), and re-walks on a tick to pick up new
// dirs created since the last walk (spec §6.1). The adapter owns the
// watcher lifecycle.
func tailLoop(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) error {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("claude_code: fsnotify watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// security.md §"Hard Rules" #1 — read-only on sources, never mkdir. A
	// missing root surfaces a SourceError and returns cleanly so the daemon
	// keeps running for other sources.
	if _, statErr := os.Stat(root); statErr != nil {
		onError(fmt.Errorf("claude_code: projects root %s not present (read-only on sources, no mkdir): %w", root, statErr))
		return nil
	}
	watched := map[string]struct{}{}
	addWatchTree(watcher, root, watched, onError)

	dirty := make(map[string]struct{}, 16)
	metaDirty := make(map[string]struct{}, 16)
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
			handleEvent(watcher, root, ev, watched, dirty, metaDirty, onError)
			if len(dirty)+len(metaDirty) >= debounceMaxEntries {
				if perr := flushDirty(ctx, root, sourceID, dirty, metaDirty, &cur, out, onError); perr != nil {
					return perr
				}
				dirty = make(map[string]struct{}, 16)
				metaDirty = make(map[string]struct{}, 16)
				continue
			}
			if len(dirty) > 0 || len(metaDirty) > 0 {
				resetDebounce(debounce)
			}
		case werr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			onError(fmt.Errorf("claude_code: watcher: %w", werr))
		case <-debounce.C:
			if perr := flushDirty(ctx, root, sourceID, dirty, metaDirty, &cur, out, onError); perr != nil {
				return perr
			}
			dirty = make(map[string]struct{}, 16)
			metaDirty = make(map[string]struct{}, 16)
		case <-tick.C:
			// Re-walk to Add() any directory created since startup
			// (new project dir, new session dir, new subagents/ dir).
			addWatchTree(watcher, root, watched, onError)
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return perr
			}
		}
	}
}

// handleEvent classifies one fsnotify event. New directories are added to
// the watch set. Transcript writes mark the relative path dirty; meta-file
// writes mark the meta path dirty. Removes/renames are logged, not acted on
// (spec §6.2).
func handleEvent(watcher *fsnotify.Watcher, root string, ev fsnotify.Event, watched, dirty, metaDirty map[string]struct{}, onError func(error)) {
	// A newly created directory must be watched (fsnotify is non-recursive).
	// Files written into it BEFORE we Add() the watch would be missed, so we
	// also walk the new dir and mark any transcripts/metas already present as
	// dirty (race-window catch-up). Subsequent writes arrive via the watch.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			addWatchTree(watcher, ev.Name, watched, onError)
			markExistingDirty(root, ev.Name, dirty, metaDirty)
			return
		}
	}
	base := filepath.Base(ev.Name)
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if strings.HasSuffix(base, transcriptExt) || strings.HasSuffix(base, metaExt) {
			onError(fmt.Errorf("claude_code: %s removed/renamed", relOrBase(root, ev.Name)))
		}
		return
	}
	rel, err := relPath(root, ev.Name)
	if err != nil {
		return
	}
	switch {
	case strings.HasSuffix(base, metaExt):
		metaDirty[rel] = struct{}{}
	case strings.HasSuffix(base, transcriptExt):
		dirty[rel] = struct{}{}
	}
}

// markExistingDirty walks a newly-created directory and marks every
// transcript / meta file already present as dirty, so the next flush reads
// content written into the dir before the watch was added (the create-race
// window). The periodic tick's addWatchTree handles dirs; this handles the
// files already inside them.
func markExistingDirty(root, dir string, dirty, metaDirty map[string]struct{}) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := relPath(root, path)
		if rerr != nil {
			return nil
		}
		switch {
		case strings.HasSuffix(d.Name(), metaExt):
			metaDirty[rel] = struct{}{}
		case strings.HasSuffix(d.Name(), transcriptExt):
			dirty[rel] = struct{}{}
		}
		return nil
	})
}

// relOrBase returns the root-relative path for logging, falling back to the
// basename when the path is outside root.
func relOrBase(root, abs string) string {
	if rel, err := relPath(root, abs); err == nil {
		return rel
	}
	return filepath.Base(abs)
}

// addWatchTree walks dir and Add()s every subdirectory not already watched.
// Errors adding a single dir are surfaced via onError but do not abort the
// walk. fsnotify de-duplicates Add() of an already-watched path, but the
// watched set avoids the syscall churn on every tick.
func addWatchTree(watcher *fsnotify.Watcher, dir string, watched map[string]struct{}, onError func(error)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if _, ok := watched[path]; ok {
			return nil
		}
		if addErr := watcher.Add(path); addErr != nil {
			onError(fmt.Errorf("claude_code: watch %s: %w", path, addErr))
			return nil
		}
		watched[path] = struct{}{}
		return nil
	})
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

// flushDirty re-reads every dirty transcript from its cursor offset and
// re-reads every changed meta file, updating the shared cursor. Emits a
// SourceProgress checkpoint at the end. Meta changes that introduce a new
// toolUseId→agentId mapping update only future Agent ops; already-emitted
// ops are not retro-patched (the resolver handles parent/child linkage via
// the structural path independently of toolUseId).
func flushDirty(ctx context.Context, root, sourceID string, dirty, metaDirty map[string]struct{}, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	// Record meta-file hashes first so a transcript flushed in the same
	// cycle picks up the freshest metaMap. A meta whose content hash is
	// unchanged from the cursor's metaSeen is skipped — a bare touch/mtime
	// bump does not warrant re-reading (spec §7 step 4).
	for rel := range metaDirty {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		h, ok := hashFile(abs)
		if !ok {
			continue
		}
		if cur.metaSeen(rel) == h {
			continue
		}
		*cur = cur.withMetaSeen(rel, h)
	}

	if len(dirty) == 0 {
		if len(metaDirty) == 0 {
			return nil
		}
		return emitProgress(ctx, sourceID, *cur, out)
	}

	names := make([]string, 0, len(dirty))
	for n := range dirty {
		names = append(names, n)
	}
	sort.Strings(names)

	metaCache := map[string]metaMap{}
	for _, rel := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		t, ok := transcriptForRel(root, rel)
		if !ok {
			continue
		}
		mm := metaMap{}
		if t.sessionDir != "" {
			cached, seen := metaCache[t.sessionDir]
			if !seen {
				cached = readSessionMetas(t.sessionDir)
				metaCache[t.sessionDir] = cached
			}
			mm = cached
		}
		fc := cur.fileCursor(rel)
		updated, _, rerr := readTranscript(ctx, t, sourceID, mm, fc, out, onError)
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

// transcriptForRel reconstructs a transcript descriptor from a root-relative
// path. Main transcripts are "<proj>/<sessionId>.jsonl"; subagents are
// "<proj>/<sessionId>/subagents/.../agent-<agentId>.jsonl". Returns false
// when the path is not a recognized transcript.
func transcriptForRel(root, rel string) (transcript, bool) {
	if !strings.HasSuffix(rel, transcriptExt) {
		return transcript{}, false
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	// Subagent: contains a "subagents" path segment.
	for i, p := range parts {
		if p == subagentsDir && i >= 2 {
			sessionID := parts[i-1]
			projParts := parts[:i-1]
			sessionDir := filepath.Join(append([]string{root}, append(projParts, sessionID)...)...)
			agentID := strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), transcriptExt)
			return transcript{
				rel:            rel,
				abs:            abs,
				nativeID:       childNativeID(sessionID, agentID),
				parentNativeID: sessionID,
				kind:           canonical.KindSubAgent,
				sessionDir:     sessionDir,
			}, true
		}
	}
	// Main transcript: basename minus extension is the sessionId. Its
	// sessionDir points at <projDir>/<sessionId>/ so the mapper can recover
	// the toolUseId→agentId map for Agent ops.
	sessionID := strings.TrimSuffix(base, transcriptExt)
	projParts := parts[:len(parts)-1]
	sessionDir := filepath.Join(append([]string{root}, append(projParts, sessionID)...)...)
	return transcript{
		rel:        rel,
		abs:        abs,
		nativeID:   sessionID,
		kind:       canonical.KindRoot,
		sessionDir: sessionDir,
	}, true
}

// hashFile returns the sha256 hex of a file's content, or ("", false) on
// error. Used by flushDirty to checkpoint meta-file state.
func hashFile(abs string) (string, bool) {
	raw, err := os.ReadFile(abs) // #nosec G304 -- path from watched tree under configured root
	if err != nil {
		return "", false
	}
	return hashBytes(raw), true
}
