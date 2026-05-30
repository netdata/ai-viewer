// Package opencode implements the canonical.Adapter for the opencode CLI
// session store. It is the only adapter that does not read filesystem
// snapshots: opencode keeps every session, message, and part in a single
// live, multi-GB SQLite database that opencode itself writes to
// concurrently.
//
// # Source layout
//
// opencode stores everything in one SQLite database with WAL companions:
//
//	~/.local/share/opencode/opencode.db          main database
//	~/.local/share/opencode/opencode.db-wal      write-ahead log
//	~/.local/share/opencode/opencode.db-shm      shared-memory index
//
// The schema is Drizzle-managed and evolves across ~30 historic migrations.
// The adapter reads four tables — session, message, part, session_message —
// and treats the schema as evolving: it introspects each table with
// PRAGMA table_info at startup and names only columns that actually exist
// (never SELECT *), so an older-schema database missing a newer column is
// tolerated rather than failing the query.
//
// # Read-only delta-query model
//
// Because the source is a live database with a concurrent writer, this
// adapter does NOT stream JSONL like the four file adapters
// (aiagent_v2/v3, claude_code, codex). Instead it opens the database
// strictly read-only (see conn.go) and runs SQL delta-queries gated by a
// watermark cursor (see cursor.go). The read-safety contract is the
// dominant design constraint: any accidental write would corrupt the
// operator's primary coding tool, so the connection helper layers
// OS-level mode=ro with the query_only(true) PRAGMA and never issues a
// write-path statement.
//
// # Watermark cursor
//
// opencode IDs are time-prefixed Sonyflake strings (ses_/msg_/prt_/evt_),
// lexicographically sortable as time and PK-indexed. The cursor records,
// per table, the highest observed (MaxID, MaxTimeUpdatedMs). MaxID is the
// primary watermark (PK b-tree, cheap WHERE id > :last); MaxTimeUpdatedMs
// is an unindexed full-scan fallback that catches in-place mutations and
// is gated to run only after WAL activity (later chunks). There are no
// byte offsets — the file-adapter cursor model does not apply here.
//
// This file (doc.go) and the package siblings deliver only the read-only
// foundation: the connection helper, the watermark cursor, the typed row
// and discriminated-data structs, and the schema-introspection layer. The
// row→event mapper, the delta-query bodies, the poll-loop tailer, the
// payload-URI builder, and the adapter/registry wiring arrive in later
// chunks.
//
// See .agents/sow/specs/adapter-opencode.md for the full format reference
// (SQLite schema, read strategy, watch strategy, cursor design, canonical
// mapping, edge cases) and .agents/sow/specs/adapter-contract.md for the
// universal adapter rules.
package opencode
