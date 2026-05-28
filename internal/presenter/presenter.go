package presenter

import (
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

// SchemaVersion is the canonical schema version the binary was built
// against. Bumped together with internal/store/migrations/NNNN_*.sql.
// Servers refuse to start when the on-disk schema_meta.version is
// different — see CheckSchema below. SOW-0015 migration 0003 sets
// schema_meta.version='3'; this constant moves in lockstep.
const SchemaVersion = 3

// ErrSchemaMismatch is returned by CheckSchema when the on-disk schema
// version disagrees with the binary's expected version. The main()
// caller surfaces this with a clear operator-facing message.
var ErrSchemaMismatch = errors.New("presenter: schema version mismatch")

// Presenter owns the HTTP handlers and the shared dependencies they
// need. One Presenter per process; the value is safe for concurrent use
// once Handler() returns.
type Presenter struct {
	db            *sql.DB
	logger        *slog.Logger
	version       string
	dbPath        string
	startedAt     time.Time
	schemaVersion int
	nowFn         func() time.Time
	frontend      fs.FS
}

// New constructs a Presenter from the provided options. Returns an
// error when a required option is missing; the caller is expected to
// configure DB and Logger before calling.
func New(opts Options) (*Presenter, error) {
	if opts.DB == nil {
		return nil, errors.New("presenter.New: DB is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("subsystem", "presenter")

	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	startedAt := opts.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}
	schemaVersion := opts.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = SchemaVersion
	}

	return &Presenter{
		db:            opts.DB,
		logger:        logger,
		version:       opts.Version,
		dbPath:        opts.DBPath,
		startedAt:     startedAt,
		schemaVersion: schemaVersion,
		nowFn:         now,
		frontend:      opts.FrontendFS,
	}, nil
}

// now returns the current time from the injected clock. Tests inject a
// fixed clock via Options.Now.
func (p *Presenter) now() time.Time { return p.nowFn() }

// Handler returns the http.Handler that the binary's main wires into
// the standard library server. The returned handler chains the
// presenter's middlewares around a fresh ServeMux carrying every
// registered route.
//
// As of Chunk 12 of SOW-0001 the registered routes are:
//
//   - GET /                       serves the embedded SPA shell
//   - GET /assets/...             serves embedded frontend assets
//   - GET /api/health             ok/degraded/down + per-source diagnostics
//   - GET /api/sources            full source list with cursors
//   - GET /api/sessions           filtered, keyset-paginated session list
//   - GET /api/sessions/{id}      session detail (turns, ops, payloads, children)
//   - GET /api/sessions/{id}/logs severity-filtered, paginated log entries
//   - GET /api/stats              cross-session aggregates over the filtered set
//
// Every other route declared in presenter.md returns NOT_FOUND until
// the relevant chunk lands. The middleware chain wraps the whole mux
// so future routes inherit the same invariants.
//
// Chain ordering (outermost first):
//
//  1. loggingMiddleware — mints the request_id, seeds it into
//     r.Context(), wraps the response writer with the byte/status
//     recorder, and defers the access log so it fires AFTER every
//     inner middleware (including recover) has finished. Logging MUST
//     be outermost so the deferred emit observes the final status —
//     including the 500 written by recoverMiddleware on a panic.
//  2. recoverMiddleware — catches handler panics, logs the stack with
//     the request_id from context, and writes a 500 JSON envelope. By
//     sitting inside logging, its writes go through the recording
//     wrapper so the access log shows status=500 + bytes_out for the
//     500 body.
//  3. bodyLimitMiddleware — caps inbound POST/PUT/PATCH bodies.
//  4. gzipMiddleware — buffers and compresses eligible JSON bodies on
//     the response path.
func (p *Presenter) Handler() http.Handler {
	mux := http.NewServeMux()

	// API routes. Strict method gating is done inside each handler so
	// the JSON error envelope is consistent across the surface. Path
	// parameters use Go 1.22+ ServeMux `{id}` wildcards read via
	// r.PathValue; the patterns carry no method verb so every handler
	// keeps the same in-handler gating style (one routing style across
	// the surface). More-specific patterns take precedence over the
	// `/api/` catch-all, so unimplemented sub-routes (topology, timeline)
	// still fall through to notImplemented.
	mux.HandleFunc("/api/health", p.handleHealth)
	mux.HandleFunc("/api/sources", p.handleSources)
	mux.HandleFunc("/api/sessions", p.handleSessionsList)
	mux.HandleFunc("/api/sessions/{id}", p.handleSessionDetail)
	mux.HandleFunc("/api/sessions/{id}/logs", p.handleSessionLogs)
	mux.HandleFunc("/api/stats", p.handleStats)
	mux.HandleFunc("/api/", p.notImplemented)

	// Frontend routes.
	mux.HandleFunc("/assets/", p.serveAsset)
	mux.HandleFunc("/", p.rootHandler)

	chain := chainMiddleware(mux,
		loggingMiddleware(p.logger),
		recoverMiddleware(p.logger),
		bodyLimitMiddleware,
		gzipMiddleware,
	)
	return chain
}

// chainMiddleware wraps base with each middleware in reverse order so
// the first listed middleware runs OUTERMOST. Pattern adapted from the
// standard library idiom: middlewares execute in the order they appear.
func chainMiddleware(base http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	h := base
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// rootHandler dispatches GET / and HEAD / to the embedded SPA shell
// and rejects any other path with NOT_FOUND. The default ServeMux
// routes every unmatched request to "/" so we filter explicitly. HEAD
// support is mandatory per RFC 9110 §9.3.2 — every resource that
// supports GET MUST also support HEAD with identical headers and an
// empty body.
func (p *Presenter) rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "not found", map[string]any{"path": r.URL.Path})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	p.serveIndex(w, r)
}

