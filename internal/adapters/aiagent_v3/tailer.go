package aiagent_v3

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

// debounceWindow is the coalescing window applied per file when multiple
// fsnotify Write events arrive in rapid succession. Spec §6.3.
const debounceWindow = 50 * time.Millisecond

// debounceMaxEntries bounds the per-file debounce map. If exceeded, the
// debouncer forces a flush so we never accumulate unbounded memory in
// pathological burst scenarios.
const debounceMaxEntries = 1024

// tailTickInterval drives the periodic SourceProgress emission so the
// cursor persists even when no fsnotify events arrive. Matches spec
// §6.2 "5s tick".
const tailTickInterval = 5 * time.Second

// tailLoop runs the fsnotify event loop until ctx is cancelled. The
// adapter's Tail method delegates here. The function owns the watcher
// lifecycle: it is created here and closed via defer on return.
func tailLoop(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error)) error {
	if cur.Files == nil {
		cur = newCursor()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("aiagent_v3: fsnotify watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	watchDir := filepath.Join(root, sessionDir)
	// 0o750 keeps the operator's source tree restrictive (gosec G301):
	// owner full, group read+exec only, world none. We create the dir
	// defensively when the operator points the ingester at a fresh
	// sessions-dir; the writer (ai-agent) sets its own perms on session
	// files. AGENTS.md "read-only on sources" applies to file content,
	// not to creating the parent directory that the watcher attaches to.
	if mkErr := os.MkdirAll(watchDir, 0o750); mkErr != nil {
		return fmt.Errorf("aiagent_v3: ensure %s: %w", watchDir, mkErr)
	}
	if addErr := watcher.Add(watchDir); addErr != nil {
		return fmt.Errorf("aiagent_v3: watch %s: %w", watchDir, addErr)
	}

	dirty := make(map[string]struct{}, 16)
	debounce := time.NewTimer(debounceWindow)
	defer debounce.Stop()
	// Initial state: timer is running but no files dirty. Drain to make
	// it idle until the first event arrives.
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
			name := tailableName(watchDir, ev)
			if name == "" {
				continue
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				onError(fmt.Errorf("aiagent_v3: ledger %s removed/renamed", name))
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
			onError(fmt.Errorf("aiagent_v3: watcher: %w", err))
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

// tailableName returns the file basename if the fsnotify event targets a
// .jsonl ledger under watchDir, or "" if the event is irrelevant
// (different extension, .tmp file, directory event, etc.).
func tailableName(watchDir string, ev fsnotify.Event) string {
	name := filepath.Base(ev.Name)
	if !strings.HasSuffix(name, ledgerExt) {
		return ""
	}
	if strings.Contains(name, ".tmp-") {
		return ""
	}
	// Defensive: ensure the event's directory matches the watched dir.
	if filepath.Dir(ev.Name) != watchDir {
		return ""
	}
	return name
}

// resetDebounce restarts the debounce timer for one debounceWindow.
// Drains any pending tick first so the next fire is exactly one window
// from now.
func resetDebounce(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(debounceWindow)
}

// flushDirty re-reads every dirty file from its cursor offset and
// updates the shared cursor. Emits a SourceProgress checkpoint at the
// end so the ingester observes the new high-water-mark even if no
// downstream consumer is ticking.
func flushDirty(ctx context.Context, root, sourceID string, dirty map[string]struct{}, cur *Cursor, out chan<- canonical.Event, onError func(error)) error {
	if len(dirty) == 0 {
		return nil
	}
	names := make([]string, 0, len(dirty))
	for n := range dirty {
		names = append(names, n)
	}
	// Stable order across flushes — keeps test assertions deterministic
	// and produces consistent SourceProgress payloads.
	sortStrings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := filepath.Join(root, sessionDir, name)
		fc := cur.fileCursor(name)
		updated, _, rerr := readFile(ctx, full, sourceID, root, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return rerr
			}
			onError(rerr)
			continue
		}
		*cur = cur.withFile(name, updated)
	}
	return emitProgress(ctx, sourceID, *cur, out)
}

// sortStrings is a tiny indirection to keep the dependency surface of
// tailer.go small (it doesn't otherwise need `sort`).
func sortStrings(s []string) {
	// Simple insertion sort — names lists are tiny (≤ debounceMaxEntries
	// per flush, typically << 16). Avoids pulling in sort just for this.
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j-1] > s[j] {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}
