import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import type {
  AggregateResponse,
  SearchResponse,
  TopResponse,
} from '../../api/types';
import { ApiError } from '../../api/client';

// Stats is the live /stats analytics dashboard: a line-chart section
// (useAggregate), a top-N bar section (useTop), and a deep-search box
// (useSearch). The three data hooks and the SSE lifecycle hook (useLiveUpdates)
// are MOCKED so the test drives the page directly: loading/error/data states,
// the control→hook-arg wiring (metric/bucket/dimension selects), the debounced
// search box driving useSearch + rendering links to /sessions/:id, the
// "logs not indexed" note, the empty-query no-call rule, and the live
// subscription. The real 9a chart components render (they are presentational and
// covered by their own tests) so the page test asserts the rendered role="img".

const aggSpy = vi.fn();
const topSpy = vi.fn();
const searchSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/stats', () => ({
  useAggregate: (...args: unknown[]) => aggSpy(...args) as unknown,
  useTop: (...args: unknown[]) => topSpy(...args) as unknown,
  useSearch: (...args: unknown[]) => searchSpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));

import { Stats } from './Stats';

/** queryResult builds a UseQueryResult-shaped object with sensible defaults. */
function queryResult<T>(over: Record<string, unknown>): T {
  return {
    data: undefined,
    isPending: false,
    isError: false,
    error: null,
    ...over,
  } as T;
}

function aggregate(over: Partial<AggregateResponse> = {}): AggregateResponse {
  return {
    bucket: 'daily',
    metric: 'cost',
    buckets: [
      { bucket_ts: 1_700_000_000_000_000, series: [{ key: '', value: 3 }] },
      { bucket_ts: 1_700_086_400_000_000, series: [{ key: '', value: 5 }] },
    ],
    ...over,
  };
}

function top(over: Partial<TopResponse> = {}): TopResponse {
  return {
    dimension: 'model',
    metric: 'cost',
    items: [
      { key: 'claude-opus-4-7', value: 12 },
      { key: 'gpt-5', value: 7 },
    ],
    ...over,
  };
}

function searchResp(over: Partial<SearchResponse> = {}): SearchResponse {
  return {
    logs_indexed: true,
    ops: [],
    logs: [],
    ...over,
  };
}

/** mountStates seeds all three hooks; callers override per-section. */
function mountStates(opts: {
  agg?: Record<string, unknown>;
  top?: Record<string, unknown>;
  search?: Record<string, unknown>;
}) {
  aggSpy.mockReturnValue(queryResult(opts.agg ?? { data: aggregate() }));
  topSpy.mockReturnValue(queryResult(opts.top ?? { data: top() }));
  searchSpy.mockReturnValue(queryResult(opts.search ?? { data: undefined }));
}

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/stats']}>
      <Stats />
    </MemoryRouter>,
  );
}

const DEBOUNCE_MS = 300;

/**
 * typeSearch sets the search input synchronously (fireEvent.change, no userEvent
 * inter-key timers) then flushes the debounce window under fake timers, so the
 * results branch (gated on a non-empty debounced query) renders. The caller is
 * responsible for vi.useFakeTimers()/vi.useRealTimers() around the call.
 */
function typeSearch(value: string): void {
  fireEvent.change(screen.getByLabelText(/search ops and logs/i), {
    target: { value },
  });
  act(() => {
    vi.advanceTimersByTime(DEBOUNCE_MS + 1);
  });
}

