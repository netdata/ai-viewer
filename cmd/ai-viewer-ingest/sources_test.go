// Tests for the opencode auto-discovery probe and its observability counters
// (SOW-0005 acceptance #8). The shared discovery counters and the codex probe
// tests live in discovery_test.go (split for the 400-line budget). These pin:
//
//   - a synthetic opencode DB at the default path is auto-discovered as an
//     "opencode" source whose location is the database FILE (not a directory),
//     and the registered factory can construct it;
//   - the discovery log line carries session/message/part counts + the latest
//     migration as distinct keys;
//   - an absent DB registers no opencode source;
//   - a probe error (a file that is not a valid opencode DB) still registers the
//     source and logs a probe_error attr (counting must not block discovery).
package main

import (
	"bytes"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"

	// The opencode probe test builds a synthetic SQLite database with the
	// modernc driver (same CGO-free driver the adapter uses), then opens it via
	// the registered adapter read-only. Synthetic, schema-shaped, never the
	// operator's data (SOW-0005 R5).
	_ "modernc.org/sqlite"
)

// plantOpencodeDB builds a synthetic opencode SQLite database at the default
// discovery path under home (~/.local/share/opencode/opencode.db) with the four
// tracked tables, a populated __drizzle_migrations table, and the given number of
// sessions/messages/parts. It is built via a throwaway read-write connection
// (the adapter NEVER opens opencode.db read-write; the probe reopens it
// read-only) and the handle is closed so the WAL is flushed before the probe
// runs. Content is synthetic, never the operator's data.
func plantOpencodeDB(t *testing.T, home string, sessions, messages, parts int, latestMigration string) string {
	t.Helper()
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir opencode data dir: %v", err)
	}
	rw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()

	ddl := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
			slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL,
			agent TEXT, model TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, time_archived INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE __drizzle_migrations (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL,
			created_at NUMERIC, name TEXT, applied_at TEXT)`,
	}
	for _, stmt := range ddl {
		if _, err := rw.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v\nstmt: %s", err, stmt)
		}
	}
	for i := 0; i < sessions; i++ {
		if _, err := rw.Exec(
			`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			 VALUES (?,?,?,?,?,?,?,?)`,
			itoaWide("ses", i), "prj_1", "slug", "/work", "Title", "9.9.9", 100+i, 100+i); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	}
	for i := 0; i < messages; i++ {
		if _, err := rw.Exec(
			`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
			itoaWide("msg", i), itoaWide("ses", 0), 200+i, 200+i, `{"role":"assistant"}`); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	for i := 0; i < parts; i++ {
		if _, err := rw.Exec(
			`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
			itoaWide("prt", i), itoaWide("msg", 0), itoaWide("ses", 0), 300+i, 300+i, `{"type":"text","text":"x"}`); err != nil {
			t.Fatalf("insert part: %v", err)
		}
	}
	// Two migrations; the second (latest) is the one the probe must report.
	if _, err := rw.Exec(`INSERT INTO __drizzle_migrations (hash, name) VALUES (?,?)`, "h0", "20260127222353_first"); err != nil {
		t.Fatalf("insert migration: %v", err)
	}
	if _, err := rw.Exec(`INSERT INTO __drizzle_migrations (hash, name) VALUES (?,?)`, "h1", latestMigration); err != nil {
		t.Fatalf("insert migration: %v", err)
	}
	return dbPath
}

// itoaWide zero-pads a small index into a 12-wide lexicographically-sortable id
// suffix so synthetic ids sort in creation order like real Sonyflake ids.
func itoaWide(prefix string, n int) string {
	digits := []byte("000000000000")
	i := len(digits) - 1
	for n > 0 && i >= 0 {
		digits[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return prefix + "_" + string(digits)
}

// clearOtherAdapterEnv unsets the env overrides for the codex/claude probes so an
// opencode probe test sees only the HOME-rooted opencode DB (the other probes
// look under HOME too, but their directories will not exist). It also clears the
// opencode resolution overrides ($OPENCODE_DB, $XDG_DATA_HOME) so the probe falls
// through to the ~/.local/share default these tests plant under HOME.
func clearOtherAdapterEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("XDG_DATA_HOME", "")
}

// TestAutoDiscover_OpencodeProbe verifies acceptance #8: a synthetic opencode DB
// at the default path is auto-discovered as an "opencode" source whose location
// is the database file, and the registered factory can construct it.
func TestAutoDiscover_OpencodeProbe(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	dbPath := plantOpencodeDB(t, tmp, 2, 3, 4, "20260510033149_latest")

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var oc *configuredSource
	for i := range got {
		if got[i].format == "opencode" {
			oc = &got[i]
		}
	}
	if oc == nil {
		t.Fatalf("opencode source not auto-discovered; got %+v", got)
	}
	if oc.location != dbPath {
		t.Fatalf("opencode location = %q, want %q (the DB file)", oc.location, dbPath)
	}
	factory, ok := adapters.Get("opencode")
	if !ok {
		t.Fatal("opencode factory not registered")
	}
	if _, err := factory(oc.location, canonical.AdapterOptions{Logger: silentLogger()}); err != nil {
		t.Fatalf("opencode factory(%q): %v", oc.location, err)
	}
}

// TestAutoDiscover_OpencodeProbeLogsCountsAndMigration verifies the discovery log
// line carries the session/message/part counts and the latest migration as
// distinct keys (acceptance #8: the structured log is the operator-facing surface
// at discovery time).
func TestAutoDiscover_OpencodeProbeLogsCountsAndMigration(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	plantOpencodeDB(t, tmp, 2, 3, 4, "20260510033149_latest")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, err := resolveSources(nil, logger); err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"sessions=2", "messages=3", "parts=4", "latest_migration=20260510033149_latest"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("discovery log missing %q; got:\n%s", want, out)
		}
	}
}

// TestAutoDiscover_NoOpencodeWhenAbsent verifies a workstation without the
// opencode DB does not register an opencode source.
func TestAutoDiscover_NoOpencodeWhenAbsent(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	for _, s := range got {
		if s.format == "opencode" {
			t.Fatalf("opencode registered with no DB present: %+v", got)
		}
	}
}

// TestAutoDiscover_OpencodeProbeErrorStillRegisters verifies that when the file
// at the probe path exists but is NOT a valid opencode database (no tables),
// ProbeStatus errors yet the source is STILL registered (counting must not block
// discovery) and the log carries a probe_error attr.
func TestAutoDiscover_OpencodeProbeErrorStillRegisters(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	dbPath := filepath.Join(tmp, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A non-SQLite regular file at the probe path: os.Stat succeeds (so it is
	// discovered) but ProbeStatus fails (the count queries hit no tables).
	if err := os.WriteFile(dbPath, []byte("not a database"), 0o644); err != nil {
		t.Fatalf("write bogus db: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	got, err := resolveSources(nil, logger)
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var registered bool
	for _, s := range got {
		if s.format == "opencode" && s.location == dbPath {
			registered = true
		}
	}
	if !registered {
		t.Fatalf("opencode source NOT registered despite a probe error; got %+v", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("probe_error=")) {
		t.Errorf("discovery log missing probe_error attr; got:\n%s", buf.String())
	}
}
