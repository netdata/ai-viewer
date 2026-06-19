import { useMemo, useState } from 'react';
import type { KeyboardEvent } from 'react';
import type { TraceNode, TurnRollup } from '../../../viz/trace';
import { buildTurnRollup, SVG_SPAN_CEILING, timeAxisTicks } from '../../../viz/trace';
import { scaleLinear } from 'd3-scale';
import type { TurnDetail } from '../../../api/types';
import { colorForStatus } from '../../../viz/color';
import { formatDuration } from '../../../lib/format';
import { Waterfall } from './Waterfall';
import styles from './TraceTab.module.css';

// By-turn waterfall (ui-pages.md §Trace, decision #6, "By-turn" view): one
// aggregated bar per turn (rolled up from that turn's ops via
// viz/trace.buildTurnRollup) on a shared time axis. Clicking a turn EXPANDS it
// into its individual ops, rendered by the standard per-op Waterfall scoped to
// that turn — the same component the Detailed view uses, so the op rendering
// (source-aware ticks, failed outline, click→drawer) is identical. This keeps a
// huge session navigable: the operator sees N turn bars first, then drills into
// the one turn they care about (performance-first — the per-op layout is built
// only for the expanded turn).

const ROW_HEIGHT = 30;
const LABEL_WIDTH = 220;
const AXIS_HEIGHT = 22;
const TRACK_WIDTH = 720;

export interface ByTurnWaterfallProps {
  turns: TurnDetail[];
  onSelect: (_node: TraceNode) => void;
  selectedId: string | null;
  /** Force the expanded per-op Waterfall's render path (tests). When omitted the
   *  expanded turn decides SVG vs Canvas from ITS OWN op count (one turn is far
   *  smaller than the session), not the whole-session count. The turn bars
   *  themselves are always SVG (a session has far fewer turns than ops). */
  useCanvas?: boolean;
}

export function ByTurnWaterfall({ turns, onSelect, selectedId, useCanvas }: ByTurnWaterfallProps) {
  const rollups = useMemo(() => buildTurnRollup(turns), [turns]);
  const [expanded, setExpanded] = useState<number | null>(null);

  // Shared time window across all turn bars: earliest start → latest end (a turn
  // with no closed op contributes its start so its bar still anchors).
  const [t0, t1] = useMemo(() => timeBounds(rollups), [rollups]);
  const ticks = timeAxisTicks(t0, t1, TRACK_WIDTH, 6);
  const x = scaleLinear()
    .domain([t0, t1 === t0 ? t0 + 1 : t1])
    .range([0, TRACK_WIDTH]);

  const height = AXIS_HEIGHT + rollups.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;

  return (
    <div className={styles.byTurnWrap}>
      <div className={styles.vizScroller} role="group" aria-label="Trace by-turn view">
        <svg
          width={totalWidth}
          height={height}
          viewBox={`0 0 ${totalWidth} ${height}`}
          className={styles.vizSvg}
        >
          <g className={styles.axis}>
            {ticks.map((t) => (
              <g key={t.value} transform={`translate(${LABEL_WIDTH + t.x},0)`}>
                <line y1={AXIS_HEIGHT} y2={height} className={styles.gridline} />
                <text x={2} y={14} className={styles.axisLabel}>
                  {formatDuration(t.value - (ticks[0]?.value ?? 0))}
                </text>
              </g>
            ))}
          </g>

          {rollups.map((r, i) => {
            const y = AXIS_HEIGHT + i * ROW_HEIGHT;
            const end = r.end_ts ?? t1;
            const x0 = x(r.start_ts);
            const w = Math.max(4, x(end) - x0);
            const isOpen = expanded === r.turn.seq;
            const dur = r.end_ts !== null ? formatDuration(r.end_ts - r.start_ts) : 'ongoing';
            const label = `Turn ${r.turn.seq} — ${r.op_count} op${r.op_count === 1 ? '' : 's'} — ${dur}${isOpen ? ' (expanded)' : ''}`;
            const failed = r.turn.status === 'failed';
            const toggle = (): void => {
              setExpanded(isOpen ? null : r.turn.seq);
            };
            const onKeyDown = (e: KeyboardEvent<SVGRectElement>): void => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                toggle();
              }
            };
            return (
              <g key={r.turn.id}>
                <text x={6} y={y + ROW_HEIGHT / 2 + 4} className={styles.rowLabel}>
                  {`Turn ${r.turn.seq} · ${r.op_count} op${r.op_count === 1 ? '' : 's'}`}
                </text>
                <rect
                  role="button"
                  aria-expanded={isOpen}
                  aria-label={label}
                  tabIndex={0}
                  x={LABEL_WIDTH + x0}
                  y={y + 5}
                  width={w}
                  height={ROW_HEIGHT - 10}
                  rx={3}
                  fill="var(--accent)"
                  stroke={failed ? colorForStatus('failed') : 'transparent'}
                  strokeWidth={failed ? 2 : 0}
                  className={isOpen ? styles.barSelected : styles.bar}
                  onClick={toggle}
                  onKeyDown={onKeyDown}
                />
              </g>
            );
          })}
        </svg>
      </div>

      {/* The expanded turn's per-op waterfall (the standard component, scoped to
          that turn). Rendered below the turn bars so the operator keeps the
          turn-level context while drilling in. */}
      {expanded !== null
        ? (() => {
            const open = rollups.find((r) => r.turn.seq === expanded);
            if (!open) {
              return null;
            }
            return (
              <section className={styles.expandedTurn} aria-label={`Turn ${expanded} operations`}>
                <Waterfall
                  nodes={open.ops}
                  onSelect={onSelect}
                  selectedId={selectedId}
                  useCanvas={useCanvas ?? open.ops.length > SVG_SPAN_CEILING}
                />
              </section>
            );
          })()
        : null}
    </div>
  );
}

/** timeBounds returns [min start, max end] across the turn rollups, counting a
 *  null end (no closed op) as its start. Returns [0,0] for no turns. */
function timeBounds(rollups: TurnRollup[]): [number, number] {
  if (rollups.length === 0) {
    return [0, 0];
  }
  let lo = Infinity;
  let hi = -Infinity;
  for (const r of rollups) {
    if (r.start_ts < lo) {
      lo = r.start_ts;
    }
    const end = r.end_ts ?? r.start_ts;
    if (end > hi) {
      hi = end;
    }
  }
  return [lo, hi];
}
