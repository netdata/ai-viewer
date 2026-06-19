import { useEffect, useId, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent, UIEvent } from 'react';
import type { TraceNode, WaterfallRow } from '../../../viz/trace';
import {
  cullByY,
  layoutWaterfall,
  timeAxisTicks,
  traceTimeBounds,
} from '../../../viz/trace';
import { colorForOpKind, colorForStatus } from '../../../viz/color';
import { attachZoom } from '../../../viz/zoomInteraction';
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
//
// The Detailed view is zoomable/pannable on the TIME (X) axis only (ui-pages.md
// §Trace big-session navigation): SHIFT+wheel zooms time, primary-button drag
// pans time, while a PLAIN wheel scrolls the rows vertically (the native
// scroller). The left row-label gutter (x < LABEL_WIDTH) and the turn-boundary
// rules stay FIXED — they are row markers, not time markers — so only the time
// track scales/translates. This mirrors the Timeline tab's X-only convention via
// the shared viz/zoomInteraction (one wheel convention, no drift). The transform
// applied to the time track is matrix(k,0,0,1,tx,0): X-scale k, Y untouched (row
// height constant under zoom), X-translate tx; ty from d3-zoom is ignored (the
// vertical axis is the native scroller, not the transform).

const ROW_HEIGHT = 26;
const LABEL_WIDTH = 220;
const AXIS_HEIGHT = 22;
const CANVAS_VIEWPORT = 460;
const CULL_OVERSCAN = 6;
const TRACK_WIDTH = 720;

/**
 * trackMatrix builds the SVG transform for the Detailed waterfall's TIME TRACK:
 * X scaled by the d3-zoom k, Y left at scale 1 (row height constant), X
 * translated by tx. It deliberately does NOT carry ty (unlike viz/timeline's
 * timeXOnlyMatrix) — the waterfall's vertical axis is the native scroller, so the
 * track must never move in Y under zoom/pan. matrix(a,b,c,d,e,f): a=X-scale,
 * d=Y-scale, e/f=translate.
 */
function trackMatrix(k: number, tx: number): string {
  return `matrix(${k},0,0,1,${tx},0)`;
}

export interface WaterfallProps {
  nodes: TraceNode[];
  onSelect: (_node: TraceNode) => void;
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
  onSelect: (_node: TraceNode) => void;
  selectedId: string | null;
  boundaries: ReadonlySet<string>;
}

