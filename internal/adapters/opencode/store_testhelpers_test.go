package opencode

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// This file holds the synthetic-DB builders and the query-counting driver the
// store_query / store_load / tailer tests share. Every DB is built throwaway in
// t.TempDir() via a SEPARATE read-write connection (production NEVER opens
// opencode.db read-write); the adapter under test reopens the path via the
// read-only openReadOnly helper. Content is synthetic, schema-shaped, never the
// operator's data (SOW-0005 R5; adapter-opencode.md §"Sensitive content").

// ocSchemaStmts is the CREATE-TABLE set for a CURRENT-schema synthetic opencode
// DB (the four tracked tables with every wanted column). Mirrors the live shape
// verified in adapter-opencode.md; used by the delta/tailer tests that need real
// JSON bodies the mapper can project to events.
var ocSchemaStmts = []string{
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
		time_archived INTEGER)`,
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

// rwDSNFor builds a writable file: DSN for a path (test-only; production never
// opens opencode.db this way).
func rwDSNFor(path string) string {
	return "file:" + escapeURIPath(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)"
}

// newEmptyDB creates an empty current-schema opencode DB at dir/name and returns
// its path plus an open read-write *sql.DB the caller uses to insert rows. The
// caller MUST close the rw handle before opening the path read-only so the WAL
// is flushed.
func newEmptyDB(t *testing.T, dir, name string, extra ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(dir, name)
	rw, err := sql.Open(driverName, rwDSNFor(path))
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	for _, s := range append(append([]string{}, ocSchemaStmts...), extra...) {
		if _, err := rw.Exec(s); err != nil {
			_ = rw.Close()
			t.Fatalf("create schema: %v\nstmt: %s", err, s)
		}
	}
	return path, rw
}

// insertSession inserts a session row with the given id/parent/times.
func insertSession(t *testing.T, rw *sql.DB, id, parent string, createdMs, updatedMs, archivedMs int64) {
	t.Helper()
	model, _ := json.Marshal(map[string]any{"id": "the-model", "providerID": "the-alias"})
	var arch any
	if archivedMs > 0 {
		arch = archivedMs
	}
	_, err := rw.Exec(
		`INSERT INTO session (id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, "prj_1", parent, "slug", "/work/dir", "Title", "9.9.9", "test-agent", string(model), createdMs, updatedMs, arch)
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

// insertAssistantMessage inserts an assistant message with a JSON body carrying
// tokens/cost/finish (the mapper reads it). The body is schema-shaped synthetic.
func insertAssistantMessage(t *testing.T, rw *sql.DB, id, sessionID string, createdMs, updatedMs int64, inTok, outTok int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"providerID": "the-alias",
		"modelID":    "the-model",
		"agent":      "test-agent",
		"cost":       0.01,
		"tokens":     map[string]any{"input": inTok, "output": outTok, "cache": map[string]any{"read": 0, "write": 0}},
		"time":       map[string]any{"created": createdMs, "completed": updatedMs},
		"finish":     "stop",
	})
	insertMessageRaw(t, rw, id, sessionID, createdMs, updatedMs, string(body))
}

// insertMessageRaw inserts a message row with a verbatim data body.
func insertMessageRaw(t *testing.T, rw *sql.DB, id, sessionID string, createdMs, updatedMs int64, body string) {
	t.Helper()
	_, err := rw.Exec(
		`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
		id, sessionID, createdMs, updatedMs, body)
	if err != nil {
		t.Fatalf("insert message %s: %v", id, err)
	}
}

// insertPart inserts a part row with a verbatim data body and the given times.
// sessionID may be "" for the old-schema-without-session_id fixtures (the column
// is NOT NULL on the current schema, so callers pass a value there).
func insertPart(t *testing.T, rw *sql.DB, id, messageID, sessionID string, createdMs, updatedMs int64, body string) {
	t.Helper()
	_, err := rw.Exec(
		`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
		id, messageID, sessionID, createdMs, updatedMs, body)
	if err != nil {
		t.Fatalf("insert part %s: %v", id, err)
	}
}

// stepStartBody / stepFinishBody / textBody build common part JSON bodies.
func stepStartBody() string {
	b, _ := json.Marshal(map[string]any{"type": "step-start"})
	return string(b)
}

func stepFinishBody(inCum, outCum int64, cost float64) string {
	b, _ := json.Marshal(map[string]any{
		"type": "step-finish", "reason": "stop", "cost": cost,
		"tokens": map[string]any{"input": inCum, "output": outCum, "cache": map[string]any{"read": 0, "write": 0}},
	})
	return string(b)
}

func textBody(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "text", "text": text})
	return string(b)
}

// openRWAgain reopens a built DB path read-write so a test can insert MORE rows
// after the read-only adapter loop is already running (simulating opencode's live
// writer). The handle uses WAL so the read-only reader sees committed rows. This
// is the ONLY writable handle pattern the tests use; production never opens
// opencode.db read-write.
func openRWAgain(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()
	dsn := "file:" + escapeURIPath(filepath.ToSlash(path)) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	rw, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	rw.SetMaxOpenConns(1)
	if err := rw.PingContext(context.Background()); err != nil {
		_ = rw.Close()
		return nil, err
	}
	return rw, nil
}

