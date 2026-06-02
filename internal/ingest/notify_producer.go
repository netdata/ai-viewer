package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// notifyRetention bounds how long a notify row survives. The ingester
// prunes rows older than this once per flush cycle so the change-log
// table stays small — the rows are disposable transport, not history.
// The serve poller keeps its cursor in memory and jumps to MAX(seq) on
// startup (it only delivers changes that happen while a client is
// connected; clients reconcile historical state through the REST API),
// so pruning consumed rows is always safe. Five minutes comfortably
// covers a serve restart / brief poller stall without unbounded growth.
// See data-model.md §notify and ingester.md §Notify Channel.
const notifyRetention = 5 * time.Minute

// emitNotify appends the batch's change-log rows to the notify table
// INSIDE the caller's batch *sql.Tx, so they commit atomically with the
// canonical rows they describe — the serve poller can never observe a
// notify row before the data it refers to is visible. On rollback the
// rows vanish with the rest of the batch. tsUS is the batch commit time
// in UNIX-microseconds; it stamps every row so all of a batch's
// notifications share one timestamp.
//
// Producer rules (ingester.md §Notify Channel):
//   - one session_changed row per canonical session id in
//     affectedSessionIDs, carrying that session's root_session_id (read
//     back from the sessions row written earlier in this same tx — the
//     authoritative post-batch value, which the resolver may have
//     rewritten) and tsUS;
//   - at most one stats_invalidated row per batch, emitted when the batch
//     changed any rollup. Catalog rollups (providers, models, tools,
//     agents, cwds) derive entirely from session/op writes, so a non-empty
//     affectedSessionIDs signals they changed; the time-bucketed
//     rollup_hourly/rollup_daily tables change when this batch marked a
//     rollup input (rollupTouchedThisBatch) OR this refresh materialized a
//     carried bucket (rollupMaterializedThisRefresh) — NOT merely because a
//     carried-open bucket is still pending in dirtyRollupBuckets. The row
//     fires on the union (ingester.md §"Incremental rollup refresh");
//   - one source_status_changed row when the batch changed the source's
//     parse_errors count or enabled flag (tracked via
//     writer.sourceStatusChanged, set in bumpSourceErrorCounter).
func (w *writer) emitNotify(ctx context.Context, tx *sql.Tx, tsUS int64) error {
	// One session_changed per affected session, with its current root.
	for id := range w.affectedSessionIDs {
		rootID, err := w.lookupRootSessionID(ctx, tx, id)
		if err != nil {
			// The row is guaranteed written by this point in the tx; a
			// miss means the database is unhealthy. Surfacing the error
			// rolls back the whole batch rather than silently dropping a
			// notification (no silent failures).
			return fmt.Errorf("ingest notify: lookup root for session %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO notify (ts_us, kind, session_id, root_session_id)
VALUES (?, 'session_changed', ?, ?)
`, tsUS, id, rootID); err != nil {
			return fmt.Errorf("ingest notify: insert session_changed: %w", err)
		}
	}

	// At most one stats_invalidated per batch, when rollups changed. The
	// catalog rollups derive from session/op writes (non-empty
	// affectedSessionIDs), and the time-bucketed rollup_hourly/rollup_daily
	// tables change when THIS batch marked a rollup input
	// (rollupTouchedThisBatch) OR this refresh materialized a (carried) bucket
	// (rollupMaterializedThisRefresh). We do NOT key off len(dirtyRollupBuckets):
	// under carry-forward that set is non-empty whenever an open bucket is merely
	// pending, which would fire stats_invalidated on every batch. The per-batch
	// signals preserve the original semantics — fire when the batch actually
	// touched/materialized a rollup, not when one is still pending. Fire on the
	// union — still at most one row per batch (round-7 P1b).
	if len(w.affectedSessionIDs) > 0 || w.rollupTouchedThisBatch || w.rollupMaterializedThisRefresh {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO notify (ts_us, kind) VALUES (?, 'stats_invalidated')
`, tsUS); err != nil {
			return fmt.Errorf("ingest notify: insert stats_invalidated: %w", err)
		}
	}

	// One source_status_changed per batch when the source's status moved.
	if w.sourceStatusChanged {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO notify (ts_us, kind, source_id) VALUES (?, 'source_status_changed', ?)
`, tsUS, w.sourceID); err != nil {
			return fmt.Errorf("ingest notify: insert source_status_changed: %w", err)
		}
	}
	return nil
}

// lookupRootSessionID returns the root_session_id of the sessions row
// identified by canonical id. The row is guaranteed present within the
// caller's tx (every affected session was written before emitNotify
// runs). sql.ErrNoRows is therefore an integrity failure, not an
// expected miss, and is returned to the caller.
func (w *writer) lookupRootSessionID(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var root string
	if err := tx.QueryRowContext(ctx,
		`SELECT root_session_id FROM sessions WHERE id = ?`, id).Scan(&root); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("sessions row %s absent in batch tx: %w", id, err)
		}
		return "", err
	}
	return root, nil
}

// pruneNotify deletes notify rows older than notifyRetention relative to
// nowUS (UNIX-microseconds). Runs once per flush inside the batch tx so
// the prune commits with the batch and remains the ingester's write (the
// serve process is read-only against notify). The delete is a bounded
// range over a table the ingester keeps small, so the cost is negligible.
func pruneNotify(ctx context.Context, tx *sql.Tx, nowUS int64) error {
	cutoff := nowUS - notifyRetention.Microseconds()
	if _, err := tx.ExecContext(ctx, `DELETE FROM notify WHERE ts_us < ?`, cutoff); err != nil {
		return fmt.Errorf("ingest notify: prune: %w", err)
	}
	return nil
}
