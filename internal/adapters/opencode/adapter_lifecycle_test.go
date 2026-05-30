package opencode

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the opencode Adapter's Scan/Tail/snapshot LIFECYCLE tests,
// split out of adapter_test.go to keep each test file under the 400-line budget.
// The construction/cursor tests + the shared discardOpts/hasSession helpers live
// in adapter_test.go (same package).

// TestAdapter_ScanRecordsCursorAndCancelReturnsNil verifies Scan emits the
// expected events, records scanCursor on the instance, and that a cancelled Scan
// returns nil while still recording a best-effort cursor.
func TestAdapter_ScanRecordsCursorAndCancelReturnsNil(t *testing.T) {
	t.Parallel()
	path := seedBackfillDB(t, t.TempDir(), 2)
	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out := make(chan canonical.Event, 4096)
	if err := a.Scan(context.Background(), nil, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := drainAll(out)
	if c := countKind(got, canonical.EvSessionStarted); c != 2 {
		t.Errorf("Scan SessionStarted = %d, want 2", c)
	}
	if a.scanCursor == nil {
		t.Fatal("Scan did not record scanCursor on the instance")
	}
	if !a.scanCursor.hasProgress() {
		t.Error("recorded scanCursor has no progress after a non-empty scan")
	}

	// A cancelled Scan returns nil and still records a best-effort cursor.
	aCancel, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out2 := make(chan canonical.Event, 4096)
	if err := aCancel.Scan(ctx, nil, out2); err != nil {
		t.Fatalf("cancelled Scan = %v, want nil", err)
	}
	if aCancel.scanCursor == nil {
		t.Fatal("cancelled Scan did not record a best-effort cursor")
	}
}

// TestAdapter_ScanThenTailHandoff is the load-bearing Scan→Tail hand-off: Scan
// records the watermark on the instance, and a following Tail resumes from it
// (not from HEAD). A session inserted AFTER Scan is emitted by Tail, and the
// already-scanned sessions are NOT re-emitted before it.
func TestAdapter_ScanThenTailHandoff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	insertSession(t, rw, "ses_a", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_a", "ses_a", 110, 110, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	scanOut := make(chan canonical.Event, 4096)
	if err := a.Scan(context.Background(), nil, scanOut); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	scanEvents := drainAll(scanOut)
	if !hasSession(scanEvents, "ses_a") {
		t.Fatal("Scan did not emit ses_a")
	}
	if a.scanCursor == nil {
		t.Fatal("Scan did not record scanCursor")
	}

	// Insert a NEW session after Scan, then run Tail. Tail must resume from the
	// recorded watermark and emit only the new session.
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	defer func() { _ = rw2.Close() }()
	insertSession(t, rw2, "ses_b", "", 200, 200, 0)
	insertAssistantMessage(t, rw2, "msg_b", "ses_b", 210, 210, 7, 3)

	tailOut := make(chan canonical.Event, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = a.Tail(ctx, tailOut)
	}()
	defer func() { cancel(); wg.Wait() }()

	got, ok := waitForSession(tailOut, "ses_b", 8*time.Second)
	if !ok {
		t.Fatal("Tail did not emit the session inserted after Scan")
	}
	// The resumed Tail must NOT replay the already-scanned ses_a before ses_b.
	if hasSession(got, "ses_a") {
		t.Error("resumed Tail re-emitted the already-scanned session ses_a (cursor hand-off broken)")
	}
}

// TestAdapter_TailColdSnapshot covers the cold-Tail path (no preceding Scan):
// Tail snapshots current HEAD so it follows from now and does NOT replay a
// pre-existing session; a session inserted AFTER the loop starts is emitted.
func TestAdapter_TailColdSnapshot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db")
	// A pre-existing session the cold snapshot must NOT replay.
	insertSession(t, rw, "ses_old", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_old", "ses_old", 110, 110, 5, 2)
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tailOut := make(chan canonical.Event, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = a.Tail(ctx, tailOut) // cold: no preceding Scan → HEAD snapshot
	}()
	defer func() { cancel(); wg.Wait() }()

	// Let the snapshot + watch establish, then insert a NEW session.
	time.Sleep(200 * time.Millisecond)
	rw2, err := openRWAgain(t, path)
	if err != nil {
		t.Fatalf("reopen rw: %v", err)
	}
	defer func() { _ = rw2.Close() }()
	insertSession(t, rw2, "ses_fresh", "", 300, 300, 0)
	insertAssistantMessage(t, rw2, "msg_fresh", "ses_fresh", 310, 310, 9, 4)

	got, ok := waitForSession(tailOut, "ses_fresh", 8*time.Second)
	if !ok {
		t.Fatal("cold Tail did not emit the session inserted after it started")
	}
	if hasSession(got, "ses_old") {
		t.Error("cold Tail replayed the pre-existing session ses_old (HEAD snapshot broken)")
	}
}

// TestAdapter_SnapshotCursor pins snapshotCursor: it records the DB's current
// HEAD watermarks for every tracked table AND the real __drizzle_migrations
// schema hash.
func TestAdapter_SnapshotCursor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, rw := newEmptyDB(t, dir, "opencode.db", drizzleMigrationsDDL)
	insertSession(t, rw, "ses_1", "", 100, 100, 0)
	insertAssistantMessage(t, rw, "msg_1", "ses_1", 110, 110, 5, 2)
	insertPart(t, rw, "prt_1", "msg_1", "ses_1", 120, 120, textBody("a"))
	insertMigration(t, rw, "20260127222353_a")
	insertMigration(t, rw, "20260510033149_b")
	if err := rw.Close(); err != nil {
		t.Fatalf("close rw: %v", err)
	}

	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cur, err := a.snapshotCursor(context.Background())
	if err != nil {
		t.Fatalf("snapshotCursor: %v", err)
	}

	// Each table's watermark equals the DB maxima.
	db, _ := introspect(t, path)
	for _, table := range trackedTables {
		wantMaxID, _ := maxID(ctxBG(), db, table)
		wantMaxTU, _ := maxTimeUpdated(ctxBG(), db, table)
		w := cur.Tables[table]
		// A cold-Tail HEAD snapshot starts both the monotonic high-water and the
		// paging-position id at MAX(id) (SOW-0005 round-2 P1-A).
		if w.MaxIDSeen != wantMaxID || w.MaxTimeUpdatedID != wantMaxID || w.MaxTimeUpdatedMs != wantMaxTU {
			t.Errorf("table %q snapshot watermark = %+v, want {MaxIDSeen:%q MaxTimeUpdatedID:%q MaxTimeUpdatedMs:%d}", table, w, wantMaxID, wantMaxID, wantMaxTU)
		}
	}
	// The real migration-name hash is recorded.
	wantHash := schemaHash([]string{"20260127222353_a", "20260510033149_b"})
	if cur.SchemaHash != wantHash {
		t.Errorf("snapshot SchemaHash = %q, want real migration digest %q", cur.SchemaHash, wantHash)
	}
}

