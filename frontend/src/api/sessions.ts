import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { Filters } from '../state/filters';
import type { SessionDetailResponse, SessionListResponse } from './types';

// Session endpoints + TanStack Query hooks. Query keys are structured so SSE
// invalidation can target them precisely (frontend-architecture.md §SSE
// Integration: ['sessions'] for the list, ['session', id] for a detail).

/** sessionsQueryKey is the cache key for the filtered session list. */
export function sessionsQueryKey(filters: Filters, group: 'root' | 'all') {
  return ['sessions', group, filters] as const;
}

/** filtersToQuery maps the URL-synced Filters into the list query string. */
export function filtersToQuery(
  filters: Filters,
  group: 'root' | 'all',
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

/** useSessions is the query hook for the session list page. */
export function useSessions(
  filters: Filters,
  group: 'root' | 'all' = 'root',
): UseQueryResult<SessionListResponse> {
  return useQuery({
    queryKey: sessionsQueryKey(filters, group),
    queryFn: ({ signal }) => fetchSessions(filters, group, signal),
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
