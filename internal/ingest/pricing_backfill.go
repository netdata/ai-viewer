package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// PricingBackfillStats summarizes a re-pricing pass.
type PricingBackfillStats struct {
	OpsPriced      int
	OpsSkipped     int
	TotalCostAdded float64
	Elapsed        time.Duration
}

// unpricedOp holds one row from the unpriced-ops query.
type unpricedOp struct {
	id               string
	provider, model  string
	ts               int64
	tokensIn         int64
	tokensOut        int64
	tokensCacheRead  int64
	tokensCacheWrite int64
}

// BackfillPricing re-prices all LLM ops whose cost_usd is 0 but that carry
// tokens, using the current pricing table. Uses a temp table + single UPDATE
// join (O(n) total) instead of per-op CASE WHEN (O(n × table_size)).
func BackfillPricing(ctx context.Context, db *sql.DB, pricer Pricer, logger *slog.Logger) (PricingBackfillStats, error) {
	start := time.Now()
	stats := PricingBackfillStats{}

	// Create a temp table for the priced results.
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS reprice_batch (id TEXT PRIMARY KEY, cost REAL NOT NULL)`); err != nil {
		return stats, fmt.Errorf("reprice: create temp table: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS temp.reprice_batch`) }()
	// Clear any leftovers from a prior partial run.
	if _, err := db.ExecContext(ctx, `DELETE FROM temp.reprice_batch`); err != nil {
		return stats, fmt.Errorf("reprice: clear temp table: %w", err)
	}

	// Read + price all unpriced ops, inserting priced results into the temp
	// table in batches. Priced = pricer returned > 0; skipped = pricer returned
	// 0 (model not in the pricing table — a data gap, not a failure).
	rows, err := db.QueryContext(ctx, `
SELECT id, provider, model, start_ts, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write
FROM ops WHERE kind='llm' AND cost_usd = 0 AND tokens_in > 0 AND model != ''`)
	if err != nil {
		return stats, fmt.Errorf("reprice: query unpriced ops: %w", err)
	}

	const insertBatch = 1000
	var placeholders []string
	var args []any
	flush := func() error {
		if len(placeholders) == 0 {
			return nil
		}
		// #nosec G202 — placeholders are static "(?,?)" strings (no user input);
		// every value is bound via args as a ? placeholder.
		q := "INSERT INTO temp.reprice_batch (id, cost) VALUES " + strings.Join(placeholders, ",")
		_, err := db.ExecContext(ctx, q, args...)
		placeholders = placeholders[:0]
		args = args[:0]
		return err
	}

	for rows.Next() {
		var op unpricedOp
		if err := rows.Scan(&op.id, &op.provider, &op.model, &op.ts,
			&op.tokensIn, &op.tokensOut, &op.tokensCacheRead, &op.tokensCacheWrite); err != nil {
			_ = rows.Close()
			return stats, fmt.Errorf("reprice: scan op: %w", err)
		}
		cost := pricer.Cost(op.provider, op.model, op.ts, op.tokensIn, op.tokensOut, op.tokensCacheRead, op.tokensCacheWrite)
		if cost == 0 {
			stats.OpsSkipped++
			continue
		}
		placeholders = append(placeholders, "(?,?)")
		args = append(args, op.id, cost)
		stats.OpsPriced++
		stats.TotalCostAdded += cost
		if len(placeholders) >= insertBatch {
			if err := flush(); err != nil {
				_ = rows.Close()
				return stats, fmt.Errorf("reprice: insert batch: %w", err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stats, fmt.Errorf("reprice: iterate: %w", err)
	}
	_ = rows.Close()
	if err := flush(); err != nil {
		return stats, fmt.Errorf("reprice: final insert: %w", err)
	}

	if stats.OpsPriced == 0 {
		stats.Elapsed = time.Since(start)
		return stats, nil
	}

	// Single UPDATE join: O(matched rows) via the temp table's PK index.
	if _, err := db.ExecContext(ctx, `
UPDATE ops SET cost_usd = (
    SELECT cost FROM temp.reprice_batch WHERE temp.reprice_batch.id = ops.id
) WHERE id IN (SELECT id FROM temp.reprice_batch)`); err != nil {
		return stats, fmt.Errorf("reprice: update ops: %w", err)
	}

	// Cascade: recompute turns + sessions cost_usd from the updated ops.
	if err := recomputeCostAggregates(ctx, db); err != nil {
		return stats, fmt.Errorf("reprice: cascade aggregates: %w", err)
	}

	stats.Elapsed = time.Since(start)
	if logger != nil {
		logger.Info("reprice: complete",
			"ops_priced", stats.OpsPriced,
			"ops_skipped", stats.OpsSkipped,
			"total_cost_added", fmt.Sprintf("%.4f", stats.TotalCostAdded),
			"elapsed", stats.Elapsed.String(),
		)
	}
	return stats, nil
}

// recomputeCostAggregates cascades op costs into turns and sessions via
// pre-aggregated temp tables. The naive correlated-subquery approach
// (UPDATE turns SET cost = (SELECT SUM(...) FROM ops WHERE turn_id = turns.id))
// is O(turns × ops) — catastrophic on a 1.3M-op / 200k-turn database. The
// temp-table GROUP BY is O(ops + turns): one pass over ops, one pass over
// turns, both indexed.
func recomputeCostAggregates(ctx context.Context, db *sql.DB) error {
	// Pre-aggregate turn costs from ops.
	if _, err := db.ExecContext(ctx, `
CREATE TEMP TABLE IF NOT EXISTS _turn_costs AS
SELECT turn_id, SUM(cost_usd) AS cost FROM ops GROUP BY turn_id`); err != nil {
		return fmt.Errorf("aggregate turn costs: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS temp._turn_costs`) }()

	if _, err := db.ExecContext(ctx, `
UPDATE turns SET cost_usd = (
    SELECT cost FROM temp._turn_costs WHERE temp._turn_costs.turn_id = turns.id
) WHERE id IN (SELECT turn_id FROM temp._turn_costs)`); err != nil {
		return fmt.Errorf("update turns cost: %w", err)
	}

	// Pre-aggregate session costs from turns.
	if _, err := db.ExecContext(ctx, `
CREATE TEMP TABLE IF NOT EXISTS _session_costs AS
SELECT session_id, SUM(cost_usd) AS cost FROM turns GROUP BY session_id`); err != nil {
		return fmt.Errorf("aggregate session costs: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS temp._session_costs`) }()

	if _, err := db.ExecContext(ctx, `
UPDATE sessions SET cost_usd = (
    SELECT cost FROM temp._session_costs WHERE temp._session_costs.session_id = sessions.id
) WHERE id IN (SELECT session_id FROM temp._session_costs)`); err != nil {
		return fmt.Errorf("update sessions cost: %w", err)
	}
	return nil
}
