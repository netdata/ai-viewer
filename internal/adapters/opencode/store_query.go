package opencode

import (
	"context"
	"database/sql"
	"fmt"
)

// This file is the SQL DELTA-QUERY layer (SOW-0005 chunk C): one paged delta
// query per tracked table, the cheap PK-indexed MAX(id) change check, the gated
// (expensive) MAX(time_updated) probe, and the affected-session derivation that
// turns a batch of changed rows into the SET of session ids whose full tree the
// tailer reloads. Every query runs in its own SHORT read-only transaction
// (BEGIN DEFERRED) so the live opencode writer's WAL is never pinned
// (adapter-opencode.md §"Read Strategy" → "Delta query…"; the page SQL is the
// SOW-recorded template). The tree LOAD lives in store_load.go; the poll loops
// in tailer.go. This layer performs SQL but no event mapping — it hands typed
// rows to the pure mapper via the loader.

// deltaPageLimit is the per-page row cap. It matches buildSelect's hardcoded
// LIMIT 1000 (store.go) and the SOW-recorded page size; a page shorter than this
// is the last page for a table. Kept as a named constant so the paging loop's
// short-page test reads against the same value the SELECT embeds.
const deltaPageLimit = 1000

// normalizeContextSQLError gives caller cancellation precedence over
// driver-specific interruption strings emitted by modernc.org/sqlite after
// ctx.Done. It preserves the original SQL error while the context is still
// active.
func normalizeContextSQLError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// rowKey is the watermark pair (id, time_updated) the per-table delta scan
// reports for each row so the paging loop can advance the cursor without
// re-scanning the row. On an old schema lacking time_updated the scan reports
// timeUpdatedMs = 0 and only id advances.
type rowKey struct {
	id            string
	timeUpdatedMs int64
}

// tableDelta is the result of paging one table forward from a watermark: the
// advanced watermark (the max (time_updated, id) seen) and the number of rows
// read across all pages (used by the backfill loop to checkpoint progress).
type tableDelta struct {
	watermark TableWatermark
	rowCount  int
}

// beginRO opens a short read-only deferred transaction. database/sql maps
// ReadOnly:true to BEGIN DEFERRED for modernc.org/sqlite, taking the snapshot on
// the first statement and never pinning the WAL for writes. The caller MUST
// commit/rollback promptly (one page per tx) to keep the snapshot window <1 s.
func beginRO(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("opencode: begin ro tx: %w", normalizeContextSQLError(ctx, err))
	}
	return tx, nil
}

// maxID returns MAX(id) for the table via the PK-indexed b-tree (the cheap
// primary change check — ~µs, adapter-opencode.md §"Performance"). An empty
// table yields "" (no rows). The table name is a fixed trackedTables entry, so
// it is safe to interpolate (quoted as an identifier defensively).
func maxID(ctx context.Context, db *sql.DB, table string) (string, error) {
	var id sql.NullString
	q := `SELECT MAX(id) FROM ` + quoteIdent(table) // #nosec G202 -- table is a fixed trackedTables identifier via quoteIdent, never user input
	if err := db.QueryRowContext(ctx, q).Scan(&id); err != nil {
		return "", fmt.Errorf("opencode: max(id) %s: %w", table, normalizeContextSQLError(ctx, err))
	}
	return id.String, nil
}

// maxTimeUpdated returns MAX(time_updated) for the table. This is the EXPENSIVE,
// UNINDEXED probe (a full scan — 400–800 ms on the 585k-row part table), so the
// tailer issues it ONLY when its gate is open (shouldProbeTimeUpdated). A table
// on an old schema lacking the column is reported by the caller before this runs
// (the caller checks tableSchema.has); this query assumes the column exists.
// Returns 0 for an empty table.
func maxTimeUpdated(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var v sql.NullInt64
	q := `SELECT MAX(time_updated) FROM ` + quoteIdent(table) // #nosec G202 -- table is a fixed trackedTables identifier via quoteIdent, never user input
	if err := db.QueryRowContext(ctx, q).Scan(&v); err != nil {
		return 0, fmt.Errorf("opencode: max(time_updated) %s: %w", table, normalizeContextSQLError(ctx, err))
	}
	return v.Int64, nil
}

