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
// Vite's `?worker` suffix is a build-time virtual module whose default export
// (the Worker constructor) is synthesized by Vite. eslint-plugin-import resolves
// the suffix-stripped `forceWorker.ts` (named exports only, no default) and so
// false-positives on import/default; the type side is correct via vite/client.
// eslint-disable-next-line import/default
import ForceWorker from '../../viz/forceWorker?worker';
import { TopologyRenderer } from '../SessionDetail/TopologyTab/TopologyRenderer';
import { formatNumber } from '../../lib/format';

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

const METRICS: readonly { key: TopologyMetric; label: string }[] = [
  { key: 'cost', label: 'Cost' },
  { key: 'tokens', label: 'Tokens' },
  { key: 'duration', label: 'Duration' },
  { key: 'calls', label: 'Calls' },
  { key: 'ctx_pct', label: 'Context %' },
];

const MODES: readonly { key: TopologyLayoutMode; label: string }[] = [
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
  const positioned: PositionedNode[] = frozenLayout !== null
    ? reapplyFrozenPositions(nodes, frozenLayout, opts)
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
    <section aria-labelledby="topology-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="topology-title" className="text-2xl font-semibold tracking-tight">Topology</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Cross-session actor graph. Each circle is one session, sized by the
          selected metric. Hover for tooltips, click to open the session.
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="inline-flex items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1 text-sm">
          <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Size by</span>
          <select
            className="bg-transparent text-sm text-foreground focus:outline-none"
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

        <fieldset className="inline-flex items-center gap-1 rounded-md border border-border bg-card p-1">
          <legend className="sr-only">Layout</legend>
          {MODES.map((m) => (
            <label
              key={m.key}
              className={`inline-flex cursor-pointer items-center gap-1.5 rounded-sm px-2.5 py-1 text-xs ${
                mode === m.key
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              <input
                type="radio"
                name="cross-topology-mode"
                checked={mode === m.key}
                onChange={() => {
                  onSelectMode(m.key);
                }}
                className="sr-only"
              />
              <span>{m.label}</span>
            </label>
          ))}
        </fieldset>

        <button
          type="button"
          className={`inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-sm transition-colors ${
            frozen
              ? 'border-primary bg-primary text-primary-foreground'
              : 'border-border bg-card text-foreground hover:bg-accent'
          }`}
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

        <span className="ml-auto inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground tabular-nums">
          <span>{nodes.length}</span>
          <span>session{nodes.length === 1 ? '' : 's'}</span>
        </span>
      </div>

      {truncated ? (
        <p className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground" role="status">
          Showing top {formatNumber(nodes.length)} sessions by{' '}
          {METRICS.find((m) => m.key === metric)?.label.toLowerCase() ?? metric}; narrow the filter
          to see the rest.
        </p>
      ) : null}

      {isPending ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <LoadingState label="Loading topology…" />
        </div>
      ) : isError ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <ErrorState error={error} title="Failed to load topology" />
        </div>
      ) : nodes.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border bg-card/50 p-12">
          <EmptyState>No sessions match the current filters.</EmptyState>
        </div>
      ) : (
        <>
          <div className="overflow-hidden rounded-lg border border-border bg-card">
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
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-card px-3 py-2 text-xs text-muted-foreground" aria-label="Topology legend">
      <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground/80">Legend</span>
      <span className="inline-flex items-center gap-1.5">
        <span
          aria-hidden
          className="inline-block size-2.5 rounded-full"
          style={{ backgroundColor: 'var(--muted-foreground)' }}
        />
        Session
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span
          aria-hidden
          className="inline-block size-2.5 rounded-full"
          style={{ backgroundColor: 'var(--status-completed)' }}
        />
        No failures
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span
          aria-hidden
          className="inline-block size-2.5 rounded-full"
          style={{ backgroundColor: 'var(--status-running)' }}
        />
        Some failures
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span
          aria-hidden
          className="inline-block size-2.5 rounded-full"
          style={{ backgroundColor: 'var(--status-failed)' }}
        />
        Many failures
      </span>
    </div>
  );
}
