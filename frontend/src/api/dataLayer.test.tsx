import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import {
  fetchSessionDetail,
  fetchSessions,
  fetchSessionsPage,
  filtersToQuery,
  sessionsQueryKey,
  useSessionDetail,
  useSessions,
  useSessionsInfinite,
} from './sessions';
import { fetchStats, statsQueryKey, useStats } from './stats';
import { fetchHealth, fetchSources, useHealth, useSources } from './sources';
import type { Filters } from '../state/filters';

// The data layer (sessions/stats/sources) is thin: pure query-key + query-string
// builders plus useQuery hooks. These tests cover both — the pure builders
// directly, and the hooks through a QueryClientProvider with mocked fetch — so
// the SSE invalidation keys and request URLs are pinned to the spec
// (rest-api.md, frontend-architecture.md §SSE Integration query keys).

const EMPTY: Filters = {
  agents: [],
  models: [],
  tools: [],
  status: [],
  sources: [],
};

/** captureUrl installs a fetch mock recording the requested URL. */
function captureUrl(body: unknown): { calls: string[] } {
  const calls: string[] = [];
  const mock = vi.fn((url: string) => {
    calls.push(url);
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

/** wrapper provides a fresh QueryClient (retries off for fast failure paths). */
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

describe('sessions data layer', () => {
  it('sessionsQueryKey is stable and group-scoped', () => {
    expect(sessionsQueryKey(EMPTY, 'root')).toEqual(['sessions', 'root', EMPTY]);
    expect(sessionsQueryKey(EMPTY, 'all')).toEqual(['sessions', 'all', EMPTY]);
  });

  it('filtersToQuery serializes filters + group', () => {
    const qs = filtersToQuery(
      { ...EMPTY, agents: ['a', 'b'], q: 'nedi', from: 10 },
      'all',
    );
    expect(qs).toContain('agents=a%2Cb');
    expect(qs).toContain('q=nedi');
    expect(qs).toContain('from=10');
    expect(qs).toContain('group=all');
  });

  it('fetchSessions GETs /api/sessions with the query string', async () => {
    const { calls } = captureUrl({ items: [], next_cursor: undefined });
    await fetchSessions({ ...EMPTY, status: ['failed'] }, 'root');
    expect(calls[0]).toBe('/api/sessions?status=failed&group=root');
  });

  it('fetchSessionDetail encodes the id', async () => {
    const { calls } = captureUrl({ session: {}, turns: [], child_sessions: [] });
    await fetchSessionDetail('sess/1');
    expect(calls[0]).toBe('/api/sessions/sess%2F1');
  });

  it('useSessions resolves the list', async () => {
    captureUrl({ items: [{ id: 'x' }], next_cursor: undefined });
    const { result } = renderHook(() => useSessions(EMPTY), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items[0]?.id).toBe('x');
  });

  it('useSessionDetail is disabled for an empty id', () => {
    captureUrl({ session: {}, turns: [], child_sessions: [] });
    const { result } = renderHook(() => useSessionDetail(''), { wrapper });
    // enabled:false → never fetches; stays pending without firing.
    expect(result.current.fetchStatus).toBe('idle');
  });

  it('useSessionDetail fetches a non-empty id', async () => {
    captureUrl({ session: { id: 's1' }, turns: [], child_sessions: [] });
    const { result } = renderHook(() => useSessionDetail('s1'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.session.id).toBe('s1');
  });
});

describe('sessions keyset pagination', () => {
  /** captureSeq replies with successive bodies from `responses` (last repeats). */
  function captureSeq(responses: unknown[]): { calls: string[] } {
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

  it('filtersToQuery appends a non-empty cursor but omits an empty one', () => {
    expect(filtersToQuery(EMPTY, 'root')).not.toContain('cursor');
    expect(filtersToQuery(EMPTY, 'root', '')).not.toContain('cursor');
    expect(filtersToQuery(EMPTY, 'root', 'cur-2')).toContain('cursor=cur-2');
  });

  it('fetchSessionsPage GETs the list with the cursor', async () => {
    const { calls } = captureSeq([{ items: [] }]);
    await fetchSessionsPage(EMPTY, 'root', 'cur-2');
    expect(calls[0]).toBe('/api/sessions?group=root&cursor=cur-2');
  });

  it('useSessionsInfinite shares the list query key (SSE invalidation target)', async () => {
    captureSeq([{ items: [{ id: 'a' }] }]);
    const { result } = renderHook(() => useSessionsInfinite(EMPTY, 'root'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // First page omits the cursor (empty sentinel).
    expect(result.current.data?.pages[0]?.items[0]?.id).toBe('a');
  });

  it('useSessionsInfinite appends the next page via next_cursor, then stops', async () => {
    const { calls } = captureSeq([
      { items: [{ id: 'a' }], next_cursor: 'cur-2' },
      { items: [{ id: 'b' }] },
    ]);
    const { result } = renderHook(() => useSessionsInfinite(EMPTY, 'root'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.hasNextPage).toBe(true);

    let merged!: Awaited<ReturnType<typeof result.current.fetchNextPage>>;
    await act(async () => {
      merged = await result.current.fetchNextPage();
    });

    // First page had no cursor; second replayed cur-2.
    expect(calls[0]).toBe('/api/sessions?group=root');
    expect(calls[1]).toBe('/api/sessions?group=root&cursor=cur-2');
    expect(merged.data?.pages).toHaveLength(2);
    expect(merged.data?.pages[1]?.items[0]?.id).toBe('b');
    expect(merged.hasNextPage).toBe(false);
  });
});

describe('stats data layer', () => {
  it('statsQueryKey is cross-session only (no session scope)', () => {
    expect(statsQueryKey(EMPTY)).toEqual(['stats', EMPTY]);
  });

  it('fetchStats serializes filters and never sends session_id', async () => {
    const { calls } = captureUrl({});
    await fetchStats({ ...EMPTY, models: ['m'] });
    expect(calls[0]).toContain('models=m');
    // /api/stats has no session_id filter (internal/presenter/stats.go:83);
    // a session_id param would be silently ignored, so it must not be sent.
    expect(calls[0]).not.toContain('session_id');
  });

  it('useStats resolves the aggregates', async () => {
    captureUrl({ totals: { session_count: 3 } });
    const { result } = renderHook(() => useStats(EMPTY), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.totals.session_count).toBe(3);
  });
});

describe('sources data layer', () => {
  it('fetchSources GETs /api/sources', async () => {
    const { calls } = captureUrl({ items: [] });
    await fetchSources();
    expect(calls[0]).toBe('/api/sources');
  });

  it('fetchHealth GETs /api/health', async () => {
    const { calls } = captureUrl({ status: 'ok' });
    await fetchHealth();
    expect(calls[0]).toBe('/api/health');
  });

  it('useSources resolves the list', async () => {
    captureUrl({ items: [{ id: 'src1' }] });
    const { result } = renderHook(() => useSources(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items[0]?.id).toBe('src1');
  });

  it('useHealth resolves the snapshot', async () => {
    captureUrl({ status: 'ok' });
    const { result } = renderHook(() => useHealth(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.status).toBe('ok');
  });
});
