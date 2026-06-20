import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { SessionListResponse, SessionListItem } from '../../api/types';
import type { StatsResponse } from '../../api/types';

// Mock dependencies: useSessionsInfinite, useStats, useLiveUpdates,
// useFilters. The AgentDetail page binds the URL agent name into the
// filters; the tests bypass the real URL-sync and supply the
// computed filters directly.
const sessionsSpy = vi.fn();
const statsSpy = vi.fn();
const liveSpy = vi.fn();
const setFiltersSpy = vi.fn();

vi.mock('../../api/sessions', () => ({
  useSessionsInfinite: (...args: unknown[]) => sessionsSpy(...args) as unknown,
}));
vi.mock('../../api/stats', () => ({
  useStats: (...args: unknown[]) => statsSpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));
vi.mock('../../state/filters', () => ({
  useFilters: () => ({
    filters: { agents: [], models: [], tools: [], status: [], sources: [] },
    setFilters: setFiltersSpy,
  }),
  filtersToSubscription: () => ({}),
}));

// useHomeSummary is also mocked (AgentDetail mounts HomeSummaryCard which
// uses useHomeSummary from /api/home).
const homeSpy = vi.fn();
vi.mock('../../api/home', () => ({
  useHomeSummary: (...args: unknown[]) => homeSpy(...args) as unknown,
  todayMidnightUs: () => 0,
}));

import { AgentDetail } from './AgentDetail';

const FULL_STATS: StatsResponse = {
  totals: {
    session_count: 50,
    turn_count: 250,
    op_count: 1000,
    tokens_in: 500_000,
    tokens_out: 250_000,
    tokens_cache_read: 50_000,
    tokens_cache_write: 25_000,
    cost_usd: 12.34,
    failures: 3,
    duration_us: 1_800_000_000,
  },
  by_agent: [],
  by_model: [],
  by_tool: [],
  by_status: [],
  by_source: [],
  by_error_class: [],
};

function mkSession(overrides: Partial<SessionListItem>): SessionListItem {
  return {
    id: 's1',
    native_id: 'native-1',
    source: 'codex',
    agent_name: 'agent-alpha',
    model: 'gpt-4',
    status: 'completed',
    cost_usd: 0.25,
    tokens_in: 1000,
    tokens_out: 500,
    start_ts: 1_700_000_000,
    end_ts: 1_700_000_010,
    turn_count: 3,
    op_count: 4,
    ...overrides,
  } as SessionListItem;
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/agents/agent-alpha']}>
      <TooltipProvider>
        <Routes>
          <Route path="/agents" element={<div data-testid="agents-list" />} />
          <Route path="/agents/:name" element={<AgentDetail />} />
          <Route path="/sessions/:id" element={<div data-testid="session-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('AgentDetail', () => {
  beforeEach(() => {
    sessionsSpy.mockReset();
    statsSpy.mockReset();
    liveSpy.mockReset();
    setFiltersSpy.mockReset();
    homeSpy.mockReset();
    homeSpy.mockReturnValue({ data: undefined, isPending: true });
  });

  it('renders the agent name as the page title', () => {
    sessionsSpy.mockReturnValue({
      data: undefined,
      isPending: true,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'agent-alpha' })).toBeInTheDocument();
  });

  it('shows a back-to-list link to /agents', () => {
    sessionsSpy.mockReturnValue({
      data: undefined, isPending: true, isError: false, error: null,
      hasNextPage: false, isFetchingNextPage: false, fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    const back = screen.getByRole('link', { name: /All agents/i });
    expect(back).toHaveAttribute('href', '/agents');
  });

  it('binds the agent name into the URL filter (URL is the source of truth)', () => {
    sessionsSpy.mockReturnValue({
      data: undefined, isPending: true, isError: false, error: null,
      hasNextPage: false, isFetchingNextPage: false, fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    // setFilters should have been called with agents: ['agent-alpha']
    expect(setFiltersSpy).toHaveBeenCalled();
    const lastCall = setFiltersSpy.mock.calls.at(-1)?.[0] as { agents?: string[] } | undefined;
    expect(lastCall?.agents).toEqual(['agent-alpha']);
  });

  it('renders the time-window selector (24h / 7d / 30d / all)', () => {
    sessionsSpy.mockReturnValue({
      data: undefined, isPending: true, isError: false, error: null,
      hasNextPage: false, isFetchingNextPage: false, fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    const group = screen.getByRole('group', { name: 'Time window' });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '24 hours' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '7 days' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '30 days' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'all time' })).toBeInTheDocument();
  });

  it('shows header stats when stats load', () => {
    sessionsSpy.mockReturnValue({
      data: undefined, isPending: true, isError: false, error: null,
      hasNextPage: false, isFetchingNextPage: false, fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    expect(screen.getByText(/50/)).toBeInTheDocument();
    expect(screen.getByText(/\$12\.34/)).toBeInTheDocument();
    // 47 successful of 50 ended = 94% reliability
    expect(screen.getByText(/94\.0%/)).toBeInTheDocument();
  });

  it('renders a row per session when loaded', () => {
    const response: SessionListResponse = {
      items: [
        mkSession({ id: 'a', model: 'gpt-4o', status: 'completed' }),
        mkSession({ id: 'b', model: 'claude-3-5-sonnet', status: 'failed' }),
      ],
    };
    sessionsSpy.mockReturnValue({
      data: { pages: [response], pageParams: [undefined] },
      isPending: false,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    const table = screen.getByRole('table');
    expect(withinScoped(table, 'gpt-4o')).toBeInTheDocument();
    expect(withinScoped(table, 'claude-3-5-sonnet')).toBeInTheDocument();
    expect(withinScoped(table, 'completed')).toBeInTheDocument();
    expect(withinScoped(table, 'failed')).toBeInTheDocument();
  });

  it('shows the error state when the sessions query fails', () => {
    sessionsSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByText(/Failed to load sessions/i)).toBeInTheDocument();
    expect(screen.getByText(/boom/)).toBeInTheDocument();
  });

  it('shows the empty state when no sessions match', () => {
    sessionsSpy.mockReturnValue({
      data: { pages: [{ items: [] }], pageParams: [undefined] },
      isPending: false,
      isError: false,
      error: null,
      hasNextPage: false,
      isFetchingNextPage: false,
      fetchNextPage: () => {},
    });
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByText(/No sessions for agent-alpha in this window/i)).toBeInTheDocument();
  });
});

// withinScoped is a tiny helper that scopes a query to a container element.
import { within } from '@testing-library/react';
function withinScoped(container: HTMLElement, text: string): HTMLElement | null {
  return within(container).queryByText(text);
}
