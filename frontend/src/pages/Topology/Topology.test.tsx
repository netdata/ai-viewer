import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { axe } from 'jest-axe';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';
import type { TopologyResponse } from '../../api/types';

// Topology is the cross-session actor-graph page (ui-pages.md §/topology). The
// data hook (useTopology) and the SSE lifecycle hook (useLiveUpdates) are MOCKED
// so the test drives the page's states directly, and the Vite ?worker import is
// stubbed (jsdom has no Worker; the small fixture stays below the 100-node
// threshold so the SVG path renders). The page reuses the chunk-6a
// TopologyRenderer + viz/topology, so the pure layout geometry is covered in
// viz/topology.test.ts; here we assert the page wiring: graph renders, the
// truncated notice appears only when the server capped the set, the metric
// selector + 3-mode layout toggle render, and a node click navigates to that
// session's detail (cross-session nodes are whole sessions).

const topologySpy = vi.fn();
const liveSpy = vi.fn();

vi.mock('../../api/topology', () => ({
  useTopology: (...args: unknown[]) => topologySpy(...args) as unknown,
}));
vi.mock('../../state/useLiveUpdates', () => ({
  useLiveUpdates: (...args: unknown[]) => liveSpy(...args) as unknown,
}));
// The ?worker import is a Vite virtual module with no jsdom equivalent.
vi.mock('../../viz/forceWorker?worker', () => ({
  default: class {
    onmessage: ((e: MessageEvent) => void) | null = null;
    postMessage(): void {}
    terminate(): void {}
  },
}));

import { Topology } from './Topology';

function result(over: Record<string, unknown>) {
  return { data: undefined, isPending: false, isError: false, error: null, ...over };
}

// A small cross-session fixture: two root sessions, one a parent of the other
// (lineage edge), failure_ratio drives color.
const GRAPH: TopologyResponse = {
  nodes: [
    { id: 'agent:rootA', kind: 'agent', label: 'nedi (root)', size_metric: 8000, failure_ratio: 0.2 },
    { id: 'agent:childA1', kind: 'agent', label: 'worker', size_metric: 2000, failure_ratio: 0 },
  ],
  edges: [{ source: 'agent:rootA', target: 'agent:childA1', calls: 1, total_us: 0 }],
  max_size_metric: 8000,
};

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{`${loc.pathname}${loc.search}`}</div>;
}

function renderPage(initialEntry = '/topology') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/topology" element={<Topology />} />
        <Route path="/sessions/:id" element={<div>detail</div>} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  topologySpy.mockReset();
  liveSpy.mockReset();
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

