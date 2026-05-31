import { describe, expect, it } from 'vitest';
import type { TopologyEdge, TopologyNode } from '../api/types';
import {
  FORCE_WORKER_THRESHOLD,
  layoutTopology,
  nodeRadius,
  positionsOf,
  reapplyFrozenPositions,
  type PositionedNode,
  type TopologyLayoutMode,
} from './topology';

// viz/topology.ts holds the PURE geometry/layout for the per-session Topology
// tab (ui-pages.md §/sessions/:id #2), separated from React (the viz/ boundary —
// project-frontend §D3 Patterns) and from the wire fetch. Given the server's
// layout-agnostic node/edge graph it computes positioned nodes for three
// operator-selectable layouts: seeded force (deterministic), plain force, and a
// hierarchical left-to-right tree. Node radius is normalized against
// max_size_metric. These tests pin the deterministic behavior without a browser
// (no Web Worker, no Canvas) — the force layouts run inline here.

function node(over: Partial<TopologyNode>): TopologyNode {
  return {
    id: 'n',
    kind: 'agent',
    label: 'n',
    size_metric: 0,
    failure_ratio: 0,
    ...over,
  };
}

function edge(source: string, target: string, over?: Partial<TopologyEdge>): TopologyEdge {
  return { source, target, calls: 1, total_us: 0, ...over };
}

// A small fixture tree: a root agent that calls one tool and spawns one
// sub-agent; the sub-agent calls a second tool. max_size_metric = 100 (root).
function fixtureNodes(): TopologyNode[] {
  return [
    node({ id: 'agent:root', kind: 'agent', label: 'nedi (root)', size_metric: 100 }),
    node({ id: 'agent:child', kind: 'agent', label: 'worker', size_metric: 40 }),
    node({ id: 'tool:shell.Bash', kind: 'tool', label: 'shell.Bash', size_metric: 25 }),
    node({ id: 'tool:fs.Read', kind: 'tool', label: 'fs.Read', size_metric: 10 }),
  ];
}

function fixtureEdges(): TopologyEdge[] {
  return [
    edge('agent:root', 'tool:shell.Bash', { calls: 12 }),
    edge('agent:root', 'agent:child', { calls: 1 }),
    edge('agent:child', 'tool:fs.Read', { calls: 3 }),
  ];
}

const SIZE = { width: 800, height: 600, maxSizeMetric: 100 } as const;

function byId(positioned: PositionedNode[]): Map<string, PositionedNode> {
  return new Map(positioned.map((p) => [p.node.id, p]));
}

describe('nodeRadius', () => {
  it('maps 0 metric to the minimum radius and max metric to the maximum radius', () => {
    expect(nodeRadius(0, 100, 4, 28)).toBeCloseTo(4, 6);
    expect(nodeRadius(100, 100, 4, 28)).toBeCloseTo(28, 6);
  });

  it('is monotonic non-decreasing in the metric', () => {
    const r0 = nodeRadius(0, 100, 4, 28);
    const r1 = nodeRadius(25, 100, 4, 28);
    const r2 = nodeRadius(64, 100, 4, 28);
    const r3 = nodeRadius(100, 100, 4, 28);
    expect(r1).toBeGreaterThanOrEqual(r0);
    expect(r2).toBeGreaterThanOrEqual(r1);
    expect(r3).toBeGreaterThanOrEqual(r2);
  });

  it('falls back to the minimum radius when max_size_metric is 0 (no nodes / flat graph)', () => {
    expect(nodeRadius(0, 0, 4, 28)).toBeCloseTo(4, 6);
    expect(nodeRadius(5, 0, 4, 28)).toBeCloseTo(4, 6);
  });

  it('clamps a metric above the max to the maximum radius (defensive)', () => {
    expect(nodeRadius(250, 100, 4, 28)).toBeCloseTo(28, 6);
  });
});

describe('layoutTopology — empty graph', () => {
  it.each<TopologyLayoutMode>(['force-seeded', 'force-plain', 'hierarchical'])(
    'returns [] for an empty node list (%s)',
    (mode) => {
      expect(layoutTopology([], [], { ...SIZE, mode, maxSizeMetric: 0 })).toEqual([]);
    },
  );
});

