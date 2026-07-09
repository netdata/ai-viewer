package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ScanIndexLifecycle manages the drop/rebuild of non-unique secondary indexes
// around the initial bulk scan (SOW-0118). Dropping the 42 non-unique secondary
// indexes during the scan eliminates ~95% of the per-event index write
// amplification (the dominant cost at GB scale). The 2 UNIQUE indexes
// (idx_log_entries_identity, idx_payload_refs_identity — the upsert ON CONFLICT
// targets) and all PK/autoindex entries are ALWAYS kept.
//
// Lifecycle:
//   - DropNonUniqueIndexes: capture + drop every non-unique CREATE INDEX;
//     returns the captured DDL for RecreateIndexes.
//   - RecreateIndexes: re-exec the captured DDL (CREATE INDEX IF NOT EXISTS).
//
// Safety: the drop NEVER touches UNIQUE indexes (the payload_refs identity
// constraint that broke the naive drop is UNIQUE → preserved). A test
// (scan_indexes_test.go) pins that every ON CONFLICT target survives.

// indexDef captures one CREATE INDEX statement for drop/rebuild.
type indexDef struct {
	name string
	sql  string
}

// DropNonUniqueIndexes drops every non-unique CREATE INDEX on all tables,
// keeping PK, autoindex, and all UNIQUE constraints (the upsert ON CONFLICT
// targets). Returns the captured DDL so RecreateIndexes can rebuild them
// post-scan. Safe to call multiple times (idempotent: already-dropped indexes
// are absent from sqlite_master). Does NOT drop FTS virtual tables or their
// shadow indexes (those are managed by the FTS backfill path).
func DropNonUniqueIndexes(ctx context.Context, db *sql.DB) ([]indexDef, error) {
	rows, err := db.QueryContext(ctx, `
SELECT name, sql FROM sqlite_master
WHERE type = 'index'
  AND sql IS NOT NULL
  AND sql NOT LIKE '%UNIQUE%'
ORDER BY name
`)
	if err != nil {
		return nil, fmt.Errorf("scan-index-drop: query indexes: %w", err)
	}
	var defs []indexDef
	for rows.Next() {
		var def indexDef
		if err := rows.Scan(&def.name, &def.sql); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan-index-drop: scan: %w", err)
		}
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("scan-index-drop: iterate: %w", err)
	}
	_ = rows.Close()

	for _, def := range defs {
		stmt := fmt.Sprintf("DROP INDEX IF EXISTS %s", def.name)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("scan-index-drop: drop %s: %w", def.name, err)
		}
	}
	return defs, nil
}

// RecreateIndexes re-executes the captured CREATE INDEX statements (from
// DropNonUniqueIndexes). The statements use IF NOT EXISTS (from migrations) so
// the recreate is idempotent. Call after the bulk scan completes.
func RecreateIndexes(ctx context.Context, db *sql.DB, defs []indexDef) error {
	for _, def := range defs {
		sqlText := strings.TrimSpace(def.sql)
		if sqlText == "" {
			continue
		}
		// Ensure IF NOT EXISTS so a partial recreate is idempotent.
		if !strings.Contains(strings.ToUpper(sqlText), "IF NOT EXISTS") {
			// Insert IF NOT EXISTS after CREATE [UNIQUE] INDEX.
			sqlText = insertIfNotExists(sqlText)
		}
		if _, err := db.ExecContext(ctx, sqlText); err != nil {
			return fmt.Errorf("scan-index-recreate: %s: %w", def.name, err)
		}
	}
	return nil
}

// insertIfNotExists inserts "IF NOT EXISTS" after "CREATE INDEX" or
// "CREATE UNIQUE INDEX" in the DDL string.
func insertIfNotExists(ddl string) string {
	upper := strings.ToUpper(ddl)
	for _, prefix := range []string{"CREATE UNIQUE INDEX ", "CREATE INDEX "} {
		idx := strings.Index(upper, prefix)
		if idx >= 0 {
			return ddl[:idx+len(prefix)] + "IF NOT EXISTS " + ddl[idx+len(prefix):]
		}
	}
	return ddl // no recognized prefix; return as-is (the exec will fail loudly)
}
