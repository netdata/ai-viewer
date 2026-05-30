package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// This file is the TREE-LOAD layer (SOW-0005 chunk C): given an affected session
// id, it loads the whole session tree — the session row, its messages ordered by
// (time_created, id), and each message's parts ordered by (id) — and assembles
// the []messageWithParts the pure mapper consumes. Full-tree reload is mandatory
// (not partial): mapSession computes per-turn cumulative-token deltas across the
// ordered message list, so a partial reload would miscompute deltas
// (adapter-opencode.md §"Read Strategy" → "Full-session-tree load + map"). It
// also holds the per-table delta-row scanners (sessionRow/messageRow/partRow/
// sessionMessageRow) that scanTableDelta (store_query.go) drives. Every read uses
// the schema's PRESENT columns only (never SELECT *), so an old schema missing a
// column degrades to a zero value rather than failing.

// errSessionGone marks an affected session whose session row could not be loaded
// (deleted between the delta page and the tree load, or a part/message orphaned
// from its session). The poll loop skips it with one structured error and
// continues with the remaining sessions (adapter-opencode.md §"Read Strategy").
var errSessionGone = errors.New("opencode: session row not found")

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

// bytes returns the present column's raw value as bytes, or nil when absent/NULL
// (used for the JSON data/model columns the mapper decodes).
func (d *scanDest) bytes(idx columnIndex, col string) []byte {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		return []byte(d.holders[i].String)
	}
	return nil
}

// --- per-table delta-row scanners (driven by scanTableDelta) ------------------

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
			ID:             d.str(idx, "id"),
			ProjectID:      d.str(idx, "project_id"),
			ParentID:       d.str(idx, "parent_id"),
			Slug:           d.str(idx, "slug"),
			Directory:      d.str(idx, "directory"),
			Title:          d.str(idx, "title"),
			Version:        d.str(idx, "version"),
			Agent:          d.str(idx, "agent"),
			Model:          d.bytes(idx, "model"),
			Cost:           d.f64(idx, "cost"),
			TokensInput:    d.i64(idx, "tokens_input"),
			TokensOutput:   d.i64(idx, "tokens_output"),
			TokensReason:   d.i64(idx, "tokens_reasoning"),
			TokensCacheRd:  d.i64(idx, "tokens_cache_read"),
			TokensCacheWr:  d.i64(idx, "tokens_cache_write"),
			TimeCreatedMs:  d.i64(idx, "time_created"),
			TimeUpdatedMs:  d.i64(idx, "time_updated"),
			TimeArchivedMs: d.i64(idx, "time_archived"),
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

// --- full-session-tree load ---------------------------------------------------
//
// resolveRootID (the parent_id chain walk that gives a nested sub-agent its TRUE
// tree root, SOW-0005 P2.4) lives in store_root.go (split to keep this file ≤400
// lines).

// loadSession loads one session row by id via the present-column SELECT. The
// bool is false (with no error) when the row does not exist (the affected
// session was deleted between the delta and the load); the caller skips it.
// onWarn surfaces a corrupt numeric cell (SOW-0005 P2.6); it may be nil.
func loadSession(ctx context.Context, db *sql.DB, schema schemaSet, id string, onWarn func(error)) (sessionRow, bool, error) {
	s := schema["session"]
	idx := newColumnIndex(s)
	q := selectByIDList(s)
	d := newScanDest(len(s.Present)).withWarn("session", onWarn)
	err := db.QueryRowContext(ctx, q, id).Scan(d.ptrs...)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, false, nil
	}
	if err != nil {
		return sessionRow{}, false, fmt.Errorf("opencode: load session %s: %w", id, err)
	}
	return sessionRow{
		ID:             d.str(idx, "id"),
		ProjectID:      d.str(idx, "project_id"),
		ParentID:       d.str(idx, "parent_id"),
		Slug:           d.str(idx, "slug"),
		Directory:      d.str(idx, "directory"),
		Title:          d.str(idx, "title"),
		Version:        d.str(idx, "version"),
		Agent:          d.str(idx, "agent"),
		Model:          d.bytes(idx, "model"),
		Cost:           d.f64(idx, "cost"),
		TokensInput:    d.i64(idx, "tokens_input"),
		TokensOutput:   d.i64(idx, "tokens_output"),
		TokensReason:   d.i64(idx, "tokens_reasoning"),
		TokensCacheRd:  d.i64(idx, "tokens_cache_read"),
		TokensCacheWr:  d.i64(idx, "tokens_cache_write"),
		TimeCreatedMs:  d.i64(idx, "time_created"),
		TimeUpdatedMs:  d.i64(idx, "time_updated"),
		TimeArchivedMs: d.i64(idx, "time_archived"),
	}, true, nil
}

