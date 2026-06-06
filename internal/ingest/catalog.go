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

// The priorOpIdentity type + the catalog identity-MIGRATION helpers
// (catalogIdentityChanged, normalizeToolNamespace, removeOpContribution,
// addMigratedTotals) live in catalog_migrate.go (SOW-0004 I1), keeping this file
// focused on the straight-line per-event upserts.

// onOpStarted populates catalog_providers, catalog_models, and
// catalog_tools depending on op kind.
//
// inserted reports whether the op's ops-table row was a GENUINE NEW INSERT in
// this batch (determined by applyOpStarted's existence probe before its upsert).
// call_count is bumped only for a genuine insert or when identity migration
// moves an existing op to a new catalog key. A re-emitted OpStarted (late
// enrichment carrying corrected status/extras for an op that already exists)
// is otherwise an UPDATE, and double-counting it would inflate the
// per-(provider,model)/(namespace,name) call totals. The first_seen/last_seen
// floor/ceiling and the ctx_max seed stay idempotent (MIN/MAX/COALESCE) and run
// on every call so a re-emit still refreshes them. (SOW-0020 / SOW-0004 H1a.)
//
// prior carries the op's persisted catalog identity + terminal totals as they
// stood before this OpStarted's row upsert (empty when inserted=true). When the
// op already existed AND its catalog identity CHANGED (codex MCP enrichment
// re-stamping tool_namespace/name on the same (turn,seq) — SOW-0004 I1), the op's
// whole contribution (call_count + any already-booked failure/tokens/cost/
// duration totals) is MOVED off the old key before it is added to the new one, so
// the physical op is counted under exactly ONE catalog row.
func (c *catalogWriter) onOpStarted(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent, inserted bool, prior priorOpIdentity) error {
	start := c.opStartedCatalogUpdate(ev, inserted, prior)
	if err := c.removePriorStartedContribution(ctx, tx, start, prior); err != nil {
		return err
	}
	if err := c.upsertStartedCatalog(ctx, tx, start); err != nil {
		return err
	}
	return c.addPriorStartedTotals(ctx, tx, start, prior)
}

type opStartedCatalogUpdate struct {
	eff             effectiveCatalogIdentity
	ts              int64
	callInc         int
	identityChanged bool
}

func (c *catalogWriter) opStartedCatalogUpdate(ev canonical.OpStartedEvent, inserted bool, prior priorOpIdentity) opStartedCatalogUpdate {
	eff := effectiveOpIdentity(ev, prior)
	identityChanged := !inserted && prior.found && c.catalogIdentityChanged(eff, prior)
	return opStartedCatalogUpdate{
		eff:             eff,
		ts:              ev.Ts,
		callInc:         catalogCallIncrement(inserted, identityChanged),
		identityChanged: identityChanged,
	}
}

func catalogCallIncrement(inserted, identityChanged bool) int {
	if inserted || identityChanged {
		return 1
	}
	return 0
}

func (c *catalogWriter) removePriorStartedContribution(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate, prior priorOpIdentity) error {
	if !start.identityChanged {
		return nil
	}
	return c.removeOpContribution(ctx, tx, prior)
}

func (c *catalogWriter) addPriorStartedTotals(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate, prior priorOpIdentity) error {
	if !start.identityChanged {
		return nil
	}
	return c.addMigratedTotals(ctx, tx, start.eff, prior.totals)
}

func (c *catalogWriter) upsertStartedCatalog(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate) error {
	switch start.eff.kind {
	case string(canonical.OpLLM):
		return c.upsertStartedLLM(ctx, tx, start)
	case string(canonical.OpTool):
		return upsertStartedTool(ctx, tx, start)
	}
	return nil
}

func (c *catalogWriter) upsertStartedLLM(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate) error {
	if start.eff.provider != "" {
		if err := upsertProvider(ctx, tx, start.eff.provider, start.eff.providerAlias, start.ts, start.callInc); err != nil {
			return err
		}
	}
	return c.upsertStartedModel(ctx, tx, start)
}

