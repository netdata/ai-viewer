package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	// Registers the "sqlite" driver with database/sql. Pure-Go,
	// CGO-free per AGENTS.md tech stack.
	_ "modernc.org/sqlite"
)

// mandatoryWriterPragmaNames is the set of _pragma NAMES the store
// controls when opening for read+write against an on-disk database.
// Any operator-supplied _pragma value whose pragma name matches an
// entry here is stripped from the DSN BEFORE the store appends its
// required value, so the final DSN carries the store's value once and
// only once.
//
// The strip-then-append strategy is mandatory because the
// modernc.org/sqlite driver sorts the `_pragma` slice alphabetically
// (with busy_timeout pinned first) before executing — see
// modernc.org/sqlite@v1.50.1/sqlite.go:143-159. Appending the store
// value last therefore would NOT make it win at runtime; for example
// `synchronous(off)` sorts after `synchronous(normal)` alphabetically
// and the operator's `off` would silently override the store's
// `normal`. Stripping operator entries by NAME removes the conflict
// before the driver ever sees the slice.
var mandatoryWriterPragmaNames = []string{
	"foreign_keys",
	"busy_timeout",
	"journal_mode",
	"synchronous",
}

// mandatoryReaderPragmaNames is the reader-side equivalent. Readers
// do not set journal_mode/synchronous (a read-only connection cannot
// change them) but they DO pin query_only(true) as defence in depth
// against a writer DSN being passed to OpenReader by mistake.
var mandatoryReaderPragmaNames = []string{
	"foreign_keys",
	"busy_timeout",
	"query_only",
}

// mandatoryMemoryWriterPragmaNames is the writer-side set for the
// in-memory variants. SQLite refuses WAL journaling on `:memory:` and
// falls back to MEMORY anyway, so journal_mode and synchronous are
// omitted both from the store's appended values and from the
// strip-list (an operator that requested a non-WAL journal mode on a
// memory DB can keep it; nothing the store enforces would conflict).
var mandatoryMemoryWriterPragmaNames = []string{
	"foreign_keys",
	"busy_timeout",
}

// driverName is the database/sql driver identifier registered by
// modernc.org/sqlite. Kept in a constant so the dsn helper and Open
// agree.
const driverName = "sqlite"

// Store owns the SQLite handle. OpenWriter / OpenReader return it;
// downstream packages reach the underlying *sql.DB via DB(). The struct
// is intentionally minimal — query helpers live in the ingester and
// presenter packages per separation-of-concerns.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// Open is a backward-compatible alias for OpenWriter. New callers
// should use OpenWriter (ingester) or OpenReader (server) explicitly so
// the intent is visible at the call site. See doc.go for the
// single-writer invariant.
func Open(ctx context.Context, dsn string, logger *slog.Logger) (*Store, error) {
	return OpenWriter(ctx, dsn, logger)
}

