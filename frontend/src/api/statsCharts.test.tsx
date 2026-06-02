import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import {
  aggregateQueryKey,
  fetchAggregate,
  fetchSearch,
  fetchTop,
  searchQueryKey,
  topQueryKey,
  useAggregate,
  useSearch,
  useTop,
} from './stats';
import type { Filters } from '../state/filters';
import type {
  AggregateResponse,
  SearchResponse,
  TopResponse,
} from './types';

// Data layer for the /stats dashboard charts (GET /api/stats/aggregate, /top,
// /search — rest-api.md). Mirrors topology.test.tsx: the pure query-key builders
// directly, and each hook through a QueryClientProvider with a mocked fetch, so
// the request URL and the SSE invalidation key prefix are pinned to the spec.
//
//   - aggregate + top are keyed under ['stats', …] so the existing SSE
//     stats_invalidated handler (api/sse.ts invalidates queryKey ['stats']) covers
//     them with NO sse.ts change (the prefix partial-matches every sub-key);
//   - search is keyed under ['search', …] and is DISABLED on an empty q (the box
//     must not fire a request before the operator types).

const EMPTY: Filters = {
  agents: [],
  models: [],
  tools: [],
  status: [],
  sources: [],
};

/** aggResponse builds a minimal valid AggregateResponse for the fetch mock. */
function aggResponse(): AggregateResponse {
  return { buckets: [], bucket: 'daily', metric: 'cost' };
}

/** topResponse builds a minimal valid TopResponse for the fetch mock. */
function topResponse(): TopResponse {
  return { dimension: 'model', metric: 'cost', items: [] };
}

/** searchResponse builds a minimal valid SearchResponse for the fetch mock. */
function searchResponse(): SearchResponse {
  return { ops: [], logs: [], logs_indexed: true };
}

