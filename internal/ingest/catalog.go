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
// call_count is bumped ONLY on a genuine insert: a re-emitted OpStarted (late
// enrichment carrying corrected status/extras for an op that already exists —
// the codex/claude_code replay-from-0 + enrichment design re-emits OpStarted on
// the same (turn,seq)) is an UPDATE, and double-counting it would inflate the
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
	// eff is the catalog identity the ops row will ACTUALLY carry after applyOpStarted's
	// upsert: the upsert COALESCEs omitted tool_namespace/model/provider/provider_alias
	// back to the prior persisted value (writer.go), so booking + migration must use
	// this effective identity, not the raw event — otherwise an empty-but-unchanged
	// re-emit drains its real catalog key without re-booking (SOW-0004 I1 shared-ingest
	// edge). On a genuine insert (prior absent) eff equals the event identity.
	eff := effectiveOpIdentity(ev, prior)
	// identityChanged is true only for a re-emit of an EXISTING op whose catalog
	// identity was corrected (codex MCP enrichment — SOW-0004 I1). Computed once and
	// reused below for the migrate-out, the call_count bump, and the migrate-in.
	identityChanged := !inserted && prior.found && c.catalogIdentityChanged(eff, prior)
	// Identity migration (I1): an existing op whose catalog identity changed has
	// already contributed (call_count, and any finalize totals) to its OLD key. Move
	// that whole contribution off the old key here, BEFORE the add below re-books it
	// under the new key. A re-emit with the SAME identity skips this (nothing to
	// move) and falls through to the idempotent callInc=0 path (H1a).
	if identityChanged {
		if err := c.removeOpContribution(ctx, tx, prior); err != nil {
			return err
		}
	}
	// callInc is added to call_count on the ON CONFLICT (existing-row) branch. On a
	// genuine INSERT the VALUES(...,1) sets the count and the branch is not taken; on
	// a same-identity re-emit (existing row) callInc=0 keeps the count, eliminating
	// the double-count (H1a). On an identity-CHANGED re-emit we just removed the old
	// key's count, so this op must register +1 under its new key even though the ops
	// row already existed — hence callInc=1 here too (I1). We still run the upsert
	// (not a bare UPDATE) so the row is created when applyOpStarted's probe raced an
	// absent row.
	callInc := 0
	if inserted || identityChanged {
		callInc = 1
	}
	// Booking keys use the EFFECTIVE post-upsert identity (eff), not the raw event:
	// they must match the ops row the upsert produces and the key the migrate-in/out
	// touch, so an empty-field re-emit books under the prior-preserved key rather than
	// an empty one (SOW-0004 I1 shared-ingest edge).
	switch eff.kind {
	case string(canonical.OpLLM):
		if eff.provider != "" {
			if err := upsertProvider(ctx, tx, eff.provider, eff.providerAlias, ev.Ts, callInc); err != nil {
				return err
			}
		}
		if eff.provider != "" && eff.model != "" {
			// Iter-8 fix iter8-4: seed ctx_max from the pricing table
			// when the pricer carries metadata. The COALESCE on
			// ON CONFLICT keeps an existing non-null ctx_max (set by a
			// prior OpFinalized recording the op's own CtxMax)
			// untouched — the table seeds, the op refines.
			ctxMaxSeed := sql.NullInt64{}
			if mp, ok := c.pricer.(MetadataPricer); ok && mp != nil {
				if cm, hit := mp.CtxMax(eff.provider, eff.model); hit && cm > 0 {
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
    call_count = catalog_models.call_count + ?
`, eff.provider, eff.model, ctxMaxSeed, ev.Ts, ev.Ts, callInc); err != nil {
				return fmt.Errorf("catalog_models upsert: %w", err)
			}
		}
	case string(canonical.OpTool):
		if eff.name != "" {
			ns := normalizeToolNamespace(eff.toolNamespace)
			if _, err := tx.ExecContext(ctx, `
INSERT INTO catalog_tools (namespace, name, first_seen, last_seen, call_count)
VALUES (?, ?, ?, ?, 1)
ON CONFLICT (namespace, name) DO UPDATE SET
    first_seen = MIN(catalog_tools.first_seen, excluded.first_seen),
    last_seen  = MAX(catalog_tools.last_seen, excluded.last_seen),
    call_count = catalog_tools.call_count + ?
`, ns, eff.name, ev.Ts, ev.Ts, callInc); err != nil {
				return fmt.Errorf("catalog_tools upsert: %w", err)
			}
		}
	}
	// I1: on an identity-changed re-emit, the upsert above re-booked call_count under
	// the NEW key but not the op's already-finalized totals (those live on the OLD key
	// we just decremented). Move those totals onto the new key now. A subsequent
	// OpFinalized re-emit applies a (now − prior) delta on top of this migrated base,
	// so the new key converges to the op's current finalize contribution exactly once
	// (migrated prior + (now − prior) = now); when no re-finalize follows, the new key
	// simply holds the op's last-known contribution.
	if identityChanged {
		if err := c.addMigratedTotals(ctx, tx, eff, prior.totals); err != nil {
			return err
		}
	}
	return nil
}

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

// onOpFinalized updates running totals on the catalog row matching the op's
// kind. The op kind/identity AND the now-persisted terminal totals are read from
// the ops row (it was upserted by applyOpFinalized just before this call, and
// recorded by the matching OpStarted) since OpFinalizedEvent carries neither the
// kind nor the resolved cost. prior is the op's contribution before this
// finalize's UPDATE, captured by applyOpFinalized; the aggregate moves by the
// (now − prior) delta so a re-emit is idempotent (SOW-0004 H1a).
func (c *catalogWriter) onOpFinalized(ctx context.Context, tx *sql.Tx, opID string, ev canonical.OpFinalizedEvent, prior opPriorTotals) error {
	// Pull the kind + identity AND the now-persisted terminal totals from the row
	// we just upserted. Reading the persisted values (not ev.*) makes the catalog
	// a faithful mirror of the ops row: duration in particular is persisted via
	// COALESCE, so a NULL-duration re-finalize keeps the old value and the delta
	// is zero (using ev.EndTs-ev.Ts here would wrongly subtract on a re-finalize).
	var (
		kind          string
		name          string
		toolNamespace sql.NullString
		model         sql.NullString
		provider      sql.NullString
		providerAlias sql.NullString
		nowStatus     string
		nowTokensIn   int64
		nowTokensOut  int64
		nowCacheRead  int64
		nowCacheWrite int64
		nowCostUSD    float64
		nowDurationUS sql.NullInt64
	)
	row := tx.QueryRowContext(ctx,
		`SELECT kind, name, tool_namespace, model, provider, provider_alias,
		        status, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, duration_us
		   FROM ops WHERE id = ?`,
		opID)
	if err := row.Scan(&kind, &name, &toolNamespace, &model, &provider, &providerAlias,
		&nowStatus, &nowTokensIn, &nowTokensOut, &nowCacheRead, &nowCacheWrite, &nowCostUSD, &nowDurationUS); err != nil {
		if err == sql.ErrNoRows {
			// OpStarted never landed (event ordering bug). Skip; the
			// per-session aggregates still pick up the row once it
			// arrives.
			return nil
		}
		return fmt.Errorf("catalog onOpFinalized lookup: %w", err)
	}
	// Deltas vs the prior persisted contribution (zero when the op row was absent
	// before this finalize). A re-emit with unchanged totals yields all-zero
	// deltas — a true no-op against the aggregate.
	failureDelta := failureInc(nowStatus) - failureInc(prior.status)
	tokensInDelta := nowTokensIn - prior.tokensIn
	tokensOutDelta := nowTokensOut - prior.tokensOut
	cacheReadDelta := nowCacheRead - prior.tokensCacheRead
	cacheWriteDelta := nowCacheWrite - prior.tokensCacheWrite
	costDelta := nowCostUSD - prior.costUSD
	durDelta := nowDurationUS.Int64 - prior.durationUS
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
`, failureDelta, tokensInDelta, tokensOutDelta, cacheReadDelta, cacheWriteDelta, costDelta, ev.EndTs,
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
			// ctx_max stays MAX-based (idempotent under re-emit by
			// construction), so it uses ev.CtxMax directly, not a delta.
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
`, failureDelta, tokensInDelta, tokensOutDelta, cacheReadDelta, cacheWriteDelta, costDelta, durDelta,
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
`, failureDelta, tokensInDelta, tokensOutDelta, costDelta, durDelta, ev.EndTs, ns, name); err != nil {
			return fmt.Errorf("catalog_tools totals: %w", err)
		}
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
