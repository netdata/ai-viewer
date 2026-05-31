import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { TopologyEdge } from '../../../api/types';
import type { PositionedNode } from '../../../viz/topology';
import { TopologyRenderer } from './TopologyRenderer';

// Focused tests for the TopologyRenderer: the SVG path (one clickable node per
// actor, agent=circle vs tool=square, keyboard activation, selected styling) and
// the Canvas path (canvas painted, click hit-tests the node under the cursor).
// Layout geometry is covered in viz/topology.test.ts; this pins the painting +
// interaction wiring.

function pnode(
  node: PositionedNode['node'],
  x: number,
  y: number,
  radius: number,
): PositionedNode {
  return { node, x, y, radius };
}

const POSITIONED: PositionedNode[] = [
  pnode(
    { id: 'agent:root', kind: 'agent', label: 'nedi (root)', size_metric: 100, failure_ratio: 0 },
    100,
    100,
    20,
  ),
  pnode(
    { id: 'tool:Bash', kind: 'tool', label: 'shell.Bash', size_metric: 40, failure_ratio: 0.5 },
    300,
    100,
    12,
  ),
];

const EDGES: TopologyEdge[] = [
  { source: 'agent:root', target: 'tool:Bash', calls: 5, total_us: 1000 },
];

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
  };
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    ctxStub as unknown as CanvasRenderingContext2D,
  );
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('TopologyRenderer (SVG path)', () => {
  it('renders one accessible node per actor with agent-circle and tool-square shapes', () => {
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const graph = screen.getByRole('img', { name: /topology graph/i });
    const nodes = within(graph).getAllByRole('button');
    expect(nodes).toHaveLength(2);
    // The agent node draws a <circle>, the tool node draws a <rect>.
    const agent = within(graph).getByRole('button', { name: /nedi \(root\)/i });
    const tool = within(graph).getByRole('button', { name: /shell\.Bash/i });
    expect(agent.querySelector('circle')).not.toBeNull();
    expect(tool.querySelector('rect')).not.toBeNull();
  });

  it('calls onNodeClick when a node is clicked or keyboard-activated', async () => {
    const user = userEvent.setup();
    const onNodeClick = vi.fn();
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={onNodeClick}
        useCanvas={false}
      />,
    );
    await user.click(screen.getByRole('button', { name: /nedi \(root\)/i }));
    fireEvent.keyDown(screen.getByRole('button', { name: /shell\.Bash/i }), { key: 'Enter' });
    expect(onNodeClick).toHaveBeenCalledTimes(2);
  });

  it('marks the selected node with the selected style', () => {
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId="tool:Bash"
        onNodeClick={vi.fn()}
        useCanvas={false}
      />,
    );
    expect(
      screen.getByRole('button', { name: /shell\.Bash/i }).getAttribute('class') ?? '',
    ).toMatch(/nodeSelected/);
  });

  it('draws an edge line between the connected nodes', () => {
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={vi.fn()}
        useCanvas={false}
      />,
    );
    const graph = screen.getByRole('img', { name: /topology graph/i });
    expect(graph.querySelectorAll('line')).toHaveLength(1);
  });
});

describe('TopologyRenderer (Canvas path)', () => {
  it('paints a canvas instead of per-node SVG shapes', () => {
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={vi.fn()}
        useCanvas={true}
      />,
    );
    const graph = screen.getByRole('img', { name: /topology graph/i });
    expect(graph.querySelector('canvas')).not.toBeNull();
    expect(within(graph).queryAllByRole('button')).toHaveLength(0);
    // The agent circle is arc-filled; edges are stroked.
    expect(ctxStub.arc).toHaveBeenCalled();
    expect(ctxStub.stroke).toHaveBeenCalled();
  });

  it('hit-tests a click against the node under the cursor', () => {
    const onNodeClick = vi.fn();
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={onNodeClick}
        useCanvas={true}
      />,
    );
    const canvas = screen
      .getByRole('img', { name: /topology graph/i })
      .querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0,
      left: 0,
      right: 600,
      bottom: 400,
      width: 600,
      height: 400,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    // The agent node sits at (100,100) r=20 (identity transform): a click there
    // lands on it.
    fireEvent.click(canvas, { clientX: 100, clientY: 100 });
    expect(onNodeClick).toHaveBeenCalledTimes(1);
    expect(onNodeClick.mock.calls[0]?.[0]).toMatchObject({ node: { id: 'agent:root' } });
  });

  it('ignores a click that lands on empty canvas space', () => {
    const onNodeClick = vi.fn();
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={onNodeClick}
        useCanvas={true}
      />,
    );
    const canvas = screen
      .getByRole('img', { name: /topology graph/i })
      .querySelector('canvas') as HTMLCanvasElement;
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue({
      top: 0,
      left: 0,
      right: 600,
      bottom: 400,
      width: 600,
      height: 400,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    // Far from any node.
    fireEvent.click(canvas, { clientX: 500, clientY: 350 });
    expect(onNodeClick).not.toHaveBeenCalled();
  });
});

describe('TopologyRenderer (auto path selection)', () => {
  it('uses the SVG path below the node ceiling by default', () => {
    render(
      <TopologyRenderer
        positioned={POSITIONED}
        edges={EDGES}
        width={600}
        height={400}
        selectedId={null}
        onNodeClick={vi.fn()}
      />,
    );
    const graph = screen.getByRole('img', { name: /topology graph/i });
    // Small graph → SVG nodes, no canvas.
    expect(graph.querySelector('canvas')).toBeNull();
    expect(within(graph).getAllByRole('button')).toHaveLength(2);
  });
});
