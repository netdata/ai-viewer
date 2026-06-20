import { cn } from '../../lib/utils';

// Sparkline — SOW-0085 B2. A tiny inline SVG line chart (no library)
// that fits inside a table cell. Renders an N-point numeric series as
// a polyline with an optional area fill.
//
// The series is normalized to the SVG's height (h). Each point's Y is
// (1 - (v - min) / (max - min)) * h. Empty / constant series render
// as a flat line at mid-height.
//
// Color comes from the parent via `tone`. Three tones are wired to
// semantic CSS tokens: 'default' (muted), 'success', 'failed'.

export interface SparklineProps {
  values: readonly number[];
  width?: number;
  height?: number;
  tone?: 'default' | 'success' | 'failed' | undefined;
  /** Stroke width in px (default 1.5). */
  strokeWidth?: number;
  /** Show the area fill below the line (default true). */
  area?: boolean;
  /** Optional className for the wrapper span (e.g. to set the inline-flex width). */
  className?: string | undefined;
}

export function Sparkline({
  values,
  width = 80,
  height = 16,
  tone = 'default',
  strokeWidth = 1.5,
  area = true,
  className,
}: SparklineProps) {
  const strokeClass =
    tone === 'success'
      ? 'stroke-status-completed'
      : tone === 'failed'
        ? 'stroke-status-failed'
        : 'stroke-muted-foreground';

  const fillClass =
    tone === 'success'
      ? 'fill-status-completed/20'
      : tone === 'failed'
        ? 'fill-status-failed/20'
        : 'fill-muted-foreground/20';

  // Empty series: render an empty placeholder box.
  if (values.length === 0) {
    return (
      <span className={cn('inline-flex items-center', className)} aria-hidden>
        <svg width={width} height={height} aria-hidden focusable="false" />
      </span>
    );
  }

  // Normalize the series.
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min;
  // Avoid divide-by-zero (constant series): draw a flat line at mid-height.
  const yFor = (v: number): number => {
    if (range === 0) return height / 2;
    return height - 2 - ((v - min) / range) * (height - 4); // 2px padding top/bottom
  };
  const xStep = values.length === 1 ? 0 : width / (values.length - 1);

  const points = values
    .map((v, i) => `${(i * xStep).toFixed(1)},${yFor(v).toFixed(1)}`)
    .join(' ');

  // Area path: same as line but closed along the bottom.
  const areaPath =
    area && values.length > 1
      ? `M0,${height} L ${points.replace(/ /g, ' L ')} L ${width},${height} Z`
      : undefined;

  return (
    <span
      className={cn('inline-flex items-center', className)}
      role="img"
      aria-label={`Sparkline, ${values.length} points`}
    >
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        aria-hidden
        focusable="false"
      >
        {areaPath !== undefined ? (
          <path d={areaPath} className={fillClass} />
        ) : null}
        <polyline
          points={points}
          fill="none"
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeLinejoin="round"
          className={strokeClass}
        />
      </svg>
      <span className="sr-only">{`Sparkline values: ${values.join(', ')}`}</span>
    </span>
  );
}

// DurationBar — SOW-0085 F1. A tiny horizontal bar whose width is
// proportional to the session's duration relative to the longest in
// the current view. Pairs nicely with the numeric duration text.

export interface DurationBarProps {
  /** The session's duration in microseconds (end_ts - start_ts). */
  durationUs: number;
  /** The longest duration in the current view, used as the 100% mark. */
  maxDurationUs: number;
  className?: string | undefined;
}

export function DurationBar({ durationUs, maxDurationUs, className }: DurationBarProps) {
  // Width as a percentage of the longest session in view.
  const pct =
    maxDurationUs <= 0
      ? 0
      : Math.max(2, Math.min(100, (durationUs / maxDurationUs) * 100));

  return (
    <span
      className={cn('inline-block h-1.5 w-24 overflow-hidden rounded-full bg-muted', className)}
      aria-hidden
    >
      <span
        className="block h-full bg-primary"
        style={{ width: `${pct}%` }}
      />
    </span>
  );
}
