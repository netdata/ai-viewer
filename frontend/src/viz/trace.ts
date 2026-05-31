import { scaleLinear } from 'd3-scale';
import { ticks as d3Ticks } from 'd3-array';
import type { OpDetail, TurnDetail } from '../api/types';

// Pure geometry/layout for the Trace tab (ui-pages.md §/sessions/:id #3 Trace).
// Lives in viz/ so React components consume plain data and never import D3
// directly (project-frontend §D3 Patterns). Two renderings of the same op tree:
//  - waterfall: one row per op on a shared time axis (Chrome-DevTools-Network
//    style), nested by parent → children → child-session transitions;
//  - flame-graph: stacked spans by depth.
//
// The wire opDetail carries NO explicit parent_op_id
// (internal/presenter/session_detail.go), so parent/child nesting is derived
// from TEMPORAL CONTAINMENT — the canonical derivation for APM waterfalls
// without explicit span links: op B is a child of the tightest enclosing op A
// whose closed window [start,end] contains B's start. A child_session_id op is
// a cross-session boundary and is always a LEAF in this session's tree (the
// child session's ops are fetched and rendered separately).

/** TraceNode is one op plus its derived tree position. */
export interface TraceNode {
  op: OpDetail;
  depth: number;
  children: TraceNode[];
}

// Internal sortable record carrying tie-break keys (turn seq, original order).
interface Indexed {
  op: OpDetail;
  turnSeq: number;
  order: number;
}

/** end returns an op's closed end, or null when it is still ongoing. */
function closedEnd(op: OpDetail): number | null {
  return op.end_ts !== null && op.end_ts >= op.start_ts ? op.end_ts : null;
}

/** A session-transition op (spawns a child session) is a leaf in this tree. */
function isLeafBoundary(op: OpDetail): boolean {
  return op.child_session_id !== null;
}

/**
 * buildOpTree flattens all turns' ops and nests them by temporal containment.
 * Siblings (and roots) are ordered by start_ts, then turn seq, then original
 * op order — a total, deterministic ordering.
 */
export function buildOpTree(turns: TurnDetail[]): TraceNode[] {
  const items: Indexed[] = [];
  let order = 0;
  for (const turn of turns) {
    for (const op of turn.ops) {
      items.push({ op, turnSeq: turn.seq, order: order++ });
    }
  }
  if (items.length === 0) {
    return [];
  }

  items.sort(
    (a, b) =>
      a.op.start_ts - b.op.start_ts || a.turnSeq - b.turnSeq || a.order - b.order,
  );

  const roots: TraceNode[] = [];
  // Stack of open ancestors (closed windows that may still contain the next op).
  const stack: { node: TraceNode; end: number }[] = [];

  for (const item of items) {
    const node: TraceNode = { op: item.op, depth: 0, children: [] };
    // Pop ancestors whose window ends before this op starts.
    while (stack.length > 0 && (stack[stack.length - 1] as { end: number }).end < item.op.start_ts) {
      stack.pop();
    }
    const parent = stack[stack.length - 1];
    if (parent) {
      node.depth = parent.node.depth + 1;
      parent.node.children.push(node);
    } else {
      roots.push(node);
    }
    // This op can itself contain later ops only if it has a closed window AND
    // it is not a cross-session boundary (which is always a leaf).
    const e = closedEnd(item.op);
    if (e !== null && !isLeafBoundary(item.op)) {
      stack.push({ node, end: e });
    }
  }

  return roots;
}

/** flattenTree returns a depth-first pre-order list (== visual row order). */
export function flattenTree(roots: TraceNode[]): TraceNode[] {
  const out: TraceNode[] = [];
  const walk = (nodes: TraceNode[]): void => {
    for (const n of nodes) {
      out.push(n);
      walk(n.children);
    }
  };
  walk(roots);
  return out;
}

/**
 * traceTimeBounds returns [min start, max end] across the flattened nodes. A
 * null end_ts contributes its start_ts to the upper bound (the op's known
 * extent is preserved without the server synthesizing an end — rest-api.md
 * §timeline). Returns [0,0] for an empty tree.
 */
export function traceTimeBounds(nodes: TraceNode[]): [number, number] {
  if (nodes.length === 0) {
    return [0, 0];
  }
  let lo = Infinity;
  let hi = -Infinity;
  for (const { op } of nodes) {
    if (op.start_ts < lo) {
      lo = op.start_ts;
    }
    const end = closedEnd(op) ?? op.start_ts;
    if (end > hi) {
      hi = end;
    }
  }
  return [lo, hi];
}

