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

// effectiveCatalogIdentity is the catalog identity an OpStarted's row will ACTUALLY
// carry AFTER applyOpStarted's upsert — which is NOT the raw event for the optional
// identity columns. The upsert (writer.go) preserves omitted fields via a
// COALESCE/NULLIF empty-as-prior rule, so a re-emit that carries an EMPTY
// tool_namespace/model/provider/provider_alias keeps the PRIOR persisted value;
// kind and name are overwritten with the event's values directly. Migration MUST be
// driven off this effective identity, otherwise an empty-but-unchanged re-emit looks
// "changed" against the raw event and the contribution is drained off its real key
// without being re-booked (the raw-event key is empty) — a permanent drain
// (SOW-0004 I1 shared-ingest edge).
type effectiveCatalogIdentity struct {
	kind          string
	name          string
	toolNamespace string
	model         string
	provider      string
	providerAlias string
}

// effectiveOpIdentity computes the post-upsert catalog identity from the event and
// the op's prior persisted identity, mirroring applyOpStarted's upsert rules: kind
// and name come from the event (overwritten directly); tool_namespace/model/
// provider/provider_alias use the event value when non-empty, else fall back to the
// prior persisted value (the COALESCE/NULLIF empty-as-prior rule). When the op is a
// genuine new insert (prior absent) the prior fields are empty, so the fallback is a
// no-op and the effective identity equals the event identity.
func effectiveOpIdentity(ev canonical.OpStartedEvent, prior priorOpIdentity) effectiveCatalogIdentity {
	coalesce := func(evVal, priorVal string) string {
		if evVal != "" {
			return evVal
		}
		return priorVal
	}
	return effectiveCatalogIdentity{
		kind:          string(ev.Kind),
		name:          ev.Name,
		toolNamespace: coalesce(ev.ToolNamespace, prior.toolNamespace),
		model:         coalesce(ev.Model, prior.model),
		provider:      coalesce(ev.Provider, prior.provider),
		providerAlias: coalesce(ev.ProviderAlias, prior.providerAlias),
	}
}

// catalogIdentityChanged reports whether a re-emitted OpStarted lands on a
// DIFFERENT catalog row than the op's prior persisted identity, so onOpStarted
// migrates the op's contribution instead of double-counting it (SOW-0004 I1). The
// comparison mirrors the catalog keying exactly: LLM ops key on
// (provider, alias, model); tool ops key on (namespace-normalized-to-builtin,
// name); a changed KIND always counts as changed. The EFFECTIVE post-upsert
// identity (empty event fields fall back to the prior persisted value) is compared
// against the columns the prior op contributed under — so an empty-but-unchanged
// re-emit, which the ops upsert COALESCEs back to the prior value, is correctly
// detected as UNCHANGED and triggers no migration (SOW-0004 I1 shared-ingest edge).
func (c *catalogWriter) catalogIdentityChanged(eff effectiveCatalogIdentity, prior priorOpIdentity) bool {
	if eff.kind != prior.kind {
		return true
	}
	switch eff.kind {
	case string(canonical.OpLLM):
		return eff.provider != prior.provider ||
			eff.providerAlias != prior.providerAlias ||
			eff.model != prior.model
	case string(canonical.OpTool):
		return normalizeToolNamespace(eff.toolNamespace) != normalizeToolNamespace(prior.toolNamespace) ||
			eff.name != prior.name
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
// OpFinalized (this UPDATE only moves accumulating totals). The destination key is
// the EFFECTIVE post-upsert identity so it always matches the ops row and the key
// the call_count upsert booked under (SOW-0004 I1 shared-ingest edge).
func (c *catalogWriter) addMigratedTotals(ctx context.Context, tx *sql.Tx, eff effectiveCatalogIdentity, t opPriorTotals) error {
	// t is the prior PERSISTED contribution; the caller only reaches here when the
	// op row already existed (prior.found), so t.found is always true. An op started
	// but never finalized has status="running" (failureInc 0) and zero tokens/cost/
	// duration, so the adds below move nothing meaningful — only the call_count the
	// OpStarted upsert already re-booked under the new key matters in that case.
	failure := failureInc(t.status)
	switch eff.kind {
	case string(canonical.OpLLM):
		if eff.provider != "" {
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
				eff.provider, eff.providerAlias); err != nil {
				return fmt.Errorf("catalog_providers migrate-in: %w", err)
			}
		}
		if eff.provider != "" && eff.model != "" {
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
				eff.provider, eff.model); err != nil {
				return fmt.Errorf("catalog_models migrate-in: %w", err)
			}
		}
	case string(canonical.OpTool):
		if eff.name != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE catalog_tools SET
    failure_count     = failure_count + ?,
    total_tokens_in   = total_tokens_in + ?,
    total_tokens_out  = total_tokens_out + ?,
    total_cost_usd    = total_cost_usd + ?,
    total_duration_us = total_duration_us + ?
WHERE namespace = ? AND name = ?
`, failure, t.tokensIn, t.tokensOut, t.costUSD, t.durationUS,
				normalizeToolNamespace(eff.toolNamespace), eff.name); err != nil {
				return fmt.Errorf("catalog_tools migrate-in: %w", err)
			}
		}
	}
	return nil
}
