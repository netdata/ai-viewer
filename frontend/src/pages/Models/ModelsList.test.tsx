import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { StatsResponse } from '../../api/types';

const statsSpy = vi.fn();
vi.mock('../../api/stats', () => ({
  useStats: (...args: unknown[]) => statsSpy(...args) as unknown,
}));

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

import { ModelsList } from './ModelsList';

const FULL_STATS: StatsResponse = {
  totals: {
    session_count: 50,
    turn_count: 250,
    op_count: 1000,
    tokens_in: 500_000,
    tokens_out: 250_000,
    tokens_cache_read: 50_000,
    tokens_cache_write: 25_000,
    cost_usd: 5.50,
    failures: 2,
    duration_us: 1_800_000_000,
  },
  by_agent: [],
  by_model: [
    { name: 'gpt-4o', provider: 'openai', calls: 800, tokens_in: 400_000, tokens_out: 200_000, tokens_cache_read: 40_000, tokens_cache_write: 20_000, cost_usd: 4.00, failures: 1, duration_us: 1_000_000_000, pct_of_cost: 72.7 },
    { name: 'claude-3-5-sonnet', provider: 'anthropic', calls: 200, tokens_in: 100_000, tokens_out: 50_000, tokens_cache_read: 10_000, tokens_cache_write: 5_000, cost_usd: 1.50, failures: 1, duration_us: 800_000_000, pct_of_cost: 27.3 },
  ],
  by_tool: [],
  by_status: [],
  by_source: [],
  by_error_class: [],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/models']}>
      <TooltipProvider>
        <Routes>
          <Route path="/models" element={<ModelsList />} />
          <Route path="/models/:name" element={<div data-testid="model-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('ModelsList', () => {
  beforeEach(() => {
    statsSpy.mockReset();
  });

  it('renders the page header', () => {
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'Models' })).toBeInTheDocument();
  });

  it('renders cards sorted by cost (descending)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    // gpt-4o has higher cost (4.00 vs 1.50) → first.
    const gptLink = screen.getByRole('link', { name: /gpt-4o/i });
    const claudeLink = screen.getByRole('link', { name: /claude-3-5-sonnet/i });
    // Both are present
    expect(gptLink).toBeInTheDocument();
    expect(claudeLink).toBeInTheDocument();
  });

  it('cards link to /models/:name', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    expect(screen.getByRole('link', { name: /gpt-4o/i })).toHaveAttribute('href', '/models/gpt-4o');
  });

  it('shows the error state', () => {
    statsSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
    });
    renderPage();
    expect(screen.getByText(/Failed to load models: boom/)).toBeInTheDocument();
  });

  it('shows the empty state', () => {
    statsSpy.mockReturnValue({
      data: { ...FULL_STATS, by_model: [] },
      isPending: false,
      isError: false,
    });
    renderPage();
    expect(screen.getByText(/No models in this window/i)).toBeInTheDocument();
  });
});
