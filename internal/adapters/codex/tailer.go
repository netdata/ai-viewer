package codex

import (
	"context"
	"time"

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
	return tailLoopWithHeartbeat(ctx, root, sourceID, cur, out, onError, nil)
}

func tailLoopWithHeartbeat(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error), tailHeartbeat func()) error {
	runtime, err := newTailRuntime(ctx, root, sourceID, cur, out, onError, tailHeartbeat)
	if err != nil || runtime == nil {
		return err
	}
	defer runtime.close()
	return runtime.run()
}
