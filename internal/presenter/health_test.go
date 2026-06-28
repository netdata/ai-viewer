package presenter

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/netdata/ai-viewer/internal/store"
)

// healthBody is the in-test mirror of healthResponse. We intentionally
// keep a separate struct so test failures cite the JSON contract the UI
// depends on, not the in-package field names.
type healthBody struct {
	Status        string         `json:"status"`
	StatusDetail  string         `json:"status_detail,omitempty"`
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

// TestHealth_EmptyDBReportsNoSourcesConfigured asserts a clean database with
// no configured source rows reports the explicit no_sources_configured detail.
func TestHealth_EmptyDBReportsNoSourcesConfigured(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusDegraded {
		t.Fatalf("status = %q, want %q", body.Status, healthStatusDegraded)
	}
	if body.StatusDetail != "no_sources_configured" {
		t.Fatalf("status_detail = %q, want no_sources_configured", body.StatusDetail)
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

// TestHealth_LegacyLastSeenLagIsSecondary asserts sources.last_seen_at lag is
// still reported but no longer drives health. Lifecycle/read-model state is the
// freshness truth after SOW-0114.
func TestHealth_LegacyLastSeenLagIsSecondary(t *testing.T) {
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
	if _, err := db.Exec(
		`INSERT INTO source_progress
		 (source_id, updated_at, lifecycle_state, lifecycle_state_at, tail_heartbeat_at,
		  read_model_state, read_model_state_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", fixedTime.UnixMicro(), "tailing", fixedTime.UnixMicro(),
		fixedTime.UnixMicro(), "ready", fixedTime.UnixMicro(),
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q, want ok", body.Status)
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

func TestHealth_DegradedOnLifecycleFailure(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/fail", "codex", "/tmp/fail", 1, now, now,
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress
		 (source_id, updated_at, lifecycle_state, lifecycle_state_at, tail_failed_at, lifecycle_error,
		  read_model_state, read_model_state_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/fail", now, "tail_failed", now, now, "tail stopped", "ready", now,
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, raw, body := doHealthRaw(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusDegraded {
		t.Fatalf("status = %q, want degraded", body.Status)
	}
	var extended struct {
		Sources []struct {
			ID               string `json:"id"`
			LifecycleState   string `json:"lifecycle_state"`
			LifecycleStateAt int64  `json:"lifecycle_state_at"`
			TailFailedAt     *int64 `json:"tail_failed_at"`
			LifecycleError   string `json:"lifecycle_error,omitempty"`
			ReadModelState   string `json:"read_model_state"`
			ReadModelStateAt int64  `json:"read_model_state_at"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &extended); err != nil {
		t.Fatalf("decode extended health body: %v", err)
	}
	if len(extended.Sources) != 1 {
		t.Fatalf("extended sources len = %d, want 1", len(extended.Sources))
	}
	src := extended.Sources[0]
	if src.LifecycleState != "tail_failed" {
		t.Fatalf("lifecycle_state = %q, want tail_failed", src.LifecycleState)
	}
	if src.LifecycleStateAt != now {
		t.Fatalf("lifecycle_state_at = %d, want %d", src.LifecycleStateAt, now)
	}
	if src.TailFailedAt == nil || *src.TailFailedAt != now {
		t.Fatalf("tail_failed_at = %v, want %d", src.TailFailedAt, now)
	}
	if src.LifecycleError != "tail stopped" {
		t.Fatalf("lifecycle_error = %q, want tail stopped", src.LifecycleError)
	}
	if src.ReadModelState != "ready" || src.ReadModelStateAt != now {
		t.Fatalf("read-model state = %q/%d, want ready/%d", src.ReadModelState, src.ReadModelStateAt, now)
	}
}

func TestHealth_StoppedSourceWithFailedReadModelDoesNotDegrade(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()

	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
		"codex:/tmp/stopped", "codex", "/tmp/stopped", 1, now,
	); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO source_progress
		 (source_id, updated_at, lifecycle_state, lifecycle_state_at,
		  read_model_state, read_model_state_at, read_model_repair_failed_at, read_model_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"codex:/tmp/stopped", now, "stopped", now,
		"repair_failed", now, now, "historical repair failure",
	); err != nil {
		t.Fatalf("seed source_progress: %v", err)
	}

	code, body := doHealth(t, p)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != healthStatusOK {
		t.Fatalf("status = %q, want ok", body.Status)
	}
	if len(body.Sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(body.Sources))
	}
	src := body.Sources[0]
	if src.LifecycleState != "stopped" {
		t.Fatalf("lifecycle_state = %q, want stopped", src.LifecycleState)
	}
	if src.ReadModelState != "repair_failed" {
		t.Fatalf("read_model_state = %q, want repair_failed", src.ReadModelState)
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
// "degraded" trigger. Only source-scoped parse errors count. The
// fixture also includes ONE source-scoped parse
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

// healthBodyWithRaw is the in-test mirror of the response body that
// preserves the raw JSON bytes so the test can assert the field is
// ABSENT (not just the typed Go value) — omitempty is the contract, and
// the only way to prove the field is genuinely absent is to check the
// rendered bytes. Mirrors the healthBody struct but adds a Raw field that
// holds the exact response payload.
type healthBodyWithRaw struct {
	Status        string         `json:"status"`
	Version       string         `json:"version"`
	SchemaVersion int            `json:"schema_version"`
	UptimeS       int64          `json:"uptime_s"`
	DBPath        string         `json:"db_path"`
	DBSizeBytes   int64          `json:"db_size_bytes"`
	Sources       []healthSource `json:"sources"`
}

// doHealthRaw runs a GET /api/health and returns the raw response bytes
// alongside the typed body. Tests that need to assert field-absence use
// the raw bytes; tests that need the typed shape use body.
func doHealthRaw(t *testing.T, p *Presenter) (int, []byte, healthBodyWithRaw) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	p.Handler().ServeHTTP(rr, req)
	raw := rr.Body.Bytes()
	var body healthBodyWithRaw
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode /api/health body: %v\nraw: %s", err, raw)
		}
	}
	return rr.Code, raw, body
}

