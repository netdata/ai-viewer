import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useFilters, filtersToSubscription } from '../../state/filters';
import { useSessionsInfinite } from '../../api/sessions';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { SessionRowBody } from '../../components/SessionRow';
import { LoadMore } from '../../components/LoadMore';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import type { SessionListItem } from '../../api/types';
import { formatNumber } from '../../lib/format';
import styles from './SessionsList.module.css';

// Sessions list page (ui-pages.md §"/"). Root sessions for the active filter,
// keyset-paginated with a "Load more" control, live-refreshed over SSE. The
// FilterBar (in Layout) drives `filters` via the URL; this page only reads them.
//
// SOW-0068: a "Show secondary" toggle switches the query from group='root'
// (primary only, the default) to group='all', revealing sub-agent / tool-
// internal / fork sessions. Each secondary row is marked with a kind badge and
// carries a "↩ parent" link to its parent's Topology tab (rendered in
// SessionRowBody). The toggle is LOCAL view state (a quick switch, not a
// shareable filter); the default stays root-only.

/**
 * ChildExpander shows child_session_count as a drill-down link to the session
 * DETAIL page (whose Overview lists the session's child_sessions), or a dash
 * when there are none. It deliberately does NOT emit a `?root=` query — no list
 * filter consumes `root` (readFilters parses only `group`, and /api/sessions has
 * no root/root_session_id filter), so that affordance would be a dead link.
 */
function ChildExpander({ session }: { session: SessionListItem }) {
  if (session.child_session_count <= 0) {
    return <span className={styles.noChildren}>—</span>;
  }
  return (
    <Link
      to={`/sessions/${encodeURIComponent(session.id)}`}
      className={styles.childLink}
      aria-label={`${session.child_session_count} child sessions`}
    >
      ▸ {formatNumber(session.child_session_count)}
    </Link>
  );
}

export function SessionsList() {
  const { filters } = useFilters();
  // Primary (root) only by default; "Show secondary" widens to all kinds so the
  // operator can surface sub-agent / tool-internal / fork sessions.
  const [showSecondary, setShowSecondary] = useState(false);
  const group = showSecondary ? 'all' : 'root';
  const {
    data,
    isPending,
    isError,
    error,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useSessionsInfinite(filters, group);

  // One live subscription for the active filter; SSE invalidates ['sessions'].
  useLiveUpdates(filtersToSubscription(filters));

  const items = data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section aria-labelledby="sessions-title">
      <div className={styles.header}>
        <h1 id="sessions-title">Sessions</h1>
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={showSecondary}
            onChange={(e) => { setShowSecondary(e.target.checked); }}
          />
          <span>Show secondary</span>
        </label>
      </div>

      {isPending ? (
        <LoadingState label="Loading sessions…" />
      ) : isError ? (
        <ErrorState error={error} title="Failed to load sessions" />
      ) : items.length === 0 ? (
        <EmptyState>No sessions match the current filters.</EmptyState>
      ) : (
        <>
          <div className={styles.tableWrap} tabIndex={0} role="region" aria-label="Sessions table">
            <table className={styles.table}>
              <thead>
                <tr>
                  <th className={styles.childCol} aria-label="Child sessions">
                    ⤷
                  </th>
                  <th>Agent</th>
                  <th>Model</th>
                  <th>Source</th>
                  <th>Start</th>
                  <th>Duration</th>
                  <th>Status</th>
                  <th className={styles.numCol}>Turns</th>
                  <th className={styles.numCol}>Ops</th>
                  <th className={styles.numCol}>Tokens in (fresh)</th>
                  <th className={styles.numCol}>Tokens out</th>
                  <th className={styles.numCol}>Cost</th>
                  <th className={styles.numCol}>Failures</th>
                </tr>
              </thead>
              <tbody>
                {items.map((session) => (
                  <tr key={session.id} className={styles.bodyRow}>
                    <td className={styles.childCell}>
                      <ChildExpander session={session} />
                    </td>
                    <SessionRowBody session={session} />
                  </tr>
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
          />
        </>
      )}
    </section>
  );
}