// TestAdapter_TailColdSnapshotMissingDB verifies a cold Tail over a missing DB
// surfaces the snapshot open error (Tail wraps and returns it before the loop).
func TestAdapter_TailColdSnapshotMissingDB(t *testing.T) {
	t.Parallel()
	opts, _, _ := discardOpts()
	a, err := New(t.TempDir()+"/no-such.db", opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// snapshotCursor surfaces the open error directly (avoids racing tailLoop).
	if _, err := a.snapshotCursor(context.Background()); err == nil {
		t.Error("snapshotCursor over a missing DB = nil error, want open error")
	}
}

// seedIncompatibleSchemaDB builds a DB whose session table LACKS the required
// time_updated column, so introspectAll fails fast — driving the fatal-schema
// error path in Scan and the cold-Tail snapshot.
func seedIncompatibleSchemaDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "opencode-bad.db")
	rw, err := sqlOpenRW(t, path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()
	stmts := []string{
		// session is MISSING time_updated (a required column) → unreadable.
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL,
			time_created INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			t.Fatalf("seed incompatible schema: %v\nstmt: %s", err, s)
		}
	}
	return path
}

// TestAdapter_ScanIncompatibleSchemaHardError drives Scan's fatal-error branch:
// an incompatible schema (a required column missing) makes scanLoop return a
// non-cancel error, which Scan wraps and returns (NOT nil). The best-effort
// cursor is still recorded on the instance.
func TestAdapter_ScanIncompatibleSchemaHardError(t *testing.T) {
	t.Parallel()
	path := seedIncompatibleSchemaDB(t, t.TempDir())
	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := make(chan canonical.Event, 16)
	if err := a.Scan(context.Background(), nil, out); err == nil {
		t.Fatal("Scan over an incompatible schema = nil error, want fatal schema error")
	}
	if a.scanCursor == nil {
		t.Error("Scan still records a best-effort cursor even on a hard error")
	}
}

// TestAdapter_TailColdSnapshotIncompatibleSchema drives the cold-Tail snapshot's
// introspect-error branch: snapshotCursor surfaces the incompatible-schema error,
// which Tail wraps and returns before entering the poll loop.
func TestAdapter_TailColdSnapshotIncompatibleSchema(t *testing.T) {
	t.Parallel()
	path := seedIncompatibleSchemaDB(t, t.TempDir())
	opts, _, _ := discardOpts()
	a, err := New(path, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.snapshotCursor(context.Background()); err == nil {
		t.Error("snapshotCursor over an incompatible schema = nil error, want introspect error")
	}
}

// sqlOpenRW opens a writable handle to a synthetic DB path (test-only; production
// never opens opencode.db read-write). Mirrors store_testhelpers_test.rwDSNFor.
func sqlOpenRW(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()
	return sql.Open(driverName, "file:"+escapeURIPath(filepath.ToSlash(path))+"?_pragma=busy_timeout(5000)")
}