function WaterfallSvg({ rows, ticks, onSelect, selectedId, boundaries }: InnerProps) {
  const height = AXIS_HEIGHT + rows.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;
  const svgRef = useRef<SVGSVGElement>(null);
  // The innermost group d3-zoom transforms (the TIME track). Its matrix scales
  // the X axis only; the gutter labels + turn rules live OUTSIDE it (fixed).
  const trackRef = useRef<SVGGElement>(null);
  // A stable unique id so the track clipPath does not collide with another
  // waterfall instance on the page (e.g. the By-turn expanded view).
  const clipId = useId().replace(/:/g, '');
  // Spans new since the previous render (a live session_changed refetch grew the
  // trace) fade in — SOW-0006 AC#6. fadeClassFor withholds the class under
  // prefers-reduced-motion, and the @keyframes is disabled there too.
  const newIds = useNewlyAppeared(rows.map((r) => r.node.op.id));

  // X-only time zoom/pan (ui-pages.md §Trace Detailed): SHIFT+wheel zooms, drag
  // pans, plain wheel scrolls the rows (plainWheelPan:false → the filter rejects
  // a plain wheel without preventDefault, so it reaches the native scroller). We
  // use only k + x from the transform (ty ignored — vertical is the scroller).
  // Re-attached only when the geometry that defines the surface changes.
  useEffect(() => {
    const svg = svgRef.current;
    const track = trackRef.current;
    if (!svg || !track) {
      return;
    }
    const { dispose } = attachZoom<SVGSVGElement>(
      svg,
      (event) => {
        const t = event.transform;
        track.setAttribute('transform', trackMatrix(t.k, t.x));
      },
      { plainWheelPan: false },
    );
    return dispose;
  }, [totalWidth, height]);

  return (
    <div
      className={styles.vizScroller}
      role="group"
      aria-label="Trace waterfall"
    >
      <svg
        ref={svgRef}
        width={totalWidth}
        height={height}
        viewBox={`0 0 ${totalWidth} ${height}`}
        className={styles.vizSvg}
      >
        {/* Clip the time track to its region so panned content never overdraws
            the fixed gutter. */}
        <defs>
          <clipPath id={clipId}>
            <rect x={LABEL_WIDTH} y={0} width={TRACK_WIDTH} height={height} />
          </clipPath>
        </defs>

        {/* FIXED layer (never transformed): row labels in the left gutter and the
            turn-boundary rules. These are ROW markers, not time markers — they
            must not move under X zoom/pan. */}
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
        <g>
          {rows.map((row) => {
            const { op } = row.node;
            const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
            return (
              <text
                key={`lbl-${op.id}`}
                x={6 + indent}
                y={AXIS_HEIGHT + row.y + ROW_HEIGHT / 2 + 4}
                className={styles.rowLabel}
              >
                {op.name || op.id}
              </text>
            );
          })}
        </g>

        {/* TIME TRACK layer: clipped to the track region, shifted so track-local
            x=0 sits at the gutter edge, then the inner <g ref={trackRef}> applies
            the X-only zoom matrix. Drawn in TRACK-LOCAL x (row.x / t.x), so a bar
            at row.x renders at user x LABEL_WIDTH+row.x at identity — unchanged. */}
        <g clipPath={`url(#${clipId})`}>
          <g transform={`translate(${LABEL_WIDTH},0)`}>
            <g ref={trackRef}>
              {/* Axis gridlines + tick labels along the top of the time track. */}
              <g className={styles.axis}>
                {ticks.map((t) => (
                  <g key={t.value} transform={`translate(${t.x},0)`}>
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
                const barClass = [
                  op.id === selectedId ? styles.barSelected : styles.bar,
                  fadeClassFor(op.id, newIds, styles.fadeIn),
                ]
                  .filter(Boolean)
                  .join(' ');
                const fill = colorForOpKind(op.kind);
                // Source-aware (P2): a point-event/running op (row.instant) recorded
                // no measured duration — its label segment reads "instant", never a
                // fabricated "0µs" (a point op is persisted as duration_us==0).
                const durLabel = row.instant ? 'instant' : formatDuration(op.duration_us);
                const label = `${op.name || op.id} — ${op.kind} — ${durLabel} — ${op.status}`;
                const activate = () => {
                  onSelect(row.node);
                };
                const onKeyDown = (e: KeyboardEvent<SVGElement>): void => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelect(row.node);
                  }
                };
                // Source-aware (P2#3): a point-event/running op (no measured span)
                // is a vertical tick at start_ts; a measured op is a duration bar.
                // A wide transparent hit-rect keeps the thin tick easy to click.
                return row.instant ? (
                  <g key={op.id}>
                    <line
                      role="button"
                      aria-label={label}
                      tabIndex={0}
                      x1={row.x}
                      y1={y + 4}
                      x2={row.x}
                      y2={y + ROW_HEIGHT - 4}
                      stroke={failed ? colorForStatus('failed') : fill}
                      strokeWidth={op.id === selectedId ? 3 : 2}
                      className={barClass}
                      onClick={activate}
                      onKeyDown={onKeyDown}
                    />
                    <rect
                      x={row.x - 4}
                      y={y + 4}
                      width={8}
                      height={ROW_HEIGHT - 8}
                      fill="transparent"
                      onClick={activate}
                    />
                  </g>
                ) : (
                  <rect
                    key={op.id}
                    role="button"
                    aria-label={label}
                    tabIndex={0}
                    x={row.x}
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
                );
              })}
            </g>
          </g>
        </g>
      </svg>
    </div>
  );
}

