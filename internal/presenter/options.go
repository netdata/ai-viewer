package presenter

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// Options carries the cross-cutting dependencies the presenter needs at
// construction time. Every field is required except StartedAt and Now,
// which fall back to time.Now-based defaults when the caller passes a
// zero value.
//
// DB MUST be opened via store.OpenReader so the operating-system mode
// flag prevents any accidental write. The presenter never opens DB
// itself; the binary's main() owns the lifecycle.
type Options struct {
	// DB is the read-only handle into the canonical SQLite store. The
	// presenter holds it for the lifetime of the process.
	DB *sql.DB

	// Logger is the structured logger every handler uses. The presenter
	// attaches `subsystem=presenter` itself; the caller can attach
	// additional fields beforehand.
	Logger *slog.Logger

	// Version is reported by /api/health. Typically the git SHA the
	// binary was built from; "dev" is acceptable for local builds.
	Version string

	// DBPath is the operator-facing DB path reported by /api/health for
	// diagnostics. Cosmetic only; the presenter never reopens it.
	DBPath string

	// StartedAt is the process start time used to compute uptime_s on
	// /api/health. Zero means "set me to time.Now() at New()".
	StartedAt time.Time

	// SchemaVersion is the schema version the binary was built against.
	// /api/health reports it and main() refuses to start when the on-
	// disk schema_meta.version does not match.
	SchemaVersion int

	// Now is an optional clock injection for tests. nil means
	// time.Now.UTC.
	Now func() time.Time

	// FrontendFS holds the embedded frontend assets rooted at
	// `frontend_dist/`. The serve binary owns the embed declaration and
	// passes the resulting fs.FS in. Tests inject a synthetic FS via
	// fstest.MapFS. nil means the frontend was never wired: "/" returns a
	// 500 INTERNAL_ERROR and "/assets/..." returns 404 NOT_FOUND. A wired
	// FS that simply lacks index.html (the .gitkeep-only clean checkout)
	// is NOT disabled — "/" degrades to the built-in not-built notice
	// (presenter.md §"serveIndex contract").
	FrontendFS fs.FS

	// Hub is the in-memory SSE fan-out the subscription routes and the
	// notify poller drive. nil means New constructs a default hub (so
	// existing tests that omit it still get working SSE routes); the serve
	// binary passes an explicit hub so it can deliver the shutdown
	// `disconnect` event before closing the hub.
	Hub *notify.Hub

	// NotifyPollInterval is how often runNotifyPoller polls the notify
	// table. Zero falls back to defaultNotifyPollInterval (~1s). Injected
	// short by tests that exercise the poll loop directly.
	NotifyPollInterval time.Duration
}
