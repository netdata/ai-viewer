package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type repairYieldFunc func(context.Context) error

// ftsBackfillBatchSize bounds the one-shot/source-scoped FTS rebuild's
// per-batch memory and writer transaction size. A var (not const) so tests can
// force keyset-boundary crossings; mirrors defaultBatchSize so FTS repair cannot
// monopolize the single SQLite writer longer than normal ingest batches.
var ftsBackfillBatchSize = defaultBatchSize

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
// re-runnable (truncateFTS makes a second run produce the same tables), and
// uses the SAME composeErrorText composition as the incremental refresh so the
// two paths' fts_ops rows are byte-identical by logical column (the FTS parity
// gate asserts this).
//
// Memory stays bounded to one batch (ftsBackfillBatchSize rows) regardless of
// install size: the work is streamed in keyset batches, and each batch's rows
// are read and written inside that batch's OWN transaction. The leading wipe
// runs in truncateFTS's own committed transaction first. This mirrors
// BackfillRollups exactly and is required by the single-writer discipline —
// store.OpenWriter pins SetMaxOpenConns(1), so a read cursor must be FULLY
// drained into a slice before any write on that one connection; a cursor must
// never straddle a write. It deliberately trades single-transaction atomicity
// for bounded memory: a crash mid-rebuild leaves a partial index that the
// idempotent re-run (truncateFTS first) repairs — identical to what
// BackfillRollups already does (ingester.md §"One-shot backfill").
func BackfillFTS(ctx context.Context, db *sql.DB, logger *slog.Logger) (FTSBackfillStats, error) {
	return backfillFTSWithYield(ctx, db, logger, nil)
}

func backfillFTSWithYield(ctx context.Context, db *sql.DB, logger *slog.Logger, yield repairYieldFunc) (FTSBackfillStats, error) {
	start := time.Now()

	// Wipe both FTS tables in their own committed transaction before streaming,
	// so the rebuild starts from a clean slate and the per-batch transactions
	// below never contend with the delete on the single writer connection.
	if err := callRepairYield(ctx, yield); err != nil {
		return FTSBackfillStats{}, err
	}
	if err := truncateFTS(ctx, db); err != nil {
		return FTSBackfillStats{}, err
	}

	opRows, err := backfillFTSOps(ctx, db, yield)
	if err != nil {
		return FTSBackfillStats{}, err
	}
	logRows, err := backfillFTSLogs(ctx, db, yield)
	if err != nil {
		return FTSBackfillStats{}, err
	}

	stats := FTSBackfillStats{OpRows: opRows, LogRows: logRows, Elapsed: time.Since(start)}
	logger.Info("fts-backfill: rebuilt full-text index",
		"fts_ops_rows", stats.OpRows, "fts_logs_rows", stats.LogRows, "elapsed", stats.Elapsed.String())
	return stats, nil
}

// truncateFTS empties fts_ops and fts_logs in one short transaction so
// BackfillFTS starts every run from a clean slate (making it idempotent /
// re-runnable, the recovery path for a missing or half-built index). The
// DELETEs are constant SQL (fixed table names, no parameters, no user input);
// the commit drains before the per-batch streaming opens its own transactions
// (mirrors truncateRollups in rollup_backfill.go).
func truncateFTS(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-backfill: begin truncate tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_ops`); err != nil {
		return fmt.Errorf("fts-backfill: clear fts_ops: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_logs`); err != nil {
		return fmt.Errorf("fts-backfill: clear fts_logs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-backfill: commit truncate: %w", err)
	}
	return nil
}

// ftsOpRow is one op's fts_ops payload, read from the ops table.
type ftsOpRow struct {
	rowID                         int64
	name, model, provider, toolNS string
	errorText                     string
	opID, sessionID               string
}

// allOpsForFTSQuery reads one keyset page of ops' searchable columns. error_class/
// error_message are coalesced to "" so composeErrorText sees plain strings
// (mirroring ftsOpsSelect in the incremental path). The `ORDER BY id ASC` is
// LOAD-BEARING (not cosmetic): the keyset stream depends on a stable total order
// over ops.id (TEXT), and `WHERE id > ?` advances the cursor past the last row
// of the previous page.
const allOpsForFTSQuery = `
SELECT rowid, id, name,
       IFNULL(model, ''), IFNULL(provider, ''), IFNULL(tool_namespace, ''),
       IFNULL(error_class, ''), IFNULL(error_message, ''),
       session_id
FROM ops
WHERE id > ?
ORDER BY id ASC
LIMIT ?`

