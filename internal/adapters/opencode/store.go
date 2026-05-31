package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// This file lays the schema-introspection foundation the opencode adapter
// needs before any delta query can run. opencode's schema evolves across ~30
// historic migrations, so older databases lack columns a newer one has
// (cost/tokens_* on session were added by 20260510033149_session_usage;
// path/agent/model later still). The adapter therefore NEVER issues
// "SELECT *": it probes each table with PRAGMA table_info, intersects the
// live columns with the set it knows how to read, and builds a SELECT list
// naming only columns that actually exist (adapter-opencode.md §"Read
// Strategy", §"Edge Cases" #1; SOW-0005 AC#5).
//
// The delta-query bodies and the poll loop are Chunk C. This file delivers
// only: the per-table wanted-column lists, the table_info probe, the
// dynamic-SELECT builder, and the missing-column detection a later chunk
// wires to a one-shot INF LogEntry.

// wantedColumns is the ordered set of columns the adapter reads from each
// tracked table, oldest-schema column first. Every name here is a column the
// mapper (later chunks) consumes; the dynamic SELECT names the INTERSECTION
// of this list with the columns PRAGMA table_info reports, so a column absent
// on an older schema is simply omitted and the mapper sees a zero value. The
// lists are verified against the live database schema (read-only probe,
// 2026-05-30) and the migration journal in adapter-opencode.md §"session" /
// §"message" / §"part" / §"session_message".
var wantedColumns = map[string][]string{
	"session": {
		"id", "project_id", "parent_id", "slug", "directory", "title",
		"version", "agent", "model", "cost", "tokens_input", "tokens_output",
		"tokens_reasoning", "tokens_cache_read", "tokens_cache_write",
		"time_created", "time_updated", "time_archived", "time_compacting",
	},
	"message": {
		"id", "session_id", "time_created", "time_updated", "data",
	},
	"part": {
		"id", "message_id", "session_id", "time_created", "time_updated", "data",
	},
	"session_message": {
		"id", "session_id", "type", "time_created", "time_updated", "data",
	},
}

// requiredColumns is the subset of wantedColumns whose absence makes a table
// unreadable: the primary key, the watermark column, and the payload/body.
// Their loss is not column drift the adapter can paper over with a zero value
// — it means the schema is incompatible and the caller must surface a fatal
// error (later chunks) rather than silently emit empty rows. The id and
// time_updated columns underpin the cursor; data carries the message/part
// body; session_message additionally needs type to discriminate.
var requiredColumns = map[string][]string{
	"session":         {"id", "time_created", "time_updated"},
	"message":         {"id", "session_id", "time_updated", "data"},
	"part":            {"id", "message_id", "session_id", "time_updated", "data"},
	"session_message": {"id", "session_id", "type", "time_updated", "data"},
}

// tableSchema is the result of introspecting one table: the columns the
// adapter wants AND found (Present, in wantedColumns order), the columns it
// wants but did NOT find (Missing, sorted), and the raw set of live column
// names for diagnostics. A later chunk turns Missing into one INF LogEntry
// per (table, column) on first occurrence; this chunk only computes it.
type tableSchema struct {
	// Table is the table name this schema describes.
	Table string
	// Present lists the wanted columns that exist in the live table, in
	// wantedColumns order. This is exactly the dynamic SELECT list.
	Present []string
	// Missing lists the wanted columns absent from the live table, sorted.
	// Empty on an up-to-date schema.
	Missing []string
	// live is the set of column names PRAGMA table_info reported, for
	// membership checks and diagnostics.
	live map[string]struct{}
}

// has reports whether the live table has the named column.
func (s tableSchema) has(col string) bool {
	_, ok := s.live[col]
	return ok
}

// missingRequired returns the required columns (PK / watermark / body) absent
// from the live table. A non-empty result means the table is unreadable and
// the caller must fail rather than emit misleading zero-valued rows. Empty on
// any schema new enough to read.
func (s tableSchema) missingRequired() []string {
	req := requiredColumns[s.Table]
	var out []string
	for _, c := range req {
		if !s.has(c) {
			out = append(out, c)
		}
	}
	return out
}

