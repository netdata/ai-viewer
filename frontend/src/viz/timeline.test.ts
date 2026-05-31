import { describe, expect, it } from 'vitest';
import {
  layoutTimeline,
  timelineScale,
  cullSpans,
  isInstant,
  isCompaction,
  timeXOnlyMatrix,
  VISIBLE_SPAN_CEILING,
  type TimelineLaneInput,
} from './timeline';

// viz/timeline.ts holds the PURE geometry/layout for the Timeline tab (ui-pages.md
// §/sessions/:id #4 "Timeline" — video-editor style), separated from React (the
// viz/ boundary — project-frontend §D3 Patterns) and from the wire fetch, exactly
// like viz/trace.ts and viz/topology.ts. Given the server's lane/span model
// (one lane per session, GET /api/sessions/:id/timeline) it computes positioned
// rows: lane index → y, span start/end → x via a shared time scale, width =
// duration. A NULL end_ts is a still-RUNNING op and a POINT EVENT carries
// end_ts == start_ts; both render as an INSTANT marker at start_ts (a point, not
// a zero-width bar) — source-aware, like the Trace tab. Compaction
// spans (kind==='compaction') are flagged so the renderer draws a full-height
// vertical breakpoint. A viewport-cull helper keeps only spans overlapping the
// visible X window AND visible lanes (the Canvas path's culling). These tests pin
// the geometry deterministically without a browser (no Canvas, no d3-zoom).

function lane(over: Partial<TimelineLaneInput>): TimelineLaneInput {
  return {
    key: 'session:s',
    label: 's',
    spans: [],
    ...over,
  };
}

// A fixture: a root lane with an llm span [100,400] and a tool span [500,900];
// a child lane with one span [300,700] that OVERLAPS the root's tool in time
// (parallel sub-agent — overlap is intentional), plus a running span [800,null].
function fixtureLanes(): TimelineLaneInput[] {
  return [
    lane({
      key: 'session:root',
      label: 'nedi (root)',
      spans: [
        { id: 'llm', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 400, status: 'completed' },
        { id: 'tool', kind: 'tool', name: 'Bash', start_ts: 500, end_ts: 900, status: 'completed' },
      ],
    }),
    lane({
      key: 'session:child',
      label: 'worker',
      spans: [
        { id: 'sub', kind: 'llm', name: 'sub', start_ts: 300, end_ts: 700, status: 'completed' },
        { id: 'live', kind: 'tool', name: 'run', start_ts: 800, end_ts: null, status: 'running' },
      ],
    }),
  ];
}

const OPTS = { width: 1000, laneHeight: 40, tStart: 100, tEnd: 900 } as const;

describe('timelineScale', () => {
  it('maps the time domain across the pixel width', () => {
    const x = timelineScale(0, 1000, 1000);
    expect(x(0)).toBeCloseTo(0, 6);
    expect(x(500)).toBeCloseTo(500, 6);
    expect(x(1000)).toBeCloseTo(1000, 6);
  });

  it('does not divide by zero for a zero-width window (everything collapses to x=0)', () => {
    const x = timelineScale(5, 5, 1000);
    expect(Number.isFinite(x(5))).toBe(true);
    expect(x(5)).toBeCloseTo(0, 6);
  });
});

describe('isInstant / isCompaction', () => {
  it('treats a null end_ts as an instant — a still-RUNNING op (no recorded end yet)', () => {
    expect(isInstant({ id: 'a', kind: 'tool', name: '', start_ts: 1, end_ts: null, status: 'running' })).toBe(true);
    expect(isInstant({ id: 'b', kind: 'tool', name: '', start_ts: 1, end_ts: 2, status: 'completed' })).toBe(false);
  });

  it('treats an end_ts equal to start_ts as an instant — the real POINT-EVENT shape (zero-duration)', () => {
    expect(isInstant({ id: 'c', kind: 'tool', name: '', start_ts: 5, end_ts: 5, status: 'completed' })).toBe(true);
  });

  it('treats an end_ts before start_ts as an instant (defensive, never negative width)', () => {
    expect(isInstant({ id: 'd', kind: 'tool', name: '', start_ts: 5, end_ts: 3, status: 'completed' })).toBe(true);
  });

  it('flags a compaction span by kind', () => {
    expect(isCompaction({ id: 'k', kind: 'compaction', name: '', start_ts: 1, end_ts: 2, status: 'completed' })).toBe(true);
    expect(isCompaction({ id: 'l', kind: 'llm', name: '', start_ts: 1, end_ts: 2, status: 'completed' })).toBe(false);
  });
});