// scanTableDelta pages one table forward from `from`, invoking onRow for every
// changed row. onRow scans the table-specific columns AND returns the row's
// (id, time_updated) so the paging loop advances the watermark without a second
// Scan of the cursor. It pages until a short page (<deltaPageLimit) returns,
// each page in its OWN short read transaction, and returns the advanced
// watermark + total row count. The page SQL is the schema's dynamic composite-key
// SELECT (buildSelect — present columns only). time_updated is a required column
// (introspectAll enforces it), so there is no id-only fallback; ordering is the
// cursor's composite (time_updated, id) key so a resume never skips/duplicates a
// row at a tie.
//
// sink buffers any per-row WARN raised inside a page tx; scanOnePage flushes it
// through onError AFTER each page's tx closes (SOW-0005 round-5 P2-1), so no
// warning/error is emitted while a read tx is open. onRow MUST write its warnings
// into the SAME sink (callers build it via deltaRowHandler(..., sink.collect)).
func scanTableDelta(ctx context.Context, db *sql.DB, s tableSchema, from TableWatermark, onRow func(rows *sql.Rows) (rowKey, error), sink *warnSink, onError func(error)) (tableDelta, error) {
	query := s.buildSelect()

	wm := from
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return tableDelta{watermark: wm, rowCount: total}, err
		}
		page, err := scanOnePage(ctx, db, query, wm, onRow, sink, onError)
		if err != nil {
			return tableDelta{watermark: wm, rowCount: total}, err
		}
		total += page.n
		if page.n > 0 {
			wm = page.watermark
		}
		if page.n < deltaPageLimit {
			break // short page → caught up
		}
	}
	return tableDelta{watermark: wm, rowCount: total}, nil
}

// pageResult is one page's outcome: rows read and the max (time_updated, id)
// observed within the page.
type pageResult struct {
	n         int
	watermark TableWatermark
}

// scanOnePage runs one page of the composite-key delta query inside a fresh
// read-only tx, invoking onRow per row and tracking the page's max watermark. The
// tx is committed before returning so the WAL is released between pages (the
// snapshot advances between pages, which is correct for a tailing reader). The
// bind is always the 3-param (time_updated, time_updated, id) form — time_updated
// is a required column on every tracked table (introspectAll), so there is no
// id-only variant.
//
// No warning/error EMISSION happens while the tx is open (SOW-0005 round-5 P2-1):
// onRow writes any corrupt-cell / unknown-type WARN into sink (a non-blocking
// slice append), the tx is committed/rolled back FIRST (explicitly, not via the
// deferred rollback — so the snapshot is provably released), and only THEN are the
// buffered warnings flushed through the live onError. A FATAL row error (a corrupt
// REQUIRED watermark/owning-id cell — round-4 P2-1 / round-5 P2-2) is RETURNED, not
// emitted inside; the tx is rolled back and the sink flushed before it propagates,
// so neither the warnings NOR the fatal error reach the (possibly backpressured)
// out channel with the WAL snapshot still pinned. sink is reset by flush, ready
// for the next page.
func scanOnePage(ctx context.Context, db *sql.DB, query string, from TableWatermark, onRow func(rows *sql.Rows) (rowKey, error), sink *warnSink, onError func(error)) (pageResult, error) {
	tx, err := beginRO(ctx, db)
	if err != nil {
		return pageResult{}, err
	}

	rows, err := tx.QueryContext(ctx, query, from.MaxTimeUpdatedMs, from.MaxTimeUpdatedMs, from.MaxTimeUpdatedID)
	if err != nil {
		_ = tx.Rollback() // close the tx before any (post-tx) error surfacing
		sink.flush(onError)
		return pageResult{}, fmt.Errorf("opencode: delta query: %w", normalizeContextSQLError(ctx, err))
	}

	res := pageResult{watermark: from}
	scanErr := iterDeltaPage(ctx, rows, &res, onRow)
	_ = rows.Close()
	if scanErr != nil {
		scanErr = normalizeContextSQLError(ctx, scanErr)
	} else {
		scanErr = normalizeContextSQLError(ctx, rows.Err())
	}
	// Close the tx (releasing the WAL snapshot) BEFORE flushing buffered warnings
	// or surfacing a fatal row error — so a backpressured onError can never block
	// with the snapshot held (P2-1). On a scan/iterate error we roll back; on a
	// clean page we commit.
	if scanErr != nil {
		_ = tx.Rollback()
		sink.flush(onError)
		return res, fmt.Errorf("opencode: delta page: %w", scanErr)
	}
	commitErr := tx.Commit()
	sink.flush(onError)
	if commitErr = normalizeContextSQLError(ctx, commitErr); commitErr != nil {
		return res, fmt.Errorf("opencode: commit ro tx: %w", commitErr)
	}
	return res, nil
}

