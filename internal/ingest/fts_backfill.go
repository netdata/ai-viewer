package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// FTSBackfillStats summarizes a one-shot FTS rebuild for logging + test
// assertions. Counts are over the rows written this run.
type FTSBackfillStats struct {
	OpRows  int           // fts_ops rows written (every op)
	LogRows int           // fts_logs rows written (logs of fts5_index_logs=1 sources)
	Elapsed time.Duration // wall-clock duration of the backfill
}

// BackfillFTS rebuilds fts_ops + fts_logs from scratch over the existing
// ops/log_entries rows. It is the one-shot sibling of BackfillRollups
// (ingester.md §"One-shot backfill" — the one-shot builds the FTS index too):
// the rollups-backfill subcommand calls it after BackfillRollups.
//
// It wipes both FTS tables then bulk-inserts: EVERY op into fts_ops (fts_ops is
// always indexed), and logs into fts_logs ONLY for sources whose
// fts5_index_logs=1 (data-model.md §Full-text search). It is idempotent /
// re-runnable (the leading DELETEs make a second run produce the same tables),
// and uses the SAME composeErrorText composition as the incremental refresh so
// the two paths' fts_ops rows are byte-identical by logical column (the FTS
// parity gate asserts this).
//
// Single-writer discipline (store.OpenWriter pins SetMaxOpenConns(1)): each
// source read is FULLY drained into a slice BEFORE any write, so a read cursor
// never straddles a write on the one pinned connection. The whole rebuild runs
// in one transaction so a crash can never leave the index half-built.
func BackfillFTS(ctx context.Context, db *sql.DB, logger *slog.Logger) (FTSBackfillStats, error) {
	start := time.Now()

	// Drain both reads into slices BEFORE opening the write transaction, so no
	// read cursor is live across a write on the single pinned connection.
	ops, err := loadAllOpsForFTS(ctx, db)
	if err != nil {
		return FTSBackfillStats{}, err
	}
	logs, err := loadIndexableLogsForFTS(ctx, db)
	if err != nil {
		return FTSBackfillStats{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return FTSBackfillStats{}, fmt.Errorf("fts-backfill: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_ops`); err != nil {
		return FTSBackfillStats{}, fmt.Errorf("fts-backfill: clear fts_ops: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_logs`); err != nil {
		return FTSBackfillStats{}, fmt.Errorf("fts-backfill: clear fts_logs: %w", err)
	}

	if err := insertFTSOps(ctx, tx, ops); err != nil {
		return FTSBackfillStats{}, err
	}
	if err := insertFTSLogs(ctx, tx, logs); err != nil {
		return FTSBackfillStats{}, err
	}

	if err := tx.Commit(); err != nil {
		return FTSBackfillStats{}, fmt.Errorf("fts-backfill: commit: %w", err)
	}
	committed = true

	stats := FTSBackfillStats{OpRows: len(ops), LogRows: len(logs), Elapsed: time.Since(start)}
	logger.Info("fts-backfill: rebuilt full-text index",
		"fts_ops_rows", stats.OpRows, "fts_logs_rows", stats.LogRows, "elapsed", stats.Elapsed.String())
	return stats, nil
}

// ftsOpRow is one op's fts_ops payload, read from the ops table.
type ftsOpRow struct {
	name, model, provider, toolNS string
	errorText                     string
	opID, sessionID               string
}

// allOpsForFTSQuery reads every op's searchable columns. error_class/
// error_message are coalesced to "" so composeErrorText sees plain strings
// (mirroring ftsOpsSelect in the incremental path). Ordered by id for a
// deterministic insert order (cosmetic — the parity gate compares logical
// columns, not internal docids).
const allOpsForFTSQuery = `
SELECT id, name,
       IFNULL(model, ''), IFNULL(provider, ''), IFNULL(tool_namespace, ''),
       IFNULL(error_class, ''), IFNULL(error_message, ''),
       session_id
FROM ops
ORDER BY id ASC`

// loadAllOpsForFTS reads every op into a slice, fully draining the cursor before
// the caller writes (single-connection discipline). error_text is composed here
// with the SAME helper the incremental path uses.
func loadAllOpsForFTS(ctx context.Context, db *sql.DB) ([]ftsOpRow, error) {
	rows, err := db.QueryContext(ctx, allOpsForFTSQuery)
	if err != nil {
		return nil, fmt.Errorf("fts-backfill: query ops: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ftsOpRow
	for rows.Next() {
		var (
			r                        ftsOpRow
			errorClass, errorMessage string
		)
		if err := rows.Scan(&r.opID, &r.name, &r.model, &r.provider, &r.toolNS,
			&errorClass, &errorMessage, &r.sessionID); err != nil {
			return nil, fmt.Errorf("fts-backfill: scan op: %w", err)
		}
		r.errorText = composeErrorText(errorClass, errorMessage)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fts-backfill: iterate ops: %w", err)
	}
	return out, nil
}

// insertFTSOps bulk-inserts every op into the (already-cleared) fts_ops table.
// A prepared statement amortizes the per-row cost.
func insertFTSOps(ctx context.Context, tx *sql.Tx, ops []ftsOpRow) error {
	if len(ops) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, ftsOpsInsert)
	if err != nil {
		return fmt.Errorf("fts-backfill: prepare fts_ops insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range ops {
		r := &ops[i]
		if _, err := stmt.ExecContext(ctx,
			r.name, r.model, r.provider, r.toolNS, r.errorText, r.opID, r.sessionID,
		); err != nil {
			return fmt.Errorf("fts-backfill: insert fts_ops row (op %s): %w", r.opID, err)
		}
	}
	return nil
}

// ftsLogRow is one log's fts_logs payload, read from log_entries.
type ftsLogRow struct {
	logID     int64
	message   string
	sessionID sql.NullString
	opID      sql.NullString
	severity  string
	ts        int64
}

// indexableLogsForFTSQuery reads exactly the log_entries the INCREMENTAL path
// indexes, so the backfill is byte-identical to it (the parity gate's premise).
//
// Only applyLogEntry (the LogEntryEvent path) inserts into fts_logs, and it
// always writes a SESSION-scoped row (session_id set, source_id NULL). The other
// three log_entries writers — pricing-miss WRN, applySourceError, orphan
// payload_ref — write SOURCE-scoped rows (session_id NULL, source_id set) and do
// NOT touch fts_logs. So the indexable set is precisely the session-scoped logs
// (le.session_id IS NOT NULL) whose OWNING source has fts5_index_logs=1, resolved
// through the session. Indexing source-scoped rows here would add fts_logs rows
// the incremental path never creates and break parity. The per-source flag the
// incremental path reads (worker.fts5IndexLogs) is the same value
// ensureSourceRow persists into sources.fts5_index_logs, so both gate on the
// same boolean.
const indexableLogsForFTSQuery = `
SELECT le.id, le.message, le.session_id, le.op_id, le.severity, le.ts
FROM log_entries le
JOIN sessions s ON le.session_id = s.id
JOIN sources  src ON s.source_id = src.id
WHERE le.session_id IS NOT NULL AND src.fts5_index_logs = 1
ORDER BY le.id ASC`

// loadIndexableLogsForFTS reads every fts5_index_logs=1 source's logs into a
// slice, fully draining the cursor before the caller writes.
func loadIndexableLogsForFTS(ctx context.Context, db *sql.DB) ([]ftsLogRow, error) {
	rows, err := db.QueryContext(ctx, indexableLogsForFTSQuery)
	if err != nil {
		return nil, fmt.Errorf("fts-backfill: query logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ftsLogRow
	for rows.Next() {
		var r ftsLogRow
		if err := rows.Scan(&r.logID, &r.message, &r.sessionID, &r.opID, &r.severity, &r.ts); err != nil {
			return nil, fmt.Errorf("fts-backfill: scan log: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fts-backfill: iterate logs: %w", err)
	}
	return out, nil
}

// ftsLogsInsert writes one fts_logs row (content-owning FTS5 → plain INSERT).
// The UNINDEXED columns mirror the incremental insert in applyLogEntry, so a
// matched log indexes identically on both paths.
const ftsLogsInsert = `
INSERT INTO fts_logs (message, log_id, session_id, op_id, severity, ts)
VALUES (?, ?, ?, ?, ?, ?)`

// insertFTSLogs bulk-inserts the indexable logs into the (already-cleared)
// fts_logs table.
func insertFTSLogs(ctx context.Context, tx *sql.Tx, logs []ftsLogRow) error {
	if len(logs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, ftsLogsInsert)
	if err != nil {
		return fmt.Errorf("fts-backfill: prepare fts_logs insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range logs {
		r := &logs[i]
		if _, err := stmt.ExecContext(ctx,
			r.message, r.logID, r.sessionID, r.opID, r.severity, r.ts,
		); err != nil {
			return fmt.Errorf("fts-backfill: insert fts_logs row (log %d): %w", r.logID, err)
		}
	}
	return nil
}
