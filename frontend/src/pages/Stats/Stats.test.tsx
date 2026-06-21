import { useEffect } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useSearchParams } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type {
  AggregateResponse,
  SearchResponse,
  StatsResponse,
  TopResponse,
} from '../../api/types';
import { ApiError } from '../../api/client';
import { useFilters } from '../../state/filters';
import { STAT_PARAM_KEYS } from './shareState';

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
const statsSpy = vi.fn();
const searchSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/stats', () => ({
  useAggregate: (...args: unknown[]) => aggSpy(...args) as unknown,
  useTop: (...args: unknown[]) => topSpy(...args) as unknown,
  useStats: (...args: unknown[]) => statsSpy(...args) as unknown,
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

/** statsData builds a StatsResponse with one row per breakdown dimension. */
function statsData(over: Partial<StatsResponse> = {}): StatsResponse {
  return {
    totals: {
      session_count: 10,
      turn_count: 20,
      op_count: 40,
      tokens_in: 1000,
      tokens_out: 2000,
      tokens_cache_read: 3000,
      tokens_cache_write: 500,
      cost_usd: 1.5,
      failures: 3,
      duration_us: 9_000_000,
    },
    by_model: [
      {
        name: 'claude-opus-4-7',
        provider: 'anthropic',
        calls: 5,
        tokens_in: 500,
        tokens_out: 1000,
        tokens_cache_read: 3000,
        tokens_cache_write: 500,
        cost_usd: 0.9,
        failures: 1,
        duration_us: 4_000_000,
        pct_of_cost: 0.6,
      },
    ],
    by_tool: [
      {
        namespace: 'shell',
        name: 'Bash',
        calls: 8,
        failures: 2,
        total_us: 5_000_000,
        pct_of_calls: 0.8,
      },
    ],
    by_agent: [
      {
        name: 'nedi',
        sessions: 6,
        failures: 1,
        tokens_in: 500,
        tokens_out: 1000,
        tokens_cache_read: 3000,
        tokens_cache_write: 500,
        cost_usd: 0.9,
        pct_of_sessions: 0.6,
      },
    ],
    by_status: [
      { status: 'completed', count: 7, cost_usd: 1.4, tokens_in: 900, tokens_out: 1900 },
      { status: 'failed', count: 3, cost_usd: 0.1, tokens_in: 100, tokens_out: 100 },
    ],
    by_source: [
      {
        source: 'src1',
        format: 'aiagent_v3',
        sessions: 10,
        failures: 3,
        cost_usd: 1.5,
        tokens_in: 1000,
        tokens_out: 2000,
        tokens_cache_read: 3000,
        op_count: 40,
      },
    ],
    by_error_class: [
      { error_class: 'io_error', sessions: 2, ops: 5, cost_usd: 0.05 },
    ],
    ...over,
  };
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
    content: [],
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
    <TooltipProvider delayDuration={0}>
      <MemoryRouter initialEntries={['/stats']}>
        <Stats />
      </MemoryRouter>
    </TooltipProvider>,
  );
}

/**
 * LocationProbe reports the live URL search params to the test via an onParams
 * callback (from an effect — never a render-time write) using the same
 * useSearchParams the page mutates, so assertions can read the post-change query
 * string (MemoryRouter has no window.location to inspect). Rendered as a sibling
 * inside the same router as <Stats />.
 */
function LocationProbe({ onParams }: { onParams: (p: URLSearchParams) => void }) {
  const [params] = useSearchParams();
  // Report the live params from an effect (post-commit), which the lint rules
  // permit — never a write during render. Tests read the latest via waitFor.
  useEffect(() => {
    onParams(params);
  }, [params, onParams]);
  return null;
}

/** A mutable holder the LocationProbe fills with the latest URL params. */
interface ParamsSink {
  params: URLSearchParams;
}

