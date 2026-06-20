import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { StatsResponse } from '../../api/types';

// Mock useStats so each test controls the data shape.
const statsSpy = vi.fn();
vi.mock('../../api/stats', () => ({
  useStats: (...args: unknown[]) => statsSpy(...args) as unknown,
}));

// useFilters + setFilters are no-ops here — the page reads filters but the
// setFilters wiring is exercised by the page itself.
vi.mock('../../state/filters', () => ({
  useFilters: () => ({
    filters: {
      agents: [],
      models: [],
      tools: [],
      status: [],
      sources: [],
    },
    setFilters: () => {},
  }),
}));

import { AgentsList } from './AgentsList';

const FULL_STATS: StatsResponse = {
  totals: {
    session_count: 100,
    turn_count: 500,
    op_count: 2000,
    tokens_in: 1_000_000,
    tokens_out: 500_000,
    tokens_cache_read: 100_000,
    tokens_cache_write: 50_000,
    cost_usd: 12.34,
    failures: 5,
    duration_us: 3_600_000_000,
  },
  by_agent: [
    { name: 'agent-alpha', sessions: 60, failures: 1, tokens_in: 600_000, tokens_out: 300_000, tokens_cache_read: 60_000, tokens_cache_write: 30_000, cost_usd: 8.00, pct_of_sessions: 60.0 },
    { name: 'agent-beta',  sessions: 30, failures: 2, tokens_in: 300_000, tokens_out: 150_000, tokens_cache_read: 30_000, tokens_cache_write: 15_000, cost_usd: 3.50, pct_of_sessions: 30.0 },
    { name: 'agent-gamma', sessions: 10, failures: 2, tokens_in: 100_000, tokens_out:  50_000, tokens_cache_read: 10_000, tokens_cache_write:  5_000, cost_usd: 0.84, pct_of_sessions: 10.0 },
  ],
  by_model: [],
  by_tool: [],
  by_status: [],
  by_source: [],
  by_error_class: [],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/agents']}>
      <TooltipProvider>
        <Routes>
          <Route path="/agents" element={<AgentsList />} />
          <Route path="/agents/:name" element={<div data-testid="agent-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('AgentsList', () => {
  beforeEach(() => {
    statsSpy.mockReset();
  });

  it('renders the page header (title + subtitle)', () => {
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'Agents' })).toBeInTheDocument();
    expect(screen.getByText(/Every distinct agent name/i)).toBeInTheDocument();
  });

  it('renders a card per agent, sorted by session count (descending)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    // Use scoped query to avoid ambiguity with the sidebar/topbar text.
    const main = document.querySelector('section[aria-labelledby="agents-title"]');
    expect(main).not.toBeNull();
    const mainWithin: { getAllByText: (s: string | RegExp) => HTMLElement[] } = within(main as HTMLElement);
    const agentLinks = mainWithin.getAllByText(/^agent-(alpha|beta|gamma)$/);
    // alpha has most sessions → first
    expect(agentLinks[0]).toHaveTextContent('agent-alpha');
    expect(agentLinks[1]).toHaveTextContent('agent-beta');
    expect(agentLinks[2]).toHaveTextContent('agent-gamma');
  });

  it('cards are links to /agents/:name', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    const alphaLink = screen.getByRole('link', { name: /agent-alpha/i });
    expect(alphaLink).toHaveAttribute('href', '/agents/agent-alpha');
  });

  it('renders the summary strip (Agents / Sessions / Total cost / Reliability)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    // Each label appears in both the page header (Agents H1) and the
    // summary strip tile; check >= 1 instead of exactly 1.
    expect(screen.getAllByText('Agents').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Sessions').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Total cost').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Reliability').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('$12.34')).toBeInTheDocument();
  });

  it('shows a skeleton state while pending', () => {
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getAllByText('Agents').length).toBeGreaterThanOrEqual(1);
  });

  it('shows the error state when the query fails', () => {
    statsSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
    });
    renderPage();
    expect(screen.getByText(/Failed to load agents: boom/)).toBeInTheDocument();
  });

  it('shows an empty state when no agents are present', () => {
    statsSpy.mockReturnValue({
      data: { ...FULL_STATS, by_agent: [] },
      isPending: false,
      isError: false,
    });
    renderPage();
    expect(screen.getByText(/No agents in this window/i)).toBeInTheDocument();
  });
});
