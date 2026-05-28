import { useState } from 'react';
import { useSessionLogs } from '../../../api/logs';
import { LogRow } from '../../../components/LogRow';
import { LoadMore } from '../../../components/LoadMore';
import { LoadingState, ErrorState, EmptyState } from '../../../components/StatusViews';
import type { LogSeverity } from '../../../api/types';
import styles from './LogsTab.module.css';

// Logs tab (ui-pages.md §/sessions/:id #5). A severity multi-select drives the
// useSessionLogs infinite query; a "Load more" control consumes the keyset
// cursor. Selecting no severities = all (the empty set omits the param — the
// server rejects a present-but-empty severity, rest-api.md §Conventions). The
// severity set is local tab state; changing it starts a fresh first page (the
// query key carries the severities, to which the server binds the cursor).

const SEVERITIES: readonly LogSeverity[] = ['DBG', 'INF', 'WRN', 'ERR'];

export function LogsTab({ sessionId }: { sessionId: string }) {
  const [severities, setSeverities] = useState<LogSeverity[]>([]);
  const {
    data,
    isPending,
    isError,
    error,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useSessionLogs(sessionId, { severities });

  const toggle = (sev: LogSeverity, checked: boolean): void => {
    setSeverities((prev) =>
      checked ? [...prev, sev] : prev.filter((s) => s !== sev),
    );
  };

  const items = data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className={styles.wrap}>
      <fieldset className={styles.severitySet}>
        <legend className={styles.legend}>Severity</legend>
        {SEVERITIES.map((sev) => (
          <label key={sev} className={styles.checkbox}>
            <input
              type="checkbox"
              checked={severities.includes(sev)}
              onChange={(e) => {
                toggle(sev, e.target.checked);
              }}
            />
            <span>{sev}</span>
          </label>
        ))}
        <span className={styles.hint}>
          {severities.length === 0 ? 'all severities' : `${severities.length} selected`}
        </span>
      </fieldset>

      {isPending ? (
        <LoadingState label="Loading logs…" />
      ) : isError ? (
        <ErrorState error={error} title="Failed to load logs" />
      ) : items.length === 0 ? (
        <EmptyState>No log entries for this session.</EmptyState>
      ) : (
        <>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Severity</th>
                  <th>Source</th>
                  <th>Op</th>
                  <th>Message</th>
                </tr>
              </thead>
              <tbody>
                {items.map((entry, i) => (
                  <LogRow key={`${entry.ts}-${entry.op_id ?? ''}-${i}`} entry={entry} />
                ))}
              </tbody>
            </table>
          </div>
          <LoadMore
            hasNextPage={hasNextPage}
            isFetching={isFetchingNextPage}
            onLoadMore={() => {
              void fetchNextPage();
            }}
            label="Load more logs"
          />
        </>
      )}
    </div>
  );
}
