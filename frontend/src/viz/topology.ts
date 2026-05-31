import {
  forceCenter,
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
  type SimulationLinkDatum,
  type SimulationNodeDatum,
} from 'd3-force';
import type { TopologyEdge, TopologyNode } from '../api/types';

// Pure geometry/layout for the per-session Topology tab (ui-pages.md
// §/sessions/:id #2). Lives in viz/ so React components consume plain positioned
// data and never import D3 directly (project-frontend §D3 Patterns), mirroring
// viz/trace.ts. The server returns a LAYOUT-AGNOSTIC actor graph (nodes + edges,
// no coordinates — rest-api.md §GET /api/sessions/:id/topology); this module
// computes node positions for three operator-selectable layouts:
//
//   - 'force-seeded'  — DETERMINISTIC force layout: each node's initial position
//     and a weak X/Y anchor are derived from a hash of its id (no Math.random),
//     so the same graph always lays out the same way and a "freeze layout"
//     button has a stable result to pin. The simulation's internal randomness is
//     also replaced by an id-seeded PRNG, so the output is fully reproducible.
//   - 'force-plain'   — a standard d3-force layout (charge + link + center),
//     still seeded deterministically so coordinates are finite and reproducible.
//   - 'hierarchical'  — a left-to-right layered tree: roots (nodes with no
//     incoming edge) on the leftmost layer, each callee one layer to the right
//     (child x > parent x). Cycle-safe (a strongly-connected component never
//     loops forever; every node is positioned exactly once).
//
// The force layouts run INLINE here (this module is browser-free and Worker-free
// — the Worker entry lives in forceWorker.ts and reuses runForceLayout). Above
// FORCE_WORKER_THRESHOLD nodes the renderer offloads to the Worker so the
// O(n²)-per-tick math never janks the main thread (frontend-architecture.md
// §Web Worker for D3 force simulation).

/** The three operator-selectable layout engines (ui-pages.md §Topology). */
export type TopologyLayoutMode = 'force-seeded' | 'force-plain' | 'hierarchical';

/** A node with its computed position and normalized radius. */
export interface PositionedNode {
  node: TopologyNode;
  x: number;
  y: number;
  radius: number;
}

export interface TopologyLayoutOpts {
  mode: TopologyLayoutMode;
  /** Viewport width (px) the layout is centered/scaled within. */
  width: number;
  /** Viewport height (px). */
  height: number;
  /** Max size_metric across nodes (server `max_size_metric`); 0 ⇒ flat sizing. */
  maxSizeMetric: number;
  /** Smallest node radius in px (default 5). */
  minRadius?: number;
  /** Largest node radius in px (default 26). */
  maxRadius?: number;
}

/**
 * FORCE_WORKER_THRESHOLD is the node count above which the force simulation must
 * run in a Web Worker rather than inline (frontend-architecture.md §Performance
 * Budgets: "D3 force simulation runs in a Web Worker when > 100 nodes"). The
 * renderer reads this to decide inline-vs-worker; the math is identical either
 * way (runForceLayout).
 */
export const FORCE_WORKER_THRESHOLD = 100;

const DEFAULT_MIN_RADIUS = 5;
const DEFAULT_MAX_RADIUS = 26;

/**
 * nodeRadius maps a node's size_metric to a pixel radius in [minRadius,maxRadius],
 * normalized against maxSizeMetric. Area-proportional scaling (sqrt of the ratio)
 * keeps the VISUAL AREA proportional to the metric — a node with 4× the metric
 * reads as 4× the ink, not 16× — which is the honest encoding for circle size.
 * maxSizeMetric ≤ 0 (no nodes, or every metric 0) collapses to minRadius. A ratio
 * above 1 (defensive — a metric exceeding the reported max) clamps to maxRadius.
 */
export function nodeRadius(
  sizeMetric: number,
  maxSizeMetric: number,
  minRadius: number,
  maxRadius: number,
): number {
  if (maxSizeMetric <= 0 || !Number.isFinite(maxSizeMetric)) {
    return minRadius;
  }
  const ratio = Math.max(0, Math.min(1, sizeMetric / maxSizeMetric));
  return minRadius + (maxRadius - minRadius) * Math.sqrt(ratio);
}

