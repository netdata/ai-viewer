package presenter

import (
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/notify"
)

// SchemaVersion is the canonical schema version the binary was built
// against. A migration bumps schema_meta.version together with this constant
// when serve reads or validates its outcome — a schema-shape change, or served
// data; an ingester-only migration serve never reads (e.g. 0002_source_progress.sql)
// stays version-neutral. Servers refuse to start when the on-disk
// schema_meta.version differs from this value — see CheckSchema below — so a
// store that has not yet had the latest migration applied is rejected rather
// than served with stale rows. 0005 (the op-duration backfill) is the latest;
// it sets schema_meta.version='5'.
const SchemaVersion = 5

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

	// notBuiltLogOnce guards the single Info log emitted when GET / falls
	// back to the not-built notice (the embedded FS has no index.html).
	// Logged once so a dev-time unbuilt UI is visible without flooding the
	// log on every request (observability.md §Structured Logging;
	// presenter.md §"serveIndex contract").
	notBuiltLogOnce sync.Once

	// hub fans matched notify events out to connected SSE clients; subs is
	// the REST-facing subscription registry kept consistent with the hub.
	hub  *notify.Hub
	subs *subscriptionManager

	// sseLifecycleMu serializes SSE-subscription creation against SSE
	// shutdown. It guards sseShuttingDown and spans the whole
	// check-shutting-down → create (hub.Add + registry insert) critical
	// section in createSubscriptionLifecycle, and the state flip in
	// ShutdownSSE. Without it an atomic flag checked once and then read again
	// across create still leaves a TOCTOU window: ShutdownSSE could run
	// between the check and the create, yielding a 200 for a subscription the
	// closed hub already dropped, or an orphan registry entry with no hub
	// channel (presenter.md §SSE Lifecycle Mutex; rest-api.md §POST
	// /api/subscriptions).
	//
	// Lock-ordering: sseLifecycleMu → (hub.mu via hub.Add) →
	// subscriptionManager.mu. ShutdownSSE never holds sseLifecycleMu while
	// calling hub.Shutdown, and neither the OnRemove hook nor the notify
	// poller acquires sseLifecycleMu, so no cycle can form.
	sseLifecycleMu sync.Mutex
	// sseShuttingDown is set true under sseLifecycleMu at the start of
	// ShutdownSSE (before the hub is closed) so a POST /api/subscriptions
	// racing the shutdown window returns 503 instead of minting a
	// subscription the closed hub would drop.
	sseShuttingDown bool

	// notifyPollInterval drives runNotifyPoller; sseKeepalive is the SSE
	// keepalive-comment cadence; notifyNow is the poller/coalesce clock
	// (injected by tests, defaults to time.Now.UTC).
	notifyPollInterval time.Duration
	sseKeepalive       time.Duration
	notifyNow          func() time.Time

	// notifyMu guards the poller's high-water state. notifyCursor is the
	// seq the poller has consumed (advances every poll); lastAppliedSeq /
	// lastAppliedTS are the seq + ts_us of the most recent applied row,
	// surfaced by /api/health. statsCoalesce tracks the last stats_invalidated
	// emit time per subscription so the poller can rate-limit to ≈1/s.
	notifyMu       sync.Mutex
	notifyCursor   int64
	lastAppliedSeq int64
	lastAppliedTS  int64
	statsCoalesce  map[string]time.Time
}

// defaultNotifyPollInterval is the notify-table poll cadence
// (sse-protocol.md §Transport: "~1 s interval").
const defaultNotifyPollInterval = time.Second

// defaultSSEKeepalive is the idle keepalive-comment cadence
// (sse-protocol.md §Event Types §keepalive: "every 15 s").
const defaultSSEKeepalive = 15 * time.Second

// statsCoalesceWindow is the minimum spacing between stats_invalidated
// events delivered to a single subscription (sse-protocol.md
// §stats_invalidated: "rate-limited to ~1 per second").
const statsCoalesceWindow = time.Second

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

	hub := opts.Hub
	if hub == nil {
		hub = notify.New(notify.Options{})
	}
	pollInterval := opts.NotifyPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultNotifyPollInterval
	}

	p := &Presenter{
		db:                 opts.DB,
		logger:             logger,
		version:            opts.Version,
		dbPath:             opts.DBPath,
		startedAt:          startedAt,
		schemaVersion:      schemaVersion,
		nowFn:              now,
		frontend:           opts.FrontendFS,
		hub:                hub,
		subs:               newSubscriptionManager(hub),
		notifyPollInterval: pollInterval,
		sseKeepalive:       defaultSSEKeepalive,
		notifyNow:          now,
		statsCoalesce:      make(map[string]time.Time),
	}
	// Wire per-subscription cleanup: whenever the hub drops a subscription
	// (retention expiry OR explicit Remove), forget its server-side filter
	// and coalesce state so neither leaks past the hub's lifetime. The hook
	// runs without the hub lock held and must NOT call back into hub.Remove
	// (the removal already happened); onSubRemoved touches only the
	// presenter's own maps.
	hub.SetOnRemove(p.onSubRemoved)
	return p, nil
}

