import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, TurnDetail } from '../../../api/types';
import type { TraceNode } from '../../../viz/trace';
import { ByTurnWaterfall } from './ByTurnWaterfall';

// Focused tests for the By-turn waterfall (decision #6): one aggregated bar per
// turn, expand/collapse on click into the turn's own per-op waterfall. The pure
// aggregation is covered in viz/trace.test.ts (buildTurnRollup); this drives the
// React + interaction glue. jsdom has no Canvas 2D, so getContext is stubbed for
// the expanded sub-waterfall's Canvas path.

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
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0,
    bytes_in: 0,
    bytes_out: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
    ...over,
  };
}

function turn(seq: number, ops: OpDetail[], status = 'completed'): TurnDetail {
  return {
    id: `t${seq}`,
    seq,
    start_ts: ops.length > 0 ? Math.min(...ops.map((o) => o.start_ts)) : 0,
    end_ts: null,
    status,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
}

const TURNS: TurnDetail[] = [
  turn(1, [
    op({ id: 't1-a', kind: 'llm', name: 'gen-1', start_ts: 0, end_ts: 300, duration_us: 300, parent_op_id: null }),
    op({ id: 't1-b', kind: 'tool', name: 'Read', start_ts: 320, end_ts: 600, duration_us: 280, parent_op_id: null }),
  ]),
  turn(2, [
    op({ id: 't2-a', kind: 'tool', name: 'Write', start_ts: 700, end_ts: 1200, duration_us: 500, parent_op_id: null }),
  ]),
];

beforeEach(() => {
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

describe('ByTurnWaterfall', () => {
  it('renders one bar per turn with an op-count/duration accessible name', () => {
    render(<ByTurnWaterfall turns={TURNS} onSelect={vi.fn()} selectedId={null} />);
    const view = screen.getByRole('group', { name: /by-turn view/i });
    const bars = within(view).getAllByRole('button');
    expect(bars).toHaveLength(2);
    expect(bars[0]).toHaveAccessibleName(/turn 1 — 2 ops/i);
    expect(bars[1]).toHaveAccessibleName(/turn 2 — 1 op/i);
  });

  it('expands a turn into its ops and collapses on a second click', async () => {
    const user = userEvent.setup();
    render(<ByTurnWaterfall turns={TURNS} onSelect={vi.fn()} selectedId={null} />);
    const view = screen.getByRole('group', { name: /by-turn view/i });
    const turn1Bar = within(view).getAllByRole('button')[0] as HTMLElement;
    expect(turn1Bar).toHaveAttribute('aria-expanded', 'false');

    await user.click(turn1Bar);
    expect(turn1Bar).toHaveAttribute('aria-expanded', 'true');
    const region = screen.getByRole('region', { name: /turn 1 operations/i });
    expect(within(region).getByText('gen-1')).toBeInTheDocument();
    expect(within(region).getByText('Read')).toBeInTheDocument();

    // A second click collapses it.
    await user.click(turn1Bar);
    expect(turn1Bar).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('region', { name: /turn 1 operations/i })).not.toBeInTheDocument();
  });

  it('opens the drawer via onSelect when an expanded op is clicked', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<ByTurnWaterfall turns={TURNS} onSelect={onSelect} selectedId={null} />);
    const view = screen.getByRole('group', { name: /by-turn view/i });
    await user.click(within(view).getAllByRole('button')[0] as HTMLElement);
    const region = screen.getByRole('region', { name: /turn 1 operations/i });
    await user.click(within(region).getByRole('button', { name: /Read/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect((onSelect.mock.calls[0]?.[0] as TraceNode).op.id).toBe('t1-b');
  });

  it('keeps the turn bars as SVG even for a session with many turns (no canvas at the turn level)', () => {
    const many: TurnDetail[] = [];
    for (let i = 0; i < 600; i++) {
      many.push(turn(i + 1, [op({ id: `o-${i}`, name: `t${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5, parent_op_id: null })]));
    }
    render(<ByTurnWaterfall turns={many} onSelect={vi.fn()} selectedId={null} />);
    const view = screen.getByRole('group', { name: /by-turn view/i });
    // The turn bars are SVG <rect> buttons (one per turn), not a canvas.
    expect(view.querySelector('canvas')).toBeNull();
    expect(within(view).getAllByRole('button')).toHaveLength(600);
  });
});
