import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import type { TopologyResponse } from '../../../api/types';

// TopologyTab is the per-session actor-graph view (ui-pages.md §/sessions/:id #2).
// useSessionTopology is MOCKED (no network) and the Vite ?worker import is stubbed
// (jsdom has no Worker; the worker path is never hit below the 100-node
// threshold), so these RTL tests drive the tab: the size-metric selector and the
// 3-mode layout toggle render and switch (the metric drives the query arg), and a
// node click opens the shared SpanDetailDrawer. The pure layout geometry is
// covered in viz/topology.test.ts.

const topologySpy = vi.fn();

vi.mock('../../../api/sessions', () => ({
  useSessionTopology: (...args: unknown[]) => topologySpy(...args) as unknown,
}));

// The ?worker import is a Vite virtual module with no jsdom equivalent; stub it
// as a no-op Worker class so importing the tab never touches a real worker.
vi.mock('../../../viz/forceWorker?worker', () => ({
  default: class {
    onmessage: ((e: MessageEvent) => void) | null = null;
    postMessage(): void {}
    terminate(): void {}
  },
}));

import { TopologyTab } from './TopologyTab';

function result(over: Record<string, unknown>) {
  return { data: undefined, isPending: false, isError: false, error: null, ...over };
}

// A small fixture graph: root agent → tool (clean) and root agent → failing tool.
const GRAPH: TopologyResponse = {
  nodes: [
    { id: 'agent:root', kind: 'agent', label: 'nedi (root)', size_metric: 100, failure_ratio: 0 },
    { id: 'tool:shell.Bash', kind: 'tool', label: 'shell.Bash', size_metric: 60, failure_ratio: 0 },
    { id: 'tool:fs.Grep', kind: 'tool', label: 'fs.Grep', size_metric: 20, failure_ratio: 0.5 },
  ],
  edges: [
    { source: 'agent:root', target: 'tool:shell.Bash', calls: 12, total_us: 3400000 },
    { source: 'agent:root', target: 'tool:fs.Grep', calls: 4, total_us: 120000 },
  ],
  max_size_metric: 100,
};