// OpenWriter opens the SQLite database at dsn for read+write, applies
// the writer PRAGMA set via modernc.org/sqlite's _pragma DSN
// parameters (so every pooled connection inherits them), pings the
// database to surface open failures eagerly, and runs all pending
// migrations from internal/store/migrations.
//
// dsn is the operator-facing DSN; the helper strips any operator
// _pragma value whose name collides with the store-mandated set
// (foreign_keys, busy_timeout, journal_mode, synchronous) and then
// appends the store's required value once. Non-mandatory operator
// pragmas — e.g. cache_size — pass through unchanged. The strip-
// then-append strategy is mandatory because the driver sorts the
// _pragma slice alphabetically before executing; see the comment on
// mandatoryWriterPragmaNames.
//
// Path-style DSNs ("./foo.db", "/tmp/foo.db") are first rewritten to
// the "file:" URI form so modernc.org/sqlite preserves the query
// string. The driver strips everything after the first '?' from
// non-"file:" DSNs (see modernc.org/sqlite conn.go:53-55), which would
// otherwise silently drop the _pragma parameters.
//
// PingContext is invoked immediately after sql.Open because sql.Open
// is lazy — it returns a *sql.DB without ever contacting the database.
// A missing parent directory or unreadable file would otherwise only
// surface on the first query. Ping surfaces it at the open call.
//
// Only one process should hold a writer at a time — the ingester. See
// doc.go.
func OpenWriter(ctx context.Context, dsn string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("subsystem", "store")

	uri, err := pathToFileURI(dsn)
	if err != nil {
		return nil, fmt.Errorf("normalise DSN %q: %w", dsn, err)
	}
	full, err := buildDSN(uri, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, full)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dsn, err)
	}

	// Pin the writer pool to a single connection unconditionally. SQLite
	// WAL allows many concurrent readers but only ONE writer at a time;
	// when the ingester runs N source goroutines (one per source per
	// AGENTS.md tech-stack note), N parallel BeginTx attempts on a
	// multi-conn pool produce SQLITE_BUSY locking errors that the
	// ingester's no-retry policy converts into dropped batches. The
	// single-conn pool serialises BeginTx at the database/sql layer so
	// every transaction either commits or returns a real error — never
	// silently loses data to lock contention. The same setting is also
	// what lets ":memory:" tests see the same DB across pooled handles
	// (otherwise every opened conn is a separate in-memory database).
	// Readers use OpenReader (mode=ro) which keeps the default pool
	// size of 8; concurrent reads remain unbounded. Codex iter-3 P2#5.
	db.SetMaxOpenConns(1)

	// sql.Open does not contact the database. Ping forces a real open
	// so missing-file or permission errors surface here rather than at
	// the first query — and lets us refuse to run migrations against a
	// database we cannot actually reach.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", dsn, err)
	}

	if err := Up(ctx, db, logger); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	return &Store{db: db, logger: logger}, nil
}

// OpenReader opens the SQLite database at dsn for read-only access via
// the modernc.org/sqlite mode=ro + query_only(true) PRAGMA combination,
// applies the reader PRAGMA set (foreign_keys on, busy_timeout 5000),
// pings the database to surface open failures eagerly, and does NOT
// run migrations. Use this from the server process; the ingester is
// the only writer and the only migration runner.
//
// Returns an error if the database file does not exist or cannot be
// opened read-only. sql.Open alone is lazy and returns a *sql.DB even
// for a missing file; the immediate PingContext below forces the
// driver to attempt a real open, which respects mode=ro and refuses
// to create the file. Without the ping, callers would see "open ok"
// followed by a confusing error on their first query.
//
// Path-style DSNs are rewritten to the "file:" URI form before sql.Open
// so the query string (mode=ro, _pragma=...) is honoured by
// modernc.org/sqlite. Without this rewrite the driver strips the query
// from non-"file:" DSNs (conn.go:53-55), the open succeeds with
// SQLITE_OPEN_READWRITE|SQLITE_OPEN_CREATE, and a missing file is
// silently created — query_only(true) would then be the only guard
// against writes, and that is a runtime check rather than an
// OS-level open mode. Wrapping the path forces the driver to honour
// mode=ro so the OS itself refuses CREATE.
func OpenReader(ctx context.Context, dsn string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("subsystem", "store", "mode", "ro")

	uri, err := pathToFileURI(dsn)
	if err != nil {
		return nil, fmt.Errorf("normalise DSN %q: %w", dsn, err)
	}
	full, err := buildDSN(uri, true)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, full)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q (ro): %w", dsn, err)
	}

	// presenter.md §"SQLite Access" pins the reader pool size to 8.
	// Go's database/sql default is unbounded, which on a single-user
	// localhost workstation translates into one fresh file descriptor +
	// PRAGMA-replay round per concurrent request — wasteful, and
	// unbounded burst pressure under a busy SSE fan-out. Pin to the
	// spec'd value so the presenter has a predictable read concurrency
	// envelope. SQLite WAL keeps reads non-blocking across this pool.
	// Codex iter-3 P2#5.
	db.SetMaxOpenConns(8)

	// sql.Open is lazy: it returns a *sql.DB without contacting the
	// driver. We want OpenReader to fail at the open call when the
	// database is missing or unreadable, not at some later query, so
	// force a real connection now and propagate the error.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %q (ro): %w", dsn, err)
	}

	return &Store{db: db, logger: logger}, nil
}

