import { useEffect, useId, useState } from 'react';
import { Link } from 'react-router-dom';
import type { Filters } from '../../state/filters';
import { useSearch } from '../../api/stats';
import { LoadingState, ErrorState } from '../../components/StatusViews';
import type { SearchLogHit, SearchOpHit, SearchResponse } from '../../api/types';
import { formatTimestamp } from '../../lib/format';
import styles from './Stats.module.css';

// Deep full-text search box for the /stats dashboard (ui-pages.md §/stats).
// A labelled text input is DEBOUNCED (~300ms) so useSearch does not fire on
// every keystroke; the resolved query feeds GET /api/search. Op hits link to
// their session (/sessions/:session_id, the op_id is carried for future
// op-anchoring) and show kind/name/model + the FTS snippet; log hits link to
// the session and show severity/ts + snippet. When the matched source has log
// indexing disabled (logs_indexed === false) a clear note replaces the empty
// log list, so "no log matches" is never confused with "logs not indexed here".
//
// Search is INTENTIONALLY NOT live-invalidated by SSE: its query key is
// ['search', …] (outside the ['stats'] prefix api/sse.ts invalidates), because a
// result list reordering under the user on every ingest is poor UX. It refetches
// only when q or the filters change.

const DEBOUNCE_MS = 300;

/** useDebounced returns `value` delayed by `ms`, resetting the timer on change. */
function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => { setDebounced(value); }, ms);
    return () => { clearTimeout(id); };
  }, [value, ms]);
  return debounced;
}

export interface SearchBoxProps {
  filters: Filters;
}

export function SearchBox({ filters }: SearchBoxProps) {
  const inputId = useId();
  const [query, setQuery] = useState('');
  // The debounced term is what actually drives the request; the hook is disabled
  // on empty/whitespace q (api/stats useSearch), so a cleared box fires nothing.
  const debouncedQuery = useDebounced(query, DEBOUNCE_MS);
  const search = useSearch(filters, debouncedQuery);

  const hasQuery = debouncedQuery.trim().length > 0;

  return (
    <div className={styles.search}>
      <label className={styles.searchField} htmlFor={inputId}>
        <span className={styles.controlLabel}>Search ops and logs</span>
        <input
          id={inputId}
          type="search"
          className={styles.searchInput}
          placeholder="Full-text query…"
          value={query}
          onChange={(e) => { setQuery(e.target.value); }}
          autoComplete="off"
        />
      </label>

      {!hasQuery ? (
        // Empty query: nothing fetched, nothing shown (the hook is disabled).
        <p className={styles.searchHint}>Type a query to search ops and logs.</p>
      ) : search.isPending ? (
        <LoadingState label="Searching…" />
      ) : search.isError ? (
        <ErrorState error={search.error} title="Search failed" />
      ) : (
        <SearchResults {...searchResultsFrom(search.data)} />
      )}
    </div>
  );
}

function searchResultsFrom(data: SearchResponse | undefined): SearchResultsProps {
  const ops = data?.ops;
  const logs = data?.logs;
  return {
    ops: Array.isArray(ops) ? ops : [],
    logs: Array.isArray(logs) ? logs : [],
    logsIndexed: typeof data?.logs_indexed === 'boolean' ? data.logs_indexed : true,
  };
}

interface SearchResultsProps {
  ops: SearchOpHit[];
  logs: SearchLogHit[];
  logsIndexed: boolean;
}

/** SearchResults lists op + log hits; each links to its session. */
function SearchResults({ ops, logs, logsIndexed }: SearchResultsProps) {
  const empty = ops.length === 0 && logs.length === 0;

  return (
    <div className={styles.results} role="region" aria-label="Search results">
      {empty && logsIndexed ? <p className={styles.searchHint}>No matches.</p> : null}

      {ops.length > 0 ? (
        <section aria-labelledby="search-ops-title">
          <h3 id="search-ops-title" className={styles.resultsTitle}>
            Ops ({ops.length})
          </h3>
          <ul className={styles.hitList}>
            {ops.map((op) => (
              <li key={op.op_id} className={styles.hit}>
                <Link
                  className={styles.hitLink}
                  to={`/sessions/${encodeURIComponent(op.session_id)}`}
                >
                  {op.name || op.kind}
                </Link>
                <span className={styles.hitMeta}>
                  {op.kind}
                  {op.model ? ` · ${op.model}` : ''}
                </span>
                <span className={styles.hitSnippet}>{op.snippet}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      {/* Logs: when indexing is disabled the source returns logs_indexed=false;
          show a clear note instead of an empty list (ui-pages.md §/stats). */}
      <section aria-labelledby="search-logs-title">
        <h3 id="search-logs-title" className={styles.resultsTitle}>
          Logs{logsIndexed ? ` (${logs.length})` : ''}
        </h3>
        {!logsIndexed ? (
          <p className={styles.searchHint} role="note">
            Logs are not indexed on this source — only ops are searchable here.
          </p>
        ) : logs.length === 0 ? (
          <p className={styles.searchHint}>No log matches.</p>
        ) : (
          <ul className={styles.hitList}>
            {logs.map((log) => (
              <li key={log.log_id} className={styles.hit}>
                <Link
                  className={styles.hitLink}
                  to={`/sessions/${encodeURIComponent(log.session_id)}`}
                >
                  {log.severity}
                </Link>
                <span className={styles.hitMeta}>{formatTimestamp(log.ts)}</span>
                <span className={styles.hitSnippet}>{log.snippet}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
