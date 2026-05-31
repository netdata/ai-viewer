import { describe, expect, it } from 'vitest';
import type { OpDetail, TurnDetail } from '../api/types';
import {
  buildOpTree,
  flattenTree,
  traceTimeBounds,
  isInstantOp,
  layoutWaterfall,
  layoutFlame,
  buildTurnRollup,
  timeAxisTicks,
  cullByY,
  windowRange,
  SVG_SPAN_CEILING,
  type TraceNode,
  type TurnRollup,
  type WaterfallRow,
} from './trace';

// viz/trace.ts holds the PURE geometry/layout for the Trace tab renderings,
// separated from React (the viz/ boundary — project-frontend skill §D3 Patterns).
// Given the session op tree it computes (a) waterfall rows on a shared time
// axis and (b) flame-graph cells stacked by depth. Nesting is the AUTHORITATIVE
// ops.parent_op_id parentage carried on the wire opDetail
// (internal/presenter/session_detail.go) — NOT temporal containment — so a
// point event (end_ts == start_ts) can never become a false ancestor of a later
// op. child_session_id marks a cross-session transition; such ops are leaves in
// THIS session's tree (the child's ops load separately). These tests pin the
// geometry deterministically from a fixture op tree.

function op(over: Partial<OpDetail>): OpDetail {
  return {
    id: 'op',
    kind: 'tool',
    name: 'n',
    model: '',
    provider: '',
    start_ts: 0,
    end_ts: 0,
    duration_us: 0,
    status: 'completed',
    error_class: null,
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    ctx_used: null,
    ctx_max: null,
    child_session_id: null,
    payload_refs: [],
    ...over,
  };
}

function turn(seq: number, ops: OpDetail[]): TurnDetail {
  const start = ops.length > 0 ? Math.min(...ops.map((o) => o.start_ts)) : 0;
  return {
    id: `t${seq}`,
    seq,
    start_ts: start,
    end_ts: null,
    status: 'completed',
    tokens_in: 0,
    tokens_out: 0,
    cost_usd: 0,
    op_count: ops.length,
    ops,
  };
}

// A fixture tree built from AUTHORITATIVE parent_op_id parentage: a root agent
// op holds an llm op and a tool op; the tool op holds a nested internal op.
// (Timestamps are kept realistic but are NOT what determines nesting.)
function fixtureTurns(): TurnDetail[] {
  return [
    turn(1, [
      op({ id: 'root', kind: 'session', start_ts: 0, end_ts: 1000, duration_us: 1000, parent_op_id: null }),
      op({ id: 'llm', kind: 'llm', start_ts: 100, end_ts: 400, duration_us: 300, parent_op_id: 'root' }),
      op({ id: 'tool', kind: 'tool', start_ts: 500, end_ts: 900, duration_us: 400, parent_op_id: 'root' }),
      op({ id: 'inner', kind: 'internal', start_ts: 600, end_ts: 700, duration_us: 100, parent_op_id: 'tool' }),
    ]),
  ];
}

