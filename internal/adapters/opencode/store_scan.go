package opencode

import (
	"database/sql"
	"fmt"
)

// This file holds the per-table DELTA-ROW SCANNERS (driven by scanTableDelta in
// store_query.go): one closure per tracked table that scans a delta row into its
// typed struct via the present-column index and reports the row's (id,
// time_updated) watermark key. Split out of store_load.go to keep each file ≤400
// lines (SOW-0005 round-2; the P2-B single-query part loader grew store_load.go).
// Every read uses the schema's PRESENT columns only (never SELECT *), so an old
// schema missing an optional column degrades to a zero value rather than failing.

// scanSessionRow reads one session delta row into a sessionRow via the present
// columns and reports its watermark key. Missing optional columns (old schema)
// stay zero.
func scanSessionRow(idx columnIndex, n int) (func(*sql.Rows) (rowKey, error), *sessionRow) {
	var out sessionRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan session row: %w", err)
		}
		out = sessionRow{
			ID:               d.str(idx, "id"),
			ProjectID:        d.str(idx, "project_id"),
			ParentID:         d.str(idx, "parent_id"),
			Slug:             d.str(idx, "slug"),
			Directory:        d.str(idx, "directory"),
			Title:            d.str(idx, "title"),
			Version:          d.str(idx, "version"),
			Agent:            d.str(idx, "agent"),
			Model:            d.bytes(idx, "model"),
			Cost:             d.f64(idx, "cost"),
			TokensInput:      d.i64(idx, "tokens_input"),
			TokensOutput:     d.i64(idx, "tokens_output"),
			TokensReason:     d.i64(idx, "tokens_reasoning"),
			TokensCacheRd:    d.i64(idx, "tokens_cache_read"),
			TokensCacheWr:    d.i64(idx, "tokens_cache_write"),
			TimeCreatedMs:    d.i64(idx, "time_created"),
			TimeUpdatedMs:    d.i64(idx, "time_updated"),
			TimeArchivedMs:   d.i64(idx, "time_archived"),
			TimeCompactingMs: d.i64(idx, "time_compacting"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanMessageRow reads one message delta row into a messageRow.
func scanMessageRow(idx columnIndex, n int) (func(*sql.Rows) (rowKey, error), *messageRow) {
	var out messageRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan message row: %w", err)
		}
		out = messageRow{
			ID:            d.str(idx, "id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanPartRow reads one part delta row into a partRow.
func scanPartRow(idx columnIndex, n int) (func(*sql.Rows) (rowKey, error), *partRow) {
	var out partRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan part row: %w", err)
		}
		out = partRow{
			ID:            d.str(idx, "id"),
			MessageID:     d.str(idx, "message_id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanSessionMessageRow reads one session_message delta row into a
// sessionMessageRow.
func scanSessionMessageRow(idx columnIndex, n int) (func(*sql.Rows) (rowKey, error), *sessionMessageRow) {
	var out sessionMessageRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan session_message row: %w", err)
		}
		out = sessionMessageRow{
			ID:            d.str(idx, "id"),
			SessionID:     d.str(idx, "session_id"),
			Type:          d.str(idx, "type"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}