beforeEach(() => {
  aggSpy.mockReset();
  topSpy.mockReset();
  searchSpy.mockReset();
  liveSpy.mockReset();
  mountStates({});
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('Stats', () => {
  it('renders the page heading and section headings', () => {
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: /statistics/i })).toBeInTheDocument();
    // Section headings give the page a real heading structure (a11y).
    expect(screen.getByRole('heading', { name: /trends over time/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /top/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /search/i })).toBeInTheDocument();
  });

  it('renders the line-chart loading state while the aggregate is pending', () => {
    mountStates({ agg: { isPending: true } });
    renderPage();
    expect(screen.getByText(/loading trends/i)).toBeInTheDocument();
  });

  it('renders the line-chart error state with the ApiError message', () => {
    mountStates({
      agg: { isError: true, error: new ApiError(400, 'BAD_REQUEST', 'bad metric') },
    });
    renderPage();
    const alerts = screen.getAllByRole('alert');
    expect(alerts.some((a) => a.textContent?.includes('bad metric'))).toBe(true);
  });

  it('renders the top-N loading and error states independently of the line chart', () => {
    mountStates({
      agg: { data: aggregate() },
      top: { isError: true, error: new ApiError(400, 'BAD_REQUEST', 'bad dimension') },
    });
    renderPage();
    // The line chart still renders even though top failed (independent queries).
    expect(screen.getAllByRole('img').length).toBeGreaterThanOrEqual(1);
    const alerts = screen.getAllByRole('alert');
    expect(alerts.some((a) => a.textContent?.includes('bad dimension'))).toBe(true);
  });

  it('renders both charts as role="img" when data is present', () => {
    renderPage();
    // The line chart + the bar chart each render an accessible role="img".
    const figures = screen.getAllByRole('img');
    expect(figures.length).toBe(2);
  });

  it('calls useAggregate with the default bucket=daily + a metric', () => {
    renderPage();
    const opts = aggSpy.mock.calls[0]?.[1] as { bucket: string; groupBy: string; metric: string };
    expect(opts.bucket).toBe('daily');
    expect(opts.groupBy).toBe('total');
    expect(opts.metric).toBe('cost');
  });

  it('changing the line-chart metric select updates the useAggregate metric arg', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/trend metric/i), 'tokens_in');
    // The latest call carries the newly-selected metric.
    const lastOpts = aggSpy.mock.calls.at(-1)?.[1] as { metric: string };
    expect(lastOpts.metric).toBe('tokens_in');
  });

  it('toggling the bucket to hourly updates the useAggregate bucket arg', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/time bucket/i), 'hourly');
    const lastOpts = aggSpy.mock.calls.at(-1)?.[1] as { bucket: string };
    expect(lastOpts.bucket).toBe('hourly');
  });

  it('calls useTop with the default dimension=model + a fixed n', () => {
    renderPage();
    const opts = topSpy.mock.calls[0]?.[1] as { dimension: string; metric: string; n: number };
    expect(opts.dimension).toBe('model');
    expect(opts.n).toBeGreaterThan(0);
  });

  it('changing the top-N dimension select updates the useTop dimension arg', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/breakdown dimension/i), 'tool');
    const lastOpts = topSpy.mock.calls.at(-1)?.[1] as { dimension: string };
    expect(lastOpts.dimension).toBe('tool');
  });

  it('changing the top-N metric select updates the useTop metric arg', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/breakdown metric/i), 'failures');
    const lastOpts = topSpy.mock.calls.at(-1)?.[1] as { metric: string };
    expect(lastOpts.metric).toBe('failures');
  });

  it('does not call useSearch with a non-empty query until the input is typed', () => {
    renderPage();
    // The initial render passes an empty query (the hook is disabled on empty q).
    expect(searchSpy.mock.calls[0]?.[1]).toBe('');
  });

  it('debounces the search input then drives useSearch with the typed term', () => {
    vi.useFakeTimers();
    try {
      renderPage();
      // Type synchronously WITHOUT flushing the debounce: the query passed to
      // useSearch is still empty (no per-keystroke fetch).
      fireEvent.change(screen.getByLabelText(/search ops and logs/i), {
        target: { value: 'refactor' },
      });
      expect(searchSpy.mock.calls.at(-1)?.[1]).toBe('');
      // After the debounce window elapses, the resolved query is the typed term.
      act(() => {
        vi.advanceTimersByTime(DEBOUNCE_MS + 1);
      });
      expect(searchSpy.mock.calls.at(-1)?.[1]).toBe('refactor');
    } finally {
      vi.useRealTimers();
    }
  });

  it('renders op results as links to the session, with kind/name/snippet', () => {
    mountStates({
      search: {
        data: searchResp({
          ops: [
            {
              op_id: 'op-1',
              session_id: 'sess-9',
              kind: 'tool',
              name: 'bash',
              model: 'claude-opus-4-7',
              snippet: 'ran the refactor',
              rank: 1,
            },
          ],
        }),
      },
    });
    vi.useFakeTimers();
    try {
      renderPage();
      typeSearch('refactor');
    } finally {
      vi.useRealTimers();
    }
    const link = screen.getByRole('link', { name: /bash/i });
    expect(link).toHaveAttribute('href', '/sessions/sess-9');
    expect(screen.getByText(/ran the refactor/i)).toBeInTheDocument();
  });

  it('renders log results as links to the session, with severity/snippet', () => {
    mountStates({
      search: {
        data: searchResp({
          logs_indexed: true,
          logs: [
            {
              log_id: 42,
              session_id: 'sess-7',
              op_id: 'op-2',
              severity: 'ERR',
              ts: 1_700_000_000_000_000,
              snippet: 'panic in handler',
              rank: 1,
            },
          ],
        }),
      },
    });
    vi.useFakeTimers();
    try {
      renderPage();
      typeSearch('panic');
    } finally {
      vi.useRealTimers();
    }
    expect(screen.getByText(/panic in handler/i)).toBeInTheDocument();
    // The log row links to its session.
    const logLinks = screen
      .getAllByRole('link')
      .filter((a) => a.getAttribute('href') === '/sessions/sess-7');
    expect(logLinks.length).toBeGreaterThanOrEqual(1);
  });

  it('shows a "logs not indexed" note when logs_indexed is false', () => {
    mountStates({
      search: { data: searchResp({ logs_indexed: false, ops: [], logs: [] }) },
    });
    vi.useFakeTimers();
    try {
      renderPage();
      typeSearch('anything');
    } finally {
      vi.useRealTimers();
    }
    expect(screen.getByText(/logs (are )?not indexed/i)).toBeInTheDocument();
  });

  it('does NOT show the "logs not indexed" note when logs are indexed', () => {
    mountStates({
      search: { data: searchResp({ logs_indexed: true, ops: [], logs: [] }) },
    });
    vi.useFakeTimers();
    try {
      renderPage();
      typeSearch('anything');
    } finally {
      vi.useRealTimers();
    }
    expect(screen.queryByText(/logs (are )?not indexed/i)).not.toBeInTheDocument();
  });

  it('subscribes to live updates with the active filter subscription', () => {
    renderPage();
    expect(liveSpy).toHaveBeenCalledTimes(1);
    // Empty filters → an empty subscription filter object.
    expect(liveSpy.mock.calls[0]?.[0]).toEqual({});
  });

  it('gives every control an accessible name (label association)', () => {
    renderPage();
    // getByLabelText throws if the control is unlabelled — these assertions ARE
    // the a11y check for the controls (full axe is Chunk 11).
    expect(screen.getByLabelText(/trend metric/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/time bucket/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/breakdown dimension/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/breakdown metric/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/search ops and logs/i)).toBeInTheDocument();
  });

  it('renders search results inside a labelled region', () => {
    mountStates({
      search: {
        data: searchResp({
          ops: [
            {
              op_id: 'op-1',
              session_id: 'sess-9',
              kind: 'tool',
              name: 'bash',
              model: 'm',
              snippet: 'hit',
              rank: 1,
            },
          ],
        }),
      },
    });
    vi.useFakeTimers();
    try {
      renderPage();
      typeSearch('hit');
    } finally {
      vi.useRealTimers();
    }
    const results = screen.getByRole('region', { name: /search results/i });
    expect(within(results).getByText(/hit/i)).toBeInTheDocument();
  });
});
