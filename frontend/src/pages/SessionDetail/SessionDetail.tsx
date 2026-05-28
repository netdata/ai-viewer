import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { ComingSoon } from '../../components/ComingSoon';
import { OverviewTab } from './OverviewTab';
import { LogsTab } from './LogsTab';
import styles from './SessionDetail.module.css';

// Session detail page (ui-pages.md §/sessions/:id). Phase-1 tabs Overview + Logs
// are real (placeholder bodies wired to the id); Trace/Topology/Timeline are
// scaffolded as Phase-2 tabs rendering ComingSoon. The active tab is local UI
// state (frontend-architecture.md §State Management). The session id comes from
// the route; the data hooks (useSessionDetail) drop into the tab bodies.

type TabKey = 'overview' | 'trace' | 'topology' | 'timeline' | 'logs';

const TABS: ReadonlyArray<{ key: TabKey; label: string; phase2?: boolean }> = [
  { key: 'overview', label: 'Overview' },
  { key: 'trace', label: 'Trace', phase2: true },
  { key: 'topology', label: 'Topology', phase2: true },
  { key: 'timeline', label: 'Timeline', phase2: true },
  { key: 'logs', label: 'Logs' },
];

export function SessionDetail() {
  const { id = '' } = useParams<{ id: string }>();
  const [tab, setTab] = useState<TabKey>('overview');

  return (
    <section aria-labelledby="session-detail-title">
      <h1 id="session-detail-title" className={styles.title}>
        Session <code className={styles.id}>{id}</code>
      </h1>

      <div className={styles.tabs} role="tablist" aria-label="Session views">
        {TABS.map((t) => (
          <button
            key={t.key}
            type="button"
            role="tab"
            aria-selected={tab === t.key}
            className={tab === t.key ? `${styles.tab} ${styles.tabActive}` : styles.tab}
            onClick={() => {
              setTab(t.key);
            }}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div role="tabpanel" className={styles.panel}>
        {tab === 'overview' && <OverviewTab sessionId={id} />}
        {tab === 'logs' && <LogsTab sessionId={id} />}
        {tab === 'trace' && <ComingSoon title="Trace (APM)" note="Span tree — Phase 2." />}
        {tab === 'topology' && (
          <ComingSoon title="Topology" note="Force-directed actor graph — Phase 2." />
        )}
        {tab === 'timeline' && (
          <ComingSoon title="Timeline" note="Time-axis span lanes — Phase 2." />
        )}
      </div>
    </section>
  );
}