describe('layoutTopology — radius normalization', () => {
  it('sizes every node radius against max_size_metric within [minRadius,maxRadius]', () => {
    const positioned = layoutTopology(fixtureNodes(), fixtureEdges(), {
      ...SIZE,
      mode: 'hierarchical',
      minRadius: 4,
      maxRadius: 28,
    });
    const m = byId(positioned);
    // root has the max metric → max radius; others are strictly smaller.
    expect(m.get('agent:root')?.radius).toBeCloseTo(28, 6);
    for (const id of ['agent:child', 'tool:shell.Bash', 'tool:fs.Read']) {
      const r = m.get(id)?.radius ?? 0;
      expect(r).toBeGreaterThanOrEqual(4);
      expect(r).toBeLessThan(28);
    }
    // Larger metric → larger-or-equal radius (child=40 ≥ Bash=25 ≥ Read=10).
    expect(m.get('agent:child')?.radius ?? 0).toBeGreaterThanOrEqual(
      m.get('tool:shell.Bash')?.radius ?? 0,
    );
    expect(m.get('tool:shell.Bash')?.radius ?? 0).toBeGreaterThanOrEqual(
      m.get('tool:fs.Read')?.radius ?? 0,
    );
  });

  it('carries every input node through exactly once, preserving node identity', () => {
    const positioned = layoutTopology(fixtureNodes(), fixtureEdges(), {
      ...SIZE,
      mode: 'force-seeded',
    });
    expect(positioned).toHaveLength(4);
    expect(new Set(positioned.map((p) => p.node.id))).toEqual(
      new Set(['agent:root', 'agent:child', 'tool:shell.Bash', 'tool:fs.Read']),
    );
  });

  it('produces finite coordinates for every node in every mode', () => {
    for (const mode of ['force-seeded', 'force-plain', 'hierarchical'] as const) {
      const positioned = layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode });
      for (const p of positioned) {
        expect(Number.isFinite(p.x)).toBe(true);
        expect(Number.isFinite(p.y)).toBe(true);
        expect(Number.isFinite(p.radius)).toBe(true);
      }
    }
  });
});

describe('layoutTopology — force-seeded determinism', () => {
  it('produces identical positions for identical input (no Math.random)', () => {
    const a = layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode: 'force-seeded' });
    const b = layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode: 'force-seeded' });
    const ma = byId(a);
    const mb = byId(b);
    for (const id of ma.keys()) {
      expect(mb.get(id)?.x).toBeCloseTo(ma.get(id)?.x ?? NaN, 9);
      expect(mb.get(id)?.y).toBeCloseTo(ma.get(id)?.y ?? NaN, 9);
    }
  });

  it('keeps a given node id at the same seed position regardless of node array order', () => {
    const nodes = fixtureNodes();
    const reversed = [...nodes].reverse();
    const a = byId(layoutTopology(nodes, [], { ...SIZE, mode: 'force-seeded' }));
    const b = byId(layoutTopology(reversed, [], { ...SIZE, mode: 'force-seeded' }));
    // With no edges there are no inter-node forces, so each node stays at its
    // id-seeded anchor: the layout is order-independent and fully deterministic.
    for (const id of a.keys()) {
      expect(b.get(id)?.x).toBeCloseTo(a.get(id)?.x ?? NaN, 9);
      expect(b.get(id)?.y).toBeCloseTo(a.get(id)?.y ?? NaN, 9);
    }
  });
});

