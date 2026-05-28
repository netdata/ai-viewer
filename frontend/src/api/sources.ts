import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get } from './client';
import type { HealthResponse, SourcesResponse } from './types';

// Sources + health endpoints. The Sources admin panel renders both (ui-pages.md
// §/sources). The query keys are ['sources'] and ['health'] so the SSE
// source_status_changed event can invalidate them.

/** fetchSources GETs the full source list with cursor metadata. */
export function fetchSources(signal?: AbortSignal): Promise<SourcesResponse> {
  return get<SourcesResponse>('/sources', signal);
}

/** useSources is the query hook for the Sources panel. */
export function useSources(): UseQueryResult<SourcesResponse> {
  return useQuery({
    queryKey: ['sources'] as const,
    queryFn: ({ signal }) => fetchSources(signal),
  });
}

/** fetchHealth GETs the server health snapshot. */
export function fetchHealth(signal?: AbortSignal): Promise<HealthResponse> {
  return get<HealthResponse>('/health', signal);
}

/** useHealth is the query hook for the health panel. */
export function useHealth(): UseQueryResult<HealthResponse> {
  return useQuery({
    queryKey: ['health'] as const,
    queryFn: ({ signal }) => fetchHealth(signal),
  });
}
