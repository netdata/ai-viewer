package claude_code

import (
	"context"
	"maps"
	"slices"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// tailDeferral carries Agent-op deferral state across Tail flush cycles.
type tailDeferral struct {
	pending   map[string]agentOpFinalize
	completed map[string]completionState
	finalized map[string]struct{}
}

// completionState records the completion timestamp of a child observed complete.
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

func (d *tailDeferral) restoreParked(parked map[string]int64) {
	if d == nil {
		return
	}
	for childID, tsUs := range parked {
		d.restoreParkedChild(childID, tsUs)
	}
}

func (d *tailDeferral) restoreParkedChild(childID string, tsUs int64) {
	if _, done := d.finalized[childID]; done {
		return
	}
	if _, present := d.completed[childID]; present {
		return
	}
	d.completed[childID] = completionState{tsUs: tsUs}
}

func (d *tailDeferral) restoreFinalized(finalized map[string]struct{}) {
	if d == nil {
		return
	}
	for childID := range finalized {
		d.finalized[childID] = struct{}{}
	}
}

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

// pairCompletedFinalizations emits deferred Agent OpFinalizedEvent values in a
// deterministic child-native-id order.
func pairCompletedFinalizations(ctx context.Context, sourceID string, def *tailDeferral, out chan<- canonical.Event) error {
	for _, childID := range slices.Sorted(maps.Keys(def.completed)) {
		if err := pairCompletedFinalization(ctx, sourceID, def, out, childID); err != nil {
			return err
		}
	}
	return nil
}

func pairCompletedFinalization(ctx context.Context, sourceID string, def *tailDeferral, out chan<- canonical.Event, childID string) error {
	if _, done := def.finalized[childID]; done {
		delete(def.completed, childID)
		return nil
	}
	parent, ok := def.pending[childID]
	if !ok {
		return nil
	}
	st := def.completed[childID]
	fin := agentFinalizeEvent(sourceID, parent.parentNativeID, parent.ref, st.tsUs, "completed")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- fin:
		def.finalized[childID] = struct{}{}
		delete(def.completed, childID)
		return nil
	}
}
