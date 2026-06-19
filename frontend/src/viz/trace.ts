import { scaleLinear } from 'd3-scale';
import { ticks as d3Ticks } from 'd3-array';
import type { OpDetail, TraceOp, TurnDetail } from '../api/types';

// Pure geometry/layout for the Trace tab (ui-pages.md §/sessions/:id #3 Trace).
// Lives in viz/ so React components consume plain data and never import D3
// directly (project-frontend §D3 Patterns). Two renderings of the same op tree:
//  - waterfall: one row per op on a shared time axis (Chrome-DevTools-Network
//    style), nested by parent → children → child-session transitions;
//  - flame-graph: stacked spans by depth.
//
// Nesting is the AUTHORITATIVE parentage the wire opDetail carries in
// `parent_op_id` (ops.parent_op_id; internal/presenter/session_detail.go always
// emits the key, value or null). Op B is a child of op A iff
// B.parent_op_id === A.id; a null/absent/dangling parent_op_id makes B a
// top-level (root) op within the session. This is the span-link model APM tools
// use — it does NOT infer nesting from time, so a point event
// (end_ts == start_ts, e.g. a claude-code LLM/reasoning message) can never
// become a FALSE ancestor of a later op the way temporal containment did.
// A child_session_id op is a cross-session boundary and is always a LEAF in this
// session's tree (the child session's ops are fetched and rendered separately);
// any op that names a boundary as its parent is hoisted to top-level.

/** TraceNode is one op plus its derived tree position. The session tags are
 *  populated only by buildMergedTree (whole-tree trace, SOW-0070); the
 *  single-session buildOpTree leaves them undefined. */
export interface TraceNode {
  op: OpDetail;
  depth: number;
  children: TraceNode[];
  /** Owning session id (whole-tree trace only). */
  sessionId?: string;
  /** Owning session agent name (whole-tree trace only; drives the sub-agent
   *  color + filter). */
  sessionAgent?: string;
}

/** end returns an op's closed end, or null when it is still ongoing/instant. */
function closedEnd(op: OpDetail): number | null {
  return op.end_ts !== null && op.end_ts > op.start_ts ? op.end_ts : null;
}

/**
 * isInstantOp is true when an op has no measured forward window — a null end_ts
 * (running / unrecorded) or a point event (end_ts == start_ts, e.g. a
 * claude-code LLM/reasoning message recorded at one timestamp). Such ops draw as
 * an instant tick/marker, never a zero-width bar (ui-pages.md §Trace
 * source-aware rendering; mirrors viz/timeline.isInstant).
 */
export function isInstantOp(op: OpDetail): boolean {
  return op.end_ts === null || op.end_ts <= op.start_ts;
}

/** A session-transition op (spawns a child session) is a leaf in this tree. */
function isLeafBoundary(op: OpDetail): boolean {
  return op.child_session_id !== null;
}

/**
 * buildOpTree flattens all turns' ops and nests them by the authoritative
 * `parent_op_id` parentage. Siblings (and roots) are ordered by start_ts, then
 * turn seq, then original op order — a total, deterministic ordering.
 *
 * Edge cases (all → top-level): a null/absent parent_op_id; a parent_op_id that
 * references an unknown id (dangling); a parent_op_id that points at a
 * cross-session boundary op (which is a leaf in this tree). A boundary op never
 * receives children regardless of what points at it.
 *
 * Cycles: the schema enforces only an FK on parent_op_id, not acyclicity
 * (data-model.md), so corrupt/adversarial data can form a parent_op_id cycle
 * (A→B→A). In a closed cycle no member is a root, so a naive attach pass would
 * leave every member attached-but-unreachable and silently drop it from the
 * returned tree. "No silent failures" (AGENTS.md) forbids that: a reachability
 * pass below hoists every unreachable node to a root, breaking the cycle
 * deterministically so every input op always appears exactly once.
 */
