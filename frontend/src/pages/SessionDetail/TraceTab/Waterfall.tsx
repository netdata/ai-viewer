import { useEffect, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent, UIEvent } from 'react';
import type { TraceNode, WaterfallRow } from '../../../viz/trace';
import {
  cullByY,
  layoutWaterfall,
  timeAxisTicks,
  traceTimeBounds,
} from '../../../viz/trace';
import { colorForOpKind, colorForStatus } from '../../../viz/color';
import { fadeClassFor } from '../../../viz/spanFade';
import { useNewlyAppeared } from '../../../viz/useNewlyAppeared';
import { formatDuration } from '../../../lib/format';
import styles from './TraceTab.module.css';

// Waterfall rendering (ui-pages.md §/sessions/:id #3, default Trace view):
// a Chrome-DevTools-Network-style horizontal waterfall — one bar per op on a
// shared time axis, indented by tree depth, colored by op kind, failed ops
// outlined with the error token. SVG for typical sessions (inspectable, one DOM
// node per span); Canvas + viewport culling above the SVG span ceiling so a big
// trace stays fast (frontend-architecture.md §Performance Budgets). All geometry
// comes from viz/trace (the D3 boundary); this file only paints + handles clicks.

const ROW_HEIGHT = 26;
const LABEL_WIDTH = 220;
const AXIS_HEIGHT = 22;
const CANVAS_VIEWPORT = 460;
const CULL_OVERSCAN = 6;
const TRACK_WIDTH = 720;

export interface WaterfallProps {
  nodes: TraceNode[];
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
  /** Force a render path (tests); defaults to op-count vs the SVG ceiling. */
  useCanvas: boolean;
  /** Op ids that START a new turn (excluding the first): a separator rule is
   *  drawn above each so inter-turn gaps read as "between turns" (decision #6,
   *  Detailed view). Omitted when there is nothing to delineate. */
  turnBoundaryIds?: ReadonlySet<string>;
}

export function Waterfall({ nodes, onSelect, selectedId, useCanvas, turnBoundaryIds }: WaterfallProps) {
  const [t0, t1] = traceTimeBounds(nodes);
  const rows = layoutWaterfall(nodes, {
    width: TRACK_WIDTH,
    rowHeight: ROW_HEIGHT,
    t0,
    t1,
  });
  const ticks = timeAxisTicks(t0, t1, TRACK_WIDTH, 6);
  const boundaries = turnBoundaryIds ?? EMPTY_BOUNDARIES;

  if (useCanvas) {
    return (
      <WaterfallCanvas
        rows={rows}
        ticks={ticks}
        onSelect={onSelect}
        selectedId={selectedId}
        boundaries={boundaries}
      />
    );
  }
  return (
    <WaterfallSvg
      rows={rows}
      ticks={ticks}
      onSelect={onSelect}
      selectedId={selectedId}
      boundaries={boundaries}
    />
  );
}

/** Stable empty set so an omitted turnBoundaryIds prop is referentially stable. */
const EMPTY_BOUNDARIES: ReadonlySet<string> = new Set<string>();

interface InnerProps {
  rows: WaterfallRow[];
  ticks: { value: number; x: number }[];
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
  boundaries: ReadonlySet<string>;
}