// buildDSN augments the operator-supplied dsn with the connection-time
// PRAGMAs the store requires. modernc.org/sqlite treats every
// `_pragma=name(value)` query parameter as a PRAGMA executed on every
// newly opened connection in the pool — solving the
// per-connection-vs-pool problem inherent to issuing PRAGMA statements
// after sql.Open.
//
// Mandatory store-required pragmas (foreign_keys, busy_timeout,
// journal_mode/synchronous for writers, query_only for readers) are
// enforced by STRIPPING any operator-supplied _pragma whose pragma
// name collides with the store-mandated set, then appending the
// store's value once. The strip step is mandatory because the driver
// sorts the _pragma slice alphabetically with busy_timeout pinned
// first (modernc.org/sqlite@v1.50.1/sqlite.go:143-159) — appending the
// store value last would not make it win at runtime since an operator
// `synchronous(off)` sorts after `synchronous(normal)`. Removing the
// operator entry by name eliminates the collision before the driver
// ever sees the slice. Non-mandatory operator pragmas (cache_size,
// temp_store, …) pass through unchanged.
//
// When readOnly is true, mode=ro and query_only(true) are added and
// journal_mode/synchronous are omitted (a read-only connection cannot
// change journal mode). For in-memory DSNs the journal_mode/synchronous
// pragmas are also skipped — SQLite refuses WAL on :memory:.
//
// buildDSN returns an error when the existing query string cannot be
// parsed; the previous behaviour of swallowing the parse error could
// turn a malformed DSN into a silently-valid one with unintended
// pragmas. Callers surface the error to the operator and refuse to
// open the database.
func buildDSN(dsn string, readOnly bool) (string, error) {
	// Split DSN into path-or-uri prefix and existing query string.
	prefix, existingQuery := splitDSNQuery(dsn)

	params, err := url.ParseQuery(existingQuery)
	if err != nil {
		return "", fmt.Errorf("invalid DSN query for %q: %w", dsn, err)
	}

	// Mode is a modernc.org/sqlite-honoured query param distinct from
	// _pragma; it maps directly to the sqlite open flags. For readers
	// the store value wins; otherwise leave any operator-supplied mode
	// alone.
	if readOnly {
		params.Set("mode", "ro")
	}

	// Pick the strip-list that matches this open's pragma contract.
	var mandatoryNames []string
	switch {
	case readOnly:
		mandatoryNames = mandatoryReaderPragmaNames
	case isMemoryDSN(dsn):
		mandatoryNames = mandatoryMemoryWriterPragmaNames
	default:
		mandatoryNames = mandatoryWriterPragmaNames
	}

	// Strip every operator-supplied _pragma whose pragma name is in
	// the mandatory set. The driver-side sort is now irrelevant for
	// those names because only the store's value will be present.
	if existing := params["_pragma"]; len(existing) > 0 {
		kept := existing[:0]
		for _, v := range existing {
			if !pragmaNameInSet(v, mandatoryNames) {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			delete(params, "_pragma")
		} else {
			params["_pragma"] = kept
		}
	}

	// Append the store-required values exactly once each. Order in the
	// query string no longer matters; the strip pass guaranteed no
	// duplicates for these names.
	appendPragma(params, "foreign_keys(on)")
	appendPragma(params, "busy_timeout(5000)")

	switch {
	case readOnly:
		appendPragma(params, "query_only(true)")
	case isMemoryDSN(dsn):
		// Skip journal_mode/synchronous for :memory: — SQLite refuses
		// WAL on in-memory databases and falls back to MEMORY anyway.
	default:
		appendPragma(params, "journal_mode(wal)")
		appendPragma(params, "synchronous(normal)")
	}

	// buildDSN always appends at least foreign_keys + busy_timeout, so
	// params.Encode() is never empty. splitDSNQuery splits at the FIRST
	// '?', so prefix never contains '?' and we always join with '?'.
	return prefix + "?" + params.Encode(), nil
}

// pragmaNameInSet reports whether the pragma name encoded in v —
// case-insensitive, with any optional `<schema>.` qualifier stripped
// — is present in set. Used by buildDSN to identify operator _pragma
// values it must strip before appending the store's mandatory value.
// The set entries themselves are expected to be lowercase; pragmaName
// already lowercases its output.
func pragmaNameInSet(v string, set []string) bool {
	return slices.Contains(set, pragmaName(v))
}

// pragmaName extracts the pragma identifier from a _pragma value. The
// modernc.org/sqlite driver accepts both `name(value)` and `name=value`
// forms — and SQLite further accepts a schema-qualified form
// `<schema>.<name>(value)` (e.g. `main.foreign_keys(off)` or
// `temp.query_only(false)`). The schema prefix is stripped first so a
// qualified operator value collides with our strip-list and never
// reaches the driver; otherwise an attacker who knew the trick could
// reintroduce a foreign_keys(off) via `main.foreign_keys(off)`. We
// then split at the first `(`, `=` or whitespace and trim surrounding
// whitespace so "  synchronous (normal)" and "synchronous=normal"
// both yield "synchronous".
func pragmaName(v string) string {
	v = strings.TrimSpace(v)
	v = stripSchemaPrefix(v)
	end := len(v)
	for i, r := range v {
		if r == '(' || r == '=' || unicode.IsSpace(r) {
			end = i
			break
		}
	}
	return strings.ToLower(strings.TrimSpace(v[:end]))
}

// stripSchemaPrefix removes an optional `<schema>.` qualifier from a
// _pragma value before pragmaName splits on `(`/`=`/whitespace.
//
// SQLite accepts `PRAGMA main.foreign_keys(off)`,
// `PRAGMA temp.query_only(false)`, and arbitrary `<schema>.<name>(value)`
// — verified locally:
//
//	$ sqlite3 :memory: 'PRAGMA foreign_keys=ON;
//	                    PRAGMA main.foreign_keys(OFF);
//	                    PRAGMA foreign_keys;'
//	0
//
// Without this strip, `_pragma=main.foreign_keys(off)` slips through
// the pragmaName splitter as the identifier `main.foreign_keys`, fails
// to match `foreign_keys` in the strip-list, and survives into the
// final DSN. modernc.org/sqlite then sorts and executes the value;
// alphabetical ordering means the operator's `off` wins after the
// store's required `on`.
//
// Schema identifiers in this position are either bare
// (`[A-Za-z_][A-Za-z0-9_]*`) or quoted (`"x"`, `[x]`, “ `x` “). We
// strip exactly one leading qualifier — multi-`.` chains are not a
// real PRAGMA syntax and stripping more than one segment risks eating
// a legitimate `(`-delimited value if the parser ever drifts. Values
// without a recognisable schema prefix are returned unchanged.
func stripSchemaPrefix(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '"', '[', '`':
		closer := byte('"')
		switch v[0] {
		case '[':
			closer = ']'
		case '`':
			closer = '`'
		}
		end := strings.IndexByte(v[1:], closer)
		if end < 0 {
			return v
		}
		// end is relative to v[1:], so the closer sits at v[1+end].
		dot := 1 + end + 1
		if dot < len(v) && v[dot] == '.' {
			return v[dot+1:]
		}
		return v
	default:
		// Bare identifier: scan letters/digits/underscores up to a '.'.
		// Anything else (including `(`, `=`, whitespace) means there is
		// no schema qualifier and the original value is returned.
		for i := 0; i < len(v); i++ {
			c := v[i]
			isAlpha := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
			isDigit := c >= '0' && c <= '9'
			switch {
			case i == 0 && isAlpha:
				continue
			case i > 0 && (isAlpha || isDigit):
				continue
			case i > 0 && c == '.':
				return v[i+1:]
			default:
				return v
			}
		}
		return v
	}
}

