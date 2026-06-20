import { useMemo } from 'react';
import { cn } from '../../lib/utils';

// Heatmap — SOW-0087 chunk 2 (B3). A small-multiples grid showing event
// counts per (day-of-week, hour-of-day) cell. Used on /stats for
// "failures by hour-of-day" — the classic observability heatmap.
//
// The grid has 7 columns (Sun..Sat, computed from local TZ) × 24 rows
// (00..23). Each cell's intensity comes from `count` — a map keyed by
// "day:hour" strings. Cells with zero events render as the empty state.
//
// Color scale is a 6-step gradient from neutral to status-failed. The
// intensity buckets are absolute (0-5+) so the heatmap is comparable
// across renders without re-scaling.
//
// The component is pure presentational; the data shape is whatever the
// page hands in (computed client-side from /api/sessions or a future
// dedicated endpoint). This keeps the component reusable.

export interface HeatmapProps {
  /** Map keyed by `"day:hour"` (e.g. `"3:14"` for Wed 14:00). Values are
   *  raw counts. Missing keys are treated as 0. */
  counts: Readonly<Record<string, number>>;
  /** Color of the cells with the most events (default status-failed). */
  tone?: 'failed' | 'completed' | undefined;
  /** Number of intensity buckets (default 5). */
  steps?: number;
  /** Whether to show the day labels on the Y axis (default true). */
  showDayLabels?: boolean;
  /** Whether to show the hour labels on the X axis (default true). */
  showHourLabels?: boolean;
}

const DAYS: readonly string[] = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const HOURS: readonly number[] = Array.from({ length: 24 }, (_, i) => i);

// computeIntensity maps a count to a 0-based bucket index. We cap at
// `steps - 1` (the hottest color) so outliers don't blow out the scale.
function computeIntensity(count: number, max: number, steps: number): number {
  if (count <= 0 || max <= 0) return 0;
  const ratio = count / max;
  // Map [0, 1] linearly to [0, steps-1] and round.
  return Math.min(steps - 1, Math.max(0, Math.round(ratio * (steps - 1))));
}

export function Heatmap({
  counts,
  tone = 'failed',
  steps = 5,
  showDayLabels = true,
  showHourLabels = true,
}: HeatmapProps) {
  const max = useMemo(() => {
    let m = 0;
    for (const v of Object.values(counts)) {
      if (v > m) m = v;
    }
    return m;
  }, [counts]);

  return (
    <div
      className="overflow-x-auto"
      role="img"
      aria-label="Heatmap of events by day-of-week and hour-of-day"
    >
      <div className="inline-block min-w-full">
        {/* Hour header row */}
        {showHourLabels ? (
          <div
            className="grid text-[10px] font-medium uppercase tracking-wider text-muted-foreground"
            style={{
              gridTemplateColumns: `auto repeat(${HOURS.length}, minmax(1.5rem, 1fr))`,
            }}
          >
            <div className="px-2 pb-1" />
            {HOURS.map((h) => (
              <div
                key={h}
                className="px-0.5 pb-1 text-center tabular-nums"
                title={`${h.toString().padStart(2, '0')}:00`}
              >
                {h % 6 === 0 ? h.toString().padStart(2, '0') : ''}
              </div>
            ))}
          </div>
        ) : null}

        {/* Day rows */}
        <div className="space-y-0.5">
          {DAYS.map((day, dayIdx) => (
            <div
              key={day}
              className="grid items-center"
              style={{
                gridTemplateColumns: `auto repeat(${HOURS.length}, minmax(1.5rem, 1fr))`,
              }}
            >
              {showDayLabels ? (
                <div className="w-12 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                  {day}
                </div>
              ) : (
                <div />
              )}
              {HOURS.map((h) => {
                const key = `${dayIdx}:${h}`;
                const count = counts[key] ?? 0;
                const intensity = computeIntensity(count, max, steps);
                const opacity = intensity === 0 ? 0 : 0.15 + (intensity / steps) * 0.7;
                return (
                  <div
                    key={h}
                    className={cn(
                      'h-5 border border-foreground/5',
                      count === 0 && 'bg-muted/30',
                      count > 0 && tone === 'failed' && 'bg-status-failed',
                      count > 0 && tone === 'completed' && 'bg-status-completed',
                    )}
                    style={count > 0 ? { opacity } : undefined}
                    title={`${day} ${h.toString().padStart(2, '0')}:00 — ${count} ${count === 1 ? 'event' : 'events'}`}
                  />
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
