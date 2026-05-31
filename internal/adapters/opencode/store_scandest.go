package opencode

import (
	"database/sql"
	"fmt"
)

// This file holds the DYNAMIC SCAN-DESTINATION DECODER (columnIndex + scanDest):
// the single dynamic Scan path every tracked-table row scan uses, regardless of
// which OPTIONAL columns the live schema omits. Split out of store_load.go to keep
// each file ≤400 lines (SOW-0005 round-7 P2-3 added the ownerOrWarn accessor). The
// tree-load scanners (store_load.go) and the delta-row scanners (store_scan.go)
// both consume these helpers; the SELECT builders / numeric parse helpers live in
// store_load_sql.go.

// columnIndex maps a present-column name to its position in a dynamic SELECT, so
// a per-table scan reads each typed field from the right scan destination
// regardless of which optional columns the live schema omits. Built once per
// table from tableSchema.Present.
type columnIndex map[string]int

// newColumnIndex builds the present-column → position map for a table schema.
func newColumnIndex(s tableSchema) columnIndex {
	idx := make(columnIndex, len(s.Present))
	for i, c := range s.Present {
		idx[c] = i
	}
	return idx
}

// scanDest allocates one sql.NullString/NullInt64-backed destination slice
// sized to the present columns, plus a typed accessor closure set. The scan
// reads every column as a nullable holder, then the per-row decoder copies the
// present ones into the typed struct via the columnIndex. This keeps a single
// dynamic Scan path for every table shape.
//
// onWarn (optional) surfaces a CORRUPT numeric cell — a non-NULL value that is
// not parseable as the column's numeric type — with table/column context, so a
// corrupt time/cost/token cell degrades to 0 WITHOUT being silently swallowed
// (SOW-0005 P2.6). The tree-load scanners (loadSession/loadMessages/loadParts —
// the content path) set it; the delta-row scanners (which only derive the
// affected-session set from id/session_id) leave it nil.
type scanDest struct {
	holders []sql.NullString
	ptrs    []any
	table   string
	onWarn  func(error)
}

// newScanDest sizes the holders/pointers to n columns.
func newScanDest(n int) *scanDest {
	d := &scanDest{holders: make([]sql.NullString, n), ptrs: make([]any, n)}
	for i := range d.holders {
		d.ptrs[i] = &d.holders[i]
	}
	return d
}

// withWarn attaches a table label + onWarn callback so i64/f64 can surface a
// corrupt numeric cell. Returns the receiver for chaining at the scan site.
func (d *scanDest) withWarn(table string, onWarn func(error)) *scanDest {
	d.table = table
	d.onWarn = onWarn
	return d
}

// str returns the present column's string value, or "" when the column is absent
// from the live schema (old-schema drift) or SQL NULL.
func (d *scanDest) str(idx columnIndex, col string) string {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		return d.holders[i].String
	}
	return ""
}

// i64 returns the present column's int64 value, or 0 when absent/NULL. opencode
// integer columns scan cleanly through a NullString (sqlite returns the decimal
// text); strconv keeps the path uniform with the string columns. A non-NULL
// value that fails to parse is CORRUPT — it degrades to 0 and is surfaced via the
// attached onWarn (SOW-0005 P2.6) rather than silently swallowed.
func (d *scanDest) i64(idx columnIndex, col string) int64 {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		v, ok := parseInt64Checked(d.holders[i].String)
		if !ok {
			d.warnCorrupt(col, d.holders[i].String)
		}
		return v
	}
	return 0
}

// f64 returns the present column's float64 value, or 0 when absent/NULL. A
// non-NULL value that fails to parse is surfaced via onWarn (SOW-0005 P2.6).
func (d *scanDest) f64(idx columnIndex, col string) float64 {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		v, ok := parseFloat64Checked(d.holders[i].String)
		if !ok {
			d.warnCorrupt(col, d.holders[i].String)
		}
		return v
	}
	return 0
}

