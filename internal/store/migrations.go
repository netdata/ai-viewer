package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// migrationFS embeds the SQL files under migrations/ at build time. The
// runner reads them in lexicographic order and applies any not already
// recorded in _schema_migrations. Files are required to follow the
// NNNN_description.sql convention enforced by lint/spec-drift in CI.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationDir is the directory inside migrationFS holding the SQL
// files. Centralized in a constant so the embed pattern and the
// runtime ReadDir agree.
const migrationDir = "migrations"

// migrationsBookkeepingDDL creates the table that records applied
// migrations. It is intentionally separate from schema_meta — the
// latter is part of the schema versioning surface exposed to operators
// while _schema_migrations is internal bookkeeping for the runner.
const migrationsBookkeepingDDL = `
CREATE TABLE IF NOT EXISTS _schema_migrations (
    filename    TEXT PRIMARY KEY,
    applied_at  INTEGER NOT NULL
)`

// migrationFile is one entry from the embedded migrations/ directory.
type migrationFile struct {
	name string
	sql  string
}

// loadMigrations reads every *.sql file under migrationDir and returns
// them sorted by filename.
func loadMigrations() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrationFS, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations dir: %w", err)
	}

	out := make([]migrationFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		path := migrationDir + "/" + name
		body, readErr := fs.ReadFile(migrationFS, path)
		if readErr != nil {
			return nil, fmt.Errorf("read embedded migration %s: %w", path, readErr)
		}
		out = append(out, migrationFile{name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// loadAppliedMigrations returns the set of migration filenames already
// recorded in _schema_migrations. The runner uses this to skip files on
// repeated runs, making Up idempotent.
func loadAppliedMigrations(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT filename FROM _schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]struct{})
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("scan applied migration row: %w", scanErr)
		}
		applied[name] = struct{}{}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", rowsErr)
	}
	return applied, nil
}

// applyMigration runs one migration file inside a transaction together
// with the bookkeeping insert. Either both succeed or both roll back.
func applyMigration(ctx context.Context, db *sql.DB, m migrationFile, logger *slog.Logger) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for migration %s: %w", m.name, err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && logger != nil {
				logger.Warn("migration rollback failed",
					"migration", m.name,
					"err", rbErr,
				)
			}
		}
	}()

	if _, execErr := tx.ExecContext(ctx, m.sql); execErr != nil {
		err = fmt.Errorf("apply migration %s: %w", m.name, execErr)
		return err
	}

	if _, execErr := tx.ExecContext(ctx,
		`INSERT INTO _schema_migrations (filename, applied_at) VALUES (?, ?)`,
		m.name, time.Now().UTC().UnixMicro(),
	); execErr != nil {
		err = fmt.Errorf("record migration %s: %w", m.name, execErr)
		return err
	}

	if commitErr := tx.Commit(); commitErr != nil {
		err = fmt.Errorf("commit migration %s: %w", m.name, commitErr)
		return err
	}
	return nil
}

// Up applies every embedded migration not yet recorded in
// _schema_migrations and returns nil on success. Safe to call multiple
// times; a second call with no new files is a no-op.
func Up(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if db == nil {
		return fmt.Errorf("store.Up: nil *sql.DB")
	}

	if _, err := db.ExecContext(ctx, migrationsBookkeepingDDL); err != nil {
		return fmt.Errorf("create _schema_migrations: %w", err)
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := loadAppliedMigrations(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range all {
		if _, ok := applied[m.name]; ok {
			continue
		}
		if err := applyMigration(ctx, db, m, logger); err != nil {
			return err
		}
		if logger != nil {
			logger.Info("migration applied", "migration", m.name)
		}
	}
	return nil
}
