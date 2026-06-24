package opencode

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// seedOldSchemaDB builds a synthetic opencode database mimicking a
// pre-20260510033149_session_usage schema: the session table LACKS the
// cost/tokens_* columns (and the later path/agent/model/time_archived
// columns). It keeps the required id/time_created/time_updated columns so the
// table is still readable. This is the AC#5 schema-drift fixture, built
// throwaway in dir, never copied from the operator's database.
func seedOldSchemaDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "opencode-old.db")
	rwDSN := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)"
	rw, err := sql.Open(driverName, rwDSN)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()
	stmts := []string{
		// Old session: no cost/tokens_*, no path/agent/model/time_archived.
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE part (
			id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`CREATE TABLE session_message (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			t.Fatalf("seed old schema: %v\nstmt: %s", err, s)
		}
	}
	return path
}

// TestIntrospectAll_CurrentSchema asserts a current-schema database yields no
// missing columns on any table and a SELECT list covering every wanted column.
func TestIntrospectAll_CurrentSchema(t *testing.T) {
	t.Parallel()
	path := seedSyntheticDB(t, t.TempDir())
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	set, err := introspectAll(context.Background(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}
	for _, table := range trackedTables {
		s, ok := set[table]
		if !ok {
			t.Fatalf("table %q absent from schemaSet", table)
		}
		if len(s.Missing) != 0 {
			t.Errorf("table %q reports missing columns on current schema: %v", table, s.Missing)
		}
		if len(s.Present) != len(wantedColumns[table]) {
			t.Errorf("table %q present=%v, want all %v", table, s.Present, wantedColumns[table])
		}
	}
}

// TestIntrospectAll_OldSchema is the AC#5 dynamic-SELECT proof. Against the
// pre-session_usage schema the session table is missing cost/tokens_* (and
// later optional columns); introspectAll must succeed (required columns
// present), the session tableSchema must list those as Missing, and the built
// SELECT must NOT name any missing column.
func TestIntrospectAll_OldSchema(t *testing.T) {
	t.Parallel()
	path := seedOldSchemaDB(t, t.TempDir())
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	set, err := introspectAll(context.Background(), db)
	if err != nil {
		t.Fatalf("introspectAll on old schema: %v", err)
	}
	sess := set["session"]

	// The session_usage columns must be detected as missing.
	wantMissing := []string{
		"agent", "cost", "model", "time_archived", "tokens_cache_read",
		"tokens_cache_write", "tokens_input", "tokens_output", "tokens_reasoning",
	}
	missingSet := map[string]bool{}
	for _, m := range sess.Missing {
		missingSet[m] = true
	}
	for _, w := range wantMissing {
		if !missingSet[w] {
			t.Errorf("expected missing column %q not reported (missing=%v)", w, sess.Missing)
		}
	}

	// The dynamic SELECT must omit every missing column and must never use *.
	sel := sess.buildSelect()
	if strings.Contains(sel, "*") {
		t.Errorf("SELECT must name columns explicitly, never *: %q", sel)
	}
	for _, m := range sess.Missing {
		if strings.Contains(sel, quoteIdent(m)) {
			t.Errorf("SELECT references missing column %q: %q", m, sel)
		}
	}
	// Required columns must be present in the SELECT.
	for _, r := range requiredColumns["session"] {
		if !strings.Contains(sel, quoteIdent(r)) {
			t.Errorf("SELECT omits required column %q: %q", r, sel)
		}
	}
	// Sanity: the SELECT pages and orders along the watermark key.
	if !strings.Contains(sel, "ORDER BY time_updated, id LIMIT 1000") {
		t.Errorf("SELECT missing watermark ordering/paging: %q", sel)
	}
}

// TestIntrospectAll_MissingRequiredFails asserts that a table missing a
// required column (here: message without its data body) is rejected, because
// such a schema cannot be read safely and must surface a fatal error rather
// than emit empty rows.
func TestIntrospectAll_MissingRequiredFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.db")
	rwDSN := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)"
	rw, err := sql.Open(driverName, rwDSN)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()
	// session/part/session_message fine; message lacks the required data column.
	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, slug TEXT NOT NULL,
			directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			t.Fatalf("seed broken: %v", err)
		}
	}
	_ = rw.Close()

	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := introspectAll(context.Background(), db); err == nil {
		t.Fatal("introspectAll on table missing a required column: want error")
	}
}

// TestIntrospectTable_UnknownTableRejected asserts the helper refuses a table
// name outside the wantedColumns set (programmer error guard).
func TestIntrospectTable_UnknownTableRejected(t *testing.T) {
	t.Parallel()
	path := seedSyntheticDB(t, t.TempDir())
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := introspectTable(context.Background(), db, "not_a_table"); err == nil {
		t.Fatal("introspectTable(unknown): want error")
	}
}

// TestTableSchema_BuildSelectEmpty covers the defensive empty-columns path.
func TestTableSchema_BuildSelectEmpty(t *testing.T) {
	t.Parallel()
	s := tableSchema{Table: "session"}
	if got := s.buildSelect(); !strings.Contains(got, "WHERE 0") {
		t.Errorf("empty-column SELECT = %q, want a no-row query", got)
	}
}

// TestQuoteIdent covers the identifier quoting and embedded-quote escaping.
func TestQuoteIdent(t *testing.T) {
	t.Parallel()
	if got := quoteIdent("time_updated"); got != `"time_updated"` {
		t.Errorf("quoteIdent = %q", got)
	}
	if got := quoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("quoteIdent escape = %q", got)
	}
}

// TestBuildSelect_ScansIntoRowStructs proves the dynamic SELECT and the typed
// row structs fit together end-to-end: the message SELECT built from the live
// schema is executed against the seeded synthetic DB with a zero watermark
// (time_updated > -1 selects everything), scanned into a messageRow, and its
// data column decoded via decodeMessageData. This pins the column order the
// SELECT emits against the struct the later delta-query layer scans into, so a
// future column reordering cannot silently misalign the scan.
func TestBuildSelect_ScansIntoRowStructs(t *testing.T) {
	t.Parallel()
	path := seedSyntheticDB(t, t.TempDir())
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	set, err := introspectAll(context.Background(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}

	// message has a fixed 5-column shape on every schema; scan it whole.
	msgSel := set["message"].buildSelect()
	var m messageRow
	row := db.QueryRowContext(context.Background(), msgSel, int64(-1), int64(-1), "")
	if err := row.Scan(&m.ID, &m.SessionID, &m.TimeCreatedMs, &m.TimeUpdatedMs, &m.Data); err != nil {
		t.Fatalf("scan messageRow via buildSelect: %v", err)
	}
	if m.ID != "msg_aaa" || m.SessionID != "ses_aaa" {
		t.Errorf("scanned message wrong: %+v", m)
	}
	md, err := decodeMessageData(m.Data)
	if err != nil {
		t.Fatalf("decode scanned message data: %v", err)
	}
	if md.role() != roleAssistant {
		t.Errorf("scanned message role = %v, want assistant", md.role())
	}

	// session present-column SELECT scans into a sessionRow's matching prefix
	// (id, project_id, slug, directory, title, version, time_created,
	// time_updated on the current synthetic schema, which omits parent_id/agent/
	// model/... only when NULL — here all wanted columns exist). Scan just the
	// always-present identity columns to prove the row struct + SELECT align.
	sessSel := set["session"].buildSelect()
	if sessSel == "" {
		t.Fatal("empty session SELECT")
	}
	// Build a column->index map from the SELECT's Present order to read id only.
	present := set["session"].Present
	dest := make([]any, len(present))
	holders := make([]sql.NullString, len(present))
	for i := range present {
		dest[i] = &holders[i]
	}
	r2 := db.QueryRowContext(context.Background(), sessSel, int64(-1), int64(-1), "")
	if err := r2.Scan(dest...); err != nil {
		t.Fatalf("scan session via buildSelect: %v", err)
	}
	var s sessionRow
	for i, c := range present {
		if c == "id" {
			s.ID = holders[i].String
		}
	}
	if s.ID != "ses_aaa" {
		t.Errorf("scanned session id = %q, want ses_aaa", s.ID)
	}

	// part and session_message share a fixed-shape SELECT; scan each whole row
	// into its typed struct to prove the SELECT column order matches the
	// container the delta-query layer (Chunk C) will scan into, and decode the
	// part body to confirm the data column round-trips.
	partSel := set["part"].buildSelect()
	var p partRow
	pr := db.QueryRowContext(context.Background(), partSel, int64(-1), int64(-1), "")
	if err := pr.Scan(&p.ID, &p.MessageID, &p.SessionID, &p.TimeCreatedMs, &p.TimeUpdatedMs, &p.Data); err != nil {
		t.Fatalf("scan partRow via buildSelect: %v", err)
	}
	if p.ID != "prt_aaa" || p.MessageID != "msg_aaa" {
		t.Errorf("scanned part wrong: %+v", p)
	}
	pd, err := decodePartData(p.Data)
	if err != nil {
		t.Fatalf("decode scanned part data: %v", err)
	}
	if pd.kind() != partText {
		t.Errorf("scanned part kind = %v, want text", pd.kind())
	}

	smSel := set["session_message"].buildSelect()
	var sm sessionMessageRow
	smr := db.QueryRowContext(context.Background(), smSel, int64(-1), int64(-1), "")
	if err := smr.Scan(&sm.ID, &sm.SessionID, &sm.Type, &sm.Seq, &sm.TimeCreatedMs, &sm.TimeUpdatedMs, &sm.Data); err != nil {
		t.Fatalf("scan sessionMessageRow via buildSelect: %v", err)
	}
	if sm.ID != "evt_aaa" || sm.Type != "model-switched" {
		t.Errorf("scanned session_message wrong: %+v", sm)
	}
}