describe('buildOpTree', () => {
  it('nests ops by the authoritative parent_op_id', () => {
    const roots = buildOpTree(fixtureTurns());
    expect(roots.map((r) => r.op.id)).toEqual(['root']);
    const root = roots[0] as TraceNode;
    expect(root.depth).toBe(0);
    // root's direct children are llm and tool (inner is under tool).
    expect(root.children.map((c) => c.op.id)).toEqual(['llm', 'tool']);
    const tool = root.children.find((c) => c.op.id === 'tool') as TraceNode;
    expect(tool.depth).toBe(1);
    expect(tool.children.map((c) => c.op.id)).toEqual(['inner']);
    expect((tool.children[0] as TraceNode).depth).toBe(2);
  });

  it('builds a 3-level tree purely from parent_op_id, ignoring timestamps', () => {
    // grandchild starts BEFORE its parent in wall-clock time; pure parent_op_id
    // nesting must still place it under the parent (no temporal reasoning).
    const turns = [
      turn(1, [
        op({ id: 'a', start_ts: 100, end_ts: 900, duration_us: 800, parent_op_id: null }),
        op({ id: 'b', start_ts: 200, end_ts: 800, duration_us: 600, parent_op_id: 'a' }),
        op({ id: 'c', start_ts: 50, end_ts: 60, duration_us: 10, parent_op_id: 'b' }),
      ]),
    ];
    const flat = flattenTree(buildOpTree(turns));
    expect(flat.map((n) => n.op.id)).toEqual(['a', 'b', 'c']);
    expect(flat.map((n) => n.depth)).toEqual([0, 1, 2]);
  });

  it('does NOT let a point event (end_ts == start_ts) become an ancestor', () => {
    // P1#1: the old temporal-containment builder made a zero-duration op a false
    // ancestor of a later op. With parent_op_id nesting, a point event with no
    // declared children is a LEAF and the later op is a sibling root.
    const turns = [
      turn(1, [
        op({ id: 'point', kind: 'llm', start_ts: 500, end_ts: 500, duration_us: null, parent_op_id: null }),
        op({ id: 'later', kind: 'tool', start_ts: 600, end_ts: 900, duration_us: 300, parent_op_id: null }),
      ]),
    ];
    const roots = buildOpTree(turns);
    const point = roots.find((r) => r.op.id === 'point') as TraceNode;
    expect(point.children).toHaveLength(0);
    // 'later' is a top-level sibling, NOT nested under the point event.
    expect(roots.map((r) => r.op.id)).toEqual(['point', 'later']);
  });

  it('orders siblings by start_ts then turn seq then op order', () => {
    const turns = [
      turn(1, [op({ id: 'b', start_ts: 200, end_ts: 300, duration_us: 100, parent_op_id: null })]),
      turn(2, [op({ id: 'a', start_ts: 100, end_ts: 150, duration_us: 50, parent_op_id: null })]),
    ];
    const roots = buildOpTree(turns);
    // 'a' starts earlier so it sorts first despite being in a later turn.
    expect(roots.map((r) => r.op.id)).toEqual(['a', 'b']);
  });

  it('orders children of a parent by start_ts', () => {
    const turns = [
      turn(1, [
        op({ id: 'p', start_ts: 0, end_ts: 1000, duration_us: 1000, parent_op_id: null }),
        op({ id: 'late', start_ts: 700, end_ts: 800, duration_us: 100, parent_op_id: 'p' }),
        op({ id: 'early', start_ts: 100, end_ts: 200, duration_us: 100, parent_op_id: 'p' }),
      ]),
    ];
    const p = buildOpTree(turns)[0] as TraceNode;
    expect(p.children.map((c) => c.op.id)).toEqual(['early', 'late']);
  });

  it('treats a child_session_id op as a leaf and reparents any declared child to top-level', () => {
    // A cross-session boundary op is a LEAF in this session's tree (the child
    // session's ops load separately). An op that names the boundary as its
    // parent is hoisted to top-level rather than nested under the boundary.
    const turns = [
      turn(1, [
        op({ id: 'spawn', kind: 'session', start_ts: 0, end_ts: 1000, duration_us: 1000, child_session_id: 'child-1', parent_op_id: null }),
        op({ id: 'sibling', kind: 'tool', start_ts: 200, end_ts: 400, duration_us: 200, parent_op_id: 'spawn' }),
      ]),
    ];
    const roots = buildOpTree(turns);
    const spawn = roots.find((r) => r.op.id === 'spawn') as TraceNode;
    expect(spawn.children).toHaveLength(0);
    // 'sibling' is hoisted to a root, not a child of the transition op.
    expect(roots.map((r) => r.op.id)).toContain('sibling');
  });

  it('treats a null/absent parent_op_id as top-level', () => {
    const turns = [
      turn(1, [
        op({ id: 'ongoing', kind: 'llm', start_ts: 0, end_ts: null, duration_us: null, parent_op_id: null }),
        // parent_op_id absent entirely (predates the Trace view) → top-level.
        op({ id: 'after', kind: 'tool', start_ts: 100, end_ts: 200, duration_us: 100 }),
      ]),
    ];
    const roots = buildOpTree(turns);
    const ongoing = roots.find((r) => r.op.id === 'ongoing') as TraceNode;
    expect(ongoing.children).toHaveLength(0);
    expect(roots.map((r) => r.op.id)).toEqual(['ongoing', 'after']);
  });

  it('hoists an op whose parent_op_id references an unknown/dangling id to top-level', () => {
    const turns = [
      turn(1, [op({ id: 'orphan', start_ts: 100, end_ts: 200, duration_us: 100, parent_op_id: 'missing' })]),
    ];
    const roots = buildOpTree(turns);
    expect(roots.map((r) => r.op.id)).toEqual(['orphan']);
    expect((roots[0] as TraceNode).depth).toBe(0);
  });

  it('returns an empty array for no turns/ops', () => {
    expect(buildOpTree([])).toEqual([]);
    expect(buildOpTree([turn(1, [])])).toEqual([]);
  });
});