describe('layoutTopology — hierarchical', () => {
  it('places roots (no incoming edge) left of their children (child x > parent x)', () => {
    const m = byId(
      layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode: 'hierarchical' }),
    );
    const root = m.get('agent:root');
    const child = m.get('agent:child');
    const bash = m.get('tool:shell.Bash');
    const read = m.get('tool:fs.Read');
    expect(root && child && bash && read).toBeTruthy();
    // root is the only node with no incoming edge → leftmost layer.
    expect((child?.x ?? 0)).toBeGreaterThan(root?.x ?? 0);
    expect((bash?.x ?? 0)).toBeGreaterThan(root?.x ?? 0);
    // fs.Read is a grandchild (root → child → fs.Read) → deepest layer.
    expect((read?.x ?? 0)).toBeGreaterThan(child?.x ?? 0);
  });

  it('groups nodes of the same depth on the same x layer', () => {
    const m = byId(
      layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode: 'hierarchical' }),
    );
    // agent:child and tool:shell.Bash are both direct children of root (depth 1).
    expect(m.get('agent:child')?.x).toBeCloseTo(m.get('tool:shell.Bash')?.x ?? NaN, 6);
  });

  it('handles a cycle without infinite looping (every node still positioned once)', () => {
    // A → B → A is a degenerate cycle; neither has a true "no incoming edge"
    // root, so the layout must still terminate and position both exactly once.
    const nodes = [
      node({ id: 'agent:a', kind: 'agent', size_metric: 10 }),
      node({ id: 'agent:b', kind: 'agent', size_metric: 10 }),
    ];
    const edges = [edge('agent:a', 'agent:b'), edge('agent:b', 'agent:a')];
    const positioned = layoutTopology(nodes, edges, {
      width: 800,
      height: 600,
      maxSizeMetric: 10,
      mode: 'hierarchical',
    });
    expect(positioned).toHaveLength(2);
    for (const p of positioned) {
      expect(Number.isFinite(p.x)).toBe(true);
      expect(Number.isFinite(p.y)).toBe(true);
    }
  });
});

describe('positionsOf — snapshots a layout to id → {x,y} only', () => {
  it('keeps only x and y per node id (drops label/radius/failure_ratio)', () => {
    const positioned: PositionedNode[] = [
      { node: node({ id: 'agent:root' }), x: 11, y: 22, radius: 9 },
      { node: node({ id: 'tool:t' }), x: 33, y: 44, radius: 5 },
    ];
    const m = positionsOf(positioned);
    expect(m.get('agent:root')).toEqual({ x: 11, y: 22 });
    expect(m.get('tool:t')).toEqual({ x: 33, y: 44 });
    expect(m.size).toBe(2);
  });

  it('round-trips through reapplyFrozenPositions, preserving coordinates', () => {
    const positioned = layoutTopology(fixtureNodes(), fixtureEdges(), { ...SIZE, mode: 'force-seeded' });
    const out = byId(reapplyFrozenPositions(fixtureNodes(), positionsOf(positioned), { ...SIZE, mode: 'force-seeded' }));
    for (const p of positioned) {
      expect(out.get(p.node.id)?.x).toBeCloseTo(p.x, 9);
      expect(out.get(p.node.id)?.y).toBeCloseTo(p.y, 9);
    }
  });
});

