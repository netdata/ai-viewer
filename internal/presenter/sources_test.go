package presenter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// sourcesBody mirrors sourcesResponse for test decoding. Keeping the
// shape independent of the production struct makes failures cite the
// JSON contract.
type sourcesBody struct {
	Items []sourceItem `json:"items"`
}

// doSources issues GET /api/sources against the presenter handler.
func doSources(t *testing.T, p *Presenter) (int, sourcesBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body sourcesBody
	if rr.Body.Len() > 0 {
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return rr.Code, body
}

// TestSources_EmptyReturns200WithEmptyItems asserts an empty database
// returns 200 with `items: []`.
func TestSources_EmptyReturns200WithEmptyItems(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body := doSources(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 0 {
		t.Fatalf("items = %v, want empty", body.Items)
	}
}

// TestSources_ListsConfiguredSources asserts the join between sources
// and source_progress surfaces every configured source with the cursor
// + last_seq observability counter (max SourceSeq seen; NOT a dedup
// gate) the ingester persisted.
func TestSources_ListsConfiguredSources(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	rows := []struct {
		id, format, location string
		lastSeen, parseErr   int64
	}{
		{"aiagent_v3:/tmp/a", "aiagent_v3", "/tmp/a", now - 1_000_000, 0},
		{"aiagent_v2:/tmp/b", "aiagent_v2", "/tmp/b", now - 5_000_000, 2},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, parse_errors, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.id, r.format, r.location, 1, r.lastSeen, r.parseErr, now,
		); err != nil {
			t.Fatalf("seed sources: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO source_progress (source_id, last_seq, last_ts_us, cursor, updated_at) VALUES (?, ?, ?, ?, ?)`,
			r.id, 100, now-1_000, `{"offset":42}`, now,
		); err != nil {
			t.Fatalf("seed source_progress: %v", err)
		}
	}

	code, body := doSources(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(body.Items))
	}

	// Lookup by id rather than relying on ordering — the SQL orders by
	// (created_at, id) which is implementation detail.
	byID := map[string]sourceItem{}
	for _, it := range body.Items {
		byID[it.ID] = it
	}
	a, ok := byID["aiagent_v3:/tmp/a"]
	if !ok {
		t.Fatal("aiagent_v3:/tmp/a missing")
	}
	if a.Format != "aiagent_v3" || a.Location != "/tmp/a" {
		t.Fatalf("source a: format=%q location=%q", a.Format, a.Location)
	}
	if a.LastSeq != 100 {
		t.Fatalf("source a: last_seq=%d", a.LastSeq)
	}
	if a.Cursor != `{"offset":42}` {
		t.Fatalf("source a: cursor=%q", a.Cursor)
	}
	if !a.Enabled {
		t.Fatal("source a: enabled = false, want true")
	}

	b := byID["aiagent_v2:/tmp/b"]
	if b.ParseErrors != 2 {
		t.Fatalf("source b: parse_errors = %d, want 2", b.ParseErrors)
	}
}

// TestSources_MethodNotAllowedOnPost asserts non-GET is rejected with a
// structured 405 carrying the METHOD_NOT_ALLOWED code.
func TestSources_MethodNotAllowedOnPost(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodPost, "/api/sources", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if env.Error.Code != CodeMethodNotAllowed {
		t.Fatalf("error.code = %q, want %q", env.Error.Code, CodeMethodNotAllowed)
	}
}

// TestSources_NullableSourceProgressFields asserts a sources row that
// has no source_progress companion still answers with sensible
// defaults (last_seq=0, cursor="") rather than dropping the row.
func TestSources_NullableSourceProgressFields(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, body := doSources(t, p)
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	it := body.Items[0]
	if it.Cursor != "" {
		t.Fatalf("cursor = %q, want empty", it.Cursor)
	}
	if it.LastSeq != 0 {
		t.Fatalf("last_seq = %d, want 0", it.LastSeq)
	}
	if it.UpdatedAt != nil {
		t.Fatalf("updated_at = %v, want nil", it.UpdatedAt)
	}
}

// TestSources_DBErrorLogCarriesRequestID pins observability.md §"Trace
// IDs" for the DB-error code path: when the sources query fails, the
// structured error log line MUST carry the same `request_id` as the
// X-Request-ID response header. The seven DB-error log sites in
// health.go + sources.go were emitted with `err`
// only — no request_id — so a failed query could not be grepped back to
// its access log line.
//
// The "sources query failed" path is the simplest of the seven to
// trigger: closing the underlying *sql.DB before the handler runs makes
// QueryContext return immediately with `sql: database is closed`, hits
// the LogAttrs site at sources.go:73, and bypasses the rows.Next /
// rows.Err branches.
func TestSources_DBErrorLogCarriesRequestID(t *testing.T) {
	t.Parallel()

	// Capture log output via a JSON handler at debug level so every
	// LogAttrs site (Warn / Error / Debug) is preserved for assertion.
	logger, buf := captureLogger()

	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}

	frontend := fstest.MapFS{
		"frontend_dist/index.html": &fstest.MapFile{
			Data:    []byte("<!doctype html><title>test</title>"),
			ModTime: fixedTime,
		},
	}
	p, err := New(Options{
		DB:            s.DB(),
		Logger:        logger,
		Version:       "test-sha",
		DBPath:        "/tmp/test.db",
		StartedAt:     fixedTime.Add(-30 * time.Second),
		SchemaVersion: SchemaVersion,
		Now:           func() time.Time { return fixedTime },
		FrontendFS:    frontend,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Force the QueryContext call to fail with "sql: database is
	// closed" — the cleanest way to drive the error branch without
	// stubbing the DB.
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body=%q)", rr.Code, rr.Body.String())
	}
	rid := rr.Header().Get("X-Request-ID")
	if !uuidV4Re.MatchString(rid) {
		t.Fatalf("X-Request-ID = %q (not UUID-v4)", rid)
	}

	// Walk every emitted log line; assert the "sources query failed"
	// ERROR line is present and carries the same request_id as the
	// response header.
	var errLine map[string]any
	for _, raw := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("unmarshal log: %v (raw: %q)", err, raw)
		}
		if m["msg"] == "presenter: sources query failed" {
			errLine = m
			break
		}
	}
	if errLine == nil {
		t.Fatalf("no \"presenter: sources query failed\" log line: %q", buf.String())
	}
	if got, _ := errLine["request_id"].(string); got != rid {
		t.Fatalf("error log request_id = %q, want %q (matches X-Request-ID)", got, rid)
	}
}
