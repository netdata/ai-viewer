package store_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/netdata/ai-viewer/internal/store"
)

// expectedTables lists every table the v1 schema must create. The list
// is the durable contract; if data-model.md adds a table, both this
// list and the migration SQL update in the same commit (enforced by
// spec-drift in CI once SOW-0013 lands).
var expectedTables = []string{
	"_schema_migrations",
	"catalog_agents",
	"catalog_cwds",
	"catalog_models",
	"catalog_providers",
	"catalog_tools",
	"log_entries",
	"ops",
	"payload_refs",
	"schema_meta",
	"sessions",
	"source_progress",
	"sources",
	"turns",
}

// silentLogger returns an slog.Logger that discards everything. Test
// output stays focused on assertions.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openInMemory opens a fresh in-memory store and registers Close on
// test cleanup. It returns the store and the underlying *sql.DB for
// convenience.
func openInMemory(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	s, err := store.Open(context.Background(), ":memory:", silentLogger())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("store.Close: %v", closeErr)
		}
	})
	return s, s.DB()
}

func listTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		// SQLite uses internal sqlite_* tables (e.g. sqlite_sequence) for
		// AUTOINCREMENT bookkeeping. Skip them so the contract list stays
		// focused on application schema.
		if len(name) >= 7 && name[:7] == "sqlite_" {
			continue
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestOpen_RunsMigrations asserts every contract table is created and
// schema_meta carries version='1'.
func TestOpen_RunsMigrations(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	got := listTables(t, db)
	if diff := cmp.Diff(expectedTables, got); diff != "" {
		t.Fatalf("table set mismatch (-want +got):\n%s", diff)
	}

	var version string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_meta.version: %v", err)
	}
	if version != "1" {
		t.Fatalf("schema_meta.version: want %q, got %q", "1", version)
	}

	var createdAt string
	if err := db.QueryRowContext(context.Background(),
		`SELECT value FROM schema_meta WHERE key='created_at'`).Scan(&createdAt); err != nil {
		t.Fatalf("read schema_meta.created_at: %v", err)
	}
	if createdAt == "" {
		t.Fatal("schema_meta.created_at: want non-empty value")
	}
}

