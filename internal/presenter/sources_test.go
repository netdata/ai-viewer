package presenter

import (
	"context"
	"database/sql"
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

// doSourcesRaw issues GET /api/sources and returns the raw response
// bytes alongside the typed body so tests can assert field-absence
// (the SOW-0024 contract for meta on NULL — omitempty must skip the
// field, not emit it as null).
func doSourcesRaw(t *testing.T, p *Presenter) (int, []byte, sourcesBody) {
	return doSourcesRawWith(t, p, "")
}

func doSourcesRawWith(t *testing.T, p *Presenter, query string) (int, []byte, sourcesBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sources"+query, nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	raw := rr.Body.Bytes()
	var body sourcesBody
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v\nraw: %s", err, raw)
		}
	}
	return rr.Code, raw, body
}

// newSourcesPresenterWithLogger mirrors newHealthPresenterWithLogger
// for the /api/sources handler: it lets the caller inject a slog.Logger
// that captures structured output (for asserting the malformed-meta
// WARN). Sourced from the store package via a fresh :memory: DB.
func newSourcesPresenterWithLogger(t *testing.T, logger *slog.Logger) (*Presenter, *sql.DB, func()) {
	t.Helper()
	s, err := store.OpenWriter(t.Context(), ":memory:", logger)
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
		_ = s.Close()
		t.Fatalf("New: %v", err)
	}
	return p, s.DB(), func() { _ = s.Close() }
}

// TestSources_SourceMetaOmittedAndPresent mirrors
// TestHealth_SourceMetaOmittedAndPresent for the /api/sources read path
// (SOW-0024): (a) NULL → field absent; (b) valid blob → rendered
// verbatim; (c) malformed blob → field omitted + WARN logged.
func TestSources_SourceMetaOmittedAndPresent(t *testing.T) {
	t.Parallel()

	t.Run("omitted when null", func(t *testing.T) {
		t.Parallel()
		p, db, cleanup := newTestPresenter(t)
		defer cleanup()
		now := fixedTime.UnixMicro()
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			"aiagent_v3:/tmp/null", "aiagent_v3", "/tmp", 1, now, now,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
		code, raw, body := doSourcesRaw(t, p)
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if len(body.Items) != 1 {
			t.Fatalf("items len = %d, want 1", len(body.Items))
		}
		if body.Items[0].Meta != nil {
			t.Errorf("meta on NULL-source: %s, want nil (omitempty)", body.Items[0].Meta)
		}
		if strings.Contains(string(raw), `"meta":`) {
			t.Errorf("raw response carries a meta field on NULL-source: %s", raw)
		}
	})

	t.Run("present when valid", func(t *testing.T) {
		t.Parallel()
		p, db, cleanup := newTestPresenter(t)
		defer cleanup()
		now := fixedTime.UnixMicro()
		blob := `{"session_count":42,"message_count":1200,"part_count":3400,"latest_migration":"0009_x"}`
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, meta_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"opencode:/tmp/x", "opencode", "/tmp/x", 1, now, blob, now,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, _, body := doSourcesRaw(t, p)
		if len(body.Items) != 1 {
			t.Fatalf("items len = %d, want 1", len(body.Items))
		}
		got := strings.TrimSpace(string(body.Items[0].Meta))
		if got != blob {
			t.Errorf("meta = %q, want %q (verbatim round-trip)", got, blob)
		}
	})

	t.Run("omitted + warn on malformed", func(t *testing.T) {
		t.Parallel()
		logger, buf := captureLogger()
		p, db, cleanup := newSourcesPresenterWithLogger(t, logger)
		defer cleanup()
		now := fixedTime.UnixMicro()
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, meta_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"opencode:/tmp/bad", "opencode", "/tmp/bad", 1, now, `{not-json`, now,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
		code, raw, body := doSourcesRaw(t, p)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(body.Items) != 1 {
			t.Fatalf("items len = %d, want 1", len(body.Items))
		}
		if body.Items[0].Meta != nil {
			t.Errorf("meta on malformed-source: %s, want nil (omit on invalid JSON)", body.Items[0].Meta)
		}
		if strings.Contains(string(raw), `"meta":`) {
			t.Errorf("raw response carries a meta field on malformed-source: %s", raw)
		}
		var warnLine map[string]any
		for _, rawLine := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			var m map[string]any
			if err := json.Unmarshal([]byte(rawLine), &m); err != nil {
				t.Fatalf("unmarshal log: %v (raw: %q)", err, rawLine)
			}
			if m["msg"] == "presenter: dropping malformed sources.meta_json (sole-writer contract violation)" {
				warnLine = m
				break
			}
		}
		if warnLine == nil {
			t.Fatalf("no malformed-meta WARN in log; got:\n%s", buf.String())
		}
		if got, _ := warnLine["source_id"].(string); got != "opencode:/tmp/bad" {
			t.Errorf("WARN source_id = %q, want %q", got, "opencode:/tmp/bad")
		}
		if got, _ := warnLine["level"].(string); got != "WARN" {
			t.Errorf("WARN level = %q, want WARN", got)
		}
	})
}

