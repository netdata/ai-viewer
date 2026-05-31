import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import type { TimelineResponse } from '../../../api/types';

// TimelineTab is the per-session video-editor-style timeline (ui-pages.md
// §/sessions/:id #4). useTimeline is MOCKED (no network), so these RTL tests
// drive the tab: lanes + spans render, a compaction op renders as a full-height
// breakpoint, a null-end op renders as an instant marker, and a span click opens
// the shared SpanDetailDrawer. The pure layout geometry is covered in
// viz/timeline.test.ts; the SVG/Canvas paint + pan/zoom in TimelineRenderer.test.tsx.

const timelineSpy = vi.fn();

vi.mock('../../../api/sessions', () => ({
  useTimeline: (...args: unknown[]) => timelineSpy(...args) as unknown,
}));

import { TimelineTab } from './TimelineTab';

function result(over: Record<string, unknown>) {
  return { data: undefined, isPending: false, isError: false, error: null, ...over };
}

// A fixture: a root lane (an llm bar, a tool bar, and a compaction op) and a
// child lane (a running null-end op — an instant marker).
const TIMELINE: TimelineResponse = {
  lanes: [
    {
      key: 'session:root',
      label: 'nedi (root)',
      spans: [
        { id: 'llm-1', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 400, status: 'completed' },
        { id: 'tool-1', kind: 'tool', name: 'Bash', start_ts: 500, end_ts: 900, status: 'completed' },
        { id: 'compact-1', kind: 'compaction', name: 'compaction', start_ts: 950, end_ts: null, status: 'completed' },
      ],
    },
    {
      key: 'session:child',
      label: 'worker',
      spans: [
        { id: 'live-1', kind: 'tool', name: 'run', start_ts: 700, end_ts: null, status: 'running' },
      ],
    },
  ],
  t_start: 100,
  t_end: 1000,
};

