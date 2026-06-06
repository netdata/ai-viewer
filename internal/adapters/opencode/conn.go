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

// txlockDeferred is the forced _txlock value: a deferred BEGIN takes its
// snapshot on the first SELECT and never acquires a write lock. The DSN builder
// drops any caller _txlock (e.g. "exclusive", which would open a write-path
// BEGIN) and sets this, so the read-only contract holds even against a
// maliciously-constructed path string (adapter-opencode.md §"Read Strategy").
const txlockDeferred = "deferred"

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
//	file:<abs-path>?_pragma=query_only(true)&_pragma=busy_timeout(5000)&_txlock=deferred&mode=ro
//
// mode=ro asks the OS to open the file O_RDONLY: SQLite cannot upgrade the
// connection to writable, so it is the primary, OS-enforced guard. The
// _pragma entries are the SQL-layer second line of defence.
//
// Read-safety policy (adapter-opencode.md §"Read Strategy"): the DSN is built
// as an ALLOWLIST, not a denylist. ALL caller-supplied `_pragma` values are
// DROPPED and replaced with exactly the readOnlyPragmas set, so no caller
// pragma — colliding or not — survives. A write-path pragma the old denylist
// did not name (wal_checkpoint(TRUNCATE), optimize, foreign_keys(on), …) can
// therefore never reach the connection. `_txlock` is forced to `deferred`
// (any caller `_txlock`, e.g. `exclusive`, is dropped) so a BEGIN can never
// take a write lock. mode=ro is forced regardless of the caller.
//
// A DSN that is already a "file:" URI (or an in-memory ":memory:" form used
// only by tests that want a throwaway shared cache) is accepted; its query is
// parsed only to be DISCARDED and rebuilt from the read-only set, so callers
// may hand either a bare path or a pre-built URI without weakening the guard.
//
// Bare-path opacity (SOW-0005 round-4 P3-2): the `?`-split that strips the query
// runs ONLY for the URI forms (file: / :memory:). A BARE filesystem path is
// treated as OPAQUE — POSIX allows '?' in a filename, so a bare path containing
// '?' opens the LITERAL file rather than misparsing everything after the '?' as a
// DSN query. The whole bare path (including any '?') is percent-escaped into the
// file: URI path. The default opencode database path contains no '?', so this is a
// correctness guard for an unusual --source location, not a change to the common
// case.
//
// CLI CONTRACT (SOW-0005 round-3 P2-4): the ingest CLI's opencode source
// location is always a FILESYSTEM PATH — both auto-discovery and
// `--source opencode:<path>` resolve to a real path, which
// cmd/ai-viewer-ingest's startSource validates with os.Stat BEFORE constructing
// the adapter. os.Stat fails for the "file:"/":memory:" DSN forms, so those
// shapes are NOT valid --source locations: they exist purely for this package's
// programmatic and test callers (throwaway shared-cache DBs). buildReadOnlyDSN
// still accepts them when called directly; the filesystem-path-only rule is a
// CLI-layer contract, not a restriction enforced here.
func buildReadOnlyDSN(dbPath string) (string, error) {
	if dbPath == "" {
		return "", fmt.Errorf("opencode: database path must be non-empty")
	}

	fileURI, existingQuery, err := normalizeSQLiteTarget(dbPath)
	if err != nil {
		return "", err
	}
	if err := validateSQLiteQuery(dbPath, existingQuery); err != nil {
		return "", err
	}
	return fileURI + "?" + readOnlyQuery().Encode(), nil
}

func normalizeSQLiteTarget(dbPath string) (fileURI, existingQuery string, err error) {
	switch {
	case strings.HasPrefix(dbPath, "file:"):
		fileURI, existingQuery = splitQuery(dbPath)
	case isMemoryDSN(dbPath):
		fileURI, existingQuery = splitQuery(dbPath)
	default:
		fileURI, err = barePathFileURI(dbPath)
	}
	return fileURI, existingQuery, err
}

// Bare filesystem paths are opaque: POSIX permits '?' in filenames, so do not
// split them as DSN queries; percent-escape the whole path into the file URI.
func barePathFileURI(dbPath string) (string, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("opencode: resolve db path %q: %w", dbPath, err)
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return "file:" + escapeURIPath(uriPath), nil
}

func validateSQLiteQuery(dbPath, existingQuery string) error {
	if _, err := url.ParseQuery(existingQuery); err != nil {
		return fmt.Errorf("opencode: invalid db DSN query for %q: %w", dbPath, err)
	}
	return nil
}

func readOnlyQuery() url.Values {
	params := url.Values{}
	params.Set("mode", "ro")
	params.Set("_txlock", txlockDeferred)
	for _, p := range readOnlyPragmas {
		params.Add("_pragma", p)
	}
	return params
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
	return dsn == ":memory:" ||
		strings.HasPrefix(dsn, ":memory:?") ||
		dsn == "file::memory:" ||
		strings.HasPrefix(dsn, "file::memory:?")
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
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentContinue(s[i]) {
			return false
		}
	}
	return true
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isIdentContinue(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// escapeURIPath percent-escapes a slash-separated absolute path for use as
// the opaque path of a "file:" SQLite URI, preserving the '/' separators.
// url.PathEscape escapes a single segment (including '/'), so the path is
// split on '/' and each segment escaped independently.
func escapeURIPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(url.PathEscape(s), ":", "%3A")
	}
	return strings.Join(segs, "/")
}
