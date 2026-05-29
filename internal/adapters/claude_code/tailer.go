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
	// Resolve the root through symlinks ONCE so every watched dir can be
	// checked for containment against it (spec §6.1, P2e): a symlinked
	// directory inside the tree that points outside the resolved root is
	// never Add()ed to the watcher.
	resolvedRoot, rrErr := filepath.EvalSymlinks(filepath.Clean(root))
	if rrErr != nil {
		onError(fmt.Errorf("claude_code: cannot resolve projects root %s; tail disabled for this source: %w", root, rrErr))
		return nil
	}
	watched := map[string]struct{}{}
	addWatchTree(watcher, resolvedRoot, root, watched, onError)

	dirty := make(map[string]struct{}, 16)
	metaDirty := make(map[string]struct{}, 16)
	// Loop-lifetime Agent-op deferral so a parent Agent op observed in one
	// flush is finalized when its child sidechain ends in another (spec §8.1).
	deferral := newTailDeferral()

	// Initial catch-up (spec §6.3, P2c): the watch is now established, but any
	// bytes appended to a known file BETWEEN Scan finishing and this point
	// arrived before the watch and would otherwise only be read on the next
	// WRITE event (which may never come for an idle session). Read every known
	// file from its cursor offset to current EOF once, up front, so the
	// Scan→Tail window loses nothing. Re-emission of an already-consumed line
	// is absorbed by the ingester's idempotent upserts.
	if perr := catchUpFromCursor(ctx, resolvedRoot, root, sourceID, &cur, deferral, out, onError); perr != nil {
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
			handleEvent(watcher, resolvedRoot, root, ev, watched, dirty, metaDirty, onError)
			if len(dirty)+len(metaDirty) >= debounceMaxEntries {
				if perr := flushDirty(ctx, root, sourceID, dirty, metaDirty, &cur, deferral, out, onError); perr != nil {
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
			if perr := flushDirty(ctx, root, sourceID, dirty, metaDirty, &cur, deferral, out, onError); perr != nil {
				return perr
			}
			dirty = make(map[string]struct{}, 16)
			metaDirty = make(map[string]struct{}, 16)
		case <-tick.C:
			// Re-walk to Add() any directory created since startup
			// (new project dir, new session dir, new subagents/ dir).
			addWatchTree(watcher, resolvedRoot, root, watched, onError)
			// Advance the deferral cycle and finalize any child that has sat at
			// EOF since an earlier cycle with no new append (quiescent, spec
			// §8.1, P2.4): a child appended in a prior flush but not since now
			// goes quiescent here and finalizes its parent Agent op.
			deferral.cycle++
			if perr := sweepQuiescentFinalizations(ctx, sourceID, deferral, out); perr != nil {
				return perr
			}
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return perr
			}
		}
	}
}

// catchUpFromCursor reads every currently-discovered transcript (and refreshes
// every changed meta) from its cursor offset to current EOF, once, at Tail
// startup (spec §6.3, P2c). It closes the Scan→Tail window: bytes appended
// before the watch was established are read here rather than waiting for a
// future WRITE event. It reuses flushDirty so the offset advance, partial-line
// hold-back, Agent-op deferral, and SourceProgress checkpoint are identical to
// the steady-state path. A file already fully consumed by Scan re-reads zero
// new bytes (offset == size) and emits nothing.
func catchUpFromCursor(ctx context.Context, resolvedRoot, root, sourceID string, cur *Cursor, def *tailDeferral, out chan<- canonical.Event, onError func(error)) error {
	transcripts, derr := discoverTranscripts(root, onError)
	if derr != nil {
		// A discovery failure is non-fatal for Tail: surface it and continue
		// into the watch loop (steady-state WRITE events still drive reads).
		onError(fmt.Errorf("claude_code: tail catch-up discovery: %w", derr))
		return nil
	}
	if len(transcripts) == 0 {
		return nil
	}
	dirty := make(map[string]struct{}, len(transcripts))
	for _, t := range transcripts {
		dirty[t.rel] = struct{}{}
	}
	// Refresh any meta whose content changed since the cursor's metaSeen so a
	// sidecar rewritten during the window is picked up (flushDirty skips
	// unchanged hashes). resolvedRoot is unused here (containment already
	// applied by discoverTranscripts) but kept in the signature so a future
	// meta-specific containment check has it.
	_ = resolvedRoot
	metaDirty := make(map[string]struct{})
	if hashes, herr := metaHashes(root, onError); herr == nil {
		for rel := range hashes {
			metaDirty[rel] = struct{}{}
		}
	}
	return flushDirty(ctx, root, sourceID, dirty, metaDirty, cur, def, out, onError)
}

