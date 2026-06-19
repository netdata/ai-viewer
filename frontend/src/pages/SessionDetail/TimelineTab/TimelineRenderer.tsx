import { useEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent, UIEvent } from 'react';
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
import { attachZoom } from '../../../viz/zoomInteraction';
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

const AXIS_HEIGHT = 22; // px reserved at the top for the time axis.
const LANE_LABEL_PAD = 6; // px inset for the lane label text.
const AXIS_TARGET_TICKS = 8;
// Bounded vertical viewport for the Canvas path — the canvas backing store is
// sized to THIS (clamped to the content height), never the full lane stack. A
// thousand-lane timeline is ~40000px tall, which exceeds the browser's single
// canvas max (~32767px) and would silently render blank; bounding the canvas and
// scrolling lanes natively (a tall spacer + a sticky canvas) keeps it fast and
// correct. Mirrors the Detailed Waterfall's CANVAS_VIEWPORT (TraceTab/Waterfall).
const CANVAS_VIEWPORT = 460;
const CULL_OVERSCAN = 1; // lane bands rendered on each side of the viewport.
// Zebra-band token + alpha mirror the SVG .laneBandAlt rule (fill var(--bg-tertiary);
// opacity 0.4). Even lanes are transparent (SVG .laneBand) so only odd lanes paint.
const LANE_BAND_TOKEN = '--bg-tertiary';
const LANE_BAND_ALPHA = 0.4;
const LANE_BAND_FALLBACK = '#21262d'; // DARK --bg-tertiary (theme/tokens.css) for non-DOM.
// Canvas-painted lane label: a literal neutral gray, matching the convention of
// the other Canvas renderers (Topology/Waterfall/FlameGraph use fixed rgba label
// colors); it reads acceptably on both the dark and light viz backgrounds. The
// SVG path uses the themed .laneLabel (var(--text-secondary), 11px sans).
const LANE_LABEL_PAINT = 'rgba(160,160,170,0.95)';
const LANE_LABEL_FONT = '11px sans-serif';

/**
 * readThemeVar resolves a CSS custom property off <html>, the same mechanism
 * viz/color.ts uses (getComputedStyle(documentElement)). The op-kind palette in
 * color.ts is a closed token set, so the lane-band background token lives here;
 * keeping the read identical honors the D3-boundary rule (renderers ask for a
 * concrete color). Falls back to the DARK token value in a non-DOM/empty context.
 */
function readThemeVar(name: string, fallback: string): string {
  if (typeof window === 'undefined' || typeof window.getComputedStyle !== 'function') {
    return fallback;
  }
  const value = window.getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value !== '' ? value : fallback;
}

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
  onSpanClick: (_span: PositionedSpan) => void;
  /** Force a render path (tests); defaults to span-count vs VISIBLE_SPAN_CEILING. */
  useCanvas?: boolean;
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
  // Route to the bounded Canvas when EITHER the span count exceeds the SVG ceiling
  // OR the lane stack is taller than the bounded viewport. The second trigger
  // matters because a many-lane / few-span timeline (e.g. 800 sessions, one span
  // each) would otherwise render as an inline ~40000px-tall SVG (or canvas) — far
  // past the browser canvas max — even though it is under the span ceiling. Either
  // way the Canvas path bounds the backing store and scrolls lanes natively.
  const canvas =
    useCanvas ?? (spans.length > VISIBLE_SPAN_CEILING || height > CANVAS_VIEWPORT);
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
 * cullWindowFor computes the visible track-space window the Canvas paints (and the
 * keyboard-fallback list mirrors). X comes from inverting the X-only zoom (X is
 * scaled by k, so divide). Y is the NATIVE vertical scroller, so the visible lane
 * band is derived from scrollTop + the BOUNDED viewport (NOT the full content
 * height) — this is what makes the cull effective on a tall lane stack instead of
 * a no-op. overscan widens the band by a lane on each side so a partially-scrolled
 * row never pops. Pure, so the paint loop and the fallback list cull identically.
 */
