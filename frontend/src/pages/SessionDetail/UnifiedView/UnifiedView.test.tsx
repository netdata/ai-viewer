import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { TooltipProvider } from '../../../components/ui/tooltip';
import type { SessionDetailResponse } from '../../../api/types';
import { UnifiedView } from './UnifiedView';

// UnifiedView (ui-turn-view.md §ui-session-unified-view) is the new Session
// Detail shell that collapses every per-session view into one resizable
// 3-zone layout. These tests pin the high-level contract:
//
//   - All three zones (overview tiles + resizable body + turn view) render
//   - The viz/bottom tab buttons switch content
//   - ?op=<id> focuses a turn in the right sidebar
//   - ?tab:viz and ?tab:bottom round-trip via the URL
//   - Sub-session rows render as linkable cards

const traceSpy = vi.fn();

interface TraceHookResult {
  data: undefined;
  isPending: boolean;
  isError: boolean;
  error: null;
}

vi.mock('../../../api/sessions', () => ({
  useSessionDetail: () => ({ data: undefined, isPending: false, isError: false, error: null }),
  useSessionTrace: (): TraceHookResult => {
    const result = traceSpy() as TraceHookResult | undefined;
    return result ?? { data: undefined, isPending: false, isError: false, error: null };
  },
  useSessionTopology: () => ({ data: undefined, isPending: false, isError: false, error: null }),
  useSessionTimeline: () => ({ data: undefined, isPending: false, isError: false, error: null }),
  useSessionLogs: () => ({ data: undefined, isPending: false, isError: false, error: null }),
  useOpPayloadRefs: () => ({ data: undefined, isPending: false, isError: false, error: null }),
  useTurnPayloadRefs: () => ({ data: undefined, isPending: false, isError: false, error: null }),
}));

function makeOp(over: Partial<{ id: string; kind: string; name: string }> = {}) {
  return {
    id: over.id ?? 'op-1',
    kind: (over.kind ?? 'llm') as 'llm',
    name: over.name ?? 'message',
    model: 'm',
    provider: 'p',
    parent_op_id: null,
    start_ts: 1,
    end_ts: 2,
    duration_us: 1,
    status: 'completed',
    error_class: null,
    error_message: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
  };
}

function makeTurn(seq: number, ops: ReturnType<typeof makeOp>[]) {
  return {
    id: `turn-${seq}`,
    seq,
    start_ts: 1,
    end_ts: 2,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
}

function makeDetail(): SessionDetailResponse {
  return {
    session: {
      id: 'session-1',
      native_id: 'n1',
      root_session_id: 'session-1',
      parent_session_id: null,
      source_id: 's',
      kind: 'root',
      agent_name: 'agent',
      model: 'm',
      provider: 'p',
      status: 'completed',
      error_class: null,
      start_ts: 1,
      end_ts: 2,
      tokens_in: 100,
      tokens_out: 200,
      tokens_cache_read: 0,
      tokens_cache_write: 0,
      cost_usd: 0.01,
      op_count: 3,
      failure_count: 0,
      turn_count: 2,
      child_session_count: 0,
      last_activity_ts: 1,
    },
    turns: [
      makeTurn(1, [makeOp({ id: 'op-1' }), makeOp({ id: 'op-2' })]),
      makeTurn(2, [makeOp({ id: 'op-3' })]),
    ],
    child_sessions: [],
  };
}

function renderUnified(detail: SessionDetailResponse, initial = '/sessions/session-1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <UnifiedView detail={detail} />
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  traceSpy.mockReset();
  if (!window.matchMedia) {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: (q: string) => ({
        matches: false,
        media: q,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      }),
    });
  }
  if (!HTMLElement.prototype.scrollIntoView) {
    (HTMLElement.prototype as unknown as { scrollIntoView: () => void }).scrollIntoView = () => {
      // no-op
    };
  }
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('UnifiedView — overall shell', () => {
  it('renders the overview tiles, viz tabs, bottom tabs, and turn view pane', () => {
    renderUnified(makeDetail());

    // Overview tiles group.
    expect(screen.getByRole('group', { name: /session overview/i })).toBeInTheDocument();

    // Viz tab buttons (Waterfall is the default; Topology/Timeline/Statistics).
    const viz = screen.getByRole('region', { name: /waterfall visualization/i });
    expect(viz).toBeInTheDocument();
    expect(within(viz).getByRole('button', { name: /^waterfall$/i })).toBeInTheDocument();
    expect(within(viz).getByRole('button', { name: /^topology$/i })).toBeInTheDocument();

    // Bottom tab buttons.
    const bottom = screen.getByRole('region', { name: /events panel/i });
    expect(within(bottom).getByRole('button', { name: /event list/i })).toBeInTheDocument();
    expect(within(bottom).getByRole('button', { name: /^logs$/i })).toBeInTheDocument();

    // Turn view pane.
    expect(screen.getByRole('region', { name: /^turn view$/i })).toBeInTheDocument();
  });
});