// iterDeltaPage walks one page's rows, delegating each row's column scan to
// onRow and advancing the watermark from each row's (id, time_updated). The
// PAGING POSITION (MaxTimeUpdatedMs, MaxTimeUpdatedID) is set to the last-paged
// row (rows arrive in (time_updated, id) order, so the last row is the max
// position). MaxIDSeen is raised MONOTONICALLY to the greatest id seen — never
// regressing — so an in-place UPDATE of an OLD row (which sorts LAST by
// time_updated but carries a small id) advances the paging position WITHOUT
// pulling the cheap-detect high-water backwards (SOW-0005 round-2 P1-A). time_
// updated is always present on a tracked table, so both position fields advance.
func iterDeltaPage(ctx context.Context, rows *sql.Rows, res *pageResult, onRow func(rows *sql.Rows) (rowKey, error)) error {
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, err := onRow(rows)
		if err != nil {
			return err
		}
		res.n++
		res.watermark.MaxTimeUpdatedMs = key.timeUpdatedMs
		res.watermark.MaxTimeUpdatedID = key.id
		res.watermark = res.watermark.advanceMaxIDSeen(key.id)
	}
	return nil
}

// affectedSet accumulates, de-duplicated and in first-seen order, the session
// ids whose full tree must be reloaded after a change cycle. First-seen order
// keeps reload (and thus emission) deterministic for a given delta batch.
type affectedSet struct {
	seen  map[string]struct{}
	order []string
}

// newAffectedSet returns an empty set.
func newAffectedSet() *affectedSet {
	return &affectedSet{seen: map[string]struct{}{}}
}

// add records a session id (ignoring empties and duplicates).
func (a *affectedSet) add(id string) {
	if id == "" {
		return
	}
	if _, ok := a.seen[id]; ok {
		return
	}
	a.seen[id] = struct{}{}
	a.order = append(a.order, id)
}

// ids returns the affected session ids in first-seen order.
func (a *affectedSet) ids() []string { return a.order }

// resolvePartSession returns the owning session id for a changed part row. The
// part table denormalizes session_id (adapter-opencode.md §"part"), and session_id
// is a REQUIRED part column (requiredColumns["part"], store.go) — introspectAll makes
// its absence FATAL upstream, so a part table that reaches this layer ALWAYS has it.
// The round-2 P2-B old-schema message-lookup fallback (message_id → session_id via a
// pool query, consulting an in-run message→session map first) was therefore
// UNREACHABLE in production and was removed (SOW-0005 round-6 P3-2; same class as the
// round-3 P3-1 dead-fallback removal). The delta scanner (scanPartRow → requiredOwner,
// round-5 P2-2) already ERRORS the page on an empty/corrupt session_id before this is
// reached, so p.SessionID is non-empty here; the empty guard remains as defence in
// depth (it returns an error rather than deriving an empty affected session, which
// affectedSet.add would silently drop while the row "succeeded" → a cursor gap).
func resolvePartSession(p partRow) (string, error) {
	if p.SessionID == "" {
		return "", fmt.Errorf("opencode: part %s has empty session_id (required column); refusing to derive an empty affected session", p.ID)
	}
	return p.SessionID, nil
}
