import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { Filters } from '../state/filters';
import type {
  AggregateGroupBy,
  AggregateResponse,
  SearchResponse,
  StatsBucket,
  StatsMetric,
  StatsResponse,
  TopDimension,
  TopResponse,
} from './types';

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

// ── /stats dashboard chart endpoints (SOW-0007 Chunk 9) ──────────────────────
//
// The aggregate + top endpoints reuse the SAME filter set as /api/sessions
// (parseSessionFilter) plus their own selectors. Both are keyed under the
// ['stats', …] prefix on PURPOSE: api/sse.ts onStatsInvalidated invalidates
// queryKey ['stats'] wholesale, and TanStack Query treats that as a prefix —
// so every aggregate/top sub-key live-refreshes on a stats_invalidated frame
// with NO change to sse.ts. Search is keyed under its own ['search', …] prefix
// (its live-refresh wiring is decided in Chunk 9b) and is disabled on empty q.

/** Selectors for /api/stats/aggregate (rest-api.md §GET /api/stats/aggregate). */
export interface AggregateOptions {
  bucket: StatsBucket;
  groupBy: AggregateGroupBy;
  metric: StatsMetric;
}

/**
 * aggregateQueryKey keys the time-series aggregate. The ['stats'] prefix is what
 * api/sse.ts invalidates on stats_invalidated, so the chart auto-refreshes; the
 * remaining segments make every (filter, bucket, group_by, metric) combination a
 * distinct cache entry.
 */
export function aggregateQueryKey(filters: Filters, opts: AggregateOptions) {
  return ['stats', 'aggregate', filters, opts.bucket, opts.groupBy, opts.metric] as const;
}

/** fetchAggregate GETs the time-series aggregate for the filter + selectors. */
export function fetchAggregate(
  filters: Filters,
  opts: AggregateOptions,
  signal?: AbortSignal,
): Promise<AggregateResponse> {
  const qs = buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q: filters.q,
    bucket: opts.bucket,
    group_by: opts.groupBy,
    metric: opts.metric,
  });
  return get<AggregateResponse>(`/stats/aggregate${qs}`, signal);
}

/** useAggregate is the query hook for the dashboard's line chart. */
export function useAggregate(
  filters: Filters,
  opts: AggregateOptions,
): UseQueryResult<AggregateResponse> {
  return useQuery({
    queryKey: aggregateQueryKey(filters, opts),
    queryFn: ({ signal }) => fetchAggregate(filters, opts, signal),
  });
}

/** Selectors for /api/stats/top (rest-api.md §GET /api/stats/top). */
export interface TopOptions {
  dimension: TopDimension;
  metric: StatsMetric;
  /** Top-N size; the server clamps to [1, 200]. */
  n: number;
}

/**
 * topQueryKey keys the top-N ranking. Like aggregateQueryKey it lives under the
 * ['stats'] prefix so the SSE stats_invalidated handler refreshes it.
 */
export function topQueryKey(filters: Filters, opts: TopOptions) {
  return ['stats', 'top', filters, opts.dimension, opts.metric, opts.n] as const;
}

/** fetchTop GETs the top-N ranking for the filter + selectors. */
export function fetchTop(
  filters: Filters,
  opts: TopOptions,
  signal?: AbortSignal,
): Promise<TopResponse> {
  const qs = buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q: filters.q,
    dimension: opts.dimension,
    metric: opts.metric,
    n: opts.n,
  });
  return get<TopResponse>(`/stats/top${qs}`, signal);
}

/** useTop is the query hook for the dashboard's horizontal bar chart. */
export function useTop(filters: Filters, opts: TopOptions): UseQueryResult<TopResponse> {
  return useQuery({
    queryKey: topQueryKey(filters, opts),
    queryFn: ({ signal }) => fetchTop(filters, opts, signal),
  });
}

/** Optional pagination for /api/search (rest-api.md §GET /api/search). */
export interface SearchOptions {
  /** Page size; the server defaults to 50 and clamps to [1, 200]. */
  limit?: number;
  /** Opaque pagination cursor (offset-style; mirrors the logs endpoint). */
  cursor?: string;
}

/**
 * searchQueryKey keys the deep full-text search. It does NOT share the ['stats']
 * prefix — search results are not invalidated by stats_invalidated (the FTS
 * tables and the rollups have independent refresh paths); Chunk 9b decides its
 * live wiring. `q` is part of the key so each query string caches separately.
 */
export function searchQueryKey(filters: Filters, q: string, limit: number) {
  return ['search', filters, q, limit] as const;
}

/** SEARCH_LIMIT_DEFAULT mirrors the server default page size (rest-api.md). */
const SEARCH_LIMIT_DEFAULT = 50;

/** fetchSearch GETs ranked op/log matches for the query within the filter. */
export function fetchSearch(
  filters: Filters,
  q: string,
  opts: SearchOptions = {},
  signal?: AbortSignal,
): Promise<SearchResponse> {
  const limit = opts.limit ?? SEARCH_LIMIT_DEFAULT;
  const qs = buildQuery({
    agents: filters.agents,
    models: filters.models,
    tools: filters.tools,
    status: filters.status,
    sources: filters.sources,
    from: filters.from,
    to: filters.to,
    q,
    limit,
    cursor: opts.cursor,
  });
  return get<SearchResponse>(`/search${qs}`, signal);
}

/**
 * useSearch is the query hook for the deep-search box. It is DISABLED until `q`
 * has non-whitespace content, so an empty box never fires a request (the server
 * rejects an empty `q` with BAD_REQUEST anyway — rest-api.md). The cache key uses
 * the resolved limit so a default-limit query and an explicit one share an entry.
 */
export function useSearch(
  filters: Filters,
  q: string,
  opts: SearchOptions = {},
): UseQueryResult<SearchResponse> {
  const limit = opts.limit ?? SEARCH_LIMIT_DEFAULT;
  return useQuery({
    queryKey: searchQueryKey(filters, q, limit),
    queryFn: ({ signal }) => fetchSearch(filters, q, opts, signal),
    enabled: q.trim().length > 0,
  });
}
