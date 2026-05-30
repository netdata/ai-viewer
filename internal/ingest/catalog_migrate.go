package ingest

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file holds the catalog identity-MIGRATION logic (SOW-0004 I1): when a
// re-emitted OpStarted CORRECTS an op's catalog identity on the same (turn, seq)
// — the codex MCP-enrichment case where a `function_call` first counted under a
// heuristic tool_namespace="custom" key is re-stamped to "mcp:<server>" (and the
// tool name) — the op's whole contribution (call_count + any already-booked
// failure/tokens/cost/duration totals) is MOVED off the old catalog row and onto
// the new one, so one physical op is counted under exactly ONE catalog row. The
// straight-line per-event upserts live in catalog.go; onOpStarted there calls
// these helpers.

// priorOpIdentity captures an op's persisted catalog identity AND its terminal
// rollup contribution as they stood BEFORE the current OpStarted's row upsert.
// applyOpStarted reads this from the ops row when the op already exists
// (inserted=false) so onOpStarted can MIGRATE the op's catalog contribution from
// its old key to its new key when a re-emitted OpStarted CHANGES the catalog
// identity (SOW-0004 I1).
//
// The OpStarted upsert touches only the identity columns (kind/name/namespace/
// model/provider/alias) + start_ts/extras; it does NOT touch status, tokens_*,
// cost_usd, or duration_us. So these totals, read just before the upsert, are
// exactly the contribution onOpFinalized already booked under the OLD identity
// (zero when the op was OpStarted but never finalized). Migrating call_count AND
// these totals from the old key to the new key keeps one physical op contributing
// to exactly ONE catalog row (its final identity), call_count = 1.
//
// found=false means the op row was absent before this OpStarted (a genuine new
// insert) — there is nothing to migrate and the normal +1 insert path runs.
type priorOpIdentity struct {
	found         bool
	kind          string
	name          string
	toolNamespace string
	model         string
	provider      string
	providerAlias string
	totals        opPriorTotals
}

// catalogIdentityChanged reports whether a re-emitted OpStarted lands on a
// DIFFERENT catalog row than the op's prior persisted identity, so onOpStarted
// migrates the op's contribution instead of double-counting it (SOW-0004 I1). The
// comparison mirrors the catalog keying exactly: LLM ops key on
// (provider, alias, model); tool ops key on (namespace-normalized-to-builtin,
// name); a changed KIND always counts as changed. The event's identity is
// compared against the persisted columns the prior op contributed under.
func (c *catalogWriter) catalogIdentityChanged(ev canonical.OpStartedEvent, prior priorOpIdentity) bool {
	if string(ev.Kind) != prior.kind {
		return true
	}
	switch ev.Kind {
	case canonical.OpLLM:
		return ev.Provider != prior.provider ||
			ev.ProviderAlias != prior.providerAlias ||
			ev.Model != prior.model
	case canonical.OpTool:
		return normalizeToolNamespace(ev.ToolNamespace) != normalizeToolNamespace(prior.toolNamespace) ||
			ev.Name != prior.name
	default:
		// session/system/reasoning/compaction ops touch no catalog rollup row, so
		// there is never a contribution to migrate.
		return false
	}
}

// normalizeToolNamespace mirrors onOpStarted/onOpFinalized's empty→"builtin"
// fold so the migration compares the SAME key the rollup wrote under.
func normalizeToolNamespace(ns string) string {
	if ns == "" {
		return "builtin"
	}
	return ns
}

// removeOpContribution backs an op's whole rollup contribution OUT of its OLD
// catalog key before onOpStarted re-books it under the new key (SOW-0004 I1
// identity migration). It subtracts call_count by 1 and the op's already-booked
// failure/tokens/cost/duration totals (zero when the op was started but never
// finalized), keyed on the prior PERSISTED identity. The columns mirror
// onOpFinalized's per-kind total sets exactly (providers carry no duration;
// tools carry no cache split) so the move is a faithful inverse. ctx_max is
// MAX-based, not summed, so it is intentionally NOT decremented — a stale seed on
// an emptied row is harmless (no op references it) and re-derives on the next
// observation.
func (c *catalogWriter) removeOpContribution(ctx context.Context, tx *sql.Tx, prior priorOpIdentity) error {
	t := prior.totals
	failure := failureInc(t.status)
	switch prior.kind {
	case string(canonical.OpLLM):
		if prior.provider != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_providers SET
    call_count               = call_count - 1,
    failure_count            = failure_count - ?,
    total_tokens_in          = total_tokens_in - ?,
    total_tokens_out         = total_tokens_out - ?,
    total_tokens_cache_read  = total_tokens_cache_read - ?,
    total_tokens_cache_write = total_tokens_cache_write - ?,
    total_cost_usd           = total_cost_usd - ?
WHERE name = ? AND alias = ?
`, failure, t.tokensIn, t.tokensOut, t.tokensCacheRead, t.tokensCacheWrite, t.costUSD,
				prior.provider, prior.providerAlias); err != nil {
				return fmt.Errorf("catalog_providers migrate-out: %w", err)
			}
		}
		if prior.provider != "" && prior.model != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_models SET
    call_count               = call_count - 1,
    failure_count            = failure_count - ?,
    total_tokens_in          = total_tokens_in - ?,
    total_tokens_out         = total_tokens_out - ?,
    total_tokens_cache_read  = total_tokens_cache_read - ?,
    total_tokens_cache_write = total_tokens_cache_write - ?,
    total_cost_usd           = total_cost_usd - ?,
    total_duration_us        = total_duration_us - ?
WHERE provider = ? AND name = ?
`, failure, t.tokensIn, t.tokensOut, t.tokensCacheRead, t.tokensCacheWrite, t.costUSD, t.durationUS,
				prior.provider, prior.model); err != nil {
				return fmt.Errorf("catalog_models migrate-out: %w", err)
			}
		}
	case string(canonical.OpTool):
		if prior.name != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_tools SET
    call_count        = call_count - 1,
    failure_count     = failure_count - ?,
    total_tokens_in   = total_tokens_in - ?,
    total_tokens_out  = total_tokens_out - ?,
    total_cost_usd    = total_cost_usd - ?,
    total_duration_us = total_duration_us - ?
