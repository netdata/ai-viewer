package presenter

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// healthBody is the in-test mirror of healthResponse. We intentionally
// keep a separate struct so test failures cite the JSON contract the UI
// depends on, not the in-package field names.
type healthBody struct {
	Status        string         `json:"status"`
	Version       string         `json:"version"`
	SchemaVersion int            `json:"schema_version"`
	UptimeS       int64          `json:"uptime_s"`
	DBPath        string         `json:"db_path"`
	DBSizeBytes   int64          `json:"db_size_bytes"`
	Sources       []healthSource `json:"sources"`
}

// doHealth runs a GET /api/health against the presenter's handler and
// decodes the JSON envelope. Fails the test on any decoding error.
func doHealth(t *testing.T, p *Presenter) (int, healthBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	var body healthBody
	if rr.Body.Len() > 0 {
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Fatalf("decode /api/health body: %v", err)
		}
	}
	return rr.Code, body
}

// TestHealth_EmptyDBIsOK asserts a clean database with no rows reports
// status="ok" and an empty sources slice.
func TestHealth_EmptyDBIsOK(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q, want %q", body.Status, healthStatusOK)
	}
	if body.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", body.SchemaVersion, SchemaVersion)
	}
	if body.Version != "test-sha" {
		t.Fatalf("version = %q, want test-sha", body.Version)
	}
	if body.UptimeS < 30 {
		t.Fatalf("uptime_s = %d, want >= 30", body.UptimeS)
	}
	if len(body.Sources) != 0 {
		t.Fatalf("sources = %v, want empty", body.Sources)
	}
}

// TestHealth_DegradedOnLag asserts a fresh source whose last_seen_at is
// older than the 60s threshold flips the global status to "degraded".
func TestHealth_DegradedOnLag(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	// Lag of 120s — well past the 60s threshold from observability.md.
	stale := fixedTime.UnixMicro() - 120_000_000
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, stale, fixedTime.UnixMicro(),
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusDegraded {
		t.Fatalf("status = %q, want degraded", body.Status)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(body.Sources))
	}
	src := body.Sources[0]
	if src.LagUS < 120_000_000 {
		t.Fatalf("lag_us = %d, want >= 120_000_000", src.LagUS)
	}
	if !src.Enabled {
		t.Fatal("source.enabled = false, want true")
	}
}

// TestHealth_DegradedOnParseErrors asserts a fresh SOURCE-SCOPED parse
// error row (source_id NOT NULL, session_id NULL — the shape
// internal/ingest/writer.go applySourceError emits) inside the 1h
// window flips the global status to "degraded" even when no source is
// lagging.
func TestHealth_DegradedOnParseErrors(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	// Source is fresh — no lag.
	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	// Recent SOURCE-scoped ERR — session_id deliberately NULL,
	// source_id set.
	if _, err := db.Exec(
		`INSERT INTO log_entries (ts, severity, source, source_id, session_id, message) VALUES (?, ?, ?, ?, NULL, ?)`,
		now-1_000_000, "ERR", "aiagent_v3", "aiagent_v3:/tmp", "parse failed",
	); err != nil {
		t.Fatalf("seed log_entries: %v", err)
	}

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Status != healthStatusDegraded {
		t.Fatalf("status = %q, want degraded", body.Status)
	}
}

