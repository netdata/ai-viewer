package claude_code

import (
	"context"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// debounceWindow coalesces rapid Write events per flush cycle (spec §6.4).
const debounceWindow = 50 * time.Millisecond

// debounceMaxEntries bounds the dirty set before a forced flush.
const debounceMaxEntries = 4096

// tailTickInterval drives periodic SourceProgress emission and rescans for
// new directories that fsnotify (non-recursive on Linux) may have missed.
const tailTickInterval = 5 * time.Second

func tailLoopWithHeartbeat(ctx context.Context, root, sourceID string, cur Cursor, out chan<- canonical.Event, onError func(error), tailHeartbeat func()) error {
	runtime, err := newTailRuntime(ctx, root, sourceID, cur, out, onError, tailHeartbeat)
	if err != nil || runtime == nil {
		return err
	}
	defer runtime.close()
	return runtime.run()
}
