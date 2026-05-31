import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, SessionDetailResponse, TurnDetail } from '../../../api/types';
import { TraceTab } from './TraceTab';

// TraceTab is the centerpiece APM view (ui-pages.md §/sessions/:id #3). It builds
// the waterfall (default) + flame (toggle) + a virtualized event list from the
// SAME op tree out of the session-detail response (turns→ops), and a click on
// any span/row opens the shared SpanDetailDrawer. These RTL tests drive the
// component against an in-memory detail object (no network).

function op(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op',
    kind: 'tool',
    name: 'n',
    model: '',
    provider: '',
    start_ts: 0,
    end_ts: 100,
    duration_us: 100,
    status: 'completed',
    error_class: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
    ...over,
  };
}

function turn(seq: number, ops: OpDetail[]): TurnDetail {
  return {
    id: `t${seq}`,
    seq,
    start_ts: ops.length > 0 ? Math.min(...ops.map((o) => o.start_ts)) : 0,
    end_ts: null,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
}

function detail(turns: TurnDetail[]): SessionDetailResponse {
  return {
    session: {
      id: 's1',
      native_id: 'n1',
      root_session_id: 's1',
      parent_session_id: null,
      source_id: 'src',
      kind: 'root',
      agent_name: 'nedi',
      model: 'claude-opus-4-7',
      provider: 'anthropic',
      status: 'completed',
      error_class: null,
      start_ts: 0,
      end_ts: 1000,
      tokens_in: 0,
      tokens_out: 0,
      tokens_cache_read: 0,
      tokens_cache_write: 0,
      cost_usd: 0,
      turn_count: turns.length,
      op_count: turns.reduce((a, t) => a + t.ops.length, 0),
      failure_count: 0,
      child_session_count: 0,
    },
    turns,
    child_sessions: [],
  };
}

const SAMPLE = detail([
  turn(1, [
    op({ id: 'root', kind: 'session', name: 'root-span', start_ts: 0, end_ts: 1000, duration_us: 1000 }),
    op({ id: 'llm-1', kind: 'llm', name: 'gen', start_ts: 100, end_ts: 400, duration_us: 300 }),
    op({ id: 'tool-1', kind: 'tool', name: 'Bash', start_ts: 500, end_ts: 900, duration_us: 400 }),
    op({
      id: 'tool-fail',
      kind: 'tool',
      name: 'Grep',
      start_ts: 910,
      end_ts: 980,
      duration_us: 70,
      status: 'failed',
      error_class: 'ToolError',
    }),
  ]),
]);

beforeEach(() => {
  // Canvas 2D is not implemented in jsdom; stub a no-op context so the (large
  // tree) Canvas branch can mount without throwing during these tests.
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    {
      clearRect: vi.fn(),
      fillRect: vi.fn(),
      strokeRect: vi.fn(),
      beginPath: vi.fn(),
      moveTo: vi.fn(),
      lineTo: vi.fn(),
      stroke: vi.fn(),
      fillText: vi.fn(),
      save: vi.fn(),
      restore: vi.fn(),
      scale: vi.fn(),
      set fillStyle(_v: string) {},
      set strokeStyle(_v: string) {},
      set font(_v: string) {},
      set textBaseline(_v: string) {},
      set lineWidth(_v: number) {},
    } as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('TraceTab', () => {
  it('renders the waterfall by default with a row per op', () => {
    render(<TraceTab detail={SAMPLE} />);
    // The view toggle defaults to Waterfall.
    expect(screen.getByRole('radio', { name: /waterfall/i })).toBeChecked();
    // The waterfall is an SVG-based figure for a small session.
    const wf = screen.getByRole('img', { name: /waterfall/i });
    // One interactive span element per op (root, llm-1, tool-1, tool-fail).
    const spans = within(wf).getAllByRole('button');
    expect(spans).toHaveLength(4);
  });

  it('toggles to the flame-graph view', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    await user.click(screen.getByRole('radio', { name: /flame/i }));
    expect(screen.getByRole('radio', { name: /flame/i })).toBeChecked();
    expect(screen.getByRole('img', { name: /flame/i })).toBeInTheDocument();
    // Waterfall is no longer rendered.
    expect(screen.queryByRole('img', { name: /waterfall/i })).not.toBeInTheDocument();
  });

  it('always renders the event list of every op in order', () => {
    render(<TraceTab detail={SAMPLE} />);
    const list = screen.getByRole('table', { name: /event list/i });
    const rows = within(list).getAllByRole('row');
    // header + 4 ops
    expect(rows).toHaveLength(5);
    expect(within(list).getByText('root-span')).toBeInTheDocument();
    expect(within(list).getByText('Bash')).toBeInTheDocument();
    expect(within(list).getByText('Grep')).toBeInTheDocument();
  });

  it('shows the empty state when the session has no ops', () => {
    render(<TraceTab detail={detail([turn(1, [])])} />);
    expect(screen.getByText(/no operations/i)).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: /waterfall/i })).not.toBeInTheDocument();
  });

  it('opens the drawer with the op fields when a waterfall span is clicked', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    await user.click(within(wf).getByRole('button', { name: /Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/Bash/i);
  });

  it('opens the drawer when an event-list row is clicked', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    const list = screen.getByRole('table', { name: /event list/i });
    await user.click(within(list).getByRole('button', { name: /Grep/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/Grep/i);
    // The failed op surfaces its error class in the drawer.
    expect(within(dialog).getByText('ToolError')).toBeInTheDocument();
  });

  it('closes the drawer on Escape', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    const list = screen.getByRole('table', { name: /event list/i });
    await user.click(within(list).getByRole('button', { name: /Bash/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('renders a large trace via the Canvas path without blowing up', () => {
    // Above the SVG ceiling the renderer must switch to Canvas + culling and
    // stay mounted. We assert the canvas branch renders and the event list
    // still windows all ops (only a slice is in the DOM at once).
    const many: OpDetail[] = [];
    for (let i = 0; i < 1200; i++) {
      many.push(
        op({ id: `op-${i}`, kind: 'tool', name: `tool-${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5 }),
      );
    }
    render(<TraceTab detail={detail([turn(1, many)])} />);
    // Canvas branch is used (an accessible image with a canvas inside).
    const wf = screen.getByRole('img', { name: /waterfall/i });
    expect(wf.querySelector('canvas')).not.toBeNull();
    // The event list is windowed: far fewer than 1200 rows are mounted.
    const list = screen.getByRole('table', { name: /event list/i });
    const bodyRows = within(list).getAllByRole('row').length - 1; // minus header
    expect(bodyRows).toBeLessThan(1200);
    expect(bodyRows).toBeGreaterThan(0);
  });
});
