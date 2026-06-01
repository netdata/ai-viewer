import { useId } from 'react';
import type { TopItem } from '../../../api/types';
import { barChartLayout, seriesColorVar } from '../../../viz/statsCharts';
import { formatMetricValue } from './formatMetric';
import styles from './charts.module.css';

// Horizontal bar chart for the /stats dashboard top-N (SOW-0007 Chunk 9a).
// PRESENTATIONAL: props only — the 9b page fetches /api/stats/top and feeds the
// already-sorted (desc) items here. SVG only (one <rect> per bar). Geometry from
// viz/statsCharts (the D3 boundary); this file paints + labels.
//
// A11Y CONTRACT (same as LineChart, for Chunk 11's axe gate):
//   - role="img" + aria-label naming the dimension + metric + a <title>/<desc>;
//   - EACH BAR is labelled with its key AND its formatted value as TEXT, so the
//     ranking is readable without relying on bar length or color;
//   - empty data renders an accessible "no data" message.

const VIEW_WIDTH = 720;
const ROW_HEIGHT = 28;
const PADDING = { top: 8, right: 16, bottom: 24, left: 8 };
// Label band reserved on the left of each row for the category + value text.
const LABEL_BAND = 220;

export interface BarChartProps {
  items: TopItem[];
  /** The ranked dimension (model/provider/tool/agent/cwd) — labels the chart. */
  dimension: string;
  /** The selected metric (drives value formatting + the aria description). */
  metric: string;
}

export function BarChart({ items, dimension, metric }: BarChartProps) {
  const titleId = useId();
  const descId = useId();

  if (items.length === 0) {
    return (
      <p className={styles.empty} role="status">
        No data for the selected dimension and time range.
      </p>
    );
  }

  // Height grows with the row count; the bar band sits to the right of the label
  // band so each bar's text never overlaps its rectangle.
  const height = PADDING.top + PADDING.bottom + items.length * ROW_HEIGHT;
  const layout = barChartLayout(
    items.map((it) => ({ key: it.key, value: it.value })),
    {
      width: VIEW_WIDTH - LABEL_BAND,
      height,
      padding: PADDING,
    },
  );

  const label =
    `Bar chart of top ${items.length} ${dimension} by ${metric}: ` +
    items.map((it) => `${it.key} ${formatMetricValue(metric, it.value)}`).join(', ') +
    '.';

  return (
    <div className={styles.chart}>
      <svg
        viewBox={`0 0 ${VIEW_WIDTH} ${height}`}
        className={styles.svg}
        role="img"
        aria-labelledby={`${titleId} ${descId}`}
      >
        <title id={titleId}>{`Top ${dimension} by ${metric}`}</title>
        <desc id={descId}>{label}</desc>

        {layout.bars.map((bar, i) => {
          const rowY = bar.y;
          const textY = rowY + bar.h / 2;
          return (
            <g key={bar.key}>
              {/* Category label (text differentiator — never color-only). */}
              <text x={PADDING.left} y={textY + 4} className={styles.barKey}>
                {bar.label}
              </text>
              {/* The bar, offset past the label band; fill is a theme var(). */}
              <rect
                data-bar={bar.key}
                x={LABEL_BAND + bar.x}
                y={rowY}
                width={bar.w}
                height={bar.h}
                rx={3}
                fill={seriesColorVar(i)}
                className={styles.bar}
              />
              {/* Value text just after the bar end, inside the viewBox. */}
              <text
                x={Math.min(LABEL_BAND + bar.x + bar.w + 6, VIEW_WIDTH - PADDING.right)}
                y={textY + 4}
                className={styles.barValue}
              >
                {formatMetricValue(metric, bar.value)}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}
