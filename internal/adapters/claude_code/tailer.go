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
	// Seed its parked completions AND its already-finalized set from the persisted
	// cursor: a child that completed before its parent op was known — and was
	// checkpointed before a restart — is still finalizable when the parent op
	// appears (P2.4d), and a child already finalized in a previous lifetime (or
	// during Scan) is not re-finalized by the catch-up or the late-meta re-read
	// (P2.5c). Restore `finalized` FIRST so restoreParked's already-finalized guard
	// sees it.
	deferral := newTailDeferral()
	deferral.restoreFinalized(cur.finalizedSet())
	deferral.restoreParked(cur.Parked)

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
				if perr := flushDirty(ctx, resolvedRoot, root, sourceID, dirty, metaDirty, &cur, deferral, out, onError); perr != nil {
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
			if perr := flushDirty(ctx, resolvedRoot, root, sourceID, dirty, metaDirty, &cur, deferral, out, onError); perr != nil {
				return perr
			}
			dirty = make(map[string]struct{}, 16)
			metaDirty = make(map[string]struct{}, 16)
		case <-tick.C:
			// Re-walk to Add() any directory created since startup
			// (new project dir, new session dir, new subagents/ dir). The
			// Agent-op finalize is event-driven (paired in flushDirty when a
			// child completes), so the tick does no finalize work — it only
			// rescans for new dirs and checkpoints progress (spec §8.1).
			addWatchTree(watcher, resolvedRoot, root, watched, onError)
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
// hold-back, Agent-op deferral rebuild, and SourceProgress checkpoint are
// identical to the steady-state path. A file already fully consumed by Scan
// re-reads zero new bytes (offset == size) and emits nothing (including no
// re-finalize — its terminal record is below the resume offset, §8.1).
// resolvedRoot is the symlink-resolved projects root, threaded into the meta
// containment checks so they do not re-resolve the root per file (P2-perf).
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
	// unchanged hashes via metaSeen).
	metaDirty := make(map[string]struct{})
	if hashes, herr := metaHashes(root, resolvedRoot, onError); herr == nil {
		for rel := range hashes {
			metaDirty[rel] = struct{}{}
		}
	}
	return flushDirty(ctx, resolvedRoot, root, sourceID, dirty, metaDirty, cur, def, out, onError)
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
// §8.1). The Tail loop owns one instance for its lifetime so a parent Agent op
// recorded in one flush is finalized when its child sidechain is observed
// COMPLETE in another, regardless of arrival order, and exactly once.
//
// Completion is the §485 terminal-assistant-text marker, observed NEWLY this
// pass (the terminal record's line began at or after emitFrom). A child marked
// completed but whose parent Agent op is not yet known is parked in `completed`
// until the parent op is observed (event-driven pairing — no ticks, no
// quiescence window). A child finalized once is recorded in `finalized`, which
// is READ before emitting, so a re-read / catch-up replay emits no second
// finalize.
type tailDeferral struct {
	// pending maps child native id → parent Agent op location (durable across
	// flushes so a parent recorded before its child completes is reachable).
	// Rebuilt on every read, including the emit-suppressed replay, so it
	// survives the Scan→Tail boundary.
	pending map[string]agentOpFinalize
	// completed maps a child native id observed complete (terminal
	// assistant-text, newly read) → its completion state, for children whose
	// parent Agent op is not yet finalized. Drained as parents are paired.
	completed map[string]completionState
	// finalized records child native ids already finalized so a re-read does
	// NOT re-emit the finalize (read before every emit).
	finalized map[string]struct{}
}

// completionState records the completion timestamp of a child observed complete
// (the terminal assistant-text record's ts), used as the Agent-op finalize
// EndTs (§8.1).
type completionState struct {
	tsUs int64
}

func newTailDeferral() *tailDeferral {
	return &tailDeferral{
		pending:   map[string]agentOpFinalize{},
		completed: map[string]completionState{},
		finalized: map[string]struct{}{},
	}
}

// parkedSnapshot projects the deferral's `completed` set into the cursor's
// `parked` shape (child native id → completion ts micros) for checkpointing
// (spec §8.1, P2.4d). It is a pure read of the live set.
func (d *tailDeferral) parkedSnapshot() map[string]int64 {
	if d == nil {
		return nil
	}
	out := make(map[string]int64, len(d.completed))
	for childID, st := range d.completed {
		out[childID] = st.tsUs
	}
	return out
}

// restoreParked seeds the deferral's `completed` set from a cursor's persisted
// `parked` map (spec §8.1, P2.4d), so a child that completed before its parent
// op was known — and was checkpointed before a daemon restart — is still
// finalizable when the parent op appears after the restart. An already-finalized
// child is not restored (it is not in `parked`, which is a snapshot of the live
// `completed` set that drops finalized children). Existing in-memory entries are
// preserved (restore only adds). Callers that also restore `finalized` should do
// so FIRST so the already-finalized guard here sees the restored set.
func (d *tailDeferral) restoreParked(parked map[string]int64) {
	if d == nil {
		return
	}
	for childID, tsUs := range parked {
		if _, done := d.finalized[childID]; done {
			continue
		}
		if _, present := d.completed[childID]; present {
			continue
		}
		d.completed[childID] = completionState{tsUs: tsUs}
	}
}

// restoreFinalized seeds the deferral's `finalized` set from a cursor's persisted
// `finalized` slice (spec §8.1, P2.5c), so a finalize emitted in a previous
// process lifetime (or during Scan) is not re-emitted by a Tail catch-up or the
// late-meta child re-read. Restore only adds; existing entries are preserved.
func (d *tailDeferral) restoreFinalized(finalized map[string]struct{}) {
	if d == nil {
		return
	}
	for childID := range finalized {
		d.finalized[childID] = struct{}{}
	}
}

// finalizedSnapshot returns a copy of the deferral's `finalized` set for
// checkpointing into the cursor (spec §8.1, P2.5c). A pure read of the live set.
func (d *tailDeferral) finalizedSnapshot() map[string]struct{} {
	if d == nil {
		return nil
	}
	out := make(map[string]struct{}, len(d.finalized))
	for childID := range d.finalized {
		out[childID] = struct{}{}
	}
	return out
}

// flushDirty re-reads every dirty transcript from its cursor offset and
// re-reads every changed meta file, updating the shared cursor. Emits a
// SourceProgress checkpoint at the end. A meta change additionally forces a
// re-read of the affected session's parent transcript(s) FROM OFFSET 0 WITH
// EMISSION (spec §8.1) so a late `.meta.json` re-emits the parent's `Agent`
// OpStarted carrying the now-resolved ChildSessionNativeID (idempotent UPDATE
// at the ingester); the resolver then links the child.
func flushDirty(ctx context.Context, resolvedRoot, root, sourceID string, dirty, metaDirty map[string]struct{}, cur *Cursor, def *tailDeferral, out chan<- canonical.Event, onError func(error)) error {
	// Record meta-file hashes first so a transcript flushed in the same
	// cycle picks up the freshest metaMap. A meta whose content hash is
	// unchanged from the cursor's metaSeen is skipped — a bare touch/mtime
	// bump does not warrant re-reading (spec §7 step 4). changedMetas collects
	// the metas whose content actually changed this flush, so only those drive
	// a parent re-read (a no-op touch must not re-emit the parent).
	changedMetas := make(map[string]struct{}, len(metaDirty))
	for rel := range metaDirty {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		// Containment guard on the Tail meta-hash read (spec §6.1, P1.3, P2.5a): a
		// .meta.json symlink planted in a watched dir after Tail starts must be
		// refused before its content is hashed/read, and the hash must open the
		// RESOLVED path the guard returns (no TOCTOU), not the original. resolvedRoot
		// is pre-resolved so this does not re-run EvalSymlinks on the root per meta.
		resolvedAbs, ok, cerr := withinResolvedRoot(resolvedRoot, abs)
		if cerr != nil {
			onError(fmt.Errorf("claude_code: cannot resolve meta %s for containment; skipping: %w", rel, cerr))
			continue
		}
		if !ok {
			onError(fmt.Errorf("claude_code: meta %s resolves outside the projects root; skipping (symlink escape)", rel))
			continue
		}
		h, herr := hashFile(resolvedAbs)
		if herr != nil {
			// A present-but-unreadable meta silently failing the rewrite-detection
			// would mask a broken sidecar whose rewrite should drive the late-meta
			// linkage repair; surface it (P2.5a, no silent failure).
			onError(fmt.Errorf("claude_code: hash meta %s: %w", rel, herr))
			continue
		}
		if cur.metaSeen(rel) == h {
			continue
		}
		*cur = cur.withMetaSeen(rel, h)
		changedMetas[rel] = struct{}{}
	}

	// A changed meta repairs the linkage of its session's parent transcript AND
	// the child subagent's AgentName: force BOTH re-read from offset 0 with
	// emission (spec §8.1, late meta). The parent re-emit carries the resolved
	// ChildSessionNativeID; the child re-emit carries the agentType AgentName
	// (P2.4c) — a child read before its meta would otherwise keep an empty
	// AgentName forever. The forced files join the dirty set (so they are read
	// this flush) and forceFromZero (so they are read with emitFrom=0, not the
	// cursor).
	forceFromZero := metaParentRels(root, changedMetas)
	for rel := range metaChildRels(root, changedMetas) {
		forceFromZero[rel] = struct{}{}
	}
	dirtyAll := dirty
	if len(forceFromZero) > 0 {
		dirtyAll = make(map[string]struct{}, len(dirty)+len(forceFromZero))
		for rel := range dirty {
			dirtyAll[rel] = struct{}{}
		}
		for rel := range forceFromZero {
			dirtyAll[rel] = struct{}{}
		}
	}

	if len(dirtyAll) == 0 {
		if len(metaDirty) == 0 {
			return nil
		}
		return emitProgress(ctx, sourceID, *cur, out)
	}

	names := make([]string, 0, len(dirtyAll))
	for n := range dirtyAll {
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
				cached = readSessionMetas(resolvedRoot, t.sessionDir, onError)
				metaCache[t.sessionDir] = cached
			}
			mm = cached
		}
		fc := cur.fileCursor(rel)
		if _, force := forceFromZero[rel]; force {
			// Re-emit the parent from the start so its Agent OpStarted carries
			// the resolved ChildSessionNativeID. A zero cursor reads 0→EOF with
			// emitFrom=0 (everything emitted, all idempotent upserts). The parent
			// has no child sidechain records, so this re-read marks no child
			// completed — Part 1's finalize gating is unaffected.
			fc = FileCursor{}
		}
		updated, _, mapper, rerr := readTranscript(ctx, root, t, sourceID, mm, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return rerr
			}
			onError(rerr)
			continue
		}
		*cur = cur.withFile(rel, updated)
		// Fold this file's Agent ops (durable parent refs) and, when its
		// terminal record is a NEWLY-read assistant-text marker, its completion
		// into the loop-lifetime deferral (spec §8.1).
		if def != nil {
			collectAgentDeferral(mapper, t, def.pending, def.completed)
		}
	}
	if def != nil {
		// Pair every newly-completed child with its parent Agent op (when known)
		// and emit the finalize exactly once (spec §8.1). Children whose parent
		// op is not yet observed stay parked in `completed`.
		if perr := pairCompletedFinalizations(ctx, sourceID, def, out); perr != nil {
			return perr
		}
		// Checkpoint the surviving parked completions AND the finalized set into the
		// cursor so a restart restores them (P2.4d, P2.5c).
		// pairCompletedFinalizations already dropped the finalized children from
		// def.completed (so the parked snapshot is exactly the children still
		// awaiting their parent op) and added them to def.finalized (so the
		// finalized snapshot persists the no-re-finalize guard).
		*cur = cur.withParked(def.parkedSnapshot())
		*cur = cur.withFinalized(def.finalizedSnapshot())
	}
	return emitProgress(ctx, sourceID, *cur, out)
}