/**
 * SVG_SPAN_CEILING is the op-count above which the Trace renderings switch from
 * SVG (one DOM node per span — fine for typical sessions, and inspectable) to
 * Canvas with viewport culling (frontend-architecture.md §Performance Budgets:
 * Canvas above the visible-span ceiling; the 200 ms render budget holds). Kept
 * a touch below the Timeline's 500-span Canvas threshold because a waterfall
 * row draws a bar + a text label + axis gridlines, so its per-row DOM cost is
 * higher than a bare timeline bar.
 */
export const SVG_SPAN_CEILING = 400;

// ── Waterfall ────────────────────────────────────────────────────────────────

export interface WaterfallOpts {
  /** Pixel width of the time track. */
  width: number;
  /** Pixel height of one row. */
  rowHeight: number;
  /** Window start (µs). */
  t0: number;
  /** Window end (µs). */
  t1: number;
  /** Minimum bar width in px so a tiny op stays clickable (default 2). */
  minBarWidth?: number;
}

export interface WaterfallRow {
  node: TraceNode;
  rowIndex: number;
  depth: number;
  /** Bar geometry within the time track. */
  x: number;
  width: number;
  /** Row vertical position. */
  y: number;
  height: number;
}

/**
 * layoutWaterfall maps each op onto a horizontal bar positioned by start_ts and
 * sized by duration on the shared [t0,t1] → [0,width] scale, and stacks the rows
 * vertically by their pre-order index. A null-end (ongoing) op extends to t1. A
 * zero-width window is handled without dividing by zero (every bar collapses to
 * x=0). Bars are clamped to minBarWidth so a zero-duration op stays visible.
 */
export function layoutWaterfall(nodes: TraceNode[], opts: WaterfallOpts): WaterfallRow[] {
  const { width, rowHeight, t0, t1 } = opts;
  const minBarWidth = opts.minBarWidth ?? 2;
  const x = scaleLinear().domain([t0, t1 === t0 ? t0 + 1 : t1]).range([0, width]);

  return nodes.map((node, rowIndex) => {
    const start = node.op.start_ts;
    const end = closedEnd(node.op) ?? t1;
    const x0 = x(start);
    const x1 = x(end);
    const w = Math.max(minBarWidth, x1 - x0);
    return {
      node,
      rowIndex,
      depth: node.depth,
      x: x0,
      width: w,
      y: rowIndex * rowHeight,
      height: rowHeight,
    };
  });
}

// ── Flame graph ──────────────────────────────────────────────────────────────

export interface FlameOpts {
  /** Pixel width of the full (root) frame. */
  width: number;
  /** Pixel height of one depth band. */
  rowHeight: number;
  /** Minimum cell width in px (default 1). */
  minCellWidth?: number;
}

export interface FlameCell {
  node: TraceNode;
  depth: number;
  x: number;
  width: number;
  y: number;
  height: number;
}

/** span returns an op's duration for flame sizing (closed window or 0). */
function flameSpan(op: OpDetail): number {
  const e = closedEnd(op);
  return e === null ? 0 : Math.max(0, e - op.start_ts);
}

/**
 * layoutFlame produces the stacked-by-depth flame (icicle) layout. The root
 * band spans the full width across the whole trace window; every cell is then
 * placed by its TIME WINDOW scaled into its parent's pixel box — width =
 * (childSpan / parentSpan) × parentWidth, x = parent.x + (childStart −
 * parentStart)/parentSpan × parentWidth. This is the true APM flame model:
 * a child sits where it actually occurred under its parent, idle gaps are left
 * blank, and siblings never overlap because their time windows do not. Roots
 * (depth 0) share the full trace window so a multi-root tree stays comparable.
 * A zero-span parent collapses its subtree to minCellWidth (avoids /0). y is
 * depth × rowHeight.
 */
