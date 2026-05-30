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

// maxSessionMessagesWarn / maxSessionPartsWarn are DEFENSIVE upper bounds on a
// single session's in-memory tree (SOW-0005 round-3 P2-3). The full ordered tree
// MUST be loaded at once — the mapper synthesizes per-turn token deltas by
// subtracting successive cumulative snapshots, so there is no correct streaming
// decomposition. These caps do NOT truncate; they only mark the threshold above
// which the loader emits ONE structured WARN via onWarn so a pathological or
// corrupt session surfaces (in the logs and, via the adapter's onError →
// SourceError, in /api/health) instead of silently spiking memory. Set
// generously: real opencode sessions are far below 100k of either.
const (
	maxSessionMessagesWarn = 100_000
	maxSessionPartsWarn    = 100_000
)

// warnIfSessionTooLarge emits ONE structured WARN via onWarn when a session's
// loaded message or part count exceeds its defensive bound (SOW-0005 round-3
// P2-3). It NEVER truncates — the caller still processes the whole tree — it only
// SURFACES the anomaly. onWarn may be nil (the pure no-DB path), in which case the
// check is a no-op. The part count is summed across the per-message map.
func warnIfSessionTooLarge(sessionID string, msgs []messageRow, partsByMessage map[string][]partRow, onWarn func(error)) {
	if onWarn == nil {
		return
	}
	if len(msgs) > maxSessionMessagesWarn {
		onWarn(fmt.Errorf("opencode: session %s has %d messages (> %d); processing in full — possible pathological/corrupt session (P2-3)", sessionID, len(msgs), maxSessionMessagesWarn))
	}
	parts := 0
	for _, ps := range partsByMessage {
		parts += len(ps)
	}
	if parts > maxSessionPartsWarn {
		onWarn(fmt.Errorf("opencode: session %s has %d parts (> %d); processing in full — possible pathological/corrupt session (P2-3)", sessionID, parts, maxSessionPartsWarn))
	}
}

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

// bytes returns the present column's raw value as bytes, or nil when absent/NULL
// (used for the JSON data/model columns the mapper decodes).
func (d *scanDest) bytes(idx columnIndex, col string) []byte {
	if i, ok := idx[col]; ok && d.holders[i].Valid {
		return []byte(d.holders[i].String)
	}
	return nil
}

// The per-table delta-row scanners (scanSessionRow/scanMessageRow/scanPartRow/
// scanSessionMessageRow) live in store_scan.go (split to keep each file ≤400
// lines).

// --- full-session-tree load ---------------------------------------------------
//
// resolveRootID (the parent_id chain walk that gives a nested sub-agent its TRUE
// tree root, SOW-0005 P2.4) lives in store_root.go (split to keep this file ≤400
// lines).

// roQuerier is the read-only query surface both *sql.DB and *sql.Tx satisfy. The
// tree-load helpers take it so the SAME code path runs either against the pool
// directly (test entrypoints) or inside ONE shared read-only transaction
// (loadAndMapSession's single consistent snapshot — SOW-0005 round-3 P1-2).
type roQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadSession loads one session row by id via the present-column SELECT. The
// bool is false (with no error) when the row does not exist (the affected
// session was deleted between the delta and the load); the caller skips it.
// onWarn surfaces a corrupt numeric cell (SOW-0005 P2.6); it may be nil. q is any
// roQuerier (the pool or the shared snapshot tx — P1-2).
func loadSession(ctx context.Context, q roQuerier, schema schemaSet, id string, onWarn func(error)) (sessionRow, bool, error) {
	s := schema["session"]
	idx := newColumnIndex(s)
	query := selectByIDList(s)
	d := newScanDest(len(s.Present)).withWarn("session", onWarn)
	err := q.QueryRowContext(ctx, query, id).Scan(d.ptrs...)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, false, nil
	}
	if err != nil {
		return sessionRow{}, false, fmt.Errorf("opencode: load session %s: %w", id, err)
	}
	return sessionRow{
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
	}, true, nil
}

