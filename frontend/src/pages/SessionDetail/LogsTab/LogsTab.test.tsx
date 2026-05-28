import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { LogItem, LogsResponse } from '../../../api/types';
import { ApiError } from '../../../api/client';

// LogsTab drives the useSessionLogs infinite query with a severity multi-select
// and a "Load more" control. useSessionLogs is MOCKED so the test asserts the
// page states + that toggling a severity re-invokes the hook with the new set
// (empty set = all severities). Pagination behavior of the hook itself is
// covered by logs.test.tsx.

const logsSpy = vi.fn();

vi.mock('../../../api/logs', () => ({
  useSessionLogs: (...args: unknown[]) => logsSpy(...args) as unknown,
}));

import { LogsTab } from './LogsTab';

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

function logItem(over: Partial<LogItem>): LogItem {
  return {
    ts: 1_700_000_000_000_000,
    severity: 'INF',
    source: 'aiagent_v3',
    op_id: null,
    message: 'hello',
    extras: null,
    ...over,
  };
}

function page(items: LogItem[], next?: string): LogsResponse {
  return next === undefined ? { items } : { items, next_cursor: next };
}

beforeEach(() => {
  logsSpy.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('LogsTab', () => {
  it('renders the loading state', () => {
    logsSpy.mockReturnValue(result({ isPending: true }));
    render(<LogsTab sessionId="s1" />);
    expect(screen.getByText('Loading logs…')).toBeInTheDocument();
  });

  it('renders the error state with the ApiError message', () => {
    logsSpy.mockReturnValue(
      result({ isError: true, error: new ApiError(400, 'BAD_REQUEST', 'bad severity') }),
    );
    render(<LogsTab sessionId="s1" />);
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load logs');
    expect(alert).toHaveTextContent('bad severity');
  });

  it('renders the empty state', () => {
    logsSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    render(<LogsTab sessionId="s1" />);
    expect(screen.getByText('No log entries for this session.')).toBeInTheDocument();
  });

  it('renders log rows from the pages', () => {
    logsSpy.mockReturnValue(
      result({
        data: {
          pages: [page([logItem({ message: 'first line', severity: 'WRN' })])],
          pageParams: [''],
        },
      }),
    );
    render(<LogsTab sessionId="s1" />);
    expect(screen.getByText('first line')).toBeInTheDocument();
    // 'WRN' also appears as a severity checkbox label; scope to the log table.
    const table = screen.getByRole('table');
    expect(within(table).getByText('WRN')).toBeInTheDocument();
  });

  it('starts with all severities (empty set) and shows the hint', () => {
    logsSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    render(<LogsTab sessionId="s1" />);
    // First call: empty severities = all.
    expect(logsSpy.mock.calls[0]?.[1]).toEqual({ severities: [] });
    expect(screen.getByText('all severities')).toBeInTheDocument();
  });

  it('toggling a severity re-invokes the hook with the new set', async () => {
    const user = userEvent.setup();
    logsSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    render(<LogsTab sessionId="s1" />);

    await user.click(screen.getByRole('checkbox', { name: 'ERR' }));
    // The latest call carries the selected severity.
    const lastArgs = logsSpy.mock.calls.at(-1);
    expect(lastArgs?.[1]).toEqual({ severities: ['ERR'] });
    expect(screen.getByText('1 selected')).toBeInTheDocument();
  });

  it('unchecking the last severity returns to all (empty set)', async () => {
    const user = userEvent.setup();
    logsSpy.mockReturnValue(result({ data: { pages: [page([])], pageParams: [''] } }));
    render(<LogsTab sessionId="s1" />);

    const err = screen.getByRole('checkbox', { name: 'ERR' });
    await user.click(err); // select
    await user.click(err); // deselect
    expect(logsSpy.mock.calls.at(-1)?.[1]).toEqual({ severities: [] });
  });

  it('"Load more logs" calls fetchNextPage', async () => {
    const user = userEvent.setup();
    const fetchNextPage = vi.fn();
    logsSpy.mockReturnValue(
      result({
        data: { pages: [page([logItem({})], 'cur-2')], pageParams: [''] },
        hasNextPage: true,
        fetchNextPage,
      }),
    );
    render(<LogsTab sessionId="s1" />);
    await user.click(screen.getByRole('button', { name: 'Load more logs' }));
    expect(fetchNextPage).toHaveBeenCalledTimes(1);
  });
});