export function buildOpTree(turns: TurnDetail[]): TraceNode[] {
  // First pass: materialize a node per op, preserving (turnSeq, order) for a
  // deterministic sibling sort, and index by op id for parent resolution.
  const nodes: TraceNode[] = [];
  const byId = new Map<string, TraceNode>();
  const keys = new Map<string, { turnSeq: number; order: number }>();
  let order = 0;
  for (const turn of turns) {
    for (const op of turn.ops) {
      const node: TraceNode = { op, depth: 0, children: [] };
      nodes.push(node);
      // A duplicate op id should not happen, but if it did the first wins as the
      // parent target (deterministic) — later ones still render as their own row.
      if (!byId.has(op.id)) {
        byId.set(op.id, node);
      }
      keys.set(op.id, { turnSeq: turn.seq, order: order++ });
    }
  }
  if (nodes.length === 0) {
    return [];
  }

  // Second pass: attach each node to its parent, or collect it as a root. A
  // parent that is a cross-session boundary cannot own children (it is a leaf),
  // so such a child is hoisted to top-level.
  const roots: TraceNode[] = [];
  for (const node of nodes) {
    const parentId = node.op.parent_op_id;
    const parent = parentId != null ? byId.get(parentId) : undefined;
    if (parent && parent !== node && !isLeafBoundary(parent.op)) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }

  // Cycle break: a closed parent_op_id cycle (A→B→A) has no root, so its members
  // were attached to each other above and are unreachable from `roots` — they
  // would silently vanish from the returned tree. Walk down from the current
  // roots marking every reachable node (single visited-set DFS, O(nodes) since
  // each node is enqueued once); any node still unvisited is a cycle member (or
  // sits under one) and is hoisted to a root so it is never dropped. `nodes` is
  // in stable (turnSeq, order) order, so the first unreachable member of each
  // cycle is hoisted first — deterministic, no Math.random. The sibling sort
  // below still orders the hoisted roots by start_ts/turnSeq/order.
  const reachable = new Set<TraceNode>();
  const stack = [...roots];
  while (stack.length > 0) {
    const n = stack.pop() as TraceNode;
    if (reachable.has(n)) {
      continue;
    }
    reachable.add(n);
    for (const child of n.children) {
      stack.push(child);
    }
  }
  for (const node of nodes) {
    if (!reachable.has(node)) {
      // Detach from the parent that swallowed it, then promote to a root.
      const parentId = node.op.parent_op_id;
      const parent = parentId != null ? byId.get(parentId) : undefined;
      if (parent) {
        const idx = parent.children.indexOf(node);
        if (idx !== -1) {
          parent.children.splice(idx, 1);
        }
      }
      roots.push(node);
      // Mark reachable so a downstream cycle member that links to this node is
      // not hoisted redundantly (it stays a descendant of this new root).
      reachable.add(node);
      stack.push(node);
      while (stack.length > 0) {
        const n = stack.pop() as TraceNode;
        for (const child of n.children) {
          if (!reachable.has(child)) {
            reachable.add(child);
            stack.push(child);
          }
        }
      }
    }
  }

  // Third pass: order siblings deterministically and assign depth top-down.
  const sortKey = (n: TraceNode): [number, number, number] => {
    const k = keys.get(n.op.id) ?? { turnSeq: 0, order: 0 };
    return [n.op.start_ts, k.turnSeq, k.order];
  };
  const cmp = (a: TraceNode, b: TraceNode): number => {
    const ka = sortKey(a);
    const kb = sortKey(b);
    return ka[0] - kb[0] || ka[1] - kb[1] || ka[2] - kb[2];
  };
  const assign = (siblings: TraceNode[], depth: number): void => {
    siblings.sort(cmp);
    for (const n of siblings) {
      n.depth = depth;
      if (n.children.length > 0) {
        assign(n.children, depth + 1);
      }
    }
  };
  assign(roots, 0);

  return roots;
}

/**
 * buildMergedTree builds ONE op tree spanning sub-session boundaries from the
 * whole-tree trace op list (SOW-0070). Within each session, ops nest by
 * parent_op_id (the same authoritative parentage buildOpTree uses); an op
 * carrying child_session_id=C is a leaf in its session's tree, and the op-roots
 * of session C splice under it — so a sub-session's work nests beneath the op
 * that spawned it.
 *
 * Defenses: a child_session_id whose session is absent from the op set stays a
 * leaf (nothing to splice); a cross-session parent cycle cannot form because
 * the splice direction is strictly session→its parent's boundary op (a session
 * is never spliced under its own descendant op). Each node is tagged with its
 * sessionId/sessionAgent so the client colors + filters by sub-agent.
 *
 * Per-session cycle handling mirrors buildOpTree: a closed parent_op_id cycle
 * within a session has no root, so a reachability+hoist pass (run per-session,
 * before the splice) promotes its FIRST member to a root and keeps the rest
 * nested under it (inner re-DFS) — no node is ever DROPPED or DUPLICATED, and
 * the cycle's nesting is preserved (one root), the same contract the
 * single-session trace has.
 */
