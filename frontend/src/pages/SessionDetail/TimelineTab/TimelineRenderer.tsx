import { useEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent } from 'react';
import { select } from 'd3-selection';
import { zoom, zoomIdentity, type D3ZoomEvent, type ZoomBehavior } from 'd3-zoom';
import { colorForOpKind } from '../../../viz/color';
import {
  cullSpans,
  timelineScale,
  timeXOnlyMatrix,
  VISIBLE_SPAN_CEILING,
  type CullWindow,
  type PositionedLane,
  type PositionedSpan,
} from '../../../viz/timeline';
import { fadeClassFor } from '../../../viz/spanFade';
import { useNewlyAppeared } from '../../../viz/useNewlyAppeared';
import { formatDuration } from '../../../lib/format';
import styles from './TimelineTab.module.css';

// Timeline graph renderer (ui-pages.md §/sessions/:id #4 "Timeline" — video-editor
// style). Paints the positioned lanes + spans: closed spans as bars colored by op
// kind, NULL-end/point ops as instant tick markers, compaction ops as full-height
// dashed vertical breakpoints, a horizontal time axis, and lane zebra bands.
// Overlap between lanes is intentional (parallel sub-agents). SVG below the
// visible-span ceiling — one clickable DOM node per span, inspectable +
// keyboard-focusable; Canvas with viewport-clipped culling above it so a big
// timeline stays fast (frontend-architecture.md §Performance Budgets). d3-zoom
// drives pan/zoom with the SOW-0006 default: SHIFT+WHEEL zooms the time axis,
// PLAIN WHEEL pans. The D3 boundary holds: this file only paints + wires
// interaction; all geometry comes from viz/timeline. A span click calls
// onSpanClick → the shared SpanDetailDrawer.

const SCALE_EXTENT: [number, number] = [0.2, 64];
const AXIS_HEIGHT = 22; // px reserved at the top for the time axis.
const LANE_LABEL_PAD = 6; // px inset for the lane label text.
const AXIS_TARGET_TICKS = 8;

export interface TimelineRendererProps {
  lanes: PositionedLane[];
  spans: PositionedSpan[];
  width: number;
  /** Pixel height of the whole track (axis + all lanes). */
  height: number;
  /** Overall time window (server t_start / t_end) for the axis + scale. */
  tStart: number;
  tEnd: number;
  selectedId: string | null;
  onSpanClick: (span: PositionedSpan) => void;
  /** Force a render path (tests); defaults to span-count vs VISIBLE_SPAN_CEILING. */
  useCanvas?: boolean;
}

/**
 * zoomEventFilter implements the SOW-0006 wheel convention at the d3-zoom layer:
 * a PLAIN wheel must NOT zoom (it pans, handled by a separate wheel listener), so
 * we reject it here; a SHIFT+wheel zooms (allowed through). Primary-button drag
 * pans. It also mirrors TopologyRenderer's hardening: d3-zoom's mousedown handler
 * dereferences event.view.document (via d3-drag's nodrag), and the jsdom test
 * environment dispatches synthetic pointer events with a null view — so a
 * view-less mousedown is rejected (a no-op in a real browser, which always
 * carries a view; never a silent crash).
 */
function zoomEventFilter(event: Event): boolean {
  const e = event as Event & { view?: unknown; button?: number; shiftKey?: boolean; ctrlKey?: boolean };
  if (e.type === 'wheel') {
    // Only shift+wheel reaches d3-zoom (→ zoom). Plain wheel is handled as pan.
    // ctrl+wheel (pinch-zoom gesture / browser zoom) is also allowed to zoom.
    return e.shiftKey === true || e.ctrlKey === true;
  }
  if (e.type === 'mousedown' && e.view == null) {
    return false;
  }
  // Primary button only; let non-mouse events through.
  return e.button === undefined || e.button === 0;
}

/**
 * attachZoom wires a d3-zoom behavior to a surface element and returns it plus a
 * disposer. The plain-wheel-pans behavior is added as a separate non-passive
 * wheel listener that calls zoomBehavior.translateBy, so panning flows through
 * the SAME transform/event pipeline as drag + shift-wheel-zoom (one source of
 * truth for the transform). Shared by the SVG and Canvas paths so the
 * interaction is defined once.
 */
