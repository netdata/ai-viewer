import { describe, expect, it } from 'vitest';
import type { OpDetail, TurnDetail } from '../api/types';
import {
  buildOpTree,
  flattenTree,
  traceTimeBounds,
  layoutWaterfall,
  layoutFlame,
  timeAxisTicks,
  cullByY,
  windowRange,
  SVG_SPAN_CEILING,
  type TraceNode,
  type WaterfallRow,
} from './trace';

// viz/trace.ts holds the PURE geometry/layout for the Trace tab renderings,
// separated from React (the viz/ boundary — project-frontend skill §D3 Patterns).
// Given the session op tree it computes (a) waterfall rows on a shared time
// axis and (b) flame-graph cells stacked by depth. The wire opDetail carries no
// explicit parent_op_id (internal/presenter/session_detail.go), so nesting is
// derived from TEMPORAL CONTAINMENT (an op whose [start,end] lies within
// another's is its child) — the standard derivation for APM waterfalls without
// explicit span links. child_session_id marks a cross-session transition; such
// ops are leaves in THIS session's tree (the child's ops load separately).
// These tests pin the geometry deterministically from a fixture op tree.

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

// A fixture tree: a root agent op [0,1000] contains an llm op [100,400] and a
// tool op [500,900]; the tool op contains a nested internal op [600,700].
function fixtureTurns(): TurnDetail[] {
  return [
    turn(1, [
      op({ id: 'root', kind: 'session', start_ts: 0, end_ts: 1000, duration_us: 1000 }),
      op({ id: 'llm', kind: 'llm', start_ts: 100, end_ts: 400, duration_us: 300 }),
      op({ id: 'tool', kind: 'tool', start_ts: 500, end_ts: 900, duration_us: 400 }),
      op({ id: 'inner', kind: 'internal', start_ts: 600, end_ts: 700, duration_us: 100 }),
    ]),
  ];
}

describe('buildOpTree', () => {
  it('nests ops by temporal containment', () => {
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

  it('orders siblings by start_ts then turn seq then op order', () => {
    const turns = [
      turn(1, [op({ id: 'b', start_ts: 200, end_ts: 300, duration_us: 100 })]),
      turn(2, [op({ id: 'a', start_ts: 100, end_ts: 150, duration_us: 50 })]),
    ];
    const roots = buildOpTree(turns);
    // 'a' starts earlier so it sorts first despite being in a later turn.
    expect(roots.map((r) => r.op.id)).toEqual(['a', 'b']);
  });

  it('treats a child_session_id op as a leaf (no temporal children absorbed)', () => {
    // The session-transition op spans [0,1000]; another op falls inside its
    // window but must NOT be nested under a cross-session boundary.
    const turns = [
      turn(1, [
        op({ id: 'spawn', kind: 'session', start_ts: 0, end_ts: 1000, duration_us: 1000, child_session_id: 'child-1' }),
        op({ id: 'sibling', kind: 'tool', start_ts: 200, end_ts: 400, duration_us: 200 }),
      ]),
    ];
    const roots = buildOpTree(turns);
    const spawn = roots.find((r) => r.op.id === 'spawn') as TraceNode;
    expect(spawn.children).toHaveLength(0);
    // 'sibling' is a root, not a child of the transition op.
    expect(roots.map((r) => r.op.id)).toContain('sibling');
  });

  it('treats a null end_ts as ongoing (does not contain later ops)', () => {
    const turns = [
      turn(1, [
        op({ id: 'ongoing', kind: 'llm', start_ts: 0, end_ts: null, duration_us: null }),
        op({ id: 'after', kind: 'tool', start_ts: 100, end_ts: 200, duration_us: 100 }),
      ]),
    ];
    const roots = buildOpTree(turns);
    // An op with no end has no closed window, so it absorbs no children.
    const ongoing = roots.find((r) => r.op.id === 'ongoing') as TraceNode;
    expect(ongoing.children).toHaveLength(0);
    expect(roots.map((r) => r.op.id)).toEqual(['ongoing', 'after']);
  });

  it('returns an empty array for no turns/ops', () => {
    expect(buildOpTree([])).toEqual([]);
    expect(buildOpTree([turn(1, [])])).toEqual([]);
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

  it('returns no cells for an empty tree', () => {
    expect(layoutFlame([], { width: 1000, rowHeight: 18 })).toEqual([]);
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
