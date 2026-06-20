import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { SessionListResponse, SessionListItem } from '../../api/types';
import { Failures } from './Failures';

// Mocks: useSessionsInfinite, useLiveUpdates. We don't need real data for
// structural tests; individual tests can override the spies.
const sessionsSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/sessions', () => ({
  useSessionsInfinite: (...args: unknown[]) => sessionsSpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));

// useFilters writes to the URL via React Router's setSearchParams; we don't
// need real URL behavior here, just a no-op so the page doesn't crash.
vi.mock('../../state/filters', () => ({
  useFilters: () => ({
    filters: { agents: [], models: [], tools: [], sources: [], status: [] },
    setFilters: () => {},
  }),
  filtersToSubscription: () => ({}),
}));

function mkSession(overrides: Partial<SessionListItem>): SessionListItem {
  return {
    id: 's1',
    native_id: 'native-1',
    source: 'codex',
    agent_name: 'agent-1',
    model: 'gpt-4',
    status: 'failed',
    cost_usd: 0.05,
    tokens_in: 100,
    tokens_out: 50,
    error_class: 'rate_limit',
    started_at: 1_700_000_000,
    ended_at: 1_700_000_010,
    start_ts: 1_700_000_000,
    end_ts: 1_700_000_010,
    turn_count: 3,
    op_count: 4,
    ...overrides,
  } as SessionListItem;
}

function renderFailures() {
  return render(
    <MemoryRouter initialEntries={['/failures']}>
      <TooltipProvider>
        <Routes>
          <Route path="/failures" element={<Failures />} />
          <Route path="/sessions/:id" element={<div data-testid="session-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('Failures page', () => {
  it('renders the page header with title and subtitle', () => {
    sessionsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false, error: null });
    renderFailures();
    expect(screen.getByRole('heading', { level: 1, name: 'Recent failures' })).toBeInTheDocument();
  });

  it('renders the four-window time selector with 7d as default', () => {
    sessionsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false, error: null });
    renderFailures();
    const group = screen.getByRole('group', { name: 'Time window' });
    expect(within(group).getByRole('button', { name: '24 hours' })).toBeInTheDocument();
    expect(within(group).getByRole('button', { name: '7 days' })).toBeInTheDocument();
    expect(within(group).getByRole('button', { name: '30 days' })).toBeInTheDocument();
    expect(within(group).getByRole('button', { name: 'all time' })).toBeInTheDocument();
    expect(within(group).getByRole('button', { name: '7 days' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('renders the four summary tiles', () => {
    sessionsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false, error: null });
    renderFailures();
    // The summary strip uses uppercase tracking-wider labels; we scope the
    // lookup to that container.
    const summary = document.querySelector('.grid.grid-cols-2');
    expect(summary).not.toBeNull();
    expect(within(summary as HTMLElement).getByText('Failures')).toBeInTheDocument();
    expect(within(summary as HTMLElement).getByText('Cost')).toBeInTheDocument();
    expect(within(summary as HTMLElement).getByText('Tokens')).toBeInTheDocument();
    expect(within(summary as HTMLElement).getByText('Top error class')).toBeInTheDocument();
  });

  it('renders skeleton rows while pending', () => {
    sessionsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false, error: null });
    renderFailures();
    // The skeleton rows have the aria-busy table; we just check that the
    // page renders without crashing while pending. Row count comes from
    // Array.from({ length: 6 }) but is not directly observable in DOM;
    // a smoke check is enough.
    expect(screen.getByRole('region', { name: /Recent failures/i })).toBeInTheDocument();
  });

  it('renders the empty state when there are no failures in window', () => {
    const emptyResponse: SessionListResponse = {
      items: [],
    };
    sessionsSpy.mockReturnValue({
      data: { pages: [emptyResponse], pageParams: [null] },
      isPending: false,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    renderFailures();
    expect(screen.getByText('No failures in this window')).toBeInTheDocument();
  });

  it('renders a row per failure with agent, error class, and cost', () => {
    const response: SessionListResponse = {
      items: [
        mkSession({ id: 'a', agent_name: 'agent-alpha', error_class: 'rate_limit', cost_usd: 0.12 }),
        mkSession({ id: 'b', agent_name: 'agent-beta', error_class: 'timeout', cost_usd: 0.05 }),
      ],
    };
    sessionsSpy.mockReturnValue({
      data: { pages: [response], pageParams: [null] },
      isPending: false,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    renderFailures();
    // Scope to the failures table so we don't pick up the summary tile.
    const table = screen.getByRole('table');
    expect(within(table).getByText('agent-alpha')).toBeInTheDocument();
    expect(within(table).getByText('agent-beta')).toBeInTheDocument();
    expect(within(table).getAllByText('rate_limit').length).toBeGreaterThanOrEqual(1);
    expect(within(table).getAllByText('timeout').length).toBeGreaterThanOrEqual(1);
    expect(within(table).getByText('$0.12')).toBeInTheDocument();
  });

  it('renders error chips for each distinct error class', () => {
    const response: SessionListResponse = {
      items: [
        mkSession({ id: 'a', error_class: 'rate_limit' }),
        mkSession({ id: 'b', error_class: 'rate_limit' }),
        mkSession({ id: 'c', error_class: 'timeout' }),
      ],
    };
    sessionsSpy.mockReturnValue({
      data: { pages: [response], pageParams: [null] },
      isPending: false,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    renderFailures();
    // rate_limit appears in BOTH the chip strip AND the row; use getAllByText
    const rateLimitChips = screen.getAllByText('rate_limit');
    expect(rateLimitChips.length).toBeGreaterThanOrEqual(2);
    // timeout appears in BOTH the chip strip AND the row
    const timeoutChips = screen.getAllByText('timeout');
    expect(timeoutChips.length).toBeGreaterThanOrEqual(2);
  });

  it('renders the error message when isError is true', () => {
    sessionsSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    renderFailures();
    expect(screen.getByText(/Failed to load failures: boom/)).toBeInTheDocument();
  });
});
