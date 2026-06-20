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

import { ToolsList } from './ToolsList';

const FULL_STATS: StatsResponse = {
  totals: {
    session_count: 20,
    turn_count: 100,
    op_count: 400,
    tokens_in: 200_000,
    tokens_out: 100_000,
    tokens_cache_read: 20_000,
    tokens_cache_write: 10_000,
    cost_usd: 2.00,
    failures: 1,
    duration_us: 600_000_000,
  },
  by_agent: [],
  by_model: [],
  by_tool: [
    { namespace: 'filesystem', name: 'read', calls: 200, failures: 0, total_us: 5_000_000, pct_of_calls: 50.0 },
    { namespace: 'filesystem', name: 'write', calls: 50, failures: 1, total_us: 2_000_000, pct_of_calls: 12.5 },
    { namespace: 'shell', name: 'bash', calls: 150, failures: 0, total_us: 3_000_000, pct_of_calls: 37.5 },
  ],
  by_status: [],
  by_source: [],
  by_error_class: [],
};

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/tools']}>
      <TooltipProvider>
        <Routes>
          <Route path="/tools" element={<ToolsList />} />
          <Route path="/tools/:name" element={<div data-testid="tool-detail" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('ToolsList', () => {
  beforeEach(() => {
    statsSpy.mockReset();
  });

  it('renders the page header', () => {
    statsSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: 'Tools' })).toBeInTheDocument();
  });

  it('renders cards sorted by calls (descending)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    // read (200) is the most-called; bash (150) is second; write (50) is third.
    const readLink = screen.getByRole('link', { name: /read/i });
    expect(readLink).toBeInTheDocument();
  });

  it('cards link to /tools/:name with the namespace::name slug (URL-encoded)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    // React Router's <Link> URL-encodes the colons in the slug.
    expect(screen.getByRole('link', { name: /read/i })).toHaveAttribute(
      'href',
      '/tools/filesystem%3A%3Aread',
    );
  });

  it('shows the error state', () => {
    statsSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new Error('boom'),
    });
    renderPage();
    expect(screen.getByText(/Failed to load tools: boom/)).toBeInTheDocument();
  });

  it('shows the empty state', () => {
    statsSpy.mockReturnValue({
      data: { ...FULL_STATS, by_tool: [] },
      isPending: false,
      isError: false,
    });
    renderPage();
    expect(screen.getByText(/No tools in this window/i)).toBeInTheDocument();
  });

  it('renders the summary strip (Tools / Total calls / Failures / Sessions)', () => {
    statsSpy.mockReturnValue({ data: FULL_STATS, isPending: false, isError: false });
    renderPage();
    expect(screen.getAllByText('Tools').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Total calls').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failures').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Sessions').length).toBeGreaterThanOrEqual(1);
  });
});
