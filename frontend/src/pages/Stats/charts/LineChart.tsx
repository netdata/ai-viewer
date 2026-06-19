import { useId } from 'react';
import type { AggregateBucket } from '../../../api/types';
import { lineChartLayout, linePath } from '../../../viz/statsCharts';
import { formatMetricValue } from './formatMetric';
import styles from './charts.module.css';

// Multi-series line chart for the /stats dashboard (SOW-0076 polish).
// PRESENTATIONAL: props only, no data fetching. SVG-only (one polyline per
// series; theme colors via var() from the design system chart-* tokens).
//
// A11Y CONTRACT:
//   - role="img" + descriptive aria-label (metric + series count);
//   - <title>/<desc> pair backs the accessible name;
//   - text legend differentiates series (color is never the only signal);
//   - empty data renders an accessible "no data" message.

const VIEW_WIDTH = 720;
const VIEW_HEIGHT = 280;
const PADDING = { top: 16, right: 20, bottom: 32, left: 64 };

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
        <title id={titleId}>{`${metric} over time (${bucket})`}</title>
        <desc id={descId}>{label}</desc>

        {/* Plot background band — subtle surface so the gridlines read on top. */}
        <rect
          x={plotLeft}
          y={PADDING.top}
          width={plotRight - plotLeft}
          height={plotBottom - PADDING.top}
          className={styles.chartBg}
        />

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
              <text x={plotLeft - 8} y={t.y + 4} className={styles.yLabel}>
                {formatMetricValue(metric, t.value)}
              </text>
            </g>
          ))}
        </g>

        {/* X tick labels (bucket start times). The format adapts to the bucket
            granularity: hourly shows date + time, daily shows month + day. */}
        <g className={styles.axis}>
          {layout.xTicks.map((t) => (
            <g key={`x-${t.value}`}>
              <line
                x1={t.x}
                x2={t.x}
                y1={PADDING.top}
                y2={plotBottom}
                className={styles.gridline}
              />
              <text x={t.x} y={plotBottom + 18} className={styles.xLabel}>
                {formatXLabel(t.value, bucket)}
              </text>
            </g>
          ))}
        </g>

        {/* Solid axis baselines (bottom + left) so the eye anchors. */}
        <line
          x1={plotLeft}
          x2={plotRight}
          y1={plotBottom}
          y2={plotBottom}
          className={styles.axisBaseline}
        />
        <line
          x1={plotLeft}
          x2={plotLeft}
          y1={PADDING.top}
          y2={plotBottom}
          className={styles.axisBaseline}
        />

        {/* One polyline per series; stroke is a theme var() (--chart-1..5). */}
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

        {/* Single-point markers: a lone bucket emits an M-only path which
            draws nothing, so a one-bucket trend would look blank. A small
            filled dot (series color) makes that sample visible. */}
        {layout.series.map((s) =>
          s.points.length === 1 && s.points[0] ? (
            <circle
              key={s.key}
              data-marker={s.key || 'total'}
              cx={s.points[0].x}
              cy={s.points[0].y}
              r={4}
              fill={s.color}
              className={styles.marker}
            />
          ) : null,
        )}
      </svg>

      {/* Text legend — the series differentiator (color is never the only cue). */}
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

// formatXLabel adapts to the bucket granularity so an hourly chart shows
// date + time, a daily chart shows month + day, and a weekly chart shows the
// month range — never a full 4-digit-year timestamp, which crowds the axis.
function formatXLabel(us: number, bucket: 'hourly' | 'daily'): string {
  const d = new Date(us / 1000);
  if (bucket === 'hourly') {
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) +
      ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  }
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}