package presenter

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"time"
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
	// fstest.MapFS. nil disables the frontend routes (every "/" or
	// "/assets/..." request returns 404 with NOT_FOUND).
	FrontendFS fs.FS
}
