import { scaleLinear, type ScaleLinear } from 'd3-scale';

// Pure geometry/layout for the Timeline tab (ui-pages.md §/sessions/:id #4
// "Timeline" — video-editor style). Lives in viz/ so React components consume
// plain positioned data and never import D3 directly (project-frontend §D3
// Patterns), mirroring viz/trace.ts and viz/topology.ts. The server returns the
// lane/span model directly (GET /api/sessions/:id/timeline — one lane per
// session, root + children stacked): this module maps it to positioned rows —
//
//   - lane index → y (stacked by laneHeight);
//   - span start/end → x via a shared [tStart,tEnd] → [0,width] time scale,
//     width = duration;
//   - a NULLABLE end_ts (a running op, or a point event) renders as an INSTANT
//     marker at start_ts — a point, NOT a zero-width or viewport-extended bar
//     (source-aware, matching the Trace tab's null-end handling). An end_ts at
//     or before start_ts is likewise an instant (never a negative-width bar);
//   - compaction spans (kind==='compaction') are FLAGGED so the renderer draws
//     a full-height vertical breakpoint instead of a lane-height bar.
//
// Overlap between lanes is intentional and preserved (parallel sub-agents must
// be visible). A viewport-cull helper returns only spans overlapping the visible
// X window AND the visible lane band — the Canvas path's culling (the SVG path
// below VISIBLE_SPAN_CEILING draws every span as a DOM node).

/** One span on a lane — mirrors the wire timelineSpan (presenter
 *  session_timeline.go). end_ts is nullable (running / point op). */
export interface TimelineSpan {
  id: string;
  kind: string;
  name: string;
  start_ts: number;
  end_ts: number | null;
  status: string;
}

/** One lane — mirrors the wire timelineLane (one session's spans). */
export interface TimelineLaneInput {
  key: string;
  label: string;
  spans: TimelineSpan[];
}

export interface TimelineOpts {
  /** Pixel width of the time track. */
  width: number;
  /** Pixel height of one lane row. */
  laneHeight: number;
  /** Window start (µs) — server t_start. */
  tStart: number;
  /** Window end (µs) — server t_end. */
  tEnd: number;
  /** Minimum bar width in px so a tiny op stays visible/clickable (default 2). */
  minBarWidth?: number;
}

/** A lane plus its computed vertical position. */
export interface PositionedLane {
  key: string;
  label: string;
  laneIndex: number;
  y: number;
  height: number;
}

/** A span plus its computed geometry and rendering flags. */
export interface PositionedSpan {
  span: TimelineSpan;
  laneIndex: number;
  /** Lane key the span belongs to (renderer grouping / drawer context). */
  laneKey: string;
  /** Bar/instant x within the time track. */
  x: number;
  /** Bar width in px (instant markers carry the minimum width for hit/draw). */
  width: number;
  /** Lane vertical position. */
  y: number;
  height: number;
  /** True when end_ts is null/≤start_ts: a point event, drawn as a marker. */
  instant: boolean;
  /** True when kind==='compaction': drawn as a full-height vertical breakpoint. */
  compaction: boolean;
}

/** The full positioned layout: lane rows + span rows (a flat span list keeps
 *  the renderer's culling + draw loop simple, each span carrying its lane y). */
export interface TimelineLayout {
  lanes: PositionedLane[];
  spans: PositionedSpan[];
}

/**
 * VISIBLE_SPAN_CEILING is the span count above which the Timeline switches from
 * SVG (one DOM node per span — inspectable, a11y-friendly) to Canvas with
 * viewport-clipped culling (frontend-architecture.md §Performance Budgets:
 * "Timeline: Canvas rendering (not SVG) when > 500 spans").
 */
export const VISIBLE_SPAN_CEILING = 500;

/** isInstant is true when a span has no closed forward window (null / ≤ start):
 *  a running op or a point event, drawn as a marker rather than a bar. */
export function isInstant(span: TimelineSpan): boolean {
  return span.end_ts === null || span.end_ts <= span.start_ts;
}

/** isCompaction flags a context-compaction op (full-height breakpoint). */
export function isCompaction(span: TimelineSpan): boolean {
  return span.kind === 'compaction';
}

/**
 * timelineScale builds the shared time→pixel linear scale for [tStart,tEnd] over
 * [0,width]. A zero-width (or inverted) window is widened by 1µs so the scale
 * never divides by zero — every span then collapses to x=0 rather than NaN
 * (matching layoutWaterfall's zero-window handling in viz/trace.ts).
 */
export function timelineScale(tStart: number, tEnd: number, width: number): ScaleLinear<number, number> {
  return scaleLinear()
    .domain([tStart, tEnd > tStart ? tEnd : tStart + 1])
    .range([0, width]);
}

/**
 * layoutTimeline maps the lane/span model onto positioned rows. Each lane gets a
 * y by its index × laneHeight; each span is positioned on the shared time scale
 * (x by start, width by duration), inheriting its lane's y. A null/≤start end_ts
 * becomes an instant marker at start_ts (no real bar). Compaction spans are
 * flagged. Lanes with no spans still appear (the lane is drawn empty). Returns
 * empty lanes+spans for an empty input.
 */
export function layoutTimeline(lanes: TimelineLaneInput[], opts: TimelineOpts): TimelineLayout {
  const { width, laneHeight, tStart, tEnd } = opts;
  const minBarWidth = opts.minBarWidth ?? 2;
  const x = timelineScale(tStart, tEnd, width);

  const positionedLanes: PositionedLane[] = lanes.map((lane, laneIndex) => ({
    key: lane.key,
    label: lane.label,
    laneIndex,
    y: laneIndex * laneHeight,
    height: laneHeight,
  }));

  const spans: PositionedSpan[] = [];
  lanes.forEach((lane, laneIndex) => {
    const y = laneIndex * laneHeight;
    for (const span of lane.spans) {
      const instant = isInstant(span);
      const x0 = x(span.start_ts);
      // A closed bar spans start→end; an instant carries the minimum width so it
      // is drawable/clickable as a marker (the renderer paints a tick, not a bar).
      const width = instant
        ? minBarWidth
        : Math.max(minBarWidth, x(span.end_ts as number) - x0);
      spans.push({
        span,
        laneIndex,
        laneKey: lane.key,
        x: x0,
        width,
        y,
        height: laneHeight,
        instant,
        compaction: isCompaction(span),
      });
    }
  });

  return { lanes: positionedLanes, spans };
}

export interface CullWindow {
  /** Visible x range in track pixels. */
  xMin: number;
  xMax: number;
  /** Visible lane-index band (inclusive). */
  laneMin: number;
  laneMax: number;
}

/**
 * cullSpans returns only the spans whose pixel extent overlaps the visible x
 * window AND whose lane index is within the visible lane band — the Canvas
 * path's viewport-clipped culling (frontend-architecture.md §Performance
 * Budgets). A bar overlaps when [x, x+width] intersects [xMin,xMax]; an instant
 * marker overlaps when its point x is within [xMin,xMax]. Off-window or
 * off-lane spans are dropped so a large timeline only ever paints what is on
 * screen. Pure + deterministic.
 */
export function cullSpans(spans: PositionedSpan[], win: CullWindow): PositionedSpan[] {
  if (spans.length === 0) {
    return [];
  }
  return spans.filter((s) => {
    if (s.laneIndex < win.laneMin || s.laneIndex > win.laneMax) {
      return false;
    }
    const left = s.x;
    const right = s.instant ? s.x : s.x + s.width;
    return right >= win.xMin && left <= win.xMax;
  });
}