export function buildMergedTree(ops: TraceOp[]): TraceNode[] {
  if (ops.length === 0) {
    return [];
  }

  // Per-session: materialize nodes (preserving feed order) + index by op id
  // WITHIN the session. parent_op_id is scoped to a session (two sessions may
  // reuse an op id), so the byId map is keyed by (sessionId, opId).
  const byKey = new Map<string, TraceNode>();
  const sessionOps = new Map<string, TraceNode[]>();
  for (const op of ops) {
    const node: TraceNode = {
      op,
      depth: 0,
      children: [],
      sessionId: op.session_id,
      sessionAgent: op.session_agent_name,
    };
    byKey.set(`${op.session_id}\0${op.id}`, node);
    const list = sessionOps.get(op.session_id);
    if (list) {
      list.push(node);
    } else {
      sessionOps.set(op.session_id, [node]);
    }
  }

  // Per-session roots: attach each node to its parent_op_id WITHIN its session,
  // or collect as a root. A child_session_id boundary is a leaf (isLeafBoundary)
  // — its sub-session's ops splice in the next pass.
  const sessionRoots = new Map<string, TraceNode[]>();
  for (const [sid, nodes] of sessionOps) {
    const roots: TraceNode[] = [];
    for (const node of nodes) {
      const parentId = node.op.parent_op_id;
      const parent = parentId != null ? byKey.get(`${sid}\0${parentId}`) : undefined;
      if (parent && parent !== node && !isLeafBoundary(parent.op)) {
        parent.children.push(node);
      } else {
        roots.push(node);
      }
    }
    // Cycle break (mirrors buildOpTree): a closed parent_op_id cycle within the
    // session (A→B→A) has no root, so its members were attached to each other
    // above and are unreachable from `roots` — they would silently vanish. Walk
    // down from the current roots marking every reachable node; any node still
    // unvisited is a cycle member (or sits under one) and is hoisted to a root
    // so it is never dropped. `nodes` is in stable feed order, so the first
    // unreachable member of each cycle hoists first — deterministic.
    const reachable = new Set<TraceNode>();
    const stack = [...roots];
    while (stack.length > 0) {
      const n = stack.pop() as TraceNode;
      if (reachable.has(n)) {
        continue;
      }
      reachable.add(n);
      for (const child of n.children) {
        stack.push(child);
      }
    }
    for (const node of nodes) {
      if (!reachable.has(node)) {
        // Detach from the parent that swallowed it, then promote to a root.
        const parentId = node.op.parent_op_id;
        const parent = parentId != null ? byKey.get(`${sid}\0${parentId}`) : undefined;
        if (parent) {
          const idx = parent.children.indexOf(node);
          if (idx >= 0) {
            parent.children.splice(idx, 1);
          }
        }
        roots.push(node);
        // Mark this hoisted root AND its descendants reachable (inner DFS), so a
        // later cycle member that is a descendant of this new root is NOT hoisted
        // redundantly — it stays nested under it, preserving the tree shape
        // (faithful port of buildOpTree's cycle-break; without this, a 3+ member
        // cycle would flatten to separate roots instead of nesting).
        reachable.add(node);
        const hoistStack = [node];
        while (hoistStack.length > 0) {
          const n = hoistStack.pop() as TraceNode;
          for (const child of n.children) {
            if (!reachable.has(child)) {
              reachable.add(child);
              hoistStack.push(child);
            }
          }
        }
      }
    }
    sessionRoots.set(sid, roots);
  }

  // Cross-session splice: for every boundary op (child_session_id set), attach
  // the named session's roots under it. A session is only spliced under a
  // boundary op that LIVES IN A DIFFERENT session (a session cannot be its own
  // ancestor), so no cross-session cycle can form.
  for (const [, nodes] of sessionOps) {
    for (const node of nodes) {
      const childSid = node.op.child_session_id;
      if (childSid === null || childSid === node.sessionId) {
        continue;
      }
      const childRoots = sessionRoots.get(childSid);
      if (childRoots && childRoots.length > 0) {
        node.children.push(...childRoots);
      }
    }
  }

  // The whole-tree roots = the root session's roots. The root session is the
  // one whose id no boundary op points at as a child (i.e. it is never spliced
  // under another session). Compute the set of sessions that ARE spliced under
  // a boundary op; the top-level forest is the roots of every session NOT in
  // that set. (For a well-formed single-root tree this is exactly the root
  // session; the forest form tolerates multiple roots.)
  const spliced = new Set<string>();
  for (const [, nodes] of sessionOps) {
    for (const node of nodes) {
      const childSid = node.op.child_session_id;
      if (childSid !== null && childSid !== node.sessionId && sessionOps.has(childSid)) {
        spliced.add(childSid);
      }
    }
  }
  const forest: TraceNode[] = [];
  for (const [sid, roots] of sessionRoots) {
    if (!spliced.has(sid)) {
      forest.push(...roots);
    }
  }

  // Re-assign depth across the merged tree (splice changed child depths).
  const assign = (siblings: TraceNode[], depth: number): void => {
    for (const n of siblings) {
      n.depth = depth;
      if (n.children.length > 0) {
        assign(n.children, depth + 1);
      }
    }
  };
  assign(forest, 0);

  return forest;
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
  /** True for a point-event/running op: the renderer draws a tick, not a bar
   *  (source-aware rendering, ui-pages.md §Trace). */
  instant: boolean;
}

