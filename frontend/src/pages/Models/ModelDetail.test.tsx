import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { SessionListResponse, SessionListItem } from '../../api/types';

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

const homeSpy = vi.fn();
vi.mock('../../api/home', () => ({
  useHomeSummary: (...args: unknown[]) => homeSpy(...args) as unknown,
  todayMidnightUs: () => 0,
}));

import { ModelDetail } from './ModelDetail';

function mkSession(overrides: Partial<SessionListItem>): SessionListItem {
  return {
    id: 's1',
    native_id: 'native-1',
    source: 'codex',
    agent_name: 'agent-alpha',
    model: 'gpt-4o',
    status: 'completed',
    cost_usd: 0.10,
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
    <MemoryRouter initialEntries={['/models/gpt-4o']}>
      <TooltipProvider>
        <Routes>
          <Route path="/models" element={<div data-testid="models-list" />} />
          <Route path="/models/:name" element={<ModelDetail />} />
          <Route path="/sessions/:id" element={<div data-testid="session-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('ModelDetail', () => {
  beforeEach(() => {
    sessionsSpy.mockReset();
    statsSpy.mockReset();
    liveSpy.mockReset();
    setFiltersSpy.mockReset();
    homeSpy.mockReset();
    homeSpy.mockReturnValue({ data: undefined, isPending: true });
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
  });

  it('renders the model name as the page title', () => {
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'gpt-4o' })).toBeInTheDocument();
  });

  it('binds the model name into the URL filter', () => {
    renderPage();
    expect(setFiltersSpy).toHaveBeenCalled();
    const lastCall = setFiltersSpy.mock.calls.at(-1)?.[0] as { models?: string[] } | undefined;
    expect(lastCall?.models).toEqual(['gpt-4o']);
  });

  it('shows a back-to-list link to /models', () => {
    renderPage();
    expect(screen.getByRole('link', { name: /All models/i })).toHaveAttribute('href', '/models');
  });

  it('renders a row per session', () => {
    const response: SessionListResponse = {
      items: [
        mkSession({ id: 'a', agent_name: 'agent-alpha' }),
        mkSession({ id: 'b', agent_name: 'agent-beta' }),
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
    renderPage();
    expect(screen.getByText('agent-alpha')).toBeInTheDocument();
    expect(screen.getByText('agent-beta')).toBeInTheDocument();
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
    renderPage();
    expect(screen.getByText(/No sessions for gpt-4o in this window/i)).toBeInTheDocument();
  });
});