function cullWindowFor(
  transform: { k: number; x: number },
  width: number,
  scrollTop: number,
  viewport: number,
  laneHeight: number,
  overscan: number,
): CullWindow {
  const xMin = -transform.x / transform.k;
  const xMax = (width - transform.x) / transform.k;
  // The viewport sees content rows [scrollTop, scrollTop+viewport]; lane y is
  // measured from AXIS_HEIGHT, so subtract it before mapping to a lane index.
  const laneMin = Math.floor((scrollTop - AXIS_HEIGHT) / laneHeight) - overscan;
  const laneMax = Math.ceil((scrollTop + viewport - AXIS_HEIGHT) / laneHeight) + overscan;
  return { xMin, xMax, laneMin, laneMax };
}

/** labelForSpan builds the accessible name for a span (kind, name, duration). */
function labelForSpan(s: PositionedSpan): string {
  const dur = s.instant
    ? 'instant'
    : formatDuration(s.span.end_ts! - s.span.start_ts);
  const kind = s.compaction ? 'compaction' : s.span.kind;
  return `${s.span.name || s.span.id} — ${kind} — ${dur}`;
}

/**
 * laneLabelForSpan returns the session-lane label a span belongs to (mapped from
 * its laneKey via the lane set), so the Canvas keyboard-fallback button names a
 * keyboard/SR user's lane (the lanes ARE the point — which session a span is in).
 * Falls back to the laneKey if the lane is somehow absent from the map.
 */
