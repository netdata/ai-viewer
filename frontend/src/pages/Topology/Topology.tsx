import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import type { TopologyMetric } from '../../api/types';
import { useTopology } from '../../api/topology';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import {
  FORCE_WORKER_THRESHOLD,
  layoutTopology,
  positionsOf,
  reapplyFrozenPositions,
  runForceLayout,
  type PositionedNode,
  type TopologyLayoutMode,
  type TopologyLayoutOpts,
} from '../../viz/topology';
import { startThemeColorWatch } from '../../viz/color';
import type { ForceWorkerRequest, ForceWorkerResponse } from '../../viz/forceWorker';
import ForceWorker from '../../viz/forceWorker?worker';
import { TopologyRenderer } from '../SessionDetail/TopologyTab/TopologyRenderer';
import { formatNumber } from '../../lib/format';
import styles from '../SessionDetail/TopologyTab/TopologyTab.module.css';

// Cross-session topology page (ui-pages.md §/topology). Reuses the chunk-6a
// renderer + layout engine, but the scope is the global FilterBar filter rather
// than one session tree: the server returns one agent node per matching session
// (no tool nodes) plus lineage edges, capped at a node ceiling. This page:
//
//   - reads the URL-synced filter via useFilters() (the FilterBar in Layout
//     drives it; this page only reads, like SessionsList);
//   - fetches /api/topology for the selected size metric (default `duration`,
//     the cross-session default — rest-api.md §GET /api/topology);
//   - lays the graph out via viz/topology in one of three operator-selectable
//     modes (seeded force / plain force / hierarchical), worker-offloaded above
//     the 100-node threshold, with a freeze button to pin positions;
//   - subscribes to live updates for the active filter (SSE refreshes the graph
//     when sessions change — see api/sse.ts onSessionChanged → ['topology']);
//   - surfaces a "showing top N of M" notice when the server truncated the set;
//   - on a node click NAVIGATES to that session's detail (a cross-session node
//     IS a whole session — NOT the per-session span drawer).

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

/** graphKey identifies a (metric→graph)+mode layout input for worker-result staleness. */
function graphKey(
  metric: TopologyMetric,
  mode: TopologyLayoutMode,
  nodeCount: number,
  edgeCount: number,
): string {
  return `${metric}|${mode}|${nodeCount}|${edgeCount}`;
}

/** sessionIdFromNode strips the "agent:" prefix from a cross-session node id. */
function sessionIdFromNode(nodeId: string): string {
  return nodeId.startsWith('agent:') ? nodeId.slice('agent:'.length) : nodeId;
}