const sourceSessionsForRepairQuery = `
SELECT id
FROM sessions
WHERE source_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?`

const sessionOpsForFTSQuery = `
SELECT o.rowid, o.id, o.name,
       IFNULL(o.model, ''), IFNULL(o.provider, ''), IFNULL(o.tool_namespace, ''),
       IFNULL(o.error_class, ''), IFNULL(o.error_message, ''),
       o.session_id
FROM ops o
WHERE o.session_id = ?
  AND o.rowid > ?
ORDER BY o.rowid ASC
LIMIT ?`

// backfillFTSOps streams every op into fts_ops in keyset batches.
// For each batch: open a transaction, read the next <=ftsBackfillBatchSize ops
// (cursor FULLY drained into a slice before any write — single-writer
// discipline), insert that slice in the SAME transaction, commit, then advance
// the TEXT cursor past the batch's last id. Returns the total rows written.
func backfillFTSOps(ctx context.Context, db *sql.DB, yield repairYieldFunc) (int, error) {
	cursor := "" // ops.id is TEXT; "" is below every id, so the first page starts at the lowest id.
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		batch, err := loadOpsBatch(ctx, db, cursor)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		if err := insertFTSOpsBatch(ctx, db, batch); err != nil {
			return 0, err
		}
		total += len(batch)
		cursor = batch[len(batch)-1].opID // last id of this page becomes the next page's exclusive lower bound.
		if len(batch) < ftsBackfillBatchSize {
			return total, nil // short page => last page; nothing past it.
		}
	}
}

func repairSourceFTSOps(ctx context.Context, db *sql.DB, sourceID string, yield repairYieldFunc) (int, error) {
	sessionCursor := ""
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		sessionIDs, err := loadSourceSessionIDBatch(ctx, db, sourceID, sessionCursor)
		if err != nil {
			return 0, err
		}
		if len(sessionIDs) == 0 {
			return total, nil
		}
		for _, sessionID := range sessionIDs {
			n, err := repairSessionFTSOps(ctx, db, sessionID, yield)
			if err != nil {
				return 0, err
			}
			total += n
		}
		sessionCursor = sessionIDs[len(sessionIDs)-1]
		if len(sessionIDs) < ftsBackfillBatchSize {
			return total, nil
		}
	}
}

func callRepairYield(ctx context.Context, yield repairYieldFunc) error {
	if yield == nil {
		return nil
	}
	return yield(ctx)
}

// loadOpsBatch reads one keyset page of ops (id > cursor, ordered by id) inside
// its own transaction, fully draining the cursor before the caller writes
// (single-connection discipline). error_text is composed here with the SAME
// helper the incremental path uses.
func loadOpsBatch(ctx context.Context, db *sql.DB, cursor string) ([]ftsOpRow, error) {
	return loadFTSOpsBatch(ctx, db, allOpsForFTSQuery, cursor, ftsBackfillBatchSize)
}

func loadSourceSessionIDBatch(ctx context.Context, db *sql.DB, sourceID, cursor string) ([]string, error) {
	rows, err := db.QueryContext(ctx, sourceSessionsForRepairQuery, sourceID, cursor, ftsBackfillBatchSize)
	if err != nil {
		return nil, fmt.Errorf("fts-repair: query source sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]string, 0, ftsBackfillBatchSize)
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("fts-repair: scan source session: %w", err)
		}
		out = append(out, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fts-repair: iterate source sessions: %w", err)
	}
	return out, nil
}

func repairSessionFTSOps(ctx context.Context, db *sql.DB, sessionID string, yield repairYieldFunc) (int, error) {
	var cursor int64
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		batch, err := loadSessionOpsBatch(ctx, db, sessionID, cursor)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		if err := repairFTSOpsBatch(ctx, db, batch); err != nil {
			return 0, err
		}
		total += len(batch)
		cursor = batch[len(batch)-1].rowID
		if len(batch) < ftsBackfillBatchSize {
			return total, nil
		}
	}
}

func loadSessionOpsBatch(ctx context.Context, db *sql.DB, sessionID string, cursor int64) ([]ftsOpRow, error) {
	return loadFTSOpsBatch(ctx, db, sessionOpsForFTSQuery, sessionID, cursor, ftsBackfillBatchSize)
}

