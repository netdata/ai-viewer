package store_test

import (
	"context"
	"database/sql"
	"testing"
)

// This file pins migration 0006 (SOW-0007 Chunk 2): the empty rollup tables
// (rollup_hourly, rollup_daily) and the FTS5 search tables (fts_ops, fts_logs).
// Schema-only — no row population (the rollup backfill + FTS indexing are
// Chunk 4). 0006 bumps schema_meta.version to '6' in lockstep with
// presenter.SchemaVersion; that OWN bump is pinned by the internal
// TestMigration0006_BumpsSchemaVersionTo6_Internal (which stops the chain at
// 0006). This external file pins 0006's SCHEMA SHAPE (table/index/FTS5 tests
// below) over the FULL chain, whose head is now 0008=v8.
// Source of truth: .agents/sow/specs/data-model.md §Rollup tables (SOW-0007)
// and §Full-text search (FTS5).

// TestMigration0006_ChainHeadSchemaVersion pins the FULL-chain head version:
// openInMemory runs every migration through the latest (0008), so the on-disk
// schema_meta.version is '8'. 0006's OWN bump (to '6') is pinned separately by
// the internal apply-through-0006 test; this assertion guards the lockstep with
// presenter.SchemaVersion as new migrations are added.
func TestMigration0006_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "9" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0009)", "9", version)
	}
}

// rollupColumns is the shared column contract for rollup_hourly and
// rollup_daily — both tables are byte-identical in shape (only the bucket
// granularity differs semantically), per data-model.md §Rollup tables.
func rollupColumns() []struct {
	name    string
	typ     string
	notnull int
	pk      int
} {
	return []struct {
		name    string
		typ     string
		notnull int
		pk      int
	}{
		{"bucket_ts", "INTEGER", 1, 1},
		{"source_format", "TEXT", 1, 2},
		{"dimension", "TEXT", 1, 3},
		{"dimension_value", "TEXT", 1, 4},
		{"op_count", "INTEGER", 1, 0},
		{"tokens_in", "INTEGER", 1, 0},
		{"tokens_out", "INTEGER", 1, 0},
		{"tokens_cache_read", "INTEGER", 1, 0},
		{"tokens_cache_write", "INTEGER", 1, 0},
		{"cost_usd", "REAL", 1, 0},
		{"failures", "INTEGER", 1, 0},
		{"duration_us", "INTEGER", 1, 0},
		{"session_starts", "INTEGER", 1, 0},
	}
}

// TestMigration0006_RollupTableShapes verifies rollup_hourly and rollup_daily
// exist with exactly the columns + the 4-column composite PRIMARY KEY
// (bucket_ts, source_format, dimension, dimension_value) data-model.md
// documents, in order.
func TestMigration0006_RollupTableShapes(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	for _, table := range []string{"rollup_hourly", "rollup_daily"} {
		table := table
		t.Run(table, func(t *testing.T) {
			t.Parallel()

			type col struct {
				name    string
				typ     string
				notnull int
				pk      int
			}
			want := make([]col, 0)
			for _, c := range rollupColumns() {
				want = append(want, col{c.name, c.typ, c.notnull, c.pk})
			}

			rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
			if err != nil {
				t.Fatalf("PRAGMA table_info(%s): %v", table, err)
			}
			defer func() { _ = rows.Close() }()

			var got []col
			for rows.Next() {
				var (
					cid     int
					name    string
					typ     string
					notnull int
					dflt    sql.NullString
					pk      int
				)
				if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
					t.Fatalf("scan table_info(%s): %v", table, err)
				}
				got = append(got, col{name: name, typ: typ, notnull: notnull, pk: pk})
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate table_info(%s): %v", table, err)
			}

			if len(got) != len(want) {
				t.Fatalf("%s column count: want %d, got %d (%+v)", table, len(want), len(got), got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s column %d: want %+v, got %+v", table, i, want[i], got[i])
				}
			}
		})
	}
}

// TestMigration0006_RollupDimensionIndexes pins the dimension-led secondary
// index that backs the top-N (/api/stats/top) and time-series
// (/api/stats/aggregate) scans: a non-unique index leading with
// (dimension, bucket_ts). The PK leads with bucket_ts and cannot seek by
// dimension, so this index is the access path for the dimension-led range
// scan.
func TestMigration0006_RollupDimensionIndexes(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	cases := []struct {
		table string
		index string
	}{
		{"rollup_hourly", "idx_rollup_hourly_dim"},
		{"rollup_daily", "idx_rollup_daily_dim"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.index, func(t *testing.T) {
			t.Parallel()

			cols := indexColumns(t, db, ctx, tc.index)
			want := []string{"dimension", "bucket_ts"}
			if len(cols) != len(want) {
				t.Fatalf("%s columns: want %v, got %v", tc.index, want, cols)
			}
			for i := range want {
				if cols[i] != want[i] {
					t.Errorf("%s column %d: want %q, got %q", tc.index, i, want[i], cols[i])
				}
			}
		})
	}
}

