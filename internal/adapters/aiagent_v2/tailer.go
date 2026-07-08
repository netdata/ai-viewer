package aiagent_v2

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

func tailLoopWithHeartbeat(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error), tailHeartbeat func(), catchUp bool) error {
	if tailHeartbeat == nil {
		tailHeartbeat = func() {}
	}
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("aiagent_v2: fsnotify watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// security.md §"Hard Rules" #1 — read-only on sources. No code path
	// writes to a source tree, NOT EVEN the watch root. If the
	// directory does not exist we surface a SourceErrorEvent via
	// onError (lifts to /api/health.parse_errors) and return cleanly:
	// the adapter has nothing to watch, but the daemon keeps running
	// for any other configured source.
	if _, statErr := os.Stat(root); statErr != nil {
		onError(fmt.Errorf("aiagent_v2: watch root %s not present (read-only on sources, no mkdir): %w", root, statErr))
		return nil
	}
	if addErr := watcher.Add(root); addErr != nil {
		return fmt.Errorf("aiagent_v2: watch %s: %w", root, addErr)
	}

	if catchUp {
		if perr := catchUpFromCursor(ctx, root, sourceID, &cur, out, onError); perr != nil {
			return perr
		}
		tailHeartbeat()
	}

	dirty := make(map[string]struct{}, 16)
	debounce := time.NewTimer(debounceWindow)
	defer debounce.Stop()
	if !debounce.Stop() {
		<-debounce.C
	}
	tick := time.NewTicker(tailTickInterval)
	defer tick.Stop()
	// lastEmittedFileCount is the cursor.Files size we last emitted a
	// checkpoint for. Tail ticks without a real change (no fsnotify
	// events, no debounce flush) emit nothing — for a source with 482k
	// files, the cursor JSON is ~9 KB, so emitting it every 5 s is ~6 MB
	// / min of allocation pressure that GC has to chase (SOW-0094).
	lastEmittedFileCount := len(cur.Files)

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
				tailHeartbeat()
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
			tailHeartbeat()
			dirty = make(map[string]struct{}, 16)
		case <-tick.C:
			// Only emit a checkpoint on the tail tick if the cursor has
			// changed since the last emit. For sources with many files
			// (e.g. aiagent_v2 has 482k sessions, cursor ≈ 9 KB JSON),
			// emitting the full cursor every 5 s allocates ~6 MB / min and
			// dominated the heap profile at 4 GB RSS after a few hours
			// (SOW-0094 root cause). The cursor only changes when a file
			// is added or removed; idle tail ticks are pure overhead.
			if len(cur.Files) != lastEmittedFileCount {
				if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
					return perr
				}
				lastEmittedFileCount = len(cur.Files)
			}
			tailHeartbeat()
		}
	}
}

func catchUpFromCursor(ctx context.Context, root, sourceID string, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	files, err := listSnapshots(root)
	if err != nil {
		return err
	}
	dirty := make(map[string]struct{}, len(files))
	for _, name := range files {
		dirty[name] = struct{}{}
	}
	return flushDirty(ctx, root, sourceID, dirty, cur, out, onError)
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
		return nil //nolint:nilerr // intentional: file rotated/removed between read and post-stat is transient, not fatal — the change already processed above stands
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

// sortStrings sorts the name list. SOW-0118: this was an insertion sort with
// a "<< 16" assumption, but catchUpFromCursor feeds it the FULL snapshot list
// (615 K files on the operator's corpus) on every Tail start/restart — O(n²)
// ≈ 380 billion comparisons, a dominant single-core cost (46% of a CPU
// profile). Use the stdlib O(n log n) sort; the small debounce-dirty sets from
// watcher events are also fine through it. (listSnapshots already returns a
// sorted slice, so catchUp's input happens to be pre-sorted, but flushDirty is
// a shared path that can receive unsorted input, so the sort stays.)
func sortStrings(s []string) {
	sort.Strings(s)
}
