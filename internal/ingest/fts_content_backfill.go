// BackfillFTSContent (SOW-0091) — one-shot rebuild of the fts_content
// FTS5 table that backs prompt/response full-text search. Iterates every
// op with payload_refs, reads the primary payload, runs
// extract.ReadableText, and INSERTs into fts_content.
//
// Idempotent: wipes fts_content in a short transaction before streaming
// so a re-run starts clean. One broken payload_ref never aborts the
// batch — the broken op gets an empty fts_content row (matching the
// "no readable text" representation). Progress is logged every
// `progressEvery` rows so a 1.7M-row backfill gives the operator
// feedback.
//
// The function does NOT call BackfillFTS (which rebuilds fts_ops /
// fts_logs). Run both via their own subcommands — the operator can run
// `fts-backfill` and `fts-content-backfill` independently.

package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/netdata/ai-viewer/internal/extract"
)

// ftsContentPreviewBytes is the per-payload read cap for fts_content
// indexing. The readable text from a typical codex assistant message
// is 2-3 KB; a long message is 4-8 KB. We cap at 32 KB so the index
// row size stays bounded while the excerpt covers any plausible
// prompt or response.
const ftsContentPreviewBytes = 32 * 1024

// progressEvery is the row count at which the backfill logs a progress
// line. A 1.7M-row backfill at this interval yields ~1,700 log lines —
// noisy but useful for long-running operations.
const progressEvery = 10_000

// FTSContentBackfillStats summarizes a one-shot fts_content rebuild.
type FTSContentBackfillStats struct {
	IndexedRows int           // ops with non-empty readable text
	EmptyRows   int           // ops with empty readable text (no payload or non-text payload)
	ErrorRows   int           // ops whose payload read failed
	Elapsed     time.Duration // wall-clock duration
}

// BackfillFTSContent rebuilds fts_content from scratch. It is the one-shot
// backfill for the SOW-0091 full-text content search index; the
// incremental ingest path (writer.refreshFTS) populates the same rows
// for newly-ingested ops.
func BackfillFTSContent(ctx context.Context, db *sql.DB, logger *slog.Logger) (FTSContentBackfillStats, error) {
	start := time.Now()

	// Wipe fts_content in its own committed transaction before streaming,
	// so the rebuild starts from a clean slate and the per-batch
	// transactions never contend with the delete on the single writer
	// connection.
	if err := truncateFTSContent(ctx, db); err != nil {
		return FTSContentBackfillStats{}, err
	}

	stats, err := backfillFTSContent(ctx, db, logger)
	if err != nil {
		return FTSContentBackfillStats{}, err
	}
	stats.Elapsed = time.Since(start)
	logger.Info("fts-content-backfill: rebuilt content index",
		"indexed_rows", stats.IndexedRows,
		"empty_rows", stats.EmptyRows,
		"error_rows", stats.ErrorRows,
		"elapsed", stats.Elapsed.String())
	return stats, nil
}

// truncateFTSContent empties fts_content so the rebuild starts from a
// clean slate (idempotency). Mirrors truncateFTS in fts_backfill.go.
func truncateFTSContent(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("fts-content-backfill: begin truncate tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM fts_content`); err != nil {
		return fmt.Errorf("fts-content-backfill: clear fts_content: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("fts-content-backfill: commit truncate: %w", err)
	}
	return nil
}

// backfillFTSContent streams every op with payload_refs into fts_content.
// We only consider ops whose payload_refs[0] exists (every meaningful
// op has at least one payload; ops without refs are typically compaction
// events). The read step is per-op and may fail; failures count as
// ErrorRows and produce no fts_content row (the op simply won't match
// in /api/search).
func backfillFTSContent(ctx context.Context, db *sql.DB, logger *slog.Logger) (FTSContentBackfillStats, error) {
	const opBatch = 1000
	stats := FTSContentBackfillStats{}

	cursor := ""
	for {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		default:
		}

		rows, err := loadOpsWithPrimaryPayload(ctx, db, cursor, opBatch)
		if err != nil {
			return stats, fmt.Errorf("fts-content-backfill: load ops: %w", err)
		}
		if len(rows) == 0 {
			return stats, nil
		}

		batchIndexed, batchEmpty, batchErr := 0, 0, 0
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return stats, fmt.Errorf("fts-content-backfill: begin tx: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO fts_content (text, op_id, session_id, turn_id) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return stats, fmt.Errorf("fts-content-backfill: prepare insert: %w", err)
		}
		defer func() { _ = stmt.Close() }()

		for _, r := range rows {
			// Only DELETE-then-INSERT (no UPSERT for FTS5) — but since we
			// truncated at start AND we process each op exactly once in
			// this run, a plain INSERT is safe. Defensive DELETE first
			// would be belt-and-braces; skip for performance.
			text := extract.ReadableTextFromRef(r.locationURI, r.compression, ftsContentPreviewBytes)
			switch {
			case text == "" && r.locationURI == "":
				batchEmpty++
			case text == "":
				batchEmpty++
			default:
				batchIndexed++
			}
			if _, err := stmt.ExecContext(ctx, text, r.opID, r.sessionID, r.turnID); err != nil {
				return stats, fmt.Errorf("fts-content-backfill: insert op %s: %w", r.opID, err)
			}
		}

		if err := tx.Commit(); err != nil {
			return stats, fmt.Errorf("fts-content-backfill: commit batch: %w", err)
		}
		committed = true

		stats.IndexedRows += batchIndexed
		stats.EmptyRows += batchEmpty
		stats.ErrorRows += batchErr
		if stats.IndexedRows%progressEvery < batchIndexed {
			logger.Info("fts-content-backfill: progress",
				"indexed_rows", stats.IndexedRows,
				"empty_rows", stats.EmptyRows,
				"error_rows", stats.ErrorRows)
		}

		cursor = rows[len(rows)-1].opID
		if len(rows) < opBatch {
			return stats, nil
		}
	}
}

// ftsContentOpRow is one row of the keyset page query: op_id, session_id,
// turn_id, location_uri, compression for the op's primary payload_ref.
type ftsContentOpRow struct {
	opID        string
	sessionID   string
	turnID      string
	locationURI string
	compression string
}

// loadOpsWithPrimaryPayload reads one keyset page of ops with their primary
// payload_ref metadata. INNER JOIN ensures we only get ops that HAVE a
// primary payload_ref (the typical case for every meaningful op).
const ftsContentOpsQuery = `
SELECT o.id, o.session_id, o.turn_id, pr.location_uri, COALESCE(pr.compression, '')
FROM ops o
INNER JOIN payload_refs pr ON pr.id = (
    SELECT id FROM payload_refs
    WHERE op_id = o.id
    ORDER BY seq ASC
    LIMIT 1
)
WHERE o.id > ?
ORDER BY o.id ASC
LIMIT ?`

func loadOpsWithPrimaryPayload(ctx context.Context, db *sql.DB, cursor string, limit int) ([]ftsContentOpRow, error) {
	rows, err := db.QueryContext(ctx, ftsContentOpsQuery, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("query ops with payload_refs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ftsContentOpRow
	for rows.Next() {
		var r ftsContentOpRow
		if err := rows.Scan(&r.opID, &r.sessionID, &r.turnID, &r.locationURI, &r.compression); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