beforeEach(() => {
  timelineSpy.mockReset();
  timelineSpy.mockReturnValue(result({ data: TIMELINE }));
  // Canvas 2D is not implemented in jsdom; stub a no-op context so any Canvas
  // branch can mount without throwing (the small fixture stays on the SVG path).
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    {
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
      setLineDash: vi.fn(),
      set fillStyle(_v: string) {},
      set strokeStyle(_v: string) {},
      set lineWidth(_v: number) {},
    } as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('TimelineTab', () => {
  it('fetches the timeline for the session id', () => {
    render(<TimelineTab sessionId="s1" />);
    expect(timelineSpy).toHaveBeenCalledWith('s1');
  });

  it('renders one accessible row per span across the lanes', () => {
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    const spans = within(track).getAllByRole('button');
    // 3 root-lane spans + 1 child-lane span = 4.
    expect(spans).toHaveLength(4);
    expect(within(track).getByRole('button', { name: /chat/i })).toBeInTheDocument();
    expect(within(track).getByRole('button', { name: /Bash/i })).toBeInTheDocument();
  });

  it('shows the lane labels (one lane per session, root + children stacked)', () => {
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    expect(within(track).getByText(/nedi \(root\)/i)).toBeInTheDocument();
    expect(within(track).getByText('worker')).toBeInTheDocument();
  });

  it('renders a compaction op as a full-height breakpoint (dashed vertical rule)', () => {
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    const breakpoint = within(track).getByRole('button', { name: /compaction/i });
    // The breakpoint draws a <line> (the vertical rule), not a bar <rect> fill.
    expect(breakpoint.querySelector('line')).not.toBeNull();
  });

  it('renders a null-end (running) op as an instant marker (a tick line, not a bar)', () => {
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    const instant = within(track).getByRole('button', { name: /run —/i });
    expect(instant.querySelector('line')).not.toBeNull();
    expect(instant.querySelector('rect[fill]:not([fill="transparent"])')).toBeNull();
  });

  it('shows the pan/zoom hint (shift+wheel zoom, wheel pan)', () => {
    render(<TimelineTab sessionId="s1" />);
    expect(screen.getByText(/shift \+ wheel to zoom, wheel to pan/i)).toBeInTheDocument();
  });

  it('opens the shared detail drawer when a span is clicked', async () => {
    const user = userEvent.setup();
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    await user.click(within(track).getByRole('button', { name: /Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/Bash/i);
  });

  it('shows an em dash (never "0µs") for a point-event span (end_ts === start_ts) and a real duration for a closed one', async () => {
    // A point event (end_ts === start_ts) has NO measured duration (ui-pages.md
    // §Trace/Timeline): the derived duration must be null, so the drawer renders
    // "—", never a fabricated "0µs". A strictly-closed span (end_ts > start_ts)
    // still shows its real formatted duration.
    const user = userEvent.setup();
    timelineSpy.mockReturnValue(
      result({
        data: {
          lanes: [
            {
              key: 'session:root',
              label: 'root',
              spans: [
                // Point event: no duration. Must render "—" in the drawer.
                { id: 'point-1', kind: 'llm', name: 'instant', start_ts: 500, end_ts: 500, status: 'completed' },
                // Closed span (400µs wide): a real duration.
                { id: 'closed-1', kind: 'tool', name: 'measured', start_ts: 100, end_ts: 500, status: 'completed' },
              ],
            },
          ],
          t_start: 100,
          t_end: 500,
        },
      }),
    );
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });

    // Point event → "—", never "0µs".
    await user.click(within(track).getByRole('button', { name: /instant/i }));
    let dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText('0µs')).not.toBeInTheDocument();
    expect(within(dialog).queryByText('0')).not.toBeInTheDocument();
    const pointDuration = within(dialog).getByText('Duration').closest('div');
    expect(pointDuration).not.toBeNull();
    expect(within(pointDuration as HTMLElement).getByText('—')).toBeInTheDocument();
    await user.keyboard('{Escape}');

    // Closed span → its real duration (400µs).
    await user.click(within(track).getByRole('button', { name: /measured/i }));
    dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('400µs')).toBeInTheDocument();
  });

  it('passes the span variant: the drawer shows lane/span fields only and never fabricates op metrics as zero', async () => {
    // A timeline span carries no op metrics, so the drawer must NOT print
    // $0.00 / 0 tokens / "No payloads" (ui-pages.md §Span detail drawer); it
    // directs the operator to the Trace tab for that detail instead.
    const user = userEvent.setup();
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    await user.click(within(track).getByRole('button', { name: /Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText(/open this op in the Trace tab/i)).toBeInTheDocument();
    expect(within(dialog).queryByText('$0.00')).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/tokens in/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/no payloads/i)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/^Payloads$/)).not.toBeInTheDocument();
  });

  it('closes the drawer on Escape', async () => {
    const user = userEvent.setup();
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    await user.click(within(track).getByRole('button', { name: /Bash/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('opens the drawer from a span via the keyboard (Enter on a focused span)', () => {
    // AC#5 keyboard path: a focusable SVG span opens the drawer with Enter.
    render(<TimelineTab sessionId="s1" />);
    const track = screen.getByRole('group', { name: /session timeline/i });
    const span = within(track).getByRole('button', { name: /Bash/i });
    span.focus();
    expect(span).toHaveFocus();
    fireEvent.keyDown(span, { key: 'Enter' });
    expect(screen.getByRole('dialog')).toHaveAccessibleName(/Bash/i);
  });

  it('has no axe violations (component-level a11y for the Timeline tab)', async () => {
    const { container } = render(<TimelineTab sessionId="s1" />);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('shows the loading state while the query is pending', () => {
    timelineSpy.mockReturnValue(result({ data: undefined, isPending: true }));
    render(<TimelineTab sessionId="s1" />);
    expect(screen.getByRole('status')).toHaveTextContent(/loading timeline/i);
  });

  it('surfaces a fetch error in an alert (no silent failure)', () => {
    timelineSpy.mockReturnValue(result({ isError: true, error: new Error('timeline boom') }));
    render(<TimelineTab sessionId="s1" />);
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent(/failed to load timeline/i);
    expect(alert).toHaveTextContent(/timeline boom/i);
  });

  it('shows an empty state when the timeline has no spans', () => {
    timelineSpy.mockReturnValue(result({ data: { lanes: [], t_start: 0, t_end: 0 } }));
    render(<TimelineTab sessionId="s1" />);
    expect(screen.getByText(/no spans recorded/i)).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /session timeline/i })).not.toBeInTheDocument();
  });
});