// notImplemented is the catch-all for routes documented in
// presenter.md but not yet implemented. Returns a structured NOT_FOUND
// so the operator immediately sees which chunk will land the missing
// route. The handler intentionally does NOT use
// http.StatusNotImplemented because that maps to "the server does not
// support this method at all", whereas these routes are scheduled to
// land in later chunks. As of Chunk 12 the still-pending routes are
// topology/timeline (Chunk 14), catalog/payloads, and the SSE
// subscription surface (Chunk 13).
func (p *Presenter) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, p.logger, http.StatusNotFound,
		CodeNotFound, "endpoint not yet implemented in this chunk",
		map[string]any{"path": r.URL.Path, "method": r.Method, "chunk": "13+"})
}

// CheckSchema verifies the SQLite store's schema_meta.version row
// matches expectedVersion. Returns ErrSchemaMismatch wrapped with
// context when the row is absent or carries a different version. The
// caller (the serve binary's main) surfaces the error with an exit
// code so the operator sees a clear failure.
func CheckSchema(db *sql.DB, expectedVersion int) error {
	if db == nil {
		return errors.New("presenter.CheckSchema: nil db")
	}
	var raw string
	if err := db.QueryRow(`SELECT value FROM schema_meta WHERE key='version'`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.Join(ErrSchemaMismatch, errors.New("schema_meta.version row missing"))
		}
		return errors.Join(ErrSchemaMismatch, err)
	}
	var v int
	for _, c := range raw {
		if c < '0' || c > '9' {
			return errors.Join(ErrSchemaMismatch, errors.New("schema_meta.version is non-numeric"))
		}
		v = v*10 + int(c-'0')
	}
	if v != expectedVersion {
		return errors.Join(ErrSchemaMismatch, &schemaVersionError{got: v, want: expectedVersion})
	}
	return nil
}

// schemaVersionError carries the structured numbers behind
// ErrSchemaMismatch so the operator-facing log line shows both sides
// of the mismatch.
type schemaVersionError struct {
	got, want int
}

func (e *schemaVersionError) Error() string {
	return "schema_meta.version is " + itoa(e.got) + ", want " + itoa(e.want)
}

// itoa is a tiny stand-in for strconv.Itoa so the file does not pull in
// strconv solely for two error strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