// handleEvent classifies one fsnotify event. New directories are added to
// the watch set. Transcript writes mark the relative path dirty; meta-file
// writes mark the meta path dirty. Removes/renames are logged, not acted on
// (spec §6.2).
func handleEvent(watcher *fsnotify.Watcher, resolvedRoot, root string, ev fsnotify.Event, watched, dirty, metaDirty map[string]struct{}, onError func(error)) {
	// A newly created directory must be watched (fsnotify is non-recursive).
	// Files written into it BEFORE we Add() the watch would be missed, so we
	// also walk the new dir and mark any transcripts/metas already present as
	// dirty (race-window catch-up). Subsequent writes arrive via the watch.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			addWatchTree(watcher, resolvedRoot, ev.Name, watched, onError)
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
// watched set avoids the syscall churn on every tick. resolvedRoot is the
// symlink-resolved projects root: a directory that resolves outside it (a
// planted symlink escaping the source) is refused with a SourceError and not
// watched (spec §6.1, P2e). SkipDir prunes the escaping subtree from the walk.
func addWatchTree(watcher *fsnotify.Watcher, resolvedRoot, dir string, watched map[string]struct{}, onError func(error)) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if !withinSourceRoot(resolvedRoot, path, onError) {
			return filepath.SkipDir
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

// tailDeferral carries Agent-op deferral state across Tail flush cycles (spec
// §8.1, P1.2 + P2.4). The Tail loop owns one instance for its lifetime so a
// parent Agent op recorded in one flush is finalized when its child sidechain
// reaches a QUIESCENT EOF in a later cycle, regardless of arrival order. A
// child finalized once is removed so a subsequent append that re-opens the
// file does not re-finalize.
//
// Quiescence (P2.4): reaching byte-EOF on a child appended in the CURRENT flush
// is not semantic completion. A fully-read child is parked in childAtEOF with
// the cycle it was last dirtied; it is finalized only on a later sweep whose
// cycle is strictly greater (i.e. at least one flush/tick cycle elapsed with no
// new append). A continuously-appended child keeps bumping its cycle and never
// finalizes — consistent with the "always running" session model (§11.11).
type tailDeferral struct {
	// pending maps child native id → parent Agent op location (durable across
	// flushes so a parent recorded before its child completes is reachable).
	pending map[string]agentOpFinalize
	// childAtEOF maps a fully-read child native id → (end state, the cycle in
	// which it was last seen dirty). It is finalized on a sweep cycle strictly
	// later than lastDirtyCycle (quiescent).
	childAtEOF map[string]childAtEOFState
	// done records child native ids already finalized so a re-read does not
	// re-emit the finalize.
	done map[string]struct{}
	// cycle is a monotonic counter incremented once per flush and once per tick
	// sweep, used to judge quiescence.
	cycle int
}

// childAtEOFState pairs a fully-read child's end state with the deferral cycle
// in which it was last observed dirty (appended), so the sweep can tell a
// just-appended child (defer) from a quiescent one (finalize).
type childAtEOFState struct {
	end            childEndState
	lastDirtyCycle int
}

func newTailDeferral() *tailDeferral {
	return &tailDeferral{
		pending:    map[string]agentOpFinalize{},
		childAtEOF: map[string]childAtEOFState{},
		done:       map[string]struct{}{},
	}
}

// flushDirty re-reads every dirty transcript from its cursor offset and
// re-reads every changed meta file, updating the shared cursor. Emits a
// SourceProgress checkpoint at the end. Meta changes that introduce a new
// toolUseId→agentId mapping update only future Agent ops; already-emitted
// ops are not retro-patched (the resolver handles parent/child linkage via
// the structural path independently of toolUseId).
func flushDirty(ctx context.Context, root, sourceID string, dirty, metaDirty map[string]struct{}, cur *Cursor, def *tailDeferral, out chan<- canonical.Event, onError func(error)) error {
	// Record meta-file hashes first so a transcript flushed in the same
	// cycle picks up the freshest metaMap. A meta whose content hash is
	// unchanged from the cursor's metaSeen is skipped — a bare touch/mtime
	// bump does not warrant re-reading (spec §7 step 4).
	for rel := range metaDirty {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		// Containment guard on the Tail meta-hash read (spec §6.1, P1.3): a
		// .meta.json symlink planted in a watched dir after Tail starts must be
		// refused before its content is hashed/read.
		if !withinSourceRoot(root, abs, onError) {
			continue
		}
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
	childEnd := map[string]childEndState{}
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
				cached = readSessionMetas(root, t.sessionDir, onError)
				metaCache[t.sessionDir] = cached
			}
			mm = cached
		}
		fc := cur.fileCursor(rel)
		updated, _, mapper, rerr := readTranscript(ctx, root, t, sourceID, mm, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return rerr
			}
			onError(rerr)
			continue
		}
		*cur = cur.withFile(rel, updated)
		// Fold this file's Agent ops / child end-state into the loop-lifetime
		// deferral so a parent Agent op is finalized when its child reaches a
		// QUIESCENT EOF, in any flush order (spec §8.1, P1.2 + P2.4).
		if def != nil {
			collectAgentDeferral(mapper, t, def.pending, childEnd)
		}
	}
	if def != nil {
		// A child read in THIS flush was just appended (its WRITE made it
		// dirty), so it is parked at the current cycle, not finalized now; a
		// later sweep (this flush's tail, or a tick) finalizes it once it is
		// quiescent (spec §8.1, P2.4). The sweep also finalizes children parked
		// in an EARLIER cycle whose parent op is now known.
		def.cycle++
		parkChildEnds(def, childEnd)
		if perr := sweepQuiescentFinalizations(ctx, sourceID, def, out); perr != nil {
			return perr
		}
	}
	return emitProgress(ctx, sourceID, *cur, out)
}