describe('reapplyFrozenPositions — freeze pins POSITIONS, data stays live', () => {
  // Freeze stores only node id → {x,y}. On a metric/filter/SSE refetch the FRESH
  // node data (label, radius from the new size_metric, failure_ratio) is re-applied
  // onto those pinned positions, matched by id ("freeze = stop the
  // simulation moving nodes, NOT stop data updates").
  const frozen = new Map<string, { x: number; y: number }>([
    ['agent:root', { x: 111, y: 222 }],
    ['tool:shell.Bash', { x: 333, y: 444 }],
  ]);

  it('keeps the frozen x/y but re-applies the fresh radius when the size metric changes', () => {
    // A new metric makes shell.Bash the largest node (max_size_metric now 60).
    const freshNodes = [
      node({ id: 'agent:root', kind: 'agent', label: 'nedi (root)', size_metric: 10 }),
      node({ id: 'tool:shell.Bash', kind: 'tool', label: 'shell.Bash', size_metric: 60 }),
    ];
    const out = reapplyFrozenPositions(freshNodes, frozen, {
      ...SIZE,
      mode: 'force-seeded',
      maxSizeMetric: 60,
      minRadius: 4,
      maxRadius: 28,
    });
    const m = byId(out);
    // Positions are pinned to the frozen snapshot…
    expect(m.get('agent:root')?.x).toBe(111);
    expect(m.get('agent:root')?.y).toBe(222);
    expect(m.get('tool:shell.Bash')?.x).toBe(333);
    expect(m.get('tool:shell.Bash')?.y).toBe(444);
    // …but the radius reflects the FRESH metric: shell.Bash is now the max → max radius.
    expect(m.get('tool:shell.Bash')?.radius).toBeCloseTo(28, 6);
    expect(m.get('agent:root')?.radius ?? 0).toBeLessThan(28);
  });

  it('re-applies the fresh label and failure_ratio onto the pinned node', () => {
    const freshNodes = [
      node({ id: 'agent:root', kind: 'agent', label: 'renamed root', size_metric: 10, failure_ratio: 0.75 }),
    ];
    const out = reapplyFrozenPositions(freshNodes, frozen, { ...SIZE, mode: 'force-seeded' });
    const root = out.find((p) => p.node.id === 'agent:root');
    expect(root?.node.label).toBe('renamed root');
    expect(root?.node.failure_ratio).toBe(0.75);
    expect(root?.x).toBe(111); // still pinned
  });

  it('gives a newly-appeared node (absent from the frozen snapshot) a finite layout position', () => {
    const freshNodes = [
      node({ id: 'agent:root', kind: 'agent', size_metric: 10 }),
      node({ id: 'agent:new', kind: 'agent', label: 'new', size_metric: 10 }), // not in `frozen`
    ];
    const out = reapplyFrozenPositions(freshNodes, frozen, { ...SIZE, mode: 'force-seeded' });
    const fresh = out.find((p) => p.node.id === 'agent:new');
    expect(fresh).toBeTruthy();
    expect(Number.isFinite(fresh?.x ?? NaN)).toBe(true);
    expect(Number.isFinite(fresh?.y ?? NaN)).toBe(true);
    // The pinned node still sits at its frozen coordinate.
    expect(out.find((p) => p.node.id === 'agent:root')?.x).toBe(111);
  });

  it('drops a vanished node (present in the frozen snapshot but gone from the fresh data)', () => {
    // Only root remains; shell.Bash vanished from the live data.
    const freshNodes = [node({ id: 'agent:root', kind: 'agent', size_metric: 10 })];
    const out = reapplyFrozenPositions(freshNodes, frozen, { ...SIZE, mode: 'force-seeded' });
    expect(out).toHaveLength(1);
    expect(out.some((p) => p.node.id === 'tool:shell.Bash')).toBe(false);
  });

  it('returns [] for empty fresh data even with a non-empty frozen snapshot', () => {
    expect(reapplyFrozenPositions([], frozen, { ...SIZE, mode: 'force-seeded' })).toEqual([]);
  });

  it('is order-independent for the newly-appeared seed (same id → same fallback position)', () => {
    // A new node's fallback position is id-seeded (deterministic), so it does not
    // depend on array order — the same property the force-seeded layout relies on.
    const a = [node({ id: 'agent:x', size_metric: 5 }), node({ id: 'agent:y', size_metric: 5 })];
    const b = [...a].reverse();
    const ra = byId(reapplyFrozenPositions(a, new Map(), { ...SIZE, mode: 'force-seeded' }));
    const rb = byId(reapplyFrozenPositions(b, new Map(), { ...SIZE, mode: 'force-seeded' }));
    expect(rb.get('agent:x')?.x).toBeCloseTo(ra.get('agent:x')?.x ?? NaN, 9);
    expect(rb.get('agent:x')?.y).toBeCloseTo(ra.get('agent:x')?.y ?? NaN, 9);
  });
});

describe('FORCE_WORKER_THRESHOLD', () => {
  it('matches the frontend-architecture.md 100-node Web-Worker boundary', () => {
    expect(FORCE_WORKER_THRESHOLD).toBe(100);
  });
});
