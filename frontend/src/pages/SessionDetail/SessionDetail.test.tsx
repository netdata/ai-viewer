import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { ApiError } from '../../api/client';

// SessionDetail is the tabbed detail shell. useSessionDetail and useLiveUpdates
// are MOCKED, and the tab bodies are stubbed, so this test drives the shell
// itself: loading / error / 404 states, tab state synced to the URL ?tab=, and
// the Phase-2 tabs rendering ComingSoon. OverviewTab and LogsTab have their own
// dedicated tests.

const detailSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/sessions', () => ({
  useSessionDetail: (...args: unknown[]) => detailSpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));
vi.mock('./OverviewTab', () => ({
  OverviewTab: () => <div data-testid="overview-body">overview</div>,
}));
vi.mock('./LogsTab', () => ({
  LogsTab: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="logs-body">logs for {sessionId}</div>
  ),
}));
vi.mock('./TraceTab', () => ({
  TraceTab: () => <div data-testid="trace-body">trace</div>,
}));
vi.mock('./TopologyTab', () => ({
  TopologyTab: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="topology-body">topology for {sessionId}</div>
  ),
}));

import { SessionDetail } from './SessionDetail';

function result(over: Record<string, unknown>) {
  return { data: undefined, isPending: false, isError: false, error: null, ...over };
}

const OK = result({
  data: {
    session: { id: 's1', agent_name: 'nedi', model: 'm', status: 'completed' },
    turns: [],
    child_sessions: [],
  },
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/sessions/:id" element={<SessionDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  detailSpy.mockReset();
  liveSpy.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SessionDetail', () => {
  it('renders the loading state', () => {
    detailSpy.mockReturnValue(result({ isPending: true }));
    renderAt('/sessions/s1');
    expect(screen.getByText('Loading session…')).toBeInTheDocument();
  });

  it('renders the error state with the ApiError message', () => {
    detailSpy.mockReturnValue(
      result({ isError: true, error: new ApiError(500, 'INTERNAL_ERROR', 'db down') }),
    );
    renderAt('/sessions/s1');
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Failed to load session');
    expect(alert).toHaveTextContent('db down');
  });

  it('renders a clean not-found state for a 404', () => {
    detailSpy.mockReturnValue(
      result({ isError: true, error: new ApiError(404, 'NOT_FOUND', 'no such session') }),
    );
    renderAt('/sessions/missing');
    expect(screen.getByText('Session not found.')).toBeInTheDocument();
    // No tablist when not found.
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });

  it('defaults to the Overview tab', () => {
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByTestId('overview-body')).toBeInTheDocument();
  });

  it('honors ?tab=logs from the URL', () => {
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1?tab=logs');
    expect(screen.getByRole('tab', { name: 'Logs' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('logs-body')).toHaveTextContent('logs for s1');
  });

  it('falls back to Overview for an unknown ?tab= value', () => {
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1?tab=bogus');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('clicking a tab switches the panel', async () => {
    const user = userEvent.setup();
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    await user.click(screen.getByRole('tab', { name: 'Logs' }));
    expect(screen.getByTestId('logs-body')).toBeInTheDocument();
    expect(screen.queryByTestId('overview-body')).not.toBeInTheDocument();
  });

  it('renders the Trace tab body for ?tab=trace', async () => {
    const user = userEvent.setup();
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    await user.click(screen.getByRole('tab', { name: 'Trace' }));
    expect(screen.getByTestId('trace-body')).toBeInTheDocument();
  });

  it('renders the Topology tab body for ?tab=topology', async () => {
    const user = userEvent.setup();
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    await user.click(screen.getByRole('tab', { name: 'Topology' }));
    expect(screen.getByTestId('topology-body')).toHaveTextContent('topology for s1');
  });

  it('renders ComingSoon for the remaining Phase-2 tab (Timeline)', async () => {
    const user = userEvent.setup();
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    await user.click(screen.getByRole('tab', { name: 'Timeline' }));
    expect(screen.getByRole('heading', { name: 'Timeline' })).toBeInTheDocument();
  });

  it('subscribes to live updates scoped to the session id', () => {
    detailSpy.mockReturnValue(OK);
    renderAt('/sessions/s1');
    expect(liveSpy).toHaveBeenCalledWith({ session_id: 's1' });
  });
});
