package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the BOUNDARY-MILLISECOND re-scan (SOW-0005 round-3 P1-1). It is
// split out of tailer.go / tailer_changes.go to keep each file ≤400 lines.
//
// The problem: an already-seen LOW-id row updated IN PLACE at exactly the cursor's
// boundary millisecond T moves neither MAX(id) (no insert) nor MAX(time_updated)
// (the bucket value is unchanged), and the forward delta's strict tie-break
// (time_updated = T AND id > highID) excludes a low-id row — so it would be skipped
// forever if it were the session's ONLY change. The fix re-scans the FULL boundary
// bucket (every row with time_updated = T, regardless of id) on each gate-open
// probe, collects the owning session ids, and re-emits their trees idempotently.
// The cursor is NOT advanced (the boundary rows are already at the watermark).

// boundarySelect builds the boundary-bucket query for a table: the present-column
// SELECT filtered to a single time_updated value (the cursor's MaxTimeUpdatedMs),
// ordered (time_updated, id) for determinism, with NO LIMIT — the boundary bucket
// is the tiny set of rows sharing one millisecond, never a paged scan. It reuses
// the same present-column projection deltaRowHandler scans, so the existing
// per-table session-derivation closures work unchanged. quoteIdent guards every
// identifier (all from the fixed schema, never operator input).
func boundarySelect(s tableSchema) string {
	if len(s.Present) == 0 {
		// Defensive: introspectAll rejects a table with no readable columns.
		return "SELECT 1 WHERE 0"
	}
	return "SELECT " + presentColsSQL(s) +
		" FROM " + quoteIdent(s.Table) +
		" WHERE time_updated = ?" +
		" ORDER BY time_updated, id"
}

// boundaryAffectedSessions re-scans the boundary millisecond bucket of every
// tracked table whose cursor watermark sits at a non-zero MaxTimeUpdatedMs and
// returns the SET of owning session ids (first-seen order, deduped across tables).
// It catches an in-place UPDATE of an already-seen row at exactly the boundary ms
// (SOW-0005 round-3 P1-1) that the forward delta's strict `> :tuid` tie-break and
// the `MAX(*) >` gates both miss. A table with MaxTimeUpdatedMs == 0 (cold start)
// or without time_updated is skipped — there is no boundary to re-check. ctx
// cancellation aborts promptly.
//
// This is READ-ONLY and does NOT touch the cursor: the boundary rows are already
// AT the watermark, so re-emitting their session trees is the only effect (the
// ingester absorbs the re-emission idempotently). Tables are scanned in
// trackedTables order so the message bucket populates msgSession before the part
// bucket consults it (old-schema part→session resolver).
func boundaryAffectedSessions(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, onError func(error)) ([]string, error) {
	affected := newAffectedSet()
	msgSession := map[string]string{}
	for _, table := range trackedTables {
		s := schema[table]
		if !s.has("time_updated") {
			continue
		}
		ms := cur.Tables[table].MaxTimeUpdatedMs
		if ms == 0 {
			continue // no boundary watermark yet (cold start)
		}
		if err := scanBoundaryBucket(ctx, db, table, s, ms, affected, msgSession, onError); err != nil {
			return affected.ids(), err
		}
	}
	return affected.ids(), nil
}

// scanBoundaryBucket runs one table's boundary-bucket query inside a short
// read-only transaction (the same WAL-friendly snapshot discipline as the forward
// delta pages) and feeds every row through the table's deltaRowHandler so the
// owning session id lands in `affected`. The watermark the handler reports is
// discarded — the cursor is not advanced by a boundary re-scan.
func scanBoundaryBucket(ctx context.Context, db *sql.DB, table string, s tableSchema, ms int64, affected *affectedSet, msgSession map[string]string, onError func(error)) error {
	tx, err := beginRO(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	onRow := deltaRowHandler(ctx, db, table, s, affected, msgSession, onError)
	rows, err := tx.QueryContext(ctx, boundarySelect(s), ms)
	if err != nil {
		return fmt.Errorf("opencode: boundary re-scan %s: %w", table, err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if _, err := onRow(rows); err != nil {
			_ = rows.Close()
			return err
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("opencode: iterate boundary rows %s: %w", table, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("opencode: commit boundary tx %s: %w", table, err)
	}
	return nil
}

// emitBoundarySessions re-loads and emits the boundary-affected sessions' trees
// (idempotent re-emission). It is called from pollOnce on a gate-open probe, in
// ADDITION to the forward delta path: the forward delta advances the cursor for
// genuinely new/`> :tuid` rows, while this catches the same-ms in-place update of
// an already-seen low-id row. A session caught by both is simply re-emitted once
// per path; the ingester's idempotent upserts absorb it. Returns whether any
// boundary session was emitted (so the caller can fold it into the active-cadence
// decision) and any fatal/ctx error.
func emitBoundarySessions(ctx context.Context, db *sql.DB, schema schemaSet, cur Cursor, sourceID string, out chan<- canonical.Event, logger *slog.Logger, onError func(error)) (bool, error) {
	affected, err := boundaryAffectedSessions(ctx, db, schema, cur, onError)
	if err != nil {
		return false, err
	}
	if len(affected) == 0 {
		return false, nil
	}
	if err := reloadAndEmit(ctx, db, schema, sourceID, affected, out, logger, onError); err != nil {
		return false, err
	}
	return true, nil
}
