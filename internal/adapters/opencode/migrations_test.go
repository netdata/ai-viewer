package opencode

import (
	"database/sql"
	"errors"
	"testing"
)

// drizzleMigrationsDDL mirrors opencode's real __drizzle_migrations shape
// (adapter-opencode.md §"__drizzle_migrations"): an auto-increment id that
// increases in application order plus a nullable name column. Synthetic only —
// never the operator's database (SOW-0005 R5).
const drizzleMigrationsDDL = `CREATE TABLE __drizzle_migrations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	hash TEXT NOT NULL,
	created_at NUMERIC,
	name TEXT,
	applied_at TEXT)`

// insertMigration inserts one applied-migration row. id is assigned by
// AUTOINCREMENT in call order so the natural application order matches insertion
// order.
func insertMigration(t *testing.T, rw *sql.DB, name string) {
	t.Helper()
	if _, err := rw.Exec(
		`INSERT INTO __drizzle_migrations (hash, name, applied_at) VALUES (?,?,?)`,
		"hash_"+name, name, "2026-05-30"); err != nil {
		t.Fatalf("insert migration %q: %v", name, err)
	}
}

// newMigrationsDB builds a current-schema DB that ALSO carries a populated
// __drizzle_migrations table with the given names (in application order), and
// returns the read-only handle. The rw handle is closed before reopening RO so
// the WAL is flushed.
func newMigrationsDB(t *testing.T, names ...string) *sql.DB {
	t.Helper()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db", drizzleMigrationsDDL)
	for _, n := range names {
		insertMigration(t, rw, n)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	return openRO(t, path)
}

// TestReadMigrations_OrderedAndLatest pins that readMigrations returns the names
// in application order (id ASC) and reports the highest-id name as latest, even
// when the names were inserted out of timestamp order (id, not name, is the
// applied-order key Drizzle maintains).
func TestReadMigrations_OrderedAndLatest(t *testing.T) {
	t.Parallel()
	db := newMigrationsDB(t,
		"20260127222353_familiar_lady_ursula",
		"20260510033149_session_usage",
		"20260511000411_data_migration_state",
	)
	names, latest, err := readMigrations(ctxBG(), db)
	if err != nil {
		t.Fatalf("readMigrations: %v", err)
	}
	want := []string{
		"20260127222353_familiar_lady_ursula",
		"20260510033149_session_usage",
		"20260511000411_data_migration_state",
	}
	if len(names) != len(want) {
		t.Fatalf("readMigrations names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("readMigrations names[%d] = %q, want %q (application order)", i, names[i], want[i])
		}
	}
	if latest != want[len(want)-1] {
		t.Fatalf("latest = %q, want %q", latest, want[len(want)-1])
	}
}

// TestReadMigrations_SkipsNullNames verifies a NULL/empty name row never pollutes
// the list or becomes the latest (the name column is nullable).
func TestReadMigrations_SkipsNullNames(t *testing.T) {
	t.Parallel()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db", drizzleMigrationsDDL)
	insertMigration(t, rw, "20260127222353_first")
	// A row with a NULL name (real schema allows it).
	if _, err := rw.Exec(`INSERT INTO __drizzle_migrations (hash, name) VALUES (?, NULL)`, "h_null"); err != nil {
		t.Fatalf("insert null-name migration: %v", err)
	}
	insertMigration(t, rw, "20260510033149_last")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)

	names, latest, err := readMigrations(ctxBG(), db)
	if err != nil {
		t.Fatalf("readMigrations: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("readMigrations names = %v, want 2 (NULL skipped)", names)
	}
	if latest != "20260510033149_last" {
		t.Fatalf("latest = %q, want the last non-null name", latest)
	}
}

// TestReadMigrations_MissingTableSentinel verifies a DB WITHOUT a
// __drizzle_migrations table (a very old or foreign SQLite file) returns the soft
// sentinel + empty results, not a hard error — so callers degrade gracefully.
func TestReadMigrations_MissingTableSentinel(t *testing.T) {
	t.Parallel()
	// newEmptyDB without the migrations DDL → no __drizzle_migrations table.
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)

	names, latest, err := readMigrations(ctxBG(), db)
	if !errors.Is(err, errNoMigrationsTable) {
		t.Fatalf("readMigrations(no table) err = %v, want errNoMigrationsTable", err)
	}
	if names != nil || latest != "" {
		t.Fatalf("readMigrations(no table) = (%v,%q), want (nil,\"\")", names, latest)
	}
}

