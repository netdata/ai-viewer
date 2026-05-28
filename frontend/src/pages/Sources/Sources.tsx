import { useSources, useHealth } from '../../api/sources';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import { formatDuration, formatNumber, formatTimestamp } from '../../lib/format';
import type { HealthStatus, SourceItem } from '../../api/types';
import styles from './Sources.module.css';

// Sources admin / status panel (ui-pages.md §/sources). A per-source table
// (id, format, enabled, parse_errors, lag, last_seq, last_seen) plus an overall
// health badge from /api/health. Live: source_status_changed invalidates both
// ['sources'] and ['health'].

const HEALTH_CLASS: Record<HealthStatus, string> = {
  ok: 'ok',
  degraded: 'degraded',
  down: 'down',
};

/** lagFor reads a source's ingest lag from the health snapshot (the sources
 *  list carries no lag field; lag is a health observability metric). */
function lagFor(lagBySource: Map<string, number>, source: SourceItem): string {
  const lag = lagBySource.get(source.id);
  return lag === undefined ? '—' : formatDuration(lag);
}

export function Sources() {
  const sources = useSources();
  const health = useHealth();

  // A source_status_changed frame refreshes both ['sources'] and ['health'].
  useLiveUpdates({});

  // On a health error, ignore any stale health.data: TanStack Query keeps the
  // last successful payload across a failed background refetch (the live
  // source_status_changed path), so lag must fall back to '—' via lagFor — not
  // show stale numbers beside the error banner. (ui-pages.md §/sources)
  const lagBySource = new Map<string, number>(
    (health.isError ? [] : (health.data?.sources ?? [])).map((s) => [s.id, s.lag_us]),
  );
  const items = sources.data?.items ?? [];

  return (
    <section aria-labelledby="sources-title">
      <div className={styles.headerRow}>
        <h1 id="sources-title">Sources</h1>
        {health.data && !health.isError ? (
          <span
            className={`${styles.health} ${styles[HEALTH_CLASS[health.data.status]] ?? ''}`}
            role="status"
          >
            {health.data.status}
          </span>
        ) : null}
      </div>

      {/* A /api/health failure is surfaced (AGENTS.md §"no silent failures"),
          never hidden behind dashes for lag. The sources table below still
          renders if only health failed (the two queries fail independently). */}
      {health.isError ? (
        <ErrorState error={health.error} title="Health unavailable" />
      ) : null}

      {sources.isPending ? (
        <LoadingState label="Loading sources…" />
      ) : sources.isError ? (
        <ErrorState error={sources.error} title="Failed to load sources" />
      ) : items.length === 0 ? (
        <EmptyState>No sources configured.</EmptyState>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>ID</th>
                <th>Format</th>
                <th>Enabled</th>
                <th className={styles.num}>Parse errors</th>
                <th className={styles.num}>Lag</th>
                <th className={styles.num}>Last seq</th>
                <th>Last seen</th>
              </tr>
            </thead>
            <tbody>
              {items.map((src) => (
                <tr key={src.id} className={styles.row}>
                  <td className={styles.mono}>{src.id}</td>
                  <td className={styles.mono}>{src.format}</td>
                  <td>{src.enabled ? 'yes' : 'no'}</td>
                  <td
                    className={`${styles.num} ${src.parse_errors > 0 ? styles.errors : ''}`}
                  >
                    {formatNumber(src.parse_errors)}
                  </td>
                  <td className={styles.num}>{lagFor(lagBySource, src)}</td>
                  <td className={styles.num}>{formatNumber(src.last_seq)}</td>
                  <td className={styles.mono}>{formatTimestamp(src.last_seen_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
