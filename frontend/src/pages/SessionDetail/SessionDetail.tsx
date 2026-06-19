import { useParams, useSearchParams } from 'react-router-dom';
import { Tabs, type TabSpec } from '../../components/Tabs';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import { useSessionDetail } from '../../api/sessions';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { ApiError } from '../../api/client';
import { OverviewTab } from './OverviewTab';
import { LogsTab } from './LogsTab';
import { TraceTab } from './TraceTab';
import { TopologyTab } from './TopologyTab';
import { TimelineTab } from './TimelineTab';
import { RawDataTab } from './RawDataTab';

// Session detail page (ui-pages.md §/sessions/:id). Tabs Overview + Trace +
// Topology + Timeline + Logs + Raw Data are all real. The active tab lives in
// the URL (?tab=) so it is shareable; an unknown value falls back to overview.
// An unknown id (404) renders a clean "not found" state instead of the tabs. The
// open session is live-refreshed over SSE (session_changed → ['session', id]).

type TabKey = 'overview' | 'trace' | 'topology' | 'timeline' | 'logs' | 'raw';

const TABS: readonly TabSpec<TabKey>[] = [
  { key: 'overview', label: 'Overview' },
  { key: 'trace', label: 'Trace' },
  { key: 'topology', label: 'Topology' },
  { key: 'timeline', label: 'Timeline' },
  { key: 'logs', label: 'Logs' },
  { key: 'raw', label: 'Raw Data' },
];

const TAB_KEYS = new Set<TabKey>(TABS.map((t) => t.key));

/** parseTab coerces the URL ?tab= value to a known key (default overview). */
function parseTab(raw: string | null): TabKey {
  return raw !== null && TAB_KEYS.has(raw as TabKey) ? (raw as TabKey) : 'overview';
}

export function SessionDetail() {
  const { id = '' } = useParams<{ id: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseTab(searchParams.get('tab'));

  const { data, isPending, isError, error } = useSessionDetail(id);

  // Live refresh of the open session; session_changed invalidates ['session', id].
  useLiveUpdates({ session_id: id });

  const setTab = (next: TabKey): void => {
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev);
        sp.set('tab', next);
        return sp;
      },
      { replace: true },
    );
  };

  const notFound = isError && error instanceof ApiError && error.status === 404;

  return (
    <section aria-labelledby="session-detail-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="session-detail-title" className="text-2xl font-semibold tracking-tight">
          Session detail
        </h1>
        <p className="mt-1 flex items-center gap-2 font-mono text-xs text-muted-foreground">
          <span className="text-[10px] font-sans uppercase tracking-wider">id</span>
          <code className="rounded bg-muted px-1.5 py-0.5 text-foreground">{id}</code>
        </p>
      </div>

      {notFound ? (
        <div className="rounded-lg border border-dashed border-border bg-card/50 p-12">
          <EmptyState>Session not found.</EmptyState>
        </div>
      ) : isPending ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <LoadingState label="Loading session…" />
        </div>
      ) : isError ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <ErrorState error={error} title="Failed to load session" />
        </div>
      ) : (
        <>
          <Tabs tabs={TABS} active={tab} onSelect={setTab} ariaLabel="Session views" />
          <div
            role="tabpanel"
            id={`tabpanel-${tab}`}
            aria-labelledby={`tab-${tab}`}
            aria-live="polite"
            className="rounded-lg border border-border bg-card p-5"
          >
            {tab === 'overview' && <OverviewTab detail={data} />}
            {tab === 'logs' && <LogsTab sessionId={id} />}
            {tab === 'trace' && <TraceTab detail={data} />}
            {tab === 'topology' && <TopologyTab sessionId={id} />}
            {tab === 'timeline' && <TimelineTab sessionId={id} />}
            {tab === 'raw' && <RawDataTab detail={data} />}
          </div>
        </>
      )}
    </section>
  );
}
