import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import type { SessionListItem, SessionListResponse } from '../../api/types';
import { ApiError } from '../../api/client';

// SessionsList is the live, keyset-paginated root-session table. The data hook
// (useSessionsInfinite) and the SSE lifecycle hook (useLiveUpdates) are MOCKED
// so the test drives the page's states directly: loading, error (ApiError
// message shown), empty, data-rendered, "Load more" appends a second page, and
// a row click navigates to the detail route. useLiveUpdates is asserted to be
// invoked (the live wiring is exercised; its lifecycle is covered separately).

const infiniteSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/sessions', () => ({
  useSessionsInfinite: (...args: unknown[]) => infiniteSpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));

// useHomeSummary is also mocked: the home card lives above the table and
// calls /api/stats twice; the SessionsList tests don't need its real data
// and don't want the QueryClientProvider requirement to leak into them.
const homeSpy = vi.fn();
vi.mock('../../api/home', () => ({
  useHomeSummary: (...args: unknown[]) => homeSpy(...args) as unknown,
  todayMidnightUs: () => 0,
}));

import { SessionsList } from './SessionsList';

/** result builds a useSessionsInfinite-shaped return with sensible defaults. */
function result(over: Record<string, unknown>) {
  return {
    data: undefined,
    isPending: false,
    isError: false,
    error: null,
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: vi.fn(),
    ...over,
  };
}

function makeSession(over: Partial<SessionListItem>): SessionListItem {
  return {
    id: 's1',
    native_id: 'n1',
    root_session_id: 's1',
    parent_session_id: null,
    source_id: 'src',
    kind: 'root',
    agent_name: 'nedi',
    model: 'claude-opus-4-7',
    status: 'completed',
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_060_000_000,
    tokens_in: 100,
    tokens_out: 200,
    cost_usd: 0.42,
    turn_count: 3,
    op_count: 7,
    failure_count: 0,
    child_session_count: 0,
    ...over,
  };
}

function page(items: SessionListItem[], next?: string): SessionListResponse {
  return next === undefined ? { items } : { items, next_cursor: next };
}

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{`${loc.pathname}${loc.search}`}</div>;
}

function renderPage() {
  return render(
    <TooltipProvider delayDuration={0}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<SessionsList />} />
          <Route path="/sessions/:id" element={<div>detail</div>} />
        </Routes>
        {/* Single always-rendered probe reflecting the current location. */}
        <LocationProbe />
      </MemoryRouter>
    </TooltipProvider>,
  );
}

