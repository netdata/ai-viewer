import { describe, expect, it } from 'vitest';
import {
  barChartLayout,
  lineChartLayout,
  linePath,
  seriesColorVar,
  type AggregateBucketInput,
  type TopItemInput,
} from './statsCharts';

// Pure layout for the /stats charts (mirrors viz/trace.ts, viz/timeline.ts: no
// React/DOM, deterministic, unit-tested). lineChartLayout pivots the per-bucket
// series rows into one polyline per distinct key on a shared time-x / value-y
// scale; barChartLayout maps a pre-sorted top-N list onto horizontal bars. Both
// are degenerate-input safe (empty, single point, all-equal values, zero span).

const DIMS = { width: 200, height: 100, padding: { top: 4, right: 4, bottom: 16, left: 30 } };

describe('lineChartLayout', () => {
  it('returns no series and bound ticks for empty buckets (no crash)', () => {
    const out = lineChartLayout([], DIMS);
    expect(out.series).toEqual([]);
    // Empty data still yields a usable (degenerate) axis rather than NaN ticks.
    expect(out.xTicks.every((t) => Number.isFinite(t.value) && Number.isFinite(t.x))).toBe(true);
    expect(out.yTicks.every((t) => Number.isFinite(t.value) && Number.isFinite(t.y))).toBe(true);
  });

  it('pivots per-bucket series into one polyline per distinct key', () => {
    const buckets: AggregateBucketInput[] = [
      { bucket_ts: 1000, series: [{ key: 'a', value: 1 }, { key: 'b', value: 5 }] },
      { bucket_ts: 2000, series: [{ key: 'a', value: 3 }, { key: 'b', value: 2 }] },
    ];
    const out = lineChartLayout(buckets, DIMS);
    const keys = out.series.map((s) => s.key).sort();
    expect(keys).toEqual(['a', 'b']);
    const a = out.series.find((s) => s.key === 'a');
    expect(a?.points).toHaveLength(2);
    // Each series carries a stable categorical color var() reference.
    expect(a?.color).toMatch(/^var\(--/);
  });

  it('maps the min/max value to the plot band (y inverted: max near the top)', () => {
    // Use real microsecond timestamps (post-2020) so the filter that drops
    // zero/NaN bucket_ts (the SOW-0076 fix that prevents the 1970-2017 X-axis
    // spread) does not swallow the test data.
    const buckets: AggregateBucketInput[] = [
      { bucket_ts: 1700000000000000, series: [{ key: 'a', value: 0 }] },
      { bucket_ts: 1700001000000000, series: [{ key: 'a', value: 100 }] },
    ];
    const out = lineChartLayout(buckets, DIMS);
    const pts = out.series[0]?.points ?? [];
    const yAtMin = pts[0]?.y ?? 0;
    const yAtMax = pts[1]?.y ?? 0;
    // Larger value -> smaller y (top of the SVG); plot stays within the band.
    expect(yAtMax).toBeLessThan(yAtMin);
    expect(yAtMax).toBeGreaterThanOrEqual(DIMS.padding.top - 0.001);
    expect(yAtMin).toBeLessThanOrEqual(DIMS.height - DIMS.padding.bottom + 0.001);
  });

  it('is single-point safe (one bucket -> one point, finite coords, no /0)', () => {
    const buckets: AggregateBucketInput[] = [{ bucket_ts: 42, series: [{ key: 'a', value: 7 }] }];
    const out = lineChartLayout(buckets, DIMS);
    const pts = out.series[0]?.points ?? [];
    expect(pts).toHaveLength(1);
    expect(Number.isFinite(pts[0]?.x)).toBe(true);
    expect(Number.isFinite(pts[0]?.y)).toBe(true);
  });

  it('is all-equal-values safe (flat series sits inside the band, no /0)', () => {
    const buckets: AggregateBucketInput[] = [
      { bucket_ts: 0, series: [{ key: 'a', value: 5 }] },
      { bucket_ts: 10, series: [{ key: 'a', value: 5 }] },
    ];
    const out = lineChartLayout(buckets, DIMS);
    for (const p of out.series[0]?.points ?? []) {
      expect(Number.isFinite(p.y)).toBe(true);
      expect(p.y).toBeGreaterThanOrEqual(DIMS.padding.top - 0.001);
      expect(p.y).toBeLessThanOrEqual(DIMS.height - DIMS.padding.bottom + 0.001);
    }
  });

  it('orders each series points by bucket_ts ascending regardless of input order', () => {
    const buckets: AggregateBucketInput[] = [
      { bucket_ts: 3000, series: [{ key: 'a', value: 1 }] },
      { bucket_ts: 1000, series: [{ key: 'a', value: 2 }] },
      { bucket_ts: 2000, series: [{ key: 'a', value: 3 }] },
    ];
    const out = lineChartLayout(buckets, DIMS);
    const xs = (out.series[0]?.points ?? []).map((p) => p.x);
    const sorted = [...xs].sort((m, n) => m - n);
    expect(xs).toEqual(sorted);
  });
});

describe('linePath', () => {
  it('returns an empty string for no points', () => {
    expect(linePath([])).toBe('');
  });

  it('builds an M/L SVG path from points', () => {
    const d = linePath([
      { x: 0, y: 0 },
      { x: 10, y: 20 },
    ]);
    expect(d.startsWith('M')).toBe(true);
    expect(d).toContain('L');
  });
});

describe('barChartLayout', () => {
  it('returns no bars for empty items (no crash)', () => {
    const out = barChartLayout([], DIMS);
    expect(out.bars).toEqual([]);
    expect(out.xTicks.every((t) => Number.isFinite(t.value))).toBe(true);
  });

  it('lays one bar per item, widths proportional to value (max fills the band)', () => {
    const items: TopItemInput[] = [
      { key: 'big', value: 100 },
      { key: 'small', value: 25 },
    ];
    const out = barChartLayout(items, DIMS);
    expect(out.bars).toHaveLength(2);
    const big = out.bars.find((b) => b.key === 'big');
    const small = out.bars.find((b) => b.key === 'small');
    expect((big?.w ?? 0)).toBeGreaterThan(small?.w ?? 0);
    // Bars never exceed the plot band width.
    const bandW = DIMS.width - DIMS.padding.left - DIMS.padding.right;
    expect((big?.w ?? 0)).toBeLessThanOrEqual(bandW + 0.001);
  });

  it('stacks bars vertically in input order with non-overlapping rows', () => {
    const items: TopItemInput[] = [
      { key: 'a', value: 3 },
      { key: 'b', value: 2 },
      { key: 'c', value: 1 },
    ];
    const out = barChartLayout(items, DIMS);
    expect(out.bars.map((b) => b.key)).toEqual(['a', 'b', 'c']);
    const ys = out.bars.map((b) => b.y);
    expect(ys).toEqual([...ys].sort((m, n) => m - n));
    // Consecutive rows do not overlap (next y >= prev y + prev height).
    for (let i = 1; i < out.bars.length; i++) {
      const prev = out.bars[i - 1];
      const cur = out.bars[i];
      if (prev && cur) {
        expect(cur.y).toBeGreaterThanOrEqual(prev.y + prev.h - 0.001);
      }
    }
  });

  it('is all-zero safe (zero-value bars collapse to width 0, no /0)', () => {
    const items: TopItemInput[] = [
      { key: 'a', value: 0 },
      { key: 'b', value: 0 },
    ];
    const out = barChartLayout(items, DIMS);
    for (const b of out.bars) {
      expect(b.w).toBe(0);
      expect(Number.isFinite(b.y)).toBe(true);
    }
  });

  it('carries the key as the bar label (text differentiator, never color-only)', () => {
    const out = barChartLayout([{ key: 'shell.Bash', value: 4 }], DIMS);
    expect(out.bars[0]?.label).toBe('shell.Bash');
  });
});

describe('seriesColorVar', () => {
  it('returns a deterministic, cycling var(--…) reference per index', () => {
    const c0 = seriesColorVar(0);
    const c1 = seriesColorVar(1);
    expect(c0).toMatch(/^var\(--/);
    expect(c1).toMatch(/^var\(--/);
    // Same index -> same color (deterministic); the palette cycles for big N.
    expect(seriesColorVar(0)).toBe(c0);
    expect(seriesColorVar(100)).toMatch(/^var\(--/);
  });
});