// metaParentRels maps each changed `.meta.json` relative path to its session's
// parent root-transcript relative path ("<proj>/<sessionId>.jsonl"), the file
// whose `Agent` OpStarted must be re-emitted to repair a late linkage (spec
// §8.1). A meta lives at "<proj>/<sessionId>/subagents/.../agent-<id>.meta.json";
// the parent transcript is "<proj>/<sessionId>.jsonl". The parent must exist on
// disk and be containment-safe (the re-read itself re-checks containment). A
// rel that is not a subagent meta path is skipped.
func metaParentRels(root string, changedMetas map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for rel := range changedMetas {
		parentRel, ok := metaParentRel(rel)
		if !ok {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(parentRel))
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			// Orphan-root session (no parent .jsonl) or a not-yet-created
			// parent: nothing to re-emit. The structural linkage still works
			// once the parent appears and is read normally.
			continue
		}
		out[parentRel] = struct{}{}
	}
	return out
}

// metaChildRels maps each changed `.meta.json` relative path to its OWN child
// subagent-transcript relative path (the sibling `agent-<id>.jsonl`), the file
// whose `SessionStarted` must be re-emitted so the child's `AgentName` picks up
// the now-known `agentType` (spec §8.1, P2.4c). The child path is the meta path
// with the `.meta.json` suffix replaced by `.jsonl`. The child must exist on
// disk (the re-read itself re-checks containment). A rel that is not a subagent
// meta path, or whose sibling transcript is absent, is skipped.
func metaChildRels(root string, changedMetas map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for rel := range changedMetas {
		childRel, ok := metaChildRel(rel)
		if !ok {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(childRel))
		if info, err := os.Stat(abs); err != nil || info.IsDir() {
			// The child sidechain is not present yet: nothing to re-read. It will
			// be read normally (with the now-known meta) when it appears.
			continue
		}
		out[childRel] = struct{}{}
	}
	return out
}

