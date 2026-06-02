import { useId } from 'react';
import type { AggregateBucket } from '../../../api/types';
import { lineChartLayout, linePath } from '../../../viz/statsCharts';
import { formatTimestamp } from '../../../lib/format';
import { formatMetricValue } from './formatMetric';
import styles from './charts.module.css';

// Multi-series line chart for the /stats dashboard (SOW-0007 Chunk 9a).
// PRESENTATIONAL: props only, no data fetching — the 9b page fetches
// /api/stats/aggregate and feeds the buckets here. Bounded data (a few hundred
// points at most: ≤ ~daily buckets × series) → SVG only, no Canvas (mirrors the
// Waterfall SVG path: one <svg> with a viewBox rendering viz/ layout output).
//
// All geometry comes from viz/statsCharts (the D3 boundary); this file only
// paints + labels. Colors are theme-token var() references from the layout, so a
// theme flip recolors with no JS.
//
// A11Y CONTRACT (Chunk 11's axe gate depends on this):
//   - the <svg> is role="img" with a descriptive aria-label summarizing the
//     metric + series count, plus a <title>/<desc> pair (screen readers announce
//     the chart without seeing the lines);
//   - series are distinguished by a TEXT legend (label + swatch), so COLOR IS
//     NEVER THE SOLE SIGNAL;
//   - empty data renders an accessible "no data" message, not a blank svg.

const VIEW_WIDTH = 720;
const VIEW_HEIGHT = 260;
const PADDING = { top: 12, right: 16, bottom: 28, left: 56 };

export interface LineChartProps {
  buckets: AggregateBucket[];
  /** The selected metric (drives value formatting + the aria description). */
  metric: string;
  /** The time-bucket granularity (labels the axis + the description). */
  bucket: 'hourly' | 'daily';
}

export function LineChart({ buckets, metric, bucket }: LineChartProps) {
  const titleId = useId();
  const descId = useId();

  if (buckets.length === 0) {
    // Accessible empty state — a status region, not an undescribed graphic.
    return (
      <p className={styles.empty} role="status">
        No data for the selected metric and time range.
      </p>
    );
  }

  const layout = lineChartLayout(
    buckets.map((b) => ({ bucket_ts: b.bucket_ts, series: b.series })),
    { width: VIEW_WIDTH, height: VIEW_HEIGHT, padding: PADDING },
  );

  const plotBottom = VIEW_HEIGHT - PADDING.bottom;
  const plotLeft = PADDING.left;
  const plotRight = VIEW_WIDTH - PADDING.right;

  const label =
    `Line chart of ${metric} per ${bucket} bucket, ` +
    `${layout.series.length} series: ${layout.series.map((s) => s.key || 'total').join(', ')}.`;

  return (
    <div className={styles.chart}>
      <svg
        viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
        className={styles.svg}
        role="img"
        aria-labelledby={`${titleId} ${descId}`}
      >
        {/* Visually-hidden title/desc back the role="img" accessible name. */}
        <title id={titleId}>{`${metric} over time (${bucket})`}</title>
        <desc id={descId}>{label}</desc>

        {/* Y gridlines + value tick labels. */}
        <g className={styles.axis}>
          {layout.yTicks.map((t) => (
            <g key={`y-${t.value}`}>
              <line
                x1={plotLeft}
                x2={plotRight}
                y1={t.y}
                y2={t.y}
                className={styles.gridline}
              />
              <text x={plotLeft - 6} y={t.y + 4} className={styles.yLabel}>
                {formatMetricValue(metric, t.value)}
              </text>
            </g>
          ))}
        </g>

        {/* X tick labels (bucket start times). */}
        <g className={styles.axis}>
          {layout.xTicks.map((t) => (
            <text key={`x-${t.value}`} x={t.x} y={plotBottom + 18} className={styles.xLabel}>
              {formatTimestamp(t.value)}
            </text>
          ))}
        </g>

        {/* One polyline per series; stroke is a theme var() so it tracks the theme.
            data-series marks value lines (tests + future styling key on it). */}
        <g fill="none">
          {layout.series.map((s) => (
            <path
              key={s.key}
              data-series={s.key || 'total'}
              d={linePath(s.points)}
              stroke={s.color}
              strokeWidth={2}
              className={styles.line}
            />
          ))}
        </g>

        {/* Point markers for SINGLE-POINT series: a lone bucket emits an M-only
            (zero-length) path which draws nothing, so a one-bucket trend would
            look blank. A small filled dot (the series color) makes that sample
            visible. Multi-point series keep their polyline only (no marker
            clutter), so this never changes their rendering. */}
        {layout.series.map((s) =>
          s.points.length === 1 && s.points[0] ? (
            <circle
              key={s.key}
              data-marker={s.key || 'total'}
              cx={s.points[0].x}
              cy={s.points[0].y}
              r={3}
              fill={s.color}
              className={styles.marker}
            />
          ) : null,
        )}
      </svg>

      {/* TEXT legend — the series differentiator (color is never the only cue).
          A labelled list so a screen reader can enumerate the series. */}
      <ul className={styles.legend} aria-label="Chart legend">
        {layout.series.map((s) => (
          <li key={s.key} className={styles.legendItem}>
            <span
              className={styles.swatch}
              style={{ background: s.color }}
              aria-hidden="true"
            />
            <span className={styles.legendLabel}>{s.key || 'total'}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
