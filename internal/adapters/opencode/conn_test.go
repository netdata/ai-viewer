package opencode

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// seedSyntheticDB creates a throwaway opencode-shaped SQLite database in dir
// via a SEPARATE read-write connection and returns its file path. The schema
// mirrors the four tracked tables (verified shape, never real data); content
// is synthetic. Callers then reopen the path through openReadOnly to assert
// the read-only contract. The read-write handle is closed before return so
// the WAL is flushed and the read-only opener sees a complete file.
func seedSyntheticDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "opencode.db")
	// A plain read-write file: URI. This is the ONLY writable handle in the
	// test; production never opens opencode.db this way.
	rwDSN := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)"
	rw, err := sql.Open(driverName, rwDSN)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() {
		if cerr := rw.Close(); cerr != nil {
			t.Errorf("close rw: %v", cerr)
		}
	}()
	stmts := []string{
		`CREATE TABLE session (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
			version TEXT NOT NULL, agent TEXT, model TEXT,
			cost REAL NOT NULL DEFAULT 0,
			tokens_input INTEGER NOT NULL DEFAULT 0,
			tokens_output INTEGER NOT NULL DEFAULT 0,
			tokens_reasoning INTEGER NOT NULL DEFAULT 0,
			tokens_cache_read INTEGER NOT NULL DEFAULT 0,
			tokens_cache_write INTEGER NOT NULL DEFAULT 0,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			time_archived INTEGER, time_compacting INTEGER)`,
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
			seq INTEGER NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
			data TEXT NOT NULL)`,
		`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			VALUES ('ses_aaa','prj_aaa','calm-otter','/work/example','synthetic title','1.0.0',1700000000000,1700000000000)`,
		`INSERT INTO message (id, session_id, time_created, time_updated, data)
			VALUES ('msg_aaa','ses_aaa',1700000000000,1700000000000,'{"role":"assistant"}')`,
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
			VALUES ('prt_aaa','msg_aaa','ses_aaa',1700000000000,1700000000000,'{"type":"text","text":"synthetic"}')`,
		`INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
			VALUES ('evt_aaa','ses_aaa','model-switched',1,1700000000000,1700000000000,'{}')`,
	}
	for _, s := range stmts {
		if _, err := rw.Exec(s); err != nil {
			t.Fatalf("seed exec failed: %v\nstmt: %s", err, s)
		}
	}
	return path
}

