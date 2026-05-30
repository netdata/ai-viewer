package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	// Registers the "sqlite" driver with database/sql. Pure-Go, CGO-free
	// per AGENTS.md tech stack — the same driver internal/store uses, but
	// this adapter opens a DIFFERENT database (opencode's, not ai-viewer's)
	// under a DIFFERENT, read-only-only contract. See openReadOnly.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver identifier registered by
// modernc.org/sqlite. Kept in a constant so the DSN builder and openReadOnly
// agree; mirrors internal/store/store.go:driverName.
const driverName = "sqlite"

// readOnlyPragmas is the EXACT, ordered set of connection-time PRAGMAs the
// adapter appends to every opencode DSN. It is the read-safety contract, in
// one place, asserted by conn_test.go's six write-probes:
//
//   - query_only(true): SQL-layer rejection of any INSERT/UPDATE/DELETE and
//     of write-path PRAGMAs (wal_checkpoint, etc). Defence-in-depth on top
//     of the OS-level mode=ro below.
//   - busy_timeout(5000): wait up to 5 s for a lock rather than failing
//     immediately. Locks are rare in WAL mode but happen during opencode's
//     own checkpoint; matches opencode's own busy_timeout
//     (anomalyco/opencode @ 2b3ddf9 :: packages/opencode/src/storage/db.ts).
//
// Deliberately NOT included (SOW-0005 Open Decision #1, recorded):
//
//   - foreign_keys: immaterial for a read-only connection — no write can
//     violate a constraint — so it is omitted rather than forced off.
//   - journal_mode(WAL): a read-only connection cannot change the journal
//     mode, and opening a WAL-mode database with mode=ro already enters WAL
//     reader mode (consistent snapshot from the last checkpoint, never
//     blocking opencode's writer). Setting it would be a no-op at best and a
//     spurious write attempt at worst, so it is left out.
//
// This is intentionally NARROWER than internal/store.buildDSN, which targets
// ai-viewer's OWN database and forces foreign_keys(on) + journal_mode(wal) +
// synchronous(normal) for a writable pool of 8. That helper must never be
// pointed at opencode.db: its writer contract and FK enforcement are wrong
// for an external, live, read-only source.
var readOnlyPragmas = []string{
	"query_only(true)",
	"busy_timeout(5000)",
}

// maxOpenConns bounds the read pool. SOW-0005 Open Decision #1: two
// connections — one for the watch poll, one for a rare presenter-triggered
// re-read. A live multi-GB WAL database tolerates many concurrent readers,
// but the adapter needs no more than two and a small bound keeps file
// descriptors and WAL page cache predictable.
const maxOpenConns = 2

// connMaxLifetime recycles a pooled connection periodically so stale WAL
// pages held in a long-lived connection's cache are released back, per
// adapter-opencode.md §"Read Strategy" (SetConnMaxLifetime(30 * time.Minute)).
const connMaxLifetime = 30 * time.Minute

// buildReadOnlyDSN turns an opencode database file path into the read-only
// modernc.org/sqlite DSN. The path is made absolute and wrapped in the
// "file:" URI form so the driver preserves the query string — without the
// "file:" prefix modernc.org/sqlite strips everything after the first '?'
// (conn.go:53-55), which would silently drop mode=ro and the PRAGMAs and let
// the OS open the file read+write. The path component is percent-escaped so
// a directory containing '?', '#', or spaces cannot corrupt the query.
//
// The resulting DSN always carries, in this order:
//
//	file:<abs-path>?mode=ro&_pragma=query_only(true)&_pragma=busy_timeout(5000)
//
// mode=ro asks the OS to open the file O_RDONLY: SQLite cannot upgrade the
// connection to writable, so it is the primary, OS-enforced guard. The
// _pragma entries are the SQL-layer second line of defence.
//
// A DSN that is already a "file:" URI (or an in-memory ":memory:" form used
// only by tests that want a throwaway shared cache) is passed through with
// the read-only query parameters merged in, so callers may hand either a
// bare path or a pre-built URI.
func buildReadOnlyDSN(dbPath string) (string, error) {
	if dbPath == "" {
		return "", fmt.Errorf("opencode: database path must be non-empty")
	}

	prefix, existingQuery := splitQuery(dbPath)

	// Normalise the prefix into a "file:" URI with an absolute, escaped path.
	var fileURI string
	switch {
	case strings.HasPrefix(prefix, "file:"):
		fileURI = prefix
	case isMemoryDSN(prefix):
		fileURI = prefix
	default:
		abs, err := filepath.Abs(prefix)
		if err != nil {
			return "", fmt.Errorf("opencode: resolve db path %q: %w", prefix, err)
		}
		// SQLite URIs require forward slashes and percent-escaping of the
		// path. url.PathEscape leaves '/' intact (it escapes only segment
		// reserved characters), giving a valid opaque file: path.
		uriPath := filepath.ToSlash(abs)
		if !strings.HasPrefix(uriPath, "/") {
			uriPath = "/" + uriPath
		}
		fileURI = "file:" + escapeURIPath(uriPath)
	}

	params, err := url.ParseQuery(existingQuery)
	if err != nil {
		return "", fmt.Errorf("opencode: invalid db DSN query for %q: %w", dbPath, err)
	}

	// mode=ro is the OS-level guard; the store always wins here.
	params.Set("mode", "ro")

	// Strip any caller-supplied _pragma whose name collides with our
	// read-only set, then append our values once. modernc.org/sqlite sorts
	// the _pragma slice before executing, so appending without stripping
	// would not guarantee our value wins; removing the collision by name is
	// the same discipline internal/store uses.
	if existing := params["_pragma"]; len(existing) > 0 {
		kept := existing[:0]
		for _, v := range existing {
			if !pragmaNameCollides(v) {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			delete(params, "_pragma")
		} else {
			params["_pragma"] = kept
		}
	}
	for _, p := range readOnlyPragmas {
		params.Add("_pragma", p)
	}

	return fileURI + "?" + params.Encode(), nil
}

// openReadOnly opens the opencode database at dbPath strictly read-only and
// returns the pooled *sql.DB. It is the ONLY way this package acquires a
// connection to opencode's database, and the single chokepoint the
// read-safety tests exercise.
//
// Why a separate helper and not store.OpenReader:
//
//   - store.OpenReader targets ai-viewer's OWN database. It forces
//     foreign_keys(on) and a pool of 8 and is paired with a writer process
//     (the ingester) that runs migrations. None of that is correct for an
//     external, live, concurrently-written source we must never touch.
//   - The opencode DB is the highest read-safety risk in the project: a
//     stray write corrupts the operator's primary coding tool. Isolating its
//     connection logic in this package keeps the contract — DSN, PRAGMAs,
//     pool bounds — visible and independently testable, decoupled from
//     ai-viewer's own-DB concerns.
//
// sql.Open is lazy (it never contacts the database), so PingContext is
// invoked immediately to surface a missing or unreadable file at the open
// call rather than at the first delta query. Because the DSN carries
// mode=ro, the OS refuses to create a missing file, so a non-existent path
// fails here rather than silently materialising an empty database.
func openReadOnly(ctx context.Context, dbPath string, opts ...connOption) (*sql.DB, error) {
	cfg := connConfig{
		maxOpenConns:    maxOpenConns,
		connMaxLifetime: connMaxLifetime,
	}
	for _, o := range opts {
		o(&cfg)
	}

	dsn, err := buildReadOnlyDSN(dbPath)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opencode: open %q (ro): %w", dbPath, err)
	}
	db.SetMaxOpenConns(cfg.maxOpenConns)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(cfg.connMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opencode: ping %q (ro): %w", dbPath, err)
	}
	return db, nil
}

