import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';
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
    <MemoryRouter initialEntries={['/']}>
      <Routes>
        <Route path="/" element={<SessionsList />} />
        <Route path="/sessions/:id" element={<div>detail</div>} />
      </Routes>
      {/* Single always-rendered probe reflecting the current location. */}
      <LocationProbe />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  infiniteSpy.mockReset();
  liveSpy.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SessionsList', () => {
  it('renders the loading state', () => {
    infiniteSpy.mockReturnValue(result({ isPending: true }));
    renderPage();
    expect(screen.getByText('Loading sessions…')).toBeInTheDocument();
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
    expect(screen.getByText('No sessions match the current filters.')).toBeInTheDocument();
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
    expect(screen.getByText('completed')).toBeInTheDocument();
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
});
