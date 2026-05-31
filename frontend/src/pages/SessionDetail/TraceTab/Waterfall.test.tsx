import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, TurnDetail } from '../../../api/types';
import { buildOpTree, flattenTree, type TraceNode } from '../../../viz/trace';
import { Waterfall } from './Waterfall';

// Focused tests for the Waterfall renderer: the SVG path (one clickable bar per
// op, keyboard activation, failed-op outline) and the Canvas path (canvas
// painted, click hit-tests the row under the cursor, scroll re-renders). The
// pure geometry is covered in viz/trace.test.ts; this drives the React + paint
// glue. jsdom has no Canvas 2D, so getContext is stubbed.

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

function nodesFrom(ops: OpDetail[]): TraceNode[] {
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
  return flattenTree(buildOpTree([turn]));
}

let ctxStub: Record<string, unknown>;

beforeEach(() => {
  ctxStub = {
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
  };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctxStub as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('Waterfall (SVG path)', () => {
  const nodes = nodesFrom([
    op({ id: 'a', name: 'alpha', start_ts: 0, end_ts: 400, duration_us: 400 }),
    op({ id: 'b', name: 'beta', kind: 'llm', start_ts: 100, end_ts: 200, duration_us: 100 }),
    op({
      id: 'c',
      name: 'gamma',
      start_ts: 500,
      end_ts: 560,
      duration_us: 60,
      status: 'failed',
      error_class: 'X',
    }),
  ]);

  it('renders one accessible bar per op with kind/duration/status in the label', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    const bars = within(wf).getAllByRole('button');
    expect(bars).toHaveLength(3);
    expect(within(wf).getByRole('button', { name: /alpha/i })).toHaveAccessibleName(/tool/i);
    expect(within(wf).getByRole('button', { name: /gamma/i })).toHaveAccessibleName(/failed/i);
  });

  it('calls onSelect when a bar is clicked', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Waterfall nodes={nodes} onSelect={onSelect} selectedId={null} useCanvas={false} />);
    await user.click(screen.getByRole('button', { name: /beta/i }));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect((onSelect.mock.calls[0]?.[0] as TraceNode).op.id).toBe('b');
  });

  it('activates a bar with Enter and Space (keyboard)', () => {
    const onSelect = vi.fn();
    render(<Waterfall nodes={nodes} onSelect={onSelect} selectedId={null} useCanvas={false} />);
    const bar = screen.getByRole('button', { name: /alpha/i });
    fireEvent.keyDown(bar, { key: 'Enter' });
    fireEvent.keyDown(bar, { key: ' ' });
    expect(onSelect).toHaveBeenCalledTimes(2);
  });

  it('marks the selected op bar with the selected style', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId="b" useCanvas={false} />);
    const bar = screen.getByRole('button', { name: /beta/i });
    expect(bar.getAttribute('class') ?? '').toMatch(/barSelected/);
  });
});

describe('Waterfall (Canvas path)', () => {
  const many = Array.from({ length: 30 }, (_, i) =>
    op({ id: `op-${i}`, name: `t${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5 }),
  );
  const nodes = nodesFrom(many);

  it('paints a canvas instead of per-op SVG bars', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    expect(wf.querySelector('canvas')).not.toBeNull();
    // No SVG button bars in the canvas path.
    expect(within(wf).queryAllByRole('button')).toHaveLength(0);
    expect(ctxStub.fillRect).toHaveBeenCalled();
  });

  it('selects the row under the cursor on a canvas click (Y hit-test)', () => {
    const onSelect = vi.fn();
    render(<Waterfall nodes={nodes} onSelect={onSelect} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    const canvas = wf.querySelector('canvas') as HTMLCanvasElement;
    // Row height is 26; a click at y≈70 maps to row index 2.
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
    fireEvent.click(wf, { clientY: 70, clientX: 300 });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect((onSelect.mock.calls[0]?.[0] as TraceNode).op.id).toBe('op-2');
  });

  it('re-renders the canvas on scroll (culling re-runs)', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    (ctxStub.fillRect as ReturnType<typeof vi.fn>).mockClear();
    fireEvent.scroll(wf, { target: { scrollTop: 200 } });
    expect(ctxStub.fillRect).toHaveBeenCalled();
  });

  it('outlines a failed op and highlights the selected op on the canvas', () => {
    const withFail = nodesFrom([
      op({ id: 'a', name: 'a', start_ts: 0, end_ts: 5, duration_us: 5 }),
      op({
        id: 'bad',
        name: 'bad',
        start_ts: 10,
        end_ts: 20,
        duration_us: 10,
        status: 'failed',
        error_class: 'E',
      }),
    ]);
    render(<Waterfall nodes={withFail} onSelect={vi.fn()} selectedId="bad" useCanvas={true} />);
    // Both the failed-op outline and the selection highlight call strokeRect.
    expect(ctxStub.strokeRect).toHaveBeenCalled();
  });

  it('ignores a canvas click that lands below the last row', () => {
    const onSelect = vi.fn();
    render(<Waterfall nodes={nodes} onSelect={onSelect} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('img', { name: /waterfall/i });
    const canvas = wf.querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0,
      toJSON: () => ({}),
    });
    // 30 rows × 26px = 780px tall; a click at y=10000 is past the end.
    fireEvent.click(wf, { clientY: 10000, clientX: 300 });
    expect(onSelect).not.toHaveBeenCalled();
  });
});