beforeEach(() => {
  topologySpy.mockReset();
  topologySpy.mockReturnValue(result({ data: GRAPH }));
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
      fillText: vi.fn(),
      save: vi.fn(),
      restore: vi.fn(),
      scale: vi.fn(),
      translate: vi.fn(),
      set fillStyle(_v: string) {},
      set strokeStyle(_v: string) {},
      set lineWidth(_v: number) {},
      set font(_v: string) {},
      set textAlign(_v: string) {},
      set textBaseline(_v: string) {},
    } as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('TopologyTab', () => {
  it('defaults to the cost size-metric and renders the 3 layout modes', () => {
    render(<TopologyTab sessionId="s1" />);
    // Size metric selector defaults to "cost".
    const select = screen.getByRole('combobox', { name: /size by/i });
    expect(select).toHaveValue('cost');
    // The query was asked for the cost metric.
    expect(topologySpy).toHaveBeenCalledWith('s1', 'cost');
    // Three layout-mode radios, seeded force selected by default.
    expect(screen.getByRole('radio', { name: /seeded force/i })).toBeChecked();
    expect(screen.getByRole('radio', { name: /plain force/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /hierarchical/i })).toBeInTheDocument();
  });

  it('renders one accessible node per actor on the SVG path', () => {
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('group', { name: /topology graph/i });
    const nodes = within(graph).getAllByRole('button');
    expect(nodes).toHaveLength(3);
    expect(within(graph).getByRole('button', { name: /nedi \(root\)/i })).toBeInTheDocument();
    // The failing tool surfaces its failure ratio in its accessible name.
    expect(within(graph).getByRole('button', { name: /fs\.Grep/i })).toHaveAccessibleName(
      /failures/i,
    );
  });

  it('switches the size metric and refetches with the new metric', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    await user.selectOptions(screen.getByRole('combobox', { name: /size by/i }), 'tokens');
    expect(screen.getByRole('combobox', { name: /size by/i })).toHaveValue('tokens');
    expect(topologySpy).toHaveBeenCalledWith('s1', 'tokens');
  });

  it('switches the layout mode via the toggle', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    await user.click(screen.getByRole('radio', { name: /hierarchical/i }));
    expect(screen.getByRole('radio', { name: /hierarchical/i })).toBeChecked();
    expect(screen.getByRole('radio', { name: /seeded force/i })).not.toBeChecked();
    // The graph is still rendered after the mode switch.
    expect(screen.getByRole('group', { name: /topology graph/i })).toBeInTheDocument();
  });

  it('opens the shared detail drawer when a node is clicked', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('group', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /shell\.Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/shell\.Bash/i);
  });

  it('closes the drawer on Escape', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('group', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /shell\.Bash/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('opens the drawer from a node via the keyboard (Enter on a focused node)', () => {
    // AC#5 keyboard path: a focusable SVG node opens the drawer with Enter.
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('group', { name: /topology graph/i });
    const node = within(graph).getByRole('button', { name: /shell\.Bash/i });
    node.focus();
    expect(node).toHaveFocus();
    fireEvent.keyDown(node, { key: 'Enter' });
    expect(screen.getByRole('dialog')).toHaveAccessibleName(/shell\.Bash/i);
  });

  it('shows the failure percentage in a failing node label (color is not the only signal)', () => {
    // AC#5 color-not-sole-signal: the failing tool node carries its failure % as
    // on-graph text, not just a red fill.
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('group', { name: /topology graph/i });
    const failing = within(graph).getByRole('button', { name: /fs\.Grep/i });
    expect(failing.textContent ?? '').toMatch(/50\.0%/);
  });

  it('has no axe violations (component-level a11y for the Topology tab)', async () => {
    const { container } = render(<TopologyTab sessionId="s1" />);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('freezes the layout (button toggles its pressed state)', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    const freeze = screen.getByRole('button', { name: /freeze layout/i });
    expect(freeze).toHaveAttribute('aria-pressed', 'false');
    await user.click(freeze);
    const unfreeze = screen.getByRole('button', { name: /unfreeze layout/i });
    expect(unfreeze).toHaveAttribute('aria-pressed', 'true');
  });

  it('disables the freeze button for the static hierarchical layout', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    await user.click(screen.getByRole('radio', { name: /hierarchical/i }));
    expect(screen.getByRole('button', { name: /freeze layout/i })).toBeDisabled();
  });

  it('keeps node DATA live while frozen: a metric change re-applies fresh radius + failure_ratio at the pinned position', async () => {
    const user = userEvent.setup();
    // Same node ids, but the "tokens" metric makes fs.Grep the largest node and
    // changes failure ratios — the live data a metric switch must re-apply.
    const TOKENS_GRAPH: TopologyResponse = {
      nodes: [
        { id: 'agent:root', kind: 'agent', label: 'nedi (root)', size_metric: 10, failure_ratio: 0 },
        { id: 'tool:shell.Bash', kind: 'tool', label: 'shell.Bash', size_metric: 5, failure_ratio: 0 },
        { id: 'tool:fs.Grep', kind: 'tool', label: 'fs.Grep', size_metric: 100, failure_ratio: 0.9 },
      ],
      edges: GRAPH.edges,
      max_size_metric: 100,
    };
    topologySpy.mockImplementation((_id: unknown, metric: unknown) =>
      result({ data: metric === 'tokens' ? TOKENS_GRAPH : GRAPH }),
    );
    render(<TopologyTab sessionId="s1" />);

    const graph = screen.getByRole('group', { name: /topology graph/i });
    const groupFor = (name: RegExp): SVGGElement => {
      const btn = within(graph).getByRole('button', { name });
      // The focusable element IS the positioned <g> (transform="translate(x,y)").
      return btn as unknown as SVGGElement;
    };
    // fs.Grep's pinned position + radius BEFORE the metric change (cost: 50% fail).
    const grepBefore = groupFor(/fs\.Grep/i);
    const posBefore = grepBefore.getAttribute('transform');
    const rBefore = grepBefore.querySelector('rect')?.getAttribute('width');
    expect(within(graph).getByRole('button', { name: /fs\.Grep/i })).toHaveAccessibleName(/50\.0%/);

    // Freeze, then switch the size metric → data refetches (tokens graph).
    await user.click(screen.getByRole('button', { name: /freeze layout/i }));
    await user.selectOptions(screen.getByRole('combobox', { name: /size by/i }), 'tokens');

    const grepAfter = groupFor(/fs\.Grep/i);
    // Position is PINNED (freeze stopped the simulation moving nodes)…
    expect(grepAfter.getAttribute('transform')).toBe(posBefore);
    // …but the DATA is live: failure ratio is now 90% (re-applied)…
    expect(within(graph).getByRole('button', { name: /fs\.Grep/i })).toHaveAccessibleName(/90\.0%/);
    // …and the radius grew (fs.Grep is now the max-metric node) — fresh radius applied.
    const rAfter = grepAfter.querySelector('rect')?.getAttribute('width');
    expect(Number(rAfter)).toBeGreaterThan(Number(rBefore));
  });

  it('shows the loading state while the query is pending', () => {
    topologySpy.mockReturnValue(result({ data: undefined, isPending: true }));
    render(<TopologyTab sessionId="s1" />);
    expect(screen.getByRole('status')).toHaveTextContent(/loading topology/i);
  });

  it('surfaces a fetch error in an alert (no silent failure)', () => {
    topologySpy.mockReturnValue(
      result({ isError: true, error: new Error('topology boom') }),
    );
    render(<TopologyTab sessionId="s1" />);
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent(/failed to load topology/i);
    expect(alert).toHaveTextContent(/topology boom/i);
  });

  it('shows an empty state when the graph has no nodes', () => {
    topologySpy.mockReturnValue(
      result({ data: { nodes: [], edges: [], max_size_metric: 0 } }),
    );
    render(<TopologyTab sessionId="s1" />);
    expect(screen.getByText(/no actors recorded/i)).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /topology graph/i })).not.toBeInTheDocument();
  });
});