/** captureUrl installs a fetch mock recording each requested URL. */
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

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('stats aggregate data layer', () => {
  it('aggregateQueryKey is stable and prefixed ["stats"] (SSE stats_invalidated covers it)', () => {
    const key = aggregateQueryKey(EMPTY, { bucket: 'daily', groupBy: 'model', metric: 'cost' });
    expect(key).toEqual(['stats', 'aggregate', EMPTY, 'daily', 'model', 'cost']);
    // The ['stats'] prefix is exactly what api/sse.ts onStatsInvalidated invalidates.
    expect(key[0]).toBe('stats');
  });

  it('fetchAggregate GETs /api/stats/aggregate with filter + bucket + group_by + metric', async () => {
    const { calls } = captureUrl(aggResponse());
    await fetchAggregate(
      { ...EMPTY, agents: ['a', 'b'], from: 10 },
      { bucket: 'hourly', groupBy: 'agent', metric: 'tokens_in' },
    );
    const url = calls[0] ?? '';
    expect(url.startsWith('/api/stats/aggregate?')).toBe(true);
    expect(url).toContain('agents=a%2Cb');
    expect(url).toContain('from=10');
    expect(url).toContain('bucket=hourly');
    expect(url).toContain('group_by=agent');
    expect(url).toContain('metric=tokens_in');
  });

  it('useAggregate keys by the params and resolves the typed buckets', async () => {
    captureUrl({
      buckets: [{ bucket_ts: 100, series: [{ key: 'gpt', value: 1.5 }] }],
      bucket: 'daily',
      metric: 'cost',
    } satisfies AggregateResponse);
    const { result } = renderHook(
      () => useAggregate(EMPTY, { bucket: 'daily', groupBy: 'total', metric: 'cost' }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.buckets[0]?.series[0]?.key).toBe('gpt');
    expect(result.current.data?.metric).toBe('cost');
  });
});

describe('stats top data layer', () => {
  it('topQueryKey is stable and prefixed ["stats"] (SSE stats_invalidated covers it)', () => {
    const key = topQueryKey(EMPTY, { dimension: 'model', metric: 'cost', n: 20 });
    expect(key).toEqual(['stats', 'top', EMPTY, 'model', 'cost', 20]);
    expect(key[0]).toBe('stats');
  });

  it('fetchTop GETs /api/stats/top with filter + dimension + metric + n', async () => {
    const { calls } = captureUrl(topResponse());
    await fetchTop({ ...EMPTY, status: ['failed'] }, { dimension: 'tool', metric: 'calls', n: 50 });
    const url = calls[0] ?? '';
    expect(url.startsWith('/api/stats/top?')).toBe(true);
    expect(url).toContain('status=failed');
    expect(url).toContain('dimension=tool');
    expect(url).toContain('metric=calls');
    expect(url).toContain('n=50');
  });

  it('useTop keys by the params and resolves the ranked items', async () => {
    captureUrl({
      dimension: 'model',
      metric: 'cost',
      items: [{ key: 'claude', value: 9 }],
    } satisfies TopResponse);
    const { result } = renderHook(
      () => useTop(EMPTY, { dimension: 'model', metric: 'cost', n: 20 }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items[0]?.key).toBe('claude');
  });
});

describe('stats search data layer', () => {
  it('searchQueryKey is prefixed ["search"] (its own SSE wiring is decided in 9b)', () => {
    const key = searchQueryKey(EMPTY, 'boom', 50);
    expect(key).toEqual(['search', EMPTY, 'boom', 50, null]);
    expect(key[0]).toBe('search');
  });

  it('searchQueryKey caches each page separately and is stable when the cursor is absent', () => {
    // Distinct cursors must produce distinct keys, else a plain useQuery serves
    // the wrong page (two fetches differing only by cursor would collide).
    expect(searchQueryKey(EMPTY, 'q', 50, 'A')).not.toEqual(searchQueryKey(EMPTY, 'q', 50, 'B'));
    // An omitted cursor normalises to null, so the cursor-less default is stable.
    expect(searchQueryKey(EMPTY, 'q', 50)).toEqual(searchQueryKey(EMPTY, 'q', 50, undefined));
  });

  it('fetchSearch GETs /api/search with the query, filters, and limit', async () => {
    const { calls } = captureUrl(searchResponse());
    await fetchSearch({ ...EMPTY, models: ['m1'] }, 'panic', { limit: 25 });
    const url = calls[0] ?? '';
    expect(url.startsWith('/api/search?')).toBe(true);
    expect(url).toContain('q=panic');
    expect(url).toContain('models=m1');
    expect(url).toContain('limit=25');
  });

  it('fetchSearch passes a cursor when provided', async () => {
    const { calls } = captureUrl(searchResponse());
    await fetchSearch(EMPTY, 'panic', { limit: 50, cursor: 'next-page' });
    expect(calls[0] ?? '').toContain('cursor=next-page');
  });

  it('useSearch resolves the ops/logs and the logs_indexed flag', async () => {
    captureUrl({
      ops: [{ op_id: 'o1', session_id: 's1', kind: 'tool', name: 'Bash', model: 'm', snippet: 'x', rank: 1 }],
      logs: [],
      logs_indexed: false,
    } satisfies SearchResponse);
    const { result } = renderHook(() => useSearch(EMPTY, 'panic'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.ops[0]?.op_id).toBe('o1');
    expect(result.current.data?.logs_indexed).toBe(false);
  });

  it('useSearch is DISABLED on an empty / whitespace q (no request fires)', async () => {
    const { calls } = captureUrl(searchResponse());
    const { result, rerender } = renderHook(({ q }: { q: string }) => useSearch(EMPTY, q), {
      wrapper,
      initialProps: { q: '   ' },
    });
    // A disabled query never enters fetching and never hits the network.
    expect(result.current.fetchStatus).toBe('idle');
    expect(calls).toHaveLength(0);
    // Typing a real term enables it and the request fires exactly once.
    rerender({ q: 'real' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(calls).toHaveLength(1);
    expect(calls[0] ?? '').toContain('q=real');
  });
});