// newHealthPresenterWithLogger mirrors newTestPresenter but lets the
// caller inject a slog.Logger that captures structured output (for
// asserting the malformed-meta WARN). The presenter itself never
// mutates the logger; the helper is purely for test isolation.
func newHealthPresenterWithLogger(t *testing.T, logger *slog.Logger) (*Presenter, *sql.DB, func()) {
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
		"frontend_dist/assets/app.js": &fstest.MapFile{
			Data:    []byte("console.log('test');\n"),
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

// TestHealth_SourceMetaOmittedAndPresent pins the SOW-0024 read path on
// /api/health:
//   - (a) a source with sources.meta_json = NULL renders the response
//     without a `meta` field (omitempty + absence = "not populated");
//   - (b) a source with a valid JSON blob renders the blob verbatim;
//   - (c) a source with a malformed meta_json renders WITHOUT the field
//     AND emits a WARN carrying the source id (the no-silent-corruption
//     defence — the sole-writer ingester would never produce this, but
//     the presenter must not corrupt the response if it ever does).
func TestHealth_SourceMetaOmittedAndPresent(t *testing.T) {
	t.Parallel()

	// (a) meta_json = NULL → field absent.
	t.Run("omitted when null", func(t *testing.T) {
		t.Parallel()
		p, db, cleanup := newTestPresenter(t)
		defer cleanup()
		now := fixedTime.UnixMicro()
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			"aiagent_v3:/tmp/null", "aiagent_v3", "/tmp", 1, now, now,
		); err != nil {
			t.Fatalf("seed sources: %v", err)
		}
		code, raw, body := doHealthRaw(t, p)
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if len(body.Sources) != 1 {
			t.Fatalf("sources len = %d, want 1", len(body.Sources))
		}
		// Typed check: Meta is a json.RawMessage; the nil/empty value
		// is the omitempty signal — the JSON encoder would skip it.
		if body.Sources[0].Meta != nil {
			t.Errorf("meta on NULL-source: %s, want nil (omitempty)", body.Sources[0].Meta)
		}
		// Raw check: the field must be absent, not present-as-null.
		if strings.Contains(string(raw), `"meta":`) {
			t.Errorf("raw response carries a meta field on NULL-source: %s", raw)
		}
	})

	// (b) valid JSON blob → rendered verbatim.
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
			t.Fatalf("seed sources: %v", err)
		}
		_, _, body := doHealthRaw(t, p)
		if len(body.Sources) != 1 {
			t.Fatalf("sources len = %d, want 1", len(body.Sources))
		}
		got := strings.TrimSpace(string(body.Sources[0].Meta))
		if got != blob {
			t.Errorf("meta = %q, want %q (verbatim round-trip)", got, blob)
		}
	})

	// (c) malformed meta_json → field omitted + WARN logged.
	t.Run("omitted + warn on malformed", func(t *testing.T) {
		t.Parallel()
		logger, buf := captureLogger()
		p, db, cleanup := newHealthPresenterWithLogger(t, logger)
		defer cleanup()
		now := fixedTime.UnixMicro()
		if _, err := db.Exec(
			`INSERT INTO sources (id, format, location, enabled, last_seen_at, meta_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"opencode:/tmp/bad", "opencode", "/tmp/bad", 1, now, `{not-json`, now,
		); err != nil {
			t.Fatalf("seed sources: %v", err)
		}
		code, raw, body := doHealthRaw(t, p)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (health must answer on malformed meta)", code)
		}
		if len(body.Sources) != 1 {
			t.Fatalf("sources len = %d, want 1", len(body.Sources))
		}
		if body.Sources[0].Meta != nil {
			t.Errorf("meta on malformed-source: %s, want nil (omit on invalid JSON)", body.Sources[0].Meta)
		}
		if strings.Contains(string(raw), `"meta":`) {
			t.Errorf("raw response carries a meta field on malformed-source: %s", raw)
		}
		// The WARN must be present with the source id; the no-silent-corruption
		// defence is the whole point.
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

// newHealthPresenterWithLogger above references t.Context() (Go 1.24+); a
// project that pinned older Go would need context.Background(). The
// project Go version is 1.24+ (see go.mod), so t.Context() is fine and
// is the modern, lint-clean idiom.
