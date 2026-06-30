package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Migration 0013 (SOW-0114) adds dirty-session aggregate support indexes. The
// ingester refreshes aggregates and writes Tail heartbeat/staleness state through
// one SQLite writer connection, so aggregate subqueries must stay session-scoped
// even when a source contains giant sessions.

func TestMigration0013_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "14" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0014)", "14", version)
	}
}

func TestMigration0013_AggregateLivenessIndexesExist(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	want := map[string]string{
		"idx_ops_session_status": "ops(session_id, status)",
		"idx_ops_session_end":    "ops(session_id, end_ts)",
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
	}
}

func TestMigration0013_DirtySessionAggregateQueriesUseSessionScopedIndexes(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()
	seedAggregateLivenessPlanRows(t, db, ctx)

	if _, err := db.ExecContext(ctx, `ANALYZE ops`); err != nil {
		t.Fatalf("ANALYZE ops: %v", err)
	}

	failurePlan := explainPlan(t, db, ctx, `
		EXPLAIN QUERY PLAN
		SELECT COUNT(*)
		FROM ops
		WHERE ops.session_id = ? AND ops.status = 'failed'
	`, "s-big")
	if !strings.Contains(failurePlan, "idx_ops_session_status") {
		t.Fatalf("failed-op aggregate query does not use idx_ops_session_status.\nPlan:\n%s", failurePlan)
	}
	if strings.Contains(failurePlan, "idx_ops_status") {
		t.Fatalf("failed-op aggregate query still uses global idx_ops_status.\nPlan:\n%s", failurePlan)
	}

	lastActivityPlan := explainPlan(t, db, ctx, `
		EXPLAIN QUERY PLAN
		SELECT MAX(end_ts)
		FROM ops
		WHERE ops.session_id = ?
	`, "s-big")
	if !strings.Contains(lastActivityPlan, "idx_ops_session_end") {
		t.Fatalf("last-activity aggregate query does not use idx_ops_session_end.\nPlan:\n%s", lastActivityPlan)
	}
}

func seedAggregateLivenessPlanRows(t *testing.T, db *sql.DB, ctx context.Context) {
	t.Helper()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at)
		 VALUES ('src-agg-live', 'codex', '/tmp/src-agg-live', 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
		VALUES
		  ('s-big',   'src-agg-live', 'n-big',   's-big',   'root', 'running',   1, 1),
		  ('s-other', 'src-agg-live', 'n-other', 's-other', 'root', 'completed', 1, 1)
	`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO turns (id, session_id, seq, start_ts, status)
		VALUES
		  ('t-big',   's-big',   1, 1, 'running'),
		  ('t-other', 's-other', 1, 1, 'completed')
	`); err != nil {
		t.Fatalf("seed turns: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
		  SELECT 1
		  UNION ALL
		  SELECT n + 1 FROM seq WHERE n < 1500
		)
		INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status)
		SELECT
		  'big-op-' || n,
		  't-big',
		  's-big',
		  n,
		  'tool',
		  'big-tool',
		  n,
		  100000 + n,
		  CASE WHEN n % 10 = 0 THEN 'failed' ELSE 'completed' END
		FROM seq
	`); err != nil {
		t.Fatalf("seed big ops: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
		  SELECT 1
		  UNION ALL
		  SELECT n + 1 FROM seq WHERE n < 1500
		)
		INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, status)
		SELECT
		  'other-op-' || n,
		  't-other',
		  's-other',
		  n,
		  'tool',
		  'other-tool',
		  n,
		  200000 + n,
		  'failed'
		FROM seq
	`); err != nil {
		t.Fatalf("seed other ops: %v", err)
	}
}

func explainPlan(t *testing.T, db *sql.DB, ctx context.Context, query string, args ...any) string {
	t.Helper()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return plan.String()
}