export function layoutFlame(roots: TraceNode[], opts: FlameOpts): FlameCell[] {
  if (roots.length === 0) {
    return [];
  }
  const { width, rowHeight } = opts;
  const minCellWidth = opts.minCellWidth ?? 1;
  const cells: FlameCell[] = [];

  // Trace window anchors depth-0 placement so multiple roots share one axis.
  const flat = flattenTree(roots);
  const [traceStart, traceEnd] = traceTimeBounds(flat);
  const traceSpan = traceEnd > traceStart ? traceEnd - traceStart : 1;

  // Place `node` into the pixel box [boxX, boxX+boxW] that represents the time
  // window [winStart, winStart+winSpan]; then recurse into its children using
  // this cell's own box+window.
  const place = (
    node: TraceNode,
    boxX: number,
    boxW: number,
    winStart: number,
    winSpan: number,
    depth: number,
  ): void => {
    const start = node.op.start_ts;
    const span = flameSpan(node.op);
    const safeWin = winSpan > 0 ? winSpan : 1;
    const x = boxX + ((start - winStart) / safeWin) * boxW;
    const w = Math.max(minCellWidth, (span / safeWin) * boxW);
    cells.push({ node, depth, x, width: w, y: depth * rowHeight, height: rowHeight });
    const childSpan = span > 0 ? span : safeWin;
    for (const child of node.children) {
      place(child, x, w, start, childSpan, depth + 1);
    }
  };

  for (const root of roots) {
    place(root, 0, width, traceStart, traceSpan, 0);
  }
  return cells;
}

// ── Viewport culling / windowing ─────────────────────────────────────────────

/**
 * cullByY returns only the waterfall rows whose vertical band overlaps the
 * visible viewport [scrollTop, scrollTop+viewportHeight], plus `overscan` rows
 * on each side (clamped to the ends). This is the Canvas-path optimization: a
 * 10k-row trace only ever draws the few dozen rows actually on screen, so a big
 * trace stays fast (the operator explicitly wants big-but-fast). Rows are
 * assumed uniform-height and in row order (as layoutWaterfall emits them).
 */
export function cullByY(
  rows: WaterfallRow[],
  scrollTop: number,
  viewportHeight: number,
  rowHeight: number,
  overscan: number,
): WaterfallRow[] {
  if (rows.length === 0 || rowHeight <= 0) {
    return rows;
  }
  const first = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  // Last row whose top is strictly above the viewport bottom edge.
  const bottomRow = Math.ceil((scrollTop + viewportHeight) / rowHeight) - 1;
  const last = Math.min(rows.length - 1, bottomRow + overscan);
  return rows.slice(first, last + 1);
}

export interface WindowSlice {
  start: number;
  end: number;
}

/**
 * windowRange computes the [start,end) index slice of a uniform-height list to
 * render for a given scroll position — the simple windowing the event list uses
 * for the 10k-op case (only the visible rows are mounted; the list's total
 * height is preserved by spacer rows). Clamped to [0,total].
 */
export function windowRange(
  total: number,
  rowHeight: number,
  scrollTop: number,
  viewportHeight: number,
  overscan: number,
): WindowSlice {
  if (total === 0 || rowHeight <= 0) {
    return { start: 0, end: total };
  }
  const first = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const bottomRow = Math.ceil((scrollTop + viewportHeight) / rowHeight) - 1;
  const end = Math.min(total, bottomRow + overscan + 1);
  // An out-of-range scroll (start past content) degrades to the whole list
  // rather than an empty window, so the list never renders blank.
  if (first >= end) {
    return { start: 0, end: total };
  }
  return { start: first, end };
}

// ── Time axis ────────────────────────────────────────────────────────────────

export interface AxisTick {
  /** Tick value in µs (relative to the same domain as the layout). */
  value: number;
  /** Pixel x within the time track. */
  x: number;
}

/**
 * timeAxisTicks returns evenly-spaced axis ticks for [t0,t1] over [0,width].
 * Uses d3-array's tick algorithm for human-friendly steps. A zero-width window
 * returns a single tick at t0.
 */
export function timeAxisTicks(
  t0: number,
  t1: number,
  width: number,
  targetCount: number,
): AxisTick[] {
  if (t1 <= t0) {
    return [{ value: t0, x: 0 }];
  }
  const x = scaleLinear().domain([t0, t1]).range([0, width]);
  const values = d3Ticks(t0, t1, targetCount);
  // Keep only ticks strictly inside the track so the first/last labels never
  // clip at the edges; if the algorithm yields none, fall back to the bounds.
  const inside = values.filter((v) => v > t0 && v < t1);
  const chosen = inside.length > 0 ? inside : [t0, t1];
  return chosen.map((value) => ({ value, x: x(value) }));
}
