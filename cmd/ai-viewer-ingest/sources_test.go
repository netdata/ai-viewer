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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/adapters/opencode"
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

// TestAutoDiscover_OpencodeDirectoryNotRegistered pins SOW-0005 round-3 P3-2: a
// DIRECTORY named opencode.db at the default discovery path must NOT register as
// an opencode source — os.Stat succeeds on a directory, so the probe additionally
// requires info.Mode().IsRegular(). The companion positive case (a regular DB
// file IS discovered) is TestAutoDiscover_OpencodeProbe above.
func TestAutoDiscover_OpencodeDirectoryNotRegistered(t *testing.T) {
	// Not parallel: mutates process-wide env.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	// Create a DIRECTORY exactly where the DB file would live.
	dirAsDB := filepath.Join(tmp, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(dirAsDB, 0o755); err != nil {
		t.Fatalf("mkdir dir-as-db: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	got, err := resolveSources(nil, logger)
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	for _, s := range got {
		if s.format == "opencode" {
			t.Fatalf("opencode registered for a DIRECTORY named opencode.db: %+v", got)
		}
	}
	if !bytes.Contains(buf.Bytes(), []byte("not a regular file")) {
		t.Errorf("expected a WARN that the opencode path is not a regular file; got:\n%s", buf.String())
	}
}

// TestOpencodeProbeRespectsCancelledContext pins SOW-0005 round-4 P3-1: the startup
// ProbeStatus is now passed a bounded/cancellable context (autoDiscoverSources uses
// context.WithTimeout(opencodeProbeTimeout)) instead of context.Background(), so a
// cancelled context aborts the probe promptly with an error rather than running the
// COUNT(*) queries to completion. A normal context still returns the counts. The
// probe is best-effort: discovery surfaces the error and still registers the source
// (covered by TestAutoDiscover_OpencodeProbeErrorStillRegisters), but the
// cancellation must be HONORED rather than ignored.
func TestOpencodeProbeRespectsCancelledContext(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	dbPath := plantOpencodeDB(t, tmp, 2, 3, 4, "20260510033149_init")

	// An already-cancelled context: ProbeStatus must return an error (it does not
	// silently run to completion ignoring cancellation).
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, _, err := opencode.ProbeStatus(cancelled, dbPath); err == nil {
		t.Error("ProbeStatus with a cancelled context returned nil error; the probe must honor cancellation (round-4 P3-1)")
	}

	// A normal (bounded) context still succeeds and returns the planted counts —
	// proving the timeout/cancellable wiring did not break the happy path.
	ctx, cancel2 := context.WithTimeout(context.Background(), opencodeProbeTimeout)
	defer cancel2()
	sessions, messages, parts, latest, err := opencode.ProbeStatus(ctx, dbPath)
	if err != nil {
		t.Fatalf("ProbeStatus(valid ctx): %v", err)
	}
	if sessions != 2 || messages != 3 || parts != 4 {
		t.Errorf("ProbeStatus counts = (%d,%d,%d), want (2,3,4)", sessions, messages, parts)
	}
	if latest != "20260510033149_init" {
		t.Errorf("ProbeStatus latest migration = %q, want the planted one", latest)
	}
}

// TestOpencodeMetaJSON_RoundTrips pins the opencode meta-blob contract
// (SOW-0024): the helper marshals the four opencode keys in a stable shape
// that decodes back to those keys. The presenter renders this blob verbatim
// under /api/health and /api/sources — any field name drift would break the
// UI's read-only consumer.
func TestOpencodeMetaJSON_RoundTrips(t *testing.T) {
	t.Parallel()
	const (
		sessions int64 = 42
		messages int64 = 1200
		parts    int64 = 3400
		latest         = "20260510033149_session_usage"
	)
	blob := opencodeMetaJSON(sessions, messages, parts, latest)
	if blob == "" {
		t.Fatal("opencodeMetaJSON returned empty string for a happy-path probe result")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(blob), &got); err != nil {
		t.Fatalf("unmarshal opencodeMetaJSON result: %v (blob=%q)", err, blob)
	}
	for _, c := range []struct {
		key string
		// want is encoded as a string for the assertion so the JSON-decoded
		// number type (float64) does not have to match the int64 input
		// exactly — round-trip equality over the wire shape is the contract.
		want string
	}{
		{"session_count", "42"},
		{"message_count", "1200"},
		{"part_count", "3400"},
		{"latest_migration", latest},
	} {
		v, ok := got[c.key]
		if !ok {
			t.Errorf("opencodeMetaJSON result missing key %q (blob=%q)", c.key, blob)
			continue
		}
		// JSON numbers decode to float64; JSON strings decode to string. Compare
		// the canonical string form so int64 and float64 agree across the wire.
		gotStr := fmt.Sprintf("%v", v)
		if gotStr != c.want {
			t.Errorf("opencodeMetaJSON[%q] = %v, want %v (blob=%q)", c.key, v, c.want, blob)
		}
	}
}

// TestAutoDiscover_OpencodeMetaBlob pins the opencode discovery → metaJSON
// wiring (SOW-0024): a successful ProbeStatus result is marshalled into
// configuredSource.metaJSON via opencodeMetaJSON, ready for main.go to
// register via ingest.WithSourceMeta. The test drives autoDiscoverSources
// end-to-end against a planted opencode DB so the round-trip — probe
// (counts + latest) → marshalled blob → unmarshalled keys — is the actual
// production path, not the helper in isolation.
func TestAutoDiscover_OpencodeMetaBlob(t *testing.T) {
	// Not parallel: mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	plantOpencodeDB(t, tmp, 7, 11, 13, "20260510033149_session_usage")

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
	if oc.metaJSON == "" {
		t.Fatal("opencode metaJSON is empty after a successful probe; the discovery path must marshal the ProbeStatus result")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(oc.metaJSON), &parsed); err != nil {
		t.Fatalf("unmarshal opencode metaJSON: %v (blob=%q)", err, oc.metaJSON)
	}
	for _, c := range []struct {
		key  string
		want string
	}{
		{"session_count", "7"},
		{"message_count", "11"},
		{"part_count", "13"},
		{"latest_migration", "20260510033149_session_usage"},
	} {
		v, ok := parsed[c.key]
		if !ok {
			t.Errorf("opencode metaJSON missing key %q (blob=%q)", c.key, oc.metaJSON)
			continue
		}
		if got := fmt.Sprintf("%v", v); got != c.want {
			t.Errorf("opencode metaJSON[%q] = %v, want %v (blob=%q)", c.key, v, c.want, oc.metaJSON)
		}
	}
}

// TestAutoDiscover_OpencodeMetaEmptyOnProbeError pins the best-effort
// discovery contract (SOW-0024): when the opencode probe errors, the source
// is still registered but metaJSON is left empty so the worker binds NULL
// (the omit-when-NULL contract). The operator-facing discovery log still
// carries the probe_error attr (covered by
// TestAutoDiscover_OpencodeProbeErrorStillRegisters).
func TestAutoDiscover_OpencodeMetaEmptyOnProbeError(t *testing.T) {
	// Not parallel: mutates process-wide HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	clearOtherAdapterEnv(t)
	dbPath := filepath.Join(tmp, ".local", "share", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A non-SQLite regular file at the probe path: os.Stat succeeds so the
	// source is discovered, but ProbeStatus errors.
	if err := os.WriteFile(dbPath, []byte("not a database"), 0o644); err != nil {
		t.Fatalf("write bogus db: %v", err)
	}

	got, err := resolveSources(nil, silentLogger())
	if err != nil {
		t.Fatalf("resolveSources: %v", err)
	}
	var oc *configuredSource
	for i := range got {
		if got[i].format == "opencode" && got[i].location == dbPath {
			oc = &got[i]
		}
	}
	if oc == nil {
		t.Fatalf("opencode source not registered despite a probe error; got %+v", got)
	}
	if oc.metaJSON != "" {
		t.Errorf("opencode metaJSON = %q on a probe error, want \"\" (omit-when-NULL contract)", oc.metaJSON)
	}
}
