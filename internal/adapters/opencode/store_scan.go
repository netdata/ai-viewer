package opencode

import (
	"database/sql"
	"fmt"
)

// This file holds the per-table DELTA-ROW SCANNERS (driven by scanTableDelta in
// store_query.go AND the boundary-bucket re-scan in tailer_boundary.go): one
// closure per tracked table that scans a delta row into its typed struct via the
// present-column index and reports the row's (id, time_updated) watermark key.
// Split out of store_load.go to keep each file ≤400 lines (SOW-0005 round-2; the
// P2-B single-query part loader grew store_load.go). Every read uses the schema's
// PRESENT columns only (never SELECT *), so an old schema missing an optional
// column degrades to a zero value rather than failing.
//
// Each scanner takes an onWarn callback (SOW-0005 round-4 P2-1): the optional
// numeric cells (cost/tokens/time_created/time_archived/time_compacting) surface a
// corrupt value as a WARN and degrade to 0 (parity with the non-delta loadSession
// path); the REQUIRED cursor-watermark columns (id, time_updated) instead return an
// ERROR via i64Required/strRequired so a corrupt cell can never advance the cursor
// to a poisoned watermark (the error aborts the page; the cursor stays at the last
// good position).

// scanSessionRow reads one session delta row into a sessionRow via the present
// columns and reports its watermark key. Missing optional columns (old schema)
// stay zero (with a WARN on a corrupt non-NULL cell); a corrupt REQUIRED id/
// time_updated returns an error rather than a poisoned-0 watermark (round-4 P2-1).
func scanSessionRow(idx columnIndex, n int, onWarn func(error)) (func(*sql.Rows) (rowKey, error), *sessionRow) {
	var out sessionRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n).withWarn("session", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan session row: %w", err)
		}
		id, tuid, err := requiredWatermark(d, idx)
		if err != nil {
			return rowKey{}, err
		}
		out = sessionRow{
			ID:               id,
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
			TimeUpdatedMs:    tuid,
			TimeArchivedMs:   d.i64(idx, "time_archived"),
			TimeCompactingMs: d.i64(idx, "time_compacting"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanMessageRow reads one message delta row into a messageRow.
func scanMessageRow(idx columnIndex, n int, onWarn func(error)) (func(*sql.Rows) (rowKey, error), *messageRow) {
	var out messageRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n).withWarn("message", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan message row: %w", err)
		}
		id, tuid, err := requiredWatermark(d, idx)
		if err != nil {
			return rowKey{}, err
		}
		out = messageRow{
			ID:            id,
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: tuid,
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanPartRow reads one part delta row into a partRow.
func scanPartRow(idx columnIndex, n int, onWarn func(error)) (func(*sql.Rows) (rowKey, error), *partRow) {
	var out partRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n).withWarn("part", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan part row: %w", err)
		}
		id, tuid, err := requiredWatermark(d, idx)
		if err != nil {
			return rowKey{}, err
		}
		out = partRow{
			ID:            id,
			MessageID:     d.str(idx, "message_id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: tuid,
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanSessionMessageRow reads one session_message delta row into a
// sessionMessageRow.
func scanSessionMessageRow(idx columnIndex, n int, onWarn func(error)) (func(*sql.Rows) (rowKey, error), *sessionMessageRow) {
	var out sessionMessageRow
	fn := func(rows *sql.Rows) (rowKey, error) {
		d := newScanDest(n).withWarn("session_message", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return rowKey{}, fmt.Errorf("opencode: scan session_message row: %w", err)
		}
		id, tuid, err := requiredWatermark(d, idx)
		if err != nil {
			return rowKey{}, err
		}
		out = sessionMessageRow{
			ID:            id,
			SessionID:     d.str(idx, "session_id"),
			Type:          d.str(idx, "type"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: tuid,
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// requiredWatermark reads the two REQUIRED cursor-watermark columns (id,
// time_updated) for a delta row, returning an error rather than a coerced-0 value
// when either is absent/NULL/corrupt (SOW-0005 round-4 P2-1). Erroring aborts the
// page so a poisoned watermark is never persisted; the cursor stays at the last
// good position and the transient error is surfaced (non-fatal) via the poll loop.
func requiredWatermark(d *scanDest, idx columnIndex) (id string, timeUpdatedMs int64, err error) {
	id, err = d.strRequired(idx, "id")
	if err != nil {
		return "", 0, err
	}
	timeUpdatedMs, err = d.i64Required(idx, "time_updated")
	if err != nil {
		return "", 0, err
	}
	return id, timeUpdatedMs, nil
}
