package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// This file pins migration 0008 (SOW-0024): the general per-source
// sources.meta_json column. Like 0007, this test covers only the migration's
// schema shape, not the metadata-blob wiring (which lives in
// internal/ingest, internal/presenter, and cmd/ai-viewer-ingest). It is an
// internal (package store) test so it can apply the embedded migration chain
// THROUGH a chosen file via openChainThrough, pinning each migration's OWN
// schema_meta bump independent of how many later migrations exist.
//
// Migration 0008 bumps schema_meta.version to '8' in lockstep with
// presenter.SchemaVersion. ai-viewer-serve runs no migrations and gates startup
// solely on schema_meta.version (CheckSchema, exact-equality), so the bump is
// what makes a v8 serve binary refuse a pre-0008 store (whose sources table
// lacks the meta_json column) rather than run against the older shape.

const migration0008Name = "0008_source_meta.sql"

// TestMigration0008_BumpsSchemaVersionTo8 pins the lockstep contract: applying
// the full migration chain (which ends at 0008) leaves schema_meta.version '8'.
func TestMigration0008_BumpsSchemaVersionTo8(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "8" {
		t.Fatalf("schema_meta.version = %q, want %q (0008 bumps the version in lockstep)", version, "8")
	}
}

// TestMigration0007_BumpsSchemaVersionTo7_Internal pins migration 0007's OWN
// bump (version '7' after applying THROUGH 0007, not the chain head). Once 0008
// makes the chain head '8', a head-anchored 0007 assertion would no longer pin
// 0007 itself; this stops-at-0007 assertion keeps it meaningful — mirroring how
// TestMigration0006_BumpsSchemaVersionTo6_Internal was made to pin 0006 once
// 0007 became the head.
func TestMigration0007_BumpsSchemaVersionTo7_Internal(t *testing.T) {
	t.Parallel()
	db := openChainThrough(t, "0007_fts5_index_logs.sql")
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "7" {
		t.Fatalf("schema_meta.version = %q, want %q", version, "7")
	}
}

// TestMigration0008_AddsMetaJSONColumnNullable verifies the sources table
// gains the meta_json column as nullable TEXT with no default: a row inserted
// without the column reads back NULL (the absence = "not populated" signal the
// presenter honors by omitting the `meta` field).
func TestMigration0008_AddsMetaJSONColumnNullable(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()

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
		if name == "meta_json" {
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
		t.Fatal("sources.meta_json column missing after migration 0008")
	}
	if notnull != 0 {
		t.Errorf("meta_json NOT NULL flag = %d, want 0 (nullable; absence = not populated)", notnull)
	}
	if typeAffin != "TEXT" {
		t.Errorf("meta_json type = %q, want TEXT", typeAffin)
	}
	if dflt.Valid {
		t.Errorf("meta_json default = %q, want no default (NULL by omission)", dflt.String)
	}

	// Inserting a row WITHOUT the column must leave it NULL.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src-meta','codex','/tmp',1000)`); err != nil {
		t.Fatalf("insert source without meta_json: %v", err)
	}
	var got sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT meta_json FROM sources WHERE id='src-meta'`).Scan(&got); err != nil {
		t.Fatalf("select meta_json: %v", err)
	}
	if got.Valid {
		t.Errorf("meta_json default value = %q, want NULL (nullable, no default)", got.String)
	}
}

// TestMigration0008_IsIdempotent re-runs the full migration chain over an
// already-migrated DB and asserts it is a no-op: the version stays '8' and the
// 0008 row is recorded exactly once. ALTER TABLE ADD COLUMN is not natively
// idempotent in SQLite, so the runner's per-file _schema_migrations tracking is
// what makes a second Up() skip 0008; this pins that guarantee.
func TestMigration0008_IsIdempotent(t *testing.T) {
	t.Parallel()
	db := openMigratedSQLite(t)
	ctx := context.Background()

	// Second Up() over the same handle must not error or re-apply 0008.
	if err := Up(ctx, db, nil); err != nil {
		t.Fatalf("second store.Up: %v", err)
	}

	var version string
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "8" {
		t.Fatalf("schema_meta.version after re-run = %q, want %q", version, "8")
	}

	if got := scanIntInternal(t, db,
		`SELECT COUNT(*) FROM _schema_migrations WHERE filename=?`, migration0008Name); got != 1 {
		t.Fatalf("_schema_migrations rows for %s = %d, want 1 (idempotent)", migration0008Name, got)
	}
}

// TestMigration0008_PreMigrationStoreIsRejected pins the serve-refusal contract:
// a store migrated only THROUGH 0007 carries version '7', which a v8 serve
// binary's presenter.CheckSchema rejects. We assert the on-disk value is '7'
// (not 8) here — the presenter package owns the CheckSchema assertion against
// SchemaVersion, kept out of this package to avoid an import cycle.
func TestMigration0008_PreMigrationStoreIsRejected(t *testing.T) {
	t.Parallel()
	db := openChainThrough(t, "0007_fts5_index_logs.sql")
	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version == "8" {
		t.Fatal("pre-0008 store reports version 8; expected the older 7 (serve must refuse it)")
	}
	if version != "7" {
		t.Fatalf("pre-0008 store version = %q, want %q", version, "7")
	}
	// The column must NOT yet exist before 0008 applies.
	var cnt int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('sources') WHERE name='meta_json'`).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info probe: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("meta_json present before 0008: count=%d, want 0", cnt)
	}
}