// TestOpen_Idempotent verifies that closing and re-opening the same
// on-disk database does not re-apply migrations and produces no error.
func TestOpen_Idempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "idempotent.db")

	for round := 1; round <= 3; round++ {
		s, err := store.Open(context.Background(), dsn, silentLogger())
		if err != nil {
			t.Fatalf("round %d: store.Open: %v", round, err)
		}

		var count int
		if err := s.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM _schema_migrations`).Scan(&count); err != nil {
			t.Fatalf("round %d: count _schema_migrations: %v", round, err)
		}
		// expectedMigrations grows by one each time a new SQL file is
		// added under migrations/. Update the constant in lockstep with
		// the new migration so this contract test stays meaningful.
		const expectedMigrations = 2
		if count != expectedMigrations {
			t.Fatalf("round %d: _schema_migrations rows: want %d, got %d", round, expectedMigrations, count)
		}

		// Inserting the same schema_meta version twice would be a defect
		// only if the migration ran a second time without the INSERT OR
		// REPLACE guard. Sanity-check the version is still '1'.
		var version string
		if err := s.DB().QueryRowContext(context.Background(),
			`SELECT value FROM schema_meta WHERE key='version'`).Scan(&version); err != nil {
			t.Fatalf("round %d: read schema_meta.version: %v", round, err)
		}
		if version != "1" {
			t.Fatalf("round %d: schema_meta.version: want %q, got %q", round, "1", version)
		}

		if err := s.Close(); err != nil {
			t.Fatalf("round %d: store.Close: %v", round, err)
		}
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var on int
	if err := db.QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if on != 1 {
		t.Fatalf("PRAGMA foreign_keys: want 1, got %d", on)
	}
}

// TestOpen_JournalModeWAL asserts WAL mode is set on an on-disk
// database. The in-memory case is exercised separately because SQLite
// refuses WAL on :memory: (the mode falls back to "memory").
func TestOpen_JournalModeWAL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "wal.db")
	s, err := store.Open(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("store.Close: %v", closeErr)
		}
	})

	var mode string
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("PRAGMA journal_mode: want %q, got %q", "wal", mode)
	}
}

// TestOpenWriter_PinsMaxOpenConnsOnDisk pins the writer pool to a
// single connection. SQLite WAL allows many readers but only ONE
// writer; without this pin a multi-source ingester would experience
// SQLITE_BUSY races at BeginTx that the no-retry policy converts into
// dropped batches. The test confirms both the on-disk case (the one
// that regressed under iter-2 — previously gated on isMemoryDSN) and
// asserts that a second concurrent acquisition blocks via the pool
// rather than succeeding (codex iter-3 P2#5).
func TestOpenWriter_PinsMaxOpenConnsOnDisk(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "single-writer.db")
	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := s.DB().Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	// Acquire one conn and verify the pool grants no second.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c1, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("acquire first conn: %v", err)
	}
	defer func() { _ = c1.Close() }()

	gotSecond := make(chan error, 1)
	go func() {
		c2ctx, c2cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer c2cancel()
		c2, err := s.DB().Conn(c2ctx)
		if err == nil {
			_ = c2.Close()
		}
		gotSecond <- err
	}()
	err = <-gotSecond
	if err == nil {
		t.Fatal("expected second Conn() to time out while first is held, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded from blocked second Conn, got %v", err)
	}
}

// TestOpenReader_PinsMaxOpenConnsToEight pins the reader pool size to
// the value documented in presenter.md §"SQLite Access" (8). Go's
// database/sql default is unbounded, which would surface as a runtime
// regression vs the spec (codex iter-3 P2#5).
func TestOpenReader_PinsMaxOpenConnsToEight(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "reader-pool.db")

	// Bootstrap the file via a writer + close so the reader has
	// something to open.
	ws, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter (bootstrap): %v", err)
	}
	if err := ws.Close(); err != nil {
		t.Fatalf("close writer bootstrap: %v", err)
	}

	rs, err := store.OpenReader(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	if got := rs.DB().Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("OpenReader MaxOpenConnections = %d, want 8 (presenter.md §SQLite Access)", got)
	}
}

func TestStore_CloseNilSafe(t *testing.T) {
	t.Parallel()

	var s *store.Store
	if err := s.Close(); err != nil {
		t.Fatalf("Close on nil receiver: want nil, got %v", err)
	}
}

func TestStore_DBNilSafe(t *testing.T) {
	t.Parallel()

	var s *store.Store
	if got := s.DB(); got != nil {
		t.Fatalf("DB on nil receiver: want nil, got %v", got)
	}
}

// TestOpen_NilLoggerUsesDefault verifies the documented fallback to
// slog.Default when the caller passes nil. We exercise the open path
// and confirm a usable Store comes back.
func TestOpen_NilLoggerUsesDefault(t *testing.T) {
	t.Parallel()

	s, err := store.Open(context.Background(), ":memory:", nil)
	if err != nil {
		t.Fatalf("store.Open with nil logger: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("store.Close: %v", closeErr)
		}
	})
	if s.DB() == nil {
		t.Fatal("DB(): want non-nil")
	}
}

// TestOpen_BadDSN exercises the error path when the driver rejects the
// DSN at first use. The intermediate directory does not exist inside
// the test's TempDir, so SQLite errors when it tries to create the
// file. Using TempDir makes the test deterministic regardless of the
// caller's working directory.
func TestOpen_BadDSN(t *testing.T) {
	t.Parallel()

	dsn := filepath.Join(t.TempDir(), "nonexistent-sub", "test.db")
	s, err := store.Open(context.Background(), dsn, silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("store.Open: want error for nonexistent parent dir, got nil")
	}
}

// TestOpen_CtxCancelled exercises ctx cancellation through migration
// application. The cancellation aborts the migration transaction and
// surfaces as an error from Open.
func TestOpen_CtxCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "cancelled.db")
	s, err := store.Open(ctx, dsn, silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("store.Open with cancelled ctx: want error, got nil")
	}
}

// TestStore_CloseTwice ensures Close is idempotent — a second call on
// an already-closed store returns nil rather than reporting
// sql.ErrConnDone.
func TestStore_CloseTwice(t *testing.T) {
	t.Parallel()

	s, err := store.Open(context.Background(), ":memory:", silentLogger())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestOpenReader_ReadsExistingDB writes via OpenWriter, closes, then
// re-opens read-only and verifies a SELECT works while a write fails
// because query_only(true) is in effect. This pins the writer/reader
// split contract documented in doc.go.
func TestOpenReader_ReadsExistingDB(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "reader.db")

	// Phase 1: writer creates the schema and seeds a row.
	w, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if _, err := w.DB().ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at) VALUES (?, ?, ?, ?)`,
		"src-r1", "aiagent-v3", "/tmp/x", 1_700_000_000_000_000); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	// Phase 2: reader opens read-only and reads the row back.
	r, err := store.OpenReader(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	var got string
	if err := r.DB().QueryRowContext(context.Background(),
		`SELECT id FROM sources WHERE id = 'src-r1'`).Scan(&got); err != nil {
		t.Fatalf("reader SELECT: %v", err)
	}
	if got != "src-r1" {
		t.Fatalf("reader SELECT: want %q, got %q", "src-r1", got)
	}

	// Defense in depth: writes through the reader must fail because of
	// query_only(true).
	if _, err := r.DB().ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at) VALUES (?, ?, ?, ?)`,
		"src-r2", "aiagent-v3", "/tmp/y", 1_700_000_000_000_000); err == nil {
		t.Fatal("reader INSERT: want error from query_only, got nil")
	}
}

// TestOpenReader_NilLoggerUsesDefault asserts OpenReader honours the
// same nil-logger fallback as OpenWriter.
func TestOpenReader_NilLoggerUsesDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "ro-defaultlogger.db")

	w, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	r, err := store.OpenReader(context.Background(), dsn, nil)
	if err != nil {
		t.Fatalf("OpenReader with nil logger: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if r.DB() == nil {
		t.Fatal("OpenReader.DB: want non-nil")
	}
}

// TestOpenWriter_PreservesOperatorPragmas ensures that a caller who
// already encoded a _pragma in the DSN (e.g. `cache_size`) is not
// silently overridden by buildDSN. Required-by-store pragmas are still
// applied; the caller's values for any other pragma round-trip.
func TestOpenWriter_PreservesOperatorPragmas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// A URI DSN with an existing _pragma the caller chose. The store
	// must keep cache_size as the caller wrote it and still apply
	// foreign_keys + busy_timeout itself.
	dsn := "file:" + filepath.Join(dir, "preserve.db") + "?_pragma=cache_size(-2000)"

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var cacheSize int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA cache_size`).Scan(&cacheSize); err != nil {
		t.Fatalf("PRAGMA cache_size: %v", err)
	}
	if cacheSize != -2000 {
		t.Fatalf("cache_size: want operator value -2000, got %d", cacheSize)
	}

	// Store-required pragma still in effect.
	var fk int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys: want 1, got %d", fk)
	}
}

