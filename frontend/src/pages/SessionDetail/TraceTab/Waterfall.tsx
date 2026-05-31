import { useEffect, useRef, useState } from 'react';
import type { MouseEvent, UIEvent } from 'react';
import type { TraceNode, WaterfallRow } from '../../../viz/trace';
import {
  cullByY,
  layoutWaterfall,
  timeAxisTicks,
  traceTimeBounds,
} from '../../../viz/trace';
import { colorForOpKind, colorForStatus } from '../../../viz/color';
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
}

export function Waterfall({ nodes, onSelect, selectedId, useCanvas }: WaterfallProps) {
  const [t0, t1] = traceTimeBounds(nodes);
  const rows = layoutWaterfall(nodes, {
    width: TRACK_WIDTH,
    rowHeight: ROW_HEIGHT,
    t0,
    t1,
  });
  const ticks = timeAxisTicks(t0, t1, TRACK_WIDTH, 6);

  if (useCanvas) {
    return (
      <WaterfallCanvas
        rows={rows}
        ticks={ticks}
        onSelect={onSelect}
        selectedId={selectedId}
      />
    );
  }
  return (
    <WaterfallSvg rows={rows} ticks={ticks} onSelect={onSelect} selectedId={selectedId} />
  );
}

interface InnerProps {
  rows: WaterfallRow[];
  ticks: { value: number; x: number }[];
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
}

function WaterfallSvg({ rows, ticks, onSelect, selectedId }: InnerProps) {
  const height = AXIS_HEIGHT + rows.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;
  return (
    <div
      className={styles.vizScroller}
      role="img"
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

        {rows.map((row) => {
          const { op } = row.node;
          const failed = op.error_class !== null;
          const y = AXIS_HEIGHT + row.y;
          const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
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
              {/* The clickable bar on the time track. */}
              <rect
                role="button"
                aria-label={`${op.name || op.id} — ${op.kind} — ${formatDuration(op.duration_us)} — ${op.status}`}
                tabIndex={0}
                x={LABEL_WIDTH + row.x}
                y={y + 4}
                width={row.width}
                height={ROW_HEIGHT - 8}
                rx={3}
                fill={colorForOpKind(op.kind)}
                stroke={failed ? colorForStatus('failed') : 'transparent'}
                strokeWidth={failed ? 2 : 0}
                className={op.id === selectedId ? styles.barSelected : styles.bar}
                onClick={() => {
                  onSelect(row.node);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelect(row.node);
                  }
                }}
              />
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function WaterfallCanvas({ rows, ticks, onSelect, selectedId }: InnerProps) {
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
      ctx.fillStyle = colorForOpKind(op.kind);
      ctx.fillRect(LABEL_WIDTH + row.x, y + 4, row.width, ROW_HEIGHT - 8);
      if (op.error_class !== null) {
        ctx.strokeStyle = colorForStatus('failed');
        ctx.lineWidth = 2;
        ctx.strokeRect(LABEL_WIDTH + row.x, y + 4, row.width, ROW_HEIGHT - 8);
      }
      ctx.fillStyle = 'rgba(160,160,170,0.95)';
      const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
      ctx.fillText(op.name || op.id, 6 + indent, y + ROW_HEIGHT / 2, LABEL_WIDTH - 12 - indent);
      if (op.id === selectedId) {
        ctx.strokeStyle = 'rgba(255,255,255,0.7)';
        ctx.lineWidth = 1;
        ctx.strokeRect(LABEL_WIDTH + row.x - 1, y + 3, row.width + 2, ROW_HEIGHT - 6);
      }
    }
    ctx.restore();
  }, [rows, ticks, scrollTop, selectedId, totalWidth]);

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
      role="img"
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