// TestHealth_SessionScopedErrorsDoNotDegrade asserts session-scoped
// log_entries rows (those with a non-NULL session_id — agent / tool
// errors emitted via canonical.LogEntryEvent) do NOT count toward the
// "degraded" trigger. Only source-scoped parse errors count (codex
// iter-3 P2#4). The fixture also includes ONE source-scoped parse
// error so we verify both rows coexist in the table while only the
// source-scoped one flips the verdict — pinning the filter exactly,
// not just rejecting session-scoped rows by accident.
func TestHealth_SessionScopedErrorsDoNotDegrade(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	// Need a sessions row so the session-scoped log_entry FK holds.
	if _, err := db.Exec(
		`INSERT INTO sessions (id, source_id, native_id, root_session_id, kind, status, start_ts, last_activity_ts) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"sess-1", "aiagent_v3:/tmp", "native-1", "sess-1", "agent", "running", now, now,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Session-scoped ERR — should NOT count as a parse error.
	if _, err := db.Exec(
		`INSERT INTO log_entries (ts, severity, source, source_id, session_id, message) VALUES (?, ?, ?, NULL, ?, ?)`,
		now-500_000, "ERR", "agent", "sess-1", "tool failed",
	); err != nil {
		t.Fatalf("seed session-scoped log_entry: %v", err)
	}
	// With ONLY the session-scoped row present the status must be OK.
	_, body := doHealth(t, p)
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q with session-scoped row only, want ok", body.Status)
	}

	// Now add a source-scoped parse error and re-check: status flips to
	// degraded. This pins the discriminator on (source_id NOT NULL AND
	// session_id IS NULL).
	if _, err := db.Exec(
		`INSERT INTO log_entries (ts, severity, source, source_id, session_id, message) VALUES (?, ?, ?, ?, NULL, ?)`,
		now-400_000, "ERR", "aiagent_v3", "aiagent_v3:/tmp", "parse failed",
	); err != nil {
		t.Fatalf("seed source-scoped log_entry: %v", err)
	}
	_, body = doHealth(t, p)
	if body.Status != healthStatusDegraded {
		t.Fatalf("status = %q after adding source-scoped row, want degraded", body.Status)
	}
}

// TestHealth_OKWhenErrorsAreStale asserts a source that lagged in the
// past but whose last_seen_at is now fresh, and a log_entries ERR row
// older than 1 hour, leaves the status at "ok".
func TestHealth_OKWhenErrorsAreStale(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO log_entries (ts, severity, source, source_id, message) VALUES (?, ?, ?, ?, ?)`,
		now-2*60*60*1_000_000, "ERR", "aiagent_v3", "aiagent_v3:/tmp", "ancient",
	); err != nil {
		t.Fatalf("seed log_entries: %v", err)
	}
	code, body := doHealth(t, p)
	if code != http.StatusOK || body.Status != healthStatusOK {
		t.Fatalf("status code=%d body.status=%q", code, body.Status)
	}
}

// TestHealth_DisabledSourceDoesNotDegrade asserts a disabled source
// with stale last_seen_at does NOT contribute to "degraded" — only
// enabled sources matter (per observability.md §`/api/health`
// "any enabled source has lag_us > 60s").
func TestHealth_DisabledSourceDoesNotDegrade(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	stale := fixedTime.UnixMicro() - 120_000_000
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 0, stale, fixedTime.UnixMicro(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, body := doHealth(t, p)
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q, want ok (disabled source must not flip status)", body.Status)
	}
}

// TestHealth_SourceLastSeenAtNullStaysOK asserts a source with NULL
// last_seen_at (newly created, has not produced any events yet) does
// not flip the status to "degraded" via lag math — the lag for a
// brand-new source is meaningless until the first event arrives.
func TestHealth_SourceLastSeenAtNullStaysOK(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, fixedTime.UnixMicro(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, body := doHealth(t, p)
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q, want ok for fresh-source-no-events", body.Status)
	}
	if len(body.Sources) != 1 || body.Sources[0].LastSeenAt != nil {
		t.Fatalf("expected one source with null last_seen_at, got %+v", body.Sources)
	}
}

// TestHealth_ReportsSourceProgressJoin asserts the per-source last_seq
// reflects source_progress.last_seq when the row is present. last_seq is
// the adapter's opaque observability counter (max SourceSeq seen), NOT a
// dedup gate and NOT a portable event count — see healthSource doc
// comment for the semantics per adapter.
func TestHealth_ReportsSourceProgressJoin(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, cursor, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", 4242, now, "{}", now,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
	_, body := doHealth(t, p)
	if len(body.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(body.Sources))
	}
	if body.Sources[0].LastSeq != 4242 {
		t.Fatalf("last_seq = %d, want 4242", body.Sources[0].LastSeq)
	}
}

// TestHealth_LastSeqJSONFieldName guards the JSON contract — external
// dashboards rely on the field name `last_seq` (renamed from the
// misleading `events_ingested_total` after iteration 2 of Chunk 11
// revealed the v2 adapter packs an FNV-64 hash, not an event count).
func TestHealth_LastSeqJSONFieldName(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress (source_id, last_seq, last_ts_us, cursor, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", 99, now, "{}", now,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	raw := rr.Body.String()
	if !strings.Contains(raw, `"last_seq":99`) {
		t.Fatalf("body missing last_seq:99 — body=%q", raw)
	}
	if strings.Contains(raw, "events_ingested_total") {
		t.Fatalf("body still carries events_ingested_total — body=%q", raw)
	}
}

// TestHealth_DownOnClosedDB asserts the handler reports status="down"
// when every internal query errors. We simulate this by closing the
// underlying *sql.DB and then issuing the request.
func TestHealth_DownOnClosedDB(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	code, body := doHealth(t, p)
	if code != http.StatusOK {
		// 200 is intentional — /api/health always answers; the status
		// field carries the verdict.
		t.Fatalf("status = %d, want 200 (health endpoint never 5xx's on its own)", code)
	}
	if body.Status != healthStatusDown {
		t.Fatalf("status = %q, want down", body.Status)
	}
}

// nullableInt is a helper for tests that need a typed nullable int.
type nullableInt struct {
	sql.NullInt64
}