describe('Topology (cross-session)', () => {
  it('defaults to the duration size-metric and renders the 3 layout modes', () => {
    renderPage();
    const select = screen.getByRole('combobox', { name: /size by/i });
    expect(select).toHaveValue('duration');
    // The query was asked for the duration metric (the cross-session default).
    const lastCall = topologySpy.mock.calls.at(-1);
    expect(lastCall?.[1]).toBe('duration');
    expect(screen.getByRole('radio', { name: /seeded force/i })).toBeChecked();
    expect(screen.getByRole('radio', { name: /plain force/i })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /hierarchical/i })).toBeInTheDocument();
  });

  it('renders one accessible node per session on the SVG path', () => {
    renderPage();
    const graph = screen.getByRole('group', { name: /topology graph/i });
    const nodes = within(graph).getAllByRole('button');
    expect(nodes).toHaveLength(2);
    expect(within(graph).getByRole('button', { name: /nedi \(root\)/i })).toBeInTheDocument();
    expect(within(graph).getByRole('button', { name: /worker/i })).toBeInTheDocument();
  });

  it('switches the size metric and refetches with the new metric', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.selectOptions(screen.getByRole('combobox', { name: /size by/i }), 'cost');
    expect(screen.getByRole('combobox', { name: /size by/i })).toHaveValue('cost');
    const lastCall = topologySpy.mock.calls.at(-1);
    expect(lastCall?.[1]).toBe('cost');
  });

  it('switches the layout mode via the toggle', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(screen.getByRole('radio', { name: /hierarchical/i }));
    expect(screen.getByRole('radio', { name: /hierarchical/i })).toBeChecked();
    expect(screen.getByRole('group', { name: /topology graph/i })).toBeInTheDocument();
  });

  it('subscribes to live updates with the active filter', () => {
    renderPage();
    expect(liveSpy).toHaveBeenCalled();
    // Empty filters → an empty subscription filter object.
    expect(liveSpy.mock.calls[0]?.[0]).toEqual({});
  });

  it('navigates to the session detail when a cross-session node is clicked', async () => {
    const user = userEvent.setup();
    renderPage();
    const graph = screen.getByRole('group', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /worker/i }));
    // A cross-session node IS a whole session, so it drills into the detail
    // route (NOT the per-session span drawer).
    expect(screen.getByTestId('loc')).toHaveTextContent('/sessions/childA1');
  });

  it('does not render the per-session span drawer on a node click', async () => {
    const user = userEvent.setup();
    renderPage();
    const graph = screen.getByRole('group', { name: /topology graph/i });
    await user.click(within(graph).getByRole('button', { name: /worker/i }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('shows the "showing top N of M" notice when the result is truncated', () => {
    topologySpy.mockReturnValue(
      result({
        data: {
          nodes: GRAPH.nodes,
          edges: GRAPH.edges,
          max_size_metric: GRAPH.max_size_metric,
          truncated: true,
        },
      }),
    );
    renderPage();
    // The notice surfaces the cap (the rendered node count) so the operator
    // knows the graph is partial — never silently truncated (rest-api.md).
    expect(screen.getByText(/showing top/i)).toBeInTheDocument();
  });

  it('does not show the truncation notice when the result is complete', () => {
    renderPage();
    expect(screen.queryByText(/showing top/i)).not.toBeInTheDocument();
  });

  it('shows the loading state while the query is pending', () => {
    topologySpy.mockReturnValue(result({ data: undefined, isPending: true }));
    renderPage();
    expect(screen.getByRole('status')).toHaveTextContent(/loading topology/i);
  });

  it('surfaces a fetch error in an alert (no silent failure)', () => {
    topologySpy.mockReturnValue(result({ isError: true, error: new Error('topology boom') }));
    renderPage();
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent(/failed to load topology/i);
    expect(alert).toHaveTextContent(/topology boom/i);
  });

  it('shows an empty state when no sessions match the filter', () => {
    topologySpy.mockReturnValue(result({ data: { nodes: [], edges: [], max_size_metric: 0 } }));
    renderPage();
    expect(screen.getByText(/no sessions match/i)).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: /topology graph/i })).not.toBeInTheDocument();
  });

  it('shows the failure percentage in a failing session node label (color is not the only signal)', () => {
    // AC#5 color-not-sole-signal: the failing root session (20%) carries its
    // failure rate as on-graph text, not only the warning fill.
    renderPage();
    const graph = screen.getByRole('group', { name: /topology graph/i });
    const failing = within(graph).getByRole('button', { name: /nedi \(root\)/i });
    expect(failing.textContent ?? '').toMatch(/20\.0%/);
  });

  it('has no axe violations (component-level a11y for the cross-session Topology page)', async () => {
    const { container } = renderPage();
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('keeps node DATA live while frozen: a metric change re-applies fresh radius + failure_ratio at the pinned position', async () => {
    const user = userEvent.setup();
    // Same session ids, but the "cost" metric makes the worker the largest node
    // and changes failure ratios — the live data a metric switch must re-apply.
    const COST_GRAPH: TopologyResponse = {
      nodes: [
        { id: 'agent:rootA', kind: 'agent', label: 'nedi (root)', size_metric: 10, failure_ratio: 0 },
        { id: 'agent:childA1', kind: 'agent', label: 'worker', size_metric: 100, failure_ratio: 0.9 },
      ],
      edges: GRAPH.edges,
      max_size_metric: 100,
    };
    topologySpy.mockImplementation((_filters: unknown, metric: unknown) =>
      result({ data: metric === 'cost' ? COST_GRAPH : GRAPH }),
    );
    renderPage();

    const graph = screen.getByRole('group', { name: /topology graph/i });
    // The worker session BEFORE the metric change (duration: 0% fail, small).
    const workerBefore = within(graph).getByRole('button', { name: /worker/i }) as unknown as SVGGElement;
    const posBefore = workerBefore.getAttribute('transform');
    const rBefore = workerBefore.querySelector('circle')?.getAttribute('r');
    expect(within(graph).getByRole('button', { name: /worker/i })).toHaveAccessibleName(/0\.0%/);

    // Freeze, then switch the size metric → data refetches (cost graph).
    await user.click(screen.getByRole('button', { name: /freeze layout/i }));
    await user.selectOptions(screen.getByRole('combobox', { name: /size by/i }), 'cost');

    const workerAfter = within(graph).getByRole('button', { name: /worker/i }) as unknown as SVGGElement;
    // Position PINNED…
    expect(workerAfter.getAttribute('transform')).toBe(posBefore);
    // …DATA live: failure ratio now 90%…
    expect(within(graph).getByRole('button', { name: /worker/i })).toHaveAccessibleName(/90\.0%/);
    // …and the radius grew (worker is now the max-metric node).
    const rAfter = workerAfter.querySelector('circle')?.getAttribute('r');
    expect(Number(rAfter)).toBeGreaterThan(Number(rBefore));
  });
});
