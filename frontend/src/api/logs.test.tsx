import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import { fetchSessionLogs, logsQueryKey, useSessionLogs } from './logs';
import type { LogsResponse } from './types';

// useSessionLogs is the keyset-paginated Logs hook (rest-api.md §GET
// /api/sessions/:id/logs). These tests pin: the query KEY (severity set is part
// of it because the cursor is bound to it server-side), the request URL params
// (severity sent only when non-empty; cursor only on later pages), the
// present-but-empty severity rule (empty array = no param = all severities),
// and cursor pagination (next_cursor → the next pageParam; absent → paging stops).

/** captureUrl installs a fetch mock recording each requested URL, replying with
 *  successive bodies from `responses` (last one repeats). */
function captureUrl(responses: unknown[]): { calls: string[] } {
  const calls: string[] = [];
  let i = 0;
  const mock = vi.fn((url: string) => {
    calls.push(url);
    const body = responses[Math.min(i, responses.length - 1)];
    i += 1;
    return Promise.resolve({
      ok: true,
      status: 200,
      statusText: 'ok',
      headers: { get: () => null },
      json: () => Promise.resolve(body),
    } as unknown as Response);
  });
  vi.stubGlobal('fetch', mock);
  return { calls };
}

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const PAGE1: LogsResponse = {
  items: [{ ts: 1, severity: 'INF', source: 's', op_id: null, message: 'a', extras: null }],
  next_cursor: 'cur-2',
};
const PAGE2: LogsResponse = {
  items: [{ ts: 2, severity: 'WRN', source: 's', op_id: null, message: 'b', extras: null }],
  // no next_cursor → paging stops
};

describe('logs query key', () => {
  it('includes the session id and severity set', () => {
    expect(logsQueryKey('s1', [])).toEqual(['logs', 's1', []]);
    expect(logsQueryKey('s1', ['WRN', 'ERR'])).toEqual(['logs', 's1', ['WRN', 'ERR']]);
  });
});

describe('fetchSessionLogs URL params', () => {
  it('encodes the id and sends no severity when the set is empty (= all)', async () => {
    const { calls } = captureUrl([PAGE2]);
    await fetchSessionLogs('sess/1', { severities: [] });
    // Empty severities → the param is OMITTED (present-but-empty is BAD_REQUEST).
    expect(calls[0]).toBe('/api/sessions/sess%2F1/logs');
    expect(calls[0]).not.toContain('severity');
  });

  it('sends a comma-joined severity param when present', async () => {
    const { calls } = captureUrl([PAGE2]);
    await fetchSessionLogs('s1', { severities: ['WRN', 'ERR'] });
    expect(calls[0]).toContain('severity=WRN%2CERR');
  });

  it('sends cursor and limit when provided', async () => {
    const { calls } = captureUrl([PAGE2]);
    await fetchSessionLogs('s1', { severities: ['ERR'], cursor: 'cur-2', limit: 50 });
    expect(calls[0]).toContain('cursor=cur-2');
    expect(calls[0]).toContain('limit=50');
  });
});

describe('useSessionLogs', () => {
  it('is disabled for an empty id (never fetches)', () => {
    captureUrl([PAGE2]);
    const { result } = renderHook(() => useSessionLogs(''), { wrapper });
    expect(result.current.fetchStatus).toBe('idle');
  });

  it('resolves the first page and exposes the next cursor', async () => {
    const { calls } = captureUrl([PAGE1, PAGE2]);
    const { result } = renderHook(() => useSessionLogs('s1', { severities: ['WRN'] }), {
      wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.pages[0]?.items[0]?.message).toBe('a');
    expect(result.current.hasNextPage).toBe(true);
    // First page must NOT carry a cursor param; severity is present.
    expect(calls[0]).toContain('severity=WRN');
    expect(calls[0]).not.toContain('cursor');
  });

  it('fetchNextPage appends the second page using next_cursor, then stops', async () => {
    const { calls } = captureUrl([PAGE1, PAGE2]);
    const { result } = renderHook(() => useSessionLogs('s1'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    // fetchNextPage resolves with the merged InfiniteData — assert on its
    // return (the documented public surface) so the test does not depend on the
    // renderHook snapshot timing of the post-fetch re-render.
    let merged!: Awaited<ReturnType<typeof result.current.fetchNextPage>>;
    await act(async () => {
      merged = await result.current.fetchNextPage();
    });

    // Second request replayed the cursor minted by page 1 (keyset pagination).
    expect(calls[1]).toContain('cursor=cur-2');
    // Both pages present and appended in order (page 1 then page 2).
    expect(merged.data?.pages).toHaveLength(2);
    expect(merged.data?.pages[0]?.items[0]?.message).toBe('a');
    expect(merged.data?.pages[1]?.items[0]?.message).toBe('b');
    // Page 2 carried no next_cursor → paging stops.
    expect(merged.data?.pageParams).toEqual(['', 'cur-2']);
    expect(merged.hasNextPage).toBe(false);
  });
});