describe('isInstantOp', () => {
  it('is true for a null end_ts (running / unmeasured)', () => {
    expect(isInstantOp(op({ start_ts: 100, end_ts: null }))).toBe(true);
  });

  it('is true for a point event (end_ts == start_ts)', () => {
    expect(isInstantOp(op({ start_ts: 100, end_ts: 100 }))).toBe(true);
  });

  it('is false for a measured span (end_ts > start_ts)', () => {
    expect(isInstantOp(op({ start_ts: 100, end_ts: 400 }))).toBe(false);
  });
});

describe('flattenTree', () => {
  it('produces a depth-first pre-order list matching the visual row order', () => {
    const roots = buildOpTree(fixtureTurns());
    const flat = flattenTree(roots);
    expect(flat.map((n) => n.op.id)).toEqual(['root', 'llm', 'tool', 'inner']);
    expect(flat.map((n) => n.depth)).toEqual([0, 1, 1, 2]);
  });
});

describe('traceTimeBounds', () => {
  it('returns [min start, max end] across all ops', () => {
    const flat = flattenTree(buildOpTree(fixtureTurns()));
    expect(traceTimeBounds(flat)).toEqual([0, 1000]);
  });

  it('counts a null end_ts as its start_ts for the upper bound', () => {
    const turns = [
      turn(1, [
        op({ id: 'a', start_ts: 100, end_ts: 500, duration_us: 400 }),
        op({ id: 'b', start_ts: 900, end_ts: null, duration_us: null }),
      ]),
    ];
    const flat = flattenTree(buildOpTree(turns));
    // b's start (900) extends the window even though it has no end.
    expect(traceTimeBounds(flat)).toEqual([100, 900]);
  });

  it('returns [0,0] for an empty tree', () => {
    expect(traceTimeBounds([])).toEqual([0, 0]);
  });
});

describe('layoutWaterfall', () => {
  const flat = flattenTree(buildOpTree(fixtureTurns()));
  const rows = layoutWaterfall(flat, { width: 1000, rowHeight: 20, t0: 0, t1: 1000 });

  it('emits one row per op in pre-order', () => {
    expect(rows.map((r) => r.node.op.id)).toEqual(['root', 'llm', 'tool', 'inner']);
  });

  it('positions and sizes each bar on the shared time axis by start/duration', () => {
    // Full-width window 0..1000 over 1000px → 1µs == 1px.
    const llm = rows.find((r) => r.node.op.id === 'llm');
    expect(llm?.x).toBeCloseTo(100, 5);
    expect(llm?.width).toBeCloseTo(300, 5);
    const tool = rows.find((r) => r.node.op.id === 'tool');
    expect(tool?.x).toBeCloseTo(500, 5);
    expect(tool?.width).toBeCloseTo(400, 5);
  });

  it('stacks rows vertically by row index using rowHeight', () => {
    expect(rows[0]?.y).toBe(0);
    expect(rows[1]?.y).toBe(20);
    expect(rows[2]?.y).toBe(40);
    expect(rows.every((r) => r.height === 20)).toBe(true);
  });

  it('carries depth for indentation of the row label', () => {
    expect(rows.find((r) => r.node.op.id === 'inner')?.depth).toBe(2);
  });

  it('gives a null-end (ongoing) bar a width extending to t1', () => {
    const turns = [turn(1, [op({ id: 'live', start_ts: 200, end_ts: null, duration_us: null })])];
    const f = flattenTree(buildOpTree(turns));
    const r = layoutWaterfall(f, { width: 1000, rowHeight: 20, t0: 0, t1: 1000 });
    expect(r[0]?.x).toBeCloseTo(200, 5);
    expect(r[0]?.width).toBeCloseTo(800, 5); // 200 → 1000
  });

  it('clamps a zero-duration bar to a minimum visible width', () => {
    const turns = [turn(1, [op({ id: 'instant', start_ts: 500, end_ts: 500, duration_us: 0 })])];
    const f = flattenTree(buildOpTree(turns));
    const r = layoutWaterfall(f, { width: 1000, rowHeight: 20, t0: 0, t1: 1000, minBarWidth: 2 });
    expect(r[0]?.width).toBeGreaterThanOrEqual(2);
  });

  it('flags point-event ops as instant and measured ops as not (source-aware, P2#3)', () => {
    const turns = [
      turn(1, [
        op({ id: 'bar', start_ts: 100, end_ts: 400, duration_us: 300, parent_op_id: null }),
        op({ id: 'tick', kind: 'llm', start_ts: 500, end_ts: 500, duration_us: null, parent_op_id: null }),
        op({ id: 'live', kind: 'llm', start_ts: 600, end_ts: null, duration_us: null, parent_op_id: null }),
      ]),
    ];
    const f = flattenTree(buildOpTree(turns));
    const r = layoutWaterfall(f, { width: 1000, rowHeight: 20, t0: 0, t1: 1000 });
    expect(r.find((x) => x.node.op.id === 'bar')?.instant).toBe(false);
    expect(r.find((x) => x.node.op.id === 'tick')?.instant).toBe(true);
    expect(r.find((x) => x.node.op.id === 'live')?.instant).toBe(true);
  });

  it('handles a zero-width time window without dividing by zero', () => {
    const turns = [turn(1, [op({ id: 'p', start_ts: 5, end_ts: 5, duration_us: 0 })])];
    const f = flattenTree(buildOpTree(turns));
    const r = layoutWaterfall(f, { width: 1000, rowHeight: 20, t0: 5, t1: 5 });
    expect(Number.isFinite(r[0]?.x ?? NaN)).toBe(true);
    expect(Number.isFinite(r[0]?.width ?? NaN)).toBe(true);
  });
});