function WaterfallCanvas({ rows, ticks, onSelect, selectedId, boundaries }: InnerProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  // X-only time zoom/pan transform (Y stays the native scroller via scrollTop).
  // k = X scale, tx = X translate; ty from d3-zoom is ignored.
  const [transform, setTransform] = useState({ k: 1, tx: 0 });
  const totalHeight = rows.length * ROW_HEIGHT;
  const totalWidth = LABEL_WIDTH + TRACK_WIDTH;

  // Attach the shared X-only zoom to the <canvas>. plainWheelPan:false so a plain
  // wheel reaches the native vertical scroller (rows scroll) while SHIFT+wheel
  // zooms and drag pans the time track. Re-attached only on geometry change.
  useEffect(() => {
    const c = canvasRef.current;
    if (!c) {
      return;
    }
    const { dispose } = attachZoom<HTMLCanvasElement>(
      c,
      (event) => {
        const tr = event.transform;
        setTransform({ k: tr.k, tx: tr.x });
      },
      { plainWheelPan: false },
    );
    return dispose;
  }, [totalWidth]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    const ctx = canvas.getContext('2d');
    if (!ctx) {
      return;
    }
    const { k, tx } = transform;
    const dpr = typeof window !== 'undefined' ? window.devicePixelRatio || 1 : 1;
    canvas.width = totalWidth * dpr;
    canvas.height = CANVAS_VIEWPORT * dpr;
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, totalWidth, CANVAS_VIEWPORT);
    ctx.font = '12px sans-serif';
    ctx.textBaseline = 'middle';

    // Only the rows overlapping the viewport are drawn (Y cull).
    const visible = cullByY(rows, scrollTop, CANVAS_VIEWPORT, ROW_HEIGHT, CULL_OVERSCAN);

    // Track region screen-x of a track-local x (NOT ctx.transform — that would
    // drag the fixed gutter labels too): screenX = LABEL_WIDTH + trackX*k + tx.
    const trackScreenX = (trackX: number): number => LABEL_WIDTH + trackX * k + tx;
    // A track element is X-visible when its [screenX, screenX+width] overlaps the
    // track region [LABEL_WIDTH, LABEL_WIDTH+TRACK_WIDTH] (in addition to the Y
    // cull above) — panned-off content is skipped.
    const xVisible = (screenX: number, w: number): boolean =>
      screenX + w >= LABEL_WIDTH && screenX <= LABEL_WIDTH + TRACK_WIDTH;

    // Clip the track drawing (bars/ticks/gridlines) so panned content never
    // overdraws the fixed gutter. Row labels are drawn OUTSIDE this clip below.
    ctx.save();
    ctx.beginPath();
    ctx.rect(LABEL_WIDTH, 0, TRACK_WIDTH, CANVAS_VIEWPORT);
    ctx.clip();

    // Gridlines for the visible band (track-local x scaled by the zoom).
    ctx.strokeStyle = 'rgba(128,128,128,0.18)';
    ctx.lineWidth = 1;
    for (const t of ticks) {
      const gx = trackScreenX(t.x);
      if (!xVisible(gx, 0)) {
        continue;
      }
      ctx.beginPath();
      ctx.moveTo(gx, 0);
      ctx.lineTo(gx, CANVAS_VIEWPORT);
      ctx.stroke();
    }

    for (const row of visible) {
      const { op } = row.node;
      const y = row.y - scrollTop;
      const fill = colorForOpKind(op.kind);
      // Turn-boundary rule above a row that starts a new turn (decision #6). The
      // rule is a ROW marker spanning the track region, so it is not X-culled.
      if (boundaries.has(op.id)) {
        ctx.strokeStyle = 'rgba(128,128,128,0.5)';
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(LABEL_WIDTH, y);
        ctx.lineTo(LABEL_WIDTH + TRACK_WIDTH, y);
        ctx.stroke();
      }
      // Source-aware (P2#3): point-event/running ops paint as a vertical tick at
      // start_ts; measured ops paint as a duration bar.
      const barX = trackScreenX(row.x);
      if (row.instant) {
        if (!xVisible(barX, 0)) {
          continue;
        }
        ctx.strokeStyle = op.error_class !== null ? colorForStatus('failed') : fill;
        ctx.lineWidth = op.id === selectedId ? 3 : 2;
        ctx.beginPath();
        ctx.moveTo(barX, y + 4);
        ctx.lineTo(barX, y + ROW_HEIGHT - 4);
        ctx.stroke();
      } else {
        const barW = row.width * k;
        if (!xVisible(barX, barW)) {
          continue;
        }
        ctx.fillStyle = fill;
        ctx.fillRect(barX, y + 4, barW, ROW_HEIGHT - 8);
        if (op.error_class !== null) {
          ctx.strokeStyle = colorForStatus('failed');
          ctx.lineWidth = 2;
          ctx.strokeRect(barX, y + 4, barW, ROW_HEIGHT - 8);
        }
        if (op.id === selectedId) {
          ctx.strokeStyle = 'rgba(255,255,255,0.7)';
          ctx.lineWidth = 1;
          ctx.strokeRect(barX - 1, y + 3, barW + 2, ROW_HEIGHT - 6);
        }
      }
    }
    ctx.restore(); // end track clip

    // Row labels in the FIXED gutter — drawn outside the track clip so a panned
    // track never overwrites them (they are row markers, not time markers).
    ctx.fillStyle = 'rgba(160,160,170,0.95)';
    for (const row of visible) {
      const { op } = row.node;
      const y = row.y - scrollTop;
      const indent = Math.min(row.depth * 10, LABEL_WIDTH - 40);
      ctx.fillText(op.name || op.id, 6 + indent, y + ROW_HEIGHT / 2, LABEL_WIDTH - 12 - indent);
    }
    ctx.restore();
  }, [rows, ticks, scrollTop, selectedId, totalWidth, boundaries, transform]);

  const onScroll = (e: UIEvent<HTMLDivElement>): void => {
    setScrollTop(e.currentTarget.scrollTop);
  };

  // A click maps to the row under the cursor (hit-test by Y), then to the op.
  // X-zoom does not change this: clicking anywhere on a row selects its op.
  const onClick = (e: MouseEvent<HTMLDivElement>): void => {
    const target = e.currentTarget.querySelector('canvas');
    if (!target) {
      return;
    }
    const bounds = target.getBoundingClientRect();
    const yInContent = e.clientY - bounds.top + scrollTop;
    const row = rowAtContentY(rows, yInContent);
    if (row !== null) {
      onSelect(row.node);
    }
  };

  return (
    // The onClick is a pointer-only pixel hit-test over the <canvas>. NOTE:
    // unlike the timeline canvas, this Canvas-mode waterfall (used only above
    // SVG_SPAN_CEILING) has NO focusable per-span DOM fallback, so this is a
    // REAL keyboard-access gap, not a clean false positive — keyboard users can
    // only select spans in the default SVG renderer (WaterfallSvg, fully
    // keyboard-operable). The disable unblocks the lint gate; the gap is
    // explicitly tracked for the SOW-0012 Chunk D viz/<chart>/a11y.md waiver +
    // a follow-up to add a focusable-span fallback list mirroring
    // TimelineRenderer's (the canvasFallbackList) / the SVG waterfall.
    // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions
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

function rowAtContentY(rows: readonly WaterfallRow[], yInContent: number): WaterfallRow | null {
  const targetIndex = Math.floor(yInContent / ROW_HEIGHT);
  if (!Number.isFinite(yInContent) || targetIndex < 0 || targetIndex >= rows.length) {
    return null;
  }
  return rows.at(targetIndex) ?? null;
}
