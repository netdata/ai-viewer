import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { OpDetail, TurnDetail } from '../../../api/types';
import { buildOpTree, flattenTree, type TraceNode } from '../../../viz/trace';
import { installMatchMedia } from '../../../test/matchMedia';
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

function nodesFrom(ops: OpDetail[]): TraceNode[] {
  const turn: TurnDetail = {
    id: 't1',
    seq: 1,
    start_ts: 0,
    end_ts: null,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    tokens_cache_write: 0,
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
    // The Detailed Canvas clips the time-track region (gutter stays fixed under
    // X zoom/pan), so the 2D stub must carry rect + clip.
    rect: vi.fn(),
    clip: vi.fn(),
  };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctxStub as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  // installMatchMedia stubs a global; undo it so the matchMedia stub never leaks
  // into a later test that expects the default jsdom (absent) matchMedia.
  vi.unstubAllGlobals();
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
    const wf = screen.getByRole('group', { name: /waterfall/i });
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

  it('draws a point-event op as a tick (line) and a measured op as a bar (rect) — source-aware (P2#3)', () => {
    const mixed = nodesFrom([
      op({ id: 'm', name: 'measured', start_ts: 0, end_ts: 400, duration_us: 400 }),
      // claude-code-style POINT EVENT: the real persisted shape is end_ts ==
      // start_ts AND duration_us === 0 (null is the still-RUNNING shape).
      op({ id: 'p', name: 'point', kind: 'llm', start_ts: 500, end_ts: 500, duration_us: 0 }),
    ]);
    render(<Waterfall nodes={mixed} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const measured = screen.getByRole('button', { name: /measured/i });
    const point = screen.getByRole('button', { name: /point/i });
    // The measured op is a rect bar; the point event is a vertical tick (line),
    // never a zero-width rect.
    expect(measured.tagName.toLowerCase()).toBe('rect');
    expect(point.tagName.toLowerCase()).toBe('line');
  });

  it('does NOT put a fabricated "0µs" in a point-event bar label (REAL shape: end_ts==start_ts, duration_us 0); a measured op keeps its duration', () => {
    // GROUND TRUTH: a point-event op is persisted with end_ts === start_ts AND
    // duration_us === 0 (NOT null — null is the still-running shape). isInstantOp
    // is true for it, so the bar's accessible name must use "instant", never a
    // fabricated "0µs"; a measured op keeps its formatted duration.
    const mixed = nodesFrom([
      op({ id: 'm', name: 'measured', start_ts: 1000, end_ts: 1400, duration_us: 400 }),
      op({ id: 'p', name: 'point', kind: 'llm', start_ts: 1000, end_ts: 1000, duration_us: 0 }),
    ]);
    render(<Waterfall nodes={mixed} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const point = screen.getByRole('button', { name: /point/i });
    expect(point).toHaveAccessibleName(/instant/i);
    expect(point).not.toHaveAccessibleName(/0µs/);
    // The measured op keeps its real duration in the label.
    expect(screen.getByRole('button', { name: /measured/i })).toHaveAccessibleName(/400µs/);
  });

  it('does NOT fade any bar on the first render (initial load is not an append)', () => {
    installMatchMedia(false); // motion allowed
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    for (const bar of within(wf).getAllByRole('button')) {
      expect(bar.getAttribute('class') ?? '').not.toMatch(/fadeIn/);
    }
  });

  it('fades ONLY the newly-appended bar when a re-render adds an op (SSE append)', () => {
    installMatchMedia(false); // motion allowed
    const { rerender } = render(
      <Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />,
    );
    // A live session_changed refetch grows the trace by one op.
    const grown = nodesFrom([
      op({ id: 'a', name: 'alpha', start_ts: 0, end_ts: 400, duration_us: 400 }),
      op({ id: 'b', name: 'beta', kind: 'llm', start_ts: 100, end_ts: 200, duration_us: 100 }),
      op({ id: 'c', name: 'gamma', start_ts: 500, end_ts: 560, duration_us: 60, status: 'failed', error_class: 'X' }),
      op({ id: 'd', name: 'delta', start_ts: 600, end_ts: 650, duration_us: 50 }),
    ]);
    rerender(<Waterfall nodes={grown} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    // The new op fades; the pre-existing ops do not.
    expect((within(wf).getByRole('button', { name: /delta/i }).getAttribute('class') ?? '')).toMatch(/fadeIn/);
    expect((within(wf).getByRole('button', { name: /alpha/i }).getAttribute('class') ?? '')).not.toMatch(/fadeIn/);
  });

  it('does NOT fade the new bar when prefers-reduced-motion is set', () => {
    installMatchMedia(true); // reduced motion requested
    const { rerender } = render(
      <Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />,
    );
    const grown = nodesFrom([
      op({ id: 'a', name: 'alpha', start_ts: 0, end_ts: 400, duration_us: 400 }),
      op({ id: 'b', name: 'beta', kind: 'llm', start_ts: 100, end_ts: 200, duration_us: 100 }),
      op({ id: 'c', name: 'gamma', start_ts: 500, end_ts: 560, duration_us: 60, status: 'failed', error_class: 'X' }),
      op({ id: 'd', name: 'delta', start_ts: 600, end_ts: 650, duration_us: 50 }),
    ]);
    rerender(<Waterfall nodes={grown} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    expect((within(wf).getByRole('button', { name: /delta/i }).getAttribute('class') ?? '')).not.toMatch(/fadeIn/);
  });
});

describe('Waterfall (Canvas path)', () => {
  const many = Array.from({ length: 30 }, (_, i) =>
    op({ id: `op-${i}`, name: `t${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5 }),
  );
  const nodes = nodesFrom(many);

  it('paints a canvas instead of per-op SVG bars', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    expect(wf.querySelector('canvas')).not.toBeNull();
    // No SVG button bars in the canvas path.
    expect(within(wf).queryAllByRole('button')).toHaveLength(0);
    expect(ctxStub.fillRect).toHaveBeenCalled();
  });

  it('selects the row under the cursor on a canvas click (Y hit-test)', () => {
    const onSelect = vi.fn();
    render(<Waterfall nodes={nodes} onSelect={onSelect} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
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
    const wf = screen.getByRole('group', { name: /waterfall/i });
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
    const wf = screen.getByRole('group', { name: /waterfall/i });
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

// X-only zoom/pan on the Detailed waterfall (ui-pages.md §Trace big-session
// navigation): SHIFT+wheel zooms the TIME (X) axis only, drag pans X, a PLAIN
// wheel scrolls the rows (native scroller, not the time track). Mirrors
// TimelineRenderer.test.tsx's jsdom zoom-driving technique. The track <g> carries
// matrix(k,0,0,1,tx,0): the X scale (a) zooms while the Y scale (d) stays 1 (row
// height constant), and the left gutter labels + turn rules never move.

describe('Waterfall (SVG path) — X-only time zoom/pan', () => {
  const nodes = nodesFrom([
    op({ id: 'a', name: 'alpha', start_ts: 0, end_ts: 400, duration_us: 400 }),
    op({ id: 'b', name: 'beta', kind: 'llm', start_ts: 100, end_ts: 200, duration_us: 100 }),
  ]);

  /** trackGroup is the innermost <g> d3-zoom transforms (inside the clip group). */
  function trackGroup(svg: SVGSVGElement): SVGGElement {
    const clipGroup = svg.querySelector('g[clip-path]') as SVGGElement;
    // <g clip-path> → <g translate(LABEL_WIDTH,0)> → <g ref={trackRef}> (deepest).
    const groups = clipGroup.querySelectorAll('g');
    return groups[1] as SVGGElement; // [0]=translate wrapper, [1]=the track group
  }

  /** parseMatrix pulls the 6 matrix(a,b,c,d,e,f) numbers from a group's transform. */
  function parseMatrix(g: SVGGElement): number[] {
    const m = /matrix\(([^)]+)\)/.exec(g.getAttribute('transform') ?? '');
    return (m?.[1] ?? '').split(',').map(Number);
  }

  it('zooms the TIME (X) axis only on a shift+wheel (k≠1, Y scale stays 1) and keeps the gutter labels fixed', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    const svg = wf.querySelector('svg') as SVGSVGElement;
    const track = trackGroup(svg);
    // A gutter row label's x before the zoom (it must not move under X zoom).
    const label = within(wf).getByText('alpha') as unknown as SVGTextElement;
    const labelXBefore = label.getAttribute('x');

    const shifted = new WheelEvent('wheel', {
      deltaY: -40,
      shiftKey: true,
      clientX: 300,
      clientY: 80,
      bubbles: true,
      cancelable: true,
    });
    svg.dispatchEvent(shifted);

    const [a, b, c, d, , f] = parseMatrix(track);
    expect(a).toBeGreaterThan(1); // X (time) scaled up
    expect(b).toBe(0);
    expect(c).toBe(0);
    expect(d).toBe(1); // Y scale UNCHANGED (row height constant)
    expect(f).toBe(0); // track never translates in Y (vertical is the scroller)
    // The gutter label did not move (it lives outside the transformed track).
    expect(label.getAttribute('x')).toBe(labelXBefore);
  });

  it('pans the TIME (X) axis on a primary-button drag (tx changes, Y untouched)', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const svg = screen
      .getByRole('group', { name: /waterfall/i })
      .querySelector('svg') as SVGSVGElement;
    const track = trackGroup(svg);

    // jsdom synthetic mousedown has a null view; the shared zoomEventFilter rejects
    // a view-null mousedown, so set a view (mirrors TimelineRenderer.test.tsx).
    const down = new MouseEvent('mousedown', { button: 0, clientX: 400, clientY: 80, bubbles: true });
    Object.defineProperty(down, 'view', { value: window, configurable: true });
    svg.dispatchEvent(down);
    const move = new MouseEvent('mousemove', { clientX: 340, clientY: 80, bubbles: true });
    Object.defineProperty(move, 'view', { value: window, configurable: true });
    window.dispatchEvent(move);
    const up = new MouseEvent('mouseup', { clientX: 340, clientY: 80, bubbles: true });
    Object.defineProperty(up, 'view', { value: window, configurable: true });
    window.dispatchEvent(up);

    const [a, , , d, e, f] = parseMatrix(track);
    expect(a).toBe(1); // no zoom on a pure pan
    expect(d).toBe(1); // Y scale unchanged
    expect(e).toBe(-60); // panned left by the drag delta (400 → 340)
    expect(f).toBe(0); // never translates in Y
  });

  it('does NOT move the time track on a plain wheel (left to the native row scroller)', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={false} />);
    const svg = screen
      .getByRole('group', { name: /waterfall/i })
      .querySelector('svg') as SVGSVGElement;
    const track = trackGroup(svg);
    const before = track.getAttribute('transform');

    // A plain wheel: plainWheelPan:false → the filter rejects it WITHOUT
    // preventDefault, so it reaches the native scroller and the track is untouched.
    const plain = new WheelEvent('wheel', { deltaY: 120, bubbles: true, cancelable: true });
    svg.dispatchEvent(plain);
    expect(plain.defaultPrevented).toBe(false);
    expect(track.getAttribute('transform')).toBe(before);
  });
});

describe('Waterfall (Canvas path) — X-only time zoom/pan', () => {
  const many = Array.from({ length: 30 }, (_, i) =>
    op({ id: `op-${i}`, name: `t${i}`, start_ts: i * 10, end_ts: i * 10 + 5, duration_us: 5 }),
  );
  const nodes = nodesFrom(many);

  it('repaints with a scaled X track on a shift+wheel (k≠1) while the plain-wheel scroll still works', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    const canvas = wf.querySelector('canvas') as HTMLCanvasElement;

    // Capture the bar x-positions painted at identity (fillRect x args). jsdom has
    // no real layout, so we assert via the stub: a zoom must re-run the paint and
    // the painted bar x must shift (k≠1 changes screenX = LABEL_WIDTH + x*k + tx).
    const fillRect = ctxStub.fillRect as ReturnType<typeof vi.fn>;
    const firstBarXBefore = (fillRect.mock.calls[0]?.[0] as number) ?? null;
    fillRect.mockClear();

    const shifted = new WheelEvent('wheel', {
      deltaY: -60,
      shiftKey: true,
      clientX: 400,
      clientY: 40,
      bubbles: true,
      cancelable: true,
    });
    // The shift+wheel updates the zoom transform state; wrap in act() so the
    // resulting state update + repaint effect flush before we assert.
    act(() => {
      canvas.dispatchEvent(shifted);
    });

    // The zoom re-ran the paint …
    expect(fillRect).toHaveBeenCalled();
    // … and at least one painted bar x reflects the zoom (differs from identity).
    const firstBarXAfter = fillRect.mock.calls[0]?.[0] as number;
    expect(typeof firstBarXAfter).toBe('number');
    if (firstBarXBefore !== null) {
      expect(firstBarXAfter).not.toBe(firstBarXBefore);
    }
  });

  it('still scrolls the rows on a plain wheel (plain wheel is not intercepted)', () => {
    render(<Waterfall nodes={nodes} onSelect={vi.fn()} selectedId={null} useCanvas={true} />);
    const wf = screen.getByRole('group', { name: /waterfall/i });
    const fillRect = ctxStub.fillRect as ReturnType<typeof vi.fn>;
    fillRect.mockClear();
    // The existing native scroll path (onScroll) still repaints with the culled
    // rows — plainWheelPan:false leaves the wheel to the scroller.
    fireEvent.scroll(wf, { target: { scrollTop: 200 } });
    expect(fillRect).toHaveBeenCalled();
  });
});