// onSubRemoved is the hub's OnRemove hook: it drops a dropped subscription's
// presenter-side state (the registry filter entry and the poller's
// statsCoalesce timestamp). It must never call hub.Remove — the hub already
// removed the subscription before invoking this hook, and a callback would
// be redundant. Touching only presenter-owned locks here (the manager mutex
// via forget, then notifyMu) keeps the lock-ordering one-directional:
// manager.mu / notifyMu are always acquired AFTER the hub lock is released,
// never while holding it.
func (p *Presenter) onSubRemoved(id string) {
	p.subs.forget(id)
	p.forgetStatsCoalesce(id)
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
//   - GET /api/sessions/{id}/topology  actor graph (agents+tools) for the session tree
//   - GET /api/sessions/{id}/timeline  per-session lanes + spans for the timeline view
//   - GET /api/topology           cross-session actor graph (agents only) over the filtered set
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
	// `/api/` catch-all, so still-unimplemented sub-routes (catalog,
	// payloads) fall through to notImplemented.
	mux.HandleFunc("/api/health", p.handleHealth)
	mux.HandleFunc("/api/sources", p.handleSources)
	mux.HandleFunc("/api/sessions", p.handleSessionsList)
	mux.HandleFunc("/api/sessions/{id}", p.handleSessionDetail)
	mux.HandleFunc("/api/sessions/{id}/logs", p.handleSessionLogs)
	mux.HandleFunc("/api/sessions/{id}/topology", p.handleSessionTopology)
	mux.HandleFunc("/api/sessions/{id}/timeline", p.handleSessionTimeline)
	mux.HandleFunc("/api/topology", p.handleCrossTopology)
	mux.HandleFunc("/api/stats", p.handleStats)
	mux.HandleFunc("/api/subscriptions", p.handleSubscriptionsCreate)
	mux.HandleFunc("/api/subscriptions/{id}", p.handleSubscriptionDelete)
	mux.HandleFunc("/api/events", p.handleEvents)
	mux.HandleFunc("/api/", p.notImplemented)

	// Frontend routes.
	mux.HandleFunc("/assets/", p.serveAsset)
	// Root public files Vite copies from frontend/public/ to dist/ root
	// (e.g. /favicon.svg). Each gets an explicit exact route so the file is
	// served with its correct content-type + cache; without it the SPA
	// fallback in rootHandler would serve the HTML shell in its place
	// (presenter.md §"Root public assets").
	for _, name := range publicRootFiles {
		mux.HandleFunc("/"+name, p.servePublicFile)
	}
	mux.HandleFunc("/", p.rootHandler)

	chain := chainMiddleware(mux,
		loggingMiddleware(p.logger),
		recoverMiddleware(p.logger),
		bodyLimitMiddleware,
		gzipMiddleware(p.logger),
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

// rootHandler is the SPA-fallback catch-all: it serves the embedded SPA
// shell for any GET/HEAD request the mux did not route to a more-specific
// pattern. The mux already sends /api/* (structured JSON, incl. NOT_FOUND
// for unknown sub-routes), /assets/* (hashed bundle, 404 on miss), and each
// exact root public file (/favicon.svg) to dedicated handlers; everything
// else falls through here. Serving the shell — built index.html, the
// not-built notice, or 500 when the FS is unwired, all via serveIndex with
// 200 + no-cache + Content-Length + HEAD parity — is what lets a hard
// navigation / reload / bookmark of a client-side route (/sessions/:id,
// /sources, /topology, /tools, /models, /agents) load the app instead of a
// JSON 404; BrowserRouter then renders its own NotFound for genuinely
// unknown client paths (presenter.md §"SPA fallback"). Only /api/* and
// /assets/* are exempt so real API/asset errors still surface. A
// non-GET/HEAD method is 405 METHOD_NOT_ALLOWED; HEAD support is mandatory
// per RFC 9110 §9.3.2 — every resource that supports GET MUST also support
// HEAD with identical headers and an empty body.
func (p *Presenter) rootHandler(w http.ResponseWriter, r *http.Request) {
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
// land in later chunks. The topology/timeline session sub-routes are now
// live (SOW-0006); the still-pending routes are catalog/payloads. The SSE
// subscription surface (subscriptions/events) is live.
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