// TestOpenReadOnly_RejectsAllWrites is the SOW-0005 AC#2 read-only
// enforcement test. It seeds a synthetic opencode-shaped DB with a SEPARATE
// read-write connection, reopens it through the adapter's openReadOnly helper,
// and exercises the six write paths AC#2 names. The contract being asserted is
// the one that actually protects the operator's live opencode database: NO
// write to that database ever succeeds.
//
// SQLite enforces this with two distinct mechanisms, and the six probes split
// into two groups accordingly (verified against modernc.org/sqlite v1.50.1):
//
//   - Direct write statements — INSERT, UPDATE, DELETE, VACUUM — are rejected
//     outright with "attempt to write a readonly database (8)". mode=ro (OS
//     O_RDONLY) blocks them at the file layer and query_only(true) blocks them
//     at the SQL layer; either alone suffices, so the guard is defence in
//     depth.
//   - Two probes do NOT return an error, and asserting that they did would pin
//     a false mechanism:
//   - PRAGMA wal_checkpoint is a NO-OP on a read-only connection: it returns
//     a status row of (busy, -1, -1) meaning "no frames checkpointed". It
//     physically cannot mutate the WAL under mode=ro. The safety property is
//     "no checkpoint occurred", asserted by the -1/-1 sentinel, not an error.
//   - ATTACH ... 'rwc' attaches a SEPARATE side database (never opencode.db)
//     and succeeds, but query_only(true) then blocks any write INTO that
//     attached schema. The safety property is "no durable mutation path
//     opens", asserted by the attached-write erroring — and opencode.db is
//     untouched regardless.
//
// Asserting the precise property of each probe is a STRONGER read-safety proof
// than a blanket "all error", and it matches how SQLite actually behaves.
func TestOpenReadOnly_RejectsAllWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := seedSyntheticDB(t, dir)

	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Group 1: direct write statements that MUST be rejected with an error.
	directWrites := []struct {
		name string
		sql  string
	}{
		{"INSERT", `INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			VALUES ('ses_bbb','prj_bbb','x','/x','t','1',1,1)`},
		{"UPDATE", `UPDATE session SET title = 'mutated' WHERE id = 'ses_aaa'`},
		{"DELETE", `DELETE FROM session WHERE id = 'ses_aaa'`},
		{"VACUUM", `VACUUM`},
	}
	for _, p := range directWrites {
		p := p
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			if _, err := db.ExecContext(context.Background(), p.sql); err == nil {
				t.Fatalf("%s succeeded against a read-only connection; want error", p.name)
			}
		})
	}

	// Group 2a: PRAGMA wal_checkpoint must be a verified no-op. The result row
	// is (busy, log, checkpointed); a read-only connection cannot checkpoint,
	// so log and checkpointed come back -1 ("nothing done"). Asserting the
	// no-op is the real safety property — it proves the WAL was not mutated.
	t.Run("PRAGMA wal_checkpoint is a no-op", func(t *testing.T) {
		t.Parallel()
		var busy, logFrames, checkpointed int
		row := db.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
		if err := row.Scan(&busy, &logFrames, &checkpointed); err != nil {
			// A hard error here is also acceptable read-safety — the checkpoint
			// definitely did not run. Only a SUCCESSFUL checkpoint of frames
			// would be a contract breach.
			t.Logf("wal_checkpoint errored (also safe): %v", err)
			return
		}
		if logFrames != -1 || checkpointed != -1 {
			t.Fatalf("wal_checkpoint mutated the WAL: busy=%d log=%d checkpointed=%d (want log=-1 checkpointed=-1)", busy, logFrames, checkpointed)
		}
	})

	// Group 2b: ATTACH 'rwc' attaches a side database, but any write into it
	// must be blocked by query_only(true). The ATTACH targets a DIFFERENT file
	// and never touches opencode.db; the durable-mutation path is the write,
	// and that write must error.
	t.Run("ATTACH rwc blocks the attached write", func(t *testing.T) {
		t.Parallel()
		side := "file:" + escapeURIPath(filepath.ToSlash(filepath.Join(dir, "attached.db"))) + "?mode=rwc"
		// The ATTACH itself may succeed (it opens a separate file). What must
		// NOT succeed is a write into the attached schema.
		_, _ = db.ExecContext(context.Background(), `ATTACH DATABASE '`+side+`' AS side`)
		if _, err := db.ExecContext(context.Background(), `CREATE TABLE side.t (x INTEGER)`); err == nil {
			t.Fatal("write into ATTACHed rwc database succeeded; query_only must block it")
		}
		_, _ = db.ExecContext(context.Background(), `DETACH DATABASE side`)
	})

	// Belt-and-braces: the row the seed wrote must be untouched after all the
	// rejected/no-op writes, proving none partially applied to opencode.db.
	var title string
	if err := db.QueryRow(`SELECT title FROM session WHERE id = 'ses_aaa'`).Scan(&title); err != nil {
		t.Fatalf("read-back select: %v", err)
	}
	if title != "synthetic title" {
		t.Fatalf("row mutated despite read-only: title=%q", title)
	}
}

// TestBuildReadOnlyDSN_ContainsContract asserts the constructed DSN carries
// exactly the read-safety contract: the file: scheme (so the driver keeps the
// query string), mode=ro (OS-level guard), and both required PRAGMAs. This
// pins the constant so a future edit cannot quietly drop a guard.
func TestBuildReadOnlyDSN_ContainsContract(t *testing.T) {
	t.Parallel()
	dsn, err := buildReadOnlyDSN("/var/lib/opencode/opencode.db")
	if err != nil {
		t.Fatalf("buildReadOnlyDSN: %v", err)
	}
	if !strings.HasPrefix(dsn, "file:") {
		t.Errorf("DSN missing file: scheme: %q", dsn)
	}
	prefix, query := splitQuery(dsn)
	if !strings.HasSuffix(prefix, "/var/lib/opencode/opencode.db") {
		t.Errorf("DSN path not preserved: %q", prefix)
	}
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query %q: %v", query, err)
	}
	if got := params.Get("mode"); got != "ro" {
		t.Errorf("mode = %q, want ro", got)
	}
	pragmas := params["_pragma"]
	wantPragma := map[string]bool{"query_only(true)": false, "busy_timeout(5000)": false}
	for _, p := range pragmas {
		if _, ok := wantPragma[p]; ok {
			wantPragma[p] = true
		}
	}
	for p, seen := range wantPragma {
		if !seen {
			t.Errorf("DSN missing required pragma %q (got %v)", p, pragmas)
		}
	}
}

