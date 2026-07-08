package store_test

import (
	"context"
	"database/sql"
	"testing"
)

// TestMigration0004_AppliesAndBumpsVersion asserts migration 0004 lands
// the notify change-log table (asserted by the shape/autoincrement tests
// below) and that the schema marker advances with the migration chain.
// openInMemory runs the FULL chain through the latest migration, so the on-disk
// schema_meta.version is '14' (each serve-relevant migration bumps it in lockstep
// with presenter.SchemaVersion through 0014=v14; the serve binary refuses to start
// on mismatch). That migration 0004 itself sets '4' is pinned by the runner's
// per-file tracking plus the dedicated own-bump assertions
// (TestMigration0008_BumpsSchemaVersionTo8, TestMigration0007_BumpsSchemaVersionTo7_Internal,
// TestMigration0006_BumpsSchemaVersionTo6_Internal, TestMigration0005_BumpsSchemaVersionTo5).
// Source of truth: .agents/sow/specs/data-model.md §notify.
func TestMigration0004_AppliesAndBumpsVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "16" {
		t.Fatalf("schema_meta.version: want %q, got %q", "16", version)
	}
}

// TestMigration0004_NotifyTableShape verifies the notify table exists
// with exactly the columns data-model.md §notify documents, in order.
func TestMigration0004_NotifyTableShape(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	type col struct {
		name    string
		typ     string
		notnull int
		pk      int
	}
	want := []col{
		{"seq", "INTEGER", 0, 1},
		{"ts_us", "INTEGER", 1, 0},
		{"kind", "TEXT", 1, 0},
		{"session_id", "TEXT", 0, 0},
		{"root_session_id", "TEXT", 0, 0},
		{"source_id", "TEXT", 0, 0},
	}

	rows, err := db.QueryContext(ctx, `PRAGMA table_info(notify)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(notify): %v", err)
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
			t.Fatalf("scan table_info row: %v", err)
		}
		got = append(got, col{name: name, typ: typ, notnull: notnull, pk: pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("notify column count: want %d, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("notify column %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// TestMigration0004_SeqIsAutoincrementMonotonic pins the critical
// AUTOINCREMENT contract: seq values are never reused after a row is
// pruned. Serve's poll cursor is a seq high-water mark; reuse would make
// it skip rows. We insert, delete the row, insert again, and assert the
// second seq is strictly greater than the first.
func TestMigration0004_SeqIsAutoincrementMonotonic(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	insert := func() int64 {
		res, err := db.ExecContext(ctx,
			`INSERT INTO notify (ts_us, kind) VALUES (?, 'stats_invalidated')`, 1_000)
		if err != nil {
			t.Fatalf("insert notify: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("LastInsertId: %v", err)
		}
		return id
	}

	first := insert()
	if _, err := db.ExecContext(ctx, `DELETE FROM notify WHERE seq = ?`, first); err != nil {
		t.Fatalf("delete notify row: %v", err)
	}
	second := insert()

	if second <= first {
		t.Fatalf("AUTOINCREMENT not monotonic across prune: first=%d second=%d (want second > first)", first, second)
	}
}
