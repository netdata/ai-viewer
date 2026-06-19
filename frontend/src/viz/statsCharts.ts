import { scaleLinear } from 'd3-scale';
import { ticks as d3Ticks } from 'd3-array';

// Pure geometry/layout for the /stats dashboard charts (SOW-0007 Chunk 9). Lives
// in viz/ so React components consume plain positioned data and never import D3
// directly (project-frontend §D3 Patterns), mirroring viz/trace.ts and
// viz/timeline.ts. Two renderings:
//   - lineChartLayout: one polyline per distinct aggregate series key, on a
//     shared time-x / value-y scale (the /api/stats/aggregate response);
//   - barChartLayout: one horizontal bar per top-N item, widths proportional to
//     value (the /api/stats/top response, already sorted desc by the server).
//
// Everything here is deterministic and degenerate-input safe (empty data, a
// single point, all-equal values, a zero-width window) — never divides by zero,
// never emits NaN coordinates. Colors are returned as theme-token var()
// references (seriesColorVar) so a theme flip recolors with no JS (the same
// var() convention the rest of the SVG viz uses for fills/strokes).

/** Inner-plot padding (axis gutters) shared by both charts. */
export interface ChartPadding {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

/** Pixel box + padding the layout maps data into. */
export interface ChartDims {
  width: number;
  height: number;
  padding: ChartPadding;
}

/** A point in pixel space. */
export interface Point {
  x: number;
  y: number;
}

// ── Series color palette ─────────────────────────────────────────────────────

/**
 * Categorical series palette as THEME-TOKEN references. Returned as `var(--…)`
 * strings (not resolved hex) so a series line/bar recolors automatically on a
 * theme flip — the same approach the markup uses for `fill="var(--…)"`. The set
 * reuses the existing semantic tokens (tokens.css) rather than inventing a new
 * scale, so the dashboard palette tracks the rest of the app. Color is NEVER the
 * sole differentiator — every series/bar is also labelled with text — so a
 * cycling palette for many series is acceptable (the legend disambiguates).
 */
const SERIES_TOKEN_COUNT = 6;

function normalizedSeriesTokenIndex(index: number): number {
  return ((index % SERIES_TOKEN_COUNT) + SERIES_TOKEN_COUNT) % SERIES_TOKEN_COUNT;
}

function seriesTokenAt(index: number): string {
  switch (normalizedSeriesTokenIndex(index)) {
    case 0:
      return '--chart-1';
    case 1:
      return '--chart-2';
    case 2:
      return '--chart-3';
    case 3:
      return '--chart-4';
    case 4:
      return '--chart-5';
    default:
      return '--text-secondary';
  }
}

/**
 * seriesColorVar returns the var() reference for the i-th series, cycling the
 * palette so an arbitrary series count is always assigned a usable color.
 * Deterministic: the same index always yields the same color.
 */
export function seriesColorVar(index: number): string {
  return `var(${seriesTokenAt(index)})`;
}

// ── Axis ticks ───────────────────────────────────────────────────────────────

/** One x-axis tick: the domain value and its pixel x. */
export interface XTick {
  value: number;
  x: number;
}

/** One y-axis tick: the domain value and its pixel y. */
export interface YTick {
  value: number;
  y: number;
}

/**
 * niceTicks returns human-friendly tick values for [lo,hi] using d3-array's
 * algorithm, degenerate-safe: a zero/inverted span yields a single tick at lo so
 * an empty or single-point chart still renders an axis (never NaN). targetCount
 * is a hint; d3 may return slightly fewer/more for round numbers.
 */
function niceTicks(lo: number, hi: number, targetCount: number): number[] {
  if (!(hi > lo)) {
    return [lo];
  }
  const values = d3Ticks(lo, hi, targetCount);
  return values.length > 0 ? values : [lo, hi];
}

// ── Line chart ─────────────────────────────────────────────────────────────

/** One aggregate bucket as consumed by the layout (mirrors the wire shape). */
export interface AggregateBucketInput {
  bucket_ts: number;
  series: { key: string; value: number }[];
}

/** One laid-out series: its key, ordered pixel points, and theme color var. */
export interface LaidSeries {
  key: string;
  points: Point[];
  color: string;
}

/** The full line-chart layout: positioned series + both axes' ticks. */
export interface LineChartLayout {
  series: LaidSeries[];
  xTicks: XTick[];
  yTicks: YTick[];
}

/** plotBand returns the inner [x0,x1]/[y0,y1] plot rectangle (after padding). */
function plotBand(dims: ChartDims): { x0: number; x1: number; y0: number; y1: number } {
  const { width, height, padding } = dims;
  return {
    x0: padding.left,
    x1: Math.max(padding.left, width - padding.right),
    y0: padding.top,
    y1: Math.max(padding.top, height - padding.bottom),
  };
}

/**
 * lineChartLayout pivots the per-bucket series rows (the /api/stats/aggregate
 * shape — each bucket lists every series' value at that time) into one polyline
 * per distinct series key, on a shared time-x / value-y scale.
 *
 * X domain = [min,max] bucket_ts; Y domain = [0, max value] (0-based so bar-like
 * magnitudes read truthfully; a value chart starts at zero). Y is inverted
 * (larger value → smaller pixel y, i.e. nearer the top). All degenerate cases
 * collapse the relevant scale to a single mid-band pixel without dividing by
 * zero: empty buckets → no series; one bucket → one point per series; all-equal
 * values → a flat line inside the band. Each series' points are ordered by
 * bucket_ts ascending regardless of input order, so the polyline never zig-zags
 * backwards.
 */
export function lineChartLayout(
  buckets: AggregateBucketInput[],
  dims: ChartDims,
): LineChartLayout {
  const { x0, x1, y0, y1 } = plotBand(dims);

  // Sort buckets by time once so every series inherits ascending x order.
  // Filter out zero/NaN bucket_ts values: a sentinel zero from the backend
  // would otherwise pin the X scale to 1970 and crowd every real data point
  // into the rightmost few pixels (the visible "1970, 1985, 2001, 2017"
  // tick spread we saw before the fix). Defensive against the contract
  // occasionally emitting 0 for the "no data yet" bucket.
  const sorted = [...buckets]
    .filter((b) => Number.isFinite(b.bucket_ts) && b.bucket_ts > 0)
    .sort((a, b) => a.bucket_ts - b.bucket_ts);

  // Domain bounds across all buckets/series.
  let tLo = Infinity;
  let tHi = -Infinity;
  let vHi = 0; // 0-based value axis
  for (const b of sorted) {
    if (b.bucket_ts < tLo) tLo = b.bucket_ts;
    if (b.bucket_ts > tHi) tHi = b.bucket_ts;
    for (const s of b.series) {
      if (Number.isFinite(s.value) && s.value > vHi) vHi = s.value;
    }
  }
  if (!Number.isFinite(tLo)) {
    // No data: empty series, but still emit a usable (degenerate) axis.
    return {
      series: [],
      xTicks: niceTicks(0, 0, 4).map((value) => ({ value, x: x0 })),
      yTicks: niceTicks(0, 0, 4).map((value) => ({ value, y: y1 })),
    };
  }

  // A zero/!finite span maps every value to the band START (x0 / y1) — the
  // d3 range is collapsed by giving it a 1-wide domain so it never divides by 0.
  const xScale = scaleLinear()
    .domain(tHi > tLo ? [tLo, tHi] : [tLo, tLo + 1])
    .range([x0, x1]);
  const yScale = scaleLinear()
    .domain(vHi > 0 ? [0, vHi] : [0, 1])
    .range([y1, y0]); // inverted: 0 at the bottom (y1), max at the top (y0)

  // Pivot: collect each key's (x,y) across buckets, preserving first-seen order
  // for a stable series/legend ordering.
  const byKey = new Map<string, Point[]>();
  const order: string[] = [];
  for (const b of sorted) {
    const px = xScale(b.bucket_ts);
    for (const s of b.series) {
      let pts = byKey.get(s.key);
      if (pts === undefined) {
        pts = [];
        byKey.set(s.key, pts);
        order.push(s.key);
      }
      pts.push({ x: px, y: yScale(Number.isFinite(s.value) ? s.value : 0) });
    }
  }

  const series: LaidSeries[] = order.map((key, i) => ({
    key,
    points: byKey.get(key) ?? [],
    color: seriesColorVar(i),
  }));

  return {
    series,
    xTicks: niceTicks(tLo, tHi, 5).map((value) => ({ value, x: xScale(value) })),
    yTicks: niceTicks(0, vHi, 4).map((value) => ({ value, y: yScale(value) })),
  };
}

/**
 * linePath builds an `M x y L x y …` SVG path string from ordered points.
 * Returns '' for no points (the caller renders nothing). Single point yields a
 * lone `M` (a zero-length path) so the renderer can still place a dot marker.
 */
export function linePath(points: Point[]): string {
  if (points.length === 0) {
    return '';
  }
  let d = '';
  let prefix = 'M';
  for (const p of points) {
    d += `${prefix}${p.x},${p.y}`;
    prefix = 'L';
  }
  return d;
}

// ── Bar chart (horizontal) ───────────────────────────────────────────────────

/** One top-N item as consumed by the layout (mirrors the wire shape). */
export interface TopItemInput {
  key: string;
  value: number;
}

/** One laid-out horizontal bar: geometry + the text label (the key). */
export interface LaidBar {
  key: string;
  value: number;
  x: number;
  y: number;
  w: number;
  h: number;
  /** Category text label (== key); the renderer also shows the value text. */
  label: string;
}

/** The full bar-chart layout: positioned bars + the value (x) axis ticks. */
export interface BarChartLayout {
  bars: LaidBar[];
  xTicks: XTick[];
}

/** Vertical gap between stacked bars, in px. */
const BAR_GAP = 4;

/**
 * barChartLayout maps a pre-sorted (desc) top-N list onto horizontal bars: each
 * bar starts at the left gutter (x0) and its width is proportional to its value
 * against the max value (the longest bar fills the plot band). Bars are stacked
 * top-to-bottom in INPUT ORDER (the server already sorts desc), each row sized to
 * fill the band height evenly. Degenerate-safe: empty items → no bars; an
 * all-zero set → zero-width bars (no /0). The value (x) axis still gets ticks so
 * the magnitude is readable.
 */
export function barChartLayout(items: TopItemInput[], dims: ChartDims): BarChartLayout {
  const { x0, x1, y0, y1 } = plotBand(dims);
  const bandW = Math.max(0, x1 - x0);
  const bandH = Math.max(0, y1 - y0);

  let vHi = 0;
  for (const it of items) {
    if (Number.isFinite(it.value) && it.value > vHi) vHi = it.value;
  }

  if (items.length === 0) {
    return { bars: [], xTicks: niceTicks(0, 0, 4).map((value) => ({ value, x: x0 })) };
  }

  // A zero/!finite max collapses every width to 0 without dividing by zero.
  const wScale = scaleLinear()
    .domain([0, vHi > 0 ? vHi : 1])
    .range([0, bandW]);

  // Row height fills the band evenly; the gap is subtracted per row so bars do
  // not touch. Clamp to >= 1px so a tall list still draws visible rows.
  const rowH = bandH / items.length;
  const barH = Math.max(1, rowH - BAR_GAP);

  const bars: LaidBar[] = items.map((it, i) => {
    const value = Number.isFinite(it.value) ? it.value : 0;
    return {
      key: it.key,
      value,
      x: x0,
      y: y0 + i * rowH,
      w: vHi > 0 ? wScale(value) : 0,
      h: barH,
      label: it.key,
    };
  });

  return {
    bars,
    xTicks: niceTicks(0, vHi, 4).map((value) => ({ value, x: x0 + wScale(value) })),
  };
}
