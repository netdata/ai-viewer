package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Migration 0015 (SOW-0117) adds partial expression indexes that back the
// ingest resolver's json_extract link predicates. Without them the resolver's
// four UPDATE…RETURNING passes full-scan every NULL-link op/session and parse
// the extras_json blob on each row every 5 s tick — the unconditional ~1-core
// idle CPU burn this migration fixes.
//
// This file pins:
//   - The four indexes exist and carry the expected partial-expression shape
//     (the expression column reports as "" in PRAGMA index_info).
//   - The two ops resolver predicates use the new indexes (EXPLAIN QUERY PLAN
//     shows USING INDEX), proving the planner can seek the tiny partial index
//     instead of scanning the full ops table.

func TestMigration0015_ResolverLinkIndexesExist(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	// The single expression column reports as "" in PRAGMA index_info (the
	// cid is -1 / name NULL for an expression), mirroring idx_sessions_tokens.
	// indexColumnsMatch compares the raw CREATE INDEX SQL, so we assert the
	// expression text is present.
	want := map[string]string{
		"idx_sessions_link_parent": "sessions(json_extract(extras_json, '$.aiViewer.parentNativeId'))",
		"idx_sessions_link_root":   "sessions(json_extract(extras_json, '$.aiViewer.rootNativeId'))",
		"idx_ops_link_child":       "ops(json_extract(extras_json, '$.aiViewer.childNativeId'))",
		"idx_ops_link_tooluse":     "ops(json_extract(extras_json, '$.aiViewer.toolUseId'))",
	}
	for name, wantCols := range want {
		var sql string
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type='index' AND name = ?`, name,
		).Scan(&sql); err != nil {
			t.Fatalf("read sqlite_master for %s: %v", name, err)
		}
		if !indexColumnsMatch(sql, wantCols) {
			t.Errorf("index %s: want %q, got CREATE INDEX sql %q", name, wantCols, sql)
		}
		// Each must be a PARTIAL index: the resolver predicate is the partial
		// WHERE that keeps the index tiny (only stashed rows are indexed).
		var partial int
		if err := db.QueryRowContext(ctx,
			`SELECT partial FROM pragma_index_list('sessions') WHERE name = ?
             UNION ALL
             SELECT partial FROM pragma_index_list('ops') WHERE name = ?`,
			name, name).Scan(&partial); err != nil {
			t.Fatalf("read partial flag for %s: %v", name, err)
		}
		if partial == 0 {
			t.Errorf("index %s: want partial=1 (WHERE … IS NOT NULL), got partial=0", name)
		}
	}
}

// TestMigration0015_ResolverOpsQueriesUseIndexes proves the optimizer seeks the
// tiny partial expression indexes (only the stashed rows) instead of scanning
// the full ops table. This is the core correctness claim of SOW-0117: the two
// ops resolver passes go from O(all-NULL-child ops) full scan to O(matched)
// index seek. It mirrors migration_0013's EXPLAIN-based index-usage test.
func TestMigration0015_ResolverOpsQueriesUseIndexes(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()
	seedResolverLinkPlanRows(t, db, ctx)

	// ANALYZE so the planner has stats; without it the planner may default to a
	// scan on a near-empty table. On the production DB (5 M ops, ~3 K stashed)
	// the index win is overwhelming; this test pins that the planner CAN pick it.
	for _, tbl := range []string{"ops", "sessions"} {
		if _, err := db.ExecContext(ctx, `ANALYZE `+tbl); err != nil {
			t.Fatalf("ANALYZE %s: %v", tbl, err)
		}
	}

	childNativePlan := explainPlan(t, db, ctx, `
		EXPLAIN QUERY PLAN
		SELECT COUNT(*) FROM ops
		WHERE ops.child_session_id IS NULL
		  AND json_extract(ops.extras_json, '$.aiViewer.childNativeId') IS NOT NULL
		  AND json_extract(ops.extras_json, '$.aiViewer.childNativeId') <> ''
	`)
	if !strings.Contains(childNativePlan, "idx_ops_link_child") {
		t.Fatalf("resolver childNativeId predicate does not use idx_ops_link_child.\nPlan:\n%s", childNativePlan)
	}

	toolUsePlan := explainPlan(t, db, ctx, `
		EXPLAIN QUERY PLAN
		SELECT COUNT(*) FROM ops
		WHERE ops.child_session_id IS NULL
		  AND json_extract(ops.extras_json, '$.aiViewer.toolUseId') IS NOT NULL
		  AND json_extract(ops.extras_json, '$.aiViewer.toolUseId') <> ''
	`)
	if !strings.Contains(toolUsePlan, "idx_ops_link_tooluse") {
		t.Fatalf("resolver toolUseId predicate does not use idx_ops_link_tooluse.\nPlan:\n%s", toolUsePlan)
	}
}

// seedResolverLinkPlanRows plants a realistic skew: many ops with
// child_session_id IS NULL and NO stash (the common case — only kind='session'
// Agent ops ever gain a child), a few ops WITH each stash, and the matching
// parent sessions so the planner sees a selective partial index.
func seedResolverLinkPlanRows(t *testing.T, db *sql.DB, ctx context.Context) {
	t.Helper()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at)
		 VALUES ('src-link', 'codex', '/tmp/src-link', 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
		VALUES
		  ('s-link', 'src-link', 'n-link', 's-link', 'root', 'completed', 1, 1)
	`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO turns (id, session_id, seq, start_ts, status)
		VALUES ('t-link', 's-link', 1, 1, 'completed')
	`); err != nil {
		t.Fatalf("seed turns: %v", err)
	}
	// Many ops with NULL child_session_id and NO stash: the rows the old resolver
	// full-scanned every 5 s. None of these should be in the partial index.
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
		  SELECT 1
		  UNION ALL
		  SELECT n + 1 FROM seq WHERE n < 2000
		)
		INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status, child_session_id, extras_json)
		SELECT
		  'plain-op-' || n,
		  't-link',
		  's-link',
		  n,
		  'tool',
		  'plain-tool',
		  n,
		  'completed',
		  NULL,
		  '{}'
		FROM seq
	`); err != nil {
		t.Fatalf("seed plain ops: %v", err)
	}
	// A handful of ops WITH each stash — exactly the rows the partial index keeps.
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
		  SELECT 1
		  UNION ALL
		  SELECT n + 1 FROM seq WHERE n < 50
		)
		INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status, child_session_id, extras_json)
		SELECT
		  'child-op-' || n,
		  't-link',
		  's-link',
		  10000 + n,
		  'session',
		  'Agent',
		  n,
		  'completed',
		  NULL,
		  '{"aiViewer":{"childNativeId":"child-' || n || '"}}'
		FROM seq
	`); err != nil {
		t.Fatalf("seed childNativeId ops: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
		  SELECT 1
		  UNION ALL
		  SELECT n + 1 FROM seq WHERE n < 50
		)
		INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status, child_session_id, extras_json)
		SELECT
		  'tooluse-op-' || n,
		  't-link',
		  's-link',
		  20000 + n,
		  'session',
		  'Agent',
		  n,
		  'completed',
		  NULL,
		  '{"aiViewer":{"toolUseId":"tu-' || n || '"}}'
		FROM seq
	`); err != nil {
		t.Fatalf("seed toolUseId ops: %v", err)
	}
}
