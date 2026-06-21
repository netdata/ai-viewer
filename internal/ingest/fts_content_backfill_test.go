// BackfillFTSContent (SOW-0091) tests — exercise the rebuild on a
// synthetic ops + payload_refs dataset and verify fts_content ends up
// matching what extract.ReadableTextFromRef returns per op.
package ingest

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openInMemoryDB opens a fresh in-memory SQLite for backfill tests. The
// schema includes fts_content (migration 0010). Test fixtures seed the
// ops + payload_refs tables and assert the backfill populates
// fts_content correctly.
func openInMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	// The store package owns the schema. Importing it pulls in all
	// migrations. To avoid a full store.Open (which needs a file path),
	// we open a plain in-memory SQLite and apply the migrations
	// directly via embed. Skip the file-based path for simplicity —
	// the full store test suite covers the migration chain.
	//
	// We CAN'T just open a raw sqlite here because the migrations are
	// embedded in internal/store. Instead, copy the production DB
	// schema into a temp file by opening with a temp path. The
	// store.Open path also validates source roots; we bypass with a
	// custom minimal schema for THIS test.
	//
	// For correctness of THIS test, we replicate the exact CREATE
	// statements fts_content_backfill needs. If the schema drifts the
	// backfill test will break AND the integration test in
	// internal/store will break — both pin the contract.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range ftsContentBackfillTestSchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply schema stmt %q: %v", stmt, err)
		}
	}
	return db
}

// ftsContentBackfillTestSchema is the minimal schema subset
// BackfillFTSContent needs. Mirrors data-model.md exactly.
var ftsContentBackfillTestSchema = []string{
	`CREATE TABLE ops (
		id TEXT PRIMARY KEY NOT NULL,
		turn_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		name TEXT NOT NULL,
		start_ts INTEGER NOT NULL,
		end_ts INTEGER,
		duration_us INTEGER,
		status TEXT NOT NULL
	)`,
	`CREATE TABLE payload_refs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		op_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		location_uri TEXT NOT NULL,
		compression TEXT,
		original_bytes INTEGER,
		stored_bytes INTEGER,
		sha256 TEXT
	)`,
	`CREATE INDEX idx_payload_refs_op ON payload_refs(op_id, seq)`,
	`CREATE VIRTUAL TABLE fts_content USING fts5(
		text,
		op_id UNINDEXED,
		session_id UNINDEXED,
		turn_id UNINDEXED
	)`,
}

func TestBackfillFTSContent(t *testing.T) {
	db := openInMemoryDB(t)
	ctx := context.Background()

	// Seed two ops: one with a JSON envelope payload, one with
	// plain-text payload. Both should produce fts_content rows.
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env.json")
	if err := os.WriteFile(envPath, []byte(
		`{"payload":{"content":[{"type":"input_text","text":"hello world"}]}}`,
	), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	plainPath := filepath.Join(tmpDir, "plain.txt")
	if err := os.WriteFile(plainPath, []byte("plain prose, no envelope"), 0o600); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	// Insert ops + payload_refs.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
		 VALUES ('op-a', 't-1', 's-1', 1, 'llm', 'message', 1000, 'completed')`); err != nil {
		t.Fatalf("insert op-a: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
		 VALUES ('op-b', 't-1', 's-1', 2, 'tool', 'read_file', 2000, 'completed')`); err != nil {
		t.Fatalf("insert op-b: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO payload_refs (op_id, seq, kind, location_uri, original_bytes, stored_bytes)
		 VALUES ('op-a', 1, 'llm_response', 'file://`+envPath+`', 64, 64)`); err != nil {
		t.Fatalf("insert pr-a: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO payload_refs (op_id, seq, kind, location_uri, original_bytes, stored_bytes)
		 VALUES ('op-b', 1, 'tool_response', 'file://`+plainPath+`', 22, 22)`); err != nil {
		t.Fatalf("insert pr-b: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	stats, err := BackfillFTSContent(ctx, db, logger)
	if err != nil {
		t.Fatalf("BackfillFTSContent: %v", err)
	}
	if stats.IndexedRows != 2 {
		t.Errorf("IndexedRows: want 2, got %d", stats.IndexedRows)
	}
	if stats.EmptyRows != 0 {
		t.Errorf("EmptyRows: want 0, got %d", stats.EmptyRows)
	}

	// Verify fts_content contents.
	var aText, bText string
	if err := db.QueryRowContext(ctx, `SELECT text FROM fts_content WHERE op_id = 'op-a'`).Scan(&aText); err != nil {
		t.Fatalf("query fts_content op-a: %v", err)
	}
	if aText != "hello world" {
		t.Errorf("op-a text: want %q, got %q", "hello world", aText)
	}
	if err := db.QueryRowContext(ctx, `SELECT text FROM fts_content WHERE op_id = 'op-b'`).Scan(&bText); err != nil {
		t.Fatalf("query fts_content op-b: %v", err)
	}
	if bText != "plain prose, no envelope" {
		t.Errorf("op-b text: want %q, got %q", "plain prose, no envelope", bText)
	}
}

func TestBackfillFTSContent_Idempotent(t *testing.T) {
	db := openInMemoryDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, "env.json")
	if err := os.WriteFile(envPath, []byte(`{"text":"hello"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
		 VALUES ('op-x', 't-1', 's-1', 1, 'llm', 'message', 1000, 'completed')`); err != nil {
		t.Fatalf("insert op: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO payload_refs (op_id, seq, kind, location_uri, original_bytes, stored_bytes)
		 VALUES ('op-x', 1, 'llm_response', 'file://`+envPath+`', 14, 14)`); err != nil {
		t.Fatalf("insert pr: %v", err)
	}

	// Run twice — second run should produce the same row (idempotent).
	if _, err := BackfillFTSContent(ctx, db, logger); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if _, err := BackfillFTSContent(ctx, db, logger); err != nil {
		t.Fatalf("second backfill: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fts_content WHERE op_id = 'op-x'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("after two runs: want 1 fts_content row for op-x, got %d", count)
	}
}

func TestBackfillFTSContent_NoPayloadRefs(t *testing.T) {
	// Ops with NO payload_refs must be skipped — the INNER JOIN excludes
	// them. They get no fts_content row, which means /api/search won't
	// surface them. Operators find such ops via name/model filters.
	db := openInMemoryDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := db.ExecContext(ctx,
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
		 VALUES ('op-noop', 't-1', 's-1', 1, 'internal', 'no_payload', 1000, 'completed')`); err != nil {
		t.Fatalf("insert op: %v", err)
	}

	stats, err := BackfillFTSContent(ctx, db, logger)
	if err != nil {
		t.Fatalf("BackfillFTSContent: %v", err)
	}
	if stats.IndexedRows != 0 || stats.EmptyRows != 0 {
		t.Errorf("ops without payload_refs should be skipped, got stats %+v", stats)
	}
}