/** renderPageAt mounts <Stats /> at a given URL with a location probe sibling. */
function renderPageAt(initialEntry: string) {
  const sink: ParamsSink = { params: new URLSearchParams() };
  const utils = render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Stats />
      <LocationProbe onParams={(p) => (sink.params = p)} />
    </MemoryRouter>,
  );
  return { ...utils, sink };
}

/**
 * stubClipboard installs a fake navigator.clipboard with the given writeText.
 * jsdom exposes navigator.clipboard as a getter-only accessor, so a plain
 * assignment throws — we defineProperty (configurable) and return a restorer
 * that puts the original descriptor back so tests stay isolated.
 */
function stubClipboard(writeText: (text: string) => Promise<void>): () => void {
  const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  });
  return () => {
    if (original) {
      Object.defineProperty(navigator, 'clipboard', original);
    } else {
      // delete is allowed because we created the prop as configurable.
      delete (navigator as { clipboard?: unknown }).clipboard;
    }
  };
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
  statsSpy.mockReset();
  searchSpy.mockReset();
  liveSpy.mockReset();
  mountStates({});
  statsSpy.mockReturnValue(queryResult({ data: undefined }));
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
    expect(screen.getByLabelText(/group by/i)).toBeInTheDocument();
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

// ── Shareable URL (chart controls in the URL) + Copy-link button ────────────
// The 4 chart controls are URL-backed (shareState.ts) so the whole view is
// shareable; the global-filter params and the control params coexist via the
// same merge-patch. The Copy-link button writes window.location.href to the
// clipboard and announces the outcome through a polite live region.
describe('Stats — shareable URL + copy link', () => {
  it('initializes the controls from control params present in the URL (read)', () => {
    const url =
      `/stats?${STAT_PARAM_KEYS.trendMetric}=tokens_in` +
      `&${STAT_PARAM_KEYS.bucket}=hourly` +
      `&${STAT_PARAM_KEYS.topDimension}=tool` +
      `&${STAT_PARAM_KEYS.topMetric}=failures`;
    renderPageAt(url);
    // The controls drive the hook args, so the hook calls reflect the URL state.
    const aggOpts = aggSpy.mock.calls.at(-1)?.[1] as { bucket: string; metric: string };
    expect(aggOpts.metric).toBe('tokens_in');
    expect(aggOpts.bucket).toBe('hourly');
    const topOpts = topSpy.mock.calls.at(-1)?.[1] as { dimension: string; metric: string };
    expect(topOpts.dimension).toBe('tool');
    expect(topOpts.metric).toBe('failures');
    // And the selects render the URL-derived values.
    expect(screen.getByLabelText(/trend metric/i)).toHaveValue('tokens_in');
    expect(screen.getByLabelText(/time bucket/i)).toHaveValue('hourly');
    expect(screen.getByLabelText(/breakdown dimension/i)).toHaveValue('tool');
    expect(screen.getByLabelText(/breakdown metric/i)).toHaveValue('failures');
  });

  it('falls back to the defaults for unknown control param values (no throw)', () => {
    const url =
      `/stats?${STAT_PARAM_KEYS.trendMetric}=bogus` +
      `&${STAT_PARAM_KEYS.bucket}=weekly` +
      `&${STAT_PARAM_KEYS.topDimension}=nope`;
    expect(() => renderPageAt(url)).not.toThrow();
    const aggOpts = aggSpy.mock.calls.at(-1)?.[1] as { bucket: string; metric: string };
    expect(aggOpts.metric).toBe('cost'); // default
    expect(aggOpts.bucket).toBe('daily'); // default
    const topOpts = topSpy.mock.calls.at(-1)?.[1] as { dimension: string };
    expect(topOpts.dimension).toBe('model'); // default
    expect(screen.getByLabelText(/trend metric/i)).toHaveValue('cost');
  });

  it('a control change writes its URL param and PRESERVES filter + control params (write)', async () => {
    const user = userEvent.setup();
    // Start with a global filter param AND an unrelated control param set.
    const url = `/stats?agents=a,b&from=100&${STAT_PARAM_KEYS.trendMetric}=tokens_in`;
    const { sink } = renderPageAt(url);
    // Change a DIFFERENT control (the top-N dimension).
    await user.selectOptions(screen.getByLabelText(/breakdown dimension/i), 'tool');
    await waitFor(() => {
      expect(sink.params.get(STAT_PARAM_KEYS.topDimension)).toBe('tool');
    });
    // The changed control is written, the other control is preserved …
    expect(sink.params.get(STAT_PARAM_KEYS.trendMetric)).toBe('tokens_in');
    // … and the global filter params are preserved (merge-patch, not replace).
    expect(sink.params.get('agents')).toBe('a,b');
    expect(sink.params.get('from')).toBe('100');
  });

  // SOW-0087 chunk 3 (B5/B6): the Stats page wires BarChart's onBarClick to
  // push the dimension value into the URL filter and navigate to /sessions.
  // The BarChart-level onBarClick is covered by BarChart.test.tsx. This test
  // asserts the Stats integration point: that an onBarClick prop is supplied
  // to BarChart (i.e. the page calls the prop and does not navigate when
  // it is absent). Verified via the rendered bar DOM.
  it('wires BarChart with an onBarClick handler (SOW-0087 chunk 3)', async () => {
    mountStates({
      agg: { data: aggregate() },
      top: {
        data: {
          dimension: 'agent',
          metric: 'cost',
          items: [
            { key: 'agent-alpha', value: 100 },
          ],
        },
      },
      search: { data: searchResp() },
    });
    renderPage();
    // Switch to 'agent' dimension (default is 'model').
    fireEvent.change(screen.getByLabelText(/breakdown dimension/i), {
      target: { value: 'agent' },
    });
    await waitFor(() => {
      const bar = document.querySelector('[data-bar="agent-alpha"]') as SVGRectElement | null;
      expect(bar).not.toBeNull();
      // When onBarClick is wired, the bar is a focusable button.
      // (The base BarChart.test covers the wiring in isolation; this is
      // a smoke test that the page actually passes the prop.)
      expect(bar?.getAttribute('role')).toBe('button');
    });
  });

  // The onBarClick handler has 3 dimension branches (agent / model-or-provider
  // / tool). The base test covers agent; this one covers model so the
  // coverage gate doesn't drop.
  it('clicking a model bar pushes ?models=<key>', async () => {
    mountStates({
      agg: { data: aggregate() },
      top: {
        data: {
          dimension: 'model',
          metric: 'cost',
          items: [{ key: 'gpt-4o', value: 100 }],
        },
      },
      search: { data: searchResp() },
    });
    const sink: ParamsSink = { params: new URLSearchParams() };
    render(
      <TooltipProvider delayDuration={0}>
        <MemoryRouter initialEntries={['/stats']}>
          <Stats />
          <LocationProbe onParams={(p) => (sink.params = p)} />
        </MemoryRouter>
      </TooltipProvider>,
    );
    const bar = document.querySelector('[data-bar="gpt-4o"]') as SVGRectElement | null;
    expect(bar).not.toBeNull();
    fireEvent.click(bar!);
    await waitFor(() => {
      expect(sink.params.get('models')).toBe('gpt-4o');
    });
  });

  // Same coverage gate — exercises the tool branch.
  it('clicking a tool bar pushes ?tools=<key>', async () => {
    mountStates({
      agg: { data: aggregate() },
      top: {
        data: {
          dimension: 'tool',
          metric: 'cost',
          items: [{ key: 'filesystem::read', value: 100 }],
        },
      },
      search: { data: searchResp() },
    });
    const sink: ParamsSink = { params: new URLSearchParams() };
    render(
      <TooltipProvider delayDuration={0}>
        <MemoryRouter initialEntries={['/stats']}>
          <Stats />
          <LocationProbe onParams={(p) => (sink.params = p)} />
        </MemoryRouter>
      </TooltipProvider>,
    );
    // Default topDimension is 'model' — switch to 'tool' so the
    // onBarClick closure uses the tool branch.
    fireEvent.change(screen.getByLabelText(/breakdown dimension/i), {
      target: { value: 'tool' },
    });
    await waitFor(() => {
      const bar = document.querySelector('[data-bar="filesystem::read"]') as SVGRectElement | null;
      expect(bar).not.toBeNull();
      fireEvent.click(bar!);
    });
    await waitFor(() => {
      // useSearchParams decodes the URL automatically; the raw value
      // is the unescaped tool slug.
      expect(sink.params.get('tools')).toBe('filesystem::read');
    });
  });

  it('a real FilterBar filter change (useFilters) preserves the control params', async () => {
    // Drive the ACTUAL FilterBar code path: useFilters().setFilters merges a
    // filter patch via applyPatch on the same URL. The page's control params
    // must survive that write (both sides use the functional merge form).
    function FilterMutator() {
      const { setFilters } = useFilters();
      return (
        <button type="button" onClick={() => setFilters({ agents: ['x'] })}>
          set-agents
        </button>
      );
    }
    const sink: ParamsSink = { params: new URLSearchParams() };
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={[`/stats?${STAT_PARAM_KEYS.bucket}=hourly`]}>
        <Stats />
        <FilterMutator />
        <LocationProbe onParams={(p) => (sink.params = p)} />
      </MemoryRouter>,
    );
    await waitFor(() => {
      expect(sink.params.get(STAT_PARAM_KEYS.bucket)).toBe('hourly'); // control at mount
    });
    await user.click(screen.getByRole('button', { name: /set-agents/i }));
    await waitFor(() => {
      expect(sink.params.get('agents')).toBe('x'); // filter written
    });
    expect(sink.params.get(STAT_PARAM_KEYS.bucket)).toBe('hourly'); // control preserved
  });

  it('clicking "Copy link" writes window.location.href and announces "Link copied"', async () => {
    // setup() FIRST: userEvent v14 installs its own clipboard stub on setup, so
    // our spy must overwrite it afterwards to observe the page's writeText call.
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    const restore = stubClipboard(writeText);
    try {
      renderPage();
      await user.click(screen.getByRole('button', { name: /copy link/i }));
      expect(writeText).toHaveBeenCalledTimes(1);
      expect(writeText).toHaveBeenCalledWith(window.location.href);
      // The polite live region announces success.
      const status = screen.getByRole('status');
      await waitFor(() => {
        expect(status).toHaveTextContent(/link copied/i);
      });
    } finally {
      restore();
    }
  });

  it('shows a failure message (no silent failure) when the clipboard write rejects', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockRejectedValue(new Error('denied'));
    const restore = stubClipboard(writeText);
    try {
      renderPage();
      await user.click(screen.getByRole('button', { name: /copy link/i }));
      const status = screen.getByRole('status');
      await waitFor(() => {
        expect(status).toHaveTextContent(/copy failed/i);
      });
    } finally {
      restore();
    }
  });

  it('gives the Copy-link button an accessible name and provides a live region', () => {
    renderPage();
    // Accessible name via the button text.
    expect(screen.getByRole('button', { name: /copy link/i })).toBeInTheDocument();
    // The aria-live="polite" status region exists (empty until a copy happens).
    const status = screen.getByRole('status');
    expect(status).toHaveAttribute('aria-live', 'polite');
  });
});

