// /search — dedicated full-text content search page (SOW-0091).
//
// Unlike the /stats SearchBox (which surfaces results inside the dashboard),
// /search is a top-level route with a single, large search field and three
// result sections — ops (metadata), logs (messages), and content
// (prompt/response text). Each section is independent; the operator
// typically finds what they want in `content` (the new fts_content
// FTS5 index).
//
// The query is URL-persisted (?q=...) so a focused view is shareable.
// Debounced (300 ms) so typing does not fire a request per keystroke.

import { useEffect, useId, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Search as SearchIcon } from 'lucide-react';
import { useSearch } from '../../api/stats';
import { useFilters } from '../../state/filters';
import { EmptyState, ErrorState, LoadingState } from '../../components/StatusViews';
import { Skeleton } from '../../components/ui/skeleton';
import type {
  SearchContentHit,
  SearchLogHit,
  SearchOpHit,
} from '../../api/types';
import { formatTimestamp } from '../../lib/format';
import styles from './Search.module.css';

const DEBOUNCE_MS = 300;

/** useDebounced returns `value` delayed by `ms`, resetting the timer on change. */
function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => {
      setDebounced(value);
    }, ms);
    return () => {
      clearTimeout(id);
    };
  }, [value, ms]);
  return debounced;
}

export function Search() {
  const [searchParams, setSearchParams] = useSearchParams();
  const inputId = useId();

  // URL is the source of truth for the current query. The debounced
  // local state drives the request; writes back to the URL on commit
  // so the view is shareable.
  const urlQ = searchParams.get('q') ?? '';
  const [query, setQuery] = useState(urlQ);
  const debouncedQuery = useDebounced(query, DEBOUNCE_MS);

  // Mirror debounced query back to the URL so a /search?q=foo link works
  // and so refreshing the page restores the query.
  useEffect(() => {
    const trimmed = debouncedQuery.trim();
    if (trimmed === urlQ.trim()) return;
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev);
        if (trimmed === '') sp.delete('q');
        else sp.set('q', trimmed);
        return sp;
      },
      { replace: true },
    );
    // We intentionally depend only on debouncedQuery — including
    // searchParams in deps would cause a feedback loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debouncedQuery]);

  const { filters } = useFilters();
  const search = useSearch(filters, debouncedQuery);

  const hasQuery = debouncedQuery.trim().length > 0;

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>
          <SearchIcon size={20} aria-hidden="true" />
          Search
        </h1>
        <p className={styles.subtitle}>
          Full-text search across sessions, logs, and prompt/response
          content. Type a phrase to find the session you ran last week.
        </p>
      </header>

      <div className={styles.searchField}>
        <label htmlFor={inputId} className={styles.label}>
          Query
        </label>
        <input
          id={inputId}
          type="search"
          className={styles.input}
          placeholder='e.g. "rate limiting", "permissions", "auth middleware"'
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
          }}
          autoComplete="off"
        />
      </div>

      {!hasQuery ? (
        <EmptyState>
          Type a query to search across ops, logs, and prompt/response
          content.
        </EmptyState>
      ) : search.isPending ? (
        <LoadingState label="Searching…" />
      ) : search.isError ? (
        <ErrorState error={search.error} title="Search failed" />
      ) : (
        <SearchResults
          ops={search.data.ops}
          logs={search.data.logs}
          content={search.data.content}
          logsIndexed={search.data.logs_indexed}
          hasQuery={hasQuery}
        />
      )}
    </div>
  );
}

interface SearchResultsProps {
  ops: SearchOpHit[];
  logs: SearchLogHit[];
  content: SearchContentHit[];
  logsIndexed: boolean;
  hasQuery: boolean;
}

function SearchResults({
  ops,
  logs,
  content,
  logsIndexed,
  hasQuery,
}: SearchResultsProps) {
  const total = ops.length + logs.length + content.length + logs.length + content.length;
  if (total === 0 && hasQuery) {
    return (
      <EmptyState>
        No matches. Try a shorter or different phrase — the search
        indexes op metadata, log messages, and prompt/response content.
      </EmptyState>
    );
  }

  return (
    <div className={styles.results}>
      <ContentSection content={content} />
      <OpsSection ops={ops} />
      <LogsSection logs={logs} logsIndexed={logsIndexed} />
    </div>
  );
}

