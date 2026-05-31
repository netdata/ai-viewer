package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// This file pins migration 0005 (SOW-0026): a data-only backfill that
// recomputes ops.duration_us = end_ts - start_ts for historical rows the
// pre-fix writer persisted as 0 (it used end_ts - finalize.Ts ≈ 0), and
// recomputes catalog_models/catalog_tools.total_duration_us as the SUM of the
// corrected member-op durations. It is an internal (package store) test so it
// can fetch the REAL embedded 0005 SQL via loadMigrations() and execute that
// exact statement — not a copy — against a seeded pre-fix DB.
//
// Migration 0005 is deliberately schema-version-NEUTRAL: it changes only row
// data, not the schema shape, so it must NOT bump schema_meta.version (the
// serve binary's presenter.SchemaVersion is still 4 and CheckSchema is an
// exact-equality gate — a bump would refuse to start a v4 binary). The
// version-stays-4 assertion below pins that contract.

const migration0005Name = "0005_op_duration_backfill.sql"

// migration0005SQL returns the body of the embedded 0005 migration, failing
// the test if the file is absent or misnamed. Using the embedded copy keeps
// the test honest: it runs precisely what ships.
func migration0005SQL(t *testing.T) string {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range all {
		if m.name == migration0005Name {
			return m.sql
		}
	}
	t.Fatalf("embedded migration %q not found (have %d migrations)", migration0005Name, len(all))
	return ""
}

// openMigratedSQLite opens a bare in-memory sqlite, runs Up() (all embedded
// migrations including 0005, all no-ops on empty tables), and returns the DB.
func openMigratedSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// nil logger: the runner tolerates it (see TestUp_NilLogger) and package
	// store's silentLogger lives in the external test package, out of reach here.
	if err := Up(context.Background(), db, nil); err != nil {
		t.Fatalf("store.Up: %v", err)
	}
	return db
}

// seedHistoricalSession inserts the minimal source/session/turn parents so ops
// FK constraints (turn_id, session_id) are satisfied for the seeded ops below.
func seedHistoricalSession(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO sources (id, format, location, enabled, parse_errors, created_at) VALUES ('src','codex','/tmp',1,0,1000)`, nil},
		{`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
		  VALUES ('sess','src','s','sess','root','completed',1000,5000)`, nil},
		{`INSERT INTO turns (id, session_id, seq, start_ts, status) VALUES ('turn','sess',1,1000,'completed')`, nil},
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s.q, s.args...); err != nil {
			t.Fatalf("seed (%s): %v", s.q, err)
		}
	}
}

// insertHistoricalOp inserts one op with start_ts/end_ts set but the buggy
// duration_us=0, simulating a row the pre-fix writer produced.
func insertHistoricalOp(t *testing.T, db *sql.DB, id string, seq int, kind, name, provider, model, toolNS string, startTs, endTs int64) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, tool_namespace, model, provider,
                 start_ts, end_ts, duration_us, status)
VALUES (?, 'turn', 'sess', ?, ?, ?, ?, ?, ?, ?, ?, 0, 'completed')`,
		id, seq, kind, name, nullIfEmptyStr(toolNS), nullIfEmptyStr(model), nullIfEmptyStr(provider),
		startTs, endTs); err != nil {
		t.Fatalf("insert historical op %s: %v", id, err)
	}
}

func nullIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestMigration0005_BackfillsOpDurationAndCatalogTotals seeds the exact
// pre-fix state — ops with start/end set but duration_us=0, and
// catalog_models/catalog_tools rows with total_duration_us=0 — runs the real
// embedded 0005 SQL, and asserts every op's duration_us = end_ts-start_ts and
// each catalog total equals the SUM of its corrected member-op durations.
func TestMigration0005_BackfillsOpDurationAndCatalogTotals(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()
	seedHistoricalSession(t, db)

	// Two LLM ops under (openai, gpt-5.5): 300us and 700us.
	insertHistoricalOp(t, db, "op-llm-1", 1, "llm", "message", "openai", "gpt-5.5", "", 1000, 1300)
	insertHistoricalOp(t, db, "op-llm-2", 2, "llm", "message", "openai", "gpt-5.5", "", 2000, 2700)
	// One tool op (shell, shell): 500us.
	insertHistoricalOp(t, db, "op-tool-1", 3, "tool", "shell", "", "", "shell", 3000, 3500)
	// One tool op with empty namespace → folds to 'builtin' in catalog: 200us.
	insertHistoricalOp(t, db, "op-tool-2", 4, "tool", "read", "", "", "", 4000, 4200)

	// Seed catalog rows in the buggy total_duration_us=0 state.
	mustExec(t, db, `INSERT INTO catalog_models (provider, name, first_seen, last_seen, call_count, total_duration_us)
	                 VALUES ('openai','gpt-5.5',1000,2700,2,0)`)
	mustExec(t, db, `INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count, total_duration_us)
	                 VALUES ('shell','shell',3000,3500,1,0)`)
	mustExec(t, db, `INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count, total_duration_us)
	                 VALUES ('builtin','read',4000,4200,1,0)`)

	// Apply the real embedded 0005 migration body.
	if _, err := db.ExecContext(ctx, migration0005SQL(t)); err != nil {
		t.Fatalf("apply migration 0005: %v", err)
	}

	// Per-op duration_us = end_ts - start_ts.
	for _, tc := range []struct {
		id   string
		want int64
	}{
		{"op-llm-1", 300}, {"op-llm-2", 700}, {"op-tool-1", 500}, {"op-tool-2", 200},
	} {
		got := scanNullInt64Internal(t, db, `SELECT duration_us FROM ops WHERE id = ?`, tc.id)
		if !got.Valid || got.Int64 != tc.want {
			t.Errorf("ops[%s].duration_us = %+v, want valid %d (end_ts-start_ts)", tc.id, got, tc.want)
		}
	}

	// catalog_models total = SUM of its two LLM ops (300+700 = 1000).
	if got := scanIntInternal(t, db, `SELECT total_duration_us FROM catalog_models WHERE provider='openai' AND name='gpt-5.5'`); got != 1000 {
		t.Errorf("catalog_models.total_duration_us = %d, want 1000 (recompute SUM)", got)
	}
	// catalog_tools (shell, shell) = 500.
	if got := scanIntInternal(t, db, `SELECT total_duration_us FROM catalog_tools WHERE namespace='shell' AND name='shell'`); got != 500 {
		t.Errorf("catalog_tools(shell,shell).total_duration_us = %d, want 500", got)
	}
	// catalog_tools (builtin, read) = 200 (empty-namespace op folds to builtin).
	if got := scanIntInternal(t, db, `SELECT total_duration_us FROM catalog_tools WHERE namespace='builtin' AND name='read'`); got != 200 {
		t.Errorf("catalog_tools(builtin,read).total_duration_us = %d, want 200", got)
	}
}

// TestMigration0005_LeavesSchemaVersionAt4 pins the schema-version-neutral
// contract: 0005 is a data backfill, so schema_meta.version stays '4'. A bump
// would make a v4-built serve binary refuse to start (CheckSchema exact match).
func TestMigration0005_LeavesSchemaVersionAt4(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "4" {
		t.Fatalf("schema_meta.version = %q, want %q (0005 is data-only, must not bump)", version, "4")
	}
}

// TestMigration0005_DoesNotTouchUnknownOrNullEndOps pins the backfill guards:
// rows with NULL end_ts (op never finalized) keep NULL duration_us, and rows
// where end_ts < start_ts (clock skew) are left untouched rather than written
// with a negative duration.
func TestMigration0005_DoesNotTouchUnknownOrNullEndOps(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()
	seedHistoricalSession(t, db)

	// op with NULL end_ts: still-running historical op.
	if _, err := db.ExecContext(ctx, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, duration_us, status)
VALUES ('op-null-end','turn','sess',1,'llm','message',1000,NULL,NULL,'running')`); err != nil {
		t.Fatalf("insert null-end op: %v", err)
	}
	// op with end_ts < start_ts: must not produce a negative duration.
	if _, err := db.ExecContext(ctx, `
INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, end_ts, duration_us, status)
VALUES ('op-skew','turn','sess',2,'llm','message',5000,4000,0,'completed')`); err != nil {
		t.Fatalf("insert skew op: %v", err)
	}

	if _, err := db.ExecContext(ctx, migration0005SQL(t)); err != nil {
		t.Fatalf("apply migration 0005: %v", err)
	}

	if got := scanNullInt64Internal(t, db, `SELECT duration_us FROM ops WHERE id='op-null-end'`); got.Valid {
		t.Errorf("null-end op duration_us = %d, want NULL (no end_ts ⇒ no duration)", got.Int64)
	}
	if got := scanNullInt64Internal(t, db, `SELECT duration_us FROM ops WHERE id='op-skew'`); got.Valid && got.Int64 < 0 {
		t.Errorf("skew op duration_us = %d, want untouched (no negative duration)", got.Int64)
	}
}

// --- tiny internal-package scan helpers (store_test.go's live in the external
// test package, unreachable from package store). ---

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

func scanIntInternal(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return v
}

func scanNullInt64Internal(t *testing.T, db *sql.DB, query string, args ...any) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return v
}
