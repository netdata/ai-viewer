package presenter

import (
	"context"
	"database/sql"
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

// fixedTime is the wall clock the tests pin so /api/health computes
// uptime + lag deterministically.
var fixedTime = time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

// newTestPresenter returns a Presenter wired against an in-memory
// SQLite database with the canonical schema applied via the store
// migration runner. The returned cleanup function tears the database
// down at end of test.
func newTestPresenter(t *testing.T) (*Presenter, *sql.DB, func()) {
	t.Helper()
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
		"frontend_dist/assets/app.js": &fstest.MapFile{
			Data:    []byte("console.log('test');\n"),
			ModTime: fixedTime,
		},
	}
	p, err := New(Options{
		DB:            s.DB(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	return p, s.DB(), func() { _ = s.Close() }
}

// TestNewRequiresDB asserts the constructor refuses a nil DB. Catches
// a future refactor that accidentally swallowed the validation.
func TestNewRequiresDB(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{}); err == nil {
		t.Fatal("New: want error on nil DB")
	}
}

// TestNewDefaultsLoggerStartedAtVersion asserts the constructor fills
// reasonable defaults when the caller omits optional fields.
func TestNewDefaultsLoggerStartedAtVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open writer store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := New(Options{DB: s.DB()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.logger == nil {
		t.Fatal("logger: want non-nil default")
	}
	if p.startedAt.IsZero() {
		t.Fatal("startedAt: want non-zero default")
	}
	if p.schemaVersion != SchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", p.schemaVersion, SchemaVersion)
	}
}

// TestHandlerRegistersRoutes asserts the basic routing surface:
// /api/health and /api/sources answer 200; /api/sessions returns 404
// with NOT_FOUND (deferred to a later chunk); a method other than GET
// on /api/health returns 405.
func TestHandlerRegistersRoutes(t *testing.T) {
	t.Parallel()
	p, _, cleanup := newTestPresenter(t)
	defer cleanup()

	h := p.Handler()
	if h == nil {
		t.Fatal("Handler: nil")
	}

	cases := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{"health ok", http.MethodGet, "/api/health", http.StatusOK},
		{"sources ok", http.MethodGet, "/api/sources", http.StatusOK},
		{"sessions deferred", http.MethodGet, "/api/sessions", http.StatusNotFound},
		{"unknown api", http.MethodGet, "/api/does-not-exist", http.StatusNotFound},
		{"post health forbidden", http.MethodPost, "/api/health", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("%s %s: status = %d, want %d (body=%q)",
					tc.method, tc.path, rr.Code, tc.wantCode, rr.Body.String())
			}
		})
	}
}

// TestCheckSchemaMatches asserts the schema-version probe accepts a
// freshly-migrated database.
func TestCheckSchemaMatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := CheckSchema(s.DB(), SchemaVersion); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}
}

// TestCheckSchemaMismatch asserts a wrong expected-version is rejected
// with ErrSchemaMismatch wrapped with context.
func TestCheckSchemaMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := store.OpenWriter(ctx, ":memory:", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	err = CheckSchema(s.DB(), 999)
	if err == nil {
		t.Fatal("CheckSchema(999): want error, got nil")
	}
	if !strings.Contains(err.Error(), "999") {
		t.Fatalf("error %q: want it to mention expected version", err)
	}
}

// TestCheckSchemaRejectsNilDB asserts the helper refuses a nil DB up
// front rather than panicking on QueryRow.
func TestCheckSchemaRejectsNilDB(t *testing.T) {
	t.Parallel()
	if err := CheckSchema(nil, 1); err == nil {
		t.Fatal("CheckSchema(nil): want error")
	}
}

// TestPresenter_HeadRouteParity asserts that every route which supports
// GET also supports HEAD with identical status + critical headers and
// an empty body, per RFC 9110 §9.3.2. The asset path is sized below
// gzipMinBytes so the buffering middleware never compresses it, which
// keeps the header comparison apples-to-apples between GET and HEAD.
func TestPresenter_HeadRouteParity(t *testing.T) {
	t.Parallel()
	p, db, cleanup := newTestPresenter(t)
	defer cleanup()
	// Seed one source so /api/health and /api/sources have realistic
	// non-empty bodies on the GET path.
	now := fixedTime.UnixMicro()
	if _, err := db.Exec(
		`INSERT INTO sources (id, format, location, enabled, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"aiagent_v3:/tmp", "aiagent_v3", "/tmp", 1, now, now,
	); err != nil {
		t.Fatalf("seed sources: %v", err)
	}

	routes := []string{"/", "/assets/app.js", "/api/health", "/api/sources"}
	h := p.Handler()
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			getReq := httptest.NewRequest(http.MethodGet, route, nil)
			getRR := httptest.NewRecorder()
			h.ServeHTTP(getRR, getReq)

			headReq := httptest.NewRequest(http.MethodHead, route, nil)
			headRR := httptest.NewRecorder()
			h.ServeHTTP(headRR, headReq)

			if getRR.Code != headRR.Code {
				t.Fatalf("status: GET=%d HEAD=%d", getRR.Code, headRR.Code)
			}
			if getRR.Code != http.StatusOK {
				t.Fatalf("expected 200 from GET %s, got %d body=%q",
					route, getRR.Code, getRR.Body.String())
			}
			if headRR.Body.Len() != 0 {
				t.Fatalf("HEAD %s returned non-empty body (%d bytes): %q",
					route, headRR.Body.Len(), headRR.Body.String())
			}
			for _, h := range []string{"Content-Type", "X-Request-ID"} {
				gv := getRR.Header().Get(h)
				hv := headRR.Header().Get(h)
				if gv == "" {
					t.Errorf("GET %s missing header %s", route, h)
				}
				if hv == "" {
					t.Errorf("HEAD %s missing header %s", route, h)
				}
				// X-Request-ID is per-request random, so we only assert
				// presence + non-empty. Content-Type must match exactly.
				if h == "Content-Type" && gv != hv {
					t.Errorf("HEAD %s Content-Type=%q, GET=%q", route, hv, gv)
				}
			}
		})
	}
}

// TestItoa asserts the tiny stand-in for strconv.Itoa handles the
// edge cases the package relies on.
func TestItoa(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{99, "99"},
		{-5, "-5"},
		{123456, "123456"},
	}
	for _, tc := range cases {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
