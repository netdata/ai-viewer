import { useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { SpanDetailDrawer } from '../../../components/SpanDetailDrawer';
import { EmptyState } from '../../../components/StatusViews';
import type { TraceNode } from '../../../viz/trace';
import { Waterfall } from './Waterfall';
import { ByTurnWaterfall } from './ByTurnWaterfall';
import { FlameGraph } from './FlameGraph';
import { EventList } from './EventList';
import { useTraceNodes, type WaterfallMode } from './useTraceNodes';
import styles from './TraceTab.module.css';

// Trace (APM) tab — the centerpiece view. Renders ONE op tree from the WHOLE
// session tree (SOW-0070), tagged by owning session, with three sub-views:
//
//   - Waterfall (default) — Detailed|By-turn toggle.
//   - Flame graph.
//   - Event list — virtualized list of every op.
//
// Originally lived behind the ?tab=trace URL. Now also embedded in the
// unified Session Detail view (ui-turn-view.md §ui-session-unified-view):
// the unified shell renders this component twice (mode='viz' for the top
// half, mode='events' for the bottom half) sharing the SAME useTraceNodes
// state via the hook.
//
// `mode` controls which sections are rendered:
//   - 'full'   → toolbar + viz + event list + drawer (legacy Trace tab)
//   - 'viz'    → toolbar + viz + drawer (unified-view top pane)
//   - 'events' → event list + drawer (unified-view bottom pane)

export type TraceTabMode = 'full' | 'viz' | 'events';

type View = 'waterfall' | 'flame';

export function TraceTab({ detail, mode = 'full' }: { detail: SessionDetailResponse; mode?: TraceTabMode }) {
  const [view, setView] = useState<View>('waterfall');
  const [waterfallMode, setWaterfallMode] = useState<WaterfallMode>('detailed');
  const [selected, setSelected] = useState<TraceNode | null>(null);

  const { flatAll, flat, roots, agentOptions, filters, setFilters, useCanvas, turnBoundaryIds } = useTraceNodes(detail);
  const selectedId = selected?.op.id ?? null;

  if (flatAll.length === 0) {
    return <EmptyState>No operations recorded for this session.</EmptyState>;
  }

  const KIND_OPTIONS = ['all', 'llm', 'tool', 'session', 'reasoning'];
  const STATUS_OPTIONS = ['all', 'completed', 'failed'];

  const showToolbar = mode === 'full' || mode === 'viz';
  const showViz = mode === 'full' || mode === 'viz';
  const showEvents = mode === 'full' || mode === 'events';

  return (
    <div className={styles.wrap}>
      {showToolbar ? (
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
              value={filters.kind}
              onChange={(e) => {
                setFilters.setKind(e.target.value);
              }}
              aria-label="Filter by op kind"
            >
              {KIND_OPTIONS.map((k) => (
                <option key={k} value={k}>{k === 'all' ? 'All kinds' : k}</option>
              ))}
            </select>
            <select
              className={styles.filterSelect}
              value={filters.status}
              onChange={(e) => {
                setFilters.setStatus(e.target.value);
              }}
              aria-label="Filter by status"
            >
              {STATUS_OPTIONS.map((s) => (
                <option key={s} value={s}>{s === 'all' ? 'All statuses' : s}</option>
              ))}
            </select>
            {agentOptions.length > 1 ? (
              <select
                className={styles.filterSelect}
                value={filters.agent}
                onChange={(e) => {
                  setFilters.setAgent(e.target.value);
                }}
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
      ) : null}

      {showViz ? (
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
      ) : null}

      {showEvents ? (
        <section className={styles.eventSection} aria-labelledby="event-list-title">
          <h2 id="event-list-title" className={styles.eventTitle}>
            Event list
          </h2>
          <EventList nodes={flat} onSelect={setSelected} selectedId={selectedId} />
        </section>
      ) : null}

      <SpanDetailDrawer
        detail={selected ? { kind: 'op', op: selected.op } : null}
        sessionId={selected?.op.session_id ?? detail.session.id}
        onClose={() => {
          setSelected(null);
        }}
      />
    </div>
  );
}
