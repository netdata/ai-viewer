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
import { FlameGraph } from './FlameGraph';
import { EventList } from './EventList';
import styles from './TraceTab.module.css';

// Trace (APM) tab — the centerpiece view (ui-pages.md §/sessions/:id #3). Builds
// ONE op tree from the session-detail response (turns→ops; nesting derived by
// temporal containment in viz/trace) and renders it three ways: a waterfall
// (default), a flame-graph (toggle), and an always-present virtualized event
// list. A click on any span/row opens the shared span detail drawer. Above the
// SVG span ceiling the visuals switch to Canvas + culling (viz/trace) so a big
// trace stays fast. Op-kind/status colors come from theme tokens (viz/color),
// re-read on a data-theme MutationObserver.

type View = 'waterfall' | 'flame';

export function TraceTab({ detail }: { detail: SessionDetailResponse }) {
  const [view, setView] = useState<View>('waterfall');
  const [selected, setSelected] = useState<TraceNode | null>(null);

  // Keep the viz palette in sync with theme flips while this tab is mounted.
  useEffect(() => startThemeColorWatch(), []);

  const roots = useMemo(() => buildOpTree(detail.turns), [detail.turns]);
  const flat = useMemo(() => flattenTree(roots), [roots]);

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
        <span className={styles.opCount}>{flat.length} ops</span>
      </div>

      <div className={styles.vizArea}>
        {view === 'waterfall' ? (
          <Waterfall
            nodes={flat}
            onSelect={setSelected}
            selectedId={selectedId}
            useCanvas={useCanvas}
          />
        ) : (
          <FlameGraph
            nodes={flat}
            roots={roots}
            onSelect={setSelected}
            selectedId={selectedId}
            useCanvas={useCanvas}
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
        op={selected?.op ?? null}
        onClose={() => {
          setSelected(null);
        }}
      />
    </div>
  );
}