/**
 * layoutWaterfall maps each op onto a horizontal bar positioned by start_ts and
 * sized by duration on the shared [t0,t1] → [0,width] scale, and stacks the rows
 * vertically by their pre-order index. A measured op (closed forward window) is a
 * bar; a point-event/running op is flagged `instant` so the renderer paints a
 * tick at its start_ts instead of a zero-width bar. A null-end (ongoing) op's bar
 * geometry extends to t1 (used only if a caller chooses to draw it as a bar). A
 * zero-width window is handled without dividing by zero (every bar collapses to
 * x=0). Bars are clamped to minBarWidth so a tiny op stays visible.
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
      instant: isInstantOp(node.op),
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
  /** True for a point-event/running op: the renderer draws a tick, not a cell
   *  (source-aware rendering, ui-pages.md §Trace). */
  instant: boolean;
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
    cells.push({
      node,
      depth,
      x,
      width: w,
      y: depth * rowHeight,
      height: rowHeight,
      instant: isInstantOp(node.op),
    });
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

// ── By-turn rollup ───────────────────────────────────────────────────────────

/**
 * TurnRollup is one turn aggregated into a single waterfall bar (ui-pages.md
 * §Trace "By-turn" view): the bar spans the turn's first op start → last op end,
 * carries the op count, and keeps the turn's ops (as pre-order TraceNodes) so a
 * click can expand the turn into its individual ops without re-deriving the tree.
 */
export interface TurnRollup {
  turn: TurnDetail;
  /** Earliest op start in the turn (µs). */
  start_ts: number;
  /** Latest measured op end in the turn (µs), or null when no op closed — drawn
   *  as ongoing/instant, mirroring a null-end op. */
  end_ts: number | null;
  op_count: number;
  /** The turn's ops in pre-order (the same tree buildOpTree derives, scoped to
   *  this turn) for expand-on-click. */
  ops: TraceNode[];
}

/**
 * buildTurnRollup aggregates each turn into one TurnRollup. The bounds come from
 * the turn's OPS (not the turn header) so the bar matches what the Detailed view
 * draws: start = min op start_ts, end = max op closed end (null if no op closed).
 * Turns with no ops are skipped (nothing to draw). The per-turn op tree is built
 * the same way as the whole-session tree, so expand-on-click shows identical
 * nesting and ordering.
 */
export function buildTurnRollup(turns: TurnDetail[]): TurnRollup[] {
  const out: TurnRollup[] = [];
  for (const turn of turns) {
    if (turn.ops.length === 0) {
      continue;
    }
    let lo = Infinity;
    let hi: number | null = null;
    for (const op of turn.ops) {
      if (op.start_ts < lo) {
        lo = op.start_ts;
      }
      const e = closedEnd(op);
      if (e !== null && (hi === null || e > hi)) {
        hi = e;
      }
    }
    out.push({
      turn,
      start_ts: lo,
      end_ts: hi,
      op_count: turn.ops.length,
      ops: flattenTree(buildOpTree([turn])),
    });
  }
  return out;
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
