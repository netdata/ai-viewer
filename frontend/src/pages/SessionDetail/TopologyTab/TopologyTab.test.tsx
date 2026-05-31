import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
      save: vi.fn(),
      restore: vi.fn(),
      scale: vi.fn(),
      translate: vi.fn(),
      set fillStyle(_v: string) {},
      set strokeStyle(_v: string) {},
      set lineWidth(_v: number) {},
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
    const graph = screen.getByRole('img', { name: /topology graph/i });
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
    expect(screen.getByRole('img', { name: /topology graph/i })).toBeInTheDocument();
  });

  it('opens the shared detail drawer when a node is clicked', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('img', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /shell\.Bash/i }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAccessibleName(/shell\.Bash/i);
  });

  it('closes the drawer on Escape', async () => {
    const user = userEvent.setup();
    render(<TopologyTab sessionId="s1" />);
    const graph = screen.getByRole('img', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /shell\.Bash/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
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
    expect(screen.queryByRole('img', { name: /topology graph/i })).not.toBeInTheDocument();
  });
});