// indexColumns returns the ordered column list of a named index.
func indexColumns(t *testing.T, db *sql.DB, ctx context.Context, indexName string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA index_info("+indexName+")")
	if err != nil {
		t.Fatalf("PRAGMA index_info(%s): %v", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
		)
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info(%s): %v", indexName, err)
		}
		cols = append(cols, name.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index_info(%s): %v", indexName, err)
	}
	return cols
}

// TestMigration0006_RollupCompositePKRejectsDuplicate proves the 4-column
// composite PRIMARY KEY enforces one row per
// (bucket_ts, source_format, dimension, dimension_value). This is the
// foundation for the ingester's idempotent upsert (SOW-0007 Chunk 4): a second
// insert of the same key must conflict.
func TestMigration0006_RollupCompositePKRejectsDuplicate(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	for _, table := range []string{"rollup_hourly", "rollup_daily"} {
		insert := `INSERT INTO ` + table +
			` (bucket_ts, source_format, dimension, dimension_value) VALUES (3600000000, 'codex', 'model', 'gpt-5.5')`
		if _, err := db.ExecContext(ctx, insert); err != nil {
			t.Fatalf("%s first insert: %v", table, err)
		}
		if _, err := db.ExecContext(ctx, insert); err == nil {
			t.Errorf("%s duplicate composite-PK insert: want UNIQUE conflict, got nil", table)
		}
	}
}

// TestMigration0006_FTSTablesAreFTS5 verifies fts_ops and fts_logs are created
// as FTS5 virtual tables (their sqlite_master SQL declares
// `USING fts5(`). This pins the engine choice (BM25 default ranking).
func TestMigration0006_FTSTablesAreFTS5(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	for _, table := range []string{"fts_ops", "fts_logs"} {
		var ddl string
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatalf("read sqlite_master for %s: %v", table, err)
		}
		if !containsFold(ddl, "using fts5(") {
			t.Errorf("%s is not an FTS5 virtual table; DDL=%q", table, ddl)
		}
	}
}

// TestMigration0006_FTSMatchSnippetRankLinkage exercises the exact read pattern
// GET /api/search relies on: insert into the content-owning FTS5 table, MATCH,
// snippet() of the matched text, BM25 rank, and resolution back via the
// UNINDEXED linkage columns. A schema that compiled but could not serve this
// pattern would pass shape tests yet fail the endpoint, so it is asserted here.
func TestMigration0006_FTSMatchSnippetRankLinkage(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	// fts_ops: index the op error text, resolve op_id/session_id back.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO fts_ops(rowid, name, model, provider, tool_namespace, error_text, op_id, session_id)
		 VALUES (1, 'Bash', '', '', 'shell', 'connection timeout while running', 'op-1', 'sess-1')`); err != nil {
		t.Fatalf("insert fts_ops: %v", err)
	}
	var (
		opSnippet   string
		opRank      float64
		opID, sesID string
	)
	if err := db.QueryRowContext(ctx,
		`SELECT snippet(fts_ops, -1, '[', ']', '…', 8), rank, op_id, session_id
		 FROM fts_ops WHERE fts_ops MATCH 'timeout' ORDER BY rank LIMIT 1`).
		Scan(&opSnippet, &opRank, &opID, &sesID); err != nil {
		t.Fatalf("fts_ops MATCH: %v", err)
	}
	if opID != "op-1" || sesID != "sess-1" {
		t.Errorf("fts_ops linkage: want op-1/sess-1, got %q/%q", opID, sesID)
	}
	if !containsFold(opSnippet, "timeout") {
		t.Errorf("fts_ops snippet missing match marker: %q", opSnippet)
	}

	// fts_logs: index the log message, resolve log_id/session_id/severity/ts.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO fts_logs(rowid, message, log_id, session_id, op_id, severity, ts)
		 VALUES (1, 'parse error at line 5', 1, 'sess-1', 'op-1', 'ERR', 12345)`); err != nil {
		t.Fatalf("insert fts_logs: %v", err)
	}
	var (
		logSnippet string
		logRank    float64
		logID      int64
		logSev     string
		logTS      int64
	)
	if err := db.QueryRowContext(ctx,
		`SELECT snippet(fts_logs, -1, '[', ']', '…', 8), rank, log_id, severity, ts
		 FROM fts_logs WHERE fts_logs MATCH 'parse' ORDER BY rank LIMIT 1`).
		Scan(&logSnippet, &logRank, &logID, &logSev, &logTS); err != nil {
		t.Fatalf("fts_logs MATCH: %v", err)
	}
	if logID != 1 || logSev != "ERR" || logTS != 12345 {
		t.Errorf("fts_logs linkage: want 1/ERR/12345, got %d/%q/%d", logID, logSev, logTS)
	}
	if !containsFold(logSnippet, "parse") {
		t.Errorf("fts_logs snippet missing match marker: %q", logSnippet)
	}
}

// containsFold is a tiny case-insensitive substring check so the test file does
// not pull in strings solely for two assertions.
func containsFold(haystack, needle string) bool {
	h := []byte(haystack)
	n := []byte(needle)
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + ('a' - 'A')
		}
		return b
	}
	if len(n) == 0 {
		return true
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := 0; j < len(n); j++ {
			if lower(h[i+j]) != lower(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
