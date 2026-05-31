import { useEffect, useRef } from 'react';
import type { MouseEvent } from 'react';
import type { FlameCell, TraceNode } from '../../../viz/trace';
import { layoutFlame } from '../../../viz/trace';
import { colorForOpKind, colorForStatus } from '../../../viz/color';
import { formatDuration } from '../../../lib/format';
import styles from './TraceTab.module.css';

// Flame-graph rendering (ui-pages.md §/sessions/:id #3, alternate Trace view):
// the same op tree stacked by depth (icicle layout from viz/trace.layoutFlame).
// SVG for typical sessions; Canvas above the SVG span ceiling so a large
// flame-graph stays fast (frontend-architecture.md §Performance Budgets). All
// geometry comes from viz/trace; this file only paints + handles clicks.

const ROW_HEIGHT = 22;
const FLAME_WIDTH = 940;
const CANVAS_MAX_HEIGHT = 520;

export interface FlameGraphProps {
  nodes: TraceNode[];
  /** Pre-order roots of the tree (for depth-based stacking). */
  roots: TraceNode[];
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
  useCanvas: boolean;
}

export function FlameGraph({ roots, onSelect, selectedId, useCanvas }: FlameGraphProps) {
  const cells = layoutFlame(roots, { width: FLAME_WIDTH, rowHeight: ROW_HEIGHT });
  const maxDepth = cells.reduce((m, c) => Math.max(m, c.depth), 0);
  const height = (maxDepth + 1) * ROW_HEIGHT;

  if (useCanvas) {
    return (
      <FlameCanvas
        cells={cells}
        height={height}
        onSelect={onSelect}
        selectedId={selectedId}
      />
    );
  }
  return (
    <div className={styles.vizScroller} role="img" aria-label="Trace flame-graph">
      <svg
        width={FLAME_WIDTH}
        height={height}
        viewBox={`0 0 ${FLAME_WIDTH} ${height}`}
        className={styles.vizSvg}
      >
        {cells.map((cell) => {
          const { op } = cell.node;
          const failed = op.error_class !== null;
          return (
            <g key={op.id}>
              <rect
                role="button"
                aria-label={`${op.name || op.id} — ${op.kind} — ${formatDuration(op.duration_us)} — ${op.status}`}
                tabIndex={0}
                x={cell.x}
                y={cell.y}
                width={cell.width}
                height={cell.height - 1}
                fill={colorForOpKind(op.kind)}
                stroke={failed ? colorForStatus('failed') : 'transparent'}
                strokeWidth={failed ? 2 : 0}
                className={op.id === selectedId ? styles.barSelected : styles.bar}
                onClick={() => {
                  onSelect(cell.node);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelect(cell.node);
                  }
                }}
              />
              {cell.width > 36 ? (
                <text
                  x={cell.x + 4}
                  y={cell.y + cell.height / 2 + 3}
                  className={styles.flameLabel}
                  clipPath="inset(0)"
                >
                  {op.name || op.id}
                </text>
              ) : null}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function FlameCanvas({
  cells,
  height,
  onSelect,
  selectedId,
}: {
  cells: FlameCell[];
  height: number;
  onSelect: (node: TraceNode) => void;
  selectedId: string | null;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const drawHeight = Math.min(height, CANVAS_MAX_HEIGHT);

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
    canvas.width = FLAME_WIDTH * dpr;
    canvas.height = height * dpr;
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, FLAME_WIDTH, height);
    ctx.font = '11px sans-serif';
    ctx.textBaseline = 'middle';
    for (const cell of cells) {
      const { op } = cell.node;
      ctx.fillStyle = colorForOpKind(op.kind);
      ctx.fillRect(cell.x, cell.y, cell.width, cell.height - 1);
      if (op.error_class !== null) {
        ctx.strokeStyle = colorForStatus('failed');
        ctx.lineWidth = 2;
        ctx.strokeRect(cell.x, cell.y, cell.width, cell.height - 1);
      }
      if (op.id === selectedId) {
        ctx.strokeStyle = 'rgba(255,255,255,0.7)';
        ctx.lineWidth = 1;
        ctx.strokeRect(cell.x, cell.y, cell.width, cell.height - 1);
      }
      if (cell.width > 36) {
        ctx.fillStyle = 'rgba(20,20,24,0.92)';
        ctx.fillText(op.name || op.id, cell.x + 4, cell.y + cell.height / 2, cell.width - 8);
      }
    }
    ctx.restore();
  }, [cells, height, selectedId]);

  // Hit-test a click against the cell rectangles (cells are few enough even in
  // a large trace because depth is bounded; a linear scan is fine here).
  const onClick = (e: MouseEvent<HTMLCanvasElement>): void => {
    const bounds = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - bounds.left;
    const py = e.clientY - bounds.top;
    for (const cell of cells) {
      if (px >= cell.x && px <= cell.x + cell.width && py >= cell.y && py <= cell.y + cell.height) {
        onSelect(cell.node);
        return;
      }
    }
  };

  return (
    <div
      className={styles.vizScroller}
      role="img"
      aria-label="Trace flame-graph"
      style={{ maxHeight: drawHeight, overflowY: 'auto' }}
    >
      <canvas
        ref={canvasRef}
        className={styles.vizCanvas}
        style={{ width: FLAME_WIDTH, height }}
        onClick={onClick}
      />
    </div>
  );
}
