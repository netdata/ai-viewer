import { useEffect, useRef, useState } from 'react';
import type { KeyboardEvent, MouseEvent } from 'react';
import { select } from 'd3-selection';
import { zoom, zoomIdentity, type D3ZoomEvent } from 'd3-zoom';
import type { TopologyEdge } from '../../../api/types';
import type { PositionedNode } from '../../../viz/topology';
import { colorForActorKind, colorForFailureRatio } from '../../../viz/color';
import { formatPct } from '../../../lib/format';
import styles from './TopologyTab.module.css';

// Topology graph renderer (ui-pages.md §/sessions/:id #2). Paints the positioned
// actor graph: agents (circle) vs tools (rounded square) by node kind, fill by
// failure_ratio, size by the selected metric (radius pre-computed in
// viz/topology). SVG below the span ceiling — one clickable DOM node per actor,
// inspectable + keyboard-focusable; Canvas above it so a big cross-tree graph
// stays fast (frontend-architecture.md §Performance Budgets). d3-zoom drives
// pan/zoom (the D3 boundary: this file only paints + wires interaction, all
// geometry comes from viz/topology). A node click calls onNodeClick → the shared
// SpanDetailDrawer.

// SVG_NODE_CEILING mirrors the Trace SVG ceiling intent: below it the graph is
// SVG DOM nodes (clickable, a11y-friendly); at/above it Canvas takes over. Kept
// at the force-Worker threshold so "many nodes" consistently means worker layout
// + Canvas paint.
export const SVG_NODE_CEILING = 100;

const TOOL_SIZE_SCALE = 1.6; // a tool's square half-side ≈ radius × this / 2.

/**
 * zoomEventFilter mirrors d3-zoom's default filter (primary button, not
 * ctrl-as-zoom-gesture) but additionally requires a usable `event.view` on
 * pointer-down. d3-zoom's mousedown handler dereferences `event.view.document`
 * (via d3-drag's nodrag); a synthetic pointer event with a null view — which the
 * jsdom test environment dispatches — would throw there. A real browser mousedown
 * always carries a view, so this guard is a no-op in production and only hardens
 * against the headless/synthetic case (no silent crash). Wheel/dblclick zoom is
 * unaffected.
 */
function zoomEventFilter(event: Event): boolean {
  // Read only the structural fields we need (avoid clashing with React's
  // MouseEvent type imported in this module).
  const e = event as Event & { view?: unknown; button?: number };
  if (e.type === 'mousedown' && e.view == null) {
    return false;
  }
  if (e.type === 'wheel') {
    return true;
  }
  // Primary button only; let non-mouse (touch/wheel) events through.
  return e.button === undefined || e.button === 0;
}

export interface TopologyRendererProps {
  positioned: PositionedNode[];
  edges: TopologyEdge[];
  width: number;
  height: number;
  selectedId: string | null;
  onNodeClick: (node: PositionedNode) => void;
  /** Force a render path (tests); defaults to node-count vs SVG_NODE_CEILING. */
  useCanvas?: boolean;
}

export function TopologyRenderer({
  positioned,
  edges,
  width,
  height,
  selectedId,
  onNodeClick,
  useCanvas,
}: TopologyRendererProps) {
  const canvas = useCanvas ?? positioned.length > SVG_NODE_CEILING;
  if (canvas) {
    return (
      <TopologyCanvas
        positioned={positioned}
        edges={edges}
        width={width}
        height={height}
        selectedId={selectedId}
        onNodeClick={onNodeClick}
      />
    );
  }
  return (
    <TopologySvg
      positioned={positioned}
      edges={edges}
      width={width}
      height={height}
      selectedId={selectedId}
      onNodeClick={onNodeClick}
    />
  );
}

/** posIndex maps node id → its positioned record (edge endpoint lookup). */
function posIndex(positioned: PositionedNode[]): Map<string, PositionedNode> {
  return new Map(positioned.map((p) => [p.node.id, p]));
}

function ariaLabelFor(p: PositionedNode): string {
  const kind = p.node.kind === 'agent' ? 'agent' : 'tool';
  return `${p.node.label} — ${kind} — ${formatPct(p.node.failure_ratio)} failures`;
}

function TopologySvg({
  positioned,
  edges,
  width,
  height,
  selectedId,
  onNodeClick,
}: Omit<TopologyRendererProps, 'useCanvas'>) {
  const svgRef = useRef<SVGSVGElement>(null);
  const gRef = useRef<SVGGElement>(null);
  const idx = posIndex(positioned);

  // d3-zoom pan/zoom applied to the inner <g> transform; the SVG element stays
  // the zoom surface. Re-attached only when the size changes (the listener is
  // stable across data updates).
  useEffect(() => {
    const svg = svgRef.current;
    const g = gRef.current;
    if (!svg || !g) {
      return;
    }
    const zoomBehavior = zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.2, 4])
      .filter(zoomEventFilter)
      .on('zoom', (event: D3ZoomEvent<SVGSVGElement, unknown>) => {
        g.setAttribute('transform', event.transform.toString());
      });
    const selection = select(svg);
    selection.call(zoomBehavior);
    // Reset to identity so a remount starts un-panned (arrow keeps `transform`
    // bound to its behavior — d3's documented selection.call idiom).
    selection.call((s) => zoomBehavior.transform(s, zoomIdentity));
    return () => {
      selection.on('.zoom', null);
    };
  }, [width, height]);

  return (
    <div className={styles.vizScroller} role="img" aria-label="Session topology graph">
      <svg
        ref={svgRef}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className={styles.vizSvg}
      >
        <g ref={gRef}>
          <g className={styles.edges}>
            {edges.map((e, i) => {
              const s = idx.get(e.source);
              const t = idx.get(e.target);
              if (!s || !t) {
                return null;
              }
              return (
                <line
                  key={`${e.source}->${e.target}-${i}`}
                  x1={s.x}
                  y1={s.y}
                  x2={t.x}
                  y2={t.y}
                  className={styles.edge}
                />
              );
            })}
          </g>
          {positioned.map((p) => (
            <TopologyNodeShape
              key={p.node.id}
              p={p}
              selected={p.node.id === selectedId}
              onClick={() => {
                onNodeClick(p);
              }}
            />
          ))}
        </g>
      </svg>
    </div>
  );
}

