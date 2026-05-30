package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file holds resolveRootID — the parent_id chain walk that resolves a
// session's TRUE tree root (SOW-0005 P2.4). Split out of store_load.go to keep
// each file ≤400 lines.

// rootChainCap bounds the parent_id chain walk in resolveRootID: a defensive
// depth cap so a cyclic or pathological parent chain can never loop forever. The
// deepest observed opencode sub-agent nesting is a few levels; 32 is far beyond
// any real tree (adapter-opencode.md §"Read Strategy" → nested root).
const rootChainCap = 32

// resolveRootID walks the session's parent_id chain to the TOPMOST ancestor (the
// true root of the whole session tree), so a nested sub-agent's RootNativeID is
// the tree root rather than its direct parent (SOW-0005 P2.4). For a root session
// (no parent) it returns the session's own id. The walk is read-only, depth-capped
// (rootChainCap) with a seen-set cycle guard. If the chain cannot be fully
// resolved — a missing ancestor row, a cycle, or the cap is hit — it FALLS BACK to
// the last known ancestor (the deepest id it did resolve, i.e. the direct parent
// for a one-step failure) and surfaces one WARN via onError, never blocking the
// session. Only the id+parent_id columns are read (the cheapest possible probe).
func resolveRootID(ctx context.Context, db *sql.DB, id, parentID string, onError func(error)) string {
	if parentID == "" {
		return id // already the root
	}
	seen := map[string]struct{}{id: {}}
	cur := parentID
	for depth := 0; depth < rootChainCap; depth++ {
		if _, dup := seen[cur]; dup {
			onError(fmt.Errorf("opencode: parent_id cycle resolving root for session %s (stopping at %s)", id, cur))
			return cur
		}
		seen[cur] = struct{}{}

		var parent sql.NullString
		err := db.QueryRowContext(ctx, `SELECT parent_id FROM session WHERE id = ?`, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			// Ancestor row not present (yet) — cur is the furthest resolvable
			// ancestor. Fall back to it (the direct parent on a one-step failure).
			onError(fmt.Errorf("opencode: parent session %s of %s not found; using it as root", cur, id))
			return cur
		}
		if err != nil {
			onError(fmt.Errorf("opencode: resolve root for session %s at %s: %w", id, cur, err))
			return cur
		}
		if !parent.Valid || parent.String == "" {
			return cur // cur is the root (no further parent)
		}
		cur = parent.String
	}
	onError(fmt.Errorf("opencode: parent_id chain for session %s exceeded depth %d; using %s as root", id, rootChainCap, cur))
	return cur
}