// loadSessionTree loads a session's full ordered message+part tree as
// []messageWithParts: messages ordered by (time_created, id), each with its
// parts ordered by (id). The whole tree is required so the mapper's per-turn
// token deltas are correct (adapter-opencode.md §"Read Strategy"). It runs in a
// single read-only transaction so messages and parts share one consistent
// snapshot (a concurrent opencode write mid-load cannot split a message from its
// parts).
func loadSessionTree(ctx context.Context, db *sql.DB, schema schemaSet, sessionID string, onWarn func(error)) ([]messageWithParts, error) {
	tx, err := beginRO(ctx, db)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	msgs, err := loadMessages(ctx, tx, schema["message"], sessionID, onWarn)
	if err != nil {
		return nil, err
	}
	out := make([]messageWithParts, 0, len(msgs))
	for i := range msgs {
		parts, perr := loadParts(ctx, tx, schema["part"], msgs[i].ID, onWarn)
		if perr != nil {
			return nil, perr
		}
		out = append(out, messageWithParts{Message: msgs[i], Parts: parts})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("opencode: commit tree tx: %w", err)
	}
	return out, nil
}

// loadMessages reads a session's messages ordered by (time_created, id). The
// order column names come from the live schema; they are required columns
// (introspectAll guarantees session_id/time_updated/data present), and
// time_created is in wantedColumns for message on every schema.
func loadMessages(ctx context.Context, tx *sql.Tx, s tableSchema, sessionID string, onWarn func(error)) ([]messageRow, error) {
	idx := newColumnIndex(s)
	q := selectByColumn(s, "session_id", messageOrderBy(s))
	rows, err := tx.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("opencode: load messages for %s: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []messageRow
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := newScanDest(len(s.Present)).withWarn("message", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return nil, fmt.Errorf("opencode: scan message: %w", err)
		}
		out = append(out, messageRow{
			ID:            d.str(idx, "id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opencode: iterate messages: %w", err)
	}
	return out, nil
}

// loadParts reads a message's parts ordered by id (the natural lexicographic
// Sonyflake order = creation order; adapter-opencode.md §"part").
func loadParts(ctx context.Context, tx *sql.Tx, s tableSchema, messageID string, onWarn func(error)) ([]partRow, error) {
	idx := newColumnIndex(s)
	q := selectByColumn(s, "message_id", "id")
	rows, err := tx.QueryContext(ctx, q, messageID)
	if err != nil {
		return nil, fmt.Errorf("opencode: load parts for %s: %w", messageID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []partRow
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := newScanDest(len(s.Present)).withWarn("part", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return nil, fmt.Errorf("opencode: scan part: %w", err)
		}
		out = append(out, partRow{
			ID:            d.str(idx, "id"),
			MessageID:     d.str(idx, "message_id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opencode: iterate parts: %w", err)
	}
	return out, nil
}

// The present-column SELECT builders (presentColsSQL/selectByIDList/
// selectByColumn/messageOrderBy) and the numeric parse helpers
// (parseInt64*/parseFloat64*) live in store_load_sql.go (split to keep this file
// ≤400 lines).
