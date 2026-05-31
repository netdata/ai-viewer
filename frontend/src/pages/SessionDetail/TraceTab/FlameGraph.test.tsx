import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, TurnDetail } from '../../../api/types';
import { buildOpTree, flattenTree, type TraceNode } from '../../../viz/trace';
import { FlameGraph } from './FlameGraph';

// Focused tests for the FlameGraph renderer: SVG path (one clickable cell per
// op, depth labels, keyboard activation) and Canvas path (canvas painted, click
// hit-tests the cell under the cursor). Geometry is covered in viz/trace.test.ts.

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

function treeFrom(ops: OpDetail[]): { roots: TraceNode[]; flat: TraceNode[] } {
  const turn: TurnDetail = {
    id: 't1',
    seq: 1,
    start_ts: 0,
    end_ts: null,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
  const roots = buildOpTree([turn]);
  return { roots, flat: flattenTree(roots) };
}

let ctxStub: Record<string, unknown>;

beforeEach(() => {
  ctxStub = {
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    strokeRect: vi.fn(),
    fillText: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    scale: vi.fn(),
  };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctxStub as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

// root [0,1000] contains llm [100,400] and tool [500,900]; tool contains inner.
const { roots, flat } = treeFrom([
  op({ id: 'root', kind: 'session', name: 'root-frame', start_ts: 0, end_ts: 1000, duration_us: 1000 }),
  op({ id: 'llm', kind: 'llm', name: 'generate', start_ts: 100, end_ts: 400, duration_us: 300 }),
  op({ id: 'tool', kind: 'tool', name: 'Bash', start_ts: 500, end_ts: 900, duration_us: 400 }),
  op({
    id: 'inner',
    kind: 'internal',
    name: 'sub',
    start_ts: 600,
    end_ts: 700,
    duration_us: 100,
    status: 'failed',
    error_class: 'E',
  }),
]);

describe('FlameGraph (SVG path)', () => {
  it('renders one accessible cell per op stacked by depth', () => {
    render(
      <FlameGraph nodes={flat} roots={roots} onSelect={vi.fn()} selectedId={null} useCanvas={false} />,
    );
    const fg = screen.getByRole('group', { name: /flame/i });
    const cells = within(fg).getAllByRole('button');
    expect(cells).toHaveLength(4);
    expect(within(fg).getByRole('button', { name: /root-frame/i })).toBeInTheDocument();
    expect(within(fg).getByRole('button', { name: /sub/i })).toHaveAccessibleName(/failed/i);
  });

  it('calls onSelect when a cell is clicked or keyboard-activated', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <FlameGraph nodes={flat} roots={roots} onSelect={onSelect} selectedId={null} useCanvas={false} />,
    );
    await user.click(screen.getByRole('button', { name: /Bash/i }));
    fireEvent.keyDown(screen.getByRole('button', { name: /generate/i }), { key: 'Enter' });
    expect(onSelect).toHaveBeenCalledTimes(2);
  });

  it('draws a point-event op as a tick (line) and a measured op as a cell (rect) — source-aware (P2#3)', () => {
    const { roots: r2, flat: f2 } = treeFrom([
      op({ id: 'root', kind: 'session', name: 'root-frame', start_ts: 0, end_ts: 1000, duration_us: 1000, parent_op_id: null }),
      // claude-code-style POINT-EVENT LLM child: the real persisted shape is
      // end_ts == start_ts AND duration_us === 0 (null is the still-RUNNING shape).
      op({ id: 'pt', kind: 'llm', name: 'point', start_ts: 200, end_ts: 200, duration_us: 0, parent_op_id: 'root' }),
    ]);
    render(<FlameGraph nodes={f2} roots={r2} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    expect(screen.getByRole('button', { name: /root-frame/i }).tagName.toLowerCase()).toBe('rect');
    expect(screen.getByRole('button', { name: /point/i }).tagName.toLowerCase()).toBe('line');
  });

  it('does NOT put a fabricated "0µs" in a point-event cell label (REAL shape: end_ts==start_ts, duration_us 0); a measured cell keeps its duration', () => {
    // GROUND TRUTH: a point-event op is persisted with end_ts === start_ts AND
    // duration_us === 0 (NOT null — null is the still-running shape). isInstantOp
    // is true for it, so its accessible name must use "instant", never a
    // fabricated "0µs"; a measured cell keeps its formatted duration.
    const { roots: r3, flat: f3 } = treeFrom([
      op({ id: 'root', kind: 'session', name: 'root-frame', start_ts: 1000, end_ts: 1400, duration_us: 400, parent_op_id: null }),
      op({ id: 'pt', kind: 'llm', name: 'point', start_ts: 1100, end_ts: 1100, duration_us: 0, parent_op_id: 'root' }),
    ]);
    render(<FlameGraph nodes={f3} roots={r3} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const point = screen.getByRole('button', { name: /point/i });
    expect(point).toHaveAccessibleName(/instant/i);
    expect(point).not.toHaveAccessibleName(/0µs/);
    // The measured root cell keeps its real duration in the label.
    expect(screen.getByRole('button', { name: /root-frame/i })).toHaveAccessibleName(/400µs/);
  });

  it('marks the selected cell with the selected style', () => {
    render(
      <FlameGraph nodes={flat} roots={roots} onSelect={vi.fn()} selectedId="tool" useCanvas={false} />,
    );
    expect(screen.getByRole('button', { name: /Bash/i }).getAttribute('class') ?? '').toMatch(
      /barSelected/,
    );
  });
});

describe('FlameGraph (Canvas path)', () => {
  it('paints a canvas instead of per-op SVG cells', () => {
    render(
      <FlameGraph nodes={flat} roots={roots} onSelect={vi.fn()} selectedId="root" useCanvas={true} />,
    );
    const fg = screen.getByRole('group', { name: /flame/i });
    expect(fg.querySelector('canvas')).not.toBeNull();
    expect(within(fg).queryAllByRole('button')).toHaveLength(0);
    expect(ctxStub.fillRect).toHaveBeenCalled();
  });

  it('hit-tests a click against the cell rectangles', () => {
    const onSelect = vi.fn();
    render(
      <FlameGraph nodes={flat} roots={roots} onSelect={onSelect} selectedId={null} useCanvas={true} />,
    );
    const canvas = screen
      .getByRole('group', { name: /flame/i })
      .querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      width: 0,
      height: 0,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    // Depth-0 root frame spans the full width at y∈[0,22): a click at (10,10)
    // lands on the root cell.
    fireEvent.click(canvas, { clientX: 10, clientY: 10 });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect((onSelect.mock.calls[0]?.[0] as TraceNode).op.id).toBe('root');
  });
});
