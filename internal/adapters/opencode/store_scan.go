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

// scanMessageRow reads one message delta row into a messageRow. session_id is a
// REQUIRED owning-id column (round-5 P2-2): an empty/corrupt value ERRORS the page
// rather than deriving an empty affected session (which affectedSet.add silently
// drops while the row "succeeds", advancing the cursor past an un-emitted change).
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
		sid, err := requiredOwner(d, idx, "session_id")
		if err != nil {
			return rowKey{}, err
		}
		out = messageRow{
			ID:            id,
			SessionID:     sid,
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: tuid,
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanPartRow reads one part delta row into a partRow. message_id AND session_id
// are REQUIRED owning-id columns (round-5 P2-2): a part's affected session is
// derived from its denormalized session_id (resolvePartSession), so an empty/
// corrupt session_id would silently drop the change (affectedSet.add("")) while
// the row "succeeds" → cursor gap. message_id is the other owning id (the
// old-schema fallback resolver and msgSession key), so it is required too. Either
// being empty/corrupt ERRORS the page so the cursor never advances past the row.
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
		mid, err := requiredOwner(d, idx, "message_id")
		if err != nil {
			return rowKey{}, err
		}
		sid, err := requiredOwner(d, idx, "session_id")
		if err != nil {
			return rowKey{}, err
		}
		out = partRow{
			ID:            id,
			MessageID:     mid,
			SessionID:     sid,
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: tuid,
			Data:          d.bytes(idx, "data"),
		}
		return rowKey{id: out.ID, timeUpdatedMs: out.TimeUpdatedMs}, nil
	}
	return fn, &out
}

// scanSessionMessageRow reads one session_message delta row into a
// sessionMessageRow. session_id is a REQUIRED owning-id column (round-5 P2-2): an
// empty/corrupt value ERRORS the page rather than deriving an empty affected
// session. `type` is NOT an owning id — it stays an optional d.str read; a missing
// type keeps its existing unknown-type WARN behaviour (deltaRowHandler), never a
// fatal error (only the owning IDs are fatal-on-corrupt).
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
		sid, err := requiredOwner(d, idx, "session_id")
		if err != nil {
			return rowKey{}, err
		}
		out = sessionMessageRow{
			ID:            id,
			SessionID:     sid,
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

// requiredOwner reads a REQUIRED OWNING-ID column (message.session_id,
// part.message_id, part.session_id, session_message.session_id) for a delta row,
// returning an error rather than the empty string when the cell is absent/NULL/
// empty (SOW-0005 round-5 P2-2). The owning id derives the AFFECTED session that
// the tailer reloads; an empty value would be silently swallowed by
// affectedSet.add("") while the row handler SUCCEEDED, so the cursor would advance
// PAST a change that emitted no content — a permanent, health-invisible gap.
// Erroring aborts the page (via the scanner closure → scanOnePage), so the cursor
// stays at the last good watermark and the transient error is surfaced (non-fatal)
// via the poll loop. The column is in requiredColumns, so it is always PRESENT
// (introspectAll makes its absence fatal upstream); the only failure mode reaching
// here is a corrupt/empty cell value. strRequired carries the table/column context.
func requiredOwner(d *scanDest, idx columnIndex, col string) (string, error) {
	return d.strRequired(idx, col)
}