function attachZoom<E extends Element>(
  surface: E,
  onZoom: (event: D3ZoomEvent<E, unknown>) => void,
): { behavior: ZoomBehavior<E, unknown>; dispose: () => void } {
  const behavior = zoom<E, unknown>()
    .scaleExtent(SCALE_EXTENT)
    .filter(zoomEventFilter)
    .on('zoom', onZoom);
  const selection = select(surface);
  selection.call(behavior);
  // Start un-panned/un-zoomed (d3's documented selection.call idiom).
  selection.call((s) => behavior.transform(s, zoomIdentity));

  // Plain wheel → horizontal/vertical pan (video-editor feel). Non-passive so we
  // can preventDefault and stop the page from scrolling. Shift/ctrl wheel is left
  // to d3-zoom (it zooms via the filter above). Typed as the base Event (the
  // generic `Element` surface has no `wheel` entry in its event map) and narrowed
  // to WheelEvent — it always is one for a 'wheel' listener.
  const onWheel = (evt: Event): void => {
    const ev = evt as WheelEvent;
    if (ev.shiftKey || ev.ctrlKey) {
      return;
    }
    ev.preventDefault();
    // deltaX pans horizontally; a plain vertical wheel scrubs the time axis too
    // when there is no horizontal delta (the natural timeline feel), otherwise it
    // pans vertically across lanes. translateBy is in pre-scale units, so it pans
    // consistently at any zoom level.
    const dx = ev.deltaX !== 0 ? ev.deltaX : ev.deltaY;
    const dy = ev.deltaX !== 0 ? ev.deltaY : 0;
    behavior.translateBy(selection, -dx, -dy);
  };
  surface.addEventListener('wheel', onWheel, { passive: false });

  return {
    behavior,
    dispose: () => {
      surface.removeEventListener('wheel', onWheel);
      selection.on('.zoom', null);
    },
  };
}

export function TimelineRenderer({
  lanes,
  spans,
  width,
  height,
  tStart,
  tEnd,
  selectedId,
  onSpanClick,
  useCanvas,
}: TimelineRendererProps) {
  const canvas = useCanvas ?? spans.length > VISIBLE_SPAN_CEILING;
  if (canvas) {
    return (
      <TimelineCanvas
        lanes={lanes}
        spans={spans}
        width={width}
        height={height}
        tStart={tStart}
        tEnd={tEnd}
        selectedId={selectedId}
        onSpanClick={onSpanClick}
      />
    );
  }
  return (
    <TimelineSvg
      lanes={lanes}
      spans={spans}
      width={width}
      height={height}
      tStart={tStart}
      tEnd={tEnd}
      selectedId={selectedId}
      onSpanClick={onSpanClick}
    />
  );
}

/** axisTicks computes evenly-spaced time-axis ticks across the width. Reuses the
 *  shared time scale's .ticks()/.(value) so axis positions match the spans. */
function axisTicks(tStart: number, tEnd: number, width: number): { value: number; x: number }[] {
  const x = timelineScale(tStart, tEnd, width);
  if (tEnd <= tStart) {
    return [{ value: tStart, x: 0 }];
  }
  return x.ticks(AXIS_TARGET_TICKS).map((value) => ({ value, x: x(value) }));
}

/**
 * cullWindowFor inverts the X-only zoom transform into the visible track-space
 * window (the same window the Canvas paints): X is scaled by k (so divide), Y is
 * only translated since lane height does not scale with zoom (codex P2#4). Pure
 * so the paint loop and the keyboard-fallback list cull to the IDENTICAL set.
 */
function cullWindowFor(
  transform: { k: number; x: number; y: number },
  width: number,
  height: number,
  laneHeight: number,
): CullWindow {
  const xMin = -transform.x / transform.k;
  const xMax = (width - transform.x) / transform.k;
  const visTop = -transform.y;
  const visBottom = height - transform.y;
  const laneMin = Math.floor((visTop - AXIS_HEIGHT) / laneHeight) - 1;
  const laneMax = Math.ceil((visBottom - AXIS_HEIGHT) / laneHeight) + 1;
  return { xMin, xMax, laneMin, laneMax };
}