// TestSchemaHash_StableOrderSensitiveAndDistinct pins the schema-hash contract:
// stable for the same ordered list, different for a different order, different
// for different content, and empty for an empty list.
func TestSchemaHash_StableOrderSensitiveAndDistinct(t *testing.T) {
	t.Parallel()
	a := []string{"m1", "m2", "m3"}
	if schemaHash(a) != schemaHash([]string{"m1", "m2", "m3"}) {
		t.Error("schemaHash not stable for the same ordered list")
	}
	if schemaHash(a) == schemaHash([]string{"m1", "m3", "m2"}) {
		t.Error("schemaHash not order-sensitive (reordered names hashed the same)")
	}
	if schemaHash(a) == schemaHash([]string{"m1", "m2"}) {
		t.Error("schemaHash collided for different lists")
	}
	if schemaHash(nil) != "" || schemaHash([]string{}) != "" {
		t.Error("schemaHash(empty) must be \"\"")
	}
	// A separator-injection guard: ["m1\nm2"] must not collide with ["m1","m2"]
	// — newline join is unambiguous because a migration name has no newline.
	if schemaHash([]string{"m1\nm2"}) == schemaHash([]string{"m1", "m2"}) {
		t.Error("schemaHash join is ambiguous (newline-in-name collides with two names)")
	}
}

// TestReadSchemaHash_RealMigrations verifies readSchemaHash returns the digest of
// the live migration names and "" (no error) when the table is absent.
func TestReadSchemaHash_RealMigrations(t *testing.T) {
	t.Parallel()
	db := newMigrationsDB(t, "20260127222353_a", "20260510033149_b")
	got, err := readSchemaHash(ctxBG(), db)
	if err != nil {
		t.Fatalf("readSchemaHash: %v", err)
	}
	want := schemaHash([]string{"20260127222353_a", "20260510033149_b"})
	if got != want {
		t.Fatalf("readSchemaHash = %q, want %q", got, want)
	}

	// Missing table → "" + nil (degrade, do not error).
	pathNo, rwNo := newEmptyDB(t, t.TempDir(), "opencode.db")
	if err := rwNo.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	dbNo := openRO(t, pathNo)
	if h, err := readSchemaHash(ctxBG(), dbNo); err != nil || h != "" {
		t.Fatalf("readSchemaHash(no table) = (%q,%v), want (\"\",nil)", h, err)
	}
}

// TestRecordSchemaHash_RecordsReal asserts recordSchemaHash stamps the cursor with
// the REAL migration-name digest (replacing chunk C's present-column placeholder).
func TestRecordSchemaHash_RecordsReal(t *testing.T) {
	t.Parallel()
	db := newMigrationsDB(t, "20260127222353_a", "20260510033149_b")
	var ce collectErrs
	got := recordSchemaHash(ctxBG(), db, newCursor(), ce.onError)
	want := schemaHash([]string{"20260127222353_a", "20260510033149_b"})
	if got.SchemaHash != want {
		t.Fatalf("recordSchemaHash SchemaHash = %q, want %q (real migration digest)", got.SchemaHash, want)
	}
	if ce.count() != 0 {
		t.Errorf("recordSchemaHash on a fresh cursor logged %d errors, want 0", ce.count())
	}
}

// TestRecordSchemaHash_MismatchContinuesPreservingWatermarks pins the spec
// behaviour (adapter-opencode.md §"Cursor"): a cursor carrying a STALE hash
// (opencode applied a migration between runs) is re-stamped with the new hash,
// the watermarks are PRESERVED (no reset), and a structured WARN is surfaced via
// onError. Column drift is handled per-column by the dynamic SELECT, so a benign
// migration never forces a re-ingest.
func TestRecordSchemaHash_MismatchContinuesPreservingWatermarks(t *testing.T) {
	t.Parallel()
	db := newMigrationsDB(t, "20260127222353_a", "20260510033149_b")
	newHash := schemaHash([]string{"20260127222353_a", "20260510033149_b"})

	// A persisted cursor from an EARLIER schema (stale hash) with live watermarks.
	stale := newCursor().
		withSchemaHash("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef").
		withTable("session", TableWatermark{MaxIDSeen: "ses_9", MaxTimeUpdatedMs: 1779, MaxTimeUpdatedID: "ses_9"}).
		withTable("message", TableWatermark{MaxIDSeen: "msg_9", MaxTimeUpdatedMs: 1780, MaxTimeUpdatedID: "msg_9"})

	var ce collectErrs
	got := recordSchemaHash(ctxBG(), db, stale, ce.onError)

	if got.SchemaHash != newHash {
		t.Fatalf("hash not re-stamped: got %q, want %q", got.SchemaHash, newHash)
	}
	// Watermarks must be preserved (NOT reset).
	if w := got.Tables["session"]; w.MaxIDSeen != "ses_9" || w.MaxTimeUpdatedMs != 1779 || w.MaxTimeUpdatedID != "ses_9" {
		t.Errorf("session watermark reset on mismatch: %+v", w)
	}
	if w := got.Tables["message"]; w.MaxIDSeen != "msg_9" || w.MaxTimeUpdatedMs != 1780 || w.MaxTimeUpdatedID != "msg_9" {
		t.Errorf("message watermark reset on mismatch: %+v", w)
	}
	// A structured WARN must have been surfaced.
	if ce.count() == 0 {
		t.Error("schema-hash mismatch did not surface a WARN via onError")
	}
}

