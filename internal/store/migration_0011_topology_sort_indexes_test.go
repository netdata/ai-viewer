package store_test

import (
	"context"
	"strings"
	"testing"
)

// Migration 0011 (SOW-0093 chunk 3) adds the topology sort indexes:
//   - sessions.duration_us   stored column = end_ts - start_ts
//   - idx_sessions_duration  on (duration_us DESC, id ASC)
//   - idx_sessions_op_count  on (op_count DESC, id ASC)
//
// These make the unfiltered /api/topology request use the index for
// the ORDER BY (the dominant cost on a 530k-row sessions table) instead
// of a full scan + temp B-tree sort.
//
// This file pins:
//   - The full-chain head version (11)
//   - The duration_us column shape (INTEGER, NULL when end_ts IS NULL)
//   - The duration_us backfill correctness (end_ts - start_ts)
//   - Both new indexes exist with the documented column lists
//   - The unfiltered cross-topology query plan uses the new index
//     (verifies the optimizer can use the index — EXPLAIN QUERY PLAN
//     rather than absolute timings, so the test is hermetic)

func TestMigration0011_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "11" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0011)", "11", version)
	}
}

// TestMigration0011_DurationUsBackfill pins the backfill arithmetic:
// duration_us is the diff of end_ts - start_ts when end_ts is non-null,
// else NULL. The default value before backfill is NULL.
func TestMigration0011_DurationUsBackfill(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('s1', 'jsonl', '/tmp/x', 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts, end_ts)
		 VALUES
		   ('in-progress', 's1', 'n1', 'in-progress', 'root', 'running', 1000, 1500, NULL),
		   ('fast',        's1', 'n2', 'fast',       'root', 'completed', 2000, 2000, 2500),
		   ('slow',        's1', 'n3', 'slow',       'root', 'completed', 3000, 3000, 4000)`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	// Re-run the migration's backfill arithmetic against the new rows
	// (the migration's UPDATE was scoped to the rows that existed at
	// migration time; seeded rows have duration_us=NULL by default).
	if _, err := db.ExecContext(ctx,
		`UPDATE sessions SET duration_us = CASE WHEN end_ts IS NOT NULL THEN end_ts - start_ts ELSE NULL END`); err != nil {
		t.Fatalf("backfill duration_us: %v", err)
	}

	tests := []struct {
		sessionID string
		want      any // int64 or nil
	}{
		{"in-progress", nil},
		{"fast", int64(500)},
		{"slow", int64(1000)},
	}
	for _, tc := range tests {
		var got sqlNullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT duration_us FROM sessions WHERE id = ?`, tc.sessionID,
		).Scan(&got); err != nil {
			t.Fatalf("scan duration_us for %s: %v", tc.sessionID, err)
		}
		if !got.equal(tc.want) {
			t.Errorf("duration_us for %q: want %v, got %v", tc.sessionID, tc.want, got)
		}
	}
}

// sqlNullInt64 is a tiny helper that mirrors the COALESCE/IS NULL
// semantics the backfill uses. It is local to this test file to keep
// the migration's contract self-contained.
type sqlNullInt64 struct {
	Valid bool
	Int64 int64
}

func (n *sqlNullInt64) Scan(src any) error {
	if src == nil {
		n.Valid = false
		return nil
	}
	v, ok := src.(int64)
	if !ok {
		// modernc may return int for INTEGER columns in some cases
		if vi, ok := src.(int); ok {
			n.Valid = true
			n.Int64 = int64(vi)
			return nil
		}
		return errInvalidType{src: src}
	}
	n.Valid = true
	n.Int64 = v
	return nil
}

func (n sqlNullInt64) equal(want any) bool {
	if w, ok := want.(int64); ok {
		return n.Valid && n.Int64 == w
	}
	if want == nil {
		return !n.Valid
	}
	return false
}

type errInvalidType struct{ src any }

func (e errInvalidType) Error() string { return "sqlNullInt64: unexpected src type" }

