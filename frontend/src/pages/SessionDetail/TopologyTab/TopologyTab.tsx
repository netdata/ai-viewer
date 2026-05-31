import { useEffect, useMemo, useState } from 'react';
import type { TopologyMetric, TopologyNode } from '../../../api/types';
import { useSessionTopology } from '../../../api/sessions';
import { SpanDetailDrawer } from '../../../components/SpanDetailDrawer';
import { LoadingState, ErrorState, EmptyState } from '../../../components/StatusViews';
import {
  FORCE_WORKER_THRESHOLD,
  layoutTopology,
  positionsOf,
  reapplyFrozenPositions,
  runForceLayout,
  type PositionedNode,
  type TopologyLayoutMode,
  type TopologyLayoutOpts,
} from '../../../viz/topology';
import { startThemeColorWatch } from '../../../viz/color';
import type { ForceWorkerRequest, ForceWorkerResponse } from '../../../viz/forceWorker';
import ForceWorker from '../../../viz/forceWorker?worker';
import { TopologyRenderer } from './TopologyRenderer';
import styles from './TopologyTab.module.css';

// Topology tab (ui-pages.md §/sessions/:id #2). Fetches the whole session tree's
// actor graph for the selected size metric, lays it out via viz/topology in one
// of three operator-selectable modes (seeded force / plain force / hierarchical),
// and renders it through the shared TopologyRenderer. A "freeze layout" button
// pins the current positions so re-renders and live SSE refreshes do not
// re-simulate (the operator can read a stable graph). Above the 100-node
// threshold the force simulation runs in a Web Worker (frontend-architecture.md);
// below it the layout runs inline. Clicking a node opens the shared
// SpanDetailDrawer with a source-aware 'node' detail (ui-pages.md §Span detail
// drawer): a node is an aggregate ACTOR, not an op, so the drawer shows its
// kind/label, failure %, and the value of the currently-selected size metric —
// it omits op-only fields rather than fabricating them as zero.

const VIEW_WIDTH = 900;
const VIEW_HEIGHT = 560;

const METRICS: ReadonlyArray<{ key: TopologyMetric; label: string }> = [
  { key: 'cost', label: 'Cost' },
  { key: 'tokens', label: 'Tokens' },
  { key: 'duration', label: 'Duration' },
  { key: 'calls', label: 'Calls' },
  { key: 'ctx_pct', label: 'Context %' },
];

const MODES: ReadonlyArray<{ key: TopologyLayoutMode; label: string }> = [
  { key: 'force-seeded', label: 'Seeded force' },
  { key: 'force-plain', label: 'Plain force' },
  { key: 'hierarchical', label: 'Hierarchical' },
];

/** metricLabelOf returns the METRICS label for a metric key (the drawer shows the
 *  size-metric value under this honest label). */
function metricLabelOf(metric: TopologyMetric): string {
  return METRICS.find((m) => m.key === metric)?.label ?? metric;
}

/** graphKey identifies a (metric→graph)+mode layout input for worker-result staleness. */
function graphKey(
  metric: TopologyMetric,
  mode: TopologyLayoutMode,
  nodeCount: number,
  edgeCount: number,
): string {
  return `${metric}|${mode}|${nodeCount}|${edgeCount}`;
}