// introspectTable runs PRAGMA table_info(<table>) on a read-only connection
// and computes the tableSchema: which wanted columns are present, which are
// missing, and the raw live column set. The table name comes from the fixed
// trackedTables/wantedColumns sets (never operator input), so the
// non-parameterisable PRAGMA argument is safe to interpolate.
//
// PRAGMA table_info returns rows (cid, name, type, notnull, dflt_value, pk).
// Only name is needed here. An unknown table (PRAGMA returns no rows) yields a
// tableSchema whose Present is empty and Missing is the full wanted list — the
// caller's missingRequired check then reports the table unreadable.
func introspectTable(ctx context.Context, db *sql.DB, table string) (tableSchema, error) {
	wanted, ok := wantedColumns[table]
	if !ok {
		return tableSchema{}, fmt.Errorf("opencode: introspect unknown table %q", table)
	}

	// table is from the fixed wantedColumns map, not operator input. PRAGMA
	// arguments cannot be bound as query parameters, so it is interpolated;
	// quoting it as an identifier defends against any future drift in the
	// source of the name.
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdent(table)+`)`)
	if err != nil {
		return tableSchema{}, fmt.Errorf("opencode: table_info(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	live := map[string]struct{}{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return tableSchema{}, fmt.Errorf("opencode: scan table_info(%s): %w", table, err)
		}
		live[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return tableSchema{}, fmt.Errorf("opencode: iterate table_info(%s): %w", table, err)
	}

	s := tableSchema{Table: table, live: live}
	for _, col := range wanted {
		if _, found := live[col]; found {
			s.Present = append(s.Present, col)
		} else {
			s.Missing = append(s.Missing, col)
		}
	}
	sort.Strings(s.Missing)
	return s, nil
}

// schemaSet is the introspection result for every tracked table, keyed by
// table name. A later chunk holds this on the adapter and consults it before
// each delta query; this chunk only builds it.
type schemaSet map[string]tableSchema

// introspectAll probes every tracked table and returns the schemaSet. It
// fails fast if any table is missing a required column (PK / watermark /
// body), because such a table cannot be read safely — the caller surfaces the
// error rather than emitting empty rows. Column drift that is NOT required
// (e.g. an old session row missing cost/tokens_*) is recorded in each
// tableSchema's Missing and tolerated.
func introspectAll(ctx context.Context, db *sql.DB) (schemaSet, error) {
	out := make(schemaSet, len(trackedTables))
	for _, table := range trackedTables {
		s, err := introspectTable(ctx, db, table)
		if err != nil {
			return nil, err
		}
		if missing := s.missingRequired(); len(missing) > 0 {
			return nil, fmt.Errorf("opencode: table %q missing required column(s) %v (schema too old or incompatible)", table, missing)
		}
		out[table] = s
	}
	return out, nil
}

// buildSelect renders a delta-read SELECT for the table naming ONLY the
// columns present in the live schema (never SELECT *), ordered by the
// composite watermark key (time_updated, id) the cursor advances along, with
// the standard 1000-row page LIMIT that keeps each read transaction short
// (adapter-opencode.md §"Cursor"). The WHERE clause is intentionally LEFT to
// the caller via two bind placeholders so the delta-query layer (Chunk C) can
// supply the watermark predicate; this chunk emits the column list, ordering,
// and paging skeleton so the dynamic-SELECT behaviour (AC#5) is testable now.
//
// The returned statement has two positional parameters: max_time_updated and
// max_id, in that order, matching:
//
//	WHERE time_updated > ?1 OR (time_updated = ?1 AND id > ?2)
//
// quoteIdent guards every identifier; the table name and column names come
// from the fixed wantedColumns map and the live-schema intersection, never
// operator input.
func (s tableSchema) buildSelect() string {
	cols := s.Present
	if len(cols) == 0 {
		// Defensive: introspectAll rejects a table with no readable columns,
		// so this path is unreachable in production. Return a syntactically
		// valid no-row query rather than an empty string a caller might run.
		return "SELECT 1 WHERE 0"
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(quoted, ", "))
	b.WriteString(" FROM ")
	b.WriteString(quoteIdent(s.Table))
	b.WriteString(" WHERE time_updated > ? OR (time_updated = ? AND id > ?)")
	b.WriteString(" ORDER BY time_updated, id LIMIT 1000")
	return b.String()
}

// NOTE: there is intentionally NO id-only delta SELECT. time_updated is a
// REQUIRED column for every tracked table (requiredColumns), enforced by
// introspectAll which fails fast when it is absent. Every schema that reaches a
// delta query therefore has time_updated, so the composite-key buildSelect above
// is the ONLY delta SELECT — the old pre-Timestamps-mixin id-only fallback
// (buildSelectByID) was unreachable dead code and was removed (SOW-0005 P3.1).

// quoteIdent wraps a SQL identifier in double quotes, escaping any embedded
// double quote per SQLite identifier rules. All identifiers passed here are
// from the adapter's fixed column/table sets, never operator input; the
// quoting is defence-in-depth so a future column added to wantedColumns that
// happens to be a SQL keyword still parses.
func quoteIdent(id string) string {
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}