// TestOpenReader_RejectsMissingFile pins the contract that opening a
// reader against a non-existent file FAILS at the OpenReader call
// itself, not at some later query. OpenReader now pings the database
// immediately after sql.Open so the missing-file error surfaces here.
// Two regressions are covered:
//
//  1. The original implementation passed a raw path-style DSN to
//     modernc.org/sqlite, which strips the query string for
//     non-"file:" DSNs (conn.go:53-55); mode=ro was silently dropped
//     and the driver then opened with
//     SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE, producing an empty
//     database. The pathToFileURI rewrite + mode=ro on the reader's
//     DSN restores OS-level enforcement so the file is not created.
//  2. sql.Open is lazy; without an explicit Ping in OpenReader the
//     call returned success against a missing file, with the error
//     materialising only on the first query. The Ping now makes
//     OpenReader itself return the error.
func TestOpenReader_RejectsMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "definitely-not-here.db")

	// Sanity: the file must not exist before the call so any creation
	// is attributable to the reader.
	if _, err := os.Stat(dsn); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("precondition: expected missing file, stat returned %v", err)
	}

	// OpenReader itself must return an error AND no *Store. No
	// fall-through to PingContext is permitted by the contract.
	s, err := store.OpenReader(context.Background(), dsn, silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatalf("OpenReader on missing file: want error, got nil")
	}
	if s != nil {
		_ = s.Close()
		t.Fatalf("OpenReader on missing file: want nil *Store, got %v", s)
	}

	// Critical assertion: the file must not have been created.
	if _, err := os.Stat(dsn); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenReader created missing file at %s: stat err=%v", dsn, err)
	}
}

