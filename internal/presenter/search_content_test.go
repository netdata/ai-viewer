// search_content_test.go (SOW-0091) — /api/search exercises fts_content
// (prompt/response text) in addition to the existing fts_ops (op
// metadata) and fts_logs (log messages). The three sources are
// independent queries that share the same LIMIT + cursor.

package presenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSearch_ContentMatchesPrompt verifies a query that hits
// fts_content returns matches under the `content` array with
// snippet() text + the linkage columns (op_id, session_id, turn_id).
func TestSearch_ContentMatchesPrompt(t *testing.T) {
	t.Parallel()

	h := newSearchContentTestHarness(t)
	defer h.Close()

	// Seed fts_content with a row whose text contains "permissions".
	h.insertContent("op-perm", "s-1", "t-1", "<permissions instructions> Filesystem sandboxing")
	h.insertContent("op-other", "s-1", "t-2", "this turn is about caching")

	rec := h.search(t, "permissions")
	body := decodeSearchBody(t, rec)

	if len(body.Content) != 1 {
		t.Fatalf("content matches: want 1, got %d", len(body.Content))
	}
	if body.Content[0].OpID != "op-perm" {
		t.Errorf("match op_id: want op-perm, got %s", body.Content[0].OpID)
	}
	if !strings.Contains(body.Content[0].Snippet, "permissions") {
		t.Errorf("snippet should contain match term, got %q", body.Content[0].Snippet)
	}
	if body.Content[0].SessionID != "s-1" {
		t.Errorf("match session_id: want s-1, got %s", body.Content[0].SessionID)
	}
	if body.Content[0].TurnID != "t-1" {
		t.Errorf("match turn_id: want t-1, got %s", body.Content[0].TurnID)
	}
}

// TestSearch_ContentAndOpsAreIndependent verifies that fts_content
// matches surface even when fts_ops has zero matches for the same query.
// (fts_ops only indexes op metadata, not prompt text.)
func TestSearch_ContentAndOpsAreIndependent(t *testing.T) {
	t.Parallel()

	h := newSearchContentTestHarness(t)
	defer h.Close()

	h.insertOps("op-1", "read_file", "")
	h.insertContent("op-1", "s-1", "t-1", "rate limiting configuration")

	// Query "rate limiting" — only matches fts_content.
	rec := h.search(t, "rate limiting")
	body := decodeSearchBody(t, rec)

	if len(body.Ops) != 0 {
		t.Errorf("ops: want 0 (no fts_ops match), got %d", len(body.Ops))
	}
	if len(body.Content) != 1 {
		t.Errorf("content: want 1 (match in prompt text), got %d", len(body.Content))
	}
}

// TestSearch_ContentEmptyWhenIndexEmpty verifies the response carries
// an empty (not null) `content` array when nothing matches.
func TestSearch_ContentEmptyWhenIndexEmpty(t *testing.T) {
	t.Parallel()

	h := newSearchContentTestHarness(t)
	defer h.Close()

	rec := h.search(t, "anything")
	body := decodeSearchBody(t, rec)

	if body.Content == nil {
		t.Fatal("content array should be empty (not nil)")
	}
	if len(body.Content) != 0 {
		t.Errorf("content: want 0, got %d", len(body.Content))
	}
}

// searchContentTestHarness wires a minimal in-memory store with the
// tables fts_content and the join chain (ops, sessions) needs. Mirrors
// the schema for the search path; ignores unrelated migrations.
type searchContentTestHarness struct {
	store *sql.DB
	p     *Presenter
}

func (h *searchContentTestHarness) Close() { _ = h.store.Close() }

func newSearchContentTestHarness(t *testing.T) *searchContentTestHarness {
	t.Helper()
	db := openInMemoryDBForSearchContent(t)

	// Pin test "now" to 2026-06-21 (matches the fixtures used elsewhere) so
	// parseSessionFilter's default time window [from=now-30d, to=now]
	// doesn't exclude fixtures with start_ts = 1000.
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Seed the minimum join chain so the JOIN in searchContent works.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts)
		 VALUES ('s-1', 'src-1', 'native-1', 's-1', 'root', 'completed', 1000, 1000)`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at, fts5_index_logs) VALUES ('src-1', 'jsonl', '/tmp/x', 1, 0)`); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	p := &Presenter{db: db, logger: silentTestLogger(), nowFn: func() time.Time { return now }}
	return &searchContentTestHarness{store: db, p: p}
}