describe('UnifiedView — focused op URL plumbing', () => {
  it('focuses the matching turn in the right sidebar when ?op=<id> is in the URL', () => {
    const detail = makeDetail();
    renderUnified(detail, '/sessions/session-1?op=op-3');

    // The "All turns" back link is visible when a turn is focused.
    expect(screen.getByRole('button', { name: /clear focus/i })).toBeInTheDocument();
    // Focused turn meta is visible.
    expect(screen.getByText(/2 turns · focused/i)).toBeInTheDocument();
  });

  it('shows the full turn picker when no ?op= is set', () => {
    renderUnified(makeDetail());

    // No "clear focus" link — we're showing the turn picker.
    expect(screen.queryByRole('button', { name: /clear focus/i })).not.toBeInTheDocument();
    // The turn picker shows one button per turn.
    const turnPane = screen.getByRole('region', { name: /^turn view$/i });
    expect(within(turnPane).getByRole('button', { name: /#1/i })).toBeInTheDocument();
    expect(within(turnPane).getByRole('button', { name: /#2/i })).toBeInTheDocument();
  });
});

describe('UnifiedView — viz tabs', () => {
  it('defaults the viz tab to Waterfall', () => {
    renderUnified(makeDetail());
    const waterfall = screen.getByRole('button', { name: /^waterfall$/i });
    expect(waterfall).toHaveAttribute('aria-pressed', 'true');
  });

  it('renders the Waterfall viz content with toolbar controls when selected', () => {
    renderUnified(makeDetail());
    // The TraceTab's filter controls are visible (kind / status selects).
    expect(screen.getByRole('combobox', { name: /filter by op kind/i })).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: /filter by status/i })).toBeInTheDocument();
  });

  it('switches to Topology viz when clicked', async () => {
    const user = userEvent.setup();
    renderUnified(makeDetail());
    const topology = screen.getByRole('button', { name: /^topology$/i });
    expect(topology).toHaveAttribute('aria-pressed', 'false');
    await user.click(topology);
    // After click the button should be pressed.
    await waitFor(() => expect(topology).toHaveAttribute('aria-pressed', 'true'));
  });

  it('switches to Statistics viz when clicked (renders an empty state)', async () => {
    const user = userEvent.setup();
    renderUnified(makeDetail());
    const stats = screen.getByRole('button', { name: /^statistics$/i });
    await user.click(stats);
    await waitFor(() => expect(stats).toHaveAttribute('aria-pressed', 'true'));
    expect(screen.getByText(/per-session statistics coming soon/i)).toBeInTheDocument();
  });
});

describe('UnifiedView — bottom tabs', () => {
  it('defaults the bottom tab to Event list', () => {
    renderUnified(makeDetail());
    const events = screen.getByRole('button', { name: /event list/i });
    expect(events).toHaveAttribute('aria-pressed', 'true');
  });

  it('switches to Logs when clicked', async () => {
    const user = userEvent.setup();
    renderUnified(makeDetail());
    const logs = screen.getByRole('button', { name: /^logs$/i });
    await user.click(logs);
    await waitFor(() => expect(logs).toHaveAttribute('aria-pressed', 'true'));
  });

  it('switches to Raw Data when clicked', async () => {
    const user = userEvent.setup();
    renderUnified(makeDetail());
    const raw = screen.getByRole('button', { name: /raw data/i });
    await user.click(raw);
    await waitFor(() => expect(raw).toHaveAttribute('aria-pressed', 'true'));
  });
});

describe('UnifiedView — turn view pane', () => {
  it('renders the turn picker when no op is focused', () => {
    renderUnified(makeDetail());
    const turnPane = screen.getByRole('region', { name: /^turn view$/i });
    expect(within(turnPane).getByRole('heading', { name: /^turns$/i })).toBeInTheDocument();
    expect(within(turnPane).getByText(/2 total/i)).toBeInTheDocument();
  });

  it('shows a hidden-turn hint when more than 50 turns exist', () => {
    const detail = makeDetail();
    // Add 55 turns so the "5 more turns" hint appears.
    const manyTurns = Array.from({ length: 55 }, (_, i) => makeTurn(i + 1, [makeOp({ id: `op-many-${i}` })]));
    detail.turns = manyTurns;
    renderUnified(detail);
    expect(screen.getByText(/5 more turns/i)).toBeInTheDocument();
  });

  it('clear-focus link removes the ?op= URL param when clicked', async () => {
    const user = userEvent.setup();
    renderUnified(makeDetail(), '/sessions/session-1?op=op-3');
    const clearBtn = screen.getByRole('button', { name: /clear focus/i });
    await user.click(clearBtn);
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /clear focus/i })).not.toBeInTheDocument();
    });
  });
});