// warnCorrupt surfaces a corrupt numeric cell via onWarn when one is attached.
// The raw value is intentionally NOT logged (it could be sensitive); only the
// table/column and a fixed message are reported.
func (d *scanDest) warnCorrupt(col, _ string) {
	if d.onWarn != nil {
		d.onWarn(fmt.Errorf("opencode: corrupt numeric cell (table=%s column=%s); using 0", d.table, col))
	}
}

// i64Required reads a REQUIRED int64 column (a cursor-watermark column —
// time_updated) and returns an ERROR rather than coercing to 0 when the cell is
// NULL/absent or present-but-unparseable (SOW-0005 round-4 P2-1). The delta
// scanners feed this into the watermark key, so a corrupt value coerced to 0 would
// persist a POISONED cursor (the watermark could regress to 0). Erroring the row
// instead aborts the page so the cursor stays at the last good watermark — a
// corrupt required cell never advances the durable resume state. The raw value is
// NOT included in the error (it could be sensitive); only the table/column.
func (d *scanDest) i64Required(idx columnIndex, col string) (int64, error) {
	i, ok := idx[col]
	if !ok || !d.holders[i].Valid {
		return 0, fmt.Errorf("opencode: required column %q absent/NULL (table=%s); refusing to advance cursor on a missing watermark", col, d.table)
	}
	v, parsed := parseInt64Checked(d.holders[i].String)
	if !parsed {
		return 0, fmt.Errorf("opencode: corrupt required numeric cell (table=%s column=%s); refusing to advance cursor on a poisoned watermark", d.table, col)
	}
	return v, nil
}

// strRequired reads a REQUIRED string column (the cursor-watermark id) and returns
// an ERROR when the cell is NULL/absent or empty (SOW-0005 round-4 P2-1). An empty
// id cannot form a valid watermark tie-break, so it must not advance the cursor.
func (d *scanDest) strRequired(idx columnIndex, col string) (string, error) {
	i, ok := idx[col]
	if !ok || !d.holders[i].Valid || d.holders[i].String == "" {
		return "", fmt.Errorf("opencode: required column %q absent/NULL/empty (table=%s); refusing to advance cursor on a missing watermark id", col, d.table)
	}
	return d.holders[i].String, nil
}

// ownerOrWarn reads a REQUIRED OWNERSHIP/id column on the FULL-TREE load path
// (message.id / message.session_id / part.message_id / part.session_id) and
// returns (value, true) when present and non-empty. When the cell is absent/NULL/
// empty it surfaces ONE structured WARN via the attached onWarn (the same table/
// column-context, no-raw-value discipline as warnCorrupt — buffered into the
// warnSink and flushed AFTER the read tx closes, P2-1) and returns ("", false) so
// the caller SKIPS the row rather than attaching it to the out[""] partition where
// it would be silently dropped (SOW-0005 round-7 P2-3). It mirrors the delta-path
// requiredOwner semantics (store_scan.go), but WARN-and-skip rather than
// error-and-abort: the full-tree reload is a content re-emit, not a cursor advance,
// so one corrupt historical row is surfaced and dropped — not fatal to the whole
// session reload (which would strand every other part). onWarn may be nil on the
// pure no-DB test entrypoints, in which case the row is skipped silently.
func (d *scanDest) ownerOrWarn(idx columnIndex, col string) (string, bool) {
	i, ok := idx[col]
	if !ok || !d.holders[i].Valid || d.holders[i].String == "" {
		if d.onWarn != nil {
			d.onWarn(fmt.Errorf("opencode: required ownership column %q absent/NULL/empty (table=%s); skipping row (would otherwise be dropped under an empty owner key)", col, d.table))
		}
		return "", false
	}
	return d.holders[i].String, true
}

// bytes returns the present column's raw value as bytes, or nil when absent/NULL
// (used for the JSON data/model columns the mapper decodes).
func (d *scanDest) bytes(idx columnIndex, col string) []byte {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		return []byte(d.holders[i].String)
	}
	return nil
}