function WaterfallSvg({ rows, ticks, onSelect, selectedId, boundaries }: InnerProps) {
  const height = AXIS_HEIGHT + rows.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;
  // Spans new since the previous render (a live session_changed refetch grew the
  // trace) fade in — SOW-0006 AC#6. fadeClassFor withholds the class under
  // prefers-reduced-motion, and the @keyframes is disabled there too.
  const newIds = useNewlyAppeared(rows.map((r) => r.node.op.id));
  return (
    <div
      className={styles.vizScroller}
      role="group"
      aria-label="Trace waterfall"
    >
      <svg
        width={totalWidth}
        height={height}
        viewBox={`0 0 ${totalWidth} ${height}`}
        className={styles.vizSvg}
      >
        {/* Axis gridlines + tick labels along the top of the time track. */}
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

        {/* Turn-boundary rules: a horizontal separator above each row that
            starts a new turn, so inter-turn gaps read as "between turns"
            (decision #6, Detailed view). */}
        <g className={styles.axis}>
          {rows.map((row) =>
            boundaries.has(row.node.op.id) ? (
              <line
                key={`tb-${row.node.op.id}`}
                x1={0}
                x2={totalWidth}
                y1={AXIS_HEIGHT + row.y}
                y2={AXIS_HEIGHT + row.y}
                className={styles.turnBoundary}
              />
            ) : null,
          )}
        </g>

        {rows.map((row) => {
          const { op } = row.node;
          const failed = op.error_class !== null;
          const y = AXIS_HEIGHT + row.y;
          const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
          const barClass = [
            op.id === selectedId ? styles.barSelected : styles.bar,
            fadeClassFor(op.id, newIds, styles.fadeIn),
          ]
            .filter(Boolean)
            .join(' ');
          const fill = colorForOpKind(op.kind);
          const label = `${op.name || op.id} — ${op.kind} — ${formatDuration(op.duration_us)} — ${op.status}`;
          const activate = () => {
            onSelect(row.node);
          };
          const onKeyDown = (e: KeyboardEvent<SVGElement>): void => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              onSelect(row.node);
            }
          };
          return (
            <g key={op.id}>
              {/* Row label (kind/name) in the left gutter, indented by depth. */}
              <text
                x={6 + indent}
                y={y + ROW_HEIGHT / 2 + 4}
                className={styles.rowLabel}
              >
                {op.name || op.id}
              </text>
              {/* Source-aware (P2#3): a point-event/running op (no measured span)
                  is a vertical tick at start_ts; a measured op is a duration bar.
                  A wide transparent hit-rect keeps the thin tick easy to click. */}
              {row.instant ? (
                <>
                  <line
                    role="button"
                    aria-label={label}
                    tabIndex={0}
                    x1={LABEL_WIDTH + row.x}
                    y1={y + 4}
                    x2={LABEL_WIDTH + row.x}
                    y2={y + ROW_HEIGHT - 4}
                    stroke={failed ? colorForStatus('failed') : fill}
                    strokeWidth={op.id === selectedId ? 3 : 2}
                    className={barClass}
                    onClick={activate}
                    onKeyDown={onKeyDown}
                  />
                  <rect
                    x={LABEL_WIDTH + row.x - 4}
                    y={y + 4}
                    width={8}
                    height={ROW_HEIGHT - 8}
                    fill="transparent"
                    onClick={activate}
                  />
                </>
              ) : (
                <rect
                  role="button"
                  aria-label={label}
                  tabIndex={0}
                  x={LABEL_WIDTH + row.x}
                  y={y + 4}
                  width={row.width}
                  height={ROW_HEIGHT - 8}
                  rx={3}
                  fill={fill}
                  stroke={failed ? colorForStatus('failed') : 'transparent'}
                  strokeWidth={failed ? 2 : 0}
                  className={barClass}
                  onClick={activate}
                  onKeyDown={onKeyDown}
                />
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function WaterfallCanvas({ rows, ticks, onSelect, selectedId, boundaries }: InnerProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const totalHeight = rows.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    const ctx = canvas.getContext('2d');
    if (!ctx) {
      return;
    }
    const dpr = typeof window !== 'undefined' ? window.devicePixelRatio || 1 : 1;
    canvas.width = totalWidth * dpr;
    canvas.height = CANVAS_VIEWPORT * dpr;
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, totalWidth, CANVAS_VIEWPORT);
    ctx.font = '12px sans-serif';
    ctx.textBaseline = 'middle';

    // Gridlines for the visible band.
    ctx.strokeStyle = 'rgba(128,128,128,0.18)';
    ctx.lineWidth = 1;
    for (const t of ticks) {
      ctx.beginPath();
      ctx.moveTo(LABEL_WIDTH + t.x, 0);
      ctx.lineTo(LABEL_WIDTH + t.x, CANVAS_VIEWPORT);
      ctx.stroke();
    }

    // Only the rows overlapping the viewport are drawn (culling).
    const visible = cullByY(rows, scrollTop, CANVAS_VIEWPORT, ROW_HEIGHT, CULL_OVERSCAN);
    for (const row of visible) {
      const { op } = row.node;
      const y = row.y - scrollTop;
      const fill = colorForOpKind(op.kind);
      // Turn-boundary rule above a row that starts a new turn (decision #6).
      if (boundaries.has(op.id)) {
        ctx.strokeStyle = 'rgba(128,128,128,0.5)';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(0, y);
        ctx.lineTo(totalWidth, y);
        ctx.stroke();
      }
      // Source-aware (P2#3): point-event/running ops paint as a vertical tick at
      // start_ts; measured ops paint as a duration bar.
      if (row.instant) {
        ctx.strokeStyle = op.error_class !== null ? colorForStatus('failed') : fill;
        ctx.lineWidth = op.id === selectedId ? 3 : 2;
        ctx.beginPath();
        ctx.moveTo(LABEL_WIDTH + row.x, y + 4);
        ctx.lineTo(LABEL_WIDTH + row.x, y + ROW_HEIGHT - 4);
        ctx.stroke();
      } else {
        ctx.fillStyle = fill;
        ctx.fillRect(LABEL_WIDTH + row.x, y + 4, row.width, ROW_HEIGHT - 8);
        if (op.error_class !== null) {
          ctx.strokeStyle = colorForStatus('failed');
          ctx.lineWidth = 2;
          ctx.strokeRect(LABEL_WIDTH + row.x, y + 4, row.width, ROW_HEIGHT - 8);
        }
        if (op.id === selectedId) {
          ctx.strokeStyle = 'rgba(255,255,255,0.7)';
          ctx.lineWidth = 1;
          ctx.strokeRect(LABEL_WIDTH + row.x - 1, y + 3, row.width + 2, ROW_HEIGHT - 6);
        }
      }
      // Row label (selection highlight is drawn per shape above).
      ctx.fillStyle = 'rgba(160,160,170,0.95)';
      const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
      ctx.fillText(op.name || op.id, 6 + indent, y + ROW_HEIGHT / 2, LABEL_WIDTH - 12 - indent);
    }
    ctx.restore();
  }, [rows, ticks, scrollTop, selectedId, totalWidth, boundaries]);

  const onScroll = (e: UIEvent<HTMLDivElement>): void => {
    setScrollTop(e.currentTarget.scrollTop);
  };

  // A click maps to the row under the cursor (hit-test by Y), then to the op.
  const onClick = (e: MouseEvent<HTMLDivElement>): void => {
    const target = e.currentTarget.querySelector('canvas');
    if (!target) {
      return;
    }
    const bounds = target.getBoundingClientRect();
    const yInContent = e.clientY - bounds.top + scrollTop;
    const idx = Math.floor(yInContent / ROW_HEIGHT);
    const row = rows[idx];
    if (row) {
      onSelect(row.node);
    }
  };

  return (
    <div
      className={styles.vizScroller}
      role="group"
      aria-label="Trace waterfall"
      style={{ maxHeight: CANVAS_VIEWPORT, overflowY: 'auto' }}
      onScroll={onScroll}
      onClick={onClick}
    >
      {/* Tall spacer establishes the scrollable height; the canvas is sticky to
          the viewport and repainted with the culled rows on scroll. */}
      <div style={{ height: totalHeight, position: 'relative', width: totalWidth }}>
        <canvas
          ref={canvasRef}
          className={styles.vizCanvas}
          style={{
            position: 'sticky',
            top: 0,
            width: totalWidth,
            height: CANVAS_VIEWPORT,
          }}
        />
      </div>
    </div>
  );
}