// ── Deterministic seeding helpers ────────────────────────────────────────────

/** fnv1a hashes a string to an unsigned 32-bit int (stable, no deps). */
function fnv1a(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    // h *= 16777619, kept in 32-bit unsigned space.
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

/** mulberry32 is a tiny deterministic PRNG seeded from a 32-bit int → [0,1). */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * seedPosition derives a stable initial (x,y) for a node id within the viewport.
 * The id hash drives a point on a phyllotaxis-like spiral so distinct ids spread
 * out instead of colliding, and the same id always lands on the same seed — the
 * basis for 'force-seeded' determinism and the freeze button's stability.
 */
function seedPosition(id: string, width: number, height: number): { x: number; y: number } {
  const h = fnv1a(id);
  const rand = mulberry32(h);
  const angle = rand() * Math.PI * 2;
  const radius = Math.sqrt(rand()) * (Math.min(width, height) / 2 - 1);
  return {
    x: width / 2 + Math.cos(angle) * radius,
    y: height / 2 + Math.sin(angle) * radius,
  };
}

// ── Force simulation (inline + Worker share this) ────────────────────────────

/** Mutable simulation node datum: the public node plus d3-force's x/y/fx/fy. */
interface SimNode extends SimulationNodeDatum {
  node: TopologyNode;
  radius: number;
}

type SimLink = SimulationLinkDatum<SimNode>;

/**
 * forceTicks caps the simulation iterations as a function of node count
 * (frontend-architecture.md): min(300, ceil(log(n+1)*60)). A small graph settles
 * in a handful of ticks; a large graph is bounded at 300 so a single layout pass
 * is O(n² · 300) worst case — predictable and Worker-offloaded above the
 * threshold.
 */
export function forceTicks(nodeCount: number): number {
  return Math.min(300, Math.ceil(Math.log(nodeCount + 1) * 60));
}

/**
 * runForceLayout runs a deterministic d3-force simulation to convergence and
 * returns positioned nodes. Shared by the inline path (layoutTopology) and the
 * Web Worker (forceWorker.ts) so the math is defined once. `seeded` adds weak
 * forceX/forceY anchors toward each node's id-seeded position (the 'force-seeded'
 * mode); without it the layout is a plain charge+link+center force. The
 * simulation's randomSource is replaced by an id-derived PRNG, and initial
 * positions are id-seeded, so the result is fully reproducible (no Math.random).
 */
export function runForceLayout(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  opts: TopologyLayoutOpts,
  seeded: boolean,
): PositionedNode[] {
  const { width, height, maxSizeMetric } = opts;
  const minRadius = opts.minRadius ?? DEFAULT_MIN_RADIUS;
  const maxRadius = opts.maxRadius ?? DEFAULT_MAX_RADIUS;

  // Stable per-node seed point + radius. The simNodes carry x/y pre-set so the
  // first tick starts from the deterministic layout rather than d3's default
  // phyllotaxis (which would still be deterministic, but anchoring to OUR seed
  // makes 'freeze' meaningful and the seeded anchors land where we expect).
  const simNodes: SimNode[] = nodes.map((node) => {
    const p = seedPosition(node.id, width, height);
    return {
      node,
      radius: nodeRadius(node.size_metric, maxSizeMetric, minRadius, maxRadius),
      x: p.x,
      y: p.y,
    };
  });
  const byId = new Map(simNodes.map((n) => [n.node.id, n]));
  // Drop edges whose endpoints are not both present (defensive — the server
  // emits consistent graphs, but a stray edge would make forceLink throw).
  const simLinks: SimLink[] = edges
    .filter((e) => byId.has(e.source) && byId.has(e.target))
    .map((e) => ({ source: e.source, target: e.target }));

  // A single id-derived seed makes the whole simulation reproducible: same graph
  // ⇒ same random stream ⇒ same settled coordinates.
  const seed = nodes.reduce((acc, n) => (acc ^ fnv1a(n.id)) >>> 0, 0x9e3779b9);
  const sim = forceSimulation<SimNode>(simNodes)
    .randomSource(mulberry32(seed >>> 0))
    .force('charge', forceManyBody<SimNode>().strength(-220).theta(0.9))
    .force(
      'link',
      forceLink<SimNode, SimLink>(simLinks)
        .id((d) => d.node.id)
        .distance(70)
        .strength(0.4),
    )
    .force('collide', forceCollide<SimNode>().radius((d) => d.radius + 3))
    .stop();

  if (seeded) {
    // Weak anchors pull each node toward its id-seeded position so the layout is
    // stable across renders/SSE while edges still shape the local structure.
    sim
      .force('x', forceX<SimNode>((d) => seedPosition(d.node.id, width, height).x).strength(0.08))
      .force('y', forceY<SimNode>((d) => seedPosition(d.node.id, width, height).y).strength(0.08));
  } else {
    // Plain force centers the whole graph in the viewport.
    sim.force('center', forceCenter<SimNode>(width / 2, height / 2));
  }

  const ticks = forceTicks(simNodes.length);
  for (let i = 0; i < ticks; i++) {
    sim.tick();
  }
  sim.stop();

  return simNodes.map((n) => ({
    node: n.node,
    x: n.x ?? width / 2,
    y: n.y ?? height / 2,
    radius: n.radius,
  }));
}

// ── Hierarchical (left-to-right layered tree) ────────────────────────────────

/**
 * runHierarchicalLayout arranges nodes in left-to-right layers by BFS distance
 * from the roots (nodes with no incoming edge — the agents nobody calls). Each
 * callee sits one layer right of its nearest caller (child x > parent x); nodes
 * sharing a depth share an x and are spread vertically. Cycle-safe: a node is
 * assigned a depth once (first visit wins), and any node unreachable from a root
 * (e.g. a pure cycle with no entry point) is given the deepest+1 layer so it is
 * still positioned exactly once. Pure (no D3) — d3-hierarchy needs a single-root
 * forest, which this graph is not, so a direct BFS is both simpler and correct.
 */
export function runHierarchicalLayout(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  opts: TopologyLayoutOpts,
): PositionedNode[] {
  const { width, height, maxSizeMetric } = opts;
  const minRadius = opts.minRadius ?? DEFAULT_MIN_RADIUS;
  const maxRadius = opts.maxRadius ?? DEFAULT_MAX_RADIUS;

  const ids = nodes.map((n) => n.id);
  const present = new Set(ids);
  const incoming = new Set<string>();
  const adjacency = new Map<string, string[]>();
  for (const id of ids) {
    adjacency.set(id, []);
  }
  for (const e of edges) {
    if (!present.has(e.source) || !present.has(e.target)) {
      continue;
    }
    adjacency.get(e.source)?.push(e.target);
    incoming.add(e.target);
  }

  // Roots = nodes nobody points to, in input order (stable, root-first).
  const roots = ids.filter((id) => !incoming.has(id));
  const depth = new Map<string, number>();
  const queue: string[] = [];
  for (const r of roots) {
    depth.set(r, 0);
    queue.push(r);
  }
  // BFS; first assignment of a depth wins (cycle-safe — a node already seen is
  // never re-enqueued, so a back-edge cannot loop).
  let head = 0;
  while (head < queue.length) {
    const id = queue[head++] as string;
    const d = depth.get(id) ?? 0;
    for (const next of adjacency.get(id) ?? []) {
      if (!depth.has(next)) {
        depth.set(next, d + 1);
        queue.push(next);
      }
    }
  }
  // Any node unreached by the BFS (a pure cycle with no root entry) lands one
  // layer past the deepest assigned, so every node gets a finite depth once.
  let maxDepth = 0;
  for (const d of depth.values()) {
    if (d > maxDepth) {
      maxDepth = d;
    }
  }
  const orphanDepth = depth.size < ids.length ? maxDepth + 1 : maxDepth;
  for (const id of ids) {
    if (!depth.has(id)) {
      depth.set(id, orphanDepth);
    }
  }

  // Group node ids by depth (input order within a layer for stability).
  const layers = new Map<number, string[]>();
  let layerCount = 0;
  for (const id of ids) {
    const d = depth.get(id) ?? 0;
    if (!layers.has(d)) {
      layers.set(d, []);
    }
    layers.get(d)?.push(id);
    if (d + 1 > layerCount) {
      layerCount = d + 1;
    }
  }

  // Map each (depth, indexInLayer) to a pixel position: x by layer across the
  // width, y spread evenly down the height. Single-layer/single-node cases avoid
  // divide-by-zero by centering.
  const pos = new Map<string, { x: number; y: number }>();
  const xStep = layerCount > 1 ? width / (layerCount + 1) : width / 2;
  for (const [d, layerIds] of layers) {
    const x = layerCount > 1 ? xStep * (d + 1) : width / 2;
    const count = layerIds.length;
    const yStep = count > 0 ? height / (count + 1) : height / 2;
    layerIds.forEach((id, i) => {
      pos.set(id, { x, y: yStep * (i + 1) });
    });
  }

  return nodes.map((node) => {
    const p = pos.get(node.id) ?? { x: width / 2, y: height / 2 };
    return {
      node,
      x: p.x,
      y: p.y,
      radius: nodeRadius(node.size_metric, maxSizeMetric, minRadius, maxRadius),
    };
  });
}

/**
 * layoutTopology is the single entry point: it dispatches to the force engines
 * or the hierarchical layout by `opts.mode`. An empty node list returns [] in
 * every mode (no work, no NaNs). The force modes run inline here; the renderer
 * decides inline-vs-Worker by FORCE_WORKER_THRESHOLD (the Worker calls
 * runForceLayout directly).
 */
export function layoutTopology(
  nodes: TopologyNode[],
  edges: TopologyEdge[],
  opts: TopologyLayoutOpts,
): PositionedNode[] {
  if (nodes.length === 0) {
    return [];
  }
  switch (opts.mode) {
    case 'hierarchical':
      return runHierarchicalLayout(nodes, edges, opts);
    case 'force-plain':
      return runForceLayout(nodes, edges, opts, false);
    case 'force-seeded':
    default:
      return runForceLayout(nodes, edges, opts, true);
  }
}

/**
 * reapplyFrozenPositions re-applies the FRESH node data (label, radius from the
 * current size_metric, failure_ratio) onto a frozen set of POSITIONS, matched by
 * node id. This is what "freeze layout" means: the simulation stops moving nodes,
 * but data keeps updating (codex P2#5). A metric/filter/SSE refetch hands a new
 * `nodes` array here; each node keeps its pinned (x,y) when its id is in `frozen`,
 * while its radius is recomputed against the fresh `maxSizeMetric` and its label /
 * failure_ratio come straight from the fresh node. A node that NEWLY appeared
 * (absent from the frozen snapshot) gets a deterministic id-seeded fallback
 * position so it is placed without re-simulating the pinned nodes; a node that
 * VANISHED (in the snapshot but gone from the fresh data) is dropped by iterating
 * the fresh nodes. Pure + deterministic (no Math.random, no simulation).
 */
export function reapplyFrozenPositions(
  nodes: TopologyNode[],
  frozen: ReadonlyMap<string, { x: number; y: number }>,
  opts: TopologyLayoutOpts,
): PositionedNode[] {
  if (nodes.length === 0) {
    return [];
  }
  const { width, height, maxSizeMetric } = opts;
  const minRadius = opts.minRadius ?? DEFAULT_MIN_RADIUS;
  const maxRadius = opts.maxRadius ?? DEFAULT_MAX_RADIUS;
  return nodes.map((node) => {
    // Pinned position when the node existed at freeze time; otherwise an
    // id-seeded fallback (the same seed the force layout uses) so a fresh node
    // lands deterministically without moving the frozen ones.
    const pinned = frozen.get(node.id) ?? seedPosition(node.id, width, height);
    return {
      node,
      x: pinned.x,
      y: pinned.y,
      radius: nodeRadius(node.size_metric, maxSizeMetric, minRadius, maxRadius),
    };
  });
}

/**
 * positionsOf snapshots a positioned layout down to a node id → {x,y} map — the
 * minimal state "freeze layout" pins (positions only, never the stale label /
 * radius / failure_ratio). reapplyFrozenPositions later re-applies fresh data
 * onto these coordinates (codex P2#5).
 */
export function positionsOf(positioned: PositionedNode[]): Map<string, { x: number; y: number }> {
  return new Map(positioned.map((p) => [p.node.id, { x: p.x, y: p.y }]));
}
