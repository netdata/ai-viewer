package opencode

import (
	"strconv"
	"strings"
)

// This file holds the present-column SELECT builders for point/ordered loads and
// the numeric parse helpers the row scanners use. Split out of store_load.go to
// keep each file ≤400 lines. Every identifier passed to a builder comes from the
// fixed schema (never operator input); quoteIdent (store.go) defends against any
// future column that is a SQL keyword.

// presentColsSQL renders the quoted present-column list for a table schema, the
// shared prefix of every load SELECT (never SELECT *).
func presentColsSQL(s tableSchema) string {
	cols := s.Present
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	return strings.Join(quoted, ", ")
}

// selectByIDList builds "SELECT <present> FROM <t> WHERE id = ?" for a point
// load by primary key. Identifiers come from the fixed schema, never user input.
func selectByIDList(s tableSchema) string {
	return "SELECT " + presentColsSQL(s) + " FROM " + quoteIdent(s.Table) + " WHERE id = ?"
}

// selectBySessionID builds "SELECT <present> FROM <t> WHERE session_id = ?
// ORDER BY <orderBy>" for an ordered child load. orderBy is a fixed schema
// order key, never user input, and is already comma-separated/quoted.
func selectBySessionID(s tableSchema, orderBy string) string {
	return "SELECT " + presentColsSQL(s) + " FROM " + quoteIdent(s.Table) +
		" WHERE " + quoteIdent("session_id") + " = ? ORDER BY " + orderBy
}

// messageOrderBy returns the message ordering key: "time_created", "id" when the
// schema has time_created (every observed schema does), else "id" alone. The
// mapper requires assistant messages in (time_created, id) order
// (adapter-opencode.md §"Turn synthesis").
func messageOrderBy(s tableSchema) string {
	if s.has("time_created") {
		return quoteIdent("time_created") + ", " + quoteIdent("id")
	}
	return quoteIdent("id")
}

// parseInt64 parses a decimal integer column value sqlite returned as text,
// returning 0 for a non-numeric value (defensive — opencode integer columns are
// always numeric, but a corrupt cell must not panic the loader).
func parseInt64(s string) int64 {
	v, _ := parseInt64Checked(s)
	return v
}

// parseInt64Checked parses a decimal integer column value, returning (0, false)
// for a non-numeric value so the caller can surface a corruption WARN
// (SOW-0005 P2.6). An empty/whitespace string is NOT corruption (it maps to 0,
// true) — a NULL never reaches here (the caller gates on Valid).
func parseInt64Checked(s string) (int64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseFloat64 parses a real column value sqlite returned as text, returning 0
// for a non-numeric value.
func parseFloat64(s string) float64 {
	v, _ := parseFloat64Checked(s)
	return v
}

// parseFloat64Checked parses a real column value, returning (0, false) for a
// non-numeric value so the caller can surface a corruption WARN (SOW-0005 P2.6).
func parseFloat64Checked(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, true
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