/** labelForSpan builds the accessible name for a span (kind, name, duration). */
function labelForSpan(s: PositionedSpan): string {
  const dur = s.instant
    ? 'instant'
    : formatDuration((s.span.end_ts as number) - s.span.start_ts);
  const kind = s.compaction ? 'compaction' : s.span.kind;
  return `${s.span.name || s.span.id} — ${kind} — ${dur}`;
}

function TimelineSvg({
  lanes,
  spans,
  width,
  height,
  tStart,
  tEnd,
  selectedId,
  onSpanClick,
}: Omit<TimelineRendererProps, 'useCanvas'>) {
  const svgRef = useRef<SVGSVGElement>(null);
  const gRef = useRef<SVGGElement>(null);
  const ticks = axisTicks(tStart, tEnd, width);
  // Spans new since the previous render (a live session_changed refetch grew the
  // timeline) fade in — SOW-0006 AC#6. Keyed on span id (the same identity the
  // drawer/selection uses); fadeClassFor withholds the class under reduced motion.
  const newIds = useNewlyAppeared(spans.map((s) => s.span.id));

  // d3-zoom pan/zoom applied to the inner <g> transform; the SVG element is the
  // zoom surface. Re-attached only when the size changes (the listener is stable
  // across data updates).
  useEffect(() => {
    const svg = svgRef.current;
    const g = gRef.current;
    if (!svg || !g) {
      return;
    }
    const { dispose } = attachZoom<SVGSVGElement>(svg, (event) => {
      // Zoom the TIME axis only: scale X by k, keep Y (lane height) at scale 1.
      // Both axes still translate so a plain-wheel vertical lane pan keeps working
      // (ui-pages.md §Timeline; codex P2#4).
      const t = event.transform;
      g.setAttribute('transform', timeXOnlyMatrix(t.k, t.x, t.y));
    });
    return dispose;
  }, [width, height]);

  return (
    <div className={styles.vizScroller} role="group" aria-label="Session timeline">
      <svg
        ref={svgRef}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className={styles.vizSvg}
      >
        {/* Time axis is OUTSIDE the panned <g> on the y, but its x ticks live
            inside so they track horizontal pan/zoom. Kept simple: ticks render
            inside <g>; the axis band is a visual guide. */}
        <g ref={gRef}>
          <g className={styles.axis}>
            {ticks.map((t, i) => (
              <g key={`tick-${i}`} transform={`translate(${t.x},0)`}>
                <line className={styles.axisTick} x1={0} y1={AXIS_HEIGHT} x2={0} y2={height} />
                <text className={styles.axisLabel} x={3} y={14}>
                  {formatDuration(t.value - tStart)}
                </text>
              </g>
            ))}
          </g>

          <g>
            {lanes.map((lane) => (
              <g key={lane.key}>
                <rect
                  className={lane.laneIndex % 2 === 1 ? styles.laneBandAlt : styles.laneBand}
                  x={0}
                  y={AXIS_HEIGHT + lane.y}
                  width={width}
                  height={lane.height}
                />
                <text
                  className={styles.laneLabel}
                  x={LANE_LABEL_PAD}
                  y={AXIS_HEIGHT + lane.y + 13}
                >
                  {lane.label}
                </text>
              </g>
            ))}
          </g>

          <g>
            {spans.map((s) => (
              <TimelineSpanShape
                key={`${s.laneKey}:${s.span.id}`}
                s={s}
                axisHeight={AXIS_HEIGHT}
                trackHeight={height}
                selected={s.span.id === selectedId}
                fadeClass={fadeClassFor(s.span.id, newIds, styles.fadeIn)}
                onClick={() => {
                  onSpanClick(s);
                }}
              />
            ))}
          </g>
        </g>
      </svg>
    </div>
  );
}