// openInMemoryDBForSearchContent opens a fresh file-backed SQLite (so
// the modernc driver is happy) with the minimal schema subset the
// search path needs: ops, sessions, sources, fts_content. No FTS5
// migration chain — just the CREATE VIRTUAL TABLE fts_content.
func openInMemoryDBForSearchContent(t *testing.T) *sql.DB {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "search-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range searchContentTestSchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply schema %q: %v", stmt, err)
		}
	}
	return db
}

var searchContentTestSchema = []string{
	`CREATE TABLE sources (
		id TEXT PRIMARY KEY NOT NULL,
		format TEXT NOT NULL,
		location TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		fts5_index_logs INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE sessions (
		id TEXT PRIMARY KEY NOT NULL,
		source_id TEXT NOT NULL,
		native_id TEXT NOT NULL,
		root_session_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		start_ts INTEGER NOT NULL,
		last_activity_ts INTEGER NOT NULL
	)`,
	`CREATE TABLE ops (
		id TEXT PRIMARY KEY NOT NULL,
		turn_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL,
		name TEXT NOT NULL,
		model TEXT,
		start_ts INTEGER NOT NULL,
		status TEXT NOT NULL
	)`,
	`CREATE VIRTUAL TABLE fts_content USING fts5(
		text,
		op_id UNINDEXED,
		session_id UNINDEXED,
		turn_id UNINDEXED
	)`,
	// fts_ops is needed because searchOps queries it; fts_logs is queried
	// when the per-source fts5_index_logs flag is true. We mark the source
	// as NOT indexing logs so the logs path is skipped.
	`CREATE VIRTUAL TABLE fts_ops USING fts5(
		name,
		model,
		provider,
		tool_namespace,
		error_text,
		op_id UNINDEXED,
		session_id UNINDEXED
	)`,
}

// silentTestLogger returns an slog.Logger that discards everything.
func silentTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (h *searchContentTestHarness) insertContent(opID, sessionID, turnID, text string) {
	if _, err := h.store.ExecContext(context.Background(),
		`INSERT INTO fts_content (text, op_id, session_id, turn_id) VALUES (?, ?, ?, ?)`,
		text, opID, sessionID, turnID); err != nil {
		panic(err)
	}
	// The search JOIN requires the op row to exist; seed a minimal one so
	// the WHERE chain resolves. We don't care about op metadata here —
	// only that the foreign-key-like relationship is intact.
	if _, err := h.store.ExecContext(context.Background(),
		`INSERT OR IGNORE INTO ops (id, turn_id, session_id, seq, kind, name, start_ts, status)
		 VALUES (?, ?, ?, 1, 'llm', 'message', 1000, 'completed')`,
		opID, turnID, sessionID); err != nil {
		panic(err)
	}
}

func (h *searchContentTestHarness) insertOps(opID, name, model string) {
	if _, err := h.store.ExecContext(context.Background(),
		`INSERT INTO ops (id, turn_id, session_id, seq, kind, name, model, start_ts, status)
		 VALUES (?, 't-1', 's-1', 1, 'tool', ?, ?, 1000, 'completed')`,
		opID, name, model); err != nil {
		panic(err)
	}
}

func (h *searchContentTestHarness) search(t *testing.T, q string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q="+url.QueryEscape(q), nil)
	h.p.handleSearch(rec, req)
	return rec
}

// searchContentBody mirrors the searchResponse envelope for tests.
type searchContentBody struct {
	Ops     []struct{} `json:"ops"`
	Logs    []struct{} `json:"logs"`
	Content []struct {
		OpID      string  `json:"op_id"`
		SessionID string  `json:"session_id"`
		TurnID    string  `json:"turn_id"`
		Snippet   string  `json:"snippet"`
		Rank      float64 `json:"rank"`
	} `json:"content"`
	LogsIndexed bool   `json:"logs_indexed"`
	NextCursor  string `json:"next_cursor,omitempty"`
}

func decodeSearchBody(t *testing.T, rec *httptest.ResponseRecorder) searchContentBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body searchContentBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	return body
}

// getRawBody returns the raw response body bytes for debug output.
//
//nolint:unused // kept for ad-hoc debugging when a test fails; harmless dead-code for the linter.
func getRawBody(rec *httptest.ResponseRecorder) string { return rec.Body.String() }