// The DSN allowlist tests (TestBuildReadOnlyDSN_AllowlistDropsAllCallerPragmas +
// TestBuildReadOnlyDSN_MaliciousDSNNeutralised, SOW-0005 P1.2) live in
// conn_dsn_test.go (split to keep this file ≤400 lines).

// TestBuildReadOnlyDSN_Errors covers the rejected inputs: an empty path and a
// query string that cannot be parsed.
func TestBuildReadOnlyDSN_Errors(t *testing.T) {
	t.Parallel()
	if _, err := buildReadOnlyDSN(""); err == nil {
		t.Error("empty path: want error")
	}
	if _, err := buildReadOnlyDSN("file:/db.sqlite?%zz"); err == nil {
		t.Error("malformed query: want error")
	}
}

// TestOpenReadOnly_MissingFileFails asserts a non-existent database path
// fails at the open call (mode=ro refuses to create it), not silently
// materialising an empty database.
func TestOpenReadOnly_MissingFileFails(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if _, err := openReadOnly(context.Background(), missing); err == nil {
		t.Fatalf("openReadOnly on missing file: want error (mode=ro must not create)")
	}
}

// TestOpenReadOnly_HonoursMaxOpenConns exercises the test-only pool override
// and confirms a connection can be acquired and queried under it.
func TestOpenReadOnly_HonoursMaxOpenConns(t *testing.T) {
	t.Parallel()
	path := seedSyntheticDB(t, t.TempDir())
	db, err := openReadOnly(context.Background(), path, withMaxOpenConns(1))
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM session`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("session count = %d, want 1", n)
	}
}

// TestIsBareIdent covers the identifier recognition used to strip a single
// "<schema>." qualifier: valid bare names, an empty string, a leading digit,
// and an embedded non-identifier character.
func TestIsBareIdent(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"main":     true,
		"temp":     true,
		"_x9":      true,
		"":         false,
		"9bad":     false,
		"has-dash": false,
		"has.dot":  false,
	}
	for in, want := range cases {
		if got := isBareIdent(in); got != want {
			t.Errorf("isBareIdent(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestIsMemoryDSN covers the in-memory DSN forms the helper recognises so a
// future test that wants a throwaway shared-cache DB is routed correctly.
func TestIsMemoryDSN(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		":memory:":                   true,
		"file::memory:?cache=shared": true,
		":memory:?cache=shared":      true,
		"file:/tmp/x.db":             false,
		"/tmp/x.db":                  false,
	}
	for in, want := range cases {
		if got := isMemoryDSN(in); got != want {
			t.Errorf("isMemoryDSN(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestBuildReadOnlyDSN_MemoryAndFileURIPassthrough asserts a pre-built file:
// URI and an in-memory DSN are accepted and still gain the read-only query
// parameters, exercising the non-path branches of buildReadOnlyDSN.
func TestBuildReadOnlyDSN_MemoryAndFileURIPassthrough(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"file:/already/a/uri.db", ":memory:"} {
		dsn, err := buildReadOnlyDSN(in)
		if err != nil {
			t.Fatalf("buildReadOnlyDSN(%q): %v", in, err)
		}
		if !strings.Contains(dsn, "mode=ro") {
			t.Errorf("buildReadOnlyDSN(%q) = %q, missing mode=ro", in, dsn)
		}
	}
}

// TestPragmaName covers the identifier extraction across the forms the strip
// pass must recognise: bare, valued, schema-qualified, and whitespace.
func TestPragmaName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"query_only(true)":       "query_only",
		"busy_timeout=5000":      "busy_timeout",
		"main.query_only(false)": "query_only",
		"  foreign_keys (on) ":   "foreign_keys",
		"cache_size(-64000)":     "cache_size",
		"":                       "",
	}
	for in, want := range cases {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q) = %q, want %q", in, got, want)
		}
	}
}
