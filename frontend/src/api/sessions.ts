import {
  useQuery,
  useInfiniteQuery,
  type UseQueryResult,
  type UseInfiniteQueryResult,
  type InfiniteData,
} from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { Filters } from '../state/filters';
import type {
  SessionDetailResponse,
  SessionListResponse,
  TimelineResponse,
  TopologyMetric,
  TopologyResponse,
} from './types';

// Session endpoints + TanStack Query hooks. Query keys are structured so SSE
// invalidation can target them precisely (frontend-architecture.md §SSE
// Integration: ['sessions'] for the list, ['session', id] for a detail).

/** sessionsQueryKey is the cache key for the filtered session list. */
export function sessionsQueryKey(filters: Filters, group: 'root' | 'all') {
  return ['sessions', group, filters] as const;
}

/**
 * filtersToQuery maps the URL-synced Filters into the list query string. An
 * optional keyset `cursor` is appended for pages after the first (an empty /
 * absent cursor means "first page" — rest-api.md §Conventions); the cursor is
 * bound server-side to the exact filter+group it was minted under, so it is only
 * ever replayed against the identical query.
 */
export function filtersToQuery(
  filters: Filters,
  group: 'root' | 'all',
  cursor?: string,
): string {
  return buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q: filters.q,
    group,
    cursor: cursor !== undefined && cursor !== '' ? cursor : undefined,
  });
}

/** fetchSessions GETs the filtered, paginated session list. */
export function fetchSessions(
  filters: Filters,
  group: 'root' | 'all',
  signal?: AbortSignal,
): Promise<SessionListResponse> {
  return get<SessionListResponse>(
    `/sessions${filtersToQuery(filters, group)}`,
    signal,
  );
}

/** useSessions is the single-page query hook for the session list. */
export function useSessions(
  filters: Filters,
  group: 'root' | 'all' = 'root',
): UseQueryResult<SessionListResponse> {
  return useQuery({
    queryKey: sessionsQueryKey(filters, group),
    queryFn: ({ signal }) => fetchSessions(filters, group, signal),
  });
}

/** fetchSessionsPage GETs one keyset page of the list (optionally with a cursor). */
export function fetchSessionsPage(
  filters: Filters,
  group: 'root' | 'all',
  cursor: string,
  signal?: AbortSignal,
): Promise<SessionListResponse> {
  return get<SessionListResponse>(
    `/sessions${filtersToQuery(filters, group, cursor)}`,
    signal,
  );
}

/**
 * useSessionsInfinite is the keyset-paginated list hook the SessionsList page
 * uses for its "Load more" control. Pages are appended (never reset on cursor —
 * a filter change mints a fresh query and a fresh first page, which is the only
 * safe way to replay the server's query-bound cursor, rest-api.md §Conventions).
 * Query key matches useSessions (['sessions', group, filters]) so the SSE
 * session_changed/resync invalidations refresh it.
 */
export function useSessionsInfinite(
  filters: Filters,
  group: 'root' | 'all' = 'root',
): UseInfiniteQueryResult<InfiniteData<SessionListResponse>, Error> {
  return useInfiniteQuery({
    queryKey: sessionsQueryKey(filters, group),
    queryFn: ({ pageParam, signal }) =>
      fetchSessionsPage(filters, group, pageParam, signal),
    // '' = first page (empty cursor); a non-string initial param confuses
    // useInfiniteQuery's page tracking, and the request omits an empty cursor.
    initialPageParam: '',
    getNextPageParam: (last: SessionListResponse) => last.next_cursor ?? undefined,
  });
}

/** fetchSessionDetail GETs the full session detail (session/turns/children). */
export function fetchSessionDetail(
  id: string,
  signal?: AbortSignal,
): Promise<SessionDetailResponse> {
  return get<SessionDetailResponse>(
    `/sessions/${encodeURIComponent(id)}`,
    signal,
  );
}

/** useSessionDetail is the query hook for the session detail page. */
export function useSessionDetail(
  id: string,
): UseQueryResult<SessionDetailResponse> {
  return useQuery({
    queryKey: ['session', id] as const,
    queryFn: ({ signal }) => fetchSessionDetail(id, signal),
    enabled: id.length > 0,
  });
}

/**
 * fetchSessionTopology GETs the per-session actor graph (the whole session tree
 * resolved server-side) for the given size `metric` (rest-api.md §GET
 * /api/sessions/:id/topology). The server returns the layout-agnostic node/edge
 * model; the client picks the layout in viz/topology.
 */
export function fetchSessionTopology(
  id: string,
  metric: TopologyMetric,
  signal?: AbortSignal,
): Promise<TopologyResponse> {
  return get<TopologyResponse>(
    `/sessions/${encodeURIComponent(id)}/topology${buildQuery({ metric })}`,
    signal,
  );
}

/**
 * useSessionTopology is the query hook for the per-session Topology tab. The
 * query key carries the metric so changing the size dimension refetches; the
 * SSE session_changed invalidation targets ['session', id] (the detail), so the
 * topology key is kept distinct to avoid coupling the two refetch cadences.
 */
export function useSessionTopology(
  id: string,
  metric: TopologyMetric,
): UseQueryResult<TopologyResponse> {
  return useQuery({
    queryKey: ['session-topology', id, metric] as const,
    queryFn: ({ signal }) => fetchSessionTopology(id, metric, signal),
    enabled: id.length > 0,
  });
}

/**
 * fetchTimeline GETs the per-session timeline (the whole session tree resolved
 * server-side: one lane per session, each lane's ordered spans, plus the overall
 * t_start/t_end window — rest-api.md §GET /api/sessions/:id/timeline). The client
 * computes the span geometry in viz/timeline.
 */
export function fetchTimeline(
  id: string,
  signal?: AbortSignal,
): Promise<TimelineResponse> {
  return get<TimelineResponse>(
    `/sessions/${encodeURIComponent(id)}/timeline`,
    signal,
  );
}

/**
 * useTimeline is the query hook for the per-session Timeline tab. Mirrors
 * useSessionTopology: a distinct query key (['session-timeline', id]) keeps its
 * refetch cadence independent of the detail's ['session', id] SSE invalidation.
 */
export function useTimeline(id: string): UseQueryResult<TimelineResponse> {
  return useQuery({
    queryKey: ['session-timeline', id] as const,
    queryFn: ({ signal }) => fetchTimeline(id, signal),
    enabled: id.length > 0,
  });
}
