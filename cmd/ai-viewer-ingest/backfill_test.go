// Tests for the `ai-viewer-ingest rollups-backfill` subcommand dispatch.
// The heavy rollup logic is unit-tested in internal/ingest; this file pins
// the THIN cmd layer: subcommand routing, flag parsing, store open, and the
// exit-code contract.
package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// migratedDBPath creates a fresh on-disk DB (migrated to the current schema)
// and returns its path. The backfill cmd opens its OWN store, so the file must
// exist on disk — not :memory:.
func migratedDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	s, err := store.OpenWriter(context.Background(), path, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter(%s): %v", path, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return path
}

// seedClosedHourOp inserts the source/session/turn/op chain for one closed
// op directly via a short-lived writer connection on the same DB file.
func seedClosedHourOp(t *testing.T, path string) {
	t.Helper()
	s, err := store.OpenWriter(context.Background(), path, silentLogger())
	if err != nil {
		t.Fatalf("reopen for seed: %v", err)
	}
	defer func() { _ = s.Close() }()
	db := s.DB()

	// A clean past hour well before now so the bucket is always closed.
	start := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC).UnixMicro()
	end := start + 1_000_000
	exec := func(q string, args ...any) {
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO sources (id, format, location, enabled, parse_errors, created_at)
	      VALUES ('codex:/loc','codex','/loc',1,0,1)`)
	exec(`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, agent_name, cwd,
	                            status, start_ts, last_activity_ts)
	      VALUES ('sess','codex:/loc','sess','sess','root','a','/w','completed',?,?)`, start, start)
	exec(`INSERT INTO turns (id, session_id, seq, start_ts, status)
	      VALUES ('turn','sess',1,?,'completed')`, start)
	exec(`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, model, provider,
	                       start_ts, end_ts, duration_us, status, tokens_in)
	      VALUES ('op','turn','sess',1,'llm','chat','m','p',?,?,1000000,'completed',42)`, start, end)
}

// countRows is a tiny on-disk-DB row counter used by the assertions below.
func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var n int
	// #nosec G201 -- table is a test-local constant, never user input.
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestRun_RollupsBackfillDispatch verifies that `run` routes the
// "rollups-backfill" subcommand, exits 0, and materializes rollup rows.
func TestRun_RollupsBackfillDispatch(t *testing.T) {
	path := migratedDBPath(t)
	seedClosedHourOp(t, path)

	stderr, read := captureStderr(t)
	code := run([]string{"rollups-backfill", "--db", path, "--log-level", "error"}, stderr, stderr)
	if code != 0 {
		t.Fatalf("run(rollups-backfill) exit = %d, want 0; stderr=%q", code, read())
	}
	if n := countRows(t, path, "rollup_hourly"); n == 0 {
		t.Fatalf("rollup_hourly empty after backfill; stderr=%q", read())
	}
}

// TestRun_RollupsBackfillBadDB verifies the subcommand returns exit 1 when the
// store cannot be opened (unwritable parent path), with the error surfaced.
func TestRun_RollupsBackfillBadDB(t *testing.T) {
	stderr, _ := captureStderr(t)
	bad := filepath.Join(t.TempDir(), "does-not-exist", "nested", "index.db")
	code := run([]string{"rollups-backfill", "--db", bad}, stderr, stderr)
	if code == 0 {
		t.Fatal("run(rollups-backfill) on unwritable DB path returned 0, want non-zero")
	}
}
