package opencode

import (
	"context"
	"database/sql"
	"errors"
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
		return nil, fmt.Errorf("opencode: begin ro tx: %w", err)
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
		return "", fmt.Errorf("opencode: max(id) %s: %w", table, err)
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
		return 0, fmt.Errorf("opencode: max(time_updated) %s: %w", table, err)
	}
	return v.Int64, nil
}

// scanTableDelta pages one table forward from `from`, invoking onRow for every
// changed row. onRow scans the table-specific columns AND returns the row's
// (id, time_updated) so the paging loop advances the watermark without a second
// Scan of the cursor. It pages until a short page (<deltaPageLimit) returns,
// each page in its OWN short read transaction, and returns the advanced
// watermark + total row count. The page SQL is the schema's dynamic SELECT
// (buildSelect — present columns only; buildSelectByID on an old schema without
// time_updated). Ordering is the cursor's composite key so a resume never
// skips/duplicates a row at a tie.
func scanTableDelta(ctx context.Context, db *sql.DB, s tableSchema, from TableWatermark, onRow func(rows *sql.Rows) (rowKey, error)) (tableDelta, error) {
	hasTU := s.has("time_updated")
	query := s.buildSelect()
	if !hasTU {
		query = s.buildSelectByID()
	}

	wm := from
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return tableDelta{watermark: wm, rowCount: total}, err
		}
		page, err := scanOnePage(ctx, db, query, wm, hasTU, onRow)
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

// scanOnePage runs one page of the delta query inside a fresh read-only tx,
// invoking onRow per row and tracking the page's max watermark. The tx is
// committed before returning so the WAL is released between pages (the snapshot
// advances between pages, which is correct for a tailing reader). hasTU selects
// the 3-param (time_updated, time_updated, id) bind vs the 1-param (id) bind.
func scanOnePage(ctx context.Context, db *sql.DB, query string, from TableWatermark, hasTU bool, onRow func(rows *sql.Rows) (rowKey, error)) (pageResult, error) {
	tx, err := beginRO(ctx, db)
	if err != nil {
		return pageResult{}, err
	}
	// Roll back on every exit path; a successful Commit makes the deferred
	// Rollback a no-op (database/sql ignores rollback after commit).
	defer func() { _ = tx.Rollback() }()

	var rows *sql.Rows
	if hasTU {
		rows, err = tx.QueryContext(ctx, query, from.MaxTimeUpdatedMs, from.MaxTimeUpdatedMs, from.MaxID)
	} else {
		rows, err = tx.QueryContext(ctx, query, from.MaxID)
	}
	if err != nil {
		return pageResult{}, fmt.Errorf("opencode: delta query: %w", err)
	}

	res := pageResult{watermark: from}
	scanErr := iterDeltaPage(ctx, rows, hasTU, &res, onRow)
	_ = rows.Close()
	if scanErr != nil {
		return res, scanErr
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("opencode: iterate delta rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("opencode: commit ro tx: %w", err)
	}
	return res, nil
}

// iterDeltaPage walks one page's rows, delegating each row's column scan to
// onRow and advancing the page watermark from the (id, time_updated) onRow
// reports. On an old schema (hasTU false) only id advances; time_updated stays 0.
func iterDeltaPage(ctx context.Context, rows *sql.Rows, hasTU bool, res *pageResult, onRow func(rows *sql.Rows) (rowKey, error)) error {
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, err := onRow(rows)
		if err != nil {
			return err
		}
		res.n++
		if hasTU {
			res.watermark.MaxTimeUpdatedMs = key.timeUpdatedMs
		}
		res.watermark.MaxID = key.id
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
// part table denormalizes session_id (adapter-opencode.md §"part"), so the
// direct value is used when present. On a hypothetical old schema where the part
// table lacks session_id, the owner is resolved via an indexed lookup on the
// message PK (message_id → session_id), consulting the already-fetched message
// deltas first to avoid the query. msgSession maps the message ids seen in this
// cycle's message delta to their session id.
func resolvePartSession(ctx context.Context, db *sql.DB, hasSessionID bool, p partRow, msgSession map[string]string) (string, error) {
	if hasSessionID && p.SessionID != "" {
		return p.SessionID, nil
	}
	if sid, ok := msgSession[p.MessageID]; ok {
		return sid, nil
	}
	if p.MessageID == "" {
		return "", nil
	}
	var sid sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT session_id FROM message WHERE id = ?`, p.MessageID).Scan(&sid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("opencode: resolve part %s session: %w", p.ID, err)
	}
	return sid.String, nil
}
