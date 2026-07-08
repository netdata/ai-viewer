package store_test

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/netdata/ai-viewer/internal/store"
)

// TestUp_NilDB asserts the runner refuses to run against a nil handle
// rather than dereferencing it.
func TestUp_NilDB(t *testing.T) {
	t.Parallel()

	err := store.Up(context.Background(), nil, silentLogger())
	if err == nil {
		t.Fatal("store.Up(nil): want error, got nil")
	}
}

// TestUp_NilLogger asserts the runner tolerates a nil logger. The
// production path always supplies one, but defensive code paths in
// applyMigration check for nil before logging.
func TestUp_NilLogger(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := store.Up(context.Background(), db, nil); err != nil {
		t.Fatalf("store.Up with nil logger: %v", err)
	}

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM _schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count _schema_migrations: %v", err)
	}
	// Grows by one for every NNNN_*.sql under migrations/. Update in
	// lockstep with each new migration.
	const expectedMigrations = 16
	if count != expectedMigrations {
		t.Fatalf("_schema_migrations rows: want %d, got %d", expectedMigrations, count)
	}
}

// TestUp_DoubleCall verifies repeated invocations on the same DB stay
// idempotent: no duplicate migration rows, no errors.
func TestUp_DoubleCall(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for i := range 3 {
		if err := store.Up(context.Background(), db, silentLogger()); err != nil {
			t.Fatalf("store.Up call %d: %v", i, err)
		}
	}

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM _schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count _schema_migrations: %v", err)
	}
	// Grows by one for every NNNN_*.sql under migrations/. Update in
	// lockstep with each new migration.
	const expectedMigrations = 16
	if count != expectedMigrations {
		t.Fatalf("_schema_migrations rows: want %d, got %d", expectedMigrations, count)
	}
}

// TestUp_FailsOnClosedDB exercises the bookkeeping DDL error path. The
// runner must surface the error before reading the embedded migrations.
func TestUp_FailsOnClosedDB(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	if err := store.Up(context.Background(), db, silentLogger()); err == nil {
		t.Fatal("store.Up against closed DB: want error, got nil")
	}
}

// TestUp_FailsOnMalformedBookkeeping exercises the
// `loadAppliedMigrations` error path. We pre-create a
// `_schema_migrations` table missing the `filename` column the loader
// SELECTs; the bookkeeping DDL is `CREATE TABLE IF NOT EXISTS` so it
// leaves our shape intact, the SELECT then fails with "no such column"
// and Up surfaces it before any migration applies.
func TestUp_FailsOnMalformedBookkeeping(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-create _schema_migrations with the wrong shape. CREATE TABLE
	// IF NOT EXISTS in the bookkeeping DDL is a no-op against this.
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE _schema_migrations (wrong TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("pre-create _schema_migrations: %v", err)
	}

	if err := store.Up(context.Background(), db, silentLogger()); err == nil {
		t.Fatal("store.Up against malformed bookkeeping: want error, got nil")
	}
}

// TestUp_FailsOnTaintedTable simulates a corrupt prior state: a
// pre-existing sessions table with an incompatible shape forces the
// migration's CREATE TABLE to fail, exercising the transaction rollback
// path inside applyMigration.
func TestUp_FailsOnTaintedTable(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-create a sessions table so the migration's CREATE TABLE
	// conflicts. The runner must roll back the transaction and surface
	// the error from Up.
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE sessions (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("pre-create sessions: %v", err)
	}

	err = store.Up(context.Background(), db, silentLogger())
	if err == nil {
		t.Fatal("store.Up: want error from tainted table, got nil")
	}

	// _schema_migrations row must NOT exist for the failed migration.
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM _schema_migrations WHERE filename='0001_initial.sql'`).Scan(&count); err != nil {
		t.Fatalf("count _schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("_schema_migrations: want 0 rows for rolled-back migration, got %d", count)
	}
}
