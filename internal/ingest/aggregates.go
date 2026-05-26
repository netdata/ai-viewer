package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// refreshAggregates re-computes turn and session aggregate columns over
// the dirty set touched by the current batch. The work is bounded to the
// touched IDs so it stays cheap regardless of total row count.
//
// The function is idempotent: re-running on the same dirty set produces
// the same row values because the queries are pure functions of the
// underlying ops/turns rows.
func refreshAggregates(ctx context.Context, tx *sql.Tx, dirtyTurns, dirtySessions map[string]struct{}) error {
	if len(dirtyTurns) > 0 {
		if err := refreshTurnAggregates(ctx, tx, dirtyTurns); err != nil {
			return err
		}
	}
	if len(dirtySessions) > 0 {
		if err := refreshSessionAggregates(ctx, tx, dirtySessions); err != nil {
			return err
		}
	}
	return nil
}

func refreshTurnAggregates(ctx context.Context, tx *sql.Tx, dirty map[string]struct{}) error {
	ids := keys(dirty)
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClauseStrings(ids)
	// G201 suppressed: placeholders is "?,?,?,..." composed exclusively
	// from inClauseStrings (no user input on this path) and every id is
	// bound as a parameter via args, not interpolated into the SQL.
	// gosec cannot trace that.
	q := fmt.Sprintf(`
UPDATE turns SET
    tokens_in          = COALESCE((SELECT SUM(tokens_in)          FROM ops WHERE ops.turn_id = turns.id), 0),
    tokens_out         = COALESCE((SELECT SUM(tokens_out)         FROM ops WHERE ops.turn_id = turns.id), 0),
    tokens_cache_read  = COALESCE((SELECT SUM(tokens_cache_read)  FROM ops WHERE ops.turn_id = turns.id), 0),
    tokens_cache_write = COALESCE((SELECT SUM(tokens_cache_write) FROM ops WHERE ops.turn_id = turns.id), 0),
    cost_usd           = COALESCE((SELECT SUM(cost_usd)           FROM ops WHERE ops.turn_id = turns.id), 0.0),
    op_count           = COALESCE((SELECT COUNT(*)                FROM ops WHERE ops.turn_id = turns.id), 0)
WHERE turns.id IN (%s)
`, placeholders) // #nosec G201 -- placeholders are ?-marks only
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("refresh turn aggregates: %w", err)
	}
	return nil
}

func refreshSessionAggregates(ctx context.Context, tx *sql.Tx, dirty map[string]struct{}) error {
	ids := keys(dirty)
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClauseStrings(ids)
	// G201 suppressed: same rationale as refreshTurnAggregates — only
	// "?,?,?,..." is interpolated; ids land via args parameter binding.
	q := fmt.Sprintf(`
UPDATE sessions SET
    tokens_in          = COALESCE((SELECT SUM(tokens_in)          FROM turns WHERE turns.session_id = sessions.id), 0),
    tokens_out         = COALESCE((SELECT SUM(tokens_out)         FROM turns WHERE turns.session_id = sessions.id), 0),
    tokens_cache_read  = COALESCE((SELECT SUM(tokens_cache_read)  FROM turns WHERE turns.session_id = sessions.id), 0),
    tokens_cache_write = COALESCE((SELECT SUM(tokens_cache_write) FROM turns WHERE turns.session_id = sessions.id), 0),
    cost_usd           = COALESCE((SELECT SUM(cost_usd)           FROM turns WHERE turns.session_id = sessions.id), 0.0),
    turn_count         = COALESCE((SELECT COUNT(*)                FROM turns WHERE turns.session_id = sessions.id), 0),
    op_count           = COALESCE((SELECT SUM(op_count)           FROM turns WHERE turns.session_id = sessions.id), 0),
    failure_count      = COALESCE((SELECT COUNT(*)                FROM ops   WHERE ops.session_id   = sessions.id AND ops.status = 'failed'), 0),
    last_activity_ts   = MAX(sessions.last_activity_ts,
                              COALESCE((SELECT MAX(end_ts)        FROM ops   WHERE ops.session_id   = sessions.id), 0))
WHERE sessions.id IN (%s)
`, placeholders) // #nosec G201 -- placeholders are ?-marks only
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("refresh session aggregates: %w", err)
	}
	return nil
}

// inClauseStrings expands ids into a (?, ?, ?) placeholder string and a
// matching []any slice. SQLite parameter limit is 32766 by default;
// the batch size of 1000 (and the dirty set is at most one per event)
// stays comfortably below.
func inClauseStrings(ids []string) (string, []any) {
	args := make([]any, len(ids))
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "?"
		args[i] = id
	}
	return strings.Join(parts, ","), args
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
