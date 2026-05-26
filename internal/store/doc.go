// Package store opens the ai-viewer SQLite database, runs schema
// migrations, and exposes the underlying *sql.DB for downstream
// ingester and presenter packages.
//
// The schema (v1) is defined in .agents/sow/specs/data-model.md and
// encoded in internal/store/migrations/0001_initial.sql. Migrations are
// embedded with go:embed, sorted by filename, and applied inside
// transactions. A separate _schema_migrations table tracks which
// migrations have been applied, making OpenWriter() idempotent across
// restarts.
//
// Writer vs reader. The ingester calls OpenWriter (read+write, runs
// migrations); the server calls OpenReader (mode=ro, query_only(true),
// no migrations). Open is a backward-compatible alias for OpenWriter.
// All PRAGMAs are applied via modernc.org/sqlite's _pragma DSN
// parameters so they propagate to every connection in the database/sql
// pool — issuing PRAGMA statements after sql.Open only affects whichever
// connection happened to serve the ExecContext call.
//
// The store deliberately exposes no business-logic methods. The
// ingester (single writer) and presenter (read-only) build their own
// query helpers on top of Store.DB(). This keeps store dependency-free
// of canonical/adapter/ingest concerns.
//
// Single-writer invariant. Migrations are run only from OpenWriter, and
// only one process writes to the database at a time (the ingester). The
// server uses OpenReader and never runs migrations. Multi-writer
// scenarios are out of scope; if introduced, a migration lock pattern
// (SQLite advisory lock or a sentinel row in _schema_migrations) would
// be required.
//
// PRAGMA choices:
//   - journal_mode=WAL — concurrent reads alongside the ingester writer
//     (writer DSN only; SQLite refuses WAL on :memory: and on a
//     read-only connection).
//   - synchronous=NORMAL — durable enough for crash recovery, fast for
//     ingest (writer DSN only).
//   - busy_timeout=5000 — five seconds to wait on locks. The ingester
//     batches in <500ms transactions, so contention should be rare.
//   - foreign_keys=ON — sessions/turns/ops references enforced (per
//     connection in SQLite, hence applied via _pragma so every pooled
//     conn picks it up).
//   - query_only=true — reader DSN only; defence in depth against
//     accidental writes from the server.
package store
