// /compare (SOW-0095) page tests. The page renders 4 tabs of structured
// diff between 2-4 sessions; the diff payload comes from a single
// /api/sessions/compare request. The tests assert the empty state,
// the loading skeleton, the 2-card and 3-card happy paths, and the
// tab navigation.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { CompareResponse } from '../../api/types';
import { Compare } from './Compare';

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** mockOkResponse wraps a body as a fetch Response. */
function mockOkResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: 'ok',
    headers: { get: () => null },
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function lastFetchUrl(): string {
  const calls = fetchMock.mock.calls;
  if (calls.length === 0) return '';
  return calls[calls.length - 1]?.[0] as string;
}

/** renderInRouter wraps with MemoryRouter at the given initial URL and
 *  supplies a fresh QueryClient. */
function renderInRouter(ui: React.ReactElement, initialPath: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initialPath]}>{ui}</MemoryRouter>
    </QueryClientProvider>
  );
}

/** fixtureCompareResponse builds a minimal CompareResponse with the
 *  given session ids. Tests override individual fields to assert
 *  specific UI shapes. */
function fixtureCompareResponse(ids: string[]): CompareResponse {
  return {
    sessions: ids.map((id, i) => ({
      id,
      native_id: `n-${id}`,
      root_session_id: id,
      parent_session_id: null,
      source_id: 's',
      kind: 'root',
      agent_name: `agent-${i + 1}`,
      model: i === 0 ? 'claude-opus-4-7' : 'gpt-5',
      status: 'completed',
      effective_status: 'completed',
      start_ts: 1_000_000 + i * 1_000,
      end_ts: 1_000_000 + i * 1_000 + 5_000_000,
      last_activity_ts: 1_000_000 + i * 1_000 + 5_000_000,
      tokens_in: 100,
      tokens_out: 200,
      cost_usd: 0.01 * (i + 1),
      turn_count: 1,
      op_count: 3,
      failure_count: 0,
      child_session_count: 0,
    })),
    summary: {
      duration_us: { best: ids[0], worst: ids[ids.length - 1], per_session: Object.fromEntries(ids.map((id, i) => [id, 5_000_000 - i * 1_000_000])) },
      cost_usd: { best: ids[0], worst: ids[ids.length - 1], per_session: Object.fromEntries(ids.map((id, i) => [id, 0.01 * (i + 1)])) },
      op_count: { per_session: Object.fromEntries(ids.map((id) => [id, 3])) },
      tokens: { per_session: Object.fromEntries(ids.map((id) => [id, 300])) },
    } as CompareResponse['summary'],
    tool_usage: {
      common: ['Bash'],
      added: Object.fromEntries(ids.map((id) => [id, [`tool-${id}`]])),
      removed: Object.fromEntries(ids.map((id) => [id, []])),
      per_session: Object.fromEntries(ids.map((id) => [id, { Bash: 5, [`tool-${id}`]: 1 }])),
    },
    errors: {
      common: [],
      only_in: Object.fromEntries(ids.map((id, i) => [id, i === 0 ? [{ op_id: `op-${id}`, kind: 'llm', name: 'claude-opus-4-7', error_class: 'rate_limit', started_at_us: 1_000_000 }] : []])),
    },
    models: {
      shared: [],
      diverged: Object.fromEntries(ids.map((id) => [id, [id === ids[0] ? 'claude-opus-4-7' : 'gpt-5']])),
    },
    agents: {
      shared: [],
      diverged: Object.fromEntries(ids.map((id, i) => [id, [`agent-${i + 1}`]])),
    },
    kind_distribution: {
      per_session: Object.fromEntries(ids.map((id) => [id, { llm: 1, tool: 2 }])),
    },
  };
}

describe('Compare page', () => {
  it('renders the empty state when ids is missing', () => {
    renderInRouter(<Compare />, '/compare');
    expect(screen.getByText(/Pick 2-4 sessions to compare/i)).toBeInTheDocument();
    // No fetch happens on the empty state.
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('renders the empty state when only 1 id is provided', () => {
    renderInRouter(<Compare />, '/compare?ids=session-a');
    expect(screen.getByText(/Pick 2-4 sessions to compare/i)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('renders the empty state when 5+ ids are provided', () => {
    renderInRouter(<Compare />, '/compare?ids=a,b,c,d,e');
    expect(screen.getByText(/2-4 sessions/i)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('renders 2 summary cards for 2 ids and triggers a compare fetch', async () => {
    fetchMock.mockResolvedValue(mockOkResponse(fixtureCompareResponse(['session-a', 'session-b'])));
    renderInRouter(<Compare />, '/compare?ids=session-a,session-b');
    // Wait for the cards to render.
    await waitFor(() => {
      expect(screen.getAllByText('agent-1').length).toBeGreaterThan(0);
    });
    expect(screen.getAllByText('agent-2').length).toBeGreaterThan(0);
    // Verify the fetch URL is the compare endpoint.
    expect(lastFetchUrl()).toContain('/api/sessions/compare');
    expect(lastFetchUrl()).toContain('ids=session-a,session-b');
  });

  it('renders 3 summary cards for 3 ids', async () => {
    fetchMock.mockResolvedValue(mockOkResponse(fixtureCompareResponse(['s1', 's2', 's3'])));
    renderInRouter(<Compare />, '/compare?ids=s1,s2,s3');
    await waitFor(() => {
      expect(screen.getAllByText('agent-1').length).toBeGreaterThan(0);
    });
    expect(screen.getAllByText('agent-2').length).toBeGreaterThan(0);
    expect(screen.getAllByText('agent-3').length).toBeGreaterThan(0);
  });

  it('navigates between Overview / Tools / Errors / Kinds tabs', async () => {
    fetchMock.mockResolvedValue(mockOkResponse(fixtureCompareResponse(['session-a', 'session-b'])));
    renderInRouter(<Compare />, '/compare?ids=session-a,session-b');
    await waitFor(() => {
      expect(screen.getAllByText('agent-1').length).toBeGreaterThan(0);
    });
    // Tools tab: click + assert Bash (common) is rendered.
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Tools' }));
    expect(screen.getByText('Bash')).toBeInTheDocument();
    // Errors tab.
    await user.click(screen.getByRole('button', { name: 'Errors' }));
    expect(screen.getByText('rate_limit')).toBeInTheDocument();
    // Kinds tab.
    await user.click(screen.getByRole('button', { name: 'Kinds' }));
    expect(screen.getByText('llm')).toBeInTheDocument();
    // Back to Overview.
    await user.click(screen.getByRole('button', { name: 'Overview' }));
    expect(screen.getAllByText('Ops').length).toBeGreaterThan(0);
  });

  it('renders the error state when the fetch fails', async () => {
    fetchMock.mockResolvedValue({
      ok: false,
      status: 500,
      statusText: 'error',
      headers: { get: () => null },
      json: () => Promise.resolve({ error: { code: 'DB_ERROR', message: 'db down' } }),
    });
    renderInRouter(<Compare />, '/compare?ids=session-a,session-b');
    await waitFor(() => {
      expect(screen.getByText(/Could not load comparison/i)).toBeInTheDocument();
    });
  });
});