function laneLabelForSpan(s: PositionedSpan, laneLabels: Map<string, string>): string {
  return laneLabels.get(s.laneKey) ?? s.laneKey;
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
    const { dispose } = attachZoom<SVGSVGElement>(
      svg,
      (event) => {
        // Zoom the TIME axis only: scale X by k, keep Y (lane height) at scale 1.
        // Both axes still translate so a plain-wheel vertical lane pan keeps working
        // (ui-pages.md §Timeline).
        const t = event.transform;
        g.setAttribute('transform', timeXOnlyMatrix(t.k, t.x, t.y));
      },
      // The Timeline pans on a plain wheel (video-editor feel); shift/ctrl wheel
      // zooms the time axis. This is the original behavior (now opt-in via the
      // shared attachZoom).
      { plainWheelPan: true },
    );
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
  // Native vertical scroll position (the Y axis is the scroller, not the zoom —
  // mirrors WaterfallCanvas). Drives the lane cull + the paint Y offset.
  const [scrollTop, setScrollTop] = useState(0);
  // X-only time zoom/pan transform (Y stays the native scroller). k = X scale,
  // x = X translate; ty from d3-zoom is ignored.
  const [transform, setTransform] = useState({ k: 1, x: 0 });
  const ticks = axisTicks(tStart, tEnd, width);

  const laneHeight = lanes[0]?.height ?? 1;
  // The full lane stack height the SPACER establishes (axis + every lane). `height`
  // is the full content height the tab computes; clamp the bounded viewport to it
  // so a SHORT timeline does not reserve more than its content needs.
  const contentHeight = height;
  const viewport = Math.min(CANVAS_VIEWPORT, contentHeight);

  // The spans currently inside the viewport — the EXACT set the Canvas paints AND
  // the set the keyboard-fallback list mirrors. Deriving the lane band from
  // scrollTop + the bounded viewport (not the full height) is what makes the cull
  // effective at scale: a keyboard user reaches the on-screen spans and
  // scrolls/zooms to reach the rest, and the DOM never holds a node per lane.
  const visible = useMemo(
    () =>
      cullSpans(
        spans,
        cullWindowFor(transform, width, scrollTop, viewport, laneHeight, CULL_OVERSCAN),
      ),
    [spans, transform, width, scrollTop, viewport, laneHeight],
  );

  // laneKey → label, so the keyboard-fallback button text names the span's session
  // lane (the lanes ARE the point in the Canvas path too — see laneLabelForSpan).
  const laneLabels = useMemo(() => new Map(lanes.map((l) => [l.key, l.label])), [lanes]);

  // Attach the shared X-only zoom to the <canvas>. plainWheelPan:false so a PLAIN
  // wheel reaches the native vertical scroller (lanes scroll) while SHIFT+wheel
  // zooms and drag pans the time axis — exactly the Detailed Waterfall convention.
  // Re-attached only on geometry change.
  useEffect(() => {
    const c = canvasRef.current;
    if (!c) {
      return;
    }
    const { dispose } = attachZoom<HTMLCanvasElement>(
      c,
      (event) => {
        const tr = event.transform;
        setTransform({ k: tr.k, x: tr.x });
      },
      { plainWheelPan: false },
    );
    return dispose;
  }, [width]);

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
    // Backing store is the BOUNDED viewport — never the full lane stack (a tall
    // stack would exceed the browser canvas max and render blank).
    c.width = width * dpr;
    c.height = viewport * dpr;
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, viewport);
    // Zoom the TIME axis only: scale X by k, keep Y at scale 1, translate X only.
    // The Y axis is the native scroller, so we paint each row at (its y - scrollTop)
    // rather than carrying a Y translate on the matrix. ctx.transform(a,b,c,d,e,f)
    // post-multiplies the dpr scale already on the matrix. `visible` is the
    // viewport-culled set (memoized above) — only on-screen spans are painted.
    ctx.transform(transform.k, 0, 0, 1, transform.x, 0);

    // Lane zebra bands + labels (UNDER the axis ticks and spans), offset by the
    // native scrollTop. Mirrors the SVG path's .laneBand/.laneBandAlt rects +
    // .laneLabel text so the Canvas path keeps lane identity (which session a span
    // belongs to — the lanes ARE the point). The X transform scales by k and
    // translates by transform.x, so a band that must cover the full [0,width]
    // viewport in screen px starts at the inverted-X left edge with the inverted-X
    // width; the lane label is pinned to the left edge the same way. Only the
    // viewport-culled lanes are painted. Math mirrors cullWindowFor's X inversion.
    const bandLeft = -transform.x / transform.k;
    const bandWidth = width / transform.k;
    const bandColor = readThemeVar(LANE_BAND_TOKEN, LANE_BAND_FALLBACK);
    const labelX = (LANE_LABEL_PAD - transform.x) / transform.k;
    for (const lane of visibleLanes(lanes, scrollTop, viewport, laneHeight, CULL_OVERSCAN)) {
      const bandTop = AXIS_HEIGHT + lane.y - scrollTop;
      // Only odd lanes paint a band (even lanes are transparent — SVG .laneBand).
      if (lane.laneIndex % 2 === 1) {
        ctx.save();
        ctx.globalAlpha = LANE_BAND_ALPHA;
        ctx.fillStyle = bandColor;
        ctx.fillRect(bandLeft, bandTop, bandWidth, lane.height);
        ctx.restore();
      }
      ctx.fillStyle = LANE_LABEL_PAINT;
      ctx.font = LANE_LABEL_FONT;
      ctx.fillText(lane.label, labelX, bandTop + 13);
    }

    // Axis ticks span the whole viewport (the axis is pinned to the top — no
    // scrollTop offset). Drawn under the spans.
    ctx.strokeStyle = 'rgba(128,128,128,0.4)';
    ctx.lineWidth = 1;
    for (const t of ticks) {
      ctx.beginPath();
      ctx.moveTo(t.x, AXIS_HEIGHT);
      ctx.lineTo(t.x, viewport);
      ctx.stroke();
    }

    for (const s of visible) {
      const yTop = AXIS_HEIGHT + s.y - scrollTop + 6;
      const barHeight = s.height - 12;
      if (s.compaction) {
        // A compaction breakpoint is a full-height rule across the visible band.
        ctx.strokeStyle = colorForOpKind('compaction');
        ctx.lineWidth = 2;
        ctx.setLineDash([4, 3]);
        ctx.beginPath();
        ctx.moveTo(s.x, AXIS_HEIGHT);
        ctx.lineTo(s.x, viewport);
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
  }, [visible, lanes, ticks, width, viewport, scrollTop, laneHeight, selectedId, transform]);

  const onScroll = (e: UIEvent<HTMLDivElement>): void => {
    setScrollTop(e.currentTarget.scrollTop);
  };

  // Hit-test a click: invert the X-only zoom for X; for Y, add scrollTop to map the
  // viewport-relative click into content space (the native scroller offsets Y, the
  // zoom does not). Last match wins → topmost painted.
  const onClick = (e: MouseEvent<HTMLDivElement>): void => {
    const target = e.currentTarget.querySelector('canvas');
    if (!target) {
      return;
    }
    const bounds = target.getBoundingClientRect();
    const px = (e.clientX - bounds.left - transform.x) / transform.k;
    const py = e.clientY - bounds.top + scrollTop;
    let hit: PositionedSpan | null = null;
    for (const s of spans) {
      // A compaction breakpoint paints as a FULL-HEIGHT vertical rule, so its hit
      // region is every lane in content space (AXIS_HEIGHT→contentHeight), not its
      // own lane band — mirroring the SVG path's full-height transparent target.
      // Every other span keeps its lane-band Y test.
      if (s.compaction) {
        if (py >= AXIS_HEIGHT && py <= contentHeight && px >= s.x - 4 && px <= s.x + 4) {
          hit = s;
        }
        continue;
      }
      const yTop = AXIS_HEIGHT + s.y + 6;
      const barHeight = s.height - 12;
      if (py < yTop || py > yTop + barHeight) {
        continue;
      }
      const left = s.instant ? s.x - 4 : s.x;
      const right = s.instant ? s.x + 4 : s.x + s.width;
      if (px >= left && px <= right) {
        hit = s;
      }
    }
    if (hit) {
      onSpanClick(hit);
    }
  };

  return (
    // The onClick here is a pointer-only pixel hit-test over the <canvas>
    // (which is one non-focusable image). The keyboard / screen-reader path is
    // the visually-hidden focusable <button> list rendered below (SOW-0006
    // AC#5), so every span IS reachable without a pointer. These two jsx-a11y
    // rules are therefore false positives for this canvas-with-DOM-fallback
    // pattern. Tracked for the SOW-0012 Chunk D viz/<chart>/a11y.md waiver.
    // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions
    <div
      className={styles.vizScroller}
      role="group"
      aria-label="Session timeline"
      style={{ maxHeight: viewport, overflowY: 'auto' }}
      onScroll={onScroll}
      onClick={onClick}
    >
      {/* Tall spacer establishes the scrollable full content height; the canvas is
          sticky to the viewport top and repainted with the viewport-culled lanes
          on scroll (mirrors WaterfallCanvas). */}
      <div style={{ height: contentHeight, position: 'relative', width }}>
        <canvas
          ref={canvasRef}
          className={styles.vizCanvas}
          style={{ position: 'sticky', top: 0, width, height: viewport }}
        />
      </div>
      {/* Keyboard / screen-reader path for the Canvas render (SOW-0006 AC#5): the
          <canvas> is one non-focusable image, so without this every span would be
          unreachable by keyboard. Each VISIBLE span (the same viewport-culled set
          the Canvas paints) gets a real focusable button with the SVG bars'
          accessible name; visually hidden (the canvas IS the visual) but operable.
          Culling to the viewport bounds the list so a thousands-span timeline does
          not emit a DOM node per span — scrolling/zooming brings other spans into
          reach. */}
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
              {`${laneLabelForSpan(s, laneLabels)} — ${labelForSpan(s)}`}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * visibleLanes returns the lane rows whose band overlaps the bounded viewport
 * (scrollTop..scrollTop+viewport) plus overscan, so the Canvas paint loop only
 * touches on-screen lanes — the lane-band equivalent of cullSpans' lane filter,
 * kept in sync with cullWindowFor's band math. Pure.
 */
function visibleLanes(
  lanes: PositionedLane[],
  scrollTop: number,
  viewport: number,
  laneHeight: number,
  overscan: number,
): PositionedLane[] {
  if (lanes.length === 0 || laneHeight <= 0) {
    return lanes;
  }
  const laneMin = Math.floor((scrollTop - AXIS_HEIGHT) / laneHeight) - overscan;
  const laneMax = Math.ceil((scrollTop + viewport - AXIS_HEIGHT) / laneHeight) + overscan;
  return lanes.filter((l) => l.laneIndex >= laneMin && l.laneIndex <= laneMax);
}
