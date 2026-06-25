import {
  useQuery,
  useInfiniteQuery,
  type UseQueryResult,
  type UseInfiniteQueryResult,
  type InfiniteData,
} from '@tanstack/react-query';
import { get, buildIncludeQuery, buildQuery, type IncludeToken } from './client';
import type { Filters } from '../state/filters';
import type {
  CompareResponse,
  PayloadRef,
  RelatedResponse,
  SessionDetailResponse,
  SessionListResponse,
  TimelineResponse,
  TopologyMetric,
  TopologyResponse,
  TraceResponse,
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
): UseInfiniteQueryResult<InfiniteData<SessionListResponse>> {
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

/** fetchSessionDetail GETs the full session detail (session/turns/children).
 *  Pass includePayloadRefs=true to add `?include=payload_refs` so the
 *  server includes each op's payload_refs metadata (1.5 refs/op on a
 *  typical session — ~30% of response size). The TurnView page passes
 *  true because it renders payload metadata inline; the unified view
 *  passes false because it fetches payloads lazily via /api/payloads/:id. */
export function fetchSessionDetail(
  id: string,
  opts: { includePayloadRefs?: boolean; includeProof?: boolean; signal?: AbortSignal } = {},
): Promise<SessionDetailResponse> {
  const include: IncludeToken[] = [];
  if (opts.includePayloadRefs) {
    include.push('payload_refs');
  }
  if (opts.includeProof) {
    include.push('proof');
  }
  const qs = buildIncludeQuery(include);
  return get<SessionDetailResponse>(
    `/sessions/${encodeURIComponent(id)}${qs}`,
    opts.signal,
  );
}

/** useSessionDetail is the query hook for the session detail page.
 *  includePayloadRefs defaults to false — the unified view / trace /
 *  topology fetch payloads lazily via /api/payloads/:id and don't need
 *  the per-op refs metadata upfront. The legacy TurnView (the right
 *  sidebar in the unified view) lazy-fetches the refs for the focused
 *  op via useOpPayloadRefs when ?op= is set. */
export function useSessionDetail(
  id: string,
  opts: { includePayloadRefs?: boolean } = {},
): UseQueryResult<SessionDetailResponse> {
  const includePayloadRefs = opts.includePayloadRefs ?? false;
  return useQuery({
    queryKey: ['session', id, includePayloadRefs] as const,
    queryFn: ({ signal }) =>
      fetchSessionDetail(id, { includePayloadRefs, signal }),
    enabled: id.length > 0,
  });
}

/** PayloadRefsEnvelope is the response shape for /api/sessions/:id/payload_refs.
 *  It is always non-nil: an empty match serializes as `[]`, not `null`,
 *  so callers iterate without a null guard. */
export interface PayloadRefsEnvelope {
  refs: PayloadRef[];
}

/** fetchOpPayloadRefs GETs the payload_refs for ONE op (SOW-0092 chunk 3).
 *  Tiny response (~169 bytes for a typical single-ref op) — TurnView
 *  uses this to lazy-fetch the refs for the focused op without paying
 *  the cost of the full per-session payload_refs scan. */
export function fetchOpPayloadRefs(
  sessionID: string,
  opID: string,
  opts: { includeProof?: boolean; signal?: AbortSignal } = {},
): Promise<PayloadRefsEnvelope> {
  const include = opts.includeProof ? buildIncludeQuery(['proof']) : '';
  const sep = include === '' ? '' : `&${include.slice(1)}`;
  return get<PayloadRefsEnvelope>(
    `/sessions/${encodeURIComponent(sessionID)}/payload_refs?op=${encodeURIComponent(opID)}${sep}`,
    opts.signal,
  );
}

/** useOpPayloadRefs lazy-loads the payload_refs for a single op. Disabled
 *  when opID is empty (no focused op). Cache key is per-op so revisiting
 *  a focused op doesn't refetch. */
export function useOpPayloadRefs(
  sessionID: string,
  opID: string | null,
): UseQueryResult<PayloadRefsEnvelope> {
  return useQuery({
    queryKey: ['opPayloadRefs', sessionID, opID ?? ''] as const,
    queryFn: ({ signal }) => fetchOpPayloadRefs(sessionID, opID!, { signal }),
    enabled: !!opID,
  });
}

/** fetchTurnPayloadRefs GETs the payload_refs for ALL ops in ONE turn.
 *  Tiny response (5-50 refs typical). The UnifiedView's TurnViewPane
 *  uses this to fetch the refs for sibling ops in the focused turn so
 *  the operator can navigate the turn's steps without per-op fetches. */
export function fetchTurnPayloadRefs(
  sessionID: string,
  turnID: string,
  opts: { includeProof?: boolean; signal?: AbortSignal } = {},
): Promise<PayloadRefsEnvelope> {
  const include = opts.includeProof ? buildIncludeQuery(['proof']) : '';
  const sep = include === '' ? '' : `&${include.slice(1)}`;
  return get<PayloadRefsEnvelope>(
    `/sessions/${encodeURIComponent(sessionID)}/payload_refs?turn=${encodeURIComponent(turnID)}${sep}`,
    opts.signal,
  );
}

/** useTurnPayloadRefs lazy-loads the payload_refs for one turn. Disabled
 *  when turnID is empty (no focused turn). Cache key is per-turn. */
export function useTurnPayloadRefs(
  sessionID: string,
  turnID: string | null,
): UseQueryResult<PayloadRefsEnvelope> {
  return useQuery({
    queryKey: ['turnPayloadRefs', sessionID, turnID ?? ''] as const,
    queryFn: ({ signal }) => fetchTurnPayloadRefs(sessionID, turnID!, { signal }),
    enabled: !!turnID,
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

/**
 * fetchSessionTrace GETs the whole-tree trace (every op of every session in the
 * resolved tree, tagged by owning session — rest-api.md §GET /api/sessions/:id/
 * trace, SOW-0070). The client builds a merged op tree from the flat op list.
 */
export function fetchSessionTrace(
  id: string,
  opts: { includePayloadRefs?: boolean; includeProof?: boolean; signal?: AbortSignal } = {},
): Promise<TraceResponse> {
  const include: IncludeToken[] = [];
  if (opts.includePayloadRefs) {
    include.push('payload_refs');
  }
  if (opts.includeProof) {
    include.push('proof');
  }
  const qs = buildIncludeQuery(include);
  return get<TraceResponse>(`/sessions/${encodeURIComponent(id)}/trace${qs}`, opts.signal);
}

/**
 * useSessionTrace is the query hook for the Trace tab's whole-tree op list.
 * Distinct query key (['session-trace', id]) keeps its refetch independent of
 * the detail's ['session', id] SSE invalidation.
 */
export function useSessionTrace(
  id: string,
  opts: { includePayloadRefs?: boolean; includeProof?: boolean } = {},
): UseQueryResult<TraceResponse> {
  const includePayloadRefs = opts.includePayloadRefs ?? false;
  const includeProof = opts.includeProof ?? false;
  return useQuery({
    queryKey: ['session-trace', id, includePayloadRefs, includeProof] as const,
    queryFn: ({ signal }) => fetchSessionTrace(id, { includePayloadRefs, includeProof, signal }),
    enabled: id.length > 0,
  });
}

/**
 * fetchSessionRelated GETs heuristic cross-harness soft links for a session
 * (SOW-0071): sessions from a different harness that started in the same cwd
 * while this session was running.
 */
export function fetchSessionRelated(id: string, signal?: AbortSignal): Promise<RelatedResponse> {
  return get<RelatedResponse>(`/sessions/${encodeURIComponent(id)}/related`, signal);
}

/**
 * useSessionRelated is the query hook for the Overview's "Possibly related"
 * section. Distinct query key (['session-related', id]).
 */
export function useSessionRelated(id: string): UseQueryResult<RelatedResponse> {
  return useQuery({
    queryKey: ['session-related', id] as const,
    queryFn: ({ signal }) => fetchSessionRelated(id, signal),
    enabled: id.length > 0,
  });
}

/**
 * fetchCompareSessions GETs a structured diff between 2-4 sessions
 * (SOW-0095). The endpoint is `GET /api/sessions/compare?ids=<csv>`;
 * the ids are joined with commas (no URL encoding inside the value;
 * the session ids are uuid-like and contain no commas in practice).
 * Server validates 2-4 ids; 1 id or 5+ ids → 400; unknown id → 404.
 */
export function fetchCompareSessions(ids: string[], signal?: AbortSignal): Promise<CompareResponse> {
  return get<CompareResponse>('/sessions/compare?ids=' + ids.map(encodeURIComponent).join(','), signal);
}

/**
 * useCompareSessions is the query hook for the /compare page. The hook
 * is `enabled` only when the id set is the contractually valid 2-4; the
 * page renders the empty / error state for other shapes (1 or 5+) and
 * does not trigger a request.
 */
export function useCompareSessions(ids: string[]): UseQueryResult<CompareResponse> {
  return useQuery({
    queryKey: ['compare', ids.join(',')] as const,
    queryFn: ({ signal }) => fetchCompareSessions(ids, signal),
    enabled: ids.length >= 2 && ids.length <= 4,
  });
}
