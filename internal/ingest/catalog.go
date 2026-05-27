package ingest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// catalogWriter handles inline upserts of the catalog_* rollup tables.
// Time-bucketed rollups (per-hour, per-day) are deferred to SOW-0007;
// here we only maintain the simple "first_seen / last_seen / counters"
// rows so cross-session analytics queries have data to lean on.
//
// Each method runs against a *sql.Tx so the catalog row update commits
// or rolls back with the rest of the batch.
//
// catalogWriter holds an optional pricer reference so onOpStarted can
// seed `catalog_models.ctx_max` from the embedded pricing table on
// first sight of a (provider, model). Per pricing.md §"Field
// semantics" the table's ctx_max is the catalog seed; the op's own
// CtxMax (recorded on OpFinalized) still takes precedence on
// subsequent updates. Pricers without ctx_max metadata (NopPricer,
// most test fakes) are no-ops here. (Iter-8 fix iter8-4.)
type catalogWriter struct {
	pricer Pricer
}

func newCatalogWriter(pricer Pricer) *catalogWriter { return &catalogWriter{pricer: pricer} }

// onSessionStarted populates catalog_agents and catalog_cwds when the
// session start event carries enough information.
func (c *catalogWriter) onSessionStarted(ctx context.Context, tx *sql.Tx, sourceFormat string, ev canonical.SessionStartedEvent) error {
	if ev.AgentName != "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_agents (source_format, name, first_seen, last_seen, session_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (source_format, name) DO UPDATE SET
    first_seen    = MIN(catalog_agents.first_seen, excluded.first_seen),
    last_seen     = MAX(catalog_agents.last_seen, excluded.last_seen),
    session_count = catalog_agents.session_count + 1
`, sourceFormat, ev.AgentName, ev.Ts, ev.Ts); err != nil {
			return fmt.Errorf("catalog_agents upsert: %w", err)
		}
	}
	if ev.Cwd != "" {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_cwds (source_format, cwd, first_seen, last_seen, session_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (source_format, cwd) DO UPDATE SET
    first_seen    = MIN(catalog_cwds.first_seen, excluded.first_seen),
    last_seen     = MAX(catalog_cwds.last_seen, excluded.last_seen),
    session_count = catalog_cwds.session_count + 1
`, sourceFormat, ev.Cwd, ev.Ts, ev.Ts); err != nil {
			return fmt.Errorf("catalog_cwds upsert: %w", err)
		}
	}
	return nil
}

