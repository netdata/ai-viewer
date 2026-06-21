// /search (SOW-0091) tests. The page renders three sections (ops,
// logs, content) and debounces the query into useSearch.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useSearchParams } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { SearchResponse } from '../../api/types';
import { Search } from './Search';

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** mockOkResponse wraps a body as a fetch Response. Mirrors the shape used
 *  by statsCharts.test.tsx (ok + status + headers.get + json) so the
 *  presenter's client.ts request() path is exercised end-to-end. */
function mockOkResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: 'ok',
    headers: { get: () => null },
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

/** lastFetchUrl returns the URL of the most recent fetch call, or '' if
 *  no fetch was made. */
function lastFetchUrl(): string {
  const calls = fetchMock.mock.calls;
  if (calls.length === 0) return '';
  return calls[calls.length - 1]?.[0] as string;
}

/** renderInRouter wraps with MemoryRouter at the given initial URL.
 *  A URLProbe component below renders the current search params so
 *  tests can assert URL persistence. */
function renderInRouter(ui: React.ReactElement, initialPath = '/search') {
  // QueryClient is required because <Search> calls useSearch (a React
  // Query hook). Each render gets its own client so cache state doesn't
  // leak between tests. MemoryRouter wraps the QueryClientProvider so
  // React Router's hooks (useSearchParams in useFilters) resolve
  // before the QueryClient hooks try to use the URL.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <QueryClientProvider client={qc}>
        <UrlProbe />
        {ui}
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

function UrlProbe() {
  const [params] = useSearchParams();
  return (
    <div data-testid="url-probe" data-q={params.get('q') ?? ''} />
  );
}

describe('Search — empty / pending states', () => {
  it('shows an empty-state hint when no query is typed', () => {
    renderInRouter(<Search />);
    expect(screen.getByRole('searchbox')).toBeInTheDocument();
    expect(
      screen.getByText(/type a query to search across ops, logs, and prompt/i),
    ).toBeInTheDocument();
  });

  it('does not fire a fetch when the query is empty', async () => {
    renderInRouter(<Search />);
    await waitFor(() => {
      expect(fetchMock).not.toHaveBeenCalled();
    });
  });
});

describe('Search — query lifecycle', () => {
  it('debounces the query and fires useSearch once after 300ms', async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(emptySearchResponse()),
    });

    renderInRouter(<Search />);
    const input = screen.getByRole('searchbox');

    await user.type(input, 'rate limiting');
    // No fetch yet — debounce window not elapsed.
    await new Promise((r) => setTimeout(r, 50));
    expect(fetchMock).not.toHaveBeenCalled();
    // After the debounce window a fetch fires with the typed query.
    await waitFor(
      () => {
        expect(fetchMock).toHaveBeenCalledTimes(1);
      },
      { timeout: 1000 },
    );
    // URLSearchParams encodes spaces as '+' so the query reads "rate+limiting".
    expect(lastFetchUrl()).toContain('q=rate+limiting');
  });

  it('persists the debounced query in the URL as ?q=', async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(emptySearchResponse()),
    });

    renderInRouter(<Search />);
    await user.type(screen.getByRole('searchbox'), 'filesystem');

    await waitFor(() => {
      const probe = screen.getByTestId('url-probe');
      expect(probe.getAttribute('data-q')).toBe('filesystem');
    });
  });
});

describe('Search — result sections', () => {
  it('renders three sections: content, ops, logs', async () => {
    fetchMock.mockResolvedValue(mockOkResponse(sampleSearchResponse()));

    renderInRouter(<Search />, '/search?q=permissions');
    await waitFor(() => {
      expect(
        screen.getByRole('heading', { name: /prompt \/ response content/i }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole('heading', { name: /op metadata/i }),
      ).toBeInTheDocument();
      expect(screen.getByRole('heading', { name: /logs/i })).toBeInTheDocument();
    });
  });

  it('renders the snippet text from content matches', async () => {
    fetchMock.mockResolvedValue(
      mockOkResponse(
        withContent({
          op_id: 'op-content-1',
          session_id: 'sess-content-1',
          turn_id: 'turn-content-1',
          snippet: 'match excerpt here [permissions] instructions',
          rank: -1,
        }),
      ),
    );

    renderInRouter(<Search />, '/search?q=permissions');
    await waitFor(() => {
      expect(screen.getByText(/match excerpt here/)).toBeInTheDocument();
    });
  });

  it('shows the empty-state when nothing matches', async () => {
    fetchMock.mockResolvedValue(mockOkResponse(emptySearchResponse()));

    renderInRouter(<Search />, '/search?q=zzznomatchzzz');
    await waitFor(() => {
      expect(screen.getByText(/no matches/i)).toBeInTheDocument();
    });
  });
});

function emptySearchResponse(): SearchResponse {
  return { ops: [], logs: [], content: [], logs_indexed: true };
}

function withContent(hit: NonNullable<SearchResponse['content']>[number]): SearchResponse {
  return { ops: [], logs: [], content: [hit], logs_indexed: true };
}

function sampleSearchResponse(): SearchResponse {
  return {
    ops: [
      {
        op_id: 'op-meta-1',
        session_id: 'sess-1',
        kind: 'tool',
        name: 'read_file',
        model: 'claude',
        snippet: '[read_file]',
        rank: -1,
      },
    ],
    logs: [
      {
        log_id: 1,
        session_id: 'sess-1',
        op_id: null,
        severity: 'INF',
        ts: 1000,
        snippet: '[hello]',
        rank: -1,
      },
    ],
    content: [
      {
        op_id: 'op-content-1',
        session_id: 'sess-1',
        turn_id: 'turn-1',
        snippet: '[permissions] instructions text',
        rank: -1,
      },
    ],
    logs_indexed: true,
  };
}