// TestOpenReader_ModeROAtOSLevel exercises the OS-level mode=ro
// enforcement: even before query_only(true) executes, a malicious or
// buggy operator DSN that tries to flip mode back to rwc must not
// succeed in producing a writable connection.
func TestOpenReader_ModeROAtOSLevel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "ro-os.db")

	// Seed the DB with the writer.
	w, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	// Operator tried to override mode. Reader must still be read-only.
	roDSN := "file:" + dsn + "?mode=rwc"
	r, err := store.OpenReader(context.Background(), roDSN, silentLogger())
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if _, err := r.DB().ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at) VALUES (?, ?, ?, ?)`,
		"x", "aiagent-v3", "/tmp/x", 1_700_000_000_000_000); err == nil {
		t.Fatal("reader INSERT with operator mode=rwc: want error, got nil")
	}
}

// TestOpenWriter_OperatorCannotWeakenSynchronous is the runtime
// counterpart to the buildDSN-level synchronous test. It proves the
// fix works at the actual SQLite layer, not just in the encoded DSN
// string. An operator DSN that asks for `synchronous(off)` is the
// motivating case: with the driver's alphabetical sort, `(off)` would
// have sorted AFTER `(normal)` and an "append last" strategy would
// have silently honoured the operator. The strip-then-append approach
// guarantees only `synchronous(normal)` reaches the connection.
func TestOpenWriter_OperatorCannotWeakenSynchronous(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "sync-override.db") + "?_pragma=synchronous(off)"

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// PRAGMA synchronous returns the integer code: 0=OFF, 1=NORMAL,
	// 2=FULL, 3=EXTRA. The store's contract is NORMAL (1).
	var sync int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA synchronous`).Scan(&sync); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if sync != 1 {
		t.Fatalf("synchronous: store must override operator off; want 1 (NORMAL), got %d", sync)
	}
}

// TestOpenWriter_OperatorCannotOverrideJournalMode is the runtime
// counterpart for journal_mode. An operator-supplied `journal_mode(off)`
// is stripped; the connection must end up in WAL mode.
func TestOpenWriter_OperatorCannotOverrideJournalMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "jm-override.db") + "?_pragma=journal_mode(off)"

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var mode string
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode: store must override operator off; want %q, got %q", "wal", mode)
	}
}

// TestOpenWriter_OperatorCannotDisableForeignKeys asserts the
// override contract end-to-end: an operator DSN that tries to
// turn off foreign_keys via _pragma still ends up with foreign_keys
// enabled on the actual connection. buildDSN strips the operator's
// `(off)` and appends `(on)`, so only the store's value reaches the
// driver — independent of how the driver orders pragmas internally.
func TestOpenWriter_OperatorCannotDisableForeignKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "fk-override.db") + "?_pragma=foreign_keys(off)"

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var fk int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys: store must override operator off; got %d", fk)
	}
}

// TestOpenWriter_PathDSNGetsFileURI confirms that a raw path DSN is
// rewritten into "file:" URI form before sql.Open, which is what
// allows the _pragma query parameters to survive. We exercise this
// indirectly by asserting foreign_keys is enabled — the only way that
// can be true with the new buildDSN is if the _pragma values reached
// the driver, and the only way THAT can be true for a path DSN is via
// the pathToFileURI rewrite.
func TestOpenWriter_PathDSNGetsFileURI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "raw-path.db")

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var fk int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys for path DSN: want 1, got %d", fk)
	}
}

// TestOpenWriter_RejectsEmptyDSN exercises the pathToFileURI error
// branch via OpenWriter. An empty DSN is a contract violation rather
// than a request to open the cwd; we want it to fail-fast so the
// operator notices the misconfiguration immediately.
func TestOpenWriter_RejectsEmptyDSN(t *testing.T) {
	t.Parallel()

	s, err := store.OpenWriter(context.Background(), "", silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("OpenWriter on empty DSN: want error, got nil")
	}
}

// TestOpenReader_RejectsEmptyDSN mirrors the writer empty-DSN test for
// the reader path.
func TestOpenReader_RejectsEmptyDSN(t *testing.T) {
	t.Parallel()

	s, err := store.OpenReader(context.Background(), "", silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("OpenReader on empty DSN: want error, got nil")
	}
}

// TestOpenWriter_RejectsMalformedDSN ensures buildDSN's fail-fast
// behaviour surfaces through OpenWriter rather than producing a
// partially-configured store.
func TestOpenWriter_RejectsMalformedDSN(t *testing.T) {
	t.Parallel()

	// "%zz" is an invalid percent-encoded sequence.
	s, err := store.OpenWriter(context.Background(),
		"file:/tmp/whatever.db?broken=%zz", silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("OpenWriter on malformed DSN: want error, got nil")
	}
}

// TestOpenReader_RejectsMalformedDSN mirrors the writer fail-fast test
// for the reader path.
func TestOpenReader_RejectsMalformedDSN(t *testing.T) {
	t.Parallel()

	s, err := store.OpenReader(context.Background(),
		"file:/tmp/whatever.db?broken=%zz", silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("OpenReader on malformed DSN: want error, got nil")
	}
}