describe('layoutTimeline — empty', () => {
  it('returns empty lanes/spans for no lanes', () => {
    const out = layoutTimeline([], OPTS);
    expect(out.lanes).toEqual([]);
    expect(out.spans).toEqual([]);
  });

  it('returns a lane row even when the lane has no spans (the lane is still drawn)', () => {
    const out = layoutTimeline([lane({ key: 'session:empty', label: 'empty', spans: [] })], OPTS);
    expect(out.lanes).toHaveLength(1);
    expect(out.lanes[0]?.key).toBe('session:empty');
    expect(out.spans).toEqual([]);
  });
});

describe('layoutTimeline — lane → y mapping', () => {
  const out = layoutTimeline(fixtureLanes(), OPTS);

  it('stacks lanes vertically by lane index using laneHeight', () => {
    expect(out.lanes[0]?.y).toBe(0);
    expect(out.lanes[0]?.laneIndex).toBe(0);
    expect(out.lanes[1]?.y).toBe(40);
    expect(out.lanes[1]?.laneIndex).toBe(1);
    expect(out.lanes.every((l) => l.height === 40)).toBe(true);
  });

  it('carries each span at the y of its lane', () => {
    const llm = out.spans.find((s) => s.span.id === 'llm');
    const sub = out.spans.find((s) => s.span.id === 'sub');
    expect(llm?.y).toBe(0); // root lane
    expect(sub?.y).toBe(40); // child lane
    expect(llm?.laneIndex).toBe(0);
    expect(sub?.laneIndex).toBe(1);
  });
});

describe('layoutTimeline — span x / width from the time scale', () => {
  const out = layoutTimeline(fixtureLanes(), OPTS);

  it('positions and sizes a bar by start/end on the shared [tStart,tEnd] → [0,width] scale', () => {
    // Window 100..900 over 1000px → 1µs == 1.25px. llm [100,400]: x=0, w=375.
    const llm = out.spans.find((s) => s.span.id === 'llm');
    expect(llm?.x).toBeCloseTo(0, 4);
    expect(llm?.width).toBeCloseTo(375, 4);
    // tool [500,900]: x=(500-100)*1.25=500, w=(900-500)*1.25=500.
    const tool = out.spans.find((s) => s.span.id === 'tool');
    expect(tool?.x).toBeCloseTo(500, 4);
    expect(tool?.width).toBeCloseTo(500, 4);
  });

  it('lets sibling-lane spans overlap in x (parallel sub-agents are intentionally visible)', () => {
    // root tool [500,900] and child sub [300,700] overlap in time but sit on
    // different lanes (different y), so their x ranges intentionally intersect.
    const tool = out.spans.find((s) => s.span.id === 'tool');
    const sub = out.spans.find((s) => s.span.id === 'sub');
    const toolX0 = tool?.x ?? 0;
    const toolX1 = toolX0 + (tool?.width ?? 0);
    const subX0 = sub?.x ?? 0;
    const subX1 = subX0 + (sub?.width ?? 0);
    // Their x intervals overlap.
    expect(subX1).toBeGreaterThan(toolX0);
    expect(toolX1).toBeGreaterThan(subX0);
    // But they are on different lanes.
    expect(tool?.y).not.toBe(sub?.y);
  });
});