function ContentSection({ content }: { content: SearchContentHit[] }) {
  return (
    <section className={styles.section} aria-labelledby="content-section">
      <h2 id="content-section" className={styles.sectionTitle}>
        Prompt / response content
        <span className={styles.sectionCount}>{content.length}</span>
      </h2>
      {content.length === 0 ? (
        <p className={styles.empty}>No prompt/response matches.</p>
      ) : (
        <ol className={styles.list}>
          {content.map((hit) => (
            <li key={hit.op_id} className={styles.card}>
              <header className={styles.cardHeader}>
                <Link
                  to={`/sessions/${hit.session_id}?op=${hit.op_id}`}
                  className={styles.cardLink}
                >
                  op <code className={styles.code}>{hit.op_id.slice(0, 8)}</code>
                </Link>
                <span className={styles.cardMeta}>
                  turn <code>{hit.turn_id.slice(0, 8)}</code> ·{' '}
                  session <code>{hit.session_id.slice(0, 8)}</code>
                </span>
              </header>
              <p className={styles.snippet}>{hit.snippet}</p>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function OpsSection({ ops }: { ops: SearchOpHit[] }) {
  return (
    <section className={styles.section} aria-labelledby="ops-section">
      <h2 id="ops-section" className={styles.sectionTitle}>
        Op metadata
        <span className={styles.sectionCount}>{ops.length}</span>
      </h2>
      {ops.length === 0 ? (
        <p className={styles.empty}>No op-metadata matches.</p>
      ) : (
        <ol className={styles.list}>
          {ops.map((hit) => (
            <li key={hit.op_id} className={styles.card}>
              <header className={styles.cardHeader}>
                <Link
                  to={`/sessions/${hit.session_id}?op=${hit.op_id}`}
                  className={styles.cardLink}
                >
                  <span className={styles.kind}>
                    {hit.kind} · {hit.name || hit.model}
                  </span>
                </Link>
                <span className={styles.cardMeta}>
                  op <code>{hit.op_id.slice(0, 8)}</code> · session{' '}
                  <code>{hit.session_id.slice(0, 8)}</code>
                </span>
              </header>
              <p className={styles.snippet}>{hit.snippet}</p>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function LogsSection({
  logs,
  logsIndexed,
}: {
  logs: SearchLogHit[];
  logsIndexed: boolean;
}) {
  return (
    <section className={styles.section} aria-labelledby="logs-section">
      <h2 id="logs-section" className={styles.sectionTitle}>
        Logs
        <span className={styles.sectionCount}>{logs.length}</span>
      </h2>
      {!logsIndexed ? (
        <p className={styles.empty}>
          Log indexing is disabled for some sources — the logs section
          shows matches only from sources with logs indexed.
        </p>
      ) : logs.length === 0 ? (
        <p className={styles.empty}>No log matches.</p>
      ) : (
        <ol className={styles.list}>
          {logs.map((hit) => (
            <li key={hit.log_id} className={styles.card}>
              <header className={styles.cardHeader}>
                <Link
                  to={
                    hit.op_id !== null
                      ? `/sessions/${hit.session_id}?op=${hit.op_id}`
                      : `/sessions/${hit.session_id}`
                  }
                  className={styles.cardLink}
                >
                  <span className={styles.kind}>{hit.severity}</span>
                </Link>
                <span className={styles.cardMeta}>
                  {formatTimestamp(hit.ts)} · session{' '}
                  <code>{hit.session_id.slice(0, 8)}</code>
                </span>
              </header>
              <p className={styles.snippet}>{hit.snippet}</p>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

// Skeleton is exported so the test (if any) can re-use it without
// duplicating the markup. Currently unused at runtime but kept here so
// future test fixtures don't need a separate import.
export const _SkeletonForFutureUse = Skeleton;