WHERE namespace = ? AND name = ?
`, failure, t.tokensIn, t.tokensOut, t.costUSD, t.durationUS,
				normalizeToolNamespace(prior.toolNamespace), prior.name); err != nil {
				return fmt.Errorf("catalog_tools migrate-out: %w", err)
			}
		}
	}
	return nil
}

// addMigratedTotals re-books the op's already-finalized totals (failure/tokens/
// cost/duration) onto its NEW catalog key after an identity change (SOW-0004 I1).
// call_count for the new key is handled by onOpStarted's upsert (callInc=1); this
// adds ONLY the totals removeOpContribution backed off the old key, so the new
// key starts from the op's prior contribution and any subsequent OpFinalized
// re-emit then applies its (now − prior) delta on top. The column sets mirror
// onOpFinalized exactly. last_seen is left to the OpStarted upsert / a later
// OpFinalized (this UPDATE only moves accumulating totals).
func (c *catalogWriter) addMigratedTotals(ctx context.Context, tx *sql.Tx, ev canonical.OpStartedEvent, t opPriorTotals) error {
	// t is the prior PERSISTED contribution; the caller only reaches here when the
	// op row already existed (prior.found), so t.found is always true. An op started
	// but never finalized has status="running" (failureInc 0) and zero tokens/cost/
	// duration, so the adds below move nothing meaningful — only the call_count the
	// OpStarted upsert already re-booked under the new key matters in that case.
	failure := failureInc(t.status)
	switch ev.Kind {
	case canonical.OpLLM:
		if ev.Provider != "" {
			alias := ev.ProviderAlias
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_providers SET
    failure_count            = failure_count + ?,
    total_tokens_in          = total_tokens_in + ?,
    total_tokens_out         = total_tokens_out + ?,
    total_tokens_cache_read  = total_tokens_cache_read + ?,
    total_tokens_cache_write = total_tokens_cache_write + ?,
    total_cost_usd           = total_cost_usd + ?
WHERE name = ? AND alias = ?
`, failure, t.tokensIn, t.tokensOut, t.tokensCacheRead, t.tokensCacheWrite, t.costUSD,
				ev.Provider, alias); err != nil {
				return fmt.Errorf("catalog_providers migrate-in: %w", err)
			}
		}
		if ev.Provider != "" && ev.Model != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_models SET
    failure_count            = failure_count + ?,
    total_tokens_in          = total_tokens_in + ?,
    total_tokens_out         = total_tokens_out + ?,
    total_tokens_cache_read  = total_tokens_cache_read + ?,
    total_tokens_cache_write = total_tokens_cache_write + ?,
    total_cost_usd           = total_cost_usd + ?,
    total_duration_us        = total_duration_us + ?
WHERE provider = ? AND name = ?
`, failure, t.tokensIn, t.tokensOut, t.tokensCacheRead, t.tokensCacheWrite, t.costUSD, t.durationUS,
				ev.Provider, ev.Model); err != nil {
				return fmt.Errorf("catalog_models migrate-in: %w", err)
			}
		}
	case canonical.OpTool:
		if ev.Name != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_tools SET
    failure_count     = failure_count + ?,
    total_tokens_in   = total_tokens_in + ?,
    total_tokens_out  = total_tokens_out + ?,
    total_cost_usd    = total_cost_usd + ?,
    total_duration_us = total_duration_us + ?
WHERE namespace = ? AND name = ?
`, failure, t.tokensIn, t.tokensOut, t.costUSD, t.durationUS,
				normalizeToolNamespace(ev.ToolNamespace), ev.Name); err != nil {
				return fmt.Errorf("catalog_tools migrate-in: %w", err)
			}
		}
	}
	return nil
}