// connConfig holds the tunables openReadOnly applies to the pool. Defaults
// come from the package constants; tests override them via connOption to
// keep production defaults the single source of truth.
type connConfig struct {
	maxOpenConns    int
	connMaxLifetime time.Duration
}

// connOption mutates a connConfig. Used by tests to pin a single connection
// or a short lifetime; production callers pass no options and inherit the
// package defaults.
type connOption func(*connConfig)

// withMaxOpenConns overrides the pool size. Test-only knob.
func withMaxOpenConns(n int) connOption {
	return func(c *connConfig) { c.maxOpenConns = n }
}

// splitQuery splits a DSN into its prefix (path or file: URI) and the query
// string after the first '?'. Mirrors store.splitDSNQuery; kept local so the
// adapter stays self-contained per SOW-0005 Open Decision #1.
func splitQuery(dsn string) (prefix, query string) {
	if p, q, ok := strings.Cut(dsn, "?"); ok {
		return p, q
	}
	return dsn, ""
}

// isMemoryDSN reports whether dsn refers to an in-memory SQLite database.
// Only tests use the in-memory form; production always passes a file path.
func isMemoryDSN(dsn string) bool {
	if dsn == ":memory:" {
		return true
	}
	return strings.HasPrefix(dsn, "file::memory:") ||
		strings.Contains(dsn, ":memory:?") ||
		strings.HasPrefix(dsn, ":memory:?")
}

// pragmaNameCollides reports whether the caller-supplied _pragma value names
// one of the read-only pragmas the helper enforces (query_only, busy_timeout).
// A schema-qualified form (e.g. "main.query_only(false)") is normalised to
// its bare name first so it cannot slip past the strip pass and re-enable a
// write path. Comparison is case-insensitive.
func pragmaNameCollides(v string) bool {
	name := pragmaName(v)
	return name == "query_only" || name == "busy_timeout"
}

// pragmaName extracts the lowercase pragma identifier from a _pragma value,
// tolerating the "name(value)" and "name=value" forms and an optional
// "<schema>." qualifier. A trimmed, lowercased identifier is returned.
func pragmaName(v string) string {
	v = strings.TrimSpace(v)
	if dot := strings.IndexByte(v, '.'); dot > 0 && isBareIdent(v[:dot]) {
		v = v[dot+1:]
	}
	end := len(v)
	for i, r := range v {
		if r == '(' || r == '=' || r == ' ' || r == '\t' {
			end = i
			break
		}
	}
	return strings.ToLower(strings.TrimSpace(v[:end]))
}

// isBareIdent reports whether s is a bare SQL identifier
// ([A-Za-z_][A-Za-z0-9_]*) — used to recognise a "<schema>." qualifier so
// pragmaName strips exactly one legitimate schema prefix and nothing else.
func isBareIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlpha := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isAlpha {
			return false
		}
		if i > 0 && !isAlpha && !isDigit {
			return false
		}
	}
	return true
}

// escapeURIPath percent-escapes a slash-separated absolute path for use as
// the opaque path of a "file:" SQLite URI, preserving the '/' separators.
// url.PathEscape escapes a single segment (including '/'), so the path is
// split on '/' and each segment escaped independently.
func escapeURIPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
