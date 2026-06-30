package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// composeErrorText joins an op's error_class and error_message into the single
// fts_ops.error_text column, separated by one space and skipping empty parts
// (both empty → ""). It is the SHARED composition both the incremental refresh
// (refreshFTS) and the one-shot rebuild (BackfillFTS) use, so the two paths can
// never disagree on what an op's indexed error text is — the property the FTS
// parity gate asserts byte-identical. NULL text columns arrive here as "".
func composeErrorText(errorClass, errorMessage string) string {
	parts := make([]string, 0, 2)
	if errorClass != "" {
		parts = append(parts, errorClass)
	}
	if errorMessage != "" {
		parts = append(parts, errorMessage)
	}
	return strings.Join(parts, " ")
}

// ftsOpsSelect reads one op's FINAL persisted searchable columns for fts_ops.
// error_class/error_message are coalesced to "" (NULL text → empty) so
// composeErrorText sees plain strings; model/provider/tool_namespace are
// coalesced for the same reason. The column order matches the Scan in
// refreshFTS and the bulk reader in BackfillFTS so both index identical text.
const ftsOpsSelect = `
SELECT rowid,
       name,
       IFNULL(model, ''), IFNULL(provider, ''), IFNULL(tool_namespace, ''),
       IFNULL(error_class, ''), IFNULL(error_message, ''),
       session_id
FROM ops WHERE id = ?`

// ftsOpsInsert writes one fts_ops row keyed by ops.rowid. fts_ops is
// CONTENT-OWNING (migration 0006); op_id/session_id are UNINDEXED linkage
// columns GET /api/search reads back without a join.
const ftsOpsInsert = `
INSERT INTO fts_ops (rowid, name, model, provider, tool_namespace, error_text, op_id, session_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// refreshFTS rebuilds fts_ops for exactly the ops this batch wrote, inside the
// caller's batch transaction (so the FTS index commits atomically with the ops
// it indexes — mirroring refreshRollups). It is a post-apply-loop hook called
// from worker.flush right after refreshRollups; the dirty-op set is complete by
// then.
//
// Each dirty op is DELETE-by-rowid then INSERT against ops.rowid.
// Reading the FINAL persisted row means a started-and-finalized-in-one-batch op
// indexes its error text correctly, and a re-emitted/refinalized op never
// accumulates duplicate fts_ops rows. fts_logs is NOT handled here — logs are
// append-only and indexed inline in applyLogEntry.
//
// It must produce a byte-identical fts_ops row-set (by logical columns) to
// BackfillFTS over the same data — the FTS parity gate asserts this — so it
// shares composeErrorText and the same ftsOpsSelect column shape.
//
// Single-writer discipline (store.OpenWriter pins SetMaxOpenConns(1)): each op's
// row is fully read into locals BEFORE its DELETE+INSERT, so a read cursor never
// straddles a write on the one pinned connection. (QueryRowContext drains its
// single row immediately, so the per-op read closes before the writes.)
func (w *writer) refreshFTS(ctx context.Context, tx *sql.Tx) error {
	if len(w.dirtyOpIDs) == 0 {
		return nil
	}
	for opID := range w.dirtyOpIDs {
		if err := w.reindexOp(ctx, tx, opID); err != nil {
			return fmt.Errorf("fts-refresh: reindex op %s: %w", opID, err)
		}
	}
	return nil
}

// reindexOp DELETE-then-INSERTs one op's fts_ops row from its FINAL persisted
// columns, keyed by ops.rowid. An op id with no surviving ops row (e.g. an
// orphan OpFinalized whose OpStarted never landed) yields sql.ErrNoRows on the
// read and no new row is inserted, which is correct (a non-existent op has no
// searchable text).
func (w *writer) reindexOp(ctx context.Context, tx *sql.Tx, opID string) error {
	var (
		rowID                         int64
		name, model, provider, toolNS string
		errorClass, errorMessage      string
		sessionID                     string
	)
	readErr := tx.QueryRowContext(ctx, ftsOpsSelect, opID).
		Scan(&rowID, &name, &model, &provider, &toolNS, &errorClass, &errorMessage, &sessionID)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return fmt.Errorf("read op fts columns: %w", readErr)
	}
	if errors.Is(readErr, sql.ErrNoRows) {
		return nil // no op row → nothing to index (orphan finalize).
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_ops WHERE rowid = ?`, rowID); err != nil {
		return fmt.Errorf("delete fts_ops row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, ftsOpsInsert,
		rowID, name, model, provider, toolNS, composeErrorText(errorClass, errorMessage), opID, sessionID,
	); err != nil {
		return fmt.Errorf("insert fts_ops row: %w", err)
	}
	return nil
}
