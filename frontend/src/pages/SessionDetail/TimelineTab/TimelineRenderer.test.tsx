import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { layoutTimeline, type TimelineLaneInput } from '../../../viz/timeline';
import { installMatchMedia } from '../../../test/matchMedia';
import { TimelineRenderer } from './TimelineRenderer';

// Focused tests for the TimelineRenderer: the SVG path (one clickable row per
// span, closed-span bars vs instant ticks vs compaction breakpoints, keyboard
// activation, selected styling) and the Canvas path (canvas painted, click
// hit-tests the span under the cursor). Layout geometry is covered in
// viz/timeline.test.ts; this pins the painting + interaction wiring, including
// that a PLAIN wheel pans (preventDefault) while SHIFT+wheel is left to d3-zoom.

function laneFixture(): TimelineLaneInput[] {
  return [
    {
      key: 'session:root',
      label: 'root',
      spans: [
        { id: 'llm', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 400, status: 'completed' },
        { id: 'compact', kind: 'compaction', name: 'compaction', start_ts: 500, end_ts: null, status: 'completed' },
        { id: 'live', kind: 'tool', name: 'run', start_ts: 600, end_ts: null, status: 'running' },
      ],
    },
  ];
}

const WIDTH = 1000;
const T_START = 100;
const T_END = 900;
const LAYOUT = layoutTimeline(laneFixture(), {
  width: WIDTH,
  laneHeight: 40,
  tStart: T_START,
  tEnd: T_END,
});
const TRACK_HEIGHT = 22 + LAYOUT.lanes.length * 40;

let ctxStub: Record<string, unknown>;

beforeEach(() => {
  ctxStub = {
    clearRect: vi.fn(),
    fillRect: vi.fn(),
    strokeRect: vi.fn(),
    beginPath: vi.fn(),
    arc: vi.fn(),
    moveTo: vi.fn(),
    lineTo: vi.fn(),
    stroke: vi.fn(),
    fill: vi.fn(),
    save: vi.fn(),
    restore: vi.fn(),
    scale: vi.fn(),
    translate: vi.fn(),
    transform: vi.fn(),
    setLineDash: vi.fn(),
  };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctxStub as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
  // installMatchMedia stubs a global; undo it so it never leaks into a later test.
  vi.unstubAllGlobals();
});

/** layoutOf lays out a lane set at the fixture's scale (helper for the fade tests). */
function layoutOf(lanes: TimelineLaneInput[]) {
  return layoutTimeline(lanes, { width: WIDTH, laneHeight: 40, tStart: T_START, tEnd: T_END });
}