describe('layoutTimeline — nullable end_ts → instant marker (not a bar)', () => {
  const out = layoutTimeline(fixtureLanes(), OPTS);

  it('marks a null-end span (a still-RUNNING op) as an instant placed at its start_ts, not a zero/extended bar', () => {
    // live [800,null] is a still-running op → instant at x=(800-100)*1.25=875,
    // flagged instant, no real width.
    const live = out.spans.find((s) => s.span.id === 'live');
    expect(live?.instant).toBe(true);
    expect(live?.x).toBeCloseTo(875, 4);
    // A closed bar is NOT an instant.
    const llm = out.spans.find((s) => s.span.id === 'llm');
    expect(llm?.instant).toBe(false);
  });

  it('marks a POINT EVENT (end_ts == start_ts) as an instant at start_ts, distinct from a running op', () => {
    // The real persisted point-event shape is end_ts == start_ts (NOT null — null
    // is the running shape). It is flagged instant and placed at start_ts with no
    // real width, exactly like a running op.
    const lanes = [
      lane({
        key: 'session:root',
        label: 'root',
        spans: [
          { id: 'point', kind: 'llm', name: 'gen', start_ts: 500, end_ts: 500, status: 'completed' },
        ],
      }),
    ];
    const out2 = layoutTimeline(lanes, OPTS);
    const point = out2.spans.find((s) => s.span.id === 'point');
    expect(point?.instant).toBe(true);
    // x=(500-100)*1.25=500; no measured forward window.
    expect(point?.x).toBeCloseTo(500, 4);
  });
});

describe('layoutTimeline — compaction flagged for full-height breakpoint', () => {
  it('flags a compaction span and exposes its x for a full-height vertical breakpoint', () => {
    const lanes = [
      lane({
        key: 'session:root',
        label: 'root',
        spans: [
          { id: 'c', kind: 'compaction', name: 'compact', start_ts: 500, end_ts: null, status: 'completed' },
          { id: 'op', kind: 'llm', name: 'chat', start_ts: 100, end_ts: 200, status: 'completed' },
        ],
      }),
    ];
    const out = layoutTimeline(lanes, { width: 1000, laneHeight: 40, tStart: 100, tEnd: 900 });
    const c = out.spans.find((s) => s.span.id === 'c');
    expect(c?.compaction).toBe(true);
    // x positioned on the scale (500-100)*1.25 = 500.
    expect(c?.x).toBeCloseTo(500, 4);
    // A non-compaction span is not flagged.
    expect(out.spans.find((s) => s.span.id === 'op')?.compaction).toBe(false);
  });
});

describe('layoutTimeline — zero-width window safety', () => {
  it('produces finite geometry when tStart === tEnd', () => {
    const out = layoutTimeline(fixtureLanes(), { width: 1000, laneHeight: 40, tStart: 100, tEnd: 100 });
    for (const s of out.spans) {
      expect(Number.isFinite(s.x)).toBe(true);
      expect(Number.isFinite(s.width)).toBe(true);
    }
  });
});