// splitDSNQuery returns the dsn prefix (path or file:// URI without
// query) and the query string (without the leading '?'). It tolerates
// both raw paths ("foo.db", ":memory:") and file URIs
// ("file:foo.db?cache=shared").
func splitDSNQuery(dsn string) (prefix, query string) {
	if p, q, ok := strings.Cut(dsn, "?"); ok {
		return p, q
	}
	return dsn, ""
}

// appendPragma adds a _pragma value to params unconditionally. For
// store-mandated pragma names the caller is expected to have already
// stripped any operator-supplied value, so the appended value ends up
// as the only _pragma carrying that name; for non-mandated pragmas
// any operator value coexists in the slice and is honoured by the
// driver in the order it determines.
func appendPragma(params url.Values, value string) {
	params.Add("_pragma", value)
}

// pathToFileURI rewrites a path-style DSN ("foo.db", "/tmp/foo.db",
// "./foo.db") into the "file:" URI form modernc.org/sqlite needs in
// order to preserve the query string. DSNs that are already "file:"
// URIs, or the in-memory variants, pass through unchanged.
//
// modernc.org/sqlite (conn.go:53-55) strips everything from the first
// '?' in the DSN unless the DSN begins with "file:". Without the
// rewrite, the _pragma parameters built by buildDSN — including
// mode=ro for OpenReader — would be silently discarded.
//
// SQLite URI path rules require forward slashes; on Windows
// "C:\\db.sqlite" becomes "file:/C:/db.sqlite". The path is also
// resolved to absolute form via filepath.Abs so relative DSNs anchored
// to the caller's working directory survive the round-trip.
func pathToFileURI(dsn string) (string, error) {
	if dsn == "" {
		return "", fmt.Errorf("empty DSN")
	}
	if strings.HasPrefix(dsn, "file:") {
		return dsn, nil
	}
	if isMemoryDSN(dsn) {
		return dsn, nil
	}

	// Split off any query the caller already attached.
	prefix, query := splitDSNQuery(dsn)

	abs, err := filepath.Abs(prefix)
	if err != nil {
		return "", fmt.Errorf("resolve DSN path %q: %w", prefix, err)
	}

	// SQLite URIs require forward slashes. On POSIX this is a no-op;
	// on Windows it normalises backslashes.
	uriPath := filepath.ToSlash(abs)
	// Absolute POSIX paths already start with '/'; URIs of the form
	// "file:/absolute/path" are valid per RFC 8089. On Windows the
	// drive-letter path becomes "file:/C:/foo".
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}

	out := "file:" + uriPath
	if query != "" {
		out += "?" + query
	}
	return out, nil
}

// isMemoryDSN reports whether the dsn refers to an in-memory SQLite
// database. The bare ":memory:" form and the "file::memory:" URI form
// are both recognized.
func isMemoryDSN(dsn string) bool {
	if dsn == ":memory:" {
		return true
	}
	// modernc.org/sqlite also recognizes "file::memory:?..." and similar.
	return strings.HasPrefix(dsn, "file::memory:") ||
		strings.Contains(dsn, ":memory:?") ||
		strings.HasPrefix(dsn, ":memory:?")
}

// DB returns the underlying *sql.DB. Downstream packages call this once
// at construction time and cache the handle. The returned pointer is
// valid until Close.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close releases the database handle. Calling Close on a nil receiver
// or after a previous successful Close returns nil.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("close store: %w", err)
	}
	s.db = nil
	return nil
}