// ── SOW-0067: multi-series trend, failure-rate, honest comparison table ─────
describe('Stats — SOW-0067 redesign', () => {
  it('changing the Group-by select updates the useAggregate groupBy arg', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/group by/i), 'model');
    const lastOpts = aggSpy.mock.calls.at(-1)?.[1] as { groupBy: string };
    expect(lastOpts.groupBy).toBe('model');
  });

  it('selecting Failure rate fetches BOTH failures and calls (derived rate)', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByLabelText(/trend metric/i), 'failure_rate');
    // The latest two useAggregate calls carry metrics failures + calls (the
    // page divides them client-side because ratios do not aggregate on the
    // rollup fast path).
    const metrics = aggSpy.mock.calls
      .slice(-2)
      .map((c) => (c[1] as { metric: string }).metric)
      .sort();
    expect(metrics).toEqual(['calls', 'failures']);
  });

  it('renders the comparison table with honest "—" cells for N/A metrics', () => {
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    renderPage();
    // The Tool dimension does not own tokens/cache/cost → those cells are "—".
    fireEvent.change(screen.getByLabelText(/comparison dimension/i), {
      target: { value: 'by_tool' },
    });
    const region = screen.getByRole('region', { name: /comparison breakdown/i });
    // Header has the full stable column set (no silently-hidden columns).
    const headers = within(region).getAllByRole('columnheader').map((h) => h.textContent ?? '');
    expect(headers.some((h) => /tokens in/i.test(h))).toBe(true);
    expect(headers.some((h) => /cache-hit/i.test(h))).toBe(true);
    // The Bash row shows "—" in the N/A cells (tools have no model tokens).
    expect(within(region).getByText('shell.Bash')).toBeInTheDocument();
    expect(within(region).getAllByText('—').length).toBeGreaterThan(0);
  });

  it('shows a real cache-hit % for the model dimension (data exists)', () => {
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    renderPage();
    fireEvent.change(screen.getByLabelText(/comparison dimension/i), {
      target: { value: 'by_model' },
    });
    const region = screen.getByRole('region', { name: /comparison breakdown/i });
    // claude-opus-4-7 cache_hit = 3000/(3000+500) = 85.7%.
    expect(within(region).getByText('85.7%')).toBeInTheDocument();
  });

  it('clicking a comparison row drills down to the sessions list with the filter', async () => {
    const user = userEvent.setup();
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    const { sink } = renderPageAt('/stats');
    // Default dimension is by_model; the label is a real (keyboard-reachable)
    // button — clicking it navigates with the filter applied.
    const region = screen.getByRole('region', { name: /comparison breakdown/i });
    const drillBtn = within(region).getByRole('button', { name: 'claude-opus-4-7' });
    await user.click(drillBtn);
    await waitFor(() => {
      expect(sink.params.get('models')).toBe('claude-opus-4-7');
    });
  });

  it('by_error_class rows are NOT interactive (no honest error_class filter)', () => {
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    renderPage();
    fireEvent.change(screen.getByLabelText(/comparison dimension/i), {
      target: { value: 'by_error_class' },
    });
    const region = screen.getByRole('region', { name: /comparison breakdown/i });
    // io_error renders as plain text, NOT a drill button — drilling to
    // status=failed would be a misleading wider result.
    expect(within(region).queryByRole('button', { name: 'io_error' })).toBeNull();
    expect(within(region).getByText('io_error')).toBeInTheDocument();
  });

  it('every data section has an Export CSV button (AC#5)', () => {
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    renderPage();
    const exportButtons = screen.getAllByRole('button', { name: /export csv/i });
    // Trend + Top-N + Comparison + Failure analysis = 4 export buttons.
    expect(exportButtons.length).toBe(4);
  });

  it('renders the failure-analysis section when error-class data is present', () => {
    statsSpy.mockReturnValue(queryResult({ data: statsData() }));
    renderPage();
    expect(screen.getByRole('heading', { name: /failure analysis/i })).toBeInTheDocument();
    // io_error appears in BOTH the failure table and the share bar — scope to
    // the table region to assert it uniquely.
    const region = screen.getByRole('region', { name: /error class breakdown/i });
    expect(within(region).getByText('io_error')).toBeInTheDocument();
  });
});
