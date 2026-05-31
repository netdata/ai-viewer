import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { Filters } from '../state/filters';
import type { TopologyMetric, TopologyResponse } from './types';

// Cross-session topology endpoint + hook (GET /api/topology — rest-api.md).
// Mirrors api/sessions.ts fetchSessionTopology/useSessionTopology but the scope
// is the active FilterBar filter, not one session tree: the server returns one
// agent node per matching session (no tool nodes) plus lineage edges, capped at
// a node ceiling with a top-level `truncated` flag. The response shape is the
// SAME TopologyResponse the per-session route returns (so it feeds the shared
// TopologyRenderer + viz/topology unchanged); `truncated` is an optional extra
// field surfaced to the page for the "showing top N of M" notice.

/** topologyQueryKey is the cache key for the cross-session topology. */
export function topologyQueryKey(filters: Filters, metric: TopologyMetric) {
  return ['topology', metric, filters] as const;
}

/**
 * topologyFiltersToQuery maps the URL-synced Filters + size metric into the
 * /api/topology query string. It mirrors api/sessions.ts filtersToQuery (the
 * server accepts the identical filter params) and appends `metric`. No cursor:
 * the cross-session topology is not paginated — it caps the node set instead.
 */
export function topologyFiltersToQuery(filters: Filters, metric: TopologyMetric): string {
  return buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q: filters.q,
    metric,
  });
}

/** fetchTopology GETs the cross-session actor graph for the active filter + metric. */
export function fetchTopology(
  filters: Filters,
  metric: TopologyMetric,
  signal?: AbortSignal,
): Promise<TopologyResponse> {
  return get<TopologyResponse>(`/topology${topologyFiltersToQuery(filters, metric)}`, signal);
}

/**
 * useTopology is the query hook for the cross-session /topology page. The query
 * key carries both the metric and the filter so changing either refetches; SSE
 * session_changed/resync invalidations target ['topology'] (see useLiveUpdates
 * wiring on the page) to keep the graph live.
 */
export function useTopology(
  filters: Filters,
  metric: TopologyMetric,
): UseQueryResult<TopologyResponse> {
  return useQuery({
    queryKey: topologyQueryKey(filters, metric),
    queryFn: ({ signal }) => fetchTopology(filters, metric, signal),
  });
}