function TimelineSpanShape({
  s,
  axisHeight,
  trackHeight,
  selected,
  fadeClass,
  onClick,
}: {
  s: PositionedSpan;
  axisHeight: number;
  trackHeight: number;
  selected: boolean;
  /** The append-fade class for a span new this render, else undefined (AC#6). */
  fadeClass: string | undefined;
  onClick: () => void;
}) {
  const onKeyDown = (e: KeyboardEvent<SVGGElement>): void => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onClick();
    }
  };
  const cls = [selected ? styles.spanSelected : styles.span, fadeClass].filter(Boolean).join(' ');

  // Compaction → full-height dashed vertical breakpoint spanning the whole track.
  if (s.compaction) {
    return (
      <g
        role="button"
        aria-label={labelForSpan(s)}
        tabIndex={0}
        className={cls}
        onClick={onClick}
        onKeyDown={onKeyDown}
      >
        <line className={styles.breakpoint} x1={s.x} y1={axisHeight} x2={s.x} y2={trackHeight} />
        {/* A wider transparent hit target so the thin rule is easy to click. */}
        <rect x={s.x - 4} y={axisHeight} width={8} height={trackHeight - axisHeight} fill="transparent" />
      </g>
    );
  }

  const fill = colorForOpKind(s.span.kind);
  const yTop = axisHeight + s.y + 6;
  const barHeight = s.height - 12;

  // Instant (null-end / point) → a lane-height vertical tick at start_ts.
  if (s.instant) {
    return (
      <g
        role="button"
        aria-label={labelForSpan(s)}
        tabIndex={0}
        className={cls}
        onClick={onClick}
        onKeyDown={onKeyDown}
      >
        <line x1={s.x} y1={yTop} x2={s.x} y2={yTop + barHeight} stroke={fill} strokeWidth={selected ? 3 : 2} />
        <rect x={s.x - 4} y={yTop} width={8} height={barHeight} fill="transparent" />
      </g>
    );
  }

  // Closed span → a bar.
  return (
    <g
      role="button"
      aria-label={labelForSpan(s)}
      tabIndex={0}
      className={cls}
      onClick={onClick}
      onKeyDown={onKeyDown}
    >
      <rect
        x={s.x}
        y={yTop}
        width={s.width}
        height={barHeight}
        rx={2}
        fill={fill}
        stroke={selected ? 'var(--accent)' : 'transparent'}
        strokeWidth={selected ? 2 : 0}
      />
    </g>
  );
}