export function TopologyTab({ sessionId }: { sessionId: string }) {
  const [metric, setMetric] = useState<TopologyMetric>('cost');
  const [mode, setMode] = useState<TopologyLayoutMode>('force-seeded');
  const [selected, setSelected] = useState<TopologyNode | null>(null);
  // The pinned node POSITIONS captured when the operator froze (id → {x,y}); while
  // non-null the simulation never re-runs, but the node DATA stays live — a
  // metric/SSE refetch re-applies fresh labels/radii/failure-ratios onto these
  // coordinates. State (not a ref) so toggling it re-renders.
  const [frozenLayout, setFrozenLayout] = useState<ReadonlyMap<string, { x: number; y: number }> | null>(
    null,
  );
  // Worker-computed positions tagged with the input key they were computed for,
  // so a stale result from a previous metric/mode is ignored. Set only inside
  // the worker's onmessage handler (an event callback, never the effect body).
  const [workerResult, setWorkerResult] = useState<{ key: string; positioned: PositionedNode[] } | null>(
    null,
  );

  useEffect(() => startThemeColorWatch(), []);

  const { data, isPending, isError, error } = useSessionTopology(sessionId, metric);

  const nodes = useMemo(() => data?.nodes ?? [], [data]);
  const edges = useMemo(() => data?.edges ?? [], [data]);
  const maxSizeMetric = data?.max_size_metric ?? 0;
  const frozen = frozenLayout !== null;
  const useWorker = nodes.length > FORCE_WORKER_THRESHOLD && mode !== 'hierarchical';
  const key = graphKey(metric, mode, nodes.length, edges.length);

  const opts = useMemo<TopologyLayoutOpts>(
    () => ({ mode, width: VIEW_WIDTH, height: VIEW_HEIGHT, maxSizeMetric }),
    [mode, maxSizeMetric],
  );

  // Inline layout: hierarchical always (cheap, exact) and the force modes below
  // the worker threshold; the large-force case is computed off-thread (worker).
  const inlinePositions = useMemo<PositionedNode[]>(() => {
    if (useWorker) {
      return [];
    }
    return layoutTopology(nodes, edges, opts);
  }, [nodes, edges, opts, useWorker]);

  // Spin up the Web Worker for the large-force case; terminate on cleanup so a
  // navigated-away tab (or a metric/mode change) never leaks a running worker
  // (frontend-architecture.md). No setState in the effect body — the result is
  // delivered through the onmessage event handler, tagged with `key`.
  useEffect(() => {
    if (!useWorker || frozen) {
      return;
    }
    const worker = new ForceWorker();
    worker.onmessage = (e: MessageEvent<ForceWorkerResponse>) => {
      if ('error' in e.data) {
        // Worker simulation failed — surface it (no silent failures, AGENTS.md §6)
        // and fall back to the inline layout so the graph still renders rather than
        // staying permanently empty. The inline positions are joined through the
        // same key, so the render path below treats them exactly like a worker result.
        console.error('[topology] force worker failed:', e.data.error);
        setWorkerResult({ key, positioned: layoutTopology(nodes, edges, opts) });
        return;
      }
      setWorkerResult({ key, positioned: e.data.positioned });
    };
    const request: ForceWorkerRequest = { nodes, edges, opts, seeded: mode === 'force-seeded' };
    worker.postMessage(request);
    return () => {
      worker.terminate();
    };
  }, [useWorker, frozen, nodes, edges, opts, mode, key]);

  // Positions actually rendered. When frozen, the simulation is pinned but the
  // DATA is live: reapplyFrozenPositions keeps each node at its frozen (x,y) while
  // re-applying the fresh label/radius/failure_ratio (a new metric or SSE refetch
  // updates the graph in place; a vanished node drops, a new node is seeded).
  // Otherwise the worker result (only when it matches the current input key — a
  // stale result for a prior metric/mode is dropped, showing an empty graph until
  // the fresh run lands) or the inline result. The worker branch re-joins the
  // worker POSITIONS onto the CURRENT nodes (same as the frozen path): the
  // counts-only key can collide across two different graphs with equal node/edge
  // counts, so taking identity from `nodes` (not from the worker's stale
  // PositionedNode[]) prevents rendering the prior graph's labels/ids — and, on
  // the cross-session page, navigating to the wrong session — until the fresh run
  // lands. Where ids match, the worker's coordinates apply; an unmatched id gets a
  // deterministic seeded fallback. Same-graph case is unchanged (identical result).
  const positioned: PositionedNode[] = frozen
    ? reapplyFrozenPositions(nodes, frozenLayout ?? new Map(), opts)
    : useWorker
      ? (workerResult?.key === key
          ? reapplyFrozenPositions(nodes, positionsOf(workerResult.positioned), opts)
          : [])
      : inlinePositions;

  // Freeze pins the current node POSITIONS (positionsOf strips to id → {x,y}). For
  // a large-force graph whose worker result is not back yet, run the simulation
  // once synchronously so the pin is immediate and deterministic.
  const onToggleFreeze = (): void => {
    if (frozen) {
      setFrozenLayout(null);
      return;
    }
    const snapshot =
      positioned.length > 0
        ? positioned
        : runForceLayout(nodes, edges, opts, mode === 'force-seeded');
    setFrozenLayout(positionsOf(snapshot));
  };

  const onSelectMode = (next: TopologyLayoutMode): void => {
    // Changing layout unfreezes (the pinned layout no longer matches the chosen
    // engine), so the new engine's layout is shown.
    setFrozenLayout(null);
    setMode(next);
  };

  return (
    <div className={styles.wrap}>
      <div className={styles.toolbar}>
        <label className={styles.control}>
          <span>Size by</span>
          <select
            className={styles.select}
            value={metric}
            onChange={(e) => {
              setMetric(e.target.value as TopologyMetric);
            }}
          >
            {METRICS.map((m) => (
              <option key={m.key} value={m.key}>
                {m.label}
              </option>
            ))}
          </select>
        </label>

        <fieldset className={styles.modeToggle}>
          <legend className={styles.srOnly}>Layout</legend>
          {MODES.map((m) => (
            <label key={m.key} className={styles.modeOption}>
              <input
                type="radio"
                name="topology-mode"
                checked={mode === m.key}
                onChange={() => {
                  onSelectMode(m.key);
                }}
              />
              <span>{m.label}</span>
            </label>
          ))}
        </fieldset>

        <button
          type="button"
          className={styles.freezeButton}
          aria-pressed={frozen}
          disabled={mode === 'hierarchical'}
          title={
            mode === 'hierarchical'
              ? 'The hierarchical layout is already static'
              : 'Pin the current node positions'
          }
          onClick={onToggleFreeze}
        >
          {frozen ? 'Unfreeze layout' : 'Freeze layout'}
        </button>

        <span className={styles.spacer} />
        <span className={styles.nodeCount}>
          {nodes.length} node{nodes.length === 1 ? '' : 's'}
        </span>
      </div>

      {isPending ? (
        <LoadingState label="Loading topology…" />
      ) : isError ? (
        <ErrorState error={error} title="Failed to load topology" />
      ) : nodes.length === 0 ? (
        <EmptyState>No actors recorded for this session.</EmptyState>
      ) : (
        <>
          <div className={styles.vizArea}>
            <TopologyRenderer
              positioned={positioned}
              edges={edges}
              width={VIEW_WIDTH}
              height={VIEW_HEIGHT}
              selectedId={selected?.id ?? null}
              onNodeClick={(p) => {
                setSelected(p.node);
              }}
            />
          </div>
          <Legend />
        </>
      )}

      <SpanDetailDrawer
        detail={
          selected
            ? {
                kind: 'node',
                node: selected,
                metricLabel: metricLabelOf(metric),
                metricValue: selected.size_metric,
              }
            : null
        }
        onClose={() => {
          setSelected(null);
        }}
      />
    </div>
  );
}

/** Legend explains the encodings (shape = actor kind, color = failures). */
function Legend() {
  return (
    <div className={styles.legend} aria-label="Topology legend">
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--text-secondary)' }} />
        Agent (circle)
      </span>
      <span className={styles.legendItem}>
        <span
          className={`${styles.legendSwatch} ${styles.legendSwatchSquare}`}
          style={{ background: 'var(--text-secondary)' }}
        />
        Tool (square)
      </span>
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--success)' }} />
        No failures
      </span>
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--warning)' }} />
        Some failures
      </span>
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--error)' }} />
        Many failures
      </span>
    </div>
  );
}