// TestOpenWriter_OperatorCannotDisableForeignKeys_SchemaQualified is
// the runtime end-to-end pin for Fix A5: an operator who writes
// `_pragma=main.foreign_keys(off)` (the schema-qualified bypass) must
// still get foreign_keys ENABLED on the resulting connection.
// pragmaName strips the `main.` qualifier so the value is recognised
// and stripped before reaching the driver. Without the fix the driver
// would execute both `foreign_keys(on)` and `main.foreign_keys(off)`
// in alphabetical order, with the operator's `off` winning at runtime.
func TestOpenWriter_OperatorCannotDisableForeignKeys_SchemaQualified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "fk-qualified.db") + "?_pragma=main.foreign_keys(off)"

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var fk int
	if err := s.DB().QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys (schema-qualified override): want 1, got %d", fk)
	}
}

// TestOpenReader_OperatorCannotDisableQueryOnly_SchemaQualified is
// the reader-side runtime pin for Fix A5. The operator's
// `_pragma=temp.query_only(false)` is stripped; the resulting
// connection enforces query_only(true). The reader path needs an
// already-existing file because mode=ro refuses to create one, so we
// first OpenWriter+Close to seed it.
func TestOpenReader_OperatorCannotDisableQueryOnly_SchemaQualified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "qo-qualified.db")

	// Seed an empty database so the reader has something to open.
	w, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter seed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	roDSN := "file:" + dsn + "?_pragma=temp.query_only(false)"
	r, err := store.OpenReader(context.Background(), roDSN, silentLogger())
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	var qo int
	if err := r.DB().QueryRowContext(context.Background(),
		`PRAGMA query_only`).Scan(&qo); err != nil {
		t.Fatalf("PRAGMA query_only: %v", err)
	}
	if qo != 1 {
		t.Fatalf("query_only (schema-qualified override): want 1, got %d", qo)
	}

	// Defence in depth: writes through this reader must fail.
	if _, err := r.DB().ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at) VALUES (?, ?, ?, ?)`,
		"src-qo", "aiagent-v3", "/tmp/qo", 1_700_000_000_000_000); err == nil {
		t.Fatal("reader INSERT with qualified query_only(false): want error, got nil")
	}
}

// TestOpenWriter_FailsOnTaintedSchema covers the OpenWriter `Up()`
// failure branch (previously suppressed in the Gate Suppression
// table). We pre-create a `sessions` table with an incompatible shape
// on a file-backed DSN, then call OpenWriter. The migration must fail
// inside Up, which OpenWriter wraps as `apply migrations: ...`. This
// hits the deferred db.Close + error-return branch at the bottom of
// OpenWriter that no other test exercises.
func TestOpenWriter_FailsOnTaintedSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "tainted.db")

	// Seed an incompatible sessions table directly through the raw
	// driver so OpenWriter sees a tainted file when it runs migrations.
	seed, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("seed sql.Open: %v", err)
	}
	if _, err := seed.ExecContext(context.Background(),
		`CREATE TABLE sessions (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed CREATE TABLE: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err == nil {
		_ = s.Close()
		t.Fatal("OpenWriter against tainted schema: want error, got nil")
	}
	if !strings.Contains(err.Error(), "apply migrations") {
		t.Fatalf("OpenWriter error: want wrap %q, got %v", "apply migrations", err)
	}
}

// TestOpenWriter_ForeignKeysAcrossPooledConns is the regression test
// for the original PRAGMA-on-the-pool bug: with PRAGMAs applied via
// _pragma DSN params, every connection in the database/sql pool has
// foreign_keys enabled, including ones the pool opens after the first.
// We force two distinct connections with SetMaxOpenConns(2) and
// confirm the pragma is set on a freshly opened connection.
func TestOpenWriter_ForeignKeysAcrossPooledConns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := filepath.Join(dir, "pool.db")

	s, err := store.OpenWriter(context.Background(), dsn, silentLogger())
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	s.DB().SetMaxOpenConns(2)

	// Hold one connection open while we ask the pool to allocate
	// another for the next query.
	c1, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire first conn: %v", err)
	}
	defer func() { _ = c1.Close() }()

	c2, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire second conn: %v", err)
	}
	defer func() { _ = c2.Close() }()

	var fk int
	if err := c2.QueryRowContext(context.Background(),
		`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys on second conn: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys on pooled conn: want 1, got %d", fk)
	}
}
