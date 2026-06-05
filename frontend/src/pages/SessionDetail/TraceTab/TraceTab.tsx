import { useEffect, useMemo, useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { SpanDetailDrawer } from '../../../components/SpanDetailDrawer';
import { EmptyState } from '../../../components/StatusViews';
import {
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
// ONE op tree from the session-detail response (turns→ops; nesting derived from
// the authoritative parent_op_id in viz/trace) and renders it as: a waterfall
// (default) with a Detailed|By-turn sub-view (decision #6), a flame-graph
// (toggle), and an always-present virtualized event list. Source-aware: a
// point-event op (end_ts == start_ts) draws as an instant tick, never a
// zero-width bar (decision #7). A click on any span/row opens the shared span
// detail drawer. Above the SVG span ceiling the visuals switch to Canvas +
// culling (viz/trace) so a big trace stays fast. Op-kind/status colors come from
// theme tokens (viz/color), re-read on a data-theme MutationObserver.

type View = 'waterfall' | 'flame';
type WaterfallMode = 'detailed' | 'byturn';

export function TraceTab({ detail }: { detail: SessionDetailResponse }) {
  const [view, setView] = useState<View>('waterfall');
  const [waterfallMode, setWaterfallMode] = useState<WaterfallMode>('detailed');
  const [selected, setSelected] = useState<TraceNode | null>(null);

  // Keep the viz palette in sync with theme flips while this tab is mounted.
  useEffect(() => startThemeColorWatch(), []);

  const roots = useMemo(() => buildOpTree(detail.turns), [detail.turns]);
  const flat = useMemo(() => flattenTree(roots), [roots]);
  // Op ids that start a new turn (after the first) — for the Detailed view's
  // turn-boundary rules (decision #6).
  const turnBoundaryIds = useMemo(() => turnBoundaries(detail), [detail]);

  const useCanvas = flat.length > SVG_SPAN_CEILING;
  const selectedId = selected?.op.id ?? null;

  if (flat.length === 0) {
    return <EmptyState>No operations recorded for this session.</EmptyState>;
  }

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

        {/* The Detailed|By-turn sub-toggle applies ONLY to the waterfall view
            (decision #6); it is hidden under flame. */}
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

        <span className={styles.opCount}>{flat.length} ops</span>
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