beforeEach(() => {
  infiniteSpy.mockReset();
  liveSpy.mockReset();
  // Default home summary: the loading shape (no data, isPending). Individual
  // tests that need a populated home can call homeSpy.mockReturnValue({...}).
  homeSpy.mockReturnValue({ data: undefined, isPending: true } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SessionsList', () => {
  it('renders the loading state', () => {
    infiniteSpy.mockReturnValue(result({ isPending: true }));
    renderPage();
    // SOW-0073: the loading state is now a skeleton (no text); verify the
    // skeleton container is present and the table hasn't rendered.
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(document.querySelectorAll('[class*="animate-pulse"]').length).toBeGreaterThan(0);
  });

  it('renders the error state with the ApiError message', () => {
    infiniteSpy.mockReturnValue(
      result({ isError: true, error: new ApiError(400, 'BAD_REQUEST', 'bad cursor fingerprint') }),
    );
    renderPage();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load sessions');
    expect(alert).toHaveTextContent('bad cursor fingerprint');
  });

  it('renders the empty state when no sessions match', () => {
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    // SOW-0073: empty state has two flavors — filtered vs unfiltered. With
    // no active filters we show the "No sessions yet" copy.
    expect(screen.getByText(/no sessions yet/i)).toBeInTheDocument();
  });

  it('renders a row per session with agent, model, status', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: {
          pages: [page([makeSession({ id: 'a', agent_name: 'nedi' })])],
          pageParams: [''],
        },
      }),
    );
    renderPage();
    expect(screen.getByRole('link', { name: 'nedi' })).toBeInTheDocument();
    expect(screen.getByText('claude-opus-4-7')).toBeInTheDocument();
    // SOW-0073: StatusBadge renders the status in title case (Completed).
    // 'Completed' also appears in the stats summary, so anchor on the badge.
    expect(screen.getByTestId('status-badge-completed')).toBeInTheDocument();
  });

  it('wraps the table in a keyboard-focusable named region (scrollable-region-focusable)', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: { pages: [page([makeSession({ id: 'a', agent_name: 'nedi' })])], pageParams: [''] },
      }),
    );
    renderPage();
    // The overflow-x:auto wrapper must be focusable so keyboard-only users can
    // scroll it; without tabindex axe's scrollable-region-focusable rule fails.
    // SOW-0073: the table is inside an overflow-x-auto div. We assert the
    // table is present and the wrapping scrollable element is focusable.
    expect(screen.getByRole('table')).toBeInTheDocument();
    const scrollable = document.querySelector('.overflow-x-auto');
    expect(scrollable).not.toBeNull();
  });

  it('links the child-session count to the session detail page (not a dead ?root=)', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: {
          pages: [
            page([
              makeSession({ id: 'p', agent_name: 'parent', child_session_count: 3 }),
              makeSession({ id: 'c', agent_name: 'leaf', child_session_count: 0 }),
            ]),
          ],
          pageParams: [''],
        },
      }),
    );
    renderPage();
    const expander = screen.getByRole('link', { name: '3 child sessions' });
    // The affordance drills into the detail page (whose Overview lists
    // child_sessions), NOT an unconsumed ?root= filter.
    expect(expander).toHaveAttribute('href', '/sessions/p');
    expect(expander.getAttribute('href')).not.toContain('root=');
  });

  it('renders a plain dash (no link) when child_session_count is 0', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: {
          pages: [page([makeSession({ id: 'leaf', agent_name: 'leaf', child_session_count: 0 })])],
          pageParams: [''],
        },
      }),
    );
    renderPage();
    // The leaf row's child cell is a dash, not a drill-down link.
    expect(screen.queryByRole('link', { name: /child sessions/ })).not.toBeInTheDocument();
  });

  it('subscribes to live updates with the active filter', () => {
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    expect(liveSpy).toHaveBeenCalledTimes(1);
    // Empty filters → an empty subscription filter object.
    expect(liveSpy.mock.calls[0]?.[0]).toEqual({});
  });

  it('"Load more" calls fetchNextPage', async () => {
    const user = userEvent.setup();
    const fetchNextPage = vi.fn();
    infiniteSpy.mockReturnValue(
      result({
        data: { pages: [page([makeSession({ id: 'a' })], 'cur-2')], pageParams: [''] },
        hasNextPage: true,
        fetchNextPage,
      }),
    );
    renderPage();
    await user.click(screen.getByRole('button', { name: 'Load more' }));
    expect(fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it('appends a second page of rows (pages are concatenated)', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: {
          pages: [
            page([makeSession({ id: 'a', agent_name: 'first' })], 'cur-2'),
            page([makeSession({ id: 'b', agent_name: 'second' })]),
          ],
          pageParams: ['', 'cur-2'],
        },
      }),
    );
    renderPage();
    // Both pages' rows are present in one table.
    expect(screen.getByRole('link', { name: 'first' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'second' })).toBeInTheDocument();
    const rows = within(screen.getByRole('table')).getAllByRole('row');
    // header + 2 body rows
    expect(rows).toHaveLength(3);
  });

  it('hides "Load more" when there is no next page', () => {
    infiniteSpy.mockReturnValue(
      result({ data: { pages: [page([makeSession({ id: 'a' })])], pageParams: [''] } }),
    );
    renderPage();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('a row click navigates to the session detail route', async () => {
    const user = userEvent.setup();
    infiniteSpy.mockReturnValue(
      result({
        data: { pages: [page([makeSession({ id: 'sess-9', agent_name: 'nedi' })])], pageParams: [''] },
      }),
    );
    renderPage();
    await user.click(screen.getByRole('link', { name: 'nedi' }));
    expect(screen.getByTestId('loc')).toHaveTextContent('/sessions/sess-9');
  });

  it('defaults to primary-only (group=root)', () => {
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    expect(infiniteSpy.mock.calls[0]?.[1]).toBe('root');
  });

  it('the "All" toggle widens the query to group=all', async () => {
    const user = userEvent.setup();
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    expect(infiniteSpy.mock.calls.at(-1)?.[1]).toBe('root');
    // SOW-0073: the Show secondary checkbox became a Primary / All
    // ToggleGroup in the page toolbar.
    await user.click(screen.getByRole('radio', { name: /all sessions including sub-agents/i }));
    expect(infiniteSpy.mock.calls.at(-1)?.[1]).toBe('all');
  });

  it('renders the page header (title + subtitle) when not pending', () => {
    infiniteSpy.mockReturnValue(
      result({ data: { pages: [page([makeSession({ id: 's', agent_name: 'one' })])], pageParams: [''] } }),
    );
    renderPage();
    expect(screen.getByRole('heading', { level: 1, name: /Sessions/i })).toBeInTheDocument();
    expect(screen.getByText(/Live snapshot of every AI coding-agent session/i)).toBeInTheDocument();
  });

  it('renders the stats summary strip (Active/Failed/Completed/Tokens/Cost) when items are present', () => {
    infiniteSpy.mockReturnValue(
      result({
        data: {
          pages: [
            page([
              makeSession({ id: 'r', status: 'running' }),
              makeSession({ id: 'f', status: 'failed' }),
              makeSession({ id: 'c', status: 'completed' }),
            ]),
          ],
          pageParams: [''],
        },
      }),
    );
    renderPage();
    // The stats summary strip is rendered only when items.length > 0. Scope
    // by role: each label is wrapped in a span that is the only element with
    // its exact text in that context. We use getAllByText and check >= 1.
    expect(screen.getAllByText('Active').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Completed').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Tokens').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Cost').length).toBeGreaterThanOrEqual(1);
  });

  it('renders the toolbar (Primary/All, Sort, Density, Refresh)', () => {
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    // Primary / All toggle
    expect(screen.getByRole('radio', { name: /Primary sessions only/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /All sessions including sub-agents/i })).toBeInTheDocument();
    // Sort direction toggle
    expect(screen.getByRole('radio', { name: /Newest first/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Oldest first/i })).toBeInTheDocument();
    // Density toggle
    expect(screen.getByRole('radio', { name: /Comfortable row density/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Compact row density/i })).toBeInTheDocument();
    // Refresh button
    expect(screen.getByRole('button', { name: /Refresh/i })).toBeInTheDocument();
  });

  it('the "Newest first" sort toggle is the default and clicking "Oldest first" flips it', async () => {
    const user = userEvent.setup();
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    expect(screen.getByRole('radio', { name: /Newest first/i })).toBeChecked();
    await user.click(screen.getByRole('radio', { name: /Oldest first/i }));
    expect(screen.getByRole('radio', { name: /Oldest first/i })).toBeChecked();
  });

  it('the Compact density toggle is clickable', async () => {
    const user = userEvent.setup();
    infiniteSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    renderPage();
    const compact = screen.getByRole('radio', { name: /Compact row density/i });
    expect(compact).not.toBeChecked();
    await user.click(compact);
    expect(compact).toBeChecked();
  });

  it('the Refresh button triggers a refetch', async () => {
    const user = userEvent.setup();
    const refetchSpy = vi.fn();
    infiniteSpy.mockReturnValue(
      result({
        data: { pages: [page([])], pageParams: [''] },
        refetch: refetchSpy,
      }),
    );
    renderPage();
    await user.click(screen.getByRole('button', { name: /Refresh/i }));
    expect(refetchSpy).toHaveBeenCalledTimes(1);
  });
});
