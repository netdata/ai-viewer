package store

import (
	"context"
	"testing"
)

const migration0014Name = "0014_source_repair_liveness_indexes.sql"

func migration0014SQL(t *testing.T) string {
	t.Helper()
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for _, m := range all {
		if m.name == migration0014Name {
			return m.sql
		}
	}
	t.Fatalf("embedded migration %q not found (have %d migrations)", migration0014Name, len(all))
	return ""
}

func TestMigration0014_ClearsDerivedFTSRowsForStableDocIDRekey(t *testing.T) {
	t.Parallel()

	db := openChainThrough(t, "0013_aggregate_liveness_indexes.sql")
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
INSERT INTO fts_ops(rowid, name, op_id, session_id)
VALUES (900, 'old-docid op row', 'op-old', 'session-old');
INSERT INTO fts_logs(rowid, message, log_id, session_id)
VALUES (800, 'old-docid log row', 123, 'session-old');
`); err != nil {
		t.Fatalf("seed old-docid FTS rows: %v", err)
	}

	if _, err := db.ExecContext(ctx, migration0014SQL(t)); err != nil {
		t.Fatalf("apply migration 0014: %v", err)
	}

	if got := scanIntInternal(t, db, `SELECT COUNT(*) FROM fts_ops`); got != 0 {
		t.Fatalf("fts_ops rows after migration 0014 = %d, want 0", got)
	}
	if got := scanIntInternal(t, db, `SELECT COUNT(*) FROM fts_logs`); got != 0 {
		t.Fatalf("fts_logs rows after migration 0014 = %d, want 0", got)
	}
	var version string
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "14" {
		t.Fatalf("schema_meta.version = %q, want %q", version, "14")
	}
}