func (c *catalogWriter) upsertStartedModel(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate) error {
	if start.eff.provider == "" || start.eff.model == "" {
		return nil
	}
	ctxMaxSeed := c.ctxMaxSeed(start.eff.provider, start.eff.model)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_models (provider, name, ctx_max, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (provider, name) DO UPDATE SET
    first_seen = MIN(catalog_models.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_models.last_seen, excluded.last_seen),
    ctx_max    = COALESCE(catalog_models.ctx_max, excluded.ctx_max),
    call_count = catalog_models.call_count + ?
`, start.eff.provider, start.eff.model, ctxMaxSeed, start.ts, start.ts, start.callInc); err != nil {
		return fmt.Errorf("catalog_models upsert: %w", err)
	}
	return nil
}

func (c *catalogWriter) ctxMaxSeed(provider, model string) sql.NullInt64 {
	if mp, ok := c.pricer.(MetadataPricer); ok && mp != nil {
		if cm, hit := mp.CtxMax(provider, model); hit && cm > 0 {
			return sql.NullInt64{Int64: cm, Valid: true}
		}
	}
	return sql.NullInt64{}
}

func upsertStartedTool(ctx context.Context, tx *sql.Tx, start opStartedCatalogUpdate) error {
	if start.eff.name == "" {
		return nil
	}
	ns := normalizeToolNamespace(start.eff.toolNamespace)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (namespace, name) DO UPDATE SET
    first_seen = MIN(catalog_tools.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_tools.last_seen, excluded.last_seen),
    call_count = catalog_tools.call_count + ?
`, ns, start.eff.name, start.ts, start.ts, start.callInc); err != nil {
		return fmt.Errorf("catalog_tools upsert: %w", err)
	}
	return nil
}

// onOpFinalized updates running totals on the catalog row matching the op's
// kind. The op kind/identity AND the now-persisted terminal totals are read from
// the ops row (it was upserted by applyOpFinalized just before this call, and
// recorded by the matching OpStarted) since OpFinalizedEvent carries neither the
// kind nor the resolved cost. prior is the op's contribution before this
// finalize's UPDATE, captured by applyOpFinalized; the aggregate moves by the
// (now − prior) delta so a re-emit is idempotent (SOW-0004 H1a).
func (c *catalogWriter) onOpFinalized(ctx context.Context, tx *sql.Tx, opID string, ev canonical.OpFinalizedEvent, prior opPriorTotals) error {
	row, found, err := readFinalizedCatalogRow(ctx, tx, opID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	delta := catalogTotalsDeltaFrom(row, prior)
	switch row.kind {
	case string(canonical.OpLLM):
		return c.updateFinalizedLLM(ctx, tx, row, delta, ev)
	case string(canonical.OpTool):
		return updateFinalizedTool(ctx, tx, row, delta, ev.EndTs)
	}
	return nil
}

// upsertProvider seeds/refreshes a catalog_providers row. callInc is added to
// call_count on the ON CONFLICT (existing-row) branch only — 1 on a genuine new
// op insert, 0 on a re-emitted OpStarted — so a late-enrichment re-emit does not
// double-count the provider's call total (SOW-0020 / SOW-0004 H1a). On a genuine
// INSERT the VALUES(...,1) sets the count and the conflict branch is not taken.
func upsertProvider(ctx context.Context, tx *sql.Tx, name, alias string, ts int64, callInc int) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_providers (name, alias, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (name, alias) DO UPDATE SET
    first_seen = MIN(catalog_providers.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_providers.last_seen, excluded.last_seen),
    call_count = catalog_providers.call_count + ?
`, name, alias, ts, ts, callInc); err != nil {
		return fmt.Errorf("catalog_providers upsert: %w", err)
	}
	return nil
}