describe('layoutFlame', () => {
  const flat = flattenTree(buildOpTree(fixtureTurns()));
  const cells = layoutFlame(flat, { width: 1000, rowHeight: 18 });

  it('places the root frame across the full width at depth 0', () => {
    const root = cells.find((c) => c.node.op.id === 'root');
    expect(root?.depth).toBe(0);
    expect(root?.y).toBe(0);
    expect(root?.x).toBeCloseTo(0, 5);
    expect(root?.width).toBeCloseTo(1000, 5);
  });

  it('stacks children one level deeper, sized by their duration share of the parent', () => {
    // root window 1000; llm=300, tool=400 (inner under tool). Children are laid
    // out left-to-right starting at the parent's x; widths proportional to span.
    const llm = cells.find((c) => c.node.op.id === 'llm');
    const tool = cells.find((c) => c.node.op.id === 'tool');
    expect(llm?.depth).toBe(1);
    expect(llm?.y).toBe(18);
    expect(llm?.width).toBeCloseTo(300, 5);
    expect(tool?.width).toBeCloseTo(400, 5);
    // tool is placed after llm horizontally (no overlap among siblings).
    expect((tool?.x ?? 0)).toBeGreaterThanOrEqual((llm?.x ?? 0) + (llm?.width ?? 0) - 1e-6);
  });

  it('places a grandchild at depth 2', () => {
    const inner = cells.find((c) => c.node.op.id === 'inner');
    expect(inner?.depth).toBe(2);
    expect(inner?.y).toBe(36);
    expect(inner?.width).toBeCloseTo(100, 5);
  });

  it('flags a point-event cell as instant (source-aware, P2#3)', () => {
    const turns = [
      turn(1, [
        op({ id: 'p', kind: 'session', start_ts: 0, end_ts: 1000, duration_us: 1000, parent_op_id: null }),
        op({ id: 'tick', kind: 'llm', start_ts: 200, end_ts: 200, duration_us: null, parent_op_id: 'p' }),
      ]),
    ];
    const f = flattenTree(buildOpTree(turns));
    const c = layoutFlame(f, { width: 1000, rowHeight: 18 });
    expect(c.find((x) => x.node.op.id === 'p')?.instant).toBe(false);
    expect(c.find((x) => x.node.op.id === 'tick')?.instant).toBe(true);
  });

  it('returns no cells for an empty tree', () => {
    expect(layoutFlame([], { width: 1000, rowHeight: 18 })).toEqual([]);
  });
});

describe('buildTurnRollup', () => {
  it('produces one aggregated bar per turn (span = turn min-start → max-end, op count)', () => {
    const turns = [
      turn(1, [
        op({ id: 'a', start_ts: 100, end_ts: 400, duration_us: 300 }),
        op({ id: 'b', start_ts: 450, end_ts: 700, duration_us: 250 }),
      ]),
      turn(2, [op({ id: 'c', start_ts: 800, end_ts: 1200, duration_us: 400 })]),
    ];
    const rollup = buildTurnRollup(turns);
    expect(rollup.map((r) => r.turn.seq)).toEqual([1, 2]);
    const t1 = rollup[0] as TurnRollup;
    expect(t1.start_ts).toBe(100);
    expect(t1.end_ts).toBe(700);
    expect(t1.op_count).toBe(2);
    // The turn's ops are carried for expand-on-click (pre-order TraceNodes).
    expect(t1.ops.map((n) => n.op.id)).toEqual(['a', 'b']);
    const t2 = rollup[1] as TurnRollup;
    expect(t2.start_ts).toBe(800);
    expect(t2.end_ts).toBe(1200);
    expect(t2.op_count).toBe(1);
  });

  it('uses the turn-level start/end when ops are sparse, and null end when no op closes', () => {
    const turns = [
      turn(1, [op({ id: 'live', start_ts: 100, end_ts: null, duration_us: null })]),
    ];
    const rollup = buildTurnRollup(turns);
    const t1 = rollup[0] as TurnRollup;
    expect(t1.start_ts).toBe(100);
    // No op closes → the rollup end is null (drawn as ongoing/instant).
    expect(t1.end_ts).toBeNull();
  });

  it('skips turns with no ops', () => {
    const turns = [turn(1, []), turn(2, [op({ id: 'x', start_ts: 0, end_ts: 10, duration_us: 10 })])];
    const rollup = buildTurnRollup(turns);
    expect(rollup.map((r) => r.turn.seq)).toEqual([2]);
  });

  it('returns an empty array for no turns', () => {
    expect(buildTurnRollup([])).toEqual([]);
  });
});

