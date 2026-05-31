import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
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
    op({ id: 'root', kind: 'session', name: 'root-span', start_ts: 0, end_ts: 1000, duration_us: 1000, parent_op_id: null }),
    op({ id: 'llm-1', kind: 'llm', name: 'gen', start_ts: 100, end_ts: 400, duration_us: 300, parent_op_id: 'root' }),
    op({ id: 'tool-1', kind: 'tool', name: 'Bash', start_ts: 500, end_ts: 900, duration_us: 400, parent_op_id: 'root' }),
    op({
      id: 'tool-fail',
      kind: 'tool',
      name: 'Grep',
      start_ts: 910,
      end_ts: 980,
      duration_us: 70,
      status: 'failed',
      error_class: 'ToolError',
      parent_op_id: 'root',
    }),
  ]),
]);

// A two-turn session for the By-turn view: each turn rolls up to ONE bar.
const TWO_TURNS = detail([
  turn(1, [
    op({ id: 't1-a', kind: 'llm', name: 'gen-1', start_ts: 0, end_ts: 300, duration_us: 300, parent_op_id: null }),
    op({ id: 't1-b', kind: 'tool', name: 'Read', start_ts: 320, end_ts: 600, duration_us: 280, parent_op_id: null }),
  ]),
  turn(2, [
    op({ id: 't2-a', kind: 'tool', name: 'Write', start_ts: 700, end_ts: 1200, duration_us: 500, parent_op_id: null }),
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
      // The Detailed Canvas clips the time-track region (fixed gutter under X
      // zoom/pan), so the stub must carry rect + clip.
      rect: vi.fn(),
      clip: vi.fn(),
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
    const wf = screen.getByRole('group', { name: /waterfall/i });
    // One interactive span element per op (root, llm-1, tool-1, tool-fail).
    const spans = within(wf).getAllByRole('button');
    expect(spans).toHaveLength(4);
  });

  it('defaults the waterfall to the Detailed mode and offers a By-turn toggle (decision #6)', () => {
    render(<TraceTab detail={SAMPLE} />);
    // Within the waterfall view, a Detailed|By-turn sub-toggle exists, Detailed
    // selected by default.
    expect(screen.getByRole('radio', { name: /detailed/i })).toBeChecked();
    expect(screen.getByRole('radio', { name: /by.?turn/i })).not.toBeChecked();
    // Detailed shows the per-op waterfall (one bar per op).
    const wf = screen.getByRole('group', { name: /waterfall/i });
    expect(within(wf).getAllByRole('button')).toHaveLength(4);
  });

  it('switches the waterfall to By-turn: one aggregated bar per turn (decision #6)', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={TWO_TURNS} />);
    await user.click(screen.getByRole('radio', { name: /by.?turn/i }));
    expect(screen.getByRole('radio', { name: /by.?turn/i })).toBeChecked();
    // The by-turn view rolls each turn up to ONE bar — 2 turns → 2 turn bars.
    const byTurn = screen.getByRole('group', { name: /by.?turn view/i });
    const turnBars = within(byTurn).getAllByRole('button');
    expect(turnBars).toHaveLength(2);
    expect(turnBars[0]).toHaveAccessibleName(/turn 1 — 2 ops/i);
    expect(turnBars[1]).toHaveAccessibleName(/turn 2 — 1 op/i);
  });

  it('expands a turn into its ops when its By-turn bar is clicked (decision #6)', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={TWO_TURNS} />);
    await user.click(screen.getByRole('radio', { name: /by.?turn/i }));
    const byTurn = screen.getByRole('group', { name: /by.?turn view/i });
    // The expanded-turn region (the per-op waterfall) is not present until a
    // turn is expanded. (The op names appear in the always-on event list, so we
    // assert on the expand region, not document-wide text.)
    expect(screen.queryByRole('region', { name: /turn 1 operations/i })).not.toBeInTheDocument();
    // Click turn 1's bar (the first turn bar).
    const bars = within(byTurn).getAllByRole('button');
    await user.click(bars[0] as HTMLElement);
    // The turn's individual ops are now visible in the expanded region.
    const region = screen.getByRole('region', { name: /turn 1 operations/i });
    expect(within(region).getByText('gen-1')).toBeInTheDocument();
    expect(within(region).getByText('Read')).toBeInTheDocument();
    // Turn 2's op is NOT in turn 1's expanded region.
    expect(within(region).queryByText('Write')).not.toBeInTheDocument();
  });

  it('hides the By-turn sub-toggle when the flame view is active', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    await user.click(screen.getByRole('radio', { name: /flame/i }));
    // Detailed/By-turn only apply to the waterfall; they are gone under flame.
    expect(screen.queryByRole('radio', { name: /by.?turn/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('radio', { name: /detailed/i })).not.toBeInTheDocument();
  });

  it('toggles to the flame-graph view', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    await user.click(screen.getByRole('radio', { name: /flame/i }));
    expect(screen.getByRole('radio', { name: /flame/i })).toBeChecked();
    expect(screen.getByRole('group', { name: /flame/i })).toBeInTheDocument();
    // Waterfall is no longer rendered.
    expect(screen.queryByRole('group', { name: /waterfall/i })).not.toBeInTheDocument();
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
    expect(screen.queryByRole('group', { name: /waterfall/i })).not.toBeInTheDocument();
  });

  it('opens the drawer with the op fields when a waterfall span is clicked', async () => {
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    await user.click(within(wf).getByRole('button', { name: /Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/Bash/i);
  });

  it('passes the op variant (Trace tab is the only source with op metrics): the drawer shows the Payloads section', async () => {
    // The Trace tab opens the drawer with { kind: 'op', op } — the only variant
    // that renders the op's cost/tokens + a Payloads section. (The Timeline/Topology
    // tabs pass span/node variants which omit those — ui-pages.md §Span detail drawer.)
    const user = userEvent.setup();
    render(<TraceTab detail={SAMPLE} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    await user.click(within(wf).getByRole('button', { name: /Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(/^Payloads$/)).toBeInTheDocument();
    expect(within(dialog).getByText(/no payloads for this op/i)).toBeInTheDocument();
    // The op variant does NOT show the Timeline-tab "open in Trace tab" note.
    expect(within(dialog).queryByText(/open this op in the Trace tab/i)).not.toBeInTheDocument();
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

  it('opens the drawer from a waterfall span via the keyboard (Enter on a focused bar)', () => {
    // AC#5 keyboard path: a focusable SVG bar opens the drawer with Enter — a
    // keyboard / screen-reader user never needs the pointer.
    render(<TraceTab detail={SAMPLE} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    const bar = within(wf).getByRole('button', { name: /Bash/i });
    bar.focus();
    expect(bar).toHaveFocus();
    fireEvent.keyDown(bar, { key: 'Enter' });
    expect(screen.getByRole('dialog')).toHaveAccessibleName(/Bash/i);
  });

  it('has no axe violations (component-level a11y for the Trace tab)', async () => {
    const { container } = render(<TraceTab detail={SAMPLE} />);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
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
    const wf = screen.getByRole('group', { name: /waterfall/i });
    expect(wf.querySelector('canvas')).not.toBeNull();
    // The event list is windowed: far fewer than 1200 rows are mounted.
    const list = screen.getByRole('table', { name: /event list/i });
    const bodyRows = within(list).getAllByRole('row').length - 1; // minus header
    expect(bodyRows).toBeLessThan(1200);
    expect(bodyRows).toBeGreaterThan(0);
  });
});