// openRO reopens a built DB path read-only via the adapter's helper, registering
// cleanup. It is the ONLY way the tests acquire a connection to the DB under
// test (the read-only contract).
func openRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("openReadOnly %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// introspect reopens a built DB read-only and introspects its schema, returning
// both the *sql.DB and the schemaSet (the common preamble of the store tests).
func introspect(t *testing.T, path string) (*sql.DB, schemaSet) {
	t.Helper()
	db := openRO(t, path)
	set, err := introspectAll(context.Background(), db)
	if err != nil {
		t.Fatalf("introspectAll: %v", err)
	}
	return db, set
}

// --- query-counting driver ----------------------------------------------------
//
// countingDriver wraps the registered modernc.org/sqlite driver to record the
// text of every executed query. It proves AC#6's stronger property: the literal
// MAX(time_updated) query is NOT executed across idle polls. It is registered
// once under a test-only name; tests open through that name and inspect the
// recorded SQL.

// queryLog records executed SQL strings, concurrency-safe (the tail loop's
// connection pool may issue from a background goroutine).
type queryLog struct {
	mu      sync.Mutex
	queries []string
}

func (l *queryLog) record(q string) {
	l.mu.Lock()
	l.queries = append(l.queries, q)
	l.mu.Unlock()
}

// countContaining returns how many recorded queries contain substr (case-sensitive).
func (l *queryLog) countContaining(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, q := range l.queries {
		if strings.Contains(q, substr) {
			n++
		}
	}
	return n
}

// reset clears the recorded queries (so a test counts only the SQL issued after
// a priming phase). The driver log is shared across tests via sql.Register's
// once-only registration, so a test that asserts counts MUST reset first and run
// non-parallel with other counting-driver tests would otherwise race; the
// counting tests use distinct DBs and reset immediately before their measured
// window, and the substrings they match (MAX(time_updated) / MAX(id)) are issued
// only by their own detectChange calls.
func (l *queryLog) reset() {
	l.mu.Lock()
	l.queries = nil
	l.mu.Unlock()
}

// countingDriver is a driver.Driver wrapping an inner driver.Driver, logging
// every query its connections execute into log.
type countingDriver struct {
	inner driver.Driver
	log   *queryLog
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, log: d.log}, nil
}

// countingConn wraps a driver.Conn, recording QueryContext SQL. modernc's conn
// implements QueryerContext + ExecerContext + the *Context preparers; we forward
// to those when present and record the query text.
type countingConn struct {
	driver.Conn
	log *queryLog
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.log.record(query)
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.log.record(query)
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.log.record(query)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Prepare(query)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for a driver without ConnBeginTx
}

var (
	countingDriverOnce sync.Once
	countingDriverLog  = &queryLog{}
	countingDriverReg  atomic.Bool
)

// countingDriverName is the registered name tests open through to capture SQL.
const countingDriverName = "sqlite-counting"

// registerCountingDriver registers the counting driver once (idempotent across
// tests) and returns the shared query log. It obtains the inner modernc driver
// by opening a throwaway DB through the standard "sqlite" name and reading its
// .Driver(), so it never depends on an unexported modernc type.
func registerCountingDriver(t *testing.T) *queryLog {
	t.Helper()
	countingDriverOnce.Do(func() {
		probe, err := sql.Open(driverName, "file::memory:")
		if err != nil {
			t.Fatalf("probe open for inner driver: %v", err)
		}
		inner := probe.Driver()
		_ = probe.Close()
		sql.Register(countingDriverName, &countingDriver{inner: inner, log: countingDriverLog})
		countingDriverReg.Store(true)
	})
	if !countingDriverReg.Load() {
		t.Fatal("counting driver not registered")
	}
	return countingDriverLog
}

// openCounting opens path read-only through the counting driver (same DSN the
// adapter builds), so executed SQL is recorded. The returned *sql.DB is
// cleaned up via t.Cleanup. A small pool keeps the recorded SQL deterministic.
func openCounting(t *testing.T, path string) (*sql.DB, *queryLog) {
	t.Helper()
	log := registerCountingDriver(t)
	dsn, err := buildReadOnlyDSN(path)
	if err != nil {
		t.Fatalf("buildReadOnlyDSN: %v", err)
	}
	db, err := sql.Open(countingDriverName, dsn)
	if err != nil {
		t.Fatalf("open counting: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("ping counting: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, log
}

// ctxBG is a tiny alias to keep test call sites short.
func ctxBG() context.Context { return context.Background() }

// fmtID zero-pads an integer into a 12-wide lexicographically-sortable suffix so
// synthetic ids sort in creation order like real Sonyflake ids.
func fmtID(prefix string, n int) string {
	return fmt.Sprintf("%s_%012d", prefix, n)
}