export function Topology() {
  const { filters } = useFilters();
  const navigate = useNavigate();
  // Cross-session default is duration (rest-api.md); the per-session tab defaults
  // to cost — they are deliberately different views.
  const [metric, setMetric] = useState<TopologyMetric>('duration');
  const [mode, setMode] = useState<TopologyLayoutMode>('force-seeded');
  // The pinned node POSITIONS captured when the operator froze (id → {x,y}); while
  // non-null the simulation never re-runs, but the node DATA stays live — a
  // metric/SSE refetch re-applies fresh labels/radii/failure-ratios onto these
  // coordinates.
  const [frozenLayout, setFrozenLayout] = useState<ReadonlyMap<string, { x: number; y: number }> | null>(
    null,
  );
  // Worker-computed positions tagged with the input key they were computed for.
  const [workerResult, setWorkerResult] = useState<{ key: string; positioned: PositionedNode[] } | null>(
    null,
  );

  useEffect(() => startThemeColorWatch(), []);

  const { data, isPending, isError, error } = useTopology(filters, metric);
  // One live subscription for the active filter; SSE invalidates ['topology'].
  useLiveUpdates(filtersToSubscription(filters));

  const nodes = useMemo(() => data?.nodes ?? [], [data]);
  const edges = useMemo(() => data?.edges ?? [], [data]);
  const maxSizeMetric = data?.max_size_metric ?? 0;
  const truncated = data?.truncated ?? false;
  const frozen = frozenLayout !== null;
  const useWorker = nodes.length > FORCE_WORKER_THRESHOLD && mode !== 'hierarchical';
  const key = graphKey(metric, mode, nodes.length, edges.length);

  const opts = useMemo<TopologyLayoutOpts>(
    () => ({ mode, width: VIEW_WIDTH, height: VIEW_HEIGHT, maxSizeMetric }),
    [mode, maxSizeMetric],
  );

  const inlinePositions = useMemo<PositionedNode[]>(() => {
    if (useWorker) {
      return [];
    }
    return layoutTopology(nodes, edges, opts);
  }, [nodes, edges, opts, useWorker]);

  // Spin up the Web Worker for the large-force case; terminate on cleanup so a
  // navigated-away page (or a metric/mode change) never leaks a running worker.
  useEffect(() => {
    if (!useWorker || frozen) {
      return;
    }
    const worker = new ForceWorker();
    worker.onmessage = (e: MessageEvent<ForceWorkerResponse>) => {
      setWorkerResult({ key, positioned: e.data.positioned });
    };
    const request: ForceWorkerRequest = { nodes, edges, opts, seeded: mode === 'force-seeded' };
    worker.postMessage(request);
    return () => {
      worker.terminate();
    };
  }, [useWorker, frozen, nodes, edges, opts, mode, key]);

  // When frozen, the simulation is pinned but the DATA is live:
  // reapplyFrozenPositions keeps each node at its frozen (x,y) while re-applying
  // the fresh label/radius/failure_ratio (a new metric or SSE refetch updates the
  // graph in place; a vanished session drops, a new one is seeded). The worker
  // branch re-joins the worker POSITIONS onto the CURRENT nodes the same way: the
  // counts-only key can collide across two different graphs with equal node/edge
  // counts, so identity (and the per-node session id used for navigation) must
  // come from `nodes`, never from the worker's stale PositionedNode[] — otherwise
  // a filter/SSE swap with matching counts would render the prior graph's sessions
  // and a click would navigate to the WRONG session until the fresh run lands.
  // Where ids match, the worker's coordinates apply; an unmatched id gets a
  // deterministic seeded fallback. Same-graph case is unchanged (identical result).
  const positioned: PositionedNode[] = frozen
    ? reapplyFrozenPositions(nodes, frozenLayout ?? new Map(), opts)
    : useWorker
      ? (workerResult?.key === key
          ? reapplyFrozenPositions(nodes, positionsOf(workerResult.positioned), opts)
          : [])
      : inlinePositions;

  // Freeze pins the current node POSITIONS (positionsOf strips to id → {x,y}).
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
    setFrozenLayout(null);
    setMode(next);
  };

  return (
    <section aria-labelledby="topology-title" className={styles.wrap}>
      <h1 id="topology-title">Topology</h1>

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
                name="cross-topology-mode"
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
          {nodes.length} session{nodes.length === 1 ? '' : 's'}
        </span>
      </div>

      {truncated ? (
        <p className={styles.nodeCount} role="status">
          Showing top {formatNumber(nodes.length)} sessions by{' '}
          {METRICS.find((m) => m.key === metric)?.label.toLowerCase() ?? metric}; narrow the filter
          to see the rest.
        </p>
      ) : null}

      {isPending ? (
        <LoadingState label="Loading topology…" />
      ) : isError ? (
        <ErrorState error={error} title="Failed to load topology" />
      ) : nodes.length === 0 ? (
        <EmptyState>No sessions match the current filters.</EmptyState>
      ) : (
        <>
          <div className={styles.vizArea}>
            <TopologyRenderer
              positioned={positioned}
              edges={edges}
              width={VIEW_WIDTH}
              height={VIEW_HEIGHT}
              selectedId={null}
              onNodeClick={(p) => {
                void navigate(`/sessions/${encodeURIComponent(sessionIdFromNode(p.node.id))}`);
              }}
            />
          </div>
          <Legend />
        </>
      )}
    </section>
  );
}

/** Legend explains the encodings (cross-session nodes are all sessions). */
function Legend() {
  return (
    <div className={styles.legend} aria-label="Topology legend">
      <span className={styles.legendItem}>
        <span className={styles.legendSwatch} style={{ background: 'var(--text-secondary)' }} />
        Session (circle)
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