func loadFTSOpsBatch(ctx context.Context, db *sql.DB, query string, args ...any) ([]ftsOpRow, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("fts-backfill: begin ops read tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, query, args...)
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
		if err := rows.Scan(&r.rowID, &r.opID, &r.name, &r.model, &r.provider, &r.toolNS,
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

// insertFTSOpsBatch inserts one keyset page of ops into the (already-cleared)
// fts_ops table inside its own transaction. A per-batch prepared statement
// amortizes the per-row cost (mirrors upsertRollups preparing per call).
func insertFTSOpsBatch(ctx context.Context, db *sql.DB, ops []ftsOpRow) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-backfill: begin ops write tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, ftsOpsInsert)
	if err != nil {
		return fmt.Errorf("fts-backfill: prepare fts_ops insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range ops {
		r := &ops[i]
		if _, err := stmt.ExecContext(ctx,
			r.rowID, r.name, r.model, r.provider, r.toolNS, r.errorText, r.opID, r.sessionID,
		); err != nil {
			return fmt.Errorf("fts-backfill: insert fts_ops row (op %s): %w", r.opID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-backfill: commit fts_ops batch: %w", err)
	}
	committed = true
	return nil
}

func repairFTSOpsBatch(ctx context.Context, db *sql.DB, ops []ftsOpRow) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-repair: begin ops write tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, ftsOpsInsert)
	if err != nil {
		return fmt.Errorf("fts-repair: prepare fts_ops insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range ops {
		r := &ops[i]
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts_ops WHERE rowid = ?`, r.rowID); err != nil {
			return fmt.Errorf("fts-repair: delete fts_ops row (op %s): %w", r.opID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			r.rowID, r.name, r.model, r.provider, r.toolNS, r.errorText, r.opID, r.sessionID,
		); err != nil {
			return fmt.Errorf("fts-repair: insert fts_ops row (op %s): %w", r.opID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-repair: commit fts_ops batch: %w", err)
	}
	committed = true
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

// indexableLogsForFTSQuery reads one keyset page of exactly the log_entries the
// INCREMENTAL path indexes, so the backfill is byte-identical to it (the parity
// gate's premise).
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
//
// The `ORDER BY le.id ASC` is LOAD-BEARING (the keyset stream depends on a stable
// total order over the INTEGER le.id); `AND le.id > ?` advances the cursor past
// the previous page's last id. The join + fts5_index_logs filter is UNCHANGED —
// only the keyset bound + LIMIT are added — so the indexed row set is identical
// to the incremental path's (the parity property).
const indexableLogsForFTSQuery = `
SELECT le.id, le.message, le.session_id, le.op_id, le.severity, le.ts
FROM log_entries le
JOIN sessions s ON le.session_id = s.id
JOIN sources  src ON s.source_id = src.id
WHERE le.session_id IS NOT NULL AND src.fts5_index_logs = 1
  AND le.id > ?
ORDER BY le.id ASC
LIMIT ?`

const sessionLogsForFTSQuery = `
SELECT le.id, le.message, le.session_id, le.op_id, le.severity, le.ts
FROM log_entries le
WHERE le.session_id = ?
  AND le.id > ?
ORDER BY le.id ASC
LIMIT ?`

// backfillFTSLogs streams every indexable log into fts_logs in keyset
// batches, mirroring backfillFTSOps. log_entries.id is INTEGER, so the cursor is
// an int64 starting at 0 (below every AUTOINCREMENT id). Returns the total rows
// written.
func backfillFTSLogs(ctx context.Context, db *sql.DB, yield repairYieldFunc) (int, error) {
	var cursor int64 // log_entries.id is INTEGER AUTOINCREMENT (>=1); 0 is below every id.
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		batch, err := loadLogsBatch(ctx, db, cursor)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		if err := insertFTSLogsBatch(ctx, db, batch); err != nil {
			return 0, err
		}
		total += len(batch)
		cursor = batch[len(batch)-1].logID // last id of this page becomes the next page's exclusive lower bound.
		if len(batch) < ftsBackfillBatchSize {
			return total, nil // short page => last page.
		}
	}
}

func repairSourceFTSLogs(ctx context.Context, db *sql.DB, sourceID string, yield repairYieldFunc) (int, error) {
	if err := callRepairYield(ctx, yield); err != nil {
		return 0, err
	}
	enabled, err := sourceFTS5IndexLogsEnabled(ctx, db, sourceID)
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}

	sessionCursor := ""
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		sessionIDs, err := loadSourceSessionIDBatch(ctx, db, sourceID, sessionCursor)
		if err != nil {
			return 0, err
		}
		if len(sessionIDs) == 0 {
			return total, nil
		}
		for _, sessionID := range sessionIDs {
			n, err := repairSessionFTSLogs(ctx, db, sessionID, yield)
			if err != nil {
				return 0, err
			}
			total += n
		}
		sessionCursor = sessionIDs[len(sessionIDs)-1]
		if len(sessionIDs) < ftsBackfillBatchSize {
			return total, nil
		}
	}
}

// loadLogsBatch reads one keyset page of indexable logs (le.id > cursor, ordered
// by le.id) inside its own transaction, fully draining the cursor before the
// caller writes.
func loadLogsBatch(ctx context.Context, db *sql.DB, cursor int64) ([]ftsLogRow, error) {
	return loadFTSLogsBatch(ctx, db, indexableLogsForFTSQuery, cursor, ftsBackfillBatchSize)
}

func sourceFTS5IndexLogsEnabled(ctx context.Context, db *sql.DB, sourceID string) (bool, error) {
	var enabled int
	err := db.QueryRowContext(ctx, `SELECT fts5_index_logs FROM sources WHERE id = ?`, sourceID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fts-repair: load source log-index flag: %w", err)
	}
	return enabled != 0, nil
}

func repairSessionFTSLogs(ctx context.Context, db *sql.DB, sessionID string, yield repairYieldFunc) (int, error) {
	var cursor int64
	total := 0
	for {
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		batch, err := loadSessionLogsBatch(ctx, db, sessionID, cursor)
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		if err := callRepairYield(ctx, yield); err != nil {
			return 0, err
		}
		if err := repairFTSLogsBatch(ctx, db, batch); err != nil {
			return 0, err
		}
		total += len(batch)
		cursor = batch[len(batch)-1].logID
		if len(batch) < ftsBackfillBatchSize {
			return total, nil
		}
	}
}

func loadSessionLogsBatch(ctx context.Context, db *sql.DB, sessionID string, cursor int64) ([]ftsLogRow, error) {
	return loadFTSLogsBatch(ctx, db, sessionLogsForFTSQuery, sessionID, cursor, ftsBackfillBatchSize)
}

func loadFTSLogsBatch(ctx context.Context, db *sql.DB, query string, args ...any) ([]ftsLogRow, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("fts-backfill: begin logs read tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, query, args...)
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

// ftsLogsInsert writes one fts_logs row keyed by log_entries.id.
// The UNINDEXED columns mirror the incremental insert in applyLogEntry, so a
// matched log indexes identically on both paths.
const ftsLogsInsert = `
INSERT INTO fts_logs (rowid, message, log_id, session_id, op_id, severity, ts)
VALUES (?, ?, ?, ?, ?, ?, ?)`

// insertFTSLogsBatch inserts one keyset page of indexable logs into the
// (already-cleared) fts_logs table inside its own transaction. A per-batch
// prepared statement amortizes the per-row cost.
func insertFTSLogsBatch(ctx context.Context, db *sql.DB, logs []ftsLogRow) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-backfill: begin logs write tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, ftsLogsInsert)
	if err != nil {
		return fmt.Errorf("fts-backfill: prepare fts_logs insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range logs {
		r := &logs[i]
		if _, err := stmt.ExecContext(ctx,
			r.logID, r.message, r.logID, r.sessionID, r.opID, r.severity, r.ts,
		); err != nil {
			return fmt.Errorf("fts-backfill: insert fts_logs row (log %d): %w", r.logID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-backfill: commit fts_logs batch: %w", err)
	}
	committed = true
	return nil
}

func repairFTSLogsBatch(ctx context.Context, db *sql.DB, logs []ftsLogRow) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-repair: begin logs write tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, ftsLogsInsert)
	if err != nil {
		return fmt.Errorf("fts-repair: prepare fts_logs insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range logs {
		r := &logs[i]
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts_logs WHERE rowid = ?`, r.logID); err != nil {
			return fmt.Errorf("fts-repair: delete fts_logs row (log %d): %w", r.logID, err)
		}
		if _, err := stmt.ExecContext(ctx,
			r.logID, r.message, r.logID, r.sessionID, r.opID, r.severity, r.ts,
		); err != nil {
			return fmt.Errorf("fts-repair: insert fts_logs row (log %d): %w", r.logID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-repair: commit fts_logs batch: %w", err)
	}
	committed = true
	return nil
}