// TestProbeStatus_CountsAndLatest verifies the auto-discovery probe reports the
// session/message/part counts and the latest migration from a synthetic DB.
func TestProbeStatus_CountsAndLatest(t *testing.T) {
	t.Parallel()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db", drizzleMigrationsDDL)
	// Two sessions, three messages, four parts.
	insertSession(t, rw, "ses_1", "", 100, 100, 0)
	insertSession(t, rw, "ses_2", "", 110, 110, 0)
	insertAssistantMessage(t, rw, "msg_1", "ses_1", 101, 101, 10, 5)
	insertAssistantMessage(t, rw, "msg_2", "ses_1", 102, 102, 20, 10)
	insertAssistantMessage(t, rw, "msg_3", "ses_2", 111, 111, 30, 15)
	insertPart(t, rw, "prt_1", "msg_1", "ses_1", 103, 103, textBody("a"))
	insertPart(t, rw, "prt_2", "msg_1", "ses_1", 104, 104, textBody("b"))
	insertPart(t, rw, "prt_3", "msg_2", "ses_1", 105, 105, textBody("c"))
	insertPart(t, rw, "prt_4", "msg_3", "ses_2", 112, 112, textBody("d"))
	insertMigration(t, rw, "20260127222353_a")
	insertMigration(t, rw, "20260510033149_latest")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	sessions, messages, parts, latest, err := ProbeStatus(ctxBG(), path)
	if err != nil {
		t.Fatalf("ProbeStatus: %v", err)
	}
	if sessions != 2 || messages != 3 || parts != 4 {
		t.Fatalf("ProbeStatus counts = (%d,%d,%d), want (2,3,4)", sessions, messages, parts)
	}
	if latest != "20260510033149_latest" {
		t.Fatalf("ProbeStatus latest = %q, want 20260510033149_latest", latest)
	}
}

// TestProbeStatus_MissingMigrationsTableDegrades verifies a DB without
// __drizzle_migrations still returns counts (no migration), with no hard error —
// a foreign SQLite file degrades gracefully.
func TestProbeStatus_MissingMigrationsTableDegrades(t *testing.T) {
	t.Parallel()
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db")
	insertSession(t, rw, "ses_1", "", 100, 100, 0)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	sessions, messages, parts, latest, err := ProbeStatus(ctxBG(), path)
	if err != nil {
		t.Fatalf("ProbeStatus(no migrations) err = %v, want nil (degrade)", err)
	}
	if sessions != 1 || messages != 0 || parts != 0 {
		t.Fatalf("ProbeStatus counts = (%d,%d,%d), want (1,0,0)", sessions, messages, parts)
	}
	if latest != "" {
		t.Fatalf("ProbeStatus latest = %q, want \"\" (no migrations table)", latest)
	}
}

// TestProbeStatus_OpenErrorIsHard verifies a non-existent database file is a hard
// probe error (mode=ro refuses to create it), so discovery can log it.
func TestProbeStatus_OpenErrorIsHard(t *testing.T) {
	t.Parallel()
	_, _, _, _, err := ProbeStatus(ctxBG(), t.TempDir()+"/does-not-exist.db")
	if err == nil {
		t.Fatal("ProbeStatus(missing file) = nil error, want hard open error")
	}
}

// TestRecordSchemaHash_ReadErrorKeepsPriorCursor drives recordSchemaHash's
// non-sentinel read-error branch: a __drizzle_migrations table that EXISTS but
// lacks the `id` column makes the `ORDER BY id` query fail (a genuine error, not
// the missing-table sentinel). recordSchemaHash must surface a WARN via onError
// and keep the prior cursor unchanged (the backfill/poll still proceeds).
func TestRecordSchemaHash_ReadErrorKeepsPriorCursor(t *testing.T) {
	t.Parallel()
	// A migrations table WITHOUT an id column: present (so not the sentinel) but
	// `ORDER BY id` errors.
	noIDDDL := `CREATE TABLE __drizzle_migrations (hash TEXT NOT NULL, name TEXT)`
	path, rw := newEmptyDB(t, t.TempDir(), "opencode.db", noIDDDL)
	if _, err := rw.Exec(`INSERT INTO __drizzle_migrations (hash, name) VALUES (?,?)`, "h0", "m1"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}
	db := openRO(t, path)

	// readSchemaHash must surface the error (not the sentinel).
	if _, err := readSchemaHash(ctxBG(), db); err == nil {
		t.Fatal("readSchemaHash over a no-id migrations table = nil error, want query error")
	}

	prior := newCursor().withSchemaHash("priorhash").
		withTable("session", TableWatermark{MaxIDSeen: "ses_5", MaxTimeUpdatedMs: 99, MaxTimeUpdatedID: "ses_5"})
	var ce collectErrs
	got := recordSchemaHash(ctxBG(), db, prior, ce.onError)
	if got.SchemaHash != "priorhash" {
		t.Errorf("recordSchemaHash on read error changed the hash to %q, want prior 'priorhash'", got.SchemaHash)
	}
	if w := got.Tables["session"]; w.MaxIDSeen != "ses_5" || w.MaxTimeUpdatedMs != 99 || w.MaxTimeUpdatedID != "ses_5" {
		t.Errorf("recordSchemaHash on read error mutated watermarks: %+v", w)
	}
	if ce.count() == 0 {
		t.Error("recordSchemaHash read error did not surface a WARN via onError")
	}
}