// metaChildRel derives the sibling child-transcript rel from a subagent meta
// rel: "<...>/agent-<id>.meta.json" → "<...>/agent-<id>.jsonl". Returns
// ("", false) when rel is not a subagent meta path.
func metaChildRel(metaRel string) (string, bool) {
	if !strings.HasSuffix(metaRel, metaExt) {
		return "", false
	}
	// Confirm it is a subagent meta (has a "subagents" segment with a session-id
	// parent), reusing metaParentRel's structural check.
	if _, ok := metaParentRel(metaRel); !ok {
		return "", false
	}
	return strings.TrimSuffix(metaRel, metaExt) + transcriptExt, true
}

// metaParentRel derives the parent root-transcript rel from a subagent meta
// rel. "<proj>/<sessionId>/subagents/.../agent-<id>.meta.json" →
// "<proj>/<sessionId>.jsonl". Returns ("", false) when rel has no "subagents"
// segment with a session-id parent.
func metaParentRel(metaRel string) (string, bool) {
	if !strings.HasSuffix(metaRel, metaExt) {
		return "", false
	}
	parts := strings.Split(metaRel, "/")
	for i, p := range parts {
		if p == subagentsDir && i >= 2 {
			sessionID := parts[i-1]
			projParts := parts[:i-1]
			rel := strings.Join(append(append([]string{}, projParts...), sessionID+transcriptExt), "/")
			return rel, true
		}
	}
	return "", false
}