describe('TimelineRenderer (SVG path)', () => {
  it('renders one accessible row per span', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(within(track).getAllByRole('button')).toHaveLength(3);
  });

  it('draws a closed span as a bar <rect>, an instant as a <line>, a compaction as a breakpoint <line>', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    const bar = within(track).getByRole('button', { name: /chat/i });
    // The closed span paints a filled bar rect (non-transparent fill).
    expect(bar.querySelector('rect[fill]:not([fill="transparent"])')).not.toBeNull();
    const instant = within(track).getByRole('button', { name: /run —/i });
    expect(instant.querySelector('line')).not.toBeNull();
    const breakpoint = within(track).getByRole('button', { name: /compaction/i });
    expect(breakpoint.querySelector('line')).not.toBeNull();
  });

  it('calls onSpanClick when a span is clicked or keyboard-activated', async () => {
    const user = userEvent.setup();
    const onSpanClick = vi.fn();
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={onSpanClick}
        useCanvas={false}
      />,
    );
    await user.click(screen.getByRole('button', { name: /chat/i }));
    fireEvent.keyDown(screen.getByRole('button', { name: /run —/i }), { key: 'Enter' });
    expect(onSpanClick).toHaveBeenCalledTimes(2);
  });

  it('marks the selected span with the selected style', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId="llm"
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    expect(
      screen.getByRole('button', { name: /chat/i }).getAttribute('class') ?? '',
    ).toMatch(/spanSelected/);
  });

  it('renders the lane labels', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    expect(screen.getByText('root')).toBeInTheDocument();
  });

  it('pans (translates) on a plain wheel and zooms the TIME (X) axis only on a shift+wheel', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const svg = screen
      .getByRole('group', { name: /session timeline/i })
      .querySelector('svg') as SVGSVGElement;
    const g = svg.querySelector('g') as SVGGElement;

    // The <g> transform is an X-only matrix(a,0,0,d,e,f): a=X-scale, d=Y-scale,
    // e/f=translate. parseMatrix pulls those out so we can assert the time axis (X)
    // scales while lane height (Y) never does (codex P2#4).
    const parseMatrix = (): number[] => {
      const m = /matrix\(([^)]+)\)/.exec(g.getAttribute('transform') ?? '');
      return (m?.[1] ?? '').split(',').map(Number);
    };

    // A plain wheel is consumed by the pan handler (preventDefault) and applies a
    // pure TRANSLATION (no scale change on either axis).
    const plain = new WheelEvent('wheel', { deltaX: 40, deltaY: 0, bubbles: true, cancelable: true });
    svg.dispatchEvent(plain);
    expect(plain.defaultPrevented).toBe(true);
    const [aPan, , , dPan, ePan] = parseMatrix();
    expect(aPan).toBe(1); // X scale 1 (no zoom)
    expect(dPan).toBe(1); // Y scale 1
    expect(ePan).toBe(-40); // panned left by 40

    // A shift+wheel is handled by d3-zoom (NOT the pan handler) and scales the
    // TIME axis (X) only — Y stays 1 so lane height is constant. Dispatch via the
    // SVG element with a pointer location so d3-zoom computes a focal point.
    const shifted = new WheelEvent('wheel', {
      deltaY: -40,
      shiftKey: true,
      clientX: 200,
      clientY: 100,
      bubbles: true,
      cancelable: true,
    });
    svg.dispatchEvent(shifted);
    const [aZoom, , , dZoom] = parseMatrix();
    expect(aZoom).toBeGreaterThan(1); // X (time) scaled up
    expect(dZoom).toBe(1); // Y (lane height) UNCHANGED — the codex P2#4 invariant
  });

  it('does NOT fade any span on the first render (initial load is not an append)', () => {
    installMatchMedia(false); // motion allowed
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    for (const span of within(track).getAllByRole('button')) {
      expect(span.getAttribute('class') ?? '').not.toMatch(/fadeIn/);
    }
  });

  it('fades ONLY the newly-appended span when a re-render adds one (SSE append)', () => {
    installMatchMedia(false); // motion allowed
    const { rerender } = render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    // A live session_changed refetch adds a new closed span to the root lane.
    const grown = layoutOf([
      {
        key: 'session:root',
        label: 'root',
        spans: [
          { id: 'llm', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 400, status: 'completed' },
          { id: 'compact', kind: 'compaction', name: 'compaction', start_ts: 500, end_ts: null, status: 'completed' },
          { id: 'live', kind: 'tool', name: 'run', start_ts: 600, end_ts: null, status: 'running' },
          { id: 'fresh', kind: 'tool', name: 'append', start_ts: 700, end_ts: 800, status: 'completed' },
        ],
      },
    ]);
    rerender(
      <TimelineRenderer
        lanes={grown.lanes}
        spans={grown.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(within(track).getByRole('button', { name: /append/i }).getAttribute('class') ?? '').toMatch(/fadeIn/);
    expect(within(track).getByRole('button', { name: /chat/i }).getAttribute('class') ?? '').not.toMatch(/fadeIn/);
  });

  it('does NOT fade the new span when prefers-reduced-motion is set', () => {
    installMatchMedia(true); // reduced motion requested
    const { rerender } = render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const grown = layoutOf([
      {
        key: 'session:root',
        label: 'root',
        spans: [
          { id: 'llm', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 400, status: 'completed' },
          { id: 'compact', kind: 'compaction', name: 'compaction', start_ts: 500, end_ts: null, status: 'completed' },
          { id: 'live', kind: 'tool', name: 'run', start_ts: 600, end_ts: null, status: 'running' },
          { id: 'fresh', kind: 'tool', name: 'append', start_ts: 700, end_ts: 800, status: 'completed' },
        ],
      },
    ]);
    rerender(
      <TimelineRenderer
        lanes={grown.lanes}
        spans={grown.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(within(track).getByRole('button', { name: /append/i }).getAttribute('class') ?? '').not.toMatch(/fadeIn/);
  });
});

describe('TimelineRenderer (Canvas path)', () => {
  it('paints a canvas instead of per-span SVG rows', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={true}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(track.querySelector('canvas')).not.toBeNull();
    // The closed span fills a bar; the compaction breakpoint dashes a line.
    expect(ctxStub.fillRect).toHaveBeenCalled();
    expect(ctxStub.setLineDash).toHaveBeenCalled();
  });

  it('exposes a keyboard-operable fallback list so a span is reachable without the canvas (a11y)', async () => {
    const user = userEvent.setup();
    const onSpanClick = vi.fn();
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={onSpanClick}
        useCanvas={true}
      />,
    );
    // The canvas itself is a single non-focusable image; the fallback list mirrors
    // every span as a real focusable button (one accessible name per span).
    const fallback = screen.getByRole('list', { name: /timeline spans/i });
    const buttons = within(fallback).getAllByRole('button');
    expect(buttons).toHaveLength(3);
    // The same span identity/label the SVG bars carry.
    const chat = within(fallback).getByRole('button', { name: /chat/i });
    await user.click(chat);
    expect(onSpanClick).toHaveBeenCalledTimes(1);
    expect(onSpanClick.mock.calls[0]?.[0]).toMatchObject({ span: { id: 'llm' } });
  });

  it('culls the keyboard-fallback list to the viewport (no DOM node per span at scale)', () => {
    // P2#4: the Canvas fallback must NOT emit one <button> per total span (a
    // thousands-span timeline would flood the DOM). It mirrors only the spans the
    // Canvas paints — the viewport-culled set. Six lanes but a viewport tall enough
    // for ~one lane: lower lanes are off-screen, so their spans are excluded.
    const sixLanes: TimelineLaneInput[] = Array.from({ length: 6 }, (_, i) => ({
      key: `session:lane${i}`,
      label: `lane${i}`,
      spans: [
        { id: `op${i}`, kind: 'tool', name: `op${i}`, start_ts: 100, end_ts: 300, status: 'completed' },
      ],
    }));
    const layout = layoutTimeline(sixLanes, { width: WIDTH, laneHeight: 40, tStart: T_START, tEnd: T_END });
    const shortHeight = 22 + 40; // axis + ~one lane visible
    render(
      <TimelineRenderer
        lanes={layout.lanes}
        spans={layout.spans}
        width={WIDTH}
        height={shortHeight}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
        useCanvas={true}
      />,
    );
    const fallback = screen.getByRole('list', { name: /timeline spans/i });
    const buttons = within(fallback).getAllByRole('button');
    // Fewer than the 6 total spans (only the on-screen lanes are listed), and the
    // top lane's span is among them.
    expect(buttons.length).toBeLessThan(6);
    expect(buttons.length).toBeGreaterThan(0);
    expect(within(fallback).queryByRole('button', { name: /op0/i })).not.toBeNull();
    // A far-off-screen lane's span is NOT in the DOM fallback.
    expect(within(fallback).queryByRole('button', { name: /op5/i })).toBeNull();
  });

  it('hit-tests a click against the span under the cursor', () => {
    const onSpanClick = vi.fn();
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={onSpanClick}
        useCanvas={true}
      />,
    );
    const canvas = screen
      .getByRole('group', { name: /session timeline/i })
      .querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0,
      left: 0,
      right: WIDTH,
      bottom: TRACK_HEIGHT,
      width: WIDTH,
      height: TRACK_HEIGHT,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    // The llm bar [x≈0..375] sits on lane 0: y in [22+0+6 .. +28]. A click at
    // (100, 34) lands on it (identity transform).
    fireEvent.click(canvas, { clientX: 100, clientY: 34 });
    expect(onSpanClick).toHaveBeenCalledTimes(1);
    expect(onSpanClick.mock.calls[0]?.[0]).toMatchObject({ span: { id: 'llm' } });
  });

  it('ignores a click on empty canvas space', () => {
    const onSpanClick = vi.fn();
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={onSpanClick}
        useCanvas={true}
      />,
    );
    const canvas = screen
      .getByRole('group', { name: /session timeline/i })
      .querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0,
      left: 0,
      right: WIDTH,
      bottom: TRACK_HEIGHT,
      width: WIDTH,
      height: TRACK_HEIGHT,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    // Below every lane.
    fireEvent.click(canvas, { clientX: 100, clientY: TRACK_HEIGHT - 1 });
    expect(onSpanClick).not.toHaveBeenCalled();
  });
});

describe('TimelineRenderer (auto path selection)', () => {
  it('uses the SVG path below the span ceiling by default', () => {
    render(
      <TimelineRenderer
        lanes={LAYOUT.lanes}
        spans={LAYOUT.spans}
        width={WIDTH}
        height={TRACK_HEIGHT}
        tStart={T_START}
        tEnd={T_END}
        selectedId={null}
        onSpanClick={vi.fn()}
      />,
    );
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(track.querySelector('canvas')).toBeNull();
    expect(within(track).getAllByRole('button')).toHaveLength(3);
  });
});