// loadSessionTree loads a session's full ordered message+part tree as
// []messageWithParts: messages ordered by (time_created, id), each with its
// parts ordered by (id). The whole tree is required so the mapper's per-turn
// token deltas are correct (adapter-opencode.md §"Read Strategy").
//
// q is any roQuerier. The PRODUCTION path (loadAndMapSession) passes the SAME
// read-only transaction that already read the session row and checked
// time_compacting, so the session metadata, the compaction check, and the tree
// share ONE consistent snapshot (SOW-0005 round-3 P1-2: no compaction-race
// TOCTOU). Test entrypoints may pass the pool directly. The tree-load itself does
// NOT begin/commit a transaction — its caller owns the snapshot lifecycle.
//
// Parts are loaded with ONE query for the whole session (SOW-0005 round-2 P2-B),
// NOT one query per message: the part table denormalizes session_id, so a single
// WHERE session_id = ? ORDER BY (message_id, id) returns every part already
// grouped by message; the rows are then partitioned in memory and attached to
// each message in its (time_created, id) order. session_id is a REQUIRED part
// column (introspectAll), so there is no old-schema message_id-IN fallback
// (SOW-0005 round-3 P3-1 removed the unreachable one).
//
// As a defensive safety signal, the loaded message and part counts are bounded by
// maxSessionMessagesWarn / maxSessionPartsWarn: a session exceeding either emits
// ONE structured WARN via onWarn and is STILL processed in full — the whole
// ordered tree is mandatory for the token-delta synthesis, so truncating would
// corrupt the deltas (SOW-0005 round-3 P2-3).
func loadSessionTree(ctx context.Context, q roQuerier, schema schemaSet, sessionID string, onWarn func(error)) ([]messageWithParts, error) {
	msgs, err := loadMessages(ctx, q, schema["message"], sessionID, onWarn)
	if err != nil {
		return nil, err
	}
	partsByMessage, err := loadSessionParts(ctx, q, schema["part"], sessionID, onWarn)
	if err != nil {
		return nil, err
	}
	warnIfSessionTooLarge(sessionID, msgs, partsByMessage, onWarn)
	out := make([]messageWithParts, 0, len(msgs))
	for i := range msgs {
		out = append(out, messageWithParts{Message: msgs[i], Parts: partsByMessage[msgs[i].ID]})
	}
	return out, nil
}

// loadMessages reads a session's messages ordered by (time_created, id). The
// order column names come from the live schema; they are required columns
// (introspectAll guarantees session_id/time_updated/data present), and
// time_created is in wantedColumns for message on every schema. q is any
// roQuerier (the shared snapshot tx in production).
func loadMessages(ctx context.Context, qr roQuerier, s tableSchema, sessionID string, onWarn func(error)) ([]messageRow, error) {
	idx := newColumnIndex(s)
	q := selectByColumn(s, "session_id", messageOrderBy(s))
	rows, err := qr.QueryContext(ctx, q, sessionID)
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

// loadSessionParts reads ALL of a session's parts in ONE indexed query over the
// denormalized session_id (SOW-0005 round-2 P2-B — replaces the per-message N+1
// loop), ordered (message_id, id) so each message's parts arrive contiguous and in
// creation order. scanPartRows partitions them by message_id. session_id is a
// REQUIRED part column (introspectAll fails fast without it), so a part table that
// reaches this function ALWAYS has it — the round-2 P2-B old-schema
// message_id-IN fallback was unreachable and was removed (SOW-0005 round-3 P3-1).
// Returns a map keyed by message_id; a message with no parts is simply absent
// (nil slice on lookup). q is any roQuerier (the shared snapshot tx in production).
func loadSessionParts(ctx context.Context, qr roQuerier, s tableSchema, sessionID string, onWarn func(error)) (map[string][]partRow, error) {
	q := selectByColumn(s, "session_id", quoteIdent("message_id")+", "+quoteIdent("id"))
	rows, err := qr.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("opencode: load parts for session %s: %w", sessionID, err)
	}
	return scanPartRows(ctx, rows, s, onWarn, "session "+sessionID)
}

// scanPartRows scans a part result set and partitions the rows into a map keyed by
// message_id, preserving the query's (message_id, id) order within each group. It
// owns closing rows. label is used only in error context.
func scanPartRows(ctx context.Context, rows *sql.Rows, s tableSchema, onWarn func(error), label string) (map[string][]partRow, error) {
	defer func() { _ = rows.Close() }()
	idx := newColumnIndex(s)
	out := map[string][]partRow{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d := newScanDest(len(s.Present)).withWarn("part", onWarn)
		if err := rows.Scan(d.ptrs...); err != nil {
			return nil, fmt.Errorf("opencode: scan part (%s): %w", label, err)
		}
		p := partRow{
			ID:            d.str(idx, "id"),
			MessageID:     d.str(idx, "message_id"),
			SessionID:     d.str(idx, "session_id"),
			TimeCreatedMs: d.i64(idx, "time_created"),
			TimeUpdatedMs: d.i64(idx, "time_updated"),
			Data:          d.bytes(idx, "data"),
		}
		out[p.MessageID] = append(out[p.MessageID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("opencode: iterate parts (%s): %w", label, err)
	}
	return out, nil
}

// The present-column SELECT builders (presentColsSQL/selectByIDList/
// selectByColumn/messageOrderBy) and the numeric parse helpers
// (parseInt64*/parseFloat64*) live in store_load_sql.go (split to keep this file
// ≤400 lines).