function TimelineCanvas({
  lanes,
  spans,
  width,
  height,
  tStart,
  tEnd,
  selectedId,
  onSpanClick,
}: Omit<TimelineRendererProps, 'useCanvas'>) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  // Zoom/pan transform applied to the canvas paint and inverted for hit-testing.
  const [transform, setTransform] = useState({ k: 1, x: 0, y: 0 });
  const ticks = axisTicks(tStart, tEnd, width);

  // The spans currently inside the viewport — the EXACT set the Canvas paints AND
  // the set the keyboard-fallback list mirrors. Culling the fallback to the
  // viewport keeps it bounded (no DOM node per span at scale — codex P2#4); a
  // keyboard user reaches the on-screen spans and pans/zooms to reach the rest.
  const visible = useMemo(
    () => cullSpans(spans, cullWindowFor(transform, width, height, lanes[0]?.height ?? 1)),
    [spans, transform, width, height, lanes],
  );

  useEffect(() => {
    const c = canvasRef.current;
    if (!c) {
      return;
    }
    const { dispose } = attachZoom<HTMLCanvasElement>(c, (event) => {
      const tr = event.transform;
      setTransform({ k: tr.k, x: tr.x, y: tr.y });
    });
    return dispose;
  }, [width, height]);

  useEffect(() => {
    const c = canvasRef.current;
    if (!c) {
      return;
    }
    const ctx = c.getContext('2d');
    if (!ctx) {
      return;
    }
    const dpr = typeof window !== 'undefined' ? window.devicePixelRatio || 1 : 1;
    c.width = width * dpr;
    c.height = height * dpr;
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, height);
    // Zoom the TIME axis only: scale X by k, keep Y at scale 1 (lane height
    // constant), translate both axes. ctx.transform(a,b,c,d,e,f) post-multiplies
    // the dpr scale already on the matrix (codex P2#4). `visible` is the
    // viewport-culled set (memoized above) — only on-screen spans are painted.
    ctx.transform(transform.k, 0, 0, 1, transform.x, transform.y);

    // Axis ticks (under the spans).
    ctx.strokeStyle = 'rgba(128,128,128,0.4)';
    ctx.lineWidth = 1;
    for (const t of ticks) {
      ctx.beginPath();
      ctx.moveTo(t.x, AXIS_HEIGHT);
      ctx.lineTo(t.x, height);
      ctx.stroke();
    }

    for (const s of visible) {
      const yTop = AXIS_HEIGHT + s.y + 6;
      const barHeight = s.height - 12;
      if (s.compaction) {
        ctx.strokeStyle = colorForOpKind('compaction');
        ctx.lineWidth = 2;
        ctx.setLineDash([4, 3]);
        ctx.beginPath();
        ctx.moveTo(s.x, AXIS_HEIGHT);
        ctx.lineTo(s.x, height);
        ctx.stroke();
        ctx.setLineDash([]);
        continue;
      }
      const fill = colorForOpKind(s.span.kind);
      if (s.instant) {
        ctx.strokeStyle = fill;
        ctx.lineWidth = s.span.id === selectedId ? 3 : 2;
        ctx.beginPath();
        ctx.moveTo(s.x, yTop);
        ctx.lineTo(s.x, yTop + barHeight);
        ctx.stroke();
        continue;
      }
      ctx.fillStyle = fill;
      ctx.fillRect(s.x, yTop, s.width, barHeight);
      if (s.span.id === selectedId) {
        ctx.strokeStyle = colorForOpKind('llm');
        ctx.lineWidth = 2;
        ctx.strokeRect(s.x, yTop, s.width, barHeight);
      }
    }
    ctx.restore();
  }, [visible, ticks, width, height, selectedId, transform]);

  // Hit-test a click: invert the zoom transform, then find the span whose pixel
  // box contains the point (last match wins → topmost painted).
  const onClick = (e: MouseEvent<HTMLCanvasElement>): void => {
    const bounds = e.currentTarget.getBoundingClientRect();
    // Invert the X-only zoom: X is scaled by k (divide), Y is only translated
    // (no /k) since lane height does not scale with zoom (codex P2#4).
    const px = (e.clientX - bounds.left - transform.x) / transform.k;
    const py = e.clientY - bounds.top - transform.y;
    let hit: PositionedSpan | null = null;
    for (const s of spans) {
      const yTop = AXIS_HEIGHT + s.y + 6;
      const barHeight = s.height - 12;
      if (py < yTop || py > yTop + barHeight) {
        continue;
      }
      const left = s.instant || s.compaction ? s.x - 4 : s.x;
      const right = s.instant || s.compaction ? s.x + 4 : s.x + s.width;
      if (px >= left && px <= right) {
        hit = s;
      }
    }
    if (hit) {
      onSpanClick(hit);
    }
  };

  return (
    <div className={styles.vizScroller} role="group" aria-label="Session timeline">
      <canvas
        ref={canvasRef}
        className={styles.vizCanvas}
        style={{ width, height }}
        onClick={onClick}
      />
      {/* Keyboard / screen-reader path for the Canvas render (SOW-0006 AC#5): the
          <canvas> is one non-focusable image, so without this every span would be
          unreachable by keyboard. Each VISIBLE span (the same viewport-culled set
          the Canvas paints) gets a real focusable button with the SVG bars'
          accessible name; visually hidden (the canvas IS the visual) but operable.
          Culling to the viewport bounds the list so a thousands-span timeline does
          not emit a DOM node per span (codex P2#4) — panning/zooming brings other
          spans into reach. */}
      <ul className={styles.canvasFallbackList} aria-label="Timeline spans">
        {visible.map((s) => (
          <li key={`${s.laneKey}:${s.span.id}`}>
            <button
              type="button"
              className={styles.canvasFallbackButton}
              onClick={() => {
                onSpanClick(s);
              }}
            >
              {labelForSpan(s)}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