describe('timeAxisTicks', () => {
  it('produces ticks within [t0,t1] with pixel positions across the width', () => {
    const ticks = timeAxisTicks(0, 1000, 1000, 5);
    expect(ticks.length).toBeGreaterThan(0);
    for (const t of ticks) {
      expect(t.value).toBeGreaterThanOrEqual(0);
      expect(t.value).toBeLessThanOrEqual(1000);
      expect(t.x).toBeGreaterThanOrEqual(0);
      expect(t.x).toBeLessThanOrEqual(1000);
    }
    // Ticks are ascending by x.
    for (let i = 1; i < ticks.length; i++) {
      expect((ticks[i]?.x ?? 0)).toBeGreaterThan(ticks[i - 1]?.x ?? 0);
    }
  });

  it('returns a single tick for a zero-width window', () => {
    const ticks = timeAxisTicks(5, 5, 1000, 5);
    expect(ticks).toHaveLength(1);
    expect(ticks[0]?.value).toBe(5);
  });
});

describe('cullByY', () => {
  function rows(n: number, rowHeight: number): WaterfallRow[] {
    const out: WaterfallRow[] = [];
    for (let i = 0; i < n; i++) {
      out.push({
        node: { op: op({ id: `r${i}` }), depth: 0, children: [] },
        rowIndex: i,
        depth: 0,
        x: 0,
        width: 10,
        y: i * rowHeight,
        height: rowHeight,
        instant: false,
      });
    }
    return out;
  }

  it('keeps only rows overlapping the visible [scrollTop, scrollTop+viewport] band', () => {
    const all = rows(1000, 20); // 20000px tall
    // Viewport showing y∈[400,800] → rows 20..39 (plus overscan).
    const visible = cullByY(all, 400, 400, 20, 0);
    expect(visible[0]?.rowIndex).toBe(20);
    expect(visible[visible.length - 1]?.rowIndex).toBe(39);
  });

  it('applies overscan rows on each side, clamped to the ends', () => {
    const all = rows(1000, 20);
    const visible = cullByY(all, 400, 400, 20, 5);
    // 5 rows of overscan above (15) and below (44).
    expect(visible[0]?.rowIndex).toBe(15);
    expect(visible[visible.length - 1]?.rowIndex).toBe(44);
  });

  it('clamps at the top (no negative indices)', () => {
    const all = rows(1000, 20);
    const visible = cullByY(all, 0, 200, 20, 5);
    expect(visible[0]?.rowIndex).toBe(0);
  });

  it('returns all rows when they fit within the viewport', () => {
    const all = rows(5, 20);
    const visible = cullByY(all, 0, 1000, 20, 0);
    expect(visible).toHaveLength(5);
  });
});

describe('windowRange', () => {
  it('computes the [start,end) slice for a windowed list with overscan', () => {
    // 1000 items, 24px rows, 480px viewport scrolled to 1200 → first visible
    // index 50, count 20, ±4 overscan → [46, 74).
    expect(windowRange(1000, 24, 1200, 480, 4)).toEqual({ start: 46, end: 74 });
  });

  it('clamps to [0, total]', () => {
    expect(windowRange(10, 24, 0, 1000, 4)).toEqual({ start: 0, end: 10 });
    expect(windowRange(10, 24, 100000, 480, 4)).toEqual({ start: 0, end: 10 });
  });
});

describe('SVG_SPAN_CEILING', () => {
  it('is a positive threshold used to pick SVG vs Canvas', () => {
    expect(SVG_SPAN_CEILING).toBeGreaterThan(0);
    expect(Number.isInteger(SVG_SPAN_CEILING)).toBe(true);
  });
});
