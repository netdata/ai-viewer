package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestSchema_ColumnContract asserts that the v1 schema produced by the
// embedded migration matches the contract documented in
// .agents/sow/specs/data-model.md exactly: columns (name, type,
// nullability, default, PK position), indexes (name, columns, partial
// predicate, uniqueness), and foreign keys (from-col, to-table, to-col).
//
// The expected lists below are the durable contract. If
// data-model.md changes — for example adding a new column to ops —
// both this contract list and the migration SQL update in the same
// commit. Without this test, silent drift between spec and schema
// would only surface as runtime errors later.
func TestSchema_ColumnContract(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	for _, tc := range expectedSchema() {
		t.Run(tc.table, func(t *testing.T) {
			t.Parallel()

			gotCols := readColumns(t, db, ctx, tc.table)
			if diff := cmp.Diff(tc.cols, gotCols); diff != "" {
				t.Errorf("columns for %s mismatch (-want +got):\n%s", tc.table, diff)
			}

			gotIdx := readIndexes(t, db, ctx, tc.table)
			if diff := cmp.Diff(tc.indexes, gotIdx); diff != "" {
				t.Errorf("indexes for %s mismatch (-want +got):\n%s", tc.table, diff)
			}

			gotFKs := readForeignKeys(t, db, ctx, tc.table)
			if diff := cmp.Diff(tc.fks, gotFKs); diff != "" {
				t.Errorf("foreign keys for %s mismatch (-want +got):\n%s", tc.table, diff)
			}
		})
	}
}

// column captures the subset of PRAGMA table_info we care about.
type column struct {
	Name    string
	Type    string
	NotNull bool
	DfltVal string // "" means no default
	PKOrder int    // 0 = not part of PK, 1+ = position in the PK
}

// index captures the subset of PRAGMA index_list + index_info we care
// about. Columns is the ordered column list; Unique reflects the index
// uniqueness flag. Partial indexes are surfaced separately as Partial.
type index struct {
	Name    string
	Cols    []string
	Unique  bool
	Partial bool
}

// fkRef captures one row of PRAGMA foreign_key_list for a table.
type fkRef struct {
	From  string
	Table string
	To    string
}

type tableContract struct {
	table   string
	cols    []column
	indexes []index
	fks     []fkRef
}

