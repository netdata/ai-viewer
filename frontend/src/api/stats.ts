import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { Filters } from '../state/filters';
import type { StatsResponse } from './types';

// Stats endpoint + hook. /api/stats takes the same filter set as the session
// list (rest-api.md §GET /api/stats). The query key is ['stats', filters] so
// the SSE stats_invalidated event can invalidate it wholesale.
//
// CROSS-SESSION ONLY. /api/stats has no session_id filter — its handler uses
// parseSessionFilter (internal/presenter/stats.go:83), which does not read a
// session_id param, so any such param would be silently ignored and yield
// cross-session totals. Per-session aggregates therefore come from
// GET /api/sessions/:id (useSessionDetail), whose session row already carries
// tokens_in/out, cost_usd, turn_count, op_count, and failure_count.

/** statsQueryKey is the cache key for the cross-session stats aggregates. */
export function statsQueryKey(filters: Filters) {
  return ['stats', filters] as const;
}

/** fetchStats GETs the cross-session aggregate stats for the filtered set. */
export function fetchStats(
  filters: Filters,
  signal?: AbortSignal,
): Promise<StatsResponse> {
  const qs = buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q: filters.q,
  });
  return get<StatsResponse>(`/stats${qs}`, signal);
}

/**
 * useStats is the query hook for the cross-session stats endpoint. It is NOT
 * session-scoped: /api/stats has no session_id filter, so for per-session
 * aggregates use useSessionDetail(id) (the session-detail endpoint) instead.
 */
export function useStats(filters: Filters): UseQueryResult<StatsResponse> {
  return useQuery({
    queryKey: statsQueryKey(filters),
    queryFn: ({ signal }) => fetchStats(filters, signal),
  });
}