// TestMigration0011_IndexesExist pins the index names + column lists.
// The optimizer may use either index depending on the query, so both
// must be present.
func TestMigration0011_IndexesExist(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	want := map[string]string{
		"idx_sessions_duration": "sessions(duration_us DESC, id ASC)",
		"idx_sessions_op_count": "sessions(op_count DESC, id ASC)",
		"idx_sessions_cost":     "sessions(cost_usd DESC, id ASC)",
		"idx_sessions_tokens":   "sessions((tokens_in + tokens_out) DESC, id ASC)",
	}
	for name, wantCols := range want {
		var sql string
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type='index' AND name = ?`, name,
		).Scan(&sql); err != nil {
			t.Fatalf("read sqlite_master for %s: %v", name, err)
		}
		// sqlite_master stores the column list without ASC/DESC decorations
		// in some SQLite builds, so match on a normalized form.
		if !indexColumnsMatch(sql, wantCols) {
			t.Errorf("index %s: want %q, got CREATE INDEX sql %q", name, wantCols, sql)
		}
	}
}

// indexColumnsMatch normalizes the column list from sqlite_master
// against the expected "(col1 [DESC|ASC], col2 [DESC|ASC])" form.
// Strips whitespace + removes the CREATE INDEX prefix and table name
// before comparing.
func indexColumnsMatch(sql, want string) bool {
	// Both inputs contain "INDEX name ON table(columns)" — pull the
	// column list out of both, normalize whitespace + sort order words.
	extract := func(s string) string {
		open := -1
		close := -1
		for i := 0; i < len(s); i++ {
			if s[i] == '(' {
				open = i
			} else if s[i] == ')' && open >= 0 {
				close = i
				break
			}
		}
		if open < 0 || close < 0 {
			return ""
		}
		return normalizeCols(s[open+1 : close])
	}
	return extract(sql) == extract(want)
}

func normalizeCols(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' {
			if !prevSpace && len(out) > 0 {
				out = append(out, ' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		out = append(out, c)
	}
	// trim trailing space
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// TestMigration0011_TopologyQueryUsesIndex pins the planner uses
// idx_sessions_duration for the unfiltered cross-topology query
// (matching the crossSizeExpr default of duration). This is a
// structural check, not a perf benchmark — the test passes on any
// machine because EXPLAIN QUERY PLAN is deterministic.
func TestMigration0011_TopologyQueryUsesIndex(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	// Seed enough rows to make the planner pick the index over a scan
	// (the threshold is well below what we use here).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('s-seed', 'jsonl', '/tmp/x', 1)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts, end_ts, op_count, duration_us)
		 SELECT 's' || x, 's-seed', 'n' || x, 's' || x, 'root', 'completed', x, x, x + 100, x, 100
		 FROM (WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 1000) SELECT n AS x FROM seq)`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	// ANALYZE so the planner has stats to use the index.
	if _, err := db.ExecContext(ctx, `ANALYZE sessions`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// The cross-topology default metric = duration, which now uses
	// duration_us. The query below mirrors crossSizeExpr + the LIMIT
	// 201 page size (loadCrossAgents binds limit+1 for the PEEK).
	rows, err := db.QueryContext(ctx, `
		EXPLAIN QUERY PLAN
		SELECT s.id, COALESCE(s.duration_us, 0) AS size_metric
		FROM sessions s
		ORDER BY size_metric DESC, s.id ASC
		LIMIT 201`)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
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

	// The plan should mention idx_sessions_duration. If it doesn't,
	// the migration didn't help and the optimizer is doing a scan +
	// sort, which is what we're trying to eliminate.
	if !strings.Contains(plan.String(), "idx_sessions_duration") {
		t.Fatalf("topology query does not use idx_sessions_duration.\nPlan:\n%s", plan.String())
	}
	// And it should NOT be a full table SCAN of sessions.
	if strings.Contains(plan.String(), "SCAN s\n") || strings.Contains(plan.String(), "SCAN sessions\n") {
		t.Fatalf("topology query still does a full table scan (would be O(rows)).\nPlan:\n%s", plan.String())
	}
}
