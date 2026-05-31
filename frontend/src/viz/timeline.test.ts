import { describe, expect, it } from 'vitest';
import {
  layoutTimeline,
  timelineScale,
  cullSpans,
  isInstant,
  isCompaction,
  VISIBLE_SPAN_CEILING,
  type TimelineLaneInput,
} from './timeline';

// viz/timeline.ts holds the PURE geometry/layout for the Timeline tab (ui-pages.md
// §/sessions/:id #4 "Timeline" — video-editor style), separated from React (the
// viz/ boundary — project-frontend §D3 Patterns) and from the wire fetch, exactly
// like viz/trace.ts and viz/topology.ts. Given the server's lane/span model
// (one lane per session, GET /api/sessions/:id/timeline) it computes positioned
// rows: lane index → y, span start/end → x via a shared time scale, width =
// duration. A NULLABLE end_ts renders as an INSTANT marker at start_ts (a point
// event, not a zero-width bar) — source-aware, like the Trace tab. Compaction
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
  it('treats a null end_ts as an instant (point) event', () => {
    expect(isInstant({ id: 'a', kind: 'tool', name: '', start_ts: 1, end_ts: null, status: 'running' })).toBe(true);
    expect(isInstant({ id: 'b', kind: 'tool', name: '', start_ts: 1, end_ts: 2, status: 'completed' })).toBe(false);
  });

  it('treats an end_ts equal to start_ts as an instant (zero-duration point)', () => {
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

  it('marks a null-end span as an instant placed at its start_ts, not a zero/extended bar', () => {
    // live [800,null] → instant at x=(800-100)*1.25=875, flagged instant, no real width.
    const live = out.spans.find((s) => s.span.id === 'live');
    expect(live?.instant).toBe(true);
    expect(live?.x).toBeCloseTo(875, 4);
    // A closed bar is NOT an instant.
    const llm = out.spans.find((s) => s.span.id === 'llm');
    expect(llm?.instant).toBe(false);
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
});

describe('VISIBLE_SPAN_CEILING', () => {
  it('matches the frontend-architecture.md 500-span Canvas boundary', () => {
    expect(VISIBLE_SPAN_CEILING).toBe(500);
  });
});
