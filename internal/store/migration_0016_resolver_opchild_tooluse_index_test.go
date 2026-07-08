package store_test

import (
	"context"
	"testing"
)

// Migration 0016 (SOW-0117) adds idx_sessions_link_tooluse, the sessions-side
// index that backs linkOpChildrenByToolUse's correlated EXISTS subquery (the
// op side is idx_ops_link_tooluse from migration 0015). Without it the EXISTS
// JSON-scans every session in the op's source per candidate op (~1.5 s/tick on
// the production DB); with it the planner seeks the matching child directly.
//
// There is intentionally no EXPLAIN-usage test here: the planner's choice
// between idx_sessions_link_tooluse and a sessions scan depends on table
// cardinality, and on the tiny synthetic test DB it correctly prefers a scan.
// At production scale (306 K sessions) the planner DOES seek the index —
// verified directly on the real store, where the EXISTS dropped 1.49 s -> 6 ms
// and EXPLAIN shows `SEARCH c USING INDEX idx_sessions_link_tooluse` (recorded
// in SOW-0117). The index-existence + partial-predicate contract below is the
// reliable invariant; a small-data EXPLAIN test would be a misleading proxy.

func TestMigration0016_SessionsToolUseIndexExists(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	var sqlText string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_sessions_link_tooluse'`,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read idx_sessions_link_tooluse: %v", err)
	}
	if !indexColumnsMatch(sqlText, "sessions(json_extract(extras_json, '$.aiViewer.toolUseId'))") {
		t.Errorf("idx_sessions_link_tooluse shape drift: %q", sqlText)
	}
	// Must be partial (WHERE … IS NOT NULL) so only stashed rows are indexed.
	var partial int
	if err := db.QueryRowContext(ctx,
		`SELECT partial FROM pragma_index_list('sessions') WHERE name='idx_sessions_link_tooluse'`,
	).Scan(&partial); err != nil {
		t.Fatalf("read partial flag: %v", err)
	}
	if partial == 0 {
		t.Errorf("idx_sessions_link_tooluse: want partial=1, got 0")
	}
}
