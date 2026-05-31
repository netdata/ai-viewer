import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import {
  fetchTopology,
  topologyFiltersToQuery,
  topologyQueryKey,
  useTopology,
} from './topology';
import type { Filters } from '../state/filters';

// Cross-session topology data layer (GET /api/topology). Mirrors the
// dataLayer.test.tsx pattern: the pure query-key + query-string builders
// directly, and the hook through a QueryClientProvider with mocked fetch, so the
// request URL and the SSE invalidation key prefix (['topology', metric, filters])
// are pinned to the spec (rest-api.md §GET /api/topology).

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

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('cross-session topology data layer', () => {
  it('topologyQueryKey is stable and metric-scoped (prefix ["topology"])', () => {
    expect(topologyQueryKey(EMPTY, 'cost')).toEqual(['topology', 'cost', EMPTY]);
    // The ['topology'] prefix is what api/sse.ts onSessionChanged invalidates.
    expect(topologyQueryKey(EMPTY, 'duration')[0]).toBe('topology');
  });

  it('topologyFiltersToQuery serializes the filter dimensions + metric (no cursor/group)', () => {
    const qs = topologyFiltersToQuery(
      { ...EMPTY, agents: ['a', 'b'], sources: ['src1'], from: 10 },
      'tokens',
    );
    expect(qs).toContain('agents=a%2Cb');
    expect(qs).toContain('sources=src1');
    expect(qs).toContain('from=10');
    expect(qs).toContain('metric=tokens');
    // The cross-session topology is not paginated and has no group toggle.
    expect(qs).not.toContain('cursor=');
    expect(qs).not.toContain('group=');
  });

  it('fetchTopology GETs /api/topology with the filter + metric query string', async () => {
    const { calls } = captureUrl({ nodes: [], edges: [], max_size_metric: 0 });
    await fetchTopology({ ...EMPTY, status: ['failed'] }, 'duration');
    expect(calls[0]).toBe('/api/topology?status=failed&metric=duration');
  });

  it('useTopology keys by metric+filters and resolves the graph (incl. truncated)', async () => {
    captureUrl({
      nodes: [{ id: 'agent:rootA', kind: 'agent', label: 'nedi (root)', size_metric: 1, failure_ratio: 0 }],
      edges: [],
      max_size_metric: 1,
      truncated: true,
    });
    const { result } = renderHook(() => useTopology(EMPTY, 'duration'), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.nodes[0]?.id).toBe('agent:rootA');
    expect(result.current.data?.truncated).toBe(true);
  });
});
