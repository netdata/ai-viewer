package store_test

import (
	"context"
	"testing"
)

// Migration 0014 (SOW-0114) adds source read-model repair indexes. They let
// repair page source sessions first, then per-session ops/logs by rowid, instead
// of scanning unrelated source rows while Tail heartbeat writes wait.

func TestMigration0014_ChainHeadSchemaVersion(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "16" {
		t.Fatalf("schema_meta.version: want %q, got %q (full chain head is 0016)", "16", version)
	}
}

func TestMigration0014_SourceRepairLivenessIndexesExist(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	want := map[string]string{
		"idx_sessions_source_id": "sessions(source_id, id)",
		"idx_ops_session":        "ops(session_id)",
		"idx_log_session":        "log_entries(session_id)",
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
