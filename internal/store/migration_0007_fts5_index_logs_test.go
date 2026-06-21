package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// This file pins migration 0007 (SOW-0007 Chunk 7a): the per-source
// fts5_index_logs opt-out flag on sources (default 1). This test covers only the
// migration's schema shape, not indexing behaviour; the flag itself gates
// fts_logs indexing in the FTS backfill and /api/search (both filter on
// src.fts5_index_logs = 1). It is an internal (package store) test so it can apply
// the embedded migration chain THROUGH a chosen file via openChainThrough,
// pinning each migration's OWN schema_meta bump independent of how many later
// migrations exist.
//
// Migration 0007 bumps schema_meta.version to '7' in lockstep with
// presenter.SchemaVersion. ai-viewer-serve runs no migrations and gates startup
// solely on schema_meta.version (CheckSchema, exact-equality), so the bump is
// what makes a v7 serve binary refuse a pre-0007 store (whose sources table
// lacks the column) rather than run against the older shape.

const migration0007Name = "0007_fts5_index_logs.sql"

// TestMigration0007_ChainHeadSchemaVersion pins the FULL-chain head version:
// openMigratedSQLite runs every migration through the latest (0008), so the
// on-disk schema_meta.version is '8'. 0007's OWN bump (to '7') is pinned
// separately by TestMigration0007_BumpsSchemaVersionTo7_Internal (in the 0008
// test file, which stops the chain at 0007); this assertion guards the lockstep
// with presenter.SchemaVersion as new migrations are added — mirroring how
// TestMigration0006_ChainHeadSchemaVersion was bumped once 0007 became head.
func TestMigration0007_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "11" {
		t.Fatalf("schema_meta.version = %q, want %q (full chain head is 0011)", version, "11")
	}
}

// TestMigration0006_BumpsSchemaVersionTo6_Internal pins migration 0006's OWN
// bump (version '6' after applying THROUGH 0006, not the chain head). Once 0007
// makes the chain head '7', a head-anchored 0006 assertion would no longer pin
// 0006 itself; this stops-at-0006 assertion keeps it meaningful — mirroring how
// TestMigration0005_BumpsSchemaVersionTo5 was made to pin 0005 once 0006 became
// the head.
func TestMigration0006_BumpsSchemaVersionTo6_Internal(t *testing.T) {
	t.Parallel()
	db := openChainThrough(t, "0006_rollups_and_fts.sql")
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "6" {
		t.Fatalf("schema_meta.version = %q, want %q (0006 bumps the version in lockstep)", version, "6")
	}
}

// TestMigration0007_AddsFTS5IndexLogsColumnDefault1 verifies the sources table
// gains the fts5_index_logs column with NOT NULL DEFAULT 1: a row inserted
// without the column reads back 1 (the opt-OUT default — logs are indexed
// unless the operator disables it).
func TestMigration0007_AddsFTS5IndexLogsColumnDefault1(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()

	// PRAGMA table_info exposes the column with its declared NOT NULL + default.
	var (
		found     bool
		notnull   int
		dflt      sql.NullString
		typeAffin string
	)
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(sources)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(sources): %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			nn      int
			d       sql.NullString
			pkOrder int
		)
		if err := rows.Scan(&cid, &name, &typ, &nn, &d, &pkOrder); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "fts5_index_logs" {
			found = true
			notnull = nn
			dflt = d
			typeAffin = typ
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	if !found {
		t.Fatal("sources.fts5_index_logs column missing after migration 0007")
	}
	if notnull != 1 {
		t.Errorf("fts5_index_logs NOT NULL flag = %d, want 1", notnull)
	}
	if typeAffin != "INTEGER" {
		t.Errorf("fts5_index_logs type = %q, want INTEGER", typeAffin)
	}
	if !dflt.Valid || dflt.String != "1" {
		t.Errorf("fts5_index_logs default = %+v, want \"1\"", dflt)
	}

	// Inserting a row WITHOUT the column must backfill the default (1).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src-default','codex','/tmp',1000)`); err != nil {
		t.Fatalf("insert source without fts5_index_logs: %v", err)
	}
	got := scanIntInternal(t, db, `SELECT fts5_index_logs FROM sources WHERE id='src-default'`)
	if got != 1 {
		t.Errorf("fts5_index_logs default value = %d, want 1", got)
	}
}

// TestMigration0007_IsIdempotent re-runs the full migration chain over an
// already-migrated DB and asserts it is a no-op: the version stays '8' and the
// 0007 row is recorded exactly once. ALTER TABLE ADD COLUMN is not natively
// idempotent in SQLite, so the runner's per-file _schema_migrations tracking is
// what makes a second Up() skip 0007; this pins that guarantee.
func TestMigration0007_IsIdempotent(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()

	// Second Up() over the same handle must not error or re-apply 0007.
	if err := Up(ctx, db, nil); err != nil {
		t.Fatalf("second store.Up: %v", err)
	}

	var version string
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "11" {
		t.Fatalf("schema_meta.version after re-run = %q, want %q", version, "11")
	}

	if got := scanIntInternal(t, db,
		`SELECT COUNT(*) FROM _schema_migrations WHERE filename=?`, migration0007Name); got != 1 {
		t.Fatalf("_schema_migrations rows for %s = %d, want 1 (idempotent)", migration0007Name, got)
	}
}

// TestMigration0007_PreMigrationStoreIsRejected pins the serve-refusal contract:
// a store migrated only THROUGH 0006 carries version '6', which a v7 serve
// binary's presenter.CheckSchema rejects. We assert the on-disk value is '6'
// (not 7) here — the presenter package owns the CheckSchema assertion against
// SchemaVersion, kept out of this package to avoid an import cycle.
func TestMigration0007_PreMigrationStoreIsRejected(t *testing.T) {
	t.Parallel()
	db := openChainThrough(t, "0006_rollups_and_fts.sql")
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version == "7" {
		t.Fatal("pre-0007 store reports version 7; expected the older 6 (serve must refuse it)")
	}
	if version != "6" {
		t.Fatalf("pre-0007 store version = %q, want %q", version, "6")
	}
	// The column must NOT yet exist before 0007 applies.
	var cnt int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('sources') WHERE name='fts5_index_logs'`).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info probe: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("fts5_index_logs present before 0007: count=%d, want 0", cnt)
	}
}