// pairCompletedFinalizations emits the deferred OpFinalizedEvent for every
// child observed complete (in `def.completed`) whose parent Agent op is known
// (in `def.pending`), exactly once (spec §8.1). It READS `def.finalized` first
// so an already-finalized child is never re-emitted, records each emitted child
// in `finalized`, and drops it from `completed`. A completed child whose parent
// op is not yet observed is left parked in `completed` (finalized in a later
// flush once the parent op is read). Ordered by child native id for
// deterministic replay.
func pairCompletedFinalizations(ctx context.Context, sourceID string, def *tailDeferral, out chan<- canonical.Event) error {
	childIDs := make([]string, 0, len(def.completed))
	for childID := range def.completed {
		childIDs = append(childIDs, childID)
	}
	sort.Strings(childIDs)
	for _, childID := range childIDs {
		if _, done := def.finalized[childID]; done {
			delete(def.completed, childID)
			continue
		}
		parent, ok := def.pending[childID]
		if !ok {
			// Parent Agent op not observed yet; keep parked until it is.
			continue
		}
		st := def.completed[childID]
		fin := agentFinalizeEvent(sourceID, parent.parentNativeID, parent.ref, st.tsUs, "completed")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- fin:
		}
		def.finalized[childID] = struct{}{}
		delete(def.completed, childID)
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

// hashFile returns the sha256 hex of a file's content. It MUST be given a
// containment-checked, symlink-RESOLVED path (the caller resolves via
// withinResolvedRoot and passes the resolved path, P2.5a — no TOCTOU). A read
// error is RETURNED (not swallowed) so the caller can surface a SourceError
// rather than silently skipping a present-but-broken meta (spec §6.1). Used by
// flushDirty to checkpoint meta-file state.
func hashFile(resolvedAbs string) (string, error) {
	raw, err := os.ReadFile(resolvedAbs) // #nosec G304 -- reading the containment-checked RESOLVED path (withinResolvedRoot) from the watched tree under the configured root
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}