// parkChildEnds records the fully-read children read in this flush as
// at-EOF-but-not-yet-quiescent, stamped with the current deferral cycle (spec
// §8.1, P2.4). Because these children were just read in THIS flush (their WRITE
// is what made them dirty), they are not finalized now; a later sweep whose
// cycle is strictly greater finalizes them, unless a new append re-stamps the
// cycle first. A child that is no longer fully read (parked partial line — the
// producer appended an in-flight record) is removed from childAtEOF so it
// reverts to running.
func parkChildEnds(def *tailDeferral, childEnd map[string]childEndState) {
	for childID, end := range childEnd {
		if !end.fullyRead {
			delete(def.childAtEOF, childID)
			continue
		}
		def.childAtEOF[childID] = childAtEOFState{end: end, lastDirtyCycle: def.cycle}
	}
}

// sweepQuiescentFinalizations finalizes parent Agent ops whose child sidechain
// has been at EOF since a STRICTLY EARLIER deferral cycle (quiescent — no new
// append in the current cycle, spec §8.1, P2.4) and whose parent Agent op has
// been observed (P1.2 durability). A child whose parent op is not yet known is
// left parked; the ingester resolver still repairs the structural child→parent
// op link (P1a), and the status finalize waits until the parent op is seen.
// Each finalized child is marked done and removed so a re-read does not
// re-finalize. Ordered for deterministic replay.
func sweepQuiescentFinalizations(ctx context.Context, sourceID string, def *tailDeferral, out chan<- canonical.Event) error {
	childIDs := make([]string, 0, len(def.childAtEOF))
	for childID := range def.childAtEOF {
		childIDs = append(childIDs, childID)
	}
	sort.Strings(childIDs)
	for _, childID := range childIDs {
		st := def.childAtEOF[childID]
		if st.lastDirtyCycle >= def.cycle {
			// Dirtied in the current cycle (just appended): not quiescent yet.
			continue
		}
		parent, ok := def.pending[childID]
		if !ok {
			// Parent Agent op not observed yet; keep parked until it is.
			continue
		}
		fin := agentFinalizeEvent(sourceID, parent.parentNativeID, parent.ref, st.end.lastTsUs, "completed")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- fin:
		}
		def.done[childID] = struct{}{}
		delete(def.childAtEOF, childID)
	}
	return nil
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
