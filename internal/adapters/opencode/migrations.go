package opencode

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file reads opencode's `__drizzle_migrations` table (SOW-0005 chunk D).
// It serves two consumers:
//
//   - the cursor schema hash (schemaHash over the ordered migration names),
//     replacing chunk C's interim present-column-shape fingerprint;
//   - the auto-discovery probe (ProbeStatus: session/message/part counts +
//     the latest migration name) the ingester surfaces at startup (AC#8).
//
// All SQL here uses fixed table identifiers, never operator input. The DB is
// always opened via the chunk-A openReadOnly helper; this file never opens a
// write path.

// migrationsTable is opencode's Drizzle migration journal. Its `name` column
// holds the migration directory name (e.g. "20260510033149_session_usage"),
// which embeds a YYYYMMDDHHMMSS timestamp prefix; Drizzle's `id` increments in
// application order (adapter-opencode.md §"__drizzle_migrations").
const migrationsTable = "__drizzle_migrations"

// errNoMigrationsTable is the soft sentinel returned by readMigrations when the
// __drizzle_migrations table does not exist (a very old or foreign SQLite file).
// Callers treat it as non-fatal: no schema hash, no migration reported, but the
// adapter keeps running rather than crashing on a database that is not
// opencode's. It is distinguished from a genuine query error (a corrupt table,
// an I/O fault) which IS propagated.
var errNoMigrationsTable = errors.New("opencode: __drizzle_migrations table not present")

// readMigrations reads the applied-migration names from __drizzle_migrations in
// application order (ORDER BY id ASC) and returns them plus the latest (the name
// with the highest id, i.e. the last element). A missing table yields
// (nil, "", errNoMigrationsTable) so callers degrade gracefully; any other query
// error is wrapped and returned. Rows with a NULL/empty name are skipped (the
// column is nullable in opencode's schema) so a stray empty name never pollutes
// the hash or masquerades as the latest migration.
func readMigrations(ctx context.Context, db *sql.DB) (names []string, latest string, err error) {
	if !migrationsTablePresent(ctx, db) {
		return nil, "", errNoMigrationsTable
	}
	// id is the Drizzle auto-increment applied-order key; ORDER BY id ASC gives
	// application order. Fixed identifiers only (migrationsTable), never input.
	q := `SELECT name FROM ` + quoteIdent(migrationsTable) + ` ORDER BY id ASC` // #nosec G202 -- migrationsTable is a fixed package constant via quoteIdent, never user input
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, "", fmt.Errorf("opencode: read %s: %w", migrationsTable, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name sql.NullString
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, "", fmt.Errorf("opencode: scan %s.name: %w", migrationsTable, scanErr)
		}
		if name.Valid && name.String != "" {
			names = append(names, name.String)
		}
	}
	if rErr := rows.Err(); rErr != nil {
		return nil, "", fmt.Errorf("opencode: iterate %s: %w", migrationsTable, rErr)
	}
	if len(names) > 0 {
		latest = names[len(names)-1]
	}
	return names, latest, nil
}

// migrationsTablePresent reports whether __drizzle_migrations exists, via
// sqlite_master. This is the cheap precheck that lets readMigrations return the
// soft sentinel for a foreign/old database instead of surfacing the driver's
// "no such table" as a hard error. The name is a fixed constant, bound as a
// parameter here (sqlite_master.name accepts a bind, unlike a PRAGMA argument).
func migrationsTablePresent(ctx context.Context, db *sql.DB) bool {
	var present int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`,
		migrationsTable).Scan(&present)
	return err == nil && present > 0
}

// schemaHash returns the hex SHA-256 of the ordered migration-name list — the
// cursor's schema_hash (adapter-opencode.md §"Cursor"). Each name is framed with
// its byte length and a newline ("<len>:<name>\n") before hashing, so the digest
// is UNAMBIGUOUS regardless of the names' content (a length-prefix is the same
// injection-safe framing the presenter cursor fingerprint uses): two different
// migration lists, the same names in a different order, or a single name that
// happens to contain a separator all yield distinct hashes. An empty list
// (missing/foreign table) yields "" so the cursor records no hash rather than
// the digest of the empty string.
func schemaHash(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString(strconv.Itoa(len(n)))
		b.WriteByte(':')
		b.WriteString(n)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// readSchemaHash reads __drizzle_migrations and returns the schema hash for the
// applied migrations, or "" when the table is absent (a foreign/old database).
// A genuine query error is propagated so scanLoop/tailLoop can surface it. This
// is the single helper the poll loops call at scan/tail start to stamp the
// cursor's schema_hash with the REAL migration-name digest (replacing chunk C's
// present-column-shape placeholder).
func readSchemaHash(ctx context.Context, db *sql.DB) (string, error) {
	names, _, err := readMigrations(ctx, db)
	if err != nil {
		if errors.Is(err, errNoMigrationsTable) {
			return "", nil
		}
		return "", err
	}
	return schemaHash(names), nil
}

// probeCountTables are the three tracked tables ProbeStatus counts, in the order
// it reports them. They are the canonical tree (session/message/part); the
// session_message sidecar is not surfaced in the startup probe (it carries only
// agent/model-switch markers, not session volume).
var probeCountTables = []string{"session", "message", "part"}

// ProbeStatus opens the opencode database at dbPath strictly read-only and
// returns the row counts of the session/message/part tables plus the latest
// applied migration name (AC#8). The ingester's auto-discovery calls it once at
// startup to surface what the source will yield via /api/health and the
// discovery log.
//
// Cost note: each count is a full COUNT(*). On a multi-GB database that is a few
// hundred ms ONCE at startup, which is acceptable for a one-time probe; the
// steady-state tailer never runs these (it uses the PK-indexed MAX(id) gate).
//
// Graceful degradation: a table that does not exist makes its count 0 and is
// recorded as a soft error (joined into the returned err) rather than failing
// the whole probe, so a foreign SQLite file the probe stumbles on still
// registers and is observable. A missing __drizzle_migrations table likewise
// leaves latestMigration empty without erroring. A hard open/ping failure (the
// file is unreadable) IS returned so discovery can log it.
func ProbeStatus(ctx context.Context, dbPath string) (sessions, messages, parts int64, latestMigration string, err error) {
	db, openErr := openReadOnly(ctx, dbPath)
	if openErr != nil {
		return 0, 0, 0, "", fmt.Errorf("opencode: probe open %s (ro): %w", dbPath, openErr)
	}
	defer func() { _ = db.Close() }()

	counts := make([]int64, len(probeCountTables))
	var softErrs []error
	for i, table := range probeCountTables {
		n, cErr := countRows(ctx, db, table)
		if cErr != nil {
			softErrs = append(softErrs, cErr)
			continue
		}
		counts[i] = n
	}

	_, latest, mErr := readMigrations(ctx, db)
	if mErr != nil && !errors.Is(mErr, errNoMigrationsTable) {
		softErrs = append(softErrs, mErr)
	}

	return counts[0], counts[1], counts[2], latest, errors.Join(softErrs...)
}

// countRows returns COUNT(*) for a tracked table. The table name is a fixed
// probeCountTables entry, never operator input, so it is safe to interpolate
// (quoted as an identifier defensively, mirroring maxID/maxTimeUpdated).
func countRows(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var n int64
	q := `SELECT COUNT(*) FROM ` + quoteIdent(table) // #nosec G202 -- table is a fixed probeCountTables identifier via quoteIdent, never user input
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("opencode: count %s: %w", table, err)
	}
	return n, nil
}
