import { useEffect, useMemo, useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { useSessionTrace } from '../../../api/sessions';
import { SpanDetailDrawer } from '../../../components/SpanDetailDrawer';
import { EmptyState } from '../../../components/StatusViews';
import {
  buildMergedTree,
  buildOpTree,
  flattenTree,
  SVG_SPAN_CEILING,
  type TraceNode,
} from '../../../viz/trace';
import { startThemeColorWatch } from '../../../viz/color';
import { Waterfall } from './Waterfall';
import { ByTurnWaterfall } from './ByTurnWaterfall';
import { FlameGraph } from './FlameGraph';
import { EventList } from './EventList';
import styles from './TraceTab.module.css';

// Trace (APM) tab — the centerpiece view (ui-pages.md §/sessions/:id #3). Builds
// ONE op tree from the WHOLE session tree (SOW-0070): fetched from
// /api/sessions/:id/trace, every op of every session in the tree, merged so a
// sub-session's ops nest under the child_session_id op that spawned them. Each
// op is tagged with its owning session so the Event List shows a per-row
// sub-agent indicator and the operator can filter by sub-agent. Falls back to
// the single-session tree (buildOpTree from detail.turns) while the whole-tree
// fetch is in flight, so the tab never blocks. Renderings: a waterfall (default)
// with a Detailed|By-turn sub-view (decision #6), a flame-graph (toggle), and an
// always-present virtualized event list. Source-aware: a point-event op
// (end_ts == start_ts) draws as an instant tick, never a zero-width bar
// (decision #7). A click on any span/row opens the shared span detail drawer.
// Above the SVG span ceiling the visuals switch to Canvas + culling (viz/trace)
// so a big trace stays fast. Op-kind/status colors come from theme tokens
// (viz/color), re-read on a data-theme MutationObserver.

type View = 'waterfall' | 'flame';
type WaterfallMode = 'detailed' | 'byturn';

export function TraceTab({ detail }: { detail: SessionDetailResponse }) {
  const [view, setView] = useState<View>('waterfall');
  const [waterfallMode, setWaterfallMode] = useState<WaterfallMode>('detailed');
  const [selected, setSelected] = useState<TraceNode | null>(null);
  const [kindFilter, setKindFilter] = useState<string>('all');
  const [statusFilter, setStatusFilter] = useState<string>('all');
  const [agentFilter, setAgentFilter] = useState<string>('all');

  // Keep the viz palette in sync with theme flips while this tab is mounted.
  useEffect(() => startThemeColorWatch(), []);

  // Whole-tree trace (SOW-0070): every op of every session in the tree, tagged
  // by owning session. While it loads (or on error) fall back to the
  // single-session tree so the tab is never empty.
  const trace = useSessionTrace(detail.session.id);
  const roots = useMemo(() => {
    if (trace.data && trace.data.ops.length > 0) {
      return buildMergedTree(trace.data.ops);
    }
    return buildOpTree(detail.turns);
  }, [trace.data, detail.turns]);
  const flatAll = useMemo(() => flattenTree(roots), [roots]);

  // Distinct sub-agent names drive the sub-agent filter (SOW-0070 AC4). Empty
  // (single-session trace) → no filter control is rendered.
  const agentOptions = useMemo(() => {
    const names = new Set<string>();
    for (const n of flatAll) {
      if (n.sessionAgent) {
        names.add(n.sessionAgent);
      }
    }
    return [...names].sort();
  }, [flatAll]);

  // Apply the kind + status + sub-agent filters to the flat op list (SOW-0070).
  const flat = useMemo(() => {
    return flatAll.filter((n) => {
      if (kindFilter !== 'all' && n.op.kind !== kindFilter) return false;
      if (statusFilter === 'failed' && n.op.error_class === null) return false;
      if (statusFilter === 'completed' && n.op.status !== 'completed') return false;
      if (agentFilter !== 'all' && (n.sessionAgent ?? '') !== agentFilter) return false;
      return true;
    });
  }, [flatAll, kindFilter, statusFilter, agentFilter]);
  // Op ids that start a new turn (after the first) — for the Detailed view's
  // turn-boundary rules (decision #6).
  const turnBoundaryIds = useMemo(() => turnBoundaries(detail), [detail]);

  const useCanvas = flat.length > SVG_SPAN_CEILING;
  const selectedId = selected?.op.id ?? null;

  if (flatAll.length === 0) {
    return <EmptyState>No operations recorded for this session.</EmptyState>;
  }

  const KIND_OPTIONS = ['all', 'llm', 'tool', 'session', 'reasoning'];
  const STATUS_OPTIONS = ['all', 'completed', 'failed'];

  return (
    <div className={styles.wrap}>
      <div className={styles.toolbar}>
        <fieldset className={styles.viewToggle}>
          <legend className={styles.srOnly}>Trace view</legend>
          <label className={styles.toggleOption}>
            <input
              type="radio"
              name="trace-view"
              checked={view === 'waterfall'}
              onChange={() => {
                setView('waterfall');
              }}
            />
            <span>Waterfall</span>
          </label>
          <label className={styles.toggleOption}>
            <input
              type="radio"
              name="trace-view"
              checked={view === 'flame'}
              onChange={() => {
                setView('flame');
              }}
            />
            <span>Flame</span>
          </label>
        </fieldset>

        {view === 'waterfall' ? (
          <fieldset className={styles.viewToggle}>
            <legend className={styles.srOnly}>Detail level</legend>
            <label className={styles.toggleOption}>
              <input
                type="radio"
                name="trace-waterfall-mode"
                checked={waterfallMode === 'detailed'}
                onChange={() => {
                  setWaterfallMode('detailed');
                }}
              />
              <span>Detailed</span>
            </label>
            <label className={styles.toggleOption}>
              <input
                type="radio"
                name="trace-waterfall-mode"
                checked={waterfallMode === 'byturn'}
                onChange={() => {
                  setWaterfallMode('byturn');
                }}
              />
              <span>By-turn</span>
            </label>
          </fieldset>
        ) : null}

        <fieldset className={styles.filterGroup}>
          <legend className={styles.srOnly}>Op kind filter</legend>
          <select
            className={styles.filterSelect}
            value={kindFilter}
            onChange={(e) => setKindFilter(e.target.value)}
            aria-label="Filter by op kind"
          >
            {KIND_OPTIONS.map((k) => (
              <option key={k} value={k}>{k === 'all' ? 'All kinds' : k}</option>
            ))}
          </select>
          <select
            className={styles.filterSelect}
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            aria-label="Filter by status"
          >
            {STATUS_OPTIONS.map((s) => (
              <option key={s} value={s}>{s === 'all' ? 'All statuses' : s}</option>
            ))}
          </select>
          {agentOptions.length > 1 ? (
            <select
              className={styles.filterSelect}
              value={agentFilter}
              onChange={(e) => setAgentFilter(e.target.value)}
              aria-label="Filter by sub-agent"
            >
              <option value="all">All sub-agents</option>
              {agentOptions.map((a) => (
                <option key={a} value={a}>{a}</option>
              ))}
            </select>
          ) : null}
        </fieldset>

        <span className={styles.opCount}>
          {flat.length === flatAll.length ? `${flat.length} ops` : `${flat.length} / ${flatAll.length} ops`}
        </span>
      </div>

      <div className={styles.vizArea}>
        {view === 'flame' ? (
          <FlameGraph
            nodes={flat}
            roots={roots}
            onSelect={setSelected}
            selectedId={selectedId}
            useCanvas={useCanvas}
          />
        ) : waterfallMode === 'byturn' ? (
          <ByTurnWaterfall turns={detail.turns} onSelect={setSelected} selectedId={selectedId} />
        ) : (
          <Waterfall
            nodes={flat}
            onSelect={setSelected}
            selectedId={selectedId}
            useCanvas={useCanvas}
            turnBoundaryIds={turnBoundaryIds}
          />
        )}
      </div>

      <section className={styles.eventSection} aria-labelledby="event-list-title">
        <h2 id="event-list-title" className={styles.eventTitle}>
          Event list
        </h2>
        <EventList nodes={flat} onSelect={setSelected} selectedId={selectedId} />
      </section>

      <SpanDetailDrawer
        detail={selected ? { kind: 'op', op: selected.op } : null}
        onClose={() => {
          setSelected(null);
        }}
      />
    </div>
  );
}

/**
 * turnBoundaries collects the op id that starts each turn AFTER the first — the
 * earliest-starting op in that turn — so the Detailed waterfall draws a
 * separator rule above it (decision #6). The first turn gets no rule (nothing
 * precedes it); turns with no ops are skipped.
 */
function turnBoundaries(detail: SessionDetailResponse): ReadonlySet<string> {
  const ids = new Set<string>();
  detail.turns.forEach((turn, index) => {
    if (index === 0 || turn.ops.length === 0) {
      return;
    }
    let earliest: { id: string; start_ts: number } | null = null;
    for (const op of turn.ops) {
      if (earliest === null || op.start_ts < earliest.start_ts) {
        earliest = { id: op.id, start_ts: op.start_ts };
      }
    }
    if (earliest !== null) {
      ids.add(earliest.id);
    }
  });
  return ids;
}