describe('cullSpans — viewport culling (visible X window AND visible lanes)', () => {
  const out = layoutTimeline(fixtureLanes(), OPTS);

  it('keeps only spans overlapping the visible x window', () => {
    // Visible x window [0,400] (≈ time 100..420): llm [x0..375] in; tool
    // [500..1000] out; sub [250..750] partially in; live instant at 875 out.
    const visible = cullSpans(out.spans, { xMin: 0, xMax: 400, laneMin: 0, laneMax: 10 });
    const ids = new Set(visible.map((s) => s.span.id));
    expect(ids.has('llm')).toBe(true);
    expect(ids.has('sub')).toBe(true);
    expect(ids.has('tool')).toBe(false);
    expect(ids.has('live')).toBe(false);
  });

  it('keeps only spans on visible lanes', () => {
    // Only lane 0 visible: drops the child lane's spans (sub, live).
    const visible = cullSpans(out.spans, { xMin: -Infinity, xMax: Infinity, laneMin: 0, laneMax: 0 });
    const ids = new Set(visible.map((s) => s.span.id));
    expect(ids.has('llm')).toBe(true);
    expect(ids.has('tool')).toBe(true);
    expect(ids.has('sub')).toBe(false);
    expect(ids.has('live')).toBe(false);
  });

  it('includes an instant marker whose point falls inside the x window', () => {
    // live instant at x=875 with lane 1 visible.
    const visible = cullSpans(out.spans, { xMin: 800, xMax: 1000, laneMin: 0, laneMax: 10 });
    expect(visible.some((s) => s.span.id === 'live')).toBe(true);
  });

  it('returns [] for no spans', () => {
    expect(cullSpans([], { xMin: 0, xMax: 100, laneMin: 0, laneMax: 1 })).toEqual([]);
  });

  it('keeps a compaction breakpoint inside the time window even when its lane is OFF the visible band', () => {
    // A compaction op renders as a FULL-HEIGHT vertical breakpoint spanning every
    // lane, so it must be culled by the visible TIME window only — never dropped
    // because its own lane index falls outside the visible lane band.
    const lanes = [
      lane({ key: 'session:root', label: 'root', spans: [] }),
      lane({
        key: 'session:child',
        label: 'worker',
        spans: [
          { id: 'compact', kind: 'compaction', name: 'compact', start_ts: 500, end_ts: null, status: 'completed' },
        ],
      }),
    ];
    const out = layoutTimeline(lanes, OPTS); // compact on lane 1, x=(500-100)*1.25=500
    // Only lane 0 visible, but the breakpoint's x (500) is inside the window.
    const visible = cullSpans(out.spans, { xMin: 0, xMax: 1000, laneMin: 0, laneMax: 0 });
    expect(visible.some((s) => s.span.id === 'compact')).toBe(true);
  });

  it('still time-culls a compaction breakpoint OUTSIDE the visible time window', () => {
    // Lane-exempt does NOT mean window-exempt: a breakpoint whose x is past the
    // visible time window is still dropped (it is off-screen in X).
    const lanes = [
      lane({
        key: 'session:root',
        label: 'root',
        spans: [
          { id: 'compact', kind: 'compaction', name: 'compact', start_ts: 800, end_ts: null, status: 'completed' },
        ],
      }),
    ];
    const out = layoutTimeline(lanes, OPTS); // x=(800-100)*1.25=875
    const visible = cullSpans(out.spans, { xMin: 0, xMax: 400, laneMin: 0, laneMax: 10 });
    expect(visible.some((s) => s.span.id === 'compact')).toBe(false);
  });
});

describe('timeXOnlyMatrix — zoom scales the time axis (X) only, never lane height (Y)', () => {
  it('builds an SVG matrix that scales X by k and keeps Y at scale 1', () => {
    // The d3-zoom transform {k,x,y} must apply to the TIME axis only: X scaled by
    // k, Y unscaled (lane height constant), translated by (x,y). The SVG matrix
    // form matrix(a,b,c,d,e,f) scales X by `a`, Y by `d` — so a===k and d===1
    // (shift+wheel zooms TIME — lane height must stay constant).
    expect(timeXOnlyMatrix(2, 30, 40)).toBe('matrix(2,0,0,1,30,40)');
  });

  it('keeps the Y-scale at 1 for any zoom factor (lanes never grow vertically)', () => {
    for (const k of [0.2, 1, 8, 64]) {
      const m = timeXOnlyMatrix(k, 0, 0);
      // The 4th matrix component (d, the Y scale) is always 1.
      const parts = /^matrix\(([^,]+),([^,]+),([^,]+),([^,]+),/.exec(m);
      expect(parts?.[1]).toBe(String(k)); // X scale === k
      expect(parts?.[4]).toBe('1'); // Y scale === 1, independent of k
    }
  });

  it('preserves the X and Y translation (vertical lane pan still works)', () => {
    // Y is not SCALED, but it is still TRANSLATED so a plain-wheel vertical pan
    // across lanes keeps working — only the zoom-driven Y SCALE is removed.
    expect(timeXOnlyMatrix(1, -40, -120)).toBe('matrix(1,0,0,1,-40,-120)');
  });
});

describe('VISIBLE_SPAN_CEILING', () => {
  it('matches the frontend-architecture.md 500-span Canvas boundary', () => {
    expect(VISIBLE_SPAN_CEILING).toBe(500);
  });
});
