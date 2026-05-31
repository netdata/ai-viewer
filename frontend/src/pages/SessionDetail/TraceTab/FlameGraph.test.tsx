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
    const fg = screen.getByRole('img', { name: /flame/i });
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
    const fg = screen.getByRole('img', { name: /flame/i });
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
      .getByRole('img', { name: /flame/i })
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
