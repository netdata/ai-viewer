import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { TooltipProvider } from '../../components/ui/tooltip';
import { ApiError } from '../../api/client';

// SessionDetail is the unified Session Detail shell (SOW-0088 chunk 4):
// single view that replaces the old Overview/Trace/Topology/Timeline/Logs/Raw
// tabs. The URL no longer drives ?tab= (the unified view is always rendered);
// it drives ?tab:viz and ?tab:bottom instead, plus ?op=<id> for the focused
// turn. This test verifies the page-level loading/error/404 states and the
// unified shell renders with all its zones present.

const detailSpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/sessions', () => ({
  useSessionDetail: (...args: unknown[]) => detailSpy(...args) as unknown,
  useSessionTrace: () => ({ data: undefined, isPending: false, isError: false, error: null }),
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));

const sessionId = 'test-session-id';

function makeSessionDetail(over: Partial<{
  id: string;
  agent_name: string;
  model: string;
  status: string;
  start_ts: number;
  end_ts: number | null;
  parent_session_id: string | null;
  last_activity_ts: number | null;
  tokens_cache_read: number;
}> = {}) {
  return {
    id: sessionId,
    native_id: 'native-id',
    root_session_id: sessionId,
    parent_session_id: null,
    source_id: 'test:source',
    kind: 'root' as const,
    agent_name: 'test-agent',
    model: 'test-model',
    provider: 'anthropic',
    status: 'completed',
    error_class: null,
    start_ts: 1_700_000_000_000_000,
    end_ts: 1_700_000_005_000_000,
    tokens_in: 100,
    tokens_out: 200,
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0.01,
    op_count: 0,
    failure_count: 0,
    ...over,
  };
}

const detailFixture = {
  session: makeSessionDetail(),
  turns: [],
  child_sessions: [],
};

function renderPage(initialEntry = `/sessions/${sessionId}`) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <TooltipProvider>
        <Routes>
          <Route path="/sessions/:id" element={<SessionDetail />} />
        </Routes>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

import { SessionDetail } from './SessionDetail';

beforeEach(() => {
  detailSpy.mockReset();
  liveSpy.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('SessionDetail — loading & error states', () => {
  it('renders a LoadingState while the session is pending', () => {
    detailSpy.mockReturnValue({ data: undefined, isPending: true, isError: false, error: null });
    renderPage();
    expect(screen.getByText(/loading session/i)).toBeInTheDocument();
  });

  it('renders an ErrorState when the session fails to load', () => {
    detailSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new ApiError(500, 'INTERNAL_ERROR', 'boom'),
    });
    renderPage();
    expect(screen.getByText(/failed to load session/i)).toBeInTheDocument();
  });

  it('renders a not-found state when the session is 404', () => {
    detailSpy.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new ApiError(404, 'NOT_FOUND', 'gone'),
    });
    renderPage();
    expect(screen.getByText(/session not found/i)).toBeInTheDocument();
  });

  it('subscribes to live updates for the open session', () => {
    detailSpy.mockReturnValue({ data: detailFixture, isPending: false, isError: false, error: null });
    renderPage();
    expect(liveSpy).toHaveBeenCalled();
    const args = liveSpy.mock.calls[0]?.[0] as { session_id: string } | undefined;
    expect(args?.session_id).toBe(sessionId);
  });
});

describe('SessionDetail — unified shell', () => {
  beforeEach(() => {
    detailSpy.mockReturnValue({ data: detailFixture, isPending: false, isError: false, error: null });
  });

  it('renders the unified view with all zones (header, tiles, resizable body)', () => {
    renderPage();
    // Header zone: agent name + id + pin button.
    expect(screen.getByRole('heading', { name: /test-agent/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /pin this session/i })).toBeInTheDocument();

    // Overview tiles: 6 tiles by role group.
    const tilesGroup = screen.getByRole('group', { name: /session overview/i });
    expect(tilesGroup).toBeInTheDocument();

    // Resizable body: each panel has role=region.
    const regions = screen.getAllByRole('region');
    expect(regions.length).toBeGreaterThanOrEqual(3); // viz, bottom, turn-view
  });

  it('renders the breadcrumb', () => {
    renderPage();
    expect(screen.getByRole('navigation')).toBeInTheDocument();
  });

  it('ignores legacy ?tab= query params (unified view is always rendered)', () => {
    renderPage(`/sessions/${sessionId}?tab=trace`);
    // No "Trace tab" header — the unified view's viz pane has a "Waterfall"
    // tab instead. The legacy tab system is gone.
    expect(screen.queryByRole('tab', { name: /^trace$/i })).not.toBeInTheDocument();
    // But the Waterfall viz tab IS present.
    expect(screen.getByRole('button', { name: /^waterfall$/i, pressed: true })).toBeInTheDocument();
  });
});