// doSources issues GET /api/sources against the presenter handler. By
// default (no include= flag) the response OMITS cursors — this matches
// the operator-facing Sources page. Tests that assert cursor contents
// pass ?include=cursors via doSourcesWithCursors.
func doSources(t *testing.T, p *Presenter) (int, sourcesBody) {
	return doSourcesWith(t, p, "")
}

func doSourcesWithCursors(t *testing.T, p *Presenter) (int, sourcesBody) {
	return doSourcesWith(t, p, "?include=cursors")
}

func doSourcesWith(t *testing.T, p *Presenter, query string) (int, sourcesBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sources"+query, nil)
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

// TestSources_RejectsUnknownIncludeToken keeps /api/sources on the shared
// fail-closed include-token contract: cursors is the only accepted token.
func TestSources_RejectsUnknownIncludeToken(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	req := httptest.NewRequest(http.MethodGet, "/api/sources?include=payload_refs", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var env errorEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if env.Error.Code != CodeBadRequest {
		t.Fatalf("error.code = %q, want %q", env.Error.Code, CodeBadRequest)
	}
}

// TestSources_EmptyReturns200WithEmptyItems asserts an empty database
// returns 200 with `items: []`.
func TestSources_EmptyReturns200WithEmptyItems(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()
	code, body := doSourcesWithCursors(t, p)
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

	code, body := doSourcesWithCursors(t, p)
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

func TestSources_ListsLifecycleAndReadModelState(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, parse_errors, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/life", "codex", "/tmp/life", 1, now, 0, now,
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress
		 (source_id, last_seq, last_ts_us, cursor, updated_at,
		  lifecycle_state, lifecycle_state_at, scan_started_at, scan_completed_at,
		  tail_started_at, tail_heartbeat_at, tail_restart_count,
		  read_model_state, read_model_state_at, read_model_repair_started_at,
		  read_model_repair_completed_at, read_model_repair_attempts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/life", 100, now-1_000, `{"offset":42}`, now,
		"tailing", now-900, now-800, now-700, now-600, now-500, 2,
		"ready", now-400, now-300, now-200, 1,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, raw, _ := doSourcesRaw(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var body struct {
		Items []struct {
			ID                         string `json:"id"`
			LifecycleState             string `json:"lifecycle_state"`
			LifecycleStateAt           int64  `json:"lifecycle_state_at"`
			ScanStartedAt              *int64 `json:"scan_started_at"`
			ScanCompletedAt            *int64 `json:"scan_completed_at"`
			TailStartedAt              *int64 `json:"tail_started_at"`
			TailHeartbeatAt            *int64 `json:"tail_heartbeat_at"`
			TailRestartCount           int64  `json:"tail_restart_count"`
			ReadModelState             string `json:"read_model_state"`
			ReadModelStateAt           int64  `json:"read_model_state_at"`
			ReadModelRepairStartedAt   *int64 `json:"read_model_repair_started_at"`
			ReadModelRepairCompletedAt *int64 `json:"read_model_repair_completed_at"`
			ReadModelRepairAttempts    int64  `json:"read_model_repair_attempts"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode extended sources body: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	item := body.Items[0]
	if item.ID != "codex:/tmp/life" {
		t.Fatalf("id = %q, want codex:/tmp/life", item.ID)
	}
	if item.LifecycleState != "tailing" || item.LifecycleStateAt != now-900 {
		t.Fatalf("lifecycle state = %q/%d, want tailing/%d", item.LifecycleState, item.LifecycleStateAt, now-900)
	}
	if item.ScanStartedAt == nil || *item.ScanStartedAt != now-800 {
		t.Fatalf("scan_started_at = %v, want %d", item.ScanStartedAt, now-800)
	}
	if item.ScanCompletedAt == nil || *item.ScanCompletedAt != now-700 {
		t.Fatalf("scan_completed_at = %v, want %d", item.ScanCompletedAt, now-700)
	}
	if item.TailStartedAt == nil || *item.TailStartedAt != now-600 {
		t.Fatalf("tail_started_at = %v, want %d", item.TailStartedAt, now-600)
	}
	if item.TailHeartbeatAt == nil || *item.TailHeartbeatAt != now-500 {
		t.Fatalf("tail_heartbeat_at = %v, want %d", item.TailHeartbeatAt, now-500)
	}
	if item.TailRestartCount != 2 {
		t.Fatalf("tail_restart_count = %d, want 2", item.TailRestartCount)
	}
	if item.ReadModelState != "ready" || item.ReadModelStateAt != now-400 {
		t.Fatalf("read-model state = %q/%d, want ready/%d", item.ReadModelState, item.ReadModelStateAt, now-400)
	}
	if item.ReadModelRepairStartedAt == nil || *item.ReadModelRepairStartedAt != now-300 {
		t.Fatalf("read_model_repair_started_at = %v, want %d", item.ReadModelRepairStartedAt, now-300)
	}
	if item.ReadModelRepairCompletedAt == nil || *item.ReadModelRepairCompletedAt != now-200 {
		t.Fatalf("read_model_repair_completed_at = %v, want %d", item.ReadModelRepairCompletedAt, now-200)
	}
	if item.ReadModelRepairAttempts != 1 {
		t.Fatalf("read_model_repair_attempts = %d, want 1", item.ReadModelRepairAttempts)
	}
}

func TestSources_ZeroLifecycleTimestampsAreUnset(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		"codex:/tmp/zero", "codex", "/tmp/zero", 1, now,
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress
		 (source_id, updated_at, lifecycle_state, lifecycle_state_at,
		  scan_started_at, tail_heartbeat_at, read_model_state, read_model_state_at,
		  read_model_repair_started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/zero", now, "unknown", 0, 0, 0, "unknown", 0, 0,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, raw, _ := doSourcesRaw(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if strings.Contains(string(raw), `"lifecycle_state_at":0`) ||
		strings.Contains(string(raw), `"scan_started_at":0`) ||
		strings.Contains(string(raw), `"tail_heartbeat_at":0`) ||
		strings.Contains(string(raw), `"read_model_state_at":0`) ||
		strings.Contains(string(raw), `"read_model_repair_started_at":0`) {
		t.Fatalf("zero timestamp leaked as wire value: %s", raw)
	}
	var body struct {
		Items []struct {
			LifecycleStateAt         *int64 `json:"lifecycle_state_at"`
			ScanStartedAt            *int64 `json:"scan_started_at"`
			TailHeartbeatAt          *int64 `json:"tail_heartbeat_at"`
			ReadModelStateAt         *int64 `json:"read_model_state_at"`
			ReadModelRepairStartedAt *int64 `json:"read_model_repair_started_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode extended sources body: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	item := body.Items[0]
	if item.LifecycleStateAt != nil ||
		item.ScanStartedAt != nil ||
		item.TailHeartbeatAt != nil ||
		item.ReadModelStateAt != nil ||
		item.ReadModelRepairStartedAt != nil {
		t.Fatalf("zero timestamp pointers = %+v, want all nil", item)
	}

	code, raw, _ = doSourcesRawWith(t, p, "?include=cursors")
	if code != http.StatusOK {
		t.Fatalf("include=cursors status = %d, want 200", code)
	}
	if strings.Contains(string(raw), `"progress_updated_at":0`) ||
		strings.Contains(string(raw), `"updated_at":0`) ||
		strings.Contains(string(raw), `"lifecycle_state_at":0`) ||
		strings.Contains(string(raw), `"read_model_state_at":0`) {
		t.Fatalf("include=cursors zero timestamp leaked as wire value: %s", raw)
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

// TestSources_DefaultOmitsCursor pins the perf-critical default: the
// operator-facing /api/sources response carries the source metadata
// + observability counters but OMITS the per-source cursor blob
// (1–15 MB per source, ~70 MB total for a typical multi-source
// install). The cursor field is required for the diagnostic page
// only and must be opted into via ?include=cursors — fetching the
// default response never pays the cursor cost.
func TestSources_DefaultOmitsCursor(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, parse_errors, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp/omit", "aiagent_v3", "/tmp/omit", 1, now, 0, now,
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, cursor, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp/omit", 50, now-1000, `{"files":{"x":{"offset":42}}}`, now,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, body := doSources(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(body.Items))
	}
	raw, _ := jsonEncode(body)
	if strings.Contains(string(raw), `"offset":42`) {
		t.Errorf("default response must NOT include cursor text; got %s", raw)
	}
	if strings.Contains(string(raw), `"cursor"`) {
		t.Errorf("default response must NOT include the cursor key at all; got %s", raw)
	}
}

// jsonEncode is a thin wrapper kept for readability — the test
// asserts on the raw JSON bytes of the response envelope to confirm
// the cursor field is omitted at the wire level.
func jsonEncode(v any) ([]byte, error) {
	return json.Marshal(v)
}