// readColumns runs PRAGMA table_info and returns the column slice in
// the schema-declared order.
func readColumns(t *testing.T, db *sql.DB, ctx context.Context, table string) []column {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []column
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pkOrder int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pkOrder); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, column{
			Name:    name,
			Type:    typ,
			NotNull: notnull != 0,
			DfltVal: dflt.String,
			PKOrder: pkOrder,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

// readIndexes returns the application-relevant indexes for a table.
// Auto-created sqlite_autoindex_* entries (from PRIMARY KEY / UNIQUE
// constraints) are skipped here so the application-index list stays
// focused on explicit CREATE INDEX statements. Composite UNIQUE
// constraints (which materialise as sqlite_autoindex_* with origin
// 'u') are covered separately by TestSchema_CompositeUniqueAutoindexes
// so the contract still pins them.
func readIndexes(t *testing.T, db *sql.DB, ctx context.Context, table string) []index {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	type idxRow struct {
		name    string
		unique  bool
		partial bool
	}
	var raw []idxRow
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		// Skip auto-indexes produced by PRIMARY KEY / UNIQUE constraints.
		if origin != "c" {
			continue
		}
		raw = append(raw, idxRow{name: name, unique: unique != 0, partial: partial != 0})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index_list(%s): %v", table, err)
	}

	if len(raw) == 0 {
		return nil
	}
	out := make([]index, 0, len(raw))
	for _, ir := range raw {
		out = append(out, index{
			Name:    ir.name,
			Cols:    readIndexCols(t, db, ctx, ir.name),
			Unique:  ir.unique,
			Partial: ir.partial,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func readIndexCols(t *testing.T, db *sql.DB, ctx context.Context, indexName string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%q)", indexName))
	if err != nil {
		t.Fatalf("PRAGMA index_info(%s): %v", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
		)
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			t.Fatalf("scan index_info(%s): %v", indexName, err)
		}
		// Expression-only index entries report NULL; we still record an
		// empty string so the slice length matches the index columns.
		cols = append(cols, name.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index_info(%s): %v", indexName, err)
	}
	return cols
}

func readForeignKeys(t *testing.T, db *sql.DB, ctx context.Context, table string) []fkRef {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
	if err != nil {
		t.Fatalf("PRAGMA foreign_key_list(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []fkRef
	for rows.Next() {
		var (
			id, seq  int
			refTable string
			from, to string
			onUpdate string
			onDelete string
			match    string
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list(%s): %v", table, err)
		}
		out = append(out, fkRef{From: from, Table: refTable, To: to})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_list(%s): %v", table, err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].To < out[j].To
	})
	return out
}

// expectedSchema returns the contract list. Order of the cols slice
// must match the column declaration order in the migration SQL.
func expectedSchema() []tableContract {
	return []tableContract{
		{
			table: "sources",
			cols: []column{
				{Name: "id", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "format", Type: "TEXT", NotNull: true},
				{Name: "location", Type: "TEXT", NotNull: true},
				{Name: "cursor", Type: "TEXT"},
				{Name: "last_seen_at", Type: "INTEGER"},
				{Name: "enabled", Type: "INTEGER", NotNull: true, DfltVal: "1"},
				{Name: "parse_errors", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "created_at", Type: "INTEGER", NotNull: true},
				// Appended by migration 0007 (ALTER TABLE ADD COLUMN appends
				// after created_at). Per-source FTS5 log-indexing opt-out flag,
				// default 1 (index logs). data-model.md §Full-text search.
				{Name: "fts5_index_logs", Type: "INTEGER", NotNull: true, DfltVal: "1"},
				// Appended by migration 0008 (ALTER TABLE ADD COLUMN appends
				// after fts5_index_logs). General adapter-owned per-source
				// metadata blob; nullable, no default (NULL = adapter did not
				// populate it). data-model.md §sources (SOW-0024).
				{Name: "meta_json", Type: "TEXT"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "sessions",
			cols: []column{
				{Name: "id", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "source_id", Type: "TEXT", NotNull: true},
				{Name: "native_id", Type: "TEXT", NotNull: true},
				{Name: "parent_session_id", Type: "TEXT"},
				{Name: "root_session_id", Type: "TEXT", NotNull: true},
				{Name: "kind", Type: "TEXT", NotNull: true},
				{Name: "agent_name", Type: "TEXT"},
				{Name: "model", Type: "TEXT"},
				{Name: "provider", Type: "TEXT"},
				{Name: "provider_alias", Type: "TEXT"},
				{Name: "cwd", Type: "TEXT"},
				{Name: "call_path", Type: "TEXT"},
				{Name: "status", Type: "TEXT", NotNull: true},
				{Name: "error_class", Type: "TEXT"},
				{Name: "error_message", Type: "TEXT"},
				{Name: "start_ts", Type: "INTEGER", NotNull: true},
				{Name: "end_ts", Type: "INTEGER"},
				{Name: "last_activity_ts", Type: "INTEGER", NotNull: true},
				{Name: "tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_read", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_write", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
				{Name: "turn_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "op_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "failure_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "extras_json", Type: "TEXT"},
				{Name: "first_user_message_hash", Type: "TEXT"},
				{Name: "duration_us", Type: "INTEGER"},
			},
			indexes: []index{
				{Name: "idx_sessions_activity", Cols: []string{"last_activity_ts"}},
				{Name: "idx_sessions_agent", Cols: []string{"agent_name"}},
				{Name: "idx_sessions_cost", Cols: []string{"cost_usd", "id"}},
				{Name: "idx_sessions_cwd", Cols: []string{"cwd"}},
				{Name: "idx_sessions_duration", Cols: []string{"duration_us", "id"}},
				{Name: "idx_sessions_first_user_message_hash", Cols: []string{"first_user_message_hash"}, Partial: true},
				{Name: "idx_sessions_model", Cols: []string{"model"}},
				{Name: "idx_sessions_op_count", Cols: []string{"op_count", "id"}},
				{Name: "idx_sessions_parent", Cols: []string{"parent_session_id"}},
				{Name: "idx_sessions_provider", Cols: []string{"provider"}},
				{Name: "idx_sessions_root_start", Cols: []string{"root_session_id", "start_ts"}},
				{Name: "idx_sessions_source_id", Cols: []string{"source_id", "id"}},
				{Name: "idx_sessions_start", Cols: []string{"start_ts"}},
				{Name: "idx_sessions_status", Cols: []string{"status"}},
				{Name: "idx_sessions_tokens", Cols: []string{"", "id"}},
			},
			fks: []fkRef{
				{From: "parent_session_id", Table: "sessions", To: "id"},
				{From: "root_session_id", Table: "sessions", To: "id"},
				{From: "source_id", Table: "sources", To: "id"},
			},
		},
		{
			table: "turns",
			cols: []column{
				{Name: "id", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "session_id", Type: "TEXT", NotNull: true},
				{Name: "seq", Type: "INTEGER", NotNull: true},
				{Name: "start_ts", Type: "INTEGER", NotNull: true},
				{Name: "end_ts", Type: "INTEGER"},
				{Name: "status", Type: "TEXT", NotNull: true},
				{Name: "error_class", Type: "TEXT"},
				{Name: "tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_read", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_write", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
				{Name: "op_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "extras_json", Type: "TEXT"},
			},
			indexes: []index{
				{Name: "idx_turns_session_seq", Cols: []string{"session_id", "seq"}},
				{Name: "idx_turns_start", Cols: []string{"start_ts"}},
			},
			fks: []fkRef{
				{From: "session_id", Table: "sessions", To: "id"},
			},
		},
		{
			table: "ops",
			cols: []column{
				{Name: "id", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "turn_id", Type: "TEXT", NotNull: true},
				{Name: "session_id", Type: "TEXT", NotNull: true},
				{Name: "parent_op_id", Type: "TEXT"},
				{Name: "seq", Type: "INTEGER", NotNull: true},
				{Name: "kind", Type: "TEXT", NotNull: true},
				{Name: "name", Type: "TEXT", NotNull: true},
				{Name: "tool_namespace", Type: "TEXT"},
				{Name: "model", Type: "TEXT"},
				{Name: "provider", Type: "TEXT"},
				{Name: "provider_alias", Type: "TEXT"},
				{Name: "reasoning_kind", Type: "TEXT"},
				{Name: "start_ts", Type: "INTEGER", NotNull: true},
				{Name: "end_ts", Type: "INTEGER"},
				{Name: "duration_us", Type: "INTEGER"},
				{Name: "status", Type: "TEXT", NotNull: true},
				{Name: "error_class", Type: "TEXT"},
				{Name: "error_message", Type: "TEXT"},
				{Name: "tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_read", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "tokens_cache_write", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
				{Name: "bytes_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "bytes_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "chars_in", Type: "INTEGER"},
				{Name: "chars_out", Type: "INTEGER"},
				{Name: "ctx_used", Type: "INTEGER"},
				{Name: "ctx_max", Type: "INTEGER"},
				{Name: "child_session_id", Type: "TEXT"},
				{Name: "extras_json", Type: "TEXT"},
			},
			indexes: []index{
				{Name: "idx_ops_compaction", Cols: []string{"session_id", "start_ts"}, Partial: true},
				{Name: "idx_ops_kind_name", Cols: []string{"kind", "name"}},
				{Name: "idx_ops_model", Cols: []string{"model"}, Partial: true},
				{Name: "idx_ops_parent", Cols: []string{"parent_op_id"}},
				{Name: "idx_ops_provider", Cols: []string{"provider"}, Partial: true},
				{Name: "idx_ops_session", Cols: []string{"session_id"}},
				{Name: "idx_ops_session_end", Cols: []string{"session_id", "end_ts"}},
				{Name: "idx_ops_session_start", Cols: []string{"session_id", "start_ts"}},
				{Name: "idx_ops_session_status", Cols: []string{"session_id", "status"}},
				{Name: "idx_ops_start", Cols: []string{"start_ts"}},
				{Name: "idx_ops_status", Cols: []string{"status"}},
				{Name: "idx_ops_tool", Cols: []string{"tool_namespace", "name"}, Partial: true},
				{Name: "idx_ops_turn_seq", Cols: []string{"turn_id", "seq"}},
			},
			fks: []fkRef{
				{From: "child_session_id", Table: "sessions", To: "id"},
				{From: "parent_op_id", Table: "ops", To: "id"},
				{From: "session_id", Table: "sessions", To: "id"},
				{From: "turn_id", Table: "turns", To: "id"},
			},
		},
		{
			table: "payload_refs",
			cols: []column{
				{Name: "id", Type: "INTEGER", PKOrder: 1},
				{Name: "op_id", Type: "TEXT", NotNull: true},
				{Name: "kind", Type: "TEXT", NotNull: true},
				{Name: "format", Type: "TEXT", NotNull: true},
				{Name: "compression", Type: "TEXT"},
				{Name: "location_uri", Type: "TEXT", NotNull: true},
				{Name: "original_bytes", Type: "INTEGER"},
				{Name: "stored_bytes", Type: "INTEGER"},
				{Name: "sha256", Type: "TEXT"},
			},
			indexes: []index{
				// Migration 0003 natural-identity UNIQUE index for
				// idempotent re-scans (SOW-0015).
				{Name: "idx_payload_refs_identity", Cols: []string{"op_id", "kind", "location_uri"}, Unique: true},
				{Name: "idx_payload_refs_op", Cols: []string{"op_id"}},
			},
			fks: []fkRef{
				{From: "op_id", Table: "ops", To: "id"},
			},
		},
		{
			table: "log_entries",
			cols: []column{
				{Name: "id", Type: "INTEGER", PKOrder: 1},
				{Name: "session_id", Type: "TEXT"},
				{Name: "source_id", Type: "TEXT"},
				{Name: "turn_id", Type: "TEXT"},
				{Name: "op_id", Type: "TEXT"},
				{Name: "ts", Type: "INTEGER", NotNull: true},
				{Name: "severity", Type: "TEXT", NotNull: true},
				{Name: "source", Type: "TEXT", NotNull: true},
				{Name: "message", Type: "TEXT", NotNull: true},
				{Name: "extras_json", Type: "TEXT"},
			},
			indexes: []index{
				// Migration 0003 natural-identity UNIQUE expression index
				// for idempotent re-scans (SOW-0015). The four leading
				// COALESCE(...) expressions (session_id, source_id, op_id,
				// turn_id) and the trailing COALESCE(extras_json, '')
				// report NULL column names from PRAGMA index_info, surfaced
				// here as empty strings. The key covers every persisted
				// content column so a duplicate is a byte-identical row.
				{Name: "idx_log_entries_identity", Cols: []string{"", "", "", "", "ts", "severity", "source", "message", ""}, Unique: true},
				{Name: "idx_log_session", Cols: []string{"session_id"}},
				{Name: "idx_log_session_ts", Cols: []string{"session_id", "ts"}},
				{Name: "idx_log_severity", Cols: []string{"severity", "ts"}, Partial: true},
				{Name: "idx_log_source_ts", Cols: []string{"source_id", "ts"}, Partial: true},
			},
			fks: []fkRef{
				{From: "op_id", Table: "ops", To: "id"},
				{From: "session_id", Table: "sessions", To: "id"},
				{From: "source_id", Table: "sources", To: "id"},
				{From: "turn_id", Table: "turns", To: "id"},
			},
		},
		{
			// notify: the ingester→serve change-log (migration 0004).
			// seq is INTEGER PRIMARY KEY AUTOINCREMENT — values are
			// strictly monotonic and never reused after prune so serve's
			// poll cursor never skips a row. No secondary index: WHERE
			// seq > ? rides the PK. No foreign keys: session_id /
			// source_id are loose references into disposable transport
			// rows that the ingester prunes, so a deleted session must
			// not block a notify insert. Source of truth:
			// .agents/sow/specs/data-model.md §notify.
			table: "notify",
			cols: []column{
				{Name: "seq", Type: "INTEGER", PKOrder: 1},
				{Name: "ts_us", Type: "INTEGER", NotNull: true},
				{Name: "kind", Type: "TEXT", NotNull: true},
				{Name: "session_id", Type: "TEXT"},
				{Name: "root_session_id", Type: "TEXT"},
				{Name: "source_id", Type: "TEXT"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "catalog_providers",
			cols: []column{
				{Name: "name", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "alias", Type: "TEXT", NotNull: true, DfltVal: "''", PKOrder: 2},
				{Name: "first_seen", Type: "INTEGER", NotNull: true},
				{Name: "last_seen", Type: "INTEGER", NotNull: true},
				{Name: "session_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "call_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "failure_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_cache_read", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_cache_write", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "catalog_models",
			cols: []column{
				{Name: "provider", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "name", Type: "TEXT", NotNull: true, PKOrder: 2},
				{Name: "first_seen", Type: "INTEGER", NotNull: true},
				{Name: "last_seen", Type: "INTEGER", NotNull: true},
				{Name: "call_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "failure_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_cache_read", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_cache_write", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
				{Name: "total_duration_us", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "ctx_max", Type: "INTEGER"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "catalog_tools",
			cols: []column{
				{Name: "namespace", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "name", Type: "TEXT", NotNull: true, PKOrder: 2},
				{Name: "first_seen", Type: "INTEGER", NotNull: true},
				{Name: "last_seen", Type: "INTEGER", NotNull: true},
				{Name: "call_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "failure_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
				{Name: "total_duration_us", Type: "INTEGER", NotNull: true, DfltVal: "0"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "catalog_agents",
			cols: []column{
				{Name: "source_format", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "name", Type: "TEXT", NotNull: true, PKOrder: 2},
				{Name: "first_seen", Type: "INTEGER", NotNull: true},
				{Name: "last_seen", Type: "INTEGER", NotNull: true},
				{Name: "session_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "turn_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "failure_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_in", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_tokens_out", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "catalog_cwds",
			cols: []column{
				{Name: "source_format", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "cwd", Type: "TEXT", NotNull: true, PKOrder: 2},
				{Name: "first_seen", Type: "INTEGER", NotNull: true},
				{Name: "last_seen", Type: "INTEGER", NotNull: true},
				{Name: "session_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "total_cost_usd", Type: "REAL", NotNull: true, DfltVal: "0.0"},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "schema_meta",
			cols: []column{
				{Name: "key", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "value", Type: "TEXT", NotNull: true},
			},
			indexes: nil,
			fks:     nil,
		},
		{
			table: "source_progress",
			cols: []column{
				{Name: "source_id", Type: "TEXT", NotNull: true, PKOrder: 1},
				{Name: "last_seq", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "last_ts_us", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "cursor", Type: "TEXT"},
				{Name: "updated_at", Type: "INTEGER", NotNull: true},
				{Name: "lifecycle_state", Type: "TEXT", NotNull: true, DfltVal: "'unknown'"},
				{Name: "lifecycle_state_at", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "scan_started_at", Type: "INTEGER"},
				{Name: "scan_completed_at", Type: "INTEGER"},
				{Name: "tail_started_at", Type: "INTEGER"},
				{Name: "tail_heartbeat_at", Type: "INTEGER"},
				{Name: "tail_failed_at", Type: "INTEGER"},
				{Name: "tail_restart_count", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "lifecycle_error", Type: "TEXT"},
				{Name: "read_model_state", Type: "TEXT", NotNull: true, DfltVal: "'unknown'"},
				{Name: "read_model_state_at", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "read_model_repair_started_at", Type: "INTEGER"},
				{Name: "read_model_repair_completed_at", Type: "INTEGER"},
				{Name: "read_model_repair_failed_at", Type: "INTEGER"},
				{Name: "read_model_repair_attempts", Type: "INTEGER", NotNull: true, DfltVal: "0"},
				{Name: "read_model_error", Type: "TEXT"},
			},
			indexes: nil,
			fks: []fkRef{
				{From: "source_id", Table: "sources", To: "id"},
			},
		},
	}
}

// TestSchema_PartialIndexPredicates pins the WHERE clauses on every
// partial index in the v1 schema. Without this, a future migration
// that drops the `WHERE kind='compaction'` predicate on
// idx_ops_compaction (or any other partial index) would still pass
// the columns/uniqueness contract while silently changing query
// plans and on-disk index size. The expected text is matched against
// a whitespace-normalised form of the raw CREATE INDEX SQL pulled
// from sqlite_master, so minor formatting nits do not fail the test
// while real semantic drift does.
func TestSchema_PartialIndexPredicates(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	want := map[string]string{
		"idx_ops_compaction": "WHERE kind='compaction'",
		"idx_ops_model":      "WHERE kind='llm'",
		"idx_ops_provider":   "WHERE kind='llm'",
		"idx_ops_tool":       "WHERE kind='tool'",
		"idx_log_severity":   "WHERE severity IN ('WRN','ERR')",
		"idx_log_source_ts":  "WHERE source_id IS NOT NULL",
	}

	for name, expected := range want {
		var sqlText sql.NullString
		err := db.QueryRowContext(context.Background(),
			`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`,
			name).Scan(&sqlText)
		if err != nil {
			t.Errorf("read sqlite_master for %s: %v", name, err)
			continue
		}
		if !sqlText.Valid {
			t.Errorf("index %s has NULL sql in sqlite_master", name)
			continue
		}
		got := normalizeSQL(sqlText.String)
		if !containsNormalized(got, expected) {
			t.Errorf("partial index %s: predicate drift; want substring %q in %q",
				name, expected, got)
		}
	}
}

// TestSchema_LogEntriesCheckConstraintShape pins the shape of the
// CHECK constraint on log_entries. The behavioural test
// (TestSchema_LogEntriesCheckConstraint) verifies a NULL-NULL row is
// rejected; this test catches the silent variant where the constraint
// text drifts to a different predicate that happens to also reject
// the test row (e.g. a typo that accepts more cases than intended).
func TestSchema_LogEntriesCheckConstraintShape(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	var sqlText sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='log_entries'`,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read sqlite_master for log_entries: %v", err)
	}
	if !sqlText.Valid {
		t.Fatal("log_entries has NULL sql in sqlite_master")
	}
	got := normalizeSQL(sqlText.String)
	want := "CHECK (session_id IS NOT NULL OR source_id IS NOT NULL)"
	if !containsNormalized(got, want) {
		t.Errorf("log_entries CHECK constraint drift; want substring %q in %q",
			want, got)
	}
}

// TestSchema_CompositeUniqueAutoindexes enumerates the
// sqlite_autoindex_* entries to verify the composite UNIQUE
// constraints documented in data-model.md are still present. The main
// TestSchema_ColumnContract subtest deliberately skips origin != 'c'
// indexes (i.e. autoindexes from PK/UNIQUE) to keep the
// application-index list clean; this test re-enables them and
// matches against expected (columns, unique) tuples so a future
// migration cannot silently drop a UNIQUE constraint.
func TestSchema_CompositeUniqueAutoindexes(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)
	ctx := context.Background()

	// Per-table expected sets of column tuples for every UNIQUE
	// autoindex (both PK-derived 'pk' and explicit-UNIQUE 'u' origins).
	// SQLite creates a 'pk' autoindex for every TEXT PRIMARY KEY (only
	// INTEGER PRIMARY KEY aliases the rowid and skips the autoindex);
	// explicit `UNIQUE (...)` clauses produce 'u' autoindexes.
	expected := map[string][][]string{
		"sources":           {{"id"}},                             // PK autoindex
		"sessions":          {{"id"}, {"source_id", "native_id"}}, // PK + UNIQUE
		"turns":             {{"id"}, {"session_id", "seq"}},      // PK + UNIQUE
		"ops":               {{"id"}, {"turn_id", "seq"}},         // PK + UNIQUE
		"catalog_providers": {{"name", "alias"}},                  // composite PK
		"catalog_models":    {{"provider", "name"}},               // composite PK
		"catalog_tools":     {{"namespace", "name"}},              // composite PK
		"catalog_agents":    {{"source_format", "name"}},          // composite PK
		"catalog_cwds":      {{"source_format", "cwd"}},           // composite PK
		"schema_meta":       {{"key"}},                            // PK autoindex
		"source_progress":   {{"source_id"}},                      // PK autoindex
	}

	for table, want := range expected {
		got := readAutoindexUniqueCols(t, db, ctx, table)
		// Sort tuples canonically so test is order-independent.
		sortColTuples(got)
		sortColTuples(want)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("composite UNIQUE drift for %s (-want +got):\n%s", table, diff)
		}
	}
}

// normalizeSQL collapses runs of whitespace to a single space and
// trims so partial-index predicate comparisons survive cosmetic
// formatting differences.
func normalizeSQL(s string) string {
	out := strings.Builder{}
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				out.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		out.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(out.String())
}

// containsNormalized returns true if want (after normalisation) is a
// substring of got (already normalised).
func containsNormalized(got, want string) bool {
	return strings.Contains(got, normalizeSQL(want))
}

// readAutoindexUniqueCols returns the ordered column list of every
// UNIQUE autoindex on the table. We deliberately include
// sqlite_autoindex_* entries (origin != 'c') because those are the
// only signal for composite UNIQUE constraints once SQLite has
// resolved the CREATE TABLE.
func readAutoindexUniqueCols(t *testing.T, db *sql.DB, ctx context.Context, table string) [][]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	type idxRow struct {
		name   string
		origin string
		unique bool
	}
	var raw []idxRow
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		raw = append(raw, idxRow{name: name, origin: origin, unique: unique != 0})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index_list(%s): %v", table, err)
	}

	var out [][]string
	for _, r := range raw {
		if !r.unique {
			continue
		}
		// origin values: 'pk' = composite-PK autoindex (single-column
		// TEXT PK does NOT produce one on rowid tables); 'u' = explicit
		// UNIQUE autoindex; 'c' = CREATE INDEX (covered by
		// readIndexes elsewhere). We capture 'pk' and 'u' here so
		// composite PKs and explicit UNIQUE constraints are both pinned.
		if r.origin != "u" && r.origin != "pk" {
			continue
		}
		out = append(out, readIndexCols(t, db, ctx, r.name))
	}
	return out
}

// sortColTuples sorts a slice of column tuples deterministically so
// test comparisons are order-independent. The first column drives the
// sort, with the full joined name as a tiebreaker.
func sortColTuples(s [][]string) {
	sort.SliceStable(s, func(i, j int) bool {
		a := strings.Join(s[i], "\x00")
		b := strings.Join(s[j], "\x00")
		return a < b
	})
}

// TestSchema_LogEntriesCheckConstraint verifies the CHECK
// (session_id IS NOT NULL OR source_id IS NOT NULL) constraint
// actually rejects rows that satisfy neither side. This protects the
// data-model contract that every log entry is owned by either a
// session or a source — without it, parse errors with neither set
// could pile up silently and the UI would have no way to navigate to
// them.
func TestSchema_LogEntriesCheckConstraint(t *testing.T) {
	t.Parallel()

	_, db := openInMemory(t)

	// Inserting a log_entries row with both session_id and source_id
	// NULL must fail.
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO log_entries (ts, severity, source, message) VALUES (?, ?, ?, ?)`,
		1_700_000_000_000_000, "ERR", "test", "no owner")
	if err == nil {
		t.Fatal("INSERT with both session_id and source_id NULL: want CHECK violation, got nil")
	}

	// Inserting with source_id set must succeed (after first creating a
	// matching source row to satisfy the foreign key).
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO sources (id, format, location, created_at) VALUES (?, ?, ?, ?)`,
		"src-1", "aiagent-v3", "/tmp/sessions", 1_700_000_000_000_000); err != nil {
		t.Fatalf("seed sources row: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO log_entries (source_id, ts, severity, source, message) VALUES (?, ?, ?, ?, ?)`,
		"src-1", 1_700_000_000_000_000, "ERR", "aiagent-v3", "parse fail"); err != nil {
		t.Fatalf("INSERT with source_id set: want success, got %v", err)
	}
}