function TopologyNodeShape({
  p,
  selected,
  onClick,
}: {
  p: PositionedNode;
  selected: boolean;
  onClick: () => void;
}) {
  const fill = colorForFailureRatio(p.node.failure_ratio);
  const stroke = colorForActorKind(p.node.kind);
  const onKeyDown = (e: KeyboardEvent<SVGGElement>): void => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onClick();
    }
  };
  const labelDy = (p.node.kind === 'agent' ? p.radius : p.radius * (TOOL_SIZE_SCALE / 2)) + 12;
  return (
    <g
      role="button"
      aria-label={ariaLabelFor(p)}
      tabIndex={0}
      className={selected ? styles.nodeSelected : styles.node}
      onClick={onClick}
      onKeyDown={onKeyDown}
      transform={`translate(${p.x},${p.y})`}
    >
      {p.node.kind === 'agent' ? (
        <circle r={p.radius} fill={fill} stroke={stroke} strokeWidth={selected ? 3 : 2} />
      ) : (
        <rect
          x={-p.radius * (TOOL_SIZE_SCALE / 2)}
          y={-p.radius * (TOOL_SIZE_SCALE / 2)}
          width={p.radius * TOOL_SIZE_SCALE}
          height={p.radius * TOOL_SIZE_SCALE}
          rx={3}
          fill={fill}
          stroke={stroke}
          strokeWidth={selected ? 3 : 2}
        />
      )}
      <text className={styles.nodeLabel} y={labelDy} textAnchor="middle">
        {p.node.label}
      </text>
    </g>
  );
}

function TopologyCanvas({
  positioned,
  edges,
  width,
  height,
  selectedId,
  onNodeClick,
}: Omit<TopologyRendererProps, 'useCanvas'>) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  // Zoom/pan transform (k scale, x/y translate) applied to the canvas paint and
  // inverted for hit-testing. d3-zoom drives it on the canvas element.
  const [transform, setTransform] = useState({ k: 1, x: 0, y: 0 });
  const idx = posIndex(positioned);

  useEffect(() => {
    const c = canvasRef.current;
    if (!c) {
      return;
    }
    const zoomBehavior = zoom<HTMLCanvasElement, unknown>()
      .scaleExtent([0.2, 4])
      .filter(zoomEventFilter)
      .on('zoom', (event: D3ZoomEvent<HTMLCanvasElement, unknown>) => {
        const tr = event.transform;
        setTransform({ k: tr.k, x: tr.x, y: tr.y });
      });
    const selection = select(c);
    selection.call(zoomBehavior);
    selection.call((s) => zoomBehavior.transform(s, zoomIdentity));
    return () => {
      selection.on('.zoom', null);
    };
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
    ctx.translate(transform.x, transform.y);
    ctx.scale(transform.k, transform.k);

    // Edges first (under the nodes).
    ctx.strokeStyle = 'rgba(128,128,128,0.35)';
    ctx.lineWidth = 1;
    for (const e of edges) {
      const s = idx.get(e.source);
      const t = idx.get(e.target);
      if (!s || !t) {
        continue;
      }
      ctx.beginPath();
      ctx.moveTo(s.x, s.y);
      ctx.lineTo(t.x, t.y);
      ctx.stroke();
    }

    for (const p of positioned) {
      ctx.fillStyle = colorForFailureRatio(p.node.failure_ratio);
      ctx.strokeStyle = colorForActorKind(p.node.kind);
      ctx.lineWidth = p.node.id === selectedId ? 3 : 2;
      if (p.node.kind === 'agent') {
        ctx.beginPath();
        ctx.arc(p.x, p.y, p.radius, 0, Math.PI * 2);
        ctx.fill();
        ctx.stroke();
      } else {
        const half = p.radius * (TOOL_SIZE_SCALE / 2);
        ctx.fillRect(p.x - half, p.y - half, half * 2, half * 2);
        ctx.strokeRect(p.x - half, p.y - half, half * 2, half * 2);
      }
    }
    ctx.restore();
  }, [positioned, edges, width, height, selectedId, transform, idx]);

  // Hit-test a click: invert the zoom transform, then find the node whose shape
  // contains the point (nearest-first by distance for overlapping circles).
  const onClick = (e: MouseEvent<HTMLCanvasElement>): void => {
    const bounds = e.currentTarget.getBoundingClientRect();
    const px = (e.clientX - bounds.left - transform.x) / transform.k;
    const py = (e.clientY - bounds.top - transform.y) / transform.k;
    let hit: PositionedNode | null = null;
    let best = Infinity;
    for (const p of positioned) {
      const dx = px - p.x;
      const dy = py - p.y;
      const reach =
        p.node.kind === 'agent' ? p.radius : p.radius * (TOOL_SIZE_SCALE / 2) * Math.SQRT2;
      const d = dx * dx + dy * dy;
      if (d <= reach * reach && d < best) {
        best = d;
        hit = p;
      }
    }
    if (hit) {
      onNodeClick(hit);
    }
  };

  return (
    <div className={styles.vizScroller} role="img" aria-label="Session topology graph">
      <canvas
        ref={canvasRef}
        className={styles.vizCanvas}
        style={{ width, height }}
        onClick={onClick}
      />
    </div>
  );
}
