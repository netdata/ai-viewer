import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { HomeSummary } from '../../api/home';
import { HomeSummaryCard } from './HomeSummaryCard';

// Mock useHomeSummary + todayMidnightUs so we control the data shape.
const homeSpy = vi.fn();
vi.mock('../../api/home', () => ({
  useHomeSummary: (...args: unknown[]) => homeSpy(...args) as unknown,
  todayMidnightUs: () => 1_700_000_000_000_000,
}));

const FULL_SUMMARY: HomeSummary = {
  today: {
    sessionCount: 50,
    opCount: 200,
    tokensIn: 100_000,
    tokensOut: 50_000,
    tokensCacheRead: 10_000,
    tokensCacheWrite: 5_000,
    costUsd: 12.34,
    failures: 5,
  },
  running: { sessionCount: 7, costUsd: 0.42 },
  todayFromUs: 1_700_000_000_000_000,
};

function renderCard() {
  return render(
    <MemoryRouter initialEntries={['/']}>
      <TooltipProvider>
        <Routes>
          <Route path="/" element={<HomeSummaryCard />} />
          <Route path="/failures" element={<div data-testid="failures-page" />} />
          <Route path="/stats" element={<div data-testid="stats-page" />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe('HomeSummaryCard', () => {
  beforeEach(() => {
    homeSpy.mockReset();
  });

  it('renders five tile labels even while pending', () => {
    homeSpy.mockReturnValue({ data: undefined, isPending: true, isError: false });
    renderCard();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Today\u2019s spend')).toBeInTheDocument();
    expect(screen.getByText('Failed today')).toBeInTheDocument();
    expect(screen.getByText('Sessions today')).toBeInTheDocument();
    expect(screen.getByText('Reliability')).toBeInTheDocument();
  });

  it('renders the loaded values from a populated summary', () => {
    homeSpy.mockReturnValue({ data: FULL_SUMMARY, isPending: false, isError: false });
    renderCard();
    // 7 running sessions
    expect(screen.getByText('7')).toBeInTheDocument();
    // $12.34 spent today
    expect(screen.getByText('$12.34')).toBeInTheDocument();
    // 5 failures today
    expect(screen.getByText('5')).toBeInTheDocument();
    // 50 sessions today (also appears as "7" if it collides — 50 is unique)
    expect(screen.getByText('50')).toBeInTheDocument();
    // reliability: (50-5)/50 = 0.9 => 90.0%
    expect(screen.getByText('90.0%')).toBeInTheDocument();
  });

  it('highlights reliability as failed when < 70%', () => {
    homeSpy.mockReturnValue({
      data: {
        ...FULL_SUMMARY,
        today: { ...FULL_SUMMARY.today!, sessionCount: 100, failures: 80 },
      },
      isPending: false,
      isError: false,
    });
    renderCard();
    // (100-80)/100 = 0.2 = 20.0%
    const reliability = screen.getByText('20.0%');
    expect(reliability.className).toContain('text-status-failed');
  });

  it('highlights reliability as success when >= 90%', () => {
    homeSpy.mockReturnValue({
      data: {
        ...FULL_SUMMARY,
        today: { ...FULL_SUMMARY.today!, sessionCount: 100, failures: 5 },
      },
      isPending: false,
      isError: false,
    });
    renderCard();
    // (100-5)/100 = 0.95 = 95.0%
    const reliability = screen.getByText('95.0%');
    expect(reliability.className).toContain('text-status-completed');
  });

  it('renders null (skeleton) when data has no today totals', () => {
    homeSpy.mockReturnValue({
      data: { today: null, running: null, todayFromUs: 1_700_000_000_000_000 },
      isPending: false,
      isError: false,
    });
    const { container } = renderCard();
    // 5 tiles × 1 skeleton each = 5 skeletons (Skeleton is rendered as a div
    // with the animate-pulse class).
    const skeletons = container.querySelectorAll('.animate-pulse');
    expect(skeletons.length).toBeGreaterThanOrEqual(5);
  });

  it('renders navigation links to filtered views', () => {
    homeSpy.mockReturnValue({ data: FULL_SUMMARY, isPending: false, isError: false });
    renderCard();
    expect(screen.getByRole('link', { name: /Active/i })).toHaveAttribute('href', '/?status=running');
    expect(screen.getByRole('link', { name: /Today\u2019s spend/i })).toHaveAttribute(
      'href',
      '/stats?from=1700000000000000',
    );
    expect(screen.getByRole('link', { name: /Failed today/i })).toHaveAttribute(
      'href',
      '/failures?from=1700000000000000',
    );
  });
});
