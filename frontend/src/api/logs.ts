import {
  useInfiniteQuery,
  type UseInfiniteQueryResult,
  type InfiniteData,
} from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { LogsResponse } from './types';

// Session-logs endpoint + hook. GET /api/sessions/:id/logs is keyset-paginated
// (rest-api.md §GET /api/sessions/:id/logs): the opaque `cursor` is bound to the
// session id + the severity set, so the query KEY carries the severities and a
// severity change starts a fresh first page rather than replaying a stale cursor
// (which the server rejects with BAD_REQUEST).
//
// Present-but-empty severity rule (rest-api.md §Conventions): an absent
// `severity` key means "all severities"; `?severity=` / `?severity=,` is a
// BAD_REQUEST. An empty `severities` array therefore OMITS the param entirely —
// buildQuery drops empty arrays — so the client never sends the rejected form.

/** Options for a logs fetch. `severities` empty/absent = all severities. */
export interface LogsQueryOptions {
  severities?: string[];
  cursor?: string;
  limit?: number;
}

/** logsQueryKey is the cache key for a session's filtered log stream. The
 *  severity set is part of the key (the cursor is bound to it server-side). */
export function logsQueryKey(id: string, severities: string[]) {
  return ['logs', id, severities] as const;
}

/**
 * fetchSessionLogs GETs one keyset page of a session's logs. `severity` is sent
 * only when non-empty (absent = all); `cursor` is sent only for pages after the
 * first (an empty/absent cursor means "first page" — rest-api.md §Conventions);
 * `limit` overrides the server default when provided.
 */
export function fetchSessionLogs(
  id: string,
  opts: LogsQueryOptions = {},
  signal?: AbortSignal,
): Promise<LogsResponse> {
  const qs = buildQuery({
    severity: opts.severities,
    // Omit an empty cursor (buildQuery sends a present empty string verbatim).
    cursor: opts.cursor !== undefined && opts.cursor !== '' ? opts.cursor : undefined,
    limit: opts.limit,
  });
  return get<LogsResponse>(
    `/sessions/${encodeURIComponent(id)}/logs${qs}`,
    signal,
  );
}

/**
 * useSessionLogs is the infinite-query hook for the Logs tab. Pages are fetched
 * with the keyset cursor (`next_cursor` → the next `pageParam`); `getNextPageParam`
 * returns undefined when the server omits `next_cursor`, which stops paging.
 * Callers flatten `data.pages[].items` for rendering. Disabled for an empty id.
 */
export function useSessionLogs(
  id: string,
  opts: { severities?: string[] } = {},
): UseInfiniteQueryResult<InfiniteData<LogsResponse>, Error> {
  const severities = opts.severities ?? [];
  return useInfiniteQuery({
    queryKey: logsQueryKey(id, severities),
    queryFn: ({ pageParam, signal }) =>
      fetchSessionLogs(id, { severities, cursor: pageParam }, signal),
    // '' is the "first page" sentinel (an empty cursor = first page per the REST
    // contract). A non-string initial param (undefined) confuses useInfiniteQuery's
    // page-param tracking, so we use the empty string and omit it in the request.
    initialPageParam: '',
    getNextPageParam: (last: LogsResponse) => last.next_cursor ?? undefined,
    enabled: id.length > 0,
  });
}
