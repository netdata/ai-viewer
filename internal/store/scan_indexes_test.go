package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/netdata/ai-viewer/internal/store"
)

// TestScanIndexLifecycle_DropRecreate pins the SOW-0118 index-drop lifecycle:
// (1) DropNonUniqueIndexes drops every non-unique CREATE INDEX;
// (2) the two UNIQUE constraints (the upsert ON CONFLICT targets) SURVIVE the
//
//	drop (the data-loss footgun from the naive drop);
//
// (3) RecreateIndexes restores all dropped indexes.
func TestScanIndexLifecycle_DropRecreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openInMemory(t)

	// Count indexes before drop.
	before := countNonUniqueIndexes(t, ctx, db)
	if before == 0 {
		t.Fatal("expected non-unique indexes to exist after migrations")
	}
	beforeUnique := countUniqueIndexes(t, ctx, db)
	if beforeUnique < 2 {
		t.Fatalf("expected ≥2 UNIQUE indexes (payload_refs + log_entries), got %d", beforeUnique)
	}

	// Drop.
	defs, err := store.DropNonUniqueIndexes(ctx, db)
	if err != nil {
		t.Fatalf("DropNonUniqueIndexes: %v", err)
	}
	if len(defs) != before {
		t.Fatalf("dropped %d, expected %d", len(defs), before)
	}

	// The CRITICAL check: UNIQUE indexes survive (the data-loss footgun).
	afterUnique := countUniqueIndexes(t, ctx, db)
	if afterUnique != beforeUnique {
		t.Fatalf("UNIQUE indexes changed: %d -> %d (the naive drop dropped the ON CONFLICT targets — data loss!)", beforeUnique, afterUnique)
	}
	afterNonUnique := countNonUniqueIndexes(t, ctx, db)
	if afterNonUnique != 0 {
		t.Fatalf("non-unique indexes after drop: %d (expected 0)", afterNonUnique)
	}

	// Recreate.
	if err := store.RecreateIndexes(ctx, db, defs); err != nil {
		t.Fatalf("RecreateIndexes: %v", err)
	}
	afterRecreate := countNonUniqueIndexes(t, ctx, db)
	if afterRecreate != before {
		t.Fatalf("non-unique indexes after recreate: %d, expected %d (full restore)", afterRecreate, before)
	}

	// The UNIQUE indexes are still there (not duplicated).
	finalUnique := countUniqueIndexes(t, ctx, db)
	if finalUnique != beforeUnique {
		t.Fatalf("UNIQUE indexes changed after recreate: %d -> %d", beforeUnique, finalUnique)
	}
}

func countNonUniqueIndexes(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND sql NOT LIKE '%UNIQUE%'`).
		Scan(&n); err != nil {
		t.Fatalf("count non-unique: %v", err)
	}
	return n
}

func countUniqueIndexes(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND sql LIKE '%UNIQUE%'`).
		Scan(&n); err != nil {
		t.Fatalf("count unique: %v", err)
	}
	return n
}

// TestScanIndexLifecycle_UpsertStillWorks proves that after dropping non-unique
// indexes, the payload_refs upsert (ON CONFLICT (op_id, kind, location_uri))
// still works — the UNIQUE constraint it depends on was preserved. This is the
// exact failure that caused the data-loss incident.
func TestScanIndexLifecycle_UpsertStillWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := openInMemory(t)

	// Drop non-unique indexes.
	if _, err := store.DropNonUniqueIndexes(ctx, db); err != nil {
		t.Fatalf("drop: %v", err)
	}

	// Insert a source + session + turn + op so payload_refs FK resolves.
	seedMinimalForPayloadRef(t, ctx, db)

	// Upsert a payload_ref — this uses ON CONFLICT (op_id, kind, location_uri)
	// which requires the UNIQUE index idx_payload_refs_identity.
	insertSQL := `INSERT INTO payload_refs (op_id, kind, format, compression, location_uri, original_bytes, stored_bytes, sha256)
VALUES ('op1', 'request', 'json', 'gzip', 'file:///test', 100, 50, 'abc123')
ON CONFLICT (op_id, kind, location_uri) DO UPDATE SET
    original_bytes = excluded.original_bytes,
    stored_bytes = excluded.stored_bytes`

	if _, err := db.ExecContext(ctx, insertSQL); err != nil {
		t.Fatalf("payload_ref upsert after index-drop failed (UNIQUE constraint was dropped!): %v", err)
	}

	// Re-upsert (conflict path).
	if _, err := db.ExecContext(ctx, insertSQL); err != nil {
		t.Fatalf("payload_ref re-upsert (conflict) failed: %v", err)
	}

	_ = s // keep the store alive
}

func seedMinimalForPayloadRef(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO sources (id, format, location, created_at) VALUES ('src1', 'test', '/test', 1)`,
		`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts) VALUES ('sess1', 'src1', 'n1', 'sess1', 'root', 'completed', 1, 1)`,
		`INSERT INTO turns (id, session_id, seq, start_ts, status) VALUES ('turn1', 'sess1', 1, 1, 'completed')`,
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status) VALUES ('op1', 'turn1', 'sess1', 1, 'tool', 'test', 1, 'completed')`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