// onOpStarted populates catalog_providers, catalog_models, and
// catalog_tools depending on op kind.
func (c *catalogWriter) onOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent) error {
	switch ev.Kind {
	case canonical.OpLLM:
		if ev.Provider != "" {
			if err := upsertProvider(ctx, tx, ev.Provider, ev.ProviderAlias, ev.Ts); err != nil {
				return err
			}
		}
		if ev.Provider != "" && ev.Model != "" {
			// Iter-8 fix iter8-4: seed ctx_max from the pricing table
			// when the pricer carries metadata. The COALESCE on
			// ON CONFLICT keeps an existing non-null ctx_max (set by a
			// prior OpFinalized recording the op's own CtxMax)
			// untouched — the table seeds, the op refines.
			ctxMaxSeed := sql.NullInt64{}
			if mp, ok := c.pricer.(MetadataPricer); ok && mp != nil {
				if cm, hit := mp.CtxMax(ev.Provider, ev.Model); hit && cm > 0 {
					ctxMaxSeed = sql.NullInt64{Int64: cm, Valid: true}
				}
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_models (provider, name, ctx_max, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (provider, name) DO UPDATE SET
    first_seen = MIN(catalog_models.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_models.last_seen, excluded.last_seen),
    ctx_max    = COALESCE(catalog_models.ctx_max, excluded.ctx_max),
    call_count = catalog_models.call_count + 1
`, ev.Provider, ev.Model, ctxMaxSeed, ev.Ts, ev.Ts); err != nil {
				return fmt.Errorf("catalog_models upsert: %w", err)
			}
		}
	case canonical.OpTool:
		if ev.Name != "" {
			ns := ev.ToolNamespace
			if ns == "" {
				ns = "builtin"
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (namespace, name) DO UPDATE SET
    first_seen = MIN(catalog_tools.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_tools.last_seen, excluded.last_seen),
    call_count = catalog_tools.call_count + 1
`, ns, ev.Name, ev.Ts, ev.Ts); err != nil {
				return fmt.Errorf("catalog_tools upsert: %w", err)
			}
		}
	}
	return nil
}

// onOpFinalized updates running totals on the catalog row matching the
// op's kind. The op kind is looked up from the ops table (it was
// recorded by the matching OpStarted) since OpFinalizedEvent does not
// carry the kind itself.
func (c *catalogWriter) onOpFinalized(ctx context.Context, tx *sql.Tx, opID string, ev canonical.OpFinalizedEvent) error {
	// Pull the kind + identity from the row we just upserted.
	var (
		kind          string
		name          string
		toolNamespace sql.NullString
		model         sql.NullString
		provider      sql.NullString
		providerAlias sql.NullString
	)
	row := tx.QueryRowContext(ctx,
		`SELECT kind, name, tool_namespace, model, provider, provider_alias FROM ops WHERE id = ?`,
		opID)
	if err := row.Scan(&kind, &name, &toolNamespace, &model, &provider, &providerAlias); err != nil {
		if err == sql.ErrNoRows {
			// OpStarted never landed (event ordering bug). Skip; the
			// per-session aggregates still pick up the row once it
			// arrives.
			return nil
		}
		return fmt.Errorf("catalog onOpFinalized lookup: %w", err)
	}
	failureInc := 0
	if ev.Status == "failed" {
		failureInc = 1
	}
	durUS := int64(0)
	if ev.EndTs > 0 && ev.Ts > 0 && ev.EndTs >= ev.Ts {
		durUS = ev.EndTs - ev.Ts
	}
	switch kind {
	case string(canonical.OpLLM):
		if provider.Valid && provider.String != "" {
			alias := ""
			if providerAlias.Valid {
				alias = providerAlias.String
			}
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
`, failureInc, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite, ev.CostUSD, ev.EndTs,
				provider.String, alias); err != nil {
				return fmt.Errorf("catalog_providers totals: %w", err)
			}
		}
		if provider.Valid && model.Valid && provider.String != "" && model.String != "" {
			// Iter-9 fix iter9-1: per data-model.md:260, :395 the
			// catalog ctx_max is the MAX of all observed values —
			// the pricing seed (set by onOpStarted) is a floor, not
			// a ceiling. An adapter that observes a LARGER ctx_max
			// must update the catalog row, otherwise the seed pins
			// the column forever. The CASE WHEN gate keeps the
			// pre-iter-9 behaviour for ops that record no CtxMax
			// (the column declares NULLIF(?, 0) in writer.go:472).
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
`, failureInc, ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite, ev.CostUSD, durUS,
				ev.CtxMax, ev.CtxMax, ev.EndTs,
				provider.String, model.String); err != nil {
				return fmt.Errorf("catalog_models totals: %w", err)
			}
		}
	case string(canonical.OpTool):
		ns := "builtin"
		if toolNamespace.Valid && toolNamespace.String != "" {
			ns = toolNamespace.String
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE catalog_tools SET
    failure_count     = failure_count + ?,
    total_tokens_in   = total_tokens_in + ?,
    total_tokens_out  = total_tokens_out + ?,
    total_cost_usd    = total_cost_usd + ?,
    total_duration_us = total_duration_us + ?,
    last_seen         = MAX(last_seen, ?)
WHERE namespace = ? AND name = ?
`, failureInc, ev.TokensIn, ev.TokensOut, ev.CostUSD, durUS, ev.EndTs, ns, name); err != nil {
			return fmt.Errorf("catalog_tools totals: %w", err)
		}
	}
	return nil
}

func upsertProvider(ctx context.Context, tx *sql.Tx, name, alias string, ts int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_providers (name, alias, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (name, alias) DO UPDATE SET
    first_seen = MIN(catalog_providers.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_providers.last_seen, excluded.last_seen),
    call_count = catalog_providers.call_count + 1
`, name, alias, ts, ts); err != nil {
		return fmt.Errorf("catalog_providers upsert: %w", err)
	}
	return nil
}
