package ingest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// opPriorTotals captures an op's persisted terminal contribution as it stood
// BEFORE the current OpFinalized's row UPDATE. onOpFinalized applies the DELTA
// between the now-persisted totals and these prior ones, so a re-emitted /
// corrected OpFinalized on the same (turn,seq) updates the catalog aggregate
// EXACTLY ONCE rather than adding its full contribution again (SOW-0020 /
// SOW-0004 H1a). found=false means the op row was absent before this finalize
// (first finalize, or OpStarted not yet landed) — then the prior contribution is
// zero and the delta equals the full new contribution, identical to the
// pre-fix single-emission behaviour.
//
// The prior status drives the failure delta (a completed→failed correction adds
// +1 failure once; a re-emit of the same status adds 0). The token/cost/duration
// deltas mirror the ops row exactly: the writer persists tokens/cost directly
// from the event and duration via COALESCE, so reading the persisted prior + new
// values keeps catalog_*.total_* a faithful running sum even when a re-finalize
// carries NULL duration (COALESCE keeps the old value ⇒ delta 0).
type opPriorTotals struct {
	found            bool
	status           string
	tokensIn         int64
	tokensOut        int64
	tokensCacheRead  int64
	tokensCacheWrite int64
	costUSD          float64
	durationUS       int64
}

// failureInc maps a terminal status to its failure-count contribution.
func failureInc(status string) int64 {
	if status == string(canonical.StatusFailed) {
		return 1
	}
	return 0
}

type finalizedCatalogRow struct {
	kind          string
	name          string
	toolNamespace sql.NullString
	model         sql.NullString
	provider      sql.NullString
	providerAlias sql.NullString
	status        string
	tokensIn      int64
	tokensOut     int64
	cacheRead     int64
	cacheWrite    int64
	costUSD       float64
	durationUS    sql.NullInt64
}

type catalogTotalsDelta struct {
	failure    int64
	tokensIn   int64
	tokensOut  int64
	cacheRead  int64
	cacheWrite int64
	costUSD    float64
	durationUS int64
}

func readFinalizedCatalogRow(ctx context.Context, tx *sql.Tx, opID string) (finalizedCatalogRow, bool, error) {
	var r finalizedCatalogRow
	row := tx.QueryRowContext(ctx,
		`SELECT kind, name, tool_namespace, model, provider, provider_alias,
		        status, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, duration_us
		   FROM ops WHERE id = ?`,
		opID)
	err := row.Scan(&r.kind, &r.name, &r.toolNamespace, &r.model, &r.provider, &r.providerAlias,
		&r.status, &r.tokensIn, &r.tokensOut, &r.cacheRead, &r.cacheWrite, &r.costUSD, &r.durationUS)
	if err == nil {
		return r, true, nil
	}
	if err == sql.ErrNoRows {
		return finalizedCatalogRow{}, false, nil
	}
	return finalizedCatalogRow{}, false, fmt.Errorf("catalog onOpFinalized lookup: %w", err)
}

func catalogTotalsDeltaFrom(row finalizedCatalogRow, prior opPriorTotals) catalogTotalsDelta {
	return catalogTotalsDelta{
		failure:    failureInc(row.status) - failureInc(prior.status),
		tokensIn:   row.tokensIn - prior.tokensIn,
		tokensOut:  row.tokensOut - prior.tokensOut,
		cacheRead:  row.cacheRead - prior.tokensCacheRead,
		cacheWrite: row.cacheWrite - prior.tokensCacheWrite,
		costUSD:    row.costUSD - prior.costUSD,
		durationUS: row.durationUS.Int64 - prior.durationUS,
	}
}

func (c *catalogWriter) updateFinalizedLLM(ctx context.Context, tx *sql.Tx, row finalizedCatalogRow, delta catalogTotalsDelta, ev canonical.OpFinalizedEvent) error {
	provider := nullableString(row.provider)
	model := nullableString(row.model)
	if provider != "" {
		alias := nullableString(row.providerAlias)
		if err := updateFinalizedProvider(ctx, tx, provider, alias, ev.EndTs, delta); err != nil {
			return err
		}
	}
	if provider != "" && model != "" {
		return updateFinalizedModel(ctx, tx, provider, model, ev.EndTs, ev.CtxMax, delta)
	}
	return nil
}

func nullableString(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

func updateFinalizedProvider(ctx context.Context, tx *sql.Tx, provider, alias string, endTs int64, delta catalogTotalsDelta) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE catalog_providers SET
    failure_count            = failure_count + ?,
    total_tokens_in          = total_tokens_in + ?,
    total_tokens_out         = total_tokens_out + ?,
    total_tokens_cache_read  = total_tokens_cache_read + ?,
    total_tokens_cache_write = total_tokens_cache_write + ?,
    total_cost_usd           = total_cost_usd + ?,
    last_seen                = MAX(last_seen, ?)
WHERE name = ? AND alias = ?
`, delta.failure, delta.tokensIn, delta.tokensOut, delta.cacheRead, delta.cacheWrite, delta.costUSD, endTs,
		provider, alias); err != nil {
		return fmt.Errorf("catalog_providers totals: %w", err)
	}
	return nil
}

func updateFinalizedModel(ctx context.Context, tx *sql.Tx, provider, model string, endTs, ctxMax int64, delta catalogTotalsDelta) error {
	// The ctx_max CASE is MAX-based: pricing metadata seeds a floor; adapter observations only raise it.
	if _, err := tx.ExecContext(ctx, `
UPDATE catalog_models SET
    failure_count            = failure_count + ?,
    total_tokens_in          = total_tokens_in + ?,
    total_tokens_out         = total_tokens_out + ?,
    total_tokens_cache_read  = total_tokens_cache_read + ?,
    total_tokens_cache_write = total_tokens_cache_write + ?,
    total_cost_usd           = total_cost_usd + ?,
    total_duration_us        = total_duration_us + ?,
    ctx_max                  = CASE WHEN ? > 0 THEN MAX(COALESCE(ctx_max, 0), ?) ELSE ctx_max END,
    last_seen                = MAX(last_seen, ?)
WHERE provider = ? AND name = ?
`, delta.failure, delta.tokensIn, delta.tokensOut, delta.cacheRead, delta.cacheWrite, delta.costUSD, delta.durationUS,
		ctxMax, ctxMax, endTs,
		provider, model); err != nil {
		return fmt.Errorf("catalog_models totals: %w", err)
	}
	return nil
}

func updateFinalizedTool(ctx context.Context, tx *sql.Tx, row finalizedCatalogRow, delta catalogTotalsDelta, endTs int64) error {
	ns := normalizeToolNamespace(nullableString(row.toolNamespace))
	if _, err := tx.ExecContext(ctx, `
UPDATE catalog_tools SET
    failure_count     = failure_count + ?,
    total_tokens_in   = total_tokens_in + ?,
    total_tokens_out  = total_tokens_out + ?,
    total_cost_usd    = total_cost_usd + ?,
    total_duration_us = total_duration_us + ?,
    last_seen         = MAX(last_seen, ?)
WHERE namespace = ? AND name = ?
`, delta.failure, delta.tokensIn, delta.tokensOut, delta.costUSD, delta.durationUS, endTs, ns, row.name); err != nil {
		return fmt.Errorf("catalog_tools totals: %w", err)
	}
	return nil
}
